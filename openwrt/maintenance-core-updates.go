// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
)

const coreCatalogURL = "https://raw.githubusercontent.com/slobys/NaiLink/main/openwrt/core-updates.json"

type coreUpdateAsset struct {
	Archive string `json:"archive"`
	SHA256  string `json:"sha256"`
}
type coreUpdateRelease struct {
	Version    string                     `json:"version"`
	MinAdapter int                        `json:"min_adapter"`
	MaxAdapter int                        `json:"max_adapter"`
	Assets     map[string]coreUpdateAsset `json:"assets"`
}
type coreUpdateCatalog struct {
	Schema  int                          `json:"schema"`
	Release string                       `json:"release"`
	Engines map[string]coreUpdateRelease `json:"engines"`
}
type checkedCoreUpdate struct {
	Adapter    int             `json:"adapter"`
	MinAdapter int             `json:"min_adapter"`
	MaxAdapter int             `json:"max_adapter"`
	Version    string          `json:"version,omitempty"`
	Checked    int64           `json:"checked"`
	Code       string          `json:"code,omitempty"`
	Release    string          `json:"release,omitempty"`
	Asset      coreUpdateAsset `json:"asset"`
}

var coreReleaseTag = regexp.MustCompile(`^nailink-cores-[0-9]{8}(?:-[0-9]+)?$`)
var coreArchive = regexp.MustCompile(`^nailink-(?:sing-box|mihomo)-[0-9]+\.[0-9]+\.[0-9]+(?:-nailink\.[0-9]+)?-(?:x86_64|arm64)\.tar\.gz$`)

func newerCoreVersion(next, current string) bool {
	parse := func(v string) []int {
		values := strings.Split(strings.Replace(v, "-nailink.", ".", 1), ".")
		parts := make([]int, 4)
		if len(values) < 3 || len(values) > 4 {
			return nil
		}
		for i, s := range values {
			n, e := strconv.Atoi(s)
			if e != nil || n < 0 {
				return nil
			}
			parts[i] = n
		}
		return parts
	}
	a, b := parse(next), parse(current)
	if a == nil || b == nil {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func updateEngine(action string) string {
	if strings.HasSuffix(action, "_singbox") {
		return "sing-box"
	}
	if strings.HasSuffix(action, "_mihomo") {
		return "mihomo"
	}
	return ""
}
func (m *maintenanceManager) updatePath(engine string) string {
	return filepath.Join(m.stateDir, "core-update-"+engine+".json")
}
func coreUpdateFresh(c checkedCoreUpdate, now int64) bool {
	return c.Checked > 0 && now-c.Checked < 3600 && c.Checked <= now+60
}
func (m *maintenanceManager) checkedUpdate(engine string) *checkedCoreUpdate {
	var c checkedCoreUpdate
	if readMaintenanceJSON(m.updatePath(engine), &c) != nil {
		return nil
	}
	// Asset locations are an internal cache, not a front-end download API.
	c.Asset = coreUpdateAsset{}
	return &c
}
func (m *maintenanceManager) installedCoreVersion(engine string) string {
	binary, args := m.corePath(), []string{"version"}
	if engine == "mihomo" {
		binary, args = m.mihomoPath(), []string{"-v"}
	}
	st, err := os.Lstat(binary)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Mode().Perm()&0111 == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := m.run(ctx, append([]string{binary}, args...)...)
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(output))
	if engine == "sing-box" && len(fields) >= 3 && fields[0] == "sing-box" && fields[1] == "version" && routercore.ValidCoreVersion(engine, fields[2]) {
		expected, e := routercore.ExpectedCoreVersion(binary, engine)
		if e == nil && expected == fields[2] {
			return fields[2]
		}
	}
	if engine == "mihomo" && len(fields) >= 3 && fields[0] == "Mihomo" && fields[1] == "Meta" && strings.HasPrefix(fields[2], "v") && routercore.ValidCoreVersion(engine, fields[2][1:]) {
		expected, e := routercore.ExpectedCoreVersion(binary, engine)
		if e == nil && expected == fields[2][1:] {
			return fields[2][1:]
		}
	}
	return ""
}
func validateCoreCatalog(c coreUpdateCatalog, engine, arch string) (checkedCoreUpdate, error) {
	if c.Schema != 1 || !coreReleaseTag.MatchString(c.Release) {
		return checkedCoreUpdate{}, errors.New("invalid_release")
	}
	r, ok := c.Engines[engine]
	if !ok || !routercore.ValidCoreVersion(engine, r.Version) {
		return checkedCoreUpdate{}, errors.New("invalid_release")
	}
	if r.MinAdapter < 1 || r.MaxAdapter < r.MinAdapter {
		return checkedCoreUpdate{}, errors.New("invalid_release")
	}
	if routercore.AdapterRevision < r.MinAdapter || routercore.AdapterRevision > r.MaxAdapter {
		return checkedCoreUpdate{}, errors.New("core_update_plugin_required")
	}
	a, ok := r.Assets[arch]
	cpu := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[arch]
	if !ok || cpu == "" {
		return checkedCoreUpdate{}, errors.New("unsupported")
	}
	if !coreArchive.MatchString(a.Archive) || a.Archive != "nailink-"+engine+"-"+r.Version+"-"+cpu+".tar.gz" || !routercore.ValidRevision(a.SHA256) {
		return checkedCoreUpdate{}, errors.New("invalid_release")
	}
	return checkedCoreUpdate{Version: r.Version, Release: c.Release, Asset: a, Adapter: routercore.AdapterRevision, MinAdapter: r.MinAdapter, MaxAdapter: r.MaxAdapter}, nil
}
func (m *maintenanceManager) checkCoreUpdate(ctx context.Context, job *maintenanceJob, engine string) error {
	c := checkedCoreUpdate{Checked: m.now().Unix()}
	data, err := m.releaseData(ctx, coreCatalogURL, 32768)
	if err == nil {
		var catalog coreUpdateCatalog
		if json.Unmarshal(data, &catalog) != nil {
			err = errors.New("invalid_release")
		} else {
			c, err = validateCoreCatalog(catalog, engine, runtime.GOARCH)
			c.Checked = m.now().Unix()
		}
	}
	if err != nil {
		c = checkedCoreUpdate{Checked: m.now().Unix(), Code: maintenanceCode(err)}
	}
	if saved := writeMaintenanceJSON(m.updatePath(engine), c); saved != nil {
		return errors.New("state_failed")
	}
	job.Target = engine
	return err
}
func (m *maintenanceManager) updateOptionalCore(ctx context.Context, job *maintenanceJob, engine string) error {
	var c checkedCoreUpdate
	if readMaintenanceJSON(m.updatePath(engine), &c) != nil || c.Adapter != routercore.AdapterRevision || !coreUpdateFresh(c, m.now().Unix()) || c.Code != "" || !coreReleaseTag.MatchString(c.Release) || !routercore.ValidCoreVersion(engine, c.Version) || !routercore.ValidRevision(c.Asset.SHA256) {
		return errors.New("check_required")
	}
	cpu := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[runtime.GOARCH]
	if cpu == "" || c.Asset.Archive != "nailink-"+engine+"-"+c.Version+"-"+cpu+".tar.gz" {
		return errors.New("invalid_release")
	}
	state, err := routercore.ReadState(m.configDir)
	if err != nil {
		return errors.New("unsafe_state")
	}
	if state.Current == engine || state.Phase == "switching" {
		return errors.New("core_update_active")
	}
	binary, args := m.corePath(), []string{"version"}
	if engine == "mihomo" {
		binary, args = m.mihomoPath(), []string{"-v"}
	}
	if current := m.installedCoreVersion(engine); current != "" && !newerCoreVersion(c.Version, current) {
		return errors.New("already_latest")
	}
	if err = privateDirectory(filepath.Dir(binary)); err != nil {
		return errors.New("unsafe_state")
	}
	var stat syscall.Statfs_t
	if syscall.Statfs(filepath.Dir(binary), &stat) != nil || uint64(stat.Bavail)*uint64(stat.Bsize) < 180<<20 {
		return errors.New("no_space")
	}
	directory, err := os.MkdirTemp(m.stateDir, "core-update-")
	if err != nil {
		return errors.New("state_failed")
	}
	defer os.RemoveAll(directory)
	if err = m.save(job, "downloading"); err != nil {
		return err
	}
	archive := filepath.Join(directory, "core.tar.gz")
	address := "https://github.com/slobys/NaiLink/releases/download/" + c.Release + "/" + c.Asset.Archive
	if err = m.fetch(ctx, address, archive, 128<<20); err != nil {
		return errors.New(downloadFailureCode(err))
	}
	// Compressed archives are regular files but not executable; hash directly.
	archiveDigest, err := routercore.BinarySHA256(archive)
	if err != nil {
		return errors.New("download_invalid")
	}
	if archiveDigest != c.Asset.SHA256 {
		return errors.New("checksum_failed")
	}
	if err = m.save(job, "verifying"); err != nil {
		return err
	}
	target, err := os.CreateTemp(filepath.Dir(binary), ".core-update-")
	if err != nil {
		return errors.New("state_failed")
	}
	path := target.Name()
	target.Close()
	defer os.Remove(path)
	if err = extractNativePackage(archive, path, engine); err != nil {
		return err
	}
	if err = validateSingboxELF(path, runtime.GOARCH); err != nil {
		return err
	}
	if err = os.Chmod(path, 0700); err != nil {
		return errors.New("state_failed")
	}
	output, err := m.run(ctx, append([]string{path}, args...)...)
	prefix := "sing-box version " + c.Version + "\n"
	if engine == "mihomo" {
		prefix = "Mihomo Meta v" + c.Version + " "
	}
	if err != nil || !strings.HasPrefix(string(output), prefix) {
		return errors.New("core_update_version_invalid")
	}
	digest, err := routercore.BinarySHA256(path)
	if err != nil {
		return errors.New("invalid_release")
	}
	lock, err := routercore.LockBinary(binary, true)
	if err != nil {
		return errors.New("busy")
	}
	defer lock.Close()
	state, err = routercore.ReadState(m.configDir)
	if err != nil || state.Current == engine || state.Phase == "switching" {
		return errors.New("core_update_active")
	}
	if err = publishCoreUpdate(binary, path, routercore.CoreReceipt{Engine: engine, Version: c.Version, SHA256: digest, MinAdapter: c.MinAdapter, MaxAdapter: c.MaxAdapter}, job.ID); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(m.stateDir, "core-plan.json"))
	return nil
}

func publishCoreUpdate(binary, candidate string, receipt routercore.CoreReceipt, operation string) error {
	return publishCoreUpdateWithWriter(binary, candidate, receipt, operation, routercore.Write)
}
func publishCoreUpdateWithWriter(binary, candidate string, receipt routercore.CoreReceipt, operation string, write func(string, []byte) error) error {
	if receipt.MinAdapter == 0 && receipt.MaxAdapter == 0 {
		receipt.MinAdapter = routercore.AdapterRevision
		receipt.MaxAdapter = routercore.AdapterRevision
	}
	if strings.ContainsAny(operation, "/\\.\x00") {
		return errors.New("unsafe_state")
	}
	backup := binary + ".previous-" + operation
	oldReceipt, receiptErr := routercore.Read(routercore.ReceiptPath(binary), 4096)
	if receiptErr != nil && !os.IsNotExist(receiptErr) {
		return errors.New("unsafe_state")
	}
	hadBinary := false
	if st, err := os.Lstat(binary); err == nil {
		if !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
			return errors.New("unsafe_state")
		}
		if _, e := os.Lstat(backup); !os.IsNotExist(e) {
			return errors.New("unsafe_state")
		}
		if err = os.Rename(binary, backup); err != nil {
			return errors.New("state_failed")
		}
		hadBinary = true
	} else if !os.IsNotExist(err) {
		return errors.New("unsafe_state")
	}
	restore := func() error {
		if hadBinary {
			if err := os.Rename(backup, binary); err != nil {
				return err
			}
		} else {
			if err := os.Remove(binary); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if receiptErr == nil {
			return routercore.Write(routercore.ReceiptPath(binary), oldReceipt)
		}
		if err := os.Remove(routercore.ReceiptPath(binary)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	err := os.Chmod(candidate, 0755)
	if err == nil {
		err = os.Rename(candidate, binary)
	}
	if err == nil {
		data, _ := json.Marshal(receipt)
		err = write(routercore.ReceiptPath(binary), data)
	}
	if err != nil {
		if restore() != nil {
			return errors.New("core_rollback_failed")
		}
		return errors.New("state_failed")
	}
	// Keep one previous executable, replacing only our own older backup.
	if hadBinary {
		previous := binary + ".previous"
		if st, e := os.Lstat(previous); e == nil && !st.Mode().IsRegular() {
			return nil
		}
		_ = os.Rename(backup, previous)
	}
	return nil
}
