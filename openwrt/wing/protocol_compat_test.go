// SPDX-License-Identifier: AGPL-3.0-only
package dialer

import (
	"context"
	proxyurl "github.com/daeuniverse/outbound/common/url"
	D "github.com/daeuniverse/outbound/dialer"
	"github.com/daeuniverse/outbound/netproxy"
	"github.com/daeuniverse/outbound/protocol/direct"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterDAERejectsMihomoOnlyVLESSBeforeDialing(t *testing.T) {
	for _, query := range []string{"type=xhttp", "net=xhttp", "network=xhttp", "encryption=mlkem768x25519plus.native.0rtt.fixture"} {
		_, _, err := routerProtocolOptions("vless://fixture@192.0.2.1:443?"+query, nil, time.Second)
		if err == nil || err.Error() != "node_requires_mihomo" {
			t.Fatal("dae would silently downgrade transport", err)
		}
	}
}

type routerLockedReadConn struct {
	net.Conn
	mu      sync.Mutex
	reading chan struct{}
	closes  atomic.Int32
}

func TestRouterVLESSCertificatePreferenceIsPerNode(t *testing.T) {
	global := D.ExtraOption{AllowInsecure: false, TlsImplementation: "tls"}
	for _, key := range []string{"insecure", "allowInsecure", "allow_insecure", "skip-cert-verify"} {
		local, err := routerProtocolExtraOption("vless://fixture@192.0.2.1:443?"+key+"=true", global)
		if err != nil || !local.AllowInsecure || global.AllowInsecure || local.TlsImplementation != global.TlsImplementation {
			t.Fatal("node setting lost or global state mutated", err)
		}
		verified, err := routerProtocolExtraOption("vless://fixture@192.0.2.1:443?"+key+"=false", local)
		if err != nil || verified.AllowInsecure || !local.AllowInsecure {
			t.Fatal("explicit validation preference was not isolated", err)
		}
	}
	for _, options := range []string{"insecure=broken", "insecure=true&allowInsecure=false", "insecure=1&insecure=0"} {
		if _, err := routerProtocolExtraOption("vless://private-fixture@192.0.2.1:443?"+options, global); err == nil {
			t.Fatal("invalid setting accepted")
		}
	}
}

func (c *routerLockedReadConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	close(c.reading)
	return c.Conn.Read(b)
}

func (c *routerLockedReadConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes.Add(1)
	return c.Conn.Close()
}

type routerFixedDialer struct{ conn netproxy.Conn }

func (d routerFixedDialer) DialContext(context.Context, string, string) (netproxy.Conn, error) {
	return d.conn, nil
}

func TestRouterSSCloseInterruptsEncryptedReadOnce(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	locked := &routerLockedReadConn{Conn: left, reading: make(chan struct{})}
	d := routerProtocolDialer("ss://fixture", routerFixedDialer{locked})
	if _, ok := routerProtocolDialer("vmess://fixture", routerFixedDialer{locked}).(*routerStreamDialer); !ok {
		t.Fatal("VMess is not protected")
	}
	conn, err := d.DialContext(context.Background(), "tcp", "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan struct{})
	go func() { _, _ = conn.Read(make([]byte, 1)); close(read) }()
	<-locked.reading
	done := make(chan struct{})
	go func() { _ = conn.Close(); _ = conn.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		_ = right.Close()
		t.Fatal("SS close blocked behind the encrypted read lock")
	}
	<-read
	if locked.closes.Load() != 1 {
		t.Fatal("stream closed more than once")
	}
	packet, err := d.DialContext(context.Background(), "udp", "127.0.0.1:1")
	if err != nil || packet != locked {
		t.Fatal("UDP packet interfaces changed")
	}
}

func TestRouterProtocolURIOptions(t *testing.T) {
	for _, c := range []struct {
		link, host, insecure string
		obfs, invalid        bool
	}{
		{"anytls://fixture@127.0.0.1:443?insecure=true&sni=fixture.test", "127.0.0.1:443", "1", false, false},
		{"anytls://fixture@127.0.0.1:443?allowInsecure=true", "127.0.0.1:443", "1", false, false},
		{"anytls://fixture@127.0.0.1:443?insecure=false&allowInsecure=true", "127.0.0.1:443", "0", false, false},
		{"anytls://fixture@127.0.0.1:443", "127.0.0.1:443", "", false, false},
		{"hy2://fixture@[::1]:443?mport=5000-5001,5010&obfs=salamander&obfsPassword=fixture", "[::1]:5000", "", true, false},
		{"hy2://fixture@127.0.0.1:443?ports=5001-5000", "", "", false, true},
		{"hy2://fixture@127.0.0.1:443?ports=0", "", "", false, true},
		{"hy2://fixture@127.0.0.1:443?obfs=unknown", "", "", false, true},
		{"hy2://fixture@127.0.0.1:443?obfs=salamander", "", "", false, true},
	} {
		link, next, err := routerProtocolOptions(c.link, direct.SymmetricDirect, 0)
		if c.invalid {
			if err == nil {
				t.Fatal("invalid options were silently accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		u, err := proxyurl.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		if u.Host != c.host || u.Query().Get("insecure") != c.insecure {
			t.Fatal("protocol URI parameters changed incorrectly")
		}
		if hop, ok := next.(*routerHopDialer); ok {
			next = hop.next
		}
		_, wrapped := next.(*routerSalamanderDialer)
		if wrapped != c.obfs {
			t.Fatal("obfuscation transport not applied")
		}
		if u.User.Username() != "fixture" {
			t.Fatal("authentication changed")
		}
	}
}
