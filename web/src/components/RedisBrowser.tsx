import { useCallback, useEffect, useRef, useState } from 'react';
import type { RedisKeyInfo, RedisReply, RedisValue, RedisZSetMember } from '../api';
import * as api from '../lib/api';
import CollapsiblePane from './CollapsiblePane';

interface Props {
  connId: string;
}

interface ConsoleEntry {
  cmd: string;
  reply?: RedisReply;
  error?: string;
}

const DBS = [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15];

export default function RedisBrowser({ connId }: Props) {
  const [db, setDb] = useState(0);
  const [pattern, setPattern] = useState('*');
  const [keys, setKeys] = useState<RedisKeyInfo[]>([]);
  const [cursor, setCursor] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selKey, setSelKey] = useState<string | null>(null);
  const [value, setValue] = useState<RedisValue | null>(null);
  const [valueError, setValueError] = useState<string | null>(null);
  const [consoleInput, setConsoleInput] = useState('');
  const [consoleEntries, setConsoleEntries] = useState<ConsoleEntry[]>([]);
  const [history, setHistory] = useState<string[]>([]);
  const [histIdx, setHistIdx] = useState(-1);
  const consoleRef = useRef<HTMLDivElement>(null);

  const loadKeys = useCallback(
    async (cursorStart: number, reset: boolean) => {
      setLoading(true);
      setError(null);
      try {
        const page = await api.kvScanKeys(connId, {
          cursor: cursorStart,
          pattern: pattern.trim() || '*',
          count: 100,
        });
        setCursor(page.cursor);
        setKeys((prev) => (reset ? page.keys : [...prev, ...page.keys]));
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setLoading(false);
      }
    },
    [connId, pattern],
  );

  useEffect(() => {
    setKeys([]);
    setSelKey(null);
    setValue(null);
    void loadKeys(0, true);
  }, [connId, loadKeys]);

  const changeDb = async (next: number) => {
    setDb(next);
    try {
      await api.kvSelectDb(connId, { index: next });
      setKeys([]);
      setSelKey(null);
      setValue(null);
      await loadKeys(0, true);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const openKey = async (key: string) => {
    setSelKey(key);
    setValue(null);
    setValueError(null);
    try {
      setValue(await api.kvGetValue(connId, key));
    } catch (e) {
      setValueError(e instanceof Error ? e.message : String(e));
    }
  };

  const runCommand = async (raw: string) => {
    const cmd = raw.trim();
    if (!cmd) return;
    const args = splitArgs(cmd);
    const entry: ConsoleEntry = { cmd };
    setConsoleEntries((prev) => [...prev, entry]);
    setConsoleInput('');
    setHistIdx(-1);
    setHistory((prev) => [...prev, cmd]);
    try {
      const reply = await api.kvExecCommand(connId, { args });
      setConsoleEntries((prev) => {
        const next = [...prev];
        next[next.length - 1] = { ...next[next.length - 1], reply };
        return next;
      });
    } catch (e) {
      setConsoleEntries((prev) => {
        const next = [...prev];
        next[next.length - 1] = { ...next[next.length - 1], error: e instanceof Error ? e.message : String(e) };
        return next;
      });
    }
  };

  const onConsoleKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      void runCommand(consoleInput);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (history.length === 0) return;
      const idx = histIdx === -1 ? history.length - 1 : Math.max(0, histIdx - 1);
      setHistIdx(idx);
      setConsoleInput(history[idx]);
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (histIdx === -1) return;
      const idx = histIdx + 1;
      if (idx >= history.length) {
        setHistIdx(-1);
        setConsoleInput('');
      } else {
        setHistIdx(idx);
        setConsoleInput(history[idx]);
      }
    }
  };

  useEffect(() => {
    if (consoleRef.current) {
      consoleRef.current.scrollTop = consoleRef.current.scrollHeight;
    }
  }, [consoleEntries]);

  const actions = (
    <select
      value={db}
      onChange={(e) => void changeDb(Number(e.target.value))}
      style={{ width: 'auto' }}
      title="数据库编号（SELECT）"
    >
      {DBS.map((d) => (
        <option key={d} value={d}>db {d}</option>
      ))}
    </select>
  );

  return (
    <>
      <CollapsiblePane title="Redis 键" variant="md" actions={actions}>
        <div className="pane-body tree" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {error && <div className="error-box">{error}</div>}
          <div className="row">
            <input
              value={pattern}
              onChange={(e) => setPattern(e.target.value)}
              placeholder="* 或 user:*"
              onKeyDown={(e) => {
                if (e.key === 'Enter') void loadKeys(0, true);
              }}
            />
            <button className="primary" disabled={loading} onClick={() => void loadKeys(0, true)}>
              {loading ? '…' : '扫描'}
            </button>
          </div>
          {keys.map((k) => (
            <div
              key={k.key}
              className={`leaf ${selKey === k.key ? 'selected' : ''}`}
              onClick={() => void openKey(k.key)}
            >
              <span className={`badge ${k.type}`}>{k.type}</span>
              <span className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {k.key}
              </span>
              {k.ttl != null && k.ttl > 0 && <span className="muted mono" style={{ marginLeft: 'auto' }}>{k.ttl}s</span>}
            </div>
          ))}
          {keys.length === 0 && !loading && <div className="empty">无匹配键</div>}
          {cursor !== 0 && (
            <button disabled={loading} onClick={() => void loadKeys(cursor, false)}>
              {loading ? '加载中…' : '加载更多'}
            </button>
          )}
        </div>
      </CollapsiblePane>
      <CollapsiblePane title="键值" variant="main">
        <div className="pane-body" style={{ display: 'flex', flexDirection: 'column', gap: 10, overflow: 'auto' }}>
          {valueError && <div className="error-box">{valueError}</div>}
          {selKey && <div className="mono muted" style={{ fontSize: 12 }}>{selKey}</div>}
          {value && <ValueView value={value} />}
          {!selKey && !value && <div className="empty">从左侧选择一个键查看值，或在下方执行 Redis 命令。</div>}
          <div
            className="pane"
            style={{ borderRight: 'none', borderTop: '1px solid var(--border)', marginTop: 'auto' }}
          >
            <div className="pane-header" style={{ fontSize: 12 }}>命令</div>
            <div ref={consoleRef} className="pane-body" style={{ maxHeight: 260, fontFamily: 'var(--mono)', fontSize: 12 }}>
              {consoleEntries.map((en, i) => (
                <div key={i} style={{ marginBottom: 6 }}>
                  <div style={{ color: 'var(--accent)' }}>&gt; {en.cmd}</div>
                  {en.error && <div style={{ color: 'var(--danger)', whiteSpace: 'pre-wrap' }}>{en.error}</div>}
                  {en.reply && <ReplyView reply={en.reply} />}
                </div>
              ))}
            </div>
            <div style={{ padding: 8, borderTop: '1px solid var(--border)' }}>
              <input
                value={consoleInput}
                onChange={(e) => setConsoleInput(e.target.value)}
                onKeyDown={onConsoleKey}
                placeholder="例如 GET user:1 / EXPIRE user:1 60"
                style={{ fontFamily: 'var(--mono)' }}
              />
            </div>
          </div>
        </div>
      </CollapsiblePane>
    </>
  );
}

// ─── 值渲染 ─────────────────────────────────────────────────

function ValueView({ value }: { value: RedisValue }) {
  switch (value.type) {
    case 'string':
    case 'stream':
      return <pre className="mono" style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{String(value.value)}</pre>;
    case 'list':
    case 'set': {
      const items = value.value as string[];
      return (
        <div className="mono" style={{ fontSize: 12 }}>
          {items.map((x, i) => (
            <div key={i}>{i + 1}) {x}</div>
          ))}
          {items.length === 0 && <div className="muted">（空）</div>}
        </div>
      );
    }
    case 'zset': {
      const rows = value.value as RedisZSetMember[];
      return (
        <table className="detail-table">
          <thead><tr><th>#</th><th>member</th><th>score</th></tr></thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={i}>
                <td className="mono">{i + 1}</td>
                <td className="mono">{r.member}</td>
                <td className="mono">{r.score}</td>
              </tr>
            ))}
          </tbody>
        </table>
      );
    }
    case 'hash': {
      const entries = Object.entries(value.value as Record<string, string>);
      return (
        <table className="detail-table">
          <thead><tr><th>field</th><th>value</th></tr></thead>
          <tbody>
            {entries.map(([k, v]) => (
              <tr key={k}>
                <td className="mono">{k}</td>
                <td className="mono">{v}</td>
              </tr>
            ))}
          </tbody>
        </table>
      );
    }
    default:
      return null;
  }
}

// ─── 命令回复渲染 ───────────────────────────────────────────

function ReplyView({ reply }: { reply: RedisReply }) {
  switch (reply.type) {
    case 'null':
      return <div className="muted">(nil)</div>;
    case 'integer':
      return <div className="mono">{String(reply.value)}</div>;
    case 'error':
      return <div style={{ color: 'var(--danger)', whiteSpace: 'pre-wrap' }}>(error) {String(reply.value)}</div>;
    case 'bulk_string':
      return <div className="mono" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{JSON.stringify(reply.value)}</div>;
    case 'simple_string':
      return <div className="mono">{String(reply.value)}</div>;
    case 'array':
      return <pre className="mono" style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{arrayRepr(reply.value as RedisReply[])}</pre>;
    default:
      return null;
  }
}

function arrayRepr(items: RedisReply[], depth = 0): string {
  const indent = '  '.repeat(depth);
  const lines: string[] = [];
  items.forEach((item, i) => {
    const num = `${i + 1}) `;
    if (item.type === 'array') {
      lines.push(`${indent}${num}`);
      lines.push(arrayRepr(item.value as RedisReply[], depth + 1));
    } else if (item.type === 'null') {
      lines.push(`${indent}${num}(nil)`);
    } else if (item.type === 'error') {
      lines.push(`${indent}${num}(error) ${String(item.value)}`);
    } else if (item.type === 'integer') {
      lines.push(`${indent}${num}${String(item.value)}`);
    } else {
      lines.push(`${indent}${num}${JSON.stringify(String(item.value))}`);
    }
  });
  return lines.join('\n');
}

// splitArgs：简单的命令行拆分（支持单双引号），供 exec_command 使用。
function splitArgs(input: string): string[] {
  const args: string[] = [];
  let cur = '';
  let quote: string | null = null;
  for (let i = 0; i < input.length; i++) {
    const ch = input[i];
    if (quote) {
      if (ch === quote) {
        quote = null;
      } else if (ch === '\\' && i + 1 < input.length && (input[i + 1] === quote || input[i + 1] === '\\')) {
        cur += input[++i];
      } else {
        cur += ch;
      }
    } else if (ch === '"' || ch === "'") {
      quote = ch;
    } else if (ch === ' ' || ch === '\t') {
      if (cur) {
        args.push(cur);
        cur = '';
      }
    } else {
      cur += ch;
    }
  }
  if (cur) args.push(cur);
  return args;
}
