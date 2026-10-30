// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/vishvananda/netlink"
)

const routerTPTable = 20342
const routerTPMark = 0x4e4104
const routerBypassMark = 0x4e4102
const routerTPNft = "nailink_tproxy"

type routerModeKey struct{}

func routerModeContext(ctx context.Context, mode string) context.Context {
	return context.WithValue(ctx, routerModeKey{}, routercore.CoreMode(mode))
}
func routerMode(ctx context.Context) string {
	mode, _ := ctx.Value(routerModeKey{}).(string)
	return routercore.CoreMode(mode)
}
func routerTPIPv6() bool {
	b, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/disable_ipv6")
	return err == nil && strings.TrimSpace(string(b)) == "0"
}
func routerTProxyListenAddress() string {
	if routerTPIPv6() {
		return "::"
	}
	return "0.0.0.0"
}

// Reserve both transports on the same dual-stack wildcard before releasing
// them to the child. Management ports remain authenticated and loopback-only.
func routerTPReserve() (int, []io.Closer, error) {
	for range 32 {
		tcp, err := net.Listen("tcp", net.JoinHostPort(routerTProxyListenAddress(), "0"))
		if err != nil {
			return 0, nil, err
		}
		port := tcp.Addr().(*net.TCPAddr).Port
		udp, err := net.ListenPacket("udp", net.JoinHostPort(routerTProxyListenAddress(), fmt.Sprint(port)))
		if err == nil {
			return port, []io.Closer{tcp, udp}, nil
		}
		tcp.Close()
		if !errors.Is(err, syscall.EADDRINUSE) {
			return 0, nil, err
		}
	}
	return 0, nil, errors.New("core_ports_unavailable")
}

func routerTPNftRun(ctx context.Context, rules string, check bool) error {
	args := []string{"-f", "-"}
	if check {
		args = []string{"--check", "-f", "-"}
	}
	cmd := exec.CommandContext(ctx, "nft", args...)
	cmd.Stdin = strings.NewReader(rules)
	if err := cmd.Run(); err != nil {
		return errors.New("tproxy_environment_unsupported")
	}
	return nil
}

func routerTPValidateOwner(owner routerCoreNetworkOwner) error {
	if owner.ProxyPort < 1 || owner.ProxyPort > 65535 || owner.DNSPort < 1 || owner.DNSPort > 65535 || owner.ProxyPort == owner.DNSPort || len(owner.LAN) == 0 || len(owner.LAN) > 32 {
		return errors.New("core_network_ownership_failed")
	}
	valid := regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,15}$`)
	if err := routerTPValidateBridges(owner); err != nil {
		return err
	}
	for _, iface := range routerTPIngress(owner) {
		if !valid.MatchString(iface) || iface == "lo" {
			return errors.New("missing_lan_interface")
		}
	}
	return nil
}

// Dedicated table; fw4 and other plugins are never flushed or edited. The
// guard is installed before the unauthenticated DNS/transparent listeners bind.
func routerTPRules(owner routerCoreNetworkOwner, active bool) (string, error) {
	if err := routerTPValidateOwner(owner); err != nil {
		return "", err
	}
	var names []string
	for _, name := range routerTPIngress(owner) {
		names = append(names, fmt.Sprintf("%q", name))
	}
	lan := "{ " + strings.Join(names, ", ") + " }"
	rules := fmt.Sprintf(`table inet %s {
 chain listener_guard {
  type filter hook input priority -155; policy accept;
  meta l4proto { tcp, udp } th dport %d meta mark != 0x%x drop
  meta l4proto { tcp, udp } th dport %d iifname != "lo" iifname != %s drop
 }
`, routerTPNft, owner.ProxyPort, routerTPMark, owner.DNSPort, lan)
	if active {
		local4, local6, err := routerTPLocalAddresses()
		if err != nil {
			return "", err
		}
		rules += fmt.Sprintf(`
 set local4 { type ipv4_addr; elements = { %s } }
 set local6 { type ipv6_addr; elements = { %s } }
 set private4 { type ipv4_addr; flags interval; elements = { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 198.18.0.0/15, 224.0.0.0/3 } }
 set private6 { type ipv6_addr; flags interval; elements = { ::/128, ::1/128, fc00::/7, fe80::/10, ff00::/8 } }
 chain dns_lan {
  type nat hook prerouting priority -101; policy accept;
  meta mark 0x%x return
  ip daddr 100.100.100.100 return
  iifname %s meta l4proto { tcp, udp } th dport 53 redirect to :%d
 }
 chain dns_local {
  type nat hook output priority -101; policy accept;
  meta mark 0x%x return
  ip daddr 100.100.100.100 return
  meta l4proto { tcp, udp } th dport 53 redirect to :%d
 }
 chain intercept {
  type filter hook prerouting priority -150; policy accept;
  meta mark 0x%x return
  ct direction reply return
  meta l4proto { tcp, udp } th dport 53 return
  iifname "lo" meta mark 0x%x meta l4proto { tcp, udp } tproxy to :%d accept
  iifname != %s return
  ip daddr @local4 return
  ip6 daddr @local6 return
  ip daddr @private4 return
  ip6 daddr @private6 return
  meta l4proto { tcp, udp } meta mark set 0x%x tproxy to :%d accept
 }
 chain router_output {
  type route hook output priority -150; policy accept;
  meta mark 0x%x return
  ct direction reply return
  meta l4proto { tcp, udp } th dport 53 return
  ip daddr @local4 return
  ip6 daddr @local6 return
  ip daddr @private4 return
  ip6 daddr @private6 return
  meta l4proto { tcp, udp } meta mark set 0x%x
 }
`, local4, local6, routerBypassMark, lan, owner.DNSPort, routerBypassMark, owner.DNSPort, routerBypassMark, routerTPMark, owner.ProxyPort, lan, routerTPMark, owner.ProxyPort, routerBypassMark, routerTPMark)
	}
	return rules + "}\n", nil
}

func routerTPLocalAddresses() (string, string, error) {
	addresses, err := net.InterfaceAddrs()
	if err != nil || len(addresses) > 1024 {
		return "", "", errors.New("core_network_check_failed")
	}
	v4, v6 := []string{}, []string{}
	seen := make(map[string]bool)
	for _, a := range addresses {
		ip, _, err := net.ParseCIDR(a.String())
		if err != nil {
			continue
		}
		if seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		if ip.To4() != nil {
			v4 = append(v4, ip.String())
		} else {
			v6 = append(v6, ip.String())
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	return strings.Join(v4, ", "), strings.Join(v6, ", "), nil
}

func routerTProxyPreflight(ctx context.Context, conf *daeConfig.Config) error {
	if conf == EmptyConfig {
		return nil
	}
	for _, name := range conf.Global.LanInterface {
		if _, err := net.InterfaceByName(name); err != nil {
			return errors.New("missing_lan_interface")
		}
	}
	bridges, err := routerTPBridgeTopology(conf.Global.LanInterface)
	if err != nil {
		return err
	}
	owner := routerCoreNetworkOwner{Mode: "tproxy", ProxyPort: 39006, DNSPort: 39003, LAN: conf.Global.LanInterface, Bridges: bridges}
	if err := routerTPCheckBridge(ctx, owner); err != nil {
		return err
	}
	rules, err := routerTPRules(owner, true)
	if err != nil {
		return err
	}
	rules = strings.ReplaceAll(rules, routerTPNft, fmt.Sprintf("nailink_probe_%d", os.Getpid()))
	return routerTPNftRun(ctx, rules, true)
}

// Keep a DHCP/WAN address change from accidentally proxying the router's own
// services. No polling, goroutine or socket is created for TUN/private probes.
func (s *routerSingBoxSession) watchTProxy() {
	owner, err := routerReadNetworkOwner(s.dirs[0])
	if err != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.tpCancel = cancel
	s.tpDone = make(chan struct{})
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		defer close(s.tpDone)
		previous4, previous6 := "", ""
		checks := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checks++
				v4, v6, err := routerTPLocalAddresses()
				bridges, bridgeErr := routerTPBridgeTopology(owner.LAN)
				if bridgeErr != nil || !routerTPBridgeSame(bridges, owner.Bridges) {
					err = errors.New("core_network_check_failed")
				}
				if checks%10 == 0 {
					attempt, stop := context.WithTimeout(ctx, 2*time.Second)
					families, e := routerNftTablesFor(attempt, routerTPNft)
					stop()
					if e != nil || len(families) != 1 || families[0] != "inet" {
						err = errors.New("core_network_check_failed")
					}
					if len(owner.Bridges) > 0 {
						attempt, stop = context.WithTimeout(ctx, 2*time.Second)
						families, e = routerNftTablesFor(attempt, routerTPBridgeNft)
						stop()
						if e != nil || len(families) != 1 || families[0] != "bridge" {
							err = errors.New("core_network_check_failed")
						}
					}
				}
				if err == nil && v4 == previous4 && v6 == previous6 {
					continue
				}
				if err == nil {
					command := fmt.Sprintf("flush set inet %s local4\nflush set inet %s local6\n", routerTPNft, routerTPNft)
					if v4 != "" {
						command += fmt.Sprintf("add element inet %s local4 { %s }\n", routerTPNft, v4)
					}
					if v6 != "" {
						command += fmt.Sprintf("add element inet %s local6 { %s }\n", routerTPNft, v6)
					}
					if len(owner.Bridges) > 0 {
						command += fmt.Sprintf("flush set bridge %s local4\nflush set bridge %s local6\n", routerTPBridgeNft, routerTPBridgeNft)
						if v4 != "" {
							command += fmt.Sprintf("add element bridge %s local4 { %s }\n", routerTPBridgeNft, v4)
						}
						if v6 != "" {
							command += fmt.Sprintf("add element bridge %s local6 { %s }\n", routerTPBridgeNft, v6)
						}
					}
					attempt, stop := context.WithTimeout(ctx, 2*time.Second)
					err = routerTPNftRun(attempt, command, false)
					stop()
				}
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					s.log.Warn("TPROXY local address refresh failed; restarting the managed core")
					_ = s.cmd.Process.Signal(syscall.SIGTERM)
					return
				}
				previous4, previous6 = v4, v6
			}
		}
	}()
}

func routerTPRoute(family int) netlink.Route {
	cidr := "0.0.0.0/0"
	if family == netlink.FAMILY_V6 {
		cidr = "::/0"
	}
	_, dst, _ := net.ParseCIDR(cidr)
	lo, _ := net.InterfaceByName("lo")
	index := 1
	if lo != nil {
		index = lo.Index
	}
	return netlink.Route{Family: family, Table: routerTPTable, Dst: dst, LinkIndex: index, Type: syscall.RTN_LOCAL, Scope: netlink.SCOPE_HOST, Protocol: syscall.RTPROT_STATIC}
}
func routerTPRule(family int) *netlink.Rule {
	r := netlink.NewRule()
	mask := uint32(0xffffffff)
	r.Family = family
	r.Priority = routerTPTable
	r.Table = routerTPTable
	r.Mark = routerTPMark
	r.Mask = &mask
	return r
}

func routerActivateTProxy(root string) error {
	owner, err := routerReadNetworkOwner(root)
	if err != nil {
		return err
	}
	if owner.Mode != "tproxy" {
		return errors.New("core_network_ownership_failed")
	}
	if owner.Active {
		return nil
	}
	rules, err := routerTPRules(owner, true)
	if err != nil {
		return err
	}
	bridgeRules, err := routerTPBridgeRules(owner)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = routerTPNftRun(ctx, "delete table inet "+routerTPNft+"\n"+rules+bridgeRules, true); err != nil {
		return err
	}
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		if family == netlink.FAMILY_V6 && !owner.IPv6 {
			continue
		}
		route := routerTPRoute(family)
		if netlink.RouteAdd(&route) != nil || netlink.RuleAdd(routerTPRule(family)) != nil {
			return errors.New("core_network_check_failed")
		}
	}
	if err = routerTPNftRun(ctx, "delete table inet "+routerTPNft+"\n"+rules+bridgeRules, false); err != nil {
		return err
	}
	owner.Active = true
	raw, _ := json.Marshal(owner)
	if err = routercore.Write(routerOwnerPath(root), raw); err != nil {
		return err
	}
	return routerDNSClearConntrack(owner, true)
}

// Remove only the exact table/rules/routes reserved by a private claim. A
// mismatched rule/route is a conflict, never permission to flush a shared table.
func routerReleaseTProxy(root string, owner routerCoreNetworkOwner) error {
	if err := routerTPValidateOwner(owner); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if len(owner.Bridges) > 0 {
		families, err := routerNftTablesFor(ctx, routerTPBridgeNft)
		if err != nil {
			return err
		}
		for _, family := range families {
			if family != "bridge" {
				return errors.New("core_network_ownership_failed")
			}
			if exec.CommandContext(ctx, "nft", "delete", "table", "bridge", routerTPBridgeNft).Run() != nil {
				return errors.New("core_cleanup_failed")
			}
		}
	}
	families, err := routerNftTablesFor(ctx, routerTPNft)
	if err != nil {
		return err
	}
	for _, family := range families {
		if family != "inet" {
			return errors.New("core_network_ownership_failed")
		}
		if exec.CommandContext(ctx, "nft", "delete", "table", "inet", routerTPNft).Run() != nil {
			return errors.New("core_cleanup_failed")
		}
	}
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		if family == netlink.FAMILY_V6 && !owner.IPv6 {
			continue
		}
		rules, err := netlink.RuleList(family)
		if err != nil {
			return errors.New("core_cleanup_failed")
		}
		for _, r := range rules {
			if r.Table == routerTPTable || r.Priority == routerTPTable {
				if r.Table != routerTPTable || r.Priority != routerTPTable || r.Mark != routerTPMark || r.Mask == nil || *r.Mask != 0xffffffff {
					return errors.New("core_network_ownership_failed")
				}
				if netlink.RuleDel(&r) != nil {
					return errors.New("core_cleanup_failed")
				}
			}
		}
		routes, err := netlink.RouteListFiltered(family, &netlink.Route{Table: routerTPTable}, netlink.RT_FILTER_TABLE)
		if err != nil {
			return errors.New("core_cleanup_failed")
		}
		expected := routerTPRoute(family)
		for _, r := range routes {
			if r.Type != expected.Type || r.LinkIndex != expected.LinkIndex || r.Gw != nil || r.Dst != nil && r.Dst.String() != expected.Dst.String() {
				return errors.New("core_network_ownership_failed")
			}
			if netlink.RouteDel(&r) != nil {
				return errors.New("core_cleanup_failed")
			}
		}
	}
	if err = routerDNSClearConntrack(owner, false); err != nil {
		return err
	}
	if err = routerClearCoreFW4(ctx); err != nil {
		return err
	}
	if err = os.Remove(routerOwnerPath(root)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.RemoveAll(filepath.Dir(owner.Config))
}
