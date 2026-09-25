import { describe, expect, it } from 'vitest';
import type { ConnectionInfo, DdlScript, DdlStatement, SchemaDiffEntry } from '../api';
import {
  candidateConnections,
  defaultSelection,
  fillGroupGaps,
  groupByPhase,
  groupGaps,
  loadSyncState,
  planExecution,
  rebuildLikelyTables,
  saveSyncState,
  selectedIds,
  summarizeScript,
  toggleEntry,
} from './compareSync';

const entry = (over: Partial<SchemaDiffEntry> & { id: string; op: SchemaDiffEntry['op']; phase: number }): SchemaDiffEntry => ({
  table: 't',
  destructive: false,
  selected: true,
  reason: '',
  ...over,
});

const stmt = (over: Partial<DdlStatement> & { sql: string }): DdlStatement => ({
  entry_ids: ['t/x/add_column'],
  phase: 60,
  table: 't',
  non_reversible: false,
  ...over,
});

const conn = (id: string, kind: string): ConnectionInfo => ({
  id, name: id, kind: kind as ConnectionInfo['kind'], created_at: '', updated_at: '',
});

describe('groupByPhase', () => {
  it('按相位聚段并保持服务端顺序', () => {
    const groups = groupByPhase([
      entry({ id: 'a//drop_table', op: 'drop_table', phase: 40 }),
      entry({ id: 'b//create_table', op: 'create_table', phase: 50 }),
      entry({ id: 't/note/add_column', op: 'add_column', phase: 60 }),
      entry({ id: 't/qty/alter_column', op: 'alter_column', phase: 70 }),
    ]);
    expect(groups.map((g) => g.phase)).toEqual([40, 50, 60, 70]);
    expect(groups[0].label).toBe('删除表');
    expect(groups[3].label).toBe('变更列');
  });

  it('空计划得到空分组（不是 undefined）', () => {
    expect(groupByPhase([])).toEqual([]);
  });
});

describe('勾选集', () => {
  it('默认勾选直接采用服务端回值', () => {
    const entries = [
      entry({ id: 'x//drop_table', op: 'drop_table', phase: 40, destructive: true, selected: false }),
      entry({ id: 't/note/add_column', op: 'add_column', phase: 60 }),
    ];
    expect(defaultSelection(entries)).toEqual(['t/note/add_column']);
  });

  it('回传 id 按计划顺序，勾掉的不出现', () => {
    const entries = [
      entry({ id: 'e1', op: 'drop_table', phase: 40 }),
      entry({ id: 'e2', op: 'add_column', phase: 60 }),
      entry({ id: 'e3', op: 'add_index', phase: 80 }),
    ];
    expect(selectedIds(entries, ['e3', 'e1'])).toEqual(['e1', 'e3']);
    expect(selectedIds(entries, ['e2', 'ghost'])).toEqual(['e2']);
  });

  it('toggle 是幂等往返', () => {
    expect(toggleEntry(['a'], 'a')).toEqual([]);
    expect(toggleEntry(['a'], 'b')).toEqual(['a', 'b']);
  });
});

describe('重建整组提示', () => {
  // 按服务端真实顺序（相位 60 的 t、u 两段在前，70 的改列在后）
  const entries = [
    entry({ id: 't/note/add_column', op: 'add_column', phase: 60, table: 't' }),
    entry({ id: 'u/x/add_column', op: 'add_column', phase: 60, table: 'u' }),
    entry({ id: 't/qty/alter_column', op: 'alter_column', phase: 70, table: 't' }),
  ];

  it('只有含重建动作的表进候选', () => {
    expect(rebuildLikelyTables(entries)).toEqual(['t']);
  });

  it('勾了重建动作但同表漏勾 ⇒ 报缺口', () => {
    expect(groupGaps(entries, ['t/qty/alter_column'])).toEqual([{ table: 't', missing: ['t/note/add_column'] }]);
  });

  it('整组都勾上就没有缺口', () => {
    expect(groupGaps(entries, ['t/qty/alter_column', 't/note/add_column'])).toEqual([]);
  });

  it('补全后按计划顺序返回（60 在 70 之前）', () => {
    expect(fillGroupGaps(entries, ['t/qty/alter_column'])).toEqual(['t/note/add_column', 't/qty/alter_column']);
  });
});

describe('planExecution', () => {
  const script = (over: Partial<DdlScript>): DdlScript => ({
    statements: [],
    rollback: [],
    pragmas_outside_tx: false,
    ddl_in_tx: true,
    ...over,
  });

  it('PRAGMA 首尾被摘出来（按相位 0/255 且无条目）', () => {
    const p = planExecution(script({
      pragmas_outside_tx: true,
      statements: [
        stmt({ sql: 'PRAGMA foreign_keys=OFF;', phase: 0, table: '', entry_ids: [] }),
        stmt({ sql: 'CREATE TABLE "t"(id INTEGER);', phase: 50 }),
        stmt({ sql: 'PRAGMA foreign_keys=ON;', phase: 255, table: '', entry_ids: [] }),
      ],
    }));
    expect(p.pragmaHead?.sql).toBe('PRAGMA foreign_keys=OFF;');
    expect(p.pragmaTail?.sql).toBe('PRAGMA foreign_keys=ON;');
    expect(p.body.map((s) => s.sql)).toEqual(['CREATE TABLE "t"(id INTEGER);']);
    expect(p.wrapInTx).toBe(true);
  });

  it('不含重建时主体一条不落外面', () => {
    const p = planExecution(script({ statements: [stmt({ sql: 'ALTER TABLE "t" ADD COLUMN "n" text;' })] }));
    expect(p.pragmaHead).toBeNull();
    expect(p.pragmaTail).toBeNull();
    expect(p.body.length).toBe(1);
  });

  it('ddl_in_tx=false ⇒ 主体逐条跑不包事务', () => {
    const p = planExecution(script({ ddl_in_tx: false, statements: [stmt({ sql: 'X' })] }));
    expect(p.wrapInTx).toBe(false);
    expect(p.body.length).toBe(1);
  });
});

describe('summarizeScript', () => {
  it('统计不可逆与告警条数、去重表名', () => {
    const s = summarizeScript([
      stmt({ sql: 'DROP TABLE "t";', table: 't', non_reversible: true }),
      stmt({ sql: 'DROP TABLE "T2";', table: 'T2', warnings: ['重建表会丢失该表上的触发器（快照不含触发器）'] }),
      stmt({ sql: 'PRAGMA foreign_keys=ON;', table: '', phase: 255, entry_ids: [] }),
    ]);
    expect(s).toEqual({ total: 3, nonReversible: 1, warned: 1, tables: ['t', 'T2'] });
  });
});

describe('candidateConnections', () => {
  it('Redis 与跨种类都不进候选（服务端一律 501）', () => {
    const all = [conn('a', 'sqlite'), conn('b', 'sqlite'), conn('r', 'redis'), conn('p', 'postgres')];
    expect(candidateConnections(all, conn('a', 'sqlite')).map((c) => c.id)).toEqual(['a', 'b']);
    expect(candidateConnections(all, conn('r', 'redis'))).toEqual([]);
    expect(candidateConnections(all, null)).toEqual([]);
  });
});

describe('polydb.compareSync.v1', () => {
  it('落库后原样读回，但 review/apply 回落 target（差异与脚本不入库）', () => {
    saveSyncState({ ...loadSyncState(), sourceId: 'a', targetId: 'b', step: 'review', selectDestructive: true });
    const back = loadSyncState();
    expect(back.sourceId).toBe('a');
    expect(back.targetId).toBe('b');
    expect(back.step).toBe('target');
    expect(back.selectDestructive).toBe(true);
    saveSyncState({ ...back, step: 'apply' });
    expect(loadSyncState().step).toBe('target');
  });

  it('坏数据回落默认步序，不炸', () => {
    localStorage.setItem('polydb.compareSync.v1', '{not json');
    expect(loadSyncState().step).toBe('source');
    localStorage.setItem('polydb.compareSync.v1', JSON.stringify({ step: 'nope', sourceId: 42 }));
    expect(loadSyncState()).toMatchObject({ step: 'source', sourceId: '' });
  });

  it('勾选集不参与持久化', () => {
    saveSyncState({ ...loadSyncState(), sourceId: 'a' });
    expect(localStorage.getItem('polydb.compareSync.v1')).not.toContain('selected');
  });
});
