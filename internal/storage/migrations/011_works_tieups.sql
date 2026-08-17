-- Phase 3: Tie-up works, track associations, and multilingual lookup indexes

CREATE TABLE works (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    normalized_title TEXT NOT NULL UNIQUE,
    reading_title TEXT,
    translated_title TEXT,
    type TEXT NOT NULL DEFAULT 'other' CHECK (type IN ('anime','drama','movie','game','commercial','other')),
    year INTEGER CHECK (year IS NULL OR (year >= 1800 AND year <= 9999)),
    poster_url TEXT,
    external_id TEXT UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE TABLE work_tracks (
    work_id INTEGER NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'other' CHECK (role IN ('op','ed','insert','theme','character','ost','image_song','other')),
    season INTEGER NOT NULL DEFAULT 0 CHECK (season >= 0),
    sequence INTEGER NOT NULL DEFAULT 0 CHECK (sequence >= 0),
    source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','auto')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (work_id, track_id, role, season, sequence)
) STRICT;

CREATE INDEX idx_works_type_year ON works(type, year DESC, id);
CREATE INDEX idx_works_reading_title ON works(reading_title COLLATE NOCASE, id);
CREATE INDEX idx_work_tracks_track ON work_tracks(track_id, work_id);
CREATE INDEX idx_work_tracks_work_role ON work_tracks(work_id, role, season, sequence, track_id);
CREATE INDEX idx_tracks_reading_title ON tracks(reading_title COLLATE NOCASE, id);
