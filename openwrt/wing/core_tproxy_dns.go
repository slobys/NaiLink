// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	dnsmessage "github.com/miekg/dns"
)

// Preserve the local IPv6 destination via miekg/dns packet-info handling.
// A wildcard sing-box direct UDP inbound may choose another LAN address when
// replying, escaping REDIRECT's reverse NAT and exposing its private port.
// The upstream stays loopback-only and still owns every DNS policy decision.
type routerTPDNSBridge struct {
	ctx     context.Context
	cancel  context.CancelFunc
	servers []*dnsmessage.Server
	once    sync.Once
}

func startRouterTPDNSBridge(port, upstreamPort int) (*routerTPDNSBridge, error) {
	address := net.JoinHostPort(routerTProxyListenAddress(), fmt.Sprint(port))
	tcp, err := net.Listen("tcp", address)
	if err != nil {
		return nil, errors.New("core_ports_unavailable")
	}
	udp, err := net.ListenPacket("udp", address)
	if err != nil {
		_ = tcp.Close()
		return nil, errors.New("core_ports_unavailable")
	}
	return serveRouterTPDNSBridge(tcp, udp, upstreamPort), nil
}

func serveRouterTPDNSBridge(tcp net.Listener, udp net.PacketConn, upstreamPort int) *routerTPDNSBridge {
	ctx, cancel := context.WithCancel(context.Background())
	b := &routerTPDNSBridge{ctx: ctx, cancel: cancel}
	slots := make(chan struct{}, 64)
	handler := dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
		reply := new(dnsmessage.Msg)
		reply.SetRcode(q, dnsmessage.RcodeServerFailure)
		if len(q.Question) != 1 || q.Response {
			reply.SetRcode(q, dnsmessage.RcodeFormatError)
			_ = w.WriteMsg(reply)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			_ = w.WriteMsg(reply)
			return
		}
		network := "udp"
		if _, ok := w.RemoteAddr().(*net.TCPAddr); ok {
			network = "tcp"
		}
		attempt, stop := context.WithTimeout(b.ctx, 5*time.Second)
		defer stop()
		if answer, err := routerTPDNSExchange(attempt, network, net.JoinHostPort("127.0.0.1", fmt.Sprint(upstreamPort)), q, 750*time.Millisecond); err == nil {
			reply = answer
		}
		_ = w.WriteMsg(reply)
	})
	b.servers = []*dnsmessage.Server{
		{Listener: tcp, Net: "tcp", Handler: handler, ReadTimeout: 6 * time.Second, WriteTimeout: 6 * time.Second, MaxTCPQueries: 16},
		{PacketConn: udp, Net: "udp", Handler: handler, UDPSize: 4096, ReadTimeout: 6 * time.Second, WriteTimeout: 6 * time.Second},
	}
	for _, server := range b.servers {
		go func(server *dnsmessage.Server) { _ = server.ActivateAndServe() }(server)
	}
	return b
}

// A cold native DNS exchange can stop replying while later queries work. Keep
// its original deadline and allow one same-protocol, same-loopback backup for
// ordinary reads. Valid replies, including rejection and truncation, win intact.
func routerTPDNSExchange(ctx context.Context, network, address string, q *dnsmessage.Msg, delay time.Duration) (*dnsmessage.Msg, error) {
	ctx, cancel := context.WithCancel(ctx)
	type result struct {
		answer *dnsmessage.Msg
		err    error
	}
	results := make(chan result, 2)
	pending := 0
	start := func() {
		pending++
		go func() {
			client := &dnsmessage.Client{Net: network, Timeout: 5 * time.Second, UDPSize: 4096}
			conn, err := client.DialContext(ctx, address)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer conn.Close()
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()
			answer, _, err := client.ExchangeWithConnContext(ctx, q.Copy(), conn)
			if err == nil && !mhValidDNSAnswer(q, answer) {
				err = errors.New("invalid_dns_reply")
			}
			results <- result{answer, err}
		}()
	}
	start()
	defer func() {
		cancel()
		for pending > 0 {
			<-results
			pending--
		}
	}()
	var tick <-chan time.Time
	if mhDNSReadOnly(q) {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		tick = timer.C
	}
	for {
		select {
		case r := <-results:
			pending--
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
				return nil, context.DeadlineExceeded
			}
			if r.err == nil {
				return r.answer, nil
			}
			if pending == 0 {
				return nil, r.err
			}
		case <-tick:
			tick = nil
			if ctx.Err() == nil {
				start()
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (b *routerTPDNSBridge) Close() {
	b.once.Do(func() {
		b.cancel()
		for _, server := range b.servers {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = server.ShutdownContext(ctx)
			cancel()
			if server.Listener != nil {
				_ = server.Listener.Close()
			}
			if server.PacketConn != nil {
				_ = server.PacketConn.Close()
			}
		}
	})
}
