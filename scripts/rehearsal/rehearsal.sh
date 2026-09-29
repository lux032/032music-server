#!/usr/bin/env bash
# rehearsal.sh — 真实库副本演练（不访问外网）。
#
# 用法:
#   scripts/rehearsal/rehearsal.sh <副本数据目录> [--source <真实 music.db>] [--port 45490]
#
#   <副本数据目录>   演练使用的数据目录。推荐加 --source 让脚本自己完成复制
#                    （托管复制会先删除目标目录残留的 -wal/-shm；源库 wal
#                    非空时连同 -wal/-shm 一起复制）。不加 --source 时须自己
#                    提前放好 music.db。
#                    路径安全（H2）：副本目录经 pwd -P 解析后，统一小写与
#                    斜杠比较——与 source 所在目录相同、位于其下、或包含它
#                    时一律拒绝；落在 .local/data 下一律拒绝。
#   --source        可选但推荐。给出真实 music.db 路径：脚本先记录首尾各一次
#                   mtime+sha256（最终比对，证明真实库未被动过），再执行上述
#                   托管复制。对真实库只 stat/散列/读取,不打开写。该路径经
#                   REHEARSAL_SOURCE 传给 migrate/dbexec/dbquery 三个工具，
#                   工具侧再做一次 Abs+EvalSymlinks 级别的拒绝。
#   --port          临时端口,默认 45490。
#
# 流程:
#   1. 记录迁移前 schema_migrations 最大版本;
#   2. 关闭副本的 metadata source enabled/auto_match（只改副本），并回读
#      确认全部为 0，否则中止；存在本地未缓存的作品海报时中止（海报回填
#      会访问外网）;
#   3. 用项目自身的 storage.Migrate 升级副本,记录迁移后版本;
#   4. PRAGMA integrity_check / foreign_key_check,统计主要表行数;
#   5. 以副本数据目录后台启动服务(临时管理员凭据经环境变量注入,
#      MUSIC_SERVER_RESET_CREDENTIALS=all 只作用于副本),媒体库指向一个
#      新的空目录——依据: scanner 的 S1 守卫在“发现 0 个文件且库内已有
#      可用文件”时拒绝标记缺失; MarkMissing 只作用于当前配置的 library id;
#      CleanupOrphans 从不删除曲目(见 internal/scanner/scanner.go 与
#      internal/storage/library.go 注释),因此空目录扫描不会动副本数据;
#   6. 登录后逐个访问主要页面要求 200,核对导航角标与审核 Tab 计数之和,
#      截图 /admin/work-review 与 /admin/works 到 .local/mockups/impl47/;
#   7. 抓服务日志统计 ERROR/panic;结束服务进程并确认退出;
#   8. 再次记录 --source 的 mtime+sha256 并比对。
#
# set -euo pipefail：cp / 改设置 / 迁移等任何一步失败都会立即中止；trap
# 保证中止时也会 kill 服务并删除 cookies.txt。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd -P)"
cd "$ROOT"

COPY_DIR=""
SOURCE_DB=""
PORT=45490
while [ $# -gt 0 ]; do
  case "$1" in
    --source) SOURCE_DB="$2"; shift 2;;
    --port)   PORT="$2"; shift 2;;
    -h|--help) sed -n '1,44p' "$0"; exit 0;;
    *) if [ -z "$COPY_DIR" ]; then COPY_DIR="$1"; shift; else echo "unknown arg: $1" >&2; exit 2; fi;;
  esac
done

if [ -z "$COPY_DIR" ]; then
  echo "用法: scripts/rehearsal/rehearsal.sh <副本数据目录> [--source <真实 music.db>] [--port 45490]" >&2
  exit 2
fi
COPY_DIR="$(cd "$COPY_DIR" 2>/dev/null && pwd -P)" || { echo "副本目录不存在: $COPY_DIR" >&2; exit 2; }

norm() { printf '%s' "$1" | tr 'A-Z/' 'a-z\\'; }
N_COPY="$(norm "$COPY_DIR")"
# H2：副本目录不得落在真实数据目录下（大小写/斜杠不敏感）。
case "$N_COPY" in
  *\.local\\data|*\.local\\data\\*) echo "拒绝指向真实库目录 .local/data: $COPY_DIR" >&2; exit 2;;
esac

DB="$COPY_DIR/music.db"
JAR="$COPY_DIR/cookies.txt"
LOG="$COPY_DIR/server.log"
SERVER_PID=""
export REHEARSAL_SOURCE="$SOURCE_DB"

cleanup() {
  # 任何退出路径都要删掉 cookies；服务已启动时尽力结束它。
  rm -f "$JAR"
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    sleep 2
    if kill -0 "$SERVER_PID" 2>/dev/null; then
      # H2：taskkill 需要 Windows PID（/proc/<pid>/winpid），而不是 MSYS PID。
      WINPID="$(cat "/proc/$SERVER_PID/winpid" 2>/dev/null || true)"
      if [ -n "$WINPID" ]; then
        taskkill //PID "$WINPID" //F >/dev/null 2>&1 || true
      fi
    fi
  fi
}
trap cleanup EXIT

if [ -n "$SOURCE_DB" ]; then
  [ -f "$SOURCE_DB" ] || { echo "真实库不存在: $SOURCE_DB" >&2; exit 2; }
  # H2：副本目录与 source 所在目录相同、位于其下、或包含它时一律拒绝。
  SRC_DIR="$(cd "$(dirname "$SOURCE_DB")" 2>/dev/null && pwd -P)" || { echo "无法解析 source 目录: $SOURCE_DB" >&2; exit 2; }
  N_SRC="$(norm "$SRC_DIR")"
  if [ "$N_COPY" = "$N_SRC" ]; then
    echo "拒绝: 副本目录与 source 所在目录相同: $COPY_DIR" >&2
    exit 2
  fi
  case "$N_COPY" in
    "$N_SRC"\\*) echo "拒绝: 副本目录位于 source 所在目录之下: $COPY_DIR" >&2; exit 2;;
  esac
  case "$N_SRC" in
    "$N_COPY"\\*) echo "拒绝: 副本目录包含 source 所在目录: $COPY_DIR" >&2; exit 2;;
  esac
  SRC_MTIME_BEFORE=$(stat -c %y "$SOURCE_DB")
  SRC_SHA_BEFORE=$(sha256sum "$SOURCE_DB" | cut -d' ' -f1)
  echo "== 真实库迁移前 mtime: $SRC_MTIME_BEFORE"
  echo "== 真实库迁移前 sha256: $SRC_SHA_BEFORE"
  # 托管复制：先删掉目标目录里残留的 -wal/-shm（它们属于上一份副本，
  # 与新 music.db 混用会报 "database disk image is malformed"）；源库
  # wal 非空时必须连同 -wal/-shm 一起复制才是完整快照。
  rm -f "$COPY_DIR/music.db-wal" "$COPY_DIR/music.db-shm"
  cp "$SOURCE_DB" "$DB"
  if [ -s "$SOURCE_DB-wal" ]; then
    echo "== 源库 wal 非空，连同 -wal/-shm 一起复制"
    cp "$SOURCE_DB-wal" "$COPY_DIR/music.db-wal"
    if [ -f "$SOURCE_DB-shm" ]; then
      cp "$SOURCE_DB-shm" "$COPY_DIR/music.db-shm"
    fi
  fi
  echo "== 已复制真实库到副本（目标残留 -wal/-shm 已清理）"
fi
[ -f "$DB" ] || { echo "副本数据库不存在: $DB（请先手动复制，或加 --source 由脚本托管复制）" >&2; exit 2; }
command -v curl >/dev/null || { echo "需要 curl" >&2; exit 2; }

Q() { go run ./scripts/rehearsal/dbquery.go "$DB" "$1"; }
X() { go run ./scripts/rehearsal/dbexec.go "$DB" "$1"; }

echo "== 副本: $DB"

echo "== 迁移前 schema_migrations 最大版本:"
Q "SELECT COALESCE(MAX(version),0) FROM schema_migrations"

echo "== 关闭副本的 metadata source enabled/auto_match（只改副本）:"
X "UPDATE metadata_source_settings SET enabled=0, auto_match=0"
# H2：改完回读确认，全部为 0 才继续，否则中止且不启动服务。
REMAINING=$(Q "SELECT COUNT(*) FROM metadata_source_settings WHERE enabled<>0 OR auto_match<>0")
if [ "$REMAINING" != "0" ]; then
  echo "中止: 副本仍有 $REMAINING 个数据源处于 enabled/auto_match，拒绝启动服务" >&2
  exit 3
fi
echo "== 回读确认: 全部数据源 enabled=0 且 auto_match=0"

# H2：海报检查按本地缓存文件判定（<数据目录>/work-posters/<sha256(url)>.<ext>），
# 只看 poster_url 是否为空会误伤已缓存的库。
UNCACHED_POSTERS=0
POSTER_TOTAL=0
while IFS= read -r url; do
  [ -z "$url" ] && continue
  POSTER_TOTAL=$((POSTER_TOTAL+1))
  key="$(printf '%s' "$url" | sha256sum | cut -d' ' -f1)"
  if ! ls "$COPY_DIR/work-posters/$key".jpg "$COPY_DIR/work-posters/$key".png "$COPY_DIR/work-posters/$key".webp "$COPY_DIR/work-posters/$key".gif >/dev/null 2>&1; then
    UNCACHED_POSTERS=$((UNCACHED_POSTERS+1))
  fi
done <<EOF_POSTERS
$(Q "SELECT poster_url FROM works WHERE COALESCE(poster_url,'')<>''")
EOF_POSTERS
echo "== 带 poster_url 的作品: $POSTER_TOTAL，其中本地未缓存: $UNCACHED_POSTERS"
if [ "$UNCACHED_POSTERS" != "0" ]; then
  echo "中止: 存在 $UNCACHED_POSTERS 张未缓存的作品海报（启动时海报回填会访问外网）。请把 .local/data/work-posters 复制到副本，或将副本 works.poster_url 置空后重试。" >&2
  exit 3
fi

echo "== 运行项目自身 Migrate 升级副本:"
go run ./scripts/rehearsal/migrate.go "$DB"
echo "== 迁移后 schema_migrations 最大版本:"
Q "SELECT COALESCE(MAX(version),0) FROM schema_migrations"

echo "== PRAGMA integrity_check:"
Q "PRAGMA integrity_check"
echo "== PRAGMA foreign_key_check（无输出即通过）:"
Q "PRAGMA foreign_key_check"

echo "== 主要表行数:"
for t in libraries artists artist_names albums tracks audio_files audio_file_tags artworks artist_custom_images works work_aliases album_works work_tracks work_series work_series_members work_series_suggestions work_match_candidates album_subject_candidates track_subject_candidates enrichment_runs playlists scan_jobs admin_sessions; do
  EXISTS=$(Q "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='$t'")
  if [ "$EXISTS" = "1" ]; then
    printf '  %-32s %s\n' "$t" "$(Q "SELECT COUNT(*) FROM $t")"
  fi
done

# ---- 后台启动服务 ----
EMPTY_MUSIC="$COPY_DIR/_empty_music"
mkdir -p "$EMPTY_MUSIC"
BIN="$COPY_DIR/server-rehearsal.exe"
echo "== 构建服务二进制: $BIN"
go build -o "$BIN" ./cmd/server
: > "$LOG"
echo "== 后台启动服务: 127.0.0.1:$PORT（数据目录=$COPY_DIR, 媒体库=$EMPTY_MUSIC）"
MUSIC_SERVER_ADDRESS="127.0.0.1:$PORT" \
MUSIC_SERVER_DATA_DIR="$COPY_DIR" \
MUSIC_SERVER_DATABASE_PATH="$DB" \
MUSIC_SERVER_MUSIC_DIR="$EMPTY_MUSIC" \
MUSIC_SERVER_LIBRARY_NAME="Rehearsal" \
MUSIC_SERVER_ADMIN_USERNAME="admin" \
MUSIC_SERVER_ADMIN_PASSWORD="rehearsal-password-7f3c" \
MUSIC_SERVER_API_TOKEN="rehearsal-api-token-9d2c8f1ab4e6" \
MUSIC_SERVER_MEDIA_TOKEN="rehearsal-media-token-51be27c093d4" \
MUSIC_SERVER_RESET_CREDENTIALS="all" \
MUSIC_SERVER_LOG_LEVEL="info" \
"$BIN" > "$LOG" 2>&1 &
SERVER_PID=$!
echo "== 服务 PID: $SERVER_PID"

BASE="http://127.0.0.1:$PORT"
READY=0
for _ in $(seq 1 60); do
  if curl -sf -o /dev/null "$BASE/api/v1/health"; then
    READY=1
    break
  fi
  sleep 1
done
if [ "$READY" != "1" ]; then
  echo "服务未在 60s 内就绪" >&2
  exit 1
fi
echo "== 服务就绪"

LOGIN_CODE=$(curl -s -o /dev/null -w "%{http_code}" -c "$JAR" -d "username=admin&password=rehearsal-password-7f3c" "$BASE/admin/login")
echo "== 登录 POST /admin/login -> $LOGIN_CODE（期望 303）"
[ "$LOGIN_CODE" = "303" ] || { echo "登录失败" >&2; exit 1; }

ALBUM_IDS=$(Q "SELECT id FROM albums ORDER BY id LIMIT 3")
ARTIST_IDS=$(Q "SELECT id FROM artists WHERE merged_into_artist_id IS NULL ORDER BY id LIMIT 3")
WORK_IDS=$(Q "SELECT id FROM works ORDER BY id LIMIT 3")
SERIES_IDS=$(Q "SELECT id FROM work_series ORDER BY id LIMIT 1" 2>/dev/null || true)

PAGES="/ /admin /admin/albums /admin/artists /admin/tracks /admin/works /admin/works?type=anime /admin/favorites /admin/enrichment /admin/work-review /admin/work-review?tab=albums /admin/work-review?tab=tracks /admin/work-review?tab=works /admin/work-review?tab=series /admin/series /admin/merges /admin/settings/metadata /admin/settings/security"
for id in $ALBUM_IDS; do PAGES="$PAGES /admin/albums/$id"; done
for id in $ARTIST_IDS; do PAGES="$PAGES /admin/artists/$id"; done
for id in $WORK_IDS; do PAGES="$PAGES /admin/works/$id"; done
for id in $SERIES_IDS; do PAGES="$PAGES /admin/series/$id"; done

echo "== 页面冒烟（要求全部 200）:"
FAIL=0
: > "$COPY_DIR/pages.txt"
for p in $PAGES; do
  CODE=$(curl -s -o /dev/null -w "%{http_code}" -b "$JAR" -L "$BASE$p")
  printf '  %-40s %s\n' "$p" "$CODE" | tee -a "$COPY_DIR/pages.txt"
  [ "$CODE" = "200" ] || FAIL=1
done
[ "$FAIL" = "0" ] || { echo "存在非 200 页面" >&2; exit 1; }

echo "== 导航角标 vs 审核 Tab 计数:"
REVIEW_HTML=$(curl -s -b "$JAR" "$BASE/admin/work-review")
NAV_BADGE=$(printf '%s' "$REVIEW_HTML" | grep -o 'nav-badge[^>]*>[0-9]\+' | grep -o '[0-9]\+' | head -1 || true)
TAB_SUM=$(printf '%s' "$REVIEW_HTML" | grep -o 'review-tab-badge[^>]*>[0-9]\+' | grep -o '[0-9]\+' | awk '{s+=$1} END {print s+0}' || true)
echo "  角标=${NAV_BADGE:-0}  四个 Tab 之和=${TAB_SUM:-0}"
if [ "${NAV_BADGE:-0}" != "${TAB_SUM:-0}" ]; then
  echo "角标与 Tab 计数不一致" >&2
  exit 1
fi

echo "== 截图到 .local/mockups/impl47/:"
REHEARSAL_BASE="$BASE" REHEARSAL_USER=admin REHEARSAL_PASS="rehearsal-password-7f3c" \
  node "$ROOT/scripts/rehearsal/screenshots.mjs"

echo "== 日志检查（ERROR / panic 计数）:"
ERRORS=$(grep -c '"level":"ERROR"' "$LOG" || true)
PANICS=$(grep -ci 'panic' "$LOG" || true)
echo "  ERROR=$ERRORS panic=$PANICS（日志: $LOG）"
grep '"level":"ERROR"' "$LOG" | head -5 || true
if [ "$ERRORS" != "0" ] || [ "$PANICS" != "0" ]; then
  echo "日志存在 ERROR/panic" >&2
  exit 1
fi

echo "== 结束服务进程 $SERVER_PID"
cleanup
sleep 1
if kill -0 "$SERVER_PID" 2>/dev/null; then
  echo "服务进程未能结束" >&2
  exit 1
fi
SERVER_PID=""
echo "== 服务进程已退出"

if [ -n "$SOURCE_DB" ]; then
  SRC_MTIME_AFTER=$(stat -c %y "$SOURCE_DB")
  SRC_SHA_AFTER=$(sha256sum "$SOURCE_DB" | cut -d' ' -f1)
  echo "== 真实库演练后 mtime: $SRC_MTIME_AFTER"
  echo "== 真实库演练后 sha256: $SRC_SHA_AFTER"
  if [ "$SRC_MTIME_BEFORE" != "$SRC_MTIME_AFTER" ] || [ "$SRC_SHA_BEFORE" != "$SRC_SHA_AFTER" ]; then
    echo "真实库被改动过！" >&2
    exit 1
  fi
  echo "== 真实库 mtime/sha256 前后一致 ✓"
fi
echo "== 演练完成"
