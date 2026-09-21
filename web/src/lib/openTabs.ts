// U1 对象标签模型（参考 TablePro 窗口 tab 制）：workbench 常驻 + 每表一 tab。
// SQL 查询子标签仍由 QueryWorkspace 自管，这里只管「打开的对象」。

export const WORKBENCH_ID = 'workbench';

export interface WorkbenchTab {
  id: typeof WORKBENCH_ID;
  kind: 'workbench';
}

export interface TableFilterPreset {
  column: string;
  op: 'eq';
  value: string | number;
}

export interface TableTabInfo {
  id: string;
  kind: 'table';
  schema: string;
  table: string;
  // U6.2 FK 跳转带入的一次性过滤条件（TableDataView 消费后清除）。
  preset?: TableFilterPreset;
}

export type OpenTab = WorkbenchTab | TableTabInfo;

export interface OpenTabsState {
  tabs: OpenTab[];
  activeId: string;
}

const KEY = 'polydb.openTabs.v1';

export function tableTabId(schema: string, table: string): string {
  return `table:${schema}.${table}`;
}

export function initialState(): OpenTabsState {
  return { tabs: [{ id: WORKBENCH_ID, kind: 'workbench' }], activeId: WORKBENCH_ID };
}

export function loadOpenTabs(connId: string): OpenTabsState {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) {
      const stored = JSON.parse(raw)?.[connId];
      if (stored && Array.isArray(stored.tabs)) {
        const tabs: OpenTab[] = stored.tabs.filter(
          (t: OpenTab) =>
            (t.kind === 'workbench' && t.id === WORKBENCH_ID) ||
            (t.kind === 'table' && typeof t.id === 'string' && typeof t.schema === 'string' && typeof t.table === 'string'),
        );
        const wb = tabs.find((t) => t.kind === 'workbench');
        const rest = tabs.filter((t) => t.kind !== 'workbench');
        const normalized: OpenTabsState = {
          tabs: [wb ?? { id: WORKBENCH_ID, kind: 'workbench' }, ...rest],
          activeId: tabs.some((t) => t.id === stored.activeId) ? stored.activeId : WORKBENCH_ID,
        };
        return normalized;
      }
    }
  } catch { /* quota / malformed */ }
  return initialState();
}

export function saveOpenTabs(connId: string, state: OpenTabsState): void {
  try {
    const raw = localStorage.getItem(KEY);
    const obj = raw ? JSON.parse(raw) : {};
    if (!obj || typeof obj !== 'object') return;
    obj[connId] = state;
    localStorage.setItem(KEY, JSON.stringify(obj));
  } catch { /* quota */ }
}

/** 打开表 tab：已存在则聚焦（可更新 preset），否则追加并激活。 */
export function openTableTab(state: OpenTabsState, schema: string, table: string, preset?: TableFilterPreset): { state: OpenTabsState; isNew: boolean } {
  const id = tableTabId(schema, table);
  if (state.tabs.some((t) => t.id === id)) {
    const tabs = preset
      ? state.tabs.map((t) => (t.id === id && t.kind === 'table' ? { ...t, preset } : t))
      : state.tabs;
    return { state: { ...state, tabs, activeId: id }, isNew: false };
  }
  const tab: TableTabInfo = { id, kind: 'table', schema, table, preset };
  return { state: { tabs: [...state.tabs, tab], activeId: id }, isNew: true };
}

/** 清除表 tab 的一次性过滤 preset（已被数据视图消费后调用）。 */
export function clearTableTabPreset(state: OpenTabsState, id: string): OpenTabsState {
  return { ...state, tabs: state.tabs.map((t) => (t.id === id && t.kind === 'table' && t.preset ? { ...t, preset: undefined } : t)) };
}

/** 关闭 tab（workbench 不可关）。关闭活动 tab 后回落到左邻或 workbench。 */
export function closeOpenTab(state: OpenTabsState, id: string): OpenTabsState {
  if (id === WORKBENCH_ID) return state;
  const idx = state.tabs.findIndex((t) => t.id === id);
  if (idx < 0) return state;
  const tabs = state.tabs.filter((t) => t.id !== id);
  let activeId = state.activeId;
  if (state.activeId === id) {
    activeId = tabs[idx - 1]?.id ?? WORKBENCH_ID;
  }
  return { tabs, activeId };
}
