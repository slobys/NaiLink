# IPv6 分流与节点验证（2026-11-16）

## 范围与结果

实机为 ARM64 iStoreOS 25.12.6，Linux 6.12.112，已安装 NaiLink 0.6.84-1。
本次在该版本上部署开发测试补丁，未发布新正式版。sing-box 为 1.14.4-nailink.1，mihomo 为 1.19.34。

电脑测试统一使用 `curl --noproxy '*'`，排除 Windows 上已启用的 v2rayN 系统代理。
PassWall 和 NaiLink 顺序运行，每次切换都设置独立恢复措施；测试结束恢复 PassWall 运行、NaiLink 停止及原来的 dae / TUN 选择。

| 路径                                 | IPv4 | IPv6 | IPv6 大包 1600 字节 | 双栈 | IPv6 DNS 检测 |
| ------------------------------------ | ---- | ---- | ------------------- | ---- | ------------- |
| PassWall，同一香港节点               | 200  | 200  | 200                 | 200  | 200           |
| NaiLink 原规则，dae                  | 200  | 超时 | 超时                | 200  | 超时          |
| 增加 IPv6 兜底，dae                  | 200  | 200  | 200                 | 200  | 200           |
| 增加 IPv6 兜底，sing-box TUN         | 200  | 200  | 200                 | 200  | 200           |
| 增加 IPv6 兜底及适配修复，mihomo TUN | 200  | 200  | 200                 | 200  | 200           |

测试使用 test-ipv6.com 当前公开的 osaka 测试地址，分别请求 IPv4、IPv6、双栈、大包与 IPv6 权威 DNS 地址。
检查了 IPv6 测试响应中的实际地址族，三内核均为 IPv6；IPv6 HTTPS 请求也分别返回 200。
这是检测请求和数据面的实测结果，未将其表述为浏览器界面的“10/10”评分。

## 原因与改动

1. PassWall 当前关闭 IPv6 透明接管；NaiLink 原有 `fallback: proxy` 则把未命中直连规则的 IPv6 交给香港节点。
   同一个节点经 PassWall SOCKS 和独立内核拨号都无法完成本次 IPv6 目标连接，原生 IPv6 路径正常。
2. 在现有明确分流规则之后、代理兜底之前增加 `ipversion(6) -> direct`。AI、Netflix 和用户明确规则优先，保留当前全部原始规则。
   新建常用分流预设采用这一策略，已有保存的路由不自动改写；已有明确的全 IPv6 策略也不被覆盖。
3. mihomo 嗅探覆盖目标时会清空原始目标 IP，导致 IPv6 规则失效。
   当配置包含 `ipversion` 匹配时保留原始目标，仍启用嗅探域名匹配；其他配置保留原来的目标覆盖行为。
   依据：[固定版本上游嗅探实现](https://github.com/MetaCubeX/mihomo/blob/0455d0d28c4526937380505153e65a843d8612e8/component/sniffer/dispatcher.go#L157)、[域名匹配使用 SniffHost](https://github.com/MetaCubeX/mihomo/blob/0455d0d28c4526937380505153e65a843d8612e8/constant/metadata.go#L261)。

mihomo 修复后，保留现有 AI 群组，分别强制使用 IPv4、IPv6 目标访问 ChatGPT CSRF 接口，均返回 200。
这验证了域名分流仍优先，不代表已完成登录账号或发送聊天消息测试。

## IPv6 服务器节点

用户尚未部署公网 IPv6 节点，因此公网 VPS 端到端验证保持待测。
已在软路由上使用带认证、仅监听 `::1` 的临时 IPv6 SOCKS 服务端，三个内核分别通过该 IPv6 节点访问真实 IPv4、IPv6 HTTP 目标，六项请求均返回 200。
另用 VLESS WebSocket TLS、Trojan、SOCKS5 示例链接回归验证 IPv6 地址、端口和 TLS / SNI 保存与转换。
这些结果验证节点导入和 IPv6 传输链路，不外推为所有协议、固件、架构或公网 VPS 已通过。

节点服务器地址族与网站地址族独立。希望其余 IPv6 网站也通过代理时，可移除 IPv6 直连兜底，并使用具备 IPv6 出口的节点。
原生 IPv6 兜底本身不会为没有 IPv6 网络的家庭提供 IPv6。

## 保存与验证

- 123 个节点、3 个订阅、3 个群组及绑定保留；配置、DNS、认证和 UCI 原值核对一致。
- 所选路由仅新增上述 IPv6 兜底规则和说明，未移除原有规则。
- 后端与配置的原始备份保留在软路由本地；临时进程和测试目录已清理。
- 配置服务回归、mihomo / 核心相关回归、IPv6 导入回归通过；文档构建检查 11 页、97 个搜索段落通过。
- 测试后端 SHA-256：`cfdc9af04941d4df2c7420caed0b5c77eedcaacdb7083827928f8e3e75dc730a`。

本地完整诊断位于被忽略的 `.build/ipv6-comparison/`，包含原始配置、请求响应和备份位置，不应公开上传。
