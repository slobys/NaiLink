// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"encoding/base64"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

const xhttpFixture = "vless://11111111-1111-4111-8111-111111111111@fixture.invalid:443"

func TestXHTTPImportAndMihomoPreserveOptions(t *testing.T) {
	enc := "mlkem768x25519plus.native.0rtt." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, encryption := range []string{"none", enc} {
		q := url.Values{"type": {"xhttp"}, "security": {"reality"}, "pbk": {"fixture-key"}, "sid": {"abcd"}, "sni": {"fixture.test"}, "path": {"/xhttp"}, "mode": {"auto"}, "x_padding_bytes": {"100-1000"}, "extra": {`{"mode":"auto","xPaddingBytes":"100-1000"}`}, "encryption": {encryption}}
		if encryption != "none" {
			q.Set("flow", "xtls-rprx-vision")
		}
		link := xhttpFixture + "?" + q.Encode() + "#测试节点"
		n, err := Node(link, "fixture", NodeOptions{Import: true})
		if err != nil {
			t.Fatal(err)
		}
		m, err := MihomoNode(n)
		if err != nil {
			t.Fatal(err)
		}
		tr := m["xhttp-opts"].(Object)
		if m["network"] != "xhttp" || tr["path"] != "/xhttp" || tr["mode"] != "auto" || tr["x-padding-bytes"] != "100-1000" || m["reality-opts"].(Object)["public-key"] != "fixture-key" || m["skip-cert-verify"] != false {
			t.Fatal("transport or authentication changed")
		}
		if encryption != "none" && (m["encryption"] != encryption || m["flow"] != "xtls-rprx-vision") {
			t.Fatal("VLESS encryption/flow changed")
		}
		clash := Object{"name": "测试节点", "type": "vless", "server": "fixture.invalid", "port": 443, "uuid": "11111111-1111-4111-8111-111111111111", "tls": true, "servername": "fixture.test", "network": "xhttp", "xhttp-opts": tr, "reality-opts": Object{"public-key": "fixture-key", "short-id": "abcd"}, "encryption": encryption}
		if encryption != "none" {
			clash["flow"] = "xtls-rprx-vision"
		}
		converted, err := ClashLink(clash)
		if err != nil {
			t.Fatal(err)
		}
		n2, err := Node(converted, "fixture", NodeOptions{Mihomo: true})
		if err != nil || !reflect.DeepEqual(n, n2) {
			t.Fatal("Clash and URI semantics differ", err)
		}
		if _, err = Node(link, "fixture", NodeOptions{}); err == nil || err.Error() != "node_requires_mihomo" {
			t.Fatal("sing-box/dae capability gate missing", err)
		}
	}
}

func TestXHTTPUnknownConflictingAndMalformedOptionsFailClosed(t *testing.T) {
	for _, query := range []string{
		"type=xhttp&mode=wrong", "type=xhttp&extra=null", "type=xhttp&extra=%7B%7D%7B%7D",
		"type=xhttp&extra=" + url.QueryEscape(`{"mode":"auto","mode":"stream-up"}`),
		"type=xhttp&mode=auto&extra=" + url.QueryEscape(`{"mode":"stream-up"}`),
		"type=xhttp&extra=" + url.QueryEscape(`{"downloadSettings":{"address":"private-value"}}`),
		"type=xhttp&extra=" + url.QueryEscape(`{"noGRPCHeader":"false"}`),
		"type=xhttp&extra=" + url.QueryEscape(`{"headers":{"X-Test":"private-value\r\nInjected: value"}}`),
		"type=xhttp&x_padding_bytes=1000-100", "type=xhttp&x_padding_bytes=0-999999999",
		"type=xhttp&type=tcp", "type=xhttp&path=invalid", "type=xhttp&extra=invalid",
		"type=tcp&extra=" + url.QueryEscape(`{"mode":"auto"}`),
		"encryption=private-value", "encryption=mlkem768x25519plus.native.0rtt.short",
	} {
		_, err := Node(xhttpFixture+"?"+query, "fixture", NodeOptions{Import: true})
		if err == nil || strings.Contains(DescribeError(err), "private-value") {
			t.Fatal("unsafe import or diagnostic")
		}
	}
	if _, err := Node(strings.Replace(xhttpFixture, "11111111-1111-4111-8111-111111111111", "invalid", 1)+"?type=xhttp", "fixture", NodeOptions{Import: true}); err == nil {
		t.Fatal("invalid UUID saved")
	}
}

func TestClashShadowsocksTCPMarkerDoesNotDisableUDP(t *testing.T) {
	for _, network := range []string{"tcp", "raw"} {
		p := Object{"type": "ss", "name": "fixture", "server": "fixture.invalid", "port": 443, "cipher": "aes-128-gcm", "password": "fixture", "network": network, "tls": false, "udp": true}
		link, err := ClashLink(p)
		if err != nil {
			t.Fatal(err)
		}
		n, err := Node(link, "fixture", NodeOptions{})
		if err != nil || n["network"] != nil || n["transport"] != nil {
			t.Fatal("SS UDP disabled by transport marker", err)
		}
		p["network"] = "ws"
		if _, err = ClashLink(p); err == nil {
			t.Fatal("SS WebSocket option ignored")
		}
		p["network"], p["udp"] = network, false
		link, err = ClashLink(p)
		if err != nil {
			t.Fatal(err)
		}
		n, err = Node(link, "fixture", NodeOptions{})
		if err != nil || n["network"] != "tcp" {
			t.Fatal("explicit UDP preference lost", err)
		}
	}
}
