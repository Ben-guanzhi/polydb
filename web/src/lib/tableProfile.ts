// U6.1 表概览画像：单条聚合 SQL 统计行数 + 每列 NULL 数/去重数/数值 min-max-avg。
// 纯函数放这里便于单测；组件侧只负责执行与展示。

export interface ProfileColumn {
  name: string;
  data_type: string;
}

export type MetricKind = 'nulls' | 'distinct' | 'min' | 'max' | 'avg';

export interface ProfileMetricSlot {
  idx: number;
  column: string;
  kind: MetricKind;
}

export interface ProfilePlan {
  sql: string;
  countIdx: number;
  metrics: ProfileMetricSlot[];
}

export interface ColumnProfile {
  nulls: number | null;
  distinct: number | null;
  min: unknown;
  max: unknown;
  avg: number | null;
}

const NUMERIC_RE = /\b(int|integer|serial|smallint|bigint|numeric|decimal|float|double|real|money|smallmoney|number|binary_float|binary_double|year)\b/i;
const LOB_RE = /(text|clob|blob|bytea|json|jsonb|xml|image|geometry|geography|interval)/i;

export function isNumericType(t: string): boolean {
  return NUMERIC_RE.test(t);
}

export function isLobType(t: string): boolean {
  return LOB_RE.test(t);
}

/**
 * 生成画像聚合查询计划。
 * @param quote 方言标识符引用函数
 * @param tableExpr 已引用的表表达式（schema.table）
 * @param columns 参与画像的列（调用方先做截断）
 */
export function buildProfilePlan(quote: (s: string) => string, tableExpr: string, columns: ProfileColumn[]): ProfilePlan {
  const selects: string[] = ['COUNT(*)'];
  const metrics: ProfileMetricSlot[] = [];
  let idx = 1;
  for (const col of columns) {
    const q = quote(col.name);
    const numeric = isNumericType(col.data_type);
    const lob = !numeric && isLobType(col.data_type);

    selects.push(`SUM(CASE WHEN ${q} IS NULL THEN 1 ELSE 0 END)`);
    metrics.push({ idx, column: col.name, kind: 'nulls' });
    idx++;

    if (!lob) {
      selects.push(`COUNT(DISTINCT ${q})`);
      metrics.push({ idx, column: col.name, kind: 'distinct' });
      idx++;
    }
    if (numeric) {
      selects.push(`MIN(${q})`, `MAX(${q})`);
      metrics.push({ idx, column: col.name, kind: 'min' });
      idx++;
      metrics.push({ idx, column: col.name, kind: 'max' });
      idx++;
      selects.push(`AVG(${q})`);
      metrics.push({ idx, column: col.name, kind: 'avg' });
      idx++;
    }
  }
  return { sql: `SELECT ${selects.join(', ')} FROM ${tableExpr}`, countIdx: 0, metrics };
}

export interface ProfileResult {
  rowCount: number | null;
  byColumn: Map<string, ColumnProfile>;
}

/** 用 QueryResult.rows[0]（值数组）按计划索引还原画像。 */
export function parseProfile(row: unknown[] | undefined, plan: ProfilePlan): ProfileResult {
  const out: ProfileResult = { rowCount: null, byColumn: new Map() };
  if (!row) return out;
  const cnt = row[plan.countIdx];
  out.rowCount = typeof cnt === 'number' ? cnt : cnt == null ? null : Number(cnt);
  const toNum = (v: unknown): number | null => (v == null ? null : typeof v === 'number' ? v : Number(v));
  for (const m of plan.metrics) {
    let p = out.byColumn.get(m.column);
    if (!p) {
      p = { nulls: null, distinct: null, min: undefined, max: undefined, avg: null };
      out.byColumn.set(m.column, p);
    }
    const v = row[m.idx];
    if (m.kind === 'nulls') p.nulls = toNum(v);
    else if (m.kind === 'distinct') p.distinct = toNum(v);
    else if (m.kind === 'min') p.min = v ?? null;
    else if (m.kind === 'max') p.max = v ?? null;
    else p.avg = toNum(v);
  }
  return out;
}
