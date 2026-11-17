// SPDX-License-Identifier: AGPL-3.0-only
package config

import (
	"context"
	"errors"
	"strings"

	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	"gorm.io/gorm"
)

// This frozen predecessor is deliberately narrower than the preset generator.
// A version, extra comment, changed exit or custom rule makes a route user-owned.
const legacyNativeIPv6Preset = `routing {
pname(NetworkManager, systemd-resolved, dnsmasq) -> must_direct
dip(geoip:private) -> direct
# NaiLink service split
domain(suffix:chatgpt.com) -> AI
domain(suffix:openai.com) -> AI
domain(suffix:oaistatic.com) -> AI
domain(suffix:oaiusercontent.com) -> AI
domain(suffix:auth0.com) -> AI
domain(suffix:claude.ai) -> AI
domain(suffix:anthropic.com) -> AI
domain(full:gemini.google.com) -> AI
domain(suffix:generativelanguage.googleapis.com) -> AI
domain(suffix:ai.google.dev) -> AI
domain(suffix:perplexity.ai) -> AI
domain(suffix:poe.com) -> AI
domain(geosite:netflix) -> Netflix
# End NaiLink service split
dip(geoip:cn) -> direct
domain(geosite:cn) -> direct
# Native IPv6 fallback; explicit routing above still takes priority
ipversion(6) -> direct
fallback: proxy
}`

func servicePresetLines(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// UpgradeLegacyServiceIPv6Presets runs before the daemon restores its running
// snapshot. Only a byte-equivalent, unedited managed predecessor is eligible;
// custom routing and DNS are never regenerated from new defaults.
func UpgradeLegacyServiceIPv6Presets(ctx context.Context, dirs []string) (int, error) {
	d := db.DB(ctx)
	var saved []db.Routing
	if err := d.Where("preset_key = ? AND version = ?", servicePresetKey, 0).Find(&saved).Error; err != nil {
		return 0, err
	}
	var candidates []db.Routing
	for _, route := range saved {
		if servicePresetLines(route.Routing) == legacyNativeIPv6Preset {
			candidates = append(candidates, route)
		}
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	if !dae.RouterServiceIPv6AssetsAvailable(dirs) {
		return 0, errors.New("IPv6 service categories are unavailable; existing routing retained")
	}
	updated := 0
	err := d.Transaction(func(tx *gorm.DB) error {
		var groups int64
		if err := tx.Model(&db.Group{}).Where("name IN ?", []string{"proxy", "AI", "Netflix"}).Count(&groups).Error; err != nil {
			return err
		}
		if groups != 3 {
			return nil
		}
		for _, route := range candidates {
			text := strings.Replace(route.Routing, "dip(geoip:cn)", serviceIPv6Routing+"proxy\ndip(geoip:cn)", 1)
			result := tx.Model(&db.Routing{}).Where("id = ? AND version = ? AND routing = ?", route.ID, 0, route.Routing).
				Updates(map[string]any{"routing": text, "version": 1})
			if result.Error != nil {
				return result.Error
			}
			updated += int(result.RowsAffected)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return updated, nil
}
