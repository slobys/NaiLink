// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	dnsmessage "github.com/miekg/dns"
)

const mhDNSIdleTimeout = 30 * time.Second

type mhDNSWireKey struct {
	address, transport string
	proxyPort          int
}
type mhDNSWireConn struct {
	key    mhDNSWireKey
	conn   net.Conn
	idle   bool
	timer  *time.Timer
	serial uint64
}

// One lease per DNS stream; responses from different queries never interleave.
// Active requests are limited by the bridge, with at most 16 idle sockets total.
type mhDNSWirePool struct {
	mu     sync.Mutex
	closed bool
	conns  map[*mhDNSWireConn]struct{}
}

type mhDNSTrackedConn struct {
	net.Conn
	pool  *mhDNSWirePool
	entry *mhDNSWireConn
}

func (c *mhDNSTrackedConn) Close() error { c.pool.put(c.entry, false); return nil }

func (p *mhDNSWirePool) track(key mhDNSWireKey, c net.Conn) (net.Conn, error) {
	e, err := p.add(key, c)
	if err != nil {
		return nil, err
	}
	return &mhDNSTrackedConn{Conn: c, pool: p, entry: e}, nil
}

func (p *mhDNSWirePool) take(key mhDNSWireKey) (*mhDNSWireConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, net.ErrClosed
	}
	for e := range p.conns {
		if e.idle && e.key == key {
			e.idle = false
			e.timer.Stop()
			e.timer = nil
			return e, nil
		}
	}
	return nil, nil
}
func (p *mhDNSWirePool) add(key mhDNSWireKey, c net.Conn) (*mhDNSWireConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = c.Close()
		return nil, net.ErrClosed
	}
	if p.conns == nil {
		p.conns = map[*mhDNSWireConn]struct{}{}
	}
	e := &mhDNSWireConn{key: key, conn: c}
	p.conns[e] = struct{}{}
	return e, nil
}
func (p *mhDNSWirePool) put(e *mhDNSWireConn, keep bool) {
	p.mu.Lock()
	_, exists := p.conns[e]
	if !exists {
		p.mu.Unlock()
		return
	}
	idle, matching := 0, 0
	for c := range p.conns {
		if c.idle {
			idle++
			if c.key == e.key {
				matching++
			}
		}
	}
	if !keep || p.closed || idle >= 16 || matching >= 2 {
		delete(p.conns, e)
		p.mu.Unlock()
		_ = e.conn.Close()
		return
	}
	e.idle = true
	e.serial++
	serial := e.serial
	e.timer = time.AfterFunc(mhDNSIdleTimeout, func() {
		p.mu.Lock()
		_, exists := p.conns[e]
		expire := exists && e.idle && e.serial == serial
		if expire {
			delete(p.conns, e)
		}
		p.mu.Unlock()
		if expire {
			_ = e.conn.Close()
		}
	})
	p.mu.Unlock()
}
func (p *mhDNSWirePool) close() {
	p.mu.Lock()
	p.closed = true
	entries := p.conns
	p.conns = nil
	for e := range entries {
		if e.timer != nil {
			e.timer.Stop()
		}
	}
	p.mu.Unlock()
	for e := range entries {
		_ = e.conn.Close()
	}
}
func mhDNSReadOnly(q *dnsmessage.Msg) bool {
	return len(q.Question) == 1 && q.Opcode == dnsmessage.OpcodeQuery && q.Question[0].Qtype != dnsmessage.TypeAXFR && q.Question[0].Qtype != dnsmessage.TypeIXFR && q.IsTsig() == nil
}
func mhDNSStaleConnection(e error) bool {
	return errors.Is(e, io.EOF) || errors.Is(e, io.ErrUnexpectedEOF) || errors.Is(e, net.ErrClosed) || errors.Is(e, syscall.ECONNRESET) || errors.Is(e, syscall.EPIPE)
}

type mhDNSWireResult struct {
	answer  *dnsmessage.Msg
	err     error
	entry   *mhDNSWireConn
	stopped bool
}

func (b *mhDNSBridge) finishWire(result mhDNSWireResult, keep bool) (*dnsmessage.Msg, error) {
	if result.entry != nil {
		b.wire.put(result.entry, keep && result.err == nil && result.stopped)
	}
	return result.answer, result.err
}

func (b *mhDNSBridge) queryWire(ctx context.Context, q *dnsmessage.Msg, key mhDNSWireKey, entry *mhDNSWireConn, connect func(context.Context) (net.Conn, error)) (result mhDNSWireResult) {
	result.entry = entry
	if result.err = ctx.Err(); result.err != nil {
		return
	}
	if entry == nil {
		conn, err := connect(ctx)
		if err != nil {
			result.err = err
			return
		}
		entry, result.err = b.wire.add(key, conn)
		if result.err != nil {
			return
		}
	}
	result.entry = entry
	stop := context.AfterFunc(ctx, func() { _ = entry.conn.Close() })
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = entry.conn.SetDeadline(deadline)
	udpSize := uint16(dnsmessage.MinMsgSize)
	if opt := q.IsEdns0(); opt != nil && opt.UDPSize() > udpSize {
		udpSize = opt.UDPSize()
	}
	client := &dnsmessage.Conn{Conn: entry.conn, UDPSize: udpSize}
	result.err = client.WriteMsg(q)
	if result.err == nil {
		result.answer, result.err = client.ReadMsg()
	}
	if result.err == nil && !mhValidDNSAnswer(q, result.answer) {
		result.err = errors.New("invalid_dns_reply")
	}
	result.stopped = stop()
	if ctx.Err() != nil {
		result.err = ctx.Err()
	}
	return
}

func (b *mhDNSBridge) exchangeWire(ctx context.Context, q *dnsmessage.Msg, key mhDNSWireKey, connect func(context.Context) (net.Conn, error)) (*dnsmessage.Msg, error) {
	// Reconnection and any speculative backup share the caller's original budget.
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
	}
	reusable := key.transport != "udp" && mhDNSReadOnly(q)
	var entry *mhDNSWireConn
	if reusable {
		var err error
		entry, err = b.wire.take(key)
		if err != nil {
			return nil, err
		}
	}
	if entry == nil {
		return b.finishWire(b.queryWire(ctx, q, key, nil, connect), reusable)
	}

	// Only a read-only query on an established stream may use one same-path
	// backup. Keep the original read alive: a slow valid response must not lose
	// its deadline merely because a middlebox blackhole is also possible.
	lifetime, cancel := context.WithCancel(ctx)
	first := make(chan mhDNSWireResult, 1)
	go func() { first <- b.queryWire(lifetime, q, key, entry, connect) }()
	var backup chan mhDNSWireResult
	started := false
	var backupErr error
	delay := 2 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		budget := time.Until(deadline)
		if budget < time.Second {
			delay = 0
		} else {
			delay = min(delay, budget/2)
		}
	}
	var timer *time.Timer
	var tick <-chan time.Time
	if delay > 0 {
		timer = time.NewTimer(delay)
		tick = timer.C
		defer timer.Stop()
	}
	defer func() {
		cancel()
		if first != nil {
			b.finishWire(<-first, false)
		}
		if backup != nil {
			b.finishWire(<-backup, false)
		}
	}()
	startBackup := func() {
		if started || ctx.Err() != nil {
			return
		}
		started = true
		tick = nil
		backup = make(chan mhDNSWireResult, 1)
		ch := backup
		go func() { ch <- b.queryWire(lifetime, q, key, nil, connect) }()
	}
	for {
		select {
		case result := <-first:
			first = nil
			if result.err == nil {
				return b.finishWire(result, true)
			}
			b.finishWire(result, false)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// Malformed responses, updates and caller cancellation are not retried.
			if !mhDNSStaleConnection(result.err) {
				var timeout net.Error
				if !started || !errors.As(result.err, &timeout) || !timeout.Timeout() {
					return nil, result.err
				}
			}
			startBackup()
			if backup == nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, backupErr
			}
		case result := <-backup:
			backup = nil
			if result.err == nil {
				return b.finishWire(result, true)
			}
			b.finishWire(result, false)
			backupErr = result.err
			if first == nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, result.err
			}
		case <-tick:
			startBackup()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (b *mhDNSBridge) dohClient(proxyPort int, dial func(context.Context, string, string) (net.Conn, error)) (*http.Client, error) {
	b.httpMu.Lock()
	defer b.httpMu.Unlock()
	if b.httpClosed {
		return nil, net.ErrClosed
	}
	if c := b.httpClients[proxyPort]; c != nil {
		return c, nil
	}
	// A bridge has one direct route and at most 16 configured DNS detours.
	if len(b.httpClients) >= 17 {
		return nil, errors.New("mihomo_dns_unsupported")
	}
	if b.httpClients == nil {
		b.httpClients = map[int]*http.Client{}
	}
	trackedDial := func(ctx context.Context, network, address string) (net.Conn, error) {
		c, e := dial(ctx, network, address)
		if e != nil {
			return nil, e
		}
		return b.wire.track(mhDNSKey("https", address, proxyPort), c)
	}
	transport := &http.Transport{Proxy: nil, DialContext: trackedDial, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, IdleConnTimeout: mhDNSIdleTimeout, MaxIdleConns: 2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4}
	client := &http.Client{Transport: transport, Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	b.httpClients[proxyPort] = client
	return client, nil
}
func (b *mhDNSBridge) closeHTTP() {
	b.httpMu.Lock()
	b.httpClosed = true
	clients := b.httpClients
	b.httpClients = nil
	b.httpMu.Unlock()
	for _, c := range clients {
		c.CloseIdleConnections()
	}
}
func (b *mhDNSBridge) exchangeHTTPS(ctx context.Context, q *dnsmessage.Msg, address, path string, proxyPort int, dial func(context.Context, string, string) (net.Conn, error)) (*dnsmessage.Msg, error) {
	if path == "" {
		path = "/dns-query"
	}
	payload, e := q.Pack()
	if e != nil {
		return nil, e
	}
	client, e := b.dohClient(proxyPort, dial)
	if e != nil {
		return nil, e
	}
	for attempt := 0; attempt < 2; attempt++ {
		request, e := http.NewRequestWithContext(ctx, "POST", "https://"+address+path, bytes.NewReader(payload))
		if e != nil {
			return nil, e
		}
		request.Header.Set("Content-Type", "application/dns-message")
		request.Header.Set("Accept", "application/dns-message")
		var reused atomic.Bool
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }}))
		response, e := client.Do(request)
		if e != nil {
			if attempt == 0 && reused.Load() && ctx.Err() == nil && mhDNSReadOnly(q) && mhDNSStaleConnection(e) {
				if retry, ok := mhDNSFreshHTTP(client); ok {
					client = retry
					defer client.CloseIdleConnections()
					continue
				}
			}
			return nil, e
		}
		media, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if response.StatusCode != http.StatusOK || parseErr != nil || media != "application/dns-message" {
			_ = response.Body.Close()
			return nil, errors.New("dns_https_failed")
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 65536))
		_ = response.Body.Close()
		if readErr != nil {
			if attempt == 0 && reused.Load() && ctx.Err() == nil && mhDNSReadOnly(q) && mhDNSStaleConnection(readErr) {
				if retry, ok := mhDNSFreshHTTP(client); ok {
					client = retry
					defer client.CloseIdleConnections()
					continue
				}
			}
			return nil, readErr
		}
		if len(raw) > 65535 {
			return nil, errors.New("invalid_dns_reply")
		}
		answer := new(dnsmessage.Msg)
		if answer.Unpack(raw) != nil || !mhValidDNSAnswer(q, answer) {
			return nil, errors.New("invalid_dns_reply")
		}
		return answer, nil
	}
	return nil, errors.New("dns_https_failed")
}

// Include the detour in a stream key even if two upstreams share an address.
func mhDNSKey(typ, address string, proxyPort int) mhDNSWireKey {
	return mhDNSWireKey{address: address, transport: typ, proxyPort: proxyPort}
}

func mhDNSFreshHTTP(client *http.Client) (*http.Client, bool) {
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		return nil, false
	}
	fresh := transport.Clone()
	fresh.DisableKeepAlives = true
	copy := *client
	copy.Transport = fresh
	return &copy, true
}
