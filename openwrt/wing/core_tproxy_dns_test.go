// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	dnsmessage "github.com/miekg/dns"
)

func TestTProxyDNSExchangeBoundsBackupAndPreservesPolicy(t *testing.T) {
	for _, network := range []string{"udp", "tcp"} {
		for _, scenario := range []string{"lost-first", "slow-original", "denied", "truncated", "update", "canceled"} {
			t.Run(network+"/"+scenario, func(t *testing.T) {
				tcp, udp, err := routerLoopbackPair(net.Listen, net.ListenPacket)
				if err != nil {
					t.Fatal(err)
				}
				defer tcp.Close()
				defer udp.Close()
				var calls atomic.Int32
				backupStarted := make(chan struct{})
				release := make(chan struct{})
				handler := dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
					n := calls.Add(1)
					if scenario == "canceled" || scenario == "lost-first" && n == 1 {
						return
					}
					if scenario == "slow-original" {
						if n == 1 {
							select {
							case <-backupStarted:
							case <-release:
								return
							}
						} else {
							close(backupStarted)
							<-release
							return
						}
					}
					if scenario == "update" {
						time.Sleep(50 * time.Millisecond)
					}
					a := mhTestDNSAnswer(q)
					if scenario == "denied" {
						a.Rcode, a.Answer = dnsmessage.RcodeNameError, nil
					}
					if scenario == "truncated" {
						a.Truncated = true
					}
					_ = w.WriteMsg(a)
				})
				server := &dnsmessage.Server{Net: network, Handler: handler, MsgAcceptFunc: func(dnsmessage.Header) dnsmessage.MsgAcceptAction { return dnsmessage.MsgAccept }}
				if network == "tcp" {
					server.Listener = tcp
				} else {
					server.PacketConn = udp
				}
				started := make(chan struct{})
				server.NotifyStartedFunc = func() { close(started) }
				go func() { _ = server.ActivateAndServe() }()
				defer server.Shutdown()
				defer close(release)
				<-started
				q := new(dnsmessage.Msg)
				q.SetQuestion("retry.test.", dnsmessage.TypeA)
				if scenario == "update" {
					q.Opcode = dnsmessage.OpcodeUpdate
				}
				budget := time.Second
				if scenario == "canceled" {
					budget = 300 * time.Millisecond
				}
				ctx, cancel := context.WithTimeout(context.Background(), budget)
				defer cancel()
				a, err := routerTPDNSExchange(ctx, network, tcp.Addr().String(), q, 20*time.Millisecond)
				if scenario == "canceled" {
					if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 2 {
						t.Fatal("original deadline or two-query limit lost", err, calls.Load())
					}
					return
				}
				if err != nil || !mhValidDNSAnswer(q, a) {
					t.Fatal("valid original or backup reply lost", a, err)
				}
				wantCalls := int32(1)
				if scenario == "lost-first" || scenario == "slow-original" {
					wantCalls = 2
				}
				if calls.Load() != wantCalls || scenario == "denied" && a.Rcode != dnsmessage.RcodeNameError || scenario == "truncated" && !a.Truncated {
					t.Fatal("policy, truncation or backup count changed", a, calls.Load())
				}
			})
		}
	}
}

func TestTProxyDNSIngressPreservesTransportReplyAndCloses(t *testing.T) {
	backendTCP, backendUDP, err := routerLoopbackPair(net.Listen, net.ListenPacket)
	if err != nil {
		t.Fatal(err)
	}
	defer backendTCP.Close()
	defer backendUDP.Close()
	handler := dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
		a := new(dnsmessage.Msg)
		a.SetReply(q)
		if q.Question[0].Name == "denied.test." {
			a.Rcode = dnsmessage.RcodeNameError
		} else if _, tcp := w.RemoteAddr().(*net.TCPAddr); !tcp && q.Question[0].Name == "large.test." {
			a.Truncated = true
		} else {
			a.Answer = []dnsmessage.RR{&dnsmessage.A{Hdr: dnsmessage.RR_Header{Name: q.Question[0].Name, Rrtype: dnsmessage.TypeA, Class: dnsmessage.ClassINET, Ttl: 30}, A: net.ParseIP("192.0.2.8")}}
		}
		_ = w.WriteMsg(a)
	})
	for _, server := range []*dnsmessage.Server{{Listener: backendTCP, Net: "tcp", Handler: handler}, {PacketConn: backendUDP, Net: "udp", Handler: handler}} {
		started := make(chan struct{})
		server.NotifyStartedFunc = func() { close(started) }
		go func() { _ = server.ActivateAndServe() }()
		t.Cleanup(func() { _ = server.Shutdown() })
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("test backend did not start")
		}
	}
	for _, host := range []string{"127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			tcp, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
			if err != nil {
				if host == "::1" {
					t.Skip("IPv6 loopback unavailable")
				}
				t.Fatal(err)
			}
			address := net.JoinHostPort(host, fmt.Sprint(tcp.Addr().(*net.TCPAddr).Port))
			udp, err := net.ListenPacket("udp", address)
			if err != nil {
				_ = tcp.Close()
				t.Fatal(err)
			}
			bridge := serveRouterTPDNSBridge(tcp, udp, backendTCP.Addr().(*net.TCPAddr).Port)
			t.Cleanup(bridge.Close)
			for _, network := range []string{"udp", "tcp"} {
				for _, name := range []string{"answer.test.", "denied.test.", "large.test."} {
					q := new(dnsmessage.Msg)
					q.SetQuestion(name, dnsmessage.TypeA)
					a, _, err := (&dnsmessage.Client{Net: network, Timeout: time.Second}).Exchange(q, address)
					if err != nil || a == nil || a.Id != q.Id || len(a.Question) != 1 || a.Question[0] != q.Question[0] {
						t.Fatalf("%s %s: reply=%v err=%v", network, name, a, err)
					}
					if name == "denied.test." {
						if a.Rcode != dnsmessage.RcodeNameError {
							t.Fatal("upstream policy rejection was changed")
						}
					} else if name == "large.test." && network == "udp" {
						if !a.Truncated {
							t.Fatal("UDP truncation was lost; clients cannot retry over TCP")
						}
					} else if len(a.Answer) != 1 || a.Answer[0].(*dnsmessage.A).A.String() != "192.0.2.8" {
						t.Fatal("upstream answer was changed")
					}
				}
			}
			bridge.Close()
			bridge.Close()
			nextTCP, err := net.Listen("tcp", address)
			if err != nil {
				t.Fatal("TCP listener leaked:", err)
			}
			defer nextTCP.Close()
			nextUDP, err := net.ListenPacket("udp", address)
			if err != nil {
				t.Fatal("UDP listener leaked:", err)
			}
			defer nextUDP.Close()
		})
	}
}
