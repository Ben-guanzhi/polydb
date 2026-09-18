import { useCallback, useMemo, useState } from 'react';
import type { DatabaseKind } from '../api';
import type { RunStat } from '../lib/runStats';
import { hashSql, summarize } from '../lib/runStats';
import { DbIcon } from './Icons';

interface Props {
  stats: RunStat[];
  connections: { id: string; name: string; kind: DatabaseKind }[];
  onPick: (connId: string, sql: string) => void;
  onClear: () => void;
}

type SortKey = 'ts' | 'elapsed_ms' | 'server_ms' | 'rows';
type ViewMode = 'detail' | 'sql' | 'conn';

interface SqlAggRow {
  hash: string;
  sql: string;
  count: number;
  min: number;
  p50: number;
  p95: number;
  max: number;
  avg: number;
  lastTs: number;
  kinds: DatabaseKind[];
  connIds: string[];
}

interface ConnAggRow {
  connId: string;
  name: string;
  kind: DatabaseKind;
  count: number;
  totalMs: number;
  avgMs: number;
  p95Ms: number;
  maxMs: number;
  totalRows: number;
  lastTs: number;
}

export default function QueryLogPanel({ stats, connections, onPick, onClear }: Props) {
  const [view, setView] = useState<ViewMode>('detail');
  const [sqlQ, setSqlQ] = useState('');
  const [kindFilter, setKindFilter] = useState<DatabaseKind | 'all'>('all');
  const [connFilter, setConnFilter] = useState<string>('all');
  const [minMs, setMinMs] = useState('');
  const [maxMs, setMaxMs] = useState('');
  const [sortKey, setSortKey] = useState<SortKey>('ts');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc');
  const [limit] = useState(100);

  const connName = useCallback((id: string) => connections.find((c) => c.id === id)?.name ?? id.slice(0, 6) + '…', [connections]);
  const connKind = useCallback((id: string) => connections.find((c) => c.id === id)?.kind ?? 'sqlite', [connections]);

  const baseFiltered = useMemo(() => {
    let out = stats.slice();
    if (kindFilter !== 'all') out = out.filter((s) => s.kind === kindFilter);
    if (connFilter !== 'all') out = out.filter((s) => s.connId === connFilter);
    if (sqlQ.trim()) {
      const q = sqlQ.trim().toLowerCase();
      out = out.filter((s) => s.sql.toLowerCase().includes(q));
    }
    if (minMs !== '') {
      const min = Number(minMs);
      if (!Number.isNaN(min)) out = out.filter((s) => s.elapsed_ms >= min);
    }
    if (maxMs !== '') {
      const max = Number(maxMs);
      if (!Number.isNaN(max)) out = out.filter((s) => s.elapsed_ms <= max);
    }
    return out;
  }, [stats, kindFilter, connFilter, sqlQ, minMs, maxMs]);

  const detail = useMemo(() => {
    const out = baseFiltered.slice();
    out.sort((a, b) => {
      const dir = sortDir === 'asc' ? 1 : -1;
      if (sortKey === 'ts') return (a.ts - b.ts) * dir;
      return (a[sortKey] - b[sortKey]) * dir;
    });
    return out.slice(0, limit);
  }, [baseFiltered, sortKey, sortDir, limit]);

  const sqlAgg = useMemo<SqlAggRow[]>(() => {
    const byHash = new Map<string, RunStat[]>();
    for (const s of baseFiltered) {
      const h = hashSql(s.sql);
      const arr = byHash.get(h);
      if (arr) arr.push(s);
      else byHash.set(h, [s]);
    }
    const rows: SqlAggRow[] = [];
    for (const [hash, arr] of byHash) {
      const kinds = [...new Set(arr.map((s) => s.kind))];
      const connIds = [...new Set(arr.map((s) => s.connId))];
      const first = arr.sort((a, b) => b.ts - a.ts)[0];
      const summary = summarize(arr, first.sql)!;
      rows.push({
        hash,
        sql: first.sql,
        count: summary.count,
        min: summary.min,
        p50: summary.p50,
        p95: summary.p95,
        max: summary.max,
        avg: summary.avg,
        lastTs: summary.lastTs,
        kinds,
        connIds,
      });
    }
    rows.sort((a, b) => b.p95 - a.p95);
    return rows.slice(0, limit);
  }, [baseFiltered, limit]);

  const connAgg = useMemo<ConnAggRow[]>(() => {
    const byConn = new Map<string, RunStat[]>();
    for (const s of baseFiltered) {
      const arr = byConn.get(s.connId);
      if (arr) arr.push(s);
      else byConn.set(s.connId, [s]);
    }
    const rows: ConnAggRow[] = [];
    for (const [connId, arr] of byConn) {
      const msArr = arr.map((s) => s.elapsed_ms).sort((a, b) => a - b);
      const totalMs = msArr.reduce((a, b) => a + b, 0);
      const p95 = msArr[Math.min(msArr.length - 1, Math.floor(0.95 * msArr.length))];
      const lastTs = arr.reduce((m, s) => Math.max(m, s.ts), 0);
      rows.push({
        connId,
        name: connName(connId),
        kind: connKind(connId),
        count: arr.length,
        totalMs,
        avgMs: totalMs / arr.length,
        p95Ms: p95,
        maxMs: msArr[msArr.length - 1],
        totalRows: arr.reduce((a, s) => a + s.rows, 0),
        lastTs,
      });
    }
    rows.sort((a, b) => b.count - a.count);
    return rows;
  }, [baseFiltered, connName, connKind]);

  const visibleList = view === 'detail' ? detail : view === 'sql' ? sqlAgg : connAgg;

  const kinds = useMemo(() => {
    const set = new Set<DatabaseKind>();
    for (const s of stats) set.add(s.kind);
    return [...set];
  }, [stats]);

  const fmtMs = (v: number) => (v < 1 ? v.toFixed(3) : v < 100 ? v.toFixed(2) : v < 10000 ? v.toFixed(0) : (v / 1000).toFixed(2) + 's');
  const fmtRows = (v: number) => (v >= 10000 ? (v / 1000).toFixed(1) + 'k' : String(v));
  const fmtTime = (ts: number) => {
    const d = new Date(ts);
    const today = new Date();
    const sameDay = d.toDateString() === today.toDateString();
    if (sameDay) return d.toLocaleTimeString();
    return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' });
  };

  const exportCsv = () => {
    const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    if (view === 'detail') {
      const cols = ['ts', 'kind', 'conn', 'elapsed_ms', 'server_ms', 'rows', 'truncated', 'sql'];
      const lines = [cols.join(',')];
      for (const s of detail) {
        lines.push([
          new Date(s.ts).toISOString(),
          s.kind,
          connName(s.connId),
          s.elapsed_ms.toFixed(3),
          s.server_ms.toFixed(3),
          s.rows,
          s.truncated ? '1' : '0',
          `"${s.sql.replace(/"/g, '""')}"`,
        ].join(','));
      }
      downloadCsv(`polydb-querylog-${stamp}.csv`, lines);
    } else if (view === 'sql') {
      const cols = ['hash', 'count', 'min', 'avg', 'p50', 'p95', 'max', 'last_ts', 'kinds', 'conns', 'sql'];
      const lines = [cols.join(',')];
      for (const r of sqlAgg) {
        lines.push([
          r.hash,
          r.count,
          r.min.toFixed(3),
          r.avg.toFixed(3),
          r.p50.toFixed(3),
          r.p95.toFixed(3),
          r.max.toFixed(3),
          new Date(r.lastTs).toISOString(),
          r.kinds.join('|'),
          r.connIds.map(connName).join('|'),
          `"${r.sql.replace(/"/g, '""')}"`,
        ].join(','));
      }
      downloadCsv(`polydb-querylog-sqlagg-${stamp}.csv`, lines);
    } else {
      const cols = ['conn', 'kind', 'count', 'total_ms', 'avg_ms', 'p95_ms', 'max_ms', 'total_rows', 'last_ts'];
      const lines = [cols.join(',')];
      for (const r of connAgg) {
        lines.push([
          connName(r.connId),
          r.kind,
          r.count,
          r.totalMs.toFixed(3),
          r.avgMs.toFixed(3),
          r.p95Ms.toFixed(3),
          r.maxMs.toFixed(3),
          r.totalRows,
          new Date(r.lastTs).toISOString(),
        ].join(','));
      }
      downloadCsv(`polydb-querylog-connagg-${stamp}.csv`, lines);
    }
  };

  const toggleSort = (k: SortKey) => {
    if (sortKey === k) setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'));
    else { setSortKey(k); setSortDir('desc'); }
  };
  const arrow = (k: SortKey) => (sortKey === k ? (sortDir === 'asc' ? '↑' : '↓') : '↕');

  const viewBtn = (mode: ViewMode, label: string, count: number) => (
    <button
      key={mode}
      className="btn-ico"
      onClick={() => setView(mode)}
      title={view === mode ? '当前视图' : `切换到${label}`}
      style={{
        padding: '2px 8px',
        background: view === mode ? 'var(--accent-bg, rgba(255,255,255,0.06))' : 'transparent',
        color: view === mode ? 'var(--fg)' : 'var(--muted)',
        border: '1px solid var(--border)',
        borderRadius: 2,
        fontSize: 12,
        fontWeight: view === mode ? 600 : 400,
      }}
    >
      {label} <span style={{ opacity: 0.7 }}>({count})</span>
    </button>
  );

  const totalElapsed = baseFiltered.reduce((a, s) => a + s.elapsed_ms, 0);
  const totalRows = baseFiltered.reduce((a, s) => a + s.rows, 0);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'center', padding: '6px 10px', borderBottom: '1px solid var(--border)', background: 'var(--bg-alt, transparent)', fontSize: 12 }}>
        <strong style={{ fontSize: 12 }}>查询日志</strong>
        <span style={{ color: 'var(--muted)', fontSize: 11 }}>
          {baseFiltered.length}/{stats.length} 条 · 总 {fmtMs(totalElapsed)}ms · {fmtRows(totalRows)} 行
        </span>
        <div style={{ display: 'flex', gap: 4 }}>
          {viewBtn('detail', '明细', detail.length)}
          {viewBtn('sql', 'SQL 聚合', sqlAgg.length)}
          {viewBtn('conn', '连接聚合', connAgg.length)}
        </div>
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'center', padding: '6px 10px', borderBottom: '1px solid var(--border)', fontSize: 12 }}>
        <input
          type="text"
          value={sqlQ}
          onChange={(e) => setSqlQ(e.target.value)}
          placeholder="搜索 SQL…"
          style={{ flex: 1, minWidth: 120, padding: '2px 6px', fontSize: 12, fontFamily: 'inherit', background: 'var(--bg)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 2, outline: 'none' }}
        />
        <select
          value={kindFilter}
          onChange={(e) => setKindFilter(e.target.value as DatabaseKind | 'all')}
          style={{ padding: '2px 4px', fontSize: 12, background: 'var(--bg)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 2 }}
        >
          <option value="all">所有 DB</option>
          {kinds.map((k) => <option key={k} value={k}>{k}</option>)}
        </select>
        <select
          value={connFilter}
          onChange={(e) => setConnFilter(e.target.value)}
          style={{ padding: '2px 4px', fontSize: 12, background: 'var(--bg)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 2 }}
        >
          <option value="all">所有连接</option>
          {connections.map((c) => <option key={c.id} value={c.id}>{c.name} · {c.kind}</option>)}
        </select>
        <span style={{ fontSize: 11, color: 'var(--muted)' }}>耗时 ≥</span>
        <input
          type="number"
          value={minMs}
          onChange={(e) => setMinMs(e.target.value)}
          placeholder="ms"
          style={{ width: 60, padding: '2px 4px', fontSize: 12, background: 'var(--bg)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 2 }}
        />
        <span style={{ fontSize: 11, color: 'var(--muted)' }}>≤</span>
        <input
          type="number"
          value={maxMs}
          onChange={(e) => setMaxMs(e.target.value)}
          placeholder="ms"
          style={{ width: 60, padding: '2px 4px', fontSize: 12, background: 'var(--bg)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 2 }}
        />
        <button className="btn-ico" onClick={exportCsv} disabled={visibleList.length === 0} title="导出当前视图 CSV">导出 CSV</button>
        <button className="btn-ico" onClick={onClear} title="清空全部查询日志">清空</button>
      </div>
      <div style={{ flex: 1, overflow: 'auto', minHeight: 0 }}>
        {stats.length === 0 ? (
          <div style={{ padding: 20, textAlign: 'center', color: 'var(--muted)', fontSize: 12 }}>
            尚无查询记录。执行 SQL 后会追加到这里。
          </div>
        ) : baseFiltered.length === 0 ? (
          <div style={{ padding: 20, textAlign: 'center', color: 'var(--muted)', fontSize: 12 }}>
            过滤条件下无记录。
          </div>
        ) : view === 'detail' ? (
          <table className="grid" style={{ width: '100%' }}>
            <thead>
              <tr>
                <th style={{ width: 90 }}>时间</th>
                <th style={{ width: 70 }}>DB</th>
                <th style={{ width: 120 }}>连接</th>
                <th style={{ width: 70, cursor: 'pointer' }} onClick={() => toggleSort('elapsed_ms')}>客户端 {arrow('elapsed_ms')}</th>
                <th style={{ width: 70, cursor: 'pointer' }} onClick={() => toggleSort('server_ms')}>服务端 {arrow('server_ms')}</th>
                <th style={{ width: 60, cursor: 'pointer' }} onClick={() => toggleSort('rows')}>行数 {arrow('rows')}</th>
                <th>SQL</th>
              </tr>
            </thead>
            <tbody>
              {detail.map((s, i) => {
                const hash = hashSql(s.sql);
                return (
                  <tr
                    key={i}
                    onClick={() => onPick(s.connId, s.sql)}
                    title="点击跳回该连接并载入 SQL"
                    style={{ cursor: 'pointer', color: s.truncated ? 'var(--danger)' : undefined }}
                  >
                    <td style={{ fontSize: 11, color: 'var(--muted)', whiteSpace: 'nowrap' }}>{fmtTime(s.ts)}</td>
                    <td style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 11 }}>
                      <DbIcon kind={s.kind} size={12} />
                      <span>{s.kind}</span>
                    </td>
                    <td style={{ fontSize: 11, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={connName(s.connId)}>
                      {connName(s.connId)}
                    </td>
                    <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)' }}>{fmtMs(s.elapsed_ms)}</td>
                    <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)', color: 'var(--muted)' }}>{fmtMs(s.server_ms)}</td>
                    <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)' }}>
                      {s.rows}{s.truncated ? '+' : ''}
                    </td>
                    <td style={{ fontSize: 11, fontFamily: 'var(--mono)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', maxWidth: 0 }} title={`${s.sql}\nhash: ${hash}`}>
                      {s.sql.replace(/\s+/g, ' ').slice(0, 120)}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        ) : view === 'sql' ? (
          <table className="grid" style={{ width: '100%' }}>
            <thead>
              <tr>
                <th style={{ width: 40 }}>次数</th>
                <th style={{ width: 55 }}>最小</th>
                <th style={{ width: 55 }}>平均</th>
                <th style={{ width: 55 }}>P50</th>
                <th style={{ width: 55 }}>P95</th>
                <th style={{ width: 55 }}>最大</th>
                <th style={{ width: 70 }}>最近</th>
                <th>SQL</th>
              </tr>
            </thead>
            <tbody>
              {sqlAgg.map((r, i) => (
                <tr
                  key={i}
                  onClick={() => onPick(r.connIds[0] ?? '', r.sql)}
                  title="点击跳回该连接并载入 SQL"
                  style={{ cursor: 'pointer' }}
                >
                  <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)' }}>{r.count}</td>
                  <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)', color: 'var(--muted)' }}>{fmtMs(r.min)}</td>
                  <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)' }}>{fmtMs(r.avg)}</td>
                  <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)' }}>{fmtMs(r.p50)}</td>
                  <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)', color: r.p95 >= r.max * 0.95 ? 'var(--warning, #faad14)' : undefined }}>{fmtMs(r.p95)}</td>
                  <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)' }}>{fmtMs(r.max)}</td>
                  <td style={{ fontSize: 11, color: 'var(--muted)', whiteSpace: 'nowrap' }}>{fmtTime(r.lastTs)}</td>
                  <td style={{ fontSize: 11, fontFamily: 'var(--mono)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', maxWidth: 0 }} title={r.sql}>
                    {r.sql.replace(/\s+/g, ' ').slice(0, 120)}
                    {r.kinds.length > 1 ? <span style={{ marginLeft: 6, color: 'var(--muted)', fontFamily: 'inherit' }}>[{r.kinds.join(',')}]</span> : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <table className="grid" style={{ width: '100%' }}>
            <thead>
              <tr>
                <th>连接</th>
                <th style={{ width: 60 }}>DB</th>
                <th style={{ width: 50 }}>次数</th>
                <th style={{ width: 70 }}>总耗时</th>
                <th style={{ width: 60 }}>平均</th>
                <th style={{ width: 60 }}>P95</th>
                <th style={{ width: 60 }}>最大</th>
                <th style={{ width: 70 }}>总行数</th>
                <th style={{ width: 80 }}>最近</th>
              </tr>
            </thead>
            <tbody>
              {connAgg.map((r, i) => (
                <tr key={i} style={{ cursor: 'default' }}>
                  <td style={{ fontSize: 11, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={r.name}>
                    {r.name}
                  </td>
                  <td style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 11 }}>
                    <DbIcon kind={r.kind} size={12} />
                    <span>{r.kind}</span>
                  </td>
                  <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)' }}>{r.count}</td>
                  <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)' }}>{fmtMs(r.totalMs)}</td>
                  <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)' }}>{fmtMs(r.avgMs)}</td>
                  <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)' }}>{fmtMs(r.p95Ms)}</td>
                  <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)' }}>{fmtMs(r.maxMs)}</td>
                  <td style={{ fontSize: 11, textAlign: 'right', fontFamily: 'var(--mono)' }}>{fmtRows(r.totalRows)}</td>
                  <td style={{ fontSize: 11, color: 'var(--muted)', whiteSpace: 'nowrap' }}>{fmtTime(r.lastTs)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function downloadCsv(name: string, lines: string[]) {
  const blob = new Blob([lines.join('\n')], { type: 'text/csv;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
