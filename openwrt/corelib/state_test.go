// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModeMigrationAndDurableRollback(t *testing.T) {
	root := t.TempDir()
	fresh, err := ReadState(root)
	if err != nil || fresh.Mode != "tproxy" || fresh.Current != "dae" {
		t.Fatal("fresh install must recommend TPROXY without changing the default dae core", err)
	}
	legacy := State{Current: "mihomo", Phase: "stable"}
	if err := SaveState(root, legacy); err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(root)
	if err != nil || CoreMode(state.Mode) != "tun" {
		t.Fatal("upgrade changed legacy TUN", err)
	}
	state.Mode, state.PreviousMode, state.TargetMode = "tun", "tun", "tproxy"
	state.Phase, state.Previous, state.Target = "switching", "mihomo", "mihomo"
	if err = SaveState(root, state); err != nil {
		t.Fatal(err)
	}
	got, err := ReadState(root)
	if err != nil || got != state {
		t.Fatal("lost same-engine mode rollback", err)
	}
	state.Mode = "unsafe"
	if SaveState(root, state) == nil {
		t.Fatal("accepted unknown mode")
	}
}

func TestStateDurabilityAndUnsafePaths(t *testing.T) {
	root := t.TempDir()
	state := State{Current: "dae", Previous: "dae", Target: "sing-box", Phase: "switching", Running: true}
	if err := SaveState(root, state); err != nil {
		t.Fatal(err)
	}
	got, err := ReadState(root)
	if err != nil || got != state {
		t.Fatal(got, err)
	}
	path := filepath.Join(Root(root), "state.json")
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadState(root); err == nil {
		t.Fatal("accepted public state")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(root, "external"), path); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadState(root); err == nil {
		t.Fatal("followed substituted state")
	}
}
