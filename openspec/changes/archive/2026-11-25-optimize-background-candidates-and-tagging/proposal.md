# Proposal

## Why

当前后台 AI 打标、声学分析/校准以及歌手/作品补全的候选筛选逻辑，在每轮任务批处理或管理页统计轮询时都会执行 O(N²) 全库扫描、大量逐行相关子查询与 N+1 循环。以 2 万首曲库为例，仅 `NextTaggingCandidates` 每拉取 8 首待打标曲目就要对全库计算约 12 个相关子查询（单批约 24 万次子查询）；同时 `NextAudioAnalysisCandidate`、`NextAudioCalibrationCandidate`、`ArtistBiographyBackfillCandidatesForMode` 与富化目标查询也存在重复全表扫描和延迟事务写锁竞争问题。

## What Changes

- **AI 打标候选选择、Stamp 计算、统计与最近结果**：
  - 将 `NextTaggingCandidates` 拆分为两阶段流程：第一阶段按 `(added_at DESC, id DESC)` 索引顺序筛选未打标且就绪的新曲目（`m.track_id IS NULL`），仅对最终命中的曲目（或存在活跃 `ai_tagging_failures` 记录的曲目）计算 `taggingStampSQL`；第二阶段仅在第一阶段不足 `limit` 时执行，提供全量版本升级快速路径，并通过一次性按表预聚合的集合 CTE 评估已打标曲目的版本/stamp 变化，严格保持既有优先级、排序与**逐字节完全一致的 `source_stamp`**。
  - 将 `TrackTaggingStamp` 改写为单曲主键直查（`WHERE t.id = ?`），避免全库物化 `taggingBase`。
  - 将 `TaggingStats` 改写为复用按表预聚合的集合 CTE，消除每首曲目 9 个相关子查询。
  - 在 `RecentTagResults` 中用批量 `WHERE track_id IN (...)` 查询替代逐曲 `TrackTags` 的 N+1 循环。
- **声学分析与校准候选选择、失败列表与进度统计**：
  - 将 `NextAudioAnalysisCandidate` 改写为 `tracks` 主键逆序扫描（`ORDER BY t.id DESC`）配合 `CROSS JOIN` 早停，消除临时 B-tree 排序。
  - 将 `NextAudioCalibrationCandidate` 改写为基于部分覆盖索引 `idx_audio_files_available_track` 的集合聚合，先完成 Knuth 哈希抽样并 `LIMIT target`，仅对命中的目标切片（及极少数永久失败行）回表读取 `tracks` 和 `audio_files`。
  - 将 `AudioAnalysisStats` 改写为基于覆盖索引 `avail` CTE 的集合查询，新增轻量的 `AudioEligibleTrackCount` 供 `analysis.Service.calibrationTarget` 使用，并将 `AudioAnalysisFailures` 改为直接从 `audio_analysis_failures` 表驱动查询。
- **歌手简介/头像补全与富化候选查询优化**：
  - 修复 `ArtistBiographyBackfillCandidatesForMode("retranslate")` 中的不可索引 `OR IN` 子查询（拆分为双 `EXISTS` 分支）以及逐歌手调用 `ArtistBiographies` 的 N+1 循环（改为分批批量加载）。
  - 将 `artistBiographyDisplayableSQL` 中的每行 `json_each` 替换为显式参数化 `IN (?, ...)` 占位符，并将 `artistBiographyPerformerPredicate` 中的 `IN (SELECT ...)` 改写为命中 `idx_track_artists_role` 索引的 `EXISTS`。
  - 移除 `artistsForMatching`、`AlbumsForEnrichment` 和 `WorksForEnrichment` 中的参数化可选/强制守卫（`?=0 OR ar.id=?`、`WHERE ? OR NOT EXISTS(...)`、`AND (? OR ...)`），按条件动态裁剪 SQL 分支；并在 `ArtistImageBackfillCacheScanForMode` 中预先读取 `web_fallback_enabled` 而非在每行相关子查询中重复查表。
- **配套索引与前置写锁**：
  - 新增迁移 `086_background_candidate_perf_indexes.sql`，创建 `idx_tracks_added_id ON tracks(added_at DESC, id DESC)` 与 `idx_artist_image_candidates_artist_status ON artist_image_candidates(artist_id, status)`。
  - 将 `SaveTrackTagResult`、`ResetTrackTagging`、`RecordTaggingFailure`、`CreateAISampleRun`、`SaveTrackAudioFeatures` 和 `SaveAudioAVModel` 统一切换为通过 `s.beginWrite(ctx)` 前置获取 SQLite 写锁。

## Capabilities

### New Capabilities
- `background-candidate-scheduling`: 后台 AI 打标、声学分析与校准、歌手简介/头像补全及元数据富化任务的低复杂度候选筛选、集合化 stamp/进度统计与批量化/索引化目标查询能力。

### Modified Capabilities
- `sqlite-storage-foundation`: 新增迁移 086 索引，支持按入库时间逆序扫描曲目（`tracks(added_at DESC, id DESC)`）以及按歌手高效检索待审核头像候选（`artist_image_candidates(artist_id, status)`）。
- `write-lock-and-cache-resilience`: 要求 AI 打标与声学分析的多语句及读改写事务提前获取 SQLite 写锁，避免并发写入触发 `SQLITE_BUSY_SNAPSHOT`。

## Impact

- **存储层与迁移**：`internal/storage/migrations/086_background_candidate_perf_indexes.sql`、`internal/storage/ai_tagging.go`、`internal/storage/audio_analysis.go`、`internal/storage/artist_biography_backfill.go`、`internal/storage/artist_image_comprehensive.go`、`internal/storage/identity.go`、`internal/storage/enrichment.go` 及对应的单元测试/执行计划测试/基准测试。
- **后台服务层**：`internal/analysis/service.go`（`calibrationTarget` 改用轻量可分析曲目计数）；`internal/tagging/service.go` 保持现有接口不变，直接受益于底层候选与统计查询提速。
- **兼容性**：无对外 API 变更；`taggingStampSQL` 生成结果保持逐字节完全一致，升级后不会导致既有 `track_tag_meta` 或 `ai_tagging_failures` 记录失效或触发全库重打标；新增索引完全兼容备份表结构校验器。
