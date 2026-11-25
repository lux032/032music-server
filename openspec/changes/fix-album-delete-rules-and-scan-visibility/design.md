# 技术设计（Design）

## Context

问题背景与线上排查过程见 `proposal.md`。

当前实现现状：
- `Store.DeleteAlbums`（`internal/storage/album_merge.go`）在删除专辑记录时会向 `album_key_rules` 写入 `(library_id, grouping_key, action='delete', source_title)`，并删除对应的 `audio_files`、`tracks` 与 `albums` 行。
- `importTrackTx`（`internal/storage/library.go`）按 `groupDir := albumFolder(input.RelativePath)` 和 `groupKey := metadata.Normalize(groupDir) + "|" + metadata.Normalize(m.Album) + "|" + strconv.Itoa(m.Year)` 计算专辑分组键；命中 `action == "delete"` 时直接返回 `nil`，不写入任何记录。
- `Manager.runImportPipeline`（`internal/scanner/scanner.go`）把 `ImportTrackBatch` 返回的每个 `errs[i] == nil` 都当作成功入库递增 `*processed++`，导致被删除规则跳过的文件在每次增量扫描中都被错误显示为「已处理」，并因 `processed > 0` 在零真实变更的增量扫描中也反复触发 `CleanupOrphans`。
- 当前没有针对 `album_key_rules` 的查询/删除存储接口、HTTP 路由或后台页面。

全部写操作需遵守既有 `write-lock-and-cache-resilience` 规范（通过 `Store.beginWrite(ctx)` 前置获取 SQLite 写锁）。

## Goals / Non-Goals

**目标（Goals）：**
- 在未取消且通过 S1 挂载保护的正常扫描结束后，自动清理磁盘目录已无任何受支持音频文件的僵尸 `delete` 规则。
- 在管理后台新增专辑规则管理页（`/admin/library/rules`），支持查看、单条与批量解除 `delete`/`merge` 规则，并可选择解除后立即触发增量扫描。
- 修正扫描记账逻辑，将被 `delete` 规则屏蔽的文件准确计入 `skipped` 与 `scan_jobs.rule_skipped_files`（不再计入 `processed`），保持零真实变更跳过 `CleanupOrphans`，并在日志、`/admin` 控制台与 `/admin/status` 轮询中明确提示。

**非目标（Non-Goals）：**
- 不修改现有 `grouping_key`（`目录|专辑名|年份`）的生成公式，也不迁移历史 `albums.grouping_key`。
- 不改变 `MergeAlbums` 的合并行为；解除 `merge` 规则时不会自动拆分目标专辑里已经合并的曲目。
- 不做按文件指纹在目录未空情况下自动失效 `delete` 规则的机制（原地替换新文件场景通过控制台规则跳过提示 + `/admin/library/rules` 一键解除覆盖）。
- 不在本变更中扩展普通 `scan_errors` 的完整失败明细查看页，保持变更聚焦。

## Decisions

### 1. 增量扩展 `scan_jobs.rule_skipped_files` 列（Migration 089）
- **决策**：新增 `internal/storage/migrations/089_scan_rule_skipped.sql`，仅包含单条增量语句：
  ```sql
  ALTER TABLE scan_jobs ADD COLUMN rule_skipped_files INTEGER NOT NULL DEFAULT 0 CHECK (rule_skipped_files >= 0);
  ```
  - 在 `storage.ScanJob` 增加 `RuleSkippedFiles int64 \`json:"ruleSkippedFiles"\``。
  - 保留既有 `Store.UpdateScanJob(ctx, id, processed, skipped, failed, current)` 签名兼容现有测试，新增 `Store.UpdateScanJobProgress(ctx, id, processed, skipped, ruleSkipped, failed, current)`（`UpdateScanJob` 委托调用并传 `ruleSkipped = 0`）。
  - 更新 `LatestScanJob` 查询并扫描 `rule_skipped_files`。
- **理由**：持久化到 `scan_jobs` 后，无论是首次打开 `/admin`、刷新页面、重启容器还是前端轮询 `/admin/status`，都能稳定显示规则跳过计数；同时 `rule_skipped_files` 作为 `skipped_files` 的子集，完整保留 `processed + skipped + failed == discovered` 与进度条语义。
- **备选方案**：仅在 `scanner.Manager` 内存中保存上次扫描的 `ruleSkipped` —— 放弃，重启后会丢失且与数据库扫描记录不一致。

### 2. 批次导入显式区分「真正入库」与「规则跳过」（`ImportResult` + `ImportTrackBatchDetailed`）
- **决策**：
  - 将 `importTrackTx` 返回值调整为 `(ruleSkipped bool, err error)`，命中 `hasRule && rule.action == "delete"` 时返回 `(true, nil)`。
  - 新增 `type ImportResult struct { RuleSkipped bool; Err error }` 与 `Store.ImportTrackBatchDetailed(ctx, inputs) ([]ImportResult, error)`。
  - 保留 `Store.ImportTrackBatch(ctx, inputs) ([]error, error)` 与 `Store.ImportTrack(ctx, input) error` 作为薄封装，保持所有既有调用方与测试兼容（`ImportTrack` 对已删专辑仍返回 `nil`）。
  - 在 `scanner.runImportPipeline` 中：
    - `res.Err != nil`：递增 `failed` 并记录 `scan_errors`；
    - `res.RuleSkipped`：同时递增 `skipped` 与 `ruleSkipped`，**不递增 `processed`，不加入 `seenRel`**；
    - 其他情况：递增 `processed` 并标记 `seenRel[item.rel]`。
  - 在 `scanner.run` 中：扫描完成日志增加 `ruleSkipped` 字段；`CleanupOrphans` 继续以 `processed > 0 || missing > 0 || purgedFiles > 0` 为条件，从而在仅有规则跳过、无真实变更时自动跳过孤儿清理。
- **理由**：避免用错误值承载正常的规则跳过分支（不触发多余的 `ROLLBACK TO import_track` 或错误日志），同时彻底消除规则跳过虚增 `processed` 与破坏零变更性能的问题。
- **备选方案**：让 `ImportTrack` 返回哨兵错误 `ErrAlbumRuleDeleted` —— 放弃，会破坏既有 `ImportTrack` 调用方与 `TestDeleteAlbumsSurvivesRescan` 对返回 `nil` 的语义约定。

### 3. 扫描后安全自动清理僵尸 `delete` 规则（`PruneStaleDeleteAlbumRules`）
- **决策**：
  - 扩展 `internal/scanner/scanner.go` 的 `discoveredLibrary`，记录遍历中因错误跳过的不可读目录相对路径列表 `unreadableDirs []string`（经 `metadata.Normalize` 归一化）。
  - 导出 `storage.AlbumFolder`（或共享归一化辅助函数），保证扫描器与入库逻辑共用同一套多碟子目录（`CD1`、`Disc 2` 等）归并规则，并基于 `discovered.files` 构建 `discoveredAlbumDirs map[string]struct{}`。
  - 仅在以下条件**全部满足**时执行 `Store.PruneStaleDeleteAlbumRules(ctx, libraryID, discoveredAlbumDirs, unreadableDirs)`：
    1. 本次扫描未被取消（`!cancelled`）；
    2. `len(discovered.files) > 0`；
    3. 空发现守卫与 >50% 缺失比例（S1）守卫均已通过且 `MarkMissingByIDs` 执行成功。
  - 判定规则：
    1. 先在写事务外查询 `SELECT grouping_key FROM album_key_rules WHERE library_id=? AND action='delete'`；若无记录直接返回 `(0, nil)`，零写锁开销。
    2. 对每条 `delete` 规则的 `key`：
       - 快速路径取 `ruleDir, _, _ := strings.Cut(key, "|")`，若 `ruleDir` 存在于 `discoveredAlbumDirs` 则保留；
       - 兜底路径（仅当目录或专辑名本身含 `|` 即 `strings.Count(key, "|") > 2` 时）：若 `discoveredAlbumDirs` 中任意目录 `d` 满足 `strings.HasPrefix(key, d+"|")` 则保留；
       - 若 `unreadableDirs` 包含 `"."`，或存在任意不可读目录 `u` 满足 `ruleDir == u || strings.HasPrefix(ruleDir, u+"/")` 则保留；
       - 其余判定为僵尸规则。
    3. 仅当存在待清理的僵尸规则时，通过 `s.beginWrite(ctx)` 前置获取写锁并分批删除这些 `action='delete'` 规则，在扫描日志中以 `INFO` 记录清理数量。
- **理由**：直接消除“旧目录改名/删除后残留永久黑名单坑掉未来同名新专辑”的隐患，同时严格保证掉盘、取消扫描、权限不足目录与多碟子目录绝不会误删有效屏蔽规则，且在无过期规则时不产生任何数据库写入。
- **备选方案**：在删除专辑时额外建表保存文件指纹并在目录内文件变化时自动解禁 —— 放弃，引入额外表结构且在外部标签工具批量修改文件时间戳时可能导致用户明确想屏蔽的专辑意外复活。

### 4. 管理后台「专辑屏蔽与合并规则」页（`/admin/library/rules`）与控制台集成
- **决策**：
  - 在 `internal/storage/album_merge.go` 新增：
    - `AlbumKeyRule` 结构体（`LibraryID`、`GroupingKey`、`Action`、`TargetAlbumID`、`TargetAlbumTitle`、`SourceTitle`、`CreatedAt`）与 `AlbumKeyRuleKey`；
    - `Store.ListAlbumKeyRules(ctx)`（`LEFT JOIN albums` 获取合并目标标题，按规则类型与创建时间倒序排列）；
    - `Store.CountAlbumKeyRules(ctx)` 返回 `(deleteCount, mergeCount int64, err error)` 供控制台概览展示；
    - `Store.DeleteAlbumKeyRules(ctx, keys []AlbumKeyRuleKey) (int64, error)`（通过 `beginWrite` 删除指定规则）。
  - 参照 `/admin/library/missing`（`internal/http/missing_files.go` 与 `missing-files.html`）的现有风格，新增 `internal/http/album_rules.go` 与 `internal/http/templates/album-rules.html`：
    - `GET /admin/library/rules`：分区或带标签展示全部屏蔽（`delete`）与合并（`merge`）规则，提供单条解除按钮、复选框批量解除表单，以及“解除并立即增量扫描”（`rescan=1`）操作。
    - `POST /admin/library/rules/delete`：强制管理员鉴权与 CSRF 校验，解析表单项 `rule`（格式 `<library_id>:<grouping_key>`，用 `strings.Cut(raw, ":")` 无歧义拆解），删除规则；若 `rescan == "1"` 则调用 `a.scanner.Start(r.Context(), "incremental")`（遇 `ErrScanRunning` 友好提示），完成后重定向回 `/admin/library/rules` 并展示结果提示。
  - 控制台与导航接入：
    - `dashboard.html`：在 `#scan-detail` 下方/右侧当 `RuleSkippedFiles > 0` 时展示“其中 N 个文件被已删除专辑规则跳过 · 查看规则 →”，并由 `admin.js` 在轮询 `/admin/status` 时同步刷新；在「缺失文件清理策略」行下方新增同级「专辑屏蔽与合并规则」摘要与入口链接。
    - `_layout.html`：在侧边栏「缺失文件与清理」旁补充「专辑规则」入口。
    - `browse.js`：更新删除专辑确认提示文案，明确告知“后续可随时在「专辑规则」中解除屏蔽并恢复入库”。
- **理由**：与既有缺失文件管理页交互完全一致（鉴权、CSRF、PRG 重定向提示、自适应轮询），学习成本最低。

## Risks / Trade-offs

- **[风险] 扫描期间某个目录因临时权限/IO 错误不可读，导致其下的有效 `delete` 规则被误判为空目录** → **缓解**：`discover()` 收集 `unreadableDirs`，命中不可读目录或其子路径的 `delete` 规则一律保留；扫描取消、发现数为 0 或触发 S1 保护时完全跳过清理。
- **[风险] 专辑目录名包含 `|` 字符导致按首个 `|` 截断失准** → **缓解**：常规路径 `O(1)` 查 `strings.Cut(key, "|")`，当 `strings.Count(key, "|") > 2` 时回退遍历 `discoveredAlbumDirs` 做 `d + "|"` 前缀匹配，宁可保守保留也不误删。
- **[风险] 管理员解除 `merge` 规则后误以为已合并曲目会立刻拆分** → **缓解**：在 `/admin/library/rules` 页面明确说明解除合并规则仅停止后续扫描自动重定向，不会删除目标专辑已有曲目，如需按原目录重新分拆可执行完整重扫。
- **[风险] 既有扫描或后台测试依赖 `UpdateScanJob` / `ImportTrackBatch` 签名或计数关系** → **缓解**：保持旧函数签名向后兼容、保持 `processed + skipped + failed == discovered`，并在 `storage`、`scanner`、`http` 三层补充覆盖测试。

## Migration Plan

1. 部署包含 Migration `089_scan_rule_skipped.sql` 的新版本，启动时 `Store.Migrate` 自动为 `scan_jobs` 追加 `rule_skipped_files` 列（纯元数据 `ALTER TABLE`，瞬间完成）。
2. 数据库中已有的 `album_key_rules` 规则保持安全不动：
   - 原目录已清空的过期 `delete` 规则将在下一次正常扫描结束时自动清理；
   - 原目录中已有新文件的规则（如 `ナナヲアカリ/ぴ`）会在控制台明确提示被跳过文件数，管理员点击进入 `/admin/library/rules` 一键「解除屏蔽并增量扫描」即可恢复入库。
3. 如需回滚，直接回退旧版本二进制即可，新增的 `rule_skipped_files` 列对旧版本无破坏性影响。
