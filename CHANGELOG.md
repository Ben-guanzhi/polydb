# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **进程外 provider 机制（M18，spec/behavior.md §16）**：第三方数据库驱动以独立进程接入，
  全程不接触凭据——
  - 进程级 driver broker + `ProviderEndpoint` 抽象（会话端点与进程端点同抽象）；
  - 能力校验、`¬ relied_n(γ)` 守卫、完成/取消信号分离（修复正常完成被误报为取消的竞态）；
  - attach 准入门禁（`POLYDB_PROVIDER_ALLOW`，deny-by-default）+ `broker_set_granted`
    线上能力衰减（只收紧不放宽，不 reload）；
  - rolling update（新端点接管 key，旧端点在途归零后自动降级并回执）；
  - 受限回调面（仅回环 + 进程级令牌 + 不透明 handle）：provider 经回调让宿主代执行
    SQL，连接元数据与凭据留在宿主侧；
  - OS 级沙箱：Windows Job Object（KILL_ON_JOB_CLOSE + 内存上限）/ Unix 进程组 +
    RLIMIT_AS 自限 + 父进程存活自检；
  - 多实例服务与负载分担（§16.18，witnessed coeffect / Thm 47）：commutative key
    的 provider 自证可并存，per-key 轮询分派，一端退出其余继续（continuous
    availability）；
  - broker 收敛性穷举（§16.18.4，Thm 80 运行时对应）：net-intent 重放 + 幂等重放
    双判据，全操作序列枚举（含两个验证判据反例的修正记录）；
  - 配置对账（§16.19，Def 81）：`POLYDB_PROVIDER_CONFIG_FILE` 的变更经 notify_d
    分类增量生效（spawn/撤下/守卫推迟），无需重启宿主；
        （修复过程中定位并修复 Rust 侧 wait_loop 持锁阻塞的潜伏死锁——PD-39，§16.19.5）；
  - compensation 的 commutation 在观测等价 ≈ₒ 上经**全枚举验证**重建（340+ 序列，
    双端 parity）；
  - 参考 provider 进程 `cmd/polydb-provider`（stdio JSON-RPC 2.0，与 MCP 同帧格式）。
- 契约测试：provider bridge / policy / process / callback 双端 parity；
  `TestWSQueryRapidCompletionsNeverCancelled` 竞态回归锁。
- 总览文档 `docs/provider.md`。
- Multi-engine database client (SQLite, MySQL, PostgreSQL, SQL Server, Oracle, Redis)
- Rust workspace with 16 crates
- Go implementation with HTTP server
- React + Monaco web frontend
- SSH tunnel support with known_hosts TOFU verification
- AES-256-GCM encrypted keyring (file backend + OS keyring)
- Large BLOB sidecar storage (spec/encoding.md section 5.2)
- Contract tests with 5 real database engines
- Docker deployment
- GPUI desktop GUI

### Changed
- Dialect abstraction: DriverSpec registry pattern for Rust and Go
- DialectCapabilities struct for Web ALTER SQL generation
- Quoter/Escaper interfaces for sqltui backend pattern
- Dialect struct with function-based registry for tableprosql

## [0.1.0] - 2026-10-13

### Added
- Initial release
- Dual implementation (Rust + Go) sharing language-neutral spec/
- MessagePack encoding for data plane
- OpenAPI + AsyncAPI specification
- Schema diff engine (DDL generation + comparison)
- Redis KV driver support
- SSH tunnel integration
- Cross-language contract tests
- Protocol code generation from spec/
