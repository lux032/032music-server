-- 每日推荐歌单（docs/daily-mix-design.md 第 4–9 节）。P3：生成、API、显式反馈；
-- 带歌单上下文的播放行为汇总（rec_*_affinity、playback_sessions.context_*）留到 P6。

-- 单行设置。没有行时等同于全部默认值。
CREATE TABLE daily_mix_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    mix_count INTEGER NOT NULL DEFAULT 5 CHECK (mix_count BETWEEN 1 AND 8),
    track_count INTEGER NOT NULL DEFAULT 25 CHECK (track_count BETWEEN 10 AND 60),
    explore_percent INTEGER NOT NULL DEFAULT 30 CHECK (explore_percent BETWEEN 20 AND 60),
    pregenerate_hour INTEGER NOT NULL DEFAULT 5 CHECK (pregenerate_hour BETWEEN 0 AND 23),
    regenerate_limit INTEGER NOT NULL DEFAULT 3 CHECK (regenerate_limit BETWEEN 0 AND 10),
    allow_sad INTEGER NOT NULL DEFAULT 0 CHECK (allow_sad IN (0, 1)),
    llm_titles INTEGER NOT NULL DEFAULT 1 CHECK (llm_titles IN (0, 1)),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

-- 每天的歌单。day 为“推荐日”（UTC+8，按预生成时刻切换）。换一个时旧歌单置为 replaced、
-- 同一槽位 generation + 1。rating_alpha/beta 记录该评分已计入主题统计的增量，便于撤销。
CREATE TABLE daily_mixes (
    id INTEGER PRIMARY KEY,
    day TEXT NOT NULL,
    slot INTEGER NOT NULL CHECK (slot >= 0),
    generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'replaced')),
    theme_type TEXT NOT NULL,
    theme_key TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    title_source TEXT NOT NULL DEFAULT 'template' CHECK (title_source IN ('template', 'llm')),
    plan_json TEXT NOT NULL DEFAULT '{}',
    generator_version INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    rating INTEGER NOT NULL DEFAULT 0 CHECK (rating IN (-1, 0, 1)),
    rating_reasons TEXT NOT NULL DEFAULT '',
    rated_at TEXT,
    rating_alpha REAL NOT NULL DEFAULT 0,
    rating_beta REAL NOT NULL DEFAULT 0,
    saved_playlist_id INTEGER REFERENCES playlists(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
CREATE UNIQUE INDEX idx_daily_mixes_active_slot ON daily_mixes(day, slot) WHERE status = 'active';
CREATE INDEX idx_daily_mixes_day ON daily_mixes(day, slot, generation);

CREATE TABLE daily_mix_items (
    mix_id INTEGER NOT NULL REFERENCES daily_mixes(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0),
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    bucket TEXT NOT NULL CHECK (bucket IN ('familiar', 'explore')),
    reason_json TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (mix_id, position)
) STRICT;
CREATE INDEX idx_daily_mix_items_track ON daily_mix_items(track_id);

-- 不再推荐的曲目。
CREATE TABLE recommendation_track_blocks (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

-- 主题的 Beta 分布参数（Thompson Sampling）与冷却。
CREATE TABLE daily_mix_theme_stats (
    theme_type TEXT NOT NULL,
    theme_key TEXT NOT NULL,
    alpha REAL NOT NULL DEFAULT 1 CHECK (alpha > 0),
    beta REAL NOT NULL DEFAULT 1 CHECK (beta > 0),
    uses INTEGER NOT NULL DEFAULT 0,
    last_used_day TEXT,
    cooldown_until TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (theme_type, theme_key)
) STRICT;

-- 用量统计新增 mix（歌单标题/主题规划）用途：SQLite 不能修改 CHECK，重建表。
CREATE TABLE ai_usage_new (
    day TEXT NOT NULL,
    model TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN ('tag', 'review', 'sample', 'test', 'mix')),
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
