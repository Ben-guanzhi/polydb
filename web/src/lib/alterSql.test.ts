import { describe, expect, it } from 'vitest';
import {
  UnsupportedAlterError,
  addColumnSql,
  addForeignKeySql,
  addIndexSql,
  dialectOf,
  dropColumnSql,
  dropForeignKeySql,
  dropIndexSql,
  quoteIdent,
  quoteTable,
  renameColumnSql,
  type NewForeignKey,
} from './alterSql';

describe('dialectOf', () => {
  it('maps DatabaseKind to dialect, defaults postgres', () => {
    expect(dialectOf('sqlite')).toBe('sqlite');
    expect(dialectOf('postgres')).toBe('postgres');
    expect(dialectOf('mysql')).toBe('mysql');
    expect(dialectOf('mssql')).toBe('mssql');
    expect(dialectOf('oracle')).toBe('oracle');
    expect(dialectOf('redis')).toBe('postgres');
  });
});

describe('quoting', () => {
  it('quoteIdent double-quotes; oracle lowercases', () => {
    expect(quoteIdent('postgres', 'user')).toBe('"user"');
    expect(quoteIdent('oracle', 'USER')).toBe('"user"');
    expect(quoteIdent('mssql', 'a-b')).toBe('"a-b"');
  });
  it('quoteTable with/without schema', () => {
    expect(quoteTable('postgres', 'public', 't')).toBe('"public"."t"');
    expect(quoteTable('postgres', '', 't')).toBe('"t"');
    expect(quoteTable('oracle', 'SALES', 'T')).toBe('"sales"."t"');
  });
});

describe('addColumnSql', () => {
  it('postgres: nullable + default', () => {
    const s = addColumnSql('postgres', 'public', 't', { name: 'age', type: 'integer', nullable: true, defaultExpr: '0' });
    expect(s).toBe('ALTER TABLE "public"."t" ADD COLUMN "age" INTEGER DEFAULT 0;');
  });
  it('postgres: NOT NULL + PK', () => {
    const s = addColumnSql('postgres', '', 't', { name: 'id', type: 'bigint', primaryKey: true });
    expect(s).toBe('ALTER TABLE "t" ADD COLUMN "id" BIGINT PRIMARY KEY;');
  });
  it('sqlite: NOT NULL without default is rejected', () => {
    expect(() => addColumnSql('sqlite', '', 't', { name: 'x', type: 'int', nullable: false })).toThrow(UnsupportedAlterError);
  });
  it('sqlite: NOT NULL with default is allowed', () => {
    expect(addColumnSql('sqlite', '', 't', { name: 'x', type: 'int', nullable: false, defaultExpr: '1' })).toContain('DEFAULT 1');
  });
  it('sqlite: PRIMARY KEY on add column rejected', () => {
    expect(() => addColumnSql('sqlite', '', 't', { name: 'id', type: 'int', primaryKey: true })).toThrow(UnsupportedAlterError);
  });
  it('mysql/mssql/oracle append NULL/NOT NULL explicitly', () => {
    expect(addColumnSql('mysql', '', 't', { name: 'a', type: 'int', nullable: true })).toContain(' NULL');
    expect(addColumnSql('mssql', '', 't', { name: 'a', type: 'int', nullable: false })).toContain('NOT NULL');
    expect(addColumnSql('oracle', '', 't', { name: 'a', type: 'int', nullable: false })).toContain('NOT NULL');
  });
  it('rejects illegal column name', () => {
    expect(() => addColumnSql('postgres', '', 't', { name: 'a b', type: 'int' })).toThrow(UnsupportedAlterError);
  });
});

describe('dropColumnSql / renameColumnSql', () => {
  it('drop column', () => {
    expect(dropColumnSql('postgres', 'public', 't', 'c')).toBe('ALTER TABLE "public"."t" DROP COLUMN "c";');
    expect(dropColumnSql('oracle', '', 'T', 'C')).toBe('ALTER TABLE "t" DROP COLUMN "c";');
  });
  it('rename column rejects on oracle', () => {
    expect(() => renameColumnSql('oracle', '', 't', 'a', 'b')).toThrow(UnsupportedAlterError);
  });
  it('rename column postgres', () => {
    expect(renameColumnSql('postgres', '', 't', 'a', 'b')).toBe('ALTER TABLE "t" RENAME COLUMN "a" TO "b";');
  });
});

describe('indexes', () => {
  it('add index', () => {
    expect(addIndexSql('postgres', 'public', 't', { name: 'ix', columns: ['a', 'b'], unique: true })).toBe(
      'CREATE UNIQUE INDEX "public"."ix" ON "public"."t" ("a", "b");',
    );
    expect(addIndexSql('mysql', '', 't', { name: 'ix', columns: ['a'] })).toBe('CREATE INDEX "ix" ON "t" ("a");');
    expect(addIndexSql('mssql', '', 't', { name: 'ix', columns: ['a'] })).toBe('CREATE INDEX "ix" ON "t" ("a");');
  });
  it('drop index', () => {
    expect(dropIndexSql('postgres', 'public', 't', 'ix')).toBe('DROP INDEX "public"."ix";');
    expect(dropIndexSql('mssql', '', 't', 'ix')).toBe('DROP INDEX "ix" ON "t";');
    expect(dropIndexSql('oracle', '', 't', 'ix')).toBe('DROP INDEX "ix";');
  });
});

describe('foreign keys', () => {
  const fk: NewForeignKey = {
    name: 'fk_orders',
    columns: ['user_id'],
    refTable: 'users',
    refColumns: ['id'],
    onDelete: 'cascade',
  };
  it('postgres with schema-qualified ref', () => {
    expect(addForeignKeySql('postgres', 'public', 'orders', { ...fk, refSchema: 'public' })).toBe(
      'ALTER TABLE "public"."orders" ADD CONSTRAINT "fk_orders" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON DELETE CASCADE;',
    );
  });
  it('sqlite rejected (rebuild required)', () => {
    expect(() => addForeignKeySql('sqlite', '', 'orders', fk)).toThrow(UnsupportedAlterError);
  });
  it('drop fk postgres/mysql differ; oracle rejected', () => {
    expect(dropForeignKeySql('postgres', '', 'orders', 'fk_orders')).toBe('ALTER TABLE "orders" DROP CONSTRAINT "fk_orders";');
    expect(dropForeignKeySql('mysql', '', 'orders', "fk_orders")).toBe('ALTER TABLE "orders" DROP FOREIGN KEY "fk_orders";');
    expect(() => dropForeignKeySql('oracle', '', 'orders', 'fk_orders')).toThrow(UnsupportedAlterError);
  });
});
