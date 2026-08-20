-- M4: two unrelated works may share a title (remakes, name collisions).
-- Identity is now (normalized_title, type, year) instead of normalized_title
-- alone, so scanning no longer merges tracks of same-named distinct works.
-- Requires a table rebuild (SQLite cannot drop a column-level UNIQUE), which
-- the migration runner executes with foreign keys disabled.

CREATE TABLE works_migrated (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    normalized_title TEXT NOT NULL,
    reading_title TEXT,
    translated_title TEXT,
    type TEXT NOT NULL DEFAULT 'other' CHECK (type IN ('anime','drama','movie','game','commercial','other')),
    year INTEGER CHECK (year IS NULL OR (year >= 1800 AND year <= 9999)),
    poster_url TEXT,
    external_id TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO works_migrated(id, title, normalized_title, reading_title, translated_title, type, year, poster_url, external_id, created_at, updated_at)
SELECT id, title, normalized_title, reading_title, translated_title, type, year, poster_url, external_id, created_at, updated_at FROM works;

DROP TABLE works;
ALTER TABLE works_migrated RENAME TO works;

CREATE UNIQUE INDEX idx_works_external_id ON works(external_id);
CREATE UNIQUE INDEX idx_works_identity ON works(normalized_title, type, IFNULL(year, 0));
CREATE INDEX idx_works_type_year ON works(type, year DESC, id);
CREATE INDEX idx_works_reading_title ON works(reading_title COLLATE NOCASE, id);
