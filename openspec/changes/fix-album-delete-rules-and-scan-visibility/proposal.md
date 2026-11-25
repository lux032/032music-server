# Proposal: 修复专辑删除规则永久拦截新专辑与扫描结果失真问题

## Why

当前用户在后台点击「删除专辑」时，`DeleteAlbums` 会无条件向 `album_key_rules` 写入一条永久的 `action='delete'` 黑名单（按 `目录|专辑名|年份` 构成的 `grouping_key` 匹配）。当用户原本只是为了清理整理目录/改名后产生的重复旧专辑（原目录随后已被移走或清空），数周后又把新的正式专辑放入同名目录时，新专辑会被这条残留的僵尸 `delete` 规则静默拦截、永远无法入库。

与此同时，扫描器将命中 `action='delete'` 的文件静默返回 `nil` 并错误计入 `processed`（已处理），既让控制台呈现出“已处理 N · 失败 0 却始终未入库”的假象，又因为 `processed > 0` 在每次零变更增量扫描时反复触发 `CleanupOrphans`；而且后台没有任何界面可以查看或解除 `album_key_rules`（包括 `delete` 屏蔽规则与 `merge` 合并规则），用户完全无法在产品内自查与恢复。

## What Changes

- **空目录僵尸 `delete` 规则自动清理**：在未取消且通过 S1 挂载保护的正常曲库扫描结束时，对比本次发现的全部音频文件所在专辑目录集合；若某条 `action='delete'` 规则对应的专辑目录在磁盘上已不存在任何受支持音频文件，则自动删除该过期规则。文件仍留在磁盘上的已删专辑继续保留防复活语义。
- **后台「专辑屏蔽与合并规则」管理页**：新增管理后台页面与路由（如 `/admin/library/rules`），集中展示 `album_key_rules` 中的屏蔽（`delete`）与合并（`merge`）规则（标题、规则键、目标专辑、创建时间），支持单条/批量一键解除规则并可选立即触发增量扫描；同时在控制台「缺失文件」同级区域与「删除专辑」确认提示中补充入口与可恢复说明。
- **扫描统计、零变更清理与控制台提示修正**：
  - 数据库新增 `scan_jobs.rule_skipped_files` 列（Migration 089），记录本次扫描因 `album_key_rules(action='delete')` 被跳过的文件数。
  - `ImportTrackBatch` 显式区分真正入库、规则跳过与导入错误；扫描器将规则跳过的文件计入 `skipped`（同时累计 `ruleSkipped`）而不再计入 `processed`，确保仅有规则跳过且无真实变更时不再误触发 `CleanupOrphans`。
  - 扫描完成日志输出 `ruleSkipped` 计数，控制台扫描卡片及 `/admin/status` 轮询状态在 `ruleSkipped > 0` 时明确提示被已删专辑规则跳过的文件数，并直接链接至规则管理页。

## Capabilities

### New Capabilities
- `album-key-rules-management`: 定义专辑屏蔽与合并规则（`album_key_rules`）的生命周期管理，包括扫描完成后的安全僵尸 `delete` 规则自动清理，以及管理后台对屏蔽/合并规则的查询、单条与批量解除及重扫恢复能力。

### Modified Capabilities
- `library-scanner-pipeline`: 修正并扩展扫描流水线的任务记账与状态可观测要求，将命中 `album_key_rules(action='delete')` 的文件准确归入 `skipped` 与 `rule_skipped_files` 而非 `processed`，避免零真实变更扫描误触发孤儿清理，并在日志与控制台状态中暴露规则跳过提示。

## Impact

- **数据库迁移**：新增 `internal/storage/migrations/089_scan_rule_skipped.sql`（为 `scan_jobs` 追加 `rule_skipped_files` 标量列，无表重建）。
- **存储层**：`internal/storage/library.go`（`ScanJob`、`UpdateScanJob`/`FinishScanJob`、`ImportTrackBatch`/`importTrackTx` 返回结果区分规则跳过）、`internal/storage/album_merge.go`（新增规则列表查询、单条/批量删除规则、按已发现目录集合清理僵尸 `delete` 规则，均遵循 `beginWrite` 前置写锁）。
- **扫描器层**：`internal/scanner/scanner.go`（`run` 与 `runImportPipeline` 正确统计 `ruleSkipped` 与 `skipped`，仅在真实变更时调用 `CleanupOrphans`，在通过 S1 保护后执行僵尸 `delete` 规则清理，并记录结构化日志）。
- **HTTP 接口与前端**：`internal/http/app.go`、`internal/http/library.go`、新增规则管理处理器与模板（`internal/http/album_rules.go`、`internal/http/templates/album-rules.html`）、`internal/http/templates/dashboard.html`、`internal/http/templates/_layout.html`、`internal/http/assets/admin.js`、`internal/http/assets/browse.js`。
- **测试覆盖**：补充存储层（规则列出/删除、僵尸规则清理及多碟子目录边界、批次导入结果区分）、扫描器层（规则跳过计入 `skipped` 而非 `processed`、S1/取消保护下绝不误清僵尸规则、零真实变更跳过 `CleanupOrphans`）及 HTTP 管理页测试。
