# 032 Music Server · 声学分析 P0 验证镜像
# Debian (glibc) 是必须的：onnxruntime / openvino 没有 musl (Alpine) 版本的 wheel。
# 模型（CC BY-NC-SA 4.0，仅限非商业）不打进镜像，首次运行时下载到 /models 卷。
FROM python:3.12-slim-bookworm

ARG INSTALL_GPU=0

RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg \
 && if [ "$INSTALL_GPU" = "1" ]; then apt-get install -y --no-install-recommends intel-opencl-icd ocl-icd-libopencl1; fi \
 && rm -rf /var/lib/apt/lists/* \
 && ffmpeg -hide_banner -v error -f lavfi -i "sine=frequency=440:duration=0.2" \
      -af "aresample=16000:resampler=soxr" -f null - \
 && echo "ffmpeg soxr OK"

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY analyzer ./analyzer
COPY bench.py .

ENV PYTHONUNBUFFERED=1 \
    OMP_NUM_THREADS=2

VOLUME ["/models", "/out"]
ENTRYPOINT ["python", "/app/bench.py"]
