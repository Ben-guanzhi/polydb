# polydb 三前端重设计（对齐 TablePro 上游）— 第三阶段

> 前置：`docs/plan-tablepro-parity.md`（M10–M17 能力对齐）、`docs/plan-ui-tablepro-redesign.md`
> （U1–U7 Web 界面重设计）均已完成。本篇为**第三阶段**：把 **桌面 GUI（Rust GPUI）与
> TUI（Go bubbletea）** 向 TablePro 上游设计对齐，Web 收余留小差距。
>
> **许可红线（升级）**：TablePro 主体为 AGPLv3，**TableProGo 同为该仓库的一部分、同受 AGPLv3
> 传染**。此前仅参考 Swift 版交互；本阶段参考 TableProGo 的界面分层与 Go 逻辑层**语义**
> （过滤器 WHERE 构造、可回写判定、变更队列模型），**一律独立实现，不移植任何代码**。

---

## 1. 现状量化（2026-09-23 盘点）

| 前端 | 规模 | 后端能力接线 | 与 TablePro 上游差距 |
|---|---|---|---|
| Web | ~33k 行 | 全量 | 小：缺 Inspector 行详情尾板、结果 JSON/Output 视图模式；过滤 13 操作符 vs 上游 18 |
| Rust GUI | **1,807 行单文件**（ui-gui/lib.rs 1789 + main.rs 18） | 8/28 Transport 方法 | **最大**：无对象 tab 壳层、无过滤/排序/编辑、无连接 CRUD、结果网格不可滚动、78 处硬编码色 |
| Go TUI | 2,281 行（不含测试） | 15/24 Client 方法 | 大：单一线性 view 栈无焦点环、浏览固定 200 行无过滤无排序无精确计数、**完全无数据编辑**、无主题/QuickOpen/历史抽屉、busy 期间无法取消 |

### 1.1 GUI 已确认缺陷（重设计时一并修复）
- 结果容器无滚动视图，超窗口行数被裁掉（lib.rs:1426）。
- SQL 编辑器点击聚焦到新造的游离 FocusHandle（lib.rs:1672-1674）。
- 心跳 loop 无退出条件（lib.rs:565）。
- `refresh()` 强制踢回启动屏清空工作区（lib.rs:376-377）。
- `browse_rows` 的 `columns/conditions/logic/order_by` 协议字段全部留空（lib.rs:635-639）。
- 文档 `docs/frontends.md` 描述落后于代码三条。

### 1.2 TUI 已确认缺陷
- `browseCmd` 只填 Offset/Limit（commands.go:198-200），过滤/排序协议字段空置。
- `transport.Client` 未暴露事务四件套与 `BrowseRowsCount` → 编辑流结构性接不到。
- 无编辑入口；不能取消在途查询；连接只能删了重建（`UpdateConnection` 未用）。
- 长列表（连接/表/详情 DDL）无虚拟滚动，撑爆终端。
- kvDB 显示恒为 0 直到手动切换（model.go:108）。

## 2. 关键设计决策

1. **三前端统一"对象标签"信息架构**。Web 已落地（workbench + 表 tab × rails 概览/数据/结构/关系）。
   GUI 采用同一模型（跨端心智一致优先于照搬 TablePro 的六模式结果切换器）；
   TUI 采用 TableProGo 的收敛形态——**对象页四段 data/structure/query/history**，
   用单字符段标签实现，适配终端宽度。
2. **GUI 壳层 = TablePro 五区裁剪**：左缘连接条（窄竖条，状态形状+用户色）本阶段**不做**
   （GPUI 侧成本大、Web 验证过侧栏列表够用）；保留「侧栏树 + 对象 tab 条 + 主区 rails +
   顶栏连接 chip + 状态栏」。侧栏树补 schema→tables 层级与列展开（当前只有平铺表清单、仅首个 schema）。
3. **Inspector 行详情尾板**（TablePro 高价值差异件）三端同语义：选中行 → 右缘（GUI）/
   下方面板（TUI）逐字段展示与编辑，支持 Set NULL / DEFAULT / SQL 函数三态；
   Web 补只读版尾板对齐。
4. **变更跟踪编辑流**以 Web `lib/changes.ts` 语义为基准：GUI 在 Rust 侧、TUI 在 Go 侧
   各自独立实现纯函数队列（RowChange/sequence 保序/改回原值自动移除/Review SQL 预览/
   事务提交+行数校验/撤销栈），三端**行为一致 + 各自单测**，不做跨语言共享库。
5. **过滤栏对齐上游交互而非 18 操作符全集**：阶段 A/B 先复用后端当时已有的 13 op 协议
   （零 spec 变更）；操作符 13→18（contains/starts with/ends with/is empty/regex + Match Case）
   需 `spec/schemas/query.json` op 枚举扩充 + **集中式 WHERE 构造器两端各一处**
   （`rust/crates/db-core/src/browse.rs` / `go/pkg/dbcore/browse.go`，驱动只声明方言差异，
   原估「6 驱动×2 语言」高估约 6 倍）+ 契约测试，**列为另案**（见 §5）。
   ✅ **阶段 D1 已交付（2026-09-24）**：spec + 两端渲染 + SQLite REGEXP UDF + 三端过滤栏。
   Raw SQL 过滤行维持"不做"（注入面，parity 文档 §2-8 结论不变）。
6. **TUI 键盘模型借 TablePro 的"焦点决定键义"+ 面板焦点环**：`Tab/Shift+Tab` 巡览
   （侧栏↔编辑器↔网格↔详情），模态键表按焦点上下文分发；`Ctrl+P` QuickOpen
   （fuzzy 语义对齐 Web `lib/tableSearch.ts`，Go 独立实现）、`F1` 快捷键面板。
   保留现有键位不破坏肌肉记忆（F5 执行、F7 浏览等），TablePro 的 ⌘ 系键在终端映射为 Ctrl 系。
7. **主题 token 化**：GUI 把 78 处 `rgb(0x…)` 收进 `Theme` 结构（dark 为默认，补 light）；
   TUI 引入 lipgloss 统一样式（当前 raw ANSI 零散），支持 NO_COLOR 与 dark/light profile。
8. **取消边界如实记录**：TUI 本地模式（进程内）无法复用服务端在途查询取消（那是
   HTTP/WS 生命周期），用 `context.Context` 贯穿 driver execute 实现本地取消；GUI 同理经
   LocalTransport。远程模式走已有 cancel 端点。文档标注各模式生效范围。

## 3. 里程碑

### 阶段 A — Rust GUI 重设计（缺口最大，先行）

**G1 壳层重构 + 工程化拆分** 【L，核心】
- `ui-gui` 从单文件拆为 `theme/ shell/ sidebar/ workbench/ browse/ preview/` 模块。
- 对象 tab 条（workbench 常驻 + 表 tab 可多开，语义对齐 Web `lib/openTabs.ts`）；
  TableTab rails 概览/数据/结构/关系（对齐 Web `TableTab.tsx`，概览列画像复用
  `tableProfile` SQL 语义）；侧栏树 schema→表→列 + schema 切换。
- 主题 token 化（含 light）；修 §1.1 六项缺陷。
- 验收：GUI 连开 3 表独立翻页；切回 workbench 内容保留；`cargo test -p polydb-ui-gui` 绿。

**G2 数据浏览对齐** 【L】
- 过滤栏（13 op + AND/OR + 预设持久化到本地 SQLite 或 JSON 配置）、表头排序、
  页大小 50/200/1000、精确计数按钮、Inspector 尾板、可滚动网格（虚拟化）。
- 验收：过滤/排序走服务端，wire 与 Web 同表同条件结果一致。

**G3 变更跟踪编辑** 【L，依赖 G2】
- Rust 独立实现 changes 队列 + Review SQL 面板 + 事务提交/行数校验/回滚 + 撤销栈 +
  只读连接全禁用；补 `Transport` 已有事务端点接线。
- 验收：多格编辑一次事务、中途失败整体回滚队列保留。

**G4 连接管理 + 编辑器 + 杂项** 【M】
- 连接 CRUD 表单（含 read_only/SSH/颜色/分组显示）+ 测试；SQL 编辑器补选区/复制/粘贴/
  undo（不追 Monaco 级）；Quick Switcher（Ctrl+P）；导出 CSV/JSON/INSERT（语义对齐
  `lib/exporters.ts`）；Redis 键浏览（对齐 Web RedisBrowser 只读子集）。
- 验收：GUI 可独立创建/编辑连接，不再依赖 Web 建连接。

**G5 门禁与文档**
- `cargo fmt && clippy（零警告）&& test`；逐屏截图核对双主题；更新 `docs/frontends.md`。

### 阶段 B — Go TUI 重设计

**T1 焦点环 + 主题 + 渲染基建** 【M】
- 面板焦点环（Tab 巡览）+ 模态键表分发；lipgloss token 化（NO_COLOR/dark/light）；
  列表虚拟滚动（连接/表/结果视口渲染）；修 §1.2 kvDB 显示。
- 验收：任意面板可达可操作，长列表不撑爆终端。

**T2 对象页四段 + 浏览对齐** 【L】
- data/structure/query/history 四段对象页（借 TableProGo `ObjectPanel` 分层语义）；
  过滤栏（13 op，Go 独立实现条件→`FilterCondition` 构造）、排序、页大小、精确计数
  （`transport.Client` 增 `BrowseRowsCount`，Local/Remote 双实现，无 spec 变更）；
  QuickOpen `Ctrl+P` + `F1` 快捷键面板；历史抽屉（消费 `m.querySQL` 死字段 + 本地 JSONL）。
- 验收：TUI 内完成「搜表→过滤→排序→翻页」全链路。

**T3 变更跟踪编辑** 【L，依赖 T2】
- `transport.Client` 增事务四方法（Local 委托 appcore / Remote 打已有端点，无 spec 变更）；
  Go 独立实现 changes 队列纯函数包 + 单测；单元格编辑、Review、提交/回滚、撤销。
- 验收：与 Web/GUI 行为一致性用例（同场景三端步骤对照表）。

**T4 收尾** 【M】
- 取消在途查询（context 贯穿 + 远程 cancel 端点）；连接编辑（`UpdateConnection` 接线）；
  Redis `SetValue` 写回；EXPLAIN 树渲染（借 `lib/explain.ts` 语义独立实现）；
  文档与键位表更新。

### 阶段 C — Web 余留（穿插，小件）

- **W1** ✅（2026-09-24）：Inspector 行详情尾板（与 G2 同语义，只读+跳转编辑入口）；结果视图模式补
  JSON / Output(消息) 两页签。**纯前端，无 spec 变更。**
- **D1** ✅（2026-09-24）：过滤操作符 13→18 + Match Case 三态。落点：`spec/`（`FilterOperator` 枚举
  + `behavior.md` §13.5 三态语义 / §13.6 能力矩阵）→ 两端集中式渲染（`db-core/browse.rs`、
  `dbcore/browse.go`；SQLite 严格大小写走 `GLOB`、正则走驱动注册的 `REGEXP` UDF，MSSQL `regex`
  显式 `POLYDB_ERR_NOT_SUPPORTED`）→ 三端过滤栏（Web `lib/filterOps.ts`、GUI `filters.rs`+`view.rs`、
  TUI `browse_filters.go`+`model.go`）→ 契约测试 `test/contract/rows_test.go`（18 op + 三态打两端）。
  **spec 变更 non-breaking**：新字段 `case_sensitive` 可选，缺席即沿用方言排序规则。

### 阶段 D — 原「另案」落地（协议 + 小件）

- **D2** ✅（2026-09-25）：Redis 当前 db 回读。`RedisScanPage` 加可选 `db`（`spec/schemas/redis.json`
  + `behavior.md` §1.3，**non-breaking**）；两端驱动跟踪所在 db（专用连接 + `SELECT` 是连接级状态，
  故跟踪值即服务端真值；`select_db` 仅在服务端 OK 后更新，`open` 以配置 `database` 初始化），
  SCAN 页回显；三端以回显覆盖本地记忆值，字段缺席一律保持上次值（**不得回落到 0**）。
  落点：`db-redis/src/lib.rs`（`AtomicU32`）、`pkg/dbredis/dbredis.go`、TUI `model.go` `kvScanMsg`、
  GUI `app.rs` `redis_scan/redis_more/redis_set_db`、Web `RedisBrowser.tsx`；
  契约 `redisScenario` 用 `SELECT 1` 证明非本地值。
- **D3** ✅（2026-09-25）Rust GUI 执行计划：新增 `rust/crates/ui-gui/src/explain.rs`（纯函数，与 Web
  `lib/explain.ts` / TUI `go/pkg/explain` 同语义、输出逐字一致），顶栏「⚙ 执行计划」+ `F9` /
  `Ctrl+Shift+E` 触发，绝对定位浮层展平渲染 `├─/└─/│` 计划行、可一键复制、`Esc`/点击外部关闭；
  Redis 连接不渲染入口并提示「Redis 无执行计划」，重新执行查询即作废旧快照。无 spec 变更。
- **D4** ✅（2026-09-25）Redis string 整值写回：Web + GUI 对齐 TUI 的 `e`。后端 PUT 早已齐备，
  本轮纯前端接线、零 spec 变更；门禁三态顺序（未加载 → 只读 → 非 string）三端一致，
  提交后回读确认、写失败保留输入。Web 门禁抽为 `lib/redisWrite.ts` + 单测，GUI 抽为
  `util::redis_write_block` + 单测。见 `docs/frontends.md`「Redis 整值写回（三端一致，D4）」。
- 仍不排期（各自需新里程碑再拆）：Rewind 快照（需存储层）、Compare & Sync（**已单独立项拆解，
  见 `docs/plan-compare-sync.md`，2026-09-25，未开工**）、Server Dashboard、
  左缘连接条 rail。

## 4. 跨端一致性门禁

- 三端同表同条件的 rows 查询结果做**人工对拍清单**（进 PR 描述模板）；
- changes 队列行为用**同一组场景用例**在 TS/Rust/Go 三套单测中各自复写（输入输出表驱动）；
- 键位在 `docs/frontends.md` 维护三端对照总表。

## 5. 风险

| 风险 | 缓解 |
|---|---|
| GPUI 无成熟表格虚拟化原语 | G2 自绘 viewport（行高固定 + 滚动偏移），已有 0.2 API 打底 |
| 工作量跨三个阶段，周期长 | 阶段间无强依赖（除 G3→G2、T3→T2），可按 G→T→C 顺序独立交付 |
| TableProGo AGPL 传染 | 仅读其代码理解语义分层，实现全部独立；提交说明沿用红线复核注记 |
| 三端行为漂移 | §4 表驱动同用例 + 对拍清单 |
| TUI 编辑流误操作风险（终端无确认弹窗习惯） | 提交前强制 Review 一步 + 只读连接门禁 + 行数校验兜底 |

## 6. 实现落点（阶段 A G1–G6、阶段 B T1–T4、阶段 C W1 已完成，2026-09-24）

> 许可红线复核：TablePro / TableProGo 为 AGPLv3，以下内容仅为**交互设计与语义参考**，
> 三端实现全部独立，未移植其任何代码。

### 阶段 A — Rust GUI（`rust/crates/ui-gui`）
- 由 1,807 行单文件拆为 `theme/tabs/profile/filters/presets/changes/exporters/app/view/util`；
  §1.1 六项缺陷（结果无滚动、游离 FocusHandle、心跳无退出、refresh 踢回启动屏、
  `browse_rows` 协议字段空置、文档落后）全部修复。
- §1.2 的 GUI 侧对应能力同批落地：过滤/排序/计数/页大小走服务端、变更队列 + 事务提交、
  连接 CRUD、导出、Redis 只读浏览、编辑器选区/撤销、`Ctrl+P` Quick Switcher。
- 门禁：`cargo fmt && clippy`（零警告）`&& test`（33 项）`&& build`。

### 阶段 B — Go TUI（`go/internal/tui` + `go/pkg/{changes,explain}`）
- T1：lipgloss 主题 token（NO_COLOR / dark/light）、列表虚拟滚动、`F1` 快捷键面板。
- T2：`transport.Client` 增 `BrowseRowsCount`；过滤栏 13 op（Go 独立条件构造）、排序、
  页大小 50/200/1000、精确计数、`Tab` 焦点环、`F4` 历史抽屉（本地 JSONL）、`Ctrl+P` 跨 schema 搜表。
- T3：`transport.Client` 增事务四方法；`go/pkg/changes` 队列纯函数包 + 单测；单元格编辑、
  Review、单事务提交/回滚、撤销栈、只读门禁——与 Web/GUI 同语义。
- T4：`F8` 取消在途查询（ctx 贯穿 + 远程 cancel 端点，UUID `query_id`）；连接编辑
  （`UpdateConnection` 接线）；Redis string 值 `e` 就地写回 + 回读；`F9` 执行计划
  （新增 `go/pkg/explain` 语义层 + `viewExplain` 树渲染）；`docs/frontends.md` 键位总表。
- §1.2 遗留项处置：kvDB 恒 0 已按「真缺陷」处理并经 D2 交付（`RedisScanPage.db` 回读字段）；其余均已修复。
- 附带后端修复：`go/pkg/dbsqlite` 把 `StatementTypeOther` 送去 `Exec`，导致 SQLite 的
  `EXPLAIN` / `PRAGMA` 行集被丢弃（另外四家 Go 驱动与 Rust 侧均按查询处理）——已对齐并补回归测试。
- 门禁：`gofmt` + `go vet ./...` + `go build ./...` + `go test ./...` 全绿
  （`golangci-lint` v2 本机未安装，lint 步骤跳过）。

### 阶段 C — Web（`web/src`，W1 已完成 2026-09-24）
- 纯逻辑下沉 `web/src/lib/resultViews.ts`：`ViewMode`、`cellClass`/`fmt`（自 QueryWorkspace 迁出）、
  `buildInspectorFields`（跳过隐藏列、保留原始列下标、400 字符裁断）、`outputMessages`
  （多语句逐条 + 单语句，统一状态文案）。呈现组件 `ResultViewPanels.tsx`
  （`JsonResultView` / `MessagesView` / `RowInspector`），派生与状态仍留在 QueryWorkspace。
- 视图模式：`chartOpen` 收敛为单一 `viewMode`，`表格 | JSON | 图表 | 消息` 四页签互斥；
  用派生 `effectiveMode` 处理「新结果不支持该模式」回落，避免在各 `setResult` 处散落重置。
  挂起事务条移出「有列」分支，四页签下常驻。
- Inspector：恰好选中 1 行走 `displayRows`+`origIdx` 取行（与网格排序/过滤/隐藏列/草稿一致），
  「编辑」复用 `startEdit` 门禁（主键列、不可识别行、只读连接均不出入口），「完整值」复用
  `setCellPreview`；开合偏好 `polydb.inspector.v1`。
- 门禁：`tsc --noEmit` + `eslint .` 干净，`vitest run` 13 文件 / 121 测试全绿，`vite build` 成功。
  界面**未在浏览器肉眼验证**（自动化浏览器被沙箱拦截），以组件测试 + 构建门禁代替。

### 三端一致 / 差异
- 一致：过滤 18 op + `case_sensitive` 三态与排序/计数走服务端（能力矩阵见 `behavior.md` §13.6，
  三端渲染同一份集中式 WHERE 构造器）；变更跟踪编辑「暂存 → Review → 单事务提交（失败回滚）→ 撤销」；
  只读连接禁用全部写入口；`Ctrl+P` Quick-open。
- 差异（有意保留）：终端无鼠标习惯 → TUI 不做拖拽选区；取消入口三端形态不同（`F8` / 顶栏按钮 / `Ctrl+Esc`）；
  执行计划三端均已具备（阶段 D 补齐 GUI，触发形态各异：`F9` / 工具栏按钮 / 顶栏按钮）；
  Redis 整值写回三端均已具备（阶段 D 补齐 Web/GUI，见 D4；list/hash/set 仍只有命令行）。

## 阶段 A–D 收口：全端门禁基线（2026-09-25）

一次性把六条门禁全跑一遍，数字如下（含**没跑成的**，避免把 soft-skip 当验证）：

| 门禁 | 命令 | 结果 |
| --- | --- | --- |
| 协议漂移 | `tools/genprotocol` `ts --check` / `check` | TS 类型与 spec 同步；`54 spec types vs rust + go` OK，零漂移 |
| Rust | `cargo fmt --all -- --check` / `clippy --workspace --all-targets` / `test --workspace` | 格式干净；**零警告**；全绿（`polydb-ui-gui` 48 测试，含 explain 7 + Redis 写回门禁 1） |
| Go | `go build ./...` / `go vet ./...` / `go test ./... -count=1` | 构建与 vet 干净；10 个有测试的包全部 `ok`（非缓存） |
| Go 格式 | `gofmt -l` | 首扫 45 个文件全部“未格式化”，逐个剥掉 CR 后**真未格式化 0** ⇒ 是本机 `core.autocrlf=true` 的换行 artifact，不是格式漂移 |
| Web | `tsc --noEmit` / `eslint --max-warnings=0` / `vitest run` / `vite build` | 0 类型错误、0 lint 警告、15 文件 / 141 测试全绿、构建成功（主 chunk 4.96 MB，Monaco 造成的既有体积告警，非本次引入） |
| 契约对拍 | `test/contract` `go test -count=1 -v` | `51 RUN / 51 PASS / 0 SKIP / 0 FAIL`（156s，Go 与 Rust 双后端） |

**上述绿灯不覆盖的部分（如实披露）**：
- 契约里 10 处驱动流程 soft-skip：PG / MySQL / MSSQL / Oracle / Redis × 两端，本机这些服务不可达。
  即 Redis 写回→回读（D4）、`regex` 的 MSSQL `NOT_SUPPORTED`（D1）、`RedisScanPage.db` 回显（D2）
  三条真库路径**只有单元测试兜底，没有服务端对拍数字**。
- `golangci-lint` v2 本机未安装 ⇒ Go lint 步骤跳过（`go vet` 已跑）。
- GUI（GPUI）与 Web 的界面渲染未肉眼验证：无头环境跑不了 GUI，Web 自动化浏览器此前被沙箱拦截，
  以组件测试 + 构建门禁代替。
