# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

OpenWrt、ImmortalWrt、iStoreOS 软路由的管理员，通过电脑和手机浏览器管理家庭或小型网络代理。项目由用户 fork 后独立维护并分享给其他使用者。

## Product Purpose

NaiLink（奶油互联）管理节点、订阅、群组、DNS、路由，显示运行数据和日志，提供升级与规则库维护。

## Operating Context

复用 LuCI 登录。管理员导入节点、加入群组、保存后应用配置，并查看实际生效出口。支持 dae、sing-box、mihomo 及既有接管方式；切换和维护已有备份、兼容性检查与失败回退。

## Capabilities and Constraints

既有 React / TanStack Query / Tailwind / Radix 前端与 Go 后端。此轮只调整视觉和交互呈现；保留搜索、来源筛选、多选、排序、测试及暂停续测、长名称查看、节点详情和加入群组等功能。保留节点、订阅、分流、未应用修改和内核选择。连接检测已由百度改为阿里云。节点地区图标仅按名称提示在本地识别，不验证出口 IP；多选采用独立绿色语义，普通节点底色加深。不得新增大型依赖或远程字体、运行时 Logo 请求。

## Brand Commitments

保留 NaiLink 名称、频道链接、现有主题选择及深浅模式。用户明确选择 Zashboard / OpenClash 控制面板为视觉参考：紧凑节点卡片、明显选中反馈、清楚的信息层级。连接检测使用阿里云、Google、OpenAI、GitHub 的真实品牌资源，保持原始比例与颜色。

## Evidence on Hand

README.md、当前运行插件的五张用户截图、现有前端组件、0.6.66 浏览器与实机验证记录。示例节点与浏览器测试数据是隔离夹具，不得宣传为用户的真实连接状态。

## Product Principles

- 已应用配置与编辑中的配置边界清楚。
- 用户数据与正在运行的服务优先保留。
- 紧凑呈现仍须可触控、可键盘操作、可查看长名称。
- 连接状态、节点测试耗时与带宽保持原有含义，不制造数据或能力。
