// SPDX-License-Identifier: AGPL-3.0-only
package dialer

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daeuniverse/dae/common"
	"github.com/daeuniverse/dae/common/consts"
	_ "github.com/daeuniverse/outbound/dialer/v2ray"
	_ "github.com/daeuniverse/outbound/protocol/vless"
	_ "github.com/daeuniverse/outbound/transport/ws"
	"github.com/gorilla/websocket"
)

type routerHealthWSReader struct {
	c *websocket.Conn
	r io.Reader
}

func (r *routerHealthWSReader) Read(p []byte) (int, error) {
	for {
		if r.r == nil {
			_, next, err := r.c.NextReader()
			if err != nil {
				return 0, err
			}
			r.r = next
		}
		n, err := r.r.Read(p)
		if err == io.EOF {
			r.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}
func TestRouterHealthHTTPWebSocketReuse(t *testing.T) {
	for _, response := range []string{"HTTP/1.1 200 OK\r\nContent-Length: 8\r\n\r\nfixture\n", "HTTP/1.1 204 No Content\r\n\r\n"} {
		t.Run(response[9:12], func(t *testing.T) {
			server, upgrades, requests := routerHealthWSServer(t, response)
			option := newTestDialer(t).GlobalOption
			d, err := NewFromLink(option, InstanceOption{DisableCheck: true}, "vless://00000000-0000-0000-0000-000000000001@"+strings.TrimPrefix(server.URL, "http://")+"?type=ws&path=%2Ffixture", "")
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			opt, err := ParseTcpCheckOption(context.Background(), []string{"http://probe.invalid/", "127.0.0.1"}, http.MethodGet, "udp")
			if err != nil {
				t.Fatal(err)
			}
			for range 20 {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				ok, err := d.HttpCheck(ctx, IdxTcp4, opt.Url, opt.Ip4, opt.Method, 0, false)
				cancel()
				if !ok || err != nil {
					t.Fatal(ok, err)
				}
			}
			d.Close()
			t.Logf("HTTP probes=%d WebSocket upgrades=%d", requests.Load(), upgrades.Load())
			if requests.Load() != 20 || upgrades.Load() != 1 {
				t.Fatal("small health-check body forced new VLESS WebSocket tunnels")
			}
		})
	}
}

func routerHealthWSServer(t *testing.T, response string) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var handlers sync.WaitGroup
	var upgrades, requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		upgrader := websocket.Upgrader{}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		upgrades.Add(1)
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(&routerHealthWSReader{c: c})
		// This fixture accepts only the fixed fake UUID, no addons, TCP to 127.0.0.1:80.
		header := make([]byte, 26)
		if _, err = io.ReadFull(reader, header); err != nil {
			return
		}
		expected := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 0, 80, 1, 127, 0, 0, 1}
		if string(header) != string(expected) {
			t.Errorf("unexpected fixture VLESS header: %x", header)
			return
		}
		if err = c.WriteMessage(websocket.BinaryMessage, []byte{0, 0}); err != nil {
			return
		}
		for {
			req, err := http.ReadRequest(reader)
			if err != nil {
				return
			}
			req.Body.Close()
			requests.Add(1)
			if err = c.WriteMessage(websocket.BinaryMessage, []byte(response)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(func() { server.Close(); handlers.Wait() })
	return server, &upgrades, &requests
}

func TestRouterHealthHTTPWorkerProbeBurst(t *testing.T) {
	for _, host := range []string{"relay.example.test", "relay.example.workers.dev"} {
		t.Run(host, func(t *testing.T) {
			server, upgrades, requests := routerHealthWSServer(t, "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
			option := newTestDialer(t).GlobalOption
			option.CheckInterval = 50 * time.Millisecond
			option.TcpCheckOptionRaw = TcpCheckOptionRaw{
				Raw: []string{"http://probe.invalid/", "127.0.0.1"}, Method: http.MethodHead, ResolverNetwork: "udp", Log: option.Log,
			}
			d, err := NewFromLink(option, InstanceOption{}, "vless://00000000-0000-0000-0000-000000000001@"+strings.TrimPrefix(server.URL, "http://")+"?type=ws&host="+host+"&path=%2Ffixture", "")
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			nt := newTestNetworkType()
			alive := NewAliveDialerSet(option.Log, "fixture", nt, 0, consts.DialerSelectionPolicy_Random, []*Dialer{d}, []*Annotation{{}}, func(bool) {}, true)
			d.RegisterAliveDialerSet(alive)
			defer d.UnregisterAliveDialerSet(alive)
			d.ActivateCheck()
			d.NotifyCheck()
			deadline := time.Now().Add(3 * time.Second)
			for !d.SnapshotLastProbe(nt).Alive && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if !d.SnapshotLastProbe(nt).Alive || requests.Load() == 0 {
				t.Fatal("initial automatic test failed")
			}
			for range 25 {
				d.NotifyCheck()
				time.Sleep(20 * time.Millisecond)
			}
			t.Logf("automatic HTTP probes=%d WebSocket upgrades=%d", requests.Load(), upgrades.Load())
			if host == "relay.example.test" {
				if upgrades.Load() < 5 {
					t.Fatal("regular node no longer observes its configured interval")
				}
				return
			}
			if requests.Load() != 1 || upgrades.Load() != 1 {
				t.Fatal("Worker received duplicate automatic probes")
			}
			d.NotifyCheckTcp()
			d.NotifyCheckDnsUdp()
			// The unthrottled emergency probe may wait up to two seconds
			// before dialing. Give it time to expose a missed cooldown.
			time.Sleep(2100 * time.Millisecond)
			if requests.Load() != 1 || upgrades.Load() != 1 {
				t.Fatal("emergency probe bypassed the Worker cooldown")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			opt, err := d.TcpCheckOptionRaw.Option()
			if err != nil {
				t.Fatal(err)
			}
			transport := d.GetHttpClient(IdxTcp4, opt.Ip4, 0, false).Transport.(*http.Transport)
			if transport.IdleConnTimeout <= common.NaiLinkWorkerProbeInterval {
				t.Fatal("reusable Worker tunnel would expire before the next probe")
			}
			ok, err := d.HttpCheck(ctx, IdxTcp4, opt.Url, opt.Ip4, opt.Method, 0, false)
			if !ok || err != nil || upgrades.Load() != 2 {
				t.Fatal("manual test did not reach the Worker", ok, err)
			}
		})
	}
}
