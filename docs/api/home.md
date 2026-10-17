# 主页接口（API v1）

网页主页 `/admin/home` 的只读 JSON 版本，供 032musicplayer、MusicBridge 等客户端按网页主页的设计渲染主页。
JSON 路由和 HTML 路由共用同一套数据构建（`homeData` / `homeRandomPane` / `homeWorksPanel` / `homeSongsPanel` / `homeChronicle`），读的是同一份按天冻结的轮换，所以两边拿到的数据完全一致。

`apiRevision` 仍为 3，本组接口全部是新增，现有接口不变。

## 鉴权与能力位

| 接口 | 鉴权 |
|---|---|
| `GET /api/v1/home`、`/api/v1/home/*`、`GET /api/v1/series/{id}/tracks` | 与其他 `/api/v1` 只读接口相同：`Authorization: Bearer <API Token>` 或管理员会话 cookie。无凭据、错误的 Bearer 返回 401 `unauthorized` |
| `GET /api/v1/works/{id}/poster` | 媒体鉴权，与 `/api/v1/artwork/{id}` 相同：`?mediaToken=`、`Authorization: Bearer <Media Token>` 或管理员会话。**不接受** API Token |

`GET /api/v1/capabilities` 新增：

```json
{
  "features": { "home": true, "seriesTracks": true, "workPosters": true },
  "home": {
    "endpoint": "/api/v1/home",
    "workFilters": ["all", "anime", "game", "movie"],
    "rotationZone": "UTC+08:00",
    "playTypes": ["album", "work", "series", "playlist", "track"]
  }
}
```

客户端以 `features.home` 探测本组接口是否可用，以 `features.seriesTracks` / `features.workPosters` 探测对应的新增接口。

## 通用约定

- **只有数据**：不返回 HTML，也不返回拼好的展示文案（如“入库于 …”“从 1:04 继续”“3 首未听完”、时长标签、规格标签、柱子高度）。客户端用返回的原始字段自行组织。
- **时间**：ISO 8601 UTC，例如 `2026-10-11T14:02:11.000Z`；空字符串表示没有（如从未播放）。
- **封面 / 媒体 URL**：沿用现有接口规则，返回服务器相对路径（`/api/v1/artwork/{id}`、`/api/v1/playlists/{id}/artwork?v=…`、`/api/v1/works/{id}/poster?v=…`），客户端自行补 `mediaToken` 和 `size`。
- **复用对象**：下列对象与现有接口逐字段相同，可以直接用现有类型和转换函数（`m032Normalize.album / song / playlist`）：

  | 对象 | 与之相同的现有接口 |
  |---|---|
  | `Album` | `GET /api/v1/albums/{id}` 的 `album`（含 `artists` 引用） |
  | `Track` | `GET /api/v1/tracks` 的条目 |
  | `Playlist` | `GET /api/v1/playlists` 的条目 |
  | `PlaybackRecord` | `GET /api/v1/playback/history` 的条目（内嵌完整 `track`） |
  | `Work` | `GET /api/v1/works/{id}` |
  | `AlbumWork` | `GET /api/v1/works/{id}/albums` 的条目 |
  | `WorkTrack` | `GET /api/v1/works/{id}/tracks` 的条目 |

- **可播放引用 `play`**：卡片上的 `play: { "type", "id" }` 指明怎么展开成曲目。`Work.type`（anime / game …）和 `Album.albumType` 已有含义，所以播放类型单独放在 `play` 里，不改动复用对象。

  | `play.type` | 展开方式 |
  |---|---|
  | `album` | `GET /api/v1/albums/{id}` → `tracks` |
  | `work` | `GET /api/v1/works/{id}/tracks` → `items` |
  | `series` | `GET /api/v1/series/{id}/tracks` → `items` |
  | `playlist` | `GET /api/v1/playlists/{id}` → `tracks` |
  | `track` | 直接播放该曲目 |

  同质列表的元素类型由所在字段决定，不再逐个携带 `play`：`recentAlbums`、`chronicle.result.albums` 是专辑，`playlists` 是歌单，`resume` 是曲目（续播位置为 `positionMillis`）。

- **错误**：沿用 `{"error": {"code", "message"}}`。查询失败 500 `query_failed`。

## GET /api/v1/home

首屏数据一次返回。只有统计查询失败才返回 500，其余区块查询失败时降级为空（与网页一致）。

| 字段 | 类型 | 含义 |
|---|---|---|
| `empty` | bool | 曲库没有专辑（首次运行）。为 true 时其他区块为空数组 / `null`，`stats` 照常返回 |
| `generatedAt` | string | 服务端生成时间 |
| `rotationDay` | string | 当前轮换日 `YYYY-MM-DD`（UTC+8） |
| `spotlight` | object \| null | 聚焦区；曲库为空时为 `null` |
| `spotlight.tabs` | string[] | 可用页签，按显示顺序：`recent`、`listening`（有未听完的歌曲时）、`series`（有可聚焦的系列或作品时）、`random` |
| `spotlight.defaultTab` | string | 默认页签，固定 `recent` |
| `spotlight.recent` / `listening` / `random` | AlbumPane \| null | 专辑类页签 |
| `spotlight.series` | SeriesPane \| null | 作品聚焦页签 |
| `resume` | PlaybackRecord[] | 接着听，最多 5 条，最近播放在前；进度 = `positionMillis / durationMillis` |
| `recentAlbums` | Album[] | 最近添加，按入库时间倒序 12 张 |
| `works` | RotationPage\<WorkCard\> | 按作品漫游，`type=all` 第 0 页 |
| `songs` | RotationPage\<SongRow\> | 主题曲与插曲第 0 页 |
| `chronicle` | object \| null | 编年；没有带年份的专辑时为 `null` |
| `chronicle.years` | `{year, count}[]` | 每年专辑数，年份升序（柱状图从左到右） |
| `chronicle.selectedYear` | int | 默认选中的年份（最新一年） |
| `chronicle.result` | Chronicle | 选中年份的专辑 |
| `playlists` | Playlist[] | 最多 20 个 |
| `stats.albums` / `stats.tracks` / `stats.works` | int | 专辑、歌曲、作品总数 |
| `stats.lastScanAt` | string | 上次扫描完成时间，没有为 `""` |
| `stats.pendingReviewCount` | int | 作品关联待审核数（网页页脚角标） |

### AlbumPane

| 字段 | 类型 | 含义 |
|---|---|---|
| `focus` | string | `recent` / `listening` / `random` |
| `play` | PlayRef | `{type: "album", id}` |
| `album` | Album | 聚焦的专辑 |
| `tracks` | Track[] | 预览曲目，最多 60 首；总数见 `album.trackCount` |
| `previewDurationMillis` | int | `tracks` 的总时长（网页显示的时长即此值） |
| `noteTime` | string | `recent`：入库时间；`listening`：上次播放时间；`random`：上次播放时间，从未播放为 `""` |
| `resume` | PlaybackRecord \| null | 仅 `listening`：要续播的曲目和断点 |

音频规格标签（如 `FLAC / 24bit / 96kHz`）由客户端从 `tracks[].codec / bitDepth / sampleRate` 汇总，网页同样基于这批预览曲目计算。

### SeriesPane

| 字段 | 类型 | 含义 |
|---|---|---|
| `isSeries` | bool | `false` 表示曲库没有可聚焦的系列，退回为“关联曲目最多的单部作品” |
| `seriesId` | int | 系列 id；非系列为 0 |
| `title` | string | 系列名；非系列为作品名 |
| `play` | PlayRef | 系列为 `{type: "series", id: seriesId}`，否则 `{type: "work", id}` |
| `workIds` | int[] | 聚焦卡上的作品（最多 6 部，网页“播放全部”按这些作品逐个取曲目） |
| `works` | `{work: Work, roleCounts: {role, count}[]}[]` | 成员作品及其各用途歌曲数 |
| `albums` | `{workId, workTitle, album: AlbumWork, play}[]` | 关联专辑（去重，最多 8 张） |
| `albumCount` | int | 关联专辑去重总数 |
| `trackTotal` | int | 各作品关联歌曲数之和 |
| `yearFrom` / `yearTo` | int | 作品年份范围，没有为 0 |

### RotationPage

| 字段 | 类型 | 含义 |
|---|---|---|
| `type` | string | 仅作品轮换：`all` / `anime` / `game` / `movie` |
| `generation` | string | 本页所属的轮换批次，如 `2026-10-11.k3x9a`；翻页时原样带回 |
| `batch` | int | 实际返回的页号（从 0 开始，超过最后一页回绕到 0） |
| `pages` | int | 总页数；`pages <= 1` 时不需要“换一批” |
| `count` | int | 作品数，或全部页的歌曲总数 |
| `renewed` | bool | 请求带的 `generation` 已过期，服务端改为返回新批次第 0 页（网页此时提示“今日推荐已更新”） |
| `items` | array | WorkCard[] 或 SongRow[] |

曲库为空时 `/api/v1/home` 里的 `generation` 为 `""`、`pages` 为 0、`items` 为 `[]`。

### WorkCard

| 字段 | 类型 | 含义 |
|---|---|---|
| `id` / `title` / `year` / `trackCount` | | 与 `Work` 同义 |
| `workType` | string | 作品类型（即 `Work.type`） |
| `posterUrl` | string | `/api/v1/works/{id}/poster?v=…`；本地尚未缓存海报时为 `""` |
| `coverUrl` | string | 作品第一张可见关联专辑的封面 `/api/v1/artwork/{id}`，没有为 `""` |
| `artworkUrl` | string | 应显示的封面：有 `posterUrl` 用它，否则 `coverUrl`（与网页规则一致） |
| `series` | `{id, title}` \| null | 所属系列 |
| `roleCounts` | `{role, count}[]` | 各用途（op / ed / insert …）歌曲数，按 OP、ED… 排序 |
| `play` | PlayRef | `{type: "work", id}` |

### SongRow

| 字段 | 类型 | 含义 |
|---|---|---|
| `track` | Track | 歌曲 |
| `role` | string | `op` / `ed` / `insert` / `theme` / `character` / `image_song` |
| `work` | `{id, title, year}` | 来源作品 |
| `play` | PlayRef | `{type: "track", id}` |

### Chronicle

`{ "year": int, "total": int, "albums": Album[] }`：该年专辑总数和按入库时间倒序的前 12 张。

### 示例

```json
{
  "empty": false,
  "generatedAt": "2026-10-12T03:15:00.123Z",
  "rotationDay": "2026-10-12",
  "spotlight": {
    "tabs": ["recent", "listening", "series", "random"],
    "defaultTab": "recent",
    "recent": {
      "focus": "recent",
      "play": { "type": "album", "id": 412 },
      "album": { "id": 412, "title": "BanG Dream! It's MyGO!!!!! OST", "artist": "MyGO!!!!!", "artists": [{ "id": 88, "name": "MyGO!!!!!" }], "artworkUrl": "/api/v1/artwork/903", "year": 2023, "discCount": 1, "trackCount": 24, "releaseKind": "soundtrack", "addedAt": "2026-10-11T14:02:11.000Z", "isFavorite": false },
      "tracks": [{ "id": 9001, "albumId": 412, "title": "春日影", "artist": "MyGO!!!!!", "durationMillis": 271000, "trackNumber": 1, "codec": "flac", "bitDepth": 24, "sampleRate": 96000, "streamUrl": "/api/v1/tracks/9001/stream", "artworkUrl": "/api/v1/artwork/903" }],
      "previewDurationMillis": 4820000,
      "noteTime": "2026-10-11T14:02:11.000Z",
      "resume": null
    },
    "listening": {
      "focus": "listening",
      "play": { "type": "album", "id": 120 },
      "album": { "id": 120, "title": "…" },
      "tracks": [],
      "previewDurationMillis": 3600000,
      "noteTime": "2026-10-12T01:00:00.000Z",
      "resume": { "trackId": 1503, "positionMillis": 95000, "durationMillis": 240000, "playCount": 3, "lastPlayedAt": "2026-10-12T01:00:00.000Z", "track": { "id": 1503, "title": "…" } }
    },
    "series": {
      "isSeries": true, "seriesId": 7, "title": "BanG Dream!",
      "play": { "type": "series", "id": 7 },
      "workIds": [31, 32],
      "works": [{ "work": { "id": 31, "title": "MyGO!!!!!", "type": "anime", "year": 2023, "trackCount": 40 }, "roleCounts": [{ "role": "op", "count": 1 }, { "role": "ed", "count": 1 }] }],
      "albums": [{ "workId": 31, "workTitle": "MyGO!!!!!", "album": { "albumId": 412, "workId": 31, "title": "…", "artworkUrl": "/api/v1/artwork/903", "trackCount": 24, "year": 2023 }, "play": { "type": "album", "id": 412 } }],
      "albumCount": 9, "trackTotal": 112, "yearFrom": 2017, "yearTo": 2025
    },
    "random": { "focus": "random", "play": { "type": "album", "id": 77 }, "album": { "id": 77 }, "tracks": [], "previewDurationMillis": 0, "noteTime": "", "resume": null }
  },
  "resume": [{ "trackId": 1503, "positionMillis": 95000, "durationMillis": 240000, "playCount": 3, "lastPlayedAt": "2026-10-12T01:00:00.000Z", "track": { "id": 1503 } }],
  "recentAlbums": [{ "id": 412 }],
  "works": {
    "type": "all", "generation": "2026-10-12.k3x9a", "batch": 0, "pages": 4, "count": 87, "renewed": false,
    "items": [{ "id": 31, "title": "MyGO!!!!!", "workType": "anime", "year": 2023, "trackCount": 40, "posterUrl": "/api/v1/works/31/poster?v=2026-09-01T00%3A00%3A00.000Z", "coverUrl": "/api/v1/artwork/903", "artworkUrl": "/api/v1/works/31/poster?v=2026-09-01T00%3A00%3A00.000Z", "series": { "id": 7, "title": "BanG Dream!" }, "roleCounts": [{ "role": "op", "count": 1 }], "play": { "type": "work", "id": 31 } }]
  },
  "songs": {
    "generation": "2026-10-12.k3x9a", "batch": 0, "pages": 6, "count": 112, "renewed": false,
    "items": [{ "track": { "id": 9001, "title": "春日影" }, "role": "op", "work": { "id": 31, "title": "MyGO!!!!!", "year": 2023 }, "play": { "type": "track", "id": 9001 } }]
  },
  "chronicle": {
    "years": [{ "year": 2019, "count": 12 }, { "year": 2023, "count": 40 }],
    "selectedYear": 2023,
    "result": { "year": 2023, "total": 40, "albums": [{ "id": 412 }] }
  },
  "playlists": [{ "id": 3, "name": "通勤", "description": "", "itemCount": 52, "durationMillis": 12345000, "artworkUrl": "/api/v1/playlists/3/artwork?v=ab12", "revision": 9, "hasCustomArtwork": false, "createdAt": "…", "updatedAt": "…" }],
  "stats": { "albums": 812, "tracks": 10342, "works": 87, "lastScanAt": "2026-10-11T20:00:00.000Z", "pendingReviewCount": 3 }
}
```

（示例中 `{ "id": … }` 处省略了完整对象的其余字段。）

首次运行（空库）：

```json
{
  "empty": true,
  "generatedAt": "2026-10-12T03:15:00.123Z",
  "rotationDay": "2026-10-12",
  "spotlight": null,
  "resume": [],
  "recentAlbums": [],
  "works": { "type": "all", "generation": "", "batch": 0, "pages": 0, "count": 0, "renewed": false, "items": [] },
  "songs": { "generation": "", "batch": 0, "pages": 0, "count": 0, "renewed": false, "items": [] },
  "chronicle": null,
  "playlists": [],
  "stats": { "albums": 0, "tracks": 0, "works": 0, "lastScanAt": "", "pendingReviewCount": 0 }
}
```

## GET /api/v1/home/spotlight/random

换一张。返回一个 AlbumPane（`focus: "random"`），页签列表不变，由客户端保留。

| 参数 | 说明 |
|---|---|
| `exclude` | 可选，要避开的专辑 id（当前显示的那张）；曲库只有这一张时仍返回它 |

曲库为空返回 404 `not_found`。随机选择不参与按天冻结，每次请求重新抽取。

## GET /api/v1/home/works

作品轮换的类型筛选与换一批。

| 参数 | 说明 |
|---|---|
| `type` | `all`（默认）/ `anime` / `game` / `movie` |
| `batch` | 非负整数页号，默认 0；超过最后一页回绕 |
| `generation` | 当前页的 `generation`（也接受网页路由使用的别名 `gen`）；首次请求可省略 |

返回 RotationPage\<WorkCard\>。`type` 或 `batch` 不合法返回 400 `invalid_parameters`。

## GET /api/v1/home/songs

主题曲与插曲的换一批。参数 `batch`、`generation`（或 `gen`）同上，返回 RotationPage\<SongRow\>，不合法返回 400 `invalid_parameters`。

## GET /api/v1/home/chronicle

切换编年年份。`year` 必填且为正整数，否则 400 `invalid_year`。返回 Chronicle；该年没有专辑时 `total: 0, albums: []`。

## 轮换语义

“按作品漫游”和“主题曲与插曲”每天轮换，规则与网页完全相同（同一份内存缓存）：

1. 每个视图（`works:all`、`works:anime`、`works:game`、`works:movie`、`songs`）在当天（UTC+8）第一次被请求时生成，之后整天冻结：候选池、播放权重和全部分页都固定，白天的播放、新入库、关联修改都不会打乱已经在翻的页。
2. 页面顺序是以“日期 + 视图名”为种子的加权随机排列：作品按关联歌曲数加权，未播放过的略提高、最近 7 天播放过的降低；“全部”的第 0 页为动画、游戏、电影各保留最多 3 个位置。连续翻页在走完全部候选之前不会重复，走完后回到第 0 页。
3. 歌曲轮换：作品顺序均匀随机，每部作品最多 2 首（OP / ED 优先，播放过的降低），同一首歌一天只出现一次，同一作品的歌不跨页。
4. `generation` = 轮换日 + 进程标识。跨天或服务重启后旧的 `generation` 失效：带旧值的请求返回新批次第 0 页，并且 `renewed: true`（同时带 `X-Home-Rotation: renewed` 响应头）。
5. 同一天、同一 `generation` 下，相同的 `type` + `batch` 在 HTML 路由（`/admin/home/works`、`/admin/home/songs`）和 JSON 路由上返回同一批数据、同样的顺序。页面数据在请求时补全，被删除或文件缺失的作品 / 歌曲会从该页剔除，因此一页可能少于每页条数（作品 24、歌曲 20）。

客户端建议：首屏用 `/api/v1/home` 的 `works` / `songs`，换一批时请求 `batch + 1` 并带上当前 `generation`；切换类型时请求 `batch=0`；收到 `renewed: true` 时以返回的新 `generation` 为准。

## 一键播放相关接口

### GET /api/v1/series/{id}/tracks

系列全部歌曲，返回 `{ "items": WorkTrack[] }`，结构与 `/api/v1/works/{id}/tracks` 相同。

- 成员作品按系列成员顺序（年份最早在前），每部作品内按 `/works/{id}/tracks` 的顺序；
- 同一首歌关联到多部成员作品时只保留第一次出现；
- 包含系列的全部成员作品。网页聚焦卡的“播放系列全部歌曲”只播卡片上最多 6 部作品，需要完全一致时改用 `spotlight.series.workIds` 逐个请求作品曲目；
- 系列不存在返回 404 `not_found`。

### GET /api/v1/works/{id}/poster

作品海报（媒体鉴权）。只读取本地缓存，不会代理到外部站点；未缓存或作品没有海报返回 404。支持 `size`（与 `/api/v1/artwork/{id}` 相同的缩略图尺寸）。`WorkCard.posterUrl` 即指向此路由。
