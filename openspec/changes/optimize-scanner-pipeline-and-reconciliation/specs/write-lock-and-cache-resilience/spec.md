# Spec Delta

## ADDED Requirements

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
