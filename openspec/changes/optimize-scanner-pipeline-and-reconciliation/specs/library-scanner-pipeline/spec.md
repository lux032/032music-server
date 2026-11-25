# Spec Delta

## Purpose

Defines single-pass library discovery, snapshot-driven incremental change detection, bounded concurrent file preparation, and fault-isolated batched persistence for the music library scanner.

## ADDED Requirements

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
