import { useEffect, useMemo, useState } from 'react';
import type { SchemaInfo, TableInfo } from '../api';
import * as api from '../lib/api';
import CollapsiblePane from './CollapsiblePane';
import ContextMenu, { type ContextMenuEntry } from './ContextMenu';
import { ChevronIcon, RefreshIcon, SearchIcon, TableIcon, ViewIcon } from './Icons';

// U1 起侧栏瘦身为纯导航：表详情改由主区 TableTab（数据/结构/关系 rails）承载。

interface Props {
  connId: string;
  onOpenTable: (schema: string, table: string) => void;
  onPrefillSql?: (sql: string) => void;
}

export default function SchemaBrowser({ connId, onOpenTable, onPrefillSql }: Props) {
  const [schemas, setSchemas] = useState<SchemaInfo[]>([]);
  const [tablesBySchema, setTablesBySchema] = useState<Record<string, TableInfo[]>>({});
  const [selSchema, setSelSchema] = useState<string | null>(null);
  const [selTable, setSelTable] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [filter, setFilter] = useState('');
  const [expandedSchemas, setExpandedSchemas] = useState<Record<string, boolean>>({});
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; schema: string; table?: string } | null>(null);

  const toggleSchema = (name: string) => {
    const isOpen = !!expandedSchemas[name];
    if (isOpen) {
      setExpandedSchemas((m) => ({ ...m, [name]: false }));
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
  };

  const reload = async (keepSel = true) => {
    setTablesBySchema({});
    if (!keepSel) {
      setSchemas([]);
      setSelSchema(null);
      setSelTable(null);
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
        { key: 'open', label: '打开表', icon: '📋', onClick: () => onOpenTable(m.schema, t) },
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
    </>
  );

  return (
    <CollapsiblePane title="库表结构" variant="md" actions={actions}>
      <div className="pane-body tree" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {error && <div className="error-box">{error}</div>}
        {!loading && schemas.length === 0 && <div className="empty">无 schema 数据</div>}
        {schemas.map((s) => {
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
                          className={`leaf ${selSchema === s.name && selTable === t.name ? 'selected' : ''}`}
                          onClick={() => { setSelSchema(s.name); setSelTable(t.name); }}
                          onDoubleClick={() => onOpenTable(s.name, t.name)}
                          onKeyDown={(e) => { if (e.key === 'Enter') onOpenTable(s.name, t.name); }}
                          tabIndex={0}
                          onContextMenu={(e) => {
                            e.preventDefault();
                            e.stopPropagation();
                            setCtxMenu({ x: e.clientX, y: e.clientY, schema: s.name, table: t.name });
                          }}
                          title={`${t.type} · ${t.name}\n单击选中，双击打开表，右键菜单`}
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
