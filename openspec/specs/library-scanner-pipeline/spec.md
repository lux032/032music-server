# library-scanner-pipeline Specification

## Purpose

Defines single-pass library discovery, snapshot-driven incremental change detection, bounded concurrent file preparation, and fault-isolated batched persistence for the music library scanner.

## Requirements

### Requirement: Single-pass filesystem discovery avoids redundant stat and missing-LRC probes
Library discovery SHALL capture each discovered audio file's size and modification timestamp alongside the set of `.lrc` files encountered in the directory walk, avoiding repeated per-file `os.Stat` calls and skipping external `.lrc` probes when no matching `.lrc` file exists in the discovered directory set.

#### Scenario: Discovery captures audio metadata and skips absent LRC checks
- **WHEN** a scan walks a library containing audio files where most tracks have no sibling `.lrc` file
- **THEN** each discovered audio file carries its size and modification time from the directory walk without a second `os.Stat`, and external `.lrc` file reads run only for tracks whose sibling `.lrc` path was observed during discovery (still rejecting empty or BOM-only `.lrc` files)

### Requirement: Incremental scans classify unchanged files from a single library snapshot without per-file writes
Incremental scans SHALL preload the library's existing audio file snapshot in a single query and classify unchanged files, pending audio probes, and external `.lrc` changes in memory. Completely unchanged available files SHALL NOT trigger per-file database `SELECT` or `UPDATE` write transactions.

#### Scenario: Zero-change incremental scan avoids per-file audio_files writes
- **WHEN** an incremental scan runs on an already-indexed library with no added, modified, removed, unprobed, or `.lrc`-changed files
- **THEN** all discovered files are counted as skipped, zero per-file `audio_files` write transactions are executed, and all tracks remain available

#### Scenario: Incremental scan applies only probe backfill and LRC deltas on unchanged audio
- **WHEN** an incremental scan encounters unchanged audio files where a legacy track requires audio probing or a sibling `.lrc` file was added or removed
- **THEN** only the tracks requiring probe backfill or `.lrc` status changes are updated in storage, existing tracks remain available even if an audio probe update fails, and scan counters (`skipped`, `failed`, `processed`) match existing semantics

### Requirement: Scan preparation runs concurrently while track persistence commits in fault-isolated batches
For new, changed, or full-scan files, the scanner SHALL prepare metadata reading, audio probing, and artwork hashing/caching across a bounded concurrent worker pool while a single database writer commits prepared tracks in bounded batches with per-track savepoint isolation.

#### Scenario: Concurrent preparation deduplicates folder and artwork I/O safely
- **WHEN** a full or multi-album incremental scan processes multiple tracks sharing folder covers or identical embedded artwork concurrently
- **THEN** folder artwork lookup and artwork cache writes are thread-safe and deduplicated without data races, and every imported track receives its artwork metadata accurately

#### Scenario: Single failing track inside an import batch does not roll back sibling tracks
- **WHEN** a batch of tracks is imported and one track fails due to a database constraint or write error while sibling tracks in the same batch are valid
- **THEN** only the failing track rolls back via its savepoint, a scan error is recorded and `failed` is incremented for that track, and all valid sibling tracks in the batch commit normally

### Requirement: Scan safety guards and job accounting semantics remain intact
The optimized scanner SHALL preserve empty-discovery and >50% missing-ratio (S1) mount-loss guards, shutdown cancellation safety (never marking unseen files missing on cancelled scans), legacy creator conversion, identity rebuild (`StartIdentityRebuild`), and post-scan completion callbacks.

#### Scenario: Cancelled or unmounted scan never marks existing tracks missing
- **WHEN** a scan is cancelled mid-flight or discovers zero files (or fewer than half of previously available files) on a populated library
- **THEN** the scan job finishes with status `failed`, no previously available files are marked missing, and orphan cleanup is not executed

### Requirement: 被删除规则屏蔽的文件按跳过记账并在扫描状态中明确提示
因专辑规则键命中活跃 `delete` 规则而被跳过的文件 SHALL NOT 计入 `processed`（已处理），也不得单独触发扫描后的孤儿清理。系统 SHALL 将每个被规则屏蔽的文件同时计入 `skipped` 与持久化的 `rule_skipped_files` 计数，在扫描完成日志中输出该计数，并在 `rule_skipped_files > 0` 时于控制台扫描状态中明确展示被屏蔽文件数及前往专辑规则管理页的直达链接。

#### Scenario: 规则屏蔽文件计入跳过与规则跳过计数而非已处理
- **WHEN** 增量或完整扫描遇到命中活跃 `delete` 规则的文件以及曲库中未变更的文件，且本次扫描没有新入库、标记缺失或永久清理的文件
- **THEN** 每个被规则屏蔽的文件同时递增 `skipped` 与 `rule_skipped_files`，`processed` 保持为 `0`，满足 `discovered == processed + skipped + failed`，且扫描结束后跳过孤儿清理

#### Scenario: 控制台在扫描期间与结束后展示规则跳过数量及跳转链接
- **WHEN** 最近一次扫描任务的 `rule_skipped_files > 0`
- **THEN** 服务端渲染的控制台页面与 `/admin/status` 轮询接口均返回 `ruleSkippedFiles`，并在页面上明确提示有多少文件被已删除专辑规则跳过以及跳转至专辑规则管理页的链接
