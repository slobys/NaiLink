package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/daeuniverse/dae-wing/routercore"
)

func TestCoreModeActionsAndDependencyReplacementGuard(t *testing.T) {
	for _, action := range []string{"core_prepare_tproxy", "core_prepare_tun", "core_check_mihomo_tproxy", "core_switch_singbox_tun"} {
		if !coreMaintenanceAction(action) {
			t.Fatal("valid mode action rejected", action)
		}
	}
	for _, action := range []string{"core_prepare_anything", "core_switch_dae_tproxy", "core_switch_mihomo_tproxy;reboot"} {
		if coreMaintenanceAction(action) {
			t.Fatal("unsafe mode action accepted", action)
		}
	}
	for _, plan := range []string{"(1/2) Upgrading kernel (6.12 -> 6.13)", "Removing firewall4", "Installing kernel (6.13)"} {
		if safeCoreDependencyPlan([]byte(plan)) {
			t.Fatal("accepted replacing installed packages", plan)
		}
	}
	if !safeCoreDependencyPlan([]byte("(1/2) Installing kmod-nft-tproxy (6.12.1-r1)")) {
		t.Fatal("matching new module rejected")
	}
}

func TestCoreArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, item := range []struct {
		name string
		kind byte
	}{{"../escape", tar.TypeReg}, {"/absolute", tar.TypeReg}, {"core/sing-box", tar.TypeSymlink}, {"core/sing-box", tar.TypeLink}, {"other", tar.TypeReg}} {
		t.Run(item.name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "asset.gz")
			f, _ := os.Create(archive)
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			header := &tar.Header{Name: item.name, Typeflag: item.kind, Mode: 0755}
			if item.kind == tar.TypeReg {
				header.Size = 1
			}
			if err := tw.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if header.Size > 0 {
				_, _ = tw.Write([]byte("x"))
			}
			_ = tw.Close()
			_ = gz.Close()
			_ = f.Close()
			out := filepath.Join(root, "binary")
			_ = os.WriteFile(out, nil, 0600)
			if extractSingbox(archive, out, "core/sing-box") == nil {
				t.Fatal("unsafe or missing binary accepted")
			}
		})
	}
}
func TestCoreInterruptedJobAndBusyGuard(t *testing.T) {
	m := maintenanceFixture(t)
	job := maintenanceJob{ID: "exact-job-id", Target: "sing-box", Action: "core_switch_singbox", State: "running"}
	state := routercore.State{Current: "dae", Previous: "dae", Target: "sing-box", Phase: "switching", Operation: job.ID}
	if err := routercore.SaveState(m.configDir, state); err != nil {
		t.Fatal(err)
	}
	if m.start("update").Code != "busy" {
		t.Fatal("started another maintenance operation during core transaction")
	}
	m.reconcileCoreJob(&job)
	if job.State != "running" {
		t.Fatal(job)
	}
	state.Current, state.Phase = "sing-box", "stable"
	_ = routercore.SaveState(m.configDir, state)
	m.reconcileCoreJob(&job)
	if job.State != "success" {
		t.Fatal(job)
	}
	job.State, job.Code = "error", "backend_unavailable"
	if err := writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), job); err != nil {
		t.Fatal(err)
	}
	status := m.status()
	if status.Job.State != "success" || status.Job.Code != "recovered" {
		t.Fatal("lost response was not reconciled", status.Job)
	}
	job.State = "error"
	state.Operation = "unrelated"
	_ = routercore.SaveState(m.configDir, state)
	m.reconcileCoreJob(&job)
	if job.State == "success" {
		t.Fatal("unrelated core transaction was accepted")
	}
}
func TestCoreDownloadFailurePreservesInstallAndData(t *testing.T) {
	m := maintenanceFixture(t)
	m.coreBinary = filepath.Join(t.TempDir(), "core", "sing-box")
	_ = os.Mkdir(filepath.Dir(m.coreBinary), 0700)
	_ = os.WriteFile(m.coreBinary, []byte("old-core"), 0755)
	database := filepath.Join(m.configDir, "wing.db")
	_ = os.WriteFile(database, []byte("user-data"), 0600)
	m.fetch = func(_ context.Context, _, dest string, _ int64) error {
		return os.WriteFile(dest, []byte("truncated"), 0600)
	}
	result := runFixtureJob(t, m, "core_install_singbox")
	if result.Job.State != "error" || result.Job.Code != "checksum_failed" {
		t.Fatal(result.Job)
	}
	if data, _ := os.ReadFile(m.coreBinary); string(data) != "old-core" {
		t.Fatal("old binary changed")
	}
	if data, _ := os.ReadFile(database); string(data) != "user-data" {
		t.Fatal("user database changed")
	}
	files, _ := filepath.Glob(filepath.Join(m.stateDir, "core-download-*"))
	if len(files) != 0 {
		t.Fatal("download staging leaked")
	}
}
func TestCorePinnedArchiveInstall(t *testing.T) {
	archive := os.Getenv("DAED_TEST_SINGBOX_ARCHIVE")
	if archive == "" {
		t.Skip("provide pinned compatibility archive for native install test")
	}
	m := maintenanceFixture(t)
	m.coreBinary = filepath.Join(t.TempDir(), "core", "sing-box")
	m.fetch = func(_ context.Context, address, dest string, _ int64) error {
		if address != "https://github.com/slobys/NaiLink/releases/download/"+routercore.SingBoxRelease+"/"+routercore.Releases["amd64"].Archive {
			t.Fatal("unexpected core origin", address)
		}
		in, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	m.run = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == m.initScript || args[0] == "modprobe" {
			return nil, nil
		}
		return maintenanceCommand(context.Background(), args...)
	}
	result := runFixtureJob(t, m, "core_install_singbox")
	if result.Job.State != "success" || !result.Cores.Installed {
		t.Fatal(result.Job)
	}
}

func TestExtraArchiveAndFailedUpdatePreserveExisting(t *testing.T) {
	archive := os.Getenv("DAED_TEST_EXTRA_ARCHIVE")
	if archive == "" {
		t.Skip("provide reviewed extension archive")
	}
	m := maintenanceFixture(t)
	m.extraRoot = filepath.Join(t.TempDir(), "components")
	db := filepath.Join(m.configDir, "wing.db")
	_ = os.WriteFile(db, []byte("user configuration"), 0600)
	m.fetch = func(_ context.Context, _, dest string, _ int64) error {
		in, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	result := runFixtureJob(t, m, "core_install_extra")
	if result.Job.State != "success" || !result.Cores.Extra {
		t.Fatal(result.Job)
	}
	installed, err := routercore.ExtraDirectory(m.extraRoot)
	if err != nil {
		t.Fatal(err)
	}
	// A bad subsequent archive must leave the exact previous pointer and DB.
	m.fetch = func(_ context.Context, _, dest string, _ int64) error {
		return os.WriteFile(dest, []byte("truncated"), 0600)
	}
	result = runFixtureJob(t, m, "core_install_extra")
	if result.Job.State != "error" || result.Job.Code != "checksum_failed" {
		t.Fatal(result.Job)
	}
	after, err := routercore.ExtraDirectory(m.extraRoot)
	if err != nil || after != installed {
		t.Fatal("old components replaced")
	}
	if raw, _ := os.ReadFile(db); string(raw) != "user configuration" {
		t.Fatal("user configuration changed")
	}
	if paths, _ := filepath.Glob(filepath.Join(m.extraRoot, ".extra-stage-*")); len(paths) != 0 {
		t.Fatal("staging leaked")
	}
}

func TestMihomoArchiveAndFailurePreserveExisting(t *testing.T) {
	archive := os.Getenv("DAED_TEST_MIHOMO_ARCHIVE")
	if archive == "" {
		t.Skip("provide reviewed static Mihomo archive")
	}
	m := maintenanceFixture(t)
	m.mihomoBinary = filepath.Join(t.TempDir(), "core", "mihomo")
	db := filepath.Join(m.configDir, "wing.db")
	os.WriteFile(db, []byte("all user nodes and configuration"), 0600)
	m.fetch = func(_ context.Context, address, dest string, _ int64) error {
		if address != "https://github.com/slobys/NaiLink/releases/download/"+routercore.MihomoRelease+"/"+routercore.MihomoReleases["amd64"].Archive {
			t.Fatal("unexpected origin", address)
		}
		data, e := os.ReadFile(archive)
		if e != nil {
			return e
		}
		return os.WriteFile(dest, data, 0600)
	}
	m.run = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "modprobe" || args[0] == m.initScript {
			return nil, nil
		}
		return maintenanceCommand(ctx, args...)
	}
	result := runFixtureJob(t, m, "core_install_mihomo")
	if result.Job.State != "success" || !result.Cores.Engines["mihomo"].Installed {
		t.Fatal(result.Job)
	}
	before, e := os.ReadFile(m.mihomoPath())
	if e != nil {
		t.Fatal(e)
	}
	m.fetch = func(_ context.Context, _, dest string, _ int64) error {
		return os.WriteFile(dest, []byte("truncated"), 0600)
	}
	result = runFixtureJob(t, m, "core_install_mihomo")
	if result.Job.State != "error" || result.Job.Code != "checksum_failed" {
		t.Fatal(result.Job)
	}
	after, _ := os.ReadFile(m.mihomoPath())
	if string(before) != string(after) {
		t.Fatal("failed download replaced old core")
	}
	if data, _ := os.ReadFile(db); string(data) != "all user nodes and configuration" {
		t.Fatal("user configuration changed")
	}
	if files, _ := filepath.Glob(filepath.Join(m.stateDir, "core-download-*")); len(files) != 0 {
		t.Fatal("download staging leaked")
	}
}
