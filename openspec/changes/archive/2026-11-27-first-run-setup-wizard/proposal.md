# Proposal

## Why

当前 032 Music Server 在全新部署启动后存在两项明显体验断层：一方面，服务端在 `main.go` 启动瞬间即执行首次全库扫描并触发 `SetOnComplete` 回调，此时所有外部元数据来源（MusicBrainz、Bangumi、Last.fm）、LLM、联网搜索与后台 AI 能力仍处于默认关闭状态，导致首次入库无法直接走完元数据与 AI 增强链路；另一方面，模型密钥、Tavily/DeepSeek 联网 Key、Fanart.tv Key、Scrobble、Jackett/qBittorrent 以及各后台能力开关散落在 7 个独立页面与审核弹窗中，新用户部署登录后缺乏统一引导。

## What Changes

- **登录后触发的独立专注态 5 步初始化引导（`/admin/setup`）**
  - 保持现有 `.env` 启动凭据安全契约不变（不引入登录前免密初始化），管理员首次登录成功后自动进入独立专注态引导页（沿用 `DESIGN.md` 与 `tokens.css` 深浅双主题，隐藏侧栏与底栏播放器，支持桌面双栏与 ≤900px 移动端适配，交互基准见 `prototypes/first-run-setup.html`）。
  - 5 步直达编排（每步独立保存、支持回跳修改与单步跳过，高级参数默认折叠）：
    1. **元数据与图源**：MusicBrainz（启用时校验联系邮箱/项目地址）、Bangumi、Last.fm API Key、歌手简介目标语言与 Wikipedia；折叠区收拢 Fanart.tv v3 API Key、结构化图源与缓存天数。
    2. **LLM 与联网搜索**：OpenAI 兼容服务商快选预设（Gemini / DeepSeek / OpenAI / 通义千问 / OpenRouter / Ollama / 自定义）、Base URL / API Key / 主模型 / 复核模型及高级参数折叠区（后备模型、代理、RPM、并发、思考强度、头像 VLM 视觉模型）；统一联网搜索（路由策略、Tavily 多 Key 轮询、DeepSeek 官方 Key 自动复用提示）；提供页内异步 JSON 连通性测试端点。
    3. **自动化能力**：实时展示前置条件就绪状态并提供「纯本地保守 / ACG 推荐组合 / 全自动拉满」预设；按前置条件动态解锁 AI 初审（建议/自治模式与联网补证）、中文别名补全、头像联网兜底与新艺人 AI 质检、简介 AI 翻译、AI 歌词翻译、每日推荐、AI 曲库打标（带批量消耗提示）及本地声学分析（依赖 `music-analyzer` Sidecar 检测）。
    4. **同步与资源获取（可选）**：ListenBrainz 一键验证连接、Last.fm Scrobble（自动复用第 1 步 API Key + Shared Secret + 两步网页授权）、Jackett 与 qBittorrent 配置及页内连接测试。
    5. **曲库目录与启动首次扫描**：快速只读预检音乐目录挂载可读性（支持重新检测）、配置自动入库监控与轮询间隔、可视化展示扫库后自动串联的增强流水线并允许开关控制、汇总前 4 步配置支持一键修改；提供「完成配置并启动首次扫描」与「仅保存配置，稍后手动扫描」。
- **首次扫描暂缓与释放编排**
  - 新增持久化状态表 `setup_wizard_state`（迁移 `094_setup_wizard.sql`），记录 `status`（`pending` / `skipped` / `completed`）、`current_step`、`step_states_json`、`banner_dismissed` 与 `auto_enrich_after_first_scan`。
  - 全新部署在状态为 `pending` 时暂缓 `cmd/server/main.go` 启动自动扫描与目录轮询监控，直至用户在第 5 步点击启动扫描或主动点击「暂不配置，直接扫描入库」时立即释放监控器并触发首次扫描，让首次入库一次性走通已配置的元数据与后台增强流水线。
  - 老实例升级迁移时，若数据库已存在任意音频文件、历史扫描记录、已启用元数据源或已配置的 AI / 联网搜索 / 获取密钥，自动将向导状态初始化为 `completed`，保持启动即扫且不拦截登录。
- **异常提醒按需浮现与软引导续配**
  - 仅在检测到真实部署提醒（如未受信反向代理 `TRUSTED_PROXIES`、临时随机 `MEDIA_TOKEN`）时在向导内容区顶部显示提示条，健康部署不增加额外只读步骤。
  - 中途跳过向导后，控制台总览顶部显示可关闭的续配提示条（展示剩余步骤与继续入口），且管理后台始终保留「重新打开初始化向导」入口。

## Capabilities

### New Capabilities
- `first-run-setup-wizard`: 管理员首次登录后的 5 步初始化引导流程、配置向导状态持久化与老实例升级判定、页内异步连通性测试接口、目录只读预检、跳过/续配/重入机制，以及全新部署暂缓首次自动扫描直至向导完成或跳过的启动编排。

### Modified Capabilities
<!-- 无既有 capability 的 spec 级需求发生破坏性或契约变更；现有各设置表、扫描引擎安全护栏与登录认证契约均保持向后兼容。 -->

## Impact

- **存储与迁移**：新增 `internal/storage/migrations/094_setup_wizard.sql`、`internal/storage/setup_wizard.go` 及单元测试；兼容既有 SQLite STRICT 与备份快照表结构校验。
- **启动与扫描编排**：`cmd/server/main.go`、`internal/scanner/watcher.go`（支持按初始化状态延迟释放首次扫描与轮询监控，以及首次扫描完成后按选项决定是否自动触发下游增强）。
- **HTTP 路由、接口与前端资源**：新增 `internal/http/setup_wizard.go`（页面渲染、分步保存 API、目录预检 API、页内异步测试 JSON 接口、完成/跳过/关闭提示条接口）、`internal/http/templates/setup.html`、更新 `internal/http/app.go`（登录后首跳路由与路由注册）、`internal/http/templates/dashboard.html`（续配提示条与重开向导入口）、以及配套样式/脚本资产。
- **外部依赖与兼容性**：无新增外部依赖；全部表单写入复用既有 `metadata_source_settings`、`artist_biography_settings`、`artist_image_settings`、`ai_settings`、`ai_review_settings`、`web_search_settings`、`artist_alias_settings`、`daily_mix_settings`、`audio_analysis_settings`、`lastfm_scrobble_settings`、`listenbrainz_settings`、`acquisition_settings` 与 `library_watch_settings` 表结构，向后兼容所有既有单页设置和 JSON API。
