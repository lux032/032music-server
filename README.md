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
| `GET` | `/api/v1/artists` | 歌手列表 |
| `GET` | `/api/v1/albums` | 专辑列表 |
| `GET` | `/api/v1/tracks` | 歌曲列表 |
| `PATCH` | `/api/v1/artists/{id}` | 编辑歌手显示名称 |
| `PATCH` | `/api/v1/albums/{id}` | 编辑专辑标题和年份 |
| `PATCH` | `/api/v1/tracks/{id}` | 编辑歌曲元数据 |
| `GET` | `/api/v1/tracks/{id}/stream` | 原文件串流，支持 Range |
| `GET` | `/api/v1/artwork/{id}` | 封面图片 |

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
