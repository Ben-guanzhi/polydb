import { useState } from 'react';
import type { OpenTab, OpenTabsState } from '../lib/openTabs';
import { WORKBENCH_ID } from '../lib/openTabs';
import ContextMenu, { type ContextMenuEntry } from './ContextMenu';
import { QueryIcon, TableIcon } from './Icons';

// U1 对象标签条：workbench + 表 tabs（参考 TablePro 窗口 tab 制）。
// U6.3 右键菜单：关闭其他/关闭所有/复制名/在查询中打开。

interface Props {
  state: OpenTabsState;
  onSelect: (id: string) => void;
  onClose: (id: string) => void;
  onMutate: (fn: (s: OpenTabsState) => OpenTabsState) => void;
  onOpenInQuery?: (sql: string) => void;
}

function tabTitle(t: OpenTab): { label: string; tip: string } {
  if (t.kind === 'workbench') return { label: '查询工作台', tip: 'SQL 查询工作台' };
  return { label: t.table, tip: `${t.schema}.${t.table}` };
}

function copyText(s: string): void {
  void navigator.clipboard.writeText(s).catch(() => {});
}

function closeOthers(s: OpenTabsState, id: string): OpenTabsState {
  const tabs = s.tabs.filter((t) => t.id === WORKBENCH_ID || t.id === id);
  const activeId = tabs.some((t) => t.id === s.activeId) ? s.activeId : id;
  return { tabs, activeId };
}

function closeAllTables(s: OpenTabsState): OpenTabsState {
  return { tabs: s.tabs.filter((t) => t.kind === 'workbench'), activeId: WORKBENCH_ID };
}

export default function ObjectTabStrip({ state, onSelect, onClose, onMutate, onOpenInQuery }: Props) {
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; tab: OpenTab } | null>(null);

  const buildItems = (t: OpenTab): ContextMenuEntry[] => {
    if (t.kind === 'workbench') {
      return state.tabs.length > 1
        ? [{ header: '查询工作台' }, '---', { key: 'close-all', label: '关闭所有表标签', icon: '✕', onClick: () => onMutate(closeAllTables) }]
        : [];
    }
    const q = `${t.schema}.${t.table}`;
    return [
      { header: q },
      { key: 'open-query', label: '在查询中打开', icon: '⚡', onClick: () => onOpenInQuery?.(`SELECT * FROM ${q}\nLIMIT 100;`) },
      {
        key: 'copy',
        label: '复制',
        icon: '⧉',
        children: [
          { key: 'copy-name', label: '复制表名', onClick: () => copyText(t.table) },
          { key: 'copy-qualified', label: '复制 schema.table', onClick: () => copyText(q) },
        ],
      },
      '---',
      { key: 'close', label: '关闭标签', onClick: () => onClose(t.id) },
      { key: 'close-others', label: '关闭其他标签', disabled: !state.tabs.some((x) => x.kind !== 'workbench' && x.id !== t.id), onClick: () => onMutate((s) => closeOthers(s, t.id)) },
      { key: 'close-all', label: '关闭所有表标签', onClick: () => onMutate(closeAllTables) },
    ];
  };

  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'stretch',
        gap: 2,
        padding: '4px 6px 0',
        background: 'var(--bg)',
        borderBottom: '1px solid var(--border)',
        overflowX: 'auto',
        overflowY: 'hidden',
        flexShrink: 0,
      }}
    >
      {state.tabs.map((t) => {
        const active = t.id === state.activeId;
        const { label, tip } = tabTitle(t);
        return (
          <div
            key={t.id}
            onClick={() => onSelect(t.id)}
            title={`${tip}\n中键/点 × 关闭，右键更多操作`}
            onMouseUp={(e) => {
              if (e.button === 1 && t.kind !== 'workbench') {
                e.preventDefault();
                onClose(t.id);
              }
            }}
            onContextMenu={(e) => {
              e.preventDefault();
              e.stopPropagation();
              setCtxMenu({ x: e.clientX, y: e.clientY, tab: t });
            }}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 6,
              padding: '4px 10px',
              fontSize: 12,
              cursor: 'pointer',
              borderRadius: '4px 4px 0 0',
              background: active ? 'var(--panel)' : 'transparent',
              border: `1px solid ${active ? 'var(--border)' : 'transparent'}`,
              borderBottom: active ? '1px solid var(--panel)' : '1px solid transparent',
              color: active ? 'var(--text)' : 'var(--muted)',
              fontWeight: active ? 600 : 400,
              whiteSpace: 'nowrap',
              maxWidth: 220,
            }}
          >
            <span className="icon" style={{ width: 12, height: 12, color: active ? 'var(--accent)' : 'var(--muted)' }}>
              {t.kind === 'workbench' ? <QueryIcon /> : <TableIcon />}
            </span>
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{label}</span>
            {t.kind !== 'workbench' && (
              <span
                onClick={(e) => {
                  e.stopPropagation();
                  onClose(t.id);
                }}
                title="关闭标签"
                style={{ padding: '0 3px', color: 'var(--muted)', borderRadius: 2, fontSize: 14, lineHeight: 1, cursor: 'pointer' }}
                onMouseEnter={(e) => (e.currentTarget.style.color = 'var(--danger)')}
                onMouseLeave={(e) => (e.currentTarget.style.color = 'var(--muted)')}
              >
                ×
              </span>
            )}
          </div>
        );
      })}
      {ctxMenu && (
        <ContextMenu
          x={ctxMenu.x}
          y={ctxMenu.y}
          items={buildItems(ctxMenu.tab)}
          onClose={() => setCtxMenu(null)}
        />
      )}
    </div>
  );
}
