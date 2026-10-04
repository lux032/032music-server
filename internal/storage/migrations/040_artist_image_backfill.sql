-- B+2: durable artist-image backfill runs (已匹配艺术家头像补全).
-- Mirrors the durable artist matching run schema (037/039): run + items with
-- claim tokens, rate-limit wait budget and auto-resume rounds, so progress
-- survives restarts and pause/resume/cancel is race-safe.
CREATE TABLE artist_image_backfill_runs (
 id INTEGER PRIMARY KEY,
 status TEXT NOT NULL CHECK(status IN ('queued','running','paused','completed','failed','cancelled')),
 total_items INTEGER NOT NULL DEFAULT 0,
 processed_items INTEGER NOT NULL DEFAULT 0,
 cached_items INTEGER NOT NULL DEFAULT 0,
 no_url_items INTEGER NOT NULL DEFAULT 0,
 skipped_items INTEGER NOT NULL DEFAULT 0,
 failed_items INTEGER NOT NULL DEFAULT 0,
 current_artist TEXT,
 error_message TEXT,
 pause_reason TEXT NOT NULL DEFAULT '',
 wait_source TEXT NOT NULL DEFAULT '',
 waiting_until TEXT,
 wait_total_ms INTEGER NOT NULL DEFAULT 0 CHECK(wait_total_ms>=0),
 budget_baseline_ms INTEGER NOT NULL DEFAULT 0 CHECK(budget_baseline_ms>=0),
 auto_resume_count INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 finished_at TEXT,
 CHECK(budget_baseline_ms<=wait_total_ms)
) STRICT;
CREATE INDEX idx_artist_image_backfill_runs_status_id ON artist_image_backfill_runs(status,id DESC);
CREATE TABLE artist_image_backfill_items (
 id INTEGER PRIMARY KEY,
 run_id INTEGER NOT NULL REFERENCES artist_image_backfill_runs(id) ON DELETE CASCADE,
 -- Artist ids deliberately have no FK: deletion/merge must retain a durable
 -- item for an explicit skipped checkpoint, not erase task history.
 artist_id INTEGER NOT NULL,
 artist_name TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','in_progress','completed')),
 outcome TEXT CHECK(outcome IN ('cached','no_url','skipped','failed')),
 error TEXT NOT NULL DEFAULT '',
 claim_token INTEGER NOT NULL DEFAULT 0 CHECK(claim_token>=0),
 rate_limit_count INTEGER NOT NULL DEFAULT 0 CHECK(rate_limit_count>=0),
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 UNIQUE(run_id,artist_id),
 CHECK((status='completed')=(outcome IS NOT NULL))
) STRICT;
CREATE INDEX idx_artist_image_backfill_items_pending ON artist_image_backfill_items(run_id,status,id);
