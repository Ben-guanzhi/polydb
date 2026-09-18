import { useEffect, useState, type CSSProperties } from 'react';
import type { SqlTemplate, TemplateContext } from './sqlTemplates';

export interface CustomTemplate {
  id: string;
  label: string;
  group: string;
  keywords: string[];
  body: string;
  requiresTable?: boolean;
}

const STORAGE_KEY = 'polydb.customTemplates.v1';

const DEFAULT_CUSTOM_TEMPLATES: CustomTemplate[] = [
  {
    id: 'custom.sample-exist',
    label: 'EXISTS 子查询',
    group: '自定义',
    keywords: ['exists', 'sample', '示范'],
    body: `SELECT *\nFROM \${table}\nWHERE EXISTS (\n  SELECT 1 FROM audit_log\n  WHERE audit_log.target = \${table}.id\n);\n`,
  },
];

export function loadCustomTemplates(): CustomTemplate[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return [...DEFAULT_CUSTOM_TEMPLATES];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((x) => x && typeof x.id === 'string' && typeof x.body === 'string');
  } catch {
    return [];
  }
}

export function saveCustomTemplates(t: CustomTemplate[]): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(t));
  } catch {
    /* quota or private-mode, ignore */
  }
}

export function generateCustomId(): string {
  return `custom-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

function resolveToken(ctx: TemplateContext, tok: string): string | null {
  switch (tok) {
    case 'table': return ctx.table ?? null;
    case 'schema': return ctx.schema ?? null;
    case 'column': return ctx.column ?? null;
    case 'kind': return ctx.kind ?? null;
    default: return null;
  }
}

export function substituteCustomBody(body: string, ctx: TemplateContext): string {
  return body.replace(/\$\{([a-z_]+)\}/gi, (whole, tok) => {
    const v = resolveToken(ctx, tok.toLowerCase());
    return v ?? whole;
  });
}

export function toSqlTemplate(t: CustomTemplate): SqlTemplate {
  return {
    id: `custom.${t.id}`,
    label: t.label,
    group: t.group || '自定义',
    keywords: [...t.keywords, '自定义', 'custom'],
    requiresTable: t.requiresTable,
    build: (ctx) => {
      const out = substituteCustomBody(t.body, ctx);
      // 若模板仍残留未解析占位符，视为不可用
      if (/\$\{[a-z_]+\}/i.test(out)) return null;
      return out;
    },
  };
}

export function filterCustom(query: string, items: CustomTemplate[]): CustomTemplate[] {
  const q = query.trim().toLowerCase();
  if (!q) return items;
  return items.filter((t) => {
    const hay = [t.label, t.group, ...t.keywords].join(' ').toLowerCase();
    return hay.includes(q);
  });
}

interface CustomTemplatesModalProps {
  items: CustomTemplate[];
  onChange: (next: CustomTemplate[]) => void;
  onClose: () => void;
}

const EMPTY_FORM: Omit<CustomTemplate, 'id'> = {
  label: '',
  group: '自定义',
  keywords: [],
  body: '',
  requiresTable: false,
};

function EditableTemplate({
  value,
  onChange,
  onSave,
  onCancel,
  onDelete,
}: {
  value: Omit<CustomTemplate, 'id'> & { id?: string };
  onChange: (v: typeof value) => void;
  onSave: () => void;
  onCancel: () => void;
  onDelete?: () => void;
}) {
  const [kwText, setKwText] = useState(value.keywords.join(', '));
  const keywordsFromText = kwText.split(',').map((s) => s.trim()).filter(Boolean);
  useEffect(() => setKwText(value.keywords.join(', ')), [value.keywords]);

  const submit = () => {
    if (!value.label.trim()) return;
    if (!value.body.trim()) return;
    onChange({ ...value, label: value.label.trim(), body: value.body.trim(), keywords: keywordsFromText });
    onSave();
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, padding: 8, border: '1px solid var(--border)', borderRadius: 6 }}>
      <div style={{ display: 'flex', gap: 8 }}>
        <label style={{ display: 'flex', flexDirection: 'column', flex: 2, fontSize: 11, color: 'var(--muted)' }}>
          名称
          <input value={value.label} onChange={(e) => onChange({ ...value, label: e.target.value })}
            style={{ marginTop: 2, padding: '4px 6px', fontSize: 12, background: 'var(--input-bg, var(--bg))', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 4 }} />
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', flex: 1, fontSize: 11, color: 'var(--muted)' }}>
          分组
          <input value={value.group} onChange={(e) => onChange({ ...value, group: e.target.value })}
            style={{ marginTop: 2, padding: '4px 6px', fontSize: 12, background: 'var(--input-bg, var(--bg))', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 4 }} />
        </label>
      </div>
      <label style={{ display: 'flex', flexDirection: 'column', fontSize: 11, color: 'var(--muted)' }}>
        关键词（逗号分隔，用于搜索）
        <input value={kwText} onChange={(e) => setKwText(e.target.value)}
          style={{ marginTop: 2, padding: '4px 6px', fontSize: 12, background: 'var(--input-bg, var(--bg))', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 4 }} />
      </label>
      <label style={{ display: 'flex', flexDirection: 'column', fontSize: 11, color: 'var(--muted)' }}>
        SQL 模板体
        <textarea value={value.body} onChange={(e) => onChange({ ...value, body: e.target.value })} rows={8}
          style={{ marginTop: 2, padding: '6px 8px', fontSize: 12, fontFamily: 'ui-monospace, monospace', background: 'var(--input-bg, var(--bg))', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: 4, resize: 'vertical' }} />
      </label>
      <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 11, color: 'var(--muted)' }}>
        <input type="checkbox" checked={!!value.requiresTable} onChange={(e) => onChange({ ...value, requiresTable: e.target.checked })} />
        <span>需要选中表（否则禁用）</span>
        <span style={{ marginLeft: 'auto', fontFamily: 'ui-monospace, monospace' }}>
          占位符：<code>{'${table}'}</code> <code>{'${schema}'}</code> <code>{'${column}'}</code> <code>{'${kind}'}</code>
        </span>
      </label>
      <div style={{ display: 'flex', gap: 6, justifyContent: 'flex-end' }}>
        {onDelete && (
          <button onClick={onDelete} title="删除模板" style={{ marginRight: 'auto', padding: '4px 10px', fontSize: 11, border: '1px solid var(--border)', borderRadius: 4, background: 'transparent', color: 'var(--muted)', cursor: 'pointer' }}>删除</button>
        )}
        <button onClick={onCancel} style={{ padding: '4px 10px', fontSize: 11, border: '1px solid var(--border)', borderRadius: 4, background: 'transparent', color: 'var(--fg)' }}>取消</button>
        <button onClick={submit} disabled={!value.label.trim() || !value.body.trim()} className="primary"
          style={{ padding: '4px 10px', fontSize: 11, borderRadius: 4, opacity: (value.label.trim() && value.body.trim()) ? 1 : 0.5, cursor: (value.label.trim() && value.body.trim()) ? 'pointer' : 'not-allowed' }}>保存</button>
      </div>
    </div>
  );
}

export default function CustomTemplatesModal({ items, onChange, onClose }: CustomTemplatesModalProps) {
  const [editingId, setEditingId] = useState<string | null>(null);
  const [draft, setDraft] = useState<Omit<CustomTemplate, 'id'> & { id?: string }>(EMPTY_FORM);

  const startNew = () => {
    setDraft({ ...EMPTY_FORM });
    setEditingId('__new__');
  };
  const startEdit = (t: CustomTemplate) => {
    setDraft({ ...t });
    setEditingId(t.id);
  };
  const save = () => {
    if (!draft.label.trim() || !draft.body.trim()) return;
    if (editingId === '__new__') {
      const created: CustomTemplate = { ...draft, id: generateCustomId() };
      onChange([...items, created]);
    } else if (editingId) {
      onChange(items.map((t) => t.id === editingId ? ({ ...t, label: draft.label.trim(), group: draft.group, keywords: draft.keywords, body: draft.body.trim(), requiresTable: draft.requiresTable }) : t));
    }
    setEditingId(null);
    setDraft(EMPTY_FORM);
  };
  const remove = (id: string) => {
    onChange(items.filter((t) => t.id !== id));
    if (editingId === id) {
      setEditingId(null);
      setDraft(EMPTY_FORM);
    }
  };
  const close = () => {
    setEditingId(null);
    setDraft(EMPTY_FORM);
    onClose();
  };

  return (
    <div style={
      {
        position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.35)', zIndex: 100,
        display: 'flex', alignItems: 'center', justifyContent: 'center',
      } as CSSProperties
    } onClick={close}>
      <div onClick={(e) => e.stopPropagation()} style={
        {
          width: 'min(720px, 92vw)', maxHeight: '85vh', background: 'var(--bg)',
          border: '1px solid var(--border)', borderRadius: 8, padding: 16,
          display: 'flex', flexDirection: 'column', gap: 10, overflowY: 'auto',
          boxShadow: '0 12px 40px rgba(0,0,0,0.4)',
        } as CSSProperties
      }>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div style={{ fontWeight: 600, fontSize: 14 }}>自定义模板（{items.length}）</div>
          <button onClick={() => { onChange(DEFAULT_CUSTOM_TEMPLATES.slice()); }} title="恢复示例模板"
            style={{ padding: '4px 8px', fontSize: 11, border: '1px solid var(--border)', borderRadius: 4, background: 'transparent', color: 'var(--fg)', cursor: 'pointer' }}>恢复示例</button>
        </div>
        <div style={{ fontSize: 11, color: 'var(--muted)', fontFamily: 'ui-monospace, monospace' }}>
          占位符：<code>{'${table}'}</code> <code>{'${schema}'}</code> <code>{'${column}'}</code> <code>{'${kind}'}</code>
        </div>
        {editingId === null && (
          <button onClick={startNew} className="primary" style={{ padding: '6px 12px', fontSize: 12, borderRadius: 4 }}>＋ 新建模板</button>
        )}
        {editingId && (
          <EditableTemplate
            value={draft}
            onChange={setDraft}
            onSave={save}
            onCancel={() => { setEditingId(null); setDraft(EMPTY_FORM); }}
            onDelete={editingId === '__new__' ? undefined : () => remove(editingId)}
          />
        )}
        {editingId === null && items.length === 0 && (
          <div style={{ padding: 20, color: 'var(--muted)', textAlign: 'center', fontSize: 12 }}>没有自定义模板，点上方按钮创建</div>
        )}
        {editingId === null && items.length > 0 && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            {items.map((t) => (
              <div key={t.id} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 8px', border: '1px solid var(--border)', borderRadius: 4 }}>
                <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 2, overflow: 'hidden' }}>
                  <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                    <span style={{ fontSize: 12, fontWeight: 500 }}>{t.label}</span>
                    <span style={{ fontSize: 10, color: 'var(--muted)' }}>[{t.group || '自定义'}]</span>
                    {t.requiresTable && <span style={{ fontSize: 10, color: 'var(--muted)' }}>(需选表)</span>}
                  </div>
                  <div style={{ fontSize: 11, fontFamily: 'ui-monospace, monospace', color: 'var(--muted)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {t.body.slice(0, 80)}
                  </div>
                </div>
                <button onClick={() => startEdit(t)} title="编辑" style={{ padding: '2px 8px', fontSize: 11, border: '1px solid var(--border)', borderRadius: 4, background: 'transparent', color: 'var(--fg)' }}>编辑</button>
                <button onClick={() => remove(t.id)} title="删除" style={{ padding: '2px 8px', fontSize: 11, border: '1px solid var(--border)', borderRadius: 4, background: 'transparent', color: 'var(--muted)' }}>删除</button>
              </div>
            ))}
          </div>
        )}
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 6, paddingTop: 6, borderTop: '1px solid var(--border)' }}>
          <button onClick={close} className="primary" style={{ padding: '6px 14px', fontSize: 12, borderRadius: 4 }}>关闭</button>
        </div>
      </div>
    </div>
  );
}
