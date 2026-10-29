// SPDX-License-Identifier: AGPL-3.0-only
package subscription

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae-wing/routercore"
	legacy "github.com/daeuniverse/dae/common/subscription"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

type subscriptionContentError struct{ message string }

func (e *subscriptionContentError) Error() string     { return e.message }
func subscriptionContentFailure(message string) error { return &subscriptionContentError{message} }
func isSubscriptionContentError(err error) bool {
	var content *subscriptionContentError
	return errors.As(err, &content)
}

var subscriptionScheme = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://`)

func decodeSubscriptionPayload(payload []byte) ([]string, error) {
	return decodeSubscriptionPayloadContext(context.Background(), payload)
}
func decodeSubscriptionPayloadContext(ctx context.Context, payload []byte) ([]string, error) {
	if len(payload) > 4<<20 {
		return nil, subscriptionContentFailure("订阅内容超过 4 MiB，已保留原节点。")
	}
	payload = bytes.TrimSpace(bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf}))
	if len(payload) == 0 {
		return nil, subscriptionContentFailure("订阅内容为空，已保留原节点。")
	}
	compact := strings.Join(strings.Fields(string(payload)), "")
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, e := encoding.DecodeString(compact); e == nil && len(decoded) > 0 && utf8.Valid(decoded) {
			payload = bytes.TrimSpace(bytes.TrimPrefix(decoded, []byte{0xef, 0xbb, 0xbf}))
			break
		}
	}
	if !utf8.Valid(payload) {
		return nil, subscriptionContentFailure("订阅不是有效的文本内容，已保留原节点。")
	}
	if bytes.HasPrefix(payload, []byte("<")) {
		return nil, subscriptionContentFailure("订阅返回了网页或登录提示，请检查订阅地址和有效期；已保留原节点。")
	}
	log := logrus.New()
	log.SetOutput(io.Discard)
	if bytes.HasPrefix(payload, []byte("{")) {
		var sip struct {
			Version int             `json:"version"`
			Servers json.RawMessage `json:"servers"`
		}
		if json.Unmarshal(payload, &sip) == nil && sip.Version == 1 && sip.Servers != nil {
			links, e := legacy.ResolveSubscriptionAsSIP008(log, payload)
			if e != nil {
				return nil, subscriptionContentFailure("SIP008 订阅格式异常，已保留原节点。")
			}
			return validateSubscriptionLinks(links)
		}
	}
	var plain []string
	plainFormat := true
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !subscriptionScheme.MatchString(line) {
			plainFormat = false
			break
		}
		plain = append(plain, line)
	}
	if plainFormat && len(plain) > 0 {
		return validateSubscriptionLinks(plain)
	}
	return decodeClashSubscriptionContext(ctx, payload)
}

func validateSubscriptionLinks(links []string) ([]string, error) {
	if len(links) == 0 || len(links) > 4096 {
		return nil, subscriptionContentFailure("订阅没有有效节点或节点超过 4096 个，已保留原节点。")
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(links))
	var problems []string
	invalid := 0
	for i, link := range links {
		valid := subscriptionScheme.MatchString(link) && len(link) <= 180000
		if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
			u, e := url.Parse(link)
			valid = valid && e == nil
			if e == nil {
				valid = valid && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && (u.Port() != "" || u.User != nil)
			}
		}
		if valid {
			n, e := db.NewNodeModel(link, nil, nil)
			valid = e == nil && n != nil
			if valid {
				link = n.Link
			} // Native URI aliases must match the persisted canonical link on refresh.
		}
		if !valid {
			invalid++
			if len(problems) < 12 {
				problems = append(problems, fmt.Sprintf("第 %d 项节点格式或协议参数无效", i+1))
			}
			continue
		}
		if !seen[link] {
			seen[link] = true
			result = append(result, link)
		}
	}
	if invalid > 0 {
		return nil, subscriptionContentFailure(fmt.Sprintf("订阅有 %d 项无法识别，未替换原节点：%s。", invalid, strings.Join(problems, "；")))
	}
	return result, nil
}

func decodeClashSubscription(payload []byte) ([]string, error) {
	return decodeClashSubscriptionContext(context.Background(), payload)
}
func decodeClashSubscriptionContext(ctx context.Context, payload []byte) ([]string, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	var doc yaml.Node
	if e := decoder.Decode(&doc); e != nil || len(doc.Content) != 1 {
		return nil, subscriptionContentFailure("订阅格式无法识别，请使用 Clash YAML、SIP008 或节点链接订阅；已保留原节点。")
	}
	var trailing yaml.Node
	if e := decoder.Decode(&trailing); e != io.EOF {
		return nil, subscriptionContentFailure("订阅包含多个 YAML 文档或尾部内容异常，已保留原节点。")
	}
	budget := 131072
	if e := validateSubscriptionYAML(&doc, 0, &budget, map[*yaml.Node]bool{}); e != nil {
		return nil, e
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, subscriptionContentFailure("订阅缺少 proxies 节点列表，已保留原节点。")
	}
	var proxies *yaml.Node
	providers := false
	for i := 0; i < len(root.Content); i += 2 {
		switch root.Content[i].Value {
		case "proxies":
			proxies = root.Content[i+1]
		case "proxy-providers":
			providers = true
		}
	}
	if proxies == nil {
		if providers {
			return nil, subscriptionContentFailure("该 Clash 配置只有 proxy-providers 外部引用，没有内嵌节点；请使用提供 proxies 列表的节点订阅。不会读取配置中的文件或自动下载外部引用。")
		}
		return nil, subscriptionContentFailure("订阅缺少 proxies 节点列表，已保留原节点。")
	}
	if proxies.Kind == yaml.AliasNode {
		proxies = proxies.Alias
	}
	if proxies == nil || proxies.Kind != yaml.SequenceNode || len(proxies.Content) == 0 || len(proxies.Content) > 4096 {
		return nil, subscriptionContentFailure("Clash proxies 必须是 1 至 4096 个节点的列表，已保留原节点。")
	}
	var links, problems []string
	invalid := 0
	for i, entry := range proxies.Content {
		var p map[string]any
		var err error
		if e := entry.Decode(&p); e != nil || p == nil {
			err = errors.New("invalid_node")
		} else {
			link, e := routercore.ClashLink(p)
			err = e
			if err == nil && link != "" {
				links = append(links, link)
			}
		}
		if err != nil {
			invalid++
			if len(problems) < 12 {
				problems = append(problems, fmt.Sprintf("第 %d 项：%s", i+1, routercore.DescribeError(err)))
			}
		}
	}
	if invalid > 0 {
		return nil, subscriptionContentFailure(fmt.Sprintf("Clash 订阅有 %d 项无法转换，未替换原节点：%s", invalid, strings.Join(problems, "；")))
	}
	valid, err := validateSubscriptionLinks(links)
	if err == nil {
		extractSubscriptionNodeDNS(ctx, root, proxies)
	}
	return valid, err
}

func validateSubscriptionYAML(node *yaml.Node, depth int, budget *int, active map[*yaml.Node]bool) error {
	*budget--
	if node == nil || depth > 40 || *budget < 0 || active[node] {
		return subscriptionContentFailure("订阅 YAML 嵌套或引用过多，已保留原节点。")
	}
	active[node] = true
	defer delete(active, node)
	if node.Kind == yaml.AliasNode {
		return validateSubscriptionYAML(node.Alias, depth+1, budget, active)
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || seen[key.Value] {
				return subscriptionContentFailure("订阅 YAML 存在重复或异常的字段，已保留原节点。")
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if e := validateSubscriptionYAML(child, depth+1, budget, active); e != nil {
			return e
		}
	}
	return nil
}
