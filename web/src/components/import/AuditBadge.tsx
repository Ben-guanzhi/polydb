import type { PresetAuditStatus } from '../../lib/importPresets';

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