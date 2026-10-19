# d:/tools/db 工作台项目分析

> 生成日期：2026-10-18
> 口径：LOC 为实测值（`find -name "*.go" -o "*.rs" -o "*.ts" -o "*.tsx" -o "*.vue" -o "*.py"`，排除 `target/`、`node_modules/`、`dist/`、`build/`）；功能结论来自各项目 README / DESIGN / 源码走读。

## 1. 总览

工作台实为 **5 个重叠的数据库客户端 + 2 个无关桌面应用**。

| 项目 | LOC | 技术栈 | 领域 | 角色 |
|---|---|---|---|---|
| **polydb** | **114k** | Rust + Go + Web 三端，契约驱动 | DB 客户端 | **统一基线（M0–M17 已完成，CI 门禁齐全）** |
| sqltui-main | 48k | Go + Bubble Tea v2 TUI | DB 客户端 | TUI 资产富矿（独立演进中） |
| dbstudio | 31k | Rust + GPUI | DB 客户端 | UI 组件移植来源（A1 已入 polydb） |
| TableProGo | 20k | Go + Wails3 + React 18 | DB 客户端 | 与 fynequery 同血脉 |
| fynequery | 17k | Go + Fyne v2 | DB 客户端 | 与 TableProGo 共享 SQL 模块 |
| idea-git-tool | 9k | Tauri 2 + React 19 + git2 | Git 客户端 | 无关应用，健康 |
| poetry-desktop | 5k | Go + Fyne v2 | 诗词阅读器 | 无关应用，健康 |

## 2. 血脉谱系

```
TableProGo ──共享 tableprosql/tableproxlsx 模块── fynequery   （Go 一族，同作者）
dbstudio ──A1 移植（编辑器/Vim/对象树/命令面板/AI 面板）──> polydb/rust/crates/ui-gui
sqltui ──── 无共享，独立血统 ───> 但 polydb 的 Go TUI 同为 bubbletea（可对接）
polydb ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈ 吸收终点（spec/ 契约 + genprotocol 防漂移）
```

关键事实：

- **fynequery 与 TableProGo 是同一产品的两个 UI**：go.mod 互相 pinned（`tableprosql v0.1.1` 按 tag 引用），`app.go` 的 connObserved/histObserved 多客户端文件合并就是为共享状态文件设计的；fynequery 有 CI job 防共享模块漂移。
- **dbstudio 的增量已基本搬空**：A1 移植完成（SQL 编辑器/Vim/对象树/命令面板/AI 面板已入 polydb Rust GUI），其 CHANGELOG 停留在 0.1.0 初始版，实际功能都在 README——现在是"参考实现"而非演进项目。
- **sqltui 是被低估的一个**：48k LOC 中 `internal/ui` 占 30.8k（~30 个 overlay 弹窗、43 套主题、绘图、embedded SQLite 引擎、12 种文件格式 reader/writer），TUI 完成度远超 polydb 当前的 Go TUI。

## 3. 各项目详析与独有资产

整合时真正要抢的是下文标注的"独有资产"，而非整体搬代码。

### 3.1 polydb（基线，架构勿动）

- `spec/`：openapi（~30 端点）+ asyncapi（WS 查询通道）+ 9 个 JSON Schema + behavior/encoding 文档；`tools/genprotocol` 的 `check` 子命令做 spec ↔ Rust ↔ Go 线格式三方比对，漂移即 CI 失败。
- Rust/Go **双实现**同一契约；Web（React+Monaco，~35 组件）为主前端，Go TUI、Rust GPUI 为辅。
- 6 驱动双语言实现（SQLite/PG/MySQL/MSSQL/Oracle/Redis）；MCP server、schema diff、ER 图、compare/sync、只读连接、Bearer auth。
- 入口：`cargo run -p polydb-server|-p polydb-gui`（rust/）、`go run ./cmd/polydb-server|tui|cli|mcp`（go/）、`npm run dev`（web/）；`make check-protocol` 为协议门禁。

### 3.2 dbstudio → 可归档，留三份文档

- SSH 隧道 TOFU + 双运行时边界（smol/tokio 交接点）的踩坑记录在 README §架构说明——polydb 也有 sshtunnel crate/pkg，这份边界知识值得并入本 docs。
- 插件 ABI（cdylib 独立编译）设计——polydb 无对应物，是差异化选项。
- Vim 模式（已移植）、MCP crate（polydb 已有 go/pkg/mcp + cmd/polydb-mcp，重复）。

### 3.3 TableProGo + fynequery → 提取语义后归档

- **硬化 loopback API 模式**：`127.0.0.1:0` 临时端口 + 每进程随机 token + 逐 origin CORS 白名单（localhost echo，永不用 `*`）+ DPAPI/keyring 密钥——防恶意网页打本地端口。polydb server 目前只有 Bearer auth，无此模式。
- **keyset 分页**：拒绝不可 seek 键类型（显式失败而非静默退化）。
- **change engine 语义**：单事务全量应用、"matched no row" 守卫、MySQL `ClientFoundRows` 让重复保存同值也计数。
- fynequery 的测试纪律：`internal/db` 46% 测试代码占比、真实 SQLite 回滚矩阵、CI 跑 mysql:8.0/postgres:16 容器。
- `internal/db/dialect.go`（836 LOC，五引擎目录 SQL）——与 polydb 各 driver 的 catalog SQL 重叠，需对照合并去重。

### 3.4 sqltui → 唯一需要做决策的

- `internal/db/` 已有 `Backend`/`KVBackend` 契约 + 7 个后端实现（mysql/pg/mssql/oracle/sqlite/redis/oobe）——与 polydb `dbcore` 的 DatabaseDriver/SqlDriver/KvDriver 抽象同构，天然可对接。
- TUI 资产：`ui/dbmode`（连接表单、schema 浏览、Redis keys）、命令面板、SQL 编辑器+补全、`data.Frame` 列式模型。
- Bubble Tea v2 踩坑记录（DESIGN.md 内有验证过的 cheatsheet）可直接复用。
- 独有层：12 种文件格式 reader/writer、embedded SQLite 引擎、histogram/scatter 绘图、43 套主题。

### 3.5 idea-git-tool / poetry-desktop → 与 DB 整合无关

- idea-git-tool 处于 stability-hardening 收尾期（0.1.0 已发；hunk 级暂存、三方冲突解决、34 个 Tauri 命令齐全）。
- poetry-desktop 在修数据完整性 bug（FTS trigram、数据集目录冲突修复期）。
- 两者保持独立仓库即可，不纳入 DB 客户端整合。

## 4. 功能重叠矩阵

| 能力 | polydb | dbstudio | TableProGo | fynequery | sqltui |
|---|---|---|---|---|---|
| SQLite/PG/MySQL/MSSQL/Oracle | ✅ 双语言 | ✅ | ✅ | ✅ | ✅ |
| Redis | ✅ 双语言 | ❌ | ❌ | ❌ | ✅ |
| 提交模型（队列+预览+单事务） | ✅ changes | ⚠️ 直接执行 | ✅ | ✅ | ⚠️ 单元格直接编辑 |
| 安全模式分级 | ❌ | ❌ | ❌ | ❌（仅破坏性确认） | ❌ |
| 导出格式 | CSV/JSON/SQL/XLSX | CSV/JSON | CSV/JSON/SQL/XLSX | CSV/JSON/SQL/XLSX | 6 种 + parquet |
| 前端形态 | Web+TUI+GUI | GPUI | Wails/React | Fyne | bubbletea |
| Vim | ✅(GUI) | ✅ | ❌ | ❌ | ❌ |
| 契约测试/CI 真库容器 | ✅ 最强 | 基础 | 有 | 有 | 基础 |

另注：TablePlus 官方已验证"显式提交 + Preview + 6 级安全模式"是同类产品的事实标准交互（见竞品调研），表中 5 个项目在这两项上全部空缺或只有部分实现——是 polydb 建立差异化的一致方向。

## 5. 整合建议（按优先级）

1. **dbstudio → 冻结归档**。A1 已搬完，剩余动作仅：README 架构踩坑（双运行时边界、TOFU）并入 `docs/`；插件 ABI 是否移植作为独立议题。
2. **fynequery + TableProGo → 提取语义后归档**。重点不是搬代码，而是补 polydb 的三个洞：
   - loopback API 硬化（token + 逐 origin CORS）
   - keyset 分页拒绝策略
   - change engine 三守卫语义（单事务 / matched-no-row / ClientFoundRows）
   - dialect SQL 与 polydb driver 对照合并去重。
3. **sqltui → 决策点**（见 §6 选项 A/B）。
4. **idea-git-tool、poetry-desktop → 不纳入**，仅是同工作台邻居。
5. **命名清理**：TableProGo 的 module 路径带作者名（`github.com/Ben-guanzhi/TableProGo/...`），归档或提取时注意。

## 6. 待决策点

1. **sqltui 归宿**：
   - 方案 A：sqltui 的 `ui/dbmode` + `data.Frame` 适配成 polydb 的 `cmd/polydb-tui` 前端（工作量大，一次性获得全场最强 TUI）
   - 方案 B：sqltui 保持独立发行版，改为经 spec 契约消费 polydb 后端（最符合契约驱动架构，但其本地 driver 层要重写成 transport 客户端）
   - 倾向 A 的变体：sqltui 保留独有的文件格式/绘图/主题层，数据库浏览部分切换为 polydb transport 客户端。
2. **polydb server 是否采用 TableProGo 的 token+CORS 硬化模式**（当前仅 Bearer auth，无逐 origin 白名单）。
3. **dbstudio 的插件 ABI 是否移植进 polydb**（唯一未被吸收的差异化设计）。
4. **安全模式分级是否进入 polydb 路线图**（5 个自有项目 + TablePlus 竞品均空缺，属于可建立的差异化）。
