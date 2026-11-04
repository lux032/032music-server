# Last.fm 红心同步：改动前后对照

同一场景、同一视口、同一数据（已连接 Last.fm 账号 `listener`，曲库有 3 首收藏），
用 Playwright 对真实实例截图。基线是当前 `main`（`6cd0ca1`），改动后是 PR 分支。

| 文件 | 说明 |
| --- | --- |
| `before-settings-desktop.png` | 基线（1800×1000 宽屏）：`/admin/settings/metadata` 的 Last.fm 区块只有 Scrobble 卡片。 |
| `after-settings-desktop.png` | 改动后同一区块、同一视口：Scrobble 卡片下方新增「Last.fm 红心」卡片（开关 + 手动回填）。 |
| `before-settings-mobile.png` | 基线 375×760 移动端。 |
| `after-settings-mobile.png` | 改动后 375×760 移动端，红心卡片在窄屏下的换行与按钮位置。 |
| `after-backfill-running.png` | 回填进行中（1280×900）：进度条 33.3%，按钮变为「停止回填」。 |
| `after-backfill-complete.png` | 回填完成：进度条 100%，页面保持打开时由轮询自动更新（未刷新）。 |
