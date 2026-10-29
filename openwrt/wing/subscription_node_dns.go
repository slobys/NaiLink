// SPDX-License-Identifier: AGPL-3.0-only
package subscription

import (
	"context"
	"encoding/json"
	"github.com/daeuniverse/dae-wing/routercore"
	"gopkg.in/yaml.v3"
	"net"
	"strings"
)

type nodeDNSContextKey struct{}
type nodeDNSCapture struct{ value string }

func captureNodeDNS(ctx context.Context) (context.Context, *nodeDNSCapture) {
	c := new(nodeDNSCapture)
	return context.WithValue(ctx, nodeDNSContextKey{}, c), c
}

// Called only after the full document has passed bounded YAML validation.
func extractSubscriptionNodeDNS(ctx context.Context, root *yaml.Node, proxies *yaml.Node) {
	c, _ := ctx.Value(nodeDNSContextKey{}).(*nodeDNSCapture)
	if c == nil {
		return
	}
	c.value = ""
	var dnsNode *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "dns" {
			dnsNode = root.Content[i+1]
		}
	}
	if dnsNode == nil {
		return
	}
	var config struct {
		Enable  *bool          `yaml:"enable"`
		Servers any            `yaml:"proxy-server-nameserver"`
		Node    map[string]any `yaml:"proxy-server-nameserver-policy"`
		General map[string]any `yaml:"nameserver-policy"`
	}
	if dnsNode.Decode(&config) != nil {
		return
	}
	if config.Enable != nil && !*config.Enable {
		return
	}
	result := routercore.NodeDNS{}
	for _, entry := range proxies.Content {
		var n struct {
			Server string `yaml:"server"`
		}
		if entry.Decode(&n) != nil {
			continue
		}
		host := routercore.NodeDNSHost(n.Server)
		if host == "" || net.ParseIP(host) != nil {
			continue
		}
		// Exact host wins; wildcard policies apply only to actual node servers.
		lookup := func(p map[string]any) any {
			if v, ok := p[host]; ok {
				return v
			}
			best, length := any(nil), 0
			for pattern, v := range p {
				pattern = routercore.NodeDNSHost(pattern)
				if pattern == host {
					return v
				}
				if strings.HasPrefix(pattern, "*.") && len(pattern) > length && strings.HasSuffix(host, pattern[1:]) {
					best, length = v, len(pattern)
				}
				if strings.HasPrefix(pattern, "+.") && len(pattern) > length && (host == pattern[2:] || strings.HasSuffix(host, pattern[1:])) {
					best, length = v, len(pattern)
				}
			}
			return best
		}
		v := lookup(config.General)
		if config.Servers != nil {
			v = lookup(config.Node)
			if v == nil {
				v = config.Servers
			}
		}
		var raw []string
		switch x := v.(type) {
		case string:
			raw = []string{x}
		case []any:
			for _, y := range x {
				if s, ok := y.(string); ok {
					raw = append(raw, s)
				}
			}
		}
		if len(raw) == 0 || len(raw) > 4 {
			continue
		}
		valid := true
		for _, r := range raw {
			if !routercore.NodeDNSResolver(r) {
				valid = false
			}
		}
		if valid {
			result[host] = raw
		}
	}
	data, _ := json.Marshal(result)
	if len(result) > 0 && routercore.DecodeNodeDNS(string(data)) != nil {
		c.value = string(data)
	}
}
