// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	"github.com/daeuniverse/dae/component/outbound/dialer"
	daeConfig "github.com/daeuniverse/dae/config"
	D "github.com/daeuniverse/outbound/dialer"
	"github.com/daeuniverse/outbound/netproxy"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

func chainConnectProxy(t *testing.T, destination string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != destination {
			http.Error(w, "unexpected target", http.StatusForbidden)
			return
		}
		remote, err := net.DialTimeout("tcp", destination, time.Second)
		if err != nil {
			http.Error(w, "fixture unavailable", http.StatusBadGateway)
			return
		}
		defer remote.Close()
		local, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer local.Close()
		hits.Add(1)
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(remote, buffered)
			_ = remote.Close()
			close(done)
		}()
		_, _ = io.Copy(local, remote)
		_ = local.Close()
		<-done
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRouterChainTrafficAndEntryFailure(t *testing.T) {
	for _, engine := range []string{"dae", "sing-box", "mihomo"} {
		t.Run(engine, func(t *testing.T) {
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "via both hops")
			}))
			defer target.Close()
			var entries, landings atomic.Int32
			landing := chainConnectProxy(t, strings.TrimPrefix(target.URL, "https://"), &landings)
			entry := chainConnectProxy(t, strings.TrimPrefix(landing.URL, "http://"), &entries)
			link := landing.URL + "#landing -> " + entry.URL + "#entry"
			log := logrus.New()
			log.SetOutput(io.Discard)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			transport := target.Client().Transport.(*http.Transport).Clone()
			transport.DisableKeepAlives = true
			if engine == "dae" {
				options := &dialer.GlobalOption{Log: log, ExtraOption: D.ExtraOption{TlsImplementation: "tls"}}
				d, err := dialer.NewFromLinkContext(ctx, options, dialer.InstanceOption{DisableCheck: true}, link, "")
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
					conn, err := d.DialContext(ctx, network, address)
					if err != nil {
						return nil, err
					}
					return &netproxy.FakeNetConn{Conn: conn}, nil
				}
			} else {
				binary, key := &routerSingBoxBinary, "DAED_TEST_SINGBOX"
				if engine == "mihomo" {
					binary, key = &routerMihomoBinary, "DAED_TEST_MIHOMO"
				}
				path := os.Getenv(key)
				if path == "" {
					t.Skip("optional core binary required")
				}
				oldBinary, oldDir := *binary, routerCoreRuntimeDir
				*binary, routerCoreRuntimeDir = path, filepath.Join(t.TempDir(), "runtime")
				defer func() { *binary, routerCoreRuntimeDir = oldBinary, oldDir }()
				conf, dirs := coreFixtureConfig(t)
				conf.Node = []daeConfig.KeyableString{daeConfig.KeyableString("fixture:" + link)}
				conf.Group[0].Policy = "fixed"
				conf.Global.WanInterface = []string{"lo"}
				conf.Routing.Rules = nil
				conf.Dns.Upstream = []daeConfig.KeyableString{"test:udp://127.0.0.1:9"}
				conf.Dns.Routing.Request.Rules = nil
				conf.Dns.Routing.Request.Fallback = "test"
				session, err := newRouterNativeSession(ctx, log, conf, dirs, false, engine)
				if err != nil {
					t.Fatal(err)
				}
				defer routerStopSession(session)
				if err = session.Ready(ctx); err != nil {
					t.Fatal(err)
				}
				ports := session.(*routerSingBoxSession).ports
				d, err := proxy.SOCKS5("tcp", routercore.SocksAddress(ports.Mixed), &proxy.Auth{User: "nailink", Password: ports.Secret}, &net.Dialer{})
				if err != nil {
					t.Fatal(err)
				}
				transport.DialContext = d.(proxy.ContextDialer).DialContext
			}
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			response, err := client.Get(target.URL)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || string(body) != "via both hops" || entries.Load() == 0 || landings.Load() == 0 {
				t.Fatalf("expected a relay at both hops, got entry=%d landing=%d, err=%v", entries.Load(), landings.Load(), err)
			}
			entry.Close()
			before := landings.Load()
			if response, err = client.Get(target.URL); err == nil {
				response.Body.Close()
				t.Fatal("traffic bypassed a stopped entry")
			}
			if landings.Load() != before {
				t.Fatal("traffic reached landing after the entry stopped")
			}
		})
	}
}
