// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daeuniverse/dae/pkg/geodata"
	"google.golang.org/protobuf/proto"
)

// Validate a complete protobuf list one entry at a time, bounding peak memory.
// Each entry must have real domains/networks; HTML and empty protobufs fail.
func validateGeodata(name, kind string) error {
	file, err := os.Open(name)
	if err != nil {
		return errors.New("invalid_geodata")
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	entries := 0
	codes := map[string]bool{}
	for {
		tag, err := binary.ReadUvarint(reader)
		if err == io.EOF {
			break
		}
		if err != nil || tag != 10 {
			return errors.New("invalid_geodata")
		}
		size, err := binary.ReadUvarint(reader)
		if err != nil || size == 0 || size > 32<<20 {
			return errors.New("invalid_geodata")
		}
		data := make([]byte, int(size))
		if _, err = io.ReadFull(reader, data); err != nil {
			return errors.New("invalid_geodata")
		}
		code := ""
		if kind == "geoip.dat" {
			var entry geodata.GeoIP
			if proto.Unmarshal(data, &entry) != nil || len(entry.Cidr) == 0 {
				return errors.New("invalid_geodata")
			}
			code = entry.CountryCode
			for _, cidr := range entry.Cidr {
				if cidr == nil || len(cidr.Ip) != 4 && len(cidr.Ip) != 16 || cidr.Prefix > uint32(len(cidr.Ip)*8) {
					return errors.New("invalid_geodata")
				}
			}
		} else {
			var entry geodata.GeoSite
			if proto.Unmarshal(data, &entry) != nil || len(entry.Domain) == 0 {
				return errors.New("invalid_geodata")
			}
			code = entry.CountryCode
			for _, domain := range entry.Domain {
				if domain == nil || domain.Value == "" || domain.Type < 0 || domain.Type > geodata.Domain_Full {
					return errors.New("invalid_geodata")
				}
				if domain.Type == geodata.Domain_Regex {
					if _, err = regexp.Compile(domain.Value); err != nil {
						return errors.New("invalid_geodata")
					}
				}
			}
		}
		code = strings.ToLower(code)
		if code == "" || len(code) > 256 || codes[code] {
			return errors.New("invalid_geodata")
		}
		codes[code] = true
		entries++
		if entries > 20000 {
			return errors.New("invalid_geodata")
		}
	}
	if entries == 0 {
		return errors.New("invalid_geodata")
	}
	return nil
}

func switchGeodata(directory, target string) error {
	if target != "" && (filepath.Base(target) != target || !dataTag.MatchString(target)) {
		return errors.New("unsafe_state")
	}
	temporary, err := os.CreateTemp(directory, ".pointer-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	temporary.Close()
	os.Remove(name)
	defer os.Remove(name)
	if target == "" {
		return os.Remove(filepath.Join(directory, "current"))
	}
	if err = os.Symlink(target, name); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(directory, "current"))
}

// Only public release metadata is fetched. This path never creates a maintenance
// job, accesses the proxy API, downloads rule data or changes the active pointer.
func (m *maintenanceManager) checkGeodataLatest(parent context.Context) maintenanceStatus {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	respond := func(check *geodataReleaseCheck) maintenanceStatus {
		result := m.status()
		result.GeodataCheck = check
		return result
	}
	failure := func(code string) maintenanceStatus {
		return respond(&geodataReleaseCheck{Checked: m.now().Unix(), Code: code})
	}
	if privateDirectory(m.stateDir) != nil {
		return failure("unsafe_state")
	}
	var lock *os.File
	for {
		var err error
		lock, err = maintenanceLock(m.lockFile+".geodata-check", false)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return failure("unsafe_state")
		}
		select {
		case <-ctx.Done():
			return failure("download_timeout")
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer lock.Close()
	cache := filepath.Join(m.stateDir, "geodata-latest.json")
	var check geodataReleaseCheck
	if readMaintenanceJSON(cache, &check) == nil {
		age, cooldown := m.now().Unix()-check.Checked, int64(30)
		if check.Code != "" {
			cooldown = 3
		}
		if age >= 0 && age < cooldown && (check.Code != "" || dataTag.MatchString(check.Tag) && check.Version == check.Tag) {
			return respond(&check)
		}
	}
	check = geodataReleaseCheck{Checked: m.now().Unix()}
	tag, err := m.geodataReleaseTag(ctx)
	check.Checked = m.now().Unix()
	if err != nil {
		check.Code = maintenanceCode(err)
	} else {
		check.Version, check.Tag = tag, tag
	}
	if writeMaintenanceJSON(cache, check) != nil {
		check = geodataReleaseCheck{Checked: m.now().Unix(), Code: "state_failed"}
	}
	return respond(&check)
}

const geodataReleases = "https://github.com/Loyalsoldier/v2ray-rules-dat/releases"

func (m *maintenanceManager) geodataReleaseTag(ctx context.Context) (string, error) {
	var release githubRelease
	err := m.getJSON(ctx, "https://api.github.com/repos/Loyalsoldier/v2ray-rules-dat/releases/latest", &release)
	if err == nil {
		return release.Tag, validateGeodataRelease(release)
	}
	if !releaseWebsiteFallback(ctx, err) {
		return "", err
	}
	return latestGeodataWebsiteTag(ctx)
}

func latestGeodataWebsiteTag(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	client := maintenanceHTTPClient(false)
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, geodataReleases+"/latest", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "NaiLink-maintenance")
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New(downloadFailureCode(err))
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return "", errors.New("download_rate_limited")
	default:
		return "", errors.New("download_http")
	}
	location, err := response.Location()
	if err != nil {
		return "", errors.New("invalid_release")
	}
	tag, ok := strings.CutPrefix(location.String(), geodataReleases+"/tag/")
	if !ok || !dataTag.MatchString(tag) {
		return "", errors.New("invalid_release")
	}
	return tag, nil
}

func validateGeodataRelease(release githubRelease) error {
	if release.Draft || !dataTag.MatchString(release.Tag) {
		return errors.New("invalid_release")
	}
	assets := map[string]bool{}
	for _, asset := range release.Assets {
		assets[asset.Name] = true
	}
	for _, name := range []string{"geoip.dat", "geosite.dat", "geoip.dat.sha256sum", "geosite.dat.sha256sum"} {
		if !assets[name] {
			return errors.New("invalid_release")
		}
	}
	return nil
}

func (m *maintenanceManager) updateGeodata(ctx context.Context, job *maintenanceJob) error {
	lock, err := maintenanceLock(m.installLock, false)
	if err != nil {
		return errors.New("busy")
	}
	defer lock.Close()
	// The top-priority config directory belongs to the user. Never overwrite it
	// or silently report success when custom data would shadow managed data.
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if _, err = os.Lstat(filepath.Join(m.configDir, name)); err == nil || !os.IsNotExist(err) {
			return errors.New("custom_geodata")
		}
	}
	var state map[string]any
	if err = m.graph(ctx, `query{healthCheck}`, &state); err != nil {
		return err
	}
	directory := filepath.Join(m.configDir, "geodata")
	if err = privateDirectory(directory); err != nil {
		return errors.New("unsafe_state")
	}
	previous := ""
	if info, e := os.Lstat(filepath.Join(directory, "current")); e == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return errors.New("unsafe_state")
		}
		previous, e = os.Readlink(filepath.Join(directory, "current"))
		if e != nil || filepath.Base(previous) != previous || !dataTag.MatchString(previous) {
			return errors.New("unsafe_state")
		}
	} else if !os.IsNotExist(e) {
		return errors.New("unsafe_state")
	}
	tag, err := m.geodataReleaseTag(ctx)
	if err != nil {
		return err
	}
	var currentVersion releaseInfo
	if readMaintenanceJSON(filepath.Join(directory, "current", "version.json"), &currentVersion) == nil && currentVersion.Tag == tag {
		valid := true
		for _, name := range []string{"geoip.dat", "geosite.dat"} {
			sums, err := os.ReadFile(filepath.Join(directory, "current", name+".sha256sum"))
			digest, e := checksumFor(sums, name)
			if err != nil || e != nil || verifyChecksum(filepath.Join(directory, "current", name), digest) != nil {
				valid = false
				break
			}
		}
		if valid {
			job.Target = tag
			job.Code = "already_latest"
			return nil
		}
	}
	stage, err := os.MkdirTemp(directory, "data-*")
	if err != nil {
		return errors.New("no_space")
	}
	installed := false
	defer func() {
		if !installed {
			os.RemoveAll(stage)
		}
	}()
	base := geodataReleases + "/download/" + tag + "/"
	if err = m.save(job, "downloading"); err != nil {
		return errors.New("state_failed")
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if err = m.fetch(ctx, base+name+".sha256sum", filepath.Join(stage, name+".sha256sum"), 4096); err != nil {
			return errors.New(downloadFailureCode(err))
		}
		if err = m.fetch(ctx, base+name, filepath.Join(stage, name), 64<<20); err != nil {
			return errors.New(downloadFailureCode(err))
		}
		sums, _ := os.ReadFile(filepath.Join(stage, name+".sha256sum"))
		digest, e := checksumFor(sums, name)
		if e != nil {
			return e
		}
		if e = verifyChecksum(filepath.Join(stage, name), digest); e != nil {
			return e
		}
		if e = validateGeodata(filepath.Join(stage, name), name); e != nil {
			return e
		}
	}
	if err = writeMaintenanceJSON(filepath.Join(stage, "version.json"), releaseInfo{tag, tag, m.now().Unix()}); err != nil {
		return errors.New("state_failed")
	}
	// The control-plane actor activates this pair while reusing its current
	// configuration. It restores the old pointer before a failed load falls
	// back; calling the ordinary run mutation here would apply pending edits.
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if _, e := os.Lstat(filepath.Join(m.configDir, name)); e == nil || !os.IsNotExist(e) {
			return errors.New("custom_geodata")
		}
	}
	if err = m.graph(ctx, `query{healthCheck}`, &state); err != nil {
		return err
	}
	if err = m.save(job, "applying"); err != nil {
		return errors.New("state_failed")
	}
	target := filepath.Base(stage)
	job.Batch, job.Target = target, tag
	if err = m.save(job, "applying"); err != nil {
		return errors.New("state_failed")
	}
	var result map[string]any
	err = m.graph(ctx, `mutation{activateGeodata(target:`+strconv.Quote(target)+`,previous:`+strconv.Quote(previous)+`)}`, &result)
	if err != nil {
		current, _ := os.Readlink(filepath.Join(directory, "current"))
		if err.Error() == "apply_failed" && current == previous {
			return err
		}
		// A lost response is not proof of rollback. Keep both batches so a
		// control-plane operation already in progress can finish safely.
		installed = true
		return errors.New("apply_unconfirmed")
	}
	current, _ := os.Readlink(filepath.Join(directory, "current"))
	if current != target {
		installed = true
		return errors.New("apply_unconfirmed")
	}
	var receipt maintenanceGeodataReceipt
	if readMaintenanceJSON(filepath.Join(directory, "activation.json"), &receipt) != nil || receipt.Target != target || receipt.State != "applied" {
		installed = true
		return errors.New("apply_unconfirmed")
	}
	// Keep only the active pair and one previous pair. Cleanup never traverses
	// symlinks or non-managed paths, and firmware/shared geodata is untouched.
	installed = true
	job.Target = tag
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		if entry.IsDir() && regexp.MustCompile(`^data-[0-9]+$`).MatchString(entry.Name()) && entry.Name() != target && entry.Name() != previous {
			var managed releaseInfo
			if readMaintenanceJSON(filepath.Join(directory, entry.Name(), "version.json"), &managed) == nil && managed.Tag == managed.Version && dataTag.MatchString(managed.Tag) {
				os.RemoveAll(filepath.Join(directory, entry.Name()))
			}
		}
	}
	return nil
}
