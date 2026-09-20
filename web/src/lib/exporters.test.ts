import { describe, expect, it } from 'vitest';
import {
  toCsv, toTsv, toJson, toNdjson, toMarkdown, toSqlInsert, toInClause,
} from './exporters';

describe('exporters', () => {
  const cols = ['id', 'name', 'score'];

  it('csv quotes and BOM', () => {
    const out = toCsv(cols, [[1, "o'brien", 10], [2, 'comma, x', null]]);
    expect(out.startsWith('\uFEFF')).toBe(true);
    const body = out.slice(1).split('\n');
    expect(body[0]).toBe('id,name,score');
    expect(body[1]).toBe("1,o'brien,10");
    expect(body[2]).toBe('2,"comma, x",');
  });

  it('tsv flattens tabs/newlines in cells', () => {
    const out = toTsv(cols, [['a\tb', 'c\nd'], [null, 1]]);
    const lines = out.split('\n');
    expect(lines[0]).toBe('id\tname\tscore');
    expect(lines[1]).toBe('a b\tc d');
    expect(lines[2]).toBe('\t1');
  });

  it('json object array', () => {
    const out = JSON.parse(toJson(cols, [[1, 'a', 2]]));
    expect(out).toEqual([{ id: 1, name: 'a', score: 2 }]);
  });

  it('ndjson one object per line', () => {
    const out = toNdjson(cols, [[1, 'a', 2], [3, 'b', 4]]);
    const lines = out.split('\n');
    expect(lines).toHaveLength(2);
    expect(JSON.parse(lines[0])).toEqual({ id: 1, name: 'a', score: 2 });
  });

  it('markdown table', () => {
    const out = toMarkdown(cols, [[1, 'a|b', 2]]);
    const lines = out.split('\n');
    expect(lines[0]).toBe('| id | name | score |');
    expect(lines[1]).toBe('| --- | --- | --- |');
    expect(lines[2]).toBe('| 1 | a\\|b | 2 |');
  });

  it('sql insert with postgres quoting and NULL', () => {
    const out = toSqlInsert('postgres', 'main.users', cols, [[null, "a'b", 2]]);
    expect(out).toBe('INSERT INTO "main"."users" ("id", "name", "score") VALUES (NULL, \'a\'\'b\', 2);');
  });

  it('sql insert mysql backticks', () => {
    const out = toSqlInsert('mysql', 'users', cols, [[1, 'a', 2]]);
    expect(out).toBe('INSERT INTO `users` (`id`, `name`, `score`) VALUES (1, \'a\', 2);');
  });

  it('in clause dedupes and quotes', () => {
    const out = toInClause('postgres', 'name', ["a", "a", "b'c", null] as unknown[]);
    expect(out).toBe('WHERE "name" IN (\'a\', \'b\'\'c\', NULL)');
  });
});
