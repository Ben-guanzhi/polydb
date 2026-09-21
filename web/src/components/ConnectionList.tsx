import { useEffect, useState } from 'react';
import type { ConnectionInfo, ConnectionStatus, CreateConnectionRequest, DatabaseKind, UpdateConnectionRequest } from '../api';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import CollapsiblePane from './CollapsiblePane';
import { DbIcon, PencilIcon, PlusIcon, TrashIcon } from './Icons';

const KINDS: DatabaseKind[] = ['sqlite', 'mysql', 'postgres', 'mssql', 'oracle', 'redis'];

interface Props {
  serverOk: boolean;
  selectedId: string | null;
  onSelect: (c: ConnectionInfo | null) => void;
}

type FormState = CreateConnectionRequest & {
  readOnly?: boolean;
  sshEnabled?: boolean;
  sshHost?: string;
  sshPort?: number;
  sshUsername?: string;
  sshPrivateKeyPath?: string;
  sshPassword?: string;
  sshPassphrase?: string;
};

export default function ConnectionList({ serverOk, selectedId, onSelect }: Props) {
  const [conns, setConns] = useState<ConnectionInfo[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [form, setForm] = useState<FormState>({
    name: '',
    kind: 'sqlite',
    host: '',
    database: '',
  });
  const [saving, setSaving] = useState(false);
  const [testResults, setTestResults] = useState<Record<string, ConnectionStatus>>({});
  const [testingId, setTestingId] = useState<string | null>(null);
  // U6.4 连接过滤（名称/分组/类型/库名，模糊子串匹配）
  const [connFilter, setConnFilter] = useState('');

  const load = async () => {
    try {
      setConns(await api.listConnections());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  useEffect(() => {
    if (serverOk) void load();
  }, [serverOk]);

  const blankForm = (): FormState => ({
    name: '',
    kind: 'sqlite',
    host: '',
    database: '',
  });

  const startCreate = () => {
    if (showForm && !editingId) {
      setShowForm(false);
      setForm(blankForm());
      return;
    }
    setEditingId(null);
    setForm(blankForm());
    setShowForm(true);
  };

  const startEdit = (c: ConnectionInfo) => {
    setEditingId(c.id);
    setShowForm(true);
    setForm({
      name: c.name,
      kind: c.kind,
      host: c.host ?? '',
      port: c.port,
      database: c.database ?? '',
      username: c.username ?? '',
      password: '',
      group: c.group,
      color: c.color,
      readOnly: !!c.read_only,
      sshEnabled: !!c.ssh_tunnel,
      sshHost: c.ssh_tunnel?.host,
      sshPort: c.ssh_tunnel?.port,
      sshUsername: c.ssh_tunnel?.username,
      sshPrivateKeyPath: c.ssh_tunnel?.private_key_path,
      sshPassword: '',
      sshPassphrase: '',
    });
  };

  const closeForm = () => {
    setShowForm(false);
    setEditingId(null);
    setForm(blankForm());
  };

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      if (editingId) {
        const req: UpdateConnectionRequest = {
          name: form.name.trim(),
          host: form.host?.trim() || undefined,
          port: form.port || undefined,
          database: form.database?.trim() || undefined,
          username: form.username?.trim() || undefined,
          password: form.password?.trim() || undefined,
          group: form.group?.trim() || undefined,
          color: form.color || undefined,
          read_only: form.readOnly || undefined,
        };
        req.ssh_tunnel = form.sshEnabled
          ? {
              host: (form.sshHost || '').trim(),
              port: form.sshPort || 22,
              username: (form.sshUsername || '').trim(),
              password: form.sshPassword?.trim() || undefined,
              private_key_path: form.sshPrivateKeyPath?.trim() || undefined,
              private_key_passphrase: form.sshPassphrase?.trim() || undefined,
            }
          : undefined;
        await api.updateConnection(editingId, req);
      } else {
        const req: CreateConnectionRequest = {
          name: form.name.trim(),
          kind: form.kind,
          host: form.host?.trim() || undefined,
          port: form.port || undefined,
          database: form.database?.trim() || undefined,
          username: form.username?.trim() || undefined,
          password: form.password?.trim() || undefined,
          group: form.group?.trim() || undefined,
          color: form.color || undefined,
          read_only: form.readOnly || undefined,
        };
        if (form.sshEnabled) {
          req.ssh_tunnel = {
            host: (form.sshHost || '').trim(),
            port: form.sshPort || 22,
            username: (form.sshUsername || '').trim(),
            password: form.sshPassword?.trim() || undefined,
            private_key_path: form.sshPrivateKeyPath?.trim() || undefined,
            private_key_passphrase: form.sshPassphrase?.trim() || undefined,
          };
        }
        const created = await api.createConnection(req);
        onSelect(created);
      }
      closeForm();
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (c: ConnectionInfo) => {
    if (!window.confirm(`删除连接「${c.name}」？`)) return;
    try {
      await api.deleteConnection(c.id);
      if (selectedId === c.id) onSelect(null);
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const test = async (c: ConnectionInfo) => {
    setTestingId(c.id);
    setError(null);
    try {
      const st = await api.testConnection(c.id);
      setTestResults((m) => ({ ...m, [c.id]: st }));
    } catch (e) {
      setError(e instanceof ApiError ? `${e.code}: ${e.message}` : String(e));
    } finally {
      setTestingId(null);
    }
  };

  const set = (k: keyof FormState, v: unknown) => setForm((f) => ({ ...f, [k]: v as never }));

  // M15 分组：任一连接有 group 时按组渲染（「未分组」兜底放最后），否则平铺。
  const q = connFilter.trim().toLowerCase();
  const visible = q
    ? conns.filter((c) => `${c.name} ${c.group ?? ''} ${c.kind} ${c.database ?? ''} ${c.host ?? ''}`.toLowerCase().includes(q))
    : conns;
  const groups: { label: string; items: ConnectionInfo[] }[] = (() => {
    if (!visible.some((c) => (c.group ?? '').trim())) {
      return [{ label: '', items: visible }];
    }
    const m = new Map<string, ConnectionInfo[]>();
    for (const c of visible) {
      const g = (c.group ?? '').trim() || '未分组';
      const arr = m.get(g);
      if (arr) arr.push(c);
      else m.set(g, [c]);
    }
    return [...m.entries()]
      .map(([label, items]) => ({ label, items }))
      .sort((a, b) => (a.label === '未分组' ? 1 : b.label === '未分组' ? -1 : 0));
  })();

  const actions = (
    <button className="primary" onClick={() => (showForm ? closeForm() : startCreate())}>
      {showForm ? '取消' : (<><PlusIcon /> <span>新建</span></>)}
    </button>
  );

  return (
    <CollapsiblePane title="连接" variant="sm" actions={actions}>
      <div className="pane-body">
        {error && <div className="error-box">{error}</div>}
        {showForm && (
          <div style={{ border: '1px solid var(--border)', borderRadius: 8, padding: '0 10px 10px', marginBottom: 10 }}>
            <label>名称</label>
            <input value={form.name} onChange={(e) => set('name', e.target.value)} placeholder="本地开发库" />
            <label>类型</label>
            <select value={form.kind} disabled={editingId !== null} onChange={(e) => set('kind', e.target.value as DatabaseKind)}>
              {KINDS.map((k) => (
                <option key={k} value={k}>{k}</option>
              ))}
            </select>
            <label style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer', marginTop: 8 }}>
              <input
                type="checkbox"
                checked={!!form.readOnly}
                onChange={(e) => set('readOnly', e.target.checked)}
              />
              只读安全模式（服务端拒绝写语句 / KV 写，可随时切换）
            </label>
            <div className="row">
              <div style={{ flex: 2 }}>
                <label>分组（可选）</label>
                <input value={form.group ?? ''} onChange={(e) => set('group', e.target.value)} placeholder="prod / staging / dev" />
              </div>
              <div style={{ flex: 1 }}>
                <label>颜色（可选）</label>
                <input
                  type="color"
                  value={form.color ?? '#2d68c8'}
                  onChange={(e) => set('color', e.target.value)}
                  style={{ height: 28, padding: 0, cursor: 'pointer' }}
                />
              </div>
            </div>
            {form.kind !== 'sqlite' && (
              <>
                <div className="row">
                  <div style={{ flex: 2 }}>
                    <label>主机</label>
                    <input value={form.host} onChange={(e) => set('host', e.target.value)} placeholder="127.0.0.1" />
                  </div>
                  <div style={{ flex: 1 }}>
                    <label>端口</label>
                    <input
                      value={form.port ?? ''}
                      onChange={(e) => set('port', e.target.value === '' ? undefined : Number(e.target.value))}
                      placeholder="5432"
                    />
                  </div>
                </div>
                {form.kind === 'postgres' && (
                  <>
                    <label>数据库</label>
                    <input value={form.database} onChange={(e) => set('database', e.target.value)} placeholder="postgres" />
                  </>
                )}
                {form.kind === 'redis' && (
                  <>
                    <label>数据库编号（db index）</label>
                    <input
                      value={form.database ?? ''}
                      onChange={(e) => set('database', e.target.value === '' ? undefined : e.target.value)}
                      placeholder="0"
                    />
                  </>
                )}
                <label>用户名</label>
                <input value={form.username} onChange={(e) => set('username', e.target.value)} placeholder="postgres" />
                <label>密码</label>
                <input
                  type="password"
                  value={form.password ?? ''}
                  onChange={(e) => set('password', e.target.value)}
                  placeholder="（可选，仅保存到系统 keyring）"
                  autoComplete="new-password"
                />
              </>
            )}
            {form.kind === 'sqlite' && (
              <>
                <label>数据库文件（:memory: 为临时库）</label>
                <input value={form.database} onChange={(e) => set('database', e.target.value)} placeholder=":memory:" />
              </>
            )}
            {form.kind !== 'sqlite' && (
              <div style={{ marginTop: 8 }}>
                <label style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
                  <input
                    type="checkbox"
                    checked={!!form.sshEnabled}
                    onChange={(e) => set('sshEnabled', e.target.checked)}
                  />
                  SSH 隧道（本地端口转发）
                </label>
                {form.sshEnabled && (
                  <div style={{ border: '1px solid var(--border)', borderRadius: 8, padding: '0 8px 8px', marginTop: 6 }}>
                    <div className="row">
                      <div style={{ flex: 2 }}>
                        <label>SSH 主机</label>
                        <input value={form.sshHost ?? ''} onChange={(e) => set('sshHost', e.target.value)} placeholder="jump.example.com" />
                      </div>
                      <div style={{ flex: 1 }}>
                        <label>SSH 端口</label>
                        <input
                          value={form.sshPort ?? ''}
                          onChange={(e) => set('sshPort', e.target.value === '' ? undefined : Number(e.target.value))}
                          placeholder="22"
                        />
                      </div>
                    </div>
                    <label>SSH 用户名</label>
                    <input value={form.sshUsername ?? ''} onChange={(e) => set('sshUsername', e.target.value)} placeholder="ubuntu" />
                    <label>私钥路径</label>
                    <input
                      value={form.sshPrivateKeyPath ?? ''}
                      onChange={(e) => set('sshPrivateKeyPath', e.target.value)}
                      placeholder="~/.ssh/id_ed25519（与密码二选一）"
                    />
                    <label>SSH 密码</label>
                    <input
                      type="password"
                      value={form.sshPassword ?? ''}
                      onChange={(e) => set('sshPassword', e.target.value)}
                      placeholder="（可选）"
                      autoComplete="new-password"
                    />
                    <label>私钥口令</label>
                    <input
                      type="password"
                      value={form.sshPassphrase ?? ''}
                      onChange={(e) => set('sshPassphrase', e.target.value)}
                      placeholder="（可选）"
                      autoComplete="new-password"
                    />
                  </div>
                )}
              </div>
            )}
            <div style={{ marginTop: 10 }}>
              <button className="primary" onClick={() => void save()} disabled={saving || !form.name.trim()}>
                {saving ? (editingId ? '修改中…' : '创建中…') : (editingId ? '保存修改' : '创建')}
              </button>
            </div>
          </div>
        )}
        {conns.length === 0 && <div className="empty">暂无连接，点击「新建」创建。</div>}
        {conns.length > 0 && (
          <div className="search" style={{ marginBottom: 6 }}>
            <input
              value={connFilter}
              onChange={(e) => setConnFilter(e.target.value)}
              placeholder="过滤连接（名称/分组/类型）"
              style={{ fontSize: 12, padding: '3px 8px' }}
            />
          </div>
        )}
        {conns.length > 0 && visible.length === 0 && (
          <div className="muted" style={{ padding: '4px 8px', fontSize: 12 }}>（无匹配连接）</div>
        )}
        {groups.map((g) => (
          <div key={g.label || '__flat'}>
            {g.label !== '' && (
              <div className="muted" style={{ fontSize: 11, textTransform: 'uppercase', letterSpacing: 0.8, padding: '8px 8px 2px' }}>
                {g.label}
              </div>
            )}
            {g.items.map((c) => {
              const st = testResults[c.id];
              return (
                <div key={c.id}>
                  <div
                    className={`conn-row ${selectedId === c.id ? 'active' : ''}`}
                    onClick={() => onSelect(selectedId === c.id ? null : c)}
                  >
                    {c.color ? (
                      <span
                        style={{ width: 8, height: 8, borderRadius: '50%', background: c.color, flexShrink: 0, alignSelf: 'center', marginRight: 2 }}
                        title={c.color}
                      />
                    ) : null}
                    <DbIcon kind={c.kind} size={16} />
                    <div style={{ minWidth: 0, flex: 1 }}>
                      <div className="conn-name">
                        {c.name}
                        {c.read_only && <span title="只读安全模式" style={{ marginLeft: 4, fontSize: 11 }}>🛡</span>}
                      </div>
                      <div className="conn-meta mono">
                        {c.database ? `${c.database} · ` : ''}{c.kind}
                        {st && (
                          <span className={`status-dot ${st.connected ? 'ok' : 'err'}`} style={{ marginLeft: 6 }} />
                        )}
                      </div>
                    </div>
                    <div className="conn-actions">
                      <button className="btn-ico" title="测试连接" disabled={testingId === c.id} onClick={(e) => { e.stopPropagation(); void test(c); }}>
                        {testingId === c.id ? '…' : '测试'}
                      </button>
                      <button className="btn-ico" title="修改" onClick={(e) => { e.stopPropagation(); startEdit(c); }}>
                        <PencilIcon />
                      </button>
                      <button className="btn-ico" title="删除" style={{ color: 'var(--danger)' }} onClick={(e) => { e.stopPropagation(); void remove(c); }}>
                        <TrashIcon />
                      </button>
                    </div>
                  </div>
                  {st && (
                    <div className="muted" style={{ fontSize: 11, padding: '0 8px 6px' }}>
                      {st.connected
                        ? `已连接${st.latency_ms != null ? ` · ${st.latency_ms.toFixed(1)} ms` : ''}${st.server_version ? ` · ${st.server_version}` : ''}`
                        : `连接失败: ${st.error ?? 'unknown'}`}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        ))}
      </div>
    </CollapsiblePane>
  );
}
