<p align="center">
  <img src="docs/site/assets/nailink-logo.png" width="112" height="112" alt="NaiLink Logo" />
</p>

<h1 align="center">NaiLink · 奶油互联</h1>

<p align="center">适用于 OpenWrt、ImmortalWrt、iStoreOS 的代理管理插件。</p>

<p align="center">
  <a href="https://slobys.github.io/NaiLink/">在线文档</a> ·
  <a href="https://slobys.github.io/NaiLink/quick-start.html">首次设置</a> ·
  <a href="https://github.com/slobys/NaiLink/releases">下载安装包</a> ·
  <a href="https://slobys.github.io/NaiLink/troubleshooting.html">常见问题</a>
</p>

管理节点、订阅和群组，测试节点延迟，查看实时流量，配置 DNS 与路由。支持电脑、手机和多种主题，沿用路由器 LuCI 登录，无需另设账户。

基于 [daed](https://github.com/daeuniverse/daed) 二次开发，独立维护。当前版本为 **[0.6.58 测试版](https://github.com/slobys/NaiLink/releases/tag/openwrt-v0.6.58)**，提供 IPK / APK v3 安装包。

新增 **mihomo 内核切换**，与 dae、sing-box 共用节点、订阅和配置。按需下载，切换前检查兼容性和实际连接，失败自动尝试恢复原内核。支持 x86_64 / ARM64。[支持范围与限制 →](docs/core-switching.md)

**0.6.58 非 TUN 接管**：sing-box、mihomo 可选 **非 TUN（TPROXY）**，TUN 保留备用。点击顶部“内核”，选择接管方式，检查后切换；失败尝试恢复原内核与原模式。保留节点、订阅、分流和未应用的修改，升级不会自动改换现有模式。[使用方法与限制 →](docs/core-switching.md#接管方式)

保留 0.6.57 的节点 HTTPS DNS 策略、生效出口查看与统一测试口径。

顶部“更新规则”可检查和更新 GeoIP / GeoSite；高级设置保留配置、DNS、路由三列。[规则库用途 →](https://slobys.github.io/NaiLink/maintenance.html#geodata)

## 安装前

- **优先选择 x86_64 / ARM64**，需要 Linux 5.17+、所需 eBPF/JIT 功能、可用 BTF 和现代 LuCI。
- **ARMv7 / MIPS 大端、小端仅实验构建**，已测常见内核无法运行透明代理，暂不推荐安装；其他未列架构暂无安装包。
- 运行程序本体约 **52–57 MiB**，另需依赖、规则库、下载与备份空间，16/32 MiB 闪存设备不宜直接安装。

脚本会自动识别 CPU、固件 ABI 与包格式，并实际检查内核能力；不兼容时停止安装。[查看完整兼容性说明 →](https://slobys.github.io/NaiLink/installation.html)

进程列表中的虚拟内存不等于实际占用。[查看真实内存占用 →](https://slobys.github.io/NaiLink/troubleshooting.html#memory)

## 安装、更新与卸载

以 **root** 登录路由器 SSH，执行下面一行，打开中文菜单：

```sh
uclient-fetch -O /tmp/nailink-install.sh https://github.com/slobys/NaiLink/releases/download/openwrt-v0.6.58/install.sh && sh /tmp/nailink-install.sh --menu --release openwrt-v0.6.58
```

选择 **1 安装 / 更新**。安装后运行 `nailink` 可再次打开菜单，也可选择 **2 卸载**、**3 检查环境** 或 **0 退出**。

更新先备份并保留节点、订阅、群组与配置。**卸载会备份后删除插件和当前数据，可离线执行**；备份保留在 `/root/daed-backups`。[更新与卸载教程 →](https://slobys.github.io/NaiLink/maintenance.html)

**在线维护**：点击顶部版本号检查并在线更新；点击 **更新规则** 打开规则库窗口，再点 **更新** 更新分流数据；运行日志按级别显示不同颜色。旧版本请先用上面的命令或 `nailink` 菜单升级一次，之后即可在面板更新。

**0.6.34 新增内核切换**：点击面板顶部“内核”，可按需安装 sing-box、检查兼容性，再与 dae 切换。保留节点、订阅、配置和未应用的修改，失败尝试自动回退；同时修复 dae 重载后首次连接可能失败的问题。首版 sing-box 支持 x86_64 / ARM64，使用 TUN 和 nftables；不是所有高级参数都能转换。[支持范围与使用方法 →](docs/core-switching.md)

沿用 LuCI 登录授权与细分诊断，更新权限仅向管理员开放。[后台打不开 →](https://slobys.github.io/NaiLink/troubleshooting.html#luci-access)

保留 0.6.30 的代理就绪检查与有效 DNS 分流关联恢复，可用 `nailink --check-network` 查看诊断。[恢复机制与限制 →](https://slobys.github.io/NaiLink/maintenance.html#restart-recovery)

旧版菜单遇到 Raw 下载失败时，可运行 `nailink --install --release openwrt-v0.6.58`。如果 GitHub 也无法解析，先检查路由器自身 DNS。[下载故障排查 →](https://slobys.github.io/NaiLink/troubleshooting.html#download)

**0.6.42 修复 Clash 订阅**：支持内嵌 `proxies` 的 YAML / JSON、SIP008 和普通 / Base64 节点列表；更新失败保留原节点，内容未变时保留节点 ID。仅提取节点，不导入订阅中的 DNS、路由或监听设置。[格式与限制 →](https://slobys.github.io/NaiLink/nodes.html#clash)

**0.6.43 优化节点测试**：三内核普通节点手动 HTTP 测试统一最多 8 秒，减少慢节点误判；增加连接、TLS / WebSocket 握手和入口网关错误提示。自动选路及现有配置保持。[超时排查说明 →](https://slobys.github.io/NaiLink/troubleshooting.html#node-timeout)

**0.6.44 优化 mihomo DNS**：复用有上限的 TCP / 加密 DNS 连接，缓冲短时查询突发；失效连接按原路由重建，停止内核时释放连接与排队请求。同时修复私有节点测试启动时的端口争用恢复。节点、DNS 上游与分流配置保留。[说明 →](https://slobys.github.io/NaiLink/troubleshooting.html#dns-stability)

**0.6.45 内核管理与稳定性**：统一顶部按钮高度，重新设计内核窗口，显示实际常驻内存；释放切换及重载后的旧会话引用，修复旧 DNS 地址下 dae 首次连接可能超时的问题。节点和配置保留。[使用说明 →](docs/core-switching.md)

**0.6.46 优化内核选择**：恢复横向三列卡片，选中项整块高亮并显示勾选，单独标记正在使用的内核。适配窄屏与深浅主题，减少留白；保留原有检查和切换流程。

**0.6.47 紧凑内核选择**：三张卡片收为一行分段按钮，保留实色高亮与勾选，所选内核的说明和状态集中展示；电脑和手机均保留 44px 点击高度，检查、切换与回退流程保持。

**0.6.48 切换状态与统计开销**：切换期间暂停流量轮询、显示切换状态，恢复后自动更新；避免统计接口短暂不可用弹出切换失败提示，真实失败仍明确提示。原生内核管理接口采用有上限的连接复用，退出时取消请求并释放连接；节点和配置保留。

**0.6.49 在线更新下载恢复**：增加有限重试、IPv4 回退与同仓库 GitHub 官方 API 下载入口，大安装包允许更长传输；细分解析、证书、超时和文件缺失等错误。下载失败保留原服务和数据。0.6.38 等旧更新器持续失败时，需要先用备用脚本升级一次。[恢复方法 →](https://slobys.github.io/NaiLink/troubleshooting.html#download)

**0.6.50 更新流程加固**：下载重试共用时限，优先复用已成功的官方入口；APK 依赖齐全时跳过软件源刷新。安装准备过久会在停服前退出，为安装和回退留出时间；面板区分历史失败与当前版本。[说明 →](https://slobys.github.io/NaiLink/maintenance.html#update)

## 开始使用

打开路由器后台 **服务 → NaiLink**：

1. **节点和订阅**：导入节点或添加订阅，标签可留空自动识别。
2. **管理面板**：把节点或订阅加入群组，测试并配置选择策略。
3. **高级设置**：核对接口、DNS 和路由，点击 **应用配置**，再实际验证联网。

[首次设置](https://slobys.github.io/NaiLink/quick-start.html) · [群组与多选](https://slobys.github.io/NaiLink/groups.html) · [DNS 与路由](https://slobys.github.io/NaiLink/network.html) · [故障排查](https://slobys.github.io/NaiLink/troubleshooting.html)

## 源码与交流

[完整源码包](https://github.com/slobys/NaiLink/releases/download/openwrt-v0.6.58/daed-openwrt-source.tar.gz) · [构建说明](openwrt/README.md#开发) · [验证记录](openwrt/VALIDATION.md) · [Logo](docs/site/assets/nailink-logo.png)

编译请使用完整源码包，或 `git clone --recurse-submodules https://github.com/slobys/NaiLink.git`。GitHub 的 Download ZIP 不包含后端子模块。

普通 Linux / Docker 见[安装说明](docs/getting-started.md)，开发与发布见[贡献指南](CONTRIBUTING.md)和[构建说明](docs/build.md)。Docker 不替代 OpenWrt 插件，仍要求宿主机满足内核条件。

[YouTube 频道](https://www.youtube.com/@naiyou) · [Telegram 群组](https://t.me/naiyous) · [反馈问题](https://github.com/slobys/NaiLink/issues)

感谢 [daed](https://github.com/daeuniverse/daed)、[dae-wing](https://github.com/daeuniverse/dae-wing) 和 [dae](https://github.com/daeuniverse/dae)。本项目并非上游官方发行版。前端及安装集成遵循 [MIT](LICENSE)，后端及其修改遵循 [AGPL-3.0](https://github.com/daeuniverse/dae-wing/blob/main/LICENSE)，保留相关版权声明。

Mihomo 为独立可选组件，感谢 [MetaCubeX/mihomo](https://github.com/MetaCubeX/mihomo)。安装包附带原许可证与固定版本来源，发布页同时提供对应源码。
