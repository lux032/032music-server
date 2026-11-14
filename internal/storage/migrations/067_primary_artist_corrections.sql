CREATE TABLE track_primary_artist_state (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL DEFAULT 0,
    active INTEGER NOT NULL DEFAULT 0 CHECK(active IN (0,1)),
    artists_json TEXT NOT NULL DEFAULT '[]',
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
) STRICT;
CREATE TABLE track_primary_artist_base (
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL REFERENCES artists(id),
    position INTEGER NOT NULL,
    join_phrase TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(track_id,artist_id)
) STRICT;
INSERT OR IGNORE INTO track_primary_artist_base(track_id,artist_id,position,join_phrase)
SELECT track_id,artist_id,MIN(position),join_phrase FROM track_artists WHERE role='primary' GROUP BY track_id,artist_id;
CREATE INDEX idx_primary_base_artist ON track_primary_artist_base(artist_id);
CREATE TABLE track_primary_artist_operations (
    id INTEGER PRIMARY KEY,
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL,
    before_active INTEGER NOT NULL,
    before_json TEXT NOT NULL,
    after_active INTEGER NOT NULL,
    after_json TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE(track_id,revision)
) STRICT;
