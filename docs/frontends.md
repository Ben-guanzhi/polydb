# 前端

polydb 有多个前端，均遵守 AGENTS.md 铁律：**只经 `app-core`（进程内）或 `transport`（网络）访问数据，不 import 任何 driver**。

## Web（M3，React + Monaco）

- 目录：`web/`，包名 `@polydb/web`
- 技术栈：Vite 6 + React 18 + TypeScript，`@msgpack/msgpack` 编解码 msgpack 控制面
- 入口：`web/src/App.tsx`；HTTP 客户端唯一出口 `web/src/lib/api.ts`（基于 spec/openapi.yaml 的 REST 控制面）
- 运行：`cd web && npm run dev`（Vite 代理 `/api` → `POLYDB_SERVER_URL || http://127.0.0.1:8080`）
- 组件：ConnectionList（连接 CRUD/测试）、SchemaBrowser（schemas→tables→columns/indexes/fks/DDL）、QueryWorkspace（Monaco + 结果表格）

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
- 视图与按键：
  - 连接列表：`↑/↓` 或 `k/j` 选择，`Enter` 打开，`n` 新建，`t` 测试，`d` 删除（再按一次确认），`r` 刷新，`q` 退出
  - 新建表单：`Tab`/`↑↓` 切换字段，`←/→` 切换类型，`Enter` 提交，`Esc` 返回
  - 库表浏览：`Enter` 表详情，`s` 切换 schema，`q` 查询，`Esc` 返回
  - 查询：多行输入，`F5`/`Ctrl+E` 执行，`Esc` 返回
  - Redis 键（redis 连接 `Enter` 后进入，M6 前端 Redis 模式在 TUI 闭环）：
    `↑/↓`/`j/k` 选键，`Enter` 看值，`/` 键模式过滤（`Enter` 应用、`Esc` 取消），`g` 加载下一页，
    `b` 循环切换 db 0–15，`c` 命令输入（`Enter` 执行、`Esc` 返回），`r` 刷新，`Esc` 返回列表
- 注意：打开连接时若已连接则跳过 `Connect`（`IsConnected` 判断），否则重复 Connect 会重开驱动导致 `:memory:` 会话数据丢失

## GUI（M1 扩展，Rust + GPUI）

- 目录：`rust/crates/ui-gui/` + 入口 `rust/apps/polydb-gui/`
- 技术栈：GPUI（`gpui` crate）。前端只经 `polydb_transport::LocalTransport`（进程内 app-core）访问数据，
  不 import 任何 driver；异步 transport 调用委托给专用 tokio runtime（gpui 的 executor 与驱动所需的
  tokio reactor 分离）
- 当前为**最小可用**形态：左侧连接列表（点选 + 连接/断开/刷新按钮）、右侧单行 SQL 输入
  （ASCII 键盘编辑、`←/→` 移动光标、`Ctrl/Cmd+V` 粘贴、`Enter` 执行）与结果表格
  （最多渲染 200 行，超出显示提示）。尚无库表浏览 / 新建连接表单 / 多行编辑——这些在 Web 端更完善
- 运行：`cd rust && cargo run -p polydb-gui`（构建含 GPUI，Windows 下首次编译较慢）
