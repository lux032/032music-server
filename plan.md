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

### Phase 2: 双语同步歌词与播放体验特化（展示与体验层）✅ 已完成
> **目标**：打造丝滑的 J-Pop 听歌体验，彻底解决日文歌词看不懂与随机播放切到伴奏的痛点。

* [x] **2.1 LRC 歌词引擎（简化版）**：
  * 本地同目录 `.lrc` 文件自动探测与内嵌 `UNSYNCEDLYRICS` / `LYRICS` 标签解析。
  * 支持 `[mm:ss.xx]` 时间戳解析，多时间戳同行展开，按时间排序。
  * 提供歌词 API：`GET /api/v1/tracks/{id}/lyrics` 返回结构化数据 `{synced, lines: [{timeMs, text}]}`。
  * 歌词源优先级：`.lrc` 外部文件 > 数据库内嵌歌词。无歌词返回 404。
* [x] **2.2 伴奏曲目智能过滤（Playback Filtering）**：
  * 在 `ListTracks` / `CountTracks` API 中增加 `hideInstrumental=true` 查询参数。
  * 基于 Phase 1 的 `track_type` 字段实现过滤（`WHERE track_type NOT IN ('instrumental','off_vocal')`）。
  * 专辑详情页完整展示，不受过滤影响。

#### Phase 2 验收标准

| # | 验收项 | 预期结果 | 验证方法 |
|---|--------|----------|----------|
| AC-2.1 | LRC 文件自动探测 | 与音频同目录、同名的 `.lrc` 文件在 API 请求时被自动关联 | 放置 `test.lrc` 旁 `test.flac`，调用歌词 API 返回内容 |
| AC-2.2 | 内嵌歌词解析 | 嵌入 `UNSYNCEDLYRICS` 的 FLAC 文件，歌词 API 返回歌词文本 | 调用 API 验证 |
| AC-2.3 | 时间戳解析 | 含 `[mm:ss.xx]` 时间戳的 LRC 文件，API 返回 synced=true 的结构化数据 | 检查 JSON 中每个时间点含 `timeMs` 和 `text` |
| AC-2.4 | 歌词 API 端点 | `GET /api/v1/tracks/{id}/lyrics` 返回 `{synced, lines: [{timeMs, text}]}` | HTTP 状态 200 + JSON Schema 校验 |
| AC-2.5 | 伴奏过滤开关 | API 传入 `hideInstrumental=true` 时不返回 `track_type` 为 `instrumental` 或 `off_vocal` 的曲目 | 构建含伴奏的测试库，验证过滤后列表 |
| AC-2.6 | 专辑页完整展示 | 专辑详情页仍展示全部曲目（含伴奏），伴奏用视觉标签区分但不隐藏 | 访问专辑页确认 |

---

### Phase 3: Tie-up 作品关联体系与多语言检索（组织与索引层）✅ 已完成
> **目标**：让音乐不再只按"歌手/专辑"分类，而是能按"动画/影视/游戏"一站式探索。

* [x] **3.1 Tie-up 数据模型设计**：
  * 建立 `works`（作品表：如《葬送的芙莉莲》、《Unnatural》）与 `work_tracks`（曲目关联与角色：OP / ED / 插入歌 / OST）。
* [x] **3.2 标签与文件夹命名规则智能推导**：
  * 解析 ID3/Vorbis 的 `CONTENTGROUP`, `SUBTITLE`, `ALBUM` 中的影视动画标签，自动聚合成作品合辑。
* [x] **3.3 多语言与假名首字母检索索引**：
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

### Phase 4: 日系专属数据源自动刮削（Enrichment & Scraping）✅ 已完成
> **目标**：大幅降低手动修元数据的成本，自动从最专业的日系数据库补全信息。

* [x] **4.1 VGMdb 刮削插件开发**（⚠️ 已下线，见 Phase 4 修订记录：vgmdb.info 镜像不可用、vgmdb.net 拦截自动化访问）：
  * 针对 ACG 唱片编号（如 `SVWC-xxxxx`, `KICA-xxxxx`）精准抓取完整发售日、作词/作曲/编曲名单与特典信息。
* [x] **4.2 MusicBrainz 日音增强适配**：
  * 针对日系艺术家别名、组合分身（SawanoHiroyuki[nZk]、YOASOBI/Ayase、米津玄师/ハチ）建立主从关联。
  * 解析 TIPL / TMCL 帧或 MusicBrainz 关系补全 Phase 1 未覆盖的 involved people 信息。
* [x] **4.3 Bangumi (番组计划) 作品元数据对齐**：
  * 自动关联动画番剧海报、原作信息与播出年份。

#### Phase 4 验收标准

| # | 验收项 | 预期结果 | 验证方法 |
|---|--------|----------|----------|
| AC-4.1 | VGMdb 编号匹配 | `catalog_number='SVWC-70658'` 的专辑能自动从 VGMdb 拉取完整 credits | 触发 enrichment → 检查 `track_artists` 新增记录 |
| AC-4.2 | MusicBrainz 别名/分身 | “ハチ”与“米津玄師”等关系生成高置信审核候选，不自动执行破坏性合并；管理员确认后复用可回滚合并流程 | 检查 `artist_relation_candidates` 与管理后台审核 |
| AC-4.3 | Bangumi 作品对齐 | 关联的动画作品自动获取海报图和播出年份 | 检查 `works` 表 `poster_url` 和 `year` 字段 |
| AC-4.4 | 增量刮削 | 已获取过的元数据在缓存期内不重复请求 | 二次刮削时无新 HTTP 请求（日志验证） |
| AC-4.5 | 手动触发 | `POST /api/v1/enrichment/run` 可手动启动刮削任务 | HTTP 202 + 后台任务完成 |

---

### Phase 4.5: Bangumi 深度整合与作品展示（✅ 已完成，含 4.5.7 自定义图片）
> **目标**：以 Bangumi 数据能可靠支撑的范围为边界，完善“类型 → 作品 → 专辑”的浏览体验与关联精度。项目定位为**日本 ACG 优先**，普通影视不在考虑范围。
> **实施方式**：硬交付流程（Oracle 审计 → 用户决策 → UI 效果图确认 → Worker → Reviewer），建议分批交付：①数据（4.5.1–4.5.4）②UI（4.5.5–4.5.6）③自定义图片（4.5.7）。

* [x] **4.5.1 Bangumi 音乐条目确认专辑关联**：专辑名 → Bangumi 音乐条目（type 3）→ 关联的动画/游戏；同时取得角色（原声集/片头曲/片尾曲/插入歌/角色歌）。以 Bangumi 为权威：作品侧关系为具体角色且唯一即自动确认（含 `infinite synthesis 6` → 269645《とある魔術の禁書目録 幻想収束》）；广播剧、其他和泛关系进入审核，不加本地启发式否决。专辑级只记 `ost` / `other`，具体角色按音乐条目同名及受限版本后缀规则记在曲目级；找不到同名曲目时只保留专辑级关联。原创专辑逐曲反查仍留待 4.5.4。实现遵循定稿设计 `docs/design/works-association.md`。
  * **详细设计与用户决策（分叉1–5：A/A/B/A/A）**：仅反查恰好一部有片头/片尾/插入/原声/角色歌角色的动画或游戏时自动确认；处理没有 manual / bangumi 专辑关联的非合辑（仅有 auto 关联也处理并由 Bangumi 替换）；普通批量只看 Anime/アニメ/アニソン 流派或 tv_size，强制及单专辑放宽动画筛选；不以首曲名搜索、不做 4.5.4 曲名反查；改标题保留既有 Bangumi 关联。艺术家一致、分数≥80、领先≥20、未抑制及候选未拒绝是自动确认的其他必要条件。候选和 miss 用 migration 026 持久化，拒绝与解除由 suppression key 保留，人工审核可选择多作品。
* [x] **4.5.2 作品类型修正**：migration 028 新增 `type_locked` 并回填既有 manual 作品；作品阶段按 Bangumi type/platform（剧场版→movie，其他 type 2→anime，type 4→game）纠正未锁定作品，冲突时保留原值并记录证据；外部资料类型同步更新。
* [x] **4.5.3 多季折叠**：按 Phase 4.6 的单层系列模型实现（migration 029：`work_series` / `work_series_members` / `work_series_locks`）；沿 Bangumi 续集/前传关系（仅 type 2 条目）把同一作品的多季归组，库外中间条目最多 3 跳、每组最多 40 节点并做环检测；请求失败的分量本轮跳过，已有归属不变；系列按成员重合度复用（保住用户改名），代表作品取库中最早播出作品（D25）；单部作品不建系列，auto 成员 ≤1 且无 manual 成员的空系列自动清理。作品列表默认把同一系列折叠为一行（共 N 部，可展开），分页与总数按折叠后行数计算；作品详情页显示所属系列与同系列作品，支持拆出（写锁定）、手动加入（source=manual，解除锁定）、重命名（title_source=manual）与解散（全体成员写锁定）。跨媒体系列（如 Fate 游戏/动画/剧场版）、联动/世界观关系本版不做（关系链噪音大，只能人工确认）。
* [x] **4.5.4 精选集/原创专辑主题曲识别**：
  * 本地识别曲名中的严格标注（媒体前缀 + 引号作品 + OP/ED 等）；
  * “曲名 + 歌手”反查 Bangumi 单曲条目；**条目名一致 + 本曲 primary 歌手及别名一致 + 恰好 1 部作品登记了具体用途**才自动确认。条目名一致但歌手不一致或没有 primary 歌手时记为未命中，不进审核（D8）；多个条目指向不同的（作品，用途）或多曲名条目才进审核（实测：only my railgun、紅蓮華 自动确认；カタオモイ、歌手不一致的 Hello/Link 记未命中；朝が来る、赤い罠/ADAMAS 写 multi_title 候选）；
  * 以可停止的后台任务运行，结果缓存，沿用 Bangumi 节流；
  * 单曲 A 面 / c/w 的精确角色由此提供（替代已放弃的曲名推断，见修订记录“选项 B”）。
  * **实现**：migration 027 只新建 `track_subject_candidates` 与 `track_enrichment_misses`（不重建 work_tracks，026 已放开 bangumi 来源）。搜索关键词把 `-` 换成空格（H1，缓存键 `music-search:v2:`），专辑级请求同样处理，打分仍用原标题；专辑级与曲目级共用抑制与落地；确认后删掉该曲目的 auto 行，本地推导遇到 bangumi/manual 行跳过（D9）。审核接口 `GET/POST /api/v1/enrichment/tracks/{trackId}/subjects...` 与 `/admin/enrichment` 曲目候选区：接受时改过用途的作品写 `source=manual`，没改的写 `source=bangumi`（D10）；拒绝写 `track_work_suppressions` 的 `bangumi:<m>`。扫描后全量任务包含曲目级（D11），未命中重查间隔 max(cacheDays,90) 天、发售 180 天内为 7 天（D12），只跳过 manual/bangumi 的 ost 专辑（D13）。
* [x] **4.5.5 专辑页 UI（仅 PC）**：
  * OST/单曲类：标题区作品胶囊（小海报 + 作品名 + 角色），多作品收成“+N”；
  * 精选集：曲目行显示关联图标（多作品带数字角标），悬停 ~150ms 弹出卡片（海报、原名、中文译名、类型·角色·年份，可点击跳转），移开 ~300ms 关闭，鼠标可移入卡片；键盘 Tab 聚焦显示、Esc 关闭；做成通用组件；
  * 右侧信息栏“关联作品”汇总（仅有关联时显示），有待审核候选时显示入口；
  * 关联编辑放入“编辑专辑”抽屉；先用 Playwright 出静态效果图给用户确认。
* [x] **4.5.6 作品页 UI**：关联专辑封面网格；精选集中的单曲单独列入“收录于”分组；多季折叠展示。
* [x] **4.5.7 自定义专辑封面与歌手图片**：
  * 编辑抽屉中上传/恢复默认，只存数据目录，绝不改写音乐文件；
  * 自定义图片优先，重扫、自动刷新（Last.fm/MusicBrainz 覆盖 `artist_image_cache`）、专辑合并都不能覆盖；把分散在 5 处 SQL 的“选封面”规则收拢为统一规则；
  * JPEG/PNG/WebP，按内容判断格式，上限 10MB 并限制像素尺寸，管理员 + CSRF，按哈希去重；
  * 缓存失效：图片地址带版本号/ETag，更新专辑 `updated_at` 以便客户端同步；
  * 不做裁剪（居中铺满显示）；专辑合并保留目标专辑的自定义封面，目标没有时沿用源专辑的。
  * **实现**：migration 030 重建 `artworks` 为 AUTOINCREMENT（每次上传必得新 artwork id，URL 即缓存键）并新建 `artist_custom_images`；`albumArtworkURLSQL` 统一为 custom 优先并替换全部 9 处分散取封面 SQL（含 playlist、Sync 两处 `is_primary=1` JOIN），新增 `artistImageURLSQL(alias)` 统一 4 处歌手图片地址（自定义带 `?v=<哈希前12位>`）；文件存 `<数据目录>/custom-images/<sha256("custom:"+内容)>.<ext>`，临时文件 + rename 原子写入、哈希去重、引用检查 + 1 小时孤儿 GC；上传按魔数 + DecodeConfig 判格式（JPEG/PNG/WebP，x/image 解码），先查像素上限（D29：边 ≤8192、总像素 ≤4000 万）再完整解码一次；`POST /admin/albums/{id}/artwork[/reset]` 与 `/admin/artists/{id}/image[/reset]` 管理员 + CSRF + MaxBytesReader 11MB；专辑合并保留目标自定义封面、否则沿用第一张源专辑的（custom 行单独转移，非 custom 封面合并行为不变）；歌手合并按 merged-from 继承自定义图片（回滚安全）；专辑清理级联删除 custom 行（D31），重扫写入默认主图不再置 primary 于 custom 之上。Reviewer 修订：数据库只存文件名（M3）、上传全程串行 + 去重刷新 mtime（M4）、主表单文件判定改按 `form.elements`（M1）、WebP 缩略图加入允许列表（M2）、歌手合并继承规则 D50（恢复默认连同删除 merged-from 行，撤销合并不恢复源行——已知行为）、启动与扫描完成后各跑一次孤儿 GC（L2）、上传完整解码复用缩略图并发槽（L7）。

### Phase 4.6: 系列层（Phase 4.5 之后单独立项）（已全部完成：批次 5 数据层 + 批次 6 UI）
* [x] 系列只做一层、一个作品只属于一个系列。
* [x] 类型作为筛选而非固定层级，单一类型的系列省略类型层（批次 6：/works 按类型筛选时系列行显示“共 N 部，其中 K 部为<类型>”、海报取第一部符合类型成员、展开区只列符合类型成员；系列展开区、作品详情页系列条、系列管理详情按类型分组显示，单一类型省略分组头）。
* [x] 季数/剧场版沿续集、前传关系自动归入（4.5.3 已上线：自动归组、拆出、重命名、手动加入、解散，用户意图永久有效）。
* [x] 跨媒体关系只生成建议（批次 5 数据层 + 批次 6 审核 UI：/admin/work-review 第 4 个 Tab“系列建议”，接受按当前归属新建/加入/合并，命名冲突与失效友好提示）；完整的管理页可手动创建系列、调整归属（批次 6：/admin/series 列表 + 详情，新建、改名、加入/移入、移出写锁、合并（D58 命名）、解散）。

### 待办与已知风险（作品关联“专辑为主”交付时暂缓）
| # | 项目 | 影响 | 建议 |
|---|------|------|------|
| D-1 | 曲目级 alias 沿用：动画 X 被用户改名为 Y 后、库中尚无游戏 X 时，精选集曲目标签 `ゲーム『X』…` 可能误挂到 Y | 游戏 X 入库后下次重算自愈；可手动解除 | `work_aliases` 增加 `alias_type`，放行条件改为类型一致 |
| D-2 | `TestSeasonSpellingAliasKeepsTypeFilter` 未单独证明曲目级 `origin='manual'` 条件 | 已解决（批次 1） | 新增曲目级 manual/auto 对照用例 |
| D-3 | 季数前缀候选查询无法走索引 | 仅精确匹配失败时执行，命中后写别名；当前规模可忽略 | 改为范围条件 |
| D-4 | Bangumi 增强在作品恰被清理时两处极窄窗口会记为失败/审核而非跳过 | 不影响数据正确性 | 写入失败后复查作品存在 |
| D-5 | `RefreshAlbumWorks` 专辑级写入失败会回滚整批（≤100 张）并中止 | 已解决（批次 1） | 按专辑 savepoint 回滚并继续其余专辑 |
| D-6 | 相似度中“同作品”信号与“同专辑”信号叠加 | 推荐权重可能偏移 | 上线后观察 |
| D-7 | 规则 v4→v5 旧格式推断键 | 仅影响未发布的开发数据库，生产库不受影响 | 无需处理 |
| D-8 | 标题含 `&` 且为 VA 的单部作品专辑（如 `EIGHTY-SIX REARRANGE & OUTTRACKS CD`）放弃自动关联 | 需手动关联 | 视误伤数量再放宽 |
| D-9 | `catalog_number` 开头含 NUL 字符（如 `\0\0\0\0ARCD0012`） | 已解决（批次 5，D62） | rawFirst 统一清理 + Migrate 幂等修复存量 |
| D-10 | `resolveAutoWork` 为优先复用已绑定 Bangumi 作品会扫描候选作品及别名 | 大型作品库刷新时可能出现性能压力 | 后续增加规范化身份索引或预计算映射，本轮不改语义 |
| D-11 | 标题含 `\|` 时抑制键被误按旧格式解析，按 anime 解除后改成 game 会失效 | 已解决（批次 1） | 从右往左拆键并验证合法类型/季数 |
| D-12 | `RemoveWorkAlbum` 不删除专辑级写到曲目上的 bangumi 行 | 已解决（批次 1） | 只清理同作品、同专辑、同 inferred_key 的 bangumi 曲目行 |
| D-13 | 专辑级人工接受的抑制解除口径与曲目级不一致：专辑级遇到精确的 `bangumi:<m>:<w>` 会返回 review（409），用户无法用人工接受推翻 | 已解决（批次 1） | 共用人工解除函数；保留整条目拒绝 |
| D-14 | 专辑级搜索由 limit=25 改为每页 20 条加分页，翻页条件是条目名完全一致，专辑名很少满足，第 21～25 条会丢失 | 已解决（批次 1） | 分页大小参数化；专辑 25、曲目 20 |
| D-15 | album scope 还会刷新该专辑已关联作品的资料 | 超出设计 8.2 第 9 条的描述，但行为合理（专辑页一键补齐） | 已在设计文档 8.2 补说明；如要收窄可单独提案 |
| D-16 | 本地推导遇到曲目上任何 manual 或 bangumi 行就整首跳过，纯手动行也会挡住本地推导去关联其他作品 | 符合 R1 和 D9，是有意为之，只作记录 | 无需处理 |
| D-17 | `/admin/enrichment` 待审曲目每条都单独调一次 `TrackByID`（最多 200 次查询） | 已解决（批次 3） | 在 `trackCandidateSelect` 里直接带出歌手，省掉逐条查询 |
| D-18 | 曲目 Bangumi 未命中指纹不包含抑制状态；解除抑制后仍可能命中旧 miss | 已解决（批次 5，D63） | 抑制删除时同事务清掉受影响曲目的 bangumi miss |
| D-19 | metadata/reader.go 里独立的 rawFirst 闭包（LYRICIST/ARRANGER/排序名等曲目级字段）不清理控制字符 | 展示问题，专辑字段已由 D62 覆盖 | 下次动 reader.go 时复用 storage 的清理口径 |

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

---

### Phase 2 实施记录 (已完成)

**变更文件清单**：
| 文件 | 变更 | 说明 |
|------|------|------|
| `internal/lyrics/parser.go` | 新增 | LRC 歌词解析引擎：时间戳解析、多时间戳展开、元数据标签跳过、LRC 文件路径探测 |
| `internal/lyrics/parser_test.go` | 新增 | 11 个 LRC 解析测试用例（空文件、同步歌词、非同步歌词、多时间戳、元数据跳过、精度变体） |
| `internal/http/lyrics.go` | 新增 | 歌词 API handler：外部 LRC 文件探测 → 内嵌歌词回退 → 404 |
| `internal/http/app.go` | 修改 | 注册 `GET /api/v1/tracks/{id}/lyrics` 路由 |
| `internal/http/client_features.go` | 修改 | Capabilities 新增 `lyrics: true`、`instrumentalFilter: true` |
| `internal/http/library.go` | 修改 | `filters()` 解析 `hideInstrumental=true` 查询参数 |
| `internal/storage/library.go` | 修改 | `Filters` 新增 `HideInstrumental`；`ListTracks`/`CountTracks` SQL 增加伴奏过滤条件；新增 `AudioFilePath()`、`TrackLyrics()` 方法 |

**技术决策**：
| 决策 | 选择 | 理由 |
|------|------|------|
| 歌词引擎复杂度 | 简化版（不做双语合并） | 双语 LRC 格式无统一标准，实现复杂度高；基础版已满足核心需求，后续可增量扩展 |
| 歌词存储策略 | 不额外存储，API 请求时动态探测 | LRC 文件可能频繁编辑，动态读取保持实时性；避免扫描时增加 I/O 开销 |
| 歌词源优先级 | 外部 `.lrc` 文件 > 内嵌歌词 | 外部 LRC 文件通常有时间戳且更精确；内嵌歌词作为兜底 |
| 伴奏过滤作用域 | 仅影响 `ListTracks`/`CountTracks` | 专辑详情页需要完整展示所有曲目（含伴奏），符合 AC-2.6 |
| 过滤实现方式 | SQL WHERE 条件 + `boolInt` 参数化 | 复用现有 Filters 模式，零改动数据库 schema |

---

### Phase 3 实施记录 (已完成)

**变更文件清单**：
| 文件 | 变更 | 说明 |
|------|------|------|
| `internal/storage/migrations/011_works_tieups.sql` | 新增 | `works` / `work_tracks` STRICT 表、约束、级联与索引 |
| `internal/metadata/tieup.go` | 新增 | CONTENTGROUP/SUBTITLE/ALBUM/目录积极推导，识别作品类型、OP/ED/插曲/OST、季数与序号 |
| `internal/storage/works.go` | 新增 | Works CRUD、作品曲目关联、分页筛选与扫描幂等入库 |
| `internal/storage/search.go` | 新增 | 平假名/片假名搜索变体与五十音行、A-Z、# 索引条件 |
| `internal/storage/library.go` | 修改 | 扫描写入自动作品关联；艺术家/专辑 reading 搜索与索引筛选 |
| `internal/http/works.go` | 新增 | 完整 Works REST API 与管理端处理器 |
| `internal/http/templates/works.html` | 新增 | 作品搜索、筛选、索引、列表与创建页面 |
| `internal/http/templates/work.html` | 新增 | 作品详情、编辑、删除与曲目关联管理页面 |
| `internal/http/assets/admin.css` | 修改 | 作品列表/详情、索引条及响应式样式 |
| `internal/metadata/tieup_test.go` | 新增 | Tie-up 推导规则测试 |
| `internal/storage/works_test.go` | 新增 | CRUD、搜索、索引、自动/手工关联及重扫幂等测试 |
| `internal/http/works_test.go` | 新增 | Works API 创建、检索和 404 测试 |

**技术决策**：
| 决策 | 选择 | 理由 |
|------|------|------|
| 作品模型 | 可扩展基础模型 | 预留译名、reading、海报及外部 ID，便于 Phase 4 Bangumi 对齐 |
| 自动推导策略 | 积极推导 + 通用词过滤 | 同时分析显式标签、专辑和目录，在提高自动化率的同时抑制明显垃圾条目 |
| 手工关联保护 | `work_tracks.source=manual/auto` | 重扫仅重建 auto 关联，不删除或覆盖管理员手工关联 |
| 跨形态搜索 | reading 字段 + 平片假名双变体 | 无大型依赖；罗马音依赖已有 reading 数据，保持纯 Go 和轻量部署 |
| Web 技术 | 现有 Go SSR 模板 | 项目没有 Node 前端构建链，沿用嵌入式模板、PRG、CSRF 与现有 Roon 风格 |

**验证结果**：`go test ./...` 与 `go build ./...` 全部通过。

---

### Phase 4 实施记录 (已完成)

**变更文件清单**：
| 文件 | 变更 | 说明 |
|------|------|------|
| `internal/storage/migrations/012_phase4_enrichment.sql` | 新增 | 来源设置安全升级、统一任务、HTTP 缓存、外部资料、候选审核、provenance 与 credit 来源表 |
| `internal/storage/enrichment.go` | 新增 | 任务/缓存/候选/保守补全、来源化 credits、重扫恢复及中断任务恢复 |
| `internal/enrichment/phase4.go` | 新增 | VGMdb、Bangumi、MusicBrainz 关系 provider 与统一后台 `StartRun` |
| `internal/enrichment/manager.go` | 修改 | 自动 enrichment、重启任务恢复及 Phase 4 并发控制 |
| `internal/metadata/reader.go` | 修改 | 原始 ID3v2.3 IPLS / ID3v2.4 TIPL/TMCL 安全解析和结构化 involved people |
| `internal/storage/library.go` | 修改 | 扫描写入 file-tag involved people 并恢复远程 credits |
| `internal/http/enrichment.go` | 新增 | HTTP 202 任务 API、任务查询、作品/艺术家关系审核及管理端处理器 |
| `internal/http/templates/enrichment-jobs.html` | 新增 | Enrichment 任务进度、结果和候选审核页面 |
| `internal/http/templates/metadata-settings.html` | 修改 | VGMdb、Bangumi 来源设置 |
| `internal/metadata/involved_people_test.go` | 新增 | ID3 involved people 编码、版本、边界和容错测试 |
| `internal/storage/enrichment_test.go` | 新增 | migration 升级、缓存、幂等、人工保护、重扫恢复和候选测试 |
| `internal/enrichment/phase4_test.go` | 新增 | 编号规范化、VGMdb 精确匹配、Bangumi 评分/自动确认和任务测试 |
| `internal/http/enrichment_http_test.go` | 新增 | 鉴权、HTTP 202、任务查询和候选决策测试 |

**技术决策**：
| 决策 | 选择 | 理由 |
|------|------|------|
| VGMdb 匹配 | 规范化 catalog number 后精确匹配 | 不采用标题模糊写入，避免错误 credits 污染曲库 |
| 写入策略 | 仅补空字段，`user_*` 与本地已有值优先 | 遵守本地标签/人工编辑优先原则 |
| 艺术家关系 | 自动生成候选、人工确认 | 禁止远程关系直接写 `merged_into_artist_id`，避免破坏性误合并 |
| Bangumi 对齐 | 标题、类型、年份评分；≥90 自动确认 | 高置信自动化，冲突结果进入审核 |
| TIPL/TMCL | 直接读取原始 ID3 帧 | `dhowden/tag` 的通用文本解析会丢失 NUL pair 边界 |
| Credits 来源 | `track_artist_sources` + provenance | file-tag 可替换，远程来源可追溯且重扫后自动恢复 |
| 缓存 | 状态码与响应体一并缓存 | 正常结果和 404 均在缓存期内避免重复 HTTP 请求 |
| 任务执行 | 单 Phase 4 后台任务 + 持久化进度 | 防并发重复执行，重启后将遗留 running 标记失败 |

**验证结果**：`go test ./...` 与 `go build ./...` 全部通过。

### Phase 4 修订记录（元数据增强收敛与作品关联“专辑为主”）

**第十轮（批次 5 系列建议与数据修复，migration 031）**：
| 决策 | 选择 | 理由 |
|------|------|------|
| D52 跨媒体建议采信范围 | A：只采信双向登记且构成白名单关系对的边（游戏↔动画、衍生↔主线故事、番外篇↔主线故事、总集篇↔全集、不同演绎↔不同演绎）；白名单为常量可扩充；联动/世界观/角色出演/其他/外传/合集/不同版本一律不采信 | 单向登记（蛋仔派对↔鬼滅）与泛关系噪音大（R2） |
| D53 建议粒度 | A：作品对作品；接受时按两部作品当时的归属决定新建系列 / 加入已有系列 / 合并两个系列 | 归属可能在接受前变化 |
| D55 决定记忆 | A：接受与拒绝都按 Bangumi subject 对记入 `work_series_suggestion_decisions`，永不再建议 | R3 |
| D56 锁定作品 | A：被锁定（拆出/解散）的作品不参与建议 | 用户意图永久有效 |
| D57 合并成员处理 | A：被吸收系列成员一律改 manual 并清锁；保留方 auto 成员不动 | 合并即人工意图 |
| D58 人工合并名字 | A：可指定任一方名字或新填；单方 manual 默认沿用；双方 manual 未指定返回 ErrSeriesTitleConflict；双方 auto 保留成员多的一方（平数取 id 小），名字保持 auto | 保住用户改过的名字 |
| D59 B1 修复 | A：ApplyAutoSeries 的重合度只计算未锁定的 auto 成员（D41 冲突检测同口径） | 手动加入的成员是钉在该系列的用户意图，不应把系列“认领”走 |
| D60 sequel 建议 | A：与系列内 manual 成员存在双向续集/前传关系时生成 kind='sequel' 建议 | 补足合并后被吸收链的新季不会自动加入的局限 |
| D61 手动新建系列 | A：至少 1 部作品；名字可空（title_source='auto'，跟随代表作） | 与自动系列语义一致 |
| D62 D-9 控制字符 | A：rawFirst 统一清理（按 NUL 切分取首个非空段、C0 清除、\t 转空格）；存量数据由 Migrate 末尾的 Go 端幂等修复 cleanAlbumTagControlChars 处理（实测 SQL replace 清不掉 NUL）；ImportTrack ON CONFLICT 只在旧值含控制字符时允许空值覆盖 | D-9 关闭 |
| D63 D-18 抑制解除清 miss | A：AddWorkAlbum 与 clearManualSuppressions 删除抑制时同事务删除受影响曲目的 bangumi miss（invalidateTrackBangumiMisses）；不把抑制状态并进指纹 | D-18 关闭；并指纹会让 ConfirmTrackSubjectCandidate 的指纹比较失效 |
| 建议生成时机 | enrichBangumiSeries 的 BFS 与 ApplyAutoSeries 完成且未中止时执行；type 2 关系复用 BFS 的 runMemo/缓存，type 4 按需拉取（同样节流/熔断）；建议库另建 suggestLibrary，不动 BFS 的 type-2 library | 游戏不能被续集链拉进自动归组 |
| 建议落库 | 单事务 upsert；删除“两端本轮都拉取成功但不再产生”的旧建议；拉取失败的作品旧建议保持不动；未变化的行不刷新 updated_at | 幂等 + 部分失败不丢数据 |
| 第十轮 reviewer 修订（M1～M3/L4/L6/L7） | ReplaceSeriesSuggestions 事务首句即为写操作（顺手删除任一端被锁定的建议，M1）；INSERT 改 INSERT…SELECT…WHERE NOT EXISTS 在事务里复核决定/锁/同系列（M2）；processed 集合改走 json_each(JSON) 避免变量上限（L4）；AcceptSeriesSuggestion/mergeWorkSeriesTx/createWorkSeriesTx/addWorkToSeriesTx 首条语句均为写操作（L6）；合并/新建/加入后同事务删除同系列待审建议（L7）；Accept 重读发现锁定返回 ErrSeriesSuggestionStale 且不清锁；sequel 建议按 D67 收窄 | 批次 5 reviewer APPROVE 条件 |
| D64 效果图的地位 | A：效果图只作参考，不作为逐像素验收标准；实现后截图存档（.local/mockups/impl46/） | 不停下来等确认 |
| D65 4.6 交付节奏 | A：分两批：批次 5 数据与逻辑、批次 6 管理页 UI | 数据层与 UI 解耦 |
| D66 B1 口径的已知副作用 | A：保持修复后口径。manual-only 系列不被 auto 分量认领：同轮进来 ≥2 部新季会另建 auto 系列，链暂时分成两个系列，靠 sequel 建议（D67a）+ 人工合并修复；只凭 manual 成员与改名系列重合不再触发 D41 冻结 | 手动成员是用户意图 |
| D67 sequel 建议收窄 | A：双向互为续集/前传只在 (a) 至少一端是某系列 manual 成员，或 (b) 两端都是 type 4 时生成 kind='sequel' 建议；普通 type 2 续集对由自动归组负责（本轮未归组的下一轮自愈）。**D60 已被 D67 取代**（D60 的“manual 成员”场景即 D67a） | 普通续集建议会刷屏且与自动归组重复 |

**第十一轮（批次 6 UI：系列建议 Tab + 系列管理页 + D51 类型筛选 + 批次 5 遗留 L-A～L-E）**：
| 决策 | 选择 | 理由 |
|------|------|------|
| 系列建议 Tab（D54） | /admin/work-review 第 4 个 Tab“系列建议”（?tab=series）：每条建议显示两部作品（海报/标题/类型/年份/链接）、双向关系中文标签与接受后效果预览（新建系列/把 B 加入《X》/合并《X》与《Y》）；不支持分组切换与 albumId 过滤（与作品对齐 Tab 相同）；同一作品出现在多条建议时按作品分组收拢 | R6 集中审核；避免 FGO 类作品刷屏 |
| 合并命名 UI（D58） | 双方都改过名：单选“保留《X》/保留《Y》/新名字（文本框）”；只有一方改过名：默认保留该方（隐藏域提交，显示提示）；双方都是自动名：不显示命名选项，按 D58 自动处理；ErrSeriesTitleConflict 回到 Tab 提示“两个系列都改过名，请选择合并后的名字”；ErrSeriesSuggestionStale 提示“建议已失效，已移除”；不存在 404；均不返回 500 | 冲突必须让人选择 |
| 系列管理页 | GET /admin/series（搜索系列名或成员名、分页、每行：系列名+“已改名”标注、成员数、类型分布“动画 3 · 游戏 1”、代表作海报、新建入口）；GET /admin/series/{id}（改名表单、成员按类型分组（D51）、来源（自动归组/手动加入）、年份、移出按钮（旁注“移出后该作品不再自动归组”）、加入作品（作品搜索自动补全 + “将从《Z》移入”提示）、合并系列（系列搜索自动补全 + D58 命名单选）、解散（D39 提示））；POST /admin/series、/{id}/members、/{id}/members/{workId}/remove、/{id}/merge；改名/解散复用既有 handler + returnTo；全部管理员 + CSRF + safeAdminReturnTo，错误友好提示不返回 500 | 完整管理入口 |
| 系列搜索接口 | GET /admin/options/series?q=（管理员 JSON，最多 10 条），做法与 /admin/options/works 相同（参数化、防抖、textContent 渲染）；/admin/options/works 增加可选 withSeries=1 在标签里标出“将从《Z》移入” | 合并目标与加入作品的选择器 |
| D51 类型筛选显示 | /works 按类型筛选：系列行“共 N 部，其中 K 部为<类型>”、海报取第一部符合类型成员、展开区只列符合类型成员（ListWorksFolded 批量查询后在 Go 端收窄，无 N+1，计数/分页/筛选同口径）；系列展开区、作品详情页系列条、系列管理详情按类型分组显示，单一类型省略分组头 | 类型是筛选不是层级 |
| 批次 5 遗留修复 | L-A：PendingWorkReviewCounts 系列建议计数加锁定过滤（与列表同口径）；L-B：ReplaceSeriesSuggestions 对被复核条件拦下的已有行当场 DELETE（不留到下一轮）；L-C：条目详情 404 也计入 seedResolved（确定性解析）；L-D：RejectSeriesSuggestion 首条语句为写操作；L-E：TestSeriesSuggestionTaintedRunProducesNothing 改为第一轮即失败真正验证 D67（含变异验证） | reviewer 遗留 Low |
| 导航与入口 | “管理”组加入“系列管理”（作品关联审核旁）；作品详情页系列条加“在系列管理页打开”链接；Tab 角标与导航角标都含系列建议数 | 可发现性 |
| e2e | 批次 6 种子扩展进 45441 独立实例（不污染 45439）：加入/新建/合并需选名/拒绝四条建议 + 两个改名系列；用例覆盖接受新建/加入、拒绝、选名合并、管理页全生命周期、/works 类型筛选；截图用 E2E_SCREENSHOTS=1 输出 .local/mockups/impl46/（声明在最前，与批次 3 同一处理；依赖声明顺序，不可开启 retries） | 双实例隔离机制不变 |
| 第十一轮 reviewer 修订（H1/M1～M3/L1～L8） | H1：合并命名单选增加“自动（按规则）”（空串交存储层按 D58 处理），MergeWorkSeries 返回保留方 id，合并后跳到 /admin/series/{keptID}（returnTo 指向被删方时同样纠正）；M1：admin.js/router.js 的 ADMIN_NAV_KEYS 加入 series（管理组自动展开）；M2：base.css 的 checkbox 规则同时覆盖 input[type=radio]；M3：建议卡关系标签改自然语言（relationAB 描述 B：“《B》是《A》的<关系>”，相同则“互为”；胶囊按 RelationBA ↔ RelationAB），种子方向修正；L1 分组计数用 counts[key]；L2 超 200 条提示；L3 withSeries 排除本系列；L4 改名/解散带 returnTo 时友好提示；L5 内联样式入 CSS；L6 .dissolve-hint 间距；L7/L8 e2e 注释 | 批次 6 reviewer BLOCK 修复 |

**第十二轮（外部 API 礼貌性加固）**：
| 决策 | 选择 | 理由 |
|------|------|------|
| 429/503 退避 | cachedJSON 与 doSourceJSON（原 doJSON）识别 429 与带 Retry-After 的 503：Retry-After 支持秒数与 HTTP 日期，缺失/非法默认 60 秒、上限 10 分钟；按来源（bangumi/musicbrainz）在 Manager 上记录 blocked-until（各自由 bangumiMu/mbMu 保护），waitBangumiRateLimit/waitMBRateLimit 等待到 max(间隔点, blocked-until)，等待可被取消；返回可识别的 *RateLimitError（包裹 ErrRateLimited，含来源/状态码/退避时长）；限流响应不写缓存（沿用非 2xx/404 不缓存） | 被限流后继续按原间隔打满来源只会延长封禁 |
| 运行循环策略 | (a) 遇到 ErrRateLimited 立即结束本轮：phase4 条目循环与系列阶段、StartAll 艺术家匹配循环都 FinishRun(failed, 中文信息“Bangumi 限流（429），已停止本轮，约 X 分钟后可重试”)；限流不计入 Failed 与连续失败熔断计数 | 退避可达 10 分钟，占住唯一后台 worker 等待重试收益低；任务幂等可稍后重跑，缓存命中使重跑代价小 |
| 系列阶段传播 | series_bangumi.go 三处拉取失败点（种子详情、BFS 关系、type-4 建议）遇 ErrRateLimited 直接返回，不累加 consecutiveFailures，由 executePhase4Run 统一收尾 | 熔断信息保持准确 |
| 艺术家匹配传播 | matchArtist 内 MB 搜索/TaggedMBID 查询/详情补查与 Last.fm 查询遇 ErrRateLimited 直接返回；Last.fm JSON 错误码 29（rate limit）同样视为限流 | StartAll 循环只认返回值 |
| 海报回填 | downloadPublicImage 遇 429 返回 RateLimitError；StartWorkPosterBackfill 遇到即停止本次回填并记日志，下一轮自愈 | 回填是可重试的幕后任务 |
| 统一 User-Agent | 新增 userAgent(setting)：<App>/<Version> (<Contact>)，空 Contact 回退项目仓库地址 https://github.com/lux032/032music-server（替换 “self-hosted”/“local-self-hosted-instance” 等不可联系值），空名称/版本回退 032-Music-Server/dev；Bangumi(cachedJSON)、MusicBrainz(mbRequest)、Last.fm 歌手信息、Wikidata/Wikipedia/Spotify、海报与歌手图片下载全部改走该函数 | MusicBrainz/Wikimedia 要求 UA 可联系到使用者 |
| Wikimedia UA 设置来源 | metadataUserAgent(ctx) 统一读 MusicBrainz 数据源设置：管理页唯一可编辑联系方式的表单就在 MB 卡片，且这些辅助请求都是为 MB 身份解析关系的 | 调用处拿不到各自的 setting，避免改一串函数签名 |
| Last.fm scrobble UA | internal/lastfm 默认 UA 的 “self-hosted scrobbler” 同样是不可联系值，一并统一为仓库地址 | 任务允许同值统一 |
| Bangumi 间隔配置 | 环境变量 MUSIC_SERVER_BANGUMI_INTERVAL_MS（默认 500ms，下限 200ms、上限 10000ms，非法/越界回退默认并告警），在 enrichment.New 读取；不放进数据源设置（需迁移且间隔是部署级调参而非数据源属性） | 沿用 MUSIC_SERVER_* 模式，改动小、无迁移 |
| 等待可注入 | Manager.sleep 字段（默认 sleepContext）承接限流等待，测试注入后断言退避时长而无需真等 | 退避默认 60 秒起，真等不可行 |

**验证**：新增 TestParseRetryAfter、TestCachedJSONRateLimited（含不写缓存/退避等待/取消）、TestCachedJSONServiceUnavailableRetryAfter、TestPhase4RunStopsOnRateLimit（断言 429 后请求数=1）、TestArtistMatchStopsOnRateLimit、TestSeriesGroupingStopsOnRateLimit、TestUserAgentFallback、TestCachedJSONUserAgent、TestMusicBrainzRequestUserAgent、TestWikidataUserAgentAndRateLimit、TestSpotifyImageRateLimit、TestLastFMError29RateLimited、TestDownloadPublicImageUserAgentAndRateLimit、TestParseBangumiIntervalMS、TestBangumiIntervalFromEnv、TestNewManagerBangumiIntervalFromEnv；变异验证：去掉 cachedJSON 退避分支→限流测试失败、UA 回退改 “self-hosted”→UA 测试失败；`go test ./...` 与 Playwright 全量通过。

| 第十二轮 reviewer 修订（H1/H2/M1/M2/L1～L6） | H1：RefreshConfirmedArtistImage/RefreshArtistBiographies 各拉取点遇限流立即返回，matchArtist 三处调用点（图片回填、简介刷新、自动确认后缓存）透传；H2：wikipediaSummary REST 限流不再回退 action API；M1：waitBangumiRateLimit/waitMBRateLimit 退避期内立即返回 RateLimitError（不再持锁睡眠），交互式 handler（手动匹配/确认/刷新简介）显示中文提示“X 限流中，约 N 分钟后再试”；M2：parseRetryAfter 相乘前先判上限、Atoi ErrRange 取上限；L1：README 按实际行为改写（仅 Bangumi/MB 记录退避）；L2：UA 的 App/Version 空白转“-”；L3：Contact 控制字符转空格且管理页拒绝含控制字符的联系方式；L4：设置页提示与实际行为一致；L5：TestArtistMatchStopsOnRateLimit 增至 2 位艺术家；L6：仓库地址常量为 internal/appmeta.RepoURL，enrichment 与 lastfm 共用 | 批次 reviewer BLOCK 修复 |

**验证**：新增 TestArtistMatchRefreshStopsOnRateLimit、TestArtistMatchBiographyRateLimitStopsRun、TestWikipediaSummaryRateLimitStopsFallback、TestWaitRateLimitReturnsImmediatelyDuringBackoff、TestMatchArtistRateLimitNotice、TestConfirmArtistMatchRateLimitNotice、TestRefreshArtistBiographiesRateLimitNotice、TestSaveMetadataSettingsRejectsControlCharContact；TestCachedJSONRateLimited 改为断言退避期立即返回且 sleep 不被调用；变异验证：去掉刷新路径限流返回→H1 两测试失败、恢复持锁睡眠→M1 两测试失败；`go test ./...` 与 Playwright 全量通过。

| 第十二轮 reviewer 第二轮修订（H-1/M-1/L-1～L-3） | H-1：musicBrainzLookup 里 wikidataImage/spotifyImage 的限流错误不再吞掉（Spotify 循环遇限流立即停），调用点（matchArtist 两处、RefreshConfirmedArtistImage）均已透传；M-1：bangumi.go 自动确认后海报下载遇限流返回错误停本轮（确认已落库，海报由回填补）；L-1：确认匹配遇限流提示“已确认匹配；<来源>限流中，图片/简介可稍后手动刷新，或在下次自动匹配时补全”；L-2：sourceDisplayName 补“歌手图片/作品海报/外部来源”，手动匹配先确认后限流时提示“已自动确认匹配，但图片/简介因<来源>限流暂未获取”（matchArtist 确认后透传改返回 AutoMatched=true 的 MatchResult）；L-3：两个 run 级测试的注释改为说明区分点是 status+消息 | 第二轮 reviewer BLOCK 修复 |

**验证**：新增 TestWikidataImageRateLimitStopsStartAll（Wikidata 429 停轮、第二位不再请求 Wikidata）、TestSpotifyImageRateLimitStopsLoop（Spotify 循环遇 429 停）、TestPhase4WorkPosterRateLimitStopsRun（海报 429 停轮且确认已落库）、TestMatchArtistConfirmedThenRateLimitNotice；变异验证：去掉 wikidata 限流返回→TestWikidataImageRateLimitStopsStartAll 失败；`go test ./...` 与 Playwright 全量通过。

| 第十二轮 reviewer 第三轮修订（Low-1～Low-3） | Low-1：提示文案统一——来源名与“限流”之间一律空格、结尾不加句号；图片下载类来源在提示里统称“图片源”（noticeSourceName），运行记录仍保留“作品海报 限流（429）”；手动匹配确认后限流的提示补“约 N 分钟后可重试”（RateLimitNoticeParts）；Low-2：StartAll 限流分支在 result.AutoMatched 时先 matched++ 并按 index+1 更新进度再结束本轮；Low-3：SetMusicBrainzBaseURL 注释标明只供测试、只能在管理器空闲时调用 | 第三轮 reviewer APPROVE 后的 Low 收尾 |

**验证**：新增 TestArtistMatchConfirmedThenRateLimitCountsMatch（确认后限流仍计 Matched=1/Processed=1）；`go test ./...` 与 Playwright 全量通过。

**第九轮（批次 4 自定义图片，migration 030）**：
| 决策 | 选择 | 理由 |
|------|------|------|
| D28 WebP | A：`golang.org/x/image` 纯 Go 解码；上传校验与缩略图都支持 WebP（缩略图允许列表含 webp，YCbCr/NYCbCrA 走 sourceRGBA 快速路径或 At() 回退，输出统一为 JPEG） | 不引入 cgo 依赖 |
| D29 图片尺寸 | A：每边 ≤8192 且总像素 ≤4000 万；DecodeConfig 先查尺寸、超限直接拒绝，之后才完整解码一次验证完整性 | 防解码炸弹 |
| D30 自定义图片存储 | A：专辑封面进 `artworks(source_type='custom')`（重建为 AUTOINCREMENT 保证每次上传新 artwork id），歌手图片用新表 `artist_custom_images` | URL 即缓存键，id 不复用 |
| D31 专辑清理 | A：custom 行随专辑 ON DELETE CASCADE；磁盘文件 GC 触发时机：上传/替换/恢复默认时即时清理无引用文件 + 1 小时孤儿扫描，另在服务启动与每次扫描完成后各跑一次（覆盖合并、级联删除留下的孤儿） | 不删并发刚写入的文件 |
| 选封面规则 | `ORDER BY (source_type='custom') DESC, is_primary DESC, id` 收拢进 `albumArtworkURLSQL`，替换全部 9 处分散位置（专辑详情/列表 hydration/曲目列表/TrackByID/歌手曲目/作品页/Sync 专辑/Sync 曲目/歌单封面） | 单一收口点，自定义封面全入口优先 |
| 歌手图片版本 | `artistImageURLSQL(alias)`：自定义优先且带 `?v=<哈希前12位>`；歌手合并按 merged-from 继承（不改行、回滚安全），自动刷新只写 `artist_image_cache` 永远压不过自定义 | 缓存自然失效 + 合并行为与专辑一致 |
| 上传安全 | MaxBytesReader 11MB → ParseMultipartForm → CSRF；魔数 + DecodeConfig 判格式（不信扩展名/Content-Type）；10MB 上限；仅管理员；前端在上传前检查 file.size 超 10MB 即提示并阻止提交（admin.js 外部脚本） | 伪造扩展名、GIF、超尺寸、超大文件均被拒 |
| D50 歌手合并后的自定义图片继承 | 读取优先级：目标自己的自定义图 > 被合并进来的源歌手自定义图（多个时按 artist_id 最小者）> 目标自己的自动图（cache）；继承也算 HasCustomImage，页面注明"来自已合并的歌手 X"；在目标歌手页"恢复默认"同时删除目标与全部 merged-from 源歌手的自定义行（合并即同一人），恢复为自动图；撤销合并后源歌手的自定义图不恢复（已知行为） | 与专辑合并规则一致且回滚安全 |
| M3 自定义图片只存文件名 | 数据库（artworks.source_path、artist_custom_images.file_path）只存 `<hash>.<ext>` 文件名，读取/GC 时按 customImagesDir() 拼接；数据目录换写法/迁移不影响引用比对 | 绝对路径入库会导致目录移动后 GC 误删 |
| M4 上传并发 | App 级 customImageMu 串行"存文件 → 提交行 → 清理"全程；去重命中已存在文件时 os.Chtimes 刷新修改时间 | 防 GC 在提交窗口误删 |
| M1 文件表单判定 | router.js 改用 `form.elements` 按表单归属判定（form= 外部关联的输入归属于上传表单），主编辑表单恢复 PJAX | DOM 包含 ≠ 表单归属 |
| 文件布局 | `<数据目录>/custom-images/<sha256("custom:"+内容)>.<ext>`，临时文件 + rename 原子写入，相同内容只存一份（命名空间哈希避免与扫描缓存哈希撞唯一索引） | 绝不改写音乐文件 |
| PJAX | 上传表单文件输入经 `form=` 外部关联，额外加 `data-no-pjax` 保证原生提交；重置表单随既有 PJAX 流程 | router 的 `querySelector('input[type=file]')` 看不到外部关联输入 |

**第八轮（批次 3 UI：集中审核页 `/admin/work-review` + 4.5.5 专辑页 + 4.5.6 作品页 + 作品列表系列展开）**：
| 决策 | 选择 | 理由 |
|------|------|------|
| D27 审核页路径 | A：审核区整体迁到 `/admin/work-review`，`/admin/enrichment` 只保留任务列表、“待审数量 → 去审核”链接以及艺术家关系候选（D44） | R6：所有作品关联审核集中在一页，与任务监控解耦 |
| D42 作品列表系列展开 | A：在系列折叠行下方就地展开成员卡片（效果图 10），非系列作品保持原卡片样式 | 保持单层系列直观浏览，列表页与作品页系列名一致（D25） |
| D43 悬停卡片只读 | A：悬停卡片仅用于查看，不提供解除或修改用途操作；管理操作统一放在“编辑专辑”抽屉和作品页 | 避免悬停态误操作，保持管理入口清晰 |
| R4 胶囊与列表用途展示 | 专辑标题区胶囊用途取自曲目级 `work_tracks`（多用途合并），OST 专辑显示 `OST`，仅专辑级 `other` 不显示用途；曲目行显示 `◆ 用途` 图标与多作品数字角标；D47 专辑级 ost 与曲目级用途合并显示（如 "OST · OP"），D48 OST 身份只认 `album_works.role='ost'`，D49 同一（曲目，作品）的全部用途合并显示 | 严格遵循角色记在曲目上、专辑级仅表示关联与 OST |
| 通用悬停卡片组件 | 服务端直出 DOM，外部 JS 事件委托（兼容 PJAX）；悬停 ~150ms 打开、移开 ~300ms 关闭、鼠标可移入、Tab 聚焦打开、Esc 关闭，定位限制在主内容区 | 满足 CSP 无内联脚本约束与零额外请求 |
| D-17 消除曲目候选 N+1 | `trackCandidateSelect` 直接通过 `bangumiTrackArtistSQL` 和 `albumArtworkURLSQL` 带出歌手与封面 | 省去审核页逐条 `TrackByID` 查询 |
| 封面公共 SQL 片段 | 新增 `albumArtworkURLSQL`，统一专辑页、作品页、审核页新增查询的取封面逻辑 | 为批次 4 自定义封面预留单一收口点 |

**第八轮修复（批次 3 UI reviewer 清单）**：
| 决策 | 选择 | 理由 |
|------|------|------|
| D44 艺术家关系候选 | 审核区保留在 `/admin/enrichment`（不迁到 `/admin/work-review`） | 艺术家关系不属于作品关联（P1=A） |
| D45 OST 口径 | 只认 `album_works.role='ost'`；`album_type=soundtrack` 不推出 OST（胶囊与作品页角标一致） | R4 + P2=A |
| D46 整张关联专辑的曲目用途 | 作品页“关联专辑”卡片下新增“曲目用途”区（曲名/用途/来源），可解除（RemoveWorkTrack 写抑制） | P3=A + R3 |
| R2 单曲口径 | “单曲”只看 `album_type='single'`，去掉 `TrackCount<=4` 推测 | 不加本地推测启发式 |
| 分组渲染 | work-review 模板按 AlbumGroups/WorkGroups 渲染分组卡片；排序前先复制 slice；作品对齐 Tab 始终平铺（与专辑无关） | 分组真正生效 |
| 导航角标 | 每请求在 `App.chromeFor` 用请求 context 查询一次写入 Chrome；删除模板函数里的 `context.Background()` 查询 | 不吞错误、不脱离请求生命周期 |
| 专辑过滤 | `albumId` 过滤下推到 SQL（`status='candidate' AND album_id=?`）；过滤状态下 Tab 计数显示过滤后的数量并在页面标注 | 不再 LIMIT 200 后在 Go 里筛 |
| D1 手工添加 role | 作品页/专辑抽屉的专辑级表单只提供 other/ost；`handleAddAlbumWork`/`handleAddWorkAlbum` 服务端只接受 ost\|other，其他值 400 | 专辑级只有 ost/other |
| 抽屉搜索框 | Enter 拦截（JS preventDefault）不提交“保存覆盖信息”；搜索内容变化清空已选 workId | 防误提交 |
| e2e 可移植 | 种子移到 `e2e-global-setup.mjs`（env GO → PATH → 平台兜底，失败即报错）；双 fixture 实例隔离：45439 共享（20 轨 1 专辑原始假设）、45441 批次 3 独立（种子只写入该实例）；spec 仅桌面项目运行；截图用 `E2E_SCREENSHOTS=1` 门控；删除 `scripts/run_batch3_e2e.js` | 不硬编码本机路径，种子不污染其他 spec |

**第四轮（Bangumi 音乐条目反查，migration 026，实施中）**：
| 决策 | 选择 | 理由 |
|------|------|------|
| 自动确认 | 艺术家一致、唯一具体角色作品、≥80 且领先≥20 | 同名音乐条目及泛关系不可信 |
| 用户意图 | album_work_suppressions + 候选状态 | 拒绝/解除及专辑合并后不复活 |
| 调度 | 专辑反查先于作品对齐，普通批量只处理动画迹象 | 降低 API 浪费与误匹配 |
| 375293 实测决策 | Bangumi 游戏侧标注片头曲，唯一具体角色作品自动确认 IS6 → 269645 幻想収束；专辑级记 other，因无同名曲目不写曲目行 | Bangumi 数据为权威，不加原创专辑本地启发式否决；逐曲反查归 4.5.4 |
| 角色落点 | 专辑级只写 ost / other，具体角色写入同名曲目的 work_tracks（source=bangumi） | 遵循 R4；版本曲目仅按 InferTrackType 已有后缀范围继承，重录版本后缀不去除 |
| 设计依据 | `docs/design/works-association.md` v1.0 | 统一人工 > Bangumi > 本地规则的优先级与用户抑制语义 |
| 抑制匹配口径 | 标题身份与季数一致即命中，忽略作品类型（恢复 migration 025 语义） | 用户解除意图跨后续自动类型修正保持有效；anime/game 类型纪律仅在全局作品解析层执行 |

**第七轮（批次 2 系列自动层，migration 029）**：
| 决策 | 选择 | 理由 |
|------|------|------|
| D21 自动系列关系 | A：只沿续集/前传 | 总集篇、番外篇、衍生、不同世界观、主线故事等关系一律不走 |
| D22 Bangumi 归组 | A：照 Bangumi 归组，用户可拆出且系统记住（work_series_locks） | R2/R3 |
| D23 库外中间条目 | A：允许连通；离开库内作品 ≤3 跳、每组 ≤40 节点、有环检测 | 缺中间季时仍能归组，同时限制请求量 |
| D24 系列模型 | A：4.6 单层模型，一部作品最多属于一个系列 | 简化展示与归属语义 |
| D25 系列代表 | A：库中最早播出的作品（年份 → Bangumi 日期 → id），系列名默认用其标题；用户改名后自动流程不覆盖（title_source） | 确定性与可预测性 |
| D38 手动创建锁类型 | A：CreateWork 明确指定类型时 type_locked=1；自动流程新建的作品不锁 | 用户手动建的类型不应被 Bangumi 纠正覆盖 |
| 解散实现 | 全体成员写锁定（D39=A，不用“用户解散”标记）；已知副作用：以后新入库两部以上同一续集链的作品可能单独组成只有新作品的系列，用户手动处理 | 锁定是现成的单作品意图机制；全锁定的分量永远不会重建系列，不需要额外状态 |
| 请求失败 | 失败的分量本轮跳过，碰到失败分量访问过节点的分量同样跳过（B2）；本轮未处理的 auto 成员一律保留；熔断（连续 5 次失败）中止阶段并返回错误 | 增量重跑自愈，部分失败不拆碎已有系列、不丢用户改名 |
| 系列合并认领 | D41=A：交集降序 → 分量大者优先 → 改名系列优先 → 系列 id 小者优先；分量与两个及以上改名系列有交集时不自动合并，本轮跳过并记录 provenance（D41-A1） | 用户改过的名字优先级最高，冲突留给用户 |
| manual 成员口径 | P3=A：manual 成员等同于锁定，不参与自动归组，也不会被自动流程挪动或删除；成员写入分两段（先删后插），跨系列移动不违反 work_id 主键（B1） | R3：人工 > Bangumi |
| 系列阶段调度 | 属于 `all` 与新 `works` 范围，作品对齐阶段之后执行 | 刚绑定的作品当轮即可归组 |
| 新建作品类型 | D40=A：表单默认“未指定（由 Bangumi 纠正）”，提交空值 → 默认类型 other 且不锁定；主动选择类型才锁定（D38） | 不因表单默认值误伤自动纠正 |

**第六轮（批次 1 数据收尾，migration 028）**：
- 新增作品类型锁与 Bangumi 权威类型纠正；剧场版映射 movie，OVA/WEB 等 type 2 映射 anime，type 4 映射 game。
- 修复专辑解除时的曲目行清理、全抑制结果记 miss、人工接受抑制口径、Bangumi 动画大类复用、含竖线抑制键解析、专辑搜索 25 条分页、逐专辑失败隔离及专辑页解除提示。
- 批次并入并关闭 D-2、D-5、D-11～D-14；4.5.3 明确采用 4.6 单层系列模型。
- D35=A：人工接受对已绑定 Bangumi 的 anime/movie 按大类解除标题抑制，game 独立、other 通配；未绑定作品仍严格类型。
- D36=A：旧格式标题抑制键在人工接受时继续按标题解除，整条目 `bangumi:<m>` 拒绝仍保留。
- D37=A：只有用户实际修改类型才锁定；Web 表单用 originalType 防止并发自动纠正被旧表单覆盖，API 与数据库当前类型比较。
- 新增待办：曲目未命中指纹暂不包含抑制状态；解除抑制后可能需等待重查间隔到期才重新查询。

**第五轮（曲目级反查，migration 027）**：
| 决策 | 选择 | 理由 |
|------|------|------|
| D8 歌手不一致 | A：条目名一致但歌手不一致或没有 primary 歌手时记为未命中，不进审核 | 同名不同歌手的条目不可信，不应占用审核队列 |
| D9 本地行 | B：Bangumi 曲目级确认后删掉该曲目的 auto 行；本地推导遇到 bangumi/manual 行时跳过 | 来源优先级人工 > Bangumi > 本地规则 |
| D10 用途 | B：多曲名候选接受时可以改用途；改过的记为 manual，没改的记为 bangumi | 用户改过的决定不再被下一次自动确认覆盖 |
| D11 调度 | A：扫描后全量任务包含曲目级 | 专辑级之后立刻补曲目级 |
| D12 重查 | B：未命中重查间隔 max(cacheDays,90) 天；发售 180 天内的专辑为 7 天 | 新专辑的条目补登记更快，旧专辑不重复打接口 |
| D13 跳过 | A：只跳过 manual/bangumi 的 ost 专辑 | 合辑与被抑制的专辑仍逐首查 |
| D14 多条目 | A：多个条目指向同一（作品，用途）时自动确认，否则进审核 | 同一登记被多个条目重复不需要人工 |
| 人工接受口径 | 曲目级按决策 A（解除类型一致的标题键加 `bangumi:*:<w>`，整条目拒绝 `bangumi:<m>` 依然拦住） | R1 人工优先；专辑级待 D-13 统一 |




**第一轮（已提交 `ba582c5`）**：
- VGMdb 专辑增强下线（vgmdb.info 镜像 TLS 失败、vgmdb.net Cloudflare 403），migration 022 禁用设置，历史数据保留；MusicBrainz 艺术家关系增强移出流程（确认后无读取方）。
- Bangumi：修复 `name_cn` 未解析与自动确认永不触发；NFKC 统一全半角/波浪线，以日文原名**唯一严格匹配**自动确认；按作品类型映射搜索分类（anime[2]、game[4]、movie[2]、other[2,4]，drama/commercial 跳过）；请求节流、先解析后缓存。
- 已关联/待审核/无结果作品普通运行不重复请求；改标题或类型后自动重查；增强任务与歌手匹配任务支持手动停止（migration 023、024）。

**第二轮（FLAC，已提交 `32930e0`）**：`readFLAC` 跳过前置 ID3v2 标签。

**第三轮（作品关联“专辑为主”，migration 025）**：
| 决策 | 选择 | 理由 |
|------|------|------|
| 关联单位 | 专辑决定作品（专辑级显式标签 → 专辑名 → 无标题时文件夹名），不再用上一级文件夹 | 日本 ACG 发行以专辑为单位对应作品；逐曲推断导致作曲家成作品、一张专辑拆成多个作品 |
| 曲目角色 | **选项 B**：不从曲名推断曲目角色，角色只存 `album_works.role` | 曲名推断 OP/ED/c-w 连续三轮审查出问题；改由 Phase 4.5.4 的 Bangumi 单曲数据提供 |
| 曲目级自动关联 | 仅来自显式标签（WORKTITLE/CONTENTGROUP/GROUPING/WORK）且作品不同于专辑作品 | 覆盖精选集/原创专辑里的主题曲，避免与专辑关联重复 |
| 合集 | 标题合集词硬否决；VA/合集标志仅作弱证据；`&`/`×` 只看引号外 | 多作曲家游戏 OST 普遍标 VA，一票否决会误杀 |
| 作品名 | 不按 `/ : -` 切分；保留季数（独立作品，D1）与 `劇場版`（D2）；季数写法（Season 2/2期/第2期）按季数数字归一 | 与 Bangumi 条目划分一致，避免 `Fate/Grand Order`→`Fate` 类截断 |
| 用户意图 | `works.origin`、专辑级/曲目级抑制、作品别名；删除/解除/改名/改类型后重算不复活 | 硬性要求“记住用户的删除” |
| 清理 | 仅删除不受保护、无引用的 auto 作品；受保护＝手动/手动关联/Bangumi 资料/已确认或已拒绝候选 | 只保护用户做过的决定；系统自动生成的待审候选可重新生成 |
| 重算 | 专辑指纹 + 规则版本（当前 `album-work-v5`），扫描后与管理页按钮触发，每 100 张专辑一批事务 | 增量、可重复、可自愈 |

**审查与验证**：Oracle 审计（50 个真实专辑样本）→ 7 轮 Reviewer 门禁（4 次 BLOCK 后收敛为 APPROVE）；本地库副本迁移演练：19 张专辑 → 9 个 OST 作品正确关联、自动曲目级覆盖 0、误识别的作曲家作品与旧规则垃圾作品被清理、`integrity_check=ok`、外键无违例；`go test ./...` 全部通过。暂缓项见“待办与已知风险”D-1～D-9。
