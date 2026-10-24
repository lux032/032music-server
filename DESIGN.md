---
name: 032 Music Server
description: 自托管、以专辑为中心的音乐库播放器与管理界面
colors:
  bg: "#fafafa"
  bg-soft: "#f4f4f5"
  bg-panel: "#ffffff"
  surface: "#ffffff"
  surface-hover: "#f0f0f2"
  surface-sunken: "#ededf0"
  fill: "#efeff2"
  fill-hover: "#e4e4e9"
  line: "#e6e6ea"
  line-strong: "#d4d4da"
  text: "#111113"
  text-secondary: "#4f4f59"
  muted: "#64646e"
  text-on-accent: "#ffffff"
  accent: "#5b47e0"
  accent-hover: "#4a37c9"
  accent-active: "#3d2db0"
  accent-soft: "rgba(91, 71, 224, 0.09)"
  accent-soft-strong: "rgba(91, 71, 224, 0.16)"
  accent-line: "rgba(91, 71, 224, 0.35)"
  accent-strong-text: "#4a37c9"
  danger: "#c42b44"
  danger-soft: "rgba(196, 43, 68, 0.08)"
  success: "#0e7651"
  warning: "#9a5b00"
  dark-bg: "#111113"
  dark-bg-soft: "#161619"
  dark-bg-panel: "#18181b"
  dark-surface: "#19191c"
  dark-surface-hover: "#242428"
  dark-surface-sunken: "#222226"
  dark-fill: "#232327"
  dark-fill-hover: "#2e2e33"
  dark-line: "#2a2a2f"
  dark-line-strong: "#3a3a41"
  dark-text: "#f4f4f6"
  dark-text-secondary: "#b8b8c2"
  dark-muted: "#9a9aa5"
  dark-text-on-accent: "#140f33"
  dark-accent: "#8d7dff"
  dark-accent-hover: "#a194ff"
  dark-accent-active: "#b4aaff"
  dark-accent-soft: "rgba(141, 125, 255, 0.14)"
  dark-accent-soft-strong: "rgba(141, 125, 255, 0.24)"
  dark-accent-line: "rgba(141, 125, 255, 0.5)"
  dark-accent-strong-text: "#b4aaff"
  dark-danger: "#ff6f83"
  dark-danger-soft: "rgba(255, 111, 131, 0.12)"
  dark-success: "#45d39a"
  dark-warning: "#f2b84b"
typography:
  display:
    fontFamily: "Inter, -apple-system, BlinkMacSystemFont, PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans CJK SC, Segoe UI, sans-serif"
    fontSize: "clamp(2.2rem, 3.6vw, 3.3rem)"
    fontWeight: 800
    lineHeight: 1.05
    letterSpacing: "-0.03em"
  title:
    fontFamily: "Inter, -apple-system, BlinkMacSystemFont, PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans CJK SC, Segoe UI, sans-serif"
    fontSize: "clamp(1.9rem, 2.8vw, 2.4rem)"
    fontWeight: 800
    lineHeight: 1.1
    letterSpacing: "-0.025em"
  headline:
    fontFamily: "Inter, -apple-system, BlinkMacSystemFont, PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans CJK SC, Segoe UI, sans-serif"
    fontSize: "1.15rem"
    fontWeight: 700
    letterSpacing: "-0.01em"
  body-lg:
    fontFamily: "Inter, -apple-system, BlinkMacSystemFont, PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans CJK SC, Segoe UI, sans-serif"
    fontSize: "1.05rem"
    fontWeight: 400
    lineHeight: 1.65
  body:
    fontFamily: "Inter, -apple-system, BlinkMacSystemFont, PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans CJK SC, Segoe UI, sans-serif"
    fontSize: "0.92rem"
    fontWeight: 400
    lineHeight: 1.55
  body-sm:
    fontFamily: "Inter, -apple-system, BlinkMacSystemFont, PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans CJK SC, Segoe UI, sans-serif"
    fontSize: "0.84rem"
    fontWeight: 400
    lineHeight: 1.55
  label:
    fontFamily: "Inter, -apple-system, BlinkMacSystemFont, PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans CJK SC, Segoe UI, sans-serif"
    fontSize: "0.74rem"
    fontWeight: 500
    lineHeight: 1.5
  kicker:
    fontFamily: "Inter, -apple-system, BlinkMacSystemFont, PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans CJK SC, Segoe UI, sans-serif"
    fontSize: "0.68rem"
    fontWeight: 700
    letterSpacing: "0.12em"
rounded:
  sm: "6px"
  row: "8px"
  md: "10px"
  lg: "14px"
  xl: "20px"
  cover: "6px"
  pill: "999px"
spacing:
  content-gutter: "clamp(22px, 3.4vw, 48px)"
  main-top: "34px"
  sidebar-width: "232px"
  now-playing-width: "340px"
  player-height: "80px"
  album-grid-row-gap: "30px"
  album-grid-column-gap: "22px"
components:
  button-primary:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.text-on-accent}"
    typography: "{typography.body-sm}"
    rounded: "{rounded.pill}"
    padding: "0.62rem 1rem"
  button-primary-hover:
    backgroundColor: "{colors.accent-hover}"
  button-secondary:
    backgroundColor: "{colors.fill}"
    textColor: "{colors.text}"
    typography: "{typography.body-sm}"
    rounded: "{rounded.pill}"
    padding: "0.62rem 1rem"
  button-secondary-hover:
    backgroundColor: "{colors.fill-hover}"
  input-field:
    backgroundColor: "{colors.fill}"
    textColor: "{colors.text}"
    rounded: "{rounded.md}"
    padding: "0.58rem 0.72rem"
  input-field-focus:
    backgroundColor: "{colors.surface}"
  search-field:
    backgroundColor: "{colors.fill}"
    textColor: "{colors.text}"
    rounded: "{rounded.md}"
    padding: "0 12px"
    height: "40px"
  search-field-focus:
    backgroundColor: "{colors.surface}"
  filter-chip:
    backgroundColor: "{colors.fill}"
    textColor: "{colors.text-secondary}"
    rounded: "{rounded.pill}"
    padding: "0 12px 0 14px"
    height: "34px"
  filter-chip-hover:
    backgroundColor: "{colors.fill-hover}"
    textColor: "{colors.text}"
  filter-chip-selected:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.text-on-accent}"
  nav-item:
    textColor: "{colors.text-secondary}"
    typography: "{typography.body}"
    rounded: "{rounded.row}"
    padding: "8px 12px"
    height: "38px"
  nav-item-active:
    backgroundColor: "{colors.fill}"
    textColor: "{colors.text}"
  album-cover:
    backgroundColor: "{colors.surface-sunken}"
    rounded: "{rounded.cover}"
  play-disc:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.text-on-accent}"
    rounded: "{rounded.pill}"
    size: "42px"
  player-play-main:
    backgroundColor: "{colors.text}"
    textColor: "{colors.bg}"
    rounded: "{rounded.pill}"
    size: "44px"
  card-surface:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.lg}"
---

# Design System: 032 Music Server

## Overview

**Creative North Star: "The Library-Player Standard, Played Straight"**

032 Music Server 遵循音乐库播放器的品类标准，并且照直执行：中性灰承载界面，封面与标题承载页面，唯一的紫色强调色只标记主操作、播放与选中。这一方向记录于 `tokens.css`（2026-10-06），是本系统的事实性起点，而不是新的品牌叙事。

界面是一个固定的播放器壳层：左侧导航、中央内容、右侧当前播放与队列、底部播放栏。壳层退为同一块中性底色，页面的色彩几乎全部来自专辑封面。控件以填充灰为主，列表行无分隔线、悬停显示圆角底色，管理卡片是安静的平面。深浅两套主题共享同一组令牌名，浅色为无脚本默认，`theme.js` 在首帧前写入 `data-theme`。

新界面（例如“发现新音乐”）是现有壳层内的页面扩展：沿用同一令牌、同一导航与播放器壳层、同一专辑卡片语言，不另起视觉世界。

**Key Characteristics:**
- 中性灰界面，封面提供色彩
- 单一紫色强调色，仅用于主操作、播放和选中
- 深浅双主题，令牌一一成对
- Inter 负责拉丁字母与数字，平台 CJK 无衬线负责中日文
- 固定三栏壳层加底部播放栏，≤900px 转为底部导航
- 填充式控件、胶囊按钮、6px 柔角封面

## Colors

中性灰阶加一支紫色，语义色只表达危险、成功与警告。

### Primary
- **Library Violet**（浅色 #5b47e0 / 深色 #8d7dff）：主按钮、播放盘、进度填充、选中筛选、当前队列行、焦点环。悬停加深为 #4a37c9（深色提亮为 #a194ff），按下为 #3d2db0 / #b4aaff。
- **Violet Wash**（accent-soft，9% / 14% 透明紫）：当前播放行、菜单项悬停、提示条等弱选中底色；更强一级为 accent-soft-strong。
- **Violet Ink**（accent-strong-text，#4a37c9 / #b4aaff）：浅底上需要满足对比度的紫色文字链接，如“清空”“清除筛选”。

### Neutral
- **Page Ground**（bg，#fafafa / #111113）：页面与侧栏底色。
- **Soft Ground**（bg-soft，#f4f4f5 / #161619）：全屏播放等次级底色。
- **Panel Ground**（bg-panel，#ffffff / #18181b）：当前播放面板与底部播放栏。
- **Surface**（surface，#ffffff / #19191c）：卡片、浮层、菜单、聚焦后的输入框。
- **Sunken Surface**（surface-sunken，#ededf0 / #222226）：封面加载前的底色、进度轨道。
- **Control Fill**（fill，#efeff2 / #232327；悬停 fill-hover #e4e4e9 / #2e2e33）：搜索框、筛选胶囊、次要按钮、列表行与导航项的悬停底色。
- **Hairline**（line，#e6e6ea / #2a2a2f；加重 line-strong #d4d4da / #3a3a41）：卡片描边、分隔与浮层边框。
- **Ink**（text，#111113 / #f4f4f6）：正文与标题；同时作为播放栏主播放键的底色。
- **Secondary Ink**（text-secondary，#4f4f59 / #b8b8c2）：艺人名、导航项、次要说明。
- **Muted Ink**（muted，#64646e / #9a9aa5）：元数据、时间、占位符。所有文字色在 bg、surface、surface-hover 上 ≥4.5:1。

### Semantic
- **Danger**（#c42b44 / #ff6f83，配 danger-soft 底）：删除、失败状态、危险确认区。
- **Success**（#0e7651 / #45d39a）与 **Warning**（#9a5b00 / #f2b84b）：状态徽章与提示，以 9% 混色作底、40% 混色作边。

### Named Rules
**The One Violet Rule.** 紫色只用于主操作、播放与选中；其余控件（筛选、次要按钮、行内工具）一律中性填充。

**The Paired Theme Rule.** 每个颜色令牌在浅色与深色中成对定义并同名；浅色是无脚本默认。

## Typography

**Display Font:** Inter（拉丁与数字），回退 -apple-system、BlinkMacSystemFont
**Body Font:** 同一栈；中日文由 PingFang SC、Hiragino Sans GB、Microsoft YaHei、Noto Sans CJK SC 承担
**Label/Mono Font:** 无独立等宽字体；时间与计数使用 `font-variant-numeric: tabular-nums`

**Character:** 单一无衬线家族，靠字重与紧字距建立层级；Inter 自托管（400–800 可变字重，latin 与 latin-ext 子集），CJK 交给平台字体。

### Hierarchy
- **Display**（800，clamp(2.2rem, 3.6vw, 3.3rem)，1.05，-0.03em）：专辑详情等英雄标题、首次运行引导。
- **Title**（800，clamp(1.9rem, 2.8vw, 2.4rem)，1.1，-0.025em）：页面 h1，如“主页”“专辑”。
- **Headline**（700，1.15rem，-0.01em）：页内分节标题，如“最近添加”“接着听”。
- **Body Large**（400，1.05rem，1.65）：引导段落、英雄副标题。
- **Body**（400，0.92rem，1.55）：界面默认字号，导航项与正文。
- **Body Small**（0.84rem）：按钮文字、艺人名、计数与表单标签。
- **Label**（0.74rem）：元数据、时长、徽章。
- **Kicker**（700，0.68rem，0.12em，大写）：小型分组标签。

### Named Rules
**The Inter-plus-CJK Rule.** 只使用 `--font-ui` 一个字体栈；不要为中日文单独引入网络字体。

## Layout

固定壳层：左侧导航栏 232px 固定，右侧当前播放面板 340px 固定且可收起（收起后内容右边距归零），底部播放栏 80px 横跨全宽。主内容区顶部内边距 34px，左右使用 clamp(22px, 3.4vw, 48px) 的流动边距，底部为播放栏留出高度加 40px。

专辑网格默认 4 列（可由网格尺寸滑块调整），≤1200px 默认 3 列，≤900px 固定 3 列；行距 30px、列距 22px（移动端 20px / 12px）。页头标题与计数共用基线，搜索与工具在右侧（桌面搜索宽 min(380px, 44%)）。

≤900px：侧栏转为播放栏上方 60px 的底部导航，只保留主要入口，其余进入“更多”浮层；当前播放面板变为全屏抽屉；主内容内边距变为 22px 18px。≤480px 进一步隐藏播放栏次要控件。

## Elevation & Depth

混合策略：界面以色调分层为主（底色 → 面板 → 表面 → 填充），阴影只给封面、浮层和悬停提升。管理类卡片明确无阴影。深色主题使用同名但更深的黑色阴影。

### Shadow Vocabulary
- **Hairline lift**（`box-shadow: 0 1px 2px rgba(17, 17, 19, 0.06)`）：极轻的徽章与小元素。
- **Cover rest**（`box-shadow: 0 2px 6px rgba(17, 17, 19, 0.1)`）：静止的专辑封面。
- **Cover hover**（`box-shadow: 0 16px 30px -12px rgba(17, 17, 19, 0.38)`）：悬停或聚焦的封面。
- **Hero cover**（`box-shadow: 0 28px 50px -22px rgba(17, 17, 19, 0.55)`）：详情页与当前播放面板的大封面。
- **Popover**（`box-shadow: 0 14px 40px rgba(17, 17, 19, 0.16)`）：菜单、筛选浮层、提示、歌词面板。

### Named Rules
**The Quiet Card Rule.** 管理卡片是 1px 细线描边的平面表面，不加阴影；阴影只属于封面、浮层和悬停提升。

## Shapes

圆角分级明确：封面 6px 柔角；导航项、列表行与菜单项 8px；输入框与搜索 10px；卡片与浮层 14px；歌词等大浮层 20px；按钮、筛选、徽章为胶囊（999px）；播放盘与主播放键为正圆。列表行之间无分隔线，以圆角悬停底色区分。

### Named Rules
**The Cover Corner Rule.** 专辑封面始终使用 6px 的 cover 圆角与正方形比例；不要把封面做成大圆角卡片。

## Components

### Buttons
- **Shape:** 胶囊（999px）。
- **Primary:** 紫色底、on-accent 文字，0.62rem 1rem 内边距，0.84rem / 600。
- **Hover / Focus:** 悬停转 accent-hover；按下下移 1px；焦点为 2px 实心紫色外框、2px 偏移。
- **Secondary:** 填充灰底、正文色文字、无描边，悬停转 fill-hover。
- **Danger:** 危险红底，悬停与正文色混合加深。
- **Icon buttons:** 透明底、muted 图标，悬停为 surface-hover 或 fill 底。

### Chips
- **Style:** 筛选胶囊高 34px，填充灰底、次要文字、500 字重、无描边。
- **State:** 悬停或展开时转 fill-hover 与正文色；已应用的筛选变为实心紫色。

### Cards / Containers
- **Corner Style:** 14px。
- **Background:** surface。
- **Shadow Strategy:** 无（见 Elevation & Depth）。
- **Border:** 1px line；危险区仅为带红色调的描边。
- **Popovers:** surface 底、1px line、10–12px 圆角、Popover 阴影，菜单项 8px 圆角、悬停为 accent-soft。

### Inputs / Fields
- **Style:** 填充灰底、1px 透明描边、10px 圆角，0.58rem 0.72rem 内边距；悬停转 fill-hover。
- **Focus:** 底色变 surface、描边变紫色、外加 3px accent-soft 光晕。
- **Search:** 40px 高的填充式搜索框，左侧 17px muted 放大镜图标；聚焦时整框描边转紫、底色转 surface。

### Navigation
- **Sidebar:** 与页面同底色；顶部为图标加“032 / MUSIC”字标。导航项 38px 高、8px 圆角、8px 12px 内边距、次要文字色、18px 图标。
- **States:** 悬停为 fill 底；当前项为 fill 底、正文色、700 字重，仅图标变紫色。
- **Mobile:** ≤900px 变为底部导航，图标在上、0.66rem 标签在下，次要入口进入“更多”浮层（14px 圆角、Popover 阴影）。

### Album Card
封面优先的核心组件。正方形封面、6px 圆角、Cover rest 阴影，下方依次为标题（0.9rem / 600 / 单行省略）、艺人（0.84rem / 次要色）、元数据（0.74rem / muted，以“·”分隔年份、类型、曲目数）。悬停或键盘聚焦时封面放大到 1.02 并换为 Cover hover 阴影，右下角浮出 42px 紫色播放盘；收藏与选择按钮同样在悬停时出现。选中状态为 3px 紫色外环。减少动态效果时取消缩放与过渡。

### Player Bar
80px 高、panel 底、顶部 1px 细线。主播放键为 44px 正圆、墨色底（text）、底色文字（bg），悬停放大到 1.06；其余传输键透明、次要色。进度条 5px 胶囊轨道、紫色填充，悬停加粗并显示拇指。

### Named Rules
**The Ink Transport Rule.** 播放栏主播放键用墨色而非紫色；紫色播放盘保留给封面上的播放入口。

**The Filled Control Rule.** 搜索、筛选、次要按钮与行悬停共用 fill / fill-hover，不加描边。

## Do's and Don'ts

### Do:
- **Do** 在应用内直接使用 `tokens.css` 的变量，并同时验证浅色与深色主题。
- **Do** 把紫色限制在主操作、播放、进度与当前选中上。
- **Do** 用 2px 实心紫色外框、2px 偏移作为全局焦点指示。
- **Do** 保持封面正方形、6px 圆角，悬停只做 1.02 缩放与阴影加深。
- **Do** 让列表行无分隔线、以 8–10px 圆角的 fill 底色表示悬停。
- **Do** 遵守 `prefers-reduced-motion`：取消缩放、页面入场与均衡器动画。

### Don't:
- **Don't** 把紫色用于次要按钮、筛选触发器或行内工具。
- **Don't** 给管理卡片加阴影。
- **Don't** 用半透明 accent-line 作为焦点环（在白底上仅 1.6:1，低于 3:1）。
- **Don't** 为中日文引入额外字体或在 `--font-ui` 之外另建字体栈。
