// SPDX-License-Identifier: AGPL-3.0-only
// Package routercore contains the shared, database-independent core contract.
package routercore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const SingBoxVersion = "1.14.3-nailink.1"
const SingBoxRelease = "openwrt-v0.6.35"
const AdapterRevision = 9
const BinaryPath = "/usr/libexec/daed/cores/sing-box"
const TunName = "nailink0"

type Release struct{ CPU, Archive, SHA256 string }

var Releases = map[string]Release{
	"amd64": {"x86_64", "nailink-sing-box-1.14.3-nailink.1-x86_64.tar.gz", "084a4e845cc1c470dfb3dbdbb41dfbf5723decaff707d22c9486455fe6eba77f"},
	"arm64": {"arm64", "nailink-sing-box-1.14.3-nailink.1-arm64.tar.gz", "ea0f739684b9418304f2cff773ff1a326d4a926601c2c75af44137b863064fb8"},
}

type State struct {
	Mode         string `json:"mode,omitempty"`
	PreviousMode string `json:"previous_mode,omitempty"`
	TargetMode   string `json:"target_mode,omitempty"`
	Operation    string `json:"operation,omitempty"`
	Current      string `json:"current"`
	Previous     string `json:"previous,omitempty"`
	Target       string `json:"target,omitempty"`
	Phase        string `json:"phase"`
	Code         string `json:"code,omitempty"`
	Running      bool   `json:"running,omitempty"`
}
type Plan struct {
	Mode       string   `json:"mode"`
	Target     string   `json:"target"`
	Revision   string   `json:"revision"`
	Compatible bool     `json:"compatible"`
	Installed  bool     `json:"installed"`
	Warnings   []string `json:"warnings"`
	Problems   []string `json:"problems"`
	Nodes      int      `json:"nodes"`
	Groups     int      `json:"groups"`
}

func ValidCore(name string) bool { return name == "dae" || name == "sing-box" || name == "mihomo" }

// Empty is the legacy TUN selection. Upgrades never change a working datapath.
func CoreMode(mode string) string {
	if mode == "" {
		return "tun"
	}
	return mode
}
func ValidMode(mode string) bool { return CoreMode(mode) == "tun" || mode == "tproxy" }
func Root(config string) string  { return filepath.Join(config, "cores") }

// Never follow substituted private state directories or files.
func PrivateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe_core_state")
	}
	return nil
}
func Write(path string, data []byte) error {
	if err := PrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".core-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func Read(path string, limit int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > limit {
		return nil, errors.New("unsafe_core_state")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("unsafe_core_state")
	}
	return data, err
}
func ReadState(config string) (State, error) {
	state := State{Current: "dae", Phase: "stable"}
	root := Root(config)
	if st, err := os.Lstat(root); err == nil && (!st.IsDir() || st.Mode().Perm()&0077 != 0) {
		return state, errors.New("unsafe_core_state")
	} else if err != nil && !os.IsNotExist(err) {
		return state, err
	}
	data, err := Read(filepath.Join(root, "state.json"), 16384)
	if os.IsNotExist(err) {
		state.Mode = "tproxy"
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if json.Unmarshal(data, &state) != nil || !ValidCore(state.Current) || !ValidMode(state.Mode) || !ValidMode(state.PreviousMode) || !ValidMode(state.TargetMode) ||
		(state.Previous != "" && !ValidCore(state.Previous)) || (state.Target != "" && !ValidCore(state.Target)) {
		return state, errors.New("unsafe_core_state")
	}
	switch state.Phase {
	case "stable", "switching", "rolled_back", "rollback_failed", "failed":
	default:
		return state, errors.New("unsafe_core_state")
	}
	if state.Phase == "switching" && state.Previous == "" {
		return state, errors.New("unsafe_core_state")
	}
	return state, nil
}
func SaveState(config string, state State) error {
	if !ValidCore(state.Current) || !ValidMode(state.Mode) || !ValidMode(state.PreviousMode) || !ValidMode(state.TargetMode) {
		return errors.New("invalid_core")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return Write(filepath.Join(Root(config), "state.json"), data)
}
func Fingerprint(value any) string {
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func ValidRevision(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
