CREATE TABLE enrichment_runs_durable (
 id INTEGER PRIMARY KEY,
 status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','paused','completed','failed','cancelled')),
 scope TEXT NOT NULL CHECK(length(trim(scope))>0),
 target_id INTEGER,
 force INTEGER NOT NULL DEFAULT 0 CHECK(force IN (0,1)),
 total INTEGER NOT NULL DEFAULT 0 CHECK(total>=0),
 processed INTEGER NOT NULL DEFAULT 0 CHECK(processed>=0),
 succeeded INTEGER NOT NULL DEFAULT 0 CHECK(succeeded>=0),
 skipped INTEGER NOT NULL DEFAULT 0 CHECK(skipped>=0),
 review INTEGER NOT NULL DEFAULT 0 CHECK(review>=0),
 failed INTEGER NOT NULL DEFAULT 0 CHECK(failed>=0),
 current TEXT,
 error_message TEXT,
 created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 started_at TEXT,
 updated_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 finished_at TEXT,
 stage TEXT NOT NULL DEFAULT '',
 stage_albums INTEGER NOT NULL DEFAULT -1,
 stage_tracks INTEGER NOT NULL DEFAULT -1,
 stage_works INTEGER NOT NULL DEFAULT -1,
 durable_version INTEGER NOT NULL DEFAULT 0 CHECK(durable_version IN (0,1)),
 pause_reason TEXT NOT NULL DEFAULT '',
 wait_source TEXT NOT NULL DEFAULT '',
 waiting_until TEXT,
 wait_total_ms INTEGER NOT NULL DEFAULT 0 CHECK(wait_total_ms>=0),
 budget_baseline_ms INTEGER NOT NULL DEFAULT 0 CHECK(budget_baseline_ms>=0 AND budget_baseline_ms<=wait_total_ms),
 epoch INTEGER NOT NULL DEFAULT 1 CHECK(epoch>0),
 CHECK(processed<=total OR total=0)
) STRICT;
INSERT INTO enrichment_runs_durable(id,status,scope,target_id,force,total,processed,succeeded,skipped,review,failed,current,error_message,created_at,started_at,updated_at,finished_at,stage,stage_albums,stage_tracks,stage_works)
 SELECT id,status,scope,target_id,force,total,processed,succeeded,skipped,review,failed,current,error_message,created_at,started_at,updated_at,finished_at,stage,stage_albums,stage_tracks,stage_works FROM enrichment_runs;
DROP TABLE enrichment_runs;
ALTER TABLE enrichment_runs_durable RENAME TO enrichment_runs;
CREATE INDEX idx_enrichment_runs_status ON enrichment_runs(status,id DESC);
CREATE TABLE enrichment_run_stages (
 run_id INTEGER NOT NULL REFERENCES enrichment_runs(id) ON DELETE CASCADE,
 stage TEXT NOT NULL CHECK(stage IN ('albums','tracks','works','series')),
 prepared INTEGER NOT NULL DEFAULT 0 CHECK(prepared IN (0,1)),
 PRIMARY KEY(run_id,stage)
) STRICT;
CREATE TABLE enrichment_run_items (
 id INTEGER PRIMARY KEY,
 run_id INTEGER NOT NULL REFERENCES enrichment_runs(id) ON DELETE CASCADE,
 stage TEXT NOT NULL CHECK(stage IN ('albums','tracks','works','series')),
 object_id INTEGER NOT NULL,
 parameters_json TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','in_progress','completed')),
 claim_token INTEGER NOT NULL DEFAULT 0,
 outcome TEXT CHECK(outcome IN ('matched','skipped','review','failed')),
 rate_limit_count INTEGER NOT NULL DEFAULT 0 CHECK(rate_limit_count>=0),
 UNIQUE(run_id,stage,object_id),
 CHECK((status='completed')=(outcome IS NOT NULL))
) STRICT;
CREATE INDEX idx_enrichment_items_pending ON enrichment_run_items(run_id,stage,status,id);
-- Request checkpoints are per run, including series graph requests. They are
-- not a global cache or permanent permission to trust changed inputs.
CREATE TABLE enrichment_run_requests (
 run_id INTEGER NOT NULL REFERENCES enrichment_runs(id) ON DELETE CASCADE,
 source TEXT NOT NULL,
 request_key TEXT NOT NULL,
 request_url TEXT NOT NULL,
 status_code INTEGER NOT NULL CHECK(status_code BETWEEN 100 AND 599),
 input_key TEXT NOT NULL DEFAULT '',
 config_key TEXT NOT NULL DEFAULT '',
 body BLOB NOT NULL,
 checked_at TEXT NOT NULL,
 PRIMARY KEY(run_id,source,request_key,request_url)
) STRICT;

CREATE INDEX idx_enrichment_runs_created ON enrichment_runs(created_at DESC,id DESC);
CREATE INDEX idx_enrichment_runs_status_scope ON enrichment_runs(status,scope,id DESC);

CREATE TABLE enrichment_item_effects (
 item_id INTEGER NOT NULL REFERENCES enrichment_run_items(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 outcome TEXT NOT NULL,
 PRIMARY KEY(item_id,name)
) STRICT;
