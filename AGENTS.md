# AGENTS.md — polydb 项目规则

本文件是 **polydb** 项目的**唯一规则源**，供所有 AI 助手（Qoder、Cursor、Claude Code 等）和人类贡献者参考。

---

## 1. 项目定位

**polydb** 是一个**多前端、多数据库**的数据库客户端，核心逻辑与 UI 严格解耦。

- **前端**：Web、TUI（终端）、GUI（桌面）
- **数据库**：SQLite、MySQL、PostgreSQL、SQL Server、Oracle、Redis
- **语言**：Rust 与 Go 双实现，共享同一份语言中立契约

名字含义：**poly**（多）+ **db**（数据库）——多库、多前端、多语言。

---

## 2. 总体架构（必须遵守）
Frontends (GUI / TUI / Web)
│
├── 进程内调用（GUI/TUI）──▶ app-core
└── HTTP/WS（Web）────────▶ server ──▶ app-core
│
┌───────────────────┼───────────────────┐
▼ ▼ ▼
db-core storage transport
(驱动抽象) (本地 SQLite) (local/http)
│
┌───────────┼───────────┬───────────┬───────────┬──────────┐
▼ ▼ ▼ ▼ ▼ ▼
sqlite mysql postgres mssql oracle redis

text

**铁律**：

1. 前端**不得**直接依赖任何 driver crate/package。
2. 前端**只**通过 `app-core`（进程内）或 `transport`（网络）访问数据。
3. driver 层**不得**包含任何 UI 假设。
4. `core` / `protocol` 层**不得**有 IO。

---

## 3. 目录结构
polydb/
├── spec/ # ★ 契约唯一真相源（语言中立）
│ ├── openapi.yaml # REST 接口
│ ├── asyncapi.yaml # WebSocket 事件
│ ├── schemas/ # JSON Schema（逻辑模型）
│ ├── arrow/ # Arrow schema（结果集）
│ ├── behavior.md # 行为约定：分页/取消/超时/重连
│ └── encoding.md # 物理编码约定
├── rust/
│ ├── Cargo.toml # workspace: polydb
│ ├── crates/
│ │ ├── protocol/ # 由 spec 生成（package: polydb-protocol）
│ │ ├── core/ # 领域模型、错误、DTO（无 IO）
│ │ ├── db-core/ # DatabaseDriver / SqlDriver / KvDriver
│ │ ├── db-sqlite/ db-mysql/ db-postgres/ db-mssql/ db-oracle/ db-redis/
│ │ ├── storage/ # sqlx + SQLite + 本地文件 keyring
│ │ ├── app-core/ # use case 编排
│ │ ├── transport/ # LocalTransport / HttpTransport
│ │ ├── server/ # Axum（HTTP 服务端，二进制在 crate 内）
│ │ └── ui-gui/ # GPUI（仅 GUI；TUI 只做 Go，无 Rust ui-tui）
│ └── apps/
│   └── polydb-gui/
├── go/
│ ├── go.mod # module github.com/<org>/polydb
│ ├── cmd/
│ │ ├── polydb-server/ # HTTP+WS
│ │ ├── polydb-tui/ # bubbletea
│ │ ├── polydb-cli/ # 本地 SQLite 最小查询器（非交互）
│ │ └── polydb-mcp/ # MCP server（stdio JSON-RPC，只读工具，M17）
│ ├── pkg/
│ │ ├── protocol/ # 由 spec 生成
│ │ ├── appcore/ dbcore/ server/ storage/ sshtunnel/ keyring/ mcp/
│ │ ├── dbsqlite/ dbmysql/ dbpostgres/ dbmssql/ dboracle/ dbredis/
│ │ ├── transport/ # 前端数据访问：Client 接口 + Local(进程内) / Remote(REST)
│ │ └── core/ # 预留空目录
│ └── internal/tui/
├── web/ # 前端只写一次（React/Vue + Monaco）
│ └── src/api/ # 由 spec 生成 TS 类型（@polydb/api）
├── test/contract/ # 双后端对拍
└── docs/

text

> **命名约定**：Rust 目录用短名（`core`、`db-core`），`Cargo.toml` 里 package name 用前缀 `polydb-*`。Go 目录用短名，import path 用 `github.com/<org>/polydb/pkg/*`。

---

## 4. 数据库抽象：能力分层

**禁止**用一个 trait/interface 塞下所有数据库方法。必须分两层。

### 4.1 Rust

```rust
#[async_trait]
pub trait DatabaseDriver: Send + Sync {
    fn kind(&self) -> DatabaseKind;
    async fn ping(&self) -> Result<()>;
    async fn close(&self) -> Result<()>;
    fn as_sql(&self) -> Option<&dyn SqlDriver> { None }
    fn as_kv(&self) -> Option<&dyn KvDriver> { None }
}

#[async_trait]
pub trait SqlDriver: DatabaseDriver {
    async fn execute(&self, sql: &str, params: &[Value]) -> Result<QueryResult>;
    async fn begin(&self) -> Result<Transaction>;
    async fn list_schemas(&self) -> Result<Vec<SchemaInfo>>;
    async fn list_tables(&self, schema: &str) -> Result<Vec<TableInfo>>;
    async fn list_columns(&self, schema: &str, table: &str) -> Result<Vec<ColumnInfo>>;
    async fn list_indexes(&self, schema: &str, table: &str) -> Result<Vec<IndexInfo>>;
    async fn list_foreign_keys(&self, schema: &str, table: &str) -> Result<Vec<FkInfo>>;
    async fn create_table_sql(&self, schema: &str, table: &str) -> Result<String>;
}

#[async_trait]
pub trait KvDriver: DatabaseDriver {
    async fn select_db(&self, index: u32) -> Result<()>;
    async fn scan_keys(&self, cursor: u64, pattern: &str, count: u32) -> Result<ScanPage>;
    async fn key_type(&self, key: &str) -> Result<KeyType>;
    async fn get_value(&self, key: &str) -> Result<RedisValue>;
    async fn set_value(&self, key: &str, value: RedisValue) -> Result<()>;
    async fn exec_command(&self, args: &[String]) -> Result<RedisReply>;
}
动态调度：统一用 `Arc<dyn SqlDriver>` / `Arc<dyn KvDriver>`（trait object）持有驱动，`DatabaseDriver` 提供 `as_sql()` / `as_kv()` 做能力降级判断。

rust
// Connection 只持有一个 trait object 引用
pub struct Connection { driver: Arc<dyn DatabaseDriver> }

impl Connection {
    pub fn new(driver: Arc<dyn DatabaseDriver>) -> Self;   // kind 由驱动自身决定
    pub fn as_sql(&self) -> Option<&dyn SqlDriver>;
    pub fn sql_driver_arc(&self) -> Option<Arc<dyn SqlDriver>>; // → SqlConnection::new(conn) 包装
}

pub struct SqlConnection { conn: Arc<dyn SqlDriver> }
pub struct KvConnection { conn: Arc<dyn KvDriver> }
```

连接类型（SQLite/MySQL/Postgres/MSSQL/Oracle 的 `Conn`、Redis 的 `RedisConn`）各自实现 `DatabaseDriver` 并返回 `clone_sql_driver_arc() / clone_kv_driver_arc()` 供上层无差异使用，避免在 driver 层做 match 分派。
4.2 Go
go
type Driver interface {
    Kind() DatabaseKind
    Ping(ctx context.Context) error
    Close() error
    AsSQL() (SQLDriver, bool)
    AsKV() (KVDriver, bool)
}

type SQLDriver interface {
    Driver
    Execute(ctx context.Context, sql string, args ...any) (*QueryResult, error)
    Begin(ctx context.Context) (Tx, error)
    ListSchemas(ctx context.Context) ([]SchemaInfo, error)
    ListTables(ctx context.Context, schema string) ([]TableInfo, error)
    ListColumns(ctx context.Context, schema, table string) ([]ColumnInfo, error)
    // ...
}

type KVDriver interface {
    Driver
    SelectDB(ctx context.Context, idx int) error
    ScanKeys(ctx context.Context, cursor uint64, pattern string, count int) (*ScanPage, error)
    KeyType(ctx context.Context, key string) (KeyType, error)
    GetValue(ctx context.Context, key string) (RedisValue, error)
    ExecCommand(ctx context.Context, args []string) (RedisReply, error)
}
5. 编码格式约定（重要）
禁止在任何通道无差别地使用 JSON。按通道选择：

通道	格式
spec/ 契约定义	OpenAPI + JSON Schema
契约测试 fixture	JSON（可读、可 diff）
REST 控制面（连接/元数据/DDL）	MessagePack
REST 数据面（查询结果）	MessagePack（当前实现；Arrow IPC 为预留，未实现）
WebSocket 事件	MessagePack
WebSocket 大结果流	MessagePack（当前实现；Arrow IPC stream 为预留，未实现）
大字段（BLOB/大 TEXT）	预留（旁路 + Range 请求，未实现；当前直接在结果集内联）
进程内（GUI/TUI↔app-core）	零序列化，直接传结构体
Content-Type：application/msgpack（预留：application/vnd.apache.arrow.stream）。

JSON 仅作为调试/握手 fallback，可保留 application/json。

契约唯一真相源是 spec/schemas/*.json，物理编码在 spec/encoding.md 约定。

编码格式库
用途	Rust	Go
MessagePack	rmp-serde	vmihailenco/msgpack
CBOR	ciborium	fxamacker/cbor
Arrow	预留（未引入）	—
压缩	flate2 / zstd	klauspost/compress
6. 存储层（storage）
本地 SQLite，sqlx（Rust）/ modernc.org/sqlite（Go，纯 Go 免 cgo）。

WAL 模式。

表结构：当前仅有单表 `connections`（含 `password_ref` 与 `ssh_tunnel` JSON 列）；`query_history`（M12 在 Web/浏览器侧本地存，不入 DB）、`ssh_tunnels` 尚未落库。

密码不落库：明文一律进 keyring，DB 只存 `password_ref`。两端默认用本地加密文件 `keyring.bin`（AES-256-GCM，scrypt 主密码派生）；Go 额外支持 `POLYDB_KEYRING=os` 走系统凭据管理器（zalando/go-keyring），Rust 目前仅有文件后端。

Repository 模式；Rust 用 OnceCell 单例。

目录约定
平台	路径
配置	~/.config/polydb/
数据	~/.local/share/polydb/
日志	~/.local/state/polydb/polydb.log
环境变量前缀	POLYDB_*
错误码前缀	POLYDB_ERR_*
7. 双实现一致性
Rust 与 Go 是双实现，必须防漂移：

spec/ 是唯一真相源，改 spec → 重新生成两端代码。

契约测试 test/contract/：同一份 fixture，分别打 Rust 与 Go 的 HTTP 接口，比对响应。

CI 双跑：

Rust：cargo test + cargo clippy

Go：go test ./... + golangci-lint

契约测试作为门禁，不通过不允许合并。

spec 变更必须在 PR 中标注 breaking / non-breaking。

行为文档 spec/behavior.md：分页、取消、超时、重连、错误码表，两端必须一致。

错误结构统一：

json
{ "code": "POLYDB_ERR_XXX", "message": "...", "detail": {}, "retryable": false }
8. 分工建议
场景	后端	前端
桌面 GUI	Rust	GPUI
TUI（本地）	Go（只维护一套，bubbletea）	—
Web 服务端 / 多用户	Go（部署、goroutine）	React/Vue + Monaco
单机 CLI / 脚本化	Go（单二进制）	—
本地开发 GUI 调试	Rust	GPUI
9. 驱动选型
Rust
数据库	crate
SQLite	rusqlite（bundled）
MySQL	sqlx
PostgreSQL	sqlx
SQL Server	tiberius
Oracle	oracle
Redis	redis
Go
数据库	库
SQLite	modernc.org/sqlite（纯 Go）
MySQL	go-sql-driver/mysql
PostgreSQL	jackc/pgx/v5
SQL Server	microsoft/go-mssqldb
Oracle	sijms/go-ora（纯 Go，默认）或 godror（cgo，可选 build tag）
Redis	redis/go-redis/v9
10. 代码风格
Rust
遵循 rustfmt 默认配置；clippy 零警告。

错误用 thiserror（库）/ anyhow（应用）。

异步统一 smol + async-lock（沿用现有项目），或 tokio（若迁移，需全局一致）。

公共 API 必须有文档注释。

crate 命名：polydb-<layer>（如 polydb-core、polydb-db-core）。

Go
遵循 gofmt / goimports。

错误用 errors.Is/As + 自定义 error 类型，禁止裸 panic。

context.Context 作为第一个参数贯穿所有 IO。

包名短小写，不用下划线。

module path：github.com/<org>/polydb。

通用
命名：snake_case（Rust/JSON 字段）、camelCase（Go 导出）、PascalCase（类型）。

禁止在 driver/app-core 中 println! 调试输出，用 tracing（Rust）/ slog 或 zerolog（Go）。

禁止把密码/token 写进日志。

日志 logger 名统一为 polydb。

11. 里程碑
阶段	目标
M0	定义 spec/，生成 Rust/Go/TS 三端协议代码
M1	Rust app-core + LocalTransport + GPUI 跑通
M2	Go app-core + HTTP server，契约测试通过（SQLite + PostgreSQL）
M3	Web 前端接 Go 后端，跑通连接/查询/结果
M4	Go TUI（bubbletea）跑通同一套 use case
M5	补齐 MySQL / SQL Server / Oracle（两端）
M6	Redis（KvDriver，两端）+ 前端 Redis 模式
M7	契约测试全覆盖 + CI 双跑 + Docker 发布
M8	SSH 隧道、keyring、Pub/Sub、Cluster（可选）
12. 给 AI 助手的指令
在本项目中工作时，必须：

先看 spec/，再动代码。任何接口变更从 spec 开始。

不要在 UI 层写业务逻辑，业务逻辑属于 app-core。

不要让 driver 层依赖 UI 或 storage。

不要在数据面引入 JSON；用 MessagePack / Arrow。

不要把大字段内联进结果集；用旁路引用。

不要在 DB 里存明文密码。

新增数据库驱动时：

在 spec/schemas 补 DTO（若需要）

Rust：crates/db-<name>/ + db-core 加 enum variant + Connection 分派

Go：pkg/db<name>/ + dbcore 实现接口

补契约测试用例

补 docs/ 说明

新增前端时：

只依赖 transport 或 app-core

不引任何 driver crate/package

改 spec/ 时：

标注 breaking / non-breaking

重新生成三端代码

跑契约测试

提交前：

Rust：cargo fmt && cargo clippy && cargo test

Go：gofmt -w . && golangci-lint run && go test ./...

契约测试：make contract-test

13. 常用命令
bash
# Rust
cd rust && cargo build --workspace
cd rust && cargo test --workspace
cd rust && cargo run -p polydb-gui

# Go
cd go && go build ./...
cd go && go test ./...
cd go && go run ./cmd/polydb-server

# 契约测试
make contract-test

# 生成协议代码
make gen-protocol   # spec/ → rust/crates/protocol, go/pkg/protocol, web/src/api
14. 禁止事项（红线）
❌ 前端直接 import driver

❌ driver 依赖 UI

❌ core / protocol 里做 IO

❌ 数据面用 JSON

❌ 大字段内联结果集

❌ 明文密码落库

❌ 未经 spec 变更就改接口

❌ 契约测试不通过就合并

❌ 双实现行为不一致

❌ 在代码/日志/配置中使用旧名 dbclient

15. 命名速查
项目	值
项目名	polydb
Rust workspace	polydb
Rust crate 前缀	polydb-
Rust 二进制	polydb-gui（仅 GUI；TUI/Server 另一套走 Go）
Go module	github.com/<org>/polydb
Go 二进制	polydb-server / polydb-tui / polydb-cli（本地 SQLite 查询器）/ polydb-mcp（MCP 只读工具）
Web 包名	@polydb/api / @polydb/web
环境变量前缀	POLYDB_
配置目录	~/.config/polydb/
数据目录	~/.local/share/polydb/
日志文件	polydb.log
Docker 镜像	polydb-server
systemd 服务	polydb.service
协议 title	PolyDB API
错误码前缀	POLYDB_ERR_
logger 名	polydb
