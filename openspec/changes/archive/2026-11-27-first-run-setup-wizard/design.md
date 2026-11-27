# Design

## Context

032 Music Server 是基于 Go + SQLite（STRICT 表、迁移 `001`–`093`、带表结构校验的一数据库快照备份）与服务端 HTML 模板（`internal/http/templates/*.html` + `internal/http/assets/` 内容寻址静态资源）构建的单用户自托管音乐服务。

当前约束与现状：
- `config.Load()` 在启动阶段校验 `.env` 最小凭据（`MUSIC_SERVER_ADMIN_PASSWORD`、`MUSIC_SERVER_API_TOKEN`、`MUSIC_SERVER_MEDIA_TOKEN`、`MUSIC_SERVER_MUSIC_DIR`）。
- `cmd/server/main.go` 启动后直接执行 `watcher.Start(rootCtx, true)`，在扫描结束回调 `scannerManager.SetOnComplete` 中串联元数据增强与全部后台队列；但全新空库下各数据源、LLM、联网搜索与 AI 能力均为关闭或未填状态。
- 所有设置分散存储在 13 张既有 SQLite 配置表中，由 7 个独立管理页面与弹窗分别维护。

需求背景见 `proposal.md`，对外行为契约见 `specs/first-run-setup-wizard/spec.md`，已确认的视觉与交互原型为 `prototypes/first-run-setup.html`。

## Goals / Non-Goals

**Goals：**
- 通过轻量无密钥表 `setup_wizard_state`（迁移 `094_setup_wizard.sql`）持久化向导状态，并在迁移 SQL 中原子完成新老实例识别。
- 在全新 `pending` 实例上安全暂缓首次扫描与目录监控，待第 5 步确认或用户主动跳过后一次性释放；支持首次扫描仅导入本地标签的一次性抑制开关。
- 所有向导表单直写既有 13 张权威配置表，复用现有校验逻辑、AI 限流器刷新、备份恢复密钥隔离与写后不回显明文约束。
- 在 `/admin/setup/...` 下提供带 CSRF 校验的 JSON 接口，支持分步保存、页内异步连通性测试、Last.fm 两步 OAuth、目录有界只读预检、跳过、完成与关闭续配条。

**Non-Goals：**
- 不改动 `.env` 启动最小凭据契约，不引入登录前免密初始化账号接口。
- 不替代或移除现有独立设置页（`/admin/settings/ai`、`/admin/settings/metadata`、`/admin/settings/acquisition` 等），它们仍作为完整高级管理页保留。
- 不修改音频源文件，不改变扫描器既有安全护栏（S1 挂载丢失保护、屏蔽规则计数等）。

## Decisions

### 1. 向导状态表设计与升级判定（迁移 `094_setup_wizard.sql`）
- **决策**：新增单行 STRICT 表 `setup_wizard_state`：
  ```sql
  CREATE TABLE setup_wizard_state (
      id INTEGER PRIMARY KEY CHECK (id = 1),
      status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'skipped', 'completed')),
      current_step INTEGER NOT NULL DEFAULT 1 CHECK (current_step BETWEEN 1 AND 5),
      step_states_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(step_states_json)),
      banner_dismissed INTEGER NOT NULL DEFAULT 0 CHECK (banner_dismissed IN (0, 1)),
      updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
  ) STRICT;
  ```
  迁移 `094` 在初始化 `id = 1` 时，只要满足以下任意老实例信号即置为 `status = 'completed'`，否则置为 `'pending'`：
  - `audio_files` 或 `scan_jobs` 存在任意记录；
  - `metadata_source_settings` 存在已启用项或已填 `api_key`；
  - `ai_settings` 已填 `api_key` 或 `model`；
  - `web_search_settings` 已配 Tavily Keys 或 `deepseek_key`；
  - `acquisition_settings` 已启用或已填 `jackett_api_key`；
  - `lastfm_scrobble_settings` 已启用或已存在 `session_key`；
  - `listenbrainz_settings` 已启用或已填 `user_token`。
- **理由**：单条原子 SQL 同时覆盖全新安装、原地升级与旧快照恢复迁移；表内不含任何密钥，天然兼容 `internal/storage/backup_restore.go` 的凭据保留策略与 `backup_schema.go` 校验器。
- **备选方案**：在 `/data` 写本地标志文件。因与 SQLite 快照恢复容易产生状态分裂而被否决。

### 2. 登录后路由拦截边界（保持 `loginNext` 与深链兼容）
- **决策**：保持 `handleLogin` / `afterLoginPath(next)` 逻辑不变（深链仍回原址，无 `next` 回 `/admin`）；仅在 `handleDashboard`（`GET /admin`）与 `handleHome`（`GET /admin/home`）开头读取 `SetupWizardState`，当 `status == "pending"` 时返回 `303 See Other` 跳转至 `/admin/setup`。
- **理由**：
  - 覆盖所有常规首次访问路径（`/`、`/admin`、`/admin/home` → `/admin/login` → 登录后落地 `/admin` 或 `/admin/home` → 自动转入 `/admin/setup`）；
  - 不破坏 `login_next_test.go` 的登录重定向安全契约；
  - 管理员在向导顶部看到部署安全提醒后，可以直接点击进入 `/admin/settings/security` 处理 Media Token，不会被全局中间件死循环拉回向导；
  - 既有 HTTP 测试夹具（`setupTestApp`、`credentialTestApp` 等）可在初始化中默认调用 `MarkSetupWizardCompleted` 避免无关回归，另由专门的 `setup_wizard_test.go` 完整测试 `pending` 链路。
- **备选方案**：在 `requireAdmin` 全局中间件拦截所有 `/admin/*` 请求。因会阻断安全设置页直达、轮询接口及静态资源而否决。

### 3. 首次扫描暂缓、一次性释放与「仅导入本地标签」开关
- **决策**：在 `cmd/server/main.go` 启动阶段读取 `setupState`：
  - 注册并发安全（`sync.Once`）的释放回调 `app.SetFirstRunScanReleaser(func(startScan bool, autoEnrich bool))`（绑定 `rootCtx`）：
    - 当 `status == pending`：启动时不调用 `watcher.Start(rootCtx, true)`，避免首次启动即触发全目录 `computeFingerprint()` stat 遍历和裸扫；
    - 当管理员在第 5 步点击完成（`startScan` 与 `autoEnrich` 按表单传入）或点击跳过（`startScan=true, autoEnrich=false`）时：
      1. 持久化 `setup_wizard_state`（`completed` 或 `skipped`）；
      2. 若 `startScan && !autoEnrich`，将原子标志 `suppressNextPostScanEnrich` 置为 `true`；
      3. 调用 `app.applyLibraryWatchSettings(rootCtx)` 并在 `sync.Once` 内调用 `watcher.Start(rootCtx, startScan)`；
    - 在 `scannerManager.SetOnComplete` 回调中：本地数据一致性收尾（`CompleteReleaseGroupRebuild`、`RefreshReleaseGroupIdentities`、`ReconcileAlbumLoudness`、`GCCustomImages`）照常执行；仅当 `suppressNextPostScanEnrich.Swap(false)` 为 `true` 时，跳过本次的 `acquisitionService.Wake()`、`enrichmentManager.StartAuto()`、`analysisService.Wake()`、`loudnessService.Wake()`、`taggingService.Wake()` 与 `aliasService.Wake()`。
  - 当启动时已为 `completed` 或 `skipped`：与现有逻辑完全一致，立即执行 `watcher.Start(rootCtx, true)`。
- **理由**：配置期间对大曲库 NAS 零额外 I/O 开销；若进程在未完成向导时退出，`scannerManager.Wait()` 立即返回；重开向导修改配置时 `sync.Once` 已消费，绝不误停监控器。
- **备选方案**：启动时传 `interval=0` 启动 `watcher`。因 `watcher.run()` 启动仍会遍历整个曲库计算基线指纹，故否决。

### 4. 分步直写既有配置表与写后不回显保护
- **决策**：在 `internal/http/setup_wizard.go` 中实现带 `X-CSRF-Token` 校验的 JSON 写入端点：
  - `POST /admin/setup/step/1`：写 `metadata_source_settings`（启用 MusicBrainz 强制校验 `contact` 非空；Last.fm 留空保留旧 `api_key`）、`artist_biography_settings`（`TargetLanguage`、`WikipediaEnabled`）、`artist_image_settings`（结构化图源、`FanartAPIKey` 与 `FanartEnabled`）。
  - `POST /admin/setup/step/2`：写 `ai_settings`（连接、模型、RPM/并发/代理/思考强度，留空保留 `api_key`/`proxy_url`，并同步更新 `sharedAILimiter.SetRate`）、`artist_image_settings.VisionModel`、`web_search_settings`（路由策略、Tavily 多 Key、DeepSeek 联网 Key，留空保留旧值）。
  - `POST /admin/setup/step/3`：批量写各能力开关（`ai_review_settings`、`artist_alias_settings.Enabled`、`artist_image_settings` 的 `WebFallbackEnabled`/`AutoIngestAIEnabled`、`artist_biography_settings` 的 `AITranslateEnabled`/`AIWebFallbackEnabled`、`ai_settings` 的 `TranslationEnabled`/`TaggingEnabled`、`daily_mix_settings` 的 `Enabled`/`LLMTitles`、`audio_analysis_settings.Enabled`）。当向导仍为 `pending` 时仅落库不提前唤醒后台 Worker；重开向导（`completed`/`skipped`）保存时正常唤醒对应服务。
  - `POST /admin/setup/step/4`：写 `listenbrainz_settings`、`lastfm_scrobble_settings`（若第 4 步填写了 `lastfm_api_key`，同步更新 `metadata_source_settings('lastfm')`）、`acquisition_settings`。
  - `POST /admin/setup/complete` / `POST /admin/setup/skip` / `POST /admin/setup/banner/dismiss`：写 `library_watch_settings`、`setup_wizard_state`，并按需触发扫描释放器。
- **理由**：与既有单页设置共用唯一数据源，无双份配置同步漂移，随时中断或跨页修改状态始终一致。

### 5. 页内异步测试与有界目录预检
- **决策**：新增 CSRF 保护的 JSON 探测端点（提交参数优先，密钥留空自动回退数据库已存密钥）：
  - `POST /admin/setup/test/llm`：复用 `a.aiTagging.TestConnection` / `aiclient.CompleteWithFallback`；
  - `POST /admin/setup/test/web-search`：复用 `a.webSearchRegistry().TestConnection`；
  - `POST /admin/setup/test/listenbrainz`：复用 ListenBrainz token 校验逻辑并返回账号名；
  - `POST /admin/setup/test/lastfm/connect` 与 `/complete`：复用既有 Last.fm OAuth 授权令牌申请与会话交换逻辑，返回 JSON（`pendingAuthURL` / `username`）；
  - `POST /admin/setup/test/jackett` 与 `/qb`：复用 `acquisition` 连接探测逻辑；
  - `GET /admin/setup/preflight/library`：对 `cfg.MusicDirectory` 执行 `os.Stat` + 有界只读 `os.ReadDir`（限制最大遍历条目如 500 项、最大深度 2 层、超时 2 秒），返回是否挂载可读、发现的一级子目录/音频文件计数与可操作排查建议。
- **理由**：避免旧设置页表单 POST 后 303 重定向导致向导状态丢失；有界预检在超大型 NAS 曲库上也能毫秒级返回。

### 6. 前端专注壳层与静态资源注册
- **决策**：参照 `prototypes/first-run-setup.html` 实现 `internal/http/templates/setup.html`、`internal/http/assets/setup.css`、`internal/http/assets/setup.js`，在 `internal/http/assets.go` 与模板函数表中注册 `assetSetupCSS` / `assetSetupJS`；在 `internal/http/templates/dashboard.html` 增加可关闭续配提示条与「重新打开初始化向导」入口（标记 `data-no-pjax` 确保整页切换进出专注向导壳层）。
- **理由**：独立壳层不加载播放器与右侧队列脚本，首帧轻量且与 `DESIGN.md`、`tokens.css` 完整一致。

## Risks / Trade-offs

- **[既有 HTTP 测试在全新空库触发 `pending` 重定向导致回归]** → 在 `internal/http/` 共用测试夹具（`setupTestApp` 等）中默认标记向导已完成，同时在 `internal/http/setup_wizard_test.go` 中使用未标记的全新存储完整覆盖首次重定向、暂缓扫描、跳过与升级场景。
- **[第 3 步开启打标/声学分析后若立即唤醒会绕过第 5 步扫描暂缓]** → 第 3 步保存时检查当前是否处于 `pending`，`pending` 状态下仅写设置不唤醒 Worker，统一由第 5 步释放扫描后在 `SetOnComplete` 中按序串联。
- **[向导填写中途容器重启或浏览器刷新]** → 每步保存即时落库并更新 `current_step` 与 `step_states_json`，重启或刷新后自动恢复进度，已存密钥仅展示配置状态不回显明文，启动扫描继续保持暂缓直至完成或跳过。
- **[超大曲库目录预检耗时或目录层级较深]** → 限制 `preflight/library` 遍历上限与上下文超时，只要根目录可读且包含子目录或受支持音频文件即判定挂载可用，不误拦深层目录结构。
- **[恢复旧版本数据库快照]** → 恢复早于迁移 `094` 的旧快照时，启动迁移会重新执行 `094` 并依据快照中已有的曲库/设置记录自动置为 `completed`，不会误弹首次引导。

## Migration Plan

1. 新增迁移文件 `internal/storage/migrations/094_setup_wizard.sql`，建表并原子写入初始状态。
2. 验证全新空库迁移、从 `093` 升级至 `094`、恢复旧快照后迁移均通过 `PRAGMA integrity_check`、`PRAGMA foreign_key_check` 与 `Backups` 表结构校验。
3. 回滚策略：可回退二进制并使用 `cmd/server/main.go` 迁移前自动生成的 `preupgrade` 一致快照恢复。
