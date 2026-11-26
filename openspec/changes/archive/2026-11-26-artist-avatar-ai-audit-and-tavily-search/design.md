# Design

## Context

参见 `proposal.md` 了解需求动机。当前 `032music-server` 已有以下基础能力可直接演进：

- 头像补全任务表 `artist_image_backfill_runs` / `artist_image_backfill_items`（`040` / `082` 迁移）与 `internal/enrichment/artist_image_backfill.go` 已有完整的持久化断点、claim token 纪元围栏、暂停/继续/停止、重启恢复、限流自动恢复及 O(1) 计数更新（遵循 `write-lock-and-cache-resilience` 规范）。
- 头像缓存文件存于 `data/artist-images/<sha256><ext>`，天然按内容哈希寻址，替换数据库缓存行不会删除旧磁盘文件。
- 现有 `internal/websearch/websearch.go` 已定义与厂商无关的 `Client` 接口（`Search` / `Fetch`），但仅有 DeepSeek 实现，且 `internal/artistaliases` 与 `internal/aireview` 仍保留了重复的 `DeepSeekWeb` 实现。
- `internal/tagging/llm.go` 为所有 OpenAI 兼容请求硬编码发送 `"temperature": 0`，且 `verifyArtistAvatarWithVLM` 在遇到 `400` / 解析错误时静默返回 `valid=true`。
- `internal/storage/track_extras.go` 中的 `artistImageURLSQL` 对自定义头像附带 `?v=<hash>`，但对自动缓存头像仅输出无版本号的 `/api/v1/artists/{id}/image`，配合 `handleArtistImage` 的 `max-age=86400` 会导致替换后浏览器长期显示旧图。
- 最新数据库迁移为 `090_audio_loudness.sql`。

## Goals / Non-Goals

**Goals:**

- 统一全库 4 处联网调用（头像补全/审查、歌手简介、中文别名、AI 审核）到共享 `internal/websearch` 路由层，支持 Tavily 多 Key 轮询与自动故障转移，同时平滑兼容既有 DeepSeek 密钥。
- 支持对全库非自定义头像的历史缓存头像执行 GPT 多模态全量审查，实现高置信自动多源获取并替换、1.A 差异化无替代图策略、按 `content_hash` 幂等跳过以及一键撤销快照。
- 保证全链路防死循环（URL + `content_hash` 双黑名单覆盖 Tier 1–3，且重抓新图全过 VLM）、保护人工自定义头像不被覆写、严守 SQLite 前置写锁与 O(1) 进度更新规范。
- 兼容 `gpt-6-luna` 等新型 OpenAI 兼容模型（自动处理 `temperature: 0` 拒收），并消除 VLM 审查静默放行盲区。

**Non-Goals:**

- 不改变 `artist_custom_images`（用户手动上传头像）的保护边界——任何全量审查/替换模式均绝不审查或覆写自定义头像。
- 不为头像模块拆分独立于全局 AI 设置中心的新 LLM Provider/BaseURL/APIKey 配置（沿用全局 OpenAI 兼容连接配置与可选模型名覆盖）。
- 首版 Tavily 搜图不直接消费 Tavily `include_images` 裸图链接，仍坚持由 Tavily 返回网页结果后通过本地 SSRF-safe `netfetch.FetchPageImageMeta` 抽图并做页面身份核验，避免引入低可信无上下文图片。
- 不改变公开媒体 API 的 `apiRevision`（新增 `?v=<hash>` 查询参数向后兼容）。

## Decisions

### 决策 1：统一联网搜索配置表与进程内共享 `websearch.Router` + `TavilyKeyRing`

- **数据表（迁移 `091_artist_image_audit_and_web_search.sql`）**：
  ```sql
  CREATE TABLE web_search_settings (
      id INTEGER PRIMARY KEY CHECK (id = 1),
      provider TEXT NOT NULL DEFAULT 'tavily_then_deepseek'
          CHECK (provider IN ('tavily', 'deepseek', 'tavily_then_deepseek')),
      tavily_keys_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tavily_keys_json)),
      deepseek_key TEXT NOT NULL DEFAULT '',
      updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
  ) STRICT;

  INSERT INTO web_search_settings(id, provider, deepseek_key)
  SELECT 1, 'tavily_then_deepseek', COALESCE((SELECT deepseek_key FROM artist_alias_settings WHERE id = 1), '');
  ```
- **平滑兼容与双向同步**：
  - 迁移时将既有 `artist_alias_settings.deepseek_key` 自动拷贝至 `web_search_settings.deepseek_key`。
  - 读取有效 DeepSeek Key 顺序：`web_search_settings.deepseek_key` → `artist_alias_settings.deepseek_key` → 若全局 `ai_settings.base_url` 为 `https://api.deepseek.com` 则回退 `ai_settings.api_key`。
  - 旧 `/admin/artist-aliases/settings` 页面不再维护独立密钥，改为展示全局联网搜索状态并提供直达 `/admin/settings/ai` 的链接（若旧测试或表单提交 `deepseek_key` 仍可兼容同步写入 `web_search_settings`）。
  - `internal/storage/backup_restore.go` 的凭据保护表加入 `{"web_search_settings", "id", []string{"tavily_keys_json", "deepseek_key"}}`。
- **Tavily API 契约与多 Key 进程内轮询池（`internal/websearch/tavily.go`）**：
  - Search：`POST https://api.tavily.com/search`，Header `Authorization: Bearer <key>`，Body `{"query": q, "max_results": 5, "search_depth": "basic", "include_raw_content": false}`，解析 `results[].{url,title,content}`。
  - Fetch：`POST https://api.tavily.com/extract`，Header `Authorization: Bearer <key>`，Body `{"urls": [pageURL]}`，解析首条 `results[0].raw_content`（空或失败返回带 `Operation: "fetch"` 的 `WebError`）。
  - 复用全局 `ai_settings.proxy_url` 与 30s 超时，响应体限制 2 MiB，禁止自动跟随非预期重定向；所有错误消息严禁回显 API Key 或原始响应体。
  - **进程内共享状态**：`websearch.Registry`（由 `cmd/server/main.go` 实例化并注入 `enrichment.Manager`、`artistaliases.Service`、`aireview.Service`、`httpapi.App`）按当前有序 key 列表的 SHA-256 指纹复用同一个 `TavilyKeyRing` 实例；key 列表或代理变更时按新指纹重建（保留未变 key 的冷却状态或重新初始化已更新的 key）。
  - **轮询与状态机**：
    - `TavilyKeyRing` 内维护 `mu sync.Mutex`、`cursor uint64` 及每把 Key 的状态（`id/mask`、`state`: `healthy`/`cooldown`/`quota_exhausted`/`invalid`、`cooldownUntil`、`lastStatus`、`lastErrorAt`）。
    - 每次调用遍历最多 `len(keys)` 把非冷却/非失效 Key（从 `cursor` 起按 Round-Robin 顺序）：
      - `200 OK`：推进 `cursor`，返回结果。
      - `429`：标记 `cooldown`（优先取 `Retry-After`，否则 60s），立即换下一把 Key 重试。
      - `402` / `432` / `433`（Tavily 额度/套餐限制）：标记 `quota_exhausted`（长冷却 6h），立即换下一把 Key 重试。
      - `401` / `403`（仅 `search` 阶段，或 extract 明确鉴权错）：标记 `invalid`（直到重新保存配置清除），立即换下一把 Key 重试；注意 `extract` 针对目标网页返回不可抓取时仅视为单页失败，不惩罚 Key。
      - `5xx` 或网络超时：标记短冷却 30s，尝试下一把 Key。
    - 若所有 Key 均处于冷却或失效状态：
      - 若策略为 `tavily_then_deepseek` 且 DeepSeek 可用，自动回退调用 DeepSeek；
      - 否则返回分类错误（若至少有一把 Key 仅在短期冷却则返回带 `Status: 429` 的可退避 `WebError`；若全部 `quota_exhausted` 返回 `432`；若全部 `invalid` 返回 `401`）。
  - **替代方案**：将轮询游标与冷却时间持久化到 SQLite。权衡后拒绝——每次搜索写库增加 SQLite 写锁竞争且重启动后重新探测一次额度状态成本极低，内存态 KeyRing 简洁无锁竞争。

### 决策 2：数据库 Schema 扩展（迁移 `091_artist_image_audit_and_web_search.sql`）

- **重建 `artist_image_backfill_runs` 与 `artist_image_backfill_items`**（遵循 SQLite 父子外键约束安全顺序：新建 `runs_v2` → 拷贝 runs → 新建 `items_v2` 引用 `runs_v2` → 拷贝 items → 删除旧 `items` → 删除旧 `runs` → 重命名 `runs_v2` 与 `items_v2` → 重建索引）：
  - `runs.mode` 扩展为：`('matched', 'all', 'web_fallback', 'rescan_all', 'audit_all', 'audit_force')`。
  - `runs` 新增 O(1) 计数列：`kept_items`、`replaced_items`、`cleared_items`（延续 `write-lock-and-cache-resilience` 规范，避免 `COUNT(*)`）。
  - `items.outcome` 扩展为：`('cached', 'kept', 'replaced', 'cleared', 'candidate', 'no_url', 'skipped', 'failed')`。
- **新增 3 张头像审查与可逆历史表**：
  1. `artist_image_reviews`（按当前图哈希幂等防重审）：
     - `artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE`
     - `image_hash TEXT NOT NULL`、`verdict TEXT NOT NULL CHECK(verdict IN ('keep','replace'))`、`issue_type TEXT NOT NULL`、`confidence INTEGER NOT NULL CHECK(confidence BETWEEN 0 AND 100)`、`reason TEXT NOT NULL DEFAULT ''`、`model TEXT NOT NULL DEFAULT ''`、`reviewed_at TEXT NOT NULL`
  2. `artist_image_rejections`（Tier 1–3 共享双黑名单）：
     - `id INTEGER PRIMARY KEY`、`artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE`、`image_url TEXT NOT NULL DEFAULT ''`、`content_hash TEXT NOT NULL DEFAULT ''`、`reason TEXT NOT NULL DEFAULT ''`、`created_at TEXT NOT NULL`
     - 索引：`idx_artist_image_rejections_artist_url(artist_id, image_url)`、`idx_artist_image_rejections_artist_hash(artist_id, content_hash)`；同时兼容既有 `artist_image_candidates(status='rejected')` 作为补充排除源。
  3. `artist_image_audit_actions`（自动替换/自动清理审计日志与一键撤销快照）：
     - `id INTEGER PRIMARY KEY`、`run_id INTEGER`、`artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE`、`action TEXT NOT NULL CHECK(action IN ('replaced','cleared'))`、`issue_type TEXT NOT NULL`、`confidence INTEGER NOT NULL`、`reason TEXT NOT NULL DEFAULT ''`、`model TEXT NOT NULL DEFAULT ''`、`prev_cache_json TEXT NOT NULL CHECK(json_valid(prev_cache_json))`（数组，记录主艺人及合并源艺人被覆盖/清理前的 `artist_image_cache` 完整行，含 `ownerArtistId`）、`new_source TEXT NOT NULL DEFAULT ''`、`new_remote_url TEXT NOT NULL DEFAULT ''`、`new_content_hash TEXT NOT NULL DEFAULT ''`、`new_cache_path TEXT NOT NULL DEFAULT ''`、`reverted INTEGER NOT NULL DEFAULT 0 CHECK(reverted IN (0,1))`、`created_at TEXT NOT NULL`、`reverted_at TEXT`
- **扩展 `artist_image_candidates`**（支持展示「疑点旧图待审」与「旧图 vs 新图」对比）：
  - 新增列：`kind TEXT NOT NULL DEFAULT 'replacement' CHECK(kind IN ('replacement','audit_review'))`、`issue_type TEXT NOT NULL DEFAULT ''`、`confidence_score INTEGER NOT NULL DEFAULT 0`（当 `kind='audit_review'` 时允许 `image_url` 为旧图 URL 或空，卡片上展示当前有效旧图 + AI 怀疑理由 +「保留并标为合格 / 清空头像 / 联网找新图」操作）。
- **扩展 `artist_image_settings`**（新增配置项）：
  - `audit_auto_confidence INTEGER NOT NULL DEFAULT 85 CHECK(audit_auto_confidence BETWEEN 50 AND 100)`（高置信自动替换/清理阈值，默认 85）。
- **扩展 `ai_usage`**：`purpose` 新增 `'artist_image_audit'`。

### 决策 3：全量审查 + 自动获取替换核心状态机

- **任务模式与候选扫描（零 N+1，沿用批量扫描 + 一次目录枚举）**：
  - `audit_all`：扫描所有未合并且本人及合并来源均无自定义头像的艺人，排除已有 `status='pending'` 候选的艺人；对于无缓存图艺人直接纳入，对于有有效缓存图艺人仅当 `artist_image_reviews` 中不存在 `image_hash == 当前有效缓存 hash AND verdict == 'keep'` 时纳入。
  - `audit_force`：与 `audit_all` 相同，但忽略 `artist_image_reviews` 已通过记录及 `artist_image_attempts` 冷却期，强制重审全部非自定义头像艺人。
- **阶段 1：存量头像审查（`auditExistingArtistAvatar`）**：
  1. 先查有效缓存行（含合并源继承信息：记录 `ownerArtistID`、`source`、`remoteURL`、`contentHash`、`mimeType`、`cachePath`）。
  2. 本地读盘检查：文件可读性 + `netfetch.InspectImageQuality(data)`；若损坏/尺寸比例不合规，跳过 VLM 调用，零 token 直接判定 `{verdict: "replace", issueType: "garbage", confidence: 100, reason: "本地缓存图片损坏或分辨率/比例不符合头像要求"}`。
  3. 否则构造多模态审查请求（`purpose="artist_image_audit"`），发送当前头像 base64（若 >4 MiB 拒绝或先缩放？复用现有 4 MiB 保护，超限则记 `skipped` 并提示过大）+ 完整艺人锚点（`name`、`aliases`、`artistType`、`country`、`knownAlbums`、`knownTracks`、`currentSource`、`currentImageUrl`、`width`、`height`）。
  4. **严格错误语义**：审查请求遇到任何 `LLMError`（400/401/429/5xx）或 JSON 解析失败时**直接返回错误**（`outcome="failed"`，不写 `artist_image_reviews`，不改旧图），绝不静默当 `keep` 放行。
  5. 结果路由：
     - `verdict == "keep"` → 写入 `artist_image_reviews(verdict='keep')`，返回 `kept`。
     - `verdict == "replace"` 且 `confidence < audit_auto_confidence`（默认 85）→ **不自动联网搜图**，保留旧图，在 `artist_image_candidates` 写入一条 `kind='audit_review'` 候选供人工决策（保留/清空/手动联网搜图），返回 `candidate`。
     - `verdict == "replace"` 且 `confidence >= audit_auto_confidence` → 进入阶段 2。
- **阶段 2：高置信不合格后的多源获取与替换（`replaceAuditedArtistAvatar`）**：
  1. 构造排除集：该艺人已有 `artist_image_rejections`（URL + `content_hash`）+ 已有 `rejected` 候选 URL + **当前旧图的 `remote_url` 与 `content_hash`**。
  2. 依次尝试：
     - **Tier 1**：已确认外部资料的 `remote_image_url`（排除集内跳过）
     - **Tier 2**：已启用的 Fanart / Deezer / Bangumi（排除集内跳过）
     - **Tier 3**：若允许联网且单轮联网预算未耗尽，执行统一联网搜索（Tavily / DeepSeek）+ SSRF-safe DOM 抽图 + GPT 文本选图（排除集内跳过）
  3. **全链路 VLM 验图**：重抓阶段无论是 Tier 1、Tier 2 还是 Tier 3 下载到的候选图，在落库前都必须通过 `InspectImageQuality` + VLM 视觉验收（返回 `{valid: bool, confidence: 0-100, reason: string}`，同样非静默失败）：
     - 候选图下载后若 `sha256` 命中排除集，直接跳过继续试下一源；
     - 若 VLM 判 `valid=false`，跳过并尝试下一候选/下一源；
     - 只有候选图同时满足来源可信/文本选图高置信 + VLM `valid=true` 且 `confidence >= audit_auto_confidence` 时，才视为「高置信新图」；若 VLM 通过但综合置信度处于中档，则视为「中置信新图」。
  4. 按结果执行（所有写库操作均在单事务前置 `artistImageBackfillWriteLock` + 校验 `checkpoint` 与无并发自定义头像后提交）：
     - **高置信新图且 `web_auto_apply_high` 开启**：快照当前有效缓存（含合并源）写入 `artist_image_audit_actions(action='replaced')` → 更新 `artist_image_cache` → 将旧图 URL + hash 写入 `artist_image_rejections` → 写入新图 `artist_image_reviews(verdict='keep')` → 返回 `replaced`。
     - **中置信新图（或未开启自动落库）**：保留旧图不动 → 缓存预览字节并写入 `artist_image_candidates(kind='replacement')` → 待审页并排展示「当前旧图 vs 新候选图」→ 返回 `candidate`。
     - **全源无可用新图（1.A 差异化策略）**：
       - `issueType == "garbage"`：快照主艺人及所有合并源艺人的 `artist_image_cache` 行写入 `artist_image_audit_actions(action='cleared')` → 删除这些缓存行 → 将旧图 URL + hash 写入 `artist_image_rejections` → 记录负缓存冷却 → 返回 `cleared`。
       - `issueType` 为 `album_cover` / `low_quality` / `wrong_subject`：保留旧图不动 → 写入 `artist_image_candidates(kind='audit_review')` 附 AI 理由与问题类型供人工复核 → 返回 `candidate`。

### 决策 4：一键撤销、缓存刷新与 LLM 温度兼容

- **一键撤销（`RevertArtistImageAuditAction`）**：
  - 事务前置写锁，检查目标艺人未合并且无并发新增的 `artist_custom_images`；
  - 若当前为 `replaced`：按 `prev_cache_json` 恢复原有 `artist_image_cache` 行（校验磁盘 `cache_path` 文件仍存在，若文件缺失返回明确错误），删除对旧图的 `artist_image_rejections`（避免立刻又被拒），并将旧图 `image_hash` 写入 `artist_image_reviews(verdict='keep', model='manual_revert')` 防止下次 `audit_all` 再次误替；
  - 若当前为 `cleared`：按 `prev_cache_json` 逐行恢复主艺人/合并源艺人的 `artist_image_cache`，同样标记旧图 `manual_revert` 保留；
  - 标记该审计记录 `reverted = 1`。
- **缓存头像 URL 版本号（`internal/storage/track_extras.go`）**：
  - 将 `artistImageURLSQL` 的缓存分支从裸 `/api/v1/artists/{id}/image` 改为：
    - 优先本艺人缓存：`'/api/v1/artists/' || alias.id || '/image?v=' || substr(ai.content_hash, 1, 12)`
    - 其次合并源最新缓存（`ORDER BY ai.fetched_at DESC LIMIT 1`）：同样附带 `?v=substr(ai.content_hash, 1, 12)`
  - 保持与 `ArtistImagePath` 完全一致的优先级语义，自动替换或撤销后浏览器/客户端立即刷新。
- **LLM 温度参数兼容（`internal/tagging/llm.go`）**：
  - `ChatClient.Complete` 仍默认携带 `"temperature": 0` 保证既有模型行为与单测不变；
  - 当收到 `HTTP 400` 且错误消息提及 `temperature`（如 `unsupported value` / `temperature`）时，自动去掉 `temperature` 字段重试一次（并在该 `ChatClient` 实例按 `model` 缓存「跳过 temperature」标记，避免后续每次请求都白白多打一次 400）。
  - 同步优化 VLM 视觉质检：在审查模式与重抓验收中取消 400 静默返回 `valid=true` 的逻辑，改为显式返回错误让上层记为 `failed`。

### 决策 5：待审头像联网状态持久化与独立后端异步批量找图队列（迁移 `092_artist_image_candidate_search_state.sql`）

- **数据表扩展（增量迁移 `092`，严禁修改已发版的 `091`）**：
  ```sql
  ALTER TABLE artist_image_candidates ADD COLUMN web_search_state TEXT NOT NULL DEFAULT '' CHECK (web_search_state IN ('', 'not_found_auto', 'not_found_manual', 'failed'));
  ALTER TABLE artist_image_candidates ADD COLUMN web_search_note TEXT NOT NULL DEFAULT '';
  ALTER TABLE artist_image_candidates ADD COLUMN web_searched_at TEXT;
  ```
- **自动审查与手动找图状态落库**：
  - 在 `replaceAuditedArtistAvatar` 阶段，仅当高置信疑点确实实际执行了 Tier 3 联网搜索（通过配置检查且成功预扣预算）但未命中合格替代图时，写入 `audit_review` 候选记录并设置 `web_search_state = 'not_found_auto'`、`web_search_note = '全量审查已自动联网找图，未找到合适新图'`、`web_searched_at = now`；若因单轮预算耗尽或未配联网而跳过 Tier 3，则保持 `web_search_state = ''`，避免误标。
  - 手动联网搜图无结果时，更新对应 `audit_review` 行：`web_search_state = 'not_found_manual'`、`web_search_note = '手动联网找图未找到更合适的候选头像'`、`web_searched_at = now`；搜图出错时写入 `web_search_state = 'failed'` 及脱敏原因。
  - 手动联网搜图找到新候选图时，通过单事务 `ResolveAuditCandidateWithReplacement` 写入新 `replacement` 候选（保留原 `issue_type` 与理由上下文）并同步关闭原 `audit_review` 候选（置为 `rejected`），避免同一艺人在「待审头像」重复出现两张卡片；若新候选 URL 命中既有已拒绝记录导致未写入，则回滚并按 `not_found_manual` 标记原疑点卡。
- **后端串行异步找图队列（`internal/enrichment/artist_image_search_queue.go`）**：
  - 由 `enrichment.Manager` 持有独立内存队列（并发度固定为 1，通过 `m.goBackground` 托管并响应 `m.baseCtx` 停机退出），与 `artist_image_backfill_runs` 完全解耦且不受 `max_web_requests_per_run` 限制，确保在后台全量审查运行期间可平稳并行且不争抢过高并发。
  - 支持批量入队（按候选 ID 去重并过滤非 `pending` / 非 `audit_review` 项，运行中再次提交自动追加到队尾并累加 `total`）、移出单个待排队项、停止队列（清空未开始项并等待当前项完成后置为 `stopped`）、收起已完成摘要（复位 `idle`）及只读状态快照（含 `queuedPositions` 供每张卡片显示 `排队中 #N` 与 `搜图中` 图标）。
  - 管理路由：`GET /admin/matches/images/search-queue.json`（列入 `quietAccessLogPath`）、`POST /admin/matches/images/search-queue/enqueue`、`POST /admin/matches/images/search-queue/remove`、`POST /admin/matches/images/search-queue/stop`、`POST /admin/matches/images/search-queue/dismiss`，以及批量审核端点 `POST /admin/matches/images/candidates/batch-keep` 与 `POST /admin/matches/images/candidates/batch-clear`；既有 `POST /admin/matches/images/candidates/{id}/search` 保持向后兼容（AJAX/JSON 或表单提交均可入队并返回/跳转）。
- **管理后台「待审头像」工作台交互（对齐 `prototypes/avatar-review-queue.html`）**：
  - 顶部队列进度条实时 2 秒轮询，原地更新进度条、统计数字、当前搜图艺人以及每张卡片右上角状态图标徽章（排队中 `#N` 带单条移出 `×` / 搜图中旋转图标 / 自动已搜无果 / 手动已搜无果 / 搜图失败 / 已找到新图），不强制整页刷新以保护当前勾选态。
  - 网格上方提供状态筛选胶囊（`全部 / 未搜过 / 排队/搜图中 / 已搜无果 / 新图待对比 / 搜图失败`）、艺术家名实时过滤、一键「仅选未搜过」「全选本页可搜」「加入联网找图队列」「批量保留原图」「批量清空头像（二次确认）」。
  - `replacement` 对比卡片同步提供 `采纳替换 / 保留原图 / 清空头像 / 忽略` 操作，其中 `KeepArtistImageCandidate` 对 `replacement` 卡片会同时拉黑被拒新图 URL 并关闭同艺人其他待审项。

## Risks / Trade-offs

- **[全量审查对大曲库消耗较多 VLM token 与搜索额度]** → 默认 `audit_all` 严格按 `(artist_id, content_hash)` 幂等跳过已审通过项；中低置信旧图默认不自动消耗联网搜索额度；保留 `单轮联网上限` 配置与全局 RPM 限流器，且支持随时暂停/继续并断点续跑。
- **[Tavily 免费 Key 月额度（如 1000 次）在全量跑批中逐个耗尽]** → 多 Key 共享轮询池按 `402/432/433` 自动隔离耗尽 Key 6 小时并无缝切换下一把健康 Key，同时支持配置 `tavily_then_deepseek` 自动兜底，页面实时可见每把 Key 状态。
- **[VLM 偶发误判把正确的冷门艺人照片换成错误照片]** → 采用双高置信门槛（旧图 `>= 85` 不合格且新图 VLM 同样 `>= 85` 合格才自动替换）、1.A 策略避免无替代图时误删非垃圾人像，且每次自动替换/清理均留存旧图快照，管理页一键撤销并锁定为人工保留。
- **[合并艺人继承旧缓存导致清空无效]** → 审查与清理/撤销均解析并记录缓存归属 `owner_artist_id`，清空时连同合并源艺人缓存行一起快照并删除，撤销时原样还原。
- **[SQLite 重建父表 `artist_image_backfill_runs` 触发外键约束失败]** → 迁移 `091` 按「建 `runs_v2` → 迁 runs → 建 `items_v2`（引用 `runs_v2`）→ 迁 items → 删旧 items → 删旧 runs → 重命名两张新表」的顺序执行，无需关闭外键检查即可在事务内安全迁移，并配专门迁移测试覆盖含历史数据场景。

## Migration Plan

1. 升级部署后自动执行迁移 `091_artist_image_audit_and_web_search.sql`：
   - 自动创建 `web_search_settings` 并无损迁入原 `artist_alias_settings.deepseek_key`；
   - 安全重建 `artist_image_backfill_runs` / `artist_image_backfill_items` 并保留全部历史任务记录；
   - 创建 `artist_image_reviews`、`artist_image_rejections`、`artist_image_audit_actions` 并扩展 `artist_image_settings`、`artist_image_candidates`、`ai_usage`。
2. 用户升级后前往「AI 设置中心」（`/admin/settings/ai`）粘贴多把 Tavily API Key 并测试连通性，随后在 `/admin/matches` 选择「全量补全与历史头像 AI 审查（`audit_all`）」启动即可。
3. 回滚策略：原有缓存磁盘文件按内容哈希保留在 `data/artist-images/`，自动替换/清理的头像可直接在管理页逐条撤销；`?v=<hash>` 查询参数对所有客户端与既有 `/api/v1/artists/{id}/image` 处理器完全向后兼容。
