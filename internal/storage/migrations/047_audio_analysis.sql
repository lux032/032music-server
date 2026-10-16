-- 声学分析（analyzer/ 服务）。默认关闭；管理页开启即开始首次全量分析，之后新入库
-- 或文件变化的曲目自动分析。设计见 docs/daily-mix-design.md 第 12、13 节。

-- 单行设置。没有行时等同于全部默认值（关闭）。
CREATE TABLE audio_analysis_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    paused INTEGER NOT NULL DEFAULT 0 CHECK (paused IN (0, 1)),
    -- 可选运行时间窗（UTC+8 当天分钟数，可跨午夜）；两者同时为空表示不限
    window_start_minute INTEGER CHECK (window_start_minute IS NULL OR window_start_minute BETWEEN 0 AND 1439),
    window_end_minute INTEGER CHECK (window_end_minute IS NULL OR window_end_minute BETWEEN 0 AND 1439),
    first_enabled_at TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK ((window_start_minute IS NULL) = (window_end_minute IS NULL)),
    CHECK (window_start_minute IS NULL OR window_start_minute <> window_end_minute)
) STRICT;

-- 唤醒度/效价回归模型：用本库校准曲目（full 模式，含 musicnn/DEAM 输出）在本地拟合。
-- 权重由 CC BY-NC 模型输出派生，只存本地数据库、不进仓库。每个分析版本最多一个生效模型。
CREATE TABLE audio_av_models (
    id INTEGER PRIMARY KEY,
    analysis_version TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('embedding', 'heads')),
    features_json TEXT NOT NULL DEFAULT '[]',
    dims INTEGER NOT NULL CHECK (dims > 0),
    weights BLOB NOT NULL,              -- float32 小端，[dims][2]，(valence, arousal)
    bias_valence REAL NOT NULL,
    bias_arousal REAL NOT NULL,
    samples INTEGER NOT NULL CHECK (samples > 0),
    metrics_json TEXT NOT NULL,
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
CREATE UNIQUE INDEX idx_audio_av_models_active ON audio_av_models(analysis_version) WHERE active = 1;

-- 每首曲目的声学特征。向量均为 float16 小端。file_fingerprint = 音频文件 id:大小:修改时间(ns)，
-- 与 analysis_version 任一变化即视为需要重新分析。
CREATE TABLE track_audio_features (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    file_fingerprint TEXT NOT NULL,
    analysis_version TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('fast', 'full')),
    duration_ms INTEGER NOT NULL CHECK (duration_ms >= 0),
    windows INTEGER NOT NULL CHECK (windows >= 0),
    patches INTEGER NOT NULL CHECK (patches >= 0),
    genre BLOB NOT NULL,                -- 400 类 Discogs 风格
    moodtheme BLOB NOT NULL,            -- 56 类 MTG-Jamendo 情绪/主题
    instrument BLOB NOT NULL,           -- 40 类乐器
    embedding BLOB NOT NULL,            -- 1280 维 effnet embedding
    heads_json TEXT NOT NULL,
    musicnn_valence REAL,               -- 仅 full 模式（校准样本）
    musicnn_arousal REAL,
    valence REAL,                       -- 生效值：musicnn 原值或回归预测
    arousal REAL,
    av_source TEXT CHECK (av_source IS NULL OR av_source IN ('musicnn', 'regression')),
    av_model_id INTEGER REFERENCES audio_av_models(id) ON DELETE SET NULL,
    analyze_ms INTEGER NOT NULL DEFAULT 0 CHECK (analyze_ms >= 0),
    analyzed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
CREATE INDEX idx_track_audio_features_version ON track_audio_features(analysis_version, av_source);
CREATE INDEX idx_track_audio_features_analyzed ON track_audio_features(analyzed_at);

-- 分析失败记录。只对同一文件指纹和分析版本生效：文件变化或版本升级后自动重试。
-- permanent=1（文件无法解码/不存在）不再自动重试；否则按 next_retry_at 指数退避。
CREATE TABLE audio_analysis_failures (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    file_fingerprint TEXT NOT NULL,
    analysis_version TEXT NOT NULL,
    attempts INTEGER NOT NULL CHECK (attempts > 0),
    permanent INTEGER NOT NULL DEFAULT 0 CHECK (permanent IN (0, 1)),
    last_error TEXT NOT NULL,
    next_retry_at TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

-- 各分析版本的向量类别名（来自分析服务 /v1/labels）。
CREATE TABLE audio_analysis_labels (
    analysis_version TEXT PRIMARY KEY,
    labels_json TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
