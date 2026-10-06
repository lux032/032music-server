-- 曲库目录监控（自动入库）的管理页覆盖设置。没有行时使用环境变量
-- MUSIC_SERVER_WATCH_INTERVAL 的默认值；管理页“恢复默认”会删除该行。
CREATE TABLE library_watch_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    interval_seconds INTEGER NOT NULL CHECK (interval_seconds > 0),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
