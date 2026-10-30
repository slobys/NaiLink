// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"errors"
	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/sirupsen/logrus"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRouterTProxyModeSwitchAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		conf, dirs := coreFixtureConfig(t)
		before := restartConfigKey(conf)
		live := 1
		log := logrus.New()
		log.SetOutput(io.Discard)
		m := &routerCoreManager{root: dirs[0], conf: conf, dirs: dirs, log: log, state: routercore.State{Current: "mihomo", Mode: "tun", Phase: "stable"}, phase: "ready", active: &coreFakeSession{live: &live, done: make(chan error)}}
		var modes []string
		m.factory = func(ctx context.Context, name string, c *daeConfig.Config) (routerCoreSession, error) {
			if live != 0 || name != "mihomo" || restartConfigKey(c) != before {
				t.Fatal("overlapping core or changed configuration")
			}
			mode := routerMode(ctx)
			modes = append(modes, mode)
			live++
			s := &coreFakeSession{live: &live, done: make(chan error)}
			if fail && mode == "tproxy" {
				s.readyErr = errors.New("core_dns_failed")
			}
			return s, nil
		}
		err := m.switchPrepared(context.Background(), "mihomo", "mode-test", "tproxy")
		if fail {
			if err == nil || err.Error() != "core_rolled_back" || m.state.Mode != "tun" || strings.Join(modes, ",") != "tproxy,tun" {
				t.Fatal(err, m.state, modes)
			}
		} else {
			if err != nil || m.state.Mode != "tproxy" || strings.Join(modes, ",") != "tproxy" {
				t.Fatal(err, m.state, modes)
			}
		}
		if live != 1 || restartConfigKey(conf) != before {
			t.Fatal("lost working core or changed user config")
		}
		saved, err := routercore.ReadState(dirs[0])
		if err != nil || saved.Mode != m.state.Mode {
			t.Fatal("mode was not durably recorded", err)
		}
	}
}

func TestRouterTProxyConfigurationAndGuards(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	ports := sbPorts{Mixed: 39001, API: 39002, DNS: 39003, TProxy: 39006, Mode: "tproxy", Secret: "test"}
	profile, _, err := coreCanonicalConfig(conf, dirs, ports, true, "sing-box")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, in := range mhObjects(profile["inbounds"]) {
		if in["type"] == "tun" {
			t.Fatal("created TUN in TPROXY mode")
		}
		if in["type"] == "tproxy" {
			found = true
		}
	}
	if !found || mhNumber(profile["route"].(sbObject)["default_mark"]) != routerBypassMark {
		t.Fatal("missing transparent ingress or loop prevention")
	}
	tunPorts := ports
	tunPorts.Mode = "tun"
	tun, _, err := coreCanonicalConfig(conf, dirs, tunPorts, true, "sing-box")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tun["route"].(sbObject)["default_mark"]; ok {
		t.Fatal("TUN auto_redirect conflicts with an explicit default mark")
	}
	mh, err := mihomoFromCanonical(profile, conf, ports)
	if err != nil {
		t.Fatal(err)
	}
	if mh["tun"].(sbObject)["enable"] != false {
		t.Fatal("mihomo TUN still enabled")
	}
	owner := routerCoreNetworkOwner{Mode: "tproxy", ProxyPort: 39006, DNSPort: 39003, LAN: []string{"br-lan", "lan0"}}
	guard, err := routerTPRules(owner, false)
	if err != nil || strings.Contains(guard, "hook output") || !strings.Contains(guard, "drop") {
		t.Fatal("guard must precede interception", err)
	}
	rules, err := routerTPRules(owner, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"tproxy to :39006", "redirect to :39003", "0x4e4102 return", "100.64.0.0/10", "ct direction reply return", "ip daddr @local4 return", "ip6 daddr @private6 return"} {
		if !strings.Contains(rules, text) {
			t.Fatal("missing bypass/ingress", text)
		}
	}
	for _, iface := range []string{"lo", "br-lan\"; drop", "*", "", "../../bad"} {
		owner.LAN = []string{iface}
		if _, err = routerTPRules(owner, true); err == nil {
			t.Fatal("accepted unsafe interface", iface)
		}
	}
}

func TestRouterTProxyNativeNftSyntax(t *testing.T) {
	if os.Getenv("DAED_TEST_TPROXY_NFT") != "1" {
		t.Skip("requires isolated Linux root nftables")
	}
	owner := routerCoreNetworkOwner{Mode: "tproxy", ProxyPort: 39006, DNSPort: 39003, LAN: []string{"br-lan"}}
	rules, err := routerTPRules(owner, true)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("unshare", "--net", "nft", "--check", "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native nft syntax: %v\n%s", err, out)
	}
}

func TestRouterTProxyBridgeScope(t *testing.T) {
	owner := routerCoreNetworkOwner{Mode: "tproxy", ProxyPort: 39006, DNSPort: 39003, LAN: []string{"br-lan"}, Bridges: []routerTPBridge{{Name: "br-lan", MAC: "02:00:00:00:00:01", Ports: []string{"lan-port"}}}}
	rules, err := routerTPBridgeRules(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{`meta ibrname "br-lan" ether daddr 02:00:00:00:00:01`, `meta broute set 1`, `ip daddr @local4 return`, `ip6 daddr @local6 return`, `th dport 53 return`, `100.64.0.0/10`} {
		if !strings.Contains(rules, scope) {
			t.Fatal("missing bridge/management isolation", scope)
		}
	}
	inet, err := routerTPRules(owner, true)
	if err != nil || !strings.Contains(inet, `"lan-port"`) {
		t.Fatal("brouted traffic must be received on its actual ingress", err)
	}
	owner.Bridges[0].Ports = []string{`lan-port"; drop`}
	if _, err = routerTPBridgeRules(owner); err == nil {
		t.Fatal("accepted unsafe bridge member")
	}
	owner.Bridges[0].Ports = []string{"lan-port"}
	owner.Bridges[0].MAC = "wrong"
	if _, err = routerTPBridgeRules(owner); err == nil {
		t.Fatal("accepted unsafe bridge MAC")
	}
	owner.Bridges[0].MAC = "02:00:00:00:00:01"
	owner.Bridges[0].Name = "other-bridge"
	if _, err = routerTPBridgeRules(owner); err == nil {
		t.Fatal("captured an unconfigured bridge")
	}
}
