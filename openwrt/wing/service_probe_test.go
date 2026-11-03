// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterServiceClassification(t *testing.T) {
	for _, x := range []struct {
		profile    string
		status     int
		body, want string
	}{
		{"proxy", 204, "", "service_accessible"}, {"proxy", 200, "portal", "service_unconfirmed"},
		{"ai", 200, `{"csrfToken":"ephemeral"}`, "service_accessible"}, {"ai", 200, `{"error":"unsupported_country"}`, "service_restricted"},
		{"ai", 200, "<html>Login</html>", "service_unconfirmed"}, {"ai", 403, "cf-chl-challenge", "service_challenge"},
		{"netflix", 200, "<html>Netflix</html>", "service_accessible"}, {"netflix", 200, "portal", "service_unconfirmed"},
		{"netflix", 451, "", "service_restricted"}, {"netflix", 429, "", "service_rate_limited"}, {"netflix", 503, "", "service_remote_error"},
	} {
		if got := routerServiceResponse(x.profile, x.status, http.Header{}, []byte(x.body)); got != x.want {
			t.Fatalf("%s %d got %s", x.profile, x.status, got)
		}
	}
	if routerServiceTarget("caller://secret") != "" {
		t.Fatal("accepted caller URL")
	}
}

type blockedServiceDialer struct{ calls atomic.Int32 }

func (d *blockedServiceDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	d.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestRouterServiceCancellationReleasesSlots(t *testing.T) {
	d := &blockedServiceDialer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan RouterCoreProbe, 3)
	for range 3 {
		go func() { done <- routerServiceHTTP(ctx, d, "ai") }()
	}
	end := time.Now().Add(time.Second)
	for d.calls.Load() < 2 && time.Now().Before(end) {
		time.Sleep(time.Millisecond)
	}
	if d.calls.Load() != 2 {
		cancel()
		t.Fatal("service concurrency not bounded", d.calls.Load())
	}
	cancel()
	for range 3 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("cancelled check leaked")
		}
	}
	if len(serviceHTTPWork) != 0 {
		t.Fatal("service slots retained")
	}
}
