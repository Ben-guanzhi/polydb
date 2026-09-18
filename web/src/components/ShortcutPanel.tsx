import type { CSSProperties } from 'react';

interface ShortcutEntry {
  keys: string[];
  desc: string;
}

interface ShortcutGroup {
  title: string;
  items: ShortcutEntry[];
}

const GROUPS: ShortcutGroup[] = [
  {
    title: '全局',
    items: [
      { keys: ['Ctrl', 'K'], desc: '打开命令面板' },
      { keys: ['?'], desc: '打开快捷键面板' },
      { keys: ['Ctrl', ','], desc: '打开设置面板' },
      { keys: ['Ctrl', '1'], desc: '切换到资源管理器' },
      { keys: ['Ctrl', '2'], desc: '切换到库表视图' },
      { keys: ['Ctrl', '3'], desc: '切换到查询日志' },
    ],
  },
  {
    title: '布局',
    items: [
      { keys: ['拖拽侧栏边'], desc: '调整侧栏宽度（200–600px）' },
      { keys: ['拖拽编辑/结果分割线'], desc: '调整编辑器高度（120–800px）' },
      { keys: ['Ctrl', '\\'], desc: '重置侧栏宽度' },
    ],
  },
  {
    title: 'SQL 编辑器',
    items: [
      { keys: ['Ctrl', 'Enter'], desc: '执行当前 SQL（多语句按分号切分逐条执行）' },
      { keys: ['Ctrl', 'Esc'], desc: '取消多语句执行的剩余语句' },
      { keys: ['Ctrl', 'P'], desc: '上一条查询历史' },
      { keys: ['Ctrl', 'N'], desc: '下一条查询历史' },
      { keys: ['Ctrl', 'Shift', 'N'], desc: '新建空白标签' },
      { keys: ['↑', '↓'], desc: '查询历史导航（光标在行首）' },
    ],
  },
  {
    title: '结果表格',
    items: [
      { keys: ['右键'], desc: '打开单元格菜单' },
      { keys: ['双击'], desc: '编辑单元格（有主键时）' },
      { keys: ['Enter'], desc: '提交单元格编辑' },
      { keys: ['Esc'], desc: '取消单元格编辑' },
      { keys: ['Ctrl', '点击行'], desc: '多选行' },
    ],
  },
  {
    title: '事务',
    items: [
      { keys: ['Ctrl', 'Enter'], desc: '提交挂起事务（有挂起事务时优先）' },
      { keys: ['Esc'], desc: '回滚挂起事务（焦点不在输入框时）' },
    ],
  },
  {
    title: '命令面板',
    items: [
      { keys: ['↑', '↓'], desc: '上下选择' },
      { keys: ['Enter'], desc: '执行选中项' },
      { keys: ['Esc'], desc: '关闭面板' },
      { keys: ['Tab'], desc: '循环切换选中' },
    ],
  },
];

function Keys({ parts, style }: { parts: string[]; style?: CSSProperties }) {
  return (
    <span style={{ display: 'inline-flex', gap: 3, alignItems: 'center', flexShrink: 0 }}>
      {parts.map((p, i) => (
        <kbd
          key={i}
          style={{
            fontFamily: 'monospace', fontSize: 11,
            color: 'var(--fg)', background: 'var(--bg-alt, rgba(0,0,0,0.15))',
            border: '1px solid var(--border)', borderRadius: 3,
            padding: '1px 6px', minWidth: 20, textAlign: 'center',
            ...style,
          }}
        >
          {p}
        </kbd>
      ))}
    </span>
  );
}

interface Props {
  open: boolean;
  onClose: () => void;
}

export default function ShortcutPanel({ open, onClose }: Props) {
  if (!open) return null;
  return (
    <div
      style={{
        position: 'fixed', inset: 0, zIndex: 900,
        background: 'rgba(0,0,0,0.35)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        padding: 24,
      }}
      onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }}
    >
      <div
        style={{
          width: '100%', maxWidth: 620, maxHeight: '80vh',
          background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 6,
          boxShadow: '0 8px 32px rgba(0,0,0,0.25)',
          display: 'flex', flexDirection: 'column', overflow: 'hidden',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', padding: '10px 14px', borderBottom: '1px solid var(--border)' }}>
          <span style={{ fontWeight: 600, fontSize: 14 }}>键盘快捷键</span>
          <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--muted)' }}>按 Esc 关闭</span>
        </div>
        <div style={{ flex: 1, overflow: 'auto', padding: 12 }}>
          {GROUPS.map((g) => (
            <div key={g.title} style={{ marginBottom: 14 }}>
              <div style={
                { fontSize: 11, color: 'var(--muted)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 6 } as CSSProperties
              }>{g.title}</div>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr auto', gap: '4px 12px', alignItems: 'center' }}>
                {g.items.map((it, i) => (
                  <div key={i} style={{ display: 'contents' }}>
                    <span style={{ fontSize: 12, color: 'var(--fg)' }}>{it.desc}</span>
                    <Keys parts={it.keys} />
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
