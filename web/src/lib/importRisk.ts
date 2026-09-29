// M30.156 D 备份预检项综合风险评分（自 components/ImportModal.tsx 下沉的纯评分逻辑）：
// diff 数 + 跨连接 + 覆盖动作 + 审计状态 + 快照数合成 0-100 分，
// 输出 {score, level, color, bg, icon, label, parts} 供备份预检每行 chip 使用。不依赖 React。
import type { BackupPreviewItem, PresetAuditStatus } from './importPresets';

export interface ItemRiskScore {
  score: number;
  level: 'low' | 'mid' | 'high';
  color: string;
  bg: string;
  icon: string;
  label: string;
  parts: string[];
}

/**
 * 单个备份预检项的风险评分。
 * @param i 预检项
 * @param connId 当前连接 id（key 前缀的源连接与之不同则视为跨连接）
 * @param backupItemAudit 预设审计状态缓存（key → 'ok' | 'warn' | 'dead'），可为 null
 */
export function computeItemRiskScore(
  i: BackupPreviewItem,
  connId: string,
  backupItemAudit: Record<string, PresetAuditStatus> | null,
): ItemRiskScore {
  const parts: string[] = [];
  let score = 0;
  const diffTotal = i.kind === 'preset' && i.diff && !i.diff.aligned ? i.diff.total : 0;
  if (diffTotal >= 11) { score += 50; parts.push(`diff=${diffTotal}(大)`); }
  else if (diffTotal >= 4) { score += 30; parts.push(`diff=${diffTotal}(中)`); }
  else if (diffTotal > 0) { score += 15; parts.push(`diff=${diffTotal}(小)`); }
  const srcConn = i.key.split('::')[0];
  const crossConn = !!srcConn && srcConn !== connId;
  if (crossConn) { score += 25; parts.push('跨连接'); }
  if (i.action === 'overwrite') { score += 10; parts.push('覆盖'); }
  const audit = i.kind === 'preset' && backupItemAudit ? backupItemAudit[i.key] : null;
  if (audit === 'dead') { score += 60; parts.push('审计失效'); }
  else if (audit === 'ok') { parts.push('存活'); }
  if (i.kind === 'snapshot' && i.snapshotCount > 0) {
    if (i.snapshotCount >= 10) { score += 10; parts.push(`快照=${i.snapshotCount}(多)`); }
  }
  score = Math.min(100, score);
  const level: 'low' | 'mid' | 'high' = score >= 50 ? 'high' : score >= 20 ? 'mid' : 'low';
  const colorMap = {
    low: { color: 'var(--success, #10b981)', bg: 'rgba(16,185,129,0.12)', icon: '🟢', label: '低风险' },
    mid: { color: 'var(--warn, #d97706)', bg: 'rgba(217,119,6,0.14)', icon: '🟡', label: '中风险' },
    high: { color: 'var(--danger, #dc2626)', bg: 'rgba(220,38,38,0.14)', icon: '🔴', label: '高风险' },
  } as const;
  const c = colorMap[level];
  return { score, level, color: c.color, bg: c.bg, icon: c.icon, label: c.label, parts };
}
