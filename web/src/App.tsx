import { useCallback, useEffect, useState } from 'react';
import * as monaco from 'monaco-editor';
import editorWorker from 'monaco-editor/editor/editor.worker?worker';
import { loader } from '@monaco-editor/react';
import ConnectionList from './components/ConnectionList';
import RedisBrowser from './components/RedisBrowser';
import SchemaBrowser from './components/SchemaBrowser';
import QueryWorkspace from './components/QueryWorkspace';
import QueryLogPanel from './components/QueryLogPanel';
import CollapsiblePane from './components/CollapsiblePane';
import CommandPalette from './components/CommandPalette';
import ShortcutPanel from './components/ShortcutPanel';
import StatusBar from './components/StatusBar';
import SettingsPanel from './components/SettingsPanel';
import ActivityBar, { type ViewId } from './components/ActivityBar';
import { onCommands, registerCommand } from './lib/commandRegistry';
import { startDragResize } from './lib/dragResize';
import type { ConnectionInfo } from './api';
import * as api from './lib/api';
import { loadStats, saveStats } from './lib/runStats';
import type { RunStat } from './lib/runStats';

self.MonacoEnvironment = {
  getWorker: () => new editorWorker(),
};
loader.config({ monaco });

const LAYOUT_KEY = 'polydb.layout.v1';
function loadView(): { activeView: ViewId; sidebarWidth: number } {
  const def = { activeView: 'explorer' as ViewId, sidebarWidth: 300 };
  try {
    const raw = localStorage.getItem(LAYOUT_KEY);
    if (!raw) return def;
    const o = JSON.parse(raw);
    const valid: ViewId[] = ['explorer', 'schema', 'log'];
    if (o && valid.includes(o.activeView)) def.activeView = o.activeView;
    if (o && typeof o.sidebarWidth === 'number' && o.sidebarWidth >= 200 && o.sidebarWidth <= 600) def.sidebarWidth = o.sidebarWidth;
  } catch { /* ignore */ }
  return def;
}
function saveView(v: { activeView: ViewId; sidebarWidth: number }) {
  try { localStorage.setItem(LAYOUT_KEY, JSON.stringify(v)); } catch { /* ignore */ }
}

function Svg({ d, fill = 'none' }: { d: string; fill?: string }) {
  return (
    <svg viewBox="0 0 24 24" width="20" height="20" fill={fill} xmlns="http://www.w3.org/2000/svg">
      <path d={d} stroke="currentColor" strokeWidth="1.5" fill="none" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

const ICONS: Record<ViewId, JSX.Element> = {
  explorer: <Svg d="M3 5h18v14H3zM9 5v14" />,
  schema: <Svg d="M3 4h18v16H3zM3 9h18M9 9v11M15 9v11" />,
  log: <Svg d="M4 5h16v14H4zM4 9h16M4 13h10" />,
};

export default function App() {
  const [serverOk, setServerOk] = useState<boolean | null>(null);
  const [serverInfo, setServerInfo] = useState<string>('');
  const [conn, setConn] = useState<ConnectionInfo | null>(null);
  const [connections, setConnections] = useState<ConnectionInfo[]>([]);
  const [context, setContext] = useState<{ sql: string; label: string; schema: string; table: string } | null>(null);
  const [autoRunToken, setAutoRunToken] = useState(0);
  const [stats, setStats] = useState<RunStat[]>(() => loadStats());
  const [cmdOpen, setCmdOpen] = useState(false);
  const [shortcutOpen, setShortcutOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [commands, setCommands] = useState<import('./lib/commandRegistry').CommandItem[]>([]);
  const [layout, setLayout] = useState(() => loadView());
  const setView = useCallback((v: ViewId) => {
    setLayout((l) => {
      const next = { ...l, activeView: v };
      saveView(next);
      return next;
    });
  }, []);

  useEffect(() => onCommands((s) => setCommands(s.commands)), []);

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey;
      const tag = document.activeElement?.tagName;
      const typing = tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || (document.activeElement?.getAttribute('contenteditable') === 'true');
      if (mod && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        setCmdOpen((v) => !v);
      } else if (mod && e.key === ',') {
        e.preventDefault();
        setSettingsOpen((v) => !v);
      } else if (mod && !e.shiftKey && ['1', '2', '3'].includes(e.key)) {
        e.preventDefault();
        const views: ViewId[] = ['explorer', 'schema', 'log'];
        setView(views[parseInt(e.key, 10) - 1]);
      } else if (mod && !e.shiftKey && e.key === '\\') {
        e.preventDefault();
        setLayout((l) => {
          const next = { ...l, sidebarWidth: 300 };
          saveView(next);
          return next;
        });
      } else if (e.key === '?' && !mod && !typing) {
        e.preventDefault();
        setShortcutOpen((v) => !v);
      } else if (e.key === 'Escape') {
        if (settingsOpen) setSettingsOpen(false);
        else if (shortcutOpen) setShortcutOpen(false);
      }
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [shortcutOpen, settingsOpen, setView]);

  const refreshConnections = useCallback(async () => {
    try { setConnections(await api.listConnections()); } catch { /* ignore */ }
  }, []);

  const checkHealth = useCallback(async () => {
    try {
      const h = await api.health();
      setServerOk(true);
      setServerInfo(`${h.status}${h.version ? ` v${h.version}` : ''}`);
      void refreshConnections();
    } catch (e) {
      setServerOk(false);
      setServerInfo(e instanceof Error ? e.message : String(e));
    }
  }, [refreshConnections]);

  useEffect(() => {
    void checkHealth();
    const t = setInterval(() => void checkHealth(), 15_000);
    return () => clearInterval(t);
  }, [checkHealth]);

  useEffect(() => {
    const h = () => setStats(loadStats());
    window.addEventListener('polydb-stats-updated', h);
    return () => window.removeEventListener('polydb-stats-updated', h);
  }, []);

  const handleSelect = (next: ConnectionInfo | null) => {
    setConn(next);
    setContext(null);
  };

  const handleSelectTable = (schema: string, table: string) => {
    setContext({
      sql: `SELECT * FROM ${schema}.${table}\nLIMIT 100;`,
      label: `${schema}.${table}`,
      schema,
      table,
    });
  };

  const handlePreviewTable = (schema: string, table: string) => {
    handleSelectTable(schema, table);
    setAutoRunToken((n) => n + 1);
  };

  const handlePrefillSql = (sql: string) => {
    setContext({ sql, label: '', schema: '', table: '' });
  };

  const handleLogPick = (targetConnId: string, sql: string) => {
    setContext({ sql, label: '', schema: '', table: '' });
    setAutoRunToken((n) => n + 1);
    if (conn?.id === targetConnId) return;
    const cached = connections.find((c) => c.id === targetConnId);
    if (cached) { setConn(cached); return; }
    void (async () => {
      try {
        const c = await api.getConnection(targetConnId);
        if (c) setConn(c);
      } catch { /* keep current conn */ }
    })();
  };

  const handleClearLog = () => {
    if (!window.confirm('清空全部查询日志（跨所有连接，不可撤销）？')) return;
    saveStats([]);
    setStats([]);
    window.dispatchEvent(new CustomEvent('polydb-stats-updated', { detail: { source: 'clear' } }));
  };

  // App-level command registrations
  useEffect(() => {
    const unregs: (() => void)[] = [];
    const themeKeys = ['vs', 'vs-dark', 'hc-black'] as const;
    const THEME_KEY = 'polydb.theme';
    const loadTheme = () => {
      try {
        const v = localStorage.getItem(THEME_KEY);
        if (v === 'vs' || v === 'vs-dark' || v === 'hc-black') return v;
      } catch { /* ignore */ }
      return 'vs';
    };

    unregs.push(registerCommand({
      id: 'app.view-explorer',
      label: '切换到资源管理器',
      category: '视图',
      hotkey: 'Ctrl+1',
      keywords: ['explorer', 'connections', '资源管理器', '连接'],
      run: () => setView('explorer'),
    }));
    unregs.push(registerCommand({
      id: 'app.view-schema',
      label: '切换到库表视图',
      category: '视图',
      hotkey: 'Ctrl+2',
      keywords: ['schema', 'tables', '数据库', '库表'],
      run: () => setView('schema'),
    }));
    unregs.push(registerCommand({
      id: 'app.view-log',
      label: '切换到查询日志',
      category: '查询日志',
      hotkey: 'Ctrl+3',
      keywords: ['log', 'history', 'stats', '查询日志'],
      run: () => setView('log'),
    }));
    unregs.push(registerCommand({
      id: 'app.reset-sidebar-width',
      label: '重置侧栏宽度',
      category: '视图',
      hotkey: 'Ctrl+\\',
      keywords: ['resize', 'width', 'sidebar', '布局', '宽度'],
      run: () => setLayout((l) => { const next = { ...l, sidebarWidth: 300 }; saveView(next); return next; }),
    }));
    unregs.push(registerCommand({
      id: 'app.reset-editor-height',
      label: '重置编辑器高度',
      category: '视图',
      keywords: ['resize', 'height', 'editor', '布局', '高度'],
      run: () => window.dispatchEvent(new CustomEvent('polydb-reset-editor-height')),
    }));
    unregs.push(registerCommand({
      id: 'app.clear-log',
      label: '清空查询日志',
      category: '查询日志',
      keywords: ['clear', 'history', 'stats'],
      enabled: () => stats.length > 0,
      run: handleClearLog,
    }));
    unregs.push(registerCommand({
      id: 'app.theme-cycle',
      label: '切换编辑器主题',
      category: '显示',
      keywords: ['theme', 'dark', 'light', 'color'],
      run: () => {
        const cur = loadTheme();
        const idx = themeKeys.indexOf(cur as typeof themeKeys[number]);
        const next = themeKeys[(idx + 1) % themeKeys.length];
        try { localStorage.setItem(THEME_KEY, next); } catch { /* ignore */ }
        window.dispatchEvent(new CustomEvent('polydb-theme-changed', { detail: { theme: next } }));
      },
    }));
    unregs.push(registerCommand({
      id: 'app.shortcuts',
      label: '显示键盘快捷键',
      category: '帮助',
      hotkey: '?',
      keywords: ['shortcut', 'key', 'help', 'keys', 'command', '快捷键'],
      run: () => { setShortcutOpen(true); },
    }));
    unregs.push(registerCommand({
      id: 'app.settings',
      label: '打开设置面板',
      category: '显示',
      hotkey: 'Ctrl+,',
      keywords: ['settings', 'config', 'preference', 'theme', '字体', '设置'],
      run: () => { setSettingsOpen(true); },
    }));
    unregs.push(registerCommand({
      id: 'app.refresh-connections',
      label: '刷新连接列表',
      category: '连接',
      keywords: ['refresh', 'reload', 'conn'],
      run: () => { void refreshConnections(); },
    }));
    for (const c of connections) {
      unregs.push(registerCommand({
        id: `conn.select.${c.id}`,
        label: `连接到 ${c.name}${c.database ? ` (${c.database})` : ''}`,
        category: '连接',
        keywords: [c.kind, c.host ?? '', c.database ?? ''],
        run: () => { setConn(c); setContext(null); },
      }));
    }
    return () => { for (const u of unregs) u(); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connections, stats.length, setView]);

  const logConns = connections.map((c) => ({ id: c.id, name: c.name, kind: c.kind }));

  return (
    <div className="app">
      <div className="app-header">
        <h1>PolyDB</h1>
        <span className="muted">Web · msgpack 控制面</span>
        <button
          className="btn-ico"
          style={{ display: 'flex', alignItems: 'center', gap: 6 }}
          onClick={() => setCmdOpen(true)}
          title="命令面板 (Ctrl+K / ?)"
        >
          <span>命令</span>
          <kbd style={{ fontFamily: 'monospace', fontSize: 10, color: 'var(--muted)', background: 'var(--bg-alt, rgba(0,0,0,0.15))', border: '1px solid var(--border)', borderRadius: 3, padding: '1px 5px' }}>Ctrl+K</kbd>
        </button>
        <span style={{ marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <span className={`status-dot ${serverOk ? 'ok' : serverOk === false ? 'err' : 'off'}`} />
          <span className="muted">后端 {serverOk === true ? '在线' : serverOk === false ? '离线' : '检测中…'} {serverInfo}</span>
        </span>
      </div>
      <div className="app-main">
        <ActivityBar
          items={[
            { id: 'explorer', label: '资源管理器 · 连接', hotkey: 'Ctrl+1', icon: ICONS.explorer, active: layout.activeView === 'explorer', badge: connections.length || undefined },
            { id: 'schema', label: '数据库 · 库表', hotkey: 'Ctrl+2', icon: ICONS.schema, active: layout.activeView === 'schema' },
            { id: 'log', label: '查询日志', hotkey: 'Ctrl+3', icon: ICONS.log, active: layout.activeView === 'log', badge: stats.length || undefined },
          ]}
          onSelect={setView}
        />
        <div
          className="pane-resize"
          title="拖拽调整侧栏宽度"
          onMouseDown={(e) => {
            const left = (e.currentTarget as HTMLElement).getBoundingClientRect().left;
            const view = layout.activeView;
            let finalWidth = layout.sidebarWidth;
            startDragResize(e, {
              cursor: 'col-resize',
              onResize: (_delta, clientX) => {
                finalWidth = Math.max(200, Math.min(600, Math.round(clientX - left)));
                setLayout((l) => ({ ...l, sidebarWidth: finalWidth }));
              },
              onEnd: () => saveView({ activeView: view, sidebarWidth: finalWidth }),
            });
          }}
        />
        <div style={{ width: layout.sidebarWidth, flexShrink: 0, minWidth: 0, overflow: 'hidden' }}>
          <div style={{ width: layout.sidebarWidth, height: '100%', display: layout.activeView === 'explorer' ? 'block' : 'none' }}>
            <ConnectionList serverOk={serverOk === true} selectedId={conn?.id ?? null} onSelect={handleSelect} />
          </div>
          <div style={{ width: layout.sidebarWidth, height: '100%', display: layout.activeView === 'schema' ? 'block' : 'none' }}>
            {conn && conn.kind !== 'redis' ? (
              <SchemaBrowser
                connId={conn.id}
                onSelectTable={handleSelectTable}
                onPreviewTable={handlePreviewTable}
                onPrefillSql={handlePrefillSql}
              />
            ) : conn && conn.kind === 'redis' ? (
              <CollapsiblePane title="Redis 键浏览" variant="main">
                <div className="pane-body"><RedisBrowser connId={conn.id} /></div>
              </CollapsiblePane>
            ) : (
              <CollapsiblePane title="库表" variant="main">
                <div className="pane-body"><div className="empty">选择连接后浏览库表。</div></div>
              </CollapsiblePane>
            )}
          </div>
          <div style={{ width: layout.sidebarWidth, height: '100%', display: layout.activeView === 'log' ? 'block' : 'none' }}>
            <CollapsiblePane title="查询日志" variant="main" defaultCollapsed={false}>
              <div style={{ height: '100%' }}>
                <QueryLogPanel
                  stats={stats}
                  connections={logConns}
                  onPick={handleLogPick}
                  onClear={handleClearLog}
                />
              </div>
            </CollapsiblePane>
          </div>
        </div>
        <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
          {conn ? (
            conn.kind === 'redis' ? (
              <RedisBrowser connId={conn.id} />
            ) : (
              <QueryWorkspace
                connId={conn.id}
                prefillSql={context?.sql}
                contextLabel={context?.label}
                contextSchema={context?.schema}
                contextTable={context?.table}
                autoRunToken={autoRunToken}
                onClearContext={context ? () => setContext(null) : undefined}
              />
            )
          ) : (
            <CollapsiblePane title="工作区" variant="main">
              <div className="pane-body">
                <div className="empty">从左侧选择或新建一个连接后开始浏览与查询。</div>
              </div>
            </CollapsiblePane>
          )}
        </div>
      </div>
      <CommandPalette open={cmdOpen} onClose={() => setCmdOpen(false)} commands={commands} />
      <ShortcutPanel open={shortcutOpen} onClose={() => setShortcutOpen(false)} />
      <SettingsPanel open={settingsOpen} onClose={() => setSettingsOpen(false)} />
      <StatusBar serverOk={serverOk} serverInfo={serverInfo} conn={conn} statsCount={stats.length} />
    </div>
  );
}
