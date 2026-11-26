# Tasks

## 1. 数据库迁移与持久化队列存储

- [x] 1.1 新增迁移 `internal/storage/migrations/093_artist_image_auto_queue.sql`：为 `artist_image_settings` 新增 `auto_ingest_ai_enabled`（默认 `1`），创建 `artist_image_auto_queue` 持久化队列小表及索引；更新 `internal/storage/artist_image_comprehensive.go` 读写 `AutoIngestAIEnabled` 并在 `SaveArtistImageBackfillGuarded` / 新辅助方法中支持写入 VLM 合格图的 `artist_image_reviews(verdict='keep')` 及 VLM 拒收图拉黑记录；编写迁移测试验证空库与 092 升级 093 均成功（`go test ./internal/storage -run "TestMigration09|ArtistImage"`）。
- [x] 1.2 新增 `internal/storage/artist_image_auto_queue.go`：实现 `EnqueueArtistImageAutoQueue`（去重入队，仅针对未合并、无自定义头像且具 MusicBrainz 身份艺人）、`ClaimNextArtistImageAutoQueueItem`（按 `next_retry_at` 与 `created_at` 认领下一项）、`CompleteArtistImageAutoQueueItem`（完成后删除行）、`RetryArtistImageAutoQueueItem`（递增 `attempts` 并写退避 `next_retry_at`，超过 3 次置 `failed`）、`RecoverArtistImageAutoQueue`（启动复位残留 `running` 为 `pending`）及 `ArtistImageAutoQueueStats`；补齐队列存储与并发认领单元测试（`go test ./internal/storage -run "TestArtistImageAutoQueue"`）。

## 2. 统一全链路 VLM 视觉准入引擎与异步后台队列 Worker

- [x] 2.1 在 `internal/enrichment/artist_image_ai.go` 与 `internal/enrichment/artist_image_backfill.go` 中实现统一的 `acquireArtistAvatarWithAIGate` 缺图获取与 VLM 准入闸门：覆盖 Tier 1（MB/Last.fm）、Tier 2（Fanart/Deezer/Bangumi）与 Tier 3（Tavily/DeepSeek + DOM 抽图 + AI 选图），当启用 VLM 质检时所有图源下载的候选均强制过 VLM（高置信合格自动落库并写 `keep` 防重审，不合格立即丢弃并写入 `artist_image_rejections` 续跑下一源，中置信送「待审头像」，VLM 400/解析失败非静默报错）；将其接入手动补全任务的缺图分支与自动入库队列，编写单元测试验证 Tier 1/2 拦截专辑封面并顺延联网命中真头像（`go test -race ./internal/enrichment -run "TestArtistImage.*Gate|TestArtistImageAudit"`）。
- [x] 2.2 新增 `internal/enrichment/artist_image_auto_queue.go` 并在 `internal/enrichment/manager.go`、`artist_runtime.go`、`artist_image_backfill.go` 与 `cmd/server/main.go` 完成接入：实现常驻串行 `autoImageWorker`（启动恢复、按事件与重试时间唤醒、与全量补全任务和手动找图队列解耦、共享 `aiLimiter` 与 `TavilyKeyRing`），将规则自动匹配（`executeArtistRun`）、Inline AI 确认（`EnrichAutoConfirmedArtist`）与人工确认统一改为调用 `QueueAutoConfirmedArtistImage`；对已确认 MusicBrainz 身份的全角色艺人同时支持「缺图走全链路质检补图」与「未审旧缓存图顺手过一次 VLM 审查及 1.A 替换/清理」，自定义头像与已 `keep` 头像零成本跳过；编写并发安全单测覆盖自动入队、未审旧图顺手审查、退避重试与重启恢复（`go test -race ./internal/enrichment -run "TestArtistImageAutoQueue"`）。

## 3. HTTP 设置项、异步人工确认与管理页实时队列状态

- [x] 3.1 更新 `internal/http/identity.go`、`internal/http/artist_image_backfill.go`、`internal/http/app.go`、`internal/http/templates/match-review.html` 与 `internal/http/assets/admin.js`：人工确认身份 `confirmArtistCandidate` 改为异步入队并立即重定向返回；头像设置弹窗与 `handleSaveArtistImageSettings` / `handleGetArtistImageSettings` 新增 `auto_ingest_ai_enabled` 开关；新增 `GET /admin/matches/images/auto-queue.json`（管理员鉴权 + `quietAccessLogPath`）并在 `/admin/matches` 顶部任务概览芯片中展示实时自动补图进度；补齐 HTTP 测试验证设置保存、人工确认非阻塞入队及状态端点鉴权（`go test ./internal/http -run "TestArtistImage|TestConfirmArtist"`）。

## 4. 整库集成回归验证

- [x] 4.1 编写跨层端到端集成测试，串联「新曲入库确认 MusicBrainz 身份（含主唱与幕后角色）→ 后台自动队列异步触发 → Tier 2 专辑封面被 VLM 拦截拉黑 → Tier 3 Tavily 联网命中高置信人像入库并写 `keep` 防重审 → 已审老图零消耗跳过 / 未审旧图顺手 VLM 审查替换」，并运行全量测试、编译与严格规格校验（`go test ./... && go build ./... && node --check internal/http/assets/admin.js && openspec validate "artist-ingest-ai-avatar-pipeline" --strict`）。
