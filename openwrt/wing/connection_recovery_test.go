// SPDX-License-Identifier: AGPL-3.0-only
package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/daeuniverse/dae/common/consts"
	"github.com/daeuniverse/dae/component/dns"
	ob "github.com/daeuniverse/dae/component/outbound"
	"github.com/daeuniverse/dae/component/outbound/dialer"
	D "github.com/daeuniverse/outbound/dialer"
	"github.com/daeuniverse/outbound/netproxy"
	"github.com/daeuniverse/outbound/protocol/direct"
	dnsmessage "github.com/miekg/dns"
	"github.com/sirupsen/logrus"
)

type routerTestDialFunc func(context.Context, string, string) (netproxy.Conn, error)

func (f routerTestDialFunc) DialContext(ctx context.Context, network, address string) (netproxy.Conn, error) {
	return f(ctx, network, address)
}

func routerTestTCPGroup(t *testing.T, policy consts.DialerSelectionPolicy, fns ...routerTestDialFunc) (*ControlPlane, []*dialer.Dialer) {
	t.Helper()
	log := logrus.New()
	log.SetOutput(io.Discard)
	option := &dialer.GlobalOption{Log: log, CheckInterval: time.Second}
	var ds []*dialer.Dialer
	var annotations []*dialer.Annotation
	for i, fn := range fns {
		d := dialer.NewDialer(fn, option, dialer.InstanceOption{DisableCheck: true}, &dialer.Property{Property: D.Property{Protocol: "vless", Address: "proxy.example:443", Name: "test"}})
		ds = append(ds, d)
		annotations = append(annotations, &dialer.Annotation{AddLatency: time.Duration(i) * time.Second})
		t.Cleanup(func() { _ = d.Close() })
	}
	g := ob.NewDialerGroup(option, "router-recovery", ds, annotations, ob.DialerSelectionPolicy{Policy: policy}, func(bool, *dialer.NetworkType, bool) {})
	t.Cleanup(func() { _ = g.Close() })
	return newTestDialControlPlane(g), ds
}

func routerTestTCPParam() *proxyDialParam {
	return &proxyDialParam{Outbound: consts.OutboundUserDefinedMin, Src: netip.MustParseAddrPort("192.0.2.1:1234"), Dest: netip.MustParseAddrPort("198.51.100.1:443"), Network: "tcp"}
}

func TestRouterConnectionTCPFailover(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, syscall.ECONNREFUSED, syscall.ECONNRESET, io.ErrUnexpectedEOF} {
		t.Run(failure.Error(), func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			var first, backup atomic.Int32
			cp, ds := routerTestTCPGroup(t, consts.DialerSelectionPolicy_MinLastLatency,
				func(context.Context, string, string) (netproxy.Conn, error) { first.Add(1); return nil, failure },
				func(context.Context, string, string) (netproxy.Conn, error) { backup.Add(1); return client, nil })
			p := routerTestTCPParam()
			conn, res, err := cp.routeDial(context.Background(), p)
			if err != nil {
				t.Fatalf("backup did not recover first connection: %v", err)
			}
			defer conn.Close()
			if res.Dialer != ds[1] || first.Load() != 1 || backup.Load() != 1 {
				t.Fatal("must try one different node once")
			}
			if p.Excluded != nil {
				t.Fatal("caller routing parameters were mutated")
			}
			if !ds[0].MustGetAlive(&dialer.NetworkType{L4Proto: consts.L4ProtoStr_TCP, IpVersion: consts.IpVersionStr_4}) {
				t.Fatal("one destination failure globally blacklisted node")
			}
			// Application bytes are written only after selection succeeds, never replayed.
			got := make(chan string, 1)
			go func() {
				b := make([]byte, 4)
				_, e := io.ReadFull(server, b)
				if e != nil {
					got <- e.Error()
					return
				}
				got <- string(b)
			}()
			conn.SetWriteDeadline(time.Now().Add(time.Second))
			if _, err = conn.Write([]byte("once")); err != nil {
				t.Fatal(err)
			}
			if <-got != "once" {
				t.Fatal("application payload corrupted")
			}
		})
	}
}

func TestRouterConnectionTCPReservesBackupBudget(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	var backup atomic.Int32
	cp, _ := routerTestTCPGroup(t, consts.DialerSelectionPolicy_MinLastLatency,
		func(ctx context.Context, _, _ string) (netproxy.Conn, error) { <-ctx.Done(); return nil, ctx.Err() },
		func(ctx context.Context, _, _ string) (netproxy.Conn, error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			backup.Add(1)
			return client, nil
		})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	conn, _, err := cp.routeDial(ctx, routerTestTCPParam())
	if err != nil {
		t.Fatalf("first node consumed backup's deadline: %v", err)
	}
	defer conn.Close()
	if backup.Load() != 1 {
		t.Fatal("backup not reached")
	}
}

func TestRouterConnectionTCPSwitchesSoonerAfterRecentFastHealthCheck(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	var firstBudget time.Duration
	cp, ds := routerTestTCPGroup(t, consts.DialerSelectionPolicy_MinLastLatency,
		func(ctx context.Context, _, _ string) (netproxy.Conn, error) {
			deadline, _ := ctx.Deadline()
			firstBudget = time.Until(deadline)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		func(context.Context, string, string) (netproxy.Conn, error) { return client, nil })
	network := &dialer.NetworkType{L4Proto: consts.L4ProtoStr_TCP, IpVersion: consts.IpVersionStr_4}
	observation := dialer.DialerProbeObservationSnapshot{Alive: true, HasLatency: true, Latency: 40 * time.Millisecond, CheckedAt: time.Now()}
	snapshot := ds[0].HealthSnapshot()
	snapshot.Collections[network.Index()].LastProbe = observation
	ds[0].RestoreHealthSnapshot(snapshot)

	conn, res, err := cp.routeDial(context.Background(), routerTestTCPParam())
	if err != nil || res.Dialer != ds[1] || firstBudget <= 0 || firstBudget > 1500*time.Millisecond {
		t.Fatalf("fast node stall did not use a shorter budget and same-group backup: %v, budget=%v", err, firstBudget)
	}
	defer conn.Close()
	t.Logf("backup started after a primary budget of %s", firstBudget.Round(time.Millisecond))

	observation.Latency = 1200 * time.Millisecond
	if got := routerTCPPrimaryBudget(8*time.Second, time.Second, observation); got != 4*time.Second {
		t.Fatalf("slow node lost its original budget: %v", got)
	}
	observation.Latency, observation.CheckedAt = 40*time.Millisecond, time.Now().Add(-time.Minute)
	if got := routerTCPPrimaryBudget(8*time.Second, time.Second, observation); got != 4*time.Second {
		t.Fatalf("stale sample shortened the budget: %v", got)
	}
}

func TestRouterConnectionTCPLatePrimarySuccessUsesBackup(t *testing.T) {
	late, latePeer := net.Pipe()
	defer late.Close()
	defer latePeer.Close()
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	var backup atomic.Int32
	cp, ds := routerTestTCPGroup(t, consts.DialerSelectionPolicy_MinLastLatency,
		func(ctx context.Context, _, _ string) (netproxy.Conn, error) { <-ctx.Done(); return late, nil },
		func(context.Context, string, string) (netproxy.Conn, error) { backup.Add(1); return client, nil })
	ctx, cancel := context.WithTimeout(context.Background(), 160*time.Millisecond)
	defer cancel()
	conn, res, err := cp.routeDial(ctx, routerTestTCPParam())
	if conn != nil {
		defer conn.Close()
	}
	if err != nil || res.Dialer != ds[1] || backup.Load() != 1 {
		t.Fatalf("accepted primary after its deadline: %v, backup=%d", err, backup.Load())
	}
	if _, err = late.Write([]byte("x")); err == nil {
		t.Fatal("late primary socket was retained")
	}
}

func TestRouterConnectionTCPBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name           string
		policy         consts.DialerSelectionPolicy
		network        string
		err            error
		single, cancel bool
	}{
		{name: "fixed", policy: consts.DialerSelectionPolicy_Fixed, network: "tcp", err: syscall.ECONNREFUSED},
		{name: "single", policy: consts.DialerSelectionPolicy_MinLastLatency, network: "tcp", err: syscall.ECONNREFUSED, single: true},
		{name: "udp", policy: consts.DialerSelectionPolicy_MinLastLatency, network: "udp", err: syscall.ECONNREFUSED},
		{name: "certificate-or-config", policy: consts.DialerSelectionPolicy_MinLastLatency, network: "tcp", err: errors.New("certificate verification failed")},
		{name: "caller-cancel", policy: consts.DialerSelectionPolicy_MinLastLatency, network: "tcp", err: context.Canceled, cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var first, backup atomic.Int32
			fns := []routerTestDialFunc{func(context.Context, string, string) (netproxy.Conn, error) {
				first.Add(1)
				if tc.cancel {
					cancel()
				}
				return nil, tc.err
			}}
			if !tc.single {
				fns = append(fns, func(context.Context, string, string) (netproxy.Conn, error) { backup.Add(1); return nil, io.EOF })
			}
			cp, _ := routerTestTCPGroup(t, tc.policy, fns...)
			p := routerTestTCPParam()
			p.Network = tc.network
			_, _, err := cp.routeDial(ctx, p)
			if !errors.Is(err, tc.err) || first.Load() != 1 || backup.Load() != 0 {
				t.Fatalf("unexpected retry: %v, calls %d/%d", err, first.Load(), backup.Load())
			}
		})
	}
}

func TestRouterConnectionTCPCanceledSuccessIsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	cp, _ := routerTestTCPGroup(t, consts.DialerSelectionPolicy_Fixed, func(context.Context, string, string) (netproxy.Conn, error) { cancel(); return client, nil })
	conn, _, err := cp.routeDial(ctx, routerTestTCPParam())
	if conn != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dial returned connection: %v", err)
	}
	server.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 1)
	if _, err = server.Read(b); !errors.Is(err, io.EOF) {
		t.Fatalf("late socket leaked: %v", err)
	}
}

func routerTestDNSController(t *testing.T, forward func(context.Context, *dns.Upstream, dialArgument) (*dnsmessage.Msg, error)) *DnsController {
	t.Helper()
	original := dnsForwarderFactory
	t.Cleanup(func() { dnsForwarderFactory = original })
	dnsForwarderFactory = func(up *dns.Upstream, arg dialArgument, _ *logrus.Logger) (DnsForwarder, error) {
		return &stubDnsForwarder{forward: func(ctx context.Context, _ []byte) (*dnsmessage.Msg, error) { return forward(ctx, up, arg) }}, nil
	}
	log := logrus.New()
	log.SetOutput(io.Discard)
	ctrl := setTestDnsControllerRuntime(&DnsController{log: log}, func(rt *dnsControllerRuntimeState) {
		rt.bestDialerChooser = func(ctx context.Context, _ *udpRequest, up *dns.Upstream) (*dialArgument, error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return &dialArgument{l4proto: consts.L4ProtoStr_TCP}, nil
		}
	})
	t.Cleanup(func() { _ = ctrl.Close() })
	return ctrl
}

func TestRouterConnectionDNSBlackholeReservesTCP(t *testing.T) {
	var tcp atomic.Int32
	ctrl := routerTestDNSController(t, func(ctx context.Context, _ *dns.Upstream, arg dialArgument) (*dnsmessage.Msg, error) {
		if arg.l4proto == consts.L4ProtoStr_UDP {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		tcp.Add(1)
		return &dnsmessage.Msg{MsgHdr: dnsmessage.MsgHdr{Response: true}}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, used, err := ctrl.forwardWithFallback(ctx, &udpRequest{}, &dns.Upstream{Scheme: dns.UpstreamScheme_TCP_UDP}, &dialArgument{l4proto: consts.L4ProtoStr_UDP}, nil)
	if err != nil || used.l4proto != consts.L4ProtoStr_TCP || tcp.Load() != 1 {
		t.Fatalf("UDP consumed TCP recovery deadline: %v", err)
	}
}

func TestRouterConnectionDNSFastUDPNoTCP(t *testing.T) {
	var tcp atomic.Int32
	ctrl := routerTestDNSController(t, func(ctx context.Context, _ *dns.Upstream, arg dialArgument) (*dnsmessage.Msg, error) {
		if arg.l4proto == consts.L4ProtoStr_TCP {
			tcp.Add(1)
		}
		return &dnsmessage.Msg{}, nil
	})
	_, used, err := ctrl.forwardWithFallback(context.Background(), &udpRequest{}, &dns.Upstream{Scheme: dns.UpstreamScheme_TCP_UDP}, &dialArgument{l4proto: consts.L4ProtoStr_UDP}, nil)
	if err != nil || used.l4proto != consts.L4ProtoStr_UDP || tcp.Load() != 0 {
		t.Fatalf("healthy UDP incurred TCP query: %v", err)
	}
}

func TestRouterConnectionDNSCanceledDoesNotFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var tcp atomic.Int32
	ctrl := routerTestDNSController(t, func(context.Context, *dns.Upstream, dialArgument) (*dnsmessage.Msg, error) {
		cancel()
		return nil, context.Canceled
	})
	rt := ctrl.runtime()
	rt.bestDialerChooser = func(context.Context, *udpRequest, *dns.Upstream) (*dialArgument, error) {
		tcp.Add(1)
		return &dialArgument{l4proto: consts.L4ProtoStr_TCP}, nil
	}
	_, _, err := ctrl.forwardWithFallback(ctx, &udpRequest{}, &dns.Upstream{Scheme: dns.UpstreamScheme_TCP_UDP}, &dialArgument{l4proto: consts.L4ProtoStr_UDP}, nil)
	if !errors.Is(err, context.Canceled) || tcp.Load() != 0 {
		t.Fatalf("cancellation started fallback: %v / %d", err, tcp.Load())
	}
}

// Uses the actual UDP and pipelined TCP forwarders against loopback sockets.
// The UDP socket deliberately consumes no replies; TCP serves a real DNS answer.
func TestRouterConnectionDNSLoopbackBlackhole(t *testing.T) {
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp4", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	var tcpQueries atomic.Int32
	server := &dnsmessage.Server{Listener: tcp, Net: "tcp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, req *dnsmessage.Msg) {
		tcpQueries.Add(1)
		response := new(dnsmessage.Msg)
		response.SetReply(req)
		response.Answer = []dnsmessage.RR{&dnsmessage.A{Hdr: dnsmessage.RR_Header{Name: req.Question[0].Name, Rrtype: dnsmessage.TypeA, Class: dnsmessage.ClassINET, Ttl: 30}, A: net.IPv4(192, 0, 2, 77)}}
		_ = w.WriteMsg(response)
	})}
	done := make(chan error, 1)
	go func() { done <- server.ActivateAndServe() }()
	defer func() { _ = server.Shutdown(); <-done }()
	log := logrus.New()
	log.SetOutput(io.Discard)
	d := dialer.NewDialer(direct.SymmetricDirect, &dialer.GlobalOption{Log: log}, dialer.InstanceOption{DisableCheck: true}, &dialer.Property{})
	defer d.Close()
	target := netip.MustParseAddrPort(tcp.Addr().String())
	makeArg := func(proto consts.L4ProtoStr) *dialArgument {
		return &dialArgument{l4proto: proto, ipversion: consts.IpVersionStr_4, bestDialer: d, bestTarget: target}
	}
	ctrl := setTestDnsControllerRuntime(&DnsController{log: log}, func(rt *dnsControllerRuntimeState) {
		rt.bestDialerChooser = func(context.Context, *udpRequest, *dns.Upstream) (*dialArgument, error) {
			return makeArg(consts.L4ProtoStr_TCP), nil
		}
	})
	defer ctrl.Close()
	question := new(dnsmessage.Msg)
	question.SetQuestion("studio.example.", dnsmessage.TypeA)
	data, err := question.Pack()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	response, used, err := ctrl.forwardWithFallback(ctx, &udpRequest{}, &dns.Upstream{Scheme: dns.UpstreamScheme_TCP_UDP, Hostname: "127.0.0.1", Port: target.Port()}, makeArg(consts.L4ProtoStr_UDP), data)
	if err != nil {
		t.Fatalf("real DNS recovery failed: %v", err)
	}
	if used.l4proto != consts.L4ProtoStr_TCP || len(response.Answer) != 1 || tcpQueries.Load() != 1 {
		t.Fatal("expected one successful TCP DNS query")
	}
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Fatalf("DNS fallback too late: %v", elapsed)
	}
	t.Logf("silent UDP -> real TCP DNS answer in %v", time.Since(start))
}

func TestRouterConnectionDNSPureUDPBudgetUnchanged(t *testing.T) {
	for _, scheme := range []dns.UpstreamScheme{dns.UpstreamScheme_UDP, dns.UpstreamScheme_TCP} {
		if got := routerDNSPrimaryTimeout(context.Background(), &dns.Upstream{Scheme: scheme}, &dialArgument{l4proto: consts.L4ProtoStr_UDP}); got != consts.DefaultDialTimeout {
			t.Fatalf("changed explicit %s timeout: %v", scheme, got)
		}
	}
}

func TestRouterConnectionProtocolFailover(t *testing.T) {
	path := os.Getenv("DAED_PROTOCOL_CASES")
	if path == "" {
		t.Skip("independent VLESS server required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Target string
		Cases  []struct{ Name, Link, Failure string }
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, item := range fixture.Cases {
		if !strings.HasPrefix(item.Name, "vless-") || item.Failure != "" {
			continue
		}
		t.Run(item.Name, func(t *testing.T) {
			log := logrus.New()
			log.SetOutput(io.Discard)
			options := &dialer.GlobalOption{Log: log, ExtraOption: D.ExtraOption{TlsImplementation: "tls"}}
			d, err := dialer.NewFromLinkContext(context.Background(), options, dialer.InstanceOption{DisableCheck: true}, item.Link, "")
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			// Obtain a closed local port to exercise a real connect refusal.
			closed, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := closed.Addr().String()
			closed.Close()
			var first, backup atomic.Int32
			cp, _ := routerTestTCPGroup(t, consts.DialerSelectionPolicy_MinLastLatency,
				func(ctx context.Context, _, _ string) (netproxy.Conn, error) {
					first.Add(1)
					c, e := (&net.Dialer{}).DialContext(ctx, "tcp4", address)
					if e != nil {
						return nil, e
					}
					return c, nil
				},
				func(ctx context.Context, network, addr string) (netproxy.Conn, error) {
					backup.Add(1)
					return d.DialContext(ctx, network, addr)
				})
			transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				p := routerTestTCPParam()
				p.Dest = netip.MustParseAddrPort(addr)
				conn, _, e := cp.routeDial(ctx, p)
				if e != nil {
					return nil, e
				}
				return &netproxy.FakeNetConn{Conn: conn}, nil
			}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			resp, err := client.Get(fixture.Target + "/payload")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || string(body) != strings.Repeat("protocol-payload", 4096) || first.Load() != 1 || backup.Load() != 1 {
				t.Fatalf("real VLESS recovery/payload failed: %v (%d/%d)", err, first.Load(), backup.Load())
			}
		})
	}
}
