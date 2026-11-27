# Tasks

## 1. 数据库迁移与向导状态存储

- [x] 1.1 新增 `internal/storage/migrations/094_setup_wizard.sql`，创建 STRICT 单行表 `setup_wizard_state` 并在 SQL 中实现老实例自动识别（存在 `audio_files`、`scan_jobs` 或已配置的元数据/AI/联网搜索/同步/获取设置时初始化为 `completed`，全新空库初始化为 `pending`），运行 `go test ./internal/storage -run "TestMigration|TestBackup"` 验证空库迁移、升级迁移、完整性检查与备份表结构校验全部通过。
- [x] 1.2 新增 `internal/storage/setup_wizard.go`（实现 `SetupWizardState`、分步进度保存、`CompleteSetupWizard`、`SkipSetupWizard`、`DismissSetupWizardBanner` 与 `MarkSetupWizardCompleted`）及 `internal/storage/setup_wizard_test.go` 覆盖全新种子、各类老实例信号判定、步骤 JSON 持久化与状态流转，运行 `go test ./internal/storage -run TestSetupWizard` 验证通过。

## 2. 启动首次扫描暂缓与释放编排

- [x] 2.1 在 `cmd/server/main.go` 与 `internal/http/app.go` 接入 `setup_wizard_state` 启动判定与 `sync.Once` 保护的 `SetFirstRunScanReleaser(startScan, autoEnrich)`（含 `scannerManager.SetOnComplete` 中的一次性 `suppressNextPostScanEnrich` 原子抑制开关），编写单元测试覆盖 `pending` 暂缓扫描、带/不带自动增强释放及 `completed` 启动即扫回归，运行 `go test ./cmd/server ./internal/http -run TestFirstRunScan` 验证通过。

## 3. 初始化向导 HTTP 接口与入口路由

- [x] 3.1 在 `internal/http/setup_wizard.go` 实现 `GET /admin/setup`（组装无明文密钥的视图状态、检测未受信反向代理与临时 `MEDIA_TOKEN` 提醒、检测声学分析 Sidecar 状态），并在 `GET /admin` 与 `GET /admin/home` 中对 `pending` 状态执行 `303` 重定向至 `/admin/setup`（保持 `afterLoginPath` 及非着陆深链可用）；同步更新既有测试夹具默认标记已完成并编写重定向单测，运行 `go test ./internal/http -run "TestLogin|TestSetupWizardRedirect"` 验证通过。
- [x] 3.2 实现带 CSRF 校验的分步保存与流转接口（`POST /admin/setup/step/1` 至 `/step/4`、`POST /admin/setup/complete`、`POST /admin/setup/skip`、`POST /admin/setup/banner/dismiss`），直写既有权威设置表并落实 MusicBrainz 联系信息必填校验、密钥留空保留、第 1 步与第 4 步 Last.fm API Key 互通及 `pending` 态抑制 Worker 提前唤醒；补齐单测并运行 `go test ./internal/http -run TestSetupWizardStep` 验证通过。
- [x] 3.3 实现带 CSRF 校验的页内异步测试与有界目录预检 JSON 接口（`GET /admin/setup/preflight/library`、`POST /admin/setup/test/llm`、`/web-search`、`/listenbrainz`、`/lastfm/connect`、`/lastfm/complete`、`/jackett`、`/qb`），支持未保存参数覆盖与已存密钥回退，编写带 Mock HTTP 服务与临时音乐目录的单元测试并运行 `go test ./internal/http -run TestSetupWizardTest` 验证通过。

## 4. 专注态前端界面、控制台续配入口与文档

- [x] 4.1 参照已确认原型 `prototypes/first-run-setup.html` 实现 `internal/http/templates/setup.html`、`internal/http/assets/setup.css` 与 `internal/http/assets/setup.js`（5 步进度轨、深浅主题切换、按需顶部部署告警条、服务商快选预设、Tavily 多 Key 计数、DeepSeek/Last.fm 复用提示、第 3 步前置锁定与 3 套预设、页内异步测试及第 5 步目录预检与流水线预览），在 `internal/http/assets.go` 与模板函数表注册资源辅助函数，运行 `go test ./internal/http -run "TestAssets|TestSetupWizardPage"` 验证通过。
- [x] 4.2 更新 `internal/http/templates/dashboard.html` 与 `handleDashboard`，在 `skipped` 且未关闭提示时渲染可关闭续配提示条并永久保留「重新打开初始化向导」入口（带 `data-no-pjax` 整页跳转），同步补充 `README.md` 首次启动引导说明，运行 `go test ./internal/http -run TestDashboardSetupBanner` 验证通过。

## 5. 端到端集成与浏览器回归验证

- [x] 5.1 运行全量测试与编译（`go test ./... && go build ./...`），并通过 Playwright 在桌面端（`1440x900`）与移动端（`390x844`）浅色/深色主题下端到端验证「全新登录 → 自动进入 `/admin/setup` → 5 步保存与页内测试 → 第 5 步目录预检并启动首次扫描 → 跳过与关闭控制台续配条 → 完成后重开向导」全链路，确认零测试失败、零控制台报错且移动端零横向溢出。
