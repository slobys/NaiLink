// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"net"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestRouterDNSConntrackOwnership(t *testing.T) {
	_, lan, _ := net.ParseCIDR("192.0.2.0/24")
	f := &routerDNSFlowFilter{port: 42000, locals: []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("127.0.0.1")}, lanIPs: []net.IP{net.ParseIP("192.0.2.1")}, lan: []*net.IPNet{lan}}
	base := netlink.ConntrackFlow{Forward: netlink.IPTuple{Protocol: syscall.IPPROTO_UDP, SrcIP: net.ParseIP("192.0.2.3"), DstIP: net.ParseIP("192.0.2.1"), SrcPort: 45000, DstPort: 53}, Reverse: netlink.IPTuple{SrcIP: net.ParseIP("192.0.2.1"), SrcPort: 42000}}
	if !f.MatchConntrackFlow(&base) {
		t.Fatal("owned DNS DNAT was not retired")
	}
	for _, mutate := range []func(*netlink.ConntrackFlow){
		func(c *netlink.ConntrackFlow) { c.Forward.DstPort = 443 },
		func(c *netlink.ConntrackFlow) { c.Reverse.SrcPort = 42001 },
		func(c *netlink.ConntrackFlow) { c.Reverse.SrcIP = net.ParseIP("198.51.100.1") },
		func(c *netlink.ConntrackFlow) { c.Forward.Protocol = syscall.IPPROTO_ICMP },
		func(c *netlink.ConntrackFlow) { c.Forward.DstIP = net.ParseIP("100.100.100.100") },
		func(c *netlink.ConntrackFlow) { c.Zone = 8 },
	} {
		other := base
		mutate(&other)
		if f.MatchConntrackFlow(&other) {
			t.Fatal("unrelated flow matched DNS retirement", other)
		}
	}
	base.Forward.Protocol = syscall.IPPROTO_TCP
	if !f.MatchConntrackFlow(&base) {
		t.Fatal("owned TCP DNS flow was not retired")
	}
	f.entering = true
	if f.MatchConntrackFlow(&base) {
		t.Fatal("new listener's valid DNS mapping was retired during activation")
	}
	base.Reverse.SrcPort = 53
	if !f.MatchConntrackFlow(&base) {
		t.Fatal("LAN's previous no-NAT DNS decision was retained")
	}
	base.Forward.SrcIP = net.ParseIP("10.10.0.3")
	if f.MatchConntrackFlow(&base) {
		t.Fatal("another network's DNS was matched")
	}
	base.Forward.SrcIP = net.ParseIP("192.0.2.1")
	if f.MatchConntrackFlow(&base) {
		t.Fatal("untranslated router DNS was matched")
	}
	if f.MatchConntrackFlow(nil) {
		t.Fatal("nil flow matched")
	}
	_, linkLocal, _ := net.ParseCIDR("fe80::/64")
	f.lan = append(f.lan, linkLocal)
	f.lanIPs = append(f.lanIPs, net.ParseIP("fe80::1"))
	base.Forward.SrcIP = net.ParseIP("fe80::3")
	base.Forward.DstIP = net.ParseIP("fe80::2")
	if f.MatchConntrackFlow(&base) {
		t.Fatal("another interface's link-local DNS was matched")
	}
	base.Forward.DstIP = net.ParseIP("fe80::1")
	if !f.MatchConntrackFlow(&base) {
		t.Fatal("selected LAN's link-local DNS was not retired")
	}
}
