ALTER TABLE playlists ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
CREATE TABLE playlist_custom_images (
 playlist_id INTEGER PRIMARY KEY REFERENCES playlists(id) ON DELETE CASCADE,
 content_hash TEXT NOT NULL, mime_type TEXT NOT NULL, file_path TEXT NOT NULL,
 width INTEGER NOT NULL CHECK(width >= 0), height INTEGER NOT NULL CHECK(height >= 0),
 byte_size INTEGER NOT NULL CHECK(byte_size >= 0),
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
) STRICT;
