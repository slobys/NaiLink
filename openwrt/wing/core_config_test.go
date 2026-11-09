// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/pkg/config_parser"
	"github.com/daeuniverse/dae/pkg/geodata"
	"google.golang.org/protobuf/proto"
)

func TestRouterCoreReportsEveryInvalidNodeInChinese(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	conf.Node = []daeConfig.KeyableString{
		"香港:vless://uuid@example.test:443?future_a=do-not-show&future_b=secret",
		"AI:trojan://secret@example.test:443?udp=invalid",
		"第三个:ssr://unsupported",
	}
	before := restartConfigKey(conf)
	_, _, err := singBoxConfig(conf, dirs, sbPorts{}, true)
	var issues sbNodeProblems
	if !errors.As(err, &issues) || len(issues) != 3 {
		t.Fatal("did not collect every node", err)
	}
	joined := strings.Join(issues, " ")
	for _, want := range []string{"香港", "AI", "第三个", "future_a、future_b", "UDP"} {
		if !strings.Contains(joined, want) {
			t.Fatal(joined)
		}
	}
	if strings.Contains(joined, "secret") || strings.Contains(joined, "do-not-show") || strings.Contains(joined, "unsupported_") || strings.Contains(joined, "://") {
		t.Fatal("unsafe/untranslated report", joined)
	}
	if before != restartConfigKey(conf) {
		t.Fatal("report changed source configuration")
	}
}

func TestRouterCoreWorkerProbeInterval(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, options string
		configured, want        time.Duration
	}{
		{"Worker address", "relay.demo.workers.dev", "", 30 * time.Second, 5 * time.Minute},
		{"Worker WebSocket host", "edge.example.test", "&host=relay.demo.workers.dev", 30 * time.Second, 5 * time.Minute},
		{"Worker longer interval", "relay.demo.workers.dev", "", 10 * time.Minute, 10 * time.Minute},
		{"ordinary node", "relay.example.test", "", 30 * time.Second, 30 * time.Second},
		{"lookalike host", "relay.demo.workers.dev.example.test", "", 30 * time.Second, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conf, dirs := coreFixtureConfig(t)
			conf.Group[0].CheckInterval = tc.configured
			conf.Node[0] = daeConfig.KeyableString("fixture:vless://00000000-0000-0000-0000-000000000001@" +
				tc.endpoint + ":443?type=ws&security=tls" + tc.options)
			before := restartConfigKey(conf)
			ports := sbPorts{Mixed: 39001, API: 39002, DNS: 39003, Secret: "fixture"}
			profile, _, err := singBoxConfig(conf, dirs, ports, true)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, outbound := range mhObjects(profile["outbounds"]) {
				if outbound["tag"] == "group:proxy" {
					found = true
					if outbound["type"] != "urltest" || outbound["interval"] != tc.want.String() {
						t.Fatal("sing-box automatic group cadence", outbound)
					}
				}
			}
			if !found {
				t.Fatal("missing sing-box automatic group")
			}
			if tc.name == "Worker address" {
				if binary := os.Getenv("DAED_TEST_SINGBOX"); binary != "" {
					checkWorkerProfile(t, profile, binary, "check", "-c")
				}
			}
			profile, _, err = coreCanonicalConfig(conf, dirs, ports, true, "mihomo")
			if err != nil {
				t.Fatal(err)
			}
			profile, err = mihomoFromCanonical(profile, conf, ports)
			if err != nil {
				t.Fatal(err)
			}
			found = false
			for _, group := range mhObjects(profile["proxy-groups"]) {
				if group["type"] == "url-test" {
					found = true
					if group["interval"] != int(tc.want/time.Second) {
						t.Fatal("mihomo automatic group cadence", group)
					}
				}
			}
			if !found || restartConfigKey(conf) != before {
				t.Fatal("missing mihomo automatic group or source configuration changed")
			}
			if tc.name == "Worker address" {
				if binary := os.Getenv("DAED_TEST_MIHOMO"); binary != "" {
					delete(profile, "_nailink_dns")
					checkWorkerProfile(t, profile, binary, "-t", "-d", t.TempDir(), "-f")
				}
			}
		})
	}
}

func checkWorkerProfile(t *testing.T, profile sbObject, binary string, args ...string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile.json")
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, binary, append(args, path)...).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
}

func TestRouterCorePreservesDomainDialAfterRouteSelection(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	for _, mode := range []string{"domain", "ip"} {
		conf.Global.DialMode = mode
		key := restartConfigKey(conf)
		profile, _, err := singBoxConfig(conf, dirs, sbPorts{}, true)
		if err != nil {
			t.Fatal(err)
		}
		sawProxy := false
		for _, rule := range profile["route"].(sbObject)["rules"].([]sbObject) {
			if outbound, ok := rule["outbound"].(string); ok {
				if outbound == "direct" && rule["override_domain"] != nil {
					t.Fatal("changed direct rule addressing")
				}
				if outbound != "direct" {
					sawProxy = true
					if (rule["override_domain"] == true) != (mode == "domain") {
						t.Fatal("lost explicit dial mode", mode)
					}
				}
			}
		}
		// IP mode may retain the native final route instead of adding a catch-all.
		if mode == "domain" && !sawProxy {
			t.Fatal("missing proxy domain dial route")
		}
		if key != restartConfigKey(conf) {
			t.Fatal("changed source configuration")
		}
	}
	conf.Global.DialMode = "domain++"
	if _, _, err := singBoxConfig(conf, dirs, sbPorts{}, true); err == nil || err.Error() != "unsupported_dial_mode" {
		t.Fatal("silently approximated advanced dial mode", err)
	}
}

func TestRouterCoreExternalGeodataPreservesRulesAndRevision(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	for _, kind := range []string{"geoip", "geosite"} {
		data, err := os.ReadFile(filepath.Join(dirs[0], kind+".dat"))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dirs[0], "adzero-"+kind+".dat"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := &sbConverter{conf: conf, dirs: dirs, geo: map[string]string{}}
	before := restartConfigKey(conf)
	for _, f := range []*config_parser.Function{
		{Name: "domain", Params: []*config_parser.Param{{Key: "ext", Val: "adzero-geosite.dat:cn"}}},
		{Name: "dip", Params: []*config_parser.Param{{Key: "ext", Val: "adzero-geoip.dat:cn"}}},
	} {
		if _, err := c.function(f, false); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.rulesets) != 2 || before != restartConfigKey(conf) {
		t.Fatal("external rules changed source or were dropped")
	}
	for _, bad := range []string{"../geosite.dat:cn", "/etc/geosite.dat:cn", "invalid", "file.json:cn"} {
		if _, err := c.function(&config_parser.Function{Name: "domain", Params: []*config_parser.Param{{Key: "ext", Val: bad}}}, false); err == nil {
			t.Fatal("accepted unsafe external reference", bad)
		}
	}
	old := routercore.Fingerprint(routerCurrentGeodataIdentity(dirs))
	if err := os.WriteFile(filepath.Join(dirs[0], "adzero-geosite.dat"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if old == routercore.Fingerprint(routerCurrentGeodataIdentity(dirs)) {
		t.Fatal("external rule updates did not invalidate plan")
	}
}

func TestRouterCoreNativeCheckTimeoutIsNotConfigurationFailure(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "check-fixture")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo 'sing-box version "+routercore.SingBoxVersion+"'; exit 0; fi\nexec sleep 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	oldBinary, oldDir := routerSingBoxBinary, routerCoreRuntimeDir
	routerSingBoxBinary = binary
	routerCoreRuntimeDir = filepath.Join(root, "private")
	defer func() { routerSingBoxBinary = oldBinary; routerCoreRuntimeDir = oldDir }()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := routerCheckSingBoxProfile(ctx, sbObject{}); err == nil || err.Error() != "core_configuration_check_timeout" {
		t.Fatal(err)
	}
}

func coreFixtureConfig(t *testing.T) (*daeConfig.Config, []string) {
	t.Helper()
	root := t.TempDir()
	ipdata := &geodata.GeoIPList{Entry: []*geodata.GeoIP{{CountryCode: "private", Cidr: []*geodata.CIDR{{Ip: []byte{10, 0, 0, 0}, Prefix: 8}}}, {CountryCode: "cn", Cidr: []*geodata.CIDR{{Ip: []byte{1, 2, 3, 0}, Prefix: 24}}}}}
	domains := &geodata.GeoSiteList{Entry: []*geodata.GeoSite{{CountryCode: "cn", Domain: []*geodata.Domain{{Type: geodata.Domain_RootDomain, Value: "cn"}}}}}
	for name, value := range map[string]proto.Message{"geoip.dat": ipdata, "geosite.dat": domains} {
		data, err := proto.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	text := `global { lan_interface: lan0
 wan_interface: auto
 check_interval: 30s
 tcp_check_url: 'http://cp.cloudflare.com'
 }
 node { fixture: 'socks5://127.0.0.1:1080' }
 group { proxy { filter: name(fixture)
 policy: min_moving_avg
 } }
 dns { upstream { alidns: 'udp://223.5.5.5:53'
 googledns: 'tcp+udp://8.8.8.8:53'
 }
 routing { request { qname(geosite:cn) -> alidns
 fallback: googledns
 } } }
 routing { pname(NetworkManager, systemd-resolved, dnsmasq) -> must_direct
 dip(geoip:private) -> direct
 dip(geoip:cn) -> direct
 domain(geosite:cn) -> direct
 fallback: proxy
 }`
	sections, err := config_parser.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	conf, err := daeConfig.New(sections)
	if err != nil {
		t.Fatal(err)
	}
	return conf, []string{root}
}
func TestRouterCoreDefaultConfigurationConversion(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	original := restartConfigKey(conf)
	profile, warnings, err := singBoxConfig(conf, dirs, sbPorts{Mixed: 39001, API: 39002, DNS: 39003, Secret: "fixture"}, true)
	if err != nil {
		for _, rule := range conf.Routing.Rules {
			t.Logf("outbound: %#v", rule.Outbound)
		}
		t.Fatal(err)
	}
	if len(warnings) < 2 {
		t.Fatal("lost semantic differences")
	}
	if len(profile["inbounds"].([]sbObject)) != 3 {
		t.Fatal("missing TUN or private management listeners")
	}
	if original != restartConfigKey(conf) {
		t.Fatal("changed source config")
	}
	binary := os.Getenv("DAED_TEST_SINGBOX")
	if binary == "" {
		t.Skip("set DAED_TEST_SINGBOX for real native configuration validation")
	}
	oldBinary, oldRuntime := routerSingBoxBinary, routerCoreRuntimeDir
	routerSingBoxBinary = binary
	routerCoreRuntimeDir = t.TempDir()
	if err := os.Chmod(routerCoreRuntimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { routerSingBoxBinary = oldBinary; routerCoreRuntimeDir = oldRuntime })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = routerCheckSingBox(ctx, conf, dirs, true); err != nil {
		path := filepath.Join(t.TempDir(), "config.json")
		data, _ := json.Marshal(profile)
		_ = os.WriteFile(path, data, 0600)
		output, _ := exec.CommandContext(ctx, binary, "check", "--disable-color", "-c", path).CombinedOutput()
		t.Fatalf("%v: %s", err, output)
	}
}
func TestRouterCoreUnsupportedConfigNeverFallsBackToDirect(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	conf.Global.Mptcp = true
	if _, _, err := singBoxConfig(conf, dirs, sbPorts{}, true); err == nil {
		t.Fatal("silently ignored MPTCP")
	}
	conf.Global.Mptcp = false
	conf.Routing.Rules = append(conf.Routing.Rules, &config_parser.RoutingRule{AndFunctions: []*config_parser.Function{{Name: "mac", Params: []*config_parser.Param{{Val: "00:11:22:33:44:55"}}}}, Outbound: config_parser.Function{Name: "direct"}})
	if _, _, err := singBoxConfig(conf, dirs, sbPorts{}, true); err == nil {
		t.Fatal("silently ignored routing constraint")
	}
}
