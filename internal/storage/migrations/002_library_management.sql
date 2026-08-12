ALTER TABLE artists ADD COLUMN user_display_name TEXT;
ALTER TABLE albums ADD COLUMN user_title TEXT;
ALTER TABLE tracks ADD COLUMN user_title TEXT;
ALTER TABLE tracks ADD COLUMN composer TEXT;
ALTER TABLE tracks ADD COLUMN lyrics TEXT;

CREATE UNIQUE INDEX idx_artworks_content_owner
ON artworks(content_hash, COALESCE(album_id, 0), COALESCE(track_id, 0));

CREATE INDEX idx_artists_display_name ON artists(display_name COLLATE NOCASE, id);
CREATE INDEX idx_tracks_title ON tracks(title COLLATE NOCASE, id);
