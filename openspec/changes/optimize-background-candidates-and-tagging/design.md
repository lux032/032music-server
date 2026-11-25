# Design

## Context

动机与改造范围见 [proposal.md](proposal.md)。

在完成 #1（索引/查询基线与播放/任务写锁）与 #2（扫描流水线与对账优化）后，对后台子系统（`internal/storage/ai_tagging.go`、`internal/storage/audio_analysis.go`、`internal/storage/artist_biography_backfill.go`、`internal/storage/artist_image_comprehensive.go`、`internal/storage/identity.go`、`internal/storage/enrichment.go` 与 `internal/analysis/service.go`）的代码与 SQL 审计表明，以下热点在 2 万+ 曲库上仍存在明显复杂度退化：

- `taggingBase` 在每次 8 首批次的 `NextTaggingCandidates`、每次单曲 `TrackTaggingStamp(trackID)` 查询，以及管理页每 5 秒一次的 `TaggingStats` 轮询中，都会对全库每首曲目执行 `taggingStampSQL`（9 个相关子查询）+ 3 个 `EXISTS` 检查。
- `audioCandidateCTE` 在每首曲目的 `NextAudioAnalysisCandidate`、`NextAudioCalibrationCandidate`、`AudioAnalysisFailures` 和 `AudioAnalysisStats` 中都会跨全库逐曲执行相关的 `MIN(a2.id)` 子查询。
- 歌手补全与富化候选查询中仍存在 N+1 循环（`ArtistBiographyBackfillCandidatesForMode("retranslate")`）、逐行 `json_each` 展开（`artistBiographyDisplayableSQL`）、全表物化 `IN (SELECT ...)`（`artistBiographyPerformerPredicate`）、逐歌手查设置子查询（`ArtistImageBackfillCacheScanForMode`）及参数化可选/强制守卫（`?=0 OR ...`、`? OR NOT EXISTS(...)`）。
- AI 打标与声学分析中的多处写事务仍使用延迟锁 `s.db.BeginTx(ctx, nil)`，而非 `s.beginWrite(ctx)`。

核心约束：
- `taggingStampSQL` 的输出字符串必须与数据库中已存储的 `track_tag_meta.source_stamp` 和 `ai_tagging_failures.source_stamp` **逐字节 100% 一致**，绝不能在升级时触发全库重打标。
- `internal/tagging` 与 `internal/analysis` 的同步单元测试会在连续调用 `Service.Step(ctx)` 之间直接修改数据库行、推进假时钟（`opts.Now()`）或更改配置，且不一定调用 `Wake()`；因此候选查询必须保持对 SQLite 的实时一致性，不依赖容易脏读的内存候选队列缓存。
- 迁移 `086` 中新增的所有 SQLite 索引必须通过现有备份表结构校验器（`backup.go`）。

## Goals / Non-Goals

**Goals:**
- 消除 `NextTaggingCandidates` 在全库初始打标与新歌入库时的 O(N²) 相关子查询计算，并通过按表预聚合的集合 CTE 将 `TaggingStats` 与过期检查开销降低一个数量级以上。
- 消除 `NextAudioAnalysisCandidate`、`NextAudioCalibrationCandidate`、`AudioAnalysisFailures`、`AudioAnalysisStats` 与 `analysis.Service.calibrationTarget` 中的全表排序和冗余回表扫描。
- 消除歌手简介/头像补全、歌手匹配和专辑/作品富化候选查询中的 N+1 循环、不可索引 `OR IN`、逐行 `json_each` 与参数化可选/强制分支。
- 将 AI 打标和声学分析的多语句/读改写事务统一到 `s.beginWrite(ctx)` 前置写锁。

**Non-Goals:**
- 不改变 `taggingStampSQL` 格式，不新增 stamp 换算数据迁移。
- 不在 `tagging.Service` 或 `analysis.Service` 中引入有状态的内存候选预取队列，避免与直接 DB 修改、人工覆盖或测试时钟推进产生一致性偏差。
- 不涉及 DailyMix、Discovery 或大 BLOB 分表（留待 #4 处理）。

## Decisions

### 决策 1：两阶段 `NextTaggingCandidates` + 集合预聚合 `taggingBaseSetSQL`（不引入内存候选缓存）

**方案：**
1. **迁移 086** 新增 `idx_tracks_added_id ON tracks(added_at DESC, id DESC)`。
2. **第一阶段（未打标新曲，`m.track_id IS NULL`）**：以 `tracks t CROSS JOIN albums a ON a.id = t.album_id` 固定驱动顺序，按 `t.added_at DESC, t.id DESC` 扫描，`LEFT JOIN track_tag_meta m`、`LEFT JOIN track_audio_features f`、`LEFT JOIN ai_tagging_failures x ON x.track_id = t.id AND (x.permanent = 1 OR x.next_retry_at > ?)`。过滤条件为 `m.track_id IS NULL`、存在可用音频文件、满足声学就绪条件，以及 `(x.track_id IS NULL OR x.source_stamp <> (<taggingStampSQL>))`，配合 `LIMIT ?`。由于 SQLite 对 `OR` 短路求值，99.9%+ 无活跃失败记录的新曲在筛选阶段完全不会计算 `<taggingStampSQL>`，仅对最终命中的 `<= limit` 首曲目在外层 `SELECT` 计算一次 stamp。
3. **第二阶段（已打标曲目，`m.track_id IS NOT NULL`，仅当第一阶段不足 `limit` 时运行）**：
   - 空表早退：若 `track_tag_meta` 为空则直接跳过第二阶段。
   - 全量版本升级快速路径：若 `NOT EXISTS(SELECT 1 FROM track_tag_meta WHERE vocab_version = ? AND prompt_version = ?)`（即所有已打标曲目均属于旧版本），直接复用 `idx_tracks_added_id` 顺序与 `INNER JOIN track_tag_meta m` 截断 `LIMIT rem`，避免全库 stamp 计算。
   - 通用过期检查路径（及 `TaggingStats`）：使用共享的集合预聚合 CTE（`taggingBaseSetSQL`），对 `avail_af`（`audio_files`）、`alb_cnt`（`tracks`）、`trk_art`（`track_artists`）、`alb_art`（`album_artists`）、`trk_genre_ov`（`track_genre_overrides`）、`trk_genre`（`track_genres`）、`wt_sig`（`work_tracks`）、`aw_sig`（`album_works`）每表只做一次 `GROUP BY`，再统一 `LEFT JOIN` 到 `tracks`。
   - 严格保证 `album_artists` 拼接顺序一致：`alb_art` 同时生成 `ord_names`（`ORDER BY aa.position, ar.id`，对应 `taggingTrackArtistSQL`）与 `pk_names`（`ORDER BY aa.artist_id, aa.position`，对应 `album_artists` 主键 `(album_id, artist_id, position)` 扫描顺序下的 `taggingAlbumArtistSQL`）。
4. **`TrackTaggingStamp(ctx, trackID)`**：直接按 `WHERE t.id = ? AND EXISTS(...)` 单曲主键查询，移除对全库 `taggingBase` 的依赖。
5. **`RecentTagResults`**：收集返回的 `TrackID` 列表，通过分块 `SELECT ... FROM track_tags WHERE track_id IN (...)` 一次性加载全部标签并保持与 `TrackTags` 完全一致的排序规则。

**备选方案与取舍：**
- *触发器/脏标记维护物化 stamp 列*：涉及 8+ 张关联表（`tracks`、`albums`、`artists`、`track_artists`、`album_artists`、`genres`、`audio_files`、`track_audio_features`、`work_tracks`、`album_works`），触发器维护复杂度高且容易漏掉失效路径。
- *在 `tagging.Service` 中维护内存候选预取队列*：存在与人工编辑、并发修改或测试时钟推进不同步的风险；而第一阶段本身已是 O(limit)、第二阶段已集合化单遍扫描，无需引入缓存复杂度。

### 决策 2：主键逆序早停 `NextAudioAnalysisCandidate`、覆盖索引 `NextAudioCalibrationCandidate` 与定向 `AudioAnalysisFailures` / `AudioEligibleTrackCount`

**方案：**
1. **`NextAudioAnalysisCandidate`**：直接从 `tracks t` 主键逆序驱动（`ORDER BY t.id DESC`），通过 `CROSS JOIN audio_files af ON af.id = (SELECT MIN(a2.id) FROM audio_files a2 WHERE a2.track_id = t.id AND a2.status = 'available')` 与 `LEFT JOIN track_audio_features f ON f.track_id = t.id` 配合 `LIMIT 1`。使用 `CROSS JOIN` 固定 `tracks` 作为外层表，消除临时 B-tree 排序并在命中第一首未分析/过期曲目时立即早停。
2. **`NextAudioCalibrationCandidate`**：以 `avail AS (SELECT track_id, MIN(id) AS file_id FROM audio_files WHERE status = 'available' GROUP BY track_id)` 驱动——仅扫描部分覆盖索引 `idx_audio_files_available_track(track_id, id) WHERE status='available'`。将 `audio_analysis_failures`（`permanent = 1`）做 `LEFT JOIN`，且仅在存在永久失败记录时才按条件关联 `audio_files` 校验指纹；随后仅凭整数列按 `(a.track_id * 2654435761) % 4294967296, a.track_id` 排序截断 `LIMIT ?`，最后才对 `<= target` 首中选曲目回表关联 `tracks`、`audio_files` 与 `track_audio_features`。
3. **`AudioAnalysisStats`**：用上述 `avail` 覆盖索引 CTE 替代 `audioCandidateCTE` 中的逐曲相关 `MIN(a2.id)` 子查询，再关联 `audio_files`、`track_audio_features` 和 `audio_analysis_failures`。
4. **`AudioEligibleTrackCount(ctx)`**：新增 `SELECT COUNT(DISTINCT track_id) FROM audio_files WHERE status = 'available'`，供 `analysis.Service.calibrationTarget` 直接获取可分析曲目总数，不再借道运行完整的 `AudioAnalysisStats`。
5. **`AudioAnalysisFailures`**：改为直接从 `audio_analysis_failures x`（`WHERE x.analysis_version = ?`）驱动，只对实际失败的曲目关联 `tracks` 与主可用文件。

**备选方案与取舍：**
- *在 `analysis.Service` 中缓存待分析/待校准队列*：新歌导入、文件变更、永久失败与退避到期均需失效联动；保持 SQL 无状态且通过覆盖索引/主键逆序控制在数毫秒级（远小于单曲秒级 Essentia 推理耗时），零缓存一致性风险。

### 决策 3：歌手补全与富化候选查询去相关化与动态分支裁剪

**方案：**
1. **`ArtistBiographyBackfillCandidatesForMode("retranslate")`**：
   - 将 `OR b.artist_id IN (...)` 拆分为两个可走索引的 `EXISTS` 分支（`b.artist_id = a.id` 与 `artists ma JOIN artist_biographies b ON b.artist_id = ma.id WHERE ma.merged_into_artist_id = a.id`）。
   - 在 `targetTag != "zh-Hant" && targetShort != ""` 路径中，用分块批量简介加载器替代逐歌手调用 `s.ArtistBiographies(ctx, cand.ID)` 的 N+1 循环，并严格对齐 `ArtistBiographies` 的合并链继承语义。
2. **`artistBiographyDisplayableSQL` 与 `artistBiographyPerformerPredicate`**：
   - 将 `b.language IN (SELECT value FROM json_each(?))` 替换为显式参数化 `b.language IN (?, ...)`（语言列表为空时回退为 `0`），使 SQLite 直接利用 `idx_artist_biographies_artist(artist_id, status, language, source)`。
   - 将 `artistBiographyPerformerPredicate` 中的 `a.id IN (SELECT artist_id FROM track_artists WHERE role='primary')` 替换为 `EXISTS(SELECT 1 FROM track_artists ta WHERE ta.role='primary' AND ta.artist_id=a.id)`，使所有单歌手检查（`ArtistIsPerformer`、`ArtistBiographyBackfillSubject`、`ArtistBiographyBackfillRetrySubject`、`aliasIdentityGuard`）走 `idx_track_artists_role` 覆盖索引点查。
3. **`ArtistImageBackfillCacheScanForMode`**：
   - 在查询前先读取一次 `web_fallback_enabled` 设置并作为静态分支/参数传入，消除逐歌手子查询 `(SELECT web_fallback_enabled FROM artist_image_settings WHERE id=1)=0`。
   - 迁移 086 新增 `idx_artist_image_candidates_artist_status ON artist_image_candidates(artist_id, status)`，使 `NOT EXISTS(... artist_image_candidates ...)` 走覆盖索引查找。
4. **可选/强制守卫动态拼接**：
   - `artistsForMatching(ctx, id)`：仅当 `id > 0` 时追加 `AND ar.id=?`（移除 `?=0 OR ar.id=?`）。
   - `AlbumsForEnrichment`：仅当 `!force` 时拼接 `WHERE NOT EXISTS(...)`。
   - `WorksForEnrichment`：仅当 `!force` 时拼接非强制复杂条件块。

### 决策 4：AI 打标与声学分析写事务统一前置 `beginWrite` 写锁

**方案：** 将 `SaveTrackTagResult`、`ResetTrackTagging`、`CreateAISampleRun`、`SaveTrackAudioFeatures` 和 `SaveAudioAVModel` 中的延迟事务 `s.db.BeginTx(ctx, nil)` 替换为 `s.beginWrite(ctx)`，并将 `RecordTaggingFailure` 的“先读尝试次数再写入”流程包入 `s.beginWrite(ctx)` 事务（与 `RecordAudioAnalysisFailure` 保持一致）。

**理由：** `tagging.Service` 默认并发 4（最高 16）个协程同时写 `SaveTrackTagResult` / `RecordTaggingFailure`，同时后台还有声学分析、扫描和播放心跳并发落库。通过 `s.beginWrite(ctx)` 执行 `BEGIN IMMEDIATE`，可让 SQLite 写事务平滑排队等待 `busy_timeout`，消除中途锁升级导致的 `SQLITE_BUSY_SNAPSHOT` 瞬时报错。

## Risks / Trade-offs

- **[Stamp 偏差风险] `taggingBaseSetSQL` 在 `GROUP_CONCAT` 排序或空值回退上的细微差异可能导致已有库全量重打标。**
  → 缓解措施：逐项对齐所有 `COALESCE`、分隔符、空集默认值（`work_tracks`/`album_works` 无行时为 `'0:'`，流派/专辑歌手为空时为 `''`，缺歌手时为 `'Unknown Artist'`）及 `ORDER BY` 顺序（`pk_names` 显式使用 `ORDER BY aa.artist_id, aa.position`）；新增覆盖多歌手曲目、多歌手专辑、NULL `performed_by`、流派覆盖、多音频文件、外置 `.lrc`、作品关联与缺声学特征的逐字节一致性测试，直接与原 `taggingStampSQL` 逐行比对。
- **[查询计划回退风险] 若不约束连接顺序，SQLite 规划器可能选 `albums` 或 `track_tag_meta` 作为外层表，重新引入临时 B-tree 排序。**
  → 缓解措施：对依赖有序早停的查询使用 `CROSS JOIN` 固定外层驱动表，并在存储测试中加入 `EXPLAIN QUERY PLAN` 回归断言（验证第一阶段 `NextTaggingCandidates` 命中 `idx_tracks_added_id`、`NextAudioAnalysisCandidate` 主键逆序且无 `USE TEMP B-TREE FOR ORDER BY`、`NextAudioCalibrationCandidate` 走覆盖索引、`ArtistImageBackfillCacheScanForMode` 命中 `idx_artist_image_candidates_artist_status`）。
- **[备份表结构校验风险] 迁移 086 的新索引 DDL 若不符合规范可能被备份校验器拒绝。**
  → 缓解措施：严格沿用与迁移 085 一致的标准 `CREATE INDEX IF NOT EXISTS` 语法，并在 `migration_086_test.go` 中执行 085→086 升级与 `ValidateBackupSchema` 校验。
- **[批量重译简介语义风险] `retranslate` 批量加载简介若与 `ArtistBiographies` 的合并链深度或过滤口径不一致，可能造成候选集偏差。**
  → 缓解措施：实施前核对 `ArtistBiographies` 的完整实现以确保 100% 同口径，并补充含合并来源歌手简介与繁体中文（`zh-Hant`）不跳过场景的单元测试。

## Migration Plan

1. 新增 `internal/storage/migrations/086_background_candidate_perf_indexes.sql`，包含两条幂等建索引语句（`idx_tracks_added_id`、`idx_artist_image_candidates_artist_status`），不修改任何业务表数据。
2. 既有数据库启动时通过 `Store.Migrate(ctx)` 自动升级并执行 `PRAGMA optimize`。
3. 回滚策略：代码回滚无需删除这两条纯附加索引即可安全运行；如需彻底回退，可直接 `DROP INDEX` 且无任何数据影响。
