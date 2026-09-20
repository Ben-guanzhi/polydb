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

export type FilterOperator = 'between' | 'eq' | 'ge' | 'gt' | 'in' | 'le' | 'like' | 'lt' | 'ne' | 'not_in' | 'not_like' | 'not_null' | 'null';

export type FilterLogic = 'and' | 'or';

export type SortDirection = 'asc' | 'desc';

export interface FilterCondition {
  /** Column name; server quotes it as an identifier (never interpolated raw) */
  column: string;
  op: FilterOperator;
  /** Single value for eq/ne/lt/le/gt/ge/like/not_like; ignored for null/not_null */
  value?: Value;
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
