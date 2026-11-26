# Proposal

## Why

当前 032 曲库的艺术家头像补全仅覆盖「无头像」的缺图艺人，无法对历史存量缓存头像做 AI 视觉复检与坏图自动替换；同时现有 VLM 质检仅覆盖 Tier 3 联网路径且遇 `400` 静默放行、缺乏置信度与问题分类、也没有可逆替换快照与缓存版本刷新。此外，全库 4 处联网功能（头像兜底、歌手简介联网概括、中文别名补全、AI 审核联网补证）分散且写死 DeepSeek 单 Key，既无法接入 Tavily，也无法通过多 Key 轮询分摊额度与避免单 Key 限流/耗尽导致的任务中断。

## What Changes

- **统一联网搜索服务与 Tavily 多 Key 轮询**
  - 抽象并统一全库联网搜索层 `internal/websearch`，将头像补全、歌手简介联网、中文别名补全、AI 审核联网补证 4 个模块全部收口至共享路由，移除 `artistaliases` / `aireview` 中的重复 DeepSeek 客户端实现。
  - 新增 Tavily 搜索与网页正文提取客户端（`POST /search` 与 `POST /extract`），支持配置多把 API Key 组成进程内共享轮询池（Round-Robin），按状态码自动区分限流冷却（429）、额度耗尽长冷却（402/432/433）、鉴权失效停用（401/403）与瞬时故障（5xx/超时），并无缝切往下一把健康 Key 重试。
  - 在「AI 设置中心」（`/admin/settings/ai`）新增统一的「联网搜索服务」配置区，支持选择提供商策略（`tavily` / `deepseek` / `tavily_then_deepseek`）、多行录入 Tavily Keys（页面仅显示掩码与健康状态，绝不明文回显）、一键测试搜索连接，并平滑兼容旧 `artist_alias_settings.deepseek_key` 配置与备份恢复密钥脱敏保护。
- **全量历史头像 AI 视觉审查与高置信自动获取替换**
  - 新增 SQLite 迁移 `091_artist_image_audit_and_web_search.sql`，新增统一联网搜索配置表、头像审查记录表（按 `content_hash` 幂等跳过已审通过项）、头像拒绝哈希/URL 表与头像自动替换/清理审计快照表，并扩展 `artist_image_backfill_runs` / `artist_image_backfill_items` 模式与结果计数以及 `ai_usage` 用途枚举。
  - 扩展现有持久化头像任务框架，新增全量审查模式（`audit_all` 与强制重审模式 `audit_force`）：对全库无自定义头像的艺人，缺图走补全、有缓存图则先经本地损坏校验再调用全局多模态主模型/视觉模型做结构化审查，输出 `verdict`（`keep`/`replace`）、`issueType`（`none`/`garbage`/`album_cover`/`low_quality`/`wrong_subject`）、`confidence`（0–100）与中文理由。
  - 高置信判定不合格（`confidence >= 85`）时，自动将旧图 `remote_url` 与 `content_hash` 纳入排除集并执行多源重抓（Tier 1 → Tier 2 → Tier 3），**重抓获得的全部候选图（含结构化图源）均经 VLM 质量复核**；获得高置信合格新图时自动落库替换、拉黑旧图并写入撤销快照；仅获中置信候选时保留旧图并在「待审头像」呈现「旧图 vs 新图」对比卡片。
  - 高置信判错但全源无合适替代图时按差异化策略处理：明确垃圾图（`garbage`）自动清空旧缓存（含合并源艺人继承缓存）并记录撤销快照；其余类型（`album_cover` / `low_quality` / `wrong_subject`）先保留旧图并推送至「待审头像」供人工处理。中/低置信疑点旧图默认不自动消耗联网额度，保留旧图并送待审，提供按需搜图/保留/清空操作。
  - 新增自动替换与自动清理记录的一键撤销能力（复原 `artist_image_cache` 对应行，含合并继承归属），补充自动缓存头像 URL 的 `?v=<content_hash>` 缓存版本参数使替换后前端即时生效，修正 `llm.go` 对不接受 `temperature: 0` 新型 GPT 模型的自动兼容重试，并取消 VLM 审查遇 `400` 时的静默放行。

## Capabilities

### New Capabilities

- `web-search-routing`: 全局统一联网搜索路由、Tavily 搜索与网页提取、多 Key 进程内共享轮询与故障冷却转移、DeepSeek 兼容回退策略、AI 设置中心集中管理与密钥安全脱敏。
- `artist-avatar-ai-audit`: 艺术家全量存量缓存头像 AI 视觉审查、问题类型分级、高置信自动获取替换与差异化清理、Tier 1–3 双黑名单与全量 VLM 校验、按 `content_hash` 幂等防重审、一键撤销快照、缓存 URL 版本刷新及 LLM 温度兼容与非静默失败处理。

### Modified Capabilities

<!-- 无既有 spec 需求变更；既有 `write-lock-and-cache-resilience`、`background-candidate-scheduling`、`media-and-home-http-serving` 约束将在实现中继续严格遵守。 -->

## Impact

- **存储与迁移**：`internal/storage/migrations/091_artist_image_audit_and_web_search.sql`（新增）、`internal/storage/web_search.go`（新增）、`internal/storage/artist_image_comprehensive.go`、`internal/storage/artist_image_backfill.go`、`internal/storage/track_extras.go`、`internal/storage/backup_restore.go`。
- **联网搜索层**：`internal/websearch/`（重构并新增 Tavily 客户端、共享 KeyRing 轮询池与路由）、`internal/artistaliases/`、`internal/aireview/`、`internal/enrichment/artist_biography_ai.go`、`internal/enrichment/artist_image_ai.go`。
- **LLM 与头像审查流水线**：`internal/tagging/llm.go`、`internal/enrichment/artist_image_ai.go`、`internal/enrichment/artist_image_backfill.go`、`internal/enrichment/manager.go`、`cmd/server/main.go`。
- **HTTP 与管理后台 UI**：`internal/http/ai_settings.go`、`internal/http/artist_image_backfill.go`、`internal/http/artist_aliases.go`、`internal/http/app.go`、`internal/http/templates/ai-settings.html`、`internal/http/templates/match-review.html`、`internal/http/templates/artist-aliases.html`、`internal/http/assets/admin.js`。
- **外部依赖**：无新增 Go 第三方库依赖，使用标准库 `net/http` 调用 Tavily REST API（`https://api.tavily.com/search`、`/extract`），兼容现有代理配置。
