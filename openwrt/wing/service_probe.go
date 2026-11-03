// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"golang.org/x/net/proxy"
	"io"
	"net/http"
	"strings"
	"time"
)

type serviceProbeKey struct{}

var serviceHTTPWork = make(chan struct{}, 2)

// Only fixed, credential-free service entrypoints are allowed, never a caller URL.
func WithRouterServiceProbe(ctx context.Context, profile string) context.Context {
	return context.WithValue(ctx, serviceProbeKey{}, profile)
}
func RouterServiceProfile(ctx context.Context) string {
	value, _ := ctx.Value(serviceProbeKey{}).(string)
	return value
}
func routerServiceTarget(profile string) string {
	switch profile {
	case "proxy":
		return "https://cp.cloudflare.com/generate_204"
	case "ai":
		return "https://chatgpt.com/api/auth/csrf"
	case "netflix":
		return "https://www.netflix.com/"
	}
	return ""
}
func routerServiceResponse(profile string, code int, header http.Header, body []byte) string {
	text := strings.ToLower(string(body))
	if strings.Contains(text, "unsupported_country") || strings.Contains(text, "not available in your country") || code == 451 {
		return "service_restricted"
	}
	if header.Get("Cf-Mitigated") == "challenge" || strings.Contains(text, "cf-chl-") || strings.Contains(text, "just a moment") || strings.Contains(text, "captcha") {
		return "service_challenge"
	}
	if code == 429 {
		return "service_rate_limited"
	}
	if code == 403 {
		return "service_restricted"
	}
	if code >= 500 {
		return "service_remote_error"
	}
	if profile == "proxy" {
		if code == 204 {
			return "service_accessible"
		}
		return "service_unconfirmed"
	}
	if code != 200 {
		return "service_unconfirmed"
	}
	if profile == "ai" {
		var value struct {
			CSRF string `json:"csrfToken"`
		}
		if json.Unmarshal(body, &value) != nil || value.CSRF == "" {
			return "service_unconfirmed"
		}
	} else if profile == "netflix" && !strings.Contains(text, "netflix") {
		return "service_unconfirmed"
	}
	return "service_accessible"
}
func routerServiceHTTP(ctx context.Context, d proxy.ContextDialer, profile string) RouterCoreProbe {
	result := RouterCoreProbe{Tested: time.Now(), Message: "service_unconfirmed"}
	target := routerServiceTarget(profile)
	if target == "" {
		return result
	}
	select {
	case serviceHTTPWork <- struct{}{}:
		defer func() { <-serviceHTTPWork }()
	case <-ctx.Done():
		result.Message = "cancelled"
		return result
	}
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: d.DialContext, DisableKeepAlives: true, MaxConnsPerHost: 1, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 6 * time.Second, MaxResponseHeaderBytes: 32 << 10}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// Netflix's country redirect is allowed only on its public service hostname.
		if len(via) > 2 || req.URL.Scheme != "https" || req.URL.Host != "www.netflix.com" || profile != "netflix" {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target, nil)
	if err != nil {
		return result
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 NaiLink-ServiceCheck/1.0")
	req.Header.Set("Accept", "text/html,application/json")
	started := time.Now()
	response, err := client.Do(req)
	if err != nil {
		result.Message = RouterProbeFailure(err)
		result.Tested = time.Now()
		return result
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<10))
	if err != nil {
		result.Message = RouterProbeFailure(err)
		return result
	}
	result.Message = routerServiceResponse(profile, response.StatusCode, response.Header, data)
	result.Alive = result.Message == "service_accessible"
	result.Latency = int32(time.Since(started).Milliseconds())
	result.Tested = time.Now()
	return result
}
