// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/pkg/config_parser"
	"github.com/mohae/deepcopy"
	"github.com/sirupsen/logrus"
)

// A session owns all resources created by one active core. The manager never
// imports db: switching cannot rewrite nodes, subscriptions or selected config.
type routerCoreSession interface {
	Reload(*ReloadMessage) error
	Ready(context.Context) error
	Stop(context.Context) error
	Done() <-chan error
}
type routerCoreSwitch struct{ Target, Revision, Operation string }
type routerCoreManager struct {
	mu        sync.RWMutex
	root      string
	log       *logrus.Logger
	dirs      []string
	dry       bool
	conf      *daeConfig.Config
	state     routercore.State
	active    routerCoreSession
	phase     string
	factory   func(context.Context, string, *daeConfig.Config) (routerCoreSession, error)
	preflight func(context.Context, string) error
}

var routerCoreGlobal struct {
	sync.RWMutex
	manager   *routerCoreManager
	recovered *daeConfig.Config
	root      string
	lock      *os.File
}

func InitializeRouterCores(root string) error {
	lock, err := routerCoreManagerLock(root)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			lock.Close()
		}
	}()
	state, err := routercore.ReadState(root)
	if err != nil {
		return err
	}
	if err = routerRecoverSingboxNetwork(root); err != nil {
		return err
	}
	var recovered *daeConfig.Config
	if state.Phase == "switching" || state.Phase == "rollback_failed" {
		data, err := routercore.Read(filepath.Join(routercore.Root(root), "previous.dae"), 16<<20)
		if err != nil {
			return errors.New("core_recovery_snapshot_missing")
		}
		sections, err := config_parser.Parse(string(data))
		if err != nil {
			return errors.New("core_recovery_snapshot_invalid")
		}
		recovered, err = daeConfig.New(sections)
		if err != nil {
			return errors.New("core_recovery_snapshot_invalid")
		}
		if !state.Running {
			recovered = EmptyConfig
		}
	}
	routerCoreGlobal.Lock()
	routerCoreGlobal.root = root
	routerCoreGlobal.recovered = recovered
	routerCoreGlobal.lock = lock
	routerCoreGlobal.Unlock()
	success = true
	return nil
}
func RouterCoreRecovered() bool {
	routerCoreGlobal.RLock()
	defer routerCoreGlobal.RUnlock()
	return routerCoreGlobal.recovered != nil
}
func currentRouterCoreManager() *routerCoreManager {
	routerCoreGlobal.RLock()
	defer routerCoreGlobal.RUnlock()
	return routerCoreGlobal.manager
}
func RouterCoreName() string {
	m := currentRouterCoreManager()
	if m == nil {
		return "dae"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.Current
}
func RouterCoreStatus() string {
	m := currentRouterCoreManager()
	if m == nil {
		return `{"current":"dae","phase":"starting"}`
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var memory *routerCoreMemory
	if m.phase == "ready" {
		memory = routerCoreResidentMemory(m.active, m.state.Current)
	}
	data, _ := json.Marshal(struct {
		routercore.State
		Runtime string            `json:"runtime"`
		Dry     bool              `json:"api_only"`
		Memory  *routerCoreMemory `json:"memory,omitempty"`
	}{m.state, m.phase, m.dry, memory})
	return string(data)
}
func RouterCorePlan(ctx context.Context, target string) routercore.Plan {
	m := currentRouterCoreManager()
	if m == nil {
		return routercore.Plan{Target: target, Problems: []string{"core_starting"}}
	}
	return m.plan(ctx, target)
}
func (m *routerCoreManager) plan(ctx context.Context, target string) routercore.Plan {
	m.mu.RLock()
	conf := m.conf
	current := m.state.Current
	phase := m.phase
	m.mu.RUnlock()
	result := routercore.Plan{Target: target, Warnings: []string{}, Problems: []string{}}
	if !routercore.ValidCore(target) {
		result.Problems = append(result.Problems, "invalid_core")
		return result
	}
	if conf == nil || phase != "ready" {
		result.Problems = append(result.Problems, "core_starting")
		return result
	}
	result.Nodes = len(conf.Node)
	result.Groups = len(conf.Group)
	result.Installed = target == "dae" || target == "sing-box" && routerSingBoxInstalled() || target == "mihomo" && routerMihomoInstalled()
	result.Revision = routercore.Fingerprint([]any{current, target, conf, routerCurrentGeodataIdentity(m.dirs), result.Installed, routercore.SingBoxVersion, routercore.AdapterRevision, routerExtraIdentity(), routercore.MihomoVersion})
	var profile sbObject
	if target == "dae" {
		result.Problems = append(result.Problems, routerDaeNodeProblems(conf)...)
	}
	if target != "dae" {
		var warnings []string
		var err error
		ports := sbPorts{Mixed: 39001, API: 39002, DNS: 39003, Secret: "configuration-check-only"}
		profile, warnings, err = coreCanonicalConfig(conf, m.dirs, ports, !m.dry, target)
		if err == nil && target == "mihomo" {
			profile, err = mihomoFromCanonical(profile, conf, ports)
			for i := range warnings {
				warnings[i] = strings.ReplaceAll(warnings[i], "sing-box", "mihomo")
			}
			warnings = append(warnings, "mihomo 自动选择使用 URL 健康检查；DNS 策略由 NaiLink 私有转发器保留，不迁移原内核 DNS 缓存。")
		}
		result.Warnings = warnings
		if err != nil {
			var nodes sbNodeProblems
			if errors.As(err, &nodes) {
				result.Problems = append(result.Problems, nodes...)
			} else {
				result.Problems = append(result.Problems, routercore.ProblemText(err.Error()))
			}
		}
		if !result.Installed {
			if target == "mihomo" {
				result.Problems = append(result.Problems, "mihomo_not_installed")
			} else {
				result.Problems = append(result.Problems, "singbox_not_installed")
			}
		}
	}
	if err := m.preflight(ctx, target); err != nil {
		result.Problems = append(result.Problems, err.Error())
	}
	if len(result.Problems) == 0 && target != "dae" {
		if err := routerCheckNativeProfile(ctx, profile, target); err != nil {
			result.Problems = append(result.Problems, err.Error())
		}
	}
	result.Compatible = len(result.Problems) == 0
	return result
}
func routerCurrentGeodataIdentity(dirs []string) []any {
	var result []any
	for _, dir := range append(append([]string{}, dirs...), "/usr/share/daed", "/usr/share/v2ray") {
		names := map[string]bool{"geoip.dat": true, "geosite.dat": true}
		if entries, err := os.ReadDir(dir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".dat") {
					names[entry.Name()] = true
				}
			}
		}
		var ordered []string
		for name := range names {
			ordered = append(ordered, name)
		}
		sort.Strings(ordered)
		for _, name := range ordered {
			path := filepath.Join(dir, name)
			if st, err := os.Stat(path); err == nil {
				resolved, _ := filepath.EvalSymlinks(path)
				result = append(result, []any{resolved, st.Size(), st.ModTime().UnixNano()})
			}
		}
	}
	return result
}
func SwitchRouterCore(ctx context.Context, target, revision string, operation ...string) error {
	if !routercore.ValidCore(target) || !routercore.ValidRevision(revision) {
		return errors.New("invalid_core_request")
	}
	callback := make(chan error, 1)
	op := ""
	if len(operation) > 0 {
		op = operation[0]
	}
	if len(op) > 64 {
		return errors.New("invalid_core_request")
	}
	select {
	case ChReloadConfigs <- &ReloadMessage{Core: &routerCoreSwitch{target, revision, op}, Callback: callback}:
	case <-ctx.Done():
		return ctx.Err()
	}
	// Cancellation never abandons a transaction half-way. A buffered callback
	// lets the manager finish rollback even if the requesting worker disappears.
	select {
	case err := <-callback:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *routerCoreManager) save(state routercore.State, phase string) error {
	if err := routercore.SaveState(m.root, state); err != nil {
		return errors.New("core_state_failed")
	}
	m.mu.Lock()
	m.state = state
	m.phase = phase
	m.mu.Unlock()
	return nil
}
func (m *routerCoreManager) start(ctx context.Context, name string, conf *daeConfig.Config) (routerCoreSession, error) {
	ctx, cancel := context.WithTimeout(ctx, routerCoreStartTimeout(conf))
	defer cancel()
	session, err := m.factory(ctx, name, conf)
	if err != nil {
		return nil, err
	}
	if err = session.Ready(ctx); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := session.Stop(stopCtx); stopErr != nil {
			return nil, errors.New("core_cleanup_failed")
		}
		return nil, err
	}
	return session, nil
}
func (m *routerCoreManager) switchTo(ctx context.Context, target, revision string, operation ...string) error {
	plan := m.plan(ctx, target)
	if !plan.Compatible {
		return errors.New("core_incompatible")
	}
	if plan.Revision != revision {
		return errors.New("core_configuration_changed")
	}
	return m.switchPrepared(ctx, target, operation...)
}
func (m *routerCoreManager) switchPrepared(ctx context.Context, target string, operation ...string) error {
	m.mu.RLock()
	oldState, conf, old := m.state, m.conf, m.active
	m.mu.RUnlock()
	if oldState.Current == target {
		return nil
	}
	snapshot, err := conf.Marshal(2)
	if err != nil {
		return errors.New("core_snapshot_failed")
	}
	if err = routercore.Write(filepath.Join(routercore.Root(m.root), "previous.dae"), snapshot); err != nil {
		return errors.New("core_snapshot_failed")
	}
	pending := routercore.State{Current: oldState.Current, Previous: oldState.Current, Target: target, Phase: "switching", Running: conf != EmptyConfig}
	if len(operation) > 0 {
		pending.Operation = operation[0]
	}
	if err = m.save(pending, "switching"); err != nil {
		return err
	}
	if old != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = old.Stop(stopCtx)
		cancel()
		if err != nil {
			pending.Phase = "rollback_failed"
			pending.Code = "core_stop_failed"
			_ = m.save(pending, "failed")
			return errors.New("core_stop_failed")
		}
	}
	m.mu.Lock()
	m.active = nil
	m.mu.Unlock()
	candidate, err := m.start(ctx, target, conf)
	if err == nil {
		// The runtime must be ready before persisting the new selection.
		stable := routercore.State{Current: target, Previous: oldState.Current, Phase: "stable", Running: conf != EmptyConfig}
		stable.Operation = pending.Operation
		err = routercore.SaveState(m.root, stable)
		if err == nil {
			m.mu.Lock()
			m.active = candidate
			m.state = stable
			m.phase = "ready"
			m.mu.Unlock()
			m.log.WithField("core", target).Info("Core switch confirmed")
			return nil
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		stopErr := candidate.Stop(stopCtx)
		cancel()
		if stopErr != nil {
			pending.Phase = "rollback_failed"
			pending.Code = "core_cleanup_failed"
			_ = m.save(pending, "failed")
			return errors.New("core_cleanup_failed")
		}
	}
	if err != nil && err.Error() == "core_cleanup_failed" {
		pending.Phase = "rollback_failed"
		pending.Code = "core_cleanup_failed"
		_ = m.save(pending, "failed")
		return err
	}
	if err != nil {
		code := err.Error()
		if regexp.MustCompile(`^[a-z_]{1,64}$`).MatchString(code) {
			m.log.WithFields(logrus.Fields{"target": target, "code": code}).Warn("Core candidate failed; restoring previous core")
		}
	}
	rollbackCtx, cancel := context.WithTimeout(context.Background(), routerCoreStartTimeout(conf))
	defer cancel()
	restored, restoreErr := m.start(rollbackCtx, oldState.Current, conf)
	pending.Current = oldState.Current
	pending.Target = ""
	pending.Phase = "rolled_back"
	pending.Code = "core_rolled_back"
	if restoreErr != nil {
		pending.Phase = "rollback_failed"
		pending.Code = "core_rollback_failed"
		_ = m.save(pending, "failed")
		return errors.New("core_rollback_failed")
	}
	m.mu.Lock()
	m.active = restored
	m.mu.Unlock()
	if err = m.save(pending, "ready"); err != nil {
		return err
	}
	m.log.WithField("core", oldState.Current).Warn("Core switch failed; previous core restored")
	return errors.New("core_rolled_back")
}
func (m *routerCoreManager) startInitial(state routercore.State, conf *daeConfig.Config) (routerCoreSession, routercore.State, error) {
	ctx, cancel := context.WithTimeout(context.Background(), routerCoreStartTimeout(conf))
	initial, err := m.start(ctx, state.Current, conf)
	cancel()
	if err == nil || state.Current == "dae" || err.Error() == "core_cleanup_failed" {
		return initial, state, err
	}
	failed := state.Current
	candidates := []string{}
	if routercore.ValidCore(state.Previous) && state.Previous != failed {
		candidates = append(candidates, state.Previous)
	}
	if state.Previous != "dae" {
		candidates = append(candidates, "dae")
	}
	for _, fallback := range candidates {
		ctx, cancel = context.WithTimeout(context.Background(), routerCoreStartTimeout(conf))
		initial, err = m.start(ctx, fallback, conf)
		cancel()
		if err != nil {
			if err.Error() == "core_cleanup_failed" {
				return nil, state, err
			}
			continue
		}
		state.Previous, state.Current, state.Target, state.Phase, state.Code = failed, fallback, "", "rolled_back", "core_startup_rolled_back"
		if saveErr := m.save(state, "ready"); saveErr != nil {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = initial.Stop(stopCtx)
			stopCancel()
			return nil, state, saveErr
		}
		return initial, state, nil
	}
	return nil, state, err
}

func Run(log *logrus.Logger, conf *daeConfig.Config, dirs []string, disableTimestamp, dry bool) (err error) {
	defer close(GracefullyExit)
	routerSetGeodataRoot(dirs)
	if err := routerRecoverGeodata(); err != nil {
		return err
	}
	routerCoreGlobal.RLock()
	root, recovered, lock := routerCoreGlobal.root, routerCoreGlobal.recovered, routerCoreGlobal.lock
	routerCoreGlobal.RUnlock()
	if lock != nil {
		defer lock.Close()
	}
	if root == "" {
		if len(dirs) == 0 {
			return errors.New("core_config_directory_missing")
		}
		root = dirs[0]
	}
	state, err := routercore.ReadState(root)
	if err != nil {
		return err
	}
	if recovered != nil {
		conf = recovered
		state.Current = state.Previous
	}
	m := &routerCoreManager{root: root, log: log, dirs: dirs, dry: dry, conf: conf, state: state, phase: "starting"}
	m.preflight = func(ctx context.Context, target string) error {
		if dry {
			return nil
		}
		return routerCorePreflight(ctx, target)
	}
	m.factory = func(ctx context.Context, name string, c *daeConfig.Config) (routerCoreSession, error) {
		if name == "dae" {
			if problems := routerDaeNodeProblems(c); len(problems) > 0 {
				return nil, errors.New(strings.Join(problems, "；"))
			}
			return newRouterDaeSession(log, c, dirs, disableTimestamp, dry), nil
		}
		return newRouterNativeSession(ctx, log, c, dirs, !dry, name)
	}
	routerCoreGlobal.Lock()
	routerCoreGlobal.manager = m
	routerCoreGlobal.Unlock()
	initial, state, err := m.startInitial(state, conf)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.active = initial
	m.mu.Unlock()
	if recovered != nil {
		state.Target = ""
		state.Phase = "rolled_back"
		state.Code = "core_interrupted_recovered"
		if err = m.save(state, "ready"); err != nil {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = initial.Stop(stopCtx)
			stopCancel()
			return err
		}
	} else {
		m.mu.Lock()
		m.phase = "ready"
		m.mu.Unlock()
	}
	var restartWindow time.Time
	restarts := 0
	for {
		m.mu.RLock()
		active := m.active
		m.mu.RUnlock()
		var done <-chan error
		if active != nil {
			done = active.Done()
		}
		select {
		case message := <-ChReloadConfigs:
			if message == nil {
				if active != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					err = active.Stop(ctx)
					cancel()
				}
				return err
			}
			if message.Core != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 260*time.Second)
				err = m.switchTo(ctx, message.Core.Target, message.Core.Revision, message.Core.Operation)
				cancel()
				message.Callback <- err
				continue
			}
			if active == nil {
				message.Callback <- errors.New("core_not_running")
				continue
			}
			err = active.Reload(message)
			m.finishReload(active, message, err)
			message.Callback <- err
		case <-done:
			// A dead core must not leave its network resources in place or silently
			// fall through to a different exit. Keep the API for diagnosis and recovery.
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			stopErr := active.Stop(cleanup)
			cancel()
			m.mu.Lock()
			m.active = nil
			m.phase = "failed"
			m.state.Code = "core_exited"
			m.mu.Unlock()
			if time.Since(restartWindow) > 5*time.Minute {
				restartWindow, restarts = time.Now(), 0
			}
			if stopErr == nil && restarts < 2 {
				restarts++
				if recoverErr := m.recoverExited(); recoverErr == nil {
					continue
				}
			}
			m.mu.RLock()
			failedState := m.state
			m.mu.RUnlock()
			failedState.Phase = "failed"
			if stopErr != nil {
				failedState.Code = "core_cleanup_failed"
			}
			_ = m.save(failedState, "failed")
			log.Error("Active core exited; automatic recovery exhausted or unsafe; management API remains available")
		}
	}
}

func (m *routerCoreManager) finishReload(active routerCoreSession, message *ReloadMessage, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != active {
		return
	}
	if native, ok := active.(*routerSingBoxSession); ok {
		// Reload may replace the process on success or rollback. Do not retain
		// the stopped wrapper and its old DNS matchers, logs and traffic samples.
		m.active = native.current()
	}
	if err == nil && message.Geodata == nil {
		m.conf = message.Config
	}
}

func (m *routerCoreManager) recoverExited() error {
	m.mu.RLock()
	state, conf := m.state, m.conf
	m.mu.RUnlock()
	snapshot, err := conf.Marshal(2)
	if err != nil {
		return err
	}
	if err = routercore.Write(filepath.Join(routercore.Root(m.root), "previous.dae"), snapshot); err != nil {
		return err
	}
	state.Previous, state.Target, state.Phase, state.Running = state.Current, state.Current, "switching", conf != EmptyConfig
	if err = m.save(state, "recovering"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), routerCoreStartTimeout(conf))
	defer cancel()
	session, err := m.start(ctx, state.Current, conf)
	if err != nil {
		state.Phase, state.Code = "rollback_failed", "core_recovery_failed"
		_ = m.save(state, "failed")
		return err
	}
	state.Target, state.Phase, state.Code = "", "stable", "core_restarted"
	if err = m.save(state, "ready"); err != nil {
		_ = session.Stop(ctx)
		return err
	}
	m.mu.Lock()
	m.active = session
	m.mu.Unlock()
	m.log.WithField("core", state.Current).Warn("Exited core restarted and connectivity verified")
	return nil
}

type routerDaeSession struct {
	nodeDNS *mhDNSBridge
	dnsMu   sync.Mutex
	reload  chan *ReloadMessage
	done    chan error
	exit    chan struct{}
	dry     bool
	conf    *daeConfig.Config
}

func newRouterDaeSession(log *logrus.Logger, conf *daeConfig.Config, dirs []string, noTimestamp, dry bool) *routerDaeSession {
	if conf != EmptyConfig {
		conf = deepcopy.Copy(conf).(*daeConfig.Config)
	}
	s := &routerDaeSession{reload: make(chan *ReloadMessage), done: make(chan error, 1), exit: make(chan struct{}), dry: dry, conf: conf}
	effective, bridge, dnsErr := routerWithNodeDNS(conf, log)
	s.conf = effective
	s.nodeDNS = bridge
	go func() {
		err := dnsErr
		if err == nil {
			err = routerRunDAE(log, effective, dirs, noTimestamp, dry, s.reload)
		}
		s.dnsMu.Lock()
		if s.nodeDNS != nil {
			s.nodeDNS.Close()
		}
		s.dnsMu.Unlock()
		s.done <- err
		close(s.exit)
	}()
	return s
}
func (s *routerDaeSession) Done() <-chan error { return s.done }
func (s *routerDaeSession) Ready(ctx context.Context) error {
	if s.dry {
		cb := make(chan error, 1)
		select {
		case s.reload <- &ReloadMessage{Config: s.conf, Callback: cb}:
		case <-s.exit:
			return errors.New("core_start_failed")
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case err := <-cb:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	timer := time.NewTicker(50 * time.Millisecond)
	defer timer.Stop()
	for {
		if status := readRouterDaeReadiness(false); status.Ready && (s.conf == EmptyConfig || status.Configured) {
			return nil
		}
		select {
		case <-s.exit:
			return errors.New("core_start_failed")
		case <-timer.C:
		case <-ctx.Done():
			return errors.New("core_not_ready")
		}
	}
}
func (s *routerDaeSession) Reload(msg *ReloadMessage) error {
	if msg.Config != nil {
		if problems := routerDaeNodeProblems(msg.Config); len(problems) > 0 {
			return errors.New(strings.Join(problems, "；"))
		}
	}
	cb := make(chan error, 1)
	copy := *msg
	copy.Callback = cb
	if copy.Config != nil && copy.Config != EmptyConfig && copy.Geodata == nil {
		copy.Config = deepcopy.Copy(copy.Config).(*daeConfig.Config)
	}
	var bridge *mhDNSBridge
	if copy.Config != nil && copy.Geodata == nil {
		var err error
		copy.Config, bridge, err = routerWithNodeDNS(copy.Config, nil)
		if err != nil {
			return err
		}
	}
	defer func() {
		if bridge != nil {
			bridge.Close()
		}
	}()
	select {
	case s.reload <- &copy:
	case <-s.exit:
		return errors.New("core_exited")
	}
	select {
	case err := <-cb:
		if err == nil && copy.Config != nil && copy.Geodata == nil {
			s.dnsMu.Lock()
			if s.nodeDNS != nil {
				s.nodeDNS.Close()
			}
			s.nodeDNS = bridge
			bridge = nil
			s.dnsMu.Unlock()
		}
		return err
	case <-s.exit:
		return errors.New("core_exited")
	}
}
func (s *routerDaeSession) Stop(ctx context.Context) error {
	select {
	case <-s.exit:
		return nil
	default:
	}
	select {
	case s.reload <- nil:
	case <-s.exit:
		return nil
	case <-ctx.Done():
		return errors.New("core_stop_failed")
	}
	select {
	case <-s.exit:
		return nil
	case <-ctx.Done():
		return errors.New("core_stop_failed")
	}
}

func routerExtraIdentity() string {
	directory, err := routercore.ExtraDirectory(routerExtraRoot)
	if err != nil {
		return "missing"
	}
	return directory
}
