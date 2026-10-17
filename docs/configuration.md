# 配置参考

除标注「仅 Compose」的变量外，下表都是**服务进程的环境变量**。用 Compose 部署时，变量还需要写进 `compose.yaml` 的 `environment` 才会传给容器（见 [部署进阶](deployment.md#给容器传更多环境变量)）。原生运行只读取进程环境变量，不会自动加载 `.env`。

## 必填项

| 环境变量 | 说明 |
|---|---|
| `MUSIC_SERVER_MUSIC_PATH` | 仅 Compose：宿主机上**已存在**的音乐目录，以只读方式挂载到容器的 `/music`。Windows 建议使用正斜杠，如 `D:/Music`。 |
| `MUSIC_SERVER_ADMIN_PASSWORD` | 管理员密码，非开发模式下至少 12 个字符。 |
| `MUSIC_SERVER_API_TOKEN` | 拥有完整权限的客户端 Token，至少 24 个字符。 |
| `MUSIC_SERVER_MEDIA_TOKEN` | 媒体 Token，只用于播放音频和读取图片，至少 24 个字符，必须与 API Token **不同**。 |

## 全部变量

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `MUSIC_SERVER_PORT` | `4533` | 仅 Compose：宿主机发布端口。 |
| `MUSIC_SERVER_DATA_PATH` | `./data` | 仅 Compose：宿主机数据目录（数据库、设置、歌单、历史以及图片/转码缓存）。不要放在音乐目录里。 |
| `MUSIC_SERVER_MUSIC_PATH` | Compose 必填 | 仅 Compose：宿主机音乐目录。 |
| `MUSIC_SERVER_ADDRESS` | `:4533` | HTTP 监听地址。 |
| `MUSIC_SERVER_DATA_DIR` | `/data` | 服务可写数据目录。 |
| `MUSIC_SERVER_DATABASE_PATH` | `<DATA_DIR>/music.db` | SQLite 文件路径。 |
| `MUSIC_SERVER_MUSIC_DIR` | `/music` | 服务侧音乐根目录。 |
| `MUSIC_SERVER_LIBRARY_NAME` | `Music` | 曲库显示名称。 |
| `MUSIC_SERVER_ADMIN_USERNAME` | `admin` | 初始/兜底管理员用户名。 |
| `MUSIC_SERVER_ADMIN_PASSWORD` | 必填 | 非开发模式下至少 12 个字符。 |
| `MUSIC_SERVER_API_TOKEN` | 必填 | 拥有完整权限的客户端 Token，至少 24 个字符。 |
| `MUSIC_SERVER_MEDIA_TOKEN` | 非开发模式下随机生成 | 独立的媒体 Token，至少 24 个字符；建议显式设置一个固定值。 |
| `MUSIC_SERVER_COOKIE_SECURE` | `false` | 通过 HTTPS 访问时启用，管理员 Cookie 会带 Secure 标记。 |
| `MUSIC_SERVER_LOG_LEVEL` | `info` | `debug`、`info`、`warn` 或 `error`。 |
| `MUSIC_SERVER_DEV_MODE` | `false` | 允许非空的短密码；缺少媒体 Token 时回退到 API Token。仅限本地开发。 |
| `MUSIC_SERVER_RESET_CREDENTIALS` | 关闭 | `password`、`tokens` 或 `all`。只要设置了这个变量，每次启动都会清除对应的覆盖值和所有管理员会话，恢复完成后请移除。 |
| `MUSIC_SERVER_TRUSTED_PROXIES` | 空 | 登录限流信任的代理 IP/CIDR，逗号分隔；格式错误时拒绝启动。 |
| `MUSIC_SERVER_FFMPEG_PATH` | PATH 中的 `ffmpeg` | ffmpeg 可执行文件。缺少某个编码器只会禁用对应的转码格式，不影响服务启动。 |
| `MUSIC_SERVER_TRANSCODE_LIVE_MAX` | `4` | 实时转码并发数。 |
| `MUSIC_SERVER_TRANSCODE_CACHE_JOBS` | `2` | 缓存转码并发数。 |
| `MUSIC_SERVER_TRANSCODE_CACHE_MB` | `4096` | FLAC 转码缓存容量，单位 MiB。 |
| `MUSIC_SERVER_THUMB_CACHE_MB` | `512` | 缩略图缓存容量，单位 MiB。 |
| `MUSIC_SERVER_BANGUMI_INTERVAL_MS` | `500` | Bangumi 请求间隔，200～10000 毫秒；非法值会告警并回退到默认值。 |
| `MUSIC_SERVER_MB_INTERVAL_MS` | `2000` | MusicBrainz 请求间隔，200～10000 毫秒。默认每 2 秒一次，低于官方每秒一次的上限；非法值会告警并回退到默认值。 |
| `MUSIC_SERVER_WORK_POSTER_BACKFILL` | `true` | 在启动约 30 秒后、扫描后和增强后补全缺失的作品海报。`0/false/off/no` 关闭；未知值会告警并保持开启。手动补全不受影响。 |
| `MUSIC_SERVER_WATCH_INTERVAL` | `60s` | 曲库目录监控的轮询间隔（纯数字按秒计，也可以写 `5m` 这样的时长，最小 `10s`）。检测到变化，并且连续两次轮询结果一致后，自动执行增量扫描。轮询只做 stat、不读取文件内容，所以在收不到 inotify 事件的场景（Docker Desktop 绑定挂载、SMB/NFS 等）同样有效。`0/off` 关闭监控，不影响启动时的扫描。这里只是默认值：在 管理 → 控制台 →「自动入库」中也能开关和调整间隔，立即生效、无需重启；管理页设置优先于环境变量，并且可以一键恢复默认。 |
| `MUSIC_SERVER_PURGE_MISSING` | `never` | 扫描发现音乐文件被删除后如何处理（参照 Navidrome）。文件缺失的歌曲，以及因此没有可用歌曲的专辑和歌手，会立即从浏览页、App 接口和同步接口中隐藏；文件放回去后重新扫描会自动恢复，收藏和播放记录都会保留。`never` 只隐藏、不删除；`always` 在每次扫描后永久删除；`full` 只在完整重扫后删除。永久删除会一并移除这些歌曲的收藏、播放记录和歌单条目。如果一次扫描中超过一半的文件消失（疑似掉盘），不会触发删除。这里只是默认值：在 管理 → 控制台 → 缺失文件页随时可以修改或手动清理，管理页设置优先于环境变量。 |
| `MUSIC_SERVER_CAST_BASE_URL` | 自动 | Sonos/DLNA 拉取音频和封面时使用的局域网地址，如 `http://192.168.1.10:4533`。不设置时：如果浏览器访问用的私网 IP 和音箱在同一网段，就沿用这个地址；否则使用通往音箱的本机网卡地址 + 监听端口。在 Docker bridge 网络下建议显式设置。 |
| `MUSIC_SERVER_CAST_DEVICES` | 空 | 额外探测的投送设备，逗号分隔：Sonos 主机 IP（默认端口 1400）或设备描述 URL。容器内收不到组播时使用。 |
| `MUSIC_SERVER_CAST_SSDP` | `true` | SSDP 组播发现。`0/false/off/no` 关闭后只使用 `MUSIC_SERVER_CAST_DEVICES`。在 Docker 中使用组播需要 `network_mode: host`。 |
| `MUSIC_SERVER_ANALYZER_URL` | 空 | 声学分析服务地址，如 `http://music-analyzer:8000`（compose 的 `analysis` profile）。为空表示没有部署。功能开关在管理页，默认关闭。必须是不含账号密码的 http(s) 地址，否则拒绝启动。 |
| `MUSIC_SERVER_ANALYZER_TOKEN` | 空 | 访问分析服务的共享 Token，必须与分析容器的 `ANALYZER_TOKEN` 一致（compose 已经用同一个变量传给两个容器）。 |

缓存容量和并发数必须是正整数，非法值或非正数会回退到默认值。在线元数据设置和外部服务密钥在 Web 管理页配置，保存在 SQLite 中，不是 `.env` 配置项。

分析服务容器自己的变量（`MUSIC_ANALYZER_THREADS`、`MUSIC_ANALYZER_CPUS`、`MUSIC_ANALYZER_HTTP_PROXY` 等）见 [`analyzer/README.md`](../analyzer/README.md)。

## 管理页修改与恢复凭据

在 `/admin/settings/security` 可以修改用户名和密码、轮换 Token，或者分别恢复为环境变量中的值。所有敏感操作都需要输入当前密码。**数据库中的覆盖值优先于环境变量**，但启动时环境变量里的初始凭据仍然必须通过配置校验。

密码覆盖值使用 PBKDF2-SHA256（600000 次迭代）存储，API Token 覆盖值保存 SHA-256 哈希；媒体 Token 以明文保存，方便在验证密码后查看。修改用户名或密码会让其他会话退出登录；轮换 Token 后，使用旧值的新请求会立即失效，但不会中断已经授权的音频流。

**忘记了在管理页改过的密码**：临时给服务传入 `MUSIC_SERVER_RESET_CREDENTIALS=password`（Compose 需要先把它加进 `environment`），然后重新创建并启动服务，用户名和密码就会恢复为环境变量中的值。恢复后移除这个变量，并再次重建容器。也可以用 `tokens` 或 `all`。在安全页输入的 Token 至少 24 个字符，只能包含字母、数字和 `-`、`_`、`.`、`~`。
