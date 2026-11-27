-- Artist canonical native display naming, album_artist MBID identity tracking,
-- merge trigger provenance, and independent AI artist conflict-merge strategy.

ALTER TABLE artists ADD COLUMN original_display_name TEXT;

ALTER TABLE artist_merge_operations
    ADD COLUMN trigger_source TEXT NOT NULL DEFAULT 'manual'
    CHECK (trigger_source IN ('manual', 'auto_match', 'ai_review', 'backfill'));

CREATE TABLE artist_identity_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    canonical_native_name_enabled INTEGER NOT NULL DEFAULT 1 CHECK (canonical_native_name_enabled IN (0, 1)),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO artist_identity_settings(id, canonical_native_name_enabled) VALUES (1, 1);

-- Expand audio_file_artist_identities role constraint to include album_artist.
CREATE TABLE audio_file_artist_identities_new (
    audio_file_id INTEGER NOT NULL REFERENCES audio_files(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'primary' CHECK (role IN ('primary', 'album_artist', 'composer', 'lyricist', 'arranger', 'producer')),
    mbid TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'valid' CHECK (status IN ('valid', 'conflict')),
    PRIMARY KEY (audio_file_id, artist_id, role)
) STRICT;

INSERT INTO audio_file_artist_identities_new(audio_file_id, artist_id, role, mbid, status)
    SELECT audio_file_id, artist_id, role, mbid, status
    FROM audio_file_artist_identities;

DROP TABLE audio_file_artist_identities;
ALTER TABLE audio_file_artist_identities_new RENAME TO audio_file_artist_identities;

CREATE INDEX idx_file_artist_identity_artist ON audio_file_artist_identities(artist_id, role, status);
CREATE INDEX idx_file_artist_identity_file_role ON audio_file_artist_identities(audio_file_id, role);

-- Expand audio_file_identity_checks role constraint to include album_artist.
CREATE TABLE audio_file_identity_checks_new (
    audio_file_id INTEGER NOT NULL REFERENCES audio_files(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('primary', 'album_artist', 'composer', 'lyricist', 'arranger', 'producer')),
    status TEXT NOT NULL CHECK (status IN ('verified', 'blocked')),
    PRIMARY KEY (audio_file_id, role)
) STRICT;

INSERT INTO audio_file_identity_checks_new(audio_file_id, role, status)
    SELECT audio_file_id, role, status
    FROM audio_file_identity_checks;

DROP TABLE audio_file_identity_checks;
ALTER TABLE audio_file_identity_checks_new RENAME TO audio_file_identity_checks;

-- Rebuild ai_review_settings to add independent artist_merge_mode policy.
ALTER TABLE ai_review_settings RENAME TO ai_review_settings_old;

CREATE TABLE ai_review_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    inline_enabled INTEGER NOT NULL DEFAULT 1 CHECK (inline_enabled IN (0, 1)),
    web_search_enabled INTEGER NOT NULL DEFAULT 0 CHECK (web_search_enabled IN (0, 1)),
    model TEXT NOT NULL DEFAULT '',
    album_mode TEXT NOT NULL DEFAULT 'auto' CHECK (album_mode IN ('off', 'recommend', 'auto')),
    track_mode TEXT NOT NULL DEFAULT 'auto' CHECK (track_mode IN ('off', 'recommend', 'auto')),
    work_mode TEXT NOT NULL DEFAULT 'auto' CHECK (work_mode IN ('off', 'recommend', 'auto')),
    artist_mode TEXT NOT NULL DEFAULT 'auto' CHECK (artist_mode IN ('off', 'recommend', 'auto')),
    artist_merge_mode TEXT NOT NULL DEFAULT 'auto' CHECK (artist_merge_mode IN ('off', 'recommend', 'auto')),
    credit_mode TEXT NOT NULL DEFAULT 'recommend' CHECK (credit_mode IN ('off', 'recommend', 'auto')),
    series_mode TEXT NOT NULL DEFAULT 'recommend' CHECK (series_mode IN ('off', 'recommend', 'auto')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO ai_review_settings(
    id, enabled, inline_enabled, web_search_enabled, model,
    album_mode, track_mode, work_mode, artist_mode, artist_merge_mode, credit_mode, series_mode, updated_at
)
SELECT
    id, enabled, inline_enabled, web_search_enabled, model,
    album_mode, track_mode, work_mode, artist_mode, 'auto', credit_mode, series_mode, updated_at
FROM ai_review_settings_old;

DROP TABLE ai_review_settings_old;
