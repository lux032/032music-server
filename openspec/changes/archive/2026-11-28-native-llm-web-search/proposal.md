# Proposal

## Why

当前 `032music-server` 的统一联网搜索路由（`internal/websearch`）仅支持 Tavily 多 Key 轮询与 DeepSeek 联网两种外部搜索 API。如果管理员配置的全局大模型端点本身已支持 OpenAI `/v1/responses` 原生联网搜索（`web_search_preview`），目前仍必须额外维护独立的搜索 API 密钥，且无法直接利用模型内置的检索与网页读取能力。引入「模型原生搜索优先、不可用或搜不到时自动回退 Tavily / DeepSeek」的统一路由选项，可让艺术家头像搜图/审查、歌手简介联网概括、歌手中文别名补全与 AI 审核联网补证四个模块在不改业务流程、不新增密钥字段的前提下直接复用全局 AI 配置完成联网检索。

## What Changes

- **新增原生优先路由策略**：在 `web_search_settings.provider` 中新增 `native_then_search`（模型原生搜索优先，不可用或无有效结果时自动回退 Tavily / DeepSeek），完整保留既有的 `tavily_then_deepseek`、`tavily`、`deepseek` 策略与升级兼容性。
- **实现 OpenAI `/v1/responses` 原生搜索客户端**：
  - 直接复用全局 `ai_settings`（`base_url`、`api_key`、`model`、`proxy_url`、共享 RPM 限流），不新增独立密钥或端点表单字段；自动将 `/v1`、`/v1/chat/completions`、`/v1/responses` 归一化至 `.../responses`。
  - 默认发送 `tools: [{"type": "web_search_preview", "search_context_size": "medium"}]`、`include: ["web_search_call.action.sources"]` 与严格 `text.format: json_schema`，并对不支持扩展参数的第三方网关执行定向 HTTP 400 参数降级重试。
  - 强校验响应中必须存在已完成的 `web_search_call`，合并采集 `action.sources`、`open_page`/`find_in_page` 动作 URL 与 `annotations` 作为服务端真实来源池，经 URL 规范化（剥离 `utm_source=openai` 等追踪参数）后过滤模型输出，丢弃未出现在来源池中的幻觉链接。
  - 解析 `/v1/responses` 的 `usage`（`input_tokens`、`cached_tokens`、`output_tokens`、`reasoning_tokens`）并记入 `ai_usage`（用途：`web_search_native`）。
- **单页抓取分层与自动回退**：
  - 原生模式下的 `Fetch(url)` 优先使用 SSRF 安全的本地公网直连抓取 HTML 并提取正文纯文本（低延迟且保留网页原文，满足中文别名与 AI 审核的原文子串核验）；直连失败或遇到反爬/JS 空壳页面时，按已配置顺序回退 Tavily `/extract`、DeepSeek `/fetch`，若均未配置则使用 `/v1/responses` `open_page` 兜底。
  - `RouterClient` 扩展为有序多级回退链：原生搜索在接口报错、端点不支持、未触发真实联网、或经来源池校验后有效结果为 0 条时自动回退 Tavily -> DeepSeek；对明确不支持 `/v1/responses` 的 404/工具不支持错误做进程内短冷却避免批量空转。
- **设置页与初始化向导同步**：
  - 在 `/admin/settings/ai` 与 `/admin/setup` 第 2 步的搜索提供商策略下拉框中新增「模型原生优先，不可用或无结果时自动回退 Tavily / DeepSeek」选项。
  - 在 `native_then_search` 策略下，只要全局 `ai_settings.Configured()` 为真即视为联网搜索已就绪（无需配置 Tavily/DeepSeek Key），并将联网搜索连通性测试的超时放宽至 90 秒以适配原生检索耗时。

## Capabilities

### New Capabilities

### Modified Capabilities
- `web-search-routing`: 新增 `native_then_search` 路由策略与复用全局 AI 配置的 OpenAI `/v1/responses` 原生联网搜索客户端，定义服务端来源池反幻觉校验、单页抓取分层降级、无结果/异常自动回退 Tavily/DeepSeek、进程内不支持短冷却、用量记账与就绪判定规则。

## Impact

- **存储与迁移**：`internal/storage/migrations/096_native_web_search_provider.sql`、`internal/storage/web_search.go`（放宽 `web_search_settings.provider` CHECK 约束，不新增密钥列，不影响既有备份密钥保护结构）。
- **联网路由与网络抓取**：`internal/websearch/`（新增 `native.go`、扩展 `registry.go` 多级回退链、超时、冷却、限流与用量回调）、`internal/netfetch/`（补充 SSRF 安全的公网网页正文纯文本提取辅助）。
- **应用装配与管理界面**：`cmd/server/main.go`（将共享 RPM 限流器与 `ai_usage` 记录回调注入 `websearch.Registry`）、`internal/enrichment/ratelimit.go`（补充 `tavily`/`native` 限流展示名）、`internal/http/ai_settings.go`、`internal/http/templates/ai-settings.html`、`internal/http/setup_wizard.go`、`internal/http/templates/setup.html`、`internal/http/assets/setup.js`。
- **四大业务模块零业务改写**：艺术家头像搜图/审查、歌手简介联网概括、歌手中文别名补全、AI 审核联网补证均通过既有 `websearch.Client` 接口自动获益。
