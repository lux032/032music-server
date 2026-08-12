ALTER TABLE artist_external_profiles ADD COLUMN remote_image_url TEXT;
ALTER TABLE artist_external_profiles ADD COLUMN image_checked_at TEXT;

CREATE TABLE artist_image_cache (
    artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    remote_url TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    cache_path TEXT NOT NULL,
    byte_size INTEGER NOT NULL CHECK (byte_size > 0),
    fetched_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_artist_image_cache_hash ON artist_image_cache(content_hash);
