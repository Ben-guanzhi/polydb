import { useEffect, useMemo, useRef, useState } from 'react';
import type { ConnectionInfo } from '../api';
import * as api from '../lib/api';
import { rankTables, type SearchableTable } from '../lib/tableSearch';
import { tableTabId, type OpenTab } from '../lib/openTabs';
import { DbIcon, TableIcon } from './Icons';

// U3 Quick Switcher（Ctrl+P，参考 TablePro Cmd+P 直达表视图）：跳转已打开 tab + 搜表 + 切连接。

interface Props {
  open: boolean;
  onClose: () => void;
  conn: ConnectionInfo | null;
  connections: ConnectionInfo[];
  openTabs: OpenTab[];
  onOpenTable: (schema: string, table: string) => void;
  onSelectTab: (id: string) => void;
  onSwitchConn: (c: ConnectionInfo) => void;
}

const CACHE_TTL_MS = 60_000;
const tableCache = new Map<string, { at: number; tables: SearchableTable[] }>();

async function loadTables(conn: ConnectionInfo): Promise<SearchableTable[]> {
  const hit = tableCache.get(conn.id);
  if (hit && Date.now() - hit.at < CACHE_TTL_MS) return hit.tables;
  const schemas = await api.listSchemas(conn.id);
  const perSchema = await Promise.all(
    schemas.map(async (s) => {
      const tables = await api.listTables(conn.id, s.name).catch(() => []);
      return tables.map((t) => ({ schema: s.name, table: t.name, rowCount: t.row_count }));
    }),
  );
  const all = perSchema.flat();
  tableCache.set(conn.id, { at: Date.now(), tables: all });
  return all;
}

export function invalidateTableCache(connId: string) {
  tableCache.delete(connId);
}

type Row =
  | { kind: 'tab'; id: string; label: string }
  | { kind: 'table'; schema: string; table: string; rowCount?: number | null }
  | { kind: 'conn'; conn: ConnectionInfo };

export default function QuickSwitcher({ open, onClose, conn, connections, openTabs, onOpenTable, onSelectTab, onSwitchConn }: Props) {
  const [query, setQuery] = useState('');
  const [selIdx, setSelIdx] = useState(0);
  const [tables, setTables] = useState<SearchableTable[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!open) return;
    setQuery('');
    setSelIdx(0);
    setLoadError(null);
    const t = setTimeout(() => inputRef.current?.focus(), 0);
    if (conn && conn.kind !== 'redis') {
      setLoading(true);
      loadTables(conn)
        .then(setTables)
        .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
        .finally(() => setLoading(false));
    } else {
      setTables([]);
    }
    return () => clearTimeout(t);
  }, [open, conn]);

  const rows: Row[] = useMemo(() => {
    const q = query.trim().toLowerCase();
    const matches = (label: string) => q === '' || label.toLowerCase().includes(q);
    const tabRows: Row[] = [];
    for (const t of openTabs) {
      if (t.kind === 'workbench') {
        if (matches('SQL 工作台')) tabRows.push({ kind: 'tab', id: t.id, label: 'SQL 工作台' });
      } else {
        const label = `${t.schema}.${t.table}`;
        if (matches(label)) tabRows.push({ kind: 'tab', id: t.id, label });
      }
    }
    const openIdSet = new Set(
      openTabs.filter((t): t is Extract<OpenTab, { kind: 'table' }> => t.kind === 'table').map((t) => t.id),
    );
    // 全部表搜索：排除已打开（避免与跳转分组重复）。
    const tableRows: Row[] = rankTables(query, tables)
      .filter((t) => !openIdSet.has(tableTabId(t.schema, t.table)))
      .map((t) => ({ kind: 'table', ...t }));
    const connRows: Row[] = connections
      .filter((c) => c.id !== conn?.id)
      .map((c) => ({ kind: 'conn' as const, conn: c }));
    return [...tabRows, ...tableRows, ...connRows];
  }, [query, tables, connections, conn?.id, openTabs]);

  useEffect(() => {
    if (selIdx >= rows.length) setSelIdx(Math.max(0, rows.length - 1));
  }, [rows.length, selIdx]);

  useEffect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); onClose(); }
      else if (e.key === 'ArrowDown') { e.preventDefault(); e.stopPropagation(); setSelIdx((n) => Math.min(n + 1, Math.max(0, rows.length - 1))); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); e.stopPropagation(); setSelIdx((n) => Math.max(0, n - 1)); }
      else if (e.key === 'Enter') {
        e.preventDefault(); e.stopPropagation();
        const r = rows[selIdx];
        if (!r) return;
        if (r.kind === 'tab') onSelectTab(r.id);
        else if (r.kind === 'table') onOpenTable(r.schema, r.table);
        else onSwitchConn(r.conn);
        onClose();
      } else if (e.key === 'Tab') { e.preventDefault(); setSelIdx((n) => (n + 1) % Math.max(1, rows.length)); }
    };
    window.addEventListener('keydown', h, true);
    return () => window.removeEventListener('keydown', h, true);
  }, [open, rows, selIdx, onClose, onOpenTable, onSelectTab, onSwitchConn]);

  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-idx="${selIdx}"]`);
    el?.scrollIntoView({ block: 'nearest' });
  }, [selIdx]);

  if (!open) return null;

  return (
    <div
      style={{
        position: 'fixed', inset: 0, zIndex: 1000,
        background: 'rgba(0,0,0,0.35)',
        display: 'flex', alignItems: 'flex-start', justifyContent: 'center',
        padding: '12vh 16px 16px',
      }}
      onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }}
    >
      <div style={{
        width: '100%', maxWidth: 640, height: 'min(65vh, 560px)',
        background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 6,
        boxShadow: '0 8px 32px rgba(0,0,0,0.25)',
        display: 'flex', flexDirection: 'column', overflow: 'hidden',
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '10px 12px', borderBottom: '1px solid var(--border)' }}>
          <span style={{ color: 'var(--muted)', fontSize: 14 }}>🔍</span>
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => { setQuery(e.target.value); setSelIdx(0); }}
            placeholder={conn && conn.kind !== 'redis' ? '搜索表（schema.table 模糊匹配）或连接…' : '搜索连接…'}
            style={{ flex: 1, background: 'transparent', border: 'none', outline: 'none', color: 'var(--fg)', fontSize: 14, fontFamily: 'inherit' }}
          />
          <kbd style={{ fontFamily: 'monospace', fontSize: 10, color: 'var(--muted)', background: 'var(--bg-alt)', border: '1px solid var(--border)', borderRadius: 3, padding: '1px 5px' }}>Esc</kbd>
        </div>
        {loading && <div className="muted" style={{ padding: '6px 12px', fontSize: 12 }}>加载表清单…</div>}
        {loadError && <div className="error-box">{loadError}</div>}
        <div ref={listRef} style={{ flex: 1, overflow: 'auto', padding: 4 }}>
          {rows.length === 0 && !loading && (
            <div style={{ padding: '20px 12px', textAlign: 'center', color: 'var(--muted)', fontSize: 12 }}>
              {conn ? '无匹配' : '未选中连接，可搜索切换连接'}
            </div>
          )}
          {rows.map((r, idx) => {
            const selected = idx === selIdx;
            const rowKey = r.kind === 'tab' ? r.id : r.kind === 'table' ? `${r.schema}.${r.table}` : r.conn.id;
            return (
              <div
                key={rowKey}
                data-idx={idx}
                onMouseEnter={() => setSelIdx(idx)}
                onClick={() => {
                  if (r.kind === 'tab') onSelectTab(r.id);
                  else if (r.kind === 'table') onOpenTable(r.schema, r.table);
                  else onSwitchConn(r.conn);
                  onClose();
                }}
                style={selected
                  ? { display: 'flex', alignItems: 'center', gap: 8, padding: '6px 12px', borderRadius: 3, background: 'var(--accent-dim)', cursor: 'pointer' }
                  : { display: 'flex', alignItems: 'center', gap: 8, padding: '6px 12px', borderRadius: 3, cursor: 'pointer' }}
              >
                <span className="icon" style={{ color: 'var(--muted)' }}>
                  {r.kind === 'conn' ? <DbIcon kind={r.conn.kind} /> : <TableIcon />}
                </span>
                {r.kind === 'tab' ? (
                  <>
                    <span className="mono" style={{ fontSize: 13 }}>{r.label}</span>
                    <span className="muted" style={{ marginLeft: 'auto', fontSize: 10 }}>已打开</span>
                  </>
                ) : r.kind === 'table' ? (
                  <>
                    <span className="mono" style={{ fontSize: 13 }}>{r.table}</span>
                    <span className="muted" style={{ fontSize: 11 }}>{r.schema}</span>
                    {r.rowCount != null && <span className="muted mono" style={{ marginLeft: 'auto', fontSize: 10 }}>{r.rowCount}</span>}
                  </>
                ) : (
                  <>
                    <span style={{ flex: 1, fontSize: 13 }}>{r.conn.name}</span>
                    <span className={`badge ${r.conn.kind}`}>{r.conn.kind}</span>
                  </>
                )}
              </div>
            );
          })}
        </div>
        {rows.length > 0 && (
          <div style={{ display: 'flex', gap: 12, alignItems: 'center', padding: '4px 12px', borderTop: '1px solid var(--border)', fontSize: 10, color: 'var(--muted)', flexWrap: 'wrap' }}>
            <span><kbd style={{ fontFamily: 'monospace', padding: '0 4px', background: 'var(--bg-alt)', border: '1px solid var(--border)', borderRadius: 2 }}>↑↓</kbd> 选择</span>
            <span><kbd style={{ fontFamily: 'monospace', padding: '0 4px', background: 'var(--bg-alt)', border: '1px solid var(--border)', borderRadius: 2 }}>Enter</kbd> 打开表 / 切连接</span>
            <span><kbd style={{ fontFamily: 'monospace', padding: '0 4px', background: 'var(--bg-alt)', border: '1px solid var(--border)', borderRadius: 2 }}>Esc</kbd> 关闭</span>
            <span style={{ marginLeft: 'auto' }}>{rows.length} 项</span>
          </div>
        )}
      </div>
    </div>
  );
}
