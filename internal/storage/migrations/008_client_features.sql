ALTER TABLE albums ADD COLUMN is_favorite INTEGER NOT NULL DEFAULT 0 CHECK (is_favorite IN (0, 1));
ALTER TABLE tracks ADD COLUMN is_favorite INTEGER NOT NULL DEFAULT 0 CHECK (is_favorite IN (0, 1));

CREATE TABLE playlists (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE TABLE playlist_items (
    playlist_id INTEGER NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0),
    added_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (playlist_id, position),
    UNIQUE (playlist_id, track_id)
) STRICT;

CREATE TABLE playback_progress (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'stopped' CHECK (state IN ('playing', 'paused', 'buffering', 'stopped')),
    position_ms INTEGER NOT NULL DEFAULT 0 CHECK (position_ms >= 0),
    duration_ms INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    play_count INTEGER NOT NULL DEFAULT 0 CHECK (play_count >= 0),
    last_played_at TEXT,
    last_completed_at TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_albums_favorite ON albums(is_favorite, updated_at DESC);
CREATE INDEX idx_tracks_favorite ON tracks(is_favorite, updated_at DESC);
CREATE INDEX idx_playlist_items_track ON playlist_items(track_id, playlist_id);
CREATE INDEX idx_playback_progress_last_played ON playback_progress(last_played_at DESC, track_id);
