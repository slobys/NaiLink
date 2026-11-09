// SPDX-License-Identifier: AGPL-3.0-only
package dialer

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	outboundCommon "github.com/daeuniverse/outbound/common"
	proxyurl "github.com/daeuniverse/outbound/common/url"
	D "github.com/daeuniverse/outbound/dialer"
	httpdialer "github.com/daeuniverse/outbound/dialer/http"
	"github.com/daeuniverse/outbound/netproxy"
	"github.com/daeuniverse/outbound/pool"
	"github.com/daeuniverse/outbound/protocol/hysteria2/udphop"
	"golang.org/x/crypto/blake2b"
)

func routerIsTwoHopLink(link string) bool {
	_, raw := outboundCommon.GetTagFromLinkLikePlaintext(link)
	return strings.Count(raw, "->") == 1
}

// Keep the first hop on dae's usual marked and DNS-aware dialing path. The
// landing hop only dials through that path, including for health probes.
func routerChainDialer(ctx context.Context, global *GlobalOption, instance InstanceOption, link, subscription string, cache *ProxyIpCache) (result *Dialer, err error) {
	name, raw := outboundCommon.GetTagFromLinkLikePlaintext(link)
	landingLink, entryLink, _ := strings.Cut(raw, "->")
	landingLink, entryLink = strings.TrimSpace(landingLink), strings.TrimSpace(entryLink)
	if !strings.Contains(landingLink, "://") || !strings.Contains(entryLink, "://") {
		return nil, errors.New("invalid_proxy_chain")
	}
	// Transport pools key on the address and namespace. Give each hop its
	// own namespace so a landing connection can never reuse a direct socket.
	entryOption := *global
	entryOption.TransportCacheNamespace = newTransportCacheNamespace()
	entry, err := routerSingleHopDialer(ctx, &entryOption, InstanceOption{DisableCheck: true}, entryLink, subscription, cache, true)
	if err != nil {
		CleanupTransportCacheNamespace(entryOption.TransportCacheNamespace)
		return nil, errors.New("invalid_proxy_chain")
	}
	chain := &routerChainTransport{entry: entry, landingNamespace: newTransportCacheNamespace()}
	defer func() {
		if result == nil {
			_ = chain.Close()
		}
	}()
	normalized, base, err := routerProtocolOptions(normalizeShadowTLSPluginOptions(landingLink), entry, global.UDPHopInterval)
	if err != nil {
		return nil, errors.New("invalid_proxy_chain")
	}
	extra, err := routerProtocolExtraOption(normalized, global.ExtraOption)
	if err != nil {
		return nil, errors.New("invalid_proxy_chain")
	}
	landing, property, err := D.NewNetproxyDialerFromLink(scopeTransportCacheDialer(base, chain.landingNamespace), &extra, normalized)
	if err != nil {
		return nil, errors.New("invalid_proxy_chain")
	}
	chain.Dialer = routerProtocolDialer(normalized, landing)
	if name == "" {
		name = entry.Property().Name + " → " + property.Name
	}
	p := &Property{
		Property:        D.Property{Name: name, Address: entry.Property().Address, Protocol: "chain", Link: raw},
		SubscriptionTag: subscription,
	}
	result = NewDialerContext(ctx, chain, global, instance, p)
	result.stickyIpDialer = entry.stickyIpDialer
	return result, nil
}

// A plain HTTP dialer may rewrite HTTP-looking payloads as forward requests.
// The chain entry must tunnel opaque bytes, including the next hop's headers.
func routerChainProtocolDialer(base netproxy.Dialer, option *D.ExtraOption, link string, forceConnect bool) (netproxy.Dialer, *D.Property, error) {
	if !forceConnect || !strings.HasPrefix(link, "http://") {
		return D.NewNetproxyDialerFromLink(base, option, link)
	}
	proxy, err := httpdialer.ParseHTTPURL(link)
	if err != nil {
		return nil, nil, err
	}
	address := net.JoinHostPort(proxy.Server, strconv.Itoa(proxy.Port))
	d := &routerHTTPConnectDialer{next: base, address: address}
	if proxy.Username != "" {
		d.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(proxy.Username+":"+proxy.Password))
	}
	return d, &D.Property{Name: proxy.Name, Address: address, Protocol: "http", Link: link}, nil
}

type routerHTTPConnectDialer struct {
	next          netproxy.Dialer
	address, auth string
}

func (d *routerHTTPConnectDialer) DialContext(ctx context.Context, network, address string) (netproxy.Conn, error) {
	magic, err := netproxy.ParseMagicNetwork(network)
	if err != nil || magic.Network != "tcp" {
		return nil, netproxy.UnsupportedTunnelTypeError
	}
	if _, _, err := net.SplitHostPort(address); err != nil || strings.ContainsAny(address, "\r\n\t ") {
		return nil, errors.New("invalid CONNECT destination")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := d.next.DialContext(ctx, network, d.address)
	if err != nil {
		return nil, err
	}
	ready := false
	defer func() {
		if !ready {
			_ = conn.Close()
		}
	}()
	restoreDeadline, err := netproxy.ApplyConnDeadlineFromContext(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer restoreDeadline()
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if d.auth != "" {
		req.Header.Set("Proxy-Authorization", d.auth)
	}
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP entry CONNECT status %d", response.StatusCode)
	}
	ready = true
	return &routerHTTPConnectConn{Conn: conn, reader: reader}, nil
}

type routerHTTPConnectConn struct {
	netproxy.Conn
	reader *bufio.Reader
}

func (c *routerHTTPConnectConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

type routerChainTransport struct {
	netproxy.Dialer
	entry            *Dialer
	landingNamespace string
	closeOnce        sync.Once
}

func (d *routerChainTransport) Close() error {
	d.closeOnce.Do(func() {
		if closer, ok := d.Dialer.(io.Closer); ok {
			_ = closer.Close()
		}
		CleanupTransportCacheNamespace(d.landingNamespace)
		_ = d.entry.Close()
		CleanupTransportCacheNamespace(d.entry.TransportCacheNamespace)
	})
	return nil
}

// The pinned VLESS reader hard-codes AllowInsecure=false. Pass an explicit
// node preference through a private copy of ExtraOption, never the shared
// global options, so tests, health checks and real connections agree.
func routerProtocolExtraOption(link string, original D.ExtraOption) (D.ExtraOption, error) {
	if !strings.HasPrefix(link, "vless://") {
		return original, nil
	}
	u, err := proxyurl.Parse(link)
	if err != nil {
		return original, fmt.Errorf("invalid protocol URL")
	}
	var selected *bool
	for _, key := range []string{"insecure", "allowInsecure", "allow_insecure", "skip-cert-verify"} {
		for _, value := range u.Query()[key] {
			b, e := strconv.ParseBool(value)
			if e != nil || (selected != nil && *selected != b) {
				return original, fmt.Errorf("invalid or conflicting TLS verification option")
			}
			selected = &b
		}
	}
	if selected != nil {
		original.AllowInsecure = *selected
	}
	return original, nil
}

// Protocol-specific interruption before Close is applied by routerStreamConn
// for SS/VMess. Do not set deadlines on every HTTP transport here: a WebSocket
// dialer may still be writing control data, and its write deadline is not safe
// to mutate concurrently. Its Close already interrupts the underlying socket.
func routerCheckConn(conn netproxy.Conn) net.Conn {
	return &netproxy.FakeNetConn{Conn: conn}
}

// SS and VMess Close take the same mutex as a blocked encrypted Read. Apply the
// deadline-before-close rule to their TCP streams, including real traffic,
// rather than relying on individual HTTP/DNS callers to interrupt it first.
func routerProtocolDialer(link string, next netproxy.Dialer) netproxy.Dialer {
	if !strings.HasPrefix(link, "ss://") && !strings.HasPrefix(link, "vmess://") {
		return next
	}
	return &routerStreamDialer{Dialer: next}
}

type routerStreamDialer struct{ netproxy.Dialer }

func (d *routerStreamDialer) DialContext(ctx context.Context, network, address string) (netproxy.Conn, error) {
	c, err := d.Dialer.DialContext(ctx, network, address)
	if err != nil {
		return c, err
	}
	magic, err := netproxy.ParseMagicNetwork(network)
	if err == nil && magic.Network == "tcp" {
		return &routerStreamConn{Conn: c}, nil
	}
	return c, nil
}

func (d *routerStreamDialer) Close() error {
	if closer, ok := d.Dialer.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

type routerStreamConn struct {
	netproxy.Conn
	once sync.Once
	err  error
}

func (c *routerStreamConn) Close() error {
	c.once.Do(func() {
		_ = c.Conn.SetDeadline(time.Now())
		c.err = c.Conn.Close()
	})
	return c.err
}

// Adapt URI options at the shared constructor so interactive tests, health
// checks and actual traffic use exactly the same protocol parameters.
func routerProtocolOptions(link string, next netproxy.Dialer, hopInterval time.Duration) (string, netproxy.Dialer, error) {
	scheme, _, _ := strings.Cut(link, "://")
	if scheme != "anytls" && scheme != "hysteria2" && scheme != "hy2" {
		return link, next, nil
	}
	u, err := proxyurl.Parse(link)
	if err != nil {
		return "", nil, fmt.Errorf("invalid protocol URL")
	}
	q := u.Query()
	// Only honor an explicit URI preference; never silently disable TLS checks.
	for _, key := range []string{"insecure", "allowInsecure", "allow_insecure"} {
		if value := q.Get(key); value != "" {
			allow, err := strconv.ParseBool(value)
			if err != nil {
				return "", nil, fmt.Errorf("invalid TLS verification option")
			}
			q.Set("insecure", "0")
			if allow {
				q.Set("insecure", "1")
			}
			break
		}
	}
	if scheme != "anytls" {
		ports := q.Get("mport")
		if ports == "" {
			ports = q.Get("ports")
		}
		if ports == "" && strings.ContainsAny(u.Port(), "-,") {
			ports = u.Port()
		}
		if ports != "" {
			if !validRouterPorts(ports) {
				return "", nil, fmt.Errorf("invalid Hysteria2 port range")
			}
			first := strings.FieldsFunc(ports, func(r rune) bool { return r == '-' || r == ',' })[0]
			u.Host = net.JoinHostPort(u.Hostname(), first)
		}
		if obfs := q.Get("obfs"); obfs != "" {
			if obfs != "salamander" {
				return "", nil, fmt.Errorf("unsupported Hysteria2 obfuscation")
			}
			password := q.Get("obfs-password")
			if password == "" {
				password = q.Get("obfsPassword")
			}
			if len(password) < 4 {
				return "", nil, fmt.Errorf("Hysteria2 obfuscation password must contain at least 4 bytes")
			}
			next = &routerSalamanderDialer{next: next, password: []byte(password)}
		}
		if ports != "" {
			next = &routerHopDialer{next: next, ports: ports, interval: hopInterval}
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), next, nil
}

type routerHopDialer struct {
	next     netproxy.Dialer
	ports    string
	interval time.Duration
}

func (d *routerHopDialer) DialContext(ctx context.Context, network, addr string) (netproxy.Conn, error) {
	magic, err := netproxy.ParseMagicNetwork(network)
	if err != nil {
		return nil, err
	}
	if magic.Network != "udp" {
		return d.next.DialContext(ctx, network, addr)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	hops, err := udphop.ParseUDPHopAddr(net.JoinHostPort(host, d.ports))
	if err != nil {
		return nil, err
	}
	initial := true
	c, err := udphop.NewUDPHopPacketConn(hops, d.interval, func(remote net.Addr) (net.PacketConn, error) {
		// Later hops outlive the initiating request. The initial dial still
		// obeys its deadline; all hops preserve the marked network string.
		hopContext := context.Background()
		if initial {
			hopContext = ctx
			initial = false
		}
		c, err := d.next.DialContext(hopContext, network, remote.String())
		if err != nil {
			return nil, err
		}
		packet, ok := c.(netproxy.PacketConn)
		if !ok {
			_ = c.Close()
			return nil, fmt.Errorf("Hysteria2 requires UDP")
		}
		if actual, ok := c.(interface{ RemoteAddr() net.Addr }); ok && actual.RemoteAddr() != nil {
			remote = actual.RemoteAddr()
		}
		return netproxy.NewFakeNetPacketConn(packet, &net.UDPAddr{}, remote), nil
	})
	if err != nil {
		return nil, err
	}
	// The upstream hopping socket exposes SyscallConn even when its wrapped
	// transport cannot. Hide that optional fast path from QUIC.
	return &routerHopConn{PacketConn: c, address: addr}, nil
}

type routerHopConn struct {
	net.PacketConn
	address string
}

func (c *routerHopConn) Read(b []byte) (int, error)  { n, _, err := c.ReadFrom(b); return n, err }
func (c *routerHopConn) Write(b []byte) (int, error) { return c.WriteTo(b, c.address) }
func (c *routerHopConn) ReadFrom(b []byte) (int, netip.AddrPort, error) {
	n, addr, err := c.PacketConn.ReadFrom(b)
	if err != nil {
		return n, netip.AddrPort{}, err
	}
	udp, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, netip.AddrPort{}, fmt.Errorf("invalid Hysteria2 UDP peer")
	}
	return n, udp.AddrPort(), nil
}
func (c *routerHopConn) WriteTo(b []byte, _ string) (int, error) { return c.PacketConn.WriteTo(b, nil) }

func validRouterPorts(value string) bool {
	for _, part := range strings.Split(value, ",") {
		pair := strings.Split(part, "-")
		if len(pair) > 2 {
			return false
		}
		first, err := strconv.Atoi(pair[0])
		if err != nil || first < 1 || first > 65535 {
			return false
		}
		if len(pair) == 2 {
			last, err := strconv.Atoi(pair[1])
			if err != nil || last < first || last > 65535 {
				return false
			}
		}
	}
	return true
}

type routerSalamanderDialer struct {
	next     netproxy.Dialer
	password []byte
}

func (d *routerSalamanderDialer) DialContext(ctx context.Context, network, addr string) (netproxy.Conn, error) {
	c, err := d.next.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	magic, err := netproxy.ParseMagicNetwork(network)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	if magic.Network != "udp" {
		return c, nil
	}
	packet, ok := c.(netproxy.PacketConn)
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("Hysteria2 requires a UDP packet connection")
	}
	return &routerSalamanderConn{PacketConn: packet, password: d.password, address: addr}, nil
}

// Do not expose SyscallConn/ReadMsgUDP: QUIC's raw socket fast path would
// bypass obfuscation. All datagrams must pass through ReadFrom/WriteTo.
type routerSalamanderConn struct {
	netproxy.PacketConn
	password []byte
	address  string
}

func (c *routerSalamanderConn) Read(b []byte) (int, error)  { n, _, err := c.ReadFrom(b); return n, err }
func (c *routerSalamanderConn) Write(b []byte) (int, error) { return c.WriteTo(b, c.address) }
func (c *routerSalamanderConn) ReadFrom(b []byte) (int, netip.AddrPort, error) {
	packet := pool.Get(65535)
	defer pool.Put(packet)
	for {
		n, addr, err := c.PacketConn.ReadFrom(packet)
		if err != nil {
			return 0, addr, err
		}
		if n < 8 {
			continue
		}
		mask := routerSalamanderMask(c.password, packet[:8])
		for i := 8; i < n; i++ {
			packet[i] ^= mask[(i-8)%len(mask)]
		}
		return copy(b, packet[8:n]), addr, nil
	}
}
func (c *routerSalamanderConn) WriteTo(b []byte, addr string) (int, error) {
	packet := make([]byte, len(b)+8)
	if _, err := io.ReadFull(rand.Reader, packet[:8]); err != nil {
		return 0, err
	}
	mask := routerSalamanderMask(c.password, packet[:8])
	for i := range b {
		packet[i+8] = b[i] ^ mask[i%len(mask)]
	}
	n, err := c.PacketConn.WriteTo(packet, addr)
	if err != nil {
		return 0, err
	}
	if n != len(packet) {
		return 0, io.ErrShortWrite
	}
	return len(b), nil
}
func routerSalamanderMask(password, salt []byte) [32]byte {
	key := make([]byte, 0, len(password)+len(salt))
	key = append(key, password...)
	key = append(key, salt...)
	return blake2b.Sum256(key)
}
