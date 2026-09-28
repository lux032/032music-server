-- 4.5.7 custom album covers and artist images.
-- artworks.source_type has no CHECK constraint, so source_type='custom' rows
-- fit the existing schema. The table is still rebuilt with AUTOINCREMENT:
-- every custom-cover upload must produce a fresh artwork id (the URL is the
-- cache key), and a plain INTEGER PRIMARY KEY would reuse the id of a just
-- deleted max-rowid custom row. Rebuild follows the migration 026 pattern.
CREATE TABLE artworks_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
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
INSERT INTO artworks_new(id,album_id,track_id,source_type,source_path,content_hash,mime_type,width,height,byte_size,is_primary,created_at) SELECT id,album_id,track_id,source_type,source_path,content_hash,mime_type,width,height,byte_size,is_primary,created_at FROM artworks;
DROP TABLE artworks;
ALTER TABLE artworks_new RENAME TO artworks;
CREATE INDEX idx_artworks_album_primary ON artworks(album_id, is_primary DESC);
CREATE UNIQUE INDEX idx_artworks_content_owner ON artworks(content_hash, COALESCE(album_id, 0), COALESCE(track_id, 0));

-- Custom album covers live in artworks with source_type='custom'; artist
-- custom images use the dedicated table below (D30). Files sit under
-- <data>/custom-images/<sha256>.<ext>, deduplicated by content hash; rows
-- reference the absolute file path.
CREATE TABLE artist_custom_images (
    artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
    content_hash TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    file_path TEXT NOT NULL,
    width INTEGER NOT NULL DEFAULT 0 CHECK (width >= 0),
    height INTEGER NOT NULL DEFAULT 0 CHECK (height >= 0),
    byte_size INTEGER NOT NULL CHECK (byte_size >= 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

-- Speeds up the custom-first artwork selection and the "has custom cover"
-- checks on album pages.
CREATE INDEX idx_artworks_album_custom ON artworks(album_id) WHERE source_type = 'custom';
