#!/bin/sh
# 在 NAS 上运行声学分析 P0 验证。
# 用法：MUSIC=/你的/音乐目录 ./run-p0.sh [额外参数，例如 --sample 300]
# 只读挂载音乐目录；结果写到 ./p0-out，模型缓存在 ./p0-models。
set -eu

MUSIC="${MUSIC:?请设置 MUSIC=音乐目录（与 032 的 MUSIC_SERVER_MUSIC_PATH 相同）}"
HERE="$(cd "$(dirname "$0")" && pwd)"
OUT="${OUT:-$HERE/p0-out}"
MODELS="${MODELS:-$HERE/p0-models}"
IMAGE="${IMAGE:-032music-audio-p0}"
CPUS="${CPUS:-2}"

mkdir -p "$OUT" "$MODELS"

if [ "${SKIP_BUILD:-0}" != "1" ]; then
  docker build -t "$IMAGE" ${INSTALL_GPU:+--build-arg INSTALL_GPU=$INSTALL_GPU} "$HERE"
fi

GPU_ARGS=""
if [ -e /dev/dri ] && [ "${USE_GPU:-0}" = "1" ]; then
  GPU_ARGS="--device /dev/dri"
fi

# --cpus 限制为 2 核，避免影响 032 的实时转码；内存上限 2GB。
exec docker run --rm \
  --name 032music-audio-p0 \
  --cpus "$CPUS" --memory 2g \
  $GPU_ARGS \
  -v "$MUSIC:/music:ro" \
  -v "$OUT:/out" \
  -v "$MODELS:/models" \
  "$IMAGE" --threads "$CPUS" "$@"
