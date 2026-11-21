import { useCallback, useEffect, useState } from 'react';
import {
  listServerProcesses,
  killServerProcess,
  listServerVariables,
  listServerUsers,
  generateUserDDL,
  generateTestData,
} from '../lib/api';
import type {
  ServerProcess,
  ServerVariable,
  ServerUser,
  UserOpsRequest,
} from '../api';

// OpsPanel（§380 web 半边）：会话 / 服务器变量 / 用户 / 造数四个 tab——
// 数据经 §376 的 6 端点。kill 是危险动作（confirm 先行）；用户 DDL 生成后
// 展示全文（执行仍走查询编辑器——内核 UserAdmin 的分工注释）。

interface Props {
  connId: string;
}

type Tab = 'procs' | 'vars' | 'users' | 'gendata';

interface TableProps<T> {
  rows: T[];
  loading: boolean;
  error: string;
  headers: string[];
  cells: (row: T) => string[];
  renderActions?: (row: T) => React.ReactNode;
}

function OpsTable<T>({ rows, loading, error, headers, cells, renderActions }: TableProps<T>) {
  if (loading) return <div className="p-3 text-xs text-muted">读取中…</div>;
  if (error) return <div className="p-3 text-xs text-danger">{error}</div>;
  if (rows.length === 0) return <div className="p-3 text-xs text-muted">（无数据）</div>;
  return (
    <table className="w-full text-xs" aria-label="运维数据表">
      <thead>
        <tr className="text-left text-muted border-b border-line">
          {headers.map(h => (
            <th key={h} className="px-2 py-1 font-normal">{h}</th>
          ))}
          {renderActions && <th className="px-2 py-1 font-normal">操作</th>}
        </tr>
      </thead>
      <tbody>
        {rows.map((r, i) => (
          <tr key={i} className="border-b border-line-soft hover:bg-canvas">
            {cells(r).map((c, j) => (
              <td key={j} className="px-2 py-1 font-mono">{c}</td>
            ))}
            {renderActions && <td className="px-2 py-1">{renderActions(r)}</td>}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export default function OpsPanel({ connId }: Props) {
  const [tab, setTab] = useState<Tab>('procs');
  const [procs, setProcs] = useState<ServerProcess[]>([]);
  const [vars, setVars] = useState<ServerVariable[]>([]);
  const [users, setUsers] = useState<ServerUser[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  // 造数表单。
  const [genTable, setGenTable] = useState('');
  const [genRows, setGenRows] = useState('10');
  const [genSeed, setGenSeed] = useState('');
  // 用户 DDL 表单。
  const [uAction, setUAction] = useState<UserOpsRequest['action']>('create');
  const [uUser, setUUser] = useState('');
  const [uHost, setUHost] = useState('');
  const [uPass, setUPass] = useState('');
  const [uDb, setUDb] = useState('');
  const [ddl, setDdl] = useState('');

  const reload = useCallback(async () => {
    setLoading(true);
    setError('');
    setNotice('');
    try {
      if (tab === 'procs') setProcs(await listServerProcesses(connId));
      else if (tab === 'vars') setVars(await listServerVariables(connId));
      else if (tab === 'users') setUsers(await listServerUsers(connId));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [tab, connId]);

  useEffect(() => {
    if (tab !== 'gendata') void reload();
  }, [tab, reload]);

  const doKill = async (pid: string) => {
    if (!window.confirm(`终止会话 ${pid}？`)) return;
    try {
      await killServerProcess(connId, pid);
      setNotice(`已终止会话 ${pid}`);
      await reload();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const doGenData = async () => {
    setError('');
    setNotice('');
    try {
      const res = await generateTestData(connId, {
        table: genTable,
        rows: Number(genRows) || 10,
        seed: genSeed ? Number(genSeed) : undefined,
      });
      setNotice(`已生成 ${res.rows} 行到 ${genTable}`);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const doUserDDL = async () => {
    setError('');
    setNotice('');
    setDdl('');
    try {
      const res = await generateUserDDL(connId, {
        action: uAction,
        user: uUser,
        host: uAction === 'grant' ? undefined : uHost || undefined,
        password: uAction === 'create' ? uPass || undefined : undefined,
        grant_db: uAction === 'grant' ? uDb || undefined : undefined,
      });
      setDdl(res.sql);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const tabs: { key: Tab; label: string }[] = [
    { key: 'procs', label: '会话' },
    { key: 'vars', label: '服务器变量' },
    { key: 'users', label: '用户' },
    { key: 'gendata', label: '造数' },
  ];

  return (
    <div className="flex flex-col h-full text-xs" data-testid="ops-panel">
      <div className="flex gap-1 p-2 border-b border-line" role="tablist" aria-label="运维面板">
        {tabs.map(t => (
          <button
            key={t.key}
            role="tab"
            aria-selected={tab === t.key}
            onClick={() => setTab(t.key)}
            className={`px-2.5 py-1 rounded ${tab === t.key ? 'bg-accent text-on-accent' : 'hover:bg-canvas'}`}
          >
            {t.label}
          </button>
        ))}
        {tab !== 'gendata' && (
          <button className="ml-auto px-2 py-1 rounded hover:bg-canvas" onClick={() => void reload()} aria-label="刷新">
            刷新
          </button>
        )}
      </div>

      {notice && <div className="px-3 py-1.5 text-success border-b border-line-soft" role="status">{notice}</div>}
      {error && <div className="px-3 py-1.5 text-danger border-b border-line-soft" role="alert">{error}</div>}

      <div className="flex-1 overflow-auto">
        {tab === 'procs' && (
          <OpsTable
            rows={procs}
            loading={loading}
            error={error}
            headers={['id', 'user', 'db', 'state', 'command', 'time']}
            cells={(p) => [p.id, p.user, p.db ?? '', p.state ?? '', p.command ?? '', p.time ?? '']}
            renderActions={(p) => (
              <button className="text-danger hover:underline" onClick={() => void doKill(p.id)} aria-label={`终止会话 ${p.id}`}>
                终止
              </button>
            )}
          />
        )}
        {tab === 'vars' && (
          <OpsTable
            rows={vars}
            loading={loading}
            error={error}
            headers={['name', 'value', 'default']}
            cells={(v) => [v.name, v.value, v.default ?? '—']}
          />
        )}
        {tab === 'users' && (
          <OpsTable
            rows={users}
            loading={loading}
            error={error}
            headers={['name', 'host']}
            cells={(u) => [u.name, u.host ?? '']}
          />
        )}
        {tab === 'gendata' && (
          <div className="p-3 space-y-2">
            <div className="flex gap-2 items-center">
              <label htmlFor="gen-table" className="text-muted">表名</label>
              <input id="gen-table" value={genTable} onChange={e => setGenTable(e.target.value)}
                className="bg-canvas border border-line-strong rounded px-2 py-1 font-mono" placeholder="orders" />
              <label htmlFor="gen-rows" className="text-muted">行数</label>
              <input id="gen-rows" value={genRows} onChange={e => setGenRows(e.target.value)}
                className="bg-canvas border border-line-strong rounded px-2 py-1 w-20" inputMode="numeric" />
              <label htmlFor="gen-seed" className="text-muted">种子</label>
              <input id="gen-seed" value={genSeed} onChange={e => setGenSeed(e.target.value)}
                className="bg-canvas border border-line-strong rounded px-2 py-1 w-20" inputMode="numeric" placeholder="可选" />
              <button
                disabled={!genTable}
                onClick={() => void doGenData()}
                className="px-2.5 py-1 rounded bg-accent text-on-accent disabled:opacity-40"
                aria-label="生成测试数据"
              >
                生成
              </button>
            </div>
            <p className="text-2xs text-muted">数据经内核 GenerateInserts 生成（确定性种子）并逐条执行；失败即中止并报已执行数。</p>
          </div>
        )}
      </div>

      {tab === 'users' && (
        <div className="border-t border-line p-3 space-y-2" aria-label="用户 DDL 生成">
          <div className="flex gap-2 items-center">
            <select value={uAction} onChange={e => setUAction(e.target.value as UserOpsRequest['action'])}
              aria-label="操作" className="bg-canvas border border-line-strong rounded px-2 py-1">
              <option value="create">create</option>
              <option value="drop">drop</option>
              <option value="grant">grant</option>
            </select>
            <input value={uUser} onChange={e => setUUser(e.target.value)} placeholder="用户名"
              aria-label="用户名" className="bg-canvas border border-line-strong rounded px-2 py-1 font-mono" />
            {uAction !== 'grant' ? (
              <input value={uHost} onChange={e => setUHost(e.target.value)} placeholder="host（可选）"
                aria-label="主机" className="bg-canvas border border-line-strong rounded px-2 py-1 font-mono" />
            ) : (
              <input value={uDb} onChange={e => setUDb(e.target.value)} placeholder="目标库"
                aria-label="目标库" className="bg-canvas border border-line-strong rounded px-2 py-1 font-mono" />
            )}
            {uAction === 'create' && (
              <input type="password" value={uPass} onChange={e => setUPass(e.target.value)} placeholder="密码"
                aria-label="密码" className="bg-canvas border border-line-strong rounded px-2 py-1" />
            )}
            <button disabled={!uUser} onClick={() => void doUserDDL()}
              className="px-2.5 py-1 rounded border border-line-strong hover:bg-canvas disabled:opacity-40"
              aria-label="生成用户 DDL">
              生成 DDL
            </button>
          </div>
          {ddl && (
            <pre className="bg-canvas border border-line rounded p-2 font-mono text-2xs whitespace-pre-wrap" aria-label="生成的 DDL">{ddl}</pre>
          )}
          <p className="text-2xs text-muted">只生成不执行——确认后复制到查询编辑器运行（高破坏性运维与普通操作分离）。</p>
        </div>
      )}
    </div>
  );
}
