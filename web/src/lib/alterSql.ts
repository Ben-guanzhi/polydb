// M16：表结构编辑器的 DDL 生成（纯函数，可单测）。
// 方言差异封装在此；执行由 UI 走事务 API（beginTransaction/executeInTx/commit）。
// 各数据库 ALTER 能力差异（不支持的操作用 UnsupportedAlterError 明确拒绝）：
//   - SQLite：无 ADD CONSTRAINT（外键需重建表）；ADD COLUMN 不能为 PK/UNIQUE，
//     NOT NULL 必须带非常量默认值；DROP COLUMN 需 SQLite ≥ 3.35（两端驱动均满足）
//   - Oracle：无 DROP CONSTRAINT（需重建表），无 DROP INDEX（实际有，支持）
//   - 其余（PG/MySQL/MSSQL）：全支持

export type AlterDialect = 'sqlite' | 'postgres' | 'mysql' | 'mssql' | 'oracle';

/** kind（DatabaseKind）→ 方言 */
export function dialectOf(kind: string): AlterDialect {
  switch (kind) {
    case 'sqlite': return 'sqlite';
    case 'postgres': return 'postgres';
    case 'mysql': return 'mysql';
    case 'mssql': return 'mssql';
    case 'oracle': return 'oracle';
    default: return 'postgres';
  }
}

export class UnsupportedAlterError extends Error {
  constructor(readonly op: string, readonly dialect: AlterDialect, reason: string) {
    super(`${dialect} 不支持该操作：${reason}`);
    this.name = 'UnsupportedAlterError';
  }
}

/** 标识符引用（DDL 生成统一加引用，避免保留字冲突；oracle 统一小写） */
export function quoteIdent(dialect: AlterDialect, s: string): string {
  if (dialect === 'oracle') return `"${s.toLowerCase()}"`;
  return `"${s}"`;
}

export function quoteTable(dialect: AlterDialect, schema: string, table: string): string {
  const t = quoteIdent(dialect, table);
  return schema ? `${quoteIdent(dialect, schema)}.${t}` : t;
}

export interface NewColumn {
  name: string;
  type: string;
  nullable?: boolean;
  defaultExpr?: string;
  primaryKey?: boolean;
}

export interface NewIndex {
  name: string;
  columns: string[];
  unique?: boolean;
}

export interface NewForeignKey {
  name: string;
  columns: string[];
  refSchema?: string;
  refTable: string;
  refColumns: string[];
  onDelete?: string;
  onUpdate?: string;
}

// ─── 列 ─────────────────────────────────────────────────────

export function addColumnSql(d: AlterDialect, schema: string, table: string, c: NewColumn): string {
  if (!/^\w+$/.test(c.name)) throw new UnsupportedAlterError('ADD COLUMN', d, `非法列名：${c.name}`);
  const t = quoteTable(d, schema, table);
  const col = quoteIdent(d, c.name);
  let ddl = `${c.type.toUpperCase()}`;
  if (c.primaryKey) {
    if (d === 'sqlite') throw new UnsupportedAlterError('ADD COLUMN', d, 'SQLite 不允许 ADD COLUMN 加 PRIMARY KEY');
    ddl += ' PRIMARY KEY';
  }
  if (c.nullable === false && !c.defaultExpr && !c.primaryKey) {
    if (d === 'sqlite') throw new UnsupportedAlterError('ADD COLUMN', d, 'SQLite NOT NULL 列必须带非常量 DEFAULT');
  }
  if (d === 'mysql' || d === 'mssql' || d === 'oracle') ddl += c.nullable === false ? ' NOT NULL' : ' NULL';
  if (c.defaultExpr) ddl += ` DEFAULT ${c.defaultExpr}`;
  return `ALTER TABLE ${t} ADD COLUMN ${col} ${ddl};`;
}

export function dropColumnSql(d: AlterDialect, schema: string, table: string, col: string): string {
  if (d === 'sqlite') {
    // SQLite 3.35+ 支持 DROP COLUMN；列被索引/触发器/FK 引用时 DB 会报错，交由驱动返回
  }
  return `ALTER TABLE ${quoteTable(d, schema, table)} DROP COLUMN ${quoteIdent(d, col)};`;
}

export function renameColumnSql(d: AlterDialect, schema: string, table: string, from: string, to: string): string {
  if (d === 'oracle') {
    throw new UnsupportedAlterError('RENAME COLUMN', d, 'Oracle 18c 前不支持 RENAME COLUMN');
  }
  return `ALTER TABLE ${quoteTable(d, schema, table)} RENAME COLUMN ${quoteIdent(d, from)} TO ${quoteIdent(d, to)};`;
}

// ─── 索引 ───────────────────────────────────────────────────

export function addIndexSql(d: AlterDialect, schema: string, table: string, idx: NewIndex): string {
  const cols = idx.columns.map((c) => quoteIdent(d, c)).join(', ');
  const u = idx.unique ? 'UNIQUE ' : '';
  const t = quoteTable(d, schema, table);
  const name = schema ? `${quoteIdent(d, schema)}.${quoteIdent(d, idx.name)}` : quoteIdent(d, idx.name);
  switch (d) {
    case 'mssql':
      // MSSQL：索引建在 schema 上，名称不能带 schema 前缀
      return `CREATE ${u}INDEX ${quoteIdent(d, idx.name)} ON ${t} (${cols});`;
    case 'oracle':
      return `CREATE ${u}INDEX ${name} ON ${t} (${cols});`;
    default:
      return `CREATE ${u}INDEX ${name} ON ${t} (${cols});`;
  }
}

export function dropIndexSql(d: AlterDialect, schema: string, table: string, indexName: string): string {
  const name = schema ? `${quoteIdent(d, schema)}.${quoteIdent(d, indexName)}` : quoteIdent(d, indexName);
  switch (d) {
    case 'mssql':
      return `DROP INDEX ${quoteIdent(d, indexName)} ON ${quoteTable(d, schema, table)};`;
    case 'oracle':
      return `DROP INDEX ${name};`;
    default:
      return `DROP INDEX ${name};`;
  }
}

// ─── 外键 ───────────────────────────────────────────────────

export function addForeignKeySql(
  d: AlterDialect,
  schema: string,
  table: string,
  fk: NewForeignKey,
): string {
  if (d === 'sqlite') {
    throw new UnsupportedAlterError(
      'ADD FOREIGN KEY',
      d,
      'SQLite 不支持 ALTER TABLE ADD FOREIGN KEY，需重建表（CREATE TABLE 新表 → INSERT INTO…SELECT → RENAME）',
    );
  }
  const t = quoteTable(d, schema, table);
  const cols = fk.columns.map((c) => quoteIdent(d, c)).join(', ');
  const ref = fk.refSchema
    ? `${quoteIdent(d, fk.refSchema)}.${quoteIdent(d, fk.refTable)}`
    : quoteIdent(d, fk.refTable);
  const refCols = fk.refColumns.map((c) => quoteIdent(d, c)).join(', ');
  let sql = `ALTER TABLE ${t} ADD CONSTRAINT ${quoteIdent(d, fk.name)} FOREIGN KEY (${cols}) REFERENCES ${ref} (${refCols})`;
  const actions: string[] = [];
  if (fk.onDelete) actions.push(`ON DELETE ${fk.onDelete.toUpperCase()}`);
  if (fk.onUpdate) actions.push(`ON UPDATE ${fk.onUpdate.toUpperCase()}`);
  if (actions.length) sql += ' ' + actions.join(' ');
  return sql + ';';
}

export function dropForeignKeySql(d: AlterDialect, schema: string, table: string, fkName: string): string {
  if (d === 'oracle') {
    throw new UnsupportedAlterError('DROP CONSTRAINT', d, 'Oracle 不支持 DROP CONSTRAINT，需重建表');
  }
  if (d === 'mysql') {
    return `ALTER TABLE ${quoteTable(d, schema, table)} DROP FOREIGN KEY ${quoteIdent(d, fkName)};`;
  }
  // postgres / mssql
  return `ALTER TABLE ${quoteTable(d, schema, table)} DROP CONSTRAINT ${quoteIdent(d, fkName)};`;
}
