import type { CSSProperties } from 'react';
import { ApiError } from '../lib/api';
import type { PresetAuditStatus } from '../lib/importPresets';
import type { Value } from '../api';

export type Step = 'input' | 'table' | 'map' | 'preview' | 'done';

export const STEP_LABEL: Record<Step, string> = {
  input: '1 · 粘贴或选择数据',
  table: '2 · 目标表 & 模式',
  map: '3 · 列映射 & 转换',
  preview: '4 · 预览 & 执行',
  done: '5 · 完成',
};
export const STEP_ORDER: Step[] = ['input', 'table', 'map', 'preview', 'done'];

// M30.51 向导头部步骤徽章：可点击跳步（回跳恒可；前进按各步前置守卫），done 恒禁
export function StepBar({ step, curStepIdx, stepWarnFlags, stepFlash, parse, selTable, mappedCount, setStep }: {
  step: Step;
  curStepIdx: number;
  stepWarnFlags: Partial<Record<Step, boolean>>;
  stepFlash: boolean;
  parse: unknown;
  selTable: unknown;
  mappedCount: number;
  setStep: (s: Step) => void;
}) {
  return (
    <div style={styles.stepBar}>
      {STEP_ORDER.map((s, i) => {
        const isCurrent = step === s;
        const isDone = i < curStepIdx || (step === 'done' && s !== 'done');
        const hasWarn = !isCurrent && !isDone && stepWarnFlags[s];
        const icon = isCurrent
          ? (hasWarn ? '⚠' : '⏳')
          : isDone
            ? (stepWarnFlags[s] ? '⚠' : '✓')
            : hasWarn
              ? '⚠'
              : '·';
        const iconColor = isCurrent
          ? (hasWarn ? 'var(--warning, #f59e0b)' : 'var(--accent, #3b82f6)')
          : isDone
            ? (stepWarnFlags[s] ? 'var(--warning, #f59e0b)' : 'var(--success, #10b981)')
            : hasWarn
              ? 'var(--warning, #f59e0b)'
              : 'var(--muted)';
        const iconTitle = isCurrent
          ? (hasWarn ? '当前步骤（有警告需处理）' : '当前步骤')
          : isDone
            ? (stepWarnFlags[s] ? '已完成（含警告）' : '已完成')
            : hasWarn
              ? '尚未开始（有警告信号）'
              : '尚未开始';
        // 前置数据守卫：跳步必须满足前置
        let canNav = false;
        let navTitle: string;
        if (s === 'done') {
          canNav = false;
          navTitle = '完成步只可通过执行导入进入';
        } else if (i < curStepIdx) {
          canNav = true;
          navTitle = `回跳「${STEP_LABEL[s]}」`;
        } else if (i > curStepIdx) {
          if (i === 1) { canNav = !!parse; navTitle = parse ? `前进「${STEP_LABEL[s]}」` : '需先解析数据'; }
          else if (i === 2) { canNav = !!parse && !!selTable; navTitle = canNav ? `前进「${STEP_LABEL[s]}」` : '需先选目标表'; }
          else if (i === 3) { canNav = !!parse && !!selTable && mappedCount > 0; navTitle = canNav ? `前进「${STEP_LABEL[s]}」` : '需至少映射一列'; }
          else { canNav = false; navTitle = '不可前进'; }
        } else {
          canNav = false;
          navTitle = '当前步骤';
        }
        return (
          <div
            key={s}
            className={isCurrent && stepFlash ? 'step-badge-flash' : undefined}
            style={{
              ...styles.stepItem,
              ...(step === s ? styles.stepItemActive : {}),
              ...(i < curStepIdx ? styles.stepItemDone : {}),
              display: 'flex', alignItems: 'center', gap: 4,
              cursor: canNav ? 'pointer' : 'default',
              opacity: canNav ? 1 : 0.7,
              userSelect: 'none',
            }}
            title={canNav ? `${navTitle}（${iconTitle}；Ctrl+Alt+${i + 1}）` : `${iconTitle}（${navTitle}）`}
            role="button"
            aria-disabled={!canNav}
            onClick={() => {
              if (!canNav || isCurrent) return;
              setStep(s);
            }}
          >
            <span style={{ color: iconColor, fontWeight: 700, fontSize: 10, flexShrink: 0, minWidth: 10, textAlign: 'center' }}>{icon}</span>
            <span>{STEP_LABEL[s]}</span>
          </div>
        );
      })}
      {/* M30.96 向导头部键盘提示徽标：绝对定位 Ctrl+Alt+1..4 与相对遍历 Ctrl+Alt+[ ] */}
      <button
        style={{
          marginLeft: 'auto',
          padding: '0 6px',
          background: 'transparent',
          border: '1px solid var(--border)',
          borderRadius: 3,
          color: 'var(--muted)',
          cursor: 'help',
          fontSize: 10,
          flexShrink: 0,
          lineHeight: '16px',
          display: 'flex',
          alignItems: 'center',
          gap: 2,
        }}
        title={
          [
            '向导键盘快捷键：',
            'Ctrl+Alt+1 → 粘贴或选择数据',
            'Ctrl+Alt+2 → 目标表 & 模式',
            'Ctrl+Alt+3 → 列映射 & 转换',
            'Ctrl+Alt+4 → 预览 & 执行',
            'Ctrl+Alt+[  → 上一步',
            'Ctrl+Alt+]  → 下一步',
            'Ctrl+Enter → Step 4 提交导入',
            'Ctrl+/    → 快捷键速查（全部）',
            '完成步只可通过执行导入进入',
          ].join('\n')
        }
        aria-label="显示向导键盘快捷键"
      >⌨</button>
    </div>
  );
}

export function formatValue(v: Value): string {
  if (v === null) return 'NULL';
  if (typeof v === 'string') return `'${v.replace(/'/g, "''")}'`;
  if (typeof v === 'boolean') return v ? 'true' : 'false';
  if (typeof v === 'number') return String(v);
  if (Array.isArray(v)) return `[${v.map(formatValue).join(', ')}]`;
  if (typeof v === 'object') return JSON.stringify(v);
  return String(v);
}

export function typeBadgeColor(t: 'string' | 'integer' | 'float' | 'boolean' | 'date' | 'null'): string {
  switch (t) {
    case 'integer': return '#3b82f6';
    case 'float': return '#06b6d4';
    case 'boolean': return '#10b981';
    case 'date': return '#eab308';
    case 'null': return 'var(--muted)';
    case 'string':
    default: return 'var(--muted)';
  }
}

export function qualityScoreColor(score: number): string {
  if (score >= 80) return 'var(--ok, #10b981)';
  if (score >= 60) return '#eab308';
  if (score >= 40) return 'var(--warn, #d97706)';
  return 'var(--danger, #dc2626)';
}

export function colDtypeColor(dtype: string): string {
  const d = dtype.toLowerCase();
  if (/\b(int|integer|bigint|smallint|tinyint|serial|bit|number|numeric|decimal)\b/.test(d)) return '#3b82f6';
  if (/\b(float|double|real|float)\b/.test(d)) return '#06b6d4';
  if (/\b(bool|boolean)\b/.test(d)) return '#10b981';
  if (/\b(date|time|timestamp|datetime)\b/.test(d)) return '#eab308';
  if (/\b(char|varchar|text|clob|nchar|nvarchar|string|uuid|json)\b/.test(d)) return 'var(--accent, #3b82f6)';
  if (/\b(blob|bytea|binary|varbinary|image)\b/.test(d)) return 'var(--muted)';
  return 'var(--muted)';
}

export function toMsg(e: unknown): string {
  if (e instanceof ApiError) return `${e.code}: ${e.message}`;
  if (e instanceof Error) return e.message;
  return String(e);
}

export function formatTime(at: number): string {
  const d = new Date(at);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// M30.127 三态审计徽章：ok=绿 / warn=橙 / dead=红；label 供区分「归档预设」/「目标预设」等上下文
// M30.128 stale 视觉提示：缓存年龄超过 AUDIT_STALE_MS（TTL 一半）时加半透明+"⚠️ stale"标签
// M30.130 busy 态：重审进行中按钮变 ⏳ + disabled，防误连点；age 徽章在 busy 时也置灰表示"刷新中"
const AUDIT_STALE_MS = 12 * 60 * 60 * 1000;
export const AuditBadge = ({ a, cachedAtMs, age, label, onReAudit, busy }: {
  a: { status: PresetAuditStatus; reason: string };
  cachedAtMs: number | null;
  age: string | null;
  label: string;
  onReAudit?: () => void;
  busy?: boolean;
}) => {
  const stale = cachedAtMs !== null && (Date.now() - cachedAtMs) > AUDIT_STALE_MS;
  const icon = a.status === 'ok' ? '✅' : a.status === 'warn' ? '⚠' : '❌';
  const color = a.status === 'ok' ? 'var(--success, #10b981)'
    : a.status === 'warn' ? 'var(--warn, #d97706)' : 'var(--danger, #dc2626)';
  const bg = a.status === 'ok' ? 'rgba(16,185,129,0.06)'
    : a.status === 'warn' ? 'rgba(217,119,6,0.08)' : 'rgba(220,38,38,0.08)';
  const border = a.status === 'ok' ? 'rgba(16,185,129,0.35)'
    : a.status === 'warn' ? 'rgba(217,119,6,0.4)' : 'rgba(220,38,38,0.4)';
  return (
    <div style={{
      display: 'flex', gap: 4, flexWrap: 'wrap', alignItems: 'center',
      padding: '2px 5px', borderRadius: 3,
      background: bg, border: `1px solid ${border}`,
      opacity: (stale ? 0.65 : 1) * (busy ? 0.7 : 1),
    }} title={`${busy ? '🔍 重审中…' : '审计：'}${a.reason}${age ? `（${age}${stale ? ' · ⚠️ stale' : ''}）` : ''}`}>
      <span style={{ color, fontWeight: 600 }}>
        {icon} {label}：{a.status === 'ok' ? '正常' : a.status === 'warn' ? '部分列缺失' : '已失效'}
      </span>
      <span style={{ color: 'var(--muted)' }}>· {a.reason}</span>
      {stale && (
        <span style={{
          padding: '0 4px', borderRadius: 3, fontSize: 8, fontWeight: 600,
          background: 'rgba(217,119,6,0.14)', border: '1px solid rgba(217,119,6,0.5)',
          color: 'var(--warn, #d97706)',
        }} title="审计缓存已超过 12h（TTL 的一半），建议重新审计">⚠️ stale</span>
      )}
      {onReAudit && (
        <button
          type="button"
          disabled={busy}
          onClick={(e) => { e.stopPropagation(); if (!busy) onReAudit(); }}
          style={{
            padding: '0 4px', borderRadius: 3, fontSize: 8, fontWeight: 600,
            background: busy ? 'rgba(148,163,184,0.10)' : 'rgba(59,130,246,0.10)',
            border: `1px solid ${busy ? 'rgba(148,163,184,0.5)' : 'rgba(59,130,246,0.5)'}`,
            color: busy ? 'var(--muted)' : 'var(--accent, #3b82f6)',
            cursor: busy ? 'not-allowed' : 'pointer',
          }} title="对目标表列重跑 listColumns 检查（更新该预设的审计状态）">{busy ? '⏳ 重审中…' : '🔍 重审'}</button>
      )}
      {age && <span style={{ color: 'var(--muted)', fontSize: 8, marginLeft: 'auto', opacity: busy ? 0.5 : 1 }}>{busy ? '🔍 刷新中…' : `🕐 ${age}`}</span>}
    </div>
  );
};

export function historyStatusBadge(status: 'success' | 'partial' | 'failed' | 'cancelled'): string {
  switch (status) {
    case 'success': return '✓ 成功';
    case 'partial': return '◐ 部分';
    case 'failed': return '✕ 失败';
    case 'cancelled': return '⏹ 取消';
  }
}

export const styles: Record<string, CSSProperties> = {
  overlay: {
    position: 'fixed', inset: 0,
    background: 'rgba(0,0,0,0.45)',
    display: 'flex', alignItems: 'center', justifyContent: 'center',
    zIndex: 100, padding: 20,
  },
  panel: {
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 6,
    boxShadow: '0 12px 40px rgba(0,0,0,0.35)',
    width: 900, maxWidth: '95vw',
    height: '85vh', maxHeight: 720,
    display: 'flex', flexDirection: 'column',
    overflow: 'hidden',
  },
  header: {
    display: 'flex', alignItems: 'center',
    padding: '10px 14px',
    borderBottom: '1px solid var(--border)',
    gap: 10,
  },
  title: { fontWeight: 700, fontSize: 14, flex: 1 },
  closeBtn: {
    background: 'transparent', border: 'none',
    color: 'var(--muted)', fontSize: 20,
    cursor: 'pointer', lineHeight: 1, padding: '0 6px',
  },
  stepBar: {
    display: 'flex', padding: '6px 14px', gap: 6,
    borderBottom: '1px solid var(--border)',
    fontFamily: 'monospace', fontSize: 11,
    color: 'var(--muted)', flexWrap: 'wrap',
  },
  stepItem: {
    padding: '2px 8px', borderRadius: 3,
    background: 'var(--panel)',
    border: '1px solid var(--border)',
  },
  stepItemActive: {
    background: 'var(--accent-dim, var(--accent))',
    color: 'var(--fg)', fontWeight: 700,
  },
  stepItemDone: {
    color: 'var(--ok)', borderColor: 'var(--ok)',
  },
  body: {
    flex: 1, padding: 14, overflow: 'auto',
    display: 'flex', flexDirection: 'column', gap: 10,
  },
  col: {
    display: 'flex', flexDirection: 'column',
    gap: 10, height: '100%',
  },
  row2col: { display: 'flex', gap: 12, flexWrap: 'wrap' },
  field: {
    display: 'flex', flexDirection: 'column', gap: 4,
    flex: 1, minWidth: 160,
  },
  label: {
    fontSize: 11, color: 'var(--muted)',
    textTransform: 'uppercase', letterSpacing: '0.05em',
    fontWeight: 600,
  },
  select: {
    padding: '6px 8px', fontSize: 13,
    background: 'var(--input-bg, var(--bg))',
    color: 'var(--fg)',
    border: '1px solid var(--border)',
    borderRadius: 4, outline: 'none',
    width: '100%', boxSizing: 'border-box',
  },
  selectSm: {
    padding: '4px 6px', fontSize: 12,
    background: 'var(--input-bg, var(--bg))',
    color: 'var(--fg)',
    border: '1px solid var(--border)',
    borderRadius: 4, outline: 'none',
    width: '100%', boxSizing: 'border-box',
    fontFamily: 'monospace',
  },
  textarea: {
    width: '100%', height: 160, padding: 10,
    fontSize: 13, fontFamily: 'monospace',
    background: 'var(--input-bg, var(--bg))',
    color: 'var(--fg)',
    border: '1px solid var(--border)',
    borderRadius: 4, resize: 'vertical',
    boxSizing: 'border-box', outline: 'none',
  },
  inputSm: {
    padding: '4px 6px', fontSize: 12,
    background: 'var(--input-bg, var(--bg))',
    color: 'var(--fg)',
    border: '1px solid var(--border)',
    borderRadius: 4, outline: 'none',
    width: '100%', boxSizing: 'border-box',
  },
  inputNum: {
    padding: '6px 8px', fontSize: 13,
    background: 'var(--input-bg, var(--bg))',
    color: 'var(--fg)',
    border: '1px solid var(--border)',
    borderRadius: 4, outline: 'none',
    width: '100%', boxSizing: 'border-box',
  },
  check: {
    display: 'flex', alignItems: 'center', gap: 6,
    fontSize: 13, color: 'var(--fg)', cursor: 'pointer',
  },
  btn: {
    padding: '8px 12px', fontSize: 13,
    background: 'var(--panel)', color: 'var(--fg)',
    border: '1px solid var(--border)',
    borderRadius: 4, cursor: 'pointer',
  },
  btnGhost: {
    padding: '8px 12px', fontSize: 13,
    background: 'transparent', color: 'var(--muted)',
    border: '1px solid var(--border)',
    borderRadius: 4, cursor: 'pointer',
  },
  btnPrimary: {
    padding: '8px 14px', fontSize: 13,
    background: 'var(--accent)', color: '#fff',
    border: 'none', borderRadius: 4,
    cursor: 'pointer', fontWeight: 600,
  },
  btnSm: {
    padding: '2px 8px', fontSize: 11,
    background: 'var(--panel)', color: 'var(--accent, #3b82f6)',
    border: '1px solid var(--accent, #3b82f6)',
    borderRadius: 3, cursor: 'pointer',
    fontFamily: 'monospace',
  },
  footer: {
    display: 'flex', alignItems: 'center',
    gap: 8, marginTop: 'auto',
    padding: '8px 0 0',
    borderTop: '1px solid var(--border)',
  },
  spacer: { flex: 1 },
  muted: { fontSize: 11, color: 'var(--muted)' },
  mutedCenter: { fontSize: 11, color: 'var(--muted)', textAlign: 'center' },
  mutedBox: {
    fontSize: 12, color: 'var(--muted)',
    padding: '8px 10px',
    background: 'var(--panel)',
    border: '1px solid var(--border)',
    borderRadius: 4,
  },
  errorBox: {
    fontSize: 13, color: 'var(--danger, #dc2626)',
    padding: '8px 10px',
    background: 'rgba(220,38,38,0.08)',
    border: '1px solid var(--danger, #dc2626)',
    borderRadius: 4,
  },
  mapGrid: {
    display: 'flex', flexDirection: 'column', gap: 4,
    maxHeight: 380, overflow: 'auto',
    padding: 4,
    background: 'var(--panel)',
    border: '1px solid var(--border)',
    borderRadius: 4,
  },
  mapRow: {
    display: 'grid',
    gridTemplateColumns: '1fr 24px 1.4fr',
    gap: 6, alignItems: 'center',
  },
  mapCell: {
    display: 'flex', gap: 6, alignItems: 'center',
    fontSize: 13,
  },
  arrow: { textAlign: 'center', color: 'var(--muted)' },
  summary: {
    cursor: 'pointer', fontSize: 12,
    color: 'var(--accent)', userSelect: 'none',
  },
  pre: {
    background: 'var(--panel)',
    border: '1px solid var(--border)',
    borderRadius: 4, padding: 8,
    fontFamily: 'monospace', fontSize: 12,
    overflow: 'auto', maxHeight: 180,
    whiteSpace: 'pre-wrap', wordBreak: 'break-all',
    margin: 0,
  },
  progressOuter: {
    width: '100%', height: 8,
    background: 'var(--panel)',
    borderRadius: 4, overflow: 'hidden',
    border: '1px solid var(--border)',
  },
  progressInner: {
    height: '100%', background: 'var(--accent)',
    transition: 'width 0.15s',
  },
  doneIcon: {
    fontSize: 44, textAlign: 'center',
    color: 'var(--ok)', marginTop: 20,
  },
  doneText: { fontSize: 16, textAlign: 'center' },
  previewWrap: {
    maxHeight: 260, overflow: 'auto',
    border: '1px solid var(--border)',
    borderRadius: 4,
    background: 'var(--panel)',
  },
  previewTable: {
    width: '100%', borderCollapse: 'collapse',
    fontSize: 12, fontFamily: 'monospace',
  },
  previewTh: {
    padding: '6px 8px',
    textAlign: 'left',
    borderBottom: '1px solid var(--border)',
    background: 'var(--bg)',
    position: 'sticky', top: 0,
    whiteSpace: 'nowrap',
  },
  previewThInner: {
    display: 'flex', alignItems: 'center', gap: 6,
  },
  previewThSub: {
    fontSize: 10, color: 'var(--muted)',
    fontFamily: 'inherit',
  },
  previewTd: {
    padding: '4px 8px',
    borderRight: '1px dashed var(--border)',
    borderBottom: '1px dashed var(--border)',
    whiteSpace: 'nowrap', overflow: 'hidden',
    textOverflow: 'ellipsis',
    maxWidth: 200,
  },
  previewTdMuted: {
    color: 'var(--muted)',
    width: 32, textAlign: 'right',
    borderBottom: '1px solid var(--border)',
  },
  previewTdNull: {
    color: 'var(--muted)',
    fontStyle: 'italic',
  },
  typeBadge: {
    display: 'inline-block',
    padding: '1px 5px',
    fontSize: 10, borderRadius: 3,
    background: 'rgba(0,0,0,0.06)',
    border: '1px solid currentColor',
    fontFamily: 'monospace',
    fontWeight: 600,
    textTransform: 'lowercase',
  },
  warnText: {
    fontSize: 11,
    color: 'var(--warn, #d97706)',
    background: 'rgba(217,119,6,0.08)',
    border: '1px solid var(--warn, #d97706)',
    borderRadius: 3,
    padding: '1px 4px',
    maxWidth: '100%', overflow: 'hidden',
    textOverflow: 'ellipsis', whiteSpace: 'nowrap',
  },
  selectWarn: {
    borderColor: 'var(--warn, #d97706)',
    background: 'rgba(217,119,6,0.05)',
  },
  failedWrap: {
    maxHeight: 220, overflow: 'auto',
    border: '1px solid var(--warn, #d97706)',
    borderRadius: 4,
    background: 'rgba(217,119,6,0.04)',
  },
  failedTable: {
    width: '100%', borderCollapse: 'collapse',
    fontSize: 12, fontFamily: 'monospace',
  },
  failedTh: {
    padding: '6px 8px', textAlign: 'left',
    borderBottom: '1px solid var(--border)',
    background: 'var(--bg)',
    position: 'sticky', top: 0,
    whiteSpace: 'nowrap',
  },
  failedTd: {
    padding: '4px 8px',
    borderBottom: '1px dashed var(--border)',
    whiteSpace: 'nowrap', overflow: 'hidden',
    textOverflow: 'ellipsis',
    maxWidth: 260,
  },
  dragHint: {
    position: 'fixed', inset: 0,
    pointerEvents: 'none',
    display: 'flex', alignItems: 'center', justifyContent: 'center',
    background: 'rgba(59,130,246,0.08)',
    border: '4px dashed var(--accent)',
    zIndex: 200,
    fontSize: 20,
    color: 'var(--accent)',
    fontWeight: 700,
  },
  historyPanel: {
    background: 'var(--panel)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: 10,
  },
  historyWrap: {
    maxHeight: 300,
    overflow: 'auto',
    border: '1px solid var(--border)',
    borderRadius: 4,
  },
  historyTable: {
    width: '100%', borderCollapse: 'collapse',
    fontSize: 11, fontFamily: 'monospace',
  },
  historyTh: {
    padding: '6px 8px',
    textAlign: 'left',
    borderBottom: '1px solid var(--border)',
    background: 'var(--bg)',
    position: 'sticky', top: 0,
    whiteSpace: 'nowrap',
    fontSize: 11,
  },
  historyTd: {
    padding: '4px 8px',
    borderBottom: '1px dashed var(--border)',
    whiteSpace: 'nowrap', overflow: 'hidden',
    textOverflow: 'ellipsis',
    maxWidth: 200,
  },
};
