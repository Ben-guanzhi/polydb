import { encode } from '@msgpack/msgpack';
import type { ClientMessage, PolyDBError, QueryRequest, QueryResult, ServerMessage } from '../api';
import { getServerToken } from './api';
import { decodeMsgpack } from './bigintSafe';

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
  // ready 只在收到 hello_ack 时 resolve；open 只代表 TCP 建连，auth 可能随后
  // 失败，此时 hello 会发往死 socket。open 不再单独 resolve。
  let resolveReady: () => void = () => {};
  let rejectReady: (e: unknown) => void = () => {};
  const ready = new Promise<void>((resolve, reject) => {
    resolveReady = resolve;
    rejectReady = reject;
  });
  const c: Client = { ws, pending, connId: null, ready, closed: false };
  ws.addEventListener('error', () => rejectReady(new Error('websocket connection error')));
  ws.addEventListener('message', (ev) => {
    const buf: ArrayBuffer = ev.data instanceof ArrayBuffer ? ev.data : new Uint8Array(ev.data).buffer;
    const msg = decodeMsgpack(buf) as ServerMessage;
    if (msg.type === 'hello_ack') {
      resolveReady();
      return;
    }
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
    c.closed = true;
    // 关闭的是当前全局客户端时清空槽位，否则下次 connect 会复用已死的 socket。
    if (client === c) client = null;
    for (const item of pending.values()) {
      item.handler.onError({
        code: 'POLYDB_ERR_UNKNOWN',
        message: 'websocket closed',
        retryable: true,
      });
    }
    pending.clear();
  });
  return c;
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
    // 服务端启用 POLYDB_SERVER_TOKEN 时 hello 必须携带 auth.token（behavior.md §12.2）。
    const token = getServerToken();
    send(next, {
      type: 'hello',
      connection_id: connId,
      client_version: '0.1.0',
      ...(token ? { auth: { token } } : {}),
    });
  } catch (e) {
    // 新连接就绪前失败：关闭死 socket 并恢复全局 client，避免全局槽位被
    // 一个永远等不到 hello_ack 的连接占住。
    next.closed = true;
    client = prev;
    next.ws.close();
    throw e;
  }
  // 新连接就绪：关闭并丢弃旧连接（一次只保留一个连接）。
  if (prev && !prev.closed) {
    prev.closed = true;
    prev.ws.close();
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
