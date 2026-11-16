// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"fmt"
	"net"
	"testing"
	"time"

	dnsmessage "github.com/miekg/dns"
)

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
