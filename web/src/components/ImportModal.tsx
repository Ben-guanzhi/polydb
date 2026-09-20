import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import {
  formatTime,
  historyStatusBadge,
  Step1Input,
  Step2Table,
  Step3Map,
  Step4Preview,
  StepBar,
  StepDone,
  styles,
  toMsg,
  type Step,
  type MappingFilter,
  type MappingSort,
  type FixEntry,
  MAX_FIX_HISTORY,
  type DiffSearchScope,
  type FailedRow,
  PresetManager,
  type BackupAppliedDetail,
} from './ImportModalParts';
import { STEP_ORDER } from './ImportModalParts';
import {
  classifyFailReason,
  failCategoryMeta,
  previewParams,
  sqlLiteral,
} from '../lib/importDisplay';
import {
  classifyDiffItem, FIX_STACK_MAX_BYTES, DIFF_EMPTY_MARKS, diffGroupColor, diffItemColor, diffItemIcon,
  diffItemLabel, diffKeywordColor, splitDiffSearchKeywords, splitDiffSearchParts,
  buildUndoDiff as buildUndoDiffLib, copyAllItems, copyDiffSummary, densityAlpha, groupDiffItems,
  filterDiffGroups as filterDiffGroupsLib,
} from '../lib/importDiff';
import {
  buildRiskWhitelistExport, loadRiskWhitelist, mergeWhitelistItems, parseRiskWhitelistCsv,
  parseRiskWhitelistJson, persistRiskWhitelist, RISK_WHITELIST_KEY, removeExpiredWhitelist,
  whitelistKeyLabel,
} from '../lib/riskWhitelist';
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
  hasAnyValidation,
  inferMapping,
  parseJsonl,
  profileColumns,
  qualTable,
  quoteIdent,
  suggestMappings,
  validateAllRows,
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
import { lintSql } from '../lib/sqlLint';
import ContextMenu, { type ContextMenuEntry } from './ContextMenu';
import { scoreDataQuality, type DataQualityReport, type QualityTransformSuggestion } from '../lib/dataQuality';
import {
  applyPreset,
  applyPresetsBackup,
  buildPreset,
  getPreset,
  loadAuditCache,
  presetKey,
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

interface Props {
  connId: string;
  kind: DatabaseKind | null;
  contextSchema?: string | null;
  contextTable?: string | null;
  onClose: () => void;
}




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

  const [riskWhitelist, setRiskWhitelist] = useState<Map<string, number>>(loadRiskWhitelist);
  const addToRiskWhitelist = (key: string) => {
    setRiskWhitelist((prev) => {
      if (prev.has(key)) return prev;
      const next = mergeWhitelistItems(prev, [{ key, addedAt: Date.now() }], 'merge');
      persistRiskWhitelist(next);
      return next;
    });
    setBackupSelected((prev) => {
      if (!prev.has(key)) return prev;
      const next = new Set(prev);
      next.delete(key);
      return next;
    });
    setPresetManagerMsg({ kind: 'ok', text: `🔒 已加入白名单并剔除（30 天后自动失效）：${whitelistKeyLabel(key)}` });
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
      persistRiskWhitelist(next);
      return next;
    });
    setWhitelistSel((prev) => { const next = new Set(prev); next.delete(key); return next; });
    setPresetManagerMsg({ kind: 'ok', text: `🔓 已从白名单移除：${whitelistKeyLabel(key)}` });
  };
  const clearRiskWhitelist = () => {
    if (riskWhitelist.size === 0) return;
    const n = riskWhitelist.size;
    setRiskWhitelist(new Map());
    setWhitelistSel(new Set());
    whitelistAnchorIdx.current = null;
    try { localStorage.removeItem(RISK_WHITELIST_KEY); } catch { /* ignore */ }
    setPresetManagerMsg({ kind: 'ok', text: `🧹 已清空白名单（${n} 项）` });
    setRiskWhitelistOpen(false);
  };
  // M30.156 C 每次打开 presetManager 时清理一次过期项（M30.160 D：只扫当前 map，不依赖新增即清理）
  useEffect(() => {
    if (!presetManagerOpen || riskWhitelist.size === 0) return;
    const { next, removed } = removeExpiredWhitelist(riskWhitelist);
    if (removed === 0) return;
    persistRiskWhitelist(next);
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
    const { text, filename, mime } = buildRiskWhitelistExport(riskWhitelist, whitelistExportFormat);
    const blob = new Blob([text], { type: mime });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    setPresetManagerMsg({ kind: 'ok', text: `📦 已导出白名单 ${filename}（${riskWhitelist.size} 项 · ${text.length} 字符）` });
  };
  const handleRiskWhitelistImport = async (file: File) => {
    try {
      const text = await file.text();
      const parsed = parseRiskWhitelistJson(text);
      if (!parsed.ok) {
        setPresetManagerMsg({ kind: 'err', text: parsed.error });
        return;
      }
      if (parsed.items.length === 0) {
        setPresetManagerMsg({ kind: 'err', text: '⚠ 导入文件 items 为空' });
        return;
      }
      setRiskWhitelist((prev) => {
        const next = mergeWhitelistItems(prev, parsed.items, whitelistImportMode);
        persistRiskWhitelist(next);
        return next;
      });
      const verb = whitelistImportMode === 'replace' ? '覆盖' : '合并';
      setPresetManagerMsg({ kind: 'ok', text: `📥 已${verb}白名单：导入 ${parsed.items.length} 项 · 当前 ${whitelistImportMode === 'replace' ? parsed.items.length : '见浮层'} 项` });
    } finally {
      if (whitelistFileRef.current) whitelistFileRef.current.value = '';
    }
  };
  // M30.157 A 白名单 CSV 导入：RFC 4180 简易解析（引号包裹 + 双引号转义）
  // 表头 key,addedAt（首行忽略），若表头顺序颠倒或缺失列会报错
  const handleRiskWhitelistCsvImport = async (file: File) => {
    try {
      const text = await file.text();
      const parsed = parseRiskWhitelistCsv(text);
      if (!parsed.ok) {
        setPresetManagerMsg({ kind: 'err', text: parsed.error });
        return;
      }
      if (parsed.items.length === 0) {
        setPresetManagerMsg({ kind: 'err', text: '⚠ CSV 无有效数据行' });
        return;
      }
      setRiskWhitelist((prev) => {
        const next = mergeWhitelistItems(prev, parsed.items, whitelistImportMode);
        persistRiskWhitelist(next);
        return next;
      });
      const verb = whitelistImportMode === 'replace' ? '覆盖' : '合并';
      setPresetManagerMsg({ kind: 'ok', text: `📥 已${verb}白名单（CSV）：导入 ${parsed.items.length} 项 · 当前 ${whitelistImportMode === 'replace' ? parsed.items.length : '见浮层'} 项` });
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
  // 撤销/重做栈持久化到 localStorage 'polydb.undoStack.v1'（含 redoStack + batchStepN）
  // 惰性恢复：解析失败/类型不符/size>512KB 时静默丢弃；QuotaExceededError 时上报一次
  const fixStackWarnRef = useRef(false);
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

  const buildUndoDiff = (a: ImportOptions, b: ImportOptions) => buildUndoDiffLib(a, b, (i) => colLabel(i));

  // 分组策略：field 首 token 作为 groupKey
  // 「转换 transform ...」→「转换」；「空值策略 nullPolicy ...」→「空值策略」；「校验 validation ...」→「校验」
  // 其他 field 直接以自身为 groupKey（模式/严格校验/空串视为 null/批大小/行过滤）


  // 每组分色：按语义分配到色卡（未识别分组 fallback 到 muted）

  // 栈可视条按 fix 字段变化密度着色：density=1 淡（rgba 0.30），density>=4 饱和（rgba 0.95）
  // undo 段用红色 rgba(220,38,38,α)，redo 段用蓝色 rgba(59,130,246,α)




  // 搜索过滤：按 field/before/after 子串（默认不区分大小写；diffSearchCaseSensitive=true 时精确匹配）
  // 支持多关键字 OR 匹配：空白或 `|` 分隔，任一命中即保留（M30.32）
  // 支持作用域过滤（M30.33）：diffSearchScope='all|field|before|after'，非 all 时仅匹配对应字段
  // 副作用：命中分组的折叠态会被自动展开（下一次渲染时用户能看到匹配内容）
  const filterDiffGroups = (groups: { group: string; items: { field: string; before: string; after: string }[] }[]):
    { group: string; items: { field: string; before: string; after: string }[] }[] =>
    filterDiffGroupsLib(groups, { search: diffSearch, caseSensitive: diffSearchCaseSensitive, scope: diffSearchScope });

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



  // 按分组复制：分组标题作为二级标题，组内 items 缩进


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
        {presetManagerOpen && (
          <PresetManager
            connId={connId}
            kindProp={kindProp}
            selSchema={selSchema}
            selTable={selTable}
            opts={opts}
            mappings={mappings}
            setSelSchema={setSelSchema}
            setSelTable={setSelTable}
            setStep={setStep}
            snapDiffTs={snapDiffTs}
            setSnapDiffTs={setSnapDiffTs}
            copiedChipText={copiedChipText}
            setCopiedChipText={setCopiedChipText}
            presetListRefresh={presetListRefresh}
            setPresetListRefresh={setPresetListRefresh}
            setPresetSnapshotsRefresh={setPresetSnapshotsRefresh}
            presetAuditFilter={presetAuditFilter}
            setPresetAuditFilter={setPresetAuditFilter}
            presetAuditResults={presetAuditResults}
            setPresetAuditResults={setPresetAuditResults}
            presetAuditCachedAt={presetAuditCachedAt}
            setPresetAuditCachedAt={setPresetAuditCachedAt}
            presetAuditBusy={presetAuditBusy}
            setPresetAuditBusy={setPresetAuditBusy}
            presetAuditCheckedRef={presetAuditCheckedRef}
            pendingPresetJumpConfirm={pendingPresetJumpConfirm}
            setPendingPresetJumpConfirm={setPendingPresetJumpConfirm}
            reAuditOne={reAuditOne}
            pendingTombstoneConfirm={pendingTombstoneConfirm}
            setPendingTombstoneConfirm={setPendingTombstoneConfirm}
            presetManagerBusy={presetManagerBusy}
            setPresetManagerBusy={setPresetManagerBusy}
            presetManagerMsg={presetManagerMsg}
            setPresetManagerMsg={setPresetManagerMsg}
            presetManagerFileRef={presetManagerFileRef}
            setPresetManagerOpen={setPresetManagerOpen}
            presetManagerOverwrite={presetManagerOverwrite}
            setPresetManagerOverwrite={setPresetManagerOverwrite}
            backupPending={backupPending}
            setBackupPending={setBackupPending}
            backupSelected={backupSelected}
            setBackupSelected={setBackupSelected}
            backupItemAudit={backupItemAudit}
            setBackupItemAudit={setBackupItemAudit}
            backupItemAuditRef={backupItemAuditRef}
            backupItemAuditBusy={backupItemAuditBusy}
            setBackupItemAuditBusy={setBackupItemAuditBusy}
            backupPreviewSearch={backupPreviewSearch}
            setBackupPreviewSearch={setBackupPreviewSearch}
            backupPreviewFilter={backupPreviewFilter}
            setBackupPreviewFilter={setBackupPreviewFilter}
            backupPreviewKindFilter={backupPreviewKindFilter}
            setBackupPreviewKindFilter={setBackupPreviewKindFilter}
            backupSortBy={backupSortBy}
            setBackupSortBy={setBackupSortBy}
            backupRiskLevelFilter={backupRiskLevelFilter}
            setBackupRiskLevelFilter={setBackupRiskLevelFilter}
            backupRiskThreshold={backupRiskThreshold}
            setBackupRiskThreshold={setBackupRiskThreshold}
            backupPreviewAllDiff={backupPreviewAllDiff}
            setBackupPreviewAllDiff={setBackupPreviewAllDiff}
            backupDiffBinFilter={backupDiffBinFilter}
            setBackupDiffBinFilter={setBackupDiffBinFilter}
            backupSourceFileName={backupSourceFileName}
            setBackupSourceFileName={setBackupSourceFileName}
            backupSourceHash={backupSourceHash}
            setBackupSourceHash={setBackupSourceHash}
            backupWhitelistSkipped={backupWhitelistSkipped}
            setBackupWhitelistSkipped={setBackupWhitelistSkipped}
            backupEmbeddedWhitelist={backupEmbeddedWhitelist}
            setBackupEmbeddedWhitelist={setBackupEmbeddedWhitelist}
            backupDiffCollapsed={backupDiffCollapsed}
            setBackupDiffCollapsed={setBackupDiffCollapsed}
            backupShortcutsOpen={backupShortcutsOpen}
            setBackupShortcutsOpen={setBackupShortcutsOpen}
            backupGroupBy={backupGroupBy}
            setBackupGroupBy={setBackupGroupBy}
            backupCollapsedGroups={backupCollapsedGroups}
            setBackupCollapsedGroups={setBackupCollapsedGroups}
            backupGroupSort={backupGroupSort}
            setBackupGroupSort={setBackupGroupSort}
            backupGroupExportFmt={backupGroupExportFmt}
            setBackupGroupExportFmt={setBackupGroupExportFmt}
            backupFocusIdx={backupFocusIdx}
            backupPreviewSorted={backupPreviewSorted}
            backupPreviewFiltered={backupPreviewFiltered}
            backupPreviewGroupOrderMap={backupPreviewGroupOrderMap}
            computeItemRiskScore={computeItemRiskScore}
            riskScoreOf={riskScoreOf}
            diffCountOf={diffCountOf}
            highlightMatch={highlightMatch}
            pendingBackupUndo={pendingBackupUndo}
            backupUndoLeft={backupUndoLeft}
            lastApplyAddedPresets={lastApplyAddedPresets}
            lastApplyAuditSummary={lastApplyAuditSummary}
            pendingBackupUndoImpact={pendingBackupUndoImpact}
            undoImpactDrillOpen={undoImpactDrillOpen}
            setUndoImpactDrillOpen={setUndoImpactDrillOpen}
            backupUndoPreviewText={backupUndoPreviewText}
            setBackupUndoPreviewText={setBackupUndoPreviewText}
            applyBackupUndo={applyBackupUndo}
            backupConfirmOverlayOpen={backupConfirmOverlayOpen}
            setBackupConfirmOverlayOpen={setBackupConfirmOverlayOpen}
            doApplyBackup={doApplyBackup}
            confirmApplyBackup={confirmApplyBackup}
            resetBackupView={resetBackupView}
            cancelBackup={cancelBackup}
            lastBackupAppliedDetail={lastBackupAppliedDetail}
            backupAppliedDetailOpen={backupAppliedDetailOpen}
            setBackupAppliedDetailOpen={setBackupAppliedDetailOpen}
            backupDetailFilter={backupDetailFilter}
            setBackupDetailFilter={setBackupDetailFilter}
            backupDetailFocusIdx={backupDetailFocusIdx}
            setBackupDetailFocusIdx={setBackupDetailFocusIdx}
            riskWhitelistOpen={riskWhitelistOpen}
            setRiskWhitelistOpen={setRiskWhitelistOpen}
            riskWhitelist={riskWhitelist}
            setRiskWhitelist={setRiskWhitelist}
            whitelistSearch={whitelistSearch}
            setWhitelistSearch={setWhitelistSearch}
            whitelistKindFilter={whitelistKindFilter}
            setWhitelistKindFilter={setWhitelistKindFilter}
            whitelistSortBy={whitelistSortBy}
            setWhitelistSortBy={setWhitelistSortBy}
            whitelistSel={whitelistSel}
            setWhitelistSel={setWhitelistSel}
            whitelistAnchorIdx={whitelistAnchorIdx}
            whitelistFocusIdx={whitelistFocusIdx}
            removeRiskWhitelistItem={removeRiskWhitelistItem}
            clearRiskWhitelist={clearRiskWhitelist}
            exportRiskWhitelist={exportRiskWhitelist}
            whitelistExportFormat={whitelistExportFormat}
            setWhitelistExportFormat={setWhitelistExportFormat}
            whitelistFileRef={whitelistFileRef}
            whitelistCsvFileRef={whitelistCsvFileRef}
            handleRiskWhitelistImport={handleRiskWhitelistImport}
            handleRiskWhitelistCsvImport={handleRiskWhitelistCsvImport}
            whitelistImportMode={whitelistImportMode}
            setWhitelistImportMode={setWhitelistImportMode}
            setRiskItemCtxMenu={setRiskItemCtxMenu}
          />
        )}

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
            <Step1Input
              kind={kind}
              inputFormat={inputFormat}
              setInputFormat={setInputFormat}
              delimiter={delimiter}
              setDelimiter={setDelimiter}
              hasHeader={hasHeader}
              setHasHeader={setHasHeader}
              encoding={encoding}
              redecodeWith={redecodeWith}
              csvText={csvText}
              setCsvText={setCsvText}
              csvTextRef={csvTextRef}
              csvRowFlash={csvRowFlash}
              fileInputRef={fileInputRef}
              fileName={fileName}
              parse={parse}
              detectedEncoding={detectedEncoding}
              sqlStatements={sqlStatements}
              sqlParamCounts={sqlParamCounts}
              sqlLintResult={sqlLintResult}
              profiles={profiles}
              qualityReport={qualityReport}
              qualityGate={qualityGate}
              qualityGateBlocked={qualityGateBlocked}
              setPendingQualitySuggestions={setPendingQualitySuggestions}
              selSchema={selSchema}
              selTable={selTable}
              fileQueue={fileQueue}
              queuePos={queuePos}
              cancelQueue={cancelQueue}
              enqueueFiles={enqueueFiles}
              handleFile={handleFile}
              detectFormatFromFile={detectFormatFromFile}
              handleParse={handleParse}
            />
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
            <Step3Map
              connId={connId}
              selSchema={selSchema}
              selTable={selTable}
              cols={cols}
              colInfoMap={colInfoMap}
              mappings={mappings}
              opts={opts}
              parse={parse}
              parseRef={parseRef}
              profiles={profiles}
              mappingSuggestions={mappingSuggestions}
              mappedCount={mappedCount}
              skippedCount={skippedCount}
              mappingFilter={mappingFilter}
              mappingSort={mappingSort}
              mappingSearch={mappingSearch}
              mappingSearchHistory={mappingSearchHistory}
              mappingSearchHistOpen={mappingSearchHistOpen}
              mappingSearchHistIdx={mappingSearchHistIdx}
              columnHealths={columnHealths}
              batchSel={batchSel}
              batchAnchor={batchAnchor}
              jumpFlashIdx={jumpFlashIdx}
              presetApplied={presetApplied}
              appliedPreset={appliedPreset}
              qualityGate={qualityGate}
              qualityReport={qualityReport}
              validationResult={validationResult}
              mapSearchInputRef={mapSearchInputRef}
              applyOneClickOptimize={applyOneClickOptimize}
              pushFix={pushFix}
              triggerDownload={triggerDownload}
              persistMappingFilter={persistMappingFilter}
              persistMappingSort={persistMappingSort}
              saveMappingSearchHistory={saveMappingSearchHistory}
              highlightQuery={highlightQuery}
              warnFor={warnFor}
              setError={setError}
              setMappings={setMappings}
              setOpts={setOpts}
              setMappingSearch={setMappingSearch}
              setMappingSearchHistOpen={setMappingSearchHistOpen}
              setMappingSearchHistIdx={setMappingSearchHistIdx}
              setMappingSearchHistory={setMappingSearchHistory}
              setBatchSel={setBatchSel}
              setBatchAnchor={setBatchAnchor}
              setBatchStepN={setBatchStepN}
              setDiffOpen={setDiffOpen}
              setUndoStack={setUndoStack}
              setAppliedPreset={setAppliedPreset}
              setPresetApplied={setPresetApplied}
              setPresetSnapshotsRefresh={setPresetSnapshotsRefresh}
              setStep={setStep}
            />
          )}

          {step === 'preview' && (
            <Step4Preview
              connId={connId}
              kindProp={kindProp}
              selSchema={selSchema}
              selTable={selTable}
              inputFormat={inputFormat}
              parse={parse}
              opts={opts}
              setOpts={setOpts}
              setStep={setStep}
              setError={setError}
              cols={cols}
              colInfoMap={colInfoMap}
              mappings={mappings}
              setMappings={setMappings}
              targetColNames={targetColNames}
              mappedCount={mappedCount}
              delimiter={delimiter}
              validations={validations}
              validationResult={validationResult}
              previewSql={previewSql}
              sqlStatements={sqlStatements}
              sqlParamCounts={sqlParamCounts}
              csvStatements={csvStatements}
              statements={statements}
              failedRows={failedRows}
              filteredFailedRows={filteredFailedRows}
              failureSummary={failureSummary}
              columnHealths={columnHealths}
              qualityReport={qualityReport}
              qualityGate={qualityGate}
              setQualityGate={setQualityGate}
              qualityGateBlocked={qualityGateBlocked}
              qualityGateOverrideRef={qualityGateOverrideRef}
              pkCheckEnabled={pkCheckEnabled}
              setPkCheckEnabled={setPkCheckEnabled}
              pkCheckBusy={pkCheckBusy}
              pkCheckResult={pkCheckResult}
              checkPkExistence={checkPkExistence}
              dupPkCheckEnabled={dupPkCheckEnabled}
              setDupPkCheckEnabled={setDupPkCheckEnabled}
              dupPkCheckBusy={dupPkCheckBusy}
              dupPkResult={dupPkResult}
              checkDuplicatePk={checkDuplicatePk}
              fkCheckEnabled={fkCheckEnabled}
              setFkCheckEnabled={setFkCheckEnabled}
              fkCheckBusy={fkCheckBusy}
              fkCheckResult={fkCheckResult}
              checkFkValidity={checkFkValidity}
              busy={busy}
              progress={progress}
              runImport={runImport}
              handleCancelImport={handleCancelImport}
              retryIndices={retryIndices}
              setRetryIndices={setRetryIndices}
              overrides={overrides}
              setOverrides={setOverrides}
              downloadReport={downloadReport}
              copyFailedRowsAsInsertSql={copyFailedRowsAsInsertSql}
              undoStack={undoStack}
              redoStack={redoStack}
              setUndoStack={setUndoStack}
              batchStepN={batchStepN}
              setBatchStepN={setBatchStepN}
              setBatchSel={setBatchSel}
              setBatchAnchor={setBatchAnchor}
              undoFixN={undoFixN}
              redoFixN={redoFixN}
              buildUndoDiff={buildUndoDiff}
              densityAlpha={densityAlpha}
              exportUndoTimeline={exportUndoTimeline}
              applyCategoryFix={applyCategoryFix}
              diffOpen={diffOpen}
              setDiffOpen={setDiffOpen}
              collapsedGroups={collapsedGroups}
              setCollapsedGroups={setCollapsedGroups}
              groupDiffItems={groupDiffItems}
              filterDiffGroups={filterDiffGroups}
              diffGroupColor={diffGroupColor}
              navDiffHit={navDiffHit}
              getVisibleDiffItems={getVisibleDiffItems}
              copyDiffSummary={copyDiffSummary}
              copyAllItems={copyAllItems}
              diffCursor={diffCursor}
              setDiffCursor={setDiffCursor}
              diffSearch={diffSearch}
              setDiffSearch={setDiffSearch}
              diffSearchCaseSensitive={diffSearchCaseSensitive}
              setDiffSearchCaseSensitive={setDiffSearchCaseSensitive}
              diffSearchScope={diffSearchScope}
              setDiffSearchScope={setDiffSearchScope}
              diffSearchHistory={diffSearchHistory}
              setDiffSearchHistory={setDiffSearchHistory}
              diffSearchHistoryOpen={diffSearchHistoryOpen}
              setDiffSearchHistoryOpen={setDiffSearchHistoryOpen}
              diffSearchHistoryCursor={diffSearchHistoryCursor}
              setDiffSearchHistoryCursor={setDiffSearchHistoryCursor}
              diffHitCursor={diffHitCursor}
              setDiffHitCursor={setDiffHitCursor}
              commitDiffSearchHistory={commitDiffSearchHistory}
              diffSearchInputRef={diffSearchInputRef}
              splitDiffSearchKeywords={splitDiffSearchKeywords}
              splitDiffSearchParts={splitDiffSearchParts}
              diffKeywordColor={diffKeywordColor}
              classifyDiffItem={classifyDiffItem}
              diffItemColor={diffItemColor}
              diffItemLabel={diffItemLabel}
              diffItemIcon={diffItemIcon}
              getDiffItemRef={getDiffItemRef}
              canRevertSingleItem={canRevertSingleItem}
              applySingleItemUndo={applySingleItemUndo}
              failCategoryFilter={failCategoryFilter}
              setFailCategoryFilter={setFailCategoryFilter}
              expandedFailRow={expandedFailRow}
              setExpandedFailRow={setExpandedFailRow}
              rawRowFor={rawRowFor}
              jumpToCsvRow={jumpToCsvRow}
              history={history}
              setAppliedPreset={setAppliedPreset}
              setPresetApplied={setPresetApplied}
              setPresetSnapshotsRefresh={setPresetSnapshotsRefresh}
              presetChipExpanded={presetChipExpanded}
              setPresetChipExpanded={setPresetChipExpanded}
              snapDiffTs={snapDiffTs}
              setSnapDiffTs={setSnapDiffTs}
              pendingRestoreConfirm={pendingRestoreConfirm}
              setPendingRestoreConfirm={setPendingRestoreConfirm}
              delPresetRestore={delPresetRestore}
              setDelPresetRestore={setDelPresetRestore}
              delPresetRestoreTick={delPresetRestoreTick}
              delPresetTick={delPresetTick}
              applyOldConfirmAt={applyOldConfirmAt}
              setApplyOldConfirmAt={setApplyOldConfirmAt}
              applyOldTick={applyOldTick}
              presetCopyAt={presetCopyAt}
              setPresetCopyAt={setPresetCopyAt}
              delPresetConfirmAt={delPresetConfirmAt}
              setDelPresetConfirmAt={setDelPresetConfirmAt}
              copiedChipText={copiedChipText}
              setCopiedChipText={setCopiedChipText}
            />
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
