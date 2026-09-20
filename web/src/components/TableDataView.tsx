import { useCallback, useEffect, useRef, useState, type CSSProperties } from 'react';
import type { FilterCondition, FilterOperator, SortDirection, TableRowsResult } from '../api';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';

// ─── M11 表数据浏览器（behavior.md §13）：服务端分页/排序/过滤 ───

const OPS: { value: FilterOperator; label: string; needsValue: boolean; needsValues: boolean; needsSecond: boolean }[] = [
  { value: 'eq', label: '= 等于', needsValue: true, needsValues: false, needsSecond: false },
  { value: 'ne', label: '≠ 不等于', needsValue: true, needsValues: false, needsSecond: false },
  { value: 'lt', label: '< 小于', needsValue: true, needsValues: false, needsSecond: false },
  { value: 'le', label: '≤ 小于等于', needsValue: true, needsValues: false, needsSecond: false },
  { value: 'gt', label: '> 大于', needsValue: true, needsValues: false, needsSecond: false },
  { value: 'ge', label: '≥ 大于等于', needsValue: true, needsValues: false, needsSecond: false },
  { value: 'like', label: 'LIKE 模式', needsValue: true, needsValues: false, needsSecond: false },
  { value: 'not_like', label: 'NOT LIKE 模式', needsValue: true, needsValues: false, needsSecond: false },
  { value: 'in', label: 'IN 列表', needsValue: false, needsValues: true, needsSecond: false },
  { value: 'not_in', label: 'NOT IN 列表', needsValue: false, needsValues: true, needsSecond: false },
  { value: 'between', label: 'BETWEEN 区间', needsValue: true, needsValues: false, needsSecond: true },
  { value: 'null', label: 'IS NULL', needsValue: false, needsValues: false, needsSecond: false },
  { value: 'not_null', label: 'IS NOT NULL', needsValue: false, needsValues: false, needsSecond: false },
];

const PAGE_SIZES = [50, 200, 1000];

interface CondRow {
  column: string;
  op: FilterOperator;
  value: string;
  second: string;
  values: string;
}

const btn: CSSProperties = {
  fontFamily: 'inherit', fontSize: 12, padding: '3px 10px',
  background: 'var(--bg-alt, rgba(0,0,0,0.1))', color: 'var(--fg)',
  border: '1px solid var(--border)', borderRadius: 3, cursor: 'pointer',
};
const input: CSSProperties = {
  fontFamily: 'inherit', fontSize: 12, background: 'var(--bg)', color: 'var(--fg)',
  border: '1px solid var(--border)', borderRadius: 3, padding: '3px 6px',
};
const cell: CSSProperties = {
  padding: '4px 8px', borderBottom: '1px solid var(--border)',
  whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', maxWidth: 320,
};
const th: CSSProperties = {
  ...cell, position: 'sticky', top: 0, background: 'var(--bg-alt, rgba(0,0,0,0.08))',
  cursor: 'pointer', userSelect: 'none', textAlign: 'left', fontWeight: 600,
};

function condToWire(r: CondRow): FilterCondition | null {
  if (!r.column) return null;
  const meta = OPS.find((o) => o.value === r.op);
  if (!meta) return null;
  const numish = (s: string): number | string => (/^-?\d+(\.\d+)?$/.test(s.trim()) ? Number(s) : s);
  const wire: FilterCondition = { column: r.column, op: r.op };
  if (meta.needsValue) wire.value = numish(r.value);
  if (meta.needsSecond) wire.second_value = numish(r.second);
  if (meta.needsValues) {
    wire.values = r.values.split(',').map((s) => numish(s.trim())).filter((v) => v !== '');
    if (wire.values.length === 0) return null;
  }
  return wire;
}

interface Props {
  connId: string;
  schema: string;
  table: string;
  onOpenSql: (schema: string, table: string) => void;
}

export default function TableDataView({ connId, schema, table, onOpenSql }: Props) {
  const [pageSize, setPageSize] = useState(200);
  const [offset, setOffset] = useState(0);
  const [sortKey, setSortKey] = useState<{ column: string; dir: SortDirection } | null>(null);
  const [conds, setConds] = useState<CondRow[]>([]);
  const [logic, setLogic] = useState<'and' | 'or'>('and');
  const [result, setResult] = useState<TableRowsResult | null>(null);
  const [exactCount, setExactCount] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showFilter, setShowFilter] = useState(false);
  const reqSeq = useRef(0);

  const qualified = `${schema}.${table}`;

  const buildReq = useCallback(() => {
    const conditions = conds.map(condToWire).filter((c): c is FilterCondition => c !== null);
    return {
      conditions,
      logic: conds.some((c) => condToWire(c)) ? logic : undefined,
      order_by: sortKey ? [{ column: sortKey.column, dir: sortKey.dir }] : undefined,
      offset,
      limit: pageSize,
    };
  }, [conds, logic, sortKey, offset, pageSize]);

  const load = useCallback(async () => {
    const seq = ++reqSeq.current;
    setLoading(true);
    setError(null);
    try {
      const res = await api.browseRows(connId, schema, table, buildReq());
      if (seq === reqSeq.current) setResult(res);
    } catch (e) {
      if (seq === reqSeq.current) {
        setError(e instanceof ApiError ? `${e.code}: ${e.message}` : String(e));
        setResult(null);
      }
    } finally {
      if (seq === reqSeq.current) setLoading(false);
    }
  }, [connId, schema, table, buildReq]);

  useEffect(() => { void load(); }, [load]);
  // 翻页/过滤/排序变化后精确计数失效
  useEffect(() => { setExactCount(null); }, [offset, pageSize, conds, logic, sortKey]);

  const runCount = async () => {
    try {
      const res = await api.browseRowsCount(connId, schema, table, buildReq());
      setExactCount(res.count);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.code}: ${e.message}` : String(e));
    }
  };

  const cycleSort = (col: string) => {
    setOffset(0);
    setSortKey((cur) => {
      if (!cur || cur.column !== col) return { column: col, dir: 'asc' };
      if (cur.dir === 'asc') return { column: col, dir: 'desc' };
      return null;
    });
  };

  const columns = result?.columns ?? [];
  const rows = result?.rows ?? [];
  const hasMore = result?.has_more ?? false;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}>
      {/* 工具栏 */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 10px', borderBottom: '1px solid var(--border)', flexWrap: 'wrap' }}>
        <strong style={{ fontSize: 13 }}>{qualified}</strong>
        <span style={{ fontSize: 11, color: 'var(--muted)' }}>表数据浏览（服务端分页）</span>
        <span style={{ flex: 1 }} />
        <button style={btn} onClick={() => setShowFilter((v) => !v)}>
          过滤{conds.filter((c) => condToWire(c)).length > 0 ? ` · ${conds.filter((c) => condToWire(c)).length}` : ''}
        </button>
        <button style={btn} onClick={() => void load()}>刷新</button>
        <button style={btn} onClick={() => onOpenSql(schema, table)}>在 SQL 中打开</button>
      </div>

      {/* 过滤栏 */}
      {showFilter && (
        <div style={{ padding: '6px 10px', borderBottom: '1px solid var(--border)', fontSize: 12 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
            <span style={{ color: 'var(--muted)' }}>条件组合</span>
            <select style={input} value={logic} onChange={(e) => { setOffset(0); setLogic(e.target.value as 'and' | 'or'); }}>
              <option value="and">Match all (AND)</option>
              <option value="or">Match any (OR)</option>
            </select>
            <span style={{ flex: 1 }} />
            <button style={btn} onClick={() => setConds((cs) => [...cs, { column: '', op: 'eq', value: '', second: '', values: '' }])}>+ 条件</button>
            <button style={btn} onClick={() => { setConds([]); setOffset(0); }}>清空</button>
          </div>
          {conds.map((c, i) => {
            const meta = OPS.find((o) => o.value === c.op);
            return (
              <div key={i} style={{ display: 'flex', gap: 6, alignItems: 'center', marginBottom: 4 }}>
                <select style={{ ...input, width: 140 }} value={c.column}
                  onChange={(e) => setConds((cs) => cs.map((x, j) => (j === i ? { ...x, column: e.target.value } : x)))}>
                  <option value="">列…</option>
                  {(columns.length > 0 ? columns.map((col) => col.name) : []).map((name) => (
                    <option key={name} value={name}>{name}</option>
                  ))}
                </select>
                <select style={{ ...input, width: 140 }} value={c.op}
                  onChange={(e) => setConds((cs) => cs.map((x, j) => (j === i ? { ...x, op: e.target.value as FilterOperator } : x)))}>
                  {OPS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
                </select>
                {meta?.needsValue && (
                  <input style={{ ...input, width: 160 }} placeholder="值" value={c.value}
                    onChange={(e) => setConds((cs) => cs.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))} />
                )}
                {meta?.needsSecond && (
                  <input style={{ ...input, width: 160 }} placeholder="上限" value={c.second}
                    onChange={(e) => setConds((cs) => cs.map((x, j) => (j === i ? { ...x, second: e.target.value } : x)))} />
                )}
                {meta?.needsValues && (
                  <input style={{ ...input, width: 220 }} placeholder="逗号分隔值列表" value={c.values}
                    onChange={(e) => setConds((cs) => cs.map((x, j) => (j === i ? { ...x, values: e.target.value } : x)))} />
                )}
                <button style={btn} onClick={() => setConds((cs) => cs.filter((_, j) => j !== i))}>−</button>
              </div>
            );
          })}
        </div>
      )}

      {/* 表格 */}
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto' }}>
        {error ? (
          <div style={{ padding: 16, color: 'var(--danger, #c0392b)', fontSize: 12 }}>{error}</div>
        ) : rows.length === 0 && !loading ? (
          <div style={{ padding: 16, color: 'var(--muted)', fontSize: 12 }}>无行（或条件过滤后为空）。</div>
        ) : (
          <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: 12 }}>
            <thead>
              <tr>
                {columns.map((col) => (
                  <th key={col.name} style={th} title={`${col.name} · ${col.type}`}
                    onClick={() => cycleSort(col.name)}>
                    {col.name}
                    {sortKey?.column === col.name ? (sortKey.dir === 'asc' ? ' ▲' : ' ▼') : ''}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row, ri) => (
                <tr key={ri}>
                  {row.map((cellVal, ci) => (
                    <td key={ci} style={cell} title={cellVal === null ? 'NULL' : String(cellVal)}>
                      {cellVal === null ? <span style={{ color: 'var(--muted)', fontStyle: 'italic' }}>NULL</span> : formatCell(cellVal)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* 分页栏 */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 10px', borderTop: '1px solid var(--border)', fontSize: 12, flexWrap: 'wrap' }}>
        <span className="muted">
          offset {result?.offset ?? offset}{loading ? ' · 加载中…' : ` · 本页 ${rows.length} 行`}
          {hasMore ? ' · 还有更多' : ' · 已到末尾'}
          {exactCount !== null ? ` · 共 ${exactCount} 行（精确）` : ''}
        </span>
        <span style={{ flex: 1 }} />
        <button style={btn} disabled={offset === 0 || loading} onClick={() => setOffset(Math.max(0, offset - pageSize))}>‹ 上一页</button>
        <button style={btn} disabled={!hasMore || loading} onClick={() => setOffset(offset + pageSize)}>下一页 ›</button>
        <select style={input} value={pageSize} onChange={(e) => { setOffset(0); setPageSize(Number(e.target.value)); }}>
          {PAGE_SIZES.map((n) => <option key={n} value={n}>{n} 行/页</option>)}
        </select>
        <button style={btn} onClick={() => void runCount()} disabled={loading}>精确计数</button>
      </div>
    </div>
  );
}

function formatCell(v: unknown): string {
  if (v === null || v === undefined) return '';
  if (typeof v === 'object') {
    try { return JSON.stringify(v); } catch { return String(v); }
  }
  return String(v);
}
