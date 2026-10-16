# 声学分析 P0 验证工具

用于在**实际运行 032 的 NAS**上验证声学分析是否可行，对应设计文档 `docs/daily-mix-design.md` 第 12、13 节。

要回答的问题：
1. 推理后端能不能在 Celeron J4125（**无 AVX**）上运行？ONNX Runtime 不行时会自动尝试 OpenVINO。
2. 每首需要多久？1.7 万首全量需要多久？
3. 模型给出的风格/情绪/乐器/人声，对你的曲库（尤其 ACG、纯音乐）准不准？
4. 声学相似度（"最像的 10 首"）听起来对不对？

## 安全性
- 音乐目录以**只读**方式挂载，不修改任何文件，也不连接、不修改 032 的数据库。
- 唯一的联网操作：首次运行时从 `essentia.upf.edu` 下载模型（约 53MB），缓存到 `./p0-models`。
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
