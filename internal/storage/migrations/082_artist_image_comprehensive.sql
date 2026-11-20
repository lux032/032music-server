-- Comprehensive multi-tier artist avatar backfill:
--   1. artist_image_settings (Tier 1-4 toggles, Fanart key, Web/AI/VLM budget & cooldown)
--   2. artist_image_backfill_runs mode & candidate_items columns
--   3. artist_image_backfill_items outcome expanded with 'candidate'
--   4. artist_image_attempts negative cache / cooldown table
--   5. artist_image_candidates review queue for medium-confidence web/AI images
--   6. ai_usage purpose expanded with 'artist_image_web' and 'artist_image_vision'

CREATE TABLE artist_image_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    bangumi_enabled INTEGER NOT NULL DEFAULT 1 CHECK (bangumi_enabled IN (0, 1)),
    deezer_enabled INTEGER NOT NULL DEFAULT 1 CHECK (deezer_enabled IN (0, 1)),
    wikipedia_enabled INTEGER NOT NULL DEFAULT 1 CHECK (wikipedia_enabled IN (0, 1)),
    fanart_enabled INTEGER NOT NULL DEFAULT 0 CHECK (fanart_enabled IN (0, 1)),
    fanart_api_key TEXT NOT NULL DEFAULT '',
    web_fallback_enabled INTEGER NOT NULL DEFAULT 0 CHECK (web_fallback_enabled IN (0, 1)),
    web_auto_apply_high INTEGER NOT NULL DEFAULT 1 CHECK (web_auto_apply_high IN (0, 1)),
    vision_enabled INTEGER NOT NULL DEFAULT 0 CHECK (vision_enabled IN (0, 1)),
    vision_model TEXT NOT NULL DEFAULT '',
    ai_model TEXT NOT NULL DEFAULT '',
    max_web_requests_per_run INTEGER NOT NULL DEFAULT 100 CHECK (max_web_requests_per_run >= 0),
    negative_cache_days INTEGER NOT NULL DEFAULT 30 CHECK (negative_cache_days >= 1),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO artist_image_settings(id) VALUES (1);

ALTER TABLE artist_image_backfill_runs ADD COLUMN mode TEXT NOT NULL DEFAULT 'matched' CHECK (mode IN ('matched', 'all', 'web_fallback', 'rescan_all'));
ALTER TABLE artist_image_backfill_runs ADD COLUMN candidate_items INTEGER NOT NULL DEFAULT 0;
ALTER TABLE artist_image_backfill_runs ADD COLUMN web_requests INTEGER NOT NULL DEFAULT 0 CHECK (web_requests >= 0);

CREATE TABLE artist_image_backfill_items_v2 (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES artist_image_backfill_runs(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL,
    artist_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'in_progress', 'completed')),
    outcome TEXT CHECK (outcome IN ('cached', 'candidate', 'no_url', 'skipped', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    claim_token INTEGER NOT NULL DEFAULT 0 CHECK (claim_token >= 0),
    rate_limit_count INTEGER NOT NULL DEFAULT 0 CHECK (rate_limit_count >= 0),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(run_id, artist_id),
    CHECK ((status='completed') = (outcome IS NOT NULL))
) STRICT;

INSERT INTO artist_image_backfill_items_v2(id, run_id, artist_id, artist_name, status, outcome, error, claim_token, rate_limit_count, updated_at)
SELECT id, run_id, artist_id, artist_name, status, outcome, error, claim_token, rate_limit_count, updated_at
FROM artist_image_backfill_items;

DROP TABLE artist_image_backfill_items;
ALTER TABLE artist_image_backfill_items_v2 RENAME TO artist_image_backfill_items;

CREATE INDEX idx_artist_image_backfill_items_pending ON artist_image_backfill_items(run_id, status, id);

CREATE TABLE artist_image_attempts (
    artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
    tier_reached TEXT NOT NULL DEFAULT '',
    last_outcome TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    checked_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    cooldown_until TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_artist_image_attempts_cooldown ON artist_image_attempts(cooldown_until);

CREATE TABLE artist_image_candidates (
    id INTEGER PRIMARY KEY,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    image_url TEXT NOT NULL,
    page_url TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    confidence TEXT NOT NULL DEFAULT 'medium' CHECK (confidence IN ('high', 'medium', 'low')),
    reason TEXT NOT NULL DEFAULT '',
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    reviewed_at TEXT,
    UNIQUE(artist_id, image_url)
) STRICT;

CREATE INDEX idx_artist_image_candidates_status ON artist_image_candidates(status, artist_id, id DESC);

CREATE TABLE ai_usage_artist_image_new (
    day TEXT NOT NULL,
    model TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN (
        'tag', 'review', 'sample', 'test', 'mix', 'translate', 'playlist', 'artist_alias',
        'album_review', 'track_review', 'work_review', 'artist_review', 'credit_split', 'series_review',
        'artist_biography_translate', 'artist_biography_web',
        'artist_image_web', 'artist_image_vision'
    )),
    requests INTEGER NOT NULL DEFAULT 0,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, model, purpose)
) STRICT;

INSERT INTO ai_usage_artist_image_new SELECT * FROM ai_usage;
DROP TABLE ai_usage;
ALTER TABLE ai_usage_artist_image_new RENAME TO ai_usage;
