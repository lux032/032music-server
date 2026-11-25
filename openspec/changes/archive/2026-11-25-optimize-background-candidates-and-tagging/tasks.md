# Tasks

## 1. 配套索引迁移（Migration 086）

- [x] 1.1 创建 `internal/storage/migrations/086_background_candidate_perf_indexes.sql`，新增 `idx_tracks_added_id ON tracks(added_at DESC, id DESC)` 与 `idx_artist_image_candidates_artist_status ON artist_image_candidates(artist_id, status)`。在 `internal/storage/migration_086_test.go` 中补充全新迁移、085→086 升级、`PRAGMA integrity_check`/`foreign_key_check` 与备份结构校验测试，验证 `go test ./internal/storage -run TestMigration086` 通过。

## 2. AI 打标候选筛选、Stamp 统计与写锁加固

- [x] 2.1 重构 `internal/storage/ai_tagging.go`：实现两阶段 `NextTaggingCandidates`（第一阶段走 `idx_tracks_added_id` 索引早停并跳过无活跃失败新曲的 stamp 计算；第二阶段含全量版本升级快速路径及集合预聚合 `taggingBaseSetSQL`）、单曲主键直查 `TrackTaggingStamp`、集合化 `TaggingStats` 与批量加载标签的 `RecentTagResults`。补充多维度逐字节 stamp 一致性测试（多歌手曲目/专辑、NULL `performed_by`、流派覆盖、外置 `.lrc`、作品关联、声学特征）、`EXPLAIN QUERY PLAN` 断言（`idx_tracks_added_id`）及 `RecentTagResults` 批量测试，验证 `go test ./internal/storage -run 'TestTagging|TestUpgradeTagging'` 通过。
- [x] 2.2 将 `internal/storage/ai_tagging.go` 中的 `SaveTrackTagResult`、`ResetTrackTagging`、`RecordTaggingFailure` 和 `CreateAISampleRun` 统一改为 `s.beginWrite(ctx)` 前置写锁；补充并发打标写与播放落库的写锁竞争测试，验证 `go test -race ./internal/storage -run 'TestTagging' && go test -race ./internal/tagging` 通过。

## 3. 声学分析与校准候选查询及写锁加固

- [x] 3.1 重构 `internal/storage/audio_analysis.go`：`NextAudioAnalysisCandidate` 改用 `tracks` 主键逆序 + `CROSS JOIN` 早停消除临时 B-tree 排序；`NextAudioCalibrationCandidate` 改用 `idx_audio_files_available_track` 覆盖索引 CTE 并在 `LIMIT target` 后回表；`AudioAnalysisStats` 改用集合化 `avail` CTE；`AudioAnalysisFailures` 直接从失败表驱动；新增轻量 `AudioEligibleTrackCount` 并更新 `internal/analysis/service.go` 中的 `calibrationTarget`。补充 `EXPLAIN QUERY PLAN` 断言与校准样本确定性一致性测试，验证 `go test ./internal/storage -run TestAudioAnalysis && go test -race ./internal/analysis` 通过。
- [x] 3.2 将 `internal/storage/audio_analysis.go` 中的 `SaveTrackAudioFeatures` 和 `SaveAudioAVModel` 改为 `s.beginWrite(ctx)` 前置写锁，补充并发声学特征保存与播放事件写入测试，验证 `go test -race ./internal/storage -run TestAudioAnalysis` 通过。

## 4. 歌手补全与富化候选查询优化

- [x] 4.1 重构 `internal/storage/artist_biography_backfill.go`：将 `artistBiographyPerformerPredicate` 中的 `a.id IN (SELECT ...)` 改为命中 `idx_track_artists_role` 的 `EXISTS`；将 `artistBiographyDisplayableSQL` 中的 `json_each` 替换为显式参数化 `IN (?, ...)`；将 `ArtistBiographyBackfillCandidatesForMode("retranslate")` 改写为可索引拆分 `EXISTS` 配合匹配 `ArtistBiographies` 合并链语义的批量简介加载器。补充 `missing`、`retranslate`（含合并来源简介与 `zh-Hant` 不跳过场景）及 `ArtistBiographyBackfillCounts` 的功能与查询计划测试，验证 `go test ./internal/storage -run 'TestArtistBiography'` 通过。
- [x] 4.2 重构 `internal/storage/artist_image_comprehensive.go`、`internal/storage/identity.go` 与 `internal/storage/enrichment.go`：在 `ArtistImageBackfillCacheScanForMode` 中预读 `web_fallback_enabled` 消除逐歌手设置子查询，并在 `artistsForMatching`、`AlbumsForEnrichment`、`WorksForEnrichment` 中按条件动态省略未启用的可选/强制过滤分支。补充单歌手匹配、头像缓存扫描（命中 `idx_artist_image_candidates_artist_status`）及强制/非强制专辑与作品富化的 `EXPLAIN QUERY PLAN` 和结果一致性测试，验证 `go test ./internal/storage -run 'TestArtistImage|TestArtist.*Match|TestEnrichment|TestWorksForEnrichment|TestAlbumsForEnrichment'` 通过。

## 5. 综合验证与大规模合成库基准回归

- [x] 5.1 在 `internal/storage/` 中新增数千首规模合成曲库性能回归测试，实测验证第一阶段 `NextTaggingCandidates`、`TrackTaggingStamp`、`TaggingStats`、`NextAudioAnalysisCandidate`、`NextAudioCalibrationCandidate` 与 `AudioAnalysisStats` 的低毫秒级延迟，并执行 `go test ./internal/storage ./internal/tagging ./internal/analysis ./internal/enrichment && go build ./...` 确保全部测试与编译通过。
