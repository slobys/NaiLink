// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/daeuniverse/dae-wing/routercore"
)

var coreMaintenanceCodes = []string{"mihomo_not_installed", "mihomo_version_mismatch", "mihomo_configuration_invalid", "extra_not_installed", "extra_dependency_failed", "extra_rollback_failed", "core_incompatible", "core_configuration_changed", "singbox_not_installed", "singbox_version_mismatch", "singbox_configuration_invalid", "core_environment_unsupported", "core_network_conflict", "core_rolled_back", "core_rollback_failed", "core_cleanup_failed", "core_stop_failed", "core_state_failed", "core_snapshot_failed", "core_start_failed", "core_not_ready", "core_connectivity_failed", "core_dependency_failed", "core_exited", "core_not_running", "nftables_unavailable"}

func coreMaintenanceCode(message string) string {
	for _, code := range []string{"invalid_core_mode", "tproxy_tor_unsupported", "tproxy_environment_unsupported", "core_tun_unavailable", "tproxy_fw4_required", "tproxy_bridge_unsupported", "tproxy_bridge_rpf", "tproxy_bridge_vlan_unsupported"} {
		if strings.Contains(message, code) {
			return code
		}
	}
	if message == "core_request_failed" {
		return message
	}
	for _, code := range []string{"core_configuration_check_timeout", "core_dns_failed", "core_reload_rolled_back", "core_recovery_failed", "core_network_check_failed", "core_network_ownership_failed", "core_process_ownership_failed", "core_ports_unavailable", "invalid_core_request", "core_recovery_snapshot_missing", "core_recovery_snapshot_invalid"} {
		if message == code {
			return code
		}
	}
	for _, code := range coreMaintenanceCodes {
		if message == code {
			return code
		}
	}
	return ""
}

func uncertainCoreJob(job maintenanceJob) bool {
	if !strings.HasPrefix(job.Action, "core_switch_") {
		return false
	}
	return job.Code == "backend_unavailable" || job.Code == "core_request_failed" || job.Code == "interrupted"
}
func coreMaintenanceAction(action string) bool {
	if action == "core_prepare_tproxy" || action == "core_prepare_tun" {
		return true
	}
	for _, suffix := range []string{"_tproxy", "_tun"} {
		if strings.HasSuffix(action, suffix) {
			base := strings.TrimSuffix(action, suffix)
			return base == "core_check_singbox" || base == "core_check_mihomo" || base == "core_switch_singbox" || base == "core_switch_mihomo"
		}
	}
	switch action {
	case "core_check_dae", "core_check_singbox", "core_check_mihomo", "core_install_singbox", "core_install_mihomo", "core_install_extra", "core_switch_dae", "core_switch_singbox", "core_switch_mihomo":
		return true
	}
	return false
}

// Match the durable transaction ID, never infer success from a selected core
// left by an unrelated operation after the worker/HTTP response was lost.
func (m *maintenanceManager) reconcileCoreJob(job *maintenanceJob) {
	state, err := routercore.ReadState(m.configDir)
	if err != nil || state.Operation != job.ID {
		return
	}
	if state.Phase == "switching" {
		job.State, job.Code, job.Phase = "running", "", "switching"
	} else if state.Phase == "stable" && state.Current == job.Target {
		job.State, job.Code = "success", "recovered"
	} else if state.Code != "" {
		job.Code = state.Code
	}
}

type optionalCoreStatus struct {
	Installed bool   `json:"installed"`
	Supported bool   `json:"supported"`
	Version   string `json:"version"`
}
type coreMaintenanceStatus struct {
	Mode         string                        `json:"mode"`
	Engines      map[string]optionalCoreStatus `json:"engines"`
	Current      string                        `json:"current"`
	Phase        string                        `json:"phase"`
	Code         string                        `json:"code,omitempty"`
	Installed    bool                          `json:"installed"`
	Supported    bool                          `json:"supported"`
	Version      string                        `json:"version"`
	Extra        bool                          `json:"extra"`
	ExtraVersion string                        `json:"extra_version"`
	Plan         *routercore.Plan              `json:"plan,omitempty"`
}
type storedCorePlan struct {
	Plan    routercore.Plan `json:"plan"`
	Checked int64           `json:"checked"`
}

func (m *maintenanceManager) corePath() string {
	if m.coreBinary != "" {
		return m.coreBinary
	}
	return routercore.BinaryPath
}
func (m *maintenanceManager) coreStatus() *coreMaintenanceStatus {
	state, err := routercore.ReadState(m.configDir)
	if err != nil {
		return &coreMaintenanceStatus{Current: "unknown", Phase: "failed", Code: "unsafe_state"}
	}
	_, supported := routercore.Releases[runtime.GOARCH]
	_, extraErr := routercore.ExtraDirectory(m.extraPath())
	status := &coreMaintenanceStatus{Extra: extraErr == nil, ExtraVersion: routercore.ExtraVersion, Current: state.Current, Phase: state.Phase, Code: state.Code, Supported: supported, Version: routercore.SingBoxVersion}
	status.Mode = routercore.CoreMode(state.Mode)
	if st, err := os.Lstat(m.corePath()); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0022 == 0 && st.Mode().Perm()&0111 != 0 {
		status.Installed = true
	}
	_, mhSupported := routercore.MihomoReleases[runtime.GOARCH]
	mhInstalled := false
	if st, e := os.Lstat(m.mihomoPath()); e == nil && st.Mode().IsRegular() && st.Mode().Perm()&0022 == 0 && st.Mode().Perm()&0111 != 0 {
		mhInstalled = true
	}
	status.Engines = map[string]optionalCoreStatus{"sing-box": {status.Installed, supported, routercore.SingBoxVersion}, "mihomo": {mhInstalled, mhSupported, routercore.MihomoVersion}}
	var saved storedCorePlan
	if readMaintenanceJSON(filepath.Join(m.stateDir, "core-plan.json"), &saved) == nil && m.now().Unix()-saved.Checked <= 600 && saved.Checked <= m.now().Unix()+60 {
		status.Plan = &saved.Plan
	}
	return status
}
func (m *maintenanceManager) maintainCore(ctx context.Context, job *maintenanceJob) error {
	lock, err := maintenanceLock(m.installLock, false)
	if err != nil {
		return errors.New("busy")
	}
	defer lock.Close()
	if job.Action == "core_prepare_tproxy" || job.Action == "core_prepare_tun" {
		return m.prepareCoreDependency(ctx, job, strings.TrimPrefix(job.Action, "core_prepare_"))
	}
	if job.Action == "core_install_extra" {
		return m.installExtra(ctx, job)
	}
	if job.Action == "core_install_mihomo" {
		return m.installOptionalCore(ctx, job, "mihomo")
	}
	if job.Action == "core_install_singbox" {
		return m.installSingBox(ctx, job)
	}
	action, mode := job.Action, ""
	for _, suffix := range []string{"_tproxy", "_tun"} {
		if strings.HasSuffix(action, suffix) {
			mode = strings.TrimPrefix(suffix, "_")
			action = strings.TrimSuffix(action, suffix)
			break
		}
	}
	target := "dae"
	if strings.HasSuffix(action, "singbox") {
		target = "sing-box"
	} else if strings.HasSuffix(action, "mihomo") {
		target = "mihomo"
	}
	job.Target = target
	modeArgument := ""
	if mode != "" {
		modeArgument = `,mode:"` + mode + `"`
	}
	if strings.HasPrefix(job.Action, "core_check_") {
		var reply struct {
			CorePlan string `json:"corePlan"`
		}
		query := `{corePlan(target:"` + target + `"` + modeArgument + `)}`
		if err = m.graph(ctx, query, &reply); err != nil {
			if err.Error() == "apply_failed" {
				return errors.New("core_request_failed")
			}
			return err
		}
		var plan routercore.Plan
		if json.Unmarshal([]byte(reply.CorePlan), &plan) != nil || plan.Target != target || mode != "" && plan.Mode != mode || !routercore.ValidRevision(plan.Revision) {
			return errors.New("backend_unavailable")
		}
		if err = writeMaintenanceJSON(filepath.Join(m.stateDir, "core-plan.json"), storedCorePlan{plan, m.now().Unix()}); err != nil {
			return errors.New("state_failed")
		}
		// A compatibility report is useful even when it finds blocking problems.
		return nil
	}
	var saved storedCorePlan
	if readMaintenanceJSON(filepath.Join(m.stateDir, "core-plan.json"), &saved) != nil || saved.Plan.Target != target || mode != "" && saved.Plan.Mode != mode || !saved.Plan.Compatible || !routercore.ValidRevision(saved.Plan.Revision) || m.now().Unix()-saved.Checked > 600 || saved.Checked > m.now().Unix()+60 {
		return errors.New("check_required")
	}
	if err = m.save(job, "switching"); err != nil {
		return err
	}
	var result struct {
		SwitchCore int `json:"switchCore"`
	}
	query := `mutation {switchCore(target:"` + target + `",revision:"` + saved.Plan.Revision + `",operation:"` + job.ID + `"` + modeArgument + `)}`
	if err = m.graph(ctx, query, &result); err != nil {
		if err.Error() == "apply_failed" {
			return errors.New("core_request_failed")
		}
		return err
	}
	if result.SwitchCore != 1 {
		return errors.New("core_not_ready")
	}
	return nil
}

// Installing a matching module does not stop the selected engine. Refuse a
// solver plan that replaces existing packages (especially the running kernel).
func (m *maintenanceManager) prepareCoreDependency(ctx context.Context, job *maintenanceJob, mode string) error {
	pkg, module := "kmod-tun", "tun"
	var extra []string
	if mode == "tproxy" {
		pkg, module = "kmod-nft-tproxy", "nft_tproxy"
		extra = []string{"kmod-nft-bridge"}
	} else if mode != "tun" {
		return errors.New("invalid_core_mode")
	}
	if err := m.save(job, "dependencies"); err != nil {
		return err
	}
	manager, check, install := "", []string{}, []string{}
	if _, err := exec.LookPath("opkg"); err == nil {
		manager = "opkg"
		check = []string{"opkg", "--noaction", "install", pkg}
		install = []string{"opkg", "install", pkg}
	} else if _, err = exec.LookPath("apk"); err == nil {
		manager = "apk"
		check = []string{"apk", "add", "--simulate", pkg}
		install = []string{"apk", "add", pkg}
	}
	if manager == "" {
		return errors.New("core_dependency_failed")
	}
	check = append(check, extra...)
	install = append(install, extra...)
	plan, err := m.run(ctx, check...)
	if err != nil || !safeCoreDependencyPlan(plan) {
		return errors.New("core_dependency_failed")
	}
	if _, err = m.run(ctx, install...); err != nil {
		return errors.New("core_dependency_failed")
	}
	if _, err = exec.LookPath("modprobe"); err == nil {
		if _, err = m.run(ctx, "modprobe", module); err != nil {
			return errors.New("core_dependency_failed")
		}
		if mode == "tproxy" {
			if _, err = m.run(ctx, "modprobe", "nft_meta_bridge"); err != nil {
				return errors.New("core_dependency_failed")
			}
		}
	}
	return nil
}
func safeCoreDependencyPlan(plan []byte) bool {
	text := strings.ToLower(string(plan))
	for _, word := range []string{"upgrading ", "downgrading ", "purging ", "removing ", "replacing ", "reinstalling ", "installing kernel ", "installing kernel("} {
		if strings.Contains(text, word) {
			return false
		}
	}
	return true
}
func (m *maintenanceManager) mihomoPath() string {
	if m.mihomoBinary != "" {
		return m.mihomoBinary
	}
	return routercore.MihomoBinaryPath
}
func (m *maintenanceManager) installSingBox(ctx context.Context, job *maintenanceJob) error {
	return m.installOptionalCore(ctx, job, "sing-box")
}
func (m *maintenanceManager) installOptionalCore(ctx context.Context, job *maintenanceJob, engine string) error {
	binary, version, releaseTag := m.corePath(), routercore.SingBoxVersion, routercore.SingBoxRelease
	releases := routercore.Releases
	versionArgs := []string{"version"}
	prefix := "sing-box version " + version + "\n"
	versionError := "singbox_version_mismatch"
	if engine == "mihomo" {
		binary, version, releaseTag = m.mihomoPath(), routercore.MihomoVersion, routercore.MihomoRelease
		releases = routercore.MihomoReleases
		versionArgs = []string{"-v"}
		prefix = "Mihomo Meta v" + version + " "
		versionError = "mihomo_version_mismatch"
	}

	release, ok := releases[runtime.GOARCH]
	if !ok {
		return errors.New("unsupported")
	}
	state, err := routercore.ReadState(m.configDir)
	if err != nil {
		return errors.New("unsafe_state")
	}
	if state.Current == engine {
		return errors.New("busy")
	}
	if err = privateDirectory(filepath.Dir(binary)); err != nil {
		return errors.New("unsafe_state")
	}
	var stat syscall.Statfs_t
	if err = syscall.Statfs(filepath.Dir(binary), &stat); err != nil || uint64(stat.Bavail)*uint64(stat.Bsize) < 180<<20 {
		return errors.New("no_space")
	}
	directory, err := os.MkdirTemp(m.stateDir, "core-download-")
	if err != nil {
		return errors.New("state_failed")
	}
	defer os.RemoveAll(directory)
	archive := filepath.Join(directory, engine+".tar.gz")
	if err = m.save(job, "downloading"); err != nil {
		return err
	}
	address := "https://github.com/slobys/NaiLink/releases/download/" + releaseTag + "/" + release.Archive
	if err = m.fetch(ctx, address, archive, 128<<20); err != nil {
		return errors.New(downloadFailureCode(err))
	}
	if err = m.save(job, "verifying"); err != nil {
		return err
	}
	f, err := os.Open(archive)
	if err != nil {
		return errors.New("download_failed")
	}
	digest := sha256.New()
	_, err = io.Copy(digest, f)
	f.Close()
	if err != nil || hex.EncodeToString(digest.Sum(nil)) != release.SHA256 {
		return errors.New("checksum_failed")
	}
	target, err := os.CreateTemp(filepath.Dir(binary), ".native-*")
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
	output, err := m.run(ctx, append([]string{path}, versionArgs...)...)
	if err != nil || !strings.HasPrefix(string(output), prefix) {
		return errors.New(versionError)
	}
	// Datapath modules are prepared separately for the selected mode; installing
	// an optional core must not require TUN when the user wants TPROXY.
	if err = os.Chmod(path, 0755); err != nil {
		return errors.New("state_failed")
	}
	backup := ""
	if engine == "mihomo" {
		if info, e := os.Lstat(binary); e == nil {
			if !info.Mode().IsRegular() {
				return errors.New("unsafe_state")
			}
			backup = binary + ".previous-" + job.ID
			if _, e = os.Lstat(backup); !os.IsNotExist(e) {
				return errors.New("unsafe_state")
			}
			if e = os.Rename(binary, backup); e != nil {
				return errors.New("state_failed")
			}
		} else if !os.IsNotExist(e) {
			return errors.New("unsafe_state")
		}
	}
	restore := func() error {
		if engine != "mihomo" {
			return nil
		}
		if backup != "" {
			return os.Rename(backup, binary)
		}
		if e := os.Remove(binary); e != nil && !os.IsNotExist(e) {
			return e
		}
		return nil
	}
	if err = os.Rename(path, binary); err != nil {
		if restore() != nil {
			return errors.New("core_rollback_failed")
		}
		return errors.New("state_failed")
	}
	parent, err := os.Open(filepath.Dir(binary))
	if err != nil {
		if restore() != nil {
			return errors.New("core_rollback_failed")
		}
		return errors.New("state_failed")
	}
	defer parent.Close()
	if err = parent.Sync(); err != nil {
		if restore() != nil {
			return errors.New("core_rollback_failed")
		}
		return errors.New("state_failed")
	}
	if backup != "" {
		_ = os.Remove(backup)
	}
	return nil
}

// Use only the static binary from the checksum-pinned compatibility archive.
// No service scripts or defaults are installed over NaiLink's configuration.
func extractSingboxPackage(archive, destination string) error {
	return extractNativePackage(archive, destination, "sing-box")
}
func extractNativePackage(archive, destination, engine string) error {
	inner, err := os.CreateTemp(filepath.Dir(destination), ".core-payload-*")
	if err != nil {
		return errors.New("state_failed")
	}
	path := inner.Name()
	_ = inner.Close()
	defer os.Remove(path)
	if err = extractSingbox(archive, path, "data.tar.gz"); err != nil {
		return err
	}
	return extractSingbox(path, destination, "./usr/bin/"+engine)
}
func extractSingbox(archive, destination, wanted string) error {
	f, err := os.Open(archive)
	if err != nil {
		return errors.New("invalid_release")
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return errors.New("invalid_release")
	}
	defer gz.Close()
	reader := tar.NewReader(io.LimitReader(gz, 300<<20))
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid_release")
		}
		if filepath.IsAbs(header.Name) || strings.HasPrefix(filepath.Clean(header.Name), "..") || header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return errors.New("invalid_release")
		}
		if header.Name != wanted {
			continue
		}
		if found || header.Size < 1 || header.Size > 200<<20 {
			return errors.New("invalid_release")
		}
		found = true
		out, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return errors.New("state_failed")
		}
		n, copyErr := io.Copy(out, io.LimitReader(reader, header.Size+1))
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil || n != header.Size || syncErr != nil || closeErr != nil {
			return errors.New("invalid_release")
		}
	}
	if !found {
		return errors.New("invalid_release")
	}
	return nil
}
func validateSingboxELF(path, arch string) error {
	f, err := elf.Open(path)
	if err != nil {
		return errors.New("invalid_release")
	}
	defer f.Close()
	expected := elf.EM_X86_64
	if arch == "arm64" {
		expected = elf.EM_AARCH64
	} else if arch != "amd64" {
		return errors.New("unsupported")
	}
	if f.Class != elf.ELFCLASS64 || f.Machine != expected {
		return errors.New("invalid_release")
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return errors.New("invalid_release")
		}
	}
	return nil
}

func (m *maintenanceManager) extraPath() string {
	if m.extraRoot != "" {
		return m.extraRoot
	}
	return routercore.ExtraRoot
}

// Stage the entire bundle before publishing one atomic link. Existing versions
// remain intact for any process using them; a download never stops the proxy.
func (m *maintenanceManager) installExtra(ctx context.Context, job *maintenanceJob) error {
	release, ok := routercore.ExtraReleases[runtime.GOARCH]
	if !ok {
		return errors.New("unsupported")
	}
	state, err := routercore.ReadState(m.configDir)
	if err != nil {
		return errors.New("unsafe_state")
	}
	if state.Current == "sing-box" {
		return errors.New("busy")
	}
	root := m.extraPath()
	if err = privateDirectory(root); err != nil {
		return errors.New("unsafe_state")
	}
	var stat syscall.Statfs_t
	if syscall.Statfs(root, &stat) != nil || uint64(stat.Bavail)*uint64(stat.Bsize) < 400<<20 {
		return errors.New("no_space")
	}
	temporary, err := os.MkdirTemp(root, ".extra-stage-")
	if err != nil {
		return errors.New("state_failed")
	}
	defer os.RemoveAll(temporary)
	archive := filepath.Join(temporary, "extra.tar.gz")
	if err = m.save(job, "downloading"); err != nil {
		return err
	}
	if err = m.fetch(ctx, "https://github.com/slobys/NaiLink/releases/download/"+routercore.ExtraRelease+"/"+release.Archive, archive, 128<<20); err != nil {
		return errors.New(downloadFailureCode(err))
	}
	f, err := os.Open(archive)
	if err != nil {
		return errors.New("download_failed")
	}
	hash := sha256.New()
	_, err = io.Copy(hash, f)
	f.Close()
	if err != nil || hex.EncodeToString(hash.Sum(nil)) != release.SHA256 {
		return errors.New("checksum_failed")
	}
	if err = m.save(job, "verifying"); err != nil {
		return err
	}
	stage := filepath.Join(temporary, "bundle")
	if err = extractExtra(archive, stage, release.CPU); err != nil {
		return err
	}
	if err = validateSingboxELF(filepath.Join(stage, "sing-box"), runtime.GOARCH); err != nil {
		return err
	}
	output, err := m.run(ctx, filepath.Join(stage, "sing-box"), "version")
	if err != nil || !strings.HasPrefix(string(output), "sing-box version "+routercore.ExtraVersion+"\n") {
		return errors.New("singbox_version_mismatch")
	}
	output, err = m.run(ctx, filepath.Join(stage, "tor-launch"), "--version")
	if err != nil || !strings.HasPrefix(string(output), "Tor version "+routercore.TorVersion+".") {
		return errors.New("extra_dependency_failed")
	}
	// Validate the previous pointer before replacing it, and preserve it if
	// persistence fails. No configuration/database files are touched here.
	link := filepath.Join(root, "extra")
	previous, readErr := os.Readlink(link)
	if readErr != nil && !os.IsNotExist(readErr) {
		return errors.New("unsafe_state")
	}
	if readErr == nil {
		if _, err = routercore.ExtraDirectory(root); err != nil {
			return errors.New("unsafe_state")
		}
	}
	target := filepath.Join(root, "extra-"+release.SHA256[:16])
	if _, err = os.Lstat(target); err == nil {
		// A prior interrupted attempt is left for inspection, never overwritten.
		target += "-" + job.ID
	} else if !os.IsNotExist(err) {
		return errors.New("state_failed")
	}
	if err = os.Rename(stage, target); err != nil {
		return errors.New("state_failed")
	}
	removeTarget := true
	defer func() {
		if removeTarget {
			_ = os.RemoveAll(target)
		}
	}()
	next := filepath.Join(temporary, "next")
	if err = os.Symlink(filepath.Base(target), next); err != nil {
		return errors.New("state_failed")
	}
	if err = os.Rename(next, link); err != nil {
		return errors.New("state_failed")
	}
	parent, err := os.Open(root)
	if err == nil {
		err = parent.Sync()
		parent.Close()
	}
	if err != nil {
		if previous != "" {
			_ = os.Symlink(previous, next)
			if os.Rename(next, link) != nil {
				removeTarget = false
				return errors.New("extra_rollback_failed")
			}
		} else if os.Remove(link) != nil {
			removeTarget = false
			return errors.New("extra_rollback_failed")
		}
		return errors.New("state_failed")
	}
	removeTarget = false
	// Old versions deliberately remain until the next plugin uninstall; never
	// delete a live executable or a Tor database as part of component updates.
	_ = os.Remove(filepath.Join(m.stateDir, "core-plan.json"))
	return nil
}

type extraFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   int64  `json:"mode"`
}
type extraManifest struct {
	Version string               `json:"version"`
	CPU     string               `json:"cpu"`
	Files   map[string]extraFile `json:"files"`
}

func extractExtra(archive, stage, cpu string) error {
	if err := privateDirectory(stage); err != nil {
		return errors.New("unsafe_state")
	}
	f, err := os.Open(archive)
	if err != nil {
		return errors.New("invalid_release")
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return errors.New("invalid_release")
	}
	defer gz.Close()
	reader := tar.NewReader(io.LimitReader(gz, 300<<20))
	seen := map[string]extraFile{}
	var manifest extraManifest
	manifestSeen := false
	count := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid_release")
		}
		name := header.Name
		count++
		if count > 96 || name == "" || filepath.IsAbs(name) || filepath.Clean(name) != name || strings.Contains(name, "\\") || strings.HasPrefix(name, "..") || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > 160<<20 {
			return errors.New("invalid_release")
		}
		if _, exists := seen[name]; exists {
			return errors.New("invalid_release")
		}
		if name == "manifest.json" {
			if manifestSeen || header.Size > 32768 {
				return errors.New("invalid_release")
			}
			manifestSeen = true
			raw, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
			if err != nil || int64(len(raw)) != header.Size || json.Unmarshal(raw, &manifest) != nil {
				return errors.New("invalid_release")
			}
			continue
		}
		if name != "sing-box" && name != "tor-launch" && name != "SOURCE.json" && !strings.HasPrefix(name, "licenses/") && !strings.HasPrefix(name, "tor/bin/") && !strings.HasPrefix(name, "tor/lib/") && !strings.HasPrefix(name, "tor/share/") {
			return errors.New("invalid_release")
		}
		path := filepath.Join(stage, name)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return errors.New("state_failed")
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("state_failed")
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(out, hash), io.LimitReader(reader, header.Size+1))
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil || n != header.Size {
			return errors.New("invalid_release")
		}
		mode := header.Mode & 0777
		if mode != 0644 && mode != 0755 {
			return errors.New("invalid_release")
		}
		if err = os.Chmod(path, os.FileMode(mode)&0700); err != nil {
			return errors.New("state_failed")
		}
		seen[name] = extraFile{hex.EncodeToString(hash.Sum(nil)), n, mode}
	}
	if manifest.Version != routercore.ExtraVersion || manifest.CPU != cpu || len(manifest.Files) != len(seen) {
		return errors.New("invalid_release")
	}
	for name, expected := range manifest.Files {
		if seen[name] != expected {
			return errors.New("invalid_release")
		}
	}
	for _, name := range []string{"sing-box", "tor-launch", "tor/bin/tor", "tor/share/geoip", "tor/share/geoip6", "SOURCE.json", "licenses/NOTICE.txt"} {
		if _, ok := seen[name]; !ok {
			return errors.New("invalid_release")
		}
	}
	// Persist nested directory entries as well as file contents before publish.
	return filepath.WalkDir(stage, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		d, e := os.Open(path)
		if e != nil {
			return e
		}
		defer d.Close()
		return d.Sync()
	})
}
