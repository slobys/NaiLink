---
version: 1
slug: 'src-pages-orchestrate-index-tsx'
primary_target: 'src/pages/Orchestrate/index.tsx'
related_targets:
  [
    'src/components/GroupResourcePickerModal.tsx',
    'src/pages/Orchestrate/SubscriptionNodes.tsx',
    'src/components/ConnectivityOverview.tsx',
    'src/components/ServiceCheckBar.tsx',
    'src/hooks/useServiceChecks.ts',
    'src/pages/Orchestrate/TrafficOverview.tsx',
    'src/console.css',
    'src/index.css',
  ]
---

# NaiLink 控制台与节点界面

Mode: Operate

## Direction contract

THESIS: 以用户指定的 Zashboard / OpenClash 为基准，把大块留白的节点行改成可以快速扫读的紧凑瓷片；保留 LuCI 导航和 NaiLink 原有任务结构。

OWN-WORLD: 保留既有紧凑控制台世界、主题、字体与 Lucide。中性表面、单层边界和小圆角；节点多选与可确认运行出口使用独立深浅绿色、实体勾选和可访问状态。名称 13–14px，紧凑协议与来源按现有实现为 11–12px，数字等宽。用户授权的节点/订阅选择卡片悬停是窄例外：加深主题色、2px 轻微上浮和向下轻投影，不增加整卡外圈；键盘焦点仍在实际控件上。触控及减少动态效果不依赖上浮。服务商 Logo 使用本地官方资源。

STORY: 打开面板先看网站连通状态，再看完整运行数据与群组。常用 proxy / AI / Netflix 群组和“分流”预设支持分别维护；空 AI / Netflix 明确提示暂用 proxy 节点。打开群组节点/订阅窗口后按持久用途自动检查入口，搜索、筛选、只看通过、勾选通过或暂停，最后由用户明确提交。订阅节点保留原顺序与显式排序，名称、协议、检测结果以及加入/更多操作一眼可见。

FIRST VIEWPORT: 顶部公共栏保持原操作，连接检测为一行四张低矮卡片（手机两列），标识与结果在同一紧凑基线；下面概况与群组保留两区。桌面运行概况按可用视口限制高度，保留全部统计。节点窗口保留搜索、检测工具栏与明确提交区；节点网格沿用 180px 最小列，订阅选择器采用 220px 最小列、10px 间隔的紧凑卡片。窄屏订阅卡自然单列，44×44px 点击标签包裹 18×18px 勾选框，主体滚动。

FORM: 用户明确指定的 Zashboard 参考优先于随机候选；只借用密度、配色层级和选中填色，保留产品控件与所有功能。seed:037e638355ad13af。候选的其他界面拓扑不用于替换 LuCI 导航。

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

## Constraints

保留用户节点、订阅、已应用路由、DNS、内核、接管方式与未应用修改。授权扩展仅为常用分流预设、持久用途和独立入口检查：新安装选择分流；旧安装按钮只补齐，不改当前路由。用途绑定 group ID，改名同步完整路由目标引用；AI / Netflix 空组暂用 proxy 节点。自动检查最多 2 并发、独立缓存 10 分钟，普通延迟结果与测试调度保留；只看通过、勾选通过和检测结束均不自动提交。

ChatGPT 仅为入口检查，不验证账号、登录或其他 AI 服务；Netflix 不声称完整播放解锁、片库或账号权限。手机保留长名称展开、详情、测试、加入和完整操作入口；桌面悬停不是唯一操作方式。保持深浅主题、减少动态效果、键盘操作与错误恢复。

## Shipped evidence and finish

本轮代码来源为 `console.css`、`GroupResourcePickerModal.tsx`、`ServiceCheckBar.tsx`、`useServiceChecks.ts`、`TrafficOverview.tsx` 与对应用途/路由后端。浅色悬停为 primary 17% 混入 card，深色为 primary 24% 混入 node-surface；订阅卡 10×12px 内边距、10px 圆角。

根目录 `.build/service71/browser/` 包含节点明暗 1800 / 390 / 320px、订阅 1800 / 390 / 320px、`subscriptions-focus-320.png`、`subscriptions-touch-390.png`、`dashboard-1600.png`。`browser3.log` 与 `browser/result.json` 记录预设、用途自动检查、通过筛选/选择、暂停、资源保留及完整概况验证。使用原生 LuCI / rpcd API-only 夹具和受控服务响应；不等同于真实服务解锁、真实手机或路由器部署验证。

完成审查先全量检查，再只复查原发现的手机勾选框拉长问题：外层触控 label 已固定 44×44px、内部 checkbox 固定 18×18px。最终原项 resolved，remaining clear，disposition ship；最终评分仅针对这项修复，不宣称重新审查了全部表面。
