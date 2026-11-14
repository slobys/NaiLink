// SPDX-License-Identifier: AGPL-3.0-only
package config

import (
	"context"
	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae-wing/graphql/service/group"
	"github.com/graph-gophers/graphql-go"
	"io"
	"strings"
	"testing"
)

func serviceFixture(t *testing.T) context.Context {
	t.Helper()
	if err := db.InitDatabase(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	db.SetOutput(io.Discard)
	ctx := context.Background()
	d := db.DB(ctx)
	for _, m := range []any{&db.Group{ID: 1, Name: "proxy", Policy: "random"}, &db.Routing{ID: 1, Name: "custom", Routing: "routing {\n# custom note\ndip(geoip:private) -> direct\ndip(geoip:cn) -> direct\nfallback: proxy\n}", Selected: true}} {
		if err := d.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}
	return ctx
}

func TestServiceLegacySchemaUpgradePreservesRows(t *testing.T) {
	directory := t.TempDir()
	ctx := context.Background()
	if err := db.InitDatabase(directory); err != nil {
		t.Fatal(err)
	}
	db.SetOutput(io.Discard)
	d := db.DB(ctx)
	originalGroup := db.Group{ID: 1, Name: "customProxy", Policy: "random", Version: 7}
	originalRoute := db.Routing{ID: 1, Name: "customRoute", Routing: "routing {fallback: customProxy}", Version: 9, Selected: true}
	originalNode := db.Node{ID: 1, Name: "private", Link: "socks5://192.0.2.1:1080"}
	for _, v := range []any{&originalGroup, &originalRoute, &originalNode} {
		if e := d.Create(v).Error; e != nil {
			t.Fatal(e)
		}
	}
	for _, q := range []string{"ALTER TABLE groups DROP COLUMN service_profile", "ALTER TABLE groups DROP COLUMN fallback_group_id", "ALTER TABLE routings DROP COLUMN preset_key"} {
		if e := d.Exec(q).Error; e != nil {
			t.Fatal(e)
		}
	}
	connection, _ := d.DB()
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.InitDatabase(directory); err != nil {
		t.Fatal(err)
	}
	d = db.DB(ctx)
	db.SetOutput(io.Discard)
	var g db.Group
	var route db.Routing
	var n db.Node
	d.First(&g, 1)
	d.First(&route, 1)
	d.First(&n, 1)
	if g.Name != originalGroup.Name || g.Policy != originalGroup.Policy || g.Version != 7 || g.ServiceProfile != "" || g.FallbackGroupID != 0 {
		t.Fatal("legacy group changed during additive migration")
	}
	if route.Name != originalRoute.Name || route.Routing != originalRoute.Routing || route.Version != 9 || !route.Selected || route.PresetKey != "" {
		t.Fatal("legacy selected routing changed")
	}
	if n.Link != originalNode.Link || n.Name != originalNode.Name {
		t.Fatal("node changed during migration")
	}
}
func TestServicePresetsAdditiveIdempotentAndRename(t *testing.T) {
	ctx := serviceFixture(t)
	d := db.DB(ctx)
	var before db.Routing
	d.First(&before, 1)
	if _, err := EnsureServicePresets(ctx, common.EncodeCursor(1), "分流", false); err != nil {
		t.Fatal(err)
	}
	var after db.Routing
	d.First(&after, 1)
	if before != after {
		t.Fatal("replaced current routing")
	}
	var preset db.Routing
	d.Where("preset_key = ?", servicePresetKey).First(&preset)
	if preset.Selected || !strings.Contains(preset.Routing, "geosite:netflix") || strings.Index(preset.Routing, "# NaiLink") > strings.Index(preset.Routing, "dip(geoip:cn)") {
		t.Fatal("wrong routing priority")
	}
	var ai db.Group
	d.Where("name = ?", "AI").First(&ai)
	if ai.ServiceProfile != "ai" || ai.FallbackGroupID != 1 {
		t.Fatal("purpose missing")
	}
	if _, err := group.Rename(ctx, common.EncodeCursor(ai.ID), "MyAI"); err != nil {
		t.Fatal(err)
	}
	d.First(&preset, preset.ID)
	if !strings.Contains(preset.Routing, " -> MyAI\n") {
		t.Fatal("rename lost routing references")
	}
	if _, err := SetGroupService(ctx, common.EncodeCursor(ai.ID), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureServicePresets(ctx, common.EncodeCursor(1), "new name", false); err != nil {
		t.Fatal(err)
	}
	d.First(&ai, ai.ID)
	if ai.ServiceProfile != "" || ai.Name != "MyAI" {
		t.Fatal("reinstallation overwrote user edits")
	}
	var count int64
	d.Model(&db.Group{}).Count(&count)
	if count != 3 {
		t.Fatal("duplicate presets", count)
	}
}
func TestServicePresetsFreshSelectionAndConflict(t *testing.T) {
	ctx := serviceFixture(t)
	d := db.DB(ctx)
	d.Create(&db.Group{Name: "AI", Policy: "random", ServiceProfile: "netflix"})
	if _, err := EnsureServicePresets(ctx, common.EncodeCursor(1), "分流", true); err != nil {
		t.Fatal(err)
	}
	var chosen db.Routing
	d.Where("selected = ?", true).First(&chosen)
	if chosen.Name != "分流" {
		t.Fatal("new installation did not select preset")
	}
	var ai db.Group
	d.Where("service_profile = ?", "ai").First(&ai)
	if ai.Name != "AI_2" {
		t.Fatal("name collision overwrote existing group")
	}
}
func TestRequestedServiceDefaultsUseExistingAIGroup(t *testing.T) {
	ctx := serviceFixture(t)
	d := db.DB(ctx)
	if err := d.Create(&db.Group{Name: "MyAI", Policy: "random", ServiceProfile: "ai", FallbackGroupID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureServicePresets(ctx, common.EncodeCursor(1), "分流", true); err != nil {
		t.Fatal(err)
	}
	var route db.Routing
	if err := d.Where("selected = ?", true).First(&route).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := dae.ParseConfig(nil, nil, &route.Routing); err != nil {
		t.Fatal(err)
	}
	// Independent expected rules: an added or missing Google dependency changes
	// the user's deliberate split and must fail this regression.
	want := []string{
		"domain(suffix:chatgpt.com) -> MyAI",
		"domain(suffix:openai.com) -> MyAI",
		"domain(suffix:oaistatic.com) -> MyAI",
		"domain(suffix:oaiusercontent.com) -> MyAI",
		"domain(suffix:auth0.com) -> MyAI",
		"domain(suffix:claude.ai) -> MyAI",
		"domain(suffix:anthropic.com) -> MyAI",
		"domain(full:gemini.google.com) -> MyAI",
		"domain(suffix:generativelanguage.googleapis.com) -> MyAI",
		"domain(suffix:ai.google.dev) -> MyAI",
		"domain(suffix:perplexity.ai) -> MyAI",
		"domain(suffix:poe.com) -> MyAI",
		"domain(geosite:netflix) -> Netflix",
	}
	_, body, ok := strings.Cut(route.Routing, serviceSplitStart)
	if !ok {
		t.Fatal("no managed block")
	}
	block, _, ok := strings.Cut(body, "# End NaiLink service split\n")
	if !ok || strings.TrimSpace(block) != strings.Join(want, "\n") {
		t.Fatalf("unexpected default service rules: %s", block)
	}
	if strings.Index(route.Routing, want[0]) > strings.Index(route.Routing, "dip(geoip:cn)") {
		t.Fatal("service rules after CN shortcut")
	}
	var count int64
	d.Model(&db.Group{}).Count(&count)
	if count != 3 {
		t.Fatal("unexpected groups", count)
	}
}

func TestServiceDefaultsNeverRewriteExistingPreset(t *testing.T) {
	ctx := serviceFixture(t)
	d := db.DB(ctx)
	if _, err := EnsureServicePresets(ctx, common.EncodeCursor(1), "分流", true); err != nil {
		t.Fatal(err)
	}
	var preset db.Routing
	if err := d.Where("preset_key = ?", servicePresetKey).First(&preset).Error; err != nil {
		t.Fatal(err)
	}
	// Old combined-domain preset, custom edits and renamed exits all survive.
	old := "routing {\ndip(geoip:private) -> direct\n# NaiLink service split\ndomain(suffix:chatgpt.com, full:gemini.google.com) -> AI\ndomain(full:accounts.google.com) -> AI\ndomain(geosite:netflix) -> Netflix\n# End NaiLink service split\ndip(geoip:cn) -> direct\ndomain(geosite:cn) -> direct\nfallback: proxy\n}"
	for _, version := range []uint{0, 1, 17} {
		if err := d.Model(&db.Routing{}).Where("id = ?", preset.ID).Updates(map[string]any{"routing": old, "version": version}).Error; err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := EnsureServicePresets(ctx, common.EncodeCursor(1), "新名字", false); err != nil {
				t.Fatal(err)
			}
		}
		var got db.Routing
		if err := d.First(&got, preset.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.Routing != old || got.Version != version || !got.Selected || got.Name != "分流" {
			t.Fatalf("existing preset changed: %+v", got)
		}
	}
}

func TestServiceFallbackIsSnapshotOnlyAndOwnNodesWin(t *testing.T) {
	ctx := serviceFixture(t)
	d := db.DB(ctx)
	if _, err := EnsureServicePresets(ctx, common.EncodeCursor(1), "分流", false); err != nil {
		t.Fatal(err)
	}
	n := db.Node{Name: "n", Link: "socks5://192.0.2.1:1080"}
	d.Create(&n)
	if _, err := group.AddNodes(ctx, common.EncodeCursor(1), []graphql.ID{common.EncodeCursor(n.ID)}); err != nil {
		t.Fatal(err)
	}
	var groups []db.Group
	d.Order("id").Find(&groups)
	values, err := serviceFallbackNodes(d, groups, []*node{{dbNode: &n, groups: []*db.Group{&groups[0]}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 {
		t.Fatal("empty service groups missing fallback", len(values))
	}
	var count int64
	d.Table("group_nodes").Count(&count)
	if count != 1 {
		t.Fatal("fallback changed bindings")
	}
	own := db.Node{Name: "own", Link: "socks5://192.0.2.2:1080"}
	values, err = serviceFallbackNodes(d, groups, []*node{{dbNode: &n, groups: []*db.Group{&groups[0]}}, {dbNode: &own, groups: []*db.Group{&groups[1]}}})
	if err != nil || len(values) != 3 {
		t.Fatal("explicit service nodes mixed with fallback", err, len(values))
	}
}
func TestServiceRenameSkipsDomainsCommentsAndRollsBackDuplicate(t *testing.T) {
	ctx := serviceFixture(t)
	d := db.DB(ctx)
	d.Create(&db.Group{ID: 2, Name: "AI", Policy: "random"})
	r := db.Routing{Name: "refs", Routing: "routing {\n# note -> AI\ndomain(suffix:AI) -> AI # comment -> AI\ndomain(full:AI) -> AILonger\nfallback: AI\n}"}
	d.Create(&r)
	if _, err := group.Rename(ctx, common.EncodeCursor(2), "proxy"); err == nil {
		t.Fatal("duplicate rename reported success")
	}
	var current db.Routing
	d.First(&current, r.ID)
	if current.Routing != r.Routing {
		t.Fatal("failed rename committed routing changes")
	}
	if _, err := group.Rename(ctx, common.EncodeCursor(2), "renamed"); err != nil {
		t.Fatal(err)
	}
	var renamed db.Group
	d.First(&renamed, 2)
	if renamed.Version != 1 {
		t.Fatal("renamed group did not become pending")
	}
	d.First(&current, r.ID)
	if !strings.Contains(current.Routing, "# note -> AI") || !strings.Contains(current.Routing, "domain(suffix:AI) -> renamed # comment -> AI") || !strings.Contains(current.Routing, "-> AILonger") || !strings.Contains(current.Routing, "fallback: renamed") {
		t.Fatal("rename damaged rules or comments", current.Routing)
	}
}
