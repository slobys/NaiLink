// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"slices"
	"strings"
	"testing"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
)

func TestWebsiteConnectionMatchesOnlyTheProbe(t *testing.T) {
	payload := `{"uploadTotal":123,"connections":[
		{"metadata":{"sourceIP":"192.0.2.7","sourcePort":"42127","host":"chatgpt.com"},"chains":["other"]},
		{"metadata":{"sourceIP":"127.0.0.1","sourcePort":42128,"host":"chatgpt.com"},"chains":["another"]},
		{"metadata":{"sourceIP":"127.0.0.1","sourcePort":"42127","host":"gemini.google.com"},"chains":["wrong-domain"]},
		{"metadata":{"sourceIP":"127.0.0.1","sourcePort":42127,"host":"chatgpt.com"},"chains":["node:chosen","group:AI"]}
	]}`
	chain, err := websiteConnectionChain(strings.NewReader(payload), 42127, "chatgpt.com")
	if err != nil || !slices.Equal(chain, []string{"node:chosen", "group:AI"}) {
		t.Fatalf("unexpected route: %v (%v)", chain, err)
	}
	if payload, err := websiteConnectionChain(strings.NewReader(payload), 9999, "chatgpt.com"); err != nil || len(payload) != 0 {
		t.Fatal("matched an unrelated socket")
	}
}

func TestWebsiteOutletOnlyShowsKnownAppliedLabels(t *testing.T) {
	conf := &daeConfig.Config{
		Group: []daeConfig.Group{{Name: "AI"}},
		Node:  []daeConfig.KeyableString{"private-node:socks5://127.0.0.1:1080"},
	}
	group, node := websiteOutlet(conf, "mihomo", []string{routercore.MihomoName("group:AI"), routercore.MihomoName("node:private-node")})
	if group != "AI" || node != "private-node" {
		t.Fatalf("unexpected route %q %q", group, node)
	}
	if group, node := websiteOutlet(conf, "sing-box", []string{"socks5://private", "unknown"}); group != "" || node != "" {
		t.Fatal("returned a route absent from the applied configuration")
	}
	if result := routerDiagnoseWebsite(t.Context(), "https://127.0.0.1/"); result.Code != "invalid_target" {
		t.Fatal("accepted a URL in place of a known website")
	}
}
