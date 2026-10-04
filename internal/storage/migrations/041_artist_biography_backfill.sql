-- 歌手简介补全（biography backfill）持久化任务：完整镜像 040 头像补全的
-- durable run 语义（run + items、claim token 纪元围栏、限流等待预算、自动
-- 恢复轮次），进度跨重启保留，暂停/继续/停止竞态安全。补全只消费已确认
-- 的 MusicBrainz 身份（含合并来源继承），绝不改写身份，绝不触碰人工简介。
CREATE TABLE artist_biography_backfill_runs (
 id INTEGER PRIMARY KEY,
 status TEXT NOT NULL CHECK(status IN ('queued','running','paused','completed','failed','cancelled')),
 total_items INTEGER NOT NULL DEFAULT 0,
 processed_items INTEGER NOT NULL DEFAULT 0,
 filled_items INTEGER NOT NULL DEFAULT 0,
 missing_items INTEGER NOT NULL DEFAULT 0,
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
CREATE INDEX idx_artist_biography_backfill_runs_status_id ON artist_biography_backfill_runs(status,id DESC);
CREATE TABLE artist_biography_backfill_items (
 id INTEGER PRIMARY KEY,
 run_id INTEGER NOT NULL REFERENCES artist_biography_backfill_runs(id) ON DELETE CASCADE,
 -- Artist ids deliberately have no FK: deletion/merge must retain a durable
 -- item for an explicit skipped checkpoint, not erase task history.
 artist_id INTEGER NOT NULL,
 artist_name TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','in_progress','completed')),
 outcome TEXT CHECK(outcome IN ('filled','missing','skipped_no_mbid','skipped_state','skipped_fresh','skipped_source_disabled','failed')),
 error TEXT NOT NULL DEFAULT '',
 claim_token INTEGER NOT NULL DEFAULT 0 CHECK(claim_token>=0),
 rate_limit_count INTEGER NOT NULL DEFAULT 0 CHECK(rate_limit_count>=0),
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 UNIQUE(run_id,artist_id),
 CHECK((status='completed')=(outcome IS NOT NULL))
) STRICT;
CREATE INDEX idx_artist_biography_backfill_items_pending ON artist_biography_backfill_items(run_id,status,id);
