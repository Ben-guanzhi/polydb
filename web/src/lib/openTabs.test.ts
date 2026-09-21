import { beforeEach, describe, expect, it } from 'vitest';
import { closeOpenTab, initialState, loadOpenTabs, openTableTab, saveOpenTabs, tableTabId, WORKBENCH_ID } from './openTabs';

beforeEach(() => localStorage.clear());

describe('openTabs', () => {
  it('初始状态只含 workbench 且激活', () => {
    const s = initialState();
    expect(s.tabs.map((t) => t.id)).toEqual([WORKBENCH_ID]);
    expect(s.activeId).toBe(WORKBENCH_ID);
  });

  it('openTableTab 新开并激活；重复打开只聚焦', () => {
    const s0 = initialState();
    const { state: s1, isNew } = openTableTab(s0, 'main', 'users');
    expect(isNew).toBe(true);
    expect(s1.tabs).toHaveLength(2);
    expect(s1.activeId).toBe(tableTabId('main', 'users'));
    const { state: s2, isNew: again } = openTableTab(s1, 'main', 'users');
    expect(again).toBe(false);
    expect(s2.tabs).toHaveLength(2);
  });

  it('closeOpenTab 回落到左邻；workbench 不可关', () => {
    let s = initialState();
    s = openTableTab(s, 'main', 'users').state;
    s = openTableTab(s, 'main', 'orders').state;
    expect(s.activeId).toBe(tableTabId('main', 'orders'));
    s = closeOpenTab(s, tableTabId('main', 'orders'));
    expect(s.activeId).toBe(tableTabId('main', 'users'));
    s = closeOpenTab(s, tableTabId('main', 'users'));
    expect(s.activeId).toBe(WORKBENCH_ID);
    s = closeOpenTab(s, WORKBENCH_ID);
    expect(s.tabs[0].kind).toBe('workbench');
  });

  it('持久化往返 + 损坏数据回落初始', () => {
    const s = openTableTab(initialState(), 'public', 't1').state;
    saveOpenTabs('c1', s);
    expect(loadOpenTabs('c1')).toEqual(s);
    expect(loadOpenTabs('other')).toEqual(initialState());
    localStorage.setItem('polydb.openTabs.v1', '{{{bad json');
    expect(loadOpenTabs('c1')).toEqual(initialState());
  });

  it('缺失 workbench 的存档会补回第一位', () => {
    localStorage.setItem('polydb.openTabs.v1', JSON.stringify({
      c2: { tabs: [{ id: 'table:x.y', kind: 'table', schema: 'x', table: 'y' }], activeId: 'table:x.y' },
    }));
    const s = loadOpenTabs('c2');
    expect(s.tabs[0].id).toBe(WORKBENCH_ID);
    expect(s.tabs).toHaveLength(2);
    expect(s.activeId).toBe('table:x.y');
  });
});
