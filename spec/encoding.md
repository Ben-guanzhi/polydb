# PolyDB 物理编码约定

本文档定义各通道使用的序列化格式和 Content-Type。

---

## 1. 格式选择矩阵

| 通道 | 格式 | Content-Type |
|------|------|--------------|
| spec/ 契约定义 | OpenAPI + JSON Schema | — |
| 契约测试 fixture | JSON | `application/json` |
| REST 控制面 | MessagePack | `application/msgpack` |
| REST 数据面（查询结果） | MessagePack（当前实现） | `application/msgpack` |
| WebSocket 控制消息 | MessagePack | `application/msgpack` |
| WebSocket 大结果流 | MessagePack（当前实现） | `application/msgpack` |
| 进程内（GUI/TUI ↔ app-core） | 零序列化（直接传结构体） | — |
| 错误响应 | JSON | `application/json` |
| 调试 / 握手 | JSON | `application/json` |

> **状态说明**：Arrow IPC（`application/vnd.apache.arrow.stream`）与大数据体旁路（§3、§5）均为**预留设计，当前未实现**——REST 与 WebSocket 数据面的实际格式一律 MessagePack。实现 Arrow 后按本文件约定回填。

---

## 2. MessagePack 约定

- 使用 **MessagePack** 作为控制面的主要序列化格式
- 字段名使用 **snake_case**（与 JSON Schema 一致）
- 可选字段缺失时**不编码**（不发送 null）
- 枚举值编码为字符串（不用整数）

### 库选型

| 语言 | 库 |
|------|-----|
| Rust | `rmp-serde` |
| Go | `github.com/vmihailenco/msgpack/v5` |

---

## 3. Arrow IPC 约定（预留，未实现）

> 本节为预留设计。当前 REST / WS 数据面一律 MessagePack（见 §1 状态说明）。以下约定在启用 Arrow 时生效。

- 使用 **Arrow IPC streaming** 格式（非 file 格式）
- 每个查询结果为一个或多个 RecordBatch
- Schema 在 stream 开头发送一次
- 大字段（BLOB / 大 TEXT）使用 Arrow 的 LargeUtf8 / LargeBinary
- 压缩：默认不压缩；可选 `zstd` 压缩（通过请求头 `X-PolyDB-Compression: zstd` 协商）

### 库选型

| 语言 | 库 |
|------|-----|
| Rust | `arrow-rs` (arrow crate) |
| Go | `github.com/apache/arrow-go/v18` |

---

## 4. JSON 约定

JSON 仅用于：
1. 错误响应（所有通道统一）
2. 调试模式（请求头 `Accept: application/json`）
3. 握手消息（WebSocket hello/hello_ack 可选 JSON）
4. 契约测试 fixture

### 数值编码
- 整数：JSON number（无引号）
- 浮点：JSON number
- NULL：JSON null
- 大整数（> 2^53）：JSON string（避免精度丢失）
- 日期时间：ISO 8601（`2024-01-15T10:30:00Z`）

---

## 5. 大字段旁路（预留，未实现）

> 本节为预留设计。当前大字段（BLOB/大 TEXT）直接在结果集内联，未做旁路引用。以下约定在实现旁路时生效。

大字段（BLOB / 大 TEXT，阈值 1MB）**不内联**到结果集中。

替代方案：
1. 结果集中大字段位置替换为引用 ID（`$polydb_blob:<uuid>`）
2. 客户端通过 REST 接口获取大字段内容：
   ```
   GET /api/connections/{id}/blobs/{blob_id}
   Range: bytes=0-1048575  (支持 Range 请求)
   ```
3. 响应 Content-Type 为原始类型（`application/octet-stream` 或推断的 MIME）

---

## 6. 压缩

| 场景 | 压缩 | 库（Rust） | 库（Go） |
|------|------|-----------|---------|
| Arrow IPC（预留） | zstd | `zstd` | `github.com/klauspost/compress/zstd` |
| HTTP 响应 | gzip（标准 HTTP） | Axum tower-http | net/http stdlib |
| 存储（SQLite） | 无（WAL 模式） | — | — |

---

## 7. 版本协商

- HTTP：通过 `Accept` 和 `Content-Type` 头
- WebSocket：hello 消息中包含 `client_version`
- 服务端在 hello_ack 中返回 `server_version`
- 版本格式：语义化版本（`MAJOR.MINOR.PATCH`）
- 不兼容变更通过 major version 递增标识
