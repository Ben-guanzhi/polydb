# SSH 隧道（M8）

统一采用**本地端口转发**（等价 `ssh -L 127.0.0.1:<random>:<target>:<port>`）：
app-core 在 127.0.0.1 随机端口起监听，驱动连接本地端口，数据经 SSH 会话转发到目标
host:port。所有驱动都走 `sql.Open(DSN)` 且无 dialer 钩子，这是唯一能覆盖全部驱动的方案。

## 行为约定（双实现一致）

- 仅非 SQLite 连接支持隧道；SQLite 忽略 `ssh_tunnel` 配置。
- 隧道目标为连接的主机/端口，端口缺省按驱动映射（PG 5432 / MySQL 3306 / MSSQL 1433 /
  Oracle 1521 / Redis 6379）。
- 认证：私钥优先（`private_key_path`，可选口令），否则密码（`password`）。
- SSH 端口缺省 22（配置 0/未填按 22；双端一致）。
- 服务器主机密钥：**known_hosts 首用校验（TOFU，双端一致，见 spec/behavior.md §9）**。
  文件在数据目录 `<data_dir>/known_hosts`（OpenSSH 兼容 `host keytype base64`，
  22 端口裸 host、其余 `[host]:port`）：已知 host key 一致 → 接受；key 变更 →
  拒绝（`POLYDB_ERR_SSH_TUNNEL_FAILED`，MITM 信号）；首次出现 → 追加条目并接受。
  记簿失败不阻断连接。
  - Go：`pkg/sshtunnel/knownhosts.go`（`HostKeyCallback`，`AppCore::SetKnownHostsPath` 注入路径）
  - Rust：`app-core/src/sshtunnel.rs`（`TunnelHandler::check_server_key` + russh
    `check_known_hosts_path` + `record_known_host` 追加）
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
