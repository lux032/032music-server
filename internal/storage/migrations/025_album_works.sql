-- Album-first work associations and persistent user intent.
CREATE TABLE album_works (
 album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
 work_id INTEGER NOT NULL REFERENCES works(id) ON DELETE CASCADE,
 role TEXT NOT NULL DEFAULT 'other' CHECK(role IN ('op','ed','insert','theme','character','ost','image_song','other')),
 season INTEGER NOT NULL DEFAULT 0 CHECK(season >= 0),
 source TEXT NOT NULL CHECK(source IN ('auto','manual')),
 inferred_key TEXT,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(album_id,work_id)
) STRICT;
CREATE INDEX idx_album_works_work ON album_works(work_id);
CREATE TABLE album_work_suppressions (
 album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
 inferred_key TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(album_id,inferred_key)
) STRICT;
CREATE TABLE track_work_suppressions (
 track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
 inferred_key TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(track_id,inferred_key)
) STRICT;
CREATE TABLE work_aliases (
 normalized_key TEXT NOT NULL,
 work_id INTEGER NOT NULL REFERENCES works(id) ON DELETE CASCADE,
 PRIMARY KEY(normalized_key,work_id)
) STRICT;
ALTER TABLE works ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual' CHECK(origin IN ('auto','manual'));
ALTER TABLE albums ADD COLUMN work_fingerprint TEXT;
ALTER TABLE work_tracks ADD COLUMN inferred_key TEXT;
UPDATE works SET origin='auto' WHERE year IS NULL AND NOT EXISTS(SELECT 1 FROM work_match_candidates c WHERE c.work_id=works.id AND c.status='rejected') AND EXISTS(SELECT 1 FROM work_tracks wt WHERE wt.work_id=works.id AND wt.source='auto') AND NOT EXISTS(SELECT 1 FROM work_tracks wt WHERE wt.work_id=works.id AND wt.source='manual') AND COALESCE(external_id,'')='' AND COALESCE(reading_title,'')='' AND COALESCE(translated_title,'')='' AND COALESCE(poster_url,'')='';
INSERT INTO work_aliases(normalized_key,work_id) SELECT normalized_title,id FROM works;
