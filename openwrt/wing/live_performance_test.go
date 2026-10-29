// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"github.com/daeuniverse/dae-wing/routercore"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/pkg/config_parser"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// Explicit private fixture only. It has no TUN, database or system-state writes.
func TestLiveNodeDNSPerformance(t *testing.T) {
	file := os.Getenv("DAED_TEST_LIVE_PERFORMANCE")
	if file == "" {
		t.Skip("private live fixture not supplied")
	}
	var input struct {
		Link    string
		NodeDNS string
		Engines []string
	}
	raw, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(raw, &input) != nil {
		t.Fatal("invalid private fixture")
	}
	if input.Link == "" {
		t.Fatal("missing private node")
	}
	oldDir := routerCoreRuntimeDir
	routerCoreRuntimeDir = filepath.Join(filepath.Dir(file), "sessions")
	defer func() { routerCoreRuntimeDir = oldDir }()
	for _, engine := range input.Engines {
		if engine != "mihomo" && engine != "sing-box" {
			t.Fatal("invalid probe engine")
		}
		t.Run(engine, func(t *testing.T) {
			log := logrus.New()
			log.SetOutput(io.Discard)
			conf := &daeConfig.Config{
				Global:  daeConfig.Global{NailinkNodeDNS: routercore.PackNodeDNS(input.NodeDNS), WanInterface: []string{"auto"}, TcpCheckUrl: []string{"https://www.gstatic.com/generate_204"}, TcpCheckHttpMethod: "HEAD", DialMode: "domain", TlsImplementation: "tls"},
				Node:    []daeConfig.KeyableString{daeConfig.KeyableString("fixture:" + input.Link)},
				Group:   []daeConfig.Group{{Name: "probe", Policy: "fixed", Filter: [][]*config_parser.Function{{{Name: "name", Params: []*config_parser.Param{{Val: "fixture"}}}}}}},
				Routing: daeConfig.Routing{Fallback: "probe"},
			}
			conf.Dns.Upstream = []daeConfig.KeyableString{"bootstrap:udp://223.5.5.5:53"}
			conf.Dns.Routing.Request.Fallback = "bootstrap"
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			session, err := newRouterNativeSession(ctx, log, conf, []string{filepath.Dir(file)}, false, engine)
			if err != nil {
				t.Fatal("private core start failed")
			}
			defer routerStopProbe(session)
			if err = session.Ready(ctx); err != nil {
				t.Fatal("private core not ready")
			}
			s := session.(*routerSingBoxSession)
			d, err := proxy.SOCKS5("tcp", routercore.SocksAddress(s.ports.Mixed), &proxy.Auth{User: "nailink", Password: s.ports.Secret}, proxy.Direct)
			if err != nil {
				t.Fatal("private SOCKS setup failed")
			}
			dial := d.(proxy.ContextDialer)
			result := routerProbeHTTPTimeout(ctx, dial, conf.Global, 15*time.Second)
			if !result.Alive {
				t.Fatalf("private HTTP probe failed: %s", result.Message)
			}
			transport := &http.Transport{Proxy: nil, DialContext: dial.DialContext}
			defer transport.CloseIdleConnections()
			request, _ := http.NewRequestWithContext(ctx, "GET", "https://speed.cloudflare.com/__down?bytes=2097152", nil)
			start := time.Now()
			response, e := (&http.Client{Transport: transport, Timeout: 20 * time.Second}).Do(request)
			if e != nil {
				t.Fatal("private download request failed")
			}
			count, e := io.Copy(io.Discard, io.LimitReader(response.Body, 2097153))
			response.Body.Close()
			if e != nil || response.StatusCode != 200 || count != 2097152 {
				t.Fatal("private download incomplete")
			}
			steady := int32(-1)
			if result.SteadyMs != nil {
				steady = *result.SteadyMs
			}
			t.Logf("cold_ms=%d reused_ms=%d sample_bytes=%d sample_mbps=%.2f", result.Latency, steady, count, float64(count)*8/time.Since(start).Seconds()/1e6)
		})
	}
}
