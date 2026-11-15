# PolyDB 行为约定

本文档定义 PolyDB 双实现（Rust / Go）必须遵守的行为规范。
任何偏离都视为 bug，契约测试应覆盖这些场景。

---

## 1. 分页

### 1.1 偏移分页（REST）
- 参数：`offset`（默认 0）、`limit`（默认 100，最大 10000）
- 响应包含 `has_more`（bool）和 `total_rows`（可选，已知时返回）
- 服务端**不得**在 `total_rows` 未知时进行全表 count

### 1.2 游标分页（WebSocket 大结果集）
- 服务端按 batch 推送（每批 ≤ 10000 行）
- 客户端通过 `query_cancel` 中断流
- 流结束后发送 end-of-stream 标记

### 1.3 Redis SCAN
- 使用 `cursor` + `count` 迭代
- `cursor = 0` 表示遍历结束
- `count` 为建议值，实际返回数量由服务端决定
- **服务端游标循环（D5，non-breaking）**：驱动单次 `scan_keys` 在内部以 `COUNT=count` 反复迭代 SCAN，
  直到①收集满 `count` 个键、②游标回到 0（遍历结束）、或③达到单调用 100 轮上限
  （防止选择性模式在大库上把单请求变成无界全库扫描）。返回的 `cursor` 仍是真实续点：
  0 恒表示遍历完成；非 0 时调用方原样续扫即可。前端"加载更多"语义不变，
  区别只在一次请求返回的是填满的一页，而非单轮 SCAN 的实际批次。两端实现必须严格一致。
- **当前 db 回读（D2，non-breaking）**：`RedisScanPage.db` 为可选整数，回显本次扫描所在的 db。
  两端驱动都持有**专用连接**（Redis 的 `SELECT` 是每连接状态，别的客户端改不动它），
  故驱动跟踪的值即服务端真相：`open` 时用连接配置的 db 初始化，`select` 成功后更新。
  驱动无法确定时**省略该字段**（不是 0）；前端省略时回落到上一次已知值，绝不把缺席当 db 0。

---

## 2. 取消

### 2.1 REST 查询
- 客户端在 `POST /api/connections/{id}/query` 的 body 中携带 `query_id`（uuid，必填）；服务端在响应头 `X-Query-ID` 回显该 id
- 未提供 `query_id` 时服务端自动生成一个 uuid，仍返回 `200`
- 客户端可通过 `POST /api/queries/{query_id}/cancel` 取消正在执行的查询（best-effort）
- 成功取消返回 `204 No Content`；对应 query 立即结束并以 `POLYDB_ERR_CANCELLED` 结束（HTTP 请求的响应体会是错误 JSON）
- 未知 `query_id` 返回 `404` 和 `POLYDB_ERR_QUERY_NOT_FOUND`
- 已完成或已取消的查询再次调用同样返回 `404`（幂等语义）
- 客户端断开 HTTP 连接**不**自动取消查询（与 §3 一致，客户端负责取消）
- `timeout_ms` 仍作为兜底；`timeout_ms = 0` 表示无超时

### 2.2 WebSocket 查询
- 客户端发送 `query_cancel { query_id }` 取消
- 服务端尽力取消（best-effort），回复 `query_cancelled`
- 取消后不再推送该 query_id 的任何消息
- 如果查询已完成但客户端未收到结果，`query_cancelled` 仍应发送
- `query_id` 必须在**同一连接内已提交过的 `query`** 中；未提交的 id 返回 `query_error`（`POLYDB_ERR_INVALID_PARAM`）
- 同一连接内可并发多个 `query`，取消只影响对应 `query_id`
- `timeout_ms` 生效：超时返回 `query_error`（`POLYDB_ERR_TIMEOUT`）
- **取消信号与完成信号分离**（M18 阶段 10）：**未被取消**的正常完成查询**不得**
  被误报为 `query_cancelled`。判定取消的依据是**显式的取消事件**（客户端
  `query_cancel` 或会话收尾），而非「取消通道被关闭」——因为查询正常完成时
  也要关闭取消通道以回收 watcher，两者若共用同一信号，完成与取消就会竞态
  （完成 → 注销 → 关通道 → watcher 误触发取消 → 结果被吞、客户端收到
  `query_cancelled`）。实现口径见 §16.15。

### 2.3 超时
- `timeout_ms = 0` 表示无超时
- 超时后返回 `POLYDB_ERR_TIMEOUT`
- 超时不自动重试

### 2.4 WebSocket 查询执行（M9）
- 控制面消息均为 msgpack；`query_result.result` 为 msgpack 对象形态（见 asyncapi.yaml）
- `hello` 未通过 `connection_id` 校验前，`query` 请求返回 `query_error`（`POLYDB_ERR_CONNECTION_NOT_FOUND`）
- `hello_ack.db_version` 为可选项，未取到数据库版本时缺省不发送
- `query_progress` 为**可选**消息：服务端不保证发送，客户端必须能处理零条 progress 的流
- 服务端对客户端连接断开负责清理在飞查询（客户端侧断开即等价于取消）
- 服务端不维护断线期间的查询状态（见 §3.1）

---

## 3. 重连

### 3.1 WebSocket 重连
- 客户端负责重连逻辑
- 重连后需重新发送 `hello` 消息
- 服务端不维护断线期间的查询状态
- 建议退避策略：指数退避，初始 1s，最大 30s，加随机抖动

### 3.2 数据库连接重连
- 连接断开时，服务端返回 `POLYDB_ERR_CONNECTION_FAILED`（`retryable: true`）
- 客户端可调用 `POST /api/connections/{id}/test` 重新建立连接
- 服务端连接池自动处理空闲连接的健康检查

---

## 4. 错误码表

| 错误码 | 含义 | retryable |
|--------|------|-----------|
| `POLYDB_ERR_UNKNOWN` | 未知错误 | false |
| `POLYDB_ERR_CONNECTION_FAILED` | 连接失败 | true |
| `POLYDB_ERR_CONNECTION_NOT_FOUND` | 连接 ID 不存在 | false |
| `POLYDB_ERR_CONNECTION_EXISTS` | 连接名已存在 | false |
| `POLYDB_ERR_AUTH_FAILED` | 认证失败 | false |
| `POLYDB_ERR_TIMEOUT` | 查询超时 | true |
| `POLYDB_ERR_QUERY_FAILED` | 查询执行失败 | false |
| `POLYDB_ERR_SYNTAX_ERROR` | SQL 语法错误 | false |
| `POLYDB_ERR_PERMISSION_DENIED` | 权限不足 | false |
| `POLYDB_ERR_SCHEMA_NOT_FOUND` | 模式不存在 | false |
| `POLYDB_ERR_TABLE_NOT_FOUND` | 表不存在 | false |
| `POLYDB_ERR_COLUMN_NOT_FOUND` | 列不存在 | false |
| `POLYDB_ERR_DUPLICATE_KEY` | 主键/唯一约束冲突 | false |
| `POLYDB_ERR_CONSTRAINT_VIOLATION` | 约束违反 | false |
| `POLYDB_ERR_DEADLOCK` | 死锁 | true |
| `POLYDB_ERR_TRANSACTION_FAILED` | 事务失败 | false |
| `POLYDB_ERR_TRANSACTION_NOT_FOUND` | 事务 ID 不存在或已结束 | false |
| `POLYDB_ERR_QUERY_NOT_FOUND` | 查询 ID 未知或已完成 | false |
| `POLYDB_ERR_INVALID_PARAM` | 参数无效 | false |
| `POLYDB_ERR_NOT_SUPPORTED` | 操作不支持 | false |
| `POLYDB_ERR_DRIVER_NOT_AVAILABLE` | 驱动未加载 | false |
| `POLYDB_ERR_SSH_TUNNEL_FAILED` | SSH 隧道失败 | true |
| `POLYDB_ERR_STORAGE_FAILED` | 本地存储失败 | false |
| `POLYDB_ERR_KEYRING_FAILED` | 密钥环操作失败 | false |
| `POLYDB_ERR_CANCELLED` | 操作已取消 | false |
| `POLYDB_ERR_UNAUTHORIZED` | 未认证（Bearer 缺失/不匹配） | false |
| `POLYDB_ERR_READ_ONLY` | 只读连接拒绝写操作 | false |
| `POLYDB_ERR_BLOB_NOT_FOUND` | blob 旁路引用不存在或已过期 | false |
| `POLYDB_ERR_PAYLOAD_TOO_LARGE` | 请求体超过服务端上限（HTTP 413） | false |

---

## 5. 默认值

| 参数 | 默认值 | 说明 |
|------|--------|------|
| 查询超时 | 30000ms | 无显式指定时 |
| 最大返回行 | 10000 | 超过则截断并设 `truncated: true` |
| 分页 limit | 100 | REST 列表接口 |
| 分页 limit 上限 | 10000 | 硬限制 |
| WebSocket batch 大小 | 10000 | 每批最大行数 |
| 连接池大小 | 5 | 每连接默认 |
| 空闲连接超时 | 300s | 连接池回收 |

---

## 6. 并发

- 同一连接可并发执行多个查询（服务端使用连接池）
- 事务内的查询必须串行（同一事务绑定一个连接）
- Redis 命令天然串行（单线程模型）
- 批量查询中 `stop_on_error: true` 时，遇到第一个错误后停止

### 6.1 双实现有意的不对称（登记册）

契约测试对拍的是**可观测行为**，拍不出"两侧用了不同算法/不同资源上限"这个事实本身。
因此下列三条不对称**显式立法**：任何一端改动使其中一条失效，都视同 spec 变更，
必须在 PR 里标注 breaking / non-breaking 并更新本节，不允许默默漂移。

> **历史注记**：原第 1 条（连接首次创建的归并）已于 2026-11-08 移除。Rust 侧
> `crates/app-core/src/lib.rs` 的 `ensure_connected` 已实现 `connect_flights`
> 归并（leader/follower + `OnceLock`，与 Go 侧 `singleflight.Group` 同构），
> 两端不再存在该维度的不对称。显式 `connect`/`Connect` 路径两端均不归并，
> 保持一致。

1. **连接建立的时机**
   - Rust：驱动构造用 `connect_lazy`（`crates/db-postgres`、`crates/db-mysql`），打开连接
     不触网，首条语句才建。
   - Go：各驱动的 `New` 内立即 `sql.Open` + `PingContext`（`pkg/dbmysql`、`pkg/dbpostgres`、
     `pkg/dbmssql`、`pkg/dboracle`、`pkg/dbredis`），因此**建连失败在创建连接时就返回**。
   - 可观测差异：`POST /api/connections` 的响应时间与失败时机。
2. **服务端连接池上限**
   - Rust：Postgres/MySQL 的 `connect_options().max_connections(4)`。
   - Go：仅 SQLite 设 `SetMaxOpenConns(1)`；MySQL/Postgres/MSSQL/Oracle **无上限**。
   - 可观测差异：慢查询并发下的排队与超时形态。
3. **WebSocket 订阅队列的背压**
   - Rust：`crates/server/src/ws.rs` 用 `mpsc::unbounded_channel`，慢客户端订阅无界排队，
     内存可无界增长直至客户端断开。
   - Go：`pkg/server/ws.go` 的 `writeCh` 缓冲 32，写满即阻塞发布方（会连带阻塞其他订阅者）。
   - 可观测差异：慢订阅者的内存曲线与对同通道其他客户端的拖慢形态。

---

## 7. 字符编码

- 所有文本使用 UTF-8
- 数据库返回的非 UTF-8 数据由驱动层转换（替换无效字符为 U+FFFD）

---

## 8. 密钥环（keyring）

- 密码绝不落库、绝不随 `ConnectionInfo` 返回。数据库只存 `password_ref`（keyring 键名）。
- `password_ref` 格式：`polydb:<作用域>:<uuid>`，作用域取 `conn` / `ssh` / `ssh-pass`。
- 创建/更新连接请求中的 `password`、`ssh_tunnel.password`、`ssh_tunnel.private_key_passphrase` 为**一次性明文**：
  - 服务端立即写入 keyring，之后丢弃请求体中的明文；
  - 创建时若提供了明文密码，服务端生成 ref 并回填 `ConnectionInfo`（Create/Update 响应不含明文）；
  - 更新时 `password` 为空/缺省表示保持原密码。
- 连接建立时服务端从 keyring 取回明文注入连接过程（不落库、不进日志）。
- keyring 后端：默认本地加密文件（AES-256-GCM，密钥派生自主密码），`POLYDB_KEYRING=os` 可选 OS 凭据管理器。
- 主密码来源：`POLYDB_MASTER_PASSWORD` 环境变量，或交互提示。
- keyring 操作失败返回 `POLYDB_ERR_KEYRING_FAILED`（`retryable: false`）。

## 9. SSH 隧道

- 连接配置了 `ssh_tunnel` 时，所有数据库连接通过隧道到达目标 `host:port`。
- 实现采用本地端口转发（等价 `ssh -L 127.0.0.1:<随机端口>:<目标host>:<目标port>`），对全部驱动通用，Rust/Go 行为一致。
- 认证优先级：`private_key_path` > `password`（从 keyring 取回）。
- 隧道建立失败返回 `POLYDB_ERR_SSH_TUNNEL_FAILED`（`retryable: true`）。
- 断开连接（`Disconnect`/`DELETE`）时关闭隧道。
- SQLite（本地文件）不适用隧道；配置了隧道时忽略。
- SSH 端口缺省为 22（配置为 0/未填时按 22 处理，双端一致）。
- **主机密钥校验（known_hosts，首用 TOFU，non-breaking 新增行为）**：
  - 文件：数据目录下 `known_hosts`（`POLYDB_DATA_DIR` 或平台数据目录），OpenSSH 兼容三列格式
    `host keytype base64`；host 字段约定：22 端口用裸 host，其余端口 `[host]:port`。
  - 连接时：该 host 已有条目且 key 一致 → 接受；条目存在但 key 不同 → **拒绝**
    （主机密钥变更 = MITM 信号，错误走 `POLYDB_ERR_SSH_TUNNEL_FAILED`）；无条目 →
    追加条目并接受（首次使用 / OpenSSH accept-new 语义）。
  - 文件记录失败（不可写等）不阻断连接；校验仅对「已知 host」有约束力。
  - 两端写出的条目互相可读（base64 同为 wire blob 的 std base64）。

## 10. 事务（M25）

### 10.1 生命周期

- 服务端以 `txn_id`（UUID）为事务唯一标识；`POST /api/connections/{id}/transactions` 创建事务并返回 `TransactionInfo{status: active}`。
- `POST /api/transactions/{txn_id}/execute` 在事务内执行 SQL，返回与常规查询同形的 `QueryResult`；`status` 保持 `active`。
- `POST /api/transactions/{txn_id}/commit` 与 `POST /api/transactions/{txn_id}/rollback` 各**只成功一次**；第二次调用返回 `POLYDB_ERR_TRANSACTION_NOT_FOUND`。
- 事务对象由服务端内存持有；服务端重启后所有活动事务丢失。
- 连接断开（`DELETE /api/connections/{id}`）会**静默丢弃**该连接上的所有活动事务；不做显式 rollback（驱动连接已释放）。

### 10.2 并发与隔离

- 同一事务内的查询**必须串行**（服务端在同一 handle 上 await）；不允许并发 execute。
- 同一连接上的多个事务可并发；由驱动底层连接池/会话隔离。
- `isolation_level` 缺省 `read_committed`；各驱动按 SQL 方言翻译（`BEGIN TRANSACTION ISOLATION LEVEL ...` / `SET SESSION TRANSACTION ISOLATION LEVEL ...` 等），非支持值视为非法参数（`POLYDB_ERR_INVALID_PARAM`）。
- SQLite 事务由驱动通过 `BEGIN IMMEDIATE` 显式开启（sqlite 库无独立事务对象）。

### 10.3 错误处理

- `execute` 中 SQL 失败：事务保持 active（客户端可自行 rollback）；错误通过常规错误响应返回。
- `commit` / `rollback` 失败：`TransactionInfo.status` 不更新，服务端丢弃该事务；调用方收到 `POLYDB_ERR_TRANSACTION_FAILED`。
- 未创建或已 finalize 的 `txn_id`：返回 `POLYDB_ERR_TRANSACTION_NOT_FOUND`（HTTP 404）。

### 10.4 与前端批量编辑的对齐

- Web 前端「批量编辑」面板：先 `begin_transaction`，逐行 `execute_in_tx`，最后 `commit_transaction` 或用户取消时 `rollback_transaction`。
- 前端展示 `TransactionInfo.isolation_level` 与 `started_at` 作为事务上下文提示。

## 11. 语句类型检测（statement_type）

- `QueryResult.statement_type` 由驱动层按**前缀规则**判定，判定前先剥离前导注释与空白（`--` 行注释、`/* */` 块注释），两端规则必须逐字一致：

  | 前缀（大写匹配） | statement_type |
  |---|---|
  | `SELECT` / `WITH` | `select` |
  | `INSERT` | `insert` |
  | `UPDATE` | `update` |
  | `DELETE` | `delete` |
  | `CREATE` / `ALTER` / `DROP` | `ddl` |
  | 其他 | `other` |

- 注意 `WITH ...` 前缀统一判 `select`（含 `WITH ... INSERT/UPDATE/DELETE` 形态）：这是 Go 侧既定规则，Rust 侧对齐；
  语义上是近似（CTE + DML 也标 select），换来的是两端 label 与执行分支（查询式 vs 命令式）的双一致，
  契约测试 `TestContractBehaviorMaxRows` 对该规则有对拍覆盖。

## 12. 鉴权与只读连接（M10，non-breaking）

### 12.1 服务端 Bearer 鉴权（可选启用）

- 服务端读取环境变量 `POLYDB_SERVER_TOKEN`；**未设置时不鉴权**（本机开发默认，历史行为不变）。
- 设置后，除以下两个豁免路径外，全部 `/api/*` 端点要求请求头 `Authorization: Bearer <token>`：
  - `GET /api/health`（探活必须无凭据可用）；
  - `GET /ws`（WebSocket 在 hello 阶段校验，见 §12.2）。
- 校验失败返回 **401** `{"code": "POLYDB_ERR_UNAUTHORIZED", ...}`（JSON 错误体）；不区分缺失与不匹配。
- 常量时间比较非目标（token 非用户凭据，直串比较即可）；token 不写入日志（§8 红线）。

### 12.2 WebSocket 鉴权

- 服务端启用 token 时，客户端 `hello` 必须携带 `auth: { token }`；缺失或不匹配：服务端发送
  `query_error`（`POLYDB_ERR_UNAUTHORIZED`）后**关闭连接**，不发送 `hello_ack`。
- 启用 token 时，完成认证 `hello` **之前**的一切 `query` 消息一律返回
  `query_error`（`POLYDB_ERR_UNAUTHORIZED`）并关闭连接——防止跳过 hello 直接携带
  `connection_id` 执行 SQL。未启用 token 时保持历史行为（query 可带显式 connection_id 先于 hello）。
- WebSocket 握手做 Origin 校验防跨站 WebSocket 劫持（CSWSH）：无 `Origin` 头（TUI/CLI 等
  非浏览器客户端）放行；`Origin` 的 host 与请求 Host 一致（同源，覆盖服务端自托管 Web 前端）
  放行；命中 `POLYDB_ALLOWED_ORIGINS`（逗号分隔，环境变量）白名单放行（vite dev 等跨源前端）；
  其余拒绝（HTTP 403 / 握手失败）。
- 未启用 token 时忽略 `auth` 字段（non-breaking）。

### 12.3 连接级只读（read_only）

- `ConnectionConfig.read_only = true`（默认 false）的连接，服务端在 **app-core 执行层**拒绝写操作
  （UI 禁用只是第一道，服务端拦截才是保证）：
  - SQL：`execute` / `execute_in_transaction` / batch 中语句做**整句变更关键字扫描**
    （不再复用 §11 判型），命中 `INSERT` / `UPDATE` / `DELETE` / `MERGE` / `REPLACE` /
    `TRUNCATE` / `GRANT` / `REVOKE` / `CREATE` / `ALTER` / `DROP` / `CALL` / `EXEC` /
    `EXECUTE` / `COPY` / `VACUUM` / `REINDEX` / `CLUSTER` / `LOCK` / `COMMENT` / `SETVAL` /
    `REFRESH` / `INTO` / `LOAD` / `DO` / `OPTIMIZE` 即返回 **409** `POLYDB_ERR_READ_ONLY`。
    扫整句使数据修改 CTE（`WITH … DELETE/UPDATE/INSERT`）与 `TRUNCATE` / `GRANT` / `CALL`
    等此前归入 `select` / `other` 的写型语句同样被拦；注释/字符串中的同名词不误判
    （`'delete'`、列名 `inserted_events` 均放行）。
    `SETVAL` / `REFRESH` / `INTO` / `LOAD` / `DO` / `OPTIMIZE` 是首关键字为 `SELECT` 或
    `REFRESH` 等、会被 §11 判型归入 `select` / `other` 的写型语句：`SELECT setval('seq', n)`
    （Postgres 推进序列）、`REFRESH MATERIALIZED VIEW` / `REFRESH TABLE`、
    `SELECT … INTO table` / `SELECT … INTO OUTFILE|DUMPFILE`（建表或落盘）、
    `LOAD DATA INFILE` / `LOAD XML`、`DO $$ … $$` 匿名块（可任意写）、`OPTIMIZE TABLE`。
    代价是 fail-closed 的误拦：`LOAD TABLE … INTO OUTFILE` 虽是读路径也会被拦；
    命中判定为整词，`setval_log` / `load_status` / `do_count` 等列名不受影响。
  - **扫描方式是方言感知的 token 词法扫描**（Go `pkg/dbcore/scan.go` 与 Rust
    `crates/db-core/src/scan.rs` 逐字镜像）：一次左到右扫描把输入分类成 token
    （裸词 / 引号标识符 / 字面量 / 注释 / 分号 / 其他），命中判定只读**裸词** token 的
    整词命中，绝不在「清洗过的文本」上做子串搜索。
    关键在于**「字面量 / 注释在哪结束」由方言决定**——认错方言就会把后面的写语句当成
    字面量吞掉（fail-open，直接丢数据）。因此扫描按连接的 `DatabaseKind` 取剖面：
    | 规则 | MySQL/MariaDB | PostgreSQL | Oracle | SQLite / SQL Server |
    |---|---|---|---|---|
    | `#` 是行注释 | 是 | 否（位异或运算符） | 否 | 否 |
    | `--` 需跟空白才是注释 | 是 | 否 | 否 | 否 |
    | `'…'` 里 `\` 是转义 | 是 | 仅 `E'…'` / `U&'…'` | 否 | 否 |
    | `$$…$$` / `$tag$…$tag$` 是字面量 | 否 | 是 | 否 | 否 |
    | `q'…'` 是字面量 | 否 | 否 | 是 | 否 |
    | `/*! … */` 会被服务端执行 | 是（当代码扫） | 否（普通注释） | 否 | 否 |
    方言未知时取各剖面**并集**（任一剖面认为写了就算写了）。
    反斜杠转义语义由**服务端**设置决定（MySQL 的 `NO_BACKSLASH_ESCAPES`、
    PostgreSQL 的 `standard_conforming_strings`），连接配置里看不到，故这两种方言各扫
    两个变体、判写取并集。代价是 fail-closed 的误拦：服务端用默认设置时
    `SELECT 'a\'; …` 这类写法会被判为写语句。
  - **MCP 侧（`run_readonly_query`）逐语句判定**，不做前缀匹配：按方言感知的切分器把脚本
    拆成语句，逐条要求「首关键字落在 `SELECT` / `WITH` / `EXPLAIN` / `SHOW` / `DESCRIBE` /
    `DESC` 白名单内」**且**「整条语句不含变更关键字」。两个条件缺一不可：只看首关键字会漏掉
    `EXPLAIN ANALYZE DELETE`（PostgreSQL 上 `EXPLAIN ANALYZE` 真执行）与数据修改 CTE；
    只看关键字扫描会漏掉首关键字不在白名单的语句。前缀匹配（`HasPrefix "SELECT"`）已被
    实测绕过并移除：`SELECT 1; DROP TABLE t`（多语句）、`WITH d AS (DELETE …) SELECT …`
    （数据修改 CTE）、`EXPLAIN ANALYZE DELETE …` 三条都能绕过旧闸门，其中第一条已端到端
    证实表被真删。
  - **只读红线纯在应用层**：两端均未下发 `SET TRANSACTION READ ONLY` /
    `default_transaction_read_only`，没有数据库层的第二道防线。因此闸门一律按 fail-closed
    收口——判错方向的代价不对称（多问一次只是烦，放过去就是丢数据），任何不确定的输入
    （未识别的引号形式、未知方言、转义语义不明）都往「判定为危险」倒。
  - **已知漏网（如实标注，未拦截）**：会话/实例级语句 `SET`（含 `SET GLOBAL` /
    `SET PERSIST` / `SET PASSWORD`）、`FLUSH`、`RESET`、`KILL`、`NOTIFY`、以及写型
    `PRAGMA`（如 `PRAGMA wal_checkpoint`）。这些不触及业务数据行，但可改写会话或实例
    配置；`read_only` 的连接若需覆盖它们，须在应用层另行限制，服务端当前不拦。
  - KV：`kv/keys/{key}` PUT（SetValue）与 `kv/exec`（ExecCommand）一律返回 409 `POLYDB_ERR_READ_ONLY`；
    GET / scan / select db 不受限。
- 事务内语句同样拦截（按事务所属连接判定）。
- 只读属性是连接配置的一部分，随 create/update 持久化并在 `ConnectionInfo.read_only` 回显。

## 13. 表数据浏览（M11，non-breaking）

### 13.1 端点与语义

- `POST /api/connections/{id}/schemas/{schema}/tables/{table}/rows/query`：按表浏览行。
  服务端把 `TableRowsRequest` 渲染为参数化 SQL——**条件值一律走绑定参数**，列名/表名按方言
  做标识符引用（内部引号翻倍），绝不拼接字面量。两端 SQL 形态不要求逐字节一致，行为一致即可。
- `POST .../rows/count`：对同一 WHERE 条件执行 `COUNT(*)`，返回 `TableCountResult`（精确计数，
  客户端显式动作才调用，不用作分页默认值）。

### 13.2 分页与 has_more

- `limit` 缺省 100（与 §1.1 及 schemas/common.json 一致），服务端钳制到 10000；`offset`
  缺省 0。分页用 OFFSET/LIMIT 语义
  （MSSQL/Oracle 用 `OFFSET n ROWS FETCH NEXT m ROWS ONLY`；MSSQL 无排序时补
  `ORDER BY (SELECT NULL)` 兜底）。
- `has_more`：服务端实际请求 `limit+1` 行，多于 limit 时截断并置 `has_more=true`。
- 无 `order_by` 时行顺序不承诺稳定；前端跨页保序应自带主键排序。
- `total_estimate` 可选；服务端拿不到廉价估算时省略，**不得**用 `COUNT(*)` 凑数。

### 13.3 过滤条件渲染（两端一致的行为约定）

| op | SQL 形态（`?` 为绑定参数） | 备注 |
|---|---|---|
| `eq` / `ne` / `lt` / `le` / `gt` / `ge` | `col = ?` 等 | |
| `like` / `not_like` | `col LIKE ?` / `col NOT LIKE ?` | 模式原样透传，不做 `%` 包裹、不做转义 |
| `in` / `not_in` | `col IN (?[, ?...])` | `values` 为空数组 → `POLYDB_ERR_INVALID_PARAM` |
| `between` | `col BETWEEN ? AND ?` | 用 `value` + `second_value` |
| `null` / `not_null` | `col IS NULL` / `col IS NOT NULL` | 忽略 value |
| `contains` / `starts_with` / `ends_with` | `col LIKE ? ESCAPE '!'` | 值先按 `!`→`!!`、`%`→`!%`、`_`→`!_` 转义，再分别包成 `%v%` / `v%` / `%v`；值缺失 → `POLYDB_ERR_INVALID_PARAM` |
| `empty` | `(col IS NULL OR col = '')` | 「空」含 NULL；忽略 value |
| `regex` | 见 §13.5 | 值必填；方言无原生正则 → `POLYDB_ERR_NOT_SUPPORTED` |

- 多条件用 `logic`（`and`/`or`，缺省 `and`）以同层平铺连接，不支持括号分组（前端需要复杂
  条件时引导用户写 SQL）。
- 未知 op / 空列名 / 非法列名（渲染失败）→ `POLYDB_ERR_INVALID_PARAM`。
- 列不存在由数据库报错，映射 `POLYDB_ERR_QUERY_FAILED`。
- `ESCAPE '!'` 而非 `ESCAPE '\'`：MySQL 字符串字面量里反斜杠自身要转义，`'\'` 会解析成空串，
  故选无歧义的 `!` 作为转义符，六方言语义一致。

### 13.4 驱动能力

- `SqlDriver` 新增 `browse_rows` / `browse_rows_count`；五个 SQL 驱动（sqlite/mysql/pg/mssql/oracle）
  双端全部实现，方言差异（标识符引用、LIMIT 风格）封在共享构造器 + 每驱动一份 dialect 声明。

### 13.5 大小写敏感与正则的方言渲染

`FilterCondition.case_sensitive`（可选布尔）**只作用于文本匹配类 op**：
`like` / `not_like` / `contains` / `starts_with` / `ends_with` / `regex`。
其余 op（`eq`/`in`/`between`/…）忽略该字段，避免对数值、日期列注入 `CAST`/`COLLATE` 而报错。

三态语义（向后兼容是第一位的）：

| 取值 | 语义 |
|---|---|
| 缺席 | **完全沿用 §13.3 形态**，不注入任何大小写处理，等价各方言默认排序规则（旧客户端零影响） |
| `true` | 强制区分大小写 |
| `false` | 强制不区分大小写 |

文本匹配类 op 的具体形态（`LIKE` 代表 `NOT LIKE` 同理，正则单列）：

| 方言 | `false`（强制不区分） | `true`（强制区分） | `regex` |
|---|---|---|---|
| PostgreSQL | `col ILIKE ?` | `col LIKE ?` | `col ~ ?` / `~* ?` |
| MySQL | `LOWER(col) LIKE LOWER(?) ESCAPE '!'` | `CAST(col AS BINARY) LIKE CAST(? AS BINARY) ESCAPE '!'` | `col REGEXP ?` / `CAST(col AS BINARY) REGEXP CAST(? AS BINARY)` |
| MSSQL | `LOWER(col) LIKE LOWER(?) ESCAPE '!'` | `col COLLATE Latin1_General_CS_AS LIKE ? ESCAPE '!'` | **无原生正则** → `POLYDB_ERR_NOT_SUPPORTED` |
| Oracle | `LOWER(col) LIKE LOWER(?) ESCAPE '!'` | `col LIKE ?`（默认即区分） | `REGEXP_LIKE(col, ?)` / `REGEXP_LIKE(col, ?, 'i')` |
| SQLite | `col LIKE ?`（ASCII 不区分，非 ASCII 区分） | `col GLOB ?` / `col NOT GLOB ?`（见下） | `col REGEXP ?`（UDF，见下） |

- **SQLite 强制区分走 GLOB**：GLOB 天然区分大小写，故把 LIKE 模式翻译成 GLOB 模式——
  `%`→`*`、`_`→`?`，并把 GLOB 的元字符按反斜杠转义（`\`→`\\`、`*`→`\*`、`?`→`\?`、`[`→`\[`）。
  `contains`/`starts_with`/`ends_with` 直接对原始值做该转义再包 `*`。GLOB 的转义符默认就是 `\`，
  不需要 `ESCAPE` 子句。
- **SQLite 的 `REGEXP` 是驱动注册的标量函数**（`rusqlite` `create_scalar_function` /
  modernc `RegisterDeterministicScalarFunction`），语义为 `x REGEXP y` ⇔ `regexp(y, x)`。
  两端在连接建立时注册同一实现：任一参数为 NULL → 结果为 NULL（与 SQL 三值逻辑一致）；
  非法正则由函数报错冒泡，映射 `POLYDB_ERR_QUERY_FAILED`。
  UDF 用各语言的正则库（Rust `regex` / Go `regexp`，同为 RE2 家族：无后向引用、无环视），
  因此强制不区分**不降级成 `LOWER()`**，而是给模式加内联标志：
  `col REGEXP ('(?i)' || ?)`——`lower()` 会破坏 `[A-Z]` 这类字符组，语义不等价。
- 强制不区分统一用 `LOWER(col) LIKE LOWER(?)`（除 PG 用 `ILIKE`、SQLite 用天然不区分的
  `LIKE`、SQLite 正则用 `(?i)`）：正确性优先于走索引，过滤栏本就是交互式收窄手段。
  代价是 MySQL/Oracle/MSSQL 的 `lower()` 只覆盖 ASCII，且正则形态的
  `LOWER(col) REGEXP LOWER(?)` 会把模式里的字符组一并小写化，即
  **非 ASCII 文本与含字符组的正则，其「不区分大小写」不保证**；
  MSSQL `COLLATE …_CS_AS` 只适用于文本列。
- 能力不足的组合必须**显式报错**（`POLYDB_ERR_NOT_SUPPORTED`），不得静默降级成不等价的 SQL；
  前端据 §13.6 的能力矩阵把不可用 op 置灰。

### 13.6 op × 方言能力矩阵（前端置灰依据）

| 方言 | contains / starts_with / ends_with / empty | regex | case_sensitive 三态 |
|---|---|---|---|
| sqlite | ✓ | ✓（驱动注册 UDF） | ✓（不区分仅 ASCII） |
| postgres | ✓ | ✓ | ✓ |
| mysql | ✓ | ✓ | ✓ |
| oracle | ✓ | ✓ | ✓ |
| mssql | ✓ | ✗ `NOT_SUPPORTED` | ✓（区分靠 `COLLATE`，仅文本列） |
| redis | 不适用（KV 无浏览过滤） | 不适用 | 不适用 |

---

## 14. 结构比对（Compare & Sync，E1/E2）

`POST /api/schema/diff` 把「两个已建立连接的库」比对成一份**有序、可勾选的变更计划**。
计划**只描述变更，不含 SQL**；语句生成属后续端点。双实现必须逐字一致，
锁定手段是共享向量语料 `test/schema-diff-vectors/*.json`（见 §14.7）。

### 14.1 快照采集

服务端为 `source` / `target` 各采一份快照：`list_tables` → 逐表 `list_columns` /
`list_indexes` / `list_foreign_keys`。视图与物化视图**不参与比对**（不报错、不产出条目），
但**名字要进快照**（`type` 标为 `view` / `materialized_view`，列/索引/外键为空，
采集时不为其发任何元数据查询）：生成器需要这份名单来避让临时表名并出视图告警（见 §15.3、§15.8）。
两侧连接必须是**同一种数据库**，且**不得为 Redis**。

### 14.2 归一规则（把「同一语义、不同写法」折叠成可比较的字面量）

只用 **ASCII 规则**：小写折叠只动 ASCII 字母，空白只认 ` \t\n\r\v\f`。这是两套实现能逐字
一致的前提；代价是**非 ASCII 标识符不做大小写折叠**（已知限制，不得悄悄扩大范围）。

类型归一（按数据库种类）：

1. 通用：`trim` → ASCII 小写 → 连续空白压成单空格 → 剥离标点旁空白
   （故 `varchar ( 20 )` ≡ `varchar(20)`、`numeric (10, 2)` ≡ `numeric(10,2)`、`text [ ]` ≡ `text[]`）。
2. MySQL：删整数显示宽度（`int(11)` → `int`，**只删宽度、保留类型名**）、删 `zerofill`。
3. Oracle：`bpchar` → `char`；`number(p,0)` → `number(p)`（仅 scale 为 0 时才算噪声）。

默认值归一：

1. `trim` → **引号外**小写（字符串字面量内容保持原样）→ 空白折叠；空串视为「无」。
2. 循环「剥最外层配对括号」+「剥尾部 `::type` 投射」直到不动点；投射取深度 0 的最后一个
   `::`，且尾部须逐词确认为类型名（支持 `character varying`、`numeric(10,2)`、`varchar[]`、`::a::b`）。
3. 结果为空串或 `null` → 视为无默认值，故 `NULL` / `((NULL))` 与缺省同义。

`options.ignore_defaults = true` 时，默认值维度整体不参与比较。

### 14.3 匹配规则（谁算「同一个对象」）

| 对象 | 匹配键 | 不匹配时的产出 |
|---|---|---|
| 表 | 小写表名 | `create_table` / `drop_table` |
| 列 | 小写列名 | `add_column` / `drop_column`；两侧都有但定义不同 ⇒ **一条** `alter_column`（`flags` 顺序固定：类型→可空性→默认值→自增） |
| 主键 | `IndexInfo.primary` 标志（**不比索引名**） | 列签名变化 ⇒ `drop_index` + `add_index` 重建对 |
| 非主键索引 | 小写索引名，名字为空时回落 `idx({列签名})` | 列签名或唯一性变化 ⇒ 重建对 |
| 外键 | 有名字用 `n:{小写名}`，名字为空用内容键 `c:{列}→{引用表}({引用列})` | 列 / 引用表 / 引用列 / `on_delete` / `on_update` 任一变化 ⇒ `drop_foreign_key` + `add_foreign_key` |

- 索引列签名 = 按 `position` 升序的列名小写逗号连接；**忽略 ASC/DESC 与前缀长度**。
- 引用表键小写；`referenced_schema` 非空且非默认时写作 `{schema}/{table}`，避免跨 schema 引用被误判为相同。
- 同名重复项以**第一条**为准；遍历顺序必须走有序容器/排序键，保证发射序确定。
- **明确不比**（v1 有意收窄，不得悄悄扩）：字符集/排序规则、列顺序、注释、非主键索引的纯改名、
  `generic_type`（跨种类比较因此直接报错，而非猜测）。
- 源侧是视图、目标侧是同名表 ⇒ 仍产出 `create_table`（不猜语义，执行期由数据库报错兜底）。

### 14.4 相位与排序

条目按 `(phase, lower(table), lower(object), id)` 升序输出，`phase` 固定为：

| 动作 | phase | | 动作 | phase |
|---|---|---|---|---|
| `drop_foreign_key` | 10 | | `create_table` | 50 |
| `drop_index` | 20 | | `add_column` | 60 |
| `drop_column` | 30 | | `alter_column` | 70 |
| `drop_table` | 40 | | `add_index` | 80 |
| | | | `add_foreign_key` | 90 |

即「先解除依赖再删、先建表再建列、列改完再建索引/外键」。`create_table` 段内还要按外键依赖做
拓扑排序（父表先建）：依赖图只认**本段内的新建表**、排除自引用，DFS 起点按表名小写键升序，
因此无环时父表必然先于子表，且输出与快照数组顺序无关。

**存在环时不存在任何可行的拓扑序**，实现按 DFS 到达序输出（不是名字序，也没有别的「兜底序」），
两端逐字一致。顺序承诺不了父子关系，就由语句说话：环上每张新建表的 `create_table` 都挂 §15.8
的成环告警。自引用（`t.user_id → t.id`）不算环，因为它不产生跨表顺序要求；指向**不在本段**的表
的引用也不算环。

### 14.5 `schema` 缺省与 SQLite 限制

- 请求里 `schema` 可省略；省略时由服务端按种类取默认（PostgreSQL `public`、其余按驱动默认）。
- **SQLite 驱动不做 schema 寻址**（`list_tables` 忽略入参、`PRAGMA table_info` 不支持 schema 前缀），
  故 SQLite 侧 `schema` 恒为 `main`，比对形态只能是**两个连接 / 两个文件**；
  「同库多 schema（ATTACH）」为后续待办，实现前不得在协议上暗示支持。

### 14.6 错误语义

| 场景 | 错误码 | 消息（双端逐字一致） |
|---|---|---|
| 两侧种类不同 | `POLYDB_ERR_NOT_SUPPORTED` | `跨种类比对未支持：{a} vs {b}（v1 只比同一种数据库）` |
| 任一侧为 Redis | `POLYDB_ERR_NOT_SUPPORTED` | `Redis 无模式概念，不参与结构比对` |
| 连接不存在/未连接 | 沿用 §4 既有连接类错误 | — |

### 14.6.1 `reason` 文案格式（契约，2026-11-10）

`reason` 是**用户可见**的人读原因，同时也是契约字段：双实现必须逐字一致。
此前它的内容只由语料锁定（见 §14.7），于是新增语料必须逐字知道措辞——
只能抄一端实现，等于把"一端回写"的循环论证保留在这一个字段上。
本节把它**升为格式契约**：给定两侧快照，文案就是可推导的。

#### 词汇表

| 记号 | 取值 |
|---|---|
| `null_w(b)` | `b` 为真 → `NULL`；否则 `NOT NULL` |
| `yes_w(b)` | `b` 为真 → `是`；否则 `否` |
| `def_w(d)` | `d` 为 null → `无`；否则取 `NormalizeDefault(d)` 的原文 |
| `act_w(a)` | `a` 为空串 → `无`；否则 `a` 原文（`no_action` / `cascade` / `restrict` / `set_null` / `set_default`）|
| `cols_lower(ns)` | 各名转小写后以 `,` 连接，**无空格** |
| `idx_sig(i)` | 列按 `(position, 小写名)` 升序排序，转小写后以 `,` 连接（同 §14.3） |
| `ref_table(fk)` | `referenced_schema` 为空 → `小写(referenced_table)`；否则 `小写(schema)/小写(table)` |
| `nrm(t)` | §14.3 的类型归一（`NormalizeType`） |
| `obj_index(i)` | 有名用 `name`；无名用 `idx(` + `idx_sig(i)` + `)` |
| `obj_fk(fk)` | 有名用 `name`；无名用 `fk(` + `cols_lower(columns)` + `)` |

**标点**：同一文案内的多个分句用**全角分号 `；`** 连接；句内箭头为 `→`
（U+2192）；`缺失/多余` 句内的逗号为全角 `，`。

#### 各 op 的模板

| op | 触发 | 文案 |
|---|---|---|
| `create_table` | 表仅在目标侧 | `表中缺失，需新建` |
| `drop_table` | 表仅在源侧 | `表中多余，需删除` |
| `add_column` | 列仅在目标侧 | `列缺失，需新增` |
| `drop_column` | 列仅在源侧 | `列多余，需删除` |
| `add_index` | 索引仅在目标侧 | `索引缺失，需新增` |
| `drop_index` | 索引仅在源侧 | `索引多余，需删除` |
| `add_index` + `drop_index` | 两侧同名索引，签名或唯一性不同 | 见「索引定义变化」 |
| `add_index` / `drop_index` | 主键列不同（成对产出） | `主键列变化，需重建` |
| `add_foreign_key` | 外键仅在目标侧 | `外键缺失，需新增` |
| `drop_foreign_key` | 外键仅在源侧 | `外键多余，需删除` |
| `add_foreign_key` + `drop_foreign_key` | 两侧同名外键，定义不同 | 见「外键定义变化」 |
| `alter_column` | `flags` 非空 | 见「列变更」 |

**索引定义变化**（成对的两条文案**完全相同**）：按下列顺序拼接**真正发生变化**
的分句，用 `；` 连接（无变化的分句整条省略）：

1. `idx_sig(源) != idx_sig(目标)` → `索引定义变化：{idx_sig(源)} → {idx_sig(目标)}`
2. `unique` 不同 → `唯一性 {yes_w(源)} → {yes_w(目标)}`

**外键定义变化**（成对的两条文案**完全相同**）：按下列顺序拼接**真正发生变化**
的分句，用 `；` 连接：

1. 本侧列不同 → `列 {cols_lower(源)} → {cols_lower(目标)}`
2. `ref_table` 不同 → `引用表 {ref_table(源)} → {ref_table(目标)}`
3. 引用列不同 → `引用列 {cols_lower(源引用列)} → {cols_lower(目标引用列)}`
4. `on_delete` 不同 → `ON DELETE {act_w(源)} → {act_w(目标)}`
5. `on_update` 不同 → `ON UPDATE {act_w(源)} → {act_w(目标)}`

**列变更**（`alter_column`）：按 `flags` 的规范顺序
（`type` → `nullability` → `default` → `auto_increment`）拼接**真正发生变化**
的分句，用 `；` 连接：

| flag | 分句 |
|---|---|
| `type` | `类型 {nrm(源)} → {nrm(目标)}` |
| `nullability` | `可空性 {null_w(源)} → {null_w(目标)}` |
| `default` | `默认值 {def_w(源)} → {def_w(目标)}` |
| `auto_increment` | `自增 {yes_w(源)} → {yes_w(目标)}` |

> `type` 分句里的两侧类型都是 `NormalizeType` 之后的值（`from`/`to` 字段同源），
> 因此 MySQL 整数显示宽度不会出现在文案里。

#### 为什么这值得进契约

- **语义**：`reason` 是用户读到的变更说明，措辞不一致即体验不一致；
- **机械性**：有了格式，文案就能从快照推导 ⇒ **diff 语料可独立扩充**，
  不再需要抄一端实现；
- **可验证**：`tools/oracle/diff_vectors_oracle.py` 现在连 `reason` 一起推导，
  与语料**逐字**比较（此前该字段被显式排除）。

### 14.7 一致性锁定

`reason` 文案的**格式**由 §14.6.1 定义（可推导），`test/schema-diff-vectors/*.json` 只是它的回归锁；条目排序与归一结果同样由该语料同时喂两端锁定。
**改规则必须同步改语料**：语料的 `expected` 由 Rust 实现生成后**逐条人工复核**再提交，
Go 侧只读不断言写回；两份错误向量的 `error_code`/`error_message` 保持手写。

## 15. DDL 语句生成（Compare & Sync，E3）

`POST /api/schema/ddl` 把 §14 计划里**被勾选的条目**翻译成一份可执行语句脚本，
并附同一份回滚脚本。本层**只生成语句，不执行**；执行由调用方显式走既有
`/api/transactions/*` + `/api/query` 通道发起。双实现逐字一致，
由共享语料 `test/schema-ddl-vectors/*.json` 锁定（见 §15.10）。

### 15.1 输入形态：回传连接 + 勾选 id，服务端重算

请求体只带 `source` / `target`（两个已连接的连接 + schema）、`options`、`selected`（条目 id 数组）。
服务端**重新采集快照、重新比对**，再用 `selected` 过滤——绝不接收客户端传来的计划：
两侧结构可能在勾选后又变了，拿旧 id 解释新计划会生成语义上错误的语句。
`options` 必须与比对时同一份，回滚方向用它镜像重算，才能保证正/反向严格对称。

`selected` 为**空数组**时产出空脚本：`statements` 与 `rollback` 都是 `[]`，**不得为 `null`**
（线路层的 nil/空由 DTO 的序列化选项保证，语料 d01 锁定）。

### 15.2 方言范围：v1 只 SQLite

只生成 **SQLite** 方言。其余种类一律 `POLYDB_ERR_NOT_SUPPORTED`，不做「近似翻译」：

| 场景 | 错误码 | 消息（双端逐字一致） |
|---|---|---|
| 非 SQLite 种类 | `POLYDB_ERR_NOT_SUPPORTED` | `{kind} 方言的 DDL 生成在 v1 未实现（当前只生成 sqlite）` |
| 任一侧 Redis | `POLYDB_ERR_NOT_SUPPORTED` | `Redis 无模式概念，不参与结构比对` |
| 两侧种类不同 | `POLYDB_ERR_NOT_SUPPORTED` | `跨种类比对未支持：{a} vs {b}（v1 只比同一种数据库）` |

### 15.3 直改语句与整表重建

SQLite 的 `ALTER TABLE` 能力极窄（取证 G1–G4），故按动作分两路：

**直改**（一条语句搞定）：`create_table`、`drop_table`、`add_column`、`drop_column`、
`add_index`、`drop_index`。其中 `drop_column` 仅在该列**未被索引引用、也不是主键列**时可直改
（否则 SQLite 报 `error in index … after drop column`，取证 G2）。

**整表重建**（凡改表定义本体的一律走重建）：
`alter_column`（SQLite 无 `ALTER COLUMN`）、`add_foreign_key` / `drop_foreign_key`
（无 `ADD/DROP CONSTRAINT`）、以及**主键索引与 `sqlite_` 前缀的 autoindex** 的 `add_index` /
`drop_index`（这类索引由表级 `PRIMARY KEY` / `UNIQUE` 隐式生成，不能单独建删）。重建固定四步
（外加目标侧用户索引）：

1. `CREATE TABLE "polydb_tmp_{表名}"(…)`——按**目标快照**的完整定义生成，含表级 `PRIMARY KEY` /
   `UNIQUE` / `FOREIGN KEY`；
2. `INSERT INTO "polydb_tmp_{表名}"(同名列) SELECT 同名列 FROM "原表"`——只搬两侧**都存在**的列，
   因此新增列取默认值/NULL、被删列自然丢弃；
3. `DROP TABLE "原表"`；
4. `ALTER TABLE "polydb_tmp_{表名}" RENAME TO "原表"`；
5. 目标侧的每个**用户索引**补一条 `CREATE [UNIQUE] INDEX …`（autoindex 已并进第 1 步）。

临时表名与任一侧快照里的**表名或视图名**重名时追加序号（`polydb_tmp_t_2`），避让规则确定，
双端一致（视图名占位是取证 G17：临时表撞同名视图时首条 `CREATE TABLE` 直接报 `already exists`）。

**重建组的输出位置**：整组语句出现在该表**第一个**条目的相位上（不是 `alter_column` 的 70），
`entry_ids` 吸收该表在本次勾选里的**全部**条目 id，只有组内**首条**语句挂重建告警。
未命名索引/外键在快照里被 E1 合成为内容签名（`idx(a,b)` / `fk(user_id)`），落到语句上时
索引取**可执行名**（`polydb_idx_{表}_{列}`），不能把签名当名字用。

### 15.4 PRAGMA 包裹与事务

任一脚本含重建 ⇒ 整份脚本首尾各包一条 `PRAGMA foreign_keys=OFF;` / `PRAGMA foreign_keys=ON;`
（相位 0 与 255，`entry_ids` 为空），并把 `pragmas_outside_tx` 置 `true`：
`PRAGMA foreign_keys` 在事务内是 **no-op**（取证 G6），这两条必须在事务外执行。
`ddl_in_tx` 对 SQLite 恒为 `true`（取证 G7：DDL 可进事务，失败可整体回滚）。
不含重建的脚本 `pragmas_outside_tx = false`，整份可直接放进一个事务里。

### 15.5 勾选规则（两条，违反即 `POLYDB_ERR_INVALID_PARAM`）

1. **过期守卫**：`selected` 里每个 id 必须真实出现在本次重算出的计划里，否则
   `勾选的条目不在当前比对计划里：{id、id…}（请重新比对后再执行）`（去重、按给定顺序连接）。
2. **重建整组**：进入重建路径的表，其**全部**条目必须一起勾选——重建输出的是整张目标定义，
   漏勾的同表变更会被**静默应用**。报错文案
   `表 {T} 需要整表重建，同表变更必须整组勾选：缺少 {id、id…}`（缺失 id 按计划出现顺序）。

### 15.6 回滚脚本 = 互换后按同一套规则再生成

`rollback` 由 `Diff(target, source, opts)` 里与正向条目**镜像同名**（id 只替换末段 op）的条目
生成，规则与 §15.3 完全相同，因此不可逆标记也打在回滚语句上（正向 `create_table` 的回滚是
`drop_table`，它自身就是不可逆动作）。

**回滚承接正向的重建**：正向重建过的表，回滚方向**强制**也走重建。原因是正向一旦重建，
原表定义已不在库里，直改语句（如 `ADD COLUMN` + `CREATE INDEX` 分开）补不回被重建丢弃的
对象与约束顺序——只重建才能把整张表还原回源侧定义。

回滚脚本是**建议脚本**：其中标了 `non_reversible` 的语句一旦执行就无法再回退，
故调用方必须自存正向执行记录，不得把 `rollback` 当成可反复使用的 undo。

### 15.7 不可逆判定

一条语句承载的条目里只要有**任一条**不可逆，整条语句标 `non_reversible = true`：

| 动作 | 不可逆 | 理由 |
|---|---|---|
| `drop_table` | 是 | 表数据没了 |
| `drop_column` | 是 | 列数据没了 |
| `alter_column` 带 `type` / `nullability` 维度 | 是 | 值被转换或收窄，原值不保证还在 |
| `alter_column` 只带 `default` | 否 | 定义变了，数据没动 |
| 其余（建表/加列/索引/外键的增删） | 否 | 结构可反向增删 |

### 15.8 告警文案（快照照不到的一律挂警告，不静默）

| 常量 | 文案 | 挂在哪 |
|---|---|---|
| `W_TRIGGERS` | 重建表会丢失该表上的触发器（快照不含触发器） | 重建组首条 |
| `W_GENERATED_COLUMNS` | 生成列会退化为普通列（快照不标注生成列） | 重建组首条 |
| `W_CHECK_CONSTRAINTS` | CHECK 约束会丢失（快照不含 CHECK 约束） | 重建组首条 |
| `W_AUTOINCREMENT` | 主键整数列的 AUTOINCREMENT 关键字无法从快照还原 | 重建/建表且整数主键 |
| `W_CREATE_TABLE` | 新建表只按快照里的列定义生成，不含 CHECK 约束、生成列与触发器 | `create_table` |
| `W_DROP_COLUMN_CHECK` | 该列若被 CHECK 约束引用，删除后约束会一并消失（快照不可见） | `drop_column` |
| `W_ADD_COLUMN_LIMITS` | SQLite 的 ADD COLUMN 只接受常量默认值，非空表上加 NOT NULL 且无默认值的列也会报错 | 非常量默认值或 NOT NULL 无默认的新列 |
| `W_REBUILD_NEW_NOT_NULL` | 重建只复制两侧同名列，新增的 NOT NULL 列若无默认值会让数据搬运 INSERT 报错 | 重建组首条 |

另有三条**带名单的动态文案**（名单按小写升序、最多点 5 个名字、超出以「等」收尾）：
前两条的名单取自 §14.1 进快照的**视图名**，第三条取自环上的**表名**：

| 文案 | 挂在哪 |
|---|---|
| `本库现有 {N} 个视图（{名单}）：DROP TABLE 不检查依赖，语句会成功，但引用它的视图将失效（快照不含视图定义，判定不了是哪一个）` | `drop_table`（取证 G16：原先一条警告都没有） |
| `本库现有 {N} 个视图（{名单}）：重建经 RENAME，而 SQLite 改名时会重解析依赖该表的视图 ⇒ 有视图引用它就整条重建报错` | 重建组首条，排在其余重建告警之后（取证 G13） |
| `外键依赖成环：{N} 个表（{名单}）互相引用，建表顺序无法保证父表在前（SQLite 到写入时才报错）` | 环上每张 `create_table`，排在 `W_CREATE_TABLE`/`W_AUTOINCREMENT` 之后（取证 G18：原先成环时静默） |

告警按**被执行的那一侧**出：回滚方向会互换两侧，故回滚脚本的视图警告描述的是回滚真正要动的库。
名单为空时整条告警不出现。成环告警只挂**建表侧**：回滚把 `create_table` 换成 `drop_table`，
删表没有父表先后的问题，故不挂。`drop_column` **不挂**视图告警：删被视图引用的列时引擎自身就报错
（实测 `error in view v_orders after drop column: no such column: qty`），按 §14.3「不猜语义、
执行期报错兜底」无需生成器多嘴；而 `DROP TABLE` 引擎**不报错**，所以必须由生成器说话。

告警只作提示，**不做执行前拦截**：真正的失败由数据库在执行期报错兜底。
新增成环告警为 **non-breaking**：只在 `Statement.warnings` 里多一句文案，语句文本、条目顺序、
接口字段全不变。

### 15.9 其余错误

计划引用了快照里不存在的表/列/索引（快照被手工裁剪或采集不全）时，
返回 `POLYDB_ERR_INVALID_PARAM`，消息 `快照中缺少表/列/索引 …，无法生成 DDL`；
重建路径专属动作（`alter_column` 等）单独成语句属实现缺陷，报
`{op} 动作必须走重建路径，不该单独成语句`。

### 15.10 一致性锁定

`test/schema-ddl-vectors/*.json` 一份语料同时喂两端，跑完整管线 **diff → SelectEntries → Generate**，
逐字段比对（含中文 `warnings`、`non_reversible`、两个 pragmas 位）。
27 份语料 = 19 份脚本（覆盖 §15.3 全部直改与重建路径、autoindex、无名索引/外键、临时表避让
（表名与视图名）、重建组吸收同表条目、两条视图告警含超 5 个的截断渲染、外键成环告警）+ 8 份错误（4 种方言、
Redis、跨种类、过期 id、未整组勾选）。
覆盖度是**断言**：9 个动作、8 个告警常量、3 条动态告警（按句头子串判，因为名单是动态的）、
可逆/不可逆两值、`pragmas_outside_tx` 两值必须全被打到。

`expected` 由 Go 侧生成器回写（`POLYDB_DDL_VECTORS_WRITE=1 go test ./pkg/schemadiff -run WriteDDLVectorExpected`，
只替换文件里 `"expected"` 这一个 JSON 值，其余逐字节不动），SQL **必须人工复核**后才提交；
Rust 侧加载器只做只读验证，构成第二实现。语料里 `selected: null` 表示「全部条目」，
`[]` 表示「一条都不勾」，两者语义不同，写死在加载器里。

---

## 16. 进程外 driver broker（M18，non-breaking）

本节定义第三方数据库驱动如何**不进入封闭 `DatabaseKind` 集合**、以独立进程经
WebSocket 异步契约接入。契约类型见 `spec/schemas/driver-broker.json`，消息见
`spec/asyncapi.yaml` 的 `broker_register` / `broker_revoke` / `broker_state` /
`broker_event`。内置驱动与既有查询消息**不变**；本节的 broker 是叠加在
`app-core` 之外的一层，不替换 `open_driver` / `OpenDriver` 分派。

### 16.1 为什么是进程外 broker，而不是进程内 dlopen

- **系统边界**（§6.1）：SQL 执行、事务提交、结果推送都是 **emission**——数据一旦
  通过通道离开系统，`id_Γ` 既不可追踪也不可撤销（「a message already sent stays
  sent」）。只有 acquisition（安装登记记录）在系统边界内、可配逆。把 provider 放进
  独立进程，宿主的边界止于 bridge；已发出的 SQL 用 withhold / compensation 处理，
  且 compensation 的 commutation 必须在更粗的等价关系上重新建立，元理论不自动继承。
- **异步契约**（§6.2）：跨进程暴露的接口「must be designed against an asynchronous
  contract」。本项目现成的 `spec/asyncapi.yaml` WS 查询流（含 `query_cancel`）即该契约，
  不新增同步 RPC。
- **隔离与能力**（§6.3）：language-level access control 不足以约束本机代码；独立进程
  提供执行边界，宿主侧 bridge 是普通 fiber，其能力可被 interception 衰减。明文密码
  不下发给 provider。
- **双实现一致性**：进程内 cdylib 天然只有 Rust 一侧，无法与 Go 端对拍，违反 §7 红线。
  经 spec 契约接入的进程外 provider 两端各有一个 broker 实现，契约测试仍可覆盖。

### 16.2 模型：broker 常驻，provider 作 backing fiber

- **broker 是唯一常驻 provider**，消费者只依赖它。backing provider 的装卸被 broker
  吸收（「the broker absorbs this perturbation」），消费者看不到依赖变化，**不触发
  reload**（§6.2）。这是选 broker 而非 exclusive binding 的全部理由。
- provider 的标识是 **namespaced key** `P:K`（`P` = 定义接口的包，`K` = 驱动种类），
  按 §6.6 的 key namespacing `K → K × P` 消除 key collision。**不得**等于任何内置
  `DatabaseKind` wire 值；`kind` 字段只是方言信息，不参与分派。
- `protocol_version` 先探、再解引用：版本不符时 broker 拒绝注册，不去解释函数表
  （`docs/plugin-abi.md` §2.3 的同口径）。

### 16.3 时间维度：注册是可撤销 effect

- 每次 `broker_register` 是一个 **tracked effect**：broker 把「撤销该 key 的登记」
  作为显式逆 `φ` 持有。加载 = 跑一步并累积逆；卸载 = **LIFO** 应用 `φ`（§3.1
  twisted composition、Thm 16）。`ctx.set(key, value)` 本身就是 `ctx.effect` 调用，
  coeffect 操作是 effect、effect 可逆（§3.2 的 synergy）。
- **recovery exactness**（Thm 68）：一个 provider 的 accumulator 只撤回它自己的贡献，
  即便其它 provider 的装卸与之任意交错。因此 `broker_state` 的终态与「按最终配置
  一次性静态装配」一致（Thm 80 汇流），与注册顺序无关。
- 每个 provider 的 `reliance` 记录绑定到它的在途操作数。**决定与动作分离**：
  `broker_revoke` 只做决定；真正应用 accumulator 的动作受 `¬ relied_n(γ)` 守卫，
  `reliance > 0` 时拒绝撤回并回 `broker_event(deactivating)` + `reason`。
  provider 的生命周期严格包住其消费者（Thm 70）。

### 16.4 空间维度：声明、满足与通知

- `DriverProvider.capabilities` 是 **coeffect specification / inject 声明**，即
  capability request；它静态可知，broker 在 provider 运行前即可审查（§6.3）。
- `DriverProviderState.granted_capabilities` 是 **interception** 之后的生效集合，
  必为 `capabilities` 的子集。因为 interception 只改变「依赖**如何**被调用」、
  不改变「是否被满足」，它可在运行时原地改写，**不触发 reload、不扰动依赖图**
  （§6.3；实现上对应 Cordis §5.2.1 的 `intercept` 原地更新）。典型用法：不可信
  provider 只获 `query` / `metadata`，核心 provider 保留 `execute` / `transaction`。
- `notify_d` 把每次上下文变化分类为 `DriverBrokerEvent`（Def 22）：
  - `activating`：`broker_register` 使某声明从「不满足」变「满足」；
  - `deactivating`：`broker_revoke` 使某声明从「满足」变「不满足」；
  - `neutral`：**仅** `granted_capabilities` 改变，声明仍被满足 → 不发
    `broker_state` 之外的事件、不触发消费者重载。
- **cannot provide twice / revoke absent**（Def 19）：同一 key 二次注册、或撤回
  未登记的 key，都是错误且**不产生转移**；回 `broker_state` 的 `error` 字段，
  错误码 `POLYDB_ERR_INVALID_PARAM`。

### 16.5 消息与错误

| 消息 | 方向 | 语义 |
|---|---|---|
| `broker_register` | 客户端 → 服务端 | 提交 `DriverProvider`；回 2 帧（见下） |
| `broker_revoke` | 客户端 → 服务端 | 提交 key；回 2 帧（见下） |
| `broker_state` | 服务端 → 客户端 | 全部 `DriverProviderState` 视图；失败时 `error` 带 `PolyDBError` |
| `broker_event` | 服务端 → 客户端 | `notify_d` 的分类结果，带 key 与可选 `reason` |

**统一 2 帧响应（M18 阶段 2 起，双端严格一致）**：每个 `broker_register` /
`broker_revoke` 请求**恰好**回两帧，先 `broker_state` 再 `broker_event`：

- `broker_state.providers` = 操作后的终态视图（失败时为操作前的视图，因为失败
  **不产生转移**）；`broker_state.error` = 失败时的 `PolyDBError`，成功为缺席。
- `broker_event.event` = `notify_d` 分类：注册成功 `activating`、撤回
  （成功或守卫拒绝）`deactivating`、其余失败 `neutral`；
  `broker_event.key` = 请求的 key；`broker_event.reason` = 失败时的错误文案
  （取 `error.message`），成功为缺席。

由此消除「成功回几帧、守卫拒绝回几帧」的歧义，契约测试可按固定 2 帧归集，
不依赖帧序假设之外的实现细节。

- 错误一律复用既有 `PolyDBError` 与既有错误码（下表），**不新增 `POLYDB_ERR_*`**：
  `POLYDB_ERR_INVALID_PARAM`（重复注册 / 撤回未登记 / 版本不符 / 能力集合非法）、
  `POLYDB_ERR_DRIVER_NOT_AVAILABLE`（`reliance > 0` 时守卫拒绝撤回）。
- 消息一律 **MessagePack**（§5 编码约定），JSON 仅调试。broker 消息**不携带
  `query_id`**：它不是查询，不进 `in_flight` 在途表。
- **鉴权**：broker 消息**不受 §12.2 的 query 认证门禁**约束，无需先 `hello`。
  理由（§6.3）：访问控制由 capability attenuation 承担（不可信 provider 只获
  只读能力），而非连接鉴权；`broker_register` 只登记一条 key/capabilities 记录，
  不接触用户数据、不执行 SQL。第 3 阶段接真实转发时在 bridge 处另行收紧。
- **已知双实现不对称（登记册）**：`broker_register` 缺 `provider` 字段时，
  - Go 的拍平 struct 解码成功（`Provider == nil`），由 arm 显式回
    `broker_state` + `broker_event(neutral)`，`error.code = POLYDB_ERR_INVALID_PARAM`；
  - Rust 的 tagged enum 要求该字段，解码即失败，回 `query_error` +
    `POLYDB_ERR_INVALID_PARAM`（`decode: …`）。
  两端错误码一致、失败均不产生转移，但**承载帧不同**。成因是 §6.1 已登记的
  「Rust tagged enum vs Go 拍平 struct」形态差异，不是 broker 逻辑漂移；
  彻底统一需扩 `DriverProvider` 的 wire 形态（breaking），留待后续。
  契约测试 `TestContractBroker` 只覆盖**合法**输入，不锁定此畸形分支的帧型。

### 16.6 双实现与门禁

- Rust 落点：`rust/crates/app-core/src/driver_broker.rs`（组合语义）+
  `rust/crates/server/src/lib.rs::AppState`（进程级 `Arc<DriverBroker>`）+
  `rust/crates/server/src/ws.rs::receive_loop`（收发接线）；Go 落点：
  `go/pkg/appcore/driver_broker.go` + `go/pkg/server/server.go::Server` +
  `go/pkg/server/ws.go::runInbound`。两端暴露同一组操作（register / revoke /
  set_granted / snapshot / reliance），由各自单测与 `test/contract/` 双端对拍锁定；
  线格式由 `tools/genprotocol` 的四门禁钉死。
- **进程级注册表**：broker 挂在 server 状态上（Rust `AppState` / Go `Server`），
  一个进程一个实例，**跨 WS 连接可见**——否则不同连接看到不同依赖图，broker
  就不成立。会话结束只释放该会话对 `Arc` 的引用，不销毁进程级视图。
- 新增 provider **不需要**改 `spec/schemas/common.json` 的 `DatabaseKind` 枚举，
  因此 `genprotocol dispatch` 门禁不受影响（它只钉内置 kind ↔ 驱动 crate）。
- 接线只新增 `receive_loop` / `runInbound` 的 broker arm；阶段 3 起 `query`
  携带可选 `provider_key` 时进入 §16.8 的绑定路径，**不带该字段的查询路径逐字
  不变**。`test/contract/` 的现有 WS 场景保持原样，新增 `TestContractBroker`
  与 `TestContractBrokerQueryGuard` 场景。

### 16.7 明确不在本切片范围（避免假装覆盖）

- 不实现 cdylib / C ABI / SHA-256 信任门禁（`docs/plugin-abi.md` 仍是未实现的
  知识保全）；
- **不实现跨 OS 进程的真实 provider**：阶段 4 的参考 provider 仍是同一进程内
  的 WS 会话（契约测试用双连接模拟），验证的是**异步契约与组合语义**，不是
  真实跨进程 RPC（§16.9 决策）。真正的跨进程 transport（stdio / socket /
  WebSocket 外连）与序列化边界属后续；
- 不实现连接池、SSH 隧道、keyring 下发；
- **不重新证明 compensation 的 commutation**：阶段 4 实现 withhold 与
  best-effort compensation，但按 §6.1 的警告，commutation 是针对 `≃` 证明的，
  补偿动作在更粗的等价关系上必须重新建立；本阶段只把边界写进契约、由单测
  锁定可观测行为，**不宣称元理论成立**（§16.9.5）；
- 不实现 broker 消息的连接级鉴权（见 §16.5 决策）；
- 不解决 §6.6 的 peer dependency 单版本解析 / interface drift 的完整模型
  （namespacing 只消 key collision）。

### 16.8 查询绑定：能力校验与 `¬ relied_n(γ)`（M18 阶段 3，non-breaking）

`QueryRequest` 与 asyncapi `Query` payload 新增**可选** `provider_key`。
**缺席时零行为变化**：仍走内置驱动，不查 broker、不占 reliance。携带时：

1. **能力校验（interception 生效点，§6.3）**：broker 查该 key 的
   `DriverProviderState`；若未注册 → `POLYDB_ERR_DRIVER_NOT_AVAILABLE`；若
   `granted_capabilities` 不含 `query` → `POLYDB_ERR_PERMISSION_DENIED`。
   校验发生在 `QueryStarted` **之前**，失败不发 `QueryStarted`、不进
   `in_flight`、不 `acquire`。这是 interception「在依赖被调用时决定请求是否
   被允许」的落点：策略可运行时改写（阶段 2 的 `set_granted`），无需 reload。
2. **reliance 绑定**：校验通过后 `acquire`，查询**整个在途期**持有一次
   reliance；`¬ relied_n(γ)` 守卫因此会拒绝这期间的 `broker_revoke`
   （`POLYDB_ERR_DRIVER_NOT_AVAILABLE`，`broker_event(deactivating)` + reason）。
   这正是 Thm 70：provider 的 episode 严格包住其消费者的 episode。
3. **不漏的 release（RAII）**：`acquire` 与 `release` 必须由同一个
   scope guard 包住，覆盖**所有**出口——正常完成、执行错误、cancel、
   timeout、二次 cancel 竞态、panic。Rust 用 `Drop` guard，Go 用 `defer`；
   禁止逐个出口手写 `release`（遗漏即 reliance 永久泄漏，守卫再也不放开）。
4. **会话卸载 = 应用 accumulator（Thm 68 的运行时落地）**：每个会话记录
   自己**成功注册**的 keys（失败注册不记）。WS 连接关闭时，先 `cancel_all()`
   通知在途查询，再对本会话每个 key 尽力 `revoke`：
   - 若无在途绑定 → 逆被应用，该会话的登记被精确撤回，其它会话的登记不动；
   - 若仍有在途绑定 → 守卫**拒绝**（不绕过），登记及其逆保留在 accumulator，
     由后台查询完成 `release` 后后续请求可再撤回。这是正确语义，不是缺陷：
     provider 不能先于消费者消失。
   - 会话只撤自己注册的 key，不撤其它会话的（recovery exactness）。
5. **执行仍走内置驱动**：本阶段 `provider_key` 是**绑定与策略**的载体，
   不是 SQL 路由。理由（§6.1）：真实转发是 emission，跨进程必须按异步契约
   设计并重新建立 compensation 的 commutation；在桥接层就绪前不假装覆盖。
   该决策与 §16.7 的排除项互相锁定。

**契约测试覆盖**（`test/contract/broker_test.go`）：能力衰减后查询被拒
（`PERMISSION_DENIED`）、未注册 key 被拒、在途慢查询期间 `broker_revoke`
被守卫拒绝、查询完成后可撤回、断连后本会话登记被自动回收而其它会话的
登记保留——全部双端 parity。

### 16.9 异步 SQL 转发（M18 阶段 4，non-breaking）

阶段 4 把 `provider_key` 从「绑定与策略」升级为**真实的 SQL 路由**：
携带该字段的 `query` 不再调内置驱动，而是经 §6.2 要求的**异步契约**转发给
attached provider。**不带 `provider_key` 的查询路径逐字不变**。

#### 16.9.1 四条消息（asyncapi `channels.query.messages`）

| 消息 | 方向 | 语义 |
|---|---|---|
| `provider_attach` | provider → 宿主 | **acquisition**：把本 WS 会话登记为某 key 的执行端。宿主保存 `(key → session sender)`；重复 attach 同一 key 被拒（`POLYDB_ERR_INVALID_PARAM`）。attach 不校验 `broker` 注册（provider 可先 attach 再 register，也可只 register 不 attach）。 |
| `provider_detach` | provider → 宿主 | acquisition 的逆：移除登记。`¬ relied_n(γ)` 语义由在途表承担：有在途 exec 时拒绝 detach，避免半途请求丢失。 |
| `provider_exec` | 宿主 → provider | **emission**：把 `query_id` / `sql` / `params` 推过桥接边界。一旦写入 socket 即不可逆（§6.1）。 |
| `provider_result` | provider → 宿主 | 执行结果：`query_id` + `result` 或 `error`（与 `query_result` / `query_error` 同构）。宿主据此发 `query_result` / `query_error`。 |

- 一律 **MessagePack**，复用既有 `PolyDBError` 与既有错误码，**不新增
  `POLYDB_ERR_*`**。
- 四条消息都**不携带 `connection_id`**：provider 不接触用户连接元数据，
  只拿 SQL 与参数（§6.3 capability attenuation 在 broker 侧已完成）。
- `provider_ack`（宿主 → provider）是 attach/detach 的**单帧**确认：
  `attached`（当前已登记列表）+ `event`（`activating`/`deactivating`/
  `neutral`）+ 可选 `error`。失败时 `event=neutral` 且登记不变。

#### 16.9.2 转发流程（异步契约，§6.2 decision rule）

1. 客户端 `query{provider_key}` → 宿主做 §16.8 的能力校验 + `acquire`
   （reliance 仍覆盖整个在途期，含等待 provider 结果）。
2. 宿主查 attached 表：无该 key → `query_error(DRIVER_NOT_AVAILABLE)` 并
   `release`（不进 in_flight、不发 `QueryStarted`）。
3. 有 attached → 发 `QueryStarted`，把 `provider_exec` 写入该 provider 会话的
   发送通道，然后**异步等待** `provider_result`。等待期间 `query_cancel` 与
   `timeout_ms` 必须仍然生效。
4. 收到 `provider_result` → 转发成 `query_result` / `query_error`，`release`。
5. **output commit / withhold（§6.1）**：`query_cancel` 或 timeout 到达而结果
   尚未落地时，宿主**扣住**结果不向客户端发射（`withhold`），发
   `query_cancelled` / `query_error(TIMEOUT)`。若 `provider_result` 已经写入
   客户端 socket，则 **a message already sent stays sent**——不撤回、不补偿，
   这与 Thm 80 的 delimitation（"speaks of the state, not of the emissions"）
   一致。判定点是"结果帧是否已进入发送通道"，与 §6.1 的 PD-11 发射边界同款。

#### 16.9.3 compensation 与其边界

- cancel / timeout 时宿主向 attached provider 发 `provider_cancel{query_id}`
  （best-effort）：provider 可据此中断自己的执行。这是 **compaction 动作**，
  不是 revertible effect 的逆。
- **明确不宣称**：§6.1 指出 compensation 组成的 LIFO 顺序可沿用，但
  **Thm 43/68 的 commutation 是相对 `≃` 证明的，必须在更粗的等价关系上
  重新建立**。本阶段只由单测/契约测试锁定可观测行为（cancel 后不发结果、
  reliance 归零、守卫放开），**不把元理论担保扩展到 compensation**。
  该声明与 §16.7 的排除项互相锁定。

#### 16.9.4 rolling update（§6.2，阶段 6 落地）

同一 key 的**新 attach** 到来时（更新版本的 provider 上线）：

- **替换**：新会话成为该 key 的当前执行端；**新 exec 全部路由到新会话**。
  消费者无感——它只依赖 broker 的 key，不感知具体会话，因此**不触发 reload**
  （broker 常驻，§6.6；§6.2 的「the broker absorbs this perturbation」）。
- **旧会话退役（retiring）**：旧会话**继续服务它已经收到的 exec**（那些 exec
  已投递过桥接边界，属 emission，不可撤回），但不再接新 exec。
- **自动降级**：旧会话的在途 exec 归零后，宿主把它从该 key 的执行端集合中移除
  （等效 detach），并向旧会话回 `provider_ack(deactivating)`。
- **归属保护**：降级只移除**自己的**登记（same-session / same-channel 判定），
  不会误删新会话的登记（recovery exactness，Thm 68）。
- 与 §16.9.1 的「cannot provide twice」不冲突：那条约束针对 **broker 注册**
  （一个 key 只能有一个 provider 声明）；这里是**执行端**的替换——同一 provider
  的新会话接管同一 key，正是 rolling update 的形态。
- **实现口径**：attached 表的每个条目带一个**在途计数**（该执行端已收到但未
  完成的 exec 数）；投递 exec 时 +1，`provider_result` 到达或被 cancel/timeout
  扣留时 -1；计数归零且该条目已不是当前执行端 → 自动降级。

#### 16.9.5 阶段边界（避免假装覆盖）

- 参考 provider 是**同一进程内的 WS 会话**（契约测试用两条连接模拟），验证
  异步契约与组合语义；真实跨 OS 进程 RPC、stdio/socket transport、宿主与
  provider 用不同工具链/语言构建，都**不在本阶段**。
- 不实现 `set_granted` 的 wire 消息（能力衰减仍只在 broker 骨架 API 与单测
  中，阶段 5 由 §16.10.2 补齐）；不实现 SQL 参数/结果的跨语言类型映射验证
  （复用既有 `Value`）。
- 不实现 provider 侧沙箱、信任门禁、鉴权（§16.5/§16.7 已登记；阶段 5 由
  §16.10.1 的 attach 白名单提供最小执行边界）。

#### 16.9.6 契约测试

`test/contract/provider_bridge_test.go`：启动一个**进程内参考 provider**
（第二条 WS 连接 attach 后，收到 `provider_exec` 经 REST
`POST /api/connections/{id}/query` 用真 SQLite 执行，再发 `provider_result`），
双端 parity 覆盖：正常转发得到真实结果、未 attach 时拒绝、cancel 的 withhold、
timeout、detach 后拒绝、rolling update 新旧会话切换、断连自动 detach 与
reliance 回收。

### 16.10 bridge 处的访问控制（M18 阶段 5，non-breaking）

阶段 4 让 provider 真的执行 SQL，随之出现两个**必须补齐**的访问控制缺口：
§16.9.1 让 `provider_attach` 不校验 broker 注册，于是任何 WS 客户端都能 attach
任意 key、替他人执行 SQL 或伪造结果；§16.4 承诺 `granted_capabilities` 可在
运行时原地改写，但阶段 4 之前没有 wire 消息，该能力线上不可用。

#### 16.10.1 attach 准入门禁（deny-by-default）

- 服务端以环境变量 **`POLYDB_PROVIDER_ALLOW`** 声明允许 attach 的 provider
  key 白名单（逗号分隔，与 `POLYDB_ALLOWED_ORIGINS` 同风格）。
- **未设置或为空 = 不允许任何 attach**（deny-by-default）。这是默认值安全的
  选择：没有显式配置时，进程外 provider 完全不能接管 SQL。
- `provider_attach` 时校验 key ∈ 白名单；不在其中 → 拒绝，回 `provider_ack`
  且 `event=neutral`、`error.code=POLYDB_ERR_PERMISSION_DENIED`，**不登记**。
- 白名单是**策略**而非连接鉴权：它与 §16.5 的「broker 消息不受 §12.2 query
  认证门禁约束」不冲突——前者决定「谁可以成为执行端」，后者只决定「谁能发消息」。
- 门禁只作用于 **attach**（获得执行权的那一步）；已登记的 provider 在
  `POLYDB_PROVIDER_ALLOW` 变化后**不被追溯撤销**（§6.3 的 interception 是
  运行时改写、重载即生效；本阶段不实现策略热重载）。
- 这是 §6.3「sandboxing 需要执行边界之外的机制」在本阶段的最小落地：
  **进程外 provider 默认进不来**；进来的 provider 仍受 `granted_capabilities`
  约束其可见的操作面。

#### 16.10.2 `broker_set_granted`：能力衰减的 wire 入口

- 新增客户端消息 `broker_set_granted{key, granted_capabilities}`：把某 key 的
  **生效能力**原地改写为 `granted_capabilities` 的子集，语义与 broker API 的
  `set_granted` 完全一致（§16.4）。
- **只能收紧，不能放宽**：`granted_capabilities` 必须是已声明 `capabilities`
  的子集，否则拒绝（`POLYDB_ERR_INVALID_PARAM`）。因此该消息只能降低能力面，
  **任何会话发起都不会扩大暴露面**。
- 成功时 `notify_d` 分类为 `neutral`（声明仍被满足）→ 消费者**不 reload**、
  依赖图不动（§6.3 的 in-place intercept 更新）。回 `broker_state`（含新的
  `granted_capabilities`）+ `broker_event(neutral)`。
- 该消息使 §16.4 的能力衰减**在线上可用**：例如把不可信 provider 的生效能力
  从 `query/execute/metadata` 收到 `query/metadata`，使其无法执行写语句，
  且**无需重启或 reload**。

#### 16.10.3 阶段边界

- 不实现 provider 侧的进程沙箱 / SFI / 容器（§6.3 要求执行边界之外的机制；
  本阶段用「进程外 + 默认拒绝 attach」提供最小边界）；
- 不实现 attach 白名单的**热重载**（改环境变量需重启进程）；
- 不实现按来源 IP / 证书的更强身份认证（白名单按 key，不按身份）。

#### 16.10.4 契约测试

`test/contract/provider_policy_test.go`：双端 parity 覆盖——未在白名单的 key
attach 被拒（`PERMISSION_DENIED`）且**不产生执行端**、白名单内的 key 可 attach、
`broker_set_granted` 收紧后带该 key 的查询被拒（`PERMISSION_DENIED`）、
放宽尝试（granted ⊄ declared）被拒（`INVALID_PARAM`）、能力衰减属 `neutral`
（不 reload、依赖图不动）。

### 16.12 provider 执行端抽象（M18 阶段 7，non-breaking）

阶段 4-6 的 provider 执行端是「**一条 WS 会话**」——它同时承担了「传输」与
「执行」两件事。阶段 7 把这两件事拆开：**执行端**是一个抽象（端点），**传输**
是端点的一种实现。这样新增「独立进程执行端」时不必改动 broker / 转发 / 守卫
的任何逻辑。

#### 16.12.1 执行端（endpoint）的语义

一个 **执行端**（endpoint）= 「能接收一次 exec 并最终产出一个结果」的东西。
端点对自己的传输无感：broker 只知道「向某个端点投递 exec」与「端点报告结果」。

| 能力 | 语义 |
|---|---|
| 投递（emit） | 宿主把 `query_id` + SQL + 参数交给端点。投递成功 = 已越过边界（emission，不可逆）。投递失败 = 端点不可达 → `POLYDB_ERR_DRIVER_NOT_AVAILABLE`。 |
| 回收（settle） | 端点的一次在途结束（成功 / 错误 / 被 cancel 或 timeout 扣留）。回收使该端点的在途计数 -1；退役端点在途归零 → 自动降级（§16.9.4）。 |
| 归属（ownership） | 端点知道自己属于哪个 key；降级/断连只移除自己的登记（Thm 68）。 |
| 降级通知（notify） | 端点被自动降级时收到 `provider_ack(deactivating)`。 |

端点实现（provider endpoint kinds）：

1. **会话端点（session endpoint）**：一条 WS 会话承载一个 provider。exec 经该
   会话的 socket 发出，结果经该会话回到宿主。**阶段 4-6 的既有实现**。
2. **进程端点（process endpoint）**：一个独立进程承载一个 provider。宿主与它
   之间是 §16.12.2 的 stdio 传输；SQL 由该进程**回调宿主**执行（§16.12.3）。
   **阶段 7 契约已定稿，宿主接线属下一阶段**。

两端各有一个对称的端点抽象（Rust `trait` / Go `interface`），broker 只依赖该
抽象；新增端点类型 = 新增一个实现，**不改 broker 的分派逻辑**。

#### 16.12.2 provider 进程协议（stdio JSON-RPC 2.0）

provider 进程与宿主之间用 **stdio + newline-delimited JSON-RPC 2.0**，与项目既有
的 MCP stdio 帧格式一致（`go/pkg/mcp`：每行一条 JSON-RPC，`jsonrpc: "2.0"`）。
复用该格式的理由：项目已有实现与经验（`cmd/polydb-mcp`），不引入第三种帧格式。

| 方法 | 方向 | 参数 | 结果 |
|---|---|---|---|
| `provider/hello` | 宿主 → 进程 | `{key, capabilities, protocol_version}` | `{ok: true}`；版本/能力不符 → error |
| `provider/exec` | 宿主 → 进程 | `{query_id, sql, params}` | `{result}` 或 `{error}` |
| `provider/cancel` | 宿主 → 进程 | `{query_id}` | `{ok: true}`（best-effort，§16.9.3） |
| `provider/shutdown` | 宿主 → 进程 | `{}` | `{ok: true}` |

- 宿主是 **client**，provider 进程是 **server**（进程读 stdin 收请求、写 stdout
  回响应）；请求带 `id`，响应回同一个 `id`。
- **stdout 只承载协议帧**：provider 的日志必须写 stderr（与 MCP 同样纪律）。
- 单帧上限 4 MiB（与 MCP 的 scanner buffer 同量级）。
- provider 进程崩溃（EOF / 非零退出）→ 宿主把它视为**端点不可达**：在途 exec 全部
  以 `POLYDB_ERR_DRIVER_NOT_AVAILABLE` 失败，该端点被移除（等效 detach）。

#### 16.12.3 回调宿主执行（provider 不接触凭据）

进程端点的 provider **不管理数据库连接、不接收任何凭据**（§6.3 的能力最小化）：
它收到 `provider/exec` 后，通过**受限回调**让宿主代为执行 SQL。

- 回调面：宿主暴露一个**仅对 provider 进程可达**的端点（loopback + 每次 attach
  生成的一次性令牌），接收 `{query_id, sql, params}`，返回 `QueryResult` 或错误。
- 凭据、连接池、只读判定、限行全部留在宿主侧（沿用既有 `app-core` 路径），provider
  只看到 SQL 与结果。
- 该回调**不是**对公网开放的管理面：仅 loopback、仅 attach 已授权的 key、每次
  attach 一次性令牌、请求体有上限。
- 代价与边界：provider 仍能**读到结果数据**（它本来就是执行 SQL 的组件），但
  无法看到连接元数据与凭据；provider 进程崩溃不会影响宿主的连接池。

#### 16.12.4 阶段边界

- **宿主对进程端点的接线属下一阶段**：本阶段定稿契约与抽象，但宿主仍只用会话
  端点。契约测试因此**不覆盖进程端点**（不假装覆盖）。
- 不实现 provider 侧的 SFI / 容器沙箱；进程边界本身提供了「崩溃隔离」与
  「凭据不可见」，但**不是**安全沙箱（§6.3：sandboxing 需要执行边界之外的机制）。
- 不实现 provider 进程的热升级（进程重启 = 新端点 attach = rolling update，语义
  天然由 §16.9.4 覆盖，但跨进程的自动重启策略未定）。
- 回调端点的令牌管理与轮转属下一阶段接线时一并落地。

### 16.13 进程端点接线（M18 阶段 8，non-breaking）

阶段 8 把 §16.12.1 的**进程端点**（kind 2）接入宿主：宿主拉起独立进程，经
stdio JSON-RPC 与它通信，行为与会话端点完全一致（同一 `ProviderEndpoint`
抽象）。**会话端点路径逐字不变**。

#### 16.13.1 进程来源：不可由客户端指定

- 进程端点只能由**服务端管理员**配置拉起：环境变量
  **`POLYDB_PROVIDER_PROCS`**，格式 `key=path[:arg],key2=path2[:arg2]`（逗号分隔
  键值对，冒号后为单个可选参数）。
- **客户端不能指定任何可执行文件**：`provider_attach` 只携带 key，宿主从配置表
  查该 key 对应的进程；查不到 → 拒绝（`POLYDB_ERR_PERMISSION_DENIED`）。
  这条是安全红线：否则 attach 退化成任意命令执行。
- 未配置 `POLYDB_PROVIDER_PROCS` → 没有进程端点（与 §16.10.1 的 deny-by-default
  一致）。
- 进程端点的 key 仍受 §16.10.1 的 `POLYDB_PROVIDER_ALLOW` 白名单约束：两道门禁
  都要过。

#### 16.13.2 握手与登记

- 宿主拉起进程后，进程先发 `provider/hello{key, capabilities, protocol_version}`；
  宿主校验：key 与配置表一致、协议版本匹配、能力集合合法 → 建立 attached 登记
  并回 `provider/hello` 的确认（`{ok: true}`）。
- 校验失败 → 宿主终止该进程（不登记），记日志。
- 登记后，进程端点与**会话端点完全等价**：`provider_exec` 经 stdio 送出、
  `provider_result` 经 stdio 收回、`provider_cancel` 经 stdio 送出；`reliance`、
  `¬ relied_n(γ)` 守卫、rolling update、withhold 的语义一字不改（它们都作用在
  `ProviderEndpoint` 抽象上）。

#### 16.13.3 结果转发与帧映射

- 进程端点的 `provider_result` 帧是 `{query_id, result?, error?}`（与 §16.12.2）。
  宿主把它转成**会话侧**的 `ServerMessage::QueryResult` / `QueryError` 发给查询
  发起方——**wire 上不新增消息类型**（§16.9 的既有消息已够用）。
- 帧映射：`result` → `QueryResult`（含 columns/rows/affected_rows/execution_time_ms）；
  `error` → `PolyDBError`（code/message）；两者皆无 → `POLYDB_ERR_INVALID_PARAM`。
- 因此进程端点的存在**对前端完全透明**：前端看到的仍是普通查询。

#### 16.13.4 崩溃与不可达

- 进程退出（EOF / 非零退出 / 被信号杀死）→ 该端点**不可达**：
  - 在途 exec 全部以 `POLYDB_ERR_DRIVER_NOT_AVAILABLE` 失败（不挂起）；
  - 该端点的 attached 登记被移除（等效 detach）；
  - **不自动重启**（重启策略未定，见 §16.12.4）。
- 投递失败（stdin 写失败 / 管道已关闭）→ 同上：本次 exec 立即失败，
  `POLYDB_ERR_DRIVER_NOT_AVAILABLE`。
- 宿主在进程退出时**必须回收子进程**（join / wait），不留僵尸进程。

#### 16.13.5 阶段边界

- **回调宿主执行的完整鉴权面属下一阶段**：阶段 8 打通「进程端点的传输与转发」，
  参考 provider 进程在本阶段**不执行真实 SQL**（回固定结果或错误），用于验证
  传输、转发、崩溃处理；把 SQL 真正交给宿主执行需要 §16.12.3 的受限回调面
  （一次性令牌 + loopback + 限流），属下一阶段。
- 不实现 provider 进程的热重启与看门狗。
- 不实现进程端点的能力衰减联动（`broker_set_granted` 对进程端点同样生效——因为
  它作用在 broker 层，与端点类型无关；这一点由 §16.10.2 覆盖）。
- 进程端点与 `POLYDB_PROVIDER_ALLOW` 的**双重门禁**：配置表给出「有哪些进程」，
  白名单给出「允许谁 attach」。

#### 16.13.6 契约测试

`test/contract/provider_process_test.go`：双端 parity 覆盖——配置了进程端点后
`provider_attach` 成功、转发拿到进程回的结果、进程被杀后查询立即失败
（`DRIVER_NOT_AVAILABLE`）、未在配置表中的 key attach 被拒。

### 16.14 受限回调面：provider 代执行 SQL（M18 阶段 9，non-breaking）

阶段 8 的参考 provider **不执行真实 SQL**（回固定结果）。阶段 9 补上 §16.12.3
承诺的回调面，让 provider 进程**真正执行 SQL**却**永不接触凭据**。

#### 16.14.1 不透明 handle（provider 看不到连接）

- provider 收到 `provider/exec` 时，**不带** `connection_id`——它本来就不该知道
  用户连接的存在。宿主在转发时把 `connection_id` 换成**不透明 handle**。
- handle 是宿主生成的随机串（如 32 位十六进制），宿主侧映射到真实
  `connection_id`。**handle 不可推导**：它与 connection_id 无算术关系，
  provider 无法通过猜测拿到别的连接的 handle。
- handle 的作用域 = **一次 provider attach**：attach 时为该 provider 分配它能
  使用的 handle 集合；provider 断开或 detach 后，handle 立即失效
  （§16.10 的 acquisition 逆）。

#### 16.14.2 回调端点（loopback + 令牌 + 限流）

宿主在**进程内**起一个 loopback HTTP 端点，专供 provider 回调：

| 属性 | 约束 |
|---|---|
| 监听地址 | `127.0.0.1`（**仅回环**，不可从外部到达） |
| 端口 | 随机（绑定 `:0` 后取实际端口），不写在配置里 |
| 认证 | 每个 provider 进程一个**随机令牌**（attach 时下发，请求带 `Authorization: Bearer <token>`）；令牌不落盘、不进日志 |
| 路由 | 只有一条：`POST /provider/exec` |
| 请求体 | 上限 1 MiB（§5 的 blob 阈值同量级）；超限 → `POLYDB_ERR_PAYLOAD_TOO_LARGE` |
| 并发 | 每个 provider 最多 N 条在途（默认 4）；超限 → `POLYDB_ERR_DRIVER_NOT_AVAILABLE` |
| 超时 | 单次执行沿用查询自身的 `timeout_ms`；无超时则 30s 兜底 |
| 响应 | `QueryResult` 或 `PolyDBError`（原样映射，provider 看得到错误码但看不到连接信息） |

- 令牌与 handle 都由宿主**每次 attach 重新生成**（不跨 provider 复用）。
- 回调端点**不是**对公网开放的管理面：它只接受带正确令牌的请求，且令牌只交给
  宿主自己拉起的 provider 进程（§16.13.1 的命令由管理员配置，客户端无从得知）。
- 宿主的执行路径**完整复用**既有 app-core 逻辑（§12.3 只读判定、§5 限行、
  事务、连接解析）——provider 的 SQL 与用户直连 SQL 走**同一条**判定链，
  不开任何后门。

#### 16.14.3 转发链路的变更

`provider/exec` 的参数从 `{query_id, sql, params}` 变为
`{query_id, handle, sql, params}`：

- **会话端点**（kind 1）不受影响：会话 provider 本来就能看到 connection_id
  （它就在同一个会话里），继续用原参数形状。
- **进程端点**（kind 2）用 handle：宿主在转发前把 `connection_id` 换成 handle；
  provider 回调时把 handle 原样带回，宿主解析出真实 connection_id 再执行。

#### 16.14.4 安全属性（明确承诺与不承诺）

**承诺**：
- provider 进程**永远看不到** connection_id、连接名、主机、端口、用户名、密码；
  它只看到 SQL、参数、结果，以及一个不透明 handle。
- 凭据（keyring）、连接池、只读判定、限行全部留在宿主进程内。
- provider 进程崩溃/被杀不影响宿主的连接池与其它 provider。

**不承诺**：
- 进程边界**不是**安全沙箱：provider 进程仍能读写宿主机器上的文件（以它自己的
  OS 权限），也能读到它自己收到的结果数据（它本来就是执行 SQL 的组件）。
  真正的隔离需要 SFI / 容器 / 独立用户（§6.3：sandboxing 需要执行边界之外的
  机制），**不在本阶段**。
- 令牌与 handle 防的是**误用与越权串扰**，不是恶意进程。

#### 16.14.5 阶段边界

- 回调面**仅 loopback**：不实现跨主机回调（provider 必须与宿主同机）。
- 不实现令牌的持久化与跨重启轮转（进程级随机，重启即失效）。
- 不实现回调的 TLS/mTLS（loopback 不需要）。
- 不实现 provider 侧的连接池缓存（每次回调都走宿主 app-core）。

#### 16.14.6 契约测试

- `provider_result` 的迟到投递（pending 已被取消清除）仍被 **withhold**
  （§16.9.2 step 5），语义不变。

### 16.15 完成与取消信号分离（M18 阶段 10，non-breaking）

§2.2 的「取消信号与完成信号分离」在实现层的口径。这是对**既有取消机制竞态**
的修正（PD-11 的发射边界设计有一个固有窗口：查询正常完成 → 注销在途表项并
关闭取消通道 → watcher goroutine 恰好被调度 → 触发 `cancelFn` → 查询上下文
被取消 → 发射前的「已取消」二次判定误判 → **正常完成的查询被吞掉**、客户端
收到 `query_cancelled`。负载越高窗口越易命中，表现为偶发）。

- 在途表项从「单个取消通道」变为「**成对信号**」：`cancel`（真取消才关闭）与
  `done`（正常完成关闭）。
- **正常完成**：只关 `done` → watcher 看到 `done` 直接退出，**不触发取消**、
  不污染查询上下文 → 结果正常发射。
- **真取消**（客户端 `query_cancel`、timeout、panic、会话收尾）：关 `cancel`
  → watcher 触发上下文取消 → 在途执行中断（内置路径）或进入 withhold +
  compensation（provider 路径，§16.9.2 step 5）。
- 两类信号**都**把表项从在途表移除（防重复触发）；watcher 对两个通道的
  竞争用「先到先得」判定——done 先关即视为完成，cancel 先关即视为取消，
  与关闭发生的时间序严格一致。
- `provider_result` 的迟到投递（pending 已被取消清除）仍被 **withhold**
  （§16.9.2 step 5），语义不变。

### 16.16 provider 侧沙箱：OS 级执行边界（M18 阶段 11，non-breaking）

进程边界（§16.13）提供了崩溃隔离，但 provider 进程仍以宿主相同的 OS 权限运行。
本阶段给它加上**OS 级执行边界**：生命周期绑定、资源上限、进程隔离。这是
§6.3「sandboxing 需要执行边界之外的机制」的最小落地——**不是** SFI，也不是
恶意进程防御。

#### 16.16.1 生命周期绑定（宿主死 → provider 必死）

| 平台 | 机制 |
|---|---|
| Windows | provider 进程加入 **Job Object**（`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`）：宿主进程退出（含被杀）→ Job 句柄关闭 → **全部 provider 进程被 OS 强制终止**。孤儿进程从机制上根除（此前靠契约测试的 `taskkill` 兜底）。 |
| Unix | provider 加入独立**进程组**（`Setpgid`），并**自检父进程**：启动时记录宿主 PID，运行中 `getppid()` 变化（宿主已死、被 init 收养）→ 自行退出。 |

- 生命周期绑定使 `provider_attach` 的资源（attached 登记、在途查询）与进程
  生命周期严格对齐——宿主意外终止时，provider 不会残留占着回调令牌与
  attached key。
- Job Object / 进程组由**宿主侧**设置（provider 进程无从退出或对抗）。

#### 16.16.2 资源上限

| 平台 | 机制 | 上限 |
|---|---|---|
| Windows | Job Object `JOB_OBJECT_LIMIT_PROCESS_MEMORY` | 内存 512 MiB（默认；`POLYDB_PROVIDER_MEM_MB` 可覆盖） |
| Unix | provider 启动时自限 `RLIMIT_AS` | 同上 |

- CPU 时间不做硬限（SQL 执行本身可能合法地长）；超时由查询自身的
  `timeout_ms` 与回调面的 30s 兜底约束（§16.14.2）。
- 上限触发即进程被 OS 终止——宿主按 §16.13.4 的「端点不可达」处理。

#### 16.16.3 阶段边界

- **不承诺**文件系统与网络访问隔离：provider 仍能以自己的 OS 权限读写宿主
  机器上的文件。真正的隔离需要容器 / 独立用户 / SFI（§6.3），**不在本阶段**。
- 不承诺恶意进程防御：令牌与 handle（§16.14）防的是**误用与越权串扰**。
- 不实现 CPU 硬限（见 §16.16.2）。

### 16.17 compensation 的 commutation：观测等价与穷举验证（M18 阶段 11）

§16.9.5 明确「不宣称 commutation 元理论成立」——因为补偿的等价关系比 ≃ 粗，
Definition 65 的交换律必须在**更粗的等价**上重新建立。本节把它做掉：
先**定义**那个更粗的等价，再**穷举验证**补偿组合在该等价下的性质。

#### 16.17.1 观测等价（≈ₒ，比 ≃ 粗）

两次执行 **≈ₒ 等价**，当且仅当客户端能观测到的**帧序列**（按 query_id 归集）
满足：对每个 query_id，收到的**最终结果帧**属于同一类别（`query_result` /
`query_cancelled` / `query_error`），且 `query_result` 的**载荷相同**。

它比 ≃ 粗：内存布局、pending 表内容、发出过的 `provider_cancel`、watcher 的
调度时序——全都不可观测，也不需要等。

#### 16.17.2 重证明的性质（穷举验证）

对 pending 状态机的操作序列（`complete(q)` / `cancel(q)`，q 遍历全部在途
查询，序列长度 ≤ 4 **全枚举**），断言：

| 性质 | 内容 | 对应论文概念 |
|---|---|---|
| **P1 withhold** | `cancel(q)` 先于 `complete(q)` → `complete` 返回 false（结果被扣住），客户端**不会**收到该 q 的结果帧 | output commit / withhold（§6.1） |
| **P2 至多一次** | 每个 q 的结果**至多投递一次**（第二次 complete 必 false）——客户端不会收到重复帧 | emission 不可逆的边界 |
| **P3 线性化** | 交错下每条操作在某个瞬时点原子生效：**先完成者胜**（complete 赢得 q 的唯一结果槽），取消只能作用于「尚未完成」的 q | Definition 65 的交换律在 ≈ₒ 上重建 |
| **P4 顺序无关** | 多 provider 的回收组合与注销顺序无关（recovery exactness，Thm 68 的运行时对应） | Thm 68 |

- **「重证明」的口径**：这不是数学证明——是用**全枚举**（确定性、可复现、
  进 CI）在实现层验证补偿组合在 ≈ₒ 下的性质。它把 §16.9.5 的「不宣称」升级为
  「穷举已验证」；若实现偏离性质，穷举**必然**命中反例。
- 该验证锁定的是 **pending 状态机**（宿主侧补偿的核心）；provider 进程内部
  的行为（§16.9.3 best-effort）不在其内——那部分的可观测效果经 P1/P2 间接
  锁定（迟到结果被扣住，无论 provider 内部做了什么）。

#### 16.17.3 验证载体

`go/pkg/server/provider_commutation_test.go` 与
`rust/crates/server/src/provider_commutation_tests.rs`：两端各自对自家 hub 的
pending 实现跑同一组性质（穷举序列相同、性质相同），双端 parity。

### 16.18 多实例服务与负载分担（M18 阶段 12，non-breaking）

§6.2 给出服务代理的三个动机：负载分担、rolling update、跨进程 RPC。阶段 4–6
落地了后两个；本阶段落地**负载分担**——同一 key 的多个 provider **并存**服务，
任一退出不影响其余（continuous availability）。并存的前提是 **Thm 47 的
witnessed coeffect**：provider **自证**其 key 是可交换表（实例可互换），独立性
检查由此退化为声明本身——同 key 的多实例互不依赖（它们服务同一抽象服务的
等价副本），无需运行时交叉计数。

#### 16.18.1 witnessed 声明

- `provider/hello`（进程端点，§16.13.2 内协议）与 `provider_attach`
  （会话端点，§16.12.1 kind 1）携带可选 `commutative: true`。
- **缺省 false**：非 commutative key 的第二次 attach 保持阶段 6 语义
  （rolling update：替换 + 退役）——**行为不变**。
- `commutative: true` 时同 key 的第二次 attach **并存**而非替换：旧端点不标记
  退役、不降级；两个端点都在 attached 列表里。

#### 16.18.2 分派与回收

- **分派**：同 key 多端点时按 **per-key 轮询**（round-robin）选择执行端——
  请求在实例间分担（负载分担的最低要求：均匀与无状态）。
- **回收**：端点 detach/断开/崩溃时，只把它自己从列表移除；**列表非空则 key
  仍然可用**（不触发 §16.9.4 的 key 级降级——那是 rolling update 的语义），
  不向其余实例发任何补偿。列表清空后 key 不可用（查询报
  `DRIVER_NOT_AVAILABLE`，§16.8 的既有行为）。
- 在途请求仍绑定**原端点**（register_pending 时选定）：实例退出时其在途请求
  按 §16.13.4 失败（`DRIVER_NOT_AVAILABLE`），**其余实例的在途不受影响**——
  Thm 43（成对独立的效果可按任意排列回收）的运行时对应：多实例的回收互不
  干扰。

#### 16.18.3 与既有语义的关系

| 场景 | 非 commutative（缺省） | commutative |
|---|---|---|
| 同 key 第二次 attach | rolling update：替换 + 旧端点退役（§16.9.4） | 并存：两个都在 |
| 分派 | 唯一端点 | per-key 轮询 |
| 一端退出 | —— | 其余继续服务；key 不降级 |
| capability 衰减 | 作用在 broker 层（§16.10.2），与端点数无关——同 key 全部实例同时被收紧 | 同左 |

- broker 层**不变**：能力声明仍 per-key 一条（多实例声明相同能力；先到者
  登记，后到者的 register 为幂等中性——其能力集必须一致，不一致报
  `INVALID_PARAM`）。
- reliance 守卫不变：per-key 计数（§16.8），与端点数无关。

#### 16.18.4 broker 收敛性（Thm 80 的运行时对应）

Thm 80：动态历史不留痕迹——quiescent 终态 = 静态组装终态。对 broker 的
穷举验证（`register` / `revoke` / `set_granted` 全操作序列，长度 ≤ 4，
key ∈ 两个）断言两个性质：

1. **net-intent 收敛**（全部序列）：终态 = **成功转移**的 net-intent 重放——
   per-key 取最后一次成功的 register（起点）与其后全部成功的 `set_granted`。
   按论文 Bounds「excludes failed fibers」，**失败转移不参与**：被守卫拒绝的
   操作（Def 19 的重复注册、未注册 key 的撤回/衰减）不改变状态，也不进入
   净意图。**注意**：净意图必须连同其前置 register 一起重放（收紧操作依赖
   「已注册」这一前置；穷举曾命中反例：把收紧单独重放会因未注册而失败，
   得到比实际更宽的状态）。
2. **幂等重放**（仅全成功序列）：同一序列执行两遍，第二遍零状态变化——
   重放的 register 重复被拒、revoke 的对象已不存在、`set_granted` 幂等收紧。
   含失败操作的序列**不能**做幂等重放：第一遍失败的衰减（当时未注册）在
   第二遍会成功（注册已存在）——那是守卫语义的正确行为，不是收敛性破坏
   （穷举曾命中反例）。

与 §16.17 互补：那里验证的是补偿（pending，emission 侧），这里验证的是
注册表（acquisition 侧）。两个穷举的验证口径都经历了「反例 → 修正判据」
的过程——这正是穷举的价值：实现是对的，**错的是验证者第一版的判据**。

#### 16.18.5 契约测试

`test/contract/provider_multiinstance_test.go`：同 key 两个 commutative 进程
provider 并存（attached 长度 2）、连续查询轮询命中两个实例、杀掉其一后
**服务仍可用**（无需重新 attach，其余实例接手）。

### 16.19 配置对账：provider 配置的增量生效（M18 阶段 13，non-breaking）

Def 81 的配置对账落到 broker 配置上：provider 配置（§16.10.1 白名单 +
§16.13.1 进程表）的变更**经 `notify_d` 分类增量生效**——改动只在受影响
的 key 上动作，其余 key 一概不动，且**无需重启宿主**。这是论文 §5
「intercept 就地更新、其余触发 reload」的 broker 版：进程表条目的变更
就是 reload，白名单变更就是 interception 的授权面更新。

#### 16.19.1 变更源

- `POLYDB_PROVIDER_CONFIG_FILE` 指定 JSON 配置文件：
  `{ "allow": ["key", ...], "procs": [{"key": "k", "cmd": "..."}] }`。
- 宿主**轮询**该文件（`POLYDB_PROVIDER_CONFIG_POLL_MS`，缺省 5000；测试可
  调小）并在内容变化时对账；Unix 额外响应 **SIGHUP** 立即重读。
- 未设置该变量时行为与此前完全一致（env 静态配置，无 watcher）。
- 文件缺失或解析失败：**保持上次已知配置**（记日志、不动作）——配置错误
  不能清空运行中的 provider。
- 文件是唯一对账源：设置后，文件内容**覆盖** env 初值（首次读取即对账）。

#### 16.19.2 notify 分类与动作（diff → 分类 → 动作）

| 变更 | 分类 | 动作 |
|---|---|---|
| 白名单 +key | **activating** | 白名单加入；不影响已 attach（attach 是 provider 的动作） |
| 白名单 −key | **deactivating** | 宿主主动撤下：`broker.Revoke`（守卫 `¬ relied`，§16.8）+ 撤下该 key 全部端点（commutative 多实例一并撤下） |
| 白名单不变 | neutral | 无动作 |
| 进程表 +条目 | **activating** | spawn 新进程（hello 后自动登记，§16.13.2） |
| 进程表 −条目 | **deactivating** | 终止对应进程（onDead → DropEndpoint，§16.13.4 既有路径）+ `broker.Revoke`（若白名单也未含该 key） |
| 进程表命令变更 | **deactivating + activating** | kill 旧进程 + spawn 新进程；非 commutative key 由新实例的 hello 按rolling update 接管（§16.9.4） |
| 进程表不变 | neutral | 无动作 |

- **deactivating 的守卫**（决定与动作分离，L-Leave/L-Divert）：撤下时若有
  在途请求（`¬ relied_n(γ)`），**推迟到下个对账周期重试**——不是失败、不是
  强杀。在途请求完成后自然撤下（Thm 70：provider 的 episode 严格包住消费者）。
- **对账幂等**：同一配置重放不产生任何动作（diff 为空）——"动态历史不留
  痕迹"（Thm 80）在配置面的对应。
- 进程的匹配键 = `(key, cmd)`：同 key 多实例（§16.18）由条目列表区分，命令
  相同的条目互为多实例。

#### 16.19.3 阶段边界

- 不实现配置的**写回**与校验回执（对账结果经日志观测；客户端不可见）。
- 不实现配置文件的热迁移语义（进程命令变更按 kill+spawn 处理，非 commutative
  key 的在途请求按 rolling update 收尾）。
- 客户端对配置变更**不可观测**（broker_state 帧只对 provider 会话发送）；
  配置对账是宿主运维面，不在 wire 契约内。

#### 16.19.4 验证载体

- 单元测试（`provider_config_test.go` / Rust 镜像）：diff 分类纯逻辑、
  deactivating 的守卫推迟、对账幂等。
- 契约测试（`provider_config_test.go`）：写配置文件 → 等对账周期 → 新实例
  生效（连续查询成功）；清空进程表 → 等对账周期 → 查询失败
  （`DRIVER_NOT_AVAILABLE`）。

#### 16.19.5 已修缺陷（PD-39，2026-11-15）

**Rust 侧配置对账的撤下路径曾触发 HTTP 层挂死**：provider 进程 spawn 后，
对账 task 卡死，后端 HTTP 层对后续请求无响应（`/api/health` 独立 handler 仍
200，其余 handler——含 REST 与 WS 升级——卡死；goroutine dump 显示请求卡在
传输层等待，后端 stderr 无 panic）。

**根因**：`ProcessEndpoint::wait_loop` **持有 child 锁阻塞在
`Child::wait()`**——进程存活期间该锁永不释放；配置对账的撤下第一次真正调用
`close()`（其 `child.lock()` 因此永久阻塞），把对账 task 卡死。此前从未暴露：
测试都是直接杀宿主进程（OS 级 Job 句柄关闭兜底回收），`close()` 从未被执行。

**修复**：`wait_loop` 改为 **try_wait 轮询**（100ms 周期，短临界区）——
kill（close）与回收（wait）互不阻塞，与 Go 侧 waitLoop goroutine 的语义
严格对齐。修复后撤下链路（kill → broker.Revoke → 端点撤下）全部畅通，
Rust 契约子测试与 go 子测试同构（独立 key + 独立配置文件，端到端覆盖
spawn → scale-out → 撤下）。

**教训**：**持锁阻塞是 async 运行时的禁手**——`std::sync::Mutex` 的临界区
里调用任何可能阻塞的系统调用（`wait`/IO），都会把持锁时间放大到不可预期；
测试直接杀进程的清理路径**不会执行** `close()`，这类缺陷要靠「真正调用
每个清理路径」的测试形态才能暴露。
