import { useCallback, useEffect, useState, type CSSProperties } from 'react';
import type { ColumnInfo, DatabaseKind } from '../api';
import * as api from '../lib/api';
import { dialectFor } from '../lib/changes';
import { buildProfilePlan, parseProfile, type ProfileResult } from '../lib/tableProfile';

// U6.1 表概览 rail（参考 TablePro Inspector 的信息卡组织，仅交互设计，不移植代码）。

const MAX_PROFILE_COLS = 24;

interface Props {
  connId: string;
  kind: DatabaseKind;
  schema: string;
  table: string;
  columns: ColumnInfo[];
  indexCount: number;
  fkOutCount: number;
  pkCols: string[];
}

const card: CSSProperties = {
  border: '1px solid var(--border)', borderRadius: 6, padding: '8px 12px',
  background: 'var(--panel)', minWidth: 96,
};
const cardNum: CSSProperties = { fontSize: 18, fontWeight: 700 };
const cardLabel: CSSProperties = { fontSize: 11, color: 'var(--muted)' };

function fmt(v: unknown): string {
  if (v === null || v === undefined) return '—';
  if (typeof v === 'number') return Number.isInteger(v) ? String(v) : v.toFixed(2);
  const s = String(v);
  return s.length > 24 ? `${s.slice(0, 24)}…` : s;
}

export default function TableOverview({ connId, kind, schema, table, columns, indexCount, fkOutCount, pkCols }: Props) {
  const [profile, setProfile] = useState<ProfileResult | null>(null);
  const [profiling, setProfiling] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [seq, setSeq] = useState(0);

  const d = dialectFor(kind);
  const tableExpr = `${d.quoteIdent(schema)}.${d.quoteIdent(table)}`;

  const run = useCallback(async () => {
    const plan = buildProfilePlan(d.quoteIdent.bind(d), tableExpr, columns.slice(0, MAX_PROFILE_COLS).map((c) => ({ name: c.name, data_type: c.data_type })));
    setProfiling(true);
    setError(null);
    try {
      const res = await api.executeQuery(connId, { sql: plan.sql });
      setProfile(parseProfile(res.rows[0], plan));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setProfile(null);
    } finally {
      setProfiling(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connId, tableExpr, columns]);

  useEffect(() => {
    if (columns.length > 0) void run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [seq, connId, schema, table, columns.length]);

  const profiled = columns.slice(0, MAX_PROFILE_COLS);

  return (
    <div style={{ padding: 10, display: 'flex', flexDirection: 'column', gap: 10 }}>
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'stretch' }}>
        <div style={card}>
          <div style={cardNum}>{profiling ? '…' : fmt(profile?.rowCount ?? null)}</div>
          <div style={cardLabel}>行数</div>
        </div>
        <div style={card}>
          <div style={cardNum}>{columns.length}</div>
          <div style={cardLabel}>列数</div>
        </div>
        <div style={card}>
          <div style={cardNum}>{indexCount}</div>
          <div style={cardLabel}>索引</div>
        </div>
        <div style={card}>
          <div style={cardNum}>{fkOutCount}</div>
          <div style={cardLabel}>出向外键</div>
        </div>
        <div style={card}>
          <div style={cardNum} className="mono" title={pkCols.join(', ')}>{pkCols.length ? pkCols.join(', ') : '—'}</div>
          <div style={cardLabel}>主键</div>
        </div>
        <span style={{ flex: 1 }} />
        <button style={{ alignSelf: 'center' }} onClick={() => setSeq((n) => n + 1)} disabled={profiling}>
          {profiling ? '统计中…' : '重新统计'}
        </button>
      </div>
      {error && <div className="error-box">列画像失败：{error}</div>}
      {columns.length > MAX_PROFILE_COLS && (
        <div className="muted" style={{ fontSize: 11 }}>列数较多，仅画像前 {MAX_PROFILE_COLS} 列。</div>
      )}
      <div>
        <div className="group">列画像</div>
        <table className="detail-table">
          <thead>
            <tr>
              <th>列</th><th>类型</th><th title="NULL 值数">NULL</th><th title="非空值占比">完整度</th>
              <th title="去重值数">DISTINCT</th><th>MIN</th><th>MAX</th><th>AVG</th>
            </tr>
          </thead>
          <tbody>
            {profiled.map((c) => {
              const p = profile?.byColumn.get(c.name);
              const total = profile?.rowCount ?? null;
              const completeness = p && total != null && total > 0
                ? Math.max(0, Math.min(100, Math.round(((total - (p.nulls ?? 0)) / total) * 100)))
                : null;
              return (
                <tr key={c.name}>
                  <td className="mono">{c.name}{pkCols.includes(c.name) ? ' 🔑' : ''}</td>
                  <td className="mono muted">{c.data_type}</td>
                  <td>{fmt(p?.nulls)}</td>
                  <td>{completeness == null ? '—' : `${completeness}%`}</td>
                  <td>{fmt(p?.distinct)}</td>
                  <td className="mono">{fmt(p?.min)}</td>
                  <td className="mono">{fmt(p?.max)}</td>
                  <td className="mono">{fmt(p?.avg)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {!profiling && !profile && !error && <div className="empty">暂无画像数据。</div>}
      </div>
    </div>
  );
}
