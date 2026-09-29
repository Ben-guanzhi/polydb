// 备份预检列表的排序 / 分组 / 过滤纯变换（自 components/ImportModal.tsx 下沉）：
// backupPreviewSorted（M30.142 排序 + M30.146 D 风险排序）、
// backupPreviewGroupOrderMap（M30.152 D 分组顺序）、
// backupPreviewFiltered（M30.144 A/B/D 过滤链 + M30.158/161 风险过滤）。
// 全部为 (items, 状态) → 新数组的纯函数，不依赖 React。
import type { BackupPreviewItem, PresetAuditStatus } from './importPresets';
import { computeItemRiskScore } from './importRisk';

/** 风险评分所需上下文：当前连接 + 预设审计缓存。 */
export interface BackupPreviewRiskCtx {
  connId: string;
  audit: Record<string, PresetAuditStatus> | null;
}

/** overwrite preset 的字段级变更总数（scalar + 目标增删 + 列增删）。 */
export function diffCountOf(i: BackupPreviewItem): number {
  if (i.action !== 'overwrite' || !i.diff) return 0;
  const d = i.diff;
  return d.scalarDiffs.length + d.removedTargets.length + d.addedTargets.length
    + Object.keys(d.removedCols).length + Object.keys(d.addedCols).length;
}

/** M30.146 D 风险排序权重：dead 审计 > 跨连接 > 变更数×10 > overwrite。数值越大越靠前。 */
export function riskScoreOf(i: BackupPreviewItem, ctx: BackupPreviewRiskCtx): number {
  let s = 0;
  if (i.kind === 'preset' && ctx.audit && ctx.audit[i.key] === 'dead') s += 10000;
  const parts = i.key.split('::');
  const srcConn = parts[0];
  if (srcConn && srcConn !== ctx.connId) s += 5000;
  s += diffCountOf(i) * 10;
  if (i.action === 'overwrite') s += 5;
  return s;
}

/**
 * M30.142 备份预检排序：risk=风险降序 / diff=变更数降序 / schema（其余值）=表名字典序。
 * stable tiebreaker：overwrite 前置，再按 schema.table 字典序。返回新数组不原地改。
 */
export function backupPreviewSorted(
  items: BackupPreviewItem[],
  sortBy: 'diff' | 'schema' | 'risk',
  ctx: BackupPreviewRiskCtx,
): BackupPreviewItem[] {
  return [...items].sort((a, b) => {
    if (sortBy === 'risk') {
      const ra = riskScoreOf(a, ctx);
      const rb = riskScoreOf(b, ctx);
      if (rb !== ra) return rb - ra;
      return `${a.schema}.${a.table}`.localeCompare(`${b.schema}.${b.table}`);
    }
    if (sortBy === 'diff') {
      const da = diffCountOf(a);
      const db = diffCountOf(b);
      if (db !== da) return db - da;
      // 稳定 tiebreaker：overwrite 前置，再按 schema.table 字典序
      if ((a.action === 'overwrite') !== (b.action === 'overwrite')) return a.action === 'overwrite' ? -1 : 1;
      return `${a.schema}.${a.table}`.localeCompare(`${b.schema}.${b.table}`);
    }
    return `${a.schema}.${a.table}`.localeCompare(`${b.schema}.${b.table}`);
  });
}

/**
 * M30.152 D：按表分组模式下，把可见项按「所在分组顺序（复用 M30.151 组排序）→ 组内变更数降序」重排。
 * 返回 groupKey（`schema::table`）→ 顺序序号；enabled=false（未开分组或无备份）返回空表。
 */
export function backupPreviewGroupOrderMap(
  items: BackupPreviewItem[],
  opts: { enabled: boolean; groupSort: 'diff' | 'name' | 'risk' } & BackupPreviewRiskCtx,
): Record<string, number> {
  if (!opts.enabled) return {};
  const groups = new Set<string>();
  for (const i of items) groups.add(`${i.schema}::${i.table}`);
  const arr = Array.from(groups);
  const diffOf = (gk: string) => {
    let s = 0;
    for (const i of items) {
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
    for (const i of items) {
      if (`${i.schema}::${i.table}` !== gk) continue;
      s += computeItemRiskScore(i, opts.connId, opts.audit).score; n += 1;
    }
    return n === 0 ? 0 : s / n;
  };
  arr.sort((a, b) => {
    if (opts.groupSort === 'diff') {
      const da = diffOf(a);
      const db = diffOf(b);
      if (db !== da) return db - da;
      return a.localeCompare(b);
    }
    if (opts.groupSort === 'risk') {
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
}

/** backupPreviewFiltered 的过滤状态（原组件 backupPreview* 一族 state 的快照）。 */
export interface BackupPreviewFilterState {
  /** 搜索关键字（对 schema/table 大小写不敏感子串匹配） */
  search: string;
  /** M30.140 动作过滤：'all' | 'overwrite' | 'add' */
  actionFilter: 'all' | 'overwrite' | 'add';
  /** 类型过滤：'all' | 'preset' | 'snapshot' */
  kindFilter: 'all' | 'preset' | 'snapshot';
  /** M30.154 B 环形图分档点击筛选：'all' | '0' | '1' | '2' | '3' */
  diffBinFilter: 'all' | '0' | '1' | '2' | '3';
  /** M30.148 D 按表分组模式 */
  groupBy: boolean;
  /** M30.163 B 已折叠分组（key = `schema::table`） */
  collapsedGroups: Set<string>;
  /** M30.158 A 风险级别过滤 */
  riskLevelFilter: 'all' | 'low' | 'mid' | 'high';
  /** M30.161 C 风险阈值滑块（0 = 关闭） */
  riskThreshold: number;
}

/**
 * M30.144 A/B/D 键盘导航用：把 sorted 再走一遍与 UI 相同的 filter 链，产出用户可见列表。
 * 分组模式下再按 backupPreviewGroupOrderMap 的组序重排（组内变更数降序 → key 字典序）。
 */
export function backupPreviewFiltered(
  sorted: BackupPreviewItem[],
  f: BackupPreviewFilterState,
  ctx: BackupPreviewRiskCtx,
  groupOrderMap: Record<string, number>,
): BackupPreviewItem[] {
  const q = f.search.trim().toLowerCase();
  // M30.154 B 环形图分档点击筛选：非 'all' 时只保留 overwrite preset 且落在对应分档的项
  const binMatch = (i: BackupPreviewItem): boolean => {
    if (f.diffBinFilter === 'all') return true;
    if (i.action !== 'overwrite' || i.kind !== 'preset' || !i.diff) return false;
    const dc = diffCountOf(i);
    const bin = dc === 0 ? '0' : dc <= 3 ? '1' : dc <= 10 ? '2' : '3';
    return bin === f.diffBinFilter;
  };
  const visible = sorted.filter((i) => {
    if (f.actionFilter !== 'all' && i.action !== f.actionFilter) return false;
    if (f.kindFilter !== 'all' && i.kind !== f.kindFilter) return false;
    if (!binMatch(i)) return false;
    if (q && !i.schema.toLowerCase().includes(q) && !i.table.toLowerCase().includes(q)) return false;
    if (f.groupBy && f.collapsedGroups.has(`${i.schema}::${i.table}`)) return false;
    // M30.158 A 风险级别过滤：'all' 全部通过；否则只保留匹配 level 的项
    if (f.riskLevelFilter !== 'all' && computeItemRiskScore(i, ctx.connId, ctx.audit).level !== f.riskLevelFilter) return false;
    // M30.161 C 风险阈值滑块：只保留 score >= threshold
    if (f.riskThreshold > 0 && computeItemRiskScore(i, ctx.connId, ctx.audit).score < f.riskThreshold) return false;
    return true;
  });
  if (!f.groupBy) return visible;
  return visible.slice().sort((a, b) => {
    const ga = groupOrderMap[`${a.schema}::${a.table}`] ?? Number.MAX_SAFE_INTEGER;
    const gb = groupOrderMap[`${b.schema}::${b.table}`] ?? Number.MAX_SAFE_INTEGER;
    if (ga !== gb) return ga - gb;
    const da = diffCountOf(a);
    const db = diffCountOf(b);
    if (db !== da) return db - da;
    return a.key.localeCompare(b.key);
  });
}
