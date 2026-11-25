# Proposal: 复合署名全角色拆分修复与 AI 分级自治优化

## Why

艺术家与身份审核工作台的「复合署名」功能当前存在两类直接影响可用性的问题：

1. **手动一键拆分对绝大多数条目实际无效（Bug）**：列表与 AI 分析采集了出现在任意 `track_artists` 角色（作曲/作词/编曲/制作/乐手/演唱）及 `album_artists` 中的复合署名，但 `ApplyCompositeCreditSplit` 仅查询并修改 `track_artists.role = 'primary'`，导致所有纯幕后角色和专辑艺人复合署名点击「一键应用拆分到关联曲目」后返回“更新 0 首曲目”、数据库无变化、条目滞留列表；混合角色条目也因剩余引用不为零而无法清出待审，且非 `primary` 角色与 `album_artists` 缺少跨全库重扫的持久化保护。
2. **高置信 AI 拆分结果未自动落库，缺少自治与批量消化闭环**：`credit_mode` 在数据库 CHECK 约束、存储校验、AI 设置页和 `aireview.Service` 四层被锁死为仅 `off` / `recommend`，即使 AI 给出 99%–100% 置信度也无法自动应用；同时缺少 `credit_split` 的一键撤销回滚机制、对已分析高置信建议的免重评落库路径，以及对误报单一组合（如带 `&` 或逗号的乐队名）的豁免出口。

## What Changes

- **数据库与持久化基线（Migration 088）**：
  - 放宽 `ai_review_settings.credit_mode` 的 CHECK 约束，允许 `off`、`recommend`、`auto` 三种策略。
  - 新增 `artist_credit_splits` 持久化规则与操作快照表，记录复合艺人到拆分目标艺人列表的映射、来源（`manual` / `ai`）、生效状态与回滚快照。
  - 新增 `artist_composite_exemptions` 单一艺人豁免表，允许将误判为复合署名的真实单一艺人/组合移出复合署名流程并放行常规外部身份匹配。
  - 在 `cleanupOrphansSQL` 孤儿清理语句中保护活跃拆分规则涉及的复合艺人及目标艺人 ID，避免重扫重放和一键撤销因外键/行删除而失效。
- **全角色拆分引擎、扫描重放与一键撤销**：
  - 重写 `ApplyCompositeCreditSplit`（及 AI 专用的 `ApplyCompositeCreditSplitByAI`），单次拆分原子覆盖 `track_artists(role='primary')`（继续复用 `PrimaryArtistCorrection` 状态机）、非 `primary` 的全部 `track_artists` 角色（`composer`/`lyricist`/`arranger`/`producer`/`instrument:*` 等）、`track_artist_sources`（先改源表防止 `restore_sourced_track_artist_after_delete` 触发器复活旧行）以及 `album_artists`。
  - 对目标艺人经 `canonicalArtistID` 归一化与去重，拒绝空拆分或包含复合艺人自身的无效拆分；当实际命中 0 条关联时返回明确提示而非假成功。
  - 在 `ImportTrack` 与 `upsertScannedAlbum` 扫描入库路径末尾重放活跃 `artist_credit_splits` 规则，确保全库重扫不会将拆分结果覆盖回原始复合标签。
  - 在 `RevertAIReviewDecision` 中补齐 `AITargetCreditSplit` 的完整回滚逻辑，并将 `credit_split` 纳入审核页「AI 最近自动处理记录」。
- **AI 复合署名分级自治与存量提速**：
  - 开放 `credit_mode = 'auto'`，设置自动拆分阈值为 `92`（要求 `isComposite=true`、归一化后不少于 2 位独立艺人、且不包含原复合艺人自身，允许自动创建曲库尚未收录的新单人艺人）。
  - 升级 `alreadyEvaluated`：在 `auto` 模式下命中已有高置信（≥92%）且未撤销的建议决策时直接应用落库，无需重调大模型消耗 token。
  - 修复 `enrichment.Manager` 在 inline AI 自动拆分复合署名后仍无条件写入 `skipped_composite` 的状态覆写问题。
- **单一艺人误报豁免与工作台交互优化**：
  - 收口全库存储与匹配层的复合署名判断逻辑，统一排除已写入 `artist_composite_exemptions` 的艺人，并提供 `POST /admin/artists/{id}/exempt-composite` 端点。
  - 升级 `/admin/matches?tab=credits` 与 `/admin/settings/ai` 页面：新增 `credit_mode=auto` 选项、Tab 2 顶部「批量应用高置信拆分（≥92%）」入口、卡片内拆分名单微调编辑、角色分布展示、单一艺人豁免按钮及准确区分曲目/专辑的反馈提示。

## Capabilities

### New Capabilities
- `composite-credit-split`: 定义复合署名的全角色（演唱/幕后/专辑艺人）原子拆分、跨重扫持久化重放、AI 分级自治与一键撤销、单一艺人误报豁免，以及审核工作台批量与微调交互能力。

### Modified Capabilities
<!-- 无既有 capability 需求变更；Migration 088 的索引与孤儿清理保护满足既有 sqlite-storage-foundation 基线约束。 -->

## Impact

- **数据库**：新增 `internal/storage/migrations/088_composite_credit_splits.sql`（重建 `ai_review_settings` 放宽约束 + 新建 `artist_credit_splits`、`artist_composite_exemptions` 及配套外键索引）。
- **存储与扫描**：`internal/storage/ai_review.go`、`internal/storage/ai_review_confirm.go`、`internal/storage/artist_review.go`、`internal/storage/artist_source_state.go`、`internal/storage/identity.go`、`internal/storage/artist_identity_conflict.go`、`internal/storage/library.go`、`internal/storage/missing.go`。
- **AI 审核与富化**：`internal/aireview/service.go`、`internal/aireview/runs.go`、`internal/enrichment/manager.go`。
- **HTTP 接口与前端工作台**：`internal/http/app.go`、`internal/http/ai_review.go`、`internal/http/ai_settings.go`、`internal/http/identity.go`、`internal/http/templates/ai-settings.html`、`internal/http/templates/match-review.html`、`internal/http/assets/pages.css`、`internal/http/assets/admin.js`。
- **测试覆盖**：需补充存储层（多角色拆分/重扫重放/触发器防复活/孤儿清理保护/撤销回滚/豁免）、AI 审核层（auto 阈值/缓存决策升级/inline 不覆写）与 HTTP 集成测试（单条编辑拆分/批量高置信应用/豁免/设置保存）。
