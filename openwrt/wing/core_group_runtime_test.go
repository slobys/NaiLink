// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"encoding/json"
	daeConfig "github.com/daeuniverse/dae/config"
	"strings"
	"testing"
)

func TestRouterGroupSelectionUsesAppliedURIAndConfirmedChoice(t *testing.T) {
	conf := &daeConfig.Config{Node: []daeConfig.KeyableString{
		"unique-A:socks5://192.0.2.1:1080#A", "unique-B:socks5://192.0.2.2:1080#B",
	}}
	ids := map[string]string{"socks5://192.0.2.1:1080#A": "cursor-A", "socks5://192.0.2.2:1080#B": "cursor-B"}
	for _, tc := range []struct {
		core       string
		group      routerGroupRuntime
		want, kind string
	}{
		{"dae", routerGroupRuntime{Nodes: []string{"unique-A"}}, "cursor-A", "single"},
		{"dae", routerGroupRuntime{Nodes: []string{"unique-A", "unique-B"}}, "", ""},
		{"sing-box", routerGroupRuntime{Nodes: []string{"unique-A"}}, "", ""},
		{"sing-box", routerGroupRuntime{Nodes: []string{"unique-A", "unique-B"}, Selected: "unique-B"}, "cursor-B", "native"},
		{"mihomo", routerGroupRuntime{Nodes: []string{"unique-A"}, Selected: "unique-A"}, "cursor-A", "native"},
		{"mihomo", routerGroupRuntime{Nodes: []string{"unique-A"}, Selected: "unique-B"}, "", ""},
	} {
		groups := []routerGroupRuntime{tc.group}
		routerGroupSelectedNodes(groups, conf, tc.core, ids)
		if groups[0].SelectedNodeID != tc.want || groups[0].SelectionKind != tc.kind {
			t.Fatalf("%s: %+v", tc.core, groups[0])
		}
		data, _ := json.Marshal(groups)
		if strings.Contains(string(data), "://") {
			t.Fatal("runtime result disclosed a URI")
		}
	}
	for _, ids := range []map[string]string{nil, {}, {"socks5://192.0.2.1:1080#edited": "cursor-A"}, {"socks5://192.0.2.1:1080#A": ""}} {
		groups := []routerGroupRuntime{{Nodes: []string{"unique-A"}, Selected: "unique-A"}}
		routerGroupSelectedNodes(groups, conf, "mihomo", ids)
		if groups[0].SelectedNodeID != "" {
			t.Fatal("edited, deleted or duplicate node got an active marker")
		}
	}
}
