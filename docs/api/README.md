# API 参考

> 各模块的详细接口另见：[作品](works.md)、[系列](series.md)、[歌单操作](playlist-operations.md)、[主页](home.md)。

项目使用自有 `/api/v1` API，**不要假定兼容 Subsonic/OpenSubsonic**。[路由注册](../../internal/http/app.go)和[能力接口实现](../../internal/http/client_features.go)可作为代码参考。

## 认证方式

| 接口 | 认证 |
|---|---|
| `GET /api/v1/health` | 公开。 |
| 曲库/客户端 JSON API | `Authorization: Bearer <API_TOKEN>` 或管理员会话；会话认证的修改需 CSRF。 |
| 音频、转码、封面、歌手图片、原始 `.lrc` 歌词 | `?mediaToken=<MEDIA_TOKEN>`、`Authorization: Bearer <MEDIA_TOKEN>` 或管理员会话；**不接受完整权限 API Token**。 |
| 播放 timeline/scrobble 上报 | API Token、管理员会话，或媒体 Token（Bearer/查询参数）。媒体凭据不能修改曲库或歌单。 |

媒体/上报接口也接受旧 `token` 查询参数别名，新客户端应使用 `mediaToken`。应用请求日志省略查询字符串，反向代理日志需单独配置。

## 主要接口

| 方法 | 路径 | 用途 |
|---|---|---|
| `GET` | `/api/v1/status`、`/api/v1/capabilities` | 状态、统计、功能、媒体规则与可用编码器。 |
| `GET` | `/api/v1/artists`、`/api/v1/albums`、`/api/v1/tracks` | 浏览/搜索，对应 `/{id}` 获取详情。 |
| `GET` | `/api/v1/browse/albums`、`/api/v1/browse/tracks`、`/api/v1/browse/artists` | 带排序方向、种子随机和歌手引用的列表（`capabilities.features.browse`），见下文“浏览接口”。 |
| `GET` | `/api/v1/tracks/{id}/works` | 曲目所属的作品（`capabilities.features.trackWorks`）：`{"items":[{id,title,translatedTitle,type,role,season,seriesId}]}`，是 `/works/{id}/tracks` 的反向：直接关联或专辑关联都算，每部作品一条（`role`/`season` 取优先级最高的关联：手动 > Bangumi > 自动 > 专辑）；`seriesId` 仅在作品属于系列时出现；未知曲目 404。 |
| `PATCH` | `/api/v1/artists/{id}`、`/api/v1/albums/{id}`、`/api/v1/tracks/{id}` | 保存人工元数据覆盖。 |
| `GET` | `/api/v1/sync/albums`、`/api/v1/sync/tracks`、`/api/v1/sync/artists` | 游标曲库同步；专辑与曲目附带 `artists: [{id,name}]` 歌手引用，歌手同步只含演唱者（专辑歌手或曲目主唱）。 |
| `GET` | `/api/v1/artists/{id}/tracks` | 歌手单曲分页列表（演唱或专辑歌手），`sort=album\|plays\|recent\|title\|added\|duration`，`order=asc\|desc` 覆盖默认方向；非法值返回 400。 |
| `GET` | `/api/v1/albums/{id}/works`、`/api/v1/artists/{id}/credits` | 作品关联与艺人 credit。 |
| `GET/POST` | `/api/v1/works` | 列出/新建作品；`/{id}` 支持 GET/PATCH/DELETE。 |
| `GET/POST` | `/api/v1/works/{id}/albums`、`/api/v1/works/{id}/tracks` | 读取/添加关联；DELETE 对应 `/{albumId}` 或 `/{trackId}` 解除。 |
| `PUT/DELETE` | `/api/v1/{artists\|albums\|tracks}/{id}/favorite` | 收藏/取消收藏。 |
| `GET` | `/api/v1/favorites/{artists\|albums\|tracks}` | 收藏列表。 |
| `GET/POST` | `/api/v1/playlists` | 列出/新建歌单；`/{id}` 支持 GET/PATCH/DELETE。 |
| `PUT` | `/api/v1/playlists/{id}/items` | 使用 `{"trackIds":[12,34]}` 替换有序内容。 |
| `POST` | `/api/v1/playback/events` | 播放会话事件上报（计数/跳过/断点均由服务端派生）。 |
| `GET/DELETE` | `/api/v1/playback/history` | 读取/清空历史和断点位置。 |
| `GET` | `/api/v1/tracks/{id}/lyrics` | 结构化歌词（JSON 凭据）`{synced, lines[], source, sourceName, manual, remoteTitle?, remoteArtist?}`。查找顺序：客户端手动选择 > 同目录 `.lrc` > 内嵌歌词 > 在线歌词缓存 > 在线搜索（网易云、LRCLIB，首次请求时抓取并缓存，没找到的 12 小时内不再搜索；纯音乐不搜索）。`source` 为 `file` / `embedded` / `netease` / `lrclib`。能力位 `features.lyricsOnline` 表示开启了在线歌词。 |
| `GET` | `/api/v1/tracks/{id}/lyrics.lrc`、`/api/v1/tracks/{id}/stream` | 原始歌词/音频（媒体凭据）。 |
| `POST` | `/api/v1/tracks/{id}/lyrics/translation` | 歌词逐行翻译（JSON 凭据）。请求 `{targetLanguage}`（如 `zh-Hans`），原文取自上面的歌词接口；返回与其 `lines` 等长的 `lines[]`、`model`、`cached`、`source`（`ai` 或 `netease`）。歌词来自网易云且目标语言为简体中文时直接使用网易云自带翻译；否则需要服务端开启 AI 翻译（能力位 `features.lyricsTranslation`），未开启返回 409 `translation_disabled`，模型出错返回 502 `translation_failed`。 |
| `GET` | `/api/v1/lyrics/search?name=&artist=&durationMs=` | 在线歌词搜索（JSON 凭据），返回 `{results: [{source, id, name, artist, album?, durationMs?, synced, score}]}`，`score` 0 为最佳。 |
| `GET` | `/api/v1/lyrics/remote/{source}/{id}` | 预览一条搜索结果的歌词，格式同歌词接口。 |
| `PUT` / `DELETE` | `/api/v1/tracks/{id}/lyrics/source` | `PUT {source, id}`：手动选择这首歌的在线歌词（优先于本地歌词，对所有客户端生效）；`DELETE`：清除在线歌词并停止自动搜索（本地歌词不受影响），返回 204。 |
| `GET` | `/api/v1/artwork/{id}`、`/api/v1/artists/{id}/image` | 图片，可传 `size` 获取缩略图。 |
| `GET` | `/api/v1/tracks/{id}/transcode.{mp3\|ogg\|flac}` | 转码媒体。 |
| `GET` | `/api/v1/tracks/{id}/similar`、`/api/v1/tracks/path` | 元数据相似度与路径（JSON 凭据）。 |
| `POST` | `/api/v1/enrichment/run` | 启动增强；`/api/v1/enrichment/jobs` 和 `/{id}` 查状态，POST `/{id}/cancel` 取消。 |
| `GET` | `/api/v1/audio-analysis` | 声学分析状态：开关、进度、校准、分析服务信息（`capabilities.features.audioAnalysis` 表示已部署且已开启）。 |
| `GET` | `/api/v1/ai/tagging` | AI 打标状态：进度、用量、复核与抽样对比（`capabilities.features.trackTags` 表示已配置且已开启）。 |
| `GET/PUT` | `/api/v1/tracks/{id}/tags` | 曲目的情绪/风格标签（生效结果、各来源与 AI 元信息）；PUT `{"moods":["cheerful"],"styles":["pop"]}` 设置人工修正，空数组清除该类修正。 |
| `GET` | `/api/v1/daily-mixes` | 今天的每日推荐（`status`：`ready` / `generating`（202，稍后重试）/ `not_ready` / `disabled`；每项含标题、简介、主题标签、曲数、时长、最多 4 张封面、评价）。`capabilities.features.dailyMixes` 表示已开启。 |
| `GET` | `/api/v1/daily-mixes/{id}` | 歌单详情与曲目（`bucket`：familiar / explore；`blocked` 表示已设为不再推荐）。 |
| `PUT` | `/api/v1/daily-mixes/{id}/rating` | `{"value":1}` 赞、`{"value":-1,"reasons":["theme"]}` 踩（原因：`repetitive`、`theme`、`tracks`、`flow`、`mismatch`）、`{"value":0}` 撤销。 |
| `POST` | `/api/v1/daily-mixes/{id}/regenerate` | 换一个：同一位置换成新主题；每天次数用完返回 429。 |
| `POST` | `/api/v1/daily-mixes/{id}/save` | 另存为普通歌单（可选 `{"name":"…"}`）。 |
| `PUT/DELETE` | `/api/v1/tracks/{id}/recommendation-block` | 不再推荐 / 恢复推荐。 |
| `GET/PATCH` | `/api/v1/daily-mixes/settings` | 每日推荐设置（`enabled`、`mixCount` 1–8、`trackCount` 10–60、`explorePercent` 20–60、`pregenerateHour`、`regenerateLimit`、`allowSad`、`llmTitles`）。 |

`{artists|albums|tracks}` 及格式枚举仅为表格简写，不是实际 URL。候选审核接口也在 `internal/http/app.go` 注册。

普通列表返回 `{"items":[],"total":0,"limit":100,"offset":0}`，单页最多 500 条；同步接口使用独立游标响应。浏览参数包括 `q`、`artist`、`album`、`year`、`genre`、`sort`、`limit`、`offset`，适用性取决于资源。歌手列表支持 `role=album|track|all`、`favorite=true`；歌曲支持 `hideInstrumental=true`，`sort` 支持 `title`、`year`、`date`、`plays`（播放次数）、`recentlyPlayed`（最近播放）。支持假名变体匹配，罗马音搜索需标签提供 reading/sort 字段。相似推荐基于元数据，**不是声学分析**。

### 浏览接口

`/api/v1/browse/{albums|tracks|artists}` 接受对应旧列表的全部筛选参数（歌曲含 `hideInstrumental`、`yearFrom`/`yearTo` 等焦点筛选），返回同样的 `{items,total,limit,offset}`，另有：

- `sort`：专辑 `title`、`year`、`date`、`added`、`recentlyPlayed`、`random`；歌曲 `album`、`title`、`year`、`date`、`recentlyPlayed`、`plays`、`added`、`duration`、`random`；歌手 `name`、`albums`、`tracks`、`random`。非法值返回 400 `invalid_filter`。
- `order=asc|desc`：覆盖主排序键的默认方向（年份、日期、加入时间、最近播放、播放次数、专辑数、曲目数默认降序，其余升序）；同值时的次级顺序不变。无年份、无日期、未播放的条目在两个方向都排在最后。
- `sort=random&seed=<整数>`：同一个 seed 的各页结果稳定、不重不漏；不带 seed 时每次请求重新洗牌。random 忽略 `order`。
- 专辑 `yearFrom`/`yearTo`：发行年份区间（含边界，无年份的专辑不匹配任何边界）；反向区间返回 400。
- 歌手引用：专辑带 `artists: [{id,name}]`，歌曲另带专辑歌手 `albumArtists: [{id,name}]`（专辑没有署名歌手时省略）。

旧的 `/albums`、`/tracks`、`/artists` 行为不变：不认识的 `sort`（如 `random`、`duration`）仍按默认顺序，`order`/`seed`/专辑 `yearFrom` 被忽略。

歌手详情 `/api/v1/artists/{id}` 不再内嵌全部单曲：返回 `topTracks`、`topTracksArePopular` 与 `tracksTotal`：有播放记录时 `topTracks` 为播放次数最多的至多 10 首（未播放过的歌不计入，按播放次数、最近播放时间排序），`topTracksArePopular=true`；歌手的歌都没播放过时兜底为专辑顺序的前 10 首，`topTracksArePopular=false`（web 歌手页同理），完整列表通过 `/api/v1/artists/{id}/tracks` 分页获取。单曲排序默认方向：`album`（年份→专辑→碟号→曲号）、`title`、`duration` 升序；`plays`、`recent`、`added` 降序；`recent` 中未播放过的歌始终排在最后；同值时按专辑顺序。

创建歌单可传 `{"name":"晚间播放","description":"客厅","trackIds":[12,34]}`，最多 5000 首去重歌曲，保留第一次出现的顺序。

## 播放上报（会话协议，apiRevision 3）

每次播放是一个持久化会话：客户端为每次播放生成新的 `sessionId`（建议 UUID），`seq` 从 1 起单调递增。向 `/api/v1/playback/events` 发送：

```json
{"clientId":"device-abc","clientKind":"android","sessionId":"7c9e…","seq":1,"type":"start","trackId":12,"state":"playing","positionMillis":0,"durationMillis":240000}
```

- `clientKind`：`web`、`android`、`desktop`（桌面客户端，如 032musicplayer），见 `capabilities.playback.clientKinds`；同一会话内不可变。
- `type`：`start`（必须携带真实初始 `state`：`playing`/`buffering`/`paused`）、`heartbeat`（同样携带真实 state）、`pause`/`buffering`/`resume`（state 由类型推导，不要发送）、`seek`（保留当前 state，仅更新位置）、`end`（必须携带 `endReason`）。
- `endReason`：`completed`（断点归零）、`skipped`、`stopped`、`replaced`、`error`、`client_closed`。
- 心跳节奏：playing/buffering 每 15 秒（租约 90 秒），paused 每 60 秒（租约 10 分钟）。超过租约会话失效，历史显示“已中断”，不算跳过/完成。
- 续播定义：refresh、restoreState、BFCache 恢复、进程重启后恢复同一首歌都属于续播——客户端持久化最近一个 sessionId+trackId，start 必须携带 `resumedFromSessionId`。只有明确切歌或循环重播才是不带 resume 的新播放（新会话）。start 网络超时后必须用同一个 sessionId 重发（服务端视为幂等重放）。
- 播放上下文（`features.playbackContext`，`playback.contextTypes: ["dailyMix"]`）：从每日推荐歌单的播放队列开始的播放，start 可带 `context: {"type":"dailyMix","id":<歌单 ID>,"position":<0 起的序号>}`；只允许出现在 start 上，续播会话不必重复携带，服务端沿续播链继承。服务器未声明该能力时不要发送（未知字段返回 400）。带上下文的会话结束后每小时汇总一次，用于调整之后的每日推荐。
- 响应 200 `{"applied":bool,"state":"…","positionMillis":…,"counted":bool}`；`seq` 过旧或终态后的迟到事件返回 `applied:false`（不续租、不改断点），终态不可复活。
- 错误：`404 session_not_found`（未知/已清空会话：若歌曲确实播放过且已过计数门槛，以 state playing、最终位置补 start 并以原 endReason end，恢复这次播放；没播放过绝不能伪装 playing；否则丢弃，下次播放重新开始）；`409 session_expired`（已过期并被固化：串行处理，仅开一个 resume 会话——以当前位置 start 并携带 `resumedFromSessionId`；迟到的 end 先以最终位置 start resume，再以原 endReason end。补发的恢复 start 用 state playing 是因为恢复对象确实播放过）；`409 session_owner_mismatch` / `session_conflict` / `resume_invalid`（前序不存在/异 client/异 track，或以 completed/skipped/replaced 结束；同 client 同 track 的活跃前序不算错误：服务端在同事务内将其以 replaced 取代并接受续播。收到 resume_invalid 禁止静默退化为不带 resume 的高位置新 start——那是新播放，会重新计数）。
- `end(completed)` 必须携带真实最终位置（≈时长），绝不能发 0；断点归 0 由服务端处理。
- 计数：位置过 50%（1 秒 slack）且有真实播放证据（事件前或事件后的状态为 playing）时服务端计一次；从头到尾只有 paused/buffering 的会话无论以何种 endReason 结束都不计数（暂停中高位置 seek 后 end 同样不计）；播放越过门槛后在 pause/buffering/end 时按当时位置计数；每条续播链（chain_id）最多计一次，由事件事务内实时查询加数据库唯一部分索引双重保证——未计数前序的顺序/并发分叉也只计一次；只有明确的新播放（切歌/循环重播）重新计数；多端独立会话分别计数。
- 跳过：仅 `endReason=skipped` 且结束位置低于 MIN(30s, 时长/2) 时计一次跳过。
- 断点：最后被接受的事件生效；`completed` 归零；过期保留最后位置。
- 旧的 `/api/v1/playback/timeline` 与 `/api/v1/playback/scrobble` 已移除，返回 410；无兼容路径。清空历史同时清空会话。
- 鉴权沿用 apiToken / 管理会话 / mediaToken；单用户服务端，`clientId` 仅绑定会话归属，不是强安全身份。

## 媒体行为

- 原音频支持字节 Range。原始歌词优先使用外部 `.lrc`，超过 1 MiB 返回 413。
- 缩略图将 `size` 向上取整到 256/512/768/1024/1536；省略时返回原图。不支持/不安全/过小的图片可能原样返回。缓存位于 `<data>/thumbs`。
- 实时 MP3：`bitrate=128|192|256|320`（默认 320）；Opus/Ogg：`64|96|128|160|192|256`（默认 128）。定位使用 `offsetMs`，非零字节 Range 返回 416。
- 缓存 FLAC 首次转码完成后提供 Content-Length 和字节 Range。`maxSampleRate=48000` 可将高解析源降至 44.1/48 kHz 族。缓存位于 `<data>/transcode-cache`。
- HEAD 不启动转码。缺少 ffmpeg/编码器时相应格式返回 503，应查询 capabilities 判断实际能力。

