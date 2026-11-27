# Proposal

## Why

「艺术家与身份审核 → 待审头像」页的批量清空头像（以及同路径的批量保留原图）在实际浏览器中点击确认后完全无效，且返回误导性的「未清空任何头像（可能已处理或存在自定义头像）」提示；同时单卡「联网找新图 / 重新搜图」会重复提交并触发整页 PJAX 刷新，手动找图队列 2 秒轮询也会反复冲掉二次确认按钮的武装状态。根本原因是 `admin.js` 的待审头像 `submit` 委托监听注册在冒泡阶段、执行晚于 `router.js` 的 PJAX 表单拦截器，导致批量提交发出空 `candidateIds`，而后端又将「未收到 ID」「已处理」「自定义头像保护」「真实写入失败」混为同一条兜底文案。

## What Changes

- 修复「待审头像」工作台前端表单提交事件阶段与选中态同步，确保多选「批量清空头像」「批量保留原图」在 PJAX 拦截前准确携带已选 `candidateIds`，且单卡「联网找新图 / 重新搜图」原地入队、不重复触发 PJAX 整页刷新。
- 修复「批量清空头像」二次确认武装状态与手动找图队列 2 秒轮询的竞态：仅在选中集合实际变化或归零时重置确认态，避免队列轮询每 2 秒自动取消确认。
- 细化 `POST /admin/matches/images/candidates/batch-clear` 与 `batch-keep` 的批量处理统计与用户反馈文案，明确区分空选（`len(ids) == 0`）、已处理、并发自定义头像保护与真实错误失败，消除无自定义头像却提示「可能存在自定义头像」的矛盾反馈。
- 补齐 Go HTTP 回归测试（空 ID、部分跳过、自定义头像保护、真实错误反馈）与 Playwright 浏览器 E2E 测试，覆盖真实 PJAX 环境下的勾选、二次确认、批量清空/保留与单卡原地入队。

## Capabilities

### New Capabilities

### Modified Capabilities
- `artist-avatar-ai-audit`: 补充「待审头像」工作台在 PJAX 环境下批量保留/清空（含二次确认不被队列轮询打断）、单卡原地入队不触发重复提交，以及批量端点按空选/已处理/自定义头像保护/失败分类反馈的规范要求。

## Impact

- 前端：`internal/http/assets/admin.js`（待审头像工作台 `submit` 捕获阶段、隐藏字段同步、二次确认防轮询误重置）。
- 后端：`internal/http/artist_image_backfill.go`（`handleBatchClearArtistImageCandidates`、`handleBatchKeepArtistImageCandidates` 的计数拆解与提示文案/JSON 返回字段）。
- 测试：`internal/http/web_search_and_avatar_audit_test.go` 与 `tests/e2e/` 新增待审头像批量与入队浏览器 E2E 用例；无数据库迁移、无破坏性 API 变更。
