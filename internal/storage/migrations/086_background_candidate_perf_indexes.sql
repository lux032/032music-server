CREATE INDEX IF NOT EXISTS idx_tracks_added_id ON tracks(added_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_artist_image_candidates_artist_status ON artist_image_candidates(artist_id, status);
