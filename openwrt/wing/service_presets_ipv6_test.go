// SPDX-License-Identifier: AGPL-3.0-only
package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae/pkg/geodata"
	"google.golang.org/protobuf/proto"
)

func serviceIPv6Assets(t *testing.T, codes ...string) []string {
	t.Helper()
	root := t.TempDir()
	list := &geodata.GeoSiteList{}
	for _, code := range codes {
		list.Entry = append(list.Entry, &geodata.GeoSite{CountryCode: strings.ToUpper(code), Domain: []*geodata.Domain{{Type: geodata.Domain_RootDomain, Value: code + ".example"}}})
	}
	data, err := proto.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "geosite.dat"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return []string{root}
}

func serviceLegacyIPv6Fixture(t *testing.T) (context.Context, db.Routing) {
	t.Helper()
	ctx := serviceFixture(t)
	d := db.DB(ctx)
	base := "routing {\npname(NetworkManager, systemd-resolved, dnsmasq) -> must_direct\ndip(geoip:private) -> direct\ndip(geoip:cn) -> direct\ndomain(geosite:cn) -> direct\nfallback: proxy\n}"
	if err := d.Model(&db.Routing{}).Where("id = ?", 1).Update("routing", base).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureServicePresets(ctx, common.EncodeCursor(1), "分流", true); err != nil {
		t.Fatal(err)
	}
	var route db.Routing
	if err := d.Where("preset_key = ?", servicePresetKey).First(&route).Error; err != nil {
		t.Fatal(err)
	}
	// Reproduce the released native-IPv6 preset by removing only the new rule.
	route.Routing = strings.Replace(route.Routing, serviceIPv6Routing+"proxy\n", "", 1)
	if err := d.Model(&route).Update("routing", route.Routing).Error; err != nil {
		t.Fatal(err)
	}
	return ctx, route
}

func TestServiceIPv6UpgradeOnlyUneditedManagedPredecessor(t *testing.T) {
	ctx, original := serviceLegacyIPv6Fixture(t)
	d := db.DB(ctx)
	node := db.Node{Name: "keep", Link: "socks5://192.0.2.1:1080"}
	if err := d.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	dirs := serviceIPv6Assets(t, "google", "youtube", "github")
	for _, expected := range []int{1, 0} {
		count, err := UpgradeLegacyServiceIPv6Presets(ctx, dirs)
		if err != nil || count != expected {
			t.Fatalf("upgrade count=%d err=%v", count, err)
		}
	}
	var got db.Routing
	d.First(&got, original.ID)
	if got.Version != 1 || got.Name != original.Name || got.Selected != original.Selected || !strings.Contains(got.Routing, serviceIPv6Routing+"proxy\n") {
		t.Fatalf("unexpected migrated preset: %+v", got)
	}
	if strings.Index(got.Routing, "domain(full:gemini.google.com) -> AI") > strings.Index(got.Routing, serviceIPv6Routing) || strings.Index(got.Routing, serviceIPv6Routing) > strings.Index(got.Routing, "dip(geoip:cn)") {
		t.Fatal("lost AI priority or left service routing behind CN shortcuts")
	}
	var saved db.Node
	d.First(&saved, node.ID)
	var groups int64
	d.Model(&db.Group{}).Count(&groups)
	if saved.Link != node.Link || saved.Name != node.Name || groups != 3 {
		t.Fatal("upgrade changed user nodes or groups")
	}
}

func TestServiceIPv6UpgradePreservesEditsVersionsAndUnmanagedRoutes(t *testing.T) {
	for _, edit := range []string{"comment", "rule", "exit", "ipv6", "version", "unmanaged"} {
		t.Run(edit, func(t *testing.T) {
			ctx, original := serviceLegacyIPv6Fixture(t)
			switch edit {
			case "comment":
				original.Routing = strings.Replace(original.Routing, "dip(geoip:private)", "# my note\ndip(geoip:private)", 1)
			case "rule":
				original.Routing = strings.Replace(original.Routing, "fallback:", "domain(full:custom.example) -> direct\nfallback:", 1)
			case "exit":
				original.Routing = strings.ReplaceAll(original.Routing, "-> AI", "-> MyAI")
			case "ipv6":
				original.Routing = strings.Replace(original.Routing, "ipversion(6) -> direct", "ipversion(6) -> proxy", 1)
			case "version":
				original.Version = 9
			case "unmanaged":
				original.PresetKey = ""
			}
			if err := db.DB(ctx).Save(&original).Error; err != nil {
				t.Fatal(err)
			}
			count, err := UpgradeLegacyServiceIPv6Presets(ctx, nil)
			var got db.Routing
			db.DB(ctx).First(&got, original.ID)
			if err != nil || count != 0 || got != original {
				t.Fatalf("changed user-owned routing: count=%d err=%v", count, err)
			}
		})
	}
}

func TestServiceIPv6UpgradeMissingAssetsAndDatabaseFailureRetainOriginal(t *testing.T) {
	for _, failure := range []string{"assets", "database"} {
		t.Run(failure, func(t *testing.T) {
			ctx, original := serviceLegacyIPv6Fixture(t)
			dirs := serviceIPv6Assets(t, "google", "youtube", "github")
			if failure == "assets" {
				dirs = serviceIPv6Assets(t, "cn")
			} else if err := db.DB(ctx).Exec("CREATE TRIGGER deny_preset_update BEFORE UPDATE ON routings BEGIN SELECT RAISE(ABORT, 'fixture'); END").Error; err != nil {
				t.Fatal(err)
			}
			count, err := UpgradeLegacyServiceIPv6Presets(ctx, dirs)
			var got db.Routing
			db.DB(ctx).First(&got, original.ID)
			if err == nil || count != 0 || got != original {
				t.Fatal("failed upgrade did not retain original preset")
			}
		})
	}
}

func TestServiceIPv6FreshPresetPreservesExplicitIPv6Policy(t *testing.T) {
	for _, exit := range []string{"direct", "block", "custom"} {
		base := "routing {\nipversion ( 6 ) -> " + exit + "\ndip(geoip:cn) -> direct\nfallback: custom\n}"
		got := serviceRouting(base, "custom", "MyAI", "MyNetflix")
		if !strings.Contains(got, "ipversion ( 6 ) -> "+exit) || strings.Contains(got, serviceIPv6Routing) || strings.Contains(got, "ipversion(6) -> direct") {
			t.Fatal("overrode explicit IPv6 routing", exit)
		}
	}
	for _, condition := range []string{"ipversion('6')", "!ipversion(4)"} {
		got := serviceRouting("routing {\n"+condition+" -> block\nfallback: custom\n}", "custom", "MyAI", "MyNetflix")
		if strings.Contains(got, serviceIPv6Routing) || !strings.Contains(got, condition+" -> block") {
			t.Fatal("reinterpreted an explicit IPv6 policy", condition)
		}
	}
}
