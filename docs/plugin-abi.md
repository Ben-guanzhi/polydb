# 插件 ABI 与信任门禁设计（知识保全，源自 dbstudio）

> **来源与状态**：本文件是 dbstudio（已归档，远端 github.com:Ben-guanzhi/dbstudio）
> `crates/plugin`（abi.rs / loader.rs / trust.rs / bridge.rs，约 1.5k 行 + 测试）与
> `plugins/sqlite-plugin`（581 行 cdylib 参考实现）的设计保全——polydb **无对应物**，
> 该体系是"第三方驱动以独立编译产物接入"的差异化选项（docs/workspace-analysis.md
> 点名保留的三份知识中最后一份；另两份 SSH 边界与 Vim 均已并入）。若未来实现，
> 本文件是口径起点；细节以归档仓源码与测试为准。

## 1. 为什么是 C ABI（而不是 extern "Rust"）

dbstudio v2 曾用 `extern "Rust" fn create_plugin() -> Box<dyn DatabasePlugin>`，方法返回
`anyhow::Result<String>`。逐条否决理由（同样适用于未来任何宿主/插件分体编译的方案）：

- `extern "Rust"` 是 rustc 内部 ABI，随时可变——插件与宿主用不同工具链构建时，
  第一次调用就是未定义行为，且无可诊断症状。
- `Box<dyn Trait>` 是两个指针，但 vtable 槽序只是"两侧恰好编译了同一 trait 定义"的巧合。
- `anyhow::Error` 是 `Box<dyn Error + Send + Sync>`，其 `Display` 分派经过**插件侧**的
  对象布局——跨工具链无稳定性承诺。
- `String` 跨分配器：插件分配、宿主释放。Linux/macOS 靠符号插入侥幸存活，
  Windows 上 DLL 导出符号不做同等插入，是经典的 invalid pointer 崩溃。

**线上只允许四种值**：`*const c_char`、`*mut c_char`、`u32`、回调指针。没有任何 Rust 形状过界。

## 2. 三条铁律

1. **所有权编码在指针类型，不靠约定**：槽位返回的 `*mut c_char` 由**插件**分配，宿主必须用
   该插件自己导出的 `free_string` 槽释放。两个分配器永不混用、绝不二次释放。
2. **错误是字符串，不是富错误类型**：宿主侧的 `anyhow` 是宿主概念；线上只传可显示的消息。
   插件内部保留完整错误链，只外发调用方能展示的部分。
3. **先探版本，再解引用**：宿主先读 `*_plugin_abi_version`（纯 `fn() -> u32`，无布局），
   版本不符就绝不去解引用函数表——布局错配永远不可能被当真解释。

## 3. 函数表的演进策略

槽位**只追加、不重排**；未使用的尾部槽位填 null 指针，宿主把 null 槽视为"未实现"并回落
trait 默认实现。因此**新增方法不强制 ABI bump**——这是 v2 为一次方法新增付出全量重编的
代价换来的规则。

槽位清单（`PluginTable`）：`free_string` / `info` / `can_handle`（三者必备）＋ `connect` /
`execute` / `list_databases` / `list_tables` / `list_columns` / `list_indexes` /
`list_foreign_keys` / `get_create_table_sql` / `ping` / `close` / `switch_database`。
复杂参数与返回值一律 JSON 字符串过界（JSON 不含裸 NUL；序列化侧对内嵌 NUL 截断处理，
FFI 边界不 panic）。

## 4. SHA-256 信任门禁

- **清单**：`plugin_trust.json`，与插件目录同根（`data_local_dir()/<app>/`），内容为
  `[{path, sha256}]`；读：文件不存在 = "尚未信任任何插件"（正常态）；写：**临时文件 + 改名**，
  中途失败不留半截 JSON。
- **判定三态**：`Trusted`（在册且摘要一致）/ `Unknown`（路径不在册）/ `Tampered{expected,
  actual}`（在册但内容已变）。
- **时序红线**：信任判定必须发生在 `libloading::Library::new` **之前**——dlopen 会立即执行
  目标 DLL/SO 的初始化代码；读字节算摘要是安全的，dlopen 不是。"先问 dlopen 要句柄再校验"
  等于让未审代码先跑起来。
- 摘要 = 全文件 SHA-256 + 字节数；UI 侧在设置面板列出插件库并原地点名信任（dbstudio
  `15de495`），Tampered 一律拒绝加载并展示期望/实际摘要。

## 5. 参考实现与验证资产（在归档仓）

- `plugins/sqlite-plugin`：rusqlite 经 C ABI 导出的完整参考——证明**宿主与插件可用不同
  工具链分别构建**（这正是 C 边界存在的全部意义）。
- `crates/plugin/src/loader_tests.rs`（408 行）与 `trust_tests.rs`（331 行）：跨边界所有权
  （释放恰好一次）、版本门、篡改拒载、清单原子写的行为锁定；未来实现时对照迁移。

## 6. 若 polydb 未来实现

- 落点：新建 `rust/crates/plugin`（宿主侧 ABI + loader + trust）+ 参考插件；驱动分派在
  `db-core` 的 Connection 枚举加 `Plugin` 变体（dbstudio `delegate_to_driver!` 宏的分派
  模式可照搬）。
- 边界（对照 AGENTS 铁律）：插件 crate 不依赖任何 driver crate（依赖方向 driver → plugin），
  保证 cdylib 独立编译；信任清单沿用 `data_local_dir()` 布局；加载前置信任门与本文件 §4 同口径。
- 决策入口：动工前先按铁律 8 扩契约（插件生命周期/DLL 搜索路径/信任流程属跨端行为面）。
