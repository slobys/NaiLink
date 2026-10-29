// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeCommon "github.com/daeuniverse/dae/common"
	"github.com/daeuniverse/dae/component/daedns"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/pkg/config_parser"
	"github.com/mohae/deepcopy"
	"github.com/sirupsen/logrus"
)

// One session owns its resolver sockets and transports, including during rollback.
// The original configuration is immutable and keeps the policy for restart snapshots.
func routerWithNodeDNS(conf *daeConfig.Config, log *logrus.Logger, native ...bool) (*daeConfig.Config, *mhDNSBridge, error) {
	if conf == nil || conf == EmptyConfig || conf.Global.NailinkNodeDNS == "" {
		return conf, nil, nil
	}
	policies := routercore.DecodeNodeDNS(conf.Global.NailinkNodeDNS)
	if len(policies) == 0 {
		return nil, nil, errors.New("节点 DNS 信息无效，已保留原运行配置")
	}
	copy := deepcopy.Copy(conf).(*daeConfig.Config)
	resolvers, e := daeConfig.BootstrapResolvers(&conf.Global)
	if e != nil || len(resolvers) == 0 {
		return nil, nil, errors.New("节点引导 DNS 配置无效")
	}
	servers := []sbObject{{"tag": "fallback", "type": "udp", "server": resolvers[0].Addr().String(), "server_port": int(resolvers[0].Port())}}
	// Native cores previously bootstrapped through the first configured DNS.
	// Keep that path for all hosts outside the imported exact-host policy.
	if len(native) > 0 && native[0] && len(conf.Dns.Upstream) > 0 {
		_, raw := daeCommon.GetTagFromLinkLikePlaintext(string(conf.Dns.Upstream[0]))
		if u, err := url.Parse(raw); err == nil && net.ParseIP(u.Hostname()) != nil {
			kind, port := u.Scheme, 53
			if kind == "tcp+udp" {
				kind = "tcp"
			}
			if kind == "tls" {
				port = 853
			}
			if kind == "https" {
				port = 443
			}
			if u.Port() != "" {
				port, _ = strconv.Atoi(u.Port())
			}
			servers[0] = sbObject{"tag": "fallback", "type": kind, "server": u.Hostname(), "server_port": port, "path": u.EscapedPath()}
		}
	}
	canonical := sbObject{"dns": sbObject{"servers": servers, "final": "fallback"}, "route": sbObject{"auto_detect_interface": true, "default_domain_resolver": sbObject{"server": "fallback"}}}
	if len(conf.Global.WanInterface) == 1 && conf.Global.WanInterface[0] != "auto" {
		canonical["route"].(sbObject)["default_interface"] = conf.Global.WanInterface[0]
	}
	profile := sbObject{"_nailink_dns": canonical, "dns": sbObject{}, "listeners": []sbObject{}}
	b, e := startMihomoDNSWithPolicies(profile, sbPorts{}, policies, log)
	if e != nil {
		return nil, nil, e
	}
	copy.Global.BootstrapResolver = strings.TrimPrefix(profile["dns"].(sbObject)["nameserver"].([]string)[0], "udp://")
	copy.Dns.Upstream = append(copy.Dns.Upstream, daeConfig.KeyableString("nailink_node_dns:udp://"+copy.Global.BootstrapResolver))
	params := []*config_parser.Param{}
	for _, host := range routercore.SortedNodeDNSHosts(policies) {
		params = append(params, &config_parser.Param{Val: host})
	}
	rule := &config_parser.RoutingRule{AndFunctions: []*config_parser.Function{{Name: "qname", Params: params}}, Outbound: config_parser.Function{Name: "nailink_node_dns"}}
	copy.Dns.Routing.Request.Rules = append([]*config_parser.RoutingRule{rule}, copy.Dns.Routing.Request.Rules...)
	if copy.Dns.Routing.Request.Fallback == nil || copy.Dns.Routing.Request.Fallback == "" {
		copy.Dns.Routing.Request.Fallback = "nailink_node_dns"
	}
	return copy, b, nil
}

func RouterProbeNodeDNS(global daeConfig.Global) (*daedns.Router, func(), error) {
	if global.NailinkNodeDNS == "" {
		return nil, func() {}, nil
	}
	log := logrus.New()
	log.SetOutput(io.Discard)
	conf, bridge, err := routerWithNodeDNS(&daeConfig.Config{Global: global}, log)
	if err != nil {
		return nil, func() {}, err
	}
	router, err := daedns.New(log, &conf.Global, &conf.Dns)
	if err != nil {
		bridge.Close()
		return nil, func() {}, err
	}
	return router, func() { bridge.Close() }, nil
}

func nodeDNSUpstream(raw string) sbObject {
	u, _ := url.Parse(raw)
	port := 443
	if u.Port() != "" {
		port, _ = strconv.Atoi(u.Port())
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/dns-query"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return sbObject{"type": "https", "server": u.Hostname(), "server_port": port, "path": path, "detour": "direct"}
}

// The standard resolver/dialer supplies bounded dual-stack racing. Every final
// address is checked before connect, including when a DNS answer has changed.
func (b *mhDNSBridge) nodeDNSDialer(base *net.Dialer) *net.Dialer {
	copy := *base
	copy.Resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, b.nodeBootstrapAddress)
	}}
	control := base.Control
	copy.Control = func(network, address string, raw syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil || !routercore.PublicResolverIP(net.ParseIP(host)) {
			return errors.New("invalid_dns_upstream")
		}
		if control != nil {
			return control(network, address, raw)
		}
		return nil
	}
	return &copy
}
