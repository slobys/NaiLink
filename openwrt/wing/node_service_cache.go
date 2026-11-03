// SPDX-License-Identifier: AGPL-3.0-only
package node

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae/component/outbound/dialer"
	"github.com/graph-gophers/graphql-go"
	"github.com/sirupsen/logrus"
	"io"
	"sync"
	"time"
)

var errInvalidServiceProfile = errors.New("invalid_service_profile")

func interactiveProbeOption(ctx context.Context) (*dialer.GlobalOption, error) {
	if dae.RouterServiceProfile(ctx) == "" {
		return latencyProbeOption(ctx)
	}
	var model db.Config
	if err := db.DB(ctx).Where("selected = ?", true).First(&model).Error; err != nil {
		return nil, err
	}
	parsed, err := dae.ParseConfig(&model.Global, nil, nil)
	if err != nil {
		return nil, err
	}
	// The legacy dialer requires a numeric check option even when checks are
	// disabled. Service requests use their own fixed HTTPS host via the dialer;
	// no request is sent to this local placeholder or to the latency-check URL.
	parsed.Global.TcpCheckUrl = []string{"http://127.0.0.1/", "127.0.0.1"}
	log := logrus.New()
	log.SetOutput(io.Discard)
	return dialer.NewGlobalOption(&parsed.Global, log), nil
}

type serviceCacheKey struct {
	Core, Profile      string
	Scope, Fingerprint [32]byte
}

var serviceCache = struct {
	sync.Mutex
	values map[serviceCacheKey]testJobResult
}{values: map[serviceCacheKey]testJobResult{}}

func serviceScope(ctx context.Context) ([32]byte, error) {
	var config db.Config
	err := db.DB(ctx).Where("selected = ?", true).First(&config).Error
	if err != nil {
		return [32]byte{}, err
	}
	var subs []db.Subscription
	if err = db.DB(ctx).Select("id", "node_dns").Order("id").Find(&subs).Error; err != nil {
		return [32]byte{}, err
	}
	data, _ := json.Marshal(struct {
		Global        string
		Subscriptions []db.Subscription
	}{config.Global, subs})
	return sha256.Sum256(data), nil
}
func cachedServiceResult(core, profile string, scope [32]byte, n *db.Node, force bool) (testJobResult, bool) {
	key := serviceCacheKey{core, profile, scope, latencyNodeFingerprint(n)}
	serviceCache.Lock()
	defer serviceCache.Unlock()
	value, ok := serviceCache.values[key]
	if force || time.Since(value.Tested) > 10*time.Minute {
		delete(serviceCache.values, key)
		return testJobResult{}, false
	}
	return value, ok
}
func storeServiceResult(ctx context.Context, core, profile string, scope [32]byte, r *LatencyResolver) bool {
	var current db.Node
	if db.DB(ctx).First(&current, r.NodeID).Error != nil || latencyNodeFingerprint(&current) != r.NodeFingerprintV {
		return false
	}
	key := serviceCacheKey{core, profile, scope, r.NodeFingerprintV}
	serviceCache.Lock()
	defer serviceCache.Unlock()
	for k, v := range serviceCache.values {
		if time.Since(v.Tested) > 10*time.Minute {
			delete(serviceCache.values, k)
		}
	}
	if len(serviceCache.values) >= 2000 {
		for k := range serviceCache.values {
			delete(serviceCache.values, k)
			break
		}
	}
	serviceCache.values[key] = testJobResult{common.EncodeCursor(r.NodeID), r.LatencyMsV, r.AliveVal, r.TestedAtV, r.MessageV, "service"}
	return true
}
func ServiceResults(ctx context.Context, ids []graphql.ID, profile string) (string, error) {
	if len(ids) > 1000 || profile != "proxy" && profile != "ai" && profile != "netflix" {
		return "", errInvalidServiceProfile
	}
	scope, err := serviceScope(ctx)
	if err != nil {
		return "", err
	}
	nodes, err := latencyProbeNodes(ctx, &ids)
	if err != nil {
		return "", err
	}
	result := []testJobResult{}
	for i := range nodes {
		if value, ok := cachedServiceResult(dae.RouterCoreName(), profile, scope, &nodes[i], false); ok {
			result = append(result, value)
		}
	}
	data, _ := json.Marshal(result)
	return string(data), nil
}
