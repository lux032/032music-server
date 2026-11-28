# Design

## Context

参见 `proposal.md` 的背景与动机。`032music-server` 已将艺术家头像联网搜图/审查、歌手简介联网概括、歌手中文别名补全和 AI 审核联网补证四个模块统一接入 `internal/websearch`（`Client` 接口：`Search(ctx, query) ([]SearchResult, error)` 与 `Fetch(ctx, pageURL) (string, error)`），由进程内共享的 `websearch.Registry` 统一管理配置路由与 Tavily 多 Key 轮询状态。

关键现状与实测约束如下：
- **配置模型**：`ai_settings` 仅保存单套全局 OpenAI 兼容配置（`base_url`、`api_key`、`model`、`fallback_model`、`review_model`、`proxy_url`、`requests_per_minute`）；`web_search_settings`（迁移 `091`）当前仅允许 `provider IN ('tavily', 'deepseek', 'tavily_then_deepseek')`。本次明确追求极简个人自用配置：不新增任何独立端点/Key/模型字段，原生搜索完全复用全局 `ai_settings`。
- **上游协议实测（`cli.032.im` + `gpt-6-luna`）**：
  - `/v1/chat/completions` 携带 `web_search_options` 会被静默忽略（返回 HTTP 200 但 `prompt_tokens=16`，未实际联网）；
  - `/v1/responses` 携带 `tools: [{"type": "web_search_preview", "search_context_size": "medium"}]`、`include: ["web_search_call.action.sources"]` 与 `text.format: json_schema` 稳定返回 HTTP 200；在严格 JSON 模式下 `output[].content[].annotations` 为空，但 `output[].type == "web_search_call"` 的 `action.sources` 完整返回 21–31 条真实检索 URL；
  - `/v1/responses` 实测单次检索耗时约 15–40 秒，单页 `open_page` 亦能真实执行并读取目标页，但现有 `websearch` 30 秒超时不足以稳定覆盖多跳原生搜索。
- **下游防幻觉护栏**：`artistaliases`（官网原文精确子串匹配自动入库）与 `aireview`（`quoteGroundedInPages`）依赖真实的网页正文内容，且 `artist_image_ai` 仅依赖 `Search()` 返回的真实 `pageURL` 后交由 `internal/netfetch.FetchPageImageMeta` 抓取 `og:image`。

## Goals / Non-Goals

**Goals:**
- 在 `internal/websearch` 中实现 OpenAI `/v1/responses` 原生联网搜索客户端（`NativeClient`），完整复用全局 `ai_settings`、`aiclient.Limiter` 与 `ai_usage` 统计。
- 将 `RouterClient` 升级为有序多级回退链，支持 `native_then_search`（原生优先 -> 报错/未联网/0 有效结果自动回退 Tavily -> DeepSeek），并对不支持 `/v1/responses` 的端点做进程内短冷却防空转。
- 在 `internal/netfetch` 复用既有 SSRF 防护传输层提供公网网页纯文本抓取能力，供 `NativeClient.Fetch` 优先直连取真原文，抓不到时平滑交由回退链或 `/v1/responses` `open_page` 兜底。
- 在 AI 设置中心与初始化向导第 2 步无缝暴露 `native_then_search` 策略，并在仅配置全局 AI 时即可判定联网就绪与通过连通测试。

**Non-Goals:**
- 不新增独立原生搜索接口地址、独立 API Key 或独立模型配置项，不修改 `backup_restore.go` 密钥表结构。
- 不引入 Anthropic `/v1/messages` 原生搜索协议（`032music-server` 全栈均为 OpenAI 兼容协议）。
- 不重构四大业务模块（头像、简介、中文别名、AI 审核）的既有调用流程、提示词或自动落库门槛（未来如有需要可再叠加单次直连优化）。

## Decisions

### Decision 1: 零新字段复用全局 `ai_settings`，仅扩展 `web_search_settings.provider`
- **选择**：
  - 新增迁移 `096_native_web_search_provider.sql`，重建 `web_search_settings` 表将 `provider` 的 `CHECK` 约束扩展为 `('native_then_search', 'tavily', 'deepseek', 'tavily_then_deepseek')`，保留原有行数据与默认值 `tavily_then_deepseek`。
  - `storage.WebSearchSettings.Configured(alias, ai)` 在 `provider == WebSearchProviderNativeThenSearch` 时返回 `ai.Configured() || hasTavily || hasDeepSeek`（仅配置全局 AI 即判定就绪）。
  - 端点 URL 归一化：`normalizeResponsesURL(ai.BaseURL)` 将 `.../v1` 转为 `.../v1/responses`，将 `.../v1/chat/completions`（含可选尾斜杠）替换为 `.../v1/responses`，已含 `/responses` 结尾的保持原样。
- **理由**：与用户自用场景完全契合，管理端和初始化向导仅在现有下拉框增加 1 个选项，不增加密钥管理、备份脱敏或表单心智负担。
- **替代方案**：增加独立的 `native_base_url` / `native_api_key` / `native_model` 字段——增加数据库敏感列、备份保护逻辑和界面复杂度，当前无需求。

### Decision 2: OpenAI `/v1/responses` 协议适配、定向 400 降级与来源池反幻觉校验
- **选择**：
  - `NativeClient.Search(ctx, query)` 发送 `POST <responsesURL>`：
    - `model`: `strings.TrimSpace(ai.Model)`
    - `instructions`: 明确要求搜索并仅基于真实搜索结果返回最多 5 条 `{url, title, content}` 严格 JSON，严禁编造 URL 或凭记忆作答，优先保留网页原文关键句。
    - `input`: 查询词（不截断到 DeepSeek 的 128 字符限制，保留完整查询意图）。
    - `tools`: `[{"type": "web_search_preview", "search_context_size": "medium"}]`
    - `include`: `["web_search_call.action.sources"]`
    - `text`: `{"format": {"type": "json_schema", "name": "web_search_results", "strict": true, "schema": ...}}`
    - `max_output_tokens`: `2000`
    - HTTP 客户端超时：**90 秒**（复用 `ai.ProxyURL`）。
  - **定向 HTTP 400 降级**（借鉴 `MusicAutoTagger/NativeWebSearchClient`）：
    - 解析 OpenAI 错误体的 `error.code` / `error.param` / `error.message`。
    - 若命中非扩展客户端错误（`model_not_found`、`invalid_model`、`invalid_api_key`、`insufficient_quota`、`context_length_exceeded` 或 `param` 为 `model`/`input`），直接失败不降级重试。
    - 若提示不支持 `include`、`search_context_size` 或 `text`/`format`/`json_schema`，仅剥离对应扩展参数重试一次（最多回退到基础 `web_search_preview` + 文本 JSON 提示）；若网关明确报不支持 `web_search_preview` 工具类型，直接判定端点不支持。
  - **真实联网强校验 + 服务端来源池过滤**：
    - 遍历响应 `output[]`：
      1. 对 `type` 为 `web_search_call` 或 `web_search` 的项，仅在 `status == ""` 或 `status == "completed"` 时采信；标记 `sawWebSearchCall = true`，采集 `action.url` 与 `action.sources[]`（兼容字符串数组与 `{url, title}` 对象数组）。
      2. 对 `message` 块，拼接 `content[].text`，同时采集 `content[].annotations[]`（兼容 `url_citation.url` 与直接 `url`）。
    - **假联网拦截**：若 `!sawWebSearchCall` 且来源池为空，返回错误（触发回退链），绝不放行模型纯记忆编造结果。
    - **URL 规范化与匹配**：实现 `canonicalizeURL(raw)`（统一小写 scheme/host、去掉 `www.`、去除尾斜杠 `/`、剔除 `utm_*`/`fbclid`/`gclid`/`ref`/`uo` 追踪参数并对剩余 query 参数排序；展示用 URL 仅清理追踪参数保留可读形式）。
    - 模型 JSON 解析出的每条 `result`：
      - 若服务端来源池非空，仅保留 `canonicalizeURL(r.URL)` 命中来源池且 `SafePageURL(cleanURL)` 为真的条目；
      - 若模型 JSON 漏出结果或解析失败但服务端来源池有合法 URL（且仅用于发现页面或补充），可将来源池中前 5 条合法 URL 补齐为仅含 URL/Title 的 `SearchResult`（至少保障头像搜图拿到真实页面）；若最终有效结果数为 0，返回空切片由 `RouterClient` 判定触发回退。
  - **错误脱敏与用量记账**：
    - 响应错误绝不把上游原始 body 或 API Key 拼入错误文案，统一映射为 `*websearch.WebError{Provider: "Native", Status: code, Operation: op}`（解析 `Retry-After` 头供限流退避）。
    - 解析 `usage`（`input_tokens`、`input_tokens_details.cached_tokens`、`output_tokens`、`output_tokens_details.reasoning_tokens`），若 `Registry` 注册了用量回调则异步/短超时写入 `ai_usage`（purpose = `"web_search_native"`）；请求前经共享 RPM 限流器等待。
- **理由**：实测证明这是目前兼容 OpenAI 官方与 `cli.032.im` 等中转站最可靠的协议形态，能够彻底规避严格 JSON 下 `annotations` 为空以及中转站静默忽略搜索参数两大陷阱。

### Decision 3: `RouterClient` 升级为有序多级回退链与不支持短冷却
- **选择**：
  - 将 `RouterClient` 的 `primary`/`fallback` 两槽重构为 `backends []namedBackend`（1–3 个），按顺序依次尝试：
    - `native_then_search`: `[Native, Tavily(如已配), DeepSeek(如已配)]`
    - `tavily_then_deepseek`: `[Tavily(如已配), DeepSeek(如已配)]`
    - `tavily`: `[Tavily]`
    - `deepseek`: `[DeepSeek]`
  - **`Search` 回退触发条件**：当前后端返回非 `ctx.Err()` 错误，**或**返回的 `len(results) == 0` 且存在下一级回退后端时，立即尝试下一级后端；若最后一级也返回 0 结果且无错，则返回空切片 `nil, nil`；若全部报错，返回首个/最有意义的错误（避免后级配置缺失掩盖前级错误）。
  - **进程内不支持短冷却**：在 `Registry` 中维护按 `(responsesURL, model)` 指纹键控的 `nativeUnsupportedUntil time.Time`：当 `NativeClient` 收到 HTTP 404 或明确不支持 `web_search_preview` 工具的 400 时，标记 10 分钟冷却；冷却期间 `RouterClient` 直接跳过 `Native` 走后级回退（若无后级回退则仍直接返回错误供上层明确感知）；当配置变更（指纹变化）或调用 `TestConnection` 时立即清空该冷却状态。
- **理由**：兼顾「搜不到自动回退搜索 API」的召回体验与批量任务遇到不支持端点时的性能保护。

### Decision 4: `Fetch(pageURL)` 采用「本地 SSRF 安全直连优先 -> 搜索 API 提取 -> 原生 `open_page` 兜底」
- **选择**：
  - 在 `internal/netfetch` 新增 `FetchPageText(ctx, client, rawURL, ua) (string, error)`：复用与 `FetchPageImageMeta` 相同的 `ValidatePublicURL`、`defaultPublicTransport()`、重定向逐跳公网重检（最多 6 跳）与响应体上限（2 MiB），校验 `Content-Type` 为 HTML/XML/text 后清洗 `<script>`/`<style>`/`<noscript>`/`<svg>` 标签、HTML 实体解码并折叠空白，提取 `<title>` + meta description + 正文文本；对 HTTP 403/429/503、明显 WAF/Cloudflare 挑战特征或有效正文不足 150 字的页面判定为直连提取失败。
  - 在 `RouterClient.Fetch(ctx, pageURL)` 中，当策略包含 `Native` 时按如下顺序执行：
    1. **本地公网直连** `netfetch.FetchPageText`（0 token、<1s、返回网页真实原文，完全满足 `artistaliases` 官网原文子串校验与 `aireview` 的 `quoteGroundedInPages`）；
    2. 直连失败时，若配置了 Tavily `/extract` 或 DeepSeek `/fetch`，优先调用它们（快速且通常为真实网页提取文本）；
    3. 若未配置任何外部搜索 API 或外部提取亦失败，调用 `NativeClient.FetchOpenPage(ctx, pageURL)` 通过 `/v1/responses` 的 `web_search_preview` 指示模型打开目标 URL 并提取主要正文文本，同时校验响应包含已完成的 `web_search_call`（`open_page`/`find_in_page` 或命中该 URL 的 `sources`）。
- **理由**：比无脑调 `open_page` 快一个数量级，同时保住中文别名和 AI 审核的原文子串防幻觉护栏，又能在纯原生无 Tavily 环境下救回 Cloudflare/JS 反爬页面。

### Decision 5: `Registry` 依赖注入与管理端/向导测试超时
- **选择**：
  - 为 `websearch.Registry` 增加轻量 setter：`SetRateWait(func(ctx context.Context, rpm int) error)` 与 `SetUsageRecorder(func(ctx context.Context, model, purpose string, usage storage.AIUsage))`（以及测试钩子 `SetNativeEndpointForTest`），在 `cmd/server/main.go` 将 `sharedAILimiter` 与 `db.AddAIUsage` 注入 `sharedWebSearch`，避免 `websearch` 与 `aiclient` 产生额外包依赖。
  - 在 [internal/http/ai_settings.go](file:///home/ubuntu/ds-workspace/032music-server/internal/http/ai_settings.go) 的 `aiPurposeLabels` 注册 `"web_search_native": "原生联网搜索"`；将 `handleAISettingsWebSearchTest` 和 `handleSetupTestWebSearch` 的测试超时从 30 秒提升至 **90 秒**；在 `setup.js` 与 `handleSetupTestWebSearch` 中补传未保存表单里的 `llmModel` 与 `llmProxyUrl`，使向导第 2 步填写完 LLM 后无需保存即可现场测试原生联网搜索。
  - 在 [internal/enrichment/ratelimit.go](file:///home/ubuntu/ds-workspace/032music-server/internal/enrichment/ratelimit.go) 的 `sourceDisplayName` 补齐 `"tavily"` -> `"Tavily 联网"`、`"native"` -> `"模型原生联网"`。

## Risks / Trade-offs

- **[Risk] 原生搜索摘要为模型转述，若 `artistaliases` 官网直连失败后走搜索摘要或 `open_page`，可能弱化官网原文精确子串自动入库护栏** → Mitigation：`Fetch` 优先本地 SSRF 安全直连获取真实 HTML 原文（绝大多数官网直连 200 成功）；在 `Search` 提示词中要求尽可能摘录网页原文关键句，同时保留既有人工审核兜底。
- **[Risk] 原生 `/v1/responses` 单次调用耗时 15–40 秒，在批量任务或网页测试按钮中可能触发上游/代理超时** → Mitigation：为原生客户端和测试接口独立设置 90 秒超时，并使用 `search_context_size: "medium"` 平衡速度与召回。
- **[Risk] 某些中转站对 `/v1/responses` 返回 200 但静默忽略 `web_search_preview` 工具** → Mitigation：硬性校验响应中必须存在已完成的 `web_search_call` 且至少命中一条规范化来源 URL，否则视作未联网失败并自动回退 Tavily/DeepSeek，且在「测试联网搜索」中明确报错提示。
- **[Risk] 本地 `FetchPageText` 存在 SSRF 或大响应体 OOM 风险** → Mitigation：复用 `internal/netfetch` 的公网 IP 校验、逐跳重定向公网复检与 2 MiB 读取硬上限，禁止携带嵌入式凭据的非 `http`/`https` 地址。

## Migration Plan

- **数据库迁移**：新增 `internal/storage/migrations/096_native_web_search_provider.sql`，重建 `web_search_settings` 放宽 `provider` 的 `CHECK` 约束并重建 `ai_usage` 为 `purpose` 放宽新增 `'web_search_native'`，完整迁回原有行数据（不改变默认策略 `tavily_then_deepseek`），对既有实例 100% 无损兼容。
- **回滚策略**：管理员随时可在 AI 设置中心将策略切回 `tavily_then_deepseek` / `tavily` / `deepseek`，不触碰任何既有密钥表或媒体库数据。
