-- ListenBrainz scrobbling, now-playing, love feedback sync, and resolved track ID cache.
CREATE TABLE listenbrainz_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    now_playing INTEGER NOT NULL DEFAULT 1 CHECK (now_playing IN (0, 1)),
    sync_loves INTEGER NOT NULL DEFAULT 0 CHECK (sync_loves IN (0, 1)),
    server_url TEXT NOT NULL DEFAULT 'https://api.listenbrainz.org',
    user_token TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT '',
    lookup_unsupported INTEGER NOT NULL DEFAULT 0 CHECK (lookup_unsupported IN (0, 1)),
    feedback_unsupported INTEGER NOT NULL DEFAULT 0 CHECK (feedback_unsupported IN (0, 1)),
    last_success_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    last_error_at TEXT,
    last_love_error TEXT NOT NULL DEFAULT '',
    last_love_error_at TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO listenbrainz_settings(id) VALUES (1) ON CONFLICT(id) DO NOTHING;

-- Outbox for POST /1/submit-listens (single / import). Metadata and embedded
-- MBIDs are snapshotted at enqueue time so rescans or deletions never lose plays.
CREATE TABLE listenbrainz_scrobble_queue (
    id INTEGER PRIMARY KEY,
    track_id INTEGER,
    artist TEXT NOT NULL CHECK (length(artist) > 0),
    track TEXT NOT NULL CHECK (length(track) > 0),
    album TEXT NOT NULL DEFAULT '',
    album_artist TEXT NOT NULL DEFAULT '',
    track_number INTEGER NOT NULL DEFAULT 0,
    duration_seconds INTEGER NOT NULL DEFAULT 0,
    recording_mbid TEXT NOT NULL DEFAULT '',
    release_mbid TEXT NOT NULL DEFAULT '',
    artist_mbids_json TEXT NOT NULL DEFAULT '[]',
    started_at INTEGER NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_listenbrainz_scrobble_queue_due ON listenbrainz_scrobble_queue(next_attempt_at, started_at);

-- Pending local -> ListenBrainz recording feedback (1 = love, 0 = unlove).
CREATE TABLE listenbrainz_love_queue (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    desired INTEGER NOT NULL CHECK (desired IN (0, 1)),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_listenbrainz_love_queue_due ON listenbrainz_love_queue(next_attempt_at);

-- Resumable manual backfill of existing local favourites to ListenBrainz.
CREATE TABLE listenbrainz_love_backfill (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    state TEXT NOT NULL DEFAULT 'idle' CHECK (state IN ('idle', 'running', 'done', 'cancelled')),
    total INTEGER NOT NULL DEFAULT 0,
    processed INTEGER NOT NULL DEFAULT 0,
    failed INTEGER NOT NULL DEFAULT 0,
    skipped_no_id INTEGER NOT NULL DEFAULT 0,
    cursor INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    started_at TEXT,
    finished_at TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO listenbrainz_love_backfill(id) VALUES (1) ON CONFLICT(id) DO NOTHING;

-- Persistent cache of resolved MusicBrainz recording_mbid and MessyBrainz
-- recording_msid (from playing_now?return_msid=true) per local track.
CREATE TABLE listenbrainz_track_ids (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    meta_fingerprint TEXT NOT NULL,
    recording_mbid TEXT NOT NULL DEFAULT '',
    recording_msid TEXT NOT NULL DEFAULT '',
    lookup_checked_at INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
