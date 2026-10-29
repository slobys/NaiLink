// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeCommon "github.com/daeuniverse/dae/common"
	daeConfig "github.com/daeuniverse/dae/config"
	dnsmessage "github.com/miekg/dns"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

var routerSingBoxBinary = routercore.BinaryPath
var routerExtraRoot = routercore.ExtraRoot
var routerTorDataRoot = "/etc/daed/tor"
var routerCoreRuntimeDir = "/var/run/daed/cores"
var routerCorePreflight = func(ctx context.Context, target string) error {
	command := exec.CommandContext(ctx, "/usr/libexec/daed/check", "--core", target, "--check")
	if err := command.Run(); err != nil {
		return errors.New("core_environment_unsupported")
	}
	return nil
}

func routerSingBoxInstalled() bool {
	st, err := os.Lstat(routerSingBoxBinary)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0022 == 0 && st.Mode().Perm()&0111 != 0 && st.Size() > 0 && st.Size() < 200<<20
}
func routerSingBoxVersion(ctx context.Context) error {
	if !routerSingBoxInstalled() {
		return errors.New("singbox_not_installed")
	}
	// Output is read through a limit. Never surface a core's arbitrary stderr.
	c := exec.CommandContext(ctx, routerSingBoxBinary, "version")
	var output routerCoreBoundedWriter
	c.Stdout = &output
	c.Stderr = io.Discard
	if err := c.Run(); err != nil || !strings.HasPrefix(string(output.data), "sing-box version "+routercore.SingBoxVersion+"\n") {
		return errors.New("singbox_version_mismatch")
	}
	return nil
}

type routerCoreBoundedWriter struct{ data []byte }

func (w *routerCoreBoundedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(w.data) < 65536 {
		limit := 65536 - len(w.data)
		if len(p) > limit {
			p = p[:limit]
		}
		w.data = append(w.data, p...)
	}
	return n, nil
}

type sbLogWriter struct {
	engine              string
	mu                  sync.Mutex
	pending             []byte
	log                 *logrus.Logger
	startupPortConflict *atomic.Bool
}

func (w *sbLogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, data...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := string(w.pending[:i])
		w.pending = w.pending[i+1:]
		level := logrus.InfoLevel
		prefix := line
		if len(prefix) > 20 {
			prefix = prefix[:20]
		}
		switch {
		case strings.Contains(prefix, "ERROR") || strings.Contains(prefix, "FATAL"):
			level = logrus.ErrorLevel
		case strings.Contains(prefix, "WARN"):
			level = logrus.WarnLevel
		case strings.Contains(prefix, "DEBUG") || strings.Contains(prefix, "TRACE"):
			level = logrus.DebugLevel
		}
		if strings.Contains(line, "level=error") || strings.Contains(line, "level=fatal") {
			level = logrus.ErrorLevel
		} else if strings.Contains(line, "level=warning") || strings.Contains(line, "level=warn") {
			level = logrus.WarnLevel
		}
		// Only retain a fixed startup category, never a child log or address.
		if w.startupPortConflict != nil && level == logrus.ErrorLevel {
			text := strings.ToLower(line)
			if strings.Contains(text, "listen ") && strings.Contains(text, "bind: address already in use") {
				w.startupPortConflict.Store(true)
			}
		}
		engine := w.engine
		if engine == "" {
			engine = "sing-box"
		}
		w.log.WithField("core", engine).Log(level, line)
	}
	if len(w.pending) > 32768 {
		w.pending = w.pending[:0]
		w.log.Warn("Oversized sing-box log record omitted")
	}
	return len(data), nil
}
func newCorePorts() (sbPorts, []io.Closer, error) {
	var listeners []io.Closer
	ports := sbPorts{}
	for _, target := range []*int{&ports.Mixed, &ports.API, &ports.DNS} {
		l, udp, err := routerLoopbackPair(net.Listen, net.ListenPacket)
		if err != nil {
			for _, l := range listeners {
				_ = l.Close()
			}
			return ports, nil, err
		}
		listeners = append(listeners, l, udp)
		*target = l.Addr().(*net.TCPAddr).Port
	}
	var secret [24]byte
	if _, err := rand.Read(secret[:]); err != nil {
		for _, l := range listeners {
			_ = l.Close()
		}
		return ports, nil, err
	}
	ports.Secret = hex.EncodeToString(secret[:])
	return ports, listeners, nil
}

func routerListenLoopback(listen func(string, string) (net.Listener, error)) (net.Listener, error) {
	l, err := listen("tcp4", "127.0.0.1:0")
	if err == nil || !errors.Is(err, syscall.EADDRINUSE) {
		return l, err
	}
	// Heavy outbound traffic may occupy the entire automatic ephemeral range
	// with TIME_WAIT sockets. Reserve an explicit loopback port instead; never
	// change system-wide ranges or take an already bound service's listener.
	for range 64 {
		var random [2]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, errors.New("core_ports_unavailable")
		}
		port := 10000 + (int(random[0])<<8|int(random[1]))%20000
		l, err = listen("tcp4", routercore.SocksAddress(port))
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
	}
	return nil, errors.New("core_ports_unavailable")
}

func routerLoopbackPair(listen func(string, string) (net.Listener, error), packet func(string, string) (net.PacketConn, error)) (net.Listener, net.PacketConn, error) {
	for range 32 {
		tcp, err := routerListenLoopback(listen)
		if err != nil {
			return nil, nil, err
		}
		udp, err := packet("udp4", routercore.SocksAddress(tcp.Addr().(*net.TCPAddr).Port))
		if err == nil {
			return tcp, udp, nil
		}
		tcp.Close()
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, nil, err
		}
	}
	return nil, nil, errors.New("core_ports_unavailable")
}
func routerCheckSingBox(ctx context.Context, conf *daeConfig.Config, dirs []string, transparent bool) error {
	profile, _, err := singBoxConfig(conf, dirs, sbPorts{Mixed: 39001, API: 39002, DNS: 39003, Secret: "offline-configuration-check"}, transparent)
	if err != nil {
		return err
	}
	return routerCheckSingBoxProfile(ctx, profile)
}

func routerCheckSingBoxProfile(ctx context.Context, profile sbObject) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	program, _, err := routerPrepareExtra(ctx, profile, false)
	if err != nil {
		return err
	}
	if err = routercore.PrivateDir(routerCoreRuntimeDir); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(routerCoreRuntimeDir, "check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "config.json")
	if err = routercore.Write(path, data); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, program, "check", "--disable-color", "-c", path)
	if err = command.Run(); err != nil {
		if ctx.Err() != nil {
			return errors.New("core_configuration_check_timeout")
		}
		return errors.New("singbox_configuration_invalid")
	}
	return nil
}

type routerSingBoxSession struct {
	engine        string
	dnsBridge     *mhDNSBridge
	nodeDNSBridge *mhDNSBridge
	hasTor        bool
	mu            sync.Mutex
	cmd           *exec.Cmd
	done          chan error
	exit          chan struct{}
	ports         sbPorts
	dir           string
	conf          *daeConfig.Config
	dirs          []string
	transparent   bool
	log           *logrus.Logger
	replacement   *routerSingBoxSession
	owned         bool
	ready         atomic.Bool
	portConflict  atomic.Bool
	apiMu         sync.Mutex
	apiClient     *http.Client
	apiContext    context.Context
	apiCancel     context.CancelFunc
	apiClosed     bool
	statsMu       sync.Mutex
	stats         *RuntimeOverview
	samples       []RuntimeTrafficSample
}

func newRouterSingBoxSession(ctx context.Context, log *logrus.Logger, conf *daeConfig.Config, dirs []string, transparent bool, profileHooks ...func(sbObject, sbPorts)) (routerCoreSession, error) {
	return newRouterNativeSession(ctx, log, conf, dirs, transparent, "sing-box", profileHooks...)
}
func newRouterNativeSession(ctx context.Context, log *logrus.Logger, conf *daeConfig.Config, dirs []string, transparent bool, engine string, profileHooks ...func(sbObject, sbPorts)) (_ routerCoreSession, failure error) {
	originalConf := conf
	conf, nodeDNSBridge, dnsErr := routerWithNodeDNS(conf, log, true)
	if dnsErr != nil {
		return nil, dnsErr
	}
	defer func() {
		if failure != nil && nodeDNSBridge != nil {
			nodeDNSBridge.Close()
		}
	}()
	ports, reserved, err := newCorePorts()
	if err != nil {
		return nil, errors.New("core_ports_unavailable")
	}
	defer func() {
		for _, l := range reserved {
			_ = l.Close()
		}
	}()
	profile, _, err := coreCanonicalConfig(conf, dirs, ports, transparent, engine)
	if err != nil {
		return nil, err
	}
	for _, hook := range profileHooks {
		hook(profile, ports)
	}
	program, hasTor := routerSingBoxBinary, false
	var bridge *mhDNSBridge
	if engine == "mihomo" {
		if err = routerMihomoVersion(ctx); err != nil {
			return nil, err
		}
		if !transparent {
			route := profile["route"].(sbObject)
			if route["auto_detect_interface"] == true {
				iface, e := routerMainWANInterface()
				if e != nil {
					return nil, e
				}
				route["default_interface"] = iface
			}
		}
		program = routerMihomoBinary
		profile, err = mihomoFromCanonical(profile, conf, ports)
		if err != nil {
			return nil, err
		}
		dnslog := log
		if !transparent {
			if manager := currentRouterCoreManager(); manager != nil && manager.log != nil {
				dnslog = manager.log
			}
		}
		bridge, err = startMihomoDNS(profile, ports, dnslog)
		if err != nil {
			return nil, err
		}
		defer func() {
			if failure != nil {
				bridge.Close()
			}
		}()
	} else {
		program, hasTor, err = routerPrepareExtra(ctx, profile, !transparent)
		if err != nil {
			return nil, err
		}
	}
	if err = routercore.PrivateDir(routerCoreRuntimeDir); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(routerCoreRuntimeDir, "active-")
	if err != nil {
		return nil, err
	}
	s := &routerSingBoxSession{engine: engine, dnsBridge: bridge, done: make(chan error, 1), exit: make(chan struct{}), ports: ports, dir: dir, conf: conf, dirs: dirs, transparent: transparent, log: log, hasTor: hasTor}
	s.conf, s.nodeDNSBridge = originalConf, nodeDNSBridge
	data, err := json.Marshal(profile)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	path := filepath.Join(dir, "config.json")
	if err = routercore.Write(path, data); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	check := exec.CommandContext(ctx, program, "check", "--disable-color", "-c", path)
	if engine == "mihomo" {
		check = exec.CommandContext(ctx, program, "-t", "-d", dir, "-f", path)
	}
	if err = check.Run(); err != nil {
		_ = os.RemoveAll(dir)
		if engine == "mihomo" {
			return nil, errors.New("mihomo_configuration_invalid")
		}
		return nil, errors.New("singbox_configuration_invalid")
	}
	if transparent && conf != EmptyConfig {
		if err = routerClaimNativeNetwork(dirs[0], path, engine); err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		s.owned = true
	}
	cleanupFailure := func(cause error) error {
		if s.owned {
			if cleanupErr := routerReleaseSingboxNetwork(dirs[0]); cleanupErr != nil {
				return errors.New("core_cleanup_failed")
			}
		}
		_ = os.RemoveAll(dir)
		return cause
	}
	s.cmd = exec.Command(program, "run", "--disable-color", "-D", dir, "-c", path)
	if engine == "mihomo" {
		s.cmd = exec.Command(program, "-d", dir, "-f", path)
	}
	// TUN and routes are native netlink operations. The manager owns fw4
	// integration; hide external tools from this optional child so its automatic
	// fw4 hook cannot replace shared policies or reload other plugins' rules.
	emptyPath := filepath.Join(dir, "bin")
	if err = os.Mkdir(emptyPath, 0700); err != nil {
		return nil, cleanupFailure(err)
	}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PATH=") {
			s.cmd.Env = append(s.cmd.Env, entry)
		}
	}
	s.cmd.Env = append(s.cmd.Env, "PATH="+emptyPath)
	s.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	writer := &sbLogWriter{log: log, engine: engine, startupPortConflict: &s.portConflict}
	s.cmd.Stdout = writer
	s.cmd.Stderr = writer
	for _, l := range reserved {
		_ = l.Close()
	}
	if bridge != nil {
		bridge.releaseReservations()
	}
	if err = s.cmd.Start(); err != nil {
		return nil, cleanupFailure(errors.New("core_start_failed"))
	}
	if s.owned {
		if err = routerRecordSingboxProcess(dirs[0], s.cmd.Process.Pid, path); err != nil {
			_ = s.cmd.Process.Kill()
			_ = s.cmd.Wait()
			return nil, cleanupFailure(err)
		}
	}
	go func() { err := s.cmd.Wait(); s.done <- err; close(s.exit) }()
	return s, nil
}
func (s *routerSingBoxSession) current() *routerSingBoxSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replacement != nil {
		return s.replacement.current()
	}
	return s
}
func (s *routerSingBoxSession) Done() <-chan error { return s.current().done }
func (s *routerSingBoxSession) controlAPIClient() (*http.Client, context.Context, error) {
	s.apiMu.Lock()
	defer s.apiMu.Unlock()
	if s.apiClosed {
		return nil, nil, errors.New("core_not_ready")
	}
	if s.apiClient == nil {
		s.apiContext, s.apiCancel = context.WithCancel(context.Background())
		s.apiClient = &http.Client{
			Transport: &http.Transport{
				Proxy: nil, MaxConnsPerHost: 2, MaxIdleConns: 2, MaxIdleConnsPerHost: 2,
				IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 2 * time.Second,
				MaxResponseHeaderBytes: 32 << 10, DisableCompression: true,
			},
			Timeout:       2 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return s.apiClient, s.apiContext, nil
}
func (s *routerSingBoxSession) closeControlAPI() {
	s.apiMu.Lock()
	s.apiClosed = true
	client, cancel := s.apiClient, s.apiCancel
	s.apiClient, s.apiContext, s.apiCancel = nil, nil, nil
	s.apiMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if client != nil {
		client.CloseIdleConnections()
	}
}
func (s *routerSingBoxSession) api(ctx context.Context, path string, value any) error {
	client, lifetime, err := s.controlAPIClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, cancel)
	defer func() { stop(); cancel() }()
	request, err := http.NewRequestWithContext(ctx, "GET", "http://"+routercore.SocksAddress(s.ports.API)+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+s.ports.Secret)
	response, err := client.Do(request)
	if err != nil {
		return errors.New("core_not_ready")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("core_not_ready")
	}
	reader := &io.LimitedReader{R: response.Body, N: (2 << 20) + 1}
	if value != nil && json.NewDecoder(reader).Decode(value) != nil {
		if ctx.Err() != nil || lifetime.Err() != nil {
			return errors.New("core_not_ready")
		}
		return errors.New("core_api_invalid")
	}
	// Reading to EOF permits bounded keep-alive reuse, including /version calls
	// that do not consume JSON. A cancelled/stopped session can never reopen it.
	if _, err := io.Copy(io.Discard, reader); err != nil || reader.N == 0 {
		if ctx.Err() != nil || lifetime.Err() != nil {
			return errors.New("core_not_ready")
		}
		return errors.New("core_api_invalid")
	}
	return nil
}
func (s *routerSingBoxSession) Ready(ctx context.Context) error {
	s = s.current()
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	verifyAttempts := 0
	for {
		if s.api(ctx, "/version", nil) == nil && routerNativeListenersReady(ctx, s) {
			if !s.transparent || s.conf == EmptyConfig {
				s.ready.Store(true)
				return nil
			}
			if _, err := net.InterfaceByName(routercore.TunName); err == nil {
				if err = routerRecordRedirectTable(s.dirs[0]); err != nil {
					if err.Error() != "core_not_ready" {
						return err
					}
					select {
					case <-ctx.Done():
						return errors.New("core_not_ready")
					case <-ticker.C:
						continue
					}
				}
				if err = routerVerifySingBox(ctx, s); err == nil {
					s.ready.Store(true)
					return nil
				} else {
					verifyAttempts++
					if verifyAttempts >= 3 || err.Error() != "core_dns_failed" && err.Error() != "core_connectivity_failed" {
						return err
					}
				}
			}
		}
		select {
		case <-s.exit:
			if s.portConflict.Load() {
				return errors.New("core_ports_unavailable")
			}
			return errors.New("core_start_failed")
		case <-ctx.Done():
			return errors.New("core_not_ready")
		case <-ticker.C:
		}
	}
}
func (s *routerSingBoxSession) Stop(ctx context.Context) error {
	s = s.current()
	defer func() {
		if s.dnsBridge != nil {
			s.dnsBridge.Close()
		}
		if s.nodeDNSBridge != nil {
			s.nodeDNSBridge.Close()
		}
	}()
	s.ready.Store(false)
	s.closeControlAPI()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
	}
	select {
	case <-s.exit:
	default:
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(12 * time.Second)
		defer timer.Stop()
		select {
		case <-s.exit:
		case <-timer.C:
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			select {
			case <-s.exit:
			case <-ctx.Done():
				return errors.New("core_cleanup_failed")
			}
		case <-ctx.Done():
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			return errors.New("core_cleanup_failed")
		}
	}
	if s.owned {
		if err := routerReleaseSingboxNetwork(s.dirs[0]); err != nil {
			return err
		}
		s.owned = false
	}
	return os.RemoveAll(s.dir)
}
func (s *routerSingBoxSession) Reload(message *ReloadMessage) error {
	old := s.current()
	conf := message.Config
	var rollback func() error
	var err error
	if message.Geodata != nil {
		conf = old.conf
		rollback, err = routerBeginGeodata(message.Geodata)
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), routerCoreStartTimeout(conf))
	defer cancel()
	if err = routerCheckNative(ctx, conf, old.dirs, old.transparent, old.engine); err != nil {
		if rollback != nil {
			_ = rollback()
		}
		return err
	}
	if err = old.Stop(ctx); err != nil {
		if rollback != nil {
			_ = rollback()
		}
		return err
	}
	candidate, err := newRouterNativeSession(ctx, old.log, conf, old.dirs, old.transparent, old.engine)
	if err == nil {
		err = candidate.Ready(ctx)
	}
	if err == nil && message.Geodata != nil {
		err = routerCommitGeodata(message.Geodata)
	}
	if err == nil {
		s.mu.Lock()
		s.replacement = candidate.(*routerSingBoxSession)
		s.mu.Unlock()
		return nil
	}
	if candidate != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		cleanupErr := candidate.Stop(cleanup)
		stop()
		if cleanupErr != nil {
			return errors.New("core_cleanup_failed")
		}
	}
	if rollback != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return errors.New("core_rollback_failed")
		}
	}
	restoreCtx, stop := context.WithTimeout(context.Background(), routerCoreStartTimeout(old.conf))
	defer stop()
	restored, restoreErr := newRouterNativeSession(restoreCtx, old.log, old.conf, old.dirs, old.transparent, old.engine)
	if restoreErr == nil {
		restoreErr = restored.Ready(restoreCtx)
	}
	if restoreErr != nil {
		if restored != nil {
			_ = restored.Stop(restoreCtx)
		}
		return errors.New("core_rollback_failed")
	}
	s.mu.Lock()
	s.replacement = restored.(*routerSingBoxSession)
	s.mu.Unlock()
	return errors.New("core_reload_rolled_back")
}
func (s *routerSingBoxSession) dial(ctx context.Context, network, address string) (net.Conn, error) {
	dialer, err := proxy.SOCKS5("tcp", routercore.SocksAddress(s.ports.Mixed), &proxy.Auth{User: "nailink", Password: s.ports.Secret}, &net.Dialer{Timeout: 8 * time.Second})
	if err != nil {
		return nil, err
	}
	return dialer.(proxy.ContextDialer).DialContext(ctx, network, address)
}
func RouterCoreDialContext(ctx context.Context, network, address string) (net.Conn, bool, error) {
	m := currentRouterCoreManager()
	if m == nil {
		return nil, false, nil
	}
	m.mu.RLock()
	active := m.active
	current := m.state.Current
	m.mu.RUnlock()
	if current == "dae" {
		return nil, false, nil
	}
	s, ok := active.(*routerSingBoxSession)
	if !ok {
		return nil, true, errors.New("core_not_ready")
	}
	conn, err := s.current().dial(ctx, network, address)
	return conn, true, err
}
func routerVerifySingBox(ctx context.Context, s *routerSingBoxSession) error {
	// Validate real DNS over the private listener, then the configured HTTP target
	// over this core's authenticated SOCKS listener; API availability is insufficient.
	if len(s.conf.Global.TcpCheckUrl) == 0 {
		return errors.New("missing_check_url")
	}
	target := s.conf.Global.TcpCheckUrl[0]
	parsed, parseErr := url.Parse(target)
	if parseErr != nil || parsed.Hostname() == "" {
		return errors.New("invalid_check_url")
	}
	name := parsed.Hostname()
	if net.ParseIP(name) != nil {
		name = "example.com"
	}
	qtype := uint16(dnsmessage.TypeA)
	if s.conf.Dns.IpVersionPrefer == 6 {
		qtype = dnsmessage.TypeAAAA
	}
	question := new(dnsmessage.Msg)
	question.SetQuestion(dnsmessage.Fqdn(name), qtype)
	dnsClient := &dnsmessage.Client{Net: "udp", Timeout: 4 * time.Second}
	answer, _, dnsErr := dnsClient.ExchangeContext(ctx, question, routercore.SocksAddress(s.ports.DNS))
	if dnsErr != nil || answer == nil || answer.Id != question.Id || answer.Rcode != dnsmessage.RcodeSuccess || len(answer.Answer) == 0 || len(answer.Question) != 1 || answer.Question[0] != question.Question[0] {
		return errors.New("core_dns_failed")
	}
	transport := &http.Transport{Proxy: nil, DialContext: s.dial, TLSHandshakeTimeout: 6 * time.Second, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, "HEAD", target, nil)
	if err != nil {
		return errors.New("invalid_check_url")
	}
	response, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		return errors.New("core_connectivity_failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 500 {
		return errors.New("core_connectivity_failed")
	}
	return nil
}

func routerPrepareExtra(ctx context.Context, profile sbObject, probe bool) (string, bool, error) {
	purpose := "main"
	if probe {
		purpose = "probe"
	}
	required, tor, err := routercore.PrepareExtraProfile(profile, routerExtraRoot, routerTorDataRoot, purpose)
	if err != nil {
		return "", false, err
	}
	if !required {
		return routerSingBoxBinary, false, routerSingBoxVersion(ctx)
	}
	directory, err := routercore.ExtraDirectory(routerExtraRoot)
	if err != nil {
		return "", false, err
	}
	program := filepath.Join(directory, "sing-box")
	command := exec.CommandContext(ctx, program, "version")
	var output routerCoreBoundedWriter
	command.Stdout = &output
	command.Stderr = io.Discard
	if command.Run() != nil || !strings.HasPrefix(string(output.data), "sing-box version "+routercore.ExtraVersion+"\n") {
		return "", false, errors.New("singbox_version_mismatch")
	}
	if tor {
		for _, node := range profile["outbounds"].([]sbObject) {
			if node["type"] == "tor" {
				directory := node["data_directory"].(string)
				for _, p := range []string{routerTorDataRoot, filepath.Dir(directory), directory} {
					if err = routercore.PrivateDir(p); err != nil {
						return "", false, err
					}
				}
			}
		}
	}
	return program, tor, nil
}

func routerCoreStartTimeout(conf *daeConfig.Config) time.Duration {
	if conf != nil {
		for _, entry := range conf.Node {
			_, link := daeCommon.GetTagFromLinkLikePlaintext(string(entry))
			node, err := routercore.Node(link, "timeout-check", routercore.NodeOptions{})
			if err == nil {
				for _, out := range routercore.ExpandNode(node) {
					if routercore.NativeFeature(out) == "tor" {
						return 180 * time.Second
					}
				}
			}
		}
	}
	return 45 * time.Second
}

// The controller can bind before DNS and mixed listeners (notably on ARM).
// Confirm local authentication before reporting ready; no user test is timed here.
func routerNativeListenersReady(ctx context.Context, s *routerSingBoxSession) bool {
	d := net.Dialer{Timeout: 250 * time.Millisecond}
	c, e := d.DialContext(ctx, "tcp4", routercore.SocksAddress(s.ports.Mixed))
	if e != nil {
		return false
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(250 * time.Millisecond))
	if _, e = c.Write([]byte{5, 1, 2}); e != nil {
		return false
	}
	var reply [2]byte
	if _, e = io.ReadFull(c, reply[:]); e != nil || reply != [2]byte{5, 2} {
		return false
	}
	user := "nailink"
	auth := append([]byte{1, byte(len(user))}, []byte(user)...)
	auth = append(auth, byte(len(s.ports.Secret)))
	auth = append(auth, []byte(s.ports.Secret)...)
	if _, e = c.Write(auth); e != nil {
		return false
	}
	if _, e = io.ReadFull(c, reply[:]); e != nil || reply != [2]byte{1, 0} {
		return false
	}
	dns, e := d.DialContext(ctx, "tcp4", routercore.SocksAddress(s.ports.DNS))
	if e != nil {
		return false
	}
	dns.Close()
	return true
}
