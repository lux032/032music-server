#!/bin/sh
# 两个 Dockerfile 除依赖安装步骤外必须一致；不得声明代理 ARG / ENV（会写入镜像历史）。
# 用法：sh tests/test_dockerfiles.sh
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
tail_of() { sed -n '/^RUN useradd/,$p' "$1"; }
fail=0
if [ "$(tail_of "$ROOT/Dockerfile")" != "$(tail_of "$ROOT/Dockerfile.buildkit")" ]; then
  echo "FAIL: Dockerfile 与 Dockerfile.buildkit 的后半部分不一致"; fail=1
fi
if grep -Eq '^ARG +(HTTP|HTTPS|NO)_PROXY|^ARG +(http|https|no)_proxy|ENV +(HTTP|HTTPS)_PROXY' "$ROOT/Dockerfile" "$ROOT/Dockerfile.buildkit"; then
  echo "FAIL: 不应声明代理 ARG/ENV"; fail=1
fi
if ! cmp -s "$ROOT/build/install-deps.sh" "$ROOT/../tools/audio-analyzer-p0/build/install-deps.sh"; then
  echo "NOTE: build/install-deps.sh 与 P0 工具的版本不同（如有意为之可忽略）"
fi
[ "$fail" -eq 0 ] && echo "Dockerfile 检查通过"
exit "$fail"
