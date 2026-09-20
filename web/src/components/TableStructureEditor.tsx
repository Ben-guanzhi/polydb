// M16：表结构可视化编辑器——基于 list_columns/indexes/fks 生成 ALTER/DDL 语句，
// 预览后在事务内执行（失败自动 rollback）。DDL 生成与方言限制见 lib/alterSql。
import { useMemo, useState } from 'react';
import type { ColumnInfo, ForeignKeyInfo, IndexInfo, TransactionInfo } from '../api';
import * as api from '../lib/api';
import {
  UnsupportedAlterError,
  addColumnSql,
  addForeignKeySql,
  addIndexSql,
  dialectOf,
  dropColumnSql,
  dropForeignKeySql,
  dropIndexSql,
} from '../lib/alterSql';

type Op = 'add-column' | 'drop-column' | 'add-index' | 'drop-index' | 'add-fk' | 'drop-fk';

const OPS: Array<{ key: Op; label: string }> = [
  { key: 'add-column', label: '+ 列' },
  { key: 'drop-column', label: '− 列' },
  { key: 'add-index', label: '+ 索引' },
  { key: 'drop-index', label: '− 索引' },
  { key: 'add-fk', label: '+ 外键' },
  { key: 'drop-fk', label: '− 外键' },
];

interface Props {
  connId: string;
  kind: string;
  schema: string;
  table: string;
  columns: ColumnInfo[];
  indexes: IndexInfo[];
  fks: ForeignKeyInfo[];
  onApplied?: () => void;
}

const inputStyle: React.CSSProperties = {
  fontSize: 11, padding: '2px 6px', border: '1px solid var(--border)',
  borderRadius: 3, background: 'var(--bg)', color: 'var(--fg)',
};

export default function TableStructureEditor({ connId, kind, schema, table, columns, indexes, fks, onApplied }: Props) {
  const [op, setOp] = useState<Op>('add-column');
  const [colName, setColName] = useState('');
  const [colType, setColType] = useState('TEXT');
  const [colNullable, setColNullable] = useState(true);
  const [colDefault, setColDefault] = useState('');
  const [colPk, setColPk] = useState(false);
  const [dropCol, setDropCol] = useState('');
  const [idxName, setIdxName] = useState('');
  const [idxCols, setIdxCols] = useState('');
  const [idxUnique, setIdxUnique] = useState(false);
  const [dropIdx, setDropIdx] = useState('');
  const [fkName, setFkName] = useState('');
  const [fkCols, setFkCols] = useState('');
  const [fkRefTable, setFkRefTable] = useState('');
  const [fkRefCols, setFkRefCols] = useState('');
  const [fkOnDelete, setFkOnDelete] = useState('');
  const [dropFk, setDropFk] = useState('');
  const [running, setRunning] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  const dialect = dialectOf(kind);

  const sql = useMemo<{ text: string; error?: string }>(() => {
    const parseCols = (s: string) => s.split(',').map((x) => x.trim()).filter(Boolean);
    try {
      switch (op) {
        case 'add-column':
          return {
            text: addColumnSql(dialect, schema, table, {
              name: colName.trim(),
              type: colType.trim() || 'TEXT',
              nullable: colNullable,
              defaultExpr: colDefault.trim() || undefined,
              primaryKey: colPk,
            }),
          };
        case 'drop-column':
          return { text: dropColumnSql(dialect, schema, table, dropCol) };
        case 'add-index':
          return {
            text: addIndexSql(dialect, schema, table, {
              name: idxName.trim() || `idx_${table}_${idxCols.replace(/[^a-z0-9]/gi, '_')}`,
              columns: parseCols(idxCols),
              unique: idxUnique,
            }),
          };
        case 'drop-index':
          return { text: dropIndexSql(dialect, schema, table, dropIdx) };
        case 'add-fk':
          return {
            text: addForeignKeySql(dialect, schema, table, {
              name: fkName.trim() || `fk_${table}_to_${fkRefTable}`,
              columns: parseCols(fkCols),
              refSchema: schema,
              refTable: fkRefTable.trim(),
              refColumns: parseCols(fkRefCols),
              onDelete: fkOnDelete || undefined,
            }),
          };
        case 'drop-fk':
          return { text: dropForeignKeySql(dialect, schema, table, dropFk) };
      }
    } catch (e) {
      if (e instanceof UnsupportedAlterError) return { text: '', error: e.message };
      throw e;
    }
  }, [op, dialect, schema, table, colName, colType, colNullable, colDefault, colPk, dropCol, idxName, idxCols, idxUnique, dropIdx, fkName, fkCols, fkRefTable, fkRefCols, fkOnDelete, dropFk]);

  const ready = sql.text !== '' && !sql.error;

  const run = async () => {
    if (!ready || running) return;
    setRunning(true);
    setMsg(null);
    let txn: TransactionInfo | null = null;
    try {
      txn = await api.beginTransaction(connId);
      await api.executeInTx(txn.id, { sql: sql.text, connection_id: connId });
      await api.commitTransaction(txn.id);
      setMsg(`✅ 已执行：${sql.text}`);
      onApplied?.();
    } catch (e) {
      if (txn) {
        try { await api.rollbackTransaction(txn.id); } catch { /* 事务可能已结束 */ }
      }
      setMsg(`❌ 执行失败（已回滚）：${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setRunning(false);
    }
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6, padding: '6px 4px' }}>
      <div className="row" style={{ gap: 4, flexWrap: 'wrap' }}>
        {OPS.map((o) => (
          <button key={o.key} className={`btn ${op === o.key ? 'primary' : ''}`} style={{ fontSize: 11, padding: '2px 8px' }}
            onClick={() => { setOp(o.key); setMsg(null); }}>
            {o.label}
          </button>
        ))}
      </div>

      {op === 'add-column' && (
        <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
          <input placeholder="列名" value={colName} onChange={(e) => setColName(e.target.value)} style={{ ...inputStyle, width: 110 }} />
          <input placeholder="类型（TEXT/INTEGER…）" value={colType} onChange={(e) => setColType(e.target.value)} style={{ ...inputStyle, width: 130 }} />
          <input placeholder="DEFAULT（可空）" value={colDefault} onChange={(e) => setColDefault(e.target.value)} style={{ ...inputStyle, width: 100 }} />
          <label style={{ fontSize: 11, color: 'var(--muted)' }}><input type="checkbox" checked={colNullable} onChange={(e) => setColNullable(e.target.checked)} /> 可空</label>
          <label style={{ fontSize: 11, color: 'var(--muted)' }}><input type="checkbox" checked={colPk} onChange={(e) => setColPk(e.target.checked)} /> PK</label>
        </div>
      )}
      {op === 'drop-column' && (
        <select value={dropCol} onChange={(e) => setDropCol(e.target.value)} style={{ ...inputStyle, width: 220 }}>
          <option value="">选择要删除的列…</option>
          {columns.map((c) => <option key={c.name} value={c.name}>{c.name}{c.is_primary_key ? ' (PK)' : ''}</option>)}
        </select>
      )}
      {op === 'add-index' && (
        <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
          <input placeholder="索引名（可空=自动）" value={idxName} onChange={(e) => setIdxName(e.target.value)} style={{ ...inputStyle, width: 140 }} />
          <input placeholder="列（逗号分隔）" value={idxCols} onChange={(e) => setIdxCols(e.target.value)} style={{ ...inputStyle, width: 160 }} />
          <label style={{ fontSize: 11, color: 'var(--muted)' }}><input type="checkbox" checked={idxUnique} onChange={(e) => setIdxUnique(e.target.checked)} /> 唯一</label>
        </div>
      )}
      {op === 'drop-index' && (
        <select value={dropIdx} onChange={(e) => setDropIdx(e.target.value)} style={{ ...inputStyle, width: 220 }}>
          <option value="">选择要删除的索引…</option>
          {indexes.map((i) => <option key={i.name} value={i.name}>{i.name} ({i.columns.map((c) => c.name).join(',')})</option>)}
        </select>
      )}
      {op === 'add-fk' && (
        <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
          <input placeholder="约束名（可空=自动）" value={fkName} onChange={(e) => setFkName(e.target.value)} style={{ ...inputStyle, width: 130 }} />
          <input placeholder="本表列（逗号分隔）" value={fkCols} onChange={(e) => setFkCols(e.target.value)} style={{ ...inputStyle, width: 140 }} />
          <input placeholder="引用表" value={fkRefTable} onChange={(e) => setFkRefTable(e.target.value)} style={{ ...inputStyle, width: 110 }} />
          <input placeholder="引用列" value={fkRefCols} onChange={(e) => setFkRefCols(e.target.value)} style={{ ...inputStyle, width: 110 }} />
          <select value={fkOnDelete} onChange={(e) => setFkOnDelete(e.target.value)} style={inputStyle}>
            <option value="">ON DELETE（默认）</option>
            {['CASCADE', 'SET NULL', 'RESTRICT', 'NO ACTION'].map((a) => <option key={a} value={a}>{a}</option>)}
          </select>
        </div>
      )}
      {op === 'drop-fk' && (
        <select value={dropFk} onChange={(e) => setDropFk(e.target.value)} style={{ ...inputStyle, width: 220 }}>
          <option value="">选择要删除的外键…</option>
          {fks.map((f) => <option key={f.name} value={f.name}>{f.name} → {f.referenced_table}({f.referenced_columns.join(',')})</option>)}
        </select>
      )}

      <pre className="mono" style={{
        margin: 0, padding: '6px 8px', fontSize: 11, overflow: 'auto', whiteSpace: 'pre-wrap',
        border: `1px solid ${sql.error ? 'var(--danger, #ef4444)' : 'var(--border)'}`,
        background: sql.error ? 'rgba(239,68,68,0.08)' : 'var(--bg)',
        color: sql.error ? 'var(--danger, #ef4444)' : undefined,
        maxWidth: 480,
      }}>
        {sql.error || sql.text || '（填写表单生成 SQL）'}
      </pre>

      <div className="row" style={{ gap: 8 }}>
        <button className="btn primary" disabled={!ready || running} onClick={() => void run()} title="在事务内执行，失败自动回滚">
          {running ? '执行中…' : '▶ 执行（事务）'}
        </button>
        {msg && <span className="muted" style={{ fontSize: 11, wordBreak: 'break-all' }}>{msg}</span>}
      </div>
    </div>
  );
}
