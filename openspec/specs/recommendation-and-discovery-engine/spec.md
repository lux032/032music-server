# recommendation-and-discovery-engine Specification

## Purpose

定义每日推荐歌单（DailyMix）数据加载、主题构池与声学计算，以及发现页（Discovery）信号聚合、列表惰性反序列化与候选排序的低复杂度与等价行为规范。

## Requirements

### Requirement: DailyMix 曲库加载与主题构池消除全库二重遍历、相关子查询及逐曲重复计算
`RecLibrary` SHALL 通过连接替代主唱歌手相关子查询、使用 `UNION ALL` 消除多余去重排序并避免逐曲重复 map 分配；DailyMix 换一换 SHALL 批量加载当日歌单曲目；主题构建 SHALL 通过单遍倒排索引与预计算状态消除 216 组 `mood × style` 全库扫描与逐曲重复日期解析，同时保持主题与选曲输出一致。

#### Scenario: RecLibrary 保持曲库语义一致且避免主唱相关子查询与作品排序去重
- **WHEN** `RecLibrary` 加载包含合并歌手、多作品关联、多来源标签与声学特征的曲库
- **THEN** 返回的 `RecTrack` 切片与既有语义完全一致（含合并后规范歌手名、去重作品 ID、生效标签优先级与 `TitleKey`），主唱查询不使用逐行相关子查询且作品查询不触发 `UNION` 临时排序

#### Scenario: DailyMix 主题构建使用倒排索引产出一致的候选主题集
- **WHEN** 曲库包含多情绪、多风格、轻度悲伤过滤、未打标声学扩池、歌手电台与重新发现曲目
- **THEN** `builder.Candidates()` 通过单遍构建的倒排索引、预计算悲伤标记与预解析声学下标，输出与原实现一致的候选主题 ID、池内容与桥接曲目，且在 1.7 万首规模下显著缩短构建耗时

#### Scenario: DailyMix 换一换批量加载当日歌单曲目且选曲避免重复日期解析
- **WHEN** `GenerateReplacement` 替换当日某一槽位歌单
- **THEN** 当日所有歌单曲目通过单次批量查询（`DailyMixItemsByMixIDs`）加载而非逐歌单 N+1 查询，且选曲阶段复用预计算的 7 天与 14 天边界日期

### Requirement: Discovery 信号聚合走计数索引，列表查询惰性反序列化且排序预计算哈希键
`DiscoveryArtistSignals` 与 `DiscoveryWorkSignals` SHALL 使用可走 `idx_playback_sessions_counted` 范围扫描的 UTC 时间戳比较过滤近 90 天播放；`ListDiscovery` SHALL 消除候选相关子查询、去重 ownership 查询、避免非 acquiring 标签页加载完整采集任务并将 JSON 反序列化推迟至分页结果；`discovery.Rank` SHALL 在排序前预计算 explore 权重与哈希键。

#### Scenario: Discovery 90 天播放信号查询命中 counted_at 索引
- **WHEN** `DiscoveryArtistSignals` 或 `DiscoveryWorkSignals` 统计近 90 天衰减播放分
- **THEN** 查询计划使用 `idx_playback_sessions_counted` 索引范围查找而非对 `counted_at` 套用 `julianday(...)` 导致全表扫描，且衰减分值计算结果保持一致

#### Scenario: ListDiscovery 在不同标签页下仅对最终展示页执行 JSON 反序列化并输出一致计数
- **WHEN** `ListDiscovery` 按 `recommended`、`possible`、`ignored`、`acquiring` 或 `mapping` 标签页与分页参数查询
- **THEN** 各项 `DiscoveryCounts`、条目排序、分页切片及映射展示信息与原逻辑完全一致，但非 `acquiring` 标签页不拉取完整采集任务明细，且未进入最终分页切片的候选不执行 `secondary_types_json`、`reasons_json` 或 `evidence_json` 反序列化

#### Scenario: discovery.Rank 预计算 explore 排序键保持完全一致的排位顺序
- **WHEN** `discovery.Rank` 对大量 `exploit` 与 `explore` 候选进行排序与限额抽取
- **THEN** `explore` 排序前按 O(N) 预计算权重与 SHA-256 哈希键，输出与原比较器逐条完全一致的 `Rank`、`Lane`、`Score` 与 `Reasons`
