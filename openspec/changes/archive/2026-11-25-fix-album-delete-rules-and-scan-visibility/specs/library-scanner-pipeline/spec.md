# Spec Delta

## ADDED Requirements

### Requirement: 被删除规则屏蔽的文件按跳过记账并在扫描状态中明确提示
因专辑规则键命中活跃 `delete` 规则而被跳过的文件 SHALL NOT 计入 `processed`（已处理），也不得单独触发扫描后的孤儿清理。系统 SHALL 将每个被规则屏蔽的文件同时计入 `skipped` 与持久化的 `rule_skipped_files` 计数，在扫描完成日志中输出该计数，并在 `rule_skipped_files > 0` 时于控制台扫描状态中明确展示被屏蔽文件数及前往专辑规则管理页的直达链接。

#### Scenario: 规则屏蔽文件计入跳过与规则跳过计数而非已处理
- **WHEN** 增量或完整扫描遇到命中活跃 `delete` 规则的文件以及曲库中未变更的文件，且本次扫描没有新入库、标记缺失或永久清理的文件
- **THEN** 每个被规则屏蔽的文件同时递增 `skipped` 与 `rule_skipped_files`，`processed` 保持为 `0`，满足 `discovered == processed + skipped + failed`，且扫描结束后跳过孤儿清理

#### Scenario: 控制台在扫描期间与结束后展示规则跳过数量及跳转链接
- **WHEN** 最近一次扫描任务的 `rule_skipped_files > 0`
- **THEN** 服务端渲染的控制台页面与 `/admin/status` 轮询接口均返回 `ruleSkippedFiles`，并在页面上明确提示有多少文件被已删除专辑规则跳过以及跳转至专辑规则管理页的链接
