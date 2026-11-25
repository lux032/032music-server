# Spec Delta

## ADDED Requirements

### Requirement: AI 打标与声学分析写事务前置获取 SQLite 写锁
AI 打标（`SaveTrackTagResult`、`ResetTrackTagging`、`RecordTaggingFailure`、`CreateAISampleRun`）与声学分析（`SaveTrackAudioFeatures`、`SaveAudioAVModel`）的多语句及读改写事务 SHALL 在读取或修改状态前前置获取 SQLite 写锁，使并发后台任务与播放落库通过 `busy_timeout` 平滑串行而避免 `SQLITE_BUSY_SNAPSHOT` 错误。

#### Scenario: 并发 AI 打标、声学分析落库与播放事件写入无快照锁冲突
- **WHEN** 多个并发 AI 打标协程保存结果或失败记录的同时，声学分析写入特征且播放事件持久化并发执行
- **THEN** 所有写事务均前置获取写锁并平滑提交，不产生偶发 `SQLITE_BUSY_SNAPSHOT` 错误
