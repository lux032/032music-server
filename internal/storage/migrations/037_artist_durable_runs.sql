CREATE TABLE artist_match_runs_new (
 id INTEGER PRIMARY KEY,
 status TEXT NOT NULL CHECK(status IN ('queued','running','paused','completed','failed','cancelled')),
 total_artists INTEGER NOT NULL DEFAULT 0,
 processed_artists INTEGER NOT NULL DEFAULT 0,
 matched_artists INTEGER NOT NULL DEFAULT 0,
 review_artists INTEGER NOT NULL DEFAULT 0,
 failed_artists INTEGER NOT NULL DEFAULT 0,
 current_artist TEXT,
 error_message TEXT,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 finished_at TEXT,
 skipped_artists INTEGER NOT NULL DEFAULT 0,
 no_result_artists INTEGER NOT NULL DEFAULT 0,
 durable_version INTEGER NOT NULL DEFAULT 0 CHECK(durable_version IN (0,1)),
 pause_reason TEXT NOT NULL DEFAULT '',
 wait_source TEXT NOT NULL DEFAULT '',
 waiting_until TEXT,
 wait_total_ms INTEGER NOT NULL DEFAULT 0 CHECK(wait_total_ms>=0),
 budget_baseline_ms INTEGER NOT NULL DEFAULT 0 CHECK(budget_baseline_ms>=0),
 CHECK(budget_baseline_ms<=wait_total_ms)
) STRICT;
INSERT INTO artist_match_runs_new(id,status,total_artists,processed_artists,matched_artists,review_artists,failed_artists,current_artist,error_message,created_at,finished_at,skipped_artists,no_result_artists)
 SELECT id,status,total_artists,processed_artists,matched_artists,review_artists,failed_artists,current_artist,error_message,created_at,finished_at,skipped_artists,no_result_artists FROM artist_match_runs;
DROP TABLE artist_match_runs;
ALTER TABLE artist_match_runs_new RENAME TO artist_match_runs;
CREATE INDEX idx_artist_match_runs_status_id ON artist_match_runs(status,id DESC);
CREATE TABLE artist_match_run_items (
 id INTEGER PRIMARY KEY,
 run_id INTEGER NOT NULL REFERENCES artist_match_runs(id) ON DELETE CASCADE,
 stage TEXT NOT NULL DEFAULT 'artists' CHECK(stage='artists'),
 object_id INTEGER NOT NULL,
 input_json TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','in_progress','completed')),
 outcome TEXT CHECK(outcome IN ('matched','review','no_result','skipped','failed')),
 matched_fact INTEGER NOT NULL DEFAULT 0 CHECK(matched_fact IN (0,1)),
 claim_token INTEGER NOT NULL DEFAULT 0 CHECK(claim_token>=0),
 rate_limit_count INTEGER NOT NULL DEFAULT 0 CHECK(rate_limit_count>=0),
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 UNIQUE(run_id,stage,object_id),
 CHECK((status='completed')=(outcome IS NOT NULL))
) STRICT;
CREATE INDEX idx_artist_run_items_pending ON artist_match_run_items(run_id,status,id);
-- Object ids deliberately have no artist FK: deletion/merge must retain a
-- durable item for an explicit skipped checkpoint, not erase task history.
CREATE TABLE artist_match_item_sources (
 item_id INTEGER NOT NULL REFERENCES artist_match_run_items(id) ON DELETE CASCADE,
 source TEXT NOT NULL,
 input_key TEXT NOT NULL,
 config_key TEXT NOT NULL,
 snapshot_json TEXT NOT NULL,
 checked_at TEXT NOT NULL,
 PRIMARY KEY(item_id,source)
) STRICT;
