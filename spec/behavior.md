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
    （不再复用 §11 判型）：剥离 `--` / `/* */` 注释与 `'…'` / `"…"` / `` `…` `` / `[…]`
    字面量后，整词命中 `INSERT` / `UPDATE` / `DELETE` / `MERGE` / `REPLACE` / `TRUNCATE` /
    `GRANT` / `REVOKE` / `CREATE` / `ALTER` / `DROP` / `CALL` / `EXEC` / `EXECUTE` / `COPY` /
    `VACUUM` / `REINDEX` / `CLUSTER` / `LOCK` / `COMMENT` 即返回 **409** `POLYDB_ERR_READ_ONLY`。
    扫整句使数据修改 CTE（`WITH … DELETE/UPDATE/INSERT`）与 `TRUNCATE` / `GRANT` / `CALL`
    等此前归入 `select` / `other` 的写型语句同样被拦；注释/字符串中的同名词不误判。
    `PRAGMA` 等写型 `other` 语句仍不在拦截范围，文档如实标注。
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

- `limit` 缺省 200，服务端钳制到 10000；`offset` 缺省 0。分页用 OFFSET/LIMIT 语义
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

### 14.7 一致性锁定

`reason` 文案、条目排序、归一结果由 `test/schema-diff-vectors/*.json` 一份语料同时喂两端锁定。
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
