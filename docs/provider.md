# 进程外 provider（M18）

polydb 的数据库驱动可以**不进宿主进程**：一个独立程序按异步契约接入宿主，
替客户端执行 SQL，全程**不接触凭据**。这套机制的总契约在
`spec/behavior.md` §16（§16.1–§16.17），wire 契约在 `spec/asyncapi.yaml` 与
`spec/schemas/driver-broker.json`；本文是**地图**——从哪里进去、有哪些运维
开关、语义要点在哪一节。

## 一图流

```
客户端(WS /ws)                    宿主进程                        provider 进程
    │                                │                                │
    │ query{provider_key}            │                                │
    ├───────────────────────────────▶│ 能力校验(§16.8)                 │
    │                                │ reliance 守卫(§16.8)            │
    │◀─── query_started ─────────────┤ provider_exec{handle,sql}       │
    │                                ├──── stdio JSON-RPC ────────────▶│
    │                                │        (§16.12.2)               │
    │                                │◀─── POST /provider/exec ────────┤
    │                                │  (回环+令牌, §16.14) SQL 交回宿主 │
    │                                │──── QueryResult ───────────────▶│
    │◀─── query_result ──────────────┤◀──── provider_result ───────────┤
```

- **会话端点**（kind 1）：一条 WS 会话承载 provider，exec 经该会话往返——
  契约测试的双连接形态（§16.12.1 kind 1）。
- **进程端点**（kind 2）：一个**独立进程**承载 provider，经 stdio JSON-RPC
  往返，SQL 经**仅回环的回调面**交回宿主执行（§16.12.1 kind 2 / §16.14）。

## 两种实现（同一抽象）

两端各有一个 `ProviderEndpoint` 抽象（Rust trait / Go interface），
broker 只依赖它——新增端点类型不改分派逻辑（§16.12.1）：

| | Rust | Go |
|---|---|---|
| 抽象 | `crates/server/src/provider_hub.rs` 的 `ProviderEndpoint` | `pkg/server/provider_endpoint.go` |
| 会话端点 | 同文件 `SessionEndpoint` | 同文件 `SessionEndpoint` |
| 进程端点 | `crates/server/src/provider_process.rs` | `pkg/server/provider_process.go` |
| 桥接 hub | `crates/server/src/provider_hub.rs` | `pkg/server/provider_hub.go` |
| 回调面 | `crates/server/src/provider_callback.rs` | `pkg/server/provider_callback.go` |
| 沙箱 | `crates/server/src/provider_sandbox.rs` | `pkg/server/provider_sandbox*.go` |

## 运维开关（环境变量）

| 变量 | 语义 | 缺省 |
|---|---|---|
| `POLYDB_PROVIDER_ALLOW` | 允许 attach 的 key 白名单（逗号分隔）。**未设置 = 拒绝一切 attach**（§16.10.1） | 未设置（deny） |
| `POLYDB_PROVIDER_PROCS` | 进程端点配置：`key=命令,key2=命令2`。**客户端不能指定命令**——否则 attach 退化成任意命令执行（§16.13.1） | 未设置（无进程端点） |
| `POLYDB_PROVIDER_MEM_MB` | provider 进程内存上限 MiB（Windows Job 限制 / Unix RLIMIT_AS，§16.16.2） | 512 |
| `POLYDB_PROVIDER_PARENT_PID` | 宿主注入给 provider 的父 PID（Unix 上 provider 自检父进程存活，§16.16.1） | —（宿主自动注入） |

两道门禁都要过：`POLYDB_PROVIDER_ALLOW` 决定「允许谁 attach」，
`POLYDB_PROVIDER_PROCS` 决定「有哪些进程」。

## 参考实现与调试

- `go/cmd/polydb-provider`：参考 provider 进程（stdio JSON-RPC；`-no-callback`
  回固定结果，默认经回调执行真实 SQL；`-exit-after N` 模拟崩溃）。
- 契约测试（`test/contract/`）是**行为真相源**：
  - `provider_bridge_test.go` —— 会话端点转发/withhold/rolling update（§16.9）
  - `provider_policy_test.go` —— attach 门禁 + 能力衰减（§16.10）
  - `provider_process_test.go` —— 真实进程转发 + 回调真实 SQL（§16.13/§16.14）
  - `broker_test.go` / `broker_query_guard_test.go` —— broker 语义与守卫（§16.5–§16.8）

## 语义要点（速查）

| 主题 | spec 节 | 要点 |
|---|---|---|
| 注册与撤回 | §16.5 | 注册是 tracked effect；`¬ relied_n(γ)` 守卫：在途 > 0 时撤回被拒 |
| 会话卸载 | §16.8.4 | 连接断开自动撤回本会话登记的 key（只撤自己的，Thm 68） |
| 能力衰减 | §16.10.2 | `broker_set_granted` 只能收紧不能放宽；生效为 neutral（不 reload） |
| rolling update | §16.9.4 | 新端点接管 key，旧端点退役、在途归零后自动降级并回执 |
| 多实例服务 | §16.18 | witnessed key（Thm 47，`-commutative`）：同 key 多实例并存 + per-key 轮询；一端退出其余继续（continuous availability） |
| 配置对账 | §16.19 | `POLYDB_PROVIDER_CONFIG_FILE`：变更经 notify_d 分类增量生效（spawn/撤下/守卫推迟），无需重启。Rust 侧曾有的 wait_loop 持锁阻塞死锁已修复（PD-39，§16.19.5） |
| 完成与取消 | §16.15 | 信号分离——正常完成的查询**不得**被误报为 `query_cancelled` |
| 沙箱 | §16.16 | 生命周期绑定 + 内存上限；**不承诺**文件系统隔离与恶意进程防御 |
| commutation | §16.17 | 补偿在观测等价 ≈ₒ 上经**穷举验证**重建（340+ 序列，双端 parity） |

## 明确不承诺

- 文件系统/网络访问隔离、恶意进程防御（沙箱是 OS 级执行边界，不是 SFI，§16.16.3）
- 跨主机回调（回调面仅回环，§16.14.5）
- 白名单/进程配置热重载（改环境变量需重启，§16.10.3）
- compensation 元理论的数学证明（由 §16.17 的穷举验证替代）
