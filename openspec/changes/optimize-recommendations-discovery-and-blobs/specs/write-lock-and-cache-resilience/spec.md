# Spec Delta

## ADDED Requirements

### Requirement: DailyMix 存为歌单与 Discovery 写事务前置获取 SQLite 写锁，声学快照查表解码与展开点积
`SaveDailyMixAsPlaylist` 及 Discovery 刷新、候选持久化与决策写事务 SHALL 通过 `beginWrite` 前置获取 SQLite 写锁；`AcousticSnapshot` SHALL 使用与原 IEEE-754 float16 逐位一致的查找表解码与展开无界检查 int8 点积，并提供按索引点积接口供批量扫描复用。

#### Scenario: 并发 Discovery 刷新/决策与 DailyMix 落库无快照锁冲突
- **WHEN** Discovery 候选写入、封面更新或映射决策与 DailyMix 存为歌单及播放心跳并发执行
- **THEN** 各写事务前置获取写锁并通过 `busy_timeout` 平滑串行提交，不触发偶发 `SQLITE_BUSY_SNAPSHOT` 错误

#### Scenario: 声学快照 float16 查表解码与展开点积保持数值一致
- **WHEN** `AcousticSnapshot` 构建索引并执行 `Cosine`、`CosineTo`、`CosineToIndex` 与 `Neighbors`
- **THEN** 全部 65536 个 `uint16` 值的 float16 解码结果与参考按位解码逐位一致，点积结果与参考逐元素累加完全一致
