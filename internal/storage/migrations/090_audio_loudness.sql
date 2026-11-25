-- 单曲与专辑 EBU R128 响度分析缓存、失败退避与后台任务设置。
-- 与音频文件物理指纹（file_size, modified_at_ns）绑定，文件变化后自动失效重算。

CREATE TABLE IF NOT EXISTS audio_loudness_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    paused INTEGER NOT NULL DEFAULT 0 CHECK (paused IN (0, 1)),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE TABLE IF NOT EXISTS audio_loudness (
    audio_file_id INTEGER PRIMARY KEY REFERENCES audio_files(id) ON DELETE CASCADE,
    file_size INTEGER NOT NULL CHECK (file_size >= 0),
    modified_at_ns INTEGER NOT NULL,
    analyzer_version INTEGER NOT NULL DEFAULT 1 CHECK (analyzer_version > 0),
    integrated_lufs REAL NOT NULL,
    true_peak_db REAL NOT NULL,
    loudness_range REAL,
    analyze_ms INTEGER NOT NULL DEFAULT 0 CHECK (analyze_ms >= 0),
    measured_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
CREATE INDEX IF NOT EXISTS idx_audio_loudness_lufs ON audio_loudness(integrated_lufs);
CREATE INDEX IF NOT EXISTS idx_audio_loudness_measured ON audio_loudness(measured_at);

CREATE TABLE IF NOT EXISTS audio_loudness_failures (
    audio_file_id INTEGER PRIMARY KEY REFERENCES audio_files(id) ON DELETE CASCADE,
    file_size INTEGER NOT NULL CHECK (file_size >= 0),
    modified_at_ns INTEGER NOT NULL,
    analyzer_version INTEGER NOT NULL DEFAULT 1 CHECK (analyzer_version > 0),
    permanent INTEGER NOT NULL DEFAULT 0 CHECK (permanent IN (0, 1)),
    attempts INTEGER NOT NULL DEFAULT 1 CHECK (attempts > 0),
    next_retry_at TEXT NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
CREATE INDEX IF NOT EXISTS idx_audio_loudness_failures_retry
    ON audio_loudness_failures(analyzer_version, permanent, next_retry_at);

CREATE TABLE IF NOT EXISTS album_loudness (
    album_id INTEGER PRIMARY KEY REFERENCES albums(id) ON DELETE CASCADE,
    integrated_lufs REAL NOT NULL,
    true_peak_db REAL NOT NULL,
    track_count INTEGER NOT NULL CHECK (track_count > 0),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
