-- Optimize pending candidate lookup order for PendingArtistImageCandidates:
-- WHERE status = 'pending' ... ORDER BY id DESC LIMIT ?
DROP INDEX IF EXISTS idx_artist_image_candidates_status;
CREATE INDEX idx_artist_image_candidates_status ON artist_image_candidates(status, id DESC);
