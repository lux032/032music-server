-- AI 打标 stamp 改为只由歌曲内容组成（不再使用 tracks/albums.updated_at，收藏等操作不再触发重新打标）。
-- 已有记录的换算在 Go 中完成（见 migrationGoSteps / upgradeTaggingStampsV2）。
SELECT 1;
