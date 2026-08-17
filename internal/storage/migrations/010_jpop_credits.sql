-- Phase 1: J-Pop credits, track types, and reading/sort keys
-- Adds structured credits fields, track type classification, and Japanese reading names

-- 1. Track type classification (regular, instrumental, off_vocal, tv_size, drama_track, remix)
ALTER TABLE tracks ADD COLUMN track_type TEXT NOT NULL DEFAULT 'regular';
ALTER TABLE tracks ADD COLUMN user_track_type TEXT;

-- 2. Structured credits text fields on tracks (display strings, mirroring existing composer)
ALTER TABLE tracks ADD COLUMN lyricist TEXT;
ALTER TABLE tracks ADD COLUMN arranger TEXT;

-- 3. Reading/sort keys for Japanese name ordering (furigana / romaji)
ALTER TABLE artists ADD COLUMN reading_name TEXT;
ALTER TABLE albums ADD COLUMN reading_title TEXT;
ALTER TABLE tracks ADD COLUMN reading_title TEXT;

-- 4. Indexes for new fields
CREATE INDEX idx_tracks_track_type ON tracks(track_type, id);
CREATE INDEX idx_artists_reading_name ON artists(reading_name COLLATE NOCASE, id);
CREATE INDEX idx_albums_reading_title ON albums(reading_title COLLATE NOCASE, id);

-- 5. Index for track_artists by role (supports credits queries like "all tracks where artist X is arranger")
CREATE INDEX idx_track_artists_role ON track_artists(role, artist_id, track_id);
