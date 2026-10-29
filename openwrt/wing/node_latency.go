// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2023, daeuniverse Organization <team@v2raya.org>
package node

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae-wing/routercore"
	daeCommon "github.com/daeuniverse/dae/common"
	dialer "github.com/daeuniverse/dae/component/outbound/dialer"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/outbound/netproxy"
	"github.com/graph-gophers/graphql-go"
	"github.com/sirupsen/logrus"
)

const latencyProbeConcurrency = 8
const latencyRequestTimeout = 24 * time.Second

var latencyRequestSlot = make(chan struct{}, latencyProbeConcurrency)
var latencyProbeSlots = make(chan struct{}, latencyProbeConcurrency)
var latencyOptionSlot = make(chan struct{}, 1)
var parseLatencyTarget = dialer.ParseTcpCheckOption
var latencyOptions struct {
	sync.Mutex
	global  string
	option  *dialer.GlobalOption
	err     error
	expires time.Time
}

func TestLatencies(ctx context.Context, ids *[]graphql.ID) ([]*LatencyResolver, error) {
	// Bound work before opening dialers. Older callers receive a useful error,
	// rather than tying up CGI workers until the gateway times out.
	if ids != nil && len(*ids) > latencyProbeConcurrency {
		return nil, fmt.Errorf("test at most 8 nodes per request")
	}
	nodes, err := latencyProbeNodes(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(nodes) > latencyProbeConcurrency {
		return nil, fmt.Errorf("test at most 8 nodes per request")
	}
	if len(nodes) == 0 {
		return []*LatencyResolver{}, nil
	}
	select {
	case latencyRequestSlot <- struct{}{}:
		defer func() { <-latencyRequestSlot }()
	default:
		return nil, fmt.Errorf("latency test already in progress; retry shortly")
	}
	ctx, cancel := context.WithTimeout(ctx, latencyRequestTimeout)
	defer cancel()
	if dae.RouterCoreName() != "dae" {
		return testSingBoxNodes(ctx, nodes)
	}
	option, err := latencyProbeOption(ctx)
	if err != nil {
		return nil, err
	}
	results, err := runLatencyProbes(ctx, nodes, func(ctx context.Context, n *db.Node) *LatencyResolver { return testSingleNodeLatency(ctx, option, n) })
	if err != nil {
		return nil, err
	}
	storeCoreLatencyResults("dae", results)
	return results, nil
}

func runLatencyProbes(ctx context.Context, nodes []db.Node, probe func(context.Context, *db.Node) *LatencyResolver) ([]*LatencyResolver, error) {
	results := make([]*LatencyResolver, len(nodes))
	var wg sync.WaitGroup
	for index := range nodes {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case latencyProbeSlots <- struct{}{}:
				defer func() { <-latencyProbeSlots }()
				results[index] = probe(ctx, &nodes[index])
			case <-ctx.Done():
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// Retained for source compatibility with the upstream cache helpers. Queries
// no longer call those helpers; explicit tests use bounded batches above.
func testLatencyResultsForNodes(option *dialer.GlobalOption, nodes []db.Node) []*LatencyResolver {
	ctx, cancel := context.WithTimeout(context.Background(), latencyRequestTimeout)
	defer cancel()
	if len(nodes) > latencyProbeConcurrency {
		return nil
	}
	results, _ := runLatencyProbes(ctx, nodes, func(ctx context.Context, n *db.Node) *LatencyResolver { return testSingleNodeLatency(ctx, option, n) })
	return results
}

func latencyProbeOption(ctx context.Context) (*dialer.GlobalOption, error) {
	var model db.Config
	if err := db.DB(ctx).Where("selected = ?", true).First(&model).Error; err != nil {
		return nil, err
	}
	select {
	case latencyOptionSlot <- struct{}{}:
		defer func() { <-latencyOptionSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// Share successful target resolution across concurrent one-node requests.
	// A changed configuration bypasses the short cache immediately. Failed DNS
	// is shared for just one second to avoid serial retries by concurrent callers.
	latencyOptions.Lock()
	defer latencyOptions.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if latencyOptions.global == model.Global && time.Now().Before(latencyOptions.expires) {
		return latencyOptions.option, latencyOptions.err
	}
	parsed, err := dae.ParseConfig(&model.Global, nil, nil)
	if err != nil {
		return nil, err
	}
	log := logrus.New()
	log.SetOutput(io.Discard)
	option := dialer.NewGlobalOption(&parsed.Global, log)
	// Resolve with the caller's deadline, then pin the resolved families for
	// this short-lived cache. Option() otherwise starts its own background DNS
	// request and ignores cancellation while every other test waits for it.
	target, err := parseLatencyTarget(ctx, option.TcpCheckOptionRaw.Raw, option.TcpCheckOptionRaw.Method, option.TcpCheckOptionRaw.ResolverNetwork)
	if err == nil {
		raw := []string{option.TcpCheckOptionRaw.Raw[0]}
		for _, address := range []netip.Addr{target.Ip4, target.Ip6} {
			if address.IsValid() {
				raw = append(raw, address.String())
			}
		}
		if len(raw) == 1 {
			err = fmt.Errorf("no target addresses")
		} else {
			option.TcpCheckOptionRaw.Raw = raw
			_, err = option.TcpCheckOptionRaw.Option()
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		failure := fmt.Errorf("cannot resolve latency check target; check DNS or tcp_check_url")
		latencyOptions.global, latencyOptions.option, latencyOptions.err, latencyOptions.expires = model.Global, nil, failure, time.Now().Add(time.Second)
		return nil, failure
	}
	latencyOptions.global, latencyOptions.option, latencyOptions.expires = model.Global, option, time.Now().Add(30*time.Second)
	latencyOptions.err = nil
	return option, nil
}

func latencyProbeNodes(ctx context.Context, ids *[]graphql.ID) ([]db.Node, error) {
	q := db.DB(ctx).Model(&db.Node{})
	if ids != nil {
		decoded, err := common.DecodeCursorBatch(*ids)
		if err != nil {
			return nil, err
		}
		q = q.Where("id in ?", decoded)
	}
	var nodes []db.Node
	if err := q.Find(&nodes).Error; err != nil {
		return nil, err
	}
	return nodes, nil
}

func testSingleNodeLatency(ctx context.Context, option *dialer.GlobalOption, node *db.Node) *LatencyResolver {
	ctx, cancel := context.WithTimeout(ctx, routercore.ManualProbeTimeout+time.Second)
	defer cancel()
	r := &LatencyResolver{NodeID: node.ID, TestedAtV: time.Now(), NodeFingerprintV: latencyNodeFingerprint(node)}
	policies, dnsErr := db.NodeDNSForNodes(db.DB(ctx), []db.Node{*node})
	if dnsErr != nil {
		message := "check_target_failed"
		r.MessageV = &message
		return r
	}
	if policies != "" {
		copy := *option
		resolver, closeDNS, err := dae.RouterProbeNodeDNS(daeConfig.Global{NailinkNodeDNS: policies})
		if err != nil {
			message := "check_target_failed"
			r.MessageV = &message
			return r
		}
		defer closeDNS()
		copy.DaeDNS = resolver
		option = &copy
	}
	if routercore.IsNativeURI(node.Link) {
		message := "unsupported_protocol"
		r.MessageV = &message
		return r
	}
	d, err := dialer.NewFromLinkWithProxyCacheContext(ctx, option, dialer.InstanceOption{DisableCheck: true}, node.Link, "", dialer.NewProxyIpCache())
	if err != nil {
		message := "invalid_node"
		r.MessageV = &message
		return r
	}
	defer d.Close()
	result, err := probeInteractiveHTTP(ctx, d)
	if err != nil {
		message := latencyFailureCode(err)
		if result.Message != "" {
			message = result.Message
		}
		r.MessageV = &message
		return r
	}
	r.AliveVal, r.TestedAtV, r.SteadyMsV = result.Alive, result.Tested, result.SteadyMs
	if result.Alive {
		ms := result.Latency
		r.LatencyMsV = &ms
	} else {
		message := "unreachable"
		r.MessageV = &message
	}
	return r
}

// Adapt the dae protocol dialer without replacing the server hostname or TLS SNI.
type latencyHTTPDialer struct {
	d     *dialer.Dialer
	mark  uint32
	mptcp bool
}

func (d latencyHTTPDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	c, err := d.d.DialContext(ctx, daeCommon.MagicNetwork(network, d.mark, d.mptcp), address)
	if err != nil {
		return nil, err
	}
	return &netproxy.FakeNetConn{Conn: c}, nil
}
func probeInteractiveHTTP(ctx context.Context, d *dialer.Dialer) (dae.RouterCoreProbe, error) {
	network, err := netproxy.ParseMagicNetwork(d.TcpCheckOptionRaw.ResolverNetwork)
	if err != nil {
		return dae.RouterCoreProbe{}, err
	}
	result := dae.ProbeRouterHTTP(ctx, latencyHTTPDialer{d: d, mark: network.Mark, mptcp: network.Mptcp}, daeConfig.Global{TcpCheckUrl: d.TcpCheckOptionRaw.Raw, TcpCheckHttpMethod: d.TcpCheckOptionRaw.Method})
	if !result.Alive {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, fmt.Errorf("%s", result.Message)
	}
	return result, nil
}
func probeInteractiveLatency(ctx context.Context, d *dialer.Dialer) (*dialer.LatencyProbeResult, error) {
	value, err := probeInteractiveHTTP(ctx, d)
	if err != nil {
		return nil, err
	}
	return &dialer.LatencyProbeResult{Alive: value.Alive, Latency: time.Duration(value.Latency) * time.Millisecond, CheckedAt: value.Tested}, nil
}

func latencyFailureCode(err error) string {
	return dae.RouterProbeFailure(err)
}
