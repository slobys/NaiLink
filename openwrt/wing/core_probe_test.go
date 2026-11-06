// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"errors"
	"github.com/daeuniverse/dae-wing/routercore"
	"github.com/sirupsen/logrus"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	daeConfig "github.com/daeuniverse/dae/config"
	dnsmessage "github.com/miekg/dns"
)

type routerProbeDialFunc func(context.Context, string, string) (net.Conn, error)

func (f routerProbeDialFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

func TestNativeReadinessChecksConfiguredMethodAndStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL + "/generate_204")
	dial := (&net.Dialer{}).DialContext
	if err := routerVerifyNativeHTTP(context.Background(), target, http.MethodGet, dial); err != nil {
		t.Fatal("configured GET failed:", err)
	}
	if err := routerVerifyNativeHTTP(context.Background(), target, http.MethodHead, dial); err == nil || err.Error() != "core_connectivity_failed" {
		t.Fatal("readiness accepted a 405 as a successful 204:", err)
	}
}

func TestNativeReadinessAcceptsWorkingDNSFamily(t *testing.T) {
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	listener, err := net.ListenPacket("udp4", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	handler := dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
		r := new(dnsmessage.Msg)
		r.SetReply(q)
		header := dnsmessage.RR_Header{Name: q.Question[0].Name, Rrtype: q.Question[0].Qtype, Class: dnsmessage.ClassINET}
		if q.Question[0].Qtype == dnsmessage.TypeAAAA {
			if w.LocalAddr().Network() == "udp" {
				r.Truncated = true
			} else {
				r.Answer = append(r.Answer, &dnsmessage.AAAA{Hdr: header, AAAA: net.ParseIP("2001:db8::42")})
			}
		}
		_ = w.WriteMsg(r)
	})
	for _, server := range []*dnsmessage.Server{{PacketConn: listener, Handler: handler}, {Listener: tcp, Handler: handler}} {
		go func() { _ = server.ActivateAndServe() }()
		defer server.Shutdown()
	}
	for _, check := range []struct {
		prefer int
		ready  bool
	}{{0, true}, {6, true}, {4, false}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		ready := routerVerifyNativeDNS(ctx, listener.LocalAddr().String(), "fixture.test", check.prefer)
		cancel()
		if ready != check.ready {
			t.Fatalf("preference %d, ready=%t, want %t", check.prefer, ready, check.ready)
		}
	}
}

func TestMihomoHealthCheckUsesExplicitExpectedStatus(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	ports := sbPorts{Mixed: 39001, API: 39002, DNS: 39003, Secret: "test-only"}
	for _, path := range []string{"/generate_204", "/custom"} {
		conf.Global.TcpCheckUrl = []string{"https://probe.example" + path}
		canonical, _, err := coreCanonicalConfig(conf, dirs, ports, false, "mihomo")
		if err != nil {
			t.Fatal(err)
		}
		profile, err := mihomoFromCanonical(canonical, conf, ports)
		if err != nil {
			t.Fatal(err)
		}
		groups := mhObjects(profile["proxy-groups"])
		if len(groups) == 0 || groups[0]["type"] != "url-test" {
			t.Fatal("fixture must generate an automatic group")
		}
		expected := ""
		if path == "/generate_204" {
			expected = "204"
		}
		if got := mhString(groups[0]["expected-status"]); got != expected {
			t.Fatalf("%s expected-status=%q, want %q", path, got, expected)
		}
	}
}

func TestSingBoxStartsWithLongHealthCheckInterval(t *testing.T) {
	binary := os.Getenv("DAED_TEST_SINGBOX")
	if binary == "" {
		t.Skip("native sing-box binary required")
	}
	oldBinary, oldRuntime := routerSingBoxBinary, routerCoreRuntimeDir
	routerSingBoxBinary, routerCoreRuntimeDir = binary, t.TempDir()
	t.Cleanup(func() { routerSingBoxBinary, routerCoreRuntimeDir = oldBinary, oldRuntime })
	if err := os.Chmod(routerCoreRuntimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	conf, dirs := coreFixtureConfig(t)
	conf.Global.WanInterface = []string{"lo"}
	conf.Global.TcpCheckUrl = []string{"http://127.0.0.1:9/generate_204"}
	conf.Group[0].CheckInterval = time.Hour
	before := restartConfigKey(conf)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := newRouterSingBoxSession(ctx, logrus.New(), conf, dirs, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = routerStopSession(session) })
	if err := routerSessionReady(ctx, session); err != nil {
		t.Fatal("sing-box could not start with a valid one-hour check interval:", err)
	}
	if before != restartConfigKey(conf) {
		t.Fatal("changed saved settings to pass readiness")
	}
}

func TestPrivateProbePortCollisionRecoveryIsBoundedAndCleansBeforeRetry(t *testing.T) {
	for _, scenario := range []string{"success", "recover", "exhausted", "other_failure", "cancelled", "cleanup_failed", "factory_failed"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			starts, live := 0, 0
			session, err := routerStartPrivateProbe(ctx, func() (routerCoreSession, error) {
				if live != 0 {
					t.Fatal("new process started before the previous one was cleaned")
				}
				starts++
				if scenario == "factory_failed" {
					return nil, errors.New("singbox_configuration_invalid")
				}
				live++
				s := &coreFakeSession{live: &live, done: make(chan error), readyErr: errors.New("core_ports_unavailable")}
				switch scenario {
				case "success":
					s.readyErr = nil
				case "recover":
					if starts == 3 {
						s.readyErr = nil
					}
				case "other_failure":
					s.readyErr = errors.New("core_start_failed")
				case "cancelled":
					cancel()
				case "cleanup_failed":
					s.stopErr = errors.New("process did not stop")
				}
				return s, nil
			})
			if scenario == "recover" || scenario == "success" {
				wantStarts := 3
				if scenario == "success" {
					wantStarts = 1
				}
				if err != nil || starts != wantStarts || live != 1 || session == nil {
					t.Fatal("listener collision did not recover", starts, live, err)
				}
				if routerStopProbe(session) != nil || live != 0 {
					t.Fatal("successful private process was not cleaned")
				}
				return
			}
			want, wantStarts, wantLive := "core_ports_unavailable", 3, 0
			switch scenario {
			case "other_failure":
				want, wantStarts = "core_start_failed", 1
			case "cancelled":
				want, wantStarts = context.Canceled.Error(), 1
			case "cleanup_failed":
				want, wantStarts, wantLive = "core_cleanup_failed", 1, 1
			case "factory_failed":
				want, wantStarts = "singbox_configuration_invalid", 1
			}
			if err == nil || err.Error() != want || starts != wantStarts || live != wantLive || session != nil {
				t.Fatal("retried a non-port failure, exceeded limit or leaked process", starts, live, err)
			}
		})
	}
}

func TestNativeReadinessClassifiesRealListenerCollision(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	other, bindErr := net.Listen("tcp4", listener.Addr().String())
	if bindErr == nil {
		other.Close()
		t.Fatal("fixture failed to create a real port collision")
	}
	log := logrus.New()
	log.SetOutput(io.Discard)
	s := &routerSingBoxSession{exit: make(chan struct{})}
	writer := &sbLogWriter{log: log, startupPortConflict: &s.portConflict}
	line := "FATAL[0000] start service: external controller listen error: " + bindErr.Error() + "\n"
	_, _ = writer.Write([]byte(line[:len(line)/2]))
	_, _ = writer.Write([]byte(line[len(line)/2:]))
	close(s.exit)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.Ready(ctx); err == nil || err.Error() != "core_ports_unavailable" {
		t.Fatal("real listener collision lost its safe category", err)
	}
	for _, text := range []string{"ERROR dial tcp secret:443: bind: address already in use\n", "ERROR listen tcp: permission denied\n", "INFO listen tcp: bind: address already in use\n"} {
		negative := &routerSingBoxSession{}
		writer := &sbLogWriter{log: log, startupPortConflict: &negative.portConflict}
		_, _ = writer.Write([]byte(text))
		if negative.portConflict.Load() {
			t.Fatal("unrelated child failure was treated as a listener collision")
		}
	}
}

func TestRouterCoreSlowHTTPResponseIsMeasuredRatherThanRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3200 * time.Millisecond):
			w.WriteHeader(204)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	dial := routerProbeDialFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	})
	result := routerProbeHTTP(context.Background(), dial, daeConfig.Global{TcpCheckUrl: []string{"http://slow.test/generate_204", "127.0.0.1"}, TcpCheckHttpMethod: http.MethodHead})
	if !result.Alive || result.Latency < 3200 {
		t.Fatal("slow valid response rejected or measured incorrectly", result)
	}
}

func TestRouterMihomoProbeErrorsUseOnlyKnownPrivateAliases(t *testing.T) {
	alias := routercore.MihomoName("node:probe-0")
	p := &routerProbeErrors{codes: map[string]string{}, aliases: map[string]string{alias: "probe-0"}}
	_ = p.Fire(&logrus.Entry{Message: "[TCP] dial " + alias + " (match InUser/probe-0) private.example:443 error: dial tcp secret:443: i/o timeout"})
	if p.message("probe-0", "unreachable") != "node_tcp_timeout" {
		t.Fatal("Mihomo error was not attached to its node")
	}
	_ = p.Fire(&logrus.Entry{Message: "[TCP] dial " + alias + " (match InUser/probe-0) websocket: bad handshake: HTTP 504 private-token"})
	if p.message("probe-0", "unreachable") != "gateway_failed" {
		t.Fatal("less informative family failure hid the gateway response")
	}
	unknown := routercore.MihomoName("node:outside")
	_ = p.Fire(&logrus.Entry{Message: "[TCP] dial " + unknown + " (match Match/) certificate error"})
	if len(p.codes) != 1 || strings.Contains(p.message("probe-0", "unreachable"), "private") {
		t.Fatal("unrelated error accepted or private text leaked")
	}
}

func TestRouterCoreHTTPRacesFixedTargetsAndHonorsMethod(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method || r.Host != "check.invalid" {
					t.Error("test changed HTTP method or Host", r.Method, r.Host)
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			cancelled := make(chan struct{}, 1)
			started := make(chan struct{})
			dial := routerProbeDialFunc(func(ctx context.Context, _, address string) (net.Conn, error) {
				switch address {
				case "192.0.2.1:80":
					close(started)
					<-ctx.Done()
					cancelled <- struct{}{}
					return nil, ctx.Err()
				case "[2001:db8::1]:80":
					select {
					case <-started:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
				default:
					t.Error("fixed test IP was not used", address)
					return nil, errors.New("unexpected target")
				}
			})
			ctx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			result := routerProbeHTTP(ctx, dial, daeConfig.Global{TcpCheckUrl: []string{"http://check.invalid/generate_204", "192.0.2.1", "2001:db8::1"}, TcpCheckHttpMethod: method})
			if !result.Alive || result.Message != "" {
				t.Fatal("working second family lost", result)
			}
			// net/http may finish the cancelled request before its dial goroutine
			// is scheduled again. Wait for cleanup rather than inspecting a racy
			// instantaneous channel length.
			select {
			case <-cancelled:
			case <-ctx.Done():
				t.Fatal("failed family's dial was not cancelled")
			}
		})
	}
}

func TestRouterCoreHTTPPreservesRealConnectionCostAndStatus(t *testing.T) {
	var mu sync.Mutex
	connections := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		w.WriteHeader(204)
	}))
	defer server.Close()
	dial := routerProbeDialFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		mu.Lock()
		connections++
		mu.Unlock()
		select {
		case <-time.After(40 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	})
	global := daeConfig.Global{TcpCheckUrl: []string{"http://check.invalid/generate_204", "192.0.2.1"}}
	for range 2 {
		r := routerProbeHTTP(context.Background(), dial, global)
		if !r.Alive || r.Latency < 60 {
			t.Fatal("connection/handshake time was omitted", r)
		}
	}
	mu.Lock()
	if connections != 2 {
		t.Error("manual tests reused a connection", connections)
	}
	mu.Unlock()
	global.TcpCheckUrl[0] = "http://check.invalid/generate_200"
	if r := routerProbeHTTP(context.Background(), dial, global); r.Alive || r.Message != "check_target_failed" {
		t.Fatal("unexpected status accepted as healthy", r)
	}
}

func TestRouterCoreHTTPParentCancellation(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	stop()
	dial := routerProbeDialFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	result := routerProbeHTTP(ctx, dial, daeConfig.Global{TcpCheckUrl: []string{"http://check.invalid", "192.0.2.1", "2001:db8::1"}})
	if result.Alive || result.Message != "cancelled" {
		t.Fatal(result)
	}
}

func TestRouterCoreProbeFailureDoesNotHideTimeoutOrLeakValues(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timeout"},
		{&url.Error{Op: "HEAD", URL: "https://secret.invalid", Err: context.DeadlineExceeded}, "timeout"},
		{context.Canceled, "cancelled"},
		{io.ErrUnexpectedEOF, "connection_closed"},
		{errors.New("private-password server-controlled failure"), "unreachable"},
	} {
		if got := routerCoreProbeFailure(c.err); got != c.want {
			t.Fatal(got, c.want)
		}
	}
}

func TestRouterCoreProbeNativeErrorsAreScopedAndRedacted(t *testing.T) {
	p := &routerProbeErrors{codes: map[string]string{}}
	cases := []struct{ message, code string }{
		{"dial tcp private.example:443: i/o timeout", "node_tcp_timeout"},
		{"tls: failed to verify certificate: private-value", "tls_failed"},
		{"REALITY verification failed private-value", "authentication_failed"},
		{"lookup private.example: no such host", "dns_failed"},
		{"dial tcp private.example: connection refused", "connection_refused"},
		{"read private.example: connection reset by peer", "connection_closed"},
	}
	var wg sync.WaitGroup
	for i, c := range cases {
		i, c := i, c
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := "probe-" + string(rune('0'+i))
			_ = p.Fire(&logrus.Entry{Message: "outbound/test[node:" + name + "]: " + c.message})
		}()
	}
	wg.Wait()
	for i, c := range cases {
		if got := p.message("probe-"+string(rune('0'+i)), "unreachable"); got != c.code || strings.Contains(got, "private") {
			t.Fatal(got, c.code)
		}
	}
	_ = p.Fire(&logrus.Entry{Message: "outbound/test[node:real-user-label]: certificate failed"})
	if got := p.message("real-user-label", "unreachable"); got != "unreachable" {
		t.Fatal("accepted a non-probe log")
	}
	_ = p.Fire(&logrus.Entry{Message: "outbound/test[node:probe-0]: authentication failed"})
	_ = p.Fire(&logrus.Entry{Message: "outbound/test[node:probe-0]: i/o timeout"})
	if got := p.message("probe-0", "unreachable"); got != "authentication_failed" {
		t.Fatal("lost actionable native error", got)
	}
}
