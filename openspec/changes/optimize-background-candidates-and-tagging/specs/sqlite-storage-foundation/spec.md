# Spec Delta

## ADDED Requirements

### Requirement: 后台候选排序与按歌手头像候选查询索引就绪
数据库表结构 SHALL 维护 `tracks(added_at DESC, id DESC)` 曲目入库顺序索引与 `artist_image_candidates(artist_id, status)` 按歌手待审核头像候选索引，以支持未打标新曲有序早停与按歌手索引过滤。

#### Scenario: 迁移 086 干净安装配套索引并通过备份表结构校验
- **WHEN** 数据库从零迁移或从迁移 085 升级并经备份快照校验器验证
- **THEN** `idx_tracks_added_id` 与 `idx_artist_image_candidates_artist_status` 均存在，`PRAGMA integrity_check` 返回 `ok`，`PRAGMA foreign_key_check` 返回零行，且备份表结构校验通过
