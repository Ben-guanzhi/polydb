import type { DatabaseKind } from '../api';

export function Icon({ children, size = 14, className = '' }: { children: React.ReactNode; size?: number; className?: string }) {
  return (
    <span className={`icon ${className}`} style={{ width: size, height: size }}>
      <svg viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg" width="100%" height="100%">{children}</svg>
    </span>
  );
}

// DatabaseKind → 品牌配色（简化的 logo）
export function DbIcon({ kind, size = 14 }: { kind: DatabaseKind; size?: number }) {
  switch (kind) {
    case 'sqlite':
      return (
        <Icon size={size}>
          <path d="M3 3h10v2H3zM3 8h10v2H3zM3 13h10v0H3z" fill="#003b57" />
          <circle cx="12" cy="4" r="1" fill="#fff" />
          <circle cx="12" cy="9" r="1" fill="#fff" />
        </Icon>
      );
    case 'mysql':
      return (
        <Icon size={size}>
          <path d="M2 13c1-4 4-8 8-9-2 3-4 6-3 9H2z" fill="#00758f" />
          <path d="M10 4c3 1 5 3 5 6 0 2-1 3-3 3h-2l1-2c1-1 1-2-1-3-1 0-2 1-3 3" stroke="#00758f" strokeWidth="1.2" fill="none" />
        </Icon>
      );
    case 'postgres':
      return (
        <Icon size={size}>
          <ellipse cx="8" cy="8" rx="6" ry="5" fill="#336791" />
          <path d="M4 8c1 2 3 3 4 3s3-1 4-3" stroke="#fff" strokeWidth="1" fill="none" />
          <circle cx="6" cy="7" r="0.6" fill="#fff" />
          <circle cx="10" cy="7" r="0.6" fill="#fff" />
        </Icon>
      );
    case 'mssql':
      return (
        <Icon size={size}>
          <path d="M2 4c2 1 4 1 6 0v3c-2 1-4 1-6 0z" fill="#cc2927" />
          <path d="M10 4c1-0.5 2-0.5 3 0v3c-1 0.5-2 0.5-3 0z" fill="#cc2927" />
          <path d="M2 8c2 1 4 1 6 0v3c-2 1-4 1-6 0z" fill="#cc2927" />
          <path d="M10 8c1-0.5 2-0.5 3 0v3c-1 0.5-2 0.5-3 0z" fill="#cc2927" />
        </Icon>
      );
    case 'oracle':
      return (
        <Icon size={size}>
          <ellipse cx="8" cy="8" rx="6" ry="2.5" fill="#c74634" />
          <text x="8" y="9.2" textAnchor="middle" fontSize="3" fill="#fff" fontWeight="700" fontFamily="sans-serif">DB</text>
        </Icon>
      );
    case 'redis':
      return (
        <Icon size={size}>
          <path d="M2 5l4-2 4 2 4-2v8l-4 2-4-2-4 2z" fill="#dc382d" />
          <path d="M6 3v10M10 5v10" stroke="#fff" strokeWidth="0.6" fill="none" />
        </Icon>
      );
    default:
      return <Icon size={size}><rect x="2" y="3" width="12" height="10" rx="1" fill="#6b7280" /></Icon>;
  }
}

export function TableIcon() {
  return (
    <Icon>
      <rect x="2" y="3" width="12" height="10" rx="1" fill="none" stroke="#6b7280" strokeWidth="1.2" />
      <path d="M2 6h12M6 6v7M10 6v7" stroke="#6b7280" strokeWidth="1" />
    </Icon>
  );
}

export function ViewIcon() {
  return (
    <Icon>
      <path d="M2 8s2.5-4 6-4 6 4 6 4-2.5 4-6 4-6-4-6-4z" fill="none" stroke="#6b7280" strokeWidth="1.2" />
      <circle cx="8" cy="8" r="1.5" fill="#6b7280" />
    </Icon>
  );
}

export function PlayIcon() {
  return (
    <Icon>
      <path d="M4 3l8 5-8 5z" fill="currentColor" />
    </Icon>
  );
}

export function StopIcon() {
  return (
    <Icon>
      <rect x="3" y="3" width="10" height="10" rx="1" fill="currentColor" />
    </Icon>
  );
}

export function RefreshIcon() {
  return (
    <Icon>
      <path d="M3 8a5 5 0 0 1 9-3M13 8a5 5 0 0 1-9 3" stroke="currentColor" strokeWidth="1.4" fill="none" strokeLinecap="round" />
      <path d="M12 3v3h-3M4 13v-3h3" stroke="currentColor" strokeWidth="1.4" fill="none" strokeLinecap="round" />
    </Icon>
  );
}

export function PlusIcon() {
  return (
    <Icon>
      <path d="M8 3v10M3 8h10" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
    </Icon>
  );
}

export function TrashIcon() {
  return (
    <Icon>
      <path d="M3 4h10M6 4V3h4v1M4 4l1 10h6l1-10" stroke="currentColor" strokeWidth="1.2" fill="none" />
    </Icon>
  );
}

export function PencilIcon() {
  return (
    <Icon>
      <path d="M2 14l2-6 6-6 4 4-6 6-6 2z" stroke="currentColor" strokeWidth="1.2" fill="none" />
    </Icon>
  );
}

export function SearchIcon() {
  return (
    <Icon>
      <circle cx="7" cy="7" r="4" stroke="currentColor" strokeWidth="1.4" fill="none" />
      <path d="M10 10l3 3" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </Icon>
  );
}

export function ChevronIcon({ dir = 'right' }: { dir?: 'right' | 'down' }) {
  return (
    <Icon>
      <path
        d={dir === 'down' ? 'M4 6l4 4 4-4' : 'M6 4l4 4-4 4'}
        stroke="currentColor"
        strokeWidth="1.4"
        fill="none"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </Icon>
  );
}

export function SchemaIcon() {
  return (
    <Icon>
      <rect x="2" y="2" width="5" height="5" rx="1" stroke="currentColor" strokeWidth="1.2" fill="none" />
      <rect x="9" y="2" width="5" height="5" rx="1" stroke="currentColor" strokeWidth="1.2" fill="none" />
      <rect x="2" y="9" width="5" height="5" rx="1" stroke="currentColor" strokeWidth="1.2" fill="none" />
      <rect x="9" y="9" width="5" height="5" rx="1" stroke="currentColor" strokeWidth="1.2" fill="none" />
    </Icon>
  );
}

export function QueryIcon() {
  return (
    <Icon>
      <path d="M3 5h10M3 8h10M3 11h6" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </Icon>
  );
}
