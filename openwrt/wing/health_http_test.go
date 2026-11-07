// SPDX-License-Identifier: AGPL-3.0-only
package dialer

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterHealthHTTPReusesSmallResponses(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		status             int
		chunked            bool
		wantAlive          bool
	}{
		{"body", "GET", "/", 200, false, true},
		{"chunked", "GET", "/", 200, true, true},
		{"head", "HEAD", "/", 200, false, true},
		{"empty", "GET", "/generate_204", 204, false, true},
		{"allowed-client-error", "GET", "/", 403, false, true},
		{"server-error", "GET", "/", 500, false, false},
		{"unexpected-status", "GET", "/generate_204", 200, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var connections, requests atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(tc.status)
				if tc.chunked {
					w.(http.Flusher).Flush()
				}
				if tc.status != 204 {
					io.WriteString(w, "fixture response\n")
				}
			}))
			server.Config.ConnState = func(_ net.Conn, s http.ConnState) {
				if s == http.StateNew {
					connections.Add(1)
				}
			}
			server.Start()
			defer server.Close()
			d := newTestDialer(t)
			opt, err := ParseTcpCheckOption(context.Background(), []string{server.URL + tc.path, "127.0.0.1"}, tc.method, "udp")
			if err != nil {
				t.Fatal(err)
			}
			for range 20 {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				ok, err := d.HttpCheck(ctx, IdxTcp4, opt.Url, opt.Ip4, opt.Method, 0, false)
				cancel()
				if ok != tc.wantAlive || (err == nil) != tc.wantAlive {
					t.Fatal("status classification changed", ok, err)
				}
			}
			d.Close()
			t.Logf("requests=%d connections=%d", requests.Load(), connections.Load())
			if requests.Load() != 20 || connections.Load() != 1 {
				t.Fatal("small response prevented connection reuse")
			}
		})
	}
}

func TestRouterHealthHTTPDrainDoesNotWaitForStalledBody(t *testing.T) {
	for _, timeout := range []time.Duration{time.Second, 20 * time.Millisecond} {
		t.Run(timeout.String(), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "8")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			d := newTestDialer(t)
			opt, err := ParseTcpCheckOption(context.Background(), []string{server.URL, "127.0.0.1"}, "GET", "udp")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			start := time.Now()
			ok, err := d.HttpCheck(ctx, IdxTcp4, opt.Url, opt.Ip4, opt.Method, 0, false)
			if !ok || err != nil {
				t.Fatal("optional drain changed header-based result", ok, err)
			}
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Fatal("drain held probe until body arrived", elapsed)
			}
		})
	}
}

type routerHealthBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *routerHealthBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *routerHealthBody) Close() error { b.closed = true; return nil }

func TestRouterHealthHTTPDrainBounds(t *testing.T) {
	for _, length := range []int64{-1, 1 << 20} {
		body := &routerHealthBody{Reader: strings.NewReader(strings.Repeat("x", 1<<20))}
		ctx, cancel := context.WithCancel(context.Background())
		routerFinishHealthResponse(&http.Response{Body: body, ContentLength: length}, cancel)
		if !body.closed || body.read > 4097 || (length > 4096 && body.read != 0) {
			t.Fatal("unbounded response drain", length, body.read, body.closed)
		}
		if ctx.Err() != nil {
			t.Fatal("completed drain cancelled caller")
		}
		cancel()
	}
}

func TestRouterHealthHTTPSilentSSStillCancels(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		// Accept the request but never send the encrypted response salt.
		_, _ = io.Copy(io.Discard, conn)
	}()
	key := base64.RawURLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:fixture-only"))
	d, err := NewFromLink(newTestDialer(t).GlobalOption, InstanceOption{DisableCheck: true}, "ss://"+key+"@"+listener.Addr().String(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	opt, err := ParseTcpCheckOption(context.Background(), []string{"http://probe.invalid/generate_204", "127.0.0.1"}, "GET", "udp")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	ok, err := d.HttpCheck(ctx, IdxTcp4, opt.Url, opt.Ip4, opt.Method, 0, false)
	if ok || err == nil || time.Since(start) > time.Second {
		t.Fatal("silent SS did not release the cancelled HTTP probe", ok, err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled probe did not close the SS connection")
	}
}
