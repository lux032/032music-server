# Spec Delta

## MODIFIED Requirements

### Requirement: 支持 Tavily 搜索与正文提取并平滑兼容 DeepSeek
系统 SHALL 实现 Tavily 搜索（`POST /search`）与网页提取（`POST /extract`）客户端，复用全局代理配置并支持 `native_then_search`、`tavily_then_deepseek`、`tavily` 与 `deepseek` 四种路由策略；升级后未重新配置时 SHALL 自动兼容既有 DeepSeek 联网密钥与原有路由策略。

#### Scenario: Tavily 搜索与提取返回标准化结果
- **WHEN** 路由策略选择 Tavily 且存在可用 Tavily Key，调用方执行 `Search` 或 `Fetch`
- **THEN** 客户端以 `Authorization: Bearer <key>` 向 Tavily `/search` 或 `/extract` 发起请求，并返回标准化的 `SearchResult` 列表或提取后的网页正文

#### Scenario: Tavily 不可用时按回退策略自动切换至 DeepSeek
- **WHEN** 路由策略为 `tavily_then_deepseek`，且未配置 Tavily Key 或全部 Tavily Key 均处于失效/冷却状态，但已配置有效 DeepSeek 联网 Key
- **THEN** 路由器自动回退调用 DeepSeek 联网接口完成本次搜索或网页读取

#### Scenario: 升级后自动继承原有中文别名模块中的 DeepSeek 密钥
- **WHEN** 历史实例在升级前仅在 `artist_alias_settings.deepseek_key` 中保存过 DeepSeek 联网密钥
- **THEN** 迁移后系统自动保留并继续使用该 DeepSeek 密钥提供联网能力，既有依赖 DeepSeek 联网的模块无需人工重新录入即可正常工作

## ADDED Requirements

### Requirement: 支持复用全局 AI 的 OpenAI 原生联网搜索与来源池校验
当路由策略为 `native_then_search` 且全局 AI 配置（接口地址、API Key、主模型）已填写时，系统 SHALL 直接复用全局 AI 的接口地址、API Key、主模型、代理与共享 RPM 限流配置，通过 OpenAI `/v1/responses` 接口调用 `web_search_preview` 原生联网搜索，并基于服务端真实来源池对输出 URL 执行防幻觉过滤与用量记账。

#### Scenario: 归一化全局接口地址并采集服务端来源池过滤结果
- **WHEN** 全局 AI 接口地址配置为 `https://example.com/v1`、`https://example.com/v1/chat/completions` 或 `https://example.com/v1/responses`，系统发起原生联网搜索，且上游响应在 `web_search_call` 中返回已完成状态与 `action.sources`（或 `open_page`/`find_in_page` 的 `action.url`、`annotations`）
- **THEN** 系统自动向 `https://example.com/v1/responses` 发送携带 `web_search_preview` 工具与 `include: ["web_search_call.action.sources"]` 的请求
- **AND** 将模型输出结果的 URL 规范化（统一小写主机名、去掉 `www.`、尾斜杠及 `utm_*`/`fbclid`/`gclid` 等追踪参数）后与服务端真实来源池比对，仅保留命中来源池且通过安全校验的干净 URL，丢弃模型编造的未命中链接
- **AND** 将 `/v1/responses` 返回的 `input_tokens`、`cached_tokens`、`output_tokens`、`reasoning_tokens` 记入全局 `ai_usage` 统计

#### Scenario: 上游网关拒绝扩展参数时定向降级重试
- **WHEN** 第三方 OpenAI `/v1/responses` 兼容网关因不支持 `include`、`search_context_size` 或 `text.format` 返回 HTTP 400
- **THEN** 系统仅移除被拒绝的扩展参数重试，而不对 `model_not_found`、`invalid_api_key`、`context_length_exceeded` 等非扩展客户端错误重复发起无效降级重试

#### Scenario: 拒绝未真实触发联网搜索的假成功响应
- **WHEN** 上游 `/v1/responses` 返回 HTTP 200，但响应中不包含任何已完成的 `web_search_call` 且未返回任何服务端来源 URL 或引用注解
- **THEN** 系统将该响应判定为未执行真实联网搜索的失败，不采信模型仅凭记忆生成的链接

### Requirement: 原生优先路由支持自动回退、分层网页提取与免搜索密钥就绪
系统 SHALL 在 `native_then_search` 策略下按「模型原生 -> Tavily -> DeepSeek」的顺序执行自动回退；单页提取 SHALL 优先采用 SSRF 安全的本地公网直连抓取以保留真实网页原文；无需额外配置 Tavily 或 DeepSeek 密钥即可在管理端与初始化向导中判定联网搜索已就绪并完成连通性测试。

#### Scenario: 原生搜索失败或有效结果为零时自动回退搜索 API
- **WHEN** 路由策略为 `native_then_search`，同时已配置 Tavily 或 DeepSeek 联网凭据，且原生搜索因接口报错（4xx/5xx/超时）、未触发真实搜索或经来源池过滤后有效结果数为 0 而未返回可用结果
- **THEN** 路由器在同一次调用内自动回退至 Tavily（若可用）或 DeepSeek 完成本次搜索并返回结果

#### Scenario: 端点明确不支持原生搜索接口时短冷却防空转
- **WHEN** 在 `native_then_search` 策略下全局 AI 端点对 `/v1/responses` 返回 HTTP 404 或明确不支持 `web_search_preview` 工具，且已配置回退搜索 API
- **THEN** 路由器在进程内对该端点记录短冷却窗口，窗口内的后续请求直接跳过原生路径走回退引擎，且在管理员重新保存配置或主动触发联网搜索测试时立即清除该冷却状态

#### Scenario: 单页提取优先本地公网直连并按需降级提取器或原生打开页面
- **WHEN** 调用方在 `native_then_search` 策略下对合法公网 URL 调用 `Fetch`
- **THEN** 系统优先通过带 SSRF 防护与体积上限的本地公网直连抓取 HTML 并提取正文纯文本返回，以保留原始网页文本并避免额外搜索开销
- **AND** 若直连遭遇反爬拦截（如 HTTP 403/429/503）或返回空壳页面，则优先回退已配置的 Tavily `/extract` 或 DeepSeek `/fetch`，若均未配置则使用原生 `/v1/responses` 打开页面兜底提取

#### Scenario: 仅配置全局 AI 时即可判定就绪并验证原生联网连通性
- **WHEN** 管理员未配置任何 Tavily Key 或 DeepSeek 联网 Key，但已完整配置全局 AI（接口地址、API Key、主模型）并将联网搜索策略设为 `native_then_search`
- **THEN** AI 设置页与初始化向导均将联网搜索判定为已就绪，允许启用头像联网、简介联网、中文别名补全与 AI 审核联网补证
- **AND** 管理员点击「测试联网搜索」时服务端使用适配原生检索耗时的超时窗口向 `/v1/responses` 发起受控探测，确认真实触发 `web_search_call` 并返回脱敏后的结果摘要
