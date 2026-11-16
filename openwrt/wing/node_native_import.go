// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/routercore"
)

// Import supported mihomo-only transports without asking dae to instantiate
// them. The original link remains authoritative for future refreshes.
func routerMihomoNode(link string, tag *string, subscriptionID *uint) (*Node, error) {
	if tag != nil {
		if err := common.ValidateTag(*tag); err != nil {
			return nil, err
		}
	}
	node, err := routercore.Node(link, "import", routercore.NodeOptions{Import: true})
	if err != nil {
		return nil, errors.New(routercore.DescribeError(err))
	}
	if _, err = routercore.MihomoNode(node); err != nil {
		return nil, errors.New(routercore.DescribeError(err))
	}
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return nil, errors.New(routercore.ProblemText("invalid_node"))
	}
	address := net.JoinHostPort(u.Hostname(), u.Port())
	name := u.Fragment
	if name == "" {
		name = u.Query().Get("remarks")
	}
	if name == "" {
		name = u.Query().Get("name")
	}
	if name == "" {
		name = address
	}
	if len(name) > 1024 || strings.ContainsAny(name, "\x00\r\n") {
		return nil, errors.New(routercore.ProblemText("invalid_node_name"))
	}
	return &Node{Link: strings.TrimSpace(link), Name: name, Address: address, Protocol: "vless", Tag: tag, SubscriptionID: subscriptionID}, nil
}

func routerChainNode(link string, tag *string, subscriptionID *uint) (*Node, error) {
	entryLink, landingLink, err := routercore.ChainLinks(link)
	if err != nil {
		return nil, errors.New(routercore.DescribeError(err))
	}
	if tag != nil {
		if err := common.ValidateTag(*tag); err != nil {
			return nil, err
		}
	}
	entry, err := NewNodeModel(entryLink, nil, nil)
	if err != nil {
		return nil, errors.New("入口节点：" + routercore.ProblemText("invalid_node"))
	}
	landing, err := NewNodeModel(landingLink, nil, nil)
	if err != nil {
		return nil, errors.New("落地节点：" + routercore.ProblemText("invalid_node"))
	}
	entryName, landingName := entry.Name, landing.Name
	if entryName == "" {
		entryName = entry.Address
	}
	if landingName == "" {
		landingName = landing.Address
	}
	return &Node{Link: link, Name: entryName + " → " + landingName, Address: landing.Address, Protocol: "chain", Tag: tag, SubscriptionID: subscriptionID}, nil
}

func routerNativeNode(link string, tag *string, subscriptionID *uint) (model *Node, err error) {
	defer func() {
		if err != nil {
			err = errors.New(routercore.DescribeError(err))
		}
	}()
	if tag != nil {
		if err := common.ValidateTag(*tag); err != nil {
			return nil, err
		}
	}
	if routercore.IsNativeURI(link) && !strings.HasPrefix(strings.TrimSpace(link), "singbox://") {
		encoded, err := routercore.EncodeNativeURI(link)
		if err != nil {
			return nil, err
		}
		link = encoded
	}
	if strings.HasPrefix(strings.TrimSpace(link), "{") {
		encoded, err := routercore.NativeLink(strings.TrimSpace(link))
		if err != nil {
			return nil, err
		}
		link = encoded
	}
	name, address, protocol, err := routercore.NativeNodeMetadata(link)
	if err != nil {
		return nil, err
	}
	return &Node{Link: link, Name: name, Address: address, Protocol: protocol, Tag: tag, SubscriptionID: subscriptionID}, nil
}
