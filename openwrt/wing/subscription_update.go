// SPDX-License-Identifier: AGPL-3.0-only
package subscription

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae-wing/graphql/internal"
	"github.com/daeuniverse/dae-wing/graphql/service/node"
	"gorm.io/gorm/clause"
)

type subscriptionUpdateGate struct {
	token chan struct{}
	refs  int
}

var subscriptionUpdates = struct {
	sync.Mutex
	items map[uint]*subscriptionUpdateGate
}{items: make(map[uint]*subscriptionUpdateGate)}

// Only updates of the same subscription wait for each other. Waiting requests
// remain cancellable, and unused gates are removed after the last waiter exits.
func lockSubscriptionUpdate(ctx context.Context, id uint) (func(), error) {
	subscriptionUpdates.Lock()
	gate := subscriptionUpdates.items[id]
	if gate == nil {
		gate = &subscriptionUpdateGate{token: make(chan struct{}, 1)}
		subscriptionUpdates.items[id] = gate
	}
	gate.refs++
	subscriptionUpdates.Unlock()
	drop := func() {
		subscriptionUpdates.Lock()
		defer subscriptionUpdates.Unlock()
		gate.refs--
		if gate.refs == 0 {
			delete(subscriptionUpdates.items, id)
		}
	}
	select {
	case gate.token <- struct{}{}:
		return func() { <-gate.token; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

func updateSubscriptionSafely(ctx context.Context, id uint) (*db.Subscription, error) {
	unlock, err := lockSubscriptionUpdate(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var before db.Subscription
	if err := db.DB(ctx).First(&before, id).Error; err != nil {
		return nil, err
	}
	dnsContext, nodeDNS := captureNodeDNS(ctx)
	links, err := fetchLinksContext(dnsContext, before.Link)
	if err != nil {
		return nil, err
	}
	tx := db.BeginTx(ctx)
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer tx.Rollback()
	// Fetching happens outside the write transaction. Re-read the row before
	// replacing nodes so a deleted/edited subscription cannot receive stale data.
	var current db.Subscription
	if err := tx.First(&current, id).Error; err != nil {
		return nil, err
	}
	if current.Link != before.Link {
		return nil, errors.New("subscription changed during download; retry the update")
	}
	var existing []db.Node
	if err := tx.Where("subscription_id = ?", id).Find(&existing).Error; err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(existing))
	for _, n := range existing {
		known[n.Link] = true
	}
	subQuery := tx.Raw(`select nodes.id from nodes inner join group_nodes on group_nodes.node_id = nodes.id where subscription_id = ?`, id)
	removed := tx.Where("subscription_id = ?", id).Where("id not in (?)", subQuery).Where("link not in ?", links).
		Select(clause.Associations).Delete(&db.Node{})
	if removed.Error != nil {
		return nil, removed.Error
	}
	args := make([]*internal.ImportArgument, 0, len(links))
	for _, link := range links {
		if !known[link] {
			args = append(args, &internal.ImportArgument{Link: link})
		}
	}
	results, err := node.Import(tx, false, &id, args)
	if err != nil {
		return nil, err
	}
	for _, result := range results {
		if result.Error != nil {
			return nil, errors.New("订阅节点保存失败，已保留原节点；请重试。")
		}
	}
	dnsChanged := current.NodeDNS != nodeDNS.value
	if err := tx.Model(&current).Clauses(clause.Returning{}).Updates(map[string]any{"updated_at": time.Now(), "node_dns": nodeDNS.value}).Error; err != nil {
		return nil, err
	}
	if len(args) > 0 || removed.RowsAffected > 0 || dnsChanged {
		if err := AutoUpdateVersionByIds(tx, []uint{id}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	return &current, nil
}
