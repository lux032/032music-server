# Proposal

## Why

032 曲库涵盖现代流行 / ACG、老动画歌曲、OST 与大动态器乐，曲目间的原始集成响度差距常达 10-15 LUFS。目前服务端只探测编码格式、采样率与位深，不提供统一的响度与真峰值元数据，导致桌面端（`032player-rs`）与未来的移动端（`MusicBridge`）无法在不破坏曲内动态的前提下实现一致的响度均衡，更无法支持按专辑聚类的智能响度对齐与基于全库分布的合理参考基准。

## What Changes

- 新增 SQLite 迁移 `090_audio_loudness.sql`，新增 `audio_loudness`、`audio_loudness_failures` 与 `album_loudness` 表，以音频文件物理指纹缓存每曲 EBU R128 测量结果并物化专辑聚合响度。
- 新增后台低优先级响度分析服务 `internal/loudness`，直接复用容器与环境中已有的 `ffmpeg`（`ebur128=peak=true` 滤镜）逐曲计算集成响度（LUFS）、真峰值（dBTP）与响度范围（LRA），按专辑以时长加权能量均值聚合专辑响度与真峰值；扫描或实时转码进行中自动让路，不阻塞入库与播放。
- 扩展 `GET /api/v1/capabilities`：新增能力位 `features.loudness` 以及顶层可选统计对象 `loudness`（含 `available`、`analyzedTracks`、`totalTracks`、`libraryMedianLufs`）。
- 扩展 `storage.TrackExtras`（自动覆盖所有曲目列表、详情接口及 `/api/v1/sync/tracks`）：新增可选指针字段 `loudnessLufs`、`truePeakDb`、`loudnessRange`、`albumLoudnessLufs`、`albumTruePeakDb`（未分析曲目省略字段，不用 `0` 占位）。
- 新增管理后台状态与控制接口（查看分析进度、曲库中位数、失败列表，支持暂停/恢复与失败重试），接入现有管理页后台任务视图。

## Capabilities

### New Capabilities

- `audio-loudness-analysis`: 服务端 EBU R128 单曲与专辑响度后台分析、指纹增量缓存、退避失败管理、管理后台状态控制，以及通过 `capabilities` 和 `TrackExtras` 向全平台客户端下发原始响度物理量。

### Modified Capabilities

<!-- 无既有 spec 需求变更 -->

## Impact

- 后端存储：`internal/storage/migrations/090_audio_loudness.sql`、`internal/storage/loudness.go`、`internal/storage/track_extras.go`。
- 后台分析：`internal/loudness/`（新增）、`cmd/server/main.go`（装配服务、绑定扫描完成唤醒与退出等待）。
- HTTP 与管理页：`internal/http/client_features.go`、`internal/http/app.go`、`internal/http/loudness.go` 及对应模板。
- 运行依赖：无新增外部依赖，复用既有 `ffmpeg` 可执行文件及探针；`apiRevision` 保持 `3`，全部新增字段均向后兼容。
- 受益客户端：`032player-rs`（本次同步接入）与 `MusicBridge`（后续通过现有 `SyncTrack` 直接复用）。
