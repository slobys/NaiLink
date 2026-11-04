package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/daeuniverse/dae/pkg/geodata"
	"google.golang.org/protobuf/proto"
)

func maintenanceFixture(t *testing.T) *maintenanceManager {
	t.Helper()
	root := t.TempDir()
	m := newMaintenance(filepath.Join(root, "config"))
	m.stateDir = filepath.Join(root, "state")
	m.versionFile = filepath.Join(root, "version")
	m.lockFile = filepath.Join(root, "maintenance.lock")
	m.installLock = filepath.Join(root, "install.lock")
	m.initScript = filepath.Join(root, "init")
	os.Mkdir(m.stateDir, 0700)
	os.Mkdir(m.configDir, 0700)
	os.WriteFile(m.versionFile, []byte("0.6.32-1\n"), 0600)
	m.now = func() time.Time { return time.Unix(1800000000, 0) }
	m.run = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == m.initScript {
			return nil, nil
		}
		return maintenanceCommand(ctx, args...)
	}
	return m
}

func TestConfigurationTaskSurvivesBrowserAndRejectsDuplicate(t *testing.T) {
	for _, action := range []string{"config_apply", "config_stop"} {
		t.Run(action, func(t *testing.T) {
			m := maintenanceFixture(t)
			entered, release, finished := make(chan string, 1), make(chan struct{}), make(chan error, 1)
			m.graph = func(ctx context.Context, query string, value any) error {
				entered <- query
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				return json.Unmarshal([]byte(`{"run":1}`), value)
			}
			accepted := m.start(action)
			if !accepted.Success || accepted.Job == nil {
				t.Fatalf("start: %+v", accepted)
			}
			go func() { finished <- m.worker(context.Background(), accepted.Job.ID) }()
			select {
			case query := <-entered:
				want := "mutation {run(dry:false)}"
				if action == "config_stop" {
					want = "mutation {run(dry:true)}"
				}
				if query != want {
					t.Errorf("query = %q", query)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("worker not started")
			}
			// A new CGI process sees the same durable running task. No browser
			// connection or database lock is needed to query its status.
			copy := *m
			active := copy.status()
			if active.Job == nil || active.Job.ID != accepted.Job.ID || active.Job.State != "running" {
				t.Errorf("active = %+v", active.Job)
			}
			if duplicate := copy.start(action); duplicate.Success || duplicate.Code != "busy" {
				t.Errorf("duplicate accepted: %+v", duplicate)
			}
			close(release)
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			completed := copy.status()
			if completed.Job == nil || completed.Job.ID != accepted.Job.ID || completed.Job.State != "success" {
				t.Fatalf("receipt = %+v", completed.Job)
			}
		})
	}
}

func TestConfigurationTaskDoesNotMisreportUnconfirmedAsFailed(t *testing.T) {
	for _, fixture := range []struct {
		err  error
		code string
	}{
		{errors.New("backend_unavailable"), "configuration_result_unconfirmed"},
		{errors.New("apply_failed"), "configuration_apply_failed"},
		{nil, "configuration_result_unconfirmed"},
	} {
		m := maintenanceFixture(t)
		m.graph = func(context.Context, string, any) error { return fixture.err }
		result := runFixtureJob(t, m, "config_apply")
		if result.Job.State != "error" || result.Job.Code != fixture.code {
			t.Fatalf("receipt = %+v", result.Job)
		}
	}
}

func TestConfigurationRequestIDIsIdempotentAndValidated(t *testing.T) {
	m := maintenanceFixture(t)
	starts := 0
	m.run = func(context.Context, ...string) ([]byte, error) { starts++; return nil, nil }
	m.graph = func(_ context.Context, _ string, value any) error { return json.Unmarshal([]byte(`{"run":1}`), value) }
	for _, id := range []string{"../invalid", strings.Repeat("a", 25), strings.Repeat("g", 24)} {
		if result := m.startTask("config_apply", id); result.Success {
			t.Fatal("invalid ID accepted")
		}
	}
	id := strings.Repeat("a", 24)
	first := m.startTask("config_apply", id)
	if !first.Success || first.Job.ID != id {
		t.Fatalf("start = %+v", first)
	}
	if err := m.worker(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	replay := m.startTask("config_apply", id)
	if !replay.Success || replay.Job.State != "success" || starts != 1 {
		t.Fatalf("replayed task: %+v, starts=%d", replay, starts)
	}
	if result := m.startTask("geodata", id); result.Success {
		t.Fatal("configuration ID accepted for unrelated action")
	}
}
func releaseFixture(tag string, draft bool) map[string]any {
	return map[string]any{"tag_name": tag, "draft": draft, "assets": []map[string]string{{"name": "install.sh"}, {"name": "SHA256SUMS"}, {"name": "manifest.tsv"}}}
}
func fixtureFetch(files map[string][]byte) func(context.Context, string, string, int64) error {
	return func(_ context.Context, address, dest string, limit int64) error {
		data, ok := files[address]
		if !ok || len(data) > int(limit) {
			return errors.New("fixture_download_failure")
		}
		return os.WriteFile(dest, data, 0600)
	}
}
func runFixtureJob(t *testing.T, m *maintenanceManager, action string) maintenanceStatus {
	t.Helper()
	s := m.start(action)
	if !s.Success {
		t.Fatalf("start: %+v", s)
	}
	if err := m.worker(context.Background(), s.Job.ID); err != nil {
		t.Fatal(err)
	}
	return m.status()
}

func TestMaintenanceVersionSelectionAndPersistentJob(t *testing.T) {
	m := maintenanceFixture(t)
	data, _ := json.Marshal([]any{releaseFixture("openwrt-v9.0.0", true), releaseFixture("other-v10", false), releaseFixture("openwrt-v0.6.33", false), releaseFixture("openwrt-v0.6.9", false)})
	m.fetch = fixtureFetch(map[string][]byte{"https://api.github.com/repos/slobys/NaiLink/releases?per_page=10": data})
	result := runFixtureJob(t, m, "check")
	if result.Latest == nil || result.Latest.Version != "0.6.33" || result.Job.State != "success" {
		t.Fatalf("%+v", result)
	}
	// A new process/request sees the same task and checked version.
	copy := *m
	if copy.status().Job.ID != result.Job.ID {
		t.Fatal("task only existed in request memory")
	}
	if m.start("update; rm -rf /").Code != "invalid_action" {
		t.Fatal("arbitrary action accepted")
	}
	for _, pair := range [][2]string{{"0.6.33", "0.6.32-1"}, {"0.7.0", "0.6.99"}, {"1.0.0", "0.99.9"}} {
		if !versionNewer(pair[0], pair[1]) {
			t.Fatal(pair)
		}
	}
	for _, pair := range [][2]string{{"0.6.32", "0.6.32-1"}, {"0.6.9", "0.6.32"}, {"evil", "0.6.32"}} {
		if versionNewer(pair[0], pair[1]) {
			t.Fatal(pair)
		}
	}
}

func TestMaintenanceMutualExclusionAndInterruptedRecovery(t *testing.T) {
	m := maintenanceFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	m.fetch = func(_ context.Context, _ string, dest string, _ int64) error {
		close(entered)
		<-release
		data, _ := json.Marshal([]any{releaseFixture("openwrt-v0.6.33", false)})
		return os.WriteFile(dest, data, 0600)
	}
	if !m.start("check").Success {
		t.Fatal("queue failed")
	}
	done := make(chan error, 1)
	go func() { done <- m.worker(context.Background(), m.status().Job.ID) }()
	<-entered
	if m.start("geodata").Code != "busy" || m.status().Job.State != "running" {
		t.Fatal("running worker was replaced or reported interrupted")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	old := maintenanceJob{ID: "fixture", State: "running", Updated: m.now().Unix()}
	writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), old)
	if job := m.status().Job; job.State != "error" || job.Code != "interrupted" {
		t.Fatal("dead worker left infinite loading")
	}
	old.State = "queued"
	old.Updated = m.now().Unix()
	writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), old)
	if m.start("check").Code != "busy" {
		t.Fatal("freshly queued worker overwritten")
	}
}

func TestMaintenanceInstallerChecksumAndPreservedData(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			m := maintenanceFixture(t)
			writeMaintenanceJSON(filepath.Join(m.stateDir, "latest.json"), releaseInfo{"0.6.33", "openwrt-v0.6.33", m.now().Unix()})
			sentinel := filepath.Join(m.configDir, "nodes.db")
			os.WriteFile(sentinel, []byte("existing nodes and settings"), 0600)
			script := []byte("#!/bin/sh\nrelease=openwrt-v0.6.33\nprintf '0.6.33-1\\n' > " + m.versionFile + "\n")
			hash := sha256.Sum256(script)
			sums := []byte(hex.EncodeToString(hash[:]) + "  install.sh\n")
			if corrupt {
				script = append(script, []byte("# corrupted")...)
			}
			base := "https://github.com/slobys/NaiLink/releases/download/openwrt-v0.6.33/"
			m.fetch = fixtureFetch(map[string][]byte{base + "install.sh": script, base + "SHA256SUMS": sums})
			result := runFixtureJob(t, m, "update")
			if corrupt {
				if result.Job.Code != "checksum_failed" || m.current() != "0.6.32-1" {
					t.Fatalf("unsafe corrupt install: %+v", result.Job)
				}
			} else if result.Job.State != "success" || m.current() != "0.6.33-1" {
				t.Fatalf("install failed: %+v", result.Job)
			}
			data, _ := os.ReadFile(sentinel)
			if string(data) != "existing nodes and settings" {
				t.Fatal("user data changed")
			}
		})
	}
}

func TestMaintenanceDownloadFailuresNeverRunInstaller(t *testing.T) {
	for _, code := range append([]string{"download_failed"}, downloadFailureCodes...) {
		t.Run(code, func(t *testing.T) {
			m := maintenanceFixture(t)
			writeMaintenanceJSON(filepath.Join(m.stateDir, "latest.json"), releaseInfo{"0.6.49", "openwrt-v0.6.49", m.now().Unix()})
			sentinel := filepath.Join(m.configDir, "nodes.db")
			os.WriteFile(sentinel, []byte("preserve nodes and pending settings"), 0600)
			m.fetch = func(context.Context, string, string, int64) error { return errors.New(code) }
			m.run = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] != m.initScript {
					t.Fatal("installer/service invoked after failed download", args[0])
				}
				return nil, nil
			}
			result := runFixtureJob(t, m, "update")
			data, _ := os.ReadFile(sentinel)
			if result.Job.State != "error" || result.Job.Code != code || m.current() != "0.6.32-1" || string(data) != "preserve nodes and pending settings" {
				t.Fatal("unsafe download failure", result.Job)
			}
		})
	}
}

func TestMaintenanceInstallerPreparationFailuresStaySpecific(t *testing.T) {
	for _, item := range []struct{ output, code string }{
		{"Firmware package feed refresh failed", "package_feed_failed"},
		{"Package preflight failed", "package_preflight_failed"},
		{"Update preparation timed out", "preparation_timeout"},
		{"Firmware package feed refresh failed\nprevious installation and service state restored", "update_rolled_back"},
	} {
		t.Run(item.code, func(t *testing.T) {
			m := maintenanceFixture(t)
			writeMaintenanceJSON(filepath.Join(m.stateDir, "latest.json"), releaseInfo{"0.6.50", "openwrt-v0.6.50", m.now().Unix()})
			script := []byte("#!/bin/sh\nrelease=openwrt-v0.6.50\n")
			hash := sha256.Sum256(script)
			base := "https://github.com/slobys/NaiLink/releases/download/openwrt-v0.6.50/"
			m.fetch = fixtureFetch(map[string][]byte{base + "install.sh": script, base + "SHA256SUMS": []byte(hex.EncodeToString(hash[:]) + "  install.sh\n")})
			m.run = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == m.initScript || len(args) > 1 && args[1] == "-n" {
					return nil, nil
				}
				return []byte(item.output), errors.New("exit status 1")
			}
			result := runFixtureJob(t, m, "update")
			if result.Job.State != "error" || result.Job.Code != item.code || m.current() != "0.6.32-1" {
				t.Fatal("preparation failure lost or changed installed version", result.Job)
			}
		})
	}
}

func TestMaintenancePreparationBudgetIncludesInitialDownloads(t *testing.T) {
	for _, minutes := range []int{4, 6, 8, 20} {
		t.Run(strconv.Itoa(minutes), func(t *testing.T) {
			m := maintenanceFixture(t)
			writeMaintenanceJSON(filepath.Join(m.stateDir, "latest.json"), releaseInfo{"0.6.50", "openwrt-v0.6.50", m.now().Unix()})
			script := []byte("#!/bin/sh\nrelease=openwrt-v0.6.50\n")
			hash := sha256.Sum256(script)
			base := "https://github.com/slobys/NaiLink/releases/download/openwrt-v0.6.50/"
			m.fetch = fixtureFetch(map[string][]byte{base + "install.sh": script, base + "SHA256SUMS": []byte(hex.EncodeToString(hash[:]) + "  install.sh\n")})
			installed := false
			m.run = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == m.initScript || len(args) > 1 && args[1] == "-n" {
					return nil, nil
				}
				if args[len(args)-2] != "--prepare-timeout" {
					t.Fatal("missing preparation budget")
				}
				seconds, err := strconv.Atoi(args[len(args)-1])
				if err != nil || seconds <= 0 || seconds > 900 || seconds > (minutes-7)*60 {
					t.Fatal("installer deadline leaves no recovery reserve", seconds)
				}
				installed = true
				return nil, os.WriteFile(m.versionFile, []byte("0.6.50-1\n"), 0600)
			}
			status := m.start("update")
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(minutes)*time.Minute)
			defer cancel()
			if err := m.worker(ctx, status.Job.ID); err != nil {
				t.Fatal(err)
			}
			result := m.status()
			if minutes <= 7 {
				if installed || result.Job.Code != "preparation_timeout" || m.current() != "0.6.32-1" {
					t.Fatal("late preparation started an install", result.Job)
				}
			} else if !installed || result.Job.State != "success" {
				t.Fatal("bounded preparation failed", result.Job)
			}
		})
	}
}

func TestMaintenanceUnsafeStateOldTargetAndMissingWorker(t *testing.T) {
	m := maintenanceFixture(t)
	if m.start("update").Code != "check_required" {
		t.Fatal("unchecked update accepted")
	}
	writeMaintenanceJSON(filepath.Join(m.stateDir, "latest.json"), releaseInfo{"0.6.31", "openwrt-v0.6.31", m.now().Unix()})
	if m.start("update").Code != "already_latest" {
		t.Fatal("downgrade accepted")
	}
	m.run = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("unavailable") }
	if m.start("check").Code != "worker_unavailable" || m.status().Job.State != "error" {
		t.Fatal("missing worker reported running")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("private"), 0600)
	os.Remove(filepath.Join(m.stateDir, "job.json"))
	os.Symlink(outside, filepath.Join(m.stateDir, "job.json"))
	var job maintenanceJob
	if readMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), &job) == nil {
		t.Fatal("symlinked state accepted")
	}
	os.RemoveAll(m.stateDir)
	os.Symlink(filepath.Dir(outside), m.stateDir)
	if m.start("check").Code != "unsafe_state" {
		t.Fatal("symlinked state directory accepted")
	}
}

func geodataFixture(t *testing.T, m *maintenanceManager) map[string][]byte {
	t.Helper()
	ip, _ := proto.Marshal(&geodata.GeoIPList{Entry: []*geodata.GeoIP{{CountryCode: "CN", Cidr: []*geodata.CIDR{{Ip: []byte{192, 0, 2, 0}, Prefix: 24}}}}})
	site, _ := proto.Marshal(&geodata.GeoSiteList{Entry: []*geodata.GeoSite{{CountryCode: "CN", Domain: []*geodata.Domain{{Type: geodata.Domain_RootDomain, Value: "example.invalid"}}}}})
	meta := map[string]any{"tag_name": "20261025", "assets": []map[string]string{{"name": "geoip.dat"}, {"name": "geosite.dat"}, {"name": "geoip.dat.sha256sum"}, {"name": "geosite.dat.sha256sum"}}}
	data, _ := json.Marshal(meta)
	files := map[string][]byte{"https://api.github.com/repos/Loyalsoldier/v2ray-rules-dat/releases/latest": data}
	base := "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/20261025/"
	for name, content := range map[string][]byte{"geoip.dat": ip, "geosite.dat": site} {
		hash := sha256.Sum256(content)
		files[base+name] = content
		files[base+name+".sha256sum"] = []byte(hex.EncodeToString(hash[:]) + "  " + name + "\n")
	}
	m.fetch = fixtureFetch(files)
	return files
}

func TestGeodataMetadataCheckIsBoundedCachedAndDoesNotUpdateRules(t *testing.T) {
	m := maintenanceFixture(t)
	files := geodataFixture(t, m)
	now := m.now()
	m.now = func() time.Time { return now }
	requests := 0
	m.fetch = func(ctx context.Context, address, dest string, limit int64) error {
		requests++
		if address != "https://api.github.com/repos/Loyalsoldier/v2ray-rules-dat/releases/latest" {
			t.Fatalf("unexpected data download: %s", address)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 13*time.Second {
			t.Fatal("metadata request is not bounded")
		}
		return fixtureFetch(files)(ctx, address, dest, limit)
	}
	m.graph = func(context.Context, string, any) error { t.Fatal("metadata check called proxy API"); return nil }
	m.run = func(context.Context, ...string) ([]byte, error) {
		t.Fatal("metadata check started a service/command")
		return nil, nil
	}
	for _, name := range []string{"wing.db", "config.dae"} {
		os.WriteFile(filepath.Join(m.configDir, name), []byte("preserve-"+name), 0600)
	}
	global, err := maintenanceLock(m.lockFile, false)
	if err != nil {
		t.Fatal(err)
	}
	defer global.Close()
	install, err := maintenanceLock(m.installLock, false)
	if err != nil {
		t.Fatal(err)
	}
	defer install.Close()
	first := m.checkGeodataLatest(context.Background())
	if !first.Success || first.GeodataCheck == nil || first.GeodataCheck.Tag != "20261025" || first.GeodataCheck.Code != "" {
		t.Fatalf("unexpected metadata: %+v", first)
	}
	second := m.checkGeodataLatest(context.Background())
	if requests != 1 || second.GeodataCheck.Checked != first.GeodataCheck.Checked {
		t.Fatalf("duplicate fetch: %d", requests)
	}
	for _, name := range []string{"wing.db", "config.dae"} {
		body, _ := os.ReadFile(filepath.Join(m.configDir, name))
		if string(body) != "preserve-"+name {
			t.Fatal("user data changed")
		}
	}
	for _, name := range []string{filepath.Join(m.configDir, "geodata"), filepath.Join(m.stateDir, "job.json"), filepath.Join(m.stateDir, "latest.json")} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatalf("unexpected maintenance output: %s", name)
		}
	}
	now = now.Add(31 * time.Second)
	m.fetch = func(context.Context, string, string, int64) error { return errors.New("download_tls") }
	failed := m.checkGeodataLatest(context.Background())
	if failed.GeodataCheck.Code != "download_tls" || failed.GeodataCheck.Tag != "" || m.status().GeodataCheck.Code != "download_tls" {
		t.Fatal("failed check retained an old success")
	}
	now = now.Add(4 * time.Second)
	m.fetch = fixtureFetch(files)
	if recovered := m.checkGeodataLatest(context.Background()); recovered.GeodataCheck.Code != "" || recovered.GeodataCheck.Tag != "20261025" {
		t.Fatal("metadata retry did not recover")
	}
	// Clock corrections invalidate cache instead of suppressing checks indefinitely.
	now = now.Add(-time.Hour)
	files["https://api.github.com/repos/Loyalsoldier/v2ray-rules-dat/releases/latest"] = []byte(`{"tag_name":"20261026","assets":[]}`)
	if invalid := m.checkGeodataLatest(context.Background()); invalid.GeodataCheck.Code != "invalid_release" || invalid.GeodataCheck.Tag != "" {
		t.Fatal("invalid metadata accepted")
	}
	os.WriteFile(filepath.Join(m.configDir, "geoip.dat"), []byte("custom"), 0600)
	if !m.status().GeodataCustom {
		t.Fatal("custom data source not detected")
	}
}

func TestGeodataMetadataLockCancellation(t *testing.T) {
	m := maintenanceFixture(t)
	lock, err := maintenanceLock(m.lockFile+".geodata-check", false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	m.fetch = func(context.Context, string, string, int64) error { t.Fatal("fetch must not bypass lock"); return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := m.checkGeodataLatest(ctx)
	if result.GeodataCheck.Code != "download_timeout" || time.Since(start) > time.Second {
		t.Fatal("metadata check did not cancel promptly")
	}
}

func TestGeodataMetadataRejectsEveryMissingRequiredAsset(t *testing.T) {
	for _, missing := range []string{"geoip.dat", "geosite.dat", "geoip.dat.sha256sum", "geosite.dat.sha256sum"} {
		t.Run(missing, func(t *testing.T) {
			m := maintenanceFixture(t)
			files := geodataFixture(t, m)
			address := "https://api.github.com/repos/Loyalsoldier/v2ray-rules-dat/releases/latest"
			var release githubRelease
			json.Unmarshal(files[address], &release)
			kept := release.Assets[:0]
			for _, asset := range release.Assets {
				if asset.Name != missing {
					kept = append(kept, asset)
				}
			}
			release.Assets = kept
			files[address], _ = json.Marshal(release)
			if s := m.checkGeodataLatest(context.Background()); s.GeodataCheck.Code != "invalid_release" {
				t.Fatalf("accepted missing asset: %s", missing)
			}
		})
	}
}

func TestMaintenanceGeodataAtomicActivationAndFailurePaths(t *testing.T) {
	for _, mode := range []string{"success", "download_failed", "invalid_geodata", "custom_geodata", "apply_failed", "apply_unconfirmed", "pending_configuration"} {
		t.Run(mode, func(t *testing.T) {
			m := maintenanceFixture(t)
			files := geodataFixture(t, m)
			directory := filepath.Join(m.configDir, "geodata")
			os.Mkdir(directory, 0700)
			old := filepath.Join(directory, "data-1")
			os.Mkdir(old, 0700)
			for _, name := range []string{"geoip.dat", "geosite.dat"} {
				os.WriteFile(filepath.Join(old, name), []byte("old-"+name), 0600)
			}
			os.Symlink("data-1", filepath.Join(directory, "current"))
			base := "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/20261025/"
			if mode == "download_failed" {
				delete(files, base+"geosite.dat")
			}
			if mode == "invalid_geodata" {
				html := []byte("<html>error</html>")
				hash := sha256.Sum256(html)
				files[base+"geosite.dat"] = html
				files[base+"geosite.dat.sha256sum"] = []byte(hex.EncodeToString(hash[:]) + "  geosite.dat\n")
			}
			if mode == "custom_geodata" {
				os.WriteFile(filepath.Join(m.configDir, "geoip.dat"), []byte("custom"), 0600)
			}
			activated := false
			m.graph = func(_ context.Context, query string, out any) error {
				if strings.HasPrefix(query, "query") {
					data := []byte(fmt.Sprintf(`{"general":{"dae":{"running":true,"modified":%t}}}`, mode == "pending_configuration"))
					return json.Unmarshal(data, out)
				}
				if !strings.Contains(query, "activateGeodata(") || strings.Contains(query, "run(") {
					t.Fatal("ordinary config apply used")
				}
				target := strings.Split(strings.Split(query, `target:"`)[1], `"`)[0]
				if _, err := os.ReadFile(filepath.Join(directory, target, "geoip.dat")); err != nil {
					t.Fatal("activation before both downloads completed")
				}
				if _, err := os.ReadFile(filepath.Join(directory, target, "geosite.dat")); err != nil {
					t.Fatal("partial pair")
				}
				activated = true
				if mode == "apply_failed" {
					return errors.New("apply_failed")
				}
				if err := switchGeodata(directory, target); err != nil {
					return err
				}
				if mode != "apply_unconfirmed" {
					if err := writeMaintenanceJSON(filepath.Join(directory, "activation.json"), maintenanceGeodataReceipt{target, "data-1", "applied"}); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := writeMaintenanceJSON(filepath.Join(directory, "activation.json"), maintenanceGeodataReceipt{target, "data-1", "applying"}); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "apply_unconfirmed" {
					return errors.New("backend_unavailable")
				}
				return nil
			}
			result := runFixtureJob(t, m, "geodata")
			current, _ := os.Readlink(filepath.Join(directory, "current"))
			if mode == "success" || mode == "pending_configuration" {
				if result.Job.State != "success" || current == "data-1" || !activated {
					t.Fatalf("%+v", result.Job)
				}
			} else {
				if result.Job.State != "error" || result.Job.Code != mode {
					t.Fatalf("wanted %s, got %+v", mode, result.Job)
				}
				if mode != "apply_unconfirmed" && current != "data-1" {
					t.Fatal("old pair lost after failure")
				}
				if mode == "apply_unconfirmed" {
					if _, err := os.Stat(filepath.Join(directory, "current", "geosite.dat")); err != nil {
						t.Fatal("deleted uncertain in-flight batch")
					}
				}
			}
			for _, name := range []string{"geoip.dat", "geosite.dat"} {
				data, _ := os.ReadFile(filepath.Join(old, name))
				if string(data) != "old-"+name {
					t.Fatal("old asset changed")
				}
			}
		})
	}
}

type maintenanceTransport func(*http.Request) (*http.Response, error)

func (f maintenanceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func downloadResponse(request *http.Request, status int, text string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(text)), ContentLength: int64(len(text)), Request: request}
}

func TestMaintenanceCheckUsesOfficialFeedWhenAPILimited(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	const base = "https://github.com/slobys/NaiLink/releases"
	feed := `<feed xmlns="http://www.w3.org/2005/Atom">
		<entry><link rel="alternate" href="` + base + `/tag/openwrt-v0.6.9"/></entry>
		<entry><link rel="alternate" href="` + base + `/tag/openwrt-v0.6.33"/></entry>
		<entry><link rel="alternate" href="https://github.com/elsewhere/NaiLink/releases/tag/openwrt-v99.0.0"/></entry>
	</feed>`
	for _, valid := range []bool{true, false} {
		t.Run(strconv.FormatBool(valid), func(t *testing.T) {
			m := maintenanceFixture(t)
			sums := strings.Repeat("a", 64) + "  install.sh\n"
			if valid {
				sums += strings.Repeat("b", 64) + "  manifest.tsv\n"
			}
			http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
				switch r.URL.String() {
				case "https://api.github.com/repos/slobys/NaiLink/releases?per_page=10":
					return downloadResponse(r, http.StatusForbidden, "API limit"), nil
				case base + ".atom":
					return downloadResponse(r, http.StatusOK, feed), nil
				case base + "/download/openwrt-v0.6.33/SHA256SUMS":
					return downloadResponse(r, http.StatusOK, sums), nil
				default:
					t.Fatalf("unexpected request: %s", r.URL)
					return nil, errors.New("unexpected request")
				}
			})
			result := runFixtureJob(t, m, "check")
			if valid {
				if result.Job.State != "success" || result.Latest == nil || result.Latest.Tag != "openwrt-v0.6.33" {
					t.Fatalf("fallback did not discover release: %+v", result)
				}
			} else if result.Job.State != "error" || result.Job.Code != "invalid_release" || result.Latest != nil {
				t.Fatalf("incomplete checksum manifest accepted: %+v", result)
			}
		})
	}
}

func TestGeodataCheckUsesOfficialRedirectWhenAPILimited(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, target := range []string{geodataReleases + "/tag/20261025", "https://github.com/elsewhere/v2ray-rules-dat/releases/tag/20261025"} {
		m := maintenanceFixture(t)
		http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case "https://api.github.com/repos/Loyalsoldier/v2ray-rules-dat/releases/latest":
				return downloadResponse(r, http.StatusTooManyRequests, "API limit"), nil
			case geodataReleases + "/latest":
				response := downloadResponse(r, http.StatusFound, "")
				response.Header.Set("Location", target)
				return response, nil
			default:
				t.Fatalf("followed redirect or downloaded data during check: %s", r.URL)
				return nil, errors.New("unexpected request")
			}
		})
		check := m.checkGeodataLatest(context.Background()).GeodataCheck
		if target == geodataReleases+"/tag/20261025" {
			if check.Tag != "20261025" || check.Code != "" {
				t.Fatalf("fallback did not discover rules: %+v", check)
			}
		} else if check.Tag != "" || check.Code != "invalid_release" {
			t.Fatalf("accepted unrelated release redirect: %+v", check)
		}
	}
}

func TestMaintenanceOfficialAssetFallback(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	address := "https://github.com/slobys/NaiLink/releases/download/openwrt-v0.6.49/install.sh"
	for _, test := range []struct{ name, metadata, code string }{
		{"valid", `{"tag_name":"openwrt-v0.6.49","draft":false,"assets":[{"id":42,"name":"install.sh","state":"uploaded","url":"https://untrusted.test/ignored"}]}`, ""},
		{"wrong tag", `{"tag_name":"openwrt-v0.6.48"}`, "download_invalid"},
		{"draft", `{"tag_name":"openwrt-v0.6.49","draft":true}`, "download_invalid"},
		{"missing", `{"tag_name":"openwrt-v0.6.49","assets":[]}`, "download_asset_missing"},
		{"duplicate", `{"tag_name":"openwrt-v0.6.49","assets":[{"id":42,"name":"install.sh","state":"uploaded"},{"id":43,"name":"install.sh","state":"uploaded"}]}`, "download_invalid"},
		{"unpublished", `{"tag_name":"openwrt-v0.6.49","assets":[{"id":42,"name":"install.sh","state":"new"}]}`, "download_invalid"},
		{"invalid", `<html>blocked</html>`, "download_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := []string{}
			http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.URL.String())
				switch r.URL.String() {
				case address:
					return downloadResponse(r, 503, "unavailable"), nil
				case "https://api.github.com/repos/slobys/NaiLink/releases/tags/openwrt-v0.6.49":
					return downloadResponse(r, 200, test.metadata), nil
				case "https://api.github.com/repos/slobys/NaiLink/releases/assets/42":
					if r.Header.Get("Accept") != "application/octet-stream" {
						t.Fatal("API asset was requested as JSON")
					}
					return downloadResponse(r, 302, ""), nil
				case "https://release-assets.githubusercontent.com/fixture":
					return downloadResponse(r, 200, "verified asset"), nil
				default:
					t.Fatal("unexpected download origin", r.URL)
					return nil, errors.New("unexpected")
				}
			})
			base := http.DefaultTransport
			http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
				response, err := base.RoundTrip(r)
				if response != nil && response.StatusCode == 302 {
					response.Header.Set("Location", "https://release-assets.githubusercontent.com/fixture")
				}
				return response, err
			})
			destination := filepath.Join(t.TempDir(), "asset")
			os.WriteFile(destination, []byte("existing"), 0600)
			err := maintenanceDownload(context.Background(), address, destination, 512<<10)
			data, _ := os.ReadFile(destination)
			if test.code == "" {
				if err != nil || string(data) != "verified asset" {
					t.Fatal(err, string(data))
				}
			} else if err == nil || downloadFailureCode(err) != test.code || string(data) != "existing" {
				t.Fatal("unsafe failed download", err, string(data))
			}
			if len(calls) > 5 {
				t.Fatal("unbounded retries", calls)
			}
		})
	}
}

type interruptedDownload struct{ sent bool }

func (b *interruptedDownload) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, "partial"), nil
	}
	return 0, io.ErrUnexpectedEOF
}
func (*interruptedDownload) Close() error { return nil }

func TestMaintenanceRetriesReplaceOnlyCompleteDownloads(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, recover := range []bool{false, true} {
		t.Run(fmt.Sprint(recover), func(t *testing.T) {
			calls := 0
			http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if recover && calls == 2 {
					return downloadResponse(r, 200, "complete"), nil
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: &interruptedDownload{}, ContentLength: -1, Request: r}, nil
			})
			directory := t.TempDir()
			destination := filepath.Join(directory, "asset")
			os.WriteFile(destination, []byte("existing"), 0600)
			err := maintenanceDownload(context.Background(), "https://api.github.com/fixture", destination, 100)
			data, _ := os.ReadFile(destination)
			if recover {
				if err != nil || string(data) != "complete" {
					t.Fatal(err, string(data))
				}
			} else if downloadFailureCode(err) != "download_connection" || string(data) != "existing" {
				t.Fatal(err, string(data))
			}
			files, _ := os.ReadDir(directory)
			if calls != 2 || len(files) != 1 {
				t.Fatal("retry/temporary file leak", calls, files)
			}
		})
	}
}

func TestMaintenanceDownloadErrorsAreSafeAndSpecific(t *testing.T) {
	if code := maintenanceCode(context.DeadlineExceeded); strings.HasPrefix(code, "download_") {
		t.Fatal("non-download timeout was mislabeled", code)
	}
	for _, test := range []struct {
		err  error
		code string
	}{
		{&url.Error{Op: "Get", URL: "https://private.test/?token=secret", Err: &net.DNSError{Err: "secret", Name: "private.test"}}, "download_dns"},
		{context.DeadlineExceeded, "download_timeout"},
		{io.ErrUnexpectedEOF, "download_connection"},
		{&url.Error{Err: errors.New("download_redirect")}, "download_redirect"},
		{errors.New("https://private.test/?token=secret"), "download_failed"},
	} {
		if code := maintenanceCode(errors.New(downloadFailureCode(test.err))); code != test.code {
			t.Fatal(code, test.code)
		}
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for status, code := range map[int]string{403: "download_rate_limited", 429: "download_rate_limited", 404: "download_asset_missing", 500: "download_http"} {
		http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) { return downloadResponse(r, status, "not an asset"), nil })
		if err := maintenanceDownload(context.Background(), "https://api.github.com/fixture", filepath.Join(t.TempDir(), "asset"), 100); downloadFailureCode(err) != code {
			t.Fatal(status, err)
		}
	}
}
func TestMaintenanceLargeTransferBudgetAndCancellation(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, test := range []struct {
		name             string
		limit            int64
		minimum, maximum time.Duration
	}{
		{"metadata", 512 << 10, 2 * time.Minute, 3 * time.Minute},
		{"optional core", 128 << 20, 9 * time.Minute, 10 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				remaining := time.Until(deadline)
				if !ok || remaining < test.minimum || remaining > test.maximum {
					t.Fatal("incorrect transfer budget", remaining)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("verified by caller")), Request: r}, nil
			})
			if err := maintenanceDownload(context.Background(), "https://github.com/test", filepath.Join(t.TempDir(), "download"), test.limit); err != nil {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	name := filepath.Join(t.TempDir(), "cancelled")
	if maintenanceDownload(ctx, "https://github.com/test", name, 128<<20) == nil {
		t.Fatal("caller cancellation ignored")
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("cancelled download changed destination")
	}
}
func TestMaintenanceDownloadBoundsOriginsAndOutput(t *testing.T) {
	for _, address := range []string{"http://github.com/test", "https://evil.test/test", "https://github.com:444/test", "https://user@github.com/test"} {
		if maintenanceDownload(context.Background(), address, filepath.Join(t.TempDir(), "x"), 10) == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 50))), ContentLength: -1, Request: r}, nil
	})
	name := filepath.Join(t.TempDir(), "download")
	if maintenanceDownload(context.Background(), "https://github.com/test", name, 10) == nil {
		t.Fatal("oversize download accepted")
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("partial file retained")
	}
	http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://evil.test"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if maintenanceDownload(context.Background(), "https://github.com/test", name, 10) == nil {
		t.Fatal("insecure redirect accepted")
	}
	var out boundedOutput
	out.Write([]byte(strings.Repeat("a", 100000)))
	out.Write([]byte("last error"))
	if len(out.data) != 65536 || !strings.HasSuffix(string(out.data), "last error") {
		t.Fatal("unbounded installer output")
	}
	if maintenanceCode(errors.New("password=private")) != "operation_failed" {
		t.Fatal("raw error leaked")
	}
}

func TestMaintenancePublishedGeodata(t *testing.T) {
	directory := os.Getenv("DAED_TEST_GEODATA_DIR")
	if directory == "" {
		t.Skip("Set DAED_TEST_GEODATA_DIR for checksum-verified upstream data")
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		sums, err := os.ReadFile(filepath.Join(directory, name+".sha256sum"))
		if err != nil {
			t.Fatal(err)
		}
		digest, err := checksumFor(sums, name)
		if err != nil {
			t.Fatal(err)
		}
		if err = verifyChecksum(filepath.Join(directory, name), digest); err != nil {
			t.Fatal(err)
		}
		if err = validateGeodata(filepath.Join(directory, name), name); err != nil {
			t.Fatalf("upstream %s: %v", name, err)
		}
	}
}

func TestMaintenanceClockCorrectionAndGeodataReceipt(t *testing.T) {
	m := maintenanceFixture(t)
	job := maintenanceJob{ID: strings.Repeat("a", 24), Action: "check", State: "queued", Updated: m.now().Unix() + 3600}
	writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), job)
	if got := m.status(); got.Job.Code != "interrupted" {
		t.Fatal("future queue never recovered")
	}
	directory := filepath.Join(m.configDir, "geodata")
	os.Mkdir(directory, 0700)
	for _, batch := range []string{"data-1", "data-2"} {
		os.Mkdir(filepath.Join(directory, batch), 0700)
		writeMaintenanceJSON(filepath.Join(directory, batch, "version.json"), releaseInfo{batch, batch, 1})
	}
	os.Symlink("data-2", filepath.Join(directory, "current"))
	job.Action, job.State, job.Code, job.Batch = "geodata", "error", "apply_unconfirmed", "data-2"
	writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), job)
	receipt := maintenanceGeodataReceipt{"data-2", "data-1", "applying"}
	writeMaintenanceJSON(filepath.Join(directory, "activation.json"), receipt)
	got := m.status()
	if got.Job.Code != "apply_unconfirmed" || got.Geodata.Version != "data-1" {
		t.Fatal("unconfirmed batch shown as active")
	}
	if m.start("check").Code != "apply_unconfirmed" {
		t.Fatal("in-flight activation overwritten")
	}
	receipt.State = "applied"
	writeMaintenanceJSON(filepath.Join(directory, "activation.json"), receipt)
	got = m.status()
	if got.Job.State != "success" || got.Job.Code != "recovered" || got.Geodata.Version != "data-2" {
		t.Fatal("lost success response did not recover")
	}
	job.State, job.Code = "error", "apply_unconfirmed"
	writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), job)
	receipt.State = "rolled_back"
	writeMaintenanceJSON(filepath.Join(directory, "activation.json"), receipt)
	if got = m.status(); got.Job.Code != "apply_failed" {
		t.Fatal("rollback was reported as success")
	}
}

func TestMaintenanceCommandAllowsRecoveryTrapAndStopsChildren(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "installer")
	os.WriteFile(script, []byte("#!/bin/sh\ntrap 'echo restored > \"$1/recovered\"; exit 1' TERM\nsleep 60 &\necho $! > \"$1/child\"\nwait\n"), 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	_, err := maintenanceCommand(ctx, "/bin/sh", script, directory)
	if err == nil {
		t.Fatal("interruption lost")
	}
	if _, err = os.Stat(filepath.Join(directory, "recovered")); err != nil {
		t.Fatal("installer recovery trap was killed")
	}
	data, _ := os.ReadFile(filepath.Join(directory, "child"))
	var pid int
	fmt.Sscanf(string(data), "%d", &pid)
	if pid <= 0 {
		t.Fatal("missing child")
	}
	data, err = os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err == nil && !strings.Contains(string(data), "State:\tZ") && syscall.Kill(pid, 0) == nil {
		t.Fatal("orphan installer child is still running")
	}
}
