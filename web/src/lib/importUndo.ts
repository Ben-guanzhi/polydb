// 单项撤销（diff 面板 ↩ 按钮）的纯变换（自 components/ImportModal.tsx 下沉）：
// 把 diff 面板中某个字段从 before（当前 opts 值）恢复到 after（目标 snapshot 值）。
// 支持：mode / strictValidation / emptyAsNull / batchSize / transforms / nullPolicies / validations / 行过滤。
// 不支持（返回 null 由调用方提示改用全撤销）：validation 的"非空"目标（正则/enum 因显示截断不可逆）。
// 状态副作用（setOpts / pushFix / publishEditorStatus）留在组件层。
import { DIFF_EMPTY_MARKS } from './importDiff';
import { hasAnyValidation, type ColumnValidation, type ImportOptions, type Mapping } from './importData';

/** diff 面板单项条目（field 形如「转换 csv_col#3」/「模式」/「批大小」…）。 */
export interface DiffPanelItem {
  field: string;
  before: string;
  after: string;
}

/** diff 展示层的"空"标记：空串或 ∅/空/null（默认） 占位符。 */
export function isDisplayEmptyStr(s: string): boolean {
  return s === '' || DIFF_EMPTY_MARKS.has(s);
}

/**
 * 从 diff 字段子标签解析目标列下标：优先「xxx#N」的 N，否则按「csv名 → 目标列」
 * 的 csv 名在 mappings 中查序号；解析失败返回 null。
 */
export function extractColIdx(subLabel: string, mappings: Mapping[]): number | null {
  const hashIdx = subLabel.indexOf('#');
  if (hashIdx >= 0) {
    const numStr = subLabel.slice(hashIdx + 1).split(/\s/)[0];
    const n = parseInt(numStr, 10);
    if (!isNaN(n)) return n;
  }
  const csvName = subLabel.split(' → ')[0].trim();
  const m = mappings.find((mm) => mm.csvName === csvName);
  return m ? mappings.indexOf(m) : null;
}

/** 单项撤销可行性预判（不 clone opts，用于 diff 面板灰化 ↩ 按钮）。 */
export function canRevertSingleItem(
  item: DiffPanelItem,
  targetSnap: ImportOptions,
  mappings: Mapping[],
): boolean {
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
      const idx = extractColIdx(sub, mappings);
      if (idx === null) return false;
      if (isDisplayEmptyStr(item.after)) return !isDisplayEmptyStr(item.before);
      return true;
    }
    case '校验': {
      const idx = extractColIdx(sub, mappings);
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
}

/**
 * 单项撤销的核心变换：返回 opts 的浅克隆 deep-copy 并把 item.field 恢复为 targetSnap 值；
 * 字段不可恢复（复杂字段回退需全撤销）时返回 null。不改传入 opts。
 */
export function revertSingleItem(
  item: DiffPanelItem,
  targetSnap: ImportOptions,
  currentOpts: ImportOptions,
  mappings: Mapping[],
): ImportOptions | null {
  const next = JSON.parse(JSON.stringify(currentOpts)) as ImportOptions;
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
      const idx = extractColIdx(sub, mappings);
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
      const idx = extractColIdx(sub, mappings);
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
      const idx = extractColIdx(sub, mappings);
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
}

export type SingleItemUndoOutcome =
  | { status: 'applied'; next: ImportOptions }
  | { status: 'already' }
  | { status: 'impossible' };

/**
 * 执行单项撤销的纯判定：applied=产出新 opts（调用方 setOpts + 压合成 FixEntry）；
 * already=该字段已是目标值；impossible=无法单项撤销。不改传入 opts。
 */
export function applySingleItemUndo(
  item: DiffPanelItem,
  targetSnap: ImportOptions,
  currentOpts: ImportOptions,
  mappings: Mapping[],
): SingleItemUndoOutcome {
  const next = revertSingleItem(item, targetSnap, currentOpts, mappings);
  if (!next) return { status: 'impossible' };
  if (JSON.stringify(next) === JSON.stringify(currentOpts)) return { status: 'already' };
  return { status: 'applied', next };
}
