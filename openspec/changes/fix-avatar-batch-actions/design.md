# Design

## Context

见 `proposal.md` 的背景说明。当前页面共享布局 `_layout.html` 按顺序加载：

```text
<script defer src="navigation.js">
<script type="module" src="player-main.js">   --> init() -> setupPjaxNavigation() (router.js 冒泡阶段 submit)
<script defer src="admin.js">                 --> 待审头像工作台 submit (当前也在冒泡阶段, 排在 router.js 之后)
<script defer src="browse.js">
```

按 HTML 规范，无 `async` 的 `type="module"` 与经典 `defer` 脚本按文档顺序在解析完成（`document.readyState === 'interactive'`）后依次执行。因此 `player-main.js` 同步调用 `setupPjaxNavigation()`，使 `router.js` 的冒泡阶段 `document` `submit` 监听先于 `admin.js` 注册。

同一仓库内其他需要在 PJAX 提交前改写表单或拦截提交的模块（如 `browse.js` 的作品审核批量表单、`queue.js` 的收藏表单、`playlist-picker.js` 的歌单封面表单）均采用**捕获阶段（`capture: true`）**确保先于 `router.js` 运行。待审头像工作台在 `admin.js` 注册 `submit` 监听时遗漏了捕获阶段，且工具栏刷新时未同步隐藏域 `candidateIds`、却无条件重置了二次确认武装态；后端 `handleBatchClearArtistImageCandidates` 与 `handleBatchKeepArtistImageCandidates` 也未区分空入参、已处理、并发自定义头像保护与真实错误。

## Goals / Non-Goals

**Goals:**
- 保证待审头像工作台的 `batch-keep`、`batch-clear` 与单卡 `data-avatar-search-form` 在 PJAX 路由和无 PJAX 降级场景下均可靠生效。
- 消除手动找图队列 2 秒轮询对「批量清空头像」二次确认状态的误重置。
- 让 `batch-clear` 与 `batch-keep` 的服务端计数与提示文案准确反映实际结果，不再把空选或 DB 错误伪装成「可能存在自定义头像」。
- 以 Go HTTP 测试与 Playwright 真实浏览器测试锁定上述行为，避免后续回归。

**Non-Goals:**
- 不修改 `router.js` 全局 PJAX 表单拦截架构，不改变现有 POST-Redirect-GET 导航模式或把 `batch-clear` / `batch-keep` 改写为纯客户端删除。
- 不修改数据库表结构，也不改动 `ClearArtistImageCandidate` / `KeepArtistImageCandidate` 的既有事务语义（仍保留单卡自定义头像并发保护与可撤销快照）。
- 不调整非待审头像页面的其他表单提交监听（例如 `browse.js` 中其他无关模块的监听顺序不在本次变更范围）。

## Decisions

### 1. 前端采用「捕获阶段 `submit` + 工具栏实时同步隐藏域」双保险
- **决策**：
  1. 将 `admin.js` 待审头像工作台的 `document.addEventListener('submit', ...)` 改为捕获阶段（`, true`）。在捕获阶段中，单卡 `data-avatar-search-form` 立即同步调用 `e.preventDefault()`（并 `e.stopPropagation()`）再执行异步 `postQueueAPI`，确保 `router.js` 不会看到未取消的提交；批量表单 `data-avatar-batch-form` 在捕获阶段校验非空并写入 `input[name="candidateIds"]`。
  2. 在 `updateFilterAndToolbar(root)` 每次刷新工具栏时，同步将 `Array.from(selectedIds).join(',')` 写入两个 `[data-avatar-batch-form]` 表单内的 `input[name="candidateIds"]`。
- **备选方案与取舍**：
  - *仅在 `updateFilterAndToolbar` 写隐藏域，不改 `submit` 阶段*：能修好 `batch-keep`/`batch-clear`，但修不好单卡 `data-avatar-search-form` 重复提交和整页 PJAX 刷新，也无法在 `selectedIds` 为空时先于 `router.js` 拦截提交。
  - *把 `batch-keep`/`batch-clear` 改成 `fetch` JSON + 手动重拉页面*：改动更大，且偏离本页其余表单（单卡保留/清空、审批、撤销）统一的 PJAX POST-Redirect-GET 模式。
  - 双保险方案改动最小，完全沿用本仓库 `browse.js` / `queue.js` 已验证的捕获阶段约定。

### 2. 二次确认态按选中集合签名判断是否重置
- **决策**：在待审头像工作台模块内维护 `lastClearConfirmKey`（由当前 `selectedIds` 排序后拼接的签名，并在 `initAvatarWorkbench()` 重置）。`updateFilterAndToolbar(root)` 仅在 `batchClearBtn` 已武装且「`totalSelected === 0` 或当前选中签名不等于武装时签名」时才解除武装态并移除「取消」按钮；武装态保持期间不覆盖按钮的确认文案（`确认清空 N 位头像`）。
- **备选方案与取舍**：
  - *在 `applyQueueSnapshot` 传入 `fromPoll` 标志跳过重置*：如果某张已被勾选的卡片在轮询时正好进入 `queued`/`running`/`found` 被从 `selectedIds` 剔除，跳过重置会导致按钮仍显示旧数量 `确认清空 5 位头像` 实际只剩 4 位，不够严谨。
  - 按实际选中集合签名判断既能在队列轮询且勾选未变时稳住确认态，又能在轮询确实锁走某张已选卡片时安全解除武装并更新数量。

### 3. 后端批量端点拆分 4 类计数与按需拼接提示文案
- **决策**：在 `handleBatchClearArtistImageCandidates` 与 `handleBatchKeepArtistImageCandidates` 中：
  - 先判断 `len(ids) == 0`：JSON 响应返回零计数，表单重定向直接返回明确空选提示（`未选中任何可清空的待审项` / `未选中任何可保留的待审项`），不触发任何「自定义头像」误导文案。
  - 循环中分别统计：`processed`、`alreadyHandled`（`sql.ErrNoRows`）、`customProtected`（`storage.ErrArtistImageBackfillState`，用于 `batch-clear`）、`failed`（其他未预期错误，同时记错误日志）。
  - JSON 响应保留既有 `processed` 与兼容的 `skipped`（`= alreadyHandled + customProtected + failed`），并附加细分字段 `alreadyHandled`、`customProtected`（clear）、`failed`。
  - 表单重定向提示按实际非零原因片段拼接，例如：
    - `processed > 0` 且无跳过/失败：`已批量清空 N 位艺术家缓存头像（可在任务页撤销恢复）`
    - `processed > 0` 且有跳过/失败：仅列出实际非零项（如「已处理 M 位」「自定义头像保护 K 位」「失败 F 位」）
    - `processed == 0`（但 `len(ids) > 0`）：直接按实际原因说明（如「选中项均已处理」/「选中艺人均存在自定义头像，未执行清空」/「批量清空失败（F 位），请稍后重试或查看日志」）。
- **备选方案与取舍**：
  - *批量接口在遇到自定义头像或已处理时整体回滚*：与既有「按单卡语义逐项落库」契约冲突，单条并发变化不应阻断其余合法项处理，因此维持逐项处理 + 精确分类汇总。

### 4. 测试策略：补齐 Go HTTP 细分断言 + Playwright 真实 PJAX 链路
- **决策**：
  - 扩展 `internal/http/web_search_and_avatar_audit_test.go`：验证空 `candidateIds` 返回空选提示（不含「自定义头像」）、存在并发自定义头像时正确计入 `customProtected` 并提示、已处理项返回对应提示、以及 `batch-keep` 同类路径。
  - 新增 Playwright E2E 用例（沿用 `tests/e2e/` 现有测试服务与登录方式，或使用可独立启动的轻量测试夹具）：在真实 Chromium + PJAX 环境下验证勾选多卡 -> 二次确认「批量清空头像」-> 实际发送非空 `candidateIds` 并移除已清卡片；验证「批量保留原图」；验证单卡「联网找新图」只发 JSON 入队请求而不触发 PJAX 整页刷新；验证轮询刷新时未改变勾选不会冲掉二次确认武装态。

## Risks / Trade-offs

- [Playwright E2E 夹具数据隔离] → 若现有固定 E2E 端口夹具不含待审头像种子数据，需评估是在现有 Playwright 测试启动脚本中补充独立待审头像种子，还是使用隔离临时服务/路由拦截；需在实现任务阶段检查 `playwright.config.js` 与测试夹具脚本（`scripts/`），避免污染 `batch6` 等既有测试数据。
- [JSON 消费者字段兼容性] → `batch-clear` / `batch-keep` JSON 响应保留原有 `processed` 和 `skipped` 字段，仅以新增可选字段暴露细分计数，保证向后兼容。
- [确认态文本与全局 `disarmInlineConfirm` 同步] → 当用户通过点 `取消` 或按 `Esc` 解除武装态时，需确保 `admin.js` 中 `disarmInlineConfirm` 恢复的 `originalLabel` 与最新选中数量一致；通过武装前设置 `data-original-label` 并在选中变化时统一更新按钮文本，避免出现过期数字。
