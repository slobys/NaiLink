// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import "testing"

func TestNodeDNSPolicyScopeAndUnsafeResolvers(t *testing.T) {
	for _, raw := range []string{"http://dns.example/dns-query", "https://u:secret@dns.example/x", "https://127.0.0.1/x", "https://192.168.2.1/x", "https://[::1]/x", "https://dns.local/x", "https://dns.example:99999/x", "https://dns.example/x#tag"} {
		if NodeDNSResolver(raw) {
			t.Fatalf("accepted unsafe resolver %q", raw)
		}
	}
	if !NodeDNSResolver("https://dns.google/dns-query") {
		t.Fatal("public HTTPS resolver rejected")
	}
	a := `{"node.fixture.test":["https://dns.google/dns-query"],"website.fixture.test":["https://dns.google/dns-query"]}`
	merged := DecodeNodeDNS(MergeNodeDNS([]string{a}, map[string]bool{"node.fixture.test": true}))
	if len(merged) != 1 || len(merged["node.fixture.test"]) != 1 {
		t.Fatal("website policy escaped scope")
	}
	b := `{"node.fixture.test":["https://cloudflare-dns.com/dns-query"]}`
	if MergeNodeDNS([]string{a, b}, map[string]bool{"node.fixture.test": true}) != "" {
		t.Fatal("conflicting subscriptions silently chose a resolver")
	}
}
