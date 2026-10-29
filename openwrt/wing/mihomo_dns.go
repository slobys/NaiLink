// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	dnsmessage "github.com/miekg/dns"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

type mhDNSRule struct {
	match          func(*dnsmessage.Msg) bool
	target, action string
}
type mhDNSBridge struct {
	nodePolicies                    routercore.NodeDNS
	nodeBootstrapAddress            string
	log                             *logrus.Logger
	lastLog                         atomic.Int64
	ctx                             context.Context
	cancel                          context.CancelFunc
	servers                         []*dnsmessage.Server
	reservations                    []io.Closer
	upstreams                       map[string]sbObject
	proxyPorts                      map[string]int
	rules                           []mhDNSRule
	final, bootstrap, iface, secret string
	slots                           chan struct{}
	pending                         chan struct{}
	wire                            mhDNSWirePool
	httpMu                          sync.Mutex
	httpClients                     map[int]*http.Client
	httpClosed                      bool
	closeOnce                       sync.Once
}

func (b *mhDNSBridge) Close() {
	b.closeOnce.Do(func() {
		b.cancel()
		b.wire.close()
		b.closeHTTP()
		b.releaseReservations()
		for _, s := range b.servers {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.ShutdownContext(ctx)
			if s.Listener != nil {
				_ = s.Listener.Close()
			}
			if s.PacketConn != nil {
				_ = s.PacketConn.Close()
			}
			cancel()
		}
	})
}
func (b *mhDNSBridge) releaseReservations() {
	for _, c := range b.reservations {
		_ = c.Close()
	}
	b.reservations = nil
}
func (b *mhDNSBridge) warnDNS(code string) {
	if b.log == nil {
		return
	}
	now := time.Now().Unix()
	last := b.lastLog.Load()
	if now > last && b.lastLog.CompareAndSwap(last, now) {
		core, phase := "mihomo", "dns_bridge"
		if len(b.nodePolicies) > 0 {
			core, phase = RouterCoreName(), "node_dns"
		}
		b.log.WithFields(logrus.Fields{"core": core, "phase": phase, "code": code}).Warn("DNS forwarding failed")
	}
}
func mhDNSCompile(rule sbObject, sets map[string][]sbObject) (func(*dnsmessage.Msg) bool, error) {
	var conditions []func(*dnsmessage.Msg) bool
	if rule["type"] == "logical" {
		var children []func(*dnsmessage.Msg) bool
		for _, child := range mhObjects(rule["rules"]) {
			f, e := mhDNSCompile(child, sets)
			if e != nil {
				return nil, e
			}
			children = append(children, f)
		}
		or := rule["mode"] == "or"
		conditions = append(conditions, func(q *dnsmessage.Msg) bool {
			for _, f := range children {
				if f(q) == or {
					return or
				}
			}
			return !or
		})
	} else {
		var domain []func(string) bool
		full, suffix := map[string]bool{}, map[string]bool{}
		for _, key := range []string{"domain", "domain_suffix", "domain_keyword", "domain_regex"} {
			for _, v := range mhStrings(rule[key]) {
				value := strings.ToLower(strings.TrimSuffix(v, "."))
				switch key {
				case "domain":
					full[value] = true
				case "domain_suffix":
					value = strings.TrimPrefix(value, ".")
					suffix[value] = true
				case "domain_keyword":
					domain = append(domain, func(name string) bool { return strings.Contains(name, value) })
				case "domain_regex":
					rx, e := regexp.Compile(v)
					if e != nil {
						return nil, errors.New("invalid_geodata")
					}
					domain = append(domain, rx.MatchString)
				}
			}
		}
		if len(domain) > 0 || len(full) > 0 || len(suffix) > 0 {
			conditions = append(conditions, func(q *dnsmessage.Msg) bool {
				if len(q.Question) != 1 {
					return false
				}
				name := strings.ToLower(strings.TrimSuffix(q.Question[0].Name, "."))
				if full[name] {
					return true
				}
				for candidate := name; candidate != ""; {
					if suffix[candidate] {
						return true
					}
					dot := strings.IndexByte(candidate, '.')
					if dot < 0 {
						break
					}
					candidate = candidate[dot+1:]
				}
				for _, f := range domain {
					if f(name) {
						return true
					}
				}
				return false
			})
		}
		if tags := mhStrings(rule["rule_set"]); len(tags) > 0 {
			var matches []func(*dnsmessage.Msg) bool
			for _, tag := range tags {
				list, ok := sets[tag]
				if !ok {
					return nil, errors.New("invalid_geodata")
				}
				for _, child := range list {
					f, e := mhDNSCompile(child, sets)
					if e != nil {
						return nil, e
					}
					matches = append(matches, f)
				}
			}
			conditions = append(conditions, func(q *dnsmessage.Msg) bool {
				for _, f := range matches {
					if f(q) {
						return true
					}
				}
				return false
			})
		}
		if types := mhStrings(rule["query_type"]); len(types) > 0 {
			allowed := map[uint16]bool{}
			for _, typ := range types {
				n, ok := dnsmessage.StringToType[strings.ToUpper(typ)]
				if !ok {
					v, e := strconv.ParseUint(typ, 10, 16)
					if e != nil {
						return nil, errors.New("mihomo_dns_unsupported")
					}
					n = uint16(v)
				}
				allowed[n] = true
			}
			conditions = append(conditions, func(q *dnsmessage.Msg) bool { return len(q.Question) == 1 && allowed[q.Question[0].Qtype] })
		}
		for key := range rule {
			switch key {
			case "domain", "domain_suffix", "domain_keyword", "domain_regex", "rule_set", "query_type", "action", "server", "rcode", "invert":
			default:
				return nil, errors.New("mihomo_dns_unsupported")
			}
		}
	}
	invert := rule["invert"] == true
	return func(q *dnsmessage.Msg) bool {
		result := true
		for _, f := range conditions {
			result = result && f(q)
		}
		return result != invert
	}, nil
}
func startMihomoDNS(profile sbObject, ports sbPorts, logs ...*logrus.Logger) (_ *mhDNSBridge, result error) {
	return startMihomoDNSWithPolicies(profile, ports, nil, logs...)
}
func startMihomoDNSWithPolicies(profile sbObject, ports sbPorts, policies routercore.NodeDNS, logs ...*logrus.Logger) (_ *mhDNSBridge, result error) {
	canonical := profile["_nailink_dns"].(sbObject)
	delete(profile, "_nailink_dns")
	dns := canonical["dns"].(sbObject)
	route := canonical["route"].(sbObject)
	ctx, cancel := context.WithCancel(context.Background())
	b := &mhDNSBridge{ctx: ctx, cancel: cancel, slots: make(chan struct{}, 32), pending: make(chan struct{}, 64), upstreams: map[string]sbObject{}, proxyPorts: map[string]int{}, secret: ports.Secret, final: mhString(dns["final"]), iface: mhString(route["default_interface"])}
	if len(logs) > 0 {
		b.log = logs[0]
	}
	b.nodePolicies = policies
	defer func() {
		if result != nil {
			b.Close()
		}
	}()
	if b.iface == "" && route["auto_detect_interface"] == true {
		iface, e := routerMainWANInterface()
		if e != nil {
			return nil, e
		}
		b.iface = iface
	}
	b.bootstrap = mhString(route["default_domain_resolver"].(sbObject)["server"])
	sets := map[string][]sbObject{}
	for _, set := range mhObjects(route["rule_set"]) {
		sets[mhString(set["tag"])] = mhObjects(set["rules"])
	}
	for _, rule := range mhObjects(dns["rules"]) {
		match, e := mhDNSCompile(rule, sets)
		if e != nil {
			return nil, e
		}
		b.rules = append(b.rules, mhDNSRule{match, mhString(rule["server"]), mhString(rule["action"])})
	}
	listeners := mhObjects(profile["listeners"])
	for _, server := range mhObjects(dns["servers"]) {
		b.upstreams[mhString(server["tag"])] = server
		detour := mhString(server["detour"])
		if detour == "" || detour == "direct" || b.proxyPorts[detour] != 0 {
			continue
		}
		if len(b.proxyPorts) >= 16 {
			return nil, errors.New("mihomo_dns_unsupported")
		}
		tcp, udp, e := routerLoopbackPair(net.Listen, net.ListenPacket)
		if e != nil {
			return nil, e
		}
		port := tcp.Addr().(*net.TCPAddr).Port
		b.reservations = append(b.reservations, tcp, udp)
		b.proxyPorts[detour] = port
		listeners = append(listeners, sbObject{"name": "dns-" + routercore.MihomoName(detour), "type": "mixed", "listen": "127.0.0.1", "port": port, "udp": true, "proxy": routercore.MihomoName(detour), "users": []sbObject{{"username": "nailink", "password": ports.Secret}}})
	}
	profile["listeners"] = listeners
	start := func(bootstrap bool) (int, error) {
		tcp, udp, e := routerLoopbackPair(net.Listen, net.ListenPacket)
		if e != nil {
			return 0, e
		}
		port := tcp.Addr().(*net.TCPAddr).Port
		handler := dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
			reply := new(dnsmessage.Msg)
			reply.SetRcode(q, dnsmessage.RcodeServerFailure)
			if len(q.Question) != 1 {
				_ = w.WriteMsg(reply)
				return
			}
			ctx, stop := context.WithTimeout(b.ctx, 6*time.Second)
			defer stop()
			// Bound both active queries and waiters, while allowing short browser bursts.
			select {
			case b.pending <- struct{}{}:
				defer func() { <-b.pending }()
			default:
				b.warnDNS("dns_overloaded")
				_ = w.WriteMsg(reply)
				return
			}
			select {
			case b.slots <- struct{}{}:
				defer func() { <-b.slots }()
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					b.warnDNS("dns_queue_timeout")
				}
				_ = w.WriteMsg(reply)
				return
			}
			target := b.final
			if bootstrap {
				target = b.bootstrap
			} else {
				for _, r := range b.rules {
					if r.match(q) {
						if r.action == "predefined" {
							reply.SetRcode(q, dnsmessage.RcodeSuccess)
							_ = w.WriteMsg(reply)
							return
						}
						if r.action == "reject" {
							reply.SetRcode(q, dnsmessage.RcodeRefused)
							_ = w.WriteMsg(reply)
							return
						}
						target = r.target
						break
					}
				}
			}
			var answer *dnsmessage.Msg
			var e error
			if resolvers := b.nodePolicies[routercore.NodeDNSHost(q.Question[0].Name)]; !bootstrap && len(resolvers) > 0 {
				e = errors.New("dns_https_failed")
				for _, resolver := range resolvers {
					attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
					answer, e = b.exchange(attempt, q, nodeDNSUpstream(resolver), true)
					cancel()
					if e == nil {
						break
					}
				}
			} else {
				answer, e = b.exchange(ctx, q, b.upstreams[target], bootstrap)
			}
			if e == nil {
				reply = answer
			} else if !errors.Is(e, context.Canceled) {
				code := routerCoreProbeFailure(e)
				switch e.Error() {
				case "missing_dns_upstream", "missing_dns_detour", "invalid_dns_reply", "invalid_dns_upstream", "mihomo_dns_unsupported", "dns_https_failed":
					code = e.Error()
				}
				if errors.Is(e, syscall.ENODEV) {
					code = "dns_interface_missing"
				}
				if errors.Is(e, syscall.EPERM) || errors.Is(e, syscall.EACCES) {
					code = "dns_permission_failed"
				}
				b.warnDNS(code)
			}
			_ = w.WriteMsg(reply)
		})
		for _, s := range []*dnsmessage.Server{{Listener: tcp, Net: "tcp", Handler: handler, ReadTimeout: 6 * time.Second, WriteTimeout: 6 * time.Second, MaxTCPQueries: 16}, {PacketConn: udp, Net: "udp", Handler: handler, UDPSize: 4096, ReadTimeout: 6 * time.Second, WriteTimeout: 6 * time.Second}} {
			b.servers = append(b.servers, s)
			go func(s *dnsmessage.Server) { _ = s.ActivateAndServe() }(s)
		}
		return port, nil
	}
	normal, e := start(false)
	if e != nil {
		return nil, e
	}
	bootstrap, e := start(true)
	if e != nil {
		return nil, e
	}
	b.nodeBootstrapAddress = routercore.SocksAddress(bootstrap)
	d := profile["dns"].(sbObject)
	d["nameserver"] = []string{"udp://" + routercore.SocksAddress(normal)}
	d["default-nameserver"] = []string{"udp://" + routercore.SocksAddress(bootstrap)}
	d["proxy-server-nameserver"] = []string{"udp://" + routercore.SocksAddress(bootstrap)}
	return b, nil
}
func (b *mhDNSBridge) exchange(ctx context.Context, q *dnsmessage.Msg, server sbObject, bootstrap bool) (*dnsmessage.Msg, error) {
	if server == nil {
		return nil, errors.New("missing_dns_upstream")
	}
	typ := mhString(server["type"])
	host := mhString(server["server"])
	port := mhNumber(server["server_port"])
	if typ == "local" {
		config, e := dnsmessage.ClientConfigFromFile("/etc/resolv.conf")
		if e != nil || len(config.Servers) == 0 {
			return nil, errors.New("missing_dns_upstream")
		}
		host = config.Servers[0]
		port, _ = strconv.Atoi(config.Port)
		typ = "udp"
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	detour := mhString(server["detour"])
	if bootstrap {
		detour = "direct"
	}
	proxyPort := b.proxyPorts[detour]
	if detour != "" && detour != "direct" && proxyPort == 0 {
		return nil, errors.New("missing_dns_detour")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	// Match the managed TUN output mark, including for private test cores.
	// Binding a WAN alone does not bypass nftables auto-redirect rules.
	dialer.Control = func(_, address string, raw syscall.RawConn) error {
		host, _, _ := net.SplitHostPort(address)
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		var e error
		if err := raw.Control(func(fd uintptr) {
			e = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, 0x4e4102)
			if e == nil && b.iface != "" && b.iface != "auto" {
				e = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, b.iface)
			}
		}); err != nil {
			return err
		}
		return e
	}

	dial := dialer.DialContext
	if typ == "https" && len(b.nodePolicies) > 0 {
		dial = b.nodeDNSDialer(dialer).DialContext
	}

	if proxyPort > 0 {
		p, e := proxy.SOCKS5("tcp", routercore.SocksAddress(proxyPort), &proxy.Auth{User: "nailink", Password: b.secret}, &net.Dialer{Timeout: 5 * time.Second})
		if e != nil {
			return nil, e
		}
		dial = p.(proxy.ContextDialer).DialContext
	}
	var answer *dnsmessage.Msg
	var err error
	switch typ {
	case "udp":
		if proxyPort > 0 {
			answer, err = routerSOCKSDNSExchange(ctx, sbPorts{Mixed: proxyPort, Secret: b.secret}, net.ParseIP(host), port, q)
		} else {
			answer, err = b.exchangeWire(ctx, q, mhDNSKey(typ, address, proxyPort), func(ctx context.Context) (net.Conn, error) { return dialer.DialContext(ctx, "udp", address) })
		}
	case "tcp", "tls":
		answer, err = b.exchangeWire(ctx, q, mhDNSKey(typ, address, proxyPort), func(ctx context.Context) (net.Conn, error) {
			conn, e := dial(ctx, "tcp", address)
			if e != nil {
				return nil, e
			}
			if typ != "tls" {
				return conn, nil
			}
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()
			deadline := time.Now().Add(5 * time.Second)
			if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
				deadline = d
			}
			_ = conn.SetDeadline(deadline)
			tc := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host})
			if e = tc.HandshakeContext(ctx); e != nil {
				_ = conn.Close()
				return nil, e
			}
			return tc, nil
		})
	case "https":
		answer, err = b.exchangeHTTPS(ctx, q, address, mhString(server["path"]), proxyPort, dial)
	default:
		return nil, errors.New("mihomo_dns_unsupported")
	}
	if err != nil {
		return nil, err
	}
	if !mhValidDNSAnswer(q, answer) {
		return nil, errors.New("invalid_dns_reply")
	}
	if typ == "udp" && answer.Truncated {
		next := sbObject{}
		for k, v := range server {
			next[k] = v
		}
		next["type"] = "tcp"
		return b.exchange(ctx, q, next, bootstrap)
	}
	return answer, nil
}
func mhValidDNSAnswer(q, a *dnsmessage.Msg) bool {
	return a != nil && a.Response && a.Opcode == q.Opcode && a.Id == q.Id && len(a.Question) == 1 && len(q.Question) == 1 && strings.EqualFold(a.Question[0].Name, q.Question[0].Name) && a.Question[0].Qtype == q.Question[0].Qtype && a.Question[0].Qclass == q.Question[0].Qclass
}

// Shared SOCKS5 UDP DNS exchange. The destination and question are verified;
// failed proxied DNS never silently falls back to a direct or system resolver.
func routerSOCKSDNSExchange(ctx context.Context, ports sbPorts, target net.IP, port int, q *dnsmessage.Msg) (*dnsmessage.Msg, error) {
	return routerSOCKSDNSExchangeWithin(ctx, ports, target, port, q, 5*time.Second)
}

func routerSOCKSDNSExchangeWithin(ctx context.Context, ports sbPorts, target net.IP, port int, q *dnsmessage.Msg, budget time.Duration) (*dnsmessage.Msg, error) {
	if target == nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid_dns_upstream")
	}
	tcp, err := (&net.Dialer{}).DialContext(ctx, "tcp4", routercore.SocksAddress(ports.Mixed))
	if err != nil {
		return nil, err
	}
	defer tcp.Close()
	stop := context.AfterFunc(ctx, func() { _ = tcp.Close() })
	defer stop()
	deadline := time.Now().Add(budget)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = tcp.SetDeadline(deadline)
	if _, err = tcp.Write([]byte{5, 1, 2}); err != nil {
		return nil, err
	}
	var reply [2]byte
	if _, err = io.ReadFull(tcp, reply[:]); err != nil || reply != [2]byte{5, 2} {
		return nil, errors.New("socks_auth_failed")
	}
	user := "nailink"
	auth := append([]byte{1, byte(len(user))}, []byte(user)...)
	auth = append(auth, byte(len(ports.Secret)))
	auth = append(auth, []byte(ports.Secret)...)
	if _, err = tcp.Write(auth); err != nil {
		return nil, err
	}
	if _, err = io.ReadFull(tcp, reply[:]); err != nil || reply != [2]byte{1, 0} {
		return nil, errors.New("socks_auth_failed")
	}
	if _, err = tcp.Write([]byte{5, 3, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return nil, err
	}
	var header [4]byte
	if _, err = io.ReadFull(tcp, header[:]); err != nil || header[0] != 5 || header[1] != 0 {
		return nil, errors.New("udp_unsupported")
	}
	length := 4
	if header[3] == 4 {
		length = 16
	} else if header[3] != 1 {
		return nil, errors.New("invalid_udp_relay")
	}
	address := make([]byte, length+2)
	if _, err = io.ReadFull(tcp, address); err != nil {
		return nil, err
	}
	ip := net.IP(address[:length])
	if !ip.IsUnspecified() && !ip.IsLoopback() {
		return nil, errors.New("invalid_udp_relay")
	}
	relayPort := int(binary.BigEndian.Uint16(address[length:]))
	if relayPort == 0 {
		relayPort = ports.Mixed
	}
	udp, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: relayPort})
	if err != nil {
		return nil, err
	}
	defer udp.Close()
	stopUDP := context.AfterFunc(ctx, func() { _ = udp.Close() })
	defer stopUDP()
	_ = udp.SetDeadline(deadline)
	data, err := q.Pack()
	if err != nil {
		return nil, err
	}
	packet := []byte{0, 0, 0}
	if ip4 := target.To4(); ip4 != nil {
		packet = append(packet, 1)
		packet = append(packet, ip4...)
	} else {
		packet = append(packet, 4)
		packet = append(packet, target.To16()...)
	}
	packet = binary.BigEndian.AppendUint16(packet, uint16(port))
	packet = append(packet, data...)
	if _, err = udp.Write(packet); err != nil {
		return nil, err
	}
	udpSize := int(dnsmessage.MinMsgSize)
	if opt := q.IsEdns0(); opt != nil && int(opt.UDPSize()) > udpSize {
		udpSize = int(opt.UDPSize())
	}
	// The negotiated DNS size plus the longest SOCKS IP envelope. Ordinary
	// queries no longer allocate a maximum-size datagram for each exchange.
	buffer := make([]byte, udpSize+22)
	if mhDNSReadOnly(q) {
		retryAt := time.Now().Add(500 * time.Millisecond)
		if deadline.Before(retryAt) {
			retryAt = deadline
		}
		_ = udp.SetReadDeadline(retryAt)
	}
	n, err := udp.Read(buffer)
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() && mhDNSReadOnly(q) && ctx.Err() == nil && time.Now().Before(deadline) {
		// A cold userspace tunnel may lose its first UDP packet. Resend a
		// read-only query ONCE on the same authenticated relay, target and ID,
		// inside the original deadline. Never retry invalid DNS, updates,
		// transfers or cancellation, and never change the upstream or route.
		_ = udp.SetReadDeadline(deadline)
		if _, err = udp.Write(packet); err == nil {
			n, err = udp.Read(buffer)
		}
	}
	if err != nil {
		return nil, err
	}
	if n < 10 || buffer[0] != 0 || buffer[1] != 0 || buffer[2] != 0 {
		return nil, errors.New("invalid_udp_reply")
	}
	offset := 0
	var responseIP net.IP
	switch buffer[3] {
	case 1:
		offset = 10
		responseIP = net.IP(buffer[4:8])
	case 4:
		if n < 22 {
			return nil, errors.New("invalid_udp_reply")
		}
		offset = 22
		responseIP = net.IP(buffer[4:20])
	default:
		return nil, errors.New("invalid_udp_reply")
	}
	if offset >= n || !responseIP.Equal(target) || int(binary.BigEndian.Uint16(buffer[offset-2:offset])) != port {
		return nil, errors.New("invalid_udp_reply")
	}
	answer := new(dnsmessage.Msg)
	if answer.Unpack(buffer[offset:n]) != nil || !mhValidDNSAnswer(q, answer) {
		return nil, errors.New("invalid_dns_reply")
	}
	return answer, nil
}
