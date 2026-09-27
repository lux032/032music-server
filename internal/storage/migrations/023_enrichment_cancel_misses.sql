-- Migration runner disables foreign keys before running table rebuilds.
CREATE TABLE artist_match_runs_migrated (
 id INTEGER PRIMARY KEY,
 status TEXT NOT NULL CHECK (status IN ('queued','running','completed','failed','cancelled')),
 total_artists INTEGER NOT NULL DEFAULT 0,
 processed_artists INTEGER NOT NULL DEFAULT 0,
 matched_artists INTEGER NOT NULL DEFAULT 0,
 review_artists INTEGER NOT NULL DEFAULT 0,
 failed_artists INTEGER NOT NULL DEFAULT 0,
 current_artist TEXT,
 error_message TEXT,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
 finished_at TEXT
) STRICT;
INSERT INTO artist_match_runs_migrated(id,status,total_artists,processed_artists,matched_artists,review_artists,failed_artists,current_artist,error_message,created_at,finished_at)
SELECT id,status,total_artists,processed_artists,matched_artists,review_artists,failed_artists,current_artist,error_message,created_at,finished_at FROM artist_match_runs;
DROP TABLE artist_match_runs;
ALTER TABLE artist_match_runs_migrated RENAME TO artist_match_runs;

CREATE TABLE work_enrichment_misses (
 work_id INTEGER NOT NULL REFERENCES works(id) ON DELETE CASCADE,
 source TEXT NOT NULL,
 checked_at TEXT NOT NULL,
 PRIMARY KEY(work_id,source)
) STRICT;
