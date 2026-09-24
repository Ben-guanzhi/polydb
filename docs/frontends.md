# 前端

polydb 有多个前端，均遵守 AGENTS.md 铁律：**只经 `app-core`（进程内）或 `transport`（网络）访问数据，不 import 任何 driver**。

## Web（M3，React + Monaco）

- 目录：`web/`，包名 `@polydb/web`
- 技术栈：Vite 6 + React 18 + TypeScript，`@msgpack/msgpack` 编解码 msgpack 控制面
- 入口：`web/src/App.tsx`；HTTP 客户端唯一出口 `web/src/lib/api.ts`（基于 spec/openapi.yaml 的 REST 控制面）
- 运行：`cd web && npm run dev`（Vite 代理 `/api` → `POLYDB_SERVER_URL || http://127.0.0.1:8080`）
- 组件：ConnectionList（连接 CRUD/测试）、SchemaBrowser（schemas→tables→columns/indexes/fks/DDL，
  🕸 开关切 ER 图，表详情内「结构编辑」ALTER 表单）、ERDiagram（M16，纯 SVG 只读关系图）、
  TableStructureEditor（M16，按方言生成 DDL，事务内执行失败回滚）、
  QueryWorkspace（Monaco + 结果表格 + 变更跟踪编辑/导出）、RedisBrowser（Redis 键浏览/命令）
- 结果区视图模式（阶段 C W1）：`表格 | JSON | 图表 | 消息` 四页签互斥，图表由叠加面板改为独立页签。
  JSON 页签跟随当前可见视图（排序、列过滤、隐藏列之后）输出「行→对象数组」，可复制/下载；
  消息页签逐条列出每次执行的状态（行列数、影响行数、服务端/往返耗时、截断与分页提示、错误、
  取消/跳过），多语句时可点击条目切回该条结果。挂起事务条在四页签下常驻。
- Inspector 行详情尾板（与 GUI G2 同语义）：**恰好选中 1 行**时自动展开，逐字段显示
  列名/类型/值（超 400 字符裁断），提供「完整值」与「编辑」入口（编辑仅在该列可内联编辑时出现：
  有主键、非主键列、行可识别、非只读）；右上 `×` 强制收起，开合偏好存 localStorage `polydb.inspector.v1`。
- 命令面板新增 `ws.view-table / ws.view-json / ws.view-output / ws.inspector-toggle`（category「视图」），
  `ws.chart-toggle` 改为切页签。
- 纯逻辑出口：`web/src/lib/resultViews.ts`（视图模式类型、`cellClass`/`fmt`、`buildInspectorFields`、
  `outputMessages`）；呈现组件 `web/src/components/ResultViewPanels.tsx`（`JsonResultView`/`MessagesView`/`RowInspector`）。

## TUI（M4，Go + bubbletea）

- 目录：`go/internal/tui/` + 入口 `go/cmd/polydb-tui/`
- 技术栈：charmbracelet bubbletea + bubbles + lipgloss
- 数据访问只经 `pkg/transport.Client`（AGENTS.md 铁律 2），两种模式：
  - **本机**（默认）：`transport.NewLocal(appcore)` 进程内直连 app-core，零序列化；与 server 共用
    同一份连接元数据：`storage.DataDir()`（`POLYDB_DATA_DIR` 优先，否则用户配置目录 `polydb/`）
  - **远程**（`-server http://host:port` 或 `POLYDB_SERVER` 环境变量）：`transport.NewRemote`
    走 polydb-server 的 REST 接口（msgpack 数据面），连接与密码归属服务端，本地不读存储/密钥环。
    注意：远程下 `IsConnected` 是本地进程视角标记，`Connect` 只校验连接记录存在
    （数据库连接由服务端在首个查询时惰性建立）
- 运行：`go run ./cmd/polydb-tui`（本机）或 `go run ./cmd/polydb-tui -server http://127.0.0.1:8080`（远程）
- 视图与按键（**程序内 `F1` 是权威键位表**，按当前视图显示；下表与之同步，阶段 B T1–T4）：

  | 视图 | 按键 |
  | --- | --- |
  | 全局 | `F1` 帮助面板 · `F8` 取消在途查询 · `Ctrl+C` 退出 · `Esc` 返回/关面板 |
  | 连接列表 | `↑/↓`·`k/j` 选择 · `Enter` 连接进库表 · `n` 新建 · `e` **编辑选中连接** · `t` 测试 · `d` 删除（连按两次确认） · `r` 刷新 · `q` 退出 |
  | 连接表单 | `Tab`/`Shift+Tab` 切字段 · `←/→` 切类型（编辑态改为移动光标，类型不可改） · `Enter` 新建/保存 · `Esc` 返回 |
  | 库表结构 | `↑/↓` 选表 · `Enter` 表详情 · `s` 切 schema · `Ctrl+P` 跨 schema 搜表 · `q` 查询编辑器 · `F7` 浏览数据 |
  | 表详情 | `↑/↓` 切表 · `q` 查询该表 · `F7` 浏览数据 |
  | Quick-open | 输入即时过滤 · `↑/↓` 选择 · `Enter` 打开 · `Esc` 返回 |
  | 查询编辑器 | `Tab` 补全表/列 · `F5`/`Ctrl+E` 执行 · `F9` **按方言生成并执行 EXPLAIN** · `F8` 取消 · `F4` 查询历史抽屉 · `F6` 导航结果 · `Ctrl+←/→` 词级移动 |
  | 执行计划 | `↑/↓`·`j/k` 滚动 · `F8` 取消在途 EXPLAIN · `Esc`/`q` 返回编辑器 |
  | 结果网格 | `↑↓←→` 移动单元格光标 · `Enter` 完整值（JSON 自动美化） · `Esc` 返回 |
  | 数据浏览 | `↑/↓` 行 · `←/→` 翻页 · `Shift+←/→` 移列 · `Enter` 完整值 · `/`·`Tab` 进过滤栏 · `e` 编辑单元格（需主键且非只读） · `u` 撤销暂存 · `w` 审阅并提交（单事务，失败回滚） · `s` 排序 · `p` 页大小 50/200/1000 · `c` 精确计数 · `o` AND/OR · `d` 删末条过滤 · `r` 刷新 |
  | 过滤栏编辑 | `Tab`/`Shift+Tab` 列·操作符·值 · `←/→` 调整 · `↑/↓` 循环选列 · `Enter` 应用 · `Esc` 取消 |
  | 变更审阅 | `↑/↓` 看语句 · `Enter`/`y` 提交 · `u` 撤销 · `Esc`/`n` 返回 |
  | Redis 键 | `↑/↓` 选键 · `Enter` 看值 · `/` 模式过滤 · `g` 加载更多 · `b` 切 db 0–15 · `c` 命令行 · `e` **写回 string 值**（`Enter` 提交并回读，`Esc` 放弃） · `r` 刷新 |

- 执行计划（`F9`）由 `go/pkg/explain` 负责：按连接方言包成 `EXPLAIN QUERY PLAN`（SQLite）/
  `EXPLAIN (FORMAT JSON)`（PG）/`EXPLAIN FORMAT=JSON`（MySQL），解析成缩进树；MSSQL/Oracle/Redis
  与解析失败时回落成表格文本。计划轮**不入查询历史**。
- 取消在途查询：客户端预生成 UUID `query_id`，远程模式经 `POST /api/queries/{id}/cancel` 让服务端
  中断，本地下发 `context.CancelFunc`；两端都是 best-effort（见 `spec/behavior.md` §2.2）。
- 注意：打开连接时若已连接则跳过 `Connect`（`IsConnected` 判断），否则重复 Connect 会重开驱动导致 `:memory:` 会话数据丢失
- 跨端键位差异（阶段 B 结束时的对照，GUI/Web 为鼠标优先、TUI 全键盘）：
  - 取消在途查询：TUI `F8`（任意视图，执行期间；本地 ctx 取消 + 远程 cancel 端点）；
    GUI 顶栏「■ 取消」按钮（多语句尽力而为：当前语句跑完、剩余跳过）；Web 在 Monaco 内
    `Ctrl+Esc`，多语句另有取消入口。
  - 执行计划：TUI `F9`（按方言生成 + 计划树，见上）；Web 工具栏「⚙ 执行计划」+ 命令面板
    「运行 EXPLAIN」，走 `lib/explain.ts`；GUI 无。
  - 连接编辑：三端均有表单（TUI 连接列表 `e`，Web/GUI 走连接列表编辑入口）。
  - Redis 值写回：TUI `e` 放开 string 整值写回（Enter 提交并回读）；Web 走命令行
    `exec_command`；GUI 为只读子集。
  - Quick Switcher：三端同为 `Ctrl+P`（语义各异：TUI 跨 schema 搜表、Web 搜表、GUI 在已开对象 tab 间跳转）。

## GUI（M1 扩展，Rust + GPUI）

- 目录：`rust/crates/ui-gui/` + 入口 `rust/apps/polydb-gui/`
- 技术栈：GPUI（`gpui` crate）。前端只经 `polydb_transport::LocalTransport`（进程内 app-core）访问数据，
  不 import 任何 driver；异步 transport 调用委托给专用 tokio runtime（gpui 的 executor 与驱动所需的
  tokio reactor 分离）
- 形态（阶段 A G1–G6 后）：对象 tab 壳层（workbench 常驻 + 表 tab 多开，rails 概览/数据/结构/关系）、
  侧栏 schema→表→列树 + schema 切换、数据浏览（过滤栏 13 op/排序/页大小/精确计数/Inspector 尾板/
  虚拟滚动网格）、变更跟踪编辑（Review + 单事务提交/回滚 + 撤销栈 + 只读门禁）、连接 CRUD 表单、
  结果导出 CSV/TSV/JSON/NDJSON/Markdown/INSERT、Redis 键浏览（只读子集）、SQL 编辑器选区/剪贴板/
  undo-redo + Quick Switcher（`Ctrl+P` 在已开对象 tab 间跳转）。主题走 `theme.rs` token（dark 默认 + light）。
- 顶栏「■ 取消」为多语句尽力而为取消（当前语句跑完、剩余跳过）；驱动层无中断，故不是即时取消。
- 运行：`cd rust && cargo run -p polydb-gui`（构建含 GPUI，Windows 下首次编译较慢）
