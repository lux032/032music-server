-- Bangumi album tie-ups and Bangumi-authored track associations.
-- Rebuild tables before indexes; the migration runner rewrites CREATE INDEX to IF NOT EXISTS.
CREATE TABLE album_works_new (
 album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
 work_id INTEGER NOT NULL REFERENCES works(id) ON DELETE CASCADE,
 role TEXT NOT NULL DEFAULT 'other' CHECK(role IN ('op','ed','insert','theme','character','ost','image_song','other')),
 season INTEGER NOT NULL DEFAULT 0 CHECK(season >= 0),
 source TEXT NOT NULL CHECK(source IN ('auto','manual','bangumi')),
 inferred_key TEXT,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(album_id,work_id)
) STRICT;
INSERT INTO album_works_new(album_id,work_id,role,season,source,inferred_key,created_at,updated_at) SELECT album_id,work_id,role,season,source,inferred_key,created_at,updated_at FROM album_works;
DROP TABLE album_works;
ALTER TABLE album_works_new RENAME TO album_works;
CREATE INDEX idx_album_works_work ON album_works(work_id);

CREATE TABLE work_tracks_new (
 work_id INTEGER NOT NULL REFERENCES works(id) ON DELETE CASCADE,
 track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
 role TEXT NOT NULL DEFAULT 'other' CHECK(role IN ('op','ed','insert','theme','character','ost','image_song','other')),
 season INTEGER NOT NULL DEFAULT 0 CHECK(season >= 0),
 sequence INTEGER NOT NULL DEFAULT 0 CHECK(sequence >= 0),
 source TEXT NOT NULL DEFAULT 'manual' CHECK(source IN ('manual','auto','bangumi')),
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 inferred_key TEXT,
 PRIMARY KEY(work_id,track_id,role,season,sequence)
) STRICT;
INSERT INTO work_tracks_new(work_id,track_id,role,season,sequence,source,created_at,inferred_key) SELECT work_id,track_id,role,season,sequence,source,created_at,inferred_key FROM work_tracks;
DROP TABLE work_tracks;
ALTER TABLE work_tracks_new RENAME TO work_tracks;
CREATE INDEX idx_work_tracks_track ON work_tracks(track_id,work_id);
CREATE INDEX idx_work_tracks_work_role ON work_tracks(work_id,role,season,sequence,track_id);
CREATE INDEX idx_work_tracks_work_track ON work_tracks(work_id,track_id);

CREATE TABLE album_subject_candidates (
 id INTEGER PRIMARY KEY,
 album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
 source TEXT NOT NULL CHECK(source='bangumi'),
 external_id TEXT NOT NULL,
 title TEXT NOT NULL,
 release_date TEXT,
 artist TEXT,
 score INTEGER NOT NULL DEFAULT 0,
 evidence_json TEXT NOT NULL DEFAULT '[]',
 tieups_json TEXT NOT NULL DEFAULT '[]',
 payload_json TEXT NOT NULL DEFAULT '{}',
 status TEXT NOT NULL DEFAULT 'candidate' CHECK(status IN ('candidate','confirmed','rejected')),
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 UNIQUE(album_id,source,external_id)
) STRICT;
CREATE TABLE album_enrichment_misses (
 album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
 source TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 checked_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(album_id,source)
) STRICT;
