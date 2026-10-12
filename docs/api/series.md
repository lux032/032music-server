# 系列接口（API v1）

网页系列管理页 `/admin/series`、`/admin/series/{id}` 的只读 JSON 版本，供 032musicplayer 等客户端在客户端内渲染系列列表页和系列页。
JSON 路由和 HTML 路由共用同一套数据查询（`seriesListEntries` / `seriesWithMembers`），搜索、排序、成员顺序与网页完全一致。

`apiRevision` 仍为 3，本组接口全部是新增，现有接口不变。

## 鉴权与能力位

| 接口 | 鉴权 |
|---|---|
| `GET /api/v1/series`、`/api/v1/series/{id}`、`/api/v1/series/{id}/albums`、`/api/v1/series/{id}/tracks` | 与其他 `/api/v1` 只读接口相同：`Authorization: Bearer <API Token>` 或管理员会话 cookie。无凭据、错误的 Bearer 返回 401 `unauthorized` |
| `GET /api/v1/works/{id}/poster`（海报图片） | 媒体鉴权：`?mediaToken=`、`Authorization: Bearer <Media Token>` 或管理员会话。**不接受** API Token |

`GET /api/v1/capabilities` 的 `features` 新增：

```json
{ "features": { "series": true, "seriesTracks": true } }
```

- `features.series`：本文档的列表、详情、专辑接口可用。
- `features.seriesTracks`：`/api/v1/series/{id}/tracks` 可用（较早加入，见 [home.md](home.md)）。

## 通用约定

- **海报 URL**：`posterUrl` 是服务器相对路径 `/api/v1/works/{workId}/poster?v=…`，与主页 `WorkCard.posterUrl` 规则相同，客户端自行补 `mediaToken` 和 `size`（网页列表用 `size=360`）。海报还没缓存到本地时为空字符串，客户端显示占位图。
  注意：`GET /api/v1/works/{id}` 的 `posterUrl` 是作品存储的原始海报地址，同一路由形式的地址在那里叫 `posterApiUrl`，见 [works.md](works.md)。
- **作品类型** `workType` / `type`：`anime`、`drama`、`movie`、`game`、`commercial`、`other`。显示名由客户端映射（网页：动画、电视剧、电影、游戏、广告、其他）。
- **可播放引用 `play`**：与主页接口相同，`series` → `/api/v1/series/{id}/tracks`，`work` → `/api/v1/works/{id}/tracks`，`album` → `/api/v1/albums/{id}`。
- **错误**：`{"error": {"code", "message"}}`。系列不存在（包括 id 不是数字）返回 404 `not_found`，查询失败返回 500 `query_failed`。

## Series 对象

列表条目和详情里的 `series` 是同一个对象。

| 字段 | 类型 | 含义 |
|---|---|---|
| `id` | int | 系列 ID |
| `title` | string | 系列名 |
| `titleSource` | `"auto"` \| `"manual"` | `manual`：用户改过名，自动流程不会覆盖（网页的“已改名”标记）；`auto`：名字跟随代表作 |
| `memberCount` | int | 成员作品数 |
| `typeCounts` | `{type, count}[]` | 成员类型分布，数量降序、同量按类型名，与网页“动画 3 · 游戏 1”的顺序相同；空系列为 `[]` |
| `representativeWorkId` | int | 代表作 ID（成员中最早的一部），没有时为 0 |
| `posterUrl` | string | 代表作海报，见通用约定 |
| `createdAt` / `updatedAt` | string | ISO 8601 UTC |

## GET /api/v1/series

系列列表，等同网页 `/admin/series`。

| 参数 | 默认 | 说明 |
|---|---|---|
| `q` | 空 | 搜索：匹配系列名，或任一成员作品的原名、假名、译名（子串匹配） |
| `limit` | 50 | 每页条数，1–200；超出范围按 50 处理，响应里的 `limit` 是实际生效值 |
| `offset` | 0 | 偏移 |

排序固定为系列名（不区分大小写）、再按 id，与网页一致。网页没有排序和筛选选项，本接口也不提供。

响应与 `/api/v1/works` 的分页格式相同：

```json
{
  "items": [
    {
      "id": 7,
      "title": "Fate系列",
      "titleSource": "manual",
      "memberCount": 4,
      "typeCounts": [{ "type": "anime", "count": 3 }, { "type": "game", "count": 1 }],
      "representativeWorkId": 12,
      "posterUrl": "/api/v1/works/12/poster?v=2026-10-01T08%3A00%3A00.000Z",
      "createdAt": "2026-09-01T10:00:00.000Z",
      "updatedAt": "2026-10-01T08:00:00.000Z"
    }
  ],
  "total": 31,
  "limit": 50,
  "offset": 0
}
```

没有结果时 `items` 为 `[]`。

## GET /api/v1/series/{id}

系列详情，等同网页 `/admin/series/{id}` 的展示内容，另加统计。

| 字段 | 类型 | 含义 |
|---|---|---|
| `series` | Series | 系列本身 |
| `members` | Member[] | 成员作品，顺序与网页相同（见下） |
| `stats.memberCount` | int | 成员数 |
| `stats.yearFrom` / `stats.yearTo` | int | 成员的最早 / 最晚年份；都没有年份时为 0 |
| `stats.albumCount` | int | 成员关联专辑去重后的数量，等于 `/series/{id}/albums` 的条数 |
| `stats.trackCount` | int | 成员关联曲目去重后的数量，等于 `/series/{id}/tracks` 的条数 |
| `play` | `{type:"series", id}` | 播放整个系列 |

Member（字段与主页 `WorkCard` 对齐）：

| 字段 | 类型 | 含义 |
|---|---|---|
| `id` | int | 作品 ID |
| `title` / `readingTitle` / `translatedTitle` | string | 原名 / 假名或罗马音 / 译名，没有时为空字符串 |
| `workType` | string | 作品类型 |
| `year` | int | 年份，未知为 0 |
| `trackCount` | int | 该作品关联的可见曲目数 |
| `posterUrl` | string | 作品海报，见通用约定 |
| `source` | `"auto"` \| `"manual"` | 自动归组 / 手动加入 |
| `play` | `{type:"work", id}` | 播放该作品 |

**成员顺序**：最早在前——先按年份（无年份排最后），再按 Bangumi 放送日期，再按作品 id。这也是代表作的选取规则。

**按类型分组**（网页的显示方式，客户端自行实现）：按成员顺序遍历，每种 `workType` 第一次出现时开一个组，组内保持成员顺序；只有一种类型时不显示组头。

```json
{
  "series": {
    "id": 7, "title": "Fate系列", "titleSource": "manual", "memberCount": 2,
    "typeCounts": [{ "type": "anime", "count": 1 }, { "type": "game", "count": 1 }],
    "representativeWorkId": 12,
    "posterUrl": "/api/v1/works/12/poster?v=2026-10-01T08%3A00%3A00.000Z",
    "createdAt": "2026-09-01T10:00:00.000Z", "updatedAt": "2026-10-01T08:00:00.000Z"
  },
  "members": [
    {
      "id": 12, "title": "Fate/stay night", "readingTitle": "", "translatedTitle": "命运守护夜",
      "workType": "game", "year": 2004, "trackCount": 18,
      "posterUrl": "/api/v1/works/12/poster?v=2026-10-01T08%3A00%3A00.000Z",
      "source": "auto", "play": { "type": "work", "id": 12 }
    },
    {
      "id": 15, "title": "Fate/Zero", "readingTitle": "", "translatedTitle": "",
      "workType": "anime", "year": 2011, "trackCount": 9, "posterUrl": "",
      "source": "manual", "play": { "type": "work", "id": 15 }
    }
  ],
  "stats": { "memberCount": 2, "yearFrom": 2004, "yearTo": 2011, "albumCount": 5, "trackCount": 26 },
  "play": { "type": "series", "id": 7 }
}
```

空系列（没有成员作品）照常返回 200：

```json
{
  "series": { "id": 9, "title": "…", "titleSource": "manual", "memberCount": 0, "typeCounts": [],
              "representativeWorkId": 0, "posterUrl": "", "createdAt": "…", "updatedAt": "…" },
  "members": [],
  "stats": { "memberCount": 0, "yearFrom": 0, "yearTo": 0, "albumCount": 0, "trackCount": 0 },
  "play": { "type": "series", "id": 9 }
}
```

## GET /api/v1/series/{id}/albums

系列下的全部专辑：按成员顺序，每个成员内按 `/api/v1/works/{id}/albums` 的顺序；同一张专辑关联多个成员时只保留第一次出现（归在较早的成员下）。不截断（主页聚焦区最多只显示 8 张）。

元素与主页 `spotlight.series.albums[]` 相同，`album` 与 `/api/v1/works/{id}/albums` 的条目逐字段相同：

```json
{
  "items": [
    {
      "workId": 12,
      "workTitle": "Fate/stay night",
      "album": {
        "albumId": 301, "workId": 12, "title": "Fate/stay night Original Soundtrack",
        "role": "ost", "source": "auto", "season": 0, "artist": "…", "year": 2004,
        "albumType": "album", "discCount": 1, "trackCount": 30,
        "artworkUrl": "/api/v1/artwork/88", "albumRole": "ost"
      },
      "play": { "type": "album", "id": 301 }
    }
  ]
}
```

空系列返回 `{"items": []}`。

## GET /api/v1/series/{id}/tracks

系列全部曲目，`{items: WorkTrack[]}`，元素与 `/api/v1/works/{id}/tracks` 相同：成员按上述顺序，每个成员内按作品曲目顺序，同一首曲目只保留第一次出现。
