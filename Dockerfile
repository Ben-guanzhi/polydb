# syntax=docker/dockerfile:1

# ─── build：Go 编译（CGO 关闭，纯静态二进制） ─────────────────
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go/go.mod go/go.sum ./
RUN go mod download
COPY go/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/polydb-server ./cmd/polydb-server

# ─── runtime：alpine + 非 root 用户 ─────────────────────────
FROM alpine:3.20
RUN adduser -D -u 10001 polydb \
    && mkdir -p /data && chown polydb:polydb /data
WORKDIR /app
COPY --from=build /out/polydb-server /app/polydb-server
USER polydb
ENV POLYDB_ADDR=0.0.0.0:8080 \
    POLYDB_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null || exit 1
ENTRYPOINT ["/app/polydb-server"]
