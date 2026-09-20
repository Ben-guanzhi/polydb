import { useEffect, useMemo, useState } from 'react';
import type { ColumnInfo, ForeignKeyInfo, IndexInfo, SchemaInfo, TableInfo } from '../api';
import * as api from '../lib/api';
import CollapsiblePane from './CollapsiblePane';
import ContextMenu, { type ContextMenuEntry } from './ContextMenu';
import ERDiagram from './ERDiagram';
import { ChevronIcon, RefreshIcon, SearchIcon, TableIcon, ViewIcon } from './Icons';
import TableStructureEditor from './TableStructureEditor';

interface TableDetail {
  columns: ColumnInfo[];
  indexes: IndexInfo[];
  fks: ForeignKeyInfo[];
  ddl: string;
}

interface Props {
  connId: string;
  onSelectTable?: (schema: string, table: string) => void;
  onPreviewTable?: (schema: string, table: string) => void;
  onPrefillSql?: (sql: string) => void;
}

export default function SchemaBrowser({ connId, onSelectTable, onPreviewTable, onPrefillSql }: Props) {
  const [schemas, setSchemas] = useState<SchemaInfo[]>([]);
  const [tablesBySchema, setTablesBySchema] = useState<Record<string, TableInfo[]>>({});
  const [selSchema, setSelSchema] = useState<string | null>(null);
  const [selTable, setSelTable] = useState<string | null>(null);
  const [detail, setDetail] = useState<TableDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [filter, setFilter] = useState('');
  const [erMode, setErMode] = useState(false);
  const [kind, setKind] = useState<string>('');

  useEffect(() => {
    let cancelled = false;
    api.getConnection(connId).then((c) => { if (!cancelled) setKind(c.kind); }).catch(() => {});
    return () => { cancelled = true; };
  }, [connId]);
  const [expandedSchemas, setExpandedSchemas] = useState<Record<string, boolean>>({});
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; schema: string; table?: string } | null>(null);

  const toggleSchema = (name: string) => {
    const isOpen = !!expandedSchemas[name];
    if (isOpen) {
      setExpandedSchemas((m) => ({ ...m, [name]: false }));
      if (selSchema === name) {
        setSelTable(null);
        setDetail(null);
      }
    } else {
      setExpandedSchemas((m) => ({ ...m, [name]: true }));
      setSelSchema(name);
    }
  };

  const expandAll = () => {
    setExpandedSchemas(Object.fromEntries(schemas.map((s) => [s.name, true])));
  };

  const collapseAll = () => {
    setExpandedSchemas({});
    setSelTable(null);
    setDetail(null);
  };

  const reload = async (keepSel = true) => {
    if (!keepSel) {
      setSchemas([]);
      setTablesBySchema({});
      setSelSchema(null);
      setSelTable(null);
      setDetail(null);
      setExpandedSchemas({});
    }
    setError(null);
    setLoading(true);
    try {
      const ss = await api.listSchemas(connId);
      setSchemas(ss);
      setExpandedSchemas((prev) => {
        if (Object.keys(prev).length === 0 && ss.length > 0) return { [ss[0].name]: true };
        return prev;
      });
      if (ss.length > 0) setSelSchema((prev) => prev && ss.some((s) => s.name === prev) ? prev : ss[0].name);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void reload(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connId]);

  useEffect(() => {
    if (!selSchema || tablesBySchema[selSchema]) return;
    let cancelled = false;
    api
      .listTables(connId, selSchema)
      .then((ts) => {
        if (!cancelled) setTablesBySchema((m) => ({ ...m, [selSchema]: ts }));
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [connId, selSchema, tablesBySchema]);

  const openTable = async (table: string) => {
    if (!selSchema) return;
    setSelTable(table);
    setDetail(null);
    setError(null);
    setLoading(true);
    onSelectTable?.(selSchema, table);
    try {
      const [columns, indexes, fks, ddlRes] = await Promise.all([
        api.listColumns(connId, selSchema, table),
        api.listIndexes(connId, selSchema, table),
        api.listForeignKeys(connId, selSchema, table),
        api.getDDL(connId, selSchema, table).catch(() => ({ sql: '' })),
      ]);
      setDetail({ columns, indexes, fks, ddl: ddlRes.sql });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  const filteredTables = useMemo(() => {
    if (!selSchema) return [];
    const tables = tablesBySchema[selSchema] ?? [];
    const q = filter.trim().toLowerCase();
    if (!q) return tables;
    return tables.filter((t) => t.name.toLowerCase().includes(q));
  }, [selSchema, tablesBySchema, filter]);

  const buildMenuItems = (m: { schema: string; table?: string }): ContextMenuEntry[] => {
    if (m.table) {
      const t = m.table;
      const q = `${m.schema}.${t}`;
      return [
        { header: `${m.schema}.${t}` },
        { key: 'preview', label: '预览数据', icon: '🔍', onClick: () => onPreviewTable?.(m.schema, t) },
        '---',
        {
          key: 'gen',
          label: '生成查询',
          icon: '⚡',
          children: [
            { key: 'gen-select', label: 'SELECT *', onClick: () => onPrefillSql?.(`SELECT * FROM ${q}\nLIMIT 100;`) },
            { key: 'gen-count', label: 'SELECT COUNT(*)', onClick: () => onPrefillSql?.(`SELECT COUNT(*) FROM ${q};`) },
            { key: 'gen-insert', label: 'INSERT 模板', onClick: () => onPrefillSql?.(`INSERT INTO ${q} (col1, col2)\nVALUES (?, ?);`) },
            { key: 'gen-update', label: 'UPDATE 模板', onClick: () => onPrefillSql?.(`UPDATE ${q}\nSET col1 = ?\nWHERE id = ?;`) },
            { key: 'gen-delete', label: 'DELETE 模板', onClick: () => onPrefillSql?.(`DELETE FROM ${q}\nWHERE id = ?;`) },
          ],
        },
        '---',
        {
          key: 'copy',
          label: '复制',
          icon: '⧉',
          children: [
            { key: 'copy-name', label: '复制表名', onClick: () => { void navigator.clipboard.writeText(t).catch(() => {}); } },
            { key: 'copy-qualified', label: '复制 schema.table', onClick: () => { void navigator.clipboard.writeText(q).catch(() => {}); } },
            { key: 'copy-ddl', label: '复制 DDL', onClick: () => { void api.getDDL(connId, m.schema, t).then((r) => { if (r.sql) void navigator.clipboard.writeText(r.sql); }).catch(() => {}); } },
          ],
        },
      ];
    }
    return [
      { header: m.schema },
      { key: 'select-all', label: '生成 SELECT（全部表）', icon: '⚡', onClick: () => { void api.listTables(connId, m.schema).then((ts) => { const lines = ts.filter((x) => x.type === 'table').map((x) => `SELECT * FROM ${m.schema}.${x.name}\nLIMIT 10;`); if (lines.length) onPrefillSql?.(lines.join('\n\n')); }).catch(() => {}); } },
      { key: 'copy-schema', label: '复制 schema 名', icon: '⧉', onClick: () => { void navigator.clipboard.writeText(m.schema).catch(() => {}); } },
    ];
  };

  const actions = (
    <>
      <button className="btn-ico" title="展开全部" onClick={expandAll} disabled={schemas.length === 0}>
        <ChevronIcon dir="down" />
      </button>
      <button className="btn-ico" title="折叠全部" onClick={collapseAll} disabled={schemas.length === 0}>
        <ChevronIcon dir="right" />
      </button>
      <button className="btn-ico" title="刷新" onClick={() => void reload(true)}>
        <RefreshIcon />
      </button>
      <button
        className={`btn-ico ${erMode ? 'active' : ''}`}
        title="ER 图（当前 schema 的表与外键关系）"
        onClick={() => setErMode((v) => !v)}
        disabled={selSchema == null}
      >
        🕸
      </button>
    </>
  );

  return (
    <CollapsiblePane title="库表结构" variant="md" actions={actions}>
      <div className="pane-body tree" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {error && <div className="error-box">{error}</div>}
        {erMode && selSchema && (
          <ERDiagram
            connId={connId}
            schema={selSchema}
            tables={tablesBySchema[selSchema] ?? []}
            onSelectTable={(t) => {
              setErMode(false);
              void openTable(t);
            }}
          />
        )}
        {erMode && !selSchema && <div className="empty">选择一个 schema 查看 ER 图</div>}
        {!erMode && schemas.length === 0 && !loading && <div className="empty">无 schema 数据</div>}
        {!erMode &&
          schemas.map((s) => {
          const open = !!expandedSchemas[s.name];
          return (
            <div key={s.name}>
              <div
                className="group"
                onClick={() => toggleSchema(s.name)}
                onContextMenu={(e) => {
                  e.preventDefault();
                  e.stopPropagation();
                  setCtxMenu({ x: e.clientX, y: e.clientY, schema: s.name });
                }}
              >
                <span className="group-chevron"><ChevronIcon dir={open ? 'down' : 'right'} /></span>
                <span style={{ textTransform: 'none', letterSpacing: 0, fontSize: 12, fontWeight: 600 }}>
                  {s.name}
                </span>
                {tablesBySchema[s.name] && (
                  <span className="group-count">{tablesBySchema[s.name].length}</span>
                )}
              </div>
              {open && (
                <>
                  <div className="search" style={{ margin: '2px 0 4px 22px' }}>
                    <span className="search-ico"><SearchIcon /></span>
                    <input
                      value={filter}
                      onChange={(e) => setFilter(e.target.value)}
                      placeholder="过滤表名"
                      style={{ fontSize: 11, padding: '2px 6px', paddingLeft: 22 }}
                    />
                  </div>
                  {!tablesBySchema[s.name] && <div className="muted" style={{ padding: '2px 8px' }}>加载中…</div>}
                  {tablesBySchema[s.name] &&
                    (filteredTables.length === 0 ? (
                      <div className="muted" style={{ padding: '2px 8px' }}>{filter ? '（无匹配）' : '（无表）'}</div>
                    ) : (
                      filteredTables.map((t) => (
                        <div
                          key={t.name}
                          className={`leaf ${selTable === t.name ? 'selected' : ''}`}
                          onClick={() => void openTable(t.name)}
                          onDoubleClick={() => { void openTable(t.name); onPreviewTable?.(selSchema!, t.name); }}
                          onContextMenu={(e) => {
                            e.preventDefault();
                            e.stopPropagation();
                            setCtxMenu({ x: e.clientX, y: e.clientY, schema: selSchema!, table: t.name });
                          }}
                          title={`${t.type} · ${t.name}\n单击选中，双击预览数据，右键菜单`}
                        >
                          {t.type === 'view' ? <ViewIcon /> : <TableIcon />}
                          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{t.name}</span>
                          {t.row_count != null && (
                            <span className="muted mono" style={{ marginLeft: 'auto', fontSize: 10 }}>{t.row_count}</span>
                          )}
                        </div>
                      ))
                    )
                  )}
                </>
              )}
            </div>
          );
        })}
        {selTable && detail && (
          <div style={{ borderTop: '1px solid var(--border)', marginTop: 8, paddingTop: 8 }}>
            <div className="group" style={{ display: 'flex', alignItems: 'center', gap: 6, textTransform: 'none', letterSpacing: 0, cursor: 'default' }}>
              <TableIcon /> {selTable}
            </div>
            <div className="group" style={{ paddingTop: 4 }}>列</div>
            <table className="detail-table">
              <thead>
                <tr><th>列</th><th>类型</th><th>可空</th><th>默认值</th><th>键</th></tr>
              </thead>
              <tbody>
                {(detail.columns ?? []).map((c) => (
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
            {(detail.indexes ?? []).length > 0 && (
              <>
                <div className="group" style={{ paddingTop: 6 }}>索引</div>
                <div className="muted mono" style={{ padding: '0 8px' }}>
                  {detail.indexes.map((i) => `${i.name}${i.unique ? ' (unique)' : ''}: ${i.columns.map((c) => c.name).join(', ')}`).join('\n')}
                </div>
              </>
            )}
            {(detail.fks ?? []).length > 0 && (
              <>
                <div className="group" style={{ paddingTop: 6 }}>外键</div>
                <div className="muted mono" style={{ padding: '0 8px' }}>
                  {detail.fks.map((fk) => `${fk.name}: ${fk.columns.join(', ')} → ${fk.referenced_schema}.${fk.referenced_table}(${fk.referenced_columns.join(', ')})`).join('\n')}
                </div>
              </>
            )}
            {kind && (
              <>
                <div className="group" style={{ paddingTop: 6 }}>结构编辑</div>
                <TableStructureEditor
                  connId={connId}
                  kind={kind}
                  schema={selSchema ?? ''}
                  table={selTable}
                  columns={detail.columns ?? []}
                  indexes={detail.indexes ?? []}
                  fks={detail.fks ?? []}
                  onApplied={() => { void openTable(selTable); }}
                />
              </>
            )}
            <div className="group" style={{ paddingTop: 6 }}>DDL</div>
            <pre className="mono" style={{ fontSize: 11, margin: '0 8px', overflow: 'auto', maxHeight: 160, whiteSpace: 'pre-wrap' }}>
              {detail.ddl || '（无 DDL 信息）'}
            </pre>
          </div>
        )}
        {ctxMenu && (
          <ContextMenu
            x={ctxMenu.x}
            y={ctxMenu.y}
            onClose={() => setCtxMenu(null)}
            items={buildMenuItems(ctxMenu)}
          />
        )}
      </div>
    </CollapsiblePane>
  );
}

