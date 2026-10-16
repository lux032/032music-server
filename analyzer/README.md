# 声学分析服务（music-analyzer）

032 Music Server 的可选组件：在本地“听”一遍曲库，输出风格、情绪、乐器、人声/可舞性与 1280 维声学向量，供每日推荐、相似歌曲等功能使用。服务本身无状态，所有结果由 032 保存在它自己的 SQLite 中。

- 只读挂载音乐目录，不修改任何文件；只在 compose 内网提供服务，不发布端口。
- 推理使用 ONNX Runtime（失败时自动回退 OpenVINO CPU），**不需要 AVX**，已在 Celeron J4125 上实测（见 `docs/daily-mix-design.md` 第 13.4 节）。
- 预处理是 Essentia `TensorflowInputMusiCNN` 的 numpy 复刻，与 Essentia 官方回归数据的余弦相似度为 0.99994，不依赖 TensorFlow 与 Essentia 本体。
- 功能默认关闭：部署后需要在 **管理 → 控制台 → 声学分析** 中打开，打开即开始首次全量分析；之后每次曲库扫描完成，新入库或文件变化的歌曲会自动分析。

## 模型与许可

使用 MTG Essentia 官方预训练模型（discogs-effnet、mtg_jamendo_moodtheme / instrument、mood_*、voice_instrumental、danceability、engagement、approachability、msd-musicnn + DEAM），许可为 **CC BY-NC-SA 4.0，仅限非商业用途**。模型不在仓库和镜像里，服务首次启动时从 `essentia.upf.edu` 下载到 `/models` 卷（约 53MB）。

本目录代码使用与仓库相同的 MIT 许可，只依赖 numpy、onnxruntime、openvino。

## 部署

在仓库根目录（`compose.yaml` 所在目录）：

1. 在 `.env` 中添加：
   ```env
   MUSIC_SERVER_ANALYZER_URL=http://music-analyzer:8000
   # 两个容器共用的随机值，例如 openssl rand -hex 24 的输出
   MUSIC_SERVER_ANALYZER_TOKEN=请替换为随机字符串
   ```
2. 启动（会同时重建 music-server 以读取新变量）：
   ```sh
   docker compose --profile analysis up -d --build
   ```
3. 等待 `docker logs 032music-analyzer` 出现 `analyzer ready`，然后在管理页打开“声学分析”。

可选变量（`.env`）：

| 变量 | 默认 | 说明 |
|---|---|---|
| `MUSIC_ANALYZER_THREADS` | `2` | 推理线程数 |
| `MUSIC_ANALYZER_CPUS` | `2` | 容器 CPU 上限 |
| `MUSIC_ANALYZER_HTTP_PROXY` | 空 | 首次下载模型时使用的代理（模型缓存后可去掉） |
| `MUSIC_ANALYZER_NO_PROXY` | 空 | 不走代理的地址 |

### 需要代理才能构建镜像时（例如 QNAP）

Docker daemon 的代理只用于拉取基础镜像，构建阶段的 `apt-get`、`pip` 不会使用它。可以先用 `docker build` 预先构建 compose 使用的同名镜像，代理只通过环境变量传入、只传参数名，地址不会出现在命令行和镜像历史中：

```sh
export HTTP_PROXY=http://<代理主机局域网IP>:<端口>
export HTTPS_PROXY=$HTTP_PROXY http_proxy=$HTTP_PROXY https_proxy=$HTTP_PROXY
docker build -t 032music-analyzer:local \
  --build-arg HTTP_PROXY --build-arg HTTPS_PROXY --build-arg http_proxy --build-arg https_proxy \
  ./analyzer
docker compose --profile analysis up -d
```

代理含账号密码时改用 BuildKit secret（凭证不会进入镜像层、历史或构建参数）：

```sh
umask 077; printf '%s\n' 'http://用户:密码@<代理主机IP>:<端口>' > ~/.analyzer-proxy
DOCKER_BUILDKIT=1 docker build -t 032music-analyzer:local -f analyzer/Dockerfile.buildkit \
  --secret id=build_proxy,src=$HOME/.analyzer-proxy ./analyzer
```

还可以设置的构建参数（非敏感，会写入镜像历史）：`--build-arg APT_MIRROR=https://mirrors.tuna.tsinghua.edu.cn`、`--build-arg PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple`、`--build-arg APT_FORCE_IPV4=1`。构建日志中出现 `[build] 代理：已启用` 即表示代理生效（加 `--no-cache` 可强制重新执行该步骤）。

### 预构建镜像

CI 会发布 `ghcr.io/lux032/032music-server-analyzer`（linux/amd64，标签 `latest`、`main`、`sha-<短提交号>`，镜像标签 `org.opencontainers.image.revision` 为完整提交号）。使用预构建镜像就不需要在本机构建：在 compose 中把 `music-analyzer` 的 `build:` 换成 `image: ghcr.io/lux032/032music-server-analyzer:sha-<提交号>`。建议用 `标签@sha256:…` 固定版本。

### QNAP（Container Station）部署注意事项

**在 QNAP 上，不要把 032 和分析服务放到 compose 新建的网络里。** QNAP 默认的 bridge 网络（`10.0.3.0/24`，网关 `10.0.3.1`）由 Container Station 管理，转发和 NAT 规则是完整的。compose 自动创建的 `<项目名>_default` 网络在 QNAP 上可能出现这样的情况：容器连不上局域网里的代理，连外网也不正常。实测现象是：分析服务下载模型时反复出现 `<urlopen error timed out>`，但 Docker 仍显示 healthy，同时 032 的 Bangumi/MusicBrainz 刮削也开始报错；网段并不重叠（`172.29.0.0/22`），换回默认 bridge 后立即恢复。

默认 bridge 网络不支持用服务名互相访问，所以 `http://music-analyzer:8000` 不可用。做法是把分析服务的端口**只发布到 bridge 网关 IP**，032 通过网关访问它：

```yaml
services:
  music-server:
    network_mode: bridge
    # …其余配置不变
  music-analyzer:
    network_mode: bridge
    ports:
      - "10.0.3.1:18000:8000"   # 只绑定网关 IP，局域网访问不到；不要写成 "18000:8000"
    # …其余配置同仓库 compose.yaml
```

```env
# .env
MUSIC_SERVER_ANALYZER_URL=http://10.0.3.1:18000
```

- 网关 IP 以 `docker network inspect bridge` 中的 `Gateway` 为准，并先确认该 IP 上的 18000 端口没有被占用。
- 验证连通：`docker exec 032music-server wget -qO- http://10.0.3.1:18000/health` 应返回 `"status": "ready"`；在 NAS 宿主机上访问 `http://<NAS局域网IP>:18000/health` 应连接失败。
- 如果分析服务需要代理下载模型，代理地址填局域网 IP（例如 `MUSIC_ANALYZER_HTTP_PROXY=http://<代理主机局域网IP>:<端口>`）。模型下载完成后会缓存在卷里，以后启动不再需要代理。
- 不建议用 `network_mode: service:music-server` 共享网络：032 重启后，分析容器会断网，必须手动重启它。
- 判断模型是否就绪要看日志里的 `analyzer ready`，或 `/health` 返回的 `status`。Docker 的 healthy 只表示 HTTP 服务在运行，不代表模型已经加载完成。

## 资源占用（J4125 实测）

| 项 | 数值 |
|---|---|
| 每首耗时 | 约 2.9 秒（快速模式，2 线程）；校准样本（完整模式，约 300 首）约 7 秒 |
| 1.7 万首全量 | 约 12–14 小时（可在管理页限制运行时间窗；有转码或扫描时自动让路） |
| 内存 | 约 300MB（完整模式约 1.2GB 峰值） |
| 数据库增量 | 每首约 3.8KB，1.7 万首约 65MB |

## HTTP 接口（供 032 调用）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/health` | `{status: loading/ready/error, analysisVersion, backend, fullModeAvailable}`；无需鉴权 |
| GET | `/v1/labels` | 各向量的类别名 |
| POST | `/v1/analyze` | `{path（相对音乐目录）, durationMs?, channels?, mode: fast/full}` → 向量（float16 小端 base64）、分类头、完整模式下的 musicnn 唤醒度/效价 |
| POST | `/v1/av/fit` | 用校准样本拟合唤醒度/效价线性回归（5 折交叉验证选择正则与方案） |

设置 `ANALYZER_TOKEN` 后，`/v1/*` 需要 `Authorization: Bearer <token>`。同一时间只分析一首，并发请求返回 503 + `Retry-After`；文件不存在 404、无法解码 422（032 视为永久失败，文件变化前不再重试）。

## 开发与测试

```sh
cd analyzer
python -m venv .venv && . .venv/bin/activate && pip install -r requirements.txt
python -m unittest discover -s tests -v       # 单元测试（不需要模型）
sh tests/test_dockerfiles.sh                    # 两个 Dockerfile 的一致性与代理参数检查
# 集成测试：需要 ffmpeg 与模型目录
ANALYZER_TEST_MODELS=/path/to/models ANALYZER_TEST_AUDIO=/path/to/song.flac python -m unittest discover -s tests -v
# 本地运行
ANALYZER_MUSIC_DIR=/path/to/music ANALYZER_MODELS_DIR=/tmp/models ANALYZER_LISTEN=127.0.0.1:8000 python -m musicanalyzer.server
```

修改分析逻辑（模型、窗口、预处理）时必须同步修改 `musicanalyzer/engine.py` 中的 `ANALYSIS_VERSION`，032 会据此自动重新分析整个曲库。
