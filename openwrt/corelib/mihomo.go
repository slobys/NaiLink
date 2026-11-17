// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const MihomoVersion = "1.19.33"
const MihomoRelease = "openwrt-v0.6.40"
const MihomoBinaryPath = "/usr/libexec/daed/cores/mihomo"

// Keep the original default valid for installations without a version receipt.
// New installs publish a receipt for the reviewed compatibility core.
const ReviewedMihomoVersion = "1.19.34-nailink.1"
const ReviewedMihomoRelease = "nailink-cores-20261117"

var ReviewedMihomoReleases = map[string]Release{
	"amd64": {"x86_64", "nailink-mihomo-1.19.34-nailink.1-x86_64.tar.gz", "aed28a081ff80f234e15484d238b4f02b43c64bb51082c0ecca977b7c8576603"},
	"arm64": {"arm64", "nailink-mihomo-1.19.34-nailink.1-arm64.tar.gz", "d235ca113901b47c4ff415635f0d690efaf580b2cbe364d3b294494b0014385f"},
}

var MihomoReleases = map[string]Release{
	"amd64": {"x86_64", "nailink-mihomo-1.19.33-x86_64.tar.gz", "db29d98245b0f8edde2fd337987a2002396ceeffcf1716a5ecf1f387453710f7"},
	"arm64": {"arm64", "nailink-mihomo-1.19.33-arm64.tar.gz", "6e79715e8c63786bbda35298c7b4bf56acae1d3401ae97dcc0e4c764404c897f"},
}

// Proxy and policy identifiers are never interpolated from imported text into
// Mihomo's comma-delimited rule language. The original node remains in the DB.
func MihomoName(tag string) string {
	if tag == "direct" {
		return "DIRECT"
	}
	h := sha256.Sum256([]byte(tag))
	return "nk-" + hex.EncodeToString(h[:12])
}

func MihomoSSR(link, tag string) (Object, error) {
	raw, err := decode64(strings.TrimPrefix(link, "ssr://"))
	if err != nil || len(raw) > 32768 {
		return nil, errors.New("invalid_node")
	}
	parts := strings.SplitN(string(raw), "/?", 2)
	p := strings.Split(parts[0], ":")
	if len(p) != 6 {
		return nil, errors.New("invalid_node")
	}
	port, err := strconv.Atoi(p[1])
	if err != nil || port < 1 || port > 65535 || p[0] == "" || strings.ContainsAny(p[0], " /\\\r\n\x00") {
		return nil, errors.New("invalid_node")
	}
	password, err := decode64(p[5])
	if err != nil {
		return nil, err
	}
	o := Object{"type": "_nailink_ssr", "tag": tag, "server": p[0], "server_port": port, "protocol": p[2], "method": p[3], "obfs": p[4], "password": string(password)}
	if len(parts) == 2 {
		q, err := url.ParseQuery(parts[1])
		if err != nil {
			return nil, errors.New("invalid_node")
		}
		for k, v := range q {
			if len(v) != 1 {
				return nil, errors.New("invalid_node")
			}
			switch k {
			case "remarks", "group":
			case "protoparam", "obfsparam":
				b, e := decode64(v[0])
				if e != nil {
					return nil, e
				}
				o[k] = string(b)
			default:
				return nil, OptionProblem("unsupported_node_option", k)
			}
		}
	}
	return o, nil
}

// Only fields explicitly mapped below are accepted. Native sing-box JSON can
// never introduce listeners, filesystem access or arbitrary Mihomo options.
func MihomoNode(node Object) (Object, error) {
	kind, _ := node["type"].(string)
	if kind == "direct" {
		return nil, nil
	}
	allowed := map[string]bool{}
	allow := func(keys ...string) {
		for _, k := range keys {
			allowed[k] = true
		}
	}
	allow("type", "tag", "server", "server_port", "detour", "_nailink_children")
	o := Object{"type": kind, "name": MihomoName(node["tag"].(string)), "server": node["server"], "port": node["server_port"], "udp": true}
	copyFields := func(mapping map[string]string) {
		for from, to := range mapping {
			allow(from)
			if v, ok := node[from]; ok {
				o[to] = v
			}
		}
	}
	copyFields(map[string]string{"password": "password", "username": "username", "tcp_fast_open": "tfo", "bind_interface": "interface-name"})
	if d, ok := node["detour"].(string); ok {
		o["dialer-proxy"] = MihomoName(d)
	}
	allow("network")
	if node["network"] == "tcp" {
		o["udp"] = false
	} else if node["network"] != nil && node["network"] != "" && node["network"] != "tcp,udp" {
		return nil, OptionProblem("mihomo_option_unsupported", "network")
	}
	switch kind {
	case "shadowsocks":
		o["type"] = "ss"
		copyFields(map[string]string{"method": "cipher"})
		allow("plugin", "plugin_opts", "udp_over_tcp")
		if p, ok := node["plugin"].(string); ok && p != "" {
			opts, err := mihomoPlugin(p, stringValue(node["plugin_opts"]))
			if err != nil {
				return nil, err
			}
			if p == "obfs-local" {
				o["plugin"] = "obfs"
			} else {
				o["plugin"] = p
			}
			o["plugin-opts"] = opts
		}
		if u, ok := node["udp_over_tcp"].(map[string]any); ok {
			if err := mhOnly(u, "enabled", "version"); err != nil {
				return nil, err
			}
			o["udp-over-tcp"] = u["enabled"]
			if v := u["version"]; v != nil {
				o["udp-over-tcp-version"] = v
			}
		}
	case "_nailink_ssr":
		o["type"] = "ssr"
		copyFields(map[string]string{"method": "cipher", "protocol": "protocol", "obfs": "obfs", "protoparam": "protocol-param", "obfsparam": "obfs-param"})
	case "vmess":
		copyFields(map[string]string{"uuid": "uuid", "security": "cipher", "alter_id": "alterId", "packet_encoding": "packet-encoding"})
	case "vless":
		copyFields(map[string]string{"uuid": "uuid", "flow": "flow", "packet_encoding": "packet-encoding", "encryption": "encryption"})
	case "trojan", "anytls", "http":
		if kind == "anytls" {
			copyFields(map[string]string{"idle_session_check_interval": "idle-session-check-interval", "idle_session_timeout": "idle-session-timeout", "min_idle_session": "min-idle-session"})
		}
	case "socks":
		o["type"] = "socks5"
		allow("version")
		if node["version"] != nil && node["version"] != "5" {
			return nil, errors.New("mihomo_protocol_unsupported")
		}
	case "hysteria2":
		copyFields(map[string]string{"up_mbps": "up", "down_mbps": "down", "server_ports": "ports", "hop_interval": "hop-interval"})
		allow("obfs")
		if v, ok := node["obfs"].(map[string]any); ok {
			if err := mhOnly(v, "type", "password"); err != nil {
				return nil, err
			}
			o["obfs"] = v["type"]
			o["obfs-password"] = v["password"]
		}
	case "hysteria":
		copyFields(map[string]string{"up_mbps": "up", "down_mbps": "down", "auth_str": "auth-str", "auth": "auth", "obfs": "obfs", "server_ports": "ports", "hop_interval": "hop-interval", "recv_window_conn": "recv-window-conn", "recv_window": "recv-window", "disable_mtu_discovery": "disable-mtu-discovery"})
	case "tuic":
		copyFields(map[string]string{"uuid": "uuid", "congestion_control": "congestion-controller", "udp_relay_mode": "udp-relay-mode", "zero_rtt_handshake": "reduce-rtt", "heartbeat": "heartbeat-interval"})
	case "snell":
		copyFields(map[string]string{"psk": "psk", "version": "version"})
		if v := node["version"]; v != nil && intValue(v) != 4 {
			return nil, OptionProblem("mihomo_option_unsupported", "version")
		}
	case "wireguard":
		allow("address", "private_key", "peers", "mtu", "system", "workers")
		o["type"] = "wireguard"
		o["private-key"] = node["private_key"]
		o["mtu"] = node["mtu"]
		for _, address := range stringValues(node["address"]) {
			host, _, e := net.ParseCIDR(address)
			if e != nil {
				return nil, errors.New("invalid_wireguard")
			}
			if host.To4() != nil {
				if o["ip"] != nil {
					return nil, OptionProblem("mihomo_option_unsupported", "multiple_ipv4_addresses")
				}
				o["ip"] = host.String()
			} else {
				if o["ipv6"] != nil {
					return nil, OptionProblem("mihomo_option_unsupported", "multiple_ipv6_addresses")
				}
				o["ipv6"] = host.String()
			}
		}
		var peers []Object
		for _, p := range objectValues(node["peers"]) {
			if err := mhOnly(p, "address", "port", "public_key", "pre_shared_key", "allowed_ips", "persistent_keepalive_interval", "reserved"); err != nil {
				return nil, err
			}
			peer := Object{"server": p["address"], "port": p["port"], "public-key": p["public_key"], "allowed-ips": p["allowed_ips"]}
			if v := p["pre_shared_key"]; v != nil {
				peer["pre-shared-key"] = v
			}
			if v := p["reserved"]; v != nil {
				peer["reserved"] = v
			}
			if v := p["persistent_keepalive_interval"]; v != nil {
				return nil, OptionProblem("mihomo_option_unsupported", "persistent_keepalive_interval")
			}
			peers = append(peers, peer)
		}
		o["peers"] = peers
		delete(o, "server")
		delete(o, "port")
		o["workers"] = 1
		if v := intValue(node["workers"]); v > 0 {
			if v > 4 {
				return nil, OptionProblem("mihomo_option_unsupported", "workers")
			}
			o["workers"] = v
		}
	case "ssh":
		copyFields(map[string]string{"user": "username", "private_key_passphrase": "private-key-passphrase", "host_key": "host-key", "host_key_algorithms": "host-key-algorithms"})
		allow("private_key")
		if v, ok := node["private_key"]; ok {
			values := stringValues(v)
			if len(values) != 1 || !strings.HasPrefix(strings.TrimSpace(values[0]), "-----BEGIN ") {
				return nil, OptionProblem("mihomo_option_unsupported", "private_key")
			}
			o["private-key"] = values[0]
		}
		o["udp"] = false
	default:
		return nil, errors.New("mihomo_protocol_unsupported")
	}
	allow("tls", "transport", "multiplex")
	if tls, ok := node["tls"].(map[string]any); ok {
		if err := mhOnly(tls, "enabled", "server_name", "insecure", "alpn", "utls", "reality", "certificate", "ech"); err != nil {
			return nil, err
		}
		if v := tls["enabled"]; v != nil {
			o["tls"] = v
		}
		if v := tls["server_name"]; v != nil {
			if kind == "vless" || kind == "vmess" {
				o["servername"] = v
			} else {
				o["sni"] = v
			}
		}
		if v := tls["insecure"]; v != nil {
			o["skip-cert-verify"] = v
		}
		if v := tls["alpn"]; v != nil {
			if kind == "http" {
				return nil, OptionProblem("mihomo_option_unsupported", "alpn")
			}
			o["alpn"] = v
		}
		if u, ok := tls["utls"].(map[string]any); ok {
			if kind == "http" && u["enabled"] != false {
				return nil, OptionProblem("mihomo_option_unsupported", "utls")
			}
			if err := mhOnly(u, "enabled", "fingerprint"); err != nil {
				return nil, err
			}
			if u["enabled"] != false {
				o["client-fingerprint"] = u["fingerprint"]
			}
		}
		if r, ok := tls["reality"].(map[string]any); ok {
			if err := mhOnly(r, "enabled", "public_key", "short_id"); err != nil {
				return nil, err
			}
			if r["enabled"] != false {
				o["reality-opts"] = Object{"public-key": r["public_key"], "short-id": r["short_id"], "support-x25519mlkem768": true}
			}
		}
		if ech, ok := tls["ech"].(map[string]any); ok {
			if err := mhOnly(ech, "enabled", "config"); err != nil {
				return nil, err
			}
			if ech["enabled"] != false {
				values := stringValues(ech["config"])
				if len(values) != 1 {
					return nil, OptionProblem("mihomo_option_unsupported", "ech")
				}
				block, rest := pem.Decode([]byte(values[0]))
				if block == nil || block.Type != "ECH CONFIGS" || len(strings.TrimSpace(string(rest))) > 0 {
					return nil, OptionProblem("invalid_ech", "ech")
				}
				o["ech-opts"] = Object{"enable": true, "config": base64.StdEncoding.EncodeToString(block.Bytes)}
			}
		}
		// This version's certificate option is client authentication, not a CA.
		if len(stringValues(tls["certificate"])) > 0 {
			return nil, OptionProblem("mihomo_option_unsupported", "certificate")
		}
	}
	if tr, ok := node["transport"].(map[string]any); ok {
		typ := stringValue(tr["type"])
		switch typ {
		case "xhttp":
			if kind != "vless" {
				return nil, OptionProblem("mihomo_option_unsupported", "transport")
			}
			v := Object{}
			for key, value := range tr {
				if key != "type" {
					v[key] = value
				}
			}
			if err := validateXHTTP(v); err != nil {
				return nil, err
			}
			o["network"], o["xhttp-opts"] = "xhttp", v
		case "ws":
			if err := mhOnly(tr, "type", "path", "headers", "max_early_data", "early_data_header_name"); err != nil {
				return nil, err
			}
			o["network"] = "ws"
			v := Object{"path": tr["path"], "headers": tr["headers"]}
			if tr["max_early_data"] != nil {
				v["max-early-data"] = tr["max_early_data"]
				v["early-data-header-name"] = tr["early_data_header_name"]
			}
			o["ws-opts"] = v
		case "grpc":
			if err := mhOnly(tr, "type", "service_name"); err != nil {
				return nil, err
			}
			o["network"] = "grpc"
			o["grpc-opts"] = Object{"grpc-service-name": tr["service_name"]}
		case "http":
			if err := mhOnly(tr, "type", "host", "path", "method", "headers"); err != nil {
				return nil, err
			}
			if tr["method"] != nil && tr["method"] != "GET" {
				return nil, OptionProblem("mihomo_option_unsupported", "method")
			}
			o["network"] = "h2"
			o["h2-opts"] = Object{"host": tr["host"], "path": tr["path"]}
			if tr["headers"] != nil {
				return nil, OptionProblem("mihomo_option_unsupported", "headers")
			}
		default:
			return nil, OptionProblem("mihomo_option_unsupported", "transport")
		}
	}
	if mux, ok := node["multiplex"].(map[string]any); ok {
		if err := mhOnly(mux, "enabled", "protocol", "max_connections", "min_streams", "max_streams", "padding"); err != nil {
			return nil, err
		}
		v := Object{}
		for a, b := range map[string]string{"enabled": "enabled", "protocol": "protocol", "max_connections": "max-connections", "min_streams": "min-streams", "max_streams": "max-streams", "padding": "padding"} {
			if mux[a] != nil {
				v[b] = mux[a]
			}
		}
		o["smux"] = v
	}
	for _, key := range []string{"idle-session-check-interval", "idle-session-timeout", "heartbeat-interval"} {
		if text, ok := o[key].(string); ok {
			duration, e := time.ParseDuration(text)
			if e != nil || duration <= 0 {
				return nil, OptionProblem("mihomo_option_unsupported", key)
			}
			unit := time.Second
			if key == "heartbeat-interval" {
				unit = time.Millisecond
			}
			o[key] = max(1, int(duration/unit))
		}
	}
	if raw, ok := node["hop_interval"].(string); ok {
		duration, e := time.ParseDuration(raw)
		if e != nil {
			n, e2 := strconv.Atoi(raw)
			if e2 != nil || n <= 0 {
				return nil, OptionProblem("mihomo_option_unsupported", "hop_interval")
			}
			duration = time.Duration(n) * time.Second
		}
		if duration <= 0 || duration%time.Second != 0 || kind == "hysteria2" && duration < 5*time.Second {
			return nil, OptionProblem("mihomo_option_unsupported", "hop_interval")
		}
		seconds := int(duration / time.Second)
		if kind == "hysteria2" {
			o["hop-interval"] = strconv.Itoa(seconds)
		} else {
			o["hop-interval"] = seconds
		}
	}
	if ports, ok := node["server_ports"]; ok {
		o["ports"] = strings.ReplaceAll(strings.Join(stringValues(ports), ","), ":", "-")
	}
	var unknown []string
	for k := range node {
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, OptionProblem("mihomo_option_unsupported", unknown...)
	}
	return o, nil
}
func mhOnly(o Object, keys ...string) error {
	allowed := map[string]bool{}
	for _, k := range keys {
		allowed[k] = true
	}
	var bad []string
	for k := range o {
		if !allowed[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return OptionProblem("mihomo_option_unsupported", bad...)
	}
	return nil
}
func stringValue(v any) string { s, _ := v.(string); return s }
func intValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}
func stringValues(v any) []string {
	if a, ok := v.([]string); ok {
		return a
	}
	var r []string
	if a, ok := v.([]any); ok {
		for _, x := range a {
			if s, ok := x.(string); ok {
				r = append(r, s)
			}
		}
	}
	return r
}
func objectValues(v any) []Object {
	if a, ok := v.([]Object); ok {
		return a
	}
	var r []Object
	if a, ok := v.([]any); ok {
		for _, x := range a {
			if o, ok := x.(map[string]any); ok {
				r = append(r, o)
			}
		}
	}
	return r
}

// Plugin flags must retain their security meaning; unknown flags fail closed.
func mihomoPlugin(plugin, raw string) (Object, error) {
	opts := Object{}
	switch plugin {
	case "obfs-local":
	case "v2ray-plugin":
		opts["mode"] = "websocket"
		opts["mux"] = true
	default:
		return nil, errors.New("mihomo_protocol_unsupported")
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ";") {
		if part == "" {
			continue
		}
		pair := strings.SplitN(part, "=", 2)
		key := pair[0]
		value := "true"
		if len(pair) == 2 {
			value = pair[1]
		}
		if seen[key] {
			return nil, OptionProblem("mihomo_option_unsupported", key)
		}
		seen[key] = true
		if plugin == "obfs-local" {
			switch key {
			case "obfs":
				opts["mode"] = value
			case "obfs-host":
				opts["host"] = value
			default:
				return nil, OptionProblem("mihomo_option_unsupported", key)
			}
			continue
		}
		switch key {
		case "mode", "host", "path":
			if len(pair) != 2 {
				return nil, OptionProblem("mihomo_option_unsupported", key)
			}
			opts[key] = value
		case "tls", "mux", "skip-cert-verify":
			v, e := strconv.ParseBool(value)
			if e != nil {
				return nil, OptionProblem("mihomo_option_unsupported", key)
			}
			opts[key] = v
		default:
			return nil, OptionProblem("mihomo_option_unsupported", key)
		}
	}
	return opts, nil
}
