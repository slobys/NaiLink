// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type Object = map[string]any
type NodeOptions struct {
	AllowInsecure bool
	UTLS          string
	Warn          func(string)
	Mihomo        bool
}

func decode64(s string) ([]byte, error) {
	for _, e := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if data, err := e.DecodeString(s); err == nil {
			return data, nil
		}
	}
	return nil, errors.New("invalid_node")
}
func Node(link, tag string, opt NodeOptions) (Object, error) {
	if strings.Contains(link, ChainSeparator) {
		return chainNode(link, tag, opt)
	}
	if opt.Mihomo && strings.HasPrefix(strings.TrimSpace(link), "ssr://") {
		return MihomoSSR(strings.TrimSpace(link), tag)
	}
	if strings.HasPrefix(strings.TrimSpace(link), "naive+https://") {
		return naiveNode(link, tag)
	}
	if IsNativeURI(link) && !strings.HasPrefix(strings.TrimSpace(link), "singbox://") {
		encoded, err := EncodeNativeURI(link)
		if err != nil {
			return nil, err
		}
		link = encoded
	}
	if strings.HasPrefix(strings.TrimSpace(link), "singbox://") {
		node, err := NativeNode(strings.TrimSpace(link), tag)
		if err != nil {
			return nil, err
		}
		all := []Object{node}
		if children, ok := node["_nailink_children"].([]Object); ok {
			all = append(all, children...)
		}
		for _, n := range all {
			if tls, ok := n["tls"].(map[string]any); ok && n["type"] != "naive" {
				if _, explicit := tls["insecure"]; !explicit && opt.AllowInsecure {
					tls["insecure"] = true
				}
			}
		}
		return node, nil
	}
	if len(link) > 32768 || tag == "" {
		return nil, errors.New("invalid_node")
	}
	link = strings.TrimSpace(link)
	if strings.HasPrefix(link, "vmess://") {
		return vmess(link, tag, opt)
	}
	if strings.HasPrefix(link, "ss://") {
		// SIP002 permits both base64 userinfo and the legacy whole-link encoding.
		raw := strings.TrimPrefix(link, "ss://")
		address := strings.SplitN(strings.SplitN(raw, "#", 2)[0], "?", 2)[0]
		suffix := raw[len(address):]
		if !strings.Contains(address, "@") {
			decoded, err := decode64(address)
			if err != nil {
				return nil, err
			}
			address = string(decoded)
		}
		at := strings.LastIndex(address, "@")
		if at < 0 {
			return nil, errors.New("invalid_node")
		}
		auth, server := address[:at], address[at+1:]
		if !strings.Contains(auth, ":") {
			decoded, err := decode64(auth)
			if err != nil {
				return nil, err
			}
			auth = string(decoded)
		} else {
			decoded, err := url.PathUnescape(auth)
			if err != nil {
				return nil, errors.New("invalid_node")
			}
			auth = decoded
		}
		pair := strings.SplitN(auth, ":", 2)
		if len(pair) != 2 {
			return nil, errors.New("invalid_node")
		}
		link = "ss://" + url.UserPassword(pair[0], pair[1]).String() + "@" + server + suffix
	}
	u, err := url.Parse(link)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("invalid_node")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid_node")
	}
	if strings.ContainsAny(u.Hostname(), " /\\\n\r\t") {
		return nil, errors.New("invalid_node")
	}
	q, queryErr := url.ParseQuery(u.RawQuery)
	if queryErr != nil {
		return nil, errors.New("invalid_node")
	}
	used := map[string]bool{"name": true, "remarks": true, "group": true}
	allowed := strings.Fields("security sni peer serverName alpn insecure allowInsecure allow_insecure skip-cert-verify fp fingerprint type net network host path serviceName service_name authority mode flow pbk publicKey sid shortId encryption packetEncoding uuid password method cipher plugin obfs obfs-password obfsPassword mport ports hop-interval hopInterval upmbps downmbps up down congestion_control congestion-control udp_relay_mode udp-relay-mode zero_rtt_handshake disable_sni udp-over-tcp udp_over_tcp uot name remarks group headerType spx udp ech support-x25519mlkem768")
	known := map[string]bool{}
	for _, key := range allowed {
		known[key] = true
	}
	var rejected []string
	for key, values := range q {
		if !known[key] || len(values) != 1 {
			rejected = append(rejected, key)
		}
	}
	if len(rejected) > 0 {
		return nil, OptionProblem("unsupported_node_option", rejected...)
	}
	for _, aliases := range [][]string{{"sni", "peer", "serverName"}, {"fp", "fingerprint"}, {"type", "net", "network"}, {"insecure", "allowInsecure", "allow_insecure", "skip-cert-verify"}} {
		var first string
		for _, key := range aliases {
			v := q.Get(key)
			if v == "" {
				continue
			}
			if first != "" && first != v {
				return nil, errors.New("conflicting_node_options")
			}
			first = v
		}
	}
	for _, key := range []string{"insecure", "allowInsecure", "allow_insecure", "skip-cert-verify"} {
		if v := q.Get(key); v != "" && v != "0" && v != "1" && v != "true" && v != "false" {
			return nil, errors.New("invalid_tls_option")
		}
	}
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New("unsupported_node_path")
	}
	get := func(keys ...string) string {
		for _, key := range keys {
			used[key] = true
		}
		for _, key := range keys {
			if v := q.Get(key); v != "" {
				return v
			}
		}
		return ""
	}
	username, password := "", ""
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	auth := username
	if password != "" {
		auth += ":" + password
	}
	result := Object{"type": u.Scheme, "tag": tag, "server": u.Hostname(), "server_port": port}
	tls := func() Object {
		value := Object{"enabled": true, "insecure": opt.AllowInsecure}
		if sni := get("sni", "peer", "serverName"); sni != "" {
			value["server_name"] = sni
		}
		if alpn := get("alpn"); alpn != "" {
			value["alpn"] = strings.Split(alpn, ",")
		}
		insecure := get("insecure", "allowInsecure", "allow_insecure", "skip-cert-verify")
		if insecure != "" {
			value["insecure"] = insecure == "1" || strings.EqualFold(insecure, "true")
		}
		fp := get("fp", "fingerprint")
		if fp == "" {
			fp = opt.UTLS
		}
		if fp != "" {
			value["utls"] = Object{"enabled": true, "fingerprint": strings.TrimSuffix(fp, "_auto")}
		}
		return value
	}
	switch u.Scheme {
	case "ss":
		// Several exporters include an explicit plain TCP marker. It is not
		// WebSocket/plugin transport and must not disable application UDP.
		for _, key := range []string{"type", "net", "network"} {
			if kind := get(key); kind != "" && kind != "tcp" && kind != "raw" {
				return nil, OptionProblem("unsupported_transport", key)
			}
		}
		if header := get("headerType"); header != "" && header != "none" {
			return nil, OptionProblem("unsupported_header_type", "headerType")
		}
		if password == "" {
			decoded, e := decode64(username)
			if e != nil {
				return nil, e
			}
			pair := strings.SplitN(string(decoded), ":", 2)
			if len(pair) != 2 {
				return nil, errors.New("invalid_node")
			}
			username, password = pair[0], pair[1]
		}
		result["type"] = "shadowsocks"
		result["method"] = username
		result["password"] = password
		if plugin := get("plugin"); plugin != "" {
			parts := strings.SplitN(plugin, ";", 2)
			if parts[0] != "obfs-local" && parts[0] != "simple-obfs" && parts[0] != "v2ray-plugin" {
				return nil, errors.New("unsupported_ss_plugin")
			}
			if parts[0] == "simple-obfs" {
				parts[0] = "obfs-local"
			}
			result["plugin"] = parts[0]
			if len(parts) == 2 {
				result["plugin_opts"] = parts[1]
			}
		}
		if v := get("uot", "udp-over-tcp", "udp_over_tcp"); v != "" && v != "0" && v != "false" {
			if v != "1" && v != "2" && v != "true" {
				return nil, errors.New("unsupported_node_option")
			}
			result["udp_over_tcp"] = Object{"enabled": true, "version": 2}
		}
	case "vless", "trojan":
		if header := get("headerType"); header != "" && header != "none" {
			return nil, OptionProblem("unsupported_header_type", "headerType")
		}
		if value := get("udp"); value != "" {
			switch value {
			case "0", "false":
				result["network"] = "tcp"
			case "1", "true": // Native sing-box enables TCP and UDP by default.
			default:
				return nil, OptionProblem("invalid_udp_option", "udp")
			}
		}
		if u.Scheme == "vless" {
			result["uuid"] = username
			if flow := get("flow"); flow != "" && flow != "none" {
				result["flow"] = flow
			}
			if enc := get("encryption"); enc != "" && enc != "none" {
				return nil, errors.New("unsupported_vless_encryption")
			}
			if encoding := get("packetEncoding"); encoding != "" {
				if encoding != "xudp" && encoding != "packetaddr" {
					return nil, errors.New("unsupported_packet_encoding")
				}
				result["packet_encoding"] = encoding
			}
		} else {
			result["password"] = auth
		}
		security := get("security")
		if spider := get("spx"); spider != "" {
			if security != "reality" || len(spider) > 2048 || !strings.HasPrefix(spider, "/") || strings.ContainsAny(spider, "\x00\r\n") {
				return nil, OptionProblem("invalid_spider_path", "spx")
			}
			if opt.Warn != nil {
				opt.Warn("spx 是 Xray 在 REALITY 验证失败后的爬虫路径；sing-box 没有对应行为。原始链接保留，正常 REALITY 认证仍使用原公钥、短 ID 和指纹。")
			}
		}
		if u.Scheme == "trojan" || security == "tls" || security == "reality" {
			result["tls"] = tls()
		} else if security != "" && security != "none" {
			return nil, errors.New("unsupported_tls")
		}
		if security == "reality" {
			t := result["tls"].(Object)
			if get("pbk", "publicKey") == "" {
				return nil, errors.New("invalid_reality")
			}
			t["reality"] = Object{"enabled": true, "public_key": get("pbk", "publicKey"), "short_id": get("sid", "shortId")}
			if _, ok := t["utls"]; !ok {
				t["utls"] = Object{"enabled": true, "fingerprint": "chrome"}
			}
			if hybrid := get("support-x25519mlkem768"); hybrid != "" {
				if hybrid != "true" && hybrid != "1" {
					return nil, OptionProblem("unsupported_reality_key_share", "support-x25519mlkem768")
				}
				if opt.Warn != nil {
					opt.Warn("support-x25519mlkem768 表示服务端支持混合密钥交换；兼容内核保留所选指纹的原生密钥协商和 REALITY 认证，不改变公钥、短 ID 或指纹。")
				}
			}
		}
		if err := nodeTransport(result, q, used); err != nil {
			return nil, err
		}
	case "hy2", "hysteria2":
		if security := get("security"); security != "" && security != "tls" {
			return nil, OptionProblem("unsupported_tls", "security")
		}
		if q.Get("fp") != "" || q.Get("fingerprint") != "" {
			return nil, errors.New("unsupported_quic_fingerprint")
		}
		result["type"] = "hysteria2"
		result["password"] = auth
		result["tls"] = tls()
		if obfs := get("obfs"); obfs != "" {
			if obfs != "salamander" {
				return nil, errors.New("unsupported_hy2_obfs")
			}
			result["obfs"] = Object{"type": "salamander", "password": get("obfs-password", "obfsPassword")}
		}
		if ports := get("mport", "ports"); ports != "" {
			var ranges []string
			for _, entry := range strings.Split(ports, ",") {
				bounds := strings.Split(strings.ReplaceAll(strings.TrimSpace(entry), "-", ":"), ":")
				if len(bounds) == 1 {
					bounds = append(bounds, bounds[0])
				}
				if len(bounds) != 2 {
					return nil, errors.New("invalid_port_range")
				}
				first, e1 := strconv.Atoi(bounds[0])
				last, e2 := strconv.Atoi(bounds[1])
				if e1 != nil || e2 != nil || first < 1 || last > 65535 || first > last {
					return nil, errors.New("invalid_port_range")
				}
				ranges = append(ranges, strconv.Itoa(first)+":"+strconv.Itoa(last))
			}
			result["server_ports"] = ranges
		}
		if hop := get("hop-interval", "hopInterval"); hop != "" {
			result["hop_interval"] = hop
		}
		for key, aliases := range map[string][]string{"up_mbps": {"upmbps", "up"}, "down_mbps": {"downmbps", "down"}} {
			if v := get(aliases...); v != "" {
				n, e := strconv.Atoi(v)
				if e != nil || n < 1 {
					return nil, errors.New("invalid_bandwidth")
				}
				result[key] = n
			}
		}
		// QUIC TLS cannot use a TCP uTLS handshake.
		delete(result["tls"].(Object), "utls")
	case "anytls":
		result["password"] = auth
		result["tls"] = tls()
	case "tuic":
		if q.Get("fp") != "" || q.Get("fingerprint") != "" {
			return nil, errors.New("unsupported_quic_fingerprint")
		}
		result["uuid"] = username
		result["password"] = password
		result["tls"] = tls()
		delete(result["tls"].(Object), "utls")
		for key, aliases := range map[string][]string{"congestion_control": {"congestion_control", "congestion-control"}, "udp_relay_mode": {"udp_relay_mode", "udp-relay-mode"}} {
			if v := get(aliases...); v != "" {
				result[key] = v
			}
		}
		if v := get("disable_sni"); v == "1" || v == "true" {
			result["tls"].(Object)["disable_sni"] = true
		}
		if v := get("zero_rtt_handshake"); v == "1" || v == "true" {
			result["zero_rtt_handshake"] = true
		}
	case "socks", "socks5", "socks5h", "http", "https":
		if strings.HasPrefix(u.Scheme, "socks") {
			result["type"] = "socks"
			result["version"] = "5"
		} else {
			result["type"] = "http"
			if u.Scheme == "https" {
				result["tls"] = tls()
			}
		}
		if username != "" {
			result["username"] = username
			result["password"] = password
		}
	default:
		return nil, errors.New("unsupported_protocol")
	}
	if ech := get("ech"); ech != "" && ech != "0" && ech != "false" {
		t, ok := result["tls"].(Object)
		if !ok || q.Get("security") == "reality" {
			return nil, OptionProblem("unsupported_ech", "ech")
		}
		config, err := echConfig(ech)
		if err != nil {
			return nil, err
		}
		t["ech"] = Object{"enabled": true, "config": []string{config}}
	}
	rejected = nil
	for key, values := range q {
		if !used[key] && values[0] != "" {
			rejected = append(rejected, key)
		}
	}
	if len(rejected) > 0 {
		sort.Strings(rejected)
		return nil, OptionProblem("unsupported_node_option", rejected...)
	}
	return result, nil
}

// Preserve the exported ECHConfigList exactly in the native PEM envelope.
// Never retry without ECH or relax certificate validation on malformed input.
func echConfig(value string) (string, error) {
	if len(value) > 16384 {
		return "", OptionProblem("invalid_ech", "ech")
	}
	data, err := decode64(value)
	if err != nil || len(data) < 6 || int(binary.BigEndian.Uint16(data[:2])) != len(data)-2 {
		return "", OptionProblem("invalid_ech", "ech")
	}
	validPublicName := false
	knownConfig := false
	for rest := data[2:]; len(rest) > 0; {
		if len(rest) < 4 {
			return "", OptionProblem("invalid_ech", "ech")
		}
		n := int(binary.BigEndian.Uint16(rest[2:4]))
		if n == 0 || len(rest) < 4+n {
			return "", OptionProblem("invalid_ech", "ech")
		}
		body := rest[4 : 4+n]
		version := binary.BigEndian.Uint16(rest[:2])
		rest = rest[4+n:]
		if version != 0xfe0d {
			continue
		}
		knownConfig = true
		// config_id, kem_id, public_key vector, cipher suites vector,
		// maximum_name_length, public_name and extensions.
		if len(body) < 5 {
			return "", OptionProblem("invalid_ech", "ech")
		}
		keyLen := int(binary.BigEndian.Uint16(body[3:5]))
		if keyLen == 0 || len(body) < 5+keyLen+2 {
			return "", OptionProblem("invalid_ech", "ech")
		}
		body = body[5+keyLen:]
		suites := int(binary.BigEndian.Uint16(body[:2]))
		if suites == 0 || suites%4 != 0 || len(body) < 2+suites+2 {
			return "", OptionProblem("invalid_ech", "ech")
		}
		body = body[2+suites:]
		nameLen := int(body[1])
		if len(body) < 2+nameLen+2 {
			return "", OptionProblem("invalid_ech", "ech")
		}
		name := string(body[2 : 2+nameLen])
		body = body[2+nameLen:]
		if int(binary.BigEndian.Uint16(body[:2])) != len(body)-2 {
			return "", OptionProblem("invalid_ech", "ech")
		}
		if validECHPublicName(name) {
			validPublicName = true
		}
	}
	if !knownConfig {
		return "", OptionProblem("unsupported_ech", "ech")
	}
	if !validPublicName {
		return "", OptionProblem("invalid_ech_public_name", "ech")
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "ECH CONFIGS", Bytes: data})), nil
}

func validECHPublicName(name string) bool {
	if len(name) == 0 || len(name) > 253 || net.ParseIP(name) != nil {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func nodeTransport(out Object, q url.Values, consumed ...map[string]bool) error {
	used := map[string]bool{}
	if len(consumed) > 0 {
		used = consumed[0]
	}
	mark := func(keys ...string) {
		for _, key := range keys {
			used[key] = true
		}
	}
	mark("type", "net", "network")
	kind := q.Get("type")
	if kind == "" {
		kind = q.Get("net")
	}
	if kind == "" {
		kind = q.Get("network")
	}
	transport := Object{"type": kind}
	switch kind {
	case "", "tcp", "raw":
	case "ws":
		mark("path", "host")
		transport["path"] = q.Get("path")
		if host := q.Get("host"); host != "" {
			transport["headers"] = Object{"Host": host}
		}
	case "grpc":
		mark("serviceName", "service_name", "path", "mode", "authority")
		service := q.Get("serviceName")
		if service == "" {
			service = q.Get("service_name")
		}
		if service == "" {
			service = q.Get("path")
		}
		transport["service_name"] = service
		if q.Get("mode") != "" && q.Get("mode") != "gun" {
			return errors.New("unsupported_grpc_mode")
		}
		if q.Get("authority") != "" {
			return errors.New("unsupported_grpc_authority")
		}
	case "httpupgrade":
		mark("path", "host")
		transport["type"] = "httpupgrade"
		transport["path"] = q.Get("path")
		transport["host"] = q.Get("host")
	default:
		return errors.New("unsupported_transport")
	}
	for _, key := range []string{"host", "path", "serviceName", "service_name", "authority", "mode"} {
		if q.Get(key) != "" && !used[key] {
			return errors.New("unsupported_transport_option")
		}
	}
	if kind != "" && kind != "tcp" && kind != "raw" {
		out["transport"] = transport
	}
	return nil
}
func vmess(link, tag string, opt NodeOptions) (Object, error) {
	raw := strings.TrimPrefix(link, "vmess://")
	data, err := decode64(raw)
	if err != nil {
		return nil, err
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(data, &v) != nil {
		return nil, errors.New("invalid_node")
	}
	allowed := strings.Fields("v ps add port id aid scy net type host path tls sni alpn fp allowInsecure serviceName mode class remark headerType verify_cert")
	known := map[string]bool{}
	for _, k := range allowed {
		known[k] = true
	}
	var rejected []string
	for k := range v {
		if !known[k] {
			rejected = append(rejected, k)
		}
	}
	if len(rejected) > 0 {
		return nil, OptionProblem("unsupported_vmess_option", rejected...)
	}
	text := func(k string) string {
		var s string
		if json.Unmarshal(v[k], &s) == nil {
			return s
		}
		return string(v[k])
	}
	if aid := text("aid"); aid != "" && aid != "0" {
		return nil, errors.New("unsupported_vmess_alterid")
	}
	if version := text("v"); version != "" && version != "2" {
		return nil, errors.New("unsupported_vmess_option")
	}
	port, err := strconv.Atoi(text("port"))
	if err != nil || port < 1 || port > 65535 || text("add") == "" {
		return nil, errors.New("invalid_node")
	}
	q := url.Values{}
	q.Set("type", text("net"))
	for _, k := range []string{"host", "path", "serviceName", "mode"} {
		if v := text(k); v != "" {
			q.Set(k, v)
		}
	}
	out := Object{"type": "vmess", "tag": tag, "server": text("add"), "server_port": port, "uuid": text("id"), "security": "auto", "alter_id": 0}
	if v := text("scy"); v != "" {
		out["security"] = v
	}
	if text("headerType") != "" && text("type") != "" && text("headerType") != text("type") {
		return nil, OptionProblem("conflicting_node_options", "headerType", "type")
	}
	if (text("type") != "" && text("type") != "none") || (text("headerType") != "" && text("headerType") != "none") {
		return nil, errors.New("unsupported_vmess_header")
	}
	// class/remark are exporter metadata. verify_cert is a certificate choice,
	// not metadata: convert its inverse and reject contradictory aliases.
	var explicitInsecure *bool
	for key, inverse := range map[string]bool{"allowInsecure": false, "verify_cert": true} {
		if _, exists := v[key]; !exists {
			continue
		}
		if text(key) == "" {
			continue
		}
		value, e := strconv.ParseBool(text(key))
		if e != nil || (text(key) != "true" && text(key) != "false" && text(key) != "1" && text(key) != "0") {
			return nil, OptionProblem("invalid_tls_option", key)
		}
		if inverse {
			value = !value
		}
		if explicitInsecure != nil && *explicitInsecure != value {
			return nil, OptionProblem("conflicting_node_options", "allowInsecure", "verify_cert")
		}
		explicitInsecure = &value
	}
	if text("tls") == "tls" {
		t := Object{"enabled": true, "insecure": opt.AllowInsecure}
		if text("sni") != "" {
			t["server_name"] = text("sni")
		}
		if explicitInsecure != nil {
			t["insecure"] = *explicitInsecure
		}
		if text("alpn") != "" {
			t["alpn"] = strings.Split(text("alpn"), ",")
		}
		fingerprint := text("fp")
		if fingerprint == "" {
			fingerprint = opt.UTLS
		}
		fingerprint = strings.TrimSuffix(fingerprint, "_auto")
		if fingerprint != "" {
			t["utls"] = Object{"enabled": true, "fingerprint": fingerprint}
		}
		out["tls"] = t
	} else if text("tls") != "" && text("tls") != "none" {
		return nil, errors.New("unsupported_tls")
	}
	if err := nodeTransport(out, q); err != nil {
		return nil, err
	}
	return out, nil
}

// SocksAddress is deliberately numeric loopback; DNS belongs to the selected core.
func SocksAddress(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }
