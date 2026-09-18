import { describe, it, expect } from 'vitest';
import { splitSql, countStatements, countParams, statementAtCursor } from './sqlSplit';

describe('splitSql', () => {
  it('按分号切分多条语句', () => {
    const stmts = splitSql('SELECT 1; SELECT 2; SELECT 3');
    expect(stmts.map((s) => s.sql.trim())).toEqual(['SELECT 1', 'SELECT 2', 'SELECT 3']);
  });

  it('忽略单引号字符串内部的分号', () => {
    const stmts = splitSql("INSERT INTO t(v) VALUES ('a;b'); SELECT 1;");
    expect(stmts.map((s) => s.sql.trim())).toEqual([
      "INSERT INTO t(v) VALUES ('a;b')",
      'SELECT 1',
    ]);
  });

  it('忽略双引号 / 反引号 / 方括号内的分号', () => {
    const stmts = splitSql('SELECT "col;x" FROM t; SELECT `a;b`; SELECT [x;y];');
    expect(stmts.length).toBe(3);
  });

  it('忽略 -- 和 # 单行注释及 /* */ 多行注释', () => {
    const src = '-- c1;\nSELECT 1; /* keep; */ SELECT 2; # c2;\nSELECT 3;';
    const stmts = splitSql(src);
    expect(stmts.map((s) => s.sql.trim())).toEqual(['SELECT 1', 'SELECT 2', 'SELECT 3']);
  });

  it('注释本身不构成 statement', () => {
    expect(splitSql('-- just a comment\n# another')).toEqual([]);
  });

  it('返回正确的 start/end 下标', () => {
    const star = 'SELECT 1;';
    const full = star + '  SELECT 2';
    const [a, b] = splitSql(full);
    expect(a.start).toBe(0);
    expect(a.end).toBe(star.length - 1);
    expect(full.slice(b.start, b.end)).toBe('SELECT 2');
  });
});

describe('countStatements / statementAtCursor', () => {
  it('countStatements 忽略注释', () => {
    expect(countStatements('SELECT 1; -- x;\n SELECT 2;')).toBe(2);
  });

  it('statementAtCursor 定位当前光标所在语句', () => {
    const sql = 'SELECT 1; SELECT 222; SELECT 3;';
    expect(statementAtCursor(sql, 0)).toBe(0);
    expect(statementAtCursor(sql, 15)).toBe(1);
    expect(statementAtCursor(sql, sql.length - 1)).toBe(2);
    expect(statementAtCursor('', 0)).toBe(-1);
  });
});

describe('countParams', () => {
  it('统计顶层 ? 忽略字符串与注释内的问号', () => {
    expect(countParams('SELECT * FROM t WHERE a=? AND b=?')).toBe(2);
    expect(countParams("SELECT '?' WHERE x=? -- ?\n AND y=?" )).toBe(2);
    expect(countParams("SELECT /* ? */ * WHERE z=?")).toBe(1);
    expect(countParams('SELECT "col?" FROM t WHERE id=?')).toBe(1);
  });

  it('空串为 0', () => {
    expect(countParams('')).toBe(0);
  });
});