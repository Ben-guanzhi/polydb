// ER 图模型与布局（M16）：纯函数，可单测。
// 数据来源：list_columns + list_foreign_keys（spec 契约，双端一致）。
import type { ColumnInfo, ForeignKeyInfo } from '../api';

export interface ERTableNode {
  /** 表名（schema 内唯一） */
  table: string;
  schema: string;
  /** 列名，按 ordinal_position 排序 */
  columns: string[];
  pk: string[];
  /** 该表列出的外键列 → 目标表（用于在盒子上标记 FK 列） */
  fkCols: Set<string>;
}

export interface EREdge {
  /** 子表（持有 FK 的一方） */
  from: string;
  /** 父表（被引用方）；跨 schema 时带 schema 前缀 */
  to: string;
  fromCols: string[];
  toCols: string[];
  onDelete?: string;
  onUpdate?: string;
}

export interface ERModel {
  nodes: ERTableNode[];
  edges: EREdge[];
}

export interface LayoutNode {
  node: ERTableNode;
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface LayoutEdge {
  edge: EREdge;
  /** 折线路径点（绝对坐标） */
  points: Array<{ x: number; y: number }>;
}

export interface ERLayout {
  nodes: LayoutNode[];
  edges: LayoutEdge[];
  width: number;
  height: number;
}

export const ER_BOX_W = 168;
const ROW_H = 16;
const HEADER_H = 24;
const PADDING = 6;
const GAP_X = 48;
const GAP_Y = 40;
const MARGIN = 16;
/** 单盒最多列出的列数，超出折叠为 "… +N" */
export const MAX_LISTED_COLS = 18;

export function nodeHeight(colCount: number): number {
  const listed = Math.min(colCount, MAX_LISTED_COLS);
  return HEADER_H + PADDING + listed * ROW_H + PADDING;
}

/**
 * 由「表 → 列」与「表 → 外键」构建 ER 模型。
 * - 只显示 `tables` 中的表；FK 指向的表不在集合内时边保留（目标标记为 schema.table 全名），
 *   但不生成目标节点（跨集引用）。
 * - 同一 from→to 的多条 FK 合并为一组边。
 */
export function buildERModel(
  tables: Array<{ name: string; schema: string; columns: ColumnInfo[] }>,
  fks: Record<string, ForeignKeyInfo[]>,
): ERModel {
  const nodes: ERTableNode[] = tables.map((t) => {
    const cols = [...t.columns].sort((a, b) => (a.ordinal_position ?? 0) - (b.ordinal_position ?? 0));
    const fkCols = new Set<string>();
    for (const fk of fks[t.name] ?? []) {
      for (const c of fk.columns) fkCols.add(c);
    }
    return {
      table: t.name,
      schema: t.schema,
      columns: cols.map((c) => c.name),
      pk: cols.filter((c) => c.is_primary_key).map((c) => c.name),
      fkCols,
    };
  });
  const byName = new Map(nodes.map((n) => [n.table, n]));
  const edges: EREdge[] = [];
  for (const n of nodes) {
    for (const fk of fks[n.table] ?? []) {
      const target = byName.has(fk.referenced_table)
        ? fk.referenced_table
        : `${fk.referenced_schema}.${fk.referenced_table}`;
      edges.push({
        from: n.table,
        to: target,
        fromCols: fk.columns,
        toCols: fk.referenced_columns,
        onDelete: fk.on_delete,
        onUpdate: fk.on_update,
      });
    }
  }
  return { nodes, edges };
}

/**
 * 确定性网格布局：按序排入 cols×rows 网格（cols = ceil(sqrt(n))）。
 * 边用「出边中点 → 肘形转折 → 目标锚点」折线；自环（同表）用右侧锚点。
 * 跨集引用（目标表不在集合内）折线终止于画布左上角。
 */
export function layoutER(model: ERModel): ERLayout {
  const n = model.nodes.length;
  const cols = n === 0 ? 1 : Math.ceil(Math.sqrt(n));
  const rowPitch = 260 + GAP_Y;
  const nodes: LayoutNode[] = model.nodes.map((node, i) => ({
    node,
    x: MARGIN + (i % cols) * (ER_BOX_W + GAP_X),
    y: MARGIN + Math.floor(i / cols) * rowPitch,
    w: ER_BOX_W,
    h: nodeHeight(node.columns.length),
  }));
  const posOf = new Map(nodes.map((p) => [p.node.table, p]));

  const edges: LayoutEdge[] = model.edges
    .filter((e) => posOf.has(e.from))
    .map((edge) => {
      const a = posOf.get(edge.from)!;
      const b = posOf.get(edge.to);
      if (b) {
        const end = b === a ? anchorPoint(a, b, true) : anchorPoint(a, b, false);
        return { edge, points: elbow(a, end) };
      }
      // 跨集引用：终点画到画布左上角外侧，提示目标不在本 schema。
      return { edge, points: [{ x: a.x + a.w / 2, y: a.y }, { x: MARGIN, y: MARGIN }] };
    });

  const rows = n === 0 ? 0 : Math.ceil(n / cols);
  if (n === 0) return { nodes, edges, width: 0, height: 0 };
  const width = MARGIN * 2 + cols * ER_BOX_W + (cols - 1) * GAP_X;
  const height = MARGIN * 2 + rows * rowPitch - GAP_Y;
  return { nodes, edges, width, height };
}

/** 两个盒之间最自然的锚点：水平相邻用左右边中点，垂直相邻用上下边中点。 */
function anchorPoint(a: LayoutNode, b: LayoutNode, self: boolean): { x: number; y: number } {
  if (self) return { x: a.x + a.w, y: a.y + a.h / 2 };
  const dx = Math.abs(b.x + b.w / 2 - (a.x + a.w / 2));
  const dy = Math.abs(b.y + b.h / 2 - (a.y + a.h / 2));
  if (dx > dy) {
    const toRight = b.x > a.x;
    return { x: toRight ? b.x : b.x + b.w, y: b.y + b.h / 2 };
  }
  const toBottom = b.y > a.y;
  return { x: b.x + b.w / 2, y: toBottom ? b.y : b.y + b.h };
}

/**
 * 从盒子 a 出发的肘形折线到目标锚点 end：
 * 先出最近边中点，再经过 (end.x, start.y) 或 (start.x, end.y) 转折，最后到 end。
 * 共 3 个点；若 start 与 end 同轴，首点即目标所在边，折线退化为 2 点。
 */
function elbow(a: LayoutNode, end: { x: number; y: number }): Array<{ x: number; y: number }> {
  const dx = Math.abs(end.x - (a.x + a.w / 2));
  const dy = Math.abs(end.y - (a.y + a.h / 2));
  if (dx > dy) {
    const toRight = end.x > a.x + a.w / 2;
    const start = { x: toRight ? a.x + a.w : a.x, y: a.y + a.h / 2 };
    return [start, { x: end.x, y: start.y }, end];
  }
  const toBottom = end.y > a.y + a.h / 2;
  const start = { x: a.x + a.w / 2, y: toBottom ? a.y + a.h : a.y };
  return [start, { x: start.x, y: end.y }, end];
}

