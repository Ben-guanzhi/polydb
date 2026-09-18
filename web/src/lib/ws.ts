import { decode, encode } from '@msgpack/msgpack';
import type { ClientMessage, PolyDBError, QueryRequest, QueryResult, ServerMessage } from '../api';

type QueryHandler = {
  onStarted?: (queryId: string) => void;
  onProgress?: (p: { rows_fetched?: number; message?: string }) => void;
  onResult: (r: QueryResult) => void;
  onError: (e: PolyDBError) => void;
  onCancelled: () => void;
};

type Pending = { queryId: string; connId: string; handler: QueryHandler };

type Client = {
  ws: WebSocket;
  pending: Map<string, Pending>;
  connId: string | null;
  ready: Promise<void>;
  closed: boolean;
};

let client: Client | null = null;

function makeId(): string {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  return `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

function wsUrl(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}${window.location.host}/ws`;
}

function newClient(): Client {
  const ws = new WebSocket(wsUrl());
  ws.binaryType = 'arraybuffer';
  const pending = new Map<string, Pending>();
  const ready = new Promise<void>((resolve, reject) => {
    ws.addEventListener('open', () => resolve(), { once: true });
    ws.addEventListener('error', () => reject(new Error('websocket connection error')), { once: true });
  });
  ws.addEventListener('message', (ev) => {
    const buf: ArrayBuffer = ev.data instanceof ArrayBuffer ? ev.data : new Uint8Array(ev.data).buffer;
    const msg = decode(buf) as ServerMessage;
    if (msg.type === 'hello_ack') return;
    if (!('query_id' in msg) || !msg.query_id) return;
    const item = pending.get(msg.query_id);
    if (!item) return;
    switch (msg.type) {
      case 'query_started':
        item.handler.onStarted?.(item.queryId);
        break;
      case 'query_progress':
        item.handler.onProgress?.({ rows_fetched: msg.rows_fetched, message: msg.message });
        break;
      case 'query_result':
        pending.delete(msg.query_id);
        item.handler.onResult(msg.result);
        break;
      case 'query_error':
        pending.delete(msg.query_id);
        item.handler.onError(msg.error);
        break;
      case 'query_cancelled':
        pending.delete(msg.query_id);
        item.handler.onCancelled();
        break;
    }
  });
  ws.addEventListener('close', () => {
    for (const item of pending.values()) {
      item.handler.onError({
        code: 'POLYDB_ERR_UNKNOWN',
        message: 'websocket closed',
        retryable: true,
      });
    }
    pending.clear();
  });
  return { ws, pending, connId: null, ready, closed: false };
}

function send(c: Client, msg: ClientMessage) {
  const bytes = encode(msg);
  c.ws.send(bytes as unknown as BufferSource);
}

async function connect(connId: string): Promise<Client> {
  if (client && !client.closed && client.connId === connId) return client;
  const prev = client;
  const next = newClient();
  client = next;
  next.connId = connId;
  try {
    await next.ready;
    send(next, { type: 'hello', connection_id: connId, client_version: '0.1.0' });
  } catch (e) {
    if (prev) client = prev;
    throw e;
  } finally {
    if (prev && !prev.closed) {
      prev.closed = true;
      prev.ws.close();
    }
  }
  return next;
}

function query(connId: string, req: QueryRequest, handler: QueryHandler): Promise<string> {
  return connect(connId).then((c) => {
    // 客户端可在 req.query_id 中显式指定；否则本地生成。
    // 注意先展开 req 再覆盖 query_id，防止被 req.query_id 反向覆盖。
    const queryId = req.query_id && req.query_id.length > 0 ? req.query_id : makeId();
    c.pending.set(queryId, { queryId, connId, handler });
    send(c, { type: 'query', ...req, query_id: queryId });
    return queryId;
  });
}

function cancel(queryId: string) {
  if (!client || !client.pending.has(queryId)) return;
  send(client, { type: 'query_cancel', query_id: queryId });
}

export { connect, query, cancel, makeId };
