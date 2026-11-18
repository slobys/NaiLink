// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/daeuniverse/dae/common/consts"
	"github.com/daeuniverse/dae/control"
	"github.com/mzz2017/softwind/netproxy"
)

var HttpTransport = &http.Transport{
	DialContext: func(ctx context.Context, _ string, address string) (net.Conn, error) {
		if conn, handled, err := RouterCoreDialContext(ctx, "tcp", address); handled {
			return conn, err
		}
		host, rawPort, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		port, err := strconv.ParseUint(rawPort, 10, 16)
		if err != nil {
			return nil, err
		}
		ctl, err := ControlPlane()
		if err != nil {
			return nil, err
		}
		start := time.Now()
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if probe, ok := ctx.Value(websiteObservationKey{}).(*websiteObservation); ok {
			probe.record("dns", time.Since(start))
		}
		if err != nil {
			return nil, err
		}
		return dialRouterAddresses(ctx, addresses, func(attempt context.Context, ip netip.Addr) (net.Conn, error) {
			conn, err := ctl.RouteDialTcpContext(attempt, &control.RouteDialParam{
				Outbound: consts.OutboundControlPlaneRouting, Domain: host,
				Src: netip.MustParseAddrPort("0.0.0.0:0"), Dest: netip.AddrPortFrom(ip, uint16(port)),
			})
			if err != nil {
				return nil, err
			}
			return &routerSubscriptionConn{Conn: &netproxy.FakeNetConn{Conn: conn}}, nil
		})
	},
	TLSHandshakeTimeout:   10 * time.Second,
	DisableKeepAlives:     true,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	ExpectContinueTimeout: time.Second,
	ForceAttemptHTTP2:     true,
}

// Closing an encrypted stream may wait for a read mutex. Interrupt I/O before
// Close so an HTTP download deadline can actually release its connection.
type routerSubscriptionConn struct{ net.Conn }

func (c *routerSubscriptionConn) Close() error {
	_ = c.Conn.SetDeadline(time.Now())
	return c.Conn.Close()
}

// A dead first DNS address must not consume the entire download deadline or
// prevent a working second family/address from being tried. Share the remaining
// budget; dial sequentially to avoid additional concurrent router load.
func dialRouterAddresses(ctx context.Context, addresses []netip.Addr, dial func(context.Context, netip.Addr) (net.Conn, error)) (net.Conn, error) {
	if len(addresses) == 0 {
		return nil, errors.New("no DNS record for subscription host")
	}
	var last error
	for index, address := range addresses {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		budget := 4 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline) / time.Duration(len(addresses)-index); remaining < budget {
				budget = remaining
			}
		}
		attempt, cancel := context.WithTimeout(ctx, budget)
		conn, err := dial(attempt, address)
		cancel()
		if err == nil {
			if parentErr := ctx.Err(); parentErr != nil {
				conn.Close()
				return nil, parentErr
			}
			return conn, nil
		}
		last = err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("subscription addresses unavailable: %w", last)
}
