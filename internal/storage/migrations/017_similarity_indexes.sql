-- Reverse lookup of raw genre links; overrides already have a reverse index.
CREATE INDEX idx_track_genres_genre_track ON track_genres(genre_id, track_id);
-- Partial index permits bounded available-file lookups from candidate tracks.
CREATE INDEX idx_audio_files_available_track ON audio_files(track_id, id) WHERE status='available';
-- Reverse work lookup (the existing index starts with track_id).
CREATE INDEX idx_work_tracks_work_track ON work_tracks(work_id, track_id);
