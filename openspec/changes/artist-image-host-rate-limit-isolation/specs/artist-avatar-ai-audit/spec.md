# Spec Delta

## ADDED Requirements

### Requirement: 歌手图片下载按域名隔离限流且第三方与多源图床 429 不阻塞整轮任务
系统 SHALL 按目标图片 URL 的规范化域名隔离歌手图片下载冷却状态（`artist image:<host>`），避免单一图床限流牵连其他无关域名；在 Tier 3 第三方联网图下载以及多源补全、全量审查重抓与自动入库补图场景中，单张图片命中 HTTP `429` 或携带 `Retry-After` 的 `503` SHALL 仅对该域名记短冷却并立即顺延尝试下一候选或下一图源，不得阻塞整轮批量任务或写入长期负缓存。

#### Scenario: 单一图床域名限流不影响其他域名的图片下载
- **WHEN** 某位艺人从域名 A（如 `upload.wikimedia.org`）下载头像返回 HTTP `429`（或带 `Retry-After` 的 `503`），系统随后需要从域名 B（如 `e-cdns-images.dzcdn.net` 或 `lain.bgm.tv`）下载头像
- **THEN** 仅 `artist image:<域名A>` 进入冷却状态，域名 B 的图片下载正常执行而不触发限流等待

#### Scenario: Tier 3 第三方联网图返回 429 时直接跳过而不拖停整轮任务
- **WHEN** 全量补全、全量 AI 审查替换、手动找图队列或自动入库队列通过联网搜索选中的第三方网页图片返回 HTTP `429`（或带 `Retry-After` 的 `503`），或目标域名已处于冷却中
- **THEN** 系统记录该图片域名的独立冷却状态并跳过该候选图片，绝不向上层任务返回全局 `RateLimitError`，不触发整轮任务退避等待或自动暂停；且在联网选图阶段自动过滤掉已处于冷却域名的候选链接

#### Scenario: 多源模式与全量审查重抓中某结构化图床 429 立即顺延下一图源且不写长期负缓存
- **WHEN** `all`、`web_fallback`、`rescan_all`、`audit_all`、`audit_force`、`auto_ingest` 或高置信判错后的多源重抓路径在 Tier 1 / Tier 2 下载某域名图片命中 `429`（或该域名处于冷却中）
- **THEN** 系统记录该域名冷却并立即顺延尝试同级其他图源及后续层级（如从 Wikimedia 顺延至 Deezer、Bangumi 或联网兜底）；若全源最终仍未拿到头像，因存在瞬时限流而不向 `artist_image_attempts` 写入长期负缓存冷却，后续重扫仍可重试

#### Scenario: 纯已匹配单源模式与共享上游 API 保留既有任务级限流退避与自动恢复
- **WHEN** `matched` 单源确认资料模式下载唯一确认资料图命中 `429`，或任意模式调用全局共享上游 API（MusicBrainz、Last.fm、Bangumi API、Deezer API、Fanart API、AI 模型服务、Tavily/DeepSeek 搜索接口）命中限流
- **THEN** 系统保留既有的任务级限流退避等待、累计等待持久化、3 轮自动恢复及预算耗尽自动暂停机制，确保上游共享接口不被持续压垮

### Requirement: 管理后台任务卡以中文展示限流来源并区分活跃限流与历史累计等待
管理后台活动任务卡片（SSR 首次渲染与前端轮询刷新）SHALL 将内部限流来源键映射为可读中文标签（含带域名的图片源标签），仅在存在未过期等待截止时间时以警告样式显示当前限流状态；当限流等待已结束、任务恢复正常运行或处于手动暂停时，SHALL 降级为低干扰中性提示展示历史累计等待。

#### Scenario: 内部英文限流来源键在页面与轮询更新中统一映射为中文标签
- **WHEN** 任务持久化记录中的 `wait_source` 为 `artist image`、`artist image:<host>`、`ai`、`tavily`、`deepseek`、`musicbrainz`、`lastfm`、`bangumi` 等内部标识
- **THEN** 服务端模板渲染与前端轮询更新均将其展示为对应中文标签（例如 `图片源`、`图片源（<host>）`、`AI 模型服务`、`Tavily 联网`），不在界面直接暴露原始英文 `artist image`

#### Scenario: 活跃限流等待与历史累计等待在文案和视觉样式上清晰区分
- **WHEN** 某活动任务存在未过期的 `waiting_until` 倒计时
- **THEN** 任务卡以警告样式展示「限流 <中文来源> · 等待 MM:SS（累计等待 …）」或「将于 … 自动继续」；当 `waiting_until` 已过期且任务正在正常运行或处于手动暂停时，卡片不再以警告样式宣称当前处于限流中，而是以低干扰中性样式展示「历史限流（<中文来源>）累计等待 …」
