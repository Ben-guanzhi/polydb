# Keyring 机密存储（M8）

密码不落库（红线）：`connections` 表只存 `password_ref`（`polydb:<scope>:<uuid>`），
明文密码/私钥口令在请求处理时**一次性**写入 keyring，随后被清空；`ConnectionInfo` 与任何
响应都绝不包含密码或 ref 之外的内容。连接时由 app-core 通过单独的 repository 查询
`GetPasswordRef` 再取回明文，用于 DSN 注入。

## 后端

| 后端 | 选择方式 | 说明 |
|---|---|---|
| 加密文件 | 默认 | 单文件 AES-256-GCM，双实现共享同一数据目录，格式兼容 |
| OS keyring | `POLYDB_KEYRING=os` | 仅 Go（zalando/go-keyring）；Rust 不支持，启动时 warn 并回退文件后端 |

## 文件格式（`<data_dir>/keyring.bin`）

```
POLYDBK1\n | salt(16) | nonce(12) | AES-256-GCM 密文（含 tag）
```

明文为 JSON `map[string]string`。密钥由 `scrypt(N=32768, r=8, p=1, len=32)` 从
master password 派生；已有文件保留原 salt，新文件在首次写入时生成 salt 并落盘。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `POLYDB_MASTER_PASSWORD` | 空（开发模式） | 文件后端主密码；错误密码启动即报错 |
| `POLYDB_KEYRING` | 文件后端 | `os` 切到 OS 凭据管理器（仅 Go） |
| `POLYDB_DATA_DIR` | APPDATA/HOME + `polydb` | 数据目录（keyring.bin 落此处，与 Go/Rust 共享） |

## 生命周期

- **create/update**：请求带一次性 `password` / `ssh_tunnel.password` /
  `ssh_tunnel.private_key_passphrase` → 写入 keyring → 回填 `*_ref` → 清空明文字段。
  secret 为空则不动已有 ref；update 复用已有 ref（同一把 key 改值）。
- **connect**：`GetPasswordRef` → keyring `Get` → 注入 DSN/驱动参数 → 不落任何明文。
- **错误**：`POLYDB_ERR_KEYRING_FAILED`（不可重试）。
