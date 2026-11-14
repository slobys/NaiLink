// SPDX-License-Identifier: AGPL-3.0-only
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
	"strings"
	"unicode/utf8"
)

const servicePresetKey = "service-split-v1"

const serviceSplitStart = "# NaiLink service split\n"

var serviceAIMatches = []string{
	"suffix:chatgpt.com", "suffix:openai.com", "suffix:oaistatic.com",
	"suffix:oaiusercontent.com", "suffix:auth0.com",
	"suffix:claude.ai", "suffix:anthropic.com",
	"full:gemini.google.com", "suffix:generativelanguage.googleapis.com", "suffix:ai.google.dev",
	"suffix:perplexity.ai", "suffix:poe.com",
}

type groupService struct {
	ID           graphql.ID `json:"id"`
	Profile      string     `json:"profile"`
	FallbackName string     `json:"fallbackName,omitempty"`
}

func GroupServices(ctx context.Context) (string, error) {
	var groups []db.Group
	if err := db.DB(ctx).Order("id").Find(&groups).Error; err != nil {
		return "", err
	}
	names := map[uint]string{}
	for _, g := range groups {
		names[g.ID] = g.Name
	}
	result := []groupService{}
	for _, g := range groups {
		result = append(result, groupService{common.EncodeCursor(g.ID), g.ServiceProfile, names[g.FallbackGroupID]})
	}
	data, _ := json.Marshal(result)
	return string(data), nil
}
func SetGroupService(ctx context.Context, encoded graphql.ID, profile string) (int32, error) {
	if profile != "" && profile != "proxy" && profile != "ai" && profile != "netflix" {
		return 0, errors.New("invalid_service_profile")
	}
	id, err := common.DecodeCursor(encoded)
	if err != nil {
		return 0, err
	}
	q := db.DB(ctx).Model(&db.Group{}).Where("id = ?", id).Update("service_profile", profile)
	return int32(q.RowsAffected), q.Error
}
func serviceRouting(base, ai, netflix string) string {
	block := serviceSplitStart
	for _, match := range serviceAIMatches {
		block += "domain(" + match + ") -> " + ai + "\n"
	}
	block += "domain(geosite:netflix) -> " + netflix + "\n# End NaiLink service split\n"
	// Keep custom blocking/private rules ahead of the preset. Place service
	// domains before the ordinary CN shortcut, or before the final fallback.
	position := strings.Index(base, "dip(geoip:cn)")
	if position < 0 {
		position = strings.Index(base, "domain(geosite:cn)")
	}
	if position < 0 {
		position = strings.Index(base, "fallback:")
	}
	if position < 0 {
		return strings.TrimSpace(base) + "\n" + block
	}
	return base[:position] + block + base[position:]
}

func EnsureServicePresets(ctx context.Context, proxyID graphql.ID, name string, selectRoute bool) (string, error) {
	if !runLock.TryLock() {
		return "", errors.New("配置正在应用，请完成后再添加分流预设")
	}
	defer runLock.Unlock()
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 64 {
		return "", errors.New("路由名称请填写 1 至 64 个字")
	}
	id, err := common.DecodeCursor(proxyID)
	if err != nil {
		return "", err
	}
	var route db.Routing
	created := []string{}

	err = db.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var proxy db.Group
		if e := tx.First(&proxy, id).Error; e != nil {
			return e
		}
		existing := tx.Where("preset_key = ?", servicePresetKey).First(&route).Error
		// Existing routes, including an unedited older preset, belong to the
		// user. Never add domains or change exits during upgrade/reopening.
		if existing == nil {
			if selectRoute && !route.Selected {
				if e := tx.Model(&db.Routing{}).Where("selected = ?", true).Update("selected", false).Error; e != nil {
					return e
				}
				if e := tx.Model(&route).Update("selected", true).Error; e != nil {
					return e
				}
			}
			return nil
		}
		if !errors.Is(existing, gorm.ErrRecordNotFound) {
			return existing
		}
		if proxy.ServiceProfile == "" {
			if e := tx.Model(&proxy).Update("service_profile", "proxy").Error; e != nil {
				return e
			}
		}
		groups := map[string]db.Group{}
		for _, profile := range []string{"ai", "netflix"} {
			canonical := "AI"
			if profile == "netflix" {
				canonical = "Netflix"
			}
			var g db.Group
			e := tx.Where("service_profile = ? AND fallback_group_id = ?", profile, id).Order("id").First(&g).Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				e = tx.Where("name = ?", canonical).First(&g).Error
				if e == nil && g.ServiceProfile != "" && g.ServiceProfile != profile {
					g = db.Group{}
					e = gorm.ErrRecordNotFound
				}
				if errors.Is(e, gorm.ErrRecordNotFound) {
					for n := 1; ; n++ {
						candidate := canonical
						if n > 1 {
							candidate = fmt.Sprintf("%s_%d", canonical, n)
						}
						var count int64
						if e = tx.Model(&db.Group{}).Where("name = ?", candidate).Count(&count).Error; e != nil {
							return e
						}
						if count > 0 {
							continue
						}
						g = db.Group{Name: candidate, Policy: "min_moving_avg", ServiceProfile: profile, FallbackGroupID: id}
						if e = tx.Create(&g).Error; e != nil {
							return e
						}
						created = append(created, candidate)
						break
					}
				} else if e == nil {
					if e = tx.Model(&g).Updates(map[string]any{"service_profile": profile, "fallback_group_id": id}).Error; e != nil {
						return e
					}
					g.ServiceProfile, g.FallbackGroupID = profile, id
				} else {
					return e
				}
			} else if e != nil {
				return e
			}
			if g.ID == id {
				return errors.New("默认代理群组不能同时作为服务分流群组")
			}
			groups[profile] = g
		}
		e := tx.Where("preset_key = ?", servicePresetKey).First(&route).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			var base db.Routing
			if e = tx.Where("selected = ?", true).First(&base).Error; e != nil {
				return e
			}
			text := serviceRouting(base.Routing, groups["ai"].Name, groups["netflix"].Name)
			if _, e = dae.ParseConfig(nil, nil, &text); e != nil {
				return fmt.Errorf("分流规则校验失败: %w", e)
			}
			route = db.Routing{Name: name, Routing: text, PresetKey: servicePresetKey}
			if e = tx.Create(&route).Error; e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		if selectRoute {
			if e = tx.Model(&db.Routing{}).Where("selected = ?", true).Update("selected", false).Error; e != nil {
				return e
			}
			if e = tx.Model(&route).Update("selected", true).Error; e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	result, _ := json.Marshal(map[string]any{"routingId": common.EncodeCursor(route.ID), "name": route.Name, "createdGroups": created, "selected": route.Selected, "updatedRouting": false})
	return string(result), nil
}

// Resolve an empty preset in the applied snapshot only. Persistent bindings
// stay empty, so its next explicit node selection never duplicates proxy data.
func serviceFallbackNodes(d *gorm.DB, groups []db.Group, nodes []*node) ([]*node, error) {
	for i := range groups {
		g := &groups[i]
		if g.FallbackGroupID == 0 || g.FallbackGroupID == g.ID {
			continue
		}
		own := false
		for _, n := range nodes {
			for _, bound := range n.groups {
				own = own || bound.ID == g.ID
			}
		}
		if own {
			continue
		}
		var fallback db.Group
		if err := d.Preload("SubscriptionBindings.Subscription.Node").First(&fallback, g.FallbackGroupID).Error; err != nil {
			return nil, errors.New("分流备用群组不存在，请重新设置分流节点")
		}
		var values []db.Node
		if err := d.Model(&fallback).Association("Node").Find(&values); err != nil {
			return nil, err
		}
		for _, binding := range fallback.SubscriptionBindings {
			matched, err := matchedNodesForGroupSubscription(&binding)
			if err != nil {
				return nil, err
			}
			values = append(values, matched...)
		}
		for _, value := range values {
			value := value
			nodes = append(nodes, &node{dbNode: &value, groups: []*db.Group{g}})
		}
	}
	return nodes, nil
}
