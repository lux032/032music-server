-- Last.fm scrobbling. The API key stays in metadata_source_settings('lastfm')
-- so enrichment and scrobbling share one key; this single-row table holds the
-- signing secret, the authorised user session and the connection status.
CREATE TABLE lastfm_scrobble_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    now_playing INTEGER NOT NULL DEFAULT 1 CHECK (now_playing IN (0, 1)),
    api_secret TEXT NOT NULL DEFAULT '',
    session_key TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT '',
    pending_state TEXT NOT NULL DEFAULT '',
    pending_state_at INTEGER NOT NULL DEFAULT 0,
    last_success_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    last_error_at TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO lastfm_scrobble_settings(id) VALUES (1) ON CONFLICT(id) DO NOTHING;

-- Outbox for track.scrobble. Metadata is snapshotted at enqueue time so a
-- rescan or deletion cannot change or lose a play that already happened.
CREATE TABLE lastfm_scrobble_queue (
    id INTEGER PRIMARY KEY,
    track_id INTEGER,
    artist TEXT NOT NULL CHECK (length(artist) > 0),
    track TEXT NOT NULL CHECK (length(track) > 0),
    album TEXT NOT NULL DEFAULT '',
    album_artist TEXT NOT NULL DEFAULT '',
    track_number INTEGER NOT NULL DEFAULT 0,
    duration_seconds INTEGER NOT NULL DEFAULT 0,
    started_at INTEGER NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_lastfm_scrobble_queue_due ON lastfm_scrobble_queue(next_attempt_at, started_at);

-- Start of the most recently counted play (unix ms, estimated as report time
-- minus position). Two reports of the same play share this value, which is
-- how duplicate scrobble calls from one playback are recognised.
ALTER TABLE playback_progress ADD COLUMN last_scrobble_started_ms INTEGER;
