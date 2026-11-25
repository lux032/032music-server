# Design

## Context

参见 `proposal.md` 了解需求动机。当前 `032music-server` 具备以下既有基础设施可直接复用：

- 运行环境与镜像（`Dockerfile`）已内置 `ffmpeg`（用于实时转码），`internal/http/transcode.go` 已有 `ffmpeg` 路径解析与能力探测缓存。
- `internal/analysis/service.go` 已有成熟的后台低优先级单工 worker 范式（通过 `Busy` 回调在扫描与转码时主动让路，通过 `Wake()` 在扫描完成与设置变更时唤醒）。
- `internal/storage/track_extras.go` 中的 `TrackExtras` 与 `hydrateTrackExtras` 被所有曲目读取接口（`Track`）及 Android 全量/增量同步接口（`SyncTrack`）共用。
- 最新数据库迁移为 `089_scan_rule_skipped.sql`。

## Goals / Non-Goals

**Goals:**

- 零新增外部运行时依赖，直接基于系统/镜像内置 `ffmpeg` 计算精确的 ITU-R BS.1770-4 / EBU R128 集成响度（LUFS）、4x 过采样真峰值（dBTP）与响度范围（LRA）。
- 后台分析不拖慢入库扫描，不与在线播放转码争抢 CPU/磁盘 I/O。
- 下发原始物理量（LUFS / dBTP）而非预扣固定目标的 ReplayGain dB，让桌面端与未来的 Android 端可自由实现高基准（如 -11 LUFS）与有界限幅策略。
- 列表、详情与同步查询的响度回填保持 O(1) 主键查找，无聚合查询退化。

**Non-Goals:**

- 不修改原始音频文件，不向音乐文件回写 `REPLAYGAIN_*` 标签。
- 不依赖独立的 Python 声学分析 sidecar（即使未部署 sidecar，响度分析也能开箱运行）。
- 不在服务端实时转码流中烘焙响度增益（响度均衡在客户端解码后执行，以保证用户可实时切换档位与开关）。
- 不调整 `apiRevision`（保持 `3`）。

## Decisions

### 决策 1：统一使用服务端 `ffmpeg ebur128=peak=true` 计算，不信任文件内嵌 ReplayGain 标签

- **选择**：后台调用 `ffmpeg -nostdin -hide_banner -threads 1 -filter_threads 1 -i <path> -map 0:a:0 -filter:a ebur128=peak=true -f null -`，解析 stderr 中 `Summary:` 段落的 `I:`（LUFS）、`LRA:`（LU）与 `Peak:`（dBFS / dBTP）。
- **理由**：不同打标工具写入的 ReplayGain 标签基准混乱（RG1.0 -18 LUFS + 样本峰值、RG2.0 真峰值、Opus R128 -23 LUFS），且大量曲目无标签。服务端统一用 EBU R128 测量可保证全库口径 100% 一致，同时一次拿到 4x 过采样 True Peak（对外置 DAC 采样间削波保护至关重要）；显式锁定 `-threads 1 -filter_threads 1` 防止单个 `ffmpeg` 子进程自动占满低功耗 NAS（如 4 核 J4125）全部线程。
- **替代方案**：在 Go 中引入 CGO 绑定 `libebur128` + 解码器（破坏 `CGO_ENABLED=0` 静态构建），或降级使用 `peak=sample`（可快约 3 倍，但会漏检现代高压缩母带的采样间真峰值过载；因响度扫描属一次性冷启动且后续秒级增量，保留 `peak=true` 换永久精度更划算）。

### 决策 2：独立物化表 `audio_loudness` + `album_loudness` + `audio_loudness_failures`

- **迁移 `090_audio_loudness.sql`**：
  ```sql
  CREATE TABLE IF NOT EXISTS audio_loudness (
      audio_file_id    INTEGER PRIMARY KEY REFERENCES audio_files(id) ON DELETE CASCADE,
      file_size        INTEGER NOT NULL,
      modified_at_ns   INTEGER NOT NULL,
      analyzer_version INTEGER NOT NULL DEFAULT 1,
      integrated_lufs  REAL    NOT NULL,
      true_peak_db     REAL    NOT NULL,
      loudness_range   REAL,
      measured_at      TEXT    NOT NULL
  );

  CREATE TABLE IF NOT EXISTS audio_loudness_failures (
      audio_file_id    INTEGER PRIMARY KEY REFERENCES audio_files(id) ON DELETE CASCADE,
      file_size        INTEGER NOT NULL,
      modified_at_ns   INTEGER NOT NULL,
      analyzer_version INTEGER NOT NULL DEFAULT 1,
      permanent        INTEGER NOT NULL DEFAULT 0 CHECK (permanent IN (0, 1)),
      attempts         INTEGER NOT NULL DEFAULT 1,
      next_retry_at    TEXT,
      last_error       TEXT    NOT NULL DEFAULT '',
      updated_at       TEXT    NOT NULL
  );

  CREATE TABLE IF NOT EXISTS album_loudness (
      album_id         INTEGER PRIMARY KEY REFERENCES albums(id) ON DELETE CASCADE,
      integrated_lufs  REAL    NOT NULL,
      true_peak_db     REAL    NOT NULL,
      track_count      INTEGER NOT NULL,
      updated_at       TEXT    NOT NULL
  );
  ```
- **理由**：
  - 与 `audio_files` 主键 1:1 绑定且 `ON DELETE CASCADE`，文件删除自动清理。
  - `album_loudness` 物化后，`hydrateTrackExtras` 只需对 `audio_loudness` 和 `album_loudness` 做两次主键 `LEFT JOIN`，对 500 条分页和万首 `SyncTrack` 几乎零额外耗时。
  - 候选查询遵循 `background-candidate-scheduling` 规范：按主键逆序扫描 `audio_files`（`status='available'`）早停，避免全表临时排序。

### 决策 3：专辑响度采用时长加权能量均值聚合（零二次解码）

- **公式**：对专辑内所有可用且已测量响度的曲目 $k$（有效权重 $w_k = \max(\text{duration\_ms}_k, 1000)$）：
  - `album_lufs` = $10 \cdot \log_{10}\left(\frac{\sum w_k \cdot 10^{I_k / 10}}{\sum w_k}\right)$
  - `album_true_peak_db` = $\max_k(\text{TP}_k)$
- **理由**：与将整张专辑拼接重扫的联合门控 EBU R128 相比，误差通常 < 0.3 LU（听感不可分辨），但无需二次读取整张专辑音频文件，且可处理专辑内不同采样率/声道数曲目混排，单曲增删后毫秒级刷新。
- **替代方案**：整张专辑用 `ffmpeg concat` 二次扫描。成本翻倍且对多采样率专辑需重采样，收益极低。

### 决策 4：API 契约使用指针 `*float64` + `omitempty`，并暴露曲库中位数

- **`TrackExtras` 增量字段**：
  ```go
  LoudnessLUFS      *float64 `json:"loudnessLufs,omitempty"`
  TruePeakDB        *float64 `json:"truePeakDb,omitempty"`
  LoudnessRange     *float64 `json:"loudnessRange,omitempty"`
  AlbumLoudnessLUFS *float64 `json:"albumLoudnessLufs,omitempty"`
  AlbumTruePeakDB   *float64 `json:"albumTruePeakDb,omitempty"`
  ```
- **`GET /api/v1/capabilities` 增量字段**：
  - `features["loudness"]`: `bool`（`loudnessService != nil && (loudnessService.Available() || stats.Analyzed > 0)`）
  - `"loudness"`: `{ "available": bool, "analyzedTracks": int64, "totalTracks": int64, "libraryMedianLufs": *float64 }`
- **曲库中位数计算**：使用带短 TTL（如 30s，写入新结果时失效）的内存缓存查询 `audio_loudness` 的中位数（`ORDER BY integrated_lufs LIMIT 1 OFFSET count/2`），避免每次 `/capabilities` 请求扫表。

### 决策 5：后台服务调度、可控并发与高频专辑优先策略

- **默认启用**：只要启动时探测到 `ffmpeg` 支持 `ebur128` 滤镜，后台响度分析默认开启（无需像外部 AI sidecar 那样手动配置地址），支持在管理页一键暂停/恢复与重试失败项；状态持久化在 `settings` 表（如 `loudness_paused`）。
- **并发与 CPU 上限控制**：
  - 并发 worker 数支持通过环境变量 `MUSIC_SERVER_LOUDNESS_WORKERS` 配置（范围 `1-4`），默认 `clamp(NumCPU/2, 1, 2)`（例如 4 核 J4125 默认 `2` 并发）。
  - 配合每进程 `-threads 1 -filter_threads 1`，2 并发在 J4125 + HDD 环境下将总 CPU 稳定控制在约 50%、HDD 读盘约 20 MB/s（2 万首一次性冷启动约 10-14 小时一晚完成），始终为 API、播放流与其他宿主机服务留出充足余量；扫描或实时转码进行中所有 worker 统一让路。
  - 单曲超时上限 5 分钟，退出时跟随 `rootCtx` 取消所有在途子进程。
- **两级候选优先级 + 专辑聚类批取**：
  - 为避免 2 万首大库冷启动时常用歌曲排在队尾，且减少机械硬盘跨目录寻道，候选按小批次（例如每批 16-32 首、跳过在途 `inFlight` 集合防止并发重复）按以下顺序拉取：
    1. **优先梯队**：已收藏专辑、含已收藏曲目或近期有播放记录（`playback_progress`）的专辑内所有待分析曲目（按最近播放/专辑聚类，确保高频聆听专辑在启动后几十分钟内率先全部完成）；
    2. **常规梯队**：其余曲库按 `(album_id DESC, audio_file_id ASC)` 聚类扫描，使同一专辑目录连续读取并在单张专辑扫完后立刻产出完整 `album_loudness`。
- **异常值守卫**：对 `math.IsNaN` / `math.IsInf` 或极端异常值（如 `I < -70 LUFS` 的纯静音轨）标记为永久失败（`permanent=1`，原因"静音或无有效响度"），绝不写入 `audio_loudness`。

## Risks / Trade-offs

- [部分自定义/精简宿主机未安装 `ffmpeg` 或缺少 `ebur128` 滤镜] -> 启动时复用探针检查 `ffmpeg -hide_banner -filters` 是否含 `ebur128`；缺失时优雅降级为不可用状态（`features.loudness=false`），不影响服务器其他功能。
- [低功耗 NAS + HDD 上 2 万首大库首次后台扫描需约 10-14 小时] -> 默认 2 并发且每进程 `-threads 1` 将 CPU 限在 ~50%、HDD 吞吐限在 ~20 MB/s；收藏与近期播放专辑优先分析让用户几十分钟内即可体验，扫描/转码时自动让路，完成后永久按指纹增量缓存。
- [单曲被删除或移出专辑时 `album_loudness` 可能残留过时值] -> 扫描完成回调中对涉及音频文件变更的专辑统一刷新 `album_loudness`，并定期清理无有效曲目的空专辑行。
- [`ffmpeg` 不同版本的 `ebur128` Summary 输出空格/对齐略有差异] -> 解析器基于行关键字与正则宽松匹配 `Summary:` 后的 `I:`、`LRA:`、`Peak:`（支持 `-inf` 识别），并配套多版本输出样例单元测试。

## Migration Plan

1. 部署新版服务端时自动执行幂等迁移 `090_audio_loudness.sql`，无锁表迁移风险，不修改既有表结构。
2. 服务启动后检测到 `ffmpeg ebur128` 可用即开始在后台低优先级渐进补全历史曲库，新入库曲目随扫描完成自动触发。
3. 回滚策略：旧版客户端完全忽略 `TrackExtras` 与 `capabilities` 新增 JSON 字段；服务端若回滚版本，新增三张表不干扰旧代码运行。
