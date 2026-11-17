CREATE TABLE ai_review_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    inline_enabled INTEGER NOT NULL DEFAULT 1 CHECK (inline_enabled IN (0, 1)),
    web_search_enabled INTEGER NOT NULL DEFAULT 0 CHECK (web_search_enabled IN (0, 1)),
    model TEXT NOT NULL DEFAULT '',
    album_mode TEXT NOT NULL DEFAULT 'auto' CHECK (album_mode IN ('off', 'recommend', 'auto')),
    track_mode TEXT NOT NULL DEFAULT 'auto' CHECK (track_mode IN ('off', 'recommend', 'auto')),
    work_mode TEXT NOT NULL DEFAULT 'auto' CHECK (work_mode IN ('off', 'recommend', 'auto')),
    artist_mode TEXT NOT NULL DEFAULT 'auto' CHECK (artist_mode IN ('off', 'recommend', 'auto')),
    credit_mode TEXT NOT NULL DEFAULT 'recommend' CHECK (credit_mode IN ('off', 'recommend')),
    series_mode TEXT NOT NULL DEFAULT 'recommend' CHECK (series_mode IN ('off', 'recommend', 'auto')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO ai_review_settings(id) VALUES(1);

CREATE TABLE ai_review_decisions (
    id INTEGER PRIMARY KEY,
    target_kind TEXT NOT NULL CHECK (target_kind IN (
        'album_subject', 'track_subject', 'work_match',
        'artist_match', 'credit_split', 'series_suggestion'
    )),
    target_id INTEGER NOT NULL,
    selected_candidate_id INTEGER NOT NULL DEFAULT 0,
    fingerprint TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL CHECK (action IN ('auto_confirm', 'recommend_accept', 'recommend_reject', 'uncertain')),
    payload_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload_json)),
    confidence INTEGER NOT NULL DEFAULT 0 CHECK (confidence BETWEEN 0 AND 100),
    reason TEXT NOT NULL DEFAULT '',
    evidence_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(evidence_json)),
    web_searched INTEGER NOT NULL DEFAULT 0 CHECK (web_searched IN (0, 1)),
    model TEXT NOT NULL DEFAULT '',
    applied INTEGER NOT NULL DEFAULT 0 CHECK (applied IN (0, 1)),
    applied_snapshot_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(applied_snapshot_json)),
    reverted INTEGER NOT NULL DEFAULT 0 CHECK (reverted IN (0, 1)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (target_kind, target_id)
) STRICT;

CREATE INDEX idx_ai_review_decisions_kind_action ON ai_review_decisions(target_kind, action, applied, reverted);
CREATE INDEX idx_ai_review_decisions_applied ON ai_review_decisions(applied, reverted, updated_at DESC);

CREATE TABLE ai_review_runs (
    id INTEGER PRIMARY KEY,
    scope TEXT NOT NULL CHECK (scope IN (
        'works_all', 'albums', 'tracks', 'works', 'series',
        'artists_all', 'artist_matches', 'credit_splits'
    )),
    force INTEGER NOT NULL DEFAULT 0 CHECK (force IN (0, 1)),
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'paused', 'completed', 'cancelled', 'failed')),
    total INTEGER NOT NULL DEFAULT 0,
    processed INTEGER NOT NULL DEFAULT 0,
    auto_applied INTEGER NOT NULL DEFAULT 0,
    recommended INTEGER NOT NULL DEFAULT 0,
    uncertain INTEGER NOT NULL DEFAULT 0,
    skipped INTEGER NOT NULL DEFAULT 0,
    failed INTEGER NOT NULL DEFAULT 0,
    current_label TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    finished_at TEXT
) STRICT;

CREATE INDEX idx_ai_review_runs_status ON ai_review_runs(status, id DESC);

CREATE TABLE ai_usage_review_new (
    day TEXT NOT NULL,
    model TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN (
        'tag', 'review', 'sample', 'test', 'mix', 'translate', 'playlist', 'artist_alias',
        'album_review', 'track_review', 'work_review', 'artist_review', 'credit_split', 'series_review'
    )),
    requests INTEGER NOT NULL DEFAULT 0,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, model, purpose)
) STRICT;

INSERT INTO ai_usage_review_new SELECT * FROM ai_usage;
DROP TABLE ai_usage;
ALTER TABLE ai_usage_review_new RENAME TO ai_usage;
