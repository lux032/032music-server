-- Unified web search settings (Tavily multi-key round-robin + DeepSeek compat)
-- and comprehensive artist avatar AI audit, auto-replace, rejection blacklist,
-- and reversible audit-action snapshots.

CREATE TABLE web_search_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    provider TEXT NOT NULL DEFAULT 'tavily_then_deepseek'
        CHECK (provider IN ('tavily', 'deepseek', 'tavily_then_deepseek')),
    tavily_keys_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tavily_keys_json)),
    deepseek_key TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO web_search_settings(id, provider, deepseek_key)
SELECT 1, 'tavily_then_deepseek', COALESCE((SELECT deepseek_key FROM artist_alias_settings WHERE id = 1), '');

-- Rebuild artist_image_backfill_runs & artist_image_backfill_items in FK-safe order.
CREATE TABLE artist_image_backfill_runs_v2 (
    id INTEGER PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'paused', 'completed', 'failed', 'cancelled')),
    mode TEXT NOT NULL DEFAULT 'matched' CHECK (mode IN ('matched', 'all', 'web_fallback', 'rescan_all', 'audit_all', 'audit_force')),
    total_items INTEGER NOT NULL DEFAULT 0,
    processed_items INTEGER NOT NULL DEFAULT 0,
    cached_items INTEGER NOT NULL DEFAULT 0,
    kept_items INTEGER NOT NULL DEFAULT 0,
    replaced_items INTEGER NOT NULL DEFAULT 0,
    cleared_items INTEGER NOT NULL DEFAULT 0,
    candidate_items INTEGER NOT NULL DEFAULT 0,
    no_url_items INTEGER NOT NULL DEFAULT 0,
    skipped_items INTEGER NOT NULL DEFAULT 0,
    failed_items INTEGER NOT NULL DEFAULT 0,
    web_requests INTEGER NOT NULL DEFAULT 0 CHECK (web_requests >= 0),
    current_artist TEXT,
    error_message TEXT,
    pause_reason TEXT NOT NULL DEFAULT '',
    wait_source TEXT NOT NULL DEFAULT '',
    waiting_until TEXT,
    wait_total_ms INTEGER NOT NULL DEFAULT 0 CHECK (wait_total_ms >= 0),
    budget_baseline_ms INTEGER NOT NULL DEFAULT 0 CHECK (budget_baseline_ms >= 0),
    auto_resume_count INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    finished_at TEXT,
    CHECK (budget_baseline_ms <= wait_total_ms)
) STRICT;

INSERT INTO artist_image_backfill_runs_v2(
    id, status, mode, total_items, processed_items, cached_items, candidate_items,
    no_url_items, skipped_items, failed_items, web_requests, current_artist, error_message,
    pause_reason, wait_source, waiting_until, wait_total_ms, budget_baseline_ms,
    auto_resume_count, created_at, finished_at
)
SELECT
    id, status, mode, total_items, processed_items, cached_items, candidate_items,
    no_url_items, skipped_items, failed_items, web_requests, current_artist, error_message,
    pause_reason, wait_source, waiting_until, wait_total_ms, budget_baseline_ms,
    auto_resume_count, created_at, finished_at
FROM artist_image_backfill_runs;

CREATE TABLE artist_image_backfill_items_v3 (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES artist_image_backfill_runs_v2(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL,
    artist_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'in_progress', 'completed')),
    outcome TEXT CHECK (outcome IN ('cached', 'kept', 'replaced', 'cleared', 'candidate', 'no_url', 'skipped', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    claim_token INTEGER NOT NULL DEFAULT 0 CHECK (claim_token >= 0),
    rate_limit_count INTEGER NOT NULL DEFAULT 0 CHECK (rate_limit_count >= 0),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (run_id, artist_id),
    CHECK ((status='completed') = (outcome IS NOT NULL))
) STRICT;

INSERT INTO artist_image_backfill_items_v3(
    id, run_id, artist_id, artist_name, status, outcome, error, claim_token, rate_limit_count, updated_at
)
SELECT
    id, run_id, artist_id, artist_name, status, outcome, error, claim_token, rate_limit_count, updated_at
FROM artist_image_backfill_items;

DROP TABLE artist_image_backfill_items;
DROP TABLE artist_image_backfill_runs;
ALTER TABLE artist_image_backfill_runs_v2 RENAME TO artist_image_backfill_runs;
ALTER TABLE artist_image_backfill_items_v3 RENAME TO artist_image_backfill_items;

CREATE INDEX idx_artist_image_backfill_runs_status_id ON artist_image_backfill_runs(status, id DESC);
CREATE INDEX idx_artist_image_backfill_items_pending ON artist_image_backfill_items(run_id, status, id);

ALTER TABLE artist_image_settings ADD COLUMN audit_auto_confidence INTEGER NOT NULL DEFAULT 85 CHECK (audit_auto_confidence BETWEEN 50 AND 100);

ALTER TABLE artist_image_candidates ADD COLUMN kind TEXT NOT NULL DEFAULT 'replacement' CHECK (kind IN ('replacement', 'audit_review'));
ALTER TABLE artist_image_candidates ADD COLUMN issue_type TEXT NOT NULL DEFAULT '';
ALTER TABLE artist_image_candidates ADD COLUMN confidence_score INTEGER NOT NULL DEFAULT 0 CHECK (confidence_score BETWEEN 0 AND 100);

CREATE TABLE artist_image_reviews (
    artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
    image_hash TEXT NOT NULL,
    verdict TEXT NOT NULL CHECK (verdict IN ('keep', 'replace')),
    issue_type TEXT NOT NULL DEFAULT '',
    confidence INTEGER NOT NULL DEFAULT 0 CHECK (confidence BETWEEN 0 AND 100),
    reason TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    reviewed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_artist_image_reviews_artist_hash ON artist_image_reviews(artist_id, image_hash, verdict);

CREATE TABLE artist_image_rejections (
    id INTEGER PRIMARY KEY,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    image_url TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (image_url <> '' OR content_hash <> '')
) STRICT;

CREATE INDEX idx_artist_image_rejections_artist_url ON artist_image_rejections(artist_id, image_url);
CREATE INDEX idx_artist_image_rejections_artist_hash ON artist_image_rejections(artist_id, content_hash);

CREATE TABLE artist_image_audit_actions (
    id INTEGER PRIMARY KEY,
    run_id INTEGER,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    action TEXT NOT NULL CHECK (action IN ('replaced', 'cleared')),
    issue_type TEXT NOT NULL DEFAULT '',
    confidence INTEGER NOT NULL DEFAULT 0 CHECK (confidence BETWEEN 0 AND 100),
    reason TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    prev_cache_json TEXT NOT NULL CHECK (json_valid(prev_cache_json)),
    new_source TEXT NOT NULL DEFAULT '',
    new_remote_url TEXT NOT NULL DEFAULT '',
    new_content_hash TEXT NOT NULL DEFAULT '',
    new_cache_path TEXT NOT NULL DEFAULT '',
    reverted INTEGER NOT NULL DEFAULT 0 CHECK (reverted IN (0, 1)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    reverted_at TEXT
) STRICT;

CREATE INDEX idx_artist_image_audit_actions_recent ON artist_image_audit_actions(reverted, id DESC);
CREATE INDEX idx_artist_image_audit_actions_artist ON artist_image_audit_actions(artist_id, id DESC);

CREATE TABLE ai_usage_artist_image_audit_new (
    day TEXT NOT NULL,
    model TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN (
        'tag', 'review', 'sample', 'test', 'mix', 'translate', 'playlist', 'artist_alias',
        'album_review', 'track_review', 'work_review', 'artist_review', 'credit_split', 'series_review',
        'artist_biography_translate', 'artist_biography_web',
        'artist_image_web', 'artist_image_vision', 'artist_image_audit'
    )),
    requests INTEGER NOT NULL DEFAULT 0,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, model, purpose)
) STRICT;

INSERT INTO ai_usage_artist_image_audit_new SELECT * FROM ai_usage;
DROP TABLE ai_usage;
ALTER TABLE ai_usage_artist_image_audit_new RENAME TO ai_usage;
