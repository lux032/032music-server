# 歌手中文搜索别名补全

后台入口：控制台 → **中文别名补全**（`/admin/artist-aliases`）。

## 启用与触发

1. 在“AI 打标”保存现有模型接口、密钥、主模型和可选后备模型；无需开启歌曲打标。
2. 启用 MusicBrainz 数据来源，先完成歌手的外部身份确认。
3. 在“中文别名补全”填写 DeepSeek 官方 API Key，开启补全并保存。
   如现有模型接口是官方 `https://api.deepseek.com` 或 `/v1`，可复用其密钥。
4. 点击“开始存量匹配”处理旧曲库。仅开启开关不会开始旧曲库匹配。
5. 此版本上线后新增的歌手，确认身份后自动进入队列。重启、刷新已有歌手资料
   不会重新启动存量任务。新歌手暂未匹配时保留观察记录，等待身份确认。

只有有专辑歌手或曲目 primary 演唱关联的未合并艺人才处理；仅有幕后关联者不处理。
已经保存可用中文搜索别名的歌手会跳过。任务启动后优先读取绑定的 MusicBrainz
中文 locale 别名和关联 Wikidata 的中文 labels/aliases；历史 external profile 的
扁平别名没有语言标记，因此不会凭汉字外形判断它就是中文。

## 联网、模型与证据

缺少明确中文名称时，后端调用 DeepSeek 官方：

- `POST https://api.deepseek.com/v1/tools/search`：查询歌手中文名，返回 5 个结果。
- `POST https://api.deepseek.com/v1/tools/fetch`：读取候选页面正文。

搜索摘要只用于发现页面，不作为名称自动生效的依据。模型使用原有 AI 设置，
输入仅包含当前歌手身份、已知名称和受限的网页正文；不发送音频或歌词。
保存来源 URL、原始名称、语言、证据原文、获取时间和处理理由。

- 绑定身份的结构化来源给出一致的中文名称时自动生效。
- 官方网站由已确认 MusicBrainz 的 `official homepage` 关系确定；同域页面有
  明确“中文名”等说明、正文含已知歌手名且模型确认同一身份时才自动接受。
- 多个不同名称、其他站点、身份不确定结果进入待审核；摘要、无法核验的引用、
  不在正文中的名称和模型凭空生成的名称不会生效。
- 支持接受、修正后接受、拒绝。自动或接受后的别名只进入搜索，不修改展示名、
  外部身份或合并匹配。删除或拒绝会抑制以后自动添加同一名称。
- 仅对明确标注为中文的名称使用内嵌 OpenCC `t2s` 繁简转换，保留原文；不会把
  日文汉字姓名直接转为中文姓名。日本姓名没有通行中文名时允许保持缺失。

## 队列、限额与用量

默认每位歌手每轮最多搜索 2 次、读取 3 个页面、模型调用 2 次（可调整）。
所有尝试包括失败、格式重试、后备模型都计入上限；暂停、重启续跑不重置计数。
并发固定为 1，默认每分钟最多 6 次模型调用。**没有每日总量或金额上限**。
DeepSeek 搜索/读取与模型 token 分别计费，页面分别显示请求次数和模型 token。

旧曲库任务由用户点击后入队；再次点击只为当前仍缺名称且没有活动任务的歌手
创建新一轮。失败或未找到的已完成任务不会自动重跑。队列持久化，暂停保留队列，
恢复继续运行；进程重启自动恢复未完成任务，人工暂停状态保持。每项提交前再次检查
身份、合并关系和任务 epoch，拒绝网络窗口内身份变化或暂停后迟到的写入。
联网鉴权/余额错误，以及共享 AI 模型鉴权/额度/模型名称错误会暂停队列；
页面和失败记录分别指出是 DeepSeek 联网还是共享模型配置，修改后点击“继续”。
网页读取 403/404 仅表示该目标页面无法读取，跳过该页并尝试其他来源，不暂停队列。
来源限流等待后继续。任务记录优先展示运行中和失败项，避免早期失败被大量排队项遮住。

## 接口

所有接口需要管理员会话；API 状态接口也接受现有 API Token。

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET | `/api/v1/artist-alias-completion` | 脱敏设置、累计统计和当前状态 |
| GET | `/admin/artist-aliases/status` | 同上，页面每 5 秒更新进度 |
| POST | `/admin/artist-aliases/settings` | 表单：`enabled`、`deepseek_key`（空值保留）、`clear_key`、`search_limit`、`fetch_limit`、`model_limit`、`rpm` |
| POST | `/admin/artist-aliases/backfill` | 手动启动一轮存量任务 |
| POST | `/admin/artist-aliases/pause` | 暂停并中止当前请求，保留调用次数 |
| POST | `/admin/artist-aliases/resume` | 继续 |
| POST | `/admin/artist-aliases/candidates/{id}/accept` | 接受，可用表单 `value` 修正名称 |
| POST | `/admin/artist-aliases/candidates/{id}/reject` | 拒绝并记录抑制 |

所有表单 POST 必须携带 `csrfToken`。密钥永不返回页面或状态 JSON。
既有 `/api/v1/artists/{id}/search-aliases` 返回新增 `source` 字段
（`manual` / `automatic`）；原有手动增删接口保持兼容。
能力标记：`artistAliasCompletion: true`。
迁移 `072_artist_alias_completion.sql` 自动执行；已有曲库不会被标记成新歌手。
