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
