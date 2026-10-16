# 032 Music Server · 声学分析服务（默认构建文件，兼容经典构建器和 BuildKit）
# Debian (glibc)：onnxruntime / openvino 没有 musl (Alpine) wheel。
# 模型（MTG Essentia，CC BY-NC-SA 4.0，仅限非商业）不打进镜像，启动时下载到 /models 卷。
#
# 代理：通过 Docker 预定义构建参数传入（--build-arg HTTP_PROXY --build-arg HTTPS_PROXY ...）。
# 这里【刻意不声明】ARG HTTP_PROXY 等，未声明的预定义代理参数不会写入 docker history。
# 代理带凭证时改用 Dockerfile.buildkit（BuildKit secret）。见 README。
FROM python:3.12-slim-bookworm

# 非敏感参数，会出现在镜像历史中，不要放凭证
ARG APT_MIRROR=
ARG APT_FORCE_IPV4=0
ARG PIP_INDEX_URL=

COPY requirements.txt build/install-deps.sh /tmp/build/
RUN sh /tmp/build/install-deps.sh && rm -rf /tmp/build

RUN useradd --system --uid 10001 --home-dir /app --shell /usr/sbin/nologin analyzer \
 && mkdir -p /models /app \
 && chown analyzer:analyzer /models

WORKDIR /app
COPY musicanalyzer ./musicanalyzer

ENV PYTHONUNBUFFERED=1 \
    OMP_NUM_THREADS=2 \
    ANALYZER_MUSIC_DIR=/music \
    ANALYZER_MODELS_DIR=/models \
    ANALYZER_LISTEN=0.0.0.0:8000

USER analyzer
VOLUME ["/models"]
EXPOSE 8000
HEALTHCHECK --interval=60s --timeout=10s --start-period=120s --retries=3 \
  CMD ["python", "-c", "import urllib.request,sys; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:8000/health', timeout=5).status == 200 else 1)"]
ENTRYPOINT ["python", "-m", "musicanalyzer.server"]
