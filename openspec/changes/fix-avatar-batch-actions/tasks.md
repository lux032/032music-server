# Tasks

## 1. 后端批量保留/清空分类计数与反馈文案

- [x] 1.1 修改 `internal/http/artist_image_backfill.go` 中的 `handleBatchClearArtistImageCandidates` 与 `handleBatchKeepArtistImageCandidates`：先显式处理 `len(ids) == 0` 返回明确空选提示（不再输出「可能已处理或存在自定义头像」）；循环内将跳过原因拆分为 `alreadyHandled`（`sql.ErrNoRows`）、`customProtected`（`storage.ErrArtistImageBackfillState`）与 `failed`（其他错误），JSON 响应保留 `processed`/`skipped` 并附细分字段，表单重定向按实际非零分类拼接提示文案。完成标准：在 `internal/http/web_search_and_avatar_audit_test.go` 扩展测试覆盖空 `candidateIds`、正常批量处理、重复/已处理项、并发自定义头像保护项的提示文案与 JSON 字段，并运行 `go test ./internal/http -run "TestArtistImageSearchQueueAndBatchEndpoints"` 通过。

## 2. 前端待审头像工作台事件流与二次确认防轮询重置

- [x] 2.1 修改 `internal/http/assets/admin.js` 中待审头像工作台模块：将 `document.addEventListener('submit', ...)` 改为捕获阶段（`, true`）并在单卡 `data-avatar-search-form` 拦截时同步 `e.preventDefault()` 与 `e.stopPropagation()`，确保先于 `router.js` 运行；在 `updateFilterAndToolbar(root)` 中同步把当前 `selectedIds` 写入两个 `[data-avatar-batch-form]` 的 `input[name="candidateIds"]` 作为纵深防御；按选中集合签名追踪二次确认态，仅在选中项实际变化或归零时解除 `[data-avatar-batch-clear]` 武装状态，并在武装期间保持确认文案不被 2 秒队列轮询覆写。完成标准：在 `tests/e2e/` 新增桌面端 Playwright 测试用例（隔离测试数据，不污染既有夹具），在真实 PJAX 环境下验证：（1）勾选多张待审卡片并二次确认「批量清空头像」后实际提交非空 `candidateIds`、后端落库并刷新移除对应卡片；（2）勾选执行「批量保留原图」同样生效；（3）点击单卡「联网找新图 / 重新搜图」仅发一次 JSON 入队请求且不触发 PJAX 整页刷新；（4）队列轮询刷新期间勾选未变时「确认清空 N 位头像」武装态与「取消」按钮不被重置；运行对应 `npx playwright test` 用例通过。

## 3. 全链路回归验证

- [x] 3.1 执行全量受影响 Go 测试与 OpenSpec 校验（`go test ./internal/http/... ./internal/storage/...`、`openspec validate "fix-avatar-batch-actions" --strict`），确认无回归且所有变更规范校验通过。
