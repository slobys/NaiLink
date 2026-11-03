// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
	"weak"

	daeConfig "github.com/daeuniverse/dae/config"
)

// Fixed public, unauthenticated targets only. The API cannot supply a URL,
// headers, credentials or redirects to a router/private-network service.
var routerConnectivityTargets = []struct{ id, url string }{
	{"baidu", "https://www.baidu.com/"},
	{"google", "https://www.google.com/generate_204"},
	{"openai", "https://chatgpt.com/robots.txt"},
	{"github", "https://github.com/robots.txt"},
}

type routerConnectivityKey struct {
	manager *routerCoreManager
	// Keep identity without retaining an obsolete configuration or native core
	// and its large rule caches after the dashboard is closed.
	config  weak.Pointer[daeConfig.Config]
	session <-chan error
	core    string
}
type routerConnectivitySample struct {
	At         int64  `json:"at"`
	LatencyMs  *int64 `json:"latencyMs,omitempty"`
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
}
type routerConnectivityTarget struct {
	ID      string                     `json:"id"`
	Samples []routerConnectivitySample `json:"samples"`
}
type routerConnectivityStatus struct {
	Core      string                     `json:"core"`
	State     string                     `json:"state"`
	Checking  bool                       `json:"checking"`
	CheckedAt int64                      `json:"checkedAt"`
	Targets   []routerConnectivityTarget `json:"targets"`
}
type routerConnectivityMonitor struct {
	mu                          sync.Mutex
	key                         routerConnectivityKey
	checked                     time.Time
	pending                     chan struct{}
	history                     map[string][]routerConnectivitySample
	capture                     func() (routerConnectivityKey, string)
	probe                       func(context.Context, string) routerConnectivitySample
	now                         func() time.Time
	interval, cooldown, timeout time.Duration
}

func routerConnectivitySnapshot() (routerConnectivityKey, string) {
	m := currentRouterCoreManager()
	if m == nil {
		return routerConnectivityKey{}, "unavailable"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var done <-chan error
	if m.active != nil {
		done = m.active.Done()
	}
	key := routerConnectivityKey{m, weak.Make(m.conf), done, m.state.Current}
	if m.conf == nil || m.conf == EmptyConfig {
		return key, "stopped"
	}
	if m.phase != "ready" {
		return key, "changing"
	}
	return key, "ready"
}

func newRouterConnectivityMonitor() *routerConnectivityMonitor {
	return &routerConnectivityMonitor{capture: routerConnectivitySnapshot, probe: routerConnectivityProbe,
		now: time.Now, interval: 30 * time.Second, cooldown: 5 * time.Second, timeout: 8 * time.Second}
}

var routerConnectivity = newRouterConnectivityMonitor()

func RouterConnectivityStatus(ctx context.Context, refresh, force bool) string {
	data, _ := json.Marshal(routerConnectivity.get(ctx, refresh, force))
	return string(data)
}

func (m *routerConnectivityMonitor) align(key routerConnectivityKey) {
	if m.history == nil || key != m.key {
		m.key, m.checked = key, time.Time{}
		m.history = map[string][]routerConnectivitySample{}
	}
}
func (m *routerConnectivityMonitor) snapshot(key routerConnectivityKey, state string) routerConnectivityStatus {
	m.align(key)
	result := routerConnectivityStatus{Core: key.core, State: state, Checking: m.pending != nil, Targets: []routerConnectivityTarget{}}
	if !m.checked.IsZero() {
		result.CheckedAt = m.checked.UnixMilli()
	}
	for _, target := range routerConnectivityTargets {
		samples := append([]routerConnectivitySample{}, m.history[target.id]...)
		result.Targets = append(result.Targets, routerConnectivityTarget{target.id, samples})
	}
	return result
}
func (m *routerConnectivityMonitor) get(ctx context.Context, refresh, force bool) routerConnectivityStatus {
	key, state := m.capture()
	m.mu.Lock()
	m.align(key)
	interval := m.interval
	if force {
		interval = m.cooldown
	}
	if state == "ready" && refresh && m.pending == nil && (m.checked.IsZero() || m.now().Sub(m.checked) >= interval) {
		m.pending = make(chan struct{})
		go m.batch(key, m.pending)
	}
	pending := m.pending
	if !refresh || state != "ready" {
		pending = nil
	}
	m.mu.Unlock()
	if pending != nil {
		select {
		case <-pending:
		case <-ctx.Done():
		}
	}
	key, state = m.capture()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshot(key, state)
}
func (m *routerConnectivityMonitor) batch(key routerConnectivityKey, done chan struct{}) {
	// Share one short batch across all browsers. Closing a page cannot start
	// another batch or cancel someone else's result. No background ticker exists.
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	values := make([]routerConnectivitySample, len(routerConnectivityTargets))
	var wg sync.WaitGroup
	for i, target := range routerConnectivityTargets {
		wg.Add(1)
		go func(i int, address string) { defer wg.Done(); values[i] = m.probe(ctx, address) }(i, target.url)
	}
	wg.Wait()
	current, state := m.capture()
	m.mu.Lock()
	defer m.mu.Unlock()
	// Never publish measurements obtained across a reload or core switch.
	if current == key && m.key == key && state == "ready" {
		for i, target := range routerConnectivityTargets {
			samples := append(m.history[target.id], values[i])
			if len(samples) > 18 {
				samples = samples[len(samples)-18:]
			}
			m.history[target.id] = samples
		}
		m.checked = m.now()
	}
	if m.pending == done {
		m.pending = nil
	}
	close(done)
}

func routerConnectivityProbe(ctx context.Context, address string) routerConnectivitySample {
	// This existing client uses the applied dae routing or the active native
	// core's authenticated SOCKS ingress. Never spawn a test core or read drafts.
	transport := HttpTransport.Clone()
	transport.Proxy = nil
	transport.TLSHandshakeTimeout = 4 * time.Second
	transport.ResponseHeaderTimeout = 5 * time.Second
	transport.MaxIdleConns = 1
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	return routerCheckConnectivityHTTP(ctx, client, address)
}
func routerCheckConnectivityHTTP(ctx context.Context, client *http.Client, address string) routerConnectivitySample {
	// A public target may redirect. Report that response instead of following
	// an arbitrary URL, and do not change a shared client's redirect policy.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	result := routerConnectivitySample{At: time.Now().UnixMilli(), Status: "error"}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, address, nil)
	if err != nil {
		result.Code = "connection_failed"
		return result
	}
	request.Header.Set("User-Agent", "NaiLink-Connectivity/1.0")
	start := time.Now()
	response, err := copyClient.Do(request)
	if err != nil {
		var dnsErr *net.DNSError
		var certErr *tls.CertificateVerificationError
		var unknownAuthority x509.UnknownAuthorityError
		var timeout net.Error
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			result.Code = "timeout"
		case errors.As(err, &dnsErr):
			result.Code = "dns_failed"
		case errors.As(err, &certErr), errors.As(err, &unknownAuthority):
			result.Code = "tls_failed"
		case errors.As(err, &timeout) && timeout.Timeout():
			result.Code = "timeout"
		default:
			result.Code = "connection_failed"
		}
		return result
	}
	defer response.Body.Close()
	elapsed := time.Since(start).Milliseconds()
	result.LatencyMs, result.HTTPStatus = &elapsed, response.StatusCode
	result.Status = "ok"
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		result.Status = "restricted"
	}
	return result
}
