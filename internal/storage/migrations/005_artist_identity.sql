ALTER TABLE artists ADD COLUMN merged_into_artist_id INTEGER REFERENCES artists(id);
ALTER TABLE artists ADD COLUMN biography TEXT;
ALTER TABLE artists ADD COLUMN user_biography TEXT;
ALTER TABLE artists ADD COLUMN country TEXT;
ALTER TABLE artists ADD COLUMN artist_type TEXT;

CREATE INDEX idx_artists_merged_into ON artists(merged_into_artist_id);

CREATE TABLE metadata_source_settings (
    source TEXT PRIMARY KEY CHECK (source IN ('musicbrainz', 'lastfm')),
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

INSERT INTO metadata_source_settings(source, priority) VALUES ('musicbrainz', 10), ('lastfm', 20);

CREATE TABLE artist_external_profiles (
    id INTEGER PRIMARY KEY,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('musicbrainz', 'lastfm')),
    external_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    sort_name TEXT,
    page_url TEXT,
    biography TEXT,
    country TEXT,
    artist_type TEXT,
    disambiguation TEXT,
    aliases_json TEXT NOT NULL DEFAULT '[]',
    tags_json TEXT NOT NULL DEFAULT '[]',
    raw_json TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    UNIQUE (artist_id, source),
    UNIQUE (source, external_id)
) STRICT;

CREATE TABLE artist_match_candidates (
    id INTEGER PRIMARY KEY,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('musicbrainz', 'lastfm', 'combined', 'file_tag')),
    external_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    sort_name TEXT,
    disambiguation TEXT,
    country TEXT,
    artist_type TEXT,
    mbid TEXT,
    score INTEGER NOT NULL CHECK (score BETWEEN 0 AND 100),
    evidence_json TEXT NOT NULL DEFAULT '[]',
    payload_json TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'candidate' CHECK (status IN ('candidate', 'confirmed', 'rejected')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (artist_id, source, external_id)
) STRICT;

CREATE INDEX idx_artist_match_candidates_artist_score ON artist_match_candidates(artist_id, status, score DESC);

CREATE TABLE artist_match_runs (
    id INTEGER PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed')),
    total_artists INTEGER NOT NULL DEFAULT 0,
    processed_artists INTEGER NOT NULL DEFAULT 0,
    matched_artists INTEGER NOT NULL DEFAULT 0,
    review_artists INTEGER NOT NULL DEFAULT 0,
    failed_artists INTEGER NOT NULL DEFAULT 0,
    current_artist TEXT,
    error_message TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    finished_at TEXT
) STRICT;

CREATE TABLE artist_merge_operations (
    id INTEGER PRIMARY KEY,
    source_artist_id INTEGER NOT NULL REFERENCES artists(id),
    target_artist_id INTEGER NOT NULL REFERENCES artists(id),
    status TEXT NOT NULL CHECK (status IN ('merged', 'rolled_back')),
    source_name TEXT NOT NULL,
    target_name TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    rolled_back_at TEXT,
    CHECK (source_artist_id <> target_artist_id)
) STRICT;

CREATE TABLE artist_merge_relation_moves (
    id INTEGER PRIMARY KEY,
    operation_id INTEGER NOT NULL REFERENCES artist_merge_operations(id) ON DELETE CASCADE,
    relation_type TEXT NOT NULL CHECK (relation_type IN ('album', 'track')),
    owner_id INTEGER NOT NULL,
    position INTEGER NOT NULL,
    join_phrase TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT '',
    target_preexisted INTEGER NOT NULL CHECK (target_preexisted IN (0, 1))
) STRICT;

CREATE TABLE artist_merge_created_aliases (
    operation_id INTEGER NOT NULL REFERENCES artist_merge_operations(id) ON DELETE CASCADE,
    artist_name_id INTEGER NOT NULL,
    PRIMARY KEY (operation_id, artist_name_id)
) STRICT;

CREATE INDEX idx_artist_merge_operations_status ON artist_merge_operations(status, id DESC);
