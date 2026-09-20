// M13 导出/复制统一出口（docs/plan-tablepro-parity.md §M13）：
// CSV/TSV/JSON/NDJSON/Markdown/SQL INSERT/IN 子句。纯函数，供 QueryWorkspace
// 与 TableDataView 复用；复制与导出遵循网格显示（隐藏列、当前列序）。
import { dialectFor, valueToLiteral, type Dialect } from './changes';
import type { DatabaseKind } from '../api';

type Cell = unknown;

function csvEscape(v: Cell): string {
  if (v === null || v === undefined) return '';
  const s = String(v);
  return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

function tsEscape(v: Cell): string {
  if (v === null || v === undefined) return '';
  return String(v).replace(/\t/g, ' ').replace(/\n/g, ' ');
}

/** CSV（默认带 BOM，Excel 友好）。 */
export function toCsv(columns: string[], rows: Cell[][], withBom = true): string {
  const lines = [columns.join(',')];
  for (const r of rows) lines.push(r.map(csvEscape).join(','));
  const text = lines.join('\n');
  return withBom ? '\uFEFF' + text : text;
}

/** TSV（制表符分隔，粘贴到表格应用）。 */
export function toTsv(columns: string[], rows: Cell[][]): string {
  const lines = [columns.join('\t')];
  for (const r of rows) lines.push(r.map(tsEscape).join('\t'));
  return lines.join('\n');
}

/** JSON：对象数组（每行一对象，键为列名）。 */
export function toJson(columns: string[], rows: Cell[][]): string {
  const obj = rows.map((r) => {
    const o: Record<string, unknown> = {};
    for (let j = 0; j < columns.length; j++) o[columns[j]] = r[j];
    return o;
  });
  return JSON.stringify(obj, null, 2);
}

/** NDJSON：每行一个 JSON 对象。 */
export function toNdjson(columns: string[], rows: Cell[][]): string {
  const lines = rows.map((r) => {
    const o: Record<string, unknown> = {};
    for (let j = 0; j < columns.length; j++) o[columns[j]] = r[j];
    return JSON.stringify(o);
  });
  return lines.join('\n');
}

function mdCell(v: Cell): string {
  if (v === null || v === undefined) return 'NULL';
  return String(v).replace(/\|/g, '\\|').replace(/\n/g, '<br>');
}

/** Markdown 表格（复制为 wiki/文档片段）。 */
export function toMarkdown(columns: string[], rows: Cell[][]): string {
  const header = `| ${columns.map((c) => c).join(' | ')} |`;
  const sep = `| ${columns.map(() => '---').join(' | ')} |`;
  const body = rows.map((r) => `| ${r.map(mdCell).join(' | ')} |`).join('\n');
  return `${header}\n${sep}\n${body}`;
}

/**
 * SQL INSERT（一行一语句，可粘贴回同类库）。null 渲染为 NULL，字符串按方言引用。
 * table 可为「schema.table」形式（点分），整段作为标识符引用（嵌套引号由调用方保证）。
 */
export function toSqlInsert(
  kind: DatabaseKind,
  table: string,
  columns: string[],
  rows: Cell[][],
): string {
  const d: Dialect = dialectFor(kind);
  const qCols = columns.map((c) => d.quoteIdent(c));
  const qTable = table.includes('.')
    ? table.split('.').map((p) => d.quoteIdent(p)).join('.')
    : d.quoteIdent(table);
  const stmts: string[] = [];
  for (const r of rows) {
    const vals = r.map((v) => (v === null || v === undefined ? 'NULL' : valueToLiteral(v as never, d))).join(', ');
    stmts.push(`INSERT INTO ${qTable} (${qCols.join(', ')}) VALUES (${vals});`);
  }
  return stmts.join('\n');
}

/** IN 子句（当前列的值列表，用于快速构造 WHERE col IN (...)）。 */
export function toInClause(kind: DatabaseKind, column: string, values: Cell[]): string {
  const d: Dialect = dialectFor(kind);
  const col = d.quoteIdent(column);
  if (values.length === 0) return `(${col}) IN (NULL)`;
  // 去重保序
  const seen = new Set<string>();
  const uniq = values.filter((v) => {
    const k = String(v);
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });
  const vals = uniq.map((v) => (v === null || v === undefined ? 'NULL' : valueToLiteral(v as never, d))).join(', ');
  return `WHERE ${col} IN (${vals})`;
}

/** 触发浏览器下载（与 QueryWorkspace 既有 download 一致）。 */
export function download(filename: string, mime: string, content: string): void {
  const blob = new Blob([content], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
