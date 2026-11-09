// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"strings"
	"testing"

	"github.com/daeuniverse/dae-wing/routercore"
	_ "github.com/daeuniverse/outbound/dialer/http"
	_ "github.com/daeuniverse/outbound/dialer/socks"
)

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
