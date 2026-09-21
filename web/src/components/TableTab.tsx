import { useCallback, useEffect, useState, type CSSProperties } from 'react';
import type { ColumnInfo, ForeignKeyInfo, IndexInfo, TableInfo } from '../api';
import * as api from '../lib/api';
import ERDiagram from './ERDiagram';
import TableDataView from './TableDataView';
import TableOverview from './TableOverview';
import TableStructureEditor from './TableStructureEditor';

// U1 表 tab（参考 TablePro 表窗口 rails）：Overview / Content / Structure / Relations。

type Rail = 'overview' | 'content' | 'structure' | 'relations';
const RAILS: { id: Rail; label: string }[] = [
  { id: 'overview', label: '概览' },
  { id: 'content', label: '数据' },
  { id: 'structure', label: '结构' },
  { id: 'relations', label: '关系' },
];

interface TableDetail {
  columns: ColumnInfo[];
  indexes: IndexInfo[];
  fks: ForeignKeyInfo[];
  ddl: string;
}

function useTableDetail(connId: string, schema: string, table: string) {
  const [detail, setDetail] = useState<TableDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [reloadSeq, setReloadSeq] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    Promise.all([
      api.listColumns(connId, schema, table),
      api.listIndexes(connId, schema, table),
      api.listForeignKeys(connId, schema, table),
      api.getDDL(connId, schema, table).catch(() => ({ sql: '' })),
    ])
      .then(([columns, indexes, fks, ddlRes]) => {
        if (!cancelled) setDetail({ columns, indexes, fks, ddl: ddlRes.sql });
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [connId, schema, table, reloadSeq]);

  return { detail, error, loading, reload: () => setReloadSeq((n) => n + 1) };
}

// schema 级全量 FK 缓存（入向关系需要扫全 schema），按 conn::schema 记忆。
const schemaFkCache = new Map<string, { table: string; schema: string; fks: ForeignKeyInfo[] }[]>();

async function loadSchemaFks(connId: string, schema: string) {
  const key = `${connId}::${schema}`;
  const hit = schemaFkCache.get(key);
  if (hit) return hit;
  const tables = await api.listTables(connId, schema);
  const limited = tables.slice(0, 300);
  const rows = await Promise.all(
    limited.map(async (t) => {
      const fks = await api.listForeignKeys(connId, schema, t.name).catch(() => [] as ForeignKeyInfo[]);
      return { table: t.name, schema, fks };
    }),
  );
  schemaFkCache.set(key, rows);
  return rows;
}

const railBtn: CSSProperties = {
  fontFamily: 'inherit', fontSize: 12, padding: '4px 12px', cursor: 'pointer',
  border: '1px solid var(--border)', borderRadius: 4,
  background: 'var(--bg)', color: 'var(--muted)',
};
const railBtnActive: CSSProperties = {
  ...railBtn,
  background: 'var(--accent)', borderColor: 'var(--accent)', color: '#fff', fontWeight: 600,
};

interface Props {
  connId: string;
  kind: string;
  schema: string;
  table: string;
  onNavigateTable: (schema: string, table: string) => void;
  onOpenInQuery: (sql: string) => void;
  readOnly?: boolean;
  // U6.2 FK 跳转带入的一次性等值过滤
  preset?: { column: string; op: 'eq'; value: string | number } | null;
  onPresetConsumed?: () => void;
  onOpenFiltered?: (schema: string, table: string, cond: { column: string; op: 'eq'; value: string | number }) => void;
}

export default function TableTab({ connId, kind, schema, table, onNavigateTable, onOpenInQuery, readOnly, preset, onPresetConsumed, onOpenFiltered }: Props) {
  const [rail, setRail] = useState<Rail>('content');
  const { detail, error, loading, reload } = useTableDetail(connId, schema, table);
  const [tables, setTables] = useState<TableInfo[]>([]);
  const [schemaFks, setSchemaFks] = useState<{ table: string; schema: string; fks: ForeignKeyInfo[] }[]>([]);
  const [showEr, setShowEr] = useState(false);
  const [fkError, setFkError] = useState<string | null>(null);

  useEffect(() => {
    api.listTables(connId, schema).then(setTables).catch(() => {});
  }, [connId, schema]);

  const reloadFks = useCallback(() => {
    loadSchemaFks(connId, schema).then(setSchemaFks).catch((e: unknown) => {
      setFkError(e instanceof Error ? e.message : String(e));
    });
  }, [connId, schema]);

  useEffect(() => {
    if (rail === 'relations' && schemaFks.length === 0) reloadFks();
  }, [rail, schemaFks.length, reloadFks]);

  const inbound = schemaFks
    .flatMap((row) => row.fks.filter((fk) => fk.referenced_table === table).map((fk) => ({ from: row, fk })))
    .filter((x) => !(x.from.table === table && x.from.schema === schema));

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}>
      {/* 面包屑 + rails 工具行 */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '5px 10px', borderBottom: '1px solid var(--border)', flexShrink: 0 }}>
        <span className="muted" style={{ fontSize: 11 }}>
          {schema} <span style={{ opacity: 0.6 }}>»</span>
        </span>
        <span className="mono" style={{ fontSize: 12, fontWeight: 600 }}>{table}</span>
        <span style={{ display: 'inline-flex', gap: 4, marginLeft: 10 }}>
          {RAILS.map((r) => (
            <button key={r.id} style={rail === r.id ? railBtnActive : railBtn} onClick={() => setRail(r.id)}>
              {r.label}
            </button>
          ))}
        </span>
        <button
          className="btn-ico"
          style={{ marginLeft: 'auto', fontSize: 11 }}
          title="在工作台生成 SELECT 查询"
          onClick={() => onOpenInQuery(`SELECT * FROM ${schema}.${table}\nLIMIT 100;`)}
        >
          在查询中打开
        </button>
        <button className="btn-ico" style={{ fontSize: 11 }} title="刷新表结构" onClick={() => { reload(); schemaFkCache.delete(`${connId}::${schema}`); reloadFks(); }}>
          刷新
        </button>
      </div>
      {error && <div className="error-box">{error}</div>}
      <div style={{ flex: 1, minHeight: 0, overflow: rail === 'content' ? 'hidden' : 'auto' }}>
        {rail === 'content' && (
          <TableDataView
            connId={connId}
            kind={kind as never}
            schema={schema}
            table={table}
            onOpenSql={onNavigateTable}
            readOnly={readOnly}
            initialCond={preset ?? null}
            onInitialCondConsumed={onPresetConsumed}
            onOpenFiltered={onOpenFiltered}
          />
        )}
        {rail === 'overview' && detail && (
          <TableOverview
            connId={connId}
            kind={kind as never}
            schema={schema}
            table={table}
            columns={detail.columns}
            indexCount={detail.indexes.length}
            fkOutCount={detail.fks.length}
            pkCols={detail.columns.filter((c) => c.is_primary_key).map((c) => c.name)}
          />
        )}
        {rail === 'structure' && detail && (
          <div style={{ padding: 10, display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div>
              <div className="group">列</div>
              <table className="detail-table">
                <thead>
                  <tr><th>列</th><th>类型</th><th>可空</th><th>默认值</th><th>键</th></tr>
                </thead>
                <tbody>
                  {detail.columns.map((c) => (
                    <tr key={c.name}>
                      <td className="mono">{c.name}</td>
                      <td className="mono">{c.data_type}</td>
                      <td>{c.nullable ? '✓' : ''}</td>
                      <td className="mono">{c.default_value ?? ''}</td>
                      <td>
                        {c.is_primary_key ? 'PK ' : ''}
                        {c.is_auto_increment ? '+AI' : ''}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {detail.indexes.length > 0 && (
              <div>
                <div className="group">索引</div>
                {detail.indexes.map((i) => (
                  <div key={i.name} className="muted mono" style={{ fontSize: 12, padding: '1px 8px' }}>
                    {i.name}{i.unique ? ' (unique)' : ''}: {i.columns.map((c) => c.name).join(', ')}
                  </div>
                ))}
              </div>
            )}
            <div>
              <div className="group">结构编辑</div>
              <TableStructureEditor
                connId={connId}
                kind={kind}
                schema={schema}
                table={table}
                columns={detail.columns}
                indexes={detail.indexes}
                fks={detail.fks}
                onApplied={() => reload()}
              />
            </div>
            <div>
              <div className="group">DDL</div>
              <pre className="mono" style={{ fontSize: 11, margin: '4px 8px', overflow: 'auto', maxHeight: 280, whiteSpace: 'pre-wrap' }}>
                {detail.ddl || '（无 DDL 信息）'}
              </pre>
            </div>
            {loading && <div className="muted">加载中…</div>}
          </div>
        )}
        {rail === 'relations' && detail && (
          <div style={{ padding: 10 }}>
            {fkError && <div className="error-box">{fkError}</div>}
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
              <div className="group" style={{ padding: 0 }}>外键（出向 {detail.fks.length}）</div>
              <button className="btn-ico" style={{ fontSize: 11 }} onClick={() => setShowEr((v) => !v)}>
                {showEr ? '收起 ER 图' : '🕸 Schema ER 图'}
              </button>
            </div>
            {showEr && (
              <ERDiagram connId={connId} schema={schema} tables={tables} onSelectTable={(t) => onNavigateTable(schema, t)} />
            )}
            {detail.fks.length === 0 && <div className="empty">本表无出向外键。</div>}
            {detail.fks.map((fk) => (
              <div key={fk.name} className="list-item" style={{ marginBottom: 2 }} onClick={() => onNavigateTable(fk.referenced_schema, fk.referenced_table)}>
                <span className="mono" style={{ fontSize: 12 }}>
                  {fk.name}: {fk.columns.join(', ')} → <b>{fk.referenced_schema}.{fk.referenced_table}</b>({fk.referenced_columns.join(', ')})
                </span>
                <span className="meta">跳转</span>
              </div>
            ))}
            <div className="group" style={{ marginTop: 12 }}>被引用（入向 {inbound.length}，扫描 schema 全表）</div>
            {inbound.length === 0 && <div className="empty">同 schema 内无其他表通过外键引用本表。</div>}
            {inbound.map((x) => (
              <div
                key={`${x.from.table}.${x.fk.name}`}
                className="list-item"
                style={{ marginBottom: 2 }}
                onClick={() => onNavigateTable(x.from.schema, x.from.table)}
              >
                <span className="mono" style={{ fontSize: 12 }}>
                  <b>{x.from.table}</b>.{x.fk.columns.join(', ')} → {table}({x.fk.referenced_columns.join(', ')})
                </span>
                <span className="meta">跳转</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
