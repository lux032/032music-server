# Proposal

## Why

在完成基础查询索引（#1）、扫描流水线（#2）与后台候选调度（#3）优化后，剩余的大曲库性能热点集中在三个模块：（1）`track_audio_features` 将约 3.5 KiB 的 4 个声学向量 BLOB 列存放在热元数据列之前，导致每行占满一个 SQLite 页（2 万首曲库约 80–100 MB），使 DailyMix、AI 打标、声学统计、相似曲目与回归回填在读取标量列时被迫扫描数十 MB 冷 BLOB 页并产生巨量 WAL 写放大；（2）DailyMix 的 `RecLibrary` 加载、216 组 `mood × style` 主题全库二重遍历、重复日期解析以及 `dotI8`/float16 声学向量计算在 1.7 万–2 万首曲库上仍有明显 CPU 与内存开销；（3）Discovery 信号统计对 `counted_at` 套用 `julianday(...)` 导致 Migration 085 索引失效，`ListDiscovery` 对全量候选提前执行数万次 `json.Unmarshal` 与相关子查询，`Rank` 在排序比较器内重复计算 SHA-256 与日期解析，且多处 DailyMix/Discovery 写事务仍采用延迟锁。

## What Changes

- **声学大 BLOB 冷热分表与配套索引（Migration 087）**：
  - 新增 `087_split_audio_feature_vectors_and_discovery_index.sql`：创建 1:1 伴随表 `track_audio_vectors(track_id PRIMARY KEY REFERENCES track_audio_features(track_id) ON DELETE CASCADE, genre, moodtheme, instrument, embedding)`，迁移既有 BLOB 数据并重建轻量主表 `track_audio_features`（保留全部外键、CHECK 约束与现有索引），将主表行宽从 ~3.8 KiB 缩减至 ~120 B；同步新增 `idx_discovery_mappings_status_subject ON discovery_bangumi_mappings(status, subject_id, rgid)`。
  - 更新全部 7 处向量 BLOB 读写点（`SaveTrackAudioFeatures`、`TrackAudioFeaturesByID`、`AudioCalibrationSamples`、`AudioAVPendingTracks`、`AlbumAudioFeatures`、`HasAcousticFeatures`、`buildAcoustic`）关联 `track_audio_vectors`，其余所有标量查询零改动直接享受 ~30× 页读减负。
- **DailyMix 数据加载、主题构池与声学向量计算优化**：
  - 优化 `Store.RecLibrary`：将主唱歌手查询中的逐行相关子查询替换为 `LEFT JOIN artists c`；将作品关联 `UNION` 改为 `UNION ALL`（Go 侧已去重）；消除每曲 3 个 map 分配的标签提取热路径与 `fmt.Sprint` 反射拼接；批量化 `GenerateReplacement` 的 `DailyMixItems` 查询（`DailyMixItemsByMixIDs`），并在 `insertDailyMix` 中复用 prepared statement。
  - 优化 `internal/dailymix` CPU 热点：在 `builder` 中单遍预计算 `mildlySad` 标志、曲目声学索引下标及 `mood`/`style`/`mood×style`/`rediscover` 倒排索引，消除 216 次全库遍历与重复 map 查找；在 `selectEnv` 中预计算 `day-7`/`day-14` 避免每曲重复 `time.Parse`；保持选曲与排序结果完全一致。
  - 优化 `internal/storage/acoustic.go`：引入 65536 项静态 `f16` 查找表加速 `buildAcoustic` 解码，并用无界检查展开的 `dotI8` 与按索引点积接口（`IndexOf` / `CosineToIndex`）加速声学近邻与 DailyMix 扩池/歌手电台计算。
- **Discovery 信号查询、列表惰性解码、排序与写锁优化**：
  - 优化 `DiscoveryArtistSignals` 与 `DiscoveryWorkSignals`：将 `julianday(p.counted_at) >= julianday('now','-90 days')` 改为可走 `idx_playback_sessions_counted` 范围扫描的 `p.counted_at >= strftime('%Y-%m-%dT%H:%M:%fZ','now','-90 days')`。
  - 优化 `ListDiscovery` 与 `DiscoveryOwnership`：将 `discovery_candidates` 的两个逐行相关子查询改为 `LEFT JOIN`；去重后再查 `DiscoveryOwnership`；仅在 `tab == "acquiring"` 时加载完整采集任务列表（其余标签页只做 `COUNT(*)`）；将 `secondary_types_json`、`reasons_json` 与映射 `evidence_json` 的 `json.Unmarshal` 推迟至过滤分页后的最终展示页（≤60 条）。
  - 优化 `discovery.Rank`：在 `explore` 排序前按 O(N) 预计算权重与 SHA-256 键，消除排序比较器内 O(N log N) 次重复哈希与日期解析。
- **DailyMix 与 Discovery 前置写锁加固**：
  - 将 `SaveDailyMixAsPlaylist` 及 Discovery 写事务（`BeginDiscoveryRefresh`、`SaveDiscoverySeed`、`SaveDiscoveryWorkSeed`、`SaveDiscoveryCandidates`、`UpdateDiscoveryCover`、`DiscoveryIgnore`、`DiscoveryNotSame`、`DiscoveryOwned`、`DiscoveryMappingDecision`、`DiscoveryMessage`）统一改为 `s.beginWrite(ctx)` 前置写锁，并在 `SaveDiscoveryCandidates` 中复用 prepared statement 批量落库。

## Capabilities

### New Capabilities
- `recommendation-and-discovery-engine`: 每日推荐歌单（DailyMix）数据加载、主题构建与声学计算，以及发现页（Discovery）信号聚合、列表惰性反序列化与候选排序的低复杂度行为规范。

### Modified Capabilities
- `sqlite-storage-foundation`: 新增 Migration 087 将 `track_audio_features` 大向量 BLOB 拆分至 `track_audio_vectors` 并维护 `idx_discovery_mappings_status_subject` 索引，保持升级完整性与备份表结构校验兼容。
- `write-lock-and-cache-resilience`: 扩展前置写锁规范至 DailyMix 存为歌单与 Discovery 刷新/决策写事务，并规范声学索引查表解码与高效向量点积。

## Impact

- **存储层与迁移**：`internal/storage/migrations/087_split_audio_feature_vectors_and_discovery_index.sql`、`internal/storage/audio_analysis.go`、`internal/storage/ai_tagging.go`、`internal/storage/acoustic.go`、`internal/storage/daily_mix.go`、`internal/storage/discovery.go`、`internal/storage/discovery_queries.go`、`internal/storage/discovery_works.go` 及对应迁移/计划/基准测试。
- **推荐与发现服务层**：`internal/dailymix/generator.go`、`internal/dailymix/themes.go`、`internal/dailymix/select.go`、`internal/discovery/rank.go` 及相应一致性与性能测试。
- **兼容性**：无对外 HTTP API 契约变更；现有声学特征数据在 Migration 087 中完整无损迁移至 `track_audio_vectors`，备份创建与快照校验器完全兼容；DailyMix 主题/选曲结果与 Discovery 排序/分页输出语义保持一致。
