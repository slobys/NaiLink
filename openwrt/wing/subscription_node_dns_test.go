// SPDX-License-Identifier: AGPL-3.0-only
package subscription

import (
	"context"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae-wing/graphql/internal"
	"github.com/daeuniverse/dae-wing/routercore"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const nodeDNSFixture = `dns:
  nameserver-policy:
    '*.fixture.test': [https://dns.google/dns-query]
    'website.example': [https://cloudflare-dns.com/dns-query]
proxies:
  - {name: Fixture, type: trojan, server: node.fixture.test, port: 443, password: fixture}
  - {name: IP, type: trojan, server: 192.0.2.3, port: 443, password: fixture}
`

func TestNodeDNSMetadataOnlyActualServerHostsAndOriginalLinks(t *testing.T) {
	ctx, capture := captureNodeDNS(context.Background())
	links, err := decodeSubscriptionPayloadContext(ctx, []byte(nodeDNSFixture))
	if err != nil || len(links) != 2 {
		t.Fatal("fixture failed", err)
	}
	policies := routercore.DecodeNodeDNS(capture.value)
	if len(policies) != 1 || len(policies["node.fixture.test"]) != 1 {
		t.Fatal("node DNS scope lost", policies)
	}
	plain, err := decodeSubscriptionPayload([]byte(nodeDNSFixture))
	if err != nil || plain[0] != links[0] {
		t.Fatal("metadata changed original node link")
	}
}

func TestNodeDNSImportAndFailedRefreshAreAtomic(t *testing.T) {
	if err := db.InitDatabase(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	db.SetOutput(io.Discard)
	payload := nodeDNSFixture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }))
	defer srv.Close()
	d := db.DB(context.Background())
	result, err := Import(d, true, &internal.ImportArgument{Link: srv.URL})
	if err != nil || result == nil {
		t.Fatal("initial import", err)
	}
	var before, after db.Subscription
	if err = d.First(&before).Error; err != nil || before.NodeDNS == "" {
		t.Fatal("metadata not saved", err)
	}
	var nodes []db.Node
	d.Find(&nodes)
	value, err := db.NodeDNSForNodes(d, nodes)
	if err != nil || value != routercore.PackNodeDNS(before.NodeDNS) {
		t.Fatal("runtime metadata not found", err)
	}
	payload = "<html>expired</html>"
	if _, err = UpdateById(context.Background(), before.ID); err == nil {
		t.Fatal("bad refresh succeeded")
	}
	d.First(&after, before.ID)
	if after.NodeDNS != before.NodeDNS {
		t.Fatal("failed refresh replaced metadata")
	}
}
