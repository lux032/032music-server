# Spec Delta

## Purpose

定义服务端统一联网搜索路由层的行为规范，涵盖 Tavily 网页搜索与正文提取、多 API Key 进程内共享轮询与按状态码自动故障转移、DeepSeek 兼容回退策略、AI 设置中心集中管理与密钥安全脱敏。

## ADDED Requirements

### Requirement: 全库联网功能统一收口至共享联网搜索路由
系统 SHALL 将艺术家头像联网兜底与审查重抓、歌手简介联网概括、歌手中文别名补全以及 AI 审核联网补证四个模块统一接入 `internal/websearch` 共享路由层，对外提供一致的 `Search` 与 `Fetch` 接口及安全 URL 校验，且不再在业务模块中维护独立的搜索客户端实现。

#### Scenario: 四大业务模块共用同一套联网配置与轮询状态
- **WHEN** 管理员在 AI 设置中心配置 Tavily 多 Key 后，先后触发头像联网搜图、歌手简介联网、中文别名补全或 AI 审核联网补证
- **THEN** 四个模块均通过进程内共享的 `websearch` 路由器发起请求，并共享同一套 Tavily 轮询游标与 Key 健康冷却状态

#### Scenario: 拒绝非公网或携带凭据的非法网页地址
- **WHEN** 任意调用方向共享搜索路由的 `Fetch` 或结果过滤传入非 `http`/`https` 协议或包含嵌入式账号密码的 URL
- **THEN** 系统拒绝处理该 URL，不向外发起携带敏感凭据或不安全协议的请求

### Requirement: 支持 Tavily 搜索与正文提取并平滑兼容 DeepSeek
系统 SHALL 实现 Tavily 搜索（`POST /search`）与网页提取（`POST /extract`）客户端，复用全局代理配置并支持 `tavily`、`deepseek` 与 `tavily_then_deepseek` 三种路由策略；升级后未重新配置时 SHALL 自动兼容既有 DeepSeek 联网密钥。

#### Scenario: Tavily 搜索与提取返回标准化结果
- **WHEN** 路由策略选择 Tavily 且存在可用 Tavily Key，调用方执行 `Search` 或 `Fetch`
- **THEN** 客户端以 `Authorization: Bearer <key>` 向 Tavily `/search` 或 `/extract` 发起请求，并返回标准化的 `SearchResult` 列表或提取后的网页正文

#### Scenario: Tavily 不可用时按回退策略自动切换至 DeepSeek
- **WHEN** 路由策略为 `tavily_then_deepseek`，且未配置 Tavily Key 或全部 Tavily Key 均处于失效/冷却状态，但已配置有效 DeepSeek 联网 Key
- **THEN** 路由器自动回退调用 DeepSeek 联网接口完成本次搜索或网页读取

#### Scenario: 升级后自动继承原有中文别名模块中的 DeepSeek 密钥
- **WHEN** 历史实例在升级前仅在 `artist_alias_settings.deepseek_key` 中保存过 DeepSeek 联网密钥
- **THEN** 迁移后系统自动保留并继续使用该 DeepSeek 密钥提供联网能力，既有依赖 DeepSeek 联网的模块无需人工重新录入即可正常工作

### Requirement: Tavily 多 Key 进程内共享轮询与自动故障转移
系统 SHALL 将配置的多把 Tavily API Key 组织为进程内共享轮询池，按 Round-Robin 顺序均衡分发请求；当单把 Key 发生限流、额度耗尽、鉴权失败或服务端瞬时异常时 SHALL 按错误类型标记状态并自动切换下一把健康 Key 重试。

#### Scenario: 多把健康 Key 按 Round-Robin 均衡轮询
- **WHEN** 系统配置了 3 把健康 Tavily Key 并连续发起 3 次搜索请求
- **THEN** 轮询池依次将 3 次请求分发至 Key 1、Key 2 与 Key 3，配置未变化时游标在多次业务调用间持续保持

#### Scenario: 单把 Key 触发限流或额度耗尽时自动切往下一把 Key
- **WHEN** 当前选中的 Key 1 返回 HTTP 429（限流）或 HTTP 402/432/433（额度耗尽），而 Key 2 处于健康状态
- **THEN** 轮询池将 Key 1 分别置入短冷却或长冷却状态，并在同一次调用内立即使用 Key 2 重试成功返回结果

#### Scenario: 单把 Key 鉴权失效时隔离该 Key 且不阻断其余健康 Key
- **WHEN** Key 1 在搜索请求中返回 HTTP 401 或 HTTP 403，而 Key 2 处于健康状态
- **THEN** 轮询池将 Key 1 标记为失效（直至管理员重新保存配置），并自动切换至 Key 2 完成请求

#### Scenario: 网页提取因目标站不可访问失败时不误伤健康 Key
- **WHEN** 使用健康 Key 调用 Tavily `/extract` 抓取单个受限或不存在的网页返回目标页读取失败
- **THEN** 系统将该错误视为单页抓取失败返回调用方，而不将该 Key 标记为失效或额度耗尽

#### Scenario: 全部 Key 不可用时返回可识别的汇总状态
- **WHEN** 池内所有 Tavily Key 均处于冷却、额度耗尽或失效状态且无可用回退提供商
- **THEN** 系统返回对应分类错误（全员冷却返回 429 供任务退避，全员耗尽或失效返回 432/401 供任务暂停提示），且不陷入无限重试循环

### Requirement: AI 设置中心集中管理联网搜索并严格脱敏密钥
系统 SHALL 在 AI 设置中心集中提供联网搜索提供商策略、多行 Tavily Key 录入（支持追加、替换、清空与自动去重）、DeepSeek 联网 Key 配置及一键连接测试；页面展示、错误日志与数据库备份恢复 SHALL 严防明文密钥泄露或复活。

#### Scenario: 页面仅展示密钥掩码与健康状态而不回显明文
- **WHEN** 管理员保存多把 Tavily Key 后刷新 `/admin/settings/ai` 页面
- **THEN** 页面展示已配置 Key 总数与每把 Key 的掩码标识及当前健康状态（正常/冷却中/额度耗尽/失效），响应 HTML 与 JSON 中绝不包含任何明文 API Key

#### Scenario: 管理员一键测试联网搜索连通性
- **WHEN** 管理员在 AI 设置中心触发联网搜索连接测试
- **THEN** 服务端使用当前配置的提供商与 Key 发起受控探测并返回脱敏后的状态摘要（成功或各失效/冷却原因），不在提示或日志中输出密钥明文

#### Scenario: 数据库备份恢复保护当前联网搜索密钥不被旧快照覆盖
- **WHEN** 管理员执行数据库快照恢复，且快照中包含与当前实例不同的 `web_search_settings` 密钥记录
- **THEN** 恢复流程保留当前实例的 `tavily_keys_json` 与 `deepseek_key`（若当前为空则保持为空），绝不复活历史备份中的旧联网密钥
