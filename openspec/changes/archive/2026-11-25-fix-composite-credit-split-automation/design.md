# Design: 复合署名全角色拆分修复与 AI 分级自治优化

## Context

当前复合署名治理存在以下核心约束与现状（问题背景详见 `proposal.md`）：

1. **展示/任务口径 vs 执行口径不一致**：
   - `PendingArtistReviewSplitPages`、`PendingCreditSplitArtistIDs` 与 `hydrateArtistReviewItems` 统计任意 `track_artists` 角色与 `album_artists`。
   - `ApplyCompositeCreditSplit` 仅查询 `track_artists WHERE artist_id=? AND role='primary'` 并逐条调用 `SetPrimaryArtistCorrection`，导致所有纯幕后（`composer`/`lyricist`/`arranger`/`producer`/`instrument:*`）与 `album_artists` 复合署名点击后“更新 0 首曲目”且无法退出队列。
2. **重扫与触发器约束**：
   - `ImportTrack` / `upsertScannedAlbum` 重建 `album_artists` 与 `track_artists`；只有 `role='primary'` 通过 `track_primary_artist_state` 在重扫后重放。
   - `012_phase4_enrichment.sql` 的 `restore_sourced_track_artist_after_delete` 触发器会在删除 `track_artists` 时自动恢复 `track_artist_sources(source<>'file_tag')` 中的行。
   - `missing.go` 的 `cleanupOrphansSQL` 会清理无 `album_artists`/`track_artists`/`track_primary_artist_base` 且不在 `protected_json_artists` 中的艺人行。
3. **AI 审核安全基线**：
   - `ai_review_settings.credit_mode` 带有 `CHECK (credit_mode IN ('off', 'recommend'))`，`RevertAIReviewDecision` 尚未支持 `AITargetCreditSplit`。
   - `metadata.CompositeArtistCredit` 在存储与匹配层 11 处直接基于字符串分隔符判断，无白名单豁免机制。

## Goals / Non-Goals

**Goals:**
- 单次拆分原子覆盖 `track_artists` 全部角色、`track_artist_sources` 与 `album_artists`，且经过 `canonicalArtistID` 归一化去重。
- 拆分结果在增量/全库重扫与孤儿清理后持久保持，不回退为原始复合标签。
- 开放 `credit_mode = 'auto'`（阈值 ≥92，允许自动创建新单人艺人），完整支持 AI 自动拆分的一键撤销与存量高置信建议免重评落库。
- 为真实单一组合（带 `&`、`,` 等标点）提供持久化豁免，放行 MusicBrainz / Last.fm 匹配。
- 在 `/admin/matches?tab=credits` 提供角色分布展示、单条人名微调、单一艺人确认与批量应用高置信建议交互。

**Non-Goals:**
- 不直接修改本地音频文件内嵌标签（延续本系统“源音乐文件只读，数据库层持久化覆盖”原则）。
- 不修改 `internal/metadata` 文件解析阶段对多 MBID 对齐的严格安全校验（`RawPrimaryArtistNamesForIdentity` / `RawCreatorNamesForIdentity` 保持保守）。
- 不在此次变更中对「AI 判定非复合署名」自动写入豁免（延续安全铁律：AI 不自动落库豁免/永久排除，由人工在卡片一键确认）。

## Decisions

### 1. 数据模型与 Migration 088 (`088_composite_credit_splits.sql`)

**决策**：新增两张轻量持久化表，并重建 `ai_review_settings` 表以放宽 `credit_mode` CHECK 约束。

```sql
-- 1. 重建 ai_review_settings，将 credit_mode CHECK 放宽为 ('off', 'recommend', 'auto')
--    (保留全部既有行数据与列定义，默认仍为 'recommend' 保持兼容)

-- 2. 持久化复合署名拆分规则与撤销快照
CREATE TABLE artist_credit_splits (
    composite_artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
    part_ids_json       TEXT NOT NULL CHECK (json_valid(part_ids_json)),
    part_names_json     TEXT NOT NULL CHECK (json_valid(part_names_json)),
    status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'reverted')),
    source              TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'ai')),
    decision_id         INTEGER REFERENCES ai_review_decisions(id) ON DELETE SET NULL,
    snapshot_json       TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(snapshot_json)),
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
CREATE INDEX idx_artist_credit_splits_status ON artist_credit_splits(status, composite_artist_id);
CREATE INDEX idx_artist_credit_splits_decision ON artist_credit_splits(decision_id);

-- 3. 单一艺人（非复合署名）误报豁免表
CREATE TABLE artist_composite_exemptions (
    artist_id  INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
    reason     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;
```

**替代方案与取舍**：
- *替代方案*：为 `composer`/`lyricist`/`arranger`/`producer` 与 `album_artists` 各自复制一套类似 `track_primary_artist_state` 的 per-track revision 状态表。
- *未采用原因*：曲目级多角色表数量膨胀、跨表同步复杂；而复合署名拆分天然是**艺人级映射**（`composite_artist_id -> part_ids`），一张艺人级规则表即可同时解决重扫重放、未来新入库同署名自动展开、以及统一回滚快照。

### 2. 全角色拆分事务与触发器安全顺序

**决策**：将 `ApplyCompositeCreditSplit` 扩展为返回结构化结果 `CreditSplitResult{TracksUpdated, AlbumsUpdated, RolesUpdated int}` 及回滚快照 `aiCreditSplitSnapshot`，在单一写事务内按严格顺序执行：

```text
BeginWriteTx
  |
  +-> 1. 清洗 partNames -> ensureArtists -> canonicalArtistID 归一化去重
  |      校验: len(partIDs) >= 1 (AI auto 要求 >= 2), 且不含 compositeArtistID
  |
  +-> 2. 采集全量受影响关联 + 构造 rollback snapshot:
  |      - primaryTracks:  track_artists WHERE artist_id=? AND role='primary'
  |        (记录每首曲目的 pre-split track_primary_artist_state: active/revision/artists_json)
  |      - nonPrimaryRows: track_artists WHERE artist_id=? AND role<>'primary'
  |        (按 (track_id, role) 记录原始完整艺人列表与 join_phrase)
  |      - sourceRows:     track_artist_sources WHERE artist_id=?
  |        (记录原始 source/external_id/run_id/position/role/join_phrase)
  |      - albumRows:      album_artists WHERE artist_id=?
  |        (按 album_id 记录原始完整艺人列表与 join_phrase)
  |      若四类全部为空 -> 清理残留 candidates/_composite 状态后返回 ErrNoCompositeReferences
  |
  +-> 3. 写入变更（严格顺序）:
  |      a) Primary 曲目: 复用 PrimaryArtistCorrection 内部事务 helper，将 compositeArtistID
  |         原地替换为 partIDs 并去重，推进 revision 并写入 track_primary_artist_operations
  |      b) track_artist_sources: 先删旧行再按原 source/role/external_id/run_id 写入 partIDs
  |         (必须在删 non-primary track_artists 之前，彻底规避 restore_sourced_track_artist_after_delete 复活)
  |      c) Non-primary track_artists: 按 (track_id, role) 重写 position 连续且去重的艺人序列
  |      d) album_artists: 按 album_id 重写 position 连续且去重的艺人序列，并清空失效的 albums.artist_credit
  |      e) 更新受影响 tracks.updated_at 与 albums.updated_at
  |
  +-> 4. 持久化规则 & 清理待审状态:
  |      - UPSERT artist_credit_splits (status='active', part_ids_json, part_names_json, snapshot_json)
  |      - 若 remaining == 0:
  |          * 记录被关闭的 candidate IDs 到 snapshot
  |          * artist_match_candidates(status='candidate') -> 'rejected'
  |          * artist_source_match_state(source='_composite') -> 'rejected'
  |          * 若为手工点击应用且存在未应用 ai_review_decisions(credit_split)，同步标记或保持可追溯
  |
Commit
```

**孤儿清理保护**：在 `missing.go` 的 `cleanupOrphansSQL` 的 `protected_json_artists` CTE 中增加：
- `SELECT composite_artist_id FROM artist_credit_splits WHERE status='active'`
- `SELECT CAST(j.value AS INTEGER) FROM artist_credit_splits s, json_each(s.part_ids_json) j WHERE s.status='active' AND j.value IS NOT NULL`

### 3. 扫描入库重放（Rescan Replay）

**决策**：提供轻量事务辅助函数 `expandActiveCreditSplits(ctx, tx, ids []int64) ([]int64, error)`：
- 当 `ids` 命中 `artist_credit_splits(status='active')` 时，将其原地展开为对应的规范化目标 `part_ids`（经 `canonicalArtistID` 解析并去重，同时防止循环展开）。
- 调用点（均位于 `internal/storage/library.go`）：
  1. `upsertScannedAlbum` 写入 `album_artists` 前展开 `albumArtistIDs`（若发生展开，同时置空 `artist_credit` 以按拆分后艺人重新渲染）。
  2. `importTrackIntoAlbum` 写入 `track_artists(role='primary')`、幕后角色 `composer/lyricist/arranger/producer` 以及 `track_artist_sources(file_tag)` 时分别展开目标 ID（若 `track_primary_artist_state.active=1` 已生效，末尾 `applyPrimaryArtistCorrection` 仍以用户/AI 纠偏为准）。
- **开销控制**：仅在存在活跃拆分规则时执行一次按 `ids` 过滤的索引点查（PK 查询），对无拆分的曲库几乎零额外开销。

### 4. 一键撤销回滚（`RevertAIReviewDecision` 支持 `AITargetCreditSplit`）

**决策**：`aiCreditSplitSnapshot` 保存：
- `PrimaryTracks`: `[]creditSplitPrimaryUndo{TrackID, ExpectedRevision, BeforeActive, BeforeIDs}`
- `NonPrimaryRoles`: `[]creditSplitTrackRoleUndo{TrackID, Role, Rows}`（拆前完整行）
- `SourceRows`: `[]creditSplitSourceUndo`（拆前 `track_artist_sources` 行）
- `Albums`: `[]creditSplitAlbumUndo{AlbumID, Rows}`（拆前完整 `album_artists` 行）
- `RejectedCandidateIDs`: `[]int64`
- `HadCompositeSkipState`: `bool`

回滚事务逻辑：
1. 检查 `artist_credit_splits.status == 'active'`，更新为 `status='reverted'`。
2. 对 `PrimaryTracks`：若当前 `track_primary_artist_state.revision == ExpectedRevision`（未被后续人工编辑覆盖），通过 `SetPrimaryArtistCorrection` 同构事务恢复至拆前状态并推进 revision；若已被人工修改，则跳过该曲目不破坏人工编辑。
3. 对 `NonPrimaryRoles`、`SourceRows`、`Albums`：按快照恢复原有行（先删当前受影响的拆后行，再还原拆前 `track_artist_sources` 与 `track_artists` / `album_artists`），刷新 `updated_at`。
4. 恢复 `RejectedCandidateIDs` 为 `'candidate'`，若 `HadCompositeSkipState` 或原本无候选则将 `artist_source_match_state(source='_composite')` 恢复为 `'skipped_composite'`。
5. 标记 `ai_review_decisions.reverted = 1`。

同时，将 `storage.AITargetCreditSplit` 加入 `/admin/matches`「任务与补全」页签的 `RecentAIAppliedDecisions` 查询列表（`internal/http/identity.go`），使 AI 自动拆分记录与自动绑定记录统一可追溯、可一键撤销。

### 5. AI 自动模式、缓存升级与 Inline 扫描修复

- **阈值与护栏**（`internal/aireview/service.go`）：
  - `autoThresholdCreditSplit = 92`。
  - `evalCreditSplit` 接收 `mode string`。当 `mode == AIReviewModeAuto && out.IsComposite && len(parts) >= 2 && out.Confidence >= autoThresholdCreditSplit` 时，调用 `ApplyCompositeCreditSplitByAI`；若归一化后有效目标不足 2 位或包含自身，则自动降级为 `recommend_accept` / `uncertain`，不落库。
  - 允许 `ensureArtists` 自动创建曲库尚不存在的个人艺人（见需求决策，由一键撤销兜底）。
- **存量高置信建议免重评落库**：
  - 在 `evalCreditSplit` 命中 `alreadyEvaluated` 时：若 `mode == AIReviewModeAuto && !existing.Applied && !existing.Reverted && existing.Action == AIActionRecommendAccept && existing.Confidence >= autoThresholdCreditSplit`，从 `existing.Payload` 读取已分析的 `parts`，直接走 `ApplyCompositeCreditSplitByAI` 并更新该 `ai_review_decisions` 行的 `action='auto_confirm', applied=1, applied_snapshot_json=snap`，无需再次调用 LLM。
- **Inline 扫描修复**（`internal/enrichment/manager.go`）：
  - `if m.inlineAI != nil && m.inlineAI(ctx, storage.AITargetCreditSplit, artistID)` 返回 `true` 时，直接返回 `MatchResult{AutoMatched: true, Outcome: "matched"}`（或等效已处理状态），**不再调用 `SaveArtistCompositeSkip`**。

### 6. 单一艺人豁免收口

- 在 `internal/storage` 提供统一辅助：
  - 单条检查：`isCompositeArtistTx(ctx, q, artistID, name)` -> `metadata.CompositeArtistCredit(name) && !existsInExemptions(artistID)`。
  - 批量检查：`compositeExemptArtistIDs(ctx, q)` -> `map[int64]bool`，在 `PendingArtistReviewSplitPages`、`PendingCreditSplitArtistIDs`、`PendingArtistReviewTargetIDs`、`FilterArtistsForScan` 等批量查询中仅多一次轻量 `SELECT artist_id FROM artist_composite_exemptions`。
  - 对外暴露 `s.store.IsCompositeArtist(ctx, artistID, name)` 给 `enrichment.Manager` 使用。
- 新增 `ExemptCompositeArtist(ctx, artistID, reason)`：写入 `artist_composite_exemptions`，并清除 `artist_source_match_state(source='_composite')` 的 `skipped_composite` 记录，使该艺人立刻从 Tab 2 移出并成为下次「扫描未检查的艺术家」的合格扫描目标。

### 7. HTTP 接口与前端工作台交互

新增/增强端点：
- `POST /admin/artists/{id}/apply-credit-split`：兼容多值 `part` 字段与逗号/斜杠分隔文本输入（支持卡片内微调后的自由提交）；返回明确更新统计（如“已拆分为 A、B（更新 15 首曲目、1 张专辑）”）或在 0 命中时给出清晰提示。
- `POST /admin/matches/credits/apply-high-confidence`：批量应用当前所有待处理、未撤销、置信度 ≥92%、`isComposite=true` 且至少 2 位有效艺人的 AI 拆分建议，逐条容错提交并汇总反馈“成功拆分 X 位复合署名（更新 Y 首曲目、Z 张专辑），跳过 K 位”。
- `POST /admin/artists/{id}/exempt-composite`：标记为单一艺人（豁免复合署名），重定向回 Tab 2 并提示可启动增量扫描匹配身份。

工作台 UI（`/admin/matches?tab=credits`）：
- Tab 2 顶部操作栏展示高置信待应用计数与「批量应用高置信拆分（≥92%）」主按钮（带二次确认防误触），并列保留「AI 分析复合署名」。
- 卡片头部展示角色分布胶囊（例如 `作曲 15` / `作词 2` / `专辑艺人 1` / `演唱 3`），消除“0 张专辑 · 15 首到底是什么”的困惑。
- 卡片内提供轻量「微调人名」切换态：展开后可增删/编辑拆分艺人标签，再点击「应用拆分」；未展开时仍支持一键应用。
- 对于「AI 判定非复合署名」的卡片，主操作区提供「确认为单一艺人」按钮；其他卡片提供次级「非复合署名」入口。

## Risks / Trade-offs

- **[Risk] 批量应用或 AI 自动拆分在大型曲库（如单复合艺人关联数百首曲目）持有 SQLite 写锁时间偏长**
  → **Mitigation**：单条复合艺人拆分在一个独立短事务内完成；批量应用端点按艺人逐条提交（非整批包在一个超大事务），结合既有 `beginWrite` / `withBusyRetry` 避免阻塞并发播放与读取请求。
- **[Risk] `RevertAIReviewDecision` 回滚非 `primary` 角色时，若用户在自动拆分后曾重新扫描或手动编辑，可能存在状态偏差**
  → **Mitigation**：`primary` 严格校验 `revision == ExpectedRevision` 防止覆写人工编辑；非 `primary` / `album_artists` 回滚仅在拆分规则仍为 `active` 时执行，且按拆前快照精确恢复该 `(track_id, role)` 与 `album_id` 关联。
- **[Risk] `ai_review_settings` 重建迁移需要保持外键与备份校验兼容**
  → **Mitigation**：`ai_review_settings` 无外部表引用，按标准 SQLite 表重建流程（`CREATE TABLE ..._new` -> `INSERT SELECT` -> `DROP` -> `RENAME`）在迁移事务内执行，并由既有 `backup_schema` 迁移模板测试自动验证一致性。
- **[Risk] 拆出新单人艺人后不会立即有头像/MBID**
  → **Mitigation**：新单人艺人默认无 `artist_source_match_state` 记录，自动成为下次「扫描未检查的艺术家」增量扫描的对象，并在操作提示中引导用户按需触发增量扫描。

## Migration Plan

1. 随版本发布执行 `088_composite_credit_splits.sql`（自动前向迁移，默认 `credit_mode` 维持既有值，不改变未主动开启 `auto` 用户的现有行为）。
2. 已有 266 条高置信 AI 建议在升级后无需重跑模型：用户既可在「复合署名」页签点击「批量应用高置信拆分（≥92%）」一次性落库，也可在「AI 设置」将复合署名切换为「分级自治（≥92 分自动拆分）」后由后续预审/增量扫描自动执行。
3. 回退策略：如个别自动拆分有误，可在「任务与补全 -> AI 最近自动处理记录」中单条一键撤销，立即还原关联曲目、专辑及待审队列。
