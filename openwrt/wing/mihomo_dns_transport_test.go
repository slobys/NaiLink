// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dnsmessage "github.com/miekg/dns"
)

type mhCountDNSListener struct {
	net.Listener
	accepted atomic.Int32
}

func (l *mhCountDNSListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e == nil {
		l.accepted.Add(1)
	}
	return c, e
}

func mhTestDNSBridge(t *testing.T, server sbObject) (*mhDNSBridge, string) {
	t.Helper()
	canonical := sbObject{"route": sbObject{"default_domain_resolver": sbObject{"server": "fixture"}}, "dns": sbObject{"servers": []sbObject{server}, "final": "fixture"}}
	p := sbObject{"_nailink_dns": canonical, "dns": sbObject{}}
	b, err := startMihomoDNS(p, sbPorts{Secret: "fixture-only"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b, strings.TrimPrefix(mhStrings(p["dns"].(sbObject)["nameserver"])[0], "udp://")
}

func mhTestDNSAnswer(q *dnsmessage.Msg) *dnsmessage.Msg {
	r := new(dnsmessage.Msg)
	r.SetReply(q)
	r.Answer = []dnsmessage.RR{&dnsmessage.A{Hdr: dnsmessage.RR_Header{Name: q.Question[0].Name, Rrtype: dnsmessage.TypeA, Class: dnsmessage.ClassINET, Ttl: 30}, A: net.IPv4(192, 0, 2, 44)}}
	return r
}

func TestMihomoDNSReusesTCP(t *testing.T) {
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	counted := &mhCountDNSListener{Listener: listener}
	upstream := &dnsmessage.Server{Listener: counted, Net: "tcp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) { _ = w.WriteMsg(mhTestDNSAnswer(q)) })}
	go upstream.ActivateAndServe()
	t.Cleanup(func() { _ = upstream.Shutdown() })
	server := sbObject{"tag": "fixture", "type": "tcp", "server": "127.0.0.1", "server_port": listener.Addr().(*net.TCPAddr).Port}
	b, _ := mhTestDNSBridge(t, server)
	for i := 0; i < 3; i++ {
		q := new(dnsmessage.Msg)
		q.SetQuestion(fmt.Sprintf("reuse-%d.example.", i), dnsmessage.TypeA)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		r, e := b.exchange(ctx, q, server, false)
		cancel()
		if e != nil || !mhValidDNSAnswer(q, r) || len(r.Answer) != 1 {
			t.Fatal("DNS answer changed", r, e)
		}
	}
	if got := counted.accepted.Load(); got != 1 {
		t.Fatalf("three DNS queries opened %d TCP connections; want one", got)
	}
}

func TestMihomoDNSQueuesShortBurst(t *testing.T) {
	packet, e := net.ListenPacket("udp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	var entered atomic.Int32
	full, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	upstream := &dnsmessage.Server{PacketConn: packet, Net: "udp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
		if entered.Add(1) == 32 {
			close(full)
		}
		<-release
		_ = w.WriteMsg(mhTestDNSAnswer(q))
	})}
	go upstream.ActivateAndServe()
	t.Cleanup(func() { _ = upstream.Shutdown() })
	_, address := mhTestDNSBridge(t, sbObject{"tag": "fixture", "type": "udp", "server": "127.0.0.1", "server_port": packet.LocalAddr().(*net.UDPAddr).Port})
	results := make(chan error, 64)
	for i := 0; i < 64; i++ {
		go func(i int) {
			q := new(dnsmessage.Msg)
			q.SetQuestion(fmt.Sprintf("burst-%d.example.", i), dnsmessage.TypeA)
			a, _, e := (&dnsmessage.Client{Timeout: 3 * time.Second}).Exchange(q, address)
			if e == nil && (!mhValidDNSAnswer(q, a) || a.Rcode != dnsmessage.RcodeSuccess || len(a.Answer) != 1) {
				e = fmt.Errorf("burst query returned failure: %v", a)
			}
			results <- e
		}(i)
	}
	select {
	case <-full:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream concurrency did not reach 32")
	}
	// Hold the first wave while the remaining local requests arrive.
	time.Sleep(100 * time.Millisecond)
	q := new(dnsmessage.Msg)
	q.SetQuestion("overflow.example.", dnsmessage.TypeA)
	a, _, err := (&dnsmessage.Client{Timeout: time.Second}).Exchange(q, address)
	if err != nil || a.Rcode != dnsmessage.RcodeServerFailure {
		t.Error("unbounded DNS admission", a, err)
	}
	once.Do(func() { close(release) })
	for i := 0; i < 64; i++ {
		if e := <-results; e != nil {
			t.Error(e)
		}
	}
}

func TestMihomoDNSRebuildsClosedTCP(t *testing.T) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	counted := &mhCountDNSListener{Listener: l}
	closed := make(chan struct{})
	s := &dnsmessage.Server{Listener: counted, Net: "tcp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
		_ = w.WriteMsg(mhTestDNSAnswer(q))
		if strings.HasPrefix(q.Question[0].Name, "close.") {
			_ = w.Close()
			close(closed)
		}
	})}
	go s.ActivateAndServe()
	t.Cleanup(func() { _ = s.Shutdown() })
	server := sbObject{"tag": "fixture", "type": "tcp", "server": "127.0.0.1", "server_port": l.Addr().(*net.TCPAddr).Port}
	b, _ := mhTestDNSBridge(t, server)
	for _, name := range []string{"close.example.", "after-close.example."} {
		q := new(dnsmessage.Msg)
		q.SetQuestion(name, dnsmessage.TypeA)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		a, e := b.exchange(ctx, q, server, false)
		cancel()
		if e != nil || !mhValidDNSAnswer(q, a) || len(a.Answer) != 1 {
			t.Fatal("dead reused DNS stream was not rebuilt", a, e)
		}
		if name == "close.example." {
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("upstream did not close")
			}
		}
	}
	if counted.accepted.Load() != 2 {
		t.Fatal("unexpected retry count", counted.accepted.Load())
	}
}

func TestMihomoDNSRebuildsSilentReusedStreamWithinQueryBudget(t *testing.T) {
	for _, scenario := range []string{"tcp", "tls", "slow-valid", "backup-fails", "both-silent", "fresh-only", "stop-active", "update", "transfer"} {
		t.Run(scenario, func(t *testing.T) {
			transport := "tcp"
			if scenario == "tls" {
				transport = "tls"
			}
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			counted := &mhCountDNSListener{Listener: listener}
			var serving net.Listener = counted
			connect := (&net.Dialer{}).DialContext
			if transport == "tls" {
				fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				t.Cleanup(fixture.Close)
				serving = tls.NewListener(counted, fixture.TLS.Clone())
				trust := fixture.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
				connect = (&tls.Dialer{Config: trust}).DialContext
			}
			var first atomic.Value
			release := make(chan struct{})
			backupSeen := make(chan struct{}, 1)
			defer close(release)
			server := &dnsmessage.Server{Listener: serving, Net: "tcp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
				if strings.HasPrefix(q.Question[0].Name, "warm.") {
					first.Store(w.RemoteAddr().String())
				} else if scenario == "slow-valid" || scenario == "backup-fails" {
					// A backup must not shorten a slow but valid original response.
					time.Sleep(1200 * time.Millisecond)
				} else if scenario == "both-silent" || scenario == "fresh-only" || scenario == "stop-active" || scenario == "update" || scenario == "transfer" || w.RemoteAddr().String() == first.Load() {
					if scenario == "stop-active" && w.RemoteAddr().String() != first.Load() {
						select {
						case backupSeen <- struct{}{}:
						default:
						}
					}
					// A middlebox silently drops the established stream without EOF.
					<-release
					return
				}
				_ = w.WriteMsg(mhTestDNSAnswer(q))
			})}
			go server.ActivateAndServe()
			t.Cleanup(func() { _ = server.Shutdown() })
			b, _ := mhTestDNSBridge(t, sbObject{"tag": "fixture", "type": "tcp", "server": "127.0.0.1", "server_port": listener.Addr().(*net.TCPAddr).Port})
			if scenario == "stop-active" {
				go func() {
					select {
					case <-backupSeen:
						b.Close()
					case <-release:
					}
				}()
			}
			names := []string{"warm.example.", "after-idle.example."}
			if scenario == "fresh-only" {
				names = names[1:]
			}
			for _, name := range names {
				q := new(dnsmessage.Msg)
				q.SetQuestion(name, dnsmessage.TypeA)
				if name != "warm.example." {
					if scenario == "update" {
						q.Opcode = dnsmessage.OpcodeUpdate
					}
					if scenario == "transfer" {
						q.Question[0].Qtype = dnsmessage.TypeAXFR
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 1600*time.Millisecond)
				answer, err := b.exchangeWire(ctx, q, mhDNSKey(transport, listener.Addr().String(), 0), func(ctx context.Context) (net.Conn, error) {
					if scenario == "backup-fails" && counted.accepted.Load() > 0 {
						return nil, errors.New("fixture_backup_unavailable")
					}
					return connect(ctx, "tcp", listener.Addr().String())
				})
				cancel()
				if (scenario == "stop-active" || scenario == "update") && name != "warm.example." {
					if err == nil {
						t.Fatal("stopped bridge returned a successful query")
					}
					continue
				}
				if (scenario == "both-silent" || scenario == "fresh-only" || scenario == "transfer") && name != "warm.example." {
					var timeout net.Error
					if err == nil || !(errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout()) {
						t.Fatalf("silent connections extended the original budget: %v", err)
					}
					continue
				}
				if err != nil || !mhValidDNSAnswer(q, answer) || len(answer.Answer) != 1 {
					t.Fatalf("first query after a silent stale stream did not recover within its original budget: %v", err)
				}
			}
			wantConnections := int32(2)
			if scenario == "backup-fails" || scenario == "fresh-only" {
				wantConnections = 1
			}
			if counted.accepted.Load() != wantConnections {
				t.Fatalf("want one bounded same-path reconnect, got %d connections", counted.accepted.Load())
			}
			b.wire.mu.Lock()
			retained := len(b.wire.conns)
			b.wire.mu.Unlock()
			wantRetained := 1
			if scenario == "both-silent" || scenario == "fresh-only" || scenario == "stop-active" {
				wantRetained = 0
			}
			if retained != wantRetained {
				t.Fatalf("losing or failed query retained a socket: %d", retained)
			}
		})
	}
}

func TestMihomoDNSCancelledLeaseDoesNotLeakConnection(t *testing.T) {
	b := &mhDNSBridge{}
	local, remote := net.Pipe()
	defer remote.Close()
	defer b.wire.close()
	key := mhDNSKey("tcp", "fixture:53", 0)
	entry, err := b.wire.add(key, local)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q := new(dnsmessage.Msg)
	q.SetQuestion("cancelled.example.", dnsmessage.TypeA)
	_, err = b.finishWire(b.queryWire(ctx, q, key, entry, func(context.Context) (net.Conn, error) {
		t.Fatal("cancelled lease opened another connection")
		return nil, nil
	}), true)
	if !errors.Is(err, context.Canceled) || len(b.wire.conns) != 0 {
		t.Fatal("cancelled request retained a leased stream", err, len(b.wire.conns))
	}
}

func TestMihomoDNSClosesCancelledUDP(t *testing.T) {
	packet, e := net.ListenPacket("udp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer packet.Close()
	server := sbObject{"tag": "fixture", "type": "udp", "server": "127.0.0.1", "server_port": packet.LocalAddr().(*net.UDPAddr).Port}
	b, _ := mhTestDNSBridge(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	q := new(dnsmessage.Msg)
	q.SetQuestion("stalled.example.", dnsmessage.TypeA)
	done := make(chan error, 1)
	go func() { _, e := b.exchange(ctx, q, server, false); done <- e }()
	_ = packet.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, e = packet.ReadFrom(make([]byte, 4096)); e != nil {
		cancel()
		t.Fatal(e)
	}
	start := time.Now()
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal("wrong cancellation", e)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled UDP query retained its socket")
	}
	if time.Since(start) > time.Second {
		t.Fatal("slow cancellation")
	}
}

func TestMihomoDNSRejectsInvalidReplyBeforeReuse(t *testing.T) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	counted := &mhCountDNSListener{Listener: l}
	s := &dnsmessage.Server{Listener: counted, Net: "tcp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
		r := mhTestDNSAnswer(q)
		if strings.HasPrefix(q.Question[0].Name, "invalid.") {
			r.Opcode = dnsmessage.OpcodeStatus
		}
		_ = w.WriteMsg(r)
	})}
	go s.ActivateAndServe()
	t.Cleanup(func() { _ = s.Shutdown() })
	server := sbObject{"tag": "fixture", "type": "tcp", "server": "127.0.0.1", "server_port": l.Addr().(*net.TCPAddr).Port}
	b, _ := mhTestDNSBridge(t, server)
	for i, name := range []string{"invalid.example.", "valid.example."} {
		q := new(dnsmessage.Msg)
		q.SetQuestion(name, dnsmessage.TypeA)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		a, e := b.exchange(ctx, q, server, false)
		cancel()
		if i == 0 {
			if e == nil || e.Error() != "invalid_dns_reply" {
				t.Fatal("invalid reply accepted", a, e)
			}
		} else if e != nil || !mhValidDNSAnswer(q, a) {
			t.Fatal(a, e)
		}
	}
	if counted.accepted.Load() != 2 {
		t.Fatal("invalid reply connection was reused")
	}
}

func TestMihomoDNSDoesNotShareRoutes(t *testing.T) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	counted := &mhCountDNSListener{Listener: l}
	s := &dnsmessage.Server{Listener: counted, Net: "tcp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) { _ = w.WriteMsg(mhTestDNSAnswer(q)) })}
	go s.ActivateAndServe()
	t.Cleanup(func() { _ = s.Shutdown() })
	b, _ := mhTestDNSBridge(t, sbObject{"tag": "fixture", "type": "tcp", "server": "127.0.0.1", "server_port": l.Addr().(*net.TCPAddr).Port})
	q := new(dnsmessage.Msg)
	q.SetQuestion("route.example.", dnsmessage.TypeA)
	for _, port := range []int{0, 10, 0, 10} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, e := b.exchangeWire(ctx, q, mhDNSKey("tcp", l.Addr().String(), port), func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", l.Addr().String())
		})
		cancel()
		if e != nil {
			t.Fatal(e)
		}
	}
	if counted.accepted.Load() != 2 {
		t.Fatal("direct and proxied DNS streams were mixed", counted.accepted.Load())
	}
}

func TestMihomoDNSFailedDetourDoesNotUseWarmDirectStream(t *testing.T) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	counted := &mhCountDNSListener{Listener: l}
	s := &dnsmessage.Server{Listener: counted, Net: "tcp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) { _ = w.WriteMsg(mhTestDNSAnswer(q)) })}
	go s.ActivateAndServe()
	t.Cleanup(func() { _ = s.Shutdown() })
	server := sbObject{"tag": "fixture", "type": "tcp", "server": "127.0.0.1", "server_port": l.Addr().(*net.TCPAddr).Port}
	b, _ := mhTestDNSBridge(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	q := new(dnsmessage.Msg)
	q.SetQuestion("detour.example.", dnsmessage.TypeA)
	if _, e := b.exchange(ctx, q, server, false); e != nil {
		t.Fatal(e)
	}
	unused, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := unused.Addr().(*net.TCPAddr).Port
	_ = unused.Close()
	b.proxyPorts["broken"] = port
	broken := sbObject{"type": "tcp", "server": "127.0.0.1", "server_port": l.Addr().(*net.TCPAddr).Port, "detour": "broken"}
	if _, e := b.exchange(ctx, q, broken, false); e == nil {
		t.Fatal("failed DNS detour silently used direct DNS")
	}
	if counted.accepted.Load() != 1 {
		t.Fatal("DNS detour contacted the direct upstream")
	}
}

func TestMihomoDNSStopCancelsQueuedAndActiveQueries(t *testing.T) {
	packet, e := net.ListenPacket("udp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	var entered atomic.Int32
	full, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	s := &dnsmessage.Server{PacketConn: packet, Net: "udp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
		if entered.Add(1) == 32 {
			close(full)
		}
		<-release
		_ = w.WriteMsg(mhTestDNSAnswer(q))
	})}
	go s.ActivateAndServe()
	t.Cleanup(func() { _ = s.Shutdown() })
	b, address := mhTestDNSBridge(t, sbObject{"tag": "fixture", "type": "udp", "server": "127.0.0.1", "server_port": packet.LocalAddr().(*net.UDPAddr).Port})
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := new(dnsmessage.Msg)
			q.SetQuestion(fmt.Sprintf("stop-%d.example.", i), dnsmessage.TypeA)
			_, _, _ = (&dnsmessage.Client{Timeout: time.Second}).Exchange(q, address)
		}(i)
	}
	select {
	case <-full:
	case <-time.After(time.Second):
		t.Fatal("queries did not reach upstream")
	}
	start := time.Now()
	b.Close()
	if time.Since(start) > time.Second {
		t.Error("DNS stop waited for abandoned upstream reads")
	}
	once.Do(func() { close(release) })
	wg.Wait()
	if len(b.slots) != 0 || len(b.pending) != 0 {
		t.Fatal("DNS work survived bridge shutdown")
	}
}

func TestMihomoDNSIdleCapacityAndStop(t *testing.T) {
	var p mhDNSWirePool
	var peers []net.Conn
	defer func() {
		p.close()
		for _, c := range peers {
			_ = c.Close()
		}
	}()
	for i := 0; i < 17; i++ {
		c, peer := net.Pipe()
		peers = append(peers, peer)
		e, err := p.add(mhDNSKey("tcp", fmt.Sprint(i), 0), c)
		if err != nil {
			t.Fatal(err)
		}
		p.put(e, true)
	}
	_ = peers[16].SetReadDeadline(time.Now().Add(time.Second))
	if _, e := peers[16].Read(make([]byte, 1)); !errors.Is(e, io.EOF) {
		t.Fatal("idle socket limit not enforced", e)
	}
	p.close()
	for _, c := range peers[:16] {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		if _, e := c.Read(make([]byte, 1)); !errors.Is(e, io.EOF) {
			t.Fatal("stop retained idle socket", e)
		}
	}
	if _, e := p.take(mhDNSKey("tcp", "0", 0)); !errors.Is(e, net.ErrClosed) {
		t.Fatal("pool reopened after stop", e)
	}
}

func TestMihomoDNSIdleExpiryClosesSocket(t *testing.T) {
	var p mhDNSWirePool
	defer p.close()
	c, peer := net.Pipe()
	defer peer.Close()
	e, err := p.add(mhDNSKey("tcp", "fixture", 0), c)
	if err != nil {
		t.Fatal(err)
	}
	p.put(e, true)
	p.mu.Lock()
	e.timer.Reset(10 * time.Millisecond)
	p.mu.Unlock()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatal("expired idle DNS socket remained open", err)
	}
}

func TestMihomoDNSUDPTruncationKeepsTCPFallback(t *testing.T) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	counted := &mhCountDNSListener{Listener: l}
	packet, e := net.ListenPacket("udp4", l.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	var tcpQueries atomic.Int32
	udp := &dnsmessage.Server{PacketConn: packet, Net: "udp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
		r := new(dnsmessage.Msg)
		r.SetReply(q)
		r.Truncated = true
		if strings.HasPrefix(q.Question[0].Name, "invalid.") {
			r.Opcode = dnsmessage.OpcodeStatus
		}
		_ = w.WriteMsg(r)
	})}
	tcp := &dnsmessage.Server{Listener: counted, Net: "tcp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
		tcpQueries.Add(1)
		_ = w.WriteMsg(mhTestDNSAnswer(q))
	})}
	go udp.ActivateAndServe()
	go tcp.ActivateAndServe()
	t.Cleanup(func() { _ = udp.Shutdown(); _ = tcp.Shutdown() })
	server := sbObject{"tag": "fixture", "type": "udp", "server": "127.0.0.1", "server_port": l.Addr().(*net.TCPAddr).Port}
	b, _ := mhTestDNSBridge(t, server)
	for i, name := range []string{"large.example.", "large2.example.", "invalid.example."} {
		q := new(dnsmessage.Msg)
		q.SetQuestion(name, dnsmessage.TypeA)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		a, e := b.exchange(ctx, q, server, false)
		cancel()
		if i == 2 {
			if e == nil || e.Error() != "invalid_dns_reply" {
				t.Fatal("invalid UDP response entered TCP fallback", a, e)
			}
		} else if e != nil || !mhValidDNSAnswer(q, a) || a.Truncated || len(a.Answer) != 1 {
			t.Fatal("TCP fallback changed reply", a, e)
		}
	}
	if counted.accepted.Load() != 1 || tcpQueries.Load() != 2 {
		t.Fatal("fallback did not reuse the same validated upstream", counted.accepted.Load(), tcpQueries.Load())
	}
}

func TestMihomoDNSReusesAuthenticatedTLSStream(t *testing.T) {
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(fixture.Close)
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	counted := &mhCountDNSListener{Listener: l}
	s := &dnsmessage.Server{Listener: tls.NewListener(counted, fixture.TLS.Clone()), Net: "tcp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) { _ = w.WriteMsg(mhTestDNSAnswer(q)) })}
	go s.ActivateAndServe()
	t.Cleanup(func() { _ = s.Shutdown() })
	b, _ := mhTestDNSBridge(t, sbObject{"tag": "fixture", "type": "tls", "server": "127.0.0.1", "server_port": l.Addr().(*net.TCPAddr).Port})
	trust := fixture.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	trust.MinVersion = tls.VersionTLS12
	for i := 0; i < 3; i++ {
		q := new(dnsmessage.Msg)
		q.SetQuestion(fmt.Sprintf("dot-%d.example.", i), dnsmessage.TypeA)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		a, e := b.exchangeWire(ctx, q, mhDNSKey("tls", l.Addr().String(), 0), func(ctx context.Context) (net.Conn, error) {
			return (&tls.Dialer{Config: trust}).DialContext(ctx, "tcp", l.Addr().String())
		})
		cancel()
		if e != nil || !mhValidDNSAnswer(q, a) {
			t.Fatal(a, e)
		}
	}
	if counted.accepted.Load() != 1 {
		t.Fatal("DoT repeated TLS connections", counted.accepted.Load())
	}
}

func mhTestDoH(t *testing.T, handler http.HandlerFunc, states ...func(net.Conn, http.ConnState)) (*mhDNSBridge, sbObject, *httptest.Server) {
	t.Helper()
	s := httptest.NewUnstartedServer(handler)
	if len(states) > 0 {
		s.Config.ConnState = states[0]
	}
	s.EnableHTTP2 = true
	s.StartTLS()
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	var n int
	_, _ = fmt.Sscan(port, &n)
	server := sbObject{"tag": "fixture", "type": "https", "server": host, "server_port": n}
	b, _ := mhTestDNSBridge(t, server)
	// Only the local fixture's private CA is trusted. Production keeps normal PKI.
	c, e := b.dohClient(0, (&net.Dialer{}).DialContext)
	if e != nil {
		t.Fatal(e)
	}
	transport := c.Transport.(*http.Transport)
	transport.TLSClientConfig = s.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	return b, server, s
}

func TestMihomoDNSDoHReusesHTTP2(t *testing.T) {
	var connections sync.Map
	var count atomic.Int32
	b, server, _ := mhTestDoH(t, func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Error("HTTP/2 not negotiated")
		}
		if _, exists := connections.LoadOrStore(r.RemoteAddr, true); !exists {
			count.Add(1)
		}
		raw, _ := io.ReadAll(r.Body)
		q := new(dnsmessage.Msg)
		_ = q.Unpack(raw)
		answer, _ := mhTestDNSAnswer(q).Pack()
		w.Header().Set("Content-Type", "application/dns-message; charset=binary")
		_, _ = w.Write(answer)
	})
	for i := 0; i < 4; i++ {
		q := new(dnsmessage.Msg)
		q.SetQuestion(fmt.Sprintf("http-%d.example.", i), dnsmessage.TypeA)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		a, e := b.exchange(ctx, q, server, false)
		cancel()
		if e != nil || !mhValidDNSAnswer(q, a) {
			t.Fatal(a, e)
		}
	}
	if count.Load() != 1 {
		t.Fatal("DoH did not reuse its connection", count.Load())
	}
}

func TestMihomoDNSRejectsDoHMimeAndOversize(t *testing.T) {
	for _, mode := range []string{"mime", "oversize", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			b, server, _ := mhTestDoH(t, func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				q := new(dnsmessage.Msg)
				_ = q.Unpack(raw)
				answer, _ := mhTestDNSAnswer(q).Pack()
				w.Header().Set("Content-Type", "application/dns-message")
				switch mode {
				case "mime":
					w.Header().Set("Content-Type", "application/dns-message-invalid")
				case "oversize":
					answer = append(answer, make([]byte, 65536)...)
				case "redirect":
					w.Header().Set("Location", "https://example.invalid/")
					w.WriteHeader(http.StatusTemporaryRedirect)
					return
				}
				_, _ = w.Write(answer)
			})
			// Keep the production redirect policy while using the fixture CA.
			b.httpClients[0].CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			q := new(dnsmessage.Msg)
			q.SetQuestion("invalid-http.example.", dnsmessage.TypeA)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, e := b.exchange(ctx, q, server, false); e == nil {
				t.Fatal("invalid DoH response accepted")
			}
		})
	}
}

func TestMihomoDNSDoHTransportLimitsAndCertificate(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS server received DNS query") }))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	var n int
	_, _ = fmt.Sscan(port, &n)
	b, _ := mhTestDNSBridge(t, sbObject{"tag": "fixture", "type": "https", "server": host, "server_port": n})
	c, e := b.dohClient(0, (&net.Dialer{}).DialContext)
	if e != nil {
		t.Fatal(e)
	}
	transport := c.Transport.(*http.Transport)
	if transport.Proxy != nil || !transport.ForceAttemptHTTP2 || transport.MaxIdleConns != 2 || transport.MaxConnsPerHost != 4 || transport.IdleConnTimeout != mhDNSIdleTimeout {
		t.Fatal("unbounded or redirected DoH transport")
	}
	q := new(dnsmessage.Msg)
	q.SetQuestion("certificate.example.", dnsmessage.TypeA)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, e = b.exchange(ctx, q, sbObject{"type": "https", "server": host, "server_port": n}, false); e == nil {
		t.Fatal("untrusted DNS certificate accepted")
	}
}

func TestMihomoDNSDoHStopClosesActiveConnection(t *testing.T) {
	entered := make(chan struct{})
	released := make(chan struct{})
	closed := make(chan struct{}, 1)
	b, server, _ := mhTestDoH(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}, func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	})
	q := new(dnsmessage.Msg)
	q.SetQuestion("stop-http.example.", dnsmessage.TypeA)
	done := make(chan error, 1)
	go func() { _, e := b.exchange(b.ctx, q, server, false); done <- e }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(released)
		t.Fatal("DoH query did not reach fixture")
	}
	b.Close()
	close(released)
	select {
	case e := <-done:
		if e == nil {
			t.Error("query survived stop")
		}
	case <-time.After(time.Second):
		t.Fatal("DoH stop did not cancel active query")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("HTTP/2 connection remained open after stop")
	}
}

func TestMihomoDNSDoHRebuildsOnlyFailedReusedConnection(t *testing.T) {
	for _, mode := range []string{"headers", "body", "fresh", "update"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				raw, _ := io.ReadAll(r.Body)
				q := new(dnsmessage.Msg)
				_ = q.Unpack(raw)
				answer, _ := mhTestDNSAnswer(q).Pack()
				broken := n == 2 || mode == "fresh"
				if broken {
					if mode == "body" {
						w.Header().Set("Content-Type", "application/dns-message")
						w.Header().Set("Content-Length", fmt.Sprint(len(answer)))
						_, _ = w.Write(answer[:len(answer)/2])
						w.(http.Flusher).Flush()
					}
					conn, _, e := w.(http.Hijacker).Hijack()
					if e == nil {
						_ = conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "application/dns-message")
				_, _ = w.Write(answer)
			}))
			t.Cleanup(s.Close)
			u, _ := url.Parse(s.URL)
			host, port, _ := net.SplitHostPort(u.Host)
			var n int
			_, _ = fmt.Sscan(port, &n)
			server := sbObject{"tag": "fixture", "type": "https", "server": host, "server_port": n}
			b, _ := mhTestDNSBridge(t, server)
			c, e := b.dohClient(0, (&net.Dialer{}).DialContext)
			if e != nil {
				t.Fatal(e)
			}
			c.Transport.(*http.Transport).TLSClientConfig = s.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			q := new(dnsmessage.Msg)
			q.SetQuestion("warm.example.", dnsmessage.TypeA)
			if mode != "fresh" {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_, e := b.exchange(ctx, q, server, false)
				cancel()
				if e != nil {
					t.Fatal(e)
				}
			}
			q.SetQuestion("retry.example.", dnsmessage.TypeA)
			if mode == "update" {
				q.Opcode = dnsmessage.OpcodeUpdate
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			a, e := b.exchange(ctx, q, server, false)
			if mode == "fresh" || mode == "update" {
				want := int32(1)
				if mode == "update" {
					want = 2
				}
				if e == nil || requests.Load() != want {
					t.Fatal("unsafe or repeated retry", requests.Load(), e)
				}
			} else if e != nil || !mhValidDNSAnswer(q, a) || requests.Load() != 3 {
				t.Fatal("dead reused DoH connection was not rebuilt once", a, e, requests.Load())
			}
		})
	}
}
