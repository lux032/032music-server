# Proposal: 修复在线歌词自带翻译时间戳对齐漏译与稳定性

## Why

使用网易云在线歌词及其自带简体中文翻译（`tlyric`）时，部分正文歌词会出现无翻译的现象（例如《わたしたちのオカルト》整首 36 句正文中有 7 句翻译缺失）。根因在于网易云 LRC/`tlyric` 广泛使用提前 1ms 的空文本时间行作为换段清屏标记（如 `[00:38.523]` 空行紧接 `[00:38.524]` 正文），而后端 `AlignTranslation` 未过滤翻译中的空行且命中 ±100ms 容差内第一条后不推进游标，导致正文命中 1ms 前的空行并丢弃精确匹配的译文；同时 `ParseLRC` 对同时间戳歌词使用不稳定排序，可能打乱多行并列歌词与译文的对应顺序。

## What Changes

- **修复 `AlignTranslation` 时间戳对齐算法（`internal/lyricsonline/translation.go`）**：
  - 对齐前过滤 `tlyric` 中的空白文本行，避免清屏空行截获紧随其后的正文歌词。
  - 原文中的空白文本行不参与翻译消耗，确保空行输出保持为空字符串。
  - 在容差窗口内采用“最近时间戳 + 邻近原文行归属保护 + 单调消费游标”匹配，支持适度时间漂移（放宽至 250ms 且防止跨行抢占邻近原文的译文）以及同时间戳/紧邻多行歌词的逐行对齐。
- **保护未同步原文不被误对齐（`internal/http/lyrics_translation.go`）**：
  - 仅在解析后的原文歌词具备时间同步（`resolved.Result.Synced == true`）时执行 `AlignTranslation`，避免无时间戳纯文本（全为 `0ms`）误匹配 `tlyric` 的 `0ms` 行。
- **保证同时间戳 LRC 行的解析稳定性（`internal/lyrics/parser.go`）**：
  - 将 `ParseLRC` 的时间戳排序从 `sort.Slice` 改为 `sort.SliceStable`，保留相同时间戳多行歌词在原始文件与译文文件中的相对顺序。
- **补充回归测试与 HTTP 端到端验证**：
  - 在 `internal/lyrics`、`internal/lyricsonline` 与 `internal/http` 补充覆盖 1ms 清屏空行、同时间戳双行、轻微时间偏移、未译行不跨行抢占以及无时间戳回退的自动化测试。

## Capabilities

### New Capabilities
- `online-lyrics-translation-alignment`: 定义在线歌词自带时间同步译文（如网易云 `tlyric`）与原文歌词行的时间戳对齐行为、清屏空行过滤、同时间戳稳定排序与边界防错配规则。

### Modified Capabilities
<!-- 无既有 capability 需求变更。 -->

## Impact

- **受影响代码**：
  - `internal/lyrics/parser.go`（`ParseLRC` 稳定排序）及 `internal/lyrics/parser_test.go`
  - `internal/lyricsonline/translation.go`（`AlignTranslation` 对齐算法）及 `internal/lyricsonline/online_test.go`
  - `internal/http/lyrics_translation.go`（`Synced` 守卫）及 `internal/http/online_lyrics_test.go`
- **API 与数据兼容性**：
  - 对外 HTTP 接口路径与响应结构不变（仍返回与服务端原文 `lines` 等长的 `lines` 字符串数组）。
  - 无需数据库迁移，也无需清理既有 `online_lyrics` 缓存——表内已存完整原始 `translation_lyrics`，后端重启后实时对齐即可生效。
  - 客户端（`MusicBridge`）无需改动。
