# Spec Delta

## ADDED Requirements

### Requirement: 全链路候选头像统一执行 VLM 视觉准入质检
当启用 VLM 视觉质检且全局多模态模型已配置时，系统 SHALL 对 Tier 1（MusicBrainz / Last.fm）、Tier 2（Fanart / Deezer / Bangumi）与 Tier 3（联网搜索 + AI 选图）获取的每一张缺图候选头像执行 VLM 视觉验收，覆盖新确认艺人的自动补图队列与手动补全任务的缺图路径；高置信合格图落库并写入已审通过记录，不合格图立即丢弃拉黑并顺延下一图源，中置信图送入待审头像队列。

#### Scenario: Tier 1 或 Tier 2 获取的专辑封面被 VLM 当场拦截并顺延至后续图源
- **WHEN** 缺图艺人在自动补图或手动补全任务中由 Deezer 返回一张专辑封面，且已启用 VLM 视觉质检
- **THEN** 系统在下载与基础尺寸校验后调用 VLM 验收并判定 `valid=false`，拒绝将其写入 `artist_image_cache`，将该图片 URL 与 `content_hash` 写入拒绝表，并继续尝试后续已启用图源与 Tier 3 联网兜底

#### Scenario: 结构化图源或联网图源拿到高置信合格人像时自动入库并免除后续重复审查
- **WHEN** Tier 1、Tier 2 或 Tier 3 下载的候选图通过 VLM 视觉验收且置信度达到 `audit_auto_confidence`（并启用高置信自动落库）
- **THEN** 系统在带写锁校验的事务内将该图写入 `artist_image_cache`，同步在 `artist_image_reviews` 记录当前 `content_hash` 的 `verdict='keep'`，使后续 `audit_all` 自动跳过该头像

#### Scenario: 仅获得中置信合格候选图时不强行入库而是送入待审头像
- **WHEN** 全源尝试结束后未能获得高置信合格图，但任一图源（含 Tier 1 / Tier 2 / Tier 3）产出了 VLM 判定合格且处于中置信度的候选图
- **THEN** 系统保持本地缓存为空、缓存该候选图预览字节，并在 `artist_image_candidates` 创建 `kind='replacement'` 待审项供管理员确认采纳

#### Scenario: VLM 验收遇模型 400 或解析失败时不静默放行结构化或联网图源
- **WHEN** 缺图获取路径执行 VLM 验收收到 HTTP 400、鉴权失败或非法 JSON 响应
- **THEN** 系统明确返回失败错误而不将该候选图静默写入 `artist_image_cache`，避免未经审查的图片漏入缓存

### Requirement: 新确认 MusicBrainz 身份全角色艺人异步自动补图与未审旧图审查队列
系统 SHALL 提供持久化异步队列 `artist_image_auto_queue` 及可配置开关 `auto_ingest_ai_enabled`，在任意角色艺人（含主唱、专辑艺人及幕后词曲编曲）通过规则自动匹配、Inline AI 自动确认或人工确认获得已确认 MusicBrainz 身份时异步入队执行头像补全与未审旧图把关，既不阻塞身份匹配或 HTTP 确认响应，也不误扫全库既有历史缺图。

#### Scenario: 仅由新确认 MusicBrainz 身份事件触发入队且不拉起历史存量缺图
- **WHEN** 管理员启用自动补图开关且曲库已有大量历史缺图艺人，随后新曲扫描并自动确认或人工确认某位艺人的 MusicBrainz 身份
- **THEN** 仅该位刚确认身份的艺人进入 `artist_image_auto_queue`，全库既有历史缺图艺人不会被自动拉入队列；未匹配到 MusicBrainz 身份（`no_result`）或仍在待审的艺人不触发入队

#### Scenario: 人工确认身份立即返回并在后台异步完成全链路头像补全
- **WHEN** 管理员在审核页或艺人页点击确认某位艺人的 MusicBrainz 候选身份
- **THEN** HTTP 确认请求在绑定身份并写入自动补图队列后立即重定向返回，不等待 VLM 质检或联网搜图耗时；后台串行 Worker 异步执行 Tier 1–3 获取与质检并共享全局 AI 限流器与 Tavily 多 Key 轮询池

#### Scenario: 刚确认 MusicBrainz 身份但已有未审旧缓存头像时顺手执行 VLM 审查
- **WHEN** 某位艺人此前已有本地缓存头像且 `artist_image_reviews` 中不存在当前 `content_hash` 的 `verdict='keep'` 记录，随后该艺人新确认了 MusicBrainz 身份
- **THEN** 后台自动队列借助新的 MusicBrainz 锚点对该旧图执行一次 VLM 视觉审查：合格则记为 `keep` 以后跳过，高置信不合格则触发全源重抓替换或按 1.A 策略差异化处理，中低置信疑点则推送至待审头像；若艺人已有自定义头像或当前哈希已为 `keep` 则零成本跳过

#### Scenario: 瞬时网络或限流失败支持退避重试且重启后队列不丢失
- **WHEN** 自动队列处理某位艺人时遇到网络超时、上游 5xx 或 429 限流，或服务端在队列尚未消化完时重启
- **THEN** 瞬时错误按退避间隔最多重试 3 次且不提前写入长期负缓存冷却；服务重启后残留的 `running` 任务自动复位为 `pending` 并由后台 Worker 继续消化

### Requirement: 管理后台配置自动入库补图开关并展示实时队列进度
系统 SHALL 在头像补全设置弹窗提供自动入库 AI 质检与联网补全开关，并提供管理员只读 JSON 端点向身份审核工作台展示后台自动补图队列状态。

#### Scenario: 管理员可独立启停新确认艺人自动 AI 质检与联网补全
- **WHEN** 管理员在 `/admin/matches` 打开头像补全设置弹窗并切换「新确认身份艺人自动 AI 质检与联网补全」后保存
- **THEN** 系统持久化 `artist_image_settings.auto_ingest_ai_enabled`；关闭后新确认 MusicBrainz 身份不再写入自动补图队列，已有配置不受影响

#### Scenario: 管理页顶部任务概览实时展示自动补图队列进度
- **WHEN** `artist_image_auto_queue` 中存在待处理或处理中的艺人，管理员访问 `/admin/matches`
- **THEN** 页面通过 `GET /admin/matches/images/auto-queue.json` 轮询获取队列计数与当前艺人，并在顶部任务概览区域展示进度标签，不占用手动全量补全任务的活动槽位
