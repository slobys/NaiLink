// SPDX-License-Identifier: AGPL-3.0-only
package node

import (
	"context"
	"encoding/json"
	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/graph-gophers/graphql-go"
	"strings"
	"testing"
	"time"
)

func TestServiceCacheScopeProfileAndNodeIsolation(t *testing.T) {
	ids := jobFixture(t)
	ctx := context.Background()
	scope, err := serviceScope(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var n db.Node
	db.DB(ctx).First(&n, 1)
	message := "service_accessible"
	r := &LatencyResolver{NodeID: n.ID, NodeFingerprintV: latencyNodeFingerprint(&n), TestedAtV: time.Now(), AliveVal: true, MessageV: &message}
	if !storeServiceResult(ctx, "dae", "ai", scope, r) {
		t.Fatal("could not cache result")
	}
	if _, ok := cachedServiceResult("dae", "ai", scope, &n, false); !ok {
		t.Fatal("lost result")
	}
	for _, p := range []struct{ core, profile string }{{"sing-box", "ai"}, {"dae", "netflix"}} {
		if _, ok := cachedServiceResult(p.core, p.profile, scope, &n, false); ok {
			t.Fatal("cross-profile or core cache")
		}
	}
	n.Link += "changed"
	if _, ok := cachedServiceResult("dae", "ai", scope, &n, false); ok {
		t.Fatal("stale node reused")
	}
	if storeServiceResult(ctx, "dae", "ai", scope, &LatencyResolver{NodeID: n.ID, NodeFingerprintV: latencyNodeFingerprint(&n)}) {
		t.Fatal("changed-node result committed")
	}
	db.DB(ctx).Model(&db.Config{}).Where("selected = ?", true).Update("global", "global {log_level: warn}")
	changed, e := serviceScope(ctx)
	if e != nil || changed == scope {
		t.Fatal("DNS/global scope not invalidated", e)
	}
	if _, ok := cachedServiceResult("dae", "ai", changed, &n, false); ok {
		t.Fatal("old DNS reused")
	}
	if _, e = ServiceResults(ctx, []graphql.ID{}, "ai"); e != nil {
		t.Fatal(e)
	}
	_ = ids
}
func TestServiceJobCachedCompletionAndNoLatencyPollution(t *testing.T) {
	ids := jobFixture(t)
	ctx := context.Background()
	scope, _ := serviceScope(ctx)
	for _, id := range ids {
		decoded, _ := common.DecodeCursor(id)
		var n db.Node
		db.DB(ctx).First(&n, decoded)
		message := "service_accessible"
		storeServiceResult(ctx, "dae", "proxy", scope, &LatencyResolver{NodeID: n.ID, NodeFingerprintV: latencyNodeFingerprint(&n), TestedAtV: time.Now(), AliveVal: true, MessageV: &message})
	}
	raw, err := StartServiceJob(ctx, ids, strings.Repeat("s", 32), "proxy", false)
	if err != nil {
		t.Fatal(err)
	}
	var first testJobStatus
	if err = json.Unmarshal([]byte(raw), &first); err != nil {
		t.Fatal(err)
	}
	s := waitJob(t, first.ID)
	if s.Phase != "completed" || s.Completed != len(ids) || s.Profile != "proxy" {
		t.Fatal("cached job failed", s.Phase, s.Completed)
	}
	if _, err = StartLatencyJob(ctx, ids, strings.Repeat("s", 32)); err == nil {
		t.Fatal("latency reused service operation")
	}
	for _, r := range s.Results {
		if r.Source != "service" {
			t.Fatal("mixed ordinary latency")
		}
	}
	if _, err = CancelServiceJob(strings.Repeat("x", 32)); err != nil {
		t.Fatal("unknown cancellation not idempotent")
	}
}
