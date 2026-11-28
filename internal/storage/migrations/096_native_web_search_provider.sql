-- Allow 'native_then_search' (OpenAI /v1/responses native web search first,
-- automatically falling back to Tavily / DeepSeek) in web_search_settings, and
-- allow 'web_search_native' in ai_usage purpose accounting.

CREATE TABLE web_search_settings_v2 (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    provider TEXT NOT NULL DEFAULT 'tavily_then_deepseek'
        CHECK (provider IN ('native_then_search', 'tavily', 'deepseek', 'tavily_then_deepseek')),
    tavily_keys_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tavily_keys_json)),
    deepseek_key TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO web_search_settings_v2(id, provider, tavily_keys_json, deepseek_key, updated_at)
SELECT id, provider, tavily_keys_json, deepseek_key, updated_at
FROM web_search_settings;

DROP TABLE web_search_settings;
ALTER TABLE web_search_settings_v2 RENAME TO web_search_settings;

CREATE TABLE ai_usage_web_search_native_new (
    day TEXT NOT NULL,
    model TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN (
        'tag', 'review', 'sample', 'test', 'mix', 'translate', 'playlist', 'artist_alias',
        'album_review', 'track_review', 'work_review', 'artist_review', 'credit_split', 'series_review',
        'artist_biography_translate', 'artist_biography_web',
        'artist_image_web', 'artist_image_vision', 'artist_image_audit',
        'web_search_native'
    )),
    requests INTEGER NOT NULL DEFAULT 0,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, model, purpose)
) STRICT;

INSERT INTO ai_usage_web_search_native_new(day, model, purpose, requests, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens)
SELECT day, model, purpose, requests, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens
FROM ai_usage;

DROP TABLE ai_usage;
ALTER TABLE ai_usage_web_search_native_new RENAME TO ai_usage;
