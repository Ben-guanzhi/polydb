# 数据库驱动（M6）

M5 补齐 MySQL / SQL Server / Oracle 双实现（Rust + Go），并把 Rust 侧 PostgreSQL 驱动从 stub 升级为真实实现。
M6 新增 Redis（KvDriver）双实现 + 前端 Redis 模式。
驱动层遵循 AGENTS.md 铁律：无 UI 假设、不依赖 storage、前端不得直接 import。

## 驱动矩阵

| 数据库 | Rust crate | Go package | Rust 依赖 | Go 依赖 |
|---|---|---|---|---|
| SQLite | `db-sqlite` | `dbcore` + `dbsqlite` | rusqlite（bundled） | modernc.org/sqlite |
| PostgreSQL | `db-postgres` | `dbpostgres` | sqlx | jackc/pgx/v5 |
| MySQL | `db-mysql` | `dbmysql` | sqlx | go-sql-driver/mysql |
| SQL Server | `db-mssql` | `dbmssql` | tiberius + tokio-util compat | microsoft/go-mssqldb |
| Oracle | `db-oracle` | `dboracle` | oracle（ODPI-C） | sijms/go-ora（纯 Go） |
| Redis | `db-redis` | `dbredis` | redis（0.29，sync API） | redis/go-redis/v9 |

所有网络驱动共用 `db-core` 的 `DatabaseDriver` / `SqlDriver` / `KvDriver` 抽象，动态分派用 enum 包装（`SqlConnection` / `KvConnection` / `Connection`），方法经 match 分派，避免 dyn 关联类型问题。

## 连接语义（Rust 与 Go 一致）

- 连接参数只有 `host / port / database / username`，**不含密码**（密码不落库红线）。Rust 侧 `net_addr(info, default_port)` 拼 host:port。
- `POST /api/connections` 仅落库（懒语义），真正的驱动实例在 `connect`（`POST /{id}/test` 或首次查询）时创建。
- **例外：Redis** 两端都在 `connect`/`Open` 时即建连并 PING（`db-index` 非 0 时额外 `SELECT`），服务不可用会立刻失败——契约测试据此在 create 阶段软跳过。

## Redis（KvDriver）

能力分层见 AGENTS.md §4：`KvDriver`（select_db / scan_keys / key_type / get_value / set_value / exec_command）。
TTL 不在 driver API 中（set_value 只有 key+value），过期时间通过 `exec_command` 的 `EXPIRE` 显式设置。

- **连接**：Rust 用 `redis` crate 的同步 `Connection`（`get_connection_with_timeout(5s)`，无公开 close，取走 drop 关闭）；Go 用 `client.Conn()` 单条专用连接（SELECT 的 db 状态在连接上持久）。两端读写超时均为 5s，与网络驱动对齐。
- **回复约定（双实现统一）**：RESP 的 simple string 与 bulk string 一律映射为 `bulk_string`——go-redis 的 proto.Reader 对两者都返回 Go `string`，`Cmd.Val()` 无法区分；Rust 侧 `SimpleString`/`Okay` 同样映射。`redis.Nil` / `Value::Nil` → `null`；嵌套数组中的 error → `error`；`Set`/`Push` → `array`；`Attribute` 剥壳后递归。
- **ServerError**：Rust `redis` crate 的 `ServerError` 无 `Display`，手动拼 `"{code} {detail}"` 为 message。
- **扁平数组**：ZRANGE WITHSCORES / HGETALL 返回扁平 `[f1,v1,f2,v2]`，元组 `FromRedisValue` 只认嵌套数组——Rust 手动配对、HGETALL 走 `as_map_iter`；Go 用 `ZRangeWithScores` / `HGetAll`，两端结果一致。
- **Stream 序列化**：`XADD key * data <string>`（单字段约定），读取 `XRANGE - + COUNT 100`，序列化为 `"<id> {data=值}"`（字段按名排序，多条 `"; "` 连接）。
- **缺失键**：spec 的 `RedisValue` 无 none 变体，`get_value` 对缺失键返回 `POLYDB_ERR_QUERY_FAILED`（message `key not found: X`），两端一致。
- **Go 反序列化**：HTTP 层 msgpack 对 `RedisValue.Value`（interface{}）产出 `[]interface{}` / `map[string]interface{}`，Go 驱动用 `strSlice` / `strMap` / `zsetMembers` 归一到强类型，与 Rust serde 强类型解码对齐。**红线提醒：直接断言 `[]string` / `map[string]string` 会静默得到空值，必须走转换辅助。**
- **scan ttl**：持久键 `ttl=-1`，过期键 `-2`（两端取 `TTL` 命令原始值）。

## 不可达时的行为（重要）

| 驱动 | 连接方式 | 无服务时的失败耗时 |
|---|---|---|
| Rust postgres/mysql | sqlx `connect_lazy` + `acquire_timeout(5s)` | ~5s，报 pool 超时 |
| Rust mssql | open() 内 `smol::block_on` 同步建连，全部 block_on 用 Timer 竞速上限 6s（tiberius 0.12 无内置超时） | 6s，报 timeout |
| Rust oracle | oracle crate 阻塞式 `Connection::connect`（ODPI-C） | 立即，ORA-12541（无监听） |
| Rust redis | `get_connection_with_timeout(5s)` | ~5s，报 connect 失败 |
| Go 全部 | `sql.Open` 懒连接；`PingContext` 带超时 | 立即（本机 ECONNREFUSED） |

契约测试对网络场景是**软跳过**：`POST /test` 返回 `connected:false` 时记录日志并跳过流程，因此无 MySQL/MSSQL/Oracle/PG/Redis 服务的机器也能跑完整契约套件（SQLite 全流程真跑）。

## Rust 驱动注意点

- **mssql（tiberius）**：`Client` 非 Send 且 `close` 消费自身。所有交互经 `smol::block_on` 同步执行，外层 async 方法**零 await**，避免 async_trait 的 Send 约束报错；`Mutex<Option<Client>>` 支持 close 取走。参数 `tsql_params` 用 `Box<dyn ToSql>`（`Value::String` 克隆为 owned `String`）。值解码用 `try_get` 链（bool→i64→…→&str→&[u8]）。
- **mssql 超时**：tiberius 0.12 没有 `connect_timeout` API；不可达主机（防火墙丢包而非拒绝）会让 TCP 握手挂到系统超时（tokio 串行尝试多个解析地址，每个可达 ~4s+）。因此所有 block_on 用 `smol::future::or` + `smol::Timer` 竞速，`IO_TIMEOUT = 6s`，超时分支获胜时直接丢弃操作 future（底层 socket 一并关闭，取消安全）。
- **oracle**：`SqlValue` 在 0.6 是结构体（非枚举），用 `is_null()` + `get::<T>()` 链解码（i64→f64→bool→String→Vec<u8>→Null）。`Statement::query(&refs)` 返回 `ResultSet`，行用 `row.sql_values()`；DML 走 `stmt.execute(&refs)` + `row_count()`。`ResultSet` 迭代元素是 `Option<Result<T>>`，`.next()` 后接 `.transpose()`。
- **redis**：`redis` crate 0.29 同步 API 在 async 方法内用 `parking_lot::Mutex<Option<Connection>>` 持锁执行（db-oracle 模式，外层零 await）；close 用 `drop(guard.take())`。
- **DatabaseKind 序列化**：serde 的 snake_case 会把 `MySql` 变成 `"my_sql"`，与 spec 的 `"mysql"` 不符；`common.rs` 中 MySql 变体加了 `#[serde(rename = "mysql")]`。
- **Rust /test 语义**：`connect` 对懒连接（sqlx lazy pool）总是成功，因此 server 的 `test_connection` handler 在 `connect` 后必须 `ping`，失败则返回 `connected:false` + 错误（与 Go 一致，也是软跳过的依据）。

## 契约测试

- 场景：`test/contract/contract_test.go` — `TestContractSQLite`（全流程真跑）、`TestContractPostgres/MySQL/MSSQL/Oracle`（网络场景，软跳过）、`TestContractRedis`（KV 场景，软跳过）。
- SQLite 场景另覆盖：**值类型矩阵**（NULL/INTEGER/REAL/TEXT/BLOB 的 rows 值映射 parity，BLOB 两端统一 `"<blob N bytes>"` 占位符）、**batch stop_on_error=true**（错误项 append 后 break，results 仅 1 项）、**单条 GET /api/connections/:id**、空库 `[]` wire 断言。
- 环境变量（默认 DSN 均为本机免密）：`POLYDB_TEST_PG`、`POLYDB_TEST_MYSQL`、`POLYDB_TEST_MSSQL`、`POLYDB_TEST_ORACLE`、`POLYDB_TEST_REDIS`（默认 `localhost:6379`）。网络 DSN 支持 `scheme://user:password@host:port/db`（密码按 URL 百分号转义），带密码时创建连接自动携带 `password` 字段（MSSQL 容器强制 sa 密码即用此路径）。
- 网络场景 SQL 用唯一表名 `ct_users_<unixnano%1e8>`，避免重复跑测试时的残留表；Oracle 标识符大写，SELECT 用 `AS "id"` 小写别名对齐列名断言。
- Redis 场景键名带唯一后缀 `ct:redis:<type>:<unixnano%1e8>`；SCAN 不保证单轮返回全部匹配，`scanAllKeys` 循环收集到 `cursor=0` 再断言；持久键断言 `ttl=-1`（无过期竞态）；流 ID 由 Redis 生成，比对前用 `onlyStreamData` 剥离动态 ID。
- **空列表约定**：Go 侧所有列表端点（connections / schemas / tables / columns / indexes / foreign-keys / kv scan）必须返回 `[]` 而非 null——vmihailenco/msgpack 会把 nil slice 编码为 msgpack null，而 Rust `Vec` 恒为 `[]`，wire 不一致会打崩 web 端（`conns.length`）。Go 侧统一 `make([]T, 0)` 初始化，契约测试以 `GET /api/connections` 空库返回 `[]` 锁定两端。
- 运行：`cd test/contract && go test -v ./...`（Rust 侧需先 `cargo build -p polydb-server`；有 make 则 `make contract-test`）。
- **CI（M7）**：`.github/workflows/ci.yml` 的 contract job 起 postgres:16-alpine / redis:7-alpine / mysql:8 / mssql-server:2022 / gvenzl-oracle-free:23-slim 服务容器，五类网络场景在 CI 真跑（MSSQL 密码 `PolyDb_Test_2026`、Oracle `system/oracle`，经 DSN 注入）；无服务的本地环境仍软跳过。CI 另有 rust / go / web 三 job（golangci-lint v2 用 `go/.golangci.yml`，errcheck 对 `defer Close`/响应写入白名单）。
- Rust 侧 `RedisValue` / `RedisReply`（internally-tagged enum）的 msgpack 往返由 `rust/crates/protocol/tests/redis_msgpack_test.rs` 单元测试覆盖（rmp-serde + `with_human_readable`），防 wire 形状漂移。
