// SPDX-License-Identifier: AGPL-3.0-only
package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/graph-gophers/graphql-go"
)

type testJobResult struct {
	ID      graphql.ID `json:"id"`
	Latency *int32     `json:"latencyMs"`
	Alive   bool       `json:"alive"`
	Tested  time.Time  `json:"testedAt"`
	Message *string    `json:"message"`
	Source  string     `json:"source"`
}
type testJobStatus struct {
	ID        string          `json:"id"`
	Core      string          `json:"core"`
	Profile   string          `json:"profile,omitempty"`
	Phase     string          `json:"phase"`
	Code      string          `json:"code,omitempty"`
	Total     int             `json:"total"`
	Completed int             `json:"completed"`
	Cursor    int             `json:"cursor"`
	Results   []testJobResult `json:"results"`
}
type latencyJob struct {
	mu        sync.Mutex
	status    testJobStatus
	operation string
	results   []testJobResult
	seen      map[uint]bool
	cancel    context.CancelFunc
	finished  time.Time
	scope     [32]byte
}

var latencyJobs = struct {
	sync.Mutex
	jobs   map[string]*latencyJob
	active *latencyJob
}{jobs: map[string]*latencyJob{}}

func (j *latencyJob) snapshot(after int) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.status
	if after < 0 || after > len(j.results) {
		after = 0
	}
	s.Completed, s.Cursor = len(j.results), len(j.results)
	s.Results = append([]testJobResult{}, j.results[after:]...)
	data, _ := json.Marshal(s)
	return string(data)
}
func (j *latencyJob) record(ctx context.Context, r *LatencyResolver) {
	if ctx.Err() != nil || dae.RouterCoreName() != j.status.Core || r == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.seen[r.NodeID] {
		return
	}
	j.seen[r.NodeID] = true
	if j.status.Profile != "" {
		if !storeServiceResult(ctx, j.status.Core, j.status.Profile, j.scope, r) {
			return
		}
	} else if !storeCoreLatencyResults(j.status.Core, []*LatencyResolver{r}) {
		return
	}
	// Explicit DTO: never marshal resolvers/models with internal fingerprints or links.
	source := "manual"
	if j.status.Profile != "" {
		source = "service"
	}
	j.results = append(j.results, testJobResult{common.EncodeCursor(r.NodeID), r.LatencyMsV, r.AliveVal, r.TestedAtV, r.MessageV, source})
}

func StartLatencyJob(ctx context.Context, ids []graphql.ID, operation string) (string, error) {
	return startLatencyJob(ctx, ids, operation, "", false)
}
func StartServiceJob(ctx context.Context, ids []graphql.ID, operation, profile string, force bool) (string, error) {
	if profile != "proxy" && profile != "ai" && profile != "netflix" {
		return "", errors.New("invalid_service_profile")
	}
	return startLatencyJob(ctx, ids, operation, profile, force)
}
func startLatencyJob(ctx context.Context, ids []graphql.ID, operation, profile string, force bool) (string, error) {
	if len(ids) == 0 || len(ids) > 1000 || len(operation) < 16 || len(operation) > 64 {
		return "", errors.New("invalid_test_job")
	}
	latencyJobs.Lock()
	defer latencyJobs.Unlock()
	for id, j := range latencyJobs.jobs {
		if j.operation == operation {
			if j.status.Profile != profile {
				return "", errors.New("invalid_test_job")
			}
			return j.snapshot(0), nil
		}
		if !j.finished.IsZero() && time.Since(j.finished) > 5*time.Minute {
			delete(latencyJobs.jobs, id)
		}
	}
	if latencyJobs.active != nil {
		return "", errors.New("latency_job_busy")
	}
	nodes, err := latencyProbeNodes(ctx, &ids)
	if err != nil {
		return "", err
	}
	var scope [32]byte
	if profile != "" {
		scope, err = serviceScope(ctx)
		if err != nil {
			return "", err
		}
	}
	unique := map[graphql.ID]bool{}
	for _, id := range ids {
		unique[id] = true
	}
	if len(nodes) != len(unique) {
		return "", errors.New("invalid_test_job")
	}
	bytes := 0
	for _, n := range nodes {
		bytes += len(n.Link)
	}
	if bytes > 4<<20 {
		return "", errors.New("invalid_test_job")
	}
	if len(latencyJobs.jobs) >= 4 {
		for id, j := range latencyJobs.jobs {
			if !j.finished.IsZero() {
				delete(latencyJobs.jobs, id)
				break
			}
		}
	}
	secret := make([]byte, 16)
	if _, err = rand.Read(secret); err != nil {
		return "", errors.New("test_job_unavailable")
	}
	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Minute)
	j := &latencyJob{status: testJobStatus{ID: hex.EncodeToString(secret), Core: dae.RouterCoreName(), Profile: profile, Phase: "running", Total: len(nodes)}, operation: operation, seen: map[uint]bool{}, cancel: cancel, scope: scope}
	if profile != "" {
		pending := make([]db.Node, 0, len(nodes))
		for _, n := range nodes {
			if cached, ok := cachedServiceResult(j.status.Core, profile, scope, &n, force); ok {
				j.seen[n.ID] = true
				j.results = append(j.results, cached)
			} else {
				pending = append(pending, n)
			}
		}
		nodes = pending
	}
	latencyJobs.jobs[j.status.ID], latencyJobs.active = j, j
	go j.run(jobCtx, nodes)
	return j.snapshot(0), nil
}
func LatencyJob(id string, after int) (string, error) {
	latencyJobs.Lock()
	j := latencyJobs.jobs[id]
	latencyJobs.Unlock()
	if j == nil {
		return "", errors.New("latency_job_expired")
	}
	return j.snapshot(after), nil
}
func CancelLatencyJob(id string) (int32, error) {
	latencyJobs.Lock()
	j := latencyJobs.jobs[id]
	latencyJobs.Unlock()
	if j == nil {
		return 0, errors.New("latency_job_expired")
	}
	j.mu.Lock()
	if j.status.Phase == "running" {
		j.status.Phase = "stopping"
		j.cancel()
	}
	j.mu.Unlock()
	return 1, nil
}

// A caller may lose the start response. The random operation identifies only
// its service job; cancelling it never stops another user's latency job.
func CancelServiceJob(operation string) (int32, error) {
	if len(operation) < 16 || len(operation) > 64 {
		return 0, errors.New("invalid_test_job")
	}
	latencyJobs.Lock()
	defer latencyJobs.Unlock()
	for _, j := range latencyJobs.jobs {
		if j.operation == operation && j.status.Profile != "" {
			j.mu.Lock()
			if j.status.Phase == "running" {
				j.status.Phase = "stopping"
				j.cancel()
			}
			j.mu.Unlock()
			return 1, nil
		}
	}
	return 0, nil
}
func (j *latencyJob) run(ctx context.Context, nodes []db.Node) {
	if j.status.Profile != "" {
		ctx = dae.WithRouterServiceProbe(ctx, j.status.Profile)
	}
	defer j.cancel()
	watchDone := make(chan struct{})
	defer func() {
		close(watchDone)
		if recover() != nil {
			j.mu.Lock()
			j.status.Phase = "failed"
			j.status.Code = "test_job_failed"
			j.mu.Unlock()
		}
		latencyJobs.Lock()
		j.finished = time.Now()
		if latencyJobs.active == j {
			latencyJobs.active = nil
		}
		latencyJobs.Unlock()
	}()
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if dae.RouterCoreName() != j.status.Core {
					j.mu.Lock()
					j.status.Code = "core_changed"
					j.mu.Unlock()
					j.cancel()
					return
				}
			}
		}
	}()
	option, err := interactiveProbeOption(ctx)
	if err == nil && len(nodes) > 0 {
		if j.status.Core != "dae" {
			err = testSingBoxNodesProgress(ctx, nodes, func(r *LatencyResolver) { j.record(ctx, r) })
		} else {
			_, err = runLatencyProbes(ctx, nodes, func(ctx context.Context, n *db.Node) *LatencyResolver {
				r := testSingleNodeLatency(ctx, option, n)
				j.record(ctx, r)
				return r
			})
		}
	}
	j.mu.Lock()
	switch {
	case j.status.Code == "core_changed":
		j.status.Phase = "failed"
	case ctx.Err() == context.Canceled:
		j.status.Phase = "cancelled"
	case ctx.Err() != nil:
		j.status.Phase = "failed"
		j.status.Code = "test_job_timeout"
	case err != nil:
		j.status.Phase = "failed"
		j.status.Code = latencyJobFailure(err)
	case len(j.results) != j.status.Total:
		j.status.Phase = "failed"
		j.status.Code = "test_job_failed"
	default:
		j.status.Phase = "completed"
	}
	j.mu.Unlock()
}

func latencyJobFailure(err error) string {
	if err.Error() == "core_ports_unavailable" {
		return "core_ports_unavailable"
	}
	if strings.Contains(err.Error(), "tcp_check_url") || strings.Contains(err.Error(), "resolve latency check target") {
		return "check_target_failed"
	}
	return "test_job_failed"
}
