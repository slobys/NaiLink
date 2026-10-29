// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

type routerInteropCase struct {
	Name, Link, Failure, Family, TargetAlt, UDPAlt string
	TCPOnly                                        bool
	Clash                                          routercore.Object
}

// A confirmed listener collision can require a fresh process for the same
// batch. Record each attempted profile's shape rather than treating recovery
// as a new per-node batch. These are private, temporary fixture credentials.
func routerInteropStartTrace(real, trace string) string {
	return fmt.Sprintf(`#!/bin/sh
if [ "$1" = run ]; then
  previous=
  config=
  for argument in "$@"; do
    if [ "$previous" = -c ]; then config=$argument; break; fi
    previous=$argument
  done
  [ -n "$config" ] || exit 96
  /bin/cp "$config" '%s'/start-$$.json || exit 97
  echo $$ >> '%s'/starts
fi
exec '%s' "$@"
`, trace, trace, real)
}

func routerInteropBatchSizes(t *testing.T, trace string) []int {
	t.Helper()
	starts, err := os.ReadFile(filepath.Join(trace, "starts"))
	if err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, pid := range strings.Fields(string(starts)) {
		var profile struct {
			Inbounds []struct{ Users []json.RawMessage }
		}
		data, err := os.ReadFile(filepath.Join(trace, "start-"+pid+".json"))
		if err != nil || json.Unmarshal(data, &profile) != nil || len(profile.Inbounds) == 0 || len(profile.Inbounds[0].Users) < 2 {
			t.Fatal("could not inspect actual private batch shape", err)
		}
		sizes = append(sizes, len(profile.Inbounds[0].Users)-1)
	}
	return sizes
}

// Use independent protocol servers, not mocked dialers or the embedded dae
// implementation. All listeners and credentials belong to the local fixture.
func TestRouterCoreProtocolInteroperability(t *testing.T) {
	path, binary := os.Getenv("DAED_PROTOCOL_CASES"), os.Getenv("DAED_TEST_SINGBOX")
	if path == "" || binary == "" {
		t.Skip("independent servers and optional native core required")
	}
	var fixture struct {
		Target, UDP string
		Cases       []routerInteropCase
		NativeCases []routerInteropCase
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	conf, _ := coreFixtureConfig(t)
	conf.Dns.Upstream = []daeConfig.KeyableString{daeConfig.KeyableString("fixture:udp://" + fixture.UDP)}
	if bundle := os.Getenv("DAED_TEST_SINGBOX_EXTRA"); bundle != "" {
		root := filepath.Join(t.TempDir(), "components")
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		// Keep the production directory contract; the bundle is immutable input.
		if err := os.Symlink(bundle, filepath.Join(root, "extra-fixture")); err != nil {
			t.Fatal(err)
		}
		// ExtraDirectory requires a real child, so copy the small fixture tree.
		_ = os.Remove(filepath.Join(root, "extra-fixture"))
		if out, err := exec.Command("cp", "-a", bundle, filepath.Join(root, "extra-fixture")).CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
		_ = os.Chmod(filepath.Join(root, "extra-fixture"), 0700)
		if err := os.Symlink("extra-fixture", filepath.Join(root, "extra")); err != nil {
			t.Fatal(err)
		}
		oldRoot, oldData := routerExtraRoot, routerTorDataRoot
		routerExtraRoot, routerTorDataRoot = root, filepath.Join(t.TempDir(), "tor-data")
		t.Cleanup(func() { routerExtraRoot, routerTorDataRoot = oldRoot, oldData })
	}
	oldBinary, oldDir := routerSingBoxBinary, routerCoreRuntimeDir
	previous := currentRouterCoreManager()
	routerSingBoxBinary, routerCoreRuntimeDir = binary, filepath.Join(t.TempDir(), "private-core")
	routerCoreGlobal.Lock()
	routerCoreGlobal.manager = &routerCoreManager{conf: conf, state: routercore.State{Current: "sing-box"}}
	routerCoreGlobal.Unlock()
	t.Cleanup(func() {
		routerSingBoxBinary, routerCoreRuntimeDir = oldBinary, oldDir
		routerCoreGlobal.Lock()
		routerCoreGlobal.manager = previous
		routerCoreGlobal.Unlock()
	})
	global := daeConfig.Global{WanInterface: []string{"lo"}, TcpCheckUrl: []string{fixture.Target + "/generate_204"}, UdpCheckDns: []string{fixture.UDP}}
	var links []string
	var cases []routerInteropCase
	for _, c := range append(fixture.Cases, fixture.NativeCases...) {
		if c.Clash != nil {
			var e error
			c.Link, e = routercore.ClashLink(c.Clash)
			if e != nil {
				t.Fatal(c.Name, e)
			}
		}
		if strings.HasPrefix(c.Link, "juicity:") {
			continue
		} // explicitly unsupported by this adapter
		links = append(links, c.Link)
		cases = append(cases, c)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	probeConf, names, _, err := routerProbeConfiguration(links, global)
	if err != nil {
		t.Fatal(err)
	}
	profile, _, err := singBoxConfig(probeConf, nil, sbPorts{Mixed: 41001, API: 41002, DNS: 41003, Secret: "fixture-only"}, false)
	if err != nil {
		t.Fatal(err)
	}
	program, _, err := routerPrepareExtra(ctx, profile, true)
	if err != nil {
		t.Fatal(err)
	}
	checkPath := filepath.Join(t.TempDir(), "profile.json")
	data, err = json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(checkPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, program, "check", "-c", checkPath).CombinedOutput(); err != nil {
		t.Fatalf("native configuration: %s: %v", out, err)
	}
	results, err := ProbeRouterCoreNodes(ctx, links, global)
	if err != nil {
		debugCtx, debugCancel := context.WithTimeout(ctx, 2*time.Second)
		defer debugCancel()
		output, _ := exec.CommandContext(debugCtx, program, "run", "--disable-color", "-c", checkPath).CombinedOutput()
		t.Fatalf("%v: fixture startup: %s", err, output)
	}
	for i, c := range cases {
		if c.TargetAlt != "" {
			local := global
			local.TcpCheckUrl = []string{c.TargetAlt + "/generate_204"}
			measured, e := ProbeRouterCoreNodes(ctx, []string{c.Link}, local)
			if e != nil {
				t.Fatal(e)
			}
			results[i] = measured[0]
		}
	}
	session, err := routerStartProbe(ctx, probeConf)
	if err != nil {
		t.Fatal(err)
	}
	defer routerStopProbe(session)
	udpSeen := map[string]bool{}
	for index, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Failure != "" {
				if results[index].Alive {
					t.Fatal("accepted invalid server certificate")
				}
				return
			}
			if !results[index].Alive {
				t.Fatalf("native HEAD probe failed: %+v", results[index])
			}
			dialer, err := proxy.SOCKS5("tcp", routercore.SocksAddress(session.ports.Mixed), &proxy.Auth{User: names[index], Password: session.ports.Secret}, &net.Dialer{Timeout: 6 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			transport := &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 6 * time.Second}
			target := fixture.Target
			if c.TargetAlt != "" {
				target = c.TargetAlt
			}
			for _, endpoint := range []string{"/payload", "/stream"} {
				response, err := client.Get(target + endpoint)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || string(body) != strings.Repeat("protocol-payload", 4096) {
					t.Fatal("payload/stream mismatch", err)
				}
			}
			family := strings.SplitN(c.Link, ":", 2)[0]
			if c.Family != "" {
				family = c.Family
			}
			if family == "hy2" {
				family = "hysteria2"
			}
			if !c.TCPOnly && !udpSeen[family] {
				udpSeen[family] = true
				udpGlobal := global
				if c.UDPAlt != "" {
					udpGlobal.UdpCheckDns = []string{c.UDPAlt}
				}
				result, err := ProbeRouterCoreUDP(ctx, c.Link, udpGlobal)
				if err != nil || !result.Alive {
					t.Fatal("native UDP DNS failed", result, err)
				}
			}
		})
	}
	t.Run("private-listener-collision", func(t *testing.T) {
		// The preceding real protocols can leave automatic ports in TIME_WAIT.
		// Allocate the occupied fixture reliably without weakening the collision.
		busy, err := routerListenLoopback(net.Listen)
		if err != nil {
			t.Fatal(err)
		}
		defer busy.Close()
		privateConf, _, _, err := routerProbeConfiguration([]string{cases[0].Link}, global)
		if err != nil {
			t.Fatal(err)
		}
		log := logrus.New()
		log.SetOutput(io.Discard)
		attempts := 0
		var failed *routerSingBoxSession
		recovered, err := routerStartPrivateProbe(ctx, func() (routerCoreSession, error) {
			attempts++
			s, e := newRouterNativeSession(ctx, log, privateConf, nil, false, "sing-box", func(profile sbObject, _ sbPorts) {
				if attempts == 1 {
					profile["experimental"].(sbObject)["clash_api"].(sbObject)["external_controller"] = busy.Addr().String()
				}
			})
			if attempts == 1 && e == nil {
				failed = s.(*routerSingBoxSession)
			}
			return s, e
		})
		if err != nil {
			t.Fatal("actual native listener collision did not recover", attempts, err)
		}
		defer routerStopProbe(recovered)
		if attempts != 2 || failed == nil {
			t.Fatal("native startup bypassed the controlled collision", attempts)
		}
		select {
		case <-failed.exit:
		default:
			t.Fatal("failed private process survived its replacement")
		}
		if _, err := os.Stat(failed.dir); !os.IsNotExist(err) {
			t.Fatal("failed private configuration directory survived", err)
		}
		if connection, err := net.DialTimeout("tcp4", busy.Addr().String(), time.Second); err != nil {
			t.Fatal("recovery modified the unrelated occupied listener", err)
		} else {
			connection.Close()
		}
	})
	if os.Getenv("DAED_TEST_SINGBOX_EXTRA") != "" {
		t.Run("bounded-extra-batch", func(t *testing.T) {
			var link string
			for _, c := range cases {
				if c.Name == "naive-cronet" {
					link = c.Link
					break
				}
			}
			if link == "" {
				t.Fatal("missing Naive fixture")
			}
			directory, e := routercore.ExtraDirectory(routerExtraRoot)
			if e != nil {
				t.Fatal(e)
			}
			native := filepath.Join(directory, "sing-box")
			real := filepath.Join(directory, "real-native")
			if e = os.Rename(native, real); e != nil {
				t.Fatal(e)
			}
			defer func() { _ = os.Remove(native); _ = os.Rename(real, native) }()
			trace := t.TempDir()
			script := routerInteropStartTrace(real, trace)
			if e = os.WriteFile(native, []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			input := make([]string, 9)
			for i := range input {
				input[i] = link
			}
			values, e := ProbeRouterCoreNodes(ctx, input, global)
			if e != nil {
				t.Fatal(e)
			}
			for _, value := range values {
				if !value.Alive {
					t.Fatal("extra bulk request failed", value)
				}
			}
			sizes, next := routerInteropBatchSizes(t, trace), 0
			for _, want := range []int{8, 1} {
				attempts := 0
				for next < len(sizes) && sizes[next] == want {
					next++
					attempts++
				}
				if attempts < 1 || attempts > 3 {
					t.Fatal("extra batch shape or retry limit changed", sizes)
				}
			}
			if next != len(sizes) {
				t.Fatal("unexpected per-node processes or interleaved extra batches", sizes)
			}
			t.Log("9 Naive nodes used sequential 8+1 batches; startup attempts:", sizes)
		})
	}

}

// A large batch shares one process; an invalid native config must not poison it.
func TestRouterCoreBulkProbe(t *testing.T) {
	path, binary := os.Getenv("DAED_PROTOCOL_CASES"), os.Getenv("DAED_TEST_SINGBOX")
	if path == "" || binary == "" {
		t.Skip("independent native server required")
	}
	var fixture struct {
		Target, UDP string
		Cases       []routerInteropCase
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var good string
	for _, c := range fixture.Cases {
		if c.Name == "aes-128-gcm" {
			good = c.Link
		}
	}
	if good == "" {
		t.Fatal("missing fixture")
	}
	conf, _ := coreFixtureConfig(t)
	conf.Dns.Upstream = []daeConfig.KeyableString{daeConfig.KeyableString("fixture:udp://" + fixture.UDP)}
	oldBinary, oldDir := routerSingBoxBinary, routerCoreRuntimeDir
	previous := currentRouterCoreManager()
	work := t.TempDir()
	trace := t.TempDir()
	wrapper := filepath.Join(work, "native-wrapper")
	if err = os.WriteFile(wrapper, []byte(routerInteropStartTrace(binary, trace)), 0700); err != nil {
		t.Fatal(err)
	}
	routerSingBoxBinary, routerCoreRuntimeDir = wrapper, filepath.Join(work, "private-core")
	routerCoreGlobal.Lock()
	routerCoreGlobal.manager = &routerCoreManager{conf: conf, state: routercore.State{Current: "sing-box"}}
	routerCoreGlobal.Unlock()
	t.Cleanup(func() {
		routerSingBoxBinary, routerCoreRuntimeDir = oldBinary, oldDir
		routerCoreGlobal.Lock()
		routerCoreGlobal.manager = previous
		routerCoreGlobal.Unlock()
	})
	global := daeConfig.Global{TcpCheckUrl: []string{fixture.Target + "/generate_204"}, UdpCheckDns: []string{fixture.UDP}}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	links := make([]string, 72)
	for i := range links {
		links[i] = good + fmt.Sprintf("#node-%d", i)
	}
	var progress atomic.Int32
	started := time.Now()
	results, err := ProbeRouterCoreNodesProgress(ctx, links, global, func(_ int, r RouterCoreProbe) {
		if r.Alive {
			progress.Add(1)
		}
	})
	if err != nil || progress.Load() != 72 {
		t.Fatalf("batch: %v, alive %d", err, progress.Load())
	}
	for _, r := range results {
		if !r.Alive {
			t.Fatalf("result: %+v", r)
		}
	}
	sizes := routerInteropBatchSizes(t, trace)
	if len(sizes) < 1 || len(sizes) > 3 {
		t.Fatal("ordinary batch exceeded its startup attempt limit", sizes)
	}
	for _, size := range sizes {
		if size != 72 {
			t.Fatal("ordinary nodes no longer share a single batch", sizes)
		}
	}
	t.Logf("72-node batch: one successful native process, %d startup attempts, %s", len(sizes), time.Since(started))
	bad, err := routercore.NativeLink(`{"type":"shadowsocks","server":"127.0.0.1","server_port":12345,"method":"invalid-cipher","password":"fixture"}`)
	if err != nil {
		t.Fatal(err)
	}
	results, err = ProbeRouterCoreNodes(ctx, []string{good, bad}, global)
	if err != nil || !results[0].Alive || results[1].Alive || results[1].Message != "invalid_native_config" {
		t.Fatalf("bad node isolation: %+v %v", results, err)
	}
	cancelCtx, stop := context.WithCancel(ctx)
	_, err = ProbeRouterCoreNodesProgress(cancelCtx, links, global, func(_ int, r RouterCoreProbe) {
		if r.Alive {
			stop()
		}
	})
	stop()
	if err != context.Canceled {
		t.Fatalf("cancelled batch: %v", err)
	}
	entries, err := os.ReadDir(routerCoreRuntimeDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private native sessions were not removed: %d %v", len(entries), err)
	}
}
