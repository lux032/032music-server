# Design

## Context

动机与问题背景见 [proposal.md](proposal.md)。

经过 #1–#3 优化后，针对推荐（DailyMix / SimilarTracks）、发现（Discovery）与大 BLOB 存储的专项代码审计确认了四组最核心的剩余瓶颈：

1. **声学向量大 BLOB 污染热表页缓存**：`track_audio_features` 每行将 4 个总计 ~3.55 KiB 的 BLOB（`genre` 800B + `moodtheme` 112B + `instrument` 80B + `embedding` 2560B）放在 `heads_json`/`valence`/`arousal`/`analyzed_at` 等高频标量列之前，导致 2 万首曲库的主表膨胀到 ~80–100 MB 且存在大量溢出页。然而整个代码库中只有 7 处查询真正需要读取这 4 个 BLOB，其余 `RecLibrary`、`NextTaggingCandidates`、`TaggingStats`、`AudioAnalysisStats`、`NextAudioAnalysisCandidate`、`similarityBatch`、`ApplyAudioAVRegression` 全都只读写几十字节的标量字段。
2. **DailyMix 曲库加载与内存主题构池存在大量冗余扫描与分配**：`RecLibrary` 使用主唱相关子查询与作品 `UNION` 排序，并在 Go 侧对每曲新建 3 个 map 提取有效标签；`builder.Candidates()` 对 216 组 `mood × style` 重复遍历全库 2 万首曲目，且在各池中反复调用 `mildlySad`、查 `b.acoustic.pos` map 及 `storage.DayAfter` 字符串日期解析；`AcousticSnapshot` 的 `f16` 逐位分支解码与未展开 `dotI8` 在构索引与近邻/电台扫描中占据主要 CPU。
3. **Discovery 信号统计与列表页存在索引失效与过度反序列化**：`DiscoveryArtistSignals` 和 `DiscoveryWorkSignals` 用 `julianday(p.counted_at) >= julianday('now','-90 days')` 破坏了 `idx_playback_sessions_counted` 索引；`ListDiscovery` 对全量候选执行两个相关子查询、重复 RGID 查 ownership、非 `acquiring` 页也拉全量采集任务，并对未命中当前 60 条分页的所有候选提前执行 `json.Unmarshal`；`discovery.Rank` 在 `sort.SliceStable` 比较器中重复计算 SHA-256 与日期解析。
4. **写锁与批量插入卫生**：`SaveDailyMixAsPlaylist` 与 `discovery*.go` 的多处写事务仍用 `s.db.BeginTx(ctx, nil)` 延迟锁，且 `SaveDiscoveryCandidates` 与 `insertDailyMix` 逐行循环 `ExecContext` 而未复用 prepared statement。

核心约束：
- 全库审计已确认**没有任何外键指向 `track_audio_features`**，且没有任何测试绕过 `SaveTrackAudioFeatures` 直接向 `track_audio_features` 原始插入行，因此 Migration 087 可安全重建主表。
- Migration 087 必须在 `PRAGMA foreign_keys = ON` 下无损迁移全部既有声学数据，并通过 `PRAGMA integrity_check`、`PRAGMA foreign_key_check` 与 `Backups.Validate` 备份结构校验。
- DailyMix 的主题候选、选曲排序（含同 seed 确定性）及 Discovery 的计数、排名、分页结果必须与原实现保持语义完全一致。

## Goals / Non-Goals

**Goals:**
- 通过 Migration 087 将 `track_audio_features` 瘦身为 ~120 B/行的轻量标量主表，把 4 个大向量 BLOB 迁入 1:1 伴随表 `track_audio_vectors`，将所有标量读取与回归回填的 SQLite 页 I/O 和 WAL 写放大降低 ~30×。
- 显著加速 DailyMix 1.7 万–2 万首曲库的加载、主题构建、声学扩池/电台与换一换流程，同时加速 `AcousticSnapshot` 重建与点积近邻计算。
- 恢复 Discovery 90 天播放信号的索引范围扫描，大幅降低 `ListDiscovery` 的 CPU/内存/SQL 开销并消除 `discovery.Rank` 比较器内 O(N log N) 重复哈希。
- 将 DailyMix/Discovery 写事务统一至 `s.beginWrite(ctx)` 前置写锁，并复用批量写入语句。

**Non-Goals:**
- 不改动 DailyMix 的推荐算法权重、Thompson Sampling 逻辑、能量曲线规则或对外 API 格式。
- 不改动 Discovery 的打分公式、车道分配、去重/限额规则或对外 API 格式。
- 不对 `artworks` 或 `http_response_cache` 做磁盘外置重构（封面已由 HTTP ETag/Cache-Control 与独立缓存目录覆盖，本阶段聚焦收益确定且零外部文件系统耦合的数据库内冷热分表）。

## Decisions

### 决策 1：Migration 087 冷热拆分 `track_audio_features` → `track_audio_features` + `track_audio_vectors`

**方案：**
1. 按“单次拷贝 BLOB、零重命名后遗症”的顺序编写 `087_split_audio_feature_vectors_and_discovery_index.sql`：
   - `ALTER TABLE track_audio_features RENAME TO track_audio_features_old;`
   - 按规范名创建瘦身后的 `track_audio_features`（仅保留 16 个标量/短文本列及原有 `CHECK` / `REFERENCES tracks(id) ON DELETE CASCADE` / `REFERENCES audio_av_models(id) ON DELETE SET NULL`），从 `track_audio_features_old` 拷贝标量数据。
   - 创建 `track_audio_vectors(track_id INTEGER PRIMARY KEY REFERENCES track_audio_features(track_id) ON DELETE CASCADE, genre BLOB NOT NULL, moodtheme BLOB NOT NULL, instrument BLOB NOT NULL, embedding BLOB NOT NULL) STRICT`，从 `track_audio_features_old` **一次性**拷贝 4 个向量 BLOB。
   - `DROP TABLE track_audio_features_old;`（同步释放旧表及其旧索引名）。
   - 在新 `track_audio_features` 上按原名重建 3 条索引：`idx_track_audio_features_version`、`idx_track_audio_features_analyzed`、`idx_track_audio_features_version_analyzed`。
   - 新增 `CREATE INDEX IF NOT EXISTS idx_discovery_mappings_status_subject ON discovery_bangumi_mappings(status, subject_id, rgid);`。
2. 仅更新代码库中真正读写这 4 个 BLOB 的 7 处存储方法：
   - `SaveTrackAudioFeatures`（`audio_analysis.go`）：在同一 `beginWrite` 事务中先 upsert `track_audio_features`，再 upsert `track_audio_vectors`，再清除 `audio_analysis_failures`。
   - `TrackAudioFeaturesByID`、`AudioCalibrationSamples`、`AudioAVPendingTracks`（`audio_analysis.go`）：`JOIN track_audio_vectors v ON v.track_id = f.track_id`。
   - `AlbumAudioFeatures`（`ai_tagging.go`）：`JOIN track_audio_vectors v ON v.track_id = f.track_id`。
   - `HasAcousticFeatures`（`acoustic.go`）：直接查 `SELECT 1 FROM track_audio_vectors WHERE length(embedding) >= 4 LIMIT 1`。
   - `buildAcoustic`（`acoustic.go`）：`SELECT f.track_id, v.embedding FROM track_audio_features f JOIN track_audio_vectors v ON v.track_id = f.track_id WHERE f.analysis_version = ? ORDER BY f.track_id`。

**备选方案与取舍：**
- *仅把 4 个 BLOB 移到同一张表末尾列*：虽能减少部分标量读取对溢出页的访问，但单行仍占据整个 4 KiB 主叶页，全表扫描页数仍高达 ~20,000 页，且 `ApplyAudioAVRegression` 更新 `valence`/`arousal` 仍会整行重写 3.55 KiB BLOB 到 WAL。独立 1:1 伴随表则同时解决读放大与写放大。

### 决策 2：DailyMix 倒排索引构池、`RecLibrary` 去相关化与 `AcousticSnapshot` 查表+展开优化

**方案：**
1. **`Store.RecLibrary`（`internal/storage/daily_mix.go`）**：
   - 主唱歌手查询改用 `LEFT JOIN artists c ON c.id = ar.merged_into_artist_id`，用 `COALESCE(c.user_display_name, c.display_name, ar.user_display_name, ar.display_name, '')` 替代逐行相关子查询。
   - 作品关联查询将 `UNION` 改为 `UNION ALL`（Go 回调已有 `containsID` 去重），消除整表临时去重排序。
   - 内联零 map 分配的生效标签筛选逻辑（用栈上固定整型比较 `user(0) < llm(1) < audio(2)`），并用 `strconv.AppendInt` 构建 `TitleKey` 歌手后缀替代 `fmt.Sprint`。
   - 新增 `DailyMixItemsByMixIDs(ctx, ids)` 批量加载当日全部歌单曲目供 `GenerateReplacement` 调用；在 `insertDailyMix` 中对 `daily_mix_items` 写入使用 `tx.PrepareContext`。
2. **`internal/dailymix`（`themes.go`、`select.go`、`generator.go`）**：
   - 在 `builder` 初始化/首轮构建阶段单遍遍历 `b.tracks`，预计算：
     - `mildSad []bool`（按 track 下标索引，彻底消除数百万次 `negativeWeight` 3-key map 查找）；
     - `acousticIdx []int`（预先调用 `b.acoustic.IndexOf(t.ID)`，把声学扩池与歌手电台中的 ~84 万次 `map[int64]int` 查找降为 O(1) 切片下标）；
     - 倒排切片：`byMood[mood]`、`byStyle[style]`、`byMoodStyle[[2]string{m,s}]`（每曲仅遍历其自身 1–2 个 mood × 1–3 个 style，把 216 × 20,000 = 432 万次迭代降至 ~12 万次）、`untaggedAcoustic`、`familiarTracks`、`rediscoverTracks`。
   - 在 `selectEnv` 构造时预计算 `dayMinus7 = storage.DayAfter(day, -7)` 与 `dayMinus14 = storage.DayAfter(day, -14)`，彻底消除 `novelty()` 中的逐曲日期解析。
3. **`internal/storage/acoustic.go`**：
   - 初始化 65,536 项静态 `[65536]float32` 查找表 `f16Table`，`f16(h)` 直接返回 `f16Table[h]`（对全部 65,536 个 `uint16` 逐位等价）。
   - 在 `dotI8(x, y)` 中使用边界检查消除 + 4 路循环展开累加（保持纯 Go、无 `unsafe`、`int32` 精确结果一致）。
   - 导出 `IndexOf(id int64) (int, bool)` 与 `CosineToIndex(idx int, centroid []int8) (float64, bool)` 供 `dailymix` 直接按索引访问向量。

**备选方案与取舍：**
- *使用 CGO/SIMD 汇编加速 `dotI8`*：引入跨平台（Linux amd64/arm64、Windows）维护成本；纯 Go 4 路展开 + BCE 已可提速 3×–5× 且零平台风险。

### 决策 3：Discovery 索引化 90 天信号、列表惰性反序列化与排序预计算

**方案：**
1. **`DiscoveryArtistSignals` & `DiscoveryWorkSignals`**：
   - 将 `WHERE p.counted_at IS NOT NULL AND julianday(p.counted_at)>=julianday('now','-90 days')` 改为 `WHERE p.counted_at IS NOT NULL AND p.counted_at>=strftime('%Y-%m-%dT%H:%M:%fZ','now','-90 days')`（`SELECT` 列继续保留 `julianday('now')-julianday(p.counted_at)` 计算精确衰减天数），使 SQLite 直接走 `idx_playback_sessions_counted` 范围扫描。
2. **`ListDiscovery` & `DiscoveryOwnership`（`discovery_queries.go`、`discovery_works.go`）**：
   - 将 `ListDiscovery` 主查询中的 2 个相关子查询改写为 `LEFT JOIN discovery_work_music wm`、`LEFT JOIN discovery_bangumi_mappings m` 与 `LEFT JOIN discovery_bangumi_mappings mc ON mc.subject_id=m.subject_id AND mc.status='confirmed' AND mc.rgid<>m.rgid`（命中 PK 与 `idx_discovery_mappings_confirmed`）。
   - `DiscoveryOwnership` 内部对输入 `ids` 去重后再序列化 `json_each`。
   - 非 `acquiring` 标签页通过轻量 `CountActiveAcquisitionTasks(ctx)`（仅 `SELECT COUNT(*) ...`）获取 `counts.Acquiring`，仅在 `tab == "acquiring"` 时加载 `ListAcquisitionTasks`。
   - 推迟 JSON 反序列化：在 `ListDiscovery` 与 `discoveryMappingCatalog` / `discoveryMappingMetadata` 中先保留原始 `secondaryRaw`、`reasonsRaw`、`evidenceRaw` 字符串，在完成所有过滤、计数、`MappingOptions` 分组与 `[offset:end]` 分页切片后，仅对最终返回的 ≤60 条展示项（及其 `MappingOptions`）执行 `json.Unmarshal`。
3. **`discovery.Rank`（`internal/discovery/rank.go`）**：
   - 预计算 `dateTag := now.Format("2006-01-02")` 及每个 `explore` 候选的 `(weight float64, hash [32]byte)`，排序比较器仅比较已缓存结构体字段。

### 决策 4：DailyMix / Discovery 前置写锁与批量语句复用

**方案：**
- 将 `SaveDailyMixAsPlaylist` 以及 `discovery.go` / `discovery_queries.go` / `discovery_works.go` 中的 10 处事务入口全部从 `s.db.BeginTx(ctx, nil)` 替换为 `s.beginWrite(ctx)`（移除多余的 dummy `discoveryLock` 或保留兼容；`beginWrite` 本身即 `BEGIN IMMEDIATE`）。
- 在 `SaveDiscoveryCandidates` 中对 `INSERT INTO discovery_candidates` 和 `INSERT OR IGNORE INTO discovery_possible_matches` 使用 `tx.PrepareContext`。

## Risks / Trade-offs

- **[Migration 087 升级时长与空间] 大曲库（2 万首）首次启动执行 Migration 087 时需拷贝约 70 MB 向量 BLOB 到新表，耗时约 1–3 秒并短暂占用额外数据库/WAL 空间。**
  → 缓解措施：通过先 `RENAME` 再单次拷贝的方式将拷贝量减半；迁移在单个 SQLite 事务中原子执行，失败自动回滚，并在 `migration_087_test.go` 中验证含真实向量数据的 086→087 升级、级联删除与备份校验。
- **[DailyMix 确定性偏差风险] 倒排索引遍历顺序若与原 `b.tracks` 顺序不同，可能导致随机抽样（Efraimidis–Spirakis 消耗 `rng.Float64()` 顺序）产生不同选曲。**
  → 缓解措施：构建倒排索引时严格按 `b.tracks` 的原始下标顺序追加（天然保持原有顺序），并在 `mood×style` 累加时按 `tagging.Moods` / `tagging.Styles` 顺序消费且每曲按 `b.tracks` 顺序追加；补充直接对比优化前后 `Candidates()` 与完整 `GenerateDay` 输出的确定性回归测试。
- **[ListDiscovery 延迟 JSON 校验行为差异] 原逻辑在扫描全部行时若数据库存在损坏 JSON 会提前报错，惰性反序列化仅解析最终展示页。**
  → 缓解措施：所有写入路径均由 Go `json.Marshal` 生成合法 JSON（且带默认 `'[]'`），展示页仍严格校验并返回解码错误，同时消除对非展示行的数万次无效反序列化。
- **[counted_at 时间戳字符串边界格式] 若历史数据中存在非标准格式时间戳（如空格分隔而非 `T`），字符串比较可能与 `julianday` 存在细微差异。**
  → 缓解措施：核查 `playback_sessions.counted_at` 全部写入路径均使用 `strftime('%Y-%m-%dT%H:%M:%fZ','now')` / `AnalysisTimestamp`（ISO-8601 UTC `T...Z`），与 `strftime('%Y-%m-%dT%H:%M:%fZ','now','-90 days')` 严格同格式可字典序比较，并在测试中验证边界时间点（89 天 vs 91 天）。

## Migration Plan

1. 新增 `internal/storage/migrations/087_split_audio_feature_vectors_and_discovery_index.sql`，在事务内完成 `track_audio_features` 冷热拆分、`track_audio_vectors` 数据迁移、旧表清理、3 条原索引重建与 `idx_discovery_mappings_status_subject` 创建。
2. 服务启动时自动通过 `Store.Migrate(ctx)` 升级并执行 `PRAGMA optimize`。
3. 建议升级前保持常规自动备份；一旦完成 Migration 087，如需回退到旧版本二进制文件，应恢复升级前的数据库备份快照。
