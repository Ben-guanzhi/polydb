import type { ColumnInfo, DatabaseKind } from '../api';
import type { ColumnTransform, ColumnValidation, ImportMode, Mapping, NullPolicy } from './importData';

export interface ImportPreset {
  key: string;
  schema: string;
  table: string;
  kind: DatabaseKind | null;
  mode: ImportMode;
  mappings: Mapping[];
  transforms: Record<number, ColumnTransform>;
  transformParams: Record<number, Record<string, string>>;
  filterColumn: number | null;
  filterOp: 'eq' | 'neq' | 'contains' | 'regex';
  filterValue: string;
  validations: Record<number, ColumnValidation>;
  strictValidation: boolean;
  emptyAsNull: boolean;
  batchSize: number;
  skipFailed: boolean;
  nullPolicies: Record<number, NullPolicy>;
  createdAt: number;
  updatedAt: number;
}

export interface PresetOptions {
  emptyAsNull: boolean;
  batchSize: number;
  skipFailed: boolean;
  filterColumn: number | null;
  filterOp: 'eq' | 'neq' | 'contains' | 'regex';
  filterValue: string;
  validations: Record<number, ColumnValidation>;
  strictValidation: boolean;
  nullPolicies: Record<number, NullPolicy>;
}

const KEY = 'polydb.importPresets.v1';
const SNAPSHOT_KEY = 'polydb.presetSnapshots.v1';
const MAX_SNAPSHOTS = 5;
// M30.115 快照 30 天自动过期（与 MAX_SNAPSHOTS=5 硬上限正交）
const SNAPSHOT_TTL_MS = 30 * 24 * 60 * 60 * 1000;

export interface PresetBackup {
  version: 1;
  exportedAt: number;
  presets: ImportPreset[];
  snapshots: Record<string, ImportPreset[]>;
  // M30.157 D 可选白名单段：仅在新导出时写入；旧备份不含此字段（读取时视为空数组）
  riskWhitelist?: { v: 2; items: { key: string; addedAt: number }[] };
}

function loadAll(): Record<string, ImportPreset> {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as Record<string, ImportPreset>;
    if (!parsed || typeof parsed !== 'object') return {};
    // 兼容旧数据：nullPolicies 缺省视为空对象
    for (const k of Object.keys(parsed)) {
      if (!parsed[k].nullPolicies) parsed[k].nullPolicies = {};
      if (!parsed[k].transformParams) parsed[k].transformParams = {};
    }
    return parsed;
  } catch {
    return {};
  }
}

function saveAll(map: Record<string, ImportPreset>): void {
  try { localStorage.setItem(KEY, JSON.stringify(map)); } catch { /* quota */ }
}

export function presetKey(connId: string, schema: string, table: string): string {
  return `${connId}::${schema}::${table}`;
}

export function listPresets(connId: string): ImportPreset[] {
  const all = loadAll();
  return Object.values(all).filter((p) => p.key.startsWith(`${connId}::`));
}

export function getPreset(connId: string, schema: string, table: string): ImportPreset | null {
  return loadAll()[presetKey(connId, schema, table)] ?? null;
}

function snapshotKey(): string {
  return SNAPSHOT_KEY;
}

function loadSnapshots(): Record<string, ImportPreset[]> {
  try {
    const raw = localStorage.getItem(snapshotKey());
    if (!raw) return {};
    const parsed = JSON.parse(raw) as Record<string, ImportPreset[]>;
    if (!parsed || typeof parsed !== 'object') return {};
    return parsed;
  } catch {
    return {};
  }
}

function saveSnapshots(map: Record<string, ImportPreset[]>): void {
  try { localStorage.setItem(snapshotKey(), JSON.stringify(map)); } catch { /* quota */ }
}

function presetEquals(a: ImportPreset, b: ImportPreset): boolean {
  return a.mode === b.mode
    && JSON.stringify(a.mappings) === JSON.stringify(b.mappings)
    && JSON.stringify(a.transforms) === JSON.stringify(b.transforms)
    && JSON.stringify(a.transformParams ?? {}) === JSON.stringify(b.transformParams ?? {})
    && a.filterColumn === b.filterColumn
    && a.filterOp === b.filterOp
    && a.filterValue === b.filterValue
    && JSON.stringify(a.validations ?? {}) === JSON.stringify(b.validations ?? {})
    && a.strictValidation === b.strictValidation
    && a.emptyAsNull === b.emptyAsNull
    && a.batchSize === b.batchSize
    && a.skipFailed === b.skipFailed
    && JSON.stringify(a.nullPolicies ?? {}) === JSON.stringify(b.nullPolicies ?? {});
}

export function savePreset(preset: ImportPreset): void {
  const all = loadAll();
  const prev = all[preset.key];
  const next = { ...preset, updatedAt: Date.now() };
  if (prev && presetEquals(prev, preset)) {
    all[preset.key] = next;
    saveAll(all);
    return;
  }
  if (prev) {
    const snaps = loadSnapshots();
    const list = snaps[preset.key] ?? [];
    const trimmed = [...list.filter((s) => !presetEquals(s, prev)), prev].slice(0, MAX_SNAPSHOTS);
    snaps[preset.key] = trimmed;
    saveSnapshots(snaps);
  }
  all[preset.key] = next;
  saveAll(all);
}

export function listSnapshots(key: string): ImportPreset[] {
  const snaps = loadSnapshots();
  const now = Date.now();
  const cutoff = now - SNAPSHOT_TTL_MS;
  return (snaps[key] ?? [])
    .filter((s) => s.updatedAt >= cutoff)
    .slice()
    .sort((a, b) => b.updatedAt - a.updatedAt);
}

// M30.115 快照年龄（毫秒），供 UI 显示相对时间
export function snapshotAgeMs(snap: ImportPreset): number {
  return Date.now() - snap.updatedAt;
}

export function snapshotExpired(snap: ImportPreset): boolean {
  return snap.updatedAt < Date.now() - SNAPSHOT_TTL_MS;
}

export function restoreSnapshot(key: string, idx: number): void {
  const snaps = loadSnapshots();
  // 兼容旧调用：按 listSnapshots 的过滤+排序结果定位
  const now = Date.now();
  const cutoff = now - SNAPSHOT_TTL_MS;
  const filtered = (snaps[key] ?? [])
    .filter((s) => s.updatedAt >= cutoff)
    .sort((a, b) => b.updatedAt - a.updatedAt);
  const target = filtered[idx];
  if (!target) return;
  const all = loadAll();
  const cur = all[key];
  // 过滤移除目标（按引用；filter+sort 不拷贝对象），然后前插当前 preset（若不重复）
  const removed = (snaps[key] ?? []).filter((s) => s !== target);
  if (cur && !removed.some((s) => presetEquals(s, cur))) {
    snaps[key] = [cur, ...removed].slice(0, MAX_SNAPSHOTS);
  } else {
    snaps[key] = removed;
  }
  saveSnapshots(snaps);
  all[key] = { ...target, updatedAt: Date.now() };
  saveAll(all);
}

export function deletePreset(connId: string, schema: string, table: string): void {
  const key = presetKey(connId, schema, table);
  const all = loadAll();
  delete all[key];
  saveAll(all);
  const snaps = loadSnapshots();
  if (snaps[key]) {
    delete snaps[key];
    saveSnapshots(snaps);
  }
}

export function restoreDeletedPreset(preset: ImportPreset, snapshots: ImportPreset[]): void {
  const all = loadAll();
  all[preset.key] = preset;
  saveAll(all);
  if (snapshots.length > 0) {
    const snaps = loadSnapshots();
    const existing = snaps[preset.key] ?? [];
    snaps[preset.key] = [...existing, ...snapshots].slice(0, MAX_SNAPSHOTS);
    saveSnapshots(snaps);
  }
}

// M30.114 删除预设跨会话恢复：把"删除前"的 preset+snapshots 存到 tombstone 桶，
// 用户在预设管理面板可一键恢复（比 transient 30s bar 覆盖面更广）
const TOMBSTONE_KEY = 'polydb.deletedPresets.v1';
const MAX_TOMBSTONES = 20;

export interface DeletedPresetEntry {
  deletedAt: number;
  preset: ImportPreset;
  snapshots: ImportPreset[];
}

function loadTombstones(): DeletedPresetEntry[] {
  try {
    const raw = localStorage.getItem(TOMBSTONE_KEY);
    if (!raw) return [];
    const p = JSON.parse(raw);
    if (!Array.isArray(p)) return [];
    return p.filter(
      (x) => x && typeof x === 'object' && typeof (x as DeletedPresetEntry).deletedAt === 'number'
        && (x as DeletedPresetEntry).preset && typeof ((x as DeletedPresetEntry).preset as unknown as Record<string, unknown>).key === 'string'
        && Array.isArray((x as DeletedPresetEntry).snapshots),
    ) as DeletedPresetEntry[];
  } catch {
    return [];
  }
}

function saveTombstones(list: DeletedPresetEntry[]): void {
  try { localStorage.setItem(TOMBSTONE_KEY, JSON.stringify(list)); } catch { /* quota */ }
}

export function addDeletedPresetTombstone(preset: ImportPreset, snapshots: ImportPreset[]): void {
  const key = preset.key;
  const list = loadTombstones().filter((e) => e.preset.key !== key);
  const entry: DeletedPresetEntry = { deletedAt: Date.now(), preset, snapshots };
  saveTombstones([entry, ...list].slice(0, MAX_TOMBSTONES));
}

export function listDeletedPresets(): DeletedPresetEntry[] {
  return loadTombstones().slice().sort((a, b) => b.deletedAt - a.deletedAt);
}

export function restoreDeletedPresetTombstone(index: number): boolean {
  const list = loadTombstones();
  const entry = list[index];
  if (!entry) return false;
  restoreDeletedPreset(entry.preset, entry.snapshots);
  saveTombstones(list.filter((_, i) => i !== index));
  return true;
}

export function purgeDeletedPresetTombstone(index: number): void {
  const list = loadTombstones();
  saveTombstones(list.filter((_, i) => i !== index));
}

export function purgeAllDeletedPresets(): void {
  saveTombstones([]);
}

// M30.113 全量备份：把所有预设 + 快照打包成 PresetBackup 结构
export function exportPresetsBackup(): PresetBackup {
  const all = loadAll();
  const snaps = loadSnapshots();
  const presets: ImportPreset[] = Object.values(all);
  const snapshots: Record<string, ImportPreset[]> = {};
  for (const k of Object.keys(snaps)) {
    if (snaps[k] && snaps[k].length > 0) snapshots[k] = snaps[k];
  }
  return {
    version: 1,
    exportedAt: Date.now(),
    presets,
    snapshots,
  };
}

export interface BackupApplyStats {
  presetsAdded: number;
  presetsOverwritten: number;
  snapshotsAdded: number;
  snapshotsOverwritten: number;
  errors: string[];
}

export interface BackupPreviewItem {
  key: string;
  kind: 'preset' | 'snapshot';
  action: 'add' | 'overwrite';
  schema: string;
  table: string;
  snapshotCount: number;
  // M30.120 字段级 diff（仅 action='overwrite' 且 kind='preset' 时非空）
  diff?: PresetDiff;
}

// M30.120 overwrite 字段级差异
export interface PresetDiff {
  total: number;
  aligned: boolean;
  scalarDiffs: string[];
  removedTargets: string[];
  addedTargets: string[];
  removedCols: Record<string, string[]>; // 字段名 -> 备份有当前无的 key 列表
  addedCols: Record<string, string[]>;   // 字段名 -> 当前有备份无的 key 列表
}

function diffRecord<T>(
  from: Record<string, T>,
  to: Record<string, T>,
  fmt: (v: T) => string,
): { removed: string[]; added: string[] } {
  const fromMap = new Map(Object.entries(from));
  const toMap = new Map(Object.entries(to));
  const removed: string[] = [];
  const added: string[] = [];
  for (const [k, v] of fromMap) {
    const nv = toMap.get(k);
    if (nv === undefined) removed.push(`−${k} ${fmt(v)}`);
    else if (JSON.stringify(v) !== JSON.stringify(nv)) added.push(`${k} ${fmt(v)}→${fmt(nv)}`);
  }
  for (const [k, v] of toMap) {
    if (!fromMap.has(k)) added.push(`+${k} ${fmt(v)}`);
  }
  return { removed, added };
}

export function diffPreset(current: ImportPreset, backup: ImportPreset): PresetDiff {
  const scalarDiffs: string[] = [];
  if (current.mode !== backup.mode) scalarDiffs.push(`模式 ${current.mode}→${backup.mode}`);
  if (current.batchSize !== backup.batchSize) scalarDiffs.push(`批量 ${current.batchSize}→${backup.batchSize}`);
  if (current.emptyAsNull !== backup.emptyAsNull) scalarDiffs.push(`空串NULL ${current.emptyAsNull ? '是' : '否'}→${backup.emptyAsNull ? '是' : '否'}`);
  if (current.skipFailed !== backup.skipFailed) scalarDiffs.push(`失败跳过 ${current.skipFailed ? '是' : '否'}→${backup.skipFailed ? '是' : '否'}`);
  if ((current.strictValidation ?? true) !== (backup.strictValidation ?? true)) {
    scalarDiffs.push(`严格校验 ${current.strictValidation ?? true ? '是' : '否'}→${backup.strictValidation ?? true ? '是' : '否'}`);
  }
  if (current.filterOp !== backup.filterOp) scalarDiffs.push(`过滤操作 ${current.filterOp}→${backup.filterOp}`);

  const cTargets = new Set(current.mappings.filter((m) => m.targetColumn).map((m) => m.targetColumn!));
  const bTargets = new Set(backup.mappings.filter((m) => m.targetColumn).map((m) => m.targetColumn!));
  const removedTargets = Array.from(cTargets).filter((c) => !bTargets.has(c));
  const addedTargets = Array.from(bTargets).filter((c) => !cTargets.has(c));

  const tf = diffRecord(current.transforms, backup.transforms, (v) => String(v));
  const tp = diffRecord(current.transformParams ?? {}, backup.transformParams ?? {}, (_v) => 'params');
  const np = diffRecord(current.nullPolicies ?? {}, backup.nullPolicies ?? {}, (v) => String(v));
  const va = diffRecord(current.validations ?? {}, backup.validations ?? {}, (_v) => '规则');

  const removedCols: Record<string, string[]> = {};
  const addedCols: Record<string, string[]> = {};
  if (tf.removed.length) removedCols.transforms = tf.removed;
  if (tf.added.length) addedCols.transforms = tf.added;
  if (tp.removed.length) removedCols.transformParams = tp.removed;
  if (tp.added.length) addedCols.transformParams = tp.added;
  if (np.removed.length) removedCols.nullPolicies = np.removed;
  if (np.added.length) addedCols.nullPolicies = np.added;
  if (va.removed.length) removedCols.validations = va.removed;
  if (va.added.length) addedCols.validations = va.added;

  const rcCount = Object.values(removedCols).reduce((s, a) => s + a.length, 0);
  const acCount = Object.values(addedCols).reduce((s, a) => s + a.length, 0);
  const total = scalarDiffs.length + removedTargets.length + addedTargets.length + rcCount + acCount;
  return {
    total,
    aligned: total === 0,
    scalarDiffs,
    removedTargets,
    addedTargets,
    removedCols,
    addedCols,
  };
}

// M30.118 备份预检：只读比较备份与当前 localStorage 状态，不写入
export function previewBackup(backup: PresetBackup): BackupPreviewItem[] {
  const items: BackupPreviewItem[] = [];
  if (!backup || typeof backup !== 'object') return items;
  const all = loadAll();
  const presets = Array.isArray(backup.presets) ? backup.presets : [];
  for (const p of presets) {
    if (!p || typeof p !== 'object' || typeof (p as ImportPreset).key !== 'string') continue;
    const preset = p as ImportPreset;
    const cur = all[preset.key];
    items.push({
      key: preset.key,
      kind: 'preset',
      action: cur ? 'overwrite' : 'add',
      schema: preset.schema,
      table: preset.table,
      snapshotCount: 0,
      diff: cur ? diffPreset(cur, preset) : undefined,
    });
  }
  const snapMap = backup.snapshots ?? {};
  const curSnaps = loadSnapshots();
  for (const k of Object.keys(snapMap)) {
    const list = snapMap[k] ?? [];
    if (!Array.isArray(list) || list.length === 0) continue;
    const parts = k.split('::');
    const schema = parts.length >= 2 ? parts[1] ?? '' : '';
    const table = parts.length >= 3 ? parts[2] ?? '' : '';
    items.push({
      key: k,
      kind: 'snapshot',
      action: curSnaps[k] && curSnaps[k].length > 0 ? 'overwrite' : 'add',
      schema,
      table,
      snapshotCount: list.length,
    });
  }
  return items;
}

export function applyPresetsBackup(backup: PresetBackup, opts?: {
  overwrite?: boolean;
  selectedPresets?: Set<string>;
  selectedSnapshots?: Set<string>;
}): BackupApplyStats {
  const overwrite = opts?.overwrite ?? true;
  const stats: BackupApplyStats = {
    presetsAdded: 0, presetsOverwritten: 0,
    snapshotsAdded: 0, snapshotsOverwritten: 0,
    errors: [],
  };
  if (!backup || typeof backup !== 'object' || !Array.isArray(backup.presets)) {
    stats.errors.push('备份文件结构无效：缺少 presets 数组');
    return stats;
  }
  const selP = opts?.selectedPresets;
  const selS = opts?.selectedSnapshots;
  const all = loadAll();
  const snaps = loadSnapshots();
  for (const p of backup.presets) {
    if (!p || typeof p !== 'object' || typeof (p as ImportPreset).key !== 'string') continue;
    if (selP && !selP.has(p.key)) continue;
    if (all[p.key]) {
      if (!overwrite) continue;
      stats.presetsOverwritten++;
    } else {
      stats.presetsAdded++;
    }
    all[p.key] = p;
  }
  saveAll(all);
  const snapMap = backup.snapshots ?? {};
  for (const k of Object.keys(snapMap)) {
    if (selS && !selS.has(k)) continue;
    const list = snapMap[k] ?? [];
    if (!Array.isArray(list) || list.length === 0) continue;
    const cur = snaps[k];
    if (cur && cur.length > 0) stats.snapshotsOverwritten++;
    else stats.snapshotsAdded++;
    snaps[k] = [...list].slice(0, MAX_SNAPSHOTS);
  }
  saveSnapshots(snaps);
  return stats;
}

// M30.143 备份应用 5s 内可撤销：快照整个 presets map + snapshots map，undo 时精确回滚
export interface BackupApplySnapshot {
  appliedAt: number;
  prevPresets: Record<string, ImportPreset>;
  prevSnapshots: Record<string, ImportPreset[]>;
}

export function undoApplyPresetsBackup(snapshot: BackupApplySnapshot): { ok: boolean; error?: string } {
  try {
    saveAll(snapshot.prevPresets);
    saveSnapshots(snapshot.prevSnapshots);
    return { ok: true };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

// M30.143 apply 前先取快照：调用方在 applyPresetsBackup 之前调用，用返回的 BackupApplySnapshot 支持 5s undo
export function snapshotPresetsState(): BackupApplySnapshot {
  return {
    appliedAt: Date.now(),
    prevPresets: loadAll(),
    prevSnapshots: loadSnapshots(),
  };
}

// M30.145 A undo 影响预览：apply 完成后与 snapshot 前态对比，算出撤销会「移除」的项数
export interface UndoImpact {
  presetsRemoved: number;   // 撤销后会消失的预设（apply 新增的）
  presetsRestored: number;  // 撤销后会恢复旧版本的预设（apply 覆盖的）
  snapshotsRemoved: number;
  snapshotsRestored: number;
}
export interface UndoImpactDetail {
  impact: UndoImpact;
  // 撤销会移除的预设 key 列表（apply 新增的）
  presetsRemovedKeys: string[];
  // 撤销会恢复旧版本的预设 key 列表（apply 覆盖的）+ 前/后 updatedAt 供展示
  presetsRestoredKeys: { key: string; prevUpdatedAt: number; nowUpdatedAt: number }[];
  snapshotsRemovedKeys: string[];
  snapshotsRestoredKeys: string[];
}
export function diffUndoImpact(snapshot: BackupApplySnapshot): UndoImpactDetail {
  const nowP = loadAll();
  const nowS = loadSnapshots();
  const prevP = snapshot.prevPresets;
  const prevS = snapshot.prevSnapshots;
  let pRemoved = 0, pRestored = 0;
  const presetsRemovedKeys: string[] = [];
  const presetsRestoredKeys: UndoImpactDetail['presetsRestoredKeys'] = [];
  const allKeys = new Set<string>([...Object.keys(nowP), ...Object.keys(prevP)]);
  for (const k of allKeys) {
    const inPrev = k in prevP;
    const inNow = k in nowP;
    if (inNow && !inPrev) { pRemoved++; presetsRemovedKeys.push(k); }
    else if (inPrev && inNow) {
      pRestored++;
      presetsRestoredKeys.push({
        key: k,
        prevUpdatedAt: prevP[k]?.updatedAt ?? 0,
        nowUpdatedAt: nowP[k]?.updatedAt ?? 0,
      });
    }
  }
  let sRemoved = 0, sRestored = 0;
  const snapshotsRemovedKeys: string[] = [];
  const snapshotsRestoredKeys: string[] = [];
  const allSk = new Set<string>([...Object.keys(nowS), ...Object.keys(prevS)]);
  for (const k of allSk) {
    const inPrev = k in prevS;
    const inNow = k in nowS;
    if (inNow && !inPrev) { sRemoved++; snapshotsRemovedKeys.push(k); }
    else if (inPrev && inNow) { sRestored++; snapshotsRestoredKeys.push(k); }
  }
  return {
    impact: {
      presetsRemoved: pRemoved,
      presetsRestored: pRestored,
      snapshotsRemoved: sRemoved,
      snapshotsRestored: sRestored,
    },
    presetsRemovedKeys,
    presetsRestoredKeys,
    snapshotsRemovedKeys,
    snapshotsRestoredKeys,
  };
}

// M30.119 审计结果 24h localStorage 缓存
const AUDIT_KEY = 'polydb.presetAudit.v1';
const AUDIT_TTL_MS = 24 * 60 * 60 * 1000;

export type PresetAuditStatus = 'ok' | 'warn' | 'dead';

export interface PresetAuditEntry {
  status: PresetAuditStatus;
  reason: string;
  auditedAt: number;
}

export interface PresetAuditCache {
  auditedAt: number;
  results: Record<string, PresetAuditEntry>;
}

export function auditExpired(cache: PresetAuditCache | null): boolean {
  if (!cache) return true;
  return Date.now() - cache.auditedAt > AUDIT_TTL_MS;
}

export function loadAuditCache(connId: string): PresetAuditCache | null {
  try {
    const raw = localStorage.getItem(AUDIT_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Record<string, PresetAuditCache>;
    const c = parsed?.[connId];
    if (!c || typeof c !== 'object' || typeof c.auditedAt !== 'number' || !c.results) return null;
    if (auditExpired(c)) return null;
    return c;
  } catch {
    return null;
  }
}

export function saveAuditCache(connId: string, results: Record<string, { status: PresetAuditStatus; reason: string }>): void {
  const now = Date.now();
  const entry: PresetAuditCache = {
    auditedAt: now,
    results: Object.fromEntries(
      Object.entries(results).map(([k, v]) => [k, { ...v, auditedAt: now }]),
    ),
  };
  try {
    const raw = localStorage.getItem(AUDIT_KEY);
    const parsed: Record<string, PresetAuditCache> = raw ? JSON.parse(raw) : {};
    parsed[connId] = entry;
    localStorage.setItem(AUDIT_KEY, JSON.stringify(parsed));
  } catch { /* quota */ }
}

// M30.129 单预设重审：只更新目标 entry 的 status/reason/auditedAt，保留其他 entry 的 auditedAt 与顶层 cache.auditedAt
export function updateAuditCacheEntry(connId: string, key: string, v: { status: PresetAuditStatus; reason: string }): void {
  const now = Date.now();
  try {
    const raw = localStorage.getItem(AUDIT_KEY);
    const parsed: Record<string, PresetAuditCache> = raw ? JSON.parse(raw) : {};
    const existing = parsed[connId];
    if (existing) {
      existing.results[key] = { ...v, auditedAt: now };
    } else {
      parsed[connId] = { auditedAt: now, results: { [key]: { ...v, auditedAt: now } } };
    }
    localStorage.setItem(AUDIT_KEY, JSON.stringify(parsed));
  } catch { /* quota */ }
}

export function deleteAuditCacheEntry(connId: string, key: string): void {
  try {
    const raw = localStorage.getItem(AUDIT_KEY);
    if (!raw) return;
    const parsed = JSON.parse(raw) as Record<string, PresetAuditCache>;
    const c = parsed[connId];
    if (!c) return;
    delete c.results[key];
    localStorage.setItem(AUDIT_KEY, JSON.stringify(parsed));
  } catch { /* ignore */ }
}

export function clearAuditCache(connId: string): void {
  try {
    const raw = localStorage.getItem(AUDIT_KEY);
    if (!raw) return;
    const parsed = JSON.parse(raw) as Record<string, PresetAuditCache>;
    if (!parsed[connId]) return;
    delete parsed[connId];
    localStorage.setItem(AUDIT_KEY, JSON.stringify(parsed));
  } catch { /* ignore */ }
}

export function buildPreset(
  connId: string,
  schema: string,
  table: string,
  kind: DatabaseKind | null,
  mode: ImportMode,
  mappings: Mapping[],
  transforms: Record<number, ColumnTransform>,
  opts: PresetOptions,
  transformParams: Record<number, Record<string, string>> = {},
): ImportPreset {
  const now = Date.now();
  return {
    key: presetKey(connId, schema, table),
    schema, table, kind, mode, mappings, transforms, transformParams,
    filterColumn: opts.filterColumn,
    filterOp: opts.filterOp,
    filterValue: opts.filterValue,
    validations: { ...opts.validations },
    strictValidation: opts.strictValidation,
    emptyAsNull: opts.emptyAsNull,
    batchSize: opts.batchSize,
    skipFailed: opts.skipFailed,
    nullPolicies: { ...opts.nullPolicies },
    createdAt: now,
    updatedAt: now,
  };
}

export interface PresetAppliedResult {
  mode: ImportMode;
  mappings: Mapping[];
  transforms: Record<number, ColumnTransform>;
  transformParams: Record<number, Record<string, string>>;
  opts: PresetOptions;
  matchedCols: number;
  totalCols: number;
  /** CSV 列对应的目标列已不再存在于目标表 */
  removedTargets: { csvIndex: number; csvName: string; targetColumn: string }[];
  /** 目标表新增了列（预设里没见过） */
  addedTargets: string[];
  /** 目标列名保留但 dtype 变化 */
  changedTargets: { targetColumn: string; presetDtype: string; currentDtype: string }[];
}

export function applyPreset(preset: ImportPreset, cols: ColumnInfo[]): PresetAppliedResult {
  const colMap = new Map(cols.map((c) => [c.name, c]));
  const targetSet = new Set(colMap.keys());
  const mappings: Mapping[] = preset.mappings.map((m) => {
    const removed = m.targetColumn && !targetSet.has(m.targetColumn);
    return {
      ...m,
      targetColumn: m.targetColumn && targetSet.has(m.targetColumn) ? m.targetColumn : null,
    };
  });
  const removedTargets = preset.mappings
    .filter((m) => m.targetColumn && !targetSet.has(m.targetColumn))
    .map((m) => ({ csvIndex: m.csvIndex, csvName: m.csvName, targetColumn: m.targetColumn! }));
  const presetTargetSet = new Set(
    preset.mappings.filter((m) => m.targetColumn).map((m) => m.targetColumn!),
  );
  const addedTargets = cols
    .map((c) => c.name)
    .filter((n) => !presetTargetSet.has(n));
  return {
    mode: preset.mode,
    mappings,
    transforms: { ...preset.transforms },
    transformParams: { ...preset.transformParams },
    opts: {
      emptyAsNull: preset.emptyAsNull,
      batchSize: preset.batchSize,
      skipFailed: preset.skipFailed,
      filterColumn: preset.filterColumn,
      filterOp: preset.filterOp,
      filterValue: preset.filterValue,
      validations: { ...(preset.validations ?? {}) },
      strictValidation: preset.strictValidation ?? true,
      nullPolicies: { ...(preset.nullPolicies ?? {}) },
    },
    matchedCols: mappings.filter((m) => m.targetColumn != null).length,
    totalCols: mappings.length,
    removedTargets,
    addedTargets,
    changedTargets: [],
  };
}
