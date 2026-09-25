# 032 Music Server

一个本地标签优先、专辑体验优先的自托管音乐库与串流后端。项目使用 Go 构建，并以 Docker 作为首要部署方式。

## 已完成功能

- Go HTTP 服务与优雅退出
- 环境变量配置和启动校验
- SQLite、WAL、外键和嵌入式迁移
- 启动时自动增量扫描、管理端手动增量扫描与完整重扫
- FLAC、MP3、M4A/MP4、AAC、Ogg/Opus 标签读取
- 按目录边界与专辑标签归档歌手、专辑、歌曲和多碟专辑
- 嵌入封面优先、目录封面回退，内容哈希去重缓存
- 新增、修改、丢失文件识别以及扫描进度、失败记录
- 歌手、专辑、歌曲浏览，以及关键词、歌手、专辑、年份、流派筛选和排序
- 管理端编辑显示名称、标题、年份、碟号、曲号、作曲与流派
- 编辑值作为用户覆盖保存，重新扫描不会改写原文件或覆盖人工修正
- 支持 HTTP Range 的原文件音频串流和封面接口
- 公共健康检查接口
- Bearer Token 保护的音乐库 JSON API
- 面向 Sonos 等无 Header 播放器的独立只读媒体 Token
- 专辑与歌曲收藏、歌单管理、播放进度、断点续播和播放历史 API
- FLAC、MP3、M4A/MP4、AAC、Ogg/Opus 时长采集
- 带登录、会话和 CSRF 防护的管理面板
- Docker 与 Compose 配置

## 快速开始

### GoLand 本地开发

本项目可以直接在 Windows 上运行，Docker 不是开发前置条件。

使用 GoLand 打开项目目录。如果 IDE 没有自动识别 SDK，在：

```text
Settings → Go → GOROOT → Add SDK → Local
```

开发脚本默认使用 `./.local/data` 保存本地数据，并从 `./.local/music` 读取测试音乐。这些目录、IDE 配置和运行产物均被 Git 忽略。

本地开发登录信息：

```text
管理面板：http://localhost:4533/admin
用户名：admin
密码：admin
API Token：由开发脚本为当前进程随机生成，或从 `MUSIC_SERVER_API_TOKEN` 读取
```

默认管理员凭据只用于本机开发，Docker 和正式部署不会读取开发脚本配置。

也可以在新打开的 PowerShell 中运行：

```powershell
.\scripts\dev.ps1
```

如需使用其他音乐目录或固定的本地 Token，请只在当前终端设置环境变量，不要写入仓库：

```powershell
$env:MUSIC_SERVER_MUSIC_DIR = 'D:\path\to\music'
$env:MUSIC_SERVER_API_TOKEN = '<generate-a-random-token>'
.\scripts\dev.ps1
```

如果刚安装完 Go 后当前终端仍找不到 `go`，关闭并重新打开终端或 GoLand，使新的用户级 `PATH` 生效。

### Docker 部署

复制环境变量示例：

```powershell
Copy-Item .env.example .env
```

编辑 `.env`，至少设置：

- `MUSIC_SERVER_MUSIC_PATH`：宿主机音乐目录
- `MUSIC_SERVER_ADMIN_PASSWORD`：生产环境不少于 12 个字符；本地开发配置使用 `admin`
- `MUSIC_SERVER_API_TOKEN`：不少于 24 个字符的随机 Token
- `MUSIC_SERVER_MEDIA_TOKEN`：另一个不少于 24 个字符的随机 Token，只允许读取音频和图片

随后启动：

```powershell
docker compose up --build
```

访问：

- 管理面板：`http://localhost:4533/admin`
- 健康检查：`http://localhost:4533/api/v1/health`

服务启动后会自动执行一次增量扫描。也可以在管理面板的“概览”页观察实时进度，或手动触发增量扫描和完整重扫。

认证状态接口示例：

```powershell
Invoke-RestMethod `
  -Uri http://localhost:4533/api/v1/status `
  -Headers @{ Authorization = "Bearer $env:MUSIC_SERVER_API_TOKEN" }
```

## Docker 数据布局

```text
/music  只读音乐目录
/data   SQLite 数据库及后续生成的封面缓存
```

音乐文件不会被服务修改、移动或删除。

## Web 管理端

登录 `/admin` 后，除扫描和元数据维护外，还可以直接管理客户端数据：

- 在专辑浏览器、歌曲列表和专辑详情中收藏或取消收藏，并在“收藏”页面集中查看
- 创建、编辑和删除歌单；按曲名、歌手或专辑搜索歌曲，添加、移除并上下调整歌曲顺序
- 分页查看客户端同步的播放状态、断点位置和播放次数
- 清空播放历史与断点位置；此操作不会删除歌曲、专辑、歌单或收藏

所有修改操作都要求管理员会话和 CSRF Token，并采用 POST 后重定向，刷新页面不会重复提交。管理端通过当前登录会话访问音频和图片，不会把 API Token 或媒体 Token 写入页面。

## 配置

| 环境变量 | 默认值 | 说明 |
|---|---:|---|
| `MUSIC_SERVER_ADDRESS` | `:4533` | HTTP 监听地址 |
| `MUSIC_SERVER_DATA_DIR` | `/data` | 可写数据目录 |
| `MUSIC_SERVER_DATABASE_PATH` | `/data/music.db` | SQLite 文件路径 |
| `MUSIC_SERVER_MUSIC_DIR` | `/music` | 容器内只读音乐目录 |
| `MUSIC_SERVER_LIBRARY_NAME` | `Music` | 音乐库显示名称 |
| `MUSIC_SERVER_ADMIN_USERNAME` | `admin` | 管理员用户名 |
| `MUSIC_SERVER_ADMIN_PASSWORD` | 无 | 管理员密码，生产环境至少 12 个字符；`admin` 仅用于本地开发 |
| `MUSIC_SERVER_API_TOKEN` | 无 | 客户端 Token，至少 24 个字符 |
| `MUSIC_SERVER_MEDIA_TOKEN` | 回退到 API Token | Sonos、图片加载器等媒体客户端使用的只读 Token，生产环境应单独设置 |
| `MUSIC_SERVER_COOKIE_SECURE` | `false` | HTTPS 部署时应设为 `true` |
| `MUSIC_SERVER_LOG_LEVEL` | `info` | `debug`、`info`、`warn` 或 `error` |

## API

### `GET /api/v1/health`

公开健康检查，不包含音乐目录和凭据等敏感信息。

### `GET /api/v1/status`

需要：

```http
Authorization: Bearer <API_TOKEN>
```

返回服务状态和当前音乐库统计。

以下接口同样使用 Bearer Token：

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/artists` | 歌手列表；可用 `role=album` 或 `role=track` 按关系分类，`favorite=true` 只看收藏 |
| `GET` | `/api/v1/artists/{id}` | 歌手详情、专辑和曲目（最多返回 5000 首，`tracksTotal` 为实际总数）；合并 ID 返回规范歌手及 `mergedFrom` |
| `GET` | `/api/v1/albums` | 专辑列表 |
| `GET` | `/api/v1/albums/{id}` | 专辑和曲目详情 |
| `GET` | `/api/v1/tracks` | 歌曲列表 |
| `GET` | `/api/v1/tracks/{id}` | 单曲详情 |
| `PATCH` | `/api/v1/artists/{id}` | 编辑歌手显示名称 |
| `PATCH` | `/api/v1/albums/{id}` | 编辑专辑标题和年份 |
| `PATCH` | `/api/v1/tracks/{id}` | 编辑歌曲元数据 |
| `GET` | `/api/v1/tracks/{id}/stream` | 原文件串流，支持 Range |
| `GET` | `/api/v1/artwork/{id}` | 封面图片 |

所有列表响应均使用统一分页结构：

```json
{
  "items": [],
  "total": 0,
  "limit": 100,
  "offset": 0
}
```

单页最多返回 500 条记录。专辑和歌曲还会返回 `addedAt`、`updatedAt`、`isFavorite`、`lastPlayedAt`；歌曲额外返回 `durationMillis`、`streamUrl`、`positionMillis` 和 `playCount`。`sort=added` 按最近入库排序，`sort=recentlyPlayed` 按最近播放排序。

歌手实体在资料、图片和外部身份层保持唯一，但浏览时可按标签关系区分：`role=album` 只返回出现在专辑歌手标签中的艺人，`role=track` 只返回出现在单曲歌手标签中的艺人，不传或使用 `role=all` 则保持兼容并返回全部艺人。同一位艺人可以同时属于两个分类。

### 客户端能力

`GET /api/v1/capabilities` 返回服务端支持的客户端能力、媒体认证方式和分页上限。需要 Bearer Token。

### 媒体访问

音频、封面、歌手图片及歌词文本只接受 `mediaToken` 查询参数或管理员会话 Cookie；不接受 Bearer API Token。Bearer Token 仅供 JSON API 使用。生产环境应配置独立的 `MUSIC_SERVER_MEDIA_TOKEN`。请求日志不记录查询参数。

```text
GET /api/v1/tracks/123/lyrics.lrc?mediaToken=<MEDIA_TOKEN>
GET /api/v1/tracks/123/stream?mediaToken=<MEDIA_TOKEN>
```

曲目 JSON 新增 `codec`、`sampleRate`、`bitDepth`、`bitrateKbps`、`channels`、`viewCount`、`lastViewedAt`（epoch 秒）、`lyricsUrl`；同步曲目另增 `discNumber`、`trackNumber`，同步专辑新增 `trackCount`、`albumType`、`compilation`、`live`、`formats`。无播放历史时播放时间与次数扩展字段省略。`POST /api/v1/playlists` 可选 `trackIds` 数组，一次事务创建并填充（去重，最多 5000 项，无效 ID 返回 400）。`GET /api/v1/tracks/{id}/lyrics.lrc` 提供外部优先的原始歌词文本（UTF BOM 自动转码）。能力端点 `apiRevision: 2`、`media.mediaAuthentication` 列出媒体认证方式，旧 `media.authentication` 字段保持兼容。

### 收藏

| 方法 | 路径 | 说明 |
|---|---|---|
| `PUT` / `DELETE` | `/api/v1/artists/{id}/favorite` | 收藏或取消收藏歌手（合并 ID 指向规范歌手） |
| `GET` | `/api/v1/favorites/artists` | 收藏歌手分页列表 |
| `PUT` / `DELETE` | `/api/v1/albums/{id}/favorite` | 收藏或取消收藏专辑 |
| `PUT` / `DELETE` | `/api/v1/tracks/{id}/favorite` | 收藏或取消收藏歌曲 |
| `GET` | `/api/v1/favorites/albums` | 收藏专辑列表 |
| `GET` | `/api/v1/favorites/tracks` | 收藏歌曲列表 |

歌手详情中的 `artist.trackCount` 统计该歌手所有角色关联的曲目；`tracksTotal` 则统计其 primary 曲目与作为专辑歌手的专辑内曲目的去重合集，两者口径不同。合并回滚后，源歌手恢复其原有收藏状态。对因合并而被收藏的目标再次显式收藏，会将其确认为用户收藏，之后回滚合并不再取消。歌手标签改名后若旧歌手成为孤儿并被 `CleanupOrphans` 删除，旧歌手的收藏会丢失；重扫现有歌手不会重置收藏。

### 歌单

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` / `POST` | `/api/v1/playlists` | 列出或创建歌单 |
| `GET` / `PATCH` / `DELETE` | `/api/v1/playlists/{id}` | 读取、编辑或删除歌单 |
| `PUT` | `/api/v1/playlists/{id}/items` | 使用有序 `trackIds` 数组替换歌单内容 |

创建或编辑歌单的请求体：

```json
{"name":"晚间播放","description":"客厅 Sonos"}
```

替换歌单内容的请求体：

```json
{"trackIds":[12,34,56]}
```

单个歌单最多保存 5000 首歌；重复 ID 会保留第一次出现的位置。

### 播放进度与历史

客户端可以每隔一段时间上报播放状态：

```http
POST /api/v1/playback/timeline
Content-Type: application/json

{"trackId":12,"state":"playing","positionMillis":30000,"durationMillis":240000,"continuing":false}
```

`state` 允许 `playing`、`paused`、`buffering` 和 `stopped`。播放达到客户端判定阈值后可记为一次完整播放：

```http
POST /api/v1/playback/scrobble
Content-Type: application/json

{"trackId":12,"positionMillis":216000,"durationMillis":240000}
```

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/playback/history` | 分页读取播放历史和断点位置 |
| `DELETE` | `/api/v1/playback/history` | 清空播放历史和断点位置 |

收藏、歌单和播放记录独立于文件扫描保存；重新扫描不会覆盖这些客户端数据。升级到包含迁移 `008_client_features.sql` 的版本后会自动建表。旧曲目的时长会在下一次增量扫描中自动回填。

列表接口支持 `q`、`artist`、`album`、`year`、`genre`、`sort`、`limit` 和 `offset`；不适用于该资源的参数会被忽略。

## 代码结构

```text
cmd/server              程序入口与生命周期
internal/config         环境配置
internal/storage        SQLite、迁移和查询
internal/metadata       多格式本地标签与封面解析
internal/scanner        增量扫描、归档和封面缓存
internal/enrichment     MusicBrainz / Last.fm 歌手匹配与资料补全
internal/http           API、管理面板和认证
```

项目保持模块化单体结构，适合直接在 GoLand 中单步调试，也可以使用同一份代码构建 Docker 镜像。

### 管理端浏览器交互测试

浏览器测试使用 Playwright Chromium，并在 `127.0.0.1:45439` 启动独立 Go 服务。测试脚本为每次运行创建临时数据目录和合成 MP3，结束后删除；不会读取 `.env`、`.local` 或现有音乐库/数据库，外部补全也不会启用。

```powershell
npm ci
npx playwright install chromium
npm run test:e2e
```

测试报告、trace、截图和 `node_modules` 均被 Git 忽略。375px/820px 项目是 Chromium 触控视口验证，不等同于真实 iOS Safari 验证。

## 歌手身份与在线元数据

管理面板的“元数据”区域包含：

- `/admin/settings/metadata`：分别启用 MusicBrainz 和 Last.fm，设置来源优先级、缓存时间及自动匹配开关。
- `/admin/matches`：批量生成候选，人工确认或拒绝低置信度结果。
- 歌手详情页：查看外部身份、重新匹配，以及把重复歌手合并到目标歌手。
- `/admin/merges`：查看合并账本并回退仍然有效的合并。

MusicBrainz 公共 API 不需要 Key，但启用时必须配置有意义的应用名称、版本和联系邮箱或项目地址。Last.fm 需要 API Key；Key 保存在本机 SQLite 数据库中，管理页不会回显。数字较小的来源优先级更高，本地标签和人工编辑始终高于在线资料。

歌手图片在身份确认或重新匹配时下载到数据目录的 `artist-images` 子目录，页面只通过本地 `/api/v1/artists/{id}/image` 接口读取，不会在每次浏览时请求远程站点。可用来源包括 Last.fm 返回的歌手图，以及 MusicBrainz 关联的 Wikidata/Wikimedia 图片；远端刷新失败不会覆盖已有缓存。

歌手简介按“语言优先级 → 来源优先级”选择，默认顺序为 `zh,ja,en` 和 Wikipedia → Last.fm。Wikipedia 只通过已确认的 MusicBrainz MBID 与 Wikidata 站点链接定位，避免同名歌手误匹配；各来源和语言的结果（包括确实无结果的状态）都会保存在 SQLite 中。歌手页会标出当前来源和语言，也可以为单个歌手固定某个可用版本或恢复全局策略。本地人工简介始终拥有最高优先级。

扫描完成后，只有同时启用“来源”和“参与自动匹配”的数据源才会进入后台匹配。文件标签携带明确 MBID，或 MusicBrainz 与 Last.fm 返回相同 MBID 时，系统才自动确认；其余结果进入审核队列。外部匹配不会自动合并两个本地歌手。

`skipCount` 与 `skipInference` 同属后续跳过推断功能，目前曲目 JSON 不提供 `skipCount`。
外部 `.lrc` 文本超过 1 MiB 时，`/lyrics.lrc` 返回 HTTP 413（不截断）。

## 封面缩略图

`GET /api/v1/artwork/{id}?size=N` 与 `GET /api/v1/artists/{id}/image?size=N` 支持正整数尺寸；向上取整到 256/512/768/1024/1536，超过 1536 使用 1536。省略 size 返回原图，非法或重复 size 返回 400。只有最长边大于目标尺寸的 JPEG、PNG、GIF（首帧）才缩放为 quality 85 的 JPEG；过大图片（边长超过 12000、像素超过 2400 万，或按解码格式估算内存超过 256 MiB）、未知格式、解码失败及小图原样返回。EXIF 方向暂不处理。原图 artwork id 若内容原地变化，旧 URL 与原图接口一样存在长期浏览器缓存风险；歌手图缓存仅一天，缩略图回退原图时要求重新验证。缓存位于 `<data>/thumbs`，`MUSIC_SERVER_THUMB_CACHE_MB` 控制容量（默认 512 MiB）；源文件大小或修改时间变化后自动重新生成。HEAD 不生成缩略图。

## 服务端转码

`GET /api/v1/tracks/{id}/transcode.mp3` 和 `.ogg` 分别实时输出 MP3 和 Opus/Ogg；支持 `bitrate`（MP3: 128/192/256/320，默认 320；Ogg: 64/96/128/160/192/256，默认 128）和 `offsetMs` 毫秒输入定位。实时流不支持非零字节 Range（416 返回 `Content-Range: bytes */*`），seek 请重新请求并设置 offsetMs。时长未知时 offsetMs 只校验非负；若定位超过实际音频长度，ffmpeg 可能只输出容器头的短流。`GET /api/v1/tracks/{id}/transcode.flac` 全量转码后缓存到 `<data>/transcode-cache`，返回 Content-Length 并支持字节 Range；`maxSampleRate=48000` 对已知采样率将 44.1k 族高解析源降至 44.1k、其他高解析源降至 48k；未知源采样率使用 ffmpeg `aformat=sample_rates=44100|48000` 兜底（实测未知 88.2k 可能选 48k），未知位深的无损源按 24-bit 输出。缓存以稳定 ETag 提供 If-Range 续传，访问时间仅记录在内存（重启后从缓存文件 mtime 初始化）。Sonos 播放 Hi-Res 可使用缓存 FLAC；首次请求必须等待转码完成。三个端点均可用 `mediaToken` 或媒体 Bearer token 授权，HEAD 不启动转码。`/api/v1/capabilities` 可查看实际可用编码器。

配置：`MUSIC_SERVER_FFMPEG_PATH`（默认 PATH 中的 ffmpeg）、`MUSIC_SERVER_TRANSCODE_LIVE_MAX`（默认 4）、`MUSIC_SERVER_TRANSCODE_CACHE_JOBS`（默认 2）、`MUSIC_SERVER_TRANSCODE_CACHE_MB`（默认 4096）。缺少 ffmpeg 或编码器时对应格式返回 503，服务器仍正常启动。
