import { describe, expect, it } from 'vitest';
import type { ColumnInfo, ForeignKeyInfo } from '../api';
import { buildERModel, layoutER, nodeHeight } from './erDiagram';

function col(name: string, over: Partial<ColumnInfo> = {}): ColumnInfo {
  return {
    name,
    data_type: 'integer',
    nullable: false,
    ordinal_position: 1,
    ...over,
  } as ColumnInfo;
}

describe('buildERModel', () => {
  it('orders columns by ordinal and collects pk/fk columns', () => {
    const tables = [
      { name: 'orders', schema: 'main', columns: [col('total', { ordinal_position: 2 }), col('id', { is_primary_key: true, ordinal_position: 1 }), col('user_id', { ordinal_position: 3 })] },
      { name: 'users', schema: 'main', columns: [col('id', { is_primary_key: true, ordinal_position: 1 }), col('name', { ordinal_position: 2 })] },
    ];
    const fks: Record<string, ForeignKeyInfo[]> = {
      orders: [
        {
          name: 'fk_orders_user',
          columns: ['user_id'],
          referenced_schema: 'main',
          referenced_table: 'users',
          referenced_columns: ['id'],
          on_delete: 'cascade',
        },
      ],
    };
    const m = buildERModel(tables, fks);
    expect(m.nodes.map((n) => n.table)).toEqual(['orders', 'users']);
    const orders = m.nodes[0];
    expect(orders.columns).toEqual(['id', 'total', 'user_id']);
    expect(orders.pk).toEqual(['id']);
    expect(orders.fkCols.has('user_id')).toBe(true);
    expect(m.edges).toHaveLength(1);
    expect(m.edges[0]).toMatchObject({ from: 'orders', to: 'users', fromCols: ['user_id'], toCols: ['id'], onDelete: 'cascade' });
  });

  it('marks cross-collection FK target with schema-qualified name and no node', () => {
    const tables = [{ name: 'a', schema: 'main', columns: [col('id', { is_primary_key: true }), col('b_id')] }];
    const fks: Record<string, ForeignKeyInfo[]> = {
      a: [
        {
          name: 'fk',
          columns: ['b_id'],
          referenced_schema: 'other',
          referenced_table: 'b',
          referenced_columns: ['id'],
        },
      ],
    };
    const m = buildERModel(tables, fks);
    expect(m.nodes).toHaveLength(1);
    expect(m.edges[0].to).toBe('other.b');
  });
});

describe('layoutER', () => {
  it('is deterministic and boxes do not overlap', () => {
    const tables = Array.from({ length: 4 }, (_, i) => ({
      name: `t${i}`,
      schema: 'main',
      columns: [col('id', { is_primary_key: true }), col('v')],
    }));
    const m = buildERModel(tables, {});
    const l1 = layoutER(m);
    const l2 = layoutER(m);
    expect(l1.nodes).toEqual(l2.nodes);
    // 2×2 网格：任意两盒不相交
    for (let i = 0; i < l1.nodes.length; i++) {
      for (let j = i + 1; j < l1.nodes.length; j++) {
        const a = l1.nodes[i];
        const b = l1.nodes[j];
        const overlapX = a.x < b.x + b.w && b.x < a.x + a.w;
        const overlapY = a.y < b.y + b.h && b.y < a.y + a.h;
        expect(overlapX && overlapY).toBe(false);
      }
    }
    // 画布尺寸可容纳所有盒
    for (const p of l1.nodes) {
      expect(p.x + p.w).toBeLessThanOrEqual(l1.width);
      expect(p.y + p.h).toBeLessThanOrEqual(l1.height);
    }
  });

  it('edges connect the two boxes with an elbow of 2-3 points', () => {
    const tables = [
      { name: 'child', schema: 'main', columns: [col('id'), col('parent_id')] },
      { name: 'parent', schema: 'main', columns: [col('id', { is_primary_key: true })] },
    ];
    const fks: Record<string, ForeignKeyInfo[]> = {
      child: [
        {
          name: 'fk',
          columns: ['parent_id'],
          referenced_schema: 'main',
          referenced_table: 'parent',
          referenced_columns: ['id'],
        },
      ],
    };
    const l = layoutER(buildERModel(tables, fks));
    expect(l.edges).toHaveLength(1);
    const pts = l.edges[0].points;
    expect(pts.length).toBeGreaterThanOrEqual(2);
    expect(pts.length).toBeLessThanOrEqual(3);
    // 首点在 child 盒边界上，末点在 parent 盒边界上
    const child = l.nodes.find((n) => n.node.table === 'child')!;
    const parent = l.nodes.find((n) => n.node.table === 'parent')!;
    expect(onBoundary(pts[0], child)).toBe(true);
    expect(onBoundary(pts[pts.length - 1], parent)).toBe(true);
  });

  it('empty model yields empty layout', () => {
    const l = layoutER({ nodes: [], edges: [] });
    expect(l.nodes).toEqual([]);
    expect(l.width).toBe(0);
  });

  it('nodeHeight caps at MAX_LISTED_COLS', () => {
    const capped = nodeHeight(500);
    expect(capped).toBe(nodeHeight(18));
    expect(nodeHeight(18)).toBeGreaterThan(nodeHeight(2));
  });
});

function onBoundary(p: { x: number; y: number }, box: { x: number; y: number; w: number; h: number }): boolean {
  const eps = 0.5;
  const onLeft = Math.abs(p.x - box.x) < eps && p.y >= box.y && p.y <= box.y + box.h;
  const onRight = Math.abs(p.x - (box.x + box.w)) < eps && p.y >= box.y && p.y <= box.y + box.h;
  const onTop = Math.abs(p.y - box.y) < eps && p.x >= box.x && p.x <= box.x + box.w;
  const onBottom = Math.abs(p.y - (box.y + box.h)) < eps && p.x >= box.x && p.x <= box.x + box.w;
  return onLeft || onRight || onTop || onBottom;
}
