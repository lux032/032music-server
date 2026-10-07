-- 缺失文件清理策略（参照 Navidrome Scanner.PurgeMissing）的管理页覆盖设置。
-- 没有行时使用环境变量 MUSIC_SERVER_PURGE_MISSING（默认 never）；管理页
-- “恢复默认”会删除该行。
CREATE TABLE purge_missing_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    policy TEXT NOT NULL CHECK (policy IN ('never', 'always', 'full')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
