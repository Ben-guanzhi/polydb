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
- 未启用 token 时忽略 `auth` 字段（non-breaking）。

### 12.3 连接级只读（read_only）

- `ConnectionConfig.read_only = true`（默认 false）的连接，服务端在 **app-core 执行层**拒绝写操作
  （UI 禁用只是第一道，服务端拦截才是保证）：
  - SQL：`execute` / `execute_in_transaction` / batch 中语句按 §11 判型，`insert` / `update` /
    `delete` / `ddl` 返回 **409** `POLYDB_ERR_READ_ONLY`；`select` / `other` 放行
    （`PRAGMA` 等写型 `other` 语句不在 M10 拦截范围，文档如实标注）。
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
