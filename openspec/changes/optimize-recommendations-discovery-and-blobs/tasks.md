# Tasks

## 1. 声学向量 BLOB 冷热分表与索引迁移（Migration 087）

- [x] 1.1 创建 `internal/storage/migrations/087_split_audio_feature_vectors_and_discovery_index.sql`，将 `track_audio_features` 的 4 个大向量 BLOB 列（`genre`、`moodtheme`、`instrument`、`embedding`）无损迁移至 1:1 伴随表 `track_audio_vectors`（`REFERENCES track_audio_features(track_id) ON DELETE CASCADE`），重建轻量主表及其 3 条索引，并新增 `idx_discovery_mappings_status_subject ON discovery_bangumi_mappings(status, subject_id, rgid)`。同步更新全部 7 处向量 BLOB 读写点（`internal/storage/audio_analysis.go` 中的 `SaveTrackAudioFeatures`、`TrackAudioFeaturesByID`、`AudioCalibrationSamples`、`AudioAVPendingTracks`，`internal/storage/ai_tagging.go` 中的 `AlbumAudioFeatures`，以及 `internal/storage/acoustic.go` 中的 `HasAcousticFeatures`、`buildAcoustic`）。在 `internal/storage/migration_087_test.go` 中补充全新迁移、含真实声学向量的 086→087 无损升级、曲目级联删除、`PRAGMA integrity_check`/`foreign_key_check` 与 `Backups.Validate` 校验测试，验证 `go test ./internal/storage -run 'TestMigration087|TestAudioAnalysis|TestAudioCalibration|TestAudioAVModel|TestAcoustic|TestTagging'` 通过。

## 2. 声学索引查表解码与展开点积优化

- [x] 2.1 优化 `internal/storage/acoustic.go`：引入 65,536 项静态 `[65536]float32` `f16Table` 查表解码、采用边界检查消除与 4 路展开的纯 Go `dotI8`，并导出 `IndexOf` 与 `CosineToIndex` 供批量向量扫描避免重复 map 查找。在 `internal/storage/acoustic_test.go` 中补充全部 65,536 个 `uint16` 值的 `f16` 逐位一致性测试、`dotI8` 精确一致性测试与 `IndexOf`/`CosineToIndex` 测试，验证 `go test -race ./internal/storage -run TestAcoustic` 通过。

## 3. DailyMix 曲库加载、主题构池与写锁优化

- [x] 3.1 优化 `internal/storage/daily_mix.go`：在 `RecLibrary` 中用 `LEFT JOIN artists c` 替代主唱相关子查询、将作品关联 `UNION` 改为 `UNION ALL`、消除每曲 3 个 map 分配的生效标签提取与 `fmt.Sprint` 拼接；新增 `DailyMixItemsByMixIDs` 批量接口并在 `insertDailyMix` 中复用 prepared statement；将 `SaveDailyMixAsPlaylist` 改为 `s.beginWrite(ctx)` 前置写锁。补充 `RecLibrary` 等价性/查询计划测试、`DailyMixItemsByMixIDs` 测试与写锁测试，验证 `go test ./internal/storage -run 'TestDailyMix|TestRecLibrary'` 通过。
- [x] 3.2 优化 `internal/dailymix/themes.go`、`internal/dailymix/select.go` 与 `internal/dailymix/generator.go`：在 `builder` 中单遍预计算 `mildSad`、曲目声学下标及 `mood`/`style`/`mood×style`/`untaggedAcoustic`/`familiar`/`rediscover` 倒排索引消除 216 组全库二重遍历；在 `selectEnv` 中预计算 7 天/14 天边界日期；在 `GenerateReplacement` 中改用 `DailyMixItemsByMixIDs` 消除 N+1。补充确定性等价测试并运行 `go test -v ./internal/dailymix`（含 17k 曲库 `TestLargeLibraryTiming` 计时对比）验证全部通过。

## 4. Discovery 信号索引化、列表惰性解码、排序与写锁优化

- [x] 4.1 优化 `internal/storage/discovery_queries.go`、`internal/storage/discovery_works.go` 与 `internal/storage/discovery.go`：将 `DiscoveryArtistSignals` 和 `DiscoveryWorkSignals` 的近 90 天过滤改为可命中 `idx_playback_sessions_counted` 的 UTC 字符串范围比较；将 `ListDiscovery` 主查询两个相关子查询改写为 `LEFT JOIN`、`DiscoveryOwnership` 去重输入 RGID、非 `acquiring` 标签页改用轻量计数、并将 `secondary_types_json`/`reasons_json`/`evidence_json` 的 `json.Unmarshal` 推迟至最终分页结果；将 Discovery 全部 10 处写事务统一切换至 `s.beginWrite(ctx)` 并在 `SaveDiscoveryCandidates` 中复用 prepared statement。补充 90 天信号查询计划（`idx_playback_sessions_counted`）与边界测试、`ListDiscovery` 全标签页等价测试与并发写锁测试，验证 `go test ./internal/storage -run 'TestDiscovery'` 通过。
- [x] 4.2 优化 `internal/discovery/rank.go`：在 `Rank` 的 `explore` 排序前按 O(N) 预计算权重与 SHA-256 哈希键，消除比较器内 O(N log N) 次重复计算。补充排序一致性测试与大规模基准回归，验证 `go test -v ./internal/discovery` 通过。

## 5. 全量集成回归与编译验证

- [x] 5.1 执行 `go test ./internal/storage ./internal/dailymix ./internal/discovery ./internal/tagging ./internal/analysis ./internal/enrichment && go test -race ./internal/dailymix ./internal/discovery && go build ./...`，确认全部单元测试、竞态检测、备份校验与编译通过。
