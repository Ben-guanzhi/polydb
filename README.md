# polydb

**poly**（多）+ **db**（数据库）——多库、多前端、多语言的数据库客户端。

polydb 是一个前端与数据库后端严格解耦的通用数据库客户端：
同一套契约（`spec/`）驱动 Rust 与 Go 双后端，Web / TUI / GUI 三类前端共用。

- **前端**：Web（React + Monaco，主力）、TUI（bubbletea，支持 `-server` 连远程服务端）、GUI（GPUI 最小可用：连接列表/查询/结果）
- **数据库**：SQLite、PostgreSQL、MySQL、SQL Server、Oracle、Redis
- **语言**：Rust（桌面 / 本地开发）+ Go（服务端 / TUI / CLI）
- **契约**：`spec/openapi.yaml`（REST 控制面）、`spec/asyncapi.yaml`（WebSocket 查询流）、`spec/schemas/*.json`（JSON Schema）、`spec/arrow/`（结果集，预留）

## 铁律

详见 [AGENTS.md](./agents.md)（唯一规则源）。核心约束：

1. 前端只经 `app-core`（进程内）或 `transport`（网络）访问数据，**不 import 任何 driver**。
2. driver 层无 UI 假设，`core` / `protocol` 无 IO。
3. 数据面禁用 JSON：REST 用 MessagePack，WebSocket 用 MessagePack（大结果集 Arrow 为预留扩展），控制面/元数据用 msgpack。
4. 密码不落库：连接参数只存 `password_ref`，明文只经 keyring（M8）。
5. Rust 与 Go 双实现，`spec/` 是唯一真相源；契约测试通过后才允许合并。

## 目录结构

```
polydb/
├── spec/          # 契约唯一真相源（语言中立）
├── rust/          # Rust workspace（workspace: polydb）
│   ├── crates/    # protocol / core / db-core / db-<name> / storage / app-core / transport / server / ui-*
│   └── apps/      # polydb-gui（GPUI；server 二进制在 crates/server 内）
├── go/            # Go module github.com/polydb/polydb
│   ├── cmd/       # polydb-server / polydb-tui / polydb-cli
│   ├── pkg/       # protocol / core / dbcore / db<name> / storage / appcore / transport / server / keyring / sshtunnel
│   └── internal/tui/
├── web/           # React 前端（Vite + TS + Monaco）
├── test/contract/ # 双后端对拍契约测试
└── docs/          # 各模块说明（drivers / frontends / docker / ssh-tunnel / keyring）
```

## 快速开始

前置依赖：Rust（stable，含 protoc 若用 sqlx 生成）、Go 1.25+、Node 20+。

```bash
# Rust 后端（本地，默认 127.0.0.1:8080，POLYDB_ADDR 覆盖）
cd rust && cargo build -p polydb-server && cargo run -p polydb-server

# Go 后端（Web 服务端，默认 127.0.0.1:8080，POLYDB_ADDR 覆盖）
cd go && go run ./cmd/polydb-server

# Web 前端
cd web && npm install && npm run dev
# Vite 代理 /api 与 /ws → POLYDB_SERVER_URL || http://127.0.0.1:8080

# TUI（本机直连 app-core）
cd go && go run ./cmd/polydb-tui

# TUI 远程模式（连 polydb-server 的 REST 接口）
cd go && go run ./cmd/polydb-tui -server http://127.0.0.1:8080

# GUI（Rust + GPUI，进程内 app-core）
cd rust && cargo run -p polydb-gui

# CLI（本地 SQLite 最小查询器，非交互）
cd go && go run ./cmd/polydb-cli -db test.db -e "SELECT 1 AS x"
printf 'SELECT 42 AS answer' | go run ./cmd/polydb-cli -db :memory:

# 契约测试
cd test/contract && go test -count=1 ./...
# Rust 后端需要先 `cargo build -p polydb-server`
```

## 里程碑进度

| 里程碑 | 内容 | 状态 |
|---|---|---|
| M0 | 定义 spec/，生成 Rust/Go/TS 协议代码 | ✓ |
| M1 | Rust app-core + LocalTransport + GPUI 跑通（GUI 作为原型沉淀，功能重心在 Web） | ✓ |
| M2 | Go app-core + HTTP server，契约测试通过 | ✓ |
| M3 | Web 前端接 Go 后端 | ✓ |
| M4 | Go TUI（bubbletea） | ✓ |
| M5 | MySQL / MSSQL / Oracle 双实现 + Rust PG 补齐 | ✓ |
| M6 | Redis（KvDriver）双实现 + 前端 Redis 模式 | ✓ |
| M7 | 契约测试全覆盖 + CI 双跑 + Docker 发布 | ✓ |
| M8 | SSH 隧道 + keyring 双实现（范围经确认排除 Pub/Sub/Cluster） | ✓ |
| M9 | WebSocket 传输层 + 查询流双实现（Pub/Sub/Cluster 明确留待后续） | ✓ |

## 门禁

```bash
# Rust
cd rust && cargo fmt --all --check && cargo clippy --workspace --all-targets -- -D warnings && cargo test --workspace

# Go
cd go && gofmt -l ./pkg ./cmd ./internal && \
  go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.0.2 run ./... && \
  go test ./...

# Web
cd web && npm run check && npm run build

# 契约测试
cd test/contract && go test -count=1 -timeout 300s ./...
```

CI 定义在 `.github/workflows/ci.yml`（Rust / Go / Web / contract 四 job，Postgres/MySQL/Redis/MSSQL/Oracle 起容器真跑；MSSQL 用 `mssql/server:2022`，Oracle 用 `gvenzl/oracle-free:23-slim`）。

## 关键设计

- **能力分层驱动抽象**（AGENTS.md §4）：`DatabaseDriver` 基础层 + `SqlDriver` / `KvDriver` 特化层；Rust 用 `Arc<dyn SqlDriver>` / `Arc<dyn KvDriver>`（trait object）动态分派。
- **编码格式**（[spec/encoding.md](./spec/encoding.md)）：控制面/元数据/WS 全部 MessagePack；JSON 仅作调试与握手 fallback；Arrow 用于大结果集流式传输（**预留扩展，未实现**，当前数据面一律 MessagePack）。
- **存储层**：本地 SQLite + WAL，当前单表 `connections`（`password_ref` + `ssh_tunnel` JSON 列）；密码只存 `password_ref`，实际值经 keyring —— Rust 与 Go 默认都是本地加密文件后端，Go 额外支持全系统凭据管理器。
- **数据面/控制面分离**：控制面走 HTTP REST（`/api/...`），查询走 HTTP（同步）或 WebSocket（`/ws`，流式与取消）。
- **错误结构统一**：`{code, message, detail, retryable, cause}`，错误码 `POLYDB_ERR_*`。

## 文档

- [AGENTS.md](./agents.md) — 项目规则源（架构、铁律、命名、命令）
- [docs/drivers.md](./docs/drivers.md) — 驱动矩阵与实现约定
- [docs/frontends.md](./docs/frontends.md) — Web / TUI / GUI 前端
- [docs/docker.md](./docs/docker.md) — Docker 发布与运行
- [docs/ssh-tunnel.md](./docs/ssh-tunnel.md) — SSH 隧道配置
- [docs/keyring.md](./docs/keyring.md) — 密钥环（密码存储）
- [spec/](./spec) — 契约唯一真相源

## 环境变量

前缀 `POLYDB_*`：`POLYDB_ADDR`（监听）、`POLYDB_DATA_DIR`（数据目录）、`POLYDB_MASTER_PASSWORD`（keyring 主密码）、`POLYDB_KEYRING`（`file` | `os`）、`POLYDB_SERVER_URL`（Web 代理目标）、`POLYDB_TEST_PG/MYSQL/MSSQL/ORACLE/REDIS`（契约测试 DSN）。

## License

[MIT](./LICENSE) — Copyright (c) 2026 polydb contributors.
