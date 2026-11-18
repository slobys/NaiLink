package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSession = "1234567890abcdef1234567890abcdef"

func fixtureAccess(active bool, session string) authResult {
	if active && session == testSession {
		return authResult{}
	}
	return authResult{401, "session_expired"}
}

func TestReadCredentialsRejectsExposedCredentials(t *testing.T) {
	directory := t.TempDir()
	name := filepath.Join(directory, credentialName)
	if err := os.WriteFile(name, []byte(`{"username":"luci","secret":"local-fixture"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentials(directory); err == nil {
		t.Fatal("accepted exposed credentials")
	}
}

func TestGatewaySessionProxyAndRevocation(t *testing.T) {
	directory := t.TempDir()
	data, _ := json.Marshal(credentials{"router", "test-private-secret"})
	os.WriteFile(filepath.Join(directory, credentialName), data, 0600)
	calls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/graphql" || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
			t.Error("incorrect proxy path or forwarded browser identity")
		}
		token, err := jwt.Parse(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), func(*jwt.Token) (any, error) { return []byte("test-private-secret"), nil }, jwt.WithValidMethods([]string{"HS256"}))
		if err != nil || !token.Valid {
			t.Error("backend did not receive valid local authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"healthCheck":1}}`))
	}))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	active := true
	handler := gateway(directory, target, func(_ context.Context, session string) authResult { return fixtureAccess(active, session) })
	request := func(method, path, body, origin, session string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://router.test"+gatewayPath+path, strings.NewReader(body))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if session != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		}
		r.Header.Set("Authorization", "Bearer attacker")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"", "assets/index.js", "graphql?session=" + testSession} {
		if w := request("GET", path, "", "", ""); w.Code != 401 {
			t.Fatalf("anonymous path accepted: %s: %d", path, w.Code)
		}
	}
	if w := request("POST", "session", `{"session":"`+testSession+`"}`, "https://evil.test", ""); w.Code != 403 {
		t.Fatal("cross-origin bootstrap accepted")
	}
	w := request("POST", "session", `{"session":"`+testSession+`"}`, "https://router.test", "")
	cookies := w.Result().Cookies()
	if w.Code != 204 || len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != gatewayPath {
		t.Fatal("incorrect session cookie", w.Code)
	}
	if w := request("POST", "graphql", `{}`, "https://evil.test", testSession); w.Code != 403 {
		t.Fatal("CSRF accepted")
	}
	w = request("POST", "graphql", `{}`, "https://router.test", testSession)
	if w.Code != 200 || calls != 1 || strings.Contains(w.Body.String(), "secret") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("proxy failed", w.Code, w.Body.String())
	}
	active = false
	if w := request("POST", "graphql", `{}`, "https://router.test", testSession); w.Code != 401 || calls != 1 {
		t.Fatal("revoked session accepted")
	}
}

func TestSessionAccessRejectsAnonymousAndUsesLuCIWritePermission(t *testing.T) {
	directory := t.TempDir()
	script := "#!/bin/sh\ncase \"$4\" in *'\"scope\":\"access-group\"'*'\"ubus_rpc_session\":\"" + testSession + "\"'*) echo '{\"access\":true}';; *) echo '{\"access\":false}';; esac\n"
	os.WriteFile(filepath.Join(directory, "ubus"), []byte(script), 0755)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, value := range []string{"", "bad", strings.Repeat("0", 32), testSession + ";echo unsafe"} {
		if sessionAccess(context.Background(), value).Status != 401 {
			t.Fatal("invalid session accepted")
		}
	}
	if sessionAccess(context.Background(), testSession).Status != 0 {
		t.Fatal("valid access check rejected")
	}
}

func TestSessionFailuresAreClassifiedWithoutLeakingCredentials(t *testing.T) {
	for _, item := range []struct {
		name, script, code string
		status             int
	}{
		{"denied", `if [ "$3" = access ]; then echo '{"access":false}'; else echo '{"values":{}}'; fi`, "permission_denied", 403},
		{"expired", `if [ "$1" = list ]; then echo session; elif [ "$3" = access ]; then echo '{"access":false}'; else exit 4; fi`, "session_expired", 401},
		{"expired_access", `if [ "$1" = list ]; then echo session; else exit 4; fi`, "session_expired", 401},
		{"expired_negative", `if [ "$1" = list ]; then echo session; else exit 252; fi`, "session_expired", 401},
		{"missing_rpcd", `exit 4`, "rpc_unavailable", 503},
		{"unavailable", `echo 'sensitive stderr' >&2; exit 1`, "rpc_unavailable", 503},
		{"malformed", `echo '{}'`, "rpc_unavailable", 503},
		{"get_failed", `if [ "$3" = access ]; then echo '{"access":false}'; else exit 1; fi`, "rpc_unavailable", 503},
		{"get_malformed", `if [ "$3" = access ]; then echo '{"access":false}'; else echo '{}'; fi`, "rpc_unavailable", 503},
		{"timeout", `exec sleep 10`, "rpc_timeout", 504},
	} {
		t.Run(item.name, func(t *testing.T) {
			directory := t.TempDir()
			os.WriteFile(filepath.Join(directory, "ubus"), []byte("#!/bin/sh\n"+item.script+"\n"), 0755)
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			got := sessionAccess(ctx, testSession)
			if got.Status != item.status || got.Code != item.code {
				t.Fatalf("got %+v", got)
			}
			target, _ := url.Parse("http://127.0.0.1:1")
			handler := gateway(t.TempDir(), target, func(context.Context, string) authResult { return got })
			r := httptest.NewRequest("POST", "https://router.test"+gatewayPath+"session", strings.NewReader(`{"session":"`+testSession+`"}`))
			r.Header.Set("Origin", "https://router.test")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != item.status || !strings.Contains(w.Body.String(), item.code) || strings.Contains(w.Body.String(), testSession) || strings.Contains(w.Body.String(), "sensitive") || len(w.Result().Cookies()) != 0 {
				t.Fatalf("unsafe authorization failure: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestOriginCanonicalizationKeepsCrossSiteProtection(t *testing.T) {
	for _, item := range []struct {
		target, origin string
		allow          bool
	}{
		{"https://ROUTER.test:443", "https://router.test", true},
		{"http://router.test:80", "http://router.test", true},
		{"https://[::1]:443", "https://[::1]", true},
		{"https://router.test:8443", "https://router.test:8443", true},
		{"https://router.test:8443", "https://router.test", false},
		{"http://router.test", "https://router.test", false},
		{"https://router.test", "https://evil.test", false},
		{"https://router.test", "https://router.test.evil.test", false},
		{"https://router.test", "https://router.test/path", false},
		{"https://router.test", "https://user@router.test", false},
		{"https://router.test", "https://router.test?", false},
		{"https://router.test", "null", false},
		{"https://router.test", "", false},
	} {
		r := httptest.NewRequest("POST", item.target+gatewayPath+"session", nil)
		r.Header.Set("Origin", item.origin)
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "evil.test")
		if got := sameOrigin(r); got != item.allow {
			t.Fatalf("%s / %s: %v", item.target, item.origin, got)
		}
	}
}

func TestGatewayCachesOnlyAuthorizedFingerprintedStaticAssets(t *testing.T) {
	directory := t.TempDir()
	data, _ := json.Marshal(credentials{"router", "test-private-secret"})
	os.WriteFile(filepath.Join(directory, credentialName), data, 0600)
	type fixture struct {
		name, contentType string
		status            int
		cached            bool
	}
	for _, item := range []fixture{
		{"assets/index-AbCd1234.js", "text/javascript; charset=utf-8", 200, true},
		{"assets/index-AbCd1234.css", "text/css", 200, true},
		{"assets/editor.worker-AbCd1234.js", "application/javascript", 200, true},
		{"assets/font-AbCd1234.woff2", "font/woff2", 200, true},
		{"assets/index-AbCd1234.js", "text/html", 200, false},
		{"assets/index-AbCd1234.js", "text/javascript", 404, false},
		{"assets/index.js", "text/javascript", 200, false},
		{"assets/config-AbCd1234.json", "application/json", 200, false},
		{"index.html", "text/html", 200, false},
		{"graphql", "application/json", 200, false},
	} {
		t.Run(item.name+item.contentType+http.StatusText(item.status), func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", item.contentType)
				w.WriteHeader(item.status)
				w.Write([]byte("fixture"))
			}))
			defer backend.Close()
			target, _ := url.Parse(backend.URL)
			active := true
			handler := gateway(directory, target, func(_ context.Context, session string) authResult { return fixtureAccess(active, session) })
			request := func() *httptest.ResponseRecorder {
				method := "GET"
				if item.name == "graphql" {
					method = "POST"
				}
				r := httptest.NewRequest(method, "https://router.test"+gatewayPath+item.name, nil)
				r.Header.Set("Origin", "https://router.test")
				r.AddCookie(&http.Cookie{Name: sessionCookie, Value: testSession})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			response := request()
			expected := "no-store"
			if item.cached {
				expected = "private, max-age=31536000, immutable"
			}
			if response.Header().Get("Cache-Control") != expected {
				t.Fatalf("wrong cache policy: %s", response.Header().Get("Cache-Control"))
			}
			if len(response.Header().Values("Cache-Control")) != 1 {
				t.Fatal("ambiguous duplicate cache policy")
			}
			active = false
			response = request()
			if response.Code != 401 || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("revoked authorization was cached or ignored")
			}
		})
	}
}
