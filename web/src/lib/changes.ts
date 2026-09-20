// M12 变更跟踪（参考 TablePro 编辑队列工作流，独立实现）：
// 单元格编辑先在本地排队，Review SQL 后在单个事务内提交，逐条校验影响行数。
// 契约依据：spec/behavior.md §10（事务单次 finalize）+ docs/plan-tablepro-parity.md §2.

import type { DatabaseKind, QueryRequest, Value } from '../api';
import * as api from './api';

export type RowChangeType = 'update' | 'insert' | 'delete';

export interface CellChange {
  column: string;
  oldValue: Value;
  newValue: Value;
}

export interface RowChange {
  /** 网格内稳定行键（PK 值组合或合成 id）。 */
  rowId: string;
  type: RowChangeType;
  /** update：合并后的单元格改动；delete：行快照（originalRow）；insert：newRow。 */
  cellChanges: CellChange[];
  /** load 时的整行快照（delete 的 WHERE 依据 + 还原）。 */
  originalRow: Record<string, Value>;
  /** insert 的完整行值。 */
  newRow?: Record<string, Value>;
  /** 用户操作顺序号：DELETE→复用值→INSERT 的顺序是 load-bearing。 */
  sequence: number;
}

type UndoOp =
  | { kind: 'cell'; key: string; column: string }
  | { kind: 'row'; key: string };

// 内部键 = rowId|type：同一 rowId 的 delete 与 insert 可并存（复用唯一值的场景，
// 提交顺序 load-bearing，参考 TablePro changeIndex[rowID+type]）。
const keyOf = (rowId: string, type: RowChangeType): string => `${rowId}\u0000${type}`;

/** 每 tab 一个队列：同格重复编辑合并（保留最初旧值）、改回原值自动塌缩。 */
export class ChangeQueue {
  private changes = new Map<string, RowChange>();
  private undoStack: UndoOp[] = [];
  private seq = 0;

  get size(): number {
    return this.changes.size;
  }

  get all(): RowChange[] {
    // 按 sequence 稳定排序（提交顺序 = 用户操作顺序）。
    return [...this.changes.values()].sort((a, b) => a.sequence - b.sequence);
  }

  /** 取某行已存在的变更（update 优先，其次 delete）。 */
  get(rowId: string): RowChange | undefined {
    return this.changes.get(keyOf(rowId, 'update')) ?? this.changes.get(keyOf(rowId, 'delete'));
  }

  has(rowId: string): boolean {
    return (
      this.changes.has(keyOf(rowId, 'update')) ||
      this.changes.has(keyOf(rowId, 'delete')) ||
      this.changes.has(keyOf(rowId, 'insert'))
    );
  }

  /** 某行的插入条目（新行编辑时用）。 */
  insertOf(rowId: string): RowChange | undefined {
    return this.changes.get(keyOf(rowId, 'insert'));
  }

  /** 单元格编辑：改回原值则移除该格改动（全空时移除整行变更）。
   * 对 insert 行则直接写 newRow，不进 cellChanges。 */
  addCellChange(rowId: string, column: string, oldValue: Value, newValue: Value, originalRow: Record<string, Value>): void {
    if (valuesEqual(oldValue, newValue)) return;
    const insertEntry = this.changes.get(keyOf(rowId, 'insert'));
    if (insertEntry) {
      const prevRow = insertEntry.newRow ?? {};
      insertEntry.newRow = { ...prevRow, [column]: newValue };
      this.undoStack.push({ kind: 'row', key: keyOf(rowId, 'insert') });
      return;
    }
    const k = keyOf(rowId, 'update');
    let change = this.changes.get(k);
    if (!change) {
      change = {
        rowId,
        type: 'update',
        cellChanges: [],
        originalRow,
        sequence: ++this.seq,
      };
      this.changes.set(k, change);
    }
    const prev = change.cellChanges.find((c) => c.column === column);
    // 新值回到原值 → 移除该格（塌缩）；否则合并/追加。
    if (valuesEqual(newValue, originalRow[column] ?? null)) {
      change.cellChanges = change.cellChanges.filter((c) => c.column !== column);
      this.undoStack.push({ kind: 'cell', key: k, column });
      if (change.cellChanges.length === 0) this.changes.delete(k);
      return;
    }
    if (prev) {
      prev.newValue = newValue;
    } else {
      change.cellChanges.push({ column, oldValue, newValue });
    }
    this.undoStack.push({ kind: 'cell', key: k, column });
  }

  addInsert(rowId: string, row: Record<string, Value>): void {
    this.changes.set(keyOf(rowId, 'insert'), {
      rowId,
      type: 'insert',
      cellChanges: [],
      originalRow: {},
      newRow: row,
      sequence: ++this.seq,
    });
    this.undoStack.push({ kind: 'row', key: keyOf(rowId, 'insert') });
  }

  addDelete(rowId: string, originalRow: Record<string, Value>): void {
    this.changes.set(keyOf(rowId, 'delete'), {
      rowId,
      type: 'delete',
      cellChanges: [],
      originalRow,
      sequence: ++this.seq,
    });
    this.undoStack.push({ kind: 'row', key: keyOf(rowId, 'delete') });
  }

  /** 撤销最近一次操作。cell 撤销：移除该格改动（全空则整行）；row 撤销：移除整条插入/删除。 */
  undo(): boolean {
    const op = this.undoStack.pop();
    if (!op) return false;
    if (op.kind === 'row') {
      return this.changes.delete(op.key);
    }
    const change = this.changes.get(op.key);
    if (!change || change.type !== 'update') return false;
    change.cellChanges = change.cellChanges.filter((c) => c.column !== op.column);
    if (change.cellChanges.length === 0) {
      this.changes.delete(op.key);
    }
    return true;
  }

  /** 保存成功 / 全部还原后清空。 */
  clear(): void {
    this.changes.clear();
    this.undoStack = [];
  }
}

export function valuesEqual(a: Value, b: Value): boolean {
  if (a === b) return true;
  if (a === null || a === undefined) return (b === null || b === undefined);
  if (typeof a !== typeof b) return false;
  if (typeof a !== 'object') return a === b;
  return JSON.stringify(a) === JSON.stringify(b);
}

// ─── 方言与字面量渲染 ─────────────────────────────────────

export interface Dialect {
  quoteIdent: (name: string) => string;
  quoteString: (s: string) => string;
  /** 自增/主键列在 INSERT 时的默认关键字。 */
  defaultKeyword: string;
}

const dialectCache: Partial<Record<string, Dialect>> = {};

export function dialectFor(kind: DatabaseKind): Dialect {
  const cached = dialectCache[kind];
  if (cached) return cached;
  let d: Dialect;
  switch (kind) {
    case 'mysql':
      d = {
        quoteIdent: (n) => '`' + n.replace(/`/g, '``') + '`',
        quoteString: (s) => "'" + s.replace(/\\/g, '\\\\').replace(/'/g, "''") + "'",
        defaultKeyword: 'DEFAULT',
      };
      break;
    case 'mssql':
      d = {
        quoteIdent: (n) => '[' + n.replace(/\]/g, ']]') + ']',
        quoteString: (s) => "'" + s.replace(/'/g, "''") + "'",
        defaultKeyword: 'DEFAULT',
      };
      break;
    default:
      // sqlite / postgres / oracle：ANSI 双引号
      d = {
        quoteIdent: (n) => '"' + n.replace(/"/g, '""') + '"',
        quoteString: (s) => "'" + s.replace(/'/g, "''") + "'",
        defaultKeyword: 'DEFAULT',
      };
  }
  dialectCache[kind] = d;
  return d;
}

export function valueToLiteral(v: Value, d: Dialect): string {
  if (v === null || v === undefined) return 'NULL';
  if (typeof v === 'number') return String(v);
  if (typeof v === 'boolean') return v ? 'TRUE' : 'FALSE';
  if (typeof v === 'string') return v === '' ? "''" : d.quoteString(v);
  return d.quoteString(JSON.stringify(v));
}

// ─── 语句生成（§2-2：PK WHERE；PK 值为空则置 DEFAULT）────────

export interface GeneratedStatement {
  sql: string; // 占位符已内联为字面量（预览用；提交仍走参数化 SQL）
  parameterized: { sql: string; params: Value[] };
  /** 期望影响行数（keyed：delete/insert 期望 1；update 0 亦合法）。 */
  expectAffected: number;
  /** keyed update：actual > expect 才拦（MySQL 同值更新报 0 是正常的）。 */
  allowZero: boolean;
}

function qualifiedTable(d: Dialect, schema: string, table: string): string {
  const t = d.quoteIdent(table);
  return schema ? `${d.quoteIdent(schema)}.${t}` : t;
}

function pkWhere(d: Dialect, pks: string[], row: Record<string, Value>): string {
  return pks.map((p) => `${d.quoteIdent(p)} = ${valueToLiteral(row[p] ?? null, d)}`).join(' AND ');
}

/** 生成提交语句（顺序 = 用户操作顺序）。PK 值缺失的行会抛错（拒绝提交）。 */
export function buildStatements(
  changes: RowChange[],
  schema: string,
  table: string,
  pkColumns: string[],
  d: Dialect,
): GeneratedStatement[] {
  const qTable = qualifiedTable(d, schema, table);
  const out: GeneratedStatement[] = [];
  for (const change of changes) {
    if (change.type === 'delete') {
      const where = pkWhere(d, pkColumns, change.originalRow);
      out.push({
        sql: `DELETE FROM ${qTable} WHERE ${where}`,
        parameterized: {
          sql: `DELETE FROM ${qTable} WHERE ${pkColumns.map((p) => `${d.quoteIdent(p)} = ?`).join(' AND ')}`,
          params: pkColumns.map((p) => change.originalRow[p] ?? null),
        },
        expectAffected: 1,
        allowZero: false,
      });
    } else if (change.type === 'insert') {
      const row = change.newRow ?? {};
      const cols = Object.keys(row);
      // PK 值为空时置 DEFAULT（内联关键字），其余列走绑定参数。
      const isDefault = (c: string) =>
        (row[c] === null || row[c] === '' || row[c] === undefined) && pkColumns.includes(c);
      const values = cols.map((c) => (isDefault(c) ? d.defaultKeyword : valueToLiteral(row[c] ?? null, d)));
      const psql = `INSERT INTO ${qTable} (${cols.map((c) => d.quoteIdent(c)).join(', ')}) VALUES (${cols.map((c) =>
        isDefault(c) ? d.defaultKeyword : '?'
      ).join(', ')})`;
      const pparams: Value[] = cols.filter((c) => !isDefault(c)).map((c) => (row[c] ?? null) as Value);
      out.push({
        sql: `INSERT INTO ${qTable} (${cols.map((c) => d.quoteIdent(c)).join(', ')}) VALUES (${values.join(', ')})`,
        parameterized: { sql: psql, params: pparams },
        expectAffected: 1,
        allowZero: false,
      });
    } else {
      // update：多格合并为一条
      const sets = change.cellChanges.map((c) => `${d.quoteIdent(c.column)} = ${valueToLiteral(c.newValue, d)}`);
      const where = pkWhere(d, pkColumns, change.originalRow);
      const psets = change.cellChanges.map((c) => `${d.quoteIdent(c.column)} = ?`);
      out.push({
        sql: `UPDATE ${qTable} SET ${sets.join(', ')} WHERE ${where}`,
        parameterized: { sql: `UPDATE ${qTable} SET ${psets.join(', ')} WHERE ${pkColumns.map((p) => `${d.quoteIdent(p)} = ?`).join(' AND ')}`, params: [...change.cellChanges.map((c) => c.newValue), ...pkColumns.map((p) => change.originalRow[p] ?? null)] },
        expectAffected: 1,
        allowZero: true,
      });
    }
  }
  return out;
}

// ─── 提交（单事务 + 逐条影响行数校验）──────────────────────

export interface CommitOutcome {
  ok: boolean;
  /** 失败时：已执行的语句数（回滚后仅用于提示）。 */
  applied: number;
  error?: string;
}

/**
 * 在单事务内按序执行提交语句并校验影响行数（§2-4）：
 * delete/insert：actual !== 1 → 中止；keyed update：actual > 1 → 中止（0 合法）。
 * 任一失败/校验不过 → rollback 后返回失败，队列由调用方保留。
 */
export async function commitInTransaction(
  connId: string,
  stmts: GeneratedStatement[],
  begin?: (connId: string, iso?: string) => Promise<string>,
  exec?: (txId: string, req: QueryRequest) => Promise<{ affected_rows: number }>,
  finalize?: (txId: string, commit: boolean) => Promise<void>,
): Promise<CommitOutcome> {
  const beginTxn = begin ?? ((cid: string) => api.beginTransaction(cid).then((t) => t.id));
  const execTxn = exec ?? ((txId: string, req: QueryRequest) =>
    api.executeInTx(txId, req).then((res) => ({ affected_rows: res.affected_rows })));
  const finalizeTxn = finalize ?? ((txId: string, commit: boolean) =>
    commit ? api.commitTransaction(txId) : api.rollbackTransaction(txId));

  if (stmts.length === 0) return { ok: true, applied: 0 };
  const txId = await beginTxn(connId);
  let applied = 0;
  try {
    for (const s of stmts) {
      const { affected_rows: actual } = await execTxn(txId, s.parameterized as unknown as QueryRequest);
      if (s.allowZero ? actual > s.expectAffected : actual !== s.expectAffected) {
        await finalizeTxn(txId, false);
        return { ok: false, applied, error: `影响行数与预期不符：预期 ${s.expectAffected}，实际 ${actual}（第 ${applied + 1} 条），已整体回滚` };
      }
      applied += 1;
    }
    await finalizeTxn(txId, true);
    return { ok: true, applied };
  } catch (e) {
    try {
      await finalizeTxn(txId, false);
    } catch { /* 回滚失败不掩盖原始错误 */ }
    return { ok: false, applied, error: e instanceof Error ? e.message : String(e) };
  }
}
