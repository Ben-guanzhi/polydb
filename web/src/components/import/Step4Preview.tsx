import { Fragment } from 'react';
import type { CSSProperties, MutableRefObject, Dispatch, SetStateAction } from 'react';
import { classifyFailReason, colDtypeColor, failCategoryMeta, formatValue, qualityScoreColor } from '../../lib/importDisplay';
import { addDeletedPresetTombstone, applyPreset, deletePreset, diffPreset, getPreset, listSnapshots, presetKey, restoreDeletedPreset, restoreSnapshot, type ImportPreset } from '../../lib/importPresets';
import { hasAnyValidation, validateCell, type ImportFormat, type ImportOptions, type Mapping, type ColumnValidation, type RowValidationResult } from '../../lib/importData';
import type { Delimiter, CsvParseResult } from '../../lib/csvParser';
import type { SqlStatement } from '../../lib/sqlSplit';
import { type DataQualityReport, suggestTransforms } from '../../lib/dataQuality';
import type { ImportHistoryEntry } from '../../lib/importHistory';
import { publishEditorStatus } from '../../lib/statusBus';
import { styles } from '../importStyles';
import type { ColumnInfo, DatabaseKind, Value } from '../../api';
import type { FixEntry, DiffSearchScope, FailedRow, DelPresetRestoreState, Step } from './types';
import { MAX_FIX_HISTORY } from './types';
import type { Step3MapProps } from './Step3Map';

// M30.04 预览 & 执行步（Step 4）：SQL 预览、行数/体积、质量门禁、批量执行进度（待提取 props，占位签名）
export interface Step4PreviewProps {
  // —— 身份/上下文 ——
  connId: string;
  kindProp: DatabaseKind | null;
  selSchema: string;
  selTable: string;
  inputFormat: ImportFormat;
  parse: CsvParseResult | null;
  opts: ImportOptions;
  setOpts: Dispatch<SetStateAction<ImportOptions>>;
  setStep: Dispatch<SetStateAction<Step>>;
  setError: Dispatch<SetStateAction<string | null>>;
  // —— 数据/派生 ——
  cols: ColumnInfo[];
  colInfoMap: Map<string, ColumnInfo>;
  mappings: Mapping[];
  setMappings: Dispatch<SetStateAction<Mapping[]>>;
  targetColNames: string[];
  mappedCount: number;
  delimiter: Delimiter | null;
  validations: Record<number, ColumnValidation>;
  validationResult: RowValidationResult | null;
  previewSql: string;
  sqlStatements: SqlStatement[];
  sqlParamCounts: number[];
  csvStatements: { sql: string; params: Value[]; origIdx: number }[];
  statements: SqlStatement[] | { sql: string; params: Value[]; origIdx: number }[];
  failedRows: FailedRow[];
  filteredFailedRows: FailedRow[];
  failureSummary: { key: string; label: string; count: number; rows: number[]; hint: string }[];
  columnHealths: Step3MapProps['columnHealths'];
  qualityReport: DataQualityReport | null;
  qualityGate: Step3MapProps['qualityGate'];
  setQualityGate: Dispatch<SetStateAction<Step3MapProps['qualityGate']>>;
  qualityGateBlocked: boolean;
  qualityGateOverrideRef: MutableRefObject<boolean>;
  // —— PK/FK/重复预检 ——
  pkCheckEnabled: boolean;
  setPkCheckEnabled: Dispatch<SetStateAction<boolean>>;
  pkCheckBusy: boolean;
  pkCheckResult: { found: number; total: number; ms: number; error: string | null } | null;
  checkPkExistence: () => Promise<void>;
  dupPkCheckEnabled: boolean;
  setDupPkCheckEnabled: Dispatch<SetStateAction<boolean>>;
  dupPkCheckBusy: boolean;
  dupPkResult: { duplicates: { csvRow: number; key: string }[]; totalKeys: number; dupKeys: number; ms: number; error: string | null } | null;
  checkDuplicatePk: () => void;
  fkCheckEnabled: boolean;
  setFkCheckEnabled: Dispatch<SetStateAction<boolean>>;
  fkCheckBusy: boolean;
  fkCheckResult: { valid: number; invalid: number; total: number; ms: number; error: string | null } | null;
  checkFkValidity: () => Promise<void>;
  // —— 执行/进度/重试 ——
  busy: boolean;
  progress: { done: number; total: number; ms: number; batch: number; batchTotal: number; batchDone: number; batchSize: number; batchMs: number[] } | null;
  runImport: () => Promise<void>;
  handleCancelImport: () => void;
  retryIndices: Set<number> | null;
  setRetryIndices: Dispatch<SetStateAction<Set<number> | null>>;
  overrides: Record<number, string[]>;
  setOverrides: Dispatch<SetStateAction<Record<number, string[]>>>;
  downloadReport: (format: 'csv' | 'json') => void;
  copyFailedRowsAsInsertSql: () => Promise<void>;
  // —— 撤销/重做历史 ——
  undoStack: FixEntry[];
  redoStack: FixEntry[];
  setUndoStack: Dispatch<SetStateAction<FixEntry[]>>;
  batchStepN: number;
  setBatchStepN: Dispatch<SetStateAction<number>>;
  setBatchSel: Dispatch<SetStateAction<Set<number>>>;
  setBatchAnchor: Dispatch<SetStateAction<number | null>>;
  undoFixN: (n: number) => void;
  redoFixN: (n: number) => void;
  buildUndoDiff: (a: ImportOptions, b: ImportOptions) => { field: string; before: string; after: string }[];
  densityAlpha: (n: number) => number;
  exportUndoTimeline: (format?: 'md' | 'json' | 'csv') => Promise<void>;
  applyCategoryFix: (key: string) => { ok: boolean; msg: string };
  // —— diff 面板 ——
  diffOpen: boolean;
  setDiffOpen: Dispatch<SetStateAction<boolean>>;
  collapsedGroups: Set<string>;
  setCollapsedGroups: Dispatch<SetStateAction<Set<string>>>;
  groupDiffItems: (items: { field: string; before: string; after: string }[]) => { group: string; items: { field: string; before: string; after: string }[] }[];
  filterDiffGroups: (groups: { group: string; items: { field: string; before: string; after: string }[] }[]) => { group: string; items: { field: string; before: string; after: string }[] }[];
  diffGroupColor: (g: string) => string;
  navDiffHit: (delta: 1 | -1) => void;
  getVisibleDiffItems: () => { group: string; item: { field: string; before: string; after: string } }[];
  copyDiffSummary: (items: { field: string; before: string; after: string }[], title?: string) => Promise<void>;
  copyAllItems: (items: { field: string; before: string; after: string }[]) => Promise<void>;
  diffCursor: number;
  setDiffCursor: Dispatch<SetStateAction<number>>;
  diffSearch: string;
  setDiffSearch: Dispatch<SetStateAction<string>>;
  diffSearchCaseSensitive: boolean;
  setDiffSearchCaseSensitive: Dispatch<SetStateAction<boolean>>;
  diffSearchScope: DiffSearchScope;
  setDiffSearchScope: Dispatch<SetStateAction<DiffSearchScope>>;
  diffSearchHistory: string[];
  setDiffSearchHistory: Dispatch<SetStateAction<string[]>>;
  diffSearchHistoryOpen: boolean;
  setDiffSearchHistoryOpen: Dispatch<SetStateAction<boolean>>;
  diffSearchHistoryCursor: number;
  setDiffSearchHistoryCursor: Dispatch<SetStateAction<number>>;
  diffHitCursor: number;
  setDiffHitCursor: Dispatch<SetStateAction<number>>;
  commitDiffSearchHistory: (q: string) => void;
  diffSearchInputRef: MutableRefObject<HTMLInputElement | null>;
  splitDiffSearchKeywords: (qRaw: string) => string[];
  splitDiffSearchParts: (text: string, query: string, caseSensitive: boolean) => { text: string; hit: boolean; kwIdx?: number }[];
  diffKeywordColor: (kwIdx: number) => { bg: string; fg: string };
  classifyDiffItem: (it: { before: string; after: string }) => 'added' | 'removed' | 'changed';
  diffItemColor: (kind: 'added' | 'removed' | 'changed') => string;
  diffItemLabel: (kind: 'added' | 'removed' | 'changed') => string;
  diffItemIcon: (kind: 'added' | 'removed' | 'changed') => string;
  getDiffItemRef: (groupKey: string, field: string) => (el: HTMLElement | null) => void;
  canRevertSingleItem: (item: { field: string; before: string; after: string }, targetSnap: ImportOptions) => boolean;
  applySingleItemUndo: (item: { field: string; before: string; after: string }, targetSnap: ImportOptions) => void;
  // —— 失败行 ——
  failCategoryFilter: string | null;
  setFailCategoryFilter: Dispatch<SetStateAction<string | null>>;
  expandedFailRow: number | null;
  setExpandedFailRow: Dispatch<SetStateAction<number | null>>;
  rawRowFor: (csvRow: number) => string[] | null;
  jumpToCsvRow: (csvRow: number) => void;
  history: ImportHistoryEntry[];
  // —— 预设 chip / 删除恢复 ——
  setAppliedPreset: Dispatch<SetStateAction<ImportPreset | null>>;
  setPresetApplied: Dispatch<SetStateAction<Step3MapProps['presetApplied']>>;
  setPresetSnapshotsRefresh: Dispatch<SetStateAction<number>>;
  presetChipExpanded: boolean;
  setPresetChipExpanded: Dispatch<SetStateAction<boolean>>;
  snapDiffTs: number | null;
  setSnapDiffTs: Dispatch<SetStateAction<number | null>>;
  pendingRestoreConfirm: { ts: number; idx: number } | null;
  setPendingRestoreConfirm: Dispatch<SetStateAction<{ ts: number; idx: number } | null>>;
  delPresetRestore: DelPresetRestoreState | null;
  setDelPresetRestore: Dispatch<SetStateAction<DelPresetRestoreState | null>>;
  delPresetRestoreTick: number;
  delPresetTick: number;
  applyOldConfirmAt: number | null;
  setApplyOldConfirmAt: Dispatch<SetStateAction<number | null>>;
  applyOldTick: number;
  presetCopyAt: number | null;
  setPresetCopyAt: Dispatch<SetStateAction<number | null>>;
  delPresetConfirmAt: number | null;
  setDelPresetConfirmAt: Dispatch<SetStateAction<number | null>>;
  copiedChipText: string | null;
  setCopiedChipText: Dispatch<SetStateAction<string | null>>;
}

export function Step4Preview(p: Step4PreviewProps) {
  const {
    connId, kindProp, selSchema, selTable, inputFormat, parse, opts, setOpts, setStep, setError,
    cols, colInfoMap, mappings, setMappings, targetColNames, mappedCount, delimiter, validations,
    validationResult, previewSql, sqlStatements, sqlParamCounts, csvStatements, statements,
    failedRows, filteredFailedRows, failureSummary, columnHealths, qualityReport, qualityGate,
    setQualityGate, qualityGateBlocked, qualityGateOverrideRef,
    pkCheckEnabled, setPkCheckEnabled, pkCheckBusy, pkCheckResult, checkPkExistence,
    dupPkCheckEnabled, setDupPkCheckEnabled, dupPkCheckBusy, dupPkResult, checkDuplicatePk,
    fkCheckEnabled, setFkCheckEnabled, fkCheckBusy, fkCheckResult, checkFkValidity,
    busy, progress, runImport, handleCancelImport, retryIndices, setRetryIndices, overrides,
    setOverrides, downloadReport, copyFailedRowsAsInsertSql,
    undoStack, redoStack, setUndoStack, batchStepN, setBatchStepN, setBatchSel, setBatchAnchor,
    undoFixN, redoFixN, buildUndoDiff, densityAlpha, exportUndoTimeline, applyCategoryFix,
    diffOpen, setDiffOpen, collapsedGroups, setCollapsedGroups, groupDiffItems, filterDiffGroups,
    diffGroupColor, navDiffHit, getVisibleDiffItems, copyDiffSummary, copyAllItems, diffCursor,
    setDiffCursor, diffSearch, setDiffSearch, diffSearchCaseSensitive, setDiffSearchCaseSensitive,
    diffSearchScope, setDiffSearchScope, diffSearchHistory, setDiffSearchHistory,
    diffSearchHistoryOpen, setDiffSearchHistoryOpen, diffSearchHistoryCursor,
    setDiffSearchHistoryCursor, diffHitCursor, setDiffHitCursor, commitDiffSearchHistory,
    diffSearchInputRef, splitDiffSearchKeywords, splitDiffSearchParts, diffKeywordColor, classifyDiffItem, diffItemColor,
    diffItemLabel, diffItemIcon, getDiffItemRef, canRevertSingleItem, applySingleItemUndo,
    failCategoryFilter, setFailCategoryFilter, expandedFailRow, setExpandedFailRow, rawRowFor,
    jumpToCsvRow, history,
    setAppliedPreset, setPresetApplied, setPresetSnapshotsRefresh, presetChipExpanded,
    setPresetChipExpanded, snapDiffTs, setSnapDiffTs, pendingRestoreConfirm,
    setPendingRestoreConfirm, delPresetRestore, setDelPresetRestore, delPresetRestoreTick,
    delPresetTick, applyOldConfirmAt, setApplyOldConfirmAt, applyOldTick, presetCopyAt,
    setPresetCopyAt, delPresetConfirmAt, setDelPresetConfirmAt, copiedChipText, setCopiedChipText,
  } = p;
  return (
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
  );
}