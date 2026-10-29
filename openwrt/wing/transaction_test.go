// SPDX-License-Identifier: AGPL-3.0-only
package graphql

import (
	"context"
	"io"
	"testing"

	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae-wing/graphql/internal"
	"github.com/daeuniverse/dae-wing/graphql/service/config"
	"github.com/daeuniverse/dae-wing/graphql/service/config/global"
	"github.com/daeuniverse/dae-wing/graphql/service/dns"
	"github.com/daeuniverse/dae-wing/graphql/service/group"
	"github.com/daeuniverse/dae-wing/graphql/service/node"
	"github.com/daeuniverse/dae-wing/graphql/service/routing"
	"github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

const transactionNode = "vless://33333333-3333-4333-8333-333333333333@192.0.2.1:443?encryption=none#fixture"

func TestGroupAddNodesRejectsStaleIDsWithoutCreatingRows(t *testing.T) {
	if err := db.InitDatabase(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	db.SetOutput(io.Discard)
	d := db.DB(context.Background())
	g := db.Group{Name: "fixture", Policy: "fixed"}
	if err := d.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	n, err := db.NewNodeModel(transactionNode, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Create(n).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = group.AddNodes(context.Background(), common.EncodeCursor(g.ID), []graphql.ID{common.EncodeCursor(n.ID), common.EncodeCursor(999999)}); err == nil {
		t.Fatal("stale ID accepted")
	}
	var nodeCount, associationCount int64
	d.Model(&db.Node{}).Count(&nodeCount)
	d.Table("group_nodes").Count(&associationCount)
	if nodeCount != 1 || associationCount != 0 {
		t.Fatal("failed add modified nodes or group", nodeCount, associationCount)
	}
	if _, err = group.AddNodes(context.Background(), common.EncodeCursor(g.ID), []graphql.ID{common.EncodeCursor(n.ID)}); err != nil {
		t.Fatal("valid add failed", err)
	}
	d.Table("group_nodes").Count(&associationCount)
	if associationCount != 1 {
		t.Fatal("successful save missing association")
	}
}

func TestRouterTransactionPanicRollsBack(t *testing.T) {
	if err := db.InitDatabase(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	db.SetOutput(io.Discard)
	d := db.DB(context.Background())
	var caught interface{}
	func() {
		defer func() { caught = recover() }()
		func() (err error) {
			tx := db.BeginTx(context.Background())
			defer db.FinishTransaction(tx, &err)
			if err := tx.Create(&db.Group{Name: "must roll back", Policy: "fixed"}).Error; err != nil {
				t.Fatal(err)
			}
			panic("fixture panic")
		}()
	}()
	if caught != "fixture panic" {
		t.Fatal("panic was swallowed", caught)
	}
	var count int64
	if err := d.Model(&db.Group{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("panic committed a partial change", count, err)
	}
	if err := d.Create(&db.Group{Name: "healthy", Policy: "fixed"}).Error; err != nil {
		t.Fatal("rollback retained database lock", err)
	}
}

func TestRouterMutationCommitFailureIsReported(t *testing.T) {
	for _, item := range []struct {
		name, table, operation string
		run                    func() error
	}{
		{"config", "configs", "update", func() error {
			_, err := config.Update(context.Background(), common.EncodeCursor(1), global.Input{})
			return err
		}},
		{"dns", "dns", "update", func() error { _, err := dns.Update(context.Background(), common.EncodeCursor(1), ""); return err }},
		{"routing", "routings", "update", func() error {
			_, err := routing.Update(context.Background(), common.EncodeCursor(1), "fallback: direct")
			return err
		}},
		{"group-rename", "groups", "update", func() error {
			_, err := group.Rename(context.Background(), common.EncodeCursor(1), "Changed")
			return err
		}},
		{"group-subscription", "groups", "update", func() error {
			_, err := group.AddSubscriptions(context.Background(), common.EncodeCursor(1), []graphql.ID{common.EncodeCursor(1)}, nil)
			return err
		}},
		{"node-import", "nodes", "create", func() error {
			_, err := (&MutationResolver{}).ImportNodes(&struct {
				RollbackError bool
				Args          []*internal.ImportArgument
			}{true, []*internal.ImportArgument{{Link: transactionNode + "-new"}}})
			return err
		}},
		{"node-update", "nodes", "update", func() error {
			_, err := (&MutationResolver{}).UpdateNode(&struct {
				ID      graphql.ID
				NewLink string
			}{common.EncodeCursor(1), transactionNode + "-edited"})
			return err
		}},
		{"node-remove", "nodes", "delete", func() error {
			_, err := node.Remove(context.Background(), []graphql.ID{common.EncodeCursor(1)})
			return err
		}},
		{"run-status", "systems", "update", func() error {
			previous := dae.ChReloadConfigs
			dae.ChReloadConfigs = make(chan *dae.ReloadMessage)
			done := make(chan struct{})
			go func() { defer close(done); message := <-dae.ChReloadConfigs; message.Callback <- nil }()
			_, err := (&MutationResolver{}).Run(&struct{ Dry bool }{true})
			<-done
			dae.ChReloadConfigs = previous
			return err
		}},
	} {
		t.Run(item.name, func(t *testing.T) {
			if err := db.InitDatabase(t.TempDir()); err != nil {
				t.Fatal(err)
			}
			db.SetOutput(io.Discard)
			d := db.DB(context.Background())
			for _, model := range []interface{}{
				&db.Config{ID: 1, Name: "Original", Global: "global{}", Selected: true},
				&db.Dns{ID: 1, Name: "Original", Dns: "dns{}"},
				&db.Routing{ID: 1, Name: "Original", Routing: "routing{}"},
				&db.Group{ID: 1, Name: "Original", Policy: "fixed"},
				&db.Subscription{ID: 1, Link: "https://fixture.invalid"},
				&db.Node{ID: 1, Link: transactionNode, Name: "Original"},
				&db.System{Running: true},
			} {
				if err := d.Create(model).Error; err != nil {
					t.Fatal(err)
				}
			}
			fired := false
			inject := func(query *gorm.DB) {
				if !fired && query.Statement.Table == item.table {
					fired = true
					query.Rollback()
				}
			}
			const callback = "router:commit-failure"
			switch item.operation {
			case "update":
				if err := d.Callback().Update().After("gorm:update").Register(callback, inject); err != nil {
					t.Fatal(err)
				}
				defer d.Callback().Update().Remove(callback)
			case "create":
				if err := d.Callback().Create().After("gorm:create").Register(callback, inject); err != nil {
					t.Fatal(err)
				}
				defer d.Callback().Create().Remove(callback)
			case "delete":
				if err := d.Callback().Delete().After("gorm:delete").Register(callback, inject); err != nil {
					t.Fatal(err)
				}
				defer d.Callback().Delete().Remove(callback)
			}
			if err := item.run(); err == nil {
				t.Error("failed commit was reported as success")
			}
			if !fired {
				t.Fatal("commit failure fixture was not reached")
			}
			var cfg db.Config
			var g db.Group
			var count int64
			if err := d.First(&cfg, 1).Error; err != nil || cfg.Global != "global{}" {
				t.Fatal("configuration was changed", err)
			}
			if err := d.First(&g, 1).Error; err != nil || g.Name != "Original" {
				t.Fatal("group was changed", err)
			}
			if err := d.Model(&db.Node{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatal("nodes were changed", err)
			}
			if err := d.Model(&db.GroupSubscription{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("group binding was changed", err)
			}
		})
	}
}
