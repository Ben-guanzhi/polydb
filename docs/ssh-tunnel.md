# SSH 隧道（M8）

统一采用**本地端口转发**（等价 `ssh -L 127.0.0.1:<random>:<target>:<port>`）：
app-core 在 127.0.0.1 随机端口起监听，驱动连接本地端口，数据经 SSH 会话转发到目标
host:port。所有驱动都走 `sql.Open(DSN)` 且无 dialer 钩子，这是唯一能覆盖全部驱动的方案。

## 行为约定（双实现一致）

- 仅非 SQLite 连接支持隧道；SQLite 忽略 `ssh_tunnel` 配置。
- 隧道目标为连接的主机/端口，端口缺省按驱动映射（PG 5432 / MySQL 3306 / MSSQL 1433 /
  Oracle 1521 / Redis 6379）。
- 认证：私钥优先（`private_key_path`，可选口令），否则密码（`password`）。
- 服务器主机密钥：两端都接受任意 key（Go `InsecureIgnoreHostKey`，Rust handler 返回
  `Ok(true)`）；first-use known_hosts 验证留待后续。
- 驱动打开失败时关闭已建立的隧道；`disconnect` 关闭驱动与隧道。
- 错误码：`POLYDB_ERR_SSH_TUNNEL_FAILED`（可重试）；驱动错误 `POLYDB_ERR_CONNECTION_FAILED`（可重试）。

## 实现

| 端 | 库 | 要点 |
|---|---|---|
| Go | `golang.org/x/crypto/ssh` | 每个转发连接 goroutine 双向 copy |
| Rust | `russh 0.55`（feature `ring`） | `tokio` runtime 由 AppCore 持有；forward loop 每个连接 `channel_open_direct_tcpip` + `into_stream()` 双向拷贝；`Handle` 含 UnboundedReceiver 不可 Clone，用 `Arc<Mutex>` 共享 |

## 一次性凭据

SSH 密码与私钥口令与连接密码同走 keyring（scope `ssh` / `ssh-pass`），请求处理完即清空
明文，连接时从 keyring 取回，绝不落库、绝不随响应返回。
