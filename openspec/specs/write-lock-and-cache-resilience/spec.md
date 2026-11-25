# write-lock-and-cache-resilience Specification

## Purpose

Defines low-contention SQLite write-lock behavior for background durable workers and playback session persistence, alongside resilient, non-blocking caching for the in-memory acoustic similarity index.

## Requirements

### Requirement: Durable background worker completion updates run progress in O(1)
Completing an item in durable enrichment, artist matching, artist image backfill, or artist biography backfill runs SHALL update the parent run's `processed` and outcome-specific counters via O(1) incremental updates inside the idempotent completion transaction rather than scanning all run items with `COUNT(*)`.

#### Scenario: Completing items accurately increments run counters once
- **WHEN** a worker completes a pending or in-progress durable item with a valid outcome
- **THEN** the parent run's processed count and corresponding outcome counter each increase by 1 in a single statement, and a duplicate completion replay for the same already-completed item leaves counters unchanged

#### Scenario: Biography backfill skipped variants map to skipped_items
- **WHEN** an artist biography backfill item completes with any `skipped_*` outcome (`skipped_no_mbid`, `skipped_state`, `skipped_fresh`, `skipped_source_disabled`)
- **THEN** `skipped_items` and `processed_items` each increment by 1 without issuing a `LIKE 'skipped%'` table scan

### Requirement: Playback event recording and sweeping minimize SQLite lock contention
- Playback event persistence SHALL acquire the SQLite write lock before reading session state inside a write transaction so concurrent background commits wait on `busy_timeout` instead of failing immediately with snapshot conflicts.
- Periodic playback session sweeping SHALL avoid acquiring a SQLite write transaction when no expired open sessions exist.

#### Scenario: Idle playback sweeper performs read-only check
- **WHEN** the periodic expiry sweep runs and no open playback sessions have exceeded their lease
- **THEN** the sweeper returns `0` after a read-only check without executing an `UPDATE` write transaction

#### Scenario: Concurrent playback events and background writes avoid snapshot busy failures
- **WHEN** a playback heartbeat or completion event executes concurrently with background database writes
- **THEN** the playback transaction acquires the write lock up front and commits cleanly without spurious `SQLITE_BUSY_SNAPSHOT` errors

### Requirement: Acoustic index cache preserves valid snapshots across transient errors and avoids blocking readers during rebuilds
The in-memory acoustic snapshot cache SHALL:
- preserve an existing valid snapshot when a staleness probe or rebuild fails due to transient errors or caller cancellation/timeout;
- cache empty/unavailable acoustic state for the check interval so repeated callers do not repeatedly query the database when no analyzed embeddings exist;
- serve existing snapshots without blocking on long-running rebuild I/O, coordinating concurrent rebuilds so at most one rebuild runs at a time.

#### Scenario: Caller timeout during staleness check does not evict a valid snapshot
- **WHEN** a valid acoustic snapshot is cached, the check interval elapses, and a caller with a cancelled or expired context invokes `AcousticIndex`
- **THEN** the call returns the context error without clearing the cached snapshot, and subsequent healthy callers continue using the valid snapshot without forcing a full rebuild

#### Scenario: Empty library acoustic state is negatively cached
- **WHEN** no analyzed acoustic embeddings exist in the database and multiple requests query `AcousticIndex` within the check interval
- **THEN** only the first request queries the database for the latest analysis version and subsequent calls within the interval return `(nil, nil)` from memory

#### Scenario: Concurrent readers are not stalled by a background rebuild
- **WHEN** a stale valid snapshot exists and a rebuild is in progress
- **THEN** concurrent callers requesting `AcousticIndex` or `HasAcousticFeatures` receive the existing snapshot immediately instead of waiting for the full embedding reload

### Requirement: Post-scan missing and purge reconciliation scopes release-group refreshes to affected albums
`MarkMissing`, ID-scoped missing reconciliation, and `PurgeMissing` SHALL recompute file-sourced `album_release_groups` only for albums whose audio files transitioned to missing or were purged, skipping release-group refreshes entirely when zero files changed.

#### Scenario: Zero-missing scan performs no album release-group rewrites
- **WHEN** post-scan missing reconciliation completes with zero files transitioning from available to missing
- **THEN** no `album_release_groups` delete or recompute queries are executed

#### Scenario: Missing or purged file refreshes only its owning albums
- **WHEN** one track in album A transitions to missing or is purged while albums B and C are untouched
- **THEN** album A's file-sourced release-group resolution updates accurately (dropping missing/purged evidence) without rewriting release-group rows for albums B and C

### Requirement: Zero-change scans skip redundant orphan cleanup and avoid correlated full-table scans
Post-scan reconciliation SHALL skip orphan cleanup when a scan neither imported tracks, marked files missing, nor purged missing files. Orphan cleanup and album-work refresh queries SHALL avoid per-row correlated JSON expansion and per-album correlated tag-table scans.

#### Scenario: Unchanged incremental scan skips orphan cleanup
- **WHEN** an incremental scan completes with zero processed tracks, zero newly missing files, and zero purged files
- **THEN** `CleanupOrphans` is not invoked while album-work refresh still detects any stale rule-version or cleared fingerprints cleanly

#### Scenario: Album-work refresh evaluates compilation tags and folder paths without per-album tag scans
- **WHEN** `RefreshAlbumWorks` inspects albums across a large library
- **THEN** compilation tag flags and representative paths are computed via set-based single-pass queries instead of executing a correlated `audio_file_tags` join per album, producing identical work associations and fingerprints

### Requirement: Scan batch import and reconciliation acquire SQLite write locks upfront
Batched track import transactions, missing/purge reconciliation transactions, and album-work refresh write transactions SHALL acquire the SQLite write lock before reading mutable state inside the transaction so concurrent playback or background writes wait cleanly on `busy_timeout` instead of failing with `SQLITE_BUSY_SNAPSHOT`.

#### Scenario: Concurrent playback heartbeats and batched scan imports commit without snapshot lock failures
- **WHEN** a batched scan import or album-work refresh runs concurrently with playback event persistence
- **THEN** write transactions acquire the SQLite write lock upfront and serialize cleanly through `busy_timeout` without spurious `SQLITE_BUSY_SNAPSHOT` errors

### Requirement: AI 打标与声学分析写事务前置获取 SQLite 写锁
AI 打标（`SaveTrackTagResult`、`ResetTrackTagging`、`RecordTaggingFailure`、`CreateAISampleRun`）与声学分析（`SaveTrackAudioFeatures`、`SaveAudioAVModel`）的多语句及读改写事务 SHALL 在读取或修改状态前前置获取 SQLite 写锁，使并发后台任务与播放落库通过 `busy_timeout` 平滑串行而避免 `SQLITE_BUSY_SNAPSHOT` 错误。

#### Scenario: 并发 AI 打标、声学分析落库与播放事件写入无快照锁冲突
- **WHEN** 多个并发 AI 打标协程保存结果或失败记录的同时，声学分析写入特征且播放事件持久化并发执行
- **THEN** 所有写事务均前置获取写锁并平滑提交，不产生偶发 `SQLITE_BUSY_SNAPSHOT` 错误

### Requirement: DailyMix 存为歌单与 Discovery 写事务前置获取 SQLite 写锁，声学快照查表解码与展开点积
`SaveDailyMixAsPlaylist` 及 Discovery 刷新、候选持久化与决策写事务 SHALL 通过 `beginWrite` 前置获取 SQLite 写锁；`AcousticSnapshot` SHALL 使用与原 IEEE-754 float16 逐位一致的查找表解码与展开无界检查 int8 点积，并提供按索引点积接口供批量扫描复用。

#### Scenario: 并发 Discovery 刷新/决策与 DailyMix 落库无快照锁冲突
- **WHEN** Discovery 候选写入、封面更新或映射决策与 DailyMix 存为歌单及播放心跳并发执行
- **THEN** 各写事务前置获取写锁并通过 `busy_timeout` 平滑串行提交，不触发偶发 `SQLITE_BUSY_SNAPSHOT` 错误

#### Scenario: 声学快照 float16 查表解码与展开点积保持数值一致
- **WHEN** `AcousticSnapshot` 构建索引并执行 `Cosine`、`CosineTo`、`CosineToIndex` 与 `Neighbors`
- **THEN** 全部 65536 个 `uint16` 值的 float16 解码结果与参考按位解码逐位一致，点积结果与参考逐元素累加完全一致
