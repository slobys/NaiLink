// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

type optionProblem struct {
	code   string
	fields []string
}

func (e *optionProblem) Error() string { return e.code }

var safeOptionName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)

func OptionProblem(code string, fields ...string) error {
	clean := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		if !safeOptionName.MatchString(field) {
			field = "异常参数名"
		}
		if !seen[field] {
			clean = append(clean, field)
			seen[field] = true
		}
	}
	sort.Strings(clean)
	return &optionProblem{code, clean}
}

// Reports include parameter names only, never credentials, URLs or values.
func DescribeError(err error) string {
	message := ProblemText(err.Error())
	var option *optionProblem
	if errors.As(err, &option) && len(option.fields) > 0 {
		message += "（参数：" + strings.Join(option.fields, "、") + "）"
	}
	return message
}
func ProblemText(code string) string {
	if message, ok := problemMessages[code]; ok {
		return message
	}
	if strings.HasPrefix(code, "unsupported_rule_") {
		return "当前路由匹配方式尚未适配，原规则保留。"
	}
	return "配置暂未通过兼容性检查，请核对协议、传输方式和附加参数；原配置保留。"
}

var problemMessages = map[string]string{
	"tproxy_tor_unsupported":         "当前配置包含 Tor 子进程，请选择 TUN，避免非 TUN 模式形成出站循环；节点与配置保留。",
	"tproxy_bridge_unsupported":      "LAN 桥接过滤需要 nftables broute 支持。请准备固件匹配的非 TUN 依赖（含 kmod-nft-bridge），仍不支持时选择 TUN；不会关闭 Docker 桥接过滤。",
	"tproxy_bridge_rpf":              "LAN 桥接端口启用了与非 TUN 转发不兼容的反向路径检查。请选择 TUN；NaiLink 不会修改系统安全设置。",
	"tproxy_bridge_vlan_unsupported": "当前带 VLAN 的 LAN 桥接过滤拓扑暂不支持非 TUN 接管，请选择 TUN；原有 VLAN 与分流配置保留。",
	"mihomo_protocol_unsupported":    "此协议暂未适配 mihomo，请保留节点并选择其他内核",
	"mihomo_option_unsupported":      "节点参数尚不能等价转换为 mihomo，原参数保留",
	"mihomo_rule_unsupported":        "路由匹配不能安全转换为 mihomo，原规则保留",
	"mihomo_dns_unsupported":         "DNS 规则不能安全转换为 mihomo，原配置保留",

	"invalid_naive_concurrency":  "Naive 并发连接参数必须为 0 至 4，避免过多连接占用资源",
	"unsupported_naive_insecure": "Naive / Cronet 不支持跳过证书验证，请配置正确证书或内联 CA",

	"extra_not_installed":          "该节点需要安装 sing-box 扩展协议包，请在代理内核窗口安装后重试",
	"too_many_tor_nodes":           "每个运行配置最多启用一个 Tor 节点，避免多进程占用过多资源",
	"unsupported_tor_option":       "Tor 仅允许出口国家和排除节点选项，不接受程序路径、命令或自定义文件",
	"unsupported_wireguard_system": "WireGuard 使用用户态 endpoint，不允许创建系统网卡或监听端口",
	"invalid_wireguard_key":        "WireGuard 密钥必须为有效的 32 字节 Base64 值",
	"invalid_wireguard_address":    "WireGuard 必须填写有效的本地 IP 前缀",
	"invalid_wireguard_peer":       "WireGuard 对端地址、端口、公钥或允许网段无效",
	"invalid_wireguard_workers":    "WireGuard 工作线程必须为 1 至 4，默认使用 1 个",
	"invalid_wireguard_mtu":        "WireGuard MTU 必须为 576 至 9000",

	"invalid_native_node":                 "sing-box 原生节点 JSON 格式或服务器参数不正确",
	"unsupported_native_section":          "这里只接受单个出站或 outbounds 出站组合，不接受完整系统配置",
	"unsupported_native_field":            "原生节点包含当前面板不允许的文件、程序或系统设置字段",
	"invalid_native_detour":               "出站组合的 detour 引用了外部节点或形成循环",
	"shadowtls_requires_inner":            "ShadowTLS 需与 Shadowsocks 等内层代理组成 outbounds 组合",
	"unsupported_dial_mode":               "当前拨号模式尚未适配，仅支持按域名或按 IP 拨号；原设置保留",
	"unsupported_node_option":             "节点包含尚未支持或重复的参数",
	"unsupported_header_type":             "当前伪装头类型尚未适配，仅支持无额外伪装头",
	"invalid_udp_option":                  "UDP 开关必须为 0、1、true 或 false",
	"invalid_spider_path":                 "REALITY 爬虫路径格式异常，或被用于非 REALITY 节点",
	"invalid_node":                        "节点链接、认证信息、地址或端口格式异常",
	"invalid_node_name":                   "节点缺少可用名称",
	"conflicting_node_options":            "同一设置的别名参数互相冲突",
	"invalid_tls_option":                  "证书校验开关格式异常",
	"unsupported_node_path":               "当前协议不支持链接中的额外路径",
	"unsupported_ss_plugin":               "Shadowsocks 插件尚未适配",
	"unsupported_vless_encryption":        "VLESS 加密方式尚未适配",
	"unsupported_packet_encoding":         "UDP 数据包编码方式尚未适配",
	"unsupported_tls":                     "传输安全方式尚未适配",
	"invalid_reality":                     "REALITY 缺少有效公钥或认证参数",
	"unsupported_quic_fingerprint":        "QUIC 节点不能使用 TCP 指纹参数",
	"unsupported_reality_key_share":       "该 REALITY 密钥交换限制暂不能等价转换；原设置保留",
	"unsupported_ech":                     "当前传输安全方式不能使用该 ECH 配置",
	"invalid_ech":                         "ECH 配置格式异常；不会忽略 ECH 或降低证书校验",
	"invalid_ech_public_name":             "ECH 配置缺少有效公开域名；请让节点提供方修正 ECH 配置，原链接保留",
	"unsupported_hy2_obfs":                "HY2 混淆方式尚未适配",
	"invalid_port_range":                  "跳跃端口范围格式异常",
	"invalid_bandwidth":                   "带宽参数必须为正整数",
	"unsupported_protocol":                "节点协议尚未适配此内核",
	"unsupported_grpc_mode":               "gRPC 模式尚未适配",
	"unsupported_grpc_authority":          "自定义 gRPC 认证目标尚未适配",
	"unsupported_transport":               "节点传输方式尚未适配",
	"unsupported_transport_option":        "附加传输参数与当前传输方式不匹配",
	"unsupported_vmess_option":            "VMess 包含尚未支持的参数",
	"unsupported_vmess_alterid":           "仅支持 alterId 为 0 的 VMess AEAD",
	"unsupported_vmess_header":            "VMess 伪装头类型尚未适配",
	"unresolved_subscription":             "运行配置中仍有未解析的订阅",
	"unsupported_global_option":           "存在尚不能等价转换的全局选项，例如 MPTCP 或 TLS 分片",
	"unsupported_dns_option":              "DNS 响应规则、固定 TTL 或自定义缓存容量尚未适配",
	"unsupported_group_filter_annotation": "群组过滤器的附加设置尚未适配",
	"unsupported_group_filter":            "群组过滤方式尚未适配",
	"invalid_group_node":                  "群组引用了不存在或无效的节点",
	"empty_group":                         "群组中没有可用节点",
	"unsupported_group_policy":            "群组选择策略尚未适配",
	"unsupported_group_policy_parameter":  "群组选择策略的附加参数尚未适配",
	"invalid_fixed_group":                 "固定策略仅支持单个节点；原群组保持不变",
	"missing_check_url":                   "尚未配置连接测试地址",
	"invalid_check_interval":              "连接检查间隔格式异常",
	"unsupported_rule_target":             "路由规则的目标动作尚未适配",
	"unsupported_fallback":                "路由回退目标尚未适配",
	"unsupported_multi_wan":               "多个 WAN 接口尚不能等价转换",
	"missing_lan_interface":               "尚未配置需要接管的 LAN 接口",
	"geodata_missing":                     "找不到配置引用的规则库",
	"invalid_geodata_reference":           "规则库引用格式异常",
	"invalid_external_geodata":            "外部规则库引用格式异常；仅支持配置目录中的 .dat 文件名和条目名称",
	"unsupported_geoip":                   "IP 规则库匹配尚未适配",
	"unsupported_geosite":                 "域名规则库匹配尚未适配",
	"invalid_geodata":                     "规则库内容无效",
	"empty_geodata":                       "规则库引用未匹配到内容",
	"empty_rule":                          "路由规则没有可用的匹配条件",
	"unsupported_rule_parameter":          "路由匹配附加参数尚未适配",
	"unsupported_rule":                    "路由匹配方式尚未适配",
	"unsupported_domain_match":            "域名匹配方式尚未适配",
	"unsupported_dns_match":               "DNS 匹配方式尚未适配",
	"unsupported_source_geoip":            "来源地址的规则库匹配尚未适配",
	"invalid_ip_rule":                     "IP 匹配规则格式异常",
	"invalid_port_rule":                   "端口匹配规则格式异常",
	"unsupported_protocol_match":          "协议匹配方式尚未适配",
	"unsupported_ip_version":              "IP 版本匹配尚未适配",
	"unsupported_process_rule":            "进程匹配规则尚未适配",
	"unsupported_dns_upstream":            "DNS 上游格式异常或名称重复",
	"unsupported_dns_transport":           "DNS 上游传输方式尚未适配",
	"dns_upstream_requires_ip":            "DNS 上游地址需要使用 IP，以避免启动时循环解析",
	"invalid_dns_port":                    "DNS 上游端口格式异常",
	"unsupported_dns_detour":              "DNS 上游的代理出口尚未适配",
	"unsupported_dns_parameter":           "DNS 上游包含尚未适配的参数",
	"unsupported_dns_fallback":            "DNS 回退策略尚未适配",
	"unsupported_dns_target":              "DNS 规则引用的上游尚未适配",
	"invalid_dns_preference":              "DNS 地址族偏好格式异常",
}
