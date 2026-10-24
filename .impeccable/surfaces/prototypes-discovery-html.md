---
version: 1
slug: "prototypes-discovery-html"
primary_target: "prototypes/discovery.html"
related_targets: []
---

# 发现新音乐原型

- Scope: `prototypes/discovery.html`，静态、可交互的 Web 原型，不接真实服务。
- Visitor mode: Operate。
- Audience/job: 曲库所有者快速判断推荐是否值得获取，明确已有/不感兴趣，并完成一次 JPopsuki 选种。
- Content: 真实发行与封面；个性化理由、拥有状态和下载进度均为明确标注的演示数据。
- Constraints: 继承 032 当前播放器壳层、深浅主题、紫色主操作、封面优先；响应式、键盘可用、减少动态效果支持。

## Direction contract

THESIS: 让“为什么推荐、是否已有、下一步去哪”在一眼内可判断；拒绝把推荐做成下载数量仪表盘，也拒绝混入每日歌单。

OWN-WORLD: 沿用现有中性灰界面、紫色操作色、紧凑专辑网格、圆角封面和播放器侧栏。封面提供主要色彩，状态色只表达可能已有、错误和完成。

STORY: 用户先看到少量高相关发行及具体理由，再筛选或反馈；点击寻找资源后，在不丢失当前上下文的情况下确认 JPopsuki 种子，随后看到下载—刮削—入库状态。

FIRST VIEWPORT: 左侧现有主导航并高亮“发现”，右侧保留当前播放；主区是标题、简短状态、来源筛选与排序，紧接三列发行卡片。卡片直接展示理由、类型和主操作，可能已有与正常候选可明显区分。

FORM: 现有应用内的完整新页面，采用专辑网格加右侧选种抽屉；这是继承现有视觉世界的 surface extension，不运行新概念竞赛。Seed: N/A / incumbent-extension.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance
