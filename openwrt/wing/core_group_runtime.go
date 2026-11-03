// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"github.com/daeuniverse/dae-wing/routercore"
	daeCommon "github.com/daeuniverse/dae/common"
	daeConfig "github.com/daeuniverse/dae/config"
	"sort"
	"time"
)

type routerGroupRuntime struct {
	Name           string   `json:"name"`
	Nodes          []string `json:"nodes"`
	Selected       string   `json:"selected,omitempty"`
	SelectedNodeID string   `json:"selectedNodeId,omitempty"`
	SelectionKind  string   `json:"selectionKind,omitempty"`
}

// Match the applied URI to a unique database row, never a display name or a
// draft group binding. An edited/deleted/ambiguous row has no active marker.
func routerGroupSelectedNodes(groups []routerGroupRuntime, conf *daeConfig.Config, core string, idsByLink map[string]string) {
	links := map[string]string{}
	for _, entry := range conf.Node {
		name, link := daeCommon.GetTagFromLinkLikePlaintext(string(entry))
		if name != "" {
			links[name] = link
		}
	}
	for i := range groups {
		g := &groups[i]
		selected := g.Selected
		kind := "native"
		if selected == "" && core == "dae" && len(g.Nodes) == 1 {
			selected, kind = g.Nodes[0], "single"
		}
		for _, candidate := range g.Nodes {
			if selected == candidate {
				if id := idsByLink[links[selected]]; id != "" {
					g.SelectedNodeID, g.SelectionKind = id, kind
				}
				break
			}
		}
	}
}

// Selection comes from the applied configuration and controller, never draft
// group bindings. The optional current URI index only resolves stable row IDs.
func RouterCoreGroups(ctx context.Context, nodeIDs ...map[string]string) string {
	result := struct {
		Core               string               `json:"core"`
		Ready              bool                 `json:"ready"`
		SelectionAvailable bool                 `json:"selectionAvailable"`
		Groups             []routerGroupRuntime `json:"groups"`
	}{Groups: []routerGroupRuntime{}}
	m := currentRouterCoreManager()
	if m == nil {
		data, _ := json.Marshal(result)
		return string(data)
	}
	m.mu.RLock()
	conf, session, dry := m.conf, m.active, m.dry
	result.Core, result.Ready = m.state.Current, m.phase == "ready" && conf != nil && conf != EmptyConfig
	m.mu.RUnlock()
	if result.Ready {
		for _, g := range conf.Group {
			group := routerGroupRuntime{Name: g.Name, Nodes: []string{}}
			for _, clause := range g.Filter {
				for _, f := range clause {
					if f.Name == "name" && !f.Not {
						for _, p := range f.Params {
							if p.Key == "" {
								group.Nodes = append(group.Nodes, p.Val)
							}
						}
					}
				}
			}
			sort.Strings(group.Nodes)
			result.Groups = append(result.Groups, group)
		}
		if original, ok := session.(*routerSingBoxSession); ok {
			s := original.current()
			var data struct {
				Proxies map[string]struct {
					Now string `json:"now"`
				} `json:"proxies"`
			}
			bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := s.api(bounded, "/proxies", &data)
			cancel()
			if err == nil {
				result.SelectionAvailable = true
				for i := range result.Groups {
					g := &result.Groups[i]
					tag := "group:" + g.Name
					if result.Core == "mihomo" {
						tag = routercore.MihomoName(tag)
					}
					selected := data.Proxies[tag].Now
					for _, n := range g.Nodes {
						candidate := "node:" + n
						if result.Core == "mihomo" {
							candidate = routercore.MihomoName(candidate)
						}
						if candidate == selected {
							g.Selected = n
							break
						}
					}
				}
			}
		}
		if len(nodeIDs) != 0 && !dry {
			routerGroupSelectedNodes(result.Groups, conf, result.Core, nodeIDs[0])
		}
		// Do not return an old selection if a stop, apply or switch completed
		// while the native controller was being read.
		m.mu.RLock()
		stillCurrent := m.conf == conf && m.active == session && m.phase == "ready"
		m.mu.RUnlock()
		if !stillCurrent {
			result.Ready = false
			result.Groups = []routerGroupRuntime{}
		}
	}
	sort.Slice(result.Groups, func(i, j int) bool { return result.Groups[i].Name < result.Groups[j].Name })
	data, _ := json.Marshal(result)
	return string(data)
}
