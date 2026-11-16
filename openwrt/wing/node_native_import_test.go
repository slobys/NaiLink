// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"strings"
	"testing"

	"github.com/daeuniverse/dae-wing/routercore"
	_ "github.com/daeuniverse/outbound/dialer/http"
	_ "github.com/daeuniverse/outbound/dialer/socks"
	_ "github.com/daeuniverse/outbound/dialer/trojan"
	_ "github.com/daeuniverse/outbound/dialer/v2ray"
	_ "github.com/daeuniverse/outbound/protocol/trojanc"
	_ "github.com/daeuniverse/outbound/protocol/vless"
)

func TestRouterMihomoTransportImportPreservesOriginalLink(t *testing.T) {
	link := "vless://11111111-1111-4111-8111-111111111111@fixture.invalid:443?type=xhttp&path=%2Fxhttp&mode=auto#XHTTP%20node"
	model, err := NewNodeModel(link, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if model.Link != link || model.Name != "XHTTP node" || model.Address != "fixture.invalid:443" || model.Protocol != "vless" {
		t.Fatal("saved metadata changed")
	}
	if _, err = NewNodeModel(strings.Replace(link, "mode=auto", "mode=invalid", 1), nil, nil); err == nil {
		t.Fatal("invalid transport saved")
	}
}

func TestRouterIPv6ServerImportPreservesAddressAndTLSName(t *testing.T) {
	for _, link := range []string{
		"vless://11111111-1111-4111-8111-111111111111@[2001:db8::10]:443?encryption=none&security=tls&type=ws&sni=node.example&host=node.example&path=%2Fproxy#IPv6",
		"trojan://fixture-password@[2001:db8::10]:443?sni=node.example#IPv6",
		"socks5://fixture:password@[2001:db8::10]:443#IPv6",
	} {
		model, err := NewNodeModel(link, nil, nil)
		if err != nil || model.Link != link || model.Address != "[2001:db8::10]:443" {
			t.Fatal("IPv6 share-link metadata changed", strings.SplitN(link, ":", 2)[0], err)
		}
		native, err := routercore.Node(link, "fixture", routercore.NodeOptions{})
		if err != nil || native["server"] != "2001:db8::10" {
			t.Fatal("invalid sing-box IPv6 server", err)
		}
		mihomo, err := routercore.MihomoNode(native)
		if err != nil || mihomo["server"] != "2001:db8::10" {
			t.Fatal("invalid mihomo IPv6 server", err)
		}
		if strings.HasPrefix(link, "vless:") || strings.HasPrefix(link, "trojan:") {
			if native["tls"].(routercore.Object)["server_name"] != "node.example" || mihomo["sni"] != "node.example" && mihomo["servername"] != "node.example" {
				t.Fatal("replaced TLS server name with the IPv6 address")
			}
		}
	}
}

func TestRouterChainImportPreservesBothLinks(t *testing.T) {
	entry := "socks5://user:fixture@entry.invalid:1080#入口"
	landing := "http://user:fixture@landing.invalid:8080#落地"
	link := landing + " -> " + entry
	model, err := NewNodeModel(link, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if model.Link != link || model.Protocol != "chain" || model.Name != "入口 → 落地" || model.Address != "landing.invalid:8080" {
		t.Fatal("saved chain metadata or URI changed")
	}
	if _, err = NewNodeModel(entry+" -> "+strings.Replace(entry, "#入口", "#other", 1), nil, nil); err == nil {
		t.Fatal("accepted the same hop with two labels")
	}
}

func TestRouterNativeImportPreservesOptions(t *testing.T) {
	raw := `{"type":"ssh","tag":"测试 SSH","server":"fixture.invalid","server_port":22,"user":"fixture","password":"private-value","host_key":["ssh-ed25519 fixture"]}`
	model, err := NewNodeModel(raw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if model.Protocol != "ssh" || model.Name != "测试 SSH" || model.Address != "fixture.invalid:22" || !strings.HasPrefix(model.Link, "singbox://") {
		t.Fatal("metadata mismatch")
	}
	out, err := routercore.NativeNode(model.Link, "test")
	if err != nil || out["password"] != "private-value" {
		t.Fatal("options changed")
	}
	canonical, err := NewNodeModel(model.Link, nil, nil)
	if err != nil || canonical.Link != model.Link {
		t.Fatal("stored link cannot be imported")
	}
	_, err = NewNodeModel(`{"type":"ssh","server":"fixture.invalid","server_port":22,"private_key_path":"/etc/shadow"}`, nil, nil)
	if err == nil || strings.Contains(err.Error(), "/etc/shadow") || strings.Contains(err.Error(), "private-value") {
		t.Fatal("unsafe diagnostic")
	}
}
