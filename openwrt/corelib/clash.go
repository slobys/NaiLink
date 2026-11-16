// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// ClashLink imports one proxy, not the surrounding listeners, DNS or routes.
// Common proxies become ordinary links so all three engines can still use them.
// Unknown options fail closed instead of silently weakening authentication.
func ClashLink(p Object) (string, error) {
	// A wrong scalar type must not become an empty value/default. In
	// particular, numeric passwords and malformed security fields are errors.
	for _, key := range []string{"type", "name", "server", "password", "username", "uuid", "cipher", "sni", "servername", "client-fingerprint", "network", "ws-path", "flow", "packet-encoding", "encryption", "plugin", "protocol", "obfs", "protocol-param", "obfs-param", "congestion-controller", "udp-relay-mode"} {
		if value, exists := p[key]; exists {
			if _, ok := value.(string); !ok {
				return "", OptionProblem("invalid_node", key)
			}
		}
	}
	used := map[string]bool{}
	take := func(k string) any { used[k] = true; return p[k] }
	text := func(k string) string {
		v := take(k)
		s, _ := v.(string)
		return s
	}
	kind, name, server := text("type"), text("name"), text("server")
	if kind == "direct" || kind == "reject" || kind == "reject-drop" {
		return "", nil
	}
	if kind != "vmess" && kind != "vless" && kind != "trojan" {
		// An explicit SS TCP marker describes the transport, not an instruction
		// to disable UDP relay. Other transport options still fail closed.
		if network := text("network"); network != "" && !(kind == "ss" && (network == "tcp" || network == "raw")) {
			return "", OptionProblem("unsupported_node_option", "network")
		}
		for _, key := range []string{"ws-opts", "ws-path", "ws-headers", "grpc-opts", "xhttp-opts", "reality-opts"} {
			if _, exists := p[key]; exists {
				return "", OptionProblem("unsupported_node_option", key)
			}
		}
	}
	if _, exists := p["reality-opts"]; exists && kind != "vless" {
		return "", OptionProblem("unsupported_node_option", "reality-opts")
	}
	port, err := clashPort(take("port"))
	if err != nil || server == "" || name == "" || len(name) > 1024 || strings.ContainsAny(server, " /\\\r\n\x00\t@") || strings.ContainsAny(name, "\r\n\x00") {
		return "", errors.New("invalid_node")
	}
	q := url.Values{}
	boolOption := func(key, query string) error {
		v := take(key)
		if v == nil {
			return nil
		}
		b, ok := v.(bool)
		if !ok {
			return OptionProblem("invalid_node", key)
		}
		q.Set(query, strconv.FormatBool(b))
		return nil
	}
	if err := boolOption("skip-cert-verify", "insecure"); err != nil {
		return "", err
	}
	if err := boolOption("udp", "udp"); err != nil {
		return "", err
	}
	for _, key := range []string{"sni", "servername"} {
		if v := text(key); v != "" {
			if old := q.Get("sni"); old != "" && old != v {
				return "", OptionProblem("conflicting_node_options", "sni", "servername")
			}
			q.Set("sni", v)
		}
	}
	if v := take("alpn"); v != nil {
		list, e := clashStrings(v)
		if e != nil {
			return "", OptionProblem("invalid_tls_option", "alpn")
		}
		q.Set("alpn", strings.Join(list, ","))
	}
	if v := text("client-fingerprint"); v != "" {
		q.Set("fp", v)
	}
	tlsEnabled := kind == "trojan" || kind == "anytls" || kind == "hysteria2" || kind == "tuic" || kind == "hysteria"
	if v := take("tls"); v != nil {
		b, ok := v.(bool)
		if !ok {
			return "", OptionProblem("invalid_tls_option", "tls")
		}
		if tlsEnabled && !b {
			return "", OptionProblem("conflicting_node_options", "tls")
		}
		tlsEnabled = b
	}
	if tlsEnabled {
		q.Set("security", "tls")
	}
	if v := take("reality-opts"); v != nil {
		o, ok := v.(map[string]any)
		if !ok {
			return "", errors.New("invalid_reality")
		}
		if err := clashOnly(o, "public-key", "short-id", "support-x25519mlkem768"); err != nil {
			return "", err
		}
		q.Set("security", "reality")
		q.Set("pbk", stringValue(o["public-key"]))
		q.Set("sid", stringValue(o["short-id"]))
		if value, exists := o["support-x25519mlkem768"]; exists {
			if value != true {
				return "", OptionProblem("unsupported_reality_key_share", "support-x25519mlkem768")
			}
			q.Set("support-x25519mlkem768", "true")
		}
	}
	transport, e := clashTransport(p, used)
	if e != nil {
		return "", e
	}
	for k, v := range transport {
		q.Set(k, v)
	}
	u := &url.URL{Scheme: kind, Host: net.JoinHostPort(server, strconv.Itoa(port)), Fragment: name}
	password, username := "", ""
	if kind != "vmess" && kind != "vless" {
		password = text("password")
	}
	if kind == "socks5" || kind == "http" {
		username = text("username")
	}
	switch kind {
	case "ss":
		method := text("cipher")
		if method == "" || password == "" {
			return "", errors.New("invalid_node")
		}
		u.User = url.User(base64.RawURLEncoding.EncodeToString([]byte(method + ":" + password)))
		if plugin := text("plugin"); plugin != "" {
			options, ok := take("plugin-opts").(map[string]any)
			if !ok {
				return "", OptionProblem("invalid_node", "plugin-opts")
			}
			value, e := clashPlugin(plugin, options)
			if e != nil {
				return "", e
			}
			q.Set("plugin", value)
		}
		if err := boolOption("udp-over-tcp", "uot"); err != nil {
			return "", err
		}
	case "ssr":
		method, protocol, obfs := text("cipher"), text("protocol"), text("obfs")
		if method == "" || protocol == "" || obfs == "" || password == "" {
			return "", errors.New("invalid_node")
		}
		enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
		params := url.Values{"remarks": {enc(name)}}
		for _, k := range []string{"protocol-param", "obfs-param"} {
			if v := text(k); v != "" {
				params.Set(map[string]string{"protocol-param": "protoparam", "obfs-param": "obfsparam"}[k], enc(v))
			}
		}
		if err := clashUnused(p, used); err != nil {
			return "", err
		}
		if strings.Contains(server, ":") || len(q) > 1 || q.Get("udp") == "false" || (len(q) == 1 && q.Get("udp") == "") {
			return "", errors.New("unsupported_node_option")
		}
		return "ssr://" + enc(server+":"+strconv.Itoa(port)+":"+protocol+":"+method+":"+obfs+":"+enc(password)+"/?"+params.Encode()), nil
	case "vmess":
		id := text("uuid")
		if id == "" {
			return "", errors.New("invalid_node")
		}
		alter := 0
		if v := take("alterId"); v != nil {
			n, e := clashNonnegativeInt(v)
			if e != nil || n != 0 {
				return "", errors.New("unsupported_vmess_alterid")
			}
			alter = n
		}
		cipher := text("cipher")
		if cipher == "" {
			cipher = "auto"
		}
		if err := clashUnused(p, used); err != nil {
			return "", err
		}
		if q.Get("udp") == "false" {
			return clashNativeVMess(p, name, server, port, id, cipher, alter, q)
		}
		v := Object{"v": "2", "ps": name, "add": server, "port": strconv.Itoa(port), "id": id, "aid": "0", "scy": cipher, "type": "none", "net": q.Get("type")}
		if v["net"] == "" {
			v["net"] = "tcp"
		}
		for from, to := range map[string]string{"host": "host", "path": "path", "serviceName": "serviceName", "sni": "sni", "alpn": "alpn", "fp": "fp", "insecure": "allowInsecure"} {
			if s := q.Get(from); s != "" {
				if from == "insecure" {
					v[to] = s == "true"
				} else {
					v[to] = s
				}
			}
		}
		if tlsEnabled {
			v["tls"] = "tls"
		}
		raw, _ := json.Marshal(v)
		link := "vmess://" + base64.StdEncoding.EncodeToString(raw)
		if _, e := Node(link, "clash", NodeOptions{}); e != nil {
			return "", e
		}
		return link, nil
	case "vless":
		id := text("uuid")
		if id == "" {
			return "", errors.New("invalid_node")
		}
		u.User = url.User(id)
		for from, to := range map[string]string{"flow": "flow", "packet-encoding": "packetEncoding", "encryption": "encryption"} {
			if s := text(from); s != "" {
				q.Set(to, s)
			}
		}
	case "trojan", "anytls", "hysteria2", "hysteria":
		if password == "" {
			return "", errors.New("invalid_node")
		}
		u.User = url.User(password)
		if kind == "hysteria2" {
			for from, to := range map[string]string{"obfs": "obfs", "obfs-password": "obfs-password", "ports": "mport", "up": "up", "down": "down", "hop-interval": "hop-interval"} {
				v := take(from)
				if v != nil {
					switch x := v.(type) {
					case string:
						q.Set(to, x)
					case int:
						q.Set(to, strconv.Itoa(x))
					default:
						return "", OptionProblem("invalid_node", from)
					}
				}
			}
		}
		if kind == "anytls" {
			for _, k := range []string{"idle-session-check-interval", "idle-session-timeout", "min-idle-session"} {
				if _, ok := p[k]; ok {
					return "", OptionProblem("unsupported_node_option", k)
				}
			}
		}
		if kind == "hysteria" {
			return clashNativeHysteria(p, used, name, server, port, password, q)
		}
	case "tuic":
		id := text("uuid")
		if id == "" || password == "" {
			return "", errors.New("invalid_node")
		}
		u.User = url.UserPassword(id, password)
		for from, to := range map[string]string{"congestion-controller": "congestion_control", "udp-relay-mode": "udp_relay_mode"} {
			if s := text(from); s != "" {
				q.Set(to, s)
			}
		}
		if err := boolOption("disable-sni", "disable_sni"); err != nil {
			return "", err
		}
		if err := boolOption("reduce-rtt", "zero_rtt_handshake"); err != nil {
			return "", err
		}
	case "socks5", "http":
		if username != "" || password != "" {
			u.User = url.UserPassword(username, password)
		}
		if kind == "http" && tlsEnabled {
			u.Scheme = "https"
		}
	default:
		return "", errors.New("unsupported_protocol")
	}
	if err := clashUnused(p, used); err != nil {
		return "", err
	}
	// dae's legacy Trojan/TUIC/HTTP readers use allowInsecure, while
	// AnyTLS expects numeric insecure. Emit the shared spelling understood by
	// all engines; VMess already carries a typed JSON boolean above.
	if value := q.Get("insecure"); value != "" {
		if value == "true" {
			value = "1"
		} else {
			value = "0"
		}
		switch kind {
		case "vless", "trojan", "tuic", "http", "socks5":
			q.Del("insecure")
			q.Set("allowInsecure", value)
		default:
			q.Set("insecure", value)
		}
	}
	// Only these schemes accept an explicit TLS marker; implied-TLS protocols
	// retain certificate options without adding an unsupported URI parameter.
	if kind != "vless" && kind != "trojan" && kind != "hysteria2" {
		q.Del("security")
	}
	if kind == "ss" {
		if len(transport) > 0 || tlsEnabled || q.Get("sni") != "" || q.Get("insecure") != "" {
			return "", errors.New("unsupported_node_option")
		}
	}
	if kind != "vless" && kind != "trojan" {
		q.Del("udp")
	}
	u.RawQuery = q.Encode()
	link := u.String()
	parsed, e := Node(link, "clash", NodeOptions{Import: true})
	if e != nil {
		return "", e
	}
	if p["udp"] == false && kind != "vless" && kind != "trojan" {
		parsed["tag"], parsed["network"] = name, "tcp"
		raw, _ := json.Marshal(parsed)
		return NativeLink(string(raw))
	}
	return link, nil
}

func clashTransport(p Object, used map[string]bool) (map[string]string, error) {
	used["network"] = true
	kind := stringValue(p["network"])
	q := map[string]string{}
	switch kind {
	case "", "tcp", "raw":
		return q, nil
	case "xhttp":
		if p["type"] != "vless" {
			return nil, errors.New("unsupported_transport")
		}
		used["xhttp-opts"] = true
		o := Object{}
		if value, exists := p["xhttp-opts"]; exists {
			var ok bool
			o, ok = value.(map[string]any)
			if !ok {
				return nil, errors.New("unsupported_transport_option")
			}
		}
		if err := validateXHTTP(o); err != nil {
			return nil, err
		}
		extra := Object{}
		for key, value := range o {
			extra[xhttpJSONKeys[key]] = value
		}
		raw, _ := json.Marshal(extra)
		q["type"], q["extra"] = "xhttp", string(raw)
	case "ws":
		q["type"] = "ws"
		used["ws-opts"], used["ws-path"], used["ws-headers"] = true, true, true
		modern := Object{}
		if v := p["ws-opts"]; v != nil {
			var ok bool
			modern, ok = v.(map[string]any)
			if !ok {
				return nil, errors.New("unsupported_transport_option")
			}
		}
		if e := clashOnly(modern, "path", "headers"); e != nil {
			return nil, e
		}
		path := stringValue(modern["path"])
		if value, exists := modern["path"]; exists {
			if _, ok := value.(string); !ok {
				return nil, OptionProblem("invalid_node", "ws-opts.path")
			}
		}
		legacy := stringValue(p["ws-path"])
		if path != "" && legacy != "" && path != legacy {
			return nil, OptionProblem("conflicting_node_options", "ws-path", "ws-opts.path")
		}
		if path == "" {
			path = legacy
		}
		q["path"] = path
		for _, value := range []any{p["ws-headers"], modern["headers"]} {
			if value == nil {
				continue
			}
			headers, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("unsupported_transport_option")
			}
			for k, v := range headers {
				if !strings.EqualFold(k, "Host") {
					return nil, OptionProblem("unsupported_transport_option", k)
				}
				host, ok := v.(string)
				if !ok {
					return nil, errors.New("unsupported_transport_option")
				}
				if old := q["host"]; old != "" && old != host {
					return nil, errors.New("conflicting_node_options")
				}
				q["host"] = host
			}
		}
	case "grpc":
		q["type"] = "grpc"
		used["grpc-opts"] = true
		o, ok := p["grpc-opts"].(map[string]any)
		if !ok {
			return nil, errors.New("unsupported_transport_option")
		}
		if e := clashOnly(o, "grpc-service-name"); e != nil {
			return nil, e
		}
		if value, exists := o["grpc-service-name"]; exists {
			if _, ok := value.(string); !ok {
				return nil, OptionProblem("invalid_node", "grpc-service-name")
			}
		}
		q["serviceName"] = stringValue(o["grpc-service-name"])
	default:
		return nil, errors.New("unsupported_transport")
	}
	return q, nil
}
func clashPort(v any) (int, error) {
	n, e := clashNonnegativeInt(v)
	if e != nil || n < 1 || n > 65535 {
		return 0, errors.New("invalid_node")
	}
	return n, nil
}
func clashNonnegativeInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		if n >= 0 {
			return n, nil
		}
	case float64:
		if n >= 0 && n <= 1<<31 && n == float64(int(n)) {
			return int(n), nil
		}
	case string:
		if i, e := strconv.Atoi(n); e == nil && i >= 0 {
			return i, nil
		}
	}
	return 0, errors.New("invalid_node")
}
func clashStrings(v any) ([]string, error) {
	values, ok := v.([]any)
	if !ok {
		return nil, errors.New("invalid_node")
	}
	var out []string
	for _, v := range values {
		s, ok := v.(string)
		if !ok || strings.Contains(s, ",") {
			return nil, errors.New("invalid_node")
		}
		out = append(out, s)
	}
	return out, nil
}
func clashUnused(p Object, used map[string]bool) error {
	var fields []string
	for k := range p {
		if !used[k] {
			fields = append(fields, k)
		}
	}
	if len(fields) > 0 {
		return OptionProblem("unsupported_node_option", fields...)
	}
	return nil
}
func clashOnly(p Object, keys ...string) error {
	used := map[string]bool{}
	for _, k := range keys {
		used[k] = true
	}
	return clashUnused(p, used)
}
func clashPlugin(plugin string, p Object) (string, error) {
	if plugin == "obfs" {
		plugin = "obfs-local"
		if e := clashOnly(p, "mode", "host"); e != nil {
			return "", e
		}
		p = Object{"obfs": p["mode"], "obfs-host": p["host"]}
	}
	if plugin != "obfs-local" && plugin != "v2ray-plugin" {
		return "", errors.New("unsupported_ss_plugin")
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{plugin}
	for _, k := range keys {
		switch v := p[k].(type) {
		case bool:
			if v {
				parts = append(parts, k)
			} else {
				parts = append(parts, k+"=false")
			}
		case string:
			if strings.ContainsAny(k+v, ";\r\n\x00") {
				return "", errors.New("unsupported_ss_plugin")
			}
			parts = append(parts, k+"="+v)
		default:
			return "", errors.New("unsupported_ss_plugin")
		}
	}
	return strings.Join(parts, ";"), nil
}
func clashNativeVMess(p Object, name, server string, port int, id, cipher string, alter int, q url.Values) (string, error) {
	v := Object{"type": "vmess", "tag": name, "server": server, "server_port": port, "uuid": id, "security": cipher, "alter_id": alter, "network": "tcp"}
	linkPayload := Object{"v": "2", "ps": name, "add": server, "port": strconv.Itoa(port), "id": id, "aid": "0", "scy": cipher, "net": q.Get("type"), "type": "none"}
	if linkPayload["net"] == "" {
		linkPayload["net"] = "tcp"
	}
	for from, to := range map[string]string{"host": "host", "path": "path", "serviceName": "serviceName", "sni": "sni", "alpn": "alpn", "fp": "fp", "insecure": "allowInsecure"} {
		if s := q.Get(from); s != "" {
			if from == "insecure" {
				linkPayload[to] = s == "true"
			} else {
				linkPayload[to] = s
			}
		}
	}
	if p["tls"] == true {
		linkPayload["tls"] = "tls"
	}
	raw, _ := json.Marshal(linkPayload)
	parsed, e := Node("vmess://"+base64.StdEncoding.EncodeToString(raw), "clash", NodeOptions{})
	if e != nil {
		return "", e
	}
	for _, k := range []string{"tls", "transport"} {
		if x := parsed[k]; x != nil {
			v[k] = x
		}
	}
	raw, _ = json.Marshal(v)
	return NativeLink(string(raw))
}
func clashNativeHysteria(p Object, used map[string]bool, name, server string, port int, password string, q url.Values) (string, error) {
	v := Object{"type": "hysteria", "tag": name, "server": server, "server_port": port, "auth_str": password, "tls": Object{"enabled": true, "server_name": q.Get("sni"), "insecure": q.Get("insecure") == "true"}}
	for _, k := range []string{"up", "down"} {
		used[k] = true
		n, e := clashNonnegativeInt(p[k])
		if e != nil || n == 0 {
			return "", OptionProblem("invalid_node", k)
		}
		v[k+"_mbps"] = n
	}
	if e := clashUnused(p, used); e != nil {
		return "", e
	}
	for key := range q {
		if key != "sni" && key != "insecure" && key != "security" && key != "udp" {
			return "", OptionProblem("unsupported_node_option", key)
		}
	}
	if p["udp"] == false {
		v["network"] = "tcp"
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return "", errors.New("invalid_node")
	}
	return NativeLink(string(raw))
}
