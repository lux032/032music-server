# 真实库副本演练（rehearsal）

在不访问外网、不碰真实库的前提下，用 `.local/data/music.db` 的**副本**验证：
迁移升级、完整性检查、主要页面冒烟、导航角标一致性、服务日志无 ERROR/panic。

## 用法

```bash
# 1. 准备副本目录（artwork/artist-images/work-posters 缓存可选但推荐，
#    页面才能显示封面；存在未缓存海报时脚本会中止，见下）
mkdir -p .local/tmp/rehearsal
cp -r .local/data/artwork .local/data/artist-images .local/tmp/rehearsal/

# 2. 运行演练：--source 让脚本托管复制（先删除目标残留的 -wal/-shm；
#    源库 wal 非空时连同 -wal/-shm 一起复制），并在首尾比对真实库
#    mtime+sha256（只读，绝不写入）
scripts/rehearsal/rehearsal.sh .local/tmp/rehearsal --source .local/data/music.db
```

## 安全约束（H2）

- **路径拒绝**：副本目录经 `pwd -P` 解析后统一小写与斜杠比较——落在
  `.local/data` 下、与 source 所在目录相同、位于 source 所在目录之下、
  或包含 source 所在目录，一律拒绝。`migrate.go` / `dbexec.go` /
  `dbquery.go` 三个工具再用 `scripts/rehearsal/guard`（Abs +
  EvalSymlinks、大小写不敏感）拒绝 `.local/data` 与 `REHEARSAL_SOURCE`
  本身；守卫逻辑有单元测试（`scripts/rehearsal/guard`）。
- **失败即停**：脚本 `set -euo pipefail`，cp / 改设置 / 迁移等任何一步
  失败立即中止；trap 在任何退出路径上都会 kill 服务并删除 cookies.txt。
- **外部请求**：副本内 `metadata_source_settings` 的 `enabled/auto_match`
  全部置 0（只改副本）并**回读确认**，不为 0 即中止、不启动服务；作品
  海报按本地缓存文件判定（`<副本>/work-posters/<sha256(url)>.<ext>`），
  存在未缓存海报即中止——把 `.local/data/work-posters` 复制到副本即可
  通过（只复制 artwork 缓存不够，work-posters 是独立目录）。
- 媒体库指向副本下的空目录 `_empty_music`：scanner 的 S1 守卫在"发现 0 个
  文件但库内已有可用文件"时拒绝标记缺失，`MarkMissing` 只作用于当前配置
  的 library id，`CleanupOrphans` 从不删除曲目，因此不会动副本数据。
- 临时管理员凭据通过环境变量 + `MUSIC_SERVER_RESET_CREDENTIALS=all` 注入，
  只作用于副本。
- 服务在后台运行（默认端口 45490）；结束时 kill 并复核存活（需要时用
  `/proc/<pid>/winpid` 取 Windows PID 交给 taskkill），日志保存在
  `<副本目录>/server.log`。

## 产物

- `<副本目录>/server.log`、`pages.txt`
- `.local/mockups/impl47/rehearsal-work-review.png`、`rehearsal-works.png`
