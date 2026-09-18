# Docker 发布（M7）

镜像名 `polydb-server`（AGENTS.md 命名速查）。Go 后端单二进制，CGO 关闭静态编译，
alpine 运行时 + 非 root 用户（`polydb`，uid 10001）。

## 构建与运行

```bash
# 构建镜像
make docker-build          # docker build -t polydb-server:latest .

# 单容器运行（数据卷 polydb-data 挂到 /data）
make docker-run            # docker run --rm -p 8080:8080 -v polydb-data:/data polydb-server:latest

# 或 compose 一键起（server + redis）
make compose-up
```

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `POLYDB_ADDR` | `0.0.0.0:8080` | 监听地址（镜像内固定 0.0.0.0，宿主机经 `-p` 映射） |
| `POLYDB_DATA_DIR` | `/data` | 数据目录（connections 等本地库落此处，务必挂卷持久化） |

数据目录含本地 SQLite 存储 `polydb.db`；密码仍只存 `password_ref`（M8 keyring 前为占位）。

## 健康检查

镜像内置 `HEALTHCHECK`：轮询 `GET /api/health`，容器编排平台（compose/k8s）可直接使用。
systemd 部署可参考同语义的 `ExecStart` + `curl /api/health`。

## 说明

- 数据库连接参数无密码（设计上密码不落库），容器内连接 Redis 等用 `host.docker.internal` 或
  同一 compose 网络内的服务名。
- Dockerfile 为多阶段构建：`golang:1.25-alpine` 编译 → `alpine:3.20` 运行，产物约 10MB 量级。
- Web 前端（`web/`）为独立 Vite 构建，不在本镜像内；如需一体化部署，另行用 nginx 静态托管
  `web/dist` 并反向代理 `/api` 到本服务。
