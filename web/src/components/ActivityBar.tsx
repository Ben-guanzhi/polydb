import type { CSSProperties, ReactNode } from 'react';

export type ViewId = 'explorer' | 'schema' | 'log';

export interface ActivityItem {
  id: ViewId;
  label: string;
  icon: ReactNode;
  hotkey?: string;
  active: boolean;
  badge?: number | string;
}

interface Props {
  items: ActivityItem[];
  onSelect: (id: ViewId) => void;
}

const btnBase: CSSProperties = {
  position: 'relative',
  width: '100%', height: 40,
  display: 'flex', alignItems: 'center', justifyContent: 'center',
  background: 'transparent', border: 'none', cursor: 'pointer',
  color: 'var(--muted)', padding: 0,
  borderLeft: '2px solid transparent',
};

export default function ActivityBar({ items, onSelect }: Props) {
  return (
    <div
      style={{
        width: 48, flexShrink: 0,
        background: 'var(--panel-alt, rgba(0,0,0,0.04))',
        borderRight: '1px solid var(--border)',
        display: 'flex', flexDirection: 'column',
        padding: '6px 0',
      }}
    >
      {items.map((it) => {
        const active = it.active;
        const style: CSSProperties = {
          ...btnBase,
          color: active ? 'var(--fg)' : 'var(--muted)',
          borderLeftColor: active ? 'var(--accent)' : 'transparent',
          background: active ? 'var(--bg-alt, rgba(0,0,0,0.05))' : 'transparent',
        };
        return (
          <button
            key={it.id}
            style={style}
            title={it.hotkey ? `${it.label} (${it.hotkey})` : it.label}
            onClick={() => onSelect(it.id)}
            onMouseEnter={(e) => { if (!active) (e.currentTarget as HTMLElement).style.color = 'var(--fg)'; }}
            onMouseLeave={(e) => { if (!active) (e.currentTarget as HTMLElement).style.color = 'var(--muted)'; }}
          >
            {it.icon}
            {it.badge !== undefined && it.badge !== 0 && (
              <span
                style={{
                  position: 'absolute', right: 4, bottom: 4,
                  fontSize: 9, fontFamily: 'monospace',
                  background: 'var(--accent)', color: '#fff',
                  borderRadius: 8, padding: '0 4px', minWidth: 12, height: 12,
                  display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
                  lineHeight: 1,
                }}
              >
                {typeof it.badge === 'number' && it.badge > 99 ? '99+' : it.badge}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}
