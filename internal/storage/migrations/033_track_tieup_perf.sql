-- 批次 8.5：TracksForBangumiTieup 性能（用户库实测 1m50s）。
-- 三个别名 GROUP_CONCAT 相关子查询的 WHERE 是 track_id+role，但现有索引是
-- PK(track_id,artist_id,position,role) 与 idx_track_artists_role(role,...)，
-- 优化器选了按 role 全扫（≈全库每个 primary 行），每首曲目一次。
-- 另：track_subject_candidates 的 NOT EXISTS(status='candidate') 走了
-- (status,id) 索引，同样按状态全扫；补 (track_id,status)。
CREATE INDEX IF NOT EXISTS idx_track_artists_track_role ON track_artists(track_id, role, artist_id);
CREATE INDEX IF NOT EXISTS idx_track_subject_candidates_track_status ON track_subject_candidates(track_id, status);
