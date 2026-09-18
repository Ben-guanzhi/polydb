import { useState } from 'react';
import type { ReactNode } from 'react';

interface Props {
  title: string;
  actions?: ReactNode;
  children: ReactNode;
  variant?: 'sm' | 'md' | 'main';
  defaultCollapsed?: boolean;
  className?: string;
}

export default function CollapsiblePane({
  title,
  actions,
  children,
  variant = 'main',
  defaultCollapsed = false,
  className = '',
}: Props) {
  const [collapsed, setCollapsed] = useState(defaultCollapsed);
  const variantClass = variant === 'sm' ? 'pane-sm' : variant === 'md' ? 'pane-md' : 'pane-main';

  const Chevron = ({ dir }: { dir: 'left' | 'right' }) => (
    <svg viewBox="0 0 16 16" width="12" height="12" fill="none" aria-hidden>
      <path
        d={dir === 'left' ? 'M11 4l-4 4 4 4' : 'M5 4l4 4-4 4'}
        stroke="currentColor"
        strokeWidth="1.6"
        fill="none"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );

  return (
    <div className={`pane ${variantClass} ${collapsed ? 'collapsed' : ''} ${className}`}>
      <div
        className="pane-header"
        title={collapsed ? `点击展开 ${title}` : undefined}
        data-collapsed={collapsed ? 'true' : 'false'}
      >
        {collapsed ? (
          <div
            className="pane-collapse-strip"
            onClick={() => setCollapsed(false)}
            role="button"
            aria-label={`展开 ${title}`}
            tabIndex={0}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                setCollapsed(false);
              }
            }}
          >
            <Chevron dir="right" />
            <span className="pane-title">{title}</span>
            <Chevron dir="right" />
          </div>
        ) : (
          <>
            <span className="pane-title">{title}</span>
            <div className="pane-actions" onClick={(e) => e.stopPropagation()}>
              {actions}
              <button
                className="btn-ico"
                title={`收起 ${title}`}
                aria-label={`收起 ${title}`}
                onClick={() => setCollapsed(true)}
              >
                <Chevron dir="left" />
              </button>
            </div>
          </>
        )}
      </div>
      {!collapsed && children}
    </div>
  );
}
