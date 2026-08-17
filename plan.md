# 032music-server: J-Pop 极致元数据与音乐知识图谱演进规划

## 1. 项目定位与核心愿景

* **核心定位**：专为个人打造的自建数字音乐服务器，深度聚焦于 **J-Pop / ACG / J-Rock / City Pop / 影视与动画配乐** 领域。
* **产品愿景**：摆脱常规流媒体或通用播放器扁平简陋的标签管理，在 **元数据分类、假名注音排序、幕后演职员图谱、Tie-up 商业绑定、伴奏管理与多版本聚合** 上打磨到极致，全面看齐并超越 Roon 在日系音乐管理上的体验。

---

## 2. J-Pop 专属极致元数据体系设计

```
┌────────────────────────────────────────────────────────────────────────┐
│                        J-Pop 音乐知识图谱与元数据模型                     │
├────────────────────────────────────────────────────────────────────────┤
│ 1. 多语言与排序层                                                       │
│    - 原文 (Kanji/Kana) | 假名注音 (Furigana / Reading) | 罗马音 | 译名     │
│    - 精准 A-Z / あ-ん 首字母归类与拼音/假名分栏                          │
├────────────────────────────────────────────────────────────────────────┤
│ 2. 演职员与幕后制作层 (Credits Graph)                                   │
│    - 演唱 (Vocal) | 作词 (Lyricist) | 作曲 (Composer) | 编曲 (Arranger) │
│    - 制作人 (Producer) | 乐器乐手 (Instruments) | 录音/混音/母带工程     │
│    - 企划名义 / 分身 / 组合关系 (Unit / Project / Persona: 如 Sawano, Eve)│
├────────────────────────────────────────────────────────────────────────┤
│ 3. Tie-up 商业与作品关联层 (Works & Tie-ups)                            │
│    - 动画 (Anime) | 电视剧 (Drama) | 电影 (Movie) | 游戏 (Game) | 广告  │
│    - 曲目定位: OP (片头) | ED (片尾) | 插曲 (Insert) | 主题曲 | 角色歌 | OST │
├────────────────────────────────────────────────────────────────────────┤
│ 4. 版本与单曲生态层 (Releases & Track Types)                            │
│    - 主打曲 (A-side) | 伴随曲 (C/W) | 伴奏 (Instrumental / Off Vocal)    │
│    - 实体版本: 初回限定盘 | 通常盘 | 动画盘 | 高解析 Hi-Res (24bit/DSD)   │
├────────────────────────────────────────────────────────────────────────┤
│ 5. 歌词与表现层                                                         │
│    - 日文原文 + 中文翻译双语同步滚动歌词 (.lrc)                          │
│    - 生僻汉字假名注音 (Ruby / Furigana) 支持                             │
└────────────────────────────────────────────────────────────────────────┘
```

---

## 3. 分阶段推进路线图（Roadmap）

### Phase 1: 核心标签扩展与演职员字段重构（数据层 & 扫描层）✅ 已完成
> **目标**：让服务器能够完整识别并入库 J-Pop 最核心的作词、作曲、编曲及假名排序元数据。

* [x] **1.1 数据库架构扩展（Migration `010_jpop_credits.sql`）**：
  * `tracks` 新增 `track_type TEXT NOT NULL DEFAULT 'regular'` 与 `user_track_type TEXT`（用户覆盖），枚举值：`regular`, `instrumental`, `off_vocal`, `tv_size`, `drama_track`, `remix`。
  * `tracks` 新增 `lyricist TEXT`（作词）、`arranger TEXT`（编曲）展示文本字段。
  * `tracks` 新增 `reading_title TEXT`（假名/罗马音排序键）。
  * `artists` 新增 `reading_name TEXT`（假名/罗马音排序键）。
  * `albums` 新增 `reading_title TEXT`（假名/罗马音排序键）。
  * 复用 `track_artists` 表的 `role` 字段，以 `primary` / `composer` / `lyricist` / `arranger` / `producer` 五种角色实现结构化多对多演职员关联。
  * 新增 `idx_track_artists_role`、`idx_tracks_track_type`、`idx_artists_reading_name`、`idx_albums_reading_title` 索引。
* [x] **1.2 音频扫描引擎升级（Scanner & Reader）**：
  * `AudioMetadata` 结构体扩展：新增 `Lyricist`、`Arranger`、`Producer`、`TrackType`、`ArtistSort`、`AlbumArtistSort`、`TitleSort`、`AlbumSort` 字段。
  * FLAC (Vorbis Comment) 解析：提取 `LYRICIST`、`ARRANGER`、`PRODUCER`、`ARTISTSORT`、`ALBUMARTISTSORT`、`TITLESORT`、`ALBUMSORT`。
  * ID3v2 / MP4 解析：从 Raw 标签提取同名字段，兼容 iTunes 风格别名（`SOAR`/`TSOP`、`SOAA`/`TSO2`、`SONM`/`TSOT`、`SOAL`/`TSOA`）。
  * TIPL/TMCL 帧解析推迟至 Phase 4（dhowden/tag 库支持有限，且日系 FLAC 以 Vorbis Comment 为主）。
* [x] **1.3 伴奏与曲目类型自动识别（三层规则）**：
  * **标题匹配**（30+ 模式）：`(Instrumental)`、`(Off Vocal)`、`(カラオケ)`、`(TV Size)`、`(TVサイズ)`、`(Anime ver.)`、`(Short Ver.)`、`(Drama)`、`(ドラマ)`、`(Skit)`、`(Remix)` 等，含括号/方括号两种写法。
  * **文件夹匹配**：`Instrumental/`、`Off Vocal/`、`Off-Vocal/`、`カラオケ/` 等目录名。
  * **标签匹配**：`CONTENTTYPE` 字段含 `instrumental` / `drama` / `remix` 关键词。
* [x] **1.4 ImportTrack 重构（入库逻辑）**：
  * 写入 `track_type`、`lyricist`、`arranger` 到 `tracks` 表。
  * 将作词/作曲/编曲/制作人按独立 role 写入 `track_artists`（`INSERT OR IGNORE` 避免重复）。
  * 从 `ALBUMSORT` 标签写入 `albums.reading_title`。
  * 从 `ALBUMARTISTSORT` / `ARTISTSORT` 标签写入 `artists.reading_name`（仅单艺术家时写入，避免多人拼接串误归属）。
* [x] **1.5 查询与 API 层适配**：
  * `Track` 结构体新增 `Lyricist`、`Arranger`、`TrackType` JSON 字段。
  * `ListTracks` 查询的 `track_artists` JOIN 限定 `role='primary'`，避免作词/编曲人出现在演唱者列。
  * `UpdateTrack` API 支持 `trackType` 用户覆盖。
  * `SplitPeople()` 导出为公共函数，供 storage 层复用拆分演职员名。

#### Phase 1 验收标准

| # | 验收项 | 预期结果 | 验证方法 |
|---|--------|----------|----------|
| AC-1.1 | Migration 平滑升级 | 已有数据库执行 `010_jpop_credits.sql` 后无报错，新字段有默认值，不破坏现有数据 | 启动服务器，观察 migration 日志无 error |
| AC-1.2 | FLAC 文件提取作词/编曲 | 含 `LYRICIST=畑亜貴` / `ARRANGER=神前暁` Vorbis Comment 的 FLAC 文件扫描后，`tracks.lyricist` 和 `tracks.arranger` 字段正确填充 | 扫描后 `SELECT lyricist, arranger FROM tracks` 验证 |
| AC-1.3 | MP3/M4A 提取 Sort 键 | 含 `ARTISTSORT`(或 `TSOP`)标签的 MP3 文件扫描后，`artists.reading_name` 正确写入 | 扫描后 `SELECT reading_name FROM artists` 验证 |
| AC-1.4 | 伴奏自动标记 | 标题含 `(Instrumental)` 的曲目自动标记为 `track_type='instrumental'`；标题含 `(Off Vocal)` 或 `(カラオケ)` 的标记为 `off_vocal` | `SELECT title, track_type FROM tracks WHERE track_type != 'regular'` |
| AC-1.5 | 文件夹级伴奏识别 | 放在 `Instrumental/` 子目录下的曲目即使标题无后缀也被标记为 `instrumental` | 准备测试目录结构，扫描后验证 |
| AC-1.6 | TV Size 识别 | 标题含 `(TV Size)`、`(TV ver.)`、`(TVサイズ)` 的曲目标记为 `tv_size` | 同 AC-1.4 |
| AC-1.7 | 演职员多角色写入 | 一首同时有 ARTIST / LYRICIST / COMPOSER / ARRANGER 标签的 FLAC 文件，扫描后 `track_artists` 表中该 track_id 出现对应的 `primary` / `lyricist` / `composer` / `arranger` 四种 role 记录 | `SELECT role, artist_id FROM track_artists WHERE track_id=? ORDER BY role` |
| AC-1.8 | API 返回新字段 | `GET /api/v1/tracks` 返回的 JSON 每条包含 `lyricist`、`arranger`、`trackType` 字段 | curl 或浏览器调用 API 检查 JSON 结构 |
| AC-1.9 | 用户覆盖 track_type | 通过 `PATCH /api/v1/tracks/{id}` 发送 `{"trackType":"instrumental"}`，该曲目的 `trackType` 在后续 API 查询中返回覆盖值 | API 写入 → 读取验证 |
| AC-1.10 | 单元测试全通过 | `go test ./...` 全部 PASS，包含 28 个 `TestInferTrackType` 用例 | `go test ./... -v` |
| AC-1.11 | 向后兼容 | 已有客户端（Sync API）不受影响，`SyncTracks` / `SyncAlbums` 返回结构不变 | 运行 `TestSyncAlbumsEndpoint` / `TestSyncTracksEndpointCursorAndAlias` 通过 |
| AC-1.12 | 编译零警告 | `go build ./...` 无编译错误、无未使用导入 | CI 编译检查 |

---

### Phase 2: 双语同步歌词与播放体验特化（展示与体验层）
> **目标**：打造丝滑的 J-Pop 听歌体验，彻底解决日文歌词看不懂与随机播放切到伴奏的痛点。

* [ ] **2.1 双语与多时轨 LRC 歌词引擎**：
  * 本地同目录 `.lrc` 文件自动探测与内嵌 `UNSYNCEDLYRICS` / `LYRICS` 标签解析。
  * 支持日文原文 + 中文翻译双行时间戳对齐与渲染。
  * 提供歌词 API：`GET /api/v1/tracks/{id}/lyrics` 返回结构化时间轴数据。
* [ ] **2.2 伴奏曲目智能过滤（Playback Filtering）**：
  * 在播放列表、随机播放（Shuffle）、电台模式中增加 `hide_instrumental` 开关。
  * 保留单曲完整浏览的同时，避免全局随机播放连续听到多首伴奏。
  * 基于 Phase 1 的 `track_type` 字段实现过滤（`WHERE track_type NOT IN ('instrumental','off_vocal')`）。

#### Phase 2 验收标准

| # | 验收项 | 预期结果 | 验证方法 |
|---|--------|----------|----------|
| AC-2.1 | LRC 文件自动探测 | 与音频同目录、同名的 `.lrc` 文件在扫描或 API 请求时被自动关联 | 放置 `test.lrc` 旁 `test.flac`，调用歌词 API 返回内容 |
| AC-2.2 | 内嵌歌词解析 | 嵌入 `UNSYNCEDLYRICS` 的 FLAC 文件，歌词 API 返回歌词文本 | 调用 API 验证 |
| AC-2.3 | 双语时间轴对齐 | 含日文+中文双语时间戳的 LRC 文件，API 返回按时间轴交织的结构化数据 | 检查 JSON 中每个时间点含 `original` 和 `translation` 两行 |
| AC-2.4 | 歌词 API 端点 | `GET /api/v1/tracks/{id}/lyrics` 返回 `{lines: [{timeMs, text, translation?}]}` | HTTP 状态 200 + JSON Schema 校验 |
| AC-2.5 | 伴奏过滤开关 | Shuffle API 传入 `hideInstrumental=true` 时不返回 `track_type` 为 `instrumental` 或 `off_vocal` 的曲目 | 构建含伴奏的测试库，验证过滤后列表 |
| AC-2.6 | 专辑页完整展示 | 专辑详情页仍展示全部曲目（含伴奏），伴奏用视觉标签区分但不隐藏 | 访问专辑页确认 |

---

### Phase 3: Tie-up 作品关联体系与多语言检索（组织与索引层）
> **目标**：让音乐不再只按"歌手/专辑"分类，而是能按"动画/影视/游戏"一站式探索。

* [ ] **3.1 Tie-up 数据模型设计**：
  * 建立 `works`（作品表：如《葬送的芙莉莲》、《Unnatural》）与 `work_tracks`（曲目关联与角色：OP / ED / 插入歌 / OST）。
* [ ] **3.2 标签与文件夹命名规则智能推导**：
  * 解析 ID3/Vorbis 的 `CONTENTGROUP`, `SUBTITLE`, `ALBUM` 中的影视动画标签，自动聚合成作品合辑。
* [ ] **3.3 多语言与假名首字母检索索引**：
  * 基于 Phase 1 的 `reading_name` / `reading_title` 字段，构建日文平假名（あ/か/さ/た/な/は/ま/や/ら/わ）及英文字母（A-Z）的多级索引视图。
  * 搜索 API 支持日文汉字、平假名、片假名与罗马音跨形态模糊搜索。

#### Phase 3 验收标准

| # | 验收项 | 预期结果 | 验证方法 |
|---|--------|----------|----------|
| AC-3.1 | Works 模型建表 | `works` 和 `work_tracks` 表创建成功，支持 CRUD 操作 | Migration 执行无报错 + 插入/查询测试 |
| AC-3.2 | OP/ED 关联 | 一首被标记为《葬送的芙莉莲》OP 的曲目可通过作品维度查到 | `GET /api/v1/works/{id}/tracks` 返回关联 |
| AC-3.3 | 标签推导 | `CONTENTGROUP` 含 "葬送のフリーレン" 的曲目自动关联到对应 work | 扫描后检查 `work_tracks` 表 |
| AC-3.4 | 假名首字母索引 | `GET /api/v1/artists?index=さ` 返回排序键以 さ 行开头的歌手 | API 调用验证 |
| AC-3.5 | 跨形态搜索 | 搜索 "yonezu" 能匹配 "米津玄師"（通过 `reading_name` 中的罗马音） | `GET /api/v1/artists?q=yonezu` 返回结果 |

---

### Phase 4: 日系专属数据源自动刮削（Enrichment & Scraping）
> **目标**：大幅降低手动修元数据的成本，自动从最专业的日系数据库补全信息。

* [ ] **4.1 VGMdb 刮削插件开发**：
  * 针对 ACG 唱片编号（如 `SVWC-xxxxx`, `KICA-xxxxx`）精准抓取完整发售日、作词/作曲/编曲名单与特典信息。
* [ ] **4.2 MusicBrainz 日音增强适配**：
  * 针对日系艺术家别名、组合分身（SawanoHiroyuki[nZk]、YOASOBI/Ayase、米津玄师/ハチ）建立主从关联。
  * 解析 TIPL / TMCL 帧或 MusicBrainz 关系补全 Phase 1 未覆盖的 involved people 信息。
* [ ] **4.3 Bangumi (番组计划) 作品元数据对齐**：
  * 自动关联动画番剧海报、原作信息与播出年份。

#### Phase 4 验收标准

| # | 验收项 | 预期结果 | 验证方法 |
|---|--------|----------|----------|
| AC-4.1 | VGMdb 编号匹配 | `catalog_number='SVWC-70658'` 的专辑能自动从 VGMdb 拉取完整 credits | 触发 enrichment → 检查 `track_artists` 新增记录 |
| AC-4.2 | MusicBrainz 别名 | "ハチ" 与 "米津玄師" 自动建立 `merged_into_artist_id` 主从关联 | 检查 artists 表合并关系 |
| AC-4.3 | Bangumi 作品对齐 | 关联的动画作品自动获取海报图和播出年份 | 检查 `works` 表 `poster_url` 和 `year` 字段 |
| AC-4.4 | 增量刮削 | 已获取过的元数据在缓存期内不重复请求 | 二次刮削时无新 HTTP 请求（日志验证） |
| AC-4.5 | 手动触发 | `POST /api/v1/enrichment/run` 可手动启动刮削任务 | HTTP 202 + 后台任务完成 |

---

### Phase 5: Roon 式多维透视筛选与知识图谱（Focus & Knowledge Graph）
> **目标**：将所有元数据打通成网，实现任意维度的探索与精准挖掘。

* [ ] **5.1 幕后制作人关系图谱（Credits Exploration）**：
  * 基于 Phase 1 的 `track_artists` 多角色数据，在 Web/API 中支持点击"编曲：神前晓"或"作词：松本隆"，瞬间拉出该创作者在曲库中的全部作品。
* [ ] **5.2 J-Pop 专属 Focus 多维筛选引擎**：
  * 支持复合条件交叉筛选：
    * `[年代: 2010s]` AND `[类型: 动画主题曲]` AND `[编曲: 泽野弘之]` AND `[格式: Hi-Res 无损]` AND `[排除伴奏]`。
* [ ] **5.3 专辑多版本（Versions）合并展示**：
  * 同一张单曲/专辑的通常盘、初回限定盘、动画盘、Hi-Res 重置版自动归入同一主条目，支持自由切换。

#### Phase 5 验收标准

| # | 验收项 | 预期结果 | 验证方法 |
|---|--------|----------|----------|
| AC-5.1 | Credits 探索 | `GET /api/v1/artists/{id}/credits?role=arranger` 返回该编曲家参与的所有曲目 | API 调用返回列表 |
| AC-5.2 | 复合筛选 | `GET /api/v1/tracks?year=2020&trackType=regular&arrangerArtist=123&format=flac` 返回交叉筛选结果 | API 调用返回精确匹配 |
| AC-5.3 | 排除伴奏筛选 | 筛选条件含 `excludeTypes=instrumental,off_vocal` 时结果中无伴奏 | API 验证 |
| AC-5.4 | 多版本合并 | 同一张单曲的通常盘和初回盘在 API 中返回时归属同一 `version_group_id` | `GET /api/v1/albums/{id}` 含 `versions` 数组 |
| AC-5.5 | 版本切换 | 点击版本条目可查看该版本的独有曲目和差异 | 对比两个版本的 track 列表 |

---

## 4. 推荐实施技术规范

1. **数据库标准**：采用 SQLite STRICT 模式，严格定义外键级联与索引，保持现有轻量高效的单文件架构。
2. **向后兼容**：新增字段均提供安全平滑的 Migration，不破坏既有 API 契约与已有客户端同步逻辑。
3. **性能保证**：扫描与元数据解析坚持纯 Go 原生实现，高频假名注音和排序键在入库时预计算并存入索引列。
4. **测试覆盖**：每个 Phase 的核心逻辑必须有单元测试，伴奏识别、标签解析等规则引擎要求 ≥90% 分支覆盖。

---

## 5. 实施记录

### Phase 1 实施记录 (已完成)

**变更文件清单**：
| 文件 | 变更 | 说明 |
|------|------|------|
| `internal/storage/migrations/010_jpop_credits.sql` | 新增 | 数据库 Migration |
| `internal/metadata/reader.go` | 修改 | AudioMetadata 扩展 + FLAC/ID3 标签提取 + 伴奏自动识别 |
| `internal/metadata/track_type_test.go` | 新增 | 28 个 track type 推断测试 |
| `internal/storage/library.go` | 修改 | ImportTrack 重构 + Track 结构体扩展 + 查询层更新 |
| `internal/http/library.go` | 修改 | API/HTTP 层适配新字段 |

**技术决策**：
| 决策 | 选择 | 理由 |
|------|------|------|
| 演职员关联方式 | 复用 `track_artists.role` | 表结构已预留 role 字段，最小改动；5 种角色覆盖 J-Pop 核心需求 |
| TIPL/TMCL 解析 | 推迟至 Phase 4 | dhowden/tag 库支持有限；日系 FLAC 以 Vorbis Comment 为主，实用性优先 |
| reading_name 写入策略 | 仅单艺术家时写入 | 多艺术家的 `*SORT` 标签是拼接串（如 "A; B"），无法准确归属单个艺术家 |
| track_type 推断优先级 | 标签 → 标题 → 文件夹 | 标签最权威；标题覆盖最广（30+ 模式）；文件夹兜底 |
