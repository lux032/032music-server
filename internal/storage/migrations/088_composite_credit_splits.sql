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
    credit_mode TEXT NOT NULL DEFAULT 'recommend' CHECK (credit_mode IN ('off', 'recommend', 'auto')),
    series_mode TEXT NOT NULL DEFAULT 'recommend' CHECK (series_mode IN ('off', 'recommend', 'auto')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO ai_review_settings(
    id, enabled, inline_enabled, web_search_enabled, model,
    album_mode, track_mode, work_mode, artist_mode, credit_mode, series_mode, updated_at
)
SELECT
    id, enabled, inline_enabled, web_search_enabled, model,
    album_mode, track_mode, work_mode, artist_mode, credit_mode, series_mode, updated_at
FROM ai_review_settings_old;

DROP TABLE ai_review_settings_old;

CREATE TABLE artist_credit_splits (
    composite_artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
    part_ids_json TEXT NOT NULL CHECK (json_valid(part_ids_json)),
    part_names_json TEXT NOT NULL CHECK (json_valid(part_names_json)),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'reverted')),
    source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'ai')),
    decision_id INTEGER REFERENCES ai_review_decisions(id) ON DELETE SET NULL,
    snapshot_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(snapshot_json)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_artist_credit_splits_status ON artist_credit_splits(status, composite_artist_id);
CREATE INDEX idx_artist_credit_splits_decision ON artist_credit_splits(decision_id);

CREATE TABLE artist_composite_exemptions (
    artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
