// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func coreAPIFixture(t *testing.T, handler http.HandlerFunc) (*routerSingBoxSession, *atomic.Int32) {
	t.Helper()
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(handler)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	number, _ := strconv.Atoi(port)
	s := &routerSingBoxSession{ports: sbPorts{API: number, Secret: "private-fixture"}}
	t.Cleanup(func() { s.closeControlAPI(); server.Close() })
	return s, &connections
}

func TestRouterCoreAPIReusesConnectionsAndReleasesOnStop(t *testing.T) {
	s, connections := coreAPIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer private-fixture" {
			t.Error("wrong local API authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"fixture"}`))
	})
	for i := 0; i < 4; i++ {
		var value any
		if i%2 == 0 {
			value = &map[string]string{}
		}
		if err := s.api(context.Background(), "/version", value); err != nil {
			t.Fatal(err)
		}
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("polling opened %d connections; want one reused connection", got)
	}
	exit := make(chan struct{})
	close(exit)
	s.exit, s.dir = exit, t.TempDir()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.api(context.Background(), "/version", nil); err == nil || err.Error() != "core_not_ready" {
		t.Fatal("stopped API reopened", err)
	}
	if connections.Load() != 1 {
		t.Fatal("request reopened a retired session")
	}
}

func TestRouterCoreAPIShutdownCancelsActiveAndQueuedRequests(t *testing.T) {
	started := make(chan struct{}, 2)
	s, connections := coreAPIFixture(t, func(_ http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	})
	done := make(chan error, 2)
	for range 2 {
		go func() { done <- s.api(context.Background(), "/version", nil) }()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("API did not start")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.api(ctx, "/version", nil); err == nil {
		t.Fatal("queued request ignored deadline")
	}
	if got := connections.Load(); got != 2 {
		t.Fatalf("connection limit exceeded: %d", got)
	}
	s.closeControlAPI()
	s.closeControlAPI()
	for range 2 {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("stopped request succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("active API request survived shutdown")
		}
	}
}

func TestRouterCoreAPIDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	defer other.Close()
	s, _ := coreAPIFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) })
	if err := s.api(context.Background(), "/version", nil); err == nil {
		t.Fatal("accepted redirect")
	}
	if followed.Load() {
		t.Fatal("local control request escaped to redirect target")
	}
}

func TestRouterCoreAPIShutdownDuringJSONIsNotReportedAsCorruptData(t *testing.T) {
	started := make(chan struct{})
	s, _ := coreAPIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"`))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	done := make(chan error, 1)
	go func() { done <- s.api(context.Background(), "/version", &map[string]string{}) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("body did not start")
	}
	s.closeControlAPI()
	select {
	case err := <-done:
		if err == nil || err.Error() != "core_not_ready" {
			t.Fatal("shutdown reported malformed JSON", err)
		}
	case <-time.After(time.Second):
		t.Fatal("body read survived shutdown")
	}
}

func TestRouterCoreAPIBoundsIgnoredResponseBodies(t *testing.T) {
	s, _ := coreAPIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/invalid" {
			_, _ = w.Write([]byte("{"))
			return
		}
		_, _ = w.Write([]byte(strings.Repeat(" ", (2<<20)+2)))
	})
	err := s.api(context.Background(), "/version", nil)
	if err == nil || err.Error() != "core_api_response_too_large" {
		t.Fatal("accepted excessive response", err)
	}
	if err = s.api(context.Background(), "/invalid", &map[string]string{}); err == nil || err.Error() != "core_api_invalid" {
		t.Fatal("real malformed JSON was hidden as a switching state", err)
	}
}
