# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

自托管音乐库的所有者和管理员。当前产品采用单用户模型；用户以专辑为中心浏览、管理和播放本地音乐，曲库以日本 ACG 内容为主。

## Product Purpose

032 Music Server 提供本地音乐库、串流、元数据增强和个性化推荐。正在设计的“发现新音乐”能力用于发现用户可能喜欢但尚未拥有的发行，并在用户控制下完成资源获取、刮削和入库，让曲库按真实兴趣增长。

## Positioning

推荐发现与资源获取分离：推荐系统先根据已知艺人、作品和真实反馈判断“什么值得听”，JPopsuki 只负责回答“能否获取”。MusicBrainz Release Group 是内容拥有的默认身份，MusicAutoTagger 是新内容进入曲库前的标准刮削流程。

## Operating Context

现有 Go Web 服务提供播放器和管理界面。拟新增链路为：music-server 发现与推荐 → Jackett 搜索 JPopsuki → 用户确认种子 → qBittorrent 下载并做种 → MusicAutoTagger 刮削整理副本 → music-server 扫描入库 → 播放与收藏反馈回流。

## Capabilities and Constraints

- 首版推荐 Album、EP、Single 和 OST；相似艺人发现、Live/Compilation、自动选种和自动下载为后续扩展。
- 已知艺人和已知作品是首版候选来源；冷启动使用现有曲库、收藏和播放行为。
- 推荐卡片需要支持获取、我已有、不感兴趣和状态展示。
- 点击获取时才通过 Jackett 搜索 JPopsuki，并缓存结果；首版由用户确认种子。
- qBittorrent 管理下载和做种；music-server 不主动删除种子或源文件。
- Release Group ID 判断内容是否拥有；Release ID 供未来版本管理使用。
- 原始 PT 文件继续做种，正式曲库接收 MusicAutoTagger 刮削后的副本。
- 本次交付是静态 HTML 交互原型，使用明确标注的演示数据，不连接真实服务。

## Brand Commitments

产品名为 032 Music Server。沿用现有图标、Inter 与平台 CJK 字体、深浅主题、紫色主操作色、专辑封面驱动的视觉层级和现有播放器壳层。

## Evidence on Hand

- `README.md` 与 `docs/features.md`：产品定位与现有能力。
- `docs/daily-mix-design.md`：单用户画像、推荐与反馈机制。
- `docs/images/main-view.png`：现有桌面界面视觉事实。
- `internal/http/assets/` 与 `internal/http/templates/`：设计令牌、组件和应用壳层。
- `/workspace/MusicAutoTagger`：标准刮削流程及 MusicBrainz 标签写入证据。

## Product Principles

- 推荐质量优先于下载数量。
- 外部身份与已拥有判定优先于自动化程度。
- 自动检索，关键下载决策可由用户确认。
- 人工决定永久有效，自动流程不得反复覆盖。
- 做种文件与正式曲库副本分离。
