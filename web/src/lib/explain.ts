import type { DatabaseKind, QueryResult } from '../api';

function stripComments(text: string): string {
  return text.replace(/--[^\n]*/g, ' ').replace(/\/\*[\s\S]*?\*\*/g, ' ');
}
function stripTrailingSemicolon(s: string): string {
  return s.replace(/;\s*$/, '');
}

export function buildExplainSql(raw: string, kind: DatabaseKind | null): string {
  const clean = stripTrailingSemicolon(stripComments(raw).trim());
  if (!clean) return clean;
  if (/^explain\b/i.test(clean)) return clean;
  switch (kind) {
    case 'sqlite': return `EXPLAIN QUERY PLAN ${clean}`;
    case 'postgres': return `EXPLAIN (FORMAT JSON) ${clean}`;
    case 'mysql': return `EXPLAIN FORMAT=JSON ${clean}`;
    case 'mssql': return `EXPLAIN ${clean}`;
    case 'oracle': return `EXPLAIN PLAN FOR ${clean}`;
    default: return `EXPLAIN ${clean}`;
  }
}

export interface ExplainNode {
  name: string;
  detail?: string;
  cost?: string;
  rows?: string;
  children: ExplainNode[];
}
export type ExplainRender =
  | { mode: 'tree'; nodes: ExplainNode[] }
  | { mode: 'text'; lines: string[] };

export function renderExplain(result: QueryResult, kind: DatabaseKind | null): ExplainRender {
  const rows = result.rows ?? [];
  const cols = result.columns.map((c) => c.name);
  if (rows.length === 0) return { mode: 'text', lines: ['(空结果)'] };

  if (kind === 'sqlite') {
    const colIdx = (n: string) => cols.indexOf(n);
    const iId = colIdx('id'), iParent = colIdx('parent'), iNot = colIdx('notused'), iDetail = colIdx('detail');
    if (iId < 0 || iParent < 0 || iDetail < 0) return textFallback(result);
    const byId = new Map<number, ExplainNode>();
    for (const r of rows) {
      const id = Number(r[iId]);
      let detail = String(r[iDetail] ?? '');
      if (iNot >= 0 && r[iNot] !== null && r[iNot] !== undefined) detail += `  [notused=${r[iNot]}]`;
      byId.set(id, { name: `Step ${id}`, detail, children: [] });
    }
    const roots: ExplainNode[] = [];
    for (const r of rows) {
      const id = Number(r[iId]);
      const parent = Number(r[iParent]);
      const node = byId.get(id);
      if (!node) continue;
      if (parent === 0 || !byId.has(parent)) roots.push(node);
      else byId.get(parent)!.children.push(node);
    }
    return { mode: 'tree', nodes: roots };
  }

  if (kind === 'postgres') {
    const first = rows[0]?.[0];
    if (typeof first === 'string') {
      try {
        const parsed = JSON.parse(first);
        const arr = Array.isArray(parsed) ? parsed : [parsed];
        const walk = (n: Record<string, unknown>): ExplainNode => {
          const name = String(n.NodeType ?? n.RelationName ?? n.ScanMethod ?? n.IndexName ?? 'Plan Node');
          const cost = n.TotalCost != null ? `cost=${Number(n.TotalCost).toFixed(2)}` : undefined;
          const rowsEst = n.PlanRows != null ? `rows≈${Math.round(Number(n.PlanRows))}` : undefined;
          const plans = (n.Plans ?? []) as Record<string, unknown>[];
          return { name, cost, rows: rowsEst, children: plans.map(walk) };
        };
        const nodes = arr.map(walk);
        return nodes.length ? { mode: 'tree', nodes } : textFallback(result);
      } catch { /* fall through */ }
    }
  }

  if (kind === 'mysql') {
    const first = rows[0]?.[0];
    if (typeof first === 'string') {
      try {
        const parsed = JSON.parse(first);
        const qb = parsed?.query_block;
        const step = qb?.steps?.[0];
        if (step) {
          const walk = (n: Record<string, unknown>): ExplainNode => {
            const tbl = n.table_name || n.reference || n.subquery_name || 'step';
            const acc = n.access_type || n.type || '';
            const key = n.key || '';
            const rowsEst = n.rows_examined_per_scan != null
              ? `rows≈${n.rows_examined_per_scan}`
              : n.rows != null ? `rows≈${n.rows}` : '';
            const name = `${tbl}${acc ? ' · ' + acc : ''}${key ? ' · key=' + key : ''}`;
            return { name, rows: rowsEst || undefined, children: [] };
          };
          return { mode: 'tree', nodes: [walk(step)] };
        }
      } catch { /* fall through */ }
    }
  }

  return textFallback(result);
}

function textFallback(result: QueryResult): ExplainRender {
  const lines: string[] = [];
  const rows = result.rows ?? [];
  if (rows.length === 0) return { mode: 'text', lines: ['(空结果)'] };
  const cols = result.columns.map((c) => c.name);
  lines.push(cols.join(' | '));
  for (const r of rows) lines.push(r.map((c) => String(c ?? '')).join(' | '));
  return { mode: 'text', lines };
}

export function isExplainResult(sql: string): boolean {
  return /^explain\b/i.test(stripComments(sql).trim());
}
