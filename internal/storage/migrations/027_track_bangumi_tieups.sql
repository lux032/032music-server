-- Track-level Bangumi single lookups. work_tracks already accepts source='bangumi' (026).
CREATE TABLE track_subject_candidates (
 id INTEGER PRIMARY KEY,
 track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
 source TEXT NOT NULL CHECK(source='bangumi'),
 external_id TEXT NOT NULL,
 title TEXT NOT NULL,
 artist TEXT,
 match_kind TEXT NOT NULL CHECK(match_kind IN ('exact','version_base','multi_title')),
 evidence_json TEXT NOT NULL DEFAULT '[]',
 tieups_json TEXT NOT NULL DEFAULT '[]',
 payload_json TEXT NOT NULL DEFAULT '{}',
 status TEXT NOT NULL DEFAULT 'candidate' CHECK(status IN ('candidate','confirmed','rejected')),
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 UNIQUE(track_id,source,external_id)
) STRICT;
CREATE INDEX idx_track_subject_candidates_status ON track_subject_candidates(status,id);

CREATE TABLE track_enrichment_misses (
 track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
 source TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 reason TEXT,
 checked_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(track_id,source)
) STRICT;
