import * as api from '../../lib/api';
import type { CSSProperties, MutableRefObject, Dispatch, SetStateAction, ReactNode } from 'react';
import { fmtCacheAgeForAudit, formatTime } from '../../lib/importDisplay';
import { addDeletedPresetTombstone, clearAuditCache, deletePreset, diffPreset, exportPresetsBackup, getPreset, listDeletedPresets, listPresets, listSnapshots, loadAuditCache, previewBackup, purgeAllDeletedPresets, purgeDeletedPresetTombstone, restoreDeletedPresetTombstone, saveAuditCache, type BackupApplySnapshot, type BackupPreviewItem, type PresetAuditStatus, type PresetBackup, type UndoImpactDetail, type ImportPreset } from '../../lib/importPresets';
import type { ImportOptions, Mapping } from '../../lib/importData';
import { publishEditorStatus } from '../../lib/statusBus';
import { csvEscape, download as downloadFile } from '../../lib/exporters';
import { styles } from '../importStyles';
import type { DatabaseKind } from '../../api';
import { WHITELIST_TTL_MS, type WhitelistPersisted } from '../../lib/riskWhitelist';
import type { BackupAppliedDetail, Step } from './types';
import { AuditBadge } from './AuditBadge';

// PresetManager 导出文件名时间戳：YYYYMMDD-HHMMSS（原在三处下载 onClick 内各写一份 pad+拼接）
const backupFileStamp = (d: Date): string => {
  const pad = (n: number) => n.toString().padStart(2, '0');
  return `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
};

// M30.05 预设管理器（预设/快照/白名单三 tab，原 ImportModal presetManagerOpen IIFE，~4.4k 行 JSX）
export interface PresetManagerProps {
  // —— 身份/上下文 ——
  connId: string;
  kindProp: DatabaseKind | null;
  selSchema: string;
  selTable: string;
  opts: ImportOptions;
  mappings: Mapping[];
  setSelSchema: Dispatch<SetStateAction<string>>;
  setSelTable: Dispatch<SetStateAction<string>>;
  setStep: Dispatch<SetStateAction<Step>>;
  snapDiffTs: number | null;
  setSnapDiffTs: Dispatch<SetStateAction<number | null>>;
  copiedChipText: string | null;
  setCopiedChipText: Dispatch<SetStateAction<string | null>>;
  // —— 预设列表 / 审计 ——
  presetListRefresh: number;
  setPresetListRefresh: Dispatch<SetStateAction<number>>;
  setPresetSnapshotsRefresh: Dispatch<SetStateAction<number>>;
  presetAuditFilter: 'all' | 'warn' | 'ok';
  setPresetAuditFilter: Dispatch<SetStateAction<'all' | 'warn' | 'ok'>>;
  presetAuditResults: Record<string, { status: PresetAuditStatus; reason: string }> | null;
  setPresetAuditResults: Dispatch<SetStateAction<Record<string, { status: PresetAuditStatus; reason: string }> | null>>;
  presetAuditCachedAt: number | null;
  setPresetAuditCachedAt: Dispatch<SetStateAction<number | null>>;
  presetAuditBusy: boolean;
  setPresetAuditBusy: Dispatch<SetStateAction<boolean>>;
  presetAuditCheckedRef: MutableRefObject<string>;
  pendingPresetJumpConfirm: string | null;
  setPendingPresetJumpConfirm: Dispatch<SetStateAction<string | null>>;
  reAuditOne: (schema: string, table: string) => Promise<void>;
  pendingTombstoneConfirm: { key: string; idx: number } | null;
  setPendingTombstoneConfirm: Dispatch<SetStateAction<{ key: string; idx: number } | null>>;
  // —— 管理器壳 ——
  presetManagerBusy: boolean;
  setPresetManagerBusy: Dispatch<SetStateAction<boolean>>;
  presetManagerMsg: { kind: 'ok' | 'err'; text: string } | null;
  setPresetManagerMsg: Dispatch<SetStateAction<{ kind: 'ok' | 'err'; text: string } | null>>;
  presetManagerFileRef: MutableRefObject<HTMLInputElement | null>;
  setPresetManagerOpen: Dispatch<SetStateAction<boolean>>;
  presetManagerOverwrite: boolean;
  setPresetManagerOverwrite: Dispatch<SetStateAction<boolean>>;
  // —— 备份预览/应用 ——
  backupPending: { backup: PresetBackup; items: BackupPreviewItem[] } | null;
  setBackupPending: Dispatch<SetStateAction<{ backup: PresetBackup; items: BackupPreviewItem[] } | null>>;
  backupSelected: Set<string>;
  setBackupSelected: Dispatch<SetStateAction<Set<string>>>;
  backupItemAudit: Record<string, PresetAuditStatus> | null;
  setBackupItemAudit: Dispatch<SetStateAction<Record<string, PresetAuditStatus> | null>>;
  backupItemAuditRef: MutableRefObject<Set<string>>;
  backupItemAuditBusy: boolean;
  setBackupItemAuditBusy: Dispatch<SetStateAction<boolean>>;
  backupPreviewSearch: string;
  setBackupPreviewSearch: Dispatch<SetStateAction<string>>;
  backupPreviewFilter: 'all' | 'overwrite' | 'add';
  setBackupPreviewFilter: Dispatch<SetStateAction<'all' | 'overwrite' | 'add'>>;
  backupPreviewKindFilter: 'all' | 'preset' | 'snapshot';
  setBackupPreviewKindFilter: Dispatch<SetStateAction<'all' | 'preset' | 'snapshot'>>;
  backupSortBy: 'diff' | 'schema' | 'risk';
  setBackupSortBy: Dispatch<SetStateAction<'diff' | 'schema' | 'risk'>>;
  backupRiskLevelFilter: 'all' | 'low' | 'mid' | 'high';
  setBackupRiskLevelFilter: Dispatch<SetStateAction<'all' | 'low' | 'mid' | 'high'>>;
  backupRiskThreshold: number;
  setBackupRiskThreshold: Dispatch<SetStateAction<number>>;
  backupPreviewAllDiff: boolean;
  setBackupPreviewAllDiff: Dispatch<SetStateAction<boolean>>;
  backupDiffBinFilter: 'all' | '0' | '1' | '2' | '3';
  setBackupDiffBinFilter: Dispatch<SetStateAction<'all' | '0' | '1' | '2' | '3'>>;
  backupSourceFileName: string | null;
  setBackupSourceFileName: Dispatch<SetStateAction<string | null>>;
  backupSourceHash: string | null;
  setBackupSourceHash: Dispatch<SetStateAction<string | null>>;
  backupWhitelistSkipped: number;
  setBackupWhitelistSkipped: Dispatch<SetStateAction<number>>;
  backupEmbeddedWhitelist: { key: string; addedAt: number }[] | null;
  setBackupEmbeddedWhitelist: Dispatch<SetStateAction<{ key: string; addedAt: number }[] | null>>;
  backupDiffCollapsed: Set<string>;
  setBackupDiffCollapsed: Dispatch<SetStateAction<Set<string>>>;
  backupShortcutsOpen: boolean;
  setBackupShortcutsOpen: Dispatch<SetStateAction<boolean>>;
  backupGroupBy: boolean;
  setBackupGroupBy: Dispatch<SetStateAction<boolean>>;
  backupCollapsedGroups: Set<string>;
  setBackupCollapsedGroups: Dispatch<SetStateAction<Set<string>>>;
  backupGroupSort: 'diff' | 'name' | 'risk';
  setBackupGroupSort: Dispatch<SetStateAction<'diff' | 'name' | 'risk'>>;
  backupGroupExportFmt: Record<string, 'json' | 'csv' | 'md'>;
  setBackupGroupExportFmt: Dispatch<SetStateAction<Record<string, 'json' | 'csv' | 'md'>>>;
  backupFocusIdx: number;
  // —— 派生/工具 ——
  backupPreviewSorted: BackupPreviewItem[];
  backupPreviewFiltered: BackupPreviewItem[];
  backupPreviewGroupOrderMap: Record<string, number>;
  computeItemRiskScore: (i: BackupPreviewItem) => { score: number; level: 'low' | 'mid' | 'high'; color: string; bg: string; icon: string; label: string; parts: string[] };
  riskScoreOf: (i: BackupPreviewItem) => number;
  diffCountOf: (i: BackupPreviewItem) => number;
  highlightMatch: (text: string, q: string) => ReactNode;
  // —— 应用/撤销 ——
  pendingBackupUndo: BackupApplySnapshot | null;
  backupUndoLeft: number;
  lastApplyAddedPresets: number | null;
  lastApplyAuditSummary: { applied: number; ok: number; dead: number } | null;
  pendingBackupUndoImpact: UndoImpactDetail | null;
  undoImpactDrillOpen: boolean;
  setUndoImpactDrillOpen: Dispatch<SetStateAction<boolean>>;
  backupUndoPreviewText: string | null;
  setBackupUndoPreviewText: Dispatch<SetStateAction<string | null>>;
  applyBackupUndo: () => void;
  backupConfirmOverlayOpen: boolean;
  setBackupConfirmOverlayOpen: Dispatch<SetStateAction<boolean>>;
  doApplyBackup: () => void;
  confirmApplyBackup: () => void;
  resetBackupView: () => void;
  cancelBackup: () => void;
  lastBackupAppliedDetail: BackupAppliedDetail | null;
  backupAppliedDetailOpen: boolean;
  setBackupAppliedDetailOpen: Dispatch<SetStateAction<boolean>>;
  backupDetailFilter: 'all' | 'added' | 'overwritten' | 'skipped';
  setBackupDetailFilter: Dispatch<SetStateAction<'all' | 'added' | 'overwritten' | 'skipped'>>;
  backupDetailFocusIdx: number;
  setBackupDetailFocusIdx: Dispatch<SetStateAction<number>>;
  // —— 风险白名单 ——
  riskWhitelistOpen: boolean;
  setRiskWhitelistOpen: Dispatch<SetStateAction<boolean>>;
  riskWhitelist: Map<string, number>;
  setRiskWhitelist: Dispatch<SetStateAction<Map<string, number>>>;
  whitelistSearch: string;
  setWhitelistSearch: Dispatch<SetStateAction<string>>;
  whitelistKindFilter: 'all' | 'preset' | 'snapshot';
  setWhitelistKindFilter: Dispatch<SetStateAction<'all' | 'preset' | 'snapshot'>>;
  whitelistSortBy: 'key' | 'addedAt' | 'expiring';
  setWhitelistSortBy: Dispatch<SetStateAction<'key' | 'addedAt' | 'expiring'>>;
  whitelistSel: Set<string>;
  setWhitelistSel: Dispatch<SetStateAction<Set<string>>>;
  whitelistAnchorIdx: MutableRefObject<number | null>;
  whitelistFocusIdx: number;
  removeRiskWhitelistItem: (key: string) => void;
  clearRiskWhitelist: () => void;
  exportRiskWhitelist: () => void;
  whitelistExportFormat: 'json' | 'csv';
  setWhitelistExportFormat: Dispatch<SetStateAction<'json' | 'csv'>>;
  whitelistFileRef: MutableRefObject<HTMLInputElement | null>;
  whitelistCsvFileRef: MutableRefObject<HTMLInputElement | null>;
  handleRiskWhitelistImport: (file: File) => Promise<void>;
  handleRiskWhitelistCsvImport: (file: File) => Promise<void>;
  whitelistImportMode: 'merge' | 'replace';
  setWhitelistImportMode: Dispatch<SetStateAction<'merge' | 'replace'>>;
  setRiskItemCtxMenu: Dispatch<SetStateAction<{ x: number; y: number; key: string } | null>>;
}

export function PresetManager(p: PresetManagerProps) {
  const {
    connId, kindProp, selSchema, selTable, opts, mappings, setSelSchema, setSelTable, setStep,
    snapDiffTs, setSnapDiffTs, copiedChipText, setCopiedChipText,
    presetListRefresh, setPresetListRefresh, setPresetSnapshotsRefresh, presetAuditFilter,
    setPresetAuditFilter, presetAuditResults, setPresetAuditResults, presetAuditCachedAt,
    setPresetAuditCachedAt, presetAuditBusy, setPresetAuditBusy, presetAuditCheckedRef,
    pendingPresetJumpConfirm, setPendingPresetJumpConfirm, reAuditOne, pendingTombstoneConfirm,
    setPendingTombstoneConfirm,
    presetManagerBusy, setPresetManagerBusy, presetManagerMsg, setPresetManagerMsg,
    presetManagerFileRef, setPresetManagerOpen, presetManagerOverwrite, setPresetManagerOverwrite,
    backupPending, setBackupPending, backupSelected, setBackupSelected, backupItemAudit,
    setBackupItemAudit, backupItemAuditRef, backupItemAuditBusy, setBackupItemAuditBusy,
    backupPreviewSearch, setBackupPreviewSearch, backupPreviewFilter, setBackupPreviewFilter,
    backupPreviewKindFilter, setBackupPreviewKindFilter, backupSortBy, setBackupSortBy,
    backupRiskLevelFilter, setBackupRiskLevelFilter, backupRiskThreshold, setBackupRiskThreshold,
    backupPreviewAllDiff, setBackupPreviewAllDiff, backupDiffBinFilter, setBackupDiffBinFilter,
    backupSourceFileName, setBackupSourceFileName, backupSourceHash, setBackupSourceHash,
    backupWhitelistSkipped, setBackupWhitelistSkipped, backupEmbeddedWhitelist,
    setBackupEmbeddedWhitelist, backupDiffCollapsed, setBackupDiffCollapsed, backupShortcutsOpen,
    setBackupShortcutsOpen, backupGroupBy, setBackupGroupBy, backupCollapsedGroups,
    setBackupCollapsedGroups, backupGroupSort, setBackupGroupSort, backupGroupExportFmt,
    setBackupGroupExportFmt, backupFocusIdx,
    backupPreviewSorted, backupPreviewFiltered, backupPreviewGroupOrderMap, computeItemRiskScore,
    riskScoreOf, diffCountOf, highlightMatch,
    pendingBackupUndo, backupUndoLeft, lastApplyAddedPresets, lastApplyAuditSummary,
    pendingBackupUndoImpact, undoImpactDrillOpen, setUndoImpactDrillOpen, backupUndoPreviewText,
    setBackupUndoPreviewText, applyBackupUndo, backupConfirmOverlayOpen, setBackupConfirmOverlayOpen,
    doApplyBackup, confirmApplyBackup, resetBackupView, cancelBackup, lastBackupAppliedDetail,
    backupAppliedDetailOpen, setBackupAppliedDetailOpen, backupDetailFilter, setBackupDetailFilter,
    backupDetailFocusIdx, setBackupDetailFocusIdx,
    riskWhitelistOpen, setRiskWhitelistOpen, riskWhitelist, setRiskWhitelist, whitelistSearch,
    setWhitelistSearch, whitelistKindFilter, setWhitelistKindFilter, whitelistSortBy,
    setWhitelistSortBy, whitelistSel, setWhitelistSel, whitelistAnchorIdx, whitelistFocusIdx,
    removeRiskWhitelistItem, clearRiskWhitelist, exportRiskWhitelist, whitelistExportFormat,
    setWhitelistExportFormat, whitelistFileRef, whitelistCsvFileRef, handleRiskWhitelistImport,
    handleRiskWhitelistCsvImport, whitelistImportMode, setWhitelistImportMode,
    setRiskItemCtxMenu,
  } = p;
  return (
    (() => {
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
                            downloadFile(name, mime, content);
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
                            for (const k of impact.presetsRemovedKeys) rows.push(`preset,removed,${csvEscape(k)},,`);
                            for (const x of impact.presetsRestoredKeys) rows.push(`preset,restored,${csvEscape(x.key)},${x.prevUpdatedAt},${x.nowUpdatedAt}`);
                            for (const k of impact.snapshotsRemovedKeys) rows.push(`snapshot,removed,${csvEscape(k)},,`);
                            for (const k of impact.snapshotsRestoredKeys) rows.push(`snapshot,restored,${csvEscape(k)},,`);
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
                    const stamp = backupFileStamp(ts);
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
                            const stamp = backupFileStamp(new Date());
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
                            const stamp = backupFileStamp(new Date());
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
                    downloadFile(name, mime, content);
                    setPresetManagerMsg({ kind: 'ok', text: `📊 已导出预检报告 ${name}（${rows.length} 项）` });
                  };
                  if (fmt === 'json') {
                    download(JSON.stringify({ summary, items: rows }, null, 2), 'application/json', `polydb-backup-report-${ts}.json`);
                    return;
                  }
                  if (fmt === 'csv') {
                    const head = ['key','kind','action','schema','table','selected','auditStatus','diffCount','riskScore'].join(',');
                    const body = rows.map((r) => [r.key,r.kind,r.action,r.schema,r.table,r.selected,r.auditStatus,r.diffCount,r.riskScore].map(csvEscape).join(',')).join('\n');
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
                      // 与 lib/importDisplay 的 formatTime 同一格式（原为本地重复实现）
                      const fmtExpAt = expAt ? formatTime(expAt) : '—';
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
                                          downloadFile(name, mime, content);
                                          setPresetManagerMsg({ kind: 'ok', text: `📥 已导出组 ${parts[0]}.${parts[1]} diff ${fmtLabel}（${diffItems.length} 项）` });
                                        };
                                        if (fmt === 'json') {
                                          const items = diffItems.map((x) => ({ key: x.key, schema: x.schema, table: x.table, diff: x.diff }));
                                          download(JSON.stringify({ exportedAt: now.toISOString(), connId, group: `${parts[0]}.${parts[1]}`, items }, null, 2), 'application/json', `polydb-group-${safe}-${ts}.json`);
                                          return;
                                        }
                                        if (fmt === 'csv') {
                                          const rows: string[] = ['key,schema,table,diffCount,scalarDiffs,removedTargets,addedTargets,removedCols,addedCols'];
                                          for (const x of diffItems) {
                                            const d = x.diff!;
                                            const bits: string[] = [];
                                            for (const s of d.scalarDiffs) bits.push(s);
                                            for (const t of d.removedTargets) bits.push(`-目标 ${t}`);
                                            for (const t of d.addedTargets) bits.push(`+目标 ${t}`);
                                            for (const [f, arr2] of Object.entries(d.removedCols)) for (const s of arr2) bits.push(`-${f} ${s}`);
                                            for (const [f, arr2] of Object.entries(d.addedCols)) for (const s of arr2) bits.push(`+${f} ${s}`);
                                            rows.push([x.key, x.schema, x.table, String(d.total), csvEscape(bits.join(' | '))].map((v) => csvEscape(v)).join(','));
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
                // 与 lib/importDisplay 的 formatTime 同一格式（原为本地重复实现 fmtDate）
                const fmtDate = formatTime;
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
    })()
  );
}
