// SPDX-License-Identifier: AGPL-3.0-only
package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/daeuniverse/dae/common/consts"
	"github.com/daeuniverse/dae/component/dns"
	"github.com/daeuniverse/dae/component/outbound/dialer"
	"github.com/daeuniverse/outbound/netproxy"
	"github.com/sirupsen/logrus"
)

// A hybrid upstream has an explicitly configured TCP recovery path. Reserve
// time for it in this request instead of spending all eight seconds on UDP.
// UDP-only upstreams retain their existing timeout and transport semantics.
func routerDNSPrimaryTimeout(ctx context.Context, upstream *dns.Upstream, arg *dialArgument) time.Duration {
	if upstream == nil || upstream.Scheme != dns.UpstreamScheme_TCP_UDP || arg == nil || arg.l4proto != consts.L4ProtoStr_UDP {
		return consts.DefaultDialTimeout
	}
	budget := 1500 * time.Millisecond
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline) / 2; remaining < budget {
			budget = remaining
		}
	}
	return budget
}

func routerHasTCPBackup(res *proxyDialResult, p *proxyDialParam) bool {
	if p.Network != "tcp" || p.Excluded != nil || res.Outbound == nil || res.Outbound.GetSelectionPolicy() == consts.DialerSelectionPolicy_Fixed || len(res.Outbound.Dialers) < 2 || !isProxyBackedDialer(res.Dialer) {
		return false
	}
	other, _, _, err := res.Outbound.SelectWithExclusionResult(res.SelectionNetworkTypeObj, res.IsDialIp, res.Dialer)
	return err == nil && other != nil && other != res.Dialer && isProxyBackedDialer(other)
}

func routerTCPPrimaryBudget(remaining, interval time.Duration, sample dialer.DialerProbeObservationSnapshot) time.Duration {
	budget := remaining / 2
	age := time.Since(sample.CheckedAt)
	if !sample.Alive || !sample.HasLatency || sample.Latency <= 0 || interval <= 0 || age < 0 || age/2 > interval || sample.Latency >= budget/4 {
		return budget
	}
	// A recent, fast health check gives a ceiling for a stalled connect.
	// Keep headroom for jitter and preserve the full split for slow links.
	return min(budget, max(1500*time.Millisecond, 4*sample.Latency))
}

// Retry only connection establishment failures, before the application relay
// starts. Authentication/configuration errors are not hidden by generic retry.
func routerRetryableConnectError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.EPIPE)
}

func (c *ControlPlane) routerRouteDial(ctx context.Context, p *proxyDialParam) (netproxy.Conn, *proxyDialResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	caller := ctx
	// A backup shares one overall dial deadline, rather than extending it to 16s.
	ctx, stop := context.WithTimeout(ctx, consts.DefaultDialTimeout)
	defer stop()
	params := *p
	var lastRes *proxyDialResult
	var lastErr error
	retryingProxy := false
	for attempt := range 2 {
		if err := ctx.Err(); err != nil {
			return nil, lastRes, err
		}
		selectStarted := time.Now()
		res, err := c.chooseProxyDialer(ctx, &params)
		if c.log != nil && c.log.IsLevelEnabled(logrus.DebugLevel) {
			c.routerLogAttempt("route_select", attempt+1, selectStarted, err, logrus.Fields{"network": params.Network})
		}
		if err != nil {
			if retryingProxy {
				return nil, lastRes, lastErr
			}
			return nil, res, err
		}
		// A concurrent policy change must not make a retry cross groups or retry
		// the failed node. Fixed selections always keep the user's chosen node.
		if retryingProxy && (res.Outbound != lastRes.Outbound || res.Dialer == lastRes.Dialer || res.Outbound.GetSelectionPolicy() == consts.DialerSelectionPolicy_Fixed || !isProxyBackedDialer(res.Dialer)) {
			return nil, lastRes, lastErr
		}
		hasBackup := attempt == 0 && routerHasTCPBackup(res, &params)
		budget := consts.DefaultDialTimeout
		if hasBackup {
			deadline, _ := ctx.Deadline()
			sample := res.Dialer.SnapshotLastProbe(res.SelectionNetworkTypeObj)
			budget = routerTCPPrimaryBudget(time.Until(deadline), res.Dialer.CheckInterval, sample)
		}
		dialCtx, cancel := context.WithTimeout(ctx, budget)
		dialStarted := time.Now()
		conn, err := res.Dialer.DialContext(dialCtx, res.Network, res.DialTarget)
		attemptErr := dialCtx.Err()
		cancel()
		if err == nil && attemptErr != nil {
			err = attemptErr
		}
		if c.log != nil && c.log.IsLevelEnabled(logrus.DebugLevel) {
			fields := logrus.Fields{"network": params.Network}
			if res.Outbound != nil {
				fields["outbound"] = res.Outbound.Name
			}
			if res.Dialer.Property() != nil {
				fields["dialer"] = res.Dialer.Property().Name
			}
			c.routerLogAttempt("proxy_connect", attempt+1, dialStarted, err, fields)
		}
		// Dialers can race cancellation and still return a socket. Never leak it,
		// or return a connection which belongs to an already cancelled operation.
		if parentErr := ctx.Err(); parentErr != nil {
			if conn != nil {
				_ = conn.SetDeadline(time.Now())
				_ = conn.Close()
			}
			if caller.Err() == nil {
				notifyProxyDialerHealthCheck(res.Dialer, consts.L4ProtoStr(params.Network), parentErr)
			}
			return nil, res, parentErr
		}
		if err == nil {
			return conn, res, nil
		}
		if conn != nil {
			_ = conn.SetDeadline(time.Now())
			_ = conn.Close()
		}
		lastRes, lastErr = res, err
		l4proto := consts.L4ProtoStr(params.Network)
		if res.SelectionNetworkTypeObj != nil {
			l4proto = res.SelectionNetworkTypeObj.L4Proto
		}
		notifyProxyDialerHealthCheck(res.Dialer, l4proto, err)
		if attempt == 0 && shouldForceMarkUnavailableOnProxyDialError(err) {
			// Preserve existing recovery from an unavailable local address family.
			if res.SelectionNetworkTypeObj != nil {
				res.Dialer.ReportUnavailableForced(res.SelectionNetworkTypeObj, fmt.Errorf("proxy dial failed: %w", err))
			}
			continue
		}
		if !hasBackup || !routerRetryableConnectError(err) {
			return nil, res, err
		}
		params.Excluded = res.Dialer
		retryingProxy = true
		if c.log != nil {
			c.log.WithFields(logrus.Fields{"outbound": res.Outbound.Name, "network": params.Network}).Debug("TCP connect failed; trying another node in the same group")
		}
	}
	return nil, lastRes, lastErr
}
