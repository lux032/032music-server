-- Phase 4: multi-source enrichment storage, provenance, and review queues

-- SQLite cannot alter a CHECK constraint in place. Rebuild this small settings
-- table while preserving all existing values and defaults.
CREATE TABLE metadata_source_settings_phase4_new (
    source TEXT PRIMARY KEY CHECK (source IN ('musicbrainz', 'lastfm', 'vgmdb', 'bangumi')),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    priority INTEGER NOT NULL DEFAULT 100,
    api_key TEXT,
    language TEXT NOT NULL DEFAULT 'zh',
    application_name TEXT NOT NULL DEFAULT '032 Music Server',
    application_version TEXT NOT NULL DEFAULT 'dev',
    contact TEXT,
    auto_match INTEGER NOT NULL DEFAULT 1 CHECK (auto_match IN (0, 1)),
    cache_days INTEGER NOT NULL DEFAULT 30 CHECK (cache_days BETWEEN 1 AND 3650),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO metadata_source_settings_phase4_new(
    source, enabled, priority, api_key, language, application_name,
    application_version, contact, auto_match, cache_days, updated_at
)
SELECT source, enabled, priority, api_key, language, application_name,
       application_version, contact, auto_match, cache_days, updated_at
FROM metadata_source_settings;

DROP TABLE metadata_source_settings;
ALTER TABLE metadata_source_settings_phase4_new RENAME TO metadata_source_settings;

INSERT INTO metadata_source_settings(source, priority, auto_match)
VALUES ('vgmdb', 30, 1), ('bangumi', 40, 1)
ON CONFLICT(source) DO NOTHING;

CREATE TABLE enrichment_runs (
    id INTEGER PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','completed','failed','cancelled')),
    scope TEXT NOT NULL CHECK (length(trim(scope)) > 0),
    target_id INTEGER,
    force INTEGER NOT NULL DEFAULT 0 CHECK (force IN (0, 1)),
    total INTEGER NOT NULL DEFAULT 0 CHECK (total >= 0),
    processed INTEGER NOT NULL DEFAULT 0 CHECK (processed >= 0),
    succeeded INTEGER NOT NULL DEFAULT 0 CHECK (succeeded >= 0),
    skipped INTEGER NOT NULL DEFAULT 0 CHECK (skipped >= 0),
    review INTEGER NOT NULL DEFAULT 0 CHECK (review >= 0),
    failed INTEGER NOT NULL DEFAULT 0 CHECK (failed >= 0),
    current TEXT,
    error_message TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    started_at TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    finished_at TEXT,
    CHECK (processed <= total OR total = 0)
) STRICT;

CREATE INDEX idx_enrichment_runs_created ON enrichment_runs(created_at DESC, id DESC);
CREATE INDEX idx_enrichment_runs_status_scope ON enrichment_runs(status, scope, id DESC);

CREATE TABLE http_response_cache (
    source TEXT NOT NULL CHECK (source IN ('musicbrainz','lastfm','vgmdb','bangumi')),
    cache_key TEXT NOT NULL,
    status_code INTEGER NOT NULL CHECK (status_code BETWEEN 100 AND 599),
    body BLOB NOT NULL,
    etag TEXT,
    last_modified TEXT,
    fetched_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    PRIMARY KEY (source, cache_key)
) STRICT;

CREATE INDEX idx_http_response_cache_expiry ON http_response_cache(expires_at);

CREATE TABLE album_external_profiles (
    id INTEGER PRIMARY KEY,
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('musicbrainz','vgmdb')),
    external_id TEXT NOT NULL,
    page_url TEXT,
    title TEXT,
    release_date TEXT,
    original_release_date TEXT,
    catalog_number TEXT,
    label TEXT,
    country TEXT,
    album_type TEXT,
    performed_by TEXT,
    disc_count INTEGER CHECK (disc_count IS NULL OR disc_count > 0),
    raw_json TEXT NOT NULL DEFAULT '{}',
    fetched_at TEXT NOT NULL,
    UNIQUE (album_id, source),
    UNIQUE (source, external_id)
) STRICT;

CREATE INDEX idx_album_external_profiles_album ON album_external_profiles(album_id, source);

CREATE TABLE work_external_profiles (
    id INTEGER PRIMARY KEY,
    work_id INTEGER NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('bangumi')),
    external_id TEXT NOT NULL,
    page_url TEXT,
    title TEXT NOT NULL,
    original_title TEXT,
    translated_title TEXT,
    type TEXT,
    year INTEGER CHECK (year IS NULL OR (year >= 1800 AND year <= 9999)),
    poster_url TEXT,
    raw_json TEXT NOT NULL DEFAULT '{}',
    fetched_at TEXT NOT NULL,
    UNIQUE (work_id, source),
    UNIQUE (source, external_id)
) STRICT;

CREATE INDEX idx_work_external_profiles_work ON work_external_profiles(work_id, source);

CREATE TABLE work_match_candidates (
    id INTEGER PRIMARY KEY,
    work_id INTEGER NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('bangumi')),
    external_id TEXT NOT NULL,
    title TEXT NOT NULL,
    original_title TEXT,
    translated_title TEXT,
    type TEXT,
    year INTEGER CHECK (year IS NULL OR (year >= 1800 AND year <= 9999)),
    page_url TEXT,
    poster_url TEXT,
    score INTEGER NOT NULL CHECK (score BETWEEN 0 AND 100),
    evidence_json TEXT NOT NULL DEFAULT '[]',
    payload_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'candidate' CHECK (status IN ('candidate','confirmed','rejected')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (work_id, source, external_id)
) STRICT;

CREATE INDEX idx_work_match_candidates_review ON work_match_candidates(work_id, status, score DESC, id);

CREATE TABLE artist_relation_candidates (
    id INTEGER PRIMARY KEY,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('musicbrainz','vgmdb')),
    external_id TEXT NOT NULL,
    related_external_id TEXT NOT NULL,
    related_name TEXT NOT NULL,
    relation_type TEXT NOT NULL,
    direction TEXT NOT NULL DEFAULT 'forward' CHECK (direction IN ('forward','backward','both')),
    target_artist_id INTEGER REFERENCES artists(id) ON DELETE SET NULL,
    score INTEGER NOT NULL DEFAULT 0 CHECK (score BETWEEN 0 AND 100),
    evidence_json TEXT NOT NULL DEFAULT '[]',
    payload_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'candidate' CHECK (status IN ('candidate','confirmed','rejected')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (artist_id, source, external_id, related_external_id, relation_type, direction)
) STRICT;

CREATE INDEX idx_artist_relation_candidates_review ON artist_relation_candidates(artist_id, status, score DESC, id);
CREATE INDEX idx_artist_relation_candidates_target ON artist_relation_candidates(target_artist_id, status, id);

-- Every conservative write can record where a field or credit came from.
CREATE TABLE enrichment_provenance (
    id INTEGER PRIMARY KEY,
    entity_type TEXT NOT NULL CHECK (entity_type IN ('album','track','work','album_credit','track_credit','artist_relation')),
    entity_id INTEGER NOT NULL,
    field_name TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('musicbrainz','lastfm','vgmdb','bangumi','manual','file_tag')),
    external_id TEXT,
    run_id INTEGER REFERENCES enrichment_runs(id) ON DELETE SET NULL,
    value_json TEXT NOT NULL DEFAULT 'null',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (entity_type, entity_id, field_name, source)
) STRICT;

CREATE INDEX idx_enrichment_provenance_entity ON enrichment_provenance(entity_type, entity_id, field_name);
CREATE INDEX idx_enrichment_provenance_run ON enrichment_provenance(run_id, id);

-- Durable ownership/source records for structured credits. Scanner imports rebuild
-- file_tag rows, while remote enrichment rows survive and are restored if needed.
CREATE TABLE track_artist_sources (
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    role TEXT NOT NULL CHECK (length(trim(role)) > 0),
    join_phrase TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL CHECK (source IN ('file_tag','musicbrainz','vgmdb','bangumi','manual')),
    external_id TEXT NOT NULL DEFAULT '',
    run_id INTEGER REFERENCES enrichment_runs(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (track_id, artist_id, position, role, source, external_id)
) STRICT;

CREATE INDEX idx_track_artist_sources_track_source ON track_artist_sources(track_id, source, role, position);
CREATE INDEX idx_track_artist_sources_run ON track_artist_sources(run_id, track_id);

-- ImportTrack currently rebuilds track_artists. A remotely owned row is restored
-- immediately after deletion; removing it intentionally requires deleting its
-- source record first. INSERT OR IGNORE means scanner/tag rows still win when an
-- identical key already exists.
CREATE TRIGGER restore_sourced_track_artist_after_delete
AFTER DELETE ON track_artists
WHEN EXISTS (
    SELECT 1 FROM track_artist_sources s
    WHERE s.track_id=OLD.track_id AND s.artist_id=OLD.artist_id
      AND s.position=OLD.position AND s.role=OLD.role
      AND s.source<>'file_tag'
)
AND EXISTS (SELECT 1 FROM tracks WHERE id=OLD.track_id)
AND EXISTS (SELECT 1 FROM artists WHERE id=OLD.artist_id)
BEGIN
    INSERT OR IGNORE INTO track_artists(track_id,artist_id,position,join_phrase,role)
    SELECT track_id,artist_id,position,join_phrase,role
    FROM track_artist_sources
    WHERE track_id=OLD.track_id AND artist_id=OLD.artist_id
      AND position=OLD.position AND role=OLD.role AND source<>'file_tag'
    ORDER BY CASE source WHEN 'manual' THEN 0 ELSE 1 END
    LIMIT 1;
END;
