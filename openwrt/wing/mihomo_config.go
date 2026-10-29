// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
)

var routerMihomoBinary = routercore.MihomoBinaryPath

func mhRuleTarget(match, target string) string {
	parts := strings.SplitN(match, ",", 3)
	if len(parts) < 2 {
		return "MATCH," + target
	}
	switch parts[0] {
	case "AND", "OR", "NOT", "DOMAIN-REGEX":
		return match + "," + target
	}
	line := parts[0] + "," + parts[1] + "," + target
	if len(parts) > 2 {
		line += "," + parts[2]
	}
	return line
}

func mhObjects(v any) []sbObject {
	if a, ok := v.([]sbObject); ok {
		return a
	}
	var r []sbObject
	if a, ok := v.([]any); ok {
		for _, x := range a {
			if o, ok := x.(map[string]any); ok {
				r = append(r, o)
			}
		}
	}
	return r
}
func mhStrings(v any) []string {
	if a, ok := v.([]string); ok {
		return a
	}
	var r []string
	if a, ok := v.([]any); ok {
		for _, x := range a {
			r = append(r, fmt.Sprint(x))
		}
	}
	if a, ok := v.([]int); ok {
		for _, x := range a {
			r = append(r, strconv.Itoa(x))
		}
	}
	return r
}
func mhString(v any) string { s, _ := v.(string); return s }
func mhNumber(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}

type mhRuleCompiler struct {
	providers map[string]any
	matches   map[string]string
}

func mhJoin(mode string, terms []string) string {
	if len(terms) == 0 {
		return ""
	}
	if len(terms) == 1 {
		return terms[0]
	}
	return mode + ",((" + strings.Join(terms, "),(") + "))"
}
func (c *mhRuleCompiler) provider(tag string, rules []sbObject) (string, error) {
	if c.matches == nil {
		c.matches = map[string]string{}
	}
	buckets := map[string][]string{}
	for _, rule := range rules {
		var keys []string
		for key := range rule {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			typ := map[string]string{"domain": "DOMAIN", "domain_suffix": "DOMAIN-SUFFIX", "domain_keyword": "DOMAIN-KEYWORD", "domain_regex": "DOMAIN-REGEX", "ip_cidr": "IP-CIDR"}[key]
			if typ == "" {
				return "", errors.New("mihomo_rule_unsupported")
			}
			for _, v := range mhStrings(rule[key]) {
				if strings.ContainsAny(v, "\r\n\x00") || typ != "DOMAIN-REGEX" && strings.Contains(v, ",") {
					return "", errors.New("mihomo_rule_unsupported")
				}
				switch key {
				case "domain":
					buckets["domain"] = append(buckets["domain"], v)
				case "domain_suffix":
					buckets["domain"] = append(buckets["domain"], "+."+strings.TrimPrefix(v, "."))
				case "ip_cidr":
					buckets["ipcidr"] = append(buckets["ipcidr"], v)
				default:
					buckets["classical"] = append(buckets["classical"], typ+","+v)
				}
			}
		}
	}
	var terms []string
	for _, kind := range []string{"domain", "ipcidr", "classical"} {
		payload := buckets[kind]
		if len(payload) == 0 {
			continue
		}
		name := routercore.MihomoName(tag + ":" + kind)
		c.providers[name] = sbObject{"type": "inline", "behavior": kind, "payload": payload}
		terms = append(terms, "RULE-SET,"+name+",no-resolve")
	}
	if len(terms) == 0 {
		return "", errors.New("empty_geodata")
	}
	value := mhJoin("OR", terms)
	c.matches[tag] = value
	return value, nil
}
func (c *mhRuleCompiler) match(rule sbObject) (string, error) {
	var terms []string
	if rule["type"] == "logical" {
		for _, child := range mhObjects(rule["rules"]) {
			v, e := c.match(child)
			if e != nil {
				return "", e
			}
			terms = append(terms, v)
		}
		mode := "AND"
		if rule["mode"] == "or" {
			mode = "OR"
		}
		terms = []string{mhJoin(mode, terms)}
	} else {
		var keys []string
		for k := range rule {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			switch key {
			case "action", "outbound", "override_domain", "invert", "type":
				continue
			}
			if key == "rule_set" {
				var values []string
				for _, tag := range mhStrings(rule[key]) {
					value := c.matches[tag]
					if value == "" {
						return "", errors.New("mihomo_rule_unsupported")
					}
					values = append(values, value)
				}
				terms = append(terms, mhJoin("OR", values))
				continue
			}
			if key == "ip_version" {
				cidr := "0.0.0.0/0"
				if mhNumber(rule[key]) == 6 {
					cidr = "::/0"
				}
				terms = append(terms, "IP-CIDR,"+cidr+",no-resolve")
				continue
			}
			typ := map[string]string{"domain": "DOMAIN", "domain_suffix": "DOMAIN-SUFFIX", "domain_keyword": "DOMAIN-KEYWORD", "domain_regex": "DOMAIN-REGEX", "ip_cidr": "IP-CIDR", "source_ip_cidr": "SRC-IP-CIDR", "port": "DST-PORT", "port_range": "DST-PORT", "source_port": "SRC-PORT", "source_port_range": "SRC-PORT", "network": "NETWORK", "process_name": "PROCESS-NAME", "auth_user": "IN-USER"}[key]
			if typ == "" {
				return "", errors.New("mihomo_rule_unsupported")
			}
			if key == "domain_regex" {
				name, e := c.provider("regex:"+strings.Join(mhStrings(rule[key]), "\x00"), []sbObject{{key: rule[key]}})
				if e != nil {
					return "", e
				}
				terms = append(terms, name)
				continue
			}
			var values []string
			for _, v := range mhStrings(rule[key]) {
				if strings.ContainsAny(v, ",\r\n\x00") {
					return "", errors.New("mihomo_rule_unsupported")
				}
				if strings.HasSuffix(key, "_range") {
					v = strings.ReplaceAll(v, ":", "-")
				}
				if typ == "NETWORK" {
					v = strings.ToUpper(v)
				}
				value := typ + "," + v
				if typ == "IP-CIDR" {
					value += ",no-resolve"
				}
				values = append(values, value)
			}
			terms = append(terms, mhJoin("OR", values))
		}
	}
	value := mhJoin("AND", terms)

	if rule["invert"] == true {
		value = "NOT,((" + value + "))"
	}
	return value, nil
}

// Convert the existing validated, database-independent canonical profile. The
// sing-box executable is not needed and no user record is migrated or rewritten.
func mihomoFromCanonical(profile sbObject, conf *daeConfig.Config, ports sbPorts) (sbObject, error) {
	sets := map[string][]sbObject{}
	for _, set := range mhObjects(profile["route"].(sbObject)["rule_set"]) {
		sets[mhString(set["tag"])] = mhObjects(set["rules"])
	}
	for _, rule := range mhObjects(profile["dns"].(sbObject)["rules"]) {
		if _, err := mhDNSCompile(rule, sets); err != nil {
			return nil, err
		}
	}
	compiler := &mhRuleCompiler{providers: map[string]any{}}
	route := profile["route"].(sbObject)
	for _, set := range mhObjects(route["rule_set"]) {
		if _, e := compiler.provider(mhString(set["tag"]), mhObjects(set["rules"])); e != nil {
			return nil, e
		}
	}
	var proxies, groups []sbObject
	var problems sbNodeProblems
	for _, node := range mhObjects(profile["outbounds"]) {
		tag := mhString(node["tag"])
		kind := mhString(node["type"])
		if kind == "selector" || kind == "urltest" {
			members := []string{}
			for _, n := range mhStrings(node["outbounds"]) {
				members = append(members, routercore.MihomoName(n))
			}
			g := sbObject{"name": routercore.MihomoName(tag), "type": "select", "proxies": members}
			if kind == "urltest" {
				g["type"] = "url-test"
				g["url"] = node["url"]
				g["interval"] = mhDurationSeconds(node["interval"], 300)
				g["tolerance"] = node["tolerance"]
				g["lazy"] = false
			}
			groups = append(groups, g)
			continue
		}
		out, e := routercore.MihomoNode(node)
		if e != nil {
			problems = append(problems, "节点「"+safeMihomoLabel(tag)+"」："+routercore.DescribeError(e))
			continue
		}
		if out != nil {
			proxies = append(proxies, out)
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	var rules []string
	forceProxy := ""
	authRules := 0
	for _, rule := range mhObjects(route["rules"]) {
		action := mhString(rule["action"])
		if action == "sniff" || action == "hijack-dns" {
			continue
		}
		if action != "route" && action != "reject" {
			return nil, errors.New("mihomo_rule_unsupported")
		}
		target := "REJECT"
		if action == "route" {
			target = routercore.MihomoName(mhString(rule["outbound"]))
		}
		if len(mhStrings(rule["auth_user"])) > 0 {
			authRules++
			forceProxy = target
		}
		if len(rule) <= 3 && rule["outbound"] != nil && rule["override_domain"] != nil {
			rules = append(rules, "MATCH,"+target)
			continue
		}
		match, e := compiler.match(rule)
		if e != nil {
			return nil, e
		}
		rules = append(rules, mhRuleTarget(match, target))
	}
	rules = append(rules, "MATCH,"+routercore.MihomoName(mhString(route["final"])))
	main := mhObjects(profile["inbounds"])[0]
	listener := sbObject{"name": "nailink-management", "type": "mixed", "listen": "127.0.0.1", "port": ports.Mixed, "udp": true, "users": main["users"]}
	// SOCKS UDP packets do not carry usernames. A single-node UDP test uses a
	// forced private listener, so it can never report success via DIRECT.
	if authRules == 1 {
		listener["proxy"] = forceProxy
	}
	level := mhString(profile["log"].(sbObject)["level"])
	if level == "warn" {
		level = "warning"
	}
	if level == "trace" {
		level = "debug"
	}
	result := sbObject{"mode": "rule", "routing-mark": 0x4e4102, "ipv6": true, "allow-lan": false, "bind-address": "127.0.0.1", "log-level": level, "external-controller": routercore.SocksAddress(ports.API), "secret": ports.Secret, "external-controller-cors": sbObject{"allow-origins": []string{}, "allow-private-network": false}, "profile": sbObject{"store-selected": false, "store-fake-ip": false}, "geodata-mode": false, "geo-auto-update": false, "find-process-mode": mhProcessMode(mhObjects(route["rules"])), "listeners": []sbObject{listener}, "proxies": proxies, "proxy-groups": groups, "rule-providers": compiler.providers, "rules": rules, "dns": sbObject{"enable": true, "listen": routercore.SocksAddress(ports.DNS), "ipv6": true, "enhanced-mode": "redir-host", "fake-ip-range": "198.19.255.1/30", "use-hosts": false, "use-system-hosts": false, "nameserver": []string{"udp://127.0.0.1:39004"}, "default-nameserver": []string{"udp://127.0.0.1:39005"}, "proxy-server-nameserver": []string{"udp://127.0.0.1:39005"}}, "_nailink_dns": profile}
	if iface := route["default_interface"]; iface != nil {
		result["interface-name"] = iface
	}
	// Race resolved entrances rather than serially waiting for a slow address.
	result["tcp-concurrent"] = true
	result["unified-delay"] = true
	for _, in := range mhObjects(profile["inbounds"]) {
		if in["type"] == "tun" {
			result["tun"] = sbObject{"enable": true, "device": routercore.TunName, "stack": "system", "auto-route": true, "auto-redirect": true, "auto-detect-interface": route["auto_detect_interface"] == true, "strict-route": true, "mtu": 1500, "inet6-address": []string{"fdfe:daed::1/126"}, "iproute2-table-index": 20340, "iproute2-rule-index": 20340, "auto-redirect-iproute2-fallback-rule-index": 32768, "auto-redirect-input-mark": 0x4e4101, "auto-redirect-output-mark": 0x4e4102, "include-interface": in["include_interface"], "dns-hijack": []string{"any:53", "tcp://any:53"}}
		}
	}
	if conf.Global.DialMode != "ip" {
		result["sniffer"] = sbObject{"enable": true, "force-dns-mapping": true, "parse-pure-ip": true, "override-destination": true, "sniff": sbObject{"HTTP": sbObject{"ports": []string{"1-65535"}}, "TLS": sbObject{"ports": []string{"1-65535"}}, "QUIC": sbObject{"ports": []string{"1-65535"}}}}
	}
	return result, nil
}
func safeMihomoLabel(tag string) string {
	tag = strings.TrimPrefix(tag, "node:")
	tag = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, tag)
	v := []rune(tag)
	if len(v) > 80 {
		return string(v[:80]) + "…"
	}
	return tag
}
func mhDurationSeconds(value any, fallback int) int {
	s := mhString(value)
	if s == "" {
		return fallback
	}
	n, e := time.ParseDuration(s)
	if e == nil && n > 0 {
		return max(1, int(n/time.Second))
	}
	return fallback
}

func mhProcessMode(rules []sbObject) string {
	for _, r := range rules {
		if len(mhStrings(r["process_name"])) > 0 || mhProcessMode(mhObjects(r["rules"])) == "strict" {
			return "strict"
		}
	}
	return "off"
}
