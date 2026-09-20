import { describe, expect, it } from 'vitest';
import {
  ChangeQueue,
  buildStatements,
  commitInTransaction,
  dialectFor,
  valueToLiteral,
  valuesEqual,
} from './changes';

describe('ChangeQueue', () => {
  it('merges repeated edits to the same cell keeping the original old value', () => {
    const q = new ChangeQueue();
    const original = { a: 'x', b: 1 };
    q.addCellChange('r1', 'a', 'x', 'y', original);
    q.addCellChange('r1', 'a', 'y', 'z', original);
    const c = q.get('r1')!;
    expect(c!.cellChanges).toHaveLength(1);
    expect(c!.cellChanges[0]).toEqual({ column: 'a', oldValue: 'x', newValue: 'z' });
    expect(q.size).toBe(1);
  });

  it('collapses when edited back to the original value', () => {
    const q = new ChangeQueue();
    const original = { a: 'x' };
    q.addCellChange('r1', 'a', 'x', 'y', original);
    expect(q.size).toBe(1);
    q.addCellChange('r1', 'a', 'y', 'x', original);
    expect(q.size).toBe(0);
  });

  it('ignores no-op edits', () => {
    const q = new ChangeQueue();
    q.addCellChange('r1', 'a', 'x', 'x', { a: 'x' });
    expect(q.size).toBe(0);
  });

  it('undoes row-level ops (delete/insert)', () => {
    const q = new ChangeQueue();
    q.addDelete('r1', { id: 1, a: 'x' });
    q.addInsert('r2', { id: 2, a: 'y' });
    expect(q.size).toBe(2);
    expect(q.undo()).toBe(true);
    expect(q.has('r2')).toBe(false);
    expect(q.undo()).toBe(true);
    expect(q.size).toBe(0);
  });

  it('orders statements by user sequence', () => {
    const q = new ChangeQueue();
    q.addDelete('r1', { id: 5, a: 'x' });
    q.addInsert('r1', { id: 5, a: 'y' }); // 复用同值
    const all = q.all;
    expect(all[0].type).toBe('delete');
    expect(all[1].type).toBe('insert');
    expect(all[1].sequence).toBeGreaterThan(all[0].sequence);
  });
});

describe('buildStatements', () => {
  const d = dialectFor('sqlite');

  it('generates PK-based UPDATE/DELETE and INSERT with DEFAULT pk', () => {
    const q = new ChangeQueue();
    q.addCellChange('r1', 'name', 'a', 'b', { id: 1, name: 'a' });
    q.addDelete('r2', { id: 2, name: 'c' });
    q.addInsert('r3', { id: 3, name: 'd' });
    q.addInsert('r4', { id: '', name: 'e' }); // 自增 PK 置 DEFAULT
    const stmts = buildStatements(q.all, 'main', 't', ['id'], d);
    expect(stmts).toHaveLength(4);
    expect(stmts[0].sql).toBe('UPDATE "main"."t" SET "name" = \'b\' WHERE "id" = 1');
    expect(stmts[0].parameterized.sql).toBe('UPDATE "main"."t" SET "name" = ? WHERE "id" = ?');
    expect(stmts[0].parameterized.params).toEqual(['b', 1]);
    expect(stmts[1].sql).toContain('DELETE FROM "main"."t" WHERE "id" = 2');
    expect(stmts[2].sql).toBe('INSERT INTO "main"."t" ("id", "name") VALUES (3, \'d\')');
    expect(stmts[3].sql).toBe('INSERT INTO "main"."t" ("id", "name") VALUES (DEFAULT, \'e\')');
    expect(stmts[3].parameterized.params).toEqual(['e']);
    // keyed update 允许 0（同值更新）；delete/insert 不允许
    expect(stmts[0].allowZero).toBe(true);
    expect(stmts[1].allowZero).toBe(false);
  });

  it('mysql dialect uses backticks', () => {
    const d = dialectFor('mysql');
    expect(d.quoteIdent('my table')).toBe('`my table`');
    expect(dialectFor('mssql').quoteIdent('x')).toBe('[x]');
    expect(valueToLiteral("a'b", d)).toBe("'a''b'");
    expect(valueToLiteral(null, d)).toBe('NULL');
  });
});

describe('commitInTransaction', () => {
  it('verifies affected rows and rolls back on mismatch', async () => {
    const calls: string[] = [];
    const stmts = [
      {
        sql: 'DELETE',
        parameterized: { sql: 'DELETE', params: [] },
        expectAffected: 1,
        allowZero: false,
      },
    ];
    const out = await commitInTransaction(
      'c1',
      stmts as never,
      async () => {
        calls.push('begin');
        return 'txn-1';
      },
      async () => {
        calls.push('exec');
        return { affected_rows: 0 }; // 行已不在 → 影响行数不符
      },
      async (tx, commit) => {
        calls.push(commit ? 'commit' : 'rollback');
      },
    );
    expect(out.ok).toBe(false);
    expect(out.error).toContain('影响行数');
    expect(calls).toEqual(['begin', 'exec', 'rollback']);
  });

  it('allows zero-affected for keyed update and commits', async () => {
    const stmts = [
      {
        sql: 'UPDATE',
        parameterized: { sql: 'UPDATE', params: [] },
        expectAffected: 1,
        allowZero: true,
      },
    ];
    const out = await commitInTransaction(
      'c1',
      stmts as never,
      async () => 'txn-1',
      async () => ({ affected_rows: 0 }),
      async () => {
        /* commit */
      },
    );
    expect(out).toEqual({ ok: true, applied: 1 });
  });

  it('rolls back on executor error and reports it', async () => {
    let rolled = false;
    const out = await commitInTransaction(
      'c1',
      [
        { sql: 'X', parameterized: { sql: 'X', params: [] }, expectAffected: 1, allowZero: false },
      ] as never,
      async () => 'txn-1',
      async () => {
        throw new Error('db boom');
      },
      async (_tx, commit) => {
        if (!commit) rolled = true;
      },
    );
    expect(out.ok).toBe(false);
    expect(out.error).toBe('db boom');
    expect(rolled).toBe(true);
  });
});

describe('valuesEqual', () => {
  it('compares null/number/object', () => {
    expect(valuesEqual(null, null)).toBe(true);
    expect((valuesEqual as (a: unknown, b: unknown) => boolean)(undefined, null)).toBe(true);
    expect(valuesEqual(1, 1)).toBe(true);
    expect(valuesEqual(1, '1')).toBe(false);
    expect(valuesEqual({ a: 1 }, { a: 1 })).toBe(true);
  });
});
