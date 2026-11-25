# Tasks

## 1. 数据库迁移与孤儿清理保护

- [x] 1.1 新增 `internal/storage/migrations/088_composite_credit_splits.sql`，重建 `ai_review_settings` 放宽 `credit_mode` CHECK 约束至 `('off', 'recommend', 'auto')`，创建 `artist_credit_splits` 与 `artist_composite_exemptions` 表及索引，并编写迁移与备份 schema 兼容测试（运行 `go test ./internal/storage -run "TestMigration|TestBackupSchema"` 通过）
- [x] 1.2 更新 `internal/storage/missing.go` 的 `cleanupOrphansSQL`，将活跃 `artist_credit_splits` 引用的 `composite_artist_id` 与 `part_ids_json` 目标艺人纳入保护集合，并补充单元测试验证拆分后执行孤儿清理不会误删复合艺人或目标艺人（运行 `go test ./internal/storage -run TestCleanupOrphans` 通过）

## 2. 全角色拆分引擎、扫描重放与一键撤销

- [x] 2.1 在 `internal/storage/ai_review_confirm.go` 重构 `ApplyCompositeCreditSplit`（并新增 `ApplyCompositeCreditSplitByAI` / 结构体 `CreditSplitResult`），单事务原子覆盖 `track_artists(role='primary')`、`track_artists(role<>'primary')`、`track_artist_sources`（先改源表防止触发器复活）与 `album_artists`，完成 `canonicalArtistID` 归一化去重、自引用/零引用校验与 `artist_credit_splits` 持久化，并补充覆盖纯幕后角色、混合角色+专辑艺人、远端源防复活与已合并别名去重的存储测试（运行 `go test ./internal/storage -run TestApplyCompositeCreditSplit` 通过）
- [x] 2.2 在 `internal/storage/library.go` 的 `upsertScannedAlbum` 与 `importTrackIntoAlbum` 入库路径接入 `expandActiveCreditSplits` 重放逻辑，确保增量与全库重扫保留已生效的复合署名拆分，并补充全库重扫回归测试（运行 `go test ./internal/storage -run TestImportTrackCompositeSplitReplay` 通过）
- [x] 2.3 在 `internal/storage/ai_review_confirm.go` 的 `RevertAIReviewDecision` 中实现 `AITargetCreditSplit` 分支（含 `primary` revision 防覆写校验、非 `primary` 与 `track_artist_sources`/`album_artists` 快照恢复、候选与 `_composite` 状态回退），并补充 AI 拆分回滚与人工编辑并发保护单元测试（运行 `go test ./internal/storage -run TestRevertAICreditSplit` 通过）

## 3. 单一艺人误报豁免收口

- [x] 3.1 在 `internal/storage` 实现 `ExemptCompositeArtist` 与统一的复合署名判定辅助（排除 `artist_composite_exemptions`），接入 `artist_review.go`、`ai_review.go`、`artist_source_state.go`、`identity.go`、`artist_identity_conflict.go`、`ai_review_confirm.go` 与 `internal/enrichment/manager.go`，并补充豁免后移出 Tab 2 且可被常规扫描/绑定匹配的测试（运行 `go test ./internal/storage ./internal/enrichment -run "TestCompositeArtistExemption|TestArtistScan"` 通过）

## 4. AI 分级自治、缓存决策升级与 Inline 扫描修复

- [x] 4.1 更新 `internal/storage/ai_review.go` 允许保存 `credit_mode='auto'`，并在 `internal/aireview/service.go` 引入 `autoThresholdCreditSplit = 92`，让 `evalCreditSplit` 在 `auto` 模式下对 `isComposite=true && len(parts)>=2 && confidence>=92` 自动落库（允许创建新单人艺人）并保存回滚快照；同时升级 `alreadyEvaluated`，在 `auto` 模式下直接应用已缓存的高置信未应用建议且不重复调用模型，并补充 `aireview` 单元测试（运行 `go test ./internal/aireview -run "TestCreditSplitAuto|TestCreditSplitCachedUpgrade"` 通过）
- [x] 4.2 修复 `internal/enrichment/manager.go` 的复合署名 inline AI 分支：当 `inlineAI` 自动拆分返回 `true` 时不再调用 `SaveArtistCompositeSkip` 覆写状态，并补充 inline 自动拆分测试（运行 `go test ./internal/enrichment -run TestInlineCompositeCreditAutoSplit` 通过）

## 5. HTTP 接口、设置页与审核工作台前端

- [x] 5.1 更新 `internal/http/templates/ai-settings.html` 与 `internal/http/ai_settings.go`，为「复合署名拆分」增加 `auto`（分级自治：≥92 分自动拆分）选项及说明，并更新 `TestAISettingsPageAndSave` 断言验证（运行 `go test ./internal/http -run TestAISettingsPageAndSave` 通过）
- [x] 5.2 更新 `internal/http/ai_review.go`、`internal/http/identity.go` 与 `internal/http/app.go`：完善 `POST /admin/artists/{id}/apply-credit-split` 的曲目/专辑计数与 0 命中提示，新增 `POST /admin/matches/credits/apply-high-confidence` 批量高置信拆分端点与 `POST /admin/artists/{id}/exempt-composite` 豁免端点，将 `AITargetCreditSplit` 纳入 `RecentAIAppliedDecisions`，并查询返回 Tab 2 卡片角色分布统计
- [x] 5.3 更新 `internal/http/templates/match-review.html`、`internal/http/assets/pages.css` 与 `internal/http/assets/admin.js`：在「复合署名」页签增加顶部「批量应用高置信拆分（≥92%）」按钮与计数、卡片角色分布徽标、拆分名单内联编辑微调交互、单一艺人豁免按钮，以及「任务与补全」页签展示 AI 自动拆分撤销记录，并补充 HTTP 端到端测试覆盖单条编辑拆分、批量应用、单一艺人豁免与撤销闭环（运行 `go test ./internal/http -run "TestWorkReviewAndMatchReviewAIIntegration|TestCreditSplit"` 通过）

## 6. 回归验证

- [x] 6.1 运行完整后端测试套件 `go test ./internal/storage ./internal/aireview ./internal/enrichment ./internal/http` 与 `go vet ./...`，确认所有新增与既有测试全部通过且无回归

## Workflow follow-up

- 实现并验证全部任务后，使用 `openspec-verify-change` 核对实现与规范一致性。
- 审查通过后执行 `openspec-archive-change` 归档变更并同步主 specs。
