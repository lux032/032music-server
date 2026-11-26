# Design

## Context

参见 `proposal.md` 了解背景与动机。在已归档的 `artist-avatar-ai-audit-and-tavily-search` 基础上，系统已具备以下核心基础设施：

- 统一联网搜索路由 `internal/websearch.Registry`（Tavily 多 Key 轮询与故障转移 + DeepSeek 回退）与共享全局限流器 `aiclient.Limiter`。
- 存量头像审查与重抓能力（`auditExistingArtistAvatar`、`replaceAuditedArtistAvatar`、`verifyArtistAvatarWithVLM`、`discoverArtistImageFromWebWithExclusions`）以及防重审表 `artist_image_reviews`、URL/哈希双黑名单 `artist_image_rejections`、可逆审计快照 `artist_image_audit_actions`。
- 增量身份匹配任务 `executeArtistRun`（[artist_runtime.go](file:///workspace/032music-server/internal/enrichment/artist_runtime.go)）、Inline AI 确认回调 `OnArtistConfirmed`（[main.go](file:///workspace/032music-server/cmd/server/main.go)）与人工确认端点 `confirmArtistCandidate`（[identity.go](file:///workspace/032music-server/internal/http/identity.go)）。
- 最新数据库迁移为 `092_artist_image_candidate_search_state.sql`。

当前存在两个架构割裂点：
1. 缺图获取路径（`cacheMissingMatchedArtistImage`、`RefreshConfirmedArtistImage`、`processArtistImageBackfillItem`）在 Tier 1 / Tier 2 下载后直接落库，绕过了 VLM 质检且不写 `artist_image_reviews(keep)`。
2. 新曲入库后的身份确认回调以同步方式在匹配循环或 HTTP 请求内拉取头像，既无法安全引入耗时较长的 VLM + Tier 3 联网检索，也导致人工确认请求直接卡在外部网络请求上。

## Goals / Non-Goals

**Goals:**

- 收口全系统所有缺图获取入口到统一的全链路 VLM 准入闸门（Tier 1 → Tier 2 → Tier 3），确保启用 VLM 质检后不再有任何路径把未质检的错图/专辑封面写入 `artist_image_cache`。
- 为新确认 MusicBrainz 身份的全角色艺人提供持久化、低并发、可重启恢复的异步自动补图与未审旧图审查队列，与手动全量补全任务（`artist_image_backfill_runs`）及手动待审找图队列（`artistImageSearchQueue`）完全解耦。
- 严格遵循「仅新确认 MusicBrainz 身份入队」边界：开启开关绝不拉起全库历史旧缺图；自定义头像与已审查通过（`keep`）的老头像始终零成本跳过。

**Non-Goals:**

- 不对未确认 MusicBrainz 身份（`no_result` / `review` / 未拆分复合署名）的新艺人触发自动队列。
- 不改变 `artist_custom_images` 自定义头像保护边界。
- 不引入新的独立 LLM Provider 配置或第三方 Go 依赖；复用既有全局 AI 设置与联网搜索路由。
- 不在自动队列中强行占满并发或打断手动全量任务：自动队列固定串行（并发 1），通过共享 `aiLimiter` 和 `TavilyKeyRing` 自然协调配额。

## Decisions

### 决策 1：数据库迁移 `093_artist_image_auto_queue.sql`

```sql
ALTER TABLE artist_image_settings
    ADD COLUMN auto_ingest_ai_enabled INTEGER NOT NULL DEFAULT 1 CHECK (auto_ingest_ai_enabled IN (0, 1));

CREATE TABLE artist_image_auto_queue (
    artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
    trigger_source TEXT NOT NULL DEFAULT 'auto_match',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_retry_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_artist_image_auto_queue_claim ON artist_image_auto_queue(status, next_retry_at, created_at);
```

- **为何用独立小表而非复用 `artist_image_backfill_runs`**：
  - `artist_image_backfill_runs` 是管理员显式启停的单活动任务槽位（同时只允许 1 个运行/暂停任务），且受 `max_web_requests_per_run` 约束；如果新曲扫描直接去开 backfill run，会和用户正在跑的全量任务产生 `ErrArtistImageBackfillState` 冲突，也容易混入历史旧缺图。
  - 独立队列小表以 `artist_id` 为主键天然去重、仅承接“新确认 MusicBrainz 身份”的增量事件、处理成功后即删除行保持表体极小（失败项最多保留近期错误记录），且启动恢复只需一行 `UPDATE ... SET status='pending' WHERE status='running'`。
- **替代方案**：纯内存 channel 队列。权衡后拒绝——新曲批量导入 30 位艺人若中途容器重启会静默丢失待补艺人，持久化小表成本极低且完全符合本项目 `write-lock-and-cache-resilience` 持久化风格。

### 决策 2：统一全链路 VLM 准入引擎（`acquireArtistAvatarWithAIGate`）

- 抽离统一的缺图获取函数，同时服务于：
  1. `artist_image_auto_queue` 后台 Worker（`checkpoint = nil`，不扣手动全量任务的 `web_requests` 计数，要求开启 `auto_ingest_ai_enabled` 且具备已确认 MusicBrainz 身份）；
  2. `artist_image_backfill` 手动补全任务的缺图分支（传入 `&checkpoint`，遵守 `runMode` 与 `max_web_requests_per_run`）。
- **核心流程**：
  1. 加载排除集 `ArtistImageRejections(ctx, artistID)`（覆盖 URL + `content_hash` + 历史 `rejected` 候选）。
  2. 构造统一候选评估闭包 `considerCandidate(srcName, imgURL, pageURL, title, structured)`：
     - 跳过空 URL / 已试 URL / 排除集 URL / 无效占位 URL；
     - 下载图片 + `InspectImageQuality` + 排除集 `sha256` 校验；
     - **VLM 准入判断**：
       - 当 `imgSettings.VisionEnabled` 开启时（且调用方启用 VLM 闸门）：调用 `verifyArtistAvatarWithVLM`（**非静默失败**：HTTP 400 / 鉴权错 / 解析错直接返回 error 终止本次尝试，绝不放行）。
         - 若 `valid == false`：写入 `artist_image_rejections`（原因 `vlm_rejected:<srcName>`）防止后续反复回抓，继续尝试下一候选/下一源！
         - 若 `valid == true && conf >= threshold`：命中高置信合格图，立即停止后续图源尝试。
         - 若 `valid == true && conf < threshold`：暂存为 `bestMedium`（按分数取最高），继续尝试后续图源看能否拿到高置信图。
       - 当 `imgSettings.VisionEnabled` 关闭时：保持向后兼容，Tier 1 / Tier 2 通过本地尺寸校验后直接视为合格入库（不额外强行要求多模态模型）。
  3. 图源尝试顺序：
     - **Tier 1**：已确认外部资料 URL（必要时执行 `RefreshConfirmedArtistImage` 刷新 MB / Last.fm profile URL 而不直接写盘）
     - **Tier 2**：已启用的 Fanart（MBID 锚定）→ Deezer（先 MB raw_json 内的 `deezerIds`，再名称检索）→ Bangumi
     - **Tier 3**：当允许联网（`webAllowed`）且满足预算/配置条件时，执行 `discoverArtistImageFromWebWithExclusions(..., requireVLM=imgSettings.VisionEnabled)`
  4. 结果落库（遵循前置写锁 + 无自定义头像 + 未合并守卫）：
     - 命中高置信合格图（对于 Tier 3 且开启 `web_auto_apply_high`，或 Tier 1/2 高置信过 VLM）：写入 `artist_image_cache`，并在通过 VLM 时同步写入 `artist_image_reviews(verdict='keep', model=vlmModel)`，清除 `artist_image_attempts`。
     - 仅命中中置信候选（`bestMedium != nil`，或 Tier 3 高置信但关闭了 `web_auto_apply_high`）：缓存候选预览字节，写入 `artist_image_candidates(kind='replacement')` 送入「待审头像」。
     - 全源无合格图且无瞬时错误：写入 `artist_image_attempts` 负缓存冷却（`negative_cache_days`）。
     - 若发生瞬时网络/429/VLM 错误：不写长期负缓存冷却，将错误返回上层以便队列退避重试或 backfill 标记 `failed`。

### 决策 3：新确认 MusicBrainz 身份触发点与未审旧图顺手审查（2B 策略）

- **统一入队方法 `Manager.QueueAutoConfirmedArtistImage(ctx, artistID, triggerSource)`**：
  1. 校验 `artist_image_settings.auto_ingest_ai_enabled == true`（若关闭，则人工/自动确认仍可选择不做阻塞联网，保持轻量无副作用；若关闭时仍希望兼容传统非 AI 行为？权衡：默认 `auto_ingest_ai_enabled = 1` 开启，当用户显式关闭该开关时即表示不希望新确认艺人自动消耗 AI/联网，因此直接跳过入队，用户仍可在艺人页或补全页手动拉图）。
  2. 校验目标艺人未合并、无自定义头像（含合并源）、且**存在已确认的 MusicBrainz 身份**（`ArtistExternalID(ctx, artistID, "musicbrainz")` 非空，含合并链归属或直接归属规范艺人）。
  3. 写入 `artist_image_auto_queue`（`ON CONFLICT(artist_id) DO UPDATE SET status='pending', attempts=0, next_retry_at=NULL, trigger_source=excluded.trigger_source, updated_at=now WHERE status<>'running'`）并向 Worker 发送非阻塞唤醒信号 `autoImageWake`（buffered chan size 1）。
- **4 个触发入口全部统一收口**：
  1. `executeArtistRun`（[artist_runtime.go](file:///workspace/032music-server/internal/enrichment/artist_runtime.go#L152)）：规则自动匹配成功时，不再同步调用 20s 的 `cacheAutoMatchedArtistImage`，改为调用 `m.QueueAutoConfirmedArtistImage(ctx, item.ObjectID, "auto_match")`（简介刷新仍保持原样）。
  2. `EnrichAutoConfirmedArtist`（[artist_image_backfill.go](file:///workspace/032music-server/internal/enrichment/artist_image_backfill.go#L121)）：Inline AI 自动确认艺人身份后，改为调用 `m.QueueAutoConfirmedArtistImage(ctx, artistID, "inline_ai")` + 异步/原样刷新简介。
  3. `confirmArtistCandidate`（[identity.go](file:///workspace/032music-server/internal/http/identity.go#L578)）：人工点击「确认匹配」绑定身份后，立即调用 `a.enrichment.QueueAutoConfirmedArtistImage(r.Context(), id, "manual_confirm")` 并在响应提示「外部身份已确认，头像将在后台自动质检补全」（彻底消除人工确认卡死 HTTP 的痛点！）。
  4. 复合署名拆分：复合署名拆分产生的新独立艺人会在下一轮自动增量匹配（`StartAuto`）中被规则或 Inline AI 确认 MusicBrainz 身份，自然经入口 (1)/(2) 进入同一队列，无需额外特判。
- **Worker 单项处理逻辑（覆盖 2B 策略）**：
  1. 实时复查资格：规范未合并、无自定义头像、仍有已确认 MusicBrainz 身份、无既有 `pending` 候选卡（避免覆盖管理员尚未审核的待审卡）。
  2. 检查本地是否已有有效缓存头像：
     - **已有有效缓存头像**：查询 `ArtistEffectiveImageCache` 与 `ArtistImageReviewByArtist`：
       - 若当前 `content_hash` 已存在 `verdict='keep'` → **直接完成出队（0 token）**；
       - 否则（未审旧图）→ 若 `VisionEnabled` 且 AI 已配置，调用既有 `auditExistingArtistAvatar` + 高置信时调用 `replaceAuditedArtistAvatar`（无 checkpoint 模式，复用全部 1.A 策略、双黑名单与撤销快照！），合格则记 `keep`、中低置信送 `audit_review`；若未配置 AI/VLM 则直接跳过完成出队。
     - **本地无有效缓存头像**：先检查是否在 `web_fallback` 负缓存冷却期（若是且刚确认 MB 身份？注意：刚拿到新 MB 身份属于信息增量，应允许忽略之前无 MB 身份时的旧冷却，直接执行一次获取！），调用 `acquireArtistAvatarWithAIGate` 完成 Tier 1→2→3 获取与质检。
  3. **退避重试**：
     - 若返回限流（`RateLimitError`）或瞬时网络/模型错误，且 `attempts < 3`：写回 `status='pending'`, `attempts = attempts + 1`, `next_retry_at = now + backoff`（1m → 5m → 15m，不写 30 天负缓存），由定时器按 `next_retry_at` 唤醒重试。
     - 若 `attempts >= 3`：置 `status='failed'` 并记录脱敏 `last_error`（等待人工或后续手动补全，不阻塞队列其余艺人）。
     - 成功/跳过：从 `artist_image_auto_queue` 删除该行并累加内存态会话统计（本次服务运行周期内 `processed / cached / replaced / cleared / candidate / skipped / failed`），供 `/admin/matches/images/auto-queue.json` 展示。

### 决策 4：管理后台可见性与设置项

- **设置弹窗（`/admin/matches`）**：在「联网 AI 兜底与质量把关」分组新增勾选卡片：**`新确认艺人自动 AI 质检与联网补全`**（`auto_ingest_ai_enabled`，副标题「确认 MusicBrainz 身份后后台自动过 VLM 质检并联网兜底」）。
- **状态端点与前端轮询**：
  - 新增 `GET /admin/matches/images/auto-queue.json`（管理员鉴权 + 列入 `quietAccessLogPath`），返回 `{enabled, pending, running, failed, currentArtist, processed, cached, replaced, candidate}`。
  - 在 `admin.js` 的 `artistTaskSources` / 顶部 `[data-artist-task-overview]` 中接入该端点：当 `pending + running > 0` 时展示状态芯片（例如 `自动补图 · 排队 3 · 当前：xxx`），平时空闲（`0`）时保持完全隐形零打扰。

## Risks / Trade-offs

- **[批量导入大合辑一次自动匹配出上百位幕后艺人，触发大量 VLM/Tavily 调用]** → 队列固定串行（并发 1），严格经过全局 `aiLimiter`（RPM 限流）与 `TavilyKeyRing`（多 Key 轮询与自动冷却转移）；同时仅对具备 MusicBrainz 身份的艺人触发，且通过弹窗开关可随时一键关闭。
- **[开启全链路 VLM 后，无多模态模型或未勾选 VLM 的老用户受影响]** → 仅当 `VisionEnabled == true` 且全局 AI 已配置时启用 VLM 闸门；未勾选 VLM 或未配置 AI 时，Tier 1/2 仍可平稳按原有尺寸校验入库，保持完全向后兼容。
- **[与手动全量补全任务并发处理同一艺人产生写冲突]** → 所有写缓存与候选表操作均复用单事务前置写锁（`artistImageBackfillWriteLock` / `artist_image_cache` 写锁）、自定义头像实时守卫与 `artist_image_reviews` 幂等判断；若全量任务已先写入 `keep` 或 `pending` 候选，自动队列复查资格时直接跳过，互不覆盖。
- **[服务重启时队列中存在 `status='running'` 残留项]** → 在 `enrichment.New()` 启动恢复阶段执行 `RecoverArtistImageAutoQueue`，将所有 `running` 复位为 `pending` 并在服务启动后自动唤醒 Worker 续跑。

## Migration Plan

1. 升级部署自动执行迁移 `093_artist_image_auto_queue.sql`：
   - 为 `artist_image_settings` 新增 `auto_ingest_ai_enabled`（默认 `1`）；
   - 创建 `artist_image_auto_queue` 持久化队列小表及索引（初始为空，不拉起任何历史旧缺图）。
2. 启动后 `enrichment.Manager` 启动常驻轻量 `autoImageWorker`（平时无任务完全休眠，仅被入队或重试定时器唤醒），新曲入库确认 MusicBrainz 身份后自动进入队列。
3. 回滚策略：可随时在头像补全设置弹窗关闭「新确认艺人自动 AI 质检与联网补全」立即停止新入队；既有撤销快照与 `artist_image_cache` 结构完全兼容，无破坏性表结构变更。
