import { useEffect, useState, type CSSProperties } from 'react';
import { DbIcon } from './Icons';
import type { ConnectionInfo } from '../api';
import { onEditorStatus, type EditorStatus } from '../lib/statusBus';

interface Props {
  serverOk: boolean | null;
  serverInfo: string;
  conn: ConnectionInfo | null;
  statsCount: number;
}

function Seg({ k, children, title }: { k?: string; children: React.ReactNode; title?: string }) {
  return (
    <div className="seg" title={title}>
      {k && <span className="k">{k}</span>}
      {children}
    </div>
  );
}

function fmtElapsed(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return '—';
  if (ms < 1000) return `${ms} ms`;
  return `${(ms / 1000).toFixed(2)} s`;
}

const LANG_LABEL: Record<string, string> = { sql: 'SQL', redis: 'Redis' };

export default function StatusBar({ serverOk, serverInfo, conn, statsCount }: Props) {
  const [s, setS] = useState<Partial<EditorStatus>>({});
  useEffect(() => onEditorStatus(setS), []);
  const [, tick] = useState(0);
  useEffect(() => {
    if (!s.message) return;
    const t = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, [s.message]);

  const mode = conn?.kind === 'redis' ? 'KV' : conn ? 'SQL' : '—';
  const busy = s.busy === true;
  const hasEditor = s.transport !== undefined || s.language !== undefined;

  const monoStyle: CSSProperties = { display: 'flex', gap: 6, alignItems: 'baseline', fontFamily: 'monospace', fontSize: 11 };

  return (
    <div className="statusbar">
      <Seg k="server" title="后端服务状态">
        <span className={`status-dot ${serverOk === true ? 'ok' : serverOk === false ? 'err' : 'off'}`.trim()} style={{ marginRight: 0 }} />
        {serverOk === true ? (serverInfo || 'online') : serverOk === false ? 'offline' : 'checking…'}
      </Seg>

      {conn && (
        <Seg k="conn" title={`${conn.name}${conn.database ? ` · ${conn.database}` : ''}${conn.host ? ` · ${conn.host}` : ''}`}>
          <DbIcon kind={conn.kind} size={12} />
          <span style={{ fontWeight: 600 }}>{conn.name}</span>
          {conn.database && <span className="muted">· {conn.database}</span>}
        </Seg>
      )}

      <Seg k="mode">{mode}</Seg>

      <Seg k="log" title="查询日志条目数">{statsCount}</Seg>

      {hasEditor && s.language && (
        <Seg k="lang" title="编辑器语言">{LANG_LABEL[s.language] ?? s.language}</Seg>
      )}

      {hasEditor && s.transport && (
        <Seg k="transport" title="查询传输方式">
          {s.transport === 'ws' ? 'WebSocket' : 'HTTP msgpack'}
        </Seg>
      )}

      {hasEditor && s.tabCount && s.tabCount > 0 && (
        <Seg k="tabs" title="编辑器 Tab 数">
          {s.tabCount}
          {s.tabTitle && <span className="muted" style={{ marginLeft: 4 }}>· {s.tabTitle}</span>}
        </Seg>
      )}

      {hasEditor && s.cursorLine !== undefined && (
        <Seg k="cursor" title="编辑器光标位置">
          <span style={monoStyle}>
            Ln {s.cursorLine}, Col {s.cursorCol}
            {s.cursorSel ? <span className="muted"> · Sel {s.cursorSel}</span> : null}
          </span>
        </Seg>
      )}

      {hasEditor && s.paramCount !== undefined && s.paramCount > 0 && (
        <Seg k="params" title="参数化查询占位符数">{s.paramCount}</Seg>
      )}

      {hasEditor && (s.lintErrors !== undefined || s.lintWarnings !== undefined || s.lintInfos !== undefined) && (() => {
        const errs = s.lintErrors ?? 0;
        const warns = s.lintWarnings ?? 0;
        const infos = s.lintInfos ?? 0;
        const total = errs + warns + infos;
        const title = errs ? `⚠ ${errs} error` + (warns ? ` · ${warns} warn` : '') + (infos ? ` · ${infos} info` : '')
          : warns ? `${warns} warn` + (infos ? ` · ${infos} info` : '')
            : infos ? `${infos} info` : 'clean';
        const color = errs ? 'var(--danger)' : warns ? 'var(--warn, #d97706)' : infos ? 'var(--info, var(--accent))' : 'var(--ok)';
        return (
          <Seg k="lint" title={`SQL Lint: ${title}`}>
            <span style={{ color, fontFamily: 'monospace', fontWeight: 600 }}>
              {total === 0 ? '✓ clean' : `${errs}·${warns}·${infos}`}
            </span>
          </Seg>
        );
      })()}

      {hasEditor && s.rows !== undefined && (
        <Seg k="rows" title="结果集行数 / 列数">
          <span style={monoStyle}>{s.rows} × {s.columns}</span>
        </Seg>
      )}

      {hasEditor && (s.elapsed !== undefined || busy) && (
        <Seg k="time" title="上一次查询耗时">
          {busy ? <span className="muted">查询中…</span> : fmtElapsed(s.elapsed)}
        </Seg>
      )}

      {hasEditor && s.message && Date.now() - (s.messageAt ?? 0) < 5000 && (
        <Seg k="msg" title={s.message}>
          <span style={{ color: 'var(--ok)' }}>{s.message}</span>
        </Seg>
      )}

      <div className="seg right">
        <span className="muted">⚙ 设置 (Ctrl+,)</span>
      </div>
    </div>
  );
}
