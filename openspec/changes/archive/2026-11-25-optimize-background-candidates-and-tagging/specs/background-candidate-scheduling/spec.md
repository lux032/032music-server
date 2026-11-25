# Spec Delta

## Purpose

定义后台 AI 打标、声学分析与校准、歌手简介/头像补全以及元数据富化任务的低复杂度候选筛选、集合化 stamp 与进度统计、以及批量化/索引化目标查询行为规范。

## ADDED Requirements

### Requirement: AI 打标候选筛选避免对未打标新曲全库重算 stamp 并以集合聚合检测过期曲目
`NextTaggingCandidates` SHALL 保持既有的优先级顺序、失败退避过滤、排除列表与逐字节一致的 `source_stamp` 结果，同时对无活跃失败记录的未选中新曲跳过 stamp 计算，并对已打标过期检测与 `TaggingStats` 采用按表预聚合的集合查询。

#### Scenario: 未打标新曲筛选跳过已打标曲目与未选中曲目的 stamp 计算
- **WHEN** 曲库同时包含已打标与未打标就绪曲目，且 `NextTaggingCandidates` 请求 `limit` 首候选
- **THEN** 系统通过曲目入库时间索引按 `(added_at DESC, track_id DESC)` 顺序早停选出未打标新曲，不触发全表排序，也不对无活跃失败记录的未选中曲目计算 `source_stamp`

#### Scenario: 单曲 stamp 查询仅按主键读取目标曲目
- **WHEN** 调用 `TrackTaggingStamp` 查询指定 `trackID`
- **THEN** 查询按主键定位该曲目而非物化全库 stamp，返回与原公式一致的 stamp；若曲目无可用音频文件则返回 `(false, nil)`

#### Scenario: 集合化 stamp 计算与原参考公式逐字节一致
- **WHEN** 曲目包含多主唱歌手、多专辑歌手、流派覆盖、普通流派、外置 `.lrc` 标记、声学特征及曲目/专辑作品关联
- **THEN** `NextTaggingCandidates`、`TrackTaggingStamp` 返回以及 `TaggingStats` 评估的 stamp 与原 `taggingStampSQL` 逐字节完全一致，且不会使既有 `track_tag_meta` 或 `ai_tagging_failures` 记录失效

#### Scenario: 最近打标结果通过批量查询加载曲目标签
- **WHEN** `RecentTagResults` 加载最近完成打标的曲目列表
- **THEN** 系统通过批量 `IN (...)` 查询一次性加载所有返回曲目的生效标签并保持来源与权重排序，而非逐曲执行 N+1 查询

### Requirement: 声学分析与校准候选筛选避免全库扫描与冗余排序
`NextAudioAnalysisCandidate` SHALL 使用主键逆序扫描早停且不产生临时 B-tree 排序；`NextAudioCalibrationCandidate` 与 `AudioAnalysisStats` SHALL 通过覆盖索引聚合主可用文件；`AudioAnalysisFailures` SHALL 从失败表驱动；校准目标计算 SHALL 直接统计可分析曲目数。

#### Scenario: 快速声学分析候选选择按主键逆序早停且无排序
- **WHEN** `NextAudioAnalysisCandidate` 查询下一首待分析曲目
- **THEN** 查询计划按 `tracks` 主键逆序迭代且不出现临时 B-tree 排序，返回相同的最大 ID 合格曲目并严格遵守版本、文件指纹与失败退避规则

#### Scenario: 校准候选筛选以最小回表开销保持确定性 Knuth 哈希抽样
- **WHEN** `NextAudioCalibrationCandidate` 按目标数量选择下一首 full 模式校准曲目
- **THEN** 系统基于可用音频文件覆盖索引计算出完全一致的确定性 Knuth 哈希子集（排除当前版本与指纹下永久失败的曲目），并返回该子集中 ID 最小的待校准曲目

#### Scenario: 声学分析失败列表直接由失败表驱动查询
- **WHEN** `AudioAnalysisFailures` 列出指定版本的生效失败记录
- **THEN** 查询以 `audio_analysis_failures` 为驱动表，仅关联命中的曲目及其主可用文件，而不扫描全库曲目

### Requirement: 歌手补全与富化候选查询消除 N+1 循环、不可索引 OR 子查询及无效参数守卫
歌手简介 `retranslate` 候选筛选 SHALL 使用可索引的拆分 `EXISTS` 与批量简介加载；歌手表演者与可展示简介判定 SHALL 使用索引 `EXISTS` 与参数化语言 `IN`；单歌手匹配、头像缓存扫描与强制富化查询 SHALL 动态省略未启用的过滤分支。

#### Scenario: 重译模式简介候选过滤避免 N+1 查询
- **WHEN** `ArtistBiographyBackfillCandidatesForMode` 以 `retranslate` 模式针对非繁体中文目标语言运行
- **THEN** 候选歌手及其合并来源的简介通过批量查询加载，并输出与逐歌手评估完全一致的过滤后候选列表

#### Scenario: 表演者判定与可展示简介检查走索引查找且无逐行 JSON 展开
- **WHEN** 执行简介补全候选筛选、统计计数或单歌手表演者判定
- **THEN** 表演者身份通过 `track_artists(role, artist_id)` 索引 `EXISTS` 判定，语言过滤使用显式参数化 `IN (?, ...)` 占位符而非逐行 `json_each`

#### Scenario: 单歌手匹配与强制富化查询动态裁剪未使用分支
- **WHEN** `ArtistForMatching` 查询单个歌手 ID，或 `AlbumsForEnrichment` / `WorksForEnrichment` 以 `force=true` 运行
- **THEN** 生成的 SQL 省略可选 `?=0 OR` 与非强制 `EXISTS` 分支，使 SQLite 规划直接主键查找并避免评估无效谓词
