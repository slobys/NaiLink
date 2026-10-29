// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/daeuniverse/dae-wing/routercore"
	daeCommon "github.com/daeuniverse/dae/common"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/pkg/config_parser"
	"github.com/daeuniverse/dae/pkg/geodata"
	"github.com/sirupsen/logrus"
)

type sbObject = routercore.Object
type sbPorts struct {
	Mixed, API, DNS int
	Secret          string
}
type sbConverter struct {
	conf     *daeConfig.Config
	dirs     []string
	warnings []string
	rulesets []sbObject
	geo      map[string]string
}

type sbNodeProblems []string

func (p sbNodeProblems) Error() string { return strings.Join(p, "；") }

func singBoxNodeOutbounds(conf *daeConfig.Config) ([]sbObject, map[string]bool, []string, sbNodeProblems) {
	return nativeNodeOutbounds(conf, "sing-box")
}
func nativeNodeOutbounds(conf *daeConfig.Config, engine string) ([]sbObject, map[string]bool, []string, sbNodeProblems) {
	outbounds := []sbObject{{"type": "direct", "tag": "direct"}}
	names := map[string]bool{}
	var warnings []string
	var problems sbNodeProblems
	for i, entry := range conf.Node {
		name, link := daeCommon.GetTagFromLinkLikePlaintext(string(entry))
		label := fmt.Sprintf("节点 %d", i+1)
		if name != "" && !strings.Contains(name, "://") {
			clean := strings.Map(func(r rune) rune {
				if r < 32 || r == 127 {
					return -1
				}
				return r
			}, name)
			chars := []rune(clean)
			if len(chars) > 80 {
				clean = string(chars[:80]) + "…"
			}
			label += "「" + clean + "」"
		}
		if name == "" {
			problems = append(problems, label+"："+routercore.ProblemText("invalid_node_name"))
			continue
		}
		fingerprint := ""
		if conf.Global.TlsImplementation == "utls" {
			fingerprint = conf.Global.UtlsImitate
		}
		out, err := routerParseCoreNode(engine, link, "node:"+name, routercore.NodeOptions{AllowInsecure: conf.Global.AllowInsecure, UTLS: fingerprint,
			Warn: func(message string) { warnings = append(warnings, label+"："+message) },
		})
		if err == nil && engine == "mihomo" {
			for _, node := range routercore.ExpandNode(out) {
				if _, err = routercore.MihomoNode(node); err != nil {
					break
				}
			}
		}
		if err != nil {
			problems = append(problems, label+"："+routercore.DescribeError(err))
			continue
		}
		names[name] = true
		outbounds = append(outbounds, routercore.ExpandNode(out)...)
	}
	return outbounds, names, warnings, problems
}

func coreOutbound(name string) (string, error) {
	name = strings.TrimPrefix(name, "must_")
	if name == "direct" {
		return "direct", nil
	}
	if name == "block" || name == "rules" {
		return "", errors.New("unsupported_rule_target")
	}
	return "group:" + name, nil
}
func singBoxConfig(conf *daeConfig.Config, dirs []string, ports sbPorts, transparent bool) (sbObject, []string, error) {
	return coreCanonicalConfig(conf, dirs, ports, transparent, "sing-box")
}
func coreCanonicalConfig(conf *daeConfig.Config, dirs []string, ports sbPorts, transparent bool, engine string) (output sbObject, reports []string, failure error) {
	c := &sbConverter{conf: conf, dirs: dirs, geo: map[string]string{}}
	defer func() {
		if reports == nil {
			reports = c.warnings
		}
	}()
	outbounds, nodeNames, warnings, nodeProblems := nativeNodeOutbounds(conf, engine)
	c.warnings = warnings
	if len(nodeProblems) > 0 {
		return nil, c.warnings, nodeProblems
	}
	if len(conf.Subscription) > 0 {
		return nil, nil, errors.New("unresolved_subscription")
	}
	if conf.Global.SoMarkFromDae != 0 || conf.Global.TlsFragment || conf.Global.Mptcp || conf.Global.PprofPort != 0 {
		return nil, nil, errors.New("unsupported_global_option")
	}
	if implementation := conf.Global.TlsImplementation; implementation != "" && implementation != "tls" && implementation != "utls" {
		return nil, nil, errors.New("unsupported_global_option")
	}
	if mode := conf.Global.DialMode; mode != "" && mode != "domain" && mode != "ip" {
		return nil, nil, errors.New("unsupported_dial_mode")
	}
	domainDial := conf.Global.DialMode == "" || conf.Global.DialMode == "domain"
	if len(conf.Dns.Routing.Response.Rules) > 0 || len(conf.Dns.FixedDomainTtl) > 0 || conf.Dns.MaxCacheSize != 0 {
		return nil, nil, errors.New("unsupported_dns_option")
	}
	for _, group := range conf.Group {
		for _, annotations := range group.FilterAnnotation {
			if len(annotations) > 0 {
				return nil, nil, errors.New("unsupported_group_filter_annotation")
			}
		}
		var members []string
		for _, clause := range group.Filter {
			if len(clause) != 1 || clause[0].Name != "name" || clause[0].Not {
				return nil, nil, errors.New("unsupported_group_filter")
			}
			for _, p := range clause[0].Params {
				if p.Key != "" || !nodeNames[p.Val] {
					return nil, nil, errors.New("invalid_group_node")
				}
				members = append(members, "node:"+p.Val)
			}
		}
		sort.Strings(members)
		members = daeCommon.Deduplicate(members)
		if len(members) == 0 {
			return nil, nil, errors.New("empty_group")
		}
		policies, err := daeConfig.ParseFunctionListOrString(group.Policy)
		if err != nil || len(policies) != 1 {
			return nil, nil, errors.New("unsupported_group_policy")
		}
		policy := policies[0]
		if len(policy.Params) > 0 && !(policy.Name == "fixed" && len(policy.Params) == 1 && policy.Params[0].Key == "" && policy.Params[0].Val == "0") {
			return nil, nil, errors.New("unsupported_group_policy_parameter")
		}
		out := sbObject{"tag": "group:" + group.Name, "outbounds": members}
		switch policy.Name {
		case "fixed":
			if len(members) != 1 {
				return nil, nil, errors.New("invalid_fixed_group")
			}
			out["type"] = "selector"
			out["default"] = members[0]
		case "min", "min_avg10", "min_moving_avg":
			out["type"] = "urltest"
			target := conf.Global.TcpCheckUrl
			if len(group.TcpCheckUrl) > 0 {
				target = group.TcpCheckUrl
			}
			if len(target) == 0 {
				return nil, nil, errors.New("missing_check_url")
			}
			out["url"] = target[0]
			if len(target) > 1 {
				c.warnings = append(c.warnings, "群组「"+group.Name+"」的自动测试使用检测 URL，由目标内核/节点解析；dae 检测 URL 的固定 IP 覆盖不用于自动选择。手动节点测试仍使用固定 IP。")
			}
			interval := conf.Global.CheckInterval
			if group.CheckInterval > 0 {
				interval = group.CheckInterval
			}
			if interval <= 0 {
				return nil, nil, errors.New("invalid_check_interval")
			}
			out["interval"] = interval.String()
			tolerance := conf.Global.CheckTolerance
			if group.CheckTolerance > 0 {
				tolerance = group.CheckTolerance
			}
			out["tolerance"] = tolerance.Milliseconds()
			out["interrupt_exist_connections"] = false
			if policy.Name != "min" {
				c.warnings = append(c.warnings, "群组「"+group.Name+"」的平滑/平均延迟策略映射为 sing-box URL 自动测试；切回 dae 恢复原策略。")
			}
		default:
			return nil, nil, errors.New("unsupported_group_policy")
		}
		outbounds = append(outbounds, out)
	}
	routeRules := []sbObject{{"action": "sniff"}, {"protocol": "dns", "action": "hijack-dns"}}
	for _, rule := range conf.Routing.Rules {
		params := rule.Outbound.Params
		if len(params) == 1 && params[0].Key == "" && params[0].Val == "must" {
			params = nil
		}
		if len(params) > 0 {
			return nil, nil, errors.New("unsupported_rule_target")
		}
		match, err := c.rule(rule.AndFunctions, false)
		if err != nil {
			return nil, nil, err
		}
		if strings.TrimPrefix(rule.Outbound.Name, "must_") == "block" {
			match["action"] = "reject"
		} else {
			target, err := coreOutbound(rule.Outbound.Name)
			if err != nil || len(params) > 0 {
				return nil, nil, errors.New("unsupported_rule_target")
			}
			match["action"] = "route"
			match["outbound"] = target
			if domainDial && target != "direct" {
				match["override_domain"] = true
			}
		}
		routeRules = append(routeRules, match)
	}
	fallback, err := daeConfig.ParseFunctionOrString(conf.Routing.Fallback)
	if err != nil || len(fallback.Params) > 0 {
		return nil, nil, errors.New("unsupported_fallback")
	}
	final, err := coreOutbound(fallback.Name)
	if err != nil {
		if fallback.Name != "block" {
			return nil, nil, err
		}
		routeRules = append(routeRules, sbObject{"action": "reject"})
		final = "direct"
	}
	if domainDial && final != "direct" {
		routeRules = append(routeRules, sbObject{"action": "route", "outbound": final, "override_domain": true})
	}
	dns, bootstrap, err := c.dns()
	if err != nil {
		return nil, nil, err
	}
	route := sbObject{"rules": routeRules, "final": final, "auto_detect_interface": true, "default_domain_resolver": sbObject{"server": bootstrap}}
	if len(conf.Global.WanInterface) == 1 && conf.Global.WanInterface[0] != "auto" {
		delete(route, "auto_detect_interface")
		route["default_interface"] = conf.Global.WanInterface[0]
	} else if len(conf.Global.WanInterface) > 1 {
		return nil, nil, errors.New("unsupported_multi_wan")
	}
	if len(c.rulesets) > 0 {
		route["rule_set"] = c.rulesets
	}
	inbounds := []sbObject{
		{"type": "mixed", "tag": "management-proxy", "listen": "127.0.0.1", "listen_port": ports.Mixed, "users": []sbObject{{"username": "nailink", "password": ports.Secret}}},
		{"type": "direct", "tag": "dns-in", "listen": "127.0.0.1", "listen_port": ports.DNS},
	}
	if transparent && conf != EmptyConfig {
		if len(conf.Global.LanInterface) == 0 {
			return nil, nil, errors.New("missing_lan_interface")
		}
		inbounds = append(inbounds, sbObject{"type": "tun", "tag": "lan-in", "interface_name": routercore.TunName, "address": []string{"198.19.255.1/30", "fdfe:daed::1/126"}, "mtu": 1500, "auto_route": true, "auto_redirect": true, "strict_route": true, "stack": "system", "iproute2_table_index": 20340, "iproute2_rule_index": 20340, "auto_redirect_iproute2_fallback_rule_index": 32768, "auto_redirect_input_mark": "0x4e4101", "auto_redirect_output_mark": "0x4e4102", "auto_redirect_reset_mark": "0x4e4103", "include_interface": conf.Global.LanInterface})
	}
	level := conf.Global.LogLevel
	if level == "warning" {
		level = "warn"
	}
	if level == "" {
		level = "info"
	}
	result := sbObject{"log": sbObject{"level": level, "timestamp": false}, "dns": dns, "inbounds": inbounds, "outbounds": outbounds, "route": route, "experimental": sbObject{"clash_api": sbObject{"external_controller": routercore.SocksAddress(ports.API), "secret": ports.Secret, "access_control_allow_private_network": false, "access_control_allow_origin": []string{}}}}
	if conf.Dns.OptimisticCache {
		c.warnings = append(c.warnings, "DNS 使用 sing-box 自身缓存，不迁移 dae 的乐观缓存和已有 DNS 缓存。")
	}
	return result, c.warnings, nil
}
func (c *sbConverter) asset(name string) (string, error) {
	dirs := append([]string{}, c.dirs...)
	dirs = append(dirs, "/usr/share/daed", "/usr/share/v2ray")
	for _, dir := range dirs {
		path := filepath.Join(dir, name)
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
			return path, nil
		}
	}
	return "", errors.New("geodata_missing")
}
func (c *sbConverter) georule(kind, code string, external ...string) (string, error) {
	filename := kind + ".dat"
	if len(external) > 0 {
		filename = external[0]
		if len(filename) > 128 || filepath.Base(filename) != filename || strings.HasPrefix(filename, ".") || !strings.HasSuffix(filename, ".dat") || strings.ContainsAny(filename, "\\\x00\r\n") {
			return "", errors.New("invalid_external_geodata")
		}
	}
	key := kind + ":" + filename + ":" + strings.ToLower(code)
	if tag := c.geo[key]; tag != "" {
		return tag, nil
	}
	if code == "" || strings.ContainsAny(code, "/\\\x00") {
		return "", errors.New("invalid_geodata_reference")
	}
	file, err := c.asset(filename)
	if err != nil {
		return "", err
	}
	log := logrus.New()
	log.SetOutput(io.Discard)
	rule := sbObject{}
	if kind == "geoip" {
		entry, err := geodata.UnmarshalGeoIp(log, file, code)
		if err != nil || entry.InverseMatch {
			return "", errors.New("unsupported_geoip")
		}
		var cidrs []string
		for _, cidr := range entry.Cidr {
			addr, ok := netip.AddrFromSlice(cidr.Ip)
			if !ok || int(cidr.Prefix) > addr.BitLen() {
				return "", errors.New("invalid_geodata")
			}
			cidrs = append(cidrs, netip.PrefixFrom(addr, int(cidr.Prefix)).Masked().String())
		}
		if len(cidrs) == 0 {
			return "", errors.New("empty_geodata")
		}
		rule["ip_cidr"] = cidrs
	} else {
		parts := strings.Split(code, "@")
		entry, err := geodata.UnmarshalGeoSite(log, file, parts[0])
		if err != nil {
			return "", errors.New("unsupported_geosite")
		}
		values := map[string][]string{}
		for _, domain := range entry.Domain {
			include := true
			for _, attribute := range parts[1:] {
				found := false
				for _, a := range domain.Attribute {
					if strings.EqualFold(a.Key, attribute) {
						found = true
					}
				}
				include = include && found
			}
			if !include {
				continue
			}
			key := ""
			switch domain.Type {
			case geodata.Domain_Plain:
				key = "domain_keyword"
			case geodata.Domain_Regex:
				key = "domain_regex"
			case geodata.Domain_RootDomain:
				key = "domain_suffix"
			case geodata.Domain_Full:
				key = "domain"
			default:
				return "", errors.New("invalid_geodata")
			}
			values[key] = append(values[key], domain.Value)
		}
		for key, value := range values {
			rule[key] = value
		}
	}
	if len(rule) == 0 {
		return "", errors.New("empty_geodata")
	}
	tag := "geo:" + key
	c.geo[key] = tag
	c.rulesets = append(c.rulesets, sbObject{"type": "inline", "tag": tag, "rules": []sbObject{rule}})
	return tag, nil
}
func (c *sbConverter) function(f *config_parser.Function, dns bool) (sbObject, error) {
	var clauses []sbObject
	if len(f.Params) == 0 {
		return nil, errors.New("empty_rule")
	}
	for _, p := range f.Params {
		if len(p.AndFunctions) > 0 {
			return nil, errors.New("unsupported_rule_parameter")
		}
		match := sbObject{}
		value := p.Val
		switch f.Name {
		case "domain", "qname":
			if dns && f.Name != "qname" || !dns && f.Name != "domain" {
				return nil, errors.New("unsupported_rule")
			}
			kind := p.Key
			if kind == "" {
				kind = "suffix"
			}
			if strings.HasPrefix(value, "geosite:") {
				kind = "geosite"
				value = strings.TrimPrefix(value, "geosite:")
			}
			switch kind {
			case "ext":
				parts := strings.SplitN(value, ":", 2)
				if len(parts) != 2 {
					return nil, errors.New("invalid_external_geodata")
				}
				tag, err := c.georule("geosite", parts[1], parts[0])
				if err != nil {
					return nil, err
				}
				match["rule_set"] = []string{tag}
			case "geosite":
				tag, err := c.georule("geosite", value)
				if err != nil {
					return nil, err
				}
				match["rule_set"] = []string{tag}
			case "suffix", "full", "keyword", "regex":
				key := map[string]string{"suffix": "domain_suffix", "full": "domain", "keyword": "domain_keyword", "regex": "domain_regex"}[kind]
				match[key] = []string{value}
			default:
				return nil, errors.New("unsupported_domain_match")
			}
		case "dip", "sip":
			if dns {
				return nil, errors.New("unsupported_dns_match")
			}
			source := f.Name == "sip"
			if strings.HasPrefix(value, "geoip:") || p.Key == "geoip" || p.Key == "ext" {
				code := strings.TrimPrefix(value, "geoip:")
				if source {
					return nil, errors.New("unsupported_source_geoip")
				}
				var external []string
				if p.Key == "ext" {
					parts := strings.SplitN(value, ":", 2)
					if len(parts) != 2 {
						return nil, errors.New("invalid_external_geodata")
					}
					external = []string{parts[0]}
					code = parts[1]
				}
				tag, err := c.georule("geoip", code, external...)
				if err != nil {
					return nil, err
				}
				match["rule_set"] = []string{tag}
			} else {
				key := "ip_cidr"
				if source {
					key = "source_ip_cidr"
				}
				if !strings.Contains(value, "/") {
					addr, err := netip.ParseAddr(value)
					if err != nil {
						return nil, errors.New("invalid_ip_rule")
					}
					value = netip.PrefixFrom(addr, addr.BitLen()).String()
				}
				if _, err := netip.ParsePrefix(value); err != nil {
					return nil, errors.New("invalid_ip_rule")
				}
				match[key] = []string{value}
			}
		case "dport", "sport":
			if dns {
				return nil, errors.New("unsupported_dns_match")
			}
			key := "port"
			if f.Name == "sport" {
				key = "source_port"
			}
			if strings.Contains(value, "-") {
				match[key+"_range"] = []string{strings.ReplaceAll(value, "-", ":")}
			} else {
				n, err := strconv.Atoi(value)
				if err != nil || n < 1 || n > 65535 {
					return nil, errors.New("invalid_port_rule")
				}
				match[key] = []int{n}
			}
		case "l4proto":
			if dns || value != "tcp" && value != "udp" {
				return nil, errors.New("unsupported_protocol_match")
			}
			match["network"] = []string{value}
		case "ipversion":
			if dns || value != "4" && value != "6" {
				return nil, errors.New("unsupported_ip_version")
			}
			n, _ := strconv.Atoi(value)
			match["ip_version"] = n
		case "pname":
			if dns {
				return nil, errors.New("unsupported_process_rule")
			}
			match["process_name"] = []string{value}
		case "qtype":
			if !dns {
				return nil, errors.New("unsupported_rule")
			}
			match["query_type"] = []string{value}
		default:
			return nil, errors.New("unsupported_rule_" + f.Name)
		}
		clauses = append(clauses, match)
	}
	var result sbObject
	if len(clauses) == 1 {
		result = clauses[0]
	} else {
		result = sbObject{"type": "logical", "mode": "or", "rules": clauses}
	}
	if f.Not {
		result["invert"] = true
	}
	return result, nil
}
func (c *sbConverter) rule(functions []*config_parser.Function, dns bool) (sbObject, error) {
	var matches []sbObject
	for _, f := range functions {
		match, err := c.function(f, dns)
		if err != nil {
			return nil, err
		}
		matches = append(matches, match)
	}
	if len(matches) == 0 {
		return nil, errors.New("empty_rule")
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return sbObject{"type": "logical", "mode": "and", "rules": matches}, nil
}
func (c *sbConverter) dns() (sbObject, string, error) {
	var servers []sbObject
	names := map[string]bool{}
	bootstrap := ""
	for _, raw := range c.conf.Dns.Upstream {
		name, link := daeCommon.GetTagFromLinkLikePlaintext(string(raw))
		u, err := url.Parse(link)
		if err != nil || name == "" || names[name] {
			return nil, "", errors.New("unsupported_dns_upstream")
		}
		names[name] = true
		kind := u.Scheme
		if kind == "tcp+udp" {
			kind = "tcp"
			c.warnings = append(c.warnings, "DNS 上游「"+name+"」的 tcp+udp 映射为 TCP。")
		}
		switch kind {
		case "udp", "tcp", "tls", "https":
		default:
			return nil, "", errors.New("unsupported_dns_transport")
		}
		host := u.Hostname()
		if net.ParseIP(host) == nil {
			return nil, "", errors.New("dns_upstream_requires_ip")
		}
		port := 53
		if kind == "tls" {
			port = 853
		}
		if kind == "https" {
			port = 443
		}
		if u.Port() != "" {
			port, err = strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				return nil, "", errors.New("invalid_dns_port")
			}
		}
		server := sbObject{"type": kind, "tag": "dns:" + name, "server": host, "server_port": port}
		if bootstrap == "" {
			bootstrap = "dns:" + name
		} else {
			out, err := daeConfig.ParseFunctionOrString(c.conf.Routing.Fallback)
			if err != nil {
				return nil, "", err
			}
			target, err := coreOutbound(out.Name)
			if err != nil {
				return nil, "", errors.New("unsupported_dns_detour")
			}
			if target != "direct" {
				server["detour"] = target
			}
		}
		if kind == "https" {
			path := u.EscapedPath()
			if path != "" {
				server["path"] = path
			}
		}
		if u.RawQuery != "" || u.User != nil || u.Fragment != "" {
			return nil, "", errors.New("unsupported_dns_parameter")
		}
		servers = append(servers, server)
		if name == "nailink_node_dns" && c.conf.Global.NailinkNodeDNS != "" {
			delete(server, "detour")
		}
	}
	if len(servers) == 0 {
		servers = []sbObject{{"type": "local", "tag": "dns:local"}}
		bootstrap = "dns:local"
	}
	final := servers[len(servers)-1]["tag"].(string)
	if f, err := daeConfig.ParseFunctionOrString(c.conf.Dns.Routing.Request.Fallback); err == nil && f.Name != "" && c.conf != EmptyConfig {
		if len(f.Params) > 0 || !names[f.Name] {
			return nil, "", errors.New("unsupported_dns_fallback")
		}
		final = "dns:" + f.Name
	}
	var rules []sbObject
	for _, r := range c.conf.Dns.Routing.Request.Rules {
		match, err := c.rule(r.AndFunctions, true)
		if err != nil {
			return nil, "", err
		}
		if len(r.Outbound.Params) > 0 {
			return nil, "", errors.New("unsupported_dns_target")
		}
		if r.Outbound.Name == "reject" {
			match["action"] = "reject"
		} else if names[r.Outbound.Name] {
			match["action"] = "route"
			match["server"] = "dns:" + r.Outbound.Name
		} else {
			return nil, "", errors.New("unsupported_dns_target")
		}
		rules = append(rules, match)
	}
	if prefer := c.conf.Dns.IpVersionPrefer; prefer != 0 {
		if prefer != 4 && prefer != 6 {
			return nil, "", errors.New("invalid_dns_preference")
		}
		blocked := "AAAA"
		if prefer == 6 {
			blocked = "A"
		}
		rules = append([]sbObject{{"query_type": []string{blocked}, "action": "predefined", "rcode": "NOERROR"}}, rules...)
	}
	c.warnings = append(c.warnings, "sing-box 的节点域名引导解析使用首个直连 DNS 上游；其余 DNS 上游经默认群组。")
	if c.conf.Global.NailinkNodeDNS != "" {
		if host, port, err := net.SplitHostPort(c.conf.Global.BootstrapResolver); err == nil && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback() {
			p, _ := strconv.Atoi(port)
			bootstrap = "dns:nailink-node-server"
			servers = append(servers, sbObject{"tag": bootstrap, "type": "udp", "server": host, "server_port": p})
		}
		c.warnings = append(c.warnings, "仅节点服务器域名使用订阅专用 DNS，网站 DNS 与分流规则保持不变。")
	}
	return sbObject{"servers": servers, "rules": rules, "final": final}, bootstrap, nil
}
