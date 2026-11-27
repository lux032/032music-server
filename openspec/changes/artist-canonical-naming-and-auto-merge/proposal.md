# Proposal

## Why

当前 `032music-server` 中，同一位东亚艺术家在曲库内极易因不同专辑/曲目标签的语种差异分裂为罗马音与原文名两条独立艺人记录（例如 `Yuki Kajiura` 与 `梶浦由記`），即便上游刮削器已锁定 `JP` 发行地区优先也难以彻底避免。根源在于服务端的四处断点：扫描期仅按规范化名称建行且未采集 `album_artist` 的 MBID 证据；自动匹配在遇到同 MBID 已被其他艺人占用时静默放弃而非自动合并；AI 初审硬编码禁止处理带身份冲突的候选；已确认 MusicBrainz 身份的艺人始终沿用首次扫入的罗马音展示名而未收敛为 MB 原文名。这些问题迫使管理员反复手工合并与改名。

## What Changes

- **补齐专辑艺人（`album_artist`）MBID 证据采集**
  - 扩展 `audio_file_artist_identities`、`audio_file_identity_checks` 与扫描导入流程，将 `MUSICBRAINZ_ALBUMARTISTID` / `MUSICBRAINZ ALBUM ARTIST ID` 纳入角色化身份证据体系，与 `primary` 及幕后创作者角色保持同等严格的位置对齐与冲突校验规则。
- **MusicBrainz 原文名自动统一规范化（策略 2A）**
  - 扩展 MusicBrainz 艺人查询解析，保留官方 `aliases` 的 `locale`、`primary`、`type`、`ended` 结构化元数据并持久化至外部资料。
  - 在元数据设置新增「自动统一为 MusicBrainz 原文名」总开关（默认开启）。开启后，任一艺人通过规则自动匹配、AI 确认或人工确认获得有效 MusicBrainz 身份时，若其本地名为纯拉丁/罗马音而 MB 原名（或日区 `locale=ja` 且 `primary=true` 的官方别名）为非拉丁原语种文字（覆盖日文、中文、韩文等东亚原名），自动将其规范展示名统一为 MB 原文名；官方拉丁艺名（如 `YOASOBI`、`LiSA`、`Aimer`）与欧美艺人保持原拉丁名不变。
  - 严格保护边界：`user_display_name` 人工命名始终保持最高优先级，绝不覆盖；原扫描名持久化至 `artists.original_display_name` 并同步写入 `artist_names` 以保证原罗马音全程可搜索；`artists.identity_key` 保持不可变以确保后续重扫稳定落到同一艺人；解除 MusicBrainz 身份绑定或撤销 AI 确认时自动恢复原始展示名。
  - 存量静默收敛：服务启动完成迁移后，自动基于本地已缓存的 MusicBrainz 外部资料（含合并来源继承的资料）静默统一存量已确认罗马音艺人的展示名（零网络开销、跳过已有 `user_display_name` 者）。
- **确定性同 MBID 冲突自动合并、本地资料免联网复用与防反复合并护栏**
  - 当待匹配艺人带有 `TaggedMBID` 时，优先查询本地 `artist_external_profiles` 是否已缓存该 MBID 资料；若本地已存在且 `artistProfileNameMatches` 校验通过，直接复用本地资料构造高置信候选并触发确定性自动合并（免去对 MusicBrainz 的冗余网络请求与限流等待），仅在本地无该 MBID 资料或本地别名未命中时再回退发起联网查询。
  - 在自动匹配绑定路径中，当高置信候选（`score >= 92`，来自经名称核验的 `TaggedMBID` 或 `MusicBrainz + Last.fm` 双源同 MBID 佐证）触发 `*ArtistExternalIDConflictError` 时，若满足非复合署名、`requires_manual_review = 0` 且该艺人对不存在历史回滚记录，自动通过既有 `mergeArtistsTx` 将当前重复艺人并入规范目标艺人。
  - 每次自动合并均生成完整的 `artist_merge_operations` 审计记录（标注触发来源），支持在 `/admin/merges` 一键撤销；合并后源行的 `merged_into_artist_id` 永久生效，后续重扫同罗马音曲目自动复用目标艺人不再分裂。
  - 回滚记忆护栏：凡经管理员在 `/admin/merges` 手动撤销过的艺人对，后续确定性匹配与 AI 审核均不得再次自动合并该对艺人。
- **无 MBID 艺人的 AI 冲突合并分级自治**
  - 在 `ai_review_settings` 新增独立策略 `artist_merge_mode`（`off | recommend | auto`，默认 `auto`），并在 AI 设置页提供独立下拉控制。
  - 解除 AI 艺人初审对 `ConflictOwnerID > 0` 候选的硬拦截，允许模型识别“本地罗马音/别名艺人实质为库内已绑定 MBID 的同一艺人”。
  - 在 `artist_merge_mode = auto` 且置信度 `>= 92`、非复合署名、非 `RequiresManualReview`、未曾被回滚且联网引证通过校验时，原子执行冲突合并（同步写入 `artist_merge_operations` 与 `ai_review_decisions` 快照）；无论从 `/admin/merges` 还是 AI 审核页均可一键回滚并保持双方状态一致。
- **存量重复艺人显式一键收敛入口（方案 A）**
  - 在 `/admin/matches` 提供显式的「一键收敛同 MBID 重复艺人」操作（并展示当前可收敛的同 MBID 冲突数量），点击后批量对库内已分裂且满足确定性安全条件的重复艺人执行自动合并（全量记入合并历史可撤销），并可继续按需触发 AI 审核清理无 MBID 历史重复艺人。

> 注：`MusicAutoTagger` 侧的发行地区排序与标签写法优化不在本变更范围内，留待本变更完成后另行立项。

## Capabilities

### New Capabilities

- `artist-identity-canonicalization`: 规范艺术家身份统一命名与跨语种自动去重合并，覆盖 `album_artist` MBID 采集、MusicBrainz 原文名自动规范化与可逆别名保留、确定性同 MBID 冲突自动合并、AI 冲突合并分级自治、回滚防复合护栏以及存量重复艺人收敛入口。

### Modified Capabilities

<!-- 本变更引入独立内聚的新能力边界，不修改既有 14 个规格的需求契约。 -->

## Impact

- **存储与迁移**：新增迁移 `internal/storage/migrations/095_artist_canonical_naming_and_auto_merge.sql`（扩展 `artists.original_display_name`、`audio_file_artist_identities`/`audio_file_identity_checks` 角色约束支持 `album_artist`、`artist_merge_operations.trigger_source`、`ai_review_settings.artist_merge_mode`、规范命名总开关设置表/列）；更新 `internal/storage/library.go`、`artist_tag_identity.go`、`identity.go`、`artist_identity_conflict.go`、`artist_identity_reset.go`、`ai_review.go`、`ai_review_confirm.go`、`artist_canonical_name.go`（新增）。
- **元数据读取**：`internal/metadata/artist_credits.go`（新增 `RawAlbumArtistNamesForIdentity` 以支持严格的专辑艺人位置对齐校验）。
- **富化与 AI 审核**：`internal/enrichment/manager.go`、`artist_runtime.go`、`internal/aireview/service.go`、`prompts.go`。
- **HTTP 与管理后台 UI**：`internal/http/identity.go`、`ai_settings.go`、`app.go`、`internal/http/templates/metadata-settings.html`、`ai-settings.html`、`match-review.html`、`merge-history.html`、`internal/http/assets/admin.js`。
- **外部依赖与兼容性**：无新增外部依赖；严格兼容既有 `COALESCE(user_display_name, display_name)` 全量查询与 Subsonic/Sync API、复合署名拆分边界与自定义头像/简介保护规则。
