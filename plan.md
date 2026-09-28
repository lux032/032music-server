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

### Phase 4.5: Bangumi 深度整合与作品展示（下一版，已与用户确认范围）
> **目标**：以 Bangumi 数据能可靠支撑的范围为边界，完善“类型 → 作品 → 专辑”的浏览体验与关联精度。项目定位为**日本 ACG 优先**，普通影视不在考虑范围。
> **实施方式**：硬交付流程（Oracle 审计 → 用户决策 → UI 效果图确认 → Worker → Reviewer），建议分批交付：①数据（4.5.1–4.5.4）②UI（4.5.5–4.5.6）③自定义图片（4.5.7）。

* [x] **4.5.1 Bangumi 音乐条目确认专辑关联**：专辑名 → Bangumi 音乐条目（type 3）→ 关联的动画/游戏；同时取得角色（原声集/片头曲/片尾曲/插入歌/角色歌）。以 Bangumi 为权威：作品侧关系为具体角色且唯一即自动确认（含 `infinite synthesis 6` → 269645《とある魔術の禁書目録 幻想収束》）；广播剧、其他和泛关系进入审核，不加本地启发式否决。专辑级只记 `ost` / `other`，具体角色按音乐条目同名及受限版本后缀规则记在曲目级；找不到同名曲目时只保留专辑级关联。原创专辑逐曲反查仍留待 4.5.4。实现遵循定稿设计 `docs/design/works-association.md`。
  * **详细设计与用户决策（分叉1–5：A/A/B/A/A）**：仅反查恰好一部有片头/片尾/插入/原声/角色歌角色的动画或游戏时自动确认；处理没有 manual / bangumi 专辑关联的非合辑（仅有 auto 关联也处理并由 Bangumi 替换）；普通批量只看 Anime/アニメ/アニソン 流派或 tv_size，强制及单专辑放宽动画筛选；不以首曲名搜索、不做 4.5.4 曲名反查；改标题保留既有 Bangumi 关联。艺术家一致、分数≥80、领先≥20、未抑制及候选未拒绝是自动确认的其他必要条件。候选和 miss 用 migration 026 持久化，拒绝与解除由 suppression key 保留，人工审核可选择多作品。
* [x] **4.5.2 作品类型修正**：migration 028 新增 `type_locked` 并回填既有 manual 作品；作品阶段按 Bangumi type/platform（剧场版→movie，其他 type 2→anime，type 4→game）纠正未锁定作品，冲突时保留原值并记录证据；外部资料类型同步更新。
* [ ] **4.5.3 多季折叠**：按 Phase 4.6 的单层系列模型实现；沿 Bangumi 续集/前传关系把同一作品的多季归组，列表**默认折叠**。跨媒体系列（如 Fate 游戏/动画/剧场版）、联动/世界观关系本版不做（关系链噪音大，只能人工确认）。
* [x] **4.5.4 精选集/原创专辑主题曲识别**：
  * 本地识别曲名中的严格标注（媒体前缀 + 引号作品 + OP/ED 等）；
  * “曲名 + 歌手”反查 Bangumi 单曲条目；**条目名一致 + 本曲 primary 歌手及别名一致 + 恰好 1 部作品登记了具体用途**才自动确认。条目名一致但歌手不一致或没有 primary 歌手时记为未命中，不进审核（D8）；多个条目指向不同的（作品，用途）或多曲名条目才进审核（实测：only my railgun、紅蓮華 自动确认；カタオモイ、歌手不一致的 Hello/Link 记未命中；朝が来る、赤い罠/ADAMAS 写 multi_title 候选）；
  * 以可停止的后台任务运行，结果缓存，沿用 Bangumi 节流；
  * 单曲 A 面 / c/w 的精确角色由此提供（替代已放弃的曲名推断，见修订记录“选项 B”）。
  * **实现**：migration 027 只新建 `track_subject_candidates` 与 `track_enrichment_misses`（不重建 work_tracks，026 已放开 bangumi 来源）。搜索关键词把 `-` 换成空格（H1，缓存键 `music-search:v2:`），专辑级请求同样处理，打分仍用原标题；专辑级与曲目级共用抑制与落地；确认后删掉该曲目的 auto 行，本地推导遇到 bangumi/manual 行跳过（D9）。审核接口 `GET/POST /api/v1/enrichment/tracks/{trackId}/subjects...` 与 `/admin/enrichment` 曲目候选区：接受时改过用途的作品写 `source=manual`，没改的写 `source=bangumi`（D10）；拒绝写 `track_work_suppressions` 的 `bangumi:<m>`。扫描后全量任务包含曲目级（D11），未命中重查间隔 max(cacheDays,90) 天、发售 180 天内为 7 天（D12），只跳过 manual/bangumi 的 ost 专辑（D13）。
* [ ] **4.5.5 专辑页 UI（仅 PC）**：
  * OST/单曲类：标题区作品胶囊（小海报 + 作品名 + 角色），多作品收成“+N”；
  * 精选集：曲目行显示关联图标（多作品带数字角标），悬停 ~150ms 弹出卡片（海报、原名、中文译名、类型·角色·年份，可点击跳转），移开 ~300ms 关闭，鼠标可移入卡片；键盘 Tab 聚焦显示、Esc 关闭；做成通用组件；
  * 右侧信息栏“关联作品”汇总（仅有关联时显示），有待审核候选时显示入口；
  * 关联编辑放入“编辑专辑”抽屉；先用 Playwright 出静态效果图给用户确认。
* [ ] **4.5.6 作品页 UI**：关联专辑封面网格；精选集中的单曲单独列入“收录于”分组；多季折叠展示。
* [ ] **4.5.7 自定义专辑封面与歌手图片**：
  * 编辑抽屉中上传/恢复默认，只存数据目录，绝不改写音乐文件；
  * 自定义图片优先，重扫、自动刷新（Last.fm/MusicBrainz 覆盖 `artist_image_cache`）、专辑合并都不能覆盖；把分散在 5 处 SQL 的“选封面”规则收拢为统一规则；
  * JPEG/PNG/WebP，按内容判断格式，上限 10MB 并限制像素尺寸，管理员 + CSRF，按哈希去重；
  * 缓存失效：图片地址带版本号/ETag，更新专辑 `updated_at` 以便客户端同步；
  * 不做裁剪（居中铺满显示）；专辑合并保留目标专辑的自定义封面，目标没有时沿用源专辑的。

### Phase 4.6: 系列层（Phase 4.5 之后单独立项）
* [ ] 系列只做一层、一个作品只属于一个系列；类型作为筛选而非固定层级，单一类型的系列省略类型层。
* [ ] 季数/剧场版沿续集、前传关系自动归入；跨媒体关系只生成建议；管理页可手动创建系列、调整归属、改名，并记住用户修改。

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
| D-9 | `catalog_number` 开头含 NUL 字符（如 `\0\0\0\0ARCD0012`） | 展示问题 | 读取标签时清理控制字符 |
| D-10 | `resolveAutoWork` 为优先复用已绑定 Bangumi 作品会扫描候选作品及别名 | 大型作品库刷新时可能出现性能压力 | 后续增加规范化身份索引或预计算映射，本轮不改语义 |
| D-11 | 标题含 `\|` 时抑制键被误按旧格式解析，按 anime 解除后改成 game 会失效 | 已解决（批次 1） | 从右往左拆键并验证合法类型/季数 |
| D-12 | `RemoveWorkAlbum` 不删除专辑级写到曲目上的 bangumi 行 | 已解决（批次 1） | 只清理同作品、同专辑、同 inferred_key 的 bangumi 曲目行 |
| D-13 | 专辑级人工接受的抑制解除口径与曲目级不一致：专辑级遇到精确的 `bangumi:<m>:<w>` 会返回 review（409），用户无法用人工接受推翻 | 已解决（批次 1） | 共用人工解除函数；保留整条目拒绝 |
| D-14 | 专辑级搜索由 limit=25 改为每页 20 条加分页，翻页条件是条目名完全一致，专辑名很少满足，第 21～25 条会丢失 | 已解决（批次 1） | 分页大小参数化；专辑 25、曲目 20 |
| D-15 | album scope 还会刷新该专辑已关联作品的资料 | 超出设计 8.2 第 9 条的描述，但行为合理（专辑页一键补齐） | 已在设计文档 8.2 补说明；如要收窄可单独提案 |
| D-16 | 本地推导遇到曲目上任何 manual 或 bangumi 行就整首跳过，纯手动行也会挡住本地推导去关联其他作品 | 符合 R1 和 D9，是有意为之，只作记录 | 无需处理 |
| D-17 | `/admin/enrichment` 待审曲目每条都单独调一次 `TrackByID`（最多 200 次查询） | 审核页渲染偏慢 | 在 `trackCandidateSelect` 里直接带出歌手，省掉逐条查询 |
| D-18 | 曲目 Bangumi 未命中指纹不包含抑制状态；解除抑制后仍可能命中旧 miss | 解除后要等重查间隔到期才重新查询 | 后续把抑制状态纳入 miss 指纹或解除时清理 miss |

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
