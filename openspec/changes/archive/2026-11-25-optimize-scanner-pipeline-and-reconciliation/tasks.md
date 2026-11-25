# Tasks

## 1. 存储层扫描快照与批量导入事务（`internal/storage`）

- [x] 1.1 在 `internal/storage/library.go` 中新增 `ScanFileSnapshot`、`LoadScanFileSnapshot(ctx, libraryID)`、`BatchUpdateExternalLRC(ctx, updates)` 与 `MarkMissingByIDs(ctx, libraryID, ids)`（均通过 `s.beginWrite(ctx)` 预先获取写锁，分批 500 条更新并仅对受影响专辑刷新 `album_release_groups`）；补充单测验证快照字段完整性、空缺失列表零写事务、部分缺失仅刷新受影响专辑及外置 LRC 批量增量更新，运行 `go test -race -count=1 ./internal/storage -run 'TestScanSnapshot|TestBatchUpdateExternalLRC|TestMarkMissing'` 通过。
- [x] 1.2 在 `internal/storage/library.go` 与 `internal/storage/release_groups.go` 中实现 `ImportTrackBatch(ctx, inputs)`：使用 `s.beginWrite(ctx)` 预先获取写锁，单首曲目以 `SAVEPOINT import_track` 隔离（异常回滚不连坐同批其余曲目），仅在单首 `RELEASE SAVEPOINT` 成功后将新解析的规范艺人写入批内 `artistCache`，并将 `refreshAlbumFileReleaseGroups` 延后至批次末尾按受影响专辑去重执行一次；将 `ImportTrack` 简化为对单元素 `ImportTrackBatch` 的薄封装；补充单测验证同批单首失败隔离、艺人缓存回滚安全、跨专辑移动刷新及与播放事件并发写入无 `SQLITE_BUSY_SNAPSHOT`，运行 `go test -race -count=1 ./internal/storage -run 'TestImportTrack|TestReleaseGroup'` 通过。

## 2. 扫描收尾对账范围收窄与单遍查询优化（`internal/storage`）

- [x] 2.1 重构 `internal/storage/library.go` 的 `MarkMissing` 与 `internal/storage/missing.go` 的 `PurgeMissing`，改用 `s.beginWrite(ctx)` 并仅收集实际状态变更/被删除文件所属的去重 `album_id` 集合，仅对受影响专辑调用 `refreshAlbumFileReleaseGroups`（零变更时跳过全部刷新，保留 `RefreshReleaseGroupIdentities` 与 `finishReleaseGroupRebuild` 的全库重建）；补充单测验证零缺失时不触发 `album_release_groups` 重写、单专辑缺失/清理仅刷新对应专辑，运行 `go test -count=1 ./internal/storage -run 'TestMarkMissing|TestPurgeMissing|TestReleaseGroup'` 通过。
- [x] 2.2 优化 `internal/storage/missing.go` 的 `cleanupOrphansSQL`（消除按艺人逐行执行的 3 重相关 `json_each` 扫描）与 `internal/storage/album_works.go` 的 `RefreshAlbumWorks`（将初始专辑查询改写为单遍物化 CTE 消除每专辑 3 重相关子查询并改用 `s.beginWrite(ctx)` 写锁）；补充 `EXPLAIN QUERY PLAN` 与规则升级/孤儿清理单测，运行 `go test -count=1 ./internal/storage -run 'TestRefreshAlbumWorks|TestCleanupOrphans|TestAlbumWork|TestPrimaryArtistCorrection'` 通过。

## 3. 扫描器单遍发现、快照增量跳过与有界并发管线（`internal/scanner`）

- [x] 3.1 重构 `internal/scanner/scanner.go` 的 `discover()` 与增量路径：单遍 `WalkDir` 同时缓存音频文件的 `size`/`modTimeNS` 与目录内 `.lrc` 路径集合（仅当候选 `.lrc` 存在时才执行 `externalLRC` BOM 检查）；`run()` 启动时单次调用 `LoadScanFileSnapshot`，在内存中判定未变更、待补探针与 `.lrc` 变更，完全未变更文件零写库，仅批量提交 `.lrc` 差异与探针补全；改用 `MarkMissingByIDs` 标记缺失并在零变更扫描（`processed == 0 && missing == 0 && purged.Files == 0`）时跳过 `CleanupOrphans`；完整保留 S1 掉盘保护与取消安全；补充单测验证零变更增量扫描不触发 `audio_files` 写锁、缺失/LRC/探针补全与 S1 保护全通过，运行 `go test -race -count=1 ./internal/scanner -run 'TestScan|TestUnchanged|TestFailedAudioProbe|TestExternalLRC|TestCleanupOrphans'` 通过。
- [x] 3.2 在 `internal/scanner/scanner.go` 中实现有界并发读备工作池（默认 `min(max(NumCPU, 2), 4)`，支持单测配置 `workers`）与单写者有序批提交管线：worker 并行执行 `readMetadata`、`probe`、线程安全的 `folderArtwork` 缓存与按内容哈希去重的原子 `writeArtwork`，单写者按发现顺序重组并每批 50 首或 250ms 刷盘调用 `ImportTrackBatch` 并节流刷新 `scan_jobs` 进度；补充 `-race` 单测验证多协程并发封面去重、单首读取/导入失败隔离、取消安全、传统创作者拆分与身份重建兼容，运行 `go test -race -count=1 ./internal/scanner` 全部通过。

## 4. 性能基准与全链路回归验证

- [x] 4.1 新增 `internal/scanner/scanner_perf_test.go`（覆盖大批量文件全量导入与零变动增量重扫的吞吐耗时、零变更增量零写库断言及多核并发正确性），并运行全量回归套件 `go test -race -count=1 ./internal/scanner && go test -count=1 ./internal/storage ./internal/http ./internal/enrichment` 确认全部通过且相对串行单事务基线有显著加速。
