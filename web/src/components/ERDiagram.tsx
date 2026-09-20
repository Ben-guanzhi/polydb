// M16：schema 级 ER 图（只读，数据来自 list_columns + list_foreign_keys）。
// 纯 SVG 渲染，无第三方图形库；布局由 lib/erDiagram 的确定性网格算法给出。
import { useEffect, useMemo, useState } from 'react';
import type { ColumnInfo, ForeignKeyInfo, TableInfo } from '../api';
import * as api from '../lib/api';
import {
  buildERModel,
  layoutER,
  type ERLayout,
  type LayoutNode,
} from '../lib/erDiagram';

const ROW_H = 16;
const HEADER_H = 24;
const MAX_LISTED = 18;

interface Props {
  connId: string;
  schema: string;
  tables: TableInfo[];
  onSelectTable?: (table: string) => void;
}

export default function ERDiagram({ connId, schema, tables, onSelectTable }: Props) {
  const [meta, setMeta] = useState<Record<string, { columns: ColumnInfo[]; fks: ForeignKeyInfo[] }>>({});
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const physical = useMemo(() => tables.filter((t) => t.type === 'table'), [tables]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    setMeta({});
    Promise.all(
      physical.map(async (t) => {
        const [columns, fks] = await Promise.all([
          api.listColumns(connId, schema, t.name),
          api.listForeignKeys(connId, schema, t.name).catch(() => [] as ForeignKeyInfo[]),
        ]);
        return [t.name, { columns, fks }] as const;
      }),
    )
      .then((entries) => {
        if (!cancelled) setMeta(Object.fromEntries(entries));
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [connId, schema, physical]);

  const layout: ERLayout | null = useMemo(() => {
    if (Object.keys(meta).length === 0) return null;
    const model = buildERModel(
      physical.map((t) => ({ name: t.name, schema, columns: meta[t.name]?.columns ?? [] })),
      Object.fromEntries(physical.map((t) => [t.name, meta[t.name]?.fks ?? []])),
    );
    return layoutER(model);
  }, [meta, physical, schema]);

  if (error) return <div className="error-box">{error}</div>;
  if (loading || !layout) return <div className="empty">加载 ER 图…</div>;

  return (
    <div
      style={{
        overflow: 'auto',
        maxHeight: 520,
        border: '1px solid var(--border)',
        borderRadius: 4,
        background: 'var(--bg)',
      }}
    >
      <svg width={layout.width} height={layout.height} style={{ display: 'block' }}>
        <defs>
          <marker id="er-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--muted)" />
          </marker>
        </defs>
        {layout.edges.map((e, i) => (
          <polyline
            key={i}
            points={e.points.map((p) => `${p.x},${p.y}`).join(' ')}
            fill="none"
            stroke="var(--muted)"
            strokeWidth={1}
            strokeOpacity={0.7}
            markerEnd="url(#er-arrow)"
          >
            <title>
              {e.edge.from}({e.edge.fromCols.join(',')}) → {e.edge.to}({e.edge.toCols.join(',')})
              {e.edge.onDelete ? ` [ON DELETE ${e.edge.onDelete.toUpperCase()}]` : ''}
            </title>
          </polyline>
        ))}
        {layout.nodes.map((p) => (
          <TableBox key={p.node.table} node={p} onClick={() => onSelectTable?.(p.node.table)} />
        ))}
      </svg>
    </div>
  );
}

function TableBox({ node, onClick }: { node: LayoutNode; onClick: () => void }) {
  const t = node.node;
  const listed = t.columns.slice(0, MAX_LISTED);
  const more = t.columns.length - listed.length;
  const pkSet = new Set(t.pk);
  return (
    <g
      transform={`translate(${node.x}, ${node.y})`}
      style={{ cursor: 'pointer' }}
      onClick={onClick}
    >
      <rect width={node.w} height={node.h} rx={4} fill="var(--bg-elevated, #111)" stroke="var(--border)" />
      <rect width={node.w} height={HEADER_H} rx={4} fill="rgba(59,130,246,0.12)" />
      <text x={8} y={16} fontSize={12} fontWeight={700} fill="var(--fg)">
        {t.table}
      </text>
      <text x={node.w - 8} y={16} fontSize={10} fill="var(--muted)" textAnchor="end">
        {t.pk.length > 0 ? `PK:${t.pk.length}` : ''}
      </text>
      {listed.map((c, i) => {
        const y = HEADER_H + 4 + i * ROW_H + 10;
        const isPk = pkSet.has(c);
        const isFk = t.fkCols.has(c);
        return (
          <g key={c}>
            <text x={8} y={y} fontSize={11} fill={isPk ? 'var(--accent, #3b82f6)' : 'var(--fg)'} fontWeight={isPk ? 700 : 400}>
              {c}
            </text>
            {isPk && <text x={14} y={y} fontSize={9} fill="var(--accent, #3b82f6)"> PK</text>}
            {isFk && !isPk && <text x={node.w - 26} y={y} fontSize={9} fill="var(--muted)" textAnchor="end">FK</text>}
          </g>
        );
      })}
      {more > 0 && (
        <text x={8} y={HEADER_H + 4 + MAX_LISTED * ROW_H + 10} fontSize={10} fill="var(--muted)">
          {`… +${more}`}
        </text>
      )}
    </g>
  );
}
