-- 歌词翻译：模型服务复用 ai_settings 的接口地址与 API Key；翻译开关与模型单独设置。
-- 客户端（032musicplayer）只在 translation_enabled 打开后才显示翻译开关。
ALTER TABLE ai_settings ADD COLUMN translation_enabled INTEGER NOT NULL DEFAULT 0 CHECK (translation_enabled IN (0, 1));
-- 留空表示使用 AI 打标的主模型。
ALTER TABLE ai_settings ADD COLUMN translation_model TEXT NOT NULL DEFAULT '';

-- 翻译缓存。按（曲目、目标语言、原文哈希）唯一：歌词来源或内容变化时哈希变化，会生成新的翻译，
-- 不会复用对不上的旧译文。source_lines / translated_lines 是等长的 JSON 字符串数组（逐行对应，空行保持为空）。
CREATE TABLE lyrics_translations (
    id INTEGER PRIMARY KEY,
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    target_language TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    source_lines TEXT NOT NULL,
    translated_lines TEXT NOT NULL,
    model TEXT NOT NULL,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (track_id, target_language, source_hash)
) STRICT;
CREATE INDEX idx_lyrics_translations_updated ON lyrics_translations(updated_at);

-- 用量统计新增 translate（歌词翻译）用途：SQLite 不能修改 CHECK，重建表。
CREATE TABLE ai_usage_new (
    day TEXT NOT NULL,
    model TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN ('tag', 'review', 'sample', 'test', 'mix', 'translate')),
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
