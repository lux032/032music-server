# 声学分析 P0 验证工具

用于在**实际运行 032 的 NAS**上验证声学分析是否可行，对应设计文档 `docs/daily-mix-design.md` 第 12、13 节。

要回答的问题：
1. 推理后端能不能在 Celeron J4125（**无 AVX**）上运行？ONNX Runtime 不行时会自动尝试 OpenVINO。
2. 每首需要多久？1.7 万首全量需要多久？
3. 模型给出的风格/情绪/乐器/人声，对你的曲库（尤其 ACG、纯音乐）准不准？
4. 声学相似度（"最像的 10 首"）听起来对不对？

## 安全性
- 音乐目录以**只读**方式挂载，不修改任何文件，也不连接、不修改 032 的数据库。
- 联网操作只有两处：构建镜像时（apt 安装 ffmpeg、pip 安装依赖），以及首次运行时从 `essentia.upf.edu` 下载模型（约 53MB，缓存到 `./p0-models`）。需要代理时见下文「通过代理构建」。
- 容器限制为 2 核、2GB 内存，进程优先级最低（nice 19）。

## 模型与许可
MTG Essentia 官方模型（discogs-effnet、mtg_jamendo_moodtheme / instrument、mood_*、voice_instrumental、danceability、engagement、approachability、msd-musicnn + DEAM），许可为 **CC BY-NC-SA 4.0（仅限非商业）**。模型不放进仓库，也不打进镜像。

预处理是用 numpy 复刻的 Essentia `TensorflowInputMusiCNN`（不依赖 TensorFlow，不会因为缺少 AVX 而崩溃）。已对照 Essentia 官方回归数据验证：同一音频的 400 类风格输出余弦相似度为 **0.99994**，Top-5 完全一致。

## 使用方法
把整个 `tools/audio-analyzer-p0` 目录拷到 NAS 上，然后：

```sh
cd audio-analyzer-p0
chmod +x run-p0.sh
# MUSIC 填 032 用的音乐目录（即 compose 里的 MUSIC_SERVER_MUSIC_PATH）
MUSIC=/path/to/music ./run-p0.sh
```
默认随机抽取 200 首，预计 30–90 分钟。中途中断后重新执行相同命令即可续跑。完成后查看 `./p0-out/summary.md`。

### 常用参数（加在命令后面）
| 参数 | 作用 |
|---|---|
| `--sample 300` | 抽样数量 |
| `--include '动画\|Anime'` | 只在路径匹配正则的曲目中抽样（例如只测 ACG 目录） |
| `--paths-file /out/must.txt` | 额外必测的曲目：把相对路径逐行写进 `./p0-out/must.txt` |
| `--similar 20` / `--similar-include 'xxx'` | 相似度抽查的数量 / 查询范围 |
| `--no-musicnn` | 不算唤醒度/效价（musicnn 每个 patch 的开销约为 effnet 的 5 倍），用来对比速度 |
| `--backend openvino` | 强制使用 OpenVINO（用来和 ONNX Runtime 对比速度） |
| `--fresh` | 丢弃已有结果，重新开始 |

### 对比测试（可选，最好分开输出目录）
```sh
# OpenVINO CPU，测 30 首，对比速度
OUT=./p0-out-ov SKIP_BUILD=1 MUSIC=/path/to/music ./run-p0.sh --sample 30 --backend openvino
# 核显 UHD 600（需要重新构建，加入 OpenCL 运行时）
INSTALL_GPU=1 USE_GPU=1 OUT=./p0-out-gpu MUSIC=/path/to/music ./run-p0.sh --sample 30 --backend openvino-gpu
```

## 通过代理构建（QNAP / NAS）

### 为什么 daemon 配置了代理仍然不够
`docker info` 里显示的 `HTTP Proxy / HTTPS Proxy` 是 **Docker daemon** 的代理，只用于拉取镜像（所以 `python:3.12-slim-bookworm` 能拉下来）。构建时 `RUN apt-get update`、`pip install` 运行在构建容器里，**不会自动使用 daemon 的代理**，所以会卡在访问 `deb.debian.org`。必须显式把代理传进构建过程。

本工具的做法：
- `run-p0.sh` 只从**环境变量**读取代理，仓库里不写任何代理地址，脚本也不会打印代理地址。
- **普通代理**（不含账号密码）：使用 Docker 的**预定义代理构建参数**（`HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` 及小写形式）。Dockerfile 中刻意**不声明**这些 `ARG`，因此它们不会写入 `docker history`；脚本只传参数名（`--build-arg HTTP_PROXY`），值由 docker CLI 从环境读取，在 `ps` 中也看不到。经典构建器和 BuildKit 都支持。
- **含凭证的代理**（`http://用户:密码@主机:端口`）：自动改用 **BuildKit secret**（`Dockerfile.buildkit`）。代理只在安装依赖的那一步 `RUN` 中以文件形式挂载，不进入镜像层、镜像历史或构建参数（BuildKit 的构建记录会保存构建参数，所以凭证不走构建参数）。
- 代理只导出到安装脚本 `build/install-deps.sh` 的进程环境中，不写 `apt.conf`、`pip.conf`，也不用 `ENV`。apt 设置了 30 秒超时加 3 次重试，网络不通时会尽快报错，而不是长时间卡住。
- 运行阶段只有首次下载模型需要联网：模型未缓存时才把代理传给容器。普通代理以 `-e HTTPS_PROXY`（只传名字）传入；含凭证的代理以只读文件挂载到 `/run/secrets/proxy_url`，不进入容器环境变量。
- 未设置代理时，构建方式与之前完全相同。

### 方式一：普通代理（推荐，例如局域网里的 Clash / mihomo）
在 QNAP 上用 SSH 登录后，在**当前 shell** 中设置环境变量再运行（把地址换成你自己的代理，不要写进仓库文件）：
```sh
export HTTP_PROXY=http://<代理主机IP>:<端口>
export HTTPS_PROXY=$HTTP_PROXY
export NO_PROXY=localhost,127.0.0.1,192.168.0.0/16,10.0.0.0/8
MUSIC=/path/to/music ./run-p0.sh
```
注意：
- **不要用 `127.0.0.1` 或 `localhost` 作为代理地址**。构建容器里的 127.0.0.1 指的是容器自己，必须用代理机器在局域网中的 IP。
- 代理软件要**允许局域网连接**（Clash 的 `allow-lan: true` /「允许局域网」），否则 NAS 上的容器连不上。
- 使用 `sudo` 时环境变量默认会被丢弃，请用 `sudo -E` 或直接用 admin 运行。

### 方式二：含账号密码的代理（BuildKit secret）
把代理 URL 写进一个仅自己可读的文件（**不要放在仓库目录里**）：
```sh
umask 077
printf '%s\n' 'http://用户:密码@<代理主机IP>:<端口>' > ~/.p0-proxy
PROXY_FILE=~/.p0-proxy MUSIC=/path/to/music ./run-p0.sh
```
如果直接通过 `HTTPS_PROXY` 传入含 `@` 的 URL，脚本也会自动切换到 secret 模式：它会写一个权限为 600 的临时文件，运行结束后自动删除。
secret 模式需要 BuildKit：Docker 23+ 需要安装 buildx 组件（用 `docker buildx version` 检查），Docker 20.10 会由脚本自动设置 `DOCKER_BUILDKIT=1`。

### 可选开关
| 变量 | 作用 |
|---|---|
| `PROXY_MODE=auto\|args\|secret\|off` | 默认 `auto`。`off` 表示即使环境里有代理也不使用 |
| `RUNTIME_PROXY=auto\|1\|0` | 运行阶段下载模型时是否用代理，默认模型未缓存时使用 |
| `BUILD_NO_CACHE=1` | 强制重新执行安装步骤（验证代理时使用） |
| `APT_FORCE_IPV4=1` | apt 只用 IPv4。NAS 的 IPv6 不通时，直连也容易长时间卡住 |
| `APT_MIRROR=https://mirrors.tuna.tsinghua.edu.cn` | 改用 Debian 镜像源（不走代理时的替代方案） |
| `PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple` | 改用 PyPI 镜像源 |
| `BUILD_NETWORK=host` | 构建容器使用宿主机网络（bridge 网络的 DNS/路由有问题时使用） |

`APT_MIRROR`、`PIP_INDEX_URL` 等是普通构建参数，**会写进镜像历史**，不要在里面放凭证。

> 不建议为此修改 `~/.docker/config.json` 的 `proxies`。它确实会自动注入构建参数，但同时也会把代理注入之后用这个 CLI 创建的**所有容器**（包括重建的 032music-server）。也不要为此修改 032 的容器或配置。

### 检查代理是否真的被使用
1. **先确认容器网络能连上代理**（只传变量名，地址不会出现在命令行里）：
   ```sh
   docker run --rm -e HTTP_PROXY -e HTTPS_PROXY python:3.12-slim-bookworm python -c "import urllib.request as u; print('apt', u.urlopen('http://deb.debian.org/debian/dists/bookworm/InRelease', timeout=20).status); print('pypi', u.urlopen('https://pypi.org/simple/onnxruntime/', timeout=20).status)"
   ```
   两行都输出 `200` 说明代理可用。报 `Connection refused` 或超时时，检查代理地址是不是局域网 IP、是否允许局域网连接。
2. **强制重新构建并看日志**：
   ```sh
   BUILD_NO_CACHE=1 MUSIC=/path/to/music ./run-p0.sh --sample 1
   ```
   构建输出中应该出现 `[build] 代理：已启用（来源：构建参数；地址不打印）`（secret 模式下显示「来源：BuildKit secret」），然后是 `apt-get update` 的 `Get:` / `Hit:` 进度。如果显示「代理：未启用」，说明环境变量没有传到脚本（常见原因是用了 `sudo` 却没加 `-E`）。BuildKit 默认会折叠日志，可以先 `export BUILDKIT_PROGRESS=plain` 再运行。
3. **在代理端确认**：构建期间，Clash / mihomo 的连接日志里应该能看到 `deb.debian.org`、`pypi.org`、`files.pythonhosted.org`；首次运行时还应该有 `essentia.upf.edu`。
4. **确认没有泄露到镜像**（两条命令都应该没有任何输出；把 `<代理主机IP>` 换成实际 IP 的一部分）：
   ```sh
   docker history --no-trunc 032music-audio-p0 | grep -iE 'proxy|<代理主机IP>'
   docker image inspect 032music-audio-p0 | grep -iE 'proxy|<代理主机IP>'
   ```

### 常见问题
| 现象 | 可能原因 / 处理 |
|---|---|
| 「代理：未启用」 | 当前 shell 没有 `export` 代理变量，或者 `sudo` 丢弃了环境变量 |
| `Could not connect to <IP>:<端口>` / `Connection refused` | 代理没有允许局域网连接，或者用了 127.0.0.1 |
| 一直卡在 `Connecting to deb.debian.org` 然后超时 | 代理没有传进构建（按上面第 2 步确认）；直连时可以试 `APT_FORCE_IPV4=1` 或 `APT_MIRROR` |
| `407 Proxy Authentication Required` | 代理需要账号密码，改用方式二 |
| secret 模式提示需要 BuildKit / buildx | 安装 buildx 组件；或者改用不含凭证的代理走方式一 |
| pip 下载很慢或超时 | 已开启 60 秒超时加 5 次重试；也可以设置 `PIP_INDEX_URL` 使用镜像源 |

脚本自带离线测试（不需要 Docker，也不联网）：`sh tests/test_build_scripts.sh`

## 需要发回的内容
- `p0-out/summary.md`、`summary.json`：环境、后端探测结果、耗时、全库预估
- `p0-out/results.csv`：逐首结果（可以用 Excel 打开），**请标出明显不对的行**
- `p0-out/similarity.md`：相似度抽查，**请挑几组试听**
- `results.jsonl` 包含 embedding，体积较大，不用发

如果容器直接退出，请把终端里的最后 30 行发回（最关键的是 `[backend] probe ...` 这几行）。

## 输出字段说明
- **推断情绪 / 推断风格**：P0 阶段的临时映射，只用了声学信息、按样本内的相对强度计算。正式版本还会结合歌词和元数据，由 LLM 做融合判定。"[负面]"开头的是内部负面标记，不会作为歌单主题。
- **能量 1–5**：根据唤醒度，结合可舞性估算。
- **效价 / 唤醒度**：DEAM 模型输出，量程 1–9（效价高 = 正面，唤醒度高 = 激烈）。
- **Discogs 风格 Top5 / MoodTheme Top5 / 乐器 Top3**：模型原始输出，用来判断映射对不对。
