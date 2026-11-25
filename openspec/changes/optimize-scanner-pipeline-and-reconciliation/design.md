# Design

## Context

See `proposal.md` for full motivation and observed bottlenecks. The scanner (`internal/scanner/scanner.go`) currently runs single-threaded discovery → per-file `os.Stat` → per-file `AudioFileUnchanged` / `AudioProbeNeeded` / `TouchAudioFile` (for incremental unchanged files) or serial `metadata.Read` → `cacheArtwork` → `ProbeAudio` → single-track `ImportTrack` (for changed/full files), followed by `MarkMissing` (calling `refreshAllAlbumReleaseGroups` on every album in the library), optional `PurgeMissing` (also calling `refreshAllAlbumReleaseGroups`), `CleanupOrphans` (with 3 correlated `json_each` scans per artist), and `RefreshAlbumWorks` (with 3 correlated subqueries per album, including an unindexed `UPPER(tag.field_name)` join over `audio_file_tags`).

All write-lock semantics must align with the established `write-lock-and-cache-resilience` main spec (specifically `Store.beginWrite(ctx)` using `BEGIN IMMEDIATE` / upfront write lock acquisition and WAL mode with `busy_timeout`). No schema migration is needed.

## Goals / Non-Goals

**Goals:**
- Reduce a zero-change incremental scan on a large library (e.g. 10k–20k tracks) from `O(3N)` DB queries + `O(N)` write transactions + `O(2N)` `os.Stat` calls to **1 snapshot query + 0 `audio_files` writes + 1 directory walk**.
- Accelerate full scans and large incremental imports through a bounded concurrent file-preparation worker pool feeding a single SQLite writer committing bounded batches (50–100 tracks/tx) with per-track savepoint isolation.
- Eliminate redundant album-level and artist-level DB churn inside import batches (in-batch canonical artist cache, deferred once-per-album `refreshAlbumFileReleaseGroups`, and thread-safe artwork hash/folder caches).
- Scope post-scan `album_release_groups` refreshes strictly to affected albums in `MarkMissing`, `MarkMissingByIDs`, and `PurgeMissing`; skip `CleanupOrphans` on zero-change scans; rewrite `cleanupOrphansSQL` and `RefreshAlbumWorks` to set-based single-pass queries.

**Non-Goals:**
- Changing audio tag parsing rules in `internal/metadata` or work/artist matching rules.
- Introducing new SQLite migrations or altering `audio_files` table columns.
- Replacing `Watcher`'s polling fingerprint mechanism (which already runs without DB locks).
- Refactoring background candidate/enrichment matching (`#3`) or DailyMix/BLOB storage (`#4`).

## Decisions

### 1. Single-pass discovery (`discoveredLibrary`) with cached `FileInfo` and `.lrc` set
- **Decision**: Change `Manager.discover()` to return a `discoveredLibrary` struct containing:
  - `files []discoveredFile` (`Path`, `Rel`, `Size`, `ModTimeNS`, `HasCandidateLRC`) populated directly from `entry.Info()` inside `filepath.WalkDir` (skipping entries whose `Info()` fails and recording a `stat_failed` error just like today).
  - `lrcPaths map[string]struct{}` collecting every regular non-symlink `.lrc` file encountered during the walk (keyed by clean path matching `lyrics.DetectLRCPath(audioPath)` — or lowercased on case-insensitive match? Wait: `lyrics.DetectLRCPath(audioPath)` simply strips the audio extension and appends `.lrc`, which is exact case on Linux; collecting exact `path` in `lrcPaths` matches `os.Stat(lyrics.DetectLRCPath(path))` verbatim on Linux/macOS!).
- **Rationale**: Eliminates a second `os.Stat` per audio file and avoids ~20,000 failed `os.Stat` syscalls for non-existent `.lrc` files. Only when `lyrics.DetectLRCPath(path)` is in `lrcPaths` does `externalLRC` open the file and inspect at most 3 bytes for empty/BOM-only checks.
- **Alternative Considered**: Checking `lrc` via `os.ReadDir` per album folder — rejected because `WalkDir` already traverses every entry in every folder once.

### 2. Snapshot-based incremental classification & ID-scoped missing reconciliation (`LoadScanFileSnapshot` + `MarkMissingByIDs`)
- **Decision**:
  - Add `Store.LoadScanFileSnapshot(ctx, libraryID) (map[string]ScanFileSnapshot, error)` returning `{AudioFileID, FileSize, ModifiedAtNS, AudioProbeVersion, HasExternalLRC, Status, HasDuration}` keyed by `relative_path` in **1 single SELECT** (`SELECT af.id, af.relative_path, af.file_size, af.modified_at_ns, af.audio_probe_version, af.has_external_lrc, af.status, (t.duration_ms IS NOT NULL) FROM audio_files af JOIN tracks t ON t.id=af.track_id WHERE af.library_id=?`).
  - During `scanType == "incremental"`, a discovered file is unchanged iff `snap, ok := snapshot[rel]` with `ok && snap.Status == "available" && snap.HasDuration && snap.FileSize == file.Size && snap.ModifiedAtNS == file.ModTimeNS`.
    - If unchanged and `snap.AudioProbeVersion >= storage.AudioProbeVersion` and `lrc == snap.HasExternalLRC`: mark `seen[rel] = true`, increment `skipped`, **0 DB writes**!
    - If unchanged and `snap.AudioProbeVersion < storage.AudioProbeVersion`: run probe backfill (`updateAudioProbe` or batched `UpdateAudioProbe`); even on probe error, keep `seen[rel] = true` (matching `TestFailedAudioProbeStillTouchesFile`), and if `lrc != snap.HasExternalLRC` queue an LRC delta update.
    - If unchanged and only `lrc != snap.HasExternalLRC`: queue `(snap.AudioFileID, lrc)` into a batched `BatchUpdateExternalLRC(ctx, updates)` write (chunked by 500 via `beginWrite`).
  - For missing reconciliation after the scan loop completes (only when not cancelled and S1 guards pass):
    - Compute `missingIDs` from `snapshot`: every entry where `snap.Status == "available"` and `!seen[rel]`.
    - Note for failed files during `full` scan or changed incremental files: if `stat`, `metadata.Read`, or `ImportTrack` fails, `rel` is NOT added to `seen`, matching existing `MarkMissing` semantics where failed files did not receive a fresh `last_scanned_at`.
    - Call `Store.MarkMissingByIDs(ctx, libraryID, missingIDs)`:
      - When `len(missingIDs) == 0`: return `0, nil` immediately (**0 write transactions!**).
      - When `len(missingIDs) > 0`: acquire write lock via `s.beginWrite(ctx)`, chunked `UPDATE audio_files SET status='missing', updated_at=... WHERE library_id=? AND status='available' AND id IN (...)` (collecting affected `DISTINCT t.album_id` first), then call `refreshAlbumFileReleaseGroups` ONLY for those affected albums!
  - Keep existing `Store.MarkMissing(ctx, libraryID, scanStarted)` intact for direct callers/tests, and upgrade its internals to use `s.beginWrite(ctx)` and refresh `album_release_groups` ONLY for albums whose rows actually transitioned to `'missing'` (0 refreshes when `n == 0`).
- **Rationale**: A full audit of the codebase confirmed `audio_files.last_scanned_at` is never read anywhere outside `MarkMissing` and a single unit test (`release_groups_test.go:218`). Avoiding 20,000 `last_scanned_at` updates on zero-change scans removes virtually all SQLite WAL write amplification during startup and watcher incremental scans.
- **Alternative Considered**: Batch-updating `last_scanned_at` in 500-row chunks for all 20,000 unchanged rows — still writes 20,000 dirty B-tree pages and WAL frames on every idle scan with zero functional benefit.

### 3. Bounded Concurrent Worker Pool + Single-Writer Batched Import (`ImportTrackBatch`)
- **Decision**:
  - Partition discovered work items after snapshot classification:
    - Unchanged incremental items (fast-path in-memory, plus optional probe backfill / LRC delta batch).
    - Import items (new/changed incremental files, or all files on `full` scan).
  - Run a bounded worker pool (`workers := clamp(runtime.NumCPU(), 2, 4)`, overridable in tests via `m.workers`) to prepare `preparedImport` values (`ImportInput`, `rel`, `errCode`, `err`):
    - Workers concurrently call `readMetadata`, `cacheArtwork` (guarded by `artworkMu` for `folderArtwork` and `knownArtworkHashes` map so `os.MkdirAll`/`os.Stat`/`os.WriteFile` run at most once per unique artwork hash!), `probe`, and `externalLRC`.
    - Keep `m.workers == 1` fast-path support AND ensure worker goroutines exit immediately on `ctx.Done()` without leaking goroutines.
    - Protect custom test hooks? Wait: check existing tests! Every test with >1 file (`purge_missing_test.go`, `watcher_test.go`) uses pure read-only test hooks, while tests with non-atomic counter increments (`scanner_test.go:185`, `creator_conversion_test.go:28`) only scan 1 file (and with `workerCount := min(configuredWorkers, len(importItems))`, a 1-file scan spawns 1 worker, guaranteeing zero data races in existing tests!).
  - Single DB writer consumes `preparedImport` items from a buffered channel (size `workers * 4`) and flushes batches to `Store.ImportTrackBatch(ctx, batch)` whenever `len(batch) >= 50` OR a flush ticker (`250ms`) fires OR input closes:
    - `ImportTrackBatch` acquires write lock via `s.beginWrite(ctx)`.
    - Uses a per-batch `importBatchState`:
      - `artistCache map[string]int64` (normalized key → canonical artist ID, avoiding repeated `INSERT/SELECT/canonicalArtistID` per artist across tracks in the same batch; cleared or bypassed safely if a track's savepoint rolls back — specifically, only merge track-local artist cache entries into the batch cache AFTER the track's `RELEASE SAVEPOINT` succeeds!).
      - `touchedAlbums map[int64]struct{}` recording every `albumID` (and `previousAlbumID`) touched by successful tracks in the batch.
    - Each track in the batch runs inside `SAVEPOINT import_track`:
      - Calls `importTrackTx(ctx, tx, input, &trackState)` (identical logic to current `ImportTrack`, except `replaceFileReleaseGroupsDeferred` skips the immediate `refreshAlbumFileReleaseGroups` call and records `albumID` in `touchedAlbums`!).
      - If a track fails: `ROLLBACK TO import_track` + `RELEASE import_track`, discard `trackState` artist cache entries, record the error in the returned per-item results slice, and continue with the next track in the batch!
      - If a track succeeds: `RELEASE import_track`, merge `trackState` artist cache entries and `touchedAlbums` into batch state.
    - At the end of the batch (before `tx.Commit()`): run `refreshAlbumFileReleaseGroups(ctx, tx, albumID)` ONCE per unique ID in `touchedAlbums`!
    - Flush `scan_jobs` progress periodically (`scanProgressFlushEvery` / `scanProgressFlushInterval`) and record `scan_errors` for failed reads/imports.
  - `Store.ImportTrack(ctx, input)` becomes a thin wrapper around `ImportTrackBatch(ctx, []ImportInput{input})` (still returning the single track's error directly), ensuring 100% single code path and zero drift between single-track and batch import!
- **Rationale**: Batch size 50–100 tracks (~20–60ms per SQLite write transaction) gives 20–50× fewer fsync/WAL commits while keeping individual write-lock hold times short enough that foreground playback heartbeats or favorite toggles never experience noticeable latency.
- **Alternative Considered**: Single giant transaction for the entire scan — rejected because it would block all foreground writers (playback, playlists, favorites) for minutes and lose all progress on cancellation.

### 4. Scoped `PurgeMissing`, Set-Based `cleanupOrphansSQL`, and Single-Pass `RefreshAlbumWorks`
- **Decision**:
  - `PurgeMissing` (`internal/storage/missing.go`): collect `affectedAlbumIDs` from `SELECT DISTINCT album_id FROM tracks WHERE id IN (...)` before deleting files/tracks, use `s.beginWrite(ctx)` (replacing the dummy `UPDATE audio_files SET id=id WHERE 0` trick cleanly), and at the end refresh `album_release_groups` ONLY for surviving `affectedAlbumIDs` instead of calling `refreshAllAlbumReleaseGroups(ctx, tx)`.
  - `cleanupOrphansSQL` (`internal/storage/missing.go`): replace the 3 correlated `NOT EXISTS(SELECT 1 FROM track_primary_artist_state/operations, json_each(...) WHERE j.value=artists.id)` per-artist subqueries with a single CTE/subquery `artists.id NOT IN (SELECT j.value FROM track_primary_artist_state st, json_each(st.artists_json) j UNION SELECT j.value FROM track_primary_artist_operations op, json_each(op.before_json) j UNION SELECT j.value FROM track_primary_artist_operations op, json_each(op.after_json) j)` (materialized once instead of O(artists × operations)). And in `scanner.run`, skip `CleanupOrphans` when `processed == 0 && missing == 0 && purgedFiles == 0`.
  - `RefreshAlbumWorks` (`internal/storage/album_works.go`):
    - Rewrite the initial album listing query (line 525) with single-pass CTEs:
      - `first_path AS MATERIALIZED (SELECT t.album_id, af.relative_path FROM (SELECT t.album_id, af.relative_path, ROW_NUMBER() OVER (PARTITION BY t.album_id ORDER BY af.id) AS rn FROM tracks t JOIN audio_files af ON af.track_id=t.id) t WHERE rn=1)` (or `MIN(af.id)` grouped by `t.album_id` joined back to `audio_files` — even faster and uses `idx_audio_files_track`!).
      - `va_albums AS MATERIALIZED (SELECT DISTINCT aa.album_id FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE LOWER(COALESCE(ar.user_display_name,ar.display_name))='various artists')`
      - `comp_albums AS MATERIALIZED (SELECT DISTINCT t.album_id FROM audio_file_tags tag JOIN audio_files af ON af.id=tag.audio_file_id JOIN tracks t ON t.id=af.track_id WHERE UPPER(tag.field_name) IN ('COMPILATION','TCMP') AND tag.value='1')`
      - `LEFT JOIN` all three onto `albums a ORDER BY a.id` — **0 correlated subqueries per album**!
    - Switch `tx, e = s.db.BeginTx(ctx, nil)` inside `RefreshAlbumWorks` to `tx, e = s.beginWrite(ctx)` (keeping the 100-album batch commit and per-album `SAVEPOINT album_refresh`).
- **Rationale**: Eliminates O(albums × audio_file_tags) scans on every scan completion while preserving exact rule-upgrade detection (including non-NULL obsolete `work_fingerprint` strings).

## Risks / Trade-offs

- **[Risk] Rollback inside `ImportTrackBatch` leaves stale artist IDs in `artistCache`** → Mitigation: Isolate each track's newly resolved artist entries in a per-track map and merge into the batch cache ONLY after `RELEASE SAVEPOINT import_track` succeeds; on `ROLLBACK TO import_track`, discard the per-track map.
- **[Risk] Concurrent workers invoking `cacheArtwork` race on `folderArtwork` or writing the same artwork cache file simultaneously** → Mitigation: Guard `folderArtwork` and `knownArtworkHashes` with a dedicated mutex (or atomic temp-file + rename in `writeArtwork`), so two workers hashing the same embedded cover never overwrite a half-written cache file.
- **[Risk] Order-dependent album metadata when multiple tracks of the same album are prepared concurrently** → Mitigation: Workers prepare items concurrently, OR preserve deterministic `discovered` order into the single DB writer (Wait: if the writer receives results out of order, two tracks in the same album with differing album-level tags like `VERSION` or `LABEL` could apply in non-deterministic order! **Better**: either group/dispatch in stable order or maintain a bounded ordered reorder buffer / ring in the writer — e.g. worker pool returns `(index, preparedImport)` and writer flushes in index order, or even simpler: `album_artists` / `upsertScannedAlbum` always runs in deterministic filesystem walk order when the writer drains ready items in `index` order!). Keeping deterministic commit order with a tiny reorder buffer (`pending map[int]preparedImport`, advancing `nextIdx`) gives **100% deterministic import order identical to the serial scanner** with full parallel I/O speedup!
- **[Risk] `externalLRC` case sensitivity on case-insensitive filesystems (macOS/SMB)** → Mitigation: On Linux/Docker containers (the primary target), filesystem paths are case-sensitive and match `lyrics.DetectLRCPath`; normalizing `lrcPaths` map keys with `strings.ToLower(path)` when checking candidate presence (and still letting `externalLRC` open `lyrics.DetectLRCPath(path)`) avoids any false negatives on case-insensitive SMB/NTFS mounts while keeping zero false-positive `os.Stat` calls for tracks without any `.lrc` file!
- **[Risk] `TestReleaseGroupMissingPurgeAndOldEAV` or other tests calling `Store.MarkMissing` directly** → Mitigation: Keep `Store.MarkMissing(ctx, libraryID, scanStarted)` signature and semantics unchanged; only optimize its internal release-group refresh to affected albums.

## Migration Plan

No database schema migration is required. Deploy as a standard binary update; rollback is a direct binary revert with full backward compatibility on the existing SQLite database.
