# 本地开发与测试

需要 **Go 1.26 或更新版本**（见 `go.mod`）。运行服务不需要 Node 构建，因为 Web 资源、模板和数据库迁移都已嵌入二进制文件。原生运行的转码功能需要自行安装 ffmpeg。原生运行只读取进程环境变量，不会读取 Compose 用的 `.env`。

## Windows 开发脚本

在仓库根目录用 **PowerShell 7** 执行：

```powershell
# 脚本默认使用 admin/admin，必须显式允许短密码：
$env:MUSIC_SERVER_DEV_MODE = '1'
# 可选：使用自己的音乐目录，否则读取 ./.local/music
$env:MUSIC_SERVER_MUSIC_DIR = 'D:\Music'
.\scripts\dev.ps1
```

脚本默认的数据目录是 `./.local/data`，音乐目录是 `./.local/music`，管理端地址为 `http://localhost:4533/admin`；API/媒体 Token 会从环境变量读取，没有则自动生成。脚本**不会自动开启开发模式**。也可以预先设置一个足够强的 `MUSIC_SERVER_ADMIN_PASSWORD`，这样就不用默认的短密码。数据库中已有的覆盖值可能优先于这些默认凭据。不要把开发凭据用在公网部署上。

## 原生构建与运行

先设置必填凭据、可写数据目录和音乐路径（见 [配置参考](configuration.md)），然后执行：

```sh
go build -o music-server ./cmd/server
./music-server            # Windows 上输出为 music-server.exe 时，运行 .\music-server.exe
```

## 测试

| 测试类别 | 命令 |
|---|---|
| 正确性 | `go test ./...` |
| 数据竞争 | `go test -race -count=1 ./internal/http/... ./internal/storage/...`（需要 CGO 和 C 编译器） |
| 性能门槛 | `go test -tags=performance -count=1 ./internal/storage -run '^TestSimilarTracksSyntheticPerformance$'` |
| 浏览器交互 | `npm ci`、`npx playwright install chromium`、`npm run test:e2e` |

Playwright 会在 `127.0.0.1:45439` 启动一个独立的服务，使用临时生成的音乐和数据，不读取 `.env` 或现有曲库。性能测试通过构建标签启用，与 race 测试分开运行。触控视口测试不能代替真实 iOS Safari 上的验证。人工验收见 [检查清单](testing/manual-checklist.md)。

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
analyzer            可选的声学分析服务（Python）
```
