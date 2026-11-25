# Proposal

## Why

当前曲库扫描器与导入管线在 2 万曲以上曲库上存在严重的串行 I/O、高频写锁争用与全库冗余对账问题：一次零变动的增量扫描会对每个未变更文件分别执行 `os.Stat`、外置 `.lrc` 探测、`AudioFileUnchanged`、`AudioProbeNeeded` 以及独立写事务 `TouchAudioFile`（累计 60,000+ 次数据库往返与 20,000 次独立写事务）；首次或全量扫描逐首串行读取元数据并每首开启独立事务且重复重建同专辑的 `album_release_groups`；扫描收尾阶段即使零文件缺失或变更，也会在单个写事务内全库重建所有专辑的 `album_release_groups` 并执行全表 `CleanupOrphans` 与多相关子查询的 `RefreshAlbumWorks`。在第一阶段（查询底座与写锁止血）已完成归档并实机验证的基础上，现在需要系统性重构扫描与对账管线，消除扫描期间的前台写锁阻塞与启动/轮询增量扫描延迟。

## What Changes

- **单遍发现与快照化增量扫描**：`discover()` 在 `WalkDir` 阶段一次性采集音频文件的 `size`/`modTimeNS` 与目录内 `.lrc` 存在集合，消除每文件二次 `os.Stat` 与 99% 无效 `.lrc` 探测；新增单次批量快照读取 `LoadScanFileSnapshot`，在内存中判定未变更、待补探针与 `.lrc` 差异，零变动文件不再逐条写入 `audio_files`，仅对 `.lrc` 变更或探针补全执行增量写入，并基于精确的缺失文件 ID 集合执行缺失标记（完整保留 S1 掉盘/空扫描保护）。
- **有界并发读备与批量事务导入**：将全量/新增/变更文件拆分为有界并发工作池（并行执行 `metadata.Read`、`ProbeAudio`、线程安全的目录封面缓存与封面哈希去重落盘）与单写者批量事务提交（`ImportTrackBatch`）；每批使用 `beginWrite` 预先获取写锁并用曲目级 `SAVEPOINT` 隔离单首异常，批内缓存规范艺人解析结果并将 `refreshAlbumFileReleaseGroups` 延后至批次末尾按受影响专辑去重执行一次。
- **按受影响范围收窄扫描收尾对账与写锁**：`MarkMissing`、`MarkMissingByIDs` 与 `PurgeMissing` 仅针对状态实际发生变更/删除的受影响专辑刷新 `album_release_groups`，零变更时直接跳过；扫描零变更（无导入、无缺失、无清理）时跳过 `CleanupOrphans`；优化 `cleanupOrphansSQL` 消除按艺人的 3 重相关 `json_each` 扫描；将 `RefreshAlbumWorks` 的专辑元信息列表查询改写为单遍 CTE 消除每专辑 3 重相关子查询（含 `audio_file_tags` 3 表关联）并采用 `beginWrite` 预先获取写锁。
- **非目标（Out of Scope）**：不改动 `internal/metadata` 各音频格式解析协议与工作/艺人匹配业务规则；不引入新的数据库 Schema 迁移；不改动 `Watcher` 轮询触发策略或自动增强/每日推荐模块（留作后续独立变更）。

## Capabilities

### New Capabilities
- `library-scanner-pipeline`: Defines single-pass filesystem discovery, snapshot-driven incremental scan skipping, bounded concurrent metadata/artwork preparation, and fault-isolated batched track import semantics.

### Modified Capabilities
- `write-lock-and-cache-resilience`: Extends SQLite write-lock and background reconciliation requirements so post-scan missing/purge reconciliation, orphan cleanup, and album-work refresh scope work strictly to affected albums and avoid redundant full-library write transactions on zero-change scans.

## Impact

- **Affected Code**: `internal/scanner/scanner.go`, `internal/storage/library.go`, `internal/storage/release_groups.go`, `internal/storage/missing.go`, `internal/storage/album_works.go`, and corresponding unit/performance tests in `internal/scanner/` and `internal/storage/`.
- **APIs & Compatibility**: Public HTTP/Admin endpoints, scan job counters (`discovered`/`processed`/`skipped`/`failed`/`missing`), S1 mount-loss protection, legacy creator conversion, identity rebuild (`StartIdentityRebuild`), and existing `Store.ImportTrack` / `Store.MarkMissing` signatures remain fully backwards-compatible.
- **Database Schema**: No new SQL migration required; operates cleanly on the existing schema (Migration 085).
