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

> **状态说明**：Arrow IPC（`application/vnd.apache.arrow.stream`，§3）为**预留设计，当前未实现**——REST 与 WebSocket 数据面的实际格式一律 MessagePack。BLOB 值按 §5.1 的 `$blob` 编码内联（≤1MB 含 base64），超过阈值按 §5.2 旁路引用（`$polydb_blob:<uuid>` + Range 取回）。实现 Arrow 后按本文件约定回填。

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

## 5. 大字段（BLOB）编码与旁路

### 5.1 内联 `$blob` 编码（已实现）

BLOB 值在结果集中以 tagged object 内联编码（复用 Value 的 Object 变体，MessagePack / JSON 两通道形状一致）：

```json
{"$blob": {"len": 1024, "base64": "..."}}
```

- `len`：原始字节数（整数），始终存在。
- `base64`：RFC 4648 标准字母表、含 `=` 填充的完整编码；仅当 `len <= 1048576`（1MB）时内联。超过阈值走 §5.2 旁路引用（发 `ref` 不发 `base64`）；未启用旁路存储的进程保持仅 `len` 的占位降级。
- 两端驱动（Rust `blob_value` / Go `BlobValue`）必须产出逐字段一致的结果；契约测试的类型矩阵以 `X'0102'` → `{"$blob":{"len":2,"base64":"AQI="}}` 锁定 parity。
- Web 网格单元格显示 `<blob N bytes>`，"复制单元格"导出 base64 内容；预览面板显示完整 JSON（含 base64），即取回路径。
- TUI 网格单元格同样显示 `<blob N bytes>` 紧凑标签；「完整值」面板（`cellFullText`）保留原始 JSON 供终端选择复制。

### 5.2 旁路引用（已实现）

超过 1MB 的大字段不再只有长度占位：结果集中的位置替换为引用 ID，内容落在数据目录的 blob 存储里，客户端按需取回。

#### 引用编码

与 §5.1 同处 `$blob` tagged object（`len` 恒在，msgpack / JSON 两通道形状一致）：

```json
{"$blob": {"len": 1048577, "ref": "$polydb_blob:<uuid>"}}
```

- `ref` 仅当 `len > 1048576` 且所在进程已启用 blob 存储时出现；`ref` 与 `base64` 互斥，是否内联由阈值单点判定。
- 未启用存储的进程（测试、无数据目录的嵌入场景）回落 §5.1 的仅 `len` 占位，无取回路径。
- 旧客户端忽略 `ref` 字段、显示仍为 `<blob N bytes>`，non-breaking。
- 两端构造点单一：Rust `polydb_db_core::blob_value` / Go `dbcore.BlobValue`；形状知识收口在各自 protocol 层。

#### 取回（REST）

```
GET /api/connections/{id}/blobs/{blob_id}
Range: bytes=0-1048575        （可选，单段字节区间）
```

- `blob_id` = `ref` 去掉 `$polydb_blob:` 前缀后的 UUID；连接 id 按既有约定校验格式（非法 400 `POLYDB_ERR_INVALID_PARAM`）。
- 未知或已过期 → 404 `POLYDB_ERR_BLOB_NOT_FOUND`。
- 成功响应 `Content-Type: application/octet-stream`、`Accept-Ranges: bytes`；进程内（GUI/TUI ↔ app-core）取回走 `GetBlob` / `get_blob` 用例（零序列化，同一存储）。

| Range 请求 | 响应 |
|---|---|
| 无 Range | `200` 全量 |
| `bytes=a-b`（`a < len`，`b` 钳到 `len-1`） | `206` + `Content-Range: bytes a-b/len` |
| `bytes=a-`（`a < len`） | `206` 从 a 到结尾 |
| `bytes=-N`（`N ≥ 1`；N 超过长度取全量） | `206` 末尾 N 字节 |
| `a ≥ len`、`bytes=-0`（不可满足） | `416` + `Content-Range: bytes */len`（体为标准错误 JSON） |
| 多段（含逗号）、`a > b`、语法错误 | 忽略 Range 头，按 `200` 全量 |

#### 删除策略

- 存储位置：数据目录 `blobs/<uuid>`（`POLYDB_DATA_DIR` 约定，与 storage 同源）。
- TTL = 24 小时（按文件 mtime 起算）；清扫时机 = 进程启动（app-core 构造）+ 每次写入后。
- 超 TTL 文件即删：无显式删除接口，取回已删内容按 404 处理；前端对 404 展示「已过期，请重新执行查询」。

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
