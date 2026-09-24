// W1（阶段 C）结果区三个视图面板：JSON、消息、行详情尾板。
// 只做呈现，状态与数据派生留在 QueryWorkspace；纯逻辑在 lib/resultViews.ts。
// 许可红线：仅参考 TablePro(AGPLv3) 的交互语义，实现全部自研，未移植其代码。
import type { CSSProperties } from 'react';
import { INSPECTOR_VALUE_MAX, type InspectorField, type StatementMessage } from '../lib/resultViews';

const mono = 'monospace';
const panelBox: CSSProperties = {
  margin: 0,
  padding: 8,
  fontFamily: mono,
  fontSize: 12,
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
  background: 'var(--bg-elev, var(--panel))',
  border: '1px solid var(--border)',
  borderRadius: 3,
};

export function JsonResultView(props: {
  text: string;
  objectCount: number;
  visibleColCount: number;
  totalColCount: number;
  onCopy: () => void;
  onDownload: () => void;
}) {
  return (
    <div style={{ padding: '6px 10px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4 }}>
        <span style={{ fontSize: 11, color: 'var(--muted)' }}>
          {props.objectCount} 个对象 · {props.visibleColCount}/{props.totalColCount} 列（跟随当前排序、过滤与隐藏列）
        </span>
        <span style={{ flex: 1 }} />
        <button className="btn-ico" onClick={props.onCopy} title="复制 JSON 文本">复制 JSON</button>
        <button className="btn-ico" onClick={props.onDownload} title="下载 JSON 文件">下载</button>
      </div>
      <pre style={panelBox}>{props.text}</pre>
    </div>
  );
}

function messageColor(status: StatementMessage['status']): string {
  if (status === 'err') return 'var(--danger, #c0392b)';
  if (status === 'cancelled') return 'var(--warning, #e67e22)';
  return 'var(--fg)';
}

export function MessagesView(props: { items: StatementMessage[]; pickable: boolean; onPick: (idx: number) => void }) {
  return (
    <div style={{ padding: '6px 10px', display: 'flex', flexDirection: 'column', gap: 4 }}>
      {props.items.length === 0 && <div className="empty">无执行记录。</div>}
      {props.items.map((m, i) => (
        <div
          key={i}
          onClick={() => { if (props.pickable) props.onPick(i); }}
          title={props.pickable ? '点击切到该语句的结果' : undefined}
          style={{
            border: `1px solid ${props.pickable && m.active ? 'var(--accent)' : 'var(--border)'}`,
            borderRadius: 3,
            padding: '4px 8px',
            background: props.pickable && m.active ? 'var(--accent-dim, rgba(0,0,0,0.05))' : 'var(--bg)',
            cursor: props.pickable ? 'pointer' : 'default',
          }}
        >
          <div style={{ fontSize: 11, color: 'var(--muted)', fontFamily: mono, marginBottom: 2 }}>{m.title}</div>
          <pre style={{ ...panelBox, border: 'none', background: 'transparent', padding: 0, color: messageColor(m.status) }}>
            {m.lines.join('\n')}
          </pre>
          {m.sql && (
            <div style={{ fontSize: 11, color: 'var(--muted)', fontFamily: mono, marginTop: 2, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {m.sql.split('\n')[0].slice(0, 160)}
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

export function RowInspector(props: {
  rowIdx: number;
  fields: InspectorField[];
  /** 该字段是否可在网格内联编辑（只读连接、主键列、不可识别行均为 false） */
  editable: (col: number) => boolean;
  onPreview: (col: number) => void;
  onEdit: (col: number) => void;
  onCopyRow: () => void;
  onClose: () => void;
}) {
  return (
    <div style={{ borderTop: '1px solid var(--border)', background: 'var(--bg-alt, transparent)', padding: '6px 10px 8px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
        <strong style={{ fontSize: 12, color: 'var(--muted)' }}>行详情 · 第 {props.rowIdx + 1} 行</strong>
        <span style={{ fontSize: 11, color: 'var(--muted)' }}>{props.fields.length} 字段</span>
        <span style={{ flex: 1 }} />
        <button className="btn-ico" onClick={props.onCopyRow} title="复制该行 JSON（仅可见列）">复制行 JSON</button>
        <button className="btn-ico" onClick={props.onClose} title="收起尾板（选中一行时自动展开）">×</button>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column' }}>
        {props.fields.map((f) => (
          <div key={f.col} style={{ display: 'flex', alignItems: 'baseline', gap: 8, padding: '3px 0', borderBottom: '1px solid var(--border)' }}>
            <div style={{ width: 200, flexShrink: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={`${f.name} · ${f.type}`}>
              <span style={{ fontSize: 11, fontWeight: 700 }}>{f.name}</span>
              <span className="type-badge" style={{ marginLeft: 4 }}>{f.type}</span>
            </div>
            <div style={{
              flex: 1,
              minWidth: 0,
              fontFamily: mono,
              fontSize: 12,
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-all',
              color: f.isNull ? 'var(--muted)' : 'var(--fg)',
            }}>
              {f.truncated ? `${f.display.slice(0, INSPECTOR_VALUE_MAX)}…` : f.display}
            </div>
            <div style={{ display: 'flex', gap: 2, flexShrink: 0 }}>
              <button
                className="btn-ico"
                onClick={() => props.onPreview(f.col)}
                title={f.truncated ? '值已裁断，点击查看完整值' : '查看完整值（JSON 自动美化）'}
              >
                完整值
              </button>
              {props.editable(f.col) && (
                <button className="btn-ico" onClick={() => props.onEdit(f.col)} title="跳到网格内联编辑该字段">
                  编辑
                </button>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
