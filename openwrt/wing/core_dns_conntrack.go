// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"errors"
	"net"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

type routerDNSFlowFilter struct {
	port     uint16
	entering bool
	locals   []net.IP
	lanIPs   []net.IP
	lan      []*net.IPNet
}

func routerDNSHasIP(ips []net.IP, ip net.IP) bool {
	for _, local := range ips {
		if local.Equal(ip) {
			return true
		}
	}
	return false
}

func (f *routerDNSFlowFilter) MatchConntrackFlow(flow *netlink.ConntrackFlow) bool {
	if flow == nil || flow.Zone != 0 || flow.Forward.DstPort != 53 || (flow.Forward.Protocol != syscall.IPPROTO_UDP && flow.Forward.Protocol != syscall.IPPROTO_TCP) || flow.Forward.DstIP.Equal(net.ParseIP("100.100.100.100")) {
		return false
	}
	// Retire only an original DNS query DNATed to this claimed local listener.
	if flow.Reverse.SrcPort == f.port && routerDNSHasIP(f.locals, flow.Reverse.SrcIP) {
		return !f.entering // Keep requests already using the new live listener.
	}
	if !f.entering || routerDNSHasIP(f.locals, flow.Forward.SrcIP) {
		return false
	}
	// Re-evaluate old NAT/no-NAT decisions for the selected LAN's DNS only.
	if flow.Forward.SrcIP.IsLinkLocalUnicast() && !routerDNSHasIP(f.lanIPs, flow.Forward.DstIP) {
		return false
	}
	for _, prefix := range f.lan {
		if prefix.Contains(flow.Forward.SrcIP) {
			return true
		}
	}
	return false
}

func routerDNSClearConntrack(owner routerCoreNetworkOwner, entering bool) error {
	if owner.DNSPort == 0 { // Legacy claims have no independent DNS listener.
		return nil
	}
	if owner.DNSPort < 1024 || owner.DNSPort > 65535 {
		return errors.New("core_network_ownership_failed")
	}
	filter := &routerDNSFlowFilter{port: uint16(owner.DNSPort), entering: entering}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return errors.New("core_dns_session_failed")
	}
	for _, address := range addresses {
		ip, _, parseErr := net.ParseCIDR(address.String())
		if parseErr == nil {
			filter.locals = append(filter.locals, ip)
		}
	}
	if entering {
		for _, name := range owner.LAN {
			link, linkErr := net.InterfaceByName(name)
			if linkErr != nil {
				return errors.New("core_dns_session_failed")
			}
			addresses, addrErr := link.Addrs()
			if addrErr != nil {
				return errors.New("core_dns_session_failed")
			}
			for _, address := range addresses {
				ip, prefix, parseErr := net.ParseCIDR(address.String())
				if parseErr == nil {
					filter.lan = append(filter.lan, prefix)
					filter.lanIPs = append(filter.lanIPs, ip)
				}
			}
		}
	}
	handle, err := netlink.NewHandle(syscall.NETLINK_NETFILTER)
	if err != nil {
		return errors.New("core_dns_session_failed")
	}
	defer handle.Close()
	if err = handle.SetSocketTimeout(2 * time.Second); err != nil {
		return errors.New("core_dns_session_failed")
	}
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		if family == netlink.FAMILY_V6 && !owner.IPv6 {
			continue
		}
		for attempt := 0; attempt < 3; attempt++ {
			_, err = handle.ConntrackDeleteFilter(netlink.ConntrackTable, netlink.InetFamily(family), filter)
			if !errors.Is(err, netlink.ErrDumpInterrupted) {
				break
			}
		}
		if err != nil {
			logrus.WithError(err).Warn("Managed DNS conntrack retirement failed")
			return errors.New("core_dns_session_failed")
		}
	}
	return nil
}
