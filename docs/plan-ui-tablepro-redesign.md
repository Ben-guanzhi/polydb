# polydb Web 界面重设计（参考 TablePro）

> 前置：`docs/plan-tablepro-parity.md`（M10–M17 及后续打磨）已全部完成。本篇为其第二阶段——
> **界面形态向 TablePro 对齐** + 体验完善。
> **许可红线**不变：只参考交互设计与视觉组织，不移植任何代码（TablePro 为 AGPLv3）。
> 本阶段全部为 **Web 前端改动，零 spec 变更、零后端改动**（沿用既有端点与 `read_only` 字段）。

---

## 1. 现状 vs TablePro 界面模型差距

| 维度 | polydb 现状 | TablePro | 结论 |
|---|---|---|---|
| 主区组织 | 单查询工作台；`sql/data` 两段手动切换（`App.tsx` mainMode），一次只能看一张表 | **对象标签页制**：每打开一个表 = 一个 tab，tab 内 rails（Content / Structure / Relations…）；查询编辑器是另一类 tab | **U1 核心重构** |
| 表详情 | 挤在侧栏 SchemaBrowser 底部（列/索引/FK/结构编辑/DDL 纵向堆叠，滚动很深） | 侧栏只做导航，详情在主区 tab | U1 移入 rails，侧栏瘦身 |
| 快速定位 | 命令面板 Ctrl+K 只搜命令/连接 | Cmd+P **Quick Switcher 搜表**直达 Content 视图 | **U3** |
| 连接状态 | 仅 health 轮询 server 存活；连接本身不探测；只读字段后端已支持但 **UI 无入口无标识** | 连接常显颜色/标签，safe mode 徽标，健康监视器自动重连 | **U4** |
| 启动体验 | 无连接时一行 empty 文案 | 连接选择卡片屏（分组 + 颜色 + 类型徽章） | U2 |
| 主题 | CSS 变量只有 8 个 token，**暗色主题严重不完整**（input `#fff`、grid 表头 `#fafafa`、hover `#f0f2f5` 等十余处硬编码，暗色下刺眼） | 完整 light/dark 设计 token | U2 修复 + token 体系 |

## 2. 关键设计决策

1. **统一"打开对象"标签模型，但不迁移 SQL tab 所有权**。
   新建 shell 级 `openTabs`（localStorage `polydb.openTabs.v1`，per conn）：`{kind:'workbench'} | {kind:'table', schema, table}`。
   - workbench tab 唯一且常驻第一位，内部仍由 QueryWorkspace 自管它的查询子标签（其 4k 行 tab 状态深耦合 ref，**不上提**——降风险）。
   - table tab 可开多个，同一 `schema.table` 复用既有 tab。
   - 切换 tab 时 workbench 用 `display:none` 保持挂载（M17 既定惯例，防 Monaco 丢状态）。
2. **TableTab rails = Content / Structure / Relations**（对应 TablePro 同名 rails）：
   - Content：复用 `TableDataView`（服务端分页/编辑，已有）。
   - Structure：从 SchemaBrowser 底部迁来的 列/索引/FK 明细表 + DDL + `TableStructureEditor`。
   - Relations：外键双向列表（本表出向 + 引用本表的入向，复用 `list_foreign_keys`），点对手表跳转其 tab。
   - 表详情数据用自建 `useTableDetail` hook（columns+indexes+fks+ddl 并发拉取）。
3. **侧栏 SchemaBrowser 瘦身为纯导航**：删除底部详情块与 erMode 内嵌 ER（ER 图入口改为：打开 table tab 的 Relations rail + 保留侧栏 🕸 快捷开当前 schema 任一表的 relations 简化版——直接移除侧栏 ER，避免双实现）。
4. **Quick Switcher（Ctrl+P）**：懒加载并缓存当前连接的 schema×tables，模糊匹配（子序列打分），结果两类：表（打开 table tab）/ 连接（切换活动连接）。与 Ctrl+K 命令面板分工：P=对象导航，K=命令。
5. **只读/健康闭环**：连接表单加 read_only 开关（spec 已有字段）；顶栏连接 chip 显示 🛡 徽标；`TableDataView`/内联编辑接收 `readOnly` prop 禁用写入口；App 对活动连接 30s `testConnection` 心跳，断线在 chip 上给 err 点并显示 latency。
6. **主题 token 完整化**：`--hover/--panel-2/--input-bg/--grid-head-bg/--stripe` 等新 token + 暗色全量覆盖；替换全部硬编码色值。顶栏重排为 TablePro 式：标题 ｜ 连接 chip（色点+kind 徽章+🛡+状态点）｜ 搜索按钮 ｜ 右侧 server pill。

## 3. 里程碑

### U1 — 工作台壳层重构：对象标签页 + TableTab rails 【L，核心】
- `lib/openTabs.ts`：模型 + localStorage 读写（含旧 mainMode 无状态可迁，直接新建）。
- `TableTab.tsx`：面包屑 + rail 段控件 + 三 rail 实体；`useTableDetail`。
- `TabStrip` 升级为对象条（图标区分 workbench/table，表 tab 带 kind 色点、× 关闭，双击重命名保留在查询子层）。
- `App.tsx`：删 `mainMode/tableCtx` 切换，`handlePreviewTable/handleSelectTable` 改开 table tab；Redis 连接维持现状。
- `SchemaBrowser`：删底部详情块 + erMode 内嵌 ER；行点击语义=双击/回车开 tab。
- 验收：连开 3 表各自独立翻页/编辑不串状态；切回 workbench 编辑器内容与结果原样保留。

### U2 — 视觉设计系统 + 启动屏 + 顶栏 【M】
- token 扩展与暗色补齐（消灭硬编码色值，含 `table.grid`、`.list-item`、`.search` 等）。
- 启动卡片屏：无连接时主区渲染连接分组卡片网格（色点/kind 徽章/组名/测试状态），点击即选。
- 顶栏重排（连接 chip、server pill、搜索按钮）。
- 验收：`data-theme=dark` 下全界面（含新 tab 栏）无白块刺眼残留。

### U3 — Quick Switcher（Ctrl+P） 【M】
- `QuickSwitcher.tsx` + `lib/tableSearch.ts`（模糊打分，纯函数，Vitest 覆盖）。
- 打开表/切换连接两类结果；键盘导航复用 ContextMenu/CommandPalette 惯例。
- 验收：200+ 表库输入 `usadd` 命中 `user_addresses` 并直达 Content。

### U4 — 只读闭环 + 连接心跳 【S-M】
- 连接表单 read_only 开关；顶栏 🛡；`TableDataView`/`QueryWorkspace` 写入口禁用提示；`POLYDB_ERR_READ_ONLY` 错误文案友好化。
- 活动连接 30s testConnection 心跳 + latency 显示 + 失败 err 点。
- 验收：只读连接在数据网格看不到可编辑态；心跳断线有视觉反馈。

### U5 — 质量门禁与文档
- `npm run check`（tsc+eslint+vitest）+ `vite build` 全绿；关键新纯函数（openTabs/tableSearch/useTableDetail 状态机）带单测。
- 更新 `docs/plan-tablepro-parity.md` 进度注记与本文件实现落点。

## 4. 明确不做（本阶段）
- SQL tab 上提合并（QueryWorkspace 拆分前置，列为后续还债）；TUI/GUI 前端不在本阶段；
- Dashboard/Users rails、schema compare/sync、备份恢复、rewind snapshots（需要后端/spec，另立阶段）；
- iCloud 式云同步、多窗口。

## 5. 风险
| 风险 | 缓解 |
|---|---|
| display:none 下多表 tab 全挂载致 Monaco/大结果内存压力 | table tab 卸载非活动体（TableDataView 状态轻量，翻页条件序列化进 tab 模型） |
| SchemaBrowser 行为变更破坏既有引用点 | onSelectTable/onPrefillSql 通道保留，只改落点 |
| 暗色 token 替换面广回归 | 纯 CSS 变量替换 + 逐屏截图核对 |

## 6. 实现落点（U1–U6 已完成，2026-09-21）

> 许可红线复核：以下内容仅为交互设计参考（TablePro, AGPLv3），未移植其任何代码。

### U1 对象标签页壳层
- `web/src/lib/openTabs.ts`：workbench 常驻 + 表 tab 模型，localStorage `polydb.openTabs.v1`（per conn），`openTableTab/closeOpenTab/clearTableTabPreset`；单测 `openTabs.test.ts`。
- `web/src/components/ObjectTabStrip.tsx`、`TableTab.tsx`（rails：概览/数据/结构/关系 + 面包屑 + `useTableDetail` + schema 级 FK 缓存）。
- `SchemaBrowser.tsx` 瘦身为纯导航（详情块/内嵌 ER 已移除）；`App.tsx` 主区改为 tab 壳层，workbench `display:none` 常驻挂载，表 tab 卸载非活动体。

### U2 视觉系统
- `index.css` 全量 token（`--fg/--bg-alt/--hover/--input-bg/--grid-head-bg/--stripe/--row-hover/--danger-*` 等）+ 暗色覆盖 + 硬编码色清零；`StartScreen.tsx` 连接卡片屏；顶栏连接 chip（色点/kind 徽章/🛡/状态点/latency）。

### U3 Quick Switcher
- `QuickSwitcher.tsx`（Ctrl+P）+ `lib/tableSearch.ts`（`fuzzyScore` 与 CommandPalette 共享，`rankTables` 带长度惩罚）；表缓存 60s TTL；单测 `tableSearch.test.ts`。

### U4 只读闭环 + 心跳
- 连接表单 read_only 开关；chip 🛡；`TableDataView` `readOnly` 隐藏全部写入口；`api.ts` `POLYDB_ERR_READ_ONLY` 文案；App 30s `testConnection` 心跳 + latency。

### U5 质量门禁（含两处后端真 bug 修复）
- 门禁全绿：tsc、eslint 0 警告、vitest 104/104（web）、`go test ./pkg/...` 全 ok、vite build 成功。
- **Go dbsqlite PRAGMA 列宽回归**：`foreign_key_list` 8 列（match）、`index_list` 5 列（origin/partial），Scan 目标数不匹配导致带 FK/索引的表浏览报错。修复 + 回归测试 `go/pkg/dbsqlite/dbsqlite_test.go`。
- **Go dbsqlite `:memory:` 死锁**：`ListIndexes` 在 `MaxOpenConns(1)` 的内存库上边迭代 row 边嵌套 `PRAGMA index_info` 查询 → 池等待死锁（任何有索引的内存库浏览 schema 即挂死）。改为两段式（先物化 index_list，释放 row 后逐索引查列）。
- 浏览器端验证：StartScreen/chip 心跳、三 rails、Ctrl+P 直达、🔗 关联跳转带过滤、tab 右键菜单、只读 🛡+写入口隐藏。

### U6 表对象体验（本轮新增）
- **U6.1 概览 rail**：`lib/tableProfile.ts`（纯函数：单条聚合 SQL 画像——行数 + 每列 NULL 数/完整度/DISTINCT/数值 MIN·MAX·AVG，LOB 列跳过 DISTINCT，数值列才出 MIN/MAX/AVG，前 24 列截断）+ `TableOverview.tsx`（信息卡 + 列画像表）；单测 6 项。列画像能力超出 TablePro（其 Inspector 无基数/空值统计）。
- **U6.2 数据网格 FK 🔗 跳转**：`TableDataView` 拉取单列 FK 映射，FK 单元格渲染 🔗 → `openTableTab(schema,table,{eq preset})` 新开目标 tab 并自动填入等值过滤（preset 一次性消费后清除）。
- **U6.3 对象 tab 右键菜单**：复用共享 `ContextMenu`——在查询中打开 / 复制表名 / 复制 schema.table / 关闭标签 / 关闭其他标签 / 关闭所有表标签（workbench 恒保留）。
- **U6.4 连接列表过滤框**：按名称/分组/kind/库名/主机子串过滤，分组渲染作用于过滤结果，空匹配提示。
