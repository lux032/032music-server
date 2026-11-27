# Proposal

## Why

在全量头像补全与 AI 审查替换任务（如 `audit_all` / `web_fallback` / `all`）以及新入库自动补图队列运行期间，所有层级的歌手图片下载（Tier 1 资料图、Tier 2 Deezer/Bangumi/Fanart、Tier 3 联网搜图命中的任意第三方网站）共用同一个全局限流键 `"artist image"`。一旦任意单个图床或外站防爬节点（如 Wikimedia Commons、Discogs、Fandom 或带 WAF 的站点）返回 HTTP `429` 或携带 `Retry-After` 的 `503`，不仅会锁死其他完全无关图源的图片下载，还会让整轮批量任务卡死在同一位艺人上反复退避重试并触发 30 分钟预算自动暂停，导致进度停滞（例如长时间停在 `1799 / 4311` 且累计等待飙升至 50+ 分钟）。同时，管理后台任务卡直接原样拼接英文键 `artist image`，且在限流早已结束、任务正在正常运行或手动暂停时依然亮着醒目的黄色「限流」提示条，容易误导运维判断当前任务是否被卡住。

## What Changes

- **图片下载按域名（Host）隔离冷却桶**：将歌手图片下载的限流/冷却键从单一全局 `"artist image"` 细化为按规范化域名隔离的 `"artist image:<host>"`（无法解析域名时回退 `"artist image"`），使某一图床冷却不再牵连其他正常图床。
- **第三方联网图与多源兜底链遇图床 429/503 不阻塞整轮任务**：
  - Tier 3 联网搜图（Tavily/DeepSeek + DOM 抽图）下载任意第三方图片若命中 `429` / `503 + Retry-After`（或该域名处于冷却中），仅记录该域名的短冷却并直接跳过当前图片/尝试下一候选或下一层，绝不向批量任务或自动入库队列抛全局 `RateLimitError`。
  - 多源模式（`all`、`web_fallback`、`rescan_all`、`audit_all`、`audit_force`、`auto_ingest` 以及存量头像高置信判错后的多源重抓 `replaceAuditedArtistAvatarOpt`）在下载 Tier 1 / Tier 2 图片遇到单图床 429 时，记录该域名冷却并立即顺延尝试下一图源/下一层，而非停住整轮任务死磕同一链接；同时标记 `hadTransientErr` 以免误写长期负缓存，后续重扫仍可重试。
  - 保留 `matched` 单源确认资料模式（无多源兜底时）以及真正全局共享的上游 API（MusicBrainz、Last.fm、Bangumi、Deezer API、Fanart API、AI 模型服务、Tavily/DeepSeek 搜索接口）的既有任务级限流退避与自动恢复语义。
- **管理后台限流提示中文化与「进行中限流 vs 历史累计」状态分离**：
  - 后端模板助手与前端 `admin.js` 统一将 `waitSource` 映射为易读中文（含 `artist image:<host>` → `图片源（<host>）`、`artist image` → `图片源`、`ai` → `AI 模型服务` 等），不再泄漏原始英文内部键。
  - 仅在存在未过期 `waitingUntil` 倒计时（或因限流自动暂停待恢复）时以琥珀色警告样式展示「限流中」；当限流窗口已过、任务已恢复正常运行或处于手动暂停时，降级为低干扰中性灰字展示历史累计等待（如 `历史限流累计等待 54 分钟`），并在手动恢复任务时清理已过期的 `waiting_until`。

## Capabilities

### New Capabilities

（无新增能力目录，本变更扩展既有 `artist-avatar-ai-audit` 能力规格。）

### Modified Capabilities

- `artist-avatar-ai-audit`: 新增按域名隔离图片下载冷却、第三方联网图与多源模式图床 429 非阻塞顺延策略，以及任务卡片限流来源中文展示与活跃限流/历史累计视觉区分的规范要求。

## Impact

- **Backend Enrichment** (`internal/enrichment/`): `work_poster.go`、`ratelimit.go`、`artist_image_ai.go`、`artist_image_backfill.go`、`artist_image_auto_queue.go` 及对应单测。
- **Backend Storage** (`internal/storage/`): `artist_image_backfill.go`（`TransitionArtistImageBackfillRun` 手动恢复时清理已过期 `waiting_until`）。无任何数据库表结构变更，不需要新增 SQL 迁移。
- **HTTP & Admin UI** (`internal/http/`): `enrichment.go`、`app.go`、`templates/match-review.html`、`templates/enrichment-jobs.html`、`assets/admin.js`、`assets/pages.css` 及对应 HTTP 测试。
- **Compatibility**: 既有 `matched` 模式 Tier 1 图片限流等待、3 次自动恢复与手动暂停/恢复测试契约保持完全兼容；对已有正在运行/暂停的历史任务（含旧 `wait_source="artist image"`）无缝向后兼容。
