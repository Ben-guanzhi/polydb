import { Fragment, useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import {
  AuditBadge,
  colDtypeColor,
  formatTime,
  formatValue,
  historyStatusBadge,
  qualityScoreColor,
  Step2Table,
  StepBar,
  StepDone,
  styles,
  toMsg,
  typeBadgeColor,
  type Step,
} from './ImportModalParts';
import { STEP_ORDER } from './ImportModalParts';
import {
  boolLabel,
  classifyFailReason,
  failCategoryMeta,
  fmtCacheAgeForAudit,
  previewParams,
  sqlLiteral,
} from '../lib/importDisplay';
import * as api from '../lib/api';
import { detectDelimiter, parseCsv, type Delimiter, type CsvParseResult } from '../lib/csvParser';
import {
  buildStatements,
  buildInsertStatement,
  chunk,
  compatible,
  DEFAULT_IMPORT_OPTIONS,
  decodeWithEncoding,
  detectEncoding,
  EMPTY_VALIDATION,
  generateBlankTemplate,
  hasAnyValidation,
  inferMapping,
  parseJsonl,
  profileColumns,
  qualTable,
  quoteIdent,
  suggestMappings,
  transformOnly,
  validateAllRows,
  validateCell,
  type ColumnProfile,
  type ColumnTransform,
  type ColumnValidation,
  type ImportFormat,
  type ImportOptions,
  type InputEncoding,
  type Mapping,
  type MappingSuggestion,
  type NullPolicy,
} from '../lib/importData';
import { splitSql, countParams, type SqlStatement } from '../lib/sqlSplit';
import { lintSql, severityLabel } from '../lib/sqlLint';
import ContextMenu, { type ContextMenuEntry } from './ContextMenu';
import { scoreDataQuality, suggestTransforms, type DataQualityReport, type QualityTransformSuggestion } from '../lib/dataQuality';
import {
  addDeletedPresetTombstone,
  applyPreset,
  applyPresetsBackup,
  buildPreset,
  clearAuditCache,
  deletePreset,
  diffPreset,
  exportPresetsBackup,
  getPreset,
  listDeletedPresets,
  listPresets,
  listSnapshots,
  loadAuditCache,
  purgeAllDeletedPresets,
  purgeDeletedPresetTombstone,
  previewBackup,
  presetKey,
  restoreDeletedPreset,
  restoreDeletedPresetTombstone,
  restoreSnapshot,
  saveAuditCache,
  savePreset,
  type BackupPreviewItem,
  type ImportPreset,
  type PresetAuditStatus,
  type PresetBackup,
  snapshotPresetsState,
  undoApplyPresetsBackup,
  updateAuditCacheEntry,
  type BackupApplySnapshot,
  diffUndoImpact,
  type UndoImpactDetail,
} from '../lib/importPresets';
import { addHistory, clearHistory, deleteHistory, listHistory, type ImportHistoryEntry } from '../lib/importHistory';
import { publishEditorStatus, clearEditorStatus } from '../lib/statusBus';
import { registerCommand } from '../lib/commandRegistry';
import type { ColumnInfo, DatabaseKind, SchemaInfo, TableInfo, Value } from '../api';

interface FailedRow {
  csvRow: number;
  reason: string;
  preview: string;
  stmtIdx: number;
}

interface Props {
  connId: string;
  kind: DatabaseKind | null;
  contextSchema?: string | null;
  contextTable?: string | null;
  onClose: () => void;
}

const SAMPLE_CSV: Record<string, { title: string; csv: string; json: string }> = {
  sqlite: {
    title: 'SQLite 示例',
    csv: `id,name,email,score,active
1,Alice,alice@example.com,95.5,true
2,Bob,bob@example.com,82.0,false
3,Charlie,charlie@example.com,77.75,true`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":true}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":false}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":true}`,
  },
  postgres: {
    title: 'PostgreSQL 示例',
    csv: `id,name,email,score,active,born_at
1,Alice,alice@example.com,95.5,true,2024-03-15 10:30:00
2,Bob,bob@example.com,82.0,false,2024-05-20
3,Charlie,charlie@example.com,77.75,true,2025-01-05 09:00:00`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":true,"born_at":"2024-03-15 10:30:00"}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":false,"born_at":"2024-05-20"}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":true,"born_at":"2025-01-05 09:00:00"}`,
  },
  mysql: {
    title: 'MySQL 示例',
    csv: `id,name,email,score,active,created_at
1,Alice,alice@example.com,95.5,1,'2024-03-15 10:30:00'
2,Bob,bob@example.com,82.0,0,'2024-05-20 08:00:00'
3,Charlie,charlie@example.com,77.75,1,'2025-01-05 09:00:00'`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":1,"created_at":"2024-03-15 10:30:00"}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":0,"created_at":"2024-05-20 08:00:00"}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":1,"created_at":"2025-01-05 09:00:00"}`,
  },
  mssql: {
    title: 'MSSQL 示例',
    csv: `id,name,email,score,active,created_date
1,Alice,alice@example.com,95.5,1,2024-03-15
2,Bob,bob@example.com,82.0,0,2024-05-20
3,Charlie,charlie@example.com,77.75,1,2025-01-05`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":1,"created_date":"2024-03-15"}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":0,"created_date":"2024-05-20"}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":1,"created_date":"2025-01-05"}`,
  },
  oracle: {
    title: 'Oracle 示例',
    csv: `id,name,email,score,active,created_date
1,Alice,alice@example.com,95.5,1,2024-03-15
2,Bob,bob@example.com,82.0,0,2024-05-20
3,Charlie,charlie@example.com,77.75,1,2025-01-05`,
    json: `{"id":1,"name":"Alice","email":"alice@example.com","score":95.5,"active":1,"created_date":"2024-03-15"}
{"id":2,"name":"Bob","email":"bob@example.com","score":82.0,"active":0,"created_date":"2024-05-20"}
{"id":3,"name":"Charlie","email":"charlie@example.com","score":77.75,"active":1,"created_date":"2025-01-05"}`,
  },
};

function sampleForKind(kind: DatabaseKind | null, format: ImportFormat): { title: string; text: string } {
  const s = (kind && SAMPLE_CSV[kind]) ?? SAMPLE_CSV.sqlite;
  return { title: s.title, text: format === 'jsonl' ? s.json : s.csv };
}

// 每组分色：按语义分配到色卡（未识别分组 fallback 到 muted）
const DIFF_GROUP_COLOR: Record<string, string> = {
  '转换':     '#f59e0b',
  '空值策略': '#8b5cf6',
  '校验':     '#ef4444',
  '模式':     '#3b82f6',
  '严格校验': '#f97316',
  '空串视为 null': '#06b6d4',
  '批大小':   '#10b981',
  '行过滤':   '#ec4899',
};

// diff 项语义分类：识别 buildUndoDiff 输出里的"新增/删除/变化"三态
// added = before 是空标记（∅ / 空 / null（默认））→ 撤销后新增
// removed = after 是空标记 → 撤销后删除
// changed = 其他（值替换）
type DiffItemKind = 'added' | 'removed' | 'changed';
const DIFF_EMPTY_MARKS = new Set(['∅', '空', 'null（默认）']);
const FIX_STACK_MAX_BYTES = 512 * 1024;
const classifyDiffItem = (it: { before: string; after: string }): DiffItemKind => {
  const isBeforeEmpty = it.before === '' || DIFF_EMPTY_MARKS.has(it.before);
  const isAfterEmpty = it.after === '' || DIFF_EMPTY_MARKS.has(it.after);
  if (isBeforeEmpty && !isAfterEmpty) return 'added';
  if (!isBeforeEmpty && isAfterEmpty) return 'removed';
  return 'changed';
};
const diffItemColor = (kind: DiffItemKind) =>
  kind === 'added' ? '#10b981' : kind === 'removed' ? '#dc2626' : '#f59e0b';
const diffItemIcon = (kind: DiffItemKind) => (kind === 'added' ? '＋' : kind === 'removed' ? '－' : '↔');
const diffItemLabel = (kind: DiffItemKind) =>
  kind === 'added' ? '新增' : kind === 'removed' ? '删除' : '变化';

// diff 面板搜索匹配的富文本分段：hit=true 的段是搜索关键字命中，需高亮渲染
// kwIdx（M30.35）：多关键字模式下该段命中的关键字索引，供分色高亮
type DiffSearchPart = { text: string; hit: boolean; kwIdx?: number };
// diff 面板搜索字段作用域：all=全部字段，field/before/after 只匹配对应字段（M30.33）
type DiffSearchScope = 'all' | 'field' | 'before' | 'after';

// 关键字分色（M30.35）：HSL 色相按 kwIdx 均匀分布，避开 danger/accent 常见色（红/蓝）
// 返回 {bg, fg} 供 <mark> 使用
const diffKeywordColor = (kwIdx: number): { bg: string; fg: string } => {
  const hues = [28, 160, 300, 55, 200, 340, 100, 250];
  const h = hues[kwIdx % hues.length];
  return {
    bg: `hsla(${h}, 75%, 55%, 0.32)`,
    fg: `hsl(${h}, 90%, 78%)`,
  };
};

// 搜索字符串切分为多个关键字（M30.32 多关键字 OR 匹配）
// 空白和 `|` 都是分隔符；空 token 过滤；不做 lowercase 由调用方处理
const splitDiffSearchKeywords = (qRaw: string): string[] => {
  if (!qRaw) return [];
  return qRaw.split(/\s+|\|/).map((s) => s).filter((s) => s.length > 0);
};

const splitDiffSearchParts = (
  text: string,
  query: string,
  caseSensitive: boolean,
): DiffSearchPart[] => {
  const keywords = splitDiffSearchKeywords(query);
  if (keywords.length === 0) return [{ text, hit: false }];
  const s = caseSensitive ? text : text.toLowerCase();
  const kws = keywords.map((k) => caseSensitive ? k : k.toLowerCase());
  // 收集所有命中区间 [start, end)，同时记录来源关键字索引（M30.35 分色）
  const intervals: Array<[number, number, number]> = [];
  for (let ki = 0; ki < kws.length; ki++) {
    const kw = kws[ki];
    if (!kw) continue;
    let i = 0;
    while (i <= s.length - kw.length) {
      const idx = s.indexOf(kw, i);
      if (idx < 0) break;
      intervals.push([idx, idx + kw.length, ki]);
      i = idx + 1; // 允许重叠匹配下一个关键字
    }
  }
  if (intervals.length === 0) return [{ text, hit: false }];
  // 按起点排序，起点相同保留最长（其 kwIdx 胜出）
  intervals.sort((a, b) => a[0] - b[0] || (b[1] - b[0]) - (a[1] - a[0]));
  const merged: Array<[number, number, number]> = [];
  for (const iv of intervals) {
    const last = merged[merged.length - 1];
    if (last && iv[0] < last[1]) {
      if (iv[1] > last[1]) last[1] = iv[1];
    } else {
      merged.push([iv[0], iv[1], iv[2]]);
    }
  }
  const parts: DiffSearchPart[] = [];
  let last = 0;
  for (const [a, b, ki] of merged) {
    if (a > last) parts.push({ text: text.slice(last, a), hit: false });
    parts.push({ text: text.slice(a, b), hit: true, kwIdx: ki });
    last = b;
  }
  if (last < text.length) parts.push({ text: text.slice(last), hit: false });
  return parts;
};

export default function ImportModal({ connId, kind: kindProp, contextSchema, contextTable, onClose }: Props) {
  const [kindState, setKindState] = useState<DatabaseKind | null>(kindProp);
  useEffect(() => {
    let cancelled = false;
    void api.getConnection(connId).then((c) => { if (!cancelled) setKindState(c.kind); }).catch(() => {});
    return () => { cancelled = true; };
  }, [connId]);
  const kind = kindState ?? kindProp;
  const [step, setStep] = useState<Step>('input');
  const [inputFormat, setInputFormat] = useState<ImportFormat>('csv');
  const [csvText, setCsvText] = useState('');
  const [fileName, setFileName] = useState<string | null>(null);
  const [delimiter, setDelimiter] = useState<Delimiter | null>(null);
  const [hasHeader, setHasHeader] = useState(true);
  const [parse, setParse] = useState<CsvParseResult | null>(null);

  const [schemas, setSchemas] = useState<SchemaInfo[]>([]);
  const [schemaLoading, setSchemaLoading] = useState(false);
  const [selSchema, setSelSchema] = useState<string>(contextSchema ?? '');
  const [tables, setTables] = useState<TableInfo[]>([]);
  const [tablesLoading, setTablesLoading] = useState(false);
  const [selTable, setSelTable] = useState<string>(contextTable ?? '');
  const [cols, setCols] = useState<ColumnInfo[]>([]);
  const [colsLoading, setColsLoading] = useState(false);
  const [mappings, setMappings] = useState<Mapping[]>([]);

  const [opts, setOpts] = useState<ImportOptions>({ ...DEFAULT_IMPORT_OPTIONS, kind: kindProp });
  const [busy, setBusy] = useState(false);
  // M30.100 Ctrl+/ 打开导入向导快捷键速查浮层；Esc 关闭
  const [helpPanelOpen, setHelpPanelOpen] = useState(false);
  // M30.113 预设管理浮层（导出全量备份 / 导入 JSON 恢复）
  const [presetManagerOpen, setPresetManagerOpen] = useState(false);
  const [presetManagerOverwrite, setPresetManagerOverwrite] = useState(true);
  const [presetManagerBusy, setPresetManagerBusy] = useState(false);
  const [presetManagerMsg, setPresetManagerMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const presetManagerFileRef = useRef<HTMLInputElement | null>(null);
  // M30.116 当前连接预设列表 + 一键应用/删除（跨会话可发现性）
  const [presetListRefresh, setPresetListRefresh] = useState(0);
  // M30.131 预设列表审计过滤（仅在 presetAuditResults 有结果时生效）
  const [presetAuditFilter, setPresetAuditFilter] = useState<'all' | 'warn' | 'ok'>('all');
  // M30.117 健康审计：并发 listColumns 检查每个预设目标表/列是否存活
  const [presetAuditResults, setPresetAuditResults] = useState<Record<string, { status: PresetAuditStatus; reason: string }> | null>(null);
  const [presetAuditBusy, setPresetAuditBusy] = useState(false);
  // M30.119 审计缓存：打开浮层时如果 <24h 直接复用
  const presetAuditCheckedRef = useRef<string>('');
  const [presetAuditCachedAt, setPresetAuditCachedAt] = useState<number | null>(null);
  // M30.118 备份预检：先展示 diff，用户勾选后再 apply
  const [backupPending, setBackupPending] = useState<{ backup: PresetBackup; items: BackupPreviewItem[] } | null>(null);
  const [backupSelected, setBackupSelected] = useState<Set<string>>(new Set());
  // M30.156 B 备份预检 filter/sort/search 状态跨会话持久化（polydb.backupPrefilter.v1）
  // 语义：Alt+R 只清运行态、不动持久化；重新打开 presetManager 时自动加载上次状态
  type BackupPrefilterPersisted = {
    filter?: 'all' | 'overwrite' | 'add';
    kindFilter?: 'all' | 'preset' | 'snapshot';
    sortBy?: 'diff' | 'schema' | 'risk';
    groupSort?: 'diff' | 'name' | 'risk';
    search?: string;
    // M30.159 C 风险级别过滤也跨会话持久化
    riskLevelFilter?: 'all' | 'low' | 'mid' | 'high';
    // M30.162 B 风险阈值滑块也跨会话持久化（0 时省略以保持稀疏）
    riskThreshold?: number;
  };
  const loadBackupPrefilter = (): BackupPrefilterPersisted => {
    try {
      const raw = localStorage.getItem('polydb.backupPrefilter.v1');
      if (!raw) return {};
      const p = JSON.parse(raw);
      return (p && typeof p === 'object') ? (p as BackupPrefilterPersisted) : {};
    } catch { return {}; }
  };
  const initialBackupPrefilter = useRef<BackupPrefilterPersisted>(loadBackupPrefilter());
  // M30.134 备份预检过滤 chip：全部 / ⚠️ 将覆盖 / ✅ 新增（M30.156 B 从持久化回填）
  const [backupPreviewFilter, setBackupPreviewFilter] = useState<'all' | 'overwrite' | 'add'>(
    initialBackupPrefilter.current.filter ?? 'all',
  );
  // M30.139 备份预检二级过滤：全部 / 预设 / 快照（与 action 过滤正交组合，M30.156 B 从持久化回填）
  const [backupPreviewKindFilter, setBackupPreviewKindFilter] = useState<'all' | 'preset' | 'snapshot'>(
    initialBackupPrefilter.current.kindFilter ?? 'all',
  );
  // M30.141 备份预检搜索框：按 schema.table 子串模糊匹配（M30.156 B 从持久化回填）
  const [backupPreviewSearch, setBackupPreviewSearch] = useState(initialBackupPrefilter.current.search ?? '');
  // M30.142 备份预检排序：'diff' 按字段级变更数降序（大改动前置），'schema' 按 schema.table 字典序（M30.156 B 从持久化回填）
  const [backupSortBy, setBackupSortBy] = useState<'diff' | 'schema' | 'risk'>(
    initialBackupPrefilter.current.sortBy ?? 'diff',
  );
  // M30.158 A 备份预检风险级别过滤：'all'/'low'/'mid'/'high'，替代 M30.157 B 的 backupHighRiskOnly bool
  // M30.159 C 从持久化回填（M30.156 B 通道扩展到 riskLevelFilter 字段）
  const [backupRiskLevelFilter, setBackupRiskLevelFilter] = useState<'all' | 'low' | 'mid' | 'high'>(
    initialBackupPrefilter.current.riskLevelFilter ?? 'all',
  );
  // M30.143 B 备份预检「预览所有 diff」全局切换：不依赖逐项勾选，Alt+V 切换
  const [backupPreviewAllDiff, setBackupPreviewAllDiff] = useState(false);
  // M30.161 C 备份预检风险阈值滑块：显示 riskScore >= threshold 的项，与 riskLevelFilter AND 叠加
  // M30.162 B 从持久化回填（clamp 到 [0,100]）
  const [backupRiskThreshold, setBackupRiskThreshold] = useState(() => {
    const v = initialBackupPrefilter.current.riskThreshold;
    if (typeof v !== 'number' || !isFinite(v)) return 0;
    return Math.max(0, Math.min(100, Math.round(v)));
  });
  // M30.143 D 备份应用后 5 秒内撤销：保存 apply 前的整份 presets/snapshots map
  const [pendingBackupUndo, setPendingBackupUndo] = useState<BackupApplySnapshot | null>(null);
  // M30.143 D 撤销倒计时（1 秒 tick），超时自动清除 pendingBackupUndo
  const [backupUndoLeft, setBackupUndoLeft] = useState(0);
  // M30.144 A 键盘导航：ArrowUp/Down/Space/Enter 在过滤后列表内操作（focusIdx 是过滤后数组下标）
  const [backupFocusIdx, setBackupFocusIdx] = useState(0);
  // M30.144 B diff 逐项折叠状态：key 集合，"折叠态覆盖"——即便 checked 或 allDiff 为真也强制隐藏
  const [backupDiffCollapsed, setBackupDiffCollapsed] = useState<Set<string>>(new Set());
  // M30.159 D 备份应用后变更清单：按分组记录本次 apply 的项 key，供 banner 数字点击展开明细浮层
  type BackupAppliedDetail = {
    presetsAdded: string[];
    presetsOverwritten: string[];
    snapshotsAdded: string[];
    snapshotsOverwritten: string[];
    skipped: string[];
    elapsedMs: number;
    at: number;
  };
  const [lastBackupAppliedDetail, setLastBackupAppliedDetail] = useState<BackupAppliedDetail | null>(null);
  const [backupAppliedDetailOpen, setBackupAppliedDetailOpen] = useState(false);
  // M30.161 B apply 变更清单键盘导航状态：focusIdx 在 visible sections 里循环，detailFilterCtrlF 切分类过滤
  const [backupDetailFocusIdx, setBackupDetailFocusIdx] = useState(0);
  const [backupDetailFilter, setBackupDetailFilter] = useState<'all' | 'added' | 'overwritten' | 'skipped'>('all');
  // M30.144 D 备份应用后审计提示：记住新增预设数供 banner 下方提示
  const [lastApplyAddedPresets, setLastApplyAddedPresets] = useState<number | null>(null);
  // M30.145 A undo 影响预览：apply 后 diff 计算撤销会移除/恢复的项数
  const [pendingBackupUndoImpact, setPendingBackupUndoImpact] = useState<UndoImpactDetail | null>(null);
  // M30.146 A undo 影响详情钻取：点击影响数字弹出具体 key 列表
  const [undoImpactDrillOpen, setUndoImpactDrillOpen] = useState(false);
  // M30.145 B 备份预检内联审计：以备份项 key 索引目标表/列存活状态
  const [backupItemAudit, setBackupItemAudit] = useState<Record<string, PresetAuditStatus> | null>(null);
  // M30.154 A 撤销摘要预览浮层：null=关闭，字符串=预览中的 Markdown 文本
  const [backupUndoPreviewText, setBackupUndoPreviewText] = useState<string | null>(null);
  const [backupItemAuditBusy, setBackupItemAuditBusy] = useState(false);
  // M30.154 C 高风险项右键菜单：{x,y,key} 或 null
  const [riskItemCtxMenu, setRiskItemCtxMenu] = useState<{ x: number; y: number; key: string } | null>(null);
  // M30.154 C 高风险项白名单：加入后从勾选剔除，并持久化下次导入自动跳过
  // M30.156 C 存储升级为 v2 {key, addedAt}[] 结构（旧 v1 string[] 兼容加载），30 天自动过期
  type WhitelistEntry = { key: string; addedAt: number };
  type WhitelistPersisted = { v: 2; items: WhitelistEntry[] };
  const WHITELIST_TTL_MS = 30 * 24 * 60 * 60 * 1000; // 30 天
  const loadRiskWhitelist = (): Map<string, number> => {
    try {
      const raw = localStorage.getItem('polydb.riskWhitelist.v1');
      if (!raw) return new Map();
      const parsed = JSON.parse(raw);
      const now = Date.now();
      const out = new Map<string, number>();
      if (Array.isArray(parsed)) {
        // 旧 v1：仅 key 字符串数组，无时间戳——用当前时间兜底（不主动清，用户手动清）
        for (const k of parsed) if (typeof k === 'string') out.set(k, now);
      } else if (parsed && typeof parsed === 'object' && Array.isArray(parsed.items)) {
        for (const e of parsed.items) {
          if (e && typeof e === 'object' && typeof e.key === 'string' && typeof e.addedAt === 'number') {
            // 首次读时若已过 TTL 静默剔除
            if (now - e.addedAt <= WHITELIST_TTL_MS) out.set(e.key, e.addedAt);
          }
        }
      }
      return out;
    } catch { return new Map(); }
  };
  const [riskWhitelist, setRiskWhitelist] = useState<Map<string, number>>(loadRiskWhitelist);
  const addToRiskWhitelist = (key: string) => {
    setRiskWhitelist((prev) => {
      if (prev.has(key)) return prev;
      const now = Date.now();
      const next = new Map(prev);
      next.set(key, now);
      try {
        const payload: WhitelistPersisted = { v: 2, items: Array.from(next, ([k, addedAt]) => ({ key: k, addedAt })) };
        localStorage.setItem('polydb.riskWhitelist.v1', JSON.stringify(payload));
      } catch { /* ignore */ }
      return next;
    });
    setBackupSelected((prev) => {
      if (!prev.has(key)) return prev;
      const next = new Set(prev);
      next.delete(key);
      return next;
    });
    setPresetManagerMsg({ kind: 'ok', text: `🔒 已加入白名单并剔除（30 天后自动失效）：${(() => { const p = key.split('::'); return p.length >= 3 ? `${p[1]}.${p[2]}` : key; })()}` });
  };
  // M30.155 A 白名单管理浮层：可视化 polydb.riskWhitelist.v1，支持单项移除/清空全部
  const [riskWhitelistOpen, setRiskWhitelistOpen] = useState(false);
  // M30.158 B 白名单浮层搜索 + 排序：keyword 按 key 子串过滤，sortBy 切换 key/addedAt 字典序/时间序（desc 升序最新在前）
  const [whitelistSearch, setWhitelistSearch] = useState('');
  const [whitelistSortBy, setWhitelistSortBy] = useState<'key' | 'addedAt' | 'expiring'>('addedAt');
  // M30.163 A 白名单按 kind 过滤（'all'/'preset'/'snapshot'），不持久化
  const [whitelistKindFilter, setWhitelistKindFilter] = useState<'all' | 'preset' | 'snapshot'>('all');
  // M30.159 B 白名单浮层多选：Set 存 key，Shift+Click 行做范围选，普通 Click 单独 toggle
  const [whitelistSel, setWhitelistSel] = useState<Set<string>>(new Set());
  const whitelistAnchorIdx = useRef<number | null>(null);
  // M30.165 A 白名单浮层键盘导航 focusIdx（↑↓ 移动、Enter 复制当前、Delete 移除）
  const [whitelistFocusIdx, setWhitelistFocusIdx] = useState(0);
  const removeRiskWhitelistItem = (key: string) => {
    setRiskWhitelist((prev) => {
      if (!prev.has(key)) return prev;
      const next = new Map(prev);
      next.delete(key);
      try {
        const payload: WhitelistPersisted = { v: 2, items: Array.from(next, ([k, addedAt]) => ({ key: k, addedAt })) };
        localStorage.setItem('polydb.riskWhitelist.v1', JSON.stringify(payload));
      } catch { /* ignore */ }
      return next;
    });
    setWhitelistSel((prev) => { const next = new Set(prev); next.delete(key); return next; });
    setPresetManagerMsg({ kind: 'ok', text: `🔓 已从白名单移除：${(() => { const p = key.split('::'); return p.length >= 3 ? `${p[1]}.${p[2]}` : key; })()}` });
  };
  const clearRiskWhitelist = () => {
    if (riskWhitelist.size === 0) return;
    const n = riskWhitelist.size;
    setRiskWhitelist(new Map());
    setWhitelistSel(new Set());
    whitelistAnchorIdx.current = null;
    try { localStorage.removeItem('polydb.riskWhitelist.v1'); } catch { /* ignore */ }
    setPresetManagerMsg({ kind: 'ok', text: `🧹 已清空白名单（${n} 项）` });
    setRiskWhitelistOpen(false);
  };
  // M30.156 C 每次打开 presetManager 时清理一次过期项（仅 presetManagerOpen 转 true 时触发；
  // M30.160 D：不再依赖 riskWhitelist，避免会话中新增后立即被清理）
  useEffect(() => {
    if (!presetManagerOpen || riskWhitelist.size === 0) return;
    const now = Date.now();
    let removed = 0;
    for (const [, addedAt] of riskWhitelist) {
      if (now - addedAt > WHITELIST_TTL_MS) removed += 1;
    }
    if (removed === 0) return;
    const next = new Map<string, number>();
    for (const [k, addedAt] of riskWhitelist) {
      if (now - addedAt <= WHITELIST_TTL_MS) next.set(k, addedAt);
    }
    try {
      const payload: WhitelistPersisted = { v: 2, items: Array.from(next, ([k, addedAt]) => ({ key: k, addedAt })) };
      localStorage.setItem('polydb.riskWhitelist.v1', JSON.stringify(payload));
    } catch { /* ignore */ }
    setRiskWhitelist(next);
    setPresetManagerMsg({ kind: 'ok', text: `⏳ 白名单过期清理：${removed} 项已超 30 天，已自动剔除` });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [presetManagerOpen]);
  // M30.155 B 备份预检自动跳过白名单项计数（0=无跳过）
  const [backupWhitelistSkipped, setBackupWhitelistSkipped] = useState(0);
  // M30.157 D 备份内嵌白名单：非空时 UI 展示「🔒 N 条白名单」chip + 应用/忽略按钮
  const [backupEmbeddedWhitelist, setBackupEmbeddedWhitelist] = useState<{ key: string; addedAt: number }[] | null>(null);
  // M30.156 A 白名单导出/导入：fileRef 供 file input，importMode='merge'|'replace' 决定合并语义
  // M30.157 A 扩展 CSV 格式：exportFormat 决定 JSON 还是 CSV；两个 fileRef 分别对应 JSON/CSV 导入
  const whitelistFileRef = useRef<HTMLInputElement | null>(null);
  const whitelistCsvFileRef = useRef<HTMLInputElement | null>(null);
  const [whitelistImportMode, setWhitelistImportMode] = useState<'merge' | 'replace'>('merge');
  const [whitelistExportFormat, setWhitelistExportFormat] = useState<'json' | 'csv'>('json');
  const exportRiskWhitelist = () => {
    if (riskWhitelist.size === 0) {
      setPresetManagerMsg({ kind: 'err', text: '⚠ 白名单为空，无内容可导出' });
      return;
    }
    const items = Array.from(riskWhitelist, ([key, addedAt]) => ({ key, addedAt }))
      .sort((a, b) => a.key.localeCompare(b.key));
    const now = new Date();
    const ts = `${now.getFullYear()}-${String(now.getMonth()+1).padStart(2,'0')}-${String(now.getDate()).padStart(2,'0')}_${String(now.getHours()).padStart(2,'0')}${String(now.getMinutes()).padStart(2,'0')}${String(now.getSeconds()).padStart(2,'0')}`;
    let text: string;
    let filename: string;
    let mime: string;
    if (whitelistExportFormat === 'csv') {
      // M30.157 A CSV 格式：手动引号转义防中文乱码，表头 key,addedAt
      const esc = (s: string) => {
        if (/["\n\r,]/.test(s)) {
          return '"' + s.replace(/"/g, '""') + '"';
        }
        return s;
      };
      const rows = items.map((it) => `${esc(it.key)},${it.addedAt}`);
      text = 'key,addedAt\n' + rows.join('\n');
      filename = `polydb-risk-whitelist-${ts}.csv`;
      mime = 'text/csv;charset=utf-8';
    } else {
      text = JSON.stringify({
        version: 2,
        exportedAt: new Date().toISOString(),
        items,
      }, null, 2);
      filename = `polydb-risk-whitelist-${ts}.json`;
      mime = 'application/json';
    }
    const blob = new Blob([text], { type: mime });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    setPresetManagerMsg({ kind: 'ok', text: `📦 已导出白名单 ${filename}（${items.length} 项 · ${text.length} 字符）` });
  };
  const handleRiskWhitelistImport = async (file: File) => {
    try {
      const text = await file.text();
      let parsed: { version?: number; exportedAt?: string; items?: unknown };
      try {
        parsed = JSON.parse(text);
      } catch (e) {
        setPresetManagerMsg({ kind: 'err', text: `❌ JSON 解析失败：${(e as Error).message}` });
        return;
      }
      if (!parsed || typeof parsed !== 'object' || !Array.isArray(parsed.items)) {
        setPresetManagerMsg({ kind: 'err', text: '❌ 文件结构无效：缺少 items 数组' });
        return;
      }
      // 兼容 v1 (string[]) 与 v2 ({key, addedAt}[]) 两种 item 形态
      const now = Date.now();
      const incoming: Array<{ key: string; addedAt: number }> = [];
      for (const x of parsed.items) {
        if (typeof x === 'string') {
          incoming.push({ key: x, addedAt: now });
        } else if (x && typeof x === 'object' && typeof (x as { key?: unknown }).key === 'string') {
          const k = (x as { key: string; addedAt?: unknown }).key;
          const a = (x as { addedAt?: unknown }).addedAt;
          incoming.push({ key: k, addedAt: typeof a === 'number' ? a : now });
        }
      }
      if (incoming.length === 0) {
        setPresetManagerMsg({ kind: 'err', text: '⚠ 导入文件 items 为空' });
        return;
      }
      setRiskWhitelist((prev) => {
        const next = whitelistImportMode === 'replace' ? new Map<string, number>() : new Map(prev);
        for (const { key, addedAt } of incoming) next.set(key, addedAt);
        try {
          const payload: WhitelistPersisted = { v: 2, items: Array.from(next, ([k, a]) => ({ key: k, addedAt: a })) };
          localStorage.setItem('polydb.riskWhitelist.v1', JSON.stringify(payload));
        } catch { /* ignore */ }
        return next;
      });
      const verb = whitelistImportMode === 'replace' ? '覆盖' : '合并';
      setPresetManagerMsg({ kind: 'ok', text: `📥 已${verb}白名单：导入 ${incoming.length} 项 · 当前 ${whitelistImportMode === 'replace' ? incoming.length : '见浮层'} 项` });
    } finally {
      if (whitelistFileRef.current) whitelistFileRef.current.value = '';
    }
  };
  // M30.157 A 白名单 CSV 导入：RFC 4180 简易解析（引号包裹 + 双引号转义）
  // 表头 key,addedAt（首行忽略），若表头顺序颠倒或缺失列会报错
  const handleRiskWhitelistCsvImport = async (file: File) => {
    try {
      const text = await file.text();
      const lines = text.split(/\r?\n/);
      if (lines.length === 0) {
        setPresetManagerMsg({ kind: 'err', text: '⚠ CSV 文件为空' });
        return;
      }
      const parseLine = (line: string): string[] => {
        const out: string[] = [];
        let cur = '';
        let inQuote = false;
        for (let i = 0; i < line.length; i++) {
          const ch = line[i];
          if (inQuote) {
            if (ch === '"') {
              if (i + 1 < line.length && line[i + 1] === '"') { cur += '"'; i++; }
              else inQuote = false;
            } else cur += ch;
          } else {
            if (ch === '"') inQuote = true;
            else if (ch === ',') { out.push(cur); cur = ''; }
            else cur += ch;
          }
        }
        out.push(cur);
        return out;
      };
      // 首行是表头：识别 key / addedAt 列位置（大小写不敏感）
      const header = parseLine(lines[0]).map((h) => h.trim().toLowerCase());
      let keyIdx = header.indexOf('key');
      let addedAtIdx = header.indexOf('addedAt');
      let dataStart = 1;
      // 无表头：假定顺序为 key,addedAt，第一行就是数据
      if (keyIdx < 0) {
        if (lines.length < 1) {
          setPresetManagerMsg({ kind: 'err', text: '❌ CSV 缺少表头或缺少 key 列' });
          return;
        }
        keyIdx = 0;
        addedAtIdx = 1;
        dataStart = 0;
      }
      const now = Date.now();
      const incoming: Array<{ key: string; addedAt: number }> = [];
      for (let li = dataStart; li < lines.length; li++) {
        const raw = lines[li];
        if (!raw.trim()) continue;
        const cells = parseLine(raw);
        const k = cells[keyIdx] ?? '';
        if (!k) continue;
        const aRaw = cells[addedAtIdx];
        const a = aRaw ? Number(aRaw) : now;
        incoming.push({ key: k, addedAt: Number.isFinite(a) && a > 0 ? a : now });
      }
      if (incoming.length === 0) {
        setPresetManagerMsg({ kind: 'err', text: '⚠ CSV 无有效数据行' });
        return;
      }
      setRiskWhitelist((prev) => {
        const next = whitelistImportMode === 'replace' ? new Map<string, number>() : new Map(prev);
        for (const { key, addedAt } of incoming) next.set(key, addedAt);
        try {
          const payload: WhitelistPersisted = { v: 2, items: Array.from(next, ([k, a]) => ({ key: k, addedAt: a })) };
          localStorage.setItem('polydb.riskWhitelist.v1', JSON.stringify(payload));
        } catch { /* ignore */ }
        return next;
      });
      const verb = whitelistImportMode === 'replace' ? '覆盖' : '合并';
      setPresetManagerMsg({ kind: 'ok', text: `📥 已${verb}白名单（CSV）：导入 ${incoming.length} 项 · 当前 ${whitelistImportMode === 'replace' ? incoming.length : '见浮层'} 项` });
    } catch (e) {
      setPresetManagerMsg({ kind: 'err', text: `❌ CSV 解析失败：${(e as Error).message}` });
    } finally {
      if (whitelistCsvFileRef.current) whitelistCsvFileRef.current.value = '';
    }
  };
  // M30.154 B 环形图分档点击筛选：'all' 或 '0'/'1'/'2'/'3'（对应 0 / 1-3 / 4-10 / 11+）
  const [backupDiffBinFilter, setBackupDiffBinFilter] = useState<'all' | '0' | '1' | '2' | '3'>('all');
  const backupItemAuditRef = useRef<Set<string>>(new Set()); // 已扫过的 key，防重复
  // M30.145 C 备份预检快捷键浮层（? 或 Ctrl+/ 打开时备份预检可见才弹）
  const [backupShortcutsOpen, setBackupShortcutsOpen] = useState(false);
  // M30.147 B 备份应用二次确认浮层：Ctrl+Enter 或「✅ 应用」按钮先弹此面板做最后把关
  const [backupConfirmOverlayOpen, setBackupConfirmOverlayOpen] = useState(false);
  // M30.148 D 同 schema.table 分组折叠
  const [backupGroupBy, setBackupGroupBy] = useState(false);
  // M30.163 B 分组折叠状态跨会话持久化 polydb.backupGroupCollapsed.v1（key = schema::table 数组）
  const [backupCollapsedGroups, setBackupCollapsedGroups] = useState<Set<string>>(() => {
    try {
      const raw = localStorage.getItem('polydb.backupGroupCollapsed.v1');
      if (!raw) return new Set();
      const arr = JSON.parse(raw);
      if (Array.isArray(arr)) return new Set(arr.filter((x): x is string => typeof x === 'string'));
      return new Set();
    } catch { return new Set(); }
  });
  // M30.151 C 分组排序方式：diff 按组内变更总数降序 / name 按表名字母序（M30.156 B 从持久化回填）
  const [backupGroupSort, setBackupGroupSort] = useState<'diff' | 'name' | 'risk'>(
    initialBackupPrefilter.current.groupSort ?? 'diff',
  );
  // M30.156 B 持久化 effect：filter/kindFilter/sortBy/groupSort/search 变化即写盘
  // 只写非默认值以压缩 JSON 体积；Alt+R 会重置状态但不会主动清持久化——用户偏好跨会话保留
  useEffect(() => {
    const out: Record<string, unknown> = {};
    if (backupPreviewFilter !== 'all') out.filter = backupPreviewFilter;
    if (backupPreviewKindFilter !== 'all') out.kindFilter = backupPreviewKindFilter;
    if (backupSortBy !== 'diff') out.sortBy = backupSortBy;
    if (backupGroupSort !== 'diff') out.groupSort = backupGroupSort;
    if (backupPreviewSearch) out.search = backupPreviewSearch;
    // M30.159 C 风险级别过滤也持久化
    if (backupRiskLevelFilter !== 'all') out.riskLevelFilter = backupRiskLevelFilter;
    // M30.162 B 阈值 >0 才写入保持稀疏
    if (backupRiskThreshold > 0) out.riskThreshold = backupRiskThreshold;
    try {
      if (Object.keys(out).length === 0) {
        localStorage.removeItem('polydb.backupPrefilter.v1');
      } else {
        localStorage.setItem('polydb.backupPrefilter.v1', JSON.stringify(out));
      }
    } catch { /* 隐私模式 / quota 满时静默 */ }
  }, [backupPreviewFilter, backupPreviewKindFilter, backupSortBy, backupGroupSort, backupPreviewSearch, backupRiskLevelFilter, backupRiskThreshold]);
  // M30.163 B 分组折叠状态持久化 effect：变化即写盘，空集时移除 key 保持 localStorage 干净
  useEffect(() => {
    try {
      if (backupCollapsedGroups.size === 0) {
        localStorage.removeItem('polydb.backupGroupCollapsed.v1');
      } else {
        localStorage.setItem('polydb.backupGroupCollapsed.v1', JSON.stringify(Array.from(backupCollapsedGroups)));
      }
    } catch { /* 隐私模式 / quota 满时静默 */ }
  }, [backupCollapsedGroups]);
  // M30.156 D 备份预检项综合风险评分：diff 数 + 审计状态 + 跨连接三因素合成
  // 输出 {score, level, color, icon, title} 供每行 chip 使用
  const computeItemRiskScore = (i: BackupPreviewItem): {
    score: number;
    level: 'low' | 'mid' | 'high';
    color: string;
    bg: string;
    icon: string;
    label: string;
    parts: string[];
  } => {
    const parts: string[] = [];
    let score = 0;
    const diffTotal = i.kind === 'preset' && i.diff && !i.diff.aligned ? i.diff.total : 0;
    if (diffTotal >= 11) { score += 50; parts.push(`diff=${diffTotal}(大)`); }
    else if (diffTotal >= 4) { score += 30; parts.push(`diff=${diffTotal}(中)`); }
    else if (diffTotal > 0) { score += 15; parts.push(`diff=${diffTotal}(小)`); }
    const srcConn = i.key.split('::')[0];
    const crossConn = !!srcConn && srcConn !== connId;
    if (crossConn) { score += 25; parts.push('跨连接'); }
    if (i.action === 'overwrite') { score += 10; parts.push('覆盖'); }
    const audit = i.kind === 'preset' && backupItemAudit ? backupItemAudit[i.key] : null;
    if (audit === 'dead') { score += 60; parts.push('审计失效'); }
    else if (audit === 'ok') { parts.push('存活'); }
    if (i.kind === 'snapshot' && i.snapshotCount > 0) {
      if (i.snapshotCount >= 10) { score += 10; parts.push(`快照=${i.snapshotCount}(多)`); }
    }
    score = Math.min(100, score);
    const level: 'low' | 'mid' | 'high' = score >= 50 ? 'high' : score >= 20 ? 'mid' : 'low';
    const colorMap = {
      low: { color: 'var(--success, #10b981)', bg: 'rgba(16,185,129,0.12)', icon: '🟢', label: '低风险' },
      mid: { color: 'var(--warn, #d97706)', bg: 'rgba(217,119,6,0.14)', icon: '🟡', label: '中风险' },
      high: { color: 'var(--danger, #dc2626)', bg: 'rgba(220,38,38,0.14)', icon: '🔴', label: '高风险' },
    } as const;
    const c = colorMap[level];
    return { score, level, color: c.color, bg: c.bg, icon: c.icon, label: c.label, parts };
  };
  // M30.149 D 分组 diff 导出：每个分组记录上次导出格式，点击 📥 循环 json→csv→md
  const [backupGroupExportFmt, setBackupGroupExportFmt] = useState<Record<string, 'json' | 'csv' | 'md'>>({});
  // M30.147 C 备份应用后自动审计汇总：{applied, ok, dead}
  const [lastApplyAuditSummary, setLastApplyAuditSummary] = useState<{ applied: number; ok: number; dead: number } | null>(null);
  // M30.147 D 备份文件元信息卡：源文件名 + 备份导出于
  const [backupSourceFileName, setBackupSourceFileName] = useState<string | null>(null);
  // M30.150 B 备份文件 SHA-256 前 8 位哈希（Web Crypto，异步计算）
  const [backupSourceHash, setBackupSourceHash] = useState<string | null>(null);
  // M30.142 备份预检搜索关键字命中高亮：把 match 段包在 <mark> 里
  const highlightMatch = (text: string, q: string): ReactNode => {
    const query = q.trim().toLowerCase();
    if (!query) return text;
    const idx = text.toLowerCase().indexOf(query);
    if (idx < 0) return text;
    return (
      <>
        {text.slice(0, idx)}
        <mark style={{ background: 'rgba(250,204,21,0.55)', color: 'inherit', borderRadius: 2, padding: '0 1px' }}>
          {text.slice(idx, idx + query.length)}
        </mark>
        {text.slice(idx + query.length)}
      </>
    );
  };
  // M30.122 快照字段级对比：存 timestamp 而非索引，防 M30.115 过期后索引漂移
  const [snapDiffTs, setSnapDiffTs] = useState<number | null>(null);
  // M30.123 快照恢复非 aligned 时的 inline 二次确认（存 timestamp+索引，防 M30.115 过期后索引漂移）
  const [pendingRestoreConfirm, setPendingRestoreConfirm] = useState<{ ts: number; idx: number } | null>(null);
  // M30.124 Tombstone 恢复冲突（同 key 已存在）时的 inline 二次确认（key + 索引双匹配）
  const [pendingTombstoneConfirm, setPendingTombstoneConfirm] = useState<{ key: string; idx: number } | null>(null);
  // M30.125 presetManager「🎯 去此表」在同表时的 diff 二次确认（防 Step 3 未保存编辑被静默覆盖）
  const [pendingPresetJumpConfirm, setPendingPresetJumpConfirm] = useState<string | null>(null);
  // M30.136 diff chip 可复制（点击复制 chip 文本到剪贴板，1.2s 视觉确认）
  const [copiedChipText, setCopiedChipText] = useState<string | null>(null);
  // M30.126 confirm 面板内嵌审计徽章：需要跨 IIFE 访问的缓存时长格式化器
  // M30.129 单预设重审：复用 M30.117 runAudit 的单 preset 分支逻辑（listColumns + mappings 检查）
  // M30.130 复用 presetAuditBusy 防与「审计全部」race，也供 AuditBadge busy 态显示 ⏳
  const reAuditOne = useCallback(async (schema: string, table: string) => {
    if (presetAuditBusy) return;
    setPresetAuditBusy(true);
    try {
      const p = getPreset(connId, schema, table);
      if (!p) return;
      const cs = await api.listColumns(connId, schema, table);
      const colNames = new Set(cs.map((c) => c.name));
      const missing: string[] = [];
      for (const m of p.mappings) {
        if (m.targetColumn && !colNames.has(m.targetColumn)) missing.push(m.targetColumn);
      }
      let entry: { status: PresetAuditStatus; reason: string };
      if (missing.length === 0) {
        entry = { status: 'ok', reason: `${cs.length} 列全部匹配 · ${p.mappings.length} 映射有效` };
      } else {
        const top = missing.slice(0, 3).join(', ') + (missing.length > 3 ? ` 等 ${missing.length} 列` : '');
        const status: PresetAuditStatus = missing.length === p.mappings.filter((m) => m.targetColumn).length ? 'dead' : 'warn';
        entry = { status, reason: `目标列已删除：${top}` };
      }
      setPresetAuditResults((prev) => ({ ...(prev ?? {}), [p.key]: entry }));
      updateAuditCacheEntry(connId, p.key, entry);
    } catch {
      const key = presetKey(connId, schema, table);
      setPresetAuditResults((prev) => ({ ...(prev ?? {}), [key]: { status: 'dead', reason: '目标表不存在或不可访问' } }));
      updateAuditCacheEntry(connId, key, { status: 'dead', reason: '目标表不存在或不可访问' });
    } finally {
      setPresetAuditBusy(false);
    }
  }, [connId, presetAuditBusy]);
  // M30.119 打开浮层时加载审计缓存（<24h 复用）
  useEffect(() => {
    if (!presetManagerOpen) { presetAuditCheckedRef.current = ''; setPresetAuditCachedAt(null); return; }
    if (presetAuditCheckedRef.current === connId) return;
    presetAuditCheckedRef.current = connId;
    const cache = loadAuditCache(connId);
    if (cache) {
      const strip: Record<string, { status: PresetAuditStatus; reason: string }> = {};
      for (const [k, v] of Object.entries(cache.results)) strip[k] = { status: v.status, reason: v.reason };
      setPresetAuditResults(strip);
      setPresetAuditCachedAt(cache.auditedAt);
    }
  }, [presetManagerOpen, connId]);
  const [presetChipExpanded, setPresetChipExpanded] = useState(false);
  const [applyOldConfirmAt, setApplyOldConfirmAt] = useState<number | null>(null);
  const [applyOldTick, setApplyOldTick] = useState(0);
  useEffect(() => {
    if (applyOldConfirmAt == null) return;
    const t = setInterval(() => setApplyOldTick((n) => n + 1), 500);
    return () => clearInterval(t);
  }, [applyOldConfirmAt]);
  useEffect(() => {
    if (applyOldConfirmAt == null) return;
    const left = applyOldConfirmAt + 5000 - Date.now();
    if (left <= 0) { setApplyOldConfirmAt(null); return; }
    const t = setTimeout(() => setApplyOldConfirmAt(null), left);
    return () => clearTimeout(t);
  }, [applyOldConfirmAt]);
  useEffect(() => {
    if (step !== 'preview' || busy) setApplyOldConfirmAt(null);
  }, [step, busy]);
  const [delPresetConfirmAt, setDelPresetConfirmAt] = useState<number | null>(null);
  const [delPresetTick, setDelPresetTick] = useState(0);
  useEffect(() => {
    if (delPresetConfirmAt == null) return;
    const t = setInterval(() => setDelPresetTick((n) => n + 1), 500);
    return () => clearInterval(t);
  }, [delPresetConfirmAt]);
  useEffect(() => {
    if (delPresetConfirmAt == null) return;
    const left = delPresetConfirmAt + 5000 - Date.now();
    if (left <= 0) { setDelPresetConfirmAt(null); return; }
    const t = setTimeout(() => setDelPresetConfirmAt(null), left);
    return () => clearTimeout(t);
  }, [delPresetConfirmAt]);
  useEffect(() => {
    if (step !== 'preview' || busy) setDelPresetConfirmAt(null);
  }, [step, busy]);
  const [presetCopyAt, setPresetCopyAt] = useState<number | null>(null);
  useEffect(() => {
    if (presetCopyAt == null) return;
    const t = setTimeout(() => setPresetCopyAt(null), 2000);
    return () => clearTimeout(t);
  }, [presetCopyAt]);
  // M30.112 删除预设后 30s 内可撤销（Gmail 风格 transient undo）
  const [delPresetRestore, setDelPresetRestore] = useState<{
    preset: ImportPreset;
    snapshots: ImportPreset[];
    at: number;
    expiresAt: number;
    connId: string;
    schema: string;
    table: string;
  } | null>(null);
  const [delPresetRestoreTick, setDelPresetRestoreTick] = useState(0);
  useEffect(() => {
    if (delPresetRestore == null) return;
    const t = setInterval(() => setDelPresetRestoreTick((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, [delPresetRestore]);
  useEffect(() => {
    if (delPresetRestore == null) return;
    const left = delPresetRestore.expiresAt - Date.now();
    if (left <= 0) { setDelPresetRestore(null); return; }
    const t = setTimeout(() => setDelPresetRestore(null), left);
    return () => clearTimeout(t);
  }, [delPresetRestore]);
  useEffect(() => {
    const h = (ev: KeyboardEvent) => {
      // Ctrl+/ 开关（不在输入控件内时生效）
      if (ev.ctrlKey && !ev.altKey && !ev.metaKey && !ev.shiftKey && ev.key === '/') {
        const t = ev.target as HTMLElement | null;
        if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
        ev.preventDefault();
        setHelpPanelOpen((v) => !v);
        return;
      }
      // Esc 关闭
      if (ev.key === 'Escape' && !ev.ctrlKey && !ev.altKey && !ev.metaKey && !ev.shiftKey) {
        setHelpPanelOpen((v) => (v ? false : v));
      }
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, []);
  const [progress, setProgress] = useState<{ done: number; total: number; ms: number; batch: number; batchTotal: number; batchDone: number; batchSize: number; batchMs: number[] } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [insertedCount, setInsertedCount] = useState(0);
  const [failedRows, setFailedRows] = useState<FailedRow[]>([]);
  const [dragActive, setDragActive] = useState(false);
  const [retryIndices, setRetryIndices] = useState<Set<number> | null>(null);
  const [encoding, setEncoding] = useState<InputEncoding | 'auto'>('auto');
  const [detectedEncoding, setDetectedEncoding] = useState<InputEncoding | null>(null);
  const [presetApplied, setPresetApplied] = useState<{
    matched: number;
    total: number;
    removed: { csvName: string; targetColumn: string }[];
    added: string[];
  } | null>(null);
  // 保存当前已应用预设的引用，用于「当前配置 vs 预设」差异计算
  const [appliedPreset, setAppliedPreset] = useState<ImportPreset | null>(null);
  const [presetSnapshotsRefresh, setPresetSnapshotsRefresh] = useState(0);
  // 历史跳转标记：下一次 tables effect 触发时跳过 contextTable 自动覆盖 selTable
  const jumpRef = useRef(false);
  // 失败行回跳：csvRow 点击后高亮 textarea 里对应行
  const [csvRowFlash, setCsvRowFlash] = useState<number | null>(null);
  const csvTextRef = useRef<HTMLTextAreaElement | null>(null);
  const [showHistory, setShowHistory] = useState(false);
  // 历史面板筛选/排序/导出（M30.47）
  type HistorySortKey = 'at' | 'totalRows' | 'ms' | 'failedRows';
  const [historyStatusFilter, setHistoryStatusFilter] = useState<'' | ImportHistoryEntry['status']>('');
  const [historySort, setHistorySort] = useState<HistorySortKey>('at');
  // M30.111: 仅显示当前表的历史条目（step==='preview' 且 selSchema/selTable 有值时可勾选）
  const [historyTableOnly, setHistoryTableOnly] = useState(false);
  const [history, setHistory] = useState<ImportHistoryEntry[]>([]);
  const [fileQueue, setFileQueue] = useState<{ file: File; format: ImportFormat; name: string }[]>([]);
  const [queuePos, setQueuePos] = useState(0);
  const [overrides, setOverrides] = useState<Record<number, string[]>>({});
  const [pkCheckEnabled, setPkCheckEnabled] = useState(false);
  const [pkCheckBusy, setPkCheckBusy] = useState(false);
  const [pkCheckResult, setPkCheckResult] = useState<{ found: number; total: number; ms: number; error: string | null } | null>(null);
  const [fkCheckEnabled, setFkCheckEnabled] = useState(false);
  const [fkCheckBusy, setFkCheckBusy] = useState(false);
  const [fkCheckResult, setFkCheckResult] = useState<{ valid: number; invalid: number; total: number; ms: number; error: string | null } | null>(null);
  const [qualityGate, setQualityGate] = useState<{ enabled: boolean; threshold: number }>(() => {
    try {
      const raw = localStorage.getItem('polydb.qualityGate.v1');
      if (raw) {
        const p = JSON.parse(raw) as { enabled?: boolean; threshold?: number };
        return { enabled: p.enabled ?? false, threshold: typeof p.threshold === 'number' ? p.threshold : 80 };
      }
    } catch { /* ignore */ }
    return { enabled: false, threshold: 80 };
  });
  const qualityGateOverrideRef = useRef(false);
  const [dupPkCheckEnabled, setDupPkCheckEnabled] = useState(false);
  const [dupPkCheckBusy, setDupPkCheckBusy] = useState(false);
  const [dupPkResult, setDupPkResult] = useState<{
    duplicates: { csvRow: number; key: string }[];
    totalKeys: number;
    dupKeys: number;
    ms: number;
    error: string | null;
  } | null>(null);
  const [autoPreviewOnSuccess, setAutoPreviewOnSuccess] = useState<boolean>(() => {
    try {
      const v = localStorage.getItem('polydb.autoPreviewImport.v1');
      return v === 'true' ? true : v === 'false' ? false : false;
    } catch { return false; }
  });
  // 自动预览时只取最近 N 行（M30.40）：优先按 PK IN(...)，无 PK 时降级到 LIMIT N
  const [autoPreviewRows, setAutoPreviewRows] = useState<number>(() => {
    try {
      const v = localStorage.getItem('polydb.autoPreviewRows.v1');
      const n = v ? parseInt(v, 10) : NaN;
      return Number.isFinite(n) && n >= 1 && n <= 10000 ? n : 50;
    } catch { return 50; }
  });
  // Step 3 列映射过滤器：all / mapped / unmapped（M30.44）；low-health 追加于 M30.74；critical-health 追加于 M30.87
  type MappingFilter = 'all' | 'mapped' | 'unmapped' | 'low-health' | 'critical-health';
  const [mappingFilter, setMappingFilter] = useState<MappingFilter>(
    () => {
      try {
        const v = localStorage.getItem('polydb.mappingFilter.v1');
        if (v === 'all' || v === 'mapped' || v === 'unmapped' || v === 'low-health' || v === 'critical-health') return v;
      } catch { /* ignore */ }
      return 'all';
    },
  );
  const persistMappingFilter = useCallback((v: MappingFilter) => {
    setMappingFilter(v);
    try { localStorage.setItem('polydb.mappingFilter.v1', v); } catch { /* ignore */ }
  }, []);
  // M30.81 映射行排序（仅影响视觉顺序，不改数据索引）
  type MappingSort = 'index' | 'health-asc' | 'empty-desc' | 'target-asc' | 'csv-asc';
  const [mappingSort, setMappingSort] = useState<MappingSort>(() => {
    try {
      const v = localStorage.getItem('polydb.mappingSort.v1');
      if (v === 'index' || v === 'health-asc' || v === 'empty-desc' || v === 'target-asc' || v === 'csv-asc') return v;
    } catch { /* ignore */ }
    return 'index';
  });
  const persistMappingSort = useCallback((v: MappingSort) => {
    setMappingSort(v);
    try { localStorage.setItem('polydb.mappingSort.v1', v); } catch { /* ignore */ }
  }, []);
  const [mappingSearch, setMappingSearch] = useState('');
  // M30.76 搜索历史：localStorage 持久化 + ↑/↓ 循环 + Enter 选中 + Esc 关闭下拉
  const [mappingSearchHistory, setMappingSearchHistory] = useState<string[]>(() => {
    try {
      const raw = localStorage.getItem('polydb.mappingSearchHistory.v1');
      const arr = raw ? (JSON.parse(raw) as unknown) : [];
      if (!Array.isArray(arr)) return [];
      return arr.filter((x): x is string => typeof x === 'string' && x.trim().length > 0).slice(0, 20);
    } catch { return []; }
  });
  const [mappingSearchHistOpen, setMappingSearchHistOpen] = useState(false);
  const [mappingSearchHistIdx, setMappingSearchHistIdx] = useState(-1);
  // M30.89 Ctrl+J/K 跳到下一个/上一个 health&lt;80 的行；cursor 记录上次停留位置，循环遍历
  const jumpCursor = useRef<number>(-1);
  // M30.91 Ctrl+Shift+J/K 跳到下一个/上一个未映射列（targetColumn==null）；独立 cursor 避免与 health 跳转混淆
  const jumpCursorUnmapped = useRef<number>(-1);
  // M30.92 Ctrl+J/K + Alt+N/P 跳转后目标行短暂闪烁（1.2s accent 高亮）
  const [jumpFlashIdx, setJumpFlashIdx] = useState<number | null>(null);
  const flashJump = useCallback((i: number) => {
    setJumpFlashIdx(i);
    window.setTimeout(() => setJumpFlashIdx((cur) => (cur === i ? null : cur)), 1200);
  }, []);
  // M30.98 Step 3 Ctrl+F 聚焦列搜索框；与 Alt+0（清空）互补；浏览器 Find 惯用键；select 现有文本便于覆盖输入
  const mapSearchInputRef = useRef<HTMLInputElement | null>(null);
  useEffect(() => {
    if (step !== 'map') return;
    const h = (ev: KeyboardEvent) => {
      if (!ev.ctrlKey || ev.altKey || ev.metaKey || ev.shiftKey) return;
      const key = ev.key.toLowerCase();
      if (key !== 'f') return;
      const t = ev.target as HTMLElement | null;
      if (t === mapSearchInputRef.current) return;
      ev.preventDefault();
      mapSearchInputRef.current?.focus();
      mapSearchInputRef.current?.select();
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [step]);
  // M30.97 Step 切换后新激活的 badge 短暂闪烁（1.2s accent 高亮），与 M30.92 map-row-jump-flash 视觉一致；覆盖所有 step 变更路径（badge 点击 + Ctrl+Alt 键盘 + setStep('done') 自动完成）；首次挂载不闪烁
  const [stepFlash, setStepFlash] = useState(false);
  const stepFirstMountRef = useRef(true);
  useEffect(() => {
    if (stepFirstMountRef.current) { stepFirstMountRef.current = false; return; }
    setStepFlash(true);
    const t = window.setTimeout(() => setStepFlash(false), 1200);
    return () => window.clearTimeout(t);
  }, [step]);
  const saveMappingSearchHistory = (term: string) => {
    const t = term.trim();
    if (!t) return;
    setMappingSearchHistory((prev) => {
      const next = [t, ...prev.filter((x) => x !== t)].slice(0, 20);
      try { localStorage.setItem('polydb.mappingSearchHistory.v1', JSON.stringify(next)); } catch { /* ignore */ }
      return next;
    });
  };
  // M30.75 搜索关键字在列名中高亮：把 text 按 q 切成 [未命中, 命中, 未命中, ...] 分段
  const highlightQuery = (text: string): (string | JSX.Element)[] => {
    const q = mappingSearch.trim().toLowerCase();
    if (!q || !text) return [text];
    const lower = text.toLowerCase();
    const out: (string | JSX.Element)[] = [];
    let i = 0;
    let hit = lower.indexOf(q, i);
    if (hit === -1) return [text];
    while (hit !== -1) {
      if (hit > i) out.push(text.slice(i, hit));
      out.push(
        <mark
          key={`${hit}`}
          style={{
            background: 'rgba(59,130,246,0.25)',
            color: 'var(--accent, #3b82f6)',
            padding: '0 2px',
            borderRadius: 2,
            fontWeight: 700,
          }}
        >
          {text.slice(hit, hit + q.length)}
        </mark>,
      );
      i = hit + q.length;
      hit = lower.indexOf(q, i);
    }
    if (i < text.length) out.push(text.slice(i));
    return out;
  };
  // Step 3 批量选择：Shift+Click 拖选多行，一次设置转换/空值策略（M30.45）
  const [batchAnchor, setBatchAnchor] = useState<number | null>(null);
  const [batchSel, setBatchSel] = useState<Set<number>>(new Set());
  const [failCategoryFilter, setFailCategoryFilter] = useState<string | null>(null);
  const [expandedFailRow, setExpandedFailRow] = useState<number | null>(null);
  interface FixEntry { key: string; label: string; icon: string; snapshot: ImportOptions; msg: string; at: number; }
  // 撤销/重做栈持久化到 localStorage 'polydb.undoStack.v1'（含 redoStack + batchStepN）
  // 惰性恢复：解析失败/类型不符/size>512KB 时静默丢弃；QuotaExceededError 时上报一次
  const fixStackWarnRef = useRef(false);
  const MAX_FIX_HISTORY = 20;
  const isFixEntry = (x: unknown): x is FixEntry => {
    if (!x || typeof x !== 'object') return false;
    const o = x as Record<string, unknown>;
    return typeof o.key === 'string' && typeof o.label === 'string' && typeof o.icon === 'string'
      && typeof o.msg === 'string' && typeof o.at === 'number' && !!o.snapshot && typeof o.snapshot === 'object';
  };
  const [undoStack, setUndoStack] = useState<FixEntry[]>(() => {
    try {
      const raw = localStorage.getItem('polydb.undoStack.v1');
      if (!raw) return [];
      const p = JSON.parse(raw) as { undo?: unknown; redo?: unknown; n?: unknown };
      if (!Array.isArray(p.undo)) return [];
      return p.undo.filter(isFixEntry).slice(0, MAX_FIX_HISTORY);
    } catch { return []; }
  });
  const [redoStack, setRedoStack] = useState<FixEntry[]>(() => {
    try {
      const raw = localStorage.getItem('polydb.undoStack.v1');
      if (!raw) return [];
      const p = JSON.parse(raw) as { redo?: unknown };
      if (!Array.isArray(p.redo)) return [];
      return p.redo.filter(isFixEntry).slice(0, MAX_FIX_HISTORY);
    } catch { return []; }
  });
  const [batchStepN, setBatchStepN] = useState(() => {
    try {
      const raw = localStorage.getItem('polydb.undoStack.v1');
      if (!raw) return 1;
      const p = JSON.parse(raw) as { n?: unknown };
      const n = typeof p.n === 'number' ? p.n : 1;
      return Number.isFinite(n) && n >= 1 ? Math.min(n, MAX_FIX_HISTORY) : 1;
    } catch { return 1; }
  });
  // 折叠的 diff 分组集合（默认全展开，只有用户手动折叠时才进入此 Set）
  // 持久化到 localStorage 'polydb.diffCollapsedGroups.v1'，跨会话保留用户偏好
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(() => {
    try {
      const raw = localStorage.getItem('polydb.diffCollapsedGroups.v1');
      if (!raw) return new Set();
      const arr = JSON.parse(raw);
      if (!Array.isArray(arr)) return new Set();
      return new Set(arr.filter((x): x is string => typeof x === 'string'));
    } catch { return new Set(); }
  });
  const [diffOpen, setDiffOpen] = useState(false);
  // diff 面板搜索关键字：按 field/before/after 子串过滤，命中时自动展开所属分组
  const [diffSearch, setDiffSearch] = useState('');
  // diff 搜索大小写敏感：默认不敏感；用户手动切换（Aa 按钮或 Alt+C）
  const [diffSearchCaseSensitive, setDiffSearchCaseSensitive] = useState(() => {
    try { return localStorage.getItem('polydb.diffSearchCase.v1') === '1'; } catch { return false; }
  });
  useEffect(() => {
    try { localStorage.setItem('polydb.diffSearchCase.v1', diffSearchCaseSensitive ? '1' : '0'); } catch { /* ignore */ }
  }, [diffSearchCaseSensitive]);
  // diff 搜索字段作用域：all/field/before/after 四值（M30.33），持久化到 localStorage
  const [diffSearchScope, setDiffSearchScope] = useState<DiffSearchScope>(() => {
    try {
      const v = localStorage.getItem('polydb.diffSearchScope.v1');
      if (v === 'all' || v === 'field' || v === 'before' || v === 'after') return v;
    } catch { /* ignore */ }
    return 'all';
  });
  useEffect(() => {
    try { localStorage.setItem('polydb.diffSearchScope.v1', diffSearchScope); } catch { /* ignore */ }
  }, [diffSearchScope]);
  // diff 搜索历史：最近 10 条关键字（去重、最新在前）；focus 时下拉，↑/↓ 选择，Enter 采用
  const [diffSearchHistory, setDiffSearchHistory] = useState<string[]>(() => {
    try {
      const raw = localStorage.getItem('polydb.diffSearchHistory.v1');
      if (!raw) return [];
      const arr = JSON.parse(raw);
      if (!Array.isArray(arr)) return [];
      return arr.filter((x): x is string => typeof x === 'string' && x.trim().length > 0).slice(0, 10);
    } catch { return []; }
  });
  const [diffSearchHistoryOpen, setDiffSearchHistoryOpen] = useState(false);
  const [diffSearchHistoryCursor, setDiffSearchHistoryCursor] = useState(-1);

  // 提交搜索关键字到历史（Enter 循环 / Esc / 点击 ✕ 等触发点调用）
  const commitDiffSearchHistory = (q: string) => {
    const trimmed = q.trim();
    if (!trimmed) return;
    setDiffSearchHistory((prev) => {
      const next = [trimmed, ...prev.filter((x) => x !== trimmed)].slice(0, 10);
      try { localStorage.setItem('polydb.diffSearchHistory.v1', JSON.stringify(next)); } catch { /* ignore */ }
      return next;
    });
  };
  // diff 面板键盘光标：当前聚焦的分组在渲染列表中的索引（-1 = 无光标）
  const [diffCursor, setDiffCursor] = useState(-1);
  // diff 搜索命中光标：在"展开态可见 items"扁平列表中的索引（0-based，-1 = 无光标）
  // Enter/Shift+Enter 或 ▲▼ 按钮循环切换；命中位置自动 scrollIntoView
  const [diffHitCursor, setDiffHitCursor] = useState(-1);
  const diffSearchInputRef = useRef<HTMLInputElement>(null);
  // 每个 (groupKey, item.field) 的稳定 DOM ref，供光标跳转时 scrollIntoView
  const diffItemRefs = useRef<Map<string, HTMLElement>>(new Map());
  const getDiffItemRef = (groupKey: string, field: string) => (el: HTMLElement | null) => {
    const k = `${groupKey}\u0000${field}`;
    if (el) diffItemRefs.current.set(k, el);
    else diffItemRefs.current.delete(k);
  };
  useEffect(() => {
    try {
      const serialized = JSON.stringify(Array.from(collapsedGroups));
      localStorage.setItem('polydb.diffCollapsedGroups.v1', serialized);
      // 容量告警：单个 key 超 4KB 提示（分组名单量小，超过阈值通常是异常数据）
      if (serialized.length > 4096) {
        publishEditorStatus({
          message: `⚠️ diff 分组折叠偏好已达 ${serialized.length}B，建议清空（命令面板：导入 → 清空分组折叠偏好）`,
          messageAt: Date.now(),
        });
      }
    } catch (err) {
      // 常见：隐私模式或 quota 满 —— 只报一次警告，避免刷屏
      try {
        if (!sessionStorage.getItem('polydb.diffCollapsedWarn')) {
          sessionStorage.setItem('polydb.diffCollapsedWarn', '1');
          publishEditorStatus({
            message: err instanceof DOMException && err.name === 'QuotaExceededError'
              ? '⚠️ localStorage 已满，diff 分组折叠偏好未持久化'
              : '⚠️ localStorage 不可用（隐私模式或已禁用），diff 分组折叠偏好仅本次会话有效',
            messageAt: Date.now(),
          });
        }
      } catch { /* ignore */ }
    }
  }, [collapsedGroups]);
  useEffect(() => {
    try { localStorage.setItem('polydb.autoPreviewImport.v1', String(autoPreviewOnSuccess)); } catch { /* ignore */ }
  }, [autoPreviewOnSuccess]);
  useEffect(() => {
    try { localStorage.setItem('polydb.autoPreviewRows.v1', String(autoPreviewRows)); } catch { /* ignore */ }
  }, [autoPreviewRows]);
  useEffect(() => {
    try { localStorage.setItem('polydb.qualityGate.v1', JSON.stringify(qualityGate)); } catch { /* ignore */ }
  }, [qualityGate]);
  // 撤销/重做栈 + batchStepN 持久化到 localStorage
  // 上限 512KB，超阈值时只保留栈顶 5 条防止挤爆；QuotaExceededError 时降级并一次性提示
  useEffect(() => {
    const payload = { undo: undoStack, redo: redoStack, n: batchStepN };
    try {
      const raw = JSON.stringify(payload);
      if (raw.length > FIX_STACK_MAX_BYTES) {
        // 超阈值降级：只保留栈顶 5 条（最近的 5 项撤销历史）
        const slim = { undo: undoStack.slice(0, 5), redo: redoStack.slice(0, 5), n: batchStepN };
        const slimRaw = JSON.stringify(slim);
        localStorage.setItem('polydb.undoStack.v1', slimRaw);
        if (!fixStackWarnRef.current) {
          fixStackWarnRef.current = true;
          publishEditorStatus({
            message: `⚠️ 撤销栈序列化 ${raw.length}B 超阈值 ${FIX_STACK_MAX_BYTES / 1024}KB，仅保留栈顶 5 条`,
            messageAt: Date.now(),
          });
        }
      } else {
        localStorage.setItem('polydb.undoStack.v1', raw);
      }
    } catch (err) {
      if (!fixStackWarnRef.current) {
        fixStackWarnRef.current = true;
        publishEditorStatus({
          message: err instanceof DOMException && err.name === 'QuotaExceededError'
            ? '⚠️ localStorage 已满，撤销栈未持久化'
            : '⚠️ localStorage 不可用，撤销栈仅本次会话有效',
          messageAt: Date.now(),
        });
      }
    }
  }, [undoStack, redoStack, batchStepN]);
  useEffect(() => {
    setUndoStack([]);
    setRedoStack([]);
    setDiffOpen(false);
  }, [inputFormat, selTable, selSchema]);
  const [pendingQualitySuggestions, setPendingQualitySuggestions] = useState<Record<number, QualityTransformSuggestion[]> | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const txIdRef = useRef<string | null>(null);
  const parseRef = useRef<CsvParseResult | null>(null);
  const cancelRef = useRef(false);
  const handleFileRef = useRef<((f: File, fmt: ImportFormat) => Promise<void>) | null>(null);
  const rawBufRef = useRef<ArrayBuffer | null>(null);

  useEffect(() => { parseRef.current = parse; }, [parse]);

  // kind 更新时同步到 opts
  useEffect(() => { setOpts((o) => ({ ...o, kind })); }, [kind]);

  // 自动解析（150ms debounce），Step 1 实时预览
  useEffect(() => {
    if (!csvText.trim()) { setParse(null); return; }
    const id = setTimeout(() => {
      try {
        if (inputFormat === 'sql') {
          setParse(null);
          setError(null);
        } else if (inputFormat === 'jsonl') {
          const { result, error: err } = parseJsonl(csvText);
          if (err) { setParse(null); setError(err); return; }
          setParse(result as unknown as CsvParseResult);
        } else {
          const d = delimiter ?? detectDelimiter(csvText);
          const r = parseCsv(csvText, { delimiter: d, hasHeader });
          setParse(r);
        }
        if (parse?.columns.length && cols.length > 0) setMappings(inferMapping(parse.columns, cols));
      } catch (e) { setError(toMsg(e)); }
    }, 150);
    return () => clearTimeout(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [csvText, delimiter, hasHeader, inputFormat]);

  const profiles: ColumnProfile[] = useMemo(
    () => (parse ? profileColumns(parse.rows, parse.columns) : []),
    [parse],
  );
  const qualityReport: DataQualityReport | null = useMemo(() => {
    if (!parse || profiles.length === 0) return null;
    if (parse.rows.length > 100_000) return null;
    return scoreDataQuality(parse.rows, parse.columns, profiles);
  }, [parse, profiles]);
  const qualityGateBlocked = useMemo(() => {
    if (!qualityGate.enabled || inputFormat === 'sql') return false;
    if (!qualityReport) return false;
    return qualityReport.overallScore < qualityGate.threshold;
  }, [qualityGate, inputFormat, qualityReport]);
  useEffect(() => {
    qualityGateOverrideRef.current = false;
  }, [parse, mappings, opts]);
  const mappingSuggestions: MappingSuggestion[] = useMemo(() => {
    if (!parse || cols.length === 0 || mappings.length === 0) return [];
    return suggestMappings(parse.columns, cols, mappings);
  }, [parse, cols, mappings]);
  const colInfoMap = useMemo(() => {
    const m = new Map<string, ColumnInfo>();
    for (const c of cols) m.set(c.name, c);
    return m;
  }, [cols]);
  const warnFor = (i: number): string | null => {
    const p = profiles[i];
    const m = mappings[i];
    if (!p || !m?.targetColumn) return null;
    const tc = colInfoMap.get(m.targetColumn);
    if (!tc) return null;
    if (!compatible(tc.data_type, p.type)) {
      return `类型不匹配：CSV=${p.type} → 目标 ${tc.data_type}`;
    }
    return null;
  };
  // M30.63: 每列健康分聚合，供 Step 3 badge / Step 4 分布 / 门禁最差列清单共用
  const columnHealths = useMemo(() => {
    if (!parse || mappings.length === 0) return null;
    const dupTargets = new Map<string, number[]>();
    for (let i = 0; i < mappings.length; i++) {
      const t = mappings[i].targetColumn;
      if (!t) continue;
      if (!dupTargets.has(t)) dupTargets.set(t, []);
      dupTargets.get(t)!.push(i);
    }
    const shadowed = new Set<number>();
    for (const idxs of dupTargets.values()) {
      if (idxs.length > 1) {
        for (let k = 0; k < idxs.length - 1; k++) shadowed.add(idxs[k]);
      }
    }
    const out: { i: number; health: number; band: 'green' | 'orange' | 'amber' | 'red'; warn: string | null }[] = [];
    for (let i = 0; i < mappings.length; i++) {
      const m = mappings[i];
      const p = profiles[i];
      const qc = qualityReport?.columns[i];
      const isDupShadowed = shadowed.has(i);
      const v = opts.validations?.[i];
      const vCfg = !!(v && hasAnyValidation(v));
      const warn = warnFor(i);
      let health = 100;
      if (!m.targetColumn) health -= 40;
      if (isDupShadowed) health -= 25;
      if (p && p.nullCount > 0 && !vCfg) health -= Math.min(15, Math.round((p.nullCount / Math.max(1, p.total)) * 15));
      if (qc && qc.suspiciousCount > 0 && qc.nonNull > 0) health -= Math.min(15, Math.round((qc.suspiciousCount / qc.nonNull) * 100 * 0.15));
      if (qc && qc.typePurity < 0.9 && qc.nonNull > 0) health -= Math.round((0.9 - qc.typePurity) * 50);
      if (warn) health -= 10;
      health = Math.max(0, health);
      const band = health >= 80 ? 'green' : health >= 60 ? 'orange' : health >= 40 ? 'amber' : 'red';
      out.push({ i, health, band, warn });
    }
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [parse, mappings, profiles, qualityReport, opts.validations, colInfoMap]);

  // 拖拽 CSV/TXT 到窗口直接载入
  useEffect(() => {
    let counter = 0;
    const onEnter = (ev: DragEvent) => {
      if (!ev.dataTransfer?.types.includes('Files')) return;
      ev.preventDefault();
      counter++;
      setDragActive(true);
    };
    const onLeave = () => {
      counter--;
      if (counter <= 0) { counter = 0; setDragActive(false); }
    };
    const onOver = (ev: DragEvent) => { ev.preventDefault(); };
    const detectFmt = (name: string): 'csv' | 'jsonl' | 'sql' => {
      const lower = name.toLowerCase();
      if (lower.endsWith('.sql')) return 'sql';
      if (lower.endsWith('.jsonl') || lower.endsWith('.ndjson') || lower.endsWith('.json')) return 'jsonl';
      return 'csv';
    };
    const onDrop = async (ev: DragEvent) => {
      ev.preventDefault();
      counter = 0;
      setDragActive(false);
      const files = Array.from(ev.dataTransfer?.files ?? []);
      if (files.length === 0) return;
      if (files.length > 1) {
        setFileQueue(files.map((f) => ({ file: f, format: detectFmt(f.name), name: f.name })));
        setQueuePos(0);
      }
      const f = files[0];
      await handleFileRef.current?.(f, detectFmt(f.name));
    };
    window.addEventListener('dragenter', onEnter);
    window.addEventListener('dragleave', onLeave);
    window.addEventListener('dragover', onOver);
    window.addEventListener('drop', onDrop);
    return () => {
      window.removeEventListener('dragenter', onEnter);
      window.removeEventListener('dragleave', onLeave);
      window.removeEventListener('dragover', onOver);
      window.removeEventListener('drop', onDrop);
    };
  }, []);

  // 载入 schema（step 变为 table 或首次打开）
  useEffect(() => {
    if (step !== 'table') return;
    if (contextSchema) setSelSchema(contextSchema);
    setSchemaLoading(true);
    api.listSchemas(connId)
      .then((ss) => {
        setSchemas(ss);
        if (!contextSchema && ss.length > 0 && !selSchema) setSelSchema(ss[0].name);
      })
      .catch((e: unknown) => setError(toMsg(e)))
      .finally(() => setSchemaLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [step, connId]);

  // 载入 tables（step=table 且 selSchema 变化时）
  useEffect(() => {
    if (step !== 'table' || !selSchema) return;
    setTablesLoading(true);
    api.listTables(connId, selSchema)
      .then((ts) => {
        const tsOnly = ts.filter((t) => t.type === 'table');
        setTables(tsOnly);
        if (jumpRef.current) {
          jumpRef.current = false;
        } else if (contextTable) {
          setSelTable(contextTable);
        }
      })
      .catch((e: unknown) => setError(toMsg(e)))
      .finally(() => setTablesLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [step, selSchema, connId]);

  // 载入 columns（step=table 且 selTable 变化时；完成后自动进 map）
  useEffect(() => {
    if (step !== 'table' || !selSchema || !selTable) return;
    let cancelled = false;
    setColsLoading(true);
    api.listColumns(connId, selSchema, selTable)
      .then((cs) => {
        if (cancelled) return;
        setCols(cs);
        const preset = getPreset(connId, selSchema, selTable);
        if (preset) {
          const r = applyPreset(preset, cs);
          setMappings(r.mappings);
          setOpts((o) => ({
            ...o,
            mode: r.mode,
            transforms: r.transforms,
            transformParams: r.transformParams,
            emptyAsNull: r.opts.emptyAsNull,
            batchSize: r.opts.batchSize,
            skipFailed: r.opts.skipFailed,
            filterColumn: r.opts.filterColumn,
            filterOp: r.opts.filterOp,
            filterValue: r.opts.filterValue,
            validations: r.opts.validations,
            strictValidation: r.opts.strictValidation,
            nullPolicies: r.opts.nullPolicies,
          }));
          setPresetApplied({
            matched: r.matchedCols,
            total: r.totalCols,
            removed: r.removedTargets.map((t) => ({ csvName: t.csvName, targetColumn: t.targetColumn })),
            added: r.addedTargets,
          });
          setAppliedPreset(preset);
        } else {
          setPresetApplied(null);
          setAppliedPreset(null);
          if (parseRef.current) setMappings(inferMapping(parseRef.current.columns, cs));
        }
        if (pendingQualitySuggestions && Object.keys(pendingQualitySuggestions).length > 0) {
          const sugg = pendingQualitySuggestions;
          const nextTransforms: Record<number, ColumnTransform> = {};
          const nextParams: Record<number, Record<string, string>> = {};
          let applied = 0;
          for (const [iStr, list] of Object.entries(sugg)) {
            const i = Number(iStr);
            const first = list[0];
            if (!first) continue;
            if (first.transform === 'regex-replace') {
              nextTransforms[i] = 'regex-replace';
              nextParams[i] = { pattern: first.pattern, replacement: first.replacement, flags: first.flags };
            } else {
              nextTransforms[i] = first.transform;
            }
            applied++;
          }
          if (applied > 0) {
            setOpts((o) => ({
              ...o,
              transforms: { ...(o.transforms ?? {}), ...nextTransforms },
              transformParams: { ...(o.transformParams ?? {}), ...nextParams },
            }));
            publishEditorStatus({
              message: `已应用 ${applied} 条数据质量建议转换`,
              messageAt: Date.now(),
            });
          }
          setPendingQualitySuggestions(null);
        }
        setStep('map');
      })
      .catch((e: unknown) => { if (!cancelled) setError(toMsg(e)); })
      .finally(() => { if (!cancelled) setColsLoading(false); });
    return () => { cancelled = true; };
     
  }, [step, selSchema, selTable, connId, pendingQualitySuggestions]);

  // presetSnapshotsRefresh 仅作 bump counter，用于 savePreset/restoreSnapshot 后触发 re-render
  void presetSnapshotsRefresh;

  // Esc 关闭（避开 INPUT/TEXTAREA/SELECT）
  useEffect(() => {
    const h = (ev: KeyboardEvent) => {
      if (ev.key !== 'Escape') return;
      const t = ev.target as HTMLElement | null;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
      ev.preventDefault();
      onClose();
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [onClose]);

  // M30.140 备份预检 chip 快捷键：Alt+1/2/3 切类型过滤（全部/预设/快照），Alt+Q/W/E 切动作过滤（全部/将覆盖/新增）
  // M30.141 补充：Alt+S 选中当前 filter 命中项，Alt+Shift+S 反选当前 filter，Alt+/ 聚焦搜索框
  // 仅在 presetManager 打开且 backupPending 非空时生效（低频确认面板，不与 M30.80 Step 3 map 过滤器冲突）
  // 避开 INPUT/TEXTAREA/SELECT/contenteditable 防与输入控件冲突
  useEffect(() => {
    if (!presetManagerOpen || !backupPending) return;
    const h = (ev: KeyboardEvent) => {
      if (!ev.altKey || ev.ctrlKey || ev.metaKey) return;
      const t = ev.target as HTMLElement | null;
      const inInput = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable);
      const key = ev.key;
      // Alt+/ 聚焦搜索框（含 Shift 允许，因 '/' 键位置固定）
      if (key === '/' || key === '?') {
        if (inInput) return;
        ev.preventDefault();
        document.getElementById('backup-preview-search')?.focus();
        return;
      }
      // 其余快捷键在输入控件聚焦时不响应，防与输入冲突
      if (inInput) return;
      // Alt+S：选中/反选当前 filter 命中项
      if (key === 's' || key === 'S') {
        ev.preventDefault();
        const { items } = backupPending;
        const matchCur = items.filter((i) => {
          if (backupPreviewFilter !== 'all' && i.action !== backupPreviewFilter) return false;
          if (backupPreviewKindFilter !== 'all' && i.kind !== backupPreviewKindFilter) return false;
          if (backupPreviewSearch) {
            const s = backupPreviewSearch.toLowerCase();
            if (!i.schema.toLowerCase().includes(s) && !i.table.toLowerCase().includes(s)) return false;
          }
          return true;
        });
        const next = new Set(backupSelected);
        if (ev.shiftKey) {
          // 反选：命中项 toggle
          for (const i of matchCur) { if (next.has(i.key)) next.delete(i.key); else next.add(i.key); }
        } else {
          // 选中：命中项加入（保留其他已选）
          for (const i of matchCur) next.add(i.key);
        }
        setBackupSelected(next);
        return;
      }
      // 类型维度：Alt+1/2/3（无 shift）
      if (!ev.shiftKey) {
        if (key === '1') { ev.preventDefault(); setBackupPreviewKindFilter('all'); return; }
        if (key === '2') { ev.preventDefault(); setBackupPreviewKindFilter('preset'); return; }
        if (key === '3') { ev.preventDefault(); setBackupPreviewKindFilter('snapshot'); return; }
        const lower = key.toLowerCase();
        if (lower === 'q') { ev.preventDefault(); setBackupPreviewFilter('all'); return; }
        if (lower === 'w') { ev.preventDefault(); setBackupPreviewFilter('overwrite'); return; }
        if (lower === 'e') { ev.preventDefault(); setBackupPreviewFilter('add'); return; }
      }
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [presetManagerOpen, backupPending, backupPreviewFilter, backupPreviewKindFilter, backupPreviewSearch, backupSelected]);

  // M30.80 Alt+1..4 切换映射过滤器，Alt+0 清空搜索（仅 Step 3 生效，避开输入控件防 Monaco 冲突）
  useEffect(() => {
    if (step !== 'map') return;
    const h = (ev: KeyboardEvent) => {
      if (!ev.altKey || ev.ctrlKey || ev.metaKey) return;
      const t = ev.target as HTMLElement | null;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
      const key = ev.key;
      let nextFilter: MappingFilter | null = null;
      if (key === '1') nextFilter = 'all';
      else if (key === '2') nextFilter = 'unmapped';
      else if (key === '3') nextFilter = 'mapped';
      else if (key === '4') nextFilter = 'low-health';
      else if (key === '5') nextFilter = 'critical-health';
      else if (key === '0') {
        ev.preventDefault();
        setMappingSearch('');
        setMappingSearchHistOpen(false);
        setMappingSearchHistIdx(-1);
        return;
      } else if (key === 'r' || key === 'R') {
        // M30.86 一键重置视图：filter+sort+search 全部回到默认
        ev.preventDefault();
        persistMappingFilter('all');
        persistMappingSort('index');
        setMappingSearch('');
        setMappingSearchHistOpen(false);
        setMappingSearchHistIdx(-1);
        return;
      } else if (key === 'h' || key === 'H') {
        // M30.93 Alt+H 跳到首个未映射列（home/首位），Alt+L 跳到末位
        if (!parse || mappings.length === 0) return;
        const unmapped: number[] = [];
        for (let i = 0; i < mappings.length; i++) {
          if (mappings[i].targetColumn == null) unmapped.push(i);
        }
        if (unmapped.length === 0) return;
        ev.preventDefault();
        const next = unmapped[0];
        jumpCursorUnmapped.current = next;
        setBatchSel(new Set<number>([next]));
        setBatchAnchor(next);
        flashJump(next);
        document.querySelector<HTMLElement>(`[data-map-row-idx="${next}"]`)?.scrollIntoView({ block: 'center', behavior: 'smooth' });
        return;
      } else if (key === 'l' || key === 'L') {
        if (!parse || mappings.length === 0) return;
        const unmapped: number[] = [];
        for (let i = 0; i < mappings.length; i++) {
          if (mappings[i].targetColumn == null) unmapped.push(i);
        }
        if (unmapped.length === 0) return;
        ev.preventDefault();
        const next = unmapped[unmapped.length - 1];
        jumpCursorUnmapped.current = next;
        setBatchSel(new Set<number>([next]));
        setBatchAnchor(next);
        flashJump(next);
        document.querySelector<HTMLElement>(`[data-map-row-idx="${next}"]`)?.scrollIntoView({ block: 'center', behavior: 'smooth' });
        return;
      } else if (key === 'n' || key === 'N') {
        // M30.91 跳到下一个未映射列（targetColumn==null），循环遍历
        if (!parse || mappings.length === 0) return;
        const unmapped: number[] = [];
        for (let i = 0; i < mappings.length; i++) {
          if (mappings[i].targetColumn == null) unmapped.push(i);
        }
        if (unmapped.length === 0) return;
        ev.preventDefault();
        const start = jumpCursorUnmapped.current;
        let next: number;
        if (start < 0) {
          next = unmapped[0];
        } else {
          const pos = unmapped.indexOf(start);
          next = pos === -1 ? unmapped[0] : unmapped[(pos + 1) % unmapped.length];
        }
        jumpCursorUnmapped.current = next;
        setBatchSel(new Set<number>([next]));
        setBatchAnchor(next);
        flashJump(next);
        document.querySelector<HTMLElement>(`[data-map-row-idx="${next}"]`)?.scrollIntoView({ block: 'center', behavior: 'smooth' });
        return;
      } else if (key === 'p' || key === 'P') {
        // M30.91 跳到上一个未映射列
        if (!parse || mappings.length === 0) return;
        const unmapped: number[] = [];
        for (let i = 0; i < mappings.length; i++) {
          if (mappings[i].targetColumn == null) unmapped.push(i);
        }
        if (unmapped.length === 0) return;
        ev.preventDefault();
        const start = jumpCursorUnmapped.current;
        let next: number;
        if (start < 0) {
          next = unmapped[unmapped.length - 1];
        } else {
          const pos = unmapped.indexOf(start);
          if (pos <= 0) next = unmapped[unmapped.length - 1];
          else next = unmapped[pos - 1];
        }
        jumpCursorUnmapped.current = next;
        setBatchSel(new Set<number>([next]));
        setBatchAnchor(next);
        flashJump(next);
        document.querySelector<HTMLElement>(`[data-map-row-idx="${next}"]`)?.scrollIntoView({ block: 'center', behavior: 'smooth' });
        return;
      } else return;
      ev.preventDefault();
      persistMappingFilter(nextFilter);
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [step, flashJump, parse, mappings, persistMappingFilter, persistMappingSort]);

  // M30.94 Ctrl+Alt+1..4 键盘切向导步：与 M30.51 徽章点击同 canNav 前置数据守卫；双修饰键区分结构导航（Ctrl+Alt=步）与视图操作（Alt=过滤器/排序/搜索）；单 Alt+1..4 已被 M30.80 过滤器快捷键占用
  useEffect(() => {
    const h = (ev: KeyboardEvent) => {
      if (!ev.ctrlKey || !ev.altKey || ev.metaKey || ev.shiftKey) return;
      const t = ev.target as HTMLElement | null;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
      const key = ev.key;
      if (key !== '1' && key !== '2' && key !== '3' && key !== '4') return;
      ev.preventDefault();
      const target: Step = (['input', 'table', 'map', 'preview'] as Step[])[parseInt(key, 10) - 1];
      if (step === target) return;
      if (target === 'input') { setStep('input'); return; }
      if (!parse) return;
      if (target === 'table') { setStep('table'); return; }
      if (!selTable) return;
      if (target === 'map') { setStep('map'); return; }
      if (!mappings.some((m) => m.targetColumn)) return;
      setStep('preview');
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [step, parse, selTable, mappings]);

  // M30.95 Ctrl+Alt+[ / Ctrl+Alt+] 相对切步：与 M30.94 绝对定位（Ctrl+Alt+1..4）互补，支持键盘用户在不记住步号的情况下相对遍历；done 步不可前进（必须走执行导入）；复用 M30.51 canNav 前置数据守卫
  useEffect(() => {
    const h = (ev: KeyboardEvent) => {
      if (!ev.ctrlKey || !ev.altKey || ev.metaKey || ev.shiftKey) return;
      const t = ev.target as HTMLElement | null;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
      const key = ev.key;
      if (key !== '[' && key !== ']') return;
      ev.preventDefault();
      const cur = STEP_ORDER.indexOf(step);
      if (cur === -1) return;
      const next = key === '[' ? cur - 1 : cur + 1;
      if (next < 0 || next >= STEP_ORDER.length) return;
      if (STEP_ORDER[next] === 'done') return;
      if (key === '[') {
        setStep(STEP_ORDER[next]);
        return;
      }
      const target = STEP_ORDER[next];
      if (target === 'input') { setStep('input'); return; }
      if (!parse) return;
      if (target === 'table') { setStep('table'); return; }
      if (!selTable) return;
      if (target === 'map') { setStep('map'); return; }
      if (!mappings.some((m) => m.targetColumn)) return;
      setStep('preview');
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [step, parse, selTable, mappings]);

  // M30.89 Ctrl+J/K 跳到下一个/上一个 health<80 的行（循环，跨列顺序；用于宽表快速浏览异常列）
  useEffect(() => {
    if (step !== 'map') return;
    const h = (ev: KeyboardEvent) => {
      if (!ev.ctrlKey || ev.altKey || ev.metaKey || ev.shiftKey) return;
      const t = ev.target as HTMLElement | null;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
      const key = ev.key.toLowerCase();
      if (key !== 'j' && key !== 'k') return;
      if (!columnHealths || columnHealths.length === 0) return;
      // 收集所有 health<80 的索引（低健康行）
      const unhealthy: number[] = [];
      for (const ch of columnHealths) {
        if (ch.health < 80) unhealthy.push(ch.i);
      }
      if (unhealthy.length === 0) return;
      ev.preventDefault();
      // 从 cursor 位置出发；-1 视为起始（J 从头，K 从尾）
      const start = jumpCursor.current;
      let next: number;
      if (key === 'j') {
        const pos = start < 0 ? -1 : unhealthy.indexOf(start);
        if (pos === -1) {
          // cursor 不在 unhealthy 中，取第一个 > start 的（无则从头循环）
          const after = unhealthy.find((idx) => idx > start);
          next = after ?? unhealthy[0];
        } else {
          next = unhealthy[(pos + 1) % unhealthy.length];
        }
      } else {
        const pos = start < 0 ? unhealthy.length : unhealthy.indexOf(start);
        if (pos <= 0) {
          // pos=0 需要 wrap；start<0 时 pos=length-1 已是最后一个
          next = unhealthy[unhealthy.length - 1];
        } else {
          next = unhealthy[(pos - 1 + unhealthy.length) % unhealthy.length];
        }
      }
      jumpCursor.current = next;
      setBatchSel(new Set<number>([next]));
      setBatchAnchor(next);
      flashJump(next);
      // 滚动到视口
      const el = document.querySelector<HTMLElement>(`[data-map-row-idx="${next}"]`);
      el?.scrollIntoView({ block: 'center', behavior: 'smooth' });
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [step, columnHealths, flashJump]);

  // 卸载时若有挂起事务立即 rollback（防泄漏）
  useEffect(() => () => {
    if (txIdRef.current) {
      void api.rollbackTransaction(txIdRef.current).catch(() => {});
      txIdRef.current = null;
    }
  }, []);

  const handleParse = () => {
    if (!csvText.trim()) { setError('CSV 内容为空'); return; }
    if (inputFormat === 'sql') {
      const stmts = splitSql(csvText);
      if (stmts.length === 0) { setError('SQL 文件中未检测到任何语句'); return; }
      setStep('preview');
      return;
    }
    setStep('table');
  };

  const detectFormatFromFile = (name: string): ImportFormat => {
    const lower = name.toLowerCase();
    if (lower.endsWith('.sql')) return 'sql';
    if (lower.endsWith('.jsonl') || lower.endsWith('.ndjson') || lower.endsWith('.json')) return 'jsonl';
    return 'csv';
  };

  const loadQueueNext = async () => {
    if (queuePos >= fileQueue.length) return;
    const next = fileQueue[queuePos];
    await handleFile(next.file, next.format);
    setQueuePos(queuePos + 1);
  };

  const enqueueFiles = (files: File[]) => {
    if (files.length === 0) return;
    setFileQueue(files.map((f) => ({ file: f, format: detectFormatFromFile(f.name), name: f.name })));
    setQueuePos(0);
  };

  const handleFile = async (file: File, fmt?: ImportFormat) => {
    if (fmt) setInputFormat(fmt);
    setFileName(file.name);
    setDetectedEncoding(null);
    try {
      const buf = await file.arrayBuffer();
      rawBufRef.current = buf;
      const enc = encoding === 'auto' ? detectEncoding(buf).encoding : encoding;
      setDetectedEncoding(enc);
      setCsvText(decodeWithEncoding(buf, enc));
    } catch (e) {
      setError(toMsg(e));
    }
  };
  handleFileRef.current = handleFile;

  const redecodeWith = (enc: InputEncoding | 'auto') => {
    setEncoding(enc);
    const buf = rawBufRef.current;
    if (!buf) return;
    const actual = enc === 'auto' ? detectEncoding(buf).encoding : enc;
    setDetectedEncoding(actual);
    setCsvText(decodeWithEncoding(buf, actual));
  };

  const fillSample = () => {
    const s = sampleForKind(kind, inputFormat);
    setCsvText(s.text);
    if (inputFormat === 'csv' && kind === 'sqlite') setDelimiter(',');
  };

  const targetColNames = useMemo(() => mappings.filter((m) => m.targetColumn).map((m) => m.targetColumn!), [mappings]);
  const mappedCount = targetColNames.length;
  const skippedCount = mappings.length - mappedCount;

  const sqlStatements = useMemo<SqlStatement[]>(
    () => (inputFormat === 'sql' ? splitSql(csvText) : []),
    [inputFormat, csvText],
  );
  const sqlParamCounts = useMemo(
    () => sqlStatements.map((s) => countParams(s.sql)),
    [sqlStatements],
  );
  const sqlLintResult = useMemo(() => {
    if (inputFormat !== 'sql' || csvText.length > 500_000) return null;
    return lintSql(csvText, kind);
  }, [inputFormat, csvText, kind]);

  const csvStatements = useMemo(() => {
    if (!parse || mappedCount === 0) return [];
    const effectiveRows = overrides
      ? parse.rows.map((r, i) => overrides[i] ? overrides[i] : r)
      : parse.rows;
    return buildStatements(
      selSchema, selTable,
      mappings, effectiveRows, cols, opts,
    );
  }, [parse, mappings, cols, opts, selSchema, selTable, mappedCount, overrides]);

  const statements = useMemo(() => {
    if (inputFormat === 'sql') return sqlStatements;
    return csvStatements;
  }, [inputFormat, sqlStatements, csvStatements]);

  // M30.99 Step 4 Ctrl+Enter 触发开始导入：Ctrl+Enter = "提交当前作用域"（SQL 编辑器=跑 SQL、Step 4=跑导入）；
  // busy 与 statements.length===0 与「开始导入」按钮禁用条件严格对齐；INPUT/TEXTAREA/SELECT/contenteditable 一律放行避免与原生控件冲突。
  // runImport 通过 ref 指向最新闭包（每次 render 更新），deps 只关心"是否可触发"三个外部信号，避免每次 render 重新订阅 window keydown。
  const runImportRef = useRef<(() => Promise<void>) | null>(null);
  useEffect(() => { runImportRef.current = () => runImport(); });
  useEffect(() => {
    if (step !== 'preview') return;
    const h = (ev: KeyboardEvent) => {
      if (!ev.ctrlKey || ev.altKey || ev.metaKey || ev.shiftKey) return;
      if (ev.key !== 'Enter') return;
      const t = ev.target as HTMLElement | null;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
      if (busy || statements.length === 0) return;
      ev.preventDefault();
      void runImportRef.current?.();
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [step, busy, statements.length]);

  const previewSql = statements.length > 0 ? statements[0].sql : (targetColNames.length > 0
    ? buildInsertStatement(selSchema, selTable, targetColNames, targetColNames.map(() => '?').join(', '))
    : '');

  const validations = useMemo(() => opts.validations ?? {}, [opts.validations]);
  const validationResult = useMemo(() => {
    if (!parse || !hasAnyValidation(Object.values(validations)[0])) {
      return null;
    }
    return validateAllRows(parse.rows, validations);
  }, [parse, validations]);

  const checkPkExistence = async () => {
    if (inputFormat === 'sql') {
      setPkCheckResult({ found: 0, total: 0, ms: 0, error: 'SQL 模式不支持 PK 预检' });
      return;
    }
    if (opts.mode === 'insert') {
      setPkCheckResult({ found: 0, total: 0, ms: 0, error: 'Insert 模式不支持 PK 预检（无 PK 目标可查）' });
      return;
    }
    const pkCols = cols.filter((c) => c.is_primary_key).map((c) => c.name);
    if (pkCols.length === 0) {
      setPkCheckResult({ found: 0, total: 0, ms: 0, error: '目标表无主键，无法预检' });
      return;
    }
    if (csvStatements.length === 0) {
      setPkCheckResult({ found: 0, total: 0, ms: 0, error: '当前无待导入行' });
      return;
    }
    const activeIdx = mappings.map((m, i) => ({ m, i })).filter(({ m }) => m.targetColumn != null).map(({ i }) => i);
    const pkIdx = activeIdx.filter((i) => pkCols.includes(mappings[i].targetColumn!));
    if (pkIdx.length === 0) {
      setPkCheckResult({ found: 0, total: 0, ms: 0, error: '当前映射未包含任何 PK 列' });
      return;
    }
    setPkCheckBusy(true);
    setPkCheckResult(null);
    const t0 = Date.now();
    try {
      const keyRows: Value[][] = [];
      for (const s of csvStatements) {
        const params = s.params ?? [];
        const keyParams = pkIdx.map((i) => {
          const paramPos = activeIdx.indexOf(i);
          return params[paramPos] ?? null;
        });
        if (keyParams.some((p) => p === null || p === undefined)) continue;
        keyRows.push(keyParams);
      }
      if (keyRows.length === 0) {
        setPkCheckResult({ found: 0, total: 0, ms: Date.now() - t0, error: '所有行的 PK 值都为空，无法预检' });
        return;
      }
      let found = 0;
      const BATCH = 200;
      const placeholders = pkCols.map(() => '?').join(', ');
      for (let i = 0; i < keyRows.length; i += BATCH) {
        const slice = keyRows.slice(i, i + BATCH);
        const tuples = slice.map(() => `(${placeholders})`).join(' OR ');
        const params: Value[] = [];
        slice.forEach((row) => row.forEach((v) => params.push(v)));
        const sql = `SELECT COUNT(*) FROM ${qualTable(selSchema, selTable)} WHERE ${tuples}`;
        const r = await api.executeQuery(connId, { sql, params });
        const first = r.rows[0]?.[0];
        const n = typeof first === 'number' ? first : Number(first ?? 0) || 0;
        found += n;
      }
      setPkCheckResult({ found, total: keyRows.length, ms: Date.now() - t0, error: null });
    } catch (e) {
      setPkCheckResult({ found: 0, total: 0, ms: Date.now() - t0, error: `预检失败：${toMsg(e)}` });
    } finally {
      setPkCheckBusy(false);
    }
  };

  const checkFkValidity = async () => {
    if (inputFormat === 'sql') {
      setFkCheckResult({ valid: 0, invalid: 0, total: 0, ms: 0, error: 'SQL 模式不支持 FK 预检' });
      return;
    }
    if (opts.mode === 'insert') {
      setFkCheckResult({ valid: 0, invalid: 0, total: 0, ms: 0, error: 'Insert 模式不支持 FK 预检' });
      return;
    }
    if (csvStatements.length === 0) {
      setFkCheckResult({ valid: 0, invalid: 0, total: 0, ms: 0, error: '当前无待导入行' });
      return;
    }
    setFkCheckBusy(true);
    setFkCheckResult(null);
    const t0 = Date.now();
    try {
      const fks = await api.listForeignKeys(connId, selSchema, selTable);
      if (fks.length === 0) {
        setFkCheckResult({ valid: 0, invalid: 0, total: 0, ms: Date.now() - t0, error: '目标表无外键，无需预检' });
        return;
      }
      const activeIdx = mappings.map((m, i) => ({ m, i })).filter(({ m }) => m.targetColumn != null).map(({ i }) => i);
      let totalChecked = 0;
      let totalInvalid = 0;
      for (const fk of fks) {
        const fkMappedIdx: { csvIdx: number; activePos: number; refCol: string }[] = [];
        for (let j = 0; j < fk.columns.length; j++) {
          const col = fk.columns[j];
          const csvIdx = activeIdx.find((i) => mappings[i].targetColumn === col);
          if (csvIdx === undefined) continue;
          fkMappedIdx.push({ csvIdx: csvIdx, activePos: activeIdx.indexOf(csvIdx), refCol: fk.referenced_columns[j] });
        }
        if (fkMappedIdx.length !== fk.columns.length || fkMappedIdx.length === 0) continue;
        const refTable = qualTable(fk.referenced_schema || selSchema, fk.referenced_table);
        const refCols = fkMappedIdx.map((x) => quoteIdent(x.refCol));
        for (const s of csvStatements) {
          const params = s.params ?? [];
          const keyParams = fkMappedIdx.map((x) => params[x.activePos] ?? null);
          if (keyParams.some((p) => p === null || p === undefined)) continue;
          totalChecked++;
          const sql = `SELECT COUNT(*) FROM ${refTable} WHERE (${refCols.join(', ')}) IN ((${fkMappedIdx.map(() => '?').join(', ')}))`;
          const r = await api.executeQuery(connId, { sql, params: keyParams });
          const first = r.rows[0]?.[0];
          const n = typeof first === 'number' ? first : Number(first ?? 0) || 0;
          if (n === 0) totalInvalid++;
        }
      }
      setFkCheckResult({ valid: totalChecked - totalInvalid, invalid: totalInvalid, total: totalChecked, ms: Date.now() - t0, error: null });
    } catch (e) {
      setFkCheckResult({ valid: 0, invalid: 0, total: 0, ms: Date.now() - t0, error: `预检失败：${toMsg(e)}` });
    } finally {
      setFkCheckBusy(false);
    }
  };

  const checkDuplicatePk = () => {
    if (inputFormat === 'sql') {
      setDupPkResult({ duplicates: [], totalKeys: 0, dupKeys: 0, ms: 0, error: 'SQL 模式不支持内部 PK 重复检测' });
      return;
    }
    const pkCols = cols.filter((c) => c.is_primary_key).map((c) => c.name);
    if (pkCols.length === 0) {
      setDupPkResult({ duplicates: [], totalKeys: 0, dupKeys: 0, ms: 0, error: '目标表无主键，无需检测' });
      return;
    }
    if (csvStatements.length === 0) {
      setDupPkResult({ duplicates: [], totalKeys: 0, dupKeys: 0, ms: 0, error: '当前无待导入行' });
      return;
    }
    const activeIdx = mappings.map((m, i) => ({ m, i })).filter(({ m }) => m.targetColumn != null).map(({ i }) => i);
    const pkIdx = activeIdx.filter((i) => pkCols.includes(mappings[i].targetColumn!));
    if (pkIdx.length === 0) {
      setDupPkResult({ duplicates: [], totalKeys: 0, dupKeys: 0, ms: 0, error: '当前映射未包含任何 PK 列' });
      return;
    }
    setDupPkCheckBusy(true);
    setDupPkResult(null);
    const t0 = Date.now();
    try {
      // 基于最终参数（转换 + null policy 后）分组，null key 跳过
      const groups = new Map<string, number[]>(); // key -> csvRow[]
      let totalKeys = 0;
      for (const s of csvStatements) {
        const params = s.params ?? [];
        const keyParts = pkIdx.map((i) => {
          const pos = activeIdx.indexOf(i);
          const v = params[pos];
          if (v === null || v === undefined) return null;
          return String(v);
        });
        if (keyParts.some((k) => k === null)) continue;
        const key = keyParts.join('\u001F');
        totalKeys++;
        const arr = groups.get(key);
        if (arr) arr.push(s.origIdx + 1);
        else groups.set(key, [s.origIdx + 1]);
      }
      const duplicates: { csvRow: number; key: string }[] = [];
      let dupKeys = 0;
      for (const [key, rows] of groups) {
        if (rows.length > 1) {
          dupKeys++;
          for (const r of rows) duplicates.push({ csvRow: r, key });
        }
      }
      duplicates.sort((a, b) => a.csvRow - b.csvRow);
      setDupPkResult({ duplicates, totalKeys, dupKeys, ms: Date.now() - t0, error: null });
    } catch (e) {
      setDupPkResult({ duplicates: [], totalKeys: 0, dupKeys: 0, ms: Date.now() - t0, error: `检测失败：${toMsg(e)}` });
    } finally {
      setDupPkCheckBusy(false);
    }
  };

  const applyOneClickOptimize = (): number => {
    if (!parse || profiles.length === 0 || cols.length === 0) return 0;
    const isNumericDtype = (dt: string) => /int|serial|smallint|mediumint|bigint|tinyint|float|double|decimal|numeric|real|number\b/i.test(dt);
    let transformApplied = 0;
    let nullPolicyApplied = 0;
    let skipRowApplied = 0;
    let useDefaultApplied = 0;
    let requiredApplied = 0;
    let regexApplied = 0;
    let enumApplied = 0;
    const nextTransforms: Record<number, ColumnTransform> = { ...(opts.transforms ?? {}) };
    const nextTransformParams: Record<number, Record<string, string>> = { ...(opts.transformParams ?? {}) };
    const nextNullPolicies: Record<number, NullPolicy> = { ...(opts.nullPolicies ?? {}) };
    const nextValidations: Record<number, ColumnValidation> = { ...(opts.validations ?? {}) };
    const EMAIL_RE = String.raw`^[^\s@]+@[^\s@]+\.[^\s@]+$`;
    const UUID_RE = String.raw`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`;
    const URL_RE = String.raw`^https?://[^\s]+$`;
    const DATE_RE = String.raw`^\d{4}-\d{2}-\d{2}`;

    for (let i = 0; i < mappings.length; i++) {
      const m = mappings[i];
      if (!m?.targetColumn) continue;
      const p = profiles[i];
      const tc = colInfoMap.get(m.targetColumn);
      if (!p || !tc) continue;

      // 1. 转换
      const qc = qualityReport?.columns[i];
      const suggs = qc?.suggestions ?? [];
      if (suggs.length > 0) {
        const pick = suggs.find((s) => s.transform === 'regex-replace') ?? suggs[0];
        if (pick.transform === 'regex-replace') {
          nextTransforms[i] = 'regex-replace';
          nextTransformParams[i] = {
            ...(nextTransformParams[i] ?? {}),
            pattern: pick.pattern,
            replacement: pick.replacement,
            flags: pick.flags,
          };
        } else {
          nextTransforms[i] = pick.transform;
        }
        transformApplied++;
      }

      // 2. 空值策略
      if (p.nullCount > 0 && !tc.nullable) {
        const dv = tc.default_value;
        if (dv != null && dv !== '') {
          nextNullPolicies[i] = 'use-default';
          useDefaultApplied++;
        } else if (isNumericDtype(tc.data_type)) {
          nextNullPolicies[i] = 'skip-row';
          skipRowApplied++;
        } else {
          nextNullPolicies[i] = 'empty-string';
          nullPolicyApplied++;
        }
      }

      // 3. 校验建议
      const cur = nextValidations[i] ?? EMPTY_VALIDATION;
      const nv: ColumnValidation = { ...cur };
      let changed = false;
      // required：目标 NOT NULL 且当前策略不是 skip-row / null-if-empty，才推 required
      const currentTransform = nextTransforms[i];
      const currentNullPolicy = nextNullPolicies[i] ?? 'null';
      const nullSafe = currentTransform !== 'null-if-empty'
        && currentNullPolicy !== 'skip-row'
        && currentNullPolicy !== 'empty-string';
      if (!tc.nullable && nullSafe && !nv.required && p.nullCount > 0) {
        nv.required = true;
        requiredApplied++;
        changed = true;
      }
      // regex：按列名/类型匹配
      if (!nv.regex) {
        const dn = m.csvName.toLowerCase();
        const dtype = tc.data_type.toLowerCase();
        if (/(^|[^a-z])(email|mail)([^a-z]|$)/.test(dn)) {
          nv.regex = EMAIL_RE; regexApplied++; changed = true;
        } else if (/(^|[^a-z])(uuid|guid|guid)([^a-z]|$)/.test(dn) || /uuid/.test(dtype)) {
          nv.regex = UUID_RE; regexApplied++; changed = true;
        } else if (/(^|[^a-z])(url|link|href)([^a-z]|$)/.test(dn)) {
          nv.regex = URL_RE; regexApplied++; changed = true;
        } else if (/(^|[^a-z])(date|created_at|updated_at|birthday|dob)([^a-z]|$)/.test(dn) && !/email|link|uuid/.test(dn)) {
          nv.regex = DATE_RE; regexApplied++; changed = true;
        }
      }
      // enum：低基数（≤20 唯一值 且 <5% 或全部唯一值 ≤ 20）
      if (!nv.enum && p.total > 0 && p.total > p.nullCount) {
        const uniq = new Set<string>();
        for (const s of p.samples) if (s !== '') uniq.add(s);
        const isStringType = /char|text|varchar|nchar|nvarchar|enum|clob|nvarchar|string/i.test(tc.data_type);
        if (isStringType && uniq.size > 0 && uniq.size <= 20 && uniq.size / Math.max(1, p.total - p.nullCount) < 0.05) {
          nv.enum = Array.from(uniq).join(',');
          enumApplied++;
          changed = true;
        }
      }
      if (changed) {
        nextValidations[i] = nv;
      }
    }

    // 稀疏化
    const sparseTransforms: Record<number, ColumnTransform> = {};
    for (const [k, v] of Object.entries(nextTransforms)) if (v !== 'none') sparseTransforms[Number(k)] = v;
    const sparseNullPolicies: Record<number, NullPolicy> = {};
    for (const [k, v] of Object.entries(nextNullPolicies)) if (v !== 'null') sparseNullPolicies[Number(k)] = v;
    const sparseValidations: Record<number, ColumnValidation> = {};
    for (const [k, v] of Object.entries(nextValidations)) if (hasAnyValidation(v)) sparseValidations[Number(k)] = v;

    setOpts({
      ...opts,
      transforms: sparseTransforms,
      transformParams: nextTransformParams,
      nullPolicies: sparseNullPolicies,
      validations: sparseValidations,
    });
    const total = transformApplied + useDefaultApplied + skipRowApplied + nullPolicyApplied
      + requiredApplied + regexApplied + enumApplied;
    publishEditorStatus({
      message: `一键优化：转换 ${transformApplied} · 空值策略 ${useDefaultApplied + skipRowApplied + nullPolicyApplied} · 校验 required ${requiredApplied} / regex ${regexApplied} / enum ${enumApplied}`,
      messageAt: Date.now(),
    });
    if (total === 0) {
      setError('一键优化：当前映射已最优，无需调整');
    }
    return total;
  };

  const runImport = async () => {
    if (statements.length === 0) { setError('没有可导入的行'); return; }
    setFailCategoryFilter(null);
    setExpandedFailRow(null);
    setUndoStack([]);
    setRedoStack([]);
    setDiffOpen(false);
    if (qualityGateBlocked && !qualityGateOverrideRef.current) {
      setError(`质量门禁拦截：评分 ${qualityReport?.overallScore}/100 低于阈值 ${qualityGate.threshold}（可点击下方「强制继续」覆盖）`);
      return;
    }
    cancelRef.current = false;
    setBusy(true);
    setError(null);
    setFailedRows([]);
    const isSql = inputFormat === 'sql';
    type AnyStatement = { sql: string; params?: Value[]; origIdx: number };
    const all: AnyStatement[] = isSql
      ? sqlStatements.map((s, i) => ({ sql: s.sql, origIdx: i + 1 }))
      : csvStatements.map((s) => ({ sql: s.sql, params: s.params, origIdx: s.origIdx }));
    const effectiveStatements = retryIndices ? all.filter((_, i) => retryIndices.has(i)) : all;
    const _preBatches = chunk(effectiveStatements, opts.batchSize);
    const _batchMs: number[] = [];
    setProgress({ done: 0, total: effectiveStatements.length, ms: 0, batch: 0, batchTotal: _preBatches.length, batchDone: 0, batchSize: opts.batchSize, batchMs: [] });
    const t0 = Date.now();
    let txId = '';
    const failedRowsBuf: FailedRow[] = [];
    let cancelled = false;
    let totalAffected = 0;
    let running = 0;
    try {
      const tx = await api.beginTransaction(connId, 'read_committed');
      txId = tx.id;
      txIdRef.current = txId;
      const failCount = () => failedRowsBuf.length;
      const batches = _preBatches;
      let flatIdx = 0;
      for (let bi = 0; bi < batches.length; bi++) {
        if (cancelRef.current) { cancelled = true; break; }
        const batch = batches[bi];
        const batchStart = Date.now();
        // 进入新批次时立即刷新 batch 子进度，让用户看到跨批次切换
        if (bi > 0) {
          setProgress({ done: running, total: effectiveStatements.length, ms: Date.now() - t0, batch: bi + 1, batchTotal: batches.length, batchDone: 0, batchSize: opts.batchSize, batchMs: [..._batchMs] });
        }
        for (let k = 0; k < batch.length; k++) {
          if (cancelRef.current) { cancelled = true; break; }
          const s = batch[k];
          const idx = flatIdx++;
          running++;
          try {
            const r = await api.executeInTx(txId, {
              sql: s.sql,
              params: s.params && s.params.length > 0 ? s.params : undefined,
            });
            if (r.affected_rows > 0) totalAffected += r.affected_rows;
            else if (!isSql) {
              failedRowsBuf.push({ csvRow: s.origIdx, reason: 'affected_rows=0', preview: previewParams(s.params ?? []), stmtIdx: idx });
            }
          } catch (e) {
            failedRowsBuf.push({
              csvRow: s.origIdx,
              reason: toMsg(e),
              preview: isSql ? s.sql.slice(0, 80) : previewParams(s.params ?? []),
              stmtIdx: idx,
            });
          }
          if (failedRowsBuf.length % 25 === 0 || running === effectiveStatements.length) {
            setFailedRows([...failedRowsBuf]);
          }
          setProgress({ done: running, total: effectiveStatements.length, ms: Date.now() - t0, batch: bi + 1, batchTotal: batches.length, batchDone: k + 1, batchSize: batch.length, batchMs: [..._batchMs] });
          if (opts.skipFailed) continue;
          if (!opts.skipFailed && failCount() > 0) break;
        }
        // 记录本批耗时（>=0，含被中断的批）
        _batchMs.push(Date.now() - batchStart);
        if (!opts.skipFailed && failCount() > 0) break;
        await new Promise((r) => setTimeout(r, 0));
      }
      setFailedRows([...failedRowsBuf]);

      if (cancelled) {
        try { await api.rollbackTransaction(txId); } catch { /* ignore */ }
        txIdRef.current = null;
        setError(`用户取消：已回滚已执行 ${running} 条，剩余 ${effectiveStatements.length - running} 条未执行`);
        setProgress({ done: running, total: effectiveStatements.length, ms: Date.now() - t0, batch: _preBatches.length, batchTotal: _preBatches.length, batchDone: opts.batchSize, batchSize: opts.batchSize, batchMs: [..._batchMs] });
        addHistory({
          connId, kind, schema: selSchema, table: isSql ? `<SQL>` : selTable,
          fileName, mode: isSql ? 'insert' : opts.mode,
          totalRows: effectiveStatements.length,
          insertedRows: 0, failedRows: failedRowsBuf.length,
          skippedRows: effectiveStatements.length - running,
          ms: Date.now() - t0, status: 'cancelled',
          failedRowsDetail: failedRowsBuf.map((r) => ({ csvRow: r.csvRow, reason: r.reason, preview: r.preview })),
        });
        setStep('preview');
        return;
      }

      if (failCount() > 0 && !opts.skipFailed) {
        try { await api.rollbackTransaction(txId); } catch { /* ignore */ }
        txIdRef.current = null;
        setError(`导入失败：${failCount()} 条出错，已回滚（可开启「跳过失败行」重跑）`);
        setProgress({ done: effectiveStatements.length, total: effectiveStatements.length, ms: Date.now() - t0, batch: _preBatches.length, batchTotal: _preBatches.length, batchDone: opts.batchSize, batchSize: opts.batchSize, batchMs: [..._batchMs] });
        addHistory({
          connId, kind, schema: selSchema, table: isSql ? `<SQL>` : selTable,
          fileName, mode: isSql ? 'insert' : opts.mode,
          totalRows: effectiveStatements.length,
          insertedRows: 0, failedRows: failedRowsBuf.length, skippedRows: 0,
          ms: Date.now() - t0, status: 'failed',
          failedRowsDetail: failedRowsBuf.map((r) => ({ csvRow: r.csvRow, reason: r.reason, preview: r.preview })),
        });
        setStep('preview');
        return;
      }
      await api.commitTransaction(txId);
      txIdRef.current = null;
      setInsertedCount(totalAffected);
      setProgress({ done: effectiveStatements.length, total: effectiveStatements.length, ms: Date.now() - t0, batch: _preBatches.length, batchTotal: _preBatches.length, batchDone: opts.batchSize, batchSize: opts.batchSize, batchMs: [..._batchMs] });
      if (retryIndices) setRetryIndices(null);

      if (!isSql && selTable) {
        savePreset(buildPreset(
          connId, selSchema, selTable, kind,
          opts.mode, mappings, opts.transforms ?? {},
          {
            emptyAsNull: opts.emptyAsNull,
            batchSize: opts.batchSize,
            skipFailed: opts.skipFailed,
            filterColumn: opts.filterColumn,
            filterOp: opts.filterOp,
            filterValue: opts.filterValue,
            validations: opts.validations ?? {},
            strictValidation: opts.strictValidation,
            nullPolicies: opts.nullPolicies ?? {},
          },
          opts.transformParams ?? {},
        ));
        setPresetSnapshotsRefresh((n) => n + 1);
      }

      addHistory({
        connId, kind, schema: selSchema, table: isSql ? `<SQL>` : selTable,
        fileName, mode: isSql ? 'insert' : opts.mode,
        totalRows: effectiveStatements.length,
        insertedRows: totalAffected, failedRows: failedRowsBuf.length,
        skippedRows: effectiveStatements.length - totalAffected,
        ms: Date.now() - t0,
        status: failCount() > 0 ? 'partial' : 'success',
        failedRowsDetail: failedRowsBuf.length > 0
          ? failedRowsBuf.map((r) => ({ csvRow: r.csvRow, reason: r.reason, preview: r.preview }))
          : undefined,
      });

      // 编辑器状态栏反馈：摘要含耗时/跳过数/主导失败类（M30.46）
      {
        const elapsedMs = Date.now() - t0;
        const failTop = (() => {
          const fc = failedRowsBuf.length;
          if (fc === 0) return '';
          // 用 buildFailureSummary 同款分类：reason.slice(0, 40) group，取 count 最高的一条
          const groups = new Map<string, number>();
          for (const r of failedRowsBuf) {
            const k = r.reason.slice(0, 40);
            groups.set(k, (groups.get(k) ?? 0) + 1);
          }
          let topK = '', topN = 0;
          for (const [k, n] of groups) if (n > topN) { topK = k; topN = n; }
          if (!topK) return '';
          const meta = failCategoryMeta(classifyFailReason(topK).key);
          return ` · 主导 ${meta.icon} ${topN} 行`;
        })();
        const totalStr = isSql ? `${effectiveStatements.length} 条` : `${totalAffected} 行`;
        const skippedStr = failCount() > 0
          ? (isSql ? `（跳过 ${failCount()} 失败）` : `（跳过 ${failCount()} 失败行）`)
          : '';
        publishEditorStatus({
          message: isSql
            ? `✅ 已执行 ${totalStr} · ${elapsedMs} ms${failTop}`
            : `✅ 已导入 ${totalStr}${skippedStr} · ${elapsedMs} ms${failTop}`,
          messageAt: Date.now(),
        });
      }
      setStep('done');
      if (!isSql && autoPreviewOnSuccess && totalAffected > 0) {
        setTimeout(() => previewImported({ auto: true, lastInserted: totalAffected }), 250);
      }
    } catch (e) {
      if (txId) {
        try { await api.rollbackTransaction(txId); } catch { /* ignore */ }
      }
      txIdRef.current = null;
      setError(toMsg(e));
      addHistory({
        connId, kind, schema: selSchema, table: isSql ? `<SQL>` : selTable,
        fileName, mode: isSql ? 'insert' : opts.mode,
        totalRows: effectiveStatements.length,
        insertedRows: 0, failedRows: effectiveStatements.length,
        skippedRows: 0, ms: Date.now() - t0, status: 'failed',
      });
      setStep('preview');
    } finally {
      setBusy(false);
    }
  };

  const handleCancelImport = () => {
    cancelRef.current = true;
    setBusy(true);
  };

  const downloadReport = (format: 'csv' | 'json') => {
    const rows = filteredFailedRows;
    if (rows.length === 0) {
      const body = format === 'json' ? JSON.stringify({
        table: inputFormat === 'sql' ? '<SQL>' : `${selSchema}.${selTable}`,
        mode: inputFormat === 'sql' ? 'sql' : opts.mode,
        total: progress?.total ?? 0,
        inserted: insertedCount,
        failed: 0,
        ms: progress?.ms ?? 0,
        categoryFilter: failCategoryFilter ?? null,
        csvRows: [],
      }, null, 2) : '';
      const blob = new Blob([body], { type: format === 'csv' ? 'text/csv' : 'application/json' });
      triggerDownload(blob, `import_report_${selTable || 'empty'}${failCategoryFilter ? `_${failCategoryFilter}` : ''}.${format}`);
      return;
    }
    let body: string;
    if (format === 'json') {
      body = JSON.stringify({
        table: inputFormat === 'sql' ? '<SQL>' : `${selSchema}.${selTable}`,
        mode: inputFormat === 'sql' ? 'sql' : opts.mode,
        total: progress?.total ?? 0,
        inserted: insertedCount,
        failed: rows.length,
        ms: progress?.ms ?? 0,
        categoryFilter: failCategoryFilter ?? null,
        categories: failureSummary.map((s) => ({ key: s.key, label: s.label, icon: failCategoryMeta(s.key).icon, count: s.count, hint: s.hint })),
        csvRows: rows.map((r) => {
          const cls = classifyFailReason(r.reason);
          return {
            csvRow: r.csvRow,
            category: cls.key,
            categoryLabel: cls.label,
            categoryIcon: failCategoryMeta(cls.key).icon,
            fixHint: cls.hint,
            reason: r.reason,
            preview: r.preview,
          };
        }),
      }, null, 2);
    } else {
      const esc = (s: string) => `"${s.replace(/"/g, '""')}"`;
      const header = 'csvRow,category,categoryLabel,fixHint,reason,preview';
      const rowsOut = rows.map((r) => {
        const cls = classifyFailReason(r.reason);
        return `${r.csvRow},${cls.key},${esc(cls.label)},${esc(cls.hint)},${esc(r.reason)},${esc(r.preview)}`;
      });
      body = [header, ...rowsOut].join('\n');
    }
    const mime = format === 'csv' ? 'text/csv' : 'application/json';
    const blob = new Blob([body], { type: mime });
    const catSuffix = failCategoryFilter ? `_${failCategoryFilter}` : '';
    triggerDownload(blob, `import_report_${selTable}${catSuffix}.${format}`);
  };

  const triggerDownload = (blob: Blob, filename: string) => {
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  const downloadFailedRowsCsv = (h: ImportHistoryEntry) => {
    if (!h.failedRowsDetail || h.failedRowsDetail.length === 0) return;
    const esc = (s: string) => `"${s.replace(/"/g, '""')}"`;
    const header = 'csvRow,reason,preview';
    const rows = h.failedRowsDetail.map((r) => `${r.csvRow},${esc(r.reason)},${esc(r.preview)}`);
    const body = [header, ...rows].join('\n');
    const blob = new Blob([body], { type: 'text/csv' });
    const safeTable = (h.table || 'empty').replace(/[^\w-]/g, '_');
    triggerDownload(blob, `import_failed_${safeTable}_${h.id.slice(0, 8)}.csv`);
  };

  const exportHistory = (format: 'csv' | 'json') => {
    if (history.length === 0) return;
    const ts = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    if (format === 'json') {
      const blob = new Blob([JSON.stringify(history, null, 2)], { type: 'application/json' });
      triggerDownload(blob, `import_history_${ts}.json`);
    } else {
      const esc = (s: string) => `"${String(s).replace(/"/g, '""')}"`;
      const header = 'at,kind,schema,table,fileName,mode,totalRows,insertedRows,failedRows,skippedRows,ms,status';
      const rows = history.map((h) => [
        new Date(h.at).toISOString(), h.kind ?? '', esc(h.schema), esc(h.table),
        esc(h.fileName ?? ''), h.mode, h.totalRows, h.insertedRows, h.failedRows,
        h.skippedRows, h.ms, h.status,
      ].join(','));
      const body = [header, ...rows].join('\n');
      const blob = new Blob([body], { type: 'text/csv' });
      triggerDownload(blob, `import_history_${ts}.csv`);
    }
  };
  const pushFix = (entry: FixEntry) => {
    setUndoStack((s) => {
      const next = [entry, ...s];
      return next.length > MAX_FIX_HISTORY ? next.slice(0, MAX_FIX_HISTORY) : next;
    });
    setRedoStack([]);
    setDiffOpen(true);
  };

  const applyCategoryFix = (key: string): { ok: boolean; msg: string } => {
    if (inputFormat === 'sql') return { ok: false, msg: 'SQL 模式不支持一键修复' };
    const snapshot = () => JSON.parse(JSON.stringify(opts)) as ImportOptions;
    const meta = failCategoryMeta(key);
    switch (key) {
      case 'dup': {
        if (opts.mode === 'upsert') return { ok: false, msg: '已经是 Upsert 模式，请检查 PK 映射或先手动清理目标表' };
        if (opts.mode === 'update') return { ok: false, msg: 'Update 模式对冲突不重试，请切 Insert 或 Upsert' };
        const prev = snapshot();
        setOpts({ ...opts, mode: 'upsert' });
        const msg = '已切到 Upsert 模式，目标表已有行将自动更新';
        pushFix({ key, label: meta.label, icon: meta.icon, snapshot: prev, msg, at: Date.now() });
        return { ok: true, msg };
      }
      case 'type': {
        const prev = snapshot();
        const total = applyOneClickOptimize();
        if (total === 0) return { ok: false, msg: '质量报告无自动转换建议，请手动调整（Step 3）' };
        const msg = '已按质量报告应用转换建议（trim / date-parse / scale 等）';
        pushFix({ key, label: meta.label, icon: meta.icon, snapshot: prev, msg, at: Date.now() });
        return { ok: true, msg };
      }
      case 'validation': {
        if (!opts.strictValidation) return { ok: false, msg: '严格校验已关闭，请检查自定义校验规则是否需要放宽（Step 3）' };
        const prev = snapshot();
        setOpts({ ...opts, strictValidation: false });
        const msg = '已关闭严格校验，违规行将允许导入（Step 3 可微调正则/enum）';
        pushFix({ key, label: meta.label, icon: meta.icon, snapshot: prev, msg, at: Date.now() });
        return { ok: true, msg };
      }
      case 'not-null':
        return { ok: false, msg: '请打开 Step 3 为目标 NOT NULL 列设置空值策略（use-default / empty-string / skip-row）' };
      case 'fk':
        return { ok: false, msg: '请先导入父表，或检查引用值是否正确（CSV 值 vs 父表实际值）' };
      case 'no-affected':
        return { ok: false, msg: 'Update/Upsert 未命中任何行：请检查 PK 条件是否正确（Step 3 PK 映射）' };
      default:
        return { ok: false, msg: '未知类别，请展开详情查看原始错误 message' };
    }
  };
  const colLabel = (idx: number): string => {
    const m = mappings[idx];
    const csvCol = m?.csvName ?? `#${idx}`;
    const tgt = m?.targetColumn ? ` → ${m.targetColumn}` : '';
    return `${csvCol}${tgt}`;
  };
  const recDiffItems = (
    label: string,
    aMap: Record<number, unknown>,
    bMap: Record<number, unknown>,
    fmt: (v: unknown) => string,
  ): { field: string; before: string; after: string }[] => {
    const out: { field: string; before: string; after: string }[] = [];
    const keys = new Set<number>([...Object.keys(aMap).map(Number), ...Object.keys(bMap).map(Number)]);
    for (const k of Array.from(keys).sort((x, y) => x - y)) {
      const av = aMap[k] === undefined ? null : aMap[k];
      const bv = bMap[k] === undefined ? null : bMap[k];
      if (JSON.stringify(av) !== JSON.stringify(bv)) out.push({ field: `${label} ${colLabel(k)}`, before: fmt(av), after: fmt(bv) });
    }
    return out;
  };
  const buildUndoDiff = (a: ImportOptions, b: ImportOptions): { field: string; before: string; after: string }[] => {
    const items: { field: string; before: string; after: string }[] = [];
    if (a.mode !== b.mode) items.push({ field: '模式 mode', before: a.mode, after: b.mode });
    if (a.strictValidation !== b.strictValidation) items.push({ field: '严格校验', before: boolLabel(a.strictValidation), after: boolLabel(b.strictValidation) });
    if (a.emptyAsNull !== b.emptyAsNull) items.push({ field: '空串视为 null', before: boolLabel(a.emptyAsNull), after: boolLabel(b.emptyAsNull) });
    if (a.batchSize !== b.batchSize) items.push({ field: '批大小', before: String(a.batchSize), after: String(b.batchSize) });
    items.push(...recDiffItems('转换 transform', a.transforms ?? {}, b.transforms ?? {}, (v) => v === null ? '∅' : String(v)));
    items.push(...recDiffItems('空值策略 nullPolicy', a.nullPolicies ?? {}, b.nullPolicies ?? {}, (v) => v === null ? 'null（默认）' : String(v)));
    items.push(...recDiffItems('校验 validation', a.validations ?? {}, b.validations ?? {}, (v) => {
      if (v === null) return '∅';
      const vv = v as ColumnValidation;
      const parts: string[] = [];
      if (vv.required) parts.push('必填');
      if (vv.regex) parts.push(`regex:${vv.regex.slice(0, 24)}`);
      if (vv.minValue) parts.push(`≥${vv.minValue}`);
      if (vv.maxValue) parts.push(`≤${vv.maxValue}`);
      if (vv.enum) parts.push(`enum:[${vv.enum.slice(0, 40)}]`);
      return parts.length > 0 ? parts.join(' · ') : '空';
    }));
    if (JSON.stringify(a.filterColumn) !== JSON.stringify(b.filterColumn) ||
        JSON.stringify(a.filterOp) !== JSON.stringify(b.filterOp) ||
        JSON.stringify(a.filterValue) !== JSON.stringify(b.filterValue)) {
      const fa = a.filterColumn === null ? '∅' : `列#${a.filterColumn} ${a.filterOp} "${a.filterValue}"`;
      const fb = b.filterColumn === null ? '∅' : `列#${b.filterColumn} ${b.filterOp} "${b.filterValue}"`;
      items.push({ field: '行过滤', before: fa, after: fb });
    }
    return items;
  };

  // 分组策略：field 首 token 作为 groupKey
  // 「转换 transform ...」→「转换」；「空值策略 nullPolicy ...」→「空值策略」；「校验 validation ...」→「校验」
  // 其他 field 直接以自身为 groupKey（模式/严格校验/空串视为 null/批大小/行过滤）
  const classifyDiffGroup = (field: string): string => {
    const spaceIdx = field.indexOf(' ');
    const head = spaceIdx > 0 ? field.slice(0, spaceIdx) : field;
    return head;
  };

  // 每组分色：按语义分配到色卡（未识别分组 fallback 到 muted）
  const diffGroupColor = (g: string): string => DIFF_GROUP_COLOR[g] ?? '#6b7280';

  // 栈可视条按 fix 字段变化密度着色：density=1 淡（rgba 0.30），density>=4 饱和（rgba 0.95）
  // undo 段用红色 rgba(220,38,38,α)，redo 段用蓝色 rgba(59,130,246,α)
  const densityAlpha = (n: number): number => {
    if (n <= 0) return 0.30;
    if (n >= 4) return 0.95;
    return 0.30 + (n - 1) * 0.22;
  };

  // 分组：保留首次出现顺序
  const groupDiffItems = (items: { field: string; before: string; after: string }[]):
    { group: string; items: { field: string; before: string; after: string }[] }[] => {
    const map = new Map<string, { field: string; before: string; after: string }[]>();
    const order: string[] = [];
    for (const it of items) {
      const g = classifyDiffGroup(it.field);
      if (!map.has(g)) { map.set(g, []); order.push(g); }
      map.get(g)!.push(it);
    }
    return order.map((g) => ({ group: g, items: map.get(g)! }));
  };

  // 搜索过滤：按 field/before/after 子串（默认不区分大小写；diffSearchCaseSensitive=true 时精确匹配）
  // 支持多关键字 OR 匹配：空白或 `|` 分隔，任一命中即保留（M30.32）
  // 支持作用域过滤（M30.33）：diffSearchScope='all|field|before|after'，非 all 时仅匹配对应字段
  // 副作用：命中分组的折叠态会被自动展开（下一次渲染时用户能看到匹配内容）
  const filterDiffGroups = (groups: { group: string; items: { field: string; before: string; after: string }[] }[]):
    { group: string; items: { field: string; before: string; after: string }[] }[] => {
    const qRaw = diffSearch.trim();
    if (!qRaw) return groups;
    const kws = splitDiffSearchKeywords(qRaw).map((k) => diffSearchCaseSensitive ? k : k.toLowerCase());
    if (kws.length === 0) return groups;
    const norm = (s: string) => diffSearchCaseSensitive ? s : s.toLowerCase();
    const matchItem = (it: { field: string; before: string; after: string }): boolean => {
      for (const k of kws) {
        if (diffSearchScope === 'field' && norm(it.field).includes(k)) return true;
        if (diffSearchScope === 'before' && norm(it.before).includes(k)) return true;
        if (diffSearchScope === 'after' && norm(it.after).includes(k)) return true;
        if (diffSearchScope === 'all' && (norm(it.field).includes(k) || norm(it.before).includes(k) || norm(it.after).includes(k))) return true;
      }
      return false;
    };
    const out: { group: string; items: { field: string; before: string; after: string }[] }[] = [];
    for (const g of groups) {
      const matchedItems = g.items.filter(matchItem);
      if (matchedItems.length > 0) out.push({ group: g.group, items: matchedItems });
    }
    return out;
  };

  // 命中时自动展开分组：只在搜索非空时生效，避免污染用户手动折叠的偏好
  useEffect(() => {
    if (!diffSearch.trim()) return;
    if (undoStack.length === 0) return;
    const actualN = Math.min(batchStepN, undoStack.length);
    const targetSnap = undoStack[actualN - 1].snapshot;
    const groups = groupDiffItems(buildUndoDiff(opts, targetSnap));
    const hitGroups = filterDiffGroups(groups).map((g) => g.group);
    if (hitGroups.length === 0) return;
    setCollapsedGroups((prev) => {
      const next = new Set(prev);
      let changed = false;
      for (const g of hitGroups) {
        if (next.has(g)) { next.delete(g); changed = true; }
      }
      return changed ? next : prev;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [diffSearch, diffSearchCaseSensitive, diffSearchScope]);

  // 构建当前 diff 面板可见 items 的扁平列表（受 filterDiffGroups + collapsedGroups 影响）
  // 供 Enter/Shift+Enter 循环命中 + 光标滚动到视口 + 键盘命中光标使用
  const getVisibleDiffItems = () => {
    if (undoStack.length === 0) return [];
    const actualN = Math.min(batchStepN, undoStack.length);
    const targetSnap = undoStack[actualN - 1].snapshot;
    const filtered = filterDiffGroups(groupDiffItems(buildUndoDiff(opts, targetSnap)));
    return filtered
      .filter((g) => !collapsedGroups.has(g.group))
      .flatMap((g) => g.items.map((it) => ({ group: g.group, item: it })));
  };

  // Enter/Shift+Enter 循环命中：delta=1 下一个，delta=-1 上一个
  // 仅在 diffSearch 非空时激活，与 diffCursor（分组光标，Alt+↑/↓）区分
  // 滚动到视口由 useEffect([diffHitCursor]) 统一处理
  const navDiffHit = (delta: 1 | -1) => {
    if (!diffSearch.trim()) return;
    const visible = getVisibleDiffItems();
    if (visible.length === 0) { setDiffHitCursor(-1); return; }
    const cur = diffHitCursor;
    const next = cur < 0
      ? (delta > 0 ? 0 : visible.length - 1)
      : (cur + delta + visible.length) % visible.length;
    setDiffHitCursor(next);
  };

  // diffSearch 变为空 → 清 hit 光标；搜索有命中但光标越界（折叠/过滤变化）→ clamp 到有效范围
  // 不做自动 scrollIntoView（导航 Enter/Shift+Enter 时才滚动，避免与用户手动折叠冲突）
  useEffect(() => {
    if (!diffSearch.trim()) {
      if (diffHitCursor !== -1) setDiffHitCursor(-1);
      return;
    }
    const visible = getVisibleDiffItems();
    if (visible.length === 0) {
      if (diffHitCursor !== -1) setDiffHitCursor(-1);
      return;
    }
    if (diffHitCursor < 0 || diffHitCursor >= visible.length) {
      setDiffHitCursor(Math.max(0, Math.min(diffHitCursor, visible.length - 1)));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [diffSearch, undoStack.length, batchStepN, collapsedGroups]);

  // 命中光标变化时把目标行滚到面板视口：首次搜索自动定位 + Enter/Shift+Enter 导航共用
  useEffect(() => {
    if (!diffSearch.trim() || diffHitCursor < 0) return;
    const visible = getVisibleDiffItems();
    if (diffHitCursor >= visible.length) return;
    const target = visible[diffHitCursor];
    requestAnimationFrame(() => {
      const el = diffItemRefs.current.get(`${target.group}\u0000${target.item.field}`);
      el?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [diffHitCursor]);

  const copyDiffSummary = async (items: { field: string; before: string; after: string }[], title?: string) => {
    const lines = [
      `# polydb 撤销预览 diff${title ? ' — ' + title : ''}`,
      `> 方向：当前（✗） → 撤销后（✓）`,
      `> 共 ${items.length} 项变化`,
      ``,
      ...items.map((it) => `- **${it.field}**: ${it.before} → ${it.after}`),
    ];
    const text = lines.join('\n');
    try {
      await navigator.clipboard.writeText(text);
      publishEditorStatus({ message: `📋 已复制撤销 diff${title ? '（' + title + '）' : ''}（${items.length} 项变化，${text.length} 字符）`, messageAt: Date.now() });
    } catch {
      publishEditorStatus({ message: `撤销 diff 已生成（${text.length} 字符），但 clipboard 权限拒绝：请手动复制`, messageAt: Date.now() });
    }
  };

  // 按分组复制：分组标题作为二级标题，组内 items 缩进
  const copyAllItems = async (items: { field: string; before: string; after: string }[]) => {
    const groups = groupDiffItems(items);
    const lines: string[] = [
      '# polydb 撤销预览 diff（按分组）',
      `> 方向：当前（✗） → 撤销后（✓）`,
      `> 共 ${items.length} 项变化 / ${groups.length} 个分组`,
      ``,
    ];
    for (const g of groups) {
      lines.push(`## ${g.group} (${g.items.length})`);
      lines.push('');
      for (const it of g.items) lines.push(`- ${it.field}: ${it.before} → ${it.after}`);
      lines.push('');
    }
    const text = lines.join('\n');
    try {
      await navigator.clipboard.writeText(text);
      publishEditorStatus({ message: `📋 已按分组复制 diff（${items.length} 项 / ${groups.length} 组，${text.length} 字符）`, messageAt: Date.now() });
    } catch {
      publishEditorStatus({ message: `分组 diff 已生成（${text.length} 字符），但 clipboard 权限拒绝：请手动复制`, messageAt: Date.now() });
    }
  };

  // 导出所有已撤销修复的完整时间线到 Markdown 文件
  // 语义：redoStack[i].snapshot 是"应用 fix[i] 前"的状态，redoStack[i+1].snapshot 是"应用 fix[i] 后"的状态
  // 所以 buildUndoDiff(redoStack[i].snapshot, redoStack[i+1].snapshot) 得到该 fix 的字段变化
  // 最后一条（最后一项）的 snapshot = redoStack[N-1].snapshot，作为"最终状态"单独输出，不与下一条配对
  type TimelineFormat = 'md' | 'json' | 'csv';
  const exportUndoTimeline = async (format: TimelineFormat = 'md') => {
    if (redoStack.length === 0) {
      publishEditorStatus({ message: '无可导出的修复历史', messageAt: Date.now() });
      return;
    }
    if (undoStack.length > 0) {
      // 只有"全撤销"态 redoStack[i].snapshot 才对应"应用 fix[i] 前"的连续时间线
      // 若用户已重做部分，栈结构被打断，导出会错乱；此场景走单条 diff 复制即可
      publishEditorStatus({ message: `仅支持全撤销态导出（当前仍剩 ${undoStack.length} 项未撤销），请全撤销或改用复制 diff`, messageAt: Date.now() });
      return;
    }
    const now = new Date();
    const ts = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}_${String(now.getHours()).padStart(2, '0')}${String(now.getMinutes()).padStart(2, '0')}${String(now.getSeconds()).padStart(2, '0')}`;
    const ordered = [...redoStack].reverse();
    // 提取每条 fix 的结构化 diff（所有格式共享）
    const entries = ordered.map((fix, i) => {
      const hasNext = i + 1 < ordered.length;
      const beforeSnap = fix.snapshot;
      const afterSnap = hasNext ? ordered[i + 1].snapshot : null;
      const diffItems = afterSnap ? buildUndoDiff(beforeSnap, afterSnap) : [];
      return {
        index: i + 1,
        total: ordered.length,
        icon: fix.icon,
        label: fix.label,
        msg: fix.msg,
        key: fix.key,
        at: new Date(fix.at).toISOString(),
        atLocal: new Date(fix.at).toLocaleString('zh-CN'),
        diffCount: diffItems.length,
        hasAfter: !!afterSnap,
        diff: diffItems,
      };
    });

    let text: string;
    let filename: string;
    let mime: string;

    if (format === 'md') {
      const lines: string[] = [
        '# polydb 修复时间线',
        '',
        `> 导出时间：${now.toLocaleString('zh-CN')}`,
        `> 撤销项数：${redoStack.length}（已全部撤销，配置已恢复）`,
        `> 方向：每条修复展示「应用前 ✗ → 应用后 ✓」`,
        '',
        '---',
      ];
      for (const e of entries) {
        lines.push('');
        lines.push(`## 修复 #${e.index}/${e.total} — ${e.icon} ${e.label}`);
        lines.push('');
        lines.push(`- 应用时间：${e.atLocal}`);
        lines.push(`- 说明：${e.msg}`);
        if (!e.hasAfter) {
          lines.push('- 字段变化：不可得（下一条 fix 不存在，此修复应用后状态已被后续撤销覆盖）');
          lines.push('- 提示：此修复在时间线末端，若要看到其完整影响，请重做后再导出');
          lines.push('');
          lines.push('---');
          continue;
        }
        lines.push(`- 字段变化：${e.diffCount} 项`);
        lines.push('');
        if (e.diffCount === 0) {
          lines.push('（此修复与相邻状态完全一致，撤销无实际效果）');
        } else {
          lines.push('| 字段 | 应用前（✗） | 应用后（✓） |');
          lines.push('|------|------------|------------|');
          for (const it of e.diff) {
            const b = it.before.replace(/\|/g, '\\|').replace(/\n/g, ' ');
            const a = it.after.replace(/\|/g, '\\|').replace(/\n/g, ' ');
            lines.push(`| ${it.field.replace(/\|/g, '\\|')} | ${b} | ${a} |`);
          }
        }
        lines.push('');
        lines.push('---');
      }
      lines.push('');
      lines.push(`> 共 ${entries.length} 条修复，导出时间 ${now.toLocaleString('zh-CN')}`);
      text = lines.join('\n');
      filename = `polydb-undo-timeline-${ts}.md`;
      mime = 'text/markdown;charset=utf-8';
    } else if (format === 'json') {
      text = JSON.stringify({
        exportedAt: now.toISOString(),
        total: entries.length,
        note: '方向：每条 fix 的 diff 是「应用前(beforeSnap) → 应用后(afterSnap)」；hasAfter=false 表示最后一条 fix 应用后状态已被后续撤销覆盖',
        entries,
      }, null, 2);
      filename = `polydb-undo-timeline-${ts}.json`;
      mime = 'application/json;charset=utf-8';
    } else {
      // CSV: 一行一个 (fix, field) diff
      const esc = (s: string | number | boolean) => {
        const t = String(s ?? '');
        if (/[",\n\r]/.test(t)) return `"${t.replace(/"/g, '""')}"`;
        return t;
      };
      const rows: string[] = [];
      rows.push(['index', 'total', 'icon', 'label', 'msg', 'key', 'at', 'hasAfter', 'field', 'before', 'after'].join(','));
      for (const e of entries) {
        if (e.diffCount === 0 && !e.hasAfter) {
          rows.push([e.index, e.total, e.icon, e.label, e.msg, e.key, e.at, false, '(不可得)', '', ''].map(esc).join(','));
        } else if (e.diffCount === 0) {
          rows.push([e.index, e.total, e.icon, e.label, e.msg, e.key, e.at, true, '(无变化)', '', ''].map(esc).join(','));
        } else {
          for (const it of e.diff) {
            rows.push([e.index, e.total, e.icon, e.label, e.msg, e.key, e.at, true, it.field, it.before, it.after].map(esc).join(','));
          }
        }
      }
      text = rows.join('\n');
      filename = `polydb-undo-timeline-${ts}.csv`;
      mime = 'text/csv;charset=utf-8';
    }

    const blob = new Blob([text], { type: mime });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    publishEditorStatus({ message: `📄 已导出修复时间线（${format.toUpperCase()}，${entries.length} 条，${text.length} 字符）`, messageAt: Date.now() });
  };

  // 批量撤销 N 步：一次性移动 N 项到 redoStack，最终 opts = 第 N 项的 snapshot
  // 与循环调用 undoFix 等价但状态更新只发生一次，避免中间态 render 抖动
  const undoFixN = (n: number) => {
    const total = undoStack.length;
    if (n <= 0 || total === 0) return;
    const actual = Math.min(n, total);
    const undone = undoStack.slice(0, actual);
    const rest = undoStack.slice(actual);
    // 保持 redoStack 时间序：最旧在前（redo 一次取栈顶 = 最近一次撤销）
    const moved: FixEntry[] = [...undone].reverse();
    setUndoStack(rest);
    setRedoStack((r) => [...moved, ...r].slice(0, MAX_FIX_HISTORY));
    setOpts(undone[actual - 1].snapshot);
    publishEditorStatus({
      message: `↩️ 已撤销 ${actual} 项修复（最新：「${undone[0].label}」${undone[0].msg}）`,
      messageAt: Date.now(),
    });
  };

  // 单项撤销：把 diff 面板中某个字段从"before"（当前 opts 值）恢复到"after"（target snapshot 值）
  // 生成合成 FixEntry 压入栈，供后续 Ctrl+Z 撤销这次撤销
  // 支持：mode / strictValidation / emptyAsNull / batchSize / transforms / nullPolicies / validations / 行过滤
  // 不支持（fallback）：validation 的"非空"目标（正则/enum 因显示截断不可逆），返回 null 由调用方提示用户改用全撤销
  const extractColIdx = (subLabel: string): number | null => {
    const hashIdx = subLabel.indexOf('#');
    if (hashIdx >= 0) {
      const numStr = subLabel.slice(hashIdx + 1).split(/\s/)[0];
      const n = parseInt(numStr, 10);
      if (!isNaN(n)) return n;
    }
    const csvName = subLabel.split(' → ')[0].trim();
    const m = mappings.find((mm) => mm.csvName === csvName);
    return m ? mappings.indexOf(m) : null;
  };
  const isDisplayEmptyStr = (s: string) => s === '' || DIFF_EMPTY_MARKS.has(s);

  // 单项撤销可行性预判（不 clone opts，用于 diff 面板灰化 ↩ 按钮）
  const canRevertSingleItem = (
    item: { field: string; before: string; after: string },
    targetSnap: ImportOptions,
  ): boolean => {
    const spaceIdx = item.field.indexOf(' ');
    const head = spaceIdx > 0 ? item.field.slice(0, spaceIdx) : item.field;
    const sub = spaceIdx > 0 ? item.field.slice(spaceIdx + 1) : '';
    switch (head) {
      case '模式':
        return ['insert', 'update', 'upsert'].includes(targetSnap.mode);
      case '严格校验': case '空串视为 null': case '行过滤':
        return true;
      case '批大小':
        return Number.isFinite(targetSnap.batchSize);
      case '转换': case '空值策略': {
        const idx = extractColIdx(sub);
        if (idx === null) return false;
        if (isDisplayEmptyStr(item.after)) return !isDisplayEmptyStr(item.before);
        return true;
      }
      case '校验': {
        const idx = extractColIdx(sub);
        if (idx === null) return false;
        if (isDisplayEmptyStr(item.after)) return !isDisplayEmptyStr(item.before);
        const tv = targetSnap.validations?.[idx];
        if (tv) return hasAnyValidation(tv);
        const after = item.after;
        const geIdx = after.indexOf('≥');
        const leIdx = after.indexOf('≤');
        const enumIdx = after.indexOf('enum:[');
        return after.includes('必填') || geIdx >= 0 || leIdx >= 0 || enumIdx >= 0;
      }
      default:
        return false;
    }
  };

  const revertSingleItem = (
    item: { field: string; before: string; after: string },
    targetSnap: ImportOptions,
  ): ImportOptions | null => {
    const next = JSON.parse(JSON.stringify(opts)) as ImportOptions;
    const spaceIdx = item.field.indexOf(' ');
    const head = spaceIdx > 0 ? item.field.slice(0, spaceIdx) : item.field;
    const sub = spaceIdx > 0 ? item.field.slice(spaceIdx + 1) : '';
    const isDisplayEmpty = isDisplayEmptyStr;
    switch (head) {
      case '模式': {
        if (!['insert', 'update', 'upsert'].includes(targetSnap.mode)) return null;
        next.mode = targetSnap.mode;
        return next;
      }
      case '严格校验': { next.strictValidation = targetSnap.strictValidation; return next; }
      case '空串视为 null': { next.emptyAsNull = targetSnap.emptyAsNull; return next; }
      case '批大小': {
        if (!Number.isFinite(targetSnap.batchSize)) return null;
        next.batchSize = targetSnap.batchSize;
        return next;
      }
      case '转换': {
        const idx = extractColIdx(sub);
        if (idx === null) return null;
        const map = { ...(next.transforms ?? {}) };
        if (isDisplayEmpty(item.after)) {
          // 目标是"空"：删除当前值（若当前非空）
          if (!isDisplayEmpty(item.before)) delete map[idx];
        } else {
          // 目标是非空：写入 target 快照里的值（显示字符串即真实值）
          map[idx] = targetSnap.transforms?.[idx] ?? item.after;
        }
        next.transforms = map;
        return next;
      }
      case '空值策略': {
        const idx = extractColIdx(sub);
        if (idx === null) return null;
        const map = { ...(next.nullPolicies ?? {}) };
        if (isDisplayEmpty(item.after)) {
          if (!isDisplayEmpty(item.before)) delete map[idx];
        } else {
          map[idx] = targetSnap.nullPolicies?.[idx] ?? item.after;
        }
        next.nullPolicies = map;
        return next;
      }
      case '校验': {
        const idx = extractColIdx(sub);
        if (idx === null) return null;
        const map = { ...(next.validations ?? {}) };
        if (isDisplayEmpty(item.after)) {
          // 目标是"空/删除"：只要当前非空就删
          if (!isDisplayEmpty(item.before)) delete map[idx];
        } else {
          // 目标是非空：从 target 快照取真实值；若快照缺失则从 after 显示字符串 fallback 解析
          const tv = targetSnap.validations?.[idx];
          if (tv) {
            map[idx] = JSON.parse(JSON.stringify(tv));
          } else {
            const after = item.after;
            const v: ColumnValidation = { required: after.includes('必填'), regex: '', minValue: '', maxValue: '', enum: '' };
            const geIdx = after.indexOf('≥');
            if (geIdx >= 0) v.minValue = after.slice(geIdx + 1).split(' · ')[0];
            const leIdx = after.indexOf('≤');
            if (leIdx >= 0) v.maxValue = after.slice(leIdx + 1).split(' · ')[0];
            const enumIdx = after.indexOf('enum:[');
            if (enumIdx >= 0) {
              const end = after.indexOf(']', enumIdx);
              const raw = end > 0 ? after.slice(enumIdx + 6, end) : after.slice(enumIdx + 6);
              const trimmed = raw.endsWith('...]') ? raw.slice(0, -3) : raw;
              v.enum = trimmed;
            }
            if (!hasAnyValidation(v)) return null;
            map[idx] = v;
          }
        }
        next.validations = map;
        return next;
      }
      case '行过滤': {
        if (targetSnap.filterColumn === null) {
          next.filterColumn = null;
          next.filterValue = '';
        } else {
          next.filterColumn = targetSnap.filterColumn;
          next.filterOp = targetSnap.filterOp;
          next.filterValue = targetSnap.filterValue;
        }
        return next;
      }
      default:
        return null;
    }
  };

  // 执行单项撤销：把当前 opts 中该字段与目标 snapshot 的差异恢复，生成合成 FixEntry 压栈
  const applySingleItemUndo = (
    item: { field: string; before: string; after: string },
    targetSnap: ImportOptions,
  ) => {
    const next = revertSingleItem(item, targetSnap);
    if (!next) {
      publishEditorStatus({
        message: `↩️ 无法单项撤销「${item.field}」（复杂字段回退需全撤销）`,
        messageAt: Date.now(),
      });
      return;
    }
    if (JSON.stringify(next) === JSON.stringify(opts)) {
      publishEditorStatus({ message: `↩️ 「${item.field}」已是目标值，无需撤销`, messageAt: Date.now() });
      return;
    }
    const prev = JSON.parse(JSON.stringify(opts)) as ImportOptions;
    setOpts(next);
    const msg = `${item.before || '∅'} → ${item.after || '∅'}`;
    pushFix({ key: 'single-item', label: `单项撤销：${item.field}`, icon: '↩️', snapshot: prev, msg, at: Date.now() });
  };

  // 批量重做 N 步：redoStack 前 N 项移回 undoStack，最终 opts = 第 N 项的 snapshot
  // 每步用"当时的 opts"作为该条在 undoStack 的 snapshot（供后续 undo 恢复中间态）
  const redoFixN = (n: number) => {
    const total = redoStack.length;
    if (n <= 0 || total === 0) return;
    const actual = Math.min(n, total);
    const moved = redoStack.slice(0, actual);
    const rest = redoStack.slice(actual);
    const newUndo: FixEntry[] = [];
    let currentSnap = JSON.parse(JSON.stringify(opts)) as ImportOptions;
    for (const e of moved) {
      newUndo.push({ ...e, snapshot: currentSnap, at: Date.now() });
      currentSnap = e.snapshot;
    }
    setUndoStack((u) => [...newUndo, ...u].slice(0, MAX_FIX_HISTORY));
    setRedoStack(rest);
    setOpts(currentSnap);
    publishEditorStatus({
      message: `↪️ 已重做 ${actual} 项修复（最新：「${moved[0].label}」${moved[0].msg}）`,
      messageAt: Date.now(),
    });
  };

  // Ctrl+Z / Ctrl+Shift+Z / Ctrl+Y 单步 + Alt+Z / Alt+Shift+Z 全部批量（避开 INPUT/TEXTAREA/SELECT/contenteditable）
  // Alt+D 切换 diff 面板，Alt+C 折叠/展开所有 diff 分组（避开浏览器书签 Ctrl+D / 打印 Ctrl+P / 复制 Ctrl+C）
  useEffect(() => {
    const h = (ev: KeyboardEvent) => {
      const t = ev.target as HTMLElement | null;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
      const mod = ev.ctrlKey || ev.metaKey;
      const k = ev.key.toLowerCase();
      if (ev.altKey && k === 'd') {
        ev.preventDefault();
        if (undoStack.length > 0) setDiffOpen((v) => !v);
        return;
      }
      if (ev.altKey && !ev.shiftKey && k === 'c') {
        ev.preventDefault();
        if (undoStack.length === 0) return;
        const actualN = Math.min(batchStepN, undoStack.length);
        const targetSnap = undoStack[actualN - 1].snapshot;
        const groups = groupDiffItems(buildUndoDiff(opts, targetSnap));
        const allCollapsed = groups.length > 0 && groups.every((g) => collapsedGroups.has(g.group));
        setCollapsedGroups(allCollapsed ? new Set() : new Set(groups.map((g) => g.group)));
        return;
      }
      // Alt+↑ / Alt+↓ 在 diff 面板分组间移动光标；Alt+Enter 或 Alt+Space 切换当前光标分组折叠
      // 需要 diff 面板已打开且 undoStack 非空
      if (ev.altKey && !ev.shiftKey && diffOpen && undoStack.length > 0 && (k === 'arrowup' || k === 'arrowdown' || k === 'enter' || k === ' ')) {
        ev.preventDefault();
        const actualN = Math.min(batchStepN, undoStack.length);
        const targetSnap = undoStack[actualN - 1].snapshot;
        const filtered = filterDiffGroups(groupDiffItems(buildUndoDiff(opts, targetSnap)));
        if (filtered.length === 0) return;
        if (k === 'arrowup' || k === 'arrowdown') {
          const delta = k === 'arrowdown' ? 1 : -1;
          setDiffCursor((cur) => {
            if (cur < 0) return delta > 0 ? 0 : filtered.length - 1;
            return (cur + delta + filtered.length) % filtered.length;
          });
          return;
        }
        if ((k === 'enter' || k === ' ') && diffCursor >= 0 && diffCursor < filtered.length) {
          const g = filtered[diffCursor].group;
          setCollapsedGroups((prev) => {
            const next = new Set(prev);
            if (next.has(g)) next.delete(g);
            else next.add(g);
            return next;
          });
        }
        return;
      }
      // Ctrl+F 在 diff 面板已打开时聚焦搜索框（避开浏览器原生 Find in Page；diffOpen 才触发，否则释放给浏览器）
      if (mod && !ev.shiftKey && !ev.altKey && k === 'f' && diffOpen && undoStack.length > 0) {
        ev.preventDefault();
        diffSearchInputRef.current?.focus();
        diffSearchInputRef.current?.select();
        return;
      }
      // Shift+Alt+C 在 diff 面板已打开时切换搜索大小写敏感（Alt+C 已用于折叠/展开全部分组）
      if (ev.altKey && ev.shiftKey && !mod && k === 'c' && diffOpen && undoStack.length > 0) {
        ev.preventDefault();
        setDiffSearchCaseSensitive((v) => !v);
        return;
      }
      // Alt+S 循环切换搜索作用域：all → field → before → after（M30.33）
      if (ev.altKey && !ev.shiftKey && !mod && k === 's' && diffOpen && undoStack.length > 0) {
        ev.preventDefault();
        setDiffSearchScope((s) => {
          const order: DiffSearchScope[] = ['all', 'field', 'before', 'after'];
          return order[(order.indexOf(s) + 1) % order.length];
        });
        return;
      }
      if (ev.altKey && k === 'z') {
        ev.preventDefault();
        if (!ev.shiftKey) undoFixN(undoStack.length);
        else redoFixN(redoStack.length);
        return;
      }
      if (!mod) return;
      if (k === 'z' && !ev.shiftKey) { ev.preventDefault(); undoFixN(batchStepN); }
      else if (k === 'z' && ev.shiftKey) { ev.preventDefault(); redoFixN(batchStepN); }
      else if (k === 'y') { ev.preventDefault(); redoFixN(batchStepN); }
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [undoStack, redoStack, opts, batchStepN, collapsedGroups]);

  // 命令面板注册：撤销/重做/查看 diff（ImportModal 打开期间可用，关闭时自动 unregister）
  useEffect(() => {
    const unregs: (() => void)[] = [];
    unregs.push(registerCommand({
      id: 'import.undo-batch',
      label: `导入：撤销修复 ×${batchStepN}`,
      category: '导入',
      hotkey: 'Ctrl+Z',
      keywords: ['import', 'undo', '撤销', '修复', '批量'],
      enabled: () => undoStack.length > 0,
      run: () => undoFixN(batchStepN),
    }));
    unregs.push(registerCommand({
      id: 'import.undo-all',
      label: `导入：全部撤销 (${undoStack.length})`,
      category: '导入',
      hotkey: 'Alt+Z',
      keywords: ['import', 'undo', '撤销', '全部', '清空'],
      enabled: () => undoStack.length > 0,
      run: () => undoFixN(undoStack.length),
    }));
    unregs.push(registerCommand({
      id: 'import.redo-batch',
      label: `导入：重做撤销 ×${batchStepN}`,
      category: '导入',
      hotkey: 'Ctrl+Shift+Z',
      keywords: ['import', 'redo', '重做', '恢复'],
      enabled: () => redoStack.length > 0,
      run: () => redoFixN(batchStepN),
    }));
    unregs.push(registerCommand({
      id: 'import.redo-all',
      label: `导入：全部重做 (${redoStack.length})`,
      category: '导入',
      hotkey: 'Alt+Shift+Z',
      keywords: ['import', 'redo', '重做', '全部', '恢复'],
      enabled: () => redoStack.length > 0,
      run: () => redoFixN(redoStack.length),
    }));
    unregs.push(registerCommand({
      id: 'import.diff-toggle',
      label: '导入：切换撤销 diff 预览',
      category: '导入',
      hotkey: 'Alt+D',
      keywords: ['import', 'diff', '预览', '变化', '撤销'],
      enabled: () => undoStack.length > 0,
      run: () => setDiffOpen((v) => !v),
    }));
    unregs.push(registerCommand({
      id: 'import.diff-copy',
      label: '导入：复制撤销 diff',
      category: '导入',
      keywords: ['import', 'diff', '复制', 'copy', 'markdown'],
      enabled: () => undoStack.length > 0,
      run: () => {
        const n = Math.min(batchStepN, undoStack.length);
        void copyDiffSummary(buildUndoDiff(opts, undoStack[n - 1].snapshot));
      },
    }));
    unregs.push(registerCommand({
      id: 'import.diff-copy-grouped',
      label: '导入：按分组复制撤销 diff',
      category: '导入',
      keywords: ['import', 'diff', '复制', 'copy', 'group', '分组', 'markdown'],
      enabled: () => undoStack.length > 0,
      run: () => {
        const n = Math.min(batchStepN, undoStack.length);
        void copyAllItems(buildUndoDiff(opts, undoStack[n - 1].snapshot));
      },
    }));
    unregs.push(registerCommand({
      id: 'import.batch-step-up',
      label: '导入：批量步数 +1',
      category: '导入',
      keywords: ['import', 'step', 'batch', '步数'],
      run: () => setBatchStepN((n) => n + 1),
    }));
    unregs.push(registerCommand({
      id: 'import.batch-step-down',
      label: '导入：批量步数 -1',
      category: '导入',
      keywords: ['import', 'step', 'batch', '步数'],
      enabled: () => batchStepN > 1,
      run: () => setBatchStepN((n) => Math.max(1, n - 1)),
    }));
    unregs.push(registerCommand({
      id: 'import.batch-step-reset',
      label: '导入：批量步数重置为 1',
      category: '导入',
      keywords: ['import', 'step', 'batch', 'reset', '重置'],
      enabled: () => batchStepN !== 1,
      run: () => setBatchStepN(1),
    }));
    unregs.push(registerCommand({
      id: 'import.shortcuts-help',
      label: '导入：快捷键速查（Ctrl+/）',
      category: '导入',
      hotkey: 'Ctrl+/',
      keywords: ['import', 'shortcut', 'keymap', '快捷键', '帮助', 'help', 'ctrl+/'],
      run: () => setHelpPanelOpen((v) => !v),
    }));
    unregs.push(registerCommand({
      id: 'import.diff-groups-collapse-all',
      label: '导入：折叠所有 diff 分组',
      category: '导入',
      hotkey: 'Alt+C',
      keywords: ['import', 'diff', 'group', '折叠', 'collapse', '分组'],
      enabled: () => undoStack.length > 0,
      run: () => {
        const actualN = Math.min(batchStepN, undoStack.length);
        const targetSnap = undoStack[actualN - 1].snapshot;
        const groups = groupDiffItems(buildUndoDiff(opts, targetSnap));
        setCollapsedGroups(new Set(groups.map((g) => g.group)));
      },
    }));
    unregs.push(registerCommand({
      id: 'import.diff-groups-expand-all',
      label: '导入：展开所有 diff 分组',
      category: '导入',
      hotkey: 'Alt+C',
      keywords: ['import', 'diff', 'group', '展开', 'expand', '分组'],
      enabled: () => collapsedGroups.size > 0,
      run: () => setCollapsedGroups(new Set()),
    }));
    unregs.push(registerCommand({
      id: 'import.undo-timeline-export',
      label: `导入：导出修复时间线 MD (${redoStack.length})`,
      category: '导入',
      keywords: ['import', 'undo', 'timeline', '时间线', '导出', 'markdown', 'md'],
      enabled: () => redoStack.length > 0 && undoStack.length === 0,
      run: () => void exportUndoTimeline('md'),
    }));
    unregs.push(registerCommand({
      id: 'import.undo-timeline-export-json',
      label: `导入：导出修复时间线 JSON (${redoStack.length})`,
      category: '导入',
      keywords: ['import', 'undo', 'timeline', '时间线', '导出', 'json'],
      enabled: () => redoStack.length > 0 && undoStack.length === 0,
      run: () => void exportUndoTimeline('json'),
    }));
    unregs.push(registerCommand({
      id: 'import.undo-timeline-export-csv',
      label: `导入：导出修复时间线 CSV (${redoStack.length})`,
      category: '导入',
      keywords: ['import', 'undo', 'timeline', '时间线', '导出', 'csv'],
      enabled: () => redoStack.length > 0 && undoStack.length === 0,
      run: () => void exportUndoTimeline('csv'),
    }));
    unregs.push(registerCommand({
      id: 'import.diff-groups-clear-preference',
      label: '导入：清空分组折叠偏好',
      category: '导入',
      keywords: ['import', 'diff', 'group', 'clear', '清空', '偏好', 'localStorage'],
      enabled: () => collapsedGroups.size > 0,
      run: () => {
        try { localStorage.removeItem('polydb.diffCollapsedGroups.v1'); } catch { /* ignore */ }
        setCollapsedGroups(new Set());
      },
    }));
    unregs.push(registerCommand({
      id: 'import.diff-search-focus',
      label: '导入：聚焦撤销 diff 搜索框',
      category: '导入',
      hotkey: 'Ctrl+F',
      keywords: ['import', 'diff', 'search', '搜索', 'focus', '查找'],
      enabled: () => undoStack.length > 0,
      run: () => {
        setDiffOpen(true);
        setTimeout(() => {
          diffSearchInputRef.current?.focus();
          diffSearchInputRef.current?.select();
        }, 0);
      },
    }));
    unregs.push(registerCommand({
      id: 'import.diff-search-clear',
      label: '导入：清除撤销 diff 搜索',
      category: '导入',
      keywords: ['import', 'diff', 'search', 'clear', '清除', '搜索'],
      enabled: () => diffSearch.length > 0,
      run: () => { setDiffSearch(''); setDiffCursor(-1); },
    }));
    const computeFilteredGroupCount = () => {
      if (undoStack.length === 0) return 0;
      const actualN = Math.min(batchStepN, undoStack.length);
      const targetSnap = undoStack[actualN - 1].snapshot;
      return filterDiffGroups(groupDiffItems(buildUndoDiff(opts, targetSnap))).length;
    };
    unregs.push(registerCommand({
      id: 'import.diff-cursor-down',
      label: '导入：diff 光标下移',
      category: '导入',
      hotkey: 'Alt+↓',
      keywords: ['import', 'diff', 'cursor', 'nav', '键盘', '下移', 'down'],
      enabled: () => undoStack.length > 0,
      run: () => {
        const n = computeFilteredGroupCount();
        if (n === 0) return;
        setDiffCursor((cur) => (cur < 0 ? 0 : (cur + 1) % n));
      },
    }));
    unregs.push(registerCommand({
      id: 'import.diff-cursor-up',
      label: '导入：diff 光标上移',
      category: '导入',
      hotkey: 'Alt+↑',
      keywords: ['import', 'diff', 'cursor', 'nav', '键盘', '上移', 'up'],
      enabled: () => undoStack.length > 0,
      run: () => {
        const n = computeFilteredGroupCount();
        if (n === 0) return;
        setDiffCursor((cur) => (cur < 0 ? n - 1 : (cur - 1 + n) % n));
      },
    }));
    unregs.push(registerCommand({
      id: 'import.diff-cursor-toggle',
      label: '导入：切换当前光标分组折叠',
      category: '导入',
      hotkey: 'Alt+Enter',
      keywords: ['import', 'diff', 'cursor', 'toggle', '折叠', '切换'],
      enabled: () => diffCursor >= 0 && undoStack.length > 0,
      run: () => {
        if (diffCursor < 0) return;
        const actualN = Math.min(batchStepN, undoStack.length);
        const targetSnap = undoStack[actualN - 1].snapshot;
        const filtered = filterDiffGroups(groupDiffItems(buildUndoDiff(opts, targetSnap)));
        if (diffCursor >= filtered.length) return;
        const g = filtered[diffCursor].group;
        setCollapsedGroups((prev) => {
          const next = new Set(prev);
          if (next.has(g)) next.delete(g);
          else next.add(g);
          return next;
        });
      },
    }));
    unregs.push(registerCommand({
      id: 'import.diff-hit-next',
      label: '导入：搜索下一个命中',
      category: '导入',
      hotkey: 'Enter',
      keywords: ['import', 'diff', 'search', 'hit', 'next', '下一个', '命中'],
      enabled: () => diffSearch.trim().length > 0 && undoStack.length > 0,
      run: () => navDiffHit(1),
    }));
    unregs.push(registerCommand({
      id: 'import.diff-hit-prev',
      label: '导入：搜索上一个命中',
      category: '导入',
      hotkey: 'Shift+Enter',
      keywords: ['import', 'diff', 'search', 'hit', 'prev', '上一个', '命中'],
      enabled: () => diffSearch.trim().length > 0 && undoStack.length > 0,
      run: () => navDiffHit(-1),
    }));
    unregs.push(registerCommand({
      id: 'import.diff-search-case',
      label: `导入：搜索大小写敏感（${diffSearchCaseSensitive ? '开' : '关'}）`,
      category: '导入',
      hotkey: 'Shift+Alt+C',
      keywords: ['import', 'diff', 'search', 'case', '敏感', '大小写', 'case-sensitive'],
      enabled: () => undoStack.length > 0,
      run: () => setDiffSearchCaseSensitive((v) => !v),
    }));
    unregs.push(registerCommand({
      id: 'import.diff-search-history-clear',
      label: `导入：清空搜索历史（${diffSearchHistory.length}）`,
      category: '导入',
      keywords: ['import', 'diff', 'search', 'history', '清空', '历史'],
      enabled: () => diffSearchHistory.length > 0,
      run: () => {
        setDiffSearchHistory([]);
        try { localStorage.removeItem('polydb.diffSearchHistory.v1'); } catch { /* ignore */ }
      },
    }));
    unregs.push(registerCommand({
      id: 'import.diff-search-scope',
      label: `导入：搜索作用域切换到「${diffSearchScope === 'all' ? '全部' : diffSearchScope === 'field' ? '字段' : diffSearchScope === 'before' ? '前值' : '后值'}」（循环）`,
      category: '导入',
      hotkey: 'Alt+S',
      keywords: ['import', 'diff', 'search', 'scope', '作用域', '字段', '前值', '后值'],
      enabled: () => undoStack.length > 0,
      run: () => setDiffSearchScope((s) => {
        const order: DiffSearchScope[] = ['all', 'field', 'before', 'after'];
        const i = order.indexOf(s);
        return order[(i + 1) % order.length];
      }),
    }));
    unregs.push(registerCommand({
      id: 'import.diff-undo-current',
      label: '导入：撤销当前命中项（↩ 按钮或命令触发）',
      category: '导入',
      keywords: ['import', 'diff', 'undo', 'item', 'single', '单项', '命中'],
      enabled: () => diffSearch.trim().length > 0 && diffHitCursor >= 0 && undoStack.length > 0,
      run: () => {
        if (diffHitCursor < 0) return;
        const actualN = Math.min(batchStepN, undoStack.length);
        const targetSnap = undoStack[actualN - 1].snapshot;
        const filtered = filterDiffGroups(groupDiffItems(buildUndoDiff(opts, targetSnap)));
        const flat = filtered.filter((g) => !collapsedGroups.has(g.group)).flatMap((g) => g.items);
        const cur = flat[diffHitCursor];
        if (cur) applySingleItemUndo(cur, targetSnap);
      },
    }));
    return () => { unregs.forEach((u) => u()); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [undoStack.length, redoStack.length, batchStepN, opts, collapsedGroups.size, diffCursor, diffSearch, diffHitCursor, diffSearchCaseSensitive, diffSearchScope]);

  const buildFailureSummary = useCallback((): { key: string; label: string; count: number; rows: number[]; hint: string }[] => {
    if (failedRows.length === 0) return [];
    const groups = new Map<string, { label: string; hint: string; rows: number[] }>();
    for (const fr of failedRows) {
      const c = classifyFailReason(fr.reason);
      const g = groups.get(c.key);
      if (g) g.rows.push(fr.csvRow);
      else groups.set(c.key, { label: c.label, hint: c.hint, rows: [fr.csvRow] });
    }
    return Array.from(groups.entries()).map(([k, v]) => ({
      key: k, label: v.label, hint: v.hint, count: v.rows.length, rows: v.rows,
    })).sort((a, b) => b.count - a.count);
  }, [failedRows]);

  const failureSummary = useMemo(() => buildFailureSummary(), [buildFailureSummary]);

  const filteredFailedRows = useMemo(() => {
    if (!failCategoryFilter) return failedRows;
    return failedRows.filter((fr) => classifyFailReason(fr.reason).key === failCategoryFilter);
  }, [failedRows, failCategoryFilter]);

  const csvRowByIndex = useMemo(() => {
    if (!parse || parse.rows.length === 0) return new Map<number, string[]>();
    return new Map(parse.rows.map((r, i) => [i, r]));
  }, [parse]);

  const rawRowFor = (csvRow: number): string[] | null => csvRowByIndex.get(csvRow - 1) ?? null;

  // 失败行回跳：跳到 Step 1 textarea 并滚动到第 csvRow 行（M30.37）
  // csvRow 是 parse.rows 的 1-based 行号：CSV 有 header 时对应 textarea 第 csvRow+1 行；否则第 csvRow 行
  const jumpToCsvRow = (csvRow: number) => {
    setStep('input');
    setCsvRowFlash(csvRow);
    requestAnimationFrame(() => {
      const ta = csvTextRef.current;
      if (!ta) return;
      const lines = csvText.split('\n');
      const lineIdx = Math.max(0, Math.min((inputFormat === 'csv' && hasHeader ? csvRow : csvRow - 1), lines.length - 1));
      let charOffset = 0;
      for (let i = 0; i < lineIdx; i++) {
        charOffset += lines[i].length + 1;
      }
      const endOffset = Math.min(charOffset + (lines[lineIdx]?.length ?? 0), lines.length > 0 ? csvText.length : 0);
      try {
        ta.focus();
        ta.setSelectionRange(charOffset, endOffset);
        const lineHeight = 16;
        ta.scrollTop = Math.max(0, (lineIdx - 2) * lineHeight);
      } catch { /* ignore */ }
    });
    setTimeout(() => setCsvRowFlash(null), 1500);
  };

  const copyReportSummary = async () => {
    const lines: string[] = [];
    lines.push(`# polydb 导入报告`);
    lines.push(`表: ${inputFormat === 'sql' ? '<SQL>' : `${selSchema}.${selTable}`}`);
    lines.push(`文件: ${fileName ?? '(内联文本)'}`);
    lines.push(`模式: ${inputFormat === 'sql' ? 'SQL' : opts.mode}`);
    if (progress) lines.push(`耗时: ${progress.ms} ms`);
    lines.push(`总行数: ${inputFormat === 'sql' ? statements.length : progress?.total ?? 0}`);
    lines.push(`成功: ${insertedCount}`);
    lines.push(`失败: ${failedRows.length}`);
    if (failureSummary.length > 0) {
      lines.push('');
      lines.push('失败原因 Top-3:');
      for (const s of failureSummary.slice(0, 3)) {
        lines.push(`  - ${failCategoryMeta(s.key).icon} ${s.label} (${s.count} 行): ${s.hint}`);
      }
    }
    if (failedRows.length > 0) {
      lines.push('');
      lines.push(`前 5 失败行: ${failedRows.slice(0, 5).map((r) => `L${r.csvRow}`).join(', ')}`);
    }
    const text = lines.join('\n');
    try {
      await navigator.clipboard.writeText(text);
      publishEditorStatus({ message: `已复制导入报告摘要（${text.length} 字符）`, messageAt: Date.now() });
    } catch (e) {
      // 剪贴板失败 fallback：显示在状态栏
      publishEditorStatus({ message: `剪贴板不可用：${toMsg(e)}；摘要已生成（${text.length} 字符）`, messageAt: Date.now() });
    }
  };

  const cancelQueue = () => {
    const skipped = fileQueue.length - queuePos;
    cancelRef.current = true;
    setFileQueue([]);
    setQueuePos(0);
    publishEditorStatus({ message: `已取消导入队列${busy ? '（当前正在执行的批次也取消）' : ''}，剩余 ${skipped} 个文件已跳过`, messageAt: Date.now() });
  };
  // 批量复制失败行为 INSERT SQL：CSV 模式下把 params 内联为字面量；SQL 模式直接用原语句
  const copyFailedRowsAsInsertSql = async () => {
    if (failedRows.length === 0) return;
    const visible = failCategoryFilter ? filteredFailedRows : failedRows;
    const statements: string[] = [];
    if (inputFormat === 'sql') {
      for (const f of visible) {
        const s = sqlStatements[f.csvRow - 1];
        if (s) statements.push(`-- CSV row ${f.csvRow}: ${f.reason}\n${s.sql};`);
      }
    } else {
      for (const f of visible) {
        const s = csvStatements[f.stmtIdx];
        if (!s) continue;
        const params = s.params ?? [];
        // 用逐个替换 ? 的方式内联字面量（避免正则贪婪/遗漏）
        let sql = '';
        let qIdx = 0;
        for (let i = 0; i < s.sql.length; i++) {
          const ch = s.sql[i];
          if (ch === '?' && qIdx < params.length) {
            sql += sqlLiteral(params[qIdx]);
            qIdx++;
          } else {
            sql += ch;
          }
        }
        statements.push(`-- CSV row ${f.csvRow}: ${f.reason}\n${sql};`);
      }
    }
    if (statements.length === 0) {
      publishEditorStatus({ message: `ℹ️ 无失败行可复制（当前筛选为 0）`, messageAt: Date.now() });
      return;
    }
    const header = `-- 失败行 INSERT SQL（共 ${statements.length} 条，取自当前筛选${failCategoryFilter ? `「${failureSummary.find((s) => s.key === failCategoryFilter)?.label ?? failCategoryFilter}」` : '全部'}）\n-- 请在测试环境先验证\n\n`;
    try {
      await navigator.clipboard.writeText(header + statements.join('\n\n') + '\n');
      publishEditorStatus({ message: `📋 已复制 ${statements.length} 条失败行 INSERT SQL`, messageAt: Date.now() });
    } catch {
      publishEditorStatus({ message: '❌ 复制失败（浏览器剪贴板权限）', messageAt: Date.now() });
    }
  };

  const previewImported = (opts2?: { auto?: boolean; lastInserted?: number }) => {
    if (!selTable) return;
    const auto = opts2?.auto ?? false;
    const lastInserted = opts2?.lastInserted ?? 0;
    let sql = '';
    let strategy: 'pk' | 'limit' = 'limit';
    if (auto && inputFormat !== 'sql' && !retryIndices && lastInserted > 0) {
      const all = csvStatements;
      const successful = all.slice(Math.max(0, all.length - lastInserted));
      const activeIdx = mappings
        .map((_, i) => i)
        .filter((i) => mappings[i].targetColumn != null);
      if (activeIdx.length > 0 && successful.length > 0) {
        const pkColNames = cols.filter((c) => c.is_primary_key).map((c) => c.name);
        const isPkIdx = (i: number) => pkColNames.includes(mappings[i].targetColumn!);
        const pkActiveIdx = activeIdx.filter(isPkIdx);
        const dataActiveIdx = activeIdx.filter((i) => !isPkIdx(i));
        let pkParamPositions: number[] | null = null;
        if (opts.mode === 'insert' && pkActiveIdx.length > 0) {
          pkParamPositions = pkActiveIdx;
        } else if (opts.mode === 'update' && pkActiveIdx.length > 0) {
          pkParamPositions = pkActiveIdx.map((_, j) => dataActiveIdx.length + j);
        } else if (opts.mode === 'upsert' && pkActiveIdx.length > 0) {
          // buildStatements 拼 param 顺序为 [pk..., data...]
          pkParamPositions = pkActiveIdx.map((_, j) => j);
        }
        if (pkParamPositions && pkParamPositions.length > 0 && pkColNames.length > 0) {
          const n = Math.min(autoPreviewRows, successful.length);
          const tuples = successful.slice(0, n).map((s) => {
            const params = s.params ?? [];
            return pkColNames
              .map((_, j) => sqlLiteral(params[pkParamPositions![j]] ?? null))
              .join(', ');
          });
          if (tuples.length > 0) {
            sql = `SELECT * FROM ${qualTable(selSchema, selTable)} WHERE (${pkColNames.map(quoteIdent).join(', ')}) IN (${tuples.map((t) => `(${t})`).join(', ')});`;
            strategy = 'pk';
          }
        }
      }
    }
    if (!sql) {
      const limit = auto ? Math.max(1, Math.min(autoPreviewRows, 10000)) : 100;
      sql = `SELECT * FROM ${qualTable(selSchema, selTable)} LIMIT ${limit};`;
    }
    if (auto) {
      publishEditorStatus({
        message: strategy === 'pk'
          ? `🔍 自动预览：按 PK IN(...) 匹配最近 ${Math.min(autoPreviewRows, lastInserted)} 行`
          : `🔍 自动预览：无 PK 上下文，降级到 LIMIT ${Math.min(autoPreviewRows, 10000)}`,
        messageAt: Date.now(),
      });
    }
    window.dispatchEvent(new CustomEvent('polydb-import-preview', { detail: { sql, strategy } }));
  };

  const finishAndClose = () => {
    if (txIdRef.current) {
      void api.rollbackTransaction(txIdRef.current).catch(() => {});
      txIdRef.current = null;
    }
    clearEditorStatus();
    onClose();
  };

  const curStepIdx = STEP_ORDER.indexOf(step);
  // M30.142 备份预检排序：diff 大改动前置（降序），schema 字典序；stable 保原顺序在并列值下
  // M30.146 D：加 'risk' 风险排序（dead > 其他连接 > 变更数降序 > overwrite > schema）
  const diffCountOf = (i: BackupPreviewItem): number => {
    if (i.action !== 'overwrite' || !i.diff) return 0;
    const d = i.diff;
    return d.scalarDiffs.length + d.removedTargets.length + d.addedTargets.length
      + Object.keys(d.removedCols).length + Object.keys(d.addedCols).length;
  };
  const riskScoreOf = (i: BackupPreviewItem): number => {
    // 数值越大越靠前
    let s = 0;
    if (i.kind === 'preset' && backupItemAudit && backupItemAudit[i.key] === 'dead') s += 10000;
    const parts = i.key.split('::');
    const srcConn = parts[0];
    if (srcConn && srcConn !== connId) s += 5000;
    s += diffCountOf(i) * 10;
    if (i.action === 'overwrite') s += 5;
    return s;
  };
  const backupPreviewSorted = backupPending ? [...backupPending.items].sort((a, b) => {
    if (backupSortBy === 'risk') {
      const ra = riskScoreOf(a);
      const rb = riskScoreOf(b);
      if (rb !== ra) return rb - ra;
      return `${a.schema}.${a.table}`.localeCompare(`${b.schema}.${b.table}`);
    }
    if (backupSortBy === 'diff') {
      const da = diffCountOf(a);
      const db = diffCountOf(b);
      if (db !== da) return db - da;
      // 稳定 tiebreaker：overwrite 前置，再按 schema.table 字典序
      if ((a.action === 'overwrite') !== (b.action === 'overwrite')) return a.action === 'overwrite' ? -1 : 1;
      return `${a.schema}.${a.table}`.localeCompare(`${b.schema}.${b.table}`);
    }
    return `${a.schema}.${a.table}`.localeCompare(`${b.schema}.${b.table}`);
  }) : [];
  // M30.152 D：按表分组模式下，把可见项按「所在分组顺序（复用 M30.151 组排序）→ 组内变更数降序」重排
  const backupPreviewGroupOrderMap: Record<string, number> = (() => {
    if (!backupPending || !backupGroupBy) return {};
    const groups = new Set<string>();
    for (const i of backupPending.items) groups.add(`${i.schema}::${i.table}`);
    const arr = Array.from(groups);
    const diffOf = (gk: string) => {
      let s = 0;
      for (const i of backupPending.items) {
        if (`${i.schema}::${i.table}` !== gk) continue;
        if (i.action !== 'overwrite' || !i.diff || i.kind !== 'preset') continue;
        const d = i.diff;
        s += d.scalarDiffs.length + d.removedTargets.length + d.addedTargets.length
          + Object.keys(d.removedCols).length + Object.keys(d.addedCols).length;
      }
      return s;
    };
    // M30.164 C 组内平均风险分（导出分组顺序与 UI 排序一致）
    const riskAvgOf = (gk: string) => {
      let s = 0, n = 0;
      for (const i of backupPending.items) {
        if (`${i.schema}::${i.table}` !== gk) continue;
        s += computeItemRiskScore(i).score; n += 1;
      }
      return n === 0 ? 0 : s / n;
    };
    arr.sort((a, b) => {
      if (backupGroupSort === 'diff') {
        const da = diffOf(a);
        const db = diffOf(b);
        if (db !== da) return db - da;
        return a.localeCompare(b);
      }
      if (backupGroupSort === 'risk') {
        const ra = riskAvgOf(a);
        const rb = riskAvgOf(b);
        if (rb !== ra) return rb - ra;
        return a.localeCompare(b);
      }
      return a.localeCompare(b);
    });
    const map: Record<string, number> = {};
    arr.forEach((g, idx) => { map[g] = idx; });
    return map;
  })();
  // M30.144 A/B/D 键盘导航用：把 sorted 再走一遍与 UI 相同的 filter 链，产出用户可见列表
  const backupPreviewFiltered = (() => {
    if (!backupPending) return [] as BackupPreviewItem[];
    const q = backupPreviewSearch.trim().toLowerCase();
    // M30.154 B 环形图分档点击筛选：非 'all' 时只保留 overwrite preset 且落在对应分档的项
    const binMatch = (i: BackupPreviewItem): boolean => {
      if (backupDiffBinFilter === 'all') return true;
      if (i.action !== 'overwrite' || i.kind !== 'preset' || !i.diff) return false;
      const dc = diffCountOf(i);
      const bin = dc === 0 ? '0' : dc <= 3 ? '1' : dc <= 10 ? '2' : '3';
      return bin === backupDiffBinFilter;
    };
    const visible = backupPreviewSorted.filter((i) => {
      if (backupPreviewFilter !== 'all' && i.action !== backupPreviewFilter) return false;
      if (backupPreviewKindFilter !== 'all' && i.kind !== backupPreviewKindFilter) return false;
      if (!binMatch(i)) return false;
      if (q && !i.schema.toLowerCase().includes(q) && !i.table.toLowerCase().includes(q)) return false;
      if (backupGroupBy && backupCollapsedGroups.has(`${i.schema}::${i.table}`)) return false;
      // M30.158 A 风险级别过滤：'all' 全部通过；否则只保留匹配 level 的项
      if (backupRiskLevelFilter !== 'all' && computeItemRiskScore(i).level !== backupRiskLevelFilter) return false;
      // M30.161 C 风险阈值滑块：只保留 score >= threshold
      if (backupRiskThreshold > 0 && computeItemRiskScore(i).score < backupRiskThreshold) return false;
      return true;
    });
    if (!backupGroupBy) return visible;
    return visible.slice().sort((a, b) => {
      const ga = backupPreviewGroupOrderMap[`${a.schema}::${a.table}`] ?? Number.MAX_SAFE_INTEGER;
      const gb = backupPreviewGroupOrderMap[`${b.schema}::${b.table}`] ?? Number.MAX_SAFE_INTEGER;
      if (ga !== gb) return ga - gb;
      const da = diffCountOf(a);
      const db = diffCountOf(b);
      if (db !== da) return db - da;
      return a.key.localeCompare(b.key);
    });
  })();

  // M30.142 备份预检确认/取消：提到组件顶层让键盘 useEffect 可见
  // M30.143 D：apply 前先取快照，成功后置 pendingBackupUndo 并启动 5s 倒计时
  // M30.147 B：Ctrl+Enter 或「✅ 应用」按钮先弹二次确认浮层，Enter 才真正 doApplyBackup
  const confirmApplyBackup = () => {
    if (!backupPending) return;
    if (backupSelected.size === 0) {
      setPresetManagerMsg({ kind: 'err', text: '❌ 未选择任何项，请勾选至少 1 项后再确认' });
      return;
    }
    setBackupConfirmOverlayOpen(true);
  };
  const doApplyBackup = () => {
    if (!backupPending) return;
    const { backup, items } = backupPending;
    const selKeys = backupSelected;
    const selectedPresets = new Set(items.filter((i) => i.kind === 'preset' && selKeys.has(i.key)).map((i) => i.key));
    const selectedSnapshots = new Set(items.filter((i) => i.kind === 'snapshot' && selKeys.has(i.key)).map((i) => i.key));
    if (selectedPresets.size === 0 && selectedSnapshots.size === 0) {
      setPresetManagerMsg({ kind: 'err', text: '❌ 未选择任何项，请勾选至少 1 项后再确认' });
      return;
    }
    const skipCount = items.length - (selectedPresets.size + selectedSnapshots.size);
    // M30.149 B：apply 耗时测量（localStorage 写盘 + diff 计算）
    const t0 = performance.now();
    const snap = snapshotPresetsState();
    const stats = applyPresetsBackup(backup, { overwrite: presetManagerOverwrite, selectedPresets, selectedSnapshots });
    const elapsedMs = Math.round(performance.now() - t0);
    if (stats.errors.length > 0) {
      setPresetManagerMsg({ kind: 'err', text: `❌ ${stats.errors.join('; ')}` });
      return;
    }
    const parts: string[] = [];
    if (stats.presetsAdded > 0) parts.push(`+${stats.presetsAdded} 新增预设`);
    if (stats.presetsOverwritten > 0) parts.push(`=${stats.presetsOverwritten} 覆盖预设`);
    if (stats.snapshotsAdded > 0) parts.push(`+${stats.snapshotsAdded} 新快照组`);
    if (stats.snapshotsOverwritten > 0) parts.push(`=${stats.snapshotsOverwritten} 覆盖快照组`);
    const summary = parts.length > 0 ? parts.join(' · ') : '无变化';
    // M30.149 C：已跳过项提示
    const skipHint = skipCount > 0 ? ` · ⊘ 已跳过 ${skipCount}` : '';
    // M30.150 C 耗时颜色分级：🟢≤50ms / 🟡50-200ms / 🟠200-500ms / 🔴>500ms
    const timingBadge = elapsedMs <= 50 ? '🟢' : elapsedMs <= 200 ? '🟡' : elapsedMs <= 500 ? '🟠' : '🔴';
    // M30.159 D：按分组记录本次 apply 的项 key，供 banner 数字点击展开明细
    const detail: BackupAppliedDetail = {
      presetsAdded: [], presetsOverwritten: [], snapshotsAdded: [], snapshotsOverwritten: [], skipped: [],
      elapsedMs, at: Date.now(),
    };
    for (const it of items) {
      const key = it.key;
      if (!selKeys.has(key)) { detail.skipped.push(key); continue; }
      if (it.kind === 'preset') {
        if (it.action === 'overwrite') detail.presetsOverwritten.push(key);
        else detail.presetsAdded.push(key);
      } else {
        if (it.action === 'overwrite') detail.snapshotsOverwritten.push(key);
        else detail.snapshotsAdded.push(key);
      }
    }
    setLastBackupAppliedDetail(detail);
    setPresetManagerMsg({ kind: 'ok', text: `✅ 已导入：${summary}（${timingBadge} ${elapsedMs}ms${skipHint}；5s 内 Ctrl+Z 撤销）` });
    publishEditorStatus({ message: `📥 已导入预设备份：${summary}（${timingBadge} ${elapsedMs}ms${skipHint}）`, messageAt: Date.now() });
    setLastApplyAddedPresets(stats.presetsAdded);
    setPendingBackupUndo(snap);
    setBackupUndoLeft(5);
    // M30.145 A undo 影响预览：与快照前态对比，算出撤销会移除/恢复的项数
    try { setPendingBackupUndoImpact(diffUndoImpact(snap)); } catch { setPendingBackupUndoImpact(null); }
    setPresetSnapshotsRefresh((n) => n + 1);
    setBackupPending(null);
    setBackupSelected(new Set());
    setBackupPreviewFilter('all');
    setBackupPreviewKindFilter('all');
    setBackupPreviewSearch('');
    setBackupSortBy('diff');
    setBackupPreviewAllDiff(false);
    setBackupFocusIdx(0);
    setBackupDiffCollapsed(new Set());
    // M30.145 B/C 备份预检态清空
    setBackupItemAudit(null);
    backupItemAuditRef.current = new Set();
    setBackupShortcutsOpen(false);
    // M30.147 B/D 关闭确认浮层并清元信息（保留 lastApplyAuditSummary 供 undo banner 显示）
    setBackupConfirmOverlayOpen(false);
    setBackupSourceFileName(null);
    setBackupSourceHash(null);
    // M30.155 B 白名单跳过计数同步清零
    setBackupWhitelistSkipped(0);
    setBackupEmbeddedWhitelist(null);
    setLastApplyAuditSummary(null);
    // M30.147 C：apply 成功后对内联审计新增/覆盖的预设项
    void (async () => {
      const applied = items.filter((i) => i.kind === 'preset' && selKeys.has(i.key));
      if (applied.length === 0) return;
      const cache = loadAuditCache(connId);
      let ok = 0, dead = 0;
      for (const it of applied) {
        const parts2 = it.key.split('::');
        if (parts2.length < 3) continue;
        const [bk] = parts2;
        if (bk !== connId) continue;
        const cached = cache?.results[it.key];
        if (cached) {
          if (cached.status === 'ok') ok++; else dead++;
          continue;
        }
        try {
          await api.listColumns(connId, it.schema, it.table);
          ok++;
        } catch {
          dead++;
        }
      }
      const total = ok + dead;
      setLastApplyAuditSummary(total > 0 ? { applied: total, ok, dead } : null);
    })();
  };
  const cancelBackup = () => {
    setBackupPending(null);
    setBackupSelected(new Set());
    setBackupPreviewFilter('all');
    setBackupPreviewKindFilter('all');
    setBackupPreviewSearch('');
    setBackupSortBy('diff');
    setBackupPreviewAllDiff(false);
    setBackupFocusIdx(0);
    setBackupDiffCollapsed(new Set());
    setBackupItemAudit(null);
    backupItemAuditRef.current = new Set();
    setBackupShortcutsOpen(false);
    // M30.147 B/D 二次确认浮层与源文件名同步清空
    setBackupConfirmOverlayOpen(false);
    setBackupSourceFileName(null);
    setBackupSourceHash(null);
    // M30.155 B 白名单跳过计数同步清零
    setBackupWhitelistSkipped(0);
    setBackupEmbeddedWhitelist(null);
    setPresetManagerMsg(null);
  };

  // M30.143 A：一键重置视图（Alt+R 或按钮）——只清 filter/search/sort/allDiff/focus/collapsed，保留 pending+selected
  const resetBackupView = () => {
    setBackupPreviewFilter('all');
    setBackupPreviewKindFilter('all');
    setBackupDiffBinFilter('all');
    setBackupPreviewSearch('');
    setBackupSortBy('diff');
    setBackupGroupSort('diff');
    setBackupPreviewAllDiff(false);
    setBackupFocusIdx(0);
    setBackupDiffCollapsed(new Set());
    setBackupRiskLevelFilter('all');
  };

  // M30.143 D：撤销 5s 内备份应用（Ctrl+Z 或撤销 banner 按钮）
  const applyBackupUndo = () => {
    if (!pendingBackupUndo) return;
    const snap = pendingBackupUndo;
    const r = undoApplyPresetsBackup(snap);
    if (!r.ok) {
      setPresetManagerMsg({ kind: 'err', text: `❌ 撤销失败：${r.error ?? 'unknown'}` });
      setPendingBackupUndo(null);
      setPendingBackupUndoImpact(null);
      setBackupUndoLeft(0);
      setUndoImpactDrillOpen(false);
      return;
    }
    setPresetManagerMsg({ kind: 'ok', text: '✅ 已撤销备份应用' });
    publishEditorStatus({ message: '↩️ 已撤销备份应用', messageAt: Date.now() });
    setPendingBackupUndo(null);
    setPendingBackupUndoImpact(null);
    setBackupUndoLeft(0);
    setPresetSnapshotsRefresh((n) => n + 1);
  };

  // M30.142/143 备份预检键盘闭环：Ctrl+Enter 确认，Esc 取消，Alt+R 重置视图，Alt+V 预览所有 diff，Ctrl+Z 撤销（5s 内）
  useEffect(() => {
    if (!presetManagerOpen) return;
    const h = (ev: KeyboardEvent) => {
      const t = ev.target as HTMLElement | null;
      const inInput = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable);
      // M30.147 B：二次确认浮层打开时接管 Esc/Enter，Esc 只关浮层、Enter/Ctrl+Enter 真正应用
      if (backupConfirmOverlayOpen && backupPending) {
        if (ev.key === 'Escape') {
          ev.preventDefault();
          ev.stopPropagation();
          setBackupConfirmOverlayOpen(false);
          return;
        }
        if (ev.key === 'Enter' && !ev.altKey && !ev.metaKey) {
          if (inInput) return;
          ev.preventDefault();
          setBackupConfirmOverlayOpen(false);
          doApplyBackup();
          return;
        }
      }
      // Esc：取消（与顶部"Esc 关闭 presetManager"分层，先取消预检保留管理器可见）
      // M30.146 A：撤销影响详情浮层打开时让 Esc 只关浮层，不关预检
      // M30.154 A：撤销摘要预览浮层打开时同样只关浮层
      // M30.155 A：白名单管理浮层打开时 Esc 只关浮层
      // M30.165 A：白名单浮层键盘导航（↑↓ 移动、Enter 复制、Delete 移除当前焦点行）
      if (riskWhitelistOpen) {
        const kws = whitelistSearch.trim().toLowerCase();
        const searchFiltered = Array.from(riskWhitelist.entries()).filter(
          ([k]) => !kws || k.toLowerCase().includes(kws),
        );
        const kindFiltered = whitelistKindFilter === 'all'
          ? searchFiltered
          : searchFiltered.filter(([k]) => {
              const parts = k.split('::');
              return parts.length >= 4 && parts[3] === whitelistKindFilter;
            });
        const sorted = kindFiltered.sort((a, b) => {
          if (whitelistSortBy === 'key') return a[0].localeCompare(b[0]);
          if (whitelistSortBy === 'expiring') return a[1] - b[1];
          return b[1] - a[1];
        });
        const n = sorted.length;
        if ((ev.key === 'ArrowDown' || ev.key === 'ArrowUp') && !ev.ctrlKey && !ev.metaKey && !ev.altKey && !ev.shiftKey && n > 0) {
          if (inInput) return;
          ev.preventDefault();
          setWhitelistFocusIdx((prev) => {
            const delta = ev.key === 'ArrowDown' ? 1 : -1;
            return (prev + delta + n) % n;
          });
          return;
        }
        if (ev.key === 'Enter' && !ev.ctrlKey && !ev.altKey && !ev.metaKey && !ev.shiftKey && n > 0) {
          if (inInput) return;
          ev.preventDefault();
          const idx = Math.min(whitelistFocusIdx, n - 1);
          const cur = sorted[idx];
          if (cur) {
            void navigator.clipboard.writeText(cur[0]).then(
              () => setPresetManagerMsg({ kind: 'ok', text: `📋 已复制白名单 key：${cur[0]}（Enter）` }),
              () => setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败' }),
            );
          }
          return;
        }
        if ((ev.key === 'Delete' || ev.key === 'Backspace') && !ev.ctrlKey && !ev.altKey && !ev.metaKey && !ev.shiftKey && n > 0) {
          if (inInput) return;
          ev.preventDefault();
          const idx = Math.min(whitelistFocusIdx, n - 1);
          const cur = sorted[idx];
          if (cur) {
            removeRiskWhitelistItem(cur[0]);
            setWhitelistFocusIdx((prev) => (n <= 1 ? 0 : Math.min(prev, n - 2)));
          }
          return;
        }
      }
      if (ev.key === 'Escape' && riskWhitelistOpen) {
        ev.preventDefault();
        ev.stopPropagation();
        setRiskWhitelistOpen(false);
        return;
      }
      if (ev.key === 'Escape' && backupUndoPreviewText !== null) {
        ev.preventDefault();
        ev.stopPropagation();
        setBackupUndoPreviewText(null);
        return;
      }
      // M30.159 D：变更清单浮层 Esc 只关浮层
      if (ev.key === 'Escape' && backupAppliedDetailOpen) {
        ev.preventDefault();
        ev.stopPropagation();
        setBackupAppliedDetailOpen(false);
        return;
      }
      if (ev.key === 'Escape' && backupPending && !undoImpactDrillOpen && !backupConfirmOverlayOpen && !backupAppliedDetailOpen) {
        ev.preventDefault();
        ev.stopPropagation();
        cancelBackup();
        return;
      }
      // M30.161 B：apply 变更清单浮层键盘导航 ↑↓ 切段、Enter 复制该段全部 key、Ctrl+F 切分类过滤
      if (backupAppliedDetailOpen && lastBackupAppliedDetail) {
        const d = lastBackupAppliedDetail;
        const sectionDefs: Array<{ id: 'added' | 'overwritten' | 'skipped'; title: string; keys: string[] }> = [
          { id: 'added', title: '新增预设', keys: d.presetsAdded },
          { id: 'overwritten', title: '覆盖预设', keys: d.presetsOverwritten },
          { id: 'added', title: '新增快照组', keys: d.snapshotsAdded },
          { id: 'overwritten', title: '覆盖快照组', keys: d.snapshotsOverwritten },
          { id: 'skipped', title: '已跳过项', keys: d.skipped },
        ];
        const visible = sectionDefs.filter((s) => {
          if (s.keys.length === 0) return false;
          if (backupDetailFilter === 'all') return true;
          if (backupDetailFilter === 'added') return s.id === 'added';
          if (backupDetailFilter === 'overwritten') return s.id === 'overwritten';
          return s.id === 'skipped';
        });
        if (ev.key === 'f' && ev.ctrlKey && !ev.altKey && !ev.metaKey && !ev.shiftKey) {
          if (inInput) return;
          ev.preventDefault();
          const order: Array<'all' | 'added' | 'overwritten' | 'skipped'> = ['all', 'added', 'overwritten', 'skipped'];
          const cur = order.indexOf(backupDetailFilter);
          const next = order[(cur + 1) % order.length];
          setBackupDetailFilter(next);
          setBackupDetailFocusIdx(0);
          setPresetManagerMsg({ kind: 'ok', text: `🔍 变更清单过滤切至：${next === 'all' ? '全部' : next === 'added' ? '新增' : next === 'overwritten' ? '覆盖' : '跳过'}（Ctrl+F）` });
          return;
        }
        if ((ev.key === 'ArrowDown' || ev.key === 'ArrowUp') && !ev.ctrlKey && !ev.metaKey && !ev.altKey && !ev.shiftKey && visible.length > 0) {
          if (inInput) return;
          ev.preventDefault();
          setBackupDetailFocusIdx((prev) => {
            const delta = ev.key === 'ArrowDown' ? 1 : -1;
            return (prev + delta + visible.length) % visible.length;
          });
          return;
        }
        if (ev.key === 'Enter' && !ev.ctrlKey && !ev.altKey && !ev.metaKey && !ev.shiftKey && visible.length > 0) {
          if (inInput) return;
          ev.preventDefault();
          const idx = Math.min(backupDetailFocusIdx, visible.length - 1);
          const s = visible[idx];
          if (s) {
            void navigator.clipboard.writeText(s.keys.join('\n')).then(
              () => setPresetManagerMsg({ kind: 'ok', text: `📋 已复制「${s.title}」全部 ${s.keys.length} 个 key（Enter）` }),
              () => setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败' }),
            );
          }
          return;
        }
      }
      // M30.160 C：Alt+D 打开 apply 变更清单浮层（M30.159 D 详情；仅在已完成一次 apply 后生效）
      if (ev.key === 'd' && ev.altKey && !ev.ctrlKey && !ev.metaKey && !ev.shiftKey && lastBackupAppliedDetail) {
        if (inInput) return;
        ev.preventDefault();
        setBackupAppliedDetailOpen(true);
        return;
      }
      // Ctrl+Enter：确认应用（保留守卫：预检打开时接管；编辑器 Ctrl+Enter 通过 backupPending 前置守卫区分）
      // M30.147 B：先弹二次确认浮层，Enter 才真正 apply
      if (ev.key === 'Enter' && ev.ctrlKey && !ev.altKey && !ev.metaKey && backupPending) {
        if (inInput) return;
        ev.preventDefault();
        if (backupSelected.size === 0) return;
        confirmApplyBackup();
        return;
      }
      // Alt+R：重置预检视图（4 条件焦点守卫，输入框跳过；M30.146 D 加 !shiftKey 让 Alt+Shift+R 走风险排序）
      if (ev.key === 'r' && ev.altKey && !ev.ctrlKey && !ev.metaKey && !ev.shiftKey && backupPending) {
        if (inInput) return;
        ev.preventDefault();
        resetBackupView();
        return;
      }
      // Alt+V：切换「预览所有 diff」全局开关
      if (ev.key === 'v' && ev.altKey && !ev.ctrlKey && !ev.metaKey && backupPending) {
        if (inInput) return;
        ev.preventDefault();
        setBackupPreviewAllDiff((v) => !v);
        return;
      }
      // M30.146 D：Alt+Shift+R 切风险排序（与 Alt+R 重置视图分层：Shift 区分）
      if (ev.key === 'r' && ev.altKey && ev.shiftKey && !ev.ctrlKey && !ev.metaKey && backupPending) {
        if (inInput) return;
        ev.preventDefault();
        setBackupSortBy('risk');
        return;
      }
      // M30.155 C：Ctrl+A 备份预检全选 / Shift+A 反选（避开过滤范围；inInput 跳过防与 Monaco 冲突）
      if ((ev.key === 'a' || ev.key === 'A') && (ev.ctrlKey || ev.shiftKey) && !ev.altKey && !ev.metaKey && backupPending && !backupGroupBy) {
        if (inInput) return;
        ev.preventDefault();
        const { items } = backupPending;
        if (ev.ctrlKey && !ev.shiftKey) {
          // Ctrl+A：全选所有项
          setBackupSelected(new Set(items.map((i) => i.key)));
        } else {
          // Shift+A：反选（已选→取消，未选→加入）
          setBackupSelected((prev) => {
            const next = new Set<string>();
            for (const i of items) {
              if (!prev.has(i.key)) next.add(i.key);
            }
            return next;
          });
        }
        return;
      }
      // M30.159 C：Ctrl+Shift+C 复制预检 filter 状态（与按钮「📋 复制 filter」一致）
      if (ev.key === 'c' && ev.ctrlKey && ev.shiftKey && !ev.altKey && !ev.metaKey && backupPending) {
        if (inInput) return;
        ev.preventDefault();
        const state = {
          filter: backupPreviewFilter,
          kindFilter: backupPreviewKindFilter,
          sortBy: backupSortBy,
          search: backupPreviewSearch,
          riskLevelFilter: backupRiskLevelFilter,
          riskThreshold: backupRiskThreshold,
          previewAllDiff: backupPreviewAllDiff,
          copyAt: new Date().toISOString(),
        };
        void navigator.clipboard.writeText(JSON.stringify(state, null, 2)).then(
          () => setPresetManagerMsg({
            kind: 'ok',
            text: `📋 已复制预检 filter 状态（Ctrl+Shift+C · filter=${backupPreviewFilter} kind=${backupPreviewKindFilter} sort=${backupSortBy} risk=${backupRiskLevelFilter}≥${backupRiskThreshold}）`,
          }),
          () => setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败（浏览器剪贴板权限）' }),
        );
        return;
      }
      // M30.159 C：Ctrl+Shift+V 粘贴预检 filter 状态（与按钮「📥 粘贴 filter」一致）
      if (ev.key === 'v' && ev.ctrlKey && ev.shiftKey && !ev.altKey && !ev.metaKey && backupPending) {
        if (inInput) return;
        ev.preventDefault();
        void (async () => {
          let text: string;
          try { text = await navigator.clipboard.readText(); }
          catch { setPresetManagerMsg({ kind: 'err', text: '❌ 读取剪贴板失败（浏览器权限）' }); return; }
          try {
            const p = JSON.parse(text) as Partial<Record<'filter' | 'kindFilter' | 'sortBy' | 'search' | 'riskLevelFilter' | 'riskThreshold' | 'previewAllDiff', unknown>>;
            if (!p || typeof p !== 'object') throw new Error('non-object');
            let n = 0;
            if (p.filter === 'all' || p.filter === 'overwrite' || p.filter === 'add') { setBackupPreviewFilter(p.filter); n += 1; }
            if (p.kindFilter === 'all' || p.kindFilter === 'preset' || p.kindFilter === 'snapshot') { setBackupPreviewKindFilter(p.kindFilter); n += 1; }
            if (p.sortBy === 'diff' || p.sortBy === 'schema' || p.sortBy === 'risk') { setBackupSortBy(p.sortBy as 'diff' | 'schema' | 'risk'); n += 1; }
            if (typeof p.search === 'string') { setBackupPreviewSearch(p.search); n += 1; }
            if (p.riskLevelFilter === 'all' || p.riskLevelFilter === 'low' || p.riskLevelFilter === 'mid' || p.riskLevelFilter === 'high') { setBackupRiskLevelFilter(p.riskLevelFilter); n += 1; }
            if (typeof p.riskThreshold === 'number' && isFinite(p.riskThreshold)) { setBackupRiskThreshold(Math.max(0, Math.min(100, Math.round(p.riskThreshold)))); n += 1; }
            if (typeof p.previewAllDiff === 'boolean') { setBackupPreviewAllDiff(p.previewAllDiff); n += 1; }
            if (n === 0) throw new Error('no fields matched');
            setPresetManagerMsg({ kind: 'ok', text: `✅ 已恢复 filter 状态（Ctrl+Shift+V · ${n} 项生效）` });
          } catch {
            setPresetManagerMsg({ kind: 'err', text: '❌ 剪贴板不是有效的 filter JSON' });
          }
        })();
        return;
      }
      // M30.148 D：Alt+A 一键展开/收起全部分组（仅分组模式打开时响应）
      if (ev.key === 'a' && ev.altKey && !ev.ctrlKey && !ev.metaKey && !ev.shiftKey && backupPending && backupGroupBy) {
        if (inInput) return;
        ev.preventDefault();
        setBackupCollapsedGroups((prev) => {
          if (prev.size > 0) return new Set();
          const next = new Set<string>();
          for (const i of backupPreviewFiltered) {
            next.add(`${i.schema}::${i.table}`);
          }
          return next;
        });
        return;
      }
      // M30.151 C：Alt+O 循环切换分组排序（diff/name/risk，M30.164 C 加 risk）
      if (ev.key === 'o' && ev.altKey && !ev.ctrlKey && !ev.metaKey && !ev.shiftKey && backupPending && backupGroupBy) {
        if (inInput) return;
        ev.preventDefault();
        setBackupGroupSort((cur) => (cur === 'diff' ? 'name' : cur === 'name' ? 'risk' : 'diff'));
        return;
      }
      // M30.163 C：Alt+0 一键重置风险阈值到 0（仅当 >0 时生效；与 M30.161 C 阈值滑块配合的快捷通道）
      if ((ev.key === '0' || ev.key === 'Digit0') && ev.altKey && !ev.ctrlKey && !ev.metaKey && !ev.shiftKey && backupPending && backupRiskThreshold > 0) {
        if (inInput) return;
        ev.preventDefault();
        setBackupRiskThreshold(0);
        setPresetManagerMsg({ kind: 'ok', text: '📊 风险阈值已重置到 0（不过滤 · Alt+0）' });
        return;
      }
      // M30.165 B：Alt+9 切"只看高风险"（riskLevelFilter='high'；再按切回 all）
      if ((ev.key === '9' || ev.key === 'Digit9') && ev.altKey && !ev.ctrlKey && !ev.metaKey && !ev.shiftKey && backupPending) {
        if (inInput) return;
        ev.preventDefault();
        const next = backupRiskLevelFilter === 'high' ? 'all' : 'high';
        setBackupRiskLevelFilter(next);
        setPresetManagerMsg({ kind: 'ok', text: next === 'high' ? '🔥 只看高风险（Alt+9）' : '👁 已显示全部风险等级（Alt+9）' });
        return;
      }
      // Ctrl+Z：撤销 5s 内备份应用（与预检互斥：预检关闭且有 pendingBackupUndo 才响应）
      if (ev.key === 'z' && ev.ctrlKey && !ev.altKey && !ev.metaKey && pendingBackupUndo && !backupPending) {
        if (inInput) return;
        ev.preventDefault();
        applyBackupUndo();
        return;
      }
      // M30.144 A：ArrowDown/ArrowUp 移动焦点（过滤后列表内循环）
      if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
        if (!backupPending) return;
        if (inInput) return; // 输入框内保留默认光标移动
        if (backupPreviewFiltered.length === 0) return;
        ev.preventDefault();
        const n = backupPreviewFiltered.length;
        const next = ev.key === 'ArrowDown'
          ? (backupFocusIdx + 1) % n
          : (backupFocusIdx - 1 + n) % n;
        setBackupFocusIdx(next);
        // scrollIntoView：行 id 格式 backup-row-{kind}-{key}
        const target = backupPreviewFiltered[next];
        if (target) {
          const el = document.querySelector<HTMLElement>(`[data-backup-key="${CSS.escape(`${target.kind}::${target.key}`)}"]`);
          el?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
        }
        return;
      }
      // M30.144 A：Space 切换焦点项勾选（不 preventDefault 让原生复选框处理）
      if (ev.key === ' ' && !ev.ctrlKey && !ev.altKey && !ev.metaKey) {
        if (!backupPending) return;
        if (inInput) return; // 让复选框/其他控件自己处理
        if (backupPreviewFiltered.length === 0) return;
        const target = backupPreviewFiltered[backupFocusIdx];
        if (!target) return;
        ev.preventDefault();
        const nextSel = new Set(backupSelected);
        if (nextSel.has(target.key)) nextSel.delete(target.key); else nextSel.add(target.key);
        setBackupSelected(nextSel);
        return;
      }
      // M30.144 A/B：plain Enter 切换焦点项 diff 折叠/展开（仅当该项是 preset+overwrite+有 diff）
      if (ev.key === 'Enter' && !ev.ctrlKey && !ev.altKey && !ev.metaKey && !ev.shiftKey) {
        if (!backupPending) return;
        if (inInput) return;
        const target = backupPreviewFiltered[backupFocusIdx];
        if (!target) return;
        if (!(target.action === 'overwrite' && target.kind === 'preset' && target.diff)) return;
        ev.preventDefault();
        setBackupDiffCollapsed((prev) => {
          const next = new Set(prev);
          if (next.has(target.key)) next.delete(target.key); else next.add(target.key);
          return next;
        });
        return;
      }
    };
    window.addEventListener('keydown', h, true);
    return () => window.removeEventListener('keydown', h, true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [presetManagerOpen, backupPending, backupSelected, pendingBackupUndo, backupPreviewFiltered.length, backupPreviewFiltered, backupFocusIdx, backupPreviewFilter, backupPreviewKindFilter, backupPreviewSearch, backupRiskLevelFilter, backupRiskThreshold, backupSortBy, backupPreviewAllDiff, undoImpactDrillOpen, backupConfirmOverlayOpen, backupAppliedDetailOpen, backupGroupBy, backupCollapsedGroups, backupGroupSort, backupUndoPreviewText, riskWhitelistOpen, riskWhitelist, whitelistFocusIdx, whitelistSearch, whitelistKindFilter, whitelistSortBy, lastBackupAppliedDetail, backupDetailFilter, backupDetailFocusIdx]);

  // M30.145 B dead 项自动取消勾选（只取消原本勾选的；用户可手动重新勾选）
  useEffect(() => {
    if (!backupPending || !backupItemAudit) return;
    const dead = Object.entries(backupItemAudit).filter(([, s]) => s === 'dead').map(([k]) => k);
    if (dead.length === 0) return;
    const deadSet = new Set(dead);
    setBackupSelected((prev) => {
      const next = new Set(prev);
      let changed = false;
      for (const k of deadSet) {
        if (next.has(k)) { next.delete(k); changed = true; }
      }
      return changed ? next : prev;
    });
  }, [backupPending, backupItemAudit]);

  // M30.145 C 备份预检快捷键浮层：? 键（capture phase）在备份预检打开时切换
  useEffect(() => {
    if (!presetManagerOpen || !backupPending) return;
    const h = (ev: KeyboardEvent) => {
      // Shift+/ 在多数键盘上就是 '?'（key === '?'），无修饰键触发
      if (ev.key === '?' && !ev.ctrlKey && !ev.altKey && !ev.metaKey && !ev.shiftKey) {
        const t = ev.target as HTMLElement | null;
        if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
        ev.preventDefault();
        setBackupShortcutsOpen((v) => !v);
      }
      if (ev.key === 'Escape' && !ev.ctrlKey && !ev.altKey && !ev.metaKey && !ev.shiftKey && backupShortcutsOpen) {
        setBackupShortcutsOpen(false);
        // 不 stopPropagation：让 Esc 继续传给下方取消预检 handler
      }
    };
    window.addEventListener('keydown', h, true);
    return () => window.removeEventListener('keydown', h, true);
  }, [presetManagerOpen, backupPending, backupShortcutsOpen]);

  // M30.146 A：撤销影响详情浮层 Esc 关闭（capture phase 优先于其他 Esc handler）
  useEffect(() => {
    if (!undoImpactDrillOpen) return;
    const h = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape' && !ev.ctrlKey && !ev.altKey && !ev.metaKey && !ev.shiftKey) {
        ev.preventDefault();
        setUndoImpactDrillOpen(false);
      }
    };
    window.addEventListener('keydown', h, true);
    return () => window.removeEventListener('keydown', h, true);
  }, [undoImpactDrillOpen]);

  // M30.143 D：撤销倒计时 1s tick，倒计时结束清除 pendingBackupUndo
  useEffect(() => {
    if (!pendingBackupUndo || backupUndoLeft <= 0) return;
    const t = setTimeout(() => {
      setBackupUndoLeft((n) => n - 1);
    }, 1000);
    return () => clearTimeout(t);
  }, [pendingBackupUndo, backupUndoLeft]);

  // M30.143 D：倒计时归零后清除 pendingBackupUndo（保留最后 1s 显示便于用户感知）
  useEffect(() => {
    if (!pendingBackupUndo || backupUndoLeft > 0) return;
    const t = setTimeout(() => {
      setPendingBackupUndo(null);
      setPendingBackupUndoImpact(null);
      setUndoImpactDrillOpen(false);
    }, 500);
    return () => clearTimeout(t);
  }, [pendingBackupUndo, backupUndoLeft]);

  // Step 徽章状态（M30.36）：✓ 已完成（绿）/ ⚠ 有警告（橙）/ ⏳ 当前（accent）/ · 未开始（muted）
  const stepWarnFlags: Record<Step, boolean> = {
    input: false,
    table: false,
    map: !!pendingQualitySuggestions && Object.keys(pendingQualitySuggestions).length > 0,
    preview: failedRows.length > 0,
    done: false,
  };
  const stepBar = (
    <StepBar step={step} curStepIdx={curStepIdx} stepWarnFlags={stepWarnFlags} stepFlash={stepFlash} parse={parse} selTable={selTable} mappedCount={mappedCount} setStep={setStep} />
  );
  return (
    <div style={styles.overlay} onClick={(e) => { if (e.target === e.currentTarget && riskItemCtxMenu === null) finishAndClose(); }}>
      {riskItemCtxMenu && (() => {
        const items: ContextMenuEntry[] = [
          { header: riskItemCtxMenu.key.split('::').slice(-2).join('.') },
          {
            key: 'copy',
            label: '复制完整 key',
            icon: '📋',
            shortcut: 'C',
            onClick: () => {
              navigator.clipboard?.writeText(riskItemCtxMenu.key).then(
                () => setPresetManagerMsg({ kind: 'ok', text: '📋 已复制完整 key' }),
                () => setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败' }),
              );
            },
          },
          {
            key: 'remove',
            label: '单独剔除（本次跳过）',
            icon: '⚡',
            onClick: () => {
              setBackupSelected((prev) => {
                const next = new Set(prev);
                next.delete(riskItemCtxMenu.key);
                return next;
              });
              setPresetManagerMsg({ kind: 'ok', text: `⚡ 已剔除：${riskItemCtxMenu.key.split('::').slice(-2).join('.')}` });
            },
          },
          '---',
          {
            key: 'whitelist',
            label: '加入白名单（下次自动跳过）',
            icon: '🔒',
            disabled: riskWhitelist.has(riskItemCtxMenu.key),
            onClick: () => addToRiskWhitelist(riskItemCtxMenu.key),
          },
        ];
        return <ContextMenu x={riskItemCtxMenu.x} y={riskItemCtxMenu.y} items={items} onClose={() => setRiskItemCtxMenu(null)} width={260} />;
      })()}
      {helpPanelOpen && (
        <div
          style={{
            position: 'absolute', inset: 0,
            background: 'rgba(0,0,0,0.45)',
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            zIndex: 10,
          }}
          onClick={() => setHelpPanelOpen(false)}
        >
          <div
            style={{
              background: 'var(--bg-elevated, #fff)',
              border: '1px solid var(--border)',
              borderRadius: 8,
              padding: '16px 20px',
              maxWidth: 640,
              width: '90%',
              maxHeight: '86%',
              overflow: 'auto',
              boxShadow: '0 8px 32px rgba(0,0,0,0.25)',
              color: 'var(--fg)',
              fontSize: 12,
              lineHeight: 1.55,
            }}
            onClick={(e) => e.stopPropagation()}
          >
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 10 }}>
              <div style={{ fontWeight: 700, fontSize: 14 }}>⌨ 导入向导快捷键</div>
              <button
                style={{
                  background: 'transparent', border: '1px solid var(--border)',
                  borderRadius: 3, color: 'var(--muted)', cursor: 'pointer',
                  padding: '2px 8px', fontSize: 11,
                }}
                onClick={() => setHelpPanelOpen(false)}
                title="关闭 (Esc / Ctrl+/)"
              >✕</button>
            </div>
            {(() => {
              const renderRow = (combo: string, desc: string, color?: string) => (
                <div style={{ display: 'flex', gap: 10, padding: '2px 0', borderBottom: '1px dotted var(--border)' }}>
                  <code style={{
                    flexShrink: 0, minWidth: 130,
                    fontFamily: 'var(--mono, monospace)',
                    fontSize: 11, padding: '1px 6px',
                    background: color ?? 'rgba(59,130,246,0.08)',
                    border: `1px solid ${color ? 'transparent' : 'var(--border)'}`,
                    borderRadius: 3, color: 'var(--fg)',
                  }}>{combo}</code>
                  <span style={color ? { color } : undefined}>{desc}</span>
                </div>
              );
              const sectionTitle = (t: string) => (
                <div style={{ fontWeight: 700, fontSize: 11, color: 'var(--muted)', margin: '10px 0 4px', letterSpacing: 0.4 }}>
                  {t}
                </div>
              );
              return (
                <>
                  {sectionTitle('向导步导航（所有步）')}
                  {renderRow('Ctrl+Alt+1', '粘贴或选择数据（Step 1）')}
                  {renderRow('Ctrl+Alt+2', '目标表 & 模式（Step 2）')}
                  {renderRow('Ctrl+Alt+3', '列映射 & 转换（Step 3）')}
                  {renderRow('Ctrl+Alt+4', '预览 & 执行（Step 4）')}
                  {renderRow('Ctrl+Alt+[', '上一步（无前置约束）')}
                  {renderRow('Ctrl+Alt+]', '下一步（复用 canNav 前置守卫）')}

                  {sectionTitle('Step 4 提交')}
                  {renderRow('Ctrl+Enter', '开始导入（等同点击「开始导入」按钮；禁用条件一致）')}

                  {sectionTitle('Step 3 列映射视图（step === \'map\'）')}
                  {renderRow('Ctrl+F', '聚焦列搜索框')}
                  {renderRow('Alt+0', '清空搜索')}
                  {renderRow('Alt+1', '过滤：全部')}
                  {renderRow('Alt+2', '过滤：未映射')}
                  {renderRow('Alt+3', '过滤：已映射')}
                  {renderRow('Alt+4', '过滤：低健康（<80）')}
                  {renderRow('Alt+5', '过滤：紧急（<60）')}
                  {renderRow('Alt+R', '重置视图（过滤器+排序+搜索）')}

                  {sectionTitle('Step 3 列映射跳转（step === \'map\'）')}
                  {renderRow('Alt+N', '跳到下一个未映射列')}
                  {renderRow('Alt+P', '跳到上一个未映射列')}
                  {renderRow('Alt+H', '跳到首个未映射列')}
                  {renderRow('Alt+L', '跳到末位未映射列')}
                  {renderRow('Ctrl+J', '跳到下一个低健康行')}
                  {renderRow('Ctrl+K', '跳到上一个低健康行')}

                  {sectionTitle('通用')}
                  {renderRow('Ctrl+/', '开关本面板', 'rgba(16,185,129,0.14)')}
                  {renderRow('Esc', '关闭本面板', 'rgba(16,185,129,0.14)')}

                  <div style={{ marginTop: 10, fontSize: 11, color: 'var(--muted)', fontStyle: 'italic' }}>
                    提示：所有快捷键在 INPUT/TEXTAREA/SELECT/contenteditable 聚焦时自动放行，避免打断输入。
                  </div>
                </>
              );
            })()}
          </div>
        </div>
      )}
      {presetManagerOpen && (() => {
        const backup = exportPresetsBackup();
        const presetCount = backup.presets.length;
        const snapshotGroups = Object.keys(backup.snapshots).length;
        const snapshotTotal = Object.values(backup.snapshots).reduce((s, arr) => s + (arr?.length ?? 0), 0);
        const handleExport = () => {
          const b = exportPresetsBackup();
          // M30.157 D 导出时把当前白名单并入备份载荷（v2 结构，与主白名单存储一致）
          if (riskWhitelist.size > 0) {
            const items = Array.from(riskWhitelist.entries())
              .sort((a, b2) => a[0].localeCompare(b2[0]))
              .map(([key, addedAt]) => ({ key, addedAt }));
            b.riskWhitelist = { v: 2, items };
          }
          const json = JSON.stringify(b, null, 2);
          const now = new Date();
          const ts = `${now.getFullYear()}-${String(now.getMonth()+1).padStart(2,'0')}-${String(now.getDate()).padStart(2,'0')}_${String(now.getHours()).padStart(2,'0')}${String(now.getMinutes()).padStart(2,'0')}${String(now.getSeconds()).padStart(2,'0')}`;
          const filename = `polydb-presets-${ts}.json`;
          const blob = new Blob([json], { type: 'application/json' });
          const url = URL.createObjectURL(blob);
          const a = document.createElement('a');
          a.href = url;
          a.download = filename;
          document.body.appendChild(a);
          a.click();
          document.body.removeChild(a);
          setTimeout(() => URL.revokeObjectURL(url), 1000);
          const wlPart = b.riskWhitelist ? ` · 🔒 ${b.riskWhitelist.items.length} 白名单` : '';
          setPresetManagerMsg({ kind: 'ok', text: `📦 已导出备份 ${filename}（${presetCount} 个预设 · ${snapshotTotal} 条快照 / ${snapshotGroups} 组${wlPart} · ${json.length} 字符）` });
          publishEditorStatus({ message: `📦 已导出预设备份（${presetCount} 预设 / ${snapshotTotal} 快照${wlPart}）`, messageAt: Date.now() });
        };
        const handleImportFile = async (file: File) => {
          setPresetManagerBusy(true);
          setPresetManagerMsg(null);
          try {
            const text = await file.text();
            let backup: PresetBackup;
            try {
              backup = JSON.parse(text) as PresetBackup;
            } catch (e) {
              setPresetManagerMsg({ kind: 'err', text: `❌ JSON 解析失败：${(e as Error).message}` });
              return;
            }
            if (!backup || typeof backup !== 'object' || !Array.isArray(backup.presets)) {
              setPresetManagerMsg({ kind: 'err', text: '❌ 文件结构无效：缺少 presets 数组（不是 polydb 预设备份）' });
              return;
            }
            // M30.118 走预检：先展示 diff，用户勾选后确认
            const rawItems = previewBackup(backup);
            // M30.155 B：白名单自动跳过——过滤掉已在 riskWhitelist 中的 key，不再出现在勾选列表中
            const items = riskWhitelist.size > 0
              ? rawItems.filter((it) => !riskWhitelist.has(it.key))
              : rawItems;
            const whitelistSkipped = rawItems.length - items.length;
            if (items.length === 0) {
              const reason = rawItems.length === 0
                ? '⚠ 备份为空（无预设也无快照）'
                : `⚠ 全部 ${rawItems.length} 项已在白名单中，自动跳过。可先在「🔒 白名单」浮层移除对应项。`;
              setPresetManagerMsg({ kind: 'err', text: reason });
              return;
            }
            setBackupPending({ backup, items });
            // M30.155 B：记录白名单跳过数，在预检顶部显示
            setBackupWhitelistSkipped(whitelistSkipped);
            // M30.157 D 备份文件内嵌白名单：非空时展示 chip + 应用/忽略按钮（不主动应用）
            setBackupEmbeddedWhitelist(
              backup.riskWhitelist && Array.isArray(backup.riskWhitelist.items) && backup.riskWhitelist.items.length > 0
                ? backup.riskWhitelist.items.filter((e) => e && typeof e === 'object' && typeof e.key === 'string' && typeof e.addedAt === 'number')
                : null,
            );
            // M30.147 D 记录源文件名供元信息卡显示
            setBackupSourceFileName(file.name);
            // M30.150 B 异步计算 SHA-256 前 8 位作为文件指纹（Web Crypto，非阻塞 UI）
            setBackupSourceHash(null);
            void (async () => {
              try {
                if (typeof crypto !== 'undefined' && crypto.subtle && crypto.subtle.digest) {
                  const buf = await file.arrayBuffer();
                  const digest = await crypto.subtle.digest('SHA-256', buf);
                  const bytes = new Uint8Array(digest);
                  let hex = '';
                  for (let i = 0; i < 8; i++) hex += bytes[i].toString(16).padStart(2, '0');
                  setBackupSourceHash(hex);
                }
              } catch { /* Web Crypto 不可用（非 HTTPS）或 File 读失败时静默 */ }
            })();
            // M30.145 B 备份预检内联审计：预设项自动扫一次目标表/列存活
            setBackupItemAudit(null);
            backupItemAuditRef.current = new Set();
            setBackupItemAuditBusy(false);
            void (async () => {
              const toAudit = items.filter((it) => it.kind === 'preset');
              if (toAudit.length === 0) return;
              setBackupItemAuditBusy(true);
              // M30.146 B：审计缓存复用 — 备份来自当前 connId 且缓存未过期则复用 status，只补缺失项
              const cache = loadAuditCache(connId);
              const cacheHit: Record<string, PresetAuditStatus> = {};
              const toRefresh: typeof toAudit = [];
              for (const it of toAudit) {
                const parts = it.key.split('::');
                if (parts.length < 3) continue;
                const [bk] = parts;
                if (bk !== connId) continue;
                const cached = cache?.results[it.key];
                if (cached) cacheHit[it.key] = cached.status;
                else toRefresh.push(it);
              }
              if (toRefresh.length > 0) {
                const next: Record<string, PresetAuditStatus> = {};
                const patch: Record<string, { status: PresetAuditStatus; reason: string }> = {};
                for (const it of toRefresh) {
                  try {
                    await api.listColumns(connId, it.schema, it.table);
                    next[it.key] = 'ok';
                    patch[it.key] = { status: 'ok', reason: '表/列存活' };
                  } catch {
                    next[it.key] = 'dead';
                    patch[it.key] = { status: 'dead', reason: '表或列不存在' };
                  }
                }
                setBackupItemAudit((prev) => ({ ...(prev ?? {}), ...cacheHit, ...next }));
                // 回写缓存：读取现有 → 合并 patch → 写回
                const existing = loadAuditCache(connId);
                const merged: Record<string, { status: PresetAuditStatus; reason: string }> = {};
                for (const [k, v] of Object.entries(existing?.results ?? {})) {
                  merged[k] = { status: v.status, reason: v.reason };
                }
                for (const [k, v] of Object.entries(patch)) merged[k] = v;
                saveAuditCache(connId, merged);
              } else {
                setBackupItemAudit((prev) => ({ ...(prev ?? {}), ...cacheHit }));
              }
              setBackupItemAuditBusy(false);
            })();
            // M30.148 A 智能默认勾选：跨连接/大 diff 覆盖跳过；dead 由后续 M30.145 B 审计 uncheck
            const initialSel = new Set<string>();
            let skippedCross = 0, skippedBigDiff = 0;
            for (const i of items) {
              const parts = i.key.split('::');
              const cross = parts[0] && parts[0] !== connId;
              if (cross) { skippedCross++; continue; }
              let dc = 0;
              if (i.action === 'overwrite' && i.diff) {
                const d = i.diff;
                dc = d.scalarDiffs.length + d.removedTargets.length + d.addedTargets.length
                  + Object.keys(d.removedCols).length + Object.keys(d.addedCols).length;
              }
              if (i.action === 'overwrite' && dc > 3) { skippedBigDiff++; continue; }
              initialSel.add(i.key);
            }
            setBackupSelected(initialSel);
            const adds = items.filter((i) => i.action === 'add').length;
            const overwrites = items.filter((i) => i.action === 'overwrite').length;
            const smartNote = (skippedCross + skippedBigDiff > 0)
              ? `（已自动跳过：${skippedCross} 跨连接 · ${skippedBigDiff} 大改动覆盖）`
              : '（全部低风险，已默认勾选）';
            setPresetManagerMsg({ kind: 'ok', text: `🔍 预检：+${adds} 新增 · ${overwrites} 覆盖（共 ${items.length} 项）。${smartNote} 手动调整后点「✅ 应用」执行导入。` });
          } catch (e) {
            setPresetManagerMsg({ kind: 'err', text: `❌ 导入失败：${(e as Error).message}` });
          } finally {
            setPresetManagerBusy(false);
            if (presetManagerFileRef.current) presetManagerFileRef.current.value = '';
          }
        };
        return (
          <div
            style={{
              position: 'absolute', inset: 0,
              background: 'rgba(0,0,0,0.45)',
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              zIndex: 10,
            }}
            onClick={() => { if (!presetManagerBusy) setPresetManagerOpen(false); }}
          >
            <div
              style={{
                background: 'var(--bg-elevated, #fff)',
                border: '1px solid var(--border)',
                borderRadius: 8,
                padding: '16px 20px',
                maxWidth: 560,
                width: '90%',
                boxShadow: '0 8px 32px rgba(0,0,0,0.25)',
                color: 'var(--fg)',
                fontSize: 12,
                lineHeight: 1.55,
              }}
              onClick={(e) => e.stopPropagation()}
            >
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 10 }}>
                <div style={{ fontWeight: 700, fontSize: 14 }}>📦 预设管理</div>
                <button onClick={() => setPresetManagerOpen(false)} style={{ ...styles.btnSm, padding: '1px 8px', fontSize: 11 }} title="关闭">✕</button>
              </div>
              <div style={{
                padding: '8px 10px', borderRadius: 4,
                background: 'rgba(59,130,246,0.08)',
                border: '1px solid rgba(59,130,246,0.25)',
                fontSize: 11, color: 'var(--muted)', marginBottom: 12,
              }}>
                当前浏览器存储：<code style={{ color: 'var(--fg)' }}>{presetCount}</code> 个预设 · <code style={{ color: 'var(--fg)' }}>{snapshotTotal}</code> 条快照 / <code style={{ color: 'var(--fg)' }}>{snapshotGroups}</code> 组。备份文件可跨浏览器/设备迁移。
              </div>

              <div style={{ fontWeight: 700, fontSize: 11, color: 'var(--muted)', letterSpacing: 0.4, marginBottom: 4 }}>📤 导出备份</div>
              <div style={{ display: 'flex', gap: 6, marginBottom: 14, alignItems: 'center' }}>
                <button
                  onClick={handleExport}
                  disabled={presetCount === 0 && snapshotTotal === 0}
                  style={{ ...styles.btnSm, padding: '4px 10px', fontSize: 11, color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)' }}
                  title="把全部预设和快照导出为 JSON 文件（可跨设备备份/迁移）"
                >⬇ 导出 JSON</button>
                <span style={{ color: 'var(--muted)', fontSize: 10 }}>包含全部连接的所有预设（非仅当前连接）</span>
              </div>

              <div style={{ fontWeight: 700, fontSize: 11, color: 'var(--muted)', letterSpacing: 0.4, marginBottom: 4 }}>📥 导入备份</div>
              <div style={{ display: 'flex', gap: 6, marginBottom: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                <button
                  onClick={() => presetManagerFileRef.current?.click()}
                  disabled={presetManagerBusy}
                  style={{ ...styles.btnSm, padding: '4px 10px', fontSize: 11, color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)' }}
                  title="选择之前导出的预设备份 JSON 文件，恢复预设和快照"
                >⬆ 选择 JSON 文件…</button>
                <input
                  ref={presetManagerFileRef}
                  type="file"
                  accept=".json,application/json"
                  style={{ display: 'none' }}
                  onChange={(e) => {
                    const f = e.target.files?.[0];
                    if (f) void handleImportFile(f);
                  }}
                />
                <label style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 11, color: 'var(--muted)', cursor: 'pointer' }}>
                  <input
                    type="checkbox"
                    checked={presetManagerOverwrite}
                    onChange={(e) => setPresetManagerOverwrite(e.target.checked)}
                  />
                  覆盖已存在的预设（关闭则跳过）
                </label>
              </div>
              <div style={{ fontSize: 10, color: 'var(--muted)', fontStyle: 'italic', marginBottom: 8 }}>
                快照组始终覆盖（快照本身是历史副本，覆盖语义安全）；预设覆盖时会覆盖主 preset 及映射。
              </div>

              {/* M30.155 A：白名单管理入口 —— 可视化 polydb.riskWhitelist.v1，单项删除/清空全部 */}
              <div style={{ display: 'flex', gap: 6, marginBottom: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                <button
                  type="button"
                  onClick={() => setRiskWhitelistOpen(true)}
                  style={{ ...styles.btnSm, padding: '4px 10px', fontSize: 11, color: 'var(--info, #8b5cf6)', borderColor: 'var(--info, #8b5cf6)' }}
                  title="高风险项白名单：加入后下次备份预检自动跳过。可在此管理/清空。"
                >🔒 白名单 ({riskWhitelist.size})</button>
                {/* M30.161 D presetManager 头「🧹 过期清理」全局按钮 */}
                {(() => {
                  const now = Date.now();
                  let expiredCount = 0;
                  for (const [, addedAt] of riskWhitelist) {
                    if (now - addedAt > WHITELIST_TTL_MS) expiredCount += 1;
                  }
                  if (expiredCount === 0) return null;
                  const cleanExpired = () => {
                    const t = Date.now();
                    const next = new Map(riskWhitelist);
                    for (const [k, addedAt] of Array.from(next)) {
                      if (t - addedAt > WHITELIST_TTL_MS) next.delete(k);
                    }
                    setRiskWhitelist(next);
                    try {
                      const payload: WhitelistPersisted = { v: 2, items: Array.from(next, ([key, a]) => ({ key, addedAt: a })) };
                      localStorage.setItem('polydb.riskWhitelist.v1', JSON.stringify(payload));
                    } catch { /* ignore */ }
                    setPresetManagerMsg({ kind: 'ok', text: `🧹 已清理 ${expiredCount} 项过期白名单（>30 天未使用）` });
                  };
                  return (
                    <button
                      type="button"
                      onClick={cleanExpired}
                      style={{ ...styles.btnSm, padding: '4px 10px', fontSize: 11, color: 'var(--warn, #d97706)', borderColor: 'rgba(217,119,6,0.5)', background: 'rgba(217,119,6,0.08)' }}
                      title={`有 ${expiredCount} 项白名单已超 30 天 · 点击立即清理（打开白名单浮层也可清理）`}
                    >🧹 过期清理 ({expiredCount})</button>
                  );
                })()}
                {riskWhitelist.size > 0 && (
                  <span style={{ fontSize: 10, color: 'var(--muted)' }}>下次备份预检自动跳过这些高风险项</span>
                )}
              </div>

              {pendingBackupUndo && backupUndoLeft > 0 && (
                <div style={{
                  padding: '6px 10px', borderRadius: 4, fontSize: 11,
                  display: 'flex', alignItems: 'center', gap: 8,
                  background: 'rgba(217,119,6,0.10)',
                  border: '1px solid rgba(217,119,6,0.40)',
                  color: 'var(--warn, #d97706)',
                  marginBottom: 6, position: 'relative', overflow: 'hidden',
                }}>
                  {/* M30.151 B：撤销倒计时视觉进度条（5s 线性递减，100%→0%） */}
                  <div
                    style={{
                      position: 'absolute', bottom: 0, left: 0, height: 2,
                      width: `${Math.max(0, (backupUndoLeft / 5) * 100)}%`,
                      background: 'linear-gradient(90deg, var(--warn, #d97706) 0%, var(--info, #3b82f6) 100%)',
                      transition: 'width 1s linear',
                    }}
                    title={`撤销窗口剩余 ${backupUndoLeft}s / 5s`}
                  />
                  <span style={{ flex: 1 }}>
                    ↩️ 备份应用已生效 · 剩余 <strong>{backupUndoLeft}s</strong> 可撤销
                    {lastApplyAddedPresets !== null && lastApplyAddedPresets > 0 && (
                      <span style={{ display: 'block', color: 'var(--info, #8b5cf6)', fontSize: 10, marginTop: 2, fontWeight: 400 }} title="新增预设可能指向已被删除的表/列，建议对全部预设运行健康审计">
                        🎯 新增 {lastApplyAddedPresets} 个预设，建议「🔍 审计全部」检查目标表/列是否仍有效
                      </span>
                    )}
                    {lastApplyAuditSummary && lastApplyAuditSummary.applied > 0 && (() => {
                      const s = lastApplyAuditSummary;
                      const pct = Math.round((s.ok / s.applied) * 100);
                      const color = s.dead === 0 ? 'var(--success, #10b981)' : s.ok === 0 ? 'var(--danger, #dc2626)' : 'var(--warn, #d97706)';
                      return (
                        <span
                          style={{ display: 'block', color, fontSize: 10, marginTop: 2, fontWeight: 600 }}
                          title="备份应用后自动审计新增/覆盖预设的目标表/列是否仍有效"
                        >
                          🩺 自动审计 {s.ok}/{s.applied} 存活（{pct}%）{s.dead > 0 ? ` · ❌ ${s.dead} 失效需处理` : ' · ✅ 全通过'}
                        </span>
                      );
                    })()}
                    {pendingBackupUndoImpact && (() => {
                      const p = pendingBackupUndoImpact.impact;
                      const total = p.presetsRemoved + p.presetsRestored + p.snapshotsRemoved + p.snapshotsRestored;
                      if (total === 0) return null;
                      const drill = (label: string, n: number, keys: string[], title: string) => {
                        if (n === 0) return null;
                        return (
                          <button
                            type="button"
                            onClick={(ev) => { ev.stopPropagation(); setUndoImpactDrillOpen(true); }}
                            style={{
                              background: 'transparent', border: 'none', padding: 0,
                              color: 'var(--info, #8b5cf6)', cursor: 'pointer',
                              textDecoration: 'underline dotted',
                              fontSize: 10, font: 'inherit',
                              fontWeight: 600,
                            }}
                            title={`${title}（点击查看详情：${keys.slice(0, 3).join(', ')}${keys.length > 3 ? ` 等 ${keys.length} 项` : ''}）`}
                          >
                            {label} {n}
                          </button>
                        );
                      };
                      const parts: ReactNode[] = [];
                      if (p.presetsRemoved > 0) {
                        parts.push(drill('移除', p.presetsRemoved, pendingBackupUndoImpact.presetsRemovedKeys, '撤销将删除的预设'));
                        parts.push(<span> 预设 ·</span>);
                      }
                      if (p.presetsRestored > 0) {
                        parts.push(drill('还原', p.presetsRestored, pendingBackupUndoImpact.presetsRestoredKeys.map((x) => x.key), '撤销将回到旧版本的预设'));
                        parts.push(<span> 预设 ·</span>);
                      }
                      if (p.snapshotsRemoved > 0) {
                        parts.push(drill('移除', p.snapshotsRemoved, pendingBackupUndoImpact.snapshotsRemovedKeys, '撤销将删除的快照组'));
                        parts.push(<span> 快照组 ·</span>);
                      }
                      if (p.snapshotsRestored > 0) {
                        parts.push(drill('还原', p.snapshotsRestored, pendingBackupUndoImpact.snapshotsRestoredKeys, '撤销将回到旧版本的快照组'));
                        parts.push(<span> 快照组</span>);
                      }
                      return (
                        <span
                          style={{ display: 'block', color: 'var(--muted)', fontSize: 10, marginTop: 2, fontWeight: 400 }}
                          title="撤销将恢复到应用前的状态：新增项消失、被覆盖项回到旧版本"
                        >
                          ⚖ 撤销影响：{parts}
                        </span>
                      );
                    })()}
                  </span>
                  {pendingBackupUndoImpact && (() => {
                    // M30.153 C：一键复制撤销影响结构化摘要到剪贴板
                    const impact = pendingBackupUndoImpact;
                    const p = impact.impact;
                    const lines: string[] = [];
                    lines.push(`# polydb 备份应用撤销摘要`);
                    lines.push(`时间：${new Date().toISOString()}`);
                    lines.push(`连接：${connId}`);
                    lines.push(`合计：移除预设 ${p.presetsRemoved} · 还原预设 ${p.presetsRestored} · 移除快照 ${p.snapshotsRemoved} · 还原快照 ${p.snapshotsRestored}`);
                    if (impact.presetsRemovedKeys.length > 0) {
                      lines.push('', `## 移除预设（${impact.presetsRemovedKeys.length}）`);
                      for (const k of impact.presetsRemovedKeys) lines.push(`- ${k}`);
                    }
                    if (impact.presetsRestoredKeys.length > 0) {
                      lines.push('', `## 还原预设（${impact.presetsRestoredKeys.length}）`);
                      for (const x of impact.presetsRestoredKeys) {
                        lines.push(`- ${x.key} · ${new Date(x.prevUpdatedAt).toISOString()} → ${new Date(x.nowUpdatedAt).toISOString()}`);
                      }
                    }
                    if (impact.snapshotsRemovedKeys.length > 0) {
                      lines.push('', `## 移除快照组（${impact.snapshotsRemovedKeys.length}）`);
                      for (const k of impact.snapshotsRemovedKeys) lines.push(`- ${k}`);
                    }
                    if (impact.snapshotsRestoredKeys.length > 0) {
                      lines.push('', `## 还原快照组（${impact.snapshotsRestoredKeys.length}）`);
                      for (const k of impact.snapshotsRestoredKeys) lines.push(`- ${k}`);
                    }
                    const previewText = lines.join('\n');
                    return (
                      <span style={{ display: 'inline-flex', gap: 2 }}>
                        <button
                          type="button"
                          onClick={() => setBackupUndoPreviewText(previewText)}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10 }}
                          title="先预览撤销摘要内容再决定是否复制"
                        >👁 预览</button>
                        <button
                          type="button"
                          onClick={() => {
                            navigator.clipboard?.writeText(previewText).then(
                              () => setPresetManagerMsg({ kind: 'ok', text: `📋 已复制撤销摘要（${lines.length} 行）` }),
                              () => setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败（浏览器拒绝访问剪贴板）' }),
                            );
                          }}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10 }}
                          title="把撤销影响结构化摘要复制到剪贴板（Markdown 格式）"
                        >📋 复制摘要</button>
                      </span>
                    );
                  })()}
                  <span style={{ display: 'inline-flex', gap: 2 }}>
                    {(['json', 'csv', 'md'] as const).map((fmt) => (
                      <button
                        key={fmt}
                        type="button"
                        onClick={() => {
                          const impact = pendingBackupUndoImpact;
                          if (!impact) return;
                          const now = new Date();
                          const ts = `${now.getFullYear()}-${String(now.getMonth()+1).padStart(2,'0')}-${String(now.getDate()).padStart(2,'0')}_${String(now.getHours()).padStart(2,'0')}${String(now.getMinutes()).padStart(2,'0')}${String(now.getSeconds()).padStart(2,'0')}`;
                          const download = (content: string, mime: string, name: string) => {
                            const blob = new Blob([content], { type: mime });
                            const url = URL.createObjectURL(blob);
                            const a = document.createElement('a');
                            a.href = url; a.download = name;
                            document.body.appendChild(a); a.click(); document.body.removeChild(a);
                            setTimeout(() => URL.revokeObjectURL(url), 1000);
                            setPresetManagerMsg({ kind: 'ok', text: `📊 已导出应用后 diff 报告 ${name}` });
                          };
                          const fmtTs = (t: number) => new Date(t).toISOString();
                          if (fmt === 'json') {
                            download(JSON.stringify({
                              exportedAt: now.toISOString(),
                              connId,
                              counts: impact.impact,
                              presetsRemoved: impact.presetsRemovedKeys,
                              presetsRestored: impact.presetsRestoredKeys.map((x) => ({ key: x.key, prevUpdatedAt: fmtTs(x.prevUpdatedAt), nowUpdatedAt: fmtTs(x.nowUpdatedAt) })),
                              snapshotsRemoved: impact.snapshotsRemovedKeys,
                              snapshotsRestored: impact.snapshotsRestoredKeys,
                            }, null, 2), 'application/json', `polydb-apply-diff-${ts}.json`);
                            return;
                          }
                          if (fmt === 'csv') {
                            const rows: string[] = ['kind,category,key,prevUpdatedAt,nowUpdatedAt'];
                            const esc = (v: string) => /[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v;
                            for (const k of impact.presetsRemovedKeys) rows.push(`preset,removed,${esc(k)},,`);
                            for (const x of impact.presetsRestoredKeys) rows.push(`preset,restored,${esc(x.key)},${x.prevUpdatedAt},${x.nowUpdatedAt}`);
                            for (const k of impact.snapshotsRemovedKeys) rows.push(`snapshot,removed,${esc(k)},,`);
                            for (const k of impact.snapshotsRestoredKeys) rows.push(`snapshot,restored,${esc(k)},,`);
                            download(rows.join('\n') + '\n', 'text/csv', `polydb-apply-diff-${ts}.csv`);
                            return;
                          }
                          const md: string[] = [];
                          md.push(`# polydb 备份应用 diff 报告`);
                          md.push('');
                          md.push(`- **时间**：${now.toISOString()}`);
                          md.push(`- **连接**：\`${connId}\``);
                          md.push(`- **合计**：移除预设 ${impact.impact.presetsRemoved} · 还原预设 ${impact.impact.presetsRestored} · 移除快照 ${impact.impact.snapshotsRemoved} · 还原快照 ${impact.impact.snapshotsRestored}`);
                          md.push('');
                          if (impact.presetsRemovedKeys.length > 0) {
                            md.push('### 撤销将删除的预设');
                            for (const k of impact.presetsRemovedKeys) md.push(`- ${k}`);
                            md.push('');
                          }
                          if (impact.presetsRestoredKeys.length > 0) {
                            md.push('### 撤销将回到旧版本的预设');
                            for (const x of impact.presetsRestoredKeys) md.push(`- ${x.key}（旧 ${fmtTs(x.prevUpdatedAt)} → 现 ${fmtTs(x.nowUpdatedAt)}）`);
                            md.push('');
                          }
                          if (impact.snapshotsRemovedKeys.length > 0) {
                            md.push('### 撤销将删除的快照组');
                            for (const k of impact.snapshotsRemovedKeys) md.push(`- ${k}`);
                            md.push('');
                          }
                          if (impact.snapshotsRestoredKeys.length > 0) {
                            md.push('### 撤销将回到旧版本的快照组');
                            for (const k of impact.snapshotsRestoredKeys) md.push(`- ${k}`);
                            md.push('');
                          }
                          download(md.join('\n') + '\n', 'text/markdown', `polydb-apply-diff-${ts}.md`);
                        }}
                        style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--info, #8b5cf6)' }}
                        title={`导出应用后 diff 报告为 ${fmt.toUpperCase()}（撤销影响详细清单）`}
                      >导出 diff {fmt.toUpperCase()}</button>
                    ))}
                  </span>
                  {/* M30.158 D 备份 apply 后 diff summary Markdown 一键复制：不下载、只把 Markdown 摘要写进剪贴板（比「导出 diff MD」更适合粘贴到 IM/工单） */}
                  {pendingBackupUndoImpact && (() => {
                    const impact = pendingBackupUndoImpact;
                    const p = impact.impact;
                    const total = p.presetsRemoved + p.presetsRestored + p.snapshotsRemoved + p.snapshotsRestored;
                    if (total === 0) return null;
                    const md: string[] = [];
                    md.push('# polydb 备份应用 diff 摘要');
                    md.push('');
                    md.push(`- **时间**：${new Date().toISOString()}`);
                    md.push(`- **连接**：\`${connId}\``);
                    md.push(`- **合计**：移除预设 ${p.presetsRemoved} · 还原预设 ${p.presetsRestored} · 移除快照 ${p.snapshotsRemoved} · 还原快照 ${p.snapshotsRestored}`);
                    if (impact.presetsRemovedKeys.length > 0) {
                      md.push('', '### 移除预设');
                      for (const k of impact.presetsRemovedKeys) md.push(`- \`${k}\``);
                    }
                    if (impact.presetsRestoredKeys.length > 0) {
                      md.push('', '### 还原预设（旧 → 现）');
                      for (const x of impact.presetsRestoredKeys) {
                        md.push(`- \`${x.key}\`：${new Date(x.prevUpdatedAt).toISOString()} → ${new Date(x.nowUpdatedAt).toISOString()}`);
                      }
                    }
                    if (impact.snapshotsRemovedKeys.length > 0) {
                      md.push('', '### 移除快照组');
                      for (const k of impact.snapshotsRemovedKeys) md.push(`- \`${k}\``);
                    }
                    if (impact.snapshotsRestoredKeys.length > 0) {
                      md.push('', '### 还原快照组');
                      for (const k of impact.snapshotsRestoredKeys) md.push(`- \`${k}\``);
                    }
                    const text = md.join('\n') + '\n';
                    return (
                      <button
                        type="button"
                        onClick={() => {
                          void (async () => {
                            try {
                              await navigator.clipboard.writeText(text);
                              setPresetManagerMsg({ kind: 'ok', text: `📋 已复制 diff 摘要（${md.length} 行 Markdown）` });
                            } catch {
                              setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败（浏览器剪贴板权限）' });
                            }
                          })();
                        }}
                        style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--info, #8b5cf6)' }}
                        title="把撤销影响 Markdown 摘要复制到剪贴板（可直接粘贴到 IM/工单；比「导出 diff MD」更轻量）"
                      >📋 复制 diff MD</button>
                    );
                  })()}
                  <button
                    type="button"
                    onClick={applyBackupUndo}
                    disabled={backupUndoLeft <= 0}
                    style={{
                      ...styles.btnSm, padding: '1px 6px', fontSize: 10,
                      opacity: backupUndoLeft <= 0 ? 0.45 : 1,
                      cursor: backupUndoLeft <= 0 ? 'not-allowed' : undefined,
                      transition: 'opacity 0.4s ease',
                    }}
                    title={backupUndoLeft > 0 ? `撤销 5s 内的备份应用（Ctrl+Z）· 剩余 ${backupUndoLeft}s` : '撤销窗口已过期'}
                  >
                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                      撤销 (Ctrl+Z)
                      {/* M30.154 D：monospace T-Ns 键盘风格倒计时，与 M30.151 B 进度条形成双通道视觉 */}
                      <code
                        style={{
                          fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                          fontSize: 9,
                          padding: '0 3px',
                          borderRadius: 2,
                          background: 'rgba(217,119,6,0.15)',
                          color: backupUndoLeft <= 2 ? 'var(--danger, #dc2626)' : 'var(--warn, #d97706)',
                          fontWeight: 700,
                          letterSpacing: '0.02em',
                          transition: 'color 0.3s ease',
                        }}
                        title={`撤销窗口剩余 ${backupUndoLeft}s / 5s`}
                      >
                        T-{backupUndoLeft}s
                      </code>
                    </span>
                  </button>
                </div>
              )}

              {/* M30.147 B 备份应用二次确认浮层：在真正 doApplyBackup 前弹一次做最后把关 */}
              {backupConfirmOverlayOpen && backupPending && (() => {
                const { items } = backupPending;
                const selKeys = backupSelected;
                const selectedAdds = items.filter((i) => selKeys.has(i.key) && i.action === 'add').length;
                const selectedOvers = items.filter((i) => selKeys.has(i.key) && i.action === 'overwrite').length;
                const totalSel = selectedAdds + selectedOvers;
                const skipCount = items.length - totalSel;
                // M30.149 A：撤销影响预览——新增将消失、覆盖将回到旧版本
                const presetAddSel = items.filter((i) => selKeys.has(i.key) && i.action === 'add' && i.kind === 'preset').length;
                const presetOverSel = items.filter((i) => selKeys.has(i.key) && i.action === 'overwrite' && i.kind === 'preset').length;
                const snapAddSel = items.filter((i) => selKeys.has(i.key) && i.action === 'add' && i.kind === 'snapshot').length;
                const snapOverSel = items.filter((i) => selKeys.has(i.key) && i.action === 'overwrite' && i.kind === 'snapshot').length;
                const undoRemoves = presetAddSel + snapAddSel;
                const undoRestores = presetOverSel + snapOverSel;
                const undoTotal = undoRemoves + undoRestores;
                // M30.150 A：变更类型分布直方图——按 diff 数量分 4 档（0/1-3/4-10/11+），仅统计勾选的 overwrite preset
                const histogramBins: { label: string; count: number; color: string; bg: string; desc: string }[] = [
                  { label: '0', count: 0, color: 'var(--success, #10b981)', bg: 'rgba(16,185,129,0.10)', desc: '完全一致' },
                  { label: '1–3', count: 0, color: 'var(--info, #3b82f6)', bg: 'rgba(59,130,246,0.10)', desc: '微调' },
                  { label: '4–10', count: 0, color: 'var(--warn, #d97706)', bg: 'rgba(217,119,6,0.10)', desc: '中等改动' },
                  { label: '11+', count: 0, color: 'var(--danger, #dc2626)', bg: 'rgba(220,38,38,0.10)', desc: '大改' },
                ];
                let histogramTotal = 0;
                let histogramDiffSum = 0;
                for (const i of items) {
                  if (!selKeys.has(i.key) || i.action !== 'overwrite' || i.kind !== 'preset') continue;
                  if (!i.diff) continue;
                  const dc = i.diff.scalarDiffs.length + i.diff.removedTargets.length + i.diff.addedTargets.length
                    + Object.keys(i.diff.removedCols).length + Object.keys(i.diff.addedCols).length;
                  if (dc === 0) histogramBins[0].count++;
                  else if (dc <= 3) histogramBins[1].count++;
                  else if (dc <= 10) histogramBins[2].count++;
                  else histogramBins[3].count++;
                  histogramTotal++;
                  histogramDiffSum += dc;
                }
                // 高风险项：dead 或 diff 变化>3 或 overwrite
                const audit = backupItemAudit ?? {};
                const riskItems = items.filter((i) => {
                  if (!selKeys.has(i.key)) return false;
                  if (audit[i.key] === 'dead') return true;
                  const parts = i.key.split('::');
                  if (parts[0] && parts[0] !== connId) return true;
                  if (i.action === 'overwrite' && i.diff) {
                    const d = i.diff;
                    const dc = d.scalarDiffs.length + d.removedTargets.length + d.addedTargets.length
                      + Object.keys(d.removedCols).length + Object.keys(d.addedCols).length;
                    if (dc > 3) return true;
                  }
                  return false;
                });
                const riskTop = riskItems.slice(0, 4);
                return (
                  <div style={{
                    position: 'absolute', inset: 0,
                    background: 'rgba(0,0,0,0.35)',
                    display: 'flex', alignItems: 'center', justifyContent: 'center',
                    zIndex: 20,
                    padding: 20,
                  }}>
                    <div style={{
                      background: 'var(--bg-elevated, #fff)',
                      border: '1px solid var(--border)',
                      borderRadius: 8,
                      padding: '14px 18px',
                      maxWidth: 480,
                      width: '100%',
                      boxShadow: '0 8px 32px rgba(0,0,0,0.25)',
                      color: 'var(--fg)',
                      fontSize: 12,
                      lineHeight: 1.55,
                    }} onClick={(e) => e.stopPropagation()}>
                      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
                        <div style={{ fontWeight: 700, fontSize: 13, color: 'var(--warn, #d97706)' }}>🛡 确认应用备份？</div>
                        <span style={{ fontSize: 10, color: 'var(--muted)' }} title="Enter 应用 · Esc 取消">Enter ↵ / Esc ✕</span>
                      </div>
                      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 6, marginBottom: 10 }}>
                        <div style={{ padding: '6px 8px', borderRadius: 4, background: 'rgba(16,185,129,0.08)', border: '1px solid rgba(16,185,129,0.3)', textAlign: 'center' }}>
                          <div style={{ fontSize: 16, fontWeight: 700, color: 'var(--success, #10b981)' }}>+{selectedAdds}</div>
                          <div style={{ fontSize: 10, color: 'var(--muted)' }}>新增</div>
                        </div>
                        <div style={{ padding: '6px 8px', borderRadius: 4, background: 'rgba(217,119,6,0.08)', border: '1px solid rgba(217,119,6,0.3)', textAlign: 'center' }}>
                          <div style={{ fontSize: 16, fontWeight: 700, color: 'var(--warn, #d97706)' }}>={selectedOvers}</div>
                          <div style={{ fontSize: 10, color: 'var(--muted)' }}>覆盖</div>
                        </div>
                        <div style={{ padding: '6px 8px', borderRadius: 4, background: 'rgba(148,163,184,0.08)', border: '1px solid var(--border)', textAlign: 'center' }}>
                          <div style={{ fontSize: 16, fontWeight: 700 }}>⊘{skipCount}</div>
                          <div style={{ fontSize: 10, color: 'var(--muted)' }}>跳过</div>
                        </div>
                      </div>
                      {histogramDiffSum >= 20 && (() => {
                        const bigGroups = histogramBins[3].count;
                        const isBig = histogramDiffSum > 50;
                        const color = isBig ? 'var(--danger, #dc2626)' : 'var(--warn, #d97706)';
                        const bg = isBig ? 'rgba(220,38,38,0.08)' : 'rgba(217,119,6,0.08)';
                        const border = isBig ? 'rgba(220,38,38,0.4)' : 'rgba(217,119,6,0.4)';
                        const icon = isBig ? '🔥' : '⚠';
                        const label = isBig ? '大改' : '中改';
                        return (
                          <div
                            style={{ padding: '6px 8px', borderRadius: 4, background: bg, border: `1px solid ${border}`, marginBottom: 8, fontSize: 11, color, fontWeight: 600 }}
                            title="勾选项累计变更规模：>50 处为大改，>20 处为中改"
                          >
                            {icon} 本次将引入 <strong>{histogramDiffSum}</strong> 处变更（{label}）
                            {bigGroups > 0 && <span> · {bigGroups} 个大改组</span>}
                            <span style={{ color: 'var(--muted)', fontWeight: 400, fontSize: 10, marginLeft: 6 }}>· 建议先审高风险项</span>
                          </div>
                        );
                      })()}
                      {histogramTotal > 0 && (() => {
                        const maxBin = Math.max(1, ...histogramBins.map((b) => b.count));
                        // M30.153 A：环形小图（4 档同色，圆心总变更），SVG 40×40 挂在直方图标题右侧
                        const donutBins = histogramBins.filter((b) => b.count > 0);
                        const donutTotal = donutBins.reduce((s, b) => s + b.count, 0);
                        const R = 15, r = 9, C = 20;
                        let acc = 0;
                        const donutSegs: { d: string; color: string }[] = [];
                        for (const b of donutBins) {
                          const frac = donutTotal === 0 ? 0 : b.count / donutTotal;
                          const startA = acc * Math.PI * 2 - Math.PI / 2;
                          const endA = (acc + frac) * Math.PI * 2 - Math.PI / 2;
                          acc += frac;
                          if (frac >= 0.999) {
                            // 单档占满 → 画完整环
                            donutSegs.push({
                              d: `M ${C} ${C - R} A ${R} ${R} 0 1 1 ${C - 0.01} ${C - R} Z M ${C} ${C - r} A ${r} ${r} 0 1 0 ${C - 0.01} ${C - r} Z`,
                              color: b.color,
                            });
                            continue;
                          }
                          const large = frac > 0.5 ? 1 : 0;
                          const x1 = C + R * Math.cos(startA), y1 = C + R * Math.sin(startA);
                          const x2 = C + R * Math.cos(endA), y2 = C + R * Math.sin(endA);
                          const xi1 = C + r * Math.cos(endA), yi1 = C + r * Math.sin(endA);
                          const xi2 = C + r * Math.cos(startA), yi2 = C + r * Math.sin(startA);
                          const d = `M ${x1} ${y1} A ${R} ${R} 0 ${large} 1 ${x2} ${y2} L ${xi1} ${yi1} A ${r} ${r} 0 ${large} 0 ${xi2} ${yi2} Z`;
                          donutSegs.push({ d, color: b.color });
                        }
                        return (
                          <div
                            style={{ padding: '6px 8px', borderRadius: 4, background: 'rgba(0,0,0,0.02)', border: '1px solid var(--border)', marginBottom: 8 }}
                            title="勾选的覆盖预设按变更数量分档分布"
                          >
                            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', fontSize: 9, color: 'var(--muted)', marginBottom: 3 }}>
                              <span>📊 覆盖项变更分布（{histogramTotal}）</span>
                              <span title={`勾选项累计变更 ${histogramDiffSum} 处`}>
                                Σ⇄ <strong style={{ color: 'var(--fg)' }}>{histogramDiffSum}</strong>
                              </span>
                            </div>
                            <div style={{ display: 'grid', gridTemplateColumns: '44px 1fr', gap: 8, alignItems: 'center' }}>
                              <svg width="44" height="44" viewBox="0 0 40 40" style={{ display: 'block', margin: '0 auto' }} aria-label="变更分布环形图">
                                {donutSegs.length === 0 && <circle cx={C} cy={C} r={(R + r) / 2} fill="none" stroke="var(--border)" strokeWidth={R - r} />}
                                {donutSegs.map((s, idx) => (
                                  <path key={idx} d={s.d} fill={s.color} fillRule="evenodd" opacity={0.85} />
                                ))}
                                <text x={C} y={C + 3.5} textAnchor="middle" fontSize="8" fontWeight="700" fill="var(--fg)" fontFamily="monospace">{histogramDiffSum}</text>
                              </svg>
                              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 4 }}>
                                {histogramBins.map((b, bIdx) => {
                                  const binKey = String(bIdx) as '0' | '1' | '2' | '3';
                                  const isFiltered = backupDiffBinFilter === binKey;
                                  const pct = b.count === 0 ? 0 : (b.count / maxBin) * 100;
                                  return (
                                    <div
                                      key={b.label}
                                      onClick={() => {
                                        setBackupDiffBinFilter(isFiltered ? 'all' : binKey);
                                        setBackupConfirmOverlayOpen(false);
                                      }}
                                      style={{
                                        padding: '3px 4px', borderRadius: 3,
                                        background: b.bg,
                                        border: `1px solid ${isFiltered ? b.color : b.color === 'var(--success, #10b981)' ? 'rgba(16,185,129,0.3)' : b.color === 'var(--info, #3b82f6)' ? 'rgba(59,130,246,0.3)' : b.color === 'var(--warn, #d97706)' ? 'rgba(217,119,6,0.3)' : 'rgba(220,38,38,0.3)'} ${isFiltered ? 2 : 1}px`,
                                        textAlign: 'center', position: 'relative', minHeight: 30,
                                        cursor: b.count > 0 ? 'pointer' : 'default',
                                        opacity: b.count === 0 ? 0.5 : 1,
                                      }}
                                      title={`${b.label} 处变更：${b.count} 项（${b.desc}）${b.count > 0 ? ' · 点击筛选预检列表此档项，再点复位' : ''}`}
                                    >
                                      <div style={{ fontSize: 11, fontWeight: 700, color: b.color, lineHeight: 1.2 }}>{b.count}</div>
                                      <div style={{ fontSize: 9, color: isFiltered ? b.color : 'var(--muted)', fontWeight: isFiltered ? 700 : 400 }}>{isFiltered ? '✓ ⇄' : '⇄'}{b.label}</div>
                                      {pct > 0 && (
                                        <div style={{
                                          position: 'absolute', bottom: 2, left: 2, right: 2,
                                          height: 2, borderRadius: 1,
                                          background: 'rgba(0,0,0,0.08)',
                                        }}>
                                          <div style={{ height: '100%', width: `${pct}%`, background: b.color, borderRadius: 1, opacity: 0.75 }} />
                                        </div>
                                      )}
                                    </div>
                                  );
                                })}
                              </div>
                            </div>
                          </div>
                        );
                      })()}
                      {undoTotal > 0 && (
                        <div
                          style={{ padding: '6px 8px', borderRadius: 4, background: 'rgba(139,92,246,0.06)', border: '1px solid rgba(139,92,246,0.3)', marginBottom: 8, fontSize: 10, color: 'var(--muted)' }}
                          title="若应用后撤销，新增项将消失、被覆盖项将回到旧版本（5s 内 Ctrl+Z）"
                        >
                          ⚖ 撤销将影响：
                          <span style={{ color: 'var(--info, #8b5cf6)', fontWeight: 600 }}>移除 {undoRemoves}</span>
                          {undoRemoves > 0 && <span> · </span>}
                          <span style={{ color: 'var(--info, #8b5cf6)', fontWeight: 600 }}>还原 {undoRestores}</span>
                          <span style={{ marginLeft: 6 }}>({presetAddSel + presetOverSel} 预设 / {snapAddSel + snapOverSel} 快照组)</span>
                        </div>
                      )}
                      {riskItems.length > 0 && (
                        <div style={{ padding: '6px 8px', borderRadius: 4, background: 'rgba(220,38,38,0.06)', border: '1px solid rgba(220,38,38,0.3)', marginBottom: 8 }}>
                          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 6, marginBottom: 4 }}>
                            <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--danger, #dc2626)' }}>
                              ⚠ 高风险项 {riskItems.length} 个（跨连接/失效/大改动）
                            </div>
                            <button
                              type="button"
                              onClick={() => {
                                const removed = riskItems.length;
                                setBackupSelected((prev) => {
                                  const next = new Set(prev);
                                  for (const r of riskItems) next.delete(r.key);
                                  return next;
                                });
                                setPresetManagerMsg({ kind: 'ok', text: `⚡ 已剔除 ${removed} 个高风险项，重新确认后应用` });
                              }}
                              style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)' }}
                              title="把这些高风险项从勾选状态移除"
                            >⚡ 排除全部（{riskItems.length}）</button>
                          </div>
                          {riskTop.length > 0 && (
                            <div style={{ fontSize: 10, color: 'var(--muted)', fontFamily: 'var(--mono, monospace)', lineHeight: 1.5 }}>
                              {riskTop.map((r) => {
                                const parts = r.key.split('::');
                                const dead = audit[r.key] === 'dead';
                                const cross = parts[0] && parts[0] !== connId;
                                let dc = 0;
                                if (r.diff) dc = r.diff.scalarDiffs.length + r.diff.removedTargets.length + r.diff.addedTargets.length + Object.keys(r.diff.removedCols).length + Object.keys(r.diff.addedCols).length;
                                const tags: string[] = [];
                                if (dead) tags.push('❌ 失效');
                                if (cross) tags.push('🔗 跨连接');
                                if (dc > 3) tags.push(`⇄ ${dc} 变更`);
                                return (
                                  <div
                                    key={r.key}
                                    style={{ padding: '1px 0', cursor: 'context-menu' }}
                                    onContextMenu={(e) => {
                                      e.preventDefault();
                                      e.stopPropagation();
                                      setRiskItemCtxMenu({ x: e.clientX, y: e.clientY, key: r.key });
                                    }}
                                    title="右键菜单：复制 key / 单独剔除 / 加入白名单"
                                  >
                                    · <code>{r.schema}.{r.table}</code> {tags.join(' ')}
                                  </div>
                                );
                              })}
                              {riskItems.length > riskTop.length && (
                                <div style={{ color: 'var(--muted)', fontSize: 9 }}>… 另有 {riskItems.length - riskTop.length} 项</div>
                              )}
                            </div>
                          )}
                        </div>
                      )}
                      <div
                        style={{
                          display: 'flex', gap: 10, flexWrap: 'wrap',
                          padding: '6px 8px', borderRadius: 3, fontSize: 10,
                          background: 'rgba(0,0,0,0.03)', border: '1px solid var(--border)',
                          color: 'var(--muted)', marginBottom: 8,
                          fontFamily: 'var(--mono, monospace)',
                        }}
                        title="本次操作的规模汇总"
                      >
                        <span>Σ 总计 <strong style={{ color: 'var(--fg)' }}>{items.length}</strong> 项</span>
                        <span style={{ color: 'var(--border)' }}>·</span>
                        <span>勾 <strong style={{ color: 'var(--success, #10b981)' }}>{totalSel}</strong> 项</span>
                        <span style={{ color: 'var(--border)' }}>·</span>
                        <span>⇄ <strong style={{ color: 'var(--fg)' }}>{histogramDiffSum}</strong> 变更</span>
                        {histogramBins[3].count > 0 && (
                          <>
                            <span style={{ color: 'var(--border)' }}>·</span>
                            <span style={{ color: 'var(--danger, #dc2626)' }}>🔥 {histogramBins[3].count} 大改组</span>
                          </>
                        )}
                        {riskItems.length > 0 && (
                          <>
                            <span style={{ color: 'var(--border)' }}>·</span>
                            <span style={{ color: 'var(--warn, #d97706)' }}>⚠ {riskItems.length} 高风险</span>
                          </>
                        )}
                      </div>
                      <div style={{ display: 'flex', gap: 6, justifyContent: 'flex-end' }}>
                        <button
                          type="button"
                          onClick={() => setBackupConfirmOverlayOpen(false)}
                          style={{ ...styles.btnSm, padding: '3px 10px', fontSize: 11 }}
                          title="Esc"
                        >✕ 取消</button>
                        <button
                          type="button"
                          onClick={() => { setBackupConfirmOverlayOpen(false); doApplyBackup(); }}
                          style={{ ...styles.btnSm, padding: '3px 12px', fontSize: 11, color: 'var(--success, #10b981)', borderColor: 'var(--success, #10b981)' }}
                          title="Enter"
                        >✅ 确认应用 {totalSel > 0 ? totalSel : ''} (Enter)</button>
                      </div>
                    </div>
                  </div>
                );
              })()}

              {undoImpactDrillOpen && pendingBackupUndoImpact && (() => {
                const p = pendingBackupUndoImpact;
                const fmtRel = (ts: number) => {
                  const diff = Date.now() - ts;
                  if (diff < 0) return '刚刚';
                  const s = Math.floor(diff / 1000);
                  if (s < 60) return `${s}s 前`;
                  const m = Math.floor(s / 60);
                  if (m < 60) return `${m} 分钟前`;
                  const h = Math.floor(m / 60);
                  if (h < 24) return `${h} 小时前`;
                  const d = Math.floor(h / 24);
                  return `${d} 天前`;
                };
                const keyShort = (k: string) => {
                  const parts = k.split('::');
                  if (parts.length >= 3) return `${parts[0].slice(0, 8)}…/${parts[1]}.${parts[2]}`;
                  return k;
                };
                const renderList = (label: string, color: string, icon: string, entries: { key: string; fullKey?: string; subtitle?: string }[]) => {
                  if (entries.length === 0) return null;
                  return (
                    <div style={{ marginBottom: 10 }}>
                      <div style={{ fontWeight: 700, fontSize: 11, color, marginBottom: 4, display: 'flex', alignItems: 'center', gap: 4 }}>
                        <span>{icon}</span>
                        <span>{label}</span>
                        <span style={{ color: 'var(--muted)', fontWeight: 400, fontSize: 10 }}>({entries.length})</span>
                      </div>
                      <div style={{ display: 'flex', flexDirection: 'column', gap: 2, maxHeight: 160, overflow: 'auto', padding: '2px 4px', background: 'rgba(0,0,0,0.02)', borderRadius: 3 }}>
                        {entries.map((e, idx) => {
                          const copyText = e.fullKey ?? e.key;
                          return (
                            <div
                              key={e.key + idx}
                              style={{ display: 'flex', alignItems: 'baseline', gap: 6, fontSize: 11, padding: '2px 4px', fontFamily: 'monospace' }}
                              title={copyText}
                            >
                              <span style={{ flex: 1, color: 'var(--fg)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                {e.key}
                              </span>
                              {e.subtitle && (
                                <span style={{ fontSize: 9, color: 'var(--muted)', flexShrink: 0 }}>{e.subtitle}</span>
                              )}
                              <button
                                type="button"
                                onClick={(ev) => {
                                  ev.stopPropagation();
                                  navigator.clipboard?.writeText(copyText).then(
                                    () => setPresetManagerMsg({ kind: 'ok', text: `📋 已复制 key：${copyText}` }),
                                    () => setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败（浏览器拒绝访问剪贴板）' }),
                                  );
                                }}
                                style={{
                                  background: 'transparent', border: '1px solid var(--border)',
                                  borderRadius: 2, padding: '0 4px', fontSize: 9, color: 'var(--muted)',
                                  cursor: 'pointer', flexShrink: 0, lineHeight: 1.2,
                                }}
                                title="复制完整 key"
                              >📋</button>
                            </div>
                          );
                        })}
                      </div>
                    </div>
                  );
                };
                const removedP = p.presetsRemovedKeys.map((k) => ({ key: keyShort(k), fullKey: k }));
                const restoredP = p.presetsRestoredKeys.map((x) => ({
                  key: keyShort(x.key),
                  fullKey: x.key,
                  subtitle: `${fmtRel(x.prevUpdatedAt)} → ${fmtRel(x.nowUpdatedAt)}`,
                }));
                const removedS = p.snapshotsRemovedKeys.map((k) => ({ key: k }));
                const restoredS = p.snapshotsRestoredKeys.map((k) => ({ key: k }));
                const anyContent = removedP.length > 0 || restoredP.length > 0 || removedS.length > 0 || restoredS.length > 0;
                return (
                  <div
                    style={{
                      position: 'fixed', inset: 0,
                      background: 'rgba(0,0,0,0.55)',
                      display: 'flex', alignItems: 'center', justifyContent: 'center',
                      zIndex: 100,
                    }}
                    onClick={() => setUndoImpactDrillOpen(false)}
                  >
                    <div
                      style={{
                        background: 'var(--bg-elevated, #fff)',
                        border: '1px solid var(--border)',
                        borderRadius: 8,
                        padding: '16px 20px',
                        maxWidth: 640,
                        width: '90%',
                        maxHeight: '82vh',
                        overflow: 'auto',
                        boxShadow: '0 8px 32px rgba(0,0,0,0.30)',
                        color: 'var(--fg)',
                        fontSize: 12,
                        lineHeight: 1.5,
                      }}
                      onClick={(e) => e.stopPropagation()}
                    >
                      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
                        <div style={{ fontWeight: 700, fontSize: 13 }}>⚖ 撤销影响详情</div>
                        <button
                          style={{ background: 'transparent', border: '1px solid var(--border)', borderRadius: 3, color: 'var(--muted)', cursor: 'pointer', padding: '2px 8px', fontSize: 11 }}
                          onClick={() => setUndoImpactDrillOpen(false)}
                          title="关闭 (Esc / 点击外部)"
                        >✕</button>
                      </div>
                      <div style={{ fontSize: 10, color: 'var(--muted)', marginBottom: 10, fontStyle: 'italic' }}>
                        以上项撤销时将回到应用前的状态：新增项消失，被覆盖项回到旧版本。倒计时结束后此预览不可用。
                      </div>
                      {!anyContent ? (
                        <div style={{ color: 'var(--muted)', fontSize: 11, padding: 12, textAlign: 'center' }}>
                          没有实际变化的项。
                        </div>
                      ) : (
                        <>
                          {renderList('撤销将删除的预设（新加入）', 'var(--danger, #dc2626)', '🗑', removedP)}
                          {renderList('撤销将回到旧版本的预设（被覆盖）', 'var(--info, #8b5cf6)', '↩', restoredP)}
                          {renderList('撤销将删除的快照组（新加入）', 'var(--danger, #dc2626)', '🗑', removedS)}
                          {renderList('撤销将回到旧版本的快照组（被覆盖）', 'var(--info, #8b5cf6)', '↩', restoredS)}
                        </>
                      )}
                      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 6, marginTop: 12, paddingTop: 8, borderTop: '1px solid var(--border)' }}>
                        <button
                          type="button"
                          onClick={() => setUndoImpactDrillOpen(false)}
                          style={{ ...styles.btnSm, padding: '3px 10px', fontSize: 11 }}
                        >关闭</button>
                        <button
                          type="button"
                          onClick={() => { setUndoImpactDrillOpen(false); applyBackupUndo(); }}
                          style={{ ...styles.btnSm, padding: '3px 10px', fontSize: 11, color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)' }}
                          title="立即撤销备份应用"
                        >↩ 立即撤销</button>
                      </div>
                    </div>
                  </div>
                );
              })()}

              {presetManagerMsg && (
                <div style={{
                  padding: '6px 10px', borderRadius: 4, fontSize: 11,
                  background: presetManagerMsg.kind === 'ok' ? 'rgba(16,185,129,0.10)' : 'rgba(220,38,38,0.10)',
                  border: `1px solid ${presetManagerMsg.kind === 'ok' ? 'rgba(16,185,129,0.40)' : 'rgba(220,38,38,0.40)'}`,
                  color: presetManagerMsg.kind === 'ok' ? 'var(--success, #10b981)' : 'var(--danger, #dc2626)',
                  display: 'flex', alignItems: 'center', gap: 8,
                }}>
                  <span style={{ flex: 1 }}>{presetManagerMsg.text}</span>
                  {presetManagerMsg.kind === 'ok' && presetManagerMsg.text.includes('已导入：') && lastBackupAppliedDetail && (() => {
                    const d = lastBackupAppliedDetail;
                    const total = d.presetsAdded.length + d.presetsOverwritten.length + d.snapshotsAdded.length + d.snapshotsOverwritten.length + d.skipped.length;
                    if (total === 0) return null;
                    const chip = (label: string, n: number, color: string) => (
                      <button
                        type="button"
                        onClick={() => setBackupAppliedDetailOpen(true)}
                        style={{
                          padding: '0 4px', fontSize: 10, borderRadius: 3,
                          border: `1px solid ${color}`, background: `${color}1a`,
                          color, cursor: 'pointer', fontFamily: 'inherit',
                          fontWeight: 600, lineHeight: '16px',
                        }}
                        title={`点击展开详细清单（${label}）`}
                      >{label} {n}</button>
                    );
                    return (
                      <span style={{ display: 'inline-flex', gap: 2, alignItems: 'center' }} title="点击数字展开详细变更清单">
                        {chip('新增预设', d.presetsAdded.length, 'var(--success, #10b981)')}
                        {chip('覆盖预设', d.presetsOverwritten.length, 'var(--warn, #d97706)')}
                        {chip('新增快照', d.snapshotsAdded.length, 'var(--success, #10b981)')}
                        {chip('覆盖快照', d.snapshotsOverwritten.length, 'var(--warn, #d97706)')}
                        {d.skipped.length > 0 && chip('跳过', d.skipped.length, 'var(--muted)')}
                      </span>
                    );
                  })()}
                </div>
              )}

              {/* M30.159 D 备份应用详细变更清单浮层：点击 banner 上的数字展开 */}
              {backupAppliedDetailOpen && lastBackupAppliedDetail && (() => {
                const d = lastBackupAppliedDetail;
                // M30.161 B：分类过滤 + 焦点导航；sectionDefs 保留 5 段原始顺序供 focusIdx 使用
                const sectionDefs: Array<{ id: 'added' | 'overwritten' | 'skipped'; sectionId: string; title: string; keys: string[]; color: string; note?: string }> = [
                  { id: 'added', sectionId: 'preset-added', title: '新增预设', keys: d.presetsAdded, color: 'var(--success, #10b981)' },
                  { id: 'overwritten', sectionId: 'preset-overwritten', title: '覆盖预设', keys: d.presetsOverwritten, color: 'var(--warn, #d97706)' },
                  { id: 'added', sectionId: 'snapshot-added', title: '新增快照组', keys: d.snapshotsAdded, color: 'var(--success, #10b981)' },
                  { id: 'overwritten', sectionId: 'snapshot-overwritten', title: '覆盖快照组', keys: d.snapshotsOverwritten, color: 'var(--warn, #d97706)' },
                  { id: 'skipped', sectionId: 'skipped', title: '已跳过项', keys: d.skipped, color: 'var(--muted)' },
                ];
                const visible = sectionDefs.filter((s) => {
                  if (s.keys.length === 0) return false;
                  if (backupDetailFilter === 'all') return true;
                  if (backupDetailFilter === 'added') return s.id === 'added';
                  if (backupDetailFilter === 'overwritten') return s.id === 'overwritten';
                  return s.id === 'skipped';
                });
                const section = (sd: typeof sectionDefs[number], focusIdx: number) => {
                  if (sd.keys.length === 0) return null;
                  const isFocus = focusIdx === backupDetailFocusIdx;
                  const downloadSectionJson = () => {
                    const ts = new Date();
                    const pad = (n: number) => n.toString().padStart(2, '0');
                    const stamp = `${ts.getFullYear()}${pad(ts.getMonth() + 1)}${pad(ts.getDate())}-${pad(ts.getHours())}${pad(ts.getMinutes())}${pad(ts.getSeconds())}`;
                    const filename = `backup-applied-${sd.sectionId}-${stamp}.json`;
                    const payload = {
                      section: sd.sectionId,
                      title: sd.title,
                      exportedAt: ts.toISOString(),
                      connectionId: connId,
                      count: sd.keys.length,
                      keys: sd.keys,
                    };
                    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json;charset=utf-8' });
                    const url = URL.createObjectURL(blob);
                    const a = document.createElement('a');
                    a.href = url;
                    a.download = filename;
                    document.body.appendChild(a);
                    a.click();
                    document.body.removeChild(a);
                    URL.revokeObjectURL(url);
                    setPresetManagerMsg({ kind: 'ok', text: `⬇ 已导出「${sd.title}」JSON（${sd.keys.length} 项 · ${filename}）` });
                  };
                  return (
                    <div style={{ marginBottom: 8, padding: isFocus ? '4px 6px' : '2px 2px', borderRadius: 4, border: isFocus ? '1px solid var(--accent, #3b82f6)' : '1px solid transparent', background: isFocus ? 'rgba(59,130,246,0.06)' : 'transparent' }}>
                      <div style={{ fontSize: 11, fontWeight: 600, color: sd.color, marginBottom: 3, display: 'flex', alignItems: 'center', gap: 6 }}>
                        {isFocus && <span style={{ color: 'var(--accent, #3b82f6)', fontSize: 10 }}>▶</span>}
                        {sd.title}（{sd.keys.length}）{sd.note ? <span style={{ color: 'var(--muted)', fontWeight: 400 }}> · {sd.note}</span> : null}
                        <button
                          type="button"
                          onClick={(e) => { e.preventDefault(); e.stopPropagation(); downloadSectionJson(); }}
                          style={{
                            padding: '0 4px', fontSize: 9, borderRadius: 3,
                            background: 'transparent', color: 'var(--info, #8b5cf6)',
                            border: '1px solid rgba(139,92,246,0.4)',
                            cursor: 'pointer', fontFamily: 'monospace', lineHeight: 1.2,
                          }}
                          title={`⬇ 仅下载本段（${sd.title}）的 keys 数组为 JSON（M30.165 C）`}
                        >⬇ JSON</button>
                        {isFocus && <span style={{ marginLeft: 'auto', fontSize: 9, color: 'var(--muted)', fontWeight: 400 }}>[Enter 复制 {sd.keys.length} key]</span>}
                      </div>
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 3 }}>
                        {sd.keys.slice(0, 30).map((k) => (
                          <button
                            key={k}
                            type="button"
                            onClick={() => { void navigator.clipboard.writeText(k).catch(() => {}); }}
                            style={{
                              padding: '1px 6px', fontSize: 10, borderRadius: 3,
                              border: `1px solid ${sd.color}`, background: 'transparent',
                              color: sd.color, cursor: 'pointer', fontFamily: 'ui-monospace, Consolas, monospace',
                              maxWidth: '100%', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                            }}
                            title={`${k}（点击复制）`}
                          >{k}</button>
                        ))}
                        {sd.keys.length > 30 && (
                          <span style={{ fontSize: 10, color: 'var(--muted)', padding: '1px 4px' }}>+{sd.keys.length - 30} 项</span>
                        )}
                      </div>
                    </div>
                  );
                };
                const copyAll = () => {
                  const lines: string[] = [];
                  lines.push('# polydb 备份应用变更清单');
                  lines.push(`时间：${new Date(d.at).toISOString()}`);
                  lines.push(`连接：${connId}`);
                  lines.push(`耗时：${d.elapsedMs}ms`);
                  lines.push(`合计：新增预设 ${d.presetsAdded.length} · 覆盖预设 ${d.presetsOverwritten.length} · 新增快照 ${d.snapshotsAdded.length} · 覆盖快照 ${d.snapshotsOverwritten.length} · 跳过 ${d.skipped.length}`);
                  if (d.presetsAdded.length > 0) { lines.push('', `## 新增预设（${d.presetsAdded.length}）`); for (const k of d.presetsAdded) lines.push(`- ${k}`); }
                  if (d.presetsOverwritten.length > 0) { lines.push('', `## 覆盖预设（${d.presetsOverwritten.length}）`); for (const k of d.presetsOverwritten) lines.push(`- ${k}`); }
                  if (d.snapshotsAdded.length > 0) { lines.push('', `## 新增快照（${d.snapshotsAdded.length}）`); for (const k of d.snapshotsAdded) lines.push(`- ${k}`); }
                  if (d.snapshotsOverwritten.length > 0) { lines.push('', `## 覆盖快照（${d.snapshotsOverwritten.length}）`); for (const k of d.snapshotsOverwritten) lines.push(`- ${k}`); }
                  if (d.skipped.length > 0) { lines.push('', `## 已跳过（${d.skipped.length}）`); for (const k of d.skipped) lines.push(`- ${k}`); }
                  void navigator.clipboard.writeText(lines.join('\n')).then(
                    () => setPresetManagerMsg({ kind: 'ok', text: '📋 已复制变更清单到剪贴板' }),
                    () => setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败' }),
                  );
                };
                const totalApplied = d.presetsAdded.length + d.presetsOverwritten.length + d.snapshotsAdded.length + d.snapshotsOverwritten.length;
                const filterChips: Array<{ id: 'all' | 'added' | 'overwritten' | 'skipped'; label: string; count: number }> = [
                  { id: 'all', label: '全部', count: sectionDefs.reduce((n, s) => n + s.keys.length, 0) },
                  { id: 'added', label: '新增', count: d.presetsAdded.length + d.snapshotsAdded.length },
                  { id: 'overwritten', label: '覆盖', count: d.presetsOverwritten.length + d.snapshotsOverwritten.length },
                  { id: 'skipped', label: '跳过', count: d.skipped.length },
                ];
                return (
                  <div
                    style={{
                      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.40)', zIndex: 200,
                      display: 'flex', alignItems: 'center', justifyContent: 'center',
                    }}
                    onClick={() => { setBackupAppliedDetailOpen(false); setBackupDetailFocusIdx(0); }}
                  >
                    <div
                      style={{
                        width: 'min(600px, 92vw)', maxHeight: '80vh', overflow: 'auto',
                        background: 'var(--bg, #fff)', border: '1px solid var(--border)',
                        borderRadius: 6, padding: 14, fontSize: 11, color: 'var(--fg, #111)',
                        boxShadow: '0 8px 32px rgba(0,0,0,0.30)',
                      }}
                      onClick={(ev) => ev.stopPropagation()}
                    >
                      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
                        <span style={{ fontWeight: 700, fontSize: 13 }}>📋 备份应用变更清单</span>
                        <span style={{ fontSize: 10, color: 'var(--muted)' }}>
                          · {new Date(d.at).toLocaleString()} · {d.elapsedMs}ms · 应用 {totalApplied} 项 / 跳过 {d.skipped.length}
                        </span>
                        <button
                          type="button"
                          onClick={() => { setBackupAppliedDetailOpen(false); setBackupDetailFocusIdx(0); }}
                          style={{ marginLeft: 'auto', ...styles.btnSm, padding: '1px 6px', fontSize: 10 }}
                          title="关闭（Esc 或点击外部）"
                        >✕</button>
                      </div>
                      {/* M30.161 B 分类过滤 chip + 键盘提示 */}
                      <div style={{ display: 'flex', gap: 4, marginBottom: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                        {filterChips.map((c) => {
                          const active = backupDetailFilter === c.id;
                          return (
                            <button
                              key={c.id}
                              type="button"
                              onClick={() => { setBackupDetailFilter(c.id); setBackupDetailFocusIdx(0); }}
                              style={{
                                padding: '1px 6px', fontSize: 10, borderRadius: 3,
                                border: active ? '1px solid var(--accent, #3b82f6)' : '1px solid var(--border)',
                                background: active ? 'rgba(59,130,246,0.10)' : 'transparent',
                                color: active ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                                cursor: 'pointer',
                                opacity: c.count === 0 ? 0.4 : 1,
                              }}
                            >{c.label} {c.count}</button>
                          );
                        })}
                        <span style={{ fontSize: 9, color: 'var(--muted)', marginLeft: 'auto', fontFamily: 'monospace' }}>
                          ↑↓ 段间 · Enter 复制 · Ctrl+F 过滤 · Esc 关闭
                        </span>
                      </div>
                      {visible.map((sd, i) => section(sd, i))}
                      {visible.length === 0 && (
                        <div style={{ padding: 12, textAlign: 'center', color: 'var(--muted)', fontSize: 11 }}>当前过滤无内容</div>
                      )}
                      <div style={{ display: 'flex', gap: 4, marginTop: 10, paddingTop: 8, borderTop: '1px solid var(--border)' }}>
                        <button type="button" onClick={copyAll} style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 10 }}>
                          📋 复制 Markdown
                        </button>
                        <button
                          type="button"
                          onClick={() => {
                            // M30.163 D apply 变更清单 JSON 下载：把 lastBackupAppliedDetail 序列化为独立快照
                            const ts = new Date();
                            const pad = (n: number) => n.toString().padStart(2, '0');
                            const stamp = `${ts.getFullYear()}${pad(ts.getMonth() + 1)}${pad(ts.getDate())}-${pad(ts.getHours())}${pad(ts.getMinutes())}${pad(ts.getSeconds())}`;
                            const filename = `backup-applied-detail-${stamp}.json`;
                            const text = JSON.stringify({ ...lastBackupAppliedDetail, exportedAt: new Date().toISOString() }, null, 2);
                            const blob = new Blob([text], { type: 'application/json;charset=utf-8' });
                            const url = URL.createObjectURL(blob);
                            const a = document.createElement('a');
                            a.href = url;
                            a.download = filename;
                            document.body.appendChild(a);
                            a.click();
                            document.body.removeChild(a);
                            URL.revokeObjectURL(url);
                            setPresetManagerMsg({ kind: 'ok', text: `⬇ 已导出 apply 清单 JSON（${totalApplied} 应用 / ${d.skipped.length} 跳过 · ${filename}）` });
                          }}
                          style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 10 }}
                          title="把当前 apply 变更清单导出为 JSON 独立快照（与「📋 复制 Markdown」互补：MD 面向人/工单，JSON 面向机器/工单附件）"
                        >⬇ JSON</button>
                        <button
                          type="button"
                          onClick={() => {
                            // M30.164 B apply 变更清单 CSV 下载：section,key 两列扁平表，工单表格附件友好
                            const ts = new Date();
                            const pad = (n: number) => n.toString().padStart(2, '0');
                            const stamp = `${ts.getFullYear()}${pad(ts.getMonth() + 1)}${pad(ts.getDate())}-${pad(ts.getHours())}${pad(ts.getMinutes())}${pad(ts.getSeconds())}`;
                            const filename = `backup-applied-detail-${stamp}.csv`;
                            const esc = (s: string) => `"${s.replace(/"/g, '""')}"`;
                            const lines: string[] = ['section,key'];
                            const pushRows = (section: string, keys: string[]) => {
                              for (const k of keys) lines.push(`${section},${esc(k)}`);
                            };
                            pushRows('preset-added', d.presetsAdded);
                            pushRows('snapshot-added', d.snapshotsAdded);
                            pushRows('preset-overwritten', d.presetsOverwritten);
                            pushRows('snapshot-overwritten', d.snapshotsOverwritten);
                            pushRows('skipped', d.skipped);
                            const text = lines.join('\n') + '\n';
                            const blob = new Blob(['\uFEFF' + text], { type: 'text/csv;charset=utf-8' });
                            const url = URL.createObjectURL(blob);
                            const a = document.createElement('a');
                            a.href = url;
                            a.download = filename;
                            document.body.appendChild(a);
                            a.click();
                            document.body.removeChild(a);
                            URL.revokeObjectURL(url);
                            setPresetManagerMsg({ kind: 'ok', text: `⬇ 已导出 apply 清单 CSV（${totalApplied} 应用 / ${d.skipped.length} 跳过 · ${filename}）` });
                          }}
                          style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 10 }}
                          title="把当前 apply 变更清单导出为 CSV（section,key 两列扁平表，工单表格/Excel 附件友好；与 JSON 面向机器、Markdown 面向人形成互补）"
                        >⬇ CSV</button>
                        <button
                          type="button"
                          onClick={() => { setBackupAppliedDetailOpen(false); setBackupDetailFocusIdx(0); }}
                          style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 10, marginLeft: 'auto' }}
                        >
                          关闭（Esc）
                        </button>
                      </div>
                    </div>
                  </div>
                );
              })()}

              {riskWhitelistOpen && (() => {
                const now = Date.now();
                // M30.158 B 按搜索关键字过滤（key 子串，忽略大小写）
                const kw = whitelistSearch.trim().toLowerCase();
                const searchFiltered = Array.from(riskWhitelist.entries()).filter(
                  ([k]) => !kw || k.toLowerCase().includes(kw),
                );
                // M30.163 A 按 kind 过滤（key 形如 connId::schema::table::preset|snapshot）
                const kindFiltered = whitelistKindFilter === 'all'
                  ? searchFiltered
                  : searchFiltered.filter(([k]) => {
                      const parts = k.split('::');
                      return parts.length >= 4 && parts[3] === whitelistKindFilter;
                    });
                // M30.163 A 每 kind 计数（仅受搜索影响，正交于 kind filter）
                const presetCount = searchFiltered.filter(([k]) => {
                  const parts = k.split('::');
                  return parts.length >= 4 && parts[3] === 'preset';
                }).length;
                const snapshotCount = searchFiltered.filter(([k]) => {
                  const parts = k.split('::');
                  return parts.length >= 4 && parts[3] === 'snapshot';
                }).length;
                // M30.158 B 排序：默认按 addedAt 降序（最新在前），可切 key 字典序升序；M30.164 A 加"按到期"asc（addedAt 升序即最快过期在前）
                const entries = kindFiltered.sort((a, b) => {
                  if (whitelistSortBy === 'key') return a[0].localeCompare(b[0]);
                  if (whitelistSortBy === 'expiring') return a[1] - b[1];
                  return b[1] - a[1];
                });
                const keys = entries.map((e) => e[0]);
                const total = riskWhitelist.size;
                const fmtRemaining = (addedAt: number) => {
                  const remainMs = WHITELIST_TTL_MS - (now - addedAt);
                  const days = Math.ceil(remainMs / (24 * 60 * 60 * 1000));
                  return days > 0 ? `${days}天` : '已过期';
                };
                return (
                  <div
                    style={{
                      position: 'fixed', inset: 0,
                      background: 'rgba(0,0,0,0.55)',
                      display: 'flex', alignItems: 'center', justifyContent: 'center',
                      zIndex: 101,
                    }}
                    onClick={() => setRiskWhitelistOpen(false)}
                  >
                    <div
                      style={{
                        background: 'var(--bg-elevated, #fff)',
                        border: '1px solid var(--border)',
                        borderRadius: 8,
                        padding: '16px 20px',
                        maxWidth: 640,
                        width: '90%',
                        maxHeight: '75vh',
                        display: 'flex', flexDirection: 'column',
                        boxShadow: '0 8px 32px rgba(0,0,0,0.30)',
                        color: 'var(--fg)',
                        fontSize: 12,
                      }}
                      onClick={(e) => e.stopPropagation()}
                    >
                      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
                        <div style={{ fontWeight: 700, fontSize: 13 }}>🔒 高风险项白名单 <span style={{ color: 'var(--muted)', fontWeight: 400, fontSize: 11 }}>({keys.length}{total !== keys.length ? ` / ${total}` : ''})</span></div>
                        <div style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
                          {/* M30.160 D 清理过期项按钮：仅在有 >30 天的项时显示 */}
                          {(() => {
                            const now = Date.now();
                            const expiredKeys: string[] = [];
                            for (const [k, addedAt] of riskWhitelist) {
                              if (now - addedAt > WHITELIST_TTL_MS) expiredKeys.push(k);
                            }
                            if (expiredKeys.length === 0) return null;
                            const cleanExpired = () => {
                              const next = new Map(riskWhitelist);
                              for (const k of expiredKeys) next.delete(k);
                              setRiskWhitelist(next);
                              try {
                                const payload: WhitelistPersisted = { v: 2, items: Array.from(next, ([key, addedAt]) => ({ key, addedAt })) };
                                localStorage.setItem('polydb.riskWhitelist.v1', JSON.stringify(payload));
                              } catch { /* ignore */ }
                              setPresetManagerMsg({ kind: 'ok', text: `🧹 已清理 ${expiredKeys.length} 项过期白名单（>30 天未使用）` });
                            };
                            return (
                              <button
                                type="button"
                                onClick={cleanExpired}
                                style={{ padding: '2px 8px', fontSize: 11, borderRadius: 3, border: '1px solid rgba(217,119,6,0.5)', background: 'rgba(217,119,6,0.08)', color: 'var(--warn, #d97706)', cursor: 'pointer' }}
                                title={`有 ${expiredKeys.length} 项已超 30 天 · 点击立即清理（下次打开 presetManager 时也会自动清理）`}
                              >🧹 清理过期 ({expiredKeys.length})</button>
                            );
                          })()}
                          <button
                            style={{ background: 'transparent', border: '1px solid var(--border)', borderRadius: 3, color: 'var(--muted)', cursor: 'pointer', padding: '2px 8px', fontSize: 11 }}
                            onClick={() => setRiskWhitelistOpen(false)}
                            title="关闭 (Esc / 点击外部)"
                          >✕</button>
                        </div>
                      </div>
                      <div style={{ fontSize: 10, color: 'var(--muted)', marginBottom: 8, fontStyle: 'italic' }}>
                        白名单内的预设/快照项在备份预检时自动跳过，不再出现在勾选列表中。30 天未使用的项会自动过期清理。
                      </div>
                      {/* M30.161 A 白名单浮层 30 天倒计时热力带 */}
                      {total > 0 && (() => {
                        const now = Date.now();
                        const all = Array.from(riskWhitelist.entries());
                        const segs = all.map(([k, addedAt]) => {
                          const used = Math.max(0, now - addedAt);
                          const remainMs = WHITELIST_TTL_MS - used;
                          const days = Math.ceil(remainMs / (24 * 60 * 60 * 1000));
                          const expired = remainMs <= 0;
                          const color = expired
                            ? 'var(--danger, #dc2626)'
                            : days <= 3
                              ? 'rgba(220,38,38,0.7)'
                              : days <= 10
                                ? 'rgba(217,119,6,0.7)'
                                : 'rgba(148,163,184,0.45)';
                          const usedPct = Math.min(100, (used / WHITELIST_TTL_MS) * 100);
                          return { key: k, days, expired, color, usedPct };
                        });
                        const expiredCount = segs.filter((s) => s.expired).length;
                        const soonCount = segs.filter((s) => !s.expired && s.days <= 3).length;
                        const midCount = segs.filter((s) => !s.expired && s.days <= 10 && s.days > 3).length;
                        const healthy = segs.length - expiredCount - soonCount - midCount;
                        return (
                          <div style={{ marginBottom: 8 }}>
                            <div
                              style={{ display: 'flex', height: 6, borderRadius: 3, overflow: 'hidden', background: 'var(--bg-muted, rgba(0,0,0,0.05))' }}
                              title={`过期 ${expiredCount} · 3 天内 ${soonCount} · 10 天内 ${midCount} · 健康 ${healthy}`}
                            >
                              {segs.map((s, i) => (
                                <div
                                  key={`${s.key}-${i}`}
                                  style={{
                                    flex: 1,
                                    background: s.color,
                                    borderRight: i < segs.length - 1 ? '1px solid rgba(255,255,255,0.4)' : 'none',
                                    minWidth: 2,
                                    cursor: 'default',
                                    position: 'relative',
                                  }}
                                  title={`${s.key}\n${s.expired ? '⚠ 已过期' : `剩余 ${s.days} 天`}\n使用进度 ${Math.round(s.usedPct)}%`}
                                />
                              ))}
                            </div>
                            <div style={{ display: 'flex', gap: 6, marginTop: 3, fontSize: 9, color: 'var(--muted)', flexWrap: 'wrap', alignItems: 'center' }}>
                              <span>🔥 过期 <strong style={{ color: 'var(--danger, #dc2626)' }}>{expiredCount}</strong></span>
                              <span>🔴 ≤3d <strong style={{ color: 'var(--danger, #dc2626)' }}>{soonCount}</strong></span>
                              <span>🟠 ≤10d <strong style={{ color: 'var(--warn, #d97706)' }}>{midCount}</strong></span>
                              <span>⚪ 健康 <strong>{healthy}</strong></span>
                              <span style={{ marginLeft: 'auto', fontFamily: 'monospace' }}>{total} 项</span>
                            </div>
                          </div>
                        );
                      })()}
                      {/* M30.160 B 白名单浮层批量加入当前预检高风险项 */}
                      {backupPending && (() => {
                        const highRiskKeys = backupPending.items
                          .filter((i) => computeItemRiskScore(i).level === 'high' && !riskWhitelist.has(i.key))
                          .map((i) => i.key);
                        if (highRiskKeys.length === 0) return null;
                        const batchAdd = () => {
                          const now = Date.now();
                          const next = new Map(riskWhitelist);
                          let added = 0;
                          for (const k of highRiskKeys) if (!next.has(k)) { next.set(k, now); added += 1; }
                          setRiskWhitelist(next);
                          try {
                            const payload: WhitelistPersisted = { v: 2, items: Array.from(next, ([key, addedAt]) => ({ key, addedAt })) };
                            localStorage.setItem('polydb.riskWhitelist.v1', JSON.stringify(payload));
                          } catch { /* ignore */ }
                          setBackupSelected((prev) => {
                            const n = new Set(prev);
                            for (const k of highRiskKeys) n.delete(k);
                            return n;
                          });
                          setPresetManagerMsg({ kind: 'ok', text: `🔒 已批量加入白名单 ${added} 项高风险（从预检剔除）` });
                        };
                        return (
                          <div style={{
                            display: 'flex', alignItems: 'center', gap: 6, marginBottom: 8,
                            padding: '4px 8px', borderRadius: 4,
                            background: 'rgba(220,38,38,0.08)', border: '1px solid rgba(220,38,38,0.35)',
                            fontSize: 10,
                          }}>
                            <span style={{ color: 'var(--danger, #dc2626)', fontWeight: 600, flex: 1 }}>
                              ⚡ 当前预检有 <strong>{highRiskKeys.length}</strong> 项未白名单化的高风险
                            </span>
                            <button
                              type="button"
                              onClick={batchAdd}
                              style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 10, color: 'var(--danger, #dc2626)', borderColor: 'rgba(220,38,38,0.5)' }}
                              title="把这批高风险项全部加入白名单，并从当前预检勾选中剔除"
                            >🔒 一键全部加入</button>
                          </div>
                        );
                      })()}
                      {/* M30.158 B 白名单浮层搜索 + 排序控件 */}
                      {total > 0 && (
                        <div style={{ display: 'flex', gap: 4, alignItems: 'center', marginBottom: 8, flexWrap: 'wrap' }}>
                          <span style={{ color: 'var(--muted)', fontSize: 10 }}>搜索：</span>
                          <input
                            type="text"
                            value={whitelistSearch}
                            onChange={(e) => setWhitelistSearch(e.target.value)}
                            placeholder="schema/table/connId 子串"
                            style={{ padding: '1px 5px', fontSize: 10, borderRadius: 3, border: '1px solid var(--border)', background: 'transparent', color: 'var(--fg)', width: 160, fontFamily: 'inherit' }}
                          />
                          {whitelistSearch && (
                            <button
                              type="button"
                              onClick={() => setWhitelistSearch('')}
                              style={{ padding: '0 4px', fontSize: 10, background: 'transparent', color: 'var(--muted)', border: 'none', cursor: 'pointer' }}
                              title="清空搜索"
                            >×</button>
                          )}
                          <span style={{ color: 'var(--muted)', fontSize: 10, marginLeft: 4 }}>类型：</span>
                          {(() => {
                            const kindChip = (id: 'all' | 'preset' | 'snapshot', label: string, count: number) => {
                              const active = whitelistKindFilter === id;
                              return (
                                <button
                                  type="button"
                                  onClick={() => setWhitelistKindFilter(id)}
                                  style={{
                                    padding: '1px 6px', fontSize: 10, borderRadius: 3,
                                    border: active ? '1px solid var(--accent, #3b82f6)' : '1px solid var(--border)',
                                    background: active ? 'rgba(59,130,246,0.10)' : 'transparent',
                                    color: active ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                                    cursor: 'pointer', fontFamily: 'inherit',
                                  }}
                                  title={`显示${id === 'all' ? '全部' : id === 'preset' ? '预设' : '快照'}项白名单（与搜索/排序正交）`}
                                >{label} {count}</button>
                              );
                            };
                            return <>
                              {kindChip('all', '全部', searchFiltered.length)}
                              {kindChip('preset', '预设', presetCount)}
                              {kindChip('snapshot', '快照', snapshotCount)}
                            </>;
                          })()}
                          <span style={{ color: 'var(--muted)', fontSize: 10, marginLeft: 4 }}>排序：</span>
                          <button
                            type="button"
                            onClick={() => setWhitelistSortBy('addedAt')}
                            style={{
                              padding: '1px 6px', fontSize: 10, borderRadius: 3, border: '1px solid var(--border)',
                              background: whitelistSortBy === 'addedAt' ? 'rgba(59,130,246,0.10)' : 'transparent',
                              color: whitelistSortBy === 'addedAt' ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                              cursor: 'pointer', fontFamily: 'inherit',
                            }}
                            title="按加入时间倒序（最新在前）"
                          >⏱ 最新</button>
                          <button
                            type="button"
                            onClick={() => setWhitelistSortBy('key')}
                            style={{
                              padding: '1px 6px', fontSize: 10, borderRadius: 3, border: '1px solid var(--border)',
                              background: whitelistSortBy === 'key' ? 'rgba(59,130,246,0.10)' : 'transparent',
                              color: whitelistSortBy === 'key' ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                              cursor: 'pointer', fontFamily: 'inherit',
                            }}
                            title="按 schema.table 字典序"
                          >🔤 A→Z</button>
                          <button
                            type="button"
                            onClick={() => setWhitelistSortBy('expiring')}
                            style={{
                              padding: '1px 6px', fontSize: 10, borderRadius: 3, border: '1px solid var(--border)',
                              background: whitelistSortBy === 'expiring' ? 'rgba(217,119,6,0.14)' : 'transparent',
                              color: whitelistSortBy === 'expiring' ? 'var(--warn, #d97706)' : 'var(--muted)',
                              cursor: 'pointer', fontFamily: 'inherit',
                            }}
                            title="按加入时间升序（最早加入 = 最快到期在前，便于先清理快过期的项）"
                          >🔥 到期</button>
                          {total !== keys.length && (
                            <span style={{ fontSize: 10, color: 'var(--muted)', marginLeft: 2 }}>
                              {keys.length}/{total}
                            </span>
                          )}
                        </div>
                      )}
                      {/* M30.159 B 白名单多选工具栏：全选/全不选 + 计数（仅在浮层打开且有可见项时显示） */}
                      {total > 0 && keys.length > 0 && (() => {
                        const visibleKeys = entries.map((e) => e[0]);
                        const allSelected = visibleKeys.length > 0 && visibleKeys.every((k) => whitelistSel.has(k));
                        return (
                          <div style={{ display: 'flex', gap: 4, alignItems: 'center', marginBottom: 6, fontSize: 10 }}>
                            <button
                              type="button"
                              onClick={() => {
                                setWhitelistSel(allSelected ? new Set() : new Set(visibleKeys));
                                whitelistAnchorIdx.current = null;
                              }}
                              style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10 }}
                              title="选中/取消全部当前可见项（受搜索/排序过滤影响；Shift+Click 行做范围选）"
                            >{allSelected ? '☐ 取消全选' : '☑ 全选可见'}</button>
                            <button
                              type="button"
                              disabled={whitelistSel.size === 0}
                              onClick={() => {
                                const next = new Map(riskWhitelist);
                                let removed = 0;
                                for (const k of whitelistSel) {
                                  if (next.has(k)) { next.delete(k); removed += 1; }
                                }
                                setRiskWhitelist(next);
                                try {
                                  const payload = { v: 2 as const, items: Array.from(next.entries()).sort((a, b) => a[0].localeCompare(b[0])).map(([key, addedAt]) => ({ key, addedAt })) };
                                  localStorage.setItem('polydb.riskWhitelist.v1', JSON.stringify(payload));
                                } catch { /* ignore */ }
                                setWhitelistSel(new Set());
                                whitelistAnchorIdx.current = null;
                                setPresetManagerMsg({ kind: 'ok', text: `🔓 已批量移除 ${removed} 项（下次备份预检这些高风险项将重新出现）` });
                              }}
                              style={{
                                ...styles.btnSm, padding: '1px 6px', fontSize: 10,
                                color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)',
                                opacity: whitelistSel.size === 0 ? 0.5 : 1,
                                cursor: whitelistSel.size === 0 ? 'not-allowed' : 'pointer',
                              }}
                              title="移除所有勾选项（Shift+Click 行可范围选；单次移除走每行 🔓移除 按钮）"
                            >🗑 移除选中 ({whitelistSel.size})</button>
                          </div>
                        );
                      })()}
                      {keys.length === 0 ? (
                        <div style={{ padding: '16px 0', textAlign: 'center', color: 'var(--muted)', fontSize: 11 }}>
                          {total === 0
                            ? '暂无白名单项。可在备份预检的高风险项右键菜单中选择「加入白名单」。'
                            : (whitelistKindFilter !== 'all' || whitelistSearch)
                              ? `当前过滤无匹配项（总 ${total} 项已过滤；搜索="${whitelistSearch || '∅'}" · 类型=${whitelistKindFilter === 'all' ? '全部' : whitelistKindFilter === 'preset' ? '预设' : '快照'}）`
                              : '无匹配项（异常状态，总计数 0 但 total 非 0）'}
                        </div>
                      ) : (
                        <div style={{ flex: 1, overflowY: 'auto', marginBottom: 10, border: '1px solid var(--border)', borderRadius: 4 }}>
                          {entries.map(([k, addedAt], i) => {
                            const parts = k.split('::');
                            const display = parts.length >= 3 ? `${parts[1]}.${parts[2]}` : k;
                            const kind = parts.length >= 4 ? parts[3] : null;
                            const remainMs = WHITELIST_TTL_MS - (now - addedAt);
                            const remainColor = remainMs <= 3 * 24 * 60 * 60 * 1000
                              ? 'var(--danger, #dc2626)'
                              : remainMs <= 10 * 24 * 60 * 60 * 1000
                                ? 'var(--warn, #d97706)'
                                : 'var(--muted)';
                            const isSel = whitelistSel.has(k);
                            const isFocus = whitelistFocusIdx === i;
                            return (
                              <div
                                key={k}
                                style={{
                                  display: 'flex', alignItems: 'center', gap: 6,
                                  padding: '6px 10px',
                                  borderBottom: i < entries.length - 1 ? '1px solid var(--border)' : 'none',
                                  fontFamily: 'var(--mono, monospace)', fontSize: 11,
                                  background: isSel ? 'rgba(220,38,38,0.06)' : isFocus ? 'rgba(59,130,246,0.08)' : 'transparent',
                                  borderLeft: isFocus ? '3px solid var(--accent, #3b82f6)' : '3px solid transparent',
                                  transition: 'background 0.12s, border-color 0.12s',
                                }}
                                title={`${k} · 单击 toggle / Shift+单击 范围选（与 anchor 之间的所有项）`}
                                onClick={(ev) => {
                                  if (ev.target instanceof HTMLInputElement) return;
                                  if (ev.target instanceof HTMLButtonElement) return;
                                  if (ev.shiftKey && whitelistAnchorIdx.current !== null) {
                                    const a = whitelistAnchorIdx.current;
                                    const b = i;
                                    const start = Math.min(a, b), end = Math.max(a, b);
                                    const rangeKeys = entries.slice(start, end + 1).map((e) => e[0]);
                                    setWhitelistSel((prev) => {
                                      const next = new Set(prev);
                                      for (const kk of rangeKeys) next.add(kk);
                                      return next;
                                    });
                                    return;
                                  }
                                  setWhitelistSel((prev) => {
                                    const next = new Set(prev);
                                    if (next.has(k)) next.delete(k); else next.add(k);
                                    return next;
                                  });
                                  whitelistAnchorIdx.current = i;
                                }}
                              >
                                <input
                                  type="checkbox"
                                  checked={isSel}
                                  onChange={() => {
                                    setWhitelistSel((prev) => {
                                      const next = new Set(prev);
                                      if (next.has(k)) next.delete(k); else next.add(k);
                                      return next;
                                    });
                                    whitelistAnchorIdx.current = i;
                                  }}
                                  onClick={(ev) => ev.stopPropagation()}
                                  style={{ cursor: 'pointer', flexShrink: 0 }}
                                />
                                <span style={{ flex: 1, display: 'flex', alignItems: 'center', gap: 4 }}>
                                  {kind === 'snapshot' ? '📸' : '⚙️'} <code style={{ color: 'var(--info, #8b5cf6)' }}>{display}</code>
                                </span>
                                <code
                                  style={{
                                    fontSize: 9, padding: '0 4px', borderRadius: 2,
                                    background: 'rgba(217,119,6,0.10)',
                                    color: remainColor, fontWeight: 700, letterSpacing: '0.02em',
                                  }}
                                  title={`加入于 ${new Date(addedAt).toLocaleString()}，剩余 ${fmtRemaining(addedAt)}（超 30 天自动清理）`}
                                >⏳ {fmtRemaining(addedAt)}</code>
                                <button
                                  type="button"
                                  onClick={(ev) => {
                                    ev.stopPropagation();
                                    void navigator.clipboard.writeText(k).then(
                                      () => setPresetManagerMsg({ kind: 'ok', text: `📋 已复制白名单 key：${k}` }),
                                      () => setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败（浏览器剪贴板权限）' }),
                                    );
                                  }}
                                  style={{ ...styles.btnSm, padding: '1px 5px', fontSize: 10, color: 'var(--info, #8b5cf6)' }}
                                  title="复制该 key 到剪贴板（可粘贴到工单/邮件定位）· 键盘：Enter"
                                >📋</button>
                                <button
                                  type="button"
                                  onClick={(ev) => { ev.stopPropagation(); removeRiskWhitelistItem(k); }}
                                  style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--danger, #dc2626)' }}
                                  title="从白名单移除（下次备份预检将重新出现）· 键盘：Delete"
                                >🔓 移除</button>
                              </div>
                            );
                          })}
                        </div>
                      )}
                      {keys.length > 0 && (
                        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 6 }}>
                          <button
                            type="button"
                            onClick={clearRiskWhitelist}
                            style={{ ...styles.btnSm, padding: '3px 12px', fontSize: 11, color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)' }}
                            title="清空全部白名单项（下次备份预检所有项都会重新出现）"
                          >🧹 清空全部 ({keys.length})</button>
                        </div>
                      )}
                      {/* M30.156 A + M30.157 A 白名单导出/导入（JSON 或 CSV，跨浏览器/设备迁移） */}
                      <div style={{ display: 'flex', gap: 6, marginTop: keys.length > 0 ? 8 : 4, alignItems: 'center', flexWrap: 'wrap' }}>
                        <button
                          type="button"
                          onClick={exportRiskWhitelist}
                          disabled={riskWhitelist.size === 0}
                          style={{
                            ...styles.btnSm, padding: '3px 10px', fontSize: 11,
                            color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)',
                            opacity: riskWhitelist.size === 0 ? 0.5 : 1,
                            cursor: riskWhitelist.size === 0 ? 'not-allowed' : undefined,
                          }}
                          title="把当前白名单导出为 JSON 或 CSV 文件（可跨浏览器/设备迁移；由右侧 radio 决定格式）"
                        >⬇ 导出 {whitelistExportFormat === 'json' ? 'JSON' : 'CSV'}</button>
                        <button
                          type="button"
                          onClick={() => whitelistFileRef.current?.click()}
                          style={{ ...styles.btnSm, padding: '3px 10px', fontSize: 11, color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)' }}
                          title="从之前导出的白名单 JSON 文件恢复"
                        >⬆ 导入 JSON…</button>
                        <button
                          type="button"
                          onClick={() => whitelistCsvFileRef.current?.click()}
                          style={{ ...styles.btnSm, padding: '3px 10px', fontSize: 11, color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)' }}
                          title="从白名单 CSV 文件恢复（表头 key,addedAt）"
                        >⬆ 导入 CSV…</button>
                        <input
                          ref={whitelistFileRef}
                          type="file"
                          accept=".json,application/json"
                          style={{ display: 'none' }}
                          onChange={(e) => {
                            const f = e.target.files?.[0];
                            if (f) void handleRiskWhitelistImport(f);
                          }}
                        />
                        <input
                          ref={whitelistCsvFileRef}
                          type="file"
                          accept=".csv,text/csv"
                          style={{ display: 'none' }}
                          onChange={(e) => {
                            const f = e.target.files?.[0];
                            if (f) void handleRiskWhitelistCsvImport(f);
                          }}
                        />
                        <label style={{ display: 'flex', alignItems: 'center', gap: 3, fontSize: 10, color: 'var(--muted)', cursor: 'pointer' }}>
                          <input
                            type="radio"
                            name="whitelist-export-fmt"
                            checked={whitelistExportFormat === 'json'}
                            onChange={() => setWhitelistExportFormat('json')}
                          />
                          导出 JSON
                        </label>
                        <label style={{ display: 'flex', alignItems: 'center', gap: 3, fontSize: 10, color: 'var(--muted)', cursor: 'pointer' }}>
                          <input
                            type="radio"
                            name="whitelist-export-fmt"
                            checked={whitelistExportFormat === 'csv'}
                            onChange={() => setWhitelistExportFormat('csv')}
                          />
                          导出 CSV
                        </label>
                        <label style={{ display: 'flex', alignItems: 'center', gap: 3, fontSize: 10, color: 'var(--muted)', cursor: 'pointer' }}>
                          <input
                            type="radio"
                            name="whitelist-import-mode"
                            checked={whitelistImportMode === 'merge'}
                            onChange={() => setWhitelistImportMode('merge')}
                          />
                          合并
                        </label>
                        <label style={{ display: 'flex', alignItems: 'center', gap: 3, fontSize: 10, color: 'var(--muted)', cursor: 'pointer' }}>
                          <input
                            type="radio"
                            name="whitelist-import-mode"
                            checked={whitelistImportMode === 'replace'}
                            onChange={() => setWhitelistImportMode('replace')}
                          />
                          覆盖
                        </label>
                      </div>
                    </div>
                  </div>
                );
              })()}

              {backupUndoPreviewText !== null && (() => {
                const text = backupUndoPreviewText;
                const lines = text.split('\n');
                return (
                  <div
                    style={{
                      position: 'fixed', inset: 0,
                      background: 'rgba(0,0,0,0.55)',
                      display: 'flex', alignItems: 'center', justifyContent: 'center',
                      zIndex: 101,
                    }}
                    onClick={() => setBackupUndoPreviewText(null)}
                  >
                    <div
                      style={{
                        background: 'var(--bg-elevated, #fff)',
                        border: '1px solid var(--border)',
                        borderRadius: 8,
                        padding: '16px 20px',
                        maxWidth: 720,
                        width: '90%',
                        maxHeight: '82vh',
                        display: 'flex', flexDirection: 'column',
                        boxShadow: '0 8px 32px rgba(0,0,0,0.30)',
                        color: 'var(--fg)',
                        fontSize: 12,
                      }}
                      onClick={(e) => e.stopPropagation()}
                    >
                      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
                        <div style={{ fontWeight: 700, fontSize: 13 }}>📋 撤销摘要预览</div>
                        <button
                          style={{ background: 'transparent', border: '1px solid var(--border)', borderRadius: 3, color: 'var(--muted)', cursor: 'pointer', padding: '2px 8px', fontSize: 11 }}
                          onClick={() => setBackupUndoPreviewText(null)}
                          title="关闭 (Esc / 点击外部)"
                        >✕</button>
                      </div>
                      <div style={{ fontSize: 10, color: 'var(--muted)', marginBottom: 10, fontStyle: 'italic' }}>
                        Markdown 格式，共 {lines.length} 行。复制到剪贴板后可粘贴到 IM / 工单 / 邮件。
                      </div>
                      <div
                        style={{
                          flex: 1, minHeight: 200,
                          background: 'rgba(0,0,0,0.03)',
                          border: '1px solid var(--border)',
                          borderRadius: 4,
                          padding: '10px 12px',
                          overflow: 'auto',
                          fontFamily: 'monospace',
                          fontSize: 11,
                          lineHeight: 1.5,
                          whiteSpace: 'pre-wrap',
                          wordBreak: 'break-all',
                        }}
                      >
                        {lines.map((ln, i) => {
                          if (ln.startsWith('# ')) {
                            return (
                              <div key={i} style={{ fontWeight: 700, fontSize: 13, color: 'var(--fg)', marginTop: i === 0 ? 0 : 6, marginBottom: 2 }}>{ln.slice(2)}</div>
                            );
                          }
                          if (ln.startsWith('## ')) {
                            return (
                              <div key={i} style={{ fontWeight: 600, fontSize: 12, color: 'var(--accent, #2563eb)', marginTop: 8, marginBottom: 2 }}>{ln.slice(3)}</div>
                            );
                          }
                          if (ln.startsWith('- ')) {
                            return (
                              <div key={i} style={{ paddingLeft: 10, color: 'var(--fg)' }}>• {ln.slice(2)}</div>
                            );
                          }
                          if (ln === '') {
                            return <div key={i} style={{ height: 4 }} />;
                          }
                          return <div key={i} style={{ color: 'var(--muted)' }}>{ln}</div>;
                        })}
                      </div>
                      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 6, marginTop: 12, paddingTop: 8, borderTop: '1px solid var(--border)' }}>
                        <button
                          type="button"
                          onClick={() => setBackupUndoPreviewText(null)}
                          style={{ ...styles.btnSm, padding: '3px 10px', fontSize: 11 }}
                        >关闭</button>
                        <button
                          type="button"
                          onClick={() => {
                            navigator.clipboard?.writeText(text).then(
                              () => {
                                setPresetManagerMsg({ kind: 'ok', text: `📋 已复制撤销摘要（${lines.length} 行）` });
                                setBackupUndoPreviewText(null);
                              },
                              () => setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败（浏览器拒绝访问剪贴板）' }),
                            );
                          }}
                          style={{ ...styles.btnSm, padding: '3px 10px', fontSize: 11 }}
                        >📋 复制并关闭</button>
                      </div>
                    </div>
                  </div>
                );
              })()}

              {/* M30.118 备份预检 diff（逐项确认） */}
              {backupPending && (() => {
                const items = backupPreviewSorted;
                const selectedCount = items.filter((i) => backupSelected.has(i.key)).length;
                const skipCount = items.length - selectedCount;
                const selectedAdds = items.filter((i) => backupSelected.has(i.key) && i.action === 'add').length;
                const selectedOverwrites = items.filter((i) => backupSelected.has(i.key) && i.action === 'overwrite').length;
                // M30.141 header 影响汇总：搜索 + 两 filter 三层组合命中项的 diff 变更数
                const searchQ = backupPreviewSearch.trim().toLowerCase();
                const matchAll = items.filter((i) => {
                  if (backupPreviewFilter !== 'all' && i.action !== backupPreviewFilter) return false;
                  if (backupPreviewKindFilter !== 'all' && i.kind !== backupPreviewKindFilter) return false;
                  if (searchQ && !i.schema.toLowerCase().includes(searchQ) && !i.table.toLowerCase().includes(searchQ)) return false;
                  return true;
                });
                let affectedCount = 0;
                let totalDiffCount = 0;
                for (const i of matchAll) {
                  if (i.action !== 'overwrite' || !i.diff) continue;
                  const d = i.diff;
                  const diffCount = d.scalarDiffs.length + d.removedTargets.length + d.addedTargets.length
                    + Object.keys(d.removedCols).length + Object.keys(d.addedCols).length;
                  if (diffCount > 0) {
                    affectedCount += 1;
                    totalDiffCount += diffCount;
                  }
                }
                // M30.147 A 导出预检报告：JSON/CSV/MD 三格式，一行一 item 带 action/kind/risk
                const exportBackupReport = (fmt: 'json' | 'csv' | 'md') => {
                  const now = new Date();
                  const ts = `${now.getFullYear()}-${String(now.getMonth()+1).padStart(2,'0')}-${String(now.getDate()).padStart(2,'0')}_${String(now.getHours()).padStart(2,'0')}${String(now.getMinutes()).padStart(2,'0')}${String(now.getSeconds()).padStart(2,'0')}`;
                  const audit = backupItemAudit ?? {};
                  const rows = items.map((i) => {
                    const dc = (() => {
                      if (i.action !== 'overwrite' || !i.diff) return 0;
                      const d = i.diff;
                      return d.scalarDiffs.length + d.removedTargets.length + d.addedTargets.length
                        + Object.keys(d.removedCols).length + Object.keys(d.addedCols).length;
                    })();
                    return {
                      key: i.key, kind: i.kind, action: i.action,
                      schema: i.schema, table: i.table,
                      selected: backupSelected.has(i.key),
                      auditStatus: audit[i.key] ?? '',
                      diffCount: dc,
                      riskScore: riskScoreOf(i),
                    };
                  });
                  const summary = {
                    exportedAt: now.toISOString(),
                    connId,
                    sourceFile: backupSourceFileName ?? '(未知)',
                    total: items.length,
                    selected: selectedCount,
                    adds: items.filter((i) => i.action === 'add').length,
                    overwrites: items.filter((i) => i.action === 'overwrite').length,
                    affectedCount, totalDiffCount,
                    auditOk: items.filter((i) => audit[i.key] === 'ok').length,
                    auditDead: items.filter((i) => audit[i.key] === 'dead').length,
                  };
                  const download = (content: string, mime: string, name: string) => {
                    const blob = new Blob([content], { type: mime });
                    const url = URL.createObjectURL(blob);
                    const a = document.createElement('a');
                    a.href = url; a.download = name;
                    document.body.appendChild(a); a.click(); document.body.removeChild(a);
                    setTimeout(() => URL.revokeObjectURL(url), 1000);
                    setPresetManagerMsg({ kind: 'ok', text: `📊 已导出预检报告 ${name}（${rows.length} 项）` });
                  };
                  if (fmt === 'json') {
                    download(JSON.stringify({ summary, items: rows }, null, 2), 'application/json', `polydb-backup-report-${ts}.json`);
                    return;
                  }
                  if (fmt === 'csv') {
                    const csvEsc = (v: unknown) => {
                      const s = String(v ?? '');
                      if (/[",\n]/.test(s)) return `"${s.replace(/"/g, '""')}"`;
                      return s;
                    };
                    const head = ['key','kind','action','schema','table','selected','auditStatus','diffCount','riskScore'].join(',');
                    const body = rows.map((r) => [r.key,r.kind,r.action,r.schema,r.table,r.selected,r.auditStatus,r.diffCount,r.riskScore].map(csvEsc).join(',')).join('\n');
                    download(`${head}\n${body}\n`, 'text/csv', `polydb-backup-report-${ts}.csv`);
                    return;
                  }
                  // md
                  const md: string[] = [];
                  md.push(`# polydb 备份预检报告`);
                  md.push('');
                  md.push(`- **时间**：${summary.exportedAt}`);
                  md.push(`- **连接**：\`${summary.connId}\``);
                  md.push(`- **源文件**：${summary.sourceFile}`);
                  md.push(`- **合计**：${summary.total} 项（新增 ${summary.adds} · 覆盖 ${summary.overwrites}）`);
                  md.push(`- **当前勾选**：${summary.selected} / ${summary.total}`);
                  md.push(`- **有变更覆盖**：${summary.affectedCount} 项 · ${summary.totalDiffCount} 处变更`);
                  md.push(`- **审计**：✅ ${summary.auditOk} 存活 · ❌ ${summary.auditDead} 失效`);
                  md.push('');
                  md.push('| key | 类型 | 动作 | 表 | 勾选 | 审计 | diff | 风险分 |');
                  md.push('|---|---|---|---|---|---|---|---|');
                  for (const r of rows) {
                    md.push(`| ${r.key} | ${r.kind} | ${r.action} | ${r.schema}.${r.table} | ${r.selected?'✅':'⬜'} | ${r.auditStatus || '—'} | ${r.diffCount} | ${r.riskScore} |`);
                  }
                  download(md.join('\n') + '\n', 'text/markdown', `polydb-backup-report-${ts}.md`);
                };
                return (
                  <div style={{
                    marginTop: 12, padding: '10px',
                    border: '1px solid rgba(217,119,6,0.4)',
                    background: 'rgba(217,119,6,0.04)',
                    borderRadius: 4,
                    position: 'relative',
                  }}>
                    {/* M30.147 D 备份文件元信息卡：源文件 / 当前连接 / 备份导出于 / 分布 / 总 diff / 命中率 */}
                    {(() => {
                      const backup = backupPending.backup;
                      const expAt = backup.exportedAt;
                      const presetAdd = items.filter((i) => i.kind === 'preset' && i.action === 'add').length;
                      const presetOver = items.filter((i) => i.kind === 'preset' && i.action === 'overwrite').length;
                      const snapAdd = items.filter((i) => i.kind === 'snapshot' && i.action === 'add').length;
                      const snapOver = items.filter((i) => i.kind === 'snapshot' && i.action === 'overwrite').length;
                      const audit = backupItemAudit ?? {};
                      const auditedOk = items.filter((i) => audit[i.key] === 'ok').length;
                      const auditedDead = items.filter((i) => audit[i.key] === 'dead').length;
                      const audited = auditedOk + auditedDead;
                      const fmtExpAt = (() => {
                        if (!expAt) return '—';
                        const d = new Date(expAt);
                        const pad = (n: number) => String(n).padStart(2, '0');
                        return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
                      })();
                      const srcConns = Array.from(new Set(items.map((i) => i.key.split('::')[0]).filter(Boolean)));
                      return (
                        <div style={{
                          padding: '6px 8px', marginBottom: 6,
                          background: 'rgba(148,163,184,0.06)',
                          border: '1px solid var(--border)',
                          borderRadius: 3,
                          fontSize: 10, color: 'var(--muted)',
                          display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'center',
                        }}>
                          <span title="备份文件的原始文件名">📄 <code style={{ color: 'var(--fg)' }}>{backupSourceFileName ?? '(未知)'}</code></span>
                          {backupSourceHash && (
                            <span
                              title={`SHA-256 前 8 位（Web Crypto）· 用作文件指纹防误导同一备份`}
                              style={{
                                padding: '0 4px', fontSize: 10, borderRadius: 3,
                                background: 'rgba(139,92,246,0.10)', color: 'var(--info, #8b5cf6)',
                                border: '1px solid rgba(139,92,246,0.35)',
                                fontFamily: 'monospace', letterSpacing: 0.3,
                              }}
                            >🔑 {backupSourceHash}</span>
                          )}
                          <span title="当前浏览器连接 ID">🔗 连接 <code style={{ color: 'var(--fg)' }}>{connId}</code></span>
                          <span title="备份 JSON 中 exportedAt 字段">📅 备份导出于 {fmtExpAt}</span>
                          {srcConns.length > 1 && (
                            <span title={`备份内含 ${srcConns.length} 个不同来源连接：${srcConns.join(', ')}`}>🌐 来源 {srcConns.length} 个连接</span>
                          )}
                          <span>📊 预设 +{presetAdd}/={presetOver} · 快照 +{snapAdd}/={snapOver}</span>
                          {totalDiffCount > 0 && (
                            <span style={{ color: 'var(--danger, #dc2626)' }} title="覆盖项字段级 diff 总数">⇄ 总 {totalDiffCount} 变更</span>
                          )}
                          {audited > 0 && (
                            <span title="审计缓存或实时命中" style={{ color: auditedDead > 0 ? 'var(--danger, #dc2626)' : 'var(--success, #10b981)' }}>
                              ✅ 命中 {audited}/{items.filter((i) => i.kind === 'preset').length}
                            </span>
                          )}
                        </div>
                      );
                    })()}
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
                      <div style={{ fontWeight: 700, fontSize: 11, color: 'var(--warn, #d97706)', letterSpacing: 0.4 }}>
                        🔍 备份预检（{items.length} 项 · 已选 {selectedCount} · 跳过 {skipCount}）
                        {backupWhitelistSkipped > 0 && (
                          <span
                            style={{ color: 'var(--info, #8b5cf6)', marginLeft: 4, cursor: 'pointer' }}
                            onClick={() => setRiskWhitelistOpen(true)}
                            title={`白名单自动跳过 ${backupWhitelistSkipped} 项 · 点击打开白名单管理`}
                          >🔒 白名单 {backupWhitelistSkipped}</span>
                        )}
                        {/* M30.157 D 备份文件内嵌白名单：显示 chip + 应用/忽略按钮 */}
                        {backupEmbeddedWhitelist && backupEmbeddedWhitelist.length > 0 && (
                          <span
                            style={{
                              marginLeft: 6, padding: '0 5px', borderRadius: 3,
                              background: 'rgba(139,92,246,0.14)', color: 'var(--info, #8b5cf6)',
                              border: '1px solid rgba(139,92,246,0.3)',
                              cursor: 'default', display: 'inline-flex', alignItems: 'center', gap: 4,
                            }}
                            title={`备份文件内嵌 ${backupEmbeddedWhitelist.length} 条高风险白名单（含 addedAt 时间戳）· 应用后将合并到当前白名单（去重按 key，取较早 addedAt）`}
                          >
                            🔒 内嵌 {backupEmbeddedWhitelist.length}
                            <button
                              type="button"
                              style={{ padding: '0 3px', borderRadius: 2, fontSize: 9, background: 'rgba(16,185,129,0.15)', color: 'var(--success, #10b981)', border: '1px solid rgba(16,185,129,0.4)', cursor: 'pointer', fontWeight: 700, lineHeight: 1 }}
                              onClick={() => {
                                const next = new Map<string, number>(riskWhitelist);
                                let added = 0, kept = 0;
                                for (const e of backupEmbeddedWhitelist ?? []) {
                                  const old = next.get(e.key);
                                  if (old === undefined) { next.set(e.key, e.addedAt); added += 1; }
                                  else if (e.addedAt < old) { next.set(e.key, e.addedAt); kept += 1; }
                                }
                                setRiskWhitelist(next);
                                try {
                                  const items = Array.from(next.entries()).sort((a, b2) => a[0].localeCompare(b2[0])).map(([key, addedAt]) => ({ key, addedAt }));
                                  localStorage.setItem('polydb.riskWhitelist.v1', JSON.stringify({ v: 2, items }));
                                } catch { /* quota */ }
                                setBackupEmbeddedWhitelist(null);
                                setPresetManagerMsg({ kind: 'ok', text: `🔒 已应用备份白名单：新增 ${added} · 保留更早时间戳 ${kept}` });
                              }}
                              title="把备份内的白名单合并到当前白名单（按 key 去重，取较早 addedAt 保证 30 天计时更保守）"
                            >✅ 应用</button>
                            <button
                              type="button"
                              style={{ padding: '0 3px', borderRadius: 2, fontSize: 9, background: 'transparent', color: 'var(--muted)', border: '1px solid var(--border)', cursor: 'pointer', lineHeight: 1 }}
                              onClick={() => { setBackupEmbeddedWhitelist(null); }}
                              title="忽略备份内的白名单段（不影响本次预设导入）"
                            >✕ 忽略</button>
                          </span>
                        )}
                        {selectedAdds > 0 && <span style={{ color: 'var(--success, #10b981)', marginLeft: 6 }}>✅ 新增 {selectedAdds}</span>}
                        {selectedOverwrites > 0 && <span style={{ color: 'var(--warn, #d97706)', marginLeft: 4 }}>⚠ 覆盖 {selectedOverwrites}</span>}
                        {affectedCount > 0 && <span style={{ marginLeft: 4, color: 'var(--danger, #dc2626)', fontWeight: 700 }} title="当前筛选范围内覆盖项有字段级 diff 的项数及总变更字段数">⇄ {affectedCount} 项 · {totalDiffCount} 处变更</span>}
                        {/* M30.145 B 备份预检内联审计汇总 */}
                        {(() => {
                          const audit = backupItemAudit ?? {};
                          const presets = items.filter((i) => i.kind === 'preset');
                          const auditedPresets = presets.filter((i) => audit[i.key]).length;
                          if (presets.length === 0) return null;
                          if (backupItemAuditBusy && auditedPresets === 0) {
                            return <span style={{ marginLeft: 4, color: 'var(--muted)' }}>⏳ 审计中…</span>;
                          }
                          const dead = presets.filter((i) => audit[i.key] === 'dead').length;
                          const ok = presets.filter((i) => audit[i.key] === 'ok').length;
                          const parts: ReactNode[] = [];
                          if (ok > 0) parts.push(<span key="ok" style={{ color: 'var(--success, #10b981)', marginLeft: 6 }}>✅ {ok} 存活</span>);
                          if (dead > 0) parts.push(<span key="dead" style={{ color: 'var(--danger, #dc2626)', marginLeft: 4, fontWeight: 700 }} title="自动取消勾选，可手动重新勾选（需先修好目标表/列）">❌ {dead} 失效</span>);
                          return <>{parts}</>;
                        })()}
                      </div>
                      <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                        {/* M30.159 A 备份预检顶部 diff 摘要条：把头行散点信号汇总成一行紧凑总览；每个 chip 点击切到对应过滤器 */}
                        {(() => {
                          const schemaSet = new Set(items.map((i) => i.schema));
                          const changedCols = items.reduce((acc, i) => acc + diffCountOf(i), 0);
                          const riskLow = items.filter((i) => computeItemRiskScore(i).level === 'low').length;
                          const riskMid = items.filter((i) => computeItemRiskScore(i).level === 'mid').length;
                          const riskHigh = items.filter((i) => computeItemRiskScore(i).level === 'high').length;
                          const chip = (label: string, active: boolean, onClick: () => void, color: string, title: string) => (
                            <button
                              type="button"
                              onClick={onClick}
                              title={title}
                              style={{
                                padding: '0 5px', fontSize: 10, borderRadius: 3,
                                border: `1px solid ${active ? color : 'var(--border)'}`,
                                background: active ? `${color}1a` : 'transparent',
                                color: active ? color : 'var(--muted)',
                                cursor: 'pointer', fontFamily: 'inherit',
                                fontWeight: active ? 600 : 400,
                              }}
                            >{label}</button>
                          );
                          return (
                            <div
                              style={{
                                display: 'flex', gap: 4, flexWrap: 'wrap',
                                padding: '2px 6px', marginBottom: 4,
                                background: 'rgba(148,163,184,0.06)',
                                border: '1px solid var(--border)',
                                borderRadius: 3,
                                alignItems: 'center',
                              }}
                              title="一行总览：点击各 chip 快速切换预检过滤器（正交于其他过滤器）"
                            >
                              <span style={{ fontSize: 9, color: 'var(--muted)', fontWeight: 600, letterSpacing: '0.02em' }}>DIFF</span>
                              {chip(`⚠ ${items.filter((i) => i.action === 'overwrite').length}`, backupPreviewFilter === 'overwrite', () => setBackupPreviewFilter('overwrite'), 'var(--warn, #d97706)', '⚠ 将覆盖项 · 点击筛选')}
                              {chip(`✅ ${items.filter((i) => i.action === 'add').length}`, backupPreviewFilter === 'add', () => setBackupPreviewFilter('add'), 'var(--success, #10b981)', '✅ 新增项 · 点击筛选')}
                              {chip(`📦 ${schemaSet.size} schema`, false, () => {}, 'var(--info, #8b5cf6)', `跨 ${schemaSet.size} 个 schema：${Array.from(schemaSet).slice(0, 5).join(', ')}${schemaSet.size > 5 ? '…' : ''}`)}
                              {chip(`📊 ${changedCols} 变更`, false, () => setBackupSortBy('diff'), 'var(--danger, #dc2626)', '覆盖项字段级 diff 总变更数 · 点击按变更数排序')}
                              <span style={{ fontSize: 9, color: 'var(--muted)', fontWeight: 600, letterSpacing: '0.02em', marginLeft: 4 }}>RISK</span>
                              {chip(`🟢 ${riskLow}`, backupRiskLevelFilter === 'low', () => setBackupRiskLevelFilter('low'), 'var(--success, #10b981)', '低风险项 · 点击筛选')}
                              {chip(`🟡 ${riskMid}`, backupRiskLevelFilter === 'mid', () => setBackupRiskLevelFilter('mid'), 'var(--warn, #d97706)', '中风险项 · 点击筛选')}
                              {chip(`🔴 ${riskHigh}`, backupRiskLevelFilter === 'high', () => setBackupRiskLevelFilter('high'), 'var(--danger, #dc2626)', '高风险项 · 点击筛选')}
                              {/* M30.161 C 风险阈值滑块：显示 riskScore >= threshold 的项，与 riskLevelFilter AND */}
                              <span style={{ fontSize: 9, color: 'var(--muted)', fontWeight: 600, letterSpacing: '0.02em', marginLeft: 4 }}>≥</span>
                              <input
                                type="range"
                                min={0}
                                max={100}
                                step={5}
                                value={backupRiskThreshold}
                                onChange={(e) => setBackupRiskThreshold(Number(e.target.value))}
                                style={{ width: 90, accentColor: 'var(--accent, #3b82f6)', cursor: 'pointer' }}
                                title={`只显示 riskScore ≥ ${backupRiskThreshold} 的项 · 与 riskLevelFilter AND 叠加 · 0=全部`}
                              />
                              <span style={{ fontSize: 10, fontFamily: 'monospace', color: backupRiskThreshold > 0 ? 'var(--accent, #3b82f6)' : 'var(--muted)', minWidth: 24 }}>
                                {backupRiskThreshold}
                                <span style={{ color: 'var(--muted)' }}>/100</span>
                              </span>
                              {backupRiskThreshold > 0 && (
                                <button
                                  type="button"
                                  onClick={() => setBackupRiskThreshold(0)}
                                  style={{ padding: '0 4px', fontSize: 9, background: 'transparent', color: 'var(--muted)', border: 'none', cursor: 'pointer' }}
                                  title="重置阈值为 0（不过滤）"
                                >×0</button>
                              )}
                              {/* M30.162 A 备份风险分数直方图：10 桶 [0-10)…[90-100]，桶高按计数归一，点击桶设 threshold=桶左边界 */}
                              {items.length > 0 && (() => {
                                const buckets: { lo: number; count: number }[] = [];
                                for (let b = 0; b < 10; b++) buckets.push({ lo: b * 10, count: 0 });
                                for (const i of items) {
                                  const s = computeItemRiskScore(i).score;
                                  const b = Math.min(9, Math.floor(s / 10));
                                  buckets[b].count += 1;
                                }
                                const maxCount = Math.max(1, ...buckets.map((b) => b.count));
                                const midColor = (lo: number) => {
                                  const mid = lo + 5;
                                  return mid >= 50 ? 'var(--danger, #dc2626)'
                                    : mid >= 20 ? 'var(--warn, #d97706)'
                                    : 'var(--success, #10b981)';
                                };
                                const activeLo = Math.floor(Math.min(100, Math.max(0, backupRiskThreshold)) / 10) * 10;
                                return (
                                  <span
                                    style={{ display: 'inline-flex', alignItems: 'flex-end', gap: 1, height: 20, marginLeft: 4, padding: '1px 2px', border: '1px solid var(--border)', borderRadius: 3, background: 'rgba(148,163,184,0.06)' }}
                                    title="风险分数分布：10 桶 · 点击桶设阈值到桶左边界"
                                  >
                                    {buckets.map((b, idx) => {
                                      const h = (b.count / maxCount) * 100;
                                      const isCur = b.lo === activeLo;
                                      return (
                                        <span
                                          key={idx}
                                          onClick={() => {
                                            setBackupRiskThreshold(b.lo);
                                            setPresetManagerMsg({ kind: 'ok', text: `📊 阈值跳到 [${b.lo}-${b.lo + 9}] 段（${b.count} 项 · Alt+S 分组联动）` });
                                          }}
                                          title={`${b.lo}-${b.lo + 9}: ${b.count} 项 · 点击设阈值到 ${b.lo}`}
                                          style={{
                                            width: 6,
                                            height: `${Math.max(2, h)}%`,
                                            background: b.count === 0 ? 'rgba(148,163,184,0.2)' : midColor(b.lo),
                                            opacity: b.count === 0 ? 1 : (isCur ? 1 : 0.55),
                                            borderRadius: 1,
                                            cursor: b.count > 0 ? 'pointer' : 'default',
                                            border: isCur ? '1px solid var(--accent, #3b82f6)' : 'none',
                                            boxSizing: 'border-box',
                                          }}
                                        />
                                      );
                                    })}
                                  </span>
                                );
                              })()}
                            </div>
                          );
                        })()}
                        {(() => {
                          const actionLabel = backupPreviewFilter === 'all' ? '全部'
                            : backupPreviewFilter === 'overwrite' ? '将覆盖' : '新增';
                          const kindLabel = backupPreviewKindFilter === 'all' ? ''
                            : backupPreviewKindFilter === 'preset' ? '预设' : '快照';
                          const searchLabel = backupPreviewSearch.trim() ? `含「${backupPreviewSearch.trim()}」` : '';
                          const curFilterLabel = [actionLabel, kindLabel, searchLabel].filter(Boolean).join('·');
                          const matchCur = items.filter((i) => {
                            if (backupPreviewFilter !== 'all' && i.action !== backupPreviewFilter) return false;
                            if (backupPreviewKindFilter !== 'all' && i.kind !== backupPreviewKindFilter) return false;
                            if (searchQ && !i.schema.toLowerCase().includes(searchQ) && !i.table.toLowerCase().includes(searchQ)) return false;
                            return true;
                          });
                          const unselectedCount = matchCur.filter((i) => !backupSelected.has(i.key)).length;
                          const selectedInCur = matchCur.length - unselectedCount;
                          return (
                            <>
                              <button
                                type="button"
                                disabled={unselectedCount === 0}
                                onClick={() => {
                                  const next = new Set(backupSelected);
                                  for (const i of matchCur) next.add(i.key);
                                  setBackupSelected(next);
                                }}
                                style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, opacity: unselectedCount === 0 ? 0.5 : 1, cursor: unselectedCount === 0 ? 'not-allowed' : 'pointer' }}
                                title={`把「${curFilterLabel}」中未选的 ${unselectedCount} 项加入勾选（保留其他已选） · Alt+S`}
                              >📌 选中「{curFilterLabel}」</button>
                              <button
                                type="button"
                                disabled={matchCur.length === 0}
                                onClick={() => {
                                  const next = new Set(backupSelected);
                                  for (const i of matchCur) {
                                    if (next.has(i.key)) next.delete(i.key); else next.add(i.key);
                                  }
                                  setBackupSelected(next);
                                }}
                                style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, opacity: matchCur.length === 0 ? 0.5 : 1, cursor: matchCur.length === 0 ? 'not-allowed' : 'pointer' }}
                                title={`对「${curFilterLabel}」中命中项做反选（当前已选 ${selectedInCur}/${matchCur.length}） · Alt+Shift+S`}
                              >⇄ 反选「{curFilterLabel}」</button>
                            </>
                          );
                        })()}
                        <button
                          type="button"
                          onClick={() => setBackupSelected(new Set(items.map((i) => i.key)))}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10 }}
                          title="全选"
                        >全选</button>
                        <button
                          type="button"
                          onClick={() => setBackupSelected(new Set())}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10 }}
                          title="全不选"
                        >清空</button>
                        <button
                          type="button"
                          onClick={() => {
                            if (!backupPending) return;
                            const audit = backupItemAudit ?? {};
                            const next = new Set<string>();
                            let removed = 0;
                            for (const i of backupPending.items) {
                              if (!backupSelected.has(i.key)) continue;
                              const parts = i.key.split('::');
                              const cross = parts[0] && parts[0] !== connId;
                              if (audit[i.key] === 'dead' || cross) { removed++; continue; }
                              let dc = 0;
                              if (i.action === 'overwrite' && i.diff) {
                                const d = i.diff;
                                dc = d.scalarDiffs.length + d.removedTargets.length + d.addedTargets.length
                                  + Object.keys(d.removedCols).length + Object.keys(d.addedCols).length;
                              }
                              if (i.action === 'overwrite' && dc > 3) { removed++; continue; }
                              next.add(i.key);
                            }
                            setBackupSelected(next);
                            setPresetManagerMsg({
                              kind: removed > 0 ? 'ok' : 'err',
                              text: removed > 0
                                ? `🛡 已剔除 ${removed} 个高风险项（失效/跨连接/大改动覆盖）`
                                : '✅ 已选集合没有高风险项',
                            });
                          }}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--warn, #d97706)' }}
                          title="一键剔除已选高风险项（失效/跨连接/diff>3 覆盖）"
                        >🛡 只留低风险</button>
                        <button
                          type="button"
                          onClick={() => exportBackupReport('json')}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--info, #8b5cf6)' }}
                          title="导出预检报告为 JSON（含汇总+每 item 详情）"
                        >📊 报告 JSON</button>
                        <button
                          type="button"
                          onClick={() => exportBackupReport('csv')}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--info, #8b5cf6)' }}
                          title="导出预检报告为 CSV（每行一 item，便于 Excel/透视）"
                        >📊 报告 CSV</button>
                        <button
                          type="button"
                          onClick={() => exportBackupReport('md')}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--info, #8b5cf6)' }}
                          title="导出预检报告为 Markdown（可读汇总+表格）"
                        >📊 报告 MD</button>
                        <button
                          type="button"
                          onClick={async () => {
                            // M30.162 C 备份预检复制选中 keys 到剪贴板：把 backupSelected 全部 key 按行拼接复制，便于工单/邮件粘贴
                            const keys = Array.from(backupSelected);
                            if (keys.length === 0) {
                              setPresetManagerMsg({ kind: 'err', text: '⚠ 没有已勾选的项可复制' });
                              return;
                            }
                            try {
                              await navigator.clipboard.writeText(keys.join('\n'));
                              setPresetManagerMsg({ kind: 'ok', text: `📋 已复制选中 ${keys.length} 个 key（换行分隔）` });
                            } catch {
                              setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败（浏览器剪贴板权限）' });
                            }
                          }}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--info, #8b5cf6)' }}
                          title="把当前勾选的所有备份项 key 按行复制到剪贴板（可粘贴到工单/邮件/其他工具）"
                        >📋 复制选中 {backupSelected.size > 0 ? `${backupSelected.size} key` : ''}</button>
                        <button
                          type="button"
                          onClick={() => {
                            // M30.158 C 备份预检 filter 状态一键复制：把当前 filter/sort/search/riskLevel 序列化为 JSON，方便跨会话/跨浏览器分享
                            const state = {
                              filter: backupPreviewFilter,
                              kindFilter: backupPreviewKindFilter,
                              sortBy: backupSortBy,
                              search: backupPreviewSearch,
                              riskLevelFilter: backupRiskLevelFilter,
                              riskThreshold: backupRiskThreshold,
                              previewAllDiff: backupPreviewAllDiff,
                              copyAt: new Date().toISOString(),
                            };
                            const payload = JSON.stringify(state, null, 2);
                            void (async () => {
                              try {
                                await navigator.clipboard.writeText(payload);
                                setPresetManagerMsg({
                                  kind: 'ok',
                                  text: `📋 已复制预检 filter 状态（filter=${backupPreviewFilter} kind=${backupPreviewKindFilter} sort=${backupSortBy} risk=${backupRiskLevelFilter}≥${backupRiskThreshold} search="${backupPreviewSearch || '∅'}"）`,
                                });
                              } catch {
                                setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败（浏览器剪贴板权限）' });
                              }
                            })();
                          }}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--info, #8b5cf6)' }}
                          title="把当前 filter 状态复制为 JSON（可粘贴到其他会话/浏览器作为分享配置；含动作/类型/搜索/排序/风险级别/预览所有 diff）"
                        >📋 复制 filter</button>
                        <button
                          type="button"
                          onClick={async () => {
                            // M30.158 C 粘贴 filter 状态：从剪贴板读 JSON 恢复到当前视图
                            let text: string;
                            try { text = await navigator.clipboard.readText(); }
                            catch {
                              setPresetManagerMsg({ kind: 'err', text: '❌ 读取剪贴板失败（浏览器权限）' });
                              return;
                            }
                            try {
                              const p = JSON.parse(text) as Partial<Record<'filter' | 'kindFilter' | 'sortBy' | 'search' | 'riskLevelFilter' | 'riskThreshold' | 'previewAllDiff', unknown>>;
                              if (!p || typeof p !== 'object') throw new Error('non-object');
                              let n = 0;
                              if (p.filter === 'all' || p.filter === 'overwrite' || p.filter === 'add') { setBackupPreviewFilter(p.filter); n += 1; }
                              if (p.kindFilter === 'all' || p.kindFilter === 'preset' || p.kindFilter === 'snapshot') { setBackupPreviewKindFilter(p.kindFilter); n += 1; }
                              if (p.sortBy === 'diff' || p.sortBy === 'schema' || p.sortBy === 'risk') { setBackupSortBy(p.sortBy as 'diff' | 'schema' | 'risk'); n += 1; }
                              if (typeof p.search === 'string') { setBackupPreviewSearch(p.search); n += 1; }
                              if (p.riskLevelFilter === 'all' || p.riskLevelFilter === 'low' || p.riskLevelFilter === 'mid' || p.riskLevelFilter === 'high') { setBackupRiskLevelFilter(p.riskLevelFilter); n += 1; }
                              if (typeof p.riskThreshold === 'number' && isFinite(p.riskThreshold)) { setBackupRiskThreshold(Math.max(0, Math.min(100, Math.round(p.riskThreshold)))); n += 1; }
                              if (typeof p.previewAllDiff === 'boolean') { setBackupPreviewAllDiff(p.previewAllDiff); n += 1; }
                              if (n === 0) throw new Error('no fields matched');
                              setPresetManagerMsg({ kind: 'ok', text: `✅ 已恢复 filter 状态（${n} 项生效）` });
                            } catch {
                              setPresetManagerMsg({ kind: 'err', text: '❌ 剪贴板不是有效的 filter JSON（需含 filter/kindFilter/sortBy/search/riskLevelFilter/previewAllDiff 中的字段）' });
                            }
                          }}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--info, #8b5cf6)' }}
                          title="从剪贴板读取 filter JSON 并应用到当前视图（与「复制 filter」配对使用；仅识别已知字段，忽略未知字段）"
                        >📥 粘贴 filter</button>
                        <button
                          type="button"
                          onClick={resetBackupView}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--muted)' }}
                          title="一键重置视图：清除动作/类型/搜索/排序/预览所有 diff 切换（Alt+R）"
                        >🔄 重置视图</button>
                        <button
                          type="button"
                          onClick={cancelBackup}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--muted)' }}
                          title="取消本次导入，关闭预检面板（Esc）"
                        >✕ 取消</button>
                      </div>
                    </div>
                    {items.length > 0 && (() => {
                      const total = items.length;
                      const searchQ = backupPreviewSearch.trim().toLowerCase();
                      const searchMatch = (i: BackupPreviewItem) => !searchQ || i.schema.toLowerCase().includes(searchQ) || i.table.toLowerCase().includes(searchQ);
                      const actionMatch = (i: BackupPreviewItem) => backupPreviewFilter === 'all' || i.action === backupPreviewFilter;
                      const kindMatch = (i: BackupPreviewItem) => backupPreviewKindFilter === 'all' || i.kind === backupPreviewKindFilter;
                      const overwriteCount = backupPreviewKindFilter === 'all' ? items.filter((i) => i.action === 'overwrite' && searchMatch(i)).length : items.filter((i) => i.action === 'overwrite').filter(kindMatch).filter(searchMatch).length;
                      const addCount = backupPreviewKindFilter === 'all' ? items.filter((i) => i.action === 'add' && searchMatch(i)).length : items.filter((i) => i.action === 'add').filter(kindMatch).filter(searchMatch).length;
                      const presetCount = backupPreviewFilter === 'all' ? items.filter((i) => i.kind === 'preset' && searchMatch(i)).length : items.filter((i) => i.kind === 'preset').filter(actionMatch).filter(searchMatch).length;
                      const snapshotCount = backupPreviewFilter === 'all' ? items.filter((i) => i.kind === 'snapshot' && searchMatch(i)).length : items.filter((i) => i.kind === 'snapshot').filter(actionMatch).filter(searchMatch).length;
                      // M30.158 A 风险级别分布：low/mid/high 三档计数（仅受 search 影响，正交于 action/kind filter）
                      const riskLevelCounts = (() => {
                        const acc: Record<'low' | 'mid' | 'high', number> = { low: 0, mid: 0, high: 0 };
                        for (const i of items) {
                          if (!searchMatch(i)) continue;
                          acc[computeItemRiskScore(i).level] += 1;
                        }
                        return acc;
                      })();
                      const visible = items.filter((i) => actionMatch(i) && kindMatch(i) && searchMatch(i));
                      const chipBase: CSSProperties = { padding: '1px 6px', fontSize: 10, borderRadius: 3, border: '1px solid var(--border)', cursor: 'pointer', background: 'transparent', color: 'var(--muted)' };
                      const chipActive: CSSProperties = { border: '1px solid var(--accent, #3b82f6)', color: 'var(--accent, #3b82f6)', background: 'rgba(59,130,246,0.10)' };
                      const kindActive: CSSProperties = { border: '1px solid var(--info, #8b5cf6)', color: 'var(--info, #8b5cf6)', background: 'rgba(139,92,246,0.10)' };
                      // M30.138 filter 命中范围实时已选/未选统计（仅非 all 时追加，避免与 header 全量重复）
                      const visSelected = visible.filter((i) => backupSelected.has(i.key)).length;
                      const visUnselected = visible.length - visSelected;
                      const filterActive = backupPreviewFilter !== 'all' || backupPreviewKindFilter !== 'all' || backupPreviewSearch.trim() !== '';
                      return (
                        <div style={{ display: 'flex', gap: 4, alignItems: 'center', marginBottom: 4, fontSize: 10, flexWrap: 'wrap' }}>
                          <span style={{ color: 'var(--muted)' }}>搜索：</span>
                          <input
                            id="backup-preview-search"
                            type="text"
                            value={backupPreviewSearch}
                            onChange={(e) => setBackupPreviewSearch(e.target.value)}
                            placeholder="schema.table 子串（Alt+/ 聚焦）"
                            style={{ padding: '1px 5px', fontSize: 10, borderRadius: 3, border: '1px solid var(--border)', background: 'transparent', color: 'var(--text)', width: 140, fontFamily: 'inherit' }}
                            title="按 schema 或 table 子串过滤备份项（Alt+/ 聚焦）"
                          />
                          {backupPreviewSearch && (
                            <button type="button" onClick={() => setBackupPreviewSearch('')} style={{ ...styles.btnSm, padding: '0 4px', fontSize: 10, color: 'var(--muted)' }} title="清空搜索">×</button>
                          )}
                          <span style={{ color: 'var(--muted)', marginLeft: 4 }}>动作：</span>
                          <button type="button" onClick={() => setBackupPreviewFilter('all')} title="显示全部动作（Alt+Q）" style={{ ...chipBase, ...(backupPreviewFilter === 'all' ? chipActive : {}) }}>全部 {total}</button>
                          <button type="button" onClick={() => setBackupPreviewFilter('overwrite')} title="仅显示将覆盖项（Alt+W）" style={{ ...chipBase, ...(backupPreviewFilter === 'overwrite' ? { border: '1px solid var(--warn, #d97706)', color: 'var(--warn, #d97706)', background: 'rgba(217,119,6,0.10)' } : {}) }}>⚠️ 将覆盖 {overwriteCount}</button>
                          <button type="button" onClick={() => setBackupPreviewFilter('add')} title="仅显示新增项（Alt+E）" style={{ ...chipBase, ...(backupPreviewFilter === 'add' ? { border: '1px solid var(--success, #10b981)', color: 'var(--success, #10b981)', background: 'rgba(16,185,129,0.10)' } : {}) }}>✅ 新增 {addCount}</button>
                          {/* M30.158 A 风险分布条：6px 三段 flex low/mid/high，点击切 filter，正交于 action/kind 过滤器 */}
                          <span
                            style={{
                              marginLeft: 4, display: 'inline-flex', alignItems: 'center', gap: 3,
                              padding: '0 5px', borderRadius: 3, border: '1px solid var(--border)', background: 'transparent',
                              fontSize: 10, color: 'var(--muted)',
                            }}
                            title={`风险分布：🟢 低 / 🟡 中 / 🔴 高 · 点击段切换 filter · 当前: ${backupRiskLevelFilter === 'all' ? '全部' : backupRiskLevelFilter}`}
                          >
                            <span style={{ fontSize: 9 }}>⚠</span>
                            <span style={{ display: 'inline-flex', width: 60, height: 8, borderRadius: 2, overflow: 'hidden', background: 'rgba(0,0,0,0.05)' }}>
                              {(() => {
                                const t = riskLevelCounts.low + riskLevelCounts.mid + riskLevelCounts.high;
                                const pct = (n: number) => t === 0 ? 0 : (n / t) * 100;
                                const color = (lvl: 'low' | 'mid' | 'high') =>
                                  lvl === 'low' ? 'var(--success, #10b981)'
                                  : lvl === 'mid' ? 'var(--warn, #d97706)'
                                  : 'var(--danger, #dc2626)';
                                const isActive = (lvl: 'low' | 'mid' | 'high') => backupRiskLevelFilter === lvl;
                                const icon = (lvl: 'low' | 'mid' | 'high') =>
                                  lvl === 'low' ? '🟢' : lvl === 'mid' ? '🟡' : '🔴';
                                const label = (lvl: 'low' | 'mid' | 'high') =>
                                  lvl === 'low' ? '低' : lvl === 'mid' ? '中' : '高';
                                return (['low', 'mid', 'high'] as const).map((lvl) => {
                                  const n = riskLevelCounts[lvl];
                                  if (n === 0) return null;
                                  const w = pct(n);
                                  const sel = isActive(lvl);
                                  return (
                                    <span
                                      key={lvl}
                                      style={{
                                        width: `${Math.max(w, 3)}%`,
                                        background: color(lvl),
                                        opacity: backupRiskLevelFilter === 'all' || sel ? 0.9 : 0.35,
                                        outline: sel ? '1.5px solid var(--text)' : undefined,
                                        outlineOffset: -1,
                                        cursor: 'pointer',
                                        transition: 'opacity 0.15s',
                                      }}
                                      title={`${icon(lvl)} ${label(lvl)} 风险 ${n} 项（${Math.round(w)}%）· 点击${sel ? '取消' : ''}筛选`}
                                      onClick={(ev) => {
                                        ev.stopPropagation();
                                        setBackupRiskLevelFilter(sel ? 'all' : lvl);
                                      }}
                                    />
                                  );
                                });
                              })()}
                            </span>
                            <span style={{ fontFamily: 'monospace', fontSize: 9, letterSpacing: '0.02em' }}>
                              <span style={{ color: 'var(--success, #10b981)' }}>🟢{riskLevelCounts.low}</span>
                              <span style={{ color: 'var(--warn, #d97706)', marginLeft: 1 }}>🟡{riskLevelCounts.mid}</span>
                              <span style={{ color: 'var(--danger, #dc2626)', marginLeft: 1 }}>🔴{riskLevelCounts.high}</span>
                            </span>
                            {backupRiskLevelFilter !== 'all' && (
                              <button
                                type="button"
                                style={{ padding: '0 3px', borderRadius: 2, fontSize: 9, background: 'transparent', color: 'var(--muted)', border: '1px solid var(--border)', cursor: 'pointer', lineHeight: 1 }}
                                title="重置风险 filter"
                                onClick={() => setBackupRiskLevelFilter('all')}
                              >✕</button>
                            )}
                          </span>
                          <span style={{ color: 'var(--muted)', marginLeft: 4 }}>类型：</span>
                          <button type="button" onClick={() => setBackupPreviewKindFilter('all')} title="显示全部类型（Alt+1）" style={{ ...chipBase, ...(backupPreviewKindFilter === 'all' ? chipActive : {}) }}>全部 {total}</button>
                          <button type="button" onClick={() => setBackupPreviewKindFilter('preset')} title="仅显示预设（Alt+2）" style={{ ...chipBase, ...(backupPreviewKindFilter === 'preset' ? kindActive : {}) }}>📄 预设 {presetCount}</button>
                          <button type="button" onClick={() => setBackupPreviewKindFilter('snapshot')} title="仅显示快照（Alt+3）" style={{ ...chipBase, ...(backupPreviewKindFilter === 'snapshot' ? kindActive : {}) }}>📸 快照 {snapshotCount}</button>
                          {backupDiffBinFilter !== 'all' && (() => {
                            const binLabels: Record<'0' | '1' | '2' | '3', string> = { '0': '一致', '1': '微调', '2': '中等', '3': '大改' };
                            const binRanges: Record<'0' | '1' | '2' | '3', string> = { '0': '⇄0', '1': '⇄1-3', '2': '⇄4-10', '3': '⇄11+' };
                            return (
                              <button
                                type="button"
                                onClick={() => setBackupDiffBinFilter('all')}
                                title="分档筛选（点击环形图分档色块时启用）· 点击复位"
                                style={{ ...chipBase, marginLeft: 4, border: '1px solid var(--accent, #3b82f6)', color: 'var(--accent, #3b82f6)', background: 'rgba(59,130,246,0.08)' }}
                              >分档：{binLabels[backupDiffBinFilter]} {binRanges[backupDiffBinFilter]} ✕</button>
                            );
                          })()}
                          <span style={{ color: 'var(--muted)', marginLeft: 4 }}>排序：</span>
                          <button type="button" onClick={() => setBackupSortBy('diff')} title="按字段级变更数降序（大改动前置）" style={{ ...chipBase, ...(backupSortBy === 'diff' ? chipActive : {}) }}>⇄ 变更数</button>
                          <button type="button" onClick={() => setBackupSortBy('risk')} title="按风险排序：审计失效 > 其他连接 > 变更数 > 覆盖 > schema（Alt+Shift+R）" style={{ ...chipBase, ...(backupSortBy === 'risk' ? chipActive : {}) }}>⚠ 风险</button>
                          <button type="button" onClick={() => setBackupSortBy('schema')} title="按 schema.table 字典序" style={{ ...chipBase, ...(backupSortBy === 'schema' ? chipActive : {}) }}>A→Z schema</button>
                          <button
                            type="button"
                            onClick={() => setBackupPreviewAllDiff((v) => !v)}
                            title="开启后所有「将覆盖」预设项都展开 diff，不再依赖逐项勾选（Alt+V 切换）"
                            style={{ ...chipBase, marginLeft: 4, ...(backupPreviewAllDiff ? { border: '1px solid var(--warn, #d97706)', color: 'var(--warn, #d97706)', background: 'rgba(217,119,6,0.10)' } : {}) }}
                          >🔍 预览所有 diff</button>
                          <button
                            type="button"
                            onClick={() => {
                              const next = new Set(backupDiffCollapsed);
                              if (next.size === 0) {
                                for (const it of items) {
                                  if (it.action === 'overwrite' && it.kind === 'preset' && it.diff) next.add(it.key);
                                }
                              } else {
                                next.clear();
                              }
                              setBackupDiffCollapsed(next);
                            }}
                            title="切换所有 diff 明细的展开/收起状态（焦点项也可用 Enter 单独切换）"
                            style={{ ...chipBase, ...(backupDiffCollapsed.size > 0 ? { border: '1px solid var(--accent, #3b82f6)', color: 'var(--accent, #3b82f6)', background: 'rgba(59,130,246,0.08)' } : {}) }}
                          >{backupDiffCollapsed.size > 0 ? '⊕ 展开全部' : '⊖ 收起全部'}</button>
                          <button
                            type="button"
                            onClick={() => setBackupShortcutsOpen((v) => !v)}
                            title="备份预检快捷键速查（? 切换）"
                            style={{ ...chipBase, marginLeft: 4, ...(backupShortcutsOpen ? chipActive : {}) }}
                          >⌨ 快捷键 (?)</button>
                          <button
                            type="button"
                            onClick={() => {
                              const next = !backupGroupBy;
                              setBackupGroupBy(next);
                              if (!next) setBackupCollapsedGroups(new Set());
                            }}
                            title="按 schema.table 分组折叠列表（Alt+A 展开/收起全部）"
                            style={{
                              ...chipBase, marginLeft: 4,
                              ...(backupGroupBy ? { border: '1px solid var(--info, #8b5cf6)', color: 'var(--info, #8b5cf6)', background: 'rgba(139,92,246,0.10)' } : {}),
                            }}
                          >🗂 分组</button>
                          <span style={{ color: 'var(--muted)', marginLeft: 'auto' }}>
                            显示 {visible.length}/{total}
                            {filterActive && (
                              <>
                                {' · '}
                                <span style={{ color: 'var(--success, #10b981)' }} title="当前筛选范围内已勾选项数">✓ {visSelected}</span>
                                {' · '}
                                <span title="当前筛选范围内未勾选项数">⊘ {visUnselected}</span>
                              </>
                            )}
                          </span>
                        </div>
                      );
                    })()}
                    {/* M30.152 A：审计状态分布条（仅审计已就绪且存在预设项时显示） */}
                    {backupItemAudit && (() => {
                      const presets = items.filter((i) => i.kind === 'preset');
                      if (presets.length === 0) return null;
                      let ok = 0, warn = 0, dead = 0, unAudited = 0;
                      for (const i of presets) {
                        const st = backupItemAudit[i.key];
                        if (st === 'ok') ok++;
                        else if (st === 'warn') warn++;
                        else if (st === 'dead') dead++;
                        else unAudited++;
                      }
                      const audited = ok + warn + dead;
                      if (audited === 0 && !backupItemAuditBusy) return null;
                      const total = audited || 1;
                      const pct = (n: number) => `${(n / total) * 100}%`;
                      return (
                        <div
                          style={{
                            marginBottom: 4, padding: '4px 6px', borderRadius: 3,
                            border: '1px solid var(--border)', background: 'rgba(0,0,0,0.02)',
                            fontSize: 10, display: 'flex', gap: 8, alignItems: 'center',
                            flexWrap: 'wrap',
                          }}
                          title="预设项健康审计结果分布（仅算已审计的预设）"
                        >
                          <span style={{ color: 'var(--muted)' }}>🩺 审计</span>
                          <div style={{ flex: 1, minWidth: 120, height: 6, display: 'flex', borderRadius: 3, overflow: 'hidden', background: 'rgba(0,0,0,0.05)' }}>
                            {ok > 0 && <div style={{ width: pct(ok), background: 'var(--success, #10b981)' }} title={`✅ 正常 ${ok}`} />}
                            {warn > 0 && <div style={{ width: pct(warn), background: 'var(--warn, #d97706)' }} title={`⚠ 部分列缺失 ${warn}`} />}
                            {dead > 0 && <div style={{ width: pct(dead), background: 'var(--danger, #dc2626)' }} title={`❌ 失效 ${dead}`} />}
                          </div>
                          <span style={{ display: 'inline-flex', gap: 6, color: 'var(--muted)' }}>
                            <span style={{ color: 'var(--success, #10b981)', fontWeight: 600 }} title="✅ 正常">✓ {ok}</span>
                            <span style={{ color: 'var(--warn, #d97706)', fontWeight: 600 }} title="⚠ 部分列缺失">⚠ {warn}</span>
                            <span style={{ color: 'var(--danger, #dc2626)', fontWeight: 600 }} title="❌ 失效">✗ {dead}</span>
                            {unAudited > 0 && (
                              <span title={`尚未审计（${backupItemAuditBusy ? '审计中…' : '等待触发'}）`}>
                                ⋯ {unAudited}
                              </span>
                            )}
                          </span>
                          {backupItemAuditBusy && <span style={{ color: 'var(--muted)' }}>审计中…</span>}
                          {!backupItemAuditBusy && (warn > 0 || dead > 0 || unAudited > 0) && (() => {
                            // M30.153 B：一键重审——只重跑非 ok 项，绕开 24h 缓存
                            const toRefresh = presets.filter((i) => {
                              const st = backupItemAudit[i.key];
                              if (st === 'ok') return false;
                              // 跨连接预设跳过（M30.145 B 同样约定）
                              const parts = i.key.split('::');
                              return !(parts.length >= 1 && parts[0] && parts[0] !== connId);
                            });
                            if (toRefresh.length === 0) return null;
                            return (
                              <button
                                type="button"
                                disabled={backupItemAuditBusy}
                                onClick={() => {
                                  setBackupItemAuditBusy(true);
                                  void (async () => {
                                    const next: Record<string, PresetAuditStatus> = {};
                                    const patch: Record<string, { status: PresetAuditStatus; reason: string }> = {};
                                    for (const it of toRefresh) {
                                      try {
                                        await api.listColumns(connId, it.schema, it.table);
                                        next[it.key] = 'ok';
                                        patch[it.key] = { status: 'ok', reason: '表/列存活' };
                                      } catch {
                                        next[it.key] = 'dead';
                                        patch[it.key] = { status: 'dead', reason: '表或列不存在' };
                                      }
                                    }
                                    // 覆盖式更新：把已重审项的状态替换，保留其他项不变
                                    setBackupItemAudit((prev) => {
                                      const merged = { ...(prev ?? {}) };
                                      for (const [k, v] of Object.entries(next)) merged[k] = v;
                                      return merged;
                                    });
                                    // 回写缓存
                                    const existing = loadAuditCache(connId);
                                    const mergedCache: Record<string, { status: PresetAuditStatus; reason: string }> = {};
                                    for (const [k, v] of Object.entries(existing?.results ?? {})) {
                                      mergedCache[k] = { status: v.status, reason: v.reason };
                                    }
                                    for (const [k, v] of Object.entries(patch)) mergedCache[k] = v;
                                    saveAuditCache(connId, mergedCache);
                                    setBackupItemAuditBusy(false);
                                    setPresetManagerMsg({ kind: 'ok', text: `🩺 已重审 ${toRefresh.length} 项，缓存已刷新` });
                                  })();
                                }}
                                style={{
                                  padding: '1px 6px', fontSize: 10, borderRadius: 3,
                                  border: '1px solid var(--info, #3b82f6)',
                                  color: 'var(--info, #3b82f6)',
                                  background: 'rgba(59,130,246,0.08)',
                                  cursor: 'pointer',
                                }}
                                title="绕开 24h 缓存，仅重跑非 ok 项（跳过跨连接项）"
                              >🔄 重审（{toRefresh.length}）</button>
                            );
                          })()}
                        </div>
                      );
                    })()}
                    {backupGroupBy && (() => {
                      const vis = items.filter((i) => {
                        if (backupPreviewFilter !== 'all' && i.action !== backupPreviewFilter) return false;
                        if (backupPreviewKindFilter !== 'all' && i.kind !== backupPreviewKindFilter) return false;
                        if (searchQ && !i.schema.toLowerCase().includes(searchQ) && !i.table.toLowerCase().includes(searchQ)) return false;
                        return true;
                      });
                      const chipBase: CSSProperties = { padding: '0 4px', fontSize: 10, borderRadius: 3, border: '1px solid var(--border)', cursor: 'pointer', background: 'transparent', color: 'var(--muted)' };
                      const groups = new Map<string, typeof vis>();
                      for (const i of vis) {
                        const gk = `${i.schema}::${i.table}`;
                        if (!groups.has(gk)) groups.set(gk, []);
                        groups.get(gk)!.push(i);
                      }
                      // M30.151 C 分组排序：diff 按组内变更总数降序 / name 按 schema.table 字母序 / M30.164 C risk 按组内平均风险分降序
                      const groupDiffOf = (arr: typeof vis) => {
                        let s = 0;
                        for (const x of arr) {
                          if (x.action !== 'overwrite' || !x.diff || x.kind !== 'preset') continue;
                          const d = x.diff;
                          s += d.scalarDiffs.length + d.removedTargets.length + d.addedTargets.length
                            + Object.keys(d.removedCols).length + Object.keys(d.addedCols).length;
                        }
                        return s;
                      };
                      const groupRiskAvg = (arr: typeof vis) => {
                        if (arr.length === 0) return 0;
                        let s = 0;
                        for (const x of arr) s += computeItemRiskScore(x).score;
                        return s / arr.length;
                      };
                      const sortedGroups: [string, typeof vis][] = Array.from(groups.entries()).sort((a, b) => {
                        if (backupGroupSort === 'diff') {
                          const da = groupDiffOf(a[1]);
                          const db = groupDiffOf(b[1]);
                          if (db !== da) return db - da;
                          return a[0].localeCompare(b[0]);
                        }
                        if (backupGroupSort === 'risk') {
                          const ra = groupRiskAvg(a[1]);
                          const rb = groupRiskAvg(b[1]);
                          if (rb !== ra) return rb - ra;
                          return a[0].localeCompare(b[0]);
                        }
                        return a[0].localeCompare(b[0]);
                      });
                      const allCollapsed = backupCollapsedGroups.size === groups.size && groups.size > 0;
                      return (
                        <div style={{ marginBottom: 4, fontSize: 10, color: 'var(--muted)', display: 'flex', gap: 4, flexWrap: 'wrap', alignItems: 'center' }}>
                          <span title="Alt+A 一键展开/收起全部">🗂 按表分组（{groups.size} 组）：</span>
                          <button
                            type="button"
                            onClick={() => setBackupCollapsedGroups(allCollapsed ? new Set() : new Set(groups.keys()))}
                            style={{ ...chipBase }}
                            title="Alt+A 一键展开/收起全部"
                          >{allCollapsed ? '⊕ 全部展开' : '⊖ 全部收起'}</button>
                          <select
                            value={backupGroupSort}
                            onChange={(e) => setBackupGroupSort(e.target.value as 'diff' | 'name' | 'risk')}
                            style={{ ...chipBase, padding: '0 4px', fontFamily: 'inherit', cursor: 'pointer' }}
                            title="分组排序（Alt+O 循环切换 diff→name→risk）"
                          >
                            <option value="diff">⇄ 按变更数</option>
                            <option value="name">A→Z 按名称</option>
                            <option value="risk">🔥 按风险</option>
                          </select>
                          {sortedGroups.map(([gk, arr]) => {
                            const collapsed = backupCollapsedGroups.has(gk);
                            const parts = gk.split('::');
                            const selCount = arr.filter((x) => backupSelected.has(x.key)).length;
                            let groupDiff = 0;
                            const diffItems = arr.filter((x) => x.action === 'overwrite' && x.diff && x.kind === 'preset');
                            for (const x of diffItems) {
                              const d = x.diff!;
                              groupDiff += d.scalarDiffs.length + d.removedTargets.length + d.addedTargets.length
                                + Object.keys(d.removedCols).length + Object.keys(d.addedCols).length;
                            }
                            // M30.160 A：组内高风险项计数
                            let groupHighRisk = 0;
                            let groupRiskSum = 0;
                            for (const x of arr) {
                              const r = computeItemRiskScore(x);
                              if (r.level === 'high') groupHighRisk += 1;
                              groupRiskSum += r.score;
                            }
                            // M30.165 D：组内平均风险分（score 0-100，与阈值色阶一致）
                            const groupAvgScore = arr.length > 0 ? Math.round(groupRiskSum / arr.length) : 0;
                            const groupRiskHeat = groupAvgScore >= 50
                              ? { bg: 'rgba(220,38,38,0.15)', border: 'rgba(220,38,38,0.5)', color: 'var(--danger, #dc2626)', label: '高风险' }
                              : groupAvgScore >= 20
                                ? { bg: 'rgba(217,119,6,0.15)', border: 'rgba(217,119,6,0.5)', color: 'var(--warn, #d97706)', label: '中风险' }
                                : { bg: 'rgba(59,130,246,0.15)', border: 'rgba(59,130,246,0.5)', color: 'var(--info, #3b82f6)', label: '低风险' };
                            const fmt = backupGroupExportFmt[gk] ?? 'json';
                            return (
                              <span
                                key={gk}
                                style={{
                                  ...chipBase, padding: '0 5px', fontSize: 10,
                                  cursor: 'pointer',
                                  border: '1px solid var(--border)',
                                  background: collapsed ? 'rgba(148,163,184,0.10)' : 'rgba(59,130,246,0.06)',
                                  display: 'inline-flex', alignItems: 'center', gap: 3,
                                }}
                                onClick={() => {
                                  const next = new Set(backupCollapsedGroups);
                                  if (next.has(gk)) next.delete(gk); else next.add(gk);
                                  setBackupCollapsedGroups(next);
                                }}
                                title={`${parts[0]}.${parts[1]}（${arr.length} 项 · ${selCount} 已选 · ${groupDiff} 变更） · 点击折叠/展开`}
                              >
                                {collapsed ? '▸' : '▾'} <code>{parts[0]}.{parts[1]}</code> · {arr.length}
                                {groupDiff > 0 && (() => {
                                  // M30.150 D 热力色：0 隐藏 / 1-3 蓝 / 4-10 橙 / 11+ 红
                                  const heat = groupDiff <= 3
                                    ? { bg: 'rgba(59,130,246,0.15)', border: 'rgba(59,130,246,0.5)', color: 'var(--info, #3b82f6)', label: '微调' }
                                    : groupDiff <= 10
                                      ? { bg: 'rgba(217,119,6,0.15)', border: 'rgba(217,119,6,0.5)', color: 'var(--warn, #d97706)', label: '中等' }
                                      : { bg: 'rgba(220,38,38,0.15)', border: 'rgba(220,38,38,0.5)', color: 'var(--danger, #dc2626)', label: '大改' };
                                  return (
                                    <span
                                      style={{
                                        padding: '0 4px', fontSize: 10, borderRadius: 3,
                                        background: heat.bg, color: heat.color,
                                        border: `1px solid ${heat.border}`,
                                        fontFamily: 'monospace', lineHeight: 1.2,
                                        fontWeight: 700,
                                      }}
                                      title={`该组共 ${groupDiff} 处变更（${heat.label}）`}
                                    >⇄{groupDiff}</span>
                                  );
                                })()}
                                {groupHighRisk > 0 && (
                                  <button
                                    type="button"
                                    onClick={(e) => { e.preventDefault(); e.stopPropagation(); setBackupRiskLevelFilter('high'); }}
                                    style={{
                                      padding: '0 4px', fontSize: 10, borderRadius: 3,
                                      background: 'rgba(220,38,38,0.15)',
                                      color: 'var(--danger, #dc2626)',
                                      border: '1px solid rgba(220,38,38,0.5)',
                                      cursor: 'pointer', fontFamily: 'monospace',
                                      lineHeight: 1.2, fontWeight: 700,
                                    }}
                                    title={`该组有 ${groupHighRisk} 项高风险 · 点击切 riskLevelFilter='high'`}
                                  >🔴{groupHighRisk}</button>
                                )}
                                {arr.length > 0 && (
                                  <span
                                    style={{
                                      padding: '0 4px', fontSize: 10, borderRadius: 3,
                                      background: groupRiskHeat.bg,
                                      color: groupRiskHeat.color,
                                      border: `1px solid ${groupRiskHeat.border}`,
                                      fontFamily: 'monospace', lineHeight: 1.2, fontWeight: 700,
                                    }}
                                    title={`该组 ${arr.length} 项平均风险分 ${groupAvgScore}/100（${groupRiskHeat.label}）· M30.165 D`}
                                  >⏱{groupAvgScore}</span>
                                )}
                                {diffItems.length > 0 && (() => {
                                  const fmtLabel = fmt.toUpperCase();
                                  const nextFmt = fmt === 'json' ? 'csv' : fmt === 'csv' ? 'md' : 'json';
                                  return (
                                    <button
                                      type="button"
                                      onClick={(e) => {
                                        e.preventDefault();
                                        e.stopPropagation();
                                        setBackupGroupExportFmt((p) => ({ ...p, [gk]: nextFmt }));
                                        // 立即导出当前 fmt
                                        const now = new Date();
                                        const ts = `${now.getFullYear()}-${String(now.getMonth()+1).padStart(2,'0')}-${String(now.getDate()).padStart(2,'0')}_${String(now.getHours()).padStart(2,'0')}${String(now.getMinutes()).padStart(2,'0')}${String(now.getSeconds()).padStart(2,'0')}`;
                                        const safe = `${parts[0]}_${parts[1]}`.replace(/[^\w-]+/g, '_');
                                        const download = (content: string, mime: string, name: string) => {
                                          const blob = new Blob([content], { type: mime });
                                          const url = URL.createObjectURL(blob);
                                          const a = document.createElement('a');
                                          a.href = url; a.download = name;
                                          document.body.appendChild(a); a.click(); document.body.removeChild(a);
                                          setTimeout(() => URL.revokeObjectURL(url), 1000);
                                          setPresetManagerMsg({ kind: 'ok', text: `📥 已导出组 ${parts[0]}.${parts[1]} diff ${fmtLabel}（${diffItems.length} 项）` });
                                        };
                                        if (fmt === 'json') {
                                          const items = diffItems.map((x) => ({ key: x.key, schema: x.schema, table: x.table, diff: x.diff }));
                                          download(JSON.stringify({ exportedAt: now.toISOString(), connId, group: `${parts[0]}.${parts[1]}`, items }, null, 2), 'application/json', `polydb-group-${safe}-${ts}.json`);
                                          return;
                                        }
                                        if (fmt === 'csv') {
                                          const rows: string[] = ['key,schema,table,diffCount,scalarDiffs,removedTargets,addedTargets,removedCols,addedCols'];
                                          const esc = (v: string) => /[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v;
                                          for (const x of diffItems) {
                                            const d = x.diff!;
                                            const bits: string[] = [];
                                            for (const s of d.scalarDiffs) bits.push(s);
                                            for (const t of d.removedTargets) bits.push(`-目标 ${t}`);
                                            for (const t of d.addedTargets) bits.push(`+目标 ${t}`);
                                            for (const [f, arr2] of Object.entries(d.removedCols)) for (const s of arr2) bits.push(`-${f} ${s}`);
                                            for (const [f, arr2] of Object.entries(d.addedCols)) for (const s of arr2) bits.push(`+${f} ${s}`);
                                            rows.push([x.key, x.schema, x.table, String(d.total), esc(bits.join(' | '))].map((v) => esc(v)).join(','));
                                          }
                                          download(rows.join('\n') + '\n', 'text/csv', `polydb-group-${safe}-${ts}.csv`);
                                          return;
                                        }
                                        // md
                                        const md: string[] = [`# polydb 分组 diff：${parts[0]}.${parts[1]}`, ''];
                                        md.push(`- **时间**：${now.toISOString()}`);
                                        md.push(`- **连接**：\`${connId}\``);
                                        md.push(`- **变更项数**：${diffItems.length}`);
                                        md.push('');
                                        for (const x of diffItems) {
                                          const d = x.diff!;
                                          md.push(`## ${x.key}`);
                                          if (d.scalarDiffs.length) { for (const s of d.scalarDiffs) md.push(`- 修改：${s}`); }
                                          for (const t of d.removedTargets) md.push(`- 移除目标列：${t}`);
                                          for (const t of d.addedTargets) md.push(`- 新增目标列：${t}`);
                                          for (const [f, arr2] of Object.entries(d.removedCols)) for (const s of arr2) md.push(`- 移除：${f} ${s}`);
                                          for (const [f, arr2] of Object.entries(d.addedCols)) for (const s of arr2) md.push(`- 新增：${f} ${s}`);
                                          md.push('');
                                        }
                                        download(md.join('\n') + '\n', 'text/markdown', `polydb-group-${safe}-${ts}.md`);
                                      }}
                                      title={diffItems.length === 0 ? '该组无覆盖预设' : `导出该组 diff ${fmtLabel}（点击切到 ${nextFmt.toUpperCase()}）`}
                                      style={{
                                        padding: '0 4px', fontSize: 9, borderRadius: 2,
                                        background: 'rgba(16,185,129,0.10)', color: 'var(--success, #10b981)',
                                        border: '1px solid rgba(16,185,129,0.3)', cursor: 'pointer',
                                        lineHeight: 1, fontWeight: 700,
                                        fontFamily: 'monospace',
                                      }}
                                    >📥{fmtLabel}</button>
                                  );
                                })()}
                              </span>
                            );
                          })}
                        </div>
                      );
                    })()}
                    {/* M30.153 D：分组热力堆叠条 — 复用 backupPreviewGroupOrderMap 保持与 chip 同序 */}
                    {backupGroupBy && backupPending && (() => {
                      const gkList = Object.keys(backupPreviewGroupOrderMap);
                      if (gkList.length === 0) return null;
                      const groupStat = new Map<string, number>();
                      for (const it of backupPending.items) {
                        const gk = `${it.schema}::${it.table}`;
                        if (!groupStat.has(gk)) groupStat.set(gk, 0);
                        if (it.action === 'overwrite' && it.diff && it.kind === 'preset') {
                          const d = it.diff;
                          groupStat.set(gk, (groupStat.get(gk) ?? 0)
                            + d.scalarDiffs.length + d.removedTargets.length + d.addedTargets.length
                            + Object.keys(d.removedCols).length + Object.keys(d.addedCols).length);
                        }
                      }
                      const total = gkList.reduce((s, g) => s + (groupStat.get(g) ?? 0), 0);
                      if (total === 0) return null;
                      const heat = (n: number) => n <= 3 ? 'var(--info, #3b82f6)' : n <= 10 ? 'var(--warn, #d97706)' : 'var(--danger, #dc2626)';
                      const label = (n: number) => n === 0 ? '无变更' : n <= 3 ? '微调' : n <= 10 ? '中等' : '大改';
                      return (
                        <div
                          style={{ marginBottom: 4, height: 6, borderRadius: 3, overflow: 'hidden', display: 'flex', background: 'rgba(0,0,0,0.05)' }}
                          title={`按表分组变更热力（总 ${total} 处变更，各段宽度按组内变更占比；颜色 3/10 分档）`}
                        >
                          {gkList.map((gk) => {
                            const n = groupStat.get(gk) ?? 0;
                            const pct = total === 0 ? 0 : (n / total) * 100;
                            const parts = gk.split('::');
                            return (
                              <div
                                key={gk}
                                style={{ width: `${Math.max(pct, 1)}%`, background: heat(n), opacity: n === 0 ? 0.25 : 0.9 }}
                                title={`${parts[0]}.${parts[1]} · ${n} 变更（${label(n)}）`}
                              />
                            );
                          })}
                        </div>
                      );
                    })()}
                    <div style={{ maxHeight: 180, overflow: 'auto', border: '1px solid var(--border)', borderRadius: 3, background: 'var(--bg, #fff)' }}>
                      {backupPreviewFiltered.map((i, rowIdx) => {
                        const checked = backupSelected.has(i.key);
                        const isOverwrite = i.action === 'overwrite';
                        const collapsed = backupDiffCollapsed.has(i.key);
                        const showDiff = isOverwrite && i.kind === 'preset' && (checked || backupPreviewAllDiff) && !!i.diff && !collapsed;
                        const focused = backupFocusIdx === rowIdx;
                        return (
                          <div
                            key={`${i.kind}-${i.key}`}
                            data-backup-key={`${i.kind}::${i.key}`}
                            style={{
                              borderBottom: '1px solid var(--border)',
                              background: isOverwrite && checked ? 'rgba(217,119,6,0.08)' : 'transparent',
                              boxShadow: focused ? 'inset 3px 0 0 var(--accent, #3b82f6)' : undefined,
                            }}
                          >
                            <label
                              style={{
                                display: 'flex', alignItems: 'center', gap: 6,
                                padding: '4px 8px', fontSize: 11, cursor: 'pointer',
                              }}
                            >
                              {(() => {
                                if (!(isOverwrite && i.kind === 'preset' && i.diff)) return null;
                                return (
                                  <button
                                    type="button"
                                    onClick={(e) => {
                                      e.preventDefault();
                                      e.stopPropagation();
                                      setBackupDiffCollapsed((prev) => {
                                        const next = new Set(prev);
                                        if (next.has(i.key)) next.delete(i.key); else next.add(i.key);
                                        return next;
                                      });
                                    }}
                                    title={collapsed ? '展开 diff 明细（Enter）' : '收起 diff 明细（Enter）'}
                                    style={{ background: 'transparent', border: 'none', color: 'var(--muted)', cursor: 'pointer', padding: 0, fontSize: 10, width: 12, flexShrink: 0 }}
                                  >{collapsed ? '⊕' : '⊖'}</button>
                                );
                              })()}
                              <input
                                type="checkbox"
                                checked={checked}
                                onChange={() => {
                                  const next = new Set(backupSelected);
                                  if (checked) next.delete(i.key); else next.add(i.key);
                                  setBackupSelected(next);
                                }}
                              />
                              <span style={{ fontSize: 10, width: 46, color: i.kind === 'preset' ? 'var(--accent, #3b82f6)' : 'var(--muted)' }}>
                                {i.kind === 'preset' ? '预设' : '快照'}
                              </span>
                              <code style={{ color: 'var(--fg)', flexShrink: 0 }}>{highlightMatch(`${i.schema}.${i.table}`, backupPreviewSearch)}</code>
                              {(() => {
                                // M30.146 C：备份 key 首段是原连接 ID，与当前 connId 不同则打 🔗 徽标
                                const parts = i.key.split('::');
                                const srcConn = parts[0];
                                if (!srcConn || srcConn === connId) return null;
                                return (
                                  <span
                                    style={{
                                      fontSize: 10, padding: '0 4px', borderRadius: 3,
                                      background: 'rgba(139,92,246,0.12)', color: 'var(--info, #8b5cf6)',
                                      fontWeight: 600,
                                      border: '1px solid rgba(139,92,246,0.3)',
                                    }}
                                    title={`备份来自其他连接 ${srcConn.slice(0, 8)}…，与当前连接不同源：无法审计表/列存活，应用将创建新预设（可能与当前预设 ID 不同）`}
                                  >🔗 其他连接</span>
                                );
                              })()}
                              <span style={{
                                fontSize: 10, padding: '0 4px', borderRadius: 3,
                                background: isOverwrite ? 'rgba(217,119,6,0.15)' : 'rgba(16,185,129,0.12)',
                                color: isOverwrite ? 'var(--warn, #d97706)' : 'var(--success, #10b981)',
                                fontWeight: 600,
                              }}>
                                {isOverwrite ? `=覆盖` : '+新增'}
                              </span>
                              {(() => {
                                const r = computeItemRiskScore(i);
                                const copyRisk = (ev: React.MouseEvent) => {
                                  // 阻止 label 的默认 checkbox 切换（chip 是 label 的子元素）
                                  ev.preventDefault();
                                  ev.stopPropagation();
                                  // 焦点守卫：若在输入框内触发则跳过，防误复制打断用户
                                  const ae = document.activeElement as HTMLElement | null;
                                  if (ae && (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA' || ae.tagName === 'SELECT' || ae.isContentEditable)) return;
                                  void (async () => {
                                    try {
                                      await navigator.clipboard.writeText(`综合风险 ${r.score}/100（${r.label}）· 构成：${r.parts.join(' + ') || '无信号'} · key=${i.key}`);
                                      setPresetManagerMsg({ kind: 'ok', text: `📋 已复制风险明细：${r.label} ${r.score}/100 · ${r.parts.join(' + ') || '无信号'}` });
                                    } catch {
                                      setPresetManagerMsg({ kind: 'err', text: '❌ 复制失败（浏览器剪贴板权限）' });
                                    }
                                  })();
                                };
                                return (
                                  <span
                                    style={{
                                      fontSize: 9, padding: '0 4px', borderRadius: 3,
                                      background: r.bg, color: r.color,
                                      fontWeight: 700, letterSpacing: '0.02em',
                                      border: '1px solid', borderColor: 'transparent',
                                      cursor: 'pointer',
                                    }}
                                    title={`综合风险 ${r.score}/100（${r.label}）· 构成：${r.parts.join(' + ') || '无信号'}\n点击复制风险明细到剪贴板`}
                                    onClick={copyRisk}
                                  >{r.icon} {r.score}</span>
                                );
                              })()}
                              {i.kind === 'preset' && i.diff && !i.diff.aligned && (
                                <span
                                  style={{
                                    fontSize: 10, padding: '0 4px', borderRadius: 3,
                                    background: 'rgba(59,130,246,0.10)',
                                    color: 'var(--accent, #3b82f6)',
                                    fontFamily: 'monospace',
                                    fontWeight: 600,
                                  }}
                                  title={`字段级差异 ${i.diff.total} 项${collapsed ? '（点击 ⊕ 或 Enter 展开明细）' : ''}`}
                                >⇄ {i.diff.total} 处</span>
                              )}
                              {i.kind === 'snapshot' && i.snapshotCount > 0 && (
                                <span style={{ color: 'var(--muted)', fontSize: 10 }}>· {i.snapshotCount} 条</span>
                              )}
                              {i.kind === 'preset' && backupItemAudit && backupItemAudit[i.key] === 'ok' && (
                                <span
                                  style={{
                                    fontSize: 10, padding: '0 4px', borderRadius: 3,
                                    background: 'rgba(16,185,129,0.12)', color: 'var(--success, #10b981)',
                                    fontWeight: 600,
                                  }}
                                  title="目标表/列仍存活（M30.145 自动审计）"
                                >✅ 存活</span>
                              )}
                              {i.kind === 'preset' && backupItemAudit && backupItemAudit[i.key] === 'dead' && (
                                <span
                                  style={{
                                    fontSize: 10, padding: '0 4px', borderRadius: 3,
                                    background: 'rgba(220,38,38,0.14)', color: 'var(--danger, #dc2626)',
                                    fontWeight: 700,
                                    border: '1px solid rgba(220,38,38,0.4)',
                                  }}
                                  title="目标表不存在或不可访问（M30.145 自动审计，已自动取消勾选）"
                                >❌ 失效</span>
                              )}
                              {i.kind === 'preset' && backupItemAuditBusy && (!backupItemAudit || !backupItemAudit[i.key]) && (
                                <span style={{ color: 'var(--muted)', fontSize: 10 }} title="正在检查目标表/列是否存活">⏳ 审计中</span>
                              )}
                              {isOverwrite && checked && (
                                <span style={{ color: 'var(--warn, #d97706)', fontSize: 10, marginLeft: 'auto' }} title="勾选后此项会覆盖当前同名预设/快照">
                                  ⚠ 将覆盖
                                </span>
                              )}
                            </label>
                            {showDiff && (() => {
                              const d = i.diff!;
                              if (d.aligned) {
                                return (
                                  <div style={
                                    {
                                      padding: '2px 8px 4px 24px', fontSize: 10,
                                      color: 'var(--success, #10b981)',
                                    }
                                  } title="备份与当前预设字段完全一致；勾选后仅刷新保存时间戳，不产生快照">
                                    ✓ 字段一致（仅刷新时间戳）
                                  </div>
                                );
                              }
                              const MAX = 6;
                              const fieldBits: { text: string; kind: 'add' | 'remove' | 'change' }[] = [];
                              for (const s of d.scalarDiffs) fieldBits.push({ text: s, kind: 'change' });
                              // diffRecord(current, backup): removed = in current not in backup（备份将移除，UI 显示 -红删除线）
                              // added = in backup not in current（备份将加入，UI 显示 +绿）
                              for (const [f, arr] of Object.entries(d.addedCols)) {
                                for (let idx = 0; idx < arr.length; idx++) {
                                  fieldBits.push({ text: `+${f} ${arr[idx]}`, kind: 'add' });
                                }
                              }
                              for (const [f, arr] of Object.entries(d.removedCols)) {
                                for (let idx = 0; idx < arr.length; idx++) {
                                  fieldBits.push({ text: `−${f} ${arr[idx]}`, kind: 'remove' });
                                }
                              }
                              for (const c of d.removedTargets) fieldBits.push({ text: `−列 ${c}`, kind: 'remove' });
                              for (const c of d.addedTargets) fieldBits.push({ text: `+列 ${c}`, kind: 'add' });
                              const shown = fieldBits.slice(0, MAX);
                              const rest = fieldBits.length - shown.length;
                              const colorFor = (k: 'add' | 'remove' | 'change') =>
                                k === 'remove' ? 'var(--danger, #dc2626)'
                                : k === 'add' ? 'var(--success, #10b981)'
                                : 'var(--accent, #3b82f6)';
                              const bgFor = (k: 'add' | 'remove' | 'change') =>
                                k === 'remove' ? 'rgba(220,38,38,0.10)'
                                : k === 'add' ? 'rgba(16,185,129,0.10)'
                                : 'rgba(59,130,246,0.10)';
                              const chipStyleFor = (k: 'add' | 'remove' | 'change'): CSSProperties => ({
                                padding: '0 4px', borderRadius: 3, fontSize: 10,
                                background: bgFor(k),
                                color: colorFor(k),
                                fontFamily: 'monospace',
                                textDecoration: k === 'remove' ? 'line-through' : 'none',
                              });
                              return (
                                <div style={{
                                  padding: '2px 8px 4px 24px', fontSize: 10,
                                  display: 'flex', gap: 4, alignItems: 'center', flexWrap: 'wrap',
                                }} title={`字段级差异 ${d.total} 项：${fieldBits.map((b) => b.text).join(' · ')}`}>
                                  <span style={{
                                    padding: '0 4px', borderRadius: 3, fontSize: 10,
                                    background: 'rgba(217,119,6,0.14)',
                                    border: '1px solid rgba(217,119,6,0.5)',
                                    color: 'var(--warn, #d97706)', fontWeight: 600,
                                  }}>⚙ {d.total} 项差异</span>
                                  {shown.map((b, idx) => {
                                    const isCopied = copiedChipText === b.text;
                                    // M30.145 D change 项 side-by-side：拆 `字段 旧值→新值` 三列
                                    if (b.kind === 'change' && b.text.includes('→')) {
                                      const firstSpace = b.text.indexOf(' ');
                                      const label = firstSpace >= 0 ? b.text.slice(0, firstSpace) : '';
                                      const value = firstSpace >= 0 ? b.text.slice(firstSpace + 1) : b.text;
                                      const arrowIdx = value.indexOf('→');
                                      const oldV = arrowIdx >= 0 ? value.slice(0, arrowIdx) : value;
                                      const newV = arrowIdx >= 0 ? value.slice(arrowIdx + 1) : '';
                                      return (
                                        <button
                                          key={idx}
                                          type="button"
                                          onClick={() => {
                                            const text = b.text;
                                            void navigator.clipboard.writeText(text).then(() => {
                                              setCopiedChipText(text);
                                              setTimeout(() => {
                                                setCopiedChipText((cur) => (cur === text ? null : cur));
                                              }, 1200);
                                            }).catch(() => { /* clipboard 拒绝忽略 */ });
                                          }}
                                          style={{
                                            display: 'inline-flex', alignItems: 'center', gap: 3,
                                            padding: '1px 5px', borderRadius: 3, fontSize: 10,
                                            background: 'rgba(59,130,246,0.08)',
                                            border: '1px solid rgba(59,130,246,0.25)',
                                            cursor: 'pointer', fontFamily: 'monospace',
                                            opacity: isCopied ? 0.75 : 1,
                                          }}
                                          title={isCopied ? `✓ 已复制「${b.text}」` : `点击复制「${b.text}」`}
                                        >
                                          {label && <span style={{ color: 'var(--muted)', fontWeight: 600 }}>{label}</span>}
                                          <span style={{
                                            background: 'rgba(220,38,38,0.14)',
                                            color: 'var(--danger, #dc2626)',
                                            textDecoration: 'line-through',
                                            padding: '0 3px', borderRadius: 2,
                                          }}>{oldV}</span>
                                          <span style={{ color: 'var(--muted)' }}>→</span>
                                          <span style={{
                                            background: 'rgba(16,185,129,0.14)',
                                            color: 'var(--success, #10b981)',
                                            padding: '0 3px', borderRadius: 2,
                                          }}>{newV}</span>
                                        </button>
                                      );
                                    }
                                    return (
                                      <button
                                        key={idx}
                                        type="button"
                                        onClick={() => {
                                          const text = b.text;
                                          void navigator.clipboard.writeText(text).then(() => {
                                            setCopiedChipText(text);
                                            setTimeout(() => {
                                              setCopiedChipText((cur) => (cur === text ? null : cur));
                                            }, 1200);
                                          }).catch(() => { /* clipboard 拒绝忽略 */ });
                                        }}
                                        style={
                                          {
                                            ...chipStyleFor(b.kind),
                                            cursor: 'pointer',
                                            border: '1px solid transparent',
                                            padding: '0 5px',
                                            opacity: isCopied ? 0.75 : 1,
                                          } as CSSProperties
                                        }
                                        title={isCopied ? `✓ 已复制「${b.text}」` : `点击复制「${b.text}」`}
                                      >{isCopied ? '✓ ' : '📋 '}{b.text}</button>
                                    );
                                  })}
                                  {rest > 0 && (
                                    <span style={{ fontSize: 9, color: 'var(--muted)' }}>+{rest}</span>
                                  )}
                                </div>
                              );
                            })()}
                          </div>
                        );
                      })}
                    </div>
                    {/* M30.145 C 备份预检快捷键浮层 */}
                    {backupShortcutsOpen && (
                      <div
                        style={{
                          position: 'absolute', inset: 0,
                          background: 'rgba(0,0,0,0.45)',
                          display: 'flex', alignItems: 'center', justifyContent: 'center',
                          zIndex: 10,
                        }}
                        onClick={() => setBackupShortcutsOpen(false)}
                      >
                        <div
                          style={{
                            background: 'var(--bg-elevated, #fff)',
                            border: '1px solid var(--border)',
                            borderRadius: 8,
                            padding: '14px 18px',
                            maxWidth: 480,
                            width: '92%',
                            maxHeight: '88%',
                            overflow: 'auto',
                            boxShadow: '0 8px 32px rgba(0,0,0,0.25)',
                            color: 'var(--fg)',
                            fontSize: 12,
                            lineHeight: 1.5,
                          }}
                          onClick={(e) => e.stopPropagation()}
                        >
                          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
                            <div style={{ fontWeight: 700, fontSize: 13 }}>⌨ 备份预检快捷键</div>
                            <button
                              style={{ background: 'transparent', border: '1px solid var(--border)', borderRadius: 3, color: 'var(--muted)', cursor: 'pointer', padding: '2px 8px', fontSize: 11 }}
                              onClick={() => setBackupShortcutsOpen(false)}
                              title="关闭 (Esc / ?)"
                            >✕</button>
                          </div>
                          {(() => {
                            const row = (combo: string, desc: string, color?: string) => (
                              <div style={{ display: 'flex', gap: 8, padding: '2px 0', borderBottom: '1px dotted var(--border)', alignItems: 'center' }}>
                                <code style={{
                                  flexShrink: 0, minWidth: 110,
                                  fontFamily: 'var(--mono, monospace)',
                                  fontSize: 11, padding: '1px 6px',
                                  background: color ?? 'rgba(59,130,246,0.08)',
                                  border: `1px solid ${color ? 'transparent' : 'var(--border)'}`,
                                  borderRadius: 3, color: 'var(--fg)',
                                }}>{combo}</code>
                                <span style={{ fontSize: 11 }}>{desc}</span>
                              </div>
                            );
                            const section = (t: string) => (
                              <div style={{ fontWeight: 700, fontSize: 11, color: 'var(--muted)', margin: '8px 0 3px', letterSpacing: 0.4 }}>{t}</div>
                            );
                            return (
                              <>
                                {section('确认 / 取消')}
                                {row('Ctrl+Enter', '确认应用（等同点击「✅ 应用」）', 'rgba(16,185,129,0.14)')}
                                {row('Esc', '取消预检，返回管理器', 'rgba(220,38,38,0.12)')}
                                {section('焦点项（↑↓ 移动）')}
                                {row('↑ / ↓', '上/下一项（过滤后列表循环）')}
                                {row('Space', '勾选/取消勾选焦点项')}
                                {row('Enter', '展开/收起焦点项 diff（仅覆盖预设）')}
                                {section('筛选 / 排序')}
                                {row('Alt+Q', '动作=全部')}
                                {row('Alt+W', '动作=将覆盖')}
                                {row('Alt+E', '动作=新增')}
                                {row('Alt+1', '类型=全部')}
                                {row('Alt+2', '类型=预设')}
                                {row('Alt+3', '类型=快照')}
                                {row('Alt+/', '聚焦搜索框')}
                                {row('Alt+S', '选中当前筛选命中项')}
                                {row('Alt+Shift+S', '反选当前筛选命中项')}
                                {row('Ctrl+A', '全选全部项（避开当前筛选）', 'rgba(59,130,246,0.10)')}
                                {row('Shift+A', '反选全部项（避开当前筛选）', 'rgba(59,130,246,0.10)')}
                                {row('Alt+Shift+R', '按风险排序（失效>其他连接>变更>覆盖）')}
                                {row('Alt+A', '展开/收起全部分组（仅分组模式）')}
                                {row('Alt+O', '切换分组排序（变更数/名称/风险，M30.164 C）')}
                                {section('风险 / 白名单')}
                                {row('Alt+0', '重置风险阈值到 0（仅 >0 时生效）')}
                                {row('Alt+9', '切"只看高风险"（riskLevelFilter=high；再按切回全部，M30.165 B）', 'rgba(220,38,38,0.10)')}
                                {row('Ctrl+Shift+C', '复制当前预检 filter JSON 到剪贴板')}
                                {row('Ctrl+Shift+V', '粘贴 filter JSON 并应用')}
                                {section('视图 / Diff / 应用')}
                                {row('Alt+R', '重置视图（筛选/搜索/排序/展开态全清）')}
                                {row('Alt+V', '预览所有 diff（不依赖逐项勾选）')}
                                {row('Alt+D', '打开 apply 变更清单浮层（需有 lastBackupAppliedDetail）', 'rgba(139,92,246,0.14)')}
                                {row('?', '切换本面板')}
                              </>
                            );
                          })()}
                        </div>
                      </div>
                    )}
                    <div style={{ display: 'flex', gap: 6, marginTop: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                      <span style={{ fontSize: 10, color: 'var(--muted)' }}>
                        将执行：<span style={{ color: 'var(--success, #10b981)' }}>+{selectedAdds} 新增</span>
                        {selectedOverwrites > 0 && <> · <span style={{ color: 'var(--warn, #d97706)' }}>={selectedOverwrites} 覆盖</span></>}
                        {skipCount > 0 && <> · <span>⊘{skipCount} 跳过</span></>}
                      </span>
                      <div style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
                        <button
                          type="button"
                          onClick={confirmApplyBackup}
                          disabled={selectedCount === 0}
                          style={{
                            ...styles.btnSm, padding: '3px 10px', fontSize: 11,
                            color: selectedCount === 0 ? 'var(--muted)' : 'var(--success, #10b981)',
                            borderColor: selectedCount === 0 ? 'var(--border)' : 'var(--success, #10b981)',
                          }}
                          title="确认应用已勾选项到当前预设/快照 · Ctrl+Enter"
                        >✅ 应用 {selectedCount > 0 ? selectedCount : ''}</button>
                      </div>
                    </div>
                  </div>
                );
              })()}

              {/* M30.116 当前连接预设列表 + M30.117 健康审计 */}
              {(() => {
                void presetListRefresh; // 让列表随 bump 重读
                void presetAuditResults; // 让每行状态可响应
                const connPresets = listPresets(connId);
                const fmtRelative = (t: number) => {
                  const diff = Date.now() - t;
                  const day = 24 * 60 * 60 * 1000;
                  if (diff < 60 * 1000) return '刚刚';
                  if (diff < 60 * 60 * 1000) return `${Math.floor(diff / (60 * 1000))} 分钟前`;
                  if (diff < day) return `${Math.floor(diff / (60 * 60 * 1000))} 小时前`;
                  if (diff < day * 30) return `${Math.floor(diff / day)} 天前`;
                  return '已过期';
                };
                const modeIcon = (m: 'insert' | 'update' | 'upsert') =>
                  m === 'insert' ? '⬆' : m === 'update' ? '♻' : '⤒';
                const modeLabel = (m: 'insert' | 'update' | 'upsert') =>
                  m === 'insert' ? 'INSERT' : m === 'update' ? 'UPDATE' : 'UPSERT';
                const allKeys = connPresets.map((p) => p.key);
                const auditCoverage = presetAuditResults
                  ? allKeys.filter((k) => presetAuditResults[k]).length
                  : 0;
                const auditDone = presetAuditResults !== null && auditCoverage === allKeys.length && connPresets.length > 0;
                const auditSummary = (() => {
                  if (!auditDone) return null;
                  const counts = { ok: 0, warn: 0, dead: 0 };
                  for (const p of connPresets) {
                    const s = presetAuditResults?.[p.key]?.status;
                    if (s) counts[s]++;
                  }
                  return counts;
                })();
                const fmtCacheAge = (t: number) => {
                  const diff = Date.now() - t;
                  const day = 24 * 60 * 60 * 1000;
                  if (diff < 60 * 1000) return '刚刚';
                  if (diff < 60 * 60 * 1000) return `${Math.floor(diff / (60 * 1000))} 分钟前`;
                  if (diff < day) return `${Math.floor(diff / (60 * 60 * 1000))} 小时前`;
                  return `${Math.floor(diff / day)} 天前`;
                };
                const cacheAgeLabel = presetAuditCachedAt ? fmtCacheAge(presetAuditCachedAt) : null;
                const runAudit = async () => {
                  if (connPresets.length === 0 || presetAuditBusy) return;
                  setPresetAuditBusy(true);
                  const results: Record<string, { status: 'ok' | 'warn' | 'dead'; reason: string }> = {};
                  await Promise.all(connPresets.map(async (p) => {
                    try {
                      const cs = await api.listColumns(connId, p.schema, p.table);
                      const colNames = new Set(cs.map((c) => c.name));
                      const missing: string[] = [];
                      for (const m of p.mappings) {
                        if (m.targetColumn && !colNames.has(m.targetColumn)) missing.push(m.targetColumn);
                      }
                      if (missing.length === 0) {
                        results[p.key] = { status: 'ok', reason: `${cs.length} 列全部匹配 · ${p.mappings.length} 映射有效` };
                      } else {
                        const top = missing.slice(0, 3).join(', ') + (missing.length > 3 ? ` 等 ${missing.length} 列` : '');
                        const status = missing.length === p.mappings.filter((m) => m.targetColumn).length ? 'dead' : 'warn';
                        results[p.key] = { status, reason: `目标列已删除：${top}` };
                      }
                    } catch {
                      results[p.key] = { status: 'dead', reason: '目标表不存在或不可访问' };
                    }
                  }));
                  setPresetAuditResults(results);
                  saveAuditCache(connId, results);
                  setPresetAuditCachedAt(Date.now());
                  setPresetAuditBusy(false);
                };
                const cleanupDead = () => {
                  if (!presetAuditResults) return;
                  const dead = connPresets.filter((p) => presetAuditResults[p.key]?.status === 'dead');
                  if (dead.length === 0) return;
                  if (!confirm(`永久删除 ${dead.length} 个失效预设？（会同时清除其快照；可先在备份中留存）`)) return;
                  for (const p of dead) {
                    addDeletedPresetTombstone(p, listSnapshots(p.key));
                    deletePreset(connId, p.schema, p.table);
                  }
                  setPresetAuditResults(null);
                  setPresetListRefresh((n) => n + 1);
                  setPresetSnapshotsRefresh((n) => n + 1);
                  setPresetManagerMsg({ kind: 'ok', text: `🗑 已清理 ${dead.length} 个失效预设（可去回收站恢复）` });
                  publishEditorStatus({ message: `🗑 已清理 ${dead.length} 个失效预设`, messageAt: Date.now() });
                  setPresetManagerOpen((v) => v);
                };
                const clearCache = () => {
                  if (!confirm('清空当前连接的审计缓存？下次打开浮层会重新询问是否重扫（不会自动审计）。')) return;
                  clearAuditCache(connId);
                  setPresetAuditResults(null);
                  setPresetAuditCachedAt(null);
                  presetAuditCheckedRef.current = '';
                  setPresetManagerMsg({ kind: 'ok', text: '🗑 审计缓存已清空，可点「🔍 审计全部」重新扫描' });
                  publishEditorStatus({ message: '🗑 审计缓存已清空', messageAt: Date.now() });
                };
                return (
                  <div style={{ marginTop: 12 }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 4 }}>
                      <div style={{ fontWeight: 700, fontSize: 11, color: 'var(--muted)', letterSpacing: 0.4 }}>
                        📋 当前连接预设（{connPresets.length}）
                        <span style={{ color: 'var(--muted)', fontWeight: 400, marginLeft: 6 }}>
                          「🎯 去此表」跳转；「🗑」删除入回收站
                        </span>
                      </div>
                      <div style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
                        <button
                          onClick={() => void runAudit()}
                          disabled={connPresets.length === 0 || presetAuditBusy}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--warn, #d97706)', borderColor: 'var(--warn, #d97706)' }}
                          title="并发检查每个预设目标表/列是否存活（会调用 listColumns）"
                        >{presetAuditBusy ? '⏳ 审计中…' : auditDone ? '🔍 重审' : '🔍 审计全部'}</button>
                        <button
                          onClick={cleanupDead}
                          disabled={!auditDone || !auditSummary || auditSummary.dead === 0}
                          style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)' }}
                          title="把审计状态为 ❌ 失效的预设移入回收站（可恢复）"
                        >🧹 清理失效</button>
                      </div>
                    </div>
                    {auditSummary && (() => {
                      // 快捷跳转：点这些数字直接把下方列表切到对应过滤态（active 视觉由下方 chip 行展示，这里只做跳转入口）
                      const chipBtn = (filter: 'all' | 'warn' | 'ok', color: string, icon: string, count: number, text: string, title: string, show: boolean) => {
                        if (!show) return null;
                        return (
                          <button
                            type="button"
                            onClick={() => setPresetAuditFilter(filter)}
                            style={{
                              fontSize: 10, padding: '0 3px', borderRadius: 3, border: '1px solid transparent',
                              background: 'transparent', color,
                              cursor: 'pointer', fontFamily: 'inherit',
                            }}
                            title={`${title}（点击筛选下方列表）`}
                          >{icon} {count} {text}</button>
                        );
                      };
                      return (
                        <div style={{ fontSize: 10, marginBottom: 4, display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
                          <button
                            type="button"
                            onClick={() => setPresetAuditFilter('all')}
                            style={{
                              fontSize: 10, padding: '0 3px', borderRadius: 3, border: '1px solid transparent',
                              background: 'transparent', color: 'var(--muted)',
                              cursor: 'pointer', fontFamily: 'inherit',
                            }}
                            title="回到全部（含未审计）"
                          >审计结果：</button>
                          {chipBtn('ok', 'var(--success, #10b981)', '✅', auditSummary.ok, '正常', '只看 ✅ 正常', auditSummary.ok > 0)}
                          {chipBtn('warn', 'var(--warn, #d97706)', '⚠', auditSummary.warn, '部分列缺失', '只看 ⚠ 部分列缺失', auditSummary.warn > 0)}
                          {chipBtn('warn', 'var(--danger, #dc2626)', '❌', auditSummary.dead, '失效', '只看 ⚠ + ❌ 异常（含失效）', auditSummary.dead > 0)}
                          <span style={{ color: 'var(--muted)' }}>· {auditCoverage}/{connPresets.length}</span>
                        </div>
                      );
                    })()}
                    {cacheAgeLabel && (() => {
                      const hitCount = presetAuditResults
                        ? connPresets.filter((p) => presetAuditResults[p.key]).length
                        : 0;
                      const pct = connPresets.length > 0 ? Math.round((hitCount / connPresets.length) * 100) : 0;
                      const cacheStale = hitCount < connPresets.length;
                      return (
                        <div style={{
                          fontSize: 10, marginBottom: 4, display: 'flex', gap: 6,
                          alignItems: 'center', flexWrap: 'wrap',
                          padding: '2px 6px', borderRadius: 3,
                          background: 'rgba(139,92,246,0.06)',
                          border: '1px solid rgba(139,92,246,0.20)',
                        }}>
                          <span
                            style={{
                              color: 'var(--muted)', padding: '0 4px',
                              border: '1px solid var(--border)', borderRadius: 3,
                              background: 'rgba(139,92,246,0.10)',
                            }}
                            title="审计结果来自 24h 内 localStorage 缓存；点顶部「🔍 重审」可强制刷新"
                          >🕐 缓存 {cacheAgeLabel}</span>
                          <span style={{ color: cacheStale ? 'var(--warn, #d97706)' : 'var(--muted)' }}
                            title={cacheStale ? `缓存只覆盖当前预设的 ${hitCount}/${connPresets.length}（部分预设可能已新增，点「🔍 重审」补齐）` : '所有当前预设均命中缓存'}>
                            命中 {hitCount}/{connPresets.length}
                          </span>
                          {cacheStale && (
                            <span style={{ color: 'var(--warn, #d97706)', fontSize: 9 }} title="当前预设数量与缓存覆盖数不一致，建议重审">⚠ 覆盖 {pct}%</span>
                          )}
                          <button
                            type="button"
                            onClick={clearCache}
                            style={{
                              marginLeft: 'auto',
                              ...styles.btnSm, padding: '1px 6px', fontSize: 10,
                              color: 'var(--muted)',
                            }}
                            title="清空当前连接的审计缓存（下次打开浮层不自动加载）"
                          >🗑 清缓存</button>
                        </div>
                      );
                    })()}
                    {connPresets.length === 0 ? (
                      <div style={{ fontSize: 10, color: 'var(--muted)', fontStyle: 'italic' }}>
                        暂无预设。在 Step 4 完成导入后会自动保存。
                      </div>
                    ) : (
                      <>
                        {presetAuditResults && (() => {
                          const matchFilter = (status: 'ok' | 'warn' | 'dead') =>
                            presetAuditFilter === 'all'
                              || (presetAuditFilter === 'warn' && status !== 'ok')
                              || (presetAuditFilter === 'ok' && status === 'ok');
                          const total = connPresets.length;
                          const filteredCount = connPresets.filter((p) => {
                            const s = presetAuditResults[p.key]?.status;
                            if (!s) return presetAuditFilter === 'all';
                            return matchFilter(s);
                          }).length;
                          const chipBase: CSSProperties = {
                            padding: '1px 6px', fontSize: 10, borderRadius: 3,
                            border: '1px solid var(--border)', cursor: 'pointer',
                            background: 'transparent', color: 'var(--muted)',
                          };
                          const chipActive: CSSProperties = {
                            border: '1px solid var(--accent, #3b82f6)',
                            color: 'var(--accent, #3b82f6)',
                            background: 'rgba(59,130,246,0.10)',
                          };
                          return (
                            <div style={{ display: 'flex', gap: 4, alignItems: 'center', marginBottom: 4, fontSize: 10, flexWrap: 'wrap' }}>
                              <span style={{ color: 'var(--muted)' }}>筛选：</span>
                              <button
                                type="button"
                                onClick={() => setPresetAuditFilter('all')}
                                style={{ ...chipBase, ...(presetAuditFilter === 'all' ? chipActive : {}) }}
                                title="显示所有预设（含未审计的）"
                              >全部 {total}</button>
                              <button
                                type="button"
                                onClick={() => setPresetAuditFilter('warn')}
                                style={{ ...chipBase, ...(presetAuditFilter === 'warn' ? chipActive : {}), color: presetAuditFilter === 'warn' ? 'var(--warn, #d97706)' : undefined, borderColor: presetAuditFilter === 'warn' ? 'var(--warn, #d97706)' : undefined, background: presetAuditFilter === 'warn' ? 'rgba(217,119,6,0.10)' : undefined }}
                                title="只看审计为 ⚠ 部分列缺失 或 ❌ 失效 的预设"
                              >⚠️ 异常 {total - auditSummary!.ok - (connPresets.filter((p) => !presetAuditResults[p.key]).length)}</button>
                              <button
                                type="button"
                                onClick={() => setPresetAuditFilter('ok')}
                                style={{ ...chipBase, ...(presetAuditFilter === 'ok' ? chipActive : {}), color: presetAuditFilter === 'ok' ? 'var(--success, #10b981)' : undefined, borderColor: presetAuditFilter === 'ok' ? 'var(--success, #10b981)' : undefined, background: presetAuditFilter === 'ok' ? 'rgba(16,185,129,0.10)' : undefined }}
                                title="只看审计为 ✅ 正常的预设"
                              >✅ 正常 {auditSummary!.ok}</button>
                              <span style={{ color: 'var(--muted)', marginLeft: 'auto' }}>显示 {filteredCount}/{total}</span>
                            </div>
                          );
                        })()}
                        <div style={{ maxHeight: 240, overflow: 'auto', border: '1px solid var(--border)', borderRadius: 4 }}>
                        {connPresets.filter((p) => {
                          if (!presetAuditResults) return true;
                          const s = presetAuditResults[p.key]?.status;
                          if (!s) return presetAuditFilter === 'all';
                          if (presetAuditFilter === 'warn') return s !== 'ok';
                          if (presetAuditFilter === 'ok') return s === 'ok';
                          return true;
                        }).map((p) => {
                          const snapCount = listSnapshots(p.key).length;
                          const audit = presetAuditResults?.[p.key];
                          const auditColor = audit
                            ? audit.status === 'ok' ? 'var(--success, #10b981)'
                              : audit.status === 'warn' ? 'var(--warn, #d97706)' : 'var(--danger, #dc2626)'
                            : null;
                          const auditIcon = audit
                            ? audit.status === 'ok' ? '✅' : audit.status === 'warn' ? '⚠' : '❌'
                            : null;
                          const jumpConfirming = pendingPresetJumpConfirm === p.key;
                          const jumpDiffExpanded = (jumpConfirming || snapDiffTs === p.updatedAt);
                          // M30.133 当前表高亮：与 Step 2 selSchema/selTable 一致时加左 accent 边 + 微背景 + title 提示
                          const isCurrentTable = selSchema === p.schema && selTable === p.table;
                          return (
                            <div
                              key={p.key}
                              style={{
                                padding: '5px 8px', fontSize: 11,
                                display: 'flex', flexDirection: 'column', gap: 2,
                                borderBottom: '1px solid var(--border)',
                                background: auditColor === 'var(--danger, #dc2626)' ? 'rgba(220,38,38,0.05)'
                                  : isCurrentTable ? 'rgba(59,130,246,0.05)' : 'transparent',
                                boxShadow: isCurrentTable ? 'inset 3px 0 0 var(--accent, #3b82f6)' : undefined,
                              }}
                              title={isCurrentTable
                                ? `📌 此预设目标表与 Step 2 当前选择一致${audit ? ` · ${audit.status} · ${audit.reason}` : ''}（点「🎯 去此表」会先弹 diff 确认）`
                                : (audit ? `${audit.status} · ${audit.reason}` : undefined)}
                            >
                            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                              {auditIcon && (
                                <span style={{ fontSize: 11, width: 14, textAlign: 'center', flexShrink: 0 }} title={audit!.reason}>{auditIcon}</span>
                              )}
                              <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                <code style={{ color: 'var(--fg)' }}>{p.schema}.{p.table}</code>
                                <span style={{ color: auditColor ?? 'var(--muted)', marginLeft: 6 }} title={modeLabel(p.mode)}>
                                  {modeIcon(p.mode)} {modeLabel(p.mode)}
                                </span>
                                <span style={{ color: 'var(--muted)', marginLeft: 4 }}>· {snapCount} 快照 · {fmtRelative(p.updatedAt)}</span>
                                {isCurrentTable && (
                                  <span style={{
                                    padding: '0 4px', borderRadius: 3, fontSize: 9, fontWeight: 700,
                                    background: 'rgba(59,130,246,0.14)',
                                    border: '1px solid rgba(59,130,246,0.5)',
                                    color: 'var(--accent, #3b82f6)',
                                    marginLeft: 2,
                                  }} title="此预设的目标表与 Step 2 当前选择一致（点「🎯 去此表」会先弹 diff 确认）">
                                    📌 当前
                                  </span>
                                )}
                                {audit && (() => {
                                  const bg = audit.status === 'ok' ? 'rgba(16,185,129,0.10)'
                                    : audit.status === 'warn' ? 'rgba(217,119,6,0.12)' : 'rgba(220,38,38,0.12)';
                                  const bd = audit.status === 'ok' ? 'rgba(16,185,129,0.4)'
                                    : audit.status === 'warn' ? 'rgba(217,119,6,0.5)' : 'rgba(220,38,38,0.5)';
                                  const cl = audit.status === 'ok' ? 'var(--success, #10b981)'
                                    : audit.status === 'warn' ? 'var(--warn, #d97706)' : 'var(--danger, #dc2626)';
                                  return (
                                    <span style={{
                                      padding: '0 4px', borderRadius: 3, fontSize: 9, fontWeight: 600,
                                      background: bg, border: `1px solid ${bd}`, color: cl,
                                    }} title={audit.reason}>
                                      {audit.status === 'ok' ? '✅' : audit.status === 'warn' ? '⚠' : '❌'}
                                    </span>
                                  );
                                })()}
                              </span>
                              <button
                                onClick={() => {
                                  const sameTable = selSchema === p.schema && selTable === p.table;
                                  if (sameTable) {
                                    setPendingPresetJumpConfirm(p.key);
                                    return;
                                  }
                                  setPresetManagerOpen(false);
                                  setStep('table');
                                  setSelSchema(p.schema);
                                  setSelTable(p.table);
                                  publishEditorStatus({ message: `🎯 已跳到 ${p.schema}.${p.table}（预设 ${modeLabel(p.mode)} · ${snapCount} 快照）`, messageAt: Date.now() });
                                }}
                                style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)' }}
                                title="跳到 Step 2 目标表（同表时会弹 diff 二次确认，异表直接跳）"
                              >🎯 去此表</button>
                              <button
                                type="button"
                                onClick={() => {
                                  setSnapDiffTs(snapDiffTs === p.updatedAt ? null : p.updatedAt);
                                  if (jumpConfirming) setPendingPresetJumpConfirm(null);
                                }}
                                style={{
                                  ...styles.btnSm, padding: '1px 6px', fontSize: 10,
                                  color: jumpDiffExpanded ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                                  borderColor: jumpDiffExpanded ? 'var(--accent, #3b82f6)' : 'var(--border)',
                                }}
                                title="对比此预设与当前配置（复用 M30.120 算法）"
                              >{jumpDiffExpanded ? '▲ 对比' : '⇄ 对比'}</button>
                              <button
                                onClick={() => {
                                  const snaps = listSnapshots(p.key);
                                  addDeletedPresetTombstone(p, snaps);
                                  deletePreset(connId, p.schema, p.table);
                                  if (presetAuditResults && presetAuditResults[p.key]) {
                                    const next = { ...presetAuditResults };
                                    delete next[p.key];
                                    setPresetAuditResults(next);
                                  }
                                  setPresetListRefresh((n) => n + 1);
                                  setPresetSnapshotsRefresh((n) => n + 1);
                                  setPresetManagerMsg({ kind: 'ok', text: `🗑 已删除 ${p.schema}.${p.table}（含 ${snaps.length} 个快照）— 可在回收站恢复` });
                                  publishEditorStatus({ message: `🗑 已删除预设 ${p.schema}.${p.table}（可去回收站恢复）`, messageAt: Date.now() });
                                  setPresetManagerOpen((v) => v);
                                }}
                                style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)' }}
                                title="删除预设（含快照），移入回收站可恢复"
                              >🗑</button>
                            </div>
                            {jumpConfirming && (() => {
                              const currentPreset: ImportPreset = {
                                key: p.key, schema: p.schema, table: p.table, kind: kindProp,
                                mode: opts.mode, mappings: [...mappings],
                                transforms: { ...(opts.transforms ?? {}) },
                                transformParams: { ...(opts.transformParams ?? {}) },
                                filterColumn: opts.filterColumn ?? null,
                                filterOp: (opts.filterOp ?? 'contains') as ImportPreset['filterOp'],
                                filterValue: opts.filterValue ?? '',
                                validations: { ...(opts.validations ?? {}) },
                                strictValidation: opts.strictValidation ?? true,
                                emptyAsNull: opts.emptyAsNull, batchSize: opts.batchSize, skipFailed: opts.skipFailed,
                                nullPolicies: { ...(opts.nullPolicies ?? {}) },
                                createdAt: p.createdAt, updatedAt: Date.now(),
                              };
                              const d = diffPreset(currentPreset, p);
                              if (d.aligned) {
                                return (
                                  <div style={{ fontSize: 9, padding: '1px 0', color: 'var(--success, #10b981)' }}>
                                    ✓ 目标预设与当前配置字段完全一致（跳转无副作用）
                                  </div>
                                );
                              }
                              const fieldBits: { text: string; kind: 'add' | 'remove' | 'change' }[] = [];
                              for (const sc of d.scalarDiffs) fieldBits.push({ text: sc, kind: 'change' });
                              for (const [f, arr] of Object.entries(d.addedCols)) {
                                for (let k = 0; k < arr.length; k++) fieldBits.push({ text: `+${f} ${arr[k]}`, kind: 'add' });
                              }
                              for (const [f, arr] of Object.entries(d.removedCols)) {
                                for (let k = 0; k < arr.length; k++) fieldBits.push({ text: `−${f} ${arr[k]}`, kind: 'remove' });
                              }
                              for (const c of d.addedTargets) fieldBits.push({ text: `+列 ${c}`, kind: 'add' });
                              for (const c of d.removedTargets) fieldBits.push({ text: `−列 ${c}`, kind: 'remove' });
                              const MAX = 4;
                              const shownBits = fieldBits.slice(0, MAX);
                              const restCount = fieldBits.length - shownBits.length;
                              const chipStyle = (k: 'add' | 'remove' | 'change'): CSSProperties => ({
                                padding: '0 4px', borderRadius: 3, fontSize: 9,
                                background: k === 'remove' ? 'rgba(220,38,38,0.10)'
                                  : k === 'add' ? 'rgba(16,185,129,0.10)'
                                  : 'rgba(59,130,246,0.10)',
                                color: k === 'remove' ? 'var(--danger, #dc2626)'
                                  : k === 'add' ? 'var(--success, #10b981)'
                                  : 'var(--accent, #3b82f6)',
                                fontFamily: 'monospace',
                                textDecoration: k === 'remove' ? 'line-through' : 'none',
                              });
                              return (
                                <div style={{
                                  fontSize: 9, padding: '4px 6px', borderRadius: 3,
                                  background: 'rgba(217,119,6,0.08)',
                                  border: '1px solid rgba(217,119,6,0.4)',
                                  display: 'flex', flexDirection: 'column', gap: 3,
                                }}>
                                  {presetAuditResults?.[p.key] && (
                                    <AuditBadge a={presetAuditResults[p.key]!} cachedAtMs={presetAuditCachedAt} age={presetAuditCachedAt ? fmtCacheAgeForAudit(presetAuditCachedAt) : null} label="目标预设" onReAudit={() => void reAuditOne(p.schema, p.table)} busy={presetAuditBusy} />
                                  )}
                                  <div style={{ display: 'flex', gap: 3, flexWrap: 'wrap', alignItems: 'center' }} title={`当前配置 vs 目标预设 共 ${d.total} 项差异：${fieldBits.map((b) => b.text).join(' · ')}`}>
                                    <span style={{
                                      padding: '0 4px', borderRadius: 3, fontSize: 9,
                                      background: 'rgba(217,119,6,0.14)', border: '1px solid rgba(217,119,6,0.5)',
                                      color: 'var(--warn, #d97706)', fontWeight: 600,
                                    }}>⚠ 将覆盖当前配置</span>
                                    <span style={{
                                      padding: '0 4px', borderRadius: 3, fontSize: 9,
                                      background: 'rgba(217,119,6,0.14)', border: '1px solid rgba(217,119,6,0.5)',
                                      color: 'var(--warn, #d97706)', fontWeight: 600,
                                    }}>⇄ {d.total} 项差异</span>
                                    {shownBits.map((b, idx) => {
                                      const isCopied = copiedChipText === b.text;
                                      const chipStyleBase = chipStyle(b.kind);
                                      return (
                                        <button
                                          key={idx}
                                          type="button"
                                          onClick={() => {
                                            const text = b.text;
                                            void navigator.clipboard.writeText(text).then(() => {
                                              setCopiedChipText(text);
                                              setTimeout(() => {
                                                setCopiedChipText((cur) => (cur === text ? null : cur));
                                              }, 1200);
                                            }).catch(() => { /* clipboard 拒绝忽略 */ });
                                          }}
                                          style={{
                                            ...chipStyleBase,
                                            cursor: 'pointer',
                                            border: '1px solid transparent',
                                            padding: '0 5px',
                                            opacity: isCopied ? 0.75 : 1,
                                          }}
                                          title={isCopied ? `✓ 已复制「${b.text}」` : `点击复制「${b.text}」`}
                                        >{isCopied ? '✓ ' : '📋 '}{b.text}</button>
                                      );
                                    })}
                                    {restCount > 0 && (
                                      <span style={{ fontSize: 8, color: 'var(--muted)' }}>+{restCount}</span>
                                    )}
                                  </div>
                                  <div style={{ display: 'flex', gap: 6, alignItems: 'center', fontSize: 9 }}>
                                    <span style={{ color: 'var(--muted)', flex: 1, minWidth: 0 }}>
                                      跳到 {p.schema}.{p.table} 会将当前配置重置为此预设（当前配置将被覆盖，无自动撤销）
                                    </span>
                                    <button
                                      type="button"
                                      onClick={() => {
                                        setPendingPresetJumpConfirm(null);
                                        setPresetManagerOpen(false);
                                        setStep('table');
                                        setSelSchema(p.schema);
                                        setSelTable(p.table);
                                        publishEditorStatus({ message: `🎯 已跳到 ${p.schema}.${p.table}（预设 ${modeLabel(p.mode)} · ${snapCount} 快照）`, messageAt: Date.now() });
                                      }}
                                      style={{ ...styles.btnSm, padding: '1px 8px', fontSize: 9, color: 'var(--success, #10b981)', borderColor: 'var(--success, #10b981)' }}
                                      title="确认跳到该表并应用预设"
                                    >✅ 确认跳转</button>
                                    <button
                                      type="button"
                                      onClick={() => setPendingPresetJumpConfirm(null)}
                                      style={{ ...styles.btnSm, padding: '1px 8px', fontSize: 9, color: 'var(--muted)', borderColor: 'var(--border)' }}
                                      title="取消跳转"
                                    >✕ 取消</button>
                                  </div>
                                </div>
                              );
                            })()}
                            {jumpDiffExpanded && !jumpConfirming && (() => {
                              const currentPreset: ImportPreset = {
                                key: p.key, schema: p.schema, table: p.table, kind: kindProp,
                                mode: opts.mode, mappings: [...mappings],
                                transforms: { ...(opts.transforms ?? {}) },
                                transformParams: { ...(opts.transformParams ?? {}) },
                                filterColumn: opts.filterColumn ?? null,
                                filterOp: (opts.filterOp ?? 'contains') as ImportPreset['filterOp'],
                                filterValue: opts.filterValue ?? '',
                                validations: { ...(opts.validations ?? {}) },
                                strictValidation: opts.strictValidation ?? true,
                                emptyAsNull: opts.emptyAsNull, batchSize: opts.batchSize, skipFailed: opts.skipFailed,
                                nullPolicies: { ...(opts.nullPolicies ?? {}) },
                                createdAt: p.createdAt, updatedAt: Date.now(),
                              };
                              const d = diffPreset(currentPreset, p);
                              if (d.aligned) {
                                return (
                                  <div style={{ fontSize: 9, padding: '1px 0', color: 'var(--success, #10b981)' }}>
                                    ✓ 目标预设与当前配置字段完全一致（跳转无副作用）
                                  </div>
                                );
                              }
                              const fieldBits: { text: string; kind: 'add' | 'remove' | 'change' }[] = [];
                              for (const sc of d.scalarDiffs) fieldBits.push({ text: sc, kind: 'change' });
                              for (const [f, arr] of Object.entries(d.addedCols)) {
                                for (let k = 0; k < arr.length; k++) fieldBits.push({ text: `+${f} ${arr[k]}`, kind: 'add' });
                              }
                              for (const [f, arr] of Object.entries(d.removedCols)) {
                                for (let k = 0; k < arr.length; k++) fieldBits.push({ text: `−${f} ${arr[k]}`, kind: 'remove' });
                              }
                              for (const c of d.addedTargets) fieldBits.push({ text: `+列 ${c}`, kind: 'add' });
                              for (const c of d.removedTargets) fieldBits.push({ text: `−列 ${c}`, kind: 'remove' });
                              const MAX = 4;
                              const shownBits = fieldBits.slice(0, MAX);
                              const restCount = fieldBits.length - shownBits.length;
                              const chipStyle = (k: 'add' | 'remove' | 'change'): CSSProperties => ({
                                padding: '0 4px', borderRadius: 3, fontSize: 9,
                                background: k === 'remove' ? 'rgba(220,38,38,0.10)'
                                  : k === 'add' ? 'rgba(16,185,129,0.10)'
                                  : 'rgba(59,130,246,0.10)',
                                color: k === 'remove' ? 'var(--danger, #dc2626)'
                                  : k === 'add' ? 'var(--success, #10b981)'
                                  : 'var(--accent, #3b82f6)',
                                fontFamily: 'monospace',
                                textDecoration: k === 'remove' ? 'line-through' : 'none',
                              });
                              return (
                                <div style={{
                                  fontSize: 9, padding: '2px 0',
                                  display: 'flex', gap: 3, flexWrap: 'wrap', alignItems: 'center',
                                }} title={`当前配置 vs 目标预设 共 ${d.total} 项差异：${fieldBits.map((b) => b.text).join(' · ')}`}>
                                  <span style={{
                                    padding: '0 4px', borderRadius: 3, fontSize: 9,
                                    background: 'rgba(217,119,6,0.14)', border: '1px solid rgba(217,119,6,0.5)',
                                    color: 'var(--warn, #d97706)', fontWeight: 600,
                                  }}>⇄ {d.total} 项差异</span>
                                  {shownBits.map((b, idx) => {
                                    const isCopied = copiedChipText === b.text;
                                    return (
                                      <button
                                        key={idx}
                                        type="button"
                                        onClick={() => {
                                          const text = b.text;
                                          void navigator.clipboard.writeText(text).then(() => {
                                            setCopiedChipText(text);
                                            setTimeout(() => {
                                              setCopiedChipText((cur) => (cur === text ? null : cur));
                                            }, 1200);
                                          }).catch(() => { /* clipboard 拒绝忽略 */ });
                                        }}
                                        style={{
                                          ...chipStyle(b.kind),
                                          cursor: 'pointer',
                                          border: '1px solid transparent',
                                          padding: '0 5px',
                                          opacity: isCopied ? 0.75 : 1,
                                        }}
                                        title={isCopied ? `✓ 已复制「${b.text}」` : `点击复制「${b.text}」`}
                                      >{isCopied ? '✓ ' : '📋 '}{b.text}</button>
                                    );
                                  })}
                                  {restCount > 0 && (
                                    <span style={{ fontSize: 8, color: 'var(--muted)' }}>+{restCount}</span>
                                  )}
                                </div>
                              );
                            })()}
                            </div>
                          );
                        })}
                      </div>
                    </>
                    )}
                  </div>
                );
              })()}

              {/* M30.114 删除预设回收站（tombstone 桶，跨会话可恢复） */}
              {(() => {
                const archive = listDeletedPresets();
                if (archive.length === 0) {
                  return (
                    <div style={{ marginTop: 12 }}>
                      <div style={{ fontWeight: 700, fontSize: 11, color: 'var(--muted)', letterSpacing: 0.4, marginBottom: 4 }}>🗄 删除回收站</div>
                      <div style={{ fontSize: 10, color: 'var(--muted)', fontStyle: 'italic' }}>
                        暂无归档项。删除预设会存到这里（最多 20 条），可随时恢复或永久清除。
                      </div>
                    </div>
                  );
                }
                const fmtDate = (t: number) => {
                  const d = new Date(t);
                  const pad = (n: number) => String(n).padStart(2, '0');
                  return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
                };
                return (
                  <div style={{ marginTop: 12 }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 4 }}>
                      <div style={{ fontWeight: 700, fontSize: 11, color: 'var(--muted)', letterSpacing: 0.4 }}>
                        🗄 删除回收站（{archive.length} 条 · 最多 20）
                      </div>
                      <button
                        onClick={() => {
                          if (archive.length === 0) return;
                          if (confirm(`永久删除全部 ${archive.length} 条归档？不可恢复。`)) {
                            purgeAllDeletedPresets();
                            setPresetManagerMsg({ kind: 'ok', text: `🗑 已清空 ${archive.length} 条归档` });
                            setPresetManagerOpen((v) => v); // 触发重渲染
                          }
                        }}
                        style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)' }}
                        title="永久清除全部归档（不可恢复）"
                      >🗑 清空归档</button>
                    </div>
                    <div style={{ maxHeight: 200, overflow: 'auto', border: '1px solid var(--border)', borderRadius: 4 }}>
                      {archive.map((entry, i) => {
                        const existing = getPreset(entry.preset.key.split('::')[0] || '', entry.preset.schema, entry.preset.table);
                        const conflict = !!existing;
                        const tombstoneConfirming = pendingTombstoneConfirm?.key === entry.preset.key && pendingTombstoneConfirm.idx === i;
                        const tombDiffExpanded = conflict && (tombstoneConfirming || (snapDiffTs !== null && snapDiffTs === entry.deletedAt));
                        return (
                          <div
                            key={`${entry.preset.key}-${i}`}
                            style={{
                              padding: '5px 8px', fontSize: 11,
                              display: 'flex', flexDirection: 'column', gap: 2,
                              borderBottom: i < archive.length - 1 ? '1px solid var(--border)' : 'none',
                              background: conflict ? 'rgba(217,119,6,0.06)' : 'transparent',
                            }}
                          >
                            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                            <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                              title={`${entry.preset.schema}.${entry.preset.table}（${entry.snapshots.length} 快照 · ${fmtDate(entry.deletedAt)} 删除${conflict ? ' · 目标位置已被占用' : ''}`}>
                              <code style={{ color: 'var(--fg)' }}>{entry.preset.schema}.{entry.preset.table}</code>
                              <span style={{ color: 'var(--muted)', marginLeft: 4 }}>· {entry.snapshots.length} 快照 · {fmtDate(entry.deletedAt)}</span>
                              {conflict && <span style={{ color: 'var(--warn, #d97706)', marginLeft: 4 }}>⚠ 冲突</span>}
                            </span>
                            <button
                              onClick={() => {
                                if (!conflict) {
                                  const ok = restoreDeletedPresetTombstone(i);
                                  if (ok) {
                                    setPresetManagerMsg({ kind: 'ok', text: `↩ 已恢复 ${entry.preset.schema}.${entry.preset.table}（含 ${entry.snapshots.length} 个快照）` });
                                    publishEditorStatus({ message: `↩ 已从回收站恢复预设 ${entry.preset.schema}.${entry.preset.table}`, messageAt: Date.now() });
                                    setPresetSnapshotsRefresh((n) => n + 1);
                                  } else {
                                    setPresetManagerMsg({ kind: 'err', text: '❌ 恢复失败：归档项不存在' });
                                  }
                                  setPresetManagerOpen((v) => v);
                                  return;
                                }
                                setPendingTombstoneConfirm({ key: entry.preset.key, idx: i });
                              }}
                              style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--success, #10b981)', borderColor: 'var(--success, #10b981)' }}
                              title={conflict ? '恢复会覆盖当前同名预设（先弹 diff 二次确认）' : '恢复预设（含快照）'}
                            >↩ 恢复</button>
                            <button
                              onClick={() => {
                                if (conflict) setSnapDiffTs(snapDiffTs === entry.deletedAt ? null : entry.deletedAt);
                                if (tombstoneConfirming) setPendingTombstoneConfirm(null);
                              }}
                              disabled={!conflict}
                              style={
                                {
                                  ...styles.btnSm, padding: '1px 6px', fontSize: 10,
                                  color: conflict ? (tombDiffExpanded ? 'var(--accent, #3b82f6)' : 'var(--muted)') : 'var(--muted)',
                                  borderColor: tombDiffExpanded ? 'var(--accent, #3b82f6)' : 'var(--border)',
                                  opacity: conflict ? 1 : 0.4,
                                }
                              }
                              title={conflict ? '对比此归档预设与当前同名预设（复用 M30.120 算法）' : '无冲突，无需对比'}
                            >{tombDiffExpanded ? '▲ 对比' : '⇄ 对比'}</button>
                            <button
                              onClick={() => {
                                purgeDeletedPresetTombstone(i);
                                setPresetManagerMsg({ kind: 'ok', text: `🗑 已清除归档：${entry.preset.schema}.${entry.preset.table}` });
                                setPresetManagerOpen((v) => v);
                              }}
                              style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--muted)' }}
                              title="永久删除本条归档（不可恢复）"
                            >✕</button>
                            </div>
                            {tombstoneConfirming && conflict && existing && (() => {
                              const d = diffPreset(existing, entry.preset);
                              if (d.aligned) {
                                return (
                                  <div style={{ fontSize: 9, padding: '1px 0 1px 0', color: 'var(--success, #10b981)' }}>
                                    ✓ 归档与当前同名预设字段完全一致（仅时间戳不同）
                                  </div>
                                );
                              }
                              const fieldBits: { text: string; kind: 'add' | 'remove' | 'change' }[] = [];
                              for (const sc of d.scalarDiffs) fieldBits.push({ text: sc, kind: 'change' });
                              for (const [f, arr] of Object.entries(d.addedCols)) {
                                for (let k = 0; k < arr.length; k++) fieldBits.push({ text: `+${f} ${arr[k]}`, kind: 'add' });
                              }
                              for (const [f, arr] of Object.entries(d.removedCols)) {
                                for (let k = 0; k < arr.length; k++) fieldBits.push({ text: `−${f} ${arr[k]}`, kind: 'remove' });
                              }
                              for (const c of d.addedTargets) fieldBits.push({ text: `+列 ${c}`, kind: 'add' });
                              for (const c of d.removedTargets) fieldBits.push({ text: `−列 ${c}`, kind: 'remove' });
                              const MAX = 4;
                              const shownBits = fieldBits.slice(0, MAX);
                              const restCount = fieldBits.length - shownBits.length;
                              const chipStyle = (k: 'add' | 'remove' | 'change'): CSSProperties => ({
                                padding: '0 4px', borderRadius: 3, fontSize: 9,
                                background: k === 'remove' ? 'rgba(220,38,38,0.10)'
                                  : k === 'add' ? 'rgba(16,185,129,0.10)'
                                  : 'rgba(59,130,246,0.10)',
                                color: k === 'remove' ? 'var(--danger, #dc2626)'
                                  : k === 'add' ? 'var(--success, #10b981)'
                                  : 'var(--accent, #3b82f6)',
                                fontFamily: 'monospace',
                                textDecoration: k === 'remove' ? 'line-through' : 'none',
                              });
                              return (
                                <div style={{
                                  fontSize: 9, padding: '4px 6px', borderRadius: 3,
                                  background: 'rgba(217,119,6,0.08)',
                                  border: '1px solid rgba(217,119,6,0.4)',
                                  display: 'flex', flexDirection: 'column', gap: 3,
                                }}>
                                  {presetAuditResults?.[entry.preset.key] && (
                                    <AuditBadge a={presetAuditResults[entry.preset.key]!} cachedAtMs={presetAuditCachedAt} age={presetAuditCachedAt ? fmtCacheAgeForAudit(presetAuditCachedAt) : null} label="归档预设" />
                                  )}
                                  <div style={{ display: 'flex', gap: 3, flexWrap: 'wrap', alignItems: 'center' }} title={`当前同名预设 vs 归档 共 ${d.total} 项差异：${fieldBits.map((b) => b.text).join(' · ')}`}>
                                    <span style={{
                                      padding: '0 4px', borderRadius: 3, fontSize: 9,
                                      background: 'rgba(217,119,6,0.14)', border: '1px solid rgba(217,119,6,0.5)',
                                      color: 'var(--warn, #d97706)', fontWeight: 600,
                                    }}>⚠ 将覆盖同名预设</span>
                                    <span style={{
                                      padding: '0 4px', borderRadius: 3, fontSize: 9,
                                      background: 'rgba(217,119,6,0.14)', border: '1px solid rgba(217,119,6,0.5)',
                                      color: 'var(--warn, #d97706)', fontWeight: 600,
                                    }}>⇄ {d.total} 项差异</span>
                                    {shownBits.map((b, idx) => {
                                      const isCopied = copiedChipText === b.text;
                                      return (
                                        <button
                                          key={idx}
                                          type="button"
                                          onClick={() => {
                                            const text = b.text;
                                            void navigator.clipboard.writeText(text).then(() => {
                                              setCopiedChipText(text);
                                              setTimeout(() => {
                                                setCopiedChipText((cur) => (cur === text ? null : cur));
                                              }, 1200);
                                            }).catch(() => { /* clipboard 拒绝忽略 */ });
                                          }}
                                          style={{
                                            ...chipStyle(b.kind),
                                            cursor: 'pointer',
                                            border: '1px solid transparent',
                                            padding: '0 5px',
                                            opacity: isCopied ? 0.75 : 1,
                                          }}
                                          title={isCopied ? `✓ 已复制「${b.text}」` : `点击复制「${b.text}」`}
                                        >{isCopied ? '✓ ' : '📋 '}{b.text}</button>
                                      );
                                    })}
                                    {restCount > 0 && (
                                      <span style={{ fontSize: 8, color: 'var(--muted)' }}>+{restCount}</span>
                                    )}
                                  </div>
                                  <div style={{ display: 'flex', gap: 6, alignItems: 'center', fontSize: 9 }}>
                                    <span style={{ color: 'var(--muted)', flex: 1, minWidth: 0 }}>
                                      确认用归档覆盖当前同名预设？（当前同名预设将被覆盖，无自动撤销）
                                    </span>
                                    <button
                                      type="button"
                                      onClick={() => {
                                        setPendingTombstoneConfirm(null);
                                        const ok = restoreDeletedPresetTombstone(i);
                                        if (ok) {
                                          setPresetManagerMsg({ kind: 'ok', text: `↩ 已恢复 ${entry.preset.schema}.${entry.preset.table}（含 ${entry.snapshots.length} 个快照）` });
                                          publishEditorStatus({ message: `↩ 已从回收站恢复预设 ${entry.preset.schema}.${entry.preset.table}`, messageAt: Date.now() });
                                          setPresetSnapshotsRefresh((n) => n + 1);
                                        } else {
                                          setPresetManagerMsg({ kind: 'err', text: '❌ 恢复失败：归档项不存在' });
                                        }
                                        setPresetManagerOpen((v) => v);
                                      }}
                                      style={{ ...styles.btnSm, padding: '1px 8px', fontSize: 9, color: 'var(--success, #10b981)', borderColor: 'var(--success, #10b981)' }}
                                      title="确认用归档覆盖当前同名预设"
                                    >✅ 确认恢复</button>
                                    <button
                                      type="button"
                                      onClick={() => setPendingTombstoneConfirm(null)}
                                      style={{ ...styles.btnSm, padding: '1px 8px', fontSize: 9, color: 'var(--muted)', borderColor: 'var(--border)' }}
                                      title="取消恢复"
                                    >✕ 取消</button>
                                  </div>
                                </div>
                              );
                            })()}
                            {tombDiffExpanded && conflict && existing && !tombstoneConfirming && (() => {
                              const d = diffPreset(existing, entry.preset);
                              if (d.aligned) {
                                return (
                                  <div style={{ fontSize: 9, padding: '1px 0', color: 'var(--success, #10b981)' }}>
                                    ✓ 归档与当前同名预设字段完全一致（仅时间戳不同）
                                  </div>
                                );
                              }
                              const fieldBits: { text: string; kind: 'add' | 'remove' | 'change' }[] = [];
                              for (const sc of d.scalarDiffs) fieldBits.push({ text: sc, kind: 'change' });
                              for (const [f, arr] of Object.entries(d.addedCols)) {
                                for (let k = 0; k < arr.length; k++) fieldBits.push({ text: `+${f} ${arr[k]}`, kind: 'add' });
                              }
                              for (const [f, arr] of Object.entries(d.removedCols)) {
                                for (let k = 0; k < arr.length; k++) fieldBits.push({ text: `−${f} ${arr[k]}`, kind: 'remove' });
                              }
                              for (const c of d.addedTargets) fieldBits.push({ text: `+列 ${c}`, kind: 'add' });
                              for (const c of d.removedTargets) fieldBits.push({ text: `−列 ${c}`, kind: 'remove' });
                              const MAX = 4;
                              const shownBits = fieldBits.slice(0, MAX);
                              const restCount = fieldBits.length - shownBits.length;
                              const chipStyle = (k: 'add' | 'remove' | 'change'): CSSProperties => ({
                                padding: '0 4px', borderRadius: 3, fontSize: 9,
                                background: k === 'remove' ? 'rgba(220,38,38,0.10)'
                                  : k === 'add' ? 'rgba(16,185,129,0.10)'
                                  : 'rgba(59,130,246,0.10)',
                                color: k === 'remove' ? 'var(--danger, #dc2626)'
                                  : k === 'add' ? 'var(--success, #10b981)'
                                  : 'var(--accent, #3b82f6)',
                                fontFamily: 'monospace',
                                textDecoration: k === 'remove' ? 'line-through' : 'none',
                              });
                              return (
                                <div style={{
                                  fontSize: 9, padding: '2px 0',
                                  display: 'flex', gap: 3, flexWrap: 'wrap', alignItems: 'center',
                                }} title={`当前同名预设 vs 归档 共 ${d.total} 项差异：${fieldBits.map((b) => b.text).join(' · ')}`}>
                                  <span style={{
                                    padding: '0 4px', borderRadius: 3, fontSize: 9,
                                    background: 'rgba(217,119,6,0.14)', border: '1px solid rgba(217,119,6,0.5)',
                                    color: 'var(--warn, #d97706)', fontWeight: 600,
                                  }}>⇄ {d.total} 项差异</span>
                                  {shownBits.map((b, idx) => {
                                    const isCopied = copiedChipText === b.text;
                                    return (
                                      <button
                                        key={idx}
                                        type="button"
                                        onClick={() => {
                                          const text = b.text;
                                          void navigator.clipboard.writeText(text).then(() => {
                                            setCopiedChipText(text);
                                            setTimeout(() => {
                                              setCopiedChipText((cur) => (cur === text ? null : cur));
                                            }, 1200);
                                          }).catch(() => { /* clipboard 拒绝忽略 */ });
                                        }}
                                        style={{
                                          ...chipStyle(b.kind),
                                          cursor: 'pointer',
                                          border: '1px solid transparent',
                                          padding: '0 5px',
                                          opacity: isCopied ? 0.75 : 1,
                                        }}
                                        title={isCopied ? `✓ 已复制「${b.text}」` : `点击复制「${b.text}」`}
                                      >{isCopied ? '✓ ' : '📋 '}{b.text}</button>
                                    );
                                  })}
                                  {restCount > 0 && (
                                    <span style={{ fontSize: 8, color: 'var(--muted)' }}>+{restCount}</span>
                                  )}
                                </div>
                              );
                            })()}
                          </div>
                        );
                      })}
                    </div>
                  </div>
                );
              })()}
            </div>
          </div>
        );
      })()}
      {dragActive && <div style={styles.dragHint}>📥 拖拽 CSV/TXT 文件到此处</div>}
      <div style={styles.panel}>
        <div style={styles.header}>
          <span style={styles.title}>📥 数据导入</span>
          <button
            onClick={() => {
              setShowHistory((v) => !v);
              if (!showHistory) setHistory(listHistory(connId));
            }}
            style={styles.btnGhost}
            title="查看本连接的最近导入历史（最多 100 条）"
          >📜 历史</button>
          <button
            onClick={() => {
              setPresetManagerOpen((v) => !v);
              setPresetManagerMsg(null);
            }}
            style={styles.btnGhost}
            title="导出全部预设（含快照）为 JSON 备份，或从备份恢复；跨浏览器/设备迁移"
          >📦 预设</button>
          <button onClick={finishAndClose} style={styles.closeBtn} title="关闭">×</button>
        </div>
        {stepBar}

        <div style={styles.body}>
          {showHistory && (
            <div style={styles.historyPanel}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
                <span style={{ fontWeight: 700, fontSize: 13 }}>📜 最近导入历史（{history.length} 条）</span>
                <div style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
                  <button onClick={() => exportHistory('json')} style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10 }} title="导出全部历史为 JSON" disabled={history.length === 0}>↓ JSON</button>
                  <button onClick={() => exportHistory('csv')} style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10 }} title="导出全部历史为 CSV" disabled={history.length === 0}>↓ CSV</button>
                  <button
                    onClick={() => setHistoryTableOnly((v) => !v)}
                    disabled={!selSchema || !selTable}
                    style={historyTableOnly
                      ? { ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)', background: 'rgba(59,130,246,0.10)', fontWeight: 600 }
                      : { ...styles.btnSm, padding: '1px 6px', fontSize: 10 }
                    }
                    title={
                      (!selSchema || !selTable)
                        ? '未选择目标表（SQL 格式或未完成 Step 2），无法按表筛选'
                        : historyTableOnly
                          ? `已启用：仅显示 ${selSchema}.${selTable} 的历史（点击关闭）`
                          : `仅显示 ${selSchema}.${selTable} 的历史（点击开启）`
                    }
                  >🎯 仅当前表</button>
                  <button
                    onClick={() => {
                      if (history.length === 0) return;
                      if (confirm(`确认清空 ${history.length} 条历史？不可恢复。`)) {
                        clearHistory();
                        setHistory([]);
                      }
                    }}
                    style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)' }}
                    title="清空全部历史（不可恢复）"
                    disabled={history.length === 0}
                  >🗑 清空全部</button>
                  <button onClick={() => setShowHistory(false)} style={styles.closeBtn}>×</button>
                </div>
              </div>
              {history.length === 0 ? (
                <div style={styles.muted}>本连接暂无导入记录</div>
              ) : (() => {
                // 状态过滤 + 表过滤（M30.111）
                const filtered = historyStatusFilter
                  ? history.filter((h) => h.status === historyStatusFilter)
                  : history;
                const tableFiltered = (historyTableOnly && selSchema && selTable)
                  ? filtered.filter((h) => h.schema === selSchema && h.table === selTable)
                  : filtered;
                // 排序
                const sorted = [...tableFiltered].sort((a, b) => {
                  if (historySort === 'at') return b.at - a.at;
                  if (historySort === 'totalRows') return b.totalRows - a.totalRows;
                  if (historySort === 'ms') return b.ms - a.ms;
                  return b.failedRows - a.failedRows;
                });
                // 汇总（基于 tableFiltered）
                const totalRows = tableFiltered.reduce((s, h) => s + h.totalRows, 0);
                const insertedRows = tableFiltered.reduce((s, h) => s + h.insertedRows, 0);
                const failedRowsSum = tableFiltered.reduce((s, h) => s + h.failedRows, 0);
                const avgMs = tableFiltered.length > 0 ? Math.round(tableFiltered.reduce((s, h) => s + h.ms, 0) / tableFiltered.length) : 0;
                const failPct = totalRows > 0 ? (failedRowsSum / totalRows) * 100 : 0;
                const statusCounts = history.reduce<Record<string, number>>((acc, h) => {
                  acc[h.status] = (acc[h.status] ?? 0) + 1;
                  return acc;
                }, {});
                const sortArrow = (k: HistorySortKey) => historySort === k ? '↓' : '';
                // M30.139 filter 影响范围：仅在有 filter 生效时显示"全部 vs 当前"对比
                const anyFilter = historyStatusFilter !== '' || (historyTableOnly && !!selSchema && !!selTable);
                const allTotalRows = history.reduce((s, h) => s + h.totalRows, 0);
                const allInserted = history.reduce((s, h) => s + h.insertedRows, 0);
                const allFailed = history.reduce((s, h) => s + h.failedRows, 0);
                const allAvgMs = history.length > 0 ? Math.round(history.reduce((s, h) => s + h.ms, 0) / history.length) : 0;
                return (
                  <>
                    <div style={{
                      display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'center',
                      padding: '4px 8px', background: 'rgba(255,255,255,0.02)',
                      border: '1px solid var(--border)', borderRadius: 3,
                      fontSize: 11, marginBottom: 6,
                    }}>
                      <span>共 <strong style={{ color: 'var(--text)' }}>{tableFiltered.length}</strong> 条{anyFilter && <span style={{ color: 'var(--muted)' }}>（全部 {history.length}）</span>}</span>
                      <span>总行 <strong style={{ color: 'var(--text)' }}>{totalRows.toLocaleString()}</strong>{anyFilter && <span style={{ color: 'var(--muted)' }}> / {allTotalRows.toLocaleString()}</span>}</span>
                      <span>成功 <strong style={{ color: 'var(--ok, #10b981)' }}>{insertedRows.toLocaleString()}</strong>{anyFilter && <span style={{ color: 'var(--muted)' }}> / {allInserted.toLocaleString()}</span>}</span>
                      <span>失败 <strong style={{ color: failedRowsSum > 0 ? 'var(--warn, #d97706)' : 'var(--muted)' }}>{failedRowsSum.toLocaleString()}</strong>{anyFilter && <span style={{ color: 'var(--muted)' }}> / {allFailed.toLocaleString()}</span>}</span>
                      <span>平均 <strong style={{ color: 'var(--text)' }}>{avgMs}</strong> ms{anyFilter && <span style={{ color: 'var(--muted)' }}> / {allAvgMs}</span>}</span>
                      <span title="失败行占总行比例">
                        失败率 <strong style={{ color: failPct >= 10 ? 'var(--danger, #dc2626)' : failPct >= 1 ? 'var(--warn, #d97706)' : 'var(--ok, #10b981)' }}>{failPct.toFixed(1)}%</strong>
                      </span>
                    </div>
                    <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', alignItems: 'center', marginBottom: 6, fontSize: 11 }}>
                      <span style={styles.muted}>状态：</span>
                      {([
                        { key: '' as const, label: `全部（${history.length}）` },
                        { key: 'success' as const, label: `✅ 成功（${statusCounts.success ?? 0}）` },
                        { key: 'partial' as const, label: `⚠ 部分（${statusCounts.partial ?? 0}）` },
                        { key: 'failed' as const, label: `❌ 失败（${statusCounts.failed ?? 0}）` },
                        { key: 'cancelled' as const, label: `⏸ 取消（${statusCounts.cancelled ?? 0}）` },
                      ]).map((f) => (
                        <button
                          key={f.key}
                          onClick={() => setHistoryStatusFilter(f.key)}
                          style={{
                            ...styles.btnSm, padding: '1px 6px', fontSize: 10,
                            color: historyStatusFilter === f.key ? 'var(--accent, #3b82f6)' : 'var(--text)',
                            borderColor: historyStatusFilter === f.key ? 'var(--accent, #3b82f6)' : 'var(--border)',
                            background: historyStatusFilter === f.key ? 'rgba(59,130,246,0.10)' : 'transparent',
                            fontWeight: historyStatusFilter === f.key ? 600 : 'inherit',
                          }}
                        >{f.label}</button>
                      ))}
                      <span style={{ ...styles.muted, marginLeft: 8 }}>排序：</span>
                      {([
                        { key: 'at' as const, label: '时间' },
                        { key: 'totalRows' as const, label: '总行' },
                        { key: 'ms' as const, label: '耗时' },
                        { key: 'failedRows' as const, label: '失败数' },
                      ]).map((s) => (
                        <button
                          key={s.key}
                          onClick={() => setHistorySort(s.key)}
                          style={{
                            ...styles.btnSm, padding: '1px 6px', fontSize: 10,
                            color: historySort === s.key ? 'var(--accent, #3b82f6)' : 'var(--text)',
                            borderColor: historySort === s.key ? 'var(--accent, #3b82f6)' : 'var(--border)',
                            background: historySort === s.key ? 'rgba(59,130,246,0.10)' : 'transparent',
                            fontWeight: historySort === s.key ? 600 : 'inherit',
                          }}
                        >{s.label}{sortArrow(s.key)}</button>
                      ))}
                    </div>
                    <div style={styles.historyWrap}>
                      <table style={styles.historyTable}>
                        <thead>
                          <tr>
                            <th style={styles.historyTh}>时间</th>
                            <th style={styles.historyTh}>表</th>
                            <th style={styles.historyTh}>模式</th>
                            <th style={styles.historyTh}>总行</th>
                            <th style={styles.historyTh}>成功</th>
                            <th style={styles.historyTh}>失败</th>
                            <th style={styles.historyTh}>耗时</th>
                            <th style={styles.historyTh}>状态</th>
                            <th style={styles.historyTh}>操作</th>
                          </tr>
                        </thead>
                        <tbody>
                          {sorted.slice(0, 50).map((h) => {
                            const isSqlEntry = h.table === '<SQL>';
                            const jumpTo = () => {
                              if (isSqlEntry) return;
                              jumpRef.current = true;
                              setSelSchema(h.schema);
                              setSelTable(h.table);
                              setCols([]);
                              setMappings([]);
                              setShowHistory(false);
                              setStep('table');
                            };
                            return (
                              <tr key={h.id}>
                                <td style={styles.historyTd}>{formatTime(h.at)}</td>
                                <td
                                  style={{
                                    ...styles.historyTd,
                                    ...(isSqlEntry ? {} : { cursor: 'pointer', color: 'var(--accent, #3b82f6)' }),
                                  }}
                                  title={isSqlEntry ? `${h.schema}.<SQL>` : `点击跳转到 ${h.schema}.${h.table}`}
                                  onClick={isSqlEntry ? undefined : jumpTo}
                                >
                                  {h.kind ? `${h.kind}: ` : ''}{h.schema}.{h.table}
                                </td>
                                <td style={styles.historyTd}>{h.mode}</td>
                                <td style={styles.historyTd}>{h.totalRows}</td>
                                <td style={{ ...styles.historyTd, color: 'var(--ok)' }}>{h.insertedRows}</td>
                                <td style={{ ...styles.historyTd, color: h.failedRows > 0 ? 'var(--warn, #d97706)' : 'var(--muted)' }}>{h.failedRows}</td>
                                <td style={styles.historyTd}>{h.ms}ms</td>
                                <td style={styles.historyTd}>{historyStatusBadge(h.status)}</td>
                                <td style={styles.historyTd}>
                                  {h.failedRowsDetail && h.failedRowsDetail.length > 0 && (
                                    <button
                                      onClick={() => downloadFailedRowsCsv(h)}
                                      style={{ ...styles.btnSm, fontSize: 10, padding: '1px 6px' }}
                                      title="下载失败行 CSV"
                                    >↓ 失败行</button>
                                  )}
                                  {!isSqlEntry && (
                                    <button
                                      onClick={jumpTo}
                                      style={{ ...styles.btnSm, fontSize: 10, padding: '1px 6px', marginLeft: 4 }}
                                      title="跳转到该 schema/table（会自动应用保存的导入预设）"
                                    >🎯 跳转</button>
                                  )}
                                  <button
                                    onClick={() => {
                                      if (!confirm(`删除该条历史记录（${formatTime(h.at)} · ${h.schema}.${h.table}）？不可恢复。`)) return;
                                      deleteHistory(h.id);
                                      setHistory((prev) => prev.filter((x) => x.id !== h.id));
                                    }}
                                    style={{ ...styles.btnSm, fontSize: 10, padding: '1px 5px', marginLeft: 4, color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)' }}
                                    title="删除该条历史（不可恢复）"
                                  >✕</button>
                                </td>
                              </tr>
                            );
                          })}
                        </tbody>
                      </table>
                      {sorted.length > 50 && (
                        <div style={{ ...styles.muted, textAlign: 'center', padding: '4px 0' }}>… 共 {sorted.length} 条，仅显示前 50 条</div>
                      )}
                      {tableFiltered.length === 0 && (
                        <div style={{ ...styles.muted, textAlign: 'center', padding: '8px 0' }}>当前筛选下无匹配记录</div>
                      )}
                    </div>
                  </>
                );
              })()}
            </div>
          )}

          {error && <div style={styles.errorBox}>{error}</div>}

          {step === 'input' && (
            <div style={styles.col}>
              <div style={styles.row2col}>
                <label style={styles.field}>
                  <div style={styles.label}>格式</div>
                  <select
                    value={inputFormat}
                    onChange={(e) => setInputFormat(e.target.value as ImportFormat)}
                    style={styles.select}
                  >
                    <option value="csv">CSV</option>
                    <option value="jsonl">JSONL / JSON 数组</option>
                    <option value="sql">SQL 多语句文件</option>
                  </select>
                </label>
                {inputFormat === 'csv' && (
                  <label style={styles.field}>
                    <div style={styles.label}>分隔符</div>
                    <select
                      value={delimiter ?? ','}
                      onChange={(e) => {
                        const v = e.target.value;
                        const d: Delimiter = v === 'TAB' ? '\t' : (v === '|' ? '|' : v === ';' ? ';' : ',');
                        setDelimiter(d);
                      }}
                      style={styles.select}
                    >
                      <option value=",">逗号 ,</option>
                      <option value=";">分号 ;</option>
                      <option value="TAB">制表符 Tab</option>
                      <option value="|">竖线 |</option>
                    </select>
                  </label>
                )}
                {inputFormat === 'csv' && (
                  <label style={styles.field}>
                    <div style={styles.label}>首行是表头</div>
                    <label style={styles.check}>
                      <input type="checkbox" checked={hasHeader} onChange={(e) => setHasHeader(e.target.checked)} />
                      <span>启用</span>
                    </label>
                  </label>
                )}
                {inputFormat !== 'sql' && (
                  <label style={styles.field}>
                    <div style={styles.label}>编码</div>
                    <select
                      value={encoding}
                      onChange={(e) => redecodeWith(e.target.value as InputEncoding | 'auto')}
                      style={styles.select}
                      title="选择文件后生效；粘贴的文本使用浏览器默认编码"
                    >
                      <option value="auto">自动检测</option>
                      <option value="utf-8">UTF-8</option>
                      <option value="gbk">GBK / GB2312（Windows 中文）</option>
                      <option value="utf-16le">UTF-16 LE</option>
                      <option value="utf-16be">UTF-16 BE</option>
                    </select>
                  </label>
                )}
              </div>
              <div style={styles.label}>
                {inputFormat === 'sql' ? 'SQL 内容（多语句，按分号切分）' : (inputFormat === 'csv' ? 'CSV 内容' : 'JSONL / JSON 数组内容')}
              </div>
              <textarea
                ref={csvTextRef}
                value={csvText}
                onChange={(e) => setCsvText(e.target.value)}
                placeholder={inputFormat === 'sql'
                  ? '粘贴 SQL 文件内容，或用「选择文件」载入 .sql\n支持多语句按分号切分，示例：\nCREATE TABLE ...;\nINSERT INTO ... VALUES (...);\nSELECT COUNT(*) FROM ...;'
                  : inputFormat === 'csv'
                    ? `粘贴 CSV，或点上方"选择文件"、拖拽文件到窗口…\n\n示例：\n${sampleForKind(kind, 'csv').text}`
                    : `粘贴 JSONL（每行一个对象）或 JSON 数组…\n\n示例：\n${sampleForKind(kind, 'jsonl').text}`}
                style={{
                  ...styles.textarea,
                  ...(csvRowFlash != null
                    ? {
                        outline: '2px solid var(--accent, #3b82f6)',
                        outlineOffset: -2,
                        boxShadow: '0 0 0 3px rgba(59,130,246,0.25), inset 0 0 12px rgba(59,130,246,0.15)',
                        transition: 'box-shadow 0.3s ease',
                      }
                    : {}),
                }}
                spellCheck={false}
              />
              <div style={styles.row2col}>
                <button
                  onClick={() => {
                    if (fileInputRef.current) fileInputRef.current.multiple = false;
                    if (fileInputRef.current) fileInputRef.current.click();
                  }}
                  style={styles.btn}
                  title="选择单个文件"
                >📂 选择文件</button>
                <button
                  onClick={() => {
                    if (fileInputRef.current) fileInputRef.current.multiple = true;
                    if (fileInputRef.current) fileInputRef.current.click();
                  }}
                  style={styles.btnGhost}
                  title="选择多个文件按顺序导入（完成后自动加载下一个）"
                >📚 多文件批量…</button>
                <button onClick={fillSample} style={styles.btnGhost}>
                  填入 {sampleForKind(kind, inputFormat === 'sql' ? 'csv' : inputFormat).title}
                </button>
                <input
                  ref={fileInputRef}
                  type="file"
                  accept={inputFormat === 'csv'
                    ? '.csv,.tsv,.txt,text/csv,text/plain'
                    : inputFormat === 'sql'
                      ? '.sql,.txt,text/plain'
                      : '.jsonl,.json,.ndjson,.txt,text/plain,application/json'}
                  style={{ display: 'none' }}
                  onChange={(e) => {
                    const files = Array.from(e.target.files ?? []);
                    e.target.value = '';
                    if (files.length === 0) return;
                    if (files.length > 1) {
                      enqueueFiles(files);
                    } else {
                      void handleFile(files[0], detectFormatFromFile(files[0].name));
                    }
                  }}
                />
              </div>
              {fileName || parse || detectedEncoding ? (() => {
                const curFile = fileQueue[queuePos];
                const size = curFile?.file.size ?? null;
                const sizeStr = size != null
                  ? (size < 1024 ? `${size} B` : size < 1024 * 1024 ? `${(size / 1024).toFixed(1)} KB` : `${(size / 1024 / 1024).toFixed(2)} MB`)
                  : null;
                const items: Array<{ k: string; v: string; color?: string; title?: string }> = [];
                if (fileName) items.push({ k: '文件', v: fileName, title: '已载入的文件名' });
                if (sizeStr) items.push({ k: '大小', v: sizeStr, title: '文件字节大小' });
                if (detectedEncoding) items.push({ k: '编码', v: detectedEncoding, title: '自动检测或用户选择的编码' });
                if (inputFormat === 'csv' && delimiter) items.push({ k: '分隔符', v: delimiter === '\t' ? 'Tab' : delimiter, title: '当前使用的分隔符' });
                if (inputFormat === 'csv' && parse) {
                  items.push({ k: '行 × 列', v: `${parse.rows.length.toLocaleString()} × ${parse.columns.length}` });
                  if (parse.truncated) items.push({ k: '状态', v: '已截断 20 万行', color: 'var(--warn, #d97706)' });
                } else if (inputFormat === 'jsonl' && parse) {
                  items.push({ k: '记录 × 字段', v: `${parse.rows.length.toLocaleString()} × ${parse.columns.length}` });
                } else if (inputFormat === 'sql') {
                  if (sqlStatements.length > 0) items.push({ k: 'SQL 语句', v: `${sqlStatements.length} 条` });
                }
                if (items.length === 0) return null;
                return (
                  <div style={{
                    display: 'flex', flexWrap: 'wrap', gap: '4px 12px',
                    padding: '4px 8px', marginBottom: 4,
                    background: 'rgba(255,255,255,0.02)',
                    border: '1px solid var(--border)',
                    borderLeft: '3px solid var(--ok, #10b981)',
                    borderRadius: 3, fontSize: 11,
                  }}>
                    <span style={{ color: 'var(--ok, #10b981)', fontWeight: 600, marginRight: 4 }}>📄</span>
                    {items.map((it, i) => (
                      <span key={i} style={{ color: 'var(--muted)' }} title={it.title}>
                        {it.k}：<strong style={{ color: it.color ?? 'var(--text)' }}>{it.v}</strong>
                      </span>
                    ))}
                  </div>
                );
              })() : null}
              {fileQueue.length > 0 && (
                <div style={styles.mutedBox}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
                    <span style={{ fontWeight: 600 }}>📚 文件队列（{fileQueue.length} 个，已处理 {queuePos} 个）</span>
                    <span style={styles.spacer} />
                    <button
                      onClick={cancelQueue}
                      style={{ ...styles.btnSm, fontSize: 11, color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)' }}
                      title="取消整个导入队列（若正在执行会同时取消当前批次）"
                    >⏹ 取消整个队列</button>
                  </div>
                  <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
                    {fileQueue.map((q, i) => (
                      <span key={i} style={{
                        ...styles.typeBadge,
                        fontSize: 10,
                        color: i === queuePos ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                        borderColor: i === queuePos ? 'var(--accent, #3b82f6)' : 'var(--border)',
                      }}>
                        {i < queuePos ? '✓ ' : ''}{i + 1}. {q.name}
                      </span>
                    ))}
                  </div>
                </div>
              )}
              {inputFormat === 'sql' && (
                <div style={styles.mutedBox}>
                  {sqlStatements.length > 0 ? (
                    <>已解析 <strong>{sqlStatements.length}</strong> 条 SQL 语句 · 总参数 {sqlParamCounts.reduce((a, b) => a + b, 0)} 个</>
                  ) : csvText.trim() ? (
                    <span style={{ color: 'var(--warn, #d97706)' }}>⚠ 未检测到分号结尾语句</span>
                  ) : null}
                  <details style={{ marginTop: 4 }}>
                    <summary style={{ cursor: 'pointer', fontSize: 11 }}>预览前 5 条</summary>
                    <pre style={{ ...styles.pre, marginTop: 4 }}>
                      {sqlStatements.slice(0, 5).map((s, i) => `#${i + 1}: ${s.sql}`).join('\n')}
                    </pre>
                  </details>
                  {sqlLintResult && (sqlLintResult.errorCount + sqlLintResult.warningCount + sqlLintResult.infoCount) > 0 && (
                    <div style={{ marginTop: 6, padding: 6, background: 'var(--bg-secondary, rgba(0,0,0,0.05))', borderRadius: 4, border: '1px solid var(--border)' }}>
                      <div style={{ fontSize: 11, fontWeight: 600, marginBottom: 4 }}>
                        <span style={{ color: 'var(--danger, #dc2626)' }}>🛡 Live Lint：{severityLabel(sqlLintResult)}</span>
                      </div>
                      <div style={{ ...styles.failedWrap, maxHeight: 180 }}>
                        <table style={styles.failedTable}>
                          <thead>
                            <tr>
                              <th style={styles.failedTh}>行</th>
                              <th style={styles.failedTh}>级别</th>
                              <th style={styles.failedTh}>消息</th>
                            </tr>
                          </thead>
                          <tbody>
                            {sqlLintResult.diagnostics.slice(0, 30).map((d, i) => (
                              <tr key={i}>
                                <td style={styles.failedTd}>L{d.startLine}:{d.startCol}</td>
                                <td style={{ ...styles.failedTd, color: d.severity === 'error' ? 'var(--danger, #dc2626)' : d.severity === 'warning' ? 'var(--warn, #d97706)' : 'var(--muted)' }}>
                                  {d.severity === 'error' ? '✕' : d.severity === 'warning' ? '⚠' : 'ℹ'} {d.severity}
                                </td>
                                <td style={styles.failedTd} title={d.message}>{d.message}</td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                        {sqlLintResult.diagnostics.length > 30 && (
                          <div style={{ ...styles.muted, textAlign: 'center', padding: '2px 0' }}>… 共 {sqlLintResult.diagnostics.length} 条</div>
                        )}
                      </div>
                    </div>
                  )}
                </div>
              )}
              {parse && parse.rows.length > 0 && (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                  <div style={styles.mutedBox}>
                    已解析 <strong>{parse.rows.length}</strong> 行 × <strong>{parse.columns.length}</strong> 列
                    {parse.truncated && <span style={{ color: 'var(--warn, #d97706)' }}> · 已截断至 20 万行</span>}
                  </div>
                  <div style={styles.label}>前 10 行预览</div>
                  <div style={styles.previewWrap}>
                    <table style={styles.previewTable}>
                      <thead>
                        <tr>
                          <th style={styles.previewTh}>#</th>
                          {parse.columns.map((c, i) => {
                            const p = profiles[i];
                            return (
                              <th key={i} style={styles.previewTh} title={p ? `${p.type} · null ${p.nullCount}/${p.total}` : c}>
                                <div style={styles.previewThInner}>
                                  <span>{c}</span>
                                  {p && <span style={{ ...styles.typeBadge, color: typeBadgeColor(p.type) }}>{p.type}</span>}
                                </div>
                                {p && p.nullCount > 0 && <div style={styles.previewThSub}>{p.nullCount} 空</div>}
                              </th>
                            );
                          })}
                        </tr>
                      </thead>
                      <tbody>
                        {parse.rows.slice(0, 10).map((row, ri) => (
                          <tr key={ri}>
                            <td style={styles.previewTdMuted}>{ri + 1}</td>
                            {parse.columns.map((_, ci) => {
                              const v = row[ci] ?? '';
                              const isNull = v === '';
                              return (
                                <td key={ci} style={{ ...styles.previewTd, ...(isNull ? styles.previewTdNull : {}) }} title={v}>
                                  {isNull ? <em>NULL</em> : v}
                                </td>
                              );
                            })}
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </div>
              )}
              {qualityReport && (
                <div style={{ ...styles.mutedBox, borderLeft: '3px solid var(--accent)', display: 'flex', flexDirection: 'column', gap: 6 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
                    <span style={styles.label}>📊 数据质量</span>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, flex: 1, minWidth: 200 }}>
                      <div style={{ ...styles.progressOuter, flex: 1, maxWidth: 240 }}>
                        <div style={{
                          ...styles.progressInner,
                          width: `${qualityReport.overallScore}%`,
                          background: qualityScoreColor(qualityReport.overallScore),
                        }} />
                      </div>
                      <strong style={{ fontSize: 13, color: qualityScoreColor(qualityReport.overallScore), minWidth: 44, textAlign: 'right' }}>
                        {qualityReport.overallScore}/100
                      </strong>
                    </div>
                    {qualityReport.issues.length > 0 && (
                      <span style={{ fontSize: 11, color: 'var(--warn, #d97706)' }}>
                        {qualityReport.issues.length} 项待关注
                      </span>
                    )}
                    {(() => {
                      const totalSugg = qualityReport.columns.reduce((n, c) => n + c.suggestions.length, 0);
                      if (totalSugg === 0) return null;
                      return (
                        <button
                          onClick={() => {
                            const map: Record<number, QualityTransformSuggestion[]> = {};
                            for (const c of qualityReport.columns) {
                              if (c.suggestions.length > 0) map[c.index] = c.suggestions;
                            }
                            setPendingQualitySuggestions(map);
                            publishEditorStatus({
                              message: `已保存 ${totalSugg} 条转换建议，进入 Step 3 后自动应用`,
                              messageAt: Date.now(),
                            });
                          }}
                          style={{ ...styles.btnSm, fontSize: 11 }}
                          title="将可疑值建议（Trim / 去前导 0 / 正则去除尾引号 / 日期转 ISO）保存到下一步的列映射"
                        >🎯 保存建议（{totalSugg} 条）</button>
                      );
                    })()}
                    <button
                      onClick={() => {
                        const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
                        const target = selSchema && selTable ? `${selSchema}.${selTable}` : 'unmapped';
                        const report = {
                          schema_version: 'polydb.dataQuality.v1',
                          generated_at: new Date().toISOString(),
                          source_file: fileName || 'inline-paste',
                          input_format: inputFormat,
                          encoding: detectedEncoding ?? 'unknown',
                          target: { kind: kind ?? null, schema: selSchema || null, table: selTable || null },
                          row_count: parse?.rows.length ?? 0,
                          column_count: parse?.columns.length ?? 0,
                          overall_score: qualityReport.overallScore,
                          gate: { enabled: qualityGate.enabled, threshold: qualityGate.threshold, passed: !qualityGateBlocked },
                          issues: qualityReport.issues,
                          columns: qualityReport.columns.map((c) => ({
                            index: c.index,
                            name: c.name,
                            total: c.total,
                            non_null: c.nonNull,
                            completeness: c.completeness,
                            dominant_type: c.dominantType,
                            type_purity: c.typePurity,
                            unique_ratio: c.uniqueRatio,
                            suspicious_count: c.suspiciousCount,
                            suspicious_breakdown: c.suspiciousBreakdown,
                            score: c.score,
                            issues: c.issues,
                            suggestions: c.suggestions,
                          })),
                        };
                        const blob = new Blob([JSON.stringify(report, null, 2)], { type: 'application/json' });
                        const url = URL.createObjectURL(blob);
                        const a = document.createElement('a');
                        a.href = url;
                        a.download = `quality-${target}-${stamp}.json`;
                        document.body.appendChild(a);
                        a.click();
                        document.body.removeChild(a);
                        URL.revokeObjectURL(url);
                        publishEditorStatus({ message: `📥 已导出质量报告 ${a.download}`, messageAt: Date.now() });
                      }}
                      style={{ ...styles.btnSm, fontSize: 11 }}
                      title="导出当前质量评估为 JSON 快照（含每列明细 + 分类统计 + 门禁状态），便于审计/CI 追溯"
                    >📥 导出 JSON</button>
                  </div>
                  <details>
                    <summary style={{ cursor: 'pointer', fontSize: 11, color: 'var(--muted)', userSelect: 'none' }}>
                      查看每列评分
                    </summary>
                    <div style={{ marginTop: 6, fontSize: 11, maxHeight: 200, overflow: 'auto', border: '1px solid var(--border)', borderRadius: 4 }}>
                      <table style={{ width: '100%', borderCollapse: 'collapse', fontFamily: 'monospace' }}>
                        <thead>
                          <tr>
                            <th style={styles.historyTh}>列</th>
                            <th style={styles.historyTh}>分数</th>
                            <th style={styles.historyTh}>完整度</th>
                            <th style={styles.historyTh}>主类型</th>
                            <th style={styles.historyTh}>可疑值</th>
                            <th style={styles.historyTh}>备注</th>
                          </tr>
                        </thead>
                        <tbody>
                          {qualityReport.columns.map((c) => (
                            <tr key={c.index}>
                              <td style={styles.historyTd} title={c.name}>{c.name}</td>
                              <td style={styles.historyTd} title={`${c.score}/100`}>
                                <strong style={{ color: qualityScoreColor(c.score) }}>{c.score}</strong>
                              </td>
                              <td style={styles.historyTd}>{(c.completeness * 100).toFixed(0)}%</td>
                              <td style={styles.historyTd}>{c.dominantType}</td>
                              <td style={styles.historyTd}>{c.suspiciousCount}</td>
                              <td style={{ ...styles.historyTd, maxWidth: 260, whiteSpace: 'normal', wordBreak: 'break-word' }}>
                                {c.issues.length === 0 ? <span style={{ color: 'var(--ok)' }}>✓</span> : c.issues.join(' · ')}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  </details>
                </div>
              )}
              <div style={styles.footer}>
                {fileName && <span style={styles.muted}>文件：{fileName}</span>}
                {detectedEncoding && <span style={styles.muted}> · 编码：{detectedEncoding}</span>}
                <span style={styles.spacer} />
                <button onClick={handleParse} disabled={!csvText.trim()} style={styles.btnPrimary}>下一步 →</button>
              </div>
            </div>
          )}

          {step === 'table' && (
            <Step2Table
              connId={connId}
              selSchema={selSchema} setSelSchema={setSelSchema}
              selTable={selTable} setSelTable={setSelTable}
              setCols={setCols} setMappings={setMappings}
              opts={opts} setOpts={setOpts}
              schemaLoading={schemaLoading} tablesLoading={tablesLoading} colsLoading={colsLoading}
              schemas={schemas} tables={tables} cols={cols}
              parse={parse}
              setStep={setStep}
            />
          )}

          {step === 'map' && (
            <div style={styles.col}>
              <div style={{ ...styles.row2col, justifyContent: 'space-between', alignItems: 'center' }}>
                <span style={styles.label}>列映射（左侧 CSV 列 → 右侧表列）</span>
                <div style={{ display: 'flex', gap: 6 }}>
                  <button
                    onClick={() => {
                      const prev = JSON.parse(JSON.stringify(opts)) as ImportOptions;
                      const total = applyOneClickOptimize();
                      if (total === 0) return;
                      const meta = failCategoryMeta('type');
                      pushFix({
                        key: 'type',
                        label: meta.label,
                        icon: meta.icon,
                        snapshot: prev,
                        msg: '已按质量报告应用转换建议（trim / date-parse / scale 等）',
                        at: Date.now(),
                      });
                    }}
                    style={styles.btnSm}
                    title="根据 CSV 数据画像与目标列约束（NOT NULL / 默认值 / dtype / 列名）自动应用转换、空值策略、必填校验、正则校验、枚举约束（可 Ctrl+Z 撤销）"
                  >✨ 一键优化</button>
                  <button
                    onClick={() => {
                      if (!selTable || cols.length === 0) return;
                      const t = generateBlankTemplate(selSchema, selTable, cols, { includeAutoInc: false });
                      if (!t.csv) { setError('目标表全部为自增列，无法生成空白模板'); return; }
                      triggerDownload(
                        new Blob([t.csv], { type: 'text/csv' }),
                        `${selSchema}_${selTable}_import_template.csv`,
                      );
                      publishEditorStatus({
                        message: `已生成 ${selTable} 空白 CSV 模板（${t.usedCols} 列，跳过 ${t.skippedCols} 自增列）`,
                        messageAt: Date.now(),
                      });
                    }}
                    style={styles.btnSm}
                    title="根据目标表结构导出空白 CSV 模板，填完再导入"
                  >📄 下载空白 CSV 模板</button>
                </div>
              </div>
              {cols.length > 0 && (() => {
                const mappedTargets = new Set(mappings.map((m) => m.targetColumn).filter((t): t is string => t != null));
                const unmapped = cols.filter((c) => !mappedTargets.has(c.name));
                const mappedCount = mappedTargets.size;
                const pct = cols.length > 0 ? Math.round((mappedCount / cols.length) * 100) : 0;
                const barColor = pct === 100 ? 'var(--ok, #10b981)' : pct >= 50 ? 'var(--accent, #3b82f6)' : 'var(--warn, #d97706)';
                const shown = unmapped.slice(0, 10);
                const rest = unmapped.length - shown.length;
                return (
                  <div style={{ marginTop: 6, padding: '6px 8px', background: 'rgba(255,255,255,0.02)', border: '1px solid var(--border)', borderRadius: 3, fontSize: 11 }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
                      <span style={{ color: 'var(--muted)' }}>📊 覆盖率</span>
                      <div style={{ flex: 1, height: 6, background: 'rgba(255,255,255,0.05)', borderRadius: 3, overflow: 'hidden' }}>
                        <div style={{ width: `${pct}%`, height: '100%', background: barColor, transition: 'width 0.2s' }} />
                      </div>
                      <strong style={{ color: barColor }}>{mappedCount}/{cols.length}</strong>
                      <span style={styles.muted}>{pct}%</span>
                    </div>
                    {unmapped.length > 0 ? (
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, alignItems: 'center' }}>
                        <span style={{ color: 'var(--muted)' }}>未映射目标列：</span>
                        {shown.map((c) => (
                          <span
                            key={c.name}
                            style={{
                              padding: '1px 6px',
                              borderRadius: 2,
                              fontSize: 10,
                              border: `1px solid ${!c.nullable ? 'var(--danger, #dc2626)' : 'var(--accent, #3b82f6)'}`,
                              color: !c.nullable ? 'var(--danger, #dc2626)' : 'var(--accent, #3b82f6)',
                              background: !c.nullable ? 'rgba(220,38,38,0.08)' : 'rgba(59,130,246,0.08)',
                            }}
                            title={c.nullable ? `${c.name} · 可空 · ${c.data_type}` : `⚠ ${c.name} · NOT NULL · ${c.data_type}${c.default_value != null ? ` · 默认值: ${c.default_value}` : ''}`}
                          >
                            {c.name}{!c.nullable && <span style={{ marginLeft: 2 }}>!</span>}
                          </span>
                        ))}
                        {rest > 0 && <span style={styles.muted}>…+{rest}</span>}
                      </div>
                    ) : (
                      <div style={{ color: 'var(--ok, #10b981)' }}>✓ 目标表所有列均已映射</div>
                    )}
                  </div>
                );
              })()}
              <div style={{ display: 'flex', gap: 4, marginTop: 4, alignItems: 'center', fontSize: 11 }}>
                {(() => {
                  // M30.90 上下文"下一步"提示：根据当前状态智能推荐下一步操作
                  const isDefaultView = mappingFilter === 'all' && mappingSort === 'index' && mappingSearch.trim() === '';
                  const unhealthyCount = columnHealths ? columnHealths.filter((h) => h.health < 80).length : 0;
                  const hasSelection = batchSel.size > 0;
                  // 优先级：有选中 > 有异常 > 全部默认
                  if (hasSelection) {
                    return (
                      <span
                        style={{
                          fontSize: 10, padding: '1px 6px', borderRadius: 2,
                          background: 'rgba(59,130,246,0.10)',
                          color: 'var(--accent, #3b82f6)',
                          border: '1px solid rgba(59,130,246,0.35)',
                          lineHeight: '16px',
                          whiteSpace: 'nowrap',
                          flexShrink: 0,
                        }}
                        title={`已选中 ${batchSel.size} 列，批量条下方有「⚡ 应用建议 / 清空设置 / 取消选择」等操作`}
                      >💡 下一步：在批量条应用建议或清空设置</span>
                    );
                  }
                  if (!isDefaultView) return null; // 用户在过滤/排序/搜索中，不主动打断
                  if (unhealthyCount > 0) {
                    return (
                      <span
                        style={{
                          fontSize: 10, padding: '1px 6px', borderRadius: 2,
                          background: 'rgba(217,119,6,0.10)',
                          color: 'var(--warn, #d97706)',
                          border: '1px solid rgba(217,119,6,0.35)',
                          lineHeight: '16px',
                          whiteSpace: 'nowrap',
                          flexShrink: 0,
                        }}
                        title={`共 ${unhealthyCount} 列健康分 < 80，建议先处理。Alt+4 或 Ctrl+J 快速定位`}
                      >💡 下一步：Alt+4 过滤低健康 · Ctrl+J 逐行跳</span>
                    );
                  }
                  return null;
                })()}
                <span style={styles.muted}>列过滤器：</span>
                {([
                  { key: 'all' as MappingFilter, label: '全部' },
                  { key: 'unmapped' as MappingFilter, label: `未映射${mappings.length > 0 ? `（${mappings.filter((m) => m.targetColumn == null).length}）` : ''}` },
                  { key: 'mapped' as MappingFilter, label: `已映射${mappings.length > 0 ? `（${mappings.filter((m) => m.targetColumn != null).length}）` : ''}` },
                  { key: 'low-health' as MappingFilter, label: `低健康${columnHealths ? `（${columnHealths.filter(h => h.health < 80).length}）` : ''}` },
                  { key: 'critical-health' as MappingFilter, label: `紧急${columnHealths ? `（${columnHealths.filter(h => h.health < 60).length}）` : ''}` },
                ]).map((f) => {
                  const isActive = mappingFilter === f.key;
                  // 低健康用 warn 橙、紧急用 danger 红、其余 accent 蓝
                  const activeColor = f.key === 'low-health' ? 'var(--warn, #d97706)' : f.key === 'critical-health' ? 'var(--danger, #dc2626)' : 'var(--accent, #3b82f6)';
                  const activeBg = f.key === 'low-health' ? 'rgba(217,119,6,0.10)' : f.key === 'critical-health' ? 'rgba(220,38,38,0.10)' : 'rgba(59,130,246,0.10)';
                  return (
                    <button
                      key={f.key}
                      onClick={() => persistMappingFilter(f.key)}
                      style={{
                        ...styles.btnSm, padding: '1px 8px', fontSize: 11,
                        color: isActive ? activeColor : 'var(--text)',
                        borderColor: isActive ? activeColor : 'var(--border)',
                        background: isActive ? activeBg : 'transparent',
                        fontWeight: isActive ? 600 : 'inherit',
                      }}
                      title={`按当前过滤器排序映射行（匹配行置顶，非匹配行下沉且半透显示）`}
                    >{f.label}</button>
                  );
                })}
                <button
                  type="button"
                  style={{
                    ...styles.btnSm, padding: '1px 6px', fontSize: 10,
                    color: 'var(--muted)', borderColor: 'var(--border)',
                    background: 'transparent', cursor: 'default',
                  }}
                  title={
                    [
                      'Step 3 列映射快捷键：',
                      'Alt+1 → 全部',
                      'Alt+2 → 未映射',
                      'Alt+3 → 已映射',
                      'Alt+4 → 低健康',
                      'Alt+5 → 紧急（健康 < 60）',
                      'Alt+0 → 清空搜索',
                      'Ctrl+F → 聚焦列搜索框',
                      'Alt+R  → 重置视图（过滤器+排序+搜索）',
                      'Alt+N  → 跳到下一个未映射列',
                      'Alt+P  → 跳到上一个未映射列',
                      'Alt+H  → 跳到首个未映射列',
                      'Alt+L  → 跳到末位未映射列',
                      'Ctrl+J → 跳到下一个低健康行',
                      'Ctrl+K → 跳到上一个低健康行',
                    ].join('\n')
                  }
                  aria-label="显示列映射快捷键"
                >⌨</button>
                {mappingFilter !== 'all' && (
                  <span style={styles.muted} title="切换过滤器即恢复显示">· 当前只显示匹配项在前</span>
                )}
                <span style={{ flex: 1 }} />
                <span style={{ ...styles.muted, fontSize: 10 }} title="调整映射行显示顺序（数据索引不变）">排序：</span>
                <select
                  value={mappingSort}
                  onChange={(e) => persistMappingSort(e.target.value as MappingSort)}
                  style={{ ...styles.selectSm, padding: '1px 4px', fontSize: 10 }}
                  title="映射行排序：CSV 索引（默认）· 健康分升序（差→好）· 空值率降序（多→少）· 目标列名升序（未映射置尾）· CSV 名升序"
                >
                  <option value="index">CSV 索引</option>
                  <option value="health-asc">健康分 升</option>
                  <option value="empty-desc">空值率 降</option>
                  <option value="target-asc">目标列名 升</option>
                  <option value="csv-asc">CSV 名 升</option>
                </select>
                {(mappingFilter !== 'all' || mappingSort !== 'index' || mappingSearch.trim() !== '') && (
                  <button
                    type="button"
                    onClick={() => {
                      persistMappingFilter('all');
                      persistMappingSort('index');
                      setMappingSearch('');
                      setMappingSearchHistOpen(false);
                      setMappingSearchHistIdx(-1);
                    }}
                    style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10, color: 'var(--muted)' }}
                    title="一键重置：过滤器=全部 + 排序=CSV 索引 + 清空搜索 · 快捷键 Alt+R"
                    aria-label="重置过滤器、排序、搜索到默认"
                  >↺ 重置</button>
                )}
                {(() => {
                  // M30.88 状态摘要 chip：当 filter/sort/search 任一非默认时列出偏离项，方便用户一眼看清当前视图状态
                  const parts: string[] = [];
                  if (mappingFilter !== 'all') {
                    const labels: Record<MappingFilter, string> = {
                      'all': '全部', 'mapped': '已映射', 'unmapped': '未映射',
                      'low-health': '低健康', 'critical-health': '紧急',
                    };
                    parts.push(`过滤:${labels[mappingFilter]}`);
                  }
                  if (mappingSort !== 'index') {
                    const labels: Record<MappingSort, string> = {
                      'index': 'CSV 索引', 'health-asc': '健康分 升', 'empty-desc': '空值率 降',
                      'target-asc': '目标列名 升', 'csv-asc': 'CSV 名 升',
                    };
                    parts.push(`排序:${labels[mappingSort]}`);
                  }
                  if (mappingSearch.trim() !== '') parts.push(`搜索:"${mappingSearch.trim()}"`);
                  if (parts.length === 0) return null;
                  const text = parts.join(' · ');
                  return (
                    <span
                      style={{
                        fontSize: 10, padding: '1px 6px', borderRadius: 2,
                        background: 'rgba(59,130,246,0.10)',
                        color: 'var(--accent, #3b82f6)',
                        border: '1px solid rgba(59,130,246,0.35)',
                        fontFamily: 'var(--mono, ui-monospace)',
                        lineHeight: '16px',
                        maxWidth: 240, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                      }}
                      title={`${text}\n点击 ↺ 重置 或按 Alt+R 回到全部+CSV 索引+空搜索`}
                    >{text}</span>
                  );
                })()}
                {mappings.length > 0 && (() => {
                  // 全选当前过滤器+搜索命中的映射行；无过滤/搜索时全选
                  const q = mappingSearch.trim().toLowerCase();
                  const idxs: number[] = [];
                  for (let i = 0; i < mappings.length; i++) {
                    const m = mappings[i];
                    let filterPass = true;
                    if (mappingFilter === 'unmapped') filterPass = m.targetColumn == null;
                    else if (mappingFilter === 'mapped') filterPass = m.targetColumn != null;
                    else if (mappingFilter === 'low-health') {
                      const ch = columnHealths?.[i];
                      filterPass = ch != null && ch.health < 80;
                    } else if (mappingFilter === 'critical-health') {
                      const ch = columnHealths?.[i];
                      filterPass = ch != null && ch.health < 60;
                    }
                    if (!filterPass) continue;
                    if (q) {
                      const searchPass = m.csvName.toLowerCase().includes(q)
                        || (m.targetColumn && m.targetColumn.toLowerCase().includes(q));
                      if (!searchPass) continue;
                    }
                    idxs.push(i);
                  }
                  const total = idxs.length;
                  if (total === 0) return null;
                  const hidden = mappings.length - total;
                  const chipColor = hidden > 0 ? 'var(--warn, #d97706)' : 'var(--muted)';
                  const chipBg = hidden > 0 ? 'rgba(217,119,6,0.10)' : 'rgba(255,255,255,0.03)';
                  return (
                    <>
                      <button
                        type="button"
                        onClick={() => {
                          if (idxs.length === 0) return;
                          setBatchSel(new Set<number>(idxs));
                          setBatchAnchor(idxs[0]);
                        }}
                        style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 10 }}
                        title="选中当前过滤器+搜索命中的映射行；无过滤/搜索时选中全部"
                      >全选（{total}）</button>
                      <button
                        type="button"
                        onClick={hidden > 0 ? () => {
                          persistMappingFilter('all');
                          setMappingSearch('');
                        } : undefined}
                        style={{
                          fontSize: 10, padding: '0 5px', borderRadius: 2,
                          fontFamily: 'var(--mono, ui-monospace)',
                          color: chipColor, background: chipBg,
                          cursor: hidden > 0 ? 'pointer' : 'default',
                          border: hidden > 0 ? `1px solid ${chipColor}` : 'none',
                          lineHeight: '16px',
                        }}
                        title={hidden > 0
                          ? `可见 ${total}/${mappings.length}（隐藏 ${hidden}）· 点击回到「全部」过滤器并清空搜索`
                          : `可见 ${total}/${mappings.length}（无隐藏）`}
                        aria-label={hidden > 0 ? `可见 ${total} 共 ${mappings.length}，点击重置过滤器与搜索` : `可见 ${total} 共 ${mappings.length}`}
                      >{total}/{mappings.length}</button>
                    </>
                  );
                })()}
              </div>
              <div style={{ display: 'flex', gap: 4, marginTop: 3, alignItems: 'center', fontSize: 11 }}>
                {(() => {
                  // 搜索命中数（M30.73）：在搜索激活时先算命中数，用于展示 + 一键选中
                  const q = mappingSearch.trim().toLowerCase();
                  if (!q) return null;
                  let hitCount = 0;
                  for (const m of mappings) {
                    if (m.csvName.toLowerCase().includes(q) || (m.targetColumn && m.targetColumn.toLowerCase().includes(q))) hitCount++;
                  }
                  return (
                    <span style={{
                      fontSize: 10, padding: '1px 6px', borderRadius: 2,
                      fontFamily: 'var(--mono, ui-monospace)',
                      color: hitCount === 0 ? 'var(--warn, #d97706)' : 'var(--accent, #3b82f6)',
                      background: hitCount === 0 ? 'rgba(217,119,6,0.10)' : 'rgba(59,130,246,0.10)',
                    }} title={`搜索「${mappingSearch.trim()}」命中 ${hitCount}/${mappings.length} 列`}>
                      {hitCount}/{mappings.length} 命中
                    </span>
                  );
                })()}
                <div style={{ position: 'relative', flex: 1, maxWidth: 320, minWidth: 200 }}>
                  <input
                    ref={(el) => { mapSearchInputRef.current = el; }}
                    type="text"
                    value={mappingSearch}
                    onChange={(e) => {
                      setMappingSearch(e.target.value);
                      // 输入新内容时重置到默认无选中态；有历史就展示下拉
                      setMappingSearchHistIdx(-1);
                      if (mappingSearchHistory.length > 0) setMappingSearchHistOpen(true);
                    }}
                    onFocus={() => {
                      if (mappingSearchHistory.length > 0) setMappingSearchHistOpen(true);
                    }}
                    onBlur={() => {
                      // 延迟以让 dropdown 的 mousedown 先触发；同时自动保存当前搜索词
                      const current = mappingSearch;
                      window.setTimeout(() => {
                        setMappingSearchHistOpen(false);
                        setMappingSearchHistIdx(-1);
                        if (current.trim()) saveMappingSearchHistory(current);
                      }, 120);
                    }}
                    onKeyDown={(e) => {
                      if (!mappingSearchHistOpen || mappingSearchHistory.length === 0) {
                        if (e.key === 'Enter') {
                          e.preventDefault();
                          saveMappingSearchHistory(mappingSearch);
                        }
                        return;
                      }
                      if (e.key === 'ArrowDown') {
                        e.preventDefault();
                        setMappingSearchHistIdx((i) => (i + 1) % mappingSearchHistory.length);
                      } else if (e.key === 'ArrowUp') {
                        e.preventDefault();
                        setMappingSearchHistIdx((i) => (i - 1 + mappingSearchHistory.length) % mappingSearchHistory.length);
                      } else if (e.key === 'Enter') {
                        e.preventDefault();
                        if (mappingSearchHistIdx >= 0 && mappingSearchHistIdx < mappingSearchHistory.length) {
                          const picked = mappingSearchHistory[mappingSearchHistIdx];
                          setMappingSearch(picked);
                          saveMappingSearchHistory(picked);
                        } else {
                          saveMappingSearchHistory(mappingSearch);
                        }
                        setMappingSearchHistOpen(false);
                        setMappingSearchHistIdx(-1);
                      } else if (e.key === 'Escape') {
                        e.preventDefault();
                        setMappingSearch('');
                        setMappingSearchHistOpen(false);
                        setMappingSearchHistIdx(-1);
                      }
                    }}
                    placeholder={mappingSearchHistory.length > 0 ? '🔍 搜索列名…（↓ 历史）' : '🔍 搜索列名…'}
                    style={{ ...styles.inputSm, padding: '1px 6px', fontSize: 11, width: '100%', boxSizing: 'border-box' }}
                    title="按 CSV 列名或目标列名子串搜索（不区分大小写），匹配项置顶，非匹配项半透；↑/↓ 循环历史，Enter 应用，Esc 清空"
                  />
                  {mappingSearchHistOpen && mappingSearchHistory.length > 0 && (
                    <div style={{
                      position: 'absolute', top: '100%', left: 0, right: 0, marginTop: 2,
                      background: 'var(--bg, #1e1e2e)', border: '1px solid var(--border, #3b3b4f)',
                      borderRadius: 4, boxShadow: '0 4px 12px rgba(0,0,0,0.4)',
                      zIndex: 50, maxHeight: 240, overflowY: 'auto',
                      fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                      fontSize: 11,
                    }}>
                      {mappingSearchHistory.map((h, i) => {
                        const isSel = i === mappingSearchHistIdx;
                        return (
                          <div
                            key={`${h}-${i}`}
                            onMouseDown={(e) => {
                              e.preventDefault();
                              setMappingSearch(h);
                              saveMappingSearchHistory(h);
                              setMappingSearchHistOpen(false);
                              setMappingSearchHistIdx(-1);
                            }}
                            onMouseEnter={() => setMappingSearchHistIdx(i)}
                            style={{
                              padding: '3px 8px',
                              cursor: 'pointer',
                              background: isSel ? 'rgba(59,130,246,0.15)' : 'transparent',
                              color: isSel ? 'var(--accent, #3b82f6)' : 'var(--text)',
                              fontWeight: isSel ? 600 : 'inherit',
                              display: 'flex', alignItems: 'center', gap: 6,
                            }}
                          >
                            <span style={{ opacity: 0.5 }}>🕘</span>
                            <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{h}</span>
                            <span
                              onMouseDown={(e) => {
                                e.preventDefault();
                                e.stopPropagation();
                                setMappingSearchHistory((prev) => {
                                  const next = prev.filter((_, idx) => idx !== i);
                                  try { localStorage.setItem('polydb.mappingSearchHistory.v1', JSON.stringify(next)); } catch { /* ignore */ }
                                  return next;
                                });
                              }}
                              title="从历史中移除这条"
                              style={{
                                opacity: 0.5, cursor: 'pointer', padding: '0 4px',
                                fontSize: 10, borderRadius: 2,
                                ...(isSel ? { background: 'rgba(0,0,0,0.2)' } : {}),
                              }}
                            >✕</span>
                          </div>
                        );
                      })}
                      <div
                        onMouseDown={(e) => {
                          e.preventDefault();
                          setMappingSearchHistory([]);
                          try { localStorage.removeItem('polydb.mappingSearchHistory.v1'); } catch { /* ignore */ }
                        }}
                        style={{
                          padding: '3px 8px',
                          cursor: 'pointer',
                          borderTop: '1px solid var(--border, #3b3b4f)',
                          color: 'var(--muted, #8b8b9e)',
                          fontSize: 10,
                          textAlign: 'center',
                        }}
                        title="清空搜索历史"
                      >清空历史</div>
                    </div>
                  )}
                </div>
                {mappingSearch && (
                  <button
                    type="button"
                    onClick={() => {
                      // M30.73：一键选中所有搜索命中列，衔接 M30.61-71 批量选通道
                      const q = mappingSearch.trim().toLowerCase();
                      const hits: number[] = [];
                      for (let i = 0; i < mappings.length; i++) {
                        const m = mappings[i];
                        if (m.csvName.toLowerCase().includes(q) || (m.targetColumn && m.targetColumn.toLowerCase().includes(q))) hits.push(i);
                      }
                      if (hits.length === 0) return;
                      setBatchSel(new Set<number>(hits));
                      setBatchAnchor(hits[0]);
                    }}
                    style={{ ...styles.btnSm, padding: '1px 8px', fontSize: 11, color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)' }}
                    title="选中所有搜索命中列（当前搜索词），继续可用批量转换/空值策略/清空设置"
                  >全选命中</button>
                )}
                {mappingSearch && (
                  <button
                    type="button"
                    onClick={() => setMappingSearch('')}
                    style={{ ...styles.btnSm, padding: '1px 6px', fontSize: 11 }}
                    title="清空搜索"
                  >✕ 清空</button>
                )}
              </div>
              {parse && (() => {
                const total = parse.rows.length;
                if (total === 0) return null;
                // 健康汇总使用小 cap 控制全列遍历成本：10 万行 × N 列会阻塞渲染
                const cap = Math.min(total, 10000);
                let colsWithTransform = 0;
                let colsWithNonDefaultPolicy = 0;
                let colsWithValidation = 0;
                let totalEmpty = 0;
                let totalViolations = 0;
                let colsWithEmpty = 0;
                let colsWithViolations = 0;
                const transformIdxs: number[] = [];
                const policyIdxs: number[] = [];
                const valIdxs: number[] = [];
                const emptyIdxs: number[] = [];
                const violIdxs: number[] = [];
                // 预先算重复目标列：一个 target 对应 N 个 CSV 列时，除首列外的 N-1 列都会被覆盖
                const targetCount = new Map<string, number>();
                for (let i = 0; i < mappings.length; i++) {
                  const t = mappings[i].targetColumn;
                  if (!t) continue;
                  targetCount.set(t, (targetCount.get(t) ?? 0) + 1);
                }
                let shadowedCols = 0;
                for (const c of targetCount.values()) {
                  if (c > 1) shadowedCols += c - 1;
                }
                const n = mappings.length;
                // 收集重复目标列的所有索引（含胜出列），用于批量选中
                const dupAllIdxs: number[] = [];
                for (let i = 0; i < n; i++) {
                  if (opts.transforms?.[i] && opts.transforms[i] !== 'none') { colsWithTransform++; transformIdxs.push(i); }
                  if (opts.nullPolicies?.[i] && opts.nullPolicies[i] !== 'null') { colsWithNonDefaultPolicy++; policyIdxs.push(i); }
                  const t = mappings[i].targetColumn;
                  if (t && (targetCount.get(t) ?? 0) > 1) dupAllIdxs.push(i);
                  const v = opts.validations?.[i];
                  const hasVal = !!(v && hasAnyValidation(v));
                  if (hasVal) { colsWithValidation++; valIdxs.push(i); }
                  let empty = 0;
                  let viol = 0;
                  for (let r = 0; r < cap; r++) {
                    const cell = parse.rows[r]?.[i];
                    const rawStr = cell == null ? '' : String(cell);
                    if (rawStr === '') {
                      empty++;
                    } else if (hasVal) {
                      const reason = validateCell(rawStr, v!);
                      if (reason) viol++;
                    }
                  }
                  if (empty > 0) { colsWithEmpty++; emptyIdxs.push(i); }
                  if (viol > 0) { colsWithViolations++; violIdxs.push(i); }
                  totalEmpty += empty;
                  totalViolations += viol;
                }
                const emptyPct = (totalEmpty * 100) / cap;
                const violPct = (totalViolations * 100) / cap;
                const capped = total > cap;
                const fmt = (n: number) => capped ? `≥${n}` : String(n);
                return (
                  <div
                    style={{
                      display: 'flex',
                      flexWrap: 'wrap',
                      gap: 6,
                      padding: '5px 8px',
                      borderRadius: 3,
                      background: 'rgba(255,255,255,0.03)',
                      border: '1px solid var(--border)',
                      fontSize: 10,
                      fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                      alignItems: 'center',
                    }}
                    title={`映射健康汇总（分母为前 ${cap} 行数据）${capped ? `，超 ${cap} 行时抽样` : ''}`}
                  >
                    <span style={{ color: 'var(--muted)' }}>📋 {n} 列</span>
                    {colsWithTransform > 0 ? (
                      <button
                        type="button"
                        onClick={() => {
                          setBatchSel(new Set<number>(transformIdxs));
                          setBatchAnchor(transformIdxs[0]);
                        }}
                        title={`点击选中这 ${colsWithTransform} 列（已配置列级转换）`}
                        style={{
                          background: 'transparent',
                          border: '1px solid var(--accent, #3b82f6)',
                          color: 'var(--accent, #3b82f6)',
                          cursor: 'pointer',
                          fontSize: 10,
                          padding: '0 4px',
                          borderRadius: 2,
                          fontFamily: 'inherit',
                          lineHeight: '16px',
                        }}
                      >
                        ⚙ {colsWithTransform} 转换 →
                      </button>
                    ) : (
                      <span style={{ color: 'var(--muted)' }}>⚙ 0 转换</span>
                    )}
                    {colsWithNonDefaultPolicy > 0 ? (
                      <button
                        type="button"
                        onClick={() => {
                          setBatchSel(new Set<number>(policyIdxs));
                          setBatchAnchor(policyIdxs[0]);
                        }}
                        title={`点击选中这 ${colsWithNonDefaultPolicy} 列（已配置非默认空值策略：empty-string / skip-row / use-default）`}
                        style={{
                          background: 'transparent',
                          border: '1px solid var(--warn, #d97706)',
                          color: 'var(--warn, #d97706)',
                          cursor: 'pointer',
                          fontSize: 10,
                          padding: '0 4px',
                          borderRadius: 2,
                          fontFamily: 'inherit',
                          lineHeight: '16px',
                        }}
                      >
                        🎚 {colsWithNonDefaultPolicy} 空值策略 →
                      </button>
                    ) : (
                      <span style={{ color: 'var(--muted)' }}>🎚 0 空值策略</span>
                    )}
                    {colsWithValidation > 0 ? (
                      <button
                        type="button"
                        onClick={() => {
                          setBatchSel(new Set<number>(valIdxs));
                          setBatchAnchor(valIdxs[0]);
                        }}
                        title={`点击选中这 ${colsWithValidation} 列（已配置列级校验规则：required / regex / min-max / enum）`}
                        style={{
                          background: 'transparent',
                          border: '1px solid var(--accent, #3b82f6)',
                          color: 'var(--accent, #3b82f6)',
                          cursor: 'pointer',
                          fontSize: 10,
                          padding: '0 4px',
                          borderRadius: 2,
                          fontFamily: 'inherit',
                          lineHeight: '16px',
                        }}
                      >
                        🛡 {colsWithValidation} 校验 →
                      </button>
                    ) : (
                      <span style={{ color: 'var(--muted)' }}>🛡 0 校验</span>
                    )}
                    {shadowedCols > 0 && (
                      <button
                        type="button"
                        onClick={() => {
                          setBatchSel(new Set<number>(dupAllIdxs));
                          setBatchAnchor(dupAllIdxs[0]);
                        }}
                        title={`点击选中这 ${dupAllIdxs.length} 列（多列映射到同一目标列，除首列外都会被覆盖写入，建议只映射一次）`}
                        style={{
                          background: 'transparent',
                          border: '1px solid var(--danger, #dc2626)',
                          color: 'var(--danger, #dc2626)',
                          cursor: 'pointer',
                          fontSize: 10,
                          padding: '0 4px',
                          borderRadius: 2,
                          fontFamily: 'inherit',
                          lineHeight: '16px',
                        }}
                      >
                        🔁 {shadowedCols} 重复 →
                      </button>
                    )}
                    <span style={{ color: 'var(--muted)' }}>·</span>
                    {colsWithEmpty > 0 ? (
                      <button
                        type="button"
                        onClick={() => {
                          setBatchSel(new Set<number>(emptyIdxs));
                          setBatchAnchor(emptyIdxs[0]);
                        }}
                        title={`点击选中这 ${colsWithEmpty} 列（前 ${cap} 行中存在空值）`}
                        style={{
                          background: 'transparent',
                          border: `1px solid ${totalEmpty / cap > 0.3 ? 'var(--warn, #d97706)' : 'var(--text)'}`,
                          color: totalEmpty / cap > 0.3 ? 'var(--warn, #d97706)' : 'var(--text)',
                          cursor: 'pointer',
                          fontSize: 10,
                          padding: '0 4px',
                          borderRadius: 2,
                          fontFamily: 'inherit',
                          lineHeight: '16px',
                        }}
                      >
                        📊 {colsWithEmpty} 列有 {fmt(totalEmpty)} 空 ({emptyPct.toFixed(1)}%) →
                      </button>
                    ) : (
                      <span style={{ color: 'var(--success, #22c55e)' }}>✅ 0 空</span>
                    )}
                    {colsWithViolations > 0 ? (
                      <button
                        type="button"
                        onClick={() => {
                          setBatchSel(new Set<number>(violIdxs));
                          setBatchAnchor(violIdxs[0]);
                        }}
                        title={`点击选中这 ${colsWithViolations} 列（前 ${cap} 行中存在校验违规）`}
                        style={{
                          background: 'transparent',
                          border: '1px solid var(--danger, #dc2626)',
                          color: 'var(--danger, #dc2626)',
                          cursor: 'pointer',
                          fontSize: 10,
                          padding: '0 4px',
                          borderRadius: 2,
                          fontFamily: 'inherit',
                          lineHeight: '16px',
                        }}
                      >
                        ⚠️ {colsWithViolations} 列有 {fmt(totalViolations)} 违规 ({violPct.toFixed(1)}%) →
                      </button>
                    ) : (
                      <span style={{ color: 'var(--success, #22c55e)' }}>✅ 0 违规</span>
                    )}
                    {qualityGate.enabled && qualityReport && (() => {
                      // 用 qc.score 与 Step 4 门禁一致：Step 4 gate 用 overallScore = mean(qc.score)
                      // columnHealth 是不同口径（deduction 分），此处刻意不用
                      const below: number[] = [];
                      for (let i = 0; i < qualityReport.columns.length; i++) {
                        if (qualityReport.columns[i].score < qualityGate.threshold) below.push(i);
                      }
                      if (below.length === 0) {
                        return <span style={{ color: 'var(--success, #22c55e)' }} title={`所有列 qc.score ≥ 门禁阈值 ${qualityGate.threshold}，Step 4 整体质量评分大概率 ≥ 阈值`}>🚦 门禁 {qualityGate.threshold} ✅</span>;
                      }
                      return (
                        <button
                          type="button"
                          onClick={() => {
                            setBatchSel(new Set<number>(below));
                            setBatchAnchor(below[0]);
                          }}
                          title={`点击选中这 ${below.length} 列（qc.score < ${qualityGate.threshold}，Step 4 门禁会拉低整体评分）`}
                          style={{
                            background: 'transparent',
                            border: '1px solid var(--danger, #dc2626)',
                            color: 'var(--danger, #dc2626)',
                            cursor: 'pointer',
                            fontSize: 10,
                            padding: '0 4px',
                            borderRadius: 2,
                            fontFamily: 'inherit',
                            lineHeight: '16px',
                          }}
                        >
                          🚦 门禁 {qualityGate.threshold} · {below.length} 低分列 →
                        </button>
                      );
                    })()}
                  </div>
                );
              })()}
              <div style={styles.mapGrid}>
                {(() => {
                  // 按过滤器 + 搜索词重排显示顺序：匹配项在前，非匹配项下沉且 opacity 0.4
                  // 数据索引 i 保持不变（Record 键），仅影响视觉顺序
                  const items = mappings.map((m, i) => ({ m, i }));
                  const matched = (m: Mapping, i: number) => {
                    if (mappingFilter === 'unmapped') return m.targetColumn == null;
                    if (mappingFilter === 'mapped') return m.targetColumn != null;
                    if (mappingFilter === 'low-health') {
                      // 低健康档：columnHealths.health < 80（M30.57 4 档阈值的前两档）
                      const ch = columnHealths?.[i];
                      return ch != null && ch.health < 80;
                    }
                    if (mappingFilter === 'critical-health') {
                      // 紧急档：health < 60（amber+red，最严重的两档）
                      const ch = columnHealths?.[i];
                      return ch != null && ch.health < 60;
                    }
                    return true;
                  };
                  // 搜索命中：CSV 列名 或 目标列名 子串（不区分大小写）
                  const q = mappingSearch.trim().toLowerCase();
                  const searchMatch = (m: Mapping) => {
                    if (!q) return true;
                    if (m.csvName.toLowerCase().includes(q)) return true;
                    if (m.targetColumn && m.targetColumn.toLowerCase().includes(q)) return true;
                    return false;
                  };
                  let sorted = items;
                  // M30.81 按映射行排序（默认按 CSV 索引；stable sort 保等值原顺序）
                  if (mappingSort !== 'index') {
                    sorted = [...items].sort((a, b) => {
                      if (mappingSort === 'health-asc') {
                        const ha = columnHealths?.[a.i]?.health ?? 100;
                        const hb = columnHealths?.[b.i]?.health ?? 100;
                        return ha - hb;
                      }
                      if (mappingSort === 'empty-desc') {
                        const ea = profiles[a.i]?.nullCount ?? 0;
                        const eb = profiles[b.i]?.nullCount ?? 0;
                        return eb - ea;
                      }
                      if (mappingSort === 'target-asc') {
                        const ta = a.m.targetColumn ?? '\uffffunmapped';
                        const tb = b.m.targetColumn ?? '\uffffunmapped';
                        return ta.localeCompare(tb);
                      }
                      if (mappingSort === 'csv-asc') {
                        return a.m.csvName.toLowerCase().localeCompare(b.m.csvName.toLowerCase());
                      }
                      return 0;
                    });
                  }
                  if (mappingFilter !== 'all') {
                    sorted = [...sorted.filter((x) => matched(x.m, x.i)), ...sorted.filter((x) => !matched(x.m, x.i))];
                  }
                  if (q) {
                    sorted = [...sorted.filter((x) => searchMatch(x.m)), ...sorted.filter((x) => !searchMatch(x.m))];
                  }
                  // 命中搜索的集合，用于高亮行
                  const searchHits = new Set<number>();
                  if (q) for (const x of items) if (searchMatch(x.m)) searchHits.add(x.i);
                  // 重复映射检测：多列指向同一 targetColumn 时后续列会覆盖前面列的写入
                  const targetToIndices = new Map<string, number[]>();
                  for (let i = 0; i < mappings.length; i++) {
                    const t = mappings[i].targetColumn;
                    if (!t) continue;
                    const arr = targetToIndices.get(t);
                    if (arr) arr.push(i);
                    else targetToIndices.set(t, [i]);
                  }
                  const dupTargets = new Map<string, number[]>();
                  for (const [t, idxs] of targetToIndices) {
                    if (idxs.length > 1) dupTargets.set(t, idxs);
                  }
                  return sorted.map(({ m, i }) => {
                  const p = profiles[i];
                  const targetCol = m.targetColumn ? colInfoMap.get(m.targetColumn) : undefined;
                  const warn = warnFor(i);
                  const qc = qualityReport?.columns[i];
                  const nonNull = p ? p.total - p.nullCount : 0;
                  const suspiciousCount = qc?.suspiciousCount ?? 0;
                  const hasTransform = !!opts.transforms?.[i] && opts.transforms[i] !== 'none';
                  const dupIdxs = m.targetColumn ? dupTargets.get(m.targetColumn) : undefined;
                  const isDupShadowed = !!dupIdxs && i !== dupIdxs[dupIdxs.length - 1];
                  const dupLastIdx = dupIdxs ? dupIdxs[dupIdxs.length - 1] : -1;
                  const impactItems: Array<{ key: string; text: string; color: string; title: string }> = [];
                  if (isDupShadowed) {
                    impactItems.push({
                      key: 'dup-shadowed',
                      text: '🔁 将被覆盖',
                      color: 'var(--danger, #dc2626)',
                      title: `本列与 CSV 列 ${dupIdxs!.map((d) => '#' + (d + 1)).join('、')} 都映射到 ${m.targetColumn}。写入顺序按 CSV 列索引递增，#${dupLastIdx + 1} 的写入值会覆盖前面的所有值（含本列）。建议只映射一次；若需保留本列的值，可将其调整到最靠后的 CSV 列位置。`,
                    });
                  }
                  if (p && p.nullCount > 0) {
                    const pct = p.total > 0 ? Math.round((p.nullCount / p.total) * 100) : 0;
                    impactItems.push({ key: 'null', text: `∅${p.nullCount} (${pct}%)`, color: 'var(--muted)', title: `CSV 空值 ${p.nullCount}/${p.total}` });
                  }
                  if (suspiciousCount > 0 && qc) {
                    impactItems.push({ key: 'susp', text: `?${suspiciousCount}`, color: 'var(--warn, #d97706)', title: `可疑值 ${suspiciousCount}：前导0=${qc.suspiciousBreakdown.leadingZero} 多余空格=${qc.suspiciousBreakdown.extraSpaces} 尾引号=${qc.suspiciousBreakdown.trailingQuote} 孤立破折号=${qc.suspiciousBreakdown.dashOnly}` });
                  }
                  if (qc && qc.typePurity < 0.9 && qc.nonNull > 0) {
                    const pct = Math.round(qc.typePurity * 100);
                    impactItems.push({ key: 'pure', text: `T${pct}%`, color: 'var(--warn, #d97706)', title: `类型纯净度 ${pct}%（主导类型 ${qc.dominantType} 占非空值比例）` });
                  }
                  if (hasTransform) {
                    impactItems.push({ key: 'trf', text: `⚙${opts.transforms?.[i]}`, color: 'var(--accent, #3b82f6)', title: `已配置转换：${opts.transforms?.[i]}（将应用于 ${nonNull} 个非空值）` });
                  }
                  // 综合健康分 0-100：聚合 M30.52-56 的所有信号
                  let health = 100;
                  if (!m.targetColumn) health -= 40;
                  if (isDupShadowed) health -= 25;
                  const v = opts.validations?.[i];
                  const vCfg = !!(v && hasAnyValidation(v));
                  if (p && p.nullCount > 0 && !vCfg) health -= Math.min(15, Math.round((p.nullCount / Math.max(1, p.total)) * 15));
                  if (qc && qc.suspiciousCount > 0 && qc.nonNull > 0) health -= Math.min(15, Math.round((qc.suspiciousCount / qc.nonNull) * 100 * 0.15));
                  if (qc && qc.typePurity < 0.9 && qc.nonNull > 0) health -= Math.round((0.9 - qc.typePurity) * 50);
                  if (warn) health -= 10;
                  health = Math.max(0, health);
                  const healthColor = health >= 80 ? 'var(--success, #22c55e)'
                    : health >= 60 ? 'var(--warn, #d97706)'
                    : health >= 40 ? '#f59e0b'
                    : 'var(--danger, #dc2626)';
                  const isSelected = batchSel.has(i);
                  const onRowClick = (e: React.MouseEvent) => {
                    if (e.shiftKey && batchAnchor !== null) {
                      const lo = Math.min(batchAnchor, i);
                      const hi = Math.max(batchAnchor, i);
                      setBatchSel(new Set<number>(Array.from({length: hi - lo + 1}, (_, k) => lo + k)));
                    } else {
                      setBatchAnchor(i);
                      setBatchSel((prev) => {
                        const has = prev.has(i);
                        const next = new Set(prev);
                        if (has) next.delete(i); else next.add(i);
                        return next;
                      });
                    }
                  };
                  // 低健康分行底色：越差越红，方便用户一眼看到最需要处理的列
                  const healthBg = health >= 80
                    ? undefined
                    : health >= 60
                      ? 'rgba(217,119,6,0.05)'
                      : health >= 40
                        ? 'rgba(245,158,11,0.08)'
                        : 'rgba(220,38,38,0.10)';
                  const rowBg = isSelected ? 'rgba(59,130,246,0.10)' : healthBg;
                  // 搜索非命中项半透下沉（M30.72），命中项保持 1.0
                  const dimBySearch = q.length > 0 && !searchHits.has(i) ? 0.4 : 1;
                  // M30.77 行级过滤/搜索命中指示：仅在 mappingFilter 非 'all' 或有搜索时显示
                  const hasFilter = mappingFilter !== 'all' || q.length > 0;
                  const filterHit = hasFilter ? matched(m, i) : false;
                  const searchHit = q.length > 0 ? searchMatch(m) : false;
                  let filterDotColor = '';
                  let filterDotTitle = '';
                  if (hasFilter) {
                    const inFilter = mappingFilter === 'all' || filterHit;
                    const inSearch = q.length === 0 || searchHit;
                    if (inFilter && inSearch) {
                      filterDotColor = 'var(--accent, #3b82f6)';
                      filterDotTitle = '✓ 命中当前过滤器与搜索';
                    } else if (inFilter || inSearch) {
                      filterDotColor = 'var(--warn, #d97706)';
                      filterDotTitle = `⚠ 部分命中：过滤器 ${inFilter ? '命中' : '排除'}，搜索 ${inSearch ? '命中' : '排除'}`;
                    } else {
                      filterDotColor = 'var(--danger, #dc2626)';
                      filterDotTitle = '✕ 被过滤器和搜索共同排除';
                    }
                  }
                  return (
                    <div key={i} data-map-row-idx={i} className={jumpFlashIdx === i ? 'map-row-jump-flash' : undefined} style={{ ...styles.mapRow, position: 'relative', background: rowBg, borderLeft: isSelected ? '3px solid var(--accent, #3b82f6)' : '3px solid transparent', padding: '0 0 0 6px', boxSizing: 'border-box', opacity: dimBySearch }}>
                      <div style={{ ...styles.mapCell, flexDirection: 'column', alignItems: 'flex-start', gap: 2, cursor: 'pointer' }} onClick={onRowClick} title="点击选中 / Shift+Click 拖选多行，用于批量设置转换/空值策略">
                        <div style={{ display: 'flex', gap: 6, alignItems: 'center', width: '100%' }}>
                          {filterDotColor && (
                            <span
                              title={filterDotTitle}
                              style={{
                                display: 'inline-block', width: 6, height: 6, borderRadius: '50%',
                                background: filterDotColor,
                                flexShrink: 0,
                              }}
                            />
                          )}
                          <span style={styles.muted}>{m.csvIndex + 1}.</span>
                          <span style={{ fontFamily: 'monospace' }}>{highlightQuery(m.csvName)}</span>
                          {p && <span style={{ ...styles.typeBadge, color: typeBadgeColor(p.type) }}>{p.type}</span>}
                          <span style={{ flex: 1 }} />
                          {(() => {
                            const autoFixSuggestions = (health < 80 && qc) ? suggestTransforms(qc) : [];
                            const canAutoFix = autoFixSuggestions.length > 0;
                            const firstS = autoFixSuggestions[0];
                            return (
                            <span
                              style={{
                                fontSize: 9,
                                fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                color: healthColor,
                                fontWeight: 700,
                                padding: '0 4px',
                                borderRadius: 2,
                                background: 'rgba(255,255,255,0.05)',
                                minWidth: 26,
                                textAlign: 'center',
                                cursor: canAutoFix ? 'pointer' : 'default',
                                ...(canAutoFix ? { border: '1px solid currentColor' } : {}),
                              }}
                              title={canAutoFix
                                ? `综合健康分：${health}/100 · 点击应用建议「${firstS!.transform}」（${firstS!.reason}），共 ${autoFixSuggestions.length} 条建议取首条保守策略。≥80 绿 · 60-79 橙 · 40-59 黄 · <40 红`
                                : `综合健康分：${health}/100 · 基于空值率、类型纯净度、可疑值、校验配置、目标映射、覆盖冲突等信号综合计算。≥80 绿 · 60-79 橙 · 40-59 黄 · <40 红`}
                              onClick={(e) => {
                                if (!canAutoFix) return;
                                e.stopPropagation();
                                const s = autoFixSuggestions[0]!;
                                const next = { ...opts.transforms };
                                next[i] = s.transform;
                                const nextParams = { ...opts.transformParams };
                                if (s.transform === 'regex-replace') {
                                  nextParams[i] = {
                                    pattern: s.pattern,
                                    replacement: s.replacement,
                                    flags: s.flags ?? 'g',
                                  };
                                }
                                setOpts({ ...opts, transforms: next, transformParams: nextParams });
                              }}
                            >
                              {health}
                            </span>
                            );
                          })()}
                        </div>
                        {impactItems.length > 0 && (
                          <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', fontSize: 9 }}>
                            {impactItems.map((it) => (
                              <span
                                key={it.key}
                                title={it.title}
                                style={{
                                  padding: '0 4px', borderRadius: 2,
                                  background: 'rgba(255,255,255,0.05)',
                                  color: it.color,
                                  fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                  fontWeight: 600,
                                }}
                              >{it.text}</span>
                            ))}
                          </div>
                        )}
                        {warn && <div style={styles.warnText} title={warn}>⚠ {warn}</div>}
                      </div>
                      <span style={styles.arrow}>→</span>
                      <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                        <select
                          value={m.targetColumn ?? ''}
                          onChange={(e) => {
                            const v = e.target.value || null;
                            const next = mappings.slice();
                            next[i] = { ...next[i], targetColumn: v };
                            setMappings(next);
                          }}
                          style={{ ...styles.selectSm, ...(warn ? styles.selectWarn : {}) }}
                        >
                          <option value="">— 跳过 —</option>
                          {cols.map((c) => (
                            <option key={c.name} value={c.name} disabled={c.is_auto_increment}>
                              {c.name}{c.is_primary_key ? ' (PK)' : ''}{c.is_auto_increment ? ' (自增)' : ''} · {c.data_type}
                            </option>
                          ))}
                        </select>
                        {targetCol && (() => {
                          const badges: Array<{ text: string; color: string; bg: string; title: string }> = [];
                          badges.push({ text: targetCol.data_type, color: 'var(--text)', bg: 'rgba(255,255,255,0.05)', title: `目标类型：${targetCol.data_type}` });
                          if (targetCol.is_primary_key) badges.push({ text: 'PK', color: '#fff', bg: 'var(--danger, #dc2626)', title: '主键列（唯一标识一行；update/upsert 模式用作 WHERE 条件）' });
                          if (targetCol.is_auto_increment) badges.push({ text: '自增', color: '#fff', bg: 'var(--accent, #3b82f6)', title: '自增列（一般无需在 CSV 中提供值）' });
                          if (!targetCol.nullable) badges.push({ text: 'NOT NULL', color: '#fff', bg: 'var(--warn, #d97706)', title: '非空列（CSV 空值会触发校验警告）' });
                          if (targetCol.default_value && String(targetCol.default_value).length > 0) {
                            const dv = String(targetCol.default_value);
                            badges.push({ text: `默认 ${dv.length > 12 ? dv.slice(0, 12) + '…' : dv}`, color: 'var(--muted)', bg: 'rgba(255,255,255,0.03)', title: `默认值：${dv}` });
                          }
                          return (
                            <div style={{ display: 'flex', gap: 3, flexWrap: 'wrap', fontSize: 10 }}>
                              {badges.map((b, bi) => (
                                <span
                                  key={bi}
                                  title={b.title}
                                  style={{
                                    padding: '1px 5px', borderRadius: 2,
                                    background: b.bg, color: b.color,
                                    fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                    fontWeight: 600, whiteSpace: 'nowrap',
                                  }}
                                >{b.text}</span>
                              ))}
                            </div>
                          );
                        })()}
                        <select
                          value={opts.transforms?.[i] ?? 'none'}
                          onChange={(e) => {
                            const v = e.target.value as ColumnTransform;
                            const next = { ...opts.transforms };
                            if (v === 'none') delete next[i];
                            else next[i] = v;
                            setOpts({ ...opts, transforms: next });
                          }}
                          style={{ ...styles.selectSm, marginTop: 2 }}
                          title="对该 CSV 列应用的字符串转换"
                        >
                          <option value="none">— 无转换 —</option>
                          <option value="trim">Trim 去首尾空格</option>
                          <option value="lower">Lower 转小写</option>
                          <option value="upper">Upper 转大写</option>
                          <option value="null-if-empty">Null if empty 空当 NULL</option>
                          <option value="strip-zero-padding">Strip 0-padding</option>
                          <option value="regex-replace">Regex Replace…</option>
                          <option value="date-parse-iso">Date → ISO 8601</option>
                          <option value="date-parse-us">Date US (MM/dd/yyyy) → ISO</option>
                          <option value="date-parse-ymd">Date YMD (yyyy-MM-dd) → ISO</option>
                          <option value="scale-x100">Scale ×100（元→分）</option>
                          <option value="scale-div100">Scale ÷100（分→元）</option>
                        </select>
                        {opts.transforms?.[i] === 'regex-replace' && (
                          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 4, marginTop: 2 }}>
                            <input
                              type="text"
                              value={opts.transformParams?.[i]?.pattern ?? ''}
                              onChange={(e) => {
                                const params = { ...opts.transformParams };
                                params[i] = { ...params[i], pattern: e.target.value };
                                setOpts({ ...opts, transformParams: params });
                              }}
                              style={{ ...styles.inputSm, fontFamily: 'monospace', fontSize: 11 }}
                              placeholder="正则 /regex/"
                            />
                            <input
                              type="text"
                              value={opts.transformParams?.[i]?.replacement ?? ''}
                              onChange={(e) => {
                                const params = { ...opts.transformParams };
                                params[i] = { ...params[i], replacement: e.target.value };
                                setOpts({ ...opts, transformParams: params });
                              }}
                              style={{ ...styles.inputSm, fontFamily: 'monospace', fontSize: 11 }}
                              placeholder="替换为"
                            />
                          </div>
                        )}
                        {hasTransform && parse && (() => {
                          const t = opts.transforms?.[i];
                          const params = opts.transformParams?.[i];
                          const samples: Array<{ raw: string; out: string | null }> = [];
                          for (let r = 0; r < parse.rows.length && samples.length < 3; r++) {
                            const raw = parse.rows[r]?.[i];
                            if (raw == null || raw === '') continue;
                            const s = String(raw);
                            if (s === '') continue;
                            samples.push({ raw: s, out: transformOnly(s, t, params) });
                          }
                          if (samples.length === 0) return null;
                          return (
                            <div
                              style={{
                                marginTop: 3,
                                padding: '3px 6px',
                                borderRadius: 2,
                                background: 'rgba(59, 130, 246, 0.06)',
                                border: '1px solid rgba(59, 130, 246, 0.15)',
                                fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                fontSize: 10,
                                display: 'flex',
                                flexDirection: 'column',
                                gap: 1,
                              }}
                              title={`显示该列前 ${samples.length} 个非空 CSV 值经转换后的结果，用于确认转换规则符合预期`}
                            >
                              {samples.map((s, si) => {
                                const disp = (x: string | null, max: number) => {
                                  const t = x ?? 'null';
                                  return t.length > max ? t.slice(0, max) + '…' : t;
                                };
                                const changed = (s.out ?? 'null') !== s.raw;
                                return (
                                  <div key={si} style={{ display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'wrap' }}>
                                    <span style={{ color: 'var(--muted)', flexShrink: 0 }} title={s.raw}>
                                      {disp(s.raw, 24)}
                                    </span>
                                    <span style={{ color: changed ? 'var(--accent, #3b82f6)' : 'var(--muted)', flexShrink: 0 }}>
                                      {changed ? '→' : '='}
                                    </span>
                                    <span
                                      style={{
                                        color: s.out == null ? 'var(--muted)' : changed ? 'var(--text)' : 'var(--muted)',
                                        fontStyle: s.out == null ? 'italic' : 'normal',
                                        flexShrink: 0,
                                      }}
                                      title={s.out ?? 'null（空值/空字符串将被视为 null）'}
                                    >
                                      {disp(s.out, 24)}
                                    </span>
                                  </div>
                                );
                              })}
                            </div>
                          );
                        })()}
                        <select
                          value={opts.nullPolicies?.[i] ?? 'null'}
                          onChange={(e) => {
                            const v = e.target.value as NullPolicy;
                            const next = { ...opts.nullPolicies };
                            if (v === 'null') delete next[i];
                            else next[i] = v;
                            setOpts({ ...opts, nullPolicies: next });
                          }}
                          style={{ ...styles.selectSm, marginTop: 2 }}
                          title="该 CSV 列为空时如何处理（默认跟随全局「空值转 NULL」开关）"
                        >
                          <option value="null">— 空值 → NULL（默认）—</option>
                          <option value="empty-string">空值 → 空字符串 ''</option>
                          <option value="skip-row">空值 → 跳过整行</option>
                          <option value="use-default" disabled={targetCol?.default_value == null || targetCol.default_value === ''}>
                            空值 → 列默认值{targetCol?.default_value ? ` (${targetCol.default_value})` : '（目标列无默认值）'}
                          </option>
                        </select>
                        {parse && (() => {
                          const policy = opts.nullPolicies?.[i] ?? 'null';
                          const total = parse.rows.length;
                          if (total === 0) return null;
                          const cap = Math.min(total, 100000);
                          let emptyCount = 0;
                          for (let r = 0; r < cap; r++) {
                            const raw = parse.rows[r]?.[i];
                            if (raw == null || String(raw) === '') emptyCount++;
                          }
                          if (emptyCount === 0) return null;
                          const pct = (emptyCount * 100) / cap;
                          const capped = total > cap;
                          const countLabel = capped ? `≥${emptyCount}` : String(emptyCount);
                          const color =
                            policy === 'skip-row' ? 'var(--danger, #dc2626)'
                            : policy === 'use-default' ? 'var(--warn, #d97706)'
                            : policy === 'empty-string' ? 'var(--accent, #3b82f6)'
                            : 'var(--muted)';
                          const effect =
                            policy === 'skip-row' ? '将跳过整行'
                            : policy === 'use-default' ? '将用默认值填充'
                            : policy === 'empty-string' ? '将填空字符串'
                            : '将置为 NULL';
                          const cappedNote = capped ? '（前 10 万行抽样）' : '';
                          return (
                            <div
                              style={{
                                marginTop: 2,
                                padding: '2px 6px',
                                borderRadius: 2,
                                background: 'rgba(255,255,255,0.03)',
                                borderLeft: `2px solid ${color}`,
                                fontSize: 10,
                                color: color,
                                fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                              }}
                              title={`该 CSV 列在 ${total} 行中有 ${emptyCount} 行为空（${pct.toFixed(1)}%）${cappedNote}，当前策略「${policy}」的影响：${effect}`}
                            >
                              📊 {countLabel} / {total} 空 ({pct.toFixed(1)}%) · {effect}
                            </div>
                          );
                        })()}
                        <details style={{ marginTop: 2 }} open={hasAnyValidation(opts.validations?.[i])}>
                          <summary style={{ fontSize: 10, color: 'var(--muted)', cursor: 'pointer' }}>
                            🛡 校验规则 {hasAnyValidation(opts.validations?.[i]) ? '·已配置' : '·未配置'}
                          </summary>
                          {(() => {
                            const v = opts.validations?.[i] ?? EMPTY_VALIDATION;
                            const update = (patch: Partial<ColumnValidation>) => {
                              const next = { ...opts.validations };
                              const merged = { ...EMPTY_VALIDATION, ...v, ...patch };
                              if (!hasAnyValidation(merged)) delete next[i];
                              else next[i] = merged;
                              setOpts({ ...opts, validations: next });
                            };
                            return (
                              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 4, marginTop: 4 }}>
                                <label style={{ ...styles.check, fontSize: 11 }}>
                                  <input
                                    type="checkbox"
                                    checked={v.required}
                                    onChange={(e) => update({ required: e.target.checked })}
                                  />
                                  <span>必填</span>
                                </label>
                                <div />
                                <input
                                  type="text"
                                  value={v.regex}
                                  onChange={(e) => update({ regex: e.target.value })}
                                  style={{ ...styles.inputSm, fontSize: 10, fontFamily: 'monospace' }}
                                  placeholder="正则 /.../"
                                />
                                <input
                                  type="text"
                                  value={v.enum}
                                  onChange={(e) => update({ enum: e.target.value })}
                                  style={{ ...styles.inputSm, fontSize: 10 }}
                                  placeholder="枚举 a,b,c"
                                />
                                <input
                                  type="number"
                                  value={v.minValue}
                                  onChange={(e) => update({ minValue: e.target.value })}
                                  style={{ ...styles.inputSm, fontSize: 10 }}
                                  placeholder="最小值"
                                />
                                <input
                                  type="number"
                                  value={v.maxValue}
                                  onChange={(e) => update({ maxValue: e.target.value })}
                                  style={{ ...styles.inputSm, fontSize: 10 }}
                                  placeholder="最大值"
                                />
                              </div>
                            );
                          })()}
                        </details>
                        {parse && opts.validations?.[i] && (() => {
                          const v = opts.validations[i];
                          if (!hasAnyValidation(v)) return null;
                          const total = parse.rows.length;
                          if (total === 0) return null;
                          const cap = Math.min(total, 100000);
                          let violations = 0;
                          const samples: Array<{ row: number; raw: string; reason: string }> = [];
                          for (let r = 0; r < cap; r++) {
                            const cell = parse.rows[r]?.[i];
                            const raw = cell == null ? '' : String(cell);
                            const reason = validateCell(raw, v);
                            if (reason) {
                              violations++;
                              if (samples.length < 3) {
                                const dispRaw = raw.length > 24 ? raw.slice(0, 24) + '…' : raw;
                                samples.push({ row: r + 1, raw: dispRaw, reason });
                              }
                            }
                          }
                          if (violations === 0) {
                            return (
                              <div
                                style={{
                                  marginTop: 2,
                                  padding: '2px 6px',
                                  borderRadius: 2,
                                  background: 'rgba(34,197,94,0.06)',
                                  borderLeft: '2px solid var(--success, #22c55e)',
                                  fontSize: 10,
                                  color: 'var(--success, #22c55e)',
                                  fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                }}
                                title={`所有 ${cap} 行都通过当前校验规则，可提交`}
                              >
                                ✅ 0 / {cap} 违规
                              </div>
                            );
                          }
                          const capped = total > cap;
                          const pct = (violations * 100) / cap;
                          const countLabel = capped ? `≥${violations}` : String(violations);
                          return (
                            <div
                              style={{
                                marginTop: 2,
                                padding: '3px 6px',
                                borderRadius: 2,
                                background: 'rgba(220,38,38,0.06)',
                                borderLeft: '2px solid var(--danger, #dc2626)',
                                fontSize: 10,
                                color: 'var(--danger, #dc2626)',
                                fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                display: 'flex',
                                flexDirection: 'column',
                                gap: 1,
                              }}
                              title={`当前校验规则在 ${cap} 行中发现 ${violations} 处违规（${pct.toFixed(1)}%）${capped ? '（前 10 万行抽样）' : ''}。开启「严格校验」时这些行会跳过导入。`}
                            >
                              <div>⚠️ {countLabel} / {cap} 违规 ({pct.toFixed(1)}%)</div>
                              {samples.map((s, si) => (
                                <div key={si} style={{ opacity: 0.85, color: 'var(--text)' }}>
                                  L{s.row}: <span style={{ opacity: 0.7 }}>「{s.raw}」</span> → {s.reason}
                                </div>
                              ))}
                            </div>
                          );
                        })()}
                      </div>
                    </div>
                  );
                  });
                })()}
              </div>
              {batchSel.size >= 1 && (() => {
                // 已选列在 "已选 N 行" 后面追加 CSV 名称（前 3 显全名，>3 截断 +N）
                // M30.61-70 各 chip 点击后都会调用 setBatchSel，用户需要立即看到选中了哪些列
                const sortedIdxs = [...batchSel].sort((a, b) => a - b);
                const showMax = 3;
                const names = sortedIdxs.slice(0, showMax).map((i) => mappings[i]?.csvName ?? `#${i}`);
                const extra = sortedIdxs.length - showMax;
                const namesText = extra > 0 ? `${names.join('、')} … +${extra}` : names.join('、');
                const healthsOfSelected = sortedIdxs
                  .map((i) => columnHealths?.[i]?.health)
                  .filter((h): h is number => h != null);
                const avgHealth = healthsOfSelected.length > 0
                  ? Math.round(healthsOfSelected.reduce((s, h) => s + h, 0) / healthsOfSelected.length)
                  : null;
                return (
                <div style={{
                  display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
                  padding: '6px 10px', marginTop: 4,
                  background: 'rgba(59,130,246,0.08)',
                  border: '1px solid var(--accent, #3b82f6)',
                  borderLeft: '3px solid var(--accent, #3b82f6)',
                  borderRadius: 4, fontSize: 11,
                }}>
                  <span style={{ color: 'var(--accent, #3b82f6)', fontWeight: 600 }} title={`已选中：${sortedIdxs.map(i => mappings[i]?.csvName ?? `#${i}`).join('、')}`}>
                    已选 {batchSel.size} 行
                    {avgHealth != null && (() => {
                      const c = avgHealth >= 80 ? 'var(--success, #22c55e)'
                        : avgHealth >= 60 ? 'var(--warn, #d97706)'
                        : avgHealth >= 40 ? '#f59e0b'
                        : 'var(--danger, #dc2626)';
                      return (
                        <span style={{ marginLeft: 6, padding: '0 4px', fontSize: 10, fontFamily: 'var(--mono, ui-monospace)', color: c, border: `1px solid ${c}`, borderRadius: 2, fontWeight: 700 }} title={`选中列平均健康分 ${avgHealth}/100`}>
                          ⚖ {avgHealth}
                        </span>
                      );
                    })()}
                  </span>
                  <span style={{ color: 'var(--muted)', fontSize: 10, fontFamily: 'var(--mono, ui-monospace)' }} title="选中列的 CSV 名称">
                    {namesText}
                  </span>
                  <span style={{ color: 'var(--muted)' }}>·</span>
                  <select
                    defaultValue=""
                    style={{ ...styles.selectSm, padding: '1px 4px' }}
                    title="对选中所有行统一设置字符串转换"
                    onChange={(e) => {
                      const v = e.target.value as ColumnTransform | '';
                      if (v === '') return;
                      const next = { ...opts.transforms };
                      for (const i of batchSel) {
                        if (v === 'none') delete next[i]; else next[i] = v;
                      }
                      setOpts({ ...opts, transforms: next });
                      e.target.value = '';
                    }}
                  >
                    <option value="" disabled>批量转换…</option>
                    <option value="none">— 无转换 —</option>
                    <option value="trim">Trim 去首尾空格</option>
                    <option value="lower">Lower 转小写</option>
                    <option value="upper">Upper 转大写</option>
                    <option value="null-if-empty">Null if empty 空当 NULL</option>
                    <option value="strip-zero-padding">Strip 0-padding</option>
                    <option value="date-parse-iso">Date → ISO 8601</option>
                    <option value="date-parse-us">Date US → ISO</option>
                    <option value="date-parse-ymd">Date YMD → ISO</option>
                    <option value="scale-x100">Scale ×100（元→分）</option>
                    <option value="scale-div100">Scale ÷100（分→元）</option>
                  </select>
                  <select
                    defaultValue=""
                    style={{ ...styles.selectSm, padding: '1px 4px' }}
                    title="对选中所有行统一设置空值策略"
                    onChange={(e) => {
                      const v = e.target.value as NullPolicy | '';
                      if (v === '') return;
                      const next = { ...opts.nullPolicies };
                      for (const i of batchSel) {
                        if (v === 'null') delete next[i]; else next[i] = v;
                      }
                      setOpts({ ...opts, nullPolicies: next });
                      e.target.value = '';
                    }}
                  >
                    <option value="" disabled>批量空值策略…</option>
                    <option value="null">空值 → NULL（默认）</option>
                    <option value="empty-string">空值 → 空字符串 ''</option>
                    <option value="skip-row">空值 → 跳过整行</option>
                    <option value="use-default">空值 → 列默认值</option>
                  </select>
                  <button
                    onClick={() => {
                      const nextT = { ...opts.transforms };
                      const nextV = { ...opts.validations };
                      const nextNP = { ...opts.nullPolicies };
                      for (const i of batchSel) { delete nextT[i]; delete nextV[i]; delete nextNP[i]; }
                      setOpts({ ...opts, transforms: nextT, validations: nextV, nullPolicies: nextNP });
                    }}
                    style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 11 }}
                    title="清除选中行的转换/空值策略/校验规则"
                  >🧹 清空设置</button>
                  <span style={{ flex: 1 }} />
                  {(() => {
                    // M30.79：批量应用建议——仅对选中列应用首条建议（health<80 且有 qualityReport）
                    const applicable: number[] = [];
                    for (const idx of batchSel) {
                      const ch = columnHealths?.[idx];
                      if (!ch || ch.health >= 80) continue;
                      const qc = qualityReport?.columns[idx];
                      if (!qc) continue;
                      if (suggestTransforms(qc).length === 0) continue;
                      applicable.push(idx);
                    }
                    if (applicable.length === 0) return null;
                    return (
                      <button
                        onClick={() => {
                          const nextT = { ...opts.transforms };
                          const nextParams = { ...opts.transformParams };
                          for (const idx of applicable) {
                            const qc = qualityReport?.columns[idx];
                            if (!qc) continue;
                            const s = suggestTransforms(qc)[0];
                            if (!s) continue;
                            nextT[idx] = s.transform;
                            if (s.transform === 'regex-replace') {
                              nextParams[idx] = {
                                pattern: s.pattern,
                                replacement: s.replacement,
                                flags: s.flags ?? 'g',
                              };
                            }
                          }
                          setOpts({ ...opts, transforms: nextT, transformParams: nextParams });
                          publishEditorStatus({
                            message: `⚡ 已对选中列中的 ${applicable.length} 列应用首条建议转换`,
                            messageAt: Date.now(),
                          });
                        }}
                        style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 11, color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)' }}
                        title="对选中列中的低健康分列各应用首条质量建议（trim/strip-zero-padding/regex-replace/date-parse-* 等，取首条保守策略）；会覆盖已有的转换设置"
                      >⚡ 应用建议（{applicable.length}）</button>
                    );
                  })()}
                  <button
                    onClick={() => {
                      const allIdx = new Set<number>(Array.from({ length: mappings.length }, (_, k) => k));
                      const next = new Set<number>();
                      for (const i of allIdx) if (!batchSel.has(i)) next.add(i);
                      setBatchSel(next);
                      setBatchAnchor(next.size > 0 ? Math.min(...next) : null);
                    }}
                    style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 11 }}
                    title="反选：切换选中集合（未选中的变成选中，已选中的变成未选中）"
                  >⇄ 反选</button>
                  <button
                    onClick={() => {
                      setBatchAnchor(null);
                      setBatchSel(new Set<number>());
                    }}
                    style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 11 }}
                    title="清除选择"
                  >取消选择</button>
                </div>
                );
              })()}
              {mappingSuggestions.length > 0 && (
                <div style={{ ...styles.mutedBox, borderLeft: '3px solid var(--ok, #10b981)', display: 'flex', flexDirection: 'column', gap: 4 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <span style={styles.label}>💡 智能映射建议</span>
                    <span style={{ fontSize: 11, color: 'var(--muted)' }}>
                      {mappingSuggestions.length} 条模糊匹配，未映射的 CSV 列可能对应以下目标列
                    </span>
                    <span style={styles.spacer} />
                    <button
                      onClick={() => {
                        const next = mappings.slice();
                        for (const s of mappingSuggestions) {
                          if (next[s.csvIndex]) next[s.csvIndex] = { ...next[s.csvIndex], targetColumn: s.targetColumn };
                        }
                        setMappings(next);
                      }}
                      style={{ ...styles.btnSm, fontSize: 11 }}
                    >全部接受</button>
                  </div>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 3, maxHeight: 160, overflow: 'auto' }}>
                    {mappingSuggestions.map((s) => (
                      <div key={s.csvIndex} style={{ display: 'grid', gridTemplateColumns: '1fr auto auto auto', gap: 8, alignItems: 'center', fontSize: 11, fontFamily: 'monospace', padding: '3px 6px', background: 'var(--panel)', borderRadius: 3, border: '1px solid var(--border)' }}>
                        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                          <span style={{ color: 'var(--muted)' }}>{s.csvIndex + 1}.</span> {s.csvName}
                          <span style={{ color: 'var(--accent)', margin: '0 4px' }}>→</span>
                          <strong>{s.targetColumn}</strong>
                        </span>
                        <span style={{ color: qualityScoreColor(Math.min(100, s.score)), fontSize: 10, fontWeight: 700 }} title="匹配分">{Math.round(s.score)}</span>
                        <span style={{ fontSize: 10, color: 'var(--muted)', maxWidth: 180, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={s.reason}>{s.reason}</span>
                        <button
                          onClick={() => {
                            const next = mappings.slice();
                            if (next[s.csvIndex]) next[s.csvIndex] = { ...next[s.csvIndex], targetColumn: s.targetColumn };
                            setMappings(next);
                          }}
                          style={{ ...styles.btnSm, fontSize: 10, padding: '2px 6px' }}
                        >接受</button>
                      </div>
                    ))}
                  </div>
                </div>
              )}
              <div style={{ ...styles.mutedBox, fontSize: 11 }}>
                <label style={{ ...styles.check }}>
                  <input
                    type="checkbox"
                    checked={opts.strictValidation}
                    onChange={(e) => setOpts({ ...opts, strictValidation: e.target.checked })}
                  />
                  <span>严格校验：违规行自动跳过（关闭时仅显示警告但继续导入）</span>
                </label>
                {validationResult && validationResult.violations.length > 0 && (
                  <div style={{ marginTop: 4, color: 'var(--warn, #d97706)' }}>
                    ⚠ {validationResult.violations.length} 行未通过校验
                    {opts.strictValidation && ' · 将被自动跳过'}
                    <details style={{ marginTop: 2 }}>
                      <summary style={{ cursor: 'pointer', fontSize: 10 }}>查看前 10 行违规</summary>
                      <pre style={{ ...styles.pre, marginTop: 4, maxHeight: 120 }}>
                        {validationResult.violations.slice(0, 10).map((r) =>
                          `第${r.csvRow}行: ${r.reasons.join('; ')}`
                        ).join('\n')}
                      </pre>
                    </details>
                  </div>
                )}
              </div>
              <div style={styles.mutedBox}>
                映射 {mappedCount} 列 · 跳过 {skippedCount} 列 · 目标 <code>{selSchema}.{selTable}</code>
                {(() => {
                  const warnCount = mappings.reduce((n, _m, i) => (warnFor(i) ? n + 1 : n), 0);
                  return warnCount > 0
                    ? <span style={{ color: 'var(--warn, #d97706)', marginLeft: 6 }}>· {warnCount} 处类型警告（可继续但可能失败）</span>
                    : null;
                })()}
                {presetApplied && (
                  <span style={{ color: 'var(--accent, #3b82f6)', marginLeft: 6 }}>
                    · 📌 已应用预设（{presetApplied.matched}/{presetApplied.total} 列匹配）
                    {presetApplied.removed.length > 0 && (
                      <span style={{ color: 'var(--warn, #d97706)' }}>· ⚠ {presetApplied.removed.length} 列目标已删除</span>
                    )}
                    {presetApplied.added.length > 0 && (
                      <span style={{ color: 'var(--muted)' }}>· 🆕 {presetApplied.added.length} 新列</span>
                    )}
                    <a
                      href="#"
                      onClick={(e) => {
                        e.preventDefault();
                        if (parseRef.current) setMappings(inferMapping(parseRef.current.columns, cols));
                        deletePreset(connId, selSchema, selTable);
                        setPresetApplied(null);
                        setAppliedPreset(null);
                        setPresetSnapshotsRefresh((n) => n + 1);
                      }}
                      style={{ marginLeft: 8, fontSize: 10, cursor: 'pointer' }}
                    >清除并重置</a>
                    {appliedPreset && (
                      <a
                        href="#"
                        onClick={(e) => {
                          e.preventDefault();
                          setDiffOpen(true);
                          // 复用 diff 面板，把 preset vs current 的字段差异塞进临时快照
                          const snapshot = JSON.parse(JSON.stringify(opts)) as ImportOptions;
                          const presetOpts: ImportOptions = {
                            ...snapshot,
                            mode: appliedPreset.mode,
                            transforms: appliedPreset.transforms ?? {},
                            transformParams: appliedPreset.transformParams ?? {},
                            emptyAsNull: appliedPreset.emptyAsNull,
                            batchSize: appliedPreset.batchSize,
                            skipFailed: appliedPreset.skipFailed,
                            filterColumn: appliedPreset.filterColumn,
                            filterOp: appliedPreset.filterOp,
                            filterValue: appliedPreset.filterValue,
                            validations: appliedPreset.validations ?? {},
                            strictValidation: appliedPreset.strictValidation,
                            nullPolicies: appliedPreset.nullPolicies ?? {},
                          };
                          const entry: FixEntry = {
                            at: Date.now(),
                            snapshot: presetOpts,
                            icon: '📌',
                            label: `预设版本（${new Date(appliedPreset.updatedAt).toLocaleTimeString('zh-CN')}）`,
                            key: 'preset',
                            msg: '当前配置与预设版本的字段级差异（用于查看，撤销将回到预设版本）',
                          };
                          setUndoStack((s) => [entry, ...s].slice(0, MAX_FIX_HISTORY));
                          setBatchStepN(1);
                          publishEditorStatus({
                            message: `🔍 已把「预设版本 vs 当前配置」推入 diff 面板（${new Date(appliedPreset.updatedAt).toLocaleTimeString('zh-CN')}）`,
                            messageAt: Date.now(),
                          });
                        }}
                        style={{ marginLeft: 6, fontSize: 10, cursor: 'pointer', color: 'var(--warn, #d97706)' }}
                        title="把当前应用的预设版本 vs 用户手动改动的当前配置 推入 diff 面板（不撤销，仅查看差异）"
                      >↔ 对比当前</a>
                    )}
                    {(() => {
                      const key = presetKey(connId, selSchema, selTable);
                      const snaps = listSnapshots(key);
                      if (snaps.length === 0) return null;
                      const fmtDate = (t: number) => {
                        const d = new Date(t);
                        const pad = (n: number) => String(n).padStart(2, '0');
                        return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
                      };
                      return (
                        <details style={{ display: 'inline-block', marginLeft: 6, verticalAlign: 'top' }}>
                          <summary style={{ cursor: 'pointer', fontSize: 10, userSelect: 'none', listStyle: 'none' }} title="预设版本历史（每次覆盖前保留旧版本，最多 5 个）">
                            🕘 历史 {snaps.length}
                          </summary>
                          <div
                            style={{
                              position: 'absolute', zIndex: 30, marginTop: 2, marginLeft: -4,
                              width: 320, maxHeight: 260, overflowY: 'auto',
                              background: 'var(--panel, #1e1e2e)', border: '1px solid var(--border, #444)',
                              borderRadius: 3, padding: 4,
                              boxShadow: '0 4px 12px rgba(0,0,0,0.4)', fontSize: 11,
                            }}
                          >
                            {snaps.map((s, i) => (
                              <div
                                key={`${s.updatedAt}-${i}`}
                                style={{
                                  display: 'flex', alignItems: 'center', gap: 6,
                                  padding: '3px 6px', marginBottom: 2,
                                  borderRadius: 2, background: 'rgba(255,255,255,0.03)',
                                }}
                              >
                                <span style={{ flex: 1, fontFamily: 'monospace', fontSize: 10, color: 'var(--muted)' }}>
                                  {s.mode} · {s.mappings.length} 列 · {fmtDate(s.updatedAt)}
                                </span>
                                <a
                                  href="#"
                                  onClick={(e) => {
                                    e.preventDefault();
                                    restoreSnapshot(key, i);
                                    const r = applyPreset(s, cols);
                                    setMappings(r.mappings);
                                    setOpts((o) => ({
                                      ...o,
                                      mode: r.mode,
                                      transforms: r.transforms,
                                      transformParams: r.transformParams,
                                      emptyAsNull: r.opts.emptyAsNull,
                                      batchSize: r.opts.batchSize,
                                      skipFailed: r.opts.skipFailed,
                                      filterColumn: r.opts.filterColumn,
                                      filterOp: r.opts.filterOp,
                                      filterValue: r.opts.filterValue,
                                      validations: r.opts.validations,
                                      strictValidation: r.opts.strictValidation,
                                      nullPolicies: r.opts.nullPolicies,
                                    }));
                                    setPresetApplied({
                                      matched: r.matchedCols,
                                      total: r.totalCols,
                                      removed: r.removedTargets.map((t) => ({ csvName: t.csvName, targetColumn: t.targetColumn })),
                                      added: r.addedTargets,
                                    });
                                    setPresetSnapshotsRefresh((n) => n + 1);
                                  }}
                                  style={{ color: 'var(--accent, #3b82f6)', fontSize: 10, cursor: 'pointer', whiteSpace: 'nowrap' }}
                                >恢复</a>
                              </div>
                            ))}
                          </div>
                        </details>
                      );
                    })()}
                  </span>
                )}
                {presetApplied && (presetApplied.removed.length > 0 || presetApplied.added.length > 0) && (
                  <details style={{ marginTop: 4 }} open={presetApplied.removed.length > 0}>
                    <summary style={{ cursor: 'pointer', fontSize: 11, color: 'var(--muted)' }}>
                      📋 预设与当前表结构差异（点击展开）
                    </summary>
                    <div style={{ marginTop: 4, fontSize: 11 }}>
                      {presetApplied.removed.length > 0 && (
                        <div style={{ color: 'var(--warn, #d97706)', marginBottom: 4 }}>
                          <div style={{ fontWeight: 600, marginBottom: 2 }}>⚠ 以下预设目标列在目标表中已不存在（这些 CSV 列已置为「跳过」，请重新映射或忽略）：</div>
                          {presetApplied.removed.slice(0, 10).map((r, i) => (
                            <div key={i} style={{ fontFamily: 'monospace', paddingLeft: 12 }}>
                              CSV <code>{r.csvName}</code> → 目标 <code>{r.targetColumn}</code> 已消失
                            </div>
                          ))}
                          {presetApplied.removed.length > 10 && (
                            <div style={{ paddingLeft: 12 }}>… 共 {presetApplied.removed.length} 列</div>
                          )}
                        </div>
                      )}
                      {presetApplied.added.length > 0 && (
                        <div style={{ color: 'var(--muted)' }}>
                          <div style={{ fontWeight: 600, marginBottom: 2 }}>🆕 目标表新增列（预设里没见过，如需映射请手动选择）：</div>
                          <div style={{ fontFamily: 'monospace', paddingLeft: 12 }}>
                            {presetApplied.added.slice(0, 20).join(', ')}
                            {presetApplied.added.length > 20 && `… 共 ${presetApplied.added.length} 列`}
                          </div>
                        </div>
                      )}
                    </div>
                  </details>
                )}
              </div>
              <div style={styles.footer}>
                <button onClick={() => setStep('table')} style={styles.btnGhost}>← 上一步</button>
                <span style={styles.spacer} />
                <button onClick={() => setStep('preview')} disabled={mappedCount === 0} title={mappedCount === 0 ? '请至少映射一列' : ''} style={styles.btnPrimary}>下一步 →</button>
              </div>
            </div>
          )}

          {step === 'preview' && (
            <div style={styles.col}>
              {statements.length === 0 && (
                <div style={{
                  padding: '16px 20px',
                  background: 'rgba(217,119,6,0.08)',
                  border: '1px solid var(--warn, #d97706)',
                  borderLeft: '4px solid var(--warn, #d97706)',
                  borderRadius: 6,
                  display: 'flex', flexDirection: 'column', gap: 8,
                }}>
                  <div style={{ fontWeight: 700, fontSize: 14, color: 'var(--fg)' }}>
                    ⚠️ 无可导入的数据
                  </div>
                  <div style={{ fontSize: 12, color: 'var(--muted)', lineHeight: 1.6 }}>
                    {inputFormat === 'sql' ? (
                      <>SQL 编辑器为空或仅包含注释/空白。<br/>
                        <strong style={{ color: 'var(--fg)' }}>回退到 Step 1</strong> 粘贴 SQL 语句（多语句按分号切分）。
                      </>
                    ) : (
                      <>当前没有生成任何待导入的 SQL。可能原因：
                        <ul style={{ margin: '6px 0 0', padding: '0 0 0 20px', color: 'var(--fg)' }}>
                          {parse && parse.rows.length === 0
                            ? <li>源文件为空或仅含表头。</li>
                            : <li>源文件含 {parse?.rows.length ?? 0} 行，但经过行过滤 + 严格校验 + PK 预检后被剔除全部。</li>}
                          {opts.filterColumn !== null && opts.filterValue !== '' && (
                            <li>行过滤条件 <code>列{opts.filterColumn + 1} {opts.filterOp} "{opts.filterValue}"</code> 剔除全部。</li>
                          )}
                          {Object.values(opts.validations ?? {}).some((v) => hasAnyValidation(v)) && (
                            <li>列校验规则零命中（严格校验开启）。</li>
                          )}
                        </ul>
                        <strong style={{ color: 'var(--fg)' }}>回退到 Step 3</strong> 调整过滤 / 校验规则；或按 <code>Ctrl+/</code> 查看快捷键清单。
                      </>
                    )}
                  </div>
                </div>
              )}
              {inputFormat !== 'sql' && qualityReport && (
                <div style={{
                  ...styles.mutedBox,
                  borderLeft: `3px solid ${qualityGateBlocked ? 'var(--danger, #dc2626)' : 'var(--accent, #3b82f6)'}`,
                  display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
                }}>
                  <span style={styles.label}>🚦 质量门禁</span>
                  <label style={{ ...styles.check, fontSize: 11 }}>
                    <input
                      type="checkbox"
                      checked={qualityGate.enabled}
                      onChange={(e) => setQualityGate((g) => ({ ...g, enabled: e.target.checked }))}
                    />
                    <span>启用</span>
                  </label>
                  <label style={{ fontSize: 11, display: 'flex', alignItems: 'center', gap: 4 }}>
                    阈值
                    <input
                      type="number"
                      min={0}
                      max={100}
                      value={qualityGate.threshold}
                      onChange={(e) => {
                        const n = parseInt(e.target.value, 10);
                        if (Number.isFinite(n) && n >= 0 && n <= 100) {
                          setQualityGate((g) => ({ ...g, threshold: n }));
                        }
                      }}
                      style={{ ...styles.inputNum, width: 60, fontSize: 11 }}
                    />
                    /100
                  </label>
                  <span style={{ fontSize: 11, color: 'var(--muted)' }}>
                    当前评分 <strong style={{ color: qualityScoreColor(qualityReport.overallScore) }}>{qualityReport.overallScore}</strong>
                    {qualityGate.enabled && !qualityGateBlocked && ' · ✅ 通过'}
                    {qualityGate.enabled && qualityGateBlocked && ' · ❌ 拦截'}
                  </span>
                  {qualityGate.enabled && columnHealths && (() => {
                    const worst = [...columnHealths].sort((a, b) => a.health - b.health).slice(0, 5);
                    const worstBelow = worst.filter((w) => w.health < qualityGate.threshold);
                    if (worstBelow.length === 0) return null;
                    return (
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, flexWrap: 'wrap', marginLeft: 'auto' }}>
                        <span style={{ fontSize: 10, color: 'var(--muted)' }}>最差：</span>
                        {worstBelow.map((w) => {
                          const name = mappings[w.i]?.csvName ?? `#${w.i}`;
                          const color = w.health >= 80 ? 'var(--success, #22c55e)'
                            : w.health >= 60 ? 'var(--warn, #d97706)'
                            : w.health >= 40 ? '#f59e0b'
                            : 'var(--danger, #dc2626)';
                          return (
                            <button
                              key={w.i}
                              type="button"
                              onClick={() => {
                                setBatchSel(new Set<number>([w.i]));
                                setBatchAnchor(w.i);
                                setStep('map');
                              }}
                              title={`${name} 健康分 ${w.health}/100 · 点击跳回 Step 3 选中此列`}
                              style={{
                                background: 'transparent',
                                border: `1px solid ${color}`,
                                color,
                                cursor: 'pointer',
                                padding: '0 6px',
                                borderRadius: 2,
                                fontSize: 10,
                                fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                lineHeight: '16px',
                              }}
                            >
                              {name} <strong>{w.health}</strong>
                            </button>
                          );
                        })}
                      </span>
                    );
                  })()}
                </div>
              )}
              {qualityGateBlocked && (
                <div style={{
                  marginTop: 6,
                  padding: '8px 10px',
                  background: 'var(--danger-bg, rgba(220,38,38,0.08))',
                  border: '1px solid var(--danger, #dc2626)',
                  borderLeft: '4px solid var(--danger, #dc2626)',
                  borderRadius: 4,
                  display: 'flex', flexDirection: 'column', gap: 4, fontSize: 11,
                }}>
                  <div style={{ color: 'var(--danger, #dc2626)', fontWeight: 700 }}>
                    🚫 数据质量评分 {qualityReport?.overallScore}/100 低于阈值 {qualityGate.threshold}，已拦截开始导入
                  </div>
                  {qualityReport && qualityReport.issues.length > 0 && (
                    <div style={{ color: 'var(--muted)' }}>
                      主要问题：{qualityReport.issues.slice(0, 3).map((i, n) =>
                        <span key={n} style={{ margin: '0 4px' }}>
                          <span style={{ color: i.severity === 'error' ? 'var(--danger, #dc2626)' : i.severity === 'warning' ? 'var(--warn, #d97706)' : 'var(--muted)' }}>
                            [{i.severity}]
                          </span> {i.message}
                        </span>
                      ).join(' · ')}
                      {qualityReport.issues.length > 3 && ` · … +${qualityReport.issues.length - 3} 项`}
                    </div>
                  )}
                  <div style={{ display: 'flex', gap: 6, marginTop: 4, flexWrap: 'wrap' }}>
                    <button
                      onClick={() => {
                        qualityGateOverrideRef.current = true;
                        setError(null);
                      }}
                      style={{ ...styles.btnSm, color: 'var(--warn, #d97706)', borderColor: 'var(--warn, #d97706)' }}
                      title="仅本次导入覆盖门禁；CSV 或映射一旦改动将自动失效"
                    >⚠ 强制继续（本次覆盖）</button>
                    <button
                      onClick={() => setStep('map')}
                      style={{ ...styles.btnSm, color: 'var(--muted)' }}
                    >← 回调整映射</button>
                  </div>
                </div>
              )}
              {inputFormat !== 'sql' && (() => {
                if (!columnHealths) return null;
                const bands = { green: 0, orange: 0, amber: 0, red: 0 };
                const bandIdxs: Record<'green' | 'orange' | 'amber' | 'red', number[]> = {
                  green: [], orange: [], amber: [], red: [],
                };
                for (const h of columnHealths) {
                  bands[h.band]++;
                  bandIdxs[h.band].push(h.i);
                }
                const total = columnHealths.length;
                const avg = Math.round(columnHealths.reduce((s, x) => s + x.health, 0) / total);
                const belowThreshIdxs: number[] = [];
                for (const h of columnHealths) {
                  if (qualityGate.enabled && h.health < qualityGate.threshold) belowThreshIdxs.push(h.i);
                }
                const belowThresh = belowThreshIdxs.length;
                // 统计 health<80 且有可应用建议的列数
                let lowWithSuggestion = 0;
                for (const h of columnHealths) {
                  if (h.health >= 80) continue;
                  const qc = qualityReport?.columns[h.i];
                  if (qc && suggestTransforms(qc).length > 0) lowWithSuggestion++;
                }
                const bandColor = (k: 'green' | 'orange' | 'amber' | 'red') =>
                  k === 'green' ? 'var(--success, #22c55e)' : k === 'orange' ? 'var(--warn, #d97706)' : k === 'amber' ? '#f59e0b' : 'var(--danger, #dc2626)';
                return (
                  <div style={{ marginTop: 6, padding: '6px 10px', background: 'rgba(255,255,255,0.02)', border: '1px solid var(--border)', borderRadius: 3, fontSize: 11 }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
                      <span style={styles.label}>🩺 列健康分布</span>
                      <strong style={{ color: avg >= 80 ? 'var(--success, #22c55e)' : avg >= 60 ? 'var(--warn, #d97706)' : avg >= 40 ? '#f59e0b' : 'var(--danger, #dc2626)' }}>
                        平均 {avg}
                      </strong>
                      <span style={styles.muted}>({total} 列)</span>
                      <span style={{ flex: 1 }} />
                      {lowWithSuggestion > 0 && (
                        <button
                          type="button"
                          onClick={() => {
                            const nextTransforms = { ...opts.transforms };
                            const nextParams = { ...opts.transformParams };
                            for (const h of columnHealths) {
                              if (h.health >= 80) continue;
                              const qc = qualityReport?.columns[h.i];
                              if (!qc) continue;
                              const s = suggestTransforms(qc)[0];
                              if (!s) continue;
                              nextTransforms[h.i] = s.transform;
                              if (s.transform === 'regex-replace') {
                                nextParams[h.i] = {
                                  pattern: s.pattern,
                                  replacement: s.replacement,
                                  flags: s.flags ?? 'g',
                                };
                              }
                            }
                            setOpts({ ...opts, transforms: nextTransforms, transformParams: nextParams });
                            publishEditorStatus({
                              message: `⚡ 已对 ${lowWithSuggestion} 个低分列应用首条建议转换`,
                              messageAt: Date.now(),
                            });
                          }}
                          title="遍历所有健康分 <80 且质量画像存在可应用建议的列，每列取首条保守建议写入 opts.transforms。与 M30.59 单列点击互补：单列 vs 全部"
                          style={{
                            background: 'transparent',
                            border: '1px solid var(--warn, #d97706)',
                            color: 'var(--warn, #d97706)',
                            cursor: 'pointer',
                            fontSize: 10,
                            padding: '0 6px',
                            borderRadius: 2,
                            fontFamily: 'inherit',
                            lineHeight: '16px',
                          }}
                        >
                          ⚡ 一键应用 {lowWithSuggestion} 列建议
                        </button>
                      )}
                      {qualityGate.enabled && belowThresh > 0 && (
                        <button
                          type="button"
                          onClick={() => {
                            setBatchSel(new Set<number>(belowThreshIdxs));
                            setBatchAnchor(belowThreshIdxs[0]);
                            setStep('map');
                          }}
                          title="点击跳回 Step 3 并选中这 N 列，方便集中调转换/空值策略/校验"
                          style={{
                            background: 'transparent',
                            border: '1px solid var(--danger, #dc2626)',
                            color: 'var(--danger, #dc2626)',
                            cursor: 'pointer',
                            fontSize: 10,
                            padding: '0 6px',
                            borderRadius: 2,
                            fontFamily: 'inherit',
                            lineHeight: '16px',
                          }}
                        >
                          {belowThresh} 列低于门禁阈值 {qualityGate.threshold} →
                        </button>
                      )}
                    </div>
                    <div style={{ height: 8, display: 'flex', borderRadius: 4, overflow: 'hidden', background: 'rgba(255,255,255,0.04)' }}>
                      {(['green', 'orange', 'amber', 'red'] as const).map((k) => {
                        const cnt = bands[k];
                        if (cnt === 0) return null;
                        const idxs = bandIdxs[k];
                        return (
                          <button
                            key={k}
                            type="button"
                            onClick={() => {
                              setBatchSel(new Set<number>(idxs));
                              setBatchAnchor(idxs[0]);
                              setStep('map');
                            }}
                            title={`点击跳回 Step 3 并选中这 ${cnt} 列（${k === 'green' ? '≥80 良好' : k === 'orange' ? '60–79 一般' : k === 'amber' ? '40–59 较差' : '<40 严重'}）`}
                            style={{
                              width: `${(cnt * 100) / total}%`,
                              background: bandColor(k),
                              transition: 'width 0.2s',
                              border: 0,
                              padding: 0,
                              cursor: 'pointer',
                              filter: 'brightness(1.2)',
                            }}
                          />
                        );
                      })}
                    </div>
                    <div style={{ display: 'flex', gap: 10, marginTop: 3, fontSize: 10, flexWrap: 'wrap', color: 'var(--muted)' }}>
                      {([
                        ['green', '≥80 良好'],
                        ['orange', '60–79 一般'],
                        ['amber', '40–59 较差'],
                        ['red', '<40 严重'],
                      ] as const).map(([k, label]) => {
                        const idxs = bandIdxs[k];
                        const clickable = idxs.length > 0;
                        return (
                          <button
                            key={k}
                            type="button"
                            disabled={!clickable}
                            onClick={() => {
                              if (idxs.length === 0) return;
                              setBatchSel(new Set<number>(idxs));
                              setBatchAnchor(idxs[0]);
                              setStep('map');
                            }}
                            title={clickable ? `点击跳回 Step 3 并选中这 ${idxs.length} 列` : undefined}
                            style={{
                              background: 'transparent',
                              border: clickable ? `1px solid ${bandColor(k)}` : '1px solid transparent',
                              color: clickable ? 'var(--text)' : 'var(--muted)',
                              cursor: clickable ? 'pointer' : 'default',
                              opacity: clickable ? 1 : 0.5,
                              padding: '0 4px',
                              borderRadius: 2,
                              fontFamily: 'inherit',
                              fontSize: 10,
                              display: 'inline-flex',
                              alignItems: 'center',
                              gap: 4,
                              lineHeight: '16px',
                            }}
                          >
                            <span style={{ width: 8, height: 8, borderRadius: 2, background: bandColor(k), display: 'inline-block' }} />
                            {label} <strong style={{ color: clickable ? 'var(--text)' : 'var(--muted)' }}>{bands[k]}</strong>
                            {clickable && <span style={{ fontSize: 9, color: 'var(--muted)', marginLeft: 2 }}>→</span>}
                          </button>
                        );
                      })}
                    </div>
                    {total > 60 && (
                      <div style={{ marginTop: 4, fontSize: 10, color: 'var(--muted)' }} title="列数超过 60 时热力条自动隐藏以避免视觉堆挤">
                        {total} 列（热力条隐藏，&gt;60 列时不显示）
                      </div>
                    )}
                    {total <= 60 && (
                      <div
                        style={{ display: 'flex', gap: 1, marginTop: 4, flexWrap: 'wrap' }}
                        title={`每列健康热力条 · ${total} 列（点击列跳回 Step 3 单选）`}
                      >
                        {columnHealths.map((h) => {
                          const name = mappings[h.i]?.csvName ?? `#${h.i}`;
                          const target = mappings[h.i]?.targetColumn;
                          const bits: string[] = [`${name} · 健康 ${h.health}/100`];
                          if (target) bits.push(`→ ${target}`);
                          if (h.warn) bits.push(`⚠ ${h.warn}`);
                          const w = Math.max(8, Math.floor(360 / total) - 1);
                          return (
                            <button
                              key={h.i}
                              type="button"
                              onClick={() => {
                                setBatchSel(new Set<number>([h.i]));
                                setBatchAnchor(h.i);
                                setStep('map');
                              }}
                              title={bits.join('\n')}
                              style={{
                                width: w,
                                height: 12,
                                padding: 0,
                                border: 0,
                                borderRadius: 2,
                                background: bandColor(h.band),
                                cursor: 'pointer',
                                opacity: h.health >= 80 ? 0.55 : 1,
                              }}
                            />
                          );
                        })}
                      </div>
                    )}
                  </div>
                );
              })()}
              <div style={styles.label}>选项</div>
              <div style={styles.row2col}>
                <label style={styles.field}>
                  <div style={styles.label}>空字符串当 NULL</div>
                  <label style={styles.check}>
                    <input type="checkbox" checked={opts.emptyAsNull} onChange={(e) => setOpts({ ...opts, emptyAsNull: e.target.checked })} />
                    <span>启用</span>
                  </label>
                </label>
                <label style={styles.field}>
                  <div style={styles.label}>批大小（每批条数）</div>
                  <input
                    type="number"
                    min={10}
                    max={10000}
                    value={opts.batchSize}
                    onChange={(e) => {
                      const n = parseInt(e.target.value, 10);
                      if (Number.isFinite(n) && n >= 10) setOpts({ ...opts, batchSize: n });
                    }}
                    style={styles.inputNum}
                  />
                </label>
                <label style={styles.field}>
                  <div style={styles.label}>失败行处理</div>
                  <label style={styles.check} title="关闭时任一失败即整批回滚；开启时收集失败行并仅提交成功行">
                    <input type="checkbox" checked={opts.skipFailed} onChange={(e) => setOpts({ ...opts, skipFailed: e.target.checked })} />
                    <span>跳过失败行</span>
                  </label>
                </label>
              </div>
              {inputFormat !== 'sql' && statements.length > 0 && (
                <details style={{ marginTop: 4, borderLeft: '3px solid var(--ok, #10b981)', padding: '4px 8px', background: 'rgba(16,185,129,0.04)' }}>
                  <summary style={{ cursor: 'pointer', fontSize: 11, color: 'var(--text)' }}>
                    🧪 首条 SQL 实时预览（共 {statements.length} 条待导入）
                  </summary>
                  <div style={{ marginTop: 4 }}>
                    <pre style={{ ...styles.pre, fontSize: 11, maxHeight: 200, overflow: 'auto' }}>{statements[0].sql}</pre>
                  {statements[0] && 'params' in statements[0] && (() => {
                    // buildStatements 参数顺序按 opts.mode 分派：
                    //  insert: activeIdx 顺序
                    //  update: [data(非PK), pk(PK)] 各按 activeIdx 内序
                    //  upsert 有 PK: [pk, data]
                    //  upsert 无 PK: 退化 insert（activeIdx 顺序）
                    const activeIdx: number[] = [];
                    for (let j = 0; j < mappings.length; j++) {
                      if (mappings[j].targetColumn != null) activeIdx.push(j);
                    }
                    const pkSet = new Set(cols.filter((c) => c.is_primary_key).map((c) => c.name));
                    const paramOrder: number[] = [];
                    if (opts.mode === 'update') {
                      const dataIdx = activeIdx.filter((i) => !pkSet.has(mappings[i].targetColumn!));
                      const pkIdx = activeIdx.filter((i) => pkSet.has(mappings[i].targetColumn!));
                      paramOrder.push(...dataIdx, ...pkIdx);
                    } else if (opts.mode === 'upsert' && pkSet.size > 0) {
                      const pkIdx = activeIdx.filter((i) => pkSet.has(mappings[i].targetColumn!));
                      const dataIdx = activeIdx.filter((i) => !pkSet.has(mappings[i].targetColumn!));
                      paramOrder.push(...pkIdx, ...dataIdx);
                    } else {
                      paramOrder.push(...activeIdx);
                    }
                    return (
                    <div style={{ marginTop: 4, fontSize: 10, color: 'var(--muted)', display: 'flex', flexWrap: 'wrap', gap: 4, alignItems: 'center' }}>
                      <span>参数 ({statements[0].params.length})：</span>
                      {statements[0].params.map((p: Value, i: number) => {
                        const csvIdx = paramOrder[i];
                        const text = p == null ? 'NULL' : String(p);
                        const truncated = text.length > 40 ? `${text.slice(0, 40)}…` : text;
                        const isNull = p == null;
                        const isNum = typeof p === 'number' || typeof p === 'boolean';
                        const ch = csvIdx != null ? columnHealths?.[csvIdx] : undefined;
                        const healthColor = ch
                          ? ch.health >= 80 ? 'var(--success, #22c55e)'
                            : ch.health >= 60 ? 'var(--warn, #d97706)'
                            : ch.health >= 40 ? '#f59e0b'
                            : 'var(--danger, #dc2626)'
                          : 'var(--muted)';
                        const csvName = csvIdx != null ? (mappings[csvIdx]?.csvName ?? `#${csvIdx}`) : '?';
                        const colTitle = ch
                          ? `${csvName} · 健康 ${ch.health}/100${ch.warn ? ` · ⚠ ${ch.warn}` : ''} · 点击批量选中本列`
                          : csvName;
                        return (
                          <span
                            key={i}
                            style={{
                              padding: '0 4px',
                              borderRadius: 2,
                              background: isNull ? 'rgba(139,92,246,0.12)' : isNum ? 'rgba(59,130,246,0.12)' : 'rgba(16,185,129,0.12)',
                              color: isNull ? '#8b5cf6' : isNum ? '#3b82f6' : '#10b981',
                              fontFamily: 'monospace',
                              fontSize: 10,
                              whiteSpace: 'nowrap',
                              display: 'inline-flex',
                              alignItems: 'center',
                              gap: 4,
                              cursor: ch && csvIdx != null ? 'pointer' : 'default',
                            }}
                            onClick={ch && csvIdx != null ? () => {
                              setBatchSel(new Set<number>([csvIdx]));
                              setBatchAnchor(csvIdx);
                              setStep('map');
                            } : undefined}
                            title={`${colTitle} · 值：${text}`}
                          >
                            ?{i + 1}={truncated}
                            {ch && (
                              <span
                                style={{
                                  padding: '0 3px',
                                  borderRadius: 2,
                                  background: 'rgba(255,255,255,0.06)',
                                  border: `1px solid ${healthColor}`,
                                  color: healthColor,
                                  fontWeight: 700,
                                  fontSize: 9,
                                }}
                              >
                                {ch.health} →
                              </span>
                            )}
                          </span>
                        );
                      })}
                    </div>
                    );
                  })()}
                  </div>
                </details>
              )}
              {inputFormat !== 'sql' && opts.mode !== 'insert' && (
                <div style={{ ...styles.mutedBox, marginTop: 4, borderLeft: '3px solid var(--accent, #3b82f6)' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <label style={{ ...styles.check, fontSize: 11 }}>
                      <input
                        type="checkbox"
                        checked={pkCheckEnabled}
                        onChange={(e) => setPkCheckEnabled(e.target.checked)}
                      />
                      <span>预检主键存在性</span>
                    </label>
                    {opts.mode === 'update'
                      ? <span style={{ ...styles.muted, fontSize: 10 }}>（Update 模式：找到 = 将更新，缺失 = 将被跳过）</span>
                      : <span style={{ ...styles.muted, fontSize: 10 }}>（Upsert 模式：找到 = 将更新已有行，缺失 = 将插入新行）</span>}
                  </div>
                  {pkCheckEnabled && (
                    <div style={{ marginTop: 4 }}>
                      <button
                        onClick={() => void checkPkExistence()}
                        disabled={pkCheckBusy || csvStatements.length === 0}
                        style={styles.btn}
                        title="执行 SELECT COUNT(*) 查询以检查 PK 在目标表中是否已存在"
                      >
                        {pkCheckBusy ? '⏳ 预检中…' : '▶ 执行预检'}
                      </button>
                      {pkCheckResult && (
                        <div style={{ marginTop: 4, fontSize: 11 }}>
                          {pkCheckResult.error ? (
                            <span style={{ color: 'var(--warn, #d97706)' }}>⚠ {pkCheckResult.error}</span>
                          ) : (
                            <>
                              <span style={{ color: 'var(--accent, #3b82f6)' }}>
                                🔎 {pkCheckResult.total} 行中 {pkCheckResult.found} 行 PK 已存在
                                （{pkCheckResult.total > 0 ? ((100 * pkCheckResult.found) / pkCheckResult.total).toFixed(1) : 0}%）
                              </span>
                              {opts.mode === 'update'
                                ? <> · {pkCheckResult.total - pkCheckResult.found} 行将被跳过（PK 不存在）</>
                                : <> · {pkCheckResult.total - pkCheckResult.found} 行将插入新行 · {pkCheckResult.found} 行将更新已有行</>}
                              <span style={{ ...styles.muted }}> · 耗时 {pkCheckResult.ms} ms</span>
                            </>
                          )}
                        </div>
                      )}
                    </div>
                  )}
                </div>
              )}
              {inputFormat !== 'sql' && (
                <div style={{ ...styles.mutedBox, marginTop: 4, borderLeft: '3px solid var(--warn, #d97706)' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <label style={{ ...styles.check, fontSize: 11 }}>
                      <input
                        type="checkbox"
                        checked={dupPkCheckEnabled}
                        onChange={(e) => setDupPkCheckEnabled(e.target.checked)}
                      />
                      <span>检测 CSV 内部 PK 重复</span>
                    </label>
                    <span style={{ ...styles.muted, fontSize: 10 }}>
                      （本地分析无需 DB：CSV 内多行共享同一 PK 时，Insert 会失败、Upsert 后覆盖前者、Update 会重复更新）
                    </span>
                  </div>
                  {dupPkCheckEnabled && (
                    <div style={{ marginTop: 4 }}>
                      <button
                        onClick={checkDuplicatePk}
                        disabled={dupPkCheckBusy || csvStatements.length === 0}
                        style={styles.btn}
                        title="扫描 CSV 内所有行的 PK 值，找出重复的主键组合"
                      >
                        {dupPkCheckBusy ? '⏳ 检测中…' : '▶ 执行检测'}
                      </button>
                      {dupPkResult && (
                        <div style={{ marginTop: 4, fontSize: 11 }}>
                          {dupPkResult.error ? (
                            <span style={{ color: 'var(--warn, #d97706)' }}>⚠ {dupPkResult.error}</span>
                          ) : dupPkResult.dupKeys === 0 ? (
                            <span style={{ color: 'var(--ok, #10b981)' }}>
                              ✅ {dupPkResult.totalKeys} 个 PK 全部唯一 · 耗时 {dupPkResult.ms} ms
                            </span>
                          ) : (
                            <>
                              <span style={{ color: 'var(--danger, #dc2626)' }}>
                                🚫 检出 {dupPkResult.dupKeys} 个重复 PK，涉及 {dupPkResult.duplicates.length} 行
                                （共 {dupPkResult.totalKeys} 个非空 PK）
                              </span>
                              <span style={{ ...styles.muted }}> · 耗时 {dupPkResult.ms} ms</span>
                              <details style={{ marginTop: 3 }}>
                                <summary style={{ cursor: 'pointer', fontSize: 10 }}>查看前 20 行</summary>
                                <pre style={{ ...styles.pre, fontSize: 10, marginTop: 3, maxHeight: 140 }}>
{dupPkResult.duplicates.slice(0, 20).map((d) =>
  `CSV 第 ${d.csvRow} 行 · PK=${d.key}`
).join('\n')}
{dupPkResult.duplicates.length > 20 ? `\n… +${dupPkResult.duplicates.length - 20} 行` : ''}
                                </pre>
                              </details>
                            </>
                          )}
                        </div>
                      )}
                    </div>
                  )}
                </div>
              )}
              {inputFormat !== 'sql' && opts.mode !== 'insert' && (
                <div style={{ ...styles.mutedBox, marginTop: 4, borderLeft: '3px solid var(--warn, #d97706)' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <label style={{ ...styles.check, fontSize: 11 }}>
                      <input
                        type="checkbox"
                        checked={fkCheckEnabled}
                        onChange={(e) => setFkCheckEnabled(e.target.checked)}
                      />
                      <span>预检外键有效性</span>
                    </label>
                    <span style={{ ...styles.muted, fontSize: 10 }}>（逐行检查 FK 列在引用表中是否存在；无效行导入时会被外键约束拒绝）</span>
                  </div>
                  {fkCheckEnabled && (
                    <div style={{ marginTop: 4 }}>
                      <button
                        onClick={() => void checkFkValidity()}
                        disabled={fkCheckBusy || csvStatements.length === 0}
                        style={styles.btn}
                        title="对每个外键列执行 SELECT COUNT(*) 检查引用表中的有效值"
                      >
                        {fkCheckBusy ? '⏳ 预检中…' : '▶ 执行预检'}
                      </button>
                      {fkCheckResult && (
                        <div style={{ marginTop: 4, fontSize: 11 }}>
                          {fkCheckResult.error ? (
                            <span style={{ color: 'var(--warn, #d97706)' }}>⚠ {fkCheckResult.error}</span>
                          ) : fkCheckResult.total === 0 ? (
                            <span style={{ color: 'var(--muted)' }}>— 无可检查的行或列（外键列未在映射中/全部为空）</span>
                          ) : (
                            <>
                              <span style={{ color: fkCheckResult.invalid === 0 ? 'var(--ok, #10b981)' : 'var(--danger, #dc2626)' }}>
                                🔗 {fkCheckResult.total} 个 FK 引用中 {fkCheckResult.invalid} 个无效
                              </span>
                              {fkCheckResult.invalid > 0 && <> · ⚠ 这些行导入时会被外键约束拒绝</>}
                              <span style={{ ...styles.muted }}> · 耗时 {fkCheckResult.ms} ms</span>
                            </>
                          )}
                        </div>
                      )}
                    </div>
                  )}
                </div>
              )}
              {inputFormat !== 'sql' && (
              <div style={{ marginTop: 4 }}>
                <div style={{ ...styles.label, marginBottom: 4 }}>
                  行过滤 <span style={{ color: 'var(--muted)', fontWeight: 400, textTransform: 'none' }}>（仅导入满足条件的行）</span>
                </div>
                <div style={{ ...styles.row2col, gap: 8 }}>
                  <label style={{ ...styles.field, flex: 1.5 }}>
                    <div style={{ ...styles.label, textTransform: 'none' }}>CSV 列</div>
                    <select
                      value={opts.filterColumn === null ? '' : String(opts.filterColumn)}
                      onChange={(e) => setOpts({ ...opts, filterColumn: e.target.value === '' ? null : Number(e.target.value) })}
                      style={styles.selectSm}
                      disabled={!parse}
                    >
                      <option value="">— 不过滤 —</option>
                      {parse?.columns.map((c, i) => (
                        <option key={i} value={i}>{i + 1}. {c}</option>
                      ))}
                    </select>
                  </label>
                  <label style={{ ...styles.field, flex: 0.5, minWidth: 120 }}>
                    <div style={{ ...styles.label, textTransform: 'none' }}>操作</div>
                    <select
                      value={opts.filterOp}
                      onChange={(e) => setOpts({ ...opts, filterOp: e.target.value as 'eq' | 'neq' | 'contains' | 'regex' })}
                      style={styles.selectSm}
                    >
                      <option value="eq">等于</option>
                      <option value="neq">不等于</option>
                      <option value="contains">包含</option>
                      <option value="regex">正则匹配</option>
                    </select>
                  </label>
                  <label style={{ ...styles.field, flex: 1 }}>
                    <div style={{ ...styles.label, textTransform: 'none' }}>值</div>
                    <input
                      type="text"
                      value={opts.filterValue}
                      onChange={(e) => setOpts({ ...opts, filterValue: e.target.value })}
                      style={styles.inputSm}
                      placeholder="过滤条件值"
                    />
                  </label>
                </div>
                {opts.filterColumn !== null && opts.filterValue !== '' && parse && (() => {
                  const keptList: number[] = [];
                  const removedList: number[] = [];
                  let totalKept = 0;
                  parse.rows.forEach((r, i) => {
                    const v = r[opts.filterColumn!] ?? '';
                    let pass = true;
                    switch (opts.filterOp) {
                      case 'eq': pass = v === opts.filterValue; break;
                      case 'neq': pass = v !== opts.filterValue; break;
                      case 'contains': pass = v.includes(opts.filterValue); break;
                      case 'regex':
                        try { pass = new RegExp(opts.filterValue).test(v); }
                        catch { pass = true; }
                        break;
                    }
                    if (pass) {
                      totalKept++;
                      if (keptList.length < 3) keptList.push(i);
                    } else {
                      if (removedList.length < 3) removedList.push(i);
                    }
                  });
                  return (
                    <div style={{ marginTop: 4 }}>
                      <span style={styles.muted}>过滤后剩 <strong>{totalKept}</strong> / {parse.rows.length} 行（前 3 命中 / 前 3 剔除）：</span>
                      <div style={{ marginTop: 2, display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 4 }}>
                        <div>
                          <div style={{ fontSize: 10, color: 'var(--ok, #10b981)' }}>✅ 保留</div>
                          {keptList.length === 0
                            ? <div style={{ fontSize: 10, color: 'var(--muted)' }}>（无匹配行）</div>
                            : keptList.map((i) => (
                              <div key={i} style={{ fontSize: 10, fontFamily: 'monospace', color: 'var(--text)' }} title={parse.rows[i]?.[opts.filterColumn!] ?? ''}>
                                <span style={styles.muted}>L{i + 1}</span> {parse.rows[i]?.[opts.filterColumn!] ?? ''}
                              </div>
                            ))}
                        </div>
                        <div>
                          <div style={{ fontSize: 10, color: 'var(--danger, #dc2626)' }}>❌ 剔除</div>
                          {removedList.length === 0
                            ? <div style={{ fontSize: 10, color: 'var(--muted)' }}>（无剔除行）</div>
                            : removedList.map((i) => (
                              <div key={i} style={{ fontSize: 10, fontFamily: 'monospace', color: 'var(--text)' }} title={parse.rows[i]?.[opts.filterColumn!] ?? ''}>
                                <span style={styles.muted}>L{i + 1}</span> {parse.rows[i]?.[opts.filterColumn!] ?? ''}
                              </div>
                            ))}
                        </div>
                      </div>
                    </div>
                  );
                })()}
              </div>
              )}
              {inputFormat !== 'sql' && parse && (() => {
                const entries = Object.entries(validations).filter(([, v]) => hasAnyValidation(v)).map(([k]) => Number(k)).sort((a, b) => a - b);
                if (entries.length === 0) return null;
                const MAX_ROWS = 100000;
                const rows = parse.rows.slice(0, MAX_ROWS);
                const cols = entries.map((idx) => {
                  const v = validations[idx];
                  const csvName = parse.columns[idx] ?? '';
                  const samples: Array<{ row: number; raw: string; reason: string }> = [];
                  let total = 0;
                  for (let i = 0; i < rows.length; i++) {
                    const raw = rows[i][idx] ?? '';
                    const reason = validateCell(raw, v);
                    if (reason) {
                      total++;
                      if (samples.length < 3) samples.push({ row: i, raw, reason });
                    }
                  }
                  return { idx, csvName, samples, total };
                });
                const totalViolations = cols.reduce((s, c) => s + c.total, 0);
                return (
                  <details style={{ marginTop: 4, borderLeft: `3px solid ${totalViolations > 0 ? 'var(--danger, #dc2626)' : 'var(--ok, #10b981)'}`, padding: '4px 8px', background: totalViolations > 0 ? 'rgba(220,38,38,0.04)' : 'rgba(16,185,129,0.04)' }}>
                    <summary style={{ cursor: 'pointer', fontSize: 11 }}>
                      {totalViolations > 0 ? '🛡 校验违规样例' : '✅ 校验通过'} · {cols.length} 列已配置 · {totalViolations} 处违规样例
                    </summary>
                    <div style={{ marginTop: 4, display: 'flex', flexDirection: 'column', gap: 6 }}>
                      {cols.map((c) => (
                        <div key={c.idx}>
                          <div style={{ fontSize: 10, color: 'var(--muted)', marginBottom: 2 }}>
                            列 {c.idx + 1}（{c.csvName || `#${c.idx + 1}`}）
                            {c.total === 0
                              ? <span style={{ color: 'var(--ok, #10b981)', marginLeft: 6 }}>全部通过</span>
                              : <span style={{ color: 'var(--danger, #dc2626)', marginLeft: 6 }}>⚠ 共 {c.total} 处违规{c.samples.length < c.total ? '（展示前 3）' : ''}</span>}
                          </div>
                          {c.samples.map((s) => (
                            <div key={s.row} style={{ fontSize: 10, fontFamily: 'monospace', padding: '2px 4px', background: 'rgba(220,38,38,0.06)', borderRadius: 2, marginBottom: 2 }} title={s.reason}>
                              <span style={styles.muted}>L{s.row + 1}</span>
                              <span style={{ color: 'var(--danger, #dc2626)' }}> {JSON.stringify(s.raw)}</span>
                              <span style={styles.muted}> — {s.reason}</span>
                            </div>
                          ))}
                        </div>
                      ))}
                    </div>
                  </details>
                );
              })()}
              {inputFormat !== 'sql' && (() => {
                // 提交前重复映射警告：多列指向同一 target 时后续列会覆盖前面列
                const targetToIndices = new Map<string, number[]>();
                for (let i = 0; i < mappings.length; i++) {
                  const t = mappings[i].targetColumn;
                  if (!t) continue;
                  const arr = targetToIndices.get(t);
                  if (arr) arr.push(i);
                  else targetToIndices.set(t, [i]);
                }
                const dups = Array.from(targetToIndices.entries()).filter(([, idxs]) => idxs.length > 1);
                if (dups.length === 0) return null;
                return (
                  <details
                    style={{
                      marginTop: 4,
                      borderLeft: '3px solid var(--danger, #dc2626)',
                      padding: '4px 8px',
                      background: 'rgba(220,38,38,0.06)',
                    }}
                    open
                  >
                    <summary style={{ cursor: 'pointer', fontSize: 11, color: 'var(--danger, #dc2626)' }}>
                      🔁 重复映射目标列 · {dups.length} 组 · {dups.reduce((s, [, idxs]) => s + idxs.length - 1, 0)} 列写入将被覆盖
                    </summary>
                    <div style={{ marginTop: 4, display: 'flex', flexDirection: 'column', gap: 4, fontSize: 10, fontFamily: 'monospace' }}>
                      <div style={{ color: 'var(--muted)' }}>写入顺序按 CSV 列索引递增，每个重复组中最靠后的 CSV 列胜出，前面列的写入值会被覆盖。建议在 Step 3 只保留一组映射。</div>
                      {dups.map(([t, idxs]) => (
                        <div key={t} style={{ padding: '2px 6px', background: 'rgba(255,255,255,0.03)', borderRadius: 2 }}>
                          <strong style={{ color: 'var(--text)' }}>{t}</strong>
                          <span style={{ color: 'var(--muted)', marginLeft: 6 }}>
                            {idxs.map((d) => {
                              const isLast = d === idxs[idxs.length - 1];
                              return `#${d + 1}(${mappings[d].csvName || `col${d + 1}`})${isLast ? ' ✅胜出' : ' ❌被覆盖'}`;
                            }).join(' · ')}
                          </span>
                        </div>
                      ))}
                    </div>
                  </details>
                );
              })()}
              <details style={{ marginTop: 4 }}>
                <summary style={styles.summary}>{(() => {
                  if (inputFormat === 'sql') return `预览 SQL 多语句（共 ${statements.length} 条，未执行）`;
                  const verb = opts.mode === 'update' ? 'UPDATE' : opts.mode === 'upsert' ? 'UPSERT' : 'INSERT';
                  return `预览 ${verb} SQL（共 ${statements.length} 条）`;
                })()}</summary>
                <pre style={styles.pre}>{previewSql}</pre>
                {inputFormat === 'sql' && sqlStatements.length > 0 && (
                  <div style={{ marginTop: 8 }}>
                    <div style={styles.muted}>前 5 条 SQL 语句（未执行）：</div>
                    <pre style={styles.pre}>
                      {sqlStatements.slice(0, 5).map((s, i) =>
                        `#${i + 1} (param ? ×${sqlParamCounts[i] ?? 0})\n${s.sql}`
                      ).join('\n\n')}
                    </pre>
                  </div>
                )}
                {parse && csvStatements.length > 0 && (
                  <div style={{ marginTop: 8 }}>
                    <div style={styles.muted}>Dry-run 转换预览（前 5 行，原值 → 转换后值 · 目标 dtype）：</div>
                    <div style={{ marginTop: 4, maxHeight: 260, overflow: 'auto', border: '1px solid var(--border)', borderRadius: 4 }}>
                      <table style={{ width: '100%', borderCollapse: 'collapse', fontFamily: 'monospace', fontSize: 11 }}>
                        <thead>
                          <tr>
                            <th style={styles.previewTh}>#</th>
                            <th style={styles.previewTh}>CSV 列</th>
                            <th style={styles.previewTh}>原值</th>
                            <th style={styles.previewTh}>→</th>
                            <th style={styles.previewTh}>转换后</th>
                            <th style={styles.previewTh}>目标 dtype</th>
                          </tr>
                        </thead>
                        <tbody>
                          {csvStatements.slice(0, 5).flatMap((s, ri) =>
                            targetColNames.map((targetName, j) => {
                              const activeIdx = mappings
                                .map((m, i) => ({ m, i }))
                                .filter(({ m }) => m.targetColumn != null)
                                .map(({ i }) => i);
                              const csvIdx = activeIdx[j];
                              const csvName = parse.columns[csvIdx] ?? '';
                              const transform = opts.transforms?.[csvIdx];
                              const raw = parse.rows[s.origIdx]?.[csvIdx] ?? '';
                              const conv = s.params[j];
                              const colInfo = colInfoMap.get(targetName);
                              const transformed = transform && transform !== 'none';
                              return (
                                <tr key={`${ri}-${j}`}>
                                  <td style={styles.previewTdMuted}>{s.origIdx + 1}</td>
                                  <td style={styles.previewTd} title={csvName}>{csvName}</td>
                                  <td style={{ ...styles.previewTd, color: 'var(--muted)', maxWidth: 160 }} title={raw}>{raw === '' ? <em>NULL</em> : raw}</td>
                                  <td style={styles.previewTdMuted}>{transformed ? '▶' : '·'}</td>
                                  <td style={{ ...styles.previewTd, maxWidth: 200, fontWeight: transformed ? 600 : 400 }} title={formatValue(conv)}>
                                    {conv === null
                                      ? <em style={{ color: 'var(--muted)' }}>NULL</em>
                                      : typeof conv === 'boolean'
                                        ? <span style={{ color: 'var(--ok, #10b981)' }}>{conv ? 'true' : 'false'}</span>
                                        : typeof conv === 'number'
                                          ? <span style={{ color: '#3b82f6' }}>{conv}</span>
                                          : <span style={{ color: 'var(--accent, #3b82f6)' }}>{String(conv)}</span>}
                                  </td>
                                  <td style={{ ...styles.previewTd, fontSize: 10 }}>
                                    {colInfo ? (
                                      <span style={{ ...styles.typeBadge, color: colDtypeColor(colInfo.data_type) }}>{colInfo.data_type}</span>
                                    ) : '—'}
                                  </td>
                                </tr>
                              );
                            })
                          )}
                        </tbody>
                      </table>
                    </div>
                  </div>
                )}
                {(validationResult?.violations.length ?? 0) > 0 && (
                  <div style={{ marginTop: 8 }}>
                    <div style={{ ...styles.muted, color: 'var(--warn, #d97706)' }}>
                      ⚠ 校验违规：{validationResult!.violations.length} 行
                      {opts.strictValidation && ' · 严格模式将自动跳过'}
                    </div>
                  </div>
                )}
                {opts.filterColumn !== null && opts.filterValue !== '' && parse && (
                  <div style={{ marginTop: 8 }}>
                    <div style={{ ...styles.muted, color: 'var(--accent, #3b82f6)' }}>
                      🎯 行过滤已生效：原 {parse.rows.length} 行 → 生成 {csvStatements.length} 条
                    </div>
                  </div>
                )}
              </details>
              {progress && (() => {
                const pct = progress.total > 0 ? (progress.done / progress.total) * 100 : 0;
                // ETA：按当前平均速率推算剩余耗时（>=1s 才显示，前 1s 抖动大；>=10 行才更准）
                let etaText = '';
                if (progress.ms >= 1000 && progress.done >= 10 && progress.done < progress.total) {
                  const rate = progress.done / progress.ms; // rows per ms
                  const remainMs = Math.round((progress.total - progress.done) / rate);
                  const remainSec = Math.max(0, Math.round(remainMs / 1000));
                  etaText = remainSec < 1
                    ? ' · 预计 <1s'
                    : remainSec < 60
                    ? ` · 预计 ${remainSec}s`
                    : remainSec < 3600
                    ? ` · 预计 ${Math.floor(remainSec / 60)}m${remainSec % 60 < 10 ? '0' : ''}${remainSec % 60}s`
                    : ` · 预计 ${Math.floor(remainSec / 3600)}h${Math.floor((remainSec % 3600) / 60)}m`;
                }
                // 速度：rows/s
                let rateText = '';
                if (progress.ms > 0 && progress.done > 0) {
                  const rps = Math.round((progress.done / progress.ms) * 1000);
                  rateText = ` · ${rps.toLocaleString()} 行/s`;
                }
                // 分块子进度：只在多批次时显示
                const showBatchBar = progress.batchTotal > 1;
                const batchPct = showBatchBar && progress.batchSize > 0
                  ? (progress.batchDone / progress.batchSize) * 100
                  : 0;
                return (
                  <div>
                    <div style={styles.progressOuter}>
                      <div style={{ ...styles.progressInner, width: `${pct}%` }} />
                    </div>
                    {showBatchBar && (
                      <div
                        style={{ ...styles.progressOuter, marginTop: 2, height: 4, background: 'rgba(255,255,255,0.06)' }}
                        title={`当前批次 ${progress.batch}/${progress.batchTotal} 内子进度：${progress.batchDone}/${progress.batchSize}`}
                      >
                        <div
                          style={{
                            ...styles.progressInner, width: `${batchPct}%`, height: '100%',
                            background: 'var(--accent, #3b82f6)', opacity: 0.6,
                          }}
                        />
                      </div>
                    )}
                    <div style={{ ...styles.muted, display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                      <span>
                        进度：<b style={{ color: 'var(--text)' }}>{progress.done}</b> / {progress.total}
                        {progress.total > 0 && ` (${pct.toFixed(1)}%)`}
                        {rateText}{etaText}
                      </span>
                      <span>· 耗时 {progress.ms} ms</span>
                      {showBatchBar && progress.batch > 0 && progress.batch <= progress.batchTotal && (
                        <span title={`分块子进度：当前第 ${progress.batch}/${progress.batchTotal} 批，批内 ${progress.batchDone}/${progress.batchSize}`}>
                          · 批次 {progress.batch}/{progress.batchTotal}（{progress.batchDone}/{progress.batchSize}）
                        </span>
                      )}
                    </div>
                  </div>
                );
              })()}
              {(undoStack.length > 0 || redoStack.length > 0) && (
                <div style={{
                  ...styles.mutedBox, marginTop: 6, marginBottom: 6,
                  borderLeft: undoStack.length > 0
                    ? `3px solid ${failCategoryMeta(undoStack[0].key).color}`
                    : '3px solid var(--accent, #3b82f6)',
                  background: 'var(--accent-dim, rgba(59,130,246,0.10))',
                  display: 'flex', alignItems: 'center', gap: 8, padding: '6px 10px', fontSize: 11,
                  flexWrap: 'wrap',
                }}>
                  {/* 栈可视条：undo 红 / redo 蓝 分段（cap 20），按 fix 字段变化密度着色（密度越深越饱和），
                      前 batchStepN 段额外尺寸 + accent 边框高亮表示"即将撤销/重做" */}
                  <div style={{ flexBasis: '100%', display: 'flex', alignItems: 'center', gap: 2, marginBottom: 2 }}>
                    {undoStack.length > 0 && (
                      <div style={{ display: 'inline-flex', gap: 1, flexShrink: 0 }} title={`待撤销 ${undoStack.length} 项`}>
                        {undoStack.slice(0, Math.min(undoStack.length, 20)).map((e, i) => {
                          const inBatch = i < batchStepN;
                          // density = 该 fix 的字段变化数：顶部与当前 opts 比，其余与下一条 undoStack 的 snapshot 比
                          const density = (i + 1 < undoStack.length
                            ? buildUndoDiff(undoStack[i + 1].snapshot, undoStack[i].snapshot)
                            : buildUndoDiff(opts, undoStack[i].snapshot)).length;
                          const a = densityAlpha(density);
                          return (
                            <div
                              key={i}
                              style={{
                                width: inBatch ? 9 : 5, height: inBatch ? 14 : 10,
                                background: `rgba(220,38,38,${a.toFixed(2)})`,
                                border: inBatch ? '1px solid rgba(220,38,38,0.9)' : '1px solid rgba(220,38,38,0.5)',
                                borderRadius: 1,
                              }}
                              title={`${e.icon} ${e.label} — ${new Date(e.at).toLocaleTimeString('zh-CN')} · ${density} 项字段变化`}
                            />
                          );
                        })}
                      </div>
                    )}
                    {undoStack.length > 0 && (
                      <span style={{ color: 'var(--danger, #dc2626)', fontSize: 10, fontWeight: 600, flexShrink: 0 }}>
                        undo:{undoStack.length}
                      </span>
                    )}
                    {undoStack.length > 0 && redoStack.length > 0 && (
                      <span style={{ color: 'var(--muted)', fontSize: 10 }}>·</span>
                    )}
                    {redoStack.length > 0 && (
                      <div style={{ display: 'inline-flex', gap: 1, flexShrink: 0 }} title={`待重做 ${redoStack.length} 项`}>
                        {redoStack.slice(0, Math.min(redoStack.length, 20)).map((e, i) => {
                          const inBatch = i < batchStepN;
                          // density：顶部与当前 opts 比，其余与下一条 redoStack 的 snapshot 比
                          const density = (i + 1 < redoStack.length
                            ? buildUndoDiff(redoStack[i + 1].snapshot, redoStack[i].snapshot)
                            : buildUndoDiff(opts, redoStack[i].snapshot)).length;
                          const a = densityAlpha(density);
                          return (
                            <div
                              key={i}
                              style={{
                                width: inBatch ? 9 : 5, height: inBatch ? 14 : 10,
                                background: `rgba(59,130,246,${a.toFixed(2)})`,
                                border: inBatch ? '1px solid rgba(59,130,246,0.9)' : '1px solid rgba(59,130,246,0.5)',
                                borderRadius: 1,
                              }}
                              title={`${e.icon} ${e.label} — ${new Date(e.at).toLocaleTimeString('zh-CN')} · ${density} 项字段变化`}
                            />
                          );
                        })}
                      </div>
                    )}
                    {redoStack.length > 0 && (
                      <span style={{ color: 'var(--accent, #3b82f6)', fontSize: 10, fontWeight: 600, flexShrink: 0 }}>
                        redo:{redoStack.length}
                      </span>
                    )}
                    {(undoStack.length + redoStack.length) >= MAX_FIX_HISTORY && (
                      <span style={{ color: 'var(--warning, #f59e0b)', fontSize: 10 }} title="已达 MAX_FIX_HISTORY 上限，最早的条目将被丢弃">⚠ MAX</span>
                    )}
                  </div>
                  {undoStack.length > 0 ? (
                    <>
                      <span style={{ fontWeight: 600 }}>{undoStack[0].icon} 已应用修复</span>
                      <span style={{ color: failCategoryMeta(undoStack[0].key).color }}>{undoStack[0].label}</span>
                      <span style={{ ...styles.muted, flex: 1, overflow: 'hidden', textOverflow: 'ellipsis' }} title={undoStack[0].msg}>
                        {undoStack[0].msg}
                      </span>
                      {undoStack.length > 1 && (
                        <span style={{ ...styles.muted, fontSize: 10 }} title={`已应用 ${undoStack.length} 次修复，可依次撤销`}>+{undoStack.length - 1}</span>
                      )}
                      <button
                        onClick={() => setDiffOpen((v) => !v)}
                        style={{ ...styles.btnSm, padding: '1px 8px', color: 'var(--accent, #3b82f6)' }}
                        title="预览撤销将带来的字段变化（当前 → 撤销后）"
                      >{diffOpen ? '🔼 隐藏变化' : '🔽 查看变化'}</button>
                      <div style={{
                        display: 'inline-flex', alignItems: 'center', gap: 2,
                        padding: '1px 4px', background: 'var(--panel)',
                        border: '1px solid var(--border)', borderRadius: 3,
                        fontSize: 11,
                      }}>
                        <span style={{ color: 'var(--muted)', fontSize: 10 }}>N</span>
                        <button
                          onClick={() => setBatchStepN((n) => Math.max(1, n - 1))}
                          style={{ border: 'none', background: 'transparent', cursor: 'pointer', padding: '0 4px', color: 'var(--text)' }}
                          title="减少 1 步"
                        >−</button>
                        <input
                          type="number" min={1} max={undoStack.length + redoStack.length} value={batchStepN}
                          onChange={(e) => {
                            const v = parseInt(e.target.value, 10);
                            setBatchStepN(Number.isFinite(v) && v > 0 ? v : 1);
                          }}
                          style={{
                            width: 36, textAlign: 'center', fontSize: 11,
                            background: 'transparent', border: '1px solid var(--border)',
                            borderRadius: 2, color: 'var(--text)', padding: '0 2px',
                            fontFamily: 'monospace',
                          }}
                          title={`批量撤销/重做步数（当前 ${batchStepN}，栈深 ${undoStack.length}/${redoStack.length}）`}
                        />
                        <button
                          onClick={() => setBatchStepN((n) => n + 1)}
                          style={{ border: 'none', background: 'transparent', cursor: 'pointer', padding: '0 4px', color: 'var(--text)' }}
                          title="增加 1 步"
                        >+</button>
                        <span style={{ display: 'inline-flex', gap: 1, marginLeft: 4 }}>
                          {[2, 3].map((k) => (
                            <button
                              key={k}
                              onClick={() => setBatchStepN(k)}
                              style={{
                                ...styles.btnSm, padding: '0 4px', fontSize: 10,
                                background: batchStepN === k ? 'var(--accent-dim, rgba(59,130,246,0.15))' : 'transparent',
                                color: batchStepN === k ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                                border: '1px solid var(--border)',
                              }}
                            >{k}</button>
                          ))}
                          <button
                            onClick={() => setBatchStepN(Math.max(undoStack.length, redoStack.length))}
                            style={{
                              ...styles.btnSm, padding: '0 4px', fontSize: 10,
                              background: batchStepN === Math.max(undoStack.length, redoStack.length) ? 'var(--accent-dim, rgba(59,130,246,0.15))' : 'transparent',
                              color: batchStepN === Math.max(undoStack.length, redoStack.length) ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                              border: '1px solid var(--border)',
                            }}
                            title="设为全部（Ctrl+Shift+Z 或 Alt+Shift+Z）"
                          >∞</button>
                        </span>
                      </div>
                      <button
                        onClick={() => undoFixN(batchStepN)}
                        style={{ ...styles.btnSm, padding: '1px 8px', color: 'var(--danger, #dc2626)' }}
                        title={`一次撤销 ${batchStepN} 项修复（Ctrl+Z），共 ${undoStack.length} 项可用；按 Alt+Z 撤销全部`}
                      >↩️ 撤销 ×{batchStepN}</button>
                      {redoStack.length > 0 && (
                        <button
                          onClick={() => redoFixN(batchStepN)}
                          style={{ ...styles.btnSm, padding: '1px 8px', color: 'var(--accent, #3b82f6)' }}
                          title={`一次重做 ${batchStepN} 项撤销（Ctrl+Shift+Z 或 Ctrl+Y），共 ${redoStack.length} 项可用；按 Alt+Shift+Z 全部重做`}
                        >↪️ 重做 ×{batchStepN}</button>
                      )}
                      {failCategoryFilter && filteredFailedRows.length > 0 && (
                        <button
                          onClick={() => {
                            const set = new Set<number>();
                            filteredFailedRows.forEach((fr) => set.add(fr.stmtIdx));
                            setRetryIndices(set);
                            setStep('input');
                          }}
                          style={{ ...styles.btnSm, padding: '1px 8px', color: 'var(--accent, #3b82f6)' }}
                          title="撤销后先修改 CSV，再只重跑受影响的行"
                        >🔍 修改 CSV 后重跑</button>
                      )}
                    </>
                  ) : (
                    <>
                      <span style={{ fontWeight: 600, color: 'var(--accent, #3b82f6)' }}>✅ 已全部撤销</span>
                      <span style={{ ...styles.muted, flex: 1, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                        共撤销 {redoStack.length} 项修复，配置已恢复到初始状态
                      </span>
                      <div style={{
                        display: 'inline-flex', alignItems: 'center', gap: 2,
                        padding: '1px 4px', background: 'var(--panel)',
                        border: '1px solid var(--border)', borderRadius: 3,
                        fontSize: 11,
                      }}>
                        <span style={{ color: 'var(--muted)', fontSize: 10 }}>N</span>
                        <button
                          onClick={() => setBatchStepN((n) => Math.max(1, n - 1))}
                          style={{ border: 'none', background: 'transparent', cursor: 'pointer', padding: '0 4px', color: 'var(--text)' }}
                          title="减少 1 步"
                        >−</button>
                        <input
                          type="number" min={1} max={redoStack.length} value={batchStepN}
                          onChange={(e) => {
                            const v = parseInt(e.target.value, 10);
                            setBatchStepN(Number.isFinite(v) && v > 0 ? v : 1);
                          }}
                          style={{
                            width: 36, textAlign: 'center', fontSize: 11,
                            background: 'transparent', border: '1px solid var(--border)',
                            borderRadius: 2, color: 'var(--text)', padding: '0 2px',
                            fontFamily: 'monospace',
                          }}
                          title={`批量重做步数（当前 ${batchStepN}，可用 ${redoStack.length}）`}
                        />
                        <button
                          onClick={() => setBatchStepN((n) => n + 1)}
                          style={{ border: 'none', background: 'transparent', cursor: 'pointer', padding: '0 4px', color: 'var(--text)' }}
                          title="增加 1 步"
                        >+</button>
                        <span style={{ display: 'inline-flex', gap: 1, marginLeft: 4 }}>
                          {[2, 3].map((k) => (
                            <button
                              key={k}
                              onClick={() => setBatchStepN(k)}
                              style={{
                                ...styles.btnSm, padding: '0 4px', fontSize: 10,
                                background: batchStepN === k ? 'var(--accent-dim, rgba(59,130,246,0.15))' : 'transparent',
                                color: batchStepN === k ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                                border: '1px solid var(--border)',
                              }}
                            >{k}</button>
                          ))}
                          <button
                            onClick={() => setBatchStepN(redoStack.length)}
                            style={{
                              ...styles.btnSm, padding: '0 4px', fontSize: 10,
                              background: batchStepN === redoStack.length ? 'var(--accent-dim, rgba(59,130,246,0.15))' : 'transparent',
                              color: batchStepN === redoStack.length ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                              border: '1px solid var(--border)',
                            }}
                            title="设为全部（Alt+Shift+Z）"
                          >∞</button>
                        </span>
                      </div>
                      <span style={{ display: 'inline-flex', gap: 2 }} title="导出修复时间线：MD/JSON/CSV 三种格式">
                        <button
                          onClick={() => void exportUndoTimeline('md')}
                          style={{ ...styles.btnSm, padding: '1px 6px', color: 'var(--accent, #3b82f6)', background: 'rgba(59,130,246,0.10)' }}
                          title="导出所有已撤销修复的完整时间线到 Markdown 文件（每条修复的字段变化 + 说明 + 应用时间）"
                        >📄 MD</button>
                        <button
                          onClick={() => void exportUndoTimeline('json')}
                          style={{ ...styles.btnSm, padding: '1px 6px', color: 'var(--muted)', background: 'transparent', border: '1px solid var(--border)' }}
                          title="导出 JSON 结构化时间线（每条 fix 含 icon/label/msg/key/at + diff 数组）"
                        >JSON</button>
                        <button
                          onClick={() => void exportUndoTimeline('csv')}
                          style={{ ...styles.btnSm, padding: '1px 6px', color: 'var(--muted)', background: 'transparent', border: '1px solid var(--border)' }}
                          title="导出 CSV 表格（每行一个 (fix, field) 变化，可直接用 Excel/Google Sheets 打开）"
                        >CSV</button>
                        <span style={{ fontSize: 10, color: 'var(--muted)', alignSelf: 'center' }}>({redoStack.length})</span>
                      </span>
                      <button
                        onClick={() => redoFixN(batchStepN)}
                        style={{ ...styles.btnSm, padding: '1px 8px', color: 'var(--accent, #3b82f6)' }}
                        title={`一次重做 ${batchStepN} 项撤销（Ctrl+Shift+Z 或 Ctrl+Y），共 ${redoStack.length} 项可用；按 Alt+Shift+Z 全部重做`}
                      >↪️ 重做 ×{batchStepN}</button>
                    </>
                  )}
                </div>
              )}
              {undoStack.length > 0 && diffOpen && (() => {
                // 若 batchStepN>1，展示"一次撤销 N 步后"的完整 diff（而非仅栈顶）
                const actualN = Math.min(batchStepN, undoStack.length);
                const targetSnap = undoStack[actualN - 1].snapshot;
                const diffItems = buildUndoDiff(opts, targetSnap);
                const groups = filterDiffGroups(groupDiffItems(diffItems));
                const allCollapsed = groups.length > 0 && groups.every((g) => collapsedGroups.has(g.group));
                const searchQuery = diffSearch.trim();
                const totalMatched = groups.reduce((sum, g) => sum + g.items.length, 0);
                // 计算可见 items 的扁平前缀和：只统计未折叠分组的 items 长度
                // 供每组渲染时 O(1) 判定该组内 item 是否为当前命中光标
                const visiblePrefix = new Map<string, number>();
                let runSum = 0;
                for (const g of groups) {
                  if (collapsedGroups.has(g.group)) continue;
                  visiblePrefix.set(g.group, runSum);
                  runSum += g.items.length;
                }
                return (
                  <div style={{
                    ...styles.mutedBox, marginBottom: 6, padding: '6px 10px',
                    fontSize: 11, lineHeight: 1.6,
                    fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                    wordBreak: 'break-word',
                  }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4, flexWrap: 'wrap' }}>
                      <span style={{ fontWeight: 600, color: 'var(--muted, #6b7280)' }}>📄 撤销预览</span>
                      {actualN > 1 && (
                        <span style={{ fontSize: 10, color: 'var(--accent, #3b82f6)', background: 'var(--accent-dim, rgba(59,130,246,0.10))', padding: '0 4px', borderRadius: 2 }}>×{actualN}</span>
                      )}
                      <span style={{ ...styles.muted, fontSize: 10 }}>（当前 <span style={{ color: 'var(--danger, #dc2626)' }}>✗</span> → {actualN > 1 ? `撤销 ${actualN} 步后` : '撤销后'} <span style={{ color: 'var(--accent, #3b82f6)' }}>✓</span>）</span>
                      <span style={{ ...styles.muted, fontSize: 10 }}>
                        · {totalMatched}/{diffItems.length} 项{searchQuery && <> · 搜索「{searchQuery}」{diffSearchScope !== 'all' && <span style={{ color: 'var(--accent, #3b82f6)' }}> · {diffSearchScope === 'field' ? '字段' : diffSearchScope === 'before' ? '前值' : '后值'}范围</span>}</>}{''}
                      </span>
                      {searchQuery && totalMatched > 0 && (
                        <span
                          style={{
                            fontSize: 10, padding: '0 4px', borderRadius: 2,
                            background: 'var(--accent-dim, rgba(59,130,246,0.15))',
                            color: 'var(--accent, #3b82f6)',
                            display: 'inline-flex', alignItems: 'center', gap: 2,
                          }}
                          title="命中计数（Enter / Shift+Enter 或 ▲▼ 循环切换；命中位置自动滚动到视口）· 当前显示：光标位置/总命中项数（组数 · 项数）"
                        >
                          {(() => {
                            // 命中分布：搜索激活时统计有多少分组被命中，便于快速判断分散度
                            const hitGroups = searchQuery ? groups.length : 0;
                            const showDistribution = hitGroups > 1;
                            return (
                              <>
                                {diffHitCursor >= 0 ? (
                                  <>{diffHitCursor + 1}/{totalMatched}</>
                                ) : (
                                  <span style={{ color: 'var(--muted)', fontSize: 9 }}>▶ 待选</span>
                                )}
                                {showDistribution && (
                                  <span
                                    style={{
                                      marginLeft: 3, paddingLeft: 3,
                                      borderLeft: '1px solid rgba(59,130,246,0.35)',
                                      fontSize: 9, opacity: 0.85,
                                    }}
                                    title={`${hitGroups} 组命中，共 ${totalMatched} 项；命中越分散（组多）越难一次扫完`}
                                  >· {hitGroups} 组 · {totalMatched} 项</span>
                                )}
                              </>
                            );
                          })()}
                          <button
                            onClick={() => navDiffHit(-1)}
                            style={{ border: 'none', background: 'transparent', cursor: 'pointer', padding: '0 3px', color: 'var(--accent, #3b82f6)' }}
                            title="上一个命中（Shift+Enter）"
                          >▲</button>
                          <button
                            onClick={() => navDiffHit(1)}
                            style={{ border: 'none', background: 'transparent', cursor: 'pointer', padding: '0 3px', color: 'var(--accent, #3b82f6)' }}
                            title="下一个命中（Enter）"
                          >▼</button>
                        </span>
                      )}
                      <span style={{ flex: 1 }} />
                      {groups.length > 0 && (
                        <button
                          onClick={() => {
                            if (allCollapsed) setCollapsedGroups(new Set());
                            else setCollapsedGroups(new Set(groups.map((g) => g.group)));
                          }}
                          style={{ ...styles.btnSm, padding: '1px 6px', color: 'var(--muted)', fontSize: 10 }}
                          title={allCollapsed ? '展开所有分组' : '折叠所有分组'}
                        >{allCollapsed ? '🔽 全部展开' : '🔼 全部折叠'}</button>
                      )}
                      {collapsedGroups.size > 0 && (
                        <button
                          onClick={
                            () => {
                              try { localStorage.removeItem('polydb.diffCollapsedGroups.v1'); } catch { /* ignore */ }
                              setCollapsedGroups(new Set());
                            }
                          }
                          style={{ ...styles.btnSm, padding: '1px 6px', color: 'var(--muted)', fontSize: 10 }}
                          title="清除 localStorage 中的分组折叠偏好（当前展开所有分组，且不再记住此偏好）"
                        >🗑 清空偏好 ({collapsedGroups.size})</button>
                      )}
                      {diffItems.length > 0 && (
                        <button
                          onClick={() => void copyDiffSummary(diffItems)}
                          style={{ ...styles.btnSm, padding: '1px 8px', color: 'var(--accent, #3b82f6)' }}
                          title="复制当前撤销 diff 到剪贴板（Markdown 格式）"
                        >📋 复制 ({diffItems.length})</button>
                      )}
                      {diffItems.length > 0 && (
                        <button
                          onClick={() => void copyAllItems(diffItems)}
                          style={{ ...styles.btnSm, padding: '1px 6px', color: 'var(--muted)', background: 'transparent', border: '1px solid var(--border)', fontSize: 10 }}
                          title="按分组（分组标题作为二级标题，组内 items 缩进）复制到剪贴板，Markdown 格式"
                        >📋 分组</button>
                      )}
                    </div>
                    {/* Diff 概览（M30.37）：字段分布 top5 + 分组/可见项统计 */}
                    {diffItems.length > 0 && (() => {
                      const fieldCount = new Map<string, number>();
                      for (const it of diffItems) {
                        fieldCount.set(it.field, (fieldCount.get(it.field) ?? 0) + 1);
                      }
                      const topFields = Array.from(fieldCount.entries())
                        .sort((a, b) => b[1] - a[1])
                        .slice(0, 5);
                      const topMax = topFields[0]?.[1] ?? 1;
                      const collapsedCount = groups.filter((g) => collapsedGroups.has(g.group)).length;
                      const visibleItems = groups.reduce((s, g) => s + (collapsedGroups.has(g.group) ? 0 : g.items.length), 0);
                      return (
                        <div
                          style={{
                            display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
                            fontSize: 10, color: 'var(--muted)',
                            padding: '2px 6px', marginBottom: 4,
                            background: 'rgba(255,255,255,0.02)',
                            borderRadius: 2,
                          }}
                          title="按字段聚合的变更分布（top 5） · 分组/可见项统计"
                        >
                          <span style={{ color: 'var(--text)', opacity: 0.7 }}>📊 概览</span>
                          <span>字段分布：</span>
                          {topFields.map(([f, c]) => {
                            const isActive = diffSearch.trim() === f;
                            return (
                              <span
                                key={f}
                                style={{
                                  fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                  display: 'inline-flex', alignItems: 'center', gap: 3,
                                  cursor: 'pointer',
                                  padding: '1px 4px',
                                  borderRadius: 2,
                                  background: isActive ? 'var(--accent-dim, rgba(59,130,246,0.15))' : 'transparent',
                                  outline: isActive ? '1px solid var(--accent, #3b82f6)' : '1px solid transparent',
                                  transition: 'background 0.15s ease',
                                }}
                                title={`${f}: ${c} 项变更${isActive ? '（当前过滤中，再点一次取消）' : '（点击按此字段过滤）'}`}
                                onClick={() => {
                                  if (isActive) {
                                    setDiffSearch('');
                                    setDiffCursor(-1);
                                    setDiffHitCursor(-1);
                                  } else {
                                    // 若该字段所在分组被折叠，先展开以便下方 items 可见
                                    const containingGroups = groups
                                      .filter((g) => g.items.some((it) => it.field === f))
                                      .map((g) => g.group);
                                    if (containingGroups.some((g) => collapsedGroups.has(g))) {
                                      setCollapsedGroups((prev) => {
                                        const next = new Set(prev);
                                        for (const g of containingGroups) next.delete(g);
                                        return next;
                                      });
                                    }
                                    setDiffSearch(f);
                                    setDiffSearchScope('field');
                                    setDiffCursor(-1);
                                    setDiffHitCursor(0);
                                    requestAnimationFrame(() => {
                                      diffSearchInputRef.current?.focus();
                                    });
                                  }
                                }}
                              >
                                <span
                                  style={{
                                    display: 'inline-block',
                                    height: 8, width: Math.max(4, Math.round((c / topMax) * 40)),
                                    background: 'var(--accent, #3b82f6)',
                                    borderRadius: 1, opacity: isActive ? 1 : 0.75,
                                  }}
                                />
                                {f.length > 16 ? f.slice(0, 15) + '…' : f}
                                <b style={{ color: 'var(--text)' }}>{c}</b>
                              </span>
                            );
                          })}
                          <span style={{ flex: 1 }} />
                          <span>
                            {groups.length} 组
                            {collapsedCount > 0 && <span style={{ color: 'var(--warn, #d97706)' }}> · {collapsedCount} 折叠</span>}
                            {' · '}{visibleItems} 项可见
                          </span>
                        </div>
                      );
                    })()}
                    <div style={{ display: 'flex', alignItems: 'center', gap: 4, marginBottom: 4, position: 'relative' }}>
                      <div style={{ flex: 1, position: 'relative' }}>
                      <input
                        ref={diffSearchInputRef}
                        type="text"
                        value={diffSearch}
                        onFocus={() => {
                          if (diffSearchHistory.length > 0) {
                            setDiffSearchHistoryOpen(true);
                            setDiffSearchHistoryCursor(-1);
                          }
                        }}
                        onBlur={() => { setDiffSearchHistoryOpen(false); setDiffSearchHistoryCursor(-1); }}
                        onChange={(e) => {
                          const v = e.target.value;
                          setDiffSearch(v);
                          setDiffCursor(-1);
                          // 关键字变化时重置历史光标；空输入时下拉已空（不显示），非空时若还有历史项则保持下拉
                          setDiffSearchHistoryCursor(-1);
                          setDiffSearchHistoryOpen(v === '' && diffSearchHistory.length > 0);
                        }}
                        onKeyDown={(e) => {
                          if (e.key === 'Escape') {
                            e.stopPropagation();
                            e.preventDefault();
                            commitDiffSearchHistory(diffSearch);
                            setDiffSearch('');
                            setDiffCursor(-1);
                            setDiffHitCursor(-1);
                            setDiffSearchHistoryOpen(false);
                            setDiffSearchHistoryCursor(-1);
                            (e.target as HTMLInputElement).blur();
                            return;
                          }
                          // 历史下拉打开时，↑/↓ 选择历史项，Enter 采用
                          if (diffSearchHistoryOpen && diffSearchHistory.length > 0) {
                            if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
                              e.preventDefault();
                              e.stopPropagation();
                              const delta = e.key === 'ArrowDown' ? 1 : -1;
                              setDiffSearchHistoryCursor((cur) => {
                                const n = diffSearchHistory.length;
                                const next = cur < 0
                                  ? (delta > 0 ? 0 : n - 1)
                                  : (cur + delta + n) % n;
                                setDiffSearch(diffSearchHistory[next] ?? '');
                                return next;
                              });
                              return;
                            }
                            if (e.key === 'Enter' && !e.shiftKey) {
                              e.preventDefault();
                              e.stopPropagation();
                              const pick = diffSearchHistoryCursor >= 0
                                ? diffSearchHistory[diffSearchHistoryCursor]
                                : diffSearch;
                              if (pick) commitDiffSearchHistory(pick);
                              setDiffSearch(pick ?? '');
                              setDiffSearchHistoryOpen(false);
                              setDiffSearchHistoryCursor(-1);
                              // 首次搜索自动定位由 useEffect 处理；导航由 M30.30 的 navDiffHit 走
                              if (diffHitCursor < 0) navDiffHit(1);
                              return;
                            }
                            if (e.key === 'Enter' && e.shiftKey) {
                              e.preventDefault();
                              e.stopPropagation();
                              commitDiffSearchHistory(diffSearch);
                              setDiffSearchHistoryOpen(false);
                              setDiffSearchHistoryCursor(-1);
                              return;
                            }
                          }
                          if (e.key === 'Enter' && diffSearch.trim()) {
                            e.preventDefault();
                            e.stopPropagation();
                            commitDiffSearchHistory(diffSearch);
                            navDiffHit(e.shiftKey ? -1 : 1);
                            return;
                          }
                        }}
                        placeholder="🔍 搜索字段/值（空格或 | 分隔多关键字 OR；Ctrl+F 聚焦，Enter/Shift+Enter 循环，Esc 清除，↑↓ 历史）"
                        style={{
                          flex: 1, width: '100%', fontSize: 11, padding: '2px 6px',
                          background: 'var(--panel)', border: '1px solid var(--border)',
                          borderRadius: 3, color: 'var(--text)',
                          fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                          outline: 'none', boxSizing: 'border-box',
                        }}
                      />
                      {(() => {
                        // 内嵌命中数徽章：搜索非空时绝对定位显示在输入框右上角，与 diff 面板折叠与否无关
                        const total = searchQuery ? (() => {
                          const g = filterDiffGroups(groupDiffItems(diffItems));
                          return g.reduce((s, x) => s + x.items.length, 0);
                        })() : 0;
                        if (!searchQuery) return null;
                        return (
                          <span
                            style={{
                              position: 'absolute', top: -3, right: -3,
                              padding: '0 4px', fontSize: 9, lineHeight: '14px',
                              background: total > 0 ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                              color: total > 0 ? 'white' : 'var(--text)',
                              borderRadius: 2, fontWeight: 700,
                              pointerEvents: 'none',
                              boxShadow: '0 1px 2px rgba(0,0,0,0.3)',
                            }}
                            title={`搜索命中：${total} 项${diffSearchScope !== 'all' ? ` · ${diffSearchScope === 'field' ? '字段' : diffSearchScope === 'before' ? '前值' : '后值'}范围` : ''}`}
                          >{total}</span>
                        );
                      })()}
                      </div>
                      <button
                        onClick={() => setDiffSearchCaseSensitive((v) => !v)}
                        onMouseDown={(e) => { e.preventDefault(); diffSearchInputRef.current?.focus(); }}
                        style={{
                          ...styles.btnSm, padding: '1px 5px', fontSize: 10, minWidth: 22,
                          color: diffSearchCaseSensitive ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                          background: diffSearchCaseSensitive ? 'var(--accent-dim, rgba(59,130,246,0.15))' : 'transparent',
                          border: '1px solid var(--border)',
                          fontWeight: 700,
                        }}
                        title={diffSearchCaseSensitive ? '大小写敏感：开启（点击或 Shift+Alt+C 切换）' : '大小写敏感：关闭（点击或 Shift+Alt+C 切换）'}
                      >Aa</button>
                      <span style={{ fontSize: 9, color: 'var(--muted)', marginLeft: 2 }}>范围</span>
                      {([
                        { key: 'all', label: '全部', tip: 'field/before/after 任一命中' },
                        { key: 'field', label: '字段', tip: '仅匹配 field（如"转换 第 3 列"）' },
                        { key: 'before', label: '前值', tip: '仅匹配 before（撤销前当前值）' },
                        { key: 'after', label: '后值', tip: '仅匹配 after（撤销后目标值）' },
                      ] as { key: DiffSearchScope; label: string; tip: string }[]).map((s) => (
                        <button
                          key={s.key}
                          onClick={() => setDiffSearchScope(s.key)}
                          onMouseDown={(e) => { e.preventDefault(); diffSearchInputRef.current?.focus(); }}
                          style={{
                            ...styles.btnSm, padding: '1px 5px', fontSize: 9,
                            color: diffSearchScope === s.key ? 'var(--accent, #3b82f6)' : 'var(--muted)',
                            background: diffSearchScope === s.key ? 'var(--accent-dim, rgba(59,130,246,0.15))' : 'transparent',
                            border: '1px solid var(--border)',
                            fontWeight: diffSearchScope === s.key ? 700 : 400,
                          }}
                          title={s.tip}
                        >{s.label}</button>
                      ))}
                      {diffSearch && (
                        <button
                          onClick={() => { commitDiffSearchHistory(diffSearch); setDiffSearch(''); setDiffCursor(-1); setDiffSearchHistoryOpen(false); setDiffSearchHistoryCursor(-1); diffSearchInputRef.current?.focus(); }}
                          style={{ ...styles.btnSm, padding: '1px 6px', color: 'var(--muted)', fontSize: 10 }}
                          title="清除搜索（Esc；已加入历史）"
                        >✕ 清除</button>
                      )}
                      {groups.length > 0 && (
                        <span style={{ fontSize: 10, color: 'var(--muted)' }}>
                          Alt+↑/↓ 跳组 · Alt+Enter 切换折叠 · Shift+Alt+C 大小写
                        </span>
                      )}
                      {diffSearchHistoryOpen && diffSearchHistory.length > 0 && (
                        <div style={{
                          position: 'absolute', top: 'calc(100% + 2px)', left: 0, right: 0,
                          background: 'var(--panel, #1e293b)', border: '1px solid var(--border)',
                          borderRadius: 3, zIndex: 20, maxHeight: 180, overflowY: 'auto',
                          boxShadow: '0 4px 12px rgba(0,0,0,0.3)',
                        }}>
                          <div style={{ padding: '2px 6px', fontSize: 9, color: 'var(--muted)', borderBottom: '1px solid var(--border)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                            <span>📜 搜索历史（↑/↓ 选择，Enter 采用）</span>
                            <button
                              onMouseDown={(e) => {
                                e.preventDefault();
                                e.stopPropagation();
                                setDiffSearchHistory([]);
                                try { localStorage.removeItem('polydb.diffSearchHistory.v1'); } catch { /* ignore */ }
                                setDiffSearchHistoryOpen(false);
                                setDiffSearchHistoryCursor(-1);
                              }}
                              style={{ ...styles.btnSm, padding: '0 4px', fontSize: 9, color: 'var(--muted)', background: 'transparent', border: '1px solid var(--border)' }}
                              title="清空搜索历史"
                            >🗑 清空</button>
                          </div>
                          {diffSearchHistory.map((q, i) => (
                            <div
                              key={q}
                              onMouseDown={(e) => {
                                e.preventDefault();
                                e.stopPropagation();
                                setDiffSearch(q);
                                setDiffSearchHistoryOpen(false);
                                setDiffSearchHistoryCursor(-1);
                                diffSearchInputRef.current?.focus();
                              }}
                              style={{
                                padding: '2px 8px', fontSize: 11, cursor: 'pointer',
                                background: i === diffSearchHistoryCursor
                                  ? 'var(--accent-dim, rgba(59,130,246,0.18))'
                                  : 'transparent',
                                color: 'var(--text)',
                                fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                display: 'flex', alignItems: 'center', gap: 6,
                                overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                              }}
                            >
                              <span style={{ color: 'var(--muted)', fontSize: 9, flexShrink: 0 }}>{i === 0 ? '◀' : '·'}</span>
                              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{q}</span>
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                    {(() => {
                      // 多关键字 chip 展示：当 diffSearch 内含多个关键字（空格或 | 分隔）时行内提示
                      const kws = searchQuery ? splitDiffSearchKeywords(searchQuery) : [];
                      if (kws.length <= 1) return null;
                      const caseActive = diffSearchCaseSensitive;
                      // 删除某个 keyword 后重组 diffSearch：剩余关键字用空格 join
                      const removeKeyword = (idx: number) => {
                        const rest = kws.filter((_, i) => i !== idx);
                        setDiffSearch(rest.join(' '));
                        diffSearchInputRef.current?.focus();
                      };
                      // 当前命中项触发了哪些关键字（M30.35）：把 flat[diffHitCursor] 各字段拆段汇总 kwIdx Set
                      const activeSet = new Set<number>();
                      if (diffHitCursor >= 0) {
                        const flat = getVisibleDiffItems();
                        const cur = flat[diffHitCursor];
                        if (cur && searchQuery) {
                          const s = diffSearchCaseSensitive ? cur.item.field : cur.item.field.toLowerCase();
                          const b = diffSearchCaseSensitive ? cur.item.before : cur.item.before.toLowerCase();
                          const a = diffSearchCaseSensitive ? cur.item.after : cur.item.after.toLowerCase();
                          const kwsNorm = kws.map((k) => diffSearchCaseSensitive ? k : k.toLowerCase());
                          for (let ki = 0; ki < kwsNorm.length; ki++) {
                            if (s.includes(kwsNorm[ki]) || b.includes(kwsNorm[ki]) || a.includes(kwsNorm[ki])) {
                              activeSet.add(ki);
                            }
                          }
                        }
                      }
                      return (
                        <div style={{ display: 'flex', alignItems: 'center', gap: 4, marginBottom: 4, flexWrap: 'wrap', fontSize: 10 }}>
                          <span style={{ color: 'var(--muted)' }}>OR:</span>
                          {kws.map((kw, i) => {
                            const col = diffKeywordColor(i);
                            const isActive = activeSet.has(i);
                            return (
                            <span
                              key={`${i}-${kw}`}
                              style={{
                                display: 'inline-flex', alignItems: 'center', gap: 3,
                                padding: '0 3px 0 5px', borderRadius: 2,
                                background: col.bg,
                                color: col.fg,
                                fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                fontSize: 10,
                                // 大小写敏感态：chip 边框加重 + 强调色，让用户一眼看出当前搜索模式
                                border: caseActive
                                  ? `1px solid ${col.fg}`
                                  : '1px solid transparent',
                                boxShadow: caseActive ? `0 0 0 1px ${col.bg}` : 'none',
                                // 当前命中项触发该关键字：额外发光环强调
                                ...(isActive ? {
                                  outline: `2px solid ${col.fg}`,
                                  outlineOffset: 1,
                                  fontWeight: 700,
                                } : {}),
                              }}
                              title={caseActive
                                ? `关键字 #${i + 1}：「${kw}」（大小写敏感；点击 ✕ 删除${isActive ? '；当前命中项包含此关键字' : ''}）`
                                : `关键字 #${i + 1}：「${kw}」（任一命中即保留；点击 ✕ 删除${isActive ? '；当前命中项包含此关键字' : ''}）`}
                            >
                              <span>{kw}</span>
                              {kws.length > 1 && (
                                <button
                                  onClick={(e) => { e.preventDefault(); e.stopPropagation(); removeKeyword(i); }}
                                  onMouseDown={(e) => e.stopPropagation()}
                                  style={{
                                    border: 'none', background: 'transparent',
                                    cursor: 'pointer', padding: '0 2px',
                                    color: 'var(--muted)', fontSize: 10, lineHeight: 1,
                                    borderRadius: 1,
                                  }}
                                  title="点击删除此关键字"
                                >✕</button>
                              )}
                            </span>
                            );
                          })}
                          <span style={{ color: 'var(--muted)', fontSize: 9 }}>
                            共 {kws.length} 关键字{caseActive ? ' · Aa 大小写敏感' : ''}（空白或 | 分隔）
                          </span>
                        </div>
                      );
                    })()}
                    {diffItems.length === 0 ? (
                      <div style={{ ...styles.muted, fontStyle: 'italic' }}>（当前配置与撤销后完全一致，撤销无实际效果）</div>
                    ) : groups.length === 0 ? (
                      <div style={{ ...styles.muted, fontStyle: 'italic' }}>（搜索「{searchQuery}」无匹配项，按 Esc 或点 ✕ 清除）</div>
                    ) : (
                      <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                        {groups.map((grp, gi) => {
                          const collapsed = collapsedGroups.has(grp.group);
                          const isCursor = gi === diffCursor;
                          return (
                            <div key={grp.group} style={isCursor ? { outline: '1px solid var(--accent, #3b82f6)', borderRadius: 2 } : undefined}>
                              <div
                                onClick={() => {
                                  setCollapsedGroups((prev) => {
                                    const next = new Set(prev);
                                    if (next.has(grp.group)) next.delete(grp.group);
                                    else next.add(grp.group);
                                    return next;
                                  });
                                  setDiffCursor(gi);
                                }}
                                style={{
                                  display: 'flex', alignItems: 'center', gap: 6, padding: '2px 4px',
                                  cursor: 'pointer', userSelect: 'none', borderRadius: 2,
                                  background: isCursor ? 'var(--accent-dim, rgba(59,130,246,0.10))' : 'rgba(255,255,255,0.03)',
                                  borderLeft: `2px solid ${diffGroupColor(grp.group)}`,
                                }}
                                title={collapsed ? '展开此分组' : '折叠此分组'}
                              >
                                <span style={{ color: 'var(--muted)', fontSize: 10, minWidth: 12, textAlign: 'center', flexShrink: 0 }}>
                                  {collapsed ? '▶' : '▼'}
                                </span>
                                {(() => {
                                  // 分组标题关键字高亮（M30.33）：与 items 内一致用 splitDiffSearchParts
                                  const parts = searchQuery
                                    ? splitDiffSearchParts(grp.group, searchQuery, diffSearchCaseSensitive)
                                    : [{ text: grp.group, hit: false }];
                                  return (
                                    <span style={{ color: diffGroupColor(grp.group), fontWeight: 600 }}>
                                      {parts.map((p, pi) => {
                                        if (!p.hit) return p.text;
                                        const col = diffKeywordColor(p.kwIdx ?? 0);
                                        return <mark key={pi} style={{ background: col.bg, color: col.fg, padding: 0, borderRadius: 1 }}>{p.text}</mark>;
                                      })}
                                    </span>
                                  );
                                })()}
                                <span style={{ fontSize: 10, color: 'var(--muted)', background: 'rgba(255,255,255,0.05)', padding: '0 4px', borderRadius: 2 }}>
                                  {grp.items.length}
                                </span>
                                <button
                                  onClick={(e) => { e.stopPropagation(); void copyDiffSummary(grp.items, grp.group); }}
                                  style={{
                                    ...styles.btnSm, padding: '0 5px', fontSize: 10,
                                    color: 'var(--muted)', background: 'transparent',
                                    border: '1px solid var(--border)',
                                    marginLeft: 'auto', flexShrink: 0,
                                  }}
                                  title={`复制「${grp.group}」分组的 ${grp.items.length} 项变化到剪贴板（Markdown 格式）`}
                                >📋</button>
                              </div>
                              {!collapsed && grp.items.map((it, ii) => {
                                const kind = classifyDiffItem(it);
                                const kindColor = diffItemColor(kind);
                                const kindBg = kind === 'added' ? 'rgba(16,185,129,0.10)'
                                  : kind === 'removed' ? 'rgba(220,38,38,0.10)'
                                  : 'rgba(245,158,11,0.08)';
                                const flatIdx = (visiblePrefix.get(grp.group) ?? -1) + ii;
                                const isHit = !!searchQuery && flatIdx === diffHitCursor;
                                // 关键字高亮：仅在搜索非空时把 before/after/field 拆成命中/非命中段
                                const fieldDisplay = it.field.startsWith(grp.group + ' ') ? it.field.slice(grp.group.length + 1) : it.field;
                                const partsField = searchQuery
                                  ? splitDiffSearchParts(fieldDisplay, searchQuery, diffSearchCaseSensitive)
                                  : [{ text: fieldDisplay, hit: false }];
                                const partsBefore = searchQuery
                                  ? splitDiffSearchParts(it.before, searchQuery, diffSearchCaseSensitive)
                                  : [{ text: it.before, hit: false }];
                                const partsAfter = searchQuery
                                  ? splitDiffSearchParts(it.after, searchQuery, diffSearchCaseSensitive)
                                  : [{ text: it.after, hit: false }];
                                return (
                                  <div
                                    key={it.field}
                                    ref={getDiffItemRef(grp.group, it.field)}
                                    style={{
                                      display: 'flex', gap: 6, padding: '1px 4px 1px 22px', alignItems: 'baseline',
                                      borderRadius: 2,
                                      background: isHit ? 'var(--accent-dim, rgba(59,130,246,0.18))' : 'transparent',
                                      boxShadow: isHit ? 'inset 0 0 0 1px var(--accent, #3b82f6)' : 'none',
                                    }}
                                    title={`[${diffItemLabel(kind)}] ${it.field}: ${it.before} → ${it.after}`}
                                  >
                                    <span
                                      style={{
                                        color: kindColor, minWidth: 14, textAlign: 'center',
                                        flexShrink: 0, fontSize: 10, fontWeight: 700,
                                        background: kindBg, padding: '0 3px', borderRadius: 2,
                                      }}
                                      title={diffItemLabel(kind)}
                                    >{diffItemIcon(kind)}</span>
                                    <span style={{ color: 'var(--muted, #6b7280)', flexShrink: 0, minWidth: 140, wordBreak: 'break-word', fontSize: 10 }}>
                                      {partsField.map((p, pi) => p.hit
                                        ? <mark key={pi} style={{ background: diffKeywordColor(p.kwIdx ?? 0).bg, color: diffKeywordColor(p.kwIdx ?? 0).fg, padding: 0, borderRadius: 1 }}>{p.text}</mark>
                                        : <span key={pi}>{p.text}</span>)}
                                    </span>
                                    <span style={{
                                      color: 'var(--danger, #dc2626)', background: 'rgba(220,38,38,0.10)',
                                      padding: '0 4px', borderRadius: 2,
                                      overflow: 'hidden', textOverflow: 'ellipsis',
                                      textDecoration: kind === 'removed' ? 'line-through' : 'none',
                                    }}>{partsBefore.map((p, pi) => p.hit
                                      ? <mark key={pi} style={{ background: diffKeywordColor(p.kwIdx ?? 0).bg, color: diffKeywordColor(p.kwIdx ?? 0).fg, padding: 0, borderRadius: 1 }}>{p.text}</mark>
                                      : <span key={pi}>{p.text}</span>)}</span>
                                    <span style={{ color: 'var(--muted, #6b7280)', flexShrink: 0 }}>→</span>
                                    <span style={{
                                      color: 'var(--accent, #3b82f6)', background: 'rgba(59,130,246,0.10)',
                                      padding: '0 4px', borderRadius: 2,
                                      overflow: 'hidden', textOverflow: 'ellipsis',
                                      fontStyle: kind === 'added' ? 'normal' : 'inherit',
                                      fontWeight: kind === 'added' ? 600 : 'inherit',
                                    }}>{partsAfter.map((p, pi) => p.hit
                                      ? <mark key={pi} style={{ background: diffKeywordColor(p.kwIdx ?? 0).bg, color: diffKeywordColor(p.kwIdx ?? 0).fg, padding: 0, borderRadius: 1 }}>{p.text}</mark>
                                      : <span key={pi}>{p.text}</span>)}</span>
                                    {(() => {
                                      const canRevert = canRevertSingleItem(it, targetSnap);
                                      return (
                                        <button
                                          onClick={(e) => {
                                            e.stopPropagation();
                                            if (!canRevert) return;
                                            applySingleItemUndo(it, targetSnap);
                                          }}
                                          disabled={!canRevert}
                                          style={{
                                            ...styles.btnSm, padding: '0 4px', fontSize: 10,
                                            color: canRevert ? 'var(--muted)' : 'var(--muted)',
                                            background: 'transparent',
                                            border: '1px solid var(--border)',
                                            opacity: canRevert ? 1 : 0.35,
                                            cursor: canRevert ? 'pointer' : 'not-allowed',
                                            marginLeft: 'auto', flexShrink: 0,
                                          }}
                                          title={canRevert
                                            ? `↩️ 单项撤销：把「${it.field}」从「${it.before || '∅'}」恢复到「${it.after || '∅'}」（生成合成 FixEntry 压栈，可再 Ctrl+Z 撤销这次撤销）`
                                            : `⚠️ 不可单项撤销「${it.field}」：字段结构复杂（如 regex/enum 已截断），请用全撤销或 Step 3 手动调整`}
                                        >{canRevert ? '↩' : '↩⚠'}</button>
                                      );
                                    })()}
                                  </div>
                                );
                              })}
                            </div>
                          );
                        })}
                      </div>
                    )}
                  </div>
                );
              })()}
              {failedRows.length > 0 && (
                <div style={{ marginTop: 6 }}>
                  <div style={{ ...styles.label, display: 'flex', alignItems: 'center', gap: 8 }}>
                    <span>失败行（{failedRows.length}）</span>
                    {failCategoryFilter && (
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                        <span>{failCategoryMeta(failCategoryFilter).icon}</span>
                        <span style={{ ...styles.muted, fontSize: 11 }}>
                          按「{failureSummary.find((s) => s.key === failCategoryFilter)?.label ?? failCategoryFilter}」筛选，显示 {filteredFailedRows.length} / {failedRows.length}
                        </span>
                      </span>
                    )}
                    {failCategoryFilter && (
                      <button
                        onClick={() => { setFailCategoryFilter(null); setExpandedFailRow(null); }}
                        style={{ ...styles.btnSm, padding: '1px 6px', marginLeft: 'auto', fontSize: 10 }}
                        title="清除筛选"
                      >✕ 清除筛选</button>
                    )}
                  </div>
                  {/* 失败摘要行（M30.42）：总数占比 + 主导失败类 + 平均耗时 */}
                  {(() => {
                    const total = progress?.total ?? 0;
                    const failPct = total > 0 ? (failedRows.length / total) * 100 : 0;
                    const top = failureSummary[0];
                    const avgMs = (progress?.ms ?? 0) > 0 ? Math.round((progress?.ms ?? 0) / failedRows.length) : 0;
                    if (failedRows.length === 0) return null;
                    return (
                      <div
                        style={{
                          ...styles.muted, fontSize: 11, marginTop: 4,
                          display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'center',
                          padding: '4px 8px', background: 'rgba(255,255,255,0.02)',
                          border: '1px solid var(--border)', borderRadius: 3,
                        }}
                        title="失败行摘要：占总行数比例 + 主导失败类 + 平均单条耗时"
                      >
                        <span title="失败行占总行数比例">失败率 <strong style={{ color: failPct >= 10 ? 'var(--danger, #dc2626)' : failPct >= 1 ? 'var(--warn, #d97706)' : 'var(--success, #10b981)' }}>{failPct.toFixed(1)}%</strong>（{failedRows.length} / {total}）</span>
                        {top && (
                          <span title={`${top.hint ?? ''}`}>
                            主导类 <span style={{ color: failCategoryMeta(top.key).color, fontWeight: 600 }}>{failCategoryMeta(top.key).icon} {top.label}</span>（{top.count} 行，占 {(top.count / failedRows.length * 100).toFixed(0)}%）
                          </span>
                        )}
                        {avgMs > 0 && (
                          <span title="失败行的平均单条耗时（ms）">平均 <strong>{avgMs}</strong> ms/失败行</span>
                        )}
                      </div>
                    );
                  })()}
                  {/* 失败分类堆叠条：按分类比例分段着色，hover 显示各类数量 + 提示，点击按该类筛选 */}
                  {failureSummary.length > 1 && (() => {
                    const total = failureSummary.reduce((s, x) => s + x.count, 0);
                    return (
                      <div
                        style={{
                          display: 'flex', height: 14, borderRadius: 3, overflow: 'hidden',
                          background: 'rgba(255,255,255,0.03)', border: '1px solid var(--border)',
                        }}
                        title="失败分类占比（点击按类筛选）"
                      >
                        {failureSummary.map((s) => {
                          const meta = failCategoryMeta(s.key);
                          const pct = total > 0 ? (s.count / total) * 100 : 0;
                          const isActive = failCategoryFilter === s.key;
                          return (
                            <div
                              key={s.key}
                              style={{
                                flex: `1 1 ${Math.max(pct, 1)}%`, height: '100%',
                                minWidth: 20,
                                background: meta.color,
                                cursor: 'pointer',
                                opacity: failCategoryFilter && !isActive ? 0.4 : 1,
                                outline: isActive ? '2px solid #fff' : 'none',
                                outlineOffset: -2,
                                transition: 'opacity 0.15s ease',
                              }}
                              title={`${meta.icon} ${meta.label}：${s.count} 行 (${pct.toFixed(1)}%)${s.hint ? ` · ${s.hint}` : ''}${isActive ? ' · 再点取消筛选' : ' · 点击筛选'}`}
                              onClick={() => {
                                setFailCategoryFilter(isActive ? null : s.key);
                                setExpandedFailRow(null);
                              }}
                            />
                          );
                        })}
                      </div>
                    );
                  })()}
                  {/* 分类图例（M30.42）：堆叠条下方一行紧凑显示 icon · label · count · 百分比 */}
                  {failureSummary.length > 0 && (() => {
                    const total = failureSummary.reduce((s, x) => s + x.count, 0);
                    return (
                      <div
                        style={{ display: 'flex', gap: 10, flexWrap: 'wrap', marginTop: 3, fontSize: 11 }}
                        title="各类失败占比图例"
                      >
                        {failureSummary.map((s) => {
                          const meta = failCategoryMeta(s.key);
                          const pct = total > 0 ? (s.count / total) * 100 : 0;
                          const isActive = failCategoryFilter === s.key;
                          return (
                            <span
                              key={s.key}
                              style={{
                                display: 'inline-flex', alignItems: 'center', gap: 4,
                                padding: '1px 6px', borderRadius: 3, cursor: 'pointer',
                                border: `1px solid ${isActive ? meta.color : 'var(--border)'}`,
                                background: isActive ? `${meta.color}22` : 'transparent',
                                opacity: failCategoryFilter && !isActive ? 0.55 : 1,
                              }}
                              title={`${meta.icon} ${meta.label}：${s.count} 行 (${pct.toFixed(1)}%)${s.hint ? ` · ${s.hint}` : ''}`}
                              onClick={() => {
                                setFailCategoryFilter(isActive ? null : s.key);
                                setExpandedFailRow(null);
                              }}
                            >
                              <span>{meta.icon}</span>
                              <span style={{ color: meta.color, fontWeight: 600 }}>{s.label}</span>
                              <span style={styles.muted}>· {s.count} · {pct.toFixed(1)}%</span>
                            </span>
                          );
                        })}
                      </div>
                    );
                  })()}
                  <div style={styles.failedWrap}>
                    <table style={styles.failedTable}>
                      <thead>
                        <tr>
                          <th style={styles.failedTh}>CSV 行</th>
                          <th style={styles.failedTh}>原因</th>
                          <th style={styles.failedTh}>参数预览</th>
                          <th style={{ ...styles.failedTh, width: 120 }}>操作</th>
                        </tr>
                      </thead>
                      <tbody>
                        {filteredFailedRows.slice(0, 50).map((f) => {
                          const cls = classifyFailReason(f.reason);
                          const raw = rawRowFor(f.csvRow);
                          return (
                            <Fragment key={f.csvRow}>
                              <tr>
                                <td
                                  style={{
                                    ...styles.failedTd,
                                    cursor: 'pointer',
                                    color: 'var(--accent, #3b82f6)',
                                    fontFamily: 'var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
                                    fontWeight: 600,
                                  }}
                                  title="点击跳回 Step 1 并定位到该行"
                                  onClick={() => jumpToCsvRow(f.csvRow)}
                                >L{f.csvRow}</td>
                                <td style={{ ...styles.failedTd, color: failCategoryMeta(cls.key).color }} title={f.reason}>
                                  <span style={{ marginRight: 4 }}>{failCategoryMeta(cls.key).icon}</span>
                                  <span>{f.reason}</span>
                                </td>
                                <td style={{ ...styles.failedTd, opacity: 0.75 }} title={f.preview}>{f.preview}</td>
                                <td style={styles.failedTd}>
                                  {inputFormat === 'sql' ? (
                                    <button
                                      onClick={() => setRetryIndices(new Set([f.stmtIdx]))}
                                      style={{ ...styles.btnSm, padding: '1px 6px' }}
                                      title={`只重跑第 ${f.csvRow} 条 SQL（先去 Step 1 修改原文）`}
                                    >重试</button>
                                  ) : (
                                    <>
                                      <button
                                        onClick={() => {
                                          const set = new Set<number>();
                                          filteredFailedRows.forEach((fr) => set.add(fr.stmtIdx));
                                          setRetryIndices(set);
                                          setStep('input');
                                        }}
                                        style={{ ...styles.btnSm, padding: '1px 6px' }}
                                        title={`只重跑可见失败行（当前显示 ${filteredFailedRows.length} 条；先去 Step 1 修改 CSV）`}
                                      >仅重试</button>
                                      {' '}
                                      <button
                                        onClick={() => {
                                          if (!parse) return;
                                          const prev = overrides[f.stmtIdx];
                                          const base = prev ?? parse.rows[f.csvRow] ?? [];
                                          setOverrides((o) => ({ ...o, [f.stmtIdx]: [...base] }));
                                        }}
                                        style={{ ...styles.btnSm, padding: '1px 6px' }}
                                        title="就地编辑该行的 CSV 值并重跑"
                                      >编辑并重跑</button>
                                    </>
                                  )}
                                  {' '}
                                  <button
                                    onClick={() => setExpandedFailRow(expandedFailRow === f.csvRow ? null : f.csvRow)}
                                    style={{ ...styles.btnSm, padding: '1px 6px' }}
                                    title="展开原始 CSV 行 + 分类标签 + 修复提示"
                                  >{expandedFailRow === f.csvRow ? '收起' : '详情'}</button>
                                </td>
                              </tr>
                              {expandedFailRow === f.csvRow && (
                                <tr>
                                  <td colSpan={4} style={{ padding: 0, background: 'var(--bg-secondary, rgba(0,0,0,0.03))' }}>
                                    <div style={{ padding: '6px 10px', display: 'grid', gridTemplateColumns: 'auto 1fr', gap: 6, fontSize: 11, fontFamily: 'monospace' }}>
                                      <span style={{ ...styles.muted }}>分类:</span>
                                      <span><strong>{failCategoryMeta(cls.key).icon} {cls.label}</strong> · <span style={{ color: failCategoryMeta(cls.key).color }}>{cls.hint}</span></span>
                                      {raw && (
                                        <>
                                          <span style={styles.muted}>原始 CSV:</span>
                                          <span title={raw.join(delimiter === ',' ? ', ' : ' │ ')} style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
                                            {raw.map((v, vi) => vi === 0 ? `<col#1> ${v}` : `<col#${vi + 1}> ${v}`).join(' │ ')}
                                          </span>
                                        </>
                                      )}
                                      <span style={styles.muted}>原因:</span>
                                      <span title={f.reason} style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{f.reason}</span>
                                    </div>
                                    <div style={{ padding: '4px 10px 6px', fontSize: 11, display: 'flex', gap: 6, alignItems: 'center' }}>
                                      <button
                                        onClick={() => setFailCategoryFilter(cls.key)}
                                        style={{ ...styles.btnSm, padding: '1px 6px' }}
                                        title="在下方行表只显示同一类的失败行"
                                      >🔍 只看同类失败</button>
                                      {inputFormat !== 'sql' && (
                                        <button
                                          onClick={() => {
                                            const r = applyCategoryFix(cls.key);
                                            publishEditorStatus({ message: r.ok ? `✅ ${r.msg}` : `ℹ️ ${r.msg}`, messageAt: Date.now() });
                                          }}
                                          style={{ ...styles.btnSm, padding: '1px 6px' }}
                                          title={`按分类「${cls.label}」应用建议修复`}
                                        >🔧 按分类一键修复</button>
                                      )}
                                    </div>
                                  </td>
                                </tr>
                              )}
                            </Fragment>
                          );
                        })}
                      </tbody>
                    </table>
                    {failedRows.length > 50 && (
                      <div style={{ ...styles.muted, textAlign: 'center', padding: '4px 0' }}>… 共 {failedRows.length} 行，仅显示前 50 行</div>
                    )}
                    {inputFormat !== 'sql' && failCategoryFilter && (
                      <div style={{ ...styles.mutedBox, marginTop: 6, borderLeft: `3px solid ${failCategoryMeta(failCategoryFilter).color}`, padding: '6px 10px', display: 'flex', alignItems: 'center', gap: 8, fontSize: 11 }}>
                        <span style={{ ...styles.muted }}>当前筛选「{failCategoryMeta(failCategoryFilter).icon} {failureSummary.find((s) => s.key === failCategoryFilter)?.label ?? failCategoryFilter}」：</span>
                        <button
                          onClick={() => {
                            const r = applyCategoryFix(failCategoryFilter);
                            publishEditorStatus({ message: r.ok ? `✅ ${r.msg}（影响 ${filteredFailedRows.length} 行）` : `ℹ️ ${r.msg}`, messageAt: Date.now() });
                          }}
                          style={styles.btnSm}
                          title="对当前筛选的所有失败行应用该类修复"
                        >🔧 应用修复</button>
                        <span style={styles.muted}>
                          {failCategoryFilter === 'dup' ? '将切到 Upsert 模式' : failCategoryFilter === 'type' ? '将应用质量建议转换' : failCategoryFilter === 'validation' ? '将关闭严格校验' : '请查看行详情获取修复指引'}
                        </span>
                      </div>
                    )}
                    {/* 失败原因聚合下钻（M30.40+）：按 reason 前 40 字符聚合，判断是同一批错还是分散错
                        点击条 = 复制 CSV 行号；Shift+点击 = 跳转 Step 1 CSV 首条；行末按钮 = 只重跑该组 */}
                    {filteredFailedRows.length >= 3 && (() => {
                      const counts = new Map<string, { reason: string; rows: number[] }>();
                      for (const f of filteredFailedRows) {
                        const key = f.reason.slice(0, 40);
                        const cur = counts.get(key);
                        if (cur) cur.rows.push(f.csvRow);
                        else counts.set(key, { reason: f.reason, rows: [f.csvRow] });
                      }
                      const groups = Array.from(counts.values()).sort((a, b) => b.rows.length - a.rows.length).slice(0, 5);
                      if (groups.length < 2) return null;
                      const topMax = groups[0].rows.length;
                      return (
                        <div
                          style={{ ...styles.mutedBox, marginTop: 6, padding: '6px 10px', fontSize: 11 }}
                          title="按失败原因前 40 字符聚合（top 5）：点击条复制 CSV 行号，Shift+点击跳转 Step 1 首条，行末按钮只重跑该组"
                        >
                          <div style={{ color: 'var(--text)', opacity: 0.7, marginBottom: 4 }}>🔬 原因聚合（{groups.length} 组，共 {filteredFailedRows.length} 行）</div>
                          <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                            {groups.map((g, i) => {
                              const pct = filteredFailedRows.length > 0 ? (g.rows.length / filteredFailedRows.length) * 100 : 0;
                              return (
                              <div
                                key={i}
                                style={{ display: 'flex', alignItems: 'center', gap: 6, fontFamily: 'monospace' }}
                                title={`点击复制对应 ${g.rows.length} 行 CSV 行号 · Shift+点击跳转首条 · 按钮只重跑该组`}
                                onClick={(e) => {
                                  if (e.shiftKey) {
                                    jumpToCsvRow(g.rows[0]);
                                    return;
                                  }
                                  void navigator.clipboard.writeText(g.rows.join(', ')).then(
                                    () => publishEditorStatus({ message: `📋 已复制 ${g.rows.length} 个 CSV 行号（Shift+点击可跳转到首条 L${g.rows[0]}）`, messageAt: Date.now() }),
                                    () => publishEditorStatus({ message: '❌ 复制失败（浏览器剪贴板权限）', messageAt: Date.now() }),
                                  );
                                }}
                              >
                                <span style={{ display: 'inline-block', width: 60, height: 6, background: 'rgba(255,255,255,0.06)', borderRadius: 2, position: 'relative', overflow: 'hidden' }}>
                                  <span style={{ position: 'absolute', left: 0, top: 0, height: '100%', width: `${Math.max(4, Math.round((g.rows.length / topMax) * 100))}%`, background: 'var(--accent, #3b82f6)' }} />
                                </span>
                                <span style={{ minWidth: 32, color: 'var(--warn, #d97706)', fontWeight: 600, cursor: 'pointer' }} title="点击复制行号列表">×{g.rows.length}</span>
                                <span style={{ minWidth: 38, color: 'var(--muted)', opacity: 0.85, textAlign: 'right' }} title="该组占筛选后失败总数的百分比">{pct.toFixed(1)}%</span>
                                <span style={{ flex: 1, color: 'var(--text)', opacity: 0.8, overflow: 'hidden', textOverflow: 'ellipsis' }} title={g.reason}>
                                  {g.reason.length > 40 ? g.reason.slice(0, 40) + '…' : g.reason}
                                </span>
                                <button
                                  onClick={(e) => {
                                    e.stopPropagation();
                                    const set = new Set<number>();
                                    filteredFailedRows.forEach((fr) => {
                                      if (fr.reason.slice(0, 40) === g.reason.slice(0, 40)) set.add(fr.stmtIdx);
                                    });
                                    setRetryIndices(set);
                                    setStep('input');
                                    publishEditorStatus({ message: `🎯 已标记 ${set.size} 行仅重跑该组（去 Step 1 修 CSV 后再跑）`, messageAt: Date.now() });
                                  }}
                                  style={{ ...styles.btnSm, padding: '1px 6px', cursor: 'pointer' }}
                                  title="仅重跑该组的失败行（先跳到 Step 1 修 CSV）"
                                >仅重跑</button>
                              </div>
                              );
                            })}
                          </div>
                        </div>
                      );
                    })()}
                  </div>
                  <div style={{ display: 'flex', gap: 6, marginTop: 6, alignItems: 'center' }}>
                    <button
                      onClick={() => {
                        const set = new Set<number>();
                        failedRows.forEach((f) => set.add(f.stmtIdx));
                        setRetryIndices(set);
                        setStep('input');
                      }}
                      style={styles.btn}
                      title="标记所有失败行，去 Step 1 修改 CSV 后仅重跑这些行"
                    >
                      🔄 全部失败行：修改 CSV 后只重跑
                    </button>
                    <button
                      onClick={() => void copyFailedRowsAsInsertSql()}
                      style={styles.btn}
                      title="把当前显示（筛选后）的失败行原样渲染为 INSERT SQL 复制到剪贴板（可直接粘到 SQL 编辑器调试）"
                    >
                      📋 复制为 INSERT SQL（{failCategoryFilter ? filteredFailedRows.length : failedRows.length}）
                    </button>
                  </div>
                </div>
              )}
              {Object.keys(overrides).length > 0 && inputFormat !== 'sql' && parse && (
                <div style={{ ...styles.mutedBox, marginTop: 6, borderLeft: '3px solid var(--accent, #3b82f6)' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4 }}>
                    <span style={{ fontWeight: 600 }}>🔧 已就地编辑 {Object.keys(overrides).length} 行（点重跑生效，不影响原始 CSV）</span>
                    <span style={{ ...styles.spacer }} />
                    <button
                      onClick={() => setOverrides({})}
                      style={styles.btnGhost}
                    >清除全部</button>
                  </div>
                  {Object.keys(overrides).map((k) => {
                    const stmtIdx = Number(k);
                    const row = overrides[stmtIdx] ?? [];
                    return (
                      <div key={k} style={{ marginBottom: 6 }}>
                        <div style={{ fontSize: 11, color: 'var(--muted)', marginBottom: 2 }}>
                          Statement #{stmtIdx}（原 CSV 第 {(parse.rows[stmtIdx] ? stmtIdx + 1 : '?')} 行）
                          <a
                            href="#"
                            onClick={(e) => { e.preventDefault(); setOverrides((prev) => { const n = { ...prev }; delete n[stmtIdx]; return n; }); }}
                            style={{ marginLeft: 8, fontSize: 10, cursor: 'pointer' }}
                          >撤销</a>
                        </div>
                        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 4 }}>
                          {parse.columns.map((col, ci) => {
                            const targetCol = mappings[ci]?.targetColumn ?? null;
                            return (
                              <div key={ci} style={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
                                <div style={{ fontSize: 9, color: 'var(--muted)', fontFamily: 'monospace' }}>
                                  {ci + 1}.{col}{targetCol ? ` → ${targetCol}` : ''}
                                </div>
                                <input
                                  type="text"
                                  value={row[ci] ?? ''}
                                  onChange={(e) => {
                                    const next = { ...overrides };
                                    const cur = [...(next[stmtIdx] ?? parse.rows[stmtIdx] ?? [])];
                                    cur[ci] = e.target.value;
                                    next[stmtIdx] = cur;
                                    setOverrides(next);
                                  }}
                                  style={{ ...styles.inputSm, fontSize: 10, padding: '2px 4px' }}
                                  placeholder="（空= NULL）"
                                />
                              </div>
                            );
                          })}
                        </div>
                      </div>
                    );
                  })}
                  <button
                    onClick={() => void runImport()}
                    disabled={busy}
                    style={styles.btnPrimary}
                    title="以编辑后的行数据重新执行导入"
                  >
                    ▶ 重跑全部（{statements.length} 条 · 含编辑）
                  </button>
                </div>
              )}
              {retryIndices && (
                <div style={{ ...styles.mutedBox, marginTop: 6, color: 'var(--accent, #3b82f6)' }}>
                  已标记 {retryIndices.size} 行重试（修改 CSV 后点「开始导入」只跑这些行）
                </div>
              )}
              {inputFormat !== 'sql' && selTable && !busy && (() => {
                const key = presetKey(connId, selSchema, selTable);
                const existingPreset = getPreset(connId, selSchema, selTable);
                // M30.112 删除预设 30s 内可撤销：即使 existingPreset 已不存在，只要 restore state 还在就展示恢复条
                if (delPresetRestore) {
                  const left = Math.max(0, Math.ceil((delPresetRestore.expiresAt - Date.now()) / 1000));
                  void delPresetRestoreTick;
                  return (
                    <div
                      style={{
                        marginTop: 8, padding: '6px 10px', borderRadius: 4,
                        background: 'rgba(217,119,6,0.10)',
                        border: '1px solid rgba(217,119,6,0.40)',
                        display: 'flex', alignItems: 'center', gap: 8,
                        fontSize: 11, color: 'var(--warn, #d97706)',
                      }}
                    >
                      <span style={{ fontWeight: 700 }}>
                        🗑 已删除预设{' '}
                        <code style={{ background: 'rgba(217,119,6,0.14)', padding: '0 4px', borderRadius: 3 }}>
                          {delPresetRestore.schema}.{delPresetRestore.table}
                        </code>
                        {' '}（含 {delPresetRestore.snapshots.length} 个快照历史）
                      </span>
                      <span style={{ color: 'var(--muted)', marginLeft: 4 }}>·</span>
                      <span>{left}s 内可撤销</span>
                      <span style={{ flex: 1 }} />
                      <button
                        onClick={() => {
                          restoreDeletedPreset(delPresetRestore.preset, delPresetRestore.snapshots);
                          setAppliedPreset(delPresetRestore.preset);
                          setPresetApplied(null);
                          setPresetSnapshotsRefresh((n) => n + 1);
                          const restored = delPresetRestore;
                          setDelPresetRestore(null);
                          publishEditorStatus({
                            message: `↩ 已恢复预设 ${restored.schema}.${restored.table}（含 ${restored.snapshots.length} 个快照历史）`,
                            messageAt: Date.now(),
                          });
                        }}
                        style={{
                          ...styles.btnSm, padding: '2px 8px', fontSize: 10,
                          color: 'var(--success, #10b981)',
                          borderColor: 'var(--success, #10b981)',
                          background: 'rgba(16,185,129,0.08)', fontWeight: 700,
                        }}
                        title="撤销删除：把预设及全部快照历史还原到删除前的状态"
                      >↩ 撤销删除</button>
                      <button
                        onClick={() => setDelPresetRestore(null)}
                        style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 10, color: 'var(--muted)' }}
                        title="不再提示（预设需重新创建）"
                      >✕</button>
                    </div>
                  );
                }
                const savedAtLabel = existingPreset
                  ? new Date(existingPreset.createdAt).toLocaleString('zh-CN', {
                      month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
                    })
                  : null;
                const isOverwrite = !!existingPreset;
                const bg = isOverwrite ? 'rgba(217,119,6,0.08)' : 'rgba(16,185,129,0.08)';
                const border = isOverwrite ? 'rgba(217,119,6,0.4)' : 'rgba(16,185,129,0.35)';
                const accent = isOverwrite ? 'var(--warn, #d97706)' : 'var(--success, #10b981)';
                const codeBg = isOverwrite ? 'rgba(217,119,6,0.14)' : 'rgba(16,185,129,0.14)';
                const snaps = existingPreset ? listSnapshots(key) : [];
                const hist = history.find((h) => h.schema === selSchema && h.table === selTable);
                const histLabel = hist
                  ? new Date(hist.at).toLocaleString('zh-CN', {
                      month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
                    })
                  : null;
                const deviation = (() => {
                  if (!isOverwrite) return null;
                  const presetTargets = new Set(
                    existingPreset!.mappings.filter((m) => m.targetColumn).map((m) => m.targetColumn!),
                  );
                  const currentTargets = new Set(
                    mappings.filter((m) => m.targetColumn).map((m) => m.targetColumn!),
                  );
                  const removedList = Array.from(presetTargets).filter((c) => !currentTargets.has(c));
                  const addedList = Array.from(currentTargets).filter((c) => !presetTargets.has(c));
                  const added = addedList.length;
                  const removed = removedList.length;
                  const scalarDiffs: string[] = [];
                  if (existingPreset!.mode !== opts.mode) scalarDiffs.push(`模式 ${existingPreset!.mode}→${opts.mode}`);
                  if (existingPreset!.batchSize !== opts.batchSize) scalarDiffs.push(`批量 ${existingPreset!.batchSize}→${opts.batchSize}`);
                  if (existingPreset!.emptyAsNull !== opts.emptyAsNull) scalarDiffs.push(`空串NULL ${existingPreset!.emptyAsNull?'是':'否'}→${opts.emptyAsNull?'是':'否'}`);
                  if (existingPreset!.skipFailed !== opts.skipFailed) scalarDiffs.push(`失败跳过 ${existingPreset!.skipFailed?'是':'否'}→${opts.skipFailed?'是':'否'}`);
                  if ((existingPreset!.strictValidation ?? true) !== opts.strictValidation) scalarDiffs.push(`严格校验 ${existingPreset!.strictValidation??true?'是':'否'}→${opts.strictValidation?'是':'否'}`);
                  const n = added + removed + scalarDiffs.length;
                  return n === 0
                    ? { state: 'aligned', n: 0, added: 0, removed: 0, addedList: [] as string[], removedList: [] as string[], scalarDiffs: [] as string[] } as const
                    : { state: 'deviated', n, added, removed, addedList, removedList, scalarDiffs } as const;
                })();
                const headerTitle = isOverwrite
                  ? `已存在预设 ${selSchema}.${selTable}（${savedAtLabel}），本次导入成功后将被覆盖；旧版本自动进入快照历史，可回滚。点击展开详情。`
                  : `将新建预设 ${selSchema}.${selTable}，下次同一表导入可直接应用。点击展开详情。`;
                const expandHint = (() => {
                  if (!existingPreset) return '首次导入此表';
                  const parts: string[] = [`历史 ${snaps.length} 个快照`];
                  if (histLabel) parts.push(`上次导入 ${histLabel}`);
                  return parts.join(' · ');
                })();
                return (
                  <div style={{
                    marginTop: 8, padding: presetChipExpanded ? '6px 10px' : '6px 10px',
                    background: bg, border: `1px solid ${border}`,
                    borderLeft: `3px solid ${accent}`,
                    borderRadius: 4, fontSize: 11, color: 'var(--muted)',
                  }}>
                    <div
                      onClick={() => setPresetChipExpanded((v) => !v)}
                      style={{
                        display: 'flex', alignItems: 'center', gap: 8,
                        cursor: 'pointer', userSelect: 'none',
                      }}
                      title={headerTitle}
                    >
                      <span style={{ fontSize: 12 }}>{isOverwrite ? '📝' : '📋'}</span>
                      <span>
                        {isOverwrite ? '成功后将覆盖预设' : '成功后将新建预设'}
                        <code style={{
                          marginLeft: 4, padding: '0 5px', borderRadius: 3,
                          background: codeBg, color: accent,
                          fontFamily: 'monospace', fontSize: 10, fontWeight: 600,
                        }}>
                          {selSchema}.{selTable}
                        </code>
                        {savedAtLabel && (
                          <span style={{ marginLeft: 6, fontSize: 10 }}>
                            （上次 {savedAtLabel}）
                          </span>
                        )}
                        {deviation && (() => {
                          if (deviation.state === 'aligned') {
                            return (
                              <span style={{
                                display: 'inline-flex', alignItems: 'center', gap: 3,
                                marginLeft: 8, padding: '1px 6px', borderRadius: 999,
                                background: 'rgba(16,185,129,0.12)',
                                border: '1px solid rgba(16,185,129,0.35)',
                                fontSize: 10, color: 'var(--success, #10b981)', fontWeight: 600,
                              }} title="当前列映射目标集合与旧预设完全一致；导入成功将刷新保存时间戳，不新增差异快照">
                                ✓ 配置一致
                              </span>
                            );
                          }
                          const bits: string[] = [];
                          if (deviation.removed > 0) bits.push(`旧预设多 ${deviation.removed} 列`);
                          if (deviation.added > 0) bits.push(`当前多 ${deviation.added} 列`);
                          if (deviation.scalarDiffs.length > 0) bits.push(`配置 ${deviation.scalarDiffs.length} 项：${deviation.scalarDiffs.join('、')}`);
                          return (
                            <span style={{ display: 'inline-flex', gap: 4, alignItems: 'center', marginLeft: 8, flexWrap: 'wrap' }}>
                              <span style={{
                                display: 'inline-flex', alignItems: 'center', gap: 3,
                                padding: '1px 6px', borderRadius: 999,
                                background: 'rgba(217,119,6,0.14)',
                                border: '1px solid rgba(217,119,6,0.5)',
                                fontSize: 10, color: 'var(--warn, #d97706)', fontWeight: 600,
                              }} title={`旧预设 vs 当前配置：${bits.join(' · ')}；可用详情面板「⚡ 应用旧预设」恢复`}>
                                ≠ {deviation.n} 处差异
                              </span>
                              {deviation.removedList.length > 0 && (() => {
                                const MAX = 3;
                                const shown = deviation.removedList.slice(0, MAX);
                                const rest = deviation.removedList.length - shown.length;
                                return (
                                  <span style={{ display: 'inline-flex', gap: 2, alignItems: 'center' }} title="以下列在旧预设中存在但当前映射未使用（导入成功将被移除）">
                                    {shown.map((c) => (
                                      <code key={`r-${c}`} style={
                                        {
                                          padding: '0 4px', borderRadius: 3, fontSize: 10,
                                          background: 'rgba(220,38,38,0.10)',
                                          color: 'var(--danger, #dc2626)',
                                          fontFamily: 'monospace', textDecoration: 'line-through',
                                        }
                                      }>{c}</code>
                                    ))}
                                    {rest > 0 && (
                                      <span style={{ fontSize: 9, color: 'var(--danger, #dc2626)', opacity: 0.7 }}>+{rest}</span>
                                    )}
                                  </span>
                                );
                              })()}
                              {deviation.addedList.length > 0 && (() => {
                                const MAX = 3;
                                const shown = deviation.addedList.slice(0, MAX);
                                const rest = deviation.addedList.length - shown.length;
                                return (
                                  <span style={{ display: 'inline-flex', gap: 2, alignItems: 'center' }} title="以下列当前映射已使用但旧预设未见过（新增列）">
                                    {shown.map((c) => (
                                      <code key={`a-${c}`} style={
                                        {
                                          padding: '0 4px', borderRadius: 3, fontSize: 10,
                                          background: 'rgba(16,185,129,0.10)',
                                          color: 'var(--success, #10b981)',
                                          fontFamily: 'monospace',
                                        }
                                      }>{c}</code>
                                    ))}
                                    {rest > 0 && (
                                      <span style={{ fontSize: 9, color: 'var(--success, #10b981)', opacity: 0.7 }}>+{rest}</span>
                                    )}
                                  </span>
                                );
                              })()}
                              {deviation.scalarDiffs.length > 0 && (
                                <span style={
                                  {
                                    display: 'inline-flex', alignItems: 'center', gap: 3,
                                    padding: '1px 6px', borderRadius: 999, fontSize: 10,
                                    background: 'rgba(59,130,246,0.10)',
                                    border: '1px solid rgba(59,130,246,0.35)',
                                    color: 'var(--accent, #3b82f6)', fontWeight: 600,
                                  }
                                } title={`以下配置项与旧预设不同：${deviation.scalarDiffs.join('、')}`}>
                                  ⚙ 配置 {deviation.scalarDiffs.length} 项
                                </span>
                              )}
                            </span>
                          );
                        })()}
                        <span style={{ marginLeft: 8, opacity: 0.6, fontSize: 10 }}>
                          {presetChipExpanded ? '▲ 收起' : '▼ 详情'}
                        </span>
                      </span>
                    </div>
                    {presetChipExpanded && (
                      <div style={{
                        marginTop: 6, paddingTop: 6,
                        borderTop: `1px dashed ${border}`,
                        display: 'flex', flexDirection: 'column', gap: 4,
                        fontSize: 11, color: 'var(--fg)',
                      }}>
                        <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
                          <span style={{ color: 'var(--muted)' }}>Key:
                            <code style={{ marginLeft: 4, padding: '0 4px', borderRadius: 3, background: 'rgba(255,255,255,0.06)', fontFamily: 'monospace', fontSize: 10 }}>
                              {key}
                            </code>
                          </span>
                          <span style={{ color: 'var(--muted)' }}>首次保存:
                            <strong>{savedAtLabel ?? '—'}</strong>
                          </span>
                          <span style={{ color: 'var(--muted)' }}>快照历史:
                            <strong style={{ color: snaps.length > 0 ? 'var(--accent, #3b82f6)' : 'var(--muted)' }}>{snaps.length}</strong>
                          </span>
                          <span style={{ color: 'var(--muted)' }}>上次导入:
                            <strong>{histLabel ?? '—'}</strong>
                          </span>
                        </div>
                        {snaps.length > 0 && (() => {
                          const MAX = 3;
                          const shown = snaps.slice(0, MAX);
                          const rest = snaps.length - shown.length;
                          const fmtSnapDate = (t: number) => {
                            const d = new Date(t);
                            const pad = (n: number) => String(n).padStart(2, '0');
                            return `${pad(d.getMonth() + 1)}/${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
                          };
                          // M30.115 相对时间 + 到期天数（30 天自动过期）
                          const fmtRelative = (t: number) => {
                            const diff = Date.now() - t;
                            const day = 24 * 60 * 60 * 1000;
                            if (diff < 60 * 1000) return '刚刚';
                            if (diff < 60 * 60 * 1000) return `${Math.floor(diff / (60 * 1000))} 分钟前`;
                            if (diff < day) return `${Math.floor(diff / (60 * 60 * 1000))} 小时前`;
                            if (diff < day * 30) return `${Math.floor(diff / day)} 天前`;
                            return '已过期';
                          };
                          const fmtExpiry = (t: number) => {
                            const leftDays = Math.ceil((30 * 24 * 60 * 60 * 1000 - (Date.now() - t)) / (24 * 60 * 60 * 1000));
                            if (leftDays <= 0) return '已过期';
                            if (leftDays <= 3) return `${leftDays} 天后过期`;
                            return `${leftDays} 天后过期`;
                          };
                          return (
                            <div style={{
                              display: 'flex', flexDirection: 'column', gap: 2,
                              padding: '4px 6px', borderRadius: 3,
                              background: 'rgba(255,255,255,0.03)',
                              border: '1px solid rgba(255,255,255,0.06)',
                            }} title="预设快照（每次覆盖前自动保存旧版本，最多 5 个，30 天后自动过期）；点击「↩ 恢复」用该快照覆盖当前配置并同步到主 preset，Ctrl+Z 可撤销">
                              <span style={{ fontSize: 9, color: 'var(--muted)', opacity: 0.7, fontWeight: 600 }}>
                                🕘 最近快照{rest > 0 ? `（+${rest} 更多）` : ''} <span style={{ fontWeight: 400 }}>· 30 天自动过期</span>
                              </span>
                              {shown.map((s, i) => {
                                const rel = fmtRelative(s.updatedAt);
                                const expiry = fmtExpiry(s.updatedAt);
                                const nearExpiry = /3 天后/.test(expiry);
                                const diffExpanded = snapDiffTs === s.updatedAt && pendingRestoreConfirm?.ts !== s.updatedAt;
                                const restoreConfirming = pendingRestoreConfirm?.ts === s.updatedAt && pendingRestoreConfirm.idx === i;
                                const diffChipStyle = (k: 'add' | 'remove' | 'change'): CSSProperties => ({
                                  padding: '0 4px', borderRadius: 3, fontSize: 9,
                                  background: k === 'remove' ? 'rgba(220,38,38,0.10)'
                                    : k === 'add' ? 'rgba(16,185,129,0.10)'
                                    : 'rgba(59,130,246,0.10)',
                                  color: k === 'remove' ? 'var(--danger, #dc2626)'
                                    : k === 'add' ? 'var(--success, #10b981)'
                                    : 'var(--accent, #3b82f6)',
                                  fontFamily: 'monospace',
                                  textDecoration: k === 'remove' ? 'line-through' : 'none',
                                });
                                const buildCurrentPreset = (): ImportPreset => ({
                                  key: s.key, schema: s.schema, table: s.table, kind: kindProp,
                                  mode: opts.mode, mappings: [...mappings],
                                  transforms: { ...(opts.transforms ?? {}) },
                                  transformParams: { ...(opts.transformParams ?? {}) },
                                  filterColumn: opts.filterColumn ?? null,
                                  filterOp: (opts.filterOp ?? 'contains') as ImportPreset['filterOp'],
                                  filterValue: opts.filterValue ?? '',
                                  validations: { ...(opts.validations ?? {}) },
                                  strictValidation: opts.strictValidation ?? true,
                                  emptyAsNull: opts.emptyAsNull, batchSize: opts.batchSize, skipFailed: opts.skipFailed,
                                  nullPolicies: { ...(opts.nullPolicies ?? {}) },
                                  createdAt: s.createdAt, updatedAt: Date.now(),
                                });
                                const buildFieldBits = (d: ReturnType<typeof diffPreset>): { text: string; kind: 'add' | 'remove' | 'change' }[] => {
                                  const bits: { text: string; kind: 'add' | 'remove' | 'change' }[] = [];
                                  for (const sc of d.scalarDiffs) bits.push({ text: sc, kind: 'change' });
                                  for (const [f, arr] of Object.entries(d.addedCols)) {
                                    for (let k = 0; k < arr.length; k++) bits.push({ text: `+${f} ${arr[k]}`, kind: 'add' });
                                  }
                                  for (const [f, arr] of Object.entries(d.removedCols)) {
                                    for (let k = 0; k < arr.length; k++) bits.push({ text: `−${f} ${arr[k]}`, kind: 'remove' });
                                  }
                                  for (const c of d.addedTargets) bits.push({ text: `+列 ${c}`, kind: 'add' });
                                  for (const c of d.removedTargets) bits.push({ text: `−列 ${c}`, kind: 'remove' });
                                  return bits;
                                };
                                const diff = cols.length > 0 ? diffPreset(buildCurrentPreset(), s) : null;
                                const doRestore = () => {
                                  if (cols.length === 0) return;
                                  const before = {
                                    ...opts,
                                    transforms: { ...opts.transforms ?? {} },
                                    transformParams: { ...opts.transformParams ?? {} },
                                    validations: { ...opts.validations ?? {} },
                                    nullPolicies: { ...opts.nullPolicies ?? {} },
                                  };
                                  restoreSnapshot(key, i);
                                  const r = applyPreset(s, cols);
                                  setUndoStack((st) => [{
                                    at: Date.now(),
                                    snapshot: before,
                                    icon: '↩',
                                    label: `恢复快照（${selSchema}.${selTable} ${fmtSnapDate(s.updatedAt)}）`,
                                    key: 'restore-snapshot',
                                    msg: '撤销可回到恢复快照之前的当前配置',
                                  }, ...st].slice(0, MAX_FIX_HISTORY));
                                  setMappings(r.mappings);
                                  setOpts((o) => ({
                                    ...o,
                                    mode: r.mode,
                                    transforms: r.transforms,
                                    transformParams: r.transformParams,
                                    emptyAsNull: r.opts.emptyAsNull,
                                    batchSize: r.opts.batchSize,
                                    skipFailed: r.opts.skipFailed,
                                    filterColumn: r.opts.filterColumn,
                                    filterOp: r.opts.filterOp,
                                    filterValue: r.opts.filterValue,
                                    validations: r.opts.validations,
                                    strictValidation: r.opts.strictValidation,
                                    nullPolicies: r.opts.nullPolicies,
                                  }));
                                  setPresetApplied({
                                    matched: r.matchedCols,
                                    total: r.totalCols,
                                    removed: r.removedTargets.map((t) => ({ csvName: t.csvName, targetColumn: t.targetColumn })),
                                    added: r.addedTargets,
                                  });
                                  setAppliedPreset(s);
                                  setPresetSnapshotsRefresh((n) => n + 1);
                                  publishEditorStatus({
                                    message: `↩ 已用快照 ${selSchema}.${selTable} @ ${fmtSnapDate(s.updatedAt)} 覆盖当前配置（${r.matchedCols}/${r.totalCols} 列匹配，可 Ctrl+Z 撤销）`,
                                    messageAt: Date.now(),
                                  });
                                };
                                const onRestoreClick = () => {
                                  if (cols.length === 0) return;
                                  if (diff && diff.aligned) { doRestore(); return; }
                                  setPendingRestoreConfirm({ ts: s.updatedAt, idx: i });
                                };
                                return (
                                  <div key={`${s.updatedAt}-${i}`} style={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
                                    <div style={
                                      {
                                        display: 'flex', alignItems: 'center', gap: 6,
                                        fontSize: 10, padding: '1px 0',
                                        fontFamily: 'monospace', color: 'var(--muted)',
                                      }
                                    } title={`${rel} · ${expiry}`}>
                                      <span style={{ flex: 1, minWidth: 0 }}>
                                        {fmtSnapDate(s.updatedAt)} · {s.mode} · {s.mappings.length} 列
                                        <span style={{ color: 'var(--muted)', marginLeft: 4 }}>（{rel}）</span>
                                        {nearExpiry && (
                                          <span style={{ color: 'var(--warn, #d97706)', marginLeft: 4 }}>⏳ {expiry}</span>
                                        )}
                                      </span>
                                    <button
                                      onClick={onRestoreClick}
                                      disabled={cols.length === 0}
                                      style={
                                        {
                                          ...styles.btnSm, padding: '0 5px', fontSize: 9,
                                          color: cols.length === 0 ? 'var(--muted)' : 'var(--accent, #3b82f6)',
                                          borderColor: 'var(--accent, #3b82f6)',
                                          opacity: cols.length === 0 ? 0.4 : 1,
                                        }
                                      }
                                      title={cols.length === 0 ? '列信息未加载，无法应用快照' : (diff && diff.aligned ? '与当前一致，直接恢复' : '用此快照覆盖主 preset 与当前配置（非 aligned 时会弹二次确认）')}
                                    >↩ 恢复</button>
                                    <button
                                      type="button"
                                      onClick={() => {
                                        setSnapDiffTs(diffExpanded ? null : s.updatedAt);
                                        if (restoreConfirming) setPendingRestoreConfirm(null);
                                      }}
                                      disabled={cols.length === 0}
                                      style={
                                        {
                                          ...styles.btnSm, padding: '0 5px', fontSize: 9,
                                          color: cols.length === 0 ? 'var(--muted)' : (diffExpanded ? 'var(--accent, #3b82f6)' : 'var(--muted)'),
                                          borderColor: diffExpanded ? 'var(--accent, #3b82f6)' : 'var(--border)',
                                          opacity: cols.length === 0 ? 0.4 : 1,
                                        }
                                      }
                                      title={cols.length === 0 ? '列信息未加载，无法对比' : (diffExpanded ? '收起字段级 diff' : '对比此快照与当前配置（复用 M30.120 算法）')}
                                    >{diffExpanded ? '▲ 对比' : '⇄ 对比'}</button>
                                    </div>
                                    {restoreConfirming && diff && (() => {
                                      const fieldBits = buildFieldBits(diff);
                                      const MAX = 4;
                                      const shownBits = fieldBits.slice(0, MAX);
                                      const restCount = fieldBits.length - shownBits.length;
                                      return (
                                        <div style={
                                          {
                                            fontSize: 9, padding: '4px 6px', borderRadius: 3,
                                            background: 'rgba(217,119,6,0.08)',
                                            border: '1px solid rgba(217,119,6,0.4)',
                                            display: 'flex', flexDirection: 'column', gap: 3,
                                          }
                                        }>
                                          <div style={{ display: 'flex', gap: 3, flexWrap: 'wrap', alignItems: 'center' }} title={`快照 vs 当前 共 ${diff.total} 项差异：${fieldBits.map((b) => b.text).join(' · ')}`}>
                                            <span style={{
                                              padding: '0 4px', borderRadius: 3, fontSize: 9,
                                              background: 'rgba(217,119,6,0.14)',
                                              border: '1px solid rgba(217,119,6,0.5)',
                                              color: 'var(--warn, #d97706)', fontWeight: 600,
                                            }}>⚠ 将覆盖</span>
                                            <span style={{
                                              padding: '0 4px', borderRadius: 3, fontSize: 9,
                                              background: 'rgba(217,119,6,0.14)',
                                              border: '1px solid rgba(217,119,6,0.5)',
                                              color: 'var(--warn, #d97706)', fontWeight: 600,
                                            }}>⇄ {diff.total} 项差异</span>
                                            {shownBits.map((b, idx) => {
                                              const isCopied = copiedChipText === b.text;
                                              return (
                                                <button
                                                  key={idx}
                                                  type="button"
                                                  onClick={() => {
                                                    const text = b.text;
                                                    void navigator.clipboard.writeText(text).then(() => {
                                                      setCopiedChipText(text);
                                                      setTimeout(() => {
                                                        setCopiedChipText((cur) => (cur === text ? null : cur));
                                                      }, 1200);
                                                    }).catch(() => { /* clipboard 拒绝忽略 */ });
                                                  }}
                                                  style={{
                                                    ...diffChipStyle(b.kind),
                                                    cursor: 'pointer',
                                                    border: '1px solid transparent',
                                                    padding: '0 5px',
                                                    opacity: isCopied ? 0.75 : 1,
                                                  }}
                                                  title={isCopied ? `✓ 已复制「${b.text}」` : `点击复制「${b.text}」`}
                                                >{isCopied ? '✓ ' : '📋 '}{b.text}</button>
                                              );
                                            })}
                                            {restCount > 0 && (
                                              <span style={{ fontSize: 8, color: 'var(--muted)' }}>+{restCount}</span>
                                            )}
                                          </div>
                                          <div style={{ display: 'flex', gap: 6, alignItems: 'center', fontSize: 9 }}>
                                            <span style={{ color: 'var(--muted)', flex: 1, minWidth: 0 }}>
                                              确认用此快照覆盖当前配置？（当前配置先入撤销栈，Ctrl+Z 可撤销）
                                            </span>
                                            <button
                                              type="button"
                                              onClick={() => { setPendingRestoreConfirm(null); doRestore(); }}
                                              style={
                                                {
                                                  ...styles.btnSm, padding: '1px 8px', fontSize: 9,
                                                  color: 'var(--success, #10b981)', borderColor: 'var(--success, #10b981)',
                                                }
                                              }
                                              title="确认用此快照覆盖当前配置"
                                            >✅ 确认恢复</button>
                                            <button
                                              type="button"
                                              onClick={() => setPendingRestoreConfirm(null)}
                                              style={
                                                {
                                                  ...styles.btnSm, padding: '1px 8px', fontSize: 9,
                                                  color: 'var(--muted)', borderColor: 'var(--border)',
                                                }
                                              }
                                              title="取消恢复"
                                            >✕ 取消</button>
                                          </div>
                                        </div>
                                      );
                                    })()}
                                    {diffExpanded && cols.length > 0 && diff && (() => {
                                      if (diff.aligned) {
                                        return (
                                          <div style={{
                                            fontSize: 9, padding: '1px 0 1px 20px',
                                            color: 'var(--success, #10b981)',
                                          }} title="快照与当前配置字段完全一致；恢复此快照不会改变配置">
                                            ✓ 与当前一致（恢复无效果）
                                          </div>
                                        );
                                      }
                                      const fieldBits = buildFieldBits(diff);
                                      const MAX = 4;
                                      const shownBits = fieldBits.slice(0, MAX);
                                      const rest = fieldBits.length - shownBits.length;
                                      return (
                                        <div style={
                                          {
                                            fontSize: 9, padding: '2px 0 2px 20px',
                                            display: 'flex', gap: 3, flexWrap: 'wrap', alignItems: 'center',
                                          }
                                        } title={`快照 vs 当前 共 ${diff.total} 项差异：${fieldBits.map((b) => b.text).join(' · ')}`}>
                                          <span style={{
                                            padding: '0 4px', borderRadius: 3, fontSize: 9,
                                            background: 'rgba(217,119,6,0.14)',
                                            border: '1px solid rgba(217,119,6,0.5)',
                                            color: 'var(--warn, #d97706)', fontWeight: 600,
                                          }}>⇄ {diff.total} 项差异</span>
                                          {shownBits.map((b, idx) => {
                                            const isCopied = copiedChipText === b.text;
                                            return (
                                              <button
                                                key={idx}
                                                type="button"
                                                onClick={() => {
                                                  const text = b.text;
                                                  void navigator.clipboard.writeText(text).then(() => {
                                                    setCopiedChipText(text);
                                                    setTimeout(() => {
                                                      setCopiedChipText((cur) => (cur === text ? null : cur));
                                                    }, 1200);
                                                  }).catch(() => { /* clipboard 拒绝忽略 */ });
                                                }}
                                                style={{
                                                  ...diffChipStyle(b.kind),
                                                  cursor: 'pointer',
                                                  border: '1px solid transparent',
                                                  padding: '0 5px',
                                                  opacity: isCopied ? 0.75 : 1,
                                                }}
                                                title={isCopied ? `✓ 已复制「${b.text}」` : `点击复制「${b.text}」`}
                                              >{isCopied ? '✓ ' : '📋 '}{b.text}</button>
                                            );
                                          })}
                                          {rest > 0 && (
                                            <span style={{ fontSize: 8, color: 'var(--muted)' }}>+{rest}</span>
                                          )}
                                        </div>
                                      );
                                    })()}
                                  </div>
                                );
                              })}
                            </div>
                          );
                        })()}
                        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
                          {existingPreset && (
                            <span style={{ fontSize: 10, color: 'var(--muted)', opacity: 0.8 }}>
                              {snaps.length > 0
                                ? `旧版本 ${snaps.length} 个快照 · 覆盖后自动进入历史`
                                : `尚无快照 · 覆盖后当前版本将进入历史可回滚`}
                            </span>
                          )}
                          {!existingPreset && (
                            <span style={{ fontSize: 10, color: 'var(--muted)', opacity: 0.7 }}>
                              {expandHint}
                            </span>
                          )}
                          {existingPreset && cols.length > 0 && (() => {
                            const confirming = applyOldConfirmAt != null;
                            if (!confirming) {
                              return (
                                <button
                                  onClick={() => setApplyOldConfirmAt(Date.now())}
                                  style={{
                                    ...styles.btnSm, padding: '2px 8px', fontSize: 10,
                                    color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)',
                                  }}
                                  title="放弃当前配置改动，用已保存的旧预设恢复（点击进入二次确认，5s 内自动失效）"
                                >⚡ 应用旧预设</button>
                              );
                            }
                            const left = Math.max(0, Math.ceil((applyOldConfirmAt! + 5000 - Date.now()) / 1000));
                            void applyOldTick;
                            return (
                              <span style={{ display: 'inline-flex', gap: 4, alignItems: 'center' }}>
                                <button
                                  onClick={() => {
                                    const before = {
                                      ...opts,
                                      transforms: { ...opts.transforms ?? {} },
                                      transformParams: { ...opts.transformParams ?? {} },
                                      validations: { ...opts.validations ?? {} },
                                      nullPolicies: { ...opts.nullPolicies ?? {} },
                                    };
                                    const r = applyPreset(existingPreset, cols);
                                    setUndoStack((s) => [{
                                      at: Date.now(),
                                      snapshot: before,
                                      icon: '⚡',
                                      label: `应用旧预设（${selSchema}.${selTable}）`,
                                      key: 'apply-old-preset',
                                      msg: '撤销可回到应用旧预设之前的当前配置',
                                    }, ...s].slice(0, MAX_FIX_HISTORY));
                                    setMappings(r.mappings);
                                    setOpts((o) => ({
                                      ...o,
                                      mode: r.mode,
                                      transforms: r.transforms,
                                      transformParams: r.transformParams,
                                      emptyAsNull: r.opts.emptyAsNull,
                                      batchSize: r.opts.batchSize,
                                      skipFailed: r.opts.skipFailed,
                                      filterColumn: r.opts.filterColumn,
                                      filterOp: r.opts.filterOp,
                                      filterValue: r.opts.filterValue,
                                      validations: r.opts.validations,
                                      strictValidation: r.opts.strictValidation,
                                      nullPolicies: r.opts.nullPolicies,
                                    }));
                                    setPresetApplied({
                                      matched: r.matchedCols,
                                      total: r.totalCols,
                                      removed: r.removedTargets.map((t) => ({ csvName: t.csvName, targetColumn: t.targetColumn })),
                                      added: r.addedTargets,
                                    });
                                    setAppliedPreset(existingPreset);
                                    setPresetChipExpanded(false);
                                    setApplyOldConfirmAt(null);
                                    publishEditorStatus({
                                      message: `⚡ 已把旧预设 ${selSchema}.${selTable} 应用回当前配置（${r.matchedCols}/${r.totalCols} 列匹配，可 Ctrl+Z 撤销）`,
                                      messageAt: Date.now(),
                                    });
                                  }}
                                  style={{
                                    ...styles.btnSm, padding: '2px 8px', fontSize: 10,
                                    color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)',
                                    background: 'rgba(220,38,38,0.08)', fontWeight: 700,
                                  }}
                                  title="确认放弃当前改动，用旧预设覆盖当前配置（5s 内未操作将自动取消）"
                                >⚠ 确认覆盖</button>
                                <button
                                  onClick={() => setApplyOldConfirmAt(null)}
                                  style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 10 }}
                                  title="取消确认"
                                >✕ 取消</button>
                                <span style={
                                  {
                                    fontSize: 10, color: 'var(--warn, #d97706)',
                                    marginLeft: 2, fontWeight: 600,
                                  }
                                } title="距自动取消">
                                  {left}s
                                </span>
                              </span>
                            );
                          })()}
                          {existingPreset && cols.length === 0 && (
                            <span style={{ fontSize: 10, color: 'var(--muted)', opacity: 0.6 }} title="目标表列信息未加载（应已在 Step 2 加载；如缺失可回 Step 2 重新选择表）">
                              列信息缺失，无法应用
                            </span>
                          )}
                          {existingPreset && (() => {
                            const copied = presetCopyAt != null;
                            return (
                              <button
                                onClick={() => {
                                  if (copied) return;
                                  const json = JSON.stringify(existingPreset, null, 2);
                                  navigator.clipboard.writeText(json).then(
                                    () => {
                                      setPresetCopyAt(Date.now());
                                      publishEditorStatus({
                                        message: `📋 已复制预设 ${selSchema}.${selTable} 完整 JSON（${json.length} 字符）到剪贴板`,
                                        messageAt: Date.now(),
                                      });
                                    },
                                    () => {
                                      publishEditorStatus({
                                        message: '❌ 复制失败：浏览器剪贴板权限被拒',
                                        messageAt: Date.now(),
                                      });
                                    },
                                  );
                                }}
                                disabled={copied}
                                style={copied
                                  ? {
                                      ...styles.btnSm, padding: '2px 8px', fontSize: 10,
                                      color: 'var(--success, #10b981)',
                                      borderColor: 'var(--success, #10b981)',
                                      background: 'rgba(16,185,129,0.08)',
                                    }
                                  : {
                                      ...styles.btnSm, padding: '2px 8px', fontSize: 10,
                                      color: 'var(--accent, #3b82f6)', borderColor: 'var(--accent, #3b82f6)',
                                    }}
                                title="复制完整预设 JSON（含 mappings/transforms/opts/时间戳）到剪贴板，便于分享或审计"
                              >{copied ? '✓ 已复制' : '📋 复制 JSON'}</button>
                            );
                          })()}
                          {existingPreset && (() => {
                            const confirming = delPresetConfirmAt != null;
                            if (!confirming) {
                              return (
                                <button
                                  onClick={() => setDelPresetConfirmAt(Date.now())}
                                  style={{
                                    ...styles.btnSm, padding: '2px 8px', fontSize: 10,
                                    color: 'var(--muted)', borderColor: 'var(--border, rgba(255,255,255,0.15))',
                                  }}
                                  title="永久删除预设 schema.table 及其全部快照历史（点击进入二次确认，5s 内自动失效）"
                                >🗑 删除预设</button>
                              );
                            }
                            const left = Math.max(0, Math.ceil((delPresetConfirmAt! + 5000 - Date.now()) / 1000));
                            void delPresetTick;
                            return (
                              <span style={{ display: 'inline-flex', gap: 4, alignItems: 'center' }} title="确认删除后无法恢复；快照历史一并清空">
                                <button
                                  onClick={() => {
                                    const preset = getPreset(connId, selSchema, selTable);
                                    const snaps = listSnapshots(key);
                                    if (!preset) return;
                                    const now = Date.now();
                                    setDelPresetRestore({
                                      preset,
                                      snapshots: snaps,
                                      at: now,
                                      expiresAt: now + 30000,
                                      connId,
                                      schema: selSchema,
                                      table: selTable,
                                    });
                                    addDeletedPresetTombstone(preset, snaps);
                                    deletePreset(connId, selSchema, selTable);
                                    setAppliedPreset(null);
                                    setPresetApplied(null);
                                    setPresetChipExpanded(false);
                                    setDelPresetConfirmAt(null);
                                    publishEditorStatus({
                                      message: `🗑 已删除预设 ${selSchema}.${selTable}（含 ${snaps.length} 个快照历史），30s 内可撤销`,
                                      messageAt: Date.now(),
                                    });
                                  }}
                                  style={{
                                    ...styles.btnSm, padding: '2px 8px', fontSize: 10,
                                    color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)',
                                    background: 'rgba(220,38,38,0.08)', fontWeight: 700,
                                  }}
                                  title="确认永久删除预设及快照（5s 内未操作将自动取消）"
                                >⚠ 确认删除</button>
                                <button
                                  onClick={() => setDelPresetConfirmAt(null)}
                                  style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 10 }}
                                  title="取消确认"
                                >✕ 取消</button>
                                <span style={{
                                  fontSize: 10, color: 'var(--warn, #d97706)',
                                  marginLeft: 2, fontWeight: 600,
                                }} title="距自动取消">{left}s</span>
                              </span>
                            );
                          })()}
                          <button
                            onClick={() => setPresetChipExpanded(false)}
                            style={{ ...styles.btnSm, padding: '2px 8px', fontSize: 10, marginLeft: 'auto' }}
                            title="关闭详情"
                          >✕ 收起</button>
                        </div>
                      </div>
                    )}
                  </div>
                );
              })()}
              <div style={styles.footer}>
                <button
                  onClick={() => setStep(inputFormat === 'sql' ? 'input' : 'map')}
                  style={styles.btnGhost}
                  disabled={busy}
                >← 上一步</button>
                {busy && (
                  <button
                    onClick={handleCancelImport}
                    style={{ ...styles.btnGhost, color: 'var(--danger, #dc2626)', borderColor: 'var(--danger, #dc2626)' }}
                    title="请求取消：已执行但未提交的事务将回滚"
                  >⏹ 取消</button>
                )}
                <span style={styles.spacer} />
                <button
                  onClick={() => downloadReport('csv')}
                  style={styles.btn}
                  title="下载导入报告为 CSV（列出成功/失败行）"
                >📄 CSV</button>
                <button
                  onClick={() => downloadReport('json')}
                  style={styles.btn}
                  title="下载导入报告为 JSON"
                >📄 JSON</button>
                <button onClick={() => void runImport()} disabled={busy || statements.length === 0} style={styles.btnPrimary} title={(() => {
                  const parts: string[] = [];
                  if (qualityGateBlocked && !qualityGateOverrideRef.current) {
                    parts.push('质量门禁拦截：请「强制继续」或调整映射');
                    parts.push('Ctrl+Enter → 提交导入');
                    return parts.join('\n');
                  }
                  if (inputFormat === 'sql') {
                    parts.push(`执行 ${statements.length} 条 SQL 语句`);
                  } else {
                    parts.push(`目标 ${selSchema}.${selTable}`);
                    parts.push(`映射 ${mappedCount}/${parse?.columns.length ?? 0} 列`);
                    parts.push(`${statements.length} 行待导入`);
                    parts.push(`模式 ${opts.mode}`);
                    if (opts.filterColumn !== null && opts.filterValue !== '') {
                      parts.push(`过滤 列${opts.filterColumn + 1} ${opts.filterOp} "${opts.filterValue}"`);
                    } else {
                      parts.push('无过滤');
                    }
                    if (qualityGateBlocked) {
                      parts.push(`🚫 门禁拦截（评分 ${qualityReport?.overallScore}/${qualityGate.threshold}）`);
                    } else if (qualityGate.enabled) {
                      parts.push(`✅ 门禁通过（评分 ${qualityReport?.overallScore}/${qualityGate.threshold}）`);
                    } else {
                      parts.push('门禁关闭');
                    }
                    if (retryIndices && retryIndices.size > 0) parts.push(`仅重跑 ${retryIndices.size} 行`);
                    parts.push(`批量 ${opts.batchSize}`);
                    parts.push(`失败行 ${opts.skipFailed ? '跳过并继续' : '首个失败即回滚'}`);
                    parts.push(`空串当NULL ${opts.emptyAsNull ? '是' : '否'}`);
                    const _existingPreset = getPreset(connId, selSchema, selTable);
                    if (_existingPreset) {
                      const _savedAt = new Date(_existingPreset.createdAt).toLocaleString('zh-CN', {
                        month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
                      });
                      parts.push(`📝 成功后将覆盖预设 ${selSchema}.${selTable}（上次 ${_savedAt}）`);
                    } else {
                      parts.push(`📋 成功后将新建预设 ${selSchema}.${selTable}`);
                    }
                  }
                  parts.push('Ctrl+Enter → 提交导入');
                  return parts.join('\n');
                })()}>
                  {busy ? '执行中…' : retryIndices
                    ? `▶ 只重跑 ${retryIndices.size} 条`
                    : inputFormat === 'sql'
                      ? `▶ 执行 ${statements.length} 条 SQL`
                      : qualityGateBlocked && !qualityGateOverrideRef.current
                        ? `🚫 门禁拦截（${qualityReport?.overallScore}/${qualityGate.threshold}）`
                        : `▶ 开始导入 ${statements.length} 行`}
                </button>
              </div>
            </div>
          )}

          {step === 'done' && (
            <StepDone
              inputFormat={inputFormat}
              statements={statements}
              insertedCount={insertedCount}
              failedRows={failedRows}
              failureSummary={failureSummary}
              failCategoryFilter={failCategoryFilter} setFailCategoryFilter={setFailCategoryFilter}
              progress={progress}
              autoPreviewOnSuccess={autoPreviewOnSuccess} setAutoPreviewOnSuccess={setAutoPreviewOnSuccess}
              autoPreviewRows={autoPreviewRows} setAutoPreviewRows={setAutoPreviewRows}
              fileQueue={fileQueue} queuePos={queuePos} loadQueueNext={loadQueueNext}
              selTable={selTable}
              previewImported={previewImported}
              downloadReport={downloadReport}
              copyReportSummary={copyReportSummary}
              finishAndClose={finishAndClose}
              setStep={setStep}
            />
          )}
        </div>
      </div>
    </div>
  );
}
