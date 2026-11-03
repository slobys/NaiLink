// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	daeConfig "github.com/daeuniverse/dae/config"
)

func TestRouterConnectivityHTTP(t *testing.T) {
	var private atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("must not download target bodies: %s", r.Method)
		}
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(204)
		case "/blocked":
			w.WriteHeader(403)
		case "/failure":
			w.WriteHeader(503)
		case "/redirect":
			http.Redirect(w, r, "/private", 302)
		case "/private":
			private.Add(1)
		case "/slow":
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		path, status string
		code         int
	}{{"ok", "ok", 204}, {"blocked", "restricted", 403}, {"failure", "restricted", 503}, {"redirect", "ok", 302}} {
		result := routerCheckConnectivityHTTP(context.Background(), server.Client(), server.URL+"/"+tc.path)
		if result.Status != tc.status || result.HTTPStatus != tc.code || result.LatencyMs == nil {
			t.Fatalf("%s: %+v", tc.path, result)
		}
	}
	if private.Load() != 0 {
		t.Fatal("followed an untrusted redirect")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if result := routerCheckConnectivityHTTP(ctx, server.Client(), server.URL+"/slow"); result.Code != "timeout" || result.LatencyMs != nil {
		t.Fatalf("timeout: %+v", result)
	}
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tlsServer.Close()
	if result := routerCheckConnectivityHTTP(context.Background(), http.DefaultClient, tlsServer.URL); result.Code != "tls_failed" {
		t.Fatalf("certificate: %+v", result)
	}
}

type connectivityFixture struct {
	mu      sync.Mutex
	key     routerConnectivityKey
	state   string
	calls   atomic.Int32
	clock   atomic.Int64
	monitor *routerConnectivityMonitor
	config  *daeConfig.Config
}

func newConnectivityFixture() *connectivityFixture {
	f := &connectivityFixture{config: &daeConfig.Config{}, state: "ready"}
	f.key = routerConnectivityKey{config: weak.Make(f.config), core: "dae"}
	f.clock.Store(time.Now().UnixMilli())
	f.monitor = newRouterConnectivityMonitor()
	f.monitor.capture = func() (routerConnectivityKey, string) { f.mu.Lock(); defer f.mu.Unlock(); return f.key, f.state }
	f.monitor.now = func() time.Time { return time.UnixMilli(f.clock.Load()) }
	f.monitor.probe = func(context.Context, string) routerConnectivitySample {
		f.calls.Add(1)
		ms := int64(31)
		return routerConnectivitySample{At: f.clock.Load(), LatencyMs: &ms, Status: "ok", HTTPStatus: 204}
	}
	return f
}
func (f *connectivityFixture) setState(state string, change bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = state
	if change {
		f.config = &daeConfig.Config{}
		f.key.config = weak.Make(f.config)
	}
}

func TestRouterConnectivityCacheAndBoundedHistory(t *testing.T) {
	f := newConnectivityFixture()
	for i := 0; i < 10; i++ {
		f.monitor.get(context.Background(), false, false)
	}
	if f.calls.Load() != 0 {
		t.Fatal("status reads started network probes")
	}
	f.monitor.get(context.Background(), true, false)
	f.monitor.get(context.Background(), true, true)
	if f.calls.Load() != 4 {
		t.Fatal("manual refresh ignored minimum cooldown")
	}
	f.clock.Add(6000)
	f.monitor.get(context.Background(), true, false)
	if f.calls.Load() != 4 {
		t.Fatal("auto refresh ignored shared 30 second cache")
	}
	f.monitor.get(context.Background(), true, true)
	if f.calls.Load() != 8 {
		t.Fatal("manual refresh did not run after cooldown")
	}
	for i := 0; i < 30; i++ {
		f.clock.Add(31000)
		f.monitor.get(context.Background(), true, false)
	}
	result := f.monitor.get(context.Background(), false, false)
	if len(result.Targets) != 4 || len(result.Targets[0].Samples) != 18 {
		t.Fatalf("history is not bounded: %+v", result)
	}
	// Callers cannot overwrite the monitor's cached slice.
	result.Targets[0].Samples[0].Status = "error"
	if f.monitor.get(context.Background(), false, false).Targets[0].Samples[0].Status != "ok" {
		t.Fatal("snapshot aliases mutable history")
	}
	for _, state := range []string{"stopped", "changing", "unavailable"} {
		before := f.calls.Load()
		f.setState(state, false)
		f.clock.Add(31000)
		f.monitor.get(context.Background(), true, true)
		if f.calls.Load() != before {
			t.Fatal("probed while core was not ready")
		}
	}
}

func TestRouterConnectivitySharedBatchCancellationAndReload(t *testing.T) {
	f := newConnectivityFixture()
	gate := make(chan struct{})
	started := make(chan struct{}, 4)
	f.monitor.probe = func(ctx context.Context, _ string) routerConnectivitySample {
		f.calls.Add(1)
		started <- struct{}{}
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return routerConnectivitySample{At: f.clock.Load(), Status: "error", Code: "timeout"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan routerConnectivityStatus, 1)
	go func() { first <- f.monitor.get(ctx, true, false) }()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("batch did not start")
		}
	}
	cancel()
	if result := <-first; !result.Checking {
		t.Fatal("abandoned browser canceled shared batch")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f.monitor.get(context.Background(), true, true) }()
	}
	close(gate)
	wg.Wait()
	if f.calls.Load() != 4 {
		t.Fatalf("duplicated batch: %d", f.calls.Load())
	}
	f.setState("ready", true)
	result := f.monitor.get(context.Background(), false, false)
	if result.CheckedAt != 0 || len(result.Targets[0].Samples) != 0 {
		t.Fatal("old configuration history survived reload")
	}
	// A batch that crosses a configuration boundary must not publish old results.
	gate = make(chan struct{})
	f.clock.Add(31000)
	done := make(chan routerConnectivityStatus, 1)
	go func() { done <- f.monitor.get(context.Background(), true, false) }()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("second batch did not start")
		}
	}
	f.setState("ready", true)
	close(gate)
	if result := <-done; result.CheckedAt != 0 || len(result.Targets[0].Samples) != 0 {
		t.Fatal("published stale in-flight results after switch")
	}
}

func TestRouterConnectivityBatchDeadline(t *testing.T) {
	f := newConnectivityFixture()
	f.monitor.timeout = 20 * time.Millisecond
	f.monitor.probe = func(ctx context.Context, _ string) routerConnectivitySample {
		<-ctx.Done()
		return routerConnectivitySample{Status: "error", Code: "timeout"}
	}
	result := f.monitor.get(context.Background(), true, false)
	if result.Checking || result.CheckedAt == 0 || len(result.Targets[0].Samples) != 1 {
		t.Fatal("deadline did not finish batch")
	}
}
