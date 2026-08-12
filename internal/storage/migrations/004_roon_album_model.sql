ALTER TABLE albums ADD COLUMN performed_by TEXT;
ALTER TABLE albums ADD COLUMN album_type TEXT NOT NULL DEFAULT 'album';
ALTER TABLE albums ADD COLUMN version TEXT;
ALTER TABLE albums ADD COLUMN original_release_date TEXT;
ALTER TABLE albums ADD COLUMN label TEXT;
ALTER TABLE albums ADD COLUMN catalog_number TEXT;
ALTER TABLE albums ADD COLUMN country TEXT;
ALTER TABLE albums ADD COLUMN review TEXT;
ALTER TABLE albums ADD COLUMN is_compilation INTEGER NOT NULL DEFAULT 0 CHECK (is_compilation IN (0, 1));
ALTER TABLE albums ADD COLUMN is_live INTEGER NOT NULL DEFAULT 0 CHECK (is_live IN (0, 1));
ALTER TABLE albums ADD COLUMN is_bootleg INTEGER NOT NULL DEFAULT 0 CHECK (is_bootleg IN (0, 1));

ALTER TABLE albums ADD COLUMN user_performed_by TEXT;
ALTER TABLE albums ADD COLUMN user_album_type TEXT;
ALTER TABLE albums ADD COLUMN user_version TEXT;
ALTER TABLE albums ADD COLUMN user_release_date TEXT;
ALTER TABLE albums ADD COLUMN user_original_release_date TEXT;
ALTER TABLE albums ADD COLUMN user_label TEXT;
ALTER TABLE albums ADD COLUMN user_catalog_number TEXT;
ALTER TABLE albums ADD COLUMN user_country TEXT;
ALTER TABLE albums ADD COLUMN user_review TEXT;
ALTER TABLE albums ADD COLUMN user_is_compilation INTEGER CHECK (user_is_compilation IS NULL OR user_is_compilation IN (0, 1));
ALTER TABLE albums ADD COLUMN user_is_live INTEGER CHECK (user_is_live IS NULL OR user_is_live IN (0, 1));
ALTER TABLE albums ADD COLUMN user_is_bootleg INTEGER CHECK (user_is_bootleg IS NULL OR user_is_bootleg IN (0, 1));

CREATE INDEX idx_albums_type ON albums(album_type, id);
CREATE INDEX idx_albums_label ON albums(label COLLATE NOCASE, id);

CREATE TABLE album_genre_overrides (
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    genre_id INTEGER NOT NULL REFERENCES genres(id) ON DELETE CASCADE,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    PRIMARY KEY (album_id, genre_id)
) STRICT;

CREATE INDEX idx_album_genre_overrides_genre ON album_genre_overrides(genre_id, album_id);
