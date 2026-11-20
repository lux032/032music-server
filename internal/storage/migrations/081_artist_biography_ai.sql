ALTER TABLE artist_biography_settings ADD COLUMN target_language TEXT NOT NULL DEFAULT 'zh-Hans';
ALTER TABLE artist_biography_settings ADD COLUMN ai_translate_enabled INTEGER NOT NULL DEFAULT 0 CHECK (ai_translate_enabled IN (0, 1));
ALTER TABLE artist_biography_settings ADD COLUMN ai_web_fallback_enabled INTEGER NOT NULL DEFAULT 0 CHECK (ai_web_fallback_enabled IN (0, 1));
ALTER TABLE artist_biography_settings ADD COLUMN ai_model TEXT NOT NULL DEFAULT '';

CREATE TABLE artist_biographies_v2 (
    id INTEGER PRIMARY KEY,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('wikipedia', 'lastfm', 'ai_translated', 'ai_web')),
    language TEXT NOT NULL,
    biography TEXT NOT NULL DEFAULT '',
    page_url TEXT NOT NULL DEFAULT '',
    origin_source TEXT NOT NULL DEFAULT '',
    origin_language TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'found' CHECK (status IN ('found', 'missing')),
    fetched_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(artist_id, source, language)
) STRICT;

INSERT INTO artist_biographies_v2(id, artist_id, source, language, biography, page_url, status, fetched_at)
SELECT id, artist_id, source, language, biography, page_url, status, fetched_at
FROM artist_biographies;

DROP TABLE artist_biographies;
ALTER TABLE artist_biographies_v2 RENAME TO artist_biographies;

CREATE INDEX idx_artist_biographies_artist ON artist_biographies(artist_id, status, language, source);

ALTER TABLE artist_biography_backfill_runs ADD COLUMN mode TEXT NOT NULL DEFAULT 'missing' CHECK (mode IN ('missing', 'retranslate', 'rescan_all'));

CREATE TABLE ai_usage_biography_new (
    day TEXT NOT NULL,
    model TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN (
        'tag', 'review', 'sample', 'test', 'mix', 'translate', 'playlist', 'artist_alias',
        'album_review', 'track_review', 'work_review', 'artist_review', 'credit_split', 'series_review',
        'artist_biography_translate', 'artist_biography_web'
    )),
    requests INTEGER NOT NULL DEFAULT 0,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, model, purpose)
) STRICT;

INSERT INTO ai_usage_biography_new SELECT * FROM ai_usage;
DROP TABLE ai_usage;
ALTER TABLE ai_usage_biography_new RENAME TO ai_usage;
