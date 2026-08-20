# 032music-server 全面缺陷审计报告

> **审计日期**:2026-08-18
> 审计范围:全部 Go 源码(25 个文件)、16 个模板、3 个前端 JS、12 个 SQL 迁移、Dockerfile / compose / 脚本、Git 仓库卫生。
> 基础验证:`go vet` / `go build` / `go test ./...` 全部通过。

> **处理状态(2026-08-20 更新)**:S1-S2、H1-H4、M1-M4、M6-M14 已全部修复,`go build` / `go vet` / `go test ./...` 全绿,并完成冒烟验证(登录限流 429、会话重启后仍有效、迁移 013/014 应用正常)。M5(内容哈希重连)经评估本期不做;全部 L 级项暂未处理。
> **遗留手动事项**:① 重启生产服务以使 rc.txt 泄露的会话失效并加载新代码;② `.env` 增加 `MUSIC_SERVER_MEDIA_TOKEN`(compose.yaml 已改为必填);③ 媒体端点不再接受 API token,客户端须改用 media token。

## 严重(立即修复)

### S1. 空目录/挂载丢失 → 一次扫描清空整个音乐库及全部客户端数据 ✅ 已修复(2026-08-20)
- 证据链:`internal/scanner/scanner.go:106-114`(每次扫描结束无条件 `MarkMissing` + `CleanupOrphans`)→ `internal/storage/library.go:439-450`(`CleanupOrphans` 删除所有无 available 音频文件的 track → 无 track 的 album → 无关联的 artist)→ `migrations/008_client_features.sql`(`playlist_items`、`playback_progress` 均 `ON DELETE CASCADE`,收藏是 tracks/albums 行内字段)。
- 触发:`docker run` 忘记挂 `/music`(Dockerfile 声明了 `VOLUME ["/music"]`,空匿名卷)、SMB/NFS 挂载掉线但目录存在、库路径改错。`discover()` 返回 0 个文件且无错误时扫描照常"成功"。
- 影响:收藏、歌单、断点、播放次数、歌手身份确认、简介缓存全部不可逆删除。README 明确承诺"重新扫描不会覆盖这些客户端数据",实际不成立。
- 修复:① `discovered == 0` 或 missing 占比异常(如 >50%)时拒绝执行 MarkMissing/CleanupOrphans 并把任务标记为失败;② missing 的 track 改为保留记录(软删除),仅标记状态;③ 至少给 `CleanupOrphans` 加干跑阈值与日志。
- **处理**:①②③均已实施。`scanner.go` 采用"保护阈值"方案:0 文件发现(库内有可用文件时)或将变 missing 的比例 ≥50%(`missingGuardRatio`)时任务标记 failed 且不 reconcile;`CleanupOrphans` 不再删除 tracks 行,missing 曲目及收藏/播放数据全部保留。回归测试 `scanner_test.go`(空发现拒绝扫描、missing 保留)通过。

### S2. 含活动会话凭据的文件被提交进 Git ✅ 已修复(2026-08-20)
- `rc.txt`(最新提交 `394589f`)内含明文 curl cookie jar:`music_server_admin_session=gtDpvJGJLbIe-...`。会话有效期 8 小时、服务端内存存储,若服务仍在运行且未过期,拿到仓库即可直接接管管理面板。`track16.bin`(64KB 无名二进制)同样被提交。
- 修复:`git rm --cached rc.txt track16.bin` 并加入 `.gitignore`;服务端重启(内存会话即失效);审计 git 历史是否需要清洗。
- **处理**:已 `git rm --cached rc.txt track16.bin`,并加入 `.gitignore` 与 `.dockerignore`;git 历史不清洗(沿用原决定)。**仍需手动:重启生产服务使泄露的会话失效。**

## 高

### H1. 全部后台 goroutine 无 panic 恢复且脱离进程生命周期 ✅ 已修复(2026-08-20)
- `scanner.go:52`、`enrichment/manager.go:116`、`enrichment/phase4.go:91,156`、`main.go:57,63` 均以 `context.Background()` 启动,无 `recover`。
- 影响:后台任务任何 panic 直接崩溃整个进程(HTTP 层的 `recoverPanic` 管不到);收到 SIGTERM 后 `server.Shutdown` 只等 HTTP,`run()` 返回即 `db.Close()`,扫描/匹配仍在写库 → 数据损坏或错误风暴。
- 修复:后台 goroutine 统一 `defer recover`;引入可取消的根 context + `sync.WaitGroup`,关机时先取消再等待再关库。
- **处理**:scanner/enrichment 全部后台任务统一走 `goBackground`(recover + WaitGroup),`main.go` 引入 `rootCtx`;关机顺序为 rootCancel → HTTP Shutdown → 等待后台任务退出(30s 超时)→ 关库。

### H2. 生产环境硬编码弱口令后门 ✅ 已修复(2026-08-20)
- `internal/config/config.go:77`:`len(c.AdminPassword) < 12 && c.AdminPassword != "admin"` —— 任何环境(含 Docker 生产)都可用 `admin/admin` 通过校验。
- 修复:仅当显式设置 `MUSIC_SERVER_DEV_MODE=1` 之类开关时才放行弱口令。
- **处理**:新增 `MUSIC_SERVER_DEV_MODE` 开关(`config.DevMode`),仅 dev mode 下放行弱口令,启动时输出 WARN;`.env.example` 已补 `MUSIC_SERVER_DEV_MODE=false`。

### H3. 媒体 Token 默认回退到主 API Token,且媒体端点接受多种查询参数名 ✅ 已修复(2026-08-20)
- `config.go:28`(mediaToken 缺省=apiToken)、`compose.yaml:15`(默认显式置空 → 回退生效,即默认 compose 部署就是此状态)、`internal/http/app.go:344-351`(接受 `mediaToken`/`apiToken`/`token`,且 `APIToken` 本身也能过媒体鉴权)。
- 影响:URL 中的 token 会进入浏览器历史、代理日志、Referer;回退模式下泄露只读媒体 URL = 泄露全权 API Token(可 PATCH/DELETE 全库)。
- 修复:生产模式禁止回退(未设置 MEDIA_TOKEN 时拒绝启动或自动生成一次性随机值并打印);媒体端点不再接受 APIToken。
- **处理**:生产模式 `MEDIA_TOKEN` 未配置时启动即失败(校验报错);dev mode 下才允许回退并打 WARN。媒体端点仅接受 `mediaToken`/`token` 查询参数且只比对 `MediaToken`,API token 一律拒绝。`compose.yaml` 已将 `MUSIC_SERVER_MEDIA_TOKEN` 改为必填。

### H4. 登录接口无速率限制 ✅ 已修复(2026-08-20)
- `app.go:259-279`:失败后仅 `time.Sleep(250ms)`,不阻塞并发请求,无失败计数/锁定/验证码。与 H2 叠加,`admin/admin` 几秒内可被撞开;任意 12 位弱密码也可在线爆破。
- 修复:按 IP+用户名做指数退避或计数锁定(如 10 次失败锁 15 分钟)。
- **处理**:新增 `internal/http/login_limit.go`,按 IP+用户名计数,10 次失败锁 15 分钟(锁定期间正确密码也返回 429),成功后清零;实测通过。

## 中

### M1. SSRF:按上游 API 返回的 URL 抓取歌手图片 ✅ 已修复(2026-08-20)
- `enrichment/manager.go:487-530` `CacheArtistImage` 只校验 scheme 为 http/https,不过滤内网/保留地址。URL 来源于 Last.fm 响应、Spotify oEmbed、MusicBrainz 关联数据(社区可编辑)。
- 影响:上游数据被污染时可让服务器请求内网服务/云元数据(169.254.169.254)。
- 修复:解析主机名并拒绝 RFC1918/loopback/link-local;或限定允许的主机白名单。
- **处理**:`CacheArtistImage` 增加 `validatePublicImageURL`,DNS 解析后拒绝 loopback/私网/link-local/组播地址,重定向目标同样校验。

### M2. MusicBrainz 限流被旁路 ✅ 已修复(2026-08-20)
- `mbRequest` 有 1 req/s 全局限流(`manager.go:377-402`),但 `phase4.go:495`(artist-relations)和 `manager.go:636`(简介用 url-rels)走 `cachedJSON` 直连 MB,无限流。大批量任务会违反 MB 使用条款,有封 IP 风险。
- 修复:所有 musicbrainz.org 请求统一经过 `mbRequest` 的限流器。
- **处理**:`cachedJSON` 中凡 `req.URL.Hostname()=="musicbrainz.org"` 的请求一律先走 `waitMBRateLimit`(全局 1 req/s)。

### M3. HTTP 缓存把错误响应也缓存 30 天 ✅ 已修复(2026-08-20)
- `phase4.go:248`:`PutHTTPResponseCache` 在状态码判断之前执行,4xx/5xx(含限流 503、网关抖动)按 TTL(默认 30 天)缓存。瞬时故障固化为长期失败,只能 force 或等过期。
- 修复:只缓存 2xx 与 404;错误响应至多短 TTL(分钟级)。
- **处理**:`cachedJSON` 仅缓存 2xx 与 404,其余状态码不写缓存。

### M4. works.normalized_title 全局唯一 → 同名作品被自动合并 ✅ 已修复(2026-08-20)
- `storage/works.go:272` `ON CONFLICT(normalized_title)`:两个不同动画/游戏若同名(现实中常见,如重制版、撞名),其曲目会被自动关联到同一个 work。
- 修复:唯一键改为 (normalized_title, type, year) 或引入消歧字段。
- **处理**:新增迁移 `013_works_identity_key.sql`,重建 works 表并建唯一索引 `(normalized_title, type, IFNULL(year,0))`;`ensureAutoWorkAssociation` 改为先按 (title,type) 查找(优先精确 type、NULL year),不存在才插入。回归测试 `works_identity_test.go` 通过。

### M5. 文件移动/重命名即丢失客户端数据(S1 的日常版) ⏭️ 本期不做(已确认)
- 文件改名/换目录 = 旧路径 missing + 新路径新 track。`is_favorite`、`play_count`、歌单项随旧行删除,无法迁移。
- 修复:用内容哈希(`audio_files.content_hash` 字段已存在但未启用)或 (大小+时长+ acoustid 类指纹)在 cleanup 前尝试重连旧 track 行。
- **处理**:本期不做(用户确认)。S1 修复后 missing 记录不再被删除,改名造成的损失已大幅降低;内容哈希重连留待后续迭代(可复用 `content_hash` 死字段,见 L14)。

### M6. `ListAlbums` 专辑总字节数统计错误 ✅ 已修复(2026-08-20)
- `library.go:509`:`SUM(DISTINCT af.file_size)` —— 两张不同文件大小相同时只计一次(同专辑内同码率曲目大小相近/相同很常见),`totalBytes` 系统性低估。
- 修复:改为对 `af.id` 去重后求和(子查询 `SUM` + `GROUP BY album_id`)。
- **处理**:`SUM(DISTINCT af.file_size)` 替换为按专辑的标量子查询(仅统计 available 文件),索引分支子查询同步补 `status='available'` 过滤。

### M7. 多处 N+1 查询 ✅ 已修复(2026-08-20)
- `storage/client_features.go:90,117,283,347`(收藏/歌单详情/播放历史逐行 `AlbumByID`/`TrackByID`)、`http/identity.go:283-284`(match-review 每个歌手一次)、`http/enrichment.go:147-166`(审核页 200 次查询)、`storage/identity.go:167-178`(`ArtistForMatching` 全表拉出再内存过滤)。
- 影响:大库管理页/API 明显变慢,SQLite 连接池(8)被占满。
- 修复:批量 JOIN 查询替代逐行查询。
- **处理**:新增共享 SELECT 常量 + `tracksByIDs`/`albumsByIDs` IN 批量加载(保持输入顺序);收藏列表、歌单详情、播放历史、match-review、enrichment 审核页(`PendingArtistCandidates`/`PendingWorkMatchCandidates`/`PendingArtistRelationCandidates` 批量 map)全部改批量;`ArtistForMatching` 改为单行直查。

### M8. 扫描健壮性与性能 ✅ 已修复(2026-08-20)
- `scanner.go:129-131`:WalkDir 任一条目报错即整个扫描失败(一个权限损坏的目录废掉整次扫描)——应记录并跳过。
- `scanner.go:104`:每个文件一次 `UpdateScanJob` 写库;`ImportTrack` 每轨一个数十语句的大事务 + `ensureArtists` 逐人两次往返。10 万级曲库全扫会非常慢。进度应批量(如每 100 文件或每秒)。
- `scanner.go:153-167`:同一文件夹的 cover.jpg 每轨都重新读盘+哈希。
- **处理**:WalkDir 条目错误改为记录并跳过(不再整次失败);进度写库批量为每 100 文件或 1 秒一次;封面按目录缓存(`map[string]*storage.ArtworkInput`,nil 表示该目录无封面),同目录只读盘一次。

### M9. `ensureArtists` 显示名"最后扫描者胜出" ✅ 已修复(2026-08-20)
- `library.go:422`:`ON CONFLICT(identity_key) DO UPDATE SET display_name=excluded.display_name` —— 同一歌手不同文件里大小写/拼写不同时(`YOASOBI` vs `Yoasobi`),显示名随扫描顺序抖动。
- 修复:仅在 display_name 为空/或从未人工或外部确认过时更新,或保留首个非空值。
- **处理**:`ensureArtists` 改为 `ON CONFLICT(identity_key) DO NOTHING`,保留首个写入的显示名。

### M10. 客户端可污染曲目时长 ✅ 已修复(2026-08-20)
- `client_features.go:314,325`:`UpdatePlayback`/`Scrobble` 用客户端上报的 duration 回填 `tracks.duration_ms`。异常客户端可写入错误时长(影响 UI、scrobble 阈值)。
- 修复:只信任扫描探测值;客户端值仅在合理性范围(如与文件大小码率推算相差 <50%)时回填,或单独存 client_duration。
- **处理**:`UpdatePlayback`/`Scrobble` 仅在探测值为空(0)且客户端值合理(>0 且 ≤6h,`plausibleClientDuration`)时才回填 `tracks.duration_ms`。

### M11. 会话仅内存存储 ✅ 已修复(2026-08-20)
- `session.go`:重启即全员掉线(含 CSRF token)。可用性缺陷;若要做持久化注意加密存储。
- **处理**:会话持久化到 SQLite(迁移 `014_admin_sessions`,仅存 token 的 SHA-256 哈希 + CSRF token + 过期时间),内存 map 作写穿缓存,重启后已登录会话仍有效(实测通过);过期会话定期清理。

### M12. 迁移不幂等 ✅ 已修复(2026-08-20)
- 002/003 等 `ALTER TABLE ... ADD COLUMN` 无存在性判断;任何曾手工/旧版本建过同名列的库启动即永久失败,且无修复路径。
- 修复:迁移框架支持列存在性探测,或为每个迁移提供修复 SQL 文档。
- **处理**:迁移框架重写——`ADD COLUMN` 逐行探测已存在则跳过,`CREATE TABLE/INDEX/TRIGGER` 自动改写 `IF NOT EXISTS`;每个迁移在独立事务中执行,前后切换 `PRAGMA foreign_keys`(支持表重建式迁移)。回归测试 `TestMigrateIdempotentWithPreExistingColumns` 通过。

### M13. Docker 构建上下文包含凭据文件;go.sum 未参与依赖层 ✅ 已修复(2026-08-20)
- `.dockerignore` 未排除 `rc.txt`/`track16.bin`(进入构建上下文与缓存层;不进最终镜像)。`Dockerfile:5-6` 只 `COPY go.mod`,`go mod download` 阶段不做校验且换 go.sum 不触发重建。
- 修复:dockerignore 排除;`COPY go.mod go.sum ./`。
- **处理**:`.dockerignore` 已排除 `rc.txt`/`track*.bin`;Dockerfile 改为 `COPY go.mod go.sum ./` 并 `go mod download && go mod verify`。

### M14. SQLite 并发写风险 ✅ 已修复(2026-08-20)
- `store.go:61` 8 连接 + WAL 单写者 + `busy_timeout(5000)`:扫描大批量写入与播放上报/收藏并发时可能超时失败(当前错误仅 500 返回)。
- 修复:写路径收敛到单连接或加重试(`SQLITE_BUSY` 时指数退避)。
- **处理**:新增 `retryDB` 包装(`internal/storage/busy.go`),`ExecContext`/`BeginTx` 遇 SQLITE_BUSY/LOCKED 指数退避重试(20ms→500ms 上限,10 次,尊重 ctx 取消);读路径不重试(WAL 下读者不阻塞)。

## 低

> 以下 L 级项本期均未处理,按优先级排期后续迭代。

| # | 位置 | 问题 |
|---|---|---|
| L1 | `http/library.go:312-318` 等 | `apiResult`/`writeAPIError` 把内部错误(含 SQL 细节)直接返回客户端 |
| L2 | `http/client_features.go:239` | 靠错误字符串包含 "must"/"invalid" 分类 400/500,脆弱 |
| L3 | `http/library.go:561` | `parseInt64` 非法 ID 静默为 0;更新类接口对 id=0 返回 204 假成功 |
| L4 | `http/lyrics.go:28` | `.lrc` 无大小限制,单文件全量读入内存 |
| L5 | `metadata/reader.go:744-768` | `" remix"`、`" instrumental"` 子串误匹配(如 "DJ Remixer"、"Instrumentalist") |
| L6 | `storage/sync.go` | `SyncTrack` 无 `updatedAt`/版本戳,客户端无法增量感知元数据变更(设计缺口) |
| L7 | 多处 | LIKE 通配符 `%`/`_` 未转义(非注入,仅匹配语义意外) |
| L8 | `enrichment/manager.go:214-216` | 自动模式 `queriedSources==0` 返回 `AutoMatched:true`,任务统计虚高 |
| L9 | `scanner.go:134-141` | 跳过符号链接与 `.` 开头目录,未文档化,可能漏扫 |
| L10 | `migrations/012` | `album_external_profiles UNIQUE(source, external_id)`:两个本地专辑同货号时第二个永远无法存 profile |
| M15 | `enrichment/manager.go:896-900` | Last.fm 简介仅截断 `<a href=`,仍含 HTML;当前靠 html/template 转义兜底(模板层安全,API 消费者会吃到原始 HTML) |
| L11 | `http/app.go:315,332` | API/媒体端点 `Access-Control-Allow-Origin: *`(媒体端跨域播放需要;API 端配合 cookie 时浏览器会拦截 credentials,实际风险低,但建议 API 端收窄) |
| L12 | `http/app.go:281-289` vs `session.go:99` | 创建 cookie 用 SameSite=Lax、删除用 Strict,不一致(无功能影响) |
| L13 | 工作区 | `server.exe` 编译产物在工作区(已 gitignore,未提交,仅提醒勿提交) |
| L14 | 迁移 001 | `audio_files.content_hash` 字段从未写入,死字段(可用于 M5 的重连方案) |
| L15 | `enrichment/manager.go:500` 等 | User-Agent 版本硬编码 `dev`,未用构建版本号 |

## 正面确认(审计后认为没有问题的方面)

- SQL 注入面干净:全部用户输入走参数绑定;动态 ORDER BY 均为白名单;`IndexCondition` 参数化。
- 模板统一 `html/template`,无 `safeHTML` 绕过;前端 JS 全部用 `textContent` 写入用户数据,无 XSS 通路。
- Token 比较走 SHA-256 + `subtle.ConstantTimeCompare`;CSRF 校验覆盖所有管理端写操作;JSON 请求体有 1MB `MaxBytesReader` 上限且 `DisallowUnknownFields`。
- 图片下载有 10MB 上限 + MIME 嗅探白名单 + 临时文件原子改名。
- 歌手合并/回滚账本设计完整,有循环检测。
- 播放列表替换有 5000 上限、去重、事务;FLAC 流式播放的 picture-type 补丁设计正确。

## 修复优先级建议

> 2026-08-20 更新:第 1-3 批(除 M5)及第 4 批的 M9-M14 已全部完成。剩余:M5(内容哈希重连,已确认暂缓)与全部 L 级项。

1. ~~**今天**:S1(加 0 文件保护)、S2(撤凭据+重启)、H2(去 admin 后门)。~~ ✅ 已完成(S2 的"重启"仍需手动执行)
2. ~~**本周**:H1(后台 recover+优雅退出)、H3、H4、M3、M6。~~ ✅ 已完成
3. ~~**下个迭代**:M1(SSRF 过滤)、M2(MB 限流统一)、M4~~ ✅ 已完成;M5(内容哈希重连)⏭️ 暂缓;~~M7(N+1)、M8~~ ✅ 已完成。
4. **排期改进**:~~M9-M14~~ ✅ 已完成;低级别项(L1-L15、表内 M15)待排期。
