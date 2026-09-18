import type { Value } from '../api';

export type FailCategoryKey = 'not-null' | 'fk' | 'dup' | 'type' | 'validation' | 'no-affected' | 'other';

export function classifyFailReason(reason: string): { key: FailCategoryKey; label: string; hint: string } {
  const r = reason.toLowerCase();
  if (r.includes('not null') || r.includes('constraint.*notnull')) return { key: 'not-null', label: 'NOT NULL 违反', hint: '对应目标列不允许为空，可改用 nullPolicy=use-default 或 skip-row' };
  if (r.includes('foreign key') || r.includes('fk ') || r.includes('foreign key constraint') || r.includes('references')) return { key: 'fk', label: '外键违反', hint: '引用值在父表不存在，需先导入父表或修正引用' };
  if (r.includes('duplicate') || r.includes('unique') || r.includes('primary key')) return { key: 'dup', label: '主键/唯一冲突', hint: '目标表已有相同 PK/UK 行，改用 upsert 或先删除重复行' };
  if (r.includes('type') || r.includes('cast') || r.includes('out of range') || r.includes('numeric') || r.includes('cannot be cast') || r.includes('syntax error')) return { key: 'type', label: '类型/值域错误', hint: 'CSV 值无法转为目标 dtype，考虑加 trim / date-parse / scale 转换' };
  if (r.includes('validation') || r.includes('必填') || r.includes('regex') || r.includes('enum') || r.includes('最小') || r.includes('最大')) return { key: 'validation', label: '校验失败', hint: '自定义校验未通过，可放宽正则或调整 enum 列表' };
  if (r.includes('affected_rows=0')) return { key: 'no-affected', label: '未影响行', hint: 'UPDATE/Upsert 无匹配行，检查 PK 条件是否正确' };
  return { key: 'other', label: '其他错误', hint: '查看原始错误详情排查' };
}

export function failCategoryMeta(key: string): { icon: string; color: string; label: string } {
  switch (key) {
    case 'not-null': return { icon: '🚫', color: '#dc2626', label: 'NOT NULL 违反' };
    case 'fk': return { icon: '🔗', color: '#7c3aed', label: '外键违反' };
    case 'dup': return { icon: '⚠️', color: '#d97706', label: '主键/唯一冲突' };
    case 'type': return { icon: '🔧', color: '#2563eb', label: '类型/值域错误' };
    case 'validation': return { icon: '📐', color: '#db2777', label: '校验失败' };
    case 'no-affected': return { icon: '🕳️', color: '#0891b2', label: '未影响行' };
    default: return { icon: '❓', color: '#6b7280', label: '其他错误' };
  }
}

export function previewParams(params: Value[]): string {
  const s = params.map((p) => {
    if (p === null) return 'NULL';
    if (typeof p === 'string') return p.length > 20 ? `${p.slice(0, 20)}…` : p;
    if (typeof p === 'number' || typeof p === 'boolean') return String(p);
    return String(p);
  }).join(', ');
  return s;
}

// 把 Value 渲染成 SQL 字面量（用于复制 INSERT 语句；null→NULL，bool→0/1，number 原样，string 用单引号包裹并转义）
export function sqlLiteral(v: Value): string {
  if (v === null) return 'NULL';
  if (typeof v === 'number') {
    if (!Number.isFinite(v)) return 'NULL';
    return String(v);
  }
  if (typeof v === 'boolean') return v ? '1' : '0';
  if (typeof v === 'string') return `'${v.replace(/'/g, "''")}'`;
  // 嵌套数组/对象：JSON 字符串兜底
  try { return `'${JSON.stringify(v).replace(/'/g, "''")}'`; } catch { return 'NULL'; }
}

export function boolLabel(v: boolean): string {
  return v ? '✅ 是' : '❌ 否';
}

export function fmtCacheAgeForAudit(t: number): string {
  const diff = Date.now() - t;
  const day = 24 * 60 * 60 * 1000;
  if (diff < 60 * 1000) return '刚刚';
  if (diff < 60 * 60 * 1000) return `${Math.floor(diff / (60 * 1000))} 分钟前`;
  if (diff < day) return `${Math.floor(diff / (60 * 60 * 1000))} 小时前`;
  return `${Math.floor(diff / day)} 天前`;
}
