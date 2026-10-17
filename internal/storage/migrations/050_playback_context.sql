-- 每日推荐 P6：带歌单上下文的播放会话与隐式反馈（docs/daily-mix-design.md 第 7 节）。

-- 会话的播放上下文：start 事件可带 context；续播链（resumedFromSessionId）继承前一个会话的上下文。
-- context_processed 标记该会话是否已计入反馈汇总（会话在 ended 之后才处理，处理一次即置 1）。
ALTER TABLE playback_sessions ADD COLUMN context_type TEXT CHECK (context_type IS NULL OR context_type IN ('dailyMix'));
ALTER TABLE playback_sessions ADD COLUMN context_id INTEGER;
ALTER TABLE playback_sessions ADD COLUMN context_position INTEGER;
ALTER TABLE playback_sessions ADD COLUMN context_processed INTEGER NOT NULL DEFAULT 0 CHECK (context_processed IN (0, 1));
CREATE INDEX idx_playback_sessions_context_pending ON playback_sessions(ended_at)
    WHERE context_type IS NOT NULL AND context_processed = 0;

-- 收藏时间：用来判断“播放中收藏”。
ALTER TABLE tracks ADD COLUMN favorited_at TEXT;

-- 曲目在推荐场景中的亲和度。score 在 updated_at 时刻的值，读取时按 30 天半衰期衰减。
CREATE TABLE rec_track_affinity (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    score REAL NOT NULL DEFAULT 0,
    plays INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
) STRICT;

-- 情绪/风格标签的亲和度：单曲信号按该曲标签权重分摊；同样按 30 天半衰期衰减。
CREATE TABLE rec_tag_affinity (
    kind TEXT NOT NULL CHECK (kind IN ('mood', 'style')),
    tag TEXT NOT NULL,
    score REAL NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (kind, tag)
) STRICT;

-- 每个歌单里每首歌的播放汇总。
CREATE TABLE daily_mix_play_tracks (
    mix_id INTEGER NOT NULL REFERENCES daily_mixes(id) ON DELETE CASCADE,
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    plays INTEGER NOT NULL DEFAULT 0,
    completes INTEGER NOT NULL DEFAULT 0,
    quick_skips INTEGER NOT NULL DEFAULT 0,
    signal REAL NOT NULL DEFAULT 0,
    favorited INTEGER NOT NULL DEFAULT 0 CHECK (favorited IN (0, 1)),
    PRIMARY KEY (mix_id, track_id)
) STRICT;

-- 歌单级信号已计入主题 α/β 的量（重新汇总时先减旧值再加新值，保证幂等）。
CREATE TABLE daily_mix_play_stats (
    mix_id INTEGER PRIMARY KEY REFERENCES daily_mixes(id) ON DELETE CASCADE,
    applied_alpha REAL NOT NULL DEFAULT 0,
    applied_beta REAL NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
) STRICT;
