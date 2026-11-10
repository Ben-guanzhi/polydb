import { encode } from '@msgpack/msgpack';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// 可控的 WebSocket 替身：记录实例、已发送消息与 close 调用，并允许测试手工
// 派发 open/message/error/close 事件。
class MockWebSocket {
  static instances: MockWebSocket[] = [];
  binaryType = 'blob';
  readonly url: string;
  sent: Uint8Array[] = [];
  closed = false;
  private readonly listeners = new Map<string, Array<(ev: unknown) => void>>();

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }
  addEventListener(type: string, fn: (ev: unknown) => void): void {
    const arr = this.listeners.get(type) ?? [];
    arr.push(fn);
    this.listeners.set(type, arr);
  }
  send(data: Uint8Array): void {
    this.sent.push(data);
  }
  close(): void {
    this.closed = true;
    this.emit('close', {});
  }
  emit(type: string, ev: unknown): void {
    for (const fn of [...(this.listeners.get(type) ?? [])]) fn(ev);
  }
}

const ORIGINAL_WS = globalThis.WebSocket;
let wsModule: typeof import('./ws');

// ws.ts 的全局 client 是模块级单例，每个用例用 resetModules 拿到干净状态。
beforeEach(async () => {
  vi.resetModules();
  MockWebSocket.instances = [];
  Object.defineProperty(globalThis, 'WebSocket', {
    value: MockWebSocket,
    configurable: true,
    writable: true,
  });
  wsModule = await import('./ws');
});

afterEach(() => {
  Object.defineProperty(globalThis, 'WebSocket', {
    value: ORIGINAL_WS,
    configurable: true,
    writable: true,
  });
});

function helloAck(): Uint8Array {
  return encode({ type: 'hello_ack', server_version: 'test-1.0' });
}

// 触发 open + hello_ack，返回已就绪的客户端。
function connectReady(connId: string): Promise<Awaited<ReturnType<typeof wsModule.connect>>> {
  const p = wsModule.connect(connId);
  const ws = MockWebSocket.instances[MockWebSocket.instances.length - 1];
  ws.emit('open', {});
  ws.emit('message', { data: helloAck() });
  return p;
}

describe('ws.ts ready/hello_ack', () => {
  it('does not resolve connect before hello_ack (open alone is not enough)', async () => {
    const p = wsModule.connect('c1');
    const ws = MockWebSocket.instances[0];
    ws.emit('open', {});

    // open 之后 ready 仍必须 pending，避免 auth 失败时 hello 发往死 socket。
    const state = await Promise.race([
      (p as Promise<unknown>).then(() => 'resolved'),
      new Promise((r) => setTimeout(() => r('pending'), 30)),
    ]);
    expect(state).toBe('pending');

    ws.emit('message', { data: helloAck() });
    await expect(p).resolves.toBeDefined();
    // hello 必须携带 connection_id（auth 场景另有 token）。
    expect(MockWebSocket.instances[0].sent.length).toBe(1);
  });

  it('rejects when the socket errors before hello_ack', async () => {
    const p = wsModule.connect('c1');
    const ws = MockWebSocket.instances[0];
    ws.emit('error', {});
    await expect(p).rejects.toThrow('websocket connection error');
  });
});

describe('ws.ts close handling', () => {
  it('marks the client closed, clears the global client, and does not reuse it', async () => {
    const c1 = await connectReady('c1');
    const ws1 = MockWebSocket.instances[0];
    expect(c1.closed).toBe(false);

    ws1.emit('close', {});
    expect(c1.closed).toBe(true);

    // 全局 client 被清空：再次 connect 打开新 socket，而不是复用已关闭的 c1。
    const c2 = await connectReady('c1');
    expect(MockWebSocket.instances).toHaveLength(2);
    expect(c2).not.toBe(c1);
    expect(c2.closed).toBe(false);
  });

  it('restores the previous client and closes next.ws when a new connection fails', async () => {
    const c1 = await connectReady('A');
    const p = wsModule.connect('B');
    const ws2 = MockWebSocket.instances[1];
    ws2.emit('error', {});
    await expect(p).rejects.toThrow();
    expect(ws2.closed).toBe(true);

    // 失败后全局 client 恢复为 c1，且仍可复用（不新建 socket）。
    const again = await wsModule.connect('A');
    expect(again).toBe(c1);
    expect(MockWebSocket.instances).toHaveLength(2);
  });

  it('replaces the previous client when a new connId connects successfully', async () => {
    const c1 = await connectReady('A');
    const c2 = await connectReady('B');
    expect(c2).not.toBe(c1);
    expect(c1.closed).toBe(true);
    expect(MockWebSocket.instances[0].closed).toBe(true);
  });
});
