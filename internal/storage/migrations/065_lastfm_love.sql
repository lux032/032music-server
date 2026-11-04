-- Last.fm loved-track ("红心") sync. The local favourite stays authoritative:
-- a change only queues the matching track.love / track.unlove call.
--
-- One row per track keeps the queue small and merges rapid toggling into the
-- final desired state. It is deleted as soon as Last.fm accepted the call.
CREATE TABLE lastfm_love_queue (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    -- desired 1 = love, 0 = unlove.
    desired INTEGER NOT NULL CHECK (desired IN (0, 1)),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_lastfm_love_queue_due ON lastfm_love_queue(next_attempt_at);

-- Opt-in switch for love sync; off by default like the manual backfill below.
ALTER TABLE lastfm_scrobble_settings ADD COLUMN sync_loves INTEGER NOT NULL DEFAULT 0 CHECK (sync_loves IN (0, 1));
ALTER TABLE lastfm_scrobble_settings ADD COLUMN last_love_error TEXT NOT NULL DEFAULT '';
ALTER TABLE lastfm_scrobble_settings ADD COLUMN last_love_error_at TEXT;

-- One row describing the optional, explicitly started backfill of favourites
-- that already existed before love sync was enabled. cursor is the highest
-- tracks.id already handled, so the run is resumable and its progress is a
-- simple processed/total fraction.
CREATE TABLE lastfm_love_backfill (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    state TEXT NOT NULL DEFAULT 'idle' CHECK (state IN ('idle', 'running', 'done', 'cancelled')),
    total INTEGER NOT NULL DEFAULT 0,
    processed INTEGER NOT NULL DEFAULT 0,
    failed INTEGER NOT NULL DEFAULT 0,
    cursor INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    started_at TEXT,
    finished_at TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO lastfm_love_backfill(id) VALUES (1) ON CONFLICT(id) DO NOTHING;
