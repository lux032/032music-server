# Tasks

## 1. 按域名隔离图片下载冷却与多源/第三方 429 非阻塞顺延

- [x] 1.1 在 `internal/enrichment/work_poster.go` 与 `internal/enrichment/ratelimit.go` 实现 `artistImageDownloadSource(rawURL)`（推导 `"artist image:<host>"`，无法解析时回退 `"artist image"`），更新 `downloadPublicImage` 在 `label == "artist image"` 时按域名检查与记录冷却，并在 `blockSourceUntil` 中对非 gate 托管的冷却表增加超 256 条时的惰性过期清理；移除 `artist_image_backfill.go` 中 `cacheAutoMatchedArtistImage` 的全局 `"artist image"` 预检；补齐单测验证域名 A 限流不阻塞域名 B（`go test -race ./internal/enrichment -run "TestArtistImageHostRateLimit|TestArtistImageBackfillRateLimit"`）。
- [x] 1.2 在 `internal/enrichment/artist_image_ai.go` 与 `internal/enrichment/artist_image_backfill.go` 落实 Tier 3 与多源模式图片 429 非阻塞顺延：在 `discoverArtistImageFromWebWithExclusions` 选图前过滤已处于冷却域名的候选 URL，下载命中 429/503 时仅记录该域名冷却并跳过而不向上抛出 `RateLimitError`；在 `acquireArtistAvatarWithAIGate`、`replaceAuditedArtistAvatarOpt` 与 `processStructuredArtistImageFallback` 中对非 `matched` 模式（含 `all`、`web_fallback`、`rescan_all`、`audit_all`、`audit_force`、`auto_ingest`）遇单图床 429 时标记 `hadTransientErr=true` 并立即顺延至下一图源/下一层级且不写长期负缓存，同时保留 `matched` 单源确认资料模式与上游共享 API 的既有退避等待和自动恢复语义；编写单测覆盖 Tier 2/3 图床 429 不卡死批量任务、顺延其他图源成功及不误写负缓存（`go test -race ./internal/enrichment -run "TestArtistImage.*RateLimit|TestArtistImageUnifiedVLMGate|TestArtistImageAutoQueue"`）。

## 2. 存储恢复清理与管理后台中文化活跃/历史限流提示

- [x] 2.1 在 `internal/storage/artist_image_backfill.go` 的 `TransitionArtistImageBackfillRun`（`"resume"` 分支）中于恢复时清理已过期（`<= now`）的 `waiting_until`（保留 `wait_total_ms` 与 `wait_source` 供历史展示），并补充存储单元测试验证手动恢复清除过期截止时间但保留累计等待（`go test ./internal/storage -run "TestArtistImageBackfill"`）。
- [x] 2.2 在 `internal/http/enrichment.go`、`internal/http/app.go`、`internal/http/templates/match-review.html`、`internal/http/templates/enrichment-jobs.html`、`internal/http/assets/admin.js` 与 `internal/http/assets/pages.css` 中实现 `waitSourceLabel` / `waitUntilFuture` 及前端 `formatWaitSource`：将 `artist image`、`artist image:<host>`、`ai`、`tavily`、`deepseek` 等来源统一映射为中文标签；仅当 `waitingUntil` 在未来时以琥珀色警告样式展示「限流中」，限流结束后（正常运行或手动暂停）降级为中性 `.is-historical` 样式并展示「历史限流（<来源>）累计等待 …」；补齐 HTTP 渲染测试覆盖英文键不泄漏及活跃/历史样式区分（`go test ./internal/http -run "TestMatchReviewPageImageBackfillUI|TestMatchReviewPageRunTasksUI|TestEnrichmentPageRunTasksUI" && node --check internal/http/assets/admin.js`）。

## 3. 全仓集成回归验证

- [x] 3.1 运行全量测试、编译、静态检查、前端语法检查与 OpenSpec 严格校验，确认既有 `matched` 限流自动恢复测试及全部新增回归测试通过（`go test ./... && go build ./... && go vet ./... && node --check internal/http/assets/admin.js && openspec validate "artist-image-host-rate-limit-isolation" --strict`）。

## Workflow follow-up

- 待实机验证确认批量任务不再因第三方图床 429 卡死且提示条显示清晰后，再归档本变更并同步主规格。
