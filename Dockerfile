# syntax=docker/dockerfile:1.7
FROM golang:1.26.5-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/music-server \
    ./cmd/server

FROM alpine:3.23

RUN addgroup -S musicserver && \
    adduser -S -G musicserver -h /app musicserver && \
    mkdir -p /data /music && \
    chown -R musicserver:musicserver /app /data

COPY --from=builder /out/music-server /usr/local/bin/music-server

USER musicserver
WORKDIR /app
EXPOSE 4533
VOLUME ["/data", "/music"]

ENV MUSIC_SERVER_ADDRESS=:4533 \
    MUSIC_SERVER_DATA_DIR=/data \
    MUSIC_SERVER_MUSIC_DIR=/music \
    MUSIC_SERVER_LOG_LEVEL=info

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:4533/api/v1/health >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/music-server"]
