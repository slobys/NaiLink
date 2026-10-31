// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func overviewManagerFixture(t *testing.T, engine string, s *routerSingBoxSession) {
	t.Helper()
	routerCoreGlobal.Lock()
	previous := routerCoreGlobal.manager
	m := &routerCoreManager{active: s}
	m.state.Current = engine
	routerCoreGlobal.manager = m
	routerCoreGlobal.Unlock()
	t.Cleanup(func() {
		routerCoreGlobal.Lock()
		routerCoreGlobal.manager = previous
		routerCoreGlobal.Unlock()
	})
}

func TestRouterConnectionTotalsWireShapes(t *testing.T) {
	for _, text := range []string{
		`{"uploadTotal":0,"downloadTotal":0,"connections":null,"memory":{"future":[1,null,{"flag":true}]}}`,
		`{"uploadTotal":12,"downloadTotal":34,"connections":[{"metadata":{"network":"tcp"}},{"metadata":{"network":"udp"}}]}`,
	} {
		value, err := routerReadConnectionTotals(strings.NewReader(text))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(text, `"tcp"`) && (value.Connections != 2 || value.UDP != 1 || value.Upload != 12 || value.Download != 34) {
			t.Fatalf("bad counters: %+v", value)
		}
	}
	for _, text := range []string{
		`null`, `{}`, `{"uploadTotal":0,"downloadTotal":0}`, // Missing fields are not a fresh zero sample.
		`{"uploadTotal":-1,"downloadTotal":0,"connections":[]}`,
		`{"uploadTotal":0,"downloadTotal":"invalid","connections":[]}`,
		`{"uploadTotal":0,"downloadTotal":0,"connections":{}}`,
		`{"uploadTotal":0,"downloadTotal":0,"connections":[42]}`,
		`{"uploadTotal":0,"downloadTotal":0,"connections":[`,
		`{"uploadTotal":0,"uploadTotal":1,"downloadTotal":0,"connections":[]}`,
		`{"uploadTotal":0,"downloadTotal":0,"connections":[]} {}`,
		`{"uploadTotal":0,"downloadTotal":0,"connections":[],"future":` + strings.Repeat("[", 70) + "0" + strings.Repeat("]", 70) + "}",
	} {
		if _, err := routerReadConnectionTotals(strings.NewReader(text)); err == nil {
			t.Fatal("invalid connection snapshot accepted")
		}
	}
}

func TestRouterOverviewSharesFailuresAndRecoversWithoutErasingSamples(t *testing.T) {
	var calls atomic.Int32
	var failed atomic.Bool
	s, _ := coreAPIFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if failed.Load() {
			_, _ = w.Write([]byte("{"))
		} else {
			_, _ = w.Write([]byte(`{"uploadTotal":12,"downloadTotal":34,"connections":[]}`))
		}
	})
	overviewManagerFixture(t, "sing-box", s)
	first, _, err := routerCoreOverview(60, 30)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = routerCoreOverview(60, 30)
	if err != nil || calls.Load() != 1 {
		t.Fatal("tabs did not share their successful sample")
	}
	failed.Store(true)
	s.statsNextAt = time.Time{}
	for range 3 {
		if _, _, err = routerCoreOverview(60, 30); err == nil || err.Error() != "core_api_invalid" {
			t.Fatal("invalid statistics were hidden", err)
		}
	}
	if calls.Load() != 2 || !s.stats.UpdatedAt.Equal(first.UpdatedAt) || len(s.samples) != 1 {
		t.Fatal("failed polling retried immediately or erased the last valid sample")
	}
	failed.Store(false)
	s.statsNextAt = time.Time{}
	recovered, _, err := routerCoreOverview(60, 30)
	if err != nil || calls.Load() != 3 || !recovered.UpdatedAt.After(first.UpdatedAt) || len(s.samples) != 2 {
		t.Fatal("statistics did not recover", err)
	}
}

func TestRouterOverviewKeepsAnUpperBoundForLargeTables(t *testing.T) {
	s, _ := coreAPIFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		// Even a syntactically valid root must stay bounded, including unknown fields.
		_, _ = w.Write([]byte(`{"future":"` + strings.Repeat("x", 16<<20) + `","uploadTotal":0,"downloadTotal":0,"connections":[]}`))
	})
	overviewManagerFixture(t, "mihomo", s)
	if _, _, err := routerCoreOverview(60, 30); err == nil || err.Error() != "core_api_response_too_large" {
		t.Fatal("unbounded statistics response accepted", err)
	}
}

func TestRouterOverviewAcceptsLargeValidConnectionSnapshot(t *testing.T) {
	// The pinned Clash-compatible API returns every connection's metadata,
	// including details that the overview never needs. Exercise that wire shape.
	connection := `{"id":"fixture","metadata":{"network":"udp","host":"` + strings.Repeat("x", 320) + `"},"chains":["proxy"],"rule":"final"}`
	payload := `{"uploadTotal":123456,"downloadTotal":987654,"memory":4096,"connections":[` + strings.Repeat(connection+",", 5999) + connection + "]}"
	if len(payload) <= 2<<20 {
		t.Fatal("snapshot must cross the previous control-response limit")
	}
	for _, engine := range []string{"sing-box", "mihomo"} {
		t.Run(engine, func(t *testing.T) {
			s, _ := coreAPIFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/connections" && r.URL.Path != "/version" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				_, _ = w.Write([]byte(payload))
			})
			overviewManagerFixture(t, engine, s)
			value, handled, err := routerCoreOverview(60, 30)
			if err != nil || !handled {
				t.Fatalf("valid %s snapshot rejected: %v", engine, err)
			}
			if value.ActiveConnections != 6000 || value.UDPSessions != 6000 || value.UploadTotal != 123456 || value.DownloadTotal != 987654 {
				t.Fatalf("wrong overview counters: %+v", value)
			}
			if err := s.api(context.Background(), "/version", nil); err == nil {
				t.Fatal("ordinary endpoints must retain their smaller response limit")
			}
		})
	}
}
