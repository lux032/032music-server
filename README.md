<p align="center"><img src="docs/images/icon.png" alt="032 Music Server" width="128" height="128"></p>

# 032 Music Server

**简体中文** | [English](README.en.md)

使用 Go 构建的自托管音乐库与串流服务。本地标签和人工编辑优先，提供以专辑为中心的 Web 界面，以及供配套客户端使用的 JSON API。

**项目定位：日本 ACG 优先。** 元数据匹配和作品关联主要面向日本动画、游戏、同人音乐，支持日文标题、假名索引和读音排序标签。普通电影、电视剧不属于主要适配范围，不保证匹配质量。

## 界面预览

![专辑主视图：曲库导航、播放队列与播放器控制栏](docs/images/main-view.png)

## 功能概览

- **本地曲库**：启动时自动增量扫描，音乐目录变化后自动增量入库，手动增量扫描/完整重扫，进度与失败记录，多碟专辑，以及新增、修改、丢失文件识别。
- **音频与标签**：FLAC、MP3、M4A/MP4、AAC、Ogg/OGA、Opus；采集时长、音频属性、艺人 credit 和本地歌词。
- **浏览与编辑**：专辑、专辑歌手、单曲歌手、创作者、歌曲、作品、系列；搜索、筛选、假名索引、收藏和歌单。人工修改以覆盖值保存，重扫不会改写音乐文件或覆盖修正。
- **网页播放器**：站内导航时持续播放，队列、随机/循环播放、全屏正在播放界面、歌词、播放进度与历史。当前管理界面主要为中文；英文 README 仅提供文档翻译，不改变界面语言。
- **媒体服务**：支持 HTTP Range 的原文件串流，实时 MP3/Opus 转码，缓存 FLAC 转码，封面缩略图，以及面向无法发送 API Header 的播放器的独立媒体凭据。
- **图片管理**：嵌入封面优先、目录图片回退，歌手图片和作品海报本地缓存，自定义专辑/歌手图片上传（仅保存在数据目录）。
- **可选在线增强**：MusicBrainz/Last.fm 歌手身份与简介，Bangumi 专辑/曲目/作品关联及系列建议，人工审核队列，可回退的歌手合并。
- **客户端 API**：能力查询、基于游标的曲库同步、收藏、歌单、播放上报、基于元数据的相似歌曲与曲目路径。
- **运行维护**：SQLite WAL 与启动自动迁移，管理员会话、CSRF 防护和登录限流，凭据轮换/恢复，健康检查、优雅退出，Docker 与 Compose。

## Docker Compose 部署（推荐）

仓库中的 `compose.yaml` **从当前源码构建镜像**，不是默认拉取预构建镜像。运行镜像包含 ffmpeg，以非 root 用户启动。

### 1. 准备环境与下载代码

安装 Git 和带 Compose v2 插件的 Docker（命令为 `docker compose`）。Windows/macOS 推荐 Docker Desktop，使用 Linux 容器。确认 Docker 有权访问音乐目录，尤其是 Docker Desktop 的磁盘/文件共享权限。

```sh
git clone https://github.com/lux032/032music-server.git
cd 032music-server
docker compose version
```

复制配置模板：

```sh
# Linux / macOS
cp .env.example .env
```

```powershell
# Windows PowerShell
Copy-Item .env.example .env
```

### 2. 配置目录与凭据

编辑 `.env`，替换**所有占位密码与 Token**，例如：

```dotenv
MUSIC_SERVER_PORT=4533
MUSIC_SERVER_DATA_PATH=./data
MUSIC_SERVER_MUSIC_PATH=/srv/music
MUSIC_SERVER_LIBRARY_NAME=My Music
MUSIC_SERVER_ADMIN_USERNAME=admin
MUSIC_SERVER_ADMIN_PASSWORD=YOUR_UNIQUE_PASSWORD_AT_LEAST_12_CHARACTERS
MUSIC_SERVER_API_TOKEN=YOUR_RANDOM_API_TOKEN_AT_LEAST_24_CHARACTERS
MUSIC_SERVER_MEDIA_TOKEN=YOUR_DIFFERENT_RANDOM_MEDIA_TOKEN_AT_LEAST_24_CHARACTERS
MUSIC_SERVER_COOKIE_SECURE=false
```

- `MUSIC_SERVER_MUSIC_PATH` 是**已存在的宿主机音乐目录**，只读挂载到容器 `/music`。Windows 推荐正斜杠，例如 `D:/Music`。
- `MUSIC_SERVER_DATA_PATH` 是**持久化、可写的宿主机数据目录**，挂载到 `/data`。数据库、设置、歌单、历史和图片/转码缓存都在这里。不要放进音乐目录。
- 管理员密码至少 12 个字符；API Token 和媒体 Token 各至少 24 个字符，使用**两个不同的随机值**。生产环境不要开启开发模式。妥善保存 `.env`，不要提交到仓库。
- Linux/macOS 可用 `openssl rand -hex 24`，分别执行两次生成 Token；PowerShell 可用以下命令：

```powershell
[Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(24)).ToLowerInvariant()
```

上述 PowerShell 命令需要 PowerShell 7。生成的十六进制值也符合管理端自定义 Token 的字符要求。

### 3. 处理 Linux 挂载目录权限

容器非 root 用户必须能写入 `/data`，并读取/遍历 `/music`。如果 Linux 宿主机的数据目录尚不可写，先从镜像查看实际 UID/GID，不要假定固定编号：

```sh
mkdir -p ./data
docker compose build
docker compose run --rm --no-deps --entrypoint id music-server
# 将下面的 UID/GID 替换为上一步输出的数字；DATA_PATH 改过时也要改路径。
sudo chown -R <UID>:<GID> ./data
```

执行 `chown` 前替换 `<UID>` 和 `<GID>`，只调整应用数据目录，不要直接更改整个音乐库的所有者。NAS/网络挂载还要检查 ACL；SELinux 主机可能需要适当的挂载标签。

### 4. 启动并验证

```sh
docker compose up -d --build
docker compose ps
docker compose logs --tail=100 music-server
```

访问：

- 管理端/网页播放器：`http://localhost:4533/admin`
- 公开健康检查：`http://localhost:4533/api/v1/health`

其他设备访问时，将 `localhost` 换成服务器 IP 或域名。使用 `.env` 中的账号密码登录。启动后自动执行增量扫描，在概览页查看进度。健康检查通过只表示服务可用，**不代表首次扫描已经结束**。

```sh
curl --fail http://localhost:4533/api/v1/health
curl --fail -H 'Authorization: Bearer YOUR_API_TOKEN' \
  http://localhost:4533/api/v1/status
```

PowerShell 对应命令：

```powershell
Invoke-RestMethod -Uri http://localhost:4533/api/v1/health
Invoke-RestMethod -Uri http://localhost:4533/api/v1/status `
  -Headers @{ Authorization = 'Bearer YOUR_API_TOKEN' }
```

请将示例 Token 替换为**实际配置值**。Compose 的 `.env` 不会自动设置当前终端里的 `$env:MUSIC_SERVER_API_TOKEN`。

### 5. 应用配置修改

Compose 用 `.env` 做变量替换，但**不会自动把所有变量传给服务**。只有 `compose.yaml` 的 `environment` 中列出的变量才会进入容器。高级设置需要额外添加映射，例如：

```yaml
# 添加到 services.music-server.environment 下，保持缩进一致：
MUSIC_SERVER_TRUSTED_PROXIES: "${MUSIC_SERVER_TRUSTED_PROXIES:-}"
MUSIC_SERVER_THUMB_CACHE_MB: "${MUSIC_SERVER_THUMB_CACHE_MB:-512}"
MUSIC_SERVER_BANGUMI_INTERVAL_MS: "${MUSIC_SERVER_BANGUMI_INTERVAL_MS:-500}"
MUSIC_SERVER_WORK_POSTER_BACKFILL: "${MUSIC_SERVER_WORK_POSTER_BACKFILL:-true}"
```

然后重新创建服务：

```sh
docker compose up -d
```

仅执行 `docker compose restart` 不会应用修改后的容器环境变量。除非同步调整挂载、端口映射和健康检查，否则保持容器内 `/music`、`/data` 和监听端口 `4533` 不变。

### HTTPS 与反向代理

服务本身提供 HTTP。如需公网访问，请在前面配置 HTTPS 反向代理，不要将明文管理登录直接暴露到互联网。

- HTTPS 部署设置 `MUSIC_SERVER_COOKIE_SECURE=true`；直接 HTTP 访问保持 `false`，否则浏览器不会发送登录 Cookie。
- 在域名根路径代理整个站点，保留 `/admin` 和 `/api/v1` 路径、Range 请求头，并允许长时间音频串流。
- 如果代理运行在宿主机上，可把 Compose 端口映射限制为 `127.0.0.1:${MUSIC_SERVER_PORT:-4533}:4533`。
- 将 `MUSIC_SERVER_TRUSTED_PROXIES` 加入 Compose 的 `environment`，并填写实际代理 IP/CIDR。只信任会正确覆盖/追加 `X-Forwarded-For` 的代理，不要填写 `0.0.0.0/0` 或任意客户端网段。不设置时，代理后的所有用户会共用代理 IP 的登录限流。
- 转发地址必须是无端口的纯 IP。只有连接对端属于受信代理时，服务才从右向左解析 `X-Forwarded-For`。此设置当前只影响登录限流，不负责 TLS 终止。
- 媒体 URL 含凭据，反向代理访问日志也应避免记录查询参数。

### 备份与升级

备份 `.env` 和**整个数据目录**，不要只备份 `music.db`。数据库含凭据覆盖值和外部服务密钥，应保护备份。SQLite 使用 WAL，运行期间单独复制数据库可能漏掉近期写入；最简单的一致性备份方式是先停服：

```sh
docker compose stop
# 此时备份 .env 与实际配置的 MUSIC_SERVER_DATA_PATH。
# Linux/macOS 默认 ./data 的示例：
tar -czf music-server-backup.tar.gz .env data
docker compose start
```

升级前先完成停服备份，然后执行：

```sh
git pull --ff-only
docker compose up -d --build
docker compose logs --tail=100 music-server
```

新版本启动时自动执行数据库迁移。不要假设旧程序兼容升级后的数据库；如需回退，恢复升级前的数据备份。服务不会修改、移动或删除音乐文件（`MUSIC_SERVER_PURGE_MISSING` 只清理数据库中已缺失文件的记录）。更换曲库根目录可能影响索引，升级时尽量保持挂载路径稳定。

### 常见问题

| 现象 | 排查方法 |
|---|---|
| Compose 提示缺少必填变量 | 在仓库目录运行，确认已创建 `.env`，填写密码、两个 Token 和音乐路径。 |
| 启动拒绝密码/Token | 密码至少 12 个字符，Token 至少 24 个字符；不要使用模板占位凭据。 |
| `permission denied` / 无法打开数据库 | 检查数据目录所有者、音乐目录读取/遍历权限、Docker Desktop 共享与 NAS ACL。 |
| 曲库为空 | 核对宿主机路径、支持的后缀和概览页扫描错误；挂载路径错误可能得到空目录。 |
| 登录后反复跳回登录页 | HTTP 下不要开启 Secure Cookie；检查代理和 Cookie 配置。 |
| 修改 `.env` 没生效 | 确认 Compose 转发了变量，并重新创建容器；安全页存储的覆盖凭据优先于环境变量。 |
| 媒体返回 401 | 用媒体 Token，不是 API Token；轮换后旧 URL 失效。非开发模式未设置媒体 Token 时，每次启动会随机生成。 |
| 转码返回 503 | 检查 `/api/v1/capabilities`、ffmpeg 编码器、日志及并发限制；原生运行需单独安装 ffmpeg。 |
| 某格式无法在浏览器播放 | 原文件播放受浏览器解码支持限制；换兼容客户端或使用转码接口。 |
| 在线增强因限流停止 | 等待后再重跑，不要缩短请求间隔绕过上游限制。 |

## 配置参考

除标注“仅 Compose”的变量外，下表均为**服务进程环境变量**。原生执行只读取环境变量，不会自行加载 `.env`。

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `MUSIC_SERVER_PORT` | `4533` | 仅 Compose：宿主机发布端口。 |
| `MUSIC_SERVER_DATA_PATH` | `./data` | 仅 Compose：宿主机数据目录。 |
| `MUSIC_SERVER_MUSIC_PATH` | Compose 必填 | 仅 Compose：宿主机音乐目录。 |
| `MUSIC_SERVER_ADDRESS` | `:4533` | HTTP 监听地址。 |
| `MUSIC_SERVER_DATA_DIR` | `/data` | 服务可写数据目录。 |
| `MUSIC_SERVER_DATABASE_PATH` | `<DATA_DIR>/music.db` | SQLite 文件路径。 |
| `MUSIC_SERVER_MUSIC_DIR` | `/music` | 服务侧音乐根目录。 |
| `MUSIC_SERVER_LIBRARY_NAME` | `Music` | 曲库显示名称。 |
| `MUSIC_SERVER_ADMIN_USERNAME` | `admin` | 初始/兜底管理员用户名。 |
| `MUSIC_SERVER_ADMIN_PASSWORD` | 必填 | 非开发模式至少 12 个字符。 |
| `MUSIC_SERVER_API_TOKEN` | 必填 | 完整权限客户端 Token，至少 24 个字符。 |
| `MUSIC_SERVER_MEDIA_TOKEN` | 非开发模式随机生成 | 独立媒体 Token，至少 24 个字符；应显式设置稳定值。 |
| `MUSIC_SERVER_COOKIE_SECURE` | `false` | HTTPS 下启用 Secure 管理员 Cookie。 |
| `MUSIC_SERVER_LOG_LEVEL` | `info` | `debug`、`info`、`warn` 或 `error`。 |
| `MUSIC_SERVER_DEV_MODE` | `false` | 允许非空短密码；缺少媒体 Token 时回退到 API Token。仅本地开发使用。 |
| `MUSIC_SERVER_RESET_CREDENTIALS` | 关闭 | `password`、`tokens` 或 `all`；保留期间每次启动都会清除对应覆盖值和全部管理员会话，恢复后移除。 |
| `MUSIC_SERVER_TRUSTED_PROXIES` | 空 | 登录限流信任的代理 IP/CIDR，逗号分隔；格式错误会拒绝启动。 |
| `MUSIC_SERVER_FFMPEG_PATH` | PATH 中的 `ffmpeg` | ffmpeg 可执行文件；缺少编码器只禁用相应转码格式，不影响服务启动。 |
| `MUSIC_SERVER_TRANSCODE_LIVE_MAX` | `4` | 实时转码并发数。 |
| `MUSIC_SERVER_TRANSCODE_CACHE_JOBS` | `2` | 缓存转码并发数。 |
| `MUSIC_SERVER_TRANSCODE_CACHE_MB` | `4096` | FLAC 缓存容量，MiB。 |
| `MUSIC_SERVER_THUMB_CACHE_MB` | `512` | 缩略图缓存容量，MiB。 |
| `MUSIC_SERVER_BANGUMI_INTERVAL_MS` | `500` | Bangumi 请求间隔，200～10000 毫秒；非法值告警并回退默认值。 |
| `MUSIC_SERVER_MB_INTERVAL_MS` | `2000` | MusicBrainz 请求间隔，200～10000 毫秒；默认 2 秒一次（低于官方每秒一次上限），非法值告警并回退默认值。 |
| `MUSIC_SERVER_WORK_POSTER_BACKFILL` | `true` | 启动约 30 秒后、扫描后、增强后补全缺失作品海报。`0/false/off/no` 关闭；未知值告警并保持开启。手动补全仍可用。 |
| `MUSIC_SERVER_WATCH_INTERVAL` | `60s` | 曲库目录监控的轮询间隔（纯数字按秒，或 `5m` 这类时长，最小 `10s`）。检测到变化且连续两次轮询保持一致后自动执行增量扫描。采用只做 stat、不读文件内容的轮询，Docker Desktop 绑定挂载、SMB/NFS 等收不到 inotify 事件的场景同样有效。`0/off` 关闭，启动扫描不受影响。这里只是默认值：也可在管理 → 控制台的“自动入库”中开关和调整间隔，立即生效且无需重启；管理页设置优先于环境变量，可一键恢复默认。 |
| `MUSIC_SERVER_PURGE_MISSING` | `never` | 扫描发现音乐文件被删除后如何处理（参照 Navidrome）。缺失文件对应的歌曲，以及因此没有可用歌曲的专辑和歌手，会立即从浏览页、App 接口与同步接口中隐藏；文件放回后重新扫描自动恢复，收藏和播放记录都保留。`never` 只隐藏不删除；`always` 每次扫描后永久删除；`full` 仅完整重扫后删除。永久删除会一并移除这些歌曲的收藏、播放记录和歌单条目；疑似掉盘（超过一半文件消失）的扫描不会触发删除。这里只是默认值：管理 → 控制台 → 缺失文件页可随时修改或手动清理，管理页设置优先于环境变量。 |

缓存容量/并发数要求正整数，非法或非正数回退默认值。在线元数据设置和服务密钥通过 Web 管理页配置，保存在 SQLite，不是 `.env` 配置项。

### 管理页修改与恢复凭据

在 `/admin/settings/security` 修改用户名/密码、轮换 Token，或分别恢复环境变量默认值。敏感操作均需当前密码。**数据库覆盖值优先于环境变量**，但启动时环境变量中的初始凭据仍必须通过配置校验。

密码覆盖值使用 PBKDF2-SHA256（600000 次迭代），API Token 覆盖值保存 SHA-256 哈希；媒体 Token 明文保存，以便验证密码后查看。修改用户名/密码会使其他会话退出；轮换 Token 后旧值的新请求立即失效，但不会中断已授权的音频流。

忘记覆盖密码时，临时向服务传入 `MUSIC_SERVER_RESET_CREDENTIALS=password`，重新创建/启动服务，即可恢复环境变量中的用户名和密码。Compose 中需先添加到 `environment`。恢复后移除该变量并再次重建容器。也可使用 `tokens` 或 `all`。安全页输入的 Token 至少 24 个字符，仅允许字母、数字、`-`、`_`、`.`、`~`。

## 元数据与作品关联

在 `/admin/settings/metadata` 启用可选来源。本地标签与人工修改始终优先，不会改写音乐文件。

- **MusicBrainz**：无需 API Key；填写有意义的应用标识与联系邮箱/项目地址。默认每 2 秒一次请求（低于官方每秒一次上限，避免连续满速触发过载保护，可用 `MUSIC_SERVER_MB_INTERVAL_MS` 调整）。
- **Last.fm 资料**：需要 API Key。在 `/admin/matches` 审核歌手身份；歌手页可维护身份、选择简介、上传图片和合并，`/admin/merges` 查看/回退合并。
- **Bangumi**：当前 ACG 作品关联在线来源。在 `/admin/work-review` 审核专辑/曲目音乐条目、作品身份和系列建议，`/admin/enrichment` 启动/取消任务。VGMdb 增强已下线，历史数据保留。
- **作品与系列管理**：`/admin/works`、`/admin/series`。本地推断以专辑为主，明确的曲目标签可补充单曲关联；在线增强还可单独检索曲目。不同季和剧场版保持独立作品，不确定/跨媒体的系列关系进入审核，不盲目合并。人工决定和解除关联记录会跨重扫保留。
- **图片与简介**：远端图片下载后从本地缓存提供；简介支持来源/语言优先级及单歌手选择，人工文本优先。

上游返回 429（或带 `Retry-After` 的 503）会停止当前任务；Bangumi 和 MusicBrainz 还会记录退避窗口。请等待后重试。

详细使用规则与已知限制见[作品关联使用说明](docs/guide/works-association-overview.md)和[设计说明](docs/design/works-association.md)。

### Last.fm 播放记录（Scrobble）

1. 在 [Last.fm API 应用申请页](https://www.last.fm/api/account/create) 创建应用。
2. 将 API Key 填入 `/admin/settings/metadata` 的 Last.fm 来源卡片，将 Shared Secret 填入 Last.fm Scrobble 卡片，启用并保存。
3. 点击连接，前往 Last.fm 授权，再回到管理页完成连接。无需公网回调地址；授权 Token 一小时后过期。

符合条件的计数播放（曲长超过 30 秒、歌手已知）进入持久化 SQLite 发件箱。网络失败按退避重试，超过 14 天的记录丢弃；会话失效需重新连接。密钥与会话保存在本地数据库，管理页不回显。

## API 概览

项目使用自有 `/api/v1` API，**不要假定兼容 Subsonic/OpenSubsonic**。[路由注册](internal/http/app.go)和[能力接口实现](internal/http/client_features.go)可作为代码参考。

### 认证方式

| 接口 | 认证 |
|---|---|
| `GET /api/v1/health` | 公开。 |
| 曲库/客户端 JSON API | `Authorization: Bearer <API_TOKEN>` 或管理员会话；会话认证的修改需 CSRF。 |
| 音频、转码、封面、歌手图片、原始 `.lrc` 歌词 | `?mediaToken=<MEDIA_TOKEN>`、`Authorization: Bearer <MEDIA_TOKEN>` 或管理员会话；**不接受完整权限 API Token**。 |
| 播放 timeline/scrobble 上报 | API Token、管理员会话，或媒体 Token（Bearer/查询参数）。媒体凭据不能修改曲库或歌单。 |

媒体/上报接口也接受旧 `token` 查询参数别名，新客户端应使用 `mediaToken`。应用请求日志省略查询字符串，反向代理日志需单独配置。

### 主要接口

| 方法 | 路径 | 用途 |
|---|---|---|
| `GET` | `/api/v1/status`、`/api/v1/capabilities` | 状态、统计、功能、媒体规则与可用编码器。 |
| `GET` | `/api/v1/artists`、`/api/v1/albums`、`/api/v1/tracks` | 浏览/搜索，对应 `/{id}` 获取详情。 |
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
| `GET` | `/api/v1/tracks/{id}/lyrics` | 结构化歌词（JSON 凭据）。 |
| `GET` | `/api/v1/tracks/{id}/lyrics.lrc`、`/api/v1/tracks/{id}/stream` | 原始歌词/音频（媒体凭据）。 |
| `GET` | `/api/v1/artwork/{id}`、`/api/v1/artists/{id}/image` | 图片，可传 `size` 获取缩略图。 |
| `GET` | `/api/v1/tracks/{id}/transcode.{mp3\|ogg\|flac}` | 转码媒体。 |
| `GET` | `/api/v1/tracks/{id}/similar`、`/api/v1/tracks/path` | 元数据相似度与路径（JSON 凭据）。 |
| `POST` | `/api/v1/enrichment/run` | 启动增强；`/api/v1/enrichment/jobs` 和 `/{id}` 查状态，POST `/{id}/cancel` 取消。 |

`{artists|albums|tracks}` 及格式枚举仅为表格简写，不是实际 URL。候选审核接口也在 `internal/http/app.go` 注册。

普通列表返回 `{"items":[],"total":0,"limit":100,"offset":0}`，单页最多 500 条；同步接口使用独立游标响应。浏览参数包括 `q`、`artist`、`album`、`year`、`genre`、`sort`、`limit`、`offset`，适用性取决于资源。歌手列表支持 `role=album|track|all`、`favorite=true`；歌曲支持 `hideInstrumental=true`，`sort` 支持 `title`、`year`、`date`、`plays`（播放次数）、`recentlyPlayed`（最近播放）。支持假名变体匹配，罗马音搜索需标签提供 reading/sort 字段。相似推荐基于元数据，**不是声学分析**。

歌手详情 `/api/v1/artists/{id}` 不再内嵌全部单曲：返回 `topTracks`（播放次数最多的至多 10 首，未播放过的歌不计入，按播放次数、最近播放时间排序）与 `tracksTotal`，完整列表通过 `/api/v1/artists/{id}/tracks` 分页获取。单曲排序默认方向：`album`（年份→专辑→碟号→曲号）、`title`、`duration` 升序；`plays`、`recent`、`added` 降序；`recent` 中未播放过的歌始终排在最后；同值时按专辑顺序。

创建歌单可传 `{"name":"晚间播放","description":"客厅","trackIds":[12,34]}`，最多 5000 首去重歌曲，保留第一次出现的顺序。

### 播放上报（会话协议，apiRevision 3）

每次播放是一个持久化会话：客户端为每次播放生成新的 `sessionId`（建议 UUID），`seq` 从 1 起单调递增。向 `/api/v1/playback/events` 发送：

```json
{"clientId":"device-abc","clientKind":"android","sessionId":"7c9e…","seq":1,"type":"start","trackId":12,"state":"playing","positionMillis":0,"durationMillis":240000}
```

- `type`：`start`（必须携带真实初始 `state`：`playing`/`buffering`/`paused`）、`heartbeat`（同样携带真实 state）、`pause`/`buffering`/`resume`（state 由类型推导，不要发送）、`seek`（保留当前 state，仅更新位置）、`end`（必须携带 `endReason`）。
- `endReason`：`completed`（断点归零）、`skipped`、`stopped`、`replaced`、`error`、`client_closed`。
- 心跳节奏：playing/buffering 每 15 秒（租约 90 秒），paused 每 60 秒（租约 10 分钟）。超过租约会话失效，历史显示“已中断”，不算跳过/完成。
- 续播定义：refresh、restoreState、BFCache 恢复、进程重启后恢复同一首歌都属于续播——客户端持久化最近一个 sessionId+trackId，start 必须携带 `resumedFromSessionId`。只有明确切歌或循环重播才是不带 resume 的新播放（新会话）。start 网络超时后必须用同一个 sessionId 重发（服务端视为幂等重放）。
- 响应 200 `{"applied":bool,"state":"…","positionMillis":…,"counted":bool}`；`seq` 过旧或终态后的迟到事件返回 `applied:false`（不续租、不改断点），终态不可复活。
- 错误：`404 session_not_found`（未知/已清空会话：若歌曲确实播放过且已过计数门槛，以 state playing、最终位置补 start 并以原 endReason end，恢复这次播放；没播放过绝不能伪装 playing；否则丢弃，下次播放重新开始）；`409 session_expired`（已过期并被固化：串行处理，仅开一个 resume 会话——以当前位置 start 并携带 `resumedFromSessionId`；迟到的 end 先以最终位置 start resume，再以原 endReason end。补发的恢复 start 用 state playing 是因为恢复对象确实播放过）；`409 session_owner_mismatch` / `session_conflict` / `resume_invalid`（前序不存在/异 client/异 track，或以 completed/skipped/replaced 结束；同 client 同 track 的活跃前序不算错误：服务端在同事务内将其以 replaced 取代并接受续播。收到 resume_invalid 禁止静默退化为不带 resume 的高位置新 start——那是新播放，会重新计数）。
- `end(completed)` 必须携带真实最终位置（≈时长），绝不能发 0；断点归 0 由服务端处理。
- 计数：位置过 50%（1 秒 slack）且有真实播放证据（事件前或事件后的状态为 playing）时服务端计一次；从头到尾只有 paused/buffering 的会话无论以何种 endReason 结束都不计数（暂停中高位置 seek 后 end 同样不计）；播放越过门槛后在 pause/buffering/end 时按当时位置计数；每条续播链（chain_id）最多计一次，由事件事务内实时查询加数据库唯一部分索引双重保证——未计数前序的顺序/并发分叉也只计一次；只有明确的新播放（切歌/循环重播）重新计数；多端独立会话分别计数。
- 跳过：仅 `endReason=skipped` 且结束位置低于 MIN(30s, 时长/2) 时计一次跳过。
- 断点：最后被接受的事件生效；`completed` 归零；过期保留最后位置。
- 旧的 `/api/v1/playback/timeline` 与 `/api/v1/playback/scrobble` 已移除，返回 410；无兼容路径。清空历史同时清空会话。
- 鉴权沿用 apiToken / 管理会话 / mediaToken；单用户服务端，`clientId` 仅绑定会话归属，不是强安全身份。

### 媒体行为

- 原音频支持字节 Range。原始歌词优先使用外部 `.lrc`，超过 1 MiB 返回 413。
- 缩略图将 `size` 向上取整到 256/512/768/1024/1536；省略时返回原图。不支持/不安全/过小的图片可能原样返回。缓存位于 `<data>/thumbs`。
- 实时 MP3：`bitrate=128|192|256|320`（默认 320）；Opus/Ogg：`64|96|128|160|192|256`（默认 128）。定位使用 `offsetMs`，非零字节 Range 返回 416。
- 缓存 FLAC 首次转码完成后提供 Content-Length 和字节 Range。`maxSampleRate=48000` 可将高解析源降至 44.1/48 kHz 族。缓存位于 `<data>/transcode-cache`。
- HEAD 不启动转码。缺少 ffmpeg/编码器时相应格式返回 503，应查询 capabilities 判断实际能力。

## 本地开发与测试

按照 `go.mod`，使用 **Go 1.26 或更新版本**。运行服务不需要 Node 构建：Web 资源、模板和数据库迁移均已嵌入。原生转码需单独安装 ffmpeg。原生运行读取进程环境变量，不读取 Compose 的 `.env`。

Windows 本地开发在仓库根目录用 **PowerShell 7** 执行：

```powershell
# 脚本默认使用 admin/admin，必须显式允许短密码：
$env:MUSIC_SERVER_DEV_MODE = '1'
# 可选：使用自己的音乐目录，否则读取 ./.local/music
$env:MUSIC_SERVER_MUSIC_DIR = 'D:\Music'
.\scripts\dev.ps1
```

脚本默认数据目录为 `./.local/data`，音乐目录为 `./.local/music`，管理端为 `http://localhost:4533/admin`，会从环境加载或生成 API/媒体 Token。脚本**不会自行开启开发模式**；也可预先设置强 `MUSIC_SERVER_ADMIN_PASSWORD`，避免使用默认短密码。已有数据库覆盖值可能优先于这些默认凭据。不要将开发凭据用于公网部署。

原生构建时，先设置必填凭据、可写数据目录和音乐路径，再执行：

```sh
go build -o music-server ./cmd/server
# 运行 ./music-server；Windows 若输出为 music-server.exe，则运行 .\music-server.exe。
```

| 测试类别 | 命令 |
|---|---|
| 正确性 | `go test ./...` |
| 数据竞争 | `go test -race -count=1 ./internal/http/... ./internal/storage/...`（需 CGO 与 C 编译器） |
| 性能门槛 | `go test -tags=performance -count=1 ./internal/storage -run '^TestSimilarTracksSyntheticPerformance$'` |
| 浏览器交互 | `npm ci`、`npx playwright install chromium`、`npm run test:e2e` |

Playwright 在 `127.0.0.1:45439` 启动独立服务，使用临时合成音乐和数据，不读取 `.env` 或现有曲库。性能测试按标签启用，与 race 测试分开。触控视口测试不等于真实 iOS Safari 验证。人工验收见[检查清单](docs/testing/manual-checklist.md)。

## 代码结构

```text
cmd/server          入口与生命周期
internal/config     环境配置与校验
internal/storage    SQLite、迁移与查询
internal/metadata   本地标签、credit 与封面解析
internal/scanner    增量扫描与专辑归档
internal/enrichment 在线身份、资料、作品与系列增强
internal/lastfm     Scrobble 客户端与持久化发件箱
internal/http       JSON API、嵌入式管理端/播放器、认证与媒体
```

## 许可证

[MIT](LICENSE)。
