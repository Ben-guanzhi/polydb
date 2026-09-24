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
  | 过滤栏编辑 | `Tab`/`Shift+Tab` 列·操作符·值·(第二值)·(大小写) · `←/→` 调整当前格 · `↑/↓` 循环选列 · `Enter` 应用 · `Esc` 取消 |
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
    「运行 EXPLAIN」，走 `lib/explain.ts`；GUI 顶栏「⚙ 执行计划」+ `F9`/`Ctrl+Shift+E`，
    走 `rust/crates/ui-gui/src/explain.rs`（三端各自实现、输出逐字一致，见「执行计划」一节）。
  - 连接编辑：三端均有表单（TUI 连接列表 `e`，Web/GUI 走连接列表编辑入口）。
  - Redis 值写回：三端同为 **string 整值写回**（TUI `e`、GUI/Web 键值面板「✎ 写回整值」），
    提交后回读确认；list/hash/set/zset 仍走命令行 `exec_command` 按元素改（见「Redis 整值写回」一节）。
  - Quick Switcher：三端同为 `Ctrl+P`（语义各异：TUI 跨 schema 搜表、Web 搜表、GUI 在已开对象 tab 间跳转）。

## GUI（M1 扩展，Rust + GPUI）

- 目录：`rust/crates/ui-gui/` + 入口 `rust/apps/polydb-gui/`
- 技术栈：GPUI（`gpui` crate）。前端只经 `polydb_transport::LocalTransport`（进程内 app-core）访问数据，
  不 import 任何 driver；异步 transport 调用委托给专用 tokio runtime（gpui 的 executor 与驱动所需的
  tokio reactor 分离）
- 形态（阶段 A G1–G6 后）：对象 tab 壳层（workbench 常驻 + 表 tab 多开，rails 概览/数据/结构/关系）、
  侧栏 schema→表→列树 + schema 切换、数据浏览（过滤栏 18 op + 大小写三态/排序/页大小/精确计数/Inspector 尾板/
  虚拟滚动网格）、变更跟踪编辑（Review + 单事务提交/回滚 + 撤销栈 + 只读门禁）、连接 CRUD 表单、
  结果导出 CSV/TSV/JSON/NDJSON/Markdown/INSERT、Redis 键浏览 + string 整值写回、SQL 编辑器选区/剪贴板/
  undo-redo + Quick Switcher（`Ctrl+P` 在已开对象 tab 间跳转）、执行计划浮层（`F9`）。
  主题走 `theme.rs` token（dark 默认 + light）。
- 顶栏「■ 取消」为多语句尽力而为取消（当前语句跑完、剩余跳过）；驱动层无中断，故不是即时取消。
- 运行：`cd rust && cargo run -p polydb-gui`（构建含 GPUI，Windows 下首次编译较慢）

## 数据浏览过滤栏（三端一致，D1）

- **18 个操作符**，与 `spec/schemas/query.json` 的 `FilterOperator` 枚举一一对应。三端各自的
  真相源文件必须与 spec 同步：Web `web/src/lib/filterOps.ts`、GUI `rust/crates/ui-gui/src/filters.rs`
  (`OPS`)、TUI `go/internal/tui/browse_filters.go` (`browseOps`)。
  取值形态：无值（`null`/`not_null`/`empty`）、单值（`eq`/`ne`/`lt`/`le`/`gt`/`ge`/`like`/
  `not_like`/`contains`/`starts_with`/`ends_with`/`regex`）、双值（`between`）、多值（`in`/`not_in`，
  TUI 用逗号分隔单字段输入）。
- **大小写三态 `case_sensitive`**（`spec/behavior.md` §13.5）：`默认`（字段缺席，沿用方言排序规则）/
  `区分`（`true`）/ `忽略`（`false`）。**只发给文本匹配族**（`like`/`not_like`/`contains`/
  `starts_with`/`ends_with`/`regex`）；比较类 op 上三端一律不下发该字段，避免给数值/日期列注入
  `CAST`/`COLLATE`。GUI 预设向后兼容：老预设缺 `case_mode` 字段按「默认」加载。
- **SQLite 形态**：强制区分走 `GLOB`（模式按 GLOB 规则重转义），强制不区分走 `col REGEXP '(?i)' || ?`
  （`lower()` 会破坏 `[A-Z]` 字符组，语义不等价）。REGEXP UDF 两端都注册
  （rusqlite `functions` feature / modernc `RegisterFunc`），同为 RE2 家族：**无后向引用、无环视**，
  NULL 传播为 NULL。
- **能力降级要显式报错**：MSSQL 无原生正则，`regex` 由 `db-core`/`dbcore` 抛
  `POLYDB_ERR_NOT_SUPPORTED`；三端把该 op 置灰并提示，且**整条条件不下发**（不是静默换实现）。
  TUI 若草稿已落在不支持的 op 上，`Enter` 拒绝应用并给状态栏提示，`←/→` 循环会跳过不支持项。
- **契约覆盖**：`test/contract/rows_test.go` 用同一组断言分别打 Go 与 Rust 后端，覆盖 18 op、
  `case_sensitive` 三态（SQLite 真库：`GLOB`/`LIKE`/`REGEXP` UDF 三条路径）与 `empty`。
  MSSQL 的 `NOT_SUPPORTED` 由两端 `browse.rs`/`browse.go` 单元测覆盖——契约测试无 MSSQL 实例，
  该分支未走服务端对拍（如实披露）。

## Redis 当前 db 回读（D2）

- 背景：连接配置可带 `database`（即建连时的 `SELECT`），但 SCAN 响应里没有「当前所在 db」，
  三端只能显示本地记忆值 → 配置 db 3 时界面仍写 `db 0`。
- 现在：`RedisScanPage.db`（可选，`spec/schemas/redis.json` + `behavior.md` §1.3）由驱动回显。
  驱动持有**专用连接**且 `SELECT` 是连接级状态，故驱动跟踪值即服务端真值；`select_db` 只在服务端
  确认 OK 后更新，`open` 以配置 db 初始化。**无法确定时省略字段而非回 0**。
- 三端消费：TUI `kvScanMsg` 与 GUI `redis_scan/redis_more/redis_set_db` 用回显覆盖本地值，
  Web `RedisBrowser.loadKeys` 同理；字段缺席一律保持上次值（不当作 db 0）。
- 契约：`redisScenario` 先 `SELECT 1` 再 SCAN，断言回显为 `1`（本地记忆恒 0，只有真回读能给出 1），
  随后回 `db 0` 断言回显 `0`；Redis 不可达时整段按既有 soft-skip 跳过。

## 执行计划（三端一致，D3）

- 三端各自实现同一套语义（前端之间不互相 import，也不得 import driver）：Web `web/src/lib/explain.ts`、
  TUI `go/pkg/explain`、GUI `rust/crates/ui-gui/src/explain.rs`。GUI 是阶段 D 补齐的第三套。
- 包裹规则按方言：SQLite `EXPLAIN QUERY PLAN`、PG `EXPLAIN (FORMAT JSON)`、MySQL `EXPLAIN FORMAT=JSON`、
  Oracle `EXPLAIN PLAN FOR`、其余 `EXPLAIN`；已经以 `EXPLAIN` 开头的原样下发。下发前先剥注释与尾分号
  （字符串字面量内的 `--`/`/*` 不算注释）。
- 解析：SQLite 用 `id`/`parent` 行组树（第 4 列 `notused` 是内部标志位、取值漂移，三端一律不显示）、
  PG/MySQL 解析 JSON 计划，其余方言与解析失败退化为「列 | 行」表格文本。递归深度上限 64，
  自引用/环上的节点标 `…` 并把不可达节点补为顶层，避免计划行凭空消失。
- 树渲染展平为 `├─ / └─ / │` 缩进行，GUI 浮层里以等宽文本显示，可一键复制。
- GUI 门禁：Redis 连接顶栏不渲染该按钮，`F9`/`Ctrl+Shift+E` 走同一入口时状态栏提示
  「Redis 无执行计划」；重新执行查询会清掉已过期的计划快照。

## Redis 整值写回（三端一致，D4）

- 后端**早已齐备**（`PUT /api/connections/{id}/kv/keys/{key}` ↔ `spec/schemas/redis.json#RedisSetRequest`、
  Rust/Go `KvDriver::set_value`），本轮零 spec 变更、纯前端接线：把此前只有 TUI 的整值写回补齐到 Web/GUI。
- **门禁三态同一顺序**（值未加载 → 只读连接 → 非 string），三端各自实现同一语义：Web
  `web/src/lib/redisWrite.ts`、GUI `util::redis_write_block`、TUI `kvStartEdit`。非 string 一律提示
  走命令行按元素改（`RPUSH`/`HSET` …），不做「整值 JSON 覆盖」这种隐式 widening。
- **提交后必须回读**：写成功再 `get_value` 刷新展示值，避免服务端静默失败时界面停在旧值却报成功；
  回读失败则清空展示值并提示重点键名确认。写失败**留在编辑态并保留输入**，改完即可重试。
- 交互入口：TUI 键列表 `e`（`Enter` 提交回读 / `Esc` 放弃）；GUI 键值面板「✎ 写回整值」→ 单行编辑框
  （`Enter` 提交回读 / `Esc` 放弃 / `Ctrl+V` 粘贴，与模式输入共用面板焦点，按状态优先分发）；Web
  键值面板同按钮 → `textarea`（`Ctrl+Enter` 提交 / `Esc` 放弃）。切库、重扫、换键一律丢弃编辑态。
- 只读连接（`read_only`）三端都禁写：Web 经 `App.tsx` 透传 `readOnly`，GUI 走 `is_read_only()`，
  TUI 在 `kvStartEdit` 门禁中判定。

## 已知未验证项

- 三端 Redis 写回**未端到端跑过**：链路需要真实 Redis 实例，本机没有常驻服务。离线门禁只保证
  纯函数层（Web `redisWrite.test.ts`、GUI `util::tests::redis_write_block_gates`）；`PUT /kv/keys`
  两端的 set/get 对拍在 `test/contract/contract_test.go` 的 `set *` / `get *` 步骤里，Redis
  不可达时按既有 soft-skip 整段跳过，因此本轮没有服务端验证数字。
- GUI 编辑框（GPUI 单行输入 + `▏` 光标）与 Web `textarea` 的渲染效果未做肉眼验证。
