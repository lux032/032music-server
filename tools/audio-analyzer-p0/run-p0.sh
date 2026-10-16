#!/bin/sh
# 在 NAS 上运行声学分析 P0 验证。
# 用法：MUSIC=/你的/音乐目录 ./run-p0.sh [额外参数，例如 --sample 300]
# 只读挂载音乐目录；结果写到 ./p0-out，模型缓存在 ./p0-models。
#
# 代理（可选，地址只从环境读取，脚本不写死、不打印）：
#   HTTP_PROXY / HTTPS_PROXY / NO_PROXY（或小写）  普通代理，通过 Docker 预定义构建参数传入
#   PROXY_FILE=/path/file                            文件第一行为代理 URL（可含凭证），走 BuildKit secret
#   PROXY_MODE=auto|args|secret|off                  默认 auto：有 PROXY_FILE 或 URL 含凭证时用 secret，否则用 args
#   RUNTIME_PROXY=auto|1|0                           运行期（首次下载模型）是否使用代理，默认 auto=模型未缓存时使用
# 其他构建选项：INSTALL_GPU=1  APT_MIRROR=https://...  PIP_INDEX_URL=https://...  APT_FORCE_IPV4=1  BUILD_NETWORK=host  BUILD_NO_CACHE=1
set -eu

MUSIC="${MUSIC:?请设置 MUSIC=音乐目录（与 032 的 MUSIC_SERVER_MUSIC_PATH 相同）}"
HERE="$(cd "$(dirname "$0")" && pwd)"
OUT="${OUT:-$HERE/p0-out}"
MODELS="${MODELS:-$HERE/p0-models}"
IMAGE="${IMAGE:-032music-audio-p0}"
CPUS="${CPUS:-2}"
PROXY_MODE="${PROXY_MODE:-auto}"
PROXY_FILE="${PROXY_FILE:-}"
RUNTIME_PROXY="${RUNTIME_PROXY:-auto}"

mkdir -p "$OUT" "$MODELS"

# ---- 代理解析（大小写都接受；只配了一个时两者共用）----
p_http="${HTTP_PROXY:-${http_proxy:-}}"
p_https="${HTTPS_PROXY:-${https_proxy:-}}"
p_none="${NO_PROXY:-${no_proxy:-}}"
[ -n "$p_http" ] || p_http="$p_https"
[ -n "$p_https" ] || p_https="$p_http"

if [ -n "$PROXY_FILE" ] && [ ! -r "$PROXY_FILE" ]; then
  echo "[p0] PROXY_FILE 不可读：$PROXY_FILE" >&2
  exit 2
fi

case "$PROXY_MODE" in
  auto)
    if [ -n "$PROXY_FILE" ]; then
      mode=secret
    elif [ -n "$p_http" ]; then
      case "$p_http$p_https" in
        *@*) mode=secret ;;  # URL 含凭证
        *) mode=args ;;
      esac
    else
      mode=off
    fi
    ;;
  args)
    if [ -z "$p_http" ]; then
      echo "[p0] PROXY_MODE=args 但没有设置 HTTP_PROXY/HTTPS_PROXY" >&2
      exit 2
    fi
    mode=args
    ;;
  secret)
    if [ -z "$PROXY_FILE" ] && [ -z "$p_http" ]; then
      echo "[p0] PROXY_MODE=secret 需要 PROXY_FILE 或 HTTP_PROXY/HTTPS_PROXY" >&2
      exit 2
    fi
    mode=secret
    ;;
  off) mode=off ;;
  *)
    echo "[p0] 未知 PROXY_MODE：$PROXY_MODE（可选 auto|args|secret|off）" >&2
    exit 2
    ;;
esac

# secret 模式下若只给了环境变量，就写入一个仅当前用户可读的临时文件，结束时删除
secret_file="$PROXY_FILE"
tmp_secret=""
cleanup() { [ -n "$tmp_secret" ] && rm -f "$tmp_secret"; }
trap cleanup EXIT
trap 'cleanup; exit 130' INT TERM
if [ "$mode" = secret ] && [ -z "$secret_file" ]; then
  tmp_secret="$(umask 077 && mktemp "${TMPDIR:-/tmp}/p0-proxy.XXXXXX")"
  printf '%s\n' "$p_https" > "$tmp_secret"
  secret_file="$tmp_secret"
fi

if [ "$mode" = args ]; then
  # 预定义构建参数只传名字、不传值：值由 docker CLI 从环境读取，不会出现在 ps 中
  export HTTP_PROXY="$p_http" http_proxy="$p_http" HTTPS_PROXY="$p_https" https_proxy="$p_https"
  if [ -n "$p_none" ]; then
    export NO_PROXY="$p_none" no_proxy="$p_none"
  fi
fi

case "$mode" in
  off) echo "[p0] 代理：未使用（直连）" ;;
  args) echo "[p0] 代理：已启用（Docker 预定义构建参数；地址不打印）" ;;
  secret) echo "[p0] 代理：已启用（BuildKit secret；地址与凭证不打印）" ;;
esac

build_image() {
  set -- build -t "$IMAGE"
  if [ "$mode" = secret ]; then
    set -- "$@" -f "$HERE/Dockerfile.buildkit" --secret "id=build_proxy,src=$secret_file"
  else
    set -- "$@" -f "$HERE/Dockerfile"
  fi
  if [ "$mode" = args ]; then
    set -- "$@" --build-arg HTTP_PROXY --build-arg HTTPS_PROXY --build-arg http_proxy --build-arg https_proxy
  fi
  if [ "$mode" != off ] && [ -n "$p_none" ]; then
    export NO_PROXY="$p_none" no_proxy="$p_none"
    set -- "$@" --build-arg NO_PROXY --build-arg no_proxy
  fi
  [ -z "${INSTALL_GPU:-}" ] || set -- "$@" --build-arg "INSTALL_GPU=$INSTALL_GPU"
  [ -z "${APT_MIRROR:-}" ] || set -- "$@" --build-arg "APT_MIRROR=$APT_MIRROR"
  [ -z "${APT_FORCE_IPV4:-}" ] || set -- "$@" --build-arg "APT_FORCE_IPV4=$APT_FORCE_IPV4"
  [ -z "${PIP_INDEX_URL:-}" ] || set -- "$@" --build-arg "PIP_INDEX_URL=$PIP_INDEX_URL"
  [ -z "${BUILD_NETWORK:-}" ] || set -- "$@" --network "$BUILD_NETWORK"
  [ "${BUILD_NO_CACHE:-0}" != "1" ] || set -- "$@" --no-cache
  set -- "$@" "$HERE"
  if [ "$mode" = secret ]; then
    DOCKER_BUILDKIT=1 docker "$@" || {
      echo "[p0] BuildKit 构建失败。secret 模式需要 BuildKit：Docker 23+ 需安装 buildx 组件，Docker 20.10 需 DOCKER_BUILDKIT=1。" >&2
      return 1
    }
  else
    docker "$@"
  fi
}

if [ "${SKIP_BUILD:-0}" != "1" ]; then
  build_image
fi

# ---- 运行期：只有首次下载模型需要联网 ----
use_runtime_proxy=0
if [ "$mode" != off ]; then
  case "$RUNTIME_PROXY" in
    1) use_runtime_proxy=1 ;;
    0) use_runtime_proxy=0 ;;
    *) [ -s "$MODELS/discogs-effnet-bsdynamic-1.onnx" ] || use_runtime_proxy=1 ;;
  esac
fi

# 参数是传给 bench.py 的用户参数；先追加 docker 选项，再把用户参数轮转到末尾（保留引号/空格）
run_container() {
  n_user=$#
  set -- "$@" --rm --name 032music-audio-p0 --cpus "$CPUS" --memory 2g
  if [ -e /dev/dri ] && [ "${USE_GPU:-0}" = "1" ]; then
    set -- "$@" --device /dev/dri
  fi
  if [ "$use_runtime_proxy" = 1 ]; then
    if [ "$mode" = secret ]; then
      # 以只读文件挂载，避免凭证出现在容器环境变量（docker inspect）中
      set -- "$@" -v "$secret_file:/run/secrets/proxy_url:ro"
    else
      set -- "$@" -e HTTP_PROXY -e HTTPS_PROXY -e http_proxy -e https_proxy
    fi
    if [ -n "$p_none" ]; then
      export NO_PROXY="$p_none" no_proxy="$p_none"
      set -- "$@" -e NO_PROXY -e no_proxy
    fi
    echo "[p0] 运行期代理：已启用（用于首次下载模型）"
  fi
  # --cpus 限制为 2 核，避免影响 032 的实时转码；内存上限 2GB；音乐目录只读。
  set -- "$@" -v "$MUSIC:/music:ro" -v "$OUT:/out" -v "$MODELS:/models" "$IMAGE" --threads "$CPUS"
  i=0
  while [ "$i" -lt "$n_user" ]; do
    a="$1"
    shift
    set -- "$@" "$a"
    i=$((i + 1))
  done
  docker run "$@"
}

# 不使用 exec：保证 trap 能在结束后删除临时 secret 文件
run_container "$@"
