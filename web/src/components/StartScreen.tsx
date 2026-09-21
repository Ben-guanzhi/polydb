import type { ConnectionInfo } from '../api';
import { DbIcon } from './Icons';

// U2 启动卡片屏（参考 TablePro 连接选择界面）：分组 + 颜色 + kind 徽章。

interface Props {
  connections: ConnectionInfo[];
  serverOk: boolean;
  onSelect: (c: ConnectionInfo) => void;
  onManage: () => void;
}

export default function StartScreen({ connections, serverOk, onSelect, onManage }: Props) {
  const groups: { label: string; items: ConnectionInfo[] }[] = (() => {
    if (!connections.some((c) => (c.group ?? '').trim())) {
      return [{ label: '', items: connections }];
    }
    const m = new Map<string, ConnectionInfo[]>();
    for (const c of connections) {
      const g = (c.group ?? '').trim() || '未分组';
      const arr = m.get(g);
      if (arr) arr.push(c);
      else m.set(g, [c]);
    }
    return [...m.entries()]
      .map(([label, items]) => ({ label, items }))
      .sort((a, b) => (a.label === '未分组' ? 1 : b.label === '未分组' ? -1 : a.label.localeCompare(b.label)));
  })();

  return (
    <div className="start-screen">
      <h2>PolyDB</h2>
      <div className="muted" style={{ fontSize: 12 }}>
        {serverOk ? '选择一个连接开始浏览与查询' : '后端离线——启动 polydb-server 后即可使用'}
      </div>
      {connections.length === 0 && (
        <div className="muted" style={{ marginTop: 24 }}>
          暂无连接。<button className="link" onClick={onManage}>去创建第一个连接 →</button>
        </div>
      )}
      {groups.map((g) => (
        <div key={g.label || '__flat'}>
          {g.label !== '' && <div className="start-group-label">{g.label}</div>}
          <div className="start-grid">
            {g.items.map((c) => (
              <div key={c.id} className="start-card" onClick={() => onSelect(c)} title={`${c.kind}${c.host ? ` · ${c.host}` : ''}${c.database ? ` · ${c.database}` : ''}`}>
                <span className="bar" style={{ background: c.color ?? 'var(--accent)' }} />
                <div className="title">
                  <DbIcon kind={c.kind} size={16} />
                  <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{c.name}</span>
                  {c.read_only && <span title="只读安全模式" style={{ fontSize: 11 }}>🛡</span>}
                  <span className={`badge ${c.kind}`} style={{ marginLeft: 'auto' }}>{c.kind}</span>
                </div>
                <div className="meta">
                  {[c.host, c.database].filter(Boolean).join(' · ') || (c.kind === 'sqlite' ? '本地文件' : '')}
                </div>
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
