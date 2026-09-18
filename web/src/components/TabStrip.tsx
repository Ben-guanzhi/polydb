import type { EditorTab } from '../lib/tabStore';

interface Props {
  tabs: EditorTab[];
  activeId: string;
  onSelect: (id: string) => void;
  onClose: (id: string) => void;
  onAdd: () => void;
  onRename: (id: string, title: string) => void;
}

export default function TabStrip({ tabs, activeId, onSelect, onClose, onAdd, onRename }: Props) {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'stretch',
        gap: 2,
        padding: '4px 6px 0',
        background: 'var(--bg-alt, transparent)',
        borderBottom: '1px solid var(--border)',
        overflowX: 'auto',
        overflowY: 'hidden',
      }}
    >
      {tabs.map((t) => {
        const active = t.id === activeId;
        return (
          <div
            key={t.id}
            onClick={() => onSelect(t.id)}
            title={`${t.context ? `· ${t.context.label}` : ''}\n双击重命名 · 右键/点 × 关闭`}
            onDoubleClick={() => {
              const next = window.prompt('重命名标签', t.title);
              if (next && next.trim() && next.trim() !== t.title) onRename(t.id, next.trim().slice(0, 40));
            }}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 6,
              padding: '4px 10px',
              fontSize: 12,
              cursor: 'pointer',
              borderRadius: '4px 4px 0 0',
              background: active ? 'var(--bg)' : 'transparent',
              border: `1px solid ${active ? 'var(--border)' : 'transparent'}`,
              borderBottom: active ? '1px solid var(--bg)' : '1px solid transparent',
              borderRight: active ? '1px solid var(--border)' : '1px solid transparent',
              margin: active ? '-1px 0 0 -1px' : undefined,
              color: active ? 'var(--fg)' : 'var(--muted)',
              fontWeight: active ? 600 : 400,
              whiteSpace: 'nowrap',
              maxWidth: 200,
              minWidth: 80,
            }}
          >
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{t.title}</span>
            {t.context && (
              <span
                title={t.context.label}
                style={{ fontSize: 9, color: 'var(--accent)', background: 'var(--accent-dim)', padding: '0 3px', borderRadius: 2 }}
              >
                ctx
              </span>
            )}
            <span
              onClick={(e) => {
                e.stopPropagation();
                if (tabs.length > 1 || window.confirm('这是最后一个标签，关闭将创建新的空白标签。继续？')) {
                  onClose(t.id);
                }
              }}
              title="关闭标签"
              style={{
                marginLeft: 2,
                padding: '0 3px',
                color: 'var(--muted)',
                borderRadius: 2,
                fontSize: 14,
                lineHeight: 1,
                cursor: 'pointer',
              }}
              onMouseEnter={(e) => (e.currentTarget.style.color = 'var(--danger)')}
              onMouseLeave={(e) => (e.currentTarget.style.color = 'var(--muted)')}
            >
              ×
            </span>
          </div>
        );
      })}
      <button
        className="btn-ico"
        onClick={onAdd}
        title="新建空白标签 (Ctrl+Shift+N)"
        style={{ marginLeft: 4, fontSize: 16, lineHeight: 1, padding: '0 8px' }}
      >
        +
      </button>
    </div>
  );
}
