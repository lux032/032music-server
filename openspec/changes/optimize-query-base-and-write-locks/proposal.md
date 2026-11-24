# Proposal

## Why

在 1.8 万 ~ 5 万首曲目规模的真实曲库下，当前服务端在前台浏览/搜索与后台并发写入场景存在明显延迟和写锁争抢：实测 1.8 万曲统一搜索耗时 449ms ~ 900ms（无匹配查询也要 449ms），曲目列表/专辑详情每次读取都会触发全表扫描 `tracks`，小尺寸封面/头像因回退 `Cache-Control: no-cache` 无法命中浏览器缓存，而后台 durable worker 每完成一条任务就在 SQLite 写锁内执行 5~6 次全表 `COUNT(*)` 扫描，直接阻塞前台播放记录与收藏写入。现在优先落地这一批低风险、高 ROI 的查询基座与写锁止血优化，可在不改动核心业务状态机的前提下把全站高频延迟和写锁压力降低一个数量级，并为后续扫描器重构打好底层索引与配置基础。

## What Changes

- **SQLite 运行时配置与索引迁移（Migration `085`）**：
  - 在 SQLite DSN 中补齐 `cache_size`（默认 `-64000`，即 64 MiB）与 `temp_store(MEMORY)`，并在启动/迁移完成后执行 `PRAGMA optimize`，补充优化器统计信息。
  - 新增迁移 `085_query_base_and_fk_perf_indexes.sql`，补齐高频表达式索引、排序/查找索引与缺失的外键级联索引（覆盖 `tracks`、`albums`、`work_aliases`、`track_audio_features`、`playback_sessions`、`track_tags`、`work_match_candidates`、`artist_relation_candidates`、`album_subject_candidates`、`acquisition_searches`、`acquisition_tasks`、`artist_merge_relation_moves`、`artworks`、`daily_mix_play_tracks`、`track_artist_sources`、`artist_alias_jobs`、`artist_merge_operations`、`artist_alias_candidates`），并保证与备份 schema 校验完全兼容。
- **核心读查询消除全表扫描与二次扫描**：
  - 将 `browse.go`、`library.go`、`works.go`、`credit_artists.go` 中导致索引失效的 `(?=0 OR ...)` / `(?='' OR ...)` 参数化可选谓词改为 Go 侧按需动态拼接 `WHERE`。
  - 重构 `unified_search.go` + `search_aliases.go`：统一搜索中仅计算一次艺人匹配结果并在空集时短路关联查询，移除无过滤条件的全表 `AS MATERIALIZED` 物化，并通过显式连接顺序防止从全库 `track_artists`/`album_artists` 反向驱动（本批不引入 FTS5，避免引入备份校验与分词变更风险）。
  - 将 `ListArtists` 升级为两阶段分页（与 `ListAlbums`/`ListTracks` 对齐）；重写 `artistImageURLSQL` 消除 `OR IN` 扫描；消除 `albumSorts["recentlyPlayed"]` 重复相关子查询；将 `effectivePlaybackStates` 的 O(S² log S) 相关子查询改为单遍有序窗口/极值查询；将 `RandomAlbum` 从全表 `ORDER BY RANDOM()` 改为无全表排序的随机采样。
- **后台写锁止血与声学缓存容错**：
  - 将 `CompleteEnrichmentItemTx`、`CompleteArtistRunItem`、`CompleteArtistImageBackfillItem`、`CompleteArtistBiographyBackfillItem` 的 run 进度更新由每次 5~6 条全表 `COUNT(*)` 改为幂等守卫后的 O(1) 单条增量 `UPDATE`。
  - 修复 `acousticCache`：任意查询错误或调用方超时/取消不再清空已有合法 `AcousticSnapshot`；缓存“无声学数据”的空状态以避免重复探库；将重量级快照重建移出全局互斥锁，避免阻塞并发读与 `/api/v1/client-features`。
  - 将 `recordPlaybackEventTx` 改为预先获取写锁（`beginWrite`）以避免 `SQLITE_BUSY_SNAPSHOT`；`PlaybackSweeper` 空闲时先做只读 `EXISTS` 检查再决定是否进入写事务。
- **HTTP 媒体缓存与首页/系列聚合止血**：
  - 区分缩略图“原图尺寸小于目标桶的正常回退”与“临时生成失败”两种状态：正常回退原图时保留调用方既定 `Cache-Control` 与 `ETag`，仅在瞬时失败时返回 `Cache-Control: no-cache`。
  - 为 `CachedWorkPoster` 增加基于目录扫描与写入同步的内存缓存索引，消除管理页和详情页数千次同步 `os.Stat`。
  - 为 `handleAPISeriesDetail` 增加 SQL 聚合计数接口，消除 2N+1 拉取全部专辑/曲目后丢弃切片的行为；合并 `homeData` / `homeAPIFrom` 中的重复查询（最近入库专辑、未完成播放、年份计数、批量 `AttachAlbumArtistRefs`）。
- **性能回归测试**：
  - 扩展 `EXPLAIN QUERY PLAN` 断言测试与合成规模性能测试，覆盖 `hydrateTrackVersionCounts`、按专辑/艺人筛选、统一搜索、`ListArtists`、`effectivePlaybackStates`、durable worker 计数与缩略图缓存头。

## Capabilities

### New Capabilities

- `sqlite-storage-foundation`: 定义 SQLite 运行时性能参数、核心查询与外键索引保障以及备份 schema 校验兼容性约束。
- `library-query-and-search`: 定义曲库浏览、分页排序、统一搜索、曲目版本计数、播放状态推导与首页随机抽样的查询行为和索引驱动约束。
- `write-lock-and-cache-resilience`: 定义后台 durable 任务 O(1) 进度更新、播放会话写锁防冲突与空闲巡检、以及内存声学索引缓存的容错与无阻塞读写行为。
- `media-and-home-http-serving`: 定义缩略图回退原图的 HTTP 缓存头策略、作品海报本地缓存内存索引，以及首页/系列详情 API 的聚合查询去重约束。

### Modified Capabilities

<!-- 仓库此前尚无 OpenSpec 主规格，本批均为首次建档的能力规格。 -->

## Impact

- **数据库与迁移**：新增 [085_query_base_and_fk_perf_indexes.sql](file:///workspace/032music-server/internal/storage/migrations/085_query_base_and_fk_perf_indexes.sql)，修改 [store.go](file:///workspace/032music-server/internal/storage/store.go)，影响所有读写连接初始化与备份校验逻辑（需确保 [backup_validate.go](file:///workspace/032music-server/internal/storage/backup_validate.go) 校验通过）。
- **存储查询与后台状态层**：[browse.go](file:///workspace/032music-server/internal/storage/browse.go)、[browse_order.go](file:///workspace/032music-server/internal/storage/browse_order.go)、[library.go](file:///workspace/032music-server/internal/storage/library.go)、[works.go](file:///workspace/032music-server/internal/storage/works.go)、[credit_artists.go](file:///workspace/032music-server/internal/storage/credit_artists.go)、[unified_search.go](file:///workspace/032music-server/internal/storage/unified_search.go)、[search_aliases.go](file:///workspace/032music-server/internal/storage/search_aliases.go)、[track_extras.go](file:///workspace/032music-server/internal/storage/track_extras.go)、[track_versions.go](file:///workspace/032music-server/internal/storage/track_versions.go)、[client_features.go](file:///workspace/032music-server/internal/storage/client_features.go)、[home.go](file:///workspace/032music-server/internal/storage/home.go)、[series.go](file:///workspace/032music-server/internal/storage/series.go)、[enrichment_durable.go](file:///workspace/032music-server/internal/storage/enrichment_durable.go)、[artist_durable_runs.go](file:///workspace/032music-server/internal/storage/artist_durable_runs.go)、[artist_image_backfill.go](file:///workspace/032music-server/internal/storage/artist_image_backfill.go)、[artist_biography_backfill.go](file:///workspace/032music-server/internal/storage/artist_biography_backfill.go)、[acoustic.go](file:///workspace/032music-server/internal/storage/acoustic.go)、[playback_sessions.go](file:///workspace/032music-server/internal/storage/playback_sessions.go)、[playback_sweeper.go](file:///workspace/032music-server/internal/storage/playback_sweeper.go)。
- **HTTP 与海报缓存层**：[thumbnail.go](file:///workspace/032music-server/internal/http/thumbnail.go)、[library.go](file:///workspace/032music-server/internal/http/library.go)、[works.go](file:///workspace/032music-server/internal/http/works.go)、[playlist_artwork.go](file:///workspace/032music-server/internal/http/playlist_artwork.go)、[work_poster.go](file:///workspace/032music-server/internal/enrichment/work_poster.go)、[series_api.go](file:///workspace/032music-server/internal/http/series_api.go)、[home.go](file:///workspace/032music-server/internal/http/home.go)、[home_api.go](file:///workspace/032music-server/internal/http/home_api.go)。
- **兼容性**：无破坏性 API 变更；对外 JSON 响应结构、分页/排序语义、去重和回退规则均保持向后兼容（仅修正小图回退原图时的 `Cache-Control` 缓存头缺陷）。
- **明确不在本批范围（留待后续变更）**：扫描器与监听重构（#2）、AI 打标与声学分析候选队列去 O(N²)（#3）、DailyMix/Discovery 算法与 `track_audio_features` 宽窄表分离（#4）、以及 FTS5 全文检索引入。
