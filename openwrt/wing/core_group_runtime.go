// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"github.com/daeuniverse/dae-wing/routercore"
	"sort"
	"time"
)

type routerGroupRuntime struct {
	Name     string   `json:"name"`
	Nodes    []string `json:"nodes"`
	Selected string   `json:"selected,omitempty"`
}

// Read the applied configuration, never the editable database or UI selection.
func RouterCoreGroups(ctx context.Context) string {
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
	conf, session := m.conf, m.active
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
	}
	sort.Slice(result.Groups, func(i, j int) bool { return result.Groups[i].Name < result.Groups[j].Name })
	data, _ := json.Marshal(result)
	return string(data)
}
