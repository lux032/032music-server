-- Add persistent async auto-ingest avatar queue and toggle for newly confirmed
-- MusicBrainz artists across all roles (performers and backstage credits).

ALTER TABLE artist_image_settings
    ADD COLUMN auto_ingest_ai_enabled INTEGER NOT NULL DEFAULT 1 CHECK (auto_ingest_ai_enabled IN (0, 1));

CREATE TABLE artist_image_auto_queue (
    artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
    trigger_source TEXT NOT NULL DEFAULT 'auto_match',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_retry_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_artist_image_auto_queue_claim ON artist_image_auto_queue(status, next_retry_at, created_at);
