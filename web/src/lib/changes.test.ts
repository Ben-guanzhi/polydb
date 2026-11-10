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

  it('归一化后的大整数发裸数字字面量（P2-4）', () => {
    const d = dialectFor('mysql');
    // 安全范围内的整数已经是 number，走原有的 number 分支。
    expect(valueToLiteral(42, d)).toBe('42');
    // 超出安全范围的大整数是精确十进制字符串：必须裸数字，加引号会生成字符串字面量。
    expect(valueToLiteral('9007199254740993', d)).toBe('9007199254740993');
    expect(valueToLiteral('-9007199254740995', d)).toBe('-9007199254740995');
    // 普通文本仍要引号包裹：安全范围内的整数字符串不是大整数。
    expect(valueToLiteral('42', d)).toBe("'42'");
    expect(valueToLiteral("a'b", d)).toBe("'a''b'");
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
        table: '',
        whereClause: '',
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
        return { affected_rows: 0, rows: [] }; // 行已不在 → 影响行数不符
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
        table: '',
        whereClause: '',
      },
    ];
    const out = await commitInTransaction(
      'c1',
      stmts as never,
      async () => 'txn-1',
      async () => ({ affected_rows: 0, rows: [] }),
      async () => {
        /* commit */
      },
    );
    expect(out).toEqual({ ok: true, applied: 1 });
  });

  // 0 行守卫（R2-02）：带定位谓词的 UPDATE 影响 0 行时跑同事务探针——
  // 行在 = 同值更新 no-op 放行；行不在 = 并发删除/PK 已改，整体回滚。
  // （table/whereClause 为空的 INSERT 不探针，0 行即回滚。）
  describe('zero-row probe', () => {
    const updWithKey = {
      sql: 'UPDATE t SET a = 2 WHERE id = 1',
      parameterized: { sql: 'UPDATE t SET a = ? WHERE id = ?', params: [2, 1] },
      expectAffected: 1,
      allowZero: true,
      table: 't',
      whereClause: 'id = 1',
    };

    it('probes the key in the same transaction and commits when the row is present', async () => {
      const calls: string[] = [];
      const sqls: string[] = [];
      const out = await commitInTransaction(
        'c1',
        [updWithKey],
        async () => {
          calls.push('begin');
          return 'txn-1';
        },
        async (_tx, req) => {
          sqls.push(req.sql);
          // 首次 = UPDATE 报 0 行；二次 = 探针，行还在。
          return sqls.length === 1 ? { affected_rows: 0, rows: [] } : { affected_rows: 0, rows: [[1]] };
        },
        async (_tx, commit) => {
          calls.push(commit ? 'commit' : 'rollback');
        },
      );
      expect(out).toEqual({ ok: true, applied: 1 });
      expect(sqls).toHaveLength(2);
      expect(sqls[1]).toContain('SELECT 1 FROM t WHERE id = 1');
      expect(calls).toEqual(['begin', 'commit']);
    });

    it('rolls back when the probe finds the row gone', async () => {
      const calls: string[] = [];
      const out = await commitInTransaction(
        'c1',
        [updWithKey],
        async () => {
          calls.push('begin');
          return 'txn-1';
        },
        async () => ({ affected_rows: 0, rows: [] }), // UPDATE 0 行 + 探针 0 行
        async (_tx, commit) => {
          calls.push(commit ? 'commit' : 'rollback');
        },
      );
      expect(out.ok).toBe(false);
      expect(out.error).toContain('已整体回滚');
      expect(calls).toEqual(['begin', 'rollback']);
    });

    it('treats a zero-row insert (no key predicate) as a mismatch', async () => {
      const calls: string[] = [];
      const out = await commitInTransaction(
        'c1',
        [{ ...updWithKey, table: '', whereClause: '', allowZero: false }],
        async () => {
          calls.push('begin');
          return 'txn-1';
        },
        async () => ({ affected_rows: 0, rows: [] }),
        async (_tx, commit) => {
          calls.push(commit ? 'commit' : 'rollback');
        },
      );
      expect(out.ok).toBe(false);
      expect(calls).toEqual(['begin', 'rollback']);
    });
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

  // R2-02 守卫漏洞：mismatched（影响行数超过预期）必须无条件回滚。探针行存在
  // 只对"keyed update 的 0 行同值更新"（allowZero && zeroRow && present，此时
  // mismatched 本就为 false）是合法豁免；delete/insert 的 0 行、以及
  // actual > expect 的 keyed update，即使探针查到行也必须回滚。
  describe('mismatched guard', () => {
    it('rolls back a keyed update whose actual rows exceed the expectation', async () => {
      const calls: string[] = [];
      const out = await commitInTransaction(
        'c1',
        [
          {
            sql: 'UPDATE t SET a = 2 WHERE id = 1',
            parameterized: { sql: 'UPDATE t SET a = ? WHERE id = ?', params: [2, 1] },
            expectAffected: 1,
            allowZero: true,
            table: 't',
            whereClause: 'id = 1',
          },
        ],
        async () => {
          calls.push('begin');
          return 'txn-1';
        },
        async (_tx, req) => {
          // 探针即使查到行也不足以豁免 actual > expect 的 mismatch。
          if (String(req.sql).startsWith('SELECT 1')) return { affected_rows: 0, rows: [[1]] };
          calls.push('exec');
          return { affected_rows: 2, rows: [] }; // actual=2 > expectAffected=1
        },
        async (_tx, commit) => {
          calls.push(commit ? 'commit' : 'rollback');
        },
      );
      expect(out.ok).toBe(false);
      expect(out.error).toContain('影响行数');
      expect(calls).toEqual(['begin', 'exec', 'rollback']);
    });

    it('rolls back a delete that affected 0 rows even when the probe row exists', async () => {
      const calls: string[] = [];
      const out = await commitInTransaction(
        'c1',
        [
          {
            sql: 'DELETE FROM t WHERE id = 1',
            parameterized: { sql: 'DELETE FROM t WHERE id = ?', params: [1] },
            expectAffected: 1,
            allowZero: false,
            table: 't',
            whereClause: 'id = 1',
          },
        ],
        async () => {
          calls.push('begin');
          return 'txn-1';
        },
        async (_tx, req) => {
          // DELETE 报 0 行，但探针查到行仍在：mismatch 不能被探针豁免。
          if (String(req.sql).startsWith('SELECT 1')) return { affected_rows: 0, rows: [[1]] };
          calls.push('exec');
          return { affected_rows: 0, rows: [] };
        },
        async (_tx, commit) => {
          calls.push(commit ? 'commit' : 'rollback');
        },
      );
      expect(out.ok).toBe(false);
      expect(out.error).toContain('影响行数');
      expect(calls).toEqual(['begin', 'exec', 'rollback']);
    });
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

  // 大整数（>2^53）经 bigintSafe 归一化为精确十进制字符串（bigintSafe.ts
  // isBigIntString 的唯一形态），valuesEqual 走字符串比较——相邻大整数必须
  // 判不等（Rust 端 int_float_equal / Go 端 ValuesEqual 同语义，R2-01 三端
  // 对齐）。钉住它防编辑器/解码路径回归：若哪天归一化改回 number，相邻大
  // 整数塌进同一 double，编辑被当 no-op 静默吞掉。
  it('keeps adjacent big integers distinct (string-carried)', () => {
    const even = '9007199254740992'; // 2^53
    const odd = '9007199254740993'; // 2^53+1
    expect(valuesEqual(even, even)).toBe(true);
    expect(valuesEqual(even, odd)).toBe(false);
    expect(valuesEqual('18446744073709551615', '18446744073709551614')).toBe(false);
    expect(valuesEqual('18446744073709551615', '18446744073709551615')).toBe(true);
    // 同值不同承载形态（number 5 vs 字符串大整数）不相等——类型即语义。
    expect(valuesEqual(5, '5')).toBe(false);
  });

  // int/float 相等（5 ↔ 5.0）：JS 无变体之分，同为 number 严格相等，与
  // Rust int_float_equal 的语义对齐（整数值浮点等于整数）。
  it('treats integral float equal to int', () => {
    expect(valuesEqual(5, 5.0)).toBe(true);
    expect(valuesEqual(5, 5.5)).toBe(false);
  });
});
