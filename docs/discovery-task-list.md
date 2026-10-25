# 曲库发现与获取：分阶段任务清单

> 用途：每个对话完成一个阶段。新对话先读本文件和 `PRODUCT.md`，确认当前勾选状态后继续，不要重复已完成阶段。
>
> 当前进度：**S0、S1、S2、S3 已完成；本清单的开发部分已结束。S4 由用户亲自进行实机验证，不再由代理执行。**

## 已确认的产品边界

- 单用户、ACG 优先；推荐 Album、EP、Single、OST。
- 首版只做已知艺人，相似艺人后置；Bangumi 作品来源在 S3 单独实施。
- 可解释规则排序，利用/探索约 80/20，不使用 ML。
- MusicBrainz Release Group（RG）是“内容拥有”单位；Release ID 留给未来版本管理。
- RG 不参与 scanner 的专辑分组；同一 RG 可对应多个 Release、本地专辑和版本。
- MusicAutoTagger 是标准刮削流程，但首版不接 MAT API、不展示刮削状态。
- 首期只接 JPopsuki，经 Jackett 按需搜索；用户确认种子，不自动选种或自动下载。
- qBittorrent 只追踪到下载完成；qB→TR、做种和刮削均不进入状态机。
- 最终只显示“专辑已入库 · 检测到 N 首”，不宣称整专文件完整。
- qB 沿用现有 category 和默认目录，只添加 acquisition tag。
- Compilation 一律排除，包括 Compilation+Soundtrack。
- “我已有”必须关联本地专辑；“不感兴趣”是 RG 级永久决定，可恢复。
- 先交付发现功能，再交付获取功能。

## S0 · 数据正确性与 RG 身份基础 ✅

- [x] 修复重扫时 `audio_file_tags` 写错文件的问题。
- [x] 新增 RG 实体、文件证据、专辑身份、冲突和用户决定模型（迁移 057）。
- [x] 不从可能受污染的存量 EAV 自动回填。
- [x] 管理员手动启动身份重建；未 ready 时读取返回 `ErrIdentityNotReady`。
- [x] 处理人工优先、跨专辑移动、合并、missing、purge 和删除规则诊断。
- [x] RG 读取接口规范化 UUID。
- [x] 完整普通测试、完整 race 和独立 reviewer 门禁通过。

**完成提交：** `d6d2112 feat(discovery): 添加功能原型与发行组身份基础`

**S0 非阻断遗留（不得在未确认时顺手扩大当前阶段）：**

- [ ] 减少扫描后全库 RG 重复重算。
- [ ] 修复重建启动失败可能留下 queued 扫描任务。
- [x] 改善重建错误提示和卡住时的恢复出口（实机发现：5/17724 文件读取失败导致整库无法就绪）。现改为：失败文件隔离（丢弃其旧 RG 证据，不参与身份判定），其余正常就绪；控制台列出失败文件；旧版本留下的“读取失败”错误在启动时自动就地完成，无需重扫。
- [ ] 补充 created_at 重导入不变、跨专辑多文件、056 污染 EAV 不回填等测试。
- [ ] 在重建按钮说明中提示全量扫描会遵循现有缺失文件清理策略。
- [ ] 在真实数据库统计历史标签污染规模；先备份，再执行手动身份重建。

## S1 · 已知艺人目录与发现功能 ✅

**目标：** 用户可以通过“发现新音乐”看到已知艺人的缺失 RG，并完成已有、忽略、恢复和可能已有核对；本阶段不接 PT/qB。

### 外部来源基础

- [x] 新建进程级 `sourcegate`，统一来源最小间隔和 429/503 退避；由 main 注入 enrichment 与 discovery，禁止两套并行限流。
- [x] 抽取可复用的公网图片下载器；保留跳转、大小、MIME 和 SSRF 防护，封面必须同源提供。
- [x] 新建独立 MusicBrainz 客户端：艺人 release-group 浏览、RG 详情、100 条分页和未来发行过滤。
- [x] 过滤 Album/EP/Single；OST 使用 Album+Soundtrack；排除 Live、Compilation 及 Compilation+Soundtrack。
- [x] 不为 discovery 复制 enrichment 的 durable run 或缓存框架。

### 推荐与数据模型

- [x] 迁移 058：外部 RG 目录、候选、候选 seed、推荐理由和刷新检查点。
- [x] 已知且 MBID 已确认的艺人生成候选；按艺人设置候选上限，避免高产艺人占满列表。
- [x] 使用现有艺人/作品播放与收藏信号，建立 discovery 自己的可解释评分和 80/20 配额；不改 daily-mix 表。
- [x] 单 worker + seed 检查点 + 代际号，防止取消后的旧 worker 晚到写入。
- [x] 已拥有强匹配、获取中、已忽略不得重复推荐；弱匹配进入“可能已有”。
- [x] 所有 RG 读取调用显式处理 `ErrIdentityNotReady`，不能当作空结果。

### Web 与反馈

- [x] 按 `prototypes/discovery.html` 实现正式模板、CSS、外部 JS；遵守现有 CSP，不直接嵌入原型。
- [x] 实现推荐、可能已有、获取中占位、已忽略和动态计数。
- [x] “我已有”选择并关联本地专辑；忽略/恢复使用 RG 级决定；可能已有先对比再搜索。
- [x] 管理写路由执行 requireAdmin + 显式 CSRF；封面本地缓存后同源展示。
- [x] 桌面、移动和 820px 边界可访问，支持键盘与 reduced motion。

### S1 完成门禁

- [x] `go test ./...` 通过。
- [x] `go test -race -count=1 -timeout 60m ./internal/storage/... ./internal/http/...` 通过。
- [x] MB 使用 httptest fake，覆盖分页、类型过滤、429/503、取消和迟到写入。
- [x] Playwright 三视口覆盖推荐、已有、忽略、恢复和可能已有（`tests/e2e/discovery.spec.js`，隔离实例 :45445 + fake MB）。
- [x] 独立 reviewer 给出 APPROVE；本地提交并更新本文件。

**完成提交：** `a91c47f feat(discovery): 已知艺人 MusicBrainz 目录与发现功能`

**S1 已拍板决策：**

- D-1 类型白名单：仅无 secondary 的 Album/EP/Single，及 Album+Soundtrack=OST；Remix、Audio drama、EP/Single+Soundtrack 等均排除。
- D-2 卡片不显示曲目数（不额外查询 release）。
- D-3 目录仅手动刷新；启动时只续跑中断（running）的那一代；限流时整轮 failed，需手动重试。
- D-4 空发行日期保留，评分 ×0.7。
- D-5 Playwright 用隔离实例 + DB 种子 + Node fake MB；新增测试用环境变量 `MUSIC_SERVER_MUSICBRAINZ_BASE_URL`。
- D-6 “我已有”替换专辑既有 RG 时返回 409 `replaces_rg`，需二次确认。
- D-7 Bangumi 限流未迁入 sourcegate，留到 S3。
- 图片下载保留 `HTTP(S)_PROXY`：直连时 dial 阶段校验实际 IP；走代理时依赖请求前与每跳 URL/DNS 校验。

**S1 实现落点：** `internal/sourcegate`、`internal/netfetch`、`internal/discovery`（含 `mb/`）、`internal/storage/discovery*.go`、迁移 058、`internal/http/discovery.go`、`templates/discover.html`、`assets/discovery.{js,css}`、`scripts/{seed,reset}_discovery_e2e.go`、`scripts/discovery-fake-mb.mjs`。

**S1 门禁记录：** `go test ./...`、S1 相关包完整 race、全量 Playwright（456 passed / 102 skipped / 6 failed，失败均为既有问题，见下）、后端与 Web 两轮独立 reviewer APPROVE。

**S1 非阻断遗留（不得在未确认时顺手扩大当前阶段）：**

- [ ] 既有 E2E 失败：`local-time.spec.js` mobile/boundary 6 例（在基线 `4c0c228` 同样失败）；共享实例 `home.html:368` 的 `index .Albums 0` 空切片报错，移动端导航选择器 `.sidebar a[data-nav="playback"]:visible` 不可见。
- [ ] 封面下载无 E2E 覆盖（下载器拒绝 loopback，fake 封面无法使用）；目前仅 Go 测试。
- [ ] RG browse 不过滤 bootleg/pseudo-release，S4 用真实数据评估。
- [ ] MB special-purpose 艺人黑名单目前仅 3 个 MBID，S4 对照 MB 文档补全。
- [ ] 验证环境缺 ffmpeg 与 Chromium 动态库，本次使用 `/tmp/ffmpeg-7.0.2-arm64-static`、`/tmp/pwdeps` 临时依赖运行 Playwright（`LD_LIBRARY_PATH=/tmp/pwdeps/usr/lib/aarch64-linux-gnu FFMPEG=... npx playwright test`）。

## S2 · JPopsuki、qB 与最终入库 ✅

**目标：** 从推荐卡片按需搜种，用户确认后交给 qB，qB 完成后由 server 根据新文件 RG 证据确认入库。

**S2 已拍板决策（2026-10-24，全部按 oracle 推荐）：**

- E1 AWAITING_LIBRARY 72 小时无证据转 ATTENTION（常量）。
- E2 Jackett indexer 可配置，默认 `jpopsuki`。
- E3 关键词默认“艺人 标题”，用户可改；不做自动降级；15 分钟缓存按规范化关键词作键。
- E4 新建 `/admin/settings/acquisition`，发现页放“获取设置”入口。
- E5 结果显示标题、分类、体积、做种/下载/完成数、日期、freeleech、正则格式徽标；按做种数倒序；不自动选。
- E6 取消只改本地状态，不碰 qB。
- E7 magnet-only 结果显示但不可选，标注“不支持磁力”。
- E8 确认前探测 qB 登录；建任务后失败按退避重试，30 分钟转 ATTENTION(qb_unreachable)，可手动重试。
- E9 单个 Node fake（Jackett+qB，`:45447`，`/control` 切场景），seed 写设置。
- E10 删除规则抑制仅启发式诊断，不改 scanner。
- E11 下载完成不自动触发扫描；目录监控关闭时在卡片提示。
- E12 qB 登录判定见下方修订条目。
- E13 有活跃任务时“我已有”→任务 IN_LIBRARY(owned)；“忽略”返回 409 `acquisition_active`。
- E14 hash 被其他 RG 活跃任务绑定时返回 409 `hash_bound` 并链接该任务。
- E15 IN_LIBRARY 卡片在“获取中”保留 7 天。
- 另：qB category 无“默认”语义，设置中提供 category 原样传入；tag 用 `032acq-<public_id UUID>`；恢复备份时整组保留 Jackett/qB 地址+凭据+indexer+category。
- 实施分三批：A 后端基础 → B 状态机与接线 → C Web 与 E2E；详细方案见 oracle 审计输出。

### 设置、Jackett 与搜索

- [x] 迁移 059：acquisition 设置、搜索结果快照、获取任务和入库证据。
- [x] Jackett/qB 密钥只写不读，并加入备份恢复 `preserveCredentials`；HTML/JSON/日志不得泄密。
- [x] Jackett 按需查询 JPopsuki，缓存 15 分钟；真实空结果与错误不得混淆。
- [x] 检查 HTTP 200 中的 Torznab `<error>`；429 遵守 Retry-After；错误描述脱敏。
- [x] 下载 URL 只在服务端结果快照中使用，不进通用 HTTP 缓存或页面；种子字节用完即丢、永不落库。

### qB 幂等与下载状态

- [x] qB 登录以 `Ok.` + `GET /api/v2/app/version` 200 为准（cookie 名不写死），请求带正确 Referer、不带 Origin；HTTP 200 不等于登录或添加成功。
- [x] 服务端下载 torrent bytes，计算 info hash 后以 multipart 上传 qB，不把敏感 URL 交给 qB。
- [x] acquisition ID 仅放 tag；沿用用户现有 category 和默认目录。
- [x] 用户确认先持久化幂等键；add 超时进入结果未知，先 hash+tag 对账，禁止盲目重加。
- [x] 同一 hash 已被其他任务绑定时合并/提示；同一 RG 同时最多一个活跃获取任务。
- [x] 状态只保留 QB_SUBMITTING、DOWNLOADING、AWAITING_LIBRARY、IN_LIBRARY、ATTENTION、CANCELLED。
- [x] qB 任务消失不等于失败，允许 DOWNLOADING→IN_LIBRARY；完成后不再追踪做种/TR。

### 最终入库

- [x] 证据同时满足：audio_files.created_at 晚于任务创建、available、精确 RG、文件 RG 不冲突、所属专辑 resolved。
- [x] 任务和文件时间使用 SQLite 时钟；保存 album_id、evidence_audio_file_id、verified_at 和检测数量。
- [x] 扫描完成组合回调中追加 acquisition.Wake，不覆盖其他回调；对账幂等且关机可等待。
- [x] 超时转 ATTENTION，提供手动关联；区分 RG 不匹配、MAT 未识别、删除规则抑制等原因。
- [x] 不引入 MAT API、做种状态、刮削状态或完整曲数承诺。

### S2 完成门禁

- [x] Jackett/qB 全部 httptest fake；覆盖 200 error XML、敏感 URL、add 超时、重复 hash、重启对账。
- [x] 覆盖备份恢复后任务重新对账和密钥保留。
- [x] 覆盖任务前已有 RG、同路径覆盖、扫描部分失败、手动关联和直接 DOWNLOADING→IN_LIBRARY。
- [x] 普通测试、完整 race、Playwright 与独立 reviewer 全部通过。
- [x] 本地提交并更新本文件。

**完成提交：** `264cce3 feat(discovery): JPopsuki 搜种、qB 获取与入库确认（S2）`

**S2 实施补充决策（实施中拍板）：**

- 059 另含 `add_unknown_since`、`qb_unreachable_since`，与 `qb_missing_since` 分开计时（消失 72h / add 未知 2m / 不可达 30m），均用 SQLite 时钟。
- 结果未知最多重下重加一次（共两次 add），第二次同样有约 2 分钟对账窗口；只有明确 `AddRejected` 立即转 `qb_rejected`。
- 重下载错误白名单：仅冷却、HTTP 429/5xx、Torznab 500/501、网络/超时延后重试，其余转 `torrent_unavailable`。
- 无 download_ref 的 Retry（如 `qb_missing`）回到 DOWNLOADING 并重新计时，复用证据检查与 72h 超时。
- 确认前每次实际请求 qB `app/version`（Probe）；建任务时在写锁内复查已忽略/已拥有；“我已有”事务内发现活跃任务转 `MarkOwned`。
- `hash_bound` 返回绑定任务 publicId/title/artist（`Store.ActiveAcquisitionByHash`）。
- 手动关联复用抽出的 `associateAlbumReleaseGroupTx`，单事务完成，保留 `replaces_rg` 二次确认。
- 种子 bencode 容忍未排序字典键、拒绝重复键，info hash 始终取原始切片；纯 v2 种子拒绝。

**S2 实现落点：** 迁移 059、`internal/acquisition`（`service.go`、`catalog/`、`jackett/`、`qbit/`、`torrent/`、`lanhttp/`、`sanitize/`）、`internal/storage/acquisition_*.go`、`internal/http/acquisition.go`、`templates/acquisition-settings.html`、`assets/acquisition-settings.js`、`assets/discovery.{js,css}`、`scripts/acquisition-fake.mjs`（:45447）、`tests/e2e/acquisition.spec.js`；`cmd/server/main.go` 注册 sourcegate `jackett`、扫描完成回调追加 `Wake()`、关机 `Wait()`。

**S2 门禁记录：** gofmt、`go vet ./...`、`go test ./...` 通过；完整 race（storage 1351s、http 927s、acquisition、discovery、cmd）全部通过；专项 Playwright（acquisition + discovery 三视口）24 passed / 0 failed；全量 Playwright 471 passed / 102 skipped / 6 failed（均为既有 `local-time.spec.js`）。批次 A、B1、B2、C 均经独立 reviewer 审查 APPROVE，修复均 resume 原 worker；拦截 1 Blocker（Gazelle `torrent_pass`/`authkey` 与 magnet 未脱敏）、3 High（证据时间信任调用方、CAS 可无证据写 IN_LIBRARY、确认前 qB 探测被缓存登录绕过）及若干 Medium。

**S2 非阻断遗留（不得在未确认时顺手扩大当前阶段）：**

- [ ] 未接真实 JPopsuki/Jackett/qB 验证；S4 核实 qB 5.x add 响应、cookie 名、category 不存在时行为、JPopsuki 分类映射与标题格式徽标。
- [ ] ATTENTION 诊断（`no_rg_files`、`rule_suppressed`、`preexisting_only`）为启发式，仅作提示；精确的删除规则抑制需 scanner 记录被抑制文件 RG（E10 方案 B）。
- [ ] 恢复旧备份后 qB 中可能遗留无主 `032acq-*` tag，不自动收养，需在 S4 文档说明。
- [ ] 一个种子含多张发行时会被 `hash_bound` 拦下，只能提示用户（未做多 RG 合并任务）。
- [ ] bencode 容忍非规范键序，S4 用真实种子确认无误判。
- [ ] 种子 v2/hybrid 仅按 v1 hash 处理；纯 v2 种子直接拒绝。
- [ ] E2E 未等待真实 60 秒下载轮询，add 超时/未知与完成等后台边界由 Go fake 测试覆盖。
- [ ] storage/http 完整 race 单包超过 10 分钟默认时限，需 `-timeout 60m` 以上；并发跑多套重型测试时 `TestLyricsFallback/timeout` 偶发超时（单跑通过，既有问题）。

## S3 · Bangumi 作品来源 ✅

- [x] 复用已知作品和系列偏好生成作品 seed。
- [x] 建立 Bangumi 音乐条目到 MB RG 的候选映射，不把 Bangumi subject 当 RG。
- [x] `A / B` 多曲名条目、同名条目和歧义关系不得自动确认。
- [x] 映射作为需确认建议；用户决定永久保存并防止自动复活。
- [x] 按 seed 艺人/作品封顶，保持 80/20 配额和推荐理由。
- [x] 完成普通测试、完整 race、Playwright、独立 reviewer、本地提交。

**完成提交：** `f2ef76f feat(discovery): Bangumi 作品来源与 MusicBrainz 发行组映射（S3）`

**S3 已拍板决策（用户授权主代理按 oracle 推荐自主拍板）：**

- Q1：待确认映射（suggested）不占“为你发现”的 60 个名额，单独放“待确认对应”视图；确认前服务端拒绝搜种和获取（409 `mapping_unconfirmed`）。
- Q2：允许自动对应（auto），但条件极严：标题严格相等（NFKC + casefold，只去空白不去标点），Bangumi 艺人与 MB credit 有交集，完整日期同日，Eligible 结果中规范化同名的唯一，非多曲名，同一作品下无同名兄弟条目，跨条目不冲突。auto 卡片提供“不是这张”。
- Q3：每轮最多 24 个作品 seed，每个作品最多 6 个音乐条目，每轮最多 120 次 MB 新搜索（按 `searched_at` 从旧到新），映射 30 天 TTL；已 confirmed/rejected 的条目不再搜索。
- Q4：MB 查询 `releasegroup:"<转义 name>"`，limit=10；`name` 无结果时才用 `name_cn` 再搜一次；艺人只参与打分，不进查询；结果先过 Eligible（D-1 不变）。
- Q5：被拒绝的映射可在“已忽略 → 已拒绝的对应”中恢复，恢复为 suggested。
- Q6：Bangumi 未启用时跳过作品阶段并在状态里提示，不算失败；UA 使用 Bangumi 设置行。
- Q7：封顶键为艺人 `a:<id>`；作品在系列中用 `s:<seriesID>`，否则用 `w:<id>`；每键最多 3 张。WorkAffinity = 自身 Affinity + 0.5 × 同系列其他作品 Affinity 的最大值。
- Q8：Bangumi 限流完整迁入共享 sourcegate（D-7 完成）；discovery 不自建 Bangumi 客户端，而是通过 enrichment 导出的 shared-fetch 方法访问 Bangumi（复用 `http_response_cache`，隔离 runMemo）。
- relation 白名单与权重：原声集 1.0；片头曲、片尾曲、主题歌 0.9；插入歌 0.8；角色歌、印象曲、意象歌 0.6；“其他”“广播剧”等排除。本地已确认的 Bangumi 音乐条目跳过。
- M-3 实施决定：只有出现在映射中的 MB 命中才写入 `discovery_release_groups`。TTL 重搜后清理孤儿 RG，但只要存在任何映射、艺人来源、获取任务或快照、RG 决定或“不是同一张”记录就保留，用户决定永不级联删除。
- 获取层 `AcquisitionReleaseGroup` 也要求身份 ready：重建期间 Search/Confirm 返回 409 `identity_not_ready`。无映射的 S2 RG 和纯艺人来源保持原行为。

**S3 实现落点：** 迁移 060（`060_discovery_works.sql`）、`internal/bangumi`、`internal/enrichment/bangumi_shared.go`（含限流迁移）、`internal/sourcegate`（`SetInterval`）、`internal/discovery/{works.go,mapping.go,rank.go,service.go}`、`internal/discovery/mb`（`SearchReleaseGroups`）、`internal/storage/discovery_works.go`、`internal/storage/discovery{,_queries}.go`、`internal/storage/acquisition_tasks.go`（H2）、`internal/http/discovery.go`（`/admin/discover/mapping/{subjectId}/{rgid}/confirm|reject`）、`templates/discover.html`、`assets/discovery.{js,css}`；`scripts/discovery-fake-mb.mjs` 按路径分流 MB/Bangumi；E2E 环境变量 `MUSIC_SERVER_BANGUMI_BASE_URL`。

**S3 门禁记录：** gofmt、`go vet ./...` 通过；`go test ./...` 通过（仅既有 `TestLyricsFallback/timeout` 在高负载下偶发失败，http 包单独重跑通过）；完整 race 全部通过（storage 1456s、http 942s、discovery 466s、enrichment 453s、acquisition、sourcegate、bangumi、cmd）；全量 Playwright 477 passed / 102 skipped / 6 failed（均为既有 `local-time.spec.js`）。流程：oracle 架构审计 → 批次 A（限流迁移）、B（迁移、存储、算法）、C（HTTP、Web、E2E），各批独立 reviewer 审查 → 阶段终审 → 终审后增量复核，全部 APPROVE。共拦截：
- Blocker 4 个：作品候选被 JOIN 静默过滤、按列位置插入失效；共享 gate 未注册 bangumi 即不限流；runMemo 并发写崩溃；MB 搜索 score 用 `,string` 解码真实响应失败。
- High 5 个：标点归一化可骗过自动确认；待确认 RG 可直接获取；用户决定可被级联删除或覆盖；作品 seed 缺少身份守卫；Bangumi 限流错误类型不统一。
- 若干 Medium：未映射命中入库可绕过 H2；拒绝后 Rerank 跳过时卡片仍在；retire 级联删掉“不是同一张”；分组分页错位；计数随 chip 漂移；决定失败不回滚；E2E 断言恒为真；待确认封面只下组首等。

批次 A 的首个 worker 以同步方式运行，事后无法 resume，其小修复改由同角色 fallback worker 完成；B、C 的修复均 resume 原 worker。

**S3 非阻断遗留（不得在未确认时顺手扩大当前阶段）：**

- [ ] D-1 白名单使 ACG OP/ED 召回率偏低（MB 上常标 Single+Soundtrack）；`discovery_bangumi_music.search_status='filtered'` 已留统计口径，S4 用真实数据评估。
- [ ] `SplitMultiTitle` 在任意 `/` 处切分（如 `Fate/stay night`），这类条目永远只能待确认，属于有意的保守。
- [ ] 预算参数（24/6/120/30 天）未经真实数据校准；首次刷新预计 5–6 分钟。
- [ ] 作品掉出前 24 个 seed 后，其已确认映射的推荐会随之消失（B 轮 L-8）。
- [ ] `album_subject_candidates` 缺少针对确认状态的索引（L-11）。
- [ ] 封面下载阶段确认映射时 Rerank 被跳过，前端会提示“排名将在下次刷新时更新”（L-16）；Rerank 本身不下载封面。
- [ ] retire RG 后磁盘上会遗留孤立封面文件（L-20）。
- [ ] 理由合并上限 3 条，作品自身理由较多时，跨来源的首条理由可能被截掉（L-15）。
- [ ] `items.json` 每次请求都全量读取映射显示数据（L-D），按当前规模可接受。
- [ ] HTTP 层缺 `Options.Score` 读取 `MappingScore` 的断言，以及推荐路径“作品候选缺 metadata 不计数”的回归测试（终审后复核 Low-1/Low-2）。
- [ ] Bangumi 匿名请求遇到 NSFW 条目返回 404，按 gone 处理（与现有 enrichment 一致）。

## S4 · 真实环境与交付收尾 ⬜（由用户亲自实机验证）

> 用户决定 S4 由本人进行实机验证，代理不执行本节。验证时可参考 S1–S3 各节的“非阻断遗留”。

- [ ] 用受控账号验证 JPopsuki 登录、搜索、分类、下载和当前站点规则。
- [ ] 核实 qB 版本、add 响应、默认 category/目录与 qB→TR 脚本兼容。
- [ ] 核实 MAT 输出根目录与 server 曲库挂载一致。
- [ ] 真实数据库先备份，再统计污染规模并执行身份重建；记录耗时和覆盖率。
- [ ] 验证恢复旧备份后获取任务和凭据行为。
- [ ] 更新 `README.md`、`docs/features.md`、`docs/configuration.md`、`docs/deployment.md`。
- [ ] 完成最终独立审查、完整测试和 Playwright；确认交付方式后再 commit/push/PR，不默认推送 main。

## 每个对话的固定工作方式

1. 读取本文件，确认只做当前阶段。
2. 新阶段先复核现状和关键外部行为；需要策略变化时先停下来让用户拍板。
3. 主代理调度 worker 实施，同一 cwd 只有一个写入者。
4. 独立 reviewer 审查；修复必须 resume 原 worker。
5. 主代理亲自运行完整门禁，不只引用 worker 自述。
6. 用户要求提交时才 commit；提交后更新本文件的勾选和 commit hash。
7. 未经用户明确批准，不 push、不建 PR、不部署。

## 下一次对话入口

> 本清单的开发阶段（S0–S3）已全部完成，S4 由用户本人实机验证。若实机验证发现问题，新对话先读本文件、`PRODUCT.md` 和 S1–S3 的遗留项，再按“固定工作方式”针对具体问题修复；迁移编号从 061 开始。
