# Tasks

## 1. LRC 稳定排序

- [x] 1.1 在 `internal/lyrics/parser.go` 将 `ParseLRC` 的时间戳排序从 `sort.Slice` 改为 `sort.SliceStable`，并在 `internal/lyrics/parser_test.go` 新增同时间戳多行保持原序的单元测试，运行 `go test ./internal/lyrics -v` 验证通过

## 2. 在线歌词译文时间戳对齐修复

- [x] 2.1 在 `internal/lyricsonline/translation.go` 重构 `AlignTranslation`：过滤 `tlyric` 空白文本行、跳过原文空白行、将容差设为 `250ms` 并采用“双向邻近原文行归属保护 + 窗口内最近时间戳 + 单调游标一次消费”算法，保持原有函数签名不变
- [x] 2.2 在 `internal/lyricsonline/online_test.go` 扩展 `TestAlignTranslation` 覆盖全部规格场景（1ms 清屏空行前置、译文不含空行、±200ms 漂移对齐、未译行不抢占下一行译文、多余残留译文不泄漏到后续未译行、窗口内精确匹配优先于更早候选、紧邻多行各自对齐、同时间戳双行一对一及单译文不重复），运行 `go test ./internal/lyricsonline -run TestAlignTranslation -v` 验证通过

## 3. HTTP 翻译入口未同步保护与端到端回归测试

- [x] 3.1 在 `internal/http/lyrics_translation.go` 为 `AlignTranslation` 调用路径增加 `resolved.Result.Synced` 守卫，防止无时间戳纯文本原文被误对齐到 `tlyric` 的 `0ms` 行
- [x] 3.2 在 `internal/http/online_lyrics_test.go` 补充 HTTP 端到端测试：验证含 1ms 清屏空行的网易云歌词通过 `/api/v1/tracks/{id}/lyrics/translation` 返回完整对齐译文，以及原文无时间戳（`synced=false`）但含同步 `tlyric` 时跳过源码对齐并回退到 AI/未开启路径，运行 `go test ./internal/http -run "TestOnlineLyrics|TestLyricsTranslation" -v` 验证通过

## 4. 全量回归验证

- [x] 4.1 运行 `go test ./internal/lyrics ./internal/lyricsonline ./internal/http` 与 `go vet ./internal/lyrics ./internal/lyricsonline ./internal/http`，确认所有新增与既有测试全部通过且无静态检查告警

## Workflow follow-up

- 全部任务实现并验证后，使用 `openspec-verify-change` 核对实现与规范一致性。
- 审查通过后执行 `openspec-archive-change` 归档变更并同步主 specs。
