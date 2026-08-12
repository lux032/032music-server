CREATE TABLE libraries (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    root_path TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE TABLE artists (
    id INTEGER PRIMARY KEY,
    display_name TEXT NOT NULL,
    sort_name TEXT NOT NULL,
    identity_key TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE TABLE artist_names (
    id INTEGER PRIMARY KEY,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    value TEXT NOT NULL,
    name_type TEXT NOT NULL DEFAULT 'primary',
    language TEXT,
    script TEXT,
    UNIQUE (artist_id, value, name_type)
) STRICT;

CREATE TABLE albums (
    id INTEGER PRIMARY KEY,
    library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    sort_title TEXT NOT NULL,
    grouping_key TEXT NOT NULL,
    release_date TEXT,
    release_year INTEGER,
    disc_count INTEGER NOT NULL DEFAULT 1 CHECK (disc_count > 0),
    added_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (library_id, grouping_key)
) STRICT;

CREATE TABLE album_artists (
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    join_phrase TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (album_id, artist_id, position)
) STRICT;

CREATE TABLE tracks (
    id INTEGER PRIMARY KEY,
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    sort_title TEXT NOT NULL,
    disc_number INTEGER NOT NULL DEFAULT 1 CHECK (disc_number > 0),
    track_number INTEGER NOT NULL DEFAULT 0 CHECK (track_number >= 0),
    duration_ms INTEGER CHECK (duration_ms IS NULL OR duration_ms >= 0),
    release_date TEXT,
    added_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE TABLE track_artists (
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    join_phrase TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'primary',
    PRIMARY KEY (track_id, artist_id, position, role)
) STRICT;

CREATE TABLE audio_files (
    id INTEGER PRIMARY KEY,
    library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    track_id INTEGER REFERENCES tracks(id) ON DELETE SET NULL,
    relative_path TEXT NOT NULL,
    file_size INTEGER NOT NULL CHECK (file_size >= 0),
    modified_at_ns INTEGER NOT NULL,
    content_hash TEXT,
    codec TEXT,
    container TEXT,
    mime_type TEXT,
    bitrate INTEGER,
    sample_rate INTEGER,
    bit_depth INTEGER,
    channels INTEGER,
    status TEXT NOT NULL DEFAULT 'available' CHECK (status IN ('available', 'missing', 'error')),
    last_scanned_at TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (library_id, relative_path)
) STRICT;

CREATE TABLE audio_file_tags (
    audio_file_id INTEGER NOT NULL REFERENCES audio_files(id) ON DELETE CASCADE,
    field_name TEXT NOT NULL,
    value TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    source TEXT NOT NULL DEFAULT 'embedded' CHECK (source IN ('embedded', 'filename', 'folder')),
    PRIMARY KEY (audio_file_id, field_name, position, source)
) STRICT;

CREATE TABLE artworks (
    id INTEGER PRIMARY KEY,
    album_id INTEGER REFERENCES albums(id) ON DELETE CASCADE,
    track_id INTEGER REFERENCES tracks(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL,
    source_path TEXT,
    content_hash TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    width INTEGER,
    height INTEGER,
    byte_size INTEGER NOT NULL CHECK (byte_size >= 0),
    is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (album_id IS NOT NULL OR track_id IS NOT NULL)
) STRICT;

CREATE TABLE genres (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE
) STRICT;

CREATE TABLE track_genres (
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    genre_id INTEGER NOT NULL REFERENCES genres(id) ON DELETE CASCADE,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    PRIMARY KEY (track_id, genre_id)
) STRICT;

CREATE TABLE scan_jobs (
    id INTEGER PRIMARY KEY,
    library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    scan_type TEXT NOT NULL CHECK (scan_type IN ('full', 'incremental')),
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled')),
    discovered_files INTEGER NOT NULL DEFAULT 0 CHECK (discovered_files >= 0),
    processed_files INTEGER NOT NULL DEFAULT 0 CHECK (processed_files >= 0),
    skipped_files INTEGER NOT NULL DEFAULT 0 CHECK (skipped_files >= 0),
    failed_files INTEGER NOT NULL DEFAULT 0 CHECK (failed_files >= 0),
    missing_files INTEGER NOT NULL DEFAULT 0 CHECK (missing_files >= 0),
    current_relative_path TEXT,
    error_message TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    started_at TEXT,
    finished_at TEXT
) STRICT;

CREATE TABLE scan_errors (
    id INTEGER PRIMARY KEY,
    scan_job_id INTEGER NOT NULL REFERENCES scan_jobs(id) ON DELETE CASCADE,
    relative_path TEXT NOT NULL,
    error_code TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_albums_library_added ON albums(library_id, added_at DESC, id DESC);
CREATE INDEX idx_albums_sort_title ON albums(sort_title, id);
CREATE INDEX idx_album_artists_artist ON album_artists(artist_id, album_id);
CREATE INDEX idx_tracks_album_order ON tracks(album_id, disc_number, track_number, id);
CREATE INDEX idx_track_artists_artist ON track_artists(artist_id, track_id);
CREATE INDEX idx_audio_files_track ON audio_files(track_id);
CREATE INDEX idx_audio_files_status ON audio_files(library_id, status);
CREATE INDEX idx_audio_files_fingerprint ON audio_files(library_id, file_size, modified_at_ns);
CREATE INDEX idx_audio_file_tags_field_value ON audio_file_tags(field_name, value);
CREATE INDEX idx_artworks_album_primary ON artworks(album_id, is_primary DESC);
CREATE INDEX idx_scan_jobs_library_created ON scan_jobs(library_id, created_at DESC);
CREATE INDEX idx_scan_errors_job ON scan_errors(scan_job_id, id);
