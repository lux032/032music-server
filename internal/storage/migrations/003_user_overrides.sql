ALTER TABLE albums ADD COLUMN user_release_year INTEGER;
ALTER TABLE tracks ADD COLUMN user_composer TEXT;

CREATE TABLE track_genre_overrides (
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    genre_id INTEGER NOT NULL REFERENCES genres(id) ON DELETE CASCADE,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    PRIMARY KEY (track_id, genre_id)
) STRICT;

CREATE INDEX idx_track_genre_overrides_genre ON track_genre_overrides(genre_id, track_id);
