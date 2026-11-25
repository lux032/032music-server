# Spec Delta

## ADDED Requirements

### Requirement: 声学大向量 BLOB 冷热拆分至伴随表且发现页映射状态索引就绪
数据库表结构 SHALL 在 Migration 087 中将 `track_audio_features` 的 4 个大向量 BLOB 列（`genre`、`moodtheme`、`instrument`、`embedding`）拆分至级联 1:1 伴随表 `track_audio_vectors`，保留主表全部标量列、外键约束及索引，并维护 `idx_discovery_mappings_status_subject ON discovery_bangumi_mappings(status, subject_id, rgid)`。

#### Scenario: Migration 087 无损迁移声学特征向量并通过完整性与备份校验
- **WHEN** 已含声学特征与向量 BLOB 的数据库从迁移 086 升级到 087（或全新迁移），随后执行曲目级联删除与备份快照校验
- **THEN** 所有既有标量特征与 4 个向量 BLOB 均逐字节无损保留，删除 `tracks` 行可级联清除 `track_audio_features` 与 `track_audio_vectors`，`idx_discovery_mappings_status_subject` 存在，`PRAGMA integrity_check` / `foreign_key_check` 与备份校验全部通过
