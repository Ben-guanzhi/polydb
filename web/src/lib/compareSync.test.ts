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
  runScript,
  saveSyncState,
  selectedIds,
  summarizeScript,
  toggleEntry,
  type ApplyDeps,
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

// ─── runScript：执行分派的应用环路（E6.1）───────────────────────
// 假引擎带一本"账"：事务内的语句先进 pending，commit 才落 db，rollback 把 pending 丢掉。
// 于是"已落地的语句到底会不会被事务撤销"成为可断言的事实，而不是只看 ran/failed 计数。
function makeEngine(failOn: (sql: string) => boolean = () => false, cancelAfter: number | null = null) {
  const calls: string[] = [];
  const db: string[] = [];
  let pending: string[] = [];
  let attempted = 0;
  const deps: ApplyDeps = {
    execute: async (_id, sql) => {
      calls.push(`exec:${sql}`);
      if (failOn(sql)) throw new Error(`boom: ${sql}`);
      db.push(sql);
    },
    begin: async () => {
      calls.push('begin');
      pending = [];
      return 'tx-1';
    },
    executeInTx: async (_tx, sql) => {
      calls.push(`tx:${sql}`);
      if (failOn(sql)) throw new Error(`boom: ${sql}`);
      pending.push(sql);
    },
    commit: async () => {
      calls.push('commit');
      db.push(...pending);
      pending = [];
    },
    rollback: async () => {
      calls.push('rollback');
      pending = [];
    },
    cancelled: () => {
      attempted++;
      return cancelAfter !== null && attempted > cancelAfter;
    },
  };
  return { deps, calls, db, landed: () => db.slice() };
}

const ddlScript = (over: Partial<DdlScript>): DdlScript => ({
  statements: [],
  rollback: [],
  pragmas_outside_tx: false,
  ddl_in_tx: true,
  ...over,
});

describe('runScript', () => {
  it('pragmas_outside_tx=false ⇒ 整份脚本一个事务，中途失败把已落地的语句一起撤销', async () => {
    const e = makeEngine((sql) => sql.includes('e6_tx(b'));
    const out = await runScript(ddlScript({
      ddl_in_tx: true,
      pragmas_outside_tx: false,
      statements: [
        stmt({ sql: 'CREATE TABLE e6_tx(a INTEGER);', phase: 50, table: 'e6_tx' }),
        stmt({ sql: 'CREATE TABLE e6_tx(b INTEGER);', phase: 50, table: 'e6_tx' }),
        stmt({ sql: 'CREATE TABLE e6_after(a INTEGER);', phase: 50, table: 'e6_after' }),
      ],
    }), 'c1', e.deps);

    expect(e.calls).toEqual([
      'begin',
      'tx:CREATE TABLE e6_tx(a INTEGER);',
      'tx:CREATE TABLE e6_tx(b INTEGER);',
      'rollback',
    ]);
    expect(out).toMatchObject({ ran: 1, rolledBack: true, skipped: 1 });
    expect(out.failed[0]).toMatchObject({ index: 1, table: 'e6_tx' });
    // 关键：ran=1 不等于落地——事务回滚后账上必须一条都没有。
    expect(e.landed()).toEqual([]);
  });

  it('ddl_in_tx=false ⇒ 一条事务都不开，失败留下中间态（与上一份断言互斥）', async () => {
    const e = makeEngine((sql) => sql.includes('e6_seq(b'));
    const out = await runScript(ddlScript({
      ddl_in_tx: false,
      statements: [
        stmt({ sql: 'CREATE TABLE e6_seq(a INTEGER);', phase: 50, table: 'e6_seq' }),
        stmt({ sql: 'CREATE TABLE e6_seq(b INTEGER);', phase: 50, table: 'e6_seq' }),
        stmt({ sql: 'CREATE TABLE e6_seq2(a INTEGER);', phase: 50, table: 'e6_seq2' }),
      ],
    }), 'c1', e.deps);

    expect(e.calls).toEqual([
      'exec:CREATE TABLE e6_seq(a INTEGER);',
      'exec:CREATE TABLE e6_seq(b INTEGER);',
    ]);
    expect(out).toMatchObject({ ran: 1, rolledBack: false, skipped: 1 });
    expect(e.landed()).toEqual(['CREATE TABLE e6_seq(a INTEGER);']);
  });

  it('pragmas_outside_tx=true ⇒ 首尾 PRAGMA 在事务外，主体共享同一个事务', async () => {
    const e = makeEngine();
    const out = await runScript(ddlScript({
      ddl_in_tx: true,
      pragmas_outside_tx: true,
      statements: [
        stmt({ sql: 'PRAGMA foreign_keys=OFF;', phase: 0, table: '', entry_ids: [] }),
        stmt({ sql: 'CREATE TABLE e6_w(a INTEGER);', phase: 50, table: 'e6_w' }),
        stmt({ sql: 'CREATE INDEX ix_e6_w ON e6_w(a);', phase: 80, table: 'e6_w' }),
        stmt({ sql: 'PRAGMA foreign_keys=ON;', phase: 255, table: '', entry_ids: [] }),
      ],
    }), 'c1', e.deps);

    expect(e.calls).toEqual([
      'exec:PRAGMA foreign_keys=OFF;',
      'begin',
      'tx:CREATE TABLE e6_w(a INTEGER);',
      'tx:CREATE INDEX ix_e6_w ON e6_w(a);',
      'commit',
      'exec:PRAGMA foreign_keys=ON;',
    ]);
    expect(out).toMatchObject({ ran: 4, rolledBack: false, skipped: 0 });
    expect(e.landed()).toEqual([
      'PRAGMA foreign_keys=OFF;',
      'CREATE TABLE e6_w(a INTEGER);',
      'CREATE INDEX ix_e6_w ON e6_w(a);',
      'PRAGMA foreign_keys=ON;',
    ]);
  });

  it('取消 ⇒ 停在该条之前；主体在事务里时整体回滚，账上不留半成品', async () => {
    const e = makeEngine(() => false, 1);
    const out = await runScript(ddlScript({
      ddl_in_tx: true,
      pragmas_outside_tx: false,
      statements: [
        stmt({ sql: 'CREATE TABLE e6_c1(a INTEGER);', phase: 50, table: 'e6_c1' }),
        stmt({ sql: 'CREATE TABLE e6_c2(a INTEGER);', phase: 50, table: 'e6_c2' }),
        stmt({ sql: 'CREATE TABLE e6_c3(a INTEGER);', phase: 50, table: 'e6_c3' }),
      ],
    }), 'c1', e.deps);

    expect(e.calls).toEqual(['begin', 'tx:CREATE TABLE e6_c1(a INTEGER);', 'rollback']);
    expect(out.ran).toBe(1);
    expect(out.rolledBack).toBe(true);
    expect(out.failed[0].reason).toContain('已取消');
    expect(e.landed()).toEqual([]);
  });

  it('回滚方向复用同一分派（组件传的是 { ...script, statements: script.rollback }）', async () => {
    const sc = ddlScript({
      pragmas_outside_tx: true,
      statements: [
        stmt({ sql: 'PRAGMA foreign_keys=OFF;', phase: 0, table: '', entry_ids: [] }),
        stmt({ sql: 'CREATE TABLE e6_w(a INTEGER);', phase: 70, table: 'e6_w' }),
        stmt({ sql: 'PRAGMA foreign_keys=ON;', phase: 255, table: '', entry_ids: [] }),
      ],
      rollback: [
        stmt({ sql: 'PRAGMA foreign_keys=OFF;', phase: 0, table: '', entry_ids: [] }),
        stmt({ sql: 'DROP TABLE e6_w;', phase: 70, table: 'e6_w' }),
        stmt({ sql: 'PRAGMA foreign_keys=ON;', phase: 255, table: '', entry_ids: [] }),
      ],
    });
    const e = makeEngine();
    const out = await runScript({ ...sc, statements: sc.rollback }, 'c1', e.deps);

    expect(e.calls).toEqual([
      'exec:PRAGMA foreign_keys=OFF;',
      'begin',
      'tx:DROP TABLE e6_w;',
      'commit',
      'exec:PRAGMA foreign_keys=ON;',
    ]);
    expect(out.ran).toBe(3);
    // 正向语句一条都没跑：回滚方向不得把 CREATE TABLE 再补一遍。
    expect(e.calls.some((c) => c.includes('CREATE TABLE'))).toBe(false);
  });
});
