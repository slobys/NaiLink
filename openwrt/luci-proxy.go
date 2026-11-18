// SPDX-License-Identifier: AGPL-3.0-only
// The CGI gateway delegates access to LuCI and keeps Daed credentials on-device.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const gatewayPath = "/cgi-bin/daed-router/"
const sessionCookie = "daed_luci_session"
const credentialName = "luci-session.json"

var validSession = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
var fingerprintedAsset = regexp.MustCompile(`^assets/[a-zA-Z0-9_][a-zA-Z0-9_.-]*-[a-zA-Z0-9_-]{8,}\.(js|css|woff2?|ttf|svg|png|webp|wasm)$`)

func cacheableAsset(name string, response *http.Response) bool {
	if response.StatusCode != http.StatusOK || !fingerprintedAsset.MatchString(name) {
		return false
	}
	contentType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	switch path.Ext(name) {
	case ".js":
		return contentType == "text/javascript" || contentType == "application/javascript"
	case ".css":
		return contentType == "text/css"
	case ".woff", ".woff2", ".ttf":
		return strings.HasPrefix(contentType, "font/") || contentType == "application/font-woff" || contentType == "application/x-font-ttf"
	case ".svg":
		return contentType == "image/svg+xml"
	case ".png", ".webp":
		return contentType == "image/"+strings.TrimPrefix(path.Ext(name), ".")
	case ".wasm":
		return contentType == "application/wasm"
	}
	return false
}

type credentials struct {
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

func readCredentials(configDir string) (credentials, error) {
	var value credentials
	name := filepath.Join(configDir, credentialName)
	info, err := os.Lstat(name)
	if err != nil {
		return value, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return value, errors.New("local credentials are not private")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(data, &value)
	if value.Username == "" || value.Secret == "" {
		return value, errors.New("local credentials are incomplete")
	}
	return value, err
}

type authResult struct {
	Status int
	Code   string
}

func authError(w http.ResponseWriter, result authResult) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(result.Status)
	json.NewEncoder(w).Encode(map[string]string{"code": result.Code})
}

func sessionAccess(ctx context.Context, session string) authResult {
	if !validSession.MatchString(session) || session == strings.Repeat("0", 32) {
		return authResult{401, "session_expired"}
	}
	input, _ := json.Marshal(map[string]string{"ubus_rpc_session": session, "scope": "access-group", "object": "luci-app-daed-router", "function": "write"})
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "ubus", "call", "session", "access", string(input)).Output()
	if sessionMissing(ctx, err) {
		return authResult{401, "session_expired"}
	}
	var response struct {
		Access *bool `json:"access"`
	}
	if err != nil || json.Unmarshal(data, &response) != nil || response.Access == nil {
		return sessionFailure(ctx)
	}
	if *response.Access {
		return authResult{}
	}
	// A denied ACL and an expired session both return access:false. Query only
	// the empty values table to distinguish them without reading user data.
	input, _ = json.Marshal(map[string]any{"ubus_rpc_session": session, "keys": []string{}})
	data, err = exec.CommandContext(ctx, "ubus", "call", "session", "get", string(input)).Output()
	if sessionMissing(ctx, err) {
		return authResult{401, "session_expired"}
	}
	var value struct {
		Values map[string]json.RawMessage `json:"values"`
	}
	if err != nil || json.Unmarshal(data, &value) != nil || value.Values == nil {
		return sessionFailure(ctx)
	}
	return authResult{403, "permission_denied"}
}

func sessionMissing(ctx context.Context, err error) bool {
	var exit *exec.ExitError
	// ubus CLI versions may return 4 or -4 (252 in the process status)
	// for NOT_FOUND. Never classify by stderr, which can contain the session.
	if !errors.As(err, &exit) || (exit.ExitCode() != 4 && exit.ExitCode() != 252) || ctx.Err() != nil {
		return false
	}
	// ubus uses NOT_FOUND for both a missing session and a missing rpcd
	// object. Only report an expired login if the service still exists.
	data, err := exec.CommandContext(ctx, "ubus", "list", "session").Output()
	return err == nil && strings.TrimSpace(string(data)) == "session"
}

func sessionFailure(ctx context.Context) authResult {
	if ctx.Err() != nil {
		return authResult{504, "rpc_timeout"}
	}
	return authResult{503, "rpc_unavailable"}
}

func backendURL() (*url.URL, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "uci", "-q", "get", "daed.main.listen_port").Output()
	port := "2023"
	if err == nil {
		port = strings.TrimSpace(string(data))
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return nil, errors.New("invalid local management port")
	}
	return url.Parse("http://127.0.0.1:" + strconv.Itoa(number))
}

func sameOrigin(r *http.Request) bool {
	scheme := r.URL.Scheme
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Scheme != scheme || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return false
	}
	// Browsers omit default ports and normalize DNS host casing. CGI servers
	// may keep an explicit :80/:443 in HTTP_HOST. Do not trust forwarded headers.
	canonical := func(host string) string {
		u, err := url.Parse(scheme + "://" + host)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" {
			return ""
		}
		port := u.Port()
		if port == "" {
			if scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		return strings.ToLower(u.Hostname()) + ":" + port
	}
	return canonical(origin.Host) != "" && canonical(origin.Host) == canonical(r.Host)
}

func gateway(configDir string, target *url.URL, access func(context.Context, string) authResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", 405)
			return
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			authError(w, authResult{403, "origin_rejected"})
			return
		}
		if r.URL.Path == strings.TrimSuffix(gatewayPath, "/") {
			http.Redirect(w, r, gatewayPath, 307)
			return
		}
		if !strings.HasPrefix(r.URL.Path, gatewayPath) {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, gatewayPath)
		if path == "session" {
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", 405)
				return
			}
			var input struct {
				Session string `json:"session"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil {
				authError(w, authResult{400, "invalid_request"})
				return
			}
			if result := access(r.Context(), input.Session); result.Status != 0 {
				authError(w, result)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: input.Session, Path: gatewayPath, HttpOnly: true, Secure: r.URL.Scheme == "https" || r.TLS != nil, SameSite: http.SameSiteStrictMode})
			w.WriteHeader(204)
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			authError(w, authResult{401, "session_expired"})
			return
		}
		if result := access(r.Context(), cookie.Value); result.Status != 0 {
			authError(w, result)
			return
		}
		if path == "graphql" && r.Method != http.MethodPost {
			http.Error(w, "GraphQL requires POST", 405)
			return
		}
		if path != "graphql" && r.Method == http.MethodPost {
			http.Error(w, "Method not allowed", 405)
			return
		}
		if r.ContentLength > 8<<20 {
			http.Error(w, "Request too large", 413)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		identity, err := readCredentials(configDir)
		if err != nil {
			http.Error(w, "NaiLink 尚未就绪，请重新启动服务。", 503)
			return
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": identity.Username, "role": "admin", "exp": time.Now().Add(time.Minute).Unix()}).SignedString([]byte(identity.Secret))
		if err != nil {
			http.Error(w, "Local authorization failed", 503)
			return
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Director = func(request *http.Request) {
			request.URL.Scheme, request.URL.Host = target.Scheme, target.Host
			request.URL.Path, request.URL.RawPath = "/"+path, ""
			request.Host = target.Host
			request.Header.Del("Cookie")
			request.Header.Del("Origin")
			request.Header.Del("X-Forwarded-Host")
			request.Header.Del("X-Forwarded-Proto")
			request.Header.Set("Authorization", "Bearer "+token)
		}
		proxy.ModifyResponse = func(response *http.Response) error {
			// ReverseProxy appends upstream headers. Remove the initial error
			// policy so a successful response has one unambiguous cache policy.
			w.Header().Del("Cache-Control")
			response.Header.Set("Cache-Control", "no-store")
			// Only public, content-fingerprinted build assets may be retained in
			// this browser's private cache. HTML, sessions, GraphQL and errors
			// remain uncached, and every request still passes LuCI authorization.
			if (r.Method == http.MethodGet || r.Method == http.MethodHead) && cacheableAsset(path, response) {
				response.Header.Set("Cache-Control", "private, max-age=31536000, immutable")
				response.Header.Add("Vary", "Cookie")
			}
			response.Header.Del("Set-Cookie")
			response.Header.Del("Access-Control-Allow-Origin")
			return nil
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "NaiLink 服务暂时不可用，请重试或查看运行日志。", 502)
		}
		proxy.Transport = &http.Transport{Proxy: nil, ResponseHeaderTimeout: 30 * time.Second, DisableKeepAlives: true}
		proxy.ServeHTTP(w, r)
	})
}

func main() {
	configDir := flag.String("config", "/etc/daed", "NaiLink configuration directory")
	initialize := flag.Bool("prepare", false, "Prepare local identity before starting NaiLink")
	maintenance := flag.String("maintenance", "", "Run an authorized local maintenance task")
	maintenanceJob := flag.String("maintenance-job", "", "One-shot maintenance task identifier")
	flag.Parse()
	if *maintenance != "" {
		maintenanceCLI(*maintenance, *configDir, *maintenanceJob)
		return
	}
	if *initialize {
		// Keep the existing CLI available for local maintenance scripts.
		command := exec.Command("/usr/bin/daed", "prepare-luci", "--config", *configDir)
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "Cannot prepare LuCI access:", err)
			os.Exit(1)
		}
		return
	}
	target, err := backendURL()
	if err == nil {
		err = cgi.Serve(gateway(*configDir, target, sessionAccess))
	}
	if err != nil {
		io.WriteString(os.Stdout, "Status: 503 Service Unavailable\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nNaiLink 服务尚未就绪。\n")
	}
}
