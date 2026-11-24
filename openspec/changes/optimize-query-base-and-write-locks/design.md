# Design

## Context

本变更对应性能优化评估中的第一批「查询基座 + 写锁止血」（见 `proposal.md - Why`）。目标曲库规模为 1.8 万 ~ 5 万首曲目、1800 ~ 5000 张专辑、数千位艺人，运行在单进程 Go + `modernc.org/sqlite`（WAL 模式，单写者，`MaxOpenConns(8)`，`busy_timeout(5000)`，`foreign_keys(1)`）架构上。

需要严格遵守的系统约束：
1. **备份校验约束**：`internal/storage/backup_validate.go` 与 `backup_schema.go` 会通过将全部迁移重放进内存库生成参照 schema，并对每张表和索引执行 `index_xinfo` 及 DDL token 严格比对；因此迁移 SQL 中的 `CREATE INDEX` 语句内部不可嵌入 SQL 注释，也不能依赖未写入迁移的临时对象。
2. **语义零破坏**：浏览、排序、可见性（`visibility.go`）、合辑/艺人合并链、搜索相关性打分（score 0/1/2/3 与平局规则）、播放会话状态推导、以及 durable run 断点续传幂等语义必须保持向后兼容。

## Goals / Non-Goals

**Goals:**
- 通过 SQLite 运行时参数与 Migration `085` 索引包，消除当前高频路径与外键级联删除的隐形全表扫描。
- 将高频读取路径（曲目版本数、按专辑/艺人/作品筛选、统一搜索、歌手列表、播放状态、首页随机专辑）全面收口到索引驱动或单遍查询。
- 消除 durable worker、播放事件与 `acousticCache` 的写锁/互斥锁放大点，防止后台任务拖垮前台请求。
- 修复小尺寸媒体回退 `no-cache` 缺陷，消除 `CachedWorkPoster` 的 `os.Stat` 风暴及 Series/Home 的明显重复查询。
- 用可重复执行的 `EXPLAIN QUERY PLAN` 与规模化性能测试固化收益，防止回归。

**Non-Goals:**
- 不改造扫描器/监听器主流程与音频解析器（留待下一独立变更）。
- 不引入 FTS5 虚拟表（避免在第一批同时引入 FTS5 shadow 表与 `backup_schema.go` 兼容及中日文分词边界问题）。
- 不拆分 `track_audio_features` 宽表结构，仅通过覆盖索引 `(analysis_version, analyzed_at)` 止血 30 秒探活问题；宽窄表拆分留待推荐/声学专项变更。
- 不改动 DailyMix 主题规划算法、Discovery 分页重构或 AI 打标候选队列架构（均留待后续专项变更）。

## Decisions

### 1. SQLite 运行时参数与 `PRAGMA optimize`（`internal/storage/store.go`）
- **决策**：在 `sqliteDSN` 中追加 `_pragma=cache_size(-64000)`（64 MiB）和 `_pragma=temp_store(MEMORY)`；在 `MigrateUpTo` 完成完整迁移（`upTo == 0`）末尾执行一次 `PRAGMA optimize`。
- **理由**：当前使用 SQLite 默认 ~2 MiB 页缓存且 temp B-tree 落盘，在 30k+ 曲库上频繁抖动；全库 154 个索引缺少 `sqlite_stat1` 统计导致优化器屡次选错驱动表。64 MiB 页缓存对 NAS/容器环境内存占用可控，同时显著降低热页淘汰。
- **替代方案**：
  - 开启大 `mmap_size`：在容器/NAS 文件系统与 `modernc.org/sqlite` 纯 Go 实现下存在地址空间占用和截断行为差异风险，本批暂不强开。
  - 启动全量 `ANALYZE`：大库启动耗时较长；`PRAGMA optimize` 仅对缺少统计或变化显著的表增量分析，更安全轻量。

### 2. Migration `085` 索引设计与表达式索引（`085_query_base_and_fk_perf_indexes.sql`）
- **决策**：新增单份幂等迁移 `085_query_base_and_fk_perf_indexes.sql`，全部采用 `CREATE INDEX IF NOT EXISTS ...`（DDL 内部无行内注释），包含：
  - 表达式索引：`idx_tracks_norm_title ON tracks(LOWER(TRIM(COALESCE(user_title, title))), id)`，与 `track_versions.go:456,488,559` 中的谓词表达式逐字匹配；
  - 排序与查找索引：`idx_albums_sort_title_nocase ON albums(sort_title COLLATE NOCASE, id)`、`idx_albums_added_at ON albums(added_at DESC, id)`、`idx_work_aliases_work ON work_aliases(work_id)`、`idx_track_audio_features_version_analyzed ON track_audio_features(analysis_version, analyzed_at)`、`idx_playback_sessions_counted ON playback_sessions(counted_at, track_id) WHERE counted_at IS NOT NULL`、`idx_track_tags_source_track ON track_tags(source, track_id)`、`idx_work_match_candidates_status_work ON work_match_candidates(status, work_id)`、`idx_artist_relation_candidates_status_artist ON artist_relation_candidates(status, artist_id)`、`idx_album_subject_candidates_status_ext ON album_subject_candidates(status, external_id)`、`idx_acquisition_searches_rgid ON acquisition_searches(rgid)`、`idx_acquisition_tasks_rgid ON acquisition_tasks(rgid)`、`idx_artist_merge_relation_moves_lookup ON artist_merge_relation_moves(relation_type, owner_id, role)`、`idx_artist_merge_relation_moves_op ON artist_merge_relation_moves(operation_id)`；
  - 缺失的非部分外键级联索引：`idx_playback_sessions_track ON playback_sessions(track_id)`、`idx_artworks_track ON artworks(track_id)`、`idx_daily_mix_play_tracks_track ON daily_mix_play_tracks(track_id)`、`idx_track_artist_sources_artist ON track_artist_sources(artist_id)`、`idx_artist_alias_jobs_artist ON artist_alias_jobs(artist_id)`、`idx_artist_merge_operations_source ON artist_merge_operations(source_artist_id)`、`idx_artist_merge_operations_target ON artist_merge_operations(target_artist_id)`、`idx_artist_alias_candidates_alias ON artist_alias_candidates(alias_id)`。
- **理由**：SQLite 对表达式索引要求查询表达式与索引定义完全一致；为 `tracks` 补表达式索引比新增冗余列+维护写入路径/触发器侵入性小得多，且无需改动 `ImportTrack`/`UpdateTrack`。注意 `idx_tracks_norm_title` 需要同时确认 `backup_schema.go` 的 `schemaTokens` 与 `index_xinfo` 校验完全支持带函数表达式与 `COLLATE NOCASE` 的索引（通过运行现有 `TestBackup*` 覆盖验证）。
- **替代方案**：给 `tracks` 新增 `GENERATED ALWAYS AS (...) STORED` 列——会触发表结构变更且需要更复杂的迁移测试，不如纯表达式索引干净可逆。

### 3. 消除 `(?=0 OR ...)` / `(?='' OR ...)` 参数化废索引
- **决策**：重构 `albumWhere`、`trackWhere`（`browse.go`）、`artistWhere`（`library.go`）、`workWherePrefix`（`works.go`）及 `creditArtistChain`/`creditArtistWhere`（`credit_artists.go`），仅当筛选值非空/非零/非 `"all"` 时才向 `clauses` 和 `args` 追加对应谓词。
  - 当 `creditArtistChain` 仅查单一角色（如 `role='composer'`）时，直接生成 `ta.role = ?`（否则 `ta.role IN ('composer','lyricist','arranger','producer')`）。
  - `albumWhere` / `trackWhere` 传入 `AlbumID > 0` 时直接生成 `t.album_id = ?`（或 `a.id = ?`），让优化器直接从主键或 `idx_tracks_album_sync` 驱动。
- **理由**：SQLite 编译查询计划时不感知绑定参数值，`(?=0 OR ...)` 迫使优化器放弃索引。动态拼接在 Go 中仅增加数行 `if` 判断，完全零副作用。

### 4. 统一搜索去重、空集短路与驱动表固定（`unified_search.go`、`search_aliases.go`）
- **决策**：
  1. 在 `Store.Search` 入口先**单次执行**艺人匹配查询（`resolveSearchArtists(ctx, q)`），返回匹配到的规范艺人 `[]matchedSearchArtist{RootID, Score}` 以及涉及到的全部合并成员 ID（展开 `chain` 一次）——或者把 `matchedRoots`（含 score）作为常量 `VALUES` 注入后续分类查询，彻底避免 4 个分类重复执行 4 遍 `searchArtistCTE` 与 `json_each(p.aliases_json)`。
  2. **空集短路**：
     - 当 `matchedArtists` 为空时：`artists` 与 `composers` 分类直接返回空页（**0 次 SQL**）；
     - `albums` 与 `tracks` 分类跳过所有 `album_artists`/`track_artists` 关联集合子查询，仅匹配标题字段！
  3. **过滤下推与驱动表固定**：
     - 移除 `album_candidates AS MATERIALIZED` / `track_candidates AS MATERIALIZED` 在未过滤前的全库物化；
     - 将“标题匹配”与“艺人关联匹配”先筛选出命中的少量 `id` 集合（例如 `albums` 中 `a.id IN (matchedAlbums) OR title LIKE ...`），**仅对命中行**计算 `score`、`collaboration` (`COUNT(*) FROM album_artists`) 和 `albumVisibleSQL`/`trackVisibleSQL`；
     - 在艺人关联子查询中以 `matched_member_ids`（少量已知 ID）作为外层 `CROSS JOIN` 驱动 `album_artists(artist_id, album_id)` 与 `track_artists(artist_id, track_id)`，杜绝 SQLite 从全库 `idx_track_artists_role(role='primary')` 反向扫描。
  4. `searchArtistsByIDs` 仅针对当前页最多 5~50 个 `ids` 做向下合并链展开（`WHERE id IN (...)` 为递归根），不再全库展开 `artistMergeChainSQL`。
- **替代方案**：引入 FTS5 trigram——作为后续独立变更评估，本批先把“4 遍重复计算 + 无过滤全表物化 + 反向驱动”三个架构错误修干净，收益已达数量级且零 schema 侵入。

### 5. `ListArtists` 两阶段分页、`artistImageURLSQL`、`recentlyPlayed`、`effectivePlaybackStates`、`RandomAlbum`
- **`ListArtists`（`library.go`）**：
  - 当 `Sort` 为 `"name"`（默认）、收藏默认序或 `"random"` 时，采用与 `ListAlbums`/`ListTracks` 相同的两阶段分页：Stage 1 仅查 `ar.id`（配合 `artistWhere` + `ORDER BY` + `LIMIT ? OFFSET ?`），且去掉 `artistWhere` 中当 `role == "album"` 或 `"track"` 时与 `artistVisibleSQL` 的冗余重复 `EXISTS`；Stage 2 仅对当前页的 `ids` 批量回表计算 `artistImageURLSQL`、`album_count`、`track_count`。
  - 当 `Sort` 为 `"albums"` 或 `"tracks"` 时，排序本身依赖聚合计数，保留排序后分页，但仍将 `artistImageURLSQL` 移至分页后的 Stage 2 计算。
- **`artistImageURLSQL`（`track_extras.go`）**：
  - 将 `WHERE ci.artist_id = ar.id OR ci.artist_id IN (SELECT ma.id FROM artists ma WHERE ma.merged_into_artist_id = ar.id)` 改写为 `UNION ALL` 两个独立索引查找分支（自身 PK 查找 + 按 `idx_artists_merged_into` 查找合并源），再 `ORDER BY is_self DESC, artist_id ASC LIMIT 1`；`artist_image_cache` 同样改为 `EXISTS(自身 PK) OR EXISTS(JOIN merged_into)`，彻底消除 `SCAN ma`。
- **`albumSorts["recentlyPlayed"]`（`browse_order.go`、`library.go`）**：
  - 在 `ListAlbums` Stage 1 检测到 `f.Sort == "recentlyPlayed"` 时，将 `albumLastPlayedSQL` 作为单个投影列或 CTE 仅计算一次，再用该别名执行 `lp='' ASC, lp <DIR>`，将子查询次数减半。
- **`effectivePlaybackStates`（`client_features.go:438`）**：
  - 将每行触发相关子查询+temp-sort 的 SQL 改写为单遍窗口函数：
    `SELECT track_id, ended_at, COALESCE(end_reason,'') FROM (SELECT track_id, ended_at, end_reason, ROW_NUMBER() OVER (PARTITION BY track_id ORDER BY ended_at DESC, rowid DESC) AS rn FROM playback_sessions WHERE track_id IN (...) AND ended_at IS NOT NULL) WHERE rn = 1`。
  - 复杂度由单曲 O(S² log S) 降为 O(S log S)（且 `idx_playback_sessions_ended_track(track_id, ended_at DESC)` 已按 `track_id, ended_at DESC` 预排，绝大多数单毫秒唯一时间戳无需排序）。
- **`RandomAlbum`（`home.go:347-373`）**：
  - 采用两段式无全表排序抽样：先读取 `SELECT MIN(id), MAX(id) FROM albums`，在 Go 中生成随机锚点 `pivot`（或从可见总数取 `OFFSET rand(0, count-1)` — Wait：对比两者，`MIN/MAX` id 采样若 ID 稀疏会偏斜，而 `SELECT a.id FROM albums a WHERE a.id >= ? AND a.id <> ? AND albumVisibleSQL ORDER BY a.id LIMIT 1` 若未命中再绕回 `a.id < ?` 虽然快但偏斜；如果先 `SELECT a.id FROM albums a WHERE ... ORDER BY a.id LIMIT 1 OFFSET ?` 按 `total := COUNT(*)` 均匀抽偏移，扫描代价是线性跳过索引项无需排序 O(N) 无 temp B-tree；更好的平衡是：**先用 `SELECT COUNT(*) FROM albums a WHERE albumVisibleSQL`（或用 `albums` 主键偏移）+ `LIMIT 1 OFFSET rand(0, N-1)`**，既保证**严格均匀分布**（与原 `ORDER BY RANDOM()` 统计分布一致），又完全消除 `USE TEMP B-TREE FOR ORDER BY`！对于 3000~5000 张专辑，`LIMIT 1 OFFSET k` 仅走 PK 索引顺序扫描平均 N/2 行，耗时 < 1ms。

### 6. Durable Worker O(1) 进度更新（`enrichment_durable.go`、`artist_durable_runs.go`、`artist_image_backfill.go`、`artist_biography_backfill.go`）
- **决策**：四个 `Complete*` 方法在已持有写锁并通过 `if status == "completed" { return nil }` 幂等检查后：
  1. 执行 `UPDATE <items_table> SET status='completed', outcome=? ... WHERE id=? AND status<>'completed'` 并校验 `RowsAffected() == 1`（双保险防重复累加）；
  2. 将 5~6 条 `SELECT count(*)` 语句合并为**单条 O(1) `UPDATE <runs_table>`**：`SET processed = processed + 1, <outcome_col> = <outcome_col> + 1, waiting_until = NULL, wait_source = '', ... WHERE id = ?`；
  3. 仅在 `CompleteArtistBiographyBackfillItem` 中将 4 种 `skipped_*` outcome 映射到 `skipped_items = skipped_items + 1`。
- **安全性验证**：已审计全库所有 `UPDATE *_items` 语句，所有恢复/暂停/重试路径均带 `WHERE status <> 'completed'`，不存在 `completed -> pending` 回退路径；且旧库中已有 run 行计数本就精确，因此升级无缝衔接。同时迁移 `085` 不再必须依赖全扫 `enrichment_run_items`，但仍可为 `enrichment_run_items(run_id, outcome)` 补索引以便审计/诊断查询。

### 7. `acousticCache` 容错与无阻塞重建（`internal/storage/acoustic.go`）
- **决策**：
  - 将 `acousticCache` 状态拆分为受轻量 `mu` 保护的字段：`snap *AcousticSnapshot`、`checkedAt time.Time`、`builtAt time.Time`、`hasEmpty bool`（标记已确认空库）、`rebuilding bool` 及 `buildDone chan struct{}`（singleflight 协调）。
  - **快路径**：`mu.Lock()` 下若 `time.Since(c.checkedAt) < acousticCheckInterval` 且（`c.snap != nil` 或 `c.hasEmpty`），直接返回 `c.snap, nil`。
  - **探活与重建**：
    - 释放 `mu` 后执行 `LatestAudioAnalysisVersion(ctx)` 和 `COUNT/MAX`（如今命中 `idx_track_audio_features_version_analyzed` 覆盖索引，微秒级）。
    - **若 `err != nil`（包括 `context.Canceled`/`DeadlineExceeded`）：绝不清空 `c.snap` 也不推进 `checkedAt`，直接返回错误。**
    - 若 `version == ""` 或 `count == 0`：加锁设置 `c.snap = nil, c.hasEmpty = true, c.checkedAt = now` 返回 `nil, nil`。
    - 若需要重建（`c.snap == nil` 或 stamp 变化且满 `acousticRebuildInterval`）：
      - 若**已有合法 `c.snap`** 且另一 goroutine 正在 `rebuilding`：直接立即返回旧 `c.snap`（不阻塞读）；若尚无重建，标记 `rebuilding = true`，在锁外执行 `buildAcoustic`（注意使用独立不随单个短超时请求取消的上下文或尊重调用方 ctx，若已有旧 `c.snap` 可先返回旧快照或仅让首个请求执行且不持 `mu` 阻塞其他读——更干净的做法是：**已有旧 `c.snap` 时，读路径直接无锁/短锁返回旧快照，仅在锁外做 singleflight 重建并原子替换 `c.snap`**；冷启动 `c.snap == nil` 时，其他并发请求等待同一个 `buildDone` 避免惊群）。
  - **内存分配优化**：`buildAcoustic` 传入已知 `count`，预分配 `ids`/`raw`，并在循环中原地量化写入 `snap.vecs[i*dims:(i+1)*dims]`，消除每次 `quantize` 的 17k 次临时 `[]int8` 分配。

### 8. 播放会话写锁与巡检止血（`playback_sessions.go`、`playback_sweeper.go`）
- **`recordPlaybackEventTx`**：将 `s.db.DB.BeginTx(ctx, nil)` 改为 `s.beginWrite(ctx)`，在读取 `playback_sessions` 之前先获取写锁，彻底消除读写升级导致的 `SQLITE_BUSY_SNAPSHOT`。
- **`FixExpiredPlaybackSessions`**：在执行 `UPDATE` 前先执行只读 `SELECT EXISTS(SELECT 1 FROM playback_sessions WHERE ended_at IS NULL AND (` + sessionExpiredSQL + `))`（命中 `idx_playback_sessions_open` 部分索引）；返回 `false` 时直接返回 `0, nil`，避免每 30 秒空闲获取写锁。

### 9. HTTP 媒体缓存、海报内存索引与 Series/Home 去重
- **缩略图回退语义（`internal/http/thumbnail.go`、`library.go`、`works.go`、`playlist_artwork.go`）**：
  - 将 `serveThumbnail` 返回值从 `bool` 改为 3 态枚举：`thumbnailServed`、`thumbnailUseOriginalCacheable`、`thumbnailUseOriginalNoCache`。
  - 当 `thumbnail()` 返回 `ok == false && err == nil`（原图尺寸 `<= bucket`、不支持格式或超过安全像素上限，属于确定性稳定回退）或 `HEAD` 请求回退原图时，返回 `thumbnailUseOriginalCacheable`，调用方保留各自的 `Cache-Control`（`artwork`: `public, max-age=31536000, immutable`；`artist`: `public, max-age=86400` 等）并基于文件 `(size, modtime)` 或内容 key 设置强/弱 `ETag`；仅当发生瞬时 I/O 或解码/打开异常时返回 `thumbnailUseOriginalNoCache` 并写入 `Cache-Control: no-cache`。
- **`CachedWorkPoster` 内存索引（`internal/enrichment/work_poster.go`）**：
  - 在 `Manager` 上维护 `posterCacheMu sync.RWMutex`、`posterCacheLoaded bool`、`posterCache map[string]posterCacheEntry`（`key -> {ext, mime}`）。
  - 首次调用 `CachedWorkPoster` 时执行一次 `os.ReadDir(m.workPosterDirectory())` 填充 map（目录不存在视为空 map 并标记 loaded）。
  - `CacheWorkPoster` 写入成功时同步更新 map；同时提供一个失效钩子（若实际 `os.Open` 返回 `os.ErrNotExist`，由调用方或辅助方法从 map 中删除该 key）。确保测试中临时写入目录的场景或目录切换可正确工作（可在 key 未命中且刚写文件测试中注意：如果测试直接往磁盘写文件绕过 `CacheWorkPoster`，需要检查现有单测！——**实施前检查 `work_poster_test.go`**：若测试在调用 `CachedWorkPoster` 之前写文件，首次 `ReadDir` 自然可见；若测试在首次调用后再直接手工写盘，需确保不破坏测试语义或提供 `InvalidateWorkPosterCache()`）。
- **Series API 聚合统计（`internal/storage/series.go`、`internal/http/series_api.go`）**：
  - 在 `Store` 新增 `SeriesAlbumAndTrackCounts(ctx, seriesID)`，用两条去重聚合 SQL 计算可见 `albumCount`（与 `AlbumsForWork` 语义一致：`album_works` 关联的可见专辑去重数）和可见 `trackCount`（与 `TracksForWork` 语义一致：`work_tracks` 与 `album_works -> tracks` 关联的可见曲目去重数），替换 `handleAPISeriesDetail` 中全量拉取再丢弃切片的 2N+1 循环。
- **Home 去重与批量化（`internal/http/home.go`、`internal/http/home_api.go`）**：
  - 在 `homeData` 中先获取 `recentAlbums`（`Limit: 12`）与 `unfinished`（`Limit: 5`），传入 `homeHero` 直接复用 `recentAlbums[0]` 与 `unfinished[0]`，消除 2 次重复查询；
  - 将 `AlbumYearFacets` 返回的 `years[0].Count` 直接传给 `homeChronicle`，跳过重复 `CountAlbums(Year)`；
  - 在 `homeAPIFrom` 中收集 spotlight 3 个专辑 + `recentAlbums` + `chronicle.Albums` 的唯一专辑列表，合并为**单次 `AttachAlbumArtistRefs`** 调用，替代原有的 5 次串行调用。

## Risks / Trade-offs

- **[Risk] 表达式索引与 `backup_schema.go` 校验不兼容** → 迁移 `085` 编写后立即运行全部 `TestBackup*` 校验套件（含 `TestBackupRestore`、`TestBackupValidate`、`TestMigrationTemplate`），确认 `index_xinfo` 和 `schemaTokens` 完全通过。
- **[Risk] `CachedWorkPoster` 内存索引与外部手动增删海报文件不同步** → 在 `handleWorkPoster` 打开文件返回 `os.ErrNotExist` 时自动剔除过期缓存项并回退一次磁盘确认；若测试中直接手动向磁盘写文件，需核对 `work_poster_test.go` 与 `works_test.go`，必要时在缓存 miss 时按目录 `mtime` 检测是否发生变化（仅 1 次目录 `os.Stat` 对比 `dirMtime`，目录未变则零 per-file `os.Stat`，目录变了自动重载 `ReadDir`！——这既彻底消除每页数千次 `os.Stat`，又 100% 兼容任何外部手工增删文件及现有测试！）。
- **[Risk] `SeriesAlbumAndTrackCounts` 计数口径与原 `seriesAlbums`/`seriesMemberTracks` 存在细微偏差** → 实现时严格对齐 `AlbumsForWork`（`album_works` + `albumVisibleSQL`）和 `TracksForWork`（`work_tracks UNION album_works->tracks` + `trackVisibleSQL`）的精确集合定义，并编写对照单测验证两者在跨作品共享专辑/曲目及缺失文件场景下完全一致。
- **[Risk] 统一搜索重构改变平局/排序或忽略合并艺人别名** → 保留 `SearchVariants` 假名展开、`score` 0/1/2/3 与 `relation` 0/1/2 规则完全不变，先跑全量现有 `search`/`unified_search` 单测，再新增宽泛词、精确词、合并艺人别名、无匹配词的对比与性能断言。
- **[Risk] `acousticCache` 异步/非阻塞重建在并发下产生竞态** → 使用 `-race` 运行 `internal/storage` 并发测试，验证超时不清空快照、负缓存生效、重建时不阻塞读且无 data race。

## Migration Plan

1. 新增 `internal/storage/migrations/085_query_base_and_fk_perf_indexes.sql`（纯新增索引，均为 `CREATE INDEX IF NOT EXISTS`）。
2. 老库升级时由启动流程自动备份（`preupgrade` 快照）并按序应用迁移 `085`，完成后执行 `PRAGMA optimize`。
3. 如需回滚，回退二进制版本即可（多出的二级索引对旧版本 SQLite 查询完全只读透明，不会破坏数据结构，或直接通过升级前快照恢复）。
