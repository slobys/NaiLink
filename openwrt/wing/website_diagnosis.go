// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeCommon "github.com/daeuniverse/dae/common"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/control"
)

type websiteObservationKey struct{}

type websiteObservation struct {
	mu          sync.Mutex
	ms          map[string]int64
	group, node string
}

func (o *websiteObservation) record(stage string, duration time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ms[stage] = duration.Milliseconds()
}

func (o *websiteObservation) route(group, node string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.group, o.node = group, node
}

type websiteDiagnosis struct {
	routerConnectivitySample
	Core  string           `json:"core,omitempty"`
	Group string           `json:"group,omitempty"`
	Node  string           `json:"node,omitempty"`
	Ms    map[string]int64 `json:"ms,omitempty"`
}

var websiteDiagnosisRunning atomic.Bool

func RouterDiagnoseWebsite(ctx context.Context, target string) string {
	value, _ := json.Marshal(routerDiagnoseWebsite(ctx, target))
	return string(value)
}

func routerDiagnoseWebsite(ctx context.Context, target string) websiteDiagnosis {
	failure := func(code string) websiteDiagnosis {
		return websiteDiagnosis{routerConnectivitySample: routerConnectivitySample{Status: "error", Code: code}}
	}
	var host string
	switch target {
	case "chatgpt":
		host = "chatgpt.com"
	case "gemini":
		host = "gemini.google.com"
	default:
		return failure("invalid_target")
	}
	key, state := routerConnectivitySnapshot()
	if state != "ready" {
		return failure("core_not_ready")
	}
	if !websiteDiagnosisRunning.CompareAndSwap(false, true) {
		return failure("busy")
	}
	defer websiteDiagnosisRunning.Store(false)
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	observation := &websiteObservation{ms: map[string]int64{}}
	ctx = context.WithValue(ctx, websiteObservationKey{}, observation)
	ctx = control.WithRouterRouteObserver(ctx, observation.route)
	transport := HttpTransport.Clone()
	transport.Proxy = nil
	transport.TLSHandshakeTimeout = 4 * time.Second
	transport.ResponseHeaderTimeout = 5 * time.Second
	defer transport.CloseIdleConnections()
	dial := transport.DialContext
	routeFound := make(chan struct{})
	var routeStarted atomic.Bool
	transport.DialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		start := time.Now()
		conn, err := dial(dialCtx, network, address)
		observation.record("connect", time.Since(start))
		if err == nil && key.core != "dae" {
			if local, ok := conn.LocalAddr().(*net.TCPAddr); ok && local.IP.IsLoopback() && routeStarted.CompareAndSwap(false, true) {
				go func() {
					defer close(routeFound)
					observeNativeWebsite(ctx, key, local.Port, host, observation)
				}()
			}
		}
		return conn, err
	}
	start := time.Now()
	var handshake time.Time
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		TLSHandshakeStart: func() { handshake = time.Now() },
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			observation.record("tls", time.Since(handshake))
		},
		GotFirstResponseByte: func() { observation.record("ttfb", time.Since(start)) },
	})
	sample := routerCheckConnectivityHTTP(ctx, &http.Client{Transport: transport}, "https://"+host+"/")
	if routeStarted.Load() {
		select {
		case <-routeFound:
		case <-ctx.Done():
		}
	}
	if key.core != "dae" {
		// Measure the same native DNS listener separately, after the website
		// request, so it neither warms the request nor changes its TTFB.
		if session, conf := nativeWebsiteSession(key); session != nil {
			bounded, done := context.WithTimeout(ctx, 1500*time.Millisecond)
			start := time.Now()
			if routerVerifyNativeDNS(bounded, routercore.SocksAddress(session.ports.DNS), host, conf.Dns.IpVersionPrefer) {
				observation.record("nativeDns", time.Since(start))
			}
			done()
		}
	}
	current, state := routerConnectivitySnapshot()
	if key != current || state != "ready" {
		return failure("configuration_changed")
	}
	observation.mu.Lock()
	defer observation.mu.Unlock()
	timings := make(map[string]int64, len(observation.ms))
	for name, ms := range observation.ms {
		timings[name] = ms
	}
	// The daemon lookup is part of the custom DialContext; report it
	// separately where it is observable. In SOCKS it stays inside the core.
	if dns, ok := timings["dns"]; ok {
		timings["connect"] -= dns
	}
	return websiteDiagnosis{sample, key.core, observation.group, observation.node, timings}
}

func nativeWebsiteSession(key routerConnectivityKey) (*routerSingBoxSession, *daeConfig.Config) {
	m := key.manager
	m.mu.RLock()
	defer m.mu.RUnlock()
	original, ok := m.active.(*routerSingBoxSession)
	conf := m.conf
	if !ok || conf == nil || key.config.Value() != conf || original.Done() != key.session {
		return nil, nil
	}
	return original.current(), conf
}

func observeNativeWebsite(ctx context.Context, key routerConnectivityKey, port int, host string, observation *websiteObservation) {
	session, conf := nativeWebsiteSession(key)
	if session == nil {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	var chain []string
	if err := session.readAPI(bounded, "/connections", 16<<20, func(r io.Reader) error {
		var err error
		chain, err = websiteConnectionChain(r, port, host)
		return err
	}); err == nil {
		observation.route(websiteOutlet(conf, key.core, chain))
	}
}

func websiteConnectionChain(reader io.Reader, port int, host string) ([]string, error) {
	decoder := json.NewDecoder(reader)
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid connection snapshot")
	}
	var chain []string
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if field != "connections" {
			if err := routerSkipJSON(decoder, 0); err != nil {
				return nil, err
			}
			continue
		}
		token, err := decoder.Token()
		if err != nil || token != json.Delim('[') {
			return nil, errors.New("connection list unavailable")
		}
		for decoder.More() {
			var entry struct {
				Metadata struct {
					SourceIP   string
					SourcePort json.RawMessage
					Host       string
				}
				Chains []string
			}
			if err := decoder.Decode(&entry); err != nil {
				return nil, err
			}
			if net.ParseIP(entry.Metadata.SourceIP).IsLoopback() &&
				strings.Trim(string(entry.Metadata.SourcePort), `"`) == strconv.Itoa(port) &&
				strings.EqualFold(entry.Metadata.Host, host) {
				chain = entry.Chains
			}
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return chain, routerJSONEnd(decoder)
}

func websiteOutlet(conf *daeConfig.Config, core string, chain []string) (group, node string) {
	tag := func(value string) string {
		if core == "mihomo" {
			return routercore.MihomoName(value)
		}
		return value
	}
	for _, entry := range chain {
		if entry == tag("direct") && group == "" {
			group = "direct"
		}
		for _, candidate := range conf.Group {
			if entry == tag("group:"+candidate.Name) {
				group = candidate.Name
			}
		}
		for _, candidate := range conf.Node {
			name, _ := daeCommon.GetTagFromLinkLikePlaintext(string(candidate))
			if node == "" && name != "" && entry == tag("node:"+name) {
				node = name
			}
		}
	}
	return websiteLabel(group), websiteLabel(node)
}

func websiteLabel(label string) string {
	if strings.Contains(label, "://") {
		return ""
	}
	name := []rune(label)
	if len(name) > 80 {
		name = name[:80]
	}
	return string(name)
}
