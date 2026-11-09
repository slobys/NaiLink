// SPDX-License-Identifier: AGPL-3.0-only
package cmd

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
)

// Daemon startup initially serves EmptyConfig. Its process being alive is not
// evidence that a saved configuration was successfully restored.
func TestFixedConfigurationRestoreStatus(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		running                            bool
		members                            int
		reloadError                        error
		wantReload, wantRunning, wantError bool
	}{
		{"stopped-invalid", false, 2, nil, false, false, false},
		{"running-invalid", true, 2, nil, false, false, true},
		{"running-valid", true, 1, nil, true, true, false},
		{"running-reload-failed", true, 1, io.EOF, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.InitDatabase(t.TempDir()); err != nil {
				t.Fatal(err)
			}
			db.SetOutput(io.Discard)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			d := db.DB(ctx)
			for _, m := range []any{
				&db.Config{ID: 1, Name: "fixture", Global: "global{}", Selected: true},
				&db.Dns{ID: 1, Name: "fixture", Dns: "dns{}", Selected: true},
				&db.Routing{ID: 1, Name: "fixture", Routing: "routing{fallback:proxy}", Selected: true},
				&db.Group{ID: 1, Name: "proxy", Policy: "fixed", Version: 7, PolicyParams: []db.GroupPolicyParam{{Key: "", Value: "0"}}},
				&db.System{Running: tc.running, RunningGroupVersionSum: 4},
			} {
				if err := d.Create(m).Error; err != nil {
					t.Fatal(err)
				}
			}
			var nodes []db.Node
			for i := 1; i <= tc.members; i++ {
				nodes = append(nodes, db.Node{ID: uint(i), Name: "fixture", Link: "vless://33333333-3333-4333-8333-333333333333@192.0.2.1:443#fixture" + string(rune('a'+i))})
			}
			if err := d.Model(&db.Group{ID: 1}).Association("Node").Append(nodes); err != nil {
				t.Fatal(err)
			}
			previous := dae.ChReloadConfigs
			dae.ChReloadConfigs = make(chan *dae.ReloadMessage)
			defer func() { dae.ChReloadConfigs = previous }()
			done := make(chan error, 1)
			go func() { done <- restoreRunningState() }()
			var err error
			select {
			case message := <-dae.ChReloadConfigs:
				if !tc.wantReload {
					t.Error("invalid configuration sent to core")
				}
				if message.Config == dae.EmptyConfig || len(message.Config.Node) != tc.members || len(message.Config.Group) != 1 || message.Config.Group[0].Name != "proxy" {
					t.Fatal("startup did not compile the current selected configuration")
				}
				message.Callback <- tc.reloadError
				err = <-done
			case err = <-done:
				if tc.wantReload {
					t.Error("valid restore did not reload")
				}
			case <-ctx.Done():
				t.Fatal("restore blocked")
			}
			if (err != nil) != tc.wantError || tc.reloadError != nil && !errors.Is(err, tc.reloadError) {
				t.Fatalf("restore error=%v", err)
			}
			var sys db.System
			if err := d.First(&sys).Error; err != nil {
				t.Fatal(err)
			}
			if sys.Running != tc.wantRunning {
				t.Fatalf("process startup misreported applied state: %+v", sys)
			}
			wantVersion := uint(4)
			if tc.wantRunning {
				wantVersion = 7
			}
			if sys.RunningGroupVersionSum != wantVersion {
				t.Fatal("failed restore marked edits as applied")
			}
			var count int64
			if err := d.Table("group_nodes").Count(&count).Error; err != nil || count != int64(tc.members) {
				t.Fatal("restore changed selected nodes", count, err)
			}
			var g db.Group
			if err := d.Preload("PolicyParams").First(&g, 1).Error; err != nil {
				t.Fatal(err)
			}
			if g.Version != 7 || g.Policy != "fixed" || len(g.PolicyParams) != 1 || g.PolicyParams[0].Key != "" || g.PolicyParams[0].Value != "0" {
				t.Fatal("restore changed saved policy")
			}
		})
	}
}
