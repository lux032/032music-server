# Proposal

## Why

当前 032 Music Server 在新曲扫描入库、自动匹配或人工确认艺术家 MusicBrainz 身份后，头像获取仍停留在旧的同步 Tier 1–2 直写逻辑：既没有经过 VLM 视觉质检（导致专辑封面、占位符等错图在新曲入库当天便持续污染缓存），也没有接入已建好的 Tavily/DeepSeek 联网兜底选图，且人工确认匹配甚至跳过了 Tier 2 结构化图源并阻塞 HTTP 响应。与此同时，手动补全任务处理「缺图艺人」的 Tier 1–2 路径也未统一接上 VLM 准入闸门，导致全库仍存在“历史审查严把关、新图入库放烂图”的双重标准。

## What Changes

- **统一全链路 VLM 视觉准入闸门（入口即审查）**
  - 抽象统一的单艺人头像获取引擎 `acquireArtistAvatarWithAIGate`，覆盖 Tier 1（MusicBrainz/Last.fm）、Tier 2（Fanart/Deezer/Bangumi）与 Tier 3（Tavily/DeepSeek 联网搜索 + DOM 抽图 + AI 选图）。
  - 无论来自新确认艺人的自动补图还是手动补全任务的缺图分支，只要启用 VLM 视觉质检，Tier 1 / Tier 2 / Tier 3 获取的每一张候选图在落库前均须通过 VLM 视觉验收：高置信合格图自动落库并同步写入 `artist_image_reviews(verdict='keep')` 防重复审查；不合格图（专辑封面、Logo、占位符、糊图、错人）当场丢弃并拉黑，自动续跑下一图源或联网兜底；中置信合格图不强行落库，改为送入「待审头像」供人工拍板。
- **新确认 MusicBrainz 身份艺人的持久化异步自动补图与审查队列（迁移 `093_artist_image_auto_queue.sql`）**
  - 新增轻量持久化队列小表 `artist_image_auto_queue` 与设置开关 `artist_image_settings.auto_ingest_ai_enabled`（默认开启）。仅由新确认 MusicBrainz 身份事件触发入队（涵盖规则自动匹配、Inline AI 自动确认、复合署名自动拆分后匹配确认及人工点击「确认匹配」，覆盖主唱、专辑艺人及幕后词曲编曲等**全部角色艺人**），绝不误拉全库历史旧缺图。
  - 将原先同步阻塞身份匹配循环与 HTTP 确认请求的头像拉取解耦为后台异步串行 Worker（共享全局 `aiLimiter` 与 `TavilyKeyRing`，不受手动全量任务的 `max_web_requests_per_run` 配额限制，服务重启自动恢复未完成队列，瞬时超时/429 最多退避重试 3 次且不误写 30 天负缓存）。
  - 兼顾「缺图补全」与「未审旧图顺手把关」：对刚确认 MusicBrainz 身份的艺人，若存在自定义头像或当前缓存图哈希已有 `keep` 审查记录则零成本跳过；若本地无头像则走全链路 VLM 质检补图；若本地已有缓存图但从未经过 AI 审查（无 `artist_image_reviews` 记录），则借新获得的 MusicBrainz 锚点顺手执行一次 VLM 审查与高置信自动替换（遵循 1.A 差异化清理策略）。
- **管理后台设置与实时状态可见性**
  - 在「头像补全数据源与 AI 策略设置」弹窗新增「新确认身份艺人自动 AI 质检与联网补全」开关。
  - 新增 `GET /admin/matches/images/auto-queue.json` 状态端点（纳入 `quietAccessLogPath`），在 `/admin/matches` 顶部任务概览芯片中展示后台自动补图进度（如 `自动补图 2/5`），让管理员可实时观测后台自动队列运行情况。

## Capabilities

### New Capabilities

<!-- 本变更扩展既有 `artist-avatar-ai-audit` 能力边界，不另建碎片化新 capability。 -->

### Modified Capabilities

- `artist-avatar-ai-audit`: 新增全链路（Tier 1–3）统一 VLM 视觉准入闸门、新确认 MusicBrainz 身份全角色艺人的持久化异步自动补图与未审旧图顺手审查队列、瞬时错误退避重试及管理页开关与实时状态可见性要求。

## Impact

- **存储与迁移**：`internal/storage/migrations/093_artist_image_auto_queue.sql`（新增）、`internal/storage/artist_image_comprehensive.go`、`internal/storage/artist_image_auto_queue.go`（新增）。
- **富化与异步队列引擎**：`internal/enrichment/artist_image_ai.go`、`internal/enrichment/artist_image_backfill.go`、`internal/enrichment/artist_image_auto_queue.go`（新增）、`internal/enrichment/artist_runtime.go`、`internal/enrichment/manager.go`。
- **HTTP 与管理后台 UI**：`internal/http/artist_image_backfill.go`、`internal/http/identity.go`、`internal/http/app.go`、`internal/http/templates/match-review.html`、`internal/http/assets/admin.js`。
- **外部依赖与兼容性**：无新增外部依赖；复用既有 `internal/websearch`、`aiclient.Limiter` 与 `artist_image_reviews` / `artist_image_rejections` / `artist_image_audit_actions` 数据模型；向后兼容所有既有 API 与自定义头像保护边界。
