-- 用量统计新增 playlist（AI 灵感歌单：把一句话解析为检索条件）用途：SQLite 不能修改 CHECK，重建表。
CREATE TABLE ai_usage_new (
    day TEXT NOT NULL,
    model TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN ('tag', 'review', 'sample', 'test', 'mix', 'translate', 'playlist')),
    requests INTEGER NOT NULL DEFAULT 0,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, model, purpose)
) STRICT;
INSERT INTO ai_usage_new SELECT day, model, purpose, requests, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens FROM ai_usage;
DROP TABLE ai_usage;
ALTER TABLE ai_usage_new RENAME TO ai_usage;
