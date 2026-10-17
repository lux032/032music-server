-- 在线歌词缓存（网易云、LRCLIB）。本地歌词（同目录 .lrc、内嵌标签）每次直接读文件，不进这张表。
-- 查找顺序：手动指定（manual=1）> 本地歌词 > 自动匹配的缓存 > 在线抓取。
-- status：found = 有歌词；not_found = 在线没找到，retry_after 之后再试；
--         suppressed = 用户在客户端清除了在线歌词，不再自动抓取（删除这一行即恢复自动匹配）。
CREATE TABLE online_lyrics (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('found', 'not_found', 'suppressed')),
    manual INTEGER NOT NULL DEFAULT 0 CHECK (manual IN (0, 1)),
    source TEXT NOT NULL DEFAULT '' CHECK (source IN ('', 'netease', 'lrclib')),
    remote_id TEXT NOT NULL DEFAULT '',
    remote_title TEXT NOT NULL DEFAULT '',
    remote_artist TEXT NOT NULL DEFAULT '',
    lyrics TEXT NOT NULL DEFAULT '',
    -- 来源自带的翻译（网易云 tlyric，LRC），按时间戳与原文对齐。
    translation_lyrics TEXT NOT NULL DEFAULT '',
    retry_after TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK ((status = 'found') = (lyrics <> ''))
) STRICT;
CREATE INDEX idx_online_lyrics_status ON online_lyrics(status, updated_at);
