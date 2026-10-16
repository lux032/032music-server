#!/bin/sh
# 构建期依赖安装（apt + pip），由 Dockerfile / Dockerfile.buildkit 共用。
#
# 代理来源（优先级从高到低）：
#   1. BuildKit secret 文件 /run/secrets/build_proxy（单行代理 URL，可含凭证）
#   2. Docker 预定义构建参数 HTTP_PROXY / HTTPS_PROXY / NO_PROXY（及小写形式）
# 代理只导出到本进程环境，不写入任何文件（不写 apt.conf、pip.conf），
# 因此不会进入镜像层；本脚本也从不打印代理地址。
set -eu

SECRET_FILE="${BUILD_PROXY_SECRET_FILE:-/run/secrets/build_proxy}"

secret_proxy=""
if [ -s "$SECRET_FILE" ]; then
  secret_proxy="$(head -n 1 "$SECRET_FILE" | tr -d '\r\n')"
fi

if [ -n "$secret_proxy" ]; then
  proxy_http="$secret_proxy"
  proxy_https="$secret_proxy"
  proxy_source="BuildKit secret"
else
  proxy_http="${http_proxy:-${HTTP_PROXY:-}}"
  proxy_https="${https_proxy:-${HTTPS_PROXY:-}}"
  # 只配置了其中一个时两者共用（apt 的 Debian 源走 http，pip 走 https）
  [ -n "$proxy_http" ] || proxy_http="$proxy_https"
  [ -n "$proxy_https" ] || proxy_https="$proxy_http"
  proxy_source="构建参数"
fi
proxy_none="${no_proxy:-${NO_PROXY:-}}"

if [ -n "$proxy_http" ]; then
  export http_proxy="$proxy_http" HTTP_PROXY="$proxy_http"
  export https_proxy="$proxy_https" HTTPS_PROXY="$proxy_https"
  if [ -n "$proxy_none" ]; then
    export no_proxy="$proxy_none" NO_PROXY="$proxy_none"
  fi
  echo "[build] 代理：已启用（来源：$proxy_source；地址不打印）"
else
  unset http_proxy HTTP_PROXY https_proxy HTTPS_PROXY 2>/dev/null || true
  echo "[build] 代理：未启用（直连）"
fi

# 可选的 Debian 镜像源（非敏感参数，例如 https://mirrors.tuna.tsinghua.edu.cn）
if [ -n "${APT_MIRROR:-}" ]; then
  mirror="${APT_MIRROR%/}"
  for f in /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list; do
    [ -f "$f" ] && sed -i "s#http://deb.debian.org#${mirror}#g" "$f"
  done
  echo "[build] apt 镜像源：${mirror}"
fi

# 超时 + 重试：网络不通时尽快失败，而不是长时间无进展。
APT_OPTS="-o Acquire::Retries=3 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 -o Dpkg::Use-Pty=0"
if [ "${APT_FORCE_IPV4:-0}" = "1" ]; then
  APT_OPTS="$APT_OPTS -o Acquire::ForceIPv4=true"
fi

echo "[build] apt-get update"
# shellcheck disable=SC2086
apt-get $APT_OPTS update
echo "[build] apt-get install ffmpeg"
# shellcheck disable=SC2086
DEBIAN_FRONTEND=noninteractive apt-get $APT_OPTS install -y --no-install-recommends ffmpeg
if [ "${INSTALL_GPU:-0}" = "1" ]; then
  echo "[build] apt-get install intel-opencl-icd"
  # shellcheck disable=SC2086
  DEBIAN_FRONTEND=noninteractive apt-get $APT_OPTS install -y --no-install-recommends intel-opencl-icd ocl-icd-libopencl1
fi
rm -rf "${APT_LISTS_DIR:-/var/lib/apt/lists}"/*

ffmpeg -hide_banner -v error -f lavfi -i "sine=frequency=440:duration=0.2" \
  -af "aresample=16000:resampler=soxr" -f null -
echo "[build] ffmpeg soxr OK"

# 空的 PIP_INDEX_URL 会被 pip 当成无效索引，未设置时必须移除
if [ -z "${PIP_INDEX_URL:-}" ]; then
  unset PIP_INDEX_URL 2>/dev/null || true
else
  echo "[build] pip 索引：自定义"
fi
echo "[build] pip install"
PIP_DISABLE_PIP_VERSION_CHECK=1 pip install --no-cache-dir --timeout 60 --retries 5 \
  -r "$(dirname "$0")/requirements.txt"
echo "[build] 依赖安装完成"
