# Tasks

## 1. 存储模型与迁移

- [x] 1.1 新增数据库迁移 `internal/storage/migrations/090_audio_loudness.sql`，创建 `audio_loudness`、`audio_loudness_failures` 与 `album_loudness` 表及必要索引，并通过 `go test ./internal/storage -run TestMigrate` 验证幂等迁移与外键级联删除。
- [x] 1.2 在 `internal/storage/loudness.go` 实现候选筛选（收藏/近期播放所在专辑优先，其余按 `album_id DESC, audio_file_id ASC` 专辑聚类批取，支持排除在途 ID、跳过最新指纹已分析与未到期退避记录）、单曲响度写入与失败退避写入、专辑时长加权能量均值聚合刷新与空专辑清理、进度统计及中位数查询，并补齐单元测试验证覆盖优先级顺序、指纹失效、静音/损坏退避与单曲删改后专辑重算（`go test ./internal/storage -run Loudness`）。
- [x] 1.3 扩展 `internal/storage/track_extras.go` 中的 `TrackExtras` 结构体（新增 `*float64` 指针字段 `LoudnessLUFS`、`TruePeakDB`、`LoudnessRange`、`AlbumLoudnessLUFS`、`AlbumTruePeakDB`）并在 `hydrateTrackExtras` 中通过主键 `LEFT JOIN` 批量回填，补齐单测验证 `Track` 与 `SyncTrack` 在已分析、真峰值为 `0.0` 及未分析三种情形下的 JSON 序列化结果（`go test ./internal/storage -run TrackExtras`）。

## 2. 后台 EBU R128 响度分析服务

- [x] 2.1 新增 `internal/loudness` 包，实现 `ffmpeg` `ebur128=peak=true` 滤镜探测、带 `-threads 1 -filter_threads 1` 约束的子进程执行与 `Summary` 段落解析（含非有限值 / `-inf` 纯静音拦截），并编写涵盖多版本 `ffmpeg` 输出格式、纯静音与异常解码场景的单元测试（`go test ./internal/loudness`）。
- [x] 2.2 实现低优先级受控并发后台 worker 池（`Service`：默认并发 `clamp(NumCPU/2, 1, 2)`，支持环境变量 `MUSIC_SERVER_LOUDNESS_WORKERS` 配置 `1-4`）：支持启动/取消等待、在途去重、扫描或实时转码进行中 `Busy` 自动让路、暂停/恢复持久化状态、失败重试与完成单曲后同步刷新专辑响度，并编写假测量器驱动的同步调度单元测试验证并发去重、让路、指纹增量复用与退避逻辑（`go test ./internal/loudness`）。
- [x] 2.3 在 `cmd/server/main.go` 装配 `loudness.Service`，接入与 `analysis.Service` 一致的 `Busy` 让路判定、扫描完成回调唤醒及清理失效专辑响度、优雅关闭 `Wait()`，并通过 `go test ./cmd/...` 与编译检查验证装配无回归。

## 3. HTTP API 与管理后台

- [x] 3.1 扩展 `internal/http/client_features.go` 中的 `handleCapabilities`，新增 `features.loudness` 能力位与顶层 `loudness` 统计对象（含带短 TTL 缓存的 `libraryMedianLufs`），并补齐 API 测试验证支持与不支持 `ffmpeg ebur128` 时的响应契约（`go test ./internal/http -run Capabilities`）。
- [x] 3.2 新增 `internal/http/loudness.go` 并在 `internal/http/app.go` 与管理页模板接入响度分析状态视图、状态 JSON 轮询接口、暂停/恢复与失败重试接口（带 CSRF 与管理员鉴权），补齐 HTTP 回归测试验证权限、暂停恢复及重试唤醒流程（`go test ./internal/http -run Loudness`）。
- [x] 3.3 更新服务端相关 API 文档（`docs/api/`）记录 `capabilities.loudness` 与 `TrackExtras` 新增响度字段，确认文档字段名、类型与单元测试一致。

## 4. 整库集成验证

- [x] 4.1 使用合成音频样本端到端验证扫描 -> 后台 EBU R128 测量 -> 专辑聚合 -> `/api/v1/capabilities`、`/api/v1/browse/tracks`、`/api/v1/albums/{id}`、`/api/v1/sync/tracks` 下发全链路，并运行全量 `go test ./...` 确认全部通过。
