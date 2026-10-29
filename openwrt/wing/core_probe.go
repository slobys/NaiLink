// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/pkg/config_parser"
	dnsmessage "github.com/miekg/dns"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

type RouterCoreProbe struct {
	SteadyMs *int32
	Alive    bool
	Latency  int32
	Message  string
	Tested   time.Time
}

// Use the selected core's SOCKS UDP relay rather than a dae dialer when the
// active core is sing-box. This process has no TUN, routes or user data writes.
func ProbeRouterCoreUDP(ctx context.Context, link string, global daeConfig.Global) (RouterCoreProbe, error) {
	result := RouterCoreProbe{Tested: time.Now()}
	if node, err := routercore.Node(link, "udp-check", routercore.NodeOptions{}); err == nil {
		for _, out := range routercore.ExpandNode(node) {
			if routercore.NativeFeature(out) == "tor" {
				result.Message = "udp_unsupported"
				return result, nil
			}
		}
	}
	select {
	case routerCoreProbeSlots <- struct{}{}:
		defer func() { <-routerCoreProbeSlots }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	conf, names, results, err := routerProbeConfiguration([]string{link}, global)
	if err != nil {
		return result, err
	}
	if len(names) == 0 {
		return results[0], nil
	}
	conf.Routing.Fallback = "probe-0"
	session, err := routerStartProbe(ctx, conf)
	if err != nil {
		return result, err
	}
	defer routerStopProbe(session)
	if len(global.UdpCheckDns) == 0 {
		return result, errors.New("check_target_failed")
	}
	host, port, err := net.SplitHostPort(global.UdpCheckDns[0])
	if err != nil {
		return result, errors.New("check_target_failed")
	}
	if len(global.UdpCheckDns) > 1 {
		host = strings.Trim(global.UdpCheckDns[1], "[]")
	}
	ip := net.ParseIP(host)
	p, e := strconv.Atoi(port)
	if e != nil || ip == nil || p < 1 || p > 65535 {
		return result, errors.New("check_target_failed")
	}
	start := time.Now()
	err = routerSOCKSUDPQuery(ctx, session, ip, p)
	result.Tested = time.Now()
	if err == nil {
		result.Alive = true
		result.Latency = int32(time.Since(start).Milliseconds())
	} else {
		result.Message = "udp_test_failed"
		if ctx.Err() != nil {
			result.Message = "cancelled"
		}
	}
	return result, nil
}

func routerSOCKSUDPQuery(ctx context.Context, session *routerSingBoxSession, target net.IP, port int) error {
	query := new(dnsmessage.Msg)
	query.SetQuestion("www.google.com.", dnsmessage.TypeA)
	answer, err := routerSOCKSDNSExchangeWithin(ctx, session.ports, target, port, query, 6*time.Second)
	if err != nil {
		return err
	}
	if answer.Rcode != dnsmessage.RcodeSuccess || len(answer.Answer) == 0 {
		return errors.New("invalid_dns_reply")
	}
	return nil
}

var routerTorProbeSlot = make(chan struct{}, 1)

var routerCoreProbeSlots = make(chan struct{}, 2)

func routerProbeConfiguration(links []string, global daeConfig.Global) (*daeConfig.Config, map[int]string, []RouterCoreProbe, error) {
	conf := &daeConfig.Config{Global: global, Routing: daeConfig.Routing{Fallback: "direct"}}
	// Manual tests deliberately pin the configured check IP. Domain dialing is
	// for transparent user traffic, not a replacement for the test's fixed target.
	conf.Global.DialMode = "ip"
	manager := currentRouterCoreManager()
	if manager == nil {
		return nil, nil, nil, errors.New("core_not_ready")
	}
	manager.mu.RLock()
	running := manager.conf
	manager.mu.RUnlock()
	if running != nil && len(running.Dns.Upstream) > 0 {
		conf.Dns.Upstream = running.Dns.Upstream[:1]
	}
	names := map[int]string{}
	results := make([]RouterCoreProbe, len(links))
	for i, link := range links {
		name := "probe-" + strconv.Itoa(i)
		fingerprint := ""
		if global.TlsImplementation == "utls" {
			fingerprint = global.UtlsImitate
		}
		node, err := routerParseCoreNode(RouterCoreName(), link, "node:"+name, routercore.NodeOptions{AllowInsecure: global.AllowInsecure, UTLS: fingerprint})
		if err == nil && RouterCoreName() == "mihomo" {
			for _, out := range routercore.ExpandNode(node) {
				if _, err = routercore.MihomoNode(out); err != nil {
					break
				}
			}
		}
		if err != nil {
			results[i] = RouterCoreProbe{Message: err.Error(), Tested: time.Now()}
			continue
		}
		names[i] = name
		conf.Node = append(conf.Node, daeConfig.KeyableString(name+":"+link))
		conf.Group = append(conf.Group, daeConfig.Group{Name: name, Policy: "fixed", Filter: [][]*config_parser.Function{{{Name: "name", Params: []*config_parser.Param{{Val: name}}}}}, FilterAnnotation: [][]*config_parser.Param{{}}})
	}
	return conf, names, results, nil
}
func routerStartProbe(ctx context.Context, conf *daeConfig.Config, hooks ...logrus.Hook) (*routerSingBoxSession, error) {
	log := logrus.New()
	log.SetOutput(io.Discard)
	for _, hook := range hooks {
		log.AddHook(hook)
	}
	session, err := routerStartPrivateProbe(ctx, func() (routerCoreSession, error) {
		return newRouterNativeSession(ctx, log, conf, nil, false, RouterCoreName(), func(profile sbObject, ports sbPorts) {
			users := []sbObject{{"username": "nailink", "password": ports.Secret}}
			var rules []sbObject
			for _, group := range conf.Group {
				users = append(users, sbObject{"username": group.Name, "password": ports.Secret})
				rules = append(rules, sbObject{"auth_user": []string{group.Name}, "action": "route", "outbound": "node:" + group.Name})
			}
			profile["inbounds"].([]sbObject)[0]["users"] = users
			route := profile["route"].(sbObject)
			route["rules"] = append(rules, route["rules"].([]sbObject)...)
		})
	})
	if err != nil {
		return nil, err
	}
	return session.(*routerSingBoxSession), nil
}
func routerStartPrivateProbe(ctx context.Context, start func() (routerCoreSession, error)) (routerCoreSession, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		session, err := start()
		if err != nil {
			return nil, err
		}
		if err = session.Ready(ctx); err == nil {
			return session, nil
		}
		if routerStopProbe(session) != nil {
			return nil, errors.New("core_cleanup_failed")
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err.Error() != "core_ports_unavailable" || attempt == 2 {
			return nil, err
		}
		if manager := currentRouterCoreManager(); manager != nil && manager.log != nil {
			manager.log.WithFields(logrus.Fields{"phase": "private_probe_start", "code": "core_ports_unavailable", "attempt": attempt + 1}).Warn("Node test listener conflicted; retrying with new private ports")
		}
	}
	return nil, errors.New("core_ports_unavailable")
}
func routerStopProbe(session routerCoreSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return session.Stop(ctx)
}
func ProbeRouterCoreNodes(ctx context.Context, links []string, global daeConfig.Global) ([]RouterCoreProbe, error) {
	return ProbeRouterCoreNodesProgress(ctx, links, global, nil)
}

// One private, authenticated core serves the entire bounded test job. Results
// arrive independently; a slow node cannot hold back successful neighbours.
func ProbeRouterCoreNodesProgress(ctx context.Context, links []string, global daeConfig.Global, report func(int, RouterCoreProbe)) ([]RouterCoreProbe, error) {
	select {
	case routerCoreProbeSlots <- struct{}{}:
		defer func() { <-routerCoreProbeSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	results := make([]RouterCoreProbe, len(links))
	var ordinary, extra []string
	var indices, extraIndices, torIndices []int
	for i, link := range links {
		node, err := routercore.Node(link, "classify", routercore.NodeOptions{})
		needs, tor := false, false
		if err == nil {
			for _, entry := range routercore.ExpandNode(node) {
				kind := routercore.NativeFeature(entry)
				needs = needs || kind != ""
				tor = tor || kind == "tor"
			}
		}
		if RouterCoreName() == "mihomo" {
			needs, tor = false, false
			for _, out := range routercore.ExpandNode(node) {
				needs = needs || routercore.NativeFeature(out) == "wireguard"
			}
		}
		if needs && RouterCoreName() != "mihomo" {
			if _, err := routercore.ExtraDirectory(routerExtraRoot); err != nil {
				results[i] = RouterCoreProbe{Tested: time.Now(), Message: "extra_not_installed"}
				if report != nil {
					report(i, results[i])
				}
				continue
			}
		}
		if tor {
			torIndices = append(torIndices, i)
		} else if needs {
			extra = append(extra, link)
			extraIndices = append(extraIndices, i)
		} else {
			ordinary = append(ordinary, link)
			indices = append(indices, i)
		}
	}

	run := func(batch []string, ids []int) error {
		if len(batch) == 0 {
			return nil
		}
		values, err := probeRouterCoreNodesProgress(ctx, batch, global, func(i int, r RouterCoreProbe) {
			results[ids[i]] = r
			if report != nil {
				report(ids[i], r)
			}
		})
		for i, r := range values {
			results[ids[i]] = r
		}
		return err
	}
	if err := run(ordinary, indices); err != nil {
		return results, err
	}
	// Cronet engines and userspace WG devices are eager native resources. Bound
	// their batch size as well as HTTP concurrency; leave ordinary bulk probes
	// on the original single-process path.
	for start := 0; start < len(extra); start += 8 {
		end := min(start+8, len(extra))
		if err := run(extra[start:end], extraIndices[start:end]); err != nil {
			return results, err
		}
	}
	for _, index := range torIndices {
		select {
		case routerTorProbeSlot <- struct{}{}:
		case <-ctx.Done():
			return results, ctx.Err()
		}
		torCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
		values, err := probeRouterCoreNodesProgress(torCtx, []string{links[index]}, global, func(_ int, r RouterCoreProbe) {
			results[index] = r
			if report != nil {
				report(index, r)
			}
		})
		cancel()
		<-routerTorProbeSlot
		if len(values) > 0 {
			results[index] = values[0]
		}
		if err != nil {
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			results[index] = RouterCoreProbe{Tested: time.Now(), Message: "tor_bootstrap_failed"}
			if report != nil {
				report(index, results[index])
			}
		}
	}
	return results, nil
}

func probeRouterCoreNodesProgress(ctx context.Context, links []string, global daeConfig.Global, report func(int, RouterCoreProbe)) ([]RouterCoreProbe, error) {
	conf, names, results, err := routerProbeConfiguration(links, global)
	if err != nil {
		return nil, err
	}
	if report != nil {
		for index, result := range results {
			if _, valid := names[index]; !valid {
				report(index, result)
			}
		}
	}
	if len(names) == 0 {
		return results, nil
	}
	if len(global.TcpCheckUrl) == 0 {
		return nil, errors.New("missing_check_url")
	}
	diagnostics := &routerProbeErrors{codes: map[string]string{}, aliases: map[string]string{}}
	for _, name := range names {
		diagnostics.aliases[routercore.MihomoName("node:"+name)] = name
	}
	session, err := routerStartProbe(ctx, conf, diagnostics)
	if err != nil {
		// Native validation can reject an option not checked by the adapter.
		// Isolate such nodes instead of failing every valid node in the job.
		if (err.Error() == "singbox_configuration_invalid" || err.Error() == "mihomo_configuration_invalid") && ctx.Err() == nil {
			if len(links) > 1 {
				middle := len(links) / 2
				var left, right []RouterCoreProbe
				left, err = probeRouterCoreNodesProgress(ctx, links[:middle], global, report)
				if err != nil {
					return nil, err
				}
				var shifted func(int, RouterCoreProbe)
				if report != nil {
					shifted = func(i int, r RouterCoreProbe) { report(i+middle, r) }
				}
				right, err = probeRouterCoreNodesProgress(ctx, links[middle:], global, shifted)
				return append(left, right...), err
			}
			results[0] = RouterCoreProbe{Tested: time.Now(), Message: "invalid_native_config"}
			if report != nil {
				report(0, results[0])
			}
			return results, nil
		}
		return nil, err
	}
	stop := sync.OnceFunc(func() { routerStopProbe(session) })
	defer stop()
	var workers sync.WaitGroup
	// At most eight HTTP requests (and their two address families) at once.
	// Waiting is outside each node's latency timer, matching dae tests.
	work := make(chan int)
	for range min(8, len(names)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range work {
				name := names[index]
				if ctx.Err() != nil {
					continue
				}
				dialer, err := proxy.SOCKS5("tcp", routercore.SocksAddress(session.ports.Mixed), &proxy.Auth{User: name, Password: session.ports.Secret}, &net.Dialer{Timeout: 8 * time.Second})
				if err != nil {
					results[index] = RouterCoreProbe{Tested: time.Now(), Message: "test_failed"}
				} else {
					if session.hasTor {
						results[index] = routerProbeHTTPTimeout(ctx, dialer.(proxy.ContextDialer), global, 30*time.Second)
					} else {
						results[index] = routerProbeHTTP(ctx, dialer.(proxy.ContextDialer), global)
					}
				}
				if !results[index].Alive {
					results[index].Message = diagnostics.message(name, results[index].Message)
				}
				if report != nil && ctx.Err() == nil {
					report(index, results[index])
				}
			}
		}()
	}
	for index := range names {
		select {
		case work <- index:
		case <-ctx.Done():
		}
	}
	close(work)
	workers.Wait()
	select {
	case <-session.exit:
		if ctx.Err() == nil {
			return results, errors.New("probe_core_exited")
		}
	default:
	}
	// Wait for native stderr to drain before collecting its fixed error codes.
	// No original message, URL, address or credential is retained or returned.
	stop()
	for index, name := range names {
		if !results[index].Alive && results[index].Message != "cancelled" {
			results[index].Message = diagnostics.message(name, results[index].Message)
		}
	}
	if ctx.Err() != nil {
		return results, ctx.Err()
	}
	return results, nil
}

// Match dae's explicit HTTP test: race the resolved IPv4/IPv6 targets using
// fresh connections, preserve Host/TLS SNI, and include the entire request's
// connection/handshake time. Starting the private core is outside this timer.
func routerProbeHTTP(ctx context.Context, dialer proxy.ContextDialer, global daeConfig.Global) RouterCoreProbe {
	return routerProbeHTTPTimeout(ctx, dialer, global, routercore.ManualProbeTimeout)
}

// dae and native adapters use the same URL, method, family race and timer.
func ProbeRouterHTTP(ctx context.Context, dialer proxy.ContextDialer, global daeConfig.Global) RouterCoreProbe {
	return routerProbeHTTP(ctx, dialer, global)
}
func routerProbeHTTPTimeout(ctx context.Context, dialer proxy.ContextDialer, global daeConfig.Global, timeout time.Duration) RouterCoreProbe {
	result := RouterCoreProbe{Tested: time.Now(), Message: "check_target_failed"}
	if len(global.TcpCheckUrl) == 0 {
		return result
	}
	target, err := url.Parse(global.TcpCheckUrl[0])
	if err != nil || target.Hostname() == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return result
	}
	method := global.TcpCheckHttpMethod
	if method == "" {
		method = http.MethodGet
	}
	var addresses []string
	seen := map[string]bool{}
	for _, value := range global.TcpCheckUrl[1:] {
		if ip := net.ParseIP(value); ip != nil && !seen[ip.String()] {
			addresses = append(addresses, ip.String())
			seen[ip.String()] = true
		}
	}
	if len(addresses) == 0 {
		addresses = []string{target.Hostname()}
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	completed := make(chan RouterCoreProbe, len(addresses))
	for _, address := range addresses {
		go func() {
			r := RouterCoreProbe{Message: "check_target_failed"}
			transport := &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1, IdleConnTimeout: time.Second, DialContext: func(ctx context.Context, network, original string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(original)
				if err == nil && host == target.Hostname() {
					original = net.JoinHostPort(address, port)
				}
				return dialer.DialContext(ctx, network, original)
			}}
			defer transport.CloseIdleConnections()
			request, err := http.NewRequestWithContext(probeCtx, method, target.String(), nil)
			if err == nil {
				client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				response, err := client.Do(request)
				if err != nil {
					r.Message = routerCoreProbeFailure(err)
				} else {
					cold := time.Since(started)
					_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4097))
					_ = response.Body.Close()
					valid := response.StatusCode >= 200 && response.StatusCode < 500
					if page := path.Base(target.Path); strings.HasPrefix(page, "generate_") {
						valid = strconv.Itoa(response.StatusCode) == strings.TrimPrefix(page, "generate_")
					}
					if valid {
						r.Alive, r.Message = true, ""
						r.Latency = int32(cold.Milliseconds())
						// Report a second number only when net/http proves reuse.
						// Failed/closed warm connections never invalidate a successful cold test.
						if !response.Close {
							warmCtx, stop := context.WithTimeout(probeCtx, time.Second)
							var reused atomic.Bool
							warmCtx = httptrace.WithClientTrace(warmCtx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }})
							warm, _ := http.NewRequestWithContext(warmCtx, method, target.String(), nil)
							begin := time.Now()
							if reply, e := client.Do(warm); e == nil {
								elapsed := int32(time.Since(begin).Milliseconds())
								if reused.Load() && reply.StatusCode == response.StatusCode {
									r.SteadyMs = &elapsed
								}
								_ = reply.Body.Close()
							}
							stop()
						}
					}
				}
			}
			r.Tested = time.Now()
			completed <- r
		}()
	}
	hasFailure := false
	for range addresses {
		r := <-completed
		if !result.Alive && r.Alive {
			result = r
			cancel()
		} else if !result.Alive && (!hasFailure || routerProbeFailurePriority(r.Message) > routerProbeFailurePriority(result.Message)) {
			result = r
			hasFailure = true
		}
	}
	if ctx.Err() != nil {
		result.Alive, result.Message = false, routerCoreProbeFailure(ctx.Err())
	}
	return result
}

func routerProbeFailurePriority(code string) int {
	switch code {
	case "", "unreachable", "timeout", "cancelled":
		return 0
	case "node_tcp_timeout", "tls_handshake_timeout":
		return 1
	case "check_target_failed":
		return 2
	default:
		return 3
	}
}

type routerProbeErrors struct {
	mu      sync.Mutex
	codes   map[string]string
	aliases map[string]string
}

var routerProbeLogNode = regexp.MustCompile(`\[node:(probe-[0-9]+)\]`)
var routerMihomoProbeLogNode = regexp.MustCompile(`\[TCP\]\s+dial\s+(nk-[a-f0-9]{24})(?:\s|\()`)

func (*routerProbeErrors) Levels() []logrus.Level {
	return []logrus.Level{logrus.ErrorLevel, logrus.WarnLevel}
}
func (p *routerProbeErrors) Fire(entry *logrus.Entry) error {
	node := routerProbeLogNode.FindStringSubmatch(entry.Message)
	name := ""
	if len(node) == 2 {
		name = node[1]
	} else if alias := routerMihomoProbeLogNode.FindStringSubmatch(entry.Message); len(alias) == 2 {
		name = p.aliases[alias[1]]
	}
	if name == "" {
		return nil
	}
	text := strings.ToLower(entry.Message)
	var code string
	switch {
	case strings.Contains(text, "encryptedclienthelloconfiglist contains no valid configs"):
		code = "invalid_ech"
	case strings.Contains(text, "certificate"), strings.Contains(text, "ech rejected"):
		code = "tls_failed"
	case strings.Contains(text, "reality verification failed"), strings.Contains(text, "authentication failed"), strings.Contains(text, "invalid password"):
		code = "authentication_failed"
	case strings.Contains(text, "no such host"), strings.Contains(text, "dns lookup failed"):
		code = "dns_failed"
	case strings.Contains(text, "connection refused"):
		code = "connection_refused"
	case routercore.ProbeProtocolFailure(text) != "":
		code = routercore.ProbeProtocolFailure(text)
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline exceeded"):
		code = "timeout"
	case strings.Contains(text, "connection reset"), strings.Contains(text, "unexpected eof"):
		code = "connection_closed"
	}
	if code != "" {
		p.mu.Lock()
		if p.codes[name] == "" || routerProbeErrorPriority(code) > routerProbeErrorPriority(p.codes[name]) {
			p.codes[name] = code
		}
		p.mu.Unlock()
	}
	return nil
}

func routerProbeErrorPriority(code string) int {
	switch code {
	case "authentication_failed", "tls_failed", "invalid_ech":
		return 6
	case "gateway_failed", "websocket_failed":
		return 5
	case "tls_handshake_timeout", "dns_failed":
		return 4
	case "node_tcp_timeout", "connection_refused":
		return 3
	case "connection_closed":
		return 2
	case "timeout":
		return 1
	}
	return 0
}
func (p *routerProbeErrors) message(name, fallback string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if code := p.codes[name]; code != "" {
		return code
	}
	return fallback
}

func routerCoreProbeFailure(err error) string {
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
		return "timeout"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return "connection_closed"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "unreachable"
}
