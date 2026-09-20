import { useCallback, useEffect, useRef, useState, type CSSProperties } from 'react';
import type { DatabaseKind, FilterCondition, FilterOperator, SortDirection, TableRowsResult, Value } from '../api';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { ChangeQueue, buildStatements, commitInTransaction, dialectFor, valuesEqual } from '../lib/changes';

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
  kind: DatabaseKind;
  schema: string;
  table: string;
  onOpenSql: (schema: string, table: string) => void;
}

export default function TableDataView({ connId, kind, schema, table, onOpenSql }: Props) {
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
  // ─── M12 变更跟踪 ────────────────────────────────────────
  const queueRef = useRef(new ChangeQueue());
  const [rev, setRev] = useState(0); // 队列变更后的重渲染计数
  const [pkCols, setPkCols] = useState<string[]>([]);
  const [originalRows, setOriginalRows] = useState<Map<string, Record<string, Value>>>(new Map());
  const [editing, setEditing] = useState<{ rowId: string; column: string } | null>(null);
  const [draft, setDraft] = useState('');
  const [reviewOpen, setReviewOpen] = useState(false);
  const [committing, setCommitting] = useState(false);
  const [commitError, setCommitError] = useState<string | null>(null);
  const newSeq = useRef(0);

  const qualified = `${schema}.${table}`;
  const queue = queueRef.current;
  const canEdit = pkCols.length > 0;

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

  // 主键列（编辑/删除的 WHERE 依据，无 PK 表不开放编辑）
  useEffect(() => {
    let cancelled = false;
    queue.clear();
    setRev(0);
    void api.listColumns(connId, schema, table)
      .then((cols) => {
        if (!cancelled) setPkCols(cols.filter((c) => c.is_primary_key).map((c) => c.name));
      })
      .catch(() => { if (!cancelled) setPkCols([]); });
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connId, schema, table]);

  // 页面加载后记录原始行快照（编辑的旧值来源 + 删除的 WHERE 依据）
  const rowKey = useCallback(
    (row: Record<string, Value>) => pkCols.map((p) => String(row[p] ?? '∅')).join('\u0000'),
    [pkCols],
  );
  useEffect(() => {
    if (!result || pkCols.length === 0) return;
    const colIdx = new Map(result.columns.map((c, i) => [c.name, i]));
    const m = new Map<string, Record<string, Value>>();
    for (const row of result.rows) {
      const rowMap: Record<string, Value> = {};
      for (const c of result.columns) rowMap[c.name] = row[colIdx.get(c.name) ?? 0] ?? null;
      m.set(rowKey(rowMap), rowMap);
    }
    setOriginalRows(m);
  }, [result, pkCols, rowKey]);

  // 有未保存修改时，翻页/刷新/改排序会丢弃队列——先确认（TablePro "Discard Unsaved Changes?"）。
  const guardDiscard = (action: string): boolean => {
    if (queue.size === 0) return true;
    if (!window.confirm(`有 ${queue.size} 条未保存修改，${action}将丢弃它们。继续？`)) return false;
    queue.clear();
    setRev(0);
    setEditing(null);
    return true;
  };

  const loadAndDiscard = () => {
    if (guardDiscard('重新加载')) void load();
  };

  // ─── M12 编辑操作 ────────────────────────────────────────
  const parseEdit = (s: string): Value => {
    const t = s.trim();
    if (t === '' || t === 'null' || t === 'NULL') return null;
    if (t === 'true') return true;
    if (t === 'false') return false;
    if (/^-?\d+$/.test(t)) return Number(t);
    if (/^-?\d*\.\d+$/.test(t)) return Number(t);
    return s;
  };

  const addNewRow = () => {
    if (!canEdit || !result) return;
    const rowId = `__new_${++newSeq.current}`;
    const empty: Record<string, Value> = {};
    for (const c of result.columns) empty[c.name] = null;
    queue.addInsert(rowId, empty);
    setRev((r) => r + 1);
  };

  const deleteRow = (rowId: string) => {
    const orig = originalRows.get(rowId);
    if (!orig) return;
    queue.addDelete(rowId, orig);
    setRev((r) => r + 1);
  };

  const displayValue = (rowId: string, colName: string, base: Value): Value => {
    const ins = queue.insertOf(rowId);
    if (ins) return ins.newRow?.[colName] ?? null;
    const change = queue.get(rowId);
    if (change?.type === 'update') {
      const cc = change.cellChanges.find((c) => c.column === colName);
      if (cc) return cc.newValue;
    }
    return base;
  };

  const startEdit = (rowId: string, colName: string, current: Value) => {
    setEditing({ rowId, column: colName });
    setDraft(current === null || current === undefined ? '' : String(current));
  };

  const commitEdit = () => {
    if (!editing) return;
    const { rowId, column } = editing;
    const newVal = parseEdit(draft);
    let orig = originalRows.get(rowId);
    const insert = queue.insertOf(rowId);
    if (insert) orig = { ...(insert.newRow ?? {}) };
    if (!orig) {
      setEditing(null);
      return;
    }
    const current = displayValue(rowId, column, orig[column] ?? null);
    queue.addCellChange(rowId, column, current, newVal, orig);
    setEditing(null);
    setRev((r) => r + 1);
  };

  const statements = () => {
    if (!canEdit) return [];
    return buildStatements(queue.all, schema, table, pkCols, dialectFor(kind));
  };

  const doCommit = async () => {
    if (queue.size === 0) return;
    setCommitting(true);
    setCommitError(null);
    try {
      const out = await commitInTransaction(connId, statements());
      if (out.ok) {
        queue.clear();
        setRev((r) => r + 1);
        setCommitError(null);
        setReviewOpen(false);
        void load(); // 重新取原始快照 + 数据
      } else {
        setCommitError(out.error ?? `提交失败（已执行 ${out.applied} 条后中止）`);
      }
    } catch (e) {
      setCommitError(e instanceof ApiError ? `${e.code}: ${e.message}` : String(e));
    } finally {
      setCommitting(false);
    }
  };

  const doRevert = () => {
    if (queue.size === 0) return;
    if (!window.confirm(`丢弃全部 ${queue.size} 条未保存修改？`)) return;
    queue.clear();
    setRev((r) => r + 1);
  };

  // Ctrl/Cmd+Z 撤销最近一次编辑（输入框聚焦时不拦截）
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const typing = ['INPUT', 'TEXTAREA', 'SELECT'].includes(document.activeElement?.tagName ?? '');
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'z' && !typing) {
        e.preventDefault();
        if (queue.undo()) setRev((r) => r + 1);
      }
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [queue]);

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
        <span style={{ fontSize: 11, color: 'var(--muted)' }}>表数据浏览 · 双击单元格编辑</span>
        {canEdit && queue.size > 0 && (
          <span style={{ fontSize: 11, color: 'var(--accent, #2d68c8)' }} data-rev={rev}>
            ● {queue.size} 条待保存
          </span>
        )}
        <span style={{ flex: 1 }} />
        {canEdit && (
          <button style={btn} onClick={addNewRow} disabled={!result}>+ 新行</button>
        )}
        <button style={btn} onClick={() => setShowFilter((v) => !v)}>
          过滤{conds.filter((c) => condToWire(c)).length > 0 ? ` · ${conds.filter((c) => condToWire(c)).length}` : ''}
        </button>
        <button style={btn} onClick={loadAndDiscard}>刷新</button>
        <button style={btn} onClick={() => onOpenSql(schema, table)}>在 SQL 中打开</button>
      </div>

      {/* M12 变更跟踪工具条（有未保存修改时出现） */}
      {canEdit && queue.size > 0 && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '5px 10px', borderBottom: '1px solid var(--border)', background: 'rgba(255, 213, 79, 0.08)', flexWrap: 'wrap', fontSize: 12 }}>
          <span>未保存修改（Ctrl+Z 撤销）</span>
          {commitError && <span style={{ color: 'var(--danger, #c0392b)' }}>{commitError}</span>}
          <span style={{ flex: 1 }} />
          <button style={btn} onClick={() => setReviewOpen(true)}>Review SQL</button>
          <button style={btn} onClick={doRevert}>还原</button>
          <button
            style={{ ...btn, background: 'var(--accent, #2d68c8)', color: '#fff', border: '1px solid var(--accent, #2d68c8)' }}
            onClick={() => void doCommit()}
            disabled={committing}
          >
            {committing ? '提交中…' : `提交 ${queue.size} 条`}
          </button>
        </div>
      )}

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
            <button style={btn} onClick={() => { if (guardDiscard('清空过滤')) { setConds([]); setOffset(0); } }}>清空</button>
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
        ) : rows.length === 0 && !loading && queue.size === 0 ? (
          <div style={{ padding: 16, color: 'var(--muted)', fontSize: 12 }}>无行（或条件过滤后为空）。</div>
        ) : (
          <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: 12 }}>
            <thead>
              <tr>
                {canEdit && <th style={{ ...th, width: 44, cursor: 'default' }} title="行操作">⋯</th>}
                {columns.map((col) => (
                  <th key={col.name} style={th} title={`${col.name} · ${col.type}${pkCols.includes(col.name) ? ' · PK' : ''}`}
                    onClick={() => { if (guardDiscard('重新排序')) cycleSort(col.name); }}>
                    {col.name}{pkCols.includes(col.name) ? ' 🔑' : ''}
                    {sortKey?.column === col.name ? (sortKey.dir === 'asc' ? ' ▲' : ' ▼') : ''}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row, ri) => {
                const colIdx = result?.columns.map((c) => c.name) ?? [];
                const rowMap: Record<string, Value> = {};
                colIdx.forEach((n, i) => { rowMap[n] = row[i] ?? null; });
                const rowId = rowKey(rowMap);
                const change = queue.get(rowId);
                const deleted = change?.type === 'delete';
                return (
                  <tr key={ri} style={deleted ? { opacity: 0.55, textDecoration: 'line-through' } : undefined}>
                    {canEdit && (
                      <td style={{ ...cell, textAlign: 'center' }}>
                        {deleted ? (
                          <button style={{ ...btn, padding: '0 6px' }} title="撤销删除" onClick={() => { queue.undo(); setRev((r) => r + 1); }}>↩</button>
                        ) : (
                          <button style={{ ...btn, padding: '0 6px' }} title="删除行（加入待保存队列）" onClick={() => deleteRow(rowId)}>✕</button>
                        )}
                      </td>
                    )}
                    {colIdx.map((colName, ci) => {
                      const base = row[ci] ?? null;
                      const val = displayValue(rowId, colName, base);
                      const isPk = pkCols.includes(colName);
                      const changed = change?.type === 'update' && change.cellChanges.some((c) => c.column === colName && !valuesEqual(c.newValue, base));
                      const isEditing = editing !== null && editing.rowId === rowId && editing.column === colName;
                      return (
                        <td key={ci} style={{ ...cell, background: changed ? 'rgba(255, 213, 79, 0.18)' : undefined }}
                          title={val === null ? 'NULL' : String(val)}
                          onDoubleClick={canEdit && !deleted ? () => startEdit(rowId, colName, val) : undefined}>
                          {isEditing ? (
                            <input
                              autoFocus
                              style={{ width: '100%', border: '1px solid var(--accent, #2d68c8)', background: 'var(--bg)', color: 'var(--fg)', fontSize: 12, padding: '2px 4px' }}
                              value={draft}
                              onChange={(e) => setDraft(e.target.value)}
                              onKeyDown={(e) => {
                                if (e.key === 'Enter') commitEdit();
                                if (e.key === 'Escape') setEditing(null);
                              }}
                              onBlur={commitEdit}
                            />
                          ) : val === null ? (
                            <span style={{ color: 'var(--muted)', fontStyle: 'italic' }}>
                              {isPk ? 'DEFAULT' : 'NULL'}
                            </span>
                          ) : (
                            formatCell(val)
                          )}
                        </td>
                      );
                    })}
                  </tr>
                );
              })}
              {canEdit && result ? (
                [...(queue.all.filter((c) => c.type === 'insert'))].map((c) => {
                  const vals: Record<string, Value> = { ...(c.newRow ?? {}) };
                  return (
                    <tr key={c.rowId} style={{ background: 'rgba(76, 215, 115, 0.10)' }}>
                      {canEdit && (
                        <td style={{ ...cell, textAlign: 'center' }}>
                          <button style={{ ...btn, padding: '0 6px' }} title="移除新行" onClick={() => { queue.undo(); setRev((r) => r + 1); }}>↩</button>
                        </td>
                      )}
                      {columns.map((col) => {
                        const isEditing = editing !== null && editing.rowId === c.rowId && editing.column === col.name;
                        const isPk = pkCols.includes(col.name);
                        return (
                          <td key={col.name} style={cell}
                            onDoubleClick={isEditing ? undefined : () => startEdit(c.rowId, col.name, vals[col.name] ?? null)}>
                            {isEditing ? (
                              <input
                                autoFocus
                                style={{ width: '100%', border: '1px solid var(--accent, #2d68c8)', background: 'var(--bg)', color: 'var(--fg)', fontSize: 12, padding: '2px 4px' }}
                                value={draft}
                                onChange={(e) => setDraft(e.target.value)}
                                onKeyDown={(e) => {
                                  if (e.key === 'Enter') commitEdit();
                                  if (e.key === 'Escape') setEditing(null);
                                }}
                                onBlur={commitEdit}
                              />
                            ) : vals[col.name] === null || vals[col.name] === undefined ? (
                              <span style={{ color: 'var(--muted)', fontStyle: 'italic' }}>{isPk ? 'DEFAULT' : ''}</span>
                            ) : (
                              formatCell(vals[col.name])
                            )}
                          </td>
                        );
                      })}
                    </tr>
                  );
                })
              ) : null}
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
        <button style={btn} disabled={offset === 0 || loading}
          onClick={() => { if (guardDiscard('翻页')) setOffset(Math.max(0, offset - pageSize)); }}>
          ‹ 上一页
        </button>
        <button style={btn} disabled={!hasMore || loading}
          onClick={() => { if (guardDiscard('翻页')) setOffset(offset + pageSize); }}>
          下一页 ›
        </button>
        <select style={input} value={pageSize}
          onChange={(e) => { if (guardDiscard('换页大小')) { setOffset(0); setPageSize(Number(e.target.value)); } }}>
          {PAGE_SIZES.map((n) => <option key={n} value={n}>{n} 行/页</option>)}
        </select>
        <button style={btn} onClick={() => void runCount()} disabled={loading}>精确计数</button>
      </div>

      {/* M12 Review SQL 面板 */}
      {reviewOpen && canEdit && (
        <div
          style={{ position: 'fixed', inset: 0, zIndex: 940, background: 'rgba(0,0,0,0.4)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}
          onMouseDown={(e) => { if (e.target === e.currentTarget) setReviewOpen(false); }}
        >
          <div style={{ width: '100%', maxWidth: 720, maxHeight: '70vh', background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 6, display: 'flex', flexDirection: 'column' }}>
            <div style={{ padding: '10px 14px', borderBottom: '1px solid var(--border)', fontSize: 13, fontWeight: 600 }}>
              即将执行的语句（{queue.size} 条，将提交到一个事务）
            </div>
            <div style={{ flex: 1, overflow: 'auto', padding: '8px 14px', fontFamily: 'monospace', fontSize: 12 }}>
              {statements().map((s, i) => (
                <div key={i} style={{ padding: '6px 0', borderBottom: '1px dashed var(--border)' }}>
                  <span style={{ color: 'var(--muted)' }}>{i + 1}. </span>
                  {s.sql}
                </div>
              ))}
            </div>
            <div style={{ display: 'flex', gap: 8, padding: '10px 14px', borderTop: '1px solid var(--border)' }}>
              <button style={btn} onClick={() => { void navigator.clipboard?.writeText(statements().map((s) => s.sql).join(';\n')); }}>Copy All</button>
              <span style={{ flex: 1 }} />
              <button style={btn} onClick={() => void doCommit()} disabled={committing}>
                {committing ? '提交中…' : `提交全部`}
              </button>
              <button style={{ ...btn, background: 'var(--accent, #2d68c8)', color: '#fff', border: '1px solid var(--accent, #2d68c8)' }}
                onClick={() => { setReviewOpen(false); void doCommit(); }}>
                关闭并提交
              </button>
            </div>
          </div>
        </div>
      )}
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
