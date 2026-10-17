# 部署进阶

[README](../README.md) 中的「快速部署」已经够大多数用户使用。这里放高级场景：目录权限、给容器传更多变量、HTTPS 反向代理、Sonos/DLNA 投送、声学分析、预构建镜像，以及备份、升级和故障排查。

## 目录权限（Linux / NAS）

容器内用非 root 用户运行，所以它必须能写入 `/data`，也能读取和进入 `/music` 下的各级目录。如果日志里有 `permission denied` 或无法打开数据库，先查看镜像里的实际 UID/GID（不要假定是哪个固定编号）：

```sh
mkdir -p ./data
docker compose build
docker compose run --rm --no-deps --entrypoint id music-server
# 把 <UID>/<GID> 换成上一步输出的数字；改过 MUSIC_SERVER_DATA_PATH 的话，路径也要一起改
sudo chown -R <UID>:<GID> ./data
```

只调整应用数据目录的所有者，**不要改整个音乐库的所有者**。NAS 或网络挂载还要检查 ACL，SELinux 主机可能需要设置合适的挂载标签。Windows/macOS 用 Docker Desktop 时，要在 Docker Desktop 的文件共享设置里放行音乐目录所在的磁盘。

## 给容器传更多环境变量

Compose 会读取 `.env` 做变量替换，但**不会把所有变量都传进容器**，只有 `compose.yaml` 的 `environment` 里列出的变量才会生效。要用 [配置参考](configuration.md) 中的其他变量，就在 `services.music-server.environment` 下添加，例如：

```yaml
MUSIC_SERVER_TRUSTED_PROXIES: "${MUSIC_SERVER_TRUSTED_PROXIES:-}"
MUSIC_SERVER_THUMB_CACHE_MB: "${MUSIC_SERVER_THUMB_CACHE_MB:-512}"
MUSIC_SERVER_BANGUMI_INTERVAL_MS: "${MUSIC_SERVER_BANGUMI_INTERVAL_MS:-500}"
MUSIC_SERVER_WORK_POSTER_BACKFILL: "${MUSIC_SERVER_WORK_POSTER_BACKFILL:-true}"
```

然后执行 `docker compose up -d` 重新创建容器。只执行 `docker compose restart` **不会**应用新的环境变量。除非同时修改挂载、端口映射和健康检查，否则不要改动容器内的 `/music`、`/data` 和监听端口 `4533`。

## 使用预构建镜像

仓库里的 `compose.yaml` 默认**从源码构建**镜像。CI 也会把 main 分支和版本标签构建出的镜像发布到 `ghcr.io/lux032/032music-server`（标签有 `latest`、`main`、`v*` 和 `sha-<短提交号>`）。不想在本机构建，可以把 `music-server` 下的 `build:` 整段换成：

```yaml
    image: ghcr.io/lux032/032music-server:latest
```

之后用 `docker compose pull && docker compose up -d` 升级。想固定版本的话，用 `sha-<提交号>` 或版本标签。分析服务的预构建镜像见 [`analyzer/README.md`](../analyzer/README.md#预构建镜像)。

## HTTPS 与反向代理

服务本身只提供 HTTP。要从公网访问，请在前面放一个 HTTPS 反向代理，**不要把明文的管理登录直接暴露到互联网**。

- 用 HTTPS 访问时设置 `MUSIC_SERVER_COOKIE_SECURE=true`；直接用 HTTP 访问时保持 `false`，否则浏览器不会发送登录 Cookie。
- 在域名根路径代理整个站点：保留 `/admin` 和 `/api/v1` 路径，转发 Range 请求头，并允许长时间的音频串流连接。
- 代理运行在宿主机上时，可以把端口映射限制为 `127.0.0.1:${MUSIC_SERVER_PORT:-4533}:4533`（用投送功能时不要这样做，见下文）。
- 把 `MUSIC_SERVER_TRUSTED_PROXIES` 加入 Compose 的 `environment`，填写代理实际的 IP/CIDR。只信任会正确覆盖或追加 `X-Forwarded-For` 的代理，**不要填 `0.0.0.0/0`** 或任意客户端网段。不设置的话，代理后面的所有用户会共用代理 IP 的登录限流。
- 转发地址必须是不带端口的纯 IP。只有连接对端是受信代理时，服务才会从右往左解析 `X-Forwarded-For`。这个设置目前只影响登录限流，和 TLS 终止无关。
- 媒体 URL 中带有凭据，反向代理的访问日志应避免记录查询参数。

## 投送到 Sonos / DLNA

投送时，服务端通过局域网控制音箱，音箱再**直接连回服务端**拉取音频和封面。Docker 默认的 bridge 网络有两个限制：容器收不到 SSDP 组播，所以发现不了设备；服务端推断出的回连地址是容器 IP（`172.x`），音箱访问不到。下面两种方式选一种：

**方式 A：保持 bridge 网络（通用，Windows/macOS 也能用）**

在 `.env` 中设置（`compose.yaml` 已经传入这两个变量）：

```dotenv
# 宿主机的局域网 IP + MUSIC_SERVER_PORT，必须是音箱能直接访问的 http 地址
MUSIC_SERVER_CAST_BASE_URL=http://192.168.1.10:4533
# Sonos 填 IP（默认端口 1400）；DLNA 设备填完整的设备描述 URL；多个用逗号分隔
MUSIC_SERVER_CAST_DEVICES=192.168.1.30
```

Sonos 编组只需填任意一台音箱的 IP，服务端会通过拓扑自动找到组协调器和其他房间。

**方式 B：host 网络（仅限 Linux 宿主机）**

在 `services.music-server` 下加上 `network_mode: host`，并删除 `ports:`（host 模式下端口映射无效，服务直接监听宿主机的 `4533`）。这样 SSDP 能自动发现 Sonos 和 DLNA 设备，回连地址也会自动推断，上面两个变量可以不设。如果反向代理原来指向容器端口，要改成指向宿主机的 `4533`。

**注意事项**

- 不要把端口映射限制成 `127.0.0.1:...`，否则音箱连不上。两者都需要时，用 `MUSIC_SERVER_CAST_BASE_URL` 指向一个音箱能访问的地址（例如局域网内的反向代理）。
- 宿主机防火墙要允许音箱访问这个端口。
- `MUSIC_SERVER_CAST_BASE_URL` 也可以设成 HTTPS 域名（需要有效证书），但音频会绕经代理，内网访问还依赖 NAT 回流，所以局域网 IP 更稳定。
- 队列中的 URL 带有媒体 Token，轮换媒体 Token 后需要重新投送。
- 验证方法：`curl -H 'Authorization: Bearer YOUR_API_TOKEN' http://localhost:4533/api/v1/cast/devices` 能列出音箱，网页播放栏的投送按钮里能看到房间名，就说明配置成功。
- MusicBridge App 由手机直接控制 Sonos，不受上述设置影响；音箱拉流使用的是 App 中配置的服务器地址。

投送的具体行为见 [功能说明](features.md#投送到-sonos--dlna)。

## 声学分析服务

可选的 `music-analyzer` 容器用本地模型分析曲库的风格、情绪、乐器、人声/可舞性、唤醒度/效价和声学向量，为每日推荐和声学相似度提供依据。这个容器默认不部署，功能也默认关闭。

```dotenv
# .env：两个容器共用同一个随机 Token
MUSIC_SERVER_ANALYZER_URL=http://music-analyzer:8000
MUSIC_SERVER_ANALYZER_TOKEN=请替换为随机字符串
```

```sh
docker compose --profile analysis up -d --build
```

然后在 **管理 → 控制台 → 声学分析** 打开开关。打开后立即开始首次全量分析：1.7 万首歌在 4 核 NAS 上大约需要 12–14 小时，可以限制运行时间窗，有转码或扫描任务时会自动让路。之后新入库的歌曲会在扫描完成后自动分析。

分析服务只读挂载音乐目录，不发布端口，不需要 AVX。模型来自 MTG Essentia（CC BY-NC-SA 4.0，**仅限非商业用途**），在首次启动时下载。通过代理构建、预构建镜像、资源占用和接口说明见 [`analyzer/README.md`](../analyzer/README.md)。

> **QNAP 用户**：请让 032 继续使用 Container Station 默认的 `bridge` 网络，不要改用 compose 新建的网络（在 QNAP 上可能导致容器连不上局域网代理和外网）。分析服务同样使用 bridge，把端口只发布到网关 IP（如 `10.0.3.1:18000:8000`），并设置 `MUSIC_SERVER_ANALYZER_URL=http://10.0.3.1:18000`。详见 [`analyzer/README.md` 的 QNAP 一节](../analyzer/README.md#qnapcontainer-station部署注意事项)。

## 备份与升级

需要备份 `.env` 和**整个数据目录**，不要只备份 `music.db`。数据库里有凭据覆盖值和外部服务密钥，备份文件也要妥善保管。SQLite 使用 WAL 模式，运行期间单独复制数据库可能漏掉最近的写入，最简单的一致性备份方法是先停止服务：

```sh
docker compose stop
# 备份 .env 和实际配置的 MUSIC_SERVER_DATA_PATH，以默认的 ./data 为例：
tar -czf music-server-backup.tar.gz .env data
docker compose start
```

升级前先按上面的方法停服备份，然后执行：

```sh
git pull --ff-only
docker compose up -d --build
docker compose logs --tail=100 music-server
```

新版本启动时会自动迁移数据库。不要假设旧版程序能读取升级后的数据库，如果要回退版本，请恢复升级前的备份。服务不会修改、移动或删除音乐文件（`MUSIC_SERVER_PURGE_MISSING` 只清理数据库中已缺失文件的记录）。更换曲库根目录可能影响索引，升级时尽量保持挂载路径不变。

## 常见问题

| 现象 | 排查方法 |
|---|---|
| Compose 提示缺少必填变量 | 确认在仓库目录下运行，并且已经创建 `.env`，填好了密码、两个 Token 和音乐路径。 |
| 启动时拒绝密码/Token | 密码至少 12 个字符，Token 至少 24 个字符，并且不能使用模板里的占位值。 |
| `permission denied` / 无法打开数据库 | 见上文「目录权限」：检查数据目录所有者、音乐目录的读取/进入权限、Docker Desktop 文件共享和 NAS ACL。 |
| 曲库为空 | 核对宿主机路径、文件后缀是否受支持，以及概览页的扫描错误；挂载路径写错时容器里会是空目录。 |
| 登录后反复跳回登录页 | 用 HTTP 访问时不要开启 `MUSIC_SERVER_COOKIE_SECURE`；检查代理和 Cookie 配置。 |
| 修改 `.env` 后没生效 | 确认 Compose 把这个变量传进了容器，并用 `docker compose up -d` 重新创建容器。在安全页保存的凭据优先于环境变量。 |
| 媒体返回 401 | 要用媒体 Token，不是 API Token；轮换后旧 URL 会失效。非开发模式下没设置媒体 Token 时，每次启动都会随机生成一个。 |
| 转码返回 503 | 检查 `/api/v1/capabilities`、ffmpeg 编码器、日志和并发限制；原生运行需要自行安装 ffmpeg。 |
| 某种格式在浏览器里放不了 | 播放原文件受浏览器解码能力限制，可以换兼容的客户端或使用转码接口。 |
| 在线增强因限流停止 | 等一段时间后再重跑，不要通过缩短请求间隔来绕过上游限制。 |
| 忘记管理员密码 | 见 [配置参考 → 恢复凭据](configuration.md#管理页修改与恢复凭据)。 |
