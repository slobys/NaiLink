// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
)

const routerTPBridgeNft = "nailink_broute"

type routerTPBridge struct {
	Name  string   `json:"name"`
	MAC   string   `json:"mac"`
	Ports []string `json:"ports"`
}

func routerTPFlag(path string) bool {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b)) == "1"
}

// br_netfilter discards the TPROXY socket association before IP delivery.
// Route only public traffic addressed to this LAN bridge's own MAC before
// that hook. Docker/guest bridging and global bridge sysctls remain untouched.
func routerTPBridgeTopology(lan []string) ([]routerTPBridge, error) {
	links, err := netlink.LinkList()
	if err != nil || len(links) > 4096 {
		return nil, errors.New("core_network_check_failed")
	}
	byName, byIndex := map[string]netlink.Link{}, map[int]netlink.Link{}
	for _, link := range links {
		byName[link.Attrs().Name] = link
		byIndex[link.Attrs().Index] = link
	}
	var result []routerTPBridge
	for _, name := range lan {
		link := byName[name]
		if link == nil {
			return nil, errors.New("missing_lan_interface")
		}
		bridge := link
		if vlan, ok := link.(*netlink.Vlan); ok {
			bridge = byIndex[vlan.ParentIndex]
		}
		if bridge == nil || bridge.Type() != "bridge" {
			continue
		}
		root := filepath.Join("/sys/class/net", bridge.Attrs().Name, "bridge")
		needed := routerTPFlag("/proc/sys/net/bridge/bridge-nf-call-iptables") || routerTPFlag("/proc/sys/net/bridge/bridge-nf-call-ip6tables") || routerTPFlag(filepath.Join(root, "nf_call_iptables")) || routerTPFlag(filepath.Join(root, "nf_call_ip6tables"))
		if !needed {
			continue
		}
		info := bridge.(*netlink.Bridge)
		if bridge.Attrs().Name != name || info.VlanFiltering != nil && *info.VlanFiltering {
			return nil, errors.New("tproxy_bridge_vlan_unsupported")
		}
		item := routerTPBridge{Name: name, MAC: bridge.Attrs().HardwareAddr.String()}
		for _, port := range links {
			if port.Attrs().MasterIndex != bridge.Attrs().Index {
				continue
			}
			// Do not weaken reverse-path protection to make a mode work.
			all, _ := os.ReadFile("/proc/sys/net/ipv4/conf/all/rp_filter")
			value, _ := os.ReadFile(filepath.Join("/proc/sys/net/ipv4/conf", port.Attrs().Name, "rp_filter"))
			a, _ := strconv.Atoi(strings.TrimSpace(string(all)))
			p, _ := strconv.Atoi(strings.TrimSpace(string(value)))
			if a > p {
				p = a
			}
			if p == 1 || p != 0 && routerTPFlag("/proc/sys/net/ipv4/conf/all/src_valid_mark") {
				return nil, errors.New("tproxy_bridge_rpf")
			}
			item.Ports = append(item.Ports, port.Attrs().Name)
		}
		if len(item.Ports) == 0 || len(item.Ports) > 256 || len(bridge.Attrs().HardwareAddr) != 6 {
			return nil, errors.New("tproxy_bridge_unsupported")
		}
		sort.Strings(item.Ports)
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func routerTPIngress(owner routerCoreNetworkOwner) []string {
	seen := map[string]bool{}
	for _, name := range owner.LAN {
		seen[name] = true
	}
	for _, bridge := range owner.Bridges {
		for _, port := range bridge.Ports {
			seen[port] = true
		}
	}
	var names []string
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func routerTPBridgeRules(owner routerCoreNetworkOwner) (string, error) {
	if len(owner.Bridges) == 0 {
		return "", nil
	}
	if err := routerTPValidateOwner(owner); err != nil {
		return "", err
	}
	v4, v6, err := routerTPLocalAddresses()
	if err != nil {
		return "", err
	}
	rules := fmt.Sprintf(`table bridge %s {
 set local4 { type ipv4_addr; elements = { %s } }
 set local6 { type ipv6_addr; elements = { %s } }
 chain routed_lan {
  type filter hook prerouting priority -350; policy accept;
  meta mark 0x%x return
  ether type ip ip daddr @local4 return
  ether type ip6 ip6 daddr @local6 return
  ether type ip ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 198.18.0.0/15, 224.0.0.0/3 } return
  ether type ip6 ip6 daddr { ::/128, ::1/128, fc00::/7, fe80::/10, ff00::/8 } return
  meta l4proto { tcp, udp } th dport 53 return
`, routerTPBridgeNft, v4, v6, routerBypassMark)
	for _, b := range owner.Bridges {
		rules += fmt.Sprintf("  meta ibrname %q ether daddr %s ether type { ip, ip6 } meta l4proto { tcp, udp } meta broute set 1 accept\n", b.Name, b.MAC)
	}
	return rules + " }\n}\n", nil
}

func routerTPCheckBridge(ctx context.Context, owner routerCoreNetworkOwner) error {
	rules, err := routerTPBridgeRules(owner)
	if err != nil || rules == "" {
		return err
	}
	rules = strings.ReplaceAll(rules, routerTPBridgeNft, fmt.Sprintf("nailink_bridge_probe_%d", os.Getpid()))
	if routerTPNftRun(ctx, rules, true) != nil {
		return errors.New("tproxy_bridge_unsupported")
	}
	return nil
}

func routerTPBridgeSame(a, b []routerTPBridge) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func routerTPValidateBridges(owner routerCoreNetworkOwner) error {
	if len(owner.Bridges) > len(owner.LAN) {
		return errors.New("core_network_ownership_failed")
	}
	for _, bridge := range owner.Bridges {
		found := false
		for _, lan := range owner.LAN {
			if lan == bridge.Name {
				found = true
			}
		}
		mac, err := net.ParseMAC(bridge.MAC)
		if !found || err != nil || len(mac) != 6 || mac.String() != bridge.MAC || len(bridge.Ports) == 0 || len(bridge.Ports) > 256 {
			return errors.New("core_network_ownership_failed")
		}
	}
	return nil
}
