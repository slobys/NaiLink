package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daeuniverse/dae-wing/routercore"
)

func coreUpdatePrivateDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}
func updateCatalogFixture(t *testing.T) coreUpdateCatalog {
	t.Helper()
	data, err := os.ReadFile("../openwrt/core-updates.json")
	if err != nil {
		// File-list Go tests execute from the prepared module directory.
		data, err = os.ReadFile(filepath.Join(filepath.Dir(os.Getenv("DAED_TEST_CORE_UPDATES")), "catalog.json"))
	}
	if err != nil {
		t.Fatal(err)
	}
	var c coreUpdateCatalog
	if err = json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}
func TestCoreUpdateCatalogBindings(t *testing.T) {
	c := updateCatalogFixture(t)
	for _, engine := range []string{"sing-box", "mihomo"} {
		for _, arch := range []string{"amd64", "arm64"} {
			result, err := validateCoreCatalog(c, engine, arch)
			if err != nil || result.Version == "" {
				t.Fatal(engine, arch, err)
			}
		}
	}
	for _, bad := range []string{"../binary.tar.gz", "nailink-mihomo-1.19.34-arm64.tar.gz", "https://evil.test/core"} {
		changed := updateCatalogFixture(t)
		r := changed.Engines["sing-box"]
		a := r.Assets["amd64"]
		a.Archive = bad
		r.Assets["amd64"] = a
		changed.Engines["sing-box"] = r
		if _, err := validateCoreCatalog(changed, "sing-box", "amd64"); err == nil {
			t.Fatal("accepted foreign asset", bad)
		}
	}
	r := c.Engines["sing-box"]
	r.MinAdapter = routercore.AdapterRevision + 1
	r.MaxAdapter = r.MinAdapter
	c.Engines["sing-box"] = r
	if _, err := validateCoreCatalog(c, "sing-box", "amd64"); err == nil || err.Error() != "core_update_plugin_required" {
		t.Fatal(err)
	}
	if _, err := validateCoreCatalog(c, "mihomo", "mips"); err == nil {
		t.Fatal("accepted unsupported ABI")
	}
}
func TestCoreUpdateCheckStoresFailureInsteadOfStaleSuccess(t *testing.T) {
	m := maintenanceFixture(t)
	c := updateCatalogFixture(t)
	raw, _ := json.Marshal(c)
	m.fetch = func(_ context.Context, url, dest string, _ int64) error {
		if url != coreCatalogURL {
			t.Fatal(url)
		}
		return os.WriteFile(dest, raw, 0600)
	}
	job := &maintenanceJob{ID: "check", Action: "core_version_mihomo"}
	if err := m.maintainCore(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if got := m.checkedUpdate("mihomo"); got == nil || got.Version == "" || got.Code != "" {
		t.Fatal(got)
	}
	m.fetch = func(context.Context, string, string, int64) error { return errors.New("download_timeout") }
	if err := m.maintainCore(context.Background(), job); err == nil {
		t.Fatal("lost download failure")
	}
	if got := m.checkedUpdate("mihomo"); got == nil || got.Code != "download_timeout" || got.Version != "" {
		t.Fatal(got)
	}
}
func TestCoreUpdateActiveAndExpiredGuards(t *testing.T) {
	m := maintenanceFixture(t)
	c, err := validateCoreCatalog(updateCatalogFixture(t), "mihomo", runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	c.Checked = m.now().Unix()
	writeMaintenanceJSON(m.updatePath("mihomo"), c)
	if err = routercore.SaveState(m.configDir, routercore.State{Current: "mihomo", Phase: "stable"}); err != nil {
		t.Fatal(err)
	}
	m.fetch = func(context.Context, string, string, int64) error { t.Fatal("downloaded active core"); return nil }
	if err = m.updateOptionalCore(context.Background(), &maintenanceJob{ID: "active"}, "mihomo"); err == nil || err.Error() != "core_update_active" {
		t.Fatal(err)
	}
	c.Checked -= 3601
	writeMaintenanceJSON(m.updatePath("mihomo"), c)
	if err = m.updateOptionalCore(context.Background(), &maintenanceJob{ID: "expired"}, "mihomo"); err == nil || err.Error() != "check_required" {
		t.Fatal(err)
	}
}
func TestCoreUpdateRealArchivesPreserveConfiguration(t *testing.T) {
	root := os.Getenv("DAED_TEST_CORE_UPDATES")
	if root == "" {
		t.Skip("provide reviewed core update archives")
	}
	for _, engine := range []string{"sing-box", "mihomo"} {
		t.Run(engine, func(t *testing.T) {
			m := maintenanceFixture(t)
			m.coreBinary = filepath.Join(coreUpdatePrivateDir(t), "sing-box")
			m.mihomoBinary = filepath.Join(coreUpdatePrivateDir(t), "mihomo")
			binary := m.corePath()
			if engine == "mihomo" {
				binary = m.mihomoPath()
			}
			db := filepath.Join(m.configDir, "wing.db")
			os.WriteFile(db, []byte("existing nodes, subscriptions and configuration"), 0600)
			os.WriteFile(binary, []byte("previous executable"), 0700)
			c, err := validateCoreCatalog(updateCatalogFixture(t), engine, runtime.GOARCH)
			if err != nil {
				t.Fatal(err)
			}
			c.Checked = m.now().Unix()
			writeMaintenanceJSON(m.updatePath(engine), c)
			address := "https://github.com/slobys/NaiLink/releases/download/" + c.Release + "/" + c.Asset.Archive
			m.fetch = func(_ context.Context, url, dest string, _ int64) error {
				if url != address {
					t.Fatal(url)
				}
				data, e := os.ReadFile(filepath.Join(root, c.Asset.Archive))
				if e != nil {
					return e
				}
				return os.WriteFile(dest, data, 0600)
			}
			priorRun := m.run
			m.run = func(ctx context.Context, args ...string) ([]byte, error) {
				if args[0] == binary {
					return []byte("unknown previous version"), nil
				}
				return priorRun(ctx, args...)
			}
			job := &maintenanceJob{ID: "real-archive", Action: "core_update_" + strings.ReplaceAll(engine, "-", "")}
			if err = m.maintainCore(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if version, e := routercore.ExpectedCoreVersion(binary, engine); e != nil || version != c.Version {
				t.Fatal(version, e)
			}
			output, e := maintenanceCommand(context.Background(), binary, map[string]string{"sing-box": "version", "mihomo": "-v"}[engine])
			if e != nil || !strings.Contains(string(output), c.Version) {
				t.Fatal(string(output), e)
			}
			if saved, _ := os.ReadFile(db); string(saved) != "existing nodes, subscriptions and configuration" {
				t.Fatal("changed user data")
			}
			if old, _ := os.ReadFile(binary + ".previous"); string(old) != "previous executable" {
				t.Fatal("lost previous executable")
			}
			m.run = priorRun
			if got := m.coreStatus().Engines[engine].Version; got != c.Version {
				t.Fatal("did not report installed version", got)
			}
		})
	}
}
func TestCoreUpdateBadDownloadNeverReplacesBinary(t *testing.T) {
	for _, engine := range []string{"sing-box", "mihomo"} {
		m := maintenanceFixture(t)
		m.coreBinary = filepath.Join(coreUpdatePrivateDir(t), "sing-box")
		m.mihomoBinary = filepath.Join(coreUpdatePrivateDir(t), "mihomo")
		binary := m.corePath()
		if engine == "mihomo" {
			binary = m.mihomoPath()
		}
		os.WriteFile(binary, []byte("old"), 0700)
		c, _ := validateCoreCatalog(updateCatalogFixture(t), engine, runtime.GOARCH)
		c.Checked = m.now().Unix()
		writeMaintenanceJSON(m.updatePath(engine), c)
		m.run = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("no output") }
		m.fetch = func(_ context.Context, _ string, dest string, _ int64) error {
			return os.WriteFile(dest, []byte("bad download"), 0600)
		}
		err := m.updateOptionalCore(context.Background(), &maintenanceJob{ID: "bad-hash"}, engine)
		if err == nil || err.Error() != "checksum_failed" {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(binary); string(b) != "old" {
			t.Fatal("overwrote existing binary")
		}
	}
}
func TestCoreUpdateReceiptFailureRestoresBothFiles(t *testing.T) {
	root := coreUpdatePrivateDir(t)
	binary := filepath.Join(root, "sing-box")
	candidate := filepath.Join(root, "candidate")
	os.WriteFile(binary, []byte("old"), 0700)
	os.WriteFile(candidate, []byte("new"), 0700)
	old := []byte(`{"previous":"receipt"}`)
	os.WriteFile(routercore.ReceiptPath(binary), old, 0600)
	h := sha256.Sum256([]byte("new"))
	receipt := routercore.CoreReceipt{Engine: "sing-box", Version: "1.14.4-nailink.1", SHA256: hex.EncodeToString(h[:])}
	err := publishCoreUpdateWithWriter(binary, candidate, receipt, "failed-write", func(string, []byte) error { return errors.New("disk failure") })
	if err == nil {
		t.Fatal("claimed success")
	}
	if data, _ := os.ReadFile(binary); string(data) != "old" {
		t.Fatal("lost old binary")
	}
	if data, _ := os.ReadFile(routercore.ReceiptPath(binary)); string(data) != string(old) {
		t.Fatal("lost old receipt")
	}
}
func TestCoreUpdateVersionsAndOriginRestrictions(t *testing.T) {
	for _, p := range [][2]string{{"1.14.4-nailink.1", "1.14.3-nailink.1"}, {"1.14.4-nailink.2", "1.14.4-nailink.1"}, {"1.19.34", "1.19.33"}} {
		if !newerCoreVersion(p[0], p[1]) || newerCoreVersion(p[1], p[0]) {
			t.Fatal(p)
		}
	}
	for _, action := range []string{"core_version_singbox", "core_version_mihomo", "core_update_singbox", "core_update_mihomo"} {
		if !coreMaintenanceAction(action) {
			t.Fatal(action)
		}
	}
	if coreMaintenanceAction("core_update_mihomo;reboot") {
		t.Fatal("accepted command injection")
	}
}
