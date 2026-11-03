---
name: NaiLink Compact Console
description: 保留主题与 LuCI 导航的紧凑代理管理控制台
colors:
  primary: 'oklch(0.75 0.18 85)'
  primary-foreground: 'oklch(0.18 0.02 60)'
  background: 'oklch(0.99 0.008 85)'
  foreground: 'oklch(0.18 0.02 60)'
  card: 'oklch(0.99 0.008 85)'
  muted: 'oklch(0.95 0.012 85)'
  muted-foreground: 'oklch(0.5 0.02 60)'
  secondary: 'oklch(0.96 0.015 85)'
  accent: 'oklch(0.94 0.02 85)'
  border: 'oklch(0.92 0.015 85)'
  input: 'oklch(0.92 0.015 85)'
  ring: 'oklch(0.75 0.18 85)'
  destructive: 'oklch(0.577 0.245 27.325)'
  dark-primary: 'oklch(0.82 0.165 85)'
  dark-primary-foreground: 'oklch(0.16 0.015 60)'
  dark-background: 'oklch(0.16 0.015 60)'
  dark-foreground: 'oklch(0.95 0.015 85)'
  dark-card: 'oklch(0.19 0.015 60)'
  dark-muted: 'oklch(0.24 0.015 60)'
  dark-muted-foreground: 'oklch(0.65 0.02 70)'
  dark-border: 'oklch(0.28 0.015 60)'
  dark-input: 'oklch(0.3 0.015 60)'
typography:
  title:
    fontFamily: "'Fira Sans', system-ui, -apple-system, sans-serif"
    fontSize: '18px'
    fontWeight: 600
    lineHeight: '26px'
  body:
    fontFamily: "'Fira Sans', system-ui, -apple-system, sans-serif"
    fontSize: '14px'
    fontWeight: 400
    lineHeight: '20px'
  node-name:
    fontFamily: "'Fira Sans', system-ui, -apple-system, sans-serif"
    fontSize: '13px'
    fontWeight: 500
  label:
    fontFamily: "'Fira Sans', system-ui, -apple-system, sans-serif"
    fontSize: '12px'
    lineHeight: '16px'
  node-meta:
    fontFamily: "'Fira Sans', system-ui, -apple-system, sans-serif"
    fontSize: '11px'
    lineHeight: '16px'
  connection-result:
    fontFamily: "'Fira Sans', system-ui, -apple-system, sans-serif"
    fontSize: '15px'
    fontWeight: 600
    lineHeight: '20px'
  mono:
    fontFamily: "'Source Code Pro', ui-monospace, monospace"
rounded:
  checkbox: '5px'
  control: '8px'
  tile: '10px'
  panel: '12px'
  dialog: '16px'
spacing:
  micro: '4px'
  tight: '6px'
  compact: '8px'
  tile-gap: '10px'
  content: '12px'
  panel: '16px'
  section: '24px'
components:
  button-primary:
    backgroundColor: '{colors.primary}'
    textColor: '{colors.primary-foreground}'
    rounded: '{rounded.control}'
    typography: '{typography.body}'
    height: '36px'
    padding: '8px 16px'
  button-outline:
    backgroundColor: '{colors.background}'
    textColor: '{colors.foreground}'
    rounded: '{rounded.control}'
    height: '36px'
    padding: '8px 16px'
  button-ghost:
    backgroundColor: 'transparent'
    textColor: '{colors.foreground}'
    rounded: '{rounded.control}'
    height: '36px'
    padding: '8px 16px'
  input-search:
    backgroundColor: 'transparent'
    textColor: '{colors.foreground}'
    rounded: '{rounded.control}'
    height: '36px'
    padding: '4px 12px'
  count-chip:
    backgroundColor: '{colors.muted}'
    textColor: '{colors.muted-foreground}'
    rounded: '999px'
    padding: '2px 8px'
    typography: '{typography.label}'
  console-panel:
    backgroundColor: '{colors.card}'
    textColor: '{colors.foreground}'
    rounded: '{rounded.panel}'
    padding: '{spacing.panel}'
  node-tile:
    backgroundColor: 'var(--console-node-surface)'
    textColor: '{colors.foreground}'
    rounded: '{rounded.tile}'
    padding: '8px 10px'
    typography: '{typography.node-name}'
  node-tile-selected:
    backgroundColor: 'var(--console-selected)'
    textColor: 'var(--console-selected-text)'
    rounded: '{rounded.tile}'
    padding: '8px 10px'
  connection-card:
    backgroundColor: '{colors.card}'
    textColor: '{colors.foreground}'
    rounded: '{rounded.tile}'
    padding: '8px 12px'
  community-link:
    backgroundColor: '{colors.card}'
    textColor: '{colors.foreground}'
    rounded: '{rounded.control}'
    height: '36px'
    padding: '0 10px'
---

# Design System: NaiLink Compact Console

## Overview

**Creative North Star: "紧凑控制台"**

系统沿用 NaiLink 的主题、字体和 LuCI 导航，以用户明确指定的 Zashboard / OpenClash 密度与选中反馈作为细节参考。名称、来源、协议和测试结果优先于留白；选中通过整卡填色、边界和实体勾选共同表达。

本文件描述已实现的控制台、添加节点窗口与订阅节点浏览器。它不替换宿主 LuCI 的导航，也不把控制台的紧凑尺寸强加给配置编辑器等其他页面。依据为 `src/console.css`、`src/index.css`、`src/constants/themes.ts`、对应 React 组件和 `.impeccable/review/` 的实现截图；没有已批准的独立视觉稿。

**Key Characteristics:**

- 主题变量决定表面和强调色，状态同时提供文字或勾选。
- 两行节点瓷片与固定工具栏支持快速扫读。
- 桌面密度与触控目标分别设置，操作入口始终可发现。
- 官方品牌文件随前端打包，连接检测不依赖远程图片。

## Colors

上方色值是现有默认 Amber 主题的浅色基线及深色对应值；保留项目的 OKLCH 格式。它们描述默认主题，不是覆盖其他主题的固定配色。

### Primary

暖金色 `primary` 用于主操作与焦点；节点多选使用独立绿色状态色，其他勾选沿用主题；前景由 `primary-foreground` 决定。深色模式对应 `dark-primary` 与 `dark-primary-foreground`。错误采用独立的 `destructive` 语义色。

`App.tsx` 通过 `themeColorsToCSSVars()` 将所选主题写入根元素的 `--primary`、`--background`、`--foreground`、`--card`、`--muted`、`--border`、`--input`、`--ring` 等变量；Tailwind 的 `--color-*` 别名再引用它们。Amber、Ocean、Rose、Emerald、Violet、Slate、Sunset、Teal、Sky、Indigo、Coffee、Olive 及深浅/系统模式均沿用现有定义。

### Neutral

浅色为暖白表面与深墨文字，深色为炭色表面与暖白文字。`muted`、`secondary` 和 `accent` 是低强度表面层级；`border` 为一像素边界，`input` 为字段边界。不要把浅色前景或背景硬编码进主题组件。

次要文字仍为 `color-mix(in oklab, var(--foreground) 76%, var(--card))`。普通节点表面改为前景混合 9%（深色 12%），边界混合 19%（深色 25%）；悬停表面混合 20%（深色 24%），加深节点与画布的区分。绿色多选语义独立于主题：浅色填充 `oklch(0.81 0.09 150)`、文字 `oklch(0.26 0.045 155)`、边界 `oklch(0.48 0.11 150)`；深色填充 `oklch(0.36 0.075 155)`、文字 `oklch(0.96 0.015 150)`、边界 `oklch(0.69 0.1 155)`。悬停保持绿色，实心勾选保留。具体控制色与悬停值以 `console.css` 为准。

连接检测沿用成功、受限、失败的绿、琥珀、红语义与原有明暗变体；历史短条与文字结果配对，不能用品牌颜色替代检测状态。

**The Theme Binding Rule.** 普通表面、悬停与焦点继续绑定主题；节点多选使用与主题分开的深浅绿色语义，必须同时有勾选与可访问状态。地区旗帜保留标准图形颜色，不映射为主题色。

## Typography

**Body Font:** 本地打包的 Fira Sans，回退 `system-ui, -apple-system, sans-serif`。中文字符使用可用的系统回退字形。没有新增展示字体或远程字体请求。

**Label/Mono Font:** Source Code Pro 仅沿用技术内容的等宽用途；测试耗时等动态数字使用 `font-variant-numeric: tabular-nums`，不必切换字体。

### Hierarchy

- **Title:** 窗口标题使用 `title`；一般区域标题保留现有较小的半粗体。
- **Body:** 普通界面文案使用 `body`，按钮为中等字重。
- **Node name:** 节点主名称使用 `node-name`，选中名称提升到半粗体；订阅名称行高为 20px。
- **Label:** 检测目标名称及常规次级文案使用 `label`。
- **Node meta:** 实现中的协议、来源与紧凑测试徽标使用 `node-meta`；窄屏少数协议标签为 10px，不将它扩展为通用正文尺寸。
- **Connection result:** 桌面当前结果使用 `connection-result`；小于 560px 时收为 12px / 14px 行高。触控搜索输入使用 16px。

**The Name Access Rule.** 截断只控制布局，不取消完整名称：保留 `title`、名称按钮、展开或详情入口，以及完整可访问名称。

## Layout

连接检测是低矮横条：默认两列，900px 起四列，间隔 8px。桌面卡片的两行内容为 16px 与 12px，行间隔 3px，加上上下内边距和边界后约 49px 高；手机缩小 Logo 与内边距。当前结果与短历史同卡显示。

节点窗口宽度为 `calc(100% - 40px)`，最大 1440px；最大高度为 `min(860px, calc(100dvh - 40px))`。添加窗口保留固定标题、搜索筛选区与提交区，只有主体滚动。小于 560px 时窗口铺满可用视口，取消外层圆角和边界。

添加节点和订阅节点网格采用 `repeat(auto-fill, minmax(min(100%, 180px), 1fr))`，常规间隔 10px；小于 560px 为两列、8px 间隔；小于 330px 的最窄嵌入视口退为单列。列数由可用宽度决定，不改变节点排序。

添加瓷片桌面最小高度为 70px，名称行 24px、操作行 26px，内边距与间隔见前端组件 token。≤640px 或粗指针时，两行均使用 44px 目标，长名称可显示两行并展开。订阅节点同样保留名称、测试、加入与更多入口；触控时操作目标为 44px。

工具栏在 1200px 以下允许操作区换行；桌面公共按钮和社区链接高 36px，≤640px 或粗指针时为 44px。横屏高度≤500px 时添加窗口铺满高度，工具栏可在 120px 范围内滚动，筛选和批量操作仍可到达。

**虚拟行约束：** 手动节点列表是另一种虚拟化布局：桌面卡片高 92px 加 10px 尾间隔，步距为 102px；触控卡片高 112px 加相同间隔，步距为 122px。`Node.tsx` 从 `--node-row-height` 读取步距并计算绝对位置、窗口和总高度，`utils/node-window.ts` 默认 102px。不得把 70px 瓷片高度应用到这里；若更改行高，必须同步步距与窗口/拖动计算并验证滚动。

## Elevation & Depth

连接卡片、公共按钮和普通控制台区块使用边界与主题色层级区分。节点卡片是用户指定的窄例外：精细指针悬停时以柔和主题色、2px 上浮和轻量向下投影反馈，不加整卡外圈；触控、拖动和减少动画模式不叠加上浮。`console.css` 在 `index.css` 之后加载。

弹窗继续使用 Radix 包装组件的原有遮罩、叠放与阴影。运行概况容器存在既有高优先级规则：保留 20px 圆角和 20px 内边距，≤479px 时内边距为 14px；不能将普通区块的 12px / 16px 覆盖说明误用于它。全局样式中的旧渐变、发光及悬浮工具类不是本次紧凑控制台的扩展规则。

## Shapes

普通控制台区块采用柔和小圆角；连接卡片、节点瓷片与订阅来源卡使用 `tile`，按钮使用 `control`，桌面节点窗口使用 `dialog`。边界通常为 1px。添加节点勾选框为 18px、触控时为 22px，采用 `checkbox` 圆角；勾选框内部尺寸不代表整项的点击范围。

## Components

### Buttons

主按钮实色填充；边框按钮保留表面与边界；幽灵按钮以悬停背景反馈。默认库按钮高 36px，控制台样式取消阴影并使用 `control` 圆角。紧凑瓷片中的次级按钮可为 26px；触控规则将名称、测试、展开、加入和更多入口增至 44px。禁用继续使用原有状态与交互逻辑。

### Inputs / Fields

搜索沿用现有 Input 与带图标字段：透明表面、一像素输入边界、主题前景与占位文字。桌面高 36px，触控高至少 44px。库字段保留主题焦点环；节点窗口对按钮、勾选、输入与选择框额外使用 2px 实线焦点外框及 2px 外偏移。订阅搜索的焦点由外层标签承载。

### Chips

已选计数是柔和的圆胶囊，用 `muted` 表面和次要文字。延迟徽标保留实际状态、测量来源与原有解释；缩短排版不改变数值和单位含义。

### Cards / Containers

普通区块使用 `console-panel`。添加节点瓷片左侧为名称和协议/来源，右侧为勾选及测试/名称展开。选中使用 `node-tile-selected` 的整卡绿色填充、绿色边界、对应深浅文字与实心勾选；未选悬停使用柔和的主题色表面，不叠加额外边框或外圈。订阅浏览保留悬停/详情，当前出口仅用真实运行状态标记，不把浏览或加入群组当作选路。焦点以实际按钮为目标，不能仅用整卡 `focus-within` 替代。

订阅瓷片采用同一表面与圆角，名称按钮打开详情；测试、加入和更多始终可见。名称或测试结果较长时允许其容器收缩，独立操作按钮不能被挤出或隐藏。

### Navigation

LuCI 继续拥有页面导航。NaiLink 公共栏中的社区链接保持 YouTube 与 Telegram 的原地址及新标签行为；桌面图标配文字，小于 560px 时隐藏可见文字但保留可访问名称。链接使用当前主题表面、边界及悬停色，键盘焦点为 2px 外框及 3px 外偏移。

### Connection strip and provider assets

四个目标为阿里云、Google、OpenAI、GitHub；检测名与实际目标保持一致。每张卡片包含品牌标识、短历史与当前检测结果。桌面标识盒为 24px，手机为 20px，以 `object-fit: contain` 保留比例。它们不是导航按钮。

资源由 `src/assets/brands/` 本地打包，来源、获取日期、SHA-256 和唯一 SVG 调整记录在 `sources.json`。Google 使用官方产品 SVG；GitHub 使用官方黑/白 Invertocat 并随界面深浅模式选择。OpenAI 使用官方 favicon SVG，仅补充与原 180×180 视口一致的 `viewBox`，路径不改绘。

阿里云是一个严格限定的原始栅格例外：直接打包官方 CDN 的原始 32×32 ICO，未重新生成或描绘；其来源与哈希同样在 manifest 中。这个例外不允许其他供应商使用随意截图、手绘近似图或未记录来源的栅格文件。

### Node region indicators

`NodeRegionIcon` 使用本地 `flag-icons` 4:3 SVG（42 个常见地区），源码修订、MIT 许可与每文件摘要在 `src/assets/regions/`，二进制静态资源另包含第三方许可。图标为 20×15px，文字仍由 `NodeName` 的独立伸缩容器负责截断与平移，虚拟列表步距不变。订阅来源名称不展示节点旗帜。

只按节点名称中的明确旗帜、中英文地区名和有边界的 HK/JP/SG 等代码识别；不解析服务器域名、不请求 IP 定位、不改名或分流。多个地区且没有唯一明确旗帜、或未知地区显示中性地球；提示说明这是名称标注，不保证实际出口位置。已有前置旗帜仅在可见排版中换成 SVG，存储名称、搜索、可访问按钮名称和完整 title 保留。

### Name motion

完整名称只在读取时测量。支持悬停且未请求减少动态效果时，溢出名称在悬停或键盘焦点后延迟 600ms 往返平移，时长按距离计算并限制在 3–12s；不溢出时不动。触控使用换行、名称展开或详情；展开后停止平移。`prefers-reduced-motion: reduce` 不启动名称平移，并取消全部节点卡片位移及过渡。节点悬停颜色过渡为 160ms，上浮/投影为 180ms `ease-out`；拖动继续由 DND 库控制，虚拟步距不变。

## Do's and Don'ts

### Do:

- **Do** 普通表面与焦点绑定主题，节点多选使用独立绿色语义及实心勾选。
- **Do** 同时保留整卡选中填色、边界、实体勾选及可访问状态。
- **Do** 在触控布局中保留 44px 主要操作目标和完整名称入口。
- **Do** 在最窄嵌入视口使用单列，并检查弹窗工具栏和提交区仍可到达。
- **Do** 使用已记录来源的本地官方 Logo，并保持原比例与颜色。
- **Do** 将虚拟行的可见高度、尾间隔和滚动步距作为同一组计算验证。

### Don't:

- **Don't** 用 70px 网格瓷片规则覆盖手动节点列表的 102px / 122px 虚拟步距。
- **Don't** 通过隐藏测试、加入、更多、筛选或长名称入口来制造紧凑效果。
- **Don't** 让悬停动画成为触控或键盘读取完整名称的唯一方式。
- **Don't** 把默认 Amber 色值硬编码为其他主题的运行时颜色。
- **Don't** 把品牌图片改成自动生成图、字符图标或运行时远程请求。
- **Don't** 将旧全局发光、渐变、悬浮阴影工具类扩散到紧凑节点与连接卡片。

### 节点交互与运行出口

悬停使用柔和主题色和 2px 上浮，不添加整卡轮廓。手动虚拟列表保留 6px 视口留白，卡片尺寸与 102px / 122px 步距不变。键盘焦点仍显示在实际控件上，订阅搜索只显示一层字段焦点。当前运行出口使用与勾选相同的绿色，并显示独立实体勾选；添加窗口的复选框仍只表示本次待添加选择。运行状态按已应用 URI 与唯一节点 ID 匹配，来自原生内核实际选择或 dae 唯一候选；停用、切换、读取失败、已编辑/删除或歧义节点不冒充运行出口。各节点表面共享一个前台刷新查询，后台页面暂停，不触发测试或配置写入。

添加节点的来源、排序、只看已选并排，窄屏换行。延迟优先只使用成功的有限非负测量值，失败/未测试放末尾，同值保留原序，切换排序不清空选择或写入持久排序。
