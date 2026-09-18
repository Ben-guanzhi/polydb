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
- 技术栈：charmbracelet bubbletea + bubbles + lipgloss；进程内直连 `pkg/appcore`（同一进程，零序列化）
- 与 server 共用同一份连接元数据：`storage.DataDir()`（`POLYDB_DATA_DIR` 优先，否则用户配置目录 `polydb/`）
- 运行：`go run ./cmd/polydb-tui`
- 视图与按键：
  - 连接列表：`↑/↓` 或 `k/j` 选择，`Enter` 打开，`n` 新建，`t` 测试，`d` 删除（再按一次确认），`r` 刷新，`q` 退出
  - 新建表单：`Tab`/`↑↓` 切换字段，`←/→` 切换类型，`Enter` 提交，`Esc` 返回
  - 库表浏览：`Enter` 表详情，`s` 切换 schema，`q` 查询，`Esc` 返回
  - 查询：多行输入，`F5`/`Ctrl+E` 执行，`Esc` 返回
- 注意：打开连接时若已连接则跳过 `Connect`（`appcore.IsConnected` 判断），否则重复 Connect 会重开驱动导致 `:memory:` 会话数据丢失
