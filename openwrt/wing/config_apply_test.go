// SPDX-License-Identifier: AGPL-3.0-only
package config

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae-wing/graphql/service/group"
	"github.com/graph-gophers/graphql-go"
)

func TestApplyReleasesSnapshotBeforeCoreWait(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		result error
	}{{"success", nil}, {"rollback", io.EOF}} {
		t.Run(fixture.name, func(t *testing.T) { testConcurrentApply(t, fixture.result) })
	}
}

func testConcurrentApply(t *testing.T, reloadError error) {
	t.Helper()
	if err := db.InitDatabase(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	db.SetOutput(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d := db.DB(ctx)
	for _, model := range []interface{}{
		&db.Config{ID: 1, Name: "fixture", Global: "global{}", Selected: true},
		&db.Dns{ID: 1, Name: "fixture", Dns: "dns{}", Selected: true},
		&db.Routing{ID: 1, Name: "fixture", Routing: "routing{fallback:proxy}", Selected: true},
		&db.Group{ID: 1, Name: "proxy", Policy: "random"},
		&db.Node{ID: 1, Name: "fixture", Link: "vless://33333333-3333-4333-8333-333333333333@192.0.2.1:443#fixture"},
		&db.System{Running: true},
	} {
		if err := d.Create(model).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := group.AddNodes(ctx, common.EncodeCursor(1), []graphql.ID{common.EncodeCursor(1)}); err != nil {
		t.Fatal(err)
	}
	var captured db.Group
	if err := d.First(&captured, 1).Error; err != nil {
		t.Fatal(err)
	}
	previous := dae.ChReloadConfigs
	dae.ChReloadConfigs = make(chan *dae.ReloadMessage)
	defer func() { dae.ChReloadConfigs = previous }()
	applied := make(chan error, 1)
	go func() {
		_, err := Run(d, false)
		applied <- err
	}()
	var message *dae.ReloadMessage
	select {
	case message = <-dae.ChReloadConfigs:
	case err := <-applied:
		t.Fatalf("prepare failed: %v", err)
	case <-ctx.Done():
		t.Fatal("apply never reached reload")
	}
	if _, err := Run(d, true); err == nil {
		t.Error("second apply accepted before first acknowledgement")
	}
	removed := make(chan error, 1)
	go func() {
		_, err := group.DelNodes(ctx, common.EncodeCursor(1), []graphql.ID{common.EncodeCursor(1)})
		removed <- err
	}()
	select {
	case err := <-removed:
		if err != nil {
			t.Errorf("edit blocked by apply snapshot: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("node removal waits for core reload while apply holds a read transaction")
	}
	message.Callback <- reloadError
	select {
	case err := <-applied:
		if !errors.Is(err, reloadError) {
			t.Errorf("apply result=%v, expected=%v", err, reloadError)
		}
	case <-ctx.Done():
		t.Error("apply did not finish after core replied")
	}
	var sys db.System
	var current db.Group
	if err := d.First(&sys).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.First(&current, 1).Error; err != nil {
		t.Fatal(err)
	}
	if current.Version != captured.Version+1 {
		t.Fatal("later edit version was overwritten")
	}
	if reloadError == nil && sys.RunningGroupVersionSum != captured.Version {
		t.Fatal("running status no longer describes applied snapshot")
	}
	if reloadError != nil && sys.RunningConfigID != nil {
		t.Fatal("failed apply saved a running configuration")
	}
	var count int64
	if err := d.Model(&db.Node{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("node data changed", count, err)
	}
	if err := d.Table("group_nodes").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("later removal was overwritten", count, err)
	}
}

func TestApplyReloadAcknowledgementSurvivesBrowserCancellation(t *testing.T) {
	previous := dae.ChReloadConfigs
	dae.ChReloadConfigs = make(chan *dae.ReloadMessage)
	defer func() { dae.ChReloadConfigs = previous }()
	request, cancel := context.WithCancel(context.Background())
	defer cancel()
	operation, stop := routerApplyContext(request)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- routerApplyReload(request, operation, dae.EmptyConfig) }()
	message := <-dae.ChReloadConfigs
	cancel()
	select {
	case err := <-done:
		t.Fatalf("accepted reload abandoned: %v", err)
	default:
	}
	message.Callback <- nil
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted reload acknowledgement lost")
	}
}

func TestApplyLateReplyDoesNotBlockCoreManager(t *testing.T) {
	previous := dae.ChReloadConfigs
	dae.ChReloadConfigs = make(chan *dae.ReloadMessage)
	defer func() { dae.ChReloadConfigs = previous }()
	operation, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- routerApplyReload(context.Background(), operation, dae.EmptyConfig) }()
	message := <-dae.ChReloadConfigs
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expired operation reported success")
	}
	select {
	case message.Callback <- nil:
	case <-time.After(time.Second):
		t.Fatal("late acknowledgement blocks core manager")
	}
}

func TestCanceledApplyDoesNotQueueReload(t *testing.T) {
	previous := dae.ChReloadConfigs
	dae.ChReloadConfigs = make(chan *dae.ReloadMessage, 1)
	defer func() { dae.ChReloadConfigs = previous }()
	request, cancel := context.WithCancel(context.Background())
	cancel()
	if err := routerApplyReload(request, context.Background(), dae.EmptyConfig); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(dae.ChReloadConfigs) != 0 {
		t.Fatal("canceled request queued a reload")
	}
}

func TestRecordingRunningGroupsNeverRecreatesDeletedGroup(t *testing.T) {
	if err := db.InitDatabase(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	d := db.DB(context.Background())
	captured := db.Group{ID: 1, Name: "deleted while applying", Policy: "random", Version: 3}
	if err := d.Create(&db.System{ID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.Create(&captured).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(&captured).Error; err != nil {
		t.Fatal(err)
	}
	if err := routerRecordRunningGroups(d, 1, []db.Group{captured}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := d.Model(&db.Group{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("deleted group was recreated", count, err)
	}
}
