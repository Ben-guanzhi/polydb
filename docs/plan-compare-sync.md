# polydb Compare & Sync（模式比对与同步）— 里程碑拆解

> 状态：**E1 已落地（2026-09-25，纯函数层双实现 + 共享向量对拍）**；**E2.0 真快照复核已落地（2026-09-25，
> SQLite 真引擎采快照喂 diff，抓到 3 个驱动缺陷并双端修复，见附录 A.9）**；E2 主体（spec DTO + 端点 + 契约）与 E3–E5 未开工。
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
| DDL 生成 | **只有 Web 一处**：`web/src/lib/alterSql.ts`（quoteIdent/addColumn/dropColumn/renameColumn/addIndex/dropIndex/addFK/dropFK + `UnsupportedAlterError`）。GUI、TUI **零 DDL 生成**（`ALTER TABLE` 只出现在两端自用 storage 里） | ★ 缺 ALTER COLUMN（类型/可空/默认）、缺 CREATE/DROP TABLE |
| 原子应用 | 事务四方法两端齐备（`app-core/src/lib.rs:502-579`、`pkg/appcore/appcore.go:338-435`），finalize 只成功一次；`POST /query/batch` 是**逐条循环、不在事务内**（`crates/server/src/lib.rs:663-681`、`go/pkg/server/server.go:441-473`） | "原子应用一批 DDL"只能客户端 begin → N×execute → commit（已有成熟范式：`ui-gui/src/app.rs:2133-2179`、`TableStructureEditor.tsx:115-135`） |
| DDL 事务能力位 | **全仓库没有任何一处**处理"某方言 DDL 不能在事务里"（MySQL/Oracle 的 DDL 隐式提交） | ★ 需新增能力位 + 显式报错，禁止静默降级（矩阵见 `spec/behavior.md:334-345`） |
| 快照结构可复用件 | `web/src/lib/erDiagram.ts:74 buildERModel()` 已是"表→列→FK"的纯结构；GUI `app.rs:2505-2529` 有整 schema FK 扫描（**上限 200 表**）、TUI `commands.go:211 allTablesCmd` 跨 schema 枚举 | 表数上限与逐表 fan-out 往返要处理 |
| Rust 侧远程传输 | **不存在 HttpTransport**（只有 `LocalTransport`，`transport/src/lib.rs:103`）。Go 侧 Local + Remote 都有 | ★ 决定 GUI 形态（见 §4 决策 D3） |

**结论**：读面齐、归一缺、生成半齐（只在 Web）、应用通道齐但能力位空白、跨进程比较在 GUI 侧结构性受限。

## 3. 规模预估（用于排期，不是承诺）

| 批次 | 净新增（约） | 说明 |
| --- | --- | --- |
| E1 diff 纯函数 + 归一层 | ✅ 实际 3641 行：Rust 1675（lib 1010 + normalize 434 + 向量测试 102 + 生成工具 129）、Go 1344、共享向量 16 份 622 行 | 零 IO、零 UI、不生成 DDL；双实现对拍已建立（详见附录 A） |
| E2.0 真快照复核 | ✅ 实际 687 行测试（Rust 368 + Go 319）+ 驱动净改 103 行（Rust +44/−24、Go +59/−16） | 零 spec 变更；抓出 3 个驱动缺陷（附录 A.9 F1–F3） |
| E2 spec DTO + 两端端点 + 契约 | spec ~120 行、两端各 ~120 行、契约 ~200 行 | 唯一需要 spec 变更的批次 |
| E3 DDL 生成器补全 | Rust/Go 各 ~350 行 + 单测（**D1 定案后落在服务端**，前端只渲染脚本） | 含 ALTER COLUMN 三态、CREATE/DROP TABLE、顺序依赖与回滚脚本 |
| E4 Web 向导 | ~700 行（四步 + 预览 + 状态持久化） | 体量参照 M30 导入向导的 1/3 |
| E5 GUI + TUI 收敛形态 + 文档 | GPUI ~400 行、TUI ~350 行 | 渲染无法无头验证（§7） |
| 合计 | **约 4.3k 行**（含测试） | 体量参照：约等于当前整个 `rust/crates/ui-gui`（10,048 行）的 43% |

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

**D5 diff 输出是"计划"不是"字符串"。—— E1 已按此实现，形状固定为 9 个 op。**
`op`：`create_table` / `drop_table` / `add_column` / `drop_column` / `alter_column` /
`add_index` / `drop_index` / `add_foreign_key` / `drop_foreign_key`；
列的三类变化（类型 / 可空性 / 默认值 / 自增）**合并成一条 `alter_column`**，用 `flags`
数组（`type` / `nullability` / `default` / `auto_increment`）表达改了哪几维——
这样 E3 生成一条 `ALTER TABLE … ALTER …` 还是多条，是方言选择，不必在协议里裂成三个 op。
每条带 `id`（`{表}/{对象}/{op}`，供 UI 记忆勾选）、`phase`、`from`/`to`、`destructive`、
`selected`、中文 `reason`。**不含语句**（语句属 E3）。

## 5. 契约影响（spec/ 真相源，逐条标注）

1. `spec/schemas/metadata.json` 新增 `SchemaDiffEntry` / `SchemaDiffPlan`（**non-breaking**，纯新增）。
2. `spec/openapi.yaml` 新增 `POST /api/schema/diff`（入参：源/目标 connection_id + schema + 选项；
   出参：`SchemaDiffPlan`）与 `POST /api/schema/ddl`（计划 + 勾选态 → 有序语句 + 回滚脚本）。
   **non-breaking**。
3. `spec/behavior.md` 新增 §14：diff 归一规则、条目排序依赖（先父表 FK 后子表、先建列后建索引）、
   大小写与引号标识处理、**能力矩阵补 `ddl_in_tx` 一行**。
4. `ColumnInfo.generic_type` 补语义说明（字段本身已存在 ⇒ non-breaking，但**双实现必须同时填**，
   否则契约测试会立刻抓到差异）。
5. 提交前跑 `tools/genprotocol`：`ts --check` + `check`（当前基线：54 spec types vs rust + go 零漂移）。

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
- **E2 协议与服务端**：spec 改动 → `gen-protocol` → 两端 DTO + 两个端点 → 契约测试
  （**SQLite 按两个连接/两个文件真跑**——ATTACH 的"同库两 schema"路径驱动层不支持，见 A.9 F6，
  是否补做待拍板（§8-6）；PG/MySQL/MSSQL/Oracle 按既有 `connected:false` soft-skip）。
  验收门禁：`check-protocol` + workspace clippy/test + `go vet` + 契约。
- **E3 语句生成**：在服务端补齐生成器（ALTER COLUMN 的类型/可空/默认三态、CREATE/DROP TABLE、
  依赖排序、回滚脚本），Web 现成的 `alterSql.ts` 继续服务结构编辑器，**不作为 diff 的出口**
  （避免同一个方言规则存在两套真相源）。
- **E4 Web 向导**：四步（选源 → 选目标 → 差异预览 → 脚本/应用），复用导入向导的
  `Step` 联合类型 + `polydb.<feature>.v1` localStorage 命名 + busy 门禁 + 失败详情展示惯例。
- **E5 GUI/TUI + 文档**：GUI 浮层/独立面板（同种类两 schema，D3），TUI 三段视图；
  `docs/frontends.md` 补能力差异行；三端一致回归 + 全端门禁基线刷新。

## 7. 本机不可验证清单（先把话说在前面）

- 本机**无 Docker、无监听中的** 5432/3306/6379/1433/1521 ⇒ PG/MySQL/MSSQL/Oracle/Redis 相关
  分支只能 soft-skip（当前基线：契约 51 绿里含 10 处驱动跳过）。**跨库 diff 的正确性、
  各方言 DDL 语法、`ddl_in_tx` 真值都无法在本机实测**。
- 只有 SQLite 能端到端真跑（**两个文件 / 两个连接**；E2.0 实测确认驱动不支持 schema 寻址，
  "同库两 schema"需先拍板 §8-6）——所以 v1 的验收以 SQLite 为主，
  其余方言的正确性依赖单测 + 后续在有实例的环境补测。
- `golangci-lint` v2 本机未安装（Go lint 步骤恒跳过）；GUI(GPUI) 与 Web 渲染无法无头肉眼验证。

## 8. 未决问题（开工前需要拍板）

1. ~~D1 选推荐（服务端）还是备选（三端各自实现）？~~ **已定：服务端一处**（2026-09-25），
   E3 的语句生成因此同样落在服务端（两端各一套 + 契约对拍），前端不自带方言规则。
2. 是否需要"生成回滚脚本"进 v1？（D4 依赖它，成本约 +400 行）
3. `generic_type` 的归一命名表由谁定：以 PG 命名为准、还是自创中立名（`int64`/`text`/`timestamp`…）？
4. 差异条目里"仅目标库存在"要不要包含**数据不可逆**提示（drop column 会丢数据）——建议 v1 强制红标 + 默认不勾选。
5. 表数上限：GUI 现有整 schema 外键扫描硬上限 200 表（`ui-gui/src/app.rs:2505-2529`），diff 场景动辄上千表，要不要在 E2 一并放宽/分页？
6. **SQLite 的比对形态**（E2.0 实测后新增）：驱动层 `list_tables` 忽略 `schema` 入参、`PRAGMA table_info` 不支持
   schema 前缀 ⇒ "同库两 schema"（ATTACH 后分别采快照）**目前不可行**。二选一：
   (a) v1 只做"两个连接 / 两个文件"，SQLite 的 `schema` 参数固定 `main`（推荐，零驱动改动）；
   (b) 给两端 SQLite 驱动加 ATTACH + `PRAGMA attached.tbl` 寻址（改动面大、且 `Connection` 是池化的，
   ATTACH 只在单连接上有效，与连接池语义冲突）。
7. **其余 4 个方言（PG/MySQL/MSSQL/Oracle）的 `on_update`/`on_delete` 与 `generic_type` 何时填**：
   两端**五个 SQL 驱动全未填**，本轮只补了 SQLite。现在补等于**本机无法验证**（无实例），
   只能靠读 information_schema / SHOW CREATE 文档写映射；建议随 E3 或有实例环境时一并补并复核。

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
| F6 | SQLite 驱动**不做 schema 寻址**：`list_tables(_schema)` 忽略入参，`PRAGMA table_info("t")` 无 schema 前缀 | E1 假设的"同库两 schema"比对形态在 SQLite 上**目前不可行**（ATTACH 后也拿不到 `attached_db` 的表） | SQLite 比对只能退化为**两个连接 / 两个文件**。要不要实现 ATTACH + schema 限定 PRAGMA 待拍板（见 §8） |
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
