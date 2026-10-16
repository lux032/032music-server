-- AI 融合打标（声学证据 + 元数据 + 歌词 → 情绪/风格词表）。设计见 docs/daily-mix-design.md 第 2、13 节。
-- 模型服务通过 OpenAI 兼容接口接入；总开关默认关闭，开启后先全量打标，之后新歌自动打标。

-- 单行设置。api_key / proxy_url 只写不读（页面只显示“已设置”）。
CREATE TABLE ai_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    base_url TEXT NOT NULL DEFAULT '',
    api_key TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    review_model TEXT NOT NULL DEFAULT '',
    review_enabled INTEGER NOT NULL DEFAULT 0 CHECK (review_enabled IN (0, 1)),
    proxy_url TEXT NOT NULL DEFAULT '',
    reasoning_effort TEXT NOT NULL DEFAULT '' CHECK (reasoning_effort IN ('', 'none', 'minimal', 'low', 'medium', 'high')),
    tagging_enabled INTEGER NOT NULL DEFAULT 0 CHECK (tagging_enabled IN (0, 1)),
    tagging_paused INTEGER NOT NULL DEFAULT 0 CHECK (tagging_paused IN (0, 1)),
    include_lyrics INTEGER NOT NULL DEFAULT 1 CHECK (include_lyrics IN (0, 1)),
    requests_per_minute INTEGER NOT NULL DEFAULT 60 CHECK (requests_per_minute BETWEEN 1 AND 10000),
    concurrency INTEGER NOT NULL DEFAULT 4 CHECK (concurrency BETWEEN 1 AND 16),
    first_enabled_at TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

-- 曲目标签。source：audio = 声学映射（证据），llm = 融合打标结果，user = 人工修正（优先级最高，
-- 重新打标不会覆盖）。kind=mood 时 weight 为该情绪在本曲中的占比（同一来源合计约 1）。
CREATE TABLE track_tags (
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('mood', 'style')),
    tag TEXT NOT NULL,
    weight REAL NOT NULL CHECK (weight >= 0 AND weight <= 1),
    confidence REAL NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    source TEXT NOT NULL CHECK (source IN ('audio', 'llm', 'user')),
    PRIMARY KEY (track_id, kind, tag, source)
) STRICT;
CREATE INDEX idx_track_tags_lookup ON track_tags(kind, tag, source);

-- 每首曲目的打标状态。source_stamp 由曲目/专辑/作品/声学特征的变更时间拼成，变化即需要重新打标；
-- input_hash 是实际发送内容的哈希：stamp 变了但内容没变时只更新 stamp，不重复调用模型。
CREATE TABLE track_tag_meta (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    source_stamp TEXT NOT NULL,
    input_hash TEXT NOT NULL,
    vocab_version INTEGER NOT NULL,
    prompt_version INTEGER NOT NULL,
    model TEXT NOT NULL,
    energy INTEGER CHECK (energy IS NULL OR energy BETWEEN 1 AND 5),
    vocal TEXT CHECK (vocal IS NULL OR vocal IN ('vocal', 'instrumental', 'mixed')),
    idol TEXT CHECK (idol IS NULL OR idol IN ('yes', 'no', 'unsure')),
    used_audio INTEGER NOT NULL DEFAULT 0 CHECK (used_audio IN (0, 1)),
    used_lyrics INTEGER NOT NULL DEFAULT 0 CHECK (used_lyrics IN (0, 1)),
    needs_review INTEGER NOT NULL DEFAULT 0 CHECK (needs_review IN (0, 1)),
    review_reasons TEXT NOT NULL DEFAULT '',
    reviewed_by TEXT NOT NULL DEFAULT '',
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    result_json TEXT NOT NULL DEFAULT '{}',
    tagged_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
CREATE INDEX idx_track_tag_meta_review ON track_tag_meta(needs_review, reviewed_by);
CREATE INDEX idx_track_tag_meta_tagged ON track_tag_meta(tagged_at);

-- 打标失败（输出无效、被内容过滤等）。只对同一 source_stamp 生效；按指数退避重试，多次失败后标记 permanent。
-- 网络错误、限流不计入（整体退避，不惩罚单曲）。
CREATE TABLE ai_tagging_failures (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    source_stamp TEXT NOT NULL,
    attempts INTEGER NOT NULL CHECK (attempts > 0),
    permanent INTEGER NOT NULL DEFAULT 0 CHECK (permanent IN (0, 1)),
    last_error TEXT NOT NULL,
    next_retry_at TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

-- 模型用量（按 UTC 日、模型、用途汇总），用于显示已用 token 与费用估算。
CREATE TABLE ai_usage (
    day TEXT NOT NULL,
    model TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN ('tag', 'review', 'sample', 'test')),
    requests INTEGER NOT NULL DEFAULT 0,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, model, purpose)
) STRICT;

-- 抽样对比：同一批曲目分别用主模型和复核模型打标，只记录结果、不写入 track_tags。只保留最近一次。
CREATE TABLE ai_sample_runs (
    id INTEGER PRIMARY KEY,
    size INTEGER NOT NULL CHECK (size BETWEEN 1 AND 1000),
    primary_model TEXT NOT NULL,
    review_model TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'done', 'cancelled', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    finished_at TEXT
) STRICT;
CREATE TABLE ai_sample_items (
    run_id INTEGER NOT NULL REFERENCES ai_sample_runs(id) ON DELETE CASCADE,
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    primary_json TEXT,
    review_json TEXT,
    error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, track_id)
) STRICT;
