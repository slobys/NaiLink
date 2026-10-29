// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// NodeDNS contains exact server hostnames, never website routing policies.
type NodeDNS map[string][]string

func NodeDNSHost(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// Only encrypted, credential-free resolver URLs are accepted from subscriptions.
// IP literals must be public; DNS rebinding is additionally checked at dial time.
func NodeDNSResolver(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(raw) > 2048 {
		return false
	}
	h := NodeDNSHost(u.Hostname())
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return false
	}
	if u.Port() != "" {
		p, e := strconv.Atoi(u.Port())
		if e != nil || p < 1 || p > 65535 {
			return false
		}
	}
	if ip := net.ParseIP(h); ip != nil && !PublicResolverIP(ip) {
		return false
	}
	return true
}

func PublicResolverIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range reservedResolverNetworks {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

var reservedResolverNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
}

func DecodeNodeDNS(raw string) NodeDNS {
	if strings.HasPrefix(raw, "v1.") {
		if len(raw) > 384<<10 {
			return nil
		}
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, "v1."))
		if err != nil {
			return nil
		}
		raw = string(data)
	}
	var p NodeDNS
	if len(raw) > 256<<10 || json.Unmarshal([]byte(raw), &p) != nil || len(p) > 4096 {
		return nil
	}
	for host, resolvers := range p {
		if host == "" || host != NodeDNSHost(host) || net.ParseIP(host) != nil || strings.ContainsAny(host, "*/:@ \t\n") || len(resolvers) == 0 || len(resolvers) > 4 {
			return nil
		}
		for _, r := range resolvers {
			if !NodeDNSResolver(r) {
				return nil
			}
		}
	}
	return p
}

// A scalar carrier avoids the dae DSL parser retaining JSON quote escapes.
// This is encoding for recovery fidelity, not encryption.
func PackNodeDNS(raw string) string {
	policy := DecodeNodeDNS(raw)
	if len(policy) == 0 {
		return ""
	}
	data, _ := json.Marshal(policy)
	return "v1." + base64.RawURLEncoding.EncodeToString(data)
}

// Conflicts are omitted instead of silently choosing one subscription's policy.
func MergeNodeDNS(values []string, hosts map[string]bool) string {
	merged, conflicts := NodeDNS{}, map[string]bool{}
	for _, raw := range values {
		for host, resolvers := range DecodeNodeDNS(raw) {
			if !hosts[host] || conflicts[host] {
				continue
			}
			if old, ok := merged[host]; ok && strings.Join(old, "\n") != strings.Join(resolvers, "\n") {
				delete(merged, host)
				conflicts[host] = true
				continue
			}
			merged[host] = resolvers
		}
	}
	if len(merged) == 0 {
		return ""
	}
	b, _ := json.Marshal(merged)
	if len(b) > 256<<10 {
		return ""
	}
	return string(b)
}

func SortedNodeDNSHosts(p NodeDNS) []string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
