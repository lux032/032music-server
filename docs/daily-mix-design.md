# 每日推荐歌单（Daily Mix）设计草案 v2

- 状态：**草案 v2**；P0–P4 已实施（见第 13.4–13.8 节）
- 范围：032 Music Server 负责打标、生成与反馈；MusicBridge App 和 Web 负责展示和收集反馈
- 前提：服务端是单用户模型
- v1 → v2 变更：首版就接入 LLM；以**情绪/风格**为主轴；去掉年代主题；新增曲目打标层；完整设计播放行为反馈；App 和 Web 都做；Web 主页左栏调整

## 已确认
| 项 | 结论 |
|---|---|
| LLM | 首版就在服务端接入（OpenAI 兼容接口，在服务端配置） |
| 主题 | 以情绪、风格为主；作品/创作者/歌手/重新发现保留为辅；**去掉年代** |
| 数量/长度 | 默认 **5 个**（1–8），每单 25 首（10–60） |
| 生成时机 | 首次请求时懒生成，加每天定时预生成（默认 05:00，UTC+8） |
| 换一个 | 允许，每天 ≤3 次 |
| 播放行为 | 需要设计（见第 7 节） |
| 展示端 | App 和 Web 都做 |

---

## 1. 目标与非目标
**目标**：每天生成 N 个以情绪/风格为主题的歌单。当天内容固定，跨天不重复，听感连贯；通过显式和隐式反馈持续变好。

**非目标（首版）**：声学特征分析（BPM、响度、音色模型）；多用户隔离；由 LLM 直接选曲（LLM 不允许生成曲目 ID）。

## 2. 核心难点：情绪/风格数据从哪来

| 来源 | 现状 | 价值 |
|---|---|---|
| 文件流派标签 | 稀疏且粗（Anime / J-Pop / Soundtrack） | 低，只作为风格线索 |
| MusicBrainz 歌手标签 | 有，按歌手粒度 | 中，作为风格线索 |
| Bangumi 条目标签 | 大多是元信息 | 低到中 |
| 歌词全文 `tracks.lyrics` | 有歌词的曲目都有 | **高**，情绪的主要依据 |
| 标题、作品、曲目类型（OP/ED/插曲） | 有 | 中。LLM 对知名 ACG 曲目本身就有认知 |
| 声学分析 | 无 | 最准，但需要额外的模型和计算（见 2.3） |

### 2.1 方案：LLM 离线打标（首版）
后台增量任务（复用 enrichment 的持久化运行框架）按批把曲目的结构化摘要发给 LLM，要求它**只能从固定词表中选择**：

- 每首曲目的输入：标题、歌手、专辑、作品名与类型、曲目类型（OP/ED/插曲/角色歌/伴奏）、流派标签、歌手标签、时长、**歌词节选**（去掉时间轴，取前约 12 行加副歌高频行，≤400 字）。
- 输出：
  `{trackId, moods:[{tag, weight}] ≤3, styles:[tag] ≤3, energy:1-5, vocal: vocal|instrumental|mixed, confidence:0-1}`
- 校验：标签不在词表内一律丢弃；trackId 不在本批内则整批作废并重试一次。
- 批量：每批 30–40 首，1 万首约 250–330 次调用。用输入指纹（元数据加歌词的哈希）做幂等：内容不变就不重打，只有新增或修改的曲目才打标。
- 没有歌词的纯器乐曲目，置信度通常较低。低置信度的标签在选曲时降权，不会被当作硬命中。
- 用户可以在 Web 曲目编辑页修正标签（`source=user`，优先级最高，重新打标时不覆盖）。

### 2.2 词表

#### 2.2.1 情绪词表 v2（已去掉负面情绪）
四个象限由 DEAM 模型的 arousal（唤醒度）和 valence（效价）划分，量程 1–9，阈值在 P0 阶段校准。声学证据取自 `mtg_jamendo_moodtheme`。

**主题情绪（12 个，可以作为歌单主题）**
| # | 标签（id） | 象限 | 听感定义 | 声学证据 | 歌词/元数据线索 |
|---|---|---|---|---|---|
| 1 | 热血（`hotblooded`） | 高能·正 | 燃、冲劲、战斗感 | energetic, powerful, action, heavy, fast, sport | 战斗、不屈、守护；战斗/竞技类 OP |
| 2 | 励志振奋（`uplifting`） | 高能·正 | 明亮向上、像大合唱 | uplifting, inspiring, motivational, hopeful, positive | 未来、约定、出发、梦想 |
| 3 | 元气欢快（`cheerful`） | 高能·正 | 轻快开心 | happy, fun, upbeat, party, positive, groovy | 日常、朋友、夏天；偶像曲 |
| 4 | 冒险旅途（`adventure`） | 中高能·正 | 出发、开阔、风景感 | adventure, travel, summer, nature | 远方、天空；游戏原野曲 |
| 5 | 俏皮诙谐（`playful`） | 中能·正 | 古灵精怪、洗脑 | funny, fun, children | 拟声词、电波歌词；角色歌 |
| 6 | 帅气律动（`cool`） | 中能·中 | 酷、有型、律动感 | cool, groovy, sexy | 都市、夜晚；爵士、City Pop |
| 7 | 壮阔史诗（`epic`） | 高能·中 | 宏大、管弦、命运感 | epic, film, trailer, adventure, powerful | 命运、世界、传说 |
| 8 | 浪漫甜蜜（`romantic`） | 中低能·正 | 心动、恋爱 | romantic, love, soft | 喜欢你、心跳、告白 |
| 9 | 治愈温柔（`healing`） | 低能·正 | 温暖、被安慰 | soft, calm, melodic, hopeful | 陪伴、没关系、家；ED |
| 10 | 宁静放松（`relaxed`） | 极低能·正 | 安静、背景感 | relaxing, calm, meditative, background, nature | 多为纯音乐 |
| 11 | 梦幻空灵（`dreamy`） | 低能·中 | 空灵、飘浮 | dream, space, soundscape, deep | 星空、梦、月；空灵女声 |
| 12 | 温暖怀旧（`nostalgic`） | 低能·中正 | 美好回忆、旧时光，偏温暖而不是悲伤 | retro, melodic, emotional（声学信号弱） | 回忆、那年夏天；主要以歌词判定 |

**内部负面标记（不作为主题、不展示）**：`sad`、`dark`、`lonely`。
这些仍然会打标，用途只有一个：**防止负面曲目混进正向主题**。如果不打负面标记，一首很悲伤的歌会被强行归到最接近的正向情绪里（例如治愈温柔），反而污染主题。
- 主情绪为负面且权重 ≥0.5 的曲目，默认不进入每日歌单；
- 负面权重在 0.3–0.5 之间的曲目，只允许进入低能量主题（治愈温柔、梦幻空灵、温暖怀旧），并放在歌单中段；
- 设置中提供"允许伤感类曲目"开关，默认关闭。

**相邻关系**：
- 热血 ↔ 励志振奋 ↔ 元气欢快 ↔ 冒险旅途
- 壮阔史诗 ↔ 热血、壮阔史诗 ↔ 冒险旅途
- 治愈温柔 ↔ 宁静放松 ↔ 梦幻空灵；治愈温柔 ↔ 浪漫甜蜜 ↔ 温暖怀旧
- 俏皮诙谐 ↔ 元气欢快；帅气律动 ↔ 梦幻空灵 / 元气欢快

**打标规则**：每首歌 1–3 个情绪（主题情绪与负面标记混合计数），主情绪权重 ≥0.4；允许"曲调明快、歌词悲伤"同时存在；纯音乐以声学结果为主；温暖怀旧的置信度上限为 0.5；用户修正优先。

#### 2.2.2 风格词表 v1（草案）
流行、摇滚、金属、朋克、电子、舞曲/EDM、抒情慢歌、偶像流行、民谣/原声、古风/民族、交响/管弦、钢琴、爵士/融合、City Pop/放克、说唱/嘻哈、电波/洗脑、氛围/环境、后摇/实验。
由 discogs-effnet 的 400 类风格加上乐器模型的结果映射得到，映射表在 P0 阶段根据实际分布再调整。

### 2.3 声学分析（已决定引入，选型见第 13 节）
声学模型的输出（风格、情绪、能量、人声与否、乐器、embedding）作为 `source=audio` 的事实依据，和歌词、元数据一起交给 LLM 做**融合判定**。LLM 不再凭空猜测标签，而是基于声学证据加上语义证据来确定最终标签。

## 3. 总体架构
```
[打标任务] 曲目 → LLM → track_tags (mood/style/energy/vocal)
                                   │
[每日生成] 口味画像(全局播放 + 推荐反馈) ─► LLM 主题规划(受约束) ─► 选曲流水线(算法) ─► 排序 ─► 冻结
                                   ▲                                         │
[反馈汇总] 显式(赞/踩/原因/不再推荐) + 隐式(带歌单上下文的播放会话) ◄──────────┘
```

## 4. 主题设计

### 4.1 主题类型
| 类型 | 定义 | 例 |
|---|---|---|
| `mood` | 单一情绪，风格可以混合 | 「深夜 · 孤独与安静」 |
| `style` | 单一风格，情绪在相邻范围内混合 | 「管弦与史诗」 |
| `mood_style` | 情绪与风格的组合（要求候选足够多） | 「伤感 · 钢琴抒情」 |
| `work` | 作品漫游，并按情绪聚合（例如某作品中所有热血曲目） | 「《XX》里的热血时刻」 |
| `composer` | 创作者，按其作品的情绪/风格聚合 | 「梶浦由記 · 壮阔篇」 |
| `artist` | 歌手电台（避开最常听的前 3 位） | 「从 XX 出发」 |
| `rediscover` | 很久没听的高播放曲目 / 收藏，按情绪聚类 | 「好久不见 · 温柔篇」 |

**槽位配比**：每天约 ⌈2N/3⌉ 个槽位给 mood / style / mood_style，其余给辅助类型。N=3 时是 2+1，N=6 时是 4+2。

### 4.2 主题规划（LLM + 约束）
1. 服务端先**算出可行的候选主题**：每个 (mood) / (style) / (mood, style) 组合中，可用曲目数要 ≥40（`mood_style` 要求 ≥30），并且不在冷却期内。
2. 口味画像：近 90 天播放加权（半衰期 30 天）后得到的情绪/风格分布，加上推荐反馈统计（第 7 节）。
3. 用 Thompson Sampling 为候选主题打分，取前 2N 个交给 LLM，附上口味画像摘要和最近 14 天已用过的主题。LLM 返回 N 个主题 ID，以及每个主题的 `title` 和 `description`。
4. 校验：只能选候选列表中的 ID，不能重复；同一 mood 不能用于两个槽位；辅助类型数量要符合配比。不合规时用 Thompson Sampling 的前 N 个兜底，标题改用模板。
5. 冷却：用过的主题冷却 7 天，被踩为"不喜欢主题"的冷却 45 天；相邻两天不使用同一个 mood。

## 5. 选曲流水线（每个歌单）
1. **候选池**：主题命中的曲目（按标签权重 × 置信度计算命中强度），加上以前若干首高偏好命中曲目为种子做 `rankedCandidates` 扩展的结果（扩展结果必须带有至少一个相邻的情绪/风格标签）。只保留 `available` 的曲目，伴奏、TV size 等非正曲由 `nonMain` 过滤。
2. **打分**：`w = relevance × preference × novelty × feedback`
   - relevance：标签命中强度（0–1）
   - preference：全局播放、完成、跳过率、收藏，取对数并截断
   - novelty：近 7 天的每日歌单中出现过 ×0.3，14 天内 ×0.6；24 小时内听过 ×0.5
   - feedback：推荐场景中的曲目亲和度（第 7 节），"不再推荐"直接排除
3. **配额抽样**：熟悉/探索默认 70/30，各组内用 Efraimidis–Spirakis 加权抽样，随机种子 = hash(日期, 槽位, 代数)。
4. **约束**：每个歌手 ≤3 首，每张专辑 ≤2 首，相邻两首不同歌手；同名曲目去重；当天各歌单之间曲目互斥；总时长 60–120 分钟。
5. **排序**：按能量曲线排（起 → 升 → 峰 → 落，`energy` 相邻差 ≤2），同时在贪心排序中兼顾元数据相似度。开头两首必须是熟悉曲目，探索曲目放在中段。
6. **不足时**：候选少于 12 首则换下一个候选主题；仍然不够就少生成一个，不硬凑。

## 6. 显式反馈
- **歌单**：👍 / 👎（可撤销）。点 👎 时可多选原因：
  - `repetitive` 太重复 → 加强 novelty 惩罚，冷却延长
  - `theme` 不喜欢主题 → 该主题 β 大幅增加，冷却 45 天
  - `tracks` 歌不好听 → 歌单内没有完整播放的曲目降低亲和度
  - `flow` 风格太跳 → 排序时加大能量/相似度约束
  - `mismatch` 歌和主题不符 → 降低这些曲目在该标签上的置信度，并进入"标签复核"列表
- **单曲**：选"不再推荐"；在歌单内收藏视为强正反馈。
- **保存为歌单**：视为强正反馈。**换一个**：如果被替换的歌单完全没播放过，记为轻度负反馈。
- **优先级**：显式反馈会压过同一歌单的隐式反馈（例如点了 👍，跳过的惩罚减半）。

## 7. 播放行为（隐式反馈）设计

### 7.1 上下文归因（协议变更）
- `POST /api/v1/playback/events` 的 start 事件新增可选字段 `context: {type: "dailyMix", id: "<mixId>", position: <index>}`。
- 现在的协议遇到未知字段会返回 400，因此要在 capabilities 中声明 `features.playbackContext` 和 `playback.contextTypes: ["dailyMix"]`。App 和 Web 只有在看到这项能力时才发送。
- 续播链（`resumedFromSessionId`）继承前一个会话的 context，客户端不需要重复发送。
- `playback_sessions` 新增 `context_type`、`context_id`、`context_position` 三列。
- 如果用户从每日歌单中把某首歌加入了别的队列，就不算这个歌单的上下文；只有从每日歌单的队列播放才算。

### 7.2 单曲信号（只统计带歌单上下文的会话）
| 行为 | 判定 | 信号 |
|---|---|---|
| 完整播放 | `completed`，或播放进度 ≥80% | +1.0 |
| 中途跳过（后段） | `skipped`，进度在 50%–80% | 0 |
| 中途跳过（中段） | `skipped`，30 秒到 50% | −0.4 |
| 秒切 | `skipped`，<30 秒 | −1.0 |
| 播放中收藏 | 会话期间曲目被收藏 | +2.0 |
| 重听 | 同一歌单中同一曲目播放 ≥2 次 | +0.5 |
| 停止 / 关闭 / 被替换 / 出错 / 过期 | `stopped` / `client_closed` / `replaced` / `error` / `expired` | 0（用户离开不等于不喜欢） |

### 7.3 歌单级信号
- 参与度 = 已播曲目数 / 歌单长度；满意度 = Σ单曲信号 / 已播曲目数。
- 已播 ≥5 首且满意度 ≥0.5：主题 α 增加 `0.5 × 参与度`。
- 前 5 首中秒切 ≥3 首：β 增加 0.7（说明开头没抓住人）。
- 打开过但一首都没播：不计分。

### 7.4 聚合与存储
- 每次每日生成前（以及每小时一次）增量汇总**尚未处理**的带上下文会话，结果写入：
  - `rec_track_affinity`：曲目在推荐场景中的亲和度，半衰期 30 天；
  - `rec_tag_affinity`：情绪/风格标签的亲和度，由单曲信号按标签权重分摊；
  - `daily_mix_theme_stats`：主题的 α/β。
- 汇总必须早于播放会话的保留期清理执行。汇总后记录处理水位（`processed_until`），保证幂等。
- 全局播放（不带上下文）只进入口味画像，且权重较低：它反映的是"你平时听什么"，不能说明"推荐得好不好"。

### 7.5 防止反馈闭环
- 探索配额最低 20%，不随反馈降低。
- 标签亲和度有下限，不会因为几次跳过就把一个情绪彻底排除；只有选了 👎 `theme` 才会进入长冷却。
- 每周至少安排一次"冷门主题"槽位（从近 30 天没出现过的情绪/风格中选）。

## 8. 数据模型（迁移 047/048）
```sql
-- 047 打标
CREATE TABLE track_tags (
  track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('mood','style')),
  tag TEXT NOT NULL, weight REAL NOT NULL, confidence REAL NOT NULL,
  source TEXT NOT NULL CHECK (source IN ('llm','user','audio')),
  PRIMARY KEY (track_id, kind, tag, source)
) STRICT;
CREATE TABLE track_tag_meta (
  track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
  energy INTEGER, vocal TEXT, input_fingerprint TEXT NOT NULL,
  vocab_version INTEGER NOT NULL, model TEXT NOT NULL, tagged_at TEXT NOT NULL,
  needs_review INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE TABLE ai_settings (id INTEGER PRIMARY KEY CHECK (id=1),
  base_url TEXT, model TEXT, api_key TEXT,     -- key 只写不读
  tagging_enabled INTEGER NOT NULL DEFAULT 0, include_lyrics INTEGER NOT NULL DEFAULT 1) STRICT;

-- 048 每日歌单与反馈
daily_mix_settings(id=1, enabled, mix_count 1-6, track_count 10-60, explore_ratio, pregenerate_hour, regenerate_limit=3)
daily_mixes(id, day, slot, generation, status active|replaced, theme_type, theme_key, title, description,
            plan_json, generator_version, rating -1|0|1, rating_reasons, rated_at, saved_playlist_id, created_at)
daily_mix_items(mix_id, position, track_id, bucket familiar|explore, reason)
recommendation_track_blocks(track_id, created_at)
daily_mix_theme_stats(theme_type, theme_key, alpha, beta, last_used_day, cooldown_until, updated_at)
rec_track_affinity(track_id, score, updated_at)
rec_tag_affinity(kind, tag, score, updated_at)
rec_feedback_watermark(id=1, processed_until)
ALTER TABLE playback_sessions ADD COLUMN context_type TEXT / context_id TEXT / context_position INTEGER;
```

## 9. API
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/daily-mixes` | 当天的歌单（封面拼图、标题、主题标签、曲数、时长、rating）。当天还没生成时同步生成（预算约 8 秒，含 LLM），超时返回 `202 {status:"generating"}` |
| GET | `/api/v1/daily-mixes/{id}` | 详情加曲目 |
| PUT | `/api/v1/daily-mixes/{id}/rating` | `{value, reasons}` |
| POST | `/api/v1/daily-mixes/{id}/regenerate` | 超过次数返回 429 |
| POST | `/api/v1/daily-mixes/{id}/save` | 另存为普通歌单 |
| PUT/DELETE | `/api/v1/tracks/{id}/recommendation-block` | 不再推荐 / 恢复 |
| GET/PATCH | `/api/v1/daily-mixes/settings` | 歌单数量等 |
| GET/PATCH | `/api/v1/ai/settings` | LLM 配置（key 只写），打标开关 |
| GET/POST | `/api/v1/ai/tagging` | 打标进度 / 手动触发 |
| GET/PUT | `/api/v1/tracks/{id}/tags` | 查看 / 用户修正标签 |
| — | `capabilities` | `dailyMixes`、`trackTags`、`playbackContext` |
| — | `/api/v1/home` | 新增 `dailyMixes` 区块 |

## 10. 展示

### 10.1 Web 主页左栏（已确认）
现状：最近播放 5 首在上，接着听 2 首在下（共 7 行）。
调整为：**今日推荐 N 行在上**（行样式沿用 `.h-row`：左侧是 2×2 封面拼图，标题，副标题为"情绪 · 风格 · 25 首 · 1 小时 32 分"，右侧是播放按钮），**最近播放在下，填充 7−N 行**（最少 2 行）；去掉接着听。`/api/v1/home` 中的 resume 字段保留，以免破坏 App。

### 10.2 Web 其他
每日歌单详情页（复用歌单详情模板，加入赞/踩和原因面板、换一个、另存）；设置页加每日推荐与 AI 配置；曲目编辑页加标签修正；控制台显示打标进度。

### 10.3 App
主页在"最近播放"上方新增"今日推荐"**可滑动横幅**（已确认，不使用专辑卡样式）：每页一个歌单，宽幅封面拼图配合取色背景，显示标题、简介、情绪/风格标签和播放按钮，带分页指示，可以左右滑动；具体视觉在 App 阶段单独出稿；详情复用歌单详情页，加赞/踩、原因面板、换一个、另存；曲目菜单加"不再推荐"；播放每日歌单时上报 context。

## 11. 待决策
| # | 问题 | 建议 |
|---|---|---|
| Q1 | 情绪/风格来源：首版用 LLM 离线打标（含歌词节选），声学分析放到以后 | 按建议 |
| Q2 | 是否允许把歌词节选发给 LLM | 允许（可在设置中关闭） |
| Q3 | 第 2.2 节的词表是否需要增删 | 请调整 |
| Q4 | 槽位配比：⌈2N/3⌉ 个给情绪/风格，其余给作品/创作者/歌手/重新发现 | 按建议，或者全部用情绪/风格 |
| Q5 | Web 主页左栏的理解（10.1） | 请确认 |
| Q6 | 曲库规模（用来估算打标成本和耗时） | 请告知 |
| Q7 | App 现有的"AI 灵感歌单"（即时输入一句话）以后是否改为调用服务端（复用打标和选曲） | 建议改 |

## 12. 分阶段（引入声学分析后调整）
| 阶段 | 内容 |
|---|---|
| P0 验证 | 在你的实际硬件上用 sidecar 原型跑 200 首（含 ACG / 纯音乐 / 摇滚 / 抒情）：实测耗时，抽查标签准确率，确定模型组合 |
| P1 声学分析 | sidecar 容器、032 侧任务调度（增量、断点续跑、低优先级、可暂停）、`track_audio_features` 表、控制台进度 |
| P2 AI 配置与融合打标 | LLM 客户端，融合打标（声学 + 歌词 + 元数据 → 词表），Web 标签修正 |
| P3 服务端生成 | 每日歌单生成全链路与 API；相似度升级为声学 + 元数据混合 |
| P4 Web | 主页左栏调整、详情页、反馈 UI、设置 |
| P5 App | 主页滑动横幅、详情、反馈、"不再推荐"、设置 |
| P6 播放上下文与反馈闭环 | 协议扩展（服务端 + Web + App）、聚合、bandit、防闭环 |
| P7 评估 | 重复率、歌手集中度、赞/踩率、推荐场景跳过率 |

## 13. 声学分析选型（调研 2026-10-16）

| 方案 | 说明 | 优点 | 缺点 |
|---|---|---|---|
| **A. 自建 Essentia 分析 sidecar**（推荐） | 独立的 Python 容器（`essentia-tensorflow` + MTG 预训练模型），以只读方式挂载同一个 `/music`；032 通过内网 HTTP 下发任务（曲目 ID + 相对路径）并取回 JSON | 直接读本地文件，不经网络下载；结果存进 032 自己的 SQLite；模型按需挑选，能直接映射到第 2 节的词表；部署只多一个容器 | 需要自己维护一个小服务（约 500 行 Python 加 Go 侧调度） |
| B. 接入 AudioMuse-AI（2.7k★，AGPL-3.0，持续活跃） | 部署完整的 AudioMuse（Flask + Worker + PostgreSQL 15）；032 需要实现 Subsonic 兼容接口（复用它的 Navidrome 适配器）或为它写 032 适配器；结果通过 `/external/get_score`、`/external/get_embedding` 取回 | 功能成熟：CLAP 文本搜索、歌词搜索、路径、聚类、AI 命名 | 栈重（官方最低要求 4 核 AVX2、8GB 内存、NVMe）；音频要经 HTTP 下载；情绪标签是 MusiCNN 的 MSD 50 类（偏西方流行标签）加 6 个 CLAP 标签，风格粒度不够；它自带的歌单/AI 功能和我们的设计重叠，长期维护两套数据，版本耦合 |
| C. bliss-rs（GPL-3.0） | 20 维手工声学特征（节奏、频谱、色度、音色），适合计算相似度 | 轻、快 | 没有情绪/风格标签，只能用于相似度 |
| D. LAION-CLAP（CC0） | 文本-音频联合 embedding | 可以直接用"安静的钢琴"这类文本检索 | 模型大，CPU 推理慢；适合作为后续的"文本找歌"，不适合作为首版主力 |

### 13.1 方案 A 的模型组合（Essentia 官方模型目录）
共享主干 **discogs-effnet**：每首曲目只需算一次 embedding，后面接多个分类头，开销很小。
| 用途 | 模型 | 输出 |
|---|---|---|
| 风格 | `genre_discogs400-discogs-effnet` | 400 个 Discogs 风格（含 J-pop、Anison、Vocaloid 一类以及 Score/Soundtrack），映射到风格词表 |
| 情绪/主题 | `mtg_jamendo_moodtheme-discogs-effnet` | 56 类（calm、dark、epic、melancholic、romantic、uplifting、energetic…），映射到情绪词表 |
| 乐器 | `mtg_jamendo_instrument-discogs-effnet` | 40 类（piano、strings、guitar、synthesizer、voice…），用于"钢琴""管弦"等风格判定 |
| 声学/电子 | `nsynth_acoustic_electronic-discogs-effnet` | acoustic / electronic |
| 人声 | `voice_instrumental`（effnet 版本优先） | voice / instrumental |
| 唤醒度/效价 | `deam` / `emomusic`（musicnn 主干，可选第二主干） | arousal、valence（1–9），作为能量和情绪象限 |
| 可选 | `danceability`、`engagement`、`approachability` | 排序和"背景/专注"类主题 |
| 经典算法 | RhythmExtractor、KeyExtractor、LoudnessEBUR128 | BPM、调式、响度 |
| 相似度 | effnet embedding（1280 维，可降到 128 维存储） | 声学相似度，替代或混合现有的纯元数据 `similar` |

- **采样**：不分析整首，取 3 个 30 秒窗口（25% / 50% / 75% 处）后取平均，可以大幅降低开销。
- **成本预估（待 P0 实测）**：4 核 x86 每首约 2–6 秒，1.7 万首约 10–30 小时；低功耗 NAS 或 ARM 可能需要 2–3 倍时间。后台低优先级运行，可暂停，可断点续跑，之后只处理新增曲目。
- **许可**：Essentia 是 AGPL-3.0，只用在独立的 sidecar 中（sidecar 本身开源即可），032 主程序仍然是 MIT。MTG 模型是 **CC BY-NC-SA 4.0（仅限非商业）**：模型文件不放进仓库，在构建或首次启动时下载，并在 README 中注明。

### 13.2 融合判定
LLM 打标的输入增加声学证据：风格 top-5、情绪 top-5（含概率）、乐器 top-3、人声/纯音乐、arousal/valence、BPM。规则是：
- 声学置信度高（概率 ≥0.5）时，声学结果直接作为风格/乐器结论，LLM 不能推翻，只能补充；
- 情绪由 LLM 综合声学结果和歌词给出；
- 纯音乐、无歌词的曲目以声学结果为主；
- 歌词与声学矛盾（例如曲调明快但歌词悲伤）时，允许两个情绪同时存在，分别记权重。

### 13.3 目标硬件结论（NAS：Celeron J4125 4C/4T、11.5GB 内存、Intel UHD 600、Alpine + Docker）
- **J4125 不支持 AVX/AVX2**（只到 SSE4.2）→ 不能使用 `essentia-tensorflow`（官方 TF 包要求 AVX），也不能使用 AudioMuse-AI（要求 AVX2）。**方案 B 排除。**
- 改为 **ONNX Runtime**（MLAS 在运行时按 CPU 能力选择内核，存在 SSE 路径）；如果实测不可用，备选 **OpenVINO**（CPU 最低要求 SSE4.2，并且可能支持用 UHD 600 核显加速，需实测）。
- 模型全部使用官方 ONNX 版本，不依赖 TensorFlow：
  - `discogs-effnet-bsdynamic-1.onnx`（18MB）一次推理同时输出 **400 类风格**和 **1280 维 embedding**，不需要再单独加 genre400 分类头；
  - `mtg_jamendo_moodtheme-discogs-effnet-1.onnx`、`mtg_jamendo_instrument-discogs-effnet-1.onnx`；
  - `msd-musicnn-1.onnx` + `deam-msd-musicnn-2.onnx`（arousal/valence，可选）。
- 梅尔频谱预处理：优先使用 `essentia`（不含 TF 的包）的 `TensorflowInputMusiCNN`，若在该 CPU 上不可用，则用 numpy 复刻，并和参考输出做数值比对。
- sidecar 镜像基于 Debian slim（glibc），因为 onnxruntime 没有 musl 版本的包；宿主机是 Alpine 不受影响。
- **资源策略**：宿主机平时负载 1.5–2，并且要承担实时转码。sidecar 默认 2 个推理线程，以最低优先级（nice 19）运行；可以设定运行时间窗口（默认 01:00–08:00，另提供"全天"选项）；**032 有实时转码会话时，自动暂停下发分析任务**。
- **耗时预估**（待 P0 实测）：每首歌取 3×30 秒，effnet 约 3–6 秒/首，2 个 worker 处理 1.7 万首约 8–15 小时，保守估计 30 小时以内；启用 musicnn/DEAM 约多 30%。

### 13.4 P0 实测结论（NAS：J4125，2026-10）
| 项 | 结果 |
|---|---|
| 稳定性 | 主测试 224/224 成功；去掉 musicnn、OpenVINO CPU、OpenVINO GPU 三组对比测试各 30/30 成功 |
| 后端 | ONNX Runtime 1.31 在无 AVX 的 J4125 上探测通过，没有发生回退 |
| 每首耗时（ORT，2 线程，3×30 秒） | 平均 6.84 秒（解码 0.82 / 梅尔 0.10 / effnet 1.27 / 分类头 0.11 / **musicnn 4.29**）；去掉 musicnn 约 2.9 秒 |
| 全库（1.7 万首） | 含 musicnn 约 32 小时；**不含约 12–13.5 小时** |
| OpenVINO CPU | 每首约 6.1 秒（与 ORT 差距在样本误差范围内），作为备用后端 |
| OpenVINO GPU（UHD 600） | 中位数 4.6 秒，但 P95 38 秒、最大 114 秒，负载升到 11.8，内存接近 2GB 上限 → **不采用** |
| 资源 | ORT 去掉 musicnn 时约 270MB 内存、2 核接近满载 |
| 用 effnet 预测唤醒度/效价（5 折交叉验证，224 首） | embedding 岭回归：唤醒度 r=0.85、效价 r=0.91，能量档一致 86%、相差≤1 为 100%；仅分类头：0.81 / 0.86 |

**决策**
1. 默认使用 ONNX Runtime CPU，OpenVINO CPU 作为探测失败时的回退，不使用核显。
2. 去掉 musicnn 的常规推理，唤醒度/效价改为由 **effnet embedding 岭回归**预测。
3. 回归权重**按部署本地校准**，不放进仓库（因为是由 CC BY-NC 模型的输出得到的）：全库分析开始时随机抽约 300 首运行完整模式（含 musicnn，约多 20 分钟，只做一次），在本地拟合并做交叉验证；唤醒度 r < 0.75 时回退到仅分类头的回归，并在控制台提示。权重与 effnet 模型版本绑定，模型升级时重新校准。
4. 临时情绪/风格映射只作为 LLM 融合打标的声学证据；"偶像流行"不再由 J-pop 推断。P0 抽查发现的情绪偏差（纯音乐被判为浪漫、摇滚被判为以黑暗为主）在 P2 融合阶段结合歌词和元数据校正。
5. 可优化的点：每首做一次 ffprobe 加 3 次 ffmpeg 解码（0.82 秒），P1 合并为单个 ffmpeg 进程。

### 13.5 P1 实施记录（声学分析，已合入 main）
| 组件 | 位置 | 要点 |
|---|---|---|
| 分析服务 | `analyzer/` | Python + ONNX Runtime（回退 OpenVINO CPU），numpy 复刻预处理；`/health`、`/v1/labels`、`/v1/analyze`（fast/full）、`/v1/av/fit`；单并发，忙时 503 + Retry-After；路径限制在 `/music` 内；可选 Bearer Token；非 root 运行；模型首次启动时下载 |
| 部署 | `compose.yaml` 的 `analysis` profile | 默认不启动；只在内网提供服务、音乐目录只读、2 核 2GB 上限；模型放在命名卷；支持用代理预先构建同名镜像 |
| 配置 | `MUSIC_SERVER_ANALYZER_URL` / `_TOKEN` | URL 为空表示未部署；URL 非法时拒绝启动 |
| 数据 | 迁移 `047_audio_analysis.sql` | 设置（默认关闭）、`track_audio_features`（float16 向量，约 3.7KB/首）、失败记录（按指纹和版本生效，指数退避或永久失败）、唤醒度/效价模型、类别名 |
| 调度 | `internal/analysis` | 开关打开即开始首次全量分析；扫描完成后唤醒，自动分析新入库或文件变化的曲目；文件指纹或分析版本变化时重新分析；有转码或扫描时让路；可选运行时间窗（UTC+8，可跨午夜）；先用完整模式校准 min(300, 曲库) 首，满 40 首即拟合，之后快速模式加回归，并支持回填 |
| 管理页 | `/admin/audio-analysis` | 开关、暂停/继续、时间窗、进度/ETA、校准指标、失败列表与全部重试；首页入口卡片；每 3 秒轮询 |
| API | `GET /api/v1/audio-analysis`；`capabilities.features.audioAnalysis` | 状态 JSON；已部署且已开启时 capability 为 true |

**验证**：Go 单元/集成测试（storage、analysis、http、config）与 race 检测通过；Python 15 项测试通过（含真实模型和 Essentia 参考输出的一致性检查）；端到端测试中使用真实分析服务、真实 032 二进制和 48 首不同的音频：默认关闭时不发起任何分析 → 管理页开启 → 48 首完整模式校准 → 拟合 → 空闲 → 新增 3 首并增量扫描 → 自动以快速模式分析并用回归计算唤醒度/效价。

**下一步（P2）**：LLM 融合打标（声学证据 + 歌词 + 元数据 → 第 2.2 节的情绪/风格词表），以及声学与元数据混合的相似度。

### 13.6 P2 实施记录（AI 融合打标，已合入 main）
| 组件 | 位置 | 要点 |
|---|---|---|
| 数据 | 迁移 `048_ai_tagging.sql` | `ai_settings`（单行；Key/代理只写不读；打标默认关闭、歌词默认发送、60 次/分钟、并发 4）、`track_tags`（source = audio/llm/user，user 优先）、`track_tag_meta`（source_stamp + input_hash 双重幂等、复核原因、用量）、`ai_tagging_failures`（输出无效等单曲失败，退避 10 分钟/1 小时/6 小时/24 小时，4 次后暂停）、`ai_usage`（按日/模型/用途）、`ai_sample_runs/items` |
| 词表与证据 | `internal/tagging/vocab.go`、`evidence.go` | 情绪 12 + 内部负面 3、风格 18（VocabVersion=1）；P0 映射规则移植到 Go：Discogs 前 10 归并风格得分，≥0.5 或乐器 ≥0.5 的风格锁定（服务端强制保留）；情绪参考得分、能量等级、专辑声学摘要 |
| 输入 | `prompt.go` | 元数据（应用人工覆盖）、作品与用途、声学证据、专辑摘要、歌词片段（去时间轴与制作人员行，前 12 行 + 高频副歌，≤600 字；纯音乐不发送）；系统提示对所有曲目相同（前缀缓存）；JSON Schema 结构化输出（PromptVersion=1） |
| 模型客户端 | `llm.go` | OpenAI 兼容 `/chat/completions`，temperature 0，可选 `reasoning_effort`；不支持 json_schema 时自动降级为 json_object；错误分类：401/403/404 阻塞到修改设置，429/配额/5xx/网络整体退避（遵守 Retry-After），400/过滤/无效输出记单曲失败；错误信息去除 API Key |
| 校验 | `result.go` | 丢弃词表外标签、截断数值、情绪 ≤3 且权重归一、nostalgic 把握 ≤0.5、风格 ≤3 且保留锁定风格、偶像判定与风格一致；复核原因：主情绪把握 <0.5、有人声流行类无法判断偶像、主情绪与唤醒度矛盾、漏掉锁定风格 |
| 调度 | `service.go` | 开启即全量，之后增量（声学分析保存结果/回填唤醒度、扫描完成、设置变化时唤醒）；声学分析开启时等待该曲特征（及唤醒度回填）或其永久失败；新曲优先；并发 + 匀速限流；复核开启时立即复核，否则标记待复核，开启后处理积压；抽样对比（主/复核模型各一次，不写标签）与连接测试 |
| 管理页 | `/admin/ai-tagging` | 开关、模型服务设置、测试连接、进度/剩余 token 预估、用量表、抽样对比指标与逐首对比、最近结果、失败与全部重试；控制台卡片；每 5 秒轮询 |
| 曲目页 | `/admin/tracks/{id}/tags` | 生效标签、AI 结果（能量/人声/偶像/模型/用量/复核原因）、声学证据、人工修正（情绪/风格各 1–3 个）、恢复自动结果、重新打标；歌曲列表抽屉入口 |
| API | `GET /api/v1/ai/tagging`、`GET/PUT /api/v1/tracks/{id}/tags`、`capabilities.features.trackTags` | 负面标记带 `internal: true`，客户端不应展示 |

**验证**：存储、打标服务（含 race）、HTTP 层测试通过；端到端：真实 032 二进制 + 51 首带声学特征的测试库 + 本地模拟 OpenAI 兼容服务：全量 51 次请求（json_schema、temperature 0、reasoning_effort=none 均生效），系统提示约 2,600 字、单曲输入约 1,340 字符 JSON；模拟服务故意漏掉的锁定风格被服务端补上；开启复核后自动处理 42 首积压；50 首抽样对比 100 次请求并给出指标；页面不回显 Key。

**下一步（P3）**：每日歌单生成全链路与 API；声学 + 元数据混合相似度。

### 13.7 P3 实施记录（每日歌单生成、API、混合相似度，已合入 main）
| 组件 | 位置 | 要点 |
|---|---|---|
| 数据 | 迁移 `049_daily_mix.sql` | `daily_mix_settings`（默认开启、**5 个** × 25 首、探索 30%、05:00、换一个 3 次/天、不允许伤感、模型起名）、`daily_mixes`（推荐日 + 槽位 + 代数，active/replaced，plan_json 记录入选依据，评分及其已计入主题统计的增量）、`daily_mix_items`（bucket、reason_json）、`recommendation_track_blocks`、`daily_mix_theme_stats`（α/β、冷却）；`ai_usage` 重建以加入 `mix` 用途。P6 的 `rec_*_affinity` 与播放上下文未建 |
| 推荐日 | `dailymix.Day` | UTC+8 日期，在预生成时刻切换（05:00 前仍显示前一天的歌单） |
| 候选主题 | `internal/dailymix/themes.go` | 情绪 12、风格 18、情绪×风格；作品（≥15 首）、创作者、歌手电台（最常听的前 3 位除外，按偏好取至多 12 位，听感相近的曲目补充）、好久不见（播放 ≥3 或收藏、60 天未听、按情绪）。命中强度：情绪 `min(1, 权重/0.6) × 把握`，风格取把握；相邻情绪/风格以 0.4–0.45 折扣补充；AI 未打标的曲目按与主题核心曲目的声学质心相似度补充（打标期间也能出歌单）。池大小门槛：情绪/风格 max(40, 1.6L)、情绪×风格 max(30, 1.2L)、好久不见 max(20, 0.8L)、其余 max(15, 0.6L) |
| 偏好画像 | `profile.go` | 90 天内计数播放按 30 天半衰期加权（会话只保留 30 天，更早的用累计播放次数轻量补充）+ 收藏；情绪/风格的“收听占比 / 曲库占比”平滑后开方，限制在 0.6–1.6 |
| 主题规划 | `planner.go` | Beta(α, β) Thompson 抽样 × 偏好；冷却：7 天内用过、`cooldown_until` 未到、昨天用过的情绪；槽位 ⌈2N/3⌉ 情绪/风格 + 其余辅助（不足时用情绪/风格补）；同一情绪/风格当天只用一次；6 天内没有冷门主题时，强制加入一个 30 天未用且偏好 ≤1 的主题。候选清单（2N 左右）交给模型：只能选清单内 id、数量与配比固定、必须包含冷门主题，标题 2–16 字、简介 ≤50 字、禁止负面词；违规时按服务端排序，并保留合规的标题；失败或未配置时用模板标题 |
| 选曲 | `select.go` | 权重 = 相关度² × 偏好（播放对数、收藏、歌单、跳过率）× 新鲜度（7 天内推荐过 ×0.3、14 天内 ×0.6、24 小时内听过 ×0.5）× 新入库 1.3；直接命中先按 70/30 配额做 Efraimidis–Spirakis 抽样（种子 = 日期/槽位/代数/主题），探索不足时用相邻/听感相近的未听曲目补到下限，其余缺口再由相邻曲目补；歌手 ≤3（歌手电台本人 40%）、专辑 ≤2（作品/创作者 4）、同名去重、当天互斥、≤120 分钟，<60 分钟时可多加至 L+8；少于 12 首换下一个主题，仍不够就少生成一个 |
| 排序 | `select.go` | 按能量排序后每隔 3 首取 1 首组成“回落段”（降序），其余为“上升段”（升序），相邻能量差小；同能量段内按声学相似度串联；再在能量相差 ≤1（不得已 ≤2）的曲目间交换，满足开头 2 首熟悉、结尾 2 首不放探索、轻度伤感放中段、相邻不同歌手 |
| 服务 | `service.go` | 每分钟检查，推荐日切换后自动生成；首次请求等待 8 秒（含模型 6 秒上限），超时返回 202 且后台继续；曲库不足时 10 分钟内不重试；换一个：同类主题优先、排除当天所有歌单（含被替换的）的曲目与主题，模型只为新主题起名，管理页不计次数；重新生成今天 |
| 显式反馈 | `storage/daily_mix.go` | 赞 α+1；踩 β+1，“不喜欢主题”再 β+2 并冷却 45 天，“太重复”冷却 14 天；改评/撤销先回退旧增量；另存为歌单 α+0.5（每个歌单一次）；不再推荐从下一次生成起排除 |
| API | 见 README | `GET /api/v1/daily-mixes`（202 生成中）、详情、评分、换一个（429/409）、另存、不再推荐、设置；`capabilities.features.dailyMixes`。入选原因只在管理页显示 |
| 管理页 | `/admin/daily-mixes` | 状态与最近一次生成的统计（曲目/标签/声学覆盖、候选主题与抽样得分、模型调用结果、跳过的主题）、每个歌单的封面拼图与曲目表（分组、能量、入选方式、相关×偏好×新鲜、标签、不再推荐）、换一个、重新生成、设置、最近 7 天标题；控制台卡片 |
| 混合相似度 | `storage/acoustic.go`、`similarity*.go` | 最新分析版本的 embedding 去均值、归一化、int8 量化常驻内存（1.7 万首约 22MB），曲库变化时最多每 10 分钟重建；两首都有声学特征时 得分 = 0.6 × 元数据 + 0.4 × 声学（余弦 0.2→0、0.8→1），并把最近的 80 首声学邻居加入候选；新增原因 `soundsSimilar`。路径搜索只重新打分、不扩充候选 |

**验证**：单元测试覆盖主题与负面规则、配额/约束/确定性/新鲜度/能量、模型输出校验与回退、冷却与 Beta 抽样、存储端到端（生成→冻结→换一个与次数→评分回退→另存→不再推荐→次日排除）、HTTP API 与管理页、声学索引与混合相似度；1.7 万首合成曲库（含 embedding，四分之一未打标）在开发机上生成 5 个歌单约 0.75 秒。真实 032 二进制上：迁移 049 正确保留 `ai_usage` 数据，测试库（51 首）显示“曲库不足”，相似曲目按听感给出 `soundsSimilar`。

**首轮 NAS 数据后的调整（GeneratorVersion 2）**：全库 LLM 标签分布显示流行 8619 首（49%）、电子 6284 首（36%），风格的第二、三个往往把握偏低（摇滚 5178 个标签中 ≥0.65 的仅 2933 个）；City Pop 只有 507 首，但借“相邻风格：流行”扩出了 6242 首候选。因此：风格直接命中要求把握 ≥0.6；超过曲库 30% 的风格视为泛风格，不单独成主题、也不作为相邻扩充的来源（仍可出现在情绪×风格组合中）；每个主题的相邻标签曲目不超过直接命中数；熟悉/探索各自的缺口先由同组的相邻曲目补，避免“熟悉 3 / 探索 22”；标题中带 TV Size / Short ver. / Off Vocal / Instrumental 等的曲目视为非正曲排除（NAS 上有 40 首只在标题里标注），装饰部分带 Live 的权重 ×0.35、Remix/其他版本 ×0.75，同一首歌的不同版本按去掉装饰后的标题去重。

**第二轮调整（GeneratorVersion 3）**：模型给的能量集中在 3–4，排序几乎没有起伏 → 排序改用声学唤醒度（1–9 连续值，没有声学数据时用能量换算），同强度段按听感串联，管理页“起伏”列显示该值；当天最安静的两首熟悉曲目开场，其余再排弧线。情绪/风格类主题的熟悉曲目不足 max(3, L/6)（25 首时为 4）时，从“同情绪任意风格 / 同风格任意情绪 / 相邻标签”中挑听过的歌补到这个下限（入选方式“熟悉开场”），避免冷门主题整单都是没听过的歌。

**待 NAS 数据后调参**：声学分数映射（余弦 0.2–0.8）、相似度中声学权重 0.4、各类主题的池大小门槛、相邻标签折扣、探索补充的阈值。

**下一步（P4）**：Web 主页左栏“今日推荐”、歌单详情页与反馈 UI、设置。

### 13.8 P4 实施记录（Web，已合入 main）
| 组件 | 位置 | 要点 |
|---|---|---|
| 主页左栏 | `templates/home.html`、`home.go` | “今日推荐” N 行（`.h-row` 样式：2×2 封面拼图、标题、“情绪 · 风格 · 25 首 · 1 小时 32 分”），点封面通过 `/api/v1/daily-mixes/{id}` 取曲目并替换播放队列（可撤销），点标题进详情；下方“最近播放”填满 7−N 行（至少 2 行），“接着听”不再显示（`/api/v1/home` 的 resume 字段不变）。首次打开当天最多等 1.5 秒，未生成完显示“正在生成”；曲库不足或关闭时保持原布局 |
| 歌单页 | `/admin/daily-mixes/{id}`（`templates/daily-mix.html`） | 封面拼图、主题类型与标签、简介；播放全部/随机/加入队列（复用歌单页的曲目列表）；探索曲目标“新发现”；每首“不再推荐/已不再推荐”；喜欢（再点撤销）、不喜欢（可多选原因，再点撤销）、换一个（计入每天次数，跳到新歌单）、另存为歌单（跳到新歌单页）；今天的其他推荐；往日或已替换的歌单只读（无“换一个”） |
| 管理页 | `/admin/daily-mixes` | 歌单标题链接到歌单页 |
| 生成调整 | `internal/dailymix` | 泛风格（流行、电子）不再参与情绪×风格组合；“熟悉开场”优先选情绪相符的歌，只对上风格的排在最后 |

**验证**：HTTP 测试覆盖主页生成与布局（今日推荐 + 最近播放、无接着听）、歌单页各状态、评分/撤销、另存、不再推荐返回、换一个计次与替换后只读、管理页链接。

**下一步（P5）**：App 主页横幅、详情与反馈；`/api/v1/home` 增加 `dailyMixes` 区块。
