---
version: 1
slug: 'src-pages-orchestrate-index-tsx'
primary_target: 'src/pages/Orchestrate/index.tsx'
related_targets:
  [
    'src/components/GroupResourcePickerModal.tsx',
    'src/pages/Orchestrate/SubscriptionNodes.tsx',
    'src/components/ConnectivityOverview.tsx',
    'src/index.css',
  ]
---

# NaiLink 控制台与节点界面

Mode: Operate

## Direction contract

THESIS: 以用户指定的 Zashboard / OpenClash 为基准，把大块留白的节点行改成可以快速扫读的紧凑瓷片；保留 LuCI 导航和 NaiLink 原有任务结构。

OWN-WORLD: 中性表面、单层边界与小圆角，无装饰渐变或悬浮阴影。保留现有主题色；通过实体勾选、填色和清楚的焦点线表达选中。名称 13–14px，协议与来源 12px，数字等宽。沿用现有字体与 Lucide；服务商 Logo 使用本地官方资源。

STORY: 打开面板先看网站连通状态，再看运行数据和群组；进入添加窗口后搜索、筛选、多选并提交。订阅节点按原顺序呈现，名称、协议、测试结果以及加入/更多操作一眼可见。

FIRST VIEWPORT: 顶部公共栏保持原操作，连接检测为一行四张低矮卡片（手机两列），标识与结果在同一紧凑基线；下面概况与群组保留两区。节点窗口固定搜索与提交区，主体采用 2–7 列响应式小卡片，不创建假分组或改变排序。

FORM: 用户明确指定的 Zashboard 参考优先于随机候选；只借用密度、配色层级和选中填色，保留产品控件与所有功能。seed:037e638355ad13af。候选的其他界面拓扑不用于替换 LuCI 导航。

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

## Constraints

不改变节点、订阅、路由、DNS、内核、测试调度、多选和排序逻辑。用户指定百度改为阿里云是唯一检测目标变化，必须与实际请求一致。手机保留长名称展开、详情、测试、加入和完整操作入口；桌面悬停不是唯一操作方式。保持深浅主题、减少动态效果、键盘操作与错误恢复。测试截图只使用明确标识的隔离夹具。
