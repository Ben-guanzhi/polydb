# polydb Compare & Sync（模式比对与同步）— 里程碑拆解

> 状态：**拆解稿，未开工**。从 `docs/plan-ui-tablepro-phase3.md` §阶段 D「仍不排期」四件中提出单列。
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
| 元数据读面 | `SqlDriver` 六方法（schemas/tables/columns/indexes/foreign_keys/create_table_sql），5 个 SQL 驱动 × Rust/Go 两端**全部实现**（`rust/crates/db-core/src/driver.rs:29-35`、`go/pkg/dbcore/dbcore.go:25-30`）；DTO 在 `spec/schemas/metadata.json:6-111` | 无（够用） |
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
| E1 diff 纯函数 + 归一层 | Rust ~450 行 + Go ~400 行 + 单测各 ~250 行 | 零 IO、零 UI，双实现对拍可提前建立 |
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

**D5 diff 输出是"计划"不是"字符串"。**
DTO 用结构化条目（`op`：create_table / add_column / drop_column / alter_type / alter_nullability /
alter_default / add_index / drop_index / add_fk / drop_fk / drop_table；带对象标识、两侧快照、
建议语句、`selected` 默认勾选态、`reason`）。这样三端渲染和勾选/反选不需要重新解析 SQL，
也和 §13 过滤条件那样保持"协议字段说话、前端不自作聪明"。

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

- **E1 纯函数层**：Rust `app-core/src/schema_diff.rs` + Go `pkg/appcore/schemadiff.go`
  （输入两侧元数据快照，输出结构化 plan）+ 两端各自单测。验收：同一份 JSON 快照喂两端，
  输出条目序列逐字一致。
- **E2 协议与服务端**：spec 改动 → `gen-protocol` → 两端 DTO + 两个端点 → 契约测试
  （**SQLite 同库两 schema 真跑**；PG/MySQL/MSSQL/Oracle 按既有 `connected:false` soft-skip）。
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
- 只有 SQLite 能端到端真跑（同文件库或两库）——所以 v1 的验收以 SQLite 为主，
  其余方言的正确性依赖单测 + 后续在有实例的环境补测。
- `golangci-lint` v2 本机未安装（Go lint 步骤恒跳过）；GUI(GPUI) 与 Web 渲染无法无头肉眼验证。

## 8. 未决问题（开工前需要拍板）

1. ~~D1 选推荐（服务端）还是备选（三端各自实现）？~~ **已定：服务端一处**（2026-09-25），
   E3 的语句生成因此同样落在服务端（两端各一套 + 契约对拍），前端不自带方言规则。
2. 是否需要"生成回滚脚本"进 v1？（D4 依赖它，成本约 +400 行）
3. `generic_type` 的归一命名表由谁定：以 PG 命名为准、还是自创中立名（`int64`/`text`/`timestamp`…）？
4. 差异条目里"仅目标库存在"要不要包含**数据不可逆**提示（drop column 会丢数据）——建议 v1 强制红标 + 默认不勾选。
5. 表数上限：GUI 现有整 schema 外键扫描硬上限 200 表（`ui-gui/src/app.rs:2505-2529`），diff 场景动辄上千表，要不要在 E2 一并放宽/分页？
