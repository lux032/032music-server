# 实施任务清单（Tasks）

## 1. 存储层 Schema 与专辑规则管理能力

- [x] 1.1 新增增量迁移 `internal/storage/migrations/089_scan_rule_skipped.sql`，为 `scan_jobs` 增加 `rule_skipped_files INTEGER NOT NULL DEFAULT 0 CHECK (rule_skipped_files >= 0)` 列；更新 `internal/storage/library.go` 中的 `ScanJob`、`UpdateScanJobProgress`（保持 `UpdateScanJob` 向后兼容）与 `LatestScanJob`，运行 `go test ./internal/storage -run TestScan` 验证迁移幂等性与扫描任务字段读写正确
- [x] 1.2 在 `internal/storage/library.go` 中新增 `ImportResult` 与 `ImportTrackBatchDetailed`（命中 `action='delete'` 时返回 `RuleSkipped: true, Err: nil`，保留 `ImportTrackBatch` 与 `ImportTrack` 兼容封装），并在 `internal/storage/scanner_storage_opt_test.go` 补充单元测试，验证单曲规则跳过标记与 `SAVEPOINT` 隔离语义
- [x] 1.3 导出 `AlbumFolder`（或共享归并辅助函数），在 `internal/storage/album_merge.go` 实现 `ListAlbumKeyRules`、`CountAlbumKeyRules`、`DeleteAlbumKeyRules` 与 `PruneStaleDeleteAlbumRules`（仅在需要删除行时通过 `beginWrite` 获取写锁，保留命中 `discoveredAlbumDirs`、多碟子目录、含 `|` 目录兜底及 `unreadableDirs` 的规则，绝不清理 `merge` 规则），并通过 `go test ./internal/storage -run "Test.*Album.*Rule|Test.*DeleteAlbums|Test.*MergeAlbums"` 验证全部边界用例

## 2. 扫描器记账修正与僵尸删除规则自动清理

- [x] 2.1 更新 `internal/scanner/scanner.go` 中的 `Manager.discover()` 收集归一化 `unreadableDirs`；更新 `runImportPipeline` 调用 `ImportTrackBatchDetailed`，将规则屏蔽文件计入 `skipped` 与 `ruleSkipped`（不计入 `processed` 或 `seenRel`），通过 `UpdateScanJobProgress` 持久化 `ruleSkipped` 并在扫描完成日志输出 `ruleSkipped`；运行 `go test ./internal/scanner -run TestScanRuleSkipped` 验证 `processed == 0`、`processed + skipped + failed == discovered` 且零真实变更时跳过 `CleanupOrphans`
- [x] 2.2 在 `Manager.run()` 中于空发现和 >50% 缺失比例（S1）保护及 `MarkMissingByIDs` 之后（且仅当 `!cancelled && len(discovered.files) > 0`）调用 `PruneStaleDeleteAlbumRules`；运行 `go test ./internal/scanner -run TestScanPrunesStaleDeleteRules` 验证空目录 `delete` 规则自动清理后可重新入库新专辑，而磁盘仍有文件（含 `CD1` 子目录）的 `delete` 规则、`merge` 规则以及取消/S1 拦截/不可读目录场景下规则均被完整保留

## 3. 后台专辑规则管理页与控制台可观测提示

- [x] 3.1 新增 `internal/http/album_rules.go` 与 `internal/http/templates/album-rules.html`，并在 `internal/http/app.go` 注册 `GET /admin/library/rules` 与 `POST /admin/library/rules/delete`（管理员鉴权 + CSRF 校验，展示 `delete`/`merge` 规则及合并目标链接，支持按 `<library_id>:<grouping_key>` 单条/批量解除规则及可选 `rescan=1` 触发增量扫描）；运行 `go test ./internal/http -run TestAdminAlbumRules` 验证鉴权/CSRF 拦截、规则展示、解除 `delete` 规则后重新入库以及解除 `merge` 规则保留既有曲目
- [x] 3.2 将规则提示接入控制台与导航：在 `internal/http/templates/dashboard.html`、`internal/http/assets/admin.js`（对接 `/admin/status`）与 `internal/http/app.go`（`handleDashboard` 规则计数摘要）中展示 `ruleSkippedFiles` 提示与 `/admin/library/rules` 入口；在 `internal/http/templates/_layout.html` 添加侧边栏导航入口；更新 `internal/http/assets/browse.js` 删除专辑确认提示文案；运行 `go test ./internal/http -run "TestDashboard|TestAdminStatus|TestAdminAlbumRules|TestAdminAlbums"` 验证页面渲染与状态接口字段

## 4. 跨层集成验证

- [x] 4.1 运行受影响全部模块的竞态与回归测试（`go test -race ./internal/storage/... ./internal/scanner/... ./internal/http/... ./internal/acquisition/...`），确认无回归、无数据竞争
