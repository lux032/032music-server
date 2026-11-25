# Design: 在线歌词自带翻译时间戳对齐修复

## Context

参见 [proposal.md](proposal.md) 的背景与动机。

当前链路为：
1. `internal/lyrics/parser.go`：`ParseLRC` 将 LRC 文本解析为 `LyricsResult{Synced, Lines}`，保留带时间戳的空文本行（`Text == ""`），并使用 `sort.Slice` 按 `TimeMs` 排序。
2. `internal/http/lyrics_translation.go`：`handleAPILyricsTranslation` 在 `resolved.TranslationLRC != "" && lyricsonline.IsSimplifiedChinese(body.TargetLanguage)` 时实时调用 `lyricsonline.AlignTranslation(resolved.Result.Lines, resolved.TranslationLRC)`，若成功（`ok == true`）则直接返回与 `resolved.Result.Lines` 等长的 `[]string`。
3. `internal/lyricsonline/translation.go`：`AlignTranslation` 当前对原始 `tr.Lines`（未过滤空行）使用单游标 `j` 扫描 `tr.Lines[j].TimeMs < line.TimeMs - 100`，取第一个满足 `<= line.TimeMs + 100` 的元素且匹配后不递增 `j`。
4. 数据库 `online_lyrics.translation_lyrics` 存储的是原始 `tlyric` 字符串，未落库对齐结果；客户端按服务端 `lines` 下标对齐译文，两端接口契约无需变动。

## Goals / Non-Goals

**Goals:**
- 修复 `tlyric` 中 1ms 清屏空行截获紧邻正文歌词导致的漏译问题。
- 在保持 $O(N+M)$ 线性复杂度和零外部依赖的前提下，支持同时间戳多行、紧邻行以及最高 `±250ms` 轻微时间漂移的准确一对一对齐。
- 避免未译原文行抢占邻近原文的译文，以及多余残留译文泄漏到后续未译行。
- 保护无时间戳原文歌词不被误对齐到 `[00:00.00]` 译文行。

**Non-Goals:**
- 不改变 `online_lyrics` 数据库表结构、HTTP API schema 或客户端协议。
- 不对网易云本身缺失的译文行触发混合 AI 补译（若需要“残缺 `tlyric` 自动回退 AI 翻译”策略，另立变更）。
- 不修改 `ParseLRC` 保留空文本行的语义（客户端/播放同步仍可依赖空行清屏信息；`MusicBridge` 已通过 `serverIndices` 正确跳过空行）。

## Decisions

### Decision 1: 过滤译文空行 + 双向 Voronoi 归属约束 + 单调最近匹配游标

在 `AlignTranslation(lines []lyrics.LyricLine, translationLRC string)` 中：

1. **预处理非空集合**：
   - 解析 `tr := lyrics.ParseLRC(translationLRC)`；若 `!tr.Synced` 则返回 `nil, false`。
   - 过滤 `tr.Lines`，仅保留 `strings.TrimSpace(l.Text) != ""` 的项构成 `trLines`（保留顺序与 `TrimSpace` 后的文本）。若 `len(trLines) == 0`，返回 `nil, false`。
   - 预先收集 `lines` 中所有非空原文行的索引 `origIdx`（保持原索引用于写回 `out[idx]`），并据此计算每个非空原文行 $p$ 对应的：
     - `prevTime`：前一个时间严格更小（`TimeMs < curTime`）的非空原文行时间戳（若无则无下界邻近约束）；
     - `nextTime`：后一个时间严格更大（`TimeMs > curTime`）的非空原文行时间戳（若无则无上界邻近约束）。
   - 使用“严格小于 / 严格大于”而非直接取相邻元素，是为了让**同时间戳的多行原文共享同一个归属区间**，按出现顺序依次消费同时间戳译文。

2. **单调游标 + 窗口内最近选择**：
   - 设 `alignToleranceMs = 250`，初始化游标 `j = 0`。
   - 按顺序遍历每个非空原文行（时间 `t = lines[idx].TimeMs`）：
     - 先推进 `j`，跳过所有 `trLines[j].TimeMs < t - alignToleranceMs` 的过早译文。
     - 再从 `k = j` 向后扫描所有 `trLines[k].TimeMs <= t + alignToleranceMs` 的候选：
       - 计算 `dist := abs(trLines[k].TimeMs - t)`。
       - 归属保护：若存在 `prevTime` 且 `dist > abs(trLines[k].TimeMs - prevTime)`，跳过（该译文更靠近前一行）；若存在 `nextTime` 且 `dist > abs(trLines[k].TimeMs - nextTime)`，终止本行扫描（后续译文只会更大、更靠近下一行）。
       - 记录最小 `dist` 对应的首个候选 `bestIdx`（相等距离取更早的 `k`，保证同时间戳按顺序消费）。
     - 若找到 `bestIdx >= 0`：设置 `out[idx] = trLines[bestIdx].Text`，`ok = true`，并将 `j = bestIdx + 1`（消费该译文，避免被下一行重复使用）。

**替代方案对比**：
- *方案 A：仅在原循环中跳过空行并把 `j++`*。
  - 缺点：仍会命中窗口内第一个而非最近的候选，且一旦放宽容差（甚至在 100ms 内），未译行会抢占紧邻下一行的译文。
- *方案 B：全局动态规划 / 二分图最小权匹配*。
  - 缺点：对歌词单调时间序列属于过度设计，且可能把时间差很远的行硬配对。
- *采纳方案（过滤空行 + 双向最近归属 + 单调游标）*：实现简洁（约 45 行纯 Go），最坏时间复杂度仍接近 $O(N+M)$，且能精确处理全部边界场景。

### Decision 2: `alignToleranceMs` 从 `100ms` 放宽至 `250ms`

- 网易云部分老歌或分批上传的 `lrc`/`tlyric` 存在 100ms～250ms 的制作精度偏差（例如百分秒四舍五入或微调轴）。
- 过去因为没有邻近原文行归属保护，不敢放大容差；引入双向 Voronoi 归属保护后，即使两句原文间隔仅 50ms，归属边界也会自动收缩到中点（25ms），因此将上限提高到 `250ms` 既能覆盖常见漂移又不会造成快节奏歌词跨行错配。

### Decision 3: `ParseLRC` 改用 `sort.SliceStable`

- `internal/lyrics/parser.go` 当前使用 `sort.Slice`，Go 标准库不保证相等键元素的相对顺序。
- 改为 `sort.SliceStable` 代价极小（单首歌通常 30–150 行），即可保证同时间戳多行歌词在原文与译文中顺序一致，使 Decision 1 的同时间戳一一配对行为确定可靠。

### Decision 4: HTTP 翻译入口增加 `resolved.Result.Synced` 守卫

- `AlignTranslation` 的公开签名 `AlignTranslation(lines []lyrics.LyricLine, translationLRC string) ([]string, bool)` 保持不变，避免破坏调用方与测试兼容性。
- 在 `internal/http/lyrics_translation.go` 中将判断条件更新为 `resolved.Result.Synced && resolved.TranslationLRC != "" && lyricsonline.IsSimplifiedChinese(body.TargetLanguage)`，显式阻止无时间戳原文（所有 `TimeMs == 0`）落入时间戳对齐分支。

## Risks / Trade-offs

- **[Risk] 原文与译文存在超过 250ms 的系统性整体偏移（如整首差 1 秒）** → 本设计不尝试猜测全局时间偏移（容易误对齐），这类极端错轴属于上游歌词数据质量问题，用户可通过手动重新绑定其他歌词源或切换 AI 翻译解决。
- **[Risk] `tlyric` 某单行将两句同时间戳原文合并为一行译文** → 一对一消费会让第一句显示该合并译文、第二句留空，避免了把同一段译文重复显示两遍的视觉噪音。
- **[Risk] 既有缓存数据中部分歌曲在抓取时网易云尚未上架翻译（`translation_lyrics == ""`）** → 本修复无需清理已有含 `translation_lyrics` 的缓存；仅对当时确实未抓到翻译的个别曲目需在后台“在线歌词”中删除后重抓。

## Migration Plan

- 纯内存/算法逻辑修复，无数据库迁移、无配置变更。
- 部署新版本 `032music-server` 重启即可对所有已缓存的网易云歌词实时生效；如需回滚，直接回退二进制版本即可。
