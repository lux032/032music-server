# Design

## Context

参见 `proposal.md` 的背景说明。当前 `032music-server` 的艺术家身份与展示体系具有以下直接约束设计方案的架构事实：

1. **展示名读取面广泛耦合 `COALESCE(user_display_name, display_name)`**：全库 38 个存储文件共 113 处查询（包含列表分页、名称排序索引 `idx_artists_display_name`、`albumCreditDisplaySQL`/`trackCreditDisplaySQL`、统一搜索、Subsonic/OpenSubsonic API 与客户端增量同步）均直接读取 `COALESCE(user_display_name, display_name)`；且 `artists.display_name` 仅在首次扫描插入时写入，重扫绝不覆盖；`artists.identity_key` 是扫描期的去重定位键。
2. **合并审计与重扫直连基础设施已完备**：`mergeArtistsTx`（`internal/storage/identity.go`）已完整实现 `album_artists`/`track_artists` 搬迁、源名与别名迁入 `artist_names(name_type='merged_alias')`、`artists.merged_into_artist_id` 永久指针（重扫经 `canonicalArtistID` 自动直连）、收藏继承、主唱修正重放与 `RollbackArtistMerge` 全量回滚；`artistIdentityConflict`（`internal/storage/artist_identity_conflict.go`）已封装非复合署名与 `requires_manual_review=0` 守卫。
3. **MusicBrainz 外部资料已缓存本地**：`artist_external_profiles` 已持久化每个已确认艺人的 `display_name`、`country`、`aliases_json` 与 `raw_json`，但现有 `mbArtistResponse`/`mbSearchResponse` 解析丢弃了官方别名的 `locale`/`primary`/`type`/`ended` 字段。
4. **身份证据角色遗漏 `album_artist`**：迁移 `074`/`075`/`077` 为 `audio_file_artist_identities` 与 `audio_file_identity_checks` 设定了仅含 `('primary','composer','lyricist','arranger','producer')` 的 `CHECK` 约束；`importTrackIntoAlbum` 与 `artist_tag_identity.go` 均未采集 `MUSICBRAINZ_ALBUMARTISTID`。
5. **AI 审核框架已具备分级自治与快照回滚模型**：`ai_review_settings` 按模块拆分 `*_mode`（`off | recommend | auto`），`ai_review_decisions` 支持 `applied_snapshot_json` 原子回滚，仅需解绑 `ConflictOwnerID != 0` 的硬拦截并打通与 `artist_merge_operations` 的双向回滚同步。

## Goals / Non-Goals

**Goals:**
- 通过单一低侵入存储方案实现 MusicBrainz 原文名规范化，使所有 Web UI、曲目/专辑署名拼接、统一搜索、排序索引与客户端 API 自动生效，且完全可逆、绝不覆写 `user_display_name`。
- 补齐 `album_artist` 角色 MBID 采集（同时兼容存量已扫描音频文件的安全只读推断），消除仅作为专辑艺人时的身份盲区。
- 打通“确定性同 MBID 自动合并（L2）→ AI 高置信冲突合并（L3）→ 存量一键收敛”全链路，保证每一次跨名称合并均经 `mergeArtistsTx` 落入 `artist_merge_operations` 可审计、可撤销，并对人工撤销过的艺人对建立永久防反复合并记忆。
- 升级启动后实现存量已确认艺人的零网络毫秒级原名收敛，同时将存量历史重复合并交由管理员在 `/admin/matches` 显式一键触发（方案 A）。

**Non-Goals:**
- **不修改 `MusicAutoTagger`**：上游刮削器的地区排序与标签字段对齐留待后续独立变更。
- **不在扫描期跳过 `mergeArtistsTx` 直接改写跨名归属**：首次见到新罗马音拼写时仍先建行再交由后台自动匹配合并（选项 1），以保证 100% 合并操作可追溯、可一键撤销；合并一次后后续重扫自动由既有 `merged_into_artist_id` + `canonicalArtistID` 零分裂直连。
- **不新增外部网络依赖或强制联网回刷全库旧艺人**：启动期存量原文名收敛仅消费本地 SQLite 已缓存的 `artist_external_profiles`，不对全库批量请求 MusicBrainz API。

## Decisions

### Decision 1: 规范展示名落库方式 —— `artists.original_display_name` + 原地更新 `display_name`

- **方案**：在 `artists` 表新增可空列 `original_display_name TEXT`；`identity_key` 保持创建时的原始规范化键永不修改；`user_display_name` 保持人工最高优先级永不自动修改。
  - 当艺人首次触发规范展示名改写（如 `Yuki Kajiura` → `梶浦由記`）时：
    1. 将原 `display_name` 备份至 `original_display_name`（`COALESCE(original_display_name, display_name)`）；
    2. 更新 `display_name = canonicalName`、`sort_name = metadata.Normalize(canonicalName)`、`updated_at = now`；
    3. 向 `artist_names(artist_id, value, name_type)` 插入 `(artistID, originalName, 'original_tag')`（`ON CONFLICT DO NOTHING`），保证统一搜索（`search_aliases.go`）与艺人详情别名展示始终可检索原罗马音。
  - 当解除 MusicBrainz 身份（`ResetArtistIdentity`）或撤销未合并的 AI 艺人绑定（`RevertAIReviewDecision`）时：
    - 若 `original_display_name IS NOT NULL`，恢复 `display_name = original_display_name`、`sort_name = metadata.Normalize(original_display_name)`、`original_display_name = NULL`，并删除该艺人 `name_type = 'original_tag'` 的自动别名行。
- **权衡与备选方案**：
  - *备选方案*：新增 `canonical_display_name` 列并将全库 113 处 `COALESCE(user_display_name, display_name)` 改写为三元 `COALESCE(user_display_name, canonical_display_name, display_name)` 并重建排序索引。
  - *为何不选备选*：改动面跨越 38 个存储文件及多个索引表达式，极易在客户端同步、署名拼接或搜索排序中遗漏；而原地更新 `display_name` + `original_display_name` 备份自动覆盖全部 113 处读取点、`albumCreditDisplaySQL`/`trackCreditDisplaySQL` 与 `idx_artists_display_name`，同时通过不可变 `identity_key` 保证重扫幂等。
- **匹配输入一致性保障**：`ArtistForMatching` 在该艺人尚未确认 MB 身份前 `original_display_name` 恒为 `NULL`；确认 MB 身份后，Last.fm 优先按 `LastFMQueryMBID` 查询；若 MB 身份被重置，`display_name` 在重置事务内即刻恢复为 `original_display_name`，因此匹配键与名称核验始终保持一致。

### Decision 2: 策略 2A 东亚原文名解析规则与结构化别名持久化

- **结构化别名持久化**：扩展 `enrichment/manager.go` 的 `mbArtistResponse` 与 `mbSearchResponse`，解码 `Aliases` 中的 `Name`、`Locale`、`Primary`、`Type`、`Ended` 字段，写入候选/资料 `payload_json`/`raw_json` 的 `structuredAliases` 字段（同时保留原 `aliases` 字符串数组向后兼容）。
- **纯函数判定逻辑 `ResolveCanonicalNativeName(localName string, profile ExternalArtistProfile) (string, bool)`**：
  1. 仅当 `profile.Source == "musicbrainz"` 且 `localName` 为纯拉丁文本（包含拉丁字母且不含任何非拉丁原语种字符，如 CJK 汉字、平假名、片假名、韩文谚文）时参与判定；本地已含非拉丁字符的名称直接保持不变。
  2. **第一优先级（MB 原文主名）**：若 `profile.DisplayName` 包含非拉丁原语种文字（`unicode.Han` / `unicode.Hiragana` / `unicode.Katakana` / `unicode.Hangul`），且非空、与 `localName` 不同，直接返回 `profile.DisplayName`（覆盖日文 `梶浦由記`/`宇多田ヒカル`、中文 `周杰倫`、韩文原名等）。
  3. **第二优先级（日区主官方艺名别名兜底）**：仅当 `strings.EqualFold(profile.Country, "JP")` 且 `profile.DisplayName` 为纯拉丁时，遍历 `structuredAliases`，选取满足 `Primary == true`、`!Ended`、`Locale` 为 `ja` 或以 `ja_` / `ja-` 开头、`Type` 不属于 `Legal name` / `Search hint`、且名称包含非拉丁原语种文字的首条别名。
     - **关键防误改**：显式排除 `Legal name` 与 `Search hint`，确保 `LiSA`（含法律本名别名 `織部里沙`）、`Aimer`、`YOASOBI`、`milet` 等官方拉丁艺名绝不会被误改为歌手本名。
- **生效触发点与有效资料继承**：在 `upsertExternalArtistProfile`、`ConfirmArtistCandidate`、`ConfirmArtistCandidateByAI`、`bindArtistCandidate`（绑定或自动合并后）、手动/批量合并完成后以及启动期存量收敛统一调用事务内 helper `applyCanonicalDisplayNameTx(ctx, tx, canonicalArtistID)`。该 helper 读取规范艺人的**有效** MusicBrainz 资料（自身优先，其次递归合并来源艺人的 MB 资料），从而连此前手工反向合并（日文并入罗马音）的历史艺人也能自动纠正。
- **配置存储**：新增单例表 `artist_identity_settings(id CHECK(id=1), canonical_native_name_enabled INTEGER NOT NULL DEFAULT 1, updated_at TEXT)`，遵循既有 `biography_settings`/`ai_review_settings`/`artist_image_settings` 约定，在 `/admin/settings/metadata` 展示并独立保存（`scope="artist_identity"`）。

### Decision 3: `album_artist` MBID 角色化采集与存量零重扫兼容

- **元数据层**：在 `internal/metadata/artist_credits.go` 新增 `RawAlbumArtistNamesForIdentity(raw, fallback, allowFallback)`，对称复用 `RawPrimaryArtistNamesForIdentity` 的严格位置对齐与多 MBID 切分恢复规则（依次检查 `ALBUMARTISTS`/`ALBUM_ARTISTS`/`ALBUM ARTISTS` 与 `ALBUMARTIST`/`ALBUM ARTIST`/`TPE2`/`TP2`/`AART`，绝不回退到曲目 `ARTIST`）。
- **数据库迁移**：迁移 `095` 重建 `audio_file_artist_identities` 与 `audio_file_identity_checks` 表，将 `role CHECK` 约束扩展为 `('primary', 'album_artist', 'composer', 'lyricist', 'arranger', 'producer')`（保留全部已有行与主键/外键约束）。
- **导入流程**：`upsertScannedAlbum` 返回解析后的 `albumArtistIDs`（或 `importTrackIntoAlbum` 复用同一批缓存计算结果），在 `importTrackIntoAlbum` 内调用 `recordRoleIdentities("album_artist", ...)` 持久化该文件的专辑艺人 MBID 证据与检查状态。
- **存量库零重扫支持**：在 `artist_tag_identity.go` 中：
  - 将 `album_artist` 的标签键（`MUSICBRAINZ_ALBUMARTISTID`、`MUSICBRAINZ ALBUM ARTIST ID` 及其 iTunes reverse-DNS 变体）加入 `roleTagExistsPred`；
  - 新增受严格守卫的单专辑艺人 legacy 推断分支：对于尚未存在 `album_artist` 检查记录的存量音频文件，若其所属专辑仅关联 1 位 `album_artists`、不存在已生效的专辑艺人合并迁移记录、且该文件 `audio_file_tags` 含唯一合法 36 位 UUID 的 `MUSICBRAINZ_ALBUMARTISTID` 标签，即可直接提供 `TaggedMBID`，无需管理员为历史曲库重跑全量磁盘扫描。

### Decision 4: 确定性同 MBID 自动合并（L2）、本地资料零联网复用与对称回滚防复合记忆

- **同 MBID 本地资料快速通道**：在 `store` 新增 `ExternalArtistProfileByID(ctx, source, externalID)`；在 `enrichment/manager.go` 的 `TaggedMBID != ""` 分支中，先查询本地 `artist_external_profiles` 是否已缓存该 `musicbrainz` 资料：
  - 若命中本地资料且 `artistProfileNameMatches(artist.Name, localProfile)` 为真，直接复用本地资料构造 `Score = 100` 候选并跳过对 MusicBrainz HTTP API 的重复请求（亦跳过多余的 Last.fm 独立拉取或仅保留必要流程，避免触发 1 req/s 限流）；
  - 若本地无该 MBID 资料，或本地缓存别名尚未收录当前拼写（`artistProfileNameMatches` 为假），自动回退发起一次实时的 `musicBrainzLookup` 核实最新官方别名，确保不会因本地旧缓存误判错标。
- **审计来源标注**：为 `artist_merge_operations` 新增列 `trigger_source TEXT NOT NULL DEFAULT 'manual' CHECK (trigger_source IN ('manual', 'auto_match', 'ai_review', 'backfill'))`，`/admin/merges` 展示明确徽章区分人工合并、自动匹配合并、AI 合并与存量一键收敛。
- **对称回滚记忆查询**：新增 `hasRolledBackArtistMergePairTx(ctx, tx, a, b)`，查询：
  ```sql
  SELECT EXISTS(
      SELECT 1 FROM artist_merge_operations
      WHERE status = 'rolled_back'
        AND ((source_artist_id = ? AND target_artist_id = ?)
          OR (source_artist_id = ? AND target_artist_id = ?))
  )
  ```
  任何自动合并路径（L2、L3、一键收敛）执行前均必须校验返回 `false`。
- **事务内自动合并**：在 `bindArtistCandidate` 中，当 `artistIdentityOwnerCheck` 返回 `*ArtistExternalIDConflictError` 时：
  - 仅在 `!refreshOnly`、`status == "candidate"`、`manualOnly == 0`、`p.Source == "musicbrainz"`（或 Last.fm 候选已被 MBID 独立佐证且 `score >= 92`）时进入自动合并判定；
  - 解析 `targetID := canonicalArtistID(ctx, tx, conflict.OwnerArtistID)`；
  - 校验 `targetID != artistID`、源未合并、双方均非复合署名（`isCompositeArtistTx`）、且 `!hasRolledBackArtistMergePairTx(artistID, targetID)`；
  - 调用 `mergeArtistsWithTriggerTx(ctx, tx, artistID, targetID, "auto_match")`；
  - 将源艺人该 source 的待审候选置为 `confirmed`/`rejected`（避免源艺人残留待审状态干扰计数），若 `checkpoint != nil` 则置 `artist_match_run_items.matched_fact = 1`；
  - 对 `targetID` 执行 `applyCanonicalDisplayNameTx(ctx, tx, targetID)`；
  - 返回 `BindOutcome`（含 `Merged: true`、`TargetArtistID`、`MergeOperationID`），使 `enrichment/manager.go` 跳过对已合并源艺人的头像/简介拉取，转而确保 `targetID` 的头像/简介与自动队列正常衔接。

### Decision 5: 无 MBID 艺人的 AI 冲突合并（L3）与双向一致回滚

- **设置扩展**：迁移 `095` 重建 `ai_review_settings` 新增 `artist_merge_mode TEXT NOT NULL DEFAULT 'auto' CHECK (artist_merge_mode IN ('off', 'recommend', 'auto'))`，在 `/admin/ai-settings` 与 `setup_wizard` 暴露配置。
- **AI 上下文与提示词增强**：
  - `ArtistCandidateAIView` 新增 `ConflictOwnerName` 与 `ConflictOwnerAliases`（来自 `artist_names` 与外部资料），让模型在看到 `conflictOwnerId > 0` 时能直接对照库内已绑定艺人的名称与别名（如本地 `Yuki Kajiura` 对照库内 `梶浦由記`）。
  - 更新 `artistReviewPrompt` 规则 3：保持 `needsSplit=true` 禁止选中的约束，但对 `conflictOwnerId > 0` 改为要求模型谨慎核对双方是否为同一艺人的不同语种/别名；确为同一人时返回 `decision="select"` 并在 `reason` 中说明应合并至该已有艺人，否则返回 `uncertain` 或 `reject_all`。
  - 同步刷新 `LoadArtistAIContext` 的 `Fingerprint` 版本前缀，使既有被旧规则打成 `uncertain` 的存量冲突艺人在新一轮 AI 审核中得以重新评估。
- **分层自治执行**：
  - 新增常量 `autoThresholdArtistMerge = 92`。
  - 在 `evalArtist` 中，若选中的候选存在 `ConflictOwnerID != 0`：
    - 读取有效 `mergeMode`（`!rev.Enabled` 时视为 `off`）；
    - 若 `mergeMode == "off"`、`info.NeedsSplit`、`selectedCand.RequiresManualReview` 或 `HasRolledBackArtistMergePair(artistID, targetID)` 为真，强制降级为 `AIActionUncertain`；
    - 否则若 `mergeMode == "auto"` 且 `out.Confidence >= 92` 且（未联网搜索或 `quoteGroundedInPages` 通过），调用 `MergeArtistConflictByAI(ctx, artistID, selectedCand.ID, targetID)`：
      - 在单事务内复验 `artistIdentityConflict.CanMerge` 与 `!hasRolledBackArtistMergePairTx`；
      - 执行 `mergeArtistsWithTriggerTx(ctx, tx, artistID, targetID, "ai_review")`；
      - 更新源艺人候选状态并对 `targetID` 调用 `applyCanonicalDisplayNameTx`；
      - 生成 `aiArtistConfirmSnapshot{CandidateID, Source, ExternalID, RejectedCandidateIDs, Merged: true, MergeOperationID, TargetArtistID}` 存入 `ai_review_decisions`（`action = 'auto_confirm'`, `applied = 1`）。
    - 否则保持 `AIActionRecommendAccept`（`applied = 0`），并在 `payload_json` 记录 `conflictMerge: true`、`targetArtistId`、`targetArtistName`，供 `/admin/matches` 展示「AI 建议合并至 <目标名>」一键确认按钮。
- **双向一致回滚**：
  - 抽出事务内公共函数 `rollbackArtistMergeTx(ctx, tx, operationID)`；
  - `RevertAIReviewDecision` 处理 `AITargetArtistMatch` 时：若 `snap.Merged && snap.MergeOperationID > 0`，检测该合并记录是否仍为 `merged`，若是则调用 `rollbackArtistMergeTx` 回滚合并（若已先被 `/admin/merges` 撤销则幂等跳过合并回滚，继续恢复候选与置 `reverted=1`）；若 `!snap.Merged` 则删除外部资料并在 `snap.Source == "musicbrainz"` 时触发原始名恢复。
  - `RollbackArtistMerge` 在调用 `rollbackArtistMergeTx` 后，同步将关联该 `operationID` 的未撤销 `ai_review_decisions` 记录标记为 `reverted = 1`。

### Decision 6: 存量数据收敛实现（方案 A）

- **启动期静默原文名统一**：在 `Store` 启动收敛入口（与 `cleanTrackTagControlChars` 同层）调用 `ConvergeCanonicalArtistNames(ctx)`：
  - 若 `canonical_native_name_enabled == 0` 直接返回；
  - 查询所有 `merged_into_artist_id IS NULL AND user_display_name IS NULL` 且自身或合并子行拥有 `musicbrainz` 资料的艺人，对纯拉丁 `display_name` 在单批/分批事务中调用 `applyCanonicalDisplayNameTx`，记录统计日志（转换数量）；全程纯本地 SQL、幂等可重复运行。
- **存量同 MBID 重复艺人一键收敛**：
  - 新增 `CountConvergeableArtistConflicts(ctx)` 与 `ConvergeDeterministicArtistConflicts(ctx)`：
    - 候选来源覆盖两类存量冲突：(a) 待审 `artist_match_candidates` 中 `source='musicbrainz'`、`status='candidate'`、`score >= 92`、`requires_manual_review=0` 且 `external_id` 已被其他规范艺人持有者；(b) 无 MB 绑定但 `artistTaggedMBIDs` 返回的 MBID 已被库内另一规范艺人持有、且本地名通过 `artistProfileNameMatches(localName, ownerMBProfile)` 核验者；
    - 逐条校验非复合署名与 `!hasRolledBackArtistMergePairTx`，通过 `mergeArtistsWithTriggerTx(..., "backfill")` 合并并执行 `applyCanonicalDisplayNameTx`，返回 `ConflictConvergenceResult{Merged, Skipped, Failed}`。
  - HTTP 端新增 `POST /admin/matches/converge-conflicts`（带 Admin + CSRF 保护），并在 `/admin/matches` 顶部任务工具栏展示可收敛数量与「一键收敛同 MBID 重复艺人」按钮，执行完成后跳转并提示合并/跳过条数及 `/admin/merges` 核对链接。

## Risks / Trade-offs

- **[Risk] 存量日区艺人 MB 主名为拉丁（但本地 `raw_json` 尚未保存 `structuredAliases`）时，启动期离线收敛无法一步改为日文别名** → **Mitigation**：覆盖 95%+ 主流东亚艺人（MB 主名本身即为日/中/韩原文）；剩余极少数日区拉丁主名艺人在后续任何一次资料刷新、重新匹配或别名补全更新 MB 资料时即可自动获得 `structuredAliases` 并完成规范化，避免在服务启动时向 MusicBrainz 发起全库限流请求。
- **[Risk] 自动改写 `display_name` 后，依赖名称哈希或精确名称匹配的既有测试/逻辑受影响** → **Mitigation**：仅对 `user_display_name IS NULL` 且纯拉丁名改写；`identity_key` 保持不可变保障重扫幂等；`original_display_name` 保留原值支持 `ResetArtistIdentity` 完整复原；新增专门单元测试覆盖重扫、搜索、署名展示、重置与客户端同步回归。
- **[Risk] 自动合并并发触发或链路合并（A→B 后又出现 C→A）导致指向失效** → **Mitigation**：所有合并入口均在单 SQLite 事务内通过 `canonicalArtistID` 实时解析最终目标 ID 并复用 `withBusyRetry`；`artist_merge_operations` 与外键约束保证同一源艺人不会被重复合并。
- **[Risk] AI 在 `auto` 模式下将同名但不同地区/不同人的歌手误判为冲突合并** → **Mitigation**：自动合并门槛设为 `>= 92`（高于普通绑定 90）；AI 上下文强制传入目标艺人的名称、别名、代表专辑与曲目；严格拦截 `NeedsSplit` 与 `RequiresManualReview`；提供独立 `artist_merge_mode`（`off`/`recommend`/`auto`）且合并历史与 AI 决策均可一键回滚并永久阻止再次自动合并该对艺人。
- **[Risk] 重建 `audio_file_artist_identities` / `audio_file_identity_checks` 迁移在大库（数万文件）上耗时** → **Mitigation**：表结构紧凑且为索引覆盖的整数/短文本列，在 SQLite 事务内采用 `CREATE TABLE ..._new` → `INSERT INTO ... SELECT` → `DROP`/`RENAME`（与迁移 `074`/`088` 一致），预计数十至数百毫秒完成，且单事务保证原子性。

## Migration Plan

1. **数据库迁移 `095_artist_canonical_naming_and_auto_merge.sql`**：
   - `ALTER TABLE artists ADD COLUMN original_display_name TEXT;`
   - `ALTER TABLE artist_merge_operations ADD COLUMN trigger_source TEXT NOT NULL DEFAULT 'manual' CHECK (trigger_source IN ('manual', 'auto_match', 'ai_review', 'backfill'));`
   - 新建 `artist_identity_settings` 单例表并初始化默认行（`canonical_native_name_enabled = 1`）。
   - 重建 `audio_file_artist_identities` 与 `audio_file_identity_checks`，在 `role CHECK` 中加入 `'album_artist'`，完整迁移原有数据。
   - 重建 `ai_review_settings`，新增 `artist_merge_mode TEXT NOT NULL DEFAULT 'auto' CHECK (artist_merge_mode IN ('off', 'recommend', 'auto'))`，完整保留原有设置值。
2. **启动期幂等收敛**：迁移完成后执行 `ConvergeCanonicalArtistNames(ctx)`，自动静默统一存量已确认罗马音艺人的展示名。
3. **部署后管理员操作（方案 A）**：管理员可在 `/admin/matches` 查看「可收敛的同 MBID 重复艺人」计数，按需点击「一键收敛同 MBID 重复艺人」清理历史同编号重复艺人，并可按需触发 AI 审核清理无 MBID 历史重复艺人。
4. **回滚策略**：
   - 任何单次自动合并（无论 `auto_match`、`ai_review` 或 `backfill`）均可在 `/admin/merges`（或 AI 审核页）一键撤销，且撤销后自动写入防反复合并记忆；
   - 若管理员不想继续自动统一展示名，可在 `/admin/settings/metadata` 关闭「自动统一为 MusicBrainz 原文名」开关；已改名的单个艺人也可随时手动设置 `user_display_name` 或重置 MB 身份恢复 `original_display_name`。
