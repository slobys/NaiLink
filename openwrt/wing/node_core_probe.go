// SPDX-License-Identifier: AGPL-3.0-only
package node

import (
	"context"
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	"sync"
)

func testSingBoxNodes(ctx context.Context, nodes []db.Node) ([]*LatencyResolver, error) {
	engine := dae.RouterCoreName()
	values := make([]*LatencyResolver, len(nodes))
	indices := map[uint]int{}
	for i, n := range nodes {
		indices[n.ID] = i
	}
	var lock sync.Mutex
	err := testSingBoxNodesProgress(ctx, nodes, func(r *LatencyResolver) {
		lock.Lock()
		values[indices[r.NodeID]] = r
		lock.Unlock()
		if ctx.Err() == nil {
			storeCoreLatencyResults(engine, []*LatencyResolver{r})
		}
	})
	return values, err
}
func testSingBoxNodesProgress(ctx context.Context, nodes []db.Node, progress func(*LatencyResolver)) error {
	var model db.Config
	if err := db.DB(ctx).Where("selected = ?", true).First(&model).Error; err != nil {
		return err
	}
	parsed, err := dae.ParseConfig(&model.Global, nil, nil)
	if err != nil {
		return err
	}
	// Resolve once outside the measured request, just as dae manual tests do.
	// Both cores receive the same configured URL, method and available families.
	option, err := latencyProbeOption(ctx)
	if err != nil {
		return err
	}
	parsed.Global.TcpCheckUrl = option.TcpCheckOptionRaw.Raw
	parsed.Global.TcpCheckHttpMethod = option.TcpCheckOptionRaw.Method
	parsed.Global.NailinkNodeDNS, err = db.NodeDNSForNodes(db.DB(ctx), nodes)
	if err != nil {
		return err
	}
	links := make([]string, len(nodes))
	for i, n := range nodes {
		links[i] = n.Link
	}
	_, err = dae.ProbeRouterCoreNodesProgress(ctx, links, parsed.Global, func(i int, result dae.RouterCoreProbe) {
		r := &LatencyResolver{NodeID: nodes[i].ID, AliveVal: result.Alive, TestedAtV: result.Tested, NodeFingerprintV: latencyNodeFingerprint(&nodes[i]), SteadyMsV: result.SteadyMs}
		if result.Alive {
			ms := result.Latency
			r.LatencyMsV = &ms
		} else {
			message := result.Message
			r.MessageV = &message
		}
		progress(r)
	})
	return err
}
