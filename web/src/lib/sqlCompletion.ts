import * as monaco from 'monaco-editor';
import { Range } from 'monaco-editor';
import * as api from './api';
import type { ConnectionInfo, DatabaseKind, TableInfo, ColumnInfo } from '../api';

export type SqlKind = DatabaseKind;

interface MetaCache {
  ts: number;
  schemas: string[];
  tables: { name: string; schema: string; type?: string }[];
}
interface ColumnsCache {
  ts: number;
  cols: ColumnInfo[];
}

const META_TTL = 30_000;
const COLS_TTL = 30_000;

const metaCache = new Map<string, MetaCache>();
const colsCache = new Map<string, ColumnsCache>();

export function invalidateCache(connId: string) {
  metaCache.delete(connId);
  for (const k of [...colsCache.keys()]) {
    if (k.startsWith(connId + ':')) colsCache.delete(k);
  }
}

export function invalidateTableCache(connId: string, schema: string, table: string) {
  colsCache.delete(`${connId}:${schema}:${table}`);
}

const KEYWORDS: { label: string; detail: string }[] = [
  { label: 'SELECT', detail: 'SELECT col1, col2 FROM table' },
  { label: 'FROM', detail: 'FROM table' },
  { label: 'WHERE', detail: 'WHERE condition' },
  { label: 'INSERT INTO', detail: 'INSERT INTO t (cols) VALUES (...)' },
  { label: 'VALUES', detail: 'VALUES (v1, v2)' },
  { label: 'UPDATE', detail: 'UPDATE t SET col = v' },
  { label: 'SET', detail: 'SET col = v' },
  { label: 'DELETE FROM', detail: 'DELETE FROM t WHERE ...' },
  { label: 'CREATE TABLE', detail: 'CREATE TABLE t (col TYPE)' },
  { label: 'DROP TABLE', detail: 'DROP TABLE t' },
  { label: 'ALTER TABLE', detail: 'ALTER TABLE t ADD COLUMN ...' },
  { label: 'JOIN', detail: 'JOIN t ON ...' },
  { label: 'LEFT JOIN', detail: 'LEFT JOIN t ON ...' },
  { label: 'RIGHT JOIN', detail: 'RIGHT JOIN t ON ...' },
  { label: 'INNER JOIN', detail: 'INNER JOIN t ON ...' },
  { label: 'FULL OUTER JOIN', detail: 'FULL OUTER JOIN t ON ...' },
  { label: 'CROSS JOIN', detail: 'CROSS JOIN t' },
  { label: 'ON', detail: 'ON cond' },
  { label: 'AND', detail: 'logical AND' },
  { label: 'OR', detail: 'logical OR' },
  { label: 'NOT', detail: 'logical NOT' },
  { label: 'IN', detail: 'IN (v1, v2)' },
  { label: 'BETWEEN', detail: 'BETWEEN a AND b' },
  { label: 'LIKE', detail: 'LIKE "%x%"' },
  { label: 'ILIKE', detail: 'case-insensitive LIKE (PG)' },
  { label: 'IS NULL', detail: 'IS NULL' },
  { label: 'IS NOT NULL', detail: 'IS NOT NULL' },
  { label: 'GROUP BY', detail: 'GROUP BY col' },
  { label: 'HAVING', detail: 'HAVING cond' },
  { label: 'ORDER BY', detail: 'ORDER BY col ASC|DESC' },
  { label: 'LIMIT', detail: 'LIMIT n [OFFSET m]' },
  { label: 'OFFSET', detail: 'OFFSET n' },
  { label: 'ASC', detail: 'ascending' },
  { label: 'DESC', detail: 'descending' },
  { label: 'DISTINCT', detail: 'DISTINCT' },
  { label: 'AS', detail: 'alias' },
  { label: 'CASE WHEN', detail: 'CASE WHEN c THEN v END' },
  { label: 'WHEN', detail: 'WHEN cond THEN v' },
  { label: 'THEN', detail: 'THEN v' },
  { label: 'ELSE', detail: 'ELSE v' },
  { label: 'END', detail: 'END' },
  { label: 'EXISTS', detail: 'EXISTS (SELECT ...)' },
  { label: 'UNION', detail: 'UNION' },
  { label: 'UNION ALL', detail: 'UNION ALL' },
  { label: 'INTERSECT', detail: 'INTERSECT' },
  { label: 'EXCEPT', detail: 'EXCEPT' },
  { label: 'EXPLAIN', detail: 'EXPLAIN [QUERY PLAN] stmt' },
  { label: 'EXPLAIN QUERY PLAN', detail: 'SQLite: EXPLAIN QUERY PLAN stmt' },
  { label: 'COUNT', detail: 'COUNT(*)' },
  { label: 'SUM', detail: 'SUM(x)' },
  { label: 'AVG', detail: 'AVG(x)' },
  { label: 'MIN', detail: 'MIN(x)' },
  { label: 'MAX', detail: 'MAX(x)' },
  { label: 'COALESCE', detail: 'COALESCE(a, b)' },
  { label: 'NULL', detail: 'NULL literal' },
  { label: 'TRUE', detail: 'boolean TRUE' },
  { label: 'FALSE', detail: 'boolean FALSE' },
  { label: 'PRIMARY KEY', detail: 'column constraint' },
  { label: 'FOREIGN KEY', detail: 'FOREIGN KEY (col) REFERENCES t(col)' },
  { label: 'REFERENCES', detail: 'REFERENCES t(col)' },
  { label: 'UNIQUE', detail: 'constraint UNIQUE' },
  { label: 'DEFAULT', detail: 'DEFAULT value' },
  { label: 'CHECK', detail: 'CHECK (cond)' },
  { label: 'INDEX', detail: 'CREATE INDEX' },
  { label: 'BEGIN', detail: 'BEGIN TRANSACTION' },
  { label: 'COMMIT', detail: 'COMMIT' },
  { label: 'ROLLBACK', detail: 'ROLLBACK' },
];

const KIND_DETAIL: Record<SqlKind, string> = {
  sqlite: 'table (SQLite)',
  mysql: 'table (MySQL)',
  postgres: 'table (PostgreSQL)',
  mssql: 'table (SQL Server)',
  oracle: 'table (Oracle)',
  redis: 'table (Redis)',
};

async function getConnKind(connId: string): Promise<SqlKind | null> {
  try {
    const c = await api.getConnection(connId) as ConnectionInfo;
    return c.kind ?? null;
  } catch {
    return null;
  }
}

async function getMeta(connId: string): Promise<MetaCache> {
  const hit = metaCache.get(connId);
  if (hit && Date.now() - hit.ts < META_TTL) return hit;
  const schemasRes = await api.listSchemas(connId).catch(() => [] as { name: string }[]);
  const schemas = (schemasRes as { name: string }[]).map((s) => s.name);
  const tables: { name: string; schema: string; type?: string }[] = [];
  await Promise.all(
    schemas.map(async (s) => {
      try {
        const t = (await api.listTables(connId, s)) as TableInfo[];
        for (const x of t) tables.push({ name: x.name, schema: s, type: x.type });
      } catch { /* ignore */ }
    }),
  );
  const next: MetaCache = { ts: Date.now(), schemas, tables };
  metaCache.set(connId, next);
  return next;
}

async function getCols(connId: string, schema: string, table: string): Promise<ColumnInfo[]> {
  const k = `${connId}:${schema}:${table}`;
  const hit = colsCache.get(k);
  if (hit && Date.now() - hit.ts < COLS_TTL) return hit.cols;
  try {
    const cols = (await api.listColumns(connId, schema, table)) as ColumnInfo[];
    colsCache.set(k, { ts: Date.now(), cols });
    return cols;
  } catch {
    return [];
  }
}

function stripComments(text: string): string {
  return text.replace(/--[^\n]*/g, ' ').replace(/\/\*[\s\S]*?\*\*/g, ' ');
}

function findContext(text: string, endOffset: number): { kind: 'keyword' | 'column'; table?: { schema?: string; name: string } } {
  const upto = text.slice(Math.max(0, endOffset - 400), endOffset);
  const cleaned = stripComments(upto);
  const tableRe = /\b(?:FROM|JOIN|UPDATE|INTO|TABLE)\s+((?:[A-Za-z_][\w$]*\.)?[A-Za-z_][\w$]*)\b/g;
  let lastIndex = -1;
  let lastFull = '';
  let lastTable = '';
  let m: RegExpExecArray | null;
  while ((m = tableRe.exec(cleaned)) !== null) {
    lastIndex = m.index;
    lastFull = m[0];
    lastTable = m[1];
  }
  if (lastIndex < 0) return { kind: 'keyword' };
  const dot = lastTable.indexOf('.');
  const table: { schema?: string; name: string } = dot < 0
    ? { name: lastTable }
    : { schema: lastTable.slice(0, dot), name: lastTable.slice(dot + 1) };
  const after = cleaned.slice(lastIndex + lastFull.length).slice(0, 80);
  const colCtx = /^\s*(?:SELECT|WHERE|SET|ON|ORDER\s+BY|GROUP\s+BY|HAVING|AND|OR|,|\.)/i.test(after);
  if (!colCtx) return { kind: 'keyword' };
  return { kind: 'column', table };
}

let registered = false;
const connIdRef: { current: string | null } = { current: null };

/** 由 QueryWorkspace 在挂载/切连接时设置当前 connId；provider 是全局注册的，用此 ref 保持最新。 */
export function setSqlCompletionConnId(connId: string | null) {
  connIdRef.current = connId;
}

/** 幂等：整个应用只注册一次。 */
export function registerSqlCompletionGlobal(): void {
  if (registered) return;
  registered = true;

  const provide: monaco.languages.CompletionItemProvider = {
    triggerCharacters: ['.'],
    async provideCompletionItems(model, position) {
      const connId = connIdRef.current;
      if (!connId) return { suggestions: [] };
      const offset = model.getOffsetAt(position);
      const lineText = model.getLineContent(position.lineNumber);
      const lineUpto = lineText.slice(0, position.column - 1);
      const rawKind = await getConnKind(connId);
      if (!rawKind || rawKind === 'redis') return { suggestions: [] };
      const kind: SqlKind = rawKind;

      const suggestions: monaco.languages.CompletionItem[] = [];
      const baseWord = model.getWordUntilPosition(position);

      for (const kw of KEYWORDS) {
        suggestions.push({
          label: kw.label,
          kind: monaco.languages.CompletionItemKind.Keyword,
          insertText: kw.label.replace(/\s+/g, ' '),
          detail: kw.detail,
          range: new Range(position.lineNumber, baseWord.startColumn, position.lineNumber, baseWord.endColumn),
        });
      }

      const meta = await getMeta(connId);

      // `.` 后：优先补列
      if (lineUpto.endsWith('.')) {
        const ctx = findContext(model.getValue(), offset);
        if (ctx.kind === 'column' && ctx.table) {
          const schema = ctx.table.schema ?? meta.schemas[0] ?? '';
          const cols = await getCols(connId, schema, ctx.table.name);
          for (const c of cols) {
            suggestions.push({
              label: c.name,
              kind: monaco.languages.CompletionItemKind.Field,
              detail: `${c.data_type}${c.is_primary_key ? ' PK' : ''}${c.nullable ? '' : ' NOT NULL'}`,
              insertText: c.name,
              range: new Range(position.lineNumber, position.column - 1, position.lineNumber, position.column),
            });
          }
          return { suggestions };
        }
        const before = lineUpto.slice(0, -1);
        const m = /(?:^|\s)([A-Za-z_][\w$]*)\.$/.exec(before);
        if (m) {
          const schemaName = m[1];
          const tables = meta.tables.filter((t) => t.schema.toLowerCase() === schemaName.toLowerCase());
          for (const t of tables) {
            suggestions.push({
              label: t.name,
              kind: monaco.languages.CompletionItemKind.Class,
              detail: KIND_DETAIL[kind] ?? 'table',
              insertText: t.name,
              range: new Range(position.lineNumber, position.column - 1, position.lineNumber, position.column),
            });
          }
          return { suggestions };
        }
      }

      // 列上下文：追加该表列
      const ctx = findContext(model.getValue(), offset);
      if (ctx.kind === 'column' && ctx.table) {
        const schema = ctx.table.schema ?? meta.schemas[0] ?? '';
        const cols = await getCols(connId, schema, ctx.table.name);
        for (const c of cols) {
          suggestions.push({
            label: c.name,
            kind: monaco.languages.CompletionItemKind.Field,
            detail: `${c.data_type}${c.is_primary_key ? ' PK' : ''}${c.nullable ? '' : ' NOT NULL'}`,
            insertText: c.name,
            range: new Range(position.lineNumber, baseWord.startColumn, position.lineNumber, baseWord.endColumn),
          });
        }
      }

      // 表名（总是追加）
      for (const t of meta.tables) {
        const display = t.schema && t.schema.toLowerCase() !== 'main' && t.schema.toLowerCase() !== 'public'
          ? `${t.schema}.${t.name}`
          : t.name;
        suggestions.push({
          label: t.name,
          kind: monaco.languages.CompletionItemKind.Class,
          detail: `${KIND_DETAIL[kind] ?? 'table'}${t.schema && t.schema !== 'main' && t.schema !== 'public' ? ` · ${t.schema}` : ''}`,
          insertText: display,
          range: new Range(position.lineNumber, baseWord.startColumn, position.lineNumber, baseWord.endColumn),
        });
      }
      return { suggestions };
    },
  };

  monaco.languages.registerCompletionItemProvider('sql', provide);

  const hover: monaco.languages.HoverProvider = {
    async provideHover(model, position) {
      const connId = connIdRef.current;
      if (!connId) return null;
      const word = model.getWordAtPosition(position);
      if (!word) return null;
      const range = new Range(position.lineNumber, word.startColumn, position.lineNumber, word.endColumn);
      const text = word.word.toLowerCase();
      if (!/^[a-z_][a-z0-9_]*$/.test(text)) return null;
      const meta = await getMeta(connId);
      const t = meta.tables.find((x) => x.name.toLowerCase() === text);
      if (!t) return null;
      const cols = await getCols(connId, t.schema, t.name);
      const pk = cols.filter((c) => c.is_primary_key).map((c) => c.name);
      const list = cols.slice(0, 20).map((c) => `• \`${c.name}\` ${c.data_type}${c.is_primary_key ? ' **PK**' : ''}`).join('\n');
      const more = cols.length > 20 ? `\n*…还有 ${cols.length - 20} 列*` : '';
      return {
        range,
        contents: [
          { value: `#### \`${t.schema}.${t.name}\`` },
          { value: `**类型**：${t.type ?? 'table'}` },
          pk.length ? { value: `**主键**：${pk.join(', ')}` } : { value: '' },
          { value: list + more },
        ],
      };
    },
  };
  monaco.languages.registerHoverProvider('sql', hover);
}
