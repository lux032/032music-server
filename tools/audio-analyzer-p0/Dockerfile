# 032 Music Server · 声学分析 P0 验证镜像（默认构建文件，兼容经典构建器和 BuildKit）
# Debian (glibc) 是必须的：onnxruntime / openvino 没有 musl (Alpine) 版本的 wheel。
# 模型（CC BY-NC-SA 4.0，仅限非商业）不打进镜像，首次运行时下载到 /models 卷。
#
# 代理：通过 Docker 预定义构建参数传入，例如
#   docker build --build-arg HTTP_PROXY --build-arg HTTPS_PROXY --build-arg http_proxy --build-arg https_proxy .
# 这里【刻意不声明】ARG HTTP_PROXY 等：未声明的预定义代理参数不会写入 docker history。
# 代理带凭证时请改用 Dockerfile.buildkit（BuildKit secret），见 README。
FROM python:3.12-slim-bookworm

# 以下均为非敏感参数，会出现在镜像历史中，不要放凭证
ARG INSTALL_GPU=0
ARG APT_MIRROR=
ARG APT_FORCE_IPV4=0
ARG PIP_INDEX_URL=

COPY requirements.txt build/install-deps.sh /tmp/build/
RUN sh /tmp/build/install-deps.sh && rm -rf /tmp/build

WORKDIR /app
COPY analyzer ./analyzer
COPY bench.py .

ENV PYTHONUNBUFFERED=1 \
    OMP_NUM_THREADS=2

VOLUME ["/models", "/out"]
ENTRYPOINT ["python", "/app/bench.py"]
