# Spec Delta

## Purpose

Defines the SQLite runtime connection parameters, performance and foreign-key indexing baseline, and backup schema validation compatibility for the local music library database.

## ADDED Requirements

### Requirement: SQLite runtime performance pragmas are configured on database open
The storage layer SHALL configure each opened SQLite connection with a page cache size of at least 64 MiB (`cache_size = -64000`) and in-memory temporary storage (`temp_store = MEMORY`) in addition to WAL mode, foreign keys, normal synchronous mode, and busy timeout.

#### Scenario: Newly opened database applies runtime pragmas
- **WHEN** the application opens the SQLite database through the standard store opener
- **THEN** `PRAGMA cache_size` reports `-64000` (or larger negative KiB magnitude) and `PRAGMA temp_store` reports `2` (`MEMORY`)

### Requirement: Query planner statistics are refreshed after migration
The storage layer SHALL run `PRAGMA optimize` after completing full database migration so SQLite maintains cardinality statistics for multi-index query planning.

#### Scenario: Migration completes on a populated database
- **WHEN** full database migration finishes on a database containing library tables
- **THEN** `PRAGMA optimize` executes without error and preserves database integrity

### Requirement: Core query, expression, and ordering indexes are present
The database schema SHALL maintain indexes for normalized track title lookups (`LOWER(TRIM(COALESCE(user_title, title)))`), case-insensitive album title sorting (`sort_title COLLATE NOCASE`), global album recency (`added_at DESC`), reverse work alias lookups (`work_aliases(work_id)`), acoustic analysis freshness (`track_audio_features(analysis_version, analyzed_at)`), counted playback time ranges (`playback_sessions(counted_at, track_id)`), and user tags (`track_tags(source, track_id)`).

#### Scenario: Fresh and upgraded databases pass schema and backup validation
- **WHEN** a database is migrated cleanly or upgraded from the previous migration version and validated by the backup snapshot validator
- **THEN** all required query and ordering indexes exist, `PRAGMA integrity_check` returns `ok`, `PRAGMA foreign_key_check` returns zero rows, and backup schema validation succeeds without rejecting any index DDL

### Requirement: Candidate review, acquisition, and merge-move lookup indexes are present
The database schema SHALL maintain status- and target-leading lookup indexes on `work_match_candidates`, `artist_relation_candidates`, `album_subject_candidates`, `enrichment_run_items`, `acquisition_searches`, `acquisition_tasks`, and `artist_merge_relation_moves`.

#### Scenario: Pending review and acquisition lookups use supporting indexes
- **WHEN** the storage layer queries pending match candidates, durable run item outcomes, acquisition records by release group, or track merge relation moves
- **THEN** the query plans use the corresponding supporting indexes instead of scanning the full tables

### Requirement: Non-partial foreign-key cascade indexes cover core parent deletions
The database schema SHALL maintain non-partial foreign-key indexes on `playback_sessions(track_id)`, `artworks(track_id)`, `daily_mix_play_tracks(track_id)`, `track_artist_sources(artist_id)`, `artist_alias_jobs(artist_id)`, `artist_merge_operations(source_artist_id)`, `artist_merge_operations(target_artist_id)`, and `artist_alias_candidates(alias_id)`.

#### Scenario: Parent deletions avoid full child-table scans on indexed foreign keys
- **WHEN** a track, artist, or work row is deleted with foreign keys enabled
- **THEN** SQLite resolves foreign-key cascade checks on the indexed child tables via index seeks rather than full table scans
