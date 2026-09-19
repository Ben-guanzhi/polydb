# polydb 优化实现方案（参考 TablePro）

> 参考对象：[TablePro](https://github.com/TableProApp/TablePro)（本地源码 `D:\tools\db\TablePro-main`）。
> **许可红线**：TablePro 为 AGPLv3。本方案**只参考其交互设计与行为约定**（下文引用其源码路径仅作设计依据），不移植任何代码。
> 基线：polydb M0–M9 已完成；当前工作区有一批在途变更（Rust server 行为测试、`Dockerfile.server`、`go/pkg/transport/`），**先行提交**作为本方案起点。

---

## 1. 差距矩阵

| TablePro 能力 | polydb 现状 | 结论 |
|---|---|---|
| 表数据浏览器（服务端分页/排序/过滤） | SchemaBrowser 只看结构；"预览数据"= 前端拼 `SELECT * LIMIT 100` 一次性执行（`App.tsx:150`） | **最大缺口，M11** |
| 变更跟踪编辑流（队列→Review SQL→事务提交→行数校验→撤销） | 已有单格内联编辑（`QueryWorkspace.tsx:1327`，PK 生成 UPDATE），但无队列、无预览、无撤销、无行数校验 | **M12** |
| 数据网格（服务端排序、列操作、分页控件、大表估算行数） | 全部客户端实现：点击表头排序、每列过滤、CSV/JSON/TSV 导出（`QueryWorkspace.tsx:1837`） | 随 M11 上移到服务端，前端保留为即时模式 |
| 过滤器（16 操作符 + AND/OR + raw SQL + 预设） | 每列一个客户端 substring 过滤 | **M11**（结构化条件，见 §2） |
| 只读/安全模式（per-connection 三档，执行层强制） | 无 | **M10** |
| 服务端鉴权 | 无（health 外全部裸奔） | **M10**（本方案前置，与 TablePro 无关但必须先做） |
| 导出 CSV/JSON/Markdown/SQL INSERT/IN 子句，复制遵循网格显示 | CSV/JSON/TSV + 右键复制（单元格/行 JSON/CSV）已有；缺 Markdown / INSERT 文件 / IN 子句 | **M13**（纯前端，快速收益） |
| 查询参数、EXPLAIN 可视化、历史全文搜索 | `tabStore.ts` 已有 `params` 字段未用；`lib/explain.ts` 已有基础；历史无搜索 | **M14** |
| 连接分组/标签/颜色、外观主题 | 无分组；仅 Monaco 编辑器主题，无应用级暗色 | **M15** |
| 表结构可视化编辑、ER 图 | DDL 只读展示 | **M16**（后置可选） |
| AI 助手 / MCP server | 无 | **M17**（可选独立） |
| iCloud 同步 / 原生多窗口 / Map 视图 / Chart 高级图表 | — | **不做**（形态不符；polydb 已有图表 PNG 导出） |

---

## 2. 关键设计决策

以下决策来自 TablePro 源码实现（括号内为其文件路径）与 polydb 架构规则的结合：

1. **过滤条件是结构化数据对象，不是 SQL 文本**。TablePro 的模型：操作符枚举 + 列名 + 值（`TableProModels/TableFilter.swift`）。polydb 更进一步：因为存在**网络边界**（Web → server → driver），过滤对象走 msgpack 到服务端，由 **driver 层渲染成参数化 SQL**（值全部走 params 通道，标识符由各驱动按方言引用）——优于 TablePro 的"客户端转义字面量拼接"（`TableProQuery/FilterSQLGenerator.swift`），天然防注入。
2. **编辑 SQL 参数化 + 主键 WHERE**。有 PK 用 PK 原值；`DEFAULT` 关键字与 SQL 函数（`NOW()`）内联不绑定（`ChangeTracking/SQLStatementGenerator.swift`）。polydb 一期**仅 PK 表可编辑**（与现状一致），无 PK 全列匹配（NULL→`IS NULL`，`RowMatchPolicy.swift`）列为后期。
3. **变更队列以行为单位**：`RowChange{rowId, type, cellChanges[], originalRow, sequence}`；同格重复编辑合并保留最初旧值，改回原值自动移除；**sequence 保序**（DELETE→复用值→INSERT 的顺序是 load-bearing）（`ChangeTracking/DataChangeModels.swift`、`PendingChanges.swift`）。
4. **提交 = 单事务 + 逐条影响行数校验**：有 PK 的写只拦 `actual > expected`（MySQL 同值更新报 0 是正常的），无 PK 的写两侧都拦；批量 DELETE 合并为 `WHERE (..) OR (..)` 并按参数上限分块（`DataWrite/DataWriteExecutor.swift`）。polydb 复用现有 `/api/transactions` 端点，服务端零新增；需补一条「rows_affected 准确性」契约测试。
5. **分页用 OFFSET/LIMIT 即可**（TablePro 也没做 keyset）；总数默认用引擎统计估算值，**精确 COUNT 做成用户显式动作**；"显示全部行"要求先精确计数并确认（`Services/Query/TableQueryBuilder.swift`、`ExactRowCounter.swift`）。
6. **只读门禁在执行层强制**，UI 禁用只是第一道：polydb 的 `statement_type` 判定已两端对齐（`spec/behavior.md` §11、`go/pkg/dbcore/detect.go`），app-core 在 `read_only` 连接上对写语句直接返回 `POLYDB_ERR_READ_ONLY`（参考 `DefaultExecutionGate.swift` 的分类拦截思路）。
7. **方言差异留在 driver 层**（AGENTS.md 铁律）：LIMIT 语法、标识符引用、LIKE 转义风格由各 db-* 驱动自己处理，db-core 提供共享的「过滤条件 → 参数化 WHERE 片段」构造器，避免 6 驱动 × 2 语言重复实现（对应 TablePro 的 `SQLDialectDescriptor` 思路，但落在我们的分层里）。
8. **raw SQL 过滤**（用户直接写 WHERE 片段）注入面大：一期**不做**；若后期引入，必须过白名单校验器（参考 `SQLBoundaryValidator.swift`）并在 spec 中标注。

---

## 3. 里程碑

### M10 — 安全基线：服务端鉴权 + 连接级只读 【规模 S-M，前置必须】

**spec 变更（均 non-breaking）**
- `openapi.yaml`：`components.securitySchemes` 增加 `bearerAuth`；说明可选启用。
- `schemas/connection.json`：`ConnectionConfig` 增可选字段 `read_only: bool`（默认 false）。
- `schemas/error.json`：错误码增 `POLYDB_ERR_UNAUTHORIZED`、`POLYDB_ERR_READ_ONLY`。
- `asyncapi.yaml`：hello 消息增可选 `auth: {token}` 字段。
- `behavior.md` 增 §12：鉴权开关语义与只读判定规则。

**双端实现**
- Go/Rust server：`POLYDB_SERVER_TOKEN` 环境变量设置后，`/api/*`（除 health）与 `/ws` 校验 `Authorization: Bearer`；未设置 = 不鉴权（本地开发体验不变）。WS 在 hello 阶段校验。
- Go/Rust app-core：connect 时保存 `read_only`；execute / executeInTransaction / batch / KV 写路径前按 `statement_type` 拦截写语句返回 `POLYDB_ERR_READ_ONLY`。
- 顺带还债：Go `logMiddleware` 空操作接 slog/zerolog（不记密码/token，红线）；Go server 各端点 msgpack 解码宽容度统一。

**Web/TUI**：设置面板与 TUI remote 模式支持填 token。

**契约测试**：无 token→401、错 token→401、对 token→通过；`read_only` 连接执行 INSERT/UPDATE/DDL→409 `POLYDB_ERR_READ_ONLY`、SELECT 正常；两端各一遍。

**验收**：带 token 部署的 server 无法匿名读写；只读连接写操作两端行为一致。

### M11 — 表数据浏览器（服务端行浏览 + 过滤 + 服务端分页排序）【规模 L，核心】

**spec 变更（non-breaking 新增）**
- `schemas/query.json` 增：
  - `FilterCondition { column, op, value?, second_value?, values? }`，`op ∈ eq|ne|lt|le|gt|ge|like|not_like|in|not_in|between|null|not_null`（13 个，TablePro 16 个去掉 escape 细节类，后续可加）。
  - `TableRowsRequest { columns?, conditions?, logic: and|or, order_by?: [{column, dir}], offset, limit }`。
  - `TableRowsResult = QueryResult + has_more + total_estimate?`。
- `openapi.yaml` 增两个端点：
  - `POST /api/connections/{id}/schemas/{schema}/tables/{table}/rows/query`（过滤体结构化，走 msgpack body，故用 POST）。
  - `POST .../rows/count` → `{count}`（精确计数，显式动作）。
- PK 信息**不新增端点**：复用 `list_columns.is_primary_key`（两端已有）。
- `behavior.md` §1 增补：rows 端点分页复用 offset/limit（上限 10000）、无排序时的顺序不承诺稳定（前端翻页保序需带 PK 排序）、非法列名→`POLYDB_ERR_INVALID_PARAM`。

**双端实现（本里程碑最大工作量）**
- `db-core`（Rust）/`dbcore`（Go）：`SqlDriver` 增 `read_rows(schema, table, TableRowsRequest)`；db-core 提供共享构造器：过滤条件→参数化 WHERE 片段（值进 params）、标识符引用 hook、LIMIT 风格 hook。
- 6 驱动 × 2 语言实现：**先 SQLite + PostgreSQL 试点跑通全链路与契约测试，再铺 MySQL/MSSQL/Oracle/Redis 之外的三个 SQL 库**（MSSQL 用 `OFFSET n ROWS FETCH NEXT`、Oracle 分页方言在各自 driver 内消化）。
- server：两个新端点（msgpack），复用现有连接分派与错误映射。

**Web**
- `tabStore.ts` 增第二种 tab 类型 `table`（{connId, schema, table, filter, sort, page}，localStorage 持久化）。
- 新组件 `TableDataView`：服务端分页控件（页大小 50/200/1000，TablePro 默认 200）、点击表头服务端排序、过滤栏（行编辑器：列下拉 + 操作符 + 值；AND/OR 切换；Preview Query 显示生成的 WHERE）、估算总数显示 +「精确计数」按钮。
- SchemaBrowser 双击表 → 打开 table tab（替代现在的拼 SQL 预填）。
- 编辑入口：本视图内单元格编辑挂接 M12 队列（M11 先只读浏览）。

**契约测试**：sqlite + PG：分页（offset/limit/has_more）、排序（asc/desc/多列）、13 操作符逐个、AND/OR、非法列名/操作符错误码、count 端点、MSSQL/Oracle 分页 smoke（容器起得来时）。

**验收**：百万行表可流畅翻页浏览过滤（服务端分页，不整表拉取）；两端响应 wire 级一致。

### M12 — 变更跟踪编辑流 【规模 M-L，前端为主】

**spec/服务端：零新增端点**。仅补一条契约测试：UPDATE/DELETE 返回的 `rows_affected` 准确性（两端）。

**Web 实现（全部前端）**
- `lib/changes.ts`：`RowChange` 队列模型（见 §2-3），per-tab 独立，工具栏显示待处理数。
- Review SQL 面板：按 §2-2 生成参数化语句预览（参数值内联展示），Copy All。
- 提交流程：`begin → executeInTransaction×N → commit`；逐条校验 `rows_affected`（keyed 只拦 > 预期；失败→rollback，队列保留可重试）；成功清队列并刷新。
- 行操作：添加行（INSERT，全默认→`DEFAULT VALUES` 方言差异由前端模板处理）、复制行（PK/自增列置 DEFAULT）、删除行（PK WHERE）。
- 撤销栈 per tab（Ctrl+Z / Shift+Ctrl+Z）；翻页/刷新/关 tab 未保存时弹确认。
- 「恢复上次保存的值」：localStorage 快照（7 天过期），逐行显示恢复计划——TablePro 的 Restore Previous Values 简化版。
- TableDataView 打开编辑开关；QueryWorkspace 现有单格编辑迁移到同一队列（`QueryWorkspace.tsx:1580` 的可编辑判定保留）。

**验收**：多格多处编辑一次事务提交、中途失败整体回滚、队列保留；影响行数不符自动中止。

### M13 — 导出与复制增强 【规模 S，纯前端，可随时插入】

- `lib/exporters.ts` 统一出口：CSV/TSV（从 `QueryWorkspace.tsx:1837` 抽出）/ JSON / NDJSON / **Markdown 表格** / **SQL INSERT（多行批）** / **IN 子句**；文件下载与剪贴板两个 sink。
- 复制/导出遵循网格显示（隐藏列、当前列序）；导出遵循当前过滤与排序（M11 后指服务端条件）。
- 大结果（>10000 行）提示确认。

### M14 — 编辑器与工作台增强 【规模 M】

- **查询参数**：运行含 `:name` / `?` 占位符的 SQL 时弹出参数填充面板（`sqlSplit.countParams` 已有；`tabStore.params` 字段已在）；参数高亮。
- **EXPLAIN 可视化**：前端对 SELECT 生成 `EXPLAIN`（PG：`FORMAT JSON`；MySQL：`EXPLAIN` 树；SQLite：`EXPLAIN QUERY PLAN`）走现有 executeQuery，`lib/explain.ts` 扩展为树形渲染 + 成本/行数高亮。零 spec 变更。
- **历史全文搜索**：QueryLogPanel + 编辑器历史按 SQL 文本/连接/时间过滤。
- Monaco snippets 库、多光标/列选择的快捷键提示进 ShortcutPanel。

### M15 — 连接管理与应用主题 【规模 M】

- **连接分组/标签/颜色**：storage 迁移（connections 表 `ALTER TABLE ADD COLUMN group_name/tags/color`，向后兼容）；spec `ConnectionInfo` 增可选字段（non-breaking）；ConnectionList 分组渲染 + 颜色点 + 折叠；连接复制；连接配置导出/导入（**不含明文密码**，`password_ref` 保留——已有 m8 红线测试兜底）。
- **应用级主题**：`data-theme="light|dark"` + CSS 变量（现有样式已用 `var(--danger)` 等）；与编辑器主题合并为一个外观设置；`app.theme-cycle` 命令升级。

### M16 — 结构编辑器 + ER 图 【规模 L，后置可选】

- 结构编辑：list_columns/indexes/fks → 表单 → 前端按方言生成 `ALTER TABLE` 预览 → 确认执行（走 executeQuery）。先 SQLite + PG。spec 不变。
- ER 图：`list_foreign_keys` 关系渲染（SVG/Canvas，评估 react-flow 依赖——纯 UI 库允许）。只读关系图，不做逆向同步。

### M17 — AI 助手 + MCP server 【规模 M，可选独立】

- **MCP**（先决条件 M10）：Go 侧新增 `cmd/polydb-mcp`（或 server 挂 `/mcp`），把「元数据浏览 + 只读查询」暴露为 MCP tools，复用 token 鉴权与只读连接，供 Cursor / Claude Desktop 直查。
- **AI 面板**：浏览器直连用户自配的 OpenAI 兼容端点（BYOK），**密钥不经过 polydb server**（服务端不持有 LLM key）；生成 SQL → 插入编辑器 / 解释当前查询 / 优化建议。

---

## 4. 横切还债（穿插进行，不占里程碑）

| 项 | 说明 | 时机 |
|---|---|---|
| 提交在途变更 | server tests + Dockerfile.server + go transport 先落库 | 立即 |
| Rust `Transport` trait 补齐 | 增加 KV/事务/批量/取消，GUI 与 server 能力对齐；Go `transport.Client` 同步评估 | M12 后 |
| TUI Redis 模式 | `transport.Client` 加 KV 方法 + TUI KV 视图（M6 在 TUI 闭环） | 与 M11 并行 |
| 协议一致性 | `web/src/api/index.ts` 声称由 tools/genprotocol 生成而 Rust/Go 手工维护——落实三端生成器，或 CI 加「spec ↔ 三端字段一致性」校验脚本 | M11 前（因为 M11 要改 protocol） |
| sshtunnel known_hosts | `go/pkg/sshtunnel/sshtunnel.go:39` TODO(M9) | M15 前任意点 |
| ImportModal 拆分 | 继续 Step3b+（14.8k 行 → 目标 <5k） | 持续并行 |

## 5. 依赖与排序

```
M10（安全） ──→ M11（表数据浏览） ──→ M12（变更跟踪） ──→ M16 / M17（可选）
                 │
                 ├── M13（导出，纯前端，任意点插入）
                 ├── M14（工作台，可与 M15 并行）
                 └── M15（连接/主题）
横切还债穿插；TUI Redis 与 M11 并行（目录不相交）
```

先提交在途变更 → M10 前**先做协议一致性校验**（横切项）→ M10 → M11 → M12。

## 6. 风险

| 风险 | 缓解 |
|---|---|
| 6 驱动 × 2 语言 `read_rows` 工作量最大 | 共享 WHERE 构造器 + 方言 hook；SQLite/PG 试点先行；契约测试兜底 |
| MSSQL/Oracle 分页与排序语法差异大 | 差异封在各自 driver；分页 smoke 进契约测试 |
| 无 PK 表编辑的行匹配歧义 | 一期 PK-only；全列匹配（NULL→IS NULL + 不可比较列排除）作为二期 |
| raw SQL 过滤注入 | 一期不做；二期必须白名单校验 + spec 标注 |
| AGPL 传染 | 只参考行为与交互设计，禁止移植代码；本方案的模型/端点均为独立设计 |
| 服务端无鉴权窗口期 | M10 排最前，完成前文档标注「仅限本机使用」 |

## 7. 验收门禁总则（每个里程碑不变）

1. spec 先行，标注 breaking/non-breaking，三端 protocol 同步（生成器或校验脚本）。
2. `test/contract` 新场景两端全绿；`make contract-test` + 三语言门禁过。
3. 行为变化写入 `spec/behavior.md` 对应章节。
4. 前端改动有 Vitest 覆盖（`npm run check && npm run build`）。
