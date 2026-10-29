// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"bytes"
	"context"
	"fmt"
	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/pkg/config_parser"
	dnsmessage "github.com/miekg/dns"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type nodeDNSRoundTripper func(*http.Request) (*http.Response, error)

func TestNodeDNSMetadataSurvivesRecoverySnapshot(t *testing.T) {
	conf, _ := coreFixtureConfig(t)
	conf.Global.NailinkNodeDNS = routercore.PackNodeDNS(`{"node.fixture.test":["https://dns.google/dns-query?value=a'b"]}`)
	encoded, err := conf.Marshal(2)
	if err != nil {
		t.Fatal(err)
	}
	sections, err := config_parser.Parse(string(encoded))
	if err != nil {
		t.Fatal("snapshot parse failed", err)
	}
	restored, err := daeConfig.New(sections)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Global.NailinkNodeDNS != conf.Global.NailinkNodeDNS {
		t.Fatal("recovery lost node DNS metadata")
	}
}

func (f nodeDNSRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNodeDNSBridgeForwardsOnlyExactServerHostPolicy(t *testing.T) {
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fallback := &dnsmessage.Server{PacketConn: packet, Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
		a := new(dnsmessage.Msg)
		a.SetReply(q)
		if q.Question[0].Qtype == dnsmessage.TypeA {
			a.Answer = append(a.Answer, &dnsmessage.A{Hdr: dnsmessage.RR_Header{Name: q.Question[0].Name, Rrtype: dnsmessage.TypeA, Class: dnsmessage.ClassINET, Ttl: 60}, A: net.ParseIP("192.0.2.10")})
		}
		_ = w.WriteMsg(a)
	})}
	go fallback.ActivateAndServe()
	defer fallback.Shutdown()
	conf := &daeConfig.Config{Global: daeConfig.Global{BootstrapResolver: packet.LocalAddr().String(), NailinkNodeDNS: `{"node.fixture.test":["https://dns.google/dns-query"]}`}}
	effective, bridge, err := routerWithNodeDNS(conf, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	var doh atomic.Int32
	bridge.httpMu.Lock()
	bridge.httpClients = map[int]*http.Client{0: {Transport: nodeDNSRoundTripper(func(r *http.Request) (*http.Response, error) {
		doh.Add(1)
		raw, _ := io.ReadAll(r.Body)
		q := new(dnsmessage.Msg)
		if err := q.Unpack(raw); err != nil {
			return nil, err
		}
		a := new(dnsmessage.Msg)
		a.SetReply(q)
		a.Answer = append(a.Answer, &dnsmessage.A{Hdr: dnsmessage.RR_Header{Name: q.Question[0].Name, Rrtype: dnsmessage.TypeA, Class: dnsmessage.ClassINET, Ttl: 60}, A: net.ParseIP("192.0.2.20")})
		wire, _ := a.Pack()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/dns-message"}}, Body: io.NopCloser(bytes.NewReader(wire))}, nil
	})}}
	bridge.httpMu.Unlock()
	for name, want := range map[string]string{"node.fixture.test.": "192.0.2.20", "website.fixture.test.": "192.0.2.10"} {
		q := new(dnsmessage.Msg)
		q.SetQuestion(name, dnsmessage.TypeA)
		a, _, err := (&dnsmessage.Client{Timeout: time.Second}).Exchange(q, effective.Global.BootstrapResolver)
		if err != nil || len(a.Answer) != 1 || a.Answer[0].(*dnsmessage.A).A.String() != want {
			t.Fatal(fmt.Sprint("wrong resolver for ", name), err)
		}
	}
	if doh.Load() != 1 {
		t.Fatal("website query leaked into node DNS policy", doh.Load())
	}
}

func TestNodeDNSBridgeLifecycleAndImmutableConfig(t *testing.T) {
	conf := &daeConfig.Config{Global: daeConfig.Global{NailinkNodeDNS: `{"node.fixture.test":["https://dns.google/dns-query"]}`}}
	effective, bridge, err := routerWithNodeDNS(conf, nil)
	if err != nil {
		t.Fatal(err)
	}
	if conf.Global.BootstrapResolver != "" || len(conf.Dns.Upstream) != 0 {
		t.Fatal("original configuration mutated")
	}
	if effective.Global.BootstrapResolver == "" || len(effective.Dns.Routing.Request.Rules) != 1 {
		t.Fatal("runtime node routing missing")
	}
	bridge.Close()
	bridge.Close()
	if _, err = net.DialTimeout("tcp", effective.Global.BootstrapResolver, 50*time.Millisecond); err == nil {
		t.Fatal("resolver listener survived close")
	}
	_, err = bridge.nodeDNSDialer(&net.Dialer{}).DialContext(context.Background(), "tcp", "127.0.0.1:443")
	if err == nil {
		t.Fatal("private resolver dial was not blocked")
	}
}

func TestNativeProbeSeparatesColdAndVerifiedReuse(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(204) }))
	defer srv.Close()
	dial := routerProbeDialFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		time.Sleep(80 * time.Millisecond)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	})
	r := routerProbeHTTPTimeout(context.Background(), dial, daeConfig.Global{TcpCheckUrl: []string{srv.URL + "/generate_204"}}, time.Second)
	if !r.Alive || r.Latency < 75 || r.SteadyMs == nil || *r.SteadyMs >= r.Latency || requests.Load() != 2 {
		t.Fatal("cold/reused measurements", r, requests.Load())
	}
}

func TestNativeProbeDoesNotInventReuseForClosingServer(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Connection", "close")
		w.WriteHeader(204)
	}))
	defer srv.Close()
	r := routerProbeHTTPTimeout(context.Background(), routerProbeDialFunc((&net.Dialer{}).DialContext), daeConfig.Global{TcpCheckUrl: []string{srv.URL + "/generate_204"}}, time.Second)
	if !r.Alive || r.SteadyMs != nil || requests.Load() != 1 {
		t.Fatal("invented reused latency", r)
	}
}

func TestSharedHTTPProbeRejectsUntrustedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	r := ProbeRouterHTTP(context.Background(), routerProbeDialFunc((&net.Dialer{}).DialContext), daeConfig.Global{TcpCheckUrl: []string{srv.URL + "/generate_204"}})
	if r.Alive || r.Message != "tls_failed" || r.SteadyMs != nil {
		t.Fatal("untrusted certificate classification or acceptance", r)
	}
}
