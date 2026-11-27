# Spec Delta

## Purpose

规范 `032music-server` 的艺术家规范展示名统一（以 MusicBrainz 原语种原名为准）与跨语种同身份自动去重合并能力，覆盖专辑艺人 MBID 采集、原文名可逆规范化与别名检索、确定性同 MBID 冲突自动合并、AI 冲突合并分级自治、人工撤销防复合护栏及存量重复艺人一键收敛。

## ADDED Requirements

### Requirement: 扫描导入采集并校验专辑艺人的 MusicBrainz 身份证据
系统在扫描音频文件并持久化角色化身份证据时，SHALL 将专辑艺人角色（`album_artist`）及其 `MUSICBRAINZ_ALBUMARTISTID` / `MUSICBRAINZ ALBUM ARTIST ID` 标签纳入与主唱及幕后创作者同等严格的位置对齐与冲突校验体系，使仅作为专辑艺人出现的歌手也能获得可靠的 `TaggedMBID` 身份依据。

#### Scenario: 专辑艺人名称与 MBID 位置严格对齐时记录有效身份证据
- **WHEN** 扫描包含单个或多个专辑艺人且 `ALBUMARTISTS` / `ALBUMARTIST` 与 `MUSICBRAINZ_ALBUMARTISTID` 数量一致、格式合法、无重复且非复合署名的音频文件
- **THEN** 系统为该音频文件的每个专辑艺人写入状态为 `valid` 的 `album_artist` 身份证据，并将该文件的 `album_artist` 检查状态标记为 `verified`，供后续艺术家匹配作为 `TaggedMBID` 使用

#### Scenario: 专辑艺人 MBID 存在数量不匹配、非法 UUID 或复合署名时阻断该角色自动绑定
- **WHEN** 音频文件带有 `MUSICBRAINZ_ALBUMARTISTID` 标签，但专辑艺人槽位与 MBID 数量无法严格一一对齐、包含非法或重复 UUID、或属于未拆分的复合署名
- **THEN** 系统将该文件的 `album_artist` 检查状态标记为 `blocked` 并对相关艺人写入 `conflict` 记录，使这些艺人不采信该冲突标签的 MBID，且不影响其他合法角色的身份证据

### Requirement: 确认 MusicBrainz 身份后自动统一为原文规范展示名并保留检索别名
当「自动统一为 MusicBrainz 原文名」开关处于开启状态且艺人获得有效 MusicBrainz 身份（无论经规则自动匹配、AI 自动确认、人工确认或合并继承）时，若该艺人不存在人工自定义展示名且当前展示名为纯拉丁文本，而 MusicBrainz 主名（或日区 `locale=ja` 且 `primary=true` 的非法律名/非搜索提示官方艺名别名）包含非拉丁原语种文字，系统 SHALL 将其规范展示名更新为该 MusicBrainz 原文名，同时保留原始扫描名作为可搜索别名并保持扫描身份键不可变。

#### Scenario: 纯拉丁罗马音艺人确认东亚原文名 MusicBrainz 身份后自动改写展示名
- **WHEN** 规范命名开关开启、本地艺人为无 `user_display_name` 的纯拉丁名（如 `Yuki Kajiura` 或 `Jay Chou`），且确认的 MusicBrainz 资料主名为非拉丁原文名（如 `梶浦由記` 或 `周杰倫`）
- **THEN** 系统记录其原始扫描名、将展示名更新为 MusicBrainz 原文名、将原罗马音名写入可搜索别名，并保持原 `identity_key` 不变使后续重扫同名罗马音曲目仍归属同一艺人

#### Scenario: 日区艺人主名为拉丁但存在主日文官方艺名别名时采用日文官方别名
- **WHEN** 规范命名开关开启、本地艺人为纯拉丁名，其确认的 MusicBrainz 资料 `country` 为 `JP`、主名为纯拉丁，但结构化官方别名中存在未结束且 `locale` 属于 `ja`、`primary=true`、非 `Legal name` 且非 `Search hint` 的非拉丁别名
- **THEN** 系统将该艺人的展示名统一为该日文主官方别名，同时保留原拉丁名为可搜索别名

#### Scenario: 官方拉丁艺名与欧美拉丁艺人保持原拉丁展示名不变
- **WHEN** 确认的 MusicBrainz 资料对应官方拉丁艺名（如 `YOASOBI`、`LiSA`、`Aimer`，即使其包含日文法律本名别名）或欧美拉丁艺人（如 `Taylor Swift`）
- **THEN** 系统不改写其拉丁展示名，也不会使用法律本名或搜索提示别名替代其官方艺名

#### Scenario: 人工自定义展示名始终优先于自动规范命名
- **WHEN** 艺人已设置非空的人工自定义展示名（`user_display_name`），且触发 MusicBrainz 身份确认、合并或存量启动收敛
- **THEN** 系统保留该人工自定义展示名作为对外展示名称，绝不清除或覆盖 `user_display_name`

#### Scenario: 解除 MusicBrainz 身份绑定或撤销 AI 确认时恢复原始扫描展示名
- **WHEN** 管理员对已自动规范改名的艺人执行解除 MusicBrainz 身份绑定，或撤销触发该改名的 AI 自动确认决策
- **THEN** 系统将该艺人的展示名恢复为保存的原始扫描名，并清理因自动规范命名写入的原始标签别名（若存在 `user_display_name` 则继续保留人工命名）

### Requirement: 元数据设置提供规范命名开关并在启动时静默收敛存量已确认艺人
系统 SHALL 在元数据设置中持久化并暴露「自动统一为 MusicBrainz 原文名」开关（默认开启），并在服务启动完成迁移且该开关开启时，基于本地已缓存的 MusicBrainz 外部资料静默收敛存量已确认罗马音艺人的展示名。

#### Scenario: 关闭规范命名开关后不再自动改写展示名
- **WHEN** 管理员在 `/admin/settings/metadata` 关闭「自动统一为 MusicBrainz 原文名」并保存，随后有新的罗马音艺人确认 MusicBrainz 身份
- **THEN** 系统保持该艺人原有扫描展示名不变，不执行自动原文名改写

#### Scenario: 服务启动时基于本地 MusicBrainz 资料静默统一存量已确认艺人
- **WHEN** 服务启动完成数据库迁移且规范命名开关处于开启状态，库内存在未合并、无 `user_display_name`、当前展示名为纯拉丁文本但自身或已合并来源艺人持有本地 MusicBrainz 资料（主名或结构化日文主别名为非拉丁原文名）的存量艺人
- **THEN** 系统在本地事务中幂等完成这些艺人的原文名统一与原罗马音别名保留，不发起外部网络请求，且已设置 `user_display_name` 的艺人保持不变

### Requirement: 自动匹配命中高置信同 MBID 冲突时执行确定性自动合并并优先复用本地 MB 资料
在自动匹配阶段，若待匹配艺人携带 `TaggedMBID` 且本地外部资料库已缓存该 MusicBrainz ID 的资料，并且本地资料的名称/别名核验通过，系统 SHALL 直接复用本地资料免去对 MusicBrainz 的重复网络请求；仅在本地缺失该 MBID 资料或本地别名未命中时再回退发起 MusicBrainz 联网查询。当高置信候选（评分 `>= 92`，来自经名称校验的 `TaggedMBID` 或 `MusicBrainz + Last.fm` 双源同 MBID 独立佐证）对应的 MusicBrainz ID 已被库内另一规范艺人持有，且满足非人工核对（`requires_manual_review = 0`）、双方均非复合署名、且无历史回滚记录时，系统 SHALL 自动将当前艺人合并入规范目标艺人而非静默放弃。

#### Scenario: 携带已缓存 TaggedMBID 且命中本地别名时零联网完成自动合并
- **WHEN** 库内艺人 `梶浦由記` 已绑定 MBID `X` 且本地 `artist_external_profiles` 缓存了其名称与别名（包含 `Yuki Kajiura`），新扫入的 `Yuki Kajiura` 带有相同 `TaggedMBID = X` 并进入自动匹配
- **THEN** 系统直接命中本地已缓存的 MusicBrainz 资料通过名称核验并在零 MusicBrainz 网络请求下自动将 `Yuki Kajiura` 合并至 `梶浦由記`，同时记录自动匹配合并审计记录

#### Scenario: 本地已缓存 MB 资料的别名未命中时回退联网核验
- **WHEN** 待匹配艺人带有已缓存 `TaggedMBID = X`，但本地缓存的 MB 资料名称与别名未能匹配当前艺人名
- **THEN** 系统回退发起一次 MusicBrainz 联网查询以核实最新官方别名；若联网资料仍不匹配则标记 `requires_manual_review` 并禁止自动合并

#### Scenario: 高置信同 MBID 冲突自动合并并保留完整审计与后续重扫直连
- **WHEN** 库内已存在确认 MBID 的艺人 `梶浦由記`，新扫入的 `Yuki Kajiura` 在自动匹配中通过经名称核验的 `TaggedMBID` 或 `MusicBrainz + Last.fm` 双源一致获得同一 MBID 且评分 `>= 92`
- **THEN** 系统在事务内自动将 `Yuki Kajiura` 合并至 `梶浦由記`，生成带自动匹配触发来源标识的合并审计记录（支持在 `/admin/merges` 一键撤销），将 `Yuki Kajiura` 保留为目标艺人的可搜索别名，并在合并后使后续重扫同名罗马音曲目自动路由到 `梶浦由記`

#### Scenario: 标签名与 MB 资料不匹配或命中复合署名时禁止自动合并
- **WHEN** 待匹配艺人的候选带有 `requires_manual_review` 标记（例如文件错标了其他歌手的 MBID）或源/目标任一方属于复合署名
- **THEN** 系统拒绝执行自动合并，将该候选保留在待审状态等待人工核对或复合署名拆分

### Requirement: 人工撤销过的合并对受回滚记忆保护不再自动复合
系统 SHALL 尊重管理员在合并历史中对任意艺人合并记录执行的手动撤销操作；一旦某对艺人存在已回滚（`rolled_back`）的合并历史记录，后续确定性自动匹配、存量一键收敛与 AI 冲突合并均 MUST 跳过该对艺人的自动合并。

#### Scenario: 撤销自动合并后再次运行匹配与 AI 审核不再重新自动合并
- **WHEN** 管理员在 `/admin/merges` 撤销了艺人 A 并入艺人 B 的合并操作，随后再次触发后台艺术家匹配、存量一键收敛或 AI 审核
- **THEN** 系统识别 A 与 B 之间存在已回滚历史，绝不自动将 A 再次并入 B（亦不自动将 B 并入 A），仅允许管理员手动执行合并

### Requirement: 无 MBID 艺人的 AI 冲突合并支持独立分级自治与双向一致回滚
系统 SHALL 在 AI 审核设置中提供独立的艺人冲突合并策略 `artist_merge_mode`（`off`、`recommend`、`auto`，默认 `auto`），允许 AI 初审对无 MBID 但与库内已绑定艺人共享同一候选身份的艺人做出合并研判，并在高置信自动合并时确保合并历史与 AI 决策记录双向一致可回滚。

#### Scenario: AI 冲突合并在 auto 模式下对高置信非复合冲突自动执行合并
- **WHEN** AI 初审启用、`artist_merge_mode` 为 `auto`，AI 对某无 MBID 的非复合、非人工核对冲突候选返回选中结论且置信度 `>= 92`（若触发联网补证则引文校验通过），且源与目标艺人无历史回滚记录
- **THEN** 系统原子执行冲突合并，同步写入标注为 AI 触发的合并审计记录与含合并操作 ID 的 AI 决策快照，并对存活目标艺人按需应用规范展示名

#### Scenario: AI 冲突合并在 recommend 或低置信场景仅推荐不自动合并
- **WHEN** `artist_merge_mode` 为 `recommend`，或 `artist_merge_mode` 为 `auto` 但 AI 置信度低于 `92`（或联网引文校验未通过）
- **THEN** 系统不执行自动合并，而是保存合并建议供管理员在 `/admin/matches` 人工确认；当 `artist_merge_mode` 为 `off` 时，系统不对冲突候选给出合并建议或自动合并

#### Scenario: 从合并历史或 AI 审核任一入口撤销 AI 自动合并时双方状态同步回滚
- **WHEN** 管理员在 AI 审核页撤销一条已自动合并的 AI 决策，或在 `/admin/merges` 撤销对应的合并操作记录
- **THEN** 曲目/专辑关联、合并别名、候选状态、合并记录状态（`rolled_back`）与 AI 决策撤销标记（`reverted=1`）均同步回滚且记录防反复合并记忆

### Requirement: 管理后台提供存量同 MBID 重复艺人的一键收敛入口
系统 SHALL 在 `/admin/matches` 页面展示当前库内满足确定性安全条件的存量同 MBID 重复艺人数量，并提供显式的一键收敛操作供管理员按需触发批量合并。

#### Scenario: 管理员触发存量同 MBID 重复艺人一键收敛
- **WHEN** 库内存在多个此前已分裂的未合并艺人，其待审高置信候选或文件标签 MBID 命中另一规范艺人的已确认 MusicBrainz 身份，且管理员在 `/admin/matches` 点击「一键收敛同 MBID 重复艺人」
- **THEN** 系统逐一校验非复合署名、非人工核对、名称核验与无历史回滚记录后批量执行合并（标注为存量收敛来源写入合并历史），跳过不满足安全条件的项，并在页面反馈成功合并数量、跳过数量以及前往 `/admin/merges` 核对或撤销的指引
