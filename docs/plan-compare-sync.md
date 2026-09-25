# polydb Compare & Sync（模式比对与同步）— 里程碑拆解

> 状态：**E1 已落地（2026-09-25，纯函数层双实现 + 共享向量对拍）**；**E2.0 真快照复核已落地（2026-09-25，
> SQLite 真引擎采快照喂 diff，抓到 3 个驱动缺陷并双端修复，见附录 A.9）**；**E2 主体已落地（2026-09-25，
> spec 7 个 DTO + `POST /api/schema/diff` 双端端点 + 契约 SQLite 两文件真跑，见附录 A.10）**；
> **E3.0 SQLite DDL 取证已落地（2026-09-25，附录 A.11 G1–G9）**；**E3 生成器与 `/api/schema/ddl` 双端已落地
> （2026-09-25，附录 A.12）**；**E4 Web 向导已落地（2026-09-26，纯前端零 spec 变更，附录 A.13）**；E5 未开工。
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
| 原子应用 | 事务四方法两端齐备（`app-core/src/lib.rs:502-579`、`pkg/appcore/appcore.go:338-435`），finalize 只成功一次；`POST /query/batch` 是**逐条循环、不在事务内**（`crates/server/src/lib.rs:663-681`、`go/pkg/server/server.go:441-473`） | "原子应用一批 DDL"只能客户端 begin → N×execute → commit（已有成熟范式：`ui-gui/src/app.rs:2133-2179`、`TableStructureEditor.tsx:115-135`） |
| DDL 事务能力位 | **全仓库没有任何一处**处理"某方言 DDL 不能在事务里"（MySQL/Oracle 的 DDL 隐式提交） | ★ 需新增能力位 + 显式报错，禁止静默降级（矩阵见 `spec/behavior.md:334-345`）→ **E3 只对 SQLite 立了两位**（`ddl_in_tx=true` 实测、`pragmas_outside_tx` 由脚本自带），其余方言的取值仍空白 |
| 快照结构可复用件 | `web/src/lib/erDiagram.ts:74 buildERModel()` 已是"表→列→FK"的纯结构；GUI `app.rs:2505-2529` 有整 schema FK 扫描（**上限 200 表**）、TUI `commands.go:211 allTablesCmd` 跨 schema 枚举 | 表数上限与逐表 fan-out 往返要处理 |
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
| E4 Web 向导 | ~700 行（四步 + 预览 + 状态持久化） | ✅ 实际 **1228 行**：新建 1132（纯函数 262 + 单测 184 + 组件 686）+ 5 个改动文件 +96/−4。比预估超 1.75×，多出来的是执行编排的三种分派与整组勾选补齐；零 spec 变更、零后端改动（附录 A.13） |
| E5 GUI + TUI 收敛形态 + 文档 | GPUI ~400 行、TUI ~350 行 | 渲染无法无头验证（§7） |
| 合计 | 预估 **约 4.3k 行**（含测试）；**E1–E4 实际已 14.1k**（上表 ✅ 行之和） | 超估的原因是共享语料：向量本身即测试资产，一份语料双端跑。E4 是唯一"零后端"批次，仍比预估多 1.75×（执行编排三种分派 + 整组勾选补齐）。E5 预估不变 |

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
- **E5 GUI/TUI + 文档**：GUI 浮层/独立面板（同种类两 schema，D3），TUI 三段视图；
  `docs/frontends.md` 补能力差异行；三端一致回归 + 全端门禁基线刷新。

## 7. 本机不可验证清单（先把话说在前面）

- 本机**无 Docker、无监听中的** 5432/3306/6379/1433/1521 ⇒ PG/MySQL/MSSQL/Oracle/Redis 相关
  分支只能 soft-skip（E3 收口时契约 **23 个顶层测试 / 66 个子测试全绿**，其中方言/Redis 分支只是
  "未打崩"而非"真跑"；上一基线记的「51 绿」是同一含子测试口径，E3 新增 3 个顶层测试后为 66）。
  **跨库 diff 的正确性、各方言 DDL 语法、`ddl_in_tx` 真值都无法在本机实测**。
- E3 因此**只交付 SQLite**：4 种方言 + Redis 的 `NOT_SUPPORTED` 拒绝路径由语料 d16–d19/d20/d21
  （纯函数层，恒可跑）与两端错误码映射（`NOT_SUPPORTED → 501`）双重锁定，
  但**契约层的 501 响应本机从未真正打出过**（`/api/schema/ddl` 的方言分支同样 soft-skip）。
- 契约里的"应用脚本"是**逐条 `/query` 顺序执行**，没有走 `/api/transactions`——因为含重建的脚本
  要求首尾两条 PRAGMA 留在事务外（G6）。**「把 `pragmas_outside_tx=false` 的脚本整份包进一个事务」
  这条真实调用路径仍未跑到**：E4 的 Web 向导已经实现了这条分派（附录 A.13），但 SQLite 两端能力位恒为
  `true`，所以浏览器里验证的是"PRAGMA 事务外 + 主体单事务"那一支；另一支（逐条 `/query`）只有纯函数
  单测覆盖，**契约层仍未对两种分派各加一条断言**。
- 视图（`CREATE VIEW`）对重建表的依赖行为**仍未测**（E3.0 遗留）：SQLite 视图按名字解析，
  预期不破，但没证据就不写进规则。
- 只有 SQLite 能端到端真跑（**两个文件 / 两个连接**；E2.0 实测确认驱动不支持 schema 寻址，
  "同库两 schema"需先拍板 §8-6）——所以 v1 的验收以 SQLite 为主，
  其余方言的正确性依赖单测 + 后续在有实例的环境补测。
- `golangci-lint` v2 本机未安装（Go lint 步骤恒跳过）；GUI(GPUI) 渲染无法无头肉眼验证。
  Web 侧 E4 已在真实 Chromium 实例里把向导全流程走通（附录 A.13「浏览器真跑」），但该实例视口固定在
  ~565×482 且没有可用的改窗口工具，所以**布局只做了数值核对**（行宽/标签宽用 `evaluate_script` 量），
  宽屏下的视觉排版仍未肉眼确认。

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
4. ~~差异条目里"仅目标库存在"要不要包含**数据不可逆**提示~~ **已定（2026-09-26，E4 落地即此）**：
   `destructive` 条目红标 + 默认不勾选，勾选初值直接采用服务端回值（前端不自造规则）；
   另有「破坏性动作默认勾选」开关供显式放开。见附录 A.13。
5. 表数上限：GUI 现有整 schema 外键扫描硬上限 200 表（`ui-gui/src/app.rs:2505-2529`），diff 场景动辄上千表，要不要在 E2 一并放宽/分页？
6. ~~**SQLite 的比对形态**~~ **已定（2026-09-25）：只做"两个连接 / 两个文件"**，SQLite 的 `schema` 参数固定 `main`。
   依据：驱动层 `list_tables` 忽略 `schema` 入参、`PRAGMA table_info` 不支持 schema 前缀（E2.0 实测，附录 A.9 F6）；
   而 ATTACH 只在单连接上有效，与现有连接池语义冲突。**"同库多 schema"记为 v1.1 待办**，spec 里 `schema` 保持可选。
7. ~~**其余 4 个方言的 `on_update`/`on_delete` 与 `generic_type` 何时填**~~ **已定（2026-09-25）：随 E3 / 有实例环境一起补**。
   理由：本轮只补了 SQLite，因为 SQLite 有 bundled 真引擎可实测；PG/MySQL/MSSQL/Oracle 现在补只能凭文档盲填、
   契约测试全 soft-skip，等于"数字上已实现、实际未验证"。与 §8-3 的 `generic_type` 命名表定案绑定推进。

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
5. 视图（`TableType::View`）在采集时跳过，"两端都跳过同一张视图"这一条只有单测覆盖，无真实例验证。

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
已在契约里同结论跑通（G10–G12）。**视图对重建表的依赖行为仍未测**（§7 最后一条）。

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
能力位恒为 `true`/`true`，另两支在本机仍没有真库可跑，详见 A.13；③ 视图对重建表的依赖未测；
④ Go 侧 `golangci-lint` 本机缺失；⑤ Web/GUI/TUI 三端渲染完全不涉及
（本批只有服务端 + 生成的 TS 类型）。

### A.13 E4 落地实况（2026-09-26，Web 向导，**零 spec 变更 / 零后端改动**）

**落点**（8 个文件：3 新建 + 5 改动）：

| 层 | 文件 | 行 |
|---|---|---|
| 纯函数层 | `web/src/lib/compareSync.ts` | 262（新建） |
| 纯函数单测 | `web/src/lib/compareSync.test.ts` | 184 / 17 份（新建） |
| 向导组件 | `web/src/components/CompareSyncWizard.tsx` | 686（新建） |
| API 包装 | `web/src/lib/api.ts` | +14（`schemaDiff` / `schemaDdl`，走既有 msgpack `request<T>`） |
| 入口接线 | `web/src/App.tsx` +41/−1、`ActivityBar.tsx` +31/−1、`SchemaBrowser.tsx` +13/−2、`ShortcutPanel.tsx` +1 |

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
