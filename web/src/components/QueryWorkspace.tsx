import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react';
import Editor, { type OnMount } from '@monaco-editor/react';
import * as monacoNS from 'monaco-editor';
import { KeyCode, KeyMod, Range } from 'monaco-editor';
import type { QueryResult, ResultColumn } from '../api';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import * as exporters from '../lib/exporters';
import * as ws from '../lib/ws';
import { registerSqlCompletionGlobal, setSqlCompletionConnId } from '../lib/sqlCompletion';
import { buildExplainSql, isExplainResult, renderExplain, type ExplainNode, type ExplainRender } from '../lib/explain';
import { addStat, hashSql, loadStats, summarize } from '../lib/runStats';
import { registerCommand } from '../lib/commandRegistry';
import { publishEditorStatus, clearEditorStatus } from '../lib/statusBus';
import { loadSettings } from '../lib/settings';
import { startDragResize } from '../lib/dragResize';
import { splitSql, countParams as countSqlParams } from '../lib/sqlSplit';
import { lintSql, severityLabel, SEV_NUM, type LintResult } from '../lib/sqlLint';
import { TEMPLATES, filterTemplates, renderTemplate, type SqlTemplate, type TemplateContext } from '../lib/sqlTemplates';
import { loadCustomTemplates, saveCustomTemplates, toSqlTemplate, type CustomTemplate, default as CustomTemplatesModal } from '../lib/customTemplates';
import ImportModal from './ImportModal';
import type { ParamItem, ParamType } from '../lib/tabStore';
import { genId, loadTabState, nextTabTitle, saveTabState, type EditorTab } from '../lib/tabStore';
import CollapsiblePane from './CollapsiblePane';
import ContextMenu, { type ContextMenuEntry } from './ContextMenu';
import TabStrip from './TabStrip';
import { PlayIcon, StopIcon } from './Icons';

interface Props {
  connId: string;
  prefillSql?: string;
  contextLabel?: string;
  contextSchema?: string;
  contextTable?: string;
  autoRunToken?: number;
  onClearContext?: () => void;
}

function ExplainTree({ node, depth = 0 }: { node: ExplainNode; depth?: number }) {
  const boxStyle: CSSProperties = {
    border: '1px solid var(--border)',
    borderRadius: 3,
    padding: '4px 6px',
    margin: '2px 0 2px 0',
    background: 'var(--bg-elev, var(--panel))',
    marginLeft: depth * 14,
  };
  return (
    <div style={boxStyle}>
      <div style={{ display: 'flex', gap: 6, alignItems: 'baseline', flexWrap: 'wrap' }}>
        <span style={{ fontFamily: 'monospace', fontSize: 12, fontWeight: 600 }}>{node.name}</span>
        {node.cost && <span style={{ fontFamily: 'monospace', fontSize: 11, color: 'var(--muted)' }}>{node.cost}</span>}
        {node.rows && <span style={{ fontFamily: 'monospace', fontSize: 11, color: 'var(--muted)' }}>{node.rows}</span>}
      </div>
      {node.detail && <div style={{ fontFamily: 'monospace', fontSize: 11, color: 'var(--text)', marginTop: 2, whiteSpace: 'pre-wrap' }}>{node.detail}</div>}
      {node.children.length > 0 && (
        <div style={{ marginTop: 4, paddingLeft: 8, borderLeft: '1px solid var(--border)' }}>
          {node.children.map((c, i) => <ExplainTree key={i} node={c} depth={0} />)}
        </div>
      )}
    </div>
  );
}

function ExplainView({ result, kind }: { result: QueryResult; kind: import('../api').DatabaseKind | null }) {
  const render: ExplainRender = renderExplain(result, kind);
  if (render.mode === 'tree') {
    return (
      <div style={{ fontSize: 12 }}>
        {render.nodes.map((n, i) => <ExplainTree key={i} node={n} />)}
      </div>
    );
  }
  return (
    <pre style={{ fontFamily: 'monospace', fontSize: 12, margin: 0, padding: 4, background: 'var(--bg-elev, var(--panel))', border: '1px solid var(--border)', borderRadius: 3, whiteSpace: 'pre-wrap' }}>
      {render.lines.join('\n')}
    </pre>
  );
}

type Transport = 'http' | 'ws';
type CellClass = 'null' | 'num' | 'bool' | 'text' | 'blob';
type SortDir = 'asc' | 'desc' | null;

const HISTORY_KEY = 'polydb.queryHistory';
const THEME_KEY = 'polydb.theme';
const HISTORY_LIMIT = 50;

type EditorTheme = 'vs' | 'vs-dark' | 'hc-black';
const THEME_LABELS: Record<EditorTheme, string> = {
  'vs': 'Light',
  'vs-dark': 'Dark',
  'hc-black': 'Hi-Con',
};

const batchFlexSpacer: CSSProperties = { flex: '1 1 auto' };
const batchBusySpinner: CSSProperties = { fontSize: 11, color: 'var(--muted)' };
const batchSummaryStyle: CSSProperties = { cursor: 'pointer', fontSize: 11, color: 'var(--muted)', userSelect: 'none' };
const batchSqlPreviewStyle: CSSProperties = {
  margin: 0,
  padding: 6,
  background: 'var(--bg)',
  border: '1px solid var(--border)',
  borderRadius: 3,
  fontFamily: 'monospace',
  fontSize: 11,
  maxHeight: 120,
  overflow: 'auto',
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
};

function loadTheme(): EditorTheme {
  try {
    const raw = localStorage.getItem(THEME_KEY);
    if (raw === 'vs' || raw === 'vs-dark' || raw === 'hc-black') return raw;
  } catch { /* ignore */ }
  return 'vs';
}
function saveTheme(t: EditorTheme) {
  try { localStorage.setItem(THEME_KEY, t); } catch { /* ignore */ }
}

function sqlLiteral(v: unknown): string {
  if (v === null || v === undefined) return 'NULL';
  if (typeof v === 'number') return String(v);
  if (typeof v === 'boolean') return v ? 'TRUE' : 'FALSE';
  const s = String(v).replace(/'/g, "''");
  return `'${s}'`;
}
function bytesToHex(s: string, chunk = 16): string {
  const bytes: number[] = [];
  for (let i = 0; i < s.length; i++) {
    const code = s.charCodeAt(i);
    if (code < 128) bytes.push(code);
    else if (code < 2048) { bytes.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f)); }
    else { bytes.push(0xe0 | (code >> 12), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f)); }
  }
  const hexBytes = (b: number) => b.toString(16).padStart(2, '0');
  const printable = (b: number) => (b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : '·');
  const lines: string[] = [];
  for (let i = 0; i < bytes.length; i += chunk) {
    const slice = bytes.slice(i, i + chunk);
    const hex = slice.map(hexBytes).join(' ');
    const text = slice.map(printable).join('');
    lines.push(`${i.toString(16).padStart(8, '0')}  ${hex.padEnd(chunk * 3 - 1)}  |${text}|`);
  }
  return lines.length > 0 ? lines.join('\n') : '(empty)';
}

function tryParseJson(v: unknown): string | null {
  if (v === null || v === undefined) return null;
  if (typeof v === 'object') return JSON.stringify(v, null, 2);
  const s = String(v).trim();
  if (!s.startsWith('{') && !s.startsWith('[')) return null;
  try { return JSON.stringify(JSON.parse(s), null, 2); } catch { return null; }
}

function cellClass(v: unknown, col?: ResultColumn): CellClass {
  if (v === null || v === undefined) return 'null';
  const t = col?.type?.toLowerCase() ?? '';
  if (typeof v === 'number') return 'num';
  if (typeof v === 'boolean') return 'bool';
  if (t.includes('int') || t.includes('smallint') || t.includes('bigint') || t.includes('decimal') || t.includes('numeric') || t.includes('float') || t.includes('double') || t.includes('real')) {
    const s = String(v);
    if (/^-?\d+(\.\d+)?$/.test(s)) return 'num';
  }
  return 'text';
}

function fmt(v: unknown): string {
  if (v === null || v === undefined) return 'NULL';
  if (typeof v === 'object') {
    if (Array.isArray(v)) return `[${v.map(fmt).join(', ')}]`;
    return JSON.stringify(v);
  }
  return String(v);
}

function loadHistory(): string[] {
  try {
    const raw = localStorage.getItem(HISTORY_KEY);
    if (!raw) return [];
    const arr = JSON.parse(raw);
    return Array.isArray(arr) ? arr.filter((x) => typeof x === 'string').slice(0, HISTORY_LIMIT) : [];
  } catch {
    return [];
  }
}

function saveHistory(h: string[]) {
  try {
    localStorage.setItem(HISTORY_KEY, JSON.stringify(h.slice(0, HISTORY_LIMIT)));
  } catch {
    // quota / private mode
  }
}

function stripCommentsForHistory(sql: string): string {
  return sql
    .split('\n')
    .filter((l) => !/^\s*(\/\*|\*|\/|--)/.test(l) && l.trim().length > 0)
    .join(' ')
    .replace(/\s+/g, ' ')
    .trim();
}

function countParams(sql: string): number {
  let count = 0;
  let i = 0;
  const n = sql.length;
  let inSingle = false;
  let inDouble = false;
  let inLine = false;
  let inBlock = false;
  while (i < n) {
    const c = sql[i];
    const next = i + 1 < n ? sql[i + 1] : '';
    if (!inSingle && !inDouble && !inLine && !inBlock) {
      if (c === '-' && next === '-') { inLine = true; i += 2; continue; }
      if (c === '#') { inLine = true; i += 1; continue; }
      if (c === '/' && next === '*') { inBlock = true; i += 2; continue; }
      if (c === "'") { inSingle = true; i += 1; continue; }
      if (c === '"') { inDouble = true; i += 1; continue; }
      if (c === '?') { count += 1; i += 1; continue; }
      i += 1;
      continue;
    }
    if (inLine) {
      if (c === '\n') inLine = false;
      i += 1;
      continue;
    }
    if (inBlock) {
      if (c === '*' && next === '/') { inBlock = false; i += 2; continue; }
      i += 1;
      continue;
    }
    if (inSingle) {
      if (c === "'") {
        // '' is escaped single quote
        if (next === "'") { i += 2; continue; }
        inSingle = false;
      }
      i += 1;
      continue;
    }
    if (inDouble) {
      if (c === '"') {
        if (next === '"') { i += 2; continue; }
        inDouble = false;
      }
      i += 1;
      continue;
    }
  }
  return count;
}

function defaultParamValue(type: ParamType): string {
  switch (type) {
    case 'number': return '0';
    case 'bool': return 'false';
    case 'null': return '';
    case 'string':
    default: return 'value';
  }
}

function coerceParam(t: ParamType, raw: string): unknown {
  switch (t) {
    case 'null': return null;
    case 'bool': {
      const s = raw.trim().toLowerCase();
      if (s === 'true' || s === '1' || s === 'yes' || s === 't') return true;
      if (s === 'false' || s === '0' || s === 'no' || s === 'f') return false;
      return s;
    }
    case 'number': {
      const s = raw.trim();
      if (s === '') return null;
      const num = Number(s);
      return Number.isNaN(num) ? s : num;
    }
    case 'string':
    default: return raw;
  }
}

function csvEscape(v: unknown): string {
  if (v === null || v === undefined) return '';
  const s = String(v);
  if (/[",\n]/.test(s)) return `"${s.replace(/"/g, '""')}"`;
  return s;
}

function colFilterMatches(cell: unknown, filter: string): boolean {
  const f = filter.trim().toLowerCase();
  if (!f) return true;
  if (f === 'is null') return cell === null || cell === undefined;
  if (f === 'is not null' || f === 'not null') return cell !== null && cell !== undefined;
  if (f.startsWith('re:')) {
    try {
      const re = new RegExp(f.slice(3), 'i');
      if (cell === null || cell === undefined) return false;
      return re.test(String(cell));
    } catch {
      return true;
    }
  }
  if (cell === null || cell === undefined) return false;
  return String(cell).toLowerCase().includes(f);
}

function download(filename: string, mime: string, content: string) {
  const blob = new Blob([content], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function baseFilename(): string {
  const d = new Date();
  const pad = (n: number) => String(n).padStart(2, '0');
  return `polydb-${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
}

function sqlLit(v: unknown): string {
  if (v === null || v === undefined) return 'NULL';
  if (typeof v === 'number' && Number.isFinite(v)) return String(v);
  if (typeof v === 'boolean') return v ? '1' : '0';
  return `'${String(v).replace(/'/g, "''")}'`;
}

function identifyableRow(row: unknown[], pkCols: string[], cols: ResultColumn[]): boolean {
  if (pkCols.length === 0) return false;
  for (const pk of pkCols) {
    const idx = cols.findIndex((c) => c.name === pk);
    if (idx < 0) return false;
    if (row[idx] === null || row[idx] === undefined) return false;
  }
  return true;
}

function validateCellValue(s: string, col: ResultColumn): string | null {
  const t = (col.type ?? '').toLowerCase();
  if (/boolean|bool\b/i.test(t)) {
    if (!/^(0|1|true|false|yes|no|on|off|t|f|y|n)$/i.test(s.trim())) return `非布尔值：${s}`;
    return null;
  }
  if (/int|smallint|bigint|decimal|numeric|float|double|real|tinyint|mediumint|unsigned/i.test(t)) {
    if (!/^-?\d+(\.\d+)?([eE][-+]?\d+)?$/.test(s.trim())) return `非数值：${s}`;
    return null;
  }
  return null;
}

function parseCellValue(s: string, col: ResultColumn): unknown {
  const t = (col.type ?? '').toLowerCase();
  if (/boolean|bool\b/i.test(t)) return /^(1|true|yes|on|t|y)$/i.test(s.trim());
  if (/int|smallint|bigint|decimal|numeric|float|double|real|tinyint|mediumint|unsigned/i.test(t)) {
    const n = Number(s);
    if (!Number.isFinite(n)) throw new Error(`非数值：${s}`);
    return Number.isInteger(n) ? n : n;
  }
  return s;
}

export default function QueryWorkspace({ connId, prefillSql, contextLabel, contextSchema, contextTable, autoRunToken, onClearContext }: Props) {
  const editorRef = useRef<Parameters<OnMount>[0] | null>(null);
  const [tabs, setTabs] = useState<EditorTab[]>([]);
  const [activeId, setActiveId] = useState<string>('');
  const [sql, setSql] = useState<string>('');
  const [busy, setBusy] = useState(false);
  const [templatesOpen, setTemplatesOpen] = useState(false);
  const [templatesQuery, setTemplatesQuery] = useState('');
  const [templatesFocusIdx, setTemplatesFocusIdx] = useState(0);
  const [customTemplates, setCustomTemplates] = useState<CustomTemplate[]>(() => loadCustomTemplates());
  const [manageCustomOpen, setManageCustomOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const templatesRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => { saveCustomTemplates(customTemplates); }, [customTemplates]);
  const [, setLintResult] = useState<LintResult>({ diagnostics: [], errorCount: 0, warningCount: 0, infoCount: 0 });
  const lintTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const runFormatRef = useRef<() => void>(() => {});
  const [transport, setTransport] = useState<Transport>(() => (loadSettings().defaultTransport === 'ws' ? 'ws' : 'http'));
  const [theme, setTheme] = useState<EditorTheme>(() => loadTheme());
  const [settings, setSettings] = useState(loadSettings);
  const EDITOR_HEIGHT_KEY = 'polydb.editorHeight.v1';
  const [editorHeight, setEditorHeight] = useState<number>(() => {
    try {
      const raw = localStorage.getItem(EDITOR_HEIGHT_KEY);
      const n = raw ? parseInt(raw, 10) : NaN;
      if (Number.isFinite(n) && n >= 120 && n <= 800) return n;
    } catch { /* ignore */ }
    return 240;
  });
  const [result, setResult] = useState<QueryResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [elapsed, setElapsed] = useState<number | null>(null);
  const [queryId, setQueryId] = useState<string | null>(null);
  // 多语句执行：一次执行可能有多条语句，每条独立结果 tab。
  // 用户切换 tab 时把 active 的快照回填到 result/error/elapsed，让下游渲染代码无需改动。
  // status: pending → running → ok|err|cancelled|skipped（cancelled=当前正跑时被取消；skipped=取消前未开始）
  type MultiStatus = 'pending' | 'running' | 'ok' | 'err' | 'cancelled' | 'skipped';
  type MultiEntry = {
    sql: string;
    result: QueryResult | null;
    error: string | null;
    elapsed: number | null;
    status: MultiStatus;
  };
  const [multiResults, setMultiResults] = useState<MultiEntry[] | null>(null);
  const [activeMultiIdx, setActiveMultiIdx] = useState(0);
  // 多语句取消控制：cancelledRef 由 cancelMulti() 置位；in-flight ws query id 保存在 multiWsQueryIdRef 便于走 ws.cancel
  const multiCancelledRef = useRef(false);
  const multiWsQueryIdRef = useRef<string | null>(null);
  // 记录每条 statement 对应的编辑器 offset（用于取消/高亮时定位）
  const multiOffsetsRef = useRef<{ start: number; end: number }[]>([]);
  // Monaco 高亮当前正在执行的 statement：使用 decorations collection 便于多次 set
  const multiDecorationCollectionRef = useRef<{ set: (decs: any[]) => void; clear: () => void } | null>(null);
  const [history, setHistory] = useState<string[]>(() => loadHistory());
  const [histIdx, setHistIdx] = useState(-1);
  const [sortCol, setSortCol] = useState<number | null>(null);
  const [sortDir, setSortDir] = useState<SortDir>(null);
  const [selRows, setSelRows] = useState<Set<number>>(new Set());
  const [lastQuerySql, setLastQuerySql] = useState<string | null>(null);
  const [pkCols, setPkCols] = useState<string[]>([]);
  const [editCell, setEditCell] = useState<{ row: number; col: number; value: string; isNull: boolean } | null>(null);
  const [editDrafts, setEditDrafts] = useState<Record<string, { row: number; col: number; value: string; isNull: boolean }>>({});
  const [flashCells, setFlashCells] = useState<Record<string, 'ok' | 'err'>>({});
  const [batchOpen, setBatchOpen] = useState(false);
  const [batchEdit, setBatchEdit] = useState<{ col: number; value: string; isNull: boolean; isExpr: boolean } | null>(null);
  const [batchBusy, setBatchBusy] = useState(false);
  // 事务化批量编辑：pending 事务信息 + 每行的旧值快照（用于回滚时恢复内存网格）
  const [pendingTx, setPendingTx] = useState<{
    txId: string;
    connId: string;
    rows: number[];
    col: number;
    oldRowsSnapshot: unknown[][];
    count: number;
    expr: boolean;
    openedAt: number;
  } | null>(null);
  const [pendingTxBusy, setPendingTxBusy] = useState(false);
  const pendingTxRef = useRef<typeof pendingTx>(null);
  const [hiddenCols, setHiddenCols] = useState<Set<number>>(new Set());
  const [colMenu, setColMenu] = useState(false);
  const [exportMenu, setExportMenu] = useState(false);
  const [colFilters, setColFilters] = useState<Record<number, string>>({});
  const [params, setParams] = useState<ParamItem[]>([]);
  const [explainBusy, setExplainBusy] = useState(false);
  const [explainResult, setExplainResult] = useState<QueryResult | null>(null);
  const [explainError, setExplainError] = useState<string | null>(null);
  const [stats, setStats] = useState<ReturnType<typeof loadStats>>(() => loadStats());
  const [statsMenu, setStatsMenu] = useState(false);
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; row: number; col: number } | null>(null);
  const [cellPreview, setCellPreview] = useState<{ row: number; col: number } | null>(null);
  const [previewTab, setPreviewTab] = useState<'text' | 'json' | 'hex' | 'time'>('text');
  const [chartOpen, setChartOpen] = useState(false);
  const [chartKind, setChartKind] = useState<'bar' | 'line' | 'pie'>('bar');
  const [chartXCol, setChartXCol] = useState<number>(0);
  const [chartYCol, setChartYCol] = useState<number>(-1);
  const [chartGroupCol, setChartGroupCol] = useState<number>(-1);
  const [chartBarMode, setChartBarMode] = useState<'grouped' | 'stacked'>('grouped');
  const connKindRef = useRef<import('../api').DatabaseKind | null>(null);

  const connIdRef = useRef(connId);
  const histIdxRef = useRef(-1);
  const historyRef = useRef(history);
  const prefillRef = useRef<string | undefined>(undefined);
  const tokenRef = useRef(0);
  const lastFiredTokenRef = useRef(0);
  const sqlRef = useRef('');
  const busyRef = useRef(false);
  const paramsRef = useRef<ParamItem[]>([]);
  const tabsRef = useRef<EditorTab[]>([]);
  const activeIdRef = useRef('');

  useEffect(() => { registerSqlCompletionGlobal(); }, []);
  useEffect(() => {
    connIdRef.current = connId;
    setSqlCompletionConnId(connId);
    let cancelled = false;
    void api.getConnection(connId).then((c) => { if (!cancelled) connKindRef.current = c.kind; }).catch(() => {});
    return () => { cancelled = true; setSqlCompletionConnId(null); connKindRef.current = null; };
  }, [connId]);

  useEffect(() => { histIdxRef.current = histIdx; }, [histIdx]);
  useEffect(() => { historyRef.current = history; }, [history]);
  useEffect(() => { busyRef.current = busy; }, [busy]);
  useEffect(() => { sqlRef.current = sql; }, [sql]);
  useEffect(() => { paramsRef.current = params; }, [params]);
  useEffect(() => { tabsRef.current = tabs; }, [tabs]);
  useEffect(() => { activeIdRef.current = activeId; }, [activeId]);
  useEffect(() => { pendingTxRef.current = pendingTx; }, [pendingTx]);

  // 挂起事务的年龄指示：仅在有 pendingTx 时每秒 tick 一次
  const [txNow, setTxNow] = useState(() => Date.now());
  useEffect(() => {
    if (!pendingTx) return;
    const id = setInterval(() => setTxNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [pendingTx]);

  // 组件卸载时：若还有挂起事务，静默回滚以免服务端 tx 泄漏。
  // 用户主动 resolve 前若关闭连接或关闭页面，此回滚是安全默认。
  useEffect(() => {
    return () => {
      const p = pendingTxRef.current;
      if (!p) return;
      api.rollbackTransaction(p.txId).catch(() => { /* server 可能已重启 */ });
    };
  }, []);

  // 挂起事务时全局快捷键：Ctrl+Enter 提交 · Esc 回滚
  // 当焦点在输入控件（含 Monaco 的 textarea）上时跳过，避免与单元格编辑/SQL 运行冲突
  useEffect(() => {
    if (!pendingTx) return;
    const h = (e: KeyboardEvent) => {
      if (pendingTxBusy) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
      if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
        e.preventDefault();
        void resolvePendingTx(true);
      } else if (e.key === 'Escape') {
        e.preventDefault();
        void resolvePendingTx(false);
      }
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pendingTx, pendingTxBusy]);

  // 连接切换：若挂起事务属于旧连接，立即回滚（新连接下继续保留会语义错乱）。
  // 立即把 ref 置 null 保证 unmount cleanup 不会二次回滚同一 tx（重复回滚按契约返回 404）。
  useEffect(() => {
    const p = pendingTxRef.current;
    if (!p) return;
    if (p.connId === connId) return;
    pendingTxRef.current = null;
    api.rollbackTransaction(p.txId).catch(() => {});
    setPendingTx(null);
     
  }, [connId]);

  // Load persisted tab state per connection. Runs once on mount and on connId change.
  useEffect(() => {
    const state = loadTabState(connId);
    setTabs(state.tabs);
    setActiveId(state.activeId);
    tabsRef.current = state.tabs;
    activeIdRef.current = state.activeId;
    const t = state.tabs.find((x) => x.id === state.activeId) ?? state.tabs[0];
    if (!t) return;
    setResult(null); setError(null); setElapsed(null); setQueryId(null);
    setMultiResults(null); setActiveMultiIdx(0);
    setHistIdx(-1); setSortCol(null); setSortDir(null);
    setSelRows(new Set()); setEditCell(null); setPkCols([]);
    setHiddenCols(new Set()); setColMenu(false); setExportMenu(false); setColFilters({});
    setExplainResult(null); setExplainError(null); setExplainBusy(false);
    setLastQuerySql(null);
    setSql(t.sql);
    setParams(t.params);
     
  }, [connId]);

  useEffect(() => {
    if (!contextSchema || !contextTable) { setPkCols([]); return; }
    let cancelled = false;
    api.listColumns(connId, contextSchema, contextTable)
      .then((cols) => { if (!cancelled) setPkCols(cols.filter((c) => c.is_primary_key).map((c) => c.name)); })
      .catch(() => { if (!cancelled) setPkCols([]); });
    return () => { cancelled = true; };
  }, [connId, contextSchema, contextTable]);

  useEffect(() => {
    if (prefillSql) {
      setSql(prefillSql);
      setHistIdx(-1);
      setParams([]);
    }
  }, [prefillSql]);

  useEffect(() => {
    const n = countParams(sql);
    setParams((prev) => {
      if (prev.length === n) return prev;
      if (prev.length > n) return prev.slice(0, n);
      const next = prev.slice();
      while (next.length < n) next.push({ type: 'string', value: defaultParamValue('string') });
      return next;
    });
  }, [sql]);

  useEffect(() => {
    const t = setTimeout(() => {
      const cid = connIdRef.current;
      const cur = activeIdRef.current;
      const all = tabsRef.current;
      const next = all.map((x) => x.id === cur ? { ...x, sql: sqlRef.current, params: paramsRef.current } : x);
      saveTabState(cid, { tabs: next, activeId: cur, cursor: 0 });
      tabsRef.current = next;
    }, 300);
    return () => clearTimeout(t);
  }, [sql, params, activeId]);

  useEffect(() => {
    setSortCol(null);
    setSortDir(null);
    setSelRows(new Set());
    setEditCell(null);
    setColFilters({});
    const n = result?.columns.length ?? 0;
    setHiddenCols((prev) => {
      if (prev.size === 0) return prev;
      const next = new Set<number>();
      let changed = false;
      prev.forEach((j) => {
        if (j < n) next.add(j);
        else changed = true;
      });
      return changed ? next : prev;
    });
  }, [result]);

  useEffect(() => {
    tokenRef.current = autoRunToken ?? 0;
    if (!prefillSql) return;
    prefillRef.current = prefillSql;
    void flushPendingRun();
    async function flushPendingRun() {
      const cur = tokenRef.current;
      if (cur <= lastFiredTokenRef.current) return;
      if (busyRef.current) return;
      const q = prefillRef.current;
      if (!q) return;
      lastFiredTokenRef.current = cur;
      prefillRef.current = undefined;
      await runHttp(q);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoRunToken]);

  // 导入完成后由 ImportModal dispatch 的 preview SELECT 事件：本地跑一遍，走独立 token 避免与 App 的 autoRunToken 冲突
  const [localRunToken, setLocalRunToken] = useState(0);
  const localLastFiredRef = useRef(-1);
  useEffect(() => {
    if (localRunToken <= localLastFiredRef.current) return;
    if (busyRef.current) return;
    const sql = sqlRef.current;
    if (!sql.trim()) return;
    localLastFiredRef.current = localRunToken;
    void runHttp(sql);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [localRunToken]);
  useEffect(() => {
    const h = (ev: Event) => {
      const sql = (ev as CustomEvent<{ sql?: string }>).detail?.sql;
      if (typeof sql !== 'string' || !sql.trim()) return;
      setSql(sql);
      sqlRef.current = sql;
      setLocalRunToken((n) => n + 1);
    };
    window.addEventListener('polydb-import-preview', h);
    return () => window.removeEventListener('polydb-import-preview', h);
  }, []);

  const pushHistory = (raw: string) => {
    const entry = stripCommentsForHistory(raw);
    if (entry.length < 2) return;
    setHistory((prev) => {
      const next = [entry, ...prev.filter((x) => x !== entry)].slice(0, HISTORY_LIMIT);
      saveHistory(next);
      return next;
    });
  };

  const buildParams = (overrides?: ParamItem[]): import('../api').Value[] => {
    const src = overrides ?? paramsRef.current;
    if (src.length === 0) return [];
    return src.map((p) => coerceParam(p.type, p.value) as import('../api').Value);
  };

  const recordStat = (sqlText: string, r: QueryResult, elapsedMs: number) => {
    if (isExplainResult(sqlText)) return;
    const next = addStat({
      sql: sqlText,
      kind: connKindRef.current ?? 'sqlite',
      connId: connIdRef.current,
      elapsed_ms: elapsedMs,
      server_ms: r.execution_time_ms,
      rows: r.rows?.length ?? 0,
      truncated: r.has_more === true,
      ts: Date.now(),
    });
    setStats(next);
    window.dispatchEvent(new CustomEvent('polydb-stats-updated', { detail: { source: 'runStats' } }));
  };

  const runHttp = async (overrideSql?: string) => {
    const sqlToRun = overrideSql ?? sqlRef.current;
    if (!sqlToRun.trim() || busyRef.current) return;
    setBusy(true);
    setError(null);
    setStatsMenu(false);
    setMultiResults(null);
    pushHistory(sqlToRun);
    setHistIdx(-1);
    setLastQuerySql(sqlToRun);
    const t0 = performance.now();
    const pm = buildParams();
    try {
      const r = await api.executeQuery(connId, { sql: sqlToRun, params: pm.length > 0 ? pm : undefined });
      const t1 = performance.now() - t0;
      setResult(r);
      setElapsed(t1);
      recordStat(sqlToRun, r, t1);
    } catch (e) {
      setResult(null);
      setElapsed(performance.now() - t0);
      if (e instanceof ApiError) {
        setError(`${e.code}: ${e.message}${e.detail ? `\n${JSON.stringify(e.detail, null, 2)}` : ''}`);
      } else {
        setError(e instanceof Error ? e.message : String(e));
      }
    } finally {
      setBusy(false);
    }
  };

  const runWs = async (overrideSql?: string) => {
    const sqlToRun = overrideSql ?? sqlRef.current;
    if (!sqlToRun.trim() || busyRef.current) return;
    setBusy(true);
    setError(null);
    setResult(null);
    setStatsMenu(false);
    setMultiResults(null);
    pushHistory(sqlToRun);
    setLastQuerySql(sqlToRun);
    setHistIdx(-1);
    const t0 = performance.now();
    const pm = buildParams();
    try {
      const qid = await ws.query(connId, { sql: sqlToRun, params: pm.length > 0 ? pm : undefined }, {
        onResult: (r) => {
          const t1 = performance.now() - t0;
          setResult(r);
          setElapsed(t1);
          setBusy(false);
          setQueryId(null);
          recordStat(sqlToRun, r, t1);
        },
        onError: (e) => {
          setResult(null);
          setElapsed(performance.now() - t0);
          setBusy(false);
          setQueryId(null);
          setError(`${e.code ?? 'ERROR'}: ${e.message}`);
        },
        onCancelled: () => {
          setResult(null);
          setElapsed(performance.now() - t0);
          setBusy(false);
          setQueryId(null);
          setError('查询已取消');
        },
      });
      setQueryId(qid);
    } catch (e) {
      setResult(null);
      setElapsed(performance.now() - t0);
      setBusy(false);
      setQueryId(null);
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  // 把 SQL 里的字符偏移转成 Monaco 的 1-based (lineNumber, column)
  // 用于给当前正在执行的 statement 添加装饰器高亮
  const offsetToLineCol = (src: string, offset: number): { line: number; col: number } => {
    const clamped = Math.max(0, Math.min(offset, src.length));
    let line = 1;
    let lastNl = -1;
    for (let i = 0; i < clamped; i++) {
      if (src.charCodeAt(i) === 10) { line++; lastNl = i; }
    }
    return { line, col: clamped - lastNl };
  };

  // 生成一个 statement 的 query_id（http/ws 通用）。
  // HTTP 后端会把此 id 塞进 registry，供 POST /api/queries/{id}/cancel 取消；
  // WS 后端会作为 query_id 直接下发。
  const makeQueryId = (): string => {
    if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
    return `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
  };

  // 单条 statement 执行：ws 与 HTTP 两通道共用；返回 QueryResult，失败 throw。
  // 由 multiCancelledRef 兜底本地取消（HTTP 用 Promise.race；ws 用 cancel 信号）。
  const runOneStatement = (sql: string, params: import('../api').Value[] | undefined, qid: string) => {
    return new Promise<QueryResult>((resolve, reject) => {
      if (transport === 'ws') {
        ws.query(connId, { sql, params, query_id: qid }, {
          onResult: resolve,
          onError: (e) => reject(new Error(`${e.code ?? 'ERROR'}: ${e.message}`)),
          onCancelled: () => reject(new Error('查询已取消')),
        }).then((id) => { multiWsQueryIdRef.current = id; })
          .catch(reject);
        return;
      }
      // HTTP 路径：把 qid 一并送后端，让 /api/queries/{qid}/cancel 能命中；
      // 本地轮询兜底，取消信号到本地立即 reject，不必等后端。
      let settled = false;
      let iv: ReturnType<typeof setInterval> | null = null;
      const done = (fn: () => void) => { if (!settled) { settled = true; if (iv) clearInterval(iv); fn(); } };
      iv = setInterval(() => {
        if (multiCancelledRef.current) done(() => reject(new Error('查询已取消')));
      }, 50);
      api.executeQuery(connId, { sql, params, query_id: qid })
        .then((v) => done(() => resolve(v)))
        .catch((e) => done(() => reject(e)))
        .finally(() => { if (iv) clearInterval(iv); });
    });
  };

  // 把编辑器 offset 转 Monaco Range 并挂到高亮 collection；失败静默。
  const highlightStatement = (raw: string, start: number, end: number) => {
    const coll = multiDecorationCollectionRef.current;
    const model = editorRef.current?.getModel();
    if (!coll || !model) return;
    try {
      const a = offsetToLineCol(raw, start);
      const b = offsetToLineCol(raw, end);
      coll.set([{
        range: new Range(a.line, a.col, b.line, b.col),
        options: {
          className: 'polydb-active-statement',
          isWholeLine: false,
          overviewRulerColor: 'rgba(60,140,255,0.5)',
          overviewRulerLane: 1,
          stickiness: 1,
        },
      }]);
    } catch { /* decoration 失败不影响执行 */ }
  };

  // 多语句执行：按分号切分后逐条执行，每条独立结果 tab，错误不阻断后续。
  // 取消：cancelledRef 置位后剩余语句标记为 skipped，当前 in-flight 尝试通过 ws.cancel 或 /cancel 停止。
  const runMulti = async () => {
    const raw = sqlRef.current;
    if (!raw.trim() || busyRef.current) return;
    const stmts = splitSql(raw);
    if (stmts.length === 0) return;
    setBusy(true);
    setError(null);
    setResult(null);
    setStatsMenu(false);
    pushHistory(raw);
    setHistIdx(-1);
    setLastQuerySql(raw);
    multiCancelledRef.current = false;
    multiOffsetsRef.current = stmts.map((s) => ({ start: s.start, end: s.end }));
    // 逐条 statement 按其 `?` 数量切片参数，避免错位
    const pmAll = buildParams();
    let paramOffset = 0;
    const paramsByIdx: (import('../api').Value[] | undefined)[] = stmts.map((s) => {
      const n = countSqlParams(s.sql);
      const slice = pmAll.slice(paramOffset, paramOffset + n);
      paramOffset += n;
      return slice.length > 0 ? slice : undefined;
    });
    const entries: MultiEntry[] = stmts.map((s) => ({
      sql: s.sql, result: null, error: null, elapsed: null, status: 'pending',
    }));
    setMultiResults([...entries]);
    let firstErrorIdx = -1;
    const clearDeco = () => { try { multiDecorationCollectionRef.current?.clear(); } catch { /* ignore */ } };
    try {
      for (let i = 0; i < stmts.length; i++) {
        if (multiCancelledRef.current) {
          entries[i] = { sql: stmts[i].sql, result: null, error: null, elapsed: null, status: 'skipped' };
          setMultiResults([...entries]);
          continue;
        }
        highlightStatement(raw, stmts[i].start, stmts[i].end);
        entries[i] = { ...entries[i], status: 'running' };
        setMultiResults([...entries]);

        const t0 = performance.now();
        const qid = makeQueryId();
        multiWsQueryIdRef.current = qid;
        try {
          const r = await runOneStatement(stmts[i].sql, paramsByIdx[i], qid);
          const t1 = performance.now() - t0;
          entries[i] = { sql: stmts[i].sql, result: r, error: null, elapsed: t1, status: 'ok' };
          recordStat(stmts[i].sql, r, t1);
        } catch (e) {
          const t1 = performance.now() - t0;
          const cancelled = multiCancelledRef.current && (
            e instanceof Error && e.message === '查询已取消'
          );
          const msg = e instanceof ApiError
            ? `${e.code}: ${e.message}`
            : e instanceof Error ? e.message : String(e);
          entries[i] = {
            sql: stmts[i].sql, result: null, error: msg, elapsed: t1,
            status: cancelled ? 'cancelled' : 'err',
          };
          if (firstErrorIdx < 0) firstErrorIdx = i;
        }
        multiWsQueryIdRef.current = null;
        setMultiResults([...entries]);
      }
    } finally {
      clearDeco();
      multiWsQueryIdRef.current = null;
    }
    setBusy(false);
    // 若存在错误，切换到第一条错误；否则显示最后一条
    const idx = firstErrorIdx >= 0 ? firstErrorIdx : entries.length - 1;
    const e = entries[idx];
    setActiveMultiIdx(idx);
    setResult(e.result);
    setError(e.error);
    setElapsed(e.elapsed);
    setLastQuerySql(e.sql);
  };

  // 取消多语句执行：置位 flag + 尝试取消当前 in-flight 的查询（ws 或 HTTP /cancel 端点）
  const cancelMulti = () => {
    multiCancelledRef.current = true;
    const qid = multiWsQueryIdRef.current;
    if (!qid) return;
    multiWsQueryIdRef.current = null;
    if (transport === 'ws') {
      ws.cancel(qid);
    } else {
      void api.cancelQuery(qid).catch(() => { /* 404 表示查询已结束，忽略 */ });
    }
  };

  const applyActiveMulti = (idx: number) => {
    if (!multiResults) return;
    const e = multiResults[idx];
    if (!e) return;
    setActiveMultiIdx(idx);
    setResult(e.result);
    setError(e.error);
    setElapsed(e.elapsed);
    setLastQuerySql(e.sql);
  };

  // 重跑失败的语句：按当前 multiResults 中 status==='err' 的 idx 顺序重跑，
  // 保留其它位置不动。若重跑前语句文本已被修改，仍用条目里冻结的 sql（避免位置漂移）。
  const rerunFailed = async () => {
    if (!multiResults || busyRef.current) return;
    const raw = sqlRef.current;
    const entries: MultiEntry[] = multiResults.map((e) => ({ ...e }));
    // 从当前编辑器重新切出 statement 列表用于定位高亮（sql 可能与 entries[i].sql 一致）
    const stmts = splitSql(raw);
    // 参数按当前顺序重新切片（rerun 不重排，只影响 err 条目）
    const pmAll = buildParams();
    let paramOffset = 0;
    const paramsByIdx: (import('../api').Value[] | undefined)[] = stmts.map((s) => {
      const n = countSqlParams(s.sql);
      const slice = pmAll.slice(paramOffset, paramOffset + n);
      paramOffset += n;
      return slice.length > 0 ? slice : undefined;
    });
    const failIdxs = entries.map((e, i) => (e.status === 'err' ? i : -1)).filter((i) => i >= 0);
    if (failIdxs.length === 0) return;
    setBusy(true);
    multiCancelledRef.current = false;
    let firstErrorIdx = -1;
    try {
      for (const idx of failIdxs) {
        if (multiCancelledRef.current) {
          entries[idx] = { ...entries[idx], status: 'skipped', result: null, error: null, elapsed: null };
          setMultiResults([...entries]);
          continue;
        }
        if (stmts[idx]) highlightStatement(raw, stmts[idx].start, stmts[idx].end);
        entries[idx] = { ...entries[idx], status: 'running' };
        setMultiResults([...entries]);
        const t0 = performance.now();
        const qid = makeQueryId();
        multiWsQueryIdRef.current = qid;
        try {
          const r = await runOneStatement(entries[idx].sql, paramsByIdx[idx], qid);
          const t1 = performance.now() - t0;
          entries[idx] = { sql: entries[idx].sql, result: r, error: null, elapsed: t1, status: 'ok' };
          recordStat(entries[idx].sql, r, t1);
        } catch (e) {
          const t1 = performance.now() - t0;
          const cancelled = multiCancelledRef.current && (e instanceof Error && e.message === '查询已取消');
          const msg = e instanceof ApiError
            ? `${e.code}: ${e.message}`
            : e instanceof Error ? e.message : String(e);
          entries[idx] = {
            sql: entries[idx].sql, result: null, error: msg, elapsed: t1,
            status: cancelled ? 'cancelled' : 'err',
          };
          if (firstErrorIdx < 0) firstErrorIdx = idx;
        }
        multiWsQueryIdRef.current = null;
        setMultiResults([...entries]);
      }
    } finally {
      try { multiDecorationCollectionRef.current?.clear(); } catch { /* ignore */ }
      multiWsQueryIdRef.current = null;
    }
    setBusy(false);
    // 若仍有失败，跳到第一个；否则跳到最后一个重跑成功的
    const targetIdx = firstErrorIdx >= 0 ? firstErrorIdx : failIdxs[failIdxs.length - 1];
    applyActiveMulti(targetIdx);
  };

  // 用当前 context (schema/table/column) + conn kind + 编辑器当前 SQL 构建模板上下文
  const buildTemplateCtx = useCallback((opts?: { extraTable?: string; extraColumn?: string; rawSql?: string }): TemplateContext => {
    const model = editorRef.current?.getModel();
    const rawSql = opts?.rawSql ?? model?.getValue() ?? sqlRef.current;
    const sel = editorRef.current?.getSelection();
    // 尝试推断光标所在行的最左列名（用于列相关的模板）
    const line = model?.getLineContent(sel?.startLineNumber ?? 1) ?? '';
    const colMatch = line.match(/([A-Za-z_][A-Za-z0-9_$]*)/g);
    const columnGuess = opts?.extraColumn ?? colMatch?.[colMatch.length - 1] ?? undefined;
    return {
      kind: connKindRef.current,
      schema: contextSchema ?? undefined,
      table: opts?.extraTable ?? contextTable ?? undefined,
      column: opts?.extraColumn ?? columnGuess,
      rawSql,
    };
  }, [contextSchema, contextTable]);

  // 在 Monaco 光标位置插入模板 SQL；executeEdits 自动把光标停在插入文本末尾
  const insertTemplateSql = (sql: string) => {
    const editor = editorRef.current;
    if (!editor) return;
    editor.focus();
    const sel = editor.getSelection();
    if (!sel) return;
    editor.executeEdits('sql-template', [{
      range: sel,
      text: sql,
    }]);
  };

  const insertTemplate = (t: SqlTemplate) => {
    const ctx = buildTemplateCtx();
    const sql = renderTemplate(t, ctx);
    if (!sql) return;
    insertTemplateSql(sql);
    setTemplatesOpen(false);
  };

  const run = async () => {
    // 挂起事务存在时 Ctrl+Enter 已被 window listener 拦截走 commit；此处仅当没有 pendingTx 时才会被调用
    const stmts = splitSql(sqlRef.current);
    if (stmts.length > 1) {
      void runMulti();
      return;
    }
    if (transport === 'ws') return runWs();
    return runHttp();
  };

  const runExplain = async () => {
    const raw = sqlRef.current;
    if (!raw.trim() || explainBusy) return;
    const kind = connKindRef.current;
    const sqlToRun = buildExplainSql(raw, kind);
    if (!sqlToRun) return;
    setExplainBusy(true);
    setExplainError(null);
    setExplainResult(null);
    try {
      const r = await api.executeQuery(connId, { sql: sqlToRun });
      setExplainResult(r);
    } catch (e) {
      if (e instanceof ApiError) {
        setExplainError(`${e.code}: ${e.message}${e.detail ? `\n${JSON.stringify(e.detail, null, 2)}` : ''}`);
      } else {
        setExplainError(e instanceof Error ? e.message : String(e));
      }
    } finally {
      setExplainBusy(false);
    }
  };

  const isSelect = /^select\b/.test(stripCommentsForHistory(sql).trim().toLowerCase());

  const handleCancel = () => {
    if (!queryId) return;
    ws.cancel(queryId);
  };

  const applyHistory = (idx: number) => {
    if (idx < 0) {
      setSql('');
      setHistIdx(-1);
      return;
    }
    setSql(historyRef.current[idx] ?? '');
    setHistIdx(idx);
  };

  const handleMount: OnMount = (editor) => {
    editorRef.current = editor;
    editorStatusCleanupRef.current?.();
    const sub = editor.onDidChangeCursorSelection(() => publishCursor(editor));
    editorStatusCleanupRef.current = () => { sub.dispose(); };
    // 多语句执行时的当前 statement 高亮
    const decs = editor.createDecorationsCollection();
    multiDecorationCollectionRef.current = decs;
    publishCursor(editor);
    editor.addCommand(KeyMod.CtrlCmd | KeyCode.Enter, () => void run());
    // 多语句执行中 Ctrl+Esc 取消剩余语句
    editor.addCommand(KeyMod.CtrlCmd | KeyCode.Escape, () => { if (busyRef.current) void cancelMulti(); });
    editor.addCommand(KeyMod.Shift | KeyCode.KeyS, () => {
      editor.getAction('editor.action.formatDocument')?.run();
    });
    const clampPos = () => {
      const pos = editor.getPosition();
      const model = editor.getModel();
      if (!pos || !model) return;
      const line = Math.min(pos.lineNumber, model.getLineCount());
      const col = Math.min(pos.column, model.getLineMaxColumn(line));
      editor.setPosition({ lineNumber: line, column: col });
    };
    editor.addCommand(KeyCode.UpArrow, () => {
      const h = historyRef.current;
      if (h.length === 0) return;
      const maxIdx = h.length - 1;
      const next = histIdxRef.current < 0 ? maxIdx : Math.max(0, histIdxRef.current - 1);
      applyHistory(next);
      setTimeout(clampPos, 0);
    });
    editor.addCommand(KeyCode.DownArrow, () => {
      if (historyRef.current.length === 0 || histIdxRef.current < 0) return;
      applyHistory(histIdxRef.current + 1);
      setTimeout(clampPos, 0);
    });
    editor.addCommand(KeyMod.CtrlCmd | KeyCode.KeyP, () => {
      const h = historyRef.current;
      if (h.length === 0) return;
      const maxIdx = h.length - 1;
      const next = histIdxRef.current < 0 ? maxIdx : Math.max(0, histIdxRef.current - 1);
      applyHistory(next);
    });
    editor.addCommand(KeyMod.CtrlCmd | KeyCode.KeyN, () => {
      if (historyRef.current.length === 0 || histIdxRef.current < 0) return;
      applyHistory(histIdxRef.current + 1);
    });
  };

  const copyAsCsv = () => {
    if (!result || visibleColumns.length === 0) return;
    const lines = [visibleColumns.map((c) => c.name).join(',')];
    for (const entry of displayRows) {
      lines.push(entry.row.filter((_, j) => !hiddenCols.has(j)).map(csvEscape).join(','));
    }
    void navigator.clipboard.writeText(lines.join('\n'));
  };

  const copySelected = (kind: 'csv' | 'json') => {
    if (!result || selRows.size === 0 || visibleColumns.length === 0) return;
    const cols = visibleColumns;
    const orig = result.rows ?? [];
    const picked = orig.filter((_, origIdx) => selRows.has(origIdx));
    let text: string;
    if (kind === 'json') {
      text = JSON.stringify(
        picked.map((row) => {
          const obj: Record<string, unknown> = {};
          for (const c of cols) {
            const idx = columns.indexOf(c);
            obj[c.name] = row[idx];
          }
          return obj;
        }),
        null,
        2,
      );
    } else {
      const idxs = cols.map((c) => columns.indexOf(c));
      const lines = [cols.map((c) => c.name).join(',')];
      for (const row of picked) {
        lines.push(idxs.map((idx) => csvEscape(row[idx])).join(','));
      }
      text = lines.join('\n');
    }
    void navigator.clipboard.writeText(text);
  };

  const toggleRow = (i: number, additive: boolean) => {
    setSelRows((prev) => {
      if (!additive) return prev.has(i) && prev.size === 1 ? new Set() : new Set([i]);
      const next = new Set(prev);
      if (next.has(i)) next.delete(i);
      else next.add(i);
      return next;
    });
  };

  const onSort = (col: number) => {
    if (sortCol !== col) {
      setSortCol(col);
      setSortDir('asc');
    } else if (sortDir === 'asc') {
      setSortDir('desc');
    } else {
      setSortCol(null);
      setSortDir(null);
    }
  };

  const sortIcon = (col: number) => {
    if (sortCol !== col) return <span style={{ opacity: 0.3 }}>↕</span>;
    return sortDir === 'asc' ? '↑' : '↓';
  };

  const columns = useMemo(() => result?.columns ?? [], [result]);
  const rowCount = result?.rows?.length ?? 0;
  const statSummary = useMemo(
    () => (lastQuerySql ? summarize(stats, lastQuerySql) : null),
    [stats, lastQuerySql],
  );

  const draftKey = (row: number, col: number) => `${row * 100000 + col}`;

  // Rows with their original index so sort/visibility don't break selection/edit identity.
  const displayRows = useMemo(() => {
    if (!result) return [] as { origIdx: number; row: unknown[] }[];
    const src = result.rows ?? [];
    const withDrafts = src.map((row, origIdx) => {
      const draftFor = (col: number) => editDrafts[draftKey(origIdx, col)];
      const hasAny = row.some((_, col) => !!draftFor(col));
      if (!hasAny) return { origIdx, row };
      const applied = row.slice();
      for (let col = 0; col < applied.length; col++) {
        const d = draftFor(col);
        if (d) applied[col] = d.isNull ? null : d.value;
      }
      return { origIdx, row: applied };
    });
    const activeFilters = Object.entries(colFilters)
      .filter(([, v]) => v.trim().length > 0)
      .map(([k, v]) => ({ idx: Number(k), filter: v.trim() }));
    let filtered = withDrafts.map((entry) => ({ origIdx: entry.origIdx, row: entry.row }));
    if (activeFilters.length > 0) {
      filtered = filtered.filter((entry) =>
        activeFilters.every((af) => colFilterMatches(entry.row[af.idx], af.filter)),
      );
    }
    if (sortCol === null || sortDir === null) return filtered;
    const col = sortCol;
    const type = result.columns[col]?.type?.toLowerCase() ?? '';
    const numeric = /int|decimal|numeric|float|double|real/i.test(type);
    return filtered.slice().sort((a, b) => {
      const x = a.row[col];
      const y = b.row[col];
      if (x === null && y === null) return 0;
      if (x === null) return 1;
      if (y === null) return -1;
      if (numeric) {
        const xn = Number(x);
        const yn = Number(y);
        if (!Number.isNaN(xn) && !Number.isNaN(yn)) return sortDir === 'asc' ? xn - yn : yn - xn;
      }
      const sx = String(x);
      const sy = String(y);
      return sortDir === 'asc' ? sx.localeCompare(sy) : sy.localeCompare(sx);
    });
  }, [result, sortCol, sortDir, colFilters, editDrafts]);

  const flashCell = (row: number, col: number, kind: 'ok' | 'err') => {
    const k = draftKey(row, col);
    setFlashCells((prev) => {
      const next = { ...prev, [k]: kind };
      setTimeout(() => {
        setFlashCells((p) => {
          const n = { ...p };
          delete n[k];
          return n;
        });
      }, 800);
      return next;
    });
  };

  const startEdit = (realIdx: number, j: number) => {
    if (!result || !contextTable || !contextSchema) return;
    if (pkCols.length === 0) return;
    const row = (result.rows ?? [])[realIdx];
    if (!row || !identifyableRow(row, pkCols, result.columns)) return;
    const colName = result.columns[j].name;
    if (pkCols.includes(colName)) return;
    if (editCell && (editCell.row !== realIdx || editCell.col !== j)) {
      setEditDrafts((prev) => ({ ...prev, [draftKey(editCell.row, editCell.col)]: editCell }));
    }
    const existing = editDrafts[draftKey(realIdx, j)];
    if (existing) {
      setEditCell({ ...existing });
      return;
    }
    const cur = row[j];
    const isNull = cur === null || cur === undefined;
    setEditCell({ row: realIdx, col: j, value: isNull ? '' : String(cur), isNull });
  };

  const commitEdit = async () => {
    if (!editCell || !result || !contextTable || !contextSchema) return;
    const cols = result.columns;
    const row = (result.rows ?? [])[editCell.row];
    if (!row) { setEditCell(null); return; }
    const col = cols[editCell.col];
    const colName = col.name;
    if (editCell.value === '' && !editCell.isNull) {
      const err = '请输入值或勾选 NULL';
      flashCell(editCell.row, editCell.col, 'err');
      setError(err);
      return;
    }
    if (!editCell.isNull) {
      const validationErr = validateCellValue(editCell.value, col);
      if (validationErr) {
        flashCell(editCell.row, editCell.col, 'err');
        setError(validationErr);
        return;
      }
    }
    const where = pkCols
      .map((pk) => {
        const idx = cols.findIndex((c) => c.name === pk);
        return `${quoteIdent(pk)} = ${sqlLit(row[idx])}`;
      })
      .join(' AND ');
    let newValueSql: string;
    try {
      newValueSql = editCell.isNull ? 'NULL' : sqlLit(parseCellValue(editCell.value, col));
    } catch (e) {
      flashCell(editCell.row, editCell.col, 'err');
      setError(e instanceof Error ? e.message : String(e));
      return;
    }
    const updateSql = `UPDATE ${contextSchema}.${contextTable} SET ${quoteIdent(colName)} = ${newValueSql} WHERE ${where}`;
    const prevRow = editCell.row;
    const prevCol = editCell.col;
    setEditCell(null);
    setEditDrafts((prev) => {
      const n = { ...prev };
      delete n[draftKey(prevRow, prevCol)];
      return n;
    });
    setError(null);
    pushHistory(updateSql);
    const t0 = performance.now();
    setBusy(true);
    try {
      const r = await api.executeQuery(connId, { sql: updateSql });
      if (r.affected_rows > 0) {
        let newVal: unknown;
        try { newVal = editCell.isNull ? null : parseCellValue(editCell.value, col); }
        catch { newVal = editCell.isNull ? null : editCell.value; }
        const prevRows = result.rows ?? [];
        if (prevRows[prevRow]) {
          const newRows = prevRows.slice();
          const rowCopy = newRows[prevRow].slice();
          rowCopy[prevCol] = newVal as typeof rowCopy[number];
          newRows[prevRow] = rowCopy;
          setResult({ ...result, rows: newRows });
        }
        flashCell(prevRow, prevCol, 'ok');
        setElapsed(performance.now() - t0);
      } else {
        flashCell(prevRow, prevCol, 'err');
        setError('未匹配到行（主键可能不唯一或已被修改）');
      }
    } catch (e) {
      flashCell(prevRow, prevCol, 'err');
      if (e instanceof ApiError) setError(`${e.code}: ${e.message}`);
      else if (e instanceof Error) setError(e.message);
      else setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  const cancelEdit = () => {
    if (editCell) {
      setEditDrafts((prev) => {
        const n = { ...prev, [draftKey(editCell.row, editCell.col)]: editCell };
        return n;
      });
    }
    setEditCell(null);
  };

  const quoteIdent = (name: string) => {
    if (/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) return name;
    return `"${name.replace(/"/g, '""')}"`;
  };

  const copyText = (s: string) => { void navigator.clipboard.writeText(s); };

  const ctxTarget = useMemo(() => {
    if (!ctxMenu || !result) return null;
    const row = result.rows?.[ctxMenu.row];
    const col = result.columns[ctxMenu.col];
    if (!row || !col) return null;
    return { row, col, colIdx: ctxMenu.col, rowIdx: ctxMenu.row, value: row[ctxMenu.col] };
  }, [ctxMenu, result]);

  const previewTarget = useMemo(() => {
    if (!cellPreview || !result) return null;
    const row = result.rows?.[cellPreview.row];
    const col = result.columns[cellPreview.col];
    if (!row || !col) return null;
    return { row, col, value: row[cellPreview.col] };
  }, [cellPreview, result]);

  const toChartNum = (v: unknown): number => {
    if (typeof v === 'number' && Number.isFinite(v)) return v;
    if (typeof v === 'boolean') return v ? 1 : 0;
    if (typeof v === 'string') {
      const t = v.trim();
      if (!t) return 0;
      const n = Number(t);
      return Number.isFinite(n) ? n : 0;
    }
    return 0;
  };

  const chartData = useMemo(() => {
    const allRows = result?.rows ?? [];
    const xIdx = chartXCol;
    if (xIdx < 0 || xIdx >= columns.length) return null;
    const gIdx = chartGroupCol;
    let yIdx = chartYCol;
    if (chartKind !== 'pie' && (yIdx < 0 || yIdx >= columns.length)) {
      const numericRe = /int|float|double|decimal|numeric|real|bigint|smallint|tinyint|unsigned/i;
      const numericCandidates = columns.map((c, j) => ({ c, j })).filter(({ c, j }) => j !== xIdx && j !== gIdx && numericRe.test(c.type));
      yIdx = numericCandidates.length > 0 ? numericCandidates[0].j : columns.findIndex((_, j) => j !== xIdx && j !== gIdx);
    }
    if (chartKind !== 'pie' && yIdx < 0) return null;

    // Preserve insertion order for both x labels and group labels.
    const seenX = new Set<string>();
    const seenG = new Set<string>();
    const labels: string[] = [];
    const groupLabels: string[] = [];
    for (const r of allRows) {
      const xv = r[xIdx];
      const xl = xv === null || xv === undefined ? '(NULL)' : String(xv);
      if (!seenX.has(xl)) { seenX.add(xl); labels.push(xl); }
      if (gIdx >= 0 && gIdx < columns.length) {
        const gv = r[gIdx];
        const gl = gv === null || gv === undefined ? '(NULL)' : String(gv);
        if (!seenG.has(gl)) { seenG.add(gl); groupLabels.push(gl); }
      }
    }

    if (groupLabels.length === 0) {
      const values: number[] = allRows.map((r) => toChartNum(r[yIdx]));
      return { xIdx, yIdx, labels, values, series: null, groupIdx: null };
    }

    const series = groupLabels.map((gl) => {
      const vals = labels.map(() => 0);
      for (const r of allRows) {
        const xv = r[xIdx];
        const xl = xv === null || xv === undefined ? '(NULL)' : String(xv);
        const gv = r[gIdx];
        const gval = gv === null || gv === undefined ? '(NULL)' : String(gv);
        if (gval !== gl) continue;
        const xi = labels.indexOf(xl);
        if (xi < 0) continue;
        vals[xi] += toChartNum(r[yIdx]);
      }
      return { label: gl, values: vals };
    });

    return { xIdx, yIdx, labels, values: null as null | number[], series, groupIdx: gIdx };
  }, [chartXCol, chartYCol, chartGroupCol, chartKind, result, columns]);

  const onCtx = (act: string) => {
    const menu = ctxMenu;
    setCtxMenu(null);
    if (!menu || !ctxTarget) return;
    const { row, col, value } = ctxTarget;
    if (act === 'copy-cell') {
      copyText(value === null || value === undefined ? 'NULL' : String(value));
    } else if (act === 'copy-cell-quoted') {
      copyText(sqlLiteral(value));
    } else if (act === 'copy-row') {
      const obj: Record<string, unknown> = {};
      result!.columns.forEach((c, i) => { obj[c.name] = row[i]; });
      copyText(JSON.stringify(obj, null, 2));
    } else if (act === 'copy-row-csv') {
      copyText(result!.columns.map((c, i) => csvEscape(row[i])).join(','));
    } else if (act === 'copy-col-values') {
      copyText((result!.rows ?? []).map((r) => (r[ctxMenu!.col] === null || r[ctxMenu!.col] === undefined ? 'NULL' : String(r[ctxMenu!.col]))).join('\n'));
    } else if (act === 'gen-insert') {
      if (!contextSchema || !contextTable) return;
      const cols = result!.columns.map((c) => quoteIdent(c.name)).join(', ');
      const vals = row.map((v) => sqlLiteral(v)).join(', ');
      const table = `${quoteIdent(contextSchema)}.${quoteIdent(contextTable)}`;
      copyText(`INSERT INTO ${table} (${cols}) VALUES (${vals});`);
    } else if (act === 'gen-update') {
      if (!contextSchema || !contextTable) return;
      const pkIdx = pkCols.map((p) => result!.columns.findIndex((c) => c.name === p)).filter((i) => i >= 0);
      if (pkIdx.length === 0) return;
      const where = pkIdx.map((i) => `${quoteIdent(result!.columns[i].name)} = ${sqlLiteral(row[i])}`).join(' AND ');
      const table = `${quoteIdent(contextSchema)}.${quoteIdent(contextTable)}`;
      const set = `${quoteIdent(col.name)} = ${sqlLiteral(value)}`;
      copyText(`UPDATE ${table} SET ${set} WHERE ${where};`);
    } else if (act === 'gen-select') {
      if (!contextSchema || !contextTable) return;
      const pkIdx = pkCols.map((p) => result!.columns.findIndex((c) => c.name === p)).filter((i) => i >= 0);
      const table = `${quoteIdent(contextSchema)}.${quoteIdent(contextTable)}`;
      let out = `SELECT * FROM ${table}`;
      if (pkIdx.length > 0) {
        const where = pkIdx.map((i) => `${quoteIdent(result!.columns[i].name)} = ${sqlLiteral(row[i])}`).join(' AND ');
        out += ` WHERE ${where}`;
      }
      out += ';';
      copyText(out);
    } else if (act === 'edit-cell') {
      if (editEnabled && identifyableRow(row, pkCols, result!.columns)) startEdit(menu.row, menu.col);
    } else if (act === 'batch-edit-cell') {
      if (editEnabled && selRows.size >= 2) openBatchEdit(menu.col);
    } else if (act === 'preview') {
      setPreviewTab('text');
      setCellPreview({ row: menu.row, col: menu.col });
    } else if (act === 'hide-col') {
      setHiddenCols((prev) => { const next = new Set(prev); next.add(menu.col); return next; });
    } else if (act === 'toggle-col') {
      setHiddenCols((prev) => {
        const next = new Set(prev);
        if (next.has(menu.col)) next.delete(menu.col);
        else next.add(menu.col);
        return next;
      });
    } else if (act === 'filter-eq') {
      setColFilters((prev) => {
        const cur = prev[menu.col];
        if (cur === undefined) return { ...prev, [menu.col]: String(value ?? '') };
        const next = { ...prev };
        delete next[menu.col];
        return next;
      });
    } else if (act === 'sort-asc') {
      setSortCol(menu.col); setSortDir('asc');
    } else if (act === 'sort-desc') {
      setSortCol(menu.col); setSortDir('desc');
    }
  };

  const ctxItems: ContextMenuEntry[] = useMemo(() => {
    if (!ctxTarget) return [];
    const { col } = ctxTarget;
    const pkAvailable = pkCols.length > 0 && identifyableRow(ctxTarget.row, pkCols, result?.columns ?? []);
    const isPkCol = pkCols.includes(col.name);
    const cellEditable = pkAvailable && !isPkCol && !!contextSchema && !!contextTable && result?.statement_type === 'select';
    const colHidden = hiddenCols.has(ctxTarget.colIdx);
    const colFiltersState: Record<number, string> | undefined = colFilters;
    const defs: ({ key: string; label: string; icon?: string; checked?: boolean; disabled?: boolean } | '---')[] = [
      { key: 'copy-cell', label: '复制单元格值', icon: '⧉' },
      { key: 'copy-cell-quoted', label: `复制为 SQL 字面量 (${col.type})`, icon: '⧉' },
      { key: 'copy-row', label: '复制整行为 JSON', icon: '⧉' },
      { key: 'copy-row-csv', label: '复制整行为 CSV', icon: '⧉' },
      { key: 'copy-col-values', label: '复制整列（可见行）', icon: '⧉' },
      '---',
      { key: 'gen-insert', label: '生成 INSERT 语句', icon: '⚡', disabled: !contextSchema || !contextTable },
      { key: 'gen-update', label: '生成 UPDATE 语句', icon: '⚡', disabled: !pkAvailable || !contextSchema || !contextTable },
      { key: 'gen-select', label: '生成 WHERE 主键的 SELECT', icon: '⚡', disabled: !pkAvailable || !contextSchema || !contextTable },
      '---',
      { key: 'preview', label: '预览此单元格', icon: '🔍' },
      { key: 'edit-cell', label: '编辑此单元格', icon: '✎', disabled: !cellEditable },
      ...(cellEditable && selRows.size >= 2
        ? [{ key: 'batch-edit-cell', label: `批量编辑此列（选中 ${selRows.size} 行）`, icon: '⚒' }]
        : []),
      { key: 'toggle-col', label: colHidden ? `显示列 ${col.name}` : `隐藏列 ${col.name}`, icon: '⊘', checked: colHidden },
      { key: 'filter-eq', label: `按此值过滤（=）`, icon: '⚑', checked: colFiltersState?.[ctxTarget.colIdx] !== undefined && colFiltersState?.[ctxTarget.colIdx] !== '' },
      { key: 'sort-asc', label: `按 ${col.name} 升序`, icon: '↑', checked: sortCol === ctxTarget.colIdx && sortDir === 'asc' },
      { key: 'sort-desc', label: `按 ${col.name} 降序`, icon: '↓', checked: sortCol === ctxTarget.colIdx && sortDir === 'desc' },
    ];
    return defs.map((d) => d === '---' ? '---' as const : { key: d.key, label: d.label, icon: d.icon, checked: d.checked, disabled: d.disabled, onClick: () => onCtx(d.key) });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ctxTarget, pkCols, contextSchema, contextTable, result, hiddenCols, colFilters, sortCol, sortDir, selRows]);

  const handleCellKeyDown = (e: { key: string; ctrlKey: boolean; preventDefault: () => void }) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      if (e.ctrlKey) { void forceCommitEdit(); }
      else { void commitEdit(); }
    } else if (e.key === 'Escape') {
      e.preventDefault();
      cancelEdit();
    }
  };

  const forceCommitEdit = async () => {
    if (!editCell || !result || !contextTable || !contextSchema) return;
    // 跳过前端校验，直接尝试 UPDATE；失败靠后端返回错误
    const tempEdit = { ...editCell, isNull: editCell.isNull || editCell.value === '' };
    // 复用 commitEdit 但通过临时"绕过校验"标志实现
    // 通过临时 state 修改太复杂，改为直接手工执行
    const cols = result.columns;
    const row = (result.rows ?? [])[tempEdit.row];
    if (!row) { setEditCell(null); return; }
    const col = cols[tempEdit.col];
    const colName = col.name;
    const where = pkCols
      .map((pk) => {
        const idx = cols.findIndex((c) => c.name === pk);
        return `${quoteIdent(pk)} = ${sqlLit(row[idx])}`;
      })
      .join(' AND ');
    const newValueSql = tempEdit.isNull ? 'NULL' : sqlLit(tempEdit.value);
    const updateSql = `UPDATE ${contextSchema}.${contextTable} SET ${quoteIdent(colName)} = ${newValueSql} WHERE ${where}`;
    const prevRow = tempEdit.row;
    const prevCol = tempEdit.col;
    setEditCell(null);
    setEditDrafts((prev) => {
      const n = { ...prev };
      delete n[draftKey(prevRow, prevCol)];
      return n;
    });
    setError(null);
    pushHistory(updateSql);
    const t0 = performance.now();
    setBusy(true);
    try {
      const r = await api.executeQuery(connId, { sql: updateSql });
      if (r.affected_rows > 0) {
        const prevRows = result.rows ?? [];
        if (prevRows[prevRow]) {
          const newRows = prevRows.slice();
          const rowCopy = newRows[prevRow].slice();
          rowCopy[prevCol] = tempEdit.isNull ? null : tempEdit.value;
          newRows[prevRow] = rowCopy;
          setResult({ ...result, rows: newRows });
        }
        flashCell(prevRow, prevCol, 'ok');
        setElapsed(performance.now() - t0);
      } else {
        flashCell(prevRow, prevCol, 'err');
        setError('未匹配到行（主键可能不唯一或已被修改）');
      }
    } catch (e) {
      flashCell(prevRow, prevCol, 'err');
      if (e instanceof ApiError) setError(`${e.code}: ${e.message}`);
      else if (e instanceof Error) setError(e.message);
      else setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  const batchTargetRows = useMemo(() => {
    if (!result || !batchEdit || selRows.size === 0) return [];
    const cols = result.columns;
    return Array.from(selRows)
      .filter((i) => i < (result.rows ?? []).length)
      .filter((i) => {
        const row = (result.rows ?? [])[i];
        return identifyableRow(row, pkCols, cols);
      })
      .slice()
      .sort((a, b) => a - b);
  }, [result, batchEdit, selRows, pkCols]);

  const buildBatchUpdates = (): string[] => {
    if (!batchEdit || !result || !contextTable || !contextSchema) return [];
    const cols = result.columns;
    const col = cols[batchEdit.col];
    if (!col || pkCols.includes(col.name)) return [];
    return batchTargetRows.map((i) => {
      const row = (result.rows ?? [])[i];
      const where = pkCols
        .map((pk) => {
          const idx = cols.findIndex((c) => c.name === pk);
          return `${quoteIdent(pk)} = ${sqlLit(row[idx])}`;
        })
        .join(' AND ');
      let valueSql: string;
      if (batchEdit.isNull) valueSql = 'NULL';
      else if (batchEdit.isExpr) valueSql = batchEdit.value.trim();
      else valueSql = sqlLit(batchEdit.value);
      return `UPDATE ${contextSchema}.${contextTable} SET ${quoteIdent(col.name)} = ${valueSql} WHERE ${where}`;
    });
  };

  const commitBatchEdit = async () => {
    if (!batchEdit || !result || !contextTable || !contextSchema) return;
    const updates = buildBatchUpdates();
    if (updates.length === 0) {
      setError('无可批量更新的行（可能选中行缺少主键或选择了主键列）');
      return;
    }
    setBatchBusy(true);
    setError(null);
    let txId = '';
    const oldRows = (result.rows ?? []).slice();
    const newRows = oldRows.slice();
    let failCount = 0;
    try {
      // 开启事务
      const tx = await api.beginTransaction(connId, 'read_committed');
      txId = tx.id;
      for (let i = 0; i < updates.length; i++) {
        try {
          const r = await api.executeInTx(txId, { sql: updates[i] });
          if (r.affected_rows > 0) {
            // 就地更新内存网格，让用户看到待提交的新值
            const idx = batchTargetRows[i];
            const col = result.columns[batchEdit.col];
            let newVal: unknown;
            if (batchEdit.isNull) newVal = null;
            else if (batchEdit.isExpr) newVal = batchEdit.value;
            else {
              try { newVal = parseCellValue(batchEdit.value, col); }
              catch { newVal = batchEdit.value; }
            }
            const rowCopy = newRows[idx].slice();
            rowCopy[batchEdit.col] = newVal as typeof rowCopy[number];
            newRows[idx] = rowCopy;
            flashCell(idx, batchEdit.col, 'ok');
          } else {
            failCount++;
          }
        } catch {
          failCount++;
        }
      }
      if (failCount > 0) {
        try { await api.rollbackTransaction(txId); } catch { /* ignore */ }
        setError(`批量更新：${failCount} 行受影响数为 0 或报错，已回滚`);
        return;
      }
      setResult({ ...result, rows: newRows });
      setPendingTx({
        txId,
        connId,
        rows: batchTargetRows.slice(),
        col: batchEdit.col,
        oldRowsSnapshot: oldRows,
        count: updates.length,
        expr: batchEdit.isExpr,
        openedAt: Date.now(),
      });
      setBatchOpen(false);
      pushHistory(updates[0]);
      if (updates.length > 1) pushHistory(`-- ${updates.length - 1} more UPDATE statements (txn ${txId.slice(0, 8)})`);
    } catch (e) {
      if (txId) {
        try { await api.rollbackTransaction(txId); } catch { /* ignore */ }
      }
      if (e instanceof ApiError) setError(`${e.code}: ${e.message}`);
      else if (e instanceof Error) setError(e.message);
      else setError(String(e));
    } finally {
      setBatchBusy(false);
    }
  };

  const resolvePendingTx = async (commit: boolean) => {
    if (!pendingTx) return;
    setPendingTxBusy(true);
    setError(null);
    try {
      if (commit) {
        await api.commitTransaction(pendingTx.txId);
        publishEditorStatus({ message: `已提交事务 · ${pendingTx.count} 行`, messageAt: Date.now() });
      } else {
        await api.rollbackTransaction(pendingTx.txId);
        // 恢复内存网格旧值：仅恢复本次批量更新的列，保留其他列的后续编辑
        if (result) {
          const restored = (result.rows ?? []).slice();
          const snapshot = pendingTx.oldRowsSnapshot ?? [];
          for (let i = 0; i < pendingTx.rows.length; i++) {
            const idx = pendingTx.rows[i];
            const oldRow = snapshot[idx];
            if (oldRow && restored[idx]) {
              const rowCopy = restored[idx].slice();
              rowCopy[pendingTx.col] = oldRow[pendingTx.col] as typeof rowCopy[number];
              restored[idx] = rowCopy;
            }
          }
          setResult({ ...result, rows: restored });
        }
        publishEditorStatus({ message: `已回滚事务 · ${pendingTx.count} 行`, messageAt: Date.now() });
      }
      setPendingTx(null);
    } catch (e) {
      if (e instanceof ApiError) setError(`${e.code}: ${e.message}`);
      else if (e instanceof Error) setError(e.message);
      else setError(String(e));
    } finally {
      setPendingTxBusy(false);
    }
  };

  const openBatchEdit = (col: number) => {
    if (selRows.size === 0) return;
    if (pkCols.includes(columns[col]?.name ?? '')) return;
    if (!contextSchema || !contextTable || result?.statement_type !== 'select') return;
    const cur = (result?.rows ?? [])[Array.from(selRows)[0]]?.[col];
    const isNull = cur === null || cur === undefined;
    setBatchEdit({ col, value: isNull ? '' : String(cur), isNull, isExpr: false });
    setBatchOpen(true);
  };

  const editEnabled = pkCols.length > 0 && !!contextTable && !!contextSchema && result?.statement_type === 'select';

  const visibleColumns = columns.filter((_, j) => !hiddenCols.has(j));
  const visibleRows = displayRows.map((entry) => ({ origIdx: entry.origIdx, cells: entry.row.filter((_, j) => !hiddenCols.has(j)) }));
  const exportRows = visibleRows.filter((entry) => selRows.size === 0 || selRows.has(entry.origIdx));

  const doExport = (kind: 'csv' | 'json' | 'tsv' | 'ndjson' | 'markdown' | 'sql-insert') => {
    if (!result || columns.length === 0) return;
    const cols = visibleColumns;
    const base = baseFilename();
    const colNames = cols.map((c) => c.name);
    const cellRows = exportRows.map((entry) => entry.cells as unknown[]);
    switch (kind) {
      case 'json':
        download(`${base}.json`, 'application/json', exporters.toJson(colNames, cellRows));
        break;
      case 'ndjson':
        download(`${base}.ndjson`, 'application/x-ndjson', exporters.toNdjson(colNames, cellRows));
        break;
      case 'tsv':
        download(`${base}.tsv`, 'text/tab-separated-values', exporters.toTsv(colNames, cellRows));
        break;
      case 'markdown':
        download(`${base}.md`, 'text/markdown', exporters.toMarkdown(colNames, cellRows));
        break;
      case 'sql-insert': {
        const qualified = contextTable
          ? (contextSchema && contextSchema !== 'main' && contextSchema !== 'public'
            ? `${contextSchema}.${contextTable}`
            : contextTable)
          : 'target_table';
        download(`${base}.insert.sql`, 'text/plain',
          exporters.toSqlInsert(connKindRef.current ?? 'sqlite', qualified, colNames, cellRows));
        break;
      }
      default:
        download(`${base}.csv`, 'text/csv;charset=utf-8', exporters.toCsv(colNames, cellRows));
    }
    setExportMenu(false);
  };

  const exportChartPng = () => {
    const svg = document.querySelector('svg[viewBox="0 0 900 340"]');
    if (!svg) return;
    const bg = loadTheme() !== 'vs' ? '#1f2226' : '#ffffff';
    const clone = svg.cloneNode(true) as SVGSVGElement;
    clone.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
    clone.setAttribute('xmlns:xlink', 'http://www.w3.org/1999/xlink');
    clone.setAttribute('width', '900');
    clone.setAttribute('height', '340');
    clone.removeAttribute('style');
    const xml = new XMLSerializer().serializeToString(clone);
    const blob = new Blob([xml], { type: 'image/svg+xml;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const img = new Image();
    img.onload = () => {
      const scale = 2;
      const canvas = document.createElement('canvas');
      canvas.width = 900 * scale;
      canvas.height = 340 * scale;
      const ctx = canvas.getContext('2d')!;
      ctx.fillStyle = bg;
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
      URL.revokeObjectURL(url);
      canvas.toBlob((b) => {
        if (!b) return;
        const burl = URL.createObjectURL(b);
        const a = document.createElement('a');
        const ts = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
        const xName = chartData ? columns[Math.max(0, chartXCol)].name : 'x';
        const yName = chartKind === 'pie' || !chartData ? 'counts' : columns[Math.max(0, chartYCol)].name;
        a.href = burl;
        a.download = `polydb-chart-${chartKind}-${xName}-${yName}-${ts}.png`;
        a.click();
        setTimeout(() => URL.revokeObjectURL(burl), 1000);
      }, 'image/png');
    };
    img.onerror = () => { URL.revokeObjectURL(url); };
    img.src = url;
  };

  const toggleColumn = (j: number) => {
    if (columns.length <= 1) return;
    setHiddenCols((prev) => {
      const next = new Set(prev);
      if (next.has(j)) next.delete(j);
      else {
        const visible = columns.length - prev.size;
        if (visible <= 1) return prev;
        next.add(j);
      }
      return next;
    });
  };

  const showAllColumns = () => setHiddenCols(new Set());
  const hideAllColumns = () => {
    if (columns.length === 0) return;
    setHiddenCols(new Set([columns.length - 1]));
  };

  const cycleTheme = () => {
    const order: EditorTheme[] = ['vs', 'vs-dark', 'hc-black'];
    const i = order.indexOf(theme);
    const next = order[(i + 1) % order.length];
    setTheme(next);
    saveTheme(next);
  };

  const switchToTab = (id: string) => {
    if (id === activeIdRef.current) return;
    const all = tabsRef.current;
    const target = all.find((x) => x.id === id);
    if (!target) return;
    setActiveId(id);
    activeIdRef.current = id;
    setSql(target.sql);
    setParams(target.params);
    setLastQuerySql(null);
    saveTabState(connIdRef.current, { tabs: all, activeId: id, cursor: 0 });
  };

  const addTab = () => {
    const all = tabsRef.current;
    const t: EditorTab = { id: genId(), title: nextTabTitle(all), sql: '-- 示例：\nSELECT 1 AS id, \'hello\' AS greeting;', params: [], context: null };
    const next = [...all, t];
    setTabs(next);
    tabsRef.current = next;
    setActiveId(t.id);
    activeIdRef.current = t.id;
    setSql(t.sql);
    setParams(t.params);
    setLastQuerySql(null);
    saveTabState(connIdRef.current, { tabs: next, activeId: t.id, cursor: 0 });
  };

  const closeTab = (id: string) => {
    const all = tabsRef.current;
    if (all.length <= 1) return;
    const next = all.filter((x) => x.id !== id);
    let newActive = activeIdRef.current;
    if (newActive === id) {
      const idx = all.findIndex((x) => x.id === id);
      const pick = next[Math.min(idx, next.length - 1)];
      newActive = pick.id;
      setSql(pick.sql);
      setParams(pick.params);
      setLastQuerySql(null);
    }
    setTabs(next);
    tabsRef.current = next;
    setActiveId(newActive);
    activeIdRef.current = newActive;
    saveTabState(connIdRef.current, { tabs: next, activeId: newActive, cursor: 0 });
  };

  const renameTab = (id: string, title: string) => {
    const all = tabsRef.current;
    const next = all.map((x) => (x.id === id ? { ...x, title } : x));
    setTabs(next);
    tabsRef.current = next;
    saveTabState(connIdRef.current, { tabs: next, activeId: activeIdRef.current, cursor: 0 });
  };

  const allTemplates = useMemo(
    () => [...TEMPLATES, ...customTemplates.map(toSqlTemplate)],
    [customTemplates],
  );
  const filteredTemplates = useMemo(
    () => filterTemplates(templatesQuery, buildTemplateCtx(), allTemplates),
    [templatesQuery, allTemplates, buildTemplateCtx],
  );
  const flatTemplates = useMemo(
    () => filteredTemplates.filter((t) => !t.requiresTable || !!contextTable),
    [filteredTemplates, contextTable],
  );
  const groupedTemplates = useMemo(() => {
    const m = new Map<string, SqlTemplate[]>();
    for (const t of flatTemplates) {
      const arr = m.get(t.group) ?? [];
      arr.push(t);
      m.set(t.group, arr);
    }
    return Array.from(m.entries());
  }, [flatTemplates]);

  // 选中索引：切模板列表/搜索改变时 clamp 到有效范围
  const clampedIdx = Math.min(Math.max(0, templatesFocusIdx), Math.max(0, flatTemplates.length - 1));
  const selectedTemplatePreview = useMemo(() => {
    const t = flatTemplates[clampedIdx];
    if (!t) return null;
    return renderTemplate(t, buildTemplateCtx());
  }, [flatTemplates, clampedIdx, buildTemplateCtx]);

  const moveTemplatesFocus = (delta: number) => {
    const n = flatTemplates.length;
    if (n === 0) return;
    const cur = clampedIdx;
    const next = (cur + delta + n) % n;
    setTemplatesFocusIdx(next);
  };
  const confirmTemplatesFocus = () => {
    const t = flatTemplates[clampedIdx];
    if (t) insertTemplate(t);
  };
  useEffect(() => {
    if (flatTemplates.length && templatesFocusIdx >= flatTemplates.length) {
      setTemplatesFocusIdx(0);
    }
  }, [flatTemplates.length, templatesFocusIdx]);

  const runFormat = () => {
    const ed = editorRef.current;
    if (!ed) return;
    ed.getAction('editor.action.formatDocument')?.run();
  };
  runFormatRef.current = runFormat;

  const openImportRef = useRef<() => void>(() => {});
  const openImport = () => { setImportOpen(true); };
  openImportRef.current = openImport;

  const actions = (
    <div style={{ position: 'relative', display: 'inline-flex', alignItems: 'center', gap: 6 }}>
      <div className="btn-group">
        <div className="seg-control" title="传输方式">
          <button className={transport === 'http' ? 'active' : ''} onClick={() => setTransport('http')}>HTTP</button>
          <button className={transport === 'ws' ? 'active' : ''} onClick={() => setTransport('ws')}>WS</button>
        </div>
        <button onClick={cycleTheme} title="切换编辑器主题（Light / Dark / Hi-Con）">
          {THEME_LABELS[theme]}
        </button>
        <button className="primary" onClick={() => void run()} disabled={busy || !sql.trim()} title="执行 (Ctrl+Enter)">
          <PlayIcon /> <span>{busy ? '执行中…' : '执行'}</span>
        </button>
        <button onClick={handleCancel} disabled={!busy || !queryId} title="取消（仅 WS）">
          <StopIcon /> <span>取消</span>
        </button>
        <button
          onClick={() => { setTemplatesOpen((v) => !v); setTemplatesQuery(''); setTemplatesFocusIdx(0); }}
          title="插入 SQL 模板 (Alt+P)"
          className={templatesOpen ? 'primary' : ''}
        >
          <span aria-hidden="true" style={{ fontSize: 12 }}>📋</span>
          <span>模板</span>
          <span style={{ fontSize: 10, opacity: 0.7 }}>▾</span>
        </button>
        <button
          onClick={() => setManageCustomOpen(true)}
          title="管理自定义模板"
        >
          <span aria-hidden="true" style={{ fontSize: 12 }}>⚙</span>
          <span>管理模板</span>
        </button>
        <button
          onClick={runFormat}
          title="格式化 SQL (Ctrl+Shift+F / Shift+S)"
          disabled={!sql.trim()}
        >
          <span aria-hidden="true" style={{ fontSize: 12 }}>✨</span>
          <span>格式</span>
        </button>
        <button
          onClick={() => setImportOpen(true)}
          title="从 CSV 导入数据到目标表 (Ctrl+Shift+I)"
          disabled={connKindRef.current === 'redis'}
        >
          <span aria-hidden="true" style={{ fontSize: 12 }}>📥</span>
          <span>导入</span>
        </button>
        {contextLabel && onClearContext && (
          <button onClick={onClearContext} title="清除上下文，回到自由 SQL">
            <span style={{ fontWeight: 600, marginRight: 2 }}>×</span>
            <span>清除上下文</span>
          </button>
        )}
      </div>
      {templatesOpen && (
        <div
          ref={templatesRef}
          style={{
            position: 'absolute',
            top: 'calc(100% + 4px)',
            right: 0,
            width: 380,
            maxHeight: 460,
            overflowY: 'auto',
            background: 'var(--bg)',
            border: '1px solid var(--border)',
            borderRadius: 6,
            boxShadow: '0 8px 24px rgba(0,0,0,0.25)',
            padding: 8,
            zIndex: 60,
            display: 'flex',
            flexDirection: 'column',
            gap: 8,
          }}
        >
          <input
            type="text"
            value={templatesQuery}
            onChange={(e) => { setTemplatesQuery(e.target.value); setTemplatesFocusIdx(0); }}
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') { e.preventDefault(); moveTemplatesFocus(1); }
              else if (e.key === 'ArrowUp') { e.preventDefault(); moveTemplatesFocus(-1); }
              else if (e.key === 'Enter') { e.preventDefault(); confirmTemplatesFocus(); }
              else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); setTemplatesOpen(false); }
            }}
            placeholder="搜索模板 (↑↓ Enter Esc)"
            autoFocus
            style={
              {
                width: '100%',
                padding: '6px 8px',
                fontSize: 12,
                background: 'var(--input-bg, var(--bg))',
                color: 'var(--fg)',
                border: '1px solid var(--border)',
                borderRadius: 4,
                outline: 'none',
              } as CSSProperties
            }
          />
          <div style={{ flex: 1, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 4, maxHeight: 300 }}>
            {!groupedTemplates.length && (
              <div style={{ padding: 12, fontSize: 12, color: 'var(--muted)', textAlign: 'center' }}>
                没有匹配的模板
              </div>
            )}
            {groupedTemplates.map(([group, items]) => (
              <div key={group} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                <div style={
                  {
                    fontSize: 10,
                    fontWeight: 700,
                    color: 'var(--muted)',
                    textTransform: 'uppercase',
                    letterSpacing: '0.06em',
                    padding: '2px 6px',
                    border: '1px solid var(--border)',
                    borderRadius: 3,
                  } as CSSProperties
                }>{group}</div>
                {items.map((t) => {
                  const idx = flatTemplates.indexOf(t);
                  const focused = idx === clampedIdx;
                  return (
                    <button
                      key={t.id}
                      onClick={() => insertTemplate(t)}
                      onMouseEnter={() => setTemplatesFocusIdx(idx)}
                      title={`插入：${t.label}`}
                      style={
                        {
                          textAlign: 'left',
                          padding: '6px 8px',
                          fontSize: 12,
                          background: focused ? 'var(--accent-dim, rgba(120,170,255,0.15))' : 'transparent',
                          color: 'var(--fg)',
                          border: focused ? '1px solid var(--accent, rgba(120,170,255,0.4))' : '1px solid transparent',
                          borderRadius: 4,
                          cursor: 'pointer',
                          display: 'flex',
                          alignItems: 'center',
                          justifyContent: 'space-between',
                          gap: 8,
                        } as CSSProperties
                      }
                    >
                      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {t.label}
                      </span>
                      <span style={
                        {
                          fontSize: 10,
                          color: 'var(--muted)',
                          fontFamily: 'ui-monospace, monospace',
                          flexShrink: 0,
                        } as CSSProperties
                      }>{t.keywords[0] ?? ''}</span>
                    </button>
                  );
                })}
              </div>
            ))}
          </div>
          <div style={
            {
              borderTop: '1px solid var(--border)',
              paddingTop: 6,
              display: 'flex',
              flexDirection: 'column',
              gap: 4,
            } as CSSProperties
          }>
            <div style={
              {
                fontSize: 10,
                color: 'var(--muted)',
                display: 'flex',
                justifyContent: 'space-between',
                padding: '0 6px',
              } as CSSProperties
            }>
              <span>预览（{clampedIdx + 1}/{flatTemplates.length}）</span>
              <span>{connKindRef.current ?? '未连接'}{contextTable ? ` · ${contextSchema ?? '-'}.${contextTable}` : ''}</span>
            </div>
            <pre style={
              {
                margin: 0,
                padding: '6px 8px',
                fontSize: 11,
                fontFamily: 'ui-monospace, monospace',
                background: 'var(--input-bg, rgba(0,0,0,0.15))',
                color: 'var(--fg)',
                border: '1px solid var(--border)',
                borderRadius: 4,
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-word',
                maxHeight: 120,
                overflowY: 'auto',
              } as CSSProperties
            }>{selectedTemplatePreview ?? '（无预览）'}</pre>
          </div>
        </div>
      )}
    </div>
  );

  // 模板下拉：点击外部关闭 + Esc 关闭
  useEffect(() => {
    if (!templatesOpen) return;
    const onDocMouseDown = (ev: MouseEvent) => {
      if (templatesRef.current && !templatesRef.current.contains(ev.target as Node)) {
        setTemplatesOpen(false);
      }
    };
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') {
        ev.stopPropagation();
        setTemplatesOpen(false);
      }
    };
    document.addEventListener('mousedown', onDocMouseDown);
    document.addEventListener('keydown', onKey, true);
    return () => {
      document.removeEventListener('mousedown', onDocMouseDown);
      document.removeEventListener('keydown', onKey, true);
    };
  }, [templatesOpen]);

  // Alt+P 打开模板下拉（避免与 Monaco/输入框冲突）
  useEffect(() => {
    const isEditable = (el: EventTarget | null) => {
      const t = el as HTMLElement | null;
      if (!t || !t.tagName) return false;
      const tag = t.tagName;
      return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || t.isContentEditable;
    };
    const onKey = (ev: KeyboardEvent) => {
      if (ev.altKey && !ev.ctrlKey && !ev.metaKey && (ev.key === 'p' || ev.key === 'P')) {
        if (isEditable(document.activeElement)) return;
        ev.preventDefault();
        setTemplatesOpen((v) => !v);
        setTemplatesQuery('');
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, []);

  // Command palette registrations (workspace-level actions)
  useEffect(() => {
    const unregs: (() => void)[] = [];
    const hasResult = !!result && (result.columns.length > 0 || result.affected_rows > 0);

    unregs.push(registerCommand({
      id: 'ws.templates-open',
      label: '打开 SQL 模板',
      category: '模板',
      hotkey: 'Alt+P',
      keywords: ['template', 'snippets', '模板', '插入', 'sql'],
      enabled: () => !busyRef.current,
      run: () => { setTemplatesOpen((v) => !v); setTemplatesQuery(''); },
    }));
    unregs.push(registerCommand({
      id: 'ws.templates-manage',
      label: '管理自定义模板',
      category: '模板',
      keywords: ['custom', 'manage', '自定义', '管理'],
      run: () => { setManageCustomOpen(true); },
    }));
    unregs.push(registerCommand({
      id: 'ws.format-sql',
      label: '格式化 SQL',
      category: '查询',
      hotkey: 'Ctrl+Shift+F',
      keywords: ['format', 'beautify', '美化', '格式化', 'sql'],
      enabled: () => sqlRef.current.trim().length > 0,
      run: () => { runFormatRef.current(); },
    }));
    unregs.push(registerCommand({
      id: 'ws.lint-sql',
      label: '立即 Lint',
      category: '查询',
      hotkey: 'Ctrl+Alt+L',
      keywords: ['lint', 'check', '检查', '诊断'],
      enabled: () => sqlRef.current.trim().length > 0,
      run: () => {
        if (lintTimerRef.current) { clearTimeout(lintTimerRef.current); lintTimerRef.current = null; }
        const r = lintSql(sqlRef.current, connKindRef.current);
        setLintResult(r);
        const editor = editorRef.current;
        const model = editor?.getModel();
        if (!model) return;
        const markers = r.diagnostics.map((d) => ({
          startLineNumber: d.startLine,
          startColumn: Math.max(1, d.startCol),
          endLineNumber: d.endLine,
          endColumn: Math.max(1, d.endCol),
          message: d.message,
          severity: SEV_NUM[d.severity],
          code: d.code,
          source: 'sql-lint',
        }));
        monacoNS.editor.setModelMarkers(model, 'sql-lint', markers);
        publishEditorStatus({
          message: r.errorCount + r.warningCount + r.infoCount === 0
            ? 'Lint 通过'
            : `Lint: ${severityLabel(r)}`,
          messageAt: Date.now(),
        });
      },
    }));
    unregs.push(registerCommand({
      id: 'ws.import-data',
      label: 'CSV 数据导入',
      category: '数据',
      hotkey: 'Ctrl+Shift+I',
      keywords: ['import', 'csv', '导入', '上传', 'data'],
      enabled: () => (connKindRef.current ?? 'sqlite') !== 'redis',
      run: () => { setImportOpen(true); },
    }));
    for (const t of allTemplates) {
      unregs.push(registerCommand({
        id: `ws.tpl.${t.id}`,
        label: `[模板] ${t.label}`,
        category: '模板',
        keywords: [...t.keywords, 'template', 'sql'],
        enabled: () => !busyRef.current && (!t.requiresTable || !!contextTable),
        run: () => { insertTemplate(t); },
      }));
    }

    unregs.push(registerCommand({
      id: 'ws.run',
      label: '执行 SQL',
      category: '查询',
      hotkey: 'Ctrl+Enter',
      keywords: ['run', 'execute', 'sql', 'query'],
      enabled: () => sqlRef.current.trim().length > 0 && !busyRef.current,
      run: () => { void run(); },
    }));
    unregs.push(registerCommand({
      id: 'ws.commit-tx',
      label: '提交挂起事务',
      category: '事务',
      keywords: ['commit', 'transaction', 'txn', '提交', '事务'],
      enabled: () => !!pendingTxRef.current && !pendingTxBusy,
      run: () => { void resolvePendingTx(true); },
    }));
    unregs.push(registerCommand({
      id: 'ws.rollback-tx',
      label: '回滚挂起事务',
      category: '事务',
      keywords: ['rollback', 'transaction', 'txn', '回滚', '撤销'],
      enabled: () => !!pendingTxRef.current && !pendingTxBusy,
      run: () => { void resolvePendingTx(false); },
    }));
    unregs.push(registerCommand({
      id: 'ws.cancel',
      label: '取消查询',
      category: '查询',
      keywords: ['cancel', 'stop', 'abort'],
      enabled: () => busyRef.current,
      run: () => {
        if (queryId) ws.cancel(queryId);
      },
    }));
    unregs.push(registerCommand({
      id: 'ws.explain',
      label: '运行 EXPLAIN',
      category: '查询',
      keywords: ['explain', 'plan', 'analyze'],
      enabled: () => sqlRef.current.trim().length > 0 && !busyRef.current && !explainBusy,
      run: () => { void runExplain(); },
    }));
    unregs.push(registerCommand({
      id: 'ws.clear-result',
      label: '清除结果',
      category: '结果',
      keywords: ['clear', 'result'],
      enabled: () => !!result || !!explainResult,
      run: () => { setResult(null); setError(null); setExplainResult(null); setExplainError(null); setLastQuerySql(null); setElapsed(null); setQueryId(null); },
    }));
    unregs.push(registerCommand({
      id: 'ws.clear-context',
      label: '清除上下文',
      category: '上下文',
      keywords: ['clear', 'context', 'table'],
      enabled: () => !!contextLabel,
      run: () => { onClearContext?.(); },
    }));
    unregs.push(registerCommand({
      id: 'ws.new-tab',
      label: '新建查询标签',
      category: '标签',
      keywords: ['tab', 'new', 'editor'],
      run: () => { addTab(); },
    }));
    unregs.push(registerCommand({
      id: 'ws.close-tab',
      label: '关闭当前标签',
      category: '标签',
      keywords: ['tab', 'close'],
      enabled: () => tabsRef.current.length > 1,
      run: () => { if (activeIdRef.current) closeTab(activeIdRef.current); },
    }));
    unregs.push(registerCommand({
      id: 'ws.cycle-tab',
      label: '切换标签 (下一个)',
      category: '标签',
      keywords: ['tab', 'next', 'switch'],
      enabled: () => tabsRef.current.length > 1,
      run: () => {
        const all = tabsRef.current;
        const idx = all.findIndex((t) => t.id === activeIdRef.current);
        const next = all[(idx + 1) % all.length];
        if (!next) return;
        setActiveId(next.id);
        activeIdRef.current = next.id;
        setSql(next.sql);
        setParams(next.params);
      },
    }));
    unregs.push(registerCommand({
      id: 'ws.transport-http',
      label: '切换到 HTTP 传输',
      category: '传输',
      keywords: ['http', 'transport', 'msgpack'],
      enabled: () => transport !== 'http',
      run: () => { setTransport('http'); },
    }));
    unregs.push(registerCommand({
      id: 'ws.transport-ws',
      label: '切换到 WS 传输',
      category: '传输',
      keywords: ['ws', 'websocket', 'transport', 'stream'],
      enabled: () => transport !== 'ws',
      run: () => { setTransport('ws'); },
    }));
    unregs.push(registerCommand({
      id: 'ws.chart-toggle',
      label: chartOpen ? '关闭图表' : '打开图表',
      category: '图表',
      keywords: ['chart', 'graph', 'viz', 'visual'],
      enabled: () => hasResult,
      run: () => { setChartOpen((v) => !v); },
    }));
    unregs.push(registerCommand({
      id: 'ws.chart-bar',
      label: '图表 → 柱状图',
      category: '图表',
      keywords: ['bar', 'chart'],
      run: () => { setChartKind('bar'); setChartOpen(true); },
    }));
    unregs.push(registerCommand({
      id: 'ws.chart-line',
      label: '图表 → 折线图',
      category: '图表',
      keywords: ['line', 'chart'],
      run: () => { setChartKind('line'); setChartOpen(true); },
    }));
    unregs.push(registerCommand({
      id: 'ws.chart-pie',
      label: '图表 → 饼图',
      category: '图表',
      keywords: ['pie', 'chart'],
      run: () => { setChartKind('pie'); setChartOpen(true); },
    }));
    unregs.push(registerCommand({
      id: 'ws.chart-bar-grouped',
      label: '柱状图模式 → 并列',
      category: '图表',
      keywords: ['grouped', 'bar', 'mode'],
      run: () => { setChartBarMode('grouped'); },
    }));
    unregs.push(registerCommand({
      id: 'ws.chart-bar-stacked',
      label: '柱状图模式 → 堆叠',
      category: '图表',
      keywords: ['stacked', 'bar', 'mode'],
      run: () => { setChartBarMode('stacked'); },
    }));
    unregs.push(registerCommand({
      id: 'ws.chart-export-png',
      label: '导出图表为 PNG',
      category: '图表',
      keywords: ['png', 'export', 'download', 'image', 'chart'],
      enabled: () => chartOpen && !!result && result.columns.length > 0,
      run: () => { exportChartPng(); },
    }));
    unregs.push(registerCommand({
      id: 'ws.export-csv',
      label: '导出结果 → CSV',
      category: '导出',
      keywords: ['csv', 'export', 'download', 'comma'],
      enabled: () => hasResult && result.statement_type === 'select',
      run: () => { doExport('csv'); },
    }));
    unregs.push(registerCommand({
      id: 'ws.export-json',
      label: '导出结果 → JSON',
      category: '导出',
      keywords: ['json', 'export', 'download'],
      enabled: () => hasResult && result.statement_type === 'select',
      run: () => { doExport('json'); },
    }));
    unregs.push(registerCommand({
      id: 'ws.export-tsv',
      label: '导出结果 → TSV',
      category: '导出',
      keywords: ['tsv', 'export', 'download', 'tab'],
      enabled: () => hasResult && result.statement_type === 'select',
      run: () => { doExport('tsv'); },
    }));
    unregs.push(registerCommand({
      id: 'ws.export-ndjson',
      label: '导出结果 → NDJSON',
      category: '导出',
      keywords: ['ndjson', 'json', 'export', 'download'],
      enabled: () => hasResult && result.statement_type === 'select',
      run: () => { doExport('ndjson'); },
    }));
    unregs.push(registerCommand({
      id: 'ws.export-markdown',
      label: '导出结果 → Markdown',
      category: '导出',
      keywords: ['markdown', 'md', 'export', 'table'],
      enabled: () => hasResult && result.statement_type === 'select',
      run: () => { doExport('markdown'); },
    }));
    unregs.push(registerCommand({
      id: 'ws.export-sql-insert',
      label: '导出结果 → SQL INSERT',
      category: '导出',
      keywords: ['insert', 'sql', 'export', 'import'],
      enabled: () => hasResult && result.statement_type === 'select',
      run: () => { doExport('sql-insert'); },
    }));
    unregs.push(registerCommand({
      id: 'ws.prev-history',
      label: '上一条查询历史',
      category: '编辑器',
      hotkey: 'Ctrl+P',
      keywords: ['history', 'prev', 'up', 'previous'],
      enabled: () => historyRef.current.length > 0,
      run: () => {
        const h = historyRef.current;
        if (h.length === 0) return;
        const idx = Math.min(histIdxRef.current + 1, h.length - 1);
        setHistIdx(idx);
        setSql(h[idx]);
      },
    }));
    unregs.push(registerCommand({
      id: 'ws.next-history',
      label: '下一条查询历史',
      category: '编辑器',
      hotkey: 'Ctrl+N',
      keywords: ['history', 'next', 'down', 'next'],
      enabled: () => histIdxRef.current > -1,
      run: () => {
        const h = historyRef.current;
        const idx = histIdxRef.current - 1;
        if (idx < -1) return;
        setHistIdx(idx);
        if (idx === -1) {
          // Restore to editor state? Keep the current one. No-op.
        } else {
          setSql(h[idx]);
        }
      },
    }));

    return () => { for (const u of unregs) u(); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chartOpen, chartBarMode, result, explainResult, contextLabel, contextTable, transport, queryId, explainBusy, pendingTxBusy, allTemplates]);

  const editorStatusCleanupRef = useRef<(() => void) | null>(null);
  const publishCursor = (ed: Parameters<OnMount>[0]) => {
    const pos = ed.getPosition();
    const sel = ed.getSelection();
    const model = ed.getModel();
    const selLen = sel && model ? model.getValueInRange(sel).length : 0;
    publishEditorStatus({
      cursorLine: pos?.lineNumber ?? 1,
      cursorCol: pos?.column ?? 1,
      cursorSel: selLen,
    });
  };

  useEffect(() => {
    const active = tabs.find((t) => t.id === activeId);
    publishEditorStatus({
      transport,
      language: 'sql',
      rows: result?.rows.length ?? 0,
      columns: result?.columns.length ?? 0,
      elapsed,
      busy: busy || explainBusy,
      paramCount: params.length,
      tabTitle: active?.title ?? '',
      tabCount: tabs.length,
    });
  }, [transport, result, elapsed, busy, explainBusy, params, tabs, activeId]);

  useEffect(() => () => {
    editorStatusCleanupRef.current?.();
    clearEditorStatus();
  }, []);

  // SQL Lint：随 SQL / connKind 变化重新计算，防抖 250ms 后推到 Monaco markers + 状态栏
  useEffect(() => {
    if (lintTimerRef.current) clearTimeout(lintTimerRef.current);
    lintTimerRef.current = setTimeout(() => {
      const r = lintSql(sql, connKindRef.current);
      setLintResult(r);
      publishEditorStatus({
        lintErrors: r.errorCount,
        lintWarnings: r.warningCount,
        lintInfos: r.infoCount,
      });
      const editor = editorRef.current;
      const model = editor?.getModel();
      if (!model) return;
      const markers = r.diagnostics.map((d) => ({
        startLineNumber: d.startLine,
        startColumn: Math.max(1, d.startCol),
        endLineNumber: d.endLine,
        endColumn: Math.max(1, d.endCol),
        message: d.message,
        severity: SEV_NUM[d.severity],
        code: d.code,
        source: 'sql-lint',
      }));
      monacoNS.editor.setModelMarkers(model, 'sql-lint', markers);
    }, 250);
    return () => {
      if (lintTimerRef.current) { clearTimeout(lintTimerRef.current); lintTimerRef.current = null; }
    };
  }, [sql, connId]);

  // 卸载时清 markers，防切 Tab 残留
  useEffect(() => () => {
    const editor = editorRef.current;
    const model = editor?.getModel();
    if (model) monacoNS.editor.setModelMarkers(model, 'sql-lint', []);
  }, []);

  // Ctrl+Shift+F 全局快捷键（避开 INPUT/TEXTAREA/SELECT/contenteditable）
  useEffect(() => {
    const h = (ev: KeyboardEvent) => {
      if (!((ev.ctrlKey || ev.metaKey) && ev.shiftKey)) return;
      const t = ev.target as HTMLElement | null;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
      if (ev.key === 'F' || ev.key === 'f') {
        ev.preventDefault();
        runFormatRef.current();
      } else if (ev.key === 'I' || ev.key === 'i') {
        if ((connKindRef.current ?? 'sqlite') === 'redis') return;
        ev.preventDefault();
        openImportRef.current();
      }
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, []);

  useEffect(() => {
    const hSettings = (e: Event) => {
      const s = (e as CustomEvent<{ settings: import('../lib/settings').Settings }>).detail?.settings;
      if (!s) return;
      setSettings(s);
    };
    const hTheme = () => setTheme(loadSettings().theme);
    const hResetHeight = () => {
      setEditorHeight(240);
      try { localStorage.setItem(EDITOR_HEIGHT_KEY, '240'); } catch { /* ignore */ }
    };
    window.addEventListener('polydb-settings-changed', hSettings);
    window.addEventListener('polydb-theme-changed', hTheme);
    window.addEventListener('polydb-reset-editor-height', hResetHeight);
    return () => {
      window.removeEventListener('polydb-settings-changed', hSettings);
      window.removeEventListener('polydb-theme-changed', hTheme);
      window.removeEventListener('polydb-reset-editor-height', hResetHeight);
    };
  }, []);

  return (
    <>
    <CollapsiblePane title="查询" variant="main" actions={actions}>
      <div className="query-area" style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }}>
        <TabStrip
          tabs={tabs}
          activeId={activeId}
          onSelect={switchToTab}
          onClose={closeTab}
          onAdd={addTab}
          onRename={renameTab}
        />
        {contextLabel && (
          <div className="muted" style={{ padding: '4px 10px', fontSize: 11, borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', gap: 6 }} title="点击左侧表可重新选中，× 清除">
            <span>针对：<strong style={{ color: 'var(--fg)', fontWeight: 600 }}>{contextLabel}</strong></span>
            {onClearContext && (
              <button
                onClick={onClearContext}
                title="清除上下文"
                style={{ marginLeft: 'auto', background: 'transparent', border: '1px solid var(--border)', borderRadius: 3, padding: '0 5px', cursor: 'pointer', color: 'var(--muted)' }}
              >
                ×
              </button>
            )}
          </div>
        )}
        <div className="query-editor" style={{ flex: `0 0 ${editorHeight}px`, height: editorHeight }}>
          <Editor
            language="sql"
            theme={theme}
            value={sql}
            onChange={(v) => {
              setSql(v ?? '');
              if (histIdxRef.current >= 0) setHistIdx(-1);
            }}
            onMount={handleMount}
            options={{
              minimap: { enabled: settings.editorMinimap },
              fontSize: settings.editorFontSize,
              scrollBeyondLastLine: false,
              automaticLayout: true,
              tabSize: settings.editorTabSize,
              wordWrap: settings.editorWordWrap ? 'on' : 'off',
              lineNumbers: settings.editorLineNumbers,
              renderLineHighlight: 'line',
              padding: { top: 6, bottom: 6 },
            }}
          />
        </div>
        <div
          className="pane-resize-h"
          title="拖拽调整编辑器高度（120–800px）"
          onMouseDown={(e) => {
            const startH = editorHeight;
            let finalH = startH;
            startDragResize(e, {
              axis: 'y',
              cursor: 'row-resize',
              onResize: (delta) => {
                finalH = Math.max(120, Math.min(800, Math.round(startH + delta)));
                setEditorHeight(finalH);
              },
              onEnd: () => {
                try { localStorage.setItem(EDITOR_HEIGHT_KEY, String(finalH)); } catch { /* ignore */ }
              },
            });
          }}
        />
        {params.length > 0 && (
          <div
            style={{
              flex: '0 0 auto',
              maxHeight: 160,
              overflow: 'auto',
              padding: '4px 10px 6px',
              border: '1px solid var(--border)',
              borderTop: 'none',
              background: 'var(--bg-alt, transparent)',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4, fontSize: 11, color: 'var(--muted)' }}>
              <strong style={{ color: 'var(--fg)' }}>参数</strong>
              <span>{params.length} 个 <code style={{ fontFamily: 'monospace' }}>?</code> 占位符</span>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
              {params.map((p, i) => (
                <div key={i} style={{ display: 'grid', gridTemplateColumns: '24px 96px 1fr', gap: 6, alignItems: 'center' }}>
                  <span style={{ textAlign: 'center', fontSize: 11, color: 'var(--muted)', fontFamily: 'monospace' }}>?{i + 1}</span>
                  <select
                    value={p.type}
                    onChange={(e) => {
                      const t = e.target.value as ParamType;
                      setParams((prev) => prev.map((x, j) => j === i ? { type: t, value: t === 'null' ? '' : x.value } : x));
                    }}
                    style={{
                      padding: '1px 3px',
                      fontSize: 12,
                      fontFamily: 'inherit',
                      background: 'var(--bg)',
                      color: 'var(--fg)',
                      border: '1px solid var(--border)',
                      borderRadius: 2,
                    }}
                  >
                    <option value="string">string</option>
                    <option value="number">number</option>
                    <option value="bool">bool</option>
                    <option value="null">null</option>
                  </select>
                  <input
                    type="text"
                    value={p.value}
                    disabled={p.type === 'null'}
                    placeholder={p.type === 'bool' ? 'true / false' : p.type === 'number' ? '123（空= NULL）' : p.type === 'null' ? 'NULL' : 'value'}
                    onChange={(e) => setParams((prev) => prev.map((x, j) => j === i ? { ...x, value: e.target.value } : x))}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') { e.preventDefault(); void run(); }
                    }}
                    style={{
                      padding: '2px 4px',
                      fontSize: 12,
                      fontFamily: 'inherit',
                      background: 'var(--bg)',
                      color: 'var(--fg)',
                      border: '1px solid var(--border)',
                      borderRadius: 2,
                      outline: 'none',
                    }}
                  />
                </div>
              ))}
            </div>
          </div>
        )}
        {error && <div className="error-box">{error}</div>}
        {multiResults && multiResults.length > 1 && (
          <div style={{ padding: '4px 10px', borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'nowrap', overflowX: 'auto' }}>
            <span style={{ fontSize: 11, color: 'var(--muted)', marginRight: 4, flexShrink: 0 }}>
              {(() => {
                const done = multiResults.filter((e) => e.status === 'ok' || e.status === 'err' || e.status === 'cancelled').length;
                const total = multiResults.length;
                return `${done}/${total}`;
              })()}
            </span>
            {busy && (
              <button
                className="btn-ico"
                onClick={() => void cancelMulti()}
                style={{
                  fontSize: 11,
                  padding: '1px 8px',
                  borderRadius: 3,
                  border: '1px solid var(--danger, #c0392b)',
                  background: 'transparent',
                  color: 'var(--danger, #c0392b)',
                  cursor: 'pointer',
                  flexShrink: 0,
                }}
                title="取消剩余语句（Esc）"
              >
                ■ 取消
              </button>
            )}
            {!busy && multiResults.some((e) => e.status === 'err') && (
              <button
                className="btn-ico"
                onClick={() => void rerunFailed()}
                style={{
                  fontSize: 11,
                  padding: '1px 8px',
                  borderRadius: 3,
                  border: '1px solid var(--warning, #e67e22)',
                  background: 'transparent',
                  color: 'var(--warning, #e67e22)',
                  cursor: 'pointer',
                  flexShrink: 0,
                }}
                title="仅重跑失败的语句"
              >
                ↻ 重跑失败
              </button>
            )}
            {/* 总进度条：宽度按 done/total 变化 */}
            {!busy && (
              <span
                style={{
                  width: 60, height: 3, borderRadius: 2, background: 'var(--border)', overflow: 'hidden', flexShrink: 0,
                }}
                title="总进度"
              >
                <span style={{
                  display: 'block', height: '100%',
                  width: `${(multiResults.filter((e) => e.status === 'ok' || e.status === 'err' || e.status === 'cancelled').length / multiResults.length) * 100}%`,
                  background: multiResults.some((e) => e.status === 'err') ? 'var(--warning, #e67e22)' : 'var(--accent)',
                  transition: 'width 200ms ease-out',
                }} />
              </span>
            )}
            {busy && (
              <span
                style={{
                  width: 60, height: 3, borderRadius: 2, background: 'var(--border)', overflow: 'hidden', flexShrink: 0,
                }}
                title="执行中"
              >
                <span style={{
                  display: 'block', height: '100%',
                  width: `${(multiResults.filter((e) => e.status === 'ok' || e.status === 'err' || e.status === 'cancelled' || e.status === 'skipped').length / multiResults.length) * 100}%`,
                  background: 'var(--accent)',
                  transition: 'width 200ms ease-out',
                }} />
              </span>
            )}
            {multiResults.map((e, i) => {
              const sql = e.sql.trimStart().toUpperCase();
              const kw = sql.split(/\s+/)[0]?.slice(0, 8) ?? '?';
              const status = e.status;
              const active = i === activeMultiIdx;
              // 状态徽章：ok ✓ / err ✕ / cancelled ⊘ / skipped ⊘(灰) / running … / pending ·
              const badge = (() => {
                switch (status) {
                  case 'ok': return { icon: '✓', color: 'var(--accent)' };
                  case 'err': return { icon: '✕', color: 'var(--danger, #c0392b)' };
                  case 'cancelled': return { icon: '⊘', color: 'var(--warning, #e67e22)' };
                  case 'skipped': return { icon: '·', color: 'var(--muted)' };
                  case 'running': return { icon: '…', color: 'var(--accent)' };
                  case 'pending': return { icon: '·', color: 'var(--muted)' };
                }
              })();
              const dim = status === 'skipped' || status === 'pending';
              return (
                <button
                  key={i}
                  className="btn-ico"
                  onClick={() => applyActiveMulti(i)}
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 4,
                    padding: '2px 8px',
                    fontSize: 11,
                    fontFamily: 'monospace',
                    borderRadius: 3,
                    border: `1px solid ${active ? 'var(--accent)' : 'var(--border)'}`,
                    background: active ? 'var(--accent-dim, rgba(0,0,0,0.06))' : 'var(--bg)',
                    color: dim && !active ? 'var(--muted)' : active ? 'var(--accent)' : 'var(--text)',
                    cursor: 'pointer',
                    flexShrink: 0,
                    opacity: dim && !active ? 0.6 : 1,
                  }}
                  title={
                    status === 'skipped' ? `语句 ${i + 1}（未执行）: ${e.sql.slice(0, 120)}${e.sql.length > 120 ? '…' : ''}`
                    : status === 'cancelled' ? `语句 ${i + 1}（取消）: ${e.sql.slice(0, 120)}${e.sql.length > 120 ? '…' : ''}`
                    : `语句 ${i + 1}: ${e.sql.slice(0, 120)}${e.sql.length > 120 ? '…' : ''}`
                  }
                >
                  <span>{i + 1}</span>
                  <span>{kw}</span>
                  <span style={{ color: badge.color }}>{badge.icon}</span>
                  {e.elapsed != null && <span style={{ color: 'var(--muted)' }}>{e.elapsed.toFixed(0)}ms</span>}
                </button>
              );
            })}
          </div>
        )}
        <div className="query-result" style={{ flex: 1, overflow: 'auto', minHeight: 0 }}>
          {!result && !error && (
            (() => {
              // 多语句下 active 条目是 cancelled/skipped/pending/running 时给一条明确的空态说明，
              // 避免下游空态「在上方输入 SQL 后执行」的误导。
              if (multiResults && multiResults.length > 1) {
                const active = multiResults[activeMultiIdx];
                if (active && !active.result && !active.error) {
                  const msgMap: Partial<Record<MultiStatus, string>> = {
                    pending: '该语句尚未执行',
                    running: '该语句执行中…',
                    cancelled: '该语句被取消，未返回结果',
                    skipped: '该语句未执行（前面的执行已取消）',
                  };
                  const msg = msgMap[active.status];
                  if (msg) {
                    return (
                      <div style={{ padding: '16px 12px', color: 'var(--muted)', fontSize: 12, textAlign: 'center' }}>
                        {msg}
                      </div>
                    );
                  }
                }
              }
              return <div className="empty">在上方输入 SQL 后执行。选中左侧表会自动填充；↑/↓ 或 Ctrl+P/N 循环查询历史。</div>;
            })()
          )}
          {result && (
            <>
              <div className="result-meta" style={{ padding: '4px 10px', borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', position: 'relative' }}>
                {result.statement_type && (
                  <span className="badge" style={{ background: 'var(--accent-dim)', color: 'var(--accent)' }}>
                    {result.statement_type}
                  </span>
                )}
                <button
                  className="btn-ico"
                  onClick={() => void runExplain()}
                  disabled={!isSelect || explainBusy || busy}
                  title={isSelect ? '分析查询计划（按当前 DB 类型加 EXPLAIN / EXPLAIN QUERY PLAN / EXPLAIN FORMAT=JSON 等）' : '仅 SELECT 语句可分析执行计划'}
                  style={{ opacity: (!isSelect || explainBusy || busy) ? 0.5 : 1, cursor: (!isSelect || explainBusy || busy) ? 'not-allowed' : 'pointer' }}
                >
                  ⚙ 执行计划{explainBusy ? '…' : ''}
                </button>
                <button
                  className="btn-ico"
                  onClick={() => {
                    setChartXCol(0);
                    setChartYCol(-1);
                    setChartOpen((v) => !v);
                  }}
                  disabled={!isSelect || columns.length === 0}
                  title={isSelect ? '图表可视化（柱/折线/饼）' : '仅 SELECT 语句可图表化'}
                  style={{ opacity: (!isSelect || columns.length === 0) ? 0.5 : 1, color: chartOpen ? 'var(--accent)' : undefined }}
                >
                  📊 {chartOpen ? '关闭图表' : '图表'}
                </button>
                <span className="badge" style={{ background: 'var(--bg)', color: 'var(--text)', fontFamily: 'monospace', border: '1px solid var(--border)' }} title="服务端执行耗时">
                  ⏱ {result.execution_time_ms < 1 ? result.execution_time_ms.toFixed(3) : result.execution_time_ms.toFixed(2)} ms
                </span>
                {elapsed != null && (
                  <span className="badge" style={{ background: 'var(--bg)', color: elapsed - result.execution_time_ms > 300 ? 'var(--danger)' : 'var(--muted)', fontFamily: 'monospace', border: '1px solid var(--border)' }} title="客户端往返耗时（含网络 + 序列化）">
                    ↔ {elapsed.toFixed(1)} ms
                  </span>
                )}
                {statSummary && statSummary.count >= 3 && (
                  <span className="badge" style={{ background: 'var(--bg)', color: 'var(--muted)', fontFamily: 'monospace', border: '1px solid var(--border)' }} title="同一 SQL 最近运行的 P95（含 P50/Min/Max，点开看详情）">
                    P95 {statSummary.p95.toFixed(1)} ms
                  </span>
                )}
                {lastQuerySql && (
                  <div style={{ position: 'relative' }}>
                    <button
                      className="btn-ico"
                      onClick={() => { setStatsMenu((v) => !v); setExportMenu(false); setColMenu(false); }}
                      title="运行历史统计（同一 SQL 聚合）"
                      disabled={!statSummary}
                      style={{ opacity: statSummary ? 1 : 0.5 }}
                    >
                      ⏲ 历史
                    </button>
                    {statsMenu && statSummary && (
                      <div
                        style={{ position: 'absolute', top: '100%', right: 0, background: 'var(--bg-elev)', border: '1px solid var(--border)', borderRadius: 3, boxShadow: '0 2px 8px rgba(0,0,0,0.15)', padding: 8, zIndex: 20, minWidth: 240 }}
                        onMouseLeave={() => setStatsMenu(false)}
                      >
                        <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6 }}>
                          <span style={{ fontSize: 11, color: 'var(--muted)' }}>相同 SQL 运行</span>
                          <strong style={{ fontSize: 12 }}>{statSummary.count}</strong>
                          <span style={{ fontSize: 11, color: 'var(--muted)' }}>次</span>
                          <span style={{ marginLeft: 'auto', fontSize: 10, color: 'var(--muted)' }}>
                            最近 {new Date(statSummary.lastTs).toLocaleString()}
                          </span>
                        </div>
                        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(5, auto)', gap: 6, fontFamily: 'monospace', fontSize: 11 }}>
                          {([['Min', statSummary.min], ['P50', statSummary.p50], ['Avg', statSummary.avg], ['P95', statSummary.p95], ['Max', statSummary.max]] as const).map(([k, v]) => (
                            <div key={k} style={{ background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 2, padding: '4px 6px', textAlign: 'center' }}>
                              <div style={{ color: 'var(--muted)', fontSize: 10 }}>{k}</div>
                              <div style={{ color: 'var(--text)' }}>{v < 1 ? v.toFixed(2) : v.toFixed(1)}<span style={{ color: 'var(--muted)' }}>ms</span></div>
                            </div>
                          ))}
                        </div>
                        {stats.filter((s) => hashSql(s.sql) === hashSql(lastQuerySql)).slice(0, 5).length > 0 && (
                          <div style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid var(--border)' }}>
                            <div style={{ color: 'var(--muted)', fontSize: 11, marginBottom: 2 }}>最近 5 次运行</div>
                            <div style={{ display: 'flex', flexDirection: 'column', gap: 1, fontFamily: 'monospace', fontSize: 11 }}>
                              {stats.filter((s) => hashSql(s.sql) === hashSql(lastQuerySql)).slice(0, 5).map((s, i) => (
                                <div key={i} style={{ display: 'grid', gridTemplateColumns: '60px 60px 50px', gap: 4, padding: '1px 2px' }}>
                                  <span style={{ color: 'var(--text)' }}>{s.elapsed_ms < 1 ? s.elapsed_ms.toFixed(2) : s.elapsed_ms.toFixed(1)}ms</span>
                                  <span style={{ color: 'var(--muted)' }}>{s.rows} 行</span>
                                  <span style={{ color: 'var(--muted)' }}>{new Date(s.ts).toLocaleTimeString()}</span>
                                </div>
                              ))}
                            </div>
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                )}
                {(rowCount > 0 || columns.length > 0) && (
                  <span className="badge" style={{ background: 'var(--bg)', color: 'var(--muted)', fontFamily: 'monospace', border: '1px solid var(--border)' }} title="结果集规模">
                    {rowCount} 行 · {columns.length} 列
                  </span>
                )}
                {selRows.size > 0 ? (
                  <>
                    <span style={{ color: 'var(--accent)' }}>{selRows.size} 行已选中</span>
                    {editEnabled && selRows.size >= 2 && (
                      <button className="btn-ico" onClick={() => setBatchOpen((v) => !v)} title="批量编辑选中行的某一列" style={{ color: 'var(--accent)' }}>
                        批量编辑
                      </button>
                    )}
                    <button className="btn-ico" onClick={() => copySelected('csv')} title="复制选中行 CSV">CSV</button>
                    <button className="btn-ico" onClick={() => copySelected('json')} title="复制选中行 JSON">JSON</button>
                    <button className="btn-ico" onClick={() => setSelRows(new Set())} title="取消选中">清除</button>
                  </>
                ) : (
                  <button className="btn-ico" onClick={copyAsCsv} title="复制全部结果 CSV（含表头，仅可见列）" disabled={visibleColumns.length === 0}>
                    复制 CSV
                  </button>
                )}
                {columns.length > 0 && (
                  <div style={{ position: 'relative' }}>
                    <button
                      className="btn-ico"
                      onClick={() => { setExportMenu((v) => !v); setColMenu(false); }}
                      title="导出为文件"
                    >
                      导出 ▾
                    </button>
                    {exportMenu && (
                      <div
                        style={{ position: 'absolute', top: '100%', left: 0, background: 'var(--bg-elev)', border: '1px solid var(--border)', borderRadius: 3, boxShadow: '0 2px 8px rgba(0,0,0,0.15)', padding: 2, zIndex: 20, minWidth: 140 }}
                        onMouseLeave={() => setExportMenu(false)}
                      >
                        <button className="btn-ico" style={{ display: 'block', width: '100%', textAlign: 'left', margin: '1px 4px' }} onClick={() => doExport('csv')} title="下载 CSV（含 BOM，Excel 友好）">CSV</button>
                        <button className="btn-ico" style={{ display: 'block', width: '100%', textAlign: 'left', margin: '1px 4px' }} onClick={() => doExport('json')} title="下载 JSON">JSON</button>
                        <button className="btn-ico" style={{ display: 'block', width: '100%', textAlign: 'left', margin: '1px 4px' }} onClick={() => doExport('ndjson')} title="下载 NDJSON（每行一对象）">NDJSON</button>
                        <button className="btn-ico" style={{ display: 'block', width: '100%', textAlign: 'left', margin: '1px 4px' }} onClick={() => doExport('tsv')} title="下载 TSV（制表符）">TSV</button>
                        <button className="btn-ico" style={{ display: 'block', width: '100%', textAlign: 'left', margin: '1px 4px' }} onClick={() => doExport('markdown')} title="复制为 Markdown 表格片段">Markdown</button>
                        <button className="btn-ico" style={{ display: 'block', width: '100%', textAlign: 'left', margin: '1px 4px' }} onClick={() => doExport('sql-insert')} title="导出为 SQL INSERT 语句">SQL INSERT</button>
                        <div style={{ height: 1, background: 'var(--border)', margin: '3px 4px' }} />
                        <div style={{ padding: '2px 6px', color: 'var(--muted)', fontSize: 11 }}>
                          {selRows.size > 0 ? `${selRows.size} 行` : `${rowCount} 行`} · {visibleColumns.length}/{columns.length} 列
                        </div>
                      </div>
                    )}
                  </div>
                )}
                {columns.length > 0 && (
                  <div style={{ position: 'relative' }}>
                    <button
                      className="btn-ico"
                      onClick={() => { setColMenu((v) => !v); setExportMenu(false); }}
                      title="列显示开关"
                    >
                      列 ▾
                    </button>
                    {colMenu && (
                      <div
                        style={{ position: 'absolute', top: '100%', left: 0, background: 'var(--bg-elev)', border: '1px solid var(--border)', borderRadius: 3, boxShadow: '0 2px 8px rgba(0,0,0,0.15)', padding: 4, zIndex: 20, minWidth: 180, maxWidth: 320, maxHeight: 320, overflow: 'auto' }}
                        onMouseLeave={() => setColMenu(false)}
                      >
                        <div style={{ display: 'flex', gap: 4, marginBottom: 4 }}>
                          <button className="btn-ico" style={{ flex: 1, fontSize: 11 }} onClick={showAllColumns}>全部显示</button>
                          <button className="btn-ico" style={{ flex: 1, fontSize: 11 }} onClick={hideAllColumns}>只留一列</button>
                        </div>
                        <div style={{ height: 1, background: 'var(--border)', margin: '2px 0 4px' }} />
                        {columns.map((c, j) => (
                          <label
                            key={c.name}
                            style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '2px 4px', cursor: 'pointer', borderRadius: 2 }}
                            onMouseEnter={(e) => (e.currentTarget.style.background = 'var(--bg-hover)')}
                            onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                          >
                            <input
                              type="checkbox"
                              checked={!hiddenCols.has(j)}
                              onChange={() => toggleColumn(j)}
                              style={{ margin: 0 }}
                            />
                            <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={`${c.name} · ${c.type}`}>{c.name}</span>
                            <span className="type-badge" style={{ fontSize: 10 }}>{c.type}</span>
                          </label>
                        ))}
                      </div>
                    )}
                  </div>
                )}
                {result.affected_rows > 0 && <span>影响 {result.affected_rows} 行</span>}
                {result.truncated && <span style={{ color: 'var(--danger)' }}>结果已截断</span>}
                {result.has_more && result.total_rows != null && (
                  <span className="muted">共 {result.total_rows} 行</span>
                )}
              </div>
              {columns.length === 0 ? (
                <div className="empty">语句执行成功，无结果集。</div>
              ) : (
                <>
                  {chartOpen && (
                    <div style={{ padding: '8px 10px', borderBottom: '1px solid var(--border)', background: 'var(--bg-alt, transparent)' }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 8, flexWrap: 'wrap' }}>
                        <strong style={{ fontSize: 12, color: 'var(--muted)' }}>图表</strong>
                        <div style={{ display: 'flex', gap: 2, margin: '0 4px' }}>
                          {([['bar', '柱状'], ['line', '折线'], ['pie', '饼图']] as const).map(([k, lbl]) => (
                            <button
                              key={k}
                              className="btn-ico"
                              onClick={() => setChartKind(k)}
                              style={{
                                padding: '2px 8px',
                                fontSize: 11,
                                background: chartKind === k ? 'var(--accent-dim)' : 'transparent',
                                color: chartKind === k ? 'var(--fg)' : 'var(--muted)',
                                border: '1px solid var(--border)',
                                borderRadius: 2,
                              }}
                            >
                              {lbl}
                            </button>
                          ))}
                        </div>
                        <span style={{ fontSize: 11, color: 'var(--muted)' }}>X</span>
                        <select
                          value={chartXCol}
                          onChange={(e) => setChartXCol(Number(e.target.value))}
                          style={{ padding: '2px 4px', fontSize: 11, fontFamily: 'inherit', background: 'var(--bg)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 2, maxWidth: 180 }}
                        >
                          {columns.map((c, j) => <option key={j} value={j}>{c.name} · {c.type}</option>)}
                        </select>
                        {chartKind !== 'pie' && (
                          <>
                            <span style={{ fontSize: 11, color: 'var(--muted)' }}>Y</span>
                            <select
                              value={chartYCol === -1 ? (chartData?.yIdx ?? 0) : chartYCol}
                              onChange={(e) => setChartYCol(Number(e.target.value))}
                              style={{ padding: '2px 4px', fontSize: 11, fontFamily: 'inherit', background: 'var(--bg)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 2, maxWidth: 180 }}
                            >
                              {columns.map((c, j) => <option key={j} value={j}>{c.name} · {c.type}</option>)}
                            </select>
                            <span style={{ fontSize: 11, color: 'var(--muted)' }}>分组</span>
                            <select
                              value={chartGroupCol}
                              onChange={(e) => setChartGroupCol(Number(e.target.value))}
                              style={{ padding: '2px 4px', fontSize: 11, fontFamily: 'inherit', background: 'var(--bg)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 2, maxWidth: 180 }}
                            >
                              <option value={-1}>无（单系列）</option>
                              {columns.map((c, j) => j !== chartXCol && j !== chartYCol ? <option key={j} value={j}>{c.name} · {c.type}</option> : null)}
                            </select>
                            {chartKind === 'bar' && (
                              <>
                                <div style={{ display: 'flex', gap: 2, margin: '0 2px' }}>
                                  <button
                                    className="btn-ico"
                                    onClick={() => setChartBarMode('grouped')}
                                    disabled={chartGroupCol < 0}
                                    title="并列（同一 X 下 series 并排）"
                                    style={{ padding: '2px 6px', fontSize: 11, background: chartBarMode === 'grouped' ? 'var(--accent-dim)' : 'transparent', color: chartBarMode === 'grouped' ? 'var(--fg)' : 'var(--muted)', border: '1px solid var(--border)', borderRadius: 2, opacity: chartGroupCol < 0 ? 0.4 : 1 }}
                                  >
                                    并列
                                  </button>
                                  <button
                                    className="btn-ico"
                                    onClick={() => setChartBarMode('stacked')}
                                    disabled={chartGroupCol < 0}
                                    title="堆叠（同一 X 下 series 上下堆叠）"
                                    style={{ padding: '2px 6px', fontSize: 11, background: chartBarMode === 'stacked' ? 'var(--accent-dim)' : 'transparent', color: chartBarMode === 'stacked' ? 'var(--fg)' : 'var(--muted)', border: '1px solid var(--border)', borderRadius: 2, opacity: chartGroupCol < 0 ? 0.4 : 1 }}
                                  >
                                    堆叠
                                  </button>
                                </div>
                              </>
                            )}
                          </>
                        )}
                        <span style={{ fontSize: 11, color: 'var(--muted)', marginLeft: 4 }}>
                          {chartData
                            ? (chartKind === 'pie'
                                ? `${new Set(chartData.labels).size} 类`
                                : chartData.series
                                  ? `${chartData.labels.length} X × ${chartData.series.length} 系列`
                                  : `${chartData.values!.length} 点`)
                            : ''}
                        </span>
                        {chartKind !== 'pie' && chartData && (
                          <span style={{ fontSize: 11, color: 'var(--muted)' }}>
                            {(() => {
                              const all = chartData.series ? chartData.series.flatMap((s) => s.values) : (chartData.values ?? []);
                              return `· Σ ${all.reduce((a, b) => a + b, 0).toFixed(2)}`;
                            })()}
                          </span>
                        )}
                        <button
                          className="btn-ico"
                          onClick={exportChartPng}
                          disabled={!!chartData && chartData.labels.length === 0}
                          title="下载为 PNG 图片（2× 分辨率）"
                          style={{ marginLeft: 'auto', fontSize: 11 }}
                        >
                          PNG
                        </button>
                      </div>
                      {chartData ? (
                        (() => {
                          const W = 900, H = 340;
                          const padL = 54, padR = 16, padT = 16, padB = 54;
                          const plotW = W - padL - padR;
                          const plotH = H - padT - padB;
                          const plotLeft = padL, plotRight = W - padR, plotTop = padT, plotBottom = H - padB;
                          const dark = loadTheme() !== 'vs';
                          const C = dark
                            ? { bg: '#1f2226', border: '#2b2f36', muted: '#8f96a3', text: '#d4d8df', accent: '#5b9ce6', accentDim: 'rgba(91,156,230,0.35)' }
                            : { bg: '#ffffff', border: '#d8dce0', muted: '#6c737e', text: '#1f2226', accent: '#2d68c8', accentDim: 'rgba(45,104,200,0.25)' };
                          const fmtTick = (v: number) => {
                            if (v === 0) return '0';
                            const a = Math.abs(v);
                            if (a >= 1e6) return (v / 1e6).toFixed(a >= 1e7 ? 0 : 1) + 'M';
                            if (a >= 1e3) return (v / 1e3).toFixed(a >= 1e4 ? 0 : 1) + 'k';
                            if (a >= 1) return v.toFixed(a < 10 && v % 1 !== 0 ? 2 : 0);
                            if (a >= 0.01) return v.toFixed(2);
                            return v.toFixed(4);
                          };
                          const trunc = (s: string, n: number) => (s.length > n ? s.slice(0, n - 1) + '…' : s);
                          if (chartKind === 'pie') {
                            const counts = new Map<string, number>();
                            for (const lb of chartData.labels) counts.set(lb, (counts.get(lb) ?? 0) + 1);
                            const items = Array.from(counts.entries());
                            const total = items.reduce((a, [, c]) => a + c, 0);
                            const cx = W * 0.34, cy = H / 2, r = Math.min(cx, cy) - 20;
                            let acc = -Math.PI / 2;
                            const arcs = items.map(([label, count]) => {
                              const ang = (count / total) * Math.PI * 2;
                              const s = acc, e = acc + ang;
                              acc = e;
                              return { label, count, pct: count / total, s, e };
                            });
                            const palette = [C.accent, '#e8a24b', '#4b9ae8', '#a85be8', '#4be8a2', '#e84b4b', '#7ec7a2', '#a2c74b', '#4ba2e8', '#c74ba2'];
                            return (
                              <svg viewBox={`0 0 ${W} ${H}`} style={{ width: '100%', height: H, display: 'block' }}>
                                <rect x={0} y={0} width={W} height={H} fill={C.bg} />
                                <g>
                                  {arcs.map((a, i) => {
                                    const large = a.e - a.s > Math.PI ? 1 : 0;
                                    const x1 = cx + r * Math.cos(a.s), y1 = cy + r * Math.sin(a.s);
                                    const x2 = cx + r * Math.cos(a.e), y2 = cy + r * Math.sin(a.e);
                                    if (a.e - a.s >= Math.PI * 2 - 1e-6) {
                                      return <circle key={i} cx={cx} cy={cy} r={r} fill={palette[i % palette.length]} opacity={0.85} />;
                                    }
                                    const d = `M ${cx} ${cy} L ${x1.toFixed(2)} ${y1.toFixed(2)} A ${r} ${r} 0 ${large} 1 ${x2.toFixed(2)} ${y2.toFixed(2)} Z`;
                                    return <path key={i} d={d} fill={palette[i % palette.length]} opacity={0.85} stroke={C.bg} strokeWidth={1} />;
                                  })}
                                  {arcs.map((a, i) => {
                                    if (a.pct < 0.04) return null;
                                    const mid = (a.s + a.e) / 2;
                                    const rr = r * 0.7;
                                    const tx = cx + rr * Math.cos(mid);
                                    const ty = cy + rr * Math.sin(mid);
                                    return (
                                      <text key={`lbl-${i}`} x={tx} y={ty} fontSize={11} textAnchor="middle" dominantBaseline="middle" style={{ fill: C.bg, fontWeight: 600 }}>
                                        {Math.round(a.pct * 100)}%
                                      </text>
                                    );
                                  })}
                                  <text x={W - padR} y={padT} textAnchor="end" fontSize={11} style={{ fill: C.muted }}>共 {total} 行</text>
                                </g>
                                <g transform={`translate(${W * 0.62}, ${padT})`}>
                                  {arcs.slice(0, 12).map((a, i) => (
                                    <g key={i} transform={`translate(0, ${i * 20})`}>
                                      <rect width={10} height={10} fill={palette[i % palette.length]} opacity={0.85} />
                                      <text x={16} y={9} fontSize={11} style={{ fill: C.text }}>
                                        {trunc(a.label, 22)} · {a.count} ({(a.pct * 100).toFixed(1)}%)
                                      </text>
                                    </g>
                                  ))}
                                  {arcs.length > 12 && (
                                    <text y={arcs.length * 20 + 4} fontSize={11} style={{ fill: C.muted }}>
                                      … +{arcs.length - 12} 类
                                    </text>
                                  )}
                                </g>
                              </svg>
                            );
                          }
                          const series = chartData.series ?? [{ label: '', values: chartData.values! }];
                          const nSeries = series.length;
                          const n = chartData.labels.length;
                          const isMulti = nSeries > 1;
                          let min = 0, max = 0;
                          if (chartKind === 'bar' && chartBarMode === 'stacked' && isMulti) {
                            let posMax = 0, negMin = 0;
                            for (let i = 0; i < n; i++) {
                              let posSum = 0, negSum = 0;
                              for (const s of series) {
                                if (s.values[i] >= 0) posSum += s.values[i]; else negSum += s.values[i];
                              }
                              if (posSum > posMax) posMax = posSum;
                              if (negSum < negMin) negMin = negSum;
                            }
                            max = posMax;
                            min = negMin;
                          } else {
                            for (const s of series) for (const v of s.values) {
                              if (v > max) max = v;
                              if (v < min) min = v;
                            }
                          }
                          if (min === max) { if (max === 0) { min = -1; max = 1; } else { min = 0; max = max * 1.1; } }
                          const range = max - min;
                          const pad = range * 0.08;
                          min -= pad;
                          max += pad;
                          if (min > 0) min = 0;
                          const y2px = (v: number) => plotTop + plotH * (1 - (v - min) / (max - min));
                          const yZero = y2px(0);
                          const bw = isMulti
                            ? Math.max(2, Math.min(28, (plotW / n) * 0.72 / nSeries))
                            : Math.max(2, Math.min(60, (plotW / n) * 0.68));
                          const xStep = plotW / n;
                          const xCenter = (i: number) => plotLeft + (i + 0.5) * xStep;
                          const ticks: number[] = [];
                          if (n <= 30 || n % 6 === 0) {
                            for (let i = 0; i < n; i++) ticks.push(i);
                          } else {
                            const target = 6;
                            const step = Math.max(1, Math.ceil(n / target));
                            for (let i = 0; i < n; i += step) ticks.push(i);
                          }
                          const yLevels = 5;
                          const yTicks: number[] = [];
                          for (let i = 0; i <= yLevels; i++) yTicks.push(min + ((max - min) * i) / yLevels);
                          const hasNeg = series.some((s) => s.values.some((v) => v < 0));
                          const hasPos = series.some((s) => s.values.some((v) => v > 0));
                          const showLineBaseline = hasNeg && hasPos;
                          const PALETTE = [C.accent, '#e8a24b', '#4b9ae8', '#a85be8', '#4be8a2', '#e84b4b', '#7ec7a2', '#a2c74b', '#4ba2e8', '#c74ba2'];
                          const seriesColors = series.map((_, i) => PALETTE[i % PALETTE.length]);
                          const legendItems = isMulti ? series.map((s, i) => ({ label: s.label, color: seriesColors[i] })) : [];
                          const legendHeight = legendItems.length > 0 ? 16 : 0;
                          const legendStartY = H - 6 - legendHeight;
                          return (
                            <svg viewBox={`0 0 ${W} ${H}`} style={{ width: '100%', height: H, display: 'block', background: C.bg }}>
                              <rect x={0} y={0} width={W} height={H} fill={C.bg} />
                              {yTicks.map((t, i) => {
                                const py = y2px(t);
                                return (
                                  <g key={i}>
                                    <line x1={plotLeft} y1={py} x2={plotRight} y2={py} stroke={C.border} strokeWidth={1} strokeDasharray="2 3" />
                                    <text x={plotLeft - 6} y={py + 4} textAnchor="end" fontSize={11} style={{ fill: C.muted }}>{fmtTick(t)}</text>
                                  </g>
                                );
                              })}
                              {showLineBaseline && (
                                <line x1={plotLeft} y1={yZero} x2={plotRight} y2={yZero} stroke={C.muted} strokeWidth={1} />
                              )}
                              <rect x={plotLeft} y={plotTop} width={plotW} height={plotH} fill="none" stroke={C.border} strokeWidth={1} />
                              {ticks.map((i) => (
                                <text key={i} x={xCenter(i)} y={plotBottom + 15} textAnchor="middle" fontSize={11} style={{ fill: C.muted }}>
                                  {trunc(chartData.labels[i], 12)}
                                </text>
                              ))}
                              {chartKind === 'bar' && (
                                <>
                                  {isMulti && chartBarMode === 'grouped' && series.map((s, si) =>
                                    s.values.map((v, i) => {
                                      const py = y2px(v);
                                      const by = Math.min(py, yZero);
                                      const bh = Math.abs(py - yZero);
                                      if (bh < 0.5) return null;
                                      const offset = (si - (nSeries - 1) / 2) * bw;
                                      const bx = xCenter(i) + offset - bw / 2;
                                      return (
                                        <rect key={`${si}-${i}`} x={bx} y={by} width={bw} height={bh} fill={seriesColors[si]} opacity={0.85} stroke={seriesColors[si]} />
                                      );
                                    }),
                                  )}
                                  {isMulti && chartBarMode === 'stacked' && chartData.labels.map((_, i) => {
                                    let posAcc = 0, negAcc = 0;
                                    return series.map((s, si) => {
                                      const v = s.values[i];
                                      if (Math.abs(v) < 0.5) return null;
                                      let y1, y2;
                                      if (v >= 0) { y1 = y2px(posAcc); y2 = y2px(posAcc + v); posAcc += v; }
                                      else { y1 = y2px(negAcc + v); y2 = y2px(negAcc); negAcc += v; }
                                      const yTop = Math.min(y1, y2);
                                      const h = Math.abs(y2 - y1);
                                      if (h < 0.5) return null;
                                      const bx = xCenter(i) - bw / 2;
                                      return <rect key={`${si}-${i}`} x={bx} y={yTop} width={bw} height={h} fill={seriesColors[si]} opacity={0.85} stroke={seriesColors[si]} strokeWidth={0.5} />;
                                    });
                                  })}
                                  {!isMulti && series[0].values.map((v, i) => {
                                    const py = y2px(v);
                                    const bx = xCenter(i) - bw / 2;
                                    const by = Math.min(py, yZero);
                                    const bh = Math.abs(py - yZero);
                                    if (bh < 0.5) return null;
                                    return (
                                      <g key={i}>
                                        <rect x={bx} y={by} width={bw} height={bh} fill={C.accentDim} stroke={C.accent} strokeWidth={1} />
                                        <text x={xCenter(i)} y={v >= 0 ? by - 3 : by + bh + 12} textAnchor="middle" fontSize={10} style={{ fill: C.muted }}>{fmtTick(v)}</text>
                                      </g>
                                    );
                                  })}
                                </>
                              )}
                              {chartKind === 'line' && series.map((s, si) => {
                                const pts = s.values.map((v, i) => `${xCenter(i).toFixed(2)},${y2px(v).toFixed(2)}`).join(' ');
                                return (
                                  <g key={si}>
                                    <polyline points={pts} fill="none" stroke={seriesColors[si]} strokeWidth={2} />
                                    {s.values.map((v, i) => (
                                      <circle key={i} cx={xCenter(i)} cy={y2px(v)} r={3} fill={seriesColors[si]} />
                                    ))}
                                    {!isMulti && s.values.map((v, i) => (
                                      <text key={`t-${i}`} x={xCenter(i)} y={y2px(v) + (v >= 0 ? -8 : 16)} textAnchor="middle" fontSize={10} style={{ fill: C.text }}>
                                        {fmtTick(v)}
                                      </text>
                                    ))}
                                  </g>
                                );
                              })}
                              {legendItems.length > 0 && (
                                <g transform={`translate(${plotLeft}, ${legendStartY})`}>
                                  {legendItems.slice(0, 10).map((it, i) => (
                                    <g key={i} transform={`translate(${i * 110}, 0)`}>
                                      <rect width={10} height={10} fill={it.color} opacity={0.85} />
                                      <text x={16} y={9} fontSize={11} style={{ fill: C.text }}>{trunc(it.label, 14)}</text>
                                    </g>
                                  ))}
                                  {legendItems.length > 10 && (
                                    <text x={1000} y={9} fontSize={11} style={{ fill: C.muted }}>… +{legendItems.length - 10} 系列</text>
                                  )}
                                </g>
                              )}
                            </svg>
                          );
                        })()
                      ) : (
                        <div style={{ padding: 20, textAlign: 'center', color: 'var(--muted)', fontSize: 12 }}>
                          无数据。请执行一条 SELECT 查询，或调整 X/Y 列。
                        </div>
                      )}
                    </div>
                  )}
                  <div
                    style={{
                      display: 'grid',
                      gridTemplateColumns: `40px repeat(${visibleColumns.length}, minmax(60px, 1fr))`,
                      gap: 2,
                      padding: '2px 10px 4px',
                      borderBottom: '1px solid var(--border)',
                      background: 'var(--bg-alt, transparent)',
                    }}
                  >
                    <div style={{ textAlign: 'center', color: 'var(--muted)', fontSize: 10, padding: '2px 0', lineHeight: 1 }}>∅</div>
                    {visibleColumns.map((c) => {
                      const j = columns.indexOf(c);
                      const v = colFilters[j] ?? '';
                      return (
                        <input
                          key={j}
                          type="text"
                          value={v}
                          placeholder="过滤…"
                          title={`${c.name}: 子串匹配 / re:正则 / is null / not null`}
                          onChange={(e) => {
                            const nv = e.target.value;
                            setColFilters((prev) => {
                              const next = { ...prev };
                              if (nv) next[j] = nv;
                              else delete next[j];
                              return next;
                            });
                          }}
                          onKeyDown={(e) => {
                            if (e.key === 'Escape' && v) {
                              setColFilters((prev) => {
                                const next = { ...prev };
                                delete next[j];
                                return next;
                              });
                            }
                          }}
                          style={{
                            width: '100%',
                            boxSizing: 'border-box',
                            padding: '1px 4px',
                            fontSize: 11,
                            fontFamily: 'inherit',
                            background: 'var(--bg)',
                            border: '1px solid var(--border)',
                            borderRadius: 2,
                            outline: 'none',
                            color: 'var(--fg)',
                          }}
                        />
                      );
                    })}
                  </div>
                  {pendingTx && (
                    <div style={{ padding: '8px 10px', borderTop: '1px solid var(--accent)', borderBottom: '1px solid var(--accent)', background: 'var(--accent-dim, rgba(255, 200, 0, 0.1))', display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
                      <strong style={{ fontSize: 12, color: 'var(--muted)' }}>待提交事务</strong>
                      <span style={{ color: 'var(--muted)', fontSize: 12 }}>
                        {pendingTx.count} 行 · txn {pendingTx.txId.slice(0, 8)}
                      </span>
                      {pendingTx.expr && <span style={{ fontSize: 11, color: 'var(--muted)' }}>（含 SQL 表达式）</span>}
                      {(() => {
                        const sec = Math.max(0, Math.floor((txNow - pendingTx.openedAt) / 1000));
                        const label = sec < 60 ? `${sec}s` : sec < 3600 ? `${Math.floor(sec / 60)}m${sec % 60}s` : `${Math.floor(sec / 3600)}h${Math.floor((sec % 3600) / 60)}m`;
                        return <span style={{ fontSize: 11, color: 'var(--muted)', fontFamily: 'monospace' }} title="事务挂起时长">· 已等待 {label}</span>;
                      })()}
                      <span style={{ flex: 1 }} />
                      {pendingTxBusy && <span style={batchBusySpinner}>处理中…</span>}
                      <span style={{ fontSize: 11, color: 'var(--muted)' }} title="Ctrl+Enter 提交 · Esc 回滚">Ctrl+Enter 提交 · Esc 回滚</span>
                      <button
                        className="btn-ico"
                        style={{ background: 'var(--danger, #c0392b)', color: 'white', borderColor: 'var(--danger, #c0392b)' }}
                        onClick={() => void resolvePendingTx(false)}
                        disabled={pendingTxBusy}
                        title="回滚事务并恢复修改前的值（Esc）"
                      >
                        撤销（回滚）
                      </button>
                      <button
                        className="primary btn-ico"
                        onClick={() => void resolvePendingTx(true)}
                        disabled={pendingTxBusy}
                        title="确认提交事务，永久保存修改（Ctrl+Enter）"
                      >
                        确认提交
                      </button>
                    </div>
                  )}
                  {batchOpen && batchEdit && selRows.size >= 2 && (
                    <div style={{ padding: '8px 10px', borderTop: '1px solid var(--border)', borderBottom: '1px solid var(--border)', background: 'var(--bg-elev, rgba(0,0,0,0.03))' }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6, flexWrap: 'wrap' }}>
                        <strong style={{ fontSize: 12, color: 'var(--muted)' }}>批量编辑</strong>
                        <span style={{ color: 'var(--muted)', fontSize: 12 }}>
                          {batchTargetRows.length} 行 · 列
                        </span>
                        <select
                          value={batchEdit.col}
                          onChange={(e) => {
                            const c = Number(e.target.value);
                            const cur = (result?.rows ?? [])[batchTargetRows[0]]?.[c];
                            const isNull = cur === null || cur === undefined;
                            setBatchEdit({ col: c, value: isNull ? '' : String(cur), isNull, isExpr: false });
                          }}
                          style={{ background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 2, padding: '2px 4px', fontSize: 12 }}
                        >
                          {columns.map((c, j) => (
                            <option key={c.name} value={j} disabled={pkCols.includes(c.name)}>
                              {c.name} ({c.type}){pkCols.includes(c.name) ? ' · PK 只读' : ''}
                            </option>
                          ))}
                        </select>
                        <input
                          type="text"
                          value={batchEdit.value}
                          placeholder={batchEdit.isExpr ? 'SQL 表达式，如 now() / age + 1' : '新值'}
                          disabled={batchEdit.isNull}
                          onChange={(e) => setBatchEdit({ ...batchEdit, value: e.target.value })}
                          style={{ width: 180, padding: '2px 4px', fontSize: 12, background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 2 }}
                        />
                        <label style={{ display: 'flex', alignItems: 'center', gap: 3, fontSize: 11, color: batchEdit.isNull ? 'var(--accent)' : 'var(--muted)', cursor: 'pointer' }}>
                          <input type="checkbox" checked={batchEdit.isNull} onChange={(e) => setBatchEdit({ ...batchEdit, isNull: e.target.checked })} style={{ margin: 0, accentColor: 'var(--accent)' }} />
                          全部设为 NULL
                        </label>
                        <label style={{ display: 'flex', alignItems: 'center', gap: 3, fontSize: 11, color: batchEdit.isExpr ? 'var(--accent)' : 'var(--muted)', cursor: 'pointer' }}>
                          <input type="checkbox" checked={batchEdit.isExpr} onChange={(e) => setBatchEdit({ ...batchEdit, isExpr: e.target.checked })} disabled={batchEdit.isNull} style={{ margin: 0, accentColor: 'var(--accent)' }} />
                          作为 SQL 表达式
                        </label>
                        <span style={batchFlexSpacer} />
                        {batchBusy && <span style={batchBusySpinner}>执行中…</span>}
                        <button className="primary btn-ico" onClick={() => void commitBatchEdit()} disabled={batchBusy || batchTargetRows.length === 0} title="在事务内执行 N 条 UPDATE，之后可提交或回滚">
                          在事务内提交（{batchTargetRows.length} 行）
                        </button>
                        <button className="btn-ico" onClick={() => setBatchOpen(false)} title="取消批量编辑">取消</button>
                      </div>
                      <details style={{ margin: 0 }}>
                        <summary style={batchSummaryStyle}>预览 SQL（{buildBatchUpdates().length} 条）</summary>
                        <pre style={batchSqlPreviewStyle}>{buildBatchUpdates().slice(0, 10).join('\n')}{buildBatchUpdates().length > 10 ? `\n… 另 ${buildBatchUpdates().length - 10} 条` : ''}</pre>
                      </details>
                    </div>
                  )}
                  <table className={`grid${settings.gridWrap ? ' wrap' : ''}`}>
                  <thead>
                    <tr>
                      <th style={{ width: 40 }}>#</th>
                      {visibleColumns.map((c, _vj) => {
                        const j = columns.indexOf(c);
                        const isPk = pkCols.includes(c.name);
                        return (
                          <th
                            key={c.name}
                            onClick={() => onSort(j)}
                            title={`${c.type}${c.table ? ` · ${c.table}` : ''}${isPk ? ' · 主键（只读）' : ''}\n点击按 ${c.name} ${sortCol === j ? (sortDir === 'asc' ? '↓ 升序' : '→ 降序') : '↑ 升序'}排序`}
                            style={{ cursor: 'pointer', userSelect: 'none', position: 'relative' }}
                          >
                            {c.name}
                            {isPk && <span className="pk-badge">PK</span>}
                            <span className="type-badge">{c.type}</span>
                            {sortIcon(j)}
                          </th>
                        );
                      })}
                    </tr>
                  </thead>
                  <tbody>
                    {visibleRows.map((entry, i) => {
                      const realIdx = entry.origIdx;
                      const selected = selRows.has(realIdx);
                      const fullRow = (result.rows ?? [])[realIdx];
                      const editable = editEnabled && identifyableRow(fullRow, pkCols, result.columns);
                      return (
                        <tr
                          key={i}
                          onClick={(e) => {
                            if ((e.target as HTMLElement).closest('input')) return;
                            toggleRow(realIdx, e.ctrlKey || e.metaKey || e.shiftKey);
                          }}
                          style={{ cursor: 'pointer', ...(selected ? { background: 'var(--accent-dim)', outline: '1px solid var(--accent)' } : {}) }}
                        >
                          <td className="rownum">{realIdx + 1}</td>
                          {entry.cells.map((cell, vj) => {
                            const j = visibleColumns[vj];
                            const originalColIdx = columns.indexOf(j);
                            const cls = cellClass(cell, j);
                            const text = cls === 'null' ? 'NULL' : fmt(cell);
                            const isEditing = editCell?.row === realIdx && editCell?.col === originalColIdx;
                            const isPkCol = pkCols.includes(j.name);
                            const isEditable = editable && !isPkCol;
                            const draft = editDrafts[draftKey(realIdx, originalColIdx)];
                            const flashKind = flashCells[draftKey(realIdx, originalColIdx)];
                            const tdClassList: string[] = [cls];
                            if (isEditable && !isEditing) tdClassList.push('editable');
                            if (draft && !isEditing) tdClassList.push('dirty');
                            if (flashKind === 'ok') tdClassList.push('flash-ok');
                            else if (flashKind === 'err') tdClassList.push('flash-err');
                            if (isEditing && editCell) {
                              const validationErr = editCell.isNull ? null : validateCellValue(editCell.value, j);
                              return (
                                <td
                                  key={originalColIdx}
                                  className={tdClassList.join(' ')}
                                  onClick={(e) => e.stopPropagation()}
                                  style={{ position: 'relative' }}
                                >
                                  <div style={{ display: 'flex', alignItems: 'center', gap: 3 }}>
                                    <input
                                      autoFocus
                                      type="text"
                                      value={editCell.value}
                                      disabled={editCell.isNull}
                                      placeholder="输入值（Enter 提交 · Esc 取消 · Ctrl+Enter 强制）"
                                      className={'cell-editor-input' + (validationErr ? ' invalid' : '')}
                                      onChange={(e) => setEditCell({ ...editCell, value: e.target.value })}
                                      onBlur={() => void commitEdit()}
                                      onKeyDown={handleCellKeyDown}
                                      style={{ flex: 1, minWidth: 60, boxSizing: 'border-box', padding: '1px 3px', fontSize: 13, fontFamily: 'inherit', background: 'var(--bg-elev)', border: '1px solid var(--accent)', borderRadius: 2, outline: 'none', ...(editCell.isNull ? { opacity: 0.5 } : {}) }}
                                      title={validationErr ?? 'Enter 提交 · Esc 取消 · Ctrl+Enter 强制提交'}
                                    />
                                    <label
                                      title="写入 NULL 而非空字符串"
                                      style={{ display: 'flex', alignItems: 'center', gap: 2, fontSize: 10, color: editCell.isNull ? 'var(--accent)' : 'var(--muted)', cursor: 'pointer', userSelect: 'none' }}
                                    >
                                      <input
                                        type="checkbox"
                                        checked={editCell.isNull}
                                        onChange={(e) => setEditCell({ ...editCell, isNull: e.target.checked })}
                                        style={{ margin: 0, accentColor: 'var(--accent)' }}
                                      />
                                      <span>NULL</span>
                                    </label>
                                  </div>
                                </td>
                              );
                            }
                            return (
                              <td
                                key={originalColIdx}
                                className={tdClassList.join(' ')}
                                title={text === 'NULL' ? undefined : String(text) + (isEditable ? '\n双击编辑 · 右键菜单' : isPkCol ? '\n主键列（只读）· 右键菜单' : '\n右键菜单')}
                                onContextMenu={(e) => {
                                  e.preventDefault();
                                  e.stopPropagation();
                                  setCtxMenu({ x: e.clientX, y: e.clientY, row: realIdx, col: originalColIdx });
                                }}
                                onDoubleClick={(e) => {
                                  if (!isEditable) return;
                                  e.stopPropagation();
                                  startEdit(realIdx, originalColIdx);
                                }}
                                style={{ position: 'relative' }}
                              >
                                {cls === 'null' ? <span className="null">NULL</span> : text}
                              </td>
                            );
                          })}
                        </tr>
                      );
                    })}
                  </tbody>
                  </table>
                </>
              )}
            </>
          )}
          {(explainError || explainBusy || explainResult) && (
            <div style={{ padding: '6px 10px', borderTop: '1px solid var(--border)', background: 'var(--bg)', maxWidth: '100%' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
                <strong style={{ fontSize: 12, color: 'var(--muted)' }}>执行计划</strong>
                <button className="btn-ico" onClick={() => { setExplainResult(null); setExplainError(null); }} title="关闭" style={{ fontSize: 11 }}>×</button>
              </div>
              {explainError && <div style={{ color: 'var(--danger)', fontFamily: 'monospace', fontSize: 12, whiteSpace: 'pre-wrap' }}>{explainError}</div>}
              {explainBusy && !explainResult && <div style={{ color: 'var(--muted)', fontSize: 12 }}>加载中…</div>}
              {explainResult && <ExplainView result={explainResult} kind={connKindRef.current} />}
            </div>
          )}
        </div>
        {cellPreview && previewTarget && (
          <div style={{ padding: '6px 10px', borderTop: '1px solid var(--border)', background: 'var(--bg)', maxWidth: '100%' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
              <strong style={{ fontSize: 12, color: 'var(--muted)' }}>
                单元格预览 · 行 {cellPreview.row + 1} · 列 <code>{previewTarget.col.name}</code> (<code>{previewTarget.col.type}</code>)
              </strong>
              <div style={{ display: 'flex', gap: 2 }}>
                {(['text', 'json', 'hex', 'time'] as const).map((t) => (
                  <button
                    key={t}
                    className="btn-ico"
                    onClick={() => setPreviewTab(t)}
                    style={{
                      padding: '2px 8px',
                      fontSize: 11,
                      background: previewTab === t ? 'var(--accent-dim)' : 'transparent',
                      color: previewTab === t ? 'var(--fg)' : 'var(--muted)',
                      border: '1px solid var(--border)',
                      borderRadius: 2,
                    }}
                  >
                    {t === 'text' ? '文本' : t === 'json' ? 'JSON' : t === 'hex' ? 'HEX' : '时间'}
                  </button>
                ))}
              </div>
              <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--muted)' }}>
                {previewTarget.value === null ? 'NULL' : `${String(previewTarget.value).length} 字符`}
              </span>
              <button className="btn-ico" onClick={() => setCellPreview(null)} title="关闭预览" style={{ fontSize: 11 }}>×</button>
            </div>
            <div style={{ fontSize: 11, color: 'var(--muted)', marginBottom: 4 }}>
              {previewTab === 'json' && !tryParseJson(previewTarget.value) && '(无法解析为 JSON)'}
              {previewTab === 'time' && !(typeof previewTarget.value === 'string' || typeof previewTarget.value === 'number') && '(仅支持字符串/数字时间)'}
            </div>
            <pre
              style={{
                margin: 0,
                padding: 8,
                background: 'var(--bg-alt, rgba(0,0,0,0.15))',
                border: '1px solid var(--border)',
                borderRadius: 3,
                fontFamily: 'monospace',
                fontSize: 12,
                maxHeight: 240,
                overflow: 'auto',
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-all',
                color: 'var(--fg)',
              }}
            >
              {previewTab === 'text' ? (previewTarget.value === null ? 'NULL' : String(previewTarget.value))
                : previewTab === 'json' ? (tryParseJson(previewTarget.value) ?? String(previewTarget.value))
                : previewTab === 'hex' ? (previewTarget.value === null ? 'NULL' : bytesToHex(String(previewTarget.value)))
                : (() => {
                    if (previewTarget.value === null) return 'NULL';
                    const s = String(previewTarget.value);
                    const out: string[] = [];
                    try { const d = new Date(s); out.push(`Parsed (local): ${d.toString()}`); out.push(`ISO 8601: ${d.toISOString()}`); } catch { /* not date */ }
                    try { const d = new Date(Number(s)); if (!Number.isNaN(d.getTime()) && s.match(/^\d+$/)) { out.push(`Unix ${s.length <= 10 ? 's' : 'ms'} (local): ${d.toString()}`); out.push(`Unix ${s.length <= 10 ? 's' : 'ms'} (ISO): ${d.toISOString()}`); } } catch { /* not number */ }
                    if (out.length === 0) out.push('非可解析的时间值');
                    return out.join('\n');
                  })()}
            </pre>
          </div>
        )}
        {ctxMenu && (
          <ContextMenu
            x={ctxMenu.x}
            y={ctxMenu.y}
            onClose={() => setCtxMenu(null)}
            items={ctxItems}
            width={260}
          />
        )}
        <div className="statusbar">
          <div className="seg">
            <span className="k">transport</span> {transport === 'http' ? 'HTTP msgpack' : 'WebSocket'}
          </div>
          <div className="seg">
            <span className="k">rows</span> {rowCount}
            {result?.has_more && result.total_rows != null ? ` / ${result.total_rows}` : ''}
          </div>
          {result && columns.length > 0 && (
            <div className="seg" title={`显示 ${visibleColumns.length}/${columns.length} 列`}>
              <span className="k">cols</span> {visibleColumns.length}/{columns.length}
              {hiddenCols.size > 0 && (
                <button
                  onClick={showAllColumns}
                  style={{ marginLeft: 6, background: 'transparent', border: 'none', color: 'var(--accent)', cursor: 'pointer', padding: 0, fontSize: 12 }}
                  title="显示全部列"
                >
                  显示全部
                </button>
              )}
            </div>
          )}
          {result && Object.values(colFilters).some((v) => v.trim()) && (
            <div className="seg" title="清除所有列过滤（对任一输入按 Esc）">
              <span className="k">filter</span> {displayRows.length}/{rowCount}
              <button
                onClick={() => setColFilters({})}
                style={{ marginLeft: 6, background: 'transparent', border: 'none', color: 'var(--accent)', cursor: 'pointer', padding: 0, fontSize: 12 }}
                title="清除全部过滤"
              >
                清除
              </button>
            </div>
          )}
          {histIdx >= 0 ? (
            <div className="seg">
              <span className="k">hist</span> {histIdx + 1}/{history.length}
            </div>
          ) : history.length > 0 ? (
            <div className="seg muted" title="按 ↑ 或 Ctrl+P 循环查询历史">
              <span className="k">hist</span> {history.length}
            </div>
          ) : null}
          {result && (
            <div className="seg">
              <span className="k">server</span> {result.execution_time_ms.toFixed(2)} ms
              {elapsed != null && <span className="muted">(+{(elapsed - result.execution_time_ms).toFixed(0)} net)</span>}
            </div>
          )}
          {busy && (
            <div className="seg">
              <span className="k">running</span> {queryId ? queryId.slice(0, 8) + '…' : '…'}
            </div>
          )}
          <div className="seg right">
            {error
              ? <span style={{ color: 'var(--danger)' }}>{error.split('\n')[0].slice(0, 60)}</span>
              : result
                ? <span style={{ color: 'var(--ok)' }}>done</span>
                : <span>idle</span>}
          </div>
        </div>
      </div>
    </CollapsiblePane>
    {manageCustomOpen && (
      <CustomTemplatesModal
        items={customTemplates}
        onChange={setCustomTemplates}
        onClose={() => setManageCustomOpen(false)}
      />
    )}
    {importOpen && (
      <ImportModal
        connId={connId}
        kind={connKindRef.current}
        contextSchema={contextSchema}
        contextTable={contextTable}
        onClose={() => setImportOpen(false)}
      />
    )}
    </>
  );
}
