// PolyDB Protocol Types — GENERATED from spec/ by tools/genprotocol. DO NOT EDIT.
// 契约唯一真相源：spec/schemas/*.json + spec/asyncapi.yaml

export type DatabaseKind = 'mssql' | 'mysql' | 'oracle' | 'postgres' | 'redis' | 'sqlite';

export type ConnectionId = string;

export type Value = null | boolean | number | string | Value[] | { [key: string]: Value };

export interface SshTunnelConfig {
  host: string;
  port: number;
  username: string;
  /** Reference to password in keyring/encrypted store */
  password_ref?: string;
  /** One-time plaintext SSH password, only accepted on create/update; server writes it to the keyring and never stores or returns it */
  password?: string;
  private_key_path?: string;
  private_key_passphrase_ref?: string;
  /** One-time plaintext passphrase for the private key, only accepted on create/update; server writes it to the keyring and never stores or returns it */
  private_key_passphrase?: string;
}

export interface PaginationParams {
  offset?: number;
  limit?: number;
}

export type SortOrder = 'asc' | 'desc';

export interface CreateConnectionRequest {
  /** User-friendly connection name */
  name: string;
  kind: DatabaseKind;
  /** Database host (not needed for sqlite) */
  host?: string;
  /** Database port */
  port?: number;
  /** Database name or file path (sqlite) */
  database?: string;
  username?: string;
  /** Reference to password in keyring/encrypted store. Never send plaintext password. */
  password_ref?: string;
  /** One-time plaintext password, only accepted on create; server writes it to the keyring (under a generated ref) and never stores or returns it */
  password?: string;
  /** Driver-specific options (e.g., sslmode, charset) */
  options?: Record<string, string>;
  ssh_tunnel?: SshTunnelConfig;
  /** Default schema to use (e.g., 'public' for postgres) */
  default_schema?: string;
  /** When true, the server rejects write statements (INSERT/UPDATE/DELETE/DDL) and KV writes on this connection with POLYDB_ERR_READ_ONLY (behavior.md §12) */
  read_only?: boolean;
  /** User-defined group/folder name for organizing connections in the UI */
  group?: string;
  /** Connection accent color (hex, e.g. #2d68c8), rendered as a dot in the UI */
  color?: string;
}

export interface UpdateConnectionRequest {
  name?: string;
  host?: string;
  port?: number;
  database?: string;
  username?: string;
  password_ref?: string;
  /** One-time plaintext password; empty string or omitted means keep the existing password. Server writes it to the keyring and never stores or returns it */
  password?: string;
  options?: Record<string, string>;
  ssh_tunnel?: SshTunnelConfig;
  default_schema?: string;
  read_only?: boolean;
  group?: string;
  color?: string;
}

export interface ConnectionInfo {
  id: ConnectionId;
  name: string;
  kind: DatabaseKind;
  host?: string;
  port?: number;
  database?: string;
  username?: string;
  options?: Record<string, string>;
  ssh_tunnel?: SshTunnelConfig;
  default_schema?: string;
  read_only?: boolean;
  group?: string;
  color?: string;
  created_at: string;
  updated_at: string;
}

export interface ConnectionStatus {
  id: ConnectionId;
  connected: boolean;
  /** Database server version string */
  server_version?: string;
  /** Round-trip latency in milliseconds */
  latency_ms?: number;
  /** Error message if not connected */
  error?: string;
}

export interface PolyDBError {
  /** Machine-readable error code (e.g., POLYDB_ERR_CONNECTION_FAILED) */
  code: string;
  /** Human-readable error message */
  message: string;
  /** Additional structured error context */
  detail?: Record<string, unknown>;
  /** Whether the operation can be retried */
  retryable?: boolean;
  /** Underlying driver error message (for debugging) */
  cause?: string;
}

export interface SchemaInfo {
  /** Schema name */
  name: string;
}

export type TableType = 'materialized_view' | 'table' | 'view';

export interface TableInfo {
  /** Table name */
  name: string;
  /** Schema this table belongs to */
  schema: string;
  /** Object type */
  type: TableType;
  /** Estimated row count (may be stale) */
  row_count?: number;
  /** Table comment/description */
  comment?: string;
}

export type GenericType = 'array' | 'bigint' | 'binary' | 'blob' | 'boolean' | 'char' | 'date' | 'datetime' | 'decimal' | 'double' | 'enum' | 'float' | 'integer' | 'interval' | 'json' | 'jsonb' | 'numeric' | 'other' | 'smallint' | 'text' | 'time' | 'timestamp' | 'uuid' | 'varbinary' | 'varchar' | 'xml';

export interface ColumnInfo {
  name: string;
  /** Database-specific type name (e.g., 'varchar', 'int4') */
  data_type: string;
  /** Normalized type category for cross-database comparison */
  generic_type?: GenericType;
  nullable: boolean;
  /** Column default expression */
  default_value?: string | null;
  /** Max length for char/varchar types */
  max_length?: number | null;
  /** Precision for numeric types */
  precision?: number | null;
  /** Scale for numeric types */
  scale?: number | null;
  is_primary_key?: boolean;
  is_auto_increment?: boolean;
  comment?: string;
  /** Column position (1-based) */
  ordinal_position: number;
}

export type IndexType = 'btree' | 'fulltext' | 'gin' | 'gist' | 'hash' | 'other' | 'spatial';

export interface IndexInfo {
  name: string;
  unique: boolean;
  /** Whether this is the primary key index */
  primary?: boolean;
  /** Index type */
  type?: IndexType;
  /** Columns in index order */
  columns: IndexColumn[];
  comment?: string;
}

export interface IndexColumn {
  /** Column name */
  name: string;
  /** Position in index (1-based) */
  position: number;
  order?: SortOrder;
  /** Prefix index length if applicable */
  prefix_length?: number | null;
}

export interface ForeignKeyInfo {
  name: string;
  /** Local column names */
  columns: string[];
  referenced_schema: string;
  referenced_table: string;
  /** Referenced column names */
  referenced_columns: string[];
  on_update?: ForeignKeyAction;
  on_delete?: ForeignKeyAction;
}

export type ForeignKeyAction = 'cascade' | 'no_action' | 'restrict' | 'set_default' | 'set_null';

export type SchemaDiffOp = 'add_column' | 'add_foreign_key' | 'add_index' | 'alter_column' | 'create_table' | 'drop_column' | 'drop_foreign_key' | 'drop_index' | 'drop_table';

export type DiffColumnFlag = 'auto_increment' | 'default' | 'nullability' | 'type';

export interface SchemaDiffEntry {
  /** 稳定标识 '{table小写}/{object小写}/{op}'，供 UI 记忆勾选状态 */
  id: string;
  op: SchemaDiffOp;
  /** 执行相位，小的先执行：drop_fk=10 drop_index=20 drop_column=30 drop_table=40 create_table=50 add_column=60 alter_column=70 add_index=80 add_fk=90 */
  phase: number;
  /** 表名（新增/变更取目标侧原样大小写，删除取源侧） */
  table: string;
  /** 列名/索引名/外键名；表级动作为空。无名外键/索引回落为内容签名 */
  object?: string;
  /** 变更前取值，语义随 op 而定 */
  from?: string | null;
  /** 变更后取值 */
  to?: string | null;
  /** 仅 alter_column 使用；其余动作为空 */
  flags?: DiffColumnFlag[];
  /** 是否丢数据（四类删除动作） */
  destructive: boolean;
  /** 默认是否勾选：破坏性动作默认 false，除非 select_destructive */
  selected: boolean;
  /** 人读原因；双实现必须逐字一致（由 test/schema-diff-vectors 锁定） */
  reason: string;
}

export interface SchemaDiffPlan {
  /** 已按 (phase, table, object, id) 排序的可执行计划；同构时为空数组 */
  entries: SchemaDiffEntry[];
}

export interface SchemaDiffTarget {
  connection_id: ConnectionId;
  /** 缺省由服务端按数据库种类取默认 schema（SQLite 恒 'main'，见 behavior.md §14.5） */
  schema?: string;
}

export interface SchemaDiffOptions {
  /** 忽略默认值差异（跨环境的序列/时间戳默认常属噪声） */
  ignore_defaults?: boolean;
  /** 破坏性动作是否默认勾选（须用户显式确认） */
  select_destructive?: boolean;
}

export interface SchemaDiffRequest {
  /** 基准侧（要变成 target 的样子） */
  source: SchemaDiffTarget;
  /** 期望侧 */
  target: SchemaDiffTarget;
  options?: SchemaDiffOptions;
}

export interface SchemaDdlRequest {
  /** 基准侧（当前库） */
  source: SchemaDiffTarget;
  /** 期望侧 */
  target: SchemaDiffTarget;
  /** 与比对时同一份选项；回滚方向按同一选项镜像重算，故必须与正向一致 */
  options?: SchemaDiffOptions;
  /** 勾选的变更条目 id（须来自同一次 /api/schema/diff 的 SchemaDiffPlan）。空数组产出空脚本；进入整表重建的表必须整组勾选，否则 POLYDB_ERR_INVALID_PARAM */
  selected: string[];
}

export interface DdlStatement {
  /** 该语句实现的变更条目 id：直改动作恰好 1 个；整表重建的一组语句承载该表被吸收的全部条目；PRAGMA 包裹条为空数组 */
  entry_ids: string[];
  /** 可执行语句，已带结尾分号 */
  sql: string;
  /** 沿用条目的执行相位；PRAGMA 首条为 0、末条为 255 */
  phase: number;
  /** 语句作用的表名（原样大小写）；PRAGMA 包裹条为空串，字段本身仍在 */
  table: string;
  /** 人读告警（快照不可见对象、ADD COLUMN 限制等）；双实现逐字一致。无告警时线路省略该字段 */
  warnings?: string[];
  /** 该语句承载的条目里只要有一条不可逆（丢数据类动作），整条语句标为不可逆 */
  non_reversible: boolean;
}

export interface DdlScript {
  /** 正向脚本，按执行顺序排列；空勾选为空数组（不得为 null） */
  statements: DdlStatement[];
  /** 回滚脚本 = 源/目标互换后按同一套规则再生成；正向重建过的表在回滚方向强制重建。本字段是**建议脚本**，其中不可逆语句执行后无法再回退，故调用方须自存正向执行记录 */
  rollback: DdlStatement[];
  /** true 时首尾两条 PRAGMA foreign_keys 必须留在事务外执行（事务内是 no-op，取证 G6） */
  pragmas_outside_tx: boolean;
  /** 该数据库种类的 DDL 是否可在事务内执行（SQLite 实测 true，取证 G7） */
  ddl_in_tx: boolean;
}

export interface QueryRequest {
  /** SQL statement to execute */
  sql: string;
  /** Positional bind parameters */
  params?: Value[];
  connection_id?: ConnectionId;
  /** Optional client-provided query identifier (uuid). Server echoes it in the X-Query-ID response header. When omitted the server generates one. Use POST /api/queries/{query_id}/cancel to cancel an in-flight HTTP query. */
  query_id?: string;
  /** Schema context for the query */
  schema?: string;
  pagination?: PaginationParams;
  /** Query timeout in milliseconds (0 = no timeout) */
  timeout_ms?: number;
  /** Maximum rows to return (0 = use server default) */
  max_rows?: number;
}

export type StatementType = 'ddl' | 'delete' | 'insert' | 'other' | 'select' | 'update';

export interface QueryResult {
  /** Column metadata for the result set */
  columns: ResultColumn[];
  /** Row data as arrays of values */
  rows: Value[][];
  /** Number of rows affected (for DML) */
  affected_rows: number;
  /** Server-side execution time */
  execution_time_ms: number;
  /** True if results were truncated due to max_rows */
  truncated?: boolean;
  /** Total rows available (if known, for pagination) */
  total_rows?: number | null;
  /** Whether more rows are available */
  has_more?: boolean;
  /** Type of SQL statement executed */
  statement_type?: StatementType;
}

export interface ResultColumn {
  /** Column name or alias */
  name: string;
  /** Source table name if applicable */
  table?: string | null;
  /** Database-specific type name */
  type: string;
  generic_type?: GenericType;
  nullable?: boolean | null;
}

export interface BatchQueryRequest {
  /** Multiple SQL statements to execute in order */
  statements: QueryRequest[];
  connection_id?: ConnectionId;
  /** Stop execution on first error */
  stop_on_error: boolean;
}

export interface BatchQueryResult {
  /** Results in same order as statements; errors if stop_on_error=false */
  results: (PolyDBError | QueryResult)[];
  total_execution_time_ms: number;
}

export type FilterOperator = 'between' | 'contains' | 'empty' | 'ends_with' | 'eq' | 'ge' | 'gt' | 'in' | 'le' | 'like' | 'lt' | 'ne' | 'not_in' | 'not_like' | 'not_null' | 'null' | 'regex' | 'starts_with';

export type FilterLogic = 'and' | 'or';

export type SortDirection = 'asc' | 'desc';

export interface FilterCondition {
  /** Column name; server quotes it as an identifier (never interpolated raw) */
  column: string;
  op: FilterOperator;
  /** Single value for eq/ne/lt/le/gt/ge/like/not_like/contains/starts_with/ends_with/regex; ignored for null/not_null/empty */
  value?: Value;
  /** Match-case for the text ops only (like/not_like/contains/starts_with/ends_with/regex). true=force case-sensitive, false=force case-insensitive, absent=dialect default (behavior.md §13.5). Ignored by all other ops. */
  case_sensitive?: boolean;
  /** Upper bound for between */
  second_value?: Value;
  /** List for in/not_in (must be non-empty) */
  values?: Value[];
}

export interface OrderClause {
  column: string;
  dir: SortDirection;
}

export interface TableRowsRequest {
  /** Projection; omitted/empty = all columns */
  columns?: string[];
  /** Filter conditions combined per logic */
  conditions?: FilterCondition[];
  logic?: FilterLogic;
  order_by?: OrderClause[];
  /** Rows to skip */
  offset?: number;
  /** Max rows to return; server clamps to 10000 (behavior.md §13) */
  limit?: number;
}

export interface TableRowsResult {
  columns: ResultColumn[];
  rows: Value[][];
  offset: number;
  /** true when more rows exist beyond offset+limit (server fetches limit+1 and trims) */
  has_more: boolean;
  /** Optional engine-estimated row count when cheaply available */
  total_estimate?: number;
  execution_time_ms?: number;
}

export interface TableCountResult {
  count: number;
}

export type RedisKeyType = 'hash' | 'list' | 'none' | 'set' | 'stream' | 'string' | 'zset';

export interface RedisValue {
  type: RedisKeyType;
  /** Stream ID or serialized form */
  value: RedisZSetMember[] | string[] | Record<string, string> | string;
}

export interface RedisZSetMember {
  member: string;
  score: number;
}

export interface RedisKeyInfo {
  key: string;
  type: RedisKeyType;
  /** TTL in seconds (-1 = no expiry, -2 = expired) */
  ttl?: number | null;
}

export interface RedisScanPage {
  /** Cursor for next scan iteration (0 = done) */
  cursor: number;
  /** 回读：执行本次 SCAN 的那条专用连接当前所在 db。SELECT 是连接级状态且驱动独占该连接，故驱动跟踪值即服务端真值；可选，缺席时前端回落到本地记忆值 */
  db?: number;
  keys: RedisKeyInfo[];
}

export type RedisReplyType = 'array' | 'bulk_string' | 'error' | 'integer' | 'null' | 'simple_string';

export interface RedisReply {
  type: RedisReplyType;
  value?: number | RedisReply[] | unknown | string;
}

export interface RedisSelectDbRequest {
  index: number;
}

export interface RedisScanRequest {
  cursor?: number;
  pattern?: string;
  count?: number;
}

export interface RedisSetRequest {
  key: string;
  value: RedisValue;
  /** TTL in seconds (0 = no expiry) */
  ttl?: number;
}

export interface RedisExecCommandRequest {
  /** Command and arguments (e.g., ['GET', 'mykey']) */
  args: string[];
}

export type TransactionStatus = 'active' | 'committed' | 'rolled_back';

export interface TransactionInfo {
  id: string;
  connection_id: ConnectionId;
  status: TransactionStatus;
  started_at: string;
  isolation_level?: IsolationLevel;
}

export interface BeginTransactionRequest {
  connection_id: ConnectionId;
  isolation_level?: IsolationLevel;
}

export type IsolationLevel = 'read_committed' | 'read_uncommitted' | 'repeatable_read' | 'serializable';

export interface WsAuth {
  /** Bearer token; required when the server is started with POLYDB_SERVER_TOKEN, ignored otherwise (behavior.md §12.2) */
  token?: string;
}

// ─── WebSocket ────────────────────────────────────────────

export type ClientMessage =
  { type: 'hello'; auth?: WsAuth; client_version?: string; connection_id: ConnectionId }
  | { type: 'query'; query_id: string } & QueryRequest
  | { type: 'query_cancel'; query_id: string };

export type ServerMessage =
  | { type: 'hello_ack'; db_version?: string; server_version: string }
  | { type: 'query_cancelled'; query_id: string; rows_returned?: number }
  | { type: 'query_error'; error: PolyDBError; query_id: string }
  | { type: 'query_progress'; message?: string; query_id: string; rows_fetched?: number }
  | { type: 'query_result'; query_id: string; result: QueryResult }
  | { type: 'query_started'; query_id: string };
