# Tasks

## 1. 数据库迁移与存储层基础

- [x] 1.1 新增迁移 `internal/storage/migrations/091_artist_image_audit_and_web_search.sql`：创建 `web_search_settings`（并从 `artist_alias_settings.deepseek_key` 无损迁移既有密钥）、按外键安全顺序重建 `artist_image_backfill_runs`（扩展 `mode` 与 `kept_items`/`replaced_items`/`cleared_items`）及 `artist_image_backfill_items`（扩展 `outcome`）、创建 `artist_image_reviews`、`artist_image_rejections`、`artist_image_audit_actions`，扩展 `artist_image_settings.audit_auto_confidence`、`artist_image_candidates`（`kind`/`issue_type`/`confidence_score`）与 `ai_usage`（新增 `artist_image_audit`）；新增迁移测试验证空库与含历史任务/密钥旧库升级均成功（`go test ./internal/storage -run TestMigrate`）。
- [x] 1.2 新增 `internal/storage/web_search.go` 实现 `WebSearchSettings` 读写、多行 Tavily Key 解析去重与掩码辅助方法，并将 `web_search_settings`（`tavily_keys_json`、`deepseek_key`）加入 `internal/storage/backup_restore.go` 凭据保护列表；补齐单测验证读写、历史 DeepSeek Key 兼容及快照恢复不复活旧密钥（`go test ./internal/storage -run "WebSearch|Backup"`）。
- [x] 1.3 扩展 `internal/storage/artist_image_backfill.go` 与 `internal/storage/artist_image_comprehensive.go`：实现 `audit_all` / `audit_force` 批量候选扫描（保持索引高效且排除自定义头像）、O(1) 增量计数更新（`kept`/`replaced`/`cleared`）、`artist_image_reviews` 幂等读写、`artist_image_rejections`（URL + `content_hash`）读写、单事务前置写锁的自动替换与自动清理（含合并源艺人缓存快照）及 `RevertArtistImageAuditAction` 一键撤销（校验无并发自定义头像并写人工保留标记）；补齐存储单测覆盖上述全部状态转换与并发冲突场景（`go test ./internal/storage -run "ArtistImage|BackgroundCandidates"`）。
- [x] 1.4 更新 `internal/storage/track_extras.go` 中的 `artistImageURLSQL`，使自动缓存头像（含合并源继承缓存）返回带 `?v=<content_hash前12位>` 版本参数的 URL；补齐单测验证替换缓存后 URL 版本号即时变化且自定义头像优先级不变（`go test ./internal/storage -run "ArtistImageURL|TrackExtras"`）。

## 2. 统一联网搜索层与 Tavily 多 Key 轮询

- [x] 2.1 在 `internal/websearch/` 实现 Tavily 客户端（`POST /search` 与 `POST /extract`，Bearer 鉴权、复用代理与 2 MiB 响应限制）、进程内共享 `TavilyKeyRing`（Round-Robin 游标、按 429/402/432/433/401/403/5xx 分类冷却与失效隔离、不因单页 extract 失败误伤 Key、脱敏状态快照）以及支持 `tavily` / `deepseek` / `tavily_then_deepseek` 策略的共享 `Registry` / `Router`；编写假 HTTP 服务单元测试验证轮询顺序、各类状态码自动切 Key、全员不可用错误分类、DeepSeek 自动回退与零明文密钥泄露（`go test ./internal/websearch`）。
- [x] 2.2 将 `internal/enrichment`（头像与简介）、`internal/artistaliases` 与 `internal/aireview` 全部统一接入共享 `websearch.Registry`，移除 `artistaliases` 与 `aireview` 中的重复 DeepSeek 客户端实现，并在 `cmd/server/main.go` 完成统一注入；验证既有与新增测试全部通过（`go test ./internal/websearch ./internal/artistaliases ./internal/aireview ./internal/enrichment -run "Web|Alias|Review|Bio|Image"`）。

## 3. LLM 温度兼容与全量历史头像 AI 审查替换引擎

- [x] 3.1 更新 `internal/tagging/llm.go` 中的 `ChatClient.Complete`：遇到 HTTP 400 且错误消息提及 `temperature` 不受支持时自动省略 `temperature` 重试一次，并在客户端实例内按模型缓存该偏好；补齐单测验证普通模型行为不变及不接受零温模型自动重试成功（`go test ./internal/tagging -run "Complete|ChatClient"`）。
- [x] 3.2 在 `internal/enrichment/artist_image_ai.go` 与 `artist_image_backfill.go` 实现 `audit_all` / `audit_force` 执行引擎：本地损坏/尺寸预检零成本判 `garbage`、多模态结构化存量审查（`verdict`/`issueType`/`confidence`/`reason`，计入 `artist_image_audit` 用量且遇 400/解析错明确返回 `failed` 绝不静默放行）、按 `content_hash` 幂等跳过已审通过项、高置信（`>= audit_auto_confidence`）触发多源重抓（全链路排除旧图 URL + `content_hash` 且 Tier 1–3 候选均过 VLM 复核）、双高置信自动替换并留存快照、中置信候选进待审新旧对比、无替代图按 1.A 策略区分清理 `garbage`（含合并源）或保留非垃圾图送待审、中低置信疑点保留旧图不自动耗联网额度；编写假图源 + 假多模态 LLM 的全场景单元测试（`go test ./internal/enrichment -run "ArtistImage"`）。

## 4. HTTP 接口、管理后台 UI 与交互

- [x] 4.1 扩展 `internal/http/ai_settings.go` 与 `internal/http/templates/ai-settings.html`：在 AI 设置中心新增「联网搜索服务」配置卡片（提供商策略、多行 Tavily Key 追加/替换/清空、DeepSeek 联网 Key、每把 Key 掩码与实时健康状态展示）及联网搜索连通性测试端点（含 CSRF 与管理员鉴权）；更新 `internal/http/artist_aliases.go` 与 `artist-aliases.html` 展示全局联网搜索状态并引导至 AI 设置中心，同时保持旧表单提交向后兼容；补齐 HTTP 测试验证保存、脱敏无明文泄露、测试连通性与权限拦截（`go test ./internal/http -run "AISettings|ArtistAliases"`）。
- [x] 4.2 扩展 `internal/http/artist_image_backfill.go`、`internal/http/app.go`、`internal/http/templates/match-review.html` 与 `internal/http/assets/admin.js`：头像补全模式选择器新增 `audit_all`（全量补全与历史头像 AI 审查）与 `audit_force`（强制重审全部历史头像），头像设置弹窗新增高置信自动替换阈值配置与全局联网状态提示，活动/最近任务计数支持展示「已保留 / 已替换 / 已清理」，「待审头像」页支持展示「旧图 vs 新图」对比卡片以及 `audit_review` 疑点卡片（保留原图并标为合格 / 清空头像 / 联网找新图），新增「最近 AI 自动替换/清理记录」折叠区与一键撤销端点（`POST /admin/matches/images/audit-actions/{id}/revert`）；补齐 HTTP 测试验证新模式启动、待审新旧对比操作与一键撤销（`go test ./internal/http -run "ArtistImage|ArtistReview"`）。

## 5. 整库集成与回归验证

- [x] 5.1 编写跨层端到端集成测试，串联 Tavily 多 Key 轮询故障转移 -> `audit_all` 全量历史头像审查（涵盖合格跳过、高置信替换、1.A 垃圾图清空含合并源、非垃圾图保留待审、中低置信不耗搜图、自定义头像保护）-> 缓存 URL `?v=` 刷新 -> 一键撤销复原，并运行全量测试与编译检查确认零回归（`go test ./... && go build ./...`）。
