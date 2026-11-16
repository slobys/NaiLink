// SPDX-License-Identifier: AGPL-3.0-only
package subscription

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae-wing/routercore"
	"gopkg.in/yaml.v3"
)

func TestMixedXHTTPSubscriptionsImportAllNodes(t *testing.T) {
	xhttp := func(name string) map[string]any {
		return map[string]any{"name": name, "type": "vless", "server": "fixture.invalid", "port": 443, "uuid": "11111111-1111-4111-8111-111111111111", "network": "xhttp", "tls": true, "xhttp-opts": map[string]any{"mode": "auto", "path": "/xhttp", "x-padding-bytes": "100-1000"}}
	}
	proxies := []map[string]any{
		{"name": "ws-1", "type": "vless", "server": "fixture.invalid", "port": 443, "uuid": "11111111-1111-4111-8111-111111111111", "network": "ws", "ws-opts": map[string]any{"path": "/ws"}},
		xhttp("xhttp-2"),
		{"name": "hy-3", "type": "hysteria2", "server": "192.0.2.1", "port": 443, "password": "fixture"},
		{"name": "hy-4", "type": "hysteria2", "server": "2001:db8::1", "port": 443, "password": "fixture"},
		xhttp("xhttp-5"),
		{"name": "trojan-6", "type": "trojan", "server": "fixture.invalid", "port": 443, "password": "fixture"},
		{"name": "ss-7", "type": "ss", "server": "fixture.invalid", "port": 443, "cipher": "aes-128-gcm", "password": "fixture", "network": "tcp", "tls": false, "udp": true},
	}
	proxies[4]["encryption"] = "mlkem768x25519plus.native.0rtt." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	proxies[4]["flow"] = "xtls-rprx-vision"
	clash, _ := json.Marshal(map[string]any{"proxies": proxies, "rules": []string{"MATCH,DIRECT"}, "dns": map[string]any{"enable": true}})
	var plain []string
	for _, p := range proxies {
		link, err := routercore.ClashLink(p)
		if err != nil {
			t.Fatal(err)
		}
		plain = append(plain, link)
	}
	for _, payload := range [][]byte{clash, []byte(strings.Join(plain, "\n")), []byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(plain, "\n"))))} {
		links, err := decodeSubscriptionPayload(payload)
		if err != nil || len(links) != 7 {
			t.Fatal("subscription was rejected or lost nodes", len(links), err)
		}
		for _, link := range links {
			model, err := db.NewNodeModel(link, nil, nil)
			if err != nil || model.Link != link {
				t.Fatal("refresh changes saved node", err)
			}
		}
	}
}

// Private captures stay outside source control. Never print a payload or link
// on failure; they contain the provider's subscription token and credentials.
func TestPrivateSevenNodeSubscriptions(t *testing.T) {
	dir := os.Getenv("NAILINK_PRIVATE_SUBSCRIPTION_DIR")
	if dir == "" {
		t.Skip("private capture not supplied")
	}
	for _, name := range []string{"links.txt", "clash.txt"} {
		data, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			t.Fatal("private capture unreadable")
		}
		links, err := decodeSubscriptionPayload(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(links) != 7 {
			t.Fatalf("%s: count=%d", name, len(links))
		}
		for i, link := range links {
			model, err := db.NewNodeModel(link, nil, nil)
			if err != nil || model.Link != link {
				t.Fatalf("%s item %d metadata mismatch", name, i+1)
			}
			node, err := routercore.Node(link, "fixture", routercore.NodeOptions{Mihomo: true})
			// The provider's third node carries a malformed ECH public name.
			// Metadata import must preserve it, while runtime validation must
			// refuse to disable ECH or relax certificate checks as a fallback.
			if i == 2 && err != nil && err.Error() == "invalid_ech_public_name" {
				t.Logf("%s item 3: provider ECH requires correction; original link retained", name)
				continue
			}
			if err != nil {
				t.Fatalf("%s item %d canonical: %s", name, i+1, err.Error())
			}
			if _, err = routercore.MihomoNode(node); err != nil {
				t.Fatalf("%s item %d mihomo: %s", name, i+1, err.Error())
			}
		}
	}
}

const clashFixture = `mixed-port: 7890
external-controller: 0.0.0.0:9090
dns: {nameserver: [https://dns.fixture.invalid/dns-query]}
rules: [MATCH,proxy]
proxies:
  - {name: 香港 Trojan, type: trojan, server: 192.0.2.10, port: 443, password: 'test:p@ss/+', sni: fixture.test, udp: true, skip-cert-verify: false}
  - name: VMess WS
    type: vmess
    server: fixture.test
    port: 443
    uuid: 11111111-1111-4111-8111-111111111111
    alterId: 0
    cipher: auto
    tls: true
    udp: true
    skip-cert-verify: false
    network: ws
    ws-path: /ws
    ws-headers: {Host: fixture.test}
    ws-opts: {path: /ws, headers: {Host: fixture.test}}
`

func TestClashConvertedURIsKeepLegacyNodeMetadata(t *testing.T) {
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if e := yaml.Unmarshal([]byte(clashFixture), &doc); e != nil {
		t.Fatal(e)
	}
	for i, p := range doc.Proxies {
		link, e := routercore.ClashLink(p)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.NewNodeModel(link, nil, nil); e != nil {
			t.Fatalf("fixture %d metadata: %v", i, e)
		}
	}
}

func TestClashSubscriptionOnlyImportsProxies(t *testing.T) {
	for _, payload := range []string{clashFixture, "\ufeff" + clashFixture, base64.StdEncoding.EncodeToString([]byte(clashFixture)), base64.RawURLEncoding.EncodeToString([]byte(clashFixture))} {
		links, e := decodeSubscriptionPayload([]byte(payload))
		if e != nil {
			t.Fatal(e)
		}
		if len(links) != 2 {
			t.Fatal("DNS/controller URLs were imported or proxies lost", len(links))
		}
		for _, link := range links {
			if strings.HasPrefix(link, "http") {
				t.Fatal("imported configuration URL")
			}
			if _, e := routercore.Node(link, "fixture", routercore.NodeOptions{}); e != nil {
				t.Fatal(e)
			}
		}
	}
}
func TestSubscriptionRejectsStructuredErrorsAndPartialUpdates(t *testing.T) {
	for _, payload := range []string{
		`<html><a href="https://login.fixture.invalid">login</a></html>`,
		`{"error":"https://private-token.fixture.invalid/login"}`,
		"proxy-providers:\n  external: {type: http, url: 'https://private-token.fixture.invalid/other'}",
		"proxies: []\nproxies: []",
		"proxies: []\n---\nproxies: []",
		"proxies: &cycle [*cycle]",
		clashFixture + "  - {name: unsupported, type: unknown, server: fixture.test, port: 443, password: private-token}",
		"https://private-token.fixture.invalid/sub?key=private-token",
		fixtureNode + "\nunknown://user:private-token@fixture.invalid:443",
	} {
		links, e := decodeSubscriptionPayload([]byte(payload))
		if e == nil || len(links) != 0 {
			t.Fatal("invalid/partial subscription accepted")
		}
		if strings.Contains(e.Error(), "private-token") {
			t.Fatal("credentials leaked")
		}
		if !isSubscriptionContentError(e) {
			t.Fatal("content failure would trigger pointless network retries")
		}
	}
}
func TestSubscriptionPlainAndBase64URIsRemainCompatible(t *testing.T) {
	for _, payload := range []string{fixtureNode, base64.StdEncoding.EncodeToString([]byte(fixtureNode)), "# comment\n" + fixtureNode + "\n" + fixtureNode} {
		links, e := decodeSubscriptionPayload([]byte(payload))
		if e != nil || len(links) != 1 || links[0] != fixtureNode {
			t.Fatal("ordinary subscription regression", e)
		}
	}
}
func TestClashSubscriptionAliasAndMerge(t *testing.T) {
	payload := `defaults: &defaults {type: trojan, server: 192.0.2.10, port: 443, password: fixture}
proxies:
  - <<: *defaults
    name: first
  - <<: *defaults
    name: second
`
	links, e := decodeSubscriptionPayload([]byte(payload))
	if e != nil || len(links) != 2 {
		t.Fatal("safe YAML aliases regressed", e)
	}
}
func TestClashCapturedProviderDocument(t *testing.T) {
	path := os.Getenv("DAED_PRIVATE_CLASH_CAPTURE")
	if path == "" {
		t.Skip("private provider capture is never included in source")
	}
	payload, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	links, e := decodeSubscriptionPayload(payload)
	if e != nil {
		t.Fatal(e)
	}
	if len(links) != 70 {
		t.Fatal("provider node count mismatch", len(links))
	}
	for _, link := range links {
		if _, e := routercore.Node(link, "private", routercore.NodeOptions{}); e != nil {
			t.Fatal(e)
		}
	}
	t.Log("private 70-node Clash document parsed without importing global settings")
}
