# Tasks

## 1. 数据库迁移与配置存储

- [x] 1.1 新增迁移 `internal/storage/migrations/095_artist_canonical_naming_and_auto_merge.sql`（增加 `artists.original_display_name`、`artist_merge_operations.trigger_source`、`artist_identity_settings` 单例表，重建 `audio_file_artist_identities` / `audio_file_identity_checks` 扩展 `album_artist` 角色约束，重建 `ai_review_settings` 增加 `artist_merge_mode` 列），并编写迁移单元测试验证升级幂等与旧数据无损保留（运行 `go test ./internal/storage/... -run TestMigration095` 通过）。
- [x] 1.2 在 `internal/storage` 实现 `ArtistIdentitySettings` 读写方法（默认 `canonical_native_name_enabled=true`）并扩展 `AIReviewSettings` 读写与校验逻辑支持 `ArtistMergeMode`（`off`/`recommend`/`auto`，默认 `auto`），补充单元测试验证默认值持久化与非法枚举值拒绝（运行 `go test ./internal/storage/... -run 'TestArtistIdentitySettings|TestAIReviewSettings'` 通过）。

## 2. 专辑艺人（`album_artist`）MBID 证据采集

- [x] 2.1 在 `internal/metadata/artist_credits.go` 新增 `RawAlbumArtistNamesForIdentity`，严格校验 `ALBUMARTISTS` / `ALBUMARTIST` / `TPE2` 槽位与 `MUSICBRAINZ_ALBUMARTISTID` 位置对齐（支持多 MBID 切分恢复且绝不回退至曲目 `ARTIST`），补充单元测试覆盖单人、多人、复合署名及不对齐拦截（运行 `go test ./internal/metadata/... -run TestRawAlbumArtistNamesForIdentity` 通过）。
- [x] 2.2 在 `internal/storage/library.go` 与 `internal/storage/artist_tag_identity.go` 将 `album_artist` 接入扫描身份证据落库与 `artistTaggedMBIDs`（含存量单专辑艺人音频文件的安全 legacy 推断），编写单元测试验证仅作为专辑艺人时可产出 `TaggedMBID`、数量/UUID 异常时阻断该角色且不污染其他角色（运行 `go test ./internal/storage/... -run TestAlbumArtistTaggedIdentity` 通过）。

## 3. MusicBrainz 结构化别名解析与原文规范展示名（策略 2A）

- [x] 3.1 扩展 `internal/enrichment/manager.go` 的 `mbArtistResponse` 与 `mbSearchResponse` 解析，保留官方别名的 `name`/`locale`/`primary`/`type`/`ended` 结构化字段并持久化至 `payload_json`/`raw_json`（兼容既有 `aliases` 字符串数组），编写单元测试验证结构化别名解析与序列化（运行 `go test ./internal/enrichment/... -run TestMusicBrainzStructuredAliases` 通过）。
- [x] 3.2 新建 `internal/storage/artist_canonical_name.go` 实现 `ResolveCanonicalNativeName`、`applyCanonicalDisplayNameTx`、`restoreCanonicalDisplayNameTx` 与 `ConvergeCanonicalArtistNames`，接入所有 MusicBrainz 身份确认路径（自动绑定、人工确认、AI 确认、合并目标艺人收敛）、`ResetArtistIdentity` 恢复路径以及服务启动期本地静默收敛流程，并补充单元测试覆盖东亚原文名统一、日区 `locale=ja & primary=true` 官方艺名别名、`YOASOBI`/`LiSA`/`Taylor Swift` 保持拉丁名（排除 `Legal name`/`Search hint`）、`user_display_name` 最高优先、原罗马音别名搜索与重扫稳定、身份重置复原及启动期零网络收敛（运行 `go test ./internal/storage/... -run TestArtistCanonicalDisplayName` 通过）。

## 4. 确定性同 MBID 冲突自动合并、本地资料零联网复用与回滚防复合护栏

- [x] 4.1 扩展 `internal/storage/identity.go` 与 `internal/storage/artist_identity_conflict.go`：实现 `ExternalArtistProfileByID`、带 `trigger_source` 的 `mergeArtistsWithTriggerTx`、抽出事务内 `rollbackArtistMergeTx` 与对称回滚记忆查询 `hasRolledBackArtistMergePairTx`，并在 `bindArtistCandidate` 遇到同 MBID 冲突且满足高置信、非人工核对、非复合署名与无历史回滚记录时原子执行自动合并与目标艺人规范命名；更新 `internal/enrichment/manager.go` 与 `artist_runtime.go` 在 `TaggedMBID` 命中本地已缓存 MB 资料且 `artistProfileNameMatches` 通过时直接免联网复用本地资料（未命中别名或本地缺失时回退实时 MB 查询），并正确处理合并结果将后续头像/简介补全指向存活目标艺人，编写单元测试覆盖本地资料零联网复用合并、本地别名未命中回退联网、`TaggedMBID`/双源一致自动合并、错标 `requires_manual_review` 拦截、复合署名拦截及撤销后不再自动复合（运行 `go test ./internal/storage/... ./internal/enrichment/... -run 'TestAutoMergeArtistIdentityConflict|TestRolledBackMergeSuppression|TestTaggedMBIDLocalProfileFastPath'` 通过）。

## 5. 无 MBID 艺人的 AI 冲突合并分级自治与双向回滚

- [x] 5.1 扩展 `internal/storage/ai_review.go`（为 `ArtistCandidateAIView` 补充 `ConflictOwnerName` 与 `ConflictOwnerAliases` 并升级指纹版本以重评历史 `uncertain` 冲突）与 `internal/storage/ai_review_confirm.go`（新增 `MergeArtistConflictByAI`、扩展 `aiArtistConfirmSnapshot` 记录合并操作与目标艺人 ID，并实现 `RevertAIReviewDecision` 与 `RollbackArtistMerge` 双向状态同步及非合并 MB 确认撤销时的原始名恢复），编写单元测试验证原子合并与双向回滚一致性（运行 `go test ./internal/storage/... -run TestAIArtistConflictMergeAndRevert` 通过）。
- [x] 5.2 更新 `internal/aireview/prompts.go` 与 `internal/aireview/service.go` 解除对非复合 `ConflictOwnerID > 0` 候选的硬拦截，按 `ArtistMergeMode`（`off`/`recommend`/`auto`）、高置信门槛 `>= 92`、联网引文校验及回滚记忆护栏执行自动合并或产出带目标艺人信息的合并建议，编写单元测试覆盖三种策略模式、低置信降级、联网引文校验与撤销记忆跳过（运行 `go test ./internal/aireview/... -run TestEvalArtistConflictMerge` 通过）。

## 6. 存量同 MBID 一键收敛与管理后台交互

- [x] 6.1 在 `internal/storage/artist_identity_conflict.go` 实现 `CountConvergeableArtistConflicts` 与 `ConvergeDeterministicArtistConflicts`（覆盖待审高置信冲突候选与 `TaggedMBID` 命中已绑定规范艺人且名称校验通过的存量重复艺人，严格执行非复合与回滚记忆守卫，以 `trigger_source='backfill'` 写入合并历史并触发规范命名），编写单元测试验证批量收敛与安全跳过（运行 `go test ./internal/storage/... -run TestConvergeDeterministicArtistConflicts` 通过）。
- [x] 6.2 更新管理后台 HTTP 路由与页面模板：在 `/admin/settings/metadata`（`internal/http/identity.go`、`metadata-settings.html`）增加「自动统一为 MusicBrainz 原文名」开关；在 `/admin/ai-settings`（`internal/http/ai_settings.go`、`ai-settings.html`、`setup_wizard.go`）增加 `artist_merge_mode` 配置项；在 `/admin/matches`（`internal/http/identity.go`、`app.go`、`match-review.html`）展示可收敛重复艺人计数、AI 冲突合并建议及 `POST /admin/matches/converge-conflicts` 一键收敛按钮；在 `/admin/merges`（`merge-history.html`）展示合并触发来源标识；编写 HTTP 测试覆盖设置保存、CSRF 保护、一键收敛接口与模板渲染（运行 `go test ./internal/http/... -run 'TestMetadataCanonicalNamingSetting|TestAIArtistMergeModeSetting|TestConvergeArtistConflictsHandler'` 通过）。

## 7. 跨模块集成与回归验证

- [x] 7.1 编写跨模块端到端回归测试覆盖“罗马音与原文音轨先后入库 → 自动匹配合并为唯一规范艺人 → 展示名统一为 MB 原文且原罗马音可搜索 → 重扫稳定复用 → 手动撤销后不再自动复合”，并执行 `go vet ./...` 与 `go test ./internal/metadata/... ./internal/storage/... ./internal/enrichment/... ./internal/aireview/... ./internal/http/...` 确认全部通过。
