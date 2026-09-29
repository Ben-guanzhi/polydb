import type { Value, DatabaseKind } from '../api';
import { ApiError } from './api';
import type { ImportFormat } from './importData';

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

// ─── 导入向导展示/格式化纯函数（自 components/ImportModalParts.tsx 下沉，无 JSX、无 React 依赖）───

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

export function historyStatusBadge(status: 'success' | 'partial' | 'failed' | 'cancelled'): string {
  switch (status) {
    case 'success': return '✓ 成功';
    case 'partial': return '◐ 部分';
    case 'failed': return '✕ 失败';
    case 'cancelled': return '⏹ 取消';
  }
}

// M30 示例数据：各数据库方言的 CSV / JSON 样例（Step 1 占位/填充用）
const SAMPLE_CSV: Record<string, { title: string; csv: string; json: string }> = {
  sqlite: {
    title: 'SQLite 示例',
    csv: `id,name,email,score,active
1,Alice,alice@example.com,95.5,true
2,Bob,bob@example.com,82.0,false
3,Charlie,charlie@example.com,77.75,true`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":true}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":false}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":true}`,
  },
  postgres: {
    title: 'PostgreSQL 示例',
    csv: `id,name,email,score,active,born_at
1,Alice,alice@example.com,95.5,true,2024-03-15 10:30:00
2,Bob,bob@example.com,82.0,false,2024-05-20
3,Charlie,charlie@example.com,77.75,true,2025-01-05 09:00:00`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":true,"born_at":"2024-03-15 10:30:00"}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":false,"born_at":"2024-05-20"}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":true,"born_at":"2025-01-05 09:00:00"}`,
  },
  mysql: {
    title: 'MySQL 示例',
    csv: `id,name,email,score,active,created_at
1,Alice,alice@example.com,95.5,1,'2024-03-15 10:30:00'
2,Bob,bob@example.com,82.0,0,'2024-05-20 08:00:00'
3,Charlie,charlie@example.com,77.75,1,'2025-01-05 09:00:00'`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":1,"created_at":"2024-03-15 10:30:00"}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":0,"created_at":"2024-05-20 08:00:00"}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":1,"created_at":"2025-01-05 09:00:00"}`,
  },
  mssql: {
    title: 'MSSQL 示例',
    csv: `id,name,email,score,active,created_date
1,Alice,alice@example.com,95.5,1,2024-03-15
2,Bob,bob@example.com,82.0,0,2024-05-20
3,Charlie,charlie@example.com,77.75,1,2025-01-05`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":1,"created_date":"2024-03-15"}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":0,"created_date":"2024-05-20"}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":1,"created_date":"2025-01-05"}`,
  },
  oracle: {
    title: 'Oracle 示例',
    csv: `id,name,email,score,active,created_date
1,Alice,alice@example.com,95.5,1,2024-03-15
2,Bob,bob@example.com,82.0,0,2024-05-20
3,Charlie,charlie@example.com,77.75,1,2025-01-05`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":1,"created_date":"2024-03-15"}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":0,"created_date":"2024-05-20"}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":1,"created_date":"2025-01-05"}`,
  },
};

export function sampleForKind(kind: DatabaseKind | null, format: ImportFormat): { title: string; text: string } {
  const s = (kind && SAMPLE_CSV[kind]) ?? SAMPLE_CSV.sqlite;
  return { title: s.title, text: format === 'jsonl' ? s.json : s.csv };
}
