ALTER TABLE artists ADD COLUMN is_favorite INTEGER NOT NULL DEFAULT 0 CHECK (is_favorite IN (0,1));
ALTER TABLE artists ADD COLUMN favorited_at TEXT;
ALTER TABLE artist_merge_operations ADD COLUMN favorite_set_by_merge INTEGER NOT NULL DEFAULT 0 CHECK (favorite_set_by_merge IN (0,1));
CREATE INDEX idx_artists_favorite ON artists(is_favorite, favorited_at DESC) WHERE merged_into_artist_id IS NULL;
