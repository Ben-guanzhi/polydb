import { encode, decode } from '@msgpack/msgpack';
import type {
  BatchQueryRequest,
  BatchQueryResult,
  ColumnInfo,
  ConnectionInfo,
  ConnectionStatus,
  CreateConnectionRequest,
  DdlScript,
  ForeignKeyInfo,
  IndexInfo,
  PolyDBError,
  QueryRequest,
  QueryResult,
  RedisExecCommandRequest,
  RedisReply,
  RedisScanPage,
  RedisScanRequest,
  RedisSelectDbRequest,
  RedisSetRequest,
  RedisValue,
  SchemaDdlRequest,
  SchemaDiffPlan,
  SchemaDiffRequest,
  SchemaInfo,
  TableCountResult,
  TableInfo,
  TableRowsRequest,
  TableRowsResult,
  TransactionInfo,
  UpdateConnectionRequest,
} from '../api';

const MSGPACK = 'application/msgpack';

// 服务端启用 POLYDB_SERVER_TOKEN 时的 Bearer 凭据（behavior.md §12.1）。
// 存 localStorage，跨会话保留；未设置时不发送 Authorization 头。
const TOKEN_KEY = 'polydb.serverToken';

export function getServerToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? '';
  } catch {
    return '';
  }
}

export function setServerToken(token: string): void {
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
  } catch { /* ignore */ }
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly detail?: Record<string, unknown>;
  constructor(status: number, code: string, message: string, detail?: Record<string, unknown>) {
    super(message);
    this.status = status;
    this.code = code;
    this.detail = detail;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const token = getServerToken();
  const headers: Record<string, string> = { Accept: MSGPACK };
  if (token) headers.Authorization = `Bearer ${token}`;
  const init: RequestInit = { method, headers };
  if (body !== undefined) {
    init.headers = { ...init.headers, 'Content-Type': MSGPACK };
    init.body = encode(body);
  }
  const res = await fetch(path, init);
  const raw = await res.arrayBuffer();
  const isMsgpack = (res.headers.get('content-type') ?? '').includes(MSGPACK);
  let data: unknown = null;
  if (raw.byteLength > 0) {
    data = isMsgpack ? decode(raw as ArrayBuffer) : res.status === 204 ? null : decodeJson(raw);
  }
  if (!res.ok) {
    throw toApiError(res.status, data);
  }
  return data as T;
}

function decodeJson(raw: ArrayBuffer): unknown {
  try {
    return JSON.parse(new TextDecoder().decode(raw));
  } catch {
    return null;
  }
}

function toApiError(status: number, data: unknown): ApiError {
  if (data && typeof data === 'object') {
    const pe = data as PolyDBError;
    if (typeof pe.code === 'string' && typeof pe.message === 'string') {
      const msg = pe.code === 'POLYDB_ERR_READ_ONLY'
        ? '连接处于只读（安全）模式：写语句/KV 写已被服务端拒绝（behavior.md §12）'
        : pe.message;
      return new ApiError(status, pe.code, msg, pe.detail);
    }
  }
  return new ApiError(status, 'POLYDB_ERR_UNKNOWN', `HTTP ${status}`);
}

function enc(id: string, rest: string): string {
  return `/api/connections/${encodeURIComponent(id)}${rest}`;
}

// ─── 系统 ───────────────────────────────────────────────────

export function health() {
  return request<{ status: string; version?: string }>('GET', '/api/health');
}

// ─── connections ────────────────────────────────────────────

export function listConnections() {
  return list<ConnectionInfo>('/api/connections');
}

// Go server 对空 slice 用 msgpack null（Rust 恒为 []），列表端点统一防御。
async function list<T>(path: string): Promise<T[]> {
  return (await request<T[] | null>('GET', path)) ?? [];
}

export function createConnection(req: CreateConnectionRequest) {
  return request<ConnectionInfo>('POST', '/api/connections', req);
}

export function getConnection(id: string) {
  return request<ConnectionInfo>('GET', enc(id, ''));
}

export function updateConnection(id: string, req: UpdateConnectionRequest) {
  return request<ConnectionInfo>('PUT', enc(id, ''), req);
}

export function deleteConnection(id: string) {
  return request<void>('DELETE', enc(id, ''));
}

export function testConnection(id: string) {
  return request<ConnectionStatus>('POST', enc(id, '/test'));
}

// ─── 元数据 ─────────────────────────────────────────────────

export function listSchemas(id: string) {
  return list<SchemaInfo>(enc(id, '/schemas'));
}

export function listTables(id: string, schema: string) {
  return list<TableInfo>(enc(id, `/schemas/${encodeURIComponent(schema)}/tables`));
}

export function listColumns(id: string, schema: string, table: string) {
  return list<ColumnInfo>(enc(id, `/schemas/${encodeURIComponent(schema)}/tables/${encodeURIComponent(table)}/columns`));
}

export function listIndexes(id: string, schema: string, table: string) {
  return list<IndexInfo>(enc(id, `/schemas/${encodeURIComponent(schema)}/tables/${encodeURIComponent(table)}/indexes`));
}

export function listForeignKeys(id: string, schema: string, table: string) {
  return list<ForeignKeyInfo>(enc(id, `/schemas/${encodeURIComponent(schema)}/tables/${encodeURIComponent(table)}/foreign-keys`));
}

export function getDDL(id: string, schema: string, table: string) {
  return request<{ sql: string }>('GET', enc(id, `/schemas/${encodeURIComponent(schema)}/tables/${encodeURIComponent(table)}/ddl`));
}

// ─── 查询 ───────────────────────────────────────────────────

export function executeQuery(id: string, req: QueryRequest) {
  return request<QueryResult>('POST', enc(id, '/query'), req);
}

export function executeBatch(id: string, req: BatchQueryRequest) {
  return request<BatchQueryResult>('POST', enc(id, '/query/batch'), req);
}

export function cancelQuery(queryId: string) {
  return request<void>('POST', `/api/queries/${encodeURIComponent(queryId)}/cancel`);
}

// ─── 事务 ──────────────────────────────────────────────────

export function beginTransaction(id: string, iso?: string) {
  return request<TransactionInfo>('POST', enc(id, '/transactions'), {
    connection_id: id,
    isolation_level: iso,
  });
}

export function executeInTx(txId: string, req: QueryRequest) {
  return request<QueryResult>('POST', `/api/transactions/${encodeURIComponent(txId)}/execute`, req);
}

export function commitTransaction(txId: string) {
  return request<TransactionInfo>('POST', `/api/transactions/${encodeURIComponent(txId)}/commit`);
}

export function rollbackTransaction(txId: string) {
  return request<TransactionInfo>('POST', `/api/transactions/${encodeURIComponent(txId)}/rollback`);
}

// ─── 结构比对与同步语句生成（Compare & Sync E2/E3）─────────────

export function schemaDiff(req: SchemaDiffRequest) {
  return request<SchemaDiffPlan>('POST', '/api/schema/diff', req);
}

export function schemaDdl(req: SchemaDdlRequest) {
  return request<DdlScript>('POST', '/api/schema/ddl', req);
}

// ─── 表数据浏览（M11）────────────────────────────────────────

export function browseRows(id: string, schema: string, table: string, req: TableRowsRequest) {
  const path = enc(id, `/schemas/${encodeURIComponent(schema)}/tables/${encodeURIComponent(table)}/rows/query`);
  return request<TableRowsResult>('POST', path, req);
}

export function browseRowsCount(id: string, schema: string, table: string, req: TableRowsRequest) {
  const path = enc(id, `/schemas/${encodeURIComponent(schema)}/tables/${encodeURIComponent(table)}/rows/count`);
  return request<TableCountResult>('POST', path, req);
}

// ─── KV（Redis）─────────────────────────────────────────────

export function kvSelectDb(id: string, req: RedisSelectDbRequest) {
  return request<void>('POST', enc(id, '/kv/select'), req);
}

export function kvScanKeys(id: string, req: RedisScanRequest) {
  return request<RedisScanPage>('POST', enc(id, '/kv/scan'), req);
}

export function kvGetValue(id: string, key: string) {
  return request<RedisValue>('GET', enc(id, `/kv/keys/${encodeURIComponent(key)}`));
}

export function kvSetValue(id: string, req: RedisSetRequest) {
  return request<void>('PUT', enc(id, `/kv/keys/${encodeURIComponent(req.key)}`), req);
}

export function kvExecCommand(id: string, req: RedisExecCommandRequest) {
  return request<RedisReply>('POST', enc(id, '/kv/exec'), req);
}

// ─── 大字段旁路取回（spec/encoding.md §5.2）────────────────

/** §5.2 规定：前端对取回 404 展示的固定文案。 */
export const BLOB_EXPIRED_MSG = '已过期，请重新执行查询';

export interface BlobContent {
  status: 200 | 206;
  data: ArrayBuffer;
  /** 206 时的 Content-Range，如 bytes 0-1048575/2097152 */
  contentRange?: string;
}

/**
 * GET /api/connections/{id}/blobs/{blob_id}：取回旁路大字段内容。
 * range 为单段字节区间（如 bytes=0-1048575；多段/语法错误服务端按无 Range 处理）。
 * 成功返回二进制内容；404/416 抛 ApiError。
 */
export async function fetchBlob(connectionId: string, blobId: string, range?: string): Promise<BlobContent> {
  const token = getServerToken();
  const headers: Record<string, string> = { Accept: 'application/octet-stream' };
  if (token) headers.Authorization = `Bearer ${token}`;
  if (range) headers.Range = range;
  const res = await fetch(
    `/api/connections/${encodeURIComponent(connectionId)}/blobs/${encodeURIComponent(blobId)}`,
    { headers },
  );
  if (res.ok) {
    return {
      status: res.status as 200 | 206,
      data: await res.arrayBuffer(),
      contentRange: res.headers.get('Content-Range') ?? undefined,
    };
  }
  const raw = await res.arrayBuffer();
  throw toApiError(res.status, raw.byteLength > 0 ? decodeJson(raw) : null);
}

/** 取回失败的展示文案：404 用 §5.2 固定文案，其余回退 code+message。 */
export function blobFetchErrorMessage(e: unknown): string {
  if (e instanceof ApiError) {
    return e.status === 404 ? BLOB_EXPIRED_MSG : `${e.code}: ${e.message}`;
  }
  return e instanceof Error ? e.message : String(e);
}

/** ArrayBuffer → base64（复制单元格导出用；分块避免大数组爆栈）。 */
export function arrayBufferToBase64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  const chunk = 0x8000;
  let bin = '';
  for (let i = 0; i < bytes.length; i += chunk) {
    bin += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(bin);
}