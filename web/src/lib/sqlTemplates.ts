import type { DatabaseKind } from '../api';

export type SqlKind = DatabaseKind;

export interface TemplateContext {
  kind: SqlKind | null;
  schema?: string;
  table?: string;
  column?: string;
  rawSql?: string;
}

export interface SqlTemplate {
  id: string;
  label: string;
  group: string;
  keywords: string[];
  hotkey?: string;
  description?: string;
  requiresTable?: boolean;
  requiresRawSql?: boolean;
  build: (ctx: TemplateContext) => string | null;
}

// 用当前连接的 db kind 生成对应方言；无 kind 时回退到 MySQL 风格。
function quote(ident: string): string {
  return /^[A-Za-z_][A-Za-z0-9_$]*$/.test(ident) ? ident : `"${ident.replace(/"/g, '""')}"`;
}

function qualify(schema: string | undefined, table: string): string {
  if (schema && schema.toLowerCase() !== 'main' && schema.toLowerCase() !== 'public') {
    return `${quote(schema)}.${quote(table)}`;
  }
  return quote(table);
}

function nowExpr(kind: SqlKind | null): string {
  switch (kind) {
    case 'postgres': return 'NOW()';
    case 'mssql': return 'GETDATE()';
    case 'oracle': return 'SYSDATE';
    case 'mysql': case 'sqlite': default: return 'CURRENT_TIMESTAMP';
  }
}

function limitClause(kind: SqlKind | null, n: number): string {
  switch (kind) {
    case 'mssql': return `OFFSET 0 ROWS FETCH NEXT ${n} ROWS ONLY`;
    case 'oracle': return `FETCH FIRST ${n} ROWS ONLY`;
    default: return `LIMIT ${n}`;
  }
}

export const TEMPLATES: SqlTemplate[] = [
  // ── 查询 ─────────────────────────────────────────
  {
    id: 'tpl.select-table',
    label: 'SELECT * FROM 表 LIMIT 100',
    group: '查询',
    keywords: ['select', '查询', '表'],
    requiresTable: true,
    build: (c) => c.table
      ? `SELECT *\nFROM ${qualify(c.schema, c.table)}\n${c.kind === 'mssql' ? 'OFFSET 0 ROWS FETCH NEXT 100 ROWS ONLY;' : `LIMIT 100;`}`
      : null,
  },
  {
    id: 'tpl.count-table',
    label: 'COUNT(*) 行数',
    group: '查询',
    keywords: ['count', '行数', '统计'],
    requiresTable: true,
    build: (c) => c.table
      ? `SELECT COUNT(*) FROM ${qualify(c.schema, c.table)};`
      : null,
  },
  {
    id: 'tpl.select-distinct',
    label: 'SELECT DISTINCT',
    group: '查询',
    keywords: ['distinct', '去重'],
    requiresTable: true,
    build: (c) => c.table
      ? `SELECT DISTINCT *\nFROM ${qualify(c.schema, c.table)};`
      : null,
  },
  {
    id: 'tpl.join-two',
    label: 'JOIN 两张表',
    group: '查询',
    keywords: ['join', '连接', 'inner join', 'left join'],
    requiresTable: true,
    build: (c) => c.table
      ? `SELECT a.*, b.*\nFROM ${qualify(c.schema, c.table)} a\nINNER JOIN ${qualify(c.schema, 'other_table')} b\n    ON a.id = b.a_id;`
      : null,
  },
  {
    id: 'tpl.group-by',
    label: 'GROUP BY + 聚合',
    group: '查询',
    keywords: ['group by', '聚合', 'having'],
    requiresTable: true,
    build: (c) => c.table
      ? `SELECT ${c.column ?? 'category'} AS k, COUNT(*) AS cnt, SUM(1) AS sum_rows\nFROM ${qualify(c.schema, c.table)}\nGROUP BY ${c.column ?? 'category'}\nHAVING COUNT(*) > 1\nORDER BY cnt DESC;`
      : null,
  },
  {
    id: 'tpl.window-rank',
    label: '窗口函数 ROW_NUMBER',
    group: '查询',
    keywords: ['window', 'row_number', 'rank', 'partition'],
    requiresTable: true,
    build: (c) => c.table
      ? `SELECT *,\n       ROW_NUMBER() OVER (PARTITION BY ${c.column ?? 'category'} ORDER BY id DESC) AS rn\nFROM ${qualify(c.schema, c.table)};`
      : null,
  },
  {
    id: 'tpl.with-cte',
    label: 'WITH CTE 骨架',
    group: '查询',
    keywords: ['with', 'cte', 'common table', 'common table expression'],
    requiresTable: true,
    build: (c) => c.table
      ? `WITH cte AS (\n    SELECT * FROM ${qualify(c.schema, c.table)}\n    WHERE ${c.column ?? 'id'} IS NOT NULL\n)\nSELECT * FROM cte;`
      : null,
  },
  {
    id: 'tpl.union',
    label: 'UNION 合并',
    group: '查询',
    keywords: ['union', '合并'],
    requiresTable: true,
    build: (c) => c.table
      ? `SELECT * FROM ${qualify(c.schema, c.table)}\nUNION ALL\nSELECT * FROM ${qualify(c.schema, 'other_table')};`
      : null,
  },

  // ── 建表 & DDL ─────────────────────────────────
  {
    id: 'tpl.create-table',
    label: 'CREATE TABLE 骨架',
    group: 'DDL',
    keywords: ['create', 'table', '建表', 'create table'],
    build: (c) => {
      const tbl = c.table ?? 'new_table';
      const cols = [
        c.kind === 'sqlite' ? 'id INTEGER PRIMARY KEY AUTOINCREMENT' :
        c.kind === 'postgres' ? 'id SERIAL PRIMARY KEY' :
        c.kind === 'mssql' ? 'id INT IDENTITY(1,1) PRIMARY KEY' :
        c.kind === 'oracle' ? 'id NUMBER PRIMARY KEY' :
        'id BIGINT AUTO_INCREMENT PRIMARY KEY',
        `name VARCHAR(100) NOT NULL`,
        `created_at TIMESTAMP DEFAULT ${nowExpr(c.kind)}`,
      ].join(',\n    ');
      return `CREATE TABLE ${quote(tbl)} (\n    ${cols}\n);`;
    },
  },
  {
    id: 'tpl.add-column',
    label: 'ALTER TABLE ADD COLUMN',
    group: 'DDL',
    keywords: ['alter', 'add', 'column', '添加列'],
    requiresTable: true,
    build: (c) => c.table
      ? `ALTER TABLE ${qualify(c.schema, c.table)}\n    ADD COLUMN ${c.column ?? 'new_col'} VARCHAR(100);`
      : null,
  },
  {
    id: 'tpl.drop-table',
    label: 'DROP TABLE',
    group: 'DDL',
    keywords: ['drop', '删除表', '删表'],
    requiresTable: true,
    build: (c) => c.table
      ? `DROP TABLE IF EXISTS ${qualify(c.schema, c.table)};`
      : null,
  },
  {
    id: 'tpl.truncate-table',
    label: 'TRUNCATE TABLE',
    group: 'DDL',
    keywords: ['truncate', '清空'],
    requiresTable: true,
    build: (c) => c.table
      ? (c.kind === 'mssql' ? `TRUNCATE TABLE ${qualify(c.schema, c.table)};`
         : c.kind === 'oracle' ? `TRUNCATE TABLE ${qualify(c.schema, c.table)};`
         : `DELETE FROM ${qualify(c.schema, c.table)};`)
      : null,
  },

  // ── 索引 ─────────────────────────────────────────
  {
    id: 'tpl.create-index',
    label: 'CREATE INDEX',
    group: 'DDL',
    keywords: ['index', '索引'],
    requiresTable: true,
    build: (c) => c.table && c.column
      ? `CREATE INDEX idx_${c.table}_${c.column}\nON ${qualify(c.schema, c.table)} (${quote(c.column)});`
      : null,
  },
  {
    id: 'tpl.create-unique-index',
    label: 'CREATE UNIQUE INDEX',
    group: 'DDL',
    keywords: ['unique', 'index', '唯一索引'],
    requiresTable: true,
    build: (c) => c.table && c.column
      ? `CREATE UNIQUE INDEX udx_${c.table}_${c.column}\nON ${qualify(c.schema, c.table)} (${quote(c.column)});`
      : null,
  },
  {
    id: 'tpl.drop-index',
    label: 'DROP INDEX',
    group: 'DDL',
    keywords: ['drop index', '删除索引'],
    requiresTable: true,
    build: (c) => c.table && c.column
      ? (c.kind === 'mssql' || c.kind === 'oracle'
          ? `DROP INDEX idx_${c.table}_${c.column} ON ${qualify(c.schema, c.table)};`
          : `DROP INDEX IF EXISTS idx_${c.table}_${c.column} ON ${qualify(c.schema, c.table)};`)
      : null,
  },
  {
    id: 'tpl.add-unique-constraint',
    label: 'UNIQUE 约束',
    group: 'DDL',
    keywords: ['unique', 'constraint', '唯一约束'],
    requiresTable: true,
    build: (c) => c.table
      ? `ALTER TABLE ${qualify(c.schema, c.table)}\n    ADD CONSTRAINT uq_${c.table}_${c.column ?? 'col'} UNIQUE (${c.column ?? 'col'});`
      : null,
  },
  {
    id: 'tpl.add-foreign-key',
    label: 'FOREIGN KEY 约束',
    group: 'DDL',
    keywords: ['foreign key', 'fk', '外键'],
    requiresTable: true,
    build: (c) => c.table
      ? `ALTER TABLE ${qualify(c.schema, c.table)}\n    ADD CONSTRAINT fk_${c.table}_ref\n    FOREIGN KEY (ref_id) REFERENCES ${qualify(c.schema, 'other_table')} (id);\n`
      : null,
  },

  // ── 数据操作 ─────────────────────────────────────
  {
    id: 'tpl.insert-table',
    label: 'INSERT INTO',
    group: 'DML',
    keywords: ['insert', '插入'],
    requiresTable: true,
    build: (c) => c.table
      ? `INSERT INTO ${qualify(c.schema, c.table)} (name)\nVALUES ('example');`
      : null,
  },
  {
    id: 'tpl.update-table',
    label: 'UPDATE',
    group: 'DML',
    keywords: ['update', '更新'],
    requiresTable: true,
    build: (c) => c.table
      ? `UPDATE ${qualify(c.schema, c.table)}\nSET name = 'example'\nWHERE id = ?;`
      : null,
  },
  {
    id: 'tpl.delete-where',
    label: 'DELETE WHERE',
    group: 'DML',
    keywords: ['delete', '删除'],
    requiresTable: true,
    build: (c) => c.table
      ? `DELETE FROM ${qualify(c.schema, c.table)}\nWHERE id = ?;`
      : null,
  },

  // ── 执行计划 ─────────────────────────────────────
  {
    id: 'tpl.explain',
    label: 'EXPLAIN 当前查询',
    group: '执行计划',
    keywords: ['explain', '执行计划'],
    requiresRawSql: true,
    build: (c) => {
      const raw = c.rawSql?.trim();
      if (!raw) return null;
      const stripped = raw.replace(/;/g, '').trim();
      const withoutExplain = stripped.replace(/^(explain\b[\s\S]*)/i, '$1');
      if (/^explain\b/i.test(stripped)) return `${stripped};`;
      switch (c.kind) {
        case 'sqlite': return `EXPLAIN QUERY PLAN ${stripped};`;
        case 'postgres': return `EXPLAIN (FORMAT JSON) ${stripped};`;
        case 'mysql': return `EXPLAIN FORMAT=JSON ${stripped};`;
        case 'mssql': return `EXPLAIN ${stripped};`;
        case 'oracle': return `EXPLAIN PLAN FOR ${stripped};\nSELECT * FROM TABLE(DBMS_XPLAN.DISPLAY);`;
        default: return `EXPLAIN ${stripped};`;
      }
    },
  },

  // ── 元数据 ───────────────────────────────────────
  {
    id: 'tpl.schema-info',
    label: 'SHOW TABLES / INFORMATION_SCHEMA',
    group: '元数据',
    keywords: ['schema', 'tables', 'information_schema', '列表'],
    build: (c) => {
      switch (c.kind) {
        case 'sqlite': return "SELECT name, type FROM sqlite_master WHERE type IN ('table','view') ORDER BY type, name;";
        case 'postgres': return "SELECT schemaname, tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') ORDER BY schemaname, tablename;";
        case 'mysql': return `SELECT table_schema, table_name FROM information_schema.tables WHERE table_schema NOT IN ('mysql','information_schema','performance_schema','sys') ORDER BY table_schema, table_name;`;
        case 'mssql': return `SELECT schema_name(schema_id) AS schema_name, name FROM sys.tables ORDER BY schema_name, name;`;
        case 'oracle': return `SELECT owner, table_name FROM all_tables ORDER BY owner, table_name;`;
        default: return `SELECT 1;`;
      }
    },
  },
  {
    id: 'tpl.row-count',
    label: '所有表行数',
    group: '元数据',
    keywords: ['row count', '行数', '所有表'],
    build: (c) => {
      switch (c.kind) {
        case 'sqlite': return `SELECT m.name, (SELECT COUNT(*) FROM main.m.name) AS rows FROM sqlite_master m WHERE m.type='table' ORDER BY rows DESC;`;
        case 'postgres': return `SELECT schemaname, tablename, n_live_tup FROM pg_stat_user_tables ORDER BY n_live_tup DESC NULLS LAST;`;
        case 'mysql': return `SELECT table_schema, table_name, table_rows FROM information_schema.tables WHERE table_schema NOT IN ('mysql','information_schema','performance_schema','sys') ORDER BY table_rows DESC;`;
        case 'mssql': return `SELECT s.name AS schema_name, t.name AS table_name, p.rows FROM sys.tables t JOIN sys.schemas s ON t.schema_id=s.schema_id JOIN sys.partitions p ON t.object_id=p.object_id WHERE p.index_id IN (0,1) ORDER BY p.rows DESC;`;
        case 'oracle': return `SELECT owner, table_name, num_rows FROM all_tables ORDER BY num_rows DESC NULLS LAST;`;
        default: return `SELECT 1;`;
      }
    },
  },
];

export function filterTemplates(query: string, ctx: TemplateContext, source: SqlTemplate[] = TEMPLATES): SqlTemplate[] {
  const q = query.trim().toLowerCase();
  return source.filter((t) => {
    if (t.requiresTable && !ctx.table) return false;
    if (t.requiresRawSql && !ctx.rawSql?.trim()) return false;
    if (!q) return true;
    const hay = [t.label, t.group, t.description ?? '', ...t.keywords].join(' ').toLowerCase();
    return hay.includes(q);
  });
}

export function renderTemplate(t: SqlTemplate, ctx: TemplateContext): string | null {
  return t.build(ctx);
}
