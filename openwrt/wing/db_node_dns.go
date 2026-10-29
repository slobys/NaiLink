// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"github.com/daeuniverse/dae-wing/routercore"
	"gorm.io/gorm"
	"net"
)

func NodeDNSForNodes(d *gorm.DB, nodes []Node) (string, error) {
	ids, hosts := []uint{}, map[string]bool{}
	seen := map[uint]bool{}
	for _, n := range nodes {
		if n.SubscriptionID == nil {
			continue
		}
		if !seen[*n.SubscriptionID] {
			ids = append(ids, *n.SubscriptionID)
			seen[*n.SubscriptionID] = true
		}
		host := n.Address
		if h, _, e := net.SplitHostPort(host); e == nil {
			host = h
		}
		hosts[routercore.NodeDNSHost(host)] = true
	}
	if len(ids) == 0 {
		return "", nil
	}
	var values []string
	if e := d.Model(&Subscription{}).Where("id IN ?", ids).Pluck("node_dns", &values).Error; e != nil {
		return "", e
	}
	return routercore.PackNodeDNS(routercore.MergeNodeDNS(values, hosts)), nil
}
