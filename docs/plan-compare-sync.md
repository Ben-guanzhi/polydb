# polydb Compare & Sync（模式比对与同步）— 里程碑拆解

> 状态：**E1 已落地（2026-09-25，纯函数层双实现 + 共享向量对拍）**；**E2.0 真快照复核已落地（2026-09-25，
> SQLite 真引擎采快照喂 diff，抓到 3 个驱动缺陷并双端修复，见附录 A.9）**；**E2 主体已落地（2026-09-25，
> spec 7 个 DTO + `POST /api/schema/diff` 双端端点 + 契约 SQLite 两文件真跑，见附录 A.10）**；
> **E3.0 SQLite DDL 取证已落地（2026-09-25，附录 A.11 G1–G9）**；**E3 生成器与 `/api/schema/ddl` 双端已落地
> （2026-09-25，附录 A.12）**；**E4 Web 向导已落地（2026-09-26，纯前端零 spec 变更，附录 A.13）**；
> **E5 GUI + TUI 向导与文档已落地（2026-09-26，同样零 spec 变更，附录 A.14）**；
> **E6「证据补齐 + 还债」已收口（2026-09-26，附录 A.15–A.21）**：视图依赖取证 → 执行分派应用环路 →
> TUI 远程端到端 → 采集扇出量化 → GUI 状态机抽纯层（抓到"从不回滚"的双实现分歧）→ 视图可见性按 A 档落地；
> **E6 顺带产出的规则变更只有一处**（§8-8 A 档：视图名进快照 + 两条粗粒度警告），其余全是证据与测试，
> **零 spec 变更**（`check-protocol` 仍 64 个类型双向一致）。
> **E6 之后另开两批 O1/O1.1「规则层复杂度归约」已落地（2026-09-27，附录 A.22 / A.23）**：
> O1 把 Rust `select_entries` 两处 Θ(n²)/Θ(R×P) 收敛到 Θ(n log n)，O1.1 把 Go `SelectEntries` 剩下的那条
> Θ(R×P) 组校验改成查桶 ⇒ 两实现现在是**同一个算法形状**。**行为逐字不变、零 spec 变更**。
> 从 `docs/plan-ui-tablepro-phase3.md` §阶段 D「仍不排期」四件中提出单列。
> 许可红线不变：TablePro / TableProGo 均为 AGPLv3，**只借鉴交互语义，不移植任何代码**。
> 架构红线不变：前端不 import driver；core/protocol 无 IO；driver 无 UI 假设；数据面 msgpack；
> spec/ 是唯一真相源，双实现行为必须一致。

---

## 1. 目标与非目标

**目标（v1）**：在**同一数据库种类**内比较两个 schema（例：PG 的 `public` vs `staging`、
MySQL 的 `app_dev` vs `app_prod`），列出表/列/索引/外键四类对象的差异，生成**按依赖排序**的
迁移 DDL，先预览后应用（默认 dry-run）。

**非目标（v1 明确不做，各自需要独立评估）**：
- 跨种类比对（PG ↔ MySQL）：类型/语义映射不是同一回事，做成"看着能比"比"不能比"更危险。
- **数据行**比对与同步（那是另一套体量：主键匹配、批量 UPSERT、进度与中断）。
- 视图 / 存储过程 / 序列 / 触发器 / 分区 / 权限：`SqlDriver` 根本没有这些元数据方法（见 §2）。
- 冲突三方合并（源↔目标↔本地脚本）。

## 2. 现状盘点（2026-09-25 实测，含"确实不存在"的结论）

| 需要的能力 | 现状 | 缺口 |
| --- | --- | --- |
| 元数据读面 | `SqlDriver` 六方法（schemas/tables/columns/indexes/foreign_keys/create_table_sql），5 个 SQL 驱动 × Rust/Go 两端**全部实现**（`rust/crates/db-core/src/driver.rs:29-35`、`go/pkg/dbcore/dbcore.go:25-30`）；DTO 在 `spec/schemas/metadata.json:6-111` | **方法齐 ≠ 字段填对**：E2.0 真快照复核抓到 SQLite 侧 3 个缺陷（外键合成名不确定、`IndexInfo.primary` 恒 false、`on_update/on_delete` 从未填），已两端修掉并固化测试；其余四方言同类字段**未复核**（见附录 A.9） |
| 跨库类型归一 | `ColumnInfo.generic_type` 在 spec 里**已声明**，注释写着"cross-database comparison"，但**没有任何驱动填值**（`db-postgres/src/lib.rs:425`、`db-oracle:517`、`db-mssql:550` 一律 `None`/省略） | ★ 必须落地或在 diff 层自建映射表 |
| DDL 生成 | **只有 Web 一处**：`web/src/lib/alterSql.ts`（quoteIdent/addColumn/dropColumn/renameColumn/addIndex/dropIndex/addFK/dropFK + `UnsupportedAlterError`）。GUI、TUI **零 DDL 生成**（`ALTER TABLE` 只出现在两端自用 storage 里） | ★ 缺 ALTER COLUMN（类型/可空/默认）、缺 CREATE/DROP TABLE → **E3 已补**：服务端生成器（Rust `schema-diff/src/ddl.rs` + Go `pkg/schemadiff/ddl.go`）**只覆盖 SQLite**，其余方言显式 `NOT_SUPPORTED`（附录 A.12） |
| 原子应用 | 事务四方法两端齐备（`app-core/src/lib.rs:502-579`、`pkg/appcore/appcore.go:338-435`），finalize 只成功一次；`POST /query/batch` 是**逐条循环、不在事务内**（`crates/server/src/lib.rs:663-681`、`go/pkg/server/server.go:441-473`） | "原子应用一批 DDL"只能客户端 begin → N×execute → commit（已有成熟范式：`ui-gui/src/app.rs` 的 `commit_changes`、`TableStructureEditor.tsx:115-135`） |
| DDL 事务能力位 | **全仓库没有任何一处**处理"某方言 DDL 不能在事务里"（MySQL/Oracle 的 DDL 隐式提交） | ★ 需新增能力位 + 显式报错，禁止静默降级（矩阵见 `spec/behavior.md:334-345`）→ **E3 只对 SQLite 立了两位**（`ddl_in_tx=true` 实测、`pragmas_outside_tx` 由脚本自带），其余方言的取值仍空白 |
| 快照结构可复用件 | `web/src/lib/erDiagram.ts:74 buildERModel()` 已是"表→列→FK"的纯结构；GUI `ui-gui/src/app.rs` 的 `load_incoming` 有整 schema FK 扫描（**上限 200 表**，仅"关系"轨道）、TUI `internal/tui/commands.go` 的 `allTablesCmd` 跨 schema 枚举 | ~~表数上限与逐表 fan-out 往返要处理~~ E6.4 已量化：那 200 上限不在比对路径上，比对路径是每表 3 次串行查询、无上限（附录 A.18） |
| Rust 侧远程传输 | **不存在 HttpTransport**（只有 `LocalTransport`，`transport/src/lib.rs:103`）。Go 侧 Local + Remote 都有 | ★ 决定 GUI 形态（见 §4 决策 D3） |

**结论**：读面齐、归一缺、生成半齐（只在 Web）、应用通道齐但能力位空白、跨进程比较在 GUI 侧结构性受限。

## 3. 规模预估（用于排期，不是承诺）

| 批次 | 净新增（约） | 说明 |
| --- | --- | --- |
| E1 diff 纯函数 + 归一层 | ✅ 实际 3641 行：Rust 1675（lib 1010 + normalize 434 + 向量测试 102 + 生成工具 129）、Go 1344、共享向量 16 份 622 行 | 零 IO、零 UI、不生成 DDL；双实现对拍已建立（详见附录 A） |
| E2.0 真快照复核 | ✅ 实际 687 行测试（Rust 368 + Go 319）+ 驱动净改 103 行（Rust +44/−24、Go +59/−16） | 零 spec 变更；抓出 3 个驱动缺陷（附录 A.9 F1–F3） |
| E2 spec DTO + 两端端点 + 契约 | ✅ 实际 spec +187（metadata 71 / openapi 28 / behavior §14 88）、Rust +336/−5、Go +329、契约 +353 | 唯一需要 spec 变更的批次；`/api/schema/ddl` 与 `ddl_in_tx` 推迟到 E3（见 §5） |
| E3.0 SQLite DDL 取证 | ✅ 实际 0 行产品代码（纯探测）+ 附录 A.11 G1–G9 | 测出"重建会级联删子表数据""PRAGMA 在事务内是 no-op"两条数据丢失级事实 |
| E3.1/E3.2 DDL 生成器（纯函数，只 SQLite） | ✅ 实际 3293 行插入：Rust `ddl.rs` 1706 + Go `ddl.go` 863 + Go 单测 697 + lib 接线 | 直改/重建两路 + 依赖排序 + 回滚 + 勾选守卫，全部在语句层，不碰连接 |
| E3.3 共享 DDL 语料 | ✅ 实际 2796 行插入：23 份语料 + 双端加载器（Rust 182 / Go 375） | 跑完整管线 diff→SelectEntries→Generate，逐字段对拍；覆盖度是断言 |
| E3.4–E3.7 协议 + 两端端点 + 契约 | ✅ 实际 1206 行插入：spec 212（metadata 52 / openapi 28 / behavior §15 132）+ 生成 TS 37、Rust 266、Go 283、契约 408 | `/api/schema/ddl` 已进 spec；契约真跑覆盖「应用后收敛 + 回滚复原 + 子表行数不丢」 |
| E4 Web 向导 | ~700 行（四步 + 预览 + 状态持久化） | ✅ 实际 **+1332/−13**（9 个文件）：新建 1138（纯函数 265 + 单测 187 + 组件 686）、5 个代码文件 +96/−4、本文档 +98/−9。比预估超 1.9×，多出来的是执行编排的三种分派与整组勾选补齐；零 spec 变更、零后端改动（附录 A.13） |
| E5 GUI + TUI 收敛形态 + 文档 | GPUI ~400 行、TUI ~350 行 | ✅ 实际 **+4724/−21**（13 个文件；Go 2424 / Rust 2300，代码 7 个提交 + 文档收口提交）：TUI 侧 1496（`compare.go` 终 765 + `commands.go` 137 + 按键测试 496）、Go 呈现层纯函数 677、Go 传输层 251；GUI 侧 1640（`app.rs` 926 + `view.rs` 714）+ `cmpsync.rs` 563 + Rust 传输层 88。比预估多 2.2×，多出来的是**呈现层三套同构纯函数**与按键驱动的端到端测试（预估只算了渲染）。渲染仍无法无头验证（§7，实况见附录 A.14） |
| 合计 | 预估 **约 4.7k 行**（E1–E5）；**E1–E5 实际已 18.8k**（上表 ✅ 行之和） | 超估的原因是共享语料：向量本身即测试资产，一份语料双端跑。E4 是唯一"零后端"批次，仍比预估多 1.9×；E5 同样"零 spec、零后端规则"，但三端各自要一套呈现层纯函数 + 按键测试，超 2.2× |

## 4. 关键设计决策（★ = 需要产品确认的分叉）

**D1 ★ diff 计算放在哪一层 —— 已定（2026-09-25）：服务端一处。**
- **推荐：服务端（app-core 纯函数 + spec DTO + 端点），三端只做渲染。**
  理由量化：500 表 × 2 侧的列/索引/FK 读取，若在前端逐表 fan-out 是 **~3000 次往返**
  （Web 每表 `listColumns`+`listIndexes`+`listForeignKeys`），服务端一次调用即可；
  且 diff 落在 Rust/Go 两端后能被**现有契约测试对拍**（`normalize.go` 那套），
  而三端各自实现要人肉保一致（filters/explain/changes 的三套实现是历史选择，代价已经付过）。
  代价：spec 变更 + 两端端点，比"纯前端接线"重。
- 备选：沿用 `filters.rs`/`explain.rs`/`changes.ts` 惯例，**三端各写一套纯函数**。
  零 spec 变更，但 diff 规则（默认值差异算不算、字符集/排序规则差异、索引列顺序）一旦漂移，
  三端会给出**不同的迁移脚本**——这是数据丢失级别的差异，不适合靠自觉。

**D2 类型归一：落地 `generic_type`，不在 diff 层私藏映射表。**
字段已在 spec 里、注释已写明用途、只是没人填。两端各 5 驱动补一张"驱动原生类型 → 归一名"
映射（约 40 行/驱动），diff 与前端显示共用。这样"比较结果"与"列类型显示"不会互相矛盾。

**D3 GUI 形态必须诚实收窄。**
Rust 无 HttpTransport ⇒ GUI 只能比较**本机 app-core 已打开的两个连接**。方案：GUI 支持
"同种类两 schema"（同库内比，最常见也最安全），跨服务器比较在 GUI 里明确提示
"请在 Web/TUI 使用远程模式"，**不为 GUI 补一套 HttpTransport**（那是独立里程碑量级）。
三端能力差异写进 `docs/frontends.md`。

**D4 应用侧安全默认：dry-run 为默认态。**
- 生成的脚本先完整预览（含被跳过项与原因），应用需显式点击 + **输入目标 schema 名确认**
  （参照导入向导的破坏性操作确认强度）。
- 只读连接（`read_only`）三端一律禁应用；`DROP` 类条目单独分组、默认不勾选。
- 应用走已有事务端点；`ddl_in_tx=false` 的方言（MySQL/Oracle 需实测确认）**不承诺原子性**：
  执行前明确告知"逐条执行、失败不回滚已提交部分"，并生成对应的**回滚脚本**供人工使用。

**D4 的 E4 落地注记（2026-09-26）**：确认强度**换了一次位**——正向应用要求"显式生成脚本 + 显式点击执行"
（脚本全文预览就是确认物，不再额外要求手输名字），**手输名字用在回滚上**（`non_reversible` 语句全量列出 +
输入基准侧连接名才放开按钮）。理由：正向是"把库改成你刚逐条看过的样子"，回滚是"再改回一个你已经
回滚过一次的旧状态"，后者更容易在事后误点。只读连接：客户端不硬禁（真相在服务端 app-core 执行层，
§12.3），向导在选项里标 `· 只读` 并在选中后给醒目告警"服务端会拒绝写语句"。

**D5 diff 输出是"计划"不是"字符串"。—— E1 已按此实现，形状固定为 9 个 op。**
`op`：`create_table` / `drop_table` / `add_column` / `drop_column` / `alter_column` /
`add_index` / `drop_index` / `add_foreign_key` / `drop_foreign_key`；
列的三类变化（类型 / 可空性 / 默认值 / 自增）**合并成一条 `alter_column`**，用 `flags`
数组（`type` / `nullability` / `default` / `auto_increment`）表达改了哪几维——
这样 E3 生成一条 `ALTER TABLE … ALTER …` 还是多条，是方言选择，不必在协议里裂成三个 op。
每条带 `id`（`{表}/{对象}/{op}`，供 UI 记忆勾选）、`phase`、`from`/`to`、`destructive`、
`selected`、中文 `reason`。**不含语句**（语句属 E3）。

## 5. 契约影响（spec/ 真相源，逐条标注）

E2 实际落地（2026-09-25）：

1. `spec/schemas/metadata.json` 新增 **7 个** definitions：`SchemaDiffOp` / `DiffColumnFlag` /
   `SchemaDiffEntry` / `SchemaDiffPlan` / `SchemaDiffTarget` / `SchemaDiffOptions` /
   `SchemaDiffRequest`（+71 行，**non-breaking**，纯新增）。
2. `spec/openapi.yaml` 新增 `POST /api/schema/diff`（`operationId: diffSchema`，tag `schema_diff`，
   +28 行，**non-breaking**）。
3. `spec/behavior.md` 新增 §14（+88 行，7 小节）：快照采集口径（视图跳过、Redis 拒绝）、
   ASCII-only 归一规则、匹配键表、相位表与拓扑、schema 缺省与 SQLite 限制、错误语义两条逐字文案、
   向量语料锁定流程。
4. **刻意未做**（E2.0 的教训直接反哺）：
   - `POST /api/schema/ddl` 与能力矩阵 `ddl_in_tx` 行**推迟到 E3**——E3 的生成器还不存在，
     现在加就是再造一个「字段存在没人填」的死维度。
   - `ColumnInfo.generic_type` 的语义说明**推迟到 E3 / 有实例环境**——四方言目前从未填该字段
     （附录 A.9 F3 同型问题），先写语义会立刻让契约测试变红。

### 5.6 E3 的契约项（✅ 已落地 2026-09-25，non-breaking）

1. `spec/openapi.yaml` 新增 `POST /api/schema/ddl`（计划 + 勾选态 → 有序语句 + 回滚脚本），
   **non-breaking**（新端点，无既有字段变更）。
2. `spec/schemas/metadata.json` 补 `SchemaDdlRequest` / `DdlStatement` / `DdlScript` 三个 DTO。
   线路纪律：`statements` / `rollback` / `entry_ids` **恒为数组**（空时为 `[]` 不得为 `null`），
   `warnings` 空时整体省略，`table` 恒在（PRAGMA 两条为空串）——由语料 d01 与两端服务端测试锁死。
3. `spec/behavior.md` **§15**（132 行）：输入形态、方言范围、直改/重建两路、PRAGMA 包裹与
   `ddl_in_tx` / `pragmas_outside_tx` 两行能力位、勾选两条守卫、回滚镜像规则、不可逆判定、
   8 条告警逐字文案。
4. `ColumnInfo.generic_type` 语义说明**未动**——本轮只生成 SQLite，语句直接渲染驱动原
   `data_type` 文本，与 §8-3 / §8-7 绑定，等有可实测的方言实例再定。

基线：`tools/genprotocol` 的 `check` 已由 **54 → 61（E2）→ 64 spec types（E3）**，
E3 收口时 `check` 与 TS 重生成均零漂移（`web/src/api/index.ts` +37 行）。

## 6. 分批交付（每批独立门禁 + 独立提交）

- **E1 纯函数层** ✅（2026-09-25）：落点由「`app-core/src/schema_diff.rs` + `pkg/appcore/schemadiff.go`」
  改为**独立薄 crate `rust/crates/schema-diff`（package `polydb-schema-diff`）+ Go `go/pkg/schemadiff`**。
  原因：`polydb-app-core` 不依赖 serde/serde_json，塞进去要么污染 app-core 的依赖面、要么让语料无法序列化；
  独立包只依赖 `polydb-core` + `serde`（`serde_json` 仅 dev），零 IO，E2 的 server 端点与将来 CLI 都能直接复用。
  输入两侧元数据快照（`Snapshot` / `TableSnapshot`，字段直接复用 protocol DTO），输出结构化 plan。
  验收：16 份共享向量（`test/schema-diff-vectors/*.json`）喂两端，条目序列逐字段一致（附录 A）。
- **E2.0 真快照复核** ✅（2026-09-25）：先不动 spec，用 SQLite 真引擎采快照喂 E1 的 diff，
  把「驱动是否真按本层假定的语义填字段」变成断言。两端各 10 份镜像测试，抓到并修掉 3 个真缺陷
  （外键合成名、`IndexInfo.primary`、`on_update/on_delete`）+ 2 处 parity（`referenced_schema`、
  PRAGMA `id` 语义）。详见附录 A.9。
- **E2 协议与服务端** ✅（2026-09-25）：spec 改动 → `gen-protocol` → 两端 DTO + 端点 → 契约测试
  （**SQLite 按两个连接/两个文件真跑**——驱动不支持 schema 寻址，"同库两 schema"已定为 v1.1 待办，见 §8-6；
  PG/MySQL/MSSQL/Oracle 与 Redis 按既有 `connected:false` soft-skip）。
  验收门禁全绿，落地实况见附录 A.10。本批只交付 **1 个端点**（`/api/schema/diff`）；
  `/api/schema/ddl` 与 `ddl_in_tx` 能力行推迟到 E3（§5.6）。
- **E3.0 SQLite DDL 取证** ✅（2026-09-25）：不写产品代码，先用两端 bundled 真引擎把
  `DROP COLUMN` 限制、表重建的触发器/级联删除行为、`PRAGMA foreign_keys` 的事务内 no-op、
  `ddl_in_tx` 真值全部测出来（附录 A.11 G1–G9）。
- **E3 语句生成（只 SQLite）** ✅（2026-09-25）：在服务端把计划变成有序脚本（ALTER COLUMN 的类型/可空/默认三态、
  CREATE/DROP TABLE、依赖排序、回滚脚本）。**已定（2026-09-25）三条**：
  ① 只覆盖 SQLite，PG/MySQL/MSSQL/Oracle 显式 `NOT_SUPPORTED`（它们的 `on_update`/`on_delete`
  与 `generic_type` 都还没填，diff 本就比不出差异，先写 5 套语法等于再造"看起来已实现"）；
  ② 回滚进 v1，用"源/目标互换反向生成"复用同一套规则（§8-2）；
  ③ `/api/schema/ddl` 入参回传 `source`/`target`/`options` + `selected` 条目 id，服务端**重算**
  diff——因为 `SchemaDiffEntry` 线路上只有 `from`/`to` 字符串，**不足以生成 DDL**
  （`add_column` 要完整列定义、`create_table` 要全部列），把定义塞进线路 DTO 会让 diff 响应背上这堆字段。
  Web 现成的 `alterSql.ts` 继续服务结构编辑器，**不作为 diff 的出口**
  （避免同一个方言规则存在两套真相源）。
  验收：23 份共享语料跑完整管线逐字段对拍 + 契约真跑「应用后计划收敛为空、回滚后计划精确复原、
  重建期间子表行数 1 行不丢」，落地实况见附录 A.12。
- **E4 Web 向导** ✅（2026-09-26）：四步（选基准侧 → 选期望侧 → 差异与勾选 → 脚本与应用），复用导入向导的
  `Step` 联合类型 + `polydb.<feature>.v1` localStorage 命名 + busy 门禁 + 失败详情展示惯例。
  **已定（2026-09-26）四条**：① 形态是**向导 Modal + 三处入口**（活动栏第 4 个图标 / `Ctrl+4`、命令面板
  `app.compare-sync`、库表树表与 schema 右键），不新增常驻侧栏视图、不动 `ViewId` 状态机；
  ② 执行通道**按 `pragmas_outside_tx` 分派**——true ⇒ 首尾两条 PRAGMA 走 `/query`（事务外）、主体包一个
  `/api/transactions` 事务，false ⇒ 逐条 `/query`；③ 回滚进 v1，**由前端代跑**回滚脚本 + 强确认
  （列出全部 `non_reversible` 语句 + 手输基准侧连接名才放开按钮）；④ 差异预览是**按相位分组的可勾选列表**，
  破坏性条目红标且默认不勾、`selected` 初值直接采用服务端回值。
  零 spec 变更、零后端改动，纯前端接线（比对/生成/校验真相全在服务端）。落地实况见附录 A.13。
- **E5 GUI/TUI + 文档**：✅ 已落地（2026-09-26，零 spec 变更）。四组分叉经确认定案：
  ① **三批推进** Go+TUI → Rust+GUI → 文档收口，每批独立门禁 + 独立中文提交；
  ② GUI 形态 = **浮层 Modal，四步在层内自走**（不碰 `workspace: Option<usize>` 单活动连接状态机，
     因此不需要"临时切活动连接"这套危险动作）；③ TUI 形态 = **独立 view + 三段**（沿 `viewExplain` 先例，
     基准侧取进入时的当前连接，故比 Web/GUI 少一步）；④ 两端回滚口径 = **代跑回滚 + 逐字输入基准侧
     连接名强确认**（与 Web 对齐），正向只要求"显式生成 + 显式执行"，执行通道按
     `pragmas_outside_tx` / `ddl_in_tx` 分派。入口：GUI 顶栏「⇄ 结构同步」+ `Ctrl+4`、TUI 库表视图 `c`。
  `docs/frontends.md` 新增「结构比对与同步（三端一致，E5）」一节 + TUI 键位表三段行。落地实况见附录 A.14。
- **E6 证据补齐 + 还债** ✅（2026-09-26，六个子任务，附录 A.15–A.21）：范围经 AskUserQuestion 定为
  「取证 + 还债」，**不含**同步预设持久化 / PG+MySQL 方言扩展 / 数据行比对同步。六批各带独立门禁与独立中文提交：
  **E6.0** 视图依赖真引擎取证（A.15，**证伪**了"视图按名字解析所以不破"的旧假设）→
  **E6.1** 执行分派的应用环路（A.16，Go/Web 调用序列断言 + Web 编排抽成 `lib/compareSync.ts` 的 `runScript`；
  顺带订正 `pragmas_outside_tx` **不是恒 true**，口径是"任一脚本含重建"）→
  **E6.2** TUI 远程模式端到端（A.17，同一套按键脚本换 `Remote` 客户端 + `httptest` 真服务端，零生产代码改动）→
  **E6.4** 采集扇出量化（A.18，"200 表上限"问错了对象；真实代价是 `2×(1+3n)` 次查询、线性无悬崖 ⇒ 不改代码）→
  **E6.3** GUI 向导状态机抽纯层（A.19，抓到**双实现分歧**：GUI 事务分支从不发 ROLLBACK；另修好三处退格失效）→
  **E6.5** 视图可见性按 **A 档**落地（A.20，本阶段唯一一处规则变更）→ **E6.6** 文档收口 + 全端门禁基线刷新（A.21）。
  证据等级刻意分三档并逐处标明：**真引擎 + 录音装饰器**（Rust/Go）> **假台账**（Web，浏览器里没有引擎）；
  两端"看不见彼此"的 Rust app-core 无私有注驱动门，这类不对称在 A.18/A.21 里明说而不是抹平。

## 7. 本机不可验证清单（先把话说在前面）

- 本机**无 Docker、无监听中的** 5432/3306/6379/1433/1521 ⇒ PG/MySQL/MSSQL/Oracle/Redis 相关
  分支只能 soft-skip（E6.6 刷新基线：契约 **23 个顶层测试 / 89 个 PASS 含子测试、0 FAIL、0 SKIP 标记**，
  方言与 Redis 分支只是"未打崩"而非"真跑"；E3 收口时同一口径记的是 66，差值是 E6 期间新增的子测试）。
  **跨库 diff 的正确性、各方言 DDL 语法、`ddl_in_tx` 真值都无法在本机实测**。
- E3 因此**只交付 SQLite**：4 种方言 + Redis 的 `NOT_SUPPORTED` 拒绝路径由语料 d16–d19/d20/d21
  （纯函数层，恒可跑）与两端错误码映射（`NOT_SUPPORTED → 501`）双重锁定，
  但**契约层的 501 响应本机从未真正打出过**（`/api/schema/ddl` 的方言分支同样 soft-skip）。
- 契约里的"应用脚本"是**逐条 `/query` 顺序执行**，没有走 `/api/transactions`——因为含重建的脚本
  要求首尾两条 PRAGMA 留在事务外（G6）。
  ~~**「把 `pragmas_outside_tx=false` 的脚本整份包进一个事务」这条真实调用路径仍未跑到**~~
  **E6.1 已跑到并订正了一处事实错误**：能力位是 `pragmas_outside_tx = 任一脚本含重建`
  （Go `pkg/schemadiff/ddl.go` 的 `PragmasOutsideTx: len(rebuilt) > 0 || len(rebuiltRev) > 0`，
  Rust 同形 `pragmas_outside_tx: !rebuilt.is_empty() || !rebuilt_rev.is_empty()`），
  所以 SQLite 的**不含重建**脚本（纯 `add_column` / `drop_index`）
  本来就是 `false` ⇒ "整份脚本包一个事务"不是罕见支路，而是 SQLite 上最常见的那一支。
  旧文本"SQLite 两端能力位恒为 `true`"只在以重建为中心的契约场景里成立，是错的。
  现在两端各有一组带**调用序列**断言的应用环路测试（附录 A.16）；
  仍未跑到的只剩**契约层**（HTTP `/api/transactions` 走一遍两种分派各一条断言）。
- ~~视图（`CREATE VIEW`）对重建表的依赖行为**仍未测**（E3.0 遗留）~~ **E6.0 已测完并证伪了原假设**
  （附录 A.15）：SQLite 的 `ALTER TABLE … RENAME` 会重解析依赖该名字的视图，所以视图不是"预期不破"，
  而是**让整表重建在 RENAME 那一步直接失败**；真正静默留下破视图的是 `drop_table`。
  **E6.5 已按 A 档落地**（附录 A.20）：视图名进快照 ⇒ 临时名避让、`drop_table` 与重建各挂一条点名视图的
  粗粒度警告。**仍未做的**：B 档的"依赖可见"（快照带视图定义文本 + 文本扫描判定谁引用了谁）——
  所以警告只能说"本库有这些视图、可能有破的"，判不出具体哪一个；SQLite 无依赖目录，这条要做得靠解析 SQL 猜。
- 只有 SQLite 能端到端真跑（**两个文件 / 两个连接**；E2.0 实测确认驱动不支持 schema 寻址，
  "同库两 schema"需先拍板 §8-6）——所以 v1 的验收以 SQLite 为主，
  其余方言的正确性依赖单测 + 后续在有实例的环境补测。
- `golangci-lint` v2 本机未安装（Go lint 步骤恒跳过）；GUI(GPUI) 渲染无法无头肉眼验证。
- **E5 之后仍缺的两类证据**（2026-09-26 收口时明确）：① ~~GUI 向导的**状态机 + 渲染没有测试**——
  Rust 侧只有 `cmpsync.rs` 纯函数单测与 `transport/tests`，`app.rs` 的按键分派与 `view.rs` 的浮层
  没有 GUI 端测试（本项目无 GPUI 测试基建），只能靠 TUI 侧同构的 keystroke 测试背书~~
  **E6.3 已补到"纯状态机 + 真引擎分派"两级**（附录 A.19）：向导态与全部转移、键位表 `key_action`、
  分派环路 `run_script` 全搬进零 gpui 类型的纯层，用真 `:memory:` SQLite + 真 `LocalTransport` 套录音
  装饰器断言调用序列与库内终态（证据等级与 Go 对齐）；**只剩"把按键灌进真窗口"那一级仍缺**（GPUI 无测试基建）；
  ② ~~TUI 向导只在**本机模式**（进程内 app-core）真跑过，远程模式（打 polydb-server 的 HTTP 路径）
  的 UI 链路未验证~~ **E6.2 已补**（附录 A.17）：同一套按键脚本换一个 `Remote` 客户端跑通，
  比对/生成/事务执行/代跑回滚都经 `httptest` 真服务端，并带请求计数证明流量真的经过 HTTP；
  该 HTTP 路径本身另由 E2/E3 契约对拍锁定。
  Web 侧 E4 已在真实 Chromium 实例里把向导全流程走通（附录 A.13「浏览器真跑」），但该实例视口固定在
  ~565×482 且没有可用的改窗口工具，所以**布局只做了数值核对**（行宽/标签宽用 `evaluate_script` 量），
  宽屏下的视觉排版仍未肉眼确认。
- **E6 收口后仍然缺的证据**（2026-09-26 收敛后的清单，逐条都是**结构性**缺、不是"还没写"）：
  ① **契约层对两种执行分派的 HTTP 级断言**——单事务一支已由 A.17 经 `httptest` 真服务端跑到，
  `ddl_in_tx=false` 一支 SQLite **结构上产不出**（引擎 DDL 可在事务里，能力位恒 `true`），
  只能等有真实 PG/MySQL/MSSQL/Oracle 实例时补；② GUI"按键灌进真窗口"的端到端（GPUI 无测试基建）；
  ③ **B 档的依赖可见**：警告只能说"本库有这些视图、可能有破的"，判不出具体哪一个（§8-8 已明确不做，
  要做就得解析 SQL 猜——SQLite 无依赖目录）；④ 4 种方言 + Redis 的真跑，含**契约层的 501 从未打出**
  （**PG 一支已由 F0.1/F0.2 从"取不到证据"变成"只差引擎"**：Go 12 点 + Rust 6 点探测已就位，
  `POLYDB_TEST_PG` 一给就跑，见附录 A.26；剩下的结构性缺口是 MySQL/MSSQL/Oracle/Redis
  连取证点清单都没有，记 F0.4）；
  ⑤ 触发器仍在盲区（A.11 G4：重建静默丢触发器，快照不含触发器，E6.5 A 档范围外）；
  ⑥ E6.5 两条新视图警告的**渲染未肉眼验证**（文案近 90 字，窄面板下换行靠通读）；
  ⑦ `golangci-lint` v2 本机未安装 ⇒ Go lint 步骤恒跳过（E6.6 复核：确实仍不在 PATH）。
  以上任何一条被补上时，请回来划掉本条并刷新附录 A.21 的基线表，不要在别处另立口径。

## 8. 未决问题（开工前需要拍板）

1. ~~D1 选推荐（服务端）还是备选（三端各自实现）？~~ **已定：服务端一处**（2026-09-25），
   E3 的语句生成因此同样落在服务端（两端各一套 + 契约对拍），前端不自带方言规则。
2. ~~是否需要"生成回滚脚本"进 v1？~~ **已定（2026-09-25）：进 v1**，实现方式是**同一批勾选条目把
   源/目标互换再走一次生成器**（不新写第二套规则，成本从 +400 行降到响应多一个字段 + 一次互换调用）。
   代价：数据类动作（`drop_table`/`drop_column`/类型收窄）**DDL 找不回数据**，必须逐条标
   `non_reversible`（判定规则见附录 A.11 第 5 条），回滚脚本里带显式警告。
3. `generic_type` 的归一命名表由谁定：以 PG 命名为准、还是自创中立名（`int64`/`text`/`timestamp`…）？
   **E3 不需要答案**——本轮生成器只覆盖 SQLite，语句直接渲染驱动原 `data_type` 文本；
   该项与 §8-7 绑定，等有可实测的方言实例再定。
   **→ F0.1/F0.2 已把"等"变成有抓手的等**（附录 A.26 P10）：PG 侧一份 14 种类型的 `format_type` 原始文本探测已就位，
   引擎到位跑一次就拿到定名表的输入。**仍待定**，但不再需要现场设计。
4. ~~差异条目里"仅目标库存在"要不要包含**数据不可逆**提示~~ **已定（2026-09-26，E4 落地即此）**：
   `destructive` 条目红标 + 默认不勾选，勾选初值直接采用服务端回值（前端不自造规则）；
   另有「破坏性动作默认勾选」开关供显式放开。见附录 A.13。
5. ~~表数上限：GUI 现有整 schema 外键扫描硬上限 200 表，diff 场景动辄上千表，要不要在 E2 一并放宽/分页？~~
   **E6.4 已订正：这个问题问错了对象**（附录 A.18）。`.take(200)` 在
   `rust/crates/ui-gui/src/app.rs` 的 `load_incoming`（表页「关系」轨道列**入向外键**时逐表扫
   `list_foreign_keys`），**与结构比对无关**；比对路径（两端 `collect_snapshot`）本来就**没有任何表数上限**。
   真正剩下的不是"上限要不要放宽"，而是"每表 3 次串行元数据查询"的扇出代价——已实测：
   SQLite 内嵌引擎上 1000 表 ≈ 25 ms（Go 单侧快照）/ 两侧比对 ≈ 137 ms（Rust，dev 构建），
   线性、无悬崖。⇒ **不放宽、不分页、不改代码**；只有换成网络数据库（每查询一次网络往返）时这条才会变成问题。
6. ~~**SQLite 的比对形态**~~ **已定（2026-09-25）：只做"两个连接 / 两个文件"**，SQLite 的 `schema` 参数固定 `main`。
   依据：驱动层 `list_tables` 忽略 `schema` 入参、`PRAGMA table_info` 不支持 schema 前缀（E2.0 实测，附录 A.9 F6）；
   而 ATTACH 只在单连接上有效，与现有连接池语义冲突。**"同库多 schema"记为 v1.1 待办**，spec 里 `schema` 保持可选。
   **→ F0.2 已备好 v1.1 的开工判据**（附录 A.26 P11，双端各一份探测）：PG 的 `list_tables` 读代码是
   `WHERE n.nspname = $1`，但"同库多 schema 可行"要**两头**成立——探测 schema 里查得到 **且** `public` 里查不到。
   本条从"没探"变成"跑一次就知道"。
7. ~~**其余 4 个方言的 `on_update`/`on_delete` 与 `generic_type` 何时填**~~ **已定（2026-09-25）：随 E3 / 有实例环境一起补**。
   理由：本轮只补了 SQLite，因为 SQLite 有 bundled 真引擎可实测；PG/MySQL/MSSQL/Oracle 现在补只能凭文档盲填、
   契约测试全 soft-skip，等于"数字上已实现、实际未验证"。与 §8-3 的 `generic_type` 命名表定案绑定推进。
   **F0 进展（2026-09-27，附录 A.26）**：本条的"缺口在哪一头"已经**由读代码确认**——
   Go 的 `dbpostgres.ListForeignKeys` 连 `confupdtype` 都没查进 SELECT，Rust 的 `db-postgres` 直接 `on_update: None`
   ⇒ **缺的是读侧，不是引擎**（PG 目录肯定给得出）。补读 SQL 是十分钟的事，但按本条定案**仍然不做盲填**，
   等 P9 探测（双端各一份，已就位）在有引擎时确认回读形状，再一次性补两端 + 语料。
   **MySQL/MSSQL/Oracle 三家的取证点清单仍未立**（本批只立了 PG；MySQL 的结构差异见 A.26 末尾"为什么不做 MySQL"）。
8. ~~**快照要不要让生成器"看见"视图？**~~ **已定（2026-09-26，E6.5 落地 A 档：名字可见 + 粗粒度警告）**。
   A.15 的 G13/G16/G17 是同一条盲区的三个面：重建被依赖视图挡住、删表静默留下破视图、临时名撞上同名视图。
   落地的三件事：① 采集层不再丢弃视图（快照只带**名字与类型**、列/索引/外键留空，**不发元数据查询**
   ⇒ 每表 3 次的扇出口径不变）；② 临时名去重集 `taken` 纳入两侧视图名（G17 彻底消除）；
   ③ `drop_table` 与重建组首条挂**粗粒度**视图警告，点名快照里的视图（升序、最多 5 个、超出以「等」收尾）。
   **明确不做 B 档**（快照携带视图定义文本 + 文本扫描启发式判定"谁依赖谁"）：SQLite 没有依赖目录，
   那是把猜测塞进双端逐字一致的规则层，收益不抵漂移面。
   **订正一处原预估的事实错误**：原先写"只去掉采集层那一行过滤即可，DTO/线路/diff 规则零变更"——
   不够。`table_map` 为了「视图不参与比对」把非表项跳掉了，而 `taken` 正是从 `table_map` 播种的
   （G17 之 ②③），所以**必须**再加一次 `view_names()` 取名字并播种 `taken`。
   代价如预期落在文档与呈现层：`spec/behavior.md` §14.1 口径改写、§15.8 多两条动态告警文案
   （详见附录 A.20）。

---

## 附录 A：E1 落地实况（2026-09-25，本附录是 E2/E3 的输入规格）

E1 只做了「两侧快照 → 结构化计划」这一件纯函数的事。**本附录里的每一条规则都是双端已实现、
已被 16 份共享向量逐字锁死的现状**，不是待办。改任何一条 = 同时改两端 + 重跑向量对拍。

### A.1 落点

| 侧 | 位置 | 行数 | 内容 |
| --- | --- | --- | --- |
| Rust | `rust/crates/schema-diff/src/lib.rs` | 1010 | `Op`/`ColumnFlag`/`Entry`/`Options`/`TableSnapshot`/`Snapshot` + `diff()` + 11 单测 |
| Rust | `rust/crates/schema-diff/src/normalize.rs` | 434 | 类型/默认值归一 + 5 单测 |
| Rust | `rust/crates/schema-diff/tests/vectors.rs` | 102 | 共享向量对拍 + 覆盖率断言 |
| Rust | `rust/crates/schema-diff/examples/regen_expected.rs` | 129 | 开发用：把 Rust 输出写回向量 `expected` |
| Go | `go/pkg/schemadiff/schemadiff.go` | 595 | 同结构 DTO + `Diff()` |
| Go | `go/pkg/schemadiff/normalize.go` | 380 | 归一逐函数镜像 |
| Go | `go/pkg/schemadiff/{schemadiff,vectors}_test.go` | 369 | 11 + 2 个测试镜像 Rust 断言 |
| 共享 | `test/schema-diff-vectors/*.json` | 622 | 16 份语料（两端读同一目录） |

**为什么不是计划里写的 `app-core/src/schema_diff.rs`**：`polydb-app-core` 没有 `serde` / `serde_json`
依赖（它只做用例编排），要落在这里就得给 app-core 加序列化依赖；而 diff 本身零 IO、零状态，
独立薄 crate / 独立包更干净，E2 由 app-core 或 server 直接依赖它。架构红线不变（无 IO、无 UI、不碰 driver）。

### A.2 类型与线路字段（JSON 字段名双端逐字一致）

```
Snapshot       { kind, schema, tables: [TableSnapshot] }
TableSnapshot  { name, type?, columns: [ColumnInfo], indexes: [IndexInfo], foreign_keys: [ForeignKeyInfo] }
Options        { ignore_defaults: bool, select_destructive: bool }
Entry          { id, op, phase, table, object?, from?, to?, flags?, destructive, selected, reason }
```

- `columns/indexes/foreign_keys` **直接复用 spec 既有 DTO**（`spec/schemas/metadata.json`），E1 不新增任何 DTO，
  所以 E1 零 spec 变更、`check-protocol` 恒过。
- `Entry.table` 取**目标侧原样大小写**（删表动作取源侧），`Entry.object` 是列名/索引名/外键名，表级动作为空。
- 空值省略规则一一对应：Rust `#[serde(skip_serializing_if=…)]` ↔ Go `omitempty`
  （`object` 空串省略、`from`/`to` `None` 省略、`flags` 空省略且 **Go 侧空必须是 `nil` 不能是 `[]`**）。
- `id = {lower(table)}/{lower(object)}/{op}`，供 UI 记忆勾选态（E4/E5 用）。
- `from`/`to` 只在列类动作有值：`add_column.to`=归一类型、`drop_column.from`=归一类型、
  `alter_column.from/to`=归一类型对（即使本次只改可空性，两者相同）。索引/外键/表类动作不带。

### A.3 op、相位与排序

| op | 相位 | destructive | 说明 |
| --- | --- | --- | --- |
| `drop_foreign_key` | 10 | ✔ | 先解除依赖才允许删列/删表 |
| `drop_index` | 20 | ✔ | 重建对的删侧 |
| `drop_column` | 30 | ✔ | |
| `drop_table` | 40 | ✔ | 注意它**排在 `create_table` 之前** |
| `create_table` | 50 | | 段内再做外键依赖拓扑 |
| `add_column` | 60 | | |
| `alter_column` | 70 | | 一条可带多个 `flags` |
| `add_index` | 80 | | 重建对的加侧 |
| `add_foreign_key` | 90 | | 最后建，引用目标一定已存在 |

- 全序排序键 `(phase, lower(table), lower(object), id)`；`op` 拼在 `id` 里，所以同相位的
  drop/add 对（`idx_a` 与 `primary` 这类）由对象名再自然定序，不做额外特判。
- `create_table` 段整体替换为 **DFS 拓扑序**（被引用表先建），拓扑内部以名字序为 tie-break，
  **成环时不报错**、按到达顺序放行（执行期由数据库报错兜底，与视图策略一致）。
- `destructive` = 四类 Drop；`selected = !destructive || opts.select_destructive`（D4：默认不勾选）。

### A.4 归一规则（「同一语义不同写法」折叠，双端逐字等价）

**只用 ASCII 规则**（小写折叠只动 ASCII 字母、空白只认 ` \t\n\r\v\f`）——这是两套实现能逐字一致的
前提；代价是非 ASCII 引号标识符不折叠大小写，v1 已知限制，写进两端模块文档。

类型 `normalize_type(kind, raw)`：
1. 通用：`trim` → ASCII 小写 → 连续空白压成单空格 → 标点旁空白剥离
   （紧邻 `(` `[` `,` 之后、紧邻 `(` `)` `,` `]` 之前的空白删掉，所以 `varchar ( 20 )` ≡ `varchar(20)`）。
2. MySQL：删整数显示宽度（`int(11)`→`int`，**只删宽度、保留类型名**）+ 删 `zerofill` 词。
3. Oracle：`bpchar`→`char`；`number(p,0)`→`number(p)`（scale 为 0 才是噪声）。

默认值 `normalize_default(raw)`：
1. `trim` → **引号外**小写（字符串字面量内保持原样）→ 空白折叠；空则视为无。
2. 循环「剥最外层配对括号」+「剥尾部 `::type` 投射」直到不动点。投射取**深度 0 的最后一个** `::`，
   且逐词校验尾部确实是类型名（支持 `character varying`、`numeric(10,2)`、`varchar[]`、多段 `::a::b`）。
3. 结果为空串或 `null` → `None`（即「无默认值」），所以 `NULL` / `((NULL))` / `'x'::text` 与缺省同义。

`ignore_defaults=true` 时默认值维度整体不参与比较。

### A.5 匹配规则（谁算「同一个对象」）与 v1 不比清单

| 对象 | 匹配键 | 变化时的产出 |
| --- | --- | --- |
| 表 | 小写表名；`type` 为视图/物化视图的**整体跳过** | `create_table` / `drop_table` |
| 列 | 小写列名 | `add_column` / `drop_column` / 一条 `alter_column`（`flags` 顺序固定：类型→可空性→默认值→自增） |
| 主键 | `IndexInfo.primary` 标志（**不比索引名**） | 列签名变化 ⇒ `drop_index`+`add_index` 重建对，对象名各取本侧 |
| 非主键索引 | 小写索引名，无名回落 `idx({列签名})` | 列签名或唯一性变化 ⇒ 重建对（同一 reason 文案） |
| 外键 | 有名字用 `n:{小写名}`，否则 `c:{列}→{引用表}({引用列})` | 列/引用表/引用列/`on_delete`/`on_update` 任一变化 ⇒ `drop_foreign_key`+`add_foreign_key` |

- 索引列签名 = 按 `position` 升序的列名小写逗号连接；**忽略 ASC/DESC 与前缀长度**。
- 引用表小写，带 `referenced_schema` 时为 `{schema}/{table}`（跨 schema 引用不会被误判为相同）。
- 遍历一律走 `BTreeMap` / 排序键（Go 侧 `sortedKeys` + `sort.SliceStable` + 首值优先入 map），
  保证**发射序**确定；同名重复项以第一条为准（Rust `entry().or_insert()` 的等价写法）。
- v1 **明确不比**：字符集/排序规则、列顺序、注释、非主键索引的纯改名、`generic_type`
  （该字段仍无人填值 ⇒ 跨种类比较直接报错，归一是 v2 前置）。
- 已文档化的有意行为：源侧是视图、目标侧是同名表 ⇒ 仍产出 `create_table`（不猜语义，执行期由 DB 报错兜底）。

### A.6 reason 文案表（E3 语句生成与三端渲染**必须引用同一张表**）

多因用全角 `；` 连接；`索引定义变化` 用全角 `：`；`是/否`、`NULL/NOT NULL`、`无`（默认值与动作缺省）为固定词。

| 场景 | 文案 |
| --- | --- |
| 表 | `表中缺失，需新建` / `表中多余，需删除` |
| 列存在性 | `列缺失，需新增` / `列多余，需删除` |
| 列类型 | `类型 {a} → {b}` |
| 可空性 | `可空性 {NULL\|NOT NULL} → {…}` |
| 默认值 | `默认值 {x\|无} → {…}` |
| 自增 | `自增 {是\|否} → {…}` |
| 索引存在性 | `索引缺失，需新增` / `索引多余，需删除` |
| 索引定义 | `索引定义变化：{a} → {b}`、`唯一性 {是\|否} → {…}` |
| 主键 | `主键列变化，需重建` |
| 外键存在性 | `外键缺失，需新增` / `外键多余，需删除` |
| 外键定义 | `列 {a} → {b}`、`引用表 {a} → {b}`、`引用列 {a} → {b}`、`ON DELETE {x\|无} → {…}`、`ON UPDATE {x\|无} → {…}` |
| 动作词 | `cascade` / `set_null` / `set_default` / `restrict` / `no_action` / `无` |
| 错误文案 | `跨种类比对未支持：{a} vs {b}（v1 只比同一种数据库）`、`Redis 无模式概念，不参与结构比对` |

错误经 `CoreError::NotSupported` → `POLYDB_ERR_NOT_SUPPORTED`（Go 侧 `*protocol.PolyDBError` 同码）。

### A.7 共享向量语料与「生成后人工复核」流程

`test/schema-diff-vectors/*.json`，每份含 `name` / `note` / `options` / `source` / `target` /
`expected`（或 `error_code` + `error_message`）。两端测试读**同一目录**、逐字段断言。

| # | 文件 | 断言要点 | 条目数 |
| --- | --- | --- | --- |
| 01 | `01_identical` | 两侧一致 ⇒ 空计划 | 0 |
| 02 | `02_table_add_drop` | `drop_table(40)` 先于 `create_table(50)`，破坏性默认不勾选 | 2 |
| 03 | `03_column_add_drop` | 同表列增删，`from`/`to` 带归一类型 | 2 |
| 04 | `04_mysql_type_width` | MySQL 宽度 + `zerofill` 是噪声 ⇒ 空计划 | 0 |
| 05 | `05_type_nullability_autoincrement` | 三维差异合并成一条 `alter_column`，`flags` 顺序固定 | 1 |
| 06 | `06_default_normalize` | 括号/尾部投射/`NULL`/引号外大小写不产差异，真差异才产 `default` | 1 |
| 07 | `07_ignore_defaults` | 开关打开后只差默认值不产变更 | 0 |
| 08 | `08_index_rebuild` | 列顺序 + 唯一性变化 ⇒ 成对重建，全角 `；` 拼接 | 4 |
| 09 | `09_pk_change` | 主键按 `primary` 匹配（不比名），`select_destructive:true` | 2 |
| 10 | `10_fk_change` | 外键按名匹配，多因合并，`drop(10)+add(90)` | 2 |
| 11 | `11_case_insensitive_match` | 大小写折叠不得造成删+建误判 | 0 |
| 12 | `12_views_skipped` | 视图/物化视图不参与；视图↔同名表仍产 `create_table` | 1 |
| 13 | `13_phase_order` | 一表七类动作，断言相位全序 10<20<30<60<70<80<90 | 7 |
| 14 | `14_create_table_topo` | `orders` 名字序在前但依赖 `users` ⇒ `users` 先建 | 2 |
| 15 | `15_cross_kind_error` | 跨种类报错而非猜测 | — |
| 16 | `16_redis_error` | 任一侧 Redis 即拒绝 | — |

流程（**改规则时必须照做**）：`cargo run -p polydb-schema-diff --example regen_expected` 从 Rust 实现
写回 `expected`（保持 CRLF 探测、条目压成单行 JSON），然后**逐条人工复核**再提交；两份错误向量的
`error_code`/`error_message` 保持手写，生成工具只校验不写回——否则断言退化成同义反复。
Go 侧**永不**写回语料，只读只断言，这样「Rust 写、Go 验」才是真对拍。

### A.8 门禁数字（2026-09-25 本机实测）

- Rust：`cargo test -p polydb-schema-diff` **18 passed**（lib 16 + 向量 2）；workspace `cargo test` **110 passed / 0 failed**；
  `cargo clippy --workspace --all-targets -- -D warnings` 退出 0（**零警告**）；`cargo fmt --all --check` clean。
- Go：`go test ./pkg/schemadiff/... -v` **13 passed**（镜像 11 + 向量 2）；`go test ./...` 全绿；`go vet ./...` clean；
  剥 CR 后 `gofmt -l` 遍历 73 个 `.go` **0 未格式化**。
- 漂移：`check-protocol` **54 spec types vs rust + go 零漂移**，`ts --check` 一致（E1 零 spec 变更）。
- Go lint：`golangci-lint` v2 本机未安装，该步骤恒跳过。

### A.9 E1 未验证项 → E2.0 真快照复核结论（2026-09-25 已完成）

E1 的 16 份向量里 `source`/`target` 全是**手写构造**，而 diff 的匹配规则只认三类**归一后的字符串**
和三个**结构性字段**（`indexes[].primary`、`foreign_keys[].name`、`on_update`/`on_delete`）——
后者靠"驱动真的去填"，这正是 E1 **从未验证**的部分。SQLite 有真引擎（两端各自 bundled 的
rusqlite / modernc.org/sqlite），所以 E2 的第一步不是补 spec DTO，而是**拿 SQLite 真快照喂 diff**
（`rust/crates/db-sqlite/tests/snapshot_diff.rs` + `go/pkg/dbsqlite/snapshot_diff_test.go`，**各 10 份镜像**），
把假设变成断言。这一步当场抓到 **3 个真缺陷**，两端同步修掉：

| # | 实测结论 | 后果（若未修） | 处置 |
|---|---|---|---|
| F1 | **SQLite 外键本来没有名字**。Rust 侧原先用 `HashMap` 聚合 `PRAGMA foreign_key_list` 的 `id` 再按**遍历序**合成 `fk_{i}`；Go 侧按**发射序**合成 | 编号既跨进程不确定、又随位置整体错位：给 `orders` **新增**一个外键，会让其余外键全部改名 ⇒ diff 凭空产出一对 `drop_foreign_key` + `add_foreign_key`（真快照测试里直接复现） | 两端 `name` 一律置**空串**，交给 E1 的**内容键**配对（`c:{列}->{引用表}({引用列})`）；`fk_object` 空名回落 `fk({列})`。补回归测试 `adding_one_real_foreign_key_does_not_disturb_the_others` |
| F2 | `IndexInfo.primary` **五个 SQL 驱动两端全未填**（恒 `false`） | 主键索引被当成普通索引 ⇒ 主键列变化只生成 `drop_index`/`add_index`，走不到「主键列变化，需重建」这条规则 | SQLite 按 `PRAGMA index_list` 第 4 列 `origin == 'pk'` 填。注意 `INTEGER PRIMARY KEY` 是 rowid 别名、**不建索引**，真快照正确地表现为"无主键索引"，测试固化了这一点 |
| F3 | `on_update` / `on_delete` 同样**五驱动两端全未填**——和 `generic_type` 同一类"字段存在但没人填"的死维度 | 外键动作差异（`CASCADE` vs `NO ACTION`）永远比对不出来，sync 会漏改 | SQLite 已补：`PRAGMA foreign_key_list` 里是 SQL 文本，两端各自映射到 `ForeignKeyAction`；`NO ACTION` 是 SQLite 的**默认值而非"无信息"**，必须映射成 `NoAction` 而不是 `None`（否则"无动作"和"显式 NO ACTION"分不清） |
| F4 | `referenced_schema` 原先 Go 侧**回显请求参数** `schema`，Rust 侧硬编码 `"main"` | dev/prod 用不同 schema 名时引用表侧多出一个假差异 | 两端统一 `"main"` |
| F5 | `PRAGMA foreign_key_list` 的 `id` 是 **SQLite 内部序**（实测 `products` 的 id=0、`customers` 的 id=1，与 DDL 声明顺序**相反**） | 误把它当声明序写测试会得到同义反复 | 测试**不断言**外键排序，只断言发射序列；两端读的是同一个 C 引擎故序一致，且 diff 配对不依赖它 |
| F6 | SQLite 驱动**不做 schema 寻址**：`list_tables(_schema)` 忽略入参，`PRAGMA table_info("t")` 无 schema 前缀 | E1 假设的"同库两 schema"比对形态在 SQLite 上**目前不可行**（ATTACH 后也拿不到 `attached_db` 的表） | SQLite 比对只能退化为**两个连接 / 两个文件**。已定（§8-6）：v1 只做两连接，ATTACH 记 v1.1 待办 |
| F7 | 真字符串上的归一（`VARCHAR(20)` → `varchar(20)`、`DEFAULT 1`、引号/大小写混杂的 DDL）行为与 E1 一致 | — | 真差异文件库测试（`real_differences_between_two_files_produce_expected_plan`）固化，相位序 20/60/70/80 与设计表逐字一致 |

**E2.0 门禁数字（本机实测）**：Rust workspace `cargo test` **120 passed / 0 failed**（`db-sqlite` 7 单测 + 10 真快照集成）；
`cargo clippy --workspace --all-targets -- -D warnings` 退出 0；`cargo fmt --all --check` clean（新测试文件用
`rustfmt --edition 2021` **单文件**格式化，避免 `--all` 波及已跟踪的 CRLF 文件）。Go：`go test ./...` 全绿
（`dbsqlite` 14 PASS 含 10 真快照、`schemadiff` 13 PASS）、`go vet` clean、剥 CR 后 74 个 `.go` **0 未格式化**。
契约测试先 `cargo build -p polydb-server` 再跑：**通过（142.3s）**——F1/F2/F4 改了驱动输出，必须确认没打破
元数据端点的既有对拍（`index_list` 的 `primary`、`foreign_keys` 的 `name`/`on_*` 都在线路上）。

**E2.0 仍未验证（继续诚实披露）**：
1. MySQL / PostgreSQL / MSSQL / Oracle 四方言的 F1–F5 **完全未复核**（本机无实例），且它们的 `on_update/on_delete`
   与 `generic_type` **依然全未填**——本轮修复只落在 SQLite。
2. SQLite 的"同库两 schema"（ATTACH）路径未实现（F6）。
3. 真快照测试只覆盖 SQLite 的**元数据读取路径**；DDL **生成**（E3）与 Web/GUI 渲染仍未验证。
4. 跨种类比对与 `generic_type` 归一均未落地（本层直接报错），这是 v1 的有意收窄。
5. 非 ASCII 标识符的大小写折叠不生效（ASCII-only 规则的代价）。
6. E2.0 仍无端点、无 UI ⇒ 契约测试只回归了既有元数据端点，diff 的三端渲染完全不涉及。

### A.10 E2 落地实况（2026-09-25，协议 + 服务端 + 契约）

**落点**（纯函数层与线路层刻意分开）：

| 层 | Rust | Go |
|---|---|---|
| 线路 DTO | `crates/protocol/src/schema_diff.rs`（78 行，手写 + 漂移门禁） | `pkg/protocol/schema_diff.go`（77 行） |
| 快照采集 + use case | `crates/app-core/src/lib.rs`（`collect_snapshot` / `schema_diff` / `to_wire_entry`，+108/−4） | `pkg/appcore/schema_diff.go`（126 行） |
| 端点 | `crates/server/src/lib.rs` `POST /api/schema/diff` | `pkg/server/server.go` 同路径 |
| 单测 | `crates/server/src/tests.rs` +131（4 份） | `pkg/server/server_test.go` +110（4 份镜像） |
| 契约 | —— | `test/contract/e2_schema_diff_test.go`（353 行，3 个顶层测试） |

**关键设计点（E3 及三端 UI 必须沿用）**：

1. `polydb-schema-diff` / `pkg/schemadiff` 的 `Entry` **不是**线路类型。线路走
   `SchemaDiffEntry`，转换在 app-core 完成（Rust 侧另有一条硬约束：孤儿规则不允许
   `impl From<schema_diff::Entry> for SchemaDiffEntry`，故用自由函数 `to_wire_entry`）。
2. msgpack 可选字段两端对称：Rust `Option<T>` + `skip_serializing_if` ⇄ Go `omitempty` / `*T`。
   `flags` 无改变时 Rust 是空 `Vec`（省略）、Go 必须保持 `nil`（不能是空切片）。
3. **空计划必须是 `[]` 而不是 `null`**：Go 侧 `SchemaDiffPlan.Entries` 显式初始化空切片，
   否则 nil 切片编成 msgpack nil，web 端直接打崩（沿用既有约定）。
4. `schema` 缺省与"显式空串"**同义**：Go `omitempty` 会把空串抹成缺席，两端必须同一解释，
   故 `trim` 后为空即回落 `default_schema`（SQLite `main` / PG `public` / 其余空串）。
5. Redis 在采集阶段就显式 `NOT_SUPPORTED`，逐字文案 `Redis 无模式概念，不参与结构比对`
   已进 behavior.md §14.6 —— 契约测试按整串比对。
6. 契约里 `entries[].id` 是**有意内容**，但 `normalize` 会把任意层级的 `id` 换成 `<dynamic>`，
   所以本批用 `hoistDiffEntryIDs` 先复制成 `diff_id` 再比较，锁死 id 字面值。

**契约覆盖**（`TestContractSchemaDiffSQLite` 在 go / rust 两后端各跑一遍，7 次请求逐字段全等）：
自比空计划 ×2、`src→tgt` 完整三条（相位 40/50/70，含 `object`/`from`/`to`/`flags`/`reason`/`selected`）、
`select_destructive=true` 只翻转勾选、`tgt→src` 镜像计划、空串 schema 与缺省等价、未知连接 404。

**E2 门禁数字（本机实测）**：`tools/genprotocol check` **61 spec types vs rust + go 零漂移**、
`ts --check` up to date（web 类型 +54 行）；Rust `cargo test --workspace` **124 passed / 0 failed**、
`clippy --workspace --all-targets -- -D warnings` 退出 0、`cargo fmt --all --check` clean；
Go `go build` / `go vet` / `go test ./...` 全绿、剥 CR 后新改文件 0 未格式化；
Web `tsc --noEmit` clean + vitest **141 passed**；契约全量 **20 顶层 / 50 子测试全绿、0 FAIL**（190.2s）。
本机未跑 `golangci-lint`（PATH 里没装）。

**E2 仍未验证（继续诚实披露）**：
1. PG/MySQL/MSSQL/Oracle/Redis 相关断言**全部 soft-skip**（本机无实例）。契约里这 5 条只跑到了
   "建连失败即跳过"，`自比空计划` 与 `Redis NOT_SUPPORTED 文案` 两条**从未真正执行过**。
2. 端点只做 diff，**不生成语句**（E3）；三端 UI 一个都还没接（E4/E5）。
3. 四方言的 `on_update`/`on_delete`、`generic_type` 仍未填（§8-7 已定随 E3 补）。
4. 跨种类比对（如 SQLite vs PG）在纯函数层直接报错，服务端未单独测该分支的线路错误码。
5. ~~视图（`TableType::View`）在采集时跳过，"两端都跳过同一张视图"这一条只有单测覆盖，无真实例验证。~~
   E6.5 改口径（视图名进快照、不参与比对，附录 A.20），并补了真引擎侧的避让/警告回归
   （`view_rebuild_probe` 两端各三条）。

### A.11 E3.0 SQLite DDL 取证（2026-09-25，本附录是 E3.1/E3.2 的输入规格）

E3 的语句生成器只覆盖 SQLite（§8 已定），所以先把 SQLite 的 DDL 现实**测出来**再写代码。
探测手段：`go run ./cmd/polydb-cli -db <file> -e "<SQL>"`（同一文件多次/单次多语句）。

| # | 实测结论 | 证据 | 对生成器的约束 |
|---|---|---|---|
| G1 | 两端 bundled SQLite 都 **≥ 3.35** | Go modernc **3.53.4**（`SELECT sqlite_version()`）；Rust rusqlite（libsqlite3-sys 0.30.1）bundled **3.46.0** | `ALTER TABLE … DROP COLUMN` 两端可用，不必退回重建 |
| G2 | `DROP COLUMN` 有四条硬限制 | 被索引引用 → `error in index ix_t_note after drop column: no such column: note`；被生成列表达式引用 → `error in table t after drop column: no such column: s`；主键列 → `cannot drop PRIMARY KEY column: "id"`；**被 CHECK 引用的列 → 直接成功，CHECK 一并消失** | 主键/索引两种情况能从快照预判 ⇒ 转重建；CHECK 那条是静默改语义，只能靠警告 |
| G3 | **生成列在快照里是瞎的** | `sl INTEGER GENERATED ALWAYS AS (length(s)) VIRTUAL` 在 `sqlite_master.sql` 里可见，但 `PRAGMA table_info` 不标注它是生成列 ⇒ `ColumnInfo` 拿不到 | 按快照重建会把生成列**悄悄变成普通列** ⇒ 重建必须挂固定警告 |
| G4 | **重建连带丢触发器** | probe2：重建前 sqlite_master 有 5 个对象（含 `tr_t`），重建后 **4 个** | 同上，警告文案要点名"触发器" |
| G5 | **FK 开着时重建被引用的父表 = 级联删子表数据** | A/B 同一序列：FK 全程 ON → 子表 `c` 从 1 行变 **0 行**；把 `PRAGMA foreign_keys=OFF` 放在**同一连接、事务之外** → 子表 **1 行保住** | 含重建的脚本**必须**首尾切 PRAGMA |
| G6 | **`PRAGMA foreign_keys` 在事务内是 no-op** | `BEGIN; PRAGMA foreign_keys=OFF; COMMIT; PRAGMA foreign_keys` → 仍为 **1** | "整脚本包进一个事务"的常规做法会让 G5 的保护失效 ⇒ 响应必须告诉应用方"首尾两条要留在事务外" |
| G7 | SQLite 的 **DDL 可以在事务里** | `BEGIN; CREATE TABLE yy(…); INSERT …; COMMIT; SELECT` 正常返回 | 本方言 `ddl_in_tx = true` 是**实测值**，不是文档推测 |
| G8 | 两端连接默认 **FK=ON** 且**单连接实例** | `go/pkg/dbsqlite/dbsqlite.go:29,35`（`SetMaxOpenConns(1)` + `PRAGMA foreign_keys=ON`）、`rust/crates/db-sqlite/src/driver.rs:79`（`execute_batch("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON")`） | 脚本首尾的 PRAGMA 在同一次应用里确实作用在同一连接上（否则 G5 无法成立） |
| G9 | 探测方法学的坑 | **polydb-cli 每次调用都是新连接**：第一条调用里的 `PRAGMA foreign_keys=OFF` 对第二条调用毫无影响 ⇒ 我第一轮 A/B 得到了假阳性 | 凡是 per-connection 的断言，必须在**单次多语句调用**内跑完 |
| G10 | **单连接假设成立**：脚本逐条走 `/query` 时，首尾两条 PRAGMA 确实落在同一连接上 | E3.7 契约真跑（go + rust 两后端同结论）：重建期间子表 `child` 恒 1 行——若 PRAGMA 失效，G5 的级联删必然把子表掏空 | 生成器可以假定"PRAGMA 语句 + 重建语句 = 同连接"，无需在脚本里自带连接句柄 |
| G11 | **回滚方向强制重建实测有效**（A.11 规则 7/8 的端到端证据） | 契约：应用 `rollback` 后 diff 精确复原为 `t/note/add_column`(60) + `t/qty/alter_column`(70)，子表仍 1 行 | 「正向重建过 ⇒ 回滚也必须重建」不是保守猜测，是还原整表定义的必要条件 |
| G12 | rusqlite 侧的 `DROP COLUMN` / 重建 / FK 行为与 Go 侧同结论（E3.0 遗留的引擎差异疑虑关闭） | 契约两端跑同一条重建用例，逐字段断言同一份期望 | 双实现共用同一套生成规则即可，无需 per-driver 分支 |

**由取证导出的 E3 规则**（E3.1/E3.2 实现、E3.3 语料、§15 文案都以此为准）：

1. 直改：`create_table` / `drop_table` / `add_column` / `add_index` / `drop_index`。
2. **恒走重建**：`alter_column` / `add_foreign_key` / `drop_foreign_key`（SQLite 无 `ALTER COLUMN`、
   无 `ADD/DROP CONSTRAINT`）。
3. `drop_column`：该列是主键、或出现在任一索引里 ⇒ 转重建；否则直改。生成列依赖判不出来（G3），
   留给执行期报错，脚本带警告。
4. 脚本含重建时，首条 `PRAGMA foreign_keys=OFF;`、末条 `PRAGMA foreign_keys=ON;`，
   并在响应里置 `pragmas_outside_tx = true`（G5 + G6）。**脚本自身不含 `BEGIN`/`COMMIT`**——
   事务由既有的 `/api/transactions` 负责，SQLite 的 `ddl_in_tx` 为 `true`（G7）。
5. 不可逆判定：`drop_table`、`drop_column` 恒不可逆；`alter_column` 当 `flags` 含 `type` 或
   `nullability` 时不可逆，仅 `default` 可逆；其余可逆。
6. 每条重建附固定警告（触发器丢失 / 生成列退化为普通列 / CHECK 不保留），逐字文案进 behavior.md §15。
7. **回滚方向强制重建**：正向里凡是走过重建的表，回滚方向**不得**退化成直改语句——正向一旦重建，
   原定义已不在库里，`ADD COLUMN` + `CREATE INDEX` 的散句补不回被丢弃的对象与约束顺序。
8. **勾选守卫两条**：① `selected` 里的 id 必须存在于本次重算的计划（过期即 400，防止拿旧计划解释新快照）；
   ② 凡进入重建路径的表，同表条目必须**整组**勾选——重建输出的是整张目标定义，
   漏勾的同表变更会被静默应用（数据语义层面等于凭空改掉）。

**E3.0 当时未验证、现由 E3.7 契约关闭**：G2/G5 原本只在 Go 侧引擎上复现过，rusqlite 侧
已在契约里同结论跑通（G10–G12）。**视图对重建表的依赖**当时留给 §7，现由 **A.15（G13–G17）** 关闭。

### A.12 E3 落地实况（2026-09-25，协议 + 双端端点 + 契约真跑）

**落点**（生成器与线路层分两次提交：E3.1–E3.3 已在 `d660594` / `7570059`，本批是 E3.4–E3.7）：

| 层 | Rust | Go |
|---|---|---|
| 线路 DTO | `crates/protocol/src/schema_ddl.rs`（56 行，手写 + 漂移门禁）+ `core` re-export | `pkg/protocol/schema_ddl.go`（46 行） |
| use case | `crates/app-core/src/lib.rs`（`schema_ddl` + `to_wire_statement` / `to_wire_script`，`diff_options` 从 `schema_diff` 里提出来共用，+49/−14） | `pkg/appcore/schema_ddl.go`（70 行）+ `diffOptions` 共用（+11/−6） |
| 端点 | `crates/server/src/lib.rs` `POST /api/schema/ddl`（+17/−1） | `pkg/server/server.go` 同路径（+17） |
| 单测 | `crates/server/src/tests.rs` +141（4 份） | `pkg/server/server_test.go` +139（3 份镜像） |
| 契约 | —— | `test/contract/e3_schema_ddl_test.go`（338 行，3 个顶层测试）+ `e2_schema_diff_test.go` 方言表 hoist 成 `netDialects`/`params()`/`dialectConn()` 供 E3 复用（+70/−51） |

**线路纪律（本批新立的，两端各自断言）**：

1. `statements` / `rollback` / `entry_ids` **空时必须是 `[]`**。Go 侧 `toWireScript` 从
   `[]protocol.DdlStatement{}` 起步——nil 切片会被 msgpack 编成 `null`，web 端 `.map` 直接打崩；
   Rust 侧 `Vec` 天然为 `[]`。三处镜像断言：Rust/Go 服务端各 1 份「空勾选」用例 + 语料 d01。
2. `warnings` 空 ⇒ 整体省略（`skip_serializing_if` ⇄ `omitempty`），但 PRAGMA 两条**永不**为空。
3. `table` 恒在：PRAGMA 两条为空串（不是 `null`、不是省略）。
4. 状态码映射两端同表：`NOT_SUPPORTED → 501`、`INVALID_PARAM → 400`、`CONNECTION_NOT_FOUND → 404`。
   **E2 的 Redis 契约用例原先写的是 500**（一直只被 soft-skip 掩盖），本批读两端
   `error_response` / `errToStatus` 后改判为 501。
5. 请求体协商差异沿用 E2：Rust `decode_body` 收 msgpack 与 JSON，Go 只收 msgpack——
   契约客户端恒发 msgpack，故对拍不敏感（既有事实，非本批引入）。

**契约真跑覆盖了什么**（`go` / `rust` 两后端各一遍，同一条字面期望）：

- 直改脚本：空勾选 ⇒ 两个 `[]`；两条勾选 ⇒ 语句按相位排序（`create_table` 50 在 `add_column` 60 之前），
  回滚两条**全部** `non_reversible = true`（正向建列 ⇒ 回滚删列）。
- 重建脚本：`t` 改类型 + 加列、子表 `child` 挂 1 行。断言 400 文案（过期 id、半组勾选各一条，
  缺失 id 逐字列出）、`pragmas_outside_tx = true`、首条 `PRAGMA foreign_keys=OFF;` 且 `entry_ids` 为空、
  末条 `PRAGMA foreign_keys=ON;`、两条勾选 id 都被重建组 `entry_ids` 承载。
- **数据不丢**：应用脚本后 `SELECT COUNT(*)` 主表/子表均 1 行；应用回滚脚本后 diff 精确复原为原两条，
  子表仍 1 行。这是 G5/G6 那条「数据丢失级」结论第一次在两端 HTTP 路径上被真跑验证。
- 4 方言：`501` + `{kind} 方言的 DDL 生成在 v1 未实现（当前只生成 sqlite）`，本机无实例 ⇒ soft-skip。

**门禁数字（2026-09-25 本机实测）**：`tools/genprotocol check` **64 spec types vs rust + go 零漂移**
（E2 的 61 → +3 个 DDL DTO），TS 重生成 +37 行、`web` `tsc --noEmit` clean；
Rust `cargo test --workspace` **150 passed / 0 failed**（38 suites；`polydb-schema-diff` 40、`polydb-server` 20 含本批 4）、
`cargo clippy --workspace --all-targets -- -D warnings` 退出 0、`cargo fmt --all --check` clean。
Go `go build ./...` / `go vet ./...` / `go test -count=1 ./...` 全绿（`pkg/schemadiff` 35 PASS、`pkg/server` 22 PASS 含本批 3）、
剥 CR 后 7 个改动 `.go` **0 未格式化**；`golangci-lint` 本机仍未安装（恒跳过，如实披露）。
契约 `go test -count=1 ./...` **全绿（434.9s，23 个顶层测试 / 66 个子测试）**，其中 E3 新增 3 个顶层测试。

**本批踩到的坑（写下来防止下次重踩）**：

1. `vmihailenco/msgpack/v5` 把小整数解成 `int8`（positive fixint）。Go 侧
   `got["phase"] != 60` 这种 `any` 比较会**静默为 false**（`any(int8(60)) != any(int(60))`）
   ⇒ 断言必须 decode 进 typed struct（`protocol.DdlScript`）再 `reflect.DeepEqual`；
   通用 `map[string]any` 只用来验「`warnings` 是否省略」「`[]` 是否真是数组」这类**形状**。
2. `INSERT` 的 `statement_type` 是 `"insert"`，不是 `"dml"`（契约里猜错一次，两端一致地打脸）。
3. DDL 生成用例**不能**拿同一个连接自比：自比 ⇒ 空计划 ⇒ 任何 `selected` id 都判为过期 ⇒ 400。
   必须两个文件 / 两个连接。
4. 写模式的格式化器一律被门禁拦：`gofmt -w`（`core.autocrlf=true` 会全量改写行尾）与
   `cargo fmt --all`（写模式）同理 ⇒ 只用 `cargo fmt --all --check` / `tr -d '\r' | gofmt -l`，
   格式差异**手工**改。

**E3 仍未验证（继续诚实披露）**：① 4 种方言 + Redis 的 501 响应**本机没有真正打出过**
（契约层 soft-skip，只有纯函数语料 d16–d21 恒跑）；② 「`pragmas_outside_tx=false` 的脚本整份包进
`/api/transactions` 执行」这条真实调用路径无人跑过（契约是逐条 `/query`）。
**E4 后订正**：向导已实现三种分派并在浏览器跑过"PRAGMA 事务外 + 主体单事务"那一支，但 SQLite 两端
能力位恒为 `true`/`true`，另两支在本机仍没有真库可跑，详见 A.13；③ ~~视图对重建表的依赖未测~~
**已由 A.15（E6.0）实测关闭**，且结论与原假设相反；
④ Go 侧 `golangci-lint` 本机缺失；⑤ Web/GUI/TUI 三端渲染完全不涉及
（本批只有服务端 + 生成的 TS 类型）。

### A.13 E4 落地实况（2026-09-26，Web 向导，**零 spec 变更 / 零后端改动**）

**落点**（8 个源文件：3 新建 + 5 改动；提交 `b848473` 合计 +1332/−13，含本文档 +98/−9）：

| 层 | 文件 | 行 |
|---|---|---|
| 纯函数层 | `web/src/lib/compareSync.ts` | 265（新建） |
| 纯函数单测 | `web/src/lib/compareSync.test.ts` | 187 / 17 份（新建） |
| 向导组件 | `web/src/components/CompareSyncWizard.tsx` | 686（新建） |
| API 包装 | `web/src/lib/api.ts` | +14（`schemaDiff` / `schemaDdl`，走既有 msgpack `request<T>`） |
| 入口接线 | `web/src/App.tsx` +40/−1、`ActivityBar.tsx` +30/−1、`SchemaBrowser.tsx` +11/−2、`ShortcutPanel.tsx` +1 |

纯函数层承担全部可测逻辑：步序与中文标签（`STEP_ORDER` / `STEP_LABEL` / `PHASE_LABEL` 含 0/255 的
PRAGMA 段）、按相位分组、默认勾选与破坏性过滤、整表重建的**前端预提示**（`rebuildLikelyTables` /
`groupGaps` / `fillGroupGaps`）、脚本统计（`summarizeScript`）、执行分派计划（`planExecution` 拆
`pragmaHead` / `pragmaTail` / `body`）、候选连接（同种类、剔 Redis）、状态持久化。
**比对与生成的真相一律在服务端**，前端只做渲染与执行——所以 `planExecution` 里那条
「首尾 PRAGMA 是 `phase===0/255` 且 `entry_ids` 为空」的识别规则，与 A.12 的线路纪律第 3 条直接对齐。

**执行编排**（`applyScript`，正向与回滚共用同一条通道，调用方传哪份跑哪份）：

1. 分派表 `steps[]`：`ddl_in_tx && pragmas_outside_tx` ⇒ 首尾 PRAGMA 标 `inTx:false`、主体全标 `true`；
   其余情况 ⇒ 全部按 `ddl_in_tx` 走（逐条 `/query`，或整份包一个事务）。
2. **事务懒开**：第一条 `inTx` 语句才 `beginTransaction`；遇到 `inTx:false` 且事务还开着 ⇒ **先 commit**
   再跑 PRAGMA（G6：`PRAGMA foreign_keys` 在事务内是 no-op，写进事务等于没关）。
3. 失败即 `break`：已开事务 ⇒ `rollbackTransaction` + `out.rolledBack=true`，UI 文案区分
   「主体事务已整体回滚（基准侧未留下半成品）」与「已跳过剩余 N 条（该方言 DDL 不能进事务，中间态需自行处理）」。
4. `cancelRef` 在每条语句前检查；`txIdRef` 兜住中途关窗/切连接，`finishAndClose` 再兜一次回滚。

**浏览器真跑**（Go `polydb-server` @127.0.0.1:8080 + vite 5173，两个 SQLite 文件连接 `e4_base` / `e4_target`，
10 条差异 / 8 个相位组）：

| 环节 | 实测结果 |
|---|---|
| 三处入口 | 活动栏图标 tooltip「结构比对与同步向导 (Ctrl+4)」、`Ctrl+4`、库表树右键均能开；后端离线时图标置灰 |
| 步序门禁 | 未选满两侧时「下一步」disabled 且 title 给出原因；候选连接只列同种类、剔 Redis |
| 差异渲染 | 10 条按相位分组、破坏性红标默认不勾，`selected` 初值直接采用服务端回值（7/10） |
| 组守卫 | 直接生成 ⇒ 服务端 400 `表 t 需要整表重建，同表变更必须整组勾选：缺少 t/idx_a/drop_index、t/dropme/drop_column`，前端补「可点『整表一起勾』补齐后重试」；补齐后 9/10（未勾的无关破坏性 `gone//drop_table` 保留） |
| 正向应用 | 12 条全部成功（不可逆 4 / 带告警 3），首尾 PRAGMA 在事务外 |
| 复查 | 只剩 **1** 条（正是未勾选的 `gone//drop_table`）⇒ 收敛断言成立 |
| 回滚 | 预览 13 条（10 条不可逆）；强确认列出全部不可逆语句，**输错名（少一个字符）按钮恒 disabled**；输对 `e4_base` 后代跑 13 条全部成功 |
| 再复查 | 回到 **10** 条 ⇒ 基准侧结构完全退回比对前 |
| 故意失败 | 把 `t.a` 置 NULL 后重跑正向 ⇒「失败 1 条，主体事务已整体回滚」+ 详情 `#3 相位 20 · t — POLYDB_ERR_QUERY_FAILED: constraint failed: NOT NULL constraint failed: polydb_tmp_t.a (1299)`；随后 `sqlite_master` 核对：无 `p2`、无 `polydb_tmp_*`、`gone`/`idx_a`/`dropme` 都在 ⇒ 零中间态 |

**浏览器与代码 review 抓到并修掉的四个缺陷**：

1. `styles.label` 自带 `text-transform: uppercase` ⇒ 强确认提示把连接名显示成 `E4_BASE`，而要输入的
   是 `e4_base`（**一个大小写敏感的确认门给了错的答案**）。名字段单独 `textTransform:'none'`。
2. 生成脚本成功后只清 `error` 没清 `note` ⇒ 上一轮 400 的「整表一起勾」提示残留到 apply 步顶部。
3. 执行分派最初写成「body + `pragmaTail` 一起进事务」，尾 PRAGMA 因此是 no-op（违 G6）⇒ 改成 `steps[]` 模型。
4. `loadSyncState` 原样恢复 `step:'apply'`，但差异条目与脚本只在内存 ⇒ 重载后是一页**空白面板**。
   现在 `review`/`apply` 一律回落 `target`（勾选集本来就不入库，陈旧 id 该让服务端 400）。

**门禁数字（2026-09-26 本机实测）**：`tsc --noEmit` clean；`vitest run` **16 files / 158 passed**
（本批 +17）；`eslint`（7 个 touched 文件）退出 0；`vite build` 成功（仅既有 >500kB 分块警告）。
零 spec 变更 ⇒ 未重跑 `genprotocol`；后端零改动 ⇒ Rust/Go/契约数字沿用 E3（150 Rust 测试、
契约 23 顶层 / 66 子测试）。

**E4 仍未验证（继续诚实披露）**：① 只对 **Go 后端**跑过（Web 同一时刻只连一个后端），Rust server 的
`/api/schema/*` 由 E2/E3 契约对拍锁定，但**向导本身没打过 Rust**；② `pragmas_outside_tx=false` 与
`ddl_in_tx=false` 两支分派**本机没有真库可跑**（SQLite 恒 `true`/`true`），只有 `planExecution` + 纯函数
单测覆盖，契约层也还没对分派各加断言；③ 「取消剩余」在毫秒级的 12 条脚本上来不及点，只有代码路径，
未肉眼验证；④ Redis / 跨种种类的置灰提示是纯函数 + 条件渲染，未用真 Redis 验证；⑤ 组件没有
testing-library 渲染测试（沿用本项目惯例：纯函数层单测 + 浏览器验证）；⑥ 验证实例视口固定 ~565×482，
宽屏视觉排版只做了数值核对（行宽/标签宽）没肉眼比过。

### A.14 E5 落地实况（2026-09-26，GUI + TUI 向导 + 文档，**零 spec 变更**）

**提交序列**（每批独立门禁 + 独立中文提交；代码 7 个提交合计 +4724/−21，文档收口 1 个提交 + 对本附录自身的排版订正）：

| 提交 | 内容 |
|---|---|
| `f48986e` | E5.0 Go 传输层 `SchemaDiff` / `SchemaDDL`（Local + Remote，含 214 行双后端测试） |
| `47c9acf` | E5.1 Go 呈现层纯函数 `pkg/cmpsync`（380 + 单测 297 / 16 份） |
| `6d3d3be` | E5.2 TUI `viewCompare` 三段向导 + 执行编排 + 代跑回滚（`compare.go` 740 + `commands.go` 137 + 按键测试 428） |
| `ed60c08` | E5.3 Rust `Transport` trait 补 `schema_diff` / `schema_ddl`（+ 本机真跑测试 70） |
| `6cf53a7` | E5.4 GUI 呈现层纯函数 `ui-gui/src/cmpsync.rs`（497 / 8 份单测） |
| `1ad03a8` | E5.5 GUI 浮层向导四步 + 执行/回滚（`app.rs` +926、`view.rs` +714、`cmpsync.rs` +66/3 份单测） |
| `d207b1b` | E5.2 补：TUI 的 `i` 忽略默认值（三端口径齐平，+ 空计划下选项键被锁死的修复） |
| `a369675` | E5.6 文档收口：`frontends.md` 新增三端一致一节 + 键位行；本附录与 §3/§6/§7 数字写回 |

（`a369675` 之后另有 2 份小提交，只订正本附录自己的排版与提交计数——表格无法收录自身 SHA，故不列。）

**三端呈现层同构**（规则层仍是唯一一份：`polydb-schema-diff` ↔ `pkg/schemadiff` + 两个服务端端点）：

| 职责 | Web | TUI/GUI 对应 |
|---|---|---|
| 相位名 / 操作名 | `PHASE_LABEL` / `OP_LABEL` | `PhaseLabel`/`OpLabel`（Go）、`phase_label`/`op_label`（Rust） |
| 按相位分组 + 默认勾选 | `groupByPhase` / `defaultSelection` | `GroupByPhase`/`DefaultSelection`、`group_by_phase`/`default_selection` |
| 整表重建缺口 | `groupGaps` / `fillGroupGaps` | `GroupGaps`/`FillGroupGaps`、同名 snake_case |
| 执行分派 | `planExecution` | `PlanExecution` / `plan_execution`（`pragma_head`/`pragma_tail`/`body`/`wrap_in_tx`） |
| 能力位 → 人话 | 组件内联三句 | `cmpExecMode`（Go）/ `exec_mode`（Rust），三句逐字相同 |
| 强确认 | 输入名比对 | `ConfirmMatches` / `confirm_matches`（只剥首尾空白，逐字才算） |
| 列表裁窗 | CSS 滚动 + 行数提示 | `ListWindow` / `list_window` +「↑ 上方还有 N 行」 |

**文案一致性钉在测试里**：Rust `wizard_step_labels_match_the_web_wizard` 断言四步步骤名与 Web
`STEP_LABEL` 逐字相等；Go `TestCompareWizardHelpAndFooter` 断言三段页脚与 F1 键位表含 `i`/`n`/`x`/`r`/`c`
与「逐字输入基准侧连接名」。三端页脚同一条语句的措辞差异视为缺陷，不是风格问题。

**行为级真跑**（Go 侧，真 app-core + Local transport + `:memory:` SQLite 两个连接，绕开一切 mock）：
`TestCompareWizardSyncAndRollback` 把 ①选期望侧 → ②差异勾选（含破坏性默认不勾、7/10 勾选）→ `f` 补齐
重建组 → `g` 生成 → `x` 正向 → `c` 复查收敛 → `r` 强确认（错名不放行）→ 代跑回滚 → 再复查退回原状
整条链路按 keystroke 驱动跑完；`TestApplyScriptSyncDispatch` 直接验两种执行分派（事务内失败整体回滚、
`ddl_in_tx=false` 逐条留下中间态）；`TestCompareScriptViewScrollWindow` 验裁窗；
`TestCompareWizardOptionKeys` 验 `i`/`n` 翻状态即重跑、请求带得上、且空计划改得动。

**GUI 形态定案的实际收益**：浮层 Modal 与 `main` 同级渲染 ⇒ SQL 编辑器的 `on_key_down` 不是它的祖先，
键位天然隔离，不需要给既有 action 前缀加 guard；四步在层内自走 ⇒ 全程不碰 `workspace` 单活动连接状态机，
"临时把活动连接切到期望侧再切回来"这个最危险的想法直接消失。点击行 = 先挪高亮再走该步 Enter 语义
（`cmp_set_cur`），键盘与鼠标共用一份游标。列表按可视行裁窗（连接 12 / 语句 8 / 回滚预览 5）而不是
接 GPUI 滚动 API，保证游标恒在视野内且"上方还有 N 行"文案三端一致。**比对/执行在途时点遮罩不关闭**，
半途失败详情不会被误触丢掉。

**门禁数字（2026-09-26 本机实测，E5 收口基线）**：协议漂移 `go run . check` OK（**64** 个 spec 类型对
Rust/Go 双向核对，E5 零 spec 变更）、`… ts --check` 无漂移；Rust `cargo check` / `clippy --workspace
--all-targets -- -D warnings` / `fmt --all -- --check` 全干净，`cargo test --workspace` **162 绿**
（`polydb_ui_gui` **59** = 阶段 G 原基线 48 + `cmpsync` 11；`polydb_schema_diff` 36、`snapshot_diff` 10）；
Go `go build` / `go vet` 干净，`go test ./...` **189 顶层 + 14 子测试全绿、0 失败**（`internal/tui` 50、
`pkg/cmpsync` 16）；Web `tsc --noEmit` 干净、vitest **158 绿**；契约对拍 **23 顶层 / 66 子测试 PASS，
0 FAIL**（网络方言与 Redis 仍在用例内部 soft-skip：`e2_schema_diff_test.go:227,271`、
`e3_schema_ddl_test.go:327`）。两处环境事实需记录：本机 **无 `make`**（Git Bash 未装），所有门禁按展开后的
直接命令跑；`golangci-lint` v2 仍缺，Go lint 步骤恒跳过。

**E5 未验证项（如实披露）**：① GUI 与 TUI 的**渲染均未肉眼验证**（GPUI 无法无头，TUI 只在测试里断言
字符串产物）；② GUI 向导的**状态机与渲染没有测试**——`cmpsync.rs` 的 11 份纯函数单测覆盖的是可测逻辑，
E5 在 `app.rs` 新增的 926 行按键分派与在 `view.rs` 新增的 714 行浮层只能靠 TUI 侧的同构测试间接背书，
这是 E5 最大的缺口；
③ 非 SQLite 方言三端只能拿到服务端 501，而**该 501 本机从未真正打出过**；④ `pragmas_outside_tx=false` /
`ddl_in_tx=false` 两支分派在 GUI/TUI 同样只有纯函数与 Go 侧 `TestApplyScriptSyncDispatch` 覆盖，
真库（PG/MySQL）跑不到；⑤ ~~TUI 远程模式（打 polydb-server）的向导链路未真跑，只在本机模式测过~~
**已由 E6.2 关闭**（附录 A.17）；
⑥ `golangci-lint` v2 本机仍未安装，Go lint 步骤恒跳过。

### A.15 E6.0 SQLite 视图依赖取证（2026-09-26，关闭 §7 与 A.11 的遗留未测项）

§7 与 A.11 一直挂着一条：**视图对重建表的依赖行为未测**，原假设是"SQLite 视图按名字解析 ⇒ 预期不破"。
E6 选定的范围是「证据补齐 + 还债」，所以先把这条**测出来**再谈要不要改规则。
探测手段升级为一等公民测试：真驱动 + 真快照 + 真 diff + 真生成器 + `:memory:` 引擎，
两端各一份镜像（`go/pkg/dbsqlite/view_rebuild_probe_test.go`、
`rust/crates/db-sqlite/tests/view_rebuild_probe.rs`，各 4 份），不再是一次性 CLI 手跑。

取证场景：`orders(id INTEGER PRIMARY KEY, qty INTEGER NOT NULL DEFAULT 1)` 两行数据，
外加一个依赖它的视图 `v_orders` 与一个触发器 `t_after_ins`；目标侧把 `qty` 换成 `TEXT` ⇒
diff 出一条 `alter_column`，生成器按规则 2 走整表重建（6 条语句、能力位 `pragmas_outside_tx=true`
`ddl_in_tx=true`，与 A.11 完全一致）。

| # | 实测结论 | 证据 | 对生成器的约束 |
|---|---|---|---|
| G13 | **原假设是错的**：依赖视图不会"静默破掉"，而是**让重建直接失败**，失败点在 `RENAME` | `DROP TABLE "orders"` 返回 **OK**（SQLite 的 `DROP` 不做依赖检查），下一条 `ALTER TABLE "polydb_tmp_orders" RENAME TO "orders"` 才报错——Go `SQL logic error: error in view v_orders: no such table: main.orders (1)`、Rust `driver error: execute failed: error in view v_orders: no such table: main.orders`。`RENAME` 会重解析引用该名字的视图，而此时原表刚被 DROP。混淆项已排除：A.11 G4 单独放触发器时重建是**成功**的，且错误文案点名 `v_orders` ⇒ 挡路的是视图不是触发器 | 快照里没有视图 ⇒ 生成器**无从预判**这类失败；不能把"视图不参与比对"当成"视图不影响执行" |
| G14 | **逐条分支（等价 `ddl_in_tx=false`）失败后的中间态是丢表** | 同一脚本按顺序跑完 6 条（探测刻意为之，不代表向导是否遇错即停）：`SELECT count(*) FROM orders` ⇒ `no such table`，`polydb_tmp_orders` ⇒ **2 行**（数据困在临时表），`sqlite_master WHERE type='trigger'` ⇒ **0**（随 `DROP` 一起没了），而尾部 `PRAGMA foreign_keys=ON` 仍 OK | SQLite 恒 `ddl_in_tx=true` ⇒ 本分支实际踩不到；但**任何 `ddl_in_tx=false` 的方言复用这套重建算法前，必须先补依赖检查**，否则一次失败就把用户表删了 |
| G15 | **事务分支把这一切干净兜住**（SQLite 真实的那一分派经失败路径验证有效） | 跳过 phase 0/255 两条 PRAGMA（G6：它们本就在事务外）⇒ `BEGIN` → 主体在 `RENAME` 失败 → `ROLLBACK`：`orders` ⇒ 2 行、`v_orders` ⇒ 2 行（视图还能查）、`typeof(qty)` ⇒ `"integer"`（旧定义原封不动）、`polydb_tmp_orders` ⇒ `no such table`（无残留）。两端同结论 | 「首尾 PRAGMA 事务外 + 主体单事务」不只是成功路径上成立，**失败路径上同样成立** ⇒ §7 里"`/api/transactions` 那条支路没验证过"的担忧，在视图失败这一具体形状上已有证据 |
| G16 | 真正**静默**留下破视图的是 `drop_table`，不是重建 | 目标侧去掉 `orders` ⇒ 只有一条 `DROP TABLE "orders";`，全程无错；`sqlite_master WHERE type='view'` 仍为 **1**（条目留着），一查 `SELECT * FROM v_orders` 才报 `no such table`；该语句的 `warnings` 实测为**空** | 与 G4「重建静默丢触发器」同一类：快照不含视图 ⇒ 无从预警。是否给 `drop_table` 补一条"可能留下依赖它的视图"警告属**产品决策**，本附录只钉事实 |
| G17 | 临时名去重集**看不见视图** ⇒ 库里已有同名视图时脚本一开局就撞车，且是**良性失败** | 源侧预置 `CREATE VIEW polydb_tmp_orders AS SELECT 1 AS one`：生成器仍按 `orders` 算出临时名 `polydb_tmp_orders`，首条 `CREATE TABLE "polydb_tmp_orders"(…)` ⇒ Go `view "polydb_tmp_orders" already exists` / Rust `prepare failed: view "polydb_tmp_orders" already exists`。此时**什么都没被改动** | 结构性原因（三处，全部实测有效；本表按 E6.0 当时的代码状态记录，故只给**符号名**不给行号，以免随手腐烂）：① 采集层过滤非表项 `app-core` 的 `collect_snapshot` / `pkg/appcore` 的 `collectSnapshot`；② 去重集只从表快照播种（`schema-diff/src/ddl.rs` 与 `pkg/schemadiff/ddl.go` 的 `emit_script` 里 `taken` 只吃 `table_map` 的键）；③ 即便放进快照，diff 层也会跳过视图（`diff()` 只遍历 `table_map`）⇒ 当时判断"只需去掉 ①，DTO 与线路零变更"。**E6.5 已按 A 档落地并订正该判断：去掉 ① 不够**——`table_map` 本身就按用途把非表项跳掉了，`taken` 从它播种 ⇒ 必须再写一次 `view_names()` 单独取名播种（附录 A.20） |

**本批没有改动的东西（刻意为之）**：零 spec 变更、零生成器规则变更、零文案变更。
G13/G16/G17 各自暗示一种规则收紧（重建前依赖检查、`drop_table` 警告、临时名去重集纳入视图名），
但那是"证据 → 产品行为"的一步，需先拍板 ⇒ 已立为 **§8-8**，落在 E6.5（`polydb_tmp_` 冲突与视图可见性的处置）。
在此之前，`spec/behavior.md` §14.1「视图与物化视图整体跳过」与 §14.3「不猜语义，执行期由数据库报错兜底」
继续按原文执行——A.15 提供的正是这两条口径**在真实引擎上的代价账单**。

> **后续状态（E6.6 收口时回填，避免本附录被读成"至今未改"）**：§8-8 已拍板 **A 档**并由 E6.5 落地
> （附录 A.20）——G17 由"`taken` 纳入视图名"消除，G16/G13 由两条粗粒度视图警告开口，
> §14.1 口径同步改写为"视图名进快照、不参与比对、不发元数据查询"。
> **B 档（依赖可见）仍不做**，故本表 G13/G16 那句"生成器**无从预判**"在"判不出具体哪一个"这个意义上依然成立。
> G14 那句"探测刻意为之，不代表向导是否遇错即停"由 E6.1 的应用环路断言接管（附录 A.16），
> 且能力位口径同时被订正为 `pragmas_outside_tx = 任一脚本含重建`（不是恒真）。

**门禁（本批）**：Go `go build ./... && go vet ./...` 干净、`go test ./...` 全量 **0 失败**
（`pkg/dbsqlite` **18** 份顶层，含新 4 份）；Rust `cargo test -p polydb-db-sqlite` **21 绿**
（lib 7 + `snapshot_diff` 10 + `view_rebuild_probe` 4）、
`clippy -p polydb-db-sqlite --all-targets -- -D warnings` 退出 0、`cargo fmt --check` 干净；
新 Go 文件剥 CR 后 `gofmt -l` 无输出。**本批未跑**：Rust workspace 全量、契约对拍、Web 检查
（改动面只有两份测试 + 本文档，零接线变更），全量基线数字在 E6.6 收口时统一刷新。
> 上面是 E6.0 **当时**的门禁口径，原样保留作证据链；两处后续：探测随后由 E6.5 扩到 **5** 份
> （`view_rebuild_probe` 4 → 5、`polydb-db-sqlite` 21 → 22 绿、Go `pkg/dbsqlite` 18 → 19），
> 全量基线已在 E6.6 刷新 ⇒ 附录 A.21。

**A.15 未覆盖（如实披露）**：① 只测了 SQLite 两端 bundled 引擎（modernc 3.53.4 / rusqlite 3.46.0），
PG/MySQL 等的 `RENAME`/依赖检查语义**仍无证据**；② 未测**视图链**（视图套视图）、物化视图
（SQLite 无此对象）、以及视图跨 schema 的情形；③ 未测"目标侧也存在同名视图"时的行为（只测了源侧）；
④ G14 是探测脚本按序跑完的结果，**向导实际的遇错即停策略**由 E6.1 的应用环路测试另行钉死。

### A.16 E6.1 执行分派的应用环路（2026-09-26，Web 编排抽层 + 两端调用序列断言）

A.15 的 G14 留下一句话没人接：`ddl_in_tx=false` 那一支失败后"数据困在临时表"是**探测脚本**跑出来的，
向导真跑时到底停在哪、事务到底包住了什么，此前只被"计数对得上"级别的断言覆盖。本批把它补齐。

**先订正一处事实错误（§7 原话）**：`pragmas_outside_tx` 是**「任一脚本含重建」**
（`go/pkg/schemadiff/ddl.go:793`），不是恒真——SQLite 上不含重建的脚本（纯 `add_column`、`drop_index`）
该位就是 `false` ⇒ **"整份脚本包进一个事务"是 SQLite 最常见的那一支**，
旧文本"SQLite 两端能力位恒为 `true`，所以另一支验证不到"在以重建为中心的契约场景里成立、在一般脚本上不成立。

**落点**：

| 端 | 改动 | 新增证据 |
|---|---|---|
| Go | 零生产代码改动（`applyScriptSync` 原样） | `internal/tui/apply_dispatch_test.go` **4 份**：真 `:memory:` SQLite + 真实 Local 客户端外套一层**调用录音**（`recorder` 包装 `transport.Client`，只记序列、其余透传，不 mock 数据库） |
| Web | `CompareSyncWizard.tsx` 的 `applyScript` 里那段分派+环路（85 行）**下沉**为 `lib/compareSync.ts` 的 `runScript(sc, connId, deps)`，组件只留接线（`setProgress` / `cancelRef` / `txIdRef` ↔ `ApplyDeps` 七个注入点） | `compareSync.test.ts` 新增 `describe('runScript')` **5 份**：假引擎带一本"账"（事务内语句先进 `pending`，`commit` 才落 `db`，`rollback` 丢掉 `pending`）⇒ 落地与否可断言，不是只看 `ran` |
| Rust | 本批未动（环路仍在 `ui-gui/src/app.rs` 的 `self.io` 异步闭包里，是同一逻辑的第三份拷贝） | —— E6.3 已抽层并按同一本账补断言（附录 A.19） |

**四类断言（Go 与 Web 逐条同构，只有"库"是真的还是假的区别）**：

| 分派 | 调用序列 | 落地状态 |
|---|---|---|
| `ddl_in_tx=true` + `pragmas_outside_tx=false`，第 2 条炸 | `begin \| tx:1 \| tx:2 \| rollback` | `ran=1` 但 `sqlite_master` 里**一条都没有**（撤销了已执行语句） |
| `ddl_in_tx=false`，第 2 条炸 | `exec:1 \| exec:2`，**一次 `begin` 都没有** | 第 1 条**必须还在库里**（G14 的中间态形状） |
| `ddl_in_tx=true` + `pragmas_outside_tx=true` | `exec:PRAGMA OFF \| begin \| tx:… \| commit \| exec:PRAGMA ON`，`begin` 恰好 1 次 | 四条全落地 |
| 取消 / 回滚方向 | 取消：停在该条之前并整体 `rollback`；回滚方向跑的是 `statements ← rollback` 那份，`CREATE` 一条不得再出现 | 账上不留半成品 / 表真被删掉 |

**唯一一处有意的行为差异**（Web 重构时不是纯搬运）：提交失败时**保留** `txIdRef`，
让 `finishAndClose` 关窗前再兜一次回滚——原实现无条件清空，等于提交报错后放任服务端事务挂到超时。
方向是更严，不是等价；记在这里以免日后被当成等价搬运。

**门禁（本批）**：Go `go build` / `go vet` 干净、`go test ./internal/tui/` 绿（新增 4 份，
`TestApplyScriptSync*` 共 5 份同跑）；Web `tsc --noEmit` 干净、vitest **163 绿**（158 → +5）、
`eslint` 改动三文件无输出。Rust 侧零改动故未重跑。

**A.16 未覆盖（如实披露）**：① **契约层**（走 HTTP `/api/transactions` 的两种分派各一条断言）
—— **E6.2 已关闭一半**：单事务那一支现在有端到端证据（A.17）；`ddl_in_tx=false` 那一支 SQLite 恒不出现，
在 SQLite 上**结构上不可能**经 HTTP 跑到，只能等非 SQLite 方言；② Web 的 `runScript` 只验过编排，**没有真发过 HTTP**（真线路由 E5.0 的双后端传输测试背书）；
③ 提交阶段本身失败（`commit` 抛错 ⇒ 记失败但不回滚）这条分支两端都还没测；
④ Rust GUI 的第三份拷贝仍无测试 —— **A.19 已关闭**（抽层 + 4 份同构断言，并顺带抓到一支"从不回滚"的分歧）。

### A.17 E6.2 TUI 远程模式向导端到端真跑（2026-09-26，**零生产代码改动**）

§7 的"E5 之后仍缺的两类证据"第 ② 条（TUI 向导只在本机模式跑过）到此关闭。
本批**没有改任何生产代码**——只是把 E5.2 那套按键脚本原封不动地换一个客户端跑：

| 位置 | 内容 |
|---|---|
| `go/internal/tui/compare_remote_test.go` | 2 份测试。服务端 = `httptest.NewServer(server.NewWithToken(app, token).Handler())`（与契约测试同一套装配），客户端 = `transport.NewRemote(srv.URL)` + `SetToken`；TUI 侧仍是 `New(rem)` 按键驱动，`openTablesRemote` 与本机版 `openTables` **只差客户端** |
| 请求计数器 | `countingHandler` 包一层，记录 `METHOD + URL.Path`，用来**证明流量真的经过服务端**，而不是测试自己走进程内直调 |

**打到的路径**（每一步都有对应的端点计数断言）：
连接列表/打开连接 → `POST /api/schema/diff` → 勾选/补缺口 → `POST /api/schema/ddl`
→ `x` 执行 ⇒ `POST /api/transactions` + `…/{txnId}/execute`（≥3 次）+ `commit`
→ `r` 代跑回滚（确认词经输入框逐字敲进去）⇒ 同一组事务端点。
两侧结构状态用 `remoteDiff` / `tableExistsRemote`（都走 HTTP）回读，不用本地对象。

**三条顺带钉住的口径**：
1. **鉴权前置断言**：不带 token 的客户端必须先被 `POLYDB_ERR_UNAUTHORIZED` 挡下——否则后面所有
   "经过服务端"的断言都是空的，所以这一条放在最前面，失败即整测失败。
2. **能力位穿过 msgpack 不丢**：`ddl_in_tx` / `pragmas_outside_tx` 在反序列化后仍为真。
3. **未整组勾选由服务端拒**：绕开界面提示直接 `rem.SchemaDDL(单条)` 必须报错——
   呈现层挡一次不算数，判定在服务端。

**顺带纠正一处我自己的误判**：一开始断言"同步后 `u` 该被删掉"，跑出来是红的——
因为破坏性条目默认不勾选，`u//drop_table` 本来就该留在差异里。改成断言 **`u` 必须还在**，
这条反而变成了"默认勾选口径一路穿过 HTTP 到服务端重算都没走样"的证据。

**门禁（本批）**：`go build ./... && go vet ./...` 干净；`go test ./internal/tui/` 绿
（新增 2 份，含 E6.1 的 4 份派发探针）；新文件剥 CR 后 `gofmt -l` 无输出。

**A.17 未覆盖（如实披露）**：① 服务端是 **Go**（httptest 里的 `pkg/server`），Rust `polydb-server`
的 HTTP 路径由 E2/E3 契约对拍锁定，本批没有把 TUI 打到 Rust 服务端上；② `ddl_in_tx=false` 那一支
在 SQLite 上结构上不会出现，因此"经 HTTP 的逐条分派"仍无端到端证据（同 A.16 ①）；
③ 只测了 SQLite 同种类候选；PG/MySQL 的远程向导仍只有契约层 soft-skip；
④ 取消（F8）在远程向导路径上未测——DDL 编排用的是 `context.Background()`，本就不走取消注册。

### A.18 E6.4 快照采集扇出量化 + 「200 表上限」事实订正（2026-09-26，**零生产代码改动**）

§8-5 与 §2 都写着"GUI 外键扫描上限 200 表，diff 动辄上千表，要不要放宽/分页"。
本批把它拆开验证，结论是**问题问错了对象**：

| 说法 | 实测 |
|---|---|
| "比对路径有 200 表上限" | **错**。`.take(200)` 只在 `rust/crates/ui-gui/src/app.rs` 的 `load_incoming`（表页"关系"轨道列入向外键扫描，E6.3 抽层后在 `:2576`），比对路径两端 `collect_snapshot` 都没有任何截断 |
| "diff 需要放宽/分页" | **不需要**（SQLite 内嵌引擎上，见下面数字）；改代码前先把量级测清楚，本批的产出就是这几个数字 |
| 真实代价 | 每表 **3 次串行元数据查询**（`list_columns` / `list_indexes` / `list_foreign_keys`）+ 每次采集 1 次 `list_tables` ⇒ 单次 `schema_diff` 是 **2×(1+3n)** 次查询 |

**Go 侧精确计数**（`go/pkg/appcore/snapshot_fanout_test.go`，包内测试 ⇒ 把已打开连接的驱动换成
计数装饰器 `dbcore.NewConnection(fanCounter)`，不 mock 数据库）：
n=12 表 + 5 张视图 ⇒ `ListTables` **1 次**、`ListColumns`/`ListIndexes`/`ListForeignKeys` **各 12 次**，
总计 `1+3n = 37`，**视图贡献 0 次**（§14.1 "快照看不见视图"在调用次数上的形状）。
n=1000 ⇒ 快照 `len(Tables)==1000`，无截断。

**耗时（本机 i7-8750H，`go test -bench -benchtime 50x`）**：

| n 表 | 单侧快照 | 元数据查询 | 每表 |
|---|---|---|---|
| 50 | 1.20 ms | 151 | 24 µs |
| 200 | 5.92 ms | 601 | 30 µs |
| 1000 | 25.3 ms | 3001 | 25 µs |

**Rust 侧**（`rust/crates/app-core/tests/snapshot_fanout.rs`，dev 构建 ⇒ 数字偏大）：
两侧各 1000 表的一次 `schema_diff`（= 6002 次元数据查询）**138–144 ms**（两次跑动各有出入，
同一量级），n=50/200/1000 三档每表约 **149–201 / 149–174 / 132–149 µs** ⇒ 线性、无悬崖。
"没上限"这一侧用**末号表可见性**证明：两侧各建 1000 表后在期望侧 `DROP TABLE t999`，
差异必须**恰好 1 条**且是 `t999/drop_table`——若快照被 200 截断，第 1000 号表两边都看不见，diff 会是空的。

**两端不对称（如实披露）**：Go 在包内能注入驱动，"每表恰好 3 次"是**精确断言**；
Rust 的 `connections` 私有、没有注入驱动的门 ⇒ 那一侧只有**耗时 + 末号表可见性**两种证据，
调用次数靠读代码同形（同一串行 `for` 循环 + `.await`）。

**结论落点**：不改代码。若日后接网络数据库（PG/MySQL 经 WLAN），这 3 次/表会变成 3 次网络往返，
届时该做的是**驱动层批量元数据查询**（一次 `information_schema` 查全部表）而不是"提高 200 上限"。
本批数字只覆盖 SQLite 内嵌引擎，网络方言未测（本机无实例）。

**门禁（本批）**：Go `go build ./... && go vet ./...` 干净、`go test ./pkg/appcore/` 绿（新增 2 份 + 1 组三档基准）；
Rust `cargo test -p polydb-app-core --test snapshot_fanout` **2 绿**。

### A.19 E6.3 GUI 向导状态机抽纯层 + keystroke 级测试（2026-09-26，**顺带修掉两处缺陷，其一为双实现分歧**）

A.16 ④ 那条"Rust GUI 的第三份拷贝仍无测试"到此关闭。这一步是 E6 里唯一一处**必须动 GUI 生产代码**的取证：
向导的四步状态机长在 `app.rs` 的 GPUI 实体里，执行分派长在 `cmp_apply` 的 `self.io` async 闭包里，
两处都没法按键、也没法看调用序列。

**落点**：

| 位置 | 内容 |
|---|---|
| `ui-gui/src/cmpsync.rs`（563 → 1610 行，含新增测试） | 搬入向导态与全部纯转移：`CmpStep` / `CompareState`（含 20 个方法：`goto_target`、`finish_diff`、`review_status`、`toggle_at`、`select_all`、`fill_gaps`、`toggle_option`、`ddl_request`、`finish_script`、`script_to_run`、`begin_apply`、`finish_apply`、`start_confirm`、`insert/backspace_confirm`、`back`、`footer` …）；新增按键语义表 `CmpAction` + `key_action(step, loading, key)`；新增分派环路 `SyncExec`（5 个动作）+ `run_script(script, rollback, ex)`。**本层不出现任何 gpui 类型** |
| `ui-gui/src/app.rs`（4215 → 3802 行） | 只剩接线：`on_cmp_key` 解 gpui 事件 → `cmpsync::key_action` → `cmp_do_action`；`TransportExec` 把那 5 个动作落到 `dyn Transport`；发 IO、`cx.notify()`、把纯层返回的文案写进 `status`。`pub(crate) use crate::cmpsync::{CmpStep, CompareState}` 让旧路径不变 |
| `ui-gui/Cargo.toml` | deps `+ async-trait`；dev-deps `+ polydb-app-core / polydb-storage / smol`——**只在测试里**造真引擎，产品代码路径仍然只依赖 transport（AGENTS.md 第 1 条红线不破） |

**新增 6 份测试**（`cmpsync.rs` 的 `mod tests`，ui-gui **59 → 65 绿**）：

| 测试 | 证据 |
|---|---|
| `whole_tx_branch_rolls_back_landed_statements` | `pragmas_outside_tx=false` 那一支：序列必须是 `begin \| tx:… \| tx:… \| rollback`，且回读 `sqlite_master` 里 `ran=1` 的那张表**必须不存在** |
| `non_tx_branch_leaves_intermediate_state` | `ddl_in_tx=false`：一次 `begin` 都不许有，且已落地语句**必须还在库里**（与上一份互斥） |
| `keeps_head_and_tail_pragmas_outside_the_tx` | 首尾 PRAGMA 走 `exec:`、主体两条共享**恰好一个**事务、表真建出来 |
| `rollback_direction_reuses_the_same_dispatch` | `rollback=true` 跑的是 `script.rollback` 那一份，能力位共用、表真被删掉 |
| `key_action_table_matches_the_other_two_frontends` | 四步 × 每个键逐条钉住：`up/k`·`down/j`·`i/n`·`escape`（① 步关窗、其余后退）·`backspace`，④ 步 `x/r/c/g` 带 loading 门禁、其余步不带 |
| `wizard_walkthrough_by_keystrokes` | 整条向导用纯转移走一遍：① Enter → ② 选项（不重跑）→ 计划回来 → 缺口文案 → `f` 补齐 → `A`/`a` 勾选口径 → `ddl_request` 的 `selected` 顺序 → ④ 步文案 → `r` 未跑正向被挡 → `x` 成功 → 确认框逐字输入/退格/取消 → `back()` 逐级返回（只有 ②→① 丢期望侧） |

**证据等级与 Go 对齐、与 Web 不对齐**（如实记）：Rust 这一份是**真 `:memory:` SQLite + 真 `LocalTransport`**
外面套 `Recorder`（只记序列、其余透传，不 mock 数据库），与 A.16 的 Go 侧同一等级；
Web 只能记假账本（浏览器里没有真引擎）。`ddl_in_tx=false` 那一支仍是**测试手工构造的脚本**给的 false——
SQLite 服务端不会这么给，两端同此局限（A.16 已记）。

**测试跑出来的两处缺陷（不是设计时想到的）**：

1. **双实现行为分歧，已修**：GUI 的 `fail` 闭包带一个 `*t = None`，把事务句柄连同失败一起清掉
   ⇒ 循环末尾 `if let Some(tx) = txn` 永假 ⇒ "主体包进事务"那一支**从不发 ROLLBACK**，
   `rolled_back` 恒 `false`，已落地语句留在库里、服务端事务挂到连接关闭。Go `applyScriptSync` 与 Web `runScript`
   都会回滚——即三端里只有 Rust GUI 走样。修法：`fail` 不再收 `&mut Option<String>`，与 Go 同形
   （提交失败那条分支由 `txn.take()` 天然清掉句柄，行为不变）。这条正是第一条测试的序列断言抓到的。
2. **GUI 三处文本输入退格失效，已修**：`char_floor(s, cursor)` 在光标恰落在 char 边界时**原地不动**
   （`is_char_boundary(len)` 恒真），于是 `replace_range(cursor..cursor, "")` 删不到任何字符——
   ASCII 文本下光标永远在边界上 ⇒ 退格键等于没键。命中：向导回滚确认框、SQL 编辑器 `backspace()`、
   Quick Switcher `quick_line_backspace()`。修法：一律 `char_floor(s, cursor - 1)`，与 `util::edit_line` 一直用的口径一致。
   **只有向导那处有按键级测试**；另两处挂在 `PolyDBApp` 实体上（要 gpui 窗口才能测），本批只靠
   `util.rs` 里新增的 3 条 `char_floor` 边界断言钉口径，**未肉眼验证**。
   这条与历次里程碑记忆里反复出现的"GUI 渲染/交互未肉眼验证"是同一个坑的兑现。

**有意的不等价**（除上面两处修正外本批是纯搬运，中文文案逐字未动）：
`cmp_toggle_entry` / `cmp_select_all` / `cmp_fill_gaps` 现在在向导未打开时也会走到 `cx.notify()`（渲染空转），
原来这三处在更早处就返回了。不影响可见行为，记下来以免日后被当成等价搬运。

**门禁（本批）**：`cargo test -p polydb-ui-gui` **65 绿**（+6）、
`cargo clippy -p polydb-ui-gui --all-targets -- -D warnings` 与 `cargo clippy --workspace --all-targets -- -D warnings` 均零警告、
`cargo fmt --all -- --check` 干净（写模式仍按项目约定手改）、`cargo check --workspace` 干净。
Go / Web 零改动，故未重跑（E6.6 统一刷新基线 ⇒ 已刷新，见附录 A.21）。

**A.19 未覆盖（如实披露）**：① `on_cmp_key` 里 gpui 事件 → `key_action` 之间那一小段（`key_char` 取字、
ctrl/platform + `4` 关窗）仍无测试，纯层与实体之间的接缝靠通读；② GUI 向导没有"把按键灌进真窗口跑一遍"的端到端
（GPUI 缺测试基建），故 GUI 侧证据链是"纯状态机 + 真引擎分派"两级，比 TUI（A.17 有整条按键脚本 + 远程真跑）**短一节**；
③ 第 2 处缺陷的另两个输入框修正无测试；④ `dd_request` 的 `Err(None)`（在途静默）只有代码路径、无断言。

### A.20 E6.5 视图可见性落地（2026-09-26，**A 档：名字进快照 + 粗粒度警告**，关闭 §8-8 与 A.15 的 G13/G16/G17）

A.15 只钉事实、不改行为，把三处盲区留给决策；§8-8 拍板为 A 档后本批落地。
一句话：**快照从"看不见视图"改成"只看见名字"**，于是临时名不再撞车（G17 消除），
删表与重建各挂一条**点名视图**的粗粒度警告（G16/G13 从"事后报错"变成"事前有话"），
而 diff 规则、DTO 线路、三端呈现**零改动**。B 档（携带视图定义文本 + 文本扫描猜依赖）明确不做。

**落点**：

| 位置 | 内容 |
|---|---|
| `app-core/src/lib.rs` ⇄ `pkg/appcore/schema_diff.go` 的 `collect_snapshot` / `collectSnapshot`（采集层） | 不再丢弃视图/物化视图：条目进快照，只带 `name` + `table_type`，列/索引/外键一律为空。**关键是仍不发元数据查询** ⇒ A.18 的每表 3 次、总 `1+3n` 扇出口径分毫未动（`pkg/appcore/snapshot_fanout_test.go` ⇄ `crates/app-core/tests/snapshot_fanout.rs` 把这条钉住了） |
| `schema-diff/src/lib.rs` ⇄ `pkg/schemadiff/schemadiff.go` | 新增 `view_names()` / `viewNames()`：按**小写键**升序、大小写不敏感去重（首个胜出，与 `table_map` 的 `or_insert` 同口径）。diff 规则本身仍跳过非表项 ⇒ 计划零变化 |
| `schema-diff/src/ddl.rs` ⇄ `pkg/schemadiff/ddl.go` | `emit_script` 里 `taken` 除了两侧表名还播种**两侧视图名**（G17）；`view_scope()`（≤5 点名、超出「等」收尾）+ `w_view_drop()` / `w_view_rebuild()`；警告只取**被执行那一侧**的视图 ⇒ 回滚脚本按回滚真正要动的库出话 |
| 参数收口（**双端同形**） | `rebuild_statements` / `rebuildStatements` 的 `taken + views` 合成一个 `Seen` / `seen` 结构体传：两者同源于同一对快照，且顺手把 clippy 的 `too_many_arguments`（8 参）摘掉 |
| 三端呈现 | **零改动**。`warnings` 一直是字符串数组，Web/GUI/TUI 各自渲染循环不变——这也是 A 档代价小的原因 |
| `spec/behavior.md` | §14.1 口径改写（视图名进快照、不参与比对、不发元数据查询）；§15.3 临时名避让范围明确覆盖**表名与视图名**并引 G17；§15.8 新增两条动态告警文案表 + 「告警按被执行的那一侧出」；§15.10 语料计数改 26（18 脚本 + 8 错误）并把两条视图告警子串纳入覆盖断言 |

**两条新警告的措辞依据**（不是套话，逐字对应 A.15 的实测失败点）：

| 场景 | 文案（`{}` 处填 `N 个视图（a, b 等）`） | 依据 |
|---|---|---|
| `drop_table` | `本库现有 {}：DROP TABLE 不检查依赖，语句会成功，但引用它的视图将失效（快照不含视图定义，判定不了是哪一个）` | G16：DROP 全程无错、`sqlite_master` 里视图条目留着、一查才 `no such table` |
| 重建组首条 | `本库现有 {}：重建经 RENAME，而 SQLite 改名时会重解析依赖该表的视图 ⇒ 有视图引用它就整条重建报错` | G13：`DROP` 返回 OK，报错在 `ALTER TABLE … RENAME`，文案点名 `v_orders` |
| `drop_column` | **不挂警告** | §14.3「不猜语义、执行期报错兜底」：引擎**自己会拦**（新增探测 `drop_column_referenced_by_view_is_blocked_by_engine` 把 SQLite 原文钉进测试），与"引擎静默放过"的 DROP TABLE 是两类，故警告不对称是有意的 |

**测试面**（A 档之所以能只改规则层：每条分支都有对拍锚）：

| 层 | 新增 |
|---|---|
| 生成器单测（双端逐字镜像，各 4 份） | `view_names` 排序/去重口径、`view_scope` 5 个上限、**被执行侧**才出警告（源侧预置视图名 `polydb_tmp_t` ⇒ 临时名让位为 `polydb_tmp_t_2`，回滚方向不带视图警告）、无视图 ⇒ 全程无视图警告 |
| 共享语料 +3 份 | `d24_view_blocks_tmp_name`（撞名让位）、`d25_drop_table_warns_about_views`（名单升序 + 回滚无警告）、`d26_view_warning_list_truncated`（7 个视图、`V_5`/`v_5` 去重 ⇒ 6 个、只点前 5、以「等」收尾）。总计 **26 = 18 脚本 + 8 错误** |
| 真引擎探测（双端各 **5** 份，A.15 的 4 份 + 本批 1 份） | 探测从"一次性观察"升成回归断言：G16 留破视图（并断言语句**带**视图警告）、G17 临时名避让（并断言改名生效）、新增第 5 份把 SQLite 拦截 `drop_column` 的原文钉住 |
| 覆盖门禁 | `ddl_vectors` 双端在既有断言外，追加要求语料里必须出现含 `DROP TABLE 不检查依赖` 与 `重建经 RENAME` 的警告——用**子串**而不是导出新的 `pub` 格式化函数，避免 API 增量，仍是两端都断言 |

**门禁（本批）**：Rust `cargo test --workspace` **0 失败**（`polydb-schema-diff` lib **40** 绿，原 36 +4；`polydb-db-sqlite` **22** 绿 = lib 7 + `snapshot_diff` 10 + `view_rebuild_probe` 5；`ui-gui` 65 绿未动）、
`cargo clippy --workspace --all-targets -- -D warnings` 零警告、`cargo fmt --all -- --check` 干净（写模式仍按项目约定手改）；
Go `go build ./... && go vet ./...` 干净、`go test ./...` 全量 ok（`pkg/schemadiff` **39** 顶层 PASS、`pkg/dbsqlite` **19**）、本批 8 个 Go 文件剥 CR 后 `gofmt -l` 无输出；
共享语料 **26** 份双端各自全绿（`cargo test -p polydb-schema-diff --test ddl_vectors`、`go test -run DDLVector ./pkg/schemadiff/`）；
`check-protocol` 侧 `go run . ts --check` ⇒ TS 类型最新、`go run . check` ⇒ **64 个 spec 类型**与 Rust/Go 双向一致
（快照 DTO 是内部件、不在 spec 里，故本批 spec 类型数不变）；
**契约对拍全绿**：Rust server 重建后 `cd test/contract && go test -v ./...` ⇒ **89 PASS（含子测试）/ 0 FAIL / 0 SKIP 标记**，
PG/MySQL/MSSQL/Oracle/Redis 按 `connected:false` 软跳过（本机无服务，与历次基线同一口径）。

**A.20 未覆盖（如实披露）**：① **B 档那部分依赖判定依然没有**——警告只说"本库有这些视图"，不判定谁引用了被改的表，
用户仍可能收到一条与本表无关的警告（这是 A 档明示的代价，不是疏漏）；② 只测 SQLite：PG/MySQL 等方言的
`RENAME` 依赖语义、视图目录可见性**仍无证据**（A.15 未覆盖 ① 原样继承）；③ 视图套视图（依赖链）、物化视图、
跨 schema 视图未测；④ 前端三端**没有肉眼验证**过新警告的换行/截断渲染（文案变长到近 90 字，窄面板下靠通读）；
⑤ 触发器仍在盲区里（G4 静默丢触发器未纳入 A 档范围，快照不含触发器）。

### A.21 E6.6 收口：文档订正 + 全端门禁基线刷新（2026-09-26，**零代码改动**）

E6 的最后一批不做新功能，只做两件**容易被跳过、跳过就会烂**的事：把前面五批留下的
"待 E6.6 刷新"全部兑现，并把文档里已经**指向不到**的行号引用清掉。
本批净账：主体提交 **`T0..T1` = +123 / −13（2 文件）**，其后的补账提交另记——
区间端点、口径与两口径不等的实测例证见本节末「本批数字的测量区间」。

**文档订正落点（这一批唯一的"改动"）**：

| 处 | 内容 |
|---|---|
| 文档头部状态行 | 补 E6 已收口，并点明**整个 E6 只有一处规则变更**（§8-8 A 档 ⇒ A.20），其余是证据与测试，零 spec 变更 |
| §6 分批交付 | 补 E6 条目：六批的顺序、各自附录号、以及"证据等级分三档并逐处标明"的口径 |
| §7 清单 | 契约数字刷新（23 顶层 / 89 含子测试 / 0 FAIL / 0 SKIP 标记，并说明与旧记「66」的差值来源）；① GUI 那一条改为"E6.3 已补两级、只剩真窗口一级"；**新增一条收敛后的缺口清单**（7 条，逐条都是结构性缺，不是"还没写"），并规定以后补上要回来划掉、不要在别处另立口径 |
| A.15 | G17 的"三处结构性原因"去掉 6 个行号引用、改为符号名，并**回填后续状态**（§8-8 已拍 A 档、G14 由 A.16 接管、能力位口径被 A.16 订正）；门禁段保留 E6.0 当时的口径原样，另注明探测 4 → 5 份 |
| A.16 | 表里 "Rust 未动、环路仍在 `app.rs:3335`" → 符号名 + 指向 A.19 |
| §2 现状盘点 | 三处 E6 之后已移位的行号（`ui-gui/src/app.rs` 两处、`internal/tui/commands.go` 一处）改为**函数名** |
| A.19 | "E6.6 统一刷新基线" → 已刷新，指向本附录 |
| `docs/frontends.md` | 「三端一致」节新增一条**语句警告只有一份真相**（三端各自如何渲染同一个 `warnings` 数组，故 E6.5 两条动态警告零前端改动）；「已知未验证项」里 E5 那句"GUI 向导状态机与渲染没有 GUI 端测试"划掉并改为 E6.3 后的实况（只剩真窗口端到端一级缺），另加一条"E6.5 两条视图警告未肉眼验证" |

**为什么把行号换成符号名**：E6 期间 `ui-gui/src/app.rs` 因抽层掉了 400 余行、`pkg/schemadiff/ddl.go`
因视图那批长了 40 余行。逐条核对正文里的 **12 处行号引用**（各取其首行看内容）：
**9 处已完全指向不到原意**——`app.rs:3335`（A.16 指的"`self.io` 闭包"）现在是一行**空行**、
`app.rs:2625`（§2 指的整 schema FK 扫描）是一句 `return;`、`pkg/schemadiff/ddl.go:793`
（§7 与 A.16 各指过一次的能力位赋值）是一个 `}`、`internal/tui/commands.go:211` 落到别的函数里、
A.15 G17 那六个引用里 ①② 四处（采集层过滤那行**已被 E6.5 删掉**、`taken` 播种两处都偏）；
1 处区间仍大致覆盖（`app.rs:2133-2179` 恰好还包得住 `commit_changes`）；
2 处偏到相邻符号（③ 的"diff 层跳过视图"落在 `is_view` / `isView` 上，差 7 行）。
这不是排版问题：文档是本模块的**取证台账**，指向错行等于伪造证据。
故按 `06906c2` 已立的约定处理——正文不给行号，只给 `文件 + 符号名`；确需区间时写"约"。
今天仍准确的行号引用只剩契约测试那几处（`e2_schema_diff_test.go:227,271`、`e3_schema_ddl_test.go:327`，本批已复核对得上）。

**全端门禁基线（2026-09-26 本机实跑，E6 收口口径）**：

| 端 | 命令 | 结果 |
|---|---|---|
| Rust 全量 | `cargo test --workspace` | **179 passed / 0 failed**（40 个 result 行，其中 **15 个套件非零**，余下 25 个是 0 份的 Doc-tests / 空壳；非零全表：`ui-gui` 65、`schema-diff` lib 40、`server` 20、`db-core` 11、`snapshot_diff` 10、`db-sqlite` lib 7、`view_rebuild_probe` 5、`redis_msgpack_test` 4、`app_core_integration` 4、`storage` 3、`local_transport` 3、`vectors` 2、`snapshot_fanout` 2、`ddl_vectors` 2、`app-core` lib 1） |
| Rust 静态 | `cargo clippy --workspace --all-targets -- -D warnings` / `cargo fmt --all -- --check` | 均 **exit 0**（fmt 写模式仍按项目约定手改，不用 `cargo fmt --all`） |
| Go 全量 | `go build ./... && go vet ./...` / `go test ./...` | 编译与 vet 干净；**13 个包全 ok、0 FAIL**（`pkg/schemadiff` 39 顶层、`pkg/dbsqlite` 19、`internal/tui` 与 `pkg/server` 是最慢的两个包） |
| Go 格式 | 剥 CR 后 `gofmt -l` | **本模块涉及的 8 个 Go 文件无输出**；全仓 117 个 .go 另有 1 处存量未格式化（见本节末披露①） |
| Web | `npm run test -- --run` / `npm run lint` / `npm run build` | **16 文件 / 163 测试全过**；eslint 无输出；vite build 成功（1m11s） |
| 协议漂移 | `tools/genprotocol` 的 `go run . check` / `go run . ts --check` | **`protocol check OK: 64 spec types vs rust + go`**、`TS types up to date` ⇒ E6 全程未改 spec（快照 DTO 是内部件，不进契约） |
| 契约对拍 | `cargo build -p polydb-server` 后 `cd test/contract && go test -v ./...` | **23 个顶层测试 / 89 个 PASS（含子测试）/ 0 FAIL / 0 SKIP 标记**；网络库与 Redis 在用例内部按 `connected:false` 软跳过（28 处记录，本机确实无 PG/MySQL/MSSQL/Oracle/Redis 实例） |
| 共享语料 | `go test -run DDLVector ./pkg/schemadiff/` + `cargo test -p polydb-schema-diff --test ddl_vectors` | **26 份**（18 脚本 + 8 错误）双端各自全绿；diff 语料 16 份同样双端绿 |

**【O1 系列之后对这张表的增量，2026-09-27】**：上表口径**冻结在 E6 收口**，此后的逐批数字看各批自己的附录，
不在这里改历史值。跨批可复算的增量只有两项——
① Rust 全量 **179 → 181**（A.24 新增 `select_vectors` 那 2 份；A.22/A.23 两批为零代码改动，179 未动）；
② 共享语料从**两套变三套**：diff 16 + DDL 26 + **勾选 `test/schema-select-vectors/` 18**（A.24）。
其余（Go 13 包全 ok、Web 163、协议 64 类型、契约 23/89/0）三批**逐项同数**，可直接沿用下面的复核命令。

**这套数字怎么复核**（照抄可跑，Git Bash；不用从正文反推命令）：

```bash
# Rust：静态 + 全量。套件名在 `Running …(<bin>-<16hex>.exe)` 行、passed 数在紧随的 `test result: ok. N passed`
cd rust && cargo clippy --workspace --all-targets -- -D warnings && cargo fmt --all -- --check
cargo test --workspace 2>&1 | tee /tmp/rust.log | grep -c '^test result'      # 40 个 result 行
awk '/^ *(Running|Doc-tests) /{n=$NF; gsub(/[()]/,"",n); sub(/-[0-9a-f]+\.exe$/,"",n)}
     /^test result: ok\./{if($4+0>0) printf "%-34s %s\n", n, $4; t+=$4} END{print "TOTAL", t}' /tmp/rust.log
# Go：必须 -count=1，否则全 cached、看不到数字
cd ../go && go build ./... && go vet ./... && go test -count=1 ./...
# 格式：工作树是 CRLF，直接 gofmt -l 会全量误报 ⇒ 剥 CR 到临时文件再判（禁 gofmt -w）
cd .. && for f in $(find go test tools -name '*.go'); do tr -d '\r' < "$f" > /tmp/g.go; gofmt -l /tmp/g.go; done
# Web / 协议
cd web && npm run test -- --run && npm run lint && npm run build
cd ../tools/genprotocol && go run . check && go run . ts --check
# 契约：先建 Rust 二进制，-v 才看得见子测试
cd ../rust && cargo build -p polydb-server && cd ../test/contract && go test -count=1 -v ./... > /tmp/c.log
grep -cE '^--- PASS' /tmp/c.log                    # 23 顶层
grep -cE '^[[:space:]]*--- PASS' /tmp/c.log         # 89 含子测试（必须允许行首空白）
grep -cE '^[[:space:]]*--- (FAIL|SKIP)' /tmp/c.log  # 0
```

**复核实测（在 `e477019` 的工作树上把上表全端重跑一遍）**：逐项与表中数字一致（Rust 179 / 0、Go 13 包全 ok、
Web 16 文件 163 测试、协议 64 类型、契约 23 / 89 / 0 / 0、语料 26 + 16 双端绿）。
两点如实披露：① **全仓 `gofmt -l`（剥 CR 后，117 个 .go）有 1 处存量未格式化** =
`test/contract/rows_test.go`（D1.7 `2fe6836` 遗留的结构体字段对齐 + 行尾注释空格），
与本里程碑无关 ⇒ 上表"Go 格式无输出"的准确范围是**本模块涉及的文件**，不是全仓；
本批刻意不顺手改它（改格式要重跑契约对拍，属另一条债）。② vite build 的**耗时**随缓存波动
（记为 1m11s，复跑 39.5s），只有"成功/失败"是门禁属性，耗时不是。

**E6 阶段的净账**（`1567bbd..6c08689`，即 E6.0–E6.5 的 **7 个提交**，**不含本批 E6.6**）：
**+4057 / −713、30 个文件**。
其中**非 `docs/` 部分 +3744 / −698（29 文件**，含 `spec/behavior.md` 与两份语料目录），
`docs/plan-compare-sync.md` 自身 +313 / −15。计数变化全部可归因：
`schema-diff` lib 36 → 40（A.20 的 4 份视图口径断言）、`ui-gui` 59 → 65（A.19 的 6 份状态机/环路断言）、
`view_rebuild_probe` 4 → 5（两端各 +1，A.20 的 `drop_column` 引擎拦截）、
DDL 语料 23 → 26（A.20 的撞名/警告/截断）。
**规则层的变更只有一处**（A.20 A 档），其余全是"把已经存在的行为变成断言"。

**本批数字的测量区间（供复算）**：E6.6 不是一笔提交，而是"收口主体 + 补账"连着好几笔，故显式给端点：
**T0 = `6c08689`（E6.5）／T1 = `9643b6f`（E6.6 主体：A.21 + 行号去腐烂）／T2 = `08b301a`（净账口径订正）**。
上表的全端门禁基线**在 T1 的工作树上实测**（当时工作树干净）；**`T1..HEAD` 没碰过 `docs/` 以外任何文件**
（`git diff --numstat 9643b6f -- . ':(exclude)docs'` 空输出，本批每笔都复核过）⇒ 表中数字对收口后的 HEAD 仍成立。
E6.6 净账因此**拆两条**：`T0..T1` = **+123 / −13（2 文件）**，`T1..T2` = **+2 / −1（1 文件）**；
本段声明自身及其后的每一笔补账，用 `git log --oneline T1..HEAD` 列出、`git show --numstat <hash>` 逐笔复原
（正文不逐笔自指——写进来就会改变自己要写的数）。补账只动本附录，不引入新口径。
**口径声明**：净账一律取 `git diff --numstat` 的**求和**（新增行 / 删除行），不引入"修改行"这一类——
git 的 diff 只回答"哪些行进、哪些行出"，一行改写会**同时**计 1 增 1 删，所以"改 N 行"这种写法在 git 口径下没有唯一含义。
顺带作废两个此前在会话里报过的数：E6.6 主体曾报"本批 +123/−13、E6.5 那批 +163/0（改 8）"，
后一组出自一个临时脚本（它把空行与纯空白行过滤了、且基线取的是当时工作树而非 commit），与 git 口径不可比，
**以上面的 T0..T1 / T1..T2 为准**；`docs/plan-compare-sync.md` 单文件在 E6.5 那批的真实读数是 **+68 / −8**。

**E6 之后的下一步（不在本里程碑内，需要另行拍板）**：① §8-8 的 B 档（依赖可见，要解析 SQL 猜）；
② 方言扩展（PG/MySQL 的 `ddl_in_tx` 真值 + 依赖目录，顺带才能补契约层两种分派断言）；
③ v1.1 的"同库多 schema"（§8-6 待办）；④ 数据行比对同步（§1 非目标，体量另一级）。

---

### A.22 O1 规则层复杂度归约（2026-09-27，**只改 Rust 一个函数，零行为变更 / 零 spec 变更 / Go 零改动**）

E6 收口后，用「按过程的形状思考」（SICP ch1：递归过程的成长阶）与「字典」（CLRS ch13：
成员判断该用数据结构买，不该用扫描买）两个视角通读比对/同步规则层，对
`rust/crates/schema-diff/src/ddl.rs` 的 `select_entries`（三端向导点「执行」时的勾选校验）做了成本核算。
**这是本台账第一次记复杂度**——此前正文没有任何 Θ/性能口径（已核对：全文无 `Θ`、无"复杂度"字样），
所以病灶不是"E6 遗留待办"，而是本批新记、新还。

**病灶两处（均为渐进问题，不是常数）**：

1. **成员判断写成线性扫**：旧代码对每个勾选 id 做 `plan.iter().any(|e| &e.id == id)`，
   再对每个候选失效 id 做 `stale.iter().any(...)` ⇒ 勾选 n 条、计划 n 条时 **Θ(n²)**。
2. **整组校验按表重扫全计划**：每张"需要整表重建"的表都用
   `plan.iter().filter(|p| lower_ascii(&p.table) == k && !want.contains(&p.id))` 现取一遍 ⇒
   R 张重建表 × 计划 P 条 = **Θ(R×P)**，且每次比较都重算一遍 `lower_ascii`。

**改法（等价替换，不是"顺手重写"）**：进函数时**一次**遍历计划，建两个字典——
`in_plan: BTreeSet<&str>`（id ⇒ 是否在计划里）与 `ids_by_table: BTreeMap<String, Vec<&str>>`
（表的小写键 ⇒ 该表全部条目 id，按计划顺序入桶）；失效名单侧再加一个 `seen_stale` 去重集。
选平衡树而非散列，要的是 CLRS 那条**最坏 Θ(log n)** 可证的界，且与本函数既有的 `BTreeSet<String>` 同形；
这里没有按序遍历需求 ⇒ 散列同样正确，属可选下一步，本批不做。

**为什么输出逐字不变（不变式，四条）**：
① `ids_by_table` 每个桶是**按计划顺序 `push`** 建起来的 ⇒ 桶内序列 == 旧"全计划保序 filter"的序列；
② 报错循环仍按 `picked` 的计划顺序走、`seen` 判重不变 ⇒ 触发哪张表、消息里的表名都不变；
③ `stale` 仍按**勾选顺序**、用 `seen_stale.insert()` 的"首次出现才为真"完成去重 ⇒
与旧 `!stale.iter().any(...)` 等价（旧写法对重复失效 id 也只点一次名，本批新增的断言把这条钉住了）；
④ `want` 从 `BTreeSet<String>` 换成 `BTreeSet<&str>` 只改借用，不改成员语义。

**实测（`--release`，每个数据点 best-of-7，单位 µs/次；同一 bench 文件、同一台机器、前后各两轮）**

场景 A：n 张表各一条 `alter_column`、**全部勾齐** ⇒ 成功返回（打成员判断那条路径）

| n | 旧·第 1 轮 | 旧·第 2 轮 | 新·第 1 轮 | 新·第 2 轮 |
|---|---|---|---|---|
| 250 | 21 586 | 4 467 | 532 | 526 |
| 500 | 31 132 | 17 134 | 1 220 | 1 157 |
| 1 000 | 96 521 | 80 260 | 2 419 | 2 227 |
| 2 000 | 432 493 | 413 254 | 4 063 | 4 500 |

场景 B：R 张重建表（计划 2R 条）、**只有最后一组缺一项** ⇒ 报错（把组校验的重扫推到端点）

| R | 旧·第 1 轮 | 旧·第 2 轮 | 新·第 1 轮 | 新·第 2 轮 |
|---|---|---|---|---|
| 50 | 1 027 | 564 | 90 | 143 |
| 100 | 1 672 | 2 018 | 364 | 310 |
| 200 | 6 981 | 7 228 | 814 | 700 |
| 400 | 28 735 | 23 672 | 1 320 | 1 708 |

**读数（只读读得出的）**：旧实现每翻倍 ≈4×（A 是 n²、B 是 R·P = 2R²），新实现每翻倍 ≈1.7–2.1× ⇒ 形状确实换了。
加速比只取**大 n**：A@2000 = **106× / 92×**，B@400 = **21.8× / 13.9×**。
三点如实披露：① **小 n 不可信**——旧 A@250 两轮差 4.8×（第 1 轮与后台编译并发），故本表只给区间、不取均值；
② 新实现**不是 Θ(n)**，是 Θ(n log n)，所以翻倍比落在 1.7–2.1 之间摆动（B@400 第 2 轮比第 1 轮高 29%）；
③ bench 与差分文件都是**临时脚手架、跑完即删、不进门禁** ⇒ 这些数字是**一次性证据**，
仓库里没有可照抄的复跑脚本（要复跑得按上面两张表的场景定义重写；刻意不留，理由见本节末）。

**等价性证据三层**：
1. **临时差分测试**：把旧算法**逐字转录**成参照实现，与新的 `select_entries` 跑 **600 例随机勾选**
   （随机 1–6 张表、每表 1–4 条、6 种 op、随机子集 + 重复勾同一条 + 掺入计划外的 ghost id + 打乱勾选顺序），
   逐例比对 `Ok` 的 id 序列与 `Err` 的消息文本 ⇒ **600/600 一致，其中 370 例走到错误分支**
   （快照取空 ⇒ `needs_rebuild` 只由 op 决定，新旧共用同一判据，不引入第二个变量）。
2. **既有逐字断言**：`schema-diff` lib 40 份全绿，含三处锁死消息与顺序的用例
   （失效名单按勾选顺序、组缺失按计划顺序），另**本批新增一条**：同一失效 id 重复勾选只点一次名。
3. **双端与契约**：共享语料 DDL 26 份 + diff 16 份双端各自绿；契约套件在本批工作树上重跑 **23 顶层 / 89 含子测试 / 0 FAIL**。

**本批门禁（2026-09-27 本机实跑，代码状态 = 本批工作树）**：

| 端 | 命令 | 结果 |
|---|---|---|
| Rust 静态 | `cargo fmt --all -- --check` / `cargo clippy --workspace --all-targets -- -D warnings` | 均 **exit 0**（fmt 仍按项目约定手改，不用写模式） |
| Rust 全量 | `cargo test --workspace` | **179 passed / 0 failed** ⇒ 与 A.21 基线**同数**（新断言并进了既有测试，不新增份数） |
| Go | `go build ./... && go vet ./... && go test -count=1 ./...` | 编译与 vet 干净、**13 包全 ok、0 FAIL** |
| 协议漂移 | `tools/genprotocol` 的 `go run . check` / `go run . ts --check` | `protocol check OK: 64 spec types vs rust + go` / `TS types up to date` ⇒ 零 spec 变更 |
| 契约对拍 | `cargo build --workspace` 后 `cd test/contract && go test -count=1 -v ./...` | **23 个顶层 / 89 个 PASS（含子测试）/ 0 FAIL / 0 SKIP 标记**（319.8s，**在 `eaa93b1` 已提交后的工作树上重跑**；网络库与 Redis 仍在用例内按 `connected:false` 软跳过）⇒ 与 A.21 基线同数 |

**本轮门禁未重取的一项（如实标出，不是跳过、是没做）**：`golangci-lint` v2 本机仍不在 PATH
（§7 第 ⑦ 条原样成立）。另：Web 端与 Go 侧本批零改动，故 `npm run test/lint/build` 与 Go 子测试计数未重跑
（契约 `-v` 计数**有**在本批工作树上重跑，即上表那行）。
契约这一行**返工过一次**：第一次 `-v` 日志是在"反向打补丁量旧实现"的混合状态期间产出的，
不足以背书新实现 ⇒ 已作废，上表是**代码提交之后**在干净工作树重跑的那一份（耗时 319.8s，比 A.21 的 280s 量级慢，
只作参考、不作门禁属性）。

**范围（经选项确认拍的）**：**只修 Rust 这两处**。
Go `pkg/schemadiff/ddl.go` 的 `SelectEntries` 已经建了 `want` / `inPlan` 两张 map（第 1 处旧病灶它本来就没有），
但**组校验那条"逐张重建表重扫全计划 + 每次重算 `lowerASCII(table)`"的 Θ(R×P) 同形仍在**。
⇒ 「双实现行为一致、算法不一致」这条债**没有还掉**，本批刻意留：改 Go 要连 `gofmt` 判定与契约对拍一起重跑，
而选的是最小范围。下一批若做，照抄本节的桶结构即可，等价性论证同一套（Go 侧还有一处顺带收益：
它每轮比较都重算 `lowerASCII`，改完可以省掉）。

> **【2026-09-27 更新】上面这段债已经还掉了 = A.23（O1.1）**：Go 侧同形病灶实测为 R=800 时 43.9–51.7 ms，
> 改成查桶后 1.87–2.38 ms（21–28×），并顺手省掉了每轮重算的 `lowerASCII`。
> 本节保留原文不删，是为了留下"当时为什么选择只改一半"这个决策本身。

**净账**：`git diff --numstat` = **`rust/crates/schema-diff/src/ddl.rs` +36 / −10（1 文件）**，
其中 **+13 全在 `mod tests`**（新增"同一失效 id 重复勾选只点一次名"那一段），其余 +23 / −10 是 `select_entries`
本体的索引化改写与注释；另有本附录（`docs/` 自身不计入代码账，口径同 A.21）。
区间端点：**父提交 `8a40183`（E6.6 数字复核）→ 本批主体 `eaa93b1`（代码 + 本附录）**；
本批之后的每一笔补账只动本附录、不引入新口径，用 `git log --oneline eaa93b1..HEAD` 列出、
`git show --numstat <hash>` 逐笔复原（正文不逐笔自指——写进来就会改变自己要写的数）。

**为什么不留脚手架**：把旧算法的转录实现留在仓库里，后来人会以为有一份"参照实现"要跟着维护，
而它唯一的价值就是证明这一次替换；场景定义（两张表的列、best-of-7、600 例随机规则）已写全在本节里，
需要复跑时按定义重写比养一份死代码便宜。

### A.23 O1.1 Go 组校验查桶（2026-09-27，**只改 Go 一个函数的索引结构，零行为变更 / 零 spec 变更 / Rust 零改动**）

这一批还的就是 A.22 末尾记下的那条债：`go/pkg/schemadiff/ddl.go` 的 `SelectEntries`，
组校验段（"同表变更必须整组勾选"那条文案的 `missing` 收集）对**每张重建表重扫一遍全计划**，
= **Θ(R×P)**（R = 重建表数、P = 计划条数），而且每轮比较都重算一次 `lowerASCII(plan[j].Table)`。
改法与 Rust 侧 A.22 完全同构：**在已经存在的那趟 `inPlan` 遍历里顺手建桶**，组校验改成查桶。

```go
inPlan := map[string]bool{}
// 两个索引一次建好：id ⇒ 是否在计划里，表键 ⇒ 该表全部条目 id（按计划顺序入桶）。
idsByTable := map[string][]string{}
for i := range plan {
    e := &plan[i]
    inPlan[e.ID] = true
    k := lowerASCII(e.Table)
    idsByTable[k] = append(idsByTable[k], e.ID)
}
...
var missing []string
for _, id := range idsByTable[k] {
    if !want[id] {
        missing = append(missing, id)
    }
}
```

**四条等价不变式**（改动只允许落在这四条里，逐条都对应上面的一段代码）：

1. **桶内容 = 旧的线性扫描命中集，且顺序一致**：`idsByTable[k]` 恰好收 `lowerASCII(Table)==k` 的那些条目 id，
   按计划顺序 `append` ⇒ `missing` 的元素与先后**逐字不变**（这条文案是双实现逐字比对对象，见 §14 口径）。
2. **键归一口径同构**：入桶键与查桶键走同一个 `lowerASCII`，与同文件的 `tableMap`、Rust 侧 `lower_ascii`
   同规则 ⇒ `V_5` / `v_5` 这类同名异写在两实现里进同一个桶。
3. **重复 id 不合并**：同一 ID 出现多条条目时桶里有两份，旧实现按条目扫描也有两份 ⇒ 一致。
   （真实 diff 里 ID 可撞：ID 是 `{表}/{对象}/{op}`，快照畸形时同三元组会重复发射。）
4. **一次建索引不碰判定**：stale 分支与 `seenStale` 去重口径分毫未动；`for _, e := range plan` 换成
   `for i := range plan` 只为省掉逐条 `Entry` 值拷贝，不改变 `inPlan` 的内容。

顺带的（不是新行为）：全函数的 `lowerASCII` 调用次数从 **R×P + 2R** 降到 **P + 2R**。

**前后计时**（同一份临时基准脚手架，两个场景、R 递增，`go test -bench`、`-benchtime=200x`、本机 i7-8750H；
每个版本各取两次采样，表中给**区间**不给均值。R = 需要整表重建的表数，P = 计划条数 = 3R，
两侧快照留空即可——`needsRebuild` 对 `OpAlterColumn` 无条件为 true）：

场景 A「全部勾选」（合法路径，必须走完 R 次收集）：

| R | P | 改前 | 改后 | 加速 |
|---|---|---|---|---|
| 50 | 150 | 0.27–0.36 ms | 0.093–0.094 ms | 2.9–3.9× |
| 100 | 300 | 0.79–1.13 ms | 0.164–0.168 ms | 4.7–6.9× |
| 200 | 600 | 3.38–3.89 ms | 0.329–0.336 ms | 10.0–11.8× |
| 400 | 1200 | 10.5–13.2 ms | 0.76–1.41 ms | 7.4–17.4× |
| 800 | 2400 | **43.9–51.7 ms** | **1.87–2.08 ms** | **21–28×** |

场景 B「末组缺一条」（错误路径，同样要扫完 R 张表才报错）：

| R | P | 改前 | 改后 | 加速 |
|---|---|---|---|---|
| 50 | 150 | 0.207–0.209 ms | 0.094–0.175 ms | 1.2–2.2× |
| 100 | 300 | 0.84–0.91 ms | 0.175–0.178 ms | 4.7–5.2× |
| 200 | 600 | 3.23–3.31 ms | 0.355–0.518 ms | 6.2–9.3× |
| 400 | 1200 | 11.3–14.0 ms | 0.73–0.90 ms | 12.5–19.1× |
| 800 | 2400 | **44.5–46.1 ms** | **2.23–2.38 ms** | **18.7–20.7×** |

**斜率才是这批的结论**：改前 R 从 100→800（规模 8 倍）用时涨 **52.9–55.4 倍**（≈ 二次），
改后同区间涨 **11.4–12.7 倍**（线性是 8 倍，多出来的是分配）。

**读法与披露**：
① 小 n 读数在这台机器上抖动 2–3 倍（B 列 R=50 的 0.094–0.175 就是**同一版本**两次采样的跨度），
⇒ **加速比只在 R ≥ 400 处可信**；另跑过一次 `-benchtime=500x` 复核，量级一致（R=800 场景 A 为 2.085 ms），
因协议不同不列入上表。
② **空间换时间，代价实测**：R=800 场景 A 从 1,715,418 B / 4,904 allocs 涨到 **1,910,019 B / 9,719 allocs**
（字节 +11%、分配次数 +98%）——桶本身存 P 份 id，加上每桶 `append` 的几何扩容。
③ 场景是人为放大的最坏情况：真实计划里重建表只占少数，**200 表规模省的是 3 ms 级**，
所以这批还的是"渐进复杂度 + 双实现算法一致"这条债，**不是**用户可感知的卡顿（口径同 A.22 对 Go 残留的判断）。
④ 共享语料（DDL 26 份 + diff 16 份）钉的是**语句生成器**，从来不覆盖 `SelectEntries`
⇒ 勾选校验至今**没有跨语言共享语料**（A.22 已披露）。本批用"旧算法转录 + 随机差分"顶这一层，
脚手架不入库（理由同 A.22 末段）。
⑤ **契约计数口径差点被我自己数错**：本批复核时用 `grep -cE '^\s+--- PASS'` 数到 66，一瞬间像是掉了 23 个测试；
真相是 A.21 记的口径用的是 `^\s*`（**星号**，把 23 个顶层行也算进来）⇒ 89 = 23 顶层 + 66 子测试，
两种写法正文里都有（本附录上表、A.21 的 23/89、A.14 的 23/66）。**照抄 A.21 那段命令即可复现，别把 `*` 手打成了 `+`。**

**等价性证据三层**：

1. **差分**（临时脚手架）：把改动前的 `SelectEntries` 原样转录成 `selectEntriesOld`，1200 个随机用例下
   要求 `err.Error()` **逐字节相同** + `picked` `reflect.DeepEqual` 相同。用例形状：1–6 张表 × 每表 1–5 条、
   op 在 7 个取值里随机（含 `add_index`/`drop_index` 撞上 `sqlite_autoindex_*` 以覆盖 `needsRebuild` 的两侧分支）、
   表名大小写混排、勾选集可含重复 id / 幽灵 id / 为空。**分支命中分布：stale 377 例、整组缺失 436 例、成功 387 例**
   ⇒ 两条错误路径都不是"碰巧没跑到"。
2. **存量断言**：`TestSelectEntriesKeepsPlanOrderAndSubset`（计划序）、
   `TestSelectEntriesRequiresWholeGroupForRebuiltTables`（缺组文案逐字，含多表先后）、`TestSelectEntriesRejectsStaleIDs`
   全绿。本批给最后一份**补了一条与 Rust `select_entries_rejects_stale_ids` 同构的断言**
   （同一失效 id 重复勾选只点一次名）⇒ A.22 里那条"只有 Rust 侧有去重断言"的测试不对称**闭合**。
   这是本批唯一的非重构改动：**8 行测试文件、零规则变更**。
3. **契约 + 双端语料**：Go 全量 13 包 ok、契约 23 顶层 / 89 含子测试 / 0 FAIL / 0 SKIP，
   Rust `polydb-schema-diff` 40 + 2 + 2 全绿（Rust 源码本批未动，重跑只为证明共享语料在两实现上仍**同时**通过）。

**本批门禁**（2026-09-27，工作树 = Go 改动 + 本附录）：

| 项 | 命令 | 结果 |
|---|---|---|
| Go 格式 | 剥 CR 后与 `gofmt` 输出比对 `ddl.go` / `ddl_test.go` | 两份都**已格式化**（仍禁 `gofmt -w`，原因见 A.21） |
| Go 编译/静态 | `go build ./... && go vet ./...` | 均 exit 0、无输出 |
| Go 全量 | `go test -count=1 ./...` | **13 包全 ok、0 FAIL**（与 A.21/A.22 基线同数） |
| 协议漂移 | `tools/genprotocol` 的 `go run . check` / `go run . ts --check` | `protocol check OK: 64 spec types vs rust + go` / `TS types up to date` ⇒ 零 spec 变更 |
| Rust | `cargo test -p polydb-schema-diff` | **44 passed / 0 failed**（40 lib + 2 + 2 份语料测试） |
| 契约对拍 | `cargo build -p polydb-server` 后 `cd test/contract && go test -count=1 -v ./...` | **23 个顶层 / 89 个 PASS 行（23 + 66 子测试）/ 0 FAIL / 0 SKIP 标记 / 28 处 `connected:false` 软跳过**（273.4s）⇒ 与 A.21/A.22 基线**逐项同数** |

**本轮门禁未重取的项（如实标出）**：① `golangci-lint` v2 本机仍不在 PATH（§7 第 ⑦ 条原样成立，
这是唯一一直没跑的门禁）；② Rust 全量 `cargo test --workspace`、`fmt --check`、`clippy -D warnings` 未重跑
——本批**没有碰任何 `.rs` 文件**，只跑了受影响的 `polydb-schema-diff`；③ Web 端零改动，`npm run test/lint/build` 未重跑。

**净账**：`git diff --numstat` = **`go/pkg/schemadiff/ddl.go` +9 / −4、`go/pkg/schemadiff/ddl_test.go` +8 / −0（2 文件）**；
生产代码只有 `ddl.go` 那 +9/−4，其中 −4 全是旧的线性扫描三行 + 一个 range 头。
区间端点：**父提交 `b6000b8`（A.22 契约数字订正）→ 本批（本行所在提交）**；
本批之后的补账只动本附录，用 `git log --oneline b6000b8..HEAD` 列出、`git show --numstat <hash>` 逐笔复原。

**至此 A.22 那条"行为一致、算法不一致"的债清掉**：Rust 与 Go 的勾选校验现在是同一个形状
（一次建桶 + 查桶 + `want` 过滤），两处 Θ(R×P) 与一处 Θ(n²) 全在两实现里消失。
剩下没做的同形项：**没有为"勾选校验"建跨语言共享语料**（本批差分是单语言新旧对比，不是跨语言对比）；
要建就得同时给 Rust 侧接一份读同一 JSON 的测试，成本比这批高，仍排在方言扩展之后。
**【2026-09-27 更新】这条债也已还掉 = A.24（O1.2）**：勾选校验现在有 18 份跨语言共享语料，
Go 与 Rust 各自读同一批 JSON 并各自现算 plan，两端断言**同一条目序列 + 逐字同一条文案**。

### A.24 O1.2 勾选校验跨语言共享语料（2026-09-27，**零规则代码改动 / 零 spec 变更：只加语料 + 两端各一份读语料的测试**）

这批还的是 A.22→A.23 连记两笔的那条债。缺口说清楚：
DDL 语料（26 份）里 `SelectEntries` 只是**被穿过**——用例把勾选喂进去、断言的是最终脚本，
**picked 的集合与顺序从来没有被直接断言过**；两条中文文案虽然被 d22/d23 逐字钉死，
但"每条分支都有语料打到"这件事没有任何门禁。diff 语料（16 份）只到 plan 层，压根不碰勾选。

**形状（拍板选的那一档：「快照 + selected，plan 现算」）**
每份用例带 `source`/`target`/`options`/`selected`，**不带** plan；两端各自跑 `Diff` 现算 plan 再喂勾选。
⇒ 一份用例同时钉住两件事：plan 的**顺序**（勾选的输出序只能来自它）与 picked 的**结果**。
好处是勾选层零额外真相源（plan 不入库，双实现若在此分叉，diff 语料会先红）；
代价是勾选用例把 diff 的正确性也一起绑上了——这是有意的。

字段表（`test/schema-select-vectors/*.json`，18 份，共 949 行 / 35,172 B，全部 LF）：

| 字段 | 类型 | 说明 |
|---|---|---|
| `name` | string | 与文件名同 |
| `note` | string | **中文**：这份用例钉住哪条分支、为什么值得钉 |
| `options` | object | `ignore_defaults` / `select_destructive`，两端都按 `Diff` 的入参用；**勾选函数不收 options**（见 s12） |
| `source` / `target` | TableSnapshot | 与 DDL 语料同一套 DTO，字段名/必填完全一致 |
| `selected` | string[] 或 **null** | null ⇒ "全选"，与 DDL 语料同口径 |
| `expected` | string[] | 成功路径的 picked id 序列（**按 plan 序**） |
| `error_code` / `error_message` | string | 失败路径；成功时两字段省略 |

**生产纪律（照 E1/E3 的规矩，一条没破）**：`expected` **绝不手写**。
只写输入 ⇒ `POLYDB_SELECT_VECTORS_WRITE=1 go test ./pkg/schemadiff -run WriteSelectVectorExpected`
由 Go 侧回填 ⇒ 人工复核每一条落盘值 ⇒ Rust 侧作为**第二实现**读同一批文件，不参与生产。
写回器只替换 `expected` / `error_code` / `error_message` 三处的值，其余字节逐字不动（含缩进与行尾风格）。
基建复用：把 `ddl_vectors_test.go` 里那个只认 `expected` 的定点替换器泛化成
`replaceJSONValue(raw, key, replacement, nl)`，原 `replaceExpectedValue` 退化成它的一行包装 ⇒
**语料写回器从两套变一套半**，这是本批唯一碰到存量 Go 文件的地方（+10/−4，纯测试文件）。

**18 份用例各钉什么**（`sel` = `selected`；⇒ 后是断言到的值）：

| 用例 | sel | 钉住的行为 |
|---|---|---|
| s01 `all_selected_null` | null | ⇒ `["w//create_table","t/c/add_column"]`——**plan 序 = 相位序**（create_table 50 在 add_column 60 前），不是快照出现序 |
| s02 `empty_selection` | `[]` | ⇒ `[]`：空勾选合法且产出为空 |
| s03 `reordered_selection` | 4 条**逆序**（跨 4 个相位） | ⇒ `drop_index → create_table → add_column → add_index`：**输出只认 plan 序**，勾选数组的次序不影响 |
| s04 `duplicate_ids_selected` | 4 条含 1 重复 | ⇒ 3 条：重复 id 合并，不产出两遍 |
| s05 `stale_single_id` | 1 真 1 假 | ⇒ `勾选的条目不在当前比对计划里：t/nope/add_column（请重新比对后再执行）`（真实场景＝界面刷新后拿旧 id 来勾） |
| s06 `stale_ids_first_appearance_dedupe` | 2 假 + 1 假重复 | ⇒ 幽灵 id 按**首次出现**去重、「、」连接（`t/ghost/drop_index、u/x/drop_column`），顺序稳定非 map 序 |
| s07 `rebuild_group_partial` | 只勾重建组里的 `alter_column` | ⇒ `表 t 需要整表重建，同表变更必须整组勾选：缺少 t/note/add_column` |
| s08 `rebuild_group_whole` | 整组、**逆序**给出 | ⇒ `["t/note/add_column","t/qty/alter_column"]`：勾全则通过，输出仍回 plan 序 |
| s09 `two_rebuild_tables_report_first` | 两张重建表各缺一条 | ⇒ 文案点名 **表 a**（plan 里先出现的那张赢），不是勾选里先出现的那张 |
| s10 `views_produce_no_entries` | null | ⇒ `["t/b/add_column"]`：**视图不产生任何条目**（两侧快照都带视图，plan 里一条都没有） |
| s11 `mixed_case_table_ids_and_message` | `foo/qty/alter_column`（小写 id） | ⇒ `表 Foo 需要整表重建…缺少 foo/note/add_column`：**文案里的表名用显示大小写、id 用小写** |
| s12 `destructive_selected_explicitly` | 显式勾 `legacy//drop_table`（options 里 `select_destructive:false`） | ⇒ `["legacy//drop_table","t/b/add_column"]`：该条目在 plan 里带的是 `Selected=false`（默认不勾），但**函数签名不收 options** ⇒ 破坏性条目的放行只认"有没有被勾"，与默认标记无关 |
| s13 `selected_id_is_case_sensitive` | `Foo/Note/add_column`（显示大小写） | ⇒ 报 **stale**：勾选层不做大小写兜底，id 一律大小写敏感 |
| s14 `stale_beats_group_check` | 幽灵 id + 缺组同时在场 | ⇒ 只报 **stale**（分支优先级；与 s11 同快照、只有 `selected` 不同） |
| s15 `rebuild_missing_two_ids` | 只勾 `t/id/drop_column`（删 PK 列） | ⇒ 缺 3 条：`t/sqlite_autoindex_t_1/drop_index、t/note/add_column、t/idx_note/add_index`（删 PK 列牵出自建索引 + 重建） |
| s16 `add_foreign_key_group` | 只勾 `t/fk_t_p/add_foreign_key` | ⇒ `…缺少 t/p_id/add_column`：**FK 条目的对象段是约束名**，不是列名 |
| s17 `rebuild_check_only_looks_at_picked` | 只勾 `t/p_id/add_column`（与 s16 同快照） | ⇒ **通过**：组校验只审视 **picked** 里的条目，纯 `add_column` 不触发整组要求（有意的不对称，见下） |
| s18 `rebuild_group_of_four_whole` | 4 条重建组**打乱**给出 | ⇒ `["t/idx_qty/drop_index","t/name/add_column","t/qty/alter_column","t/idx_name/add_index"]`（A.22/A.23 那条查桶改动的正向对照：R=1、P=4 的最小整组） |

**建语料时才浮出来的三条非显然行为**（都已固化成用例，不是事后补的解释）：

1. **视图零条目**（s10）。`tableMap` 按用途把非表项跳掉，视图既进不了 plan 也进不了重建组。
   勾选层因此**永远看不见视图**——这与 A.20 的"视图名进快照只为避让临时名"是两件事，
   此前只写在文档里，现在有断言。
2. **组校验只看 picked**（s16 vs s17）。同一份快照，勾 FK 报缺列、只勾那列反而过。
   这不是漏洞，是规则层的既定口径：**只有触发重建的那条条目才把整表拉进整组要求**。
   以前这个形状没有任何测试覆盖，改起来全靠口头保证。
3. **id 大小写敏感、文案用显示大小写**（s11 + s13）。一对用例把"归一只发生在桶键与 id 生成，
   不发生在勾选输入"钉住：拿显示大小写去勾 ⇒ 直接判过期，不做兜底。

**覆盖度门禁（两端各一份，15 个分支键缺一个即红）**：
`selected_null`、`selected_empty`、`subset_pass`、`duplicate_ids`、`unordered_selected`、
`view_present`、`plan_order_not_lexicographic`、`stale`、`stale_multi`、`stale_display_casing`、
`stale_beats_group`、`group`、`group_multi_missing`、`group_mixed_case_display`、`group_whole_pass`。
再加两条硬约束：**语料份数 ≥ 10**（两端都断言，现 18）与
**"错误文案必须落在已知分支里"**（未知文案直接失败 ⇒ 将来改文案必须同时改两端门禁，不会静默漂移）。

关键是**三个推导式键，不靠标记、也就不会恒真**：

| 键 | 怎么推出来 |
|---|---|
| `plan_order_not_lexicographic` | picked 的 id 序列 ≠ 同一批 id 排序后的序列 ⇒ 将来谁把输出改成"按 id 排"，这条立刻红 |
| `stale_beats_group` | 把幽灵 id 摘掉后**用同一份输入重跑一次** `SelectEntries`，若改报缺组 ⇒ 这次勾选确实同时踩中两个分支 |
| `group_whole_pass` | 用规则层的 `needsRebuild`（Go）/ 生成结果里的 `pragmas_outside_tx`（Rust）**复核 picked 里确实有重建条目**，而不是靠文件名里写着"group" |

另外两条结构性断言（两端都有，不只靠比字符串）：picked 必须是 plan 的**严格单调子序列**
（逐位置 index 递增 + 每条 `assert_eq!(&plan[at], p)` 全字段等），以及勾选校验**幂等**（重跑同输入同输出）。

**一处有意的实现不对称（如实标出）**：Rust 的 `needs_rebuild` 是 crate 内私有，
集成测试（`tests/select_vectors.rs`）拿不到 ⇒ Rust 侧改用 `generate(...).pragmas_outside_tx` 作"这组真需要重建"的代理；
Go 侧 `select_vectors_test.go` 在包内，直接调 `needsRebuild`。
代价：Rust 的那条推导多绕一层生成器；好处：没有为此把 `needs_rebuild` 提公开（不扩大 API 面）。
口径记录在 Rust 文件的块注释里。

**两个由语料反过来的取证**（这批的价值不只在"有测试"）：

1. **FK 的 id 对象段是约束名**。s16 最初按列名写成 `t/p_id/add_foreign_key`，
   写回器回给它的是 **stale** 而不是缺组——由此确认 plan 里是 `t/fk_t_p/add_foreign_key`（`{表}/{约束名}/{op}`）。
   先前这条只在 A.15 的叙述里，没有断言；现在是 s16。
2. **语料必须满足更严的那一端**。s16/s17 的 FK 少写 `referenced_schema` 时，
   Rust serde 直接 `missing field referenced_schema` 失败，Go 却**静默容忍**（该字段无 `validate` 约束）。
   已补齐。教训与 A.13 的 serde 口径同类：**共享语料的必填集合取两端交集里的最严者**，
   否则"Go 绿 Rust 红"会被误读成实现分叉。

**等价性证据**：这份语料本身就是跨语言证据——两端读同一批 18 个 JSON，
各自现算 plan、各自跑勾选，断言**同一条 id 序列 + 逐字同一条中文文案 + 同一个 `POLYDB_ERR_INVALID_PARAM`**。
这与 A.23 的差分（单语言新旧对比）是互补的两层：那层证明"改索引结构不改行为"，这层证明"两实现行为一致"。

**与存量语料的重叠（不粉饰）**：d22/d23 已经逐字钉过两条错误文案，
`TestSelectEntries*`（Go 3 份：plan 序 / stale / 整组）与 Rust `select_entries_*`（同样 3 份）
也已覆盖 plan 序与去重。s05/s06/s07 与它们是**同一断言的第二处**——保留的理由是：
那些断言写在单语言测试里，改一端不会让另一端红；而这批文件是**共享数据**，跨语言分叉必须两端同时红。

**本批门禁**（2026-09-27，工作树 = 18 份语料 + Go 测试 + Rust 测试 + `ddl_vectors_test.go` 泛化 + 本附录）：

| 项 | 命令 | 结果 |
|---|---|---|
| Go 格式 | 剥 CR 后与 `gofmt` 输出比对 `select_vectors_test.go` / `ddl_vectors_test.go` | 两份都**已格式化**（新增两份本身即 LF；仍禁 `gofmt -w`，见 A.21） |
| Go 编译/静态 | `go build ./... && go vet ./...` | 均 exit 0、无输出 |
| Go 勾选语料 | `go test ./pkg/schemadiff -run 'SelectVectors' -count=1 -v` | **2 PASS**（`TestSharedSelectVectorsMatchExpectedPicked` + `TestSharedSelectVectorsCoverage`） |
| Go 全量 | `go test -count=1 ./...` | **13 包全 ok、0 FAIL**（与 A.21–A.23 基线同数） |
| Rust 勾选语料 | `cargo test -p polydb-schema-diff --test select_vectors` | **2 passed / 0 failed** |
| Rust 单 crate | `cargo test -p polydb-schema-diff` | **46 passed / 0 failed**（40 lib + 2 diff + 2 DDL + 2 勾选） |
| Rust 全量 | `cargo test --workspace` | **181 passed / 0 failed**（41 个 result 行）⇒ **恰好 = A.22 基线 179 + 本批 2**，逐笔对得上 |
| Rust 格式/哨兵 | `cargo fmt --all --check`、`cargo clippy -p polydb-schema-diff --all-targets -- -D warnings` | 均 exit 0、零警告 |
| 协议漂移 | `go run . ts --check` / `go run . check` | `TS types up to date` / `protocol check OK: 64 spec types vs rust + go` ⇒ 零 spec 变更 |
| Web | `npm run check` + `npm run test` | tsc 无输出、**163 passed (163)** ⇒ 与 A.21 基线同数（本批零前端改动，重跑只为证明没被碰坏） |
| 契约对拍 | `cargo build -p polydb-server` 后 `cd test/contract && go test -count=1 -v ./...` | **23 个顶层 / 66 个子测试（合计 89 个 PASS 行，口径见 A.23 ⑤）/ 0 FAIL**，另一次非 -v 复跑 `ok 265.7s` ⇒ 与 A.21–A.23 基线逐项同数 |

**门禁自身会不会恒真？两组反证实测**（做完立刻按 md5 还原，语料最终态与反证前逐字节一致）：

| 反证 | 结果 |
|---|---|
| 把 s01 的 `expected` 改成逆序 | **两端同时红**（Go `TestSharedSelectVectorsMatchExpectedPicked` FAIL、Rust 同名测试 FAILED），还原后双端绿 ⇒ 断言真在比数据 |
| 逐份摘掉 s13 / s02 / s06 / s11 / s15 后跑覆盖度门禁 | 分别精确报缺 `stale_display_casing` / `selected_empty` / `stale_multi` / `group_mixed_case_display` / `group_multi_missing` ⇒ 覆盖度键**不是摆设** |
| 摘掉 s03 | **不红** ⇒ `unordered_selected` 与 `plan_order_not_lexicographic` 由 s01/s04/s08/s12/s18 共同覆盖，s03 是冗余样本（冗余不是缺陷，但说明这两个键的"唯一来源"不是 s03） |
| 其余 9 个键（`subset_pass`、`duplicate_ids`、`view_present`、`stale`、`stale_beats_group`、`group`、`group_whole_pass`、`selected_null`、份数下限） | **未逐一摘样**，只确认当前 18 份下非零 ⇒ 这条披露如实留白 |

**本批未取/未验证项（照例标出）**：① `golangci-lint` v2 本机仍不在 PATH（§7 第 ⑦ 条原样成立，
这是唯一一直没跑的门禁）；② **数错一次**：先前一条管道把 workspace passed 数报成 189，
复核用的 `grep -E "^test result"` 计数 = **41 行、sum 181、failed sum 0**，与 A.22 的 179 差 2 恰好是本批新增两份，
故上表采用 181；③ 三端 UI 零改动 ⇒ GUI/TUI/Web 向导未肉眼验证。

**净账**（`git show --numstat 6e2348f`，本批主体提交）：22 个文件 **+1,809 / −4**，逐笔对得上——
`test/schema-select-vectors/` 18 份 **+949**、`go/pkg/schemadiff/select_vectors_test.go` **+417**、
`rust/crates/schema-diff/tests/select_vectors.rs` **+268**、本附录 **+165**，
存量代码只有 **`go/pkg/schemadiff/ddl_vectors_test.go` +10 / −4（1 文件，纯测试）**——
把只认 `expected` 的定点替换器泛化成 `replaceJSONValue`，原函数退化成一行包装。
**规则层（`ddl.go` / `ddl.rs`）与 `spec/` 零字节改动**：这批没碰任何生产代码，全部是断言与数据。
区间端点：**父提交 `3ed3be0`（A.23 Go 查桶）→ `6e2348f`（本批主体）**；
本批之后的补账只动本附录，用 `git log --oneline 3ed3be0..HEAD` 列出、`git show --numstat <hash>` 逐笔复原。

**遗留（下一批再说，本批没动）**：`test/schema-ddl-vectors/d22_stale_selected_id.json` 与
`d23_rebuild_group_must_be_selected_whole.json` 的 `name` 字段都写成了 `d14_rebuild_group_absorbs_same_table`
（E3.3 时期的复制粘贴，两份语料的 name 重复且与文件名不符）。它只进测试失败消息、不进断言，
但会误导读语料的人；改它要同时重跑两端 DDL 语料，属于独立一笔账。**→ 已由 A.25 还清。**

**至此 O1 系列三条债清完**：A.22（Rust 两处 Θ(n²)/Θ(R×P) 归约）→ A.23（Go 同形病灶查桶）→
A.24（勾选校验的跨语言共享语料）。规则层现在的状态是：
**算法同形 + 渐进同阶 + 行为有 18 份共享数据钉住**，同形项里剩下的都在方言扩展之后
（MySQL/PG/MSSQL/Oracle 四套 DDL 生成器尚无语料，勾选层只有 SQLite 口径）。

---

### A.25 O1.2 补账：三套语料的 `name` 与文件名一致性（2026-09-27，**零规则代码 / 零 spec 变更：2 份语料的元数据 + 六处加载器各一条断言**）

A.24 的遗留只点了 d22/d23 两份。开工第一件事不是改那两行，而是**先量化范围**——
"两份写错"这个说法本身可能就是错的，也许是一批、也许三套都有。

**量化（60 份语料全查，2026-09-27 实测）**

| 语料目录 | 份数 | `name` ≠ 文件名（去 `.json`） | 目录内重复 `name` |
|---|---|---|---|
| `test/schema-diff-vectors` | 16 | 0 | 无 |
| `test/schema-ddl-vectors` | 26 | **2**（d22、d23） | **1 组**：`d14_rebuild_group_absorbs_same_table` 出现 3 次 |
| `test/schema-select-vectors` | 18 | 0 | 无 |
| 合计 | **60** | **2** | 58/60 本就一致 |

所以 A.24 那句话是准的：病灶确实只在 DDL 那两份。但**只有查过才敢说它准**。

顺手把同一套口径扩到语料里另一个纯人读字段 `note`（同一批复制粘贴最可能一起带过来的东西）：

| 语料目录 | 份数 | 去重后 note 组数 | note 与他份逐字相同 |
|---|---|---|---|
| `test/schema-diff-vectors` | 16 | 16 | 无 |
| `test/schema-ddl-vectors` | 26 | 24 | **1 组 = d14、d22、d23 三份共用同一段** |
| `test/schema-select-vectors` | 18 | 18 | 无 |

命中的还是那三份文件——同一次复制粘贴同时留了 `name` 和 `note` 两处漂移。
d22/d23 的 `note` 描述的是 **d14 的 golden 输出**（"整组语句输出在第一个条目位置、entry_ids 吸收四条…"），
而这两份**根本不出脚本**（都在勾选层报错、`expected` 为空）⇒ 读者按 note 去找断言会完全找错方向。
`note` 比 `name` 更"没人管"：Go 侧结构体有 `Note` 字段但从不用，Rust 侧 `Vector` 连这个字段都没声明（serde 直接忽略）
⇒ 它连进失败消息都不会，纯靠人读。

**为什么它能活这么久**：`name` 在六处加载器里只用来拼失败消息，**不进任何断言**
⇒ 写错永远不会让测试红。反过来，也正因为它不进断言，改它对行为**零影响**（这是本批"行为等价"的根据）。
语料生产器（Go 两个 writer、Rust `regen_expected.rs`）只回写 `expected`，`name` 由人手写 ⇒
漂移没有自动出口，只能补在**读取侧**。

**处置（四件事）**

1. 两行语料改回与文件名一致：`d22_stale_selected_id.json`、`d23_rebuild_group_must_be_selected_whole.json`；
   同时把这两份的 `note` 重写成各自真正钉的东西（d22 = stale **优先于**整组校验、d23 = 缺组文案按**计划序**列举），
   d14 的 note 原样保留（那才是它描述的输出）。
2. 六处加载器各挂一条自检：Go `checkVectorName`（写在 `vectors_test.go`，同包三处共用一份），
   Rust 三份集成测试各一份 `check_name`（Rust 的 `tests/*.rs` 每份是独立 crate，helper 无法共享 ⇒
   宁可重复 9 行，也不把测试工具塞进生产 crate 扩大 API 面）。
3. **一度写进两端的"目录内 name 查重"断言，当场删掉**。理由是它恒不可达：
   文件名在目录内本就唯一，而 `name == 去 .json 的文件名` 对每份都成立 ⇒ name 必然唯一。
   实测印证（见下面反证第二组）：让两份语料顶同一个名字，两端红的仍然是「与文件名不符」这条，
   且只报那一份不合法的文件。**d22/d23 的"重复"从来只是"≠ 自己文件名"的副产物**，一条断言已经全收。
4. `note` **刻意不加门禁**：它不参与任何断言、也没有"正确答案"可机检（措辞是人的事），
   把重复 note 一律判红会误伤"同一规则的第二份用例本就同因"这种正当写法。这次只修数据、立字为据。

**本批门禁**（2026-09-27，工作树 = 2 份语料的 `name`+`note` + 6 处加载器 + 本附录）：

| 项 | 命令 | 结果 |
|---|---|---|
| Go 格式 | 三份改动文件与 `gofmt` 输出比对（本批三份本就是 LF，无需剥 CR） | 全部已格式化（仍禁 `gofmt -w`，见 A.21） |
| Go 编译/静态 | `go build ./...`、`go vet ./pkg/schemadiff/` | 均 exit 0、无输出 |
| Go 三套语料 | `go test -count=1 -run 'TestSharedVectors\|TestSharedDDLVectors\|TestSharedSelectVectors' ./pkg/schemadiff -v` | **6 PASS**（每套"匹配期望"+"覆盖度"各一份） |
| Go 全量 | `go test -count=1 ./...` | **13 包全 ok、0 FAIL** ⇒ 与 A.21–A.24 基线同数 |
| Rust 单 crate | `cargo test -p polydb-schema-diff` | **46 passed / 0 failed**（40 lib + 2 diff + 2 DDL + 2 勾选）⇒ 与 A.24 同数 |
| Rust 全量 | `cargo test --workspace` | **181 passed / 0 failed**（41 个 result 行）⇒ **与 A.24 基线分毫不差**，本批不新增测试函数，只往既有加载器里加断言 |
| Rust 格式/哨兵 | `cargo fmt --all --check`、`cargo clippy -p polydb-schema-diff --all-targets -- -D warnings` | 均 exit 0、零警告 |
| 协议漂移 | `go run . ts --check` / `go run . check` | `TS types up to date` / `protocol check OK: 64 spec types vs rust + go` ⇒ 零 spec 变更 |
| 契约对拍 | `cargo build -p polydb-server` 后 `cd test/contract && go test -count=1 ./...` | 整包 **ok / 261.7s / 0 FAIL**（A.24 那次 265.7s）⇒ 与基线一致 |

**新护栏不是恒真？两组反证实测**（做完立刻按备份字节还原，md5 逐一比对前后一致）：

| 反证 | 结果 |
|---|---|
| 三套语料各挑一份把 `name` 改成 `<stem>_bei_gai_huai`（`01_identical` / `d02_create_table` / `s01_all_selected_null`） | **六个方向全红**：Go 三份测试各 FAIL 且消息点名该文件、Rust 三套各 2 FAILED（匹配 + 覆盖度，因为它们共用同一个 `load_all`）。还原后双端绿 |
| 让两份语料顶**同一个**名字（s02 的 `name` 写成 s03 的合法名） | 两端都只红在 s02，报的是「与文件名不符」⇒ 印证「查重」分支恒不可达，删得对 |
| 反证涉及的文件 | `abaefa65` / `79e54c4e` / `ef368cb1` / `65af02dc` / `84858e7c` 五份 md5 改前改后逐一对齐 |

**本批未取/未验证项（照例标出）**：① `golangci-lint` v2 本机仍不在 PATH（§7 第 ⑦ 条原样成立，
仍是唯一一直没跑的门禁）；② 契约这轮**没加 `-v`** ⇒ 顶层/子测试的 PASS 行数（基线 23 / 66）本批未重数，
只拿到整包 ok；③ Web 未跑（`web/` 零字节改动，A.24 基线 163 仍有效）；④ 三端 UI 零改动 ⇒ 向导未肉眼验证。

**净账**（主体提交 `901879a`，`git show --numstat 901879a` 复原）：**8 个文件 +75 / −6**——语料 **2 份各 +2/−2**（`name` 与 `note` 各一行），
Go 三份测试 **+16 / −0**（`vectors_test.go` +14 含 helper 与 `strings` import，另两份各 +1 处调用），
Rust 三份测试 **+55 / −2**（每份一个 13 行 `check_name` + `load_all` 由裸 `.map()` 改成先解析再校验），
外加本附录（含 A.24 遗留那句的指向）。`spec/`、规则层（`ddl.go`/`ddl.rs`/`schemadiff.go`）、三端前端**零字节改动**：
一笔账从头到尾没碰过生产代码，改的是数据与断言。

**读语料的人现在得到的保证**：任一目录下 `name` 与文件名不符 ⇒ 两端测试立刻红，并点名是哪一份文件。
O1 系列的"证据侧"到此闭合；下一笔真正的增量仍在**方言扩展**（PG/MySQL 的 `ddl_in_tx` 真值 + 依赖目录），
那需要另行拍板。**→ 已拍板并开工：附录 A.26（F0 方言取证基建，PG 侧 12 + 6 点探测）。**

### A.26 F0 方言取证基建（2026-09-27，**零规则代码 / 零 spec 变更：只加探测测试 + 一份取证清单**）

A.25 收口时留下的下一笔增量是**方言扩展**。开工前量化：本机 5432/3306 **无监听**（`netstat` 无一行命中）、
PATH 里没有 `docker` ⇒ 现在能交付的只有**取证基建**，真值要等引擎到位。经拍板：
① 引擎由用户起一台本地 PG；② 本批范围＝**取证基建先行**。所以本批的产物是**一套会在 PG 到位时自己开口说话的探测**，
不是任何规则改动。

**取证点从哪来**：§8 里所有「等有可实测的方言实例再定」的欠账（§8-3 `generic_type` 命名表、§8-7 四方言
`on_update`/`on_delete`、§8-6 同库多 schema），加上 A.15/A.20 关闭 SQLite 视图盲区时留下的三个"只在 SQLite 上验过"的
外推假设——A.11/A.15 那 17 条 G 项事实**全部**是 SQLite 实测，PG 上一条都没对过，
所以它们现在是**外推**而不是结论。

**12 点清单**（P1–P12）：

| 点 | 探什么 | 要钉进规则层的哪条口径 | 分档 | 回填去处 |
|---|---|---|---|---|
| P1 | DDL 进事务、整体回滚是否成立（事务内可见 / 回滚后连接池视角消失） | `Script.ddl_in_tx` 现在**硬编码 true**（`ddl.go:851`、`ddl.rs:892`），只被 SQLite 取证背书；PG 若为 false ⇒ 三端"逐条执行"分派才第一次真正用得上 | 驱动（+判据） | §8 新增能力位；A.16 的分派表；`spec/metadata.json` 的 `ddl_in_tx` 注释 |
| P2 | `DROP TABLE` 被依赖视图挡住，还是静默留破视图 | 生成器删表前要不要先发依赖对象（A.20 B 档的前提）；对照 SQLite G16"静默留破视图" | 引擎 | A.20 的 B 档可行性结论 |
| P3 | `ALTER TABLE … DROP COLUMN` 被视图依赖挡住吗 | `drop_column` 在 PG 上是否需要整组重建（SQLite 靠整表重建绕过） | 引擎 | §8 新增"哪些动作触发重建"的方言表 |
| P4 | `RENAME` 是否重解析引用它的视图 | **整表重建四步（CREATE tmp → INSERT SELECT → DROP → RENAME）在 PG 上能不能走**——SQLite 上正是死在这一步（A.15 G13） | 引擎 | A.11/A.15 的重建规则是否可按方言放开 |
| P5 | `ALTER COLUMN … TYPE` 是否需要 `USING`、视图是否挡路 | 类型变更条目走直改语句还是走重建；改完驱动能否读回新类型（否则 diff 以为"没变"） | 引擎 | §8-3；重建触发条件表 |
| P6 | 非空表 `ADD COLUMN NOT NULL DEFAULT` 是否即时（判据用 `atthasmissing`，不量时间） | SQLite 的"NOT NULL 无默认 ⇒ 重建"在 PG 上是否需要；`add_column` 要不要拆两支 | 引擎 | §8-3；A.11 的 G 项对照表 |
| P7 | identity / serial 的自增回读 | `is_auto_increment` 填得出吗（SQLite 侧恒 false 是 A.11 G3 实测的）；判不出自增 ⇒ 会误生成重建 | 驱动 | §8-3；PG 方言的重建触发条件 |
| P8 | 主键/唯一约束的自动索引**名字形状** | diff 拿索引名当身份键（SQLite 是 `sqlite_autoindex_*`）；PG 的名字由约束名派生、含表名 ⇒ 同名不同表的索引在 PG 上天然不撞，规则层要不要据此放宽 | 驱动 | A.5 匹配规则的方言注 |
| P9 | 外键 `on_update`/`on_delete` 回读 | **§8-7 的债本体**。已确认缺口在**读侧不在引擎**：Go 的 `ListForeignKeys` 连查都没查 `confupdtype`，Rust `db-postgres/src/lib.rs:571` 直接 `on_update: None`；本条要钉的是"目录给不给得出"这另一半 | 驱动 | §8-7 定案 → 补读 SQL + 双端 DTO 填充 |
| P10 | `format_type` 的原始文本形状（14 种类型） | **§8-3 `generic_type` 归一命名表的输入**。那张表一直没落笔，等的就是可实测的方言实例 | 驱动 | §8-3 定案（含 `MaxLength/Precision/Scale` 该从文本里拆还是另查） |
| P11 | `ListTables` 是否真的尊重 `schema` 入参 | **§8-6 同库多 schema（v1.1）**。SQLite 驱动忽略入参（A.9 F6），PG 读代码是 `WHERE n.nspname = $1`——两头都要实测才算"可行" | 驱动 | §8-6 v1.1 是否开工 |
| P12 | `pg_depend` 能否枚举"哪些视图引用了这张表/这一列"（表级 + 列级两条查询） | **A.20 B 档的可行性**：B 档在 SQLite 上被判死（无依赖目录），若 PG 的 `refobjsubid` 能指到列，则"依赖可见"是**方言能力位**而非全库盲区 | 引擎 | §8-8 补注：B 档按方言分级 |

**为什么这样分档**（引擎事实 vs 驱动事实）：P2–P6、P12 是 **PG 服务器自己的脾气**，与客户端用 Go 还是 Rust 无关——
两端各探一遍只会造出"两个实现两个真值"的假象（本项目最贵的一类缺陷）。P1、P7–P11 是 **pgx 与 sqlx 各自把系统目录读成了什么**，
必须两端各探，因为双实现漂移恰恰发生在这一层。⇒ Go 侧 12 点全探，Rust 侧只镜像 6 个驱动点。

**落点与形态**：

| 文件 | 内容 | 规模 |
|---|---|---|
| `go/pkg/dbpostgres/probe_test.go` | P1–P12 全 12 点，`package dbpostgres`（要读驱动私有形状，只能是包内测试） | 新文件 605 行 |
| `rust/crates/db-postgres/tests/probe.rs` | P1、P7–P11 六个驱动点，`#[tokio::test]` | 新文件 405 行 |
| `rust/crates/db-postgres/Cargo.toml` | 加 `[dev-dependencies] tokio`——**测试专用**，非生产依赖 | +5 行 |

`db-postgres` 此前**没有 `tests/` 目录**，本批是 Rust 侧第一份活 PG 测试，所以软跳过口径要在那儿现建（见下面坑 ②）。
探测现场统一：专用 schema `polydb_probe`，每个测试开头 `DROP SCHEMA IF EXISTS … CASCADE` + `CREATE SCHEMA`，
`Cleanup` 再 CASCADE 一次——探测会在真库上建表建视图，绝不能落在 `public` 里污染使用者的数据。
DSN 串与 `test/contract` 同一口径（`POLYDB_TEST_PG`，默认 `postgres://postgres@localhost:5432/postgres?sslmode=disable`）
⇒ 契约与本探测要么都能连，要么都静默跳过，不会出现"一边真跑一边跳过"的假对拍。

**断言只打三类东西**（这是本批的设计核心，不是偷懒）：

1. **我们自身层的形状不变式**：列数、`ordinal_position` 与回读序一致、外键本体读得到、索引名非空且不重复、
   `DROP … CASCADE` 之后对象计数归零。这些与 PG 的脾气无关，红了就是**我们读侧坏了**。
2. **该点要钉的那条判据本身**：只有 P1（回滚后必须归零）、P11（探测 schema 查得到 + `public` 查不到）、
   P12（`pg_depend` 必须枚举得出物化视图对 `qty` 的列级引用）三处下赌注——它们的红**正是**要拿到的证据，
   且都是 PG 文档级行为，红了说明环境不对或口径变了。
3. **纯两可的分支只记不赌**：P2 的"挡 / 不挡"、P3 的"能不能删无人引用的列"、P6 的"要不要 `USING`"——
   两条分支各自决定一条不同的生成器规则，**预先押哪条都是猜**。

所有观测以 `OBS[px]` 前缀打印，引擎到位后一条 grep 收齐真值：

```bash
export POLYDB_TEST_PG='postgres://user:pw@localhost:5432/postgres?sslmode=disable'   # 密码走环境变量，不落文件不落日志
cd go   && go test -count=1 -v ./pkg/dbpostgres/ 2>&1 | grep -o 'OBS\[.*'            # 12 点
cd rust && cargo test -p polydb-db-postgres -- --nocapture 2>&1 | grep -o 'OBS\[.*'  # 6 点驱动事实
```

**本批踩到 / 立此存照的三处坑**：

① **Go 侧探测事务只能走 `Begin` + `ExecuteIn`**。`Conn::Execute` 与 `Begin` 都抢同一把 `c.mu`，
而裸 `BEGIN` 经 `Execute` 会落到 pgx 连接池的某一条连接上、把那条连接留在事务态——后续探测被静默污染。
（第一版探测里真写了 `BEGIN`/`COMMIT`，跑之前自查删掉了。）
② **Rust 的软跳过在汇总里长得和"真跑通过"一模一样**：libtest 没有 SKIP 状态，
6 个探测缺引擎时全是 `test result: ok. 6 passed`，只有 `--nocapture` 才看得见 `SKIP[…] connected:false` 那行。
Go 侧有 `--- SKIP` 可见（本批实测 12 行）。⇒ 刷新 A.21 基线表时，Rust 侧的 passed 数**不能**当作"PG 探过了"的证据，
必须回头 grep `SKIP[` 或 `OBS[`。
③ **sqlx 的 PG 不能套 `smol::block_on`**（db-sqlite 那套测试的做法）：sqlx 编译在 `runtime-tokio` 上，
包进 smol 会 panic「no reactor running」⇒ 本 crate 用 `#[tokio::test]`。
另外 `PostgresConn::open` 是**懒连接**，池建不出来不代表连不上，所以"能不能探"的唯一真探是 `ping()`；
缺引擎时每条探测各等 `acquire_timeout` 5 s，6 条并行共 5.01 s——将来若把 Rust 侧探测加多，这个常数要重估。

**为什么本批不做 MySQL**（范围收缩，明示不隐藏）：拍板的引擎可得性只有 PG 一台。
MySQL 的取证点**不能复用 PG 那 12 条**——至少四处结构上不同：MySQL 的 DDL **隐式提交**（P1 的判据方向相反）、
无物化视图（P12 的清单形态不同）、无 `pg_depend`（依赖要改查 `information_schema`）、
自增只有 `AUTO_INCREMENT` 一种（P7 的 identity/serial 分野不存在）。
照抄一份 12 点 MySQL 探测就是本文件反复回避的"数字上已实现、实际未验证"。⇒ 记 **F0.4：MySQL 取证点清单另立**，
等 MySQL 引擎到位（或用户明确要盲写）再拍板。

**本批门禁**（2026-09-27，工作树 = 2 份新探测 + 1 份 dev-dependency + 本附录；规则层 / `spec/` / 三端前端零字节改动）：

| 项 | 命令 | 结果 |
|---|---|---|
| Go 格式 | 新文件与 `gofmt` 输出比对（`gofmt -l` 空 = 已格式化；本批新文件本就是 LF，无需剥 CR） | 通过（仍禁 `gofmt -w`，见 A.21） |
| Go 编译/静态 | `go build ./...`、`go vet ./...` | 均 exit 0、无输出 |
| Go 探测 | `go test -count=1 -v ./pkg/dbpostgres/` | **12 份 `--- SKIP`、0 FAIL** ⇒ 12 点全部 `connected:false` 软跳过，**真值 0 条** |
| Go 全量 | `go test -count=1 ./...` | **14 包全 ok、0 FAIL**（A.25 基线 13 包 ⇒ `pkg/dbpostgres` 首次有测试文件，多出的那一包就是它） |
| Rust 单 crate | `cargo test -p polydb-db-postgres` | **6 passed / 0 failed**，42 result 行之本 crate 一支；耗时 5.01 s ＝ 6 条并行各等 5 s `acquire_timeout` |
| Rust 软跳过可见性 | `cargo test -p polydb-db-postgres -- --nocapture` | **6 行 `SKIP[…] connected:false`**（不加 `--nocapture` 就只能看见 6 passed——见上面坑 ②） |
| Rust 全量 | `cargo test --workspace` | **187 passed / 0 failed**（42 个 result 行）＝ A.25 基线 181 **+6**，那 6 条**全是软跳过** ⇒ 有效新增真跑断言 **0** |
| Rust 格式/哨兵 | `cargo fmt --all --check`、`cargo clippy -p polydb-db-postgres --all-targets` | 均 exit 0、零警告（新文件用 `rustfmt --edition 2021` 单文件写模式格式化，绕开 `cargo fmt --all` 的写模式门禁） |
| 协议漂移 | `go run . check` / `go run . ts --check` | `protocol check OK: 64 spec types vs rust + go` / `TS types up to date` ⇒ 零 spec 变更 |
| 契约对拍 | `cargo build -p polydb-server` 后 `cd test/contract && go test -count=1 ./...` | 整包 **ok / 265.2s / 0 FAIL**（A.24 265.7s、A.25 261.7s）⇒ 与基线一致 |

**本批未取/未验证项（照例标出）**：
① **12 + 6 个取证点一条真值都没拿到**——本机 5432 无监听，这是本批的既定结果而不是意外；
所有 `OBS[…]` 行尚未打印过一次，探测代码本身**只经过编译与 clippy 检验，没经过引擎检验**。
换句直白的：PG 到位那一次跑，很可能第一遍就红在若干条上（SQL 串写错、regclass 大小写、`attnum=2` 这类位置假设），
**那是本批设计好的第一次真跑**，不算回归；② MySQL/MSSQL/Oracle/Redis 连清单都没有（F0.4）；
③ `golangci-lint` v2 本机仍不在 PATH（§7 第 ⑦ 条原样成立，仍是唯一一直没跑的门禁）；
④ 契约这轮没加 `-v` ⇒ 顶层/子测试 PASS 行数未重数，只拿到整包 ok；⑤ Web/TUI/GUI 三端零改动，未跑未看。

**净账**（主体提交 `af8553e`，`git show --numstat af8553e` 复原）：**4 个文件 +1016 / −0**——
`go/pkg/dbpostgres/probe_test.go` 新 605 行、`rust/crates/db-postgres/tests/probe.rs` 新 405 行、
`Cargo.toml` +5（dev-dependency）、`Cargo.lock` +1。规则层（`ddl.go`/`ddl.rs`/`schemadiff.go`/`diff.rs`）、
`spec/`、三端前端**零字节改动**。

**引擎到位后的回填流程（F0.3 → 记为下一批）**：
① 上面那条 `grep OBS` 一次收 18 点真值；② 逐条把观测转成**固定断言**（删掉"只记不赌"的那一类，
因为届时已知哪一支成立）；③ 回填本表"回填去处"那一列点到的条目——`§8-3` 定名表、`§8-7` 补读 SQL、
`§8-6` v1.1 判据、`§8-8` B 档分级、A.11/A.15 的 G 项方言对照；④ 才轮到规则层动手：
`ddl_in_tx` 从硬编码改成按方言取（P1）、重建触发条件加方言维度（P3–P6）、依赖目录进快照（P12）。
顺序不能反：**先有真值，再改生成器**——反过来说就是 A.26 立这份清单的全部理由。
