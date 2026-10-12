# 作品接口（API v1）

本文档目前只覆盖作品详情页需要的只读接口，以及 `GET /api/v1/works/{id}` 新增的字段。

## 鉴权与能力位

| 接口 | 鉴权 |
|---|---|
| `GET /api/v1/works`、`/api/v1/works/{id}`、`/api/v1/works/{id}/albums`、`/api/v1/works/{id}/tracks` | 与其他 `/api/v1` 只读接口相同：`Authorization: Bearer <API Token>` 或管理员会话 cookie。无凭据、错误的 Bearer 返回 401 `unauthorized` |
| `GET /api/v1/works/{id}/poster` | 媒体鉴权：`?mediaToken=`、`Authorization: Bearer <Media Token>` 或管理员会话。**不接受** API Token |

`GET /api/v1/capabilities` 的 `features`：

- `works`：作品接口可用。
- `workPosters`：`/api/v1/works/{id}/poster` 可用。
- `workSeriesInfo`（新增）：`GET /api/v1/works/{id}` 带下文的扩展字段。旧服务端没有这些字段，客户端应以此能力位判断，不要把缺字段当作“不属于系列”。

## GET /api/v1/works/{id}

返回 Work 对象，并在它的基础上**新增**以下字段。原有字段的名称和含义都不变。`POST`、`PATCH /api/v1/works/{id}` 和 `GET /api/v1/works` 列表的响应也不变。

| 字段 | 类型 | 含义 |
|---|---|---|
| `posterApiUrl` | string | 本地缓存海报的路由 `/api/v1/works/{id}/poster?v=…`，客户端自行补 `mediaToken` 和 `size`（网页详情页用 `size=768`）；海报还没缓存时为空字符串。原有的 `posterUrl` 仍是作品存储的原始海报地址（通常是 Bangumi 远程地址），供编辑回写，不用于显示 |
| `bangumiSubjectId` | string | 已对齐的 Bangumi 条目 ID（链接为 `https://bgm.tv/subject/{id}`），未对齐时为空字符串 |
| `series` | object \| null | 所属系列，不属于任何系列时为 `null` |
| `series.id` | int | 系列 ID，用于 `GET /api/v1/series/{id}` 获取同系列的其他作品 |
| `series.title` | string | 系列名 |
| `series.titleSource` | `"auto"` \| `"manual"` | 名字来源，见 [series.md](series.md) |
| `series.memberCount` | int | 系列成员数 |
| `series.position` | int | 本作品在系列成员顺序中的位置，从 1 开始（网页的“当前：第 N 部”） |
| `series.source` | `"auto"` \| `"manual"` | 本作品是自动归组还是手动加入 |
| `seriesLocked` | bool | 已从系列中拆出：自动归组不会再把它加回来（网页的“已拆出”） |

作品不存在返回 404 `not_found`。

```json
{
  "id": 15,
  "title": "Fate/Zero",
  "readingTitle": "",
  "translatedTitle": "",
  "type": "anime",
  "typeLocked": false,
  "year": 2011,
  "posterUrl": "https://lain.bgm.tv/pic/cover/l/xx/yy/10380_abc.jpg",
  "externalId": "",
  "trackCount": 9,
  "createdAt": "2026-09-01T10:00:00.000Z",
  "updatedAt": "2026-10-01T08:00:00.000Z",
  "posterApiUrl": "/api/v1/works/15/poster?v=2026-10-01T08%3A00%3A00.000Z",
  "bangumiSubjectId": "10380",
  "series": { "id": 7, "title": "Fate系列", "titleSource": "manual", "memberCount": 4, "position": 2, "source": "manual" },
  "seriesLocked": false
}
```

## 作品详情页的数据拼法

网页 `/admin/works/{id}` 展示的信息都可以由以下三个接口得到：

| 网页区块 | 数据来源 |
|---|---|
| 标题、译名、假名、类型、年份、海报、Bangumi 链接 | `GET /api/v1/works/{id}` |
| “关联 N 张专辑” | `GET /api/v1/works/{id}/albums` 的条数 |
| “N 首关联曲目” | `trackCount` |
| 系列条（系列名、第几部、展开后的同系列作品） | `series` + `GET /api/v1/series/{series.id}` 的 `members` |
| 关联专辑卡片（关系标注） | `GET /api/v1/works/{id}/albums`：`albumRole` 是专辑级关系（`ost` / `other`），`trackRoles` 是该专辑中本作品曲目级用途的集合（已按 op、ed、insert… 的优先级排好） |
| 专辑下的曲目级用途（如“OP1”“ED2”） | `GET /api/v1/works/{id}/tracks` 中 `albumId` 属于关联专辑、且 `source != "album"` 的条目；标注为 `role` + `sequence`（`sequence` 为 0 时不显示数字） |
| “收录于精选集 / 原创专辑”曲目列表 | `GET /api/v1/works/{id}/tracks` 中 `albumId` **不**属于任何关联专辑的条目 |

`WorkTrack.source` 取值：`album`（由整张专辑关联派生）、`manual`（手动）、`bangumi`（Bangumi）、`auto`（自动推断）。
曲目用途 `role`：`op`、`ed`、`insert`、`theme`、`character`、`ost`、`image_song`、`other`。
