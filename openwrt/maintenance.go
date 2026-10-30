// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var releaseTag = regexp.MustCompile(`^openwrt-v([0-9]+\.[0-9]+\.[0-9]+)$`)
var dataTag = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$`)

func queuedMaintenanceStale(updated, now int64) bool {
	// OpenWrt can correct its clock after boot. A future timestamp must not
	// leave an abandoned queue entry busy forever.
	return now-updated > 30 || updated-now > 60
}

type maintenanceJob struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	State   string `json:"state"`
	Phase   string `json:"phase"`
	Code    string `json:"code,omitempty"`
	Target  string `json:"target,omitempty"`
	Batch   string `json:"batch,omitempty"`
	Started int64  `json:"started"`
	Updated int64  `json:"updated"`
}
type releaseInfo struct {
	Version string `json:"version"`
	Tag     string `json:"tag"`
	Checked int64  `json:"checked"`
}
type geodataReleaseCheck struct {
	Version string `json:"version,omitempty"`
	Tag     string `json:"tag,omitempty"`
	Checked int64  `json:"checked"`
	Code    string `json:"code,omitempty"`
}
type maintenanceGeodataReceipt struct {
	Target   string `json:"target"`
	Previous string `json:"previous"`
	State    string `json:"state"`
}
type maintenanceStatus struct {
	Success       bool                   `json:"success"`
	Code          string                 `json:"code,omitempty"`
	Current       string                 `json:"current"`
	Latest        *releaseInfo           `json:"latest,omitempty"`
	Job           *maintenanceJob        `json:"job,omitempty"`
	Geodata       *releaseInfo           `json:"geodata,omitempty"`
	GeodataCheck  *geodataReleaseCheck   `json:"geodata_check,omitempty"`
	GeodataCustom bool                   `json:"geodata_custom,omitempty"`
	Cores         *coreMaintenanceStatus `json:"cores,omitempty"`
}
type maintenanceManager struct {
	stateDir, configDir, versionFile, lockFile, installLock, initScript string
	coreBinary, mihomoBinary, extraRoot                                 string
	fetch                                                               func(context.Context, string, string, int64) error
	run                                                                 func(context.Context, ...string) ([]byte, error)
	graph                                                               func(context.Context, string, any) error
	now                                                                 func() time.Time
}

func newMaintenance(configDir string) *maintenanceManager {
	m := &maintenanceManager{stateDir: "/var/run/daed-maintenance", configDir: configDir,
		versionFile: "/usr/share/daed-router/version", lockFile: "/var/lock/daed-maintenance.lock",
		installLock: "/var/lock/daed-install.lock", initScript: "/usr/libexec/daed/nailink-maintenance", now: time.Now,
		fetch: maintenanceDownload, run: maintenanceCommand}
	m.graph = m.graphQL
	return m
}

// Files are root-owned private state. Never follow pre-existing symlinks.
func privateDirectory(name string) error {
	if err := os.MkdirAll(name, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe_directory")
	}
	return nil
}
func readMaintenanceJSON(name string, value any) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 32768 {
		return errors.New("unsafe_state")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
func writeMaintenanceJSON(name string, value any) error {
	if err := privateDirectory(filepath.Dir(name)); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(name), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), name)
}
func maintenanceLock(name string, wait bool) (*os.File, error) {
	// /var/lock is firmware-owned; O_NOFOLLOW rejects a substituted lock file.
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(name, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	flags := syscall.LOCK_EX
	if !wait {
		flags |= syscall.LOCK_NB
	}
	if err = syscall.Flock(fd, flags); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
func versionNewer(next, current string) bool {
	parse := func(value string) []int {
		value = strings.Split(value, "-")[0]
		parts := strings.Split(value, ".")
		if len(parts) != 3 {
			return nil
		}
		result := make([]int, 3)
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 {
				return nil
			}
			result[i] = n
		}
		return result
	}
	a, b := parse(next), parse(current)
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
func (m *maintenanceManager) current() string {
	data, err := os.ReadFile(m.versionFile)
	if err != nil || len(data) > 80 {
		return "unknown"
	}
	value := strings.TrimSpace(string(data))
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9]+)?$`).MatchString(value) {
		return "unknown"
	}
	return value
}
func (m *maintenanceManager) status() maintenanceStatus {
	s := maintenanceStatus{Success: true, Current: m.current(), Cores: m.coreStatus()}
	var latest releaseInfo
	if readMaintenanceJSON(filepath.Join(m.stateDir, "latest.json"), &latest) == nil && releaseTag.MatchString(latest.Tag) {
		s.Latest = &latest
	}
	var geo releaseInfo
	var receipt maintenanceGeodataReceipt
	_ = readMaintenanceJSON(filepath.Join(m.configDir, "geodata", "activation.json"), &receipt)
	geoBatch := "current"
	if receipt.State == "applying" {
		geoBatch = receipt.Previous
	}
	if geoBatch != "" && readMaintenanceJSON(filepath.Join(m.configDir, "geodata", geoBatch, "version.json"), &geo) == nil {
		s.Geodata = &geo
	}
	var check geodataReleaseCheck
	if readMaintenanceJSON(filepath.Join(m.stateDir, "geodata-latest.json"), &check) == nil && check.Checked > 0 && (check.Code != "" || dataTag.MatchString(check.Tag) && check.Version == check.Tag) {
		s.GeodataCheck = &check
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if _, err := os.Lstat(filepath.Join(m.configDir, name)); !os.IsNotExist(err) {
			s.GeodataCustom = true
		}
	}
	var job maintenanceJob
	if readMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), &job) == nil {
		// An exited/killed worker must never leave an endless spinner. A freshly
		// queued task has a short grace period while procd starts the worker.
		if job.State == "running" || job.State == "queued" && queuedMaintenanceStale(job.Updated, m.now().Unix()) || job.Code == "apply_unconfirmed" || uncertainCoreJob(job) {
			if lock, err := maintenanceLock(m.lockFile, false); err == nil {
				var fresh maintenanceJob
				if readMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), &fresh) == nil && fresh.ID == job.ID && (fresh.State == "running" || fresh.State == "queued" && queuedMaintenanceStale(fresh.Updated, m.now().Unix()) || fresh.Code == "apply_unconfirmed" || uncertainCoreJob(fresh)) {
					job.State, job.Code, job.Phase = "error", "interrupted", "finished"
					if strings.HasPrefix(job.Action, "core_switch_") {
						m.reconcileCoreJob(&job)
					}
					if job.Action == "geodata" && job.Batch != "" && job.Batch == receipt.Target {
						current, _ := os.Readlink(filepath.Join(m.configDir, "geodata", "current"))
						switch receipt.State {
						case "applied":
							if current == job.Batch {
								job.State, job.Code = "success", "recovered"
							}
						case "rolled_back":
							job.Code = "apply_failed"
						case "applying":
							job.Code = "apply_unconfirmed"
						}
					}
					job.Updated = m.now().Unix()
					_ = writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), job)
				}
				lock.Close()
			}
		}
		s.Job = &job
	}
	return s
}
func (m *maintenanceManager) save(job *maintenanceJob, phase string) error {
	job.Phase = phase
	job.Updated = m.now().Unix()
	return writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), job)
}
func (m *maintenanceManager) start(action string) maintenanceStatus {
	if action != "check" && action != "update" && action != "geodata" && !coreMaintenanceAction(action) {
		return maintenanceStatus{Code: "invalid_action"}
	}
	lock, err := maintenanceLock(m.lockFile, false)
	if err != nil {
		return maintenanceStatus{Code: "busy"}
	}
	defer lock.Close()
	if err = privateDirectory(m.stateDir); err != nil {
		return maintenanceStatus{Code: "unsafe_state"}
	}
	var previous maintenanceJob
	if core := m.coreStatus(); core.Phase == "switching" {
		return maintenanceStatus{Code: "busy"}
	}
	var pending maintenanceGeodataReceipt
	if readMaintenanceJSON(filepath.Join(m.configDir, "geodata", "activation.json"), &pending) == nil && pending.State == "applying" {
		return maintenanceStatus{Code: "apply_unconfirmed"}
	}
	if readMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), &previous) == nil && previous.State == "queued" && !queuedMaintenanceStale(previous.Updated, m.now().Unix()) {
		return maintenanceStatus{Code: "busy"}
	}
	job := maintenanceJob{Action: action, State: "queued", Phase: "queued", Started: m.now().Unix()}
	if action == "update" {
		var latest releaseInfo
		if readMaintenanceJSON(filepath.Join(m.stateDir, "latest.json"), &latest) != nil || !releaseTag.MatchString(latest.Tag) || m.now().Unix()-latest.Checked > 3600 || latest.Checked > m.now().Unix()+60 {
			return maintenanceStatus{Code: "check_required"}
		}
		if !versionNewer(latest.Version, m.current()) {
			return maintenanceStatus{Code: "already_latest"}
		}
		if strings.TrimPrefix(latest.Tag, "openwrt-v") != latest.Version {
			return maintenanceStatus{Code: "check_required"}
		}
		job.Target = latest.Tag
	}
	id := make([]byte, 12)
	if _, err = rand.Read(id); err != nil {
		return maintenanceStatus{Code: "state_failed"}
	}
	job.ID = hex.EncodeToString(id)
	if err = m.save(&job, "queued"); err != nil {
		return maintenanceStatus{Code: "state_failed"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err = m.run(ctx, m.initScript, "start", job.ID); err != nil {
		job.State, job.Code = "error", "worker_unavailable"
		_ = m.save(&job, "finished")
		return maintenanceStatus{Code: "worker_unavailable"}
	}
	return maintenanceStatus{Success: true, Current: m.current(), Job: &job}
}
func (m *maintenanceManager) worker(ctx context.Context, expectedID string) error {
	lock, err := maintenanceLock(m.lockFile, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	var job maintenanceJob
	if err = readMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), &job); err != nil || job.State != "queued" || job.ID != expectedID {
		return errors.New("no_queued_job")
	}
	job.State = "running"
	if err = m.save(&job, "checking"); err != nil {
		return err
	}
	switch job.Action {
	case "check":
		err = m.checkRelease(ctx, &job)
	case "update":
		err = m.installUpdate(ctx, &job)
	case "geodata":
		err = m.updateGeodata(ctx, &job)
	default:
		if coreMaintenanceAction(job.Action) {
			err = m.maintainCore(ctx, &job)
		} else {
			err = errors.New("invalid_action")
		}
	}
	job.State = "success"
	if err != nil {
		job.State = "error"
		job.Code = maintenanceCode(err)
	}
	return m.save(&job, "finished")
}
func maintenanceCode(err error) string {
	for _, code := range downloadFailureCodes {
		if err.Error() == code {
			return code
		}
	}
	if code := coreMaintenanceCode(err.Error()); code != "" {
		return code
	}
	// Only stable codes cross the UI boundary; network/installer errors may
	// contain full URLs, query strings or configuration paths.
	for _, code := range []string{"busy", "download_failed", "invalid_release", "checksum_failed", "invalid_installer", "install_failed", "install_missing", "package_feed_failed", "package_preflight_failed", "preparation_timeout", "version_changed", "unsupported", "no_space", "backup_failed", "service_not_ready", "custom_geodata", "invalid_geodata", "apply_failed", "apply_unconfirmed", "rollback_failed", "configuration_modified", "backend_unavailable", "unsafe_state", "state_failed", "update_rolled_back", "interrupted"} {
		if err.Error() == code {
			return code
		}
	}
	return "operation_failed"
}

type githubRelease struct {
	Tag    string `json:"tag_name"`
	Draft  bool   `json:"draft"`
	Assets []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

func (m *maintenanceManager) getJSON(ctx context.Context, address string, value any) error {
	file, err := os.CreateTemp(m.stateDir, ".download-*")
	if err != nil {
		return errors.New("state_failed")
	}
	name := file.Name()
	file.Close()
	defer os.Remove(name)
	if err = m.fetch(ctx, address, name, 4<<20); err != nil {
		return errors.New(downloadFailureCode(err))
	}
	data, err := os.ReadFile(name)
	if err != nil || json.Unmarshal(data, value) != nil {
		return errors.New("invalid_release")
	}
	return nil
}
func (m *maintenanceManager) checkRelease(ctx context.Context, job *maintenanceJob) error {
	var releases []githubRelease
	if err := m.getJSON(ctx, "https://api.github.com/repos/slobys/NaiLink/releases?per_page=10", &releases); err != nil {
		return err
	}
	var latest releaseInfo
	for _, release := range releases {
		match := releaseTag.FindStringSubmatch(release.Tag)
		if release.Draft || match == nil {
			continue
		}
		assets := map[string]bool{}
		for _, asset := range release.Assets {
			assets[asset.Name] = true
		}
		if !assets["install.sh"] || !assets["SHA256SUMS"] || !assets["manifest.tsv"] {
			continue
		}
		if latest.Version == "" || versionNewer(match[1], latest.Version) {
			latest = releaseInfo{match[1], release.Tag, m.now().Unix()}
		}
	}
	if latest.Version == "" {
		return errors.New("invalid_release")
	}
	job.Target = latest.Tag
	return writeMaintenanceJSON(filepath.Join(m.stateDir, "latest.json"), latest)
}
func checksumFor(data []byte, name string) (string, error) {
	found := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		if found != "" || len(fields[0]) != 64 {
			return "", errors.New("checksum_failed")
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", errors.New("checksum_failed")
		}
		found = strings.ToLower(fields[0])
	}
	if found == "" {
		return "", errors.New("checksum_failed")
	}
	return found, nil
}
func verifyChecksum(name, expected string) error {
	file, err := os.Open(name)
	if err != nil {
		return errors.New("checksum_failed")
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil || hex.EncodeToString(hash.Sum(nil)) != expected {
		return errors.New("checksum_failed")
	}
	return nil
}
func (m *maintenanceManager) installUpdate(ctx context.Context, job *maintenanceJob) error {
	if !releaseTag.MatchString(job.Target) || !versionNewer(strings.TrimPrefix(job.Target, "openwrt-v"), m.current()) {
		return errors.New("invalid_release")
	}
	stage, err := os.MkdirTemp(m.stateDir, "install-*")
	if err != nil {
		return errors.New("state_failed")
	}
	defer os.RemoveAll(stage)
	if err = m.save(job, "downloading"); err != nil {
		return errors.New("state_failed")
	}
	base := "https://github.com/slobys/NaiLink/releases/download/" + job.Target + "/"
	for _, name := range []string{"SHA256SUMS", "install.sh"} {
		if err = m.fetch(ctx, base+name, filepath.Join(stage, name), 512<<10); err != nil {
			return errors.New(downloadFailureCode(err))
		}
	}
	if err = m.save(job, "verifying"); err != nil {
		return errors.New("state_failed")
	}
	sums, _ := os.ReadFile(filepath.Join(stage, "SHA256SUMS"))
	digest, err := checksumFor(sums, "install.sh")
	if err != nil {
		return err
	}
	installer := filepath.Join(stage, "install.sh")
	if err = verifyChecksum(installer, digest); err != nil {
		return err
	}
	data, _ := os.ReadFile(installer)
	matches := regexp.MustCompile(`(?m)^release=(openwrt-v[0-9]+\.[0-9]+\.[0-9]+)\r?$`).FindAllSubmatch(data, -1)
	if len(matches) != 1 || string(matches[0][1]) != job.Target {
		return errors.New("invalid_installer")
	}
	if _, err = m.run(ctx, "/bin/sh", "-n", installer); err != nil {
		return errors.New("invalid_installer")
	}
	prepareSeconds := 900
	if deadline, limited := ctx.Deadline(); limited {
		// Initial script/checksum downloads already consumed worker time. Keep
		// five minutes for installing and recovering, rather than resetting the
		// preparation clock when the shell starts.
		remaining := int(time.Until(deadline).Seconds()) - 300
		if remaining <= 0 {
			return errors.New("preparation_timeout")
		}
		if remaining < prepareSeconds {
			prepareSeconds = remaining
		}
	}
	if err = m.save(job, "installing"); err != nil {
		return errors.New("state_failed")
	}
	if !versionNewer(strings.TrimPrefix(job.Target, "openwrt-v"), m.current()) {
		return errors.New("version_changed")
	}
	output, err := m.run(ctx, "/bin/sh", installer, "--install", "--update-only", "--release", job.Target, "--prepare-timeout", strconv.Itoa(prepareSeconds))
	if err != nil {
		text := string(output)
		if strings.Contains(text, "previous installation and service state restored") {
			return errors.New("update_rolled_back")
		}
		if strings.Contains(text, "Restoring verified previous native packages") {
			return errors.New("rollback_failed")
		}
		for _, code := range downloadFailureCodes {
			if strings.Contains(text, "NAILINK_DOWNLOAD_ERROR="+code+"\n") {
				return errors.New(code)
			}
		}
		for _, item := range []struct{ token, code string }{{"Another NaiLink", "busy"}, {"Existing managed install required", "install_missing"}, {"Online update refused", "version_changed"}, {"Firmware package feed refresh failed", "package_feed_failed"}, {"Package preflight failed", "package_preflight_failed"}, {"Update preparation timed out", "preparation_timeout"}, {"Unsupported CPU", "unsupported"}, {"Compatibility check failed", "unsupported"}, {"Supported:", "unsupported"}, {"space", "no_space"}, {"Cannot back up", "backup_failed"}, {"Checksum mismatch", "checksum_failed"}, {"Cannot download", "download_failed"}, {"Management HTTP did not become ready", "service_not_ready"}} {
			if strings.Contains(text, item.token) {
				return errors.New(item.code)
			}
		}
		if ctx.Err() != nil {
			return errors.New("interrupted")
		}
		return errors.New("install_failed")
	}
	if strings.Split(m.current(), "-")[0] != strings.TrimPrefix(job.Target, "openwrt-v") {
		return errors.New("install_failed")
	}
	return nil
}

var downloadFailureCodes = []string{"download_dns", "download_tls", "download_timeout", "download_connection", "download_rate_limited", "download_asset_missing", "download_http", "download_redirect", "download_invalid"}

func downloadFailureCode(err error) string {
	if err == nil {
		return "download_failed"
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		for _, code := range downloadFailureCodes {
			if current.Error() == code {
				return code
			}
		}
	}
	var dns *net.DNSError
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var network net.Error
	var file *os.PathError
	switch {
	case errors.Is(err, context.Canceled):
		return "interrupted"
	case errors.Is(err, syscall.ENOSPC):
		return "no_space"
	case errors.As(err, &file):
		return "state_failed"
	case errors.As(err, &dns):
		return "download_dns"
	case errors.As(err, &cert), errors.As(err, &unknown), errors.As(err, &invalid), errors.As(err, &hostname):
		return "download_tls"
	case errors.Is(err, context.DeadlineExceeded):
		return "download_timeout"
	case errors.As(err, &network) && network.Timeout():
		return "download_timeout"
	case errors.As(err, &network), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "download_connection"
	}
	return "download_failed"
}

func maintenanceDownloadAllowed(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return false
	}
	switch u.Hostname() {
	case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}

// A bounded retry stays on the original HTTPS origin. A second address-family
// attempt also handles IPv6 that connects but stalls during TLS/HTTP.
func maintenanceDownloadAttempts(ctx context.Context, address, destination string, limit int64, accept string) error {
	var err error
	for _, ipv4 := range []bool{false, true} {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = maintenanceDownloadAttempt(ctx, address, destination, limit, accept, ipv4)
		if err == nil {
			return nil
		}
		code := downloadFailureCode(err)
		if code != "download_dns" && code != "download_timeout" && code != "download_connection" && code != "download_http" {
			break
		}
	}
	return err
}

func maintenanceDownload(ctx context.Context, address, destination string, limit int64) error {
	parsed, err := url.Parse(address)
	if err != nil || !maintenanceDownloadAllowed(parsed) || limit <= 0 {
		return errors.New("download_redirect")
	}
	// Keep metadata requests short, but allow a bounded large component transfer
	// on slow router links. Header/TLS deadlines still reject stalled starts.
	transferTimeout := 3 * time.Minute
	if limit > 8<<20 {
		transferTimeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, transferTimeout)
	defer cancel()
	err = maintenanceDownloadAttempts(ctx, address, destination, limit, "application/vnd.github+json")
	if err == nil || ctx.Err() != nil {
		return err
	}
	// The GitHub website and its public API have different reachability on some
	// router networks. Resolve an exact asset ID from the pinned release; never
	// trust a returned download URL or use a third-party mirror.
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if parsed.Hostname() != "github.com" || parsed.RawQuery != "" || parsed.Fragment != "" || len(parts) != 6 || parts[2] != "releases" || parts[3] != "download" {
		return err
	}
	repository := parts[0] + "/" + parts[1]
	if repository != "slobys/NaiLink" && repository != "Loyalsoldier/v2ray-rules-dat" {
		return err
	}
	if !dataTag.MatchString(parts[4]) || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,199}$`).MatchString(parts[5]) {
		return err
	}
	meta, createErr := os.CreateTemp(filepath.Dir(destination), ".release-*")
	if createErr != nil {
		return createErr
	}
	meta.Close()
	defer os.Remove(meta.Name())
	metadata := "https://api.github.com/repos/" + repository + "/releases/tags/" + parts[4]
	if fetchErr := maintenanceDownloadAttempts(ctx, metadata, meta.Name(), 4<<20, "application/vnd.github+json"); fetchErr != nil {
		return fetchErr
	}
	var release struct {
		Tag    string `json:"tag_name"`
		Draft  bool   `json:"draft"`
		Assets []struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"assets"`
	}
	data, readErr := os.ReadFile(meta.Name())
	if readErr != nil {
		return readErr
	}
	if json.Unmarshal(data, &release) != nil || release.Draft || release.Tag != parts[4] {
		return errors.New("download_invalid")
	}
	id := int64(0)
	for _, asset := range release.Assets {
		if asset.Name == parts[5] {
			if id != 0 || asset.ID <= 0 || asset.State != "uploaded" {
				return errors.New("download_invalid")
			}
			id = asset.ID
		}
	}
	if id == 0 {
		return errors.New("download_asset_missing")
	}
	assetURL := "https://api.github.com/repos/" + repository + "/releases/assets/" + strconv.FormatInt(id, 10)
	return maintenanceDownloadAttempts(ctx, assetURL, destination, limit, "application/octet-stream")
}

func maintenanceDownloadAttempt(ctx context.Context, address, destination string, limit int64, accept string, ipv4 bool) error {
	transport := http.DefaultTransport
	if base, ok := transport.(*http.Transport); ok {
		copy := base.Clone()
		copy.Proxy = nil
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		copy.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if ipv4 {
				network = "tcp4"
			}
			return dialer.DialContext(ctx, network, address)
		}
		copy.ResponseHeaderTimeout = 20 * time.Second
		copy.TLSHandshakeTimeout = 10 * time.Second
		transport = copy
		defer copy.CloseIdleConnections()
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 || !maintenanceDownloadAllowed(r.URL) {
			return errors.New("download_redirect")
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "NaiLink-maintenance")
	request.Header.Set("Accept", accept)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == 403 || response.StatusCode == 429 {
		return errors.New("download_rate_limited")
	}
	if response.StatusCode == 404 {
		return errors.New("download_asset_missing")
	}
	if response.StatusCode != 200 {
		return errors.New("download_http")
	}
	if response.ContentLength > limit {
		return errors.New("download_invalid")
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".transfer-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	size, err := io.Copy(file, io.LimitReader(response.Body, limit+1))
	if err != nil {
		file.Close()
		return err
	}
	if response.ContentLength > 0 && size != response.ContentLength {
		file.Close()
		return io.ErrUnexpectedEOF
	}
	if size == 0 || size > limit {
		file.Close()
		return errors.New("download_invalid")
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), destination)
}

type boundedOutput struct{ data []byte }

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= 65536 {
		b.data = append(b.data[:0], p[len(p)-65536:]...)
	} else {
		if len(b.data)+len(p) > 65536 {
			copy(b.data, b.data[len(b.data)+len(p)-65536:])
			b.data = b.data[:65536-len(p)]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func maintenanceCommand(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("empty_command")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	// Give the installer trap time to restore its verified backup/packages.
	cmd.WaitDelay = 90 * time.Second
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return out.data, err
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	err := cmd.Wait()
	return out.data, err
}
func (m *maintenanceManager) graphQL(ctx context.Context, query string, value any) error {
	identity, err := readCredentials(m.configDir)
	if err != nil {
		return errors.New("backend_unavailable")
	}
	target, err := backendURL()
	if err != nil {
		return errors.New("backend_unavailable")
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": identity.Username, "role": "admin", "exp": m.now().Add(time.Minute).Unix()}).SignedString([]byte(identity.Secret))
	if err != nil {
		return errors.New("backend_unavailable")
	}
	body, _ := json.Marshal(map[string]string{"query": query})
	request, err := http.NewRequestWithContext(ctx, "POST", target.String()+"/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 300 * time.Second, Transport: &http.Transport{Proxy: nil}}).Do(request)
	if err != nil {
		return errors.New("backend_unavailable")
	}
	defer response.Body.Close()
	var envelope struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope) != nil {
		return errors.New("backend_unavailable")
	}
	if len(envelope.Errors) > 0 {
		for _, raw := range envelope.Errors {
			var item struct{ Message string }
			if json.Unmarshal(raw, &item) == nil {
				if code := coreMaintenanceCode(item.Message); code != "" {
					return errors.New(code)
				}
			}
		}
		return errors.New("apply_failed")
	}
	if len(envelope.Data) == 0 {
		return errors.New("backend_unavailable")
	}
	if err = json.Unmarshal(envelope.Data, value); err != nil {
		return errors.New("backend_unavailable")
	}
	return nil
}

func maintenanceCLI(command, configDir, jobID string) {
	debug.SetMemoryLimit(64 << 20)
	if os.Geteuid() != 0 {
		json.NewEncoder(os.Stdout).Encode(maintenanceStatus{Code: "permission_denied"})
		return
	}
	manager := newMaintenance(configDir)
	switch command {
	case "geodata_check":
		json.NewEncoder(os.Stdout).Encode(manager.checkGeodataLatest(context.Background()))
	case "status":
		json.NewEncoder(os.Stdout).Encode(manager.status())
	case "check", "update", "geodata", "core_check_dae", "core_check_singbox", "core_check_mihomo", "core_install_singbox", "core_install_mihomo", "core_install_extra", "core_switch_dae", "core_switch_singbox", "core_switch_mihomo":
		json.NewEncoder(os.Stdout).Encode(manager.start(command))
	case "worker":
		if !regexp.MustCompile(`^[a-f0-9]{24}$`).MatchString(jobID) {
			return
		}
		signalContext, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		defer stop()
		ctx, cancel := context.WithTimeout(signalContext, 20*time.Minute)
		defer cancel()
		if err := manager.worker(ctx, jobID); err != nil {
			fmt.Fprintln(os.Stderr, "NaiLink maintenance worker stopped")
		}
	default:
		if coreMaintenanceAction(command) {
			json.NewEncoder(os.Stdout).Encode(manager.start(command))
		} else {
			json.NewEncoder(os.Stdout).Encode(maintenanceStatus{Code: "invalid_action"})
		}
	}
}
