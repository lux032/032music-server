# Design

## Context

详见 [proposal.md](./proposal.md) 中的问题背景。当前代码关键现状与约束如下：
- 所有歌手图片下载（Tier 1 资料图、Tier 2 结构化图源、Tier 3 联网抽图）均通过 `Manager.downloadPublicImage(ctx, remoteURL, "artist image")`（[work_poster.go](file:///workspace/032music-server/internal/enrichment/work_poster.go)）发起；`netfetch.DownloadImage` 对 HTTP `429` 或带 `Retry-After` 的 `503` 返回 `ImageRateLimitError`，`downloadPublicImage` 统一将其记入全局键 `"artist image"` 的冷却表并返回 `*RateLimitError`。
- `ratelimit.go` 中的 `sourceDisplayName` 与 `noticeSourceName` 已使用 `strings.HasPrefix(source, "artist image")`，天然兼容带域名后缀的 `"artist image:<host>"` 键，但目前从未传入域名。
- `artist_image_backfill.go` 的主循环（`executeArtistImageBackfill`）收到任意 `*RateLimitError` 都会对**同一位艺人**做 30s / 1m / 3m 阶梯退避并在累计 30 分钟或同一对象连续 3 次后自动暂停；既有测试 `TestArtistImageBackfillRateLimitAutoResumeExhausted`、`TestArtistImageBackfillRateLimitThenSuccess`、`TestArtistImageBackfillPauseResumeCancel`、`TestArtistImageBackfillGracefulShutdownReason` 均验证 `matched` 模式下 Tier 1 确认资料图 429 走这套等待/自动恢复逻辑。
- 管理页模板（`match-review.html`、`enrichment-jobs.html`）直接插值 `{{.WaitSource}}`，`admin.js` 在 `waitingUntil` 过期后仍以 `.match-active-wait` 琥珀色警告条展示 `限流 ${source} · 累计等待 ${total}`。

## Goals / Non-Goals

**Goals:**
- 按图片 URL 域名隔离冷却桶，防止一个被限流的第三方图床拖垮其他图源或整轮批量任务。
- 彻底消除全量补全/全量审查/自动入库队列因某个外站或单一图床返回 429 而卡死在同一艺人上无限累积等待的问题。
- 在不引入任何数据库表结构变更的前提下，保持 `matched` 单源模式与全局上游 API 限流退避的完整向后兼容。
- 管理页对活跃限流与历史累计等待提供清晰、本地化的中文展示与视觉层级。

**Non-Goals:**
- 不修改上游共享元数据/AI/搜索 API（MusicBrainz、Last.fm、Bangumi API、Deezer API、Fanart API、LLM、Tavily、DeepSeek）的限流检测与任务退避机制。
- 不修改数据库表结构或新增 SQL 迁移（复用现有 `wait_source`、`waiting_until`、`wait_total_ms` 字段）。
- 不为每个域名引入持久化数据库冷却表（内存级冷却表即可覆盖单次运行内的防重锤保护，且避免无限膨胀）。

## Decisions

### Decision 1: 图片下载冷却键按域名隔离并做内存上限清理
- **方案**：新增辅助函数 `artistImageDownloadSource(rawURL string) string`，解析 URL 获取规范化小写域名（去除尾点与端口），非空时返回 `"artist image:" + host`（如 `"artist image:upload.wikimedia.org"`），解析失败时回退 `"artist image"`。在 `downloadPublicImage` 中当 `label == "artist image"` 时自动推导并使用该域名键做 `checkSourceCooldown` 和 `rateLimitedError`。
- **内存边界**：在 `blockSourceUntil` 写入非 gate 托管的 `m.blockedUntil` 时，若当前 map 大小超过 256，惰性清理所有已过期（`!until.After(now)`）条目，避免遍历数千个随机第三方域名时内存无界增长。
- **备选方案与取舍**：曾考虑为所有域名共享 1 个 `"third_party_image"` 桶，但仍会导致 Wikimedia 429 拖累 Discogs/官网；按域名隔离精确到真实限流边界且完全复用既有 `blockedUntil` 结构。

### Decision 2: 按调用场景区分「非阻塞顺延」与「任务级退避等待」
- **Tier 3 第三方联网图（`discoverArtistImageFromWebWithExclusions`）**：
  - 在将 `flat` 图片候选送给文本 LLM 选图之前，过滤掉 `m.checkSourceCooldown(artistImageDownloadSource(c.ImageURL)) != nil` 的候选，既省 LLM token 又避免再次选中刚刚 429 的坏域名。
  - 下载 `picked.ImageURL` 遇 `asRateLimited(dlErr) != nil` 时，由于 `downloadPublicImage` 已自动登记该域名冷却，此处**不再返回 `dlErr`**，仅记 `Debug` 日志并返回 `(zero, false, nil)`（视为该候选图不可用）。
- **多源缺图获取与存量替换重抓（`acquireArtistAvatarWithAIGate`、`replaceAuditedArtistAvatarOpt`、`processStructuredArtistImageFallback`）**：
  - 对图片下载限流引入清晰开关：`blockOnImageHostRateLimit := (runMode == "matched")`（仅在无多源兜底的 `matched` 模式下允许 Tier 1 图片下载 429 冒泡到 run 的退避等待循环；`all`、`web_fallback`、`rescan_all`、`audit_all`、`audit_force`、`auto_ingest` 以及 `replaceAuditedArtistAvatarOpt` 均设为 `false`）。
  - 当 `blockOnImageHostRateLimit == false` 且图片下载命中 `asRateLimited(dlErr) != nil`：登记域名冷却、置 `hadTransientErr = true`（确保后续不写入 30 天负缓存冷却，下次扫描仍可重试），并**立即顺延尝试下一张 URL / 下一个图源 / 下一层级**。
  - 注意：**JSON API 查询阶段**（MusicBrainz lookup、Last.fm API、Fanart API、Deezer search API、Bangumi person search API、Tavily/DeepSeek API、VLM/LLM API）的 429/503 仍照常返回 `*RateLimitError`，不受此开关影响。
- **旧全局预检查清理**：移除 `cacheAutoMatchedArtistImage` 中写死的 `m.checkSourceCooldown("artist image")`（因实际下载已按域名检查）。
- **备选方案与取舍**：曾考虑把所有模式（含 `matched`）的 429 全部静默跳过，但 `matched` 模式只有唯一确认资料 URL 且既有 4 个核心单测明确约定其退避等待与 3 轮自动恢复契约；按模式区分既 100% 解决多源/全量审查卡死痛点，又零破坏既有契约。

### Decision 3: 存储层手动恢复清理过期 `waiting_until` + 前后端一致的中文限流展示
- **存储层**（`internal/storage/artist_image_backfill.go`）：`TransitionArtistImageBackfillRun` 执行 `"resume"` 时，若库中 `waiting_until` 已早于当前时间，将其清为空字符串/NULL（保留 `wait_total_ms` 与 `wait_source` 供历史累计显示），避免恢复瞬间 SSR 读取到过期时间戳。
- **文案映射**：
  - 后端在 `internal/http/enrichment.go` 新增 `waitSourceLabel(source string) string` 与 `waitUntilFuture(raw string) bool` 并注册进 `app.go` 的 `template.FuncMap`。
  - 前端在 `internal/http/assets/admin.js` 新增同口径 `formatWaitSource(source)`，支持 `"artist image"` → `"图片源"`、`"artist image:<host>"` → `"图片源（<host>）"`，以及常见元数据/AI/搜索来源的中文名映射。
- **活跃 vs 历史视觉分层**：
  - 当 `waitingUntil` 在未来（活跃限流等待或限流自动暂停待恢复）：保持 `.match-active-wait` 琥珀色警告样式，文案为 `限流 ${label} · 等待 ${mm}:${ss}（累计等待 ${total}）` 或 `限流 ${label} · ${mm}:${ss} 后自动继续（累计等待 ${total}）`。
  - 当 `waitingUntil` 为空或已过期、但 `waitTotalMs > 0`（已恢复正常执行或处于手动暂停）：为元素添加 `.is-historical` 类（在 `pages.css` 中使用低对比中性灰样式，不再显示琥珀警告底色），文案改为 `历史限流（${label}）累计等待 ${total}`。

## Risks / Trade-offs

- **[Risk] 全量模式下某一结构化图源（如 Wikimedia）临时 429 时该艺人被跳过当前图源** → **Mitigation**: 因为立即顺延至 Deezer/Bangumi/Tavily，绝大多数艺人会从其他正常图源拿到合格头像；即便全源都无图，因设置了 `hadTransientErr = true` 不会写入 30 天负缓存冷却，后续补扫会自动重试。
- **[Risk] 正在运行的旧任务（如 `#14`）数据库中 `wait_source` 仍保留历史值 `"artist image"`** → **Mitigation**: 后端与前端的中文映射均显式兼容无后缀 `"artist image"`（显示为 `图片源`），重启升级后历史任务继续运行时无缝生效，且不再因第三方 429 卡死在同一艺人。
- **[Risk] `m.blockedUntil` 按域名存储后在超大曲库下条目数增加** → **Mitigation**: 在 `blockSourceUntil` 内设置 256 条阈值的过期清理，确保常驻内存占用极小且有界。
