# Tasks

## 1. 存储层迁移与路由策略枚举

- [x] 1.1 新增迁移 `internal/storage/migrations/096_native_web_search_provider.sql`，重建 `web_search_settings` 将 `provider` CHECK 约束扩展为 `('native_then_search', 'tavily', 'deepseek', 'tavily_then_deepseek')` 并完整保留既有行数据；运行 `go test ./internal/storage/...` 验证迁移升级与既有数据兼容性。
- [x] 1.2 更新 `internal/storage/web_search.go` 新增 `WebSearchProviderNativeThenSearch = "native_then_search"`，扩展 `NormalizeWebSearchProvider` 与 `WebSearchSettings.Configured(alias, ai)`（在 `native_then_search` 下仅配置全局 AI 即可返回 `true`），并补充对应单元测试运行 `go test ./internal/storage -run WebSearch` 通过。

## 2. SSRF 安全本地公网网页正文提取

- [x] 2.1 在 `internal/netfetch` 新增 `FetchPageText(ctx, client, rawURL, ua)` 与 HTML 纯文本清洗辅助函数（复用 `ValidatePublicURL`、公网 transport、逐跳重定向公网校验与 2 MiB 响应体上限，过滤 `<script>`/`<style>`/`<noscript>`/`<svg>` 并识别 403/429/503/WAF 空壳页），编写单元测试覆盖正常页面提取、反爬空壳识别与私网/重定向 SSRF 拦截，运行 `go test ./internal/netfetch/...` 通过。

## 3. OpenAI 原生联网搜索客户端与多级回退路由

- [x] 3.1 新增 `internal/websearch/native.go` 实现 `NativeClient`：自动归一化 `/v1`、`/v1/chat/completions`、`/v1/responses` 至 `.../responses`，默认 90 秒超时、复用代理与共享 RPM 限流，发送 `web_search_preview`（`search_context_size: "medium"`）+ `include: ["web_search_call.action.sources"]` + `text.format: json_schema`，实现定向 HTTP 400 扩展参数降级重试、已完成 `web_search_call` 强校验、URL 规范化（剥离 `utm_*`/`fbclid`/`gclid` 等追踪参数）与来源池防幻觉过滤、`/v1/responses` `open_page` 单页兜底提取、`usage` 回调记账及脱敏 `WebError` 映射；编写单元测试覆盖上述全部路径并运行 `go test ./internal/websearch/...` 通过。
- [x] 3.2 重构 `internal/websearch/registry.go`：将 `RouterClient` 升级为有序多级回退链（`native_then_search` 按 `Native -> Tavily -> DeepSeek` 顺次执行，错误、未触发真实搜索或有效结果为 0 时自动回退；`Fetch` 按「本地公网直连 -> Tavily/DeepSeek 提取 -> 原生 `open_page`」分层执行），新增按 `(responsesURL, model)` 键控的 10 分钟不支持短冷却（配置变更或手动 `TestConnection` 时重置）、`SetRateWait` / `SetUsageRecorder` 注入与原生探针测试摘要，补充多级回退、0 结果回退、冷却跳过与重置单元测试并运行 `go test ./internal/websearch/...` 通过。

## 4. 服务装配、限流文案与管理端/初始化向导接入

- [x] 4.1 在 `cmd/server/main.go` 为共享 `websearch.Registry` 注入全局 `sharedAILimiter` 限流回调与 `db.AddAIUsage`（`web_search_native`）用量记录回调；在 `internal/enrichment/ratelimit.go` 补充 `tavily` 与 `native` 的中文限流显示名并验证 `go test ./internal/enrichment -run RateLimit` 通过。
- [x] 4.2 更新 `internal/http/ai_settings.go` 与 `internal/http/templates/ai-settings.html`：注册 `"web_search_native": "原生联网搜索"` 用量标签，在搜索提供商策略下拉框增加 `native_then_search` 选项，更新 `webSearchStatusSummary` 状态文案，将 `handleAISettingsWebSearchTest` 超时放宽至 90 秒；补充 HTTP 测试验证保存、状态摘要与测试接口并运行 `go test ./internal/http -run 'TestAISettings|TestWebSearch'` 通过。
- [x] 4.3 更新 `internal/http/setup_wizard.go`、`internal/http/templates/setup.html` 与 `internal/http/assets/setup.js`：在向导第 2 步搜索路由策略下拉框增加 `native_then_search` 选项，向导页内测试联网搜索请求补传 `llmModel` 与 `llmProxyUrl` 并将超时放宽至 90 秒，确认仅配置全局 AI 选择 `native_then_search` 时第 3 步正确解锁依赖联网搜索的自动化能力；补充向导单测并运行 `go test ./internal/http -run Setup` 通过。

## 5. 跨模块回归验证

- [x] 5.1 运行 `go test ./internal/storage/... ./internal/netfetch/... ./internal/websearch/... ./internal/enrichment/... ./internal/artistaliases/... ./internal/aireview/... ./internal/http/...` 全量回归验证，确认四大联网业务模块（头像、简介、中文别名、AI 审核）与既有 Tavily/DeepSeek 策略无回归。
