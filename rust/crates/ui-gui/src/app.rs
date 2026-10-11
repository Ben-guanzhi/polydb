//! 应用状态与动作层：实体结构、异步 IO 编排（G1 壳层重构后的单一实体）。
//! 渲染在 view.rs，纯函数在 util/tabs/profile，颜色 token 在 theme。

use std::collections::{HashMap, HashSet};
use std::future::Future;
use std::ops::Range;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use gpui::*;

use async_trait::async_trait;

type BoxFuture<'a, T> = std::pin::Pin<Box<dyn Future<Output = T> + Send + 'a>>;
use polydb_core::{
    BeginTransactionRequest, ColumnInfo, ConnectionId, ConnectionInfo, CoreResult,
    CreateConnectionRequest, DatabaseKind, FilterCondition, FilterLogic, ForeignKeyInfo, IndexInfo,
    OrderClause, QueryResult, RedisKeyInfo, RedisValue, ResultColumn, SchemaDiffOptions,
    SchemaDiffRequest, SchemaDiffTarget, SchemaInfo, SortDirection, TableInfo, TableRowsRequest,
    UpdateConnectionRequest, Value,
};
use polydb_transport::Transport;

use crate::changes::{self, ChangeQueue, Row};
use crate::cmpsync::{self, CmpAction, SyncExec};
use crate::exporters::{self, ExportFormat};
use crate::filters::{self, FilterDraft, FilterPreset};
use crate::presets;
use crate::profile::{self, ProfileColumn};
use crate::tabs::{self, ActiveView};
use crate::theme::Theme;
use crate::util::{
    blob_fetch_err_text, char_ceil, char_floor, col_to_byte, edit_line, line_bounds,
    newline_insert, split_statements, word_at, word_ceil, word_floor,
};

// ─── IO 桥 ─────────────────────────────────────────────────
// gpui 自带 executor，而驱动（sqlx / tiberius / redis）的 future 需要 tokio
// reactor。所有 transport 调用统一委托给专用多线程 runtime；
// `JoinHandle` 是标准 `Future`，在 gpui 侧 await 合法。

pub(crate) struct IoBridge {
    rt: tokio::runtime::Runtime,
}

impl IoBridge {
    fn new() -> Self {
        let rt = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(4)
            .thread_name("polydb-gui-io")
            .enable_all()
            .build()
            .expect("build io runtime");
        Self { rt }
    }

    fn spawn<T, F>(&self, f: F) -> tokio::task::JoinHandle<T>
    where
        F: Future<Output = T> + Send + 'static,
        T: Send + 'static,
    {
        self.rt.spawn(f)
    }
}

// ─── 领域状态 ───────────────────────────────────────────────

/// 单条语句的执行结果（✓ 行数 / ✗ 错误 / ⊘ 取消跳过）。
#[derive(Clone)]
pub(crate) enum StmtOutcome {
    Ok(usize),
    Err(String),
    Skipped,
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum Rail {
    Overview,
    Data,
    Structure,
    Relations,
}

impl Rail {
    pub(crate) fn label(self) -> &'static str {
        match self {
            Rail::Overview => "概览",
            Rail::Data => "数据",
            Rail::Structure => "结构",
            Rail::Relations => "关系",
        }
    }
}

pub(crate) struct TableDetail {
    pub columns: Vec<ColumnInfo>,
    pub indexes: Vec<IndexInfo>,
    pub fks: Vec<ForeignKeyInfo>,
    pub ddl: String,
}

/// 引用「本表」的外键（关系 rail 入向扫描结果）。
pub(crate) struct IncomingFk {
    pub schema: String,
    pub table: String,
    pub fk: ForeignKeyInfo,
}

#[derive(Clone)]
pub(crate) struct GridData {
    pub columns: Vec<ResultColumn>,
    pub rows: Vec<Vec<Value>>,
    pub elapsed_ms: f64,
}

pub(crate) const PAGE_SIZES: [u32; 3] = [50, 200, 1000];

pub(crate) struct BrowsePage {
    pub offset: u64,
    pub limit: u32,
    pub has_more: bool,
    pub total: Option<u64>,
    pub grid: Option<GridData>,
    pub loading: bool,
    /// 过滤栏草稿行（尚未应用也在其中；应用时才转 conditions 上线）。
    pub filters: Vec<FilterDraft>,
    /// 多条件组合逻辑。
    pub logic: FilterLogic,
    /// 单列排序（表头点击循环 升→降→无）。
    pub sort: Option<(String, SortDirection)>,
    /// 精确计数结果（None=未计或已因条件/翻页失效）。
    pub exact: Option<u64>,
    pub counting: bool,
}

pub(crate) struct TableTabState {
    pub schema: String,
    pub table: String,
    pub rail: Rail,
    pub detail: Option<TableDetail>,
    pub loading_detail: bool,
    pub detail_err: Option<String>,
    pub browse: BrowsePage,
    pub profile: Option<profile::ProfileResult>,
    pub loading_profile: bool,
    pub incoming: Option<Vec<IncomingFk>>,
    pub loading_incoming: bool,
    /// G3 变更跟踪队列：单元格编辑本地暂存，Review 后单事务提交。
    pub changes: ChangeQueue,
}

impl TableTabState {
    fn new(schema: String, table: String) -> Self {
        Self {
            schema,
            table,
            rail: Rail::Data,
            detail: None,
            loading_detail: false,
            detail_err: None,
            browse: BrowsePage {
                offset: 0,
                limit: 200,
                has_more: false,
                total: None,
                grid: None,
                loading: false,
                filters: Vec::new(),
                logic: FilterLogic::And,
                sort: None,
                exact: None,
                counting: false,
            },
            profile: None,
            loading_profile: false,
            incoming: None,
            loading_incoming: false,
            changes: ChangeQueue::new(),
        }
    }
}

// 预览/右键菜单作用域：挂在哪个网格上（工作台结果 or 某表 tab 数据页）。
#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum GridSrc {
    Workbench,
    Table(usize),
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum PreviewTab {
    Text,
    Json,
    Hex,
}

#[derive(Clone, Copy)]
pub(crate) struct PreviewState {
    pub src: GridSrc,
    pub row: usize,
    pub col: usize,
    pub tab: PreviewTab,
}

#[derive(Clone, Copy)]
pub(crate) struct CellMenuState {
    pub x: f32,
    pub y: f32,
    pub src: GridSrc,
    pub row: usize,
    pub col: usize,
}

/// D3 执行计划浮层：实际下发的 EXPLAIN 语句 + 展平后的计划行。
#[derive(Clone)]
pub(crate) struct ExplainState {
    pub(crate) sql: String,
    pub(crate) lines: Vec<String>,
}

#[derive(Clone)]
pub(crate) struct HbState {
    pub ok: bool,
    pub ms: f64,
    pub err: Option<String>,
}

// ─── G4 连接 CRUD 表单 ───

/// 连接表单里可键盘编辑的文本字段。
#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub(crate) enum ConnField {
    Name,
    Host,
    Port,
    Database,
    Username,
    Password,
    Group,
    Color,
    SshHost,
    SshPort,
    SshUsername,
    SshKeyPath,
    SshPassword,
    SshPassphrase,
}

/// 可编辑连接表单：新建（editing=None）或编辑既有连接（editing=Some(id)）。
/// 纯 UI 态；保存时映射到 `CreateConnectionRequest` / `UpdateConnectionRequest`。
pub(crate) struct ConnForm {
    pub editing: Option<ConnectionId>,
    pub kind: DatabaseKind,
    pub read_only: bool,
    pub ssh: bool,
    pub values: HashMap<ConnField, String>,
    /// 当前聚焦（键盘编辑）的文本字段。
    pub active: Option<ConnField>,
    pub cursor: usize,
}

impl ConnForm {
    fn blank() -> Self {
        let mut values = HashMap::new();
        values.insert(ConnField::Color, "#2d68c8".to_string());
        Self {
            editing: None,
            kind: DatabaseKind::Sqlite,
            read_only: false,
            ssh: false,
            values,
            active: None,
            cursor: 0,
        }
    }

    pub(crate) fn get(&self, f: ConnField) -> &str {
        self.values.get(&f).map(String::as_str).unwrap_or("")
    }

    fn set(&mut self, f: ConnField, v: String) {
        self.values.insert(f, v);
    }

    /// 取修剪后的可选字符串；空串→None。
    fn opt(&self, f: ConnField) -> Option<String> {
        let t = self.get(f).trim();
        (!t.is_empty()).then(|| t.to_string())
    }

    fn port_of(&self, f: ConnField, default: u16) -> u16 {
        self.get(f).trim().parse::<u16>().unwrap_or(default)
    }

    fn from_info(info: &ConnectionInfo) -> Self {
        let mut form = Self::blank();
        form.editing = Some(info.id);
        form.kind = info.kind;
        form.read_only = info.read_only == Some(true);
        form.set(ConnField::Name, info.name.clone());
        form.set(ConnField::Host, info.host.clone().unwrap_or_default());
        form.set(
            ConnField::Port,
            info.port.map(|p| p.to_string()).unwrap_or_default(),
        );
        form.set(
            ConnField::Database,
            info.database.clone().unwrap_or_default(),
        );
        form.set(
            ConnField::Username,
            info.username.clone().unwrap_or_default(),
        );
        form.set(ConnField::Group, info.group.clone().unwrap_or_default());
        form.set(
            ConnField::Color,
            info.color.clone().unwrap_or_else(|| "#2d68c8".into()),
        );
        // 密码/口令从不回填（服务端只存 keyring 引用）。
        if let Some(ssh) = &info.ssh_tunnel {
            form.ssh = true;
            form.set(ConnField::SshHost, ssh.host.clone());
            form.set(ConnField::SshPort, ssh.port.to_string());
            form.set(ConnField::SshUsername, ssh.username.clone());
            form.set(
                ConnField::SshKeyPath,
                ssh.private_key_path.clone().unwrap_or_default(),
            );
        }
        form
    }

    fn ssh_tunnel(&self) -> Option<polydb_core::SshTunnelConfig> {
        if !self.ssh {
            return None;
        }
        Some(polydb_core::SshTunnelConfig {
            host: self.get(ConnField::SshHost).trim().to_string(),
            port: self.port_of(ConnField::SshPort, 22),
            username: self.get(ConnField::SshUsername).trim().to_string(),
            password_ref: None,
            password: self.opt(ConnField::SshPassword),
            private_key_path: self.opt(ConnField::SshKeyPath),
            private_key_passphrase_ref: None,
            private_key_passphrase: self.opt(ConnField::SshPassphrase),
        })
    }

    fn to_create(&self) -> CreateConnectionRequest {
        CreateConnectionRequest {
            name: self.get(ConnField::Name).trim().to_string(),
            kind: self.kind,
            host: self.opt(ConnField::Host),
            port: (self.kind != DatabaseKind::Sqlite).then(|| self.port_of(ConnField::Port, 0)),
            database: self.opt(ConnField::Database),
            username: self.opt(ConnField::Username),
            password_ref: None,
            password: self.opt(ConnField::Password),
            options: HashMap::new(),
            ssh_tunnel: self.ssh_tunnel(),
            default_schema: None,
            read_only: Some(self.read_only),
            group: self.opt(ConnField::Group),
            color: self.opt(ConnField::Color),
        }
    }

    fn to_update(&self) -> UpdateConnectionRequest {
        UpdateConnectionRequest {
            name: Some(self.get(ConnField::Name).trim().to_string()),
            host: self.opt(ConnField::Host),
            port: (self.kind != DatabaseKind::Sqlite).then(|| self.port_of(ConnField::Port, 0)),
            database: self.opt(ConnField::Database),
            username: self.opt(ConnField::Username),
            password_ref: None,
            password: self.opt(ConnField::Password),
            options: None,
            ssh_tunnel: self.ssh_tunnel(),
            default_schema: None,
            read_only: Some(self.read_only),
            group: self.opt(ConnField::Group),
            color: self.opt(ConnField::Color),
        }
    }
}

/// 表单里某个文本行的渲染描述（按 kind / ssh 条件显隐）。
pub(crate) struct ConnFieldSpec {
    pub field: ConnField,
    pub label: &'static str,
    pub placeholder: &'static str,
    pub secret: bool,
}

/// 依 kind 与是否启用 SSH 返回可见文本字段清单（对齐 Web ConnectionList 的条件显隐）。
pub(crate) fn conn_visible_fields(kind: DatabaseKind, ssh: bool) -> Vec<ConnFieldSpec> {
    let mut v = vec![ConnFieldSpec {
        field: ConnField::Name,
        label: "名称",
        placeholder: "连接名称",
        secret: false,
    }];
    match kind {
        DatabaseKind::Sqlite => v.push(ConnFieldSpec {
            field: ConnField::Database,
            label: "数据库文件",
            placeholder: ":memory: 或路径",
            secret: false,
        }),
        DatabaseKind::Redis => {
            v.push(ConnFieldSpec {
                field: ConnField::Host,
                label: "主机",
                placeholder: "127.0.0.1",
                secret: false,
            });
            v.push(ConnFieldSpec {
                field: ConnField::Port,
                label: "端口",
                placeholder: "6379",
                secret: false,
            });
            v.push(ConnFieldSpec {
                field: ConnField::Database,
                label: "库编号",
                placeholder: "0",
                secret: false,
            });
            v.push(ConnFieldSpec {
                field: ConnField::Password,
                label: "密码",
                placeholder: "（可选，仅存 keyring）",
                secret: true,
            });
        }
        _ => {
            v.push(ConnFieldSpec {
                field: ConnField::Host,
                label: "主机",
                placeholder: "127.0.0.1",
                secret: false,
            });
            v.push(ConnFieldSpec {
                field: ConnField::Port,
                label: "端口",
                placeholder: "默认端口",
                secret: false,
            });
            if kind == DatabaseKind::Postgres {
                v.push(ConnFieldSpec {
                    field: ConnField::Database,
                    label: "数据库",
                    placeholder: "postgres",
                    secret: false,
                });
            }
            v.push(ConnFieldSpec {
                field: ConnField::Username,
                label: "用户名",
                placeholder: "user",
                secret: false,
            });
            v.push(ConnFieldSpec {
                field: ConnField::Password,
                label: "密码",
                placeholder: "（可选，仅存 keyring）",
                secret: true,
            });
        }
    }
    v.push(ConnFieldSpec {
        field: ConnField::Group,
        label: "分组",
        placeholder: "（可选）",
        secret: false,
    });
    v.push(ConnFieldSpec {
        field: ConnField::Color,
        label: "颜色",
        placeholder: "#2d68c8",
        secret: false,
    });
    if ssh && kind != DatabaseKind::Sqlite {
        v.extend([
            ConnFieldSpec {
                field: ConnField::SshHost,
                label: "SSH 主机",
                placeholder: "bastion",
                secret: false,
            },
            ConnFieldSpec {
                field: ConnField::SshPort,
                label: "SSH 端口",
                placeholder: "22",
                secret: false,
            },
            ConnFieldSpec {
                field: ConnField::SshUsername,
                label: "SSH 用户",
                placeholder: "root",
                secret: false,
            },
            ConnFieldSpec {
                field: ConnField::SshKeyPath,
                label: "私钥路径",
                placeholder: "~/.ssh/id_rsa",
                secret: false,
            },
            ConnFieldSpec {
                field: ConnField::SshPassword,
                label: "SSH 密码",
                placeholder: "（可选）",
                secret: true,
            },
            ConnFieldSpec {
                field: ConnField::SshPassphrase,
                label: "私钥口令",
                placeholder: "（可选）",
                secret: true,
            },
        ]);
    }
    v
}

/// SQL 编辑器快照：文本 + 光标 + 选区锚点，用于撤销/重做。
pub(crate) struct SqlSnapshot {
    pub(crate) sql: String,
    pub(crate) cursor: usize,
    pub(crate) anchor: Option<usize>,
}

/// Quick Switcher 目标：工作台 / 已打开 tab（索引）/ 尚未打开的表（schema, table）。
#[derive(Clone, PartialEq)]
pub(crate) enum QuickTarget {
    Workbench,
    Tab(usize),
    NewTable(String, String),
}

// ─── E5.5 结构比对与同步向导（浮层，四步在层内自走）───
//
// E6.3 抽层：步骤枚举、向导态、执行结果结构与「哪个键在哪一步干什么」的按键表全部
// 搬到 `cmpsync.rs`（纯层，无 gpui），`app.rs` 这里只剩接线——发 IO、`cx.notify()`、
// 把纯层返回的文案写进 `status`。re-export 让 `crate::app::CmpStep` 的旧路径仍可用。

pub(crate) use crate::cmpsync::{CmpStep, CompareState};

/// 把 `cmpsync::run_script` 需要的五个动作接到真实线路（E6.3）。
/// 抽层之前这段逻辑内联在 `cmp_apply` 的 `self.io` async 闭包里，
/// 于是三份呈现实现中唯有 GUI 这一份的分派环路没有测试可挂。
pub(crate) struct TransportExec<'a> {
    t: &'a dyn Transport,
    id: ConnectionId,
}

#[async_trait]
impl SyncExec for TransportExec<'_> {
    async fn begin(&self) -> CoreResult<String> {
        let info = self
            .t
            .begin_transaction(&BeginTransactionRequest {
                connection_id: self.id,
                isolation_level: None,
            })
            .await?;
        Ok(info.id.to_string())
    }

    async fn exec_in_tx(&self, tx: &str, sql: &str) -> CoreResult<()> {
        self.t.execute_in_transaction(tx, sql, &[]).await?;
        Ok(())
    }

    async fn exec(&self, sql: &str) -> CoreResult<()> {
        self.t.execute(self.id, sql, &[]).await?;
        Ok(())
    }

    async fn commit(&self, tx: &str) -> CoreResult<()> {
        self.t.commit_transaction(tx).await?;
        Ok(())
    }

    async fn rollback(&self, tx: &str) -> CoreResult<()> {
        self.t.rollback_transaction(tx).await?;
        Ok(())
    }
}

pub struct PolyDBApp {
    pub(crate) transport: Arc<dyn Transport>,
    pub(crate) io: Arc<IoBridge>,
    pub(crate) conns: Vec<ConnectionInfo>,
    /// 当前活动连接（None=列表为空/未选，右侧显示空态引导）。
    pub(crate) workspace: Option<usize>,
    pub(crate) theme: Theme,
    pub(crate) dark: bool,
    pub(crate) busy: bool,
    pub(crate) status: String,

    // ── 工作台（唯一常驻 tab）──
    pub(crate) sql: String,
    pub(crate) cursor: usize,
    /// 选区锚点（字节下标）。Some 且 != cursor 时表示存在选区。
    pub(crate) sql_anchor: Option<usize>,
    /// SQL 编辑器撤销/重做栈（快照含文本、光标、锚点）。
    pub(crate) sql_undo: Vec<SqlSnapshot>,
    pub(crate) sql_redo: Vec<SqlSnapshot>,
    /// 编辑器当前帧的文本布局句柄，用于鼠标像素→字节下标映射（每帧 render 时刷新）。
    pub(crate) sql_layout: Option<TextLayout>,
    /// 鼠标左键按下拖拽中（扩展选区）。
    pub(crate) sql_selecting: bool,
    pub(crate) focus: FocusHandle,
    pub(crate) result: Option<QueryResult>,
    pub(crate) multi_log: Vec<(String, StmtOutcome)>,
    pub(crate) cancel: Arc<AtomicBool>,

    // ── 全局浮层（预览/右键菜单，作用于某个网格源）──
    pub(crate) preview: Option<PreviewState>,
    pub(crate) ctx_menu: Option<CellMenuState>,
    /// D3 执行计划浮层（None=未打开）。
    pub(crate) explain: Option<ExplainState>,
    /// E5.5 结构比对与同步向导（None=未打开）。
    pub(crate) compare: Option<CompareState>,
    pub(crate) cmp_focus: FocusHandle,

    // ── §5.2 旁路大字段取回（预览/Inspector 单槽缓存）──
    /// 当前取回目标（引用 UUID）。展示块仅在单元格引用与此相等时显示 data/err。
    pub(crate) blob_key: Option<String>,
    pub(crate) blob_loading: bool,
    pub(crate) blob_data: Option<Vec<u8>>,
    pub(crate) blob_err: Option<String>,

    // ── 过滤栏值输入（复用单一聚焦句柄；编辑目标定位到当前表 tab 的某草稿某字段）──
    pub(crate) filter_focus: FocusHandle,
    /// (草稿行下标, 字段：0=值 1=第二值 2=IN列表)
    pub(crate) editing: Option<(usize, u8)>,
    /// 当前编辑字节的光标（字节下标，落在对应字符串内）。
    pub(crate) edit_cursor: usize,

    // ── G3 网格内联编辑（复用单一聚焦句柄，编辑目标 = 某表某行某列）──
    pub(crate) cell_focus: FocusHandle,
    /// 正在编辑的单元格 (tab 下标, 行, 列)。
    pub(crate) cell_edit: Option<(usize, usize, usize)>,
    /// 编辑缓冲 + 光标（字节下标）。
    pub(crate) cell_buf: String,
    pub(crate) cell_cursor: usize,
    /// Review SQL 面板是否展开。
    pub(crate) review_open: bool,
    /// 提交进行中（事务在途）。
    pub(crate) committing: bool,

    // ── G4 连接 CRUD 表单（浮层）──
    pub(crate) conn_focus: FocusHandle,
    /// 当前打开的连接表单（None=未打开）。
    pub(crate) conn_form: Option<ConnForm>,

    // ── G4.4 Redis 键浏览（只读子集）──
    pub(crate) redis_focus: FocusHandle,
    pub(crate) redis_db: u32,
    pub(crate) redis_pattern: String,
    /// 匹配模式输入是否处于编辑态（单一聚焦句柄 + armed 路由）。
    pub(crate) redis_editing: bool,
    /// 模式输入的字节光标。
    pub(crate) redis_cursor: usize,
    /// SCAN 游标（0 表示已遍历完）。
    pub(crate) redis_scan_cursor: u64,
    pub(crate) redis_keys: Vec<RedisKeyInfo>,
    pub(crate) redis_loading: bool,
    pub(crate) redis_selected: Option<String>,
    pub(crate) redis_value: Option<RedisValue>,
    /// D4 string 值整值写回子模式（对齐 TUI 的 `e`；其余类型走不了整值替换）。
    pub(crate) redis_val_editing: bool,
    /// 写回缓冲 + 字节光标（单行，与 TUI textinput 同形态）。
    pub(crate) redis_val_buf: String,
    pub(crate) redis_val_cursor: usize,

    // ── 壳层标签 ──
    pub(crate) active: ActiveView,
    pub(crate) tabs: Vec<TableTabState>,

    // ── G5.3 Quick Switcher（Ctrl+P 浮层）──
    pub(crate) quick_open: bool,
    pub(crate) quick_focus: FocusHandle,
    pub(crate) quick_query: String,
    pub(crate) quick_cursor: usize,
    /// 当前高亮项（在匹配列表中的下标）。
    pub(crate) quick_sel: usize,

    // ── schema 导航树 ──
    pub(crate) schemas: Vec<SchemaInfo>,
    pub(crate) active_schema: String,
    pub(crate) tables: Vec<TableInfo>,
    pub(crate) expanded: HashSet<usize>,
    pub(crate) cols_cache: HashMap<usize, Vec<ColumnInfo>>,
    pub(crate) cols_loading: HashSet<usize>,

    // ── 心跳 ──
    pub(crate) hb: Option<HbState>,
}

impl PolyDBApp {
    pub fn new(transport: Arc<dyn Transport>, cx: &mut Context<Self>) -> Self {
        let mut this = Self {
            transport,
            io: Arc::new(IoBridge::new()),
            conns: Vec::new(),
            workspace: None,
            theme: Theme::dark(),
            dark: true,
            busy: false,
            status: "加载中…".into(),
            sql: String::new(),
            cursor: 0,
            sql_anchor: None,
            sql_undo: Vec::new(),
            sql_redo: Vec::new(),
            sql_layout: None,
            sql_selecting: false,
            focus: cx.focus_handle(),
            result: None,
            multi_log: Vec::new(),
            cancel: Arc::new(AtomicBool::new(false)),
            preview: None,
            ctx_menu: None,
            explain: None,
            compare: None,
            cmp_focus: cx.focus_handle(),
            blob_key: None,
            blob_loading: false,
            blob_data: None,
            blob_err: None,
            filter_focus: cx.focus_handle(),
            editing: None,
            edit_cursor: 0,
            cell_focus: cx.focus_handle(),
            cell_edit: None,
            cell_buf: String::new(),
            cell_cursor: 0,
            review_open: false,
            committing: false,
            conn_focus: cx.focus_handle(),
            conn_form: None,
            redis_focus: cx.focus_handle(),
            redis_db: 0,
            redis_pattern: "*".into(),
            redis_editing: false,
            redis_cursor: 1,
            redis_scan_cursor: 0,
            redis_keys: Vec::new(),
            redis_loading: false,
            redis_selected: None,
            redis_value: None,
            redis_val_editing: false,
            redis_val_buf: String::new(),
            redis_val_cursor: 0,
            active: ActiveView::Workbench,
            tabs: Vec::new(),
            quick_open: false,
            quick_focus: cx.focus_handle(),
            quick_query: String::new(),
            quick_cursor: 0,
            quick_sel: 0,
            schemas: Vec::new(),
            active_schema: "main".into(),
            tables: Vec::new(),
            expanded: HashSet::new(),
            cols_cache: HashMap::new(),
            cols_loading: HashSet::new(),
            hb: None,
        };
        this.refresh(cx);
        this.start_heartbeat(cx);
        this
    }

    // ─── 通用异步编排：work 在 IO runtime 跑，done 回主线程更新实体 ───

    fn io<T, W, D>(&self, cx: &mut Context<Self>, work: W, done: D)
    where
        W: FnOnce(Arc<dyn Transport>) -> BoxFuture<'static, T> + Send + 'static,
        T: Send + 'static,
        D: FnOnce(&mut Self, T, &mut Context<Self>) + 'static,
    {
        let t = Arc::clone(&self.transport);
        let join = self.io.spawn(work(t));
        cx.spawn(async move |this, cx| {
            let out = join.await;
            let _ = this.update(cx, |this, cx| match out {
                Ok(v) => done(this, v, cx),
                Err(e) => {
                    this.status = format!("IO 任务失败: {e}");
                    cx.notify();
                }
            });
        })
        .detach();
    }

    /// 取回 §5.2 旁路大字段（单槽：新取回顶掉旧目标；过期回包按 blob_key 丢弃）。
    pub(crate) fn fetch_blob(&mut self, conn: ConnectionId, uuid: String, cx: &mut Context<Self>) {
        if self.blob_loading {
            return;
        }
        self.blob_key = Some(uuid.clone());
        self.blob_loading = true;
        self.blob_data = None;
        self.blob_err = None;
        cx.notify();
        let key = uuid.clone();
        self.io(
            cx,
            move |t| Box::pin(async move { t.get_blob(conn, &uuid).await }),
            move |this, out, cx| {
                this.blob_loading = false;
                if this.blob_key.as_deref() == Some(key.as_str()) {
                    match out {
                        Ok(bytes) => {
                            this.blob_data = Some(bytes);
                            this.blob_err = None;
                        }
                        Err(e) => {
                            this.blob_data = None;
                            this.blob_err = Some(blob_fetch_err_text(&e));
                        }
                    }
                }
                cx.notify();
            },
        );
    }

    pub(crate) fn sel_id(&self) -> Option<ConnectionId> {
        self.workspace.and_then(|i| self.conns.get(i).map(|c| c.id))
    }

    pub(crate) fn kind_str(&self) -> String {
        self.workspace
            .and_then(|i| self.conns.get(i))
            .map(|c| c.kind.to_string())
            .unwrap_or_default()
    }

    pub(crate) fn sel_kind(&self) -> Option<DatabaseKind> {
        self.workspace
            .and_then(|i| self.conns.get(i))
            .map(|c| c.kind)
    }

    /// U4 只读闭环：只读连接禁用全部写入口（服务端仍强制拒绝，双保险）。
    pub(crate) fn is_read_only(&self) -> bool {
        self.workspace
            .and_then(|i| self.conns.get(i))
            .and_then(|c| c.read_only)
            .unwrap_or(false)
    }

    pub(crate) fn toggle_theme(&mut self) {
        self.dark = !self.dark;
        self.theme = if self.dark {
            Theme::dark()
        } else {
            Theme::light()
        };
    }

    // ─── 连接列表刷新（按 id 重定位活动连接；失去焦点连接时自动落到第一条）───

    pub(crate) fn refresh(&mut self, cx: &mut Context<Self>) {
        let keep_id = self.sel_id();
        self.io(
            cx,
            move |t| Box::pin(async move { t.list_connections() }),
            move |this, out, cx| {
                match out {
                    Ok(conns) => {
                        let keep = keep_id.and_then(|id| conns.iter().position(|c| c.id == id));
                        let picked = keep.or(if conns.is_empty() { None } else { Some(0) });
                        let changed = picked != this.workspace;
                        this.conns = conns;
                        if changed {
                            match picked {
                                Some(i) => this.enter_workspace(i, cx),
                                None => {
                                    this.workspace = None;
                                    this.clear_shell_state();
                                    this.status = "暂无连接——请在 Web 端创建后点「刷新」".into();
                                }
                            }
                        } else {
                            this.status = "已加载连接列表".into();
                        }
                    }
                    Err(e) => this.status = format!("加载连接失败: {e}"),
                }
                cx.notify();
            },
        );
    }

    fn clear_shell_state(&mut self) {
        self.tabs.clear();
        self.active = ActiveView::Workbench;
        self.result = None;
        self.preview = None;
        self.ctx_menu = None;
        self.explain = None;
        self.compare = None;
        self.editing = None;
        self.multi_log.clear();
        self.tables.clear();
        self.expanded.clear();
        self.cols_cache.clear();
        self.redis_keys.clear();
        self.redis_selected = None;
        self.redis_value = None;
        self.redis_val_editing = false;
        self.redis_scan_cursor = 0;
    }

    /// 点选侧栏连接：切活动连接并重置壳层态；重复点击当前连接=重拉 schema 树。
    pub(crate) fn enter_workspace(&mut self, i: usize, cx: &mut Context<Self>) {
        if self.workspace == Some(i) {
            self.load_schema(cx);
            self.beat(cx);
            return;
        }
        self.workspace = Some(i);
        self.active = ActiveView::Workbench;
        self.tabs.clear();
        self.result = None;
        self.preview = None;
        self.ctx_menu = None;
        self.explain = None;
        self.compare = None;
        self.editing = None;
        self.multi_log.clear();
        self.status = "Enter 执行 · Shift+Enter 换行 · 双击侧栏表开标签".into();
        self.load_schema(cx);
        self.beat(cx);
        cx.notify();
    }

    pub(crate) fn connect(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else {
            self.status = "请先选择连接".into();
            cx.notify();
            return;
        };
        self.io(
            cx,
            move |t| Box::pin(async move { t.connect(id) }),
            |this, out, cx| {
                this.status = match out {
                    Ok(()) => "已连接（驱动实例已打开）".into(),
                    Err(e) => format!("连接失败: {e}"),
                };
                cx.notify();
            },
        );
    }

    pub(crate) fn disconnect(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else {
            self.status = "请先选择连接".into();
            cx.notify();
            return;
        };
        self.io(
            cx,
            move |t| Box::pin(async move { t.disconnect(id) }),
            |this, _, cx| {
                this.status = "已断开".into();
                cx.notify();
            },
        );
    }

    // ─── G4 连接 CRUD 表单 ───

    pub(crate) fn open_conn_new(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let mut form = ConnForm::blank();
        form.active = Some(ConnField::Name);
        form.cursor = 0;
        self.conn_form = Some(form);
        window.focus(&self.conn_focus);
        cx.notify();
    }

    pub(crate) fn open_conn_edit(
        &mut self,
        id: ConnectionId,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(info) = self.conns.iter().find(|c| c.id == id).cloned() else {
            return;
        };
        let mut form = ConnForm::from_info(&info);
        form.active = Some(ConnField::Name);
        form.cursor = form.get(ConnField::Name).len();
        self.conn_form = Some(form);
        window.focus(&self.conn_focus);
        cx.notify();
    }

    pub(crate) fn close_conn(&mut self, cx: &mut Context<Self>) {
        self.conn_form = None;
        cx.notify();
    }

    /// 直接设定数据库类型（编辑既有连接时禁用：kind 创建后不可改）。
    pub(crate) fn set_conn_kind(&mut self, kind: DatabaseKind, cx: &mut Context<Self>) {
        let Some(form) = &mut self.conn_form else {
            return;
        };
        if form.editing.is_some() {
            return;
        }
        form.kind = kind;
        cx.notify();
    }

    pub(crate) fn toggle_read_only(&mut self, cx: &mut Context<Self>) {
        if let Some(form) = &mut self.conn_form {
            form.read_only = !form.read_only;
        }
        cx.notify();
    }

    pub(crate) fn toggle_ssh(&mut self, cx: &mut Context<Self>) {
        if let Some(form) = &mut self.conn_form {
            form.ssh = !form.ssh;
        }
        cx.notify();
    }

    /// 聚焦某个文本字段进行键盘编辑（单一聚焦句柄 + armed 字段路由）。
    pub(crate) fn focus_conn_field(
        &mut self,
        f: ConnField,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if let Some(form) = &mut self.conn_form {
            form.cursor = form.get(f).len();
            form.active = Some(f);
        }
        window.focus(&self.conn_focus);
        cx.notify();
    }

    /// Tab/Shift-Tab 在可见字段间循环聚焦。
    fn conn_cycle_field(&mut self, back: bool, window: &mut Window, cx: &mut Context<Self>) {
        let Some(form) = &self.conn_form else {
            return;
        };
        let fields = conn_visible_fields(form.kind, form.ssh);
        if fields.is_empty() {
            return;
        }
        let cur = form
            .active
            .and_then(|a| fields.iter().position(|s| s.field == a))
            .unwrap_or(0);
        let n = fields.len();
        let next = if back {
            (cur + n - 1) % n
        } else {
            (cur + 1) % n
        };
        let f = fields[next].field;
        if let Some(form) = &mut self.conn_form {
            form.cursor = form.get(f).len();
            form.active = Some(f);
        }
        window.focus(&self.conn_focus);
        cx.notify();
    }

    pub(crate) fn on_conn_key(
        &mut self,
        ev: &KeyDownEvent,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(form) = &mut self.conn_form else {
            return;
        };
        let Some(active) = form.active else {
            return;
        };
        let ks = &ev.keystroke;
        match ks.key.as_str() {
            "escape" => {
                self.close_conn(cx);
                return;
            }
            "enter" => {
                self.save_conn(cx);
                return;
            }
            "tab" => {
                self.conn_cycle_field(false, window, cx);
                return;
            }
            "shift-tab" => {
                self.conn_cycle_field(true, window, cx);
                return;
            }
            _ => {}
        }
        let pasted = if (ks.modifiers.control || ks.modifiers.platform) && ks.key == "v" {
            cx.read_from_clipboard()
                .and_then(|i| i.text().map(|t| t.to_string()))
        } else {
            None
        };
        let entry = form.values.entry(active).or_default();
        if let Some(text) = pasted {
            let at = char_ceil(entry, form.cursor.min(entry.len()));
            entry.insert_str(at, &text);
            form.cursor = at + text.len();
            cx.notify();
        } else if edit_line(entry, &mut form.cursor, ks) {
            cx.notify();
        }
    }

    pub(crate) fn save_conn(&mut self, cx: &mut Context<Self>) {
        let Some(form) = &self.conn_form else {
            return;
        };
        if form.get(ConnField::Name).trim().is_empty() {
            self.status = "名称不能为空".into();
            cx.notify();
            return;
        }
        let editing = form.editing;
        if editing.is_some() {
            let req = form.to_update();
            let Some(id) = editing else {
                return;
            };
            self.io(
                cx,
                move |t| Box::pin(async move { t.update_connection(id, &req) }),
                |this, out, cx| {
                    let ok = out.is_ok();
                    this.status = match out {
                        Ok(_) => "连接已更新".into(),
                        Err(e) => format!("更新失败: {e}"),
                    };
                    if ok {
                        this.conn_form = None;
                        this.refresh(cx);
                    }
                    cx.notify();
                },
            );
        } else {
            let req = form.to_create();
            self.io(
                cx,
                move |t| Box::pin(async move { t.create_connection(&req) }),
                |this, out, cx| {
                    let ok = out.is_ok();
                    this.status = match out {
                        Ok(_) => "连接已创建".into(),
                        Err(e) => format!("创建失败: {e}"),
                    };
                    if ok {
                        this.conn_form = None;
                        this.refresh(cx);
                    }
                    cx.notify();
                },
            );
        }
    }

    pub(crate) fn delete_conn(&mut self, id: ConnectionId, cx: &mut Context<Self>) {
        if self.sel_id() == Some(id) {
            self.disconnect(cx);
        }
        self.io(
            cx,
            move |t| Box::pin(async move { t.delete_connection(id) }),
            |this, out, cx| {
                this.status = match out {
                    Ok(true) => "连接已删除".into(),
                    Ok(false) => "删除失败：连接不存在".into(),
                    Err(e) => format!("删除失败: {e}"),
                };
                this.refresh(cx);
                cx.notify();
            },
        );
    }

    pub(crate) fn test_conn(&mut self, id: ConnectionId, cx: &mut Context<Self>) {
        self.io(
            cx,
            move |t| Box::pin(async move { t.connection_status(id) }),
            |this, out, cx| {
                this.status = match out {
                    Ok(s) if s.connected => format!(
                        "连接正常 · {} · {:.1}ms",
                        s.server_version.unwrap_or_else(|| "未知版本".into()),
                        s.latency_ms.unwrap_or(0.0)
                    ),
                    Ok(s) => format!("未连接：{}", s.error.unwrap_or_else(|| "无错误信息".into())),
                    Err(e) => format!("测试失败: {e}"),
                };
                cx.notify();
            },
        );
    }

    // ─── G4 结果集导出（弹保存对话框 + 落盘）───

    pub(crate) fn export_grid(&mut self, src: GridSrc, fmt: ExportFormat, cx: &mut Context<Self>) {
        let cols: Vec<String> = match self.grid_cols(src) {
            Some(c) => c.iter().map(|x| x.name.clone()).collect(),
            None => {
                self.status = "无数据可导出".into();
                cx.notify();
                return;
            }
        };
        let rows: Vec<Vec<Value>> = match self.grid_rows(src) {
            Some(r) => r.to_vec(),
            None => {
                self.status = "无数据可导出".into();
                cx.notify();
                return;
            }
        };
        if rows.is_empty() {
            self.status = "无行可导出".into();
            cx.notify();
            return;
        }
        let kind = self.sel_kind().unwrap_or(DatabaseKind::Sqlite);
        let base = match src {
            GridSrc::Table(i) => self
                .tabs
                .get(i)
                .map(|t| format!("{}_{}", t.schema, t.table))
                .unwrap_or_else(|| "table".into()),
            GridSrc::Workbench => "query_result".into(),
        };
        let content = match fmt {
            ExportFormat::Csv => exporters::to_csv(&cols, &rows, true),
            ExportFormat::Tsv => exporters::to_tsv(&cols, &rows),
            ExportFormat::Json => exporters::to_json(&cols, &rows),
            ExportFormat::Ndjson => exporters::to_ndjson(&cols, &rows),
            ExportFormat::Markdown => exporters::to_markdown(&cols, &rows),
            ExportFormat::Insert => {
                let table = match src {
                    GridSrc::Table(i) => self
                        .tabs
                        .get(i)
                        .map(|t| format!("{}.{}", t.schema, t.table))
                        .unwrap_or_else(|| "t".into()),
                    GridSrc::Workbench => "query_result".into(),
                };
                exporters::to_sql_insert(kind, &table, &cols, &rows)
            }
        };
        let filename = format!("{base}.{}", fmt.extension());
        let dir = crate::presets::config_dir().unwrap_or_else(std::env::temp_dir);
        let rx = cx.prompt_for_new_path(&dir, Some(&filename));
        cx.spawn(async move |this, cx| {
            let picked = rx.await.ok().and_then(|r| r.ok()).flatten();
            let Some(path) = picked else { return };
            let result = std::fs::write(&path, content.as_bytes());
            let _ = this.update(cx, |this, cx| {
                this.status = match result {
                    Ok(()) => format!("已导出 {}", path.display()),
                    Err(e) => format!("导出失败: {e}"),
                };
                cx.notify();
            });
        })
        .detach();
    }

    // ─── G4.4 Redis 键浏览（选库 / SCAN / 取值）+ D4 string 整值写回 ───

    pub(crate) fn is_redis(&self) -> bool {
        self.sel_kind() == Some(DatabaseKind::Redis)
    }

    fn redis_pat(&self) -> String {
        let p = self.redis_pattern.trim();
        if p.is_empty() {
            "*".to_string()
        } else {
            p.to_string()
        }
    }

    /// 选库并从头扫描。
    pub(crate) fn redis_set_db(&mut self, index: u32, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else { return };
        let pattern = self.redis_pat();
        self.redis_selected = None;
        self.redis_value = None;
        self.redis_val_editing = false;
        self.redis_loading = true;
        cx.notify();
        self.io(
            cx,
            move |t| {
                Box::pin(async move {
                    t.select_db(id, index).await?;
                    t.scan_keys(id, 0, &pattern, 100).await
                })
            },
            move |this, out, cx| {
                this.redis_loading = false;
                match out {
                    Ok(page) => {
                        // 服务端回显优先于本地记忆值（spec/behavior.md §1.3）。
                        this.redis_db = page.db.unwrap_or(index);
                        let db = this.redis_db;
                        this.redis_scan_cursor = page.cursor;
                        this.redis_keys = page.keys;
                        this.status = format!("db{db} · 已载入 {} 键", this.redis_keys.len());
                    }
                    Err(e) => this.status = format!("切库失败: {e}"),
                }
                cx.notify();
            },
        );
    }

    /// 从游标 0 重新扫描当前库。
    pub(crate) fn redis_scan(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else { return };
        let pattern = self.redis_pat();
        self.redis_selected = None;
        self.redis_value = None;
        self.redis_val_editing = false;
        self.redis_loading = true;
        cx.notify();
        self.io(
            cx,
            move |t| Box::pin(async move { t.scan_keys(id, 0, &pattern, 100).await }),
            move |this, out, cx| {
                this.redis_loading = false;
                match out {
                    Ok(page) => {
                        this.redis_db = page.db.unwrap_or(this.redis_db);
                        let db = this.redis_db;
                        this.redis_scan_cursor = page.cursor;
                        this.redis_keys = page.keys;
                        this.status = format!("db{db} · 已载入 {} 键", this.redis_keys.len());
                    }
                    Err(e) => this.status = format!("扫描失败: {e}"),
                }
                cx.notify();
            },
        );
    }

    /// 追加下一页（游标非 0 时有效）。
    pub(crate) fn redis_more(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else { return };
        if self.redis_scan_cursor == 0 {
            self.status = "已到末尾".into();
            cx.notify();
            return;
        }
        let pattern = self.redis_pat();
        let from = self.redis_scan_cursor;
        self.redis_loading = true;
        cx.notify();
        self.io(
            cx,
            move |t| Box::pin(async move { t.scan_keys(id, from, &pattern, 100).await }),
            move |this, out, cx| {
                this.redis_loading = false;
                match out {
                    Ok(page) => {
                        this.redis_db = page.db.unwrap_or(this.redis_db);
                        this.redis_scan_cursor = page.cursor;
                        this.redis_keys.extend(page.keys);
                        this.status = format!("累计 {} 键", this.redis_keys.len());
                    }
                    Err(e) => this.status = format!("翻页失败: {e}"),
                }
                cx.notify();
            },
        );
    }

    /// 打开某个键读取值。
    pub(crate) fn redis_open_key(&mut self, key: String, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else { return };
        self.redis_selected = Some(key.clone());
        self.redis_value = None;
        self.redis_val_editing = false;
        cx.notify();
        self.io(
            cx,
            move |t| Box::pin(async move { t.get_value(id, &key).await }),
            move |this, out, cx| {
                match out {
                    Ok(v) => this.redis_value = Some(v),
                    Err(e) => this.status = format!("取值失败: {e}"),
                }
                cx.notify();
            },
        );
    }

    // ─── D4 string 值整值写回（对齐 TUI 的 `e`：仅 string、写后回读、失败留输入）───

    fn redis_write_block(&self) -> Option<&'static str> {
        crate::util::redis_write_block(self.redis_value.as_ref(), self.is_read_only())
    }

    /// 进入写回子模式，缓冲预填当前值。
    pub(crate) fn redis_start_edit_value(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if let Some(why) = self.redis_write_block() {
            self.status = why.to_string();
            cx.notify();
            return;
        }
        let raw = match self.redis_value.as_ref() {
            Some(RedisValue::String(s)) => s.clone(),
            _ => String::new(),
        };
        self.redis_editing = false;
        self.redis_val_buf = raw;
        self.redis_val_cursor = self.redis_val_buf.len();
        self.redis_val_editing = true;
        window.focus(&self.redis_focus);
        cx.notify();
    }

    pub(crate) fn redis_cancel_edit_value(&mut self, cx: &mut Context<Self>) {
        self.redis_val_editing = false;
        self.status = "已放弃写入".into();
        cx.notify();
    }

    /// 提交整值并回读确认：写失败留在编辑态保留输入，回读失败不把旧值当新值显示。
    pub(crate) fn redis_commit_edit_value(&mut self, cx: &mut Context<Self>) {
        let (Some(id), Some(key)) = (self.sel_id(), self.redis_selected.clone()) else {
            return;
        };
        self.redis_val_editing = false;
        cx.notify();
        let buf = self.redis_val_buf.clone();
        self.io(
            cx,
            move |t| {
                Box::pin(async move {
                    match t.set_value(id, &key, RedisValue::String(buf)).await {
                        Ok(()) => (None, Some(t.get_value(id, &key).await)),
                        Err(e) => (Some(e.to_string()), None),
                    }
                })
            },
            move |this, out, cx| {
                let (err, readback) = out;
                if let Some(e) = err {
                    this.redis_val_editing = true;
                    this.status = format!("写入失败: {e}");
                } else if let Some(Ok(v)) = readback {
                    this.redis_value = Some(v);
                    this.status = "已写入并回读确认".into();
                } else {
                    this.redis_value = None;
                    this.status = "已写入，但回读失败：点击键名重试确认".into();
                }
                cx.notify();
            },
        );
    }

    /// 写回子模式按键：Enter 提交并回读、Esc 放弃，其余走单行编辑。
    fn on_redis_value_key(&mut self, ev: &KeyDownEvent, cx: &mut Context<Self>) {
        let ks = &ev.keystroke;
        if ks.key == "escape" {
            self.redis_cancel_edit_value(cx);
            return;
        }
        if ks.key == "enter" {
            self.redis_commit_edit_value(cx);
            return;
        }
        let mut cursor = self.redis_val_cursor;
        let pasted = if (ks.modifiers.control || ks.modifiers.platform) && ks.key == "v" {
            cx.read_from_clipboard()
                .and_then(|i| i.text().map(|t| t.to_string()))
        } else {
            None
        };
        if let Some(text) = pasted {
            let at = char_ceil(&self.redis_val_buf, cursor.min(self.redis_val_buf.len()));
            self.redis_val_buf.insert_str(at, &text);
            self.redis_val_cursor = at + text.len();
            cx.notify();
        } else if edit_line(&mut self.redis_val_buf, &mut cursor, ks) {
            self.redis_val_cursor = cursor;
            cx.notify();
        }
    }

    pub(crate) fn focus_redis_pattern(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.redis_editing = true;
        self.redis_cursor = self.redis_pattern.len();
        window.focus(&self.redis_focus);
        cx.notify();
    }

    pub(crate) fn on_redis_key(&mut self, ev: &KeyDownEvent, cx: &mut Context<Self>) {
        // 面板级键分发：整值写回优先于模式输入（两者共用 redis_focus）。
        if self.redis_val_editing {
            self.on_redis_value_key(ev, cx);
            return;
        }
        if !self.redis_editing {
            return;
        }
        let ks = &ev.keystroke;
        if ks.key == "escape" {
            self.redis_editing = false;
            cx.notify();
            return;
        }
        if ks.key == "enter" {
            self.redis_editing = false;
            self.redis_scan(cx);
            return;
        }
        let mut cursor = self.redis_cursor;
        let pasted = if (ks.modifiers.control || ks.modifiers.platform) && ks.key == "v" {
            cx.read_from_clipboard()
                .and_then(|i| i.text().map(|t| t.to_string()))
        } else {
            None
        };
        if let Some(text) = pasted {
            let at = char_ceil(&self.redis_pattern, cursor.min(self.redis_pattern.len()));
            self.redis_pattern.insert_str(at, &text);
            self.redis_cursor = at + text.len();
            cx.notify();
        } else if edit_line(&mut self.redis_pattern, &mut cursor, ks) {
            self.redis_cursor = cursor;
            cx.notify();
        }
    }

    // ─── 心跳：每 30s ping 当前工作区连接（修复：实体销毁即退出循环）───

    fn beat(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id().filter(|_| self.workspace.is_some()) else {
            return;
        };
        self.io(
            cx,
            move |t| {
                Box::pin(async move {
                    let start = std::time::Instant::now();
                    let res = t.ping(id).await;
                    let ms = start.elapsed().as_secs_f64() * 1000.0;
                    (res.is_ok(), res.err().map(|e| e.to_string()), ms)
                })
            },
            |this, out, cx| {
                let (ok, err, ms) = out;
                this.hb = Some(HbState { ok, ms, err });
                cx.notify();
            },
        );
    }

    fn start_heartbeat(&mut self, cx: &mut Context<Self>) {
        cx.spawn(async move |this, cx| loop {
            cx.background_executor()
                .timer(std::time::Duration::from_secs(30))
                .await;
            if this.update(cx, |this, cx| this.beat(cx)).is_err() {
                break;
            }
        })
        .detach();
    }

    // ─── schema / 表导航 ───

    pub(crate) fn load_schema(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else {
            return;
        };
        self.tables.clear();
        self.expanded.clear();
        self.cols_cache.clear();
        self.io(
            cx,
            move |t| {
                Box::pin(async move {
                    let schemas = t.list_schemas(id).await.unwrap_or_default();
                    let schema = schemas
                        .first()
                        .map(|s| s.name.clone())
                        .unwrap_or_else(|| "main".to_string());
                    let tables = t.list_tables(id, &schema).await;
                    (schemas, schema, tables)
                })
            },
            |this, (schemas, schema, tables), cx| {
                match tables {
                    Ok(t) => {
                        this.schemas = schemas;
                        this.active_schema = schema.clone();
                        this.tables = t;
                        this.status = format!("{schema} · {} 张表", this.tables.len());
                    }
                    Err(e) => this.status = format!("加载表失败: {e}"),
                }
                cx.notify();
            },
        );
    }

    pub(crate) fn select_schema(&mut self, name: String, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else {
            return;
        };
        if name == self.active_schema {
            return;
        }
        self.active_schema = name.clone();
        self.tables.clear();
        self.expanded.clear();
        self.cols_cache.clear();
        let for_work = name.clone();
        self.io(
            cx,
            move |t| Box::pin(async move { t.list_tables(id, &for_work).await }),
            move |this, out, cx| {
                match out {
                    Ok(t) => {
                        this.tables = t;
                        this.status = format!("{} · {} 张表", name, this.tables.len());
                    }
                    Err(e) => this.status = format!("加载表失败: {e}"),
                }
                cx.notify();
            },
        );
    }

    pub(crate) fn toggle_expand(&mut self, ti: usize, cx: &mut Context<Self>) {
        if !self.expanded.remove(&ti) {
            self.expanded.insert(ti);
            self.ensure_columns(ti, cx);
        }
        cx.notify();
    }

    /// 展开表节点时懒加载列清单（首列/类型徽标；失败静默，仅提示行）。
    pub(crate) fn ensure_columns(&mut self, ti: usize, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else { return };
        if self.cols_cache.contains_key(&ti) || self.cols_loading.contains(&ti) {
            return;
        }
        let Some(t) = self.tables.get(ti) else { return };
        let (schema, table) = (t.schema.clone(), t.name.clone());
        self.cols_loading.insert(ti);
        let (schema_w, table_w) = (schema.clone(), table.clone());
        self.io(
            cx,
            move |t| Box::pin(async move { t.list_columns(id, &schema_w, &table_w).await }),
            move |this, out, cx| {
                this.cols_loading.remove(&ti);
                match out {
                    Ok(cols) => {
                        this.cols_cache.insert(ti, cols);
                    }
                    Err(e) => {
                        this.cols_cache.insert(ti, Vec::new());
                        this.status = format!("加载 {schema}.{table} 列失败: {e}");
                    }
                }
                cx.notify();
            },
        );
    }

    // ─── 对象标签页 ───

    pub(crate) fn open_table_tab(&mut self, schema: String, table: String, cx: &mut Context<Self>) {
        let key = tabs::table_key(&schema, &table);
        let existing = self
            .tabs
            .iter()
            .position(|t| tabs::table_key(&t.schema, &t.table) == key);
        let idx = match existing {
            Some(i) => i,
            None => {
                self.tabs.push(TableTabState::new(schema, table));
                self.tabs.len() - 1
            }
        };
        self.active = ActiveView::Table(idx);
        self.preview = None;
        self.ctx_menu = None;
        let fresh = self.tabs[idx].detail.is_none() && !self.tabs[idx].loading_detail;
        let no_page = self.tabs[idx].browse.grid.is_none() && !self.tabs[idx].browse.loading;
        if fresh || no_page {
            self.reload_tab(idx, cx);
        }
        cx.notify();
    }

    pub(crate) fn close_table_tab(&mut self, idx: usize, _cx: &mut Context<Self>) {
        if idx >= self.tabs.len() {
            return;
        }
        self.tabs.remove(idx);
        self.active = tabs::active_after_close(self.tabs.len() + 1, idx, self.active);
        // 浮层作用域索引修正/失效。
        if matches!(self.preview, Some(p) if p.src == GridSrc::Table(idx)) {
            self.preview = None;
        } else if let Some(p) = self.preview.as_mut() {
            if let GridSrc::Table(j) = &mut p.src {
                if *j > idx {
                    *j -= 1;
                }
            }
        }
        if matches!(self.ctx_menu, Some(m) if m.src == GridSrc::Table(idx)) {
            self.ctx_menu = None;
        } else if let Some(m) = self.ctx_menu.as_mut() {
            if let GridSrc::Table(j) = &mut m.src {
                if *j > idx {
                    *j -= 1;
                }
            }
        }
    }

    pub(crate) fn set_rail(&mut self, idx: usize, rail: Rail, cx: &mut Context<Self>) {
        let (load_prof, load_rel) = match self.tabs.get(idx) {
            Some(t) => (
                rail == Rail::Overview && t.profile.is_none() && !t.loading_profile,
                rail == Rail::Relations && t.incoming.is_none() && !t.loading_incoming,
            ),
            None => return,
        };
        if let Some(t) = self.tabs.get_mut(idx) {
            t.rail = rail;
        }
        if load_prof {
            self.load_profile(idx, cx);
        }
        if load_rel {
            self.load_incoming(idx, cx);
        }
        cx.notify();
    }

    // 预设/展示用复合 key（与 Web `connId::` 前缀思路一致，此处以 kind.schema.table 归组）。
    pub(crate) fn preset_key(tab: &TableTabState, kind: &str) -> String {
        format!("{}.{}.{}", kind, tab.schema, tab.table)
    }

    /// 由当前草稿行 + 逻辑 + 排序，构造带 conditions/order_by 的分页请求。
    /// 按连接方言过滤掉不可用的 op（behavior.md §13.6），与 Web `condToWire(c, kind)` 一致。
    fn build_request(&self, tab: &TableTabState, offset: u64) -> TableRowsRequest {
        let kind = self.sel_kind().unwrap_or(DatabaseKind::Sqlite);
        let conditions: Vec<FilterCondition> = tab
            .browse
            .filters
            .iter()
            .filter_map(|d| filters::draft_to_condition(d, kind))
            .collect();
        let logic = if conditions.is_empty() {
            None
        } else {
            Some(tab.browse.logic)
        };
        let order_by: Vec<OrderClause> = tab
            .browse
            .sort
            .as_ref()
            .map(|(c, d)| {
                vec![OrderClause {
                    column: c.clone(),
                    dir: *d,
                }]
            })
            .unwrap_or_default();
        TableRowsRequest {
            columns: Vec::new(),
            conditions,
            logic,
            order_by,
            offset,
            limit: tab.browse.limit,
        }
    }

    /// 重新拉取表 tab 的元数据 + 当前页数据。
    pub(crate) fn reload_tab(&mut self, idx: usize, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else { return };
        let Some(tab) = self.tabs.get(idx) else {
            return;
        };
        let (schema, table, req) = (
            tab.schema.clone(),
            tab.table.clone(),
            self.build_request(tab, tab.browse.offset),
        );
        let t = Arc::clone(&self.transport);
        let io = Arc::clone(&self.io);
        self.tabs[idx].loading_detail = true;
        self.tabs[idx].detail_err = None;
        self.tabs[idx].browse.loading = true;
        cx.notify();
        cx.spawn(async move |this, cx| {
            let detail = io
                .spawn({
                    let (t1, s, ta) = (Arc::clone(&t), schema.clone(), table.clone());
                    async move {
                        let columns = t1.list_columns(id, &s, &ta).await;
                        let indexes = t1.list_indexes(id, &s, &ta).await.unwrap_or_default();
                        let fks = t1.list_foreign_keys(id, &s, &ta).await.unwrap_or_default();
                        let ddl = t1.create_table_sql(id, &s, &ta).await.unwrap_or_default();
                        columns
                            .map(|c| (c, indexes, fks, ddl))
                            .map_err(|e| e.to_string())
                    }
                })
                .await;
            let page = io
                .spawn({
                    let (t2, s, ta, rq) =
                        (Arc::clone(&t), schema.clone(), table.clone(), req.clone());
                    async move { t2.browse_rows(id, &s, &ta, &rq).await }
                })
                .await;
            let _ = this.update(cx, |this, cx| {
                let Some(tab) = this.tabs.get_mut(idx) else {
                    cx.notify();
                    return;
                };
                tab.loading_detail = false;
                tab.browse.loading = false;
                match detail {
                    Ok(Ok((columns, indexes, fks, ddl))) => {
                        tab.detail = Some(TableDetail {
                            columns,
                            indexes,
                            fks,
                            ddl,
                        });
                    }
                    Ok(Err(e)) => tab.detail_err = Some(e),
                    Err(e) => tab.detail_err = Some(format!("IO 任务失败: {e}")),
                }
                match page {
                    Ok(Ok(r)) => {
                        tab.browse.has_more = r.has_more;
                        tab.browse.total = r.total_estimate;
                        tab.browse.grid = Some(GridData {
                            columns: r.columns,
                            rows: r.rows,
                            elapsed_ms: r.execution_time_ms,
                        });
                    }
                    Ok(Err(e)) => this.status = format!("浏览失败: {e}"),
                    Err(e) => this.status = format!("IO 任务失败: {e}"),
                }
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn load_page(&mut self, idx: usize, offset: u64, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else { return };
        let Some(tab) = self.tabs.get(idx) else {
            return;
        };
        let (schema, table, req) = (
            tab.schema.clone(),
            tab.table.clone(),
            self.build_request(tab, offset),
        );
        tab_set(self, idx, |t| {
            t.browse.loading = true;
            t.browse.exact = None; // 条件/翻页使既有精确计数失效
        });
        if matches!(self.preview, Some(p) if p.src == GridSrc::Table(idx)) {
            self.preview = None;
        }
        self.ctx_menu = None;
        self.editing = None;
        cx.notify();
        self.io(
            cx,
            move |t| Box::pin(async move { t.browse_rows(id, &schema, &table, &req).await }),
            move |this, out, cx| {
                let Some(tab) = this.tabs.get_mut(idx) else {
                    return;
                };
                tab.browse.loading = false;
                match out {
                    Ok(r) => {
                        tab.browse.offset = r.offset;
                        tab.browse.has_more = r.has_more;
                        tab.browse.total = r.total_estimate;
                        tab.browse.grid = Some(GridData {
                            columns: r.columns,
                            rows: r.rows,
                            elapsed_ms: r.execution_time_ms,
                        });
                    }
                    Err(e) => this.status = format!("浏览失败: {e}"),
                }
                cx.notify();
            },
        );
    }

    pub(crate) fn set_page_size(&mut self, idx: usize, limit: u32, cx: &mut Context<Self>) {
        if let Some(tab) = self.tabs.get_mut(idx) {
            tab.browse.limit = limit;
            tab.browse.offset = 0;
        }
        self.load_page(idx, 0, cx);
    }

    // ─── G2：排序 / 精确计数 / 过滤栏 / 预设 ───

    /// 表头点击循环排序：无→升→降→无（单列，对齐 Web sortKey）。
    pub(crate) fn toggle_sort(&mut self, idx: usize, column: String, cx: &mut Context<Self>) {
        let cur = self.tabs.get(idx).and_then(|t| t.browse.sort.clone());
        let next = match cur {
            Some((c, SortDirection::Asc)) if c == column => Some((column, SortDirection::Desc)),
            Some((c, SortDirection::Desc)) if c == column => None,
            _ => Some((column, SortDirection::Asc)),
        };
        if let Some(t) = self.tabs.get_mut(idx) {
            t.browse.sort = next;
        }
        self.load_page(idx, 0, cx);
    }

    /// 精确计数：以当前过滤条件走服务端 COUNT（limit=0）。
    pub(crate) fn count_rows(&mut self, idx: usize, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else { return };
        let Some(tab) = self.tabs.get(idx) else {
            return;
        };
        let (schema, table) = (tab.schema.clone(), tab.table.clone());
        let req = self.build_request(tab, 0);
        tab_set(self, idx, |t| t.browse.counting = true);
        cx.notify();
        self.io(
            cx,
            move |t| Box::pin(async move { t.browse_rows_count(id, &schema, &table, &req).await }),
            move |this, out, cx| {
                if let Some(t) = this.tabs.get_mut(idx) {
                    t.browse.counting = false;
                    match out {
                        Ok(n) => t.browse.exact = Some(n),
                        Err(e) => this.status = format!("计数失败: {e}"),
                    }
                }
                cx.notify();
            },
        );
    }

    fn column_names(&self, idx: usize) -> Vec<String> {
        let Some(tab) = self.tabs.get(idx) else {
            return Vec::new();
        };
        if let Some(d) = &tab.detail {
            d.columns.iter().map(|c| c.name.clone()).collect()
        } else if let Some(g) = &tab.browse.grid {
            g.columns.iter().map(|c| c.name.clone()).collect()
        } else {
            Vec::new()
        }
    }

    // ─── G3：变更跟踪编辑 ───

    /// 主键列名（编辑/提交按此定位行；无主键则整表不可编辑）。
    fn pk_column_names(&self, idx: usize) -> Vec<String> {
        self.tabs
            .get(idx)
            .and_then(|t| t.detail.as_ref())
            .map(|d| {
                d.columns
                    .iter()
                    .filter(|c| c.is_primary_key)
                    .map(|c| c.name.clone())
                    .collect()
            })
            .unwrap_or_default()
    }

    fn grid_col_name(&self, idx: usize, col: usize) -> Option<String> {
        self.tabs
            .get(idx)
            .and_then(|t| t.browse.grid.as_ref())?
            .columns
            .get(col)
            .map(|c| c.name.clone())
    }

    /// 数据库原始整行快照（列名 → 值），作为 update/delete 的 WHERE 依据。
    fn row_snapshot(&self, idx: usize, row: usize) -> Option<Row> {
        let g = self.tabs.get(idx)?.browse.grid.as_ref()?;
        let vals = g.rows.get(row)?;
        Some(
            g.columns
                .iter()
                .zip(vals.iter())
                .map(|(c, v)| (c.name.clone(), v.clone()))
                .collect(),
        )
    }

    /// 行稳定键：主键值按序拼接；无主键列则 None（不可编辑）。
    pub(crate) fn browse_row_key(&self, idx: usize, row: usize) -> Option<String> {
        let pk = self.pk_column_names(idx);
        if pk.is_empty() {
            return None;
        }
        let snap = self.row_snapshot(idx, row)?;
        Some(
            pk.iter()
                .map(|p| value_to_text(&changes::row_get(&snap, p)))
                .collect::<Vec<_>>()
                .join("\u{1}"),
        )
    }

    /// 该表当前是否可内联编辑（非只读 + 有主键 + 有数据）。
    pub(crate) fn browse_editable(&self, idx: usize) -> bool {
        !self.is_read_only()
            && !self.pk_column_names(idx).is_empty()
            && self
                .tabs
                .get(idx)
                .map(|t| t.browse.grid.is_some())
                .unwrap_or(false)
    }

    /// 待提交语句（Review 面板 + 提交共用）。
    pub(crate) fn pending_statements(&self, idx: usize) -> Vec<changes::GeneratedStatement> {
        let kind = match self.sel_kind() {
            Some(k) => k,
            None => return Vec::new(),
        };
        let tab = match self.tabs.get(idx) {
            Some(t) => t,
            None => return Vec::new(),
        };
        let pk = self.pk_column_names(idx);
        if pk.is_empty() {
            return Vec::new();
        }
        changes::build_statements(&tab.changes.all(), &tab.schema, &tab.table, &pk, kind)
    }

    /// 单元格当前应显示的值：有待提交改动则取改动值，否则网格原值。
    pub(crate) fn browse_cell_display(&self, idx: usize, row: usize, col: usize) -> Option<Value> {
        let name = self.grid_col_name(idx, col)?;
        if let Some(key) = self.browse_row_key(idx, row) {
            if let Some(ch) = self.tabs.get(idx).and_then(|t| t.changes.get(&key)) {
                if let Some(c) = ch.cell_changes.iter().find(|c| c.column == name) {
                    return Some(c.new_value.clone());
                }
            }
        }
        self.tabs
            .get(idx)?
            .browse
            .grid
            .as_ref()?
            .rows
            .get(row)?
            .get(col)
            .cloned()
    }

    /// 该格是否有未提交改动（脏格高亮）。
    pub(crate) fn browse_cell_dirty(&self, idx: usize, row: usize, col: usize) -> bool {
        let Some(name) = self.grid_col_name(idx, col) else {
            return false;
        };
        let Some(key) = self.browse_row_key(idx, row) else {
            return false;
        };
        self.tabs
            .get(idx)
            .and_then(|t| t.changes.get(&key))
            .map(|c| c.cell_changes.iter().any(|cc| cc.column == name))
            .unwrap_or(false)
    }

    pub(crate) fn change_count(&self, idx: usize) -> usize {
        self.tabs.get(idx).map(|t| t.changes.size()).unwrap_or(0)
    }

    /// 网格单元格显示文本（待提交改动覆盖原值）。
    pub(crate) fn browse_cell_text(&self, idx: usize, row: usize, col: usize) -> String {
        match self.browse_cell_display(idx, row, col) {
            Some(v) => value_to_text(&v),
            None => String::new(),
        }
    }

    pub(crate) fn begin_cell_edit(
        &mut self,
        idx: usize,
        row: usize,
        col: usize,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if !self.browse_editable(idx) {
            return;
        }
        if self.cell_edit == Some((idx, row, col)) {
            return;
        }
        let val = self
            .browse_cell_display(idx, row, col)
            .unwrap_or(Value::Null);
        let buf = value_to_text(&val);
        self.cell_cursor = buf.len();
        self.cell_buf = buf;
        self.cell_edit = Some((idx, row, col));
        window.focus(&self.cell_focus);
        cx.notify();
    }

    fn commit_cell_edit(&mut self, cx: &mut Context<Self>) {
        let Some((idx, row, col)) = self.cell_edit else {
            return;
        };
        let name = match self.grid_col_name(idx, col) {
            Some(n) => n,
            None => {
                self.cell_edit = None;
                cx.notify();
                return;
            }
        };
        let key = match self.browse_row_key(idx, row) {
            Some(k) => k,
            None => {
                self.cell_edit = None;
                cx.notify();
                return;
            }
        };
        let snapshot = self.row_snapshot(idx, row).unwrap_or_default();
        let old_value = self
            .browse_cell_display(idx, row, col)
            .unwrap_or(Value::Null);
        let new_value = parse_edit_value(&self.cell_buf);
        if let Some(tab) = self.tabs.get_mut(idx) {
            tab.changes
                .add_cell_change(&key, &name, old_value, new_value, snapshot);
        }
        self.cell_edit = None;
        cx.notify();
    }

    pub(crate) fn cancel_cell_edit(&mut self, cx: &mut Context<Self>) {
        self.cell_edit = None;
        cx.notify();
    }

    pub(crate) fn undo_last_change(&mut self, idx: usize, cx: &mut Context<Self>) {
        if let Some(tab) = self.tabs.get_mut(idx) {
            tab.changes.undo();
        }
        cx.notify();
    }

    pub(crate) fn toggle_review(&mut self, cx: &mut Context<Self>) {
        self.review_open = !self.review_open;
        cx.notify();
    }

    /// 单事务提交全部改动：逐条校验影响行数，任一失败整体回滚且队列保留。
    pub(crate) fn commit_changes(&mut self, idx: usize, cx: &mut Context<Self>) {
        if self.is_read_only() {
            self.status = "只读连接不可写入".to_string();
            cx.notify();
            return;
        }
        if self.committing {
            return;
        }
        let Some(id) = self.sel_id() else {
            return;
        };
        if self.sel_kind().is_none() {
            return;
        }
        let pk = self.pk_column_names(idx);
        if pk.is_empty() {
            self.status = "无主键，无法定位行".to_string();
            cx.notify();
            return;
        }
        let stmts = self.pending_statements(idx);
        if stmts.is_empty() {
            return;
        }
        self.cell_edit = None;
        self.committing = true;
        self.status = format!("提交 {} 条改动…", stmts.len());
        cx.notify();
        self.io(
            cx,
            move |t| {
                Box::pin(async move {
                    let tx = match t
                        .begin_transaction(&BeginTransactionRequest {
                            connection_id: id,
                            isolation_level: None,
                        })
                        .await
                    {
                        Ok(i) => i,
                        Err(e) => {
                            return CommitResult {
                                ok: false,
                                applied: 0,
                                error: Some(format!("开启事务失败: {e}")),
                            }
                        }
                    };
                    let txn = tx.id.to_string();
                    let mut applied: u64 = 0;
                    for s in &stmts {
                        match t
                            .execute_in_transaction(&txn, &s.param_sql, &s.params)
                            .await
                        {
                            Ok(r) => {
                                if changes::rows_mismatch(s, r.affected_rows) {
                                    let _ = t.rollback_transaction(&txn).await;
                                    return CommitResult {
                                        ok: false,
                                        applied,
                                        error: Some(format!(
                                            "影响行数与预期不符（第 {} 条），已整体回滚",
                                            applied + 1
                                        )),
                                    };
                                }
                                applied += 1;
                            }
                            Err(e) => {
                                let _ = t.rollback_transaction(&txn).await;
                                return CommitResult {
                                    ok: false,
                                    applied,
                                    error: Some(e.to_string()),
                                };
                            }
                        }
                    }
                    match t.commit_transaction(&txn).await {
                        Ok(_) => CommitResult {
                            ok: true,
                            applied,
                            error: None,
                        },
                        Err(e) => CommitResult {
                            ok: false,
                            applied,
                            error: Some(format!("提交失败: {e}")),
                        },
                    }
                })
            },
            move |this, out, cx| {
                this.committing = false;
                if out.ok {
                    if let Some(tab) = this.tabs.get_mut(idx) {
                        tab.changes.clear();
                    }
                    this.review_open = false;
                    this.status = format!("已提交 {} 条改动", out.applied);
                    this.reload_tab(idx, cx);
                } else {
                    this.status = out.error.unwrap_or_else(|| "提交失败".to_string());
                    cx.notify();
                }
                cx.notify();
            },
        );
    }

    pub(crate) fn add_filter(&mut self, idx: usize, cx: &mut Context<Self>) {
        let col = self
            .column_names(idx)
            .into_iter()
            .next()
            .unwrap_or_default();
        if let Some(t) = self.tabs.get_mut(idx) {
            t.browse.filters.push(FilterDraft::with_column(col));
        }
        cx.notify();
    }

    pub(crate) fn remove_filter(&mut self, idx: usize, fi: usize, cx: &mut Context<Self>) {
        let Some(t) = self.tabs.get_mut(idx) else {
            return;
        };
        if fi < t.browse.filters.len() {
            t.browse.filters.remove(fi);
        }
        if matches!(self.editing, Some((e, _)) if e == fi) {
            self.editing = None;
        } else if let Some((e, _)) = self.editing {
            if e > fi {
                self.editing = Some((e - 1, self.editing.map(|(_, f)| f).unwrap_or(0)));
            }
        }
        cx.notify();
    }

    pub(crate) fn cycle_filter_column(&mut self, idx: usize, fi: usize, cx: &mut Context<Self>) {
        let cols = self.column_names(idx);
        if cols.is_empty() {
            return;
        }
        if let Some(t) = self.tabs.get_mut(idx) {
            if let Some(d) = t.browse.filters.get_mut(fi) {
                let pos = cols
                    .iter()
                    .position(|c| *c == d.column)
                    .unwrap_or(cols.len());
                d.column = cols[(pos + 1) % cols.len()].clone();
            }
        }
        cx.notify();
    }

    pub(crate) fn cycle_filter_op(&mut self, idx: usize, fi: usize, cx: &mut Context<Self>) {
        let kind = self.sel_kind().unwrap_or(DatabaseKind::Sqlite);
        if let Some(t) = self.tabs.get_mut(idx) {
            if let Some(d) = t.browse.filters.get_mut(fi) {
                d.op = filters::next_op(kind, d.op);
            }
        }
        cx.notify();
    }

    /// 循环大小写三态（默认→区分→忽略）；仅文本匹配类 op 的草稿行会渲染该控件。
    pub(crate) fn cycle_filter_case(&mut self, idx: usize, fi: usize, cx: &mut Context<Self>) {
        if let Some(t) = self.tabs.get_mut(idx) {
            if let Some(d) = t.browse.filters.get_mut(fi) {
                d.case_mode = match d.case_mode {
                    filters::CaseMode::Default => filters::CaseMode::Sensitive,
                    filters::CaseMode::Sensitive => filters::CaseMode::Insensitive,
                    filters::CaseMode::Insensitive => filters::CaseMode::Default,
                };
            }
        }
        cx.notify();
    }

    pub(crate) fn toggle_logic(&mut self, idx: usize, cx: &mut Context<Self>) {
        if let Some(t) = self.tabs.get_mut(idx) {
            t.browse.logic = if t.browse.logic == FilterLogic::And {
                FilterLogic::Or
            } else {
                FilterLogic::And
            };
        }
        cx.notify();
    }

    /// 应用过滤/排序：回到首页重拉。
    pub(crate) fn apply_filters(&mut self, idx: usize, cx: &mut Context<Self>) {
        self.editing = None;
        self.load_page(idx, 0, cx);
    }

    pub(crate) fn clear_filters(&mut self, idx: usize, cx: &mut Context<Self>) {
        if let Some(t) = self.tabs.get_mut(idx) {
            t.browse.filters.clear();
            t.browse.logic = FilterLogic::And;
            t.browse.sort = None;
            t.browse.exact = None;
        }
        self.editing = None;
        self.load_page(idx, 0, cx);
    }

    pub(crate) fn save_filter_preset(&mut self, idx: usize, cx: &mut Context<Self>) {
        let Some(tab) = self.tabs.get(idx) else {
            return;
        };
        let key = Self::preset_key(tab, &self.kind_str());
        let existing = presets::load_presets(&key);
        let name = presets::next_name(&existing);
        let and = tab.browse.logic == FilterLogic::And;
        let preset = FilterPreset::from_drafts(&name, &tab.browse.filters, and);
        match presets::save_preset(&key, preset) {
            Ok(()) => self.status = format!("已保存过滤预设 {name}（{key}）"),
            Err(e) => self.status = format!("保存预设失败: {e}"),
        }
        cx.notify();
    }

    pub(crate) fn load_last_preset(&mut self, idx: usize, cx: &mut Context<Self>) {
        let Some(tab) = self.tabs.get(idx) else {
            return;
        };
        let key = Self::preset_key(tab, &self.kind_str());
        let all = presets::load_presets(&key);
        let Some(p) = all.last() else {
            self.status = format!("无已存预设（{key}）");
            cx.notify();
            return;
        };
        let drafts = p.to_drafts();
        let and = p.logic_is_and();
        let n = drafts.len();
        if let Some(t) = self.tabs.get_mut(idx) {
            t.browse.filters = drafts;
            t.browse.logic = if and {
                FilterLogic::And
            } else {
                FilterLogic::Or
            };
        }
        self.status = format!("已载入预设 {}（{n} 条件）", p.name);
        self.load_page(idx, 0, cx);
    }

    /// 聚焦某草稿的值字段编辑（field：0=值 1=第二值 2=IN 列表）；光标移到该串末尾。
    pub(crate) fn begin_edit(&mut self, idx: usize, fi: usize, field: u8) {
        let len = self
            .tabs
            .get(idx)
            .and_then(|t| t.browse.filters.get(fi))
            .map(|d| match field {
                0 => d.value.len(),
                1 => d.second.len(),
                _ => d.values.len(),
            })
            .unwrap_or(0);
        self.editing = Some((fi, field));
        self.edit_cursor = len;
    }

    fn active_table_idx(&self) -> Option<usize> {
        match self.active {
            ActiveView::Table(i) if i < self.tabs.len() => Some(i),
            _ => None,
        }
    }

    // 单行值输入：仅作用于当前编辑目标；Enter=应用过滤，Esc=失焦。
    pub(crate) fn on_filter_key(&mut self, ev: &KeyDownEvent, cx: &mut Context<Self>) {
        let Some((fi, field)) = self.editing else {
            return;
        };
        let Some(ti) = self.active_table_idx() else {
            return;
        };
        let ks = &ev.keystroke;
        if ks.key == "escape" {
            self.editing = None;
            cx.notify();
            return;
        }
        if ks.key == "enter" {
            self.apply_filters(ti, cx);
            return;
        }
        let mut cursor = self.edit_cursor;
        // 粘贴（Ctrl/Cmd+V）与常规编辑；命中任一即写回目标串并同步光标。
        let pasted = if (ks.modifiers.control || ks.modifiers.platform) && ks.key == "v" {
            cx.read_from_clipboard()
                .and_then(|i| i.text().map(|t| t.to_string()))
        } else {
            None
        };
        let mut handled = true;
        if let Some(t) = self.tabs.get_mut(ti) {
            if let Some(d) = t.browse.filters.get_mut(fi) {
                let s = match field {
                    0 => &mut d.value,
                    1 => &mut d.second,
                    _ => &mut d.values,
                };
                if let Some(text) = pasted {
                    let at = char_ceil(s, cursor.min(s.len()));
                    s.insert_str(at, &text);
                    cursor = at + text.len();
                } else if !edit_line(s, &mut cursor, ks) {
                    handled = false;
                }
            } else {
                handled = false;
            }
        } else {
            handled = false;
        }
        if handled {
            self.edit_cursor = cursor;
            cx.notify();
        }
    }

    /// 单元格内联编辑：单行输入，Enter=入队提交该格，Esc=取消，Ctrl/Cmd+V=粘贴。
    pub(crate) fn on_cell_key(&mut self, ev: &KeyDownEvent, cx: &mut Context<Self>) {
        if self.cell_edit.is_none() {
            return;
        }
        let ks = &ev.keystroke;
        if ks.key == "escape" {
            self.cancel_cell_edit(cx);
            return;
        }
        if ks.key == "enter" {
            self.commit_cell_edit(cx);
            return;
        }
        let mut cursor = self.cell_cursor;
        let pasted = if (ks.modifiers.control || ks.modifiers.platform) && ks.key == "v" {
            cx.read_from_clipboard()
                .and_then(|i| i.text().map(|t| t.to_string()))
        } else {
            None
        };
        if let Some(text) = pasted {
            let at = char_ceil(&self.cell_buf, cursor.min(self.cell_buf.len()));
            self.cell_buf.insert_str(at, &text);
            self.cell_cursor = at + text.len();
            cx.notify();
        } else if edit_line(&mut self.cell_buf, &mut cursor, ks) {
            self.cell_cursor = cursor;
            cx.notify();
        }
    }

    /// 概览 rail：单条聚合 SQL 列画像（语义对齐 Web tableProfile，截前 24 列）。
    pub(crate) fn load_profile(&mut self, idx: usize, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else { return };
        let kind = self.kind_str();
        let Some(tab) = self.tabs.get(idx) else {
            return;
        };
        let Some(detail) = &tab.detail else { return };
        let cols: Vec<ProfileColumn> = detail
            .columns
            .iter()
            .take(profile::MAX_PROFILE_COLS)
            .map(|c| ProfileColumn {
                name: c.name.clone(),
                data_type: c.data_type.clone(),
            })
            .collect();
        let expr = format!(
            "{}.{}",
            profile::quote_ident(&kind, &tab.schema),
            profile::quote_ident(&kind, &tab.table)
        );
        let plan = profile::build_profile_plan(&kind, &expr, &cols);
        tab_set(self, idx, |t| t.loading_profile = true);
        cx.notify();
        let sql_for_work = plan.sql.clone();
        self.io(
            cx,
            move |t| Box::pin(async move { t.execute(id, &sql_for_work, &[]).await }),
            move |this, out, cx| {
                let Some(tab) = this.tabs.get_mut(idx) else {
                    return;
                };
                tab.loading_profile = false;
                match out {
                    Ok(r) => {
                        let row = r.rows.first().map(|v| v.as_slice());
                        tab.profile = Some(profile::parse_profile(row, &plan));
                    }
                    Err(e) => this.status = format!("画像失败: {e}"),
                }
                cx.notify();
            },
        );
    }

    /// 关系 rail：入向 FK 扫描（遍历当前 schema 全部表，上限 200，逐表 list_foreign_keys）。
    pub(crate) fn load_incoming(&mut self, idx: usize, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else { return };
        let Some(tab) = self.tabs.get(idx) else {
            return;
        };
        let (schema, table) = (tab.schema.clone(), tab.table.clone());
        let scan: Vec<(String, String)> = self
            .tables
            .iter()
            .take(200)
            .map(|t| (t.schema.clone(), t.name.clone()))
            .collect();
        tab_set(self, idx, |t| t.loading_incoming = true);
        cx.notify();
        self.io(
            cx,
            move |t| {
                Box::pin(async move {
                    let mut out: Vec<(String, String, ForeignKeyInfo)> = Vec::new();
                    for (s, tb) in scan {
                        if s == schema && tb == table {
                            continue;
                        }
                        if let Ok(fks) = t.list_foreign_keys(id, &s, &tb).await {
                            for fk in fks {
                                if fk.referenced_table == table && fk.referenced_schema == schema {
                                    out.push((s.clone(), tb.clone(), fk));
                                }
                            }
                        }
                    }
                    out
                })
            },
            move |this, out, cx| {
                if let Some(tab) = this.tabs.get_mut(idx) {
                    tab.loading_incoming = false;
                    tab.incoming = Some(
                        out.into_iter()
                            .map(|(s, tb, fk)| IncomingFk {
                                schema: s,
                                table: tb,
                                fk,
                            })
                            .collect(),
                    );
                }
                cx.notify();
            },
        );
    }

    // ─── 工作台查询执行（多语句 M27 语义）───

    pub(crate) fn run_query(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else {
            self.status = "请先选择连接".into();
            cx.notify();
            return;
        };
        let sql = self.sql.trim().to_string();
        if sql.is_empty() {
            self.status = "SQL 为空".into();
            cx.notify();
            return;
        }
        let stmts = split_statements(&sql);
        self.active = ActiveView::Workbench;
        self.busy = true;
        self.result = None;
        self.preview = None;
        self.ctx_menu = None;
        // 计划是旧快照：重新执行查询即失效，避免浮层展示过期内容。
        self.explain = None;
        self.cancel.store(false, Ordering::Relaxed);
        if stmts.len() > 1 {
            self.multi_log = stmts
                .iter()
                .map(|s| (s.clone(), StmtOutcome::Skipped))
                .collect();
        } else {
            self.multi_log.clear();
        }
        cx.notify();

        let t = Arc::clone(&self.transport);
        let io = Arc::clone(&self.io);
        let cancel = Arc::clone(&self.cancel);
        cx.spawn(async move |this, cx| {
            // 多语句（M27 语义）：顺序执行、错误不阻断后续、取消为尽力而为
            // （当前语句跑完，剩余语句标 ⊘）。单语句走原路径。
            let mut last: Option<QueryResult> = None;
            let mut last_err: Option<String> = None;
            let mut ok = 0usize;
            let mut err = 0usize;
            let mut skip = 0usize;
            for (i, s) in stmts.iter().enumerate() {
                let outcome = if cancel.load(Ordering::Relaxed) {
                    skip += 1;
                    StmtOutcome::Skipped
                } else {
                    let t2 = Arc::clone(&t);
                    let id2 = id;
                    let s2 = s.clone();
                    let join = io.spawn(async move { t2.execute(id2, &s2, &[]).await });
                    match join.await {
                        Ok(Ok(r)) => {
                            ok += 1;
                            let rows = r.rows.len();
                            last = Some(r);
                            StmtOutcome::Ok(rows)
                        }
                        Ok(Err(e)) => {
                            err += 1;
                            last_err = Some(e.to_string());
                            StmtOutcome::Err(e.to_string())
                        }
                        Err(e) => {
                            err += 1;
                            last_err = Some(format!("IO 任务失败: {e}"));
                            StmtOutcome::Err(format!("IO 任务失败: {e}"))
                        }
                    }
                };
                if stmts.len() > 1 {
                    let _ = this.update(cx, |this, cx| {
                        if let Some(entry) = this.multi_log.get_mut(i) {
                            entry.1 = outcome;
                        }
                        cx.notify();
                    });
                }
            }
            let _ = this.update(cx, move |this, cx| {
                this.busy = false;
                if stmts.len() > 1 {
                    let cancelled = cancel.load(Ordering::Relaxed);
                    this.status = format!(
                        "多语句 {}/{}：✓ {ok} · ✗ {err} · ⊘ {skip}{}",
                        ok + err + skip,
                        stmts.len(),
                        if cancelled { "（已取消）" } else { "" }
                    );
                } else if let Some(r) = last.as_ref() {
                    this.status = format!(
                        "执行完成：{} 行，耗时 {:.1}ms",
                        r.rows.len(),
                        r.execution_time_ms
                    );
                } else {
                    this.status = format!("查询失败: {}", last_err.unwrap_or_default());
                }
                this.result = last;
                cx.notify();
            });
        })
        .detach();
    }

    // ─── D3 执行计划（语义对齐 Web lib/explain.ts / TUI pkg/explain）───

    /// 取编辑器第一条语句，按方言包成 EXPLAIN 执行，结果渲染为计划树/表格文本。
    pub(crate) fn run_explain(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else {
            self.status = "请先选择连接".into();
            cx.notify();
            return;
        };
        if self.is_redis() {
            self.status = "Redis 无执行计划".into();
            cx.notify();
            return;
        }
        let Some(kind) = self.sel_kind() else { return };
        let sql = self.sql.trim().to_string();
        let esql = split_statements(&sql)
            .into_iter()
            .next()
            .map(|s| crate::explain::build_sql(&s, kind))
            .unwrap_or_default();
        if esql.is_empty() {
            self.status = "SQL 为空".into();
            cx.notify();
            return;
        }
        self.active = ActiveView::Workbench;
        self.busy = true;
        self.preview = None;
        self.ctx_menu = None;
        cx.notify();
        let shown = esql.clone();
        self.io(
            cx,
            move |t| Box::pin(async move { t.execute(id, &esql, &[]).await }),
            move |this, out, cx| {
                this.busy = false;
                match out {
                    Ok(r) => {
                        let lines = crate::explain::render(&r, kind).lines();
                        this.status =
                            format!("执行计划：{} 行（F9 / Ctrl+Shift+E 重跑）", lines.len());
                        this.explain = Some(ExplainState { sql: shown, lines });
                    }
                    Err(e) => this.status = format!("执行计划失败: {e}"),
                }
                cx.notify();
            },
        );
    }

    pub(crate) fn request_cancel(&mut self, cx: &mut Context<Self>) {
        self.cancel.store(true, Ordering::Relaxed);
        self.status = "取消中：当前语句将执行完，剩余跳过".into();
        cx.notify();
    }

    // ─── E5.5 结构比对与同步向导（浮层四步，语义对齐 Web 向导与 TUI 的 viewCompare）───
    // 判定与语句生成全在服务端；本段只做「选两侧 → 勾条目 → 取脚本 → 按能力位执行」的编排。
    //
    // E6.3 抽层后的分工：状态怎么变（游标、勾选集、确认缓冲）、键在哪一步干什么、
    // 文案长什么样 ⇒ 全在 `cmpsync.rs`（纯层，无 gpui，可按键驱动测试）；
    // 这里只剩三件事——把连接列表喂给纯层、发 IO、把纯层返回的文案写进 `status`。

    /// ① 基准侧候选：所有可参与比对的连接（Redis 无模式概念，服务端拒）。
    pub(crate) fn cmp_source_list(&self) -> Vec<ConnectionInfo> {
        self.conns
            .iter()
            .filter(|c| cmpsync::is_comparable_kind(c.kind))
            .cloned()
            .collect()
    }

    /// ② 期望侧候选：与基准侧同种类；**含基准侧自己**（比对必为空），由呈现层标注，
    /// 不在这里偷偷过滤——三端口径要一致。
    pub(crate) fn cmp_target_list(&self) -> Vec<ConnectionInfo> {
        let src = self.compare.as_ref().and_then(|c| self.cmp_conn(c.src));
        match src {
            Some(s) => cmpsync::candidate_connections(&self.conns, &s),
            None => Vec::new(),
        }
    }

    pub(crate) fn cmp_conn(&self, id: ConnectionId) -> Option<ConnectionInfo> {
        self.conns.iter().find(|c| c.id == id).cloned()
    }

    /// 向导里两个连接名（任一侧未选时给占位，渲染层不判空）。
    pub(crate) fn cmp_names(&self) -> (String, String) {
        let Some(c) = self.compare.as_ref() else {
            return ("—".into(), "—".into());
        };
        let src = self
            .cmp_conn(c.src)
            .map(|x| x.name)
            .unwrap_or_else(|| "（已删除）".into());
        let tgt = c
            .tgt
            .and_then(|id| self.cmp_conn(id))
            .map(|x| x.name)
            .unwrap_or_else(|| "（未选）".into());
        (src, tgt)
    }

    /// 打开向导：三道守卫与 TUI 的 `openCompare` 同口径（连接缺失 / Redis / 基准侧只读）。
    /// 基准侧默认取当前活动连接，但①步允许改选别的同类连接。
    pub(crate) fn open_compare(&mut self, cx: &mut Context<Self>) {
        if self.compare.is_some() {
            self.compare = None;
            self.status = "已关闭结构同步向导".into();
            cx.notify();
            return;
        }
        let Some(base) = self.workspace.and_then(|i| self.conns.get(i)).cloned() else {
            self.status = "请先在左侧选择一个连接".into();
            cx.notify();
            return;
        };
        if !cmpsync::is_comparable_kind(base.kind) {
            self.status = format!("{} 无模式概念，不参与结构比对", base.kind);
            cx.notify();
            return;
        }
        if conn_is_read_only(&base) {
            self.status = format!("基准侧「{}」是只读连接，不可执行同步", base.name);
            cx.notify();
            return;
        }
        let cur = self
            .cmp_source_list()
            .iter()
            .position(|c| c.id == base.id)
            .unwrap_or(0);
        self.compare = Some(CompareState::new(base.id, cur));
        self.status = "结构同步向导 ① 选基准侧".into();
        cx.notify();
    }

    pub(crate) fn close_compare(&mut self, cx: &mut Context<Self>) {
        self.compare = None;
        cx.notify();
    }

    /// 列表高亮移动（四步共用：① 基准侧 ② 期望侧 ③ 条目 ④ 语句）。
    /// 总数只有这里知道——①② 步来自连接列表，③④ 步来自向导态自身。
    pub(crate) fn cmp_move(&mut self, delta: isize, cx: &mut Context<Self>) {
        let total = match self.compare.as_ref() {
            Some(c) => match c.step {
                CmpStep::Source => self.cmp_source_list().len(),
                CmpStep::Target => self.cmp_target_list().len(),
                CmpStep::Review => c.entries.len(),
                CmpStep::Apply => c
                    .script
                    .as_ref()
                    .map(|s| s.statements.len())
                    .unwrap_or_default(),
            },
            None => return,
        };
        if let Some(c) = self.compare.as_mut() {
            c.shift(delta, total);
        }
        cx.notify();
    }

    /// 点击行 = 先把高亮挪过去，再走该步的 Enter 语义（键盘与鼠标共用一套状态）。
    pub(crate) fn cmp_set_cur(&mut self, ix: usize, cx: &mut Context<Self>) {
        if let Some(c) = self.compare.as_mut() {
            c.set_cur(ix);
        }
        cx.notify();
    }

    /// ① Enter：确认基准侧 → ②。基准侧是执行侧，所以它不能是只读连接。
    pub(crate) fn cmp_pick_source(&mut self, cx: &mut Context<Self>) {
        let cur = match self.compare.as_ref() {
            Some(c) => c.src_cur,
            None => return,
        };
        let list = self.cmp_source_list();
        let Some(chosen) = list.get(cur.min(list.len().saturating_sub(1))).cloned() else {
            self.status = "没有可参与比对的连接".into();
            cx.notify();
            return;
        };
        if conn_is_read_only(&chosen) {
            self.status = format!("基准侧「{}」是只读连接，不可执行同步", chosen.name);
            cx.notify();
            return;
        }
        let status = self
            .compare
            .as_mut()
            .map(|c| c.goto_target(chosen.id, &chosen.name));
        if let Some(status) = status {
            self.status = status;
        }
        cx.notify();
    }

    /// ② Enter：确认期望侧并比对 → ③；③ 步的 `r`/`i`/`n`、④ 步的 `c` 也走这里。
    pub(crate) fn cmp_run_diff(&mut self, cx: &mut Context<Self>) {
        let (loading, committed, src, opts, cur) = {
            let c = match self.compare.as_ref() {
                Some(c) => c,
                None => return,
            };
            (
                c.loading,
                c.tgt,
                c.src,
                SchemaDiffOptions {
                    ignore_defaults: Some(c.ignore_defaults),
                    select_destructive: Some(c.select_destructive),
                },
                c.tgt_cur,
            )
        };
        if loading {
            return;
        }
        // ② 步第一次取高亮项并写回；③④ 步的重比对复用已确认的期望侧（此刻高亮可能已挪走）。
        let tgt = match committed {
            Some(id) => id,
            None => {
                let list = self.cmp_target_list();
                let Some(chosen) = list.get(cur.min(list.len().saturating_sub(1))).cloned() else {
                    self.status = "没有同种类的候选连接（Redis 与跨种类服务端一律拒绝）".into();
                    cx.notify();
                    return;
                };
                if let Some(c) = self.compare.as_mut() {
                    c.tgt = Some(chosen.id);
                }
                chosen.id
            }
        };
        let req = SchemaDiffRequest {
            source: SchemaDiffTarget {
                connection_id: src,
                schema: None,
            },
            target: SchemaDiffTarget {
                connection_id: tgt,
                schema: None,
            },
            options: Some(opts),
        };
        if let Some(c) = self.compare.as_mut() {
            c.begin_io();
        }
        self.status = "比对中…".into();
        cx.notify();
        self.io(
            cx,
            move |t| Box::pin(async move { t.schema_diff(&req).await }),
            move |this, out, cx| {
                let status = match out {
                    Ok(plan) => this.compare.as_mut().map(|c| c.finish_diff(plan.entries)),
                    Err(e) => {
                        if let Some(c) = this.compare.as_mut() {
                            c.fail_io();
                        }
                        Some(format!("比对失败: {e}"))
                    }
                };
                if let Some(status) = status {
                    this.status = status;
                }
                cx.notify();
            },
        );
    }

    /// Space / 点击复选框：勾选集与缺口由纯层重算。
    pub(crate) fn cmp_toggle_entry(&mut self, ix: usize, cx: &mut Context<Self>) {
        let status = self.compare.as_mut().and_then(|c| c.toggle_at(ix));
        if let Some(status) = status {
            self.status = status;
        }
        cx.notify();
    }

    /// `a` 只勾非破坏 / `A` 全选。
    pub(crate) fn cmp_select_all(&mut self, only_safe: bool, cx: &mut Context<Self>) {
        let status = self.compare.as_mut().map(|c| c.select_all(only_safe));
        if let Some(status) = status {
            self.status = status;
        }
        cx.notify();
    }

    /// `f` 补齐重建组缺口。
    pub(crate) fn cmp_fill_gaps(&mut self, cx: &mut Context<Self>) {
        let status = self.compare.as_mut().map(|c| c.fill_gaps());
        if let Some(status) = status {
            self.status = status;
        }
        cx.notify();
    }

    /// `i` 忽略默认值 / `n` 破坏性默认勾选：都是**服务端**选项，改完必须重跑比对，
    /// 否则屏幕上的计划与 id 就与服务端重算的不是同一份（§15.5）。
    pub(crate) fn cmp_toggle_option(&mut self, ignore: bool, cx: &mut Context<Self>) {
        let (status, rerun) = match self.compare.as_mut() {
            Some(c) => c.toggle_option(ignore),
            None => return,
        };
        self.status = status;
        if rerun {
            self.cmp_run_diff(cx);
        }
        cx.notify();
    }

    /// ③ `g`：按勾选集取脚本 → ④。缺口没补齐时不提交（服务端一定 400）。
    pub(crate) fn cmp_generate(&mut self, cx: &mut Context<Self>) {
        let req = match self.compare.as_ref().map(|c| c.ddl_request()) {
            Some(Ok(req)) => req,
            Some(Err(Some(why))) => {
                self.status = why.into();
                cx.notify();
                return;
            }
            _ => return,
        };
        if let Some(c) = self.compare.as_mut() {
            c.begin_io();
        }
        self.status = format!("生成脚本：{} 条变更…", req.selected.len());
        cx.notify();
        self.io(
            cx,
            move |t| Box::pin(async move { t.schema_ddl(&req).await }),
            move |this, out, cx| {
                let status = match out {
                    Ok(script) => this.compare.as_mut().map(|c| c.finish_script(script)),
                    Err(e) => {
                        if let Some(c) = this.compare.as_mut() {
                            c.fail_io();
                        }
                        Some(format!("生成脚本失败: {e}"))
                    }
                };
                if let Some(status) = status {
                    this.status = status;
                }
                cx.notify();
            },
        );
    }

    /// ④ `x` / 回滚确认后的执行。分派环路本身在 `cmpsync::run_script`（可单测），
    /// 这里只给它接上真实线路：`TransportExec` 是那五个动作的 Transport 实现。
    pub(crate) fn cmp_apply(&mut self, cx: &mut Context<Self>, rollback: bool) {
        let run = match self.compare.as_ref().map(|c| c.script_to_run(rollback)) {
            Some(Ok(v)) => v,
            Some(Err(Some(why))) => {
                self.status = why.into();
                cx.notify();
                return;
            }
            _ => return,
        };
        let (id, script) = run;
        let status = self
            .compare
            .as_mut()
            .map(|c| c.begin_apply(rollback).to_string());
        if let Some(status) = status {
            self.status = status;
        }
        cx.notify();
        self.io(
            cx,
            move |t| {
                Box::pin(async move {
                    cmpsync::run_script(&script, rollback, &TransportExec { t: &*t, id }).await
                })
            },
            move |this, out, cx| {
                let status = this.compare.as_mut().map(|c| c.finish_apply(out));
                if let Some(status) = status {
                    this.status = status;
                }
                cx.notify();
            },
        );
    }

    /// ④ `r`：进入回滚强确认输入态（门禁在纯层：只有正向全成功才允许）。
    pub(crate) fn cmp_start_confirm(&mut self, cx: &mut Context<Self>) {
        let name = self.cmp_names().0;
        let status = self.compare.as_mut().map(|c| c.start_confirm(&name));
        if let Some(status) = status {
            self.status = status;
        }
        cx.notify();
    }

    pub(crate) fn cmp_cancel_confirm(&mut self, cx: &mut Context<Self>) {
        let status = self.compare.as_mut().map(|c| c.cancel_confirm());
        if let Some(status) = status {
            self.status = status.into();
        }
        cx.notify();
    }

    pub(crate) fn cmp_submit_confirm(&mut self, cx: &mut Context<Self>) {
        let (typed, name) = (
            self.compare
                .as_ref()
                .map(|c| c.confirm_buf.clone())
                .unwrap_or_default(),
            self.cmp_names().0,
        );
        if !cmpsync::confirm_matches(&typed, &name) {
            self.status = format!("确认文字不匹配：请逐字输入「{name}」");
            cx.notify();
            return;
        }
        self.cmp_apply(cx, true);
    }

    /// 向导键位：与 TUI 的 viewCompare 同集合（外加浮层固有的 Esc/Backspace 返回）。
    /// E6.3：按键 → 语义这张表在 `cmpsync::key_action`，这里只认 gpui 的事件结构。
    pub(crate) fn on_cmp_key(&mut self, ev: &KeyDownEvent, cx: &mut Context<Self>) {
        let ks = &ev.keystroke;
        let Some(c) = self.compare.as_ref() else {
            return;
        };
        let (step, confirming, loading) = (c.step, c.confirming, c.loading);
        if confirming {
            match ks.key.as_str() {
                "escape" => self.cmp_cancel_confirm(cx),
                "enter" => self.cmp_submit_confirm(cx),
                "backspace" => {
                    if let Some(c) = self.compare.as_mut() {
                        c.backspace_confirm();
                    }
                    cx.notify();
                }
                _ => {
                    if let Some(ch) = ks.key_char.as_deref().filter(|t| t.chars().count() == 1) {
                        if let Some(c) = self.compare.as_mut() {
                            c.insert_confirm(ch);
                        }
                        cx.notify();
                    }
                }
            }
            return;
        }
        if ks.modifiers.control || ks.modifiers.platform {
            if ks.key == "4" {
                self.close_compare(cx);
            }
            return;
        }
        let action = cmpsync::key_action(step, loading, ks.key.as_str());
        self.cmp_do_action(action, cx);
    }

    /// 纯层给出的动作 → 实体方法。这一层没有判断，只有转接。
    fn cmp_do_action(&mut self, action: CmpAction, cx: &mut Context<Self>) {
        match action {
            CmpAction::Nothing => {}
            CmpAction::Move(delta) => self.cmp_move(delta, cx),
            CmpAction::ToggleOption(ignore) => self.cmp_toggle_option(ignore, cx),
            CmpAction::PickSource => self.cmp_pick_source(cx),
            CmpAction::RunDiff => self.cmp_run_diff(cx),
            CmpAction::ToggleAtCursor => {
                let cur = self.compare.as_ref().map(|c| c.entry_cur).unwrap_or(0);
                self.cmp_toggle_entry(cur, cx);
            }
            CmpAction::SelectAll { only_safe } => self.cmp_select_all(only_safe, cx),
            CmpAction::FillGaps => self.cmp_fill_gaps(cx),
            CmpAction::Generate => self.cmp_generate(cx),
            CmpAction::Apply => self.cmp_apply(cx, false),
            CmpAction::StartConfirm => self.cmp_start_confirm(cx),
            CmpAction::Back => self.cmp_back(cx),
            CmpAction::Close => self.close_compare(cx),
        }
    }

    /// 返回上一步（Esc / Backspace）；① 步的 Esc 由按键表判成 Close。
    fn cmp_back(&mut self, cx: &mut Context<Self>) {
        if let Some(c) = self.compare.as_mut() {
            c.back();
        }
        cx.notify();
    }

    /// 弹层底部键位提示（与 TUI 的 `cmpFooter` 同语义，逐字对齐后写进 docs/frontends.md）。
    pub(crate) fn cmp_footer_text(&self) -> &'static str {
        self.compare.as_ref().map(|c| c.footer()).unwrap_or("")
    }

    // ─── 网格数据访问（预览/右键菜单按作用域解析）───

    pub(crate) fn grid_cols(&self, src: GridSrc) -> Option<&[ResultColumn]> {
        match src {
            GridSrc::Workbench => self.result.as_ref().map(|r| r.columns.as_slice()),
            GridSrc::Table(i) => self
                .tabs
                .get(i)?
                .browse
                .grid
                .as_ref()
                .map(|g| g.columns.as_slice()),
        }
    }

    pub(crate) fn grid_rows(&self, src: GridSrc) -> Option<&[Vec<Value>]> {
        match src {
            GridSrc::Workbench => self.result.as_ref().map(|r| r.rows.as_slice()),
            GridSrc::Table(i) => self
                .tabs
                .get(i)?
                .browse
                .grid
                .as_ref()
                .map(|g| g.rows.as_slice()),
        }
    }

    pub(crate) fn grid_cell(&self, src: GridSrc, row: usize, col: usize) -> Option<&Value> {
        self.grid_rows(src)?.get(row)?.get(col)
    }

    // ─── 多行文本编辑（键盘驱动，cursor/anchor 为字节下标，缓冲允许 \n）───

    /// 当前选区 `[start,end)`（字节）；无选区返回 None。
    pub(crate) fn sel_range(&self) -> Option<(usize, usize)> {
        let a = self.sql_anchor?;
        if a == self.cursor {
            return None;
        }
        let (s, e) = if a < self.cursor {
            (a, self.cursor)
        } else {
            (self.cursor, a)
        };
        let s = s.min(self.sql.len());
        let e = e.min(self.sql.len());
        if s < e {
            Some((s, e))
        } else {
            None
        }
    }

    /// 快照入撤销栈并清空重做栈（上限 200 步）。
    fn push_undo(&mut self) {
        self.sql_undo.push(SqlSnapshot {
            sql: self.sql.clone(),
            cursor: self.cursor,
            anchor: self.sql_anchor,
        });
        if self.sql_undo.len() > 200 {
            self.sql_undo.remove(0);
        }
        self.sql_redo.clear();
    }

    pub(crate) fn sql_undo_step(&mut self) {
        if let Some(s) = self.sql_undo.pop() {
            self.sql_redo.push(SqlSnapshot {
                sql: self.sql.clone(),
                cursor: self.cursor,
                anchor: self.sql_anchor,
            });
            self.sql = s.sql;
            self.cursor = s.cursor.min(self.sql.len());
            self.sql_anchor = s.anchor.map(|a| a.min(self.sql.len()));
        }
    }

    pub(crate) fn sql_redo_step(&mut self) {
        if let Some(s) = self.sql_redo.pop() {
            self.sql_undo.push(SqlSnapshot {
                sql: self.sql.clone(),
                cursor: self.cursor,
                anchor: self.sql_anchor,
            });
            self.sql = s.sql;
            self.cursor = s.cursor.min(self.sql.len());
            self.sql_anchor = s.anchor.map(|a| a.min(self.sql.len()));
        }
    }

    /// 若存在选区则删除并将光标塌缩到起点，返回是否发生了删除。调用方负责 push_undo。
    fn delete_selection(&mut self) -> bool {
        if let Some((s, e)) = self.sel_range() {
            self.sql.replace_range(s..e, "");
            self.cursor = s;
            self.sql_anchor = None;
            return true;
        }
        false
    }

    fn insert_text(&mut self, text: &str) {
        self.push_undo();
        self.delete_selection();
        let boundary = char_ceil(&self.sql, self.cursor);
        self.sql.insert_str(boundary, text);
        self.cursor = boundary + text.len();
    }

    // Shift+Enter 要插入的换行文本：光标所在行以 Smart AutoIndent 词开头就带
    // 一级缩进；有选区时不猜缩进，只塌缩选区。
    fn newline_text(&self) -> &'static str {
        if self.sel_range().is_some() {
            return "\n";
        }
        let (start, end) = line_bounds(&self.sql, self.cursor.min(self.sql.len()));
        newline_insert(&self.sql[start..end])
    }

    fn backspace(&mut self) {
        if self.sel_range().is_none() && self.cursor == 0 {
            return;
        }
        self.push_undo();
        if self.delete_selection() {
            return;
        }
        // 退到**光标前那个字符**的起点：char_floor 落在边界上会原地不动（ASCII 恒如此），
        // 所以先退一格再取边界——与 `util::edit_line` 的退格同一口径。
        let start = char_floor(&self.sql, self.cursor - 1);
        self.sql.replace_range(start..self.cursor, "");
        self.cursor = start;
    }

    fn delete_forward(&mut self) {
        if self.sel_range().is_none() && self.cursor >= self.sql.len() {
            return;
        }
        self.push_undo();
        if self.delete_selection() {
            return;
        }
        let end = char_ceil(&self.sql, self.cursor);
        self.sql.replace_range(self.cursor..end, "");
    }

    // 移动光标：shift 时以锚点扩展选区，否则塌缩选区。
    fn apply_move(&mut self, shift: bool, new_cursor: usize) {
        if shift {
            if self.sql_anchor.is_none() {
                self.sql_anchor = Some(self.cursor);
            }
        } else {
            self.sql_anchor = None;
        }
        self.cursor = new_cursor;
    }

    fn move_left(&mut self, shift: bool) {
        let nc = if self.cursor == 0 {
            0
        } else {
            char_floor(&self.sql, self.cursor - 1)
        };
        self.apply_move(shift, nc);
    }

    fn move_right(&mut self, shift: bool) {
        let nc = if self.cursor >= self.sql.len() {
            self.sql.len()
        } else {
            char_ceil(&self.sql, self.cursor + 1)
        };
        self.apply_move(shift, nc);
    }

    // Ctrl+← / Ctrl+Shift+←：按词向左移动光标。
    fn move_word_left(&mut self, shift: bool) {
        let nc = word_floor(&self.sql, self.cursor);
        self.apply_move(shift, nc);
    }

    fn move_word_right(&mut self, shift: bool) {
        let nc = word_ceil(&self.sql, self.cursor);
        self.apply_move(shift, nc);
    }

    fn move_home(&mut self, shift: bool) {
        let nc = line_bounds(&self.sql, self.cursor).0;
        self.apply_move(shift, nc);
    }

    fn move_end(&mut self, shift: bool) {
        let nc = line_bounds(&self.sql, self.cursor).1;
        self.apply_move(shift, nc);
    }

    // 跨行上下移动：保持字符列，越界钳到新行行尾。
    fn move_up(&mut self, shift: bool) {
        let (start, _) = line_bounds(&self.sql, self.cursor);
        let nc = if start == 0 {
            start
        } else {
            let col = self.sql[start..self.cursor].chars().count();
            let (ps, pe) = line_bounds(&self.sql, start - 1);
            ps + col_to_byte(&self.sql[ps..pe], col)
        };
        self.apply_move(shift, nc);
    }

    fn move_down(&mut self, shift: bool) {
        let (start, end) = line_bounds(&self.sql, self.cursor);
        let nc = if end >= self.sql.len() {
            end
        } else {
            let col = self.sql[start..self.cursor].chars().count();
            let (ns, ne) = line_bounds(&self.sql, end + 1);
            ns + col_to_byte(&self.sql[ns..ne], col)
        };
        self.apply_move(shift, nc);
    }

    pub(crate) fn select_all(&mut self) {
        self.sql_anchor = Some(0);
        self.cursor = self.sql.len();
    }

    fn copy_selection(&self, cx: &mut Context<Self>) {
        if let Some((s, e)) = self.sel_range() {
            let text = self.sql[s..e].to_string();
            cx.write_to_clipboard(ClipboardItem::new_string(text));
        }
    }

    fn cut_selection(&mut self, cx: &mut Context<Self>) {
        self.copy_selection(cx);
        if self.sel_range().is_some() {
            self.push_undo();
            self.delete_selection();
        }
    }

    pub(crate) fn on_input_key(&mut self, ev: &KeyDownEvent, cx: &mut Context<Self>) {
        let ks = &ev.keystroke;
        if ks.modifiers.control || ks.modifiers.platform {
            match ks.key.as_str() {
                "v" => {
                    if let Some(item) = cx.read_from_clipboard() {
                        if let Some(text) = item.text() {
                            self.insert_text(&text);
                            cx.notify();
                        }
                    }
                }
                "c" => self.copy_selection(cx),
                "x" => self.cut_selection(cx),
                "a" => self.select_all(),
                "z" => {
                    if ks.modifiers.shift {
                        self.sql_redo_step();
                    } else {
                        self.sql_undo_step();
                    }
                    cx.notify();
                }
                "y" => {
                    self.sql_redo_step();
                    cx.notify();
                }
                "left" => {
                    self.move_word_left(ks.modifiers.shift);
                    cx.notify();
                }
                "right" => {
                    self.move_word_right(ks.modifiers.shift);
                    cx.notify();
                }
                "p" => self.open_quick_switcher(cx),
                "4" => self.open_compare(cx),
                "e" if ks.modifiers.shift => self.run_explain(cx),
                "enter" => self.run_query(cx),
                _ => {}
            }
            return;
        }
        let shift = ks.modifiers.shift;
        match ks.key.as_str() {
            // Enter 执行、Shift+Enter 换行（与多数 SQL 客户端一致）。
            "enter" => {
                if shift {
                    self.insert_text(self.newline_text());
                    cx.notify();
                } else {
                    self.run_query(cx);
                }
                return;
            }
            // F9 执行计划、Esc 关闭计划浮层（与 TUI 键位对齐）。
            "f9" => {
                self.run_explain(cx);
                return;
            }
            "escape" if self.explain.is_some() => {
                self.explain = None;
                cx.notify();
                return;
            }
            "backspace" => self.backspace(),
            "delete" => self.delete_forward(),
            "left" => self.move_left(shift),
            "right" => self.move_right(shift),
            "up" => self.move_up(shift),
            "down" => self.move_down(shift),
            "home" => self.move_home(shift),
            "end" => self.move_end(shift),
            _ => {
                if let Some(ch) = ks.key_char.as_deref() {
                    if ch.chars().count() == 1 {
                        self.insert_text(ch);
                    }
                }
            }
        }
        cx.notify();
    }

    // 渲染 SQL：单一文本串（含 \n），在光标处插入 '▏'；返回 (显示文本, 选区显示坐标区间)。
    pub(crate) fn sql_display(&self) -> (String, Vec<Range<usize>>) {
        const CARET: char = '▏';
        let clen = CARET.len_utf8();
        let cur = char_ceil(&self.sql, self.cursor.min(self.sql.len()));
        let mut disp = String::with_capacity(self.sql.len() + clen);
        disp.push_str(&self.sql[..cur]);
        disp.push(CARET);
        disp.push_str(&self.sql[cur..]);
        let mut ranges = Vec::new();
        if let Some((s, e)) = self.sel_range() {
            let sh = |x: usize| if x > cur { x + clen } else { x };
            let (rs, re) = (sh(s), sh(e));
            if rs < re {
                ranges.push(rs..re);
            }
        }
        (disp, ranges)
    }

    // 显示坐标下标（index_for_position 返回值）→ self.sql 字节下标（回退光标字符）。
    fn display_to_sql(&self, d: usize) -> usize {
        const CARET: char = '▏';
        let clen = CARET.len_utf8();
        let cur = char_ceil(&self.sql, self.cursor.min(self.sql.len()));
        if d <= cur {
            d
        } else if d >= cur + clen {
            d - clen
        } else {
            cur
        }
    }

    // 鼠标按下：定位光标；双击选词；开始拖拽选区。
    pub(crate) fn on_sql_mouse_down(
        &mut self,
        ev: &MouseDownEvent,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        window.focus(&self.focus);
        let layout = self.sql_layout.clone();
        let Some(layout) = layout else {
            return;
        };
        let d = layout.index_for_position(ev.position).unwrap_or_else(|e| e);
        let ix = self.display_to_sql(d);
        if ev.click_count >= 2 {
            if let Some((s, e)) = word_at(&self.sql, ix) {
                self.cursor = e;
                self.sql_anchor = Some(s);
                self.sql_selecting = false;
                cx.notify();
                return;
            }
        }
        self.cursor = ix;
        self.sql_anchor = Some(ix);
        self.sql_selecting = true;
        cx.notify();
    }

    pub(crate) fn on_sql_mouse_move(&mut self, ev: &MouseMoveEvent, cx: &mut Context<Self>) {
        if !self.sql_selecting {
            return;
        }
        let layout = self.sql_layout.clone();
        let Some(layout) = layout else {
            return;
        };
        let d = layout.index_for_position(ev.position).unwrap_or_else(|e| e);
        let ix = self.display_to_sql(d);
        if ix != self.cursor {
            self.cursor = ix;
            cx.notify();
        }
    }

    pub(crate) fn on_sql_mouse_up(&mut self, _ev: &MouseUpEvent, cx: &mut Context<Self>) {
        if self.sql_selecting {
            self.sql_selecting = false;
            cx.notify();
        }
    }

    // ─── G5.3 Quick Switcher（Ctrl+P：在打开的对象标签间跳转）───

    pub(crate) fn open_quick_switcher(&mut self, cx: &mut Context<Self>) {
        self.quick_open = true;
        self.quick_query.clear();
        self.quick_cursor = 0;
        self.quick_sel = 0;
        cx.notify();
    }

    pub(crate) fn close_quick_switcher(&mut self) {
        self.quick_open = false;
    }

    /// 匹配列表：(目标, 标签文本)。含工作台 + 已打开 tab + 当前 schema 下未打开的表。
    /// query 大小写不敏感子串过滤；空 query 列全部。
    pub(crate) fn quick_matches(&self) -> Vec<(QuickTarget, String)> {
        let q = self.quick_query.trim().to_lowercase();
        let mut all: Vec<(QuickTarget, String)> = Vec::new();
        all.push((QuickTarget::Workbench, "SQL 工作台".to_string()));
        for (i, t) in self.tabs.iter().enumerate() {
            all.push((QuickTarget::Tab(i), format!("{}.{}", t.schema, t.table)));
        }
        let open: std::collections::HashSet<String> = self
            .tabs
            .iter()
            .map(|t| tabs::table_key(&t.schema, &t.table))
            .collect();
        for tb in &self.tables {
            let key = tabs::table_key(&tb.schema, &tb.name);
            if open.contains(&key) {
                continue;
            }
            all.push((
                QuickTarget::NewTable(tb.schema.clone(), tb.name.clone()),
                key,
            ));
        }
        if q.is_empty() {
            all
        } else {
            all.into_iter()
                .filter(|(_, label)| label.to_lowercase().contains(&q))
                .collect()
        }
    }

    fn quick_move(&mut self, delta: isize) {
        let n = self.quick_matches().len();
        if n == 0 {
            self.quick_sel = 0;
            return;
        }
        let cur = self.quick_sel.min(n - 1) as isize;
        self.quick_sel = ((cur + delta + n as isize) % n as isize) as usize;
    }

    pub(crate) fn quick_activate(&mut self, target: QuickTarget, cx: &mut Context<Self>) {
        match target {
            QuickTarget::Workbench => self.active = ActiveView::Workbench,
            QuickTarget::Tab(i) => self.active = ActiveView::Table(i),
            QuickTarget::NewTable(schema, table) => {
                self.open_table_tab(schema, table, cx);
                cx.notify();
                return;
            }
        }
        self.quick_open = false;
        cx.notify();
    }

    pub(crate) fn on_quick_key(&mut self, ev: &KeyDownEvent, cx: &mut Context<Self>) {
        let ks = &ev.keystroke;
        if ks.modifiers.control || ks.modifiers.platform {
            match ks.key.as_str() {
                "p" | "escape" => {
                    self.close_quick_switcher();
                    cx.notify();
                }
                _ => {}
            }
            return;
        }
        match ks.key.as_str() {
            "escape" => {
                self.close_quick_switcher();
                cx.notify();
                return;
            }
            "enter" => {
                let matches = self.quick_matches();
                if let Some((target, _)) = matches.get(self.quick_sel) {
                    let target = target.clone();
                    self.quick_activate(target, cx);
                }
                return;
            }
            "up" => self.quick_move(-1),
            "down" => self.quick_move(1),
            "backspace" => {
                self.quick_line_backspace();
            }
            _ => {
                if let Some(ch) = ks.key_char.as_deref() {
                    if ch.chars().count() == 1 {
                        self.quick_line_insert(ch);
                    }
                }
            }
        }
        self.quick_sel = 0;
        cx.notify();
    }

    fn quick_line_insert(&mut self, text: &str) {
        let boundary = char_ceil(&self.quick_query, self.quick_cursor);
        self.quick_query.insert_str(boundary, text);
        self.quick_cursor = boundary + text.len();
    }

    fn quick_line_backspace(&mut self) {
        if self.quick_cursor == 0 {
            return;
        }
        let start = char_floor(&self.quick_query, self.quick_cursor - 1);
        self.quick_query.replace_range(start..self.quick_cursor, "");
        self.quick_cursor = start;
    }
}

fn tab_set(this: &mut PolyDBApp, idx: usize, f: impl FnOnce(&mut TableTabState)) {
    if let Some(tab) = this.tabs.get_mut(idx) {
        f(tab);
    }
}

/// 连接是否只读（`read_only` 缺省视为可写，与服务端口径一致）。
fn conn_is_read_only(c: &ConnectionInfo) -> bool {
    c.read_only.unwrap_or(false)
}

/// 单元格/编辑缓冲的纯文本呈现：NULL→"NULL"，字符串原样，数组/对象转 JSON。
fn value_to_text(v: &Value) -> String {
    match v {
        Value::Null => "NULL".to_string(),
        Value::Bool(b) => {
            if *b {
                "true".to_string()
            } else {
                "false".to_string()
            }
        }
        Value::Integer(i) => i.to_string(),
        Value::Float(f) => format!("{}", f),
        Value::String(s) => s.clone(),
        other => serde_json::to_string(other).unwrap_or_default(),
    }
}

/// 编辑缓冲 → Value：字面 "NULL"（不区分大小写）视为 SQL NULL，其余走 numish 探测。
fn parse_edit_value(s: &str) -> Value {
    if s.trim().eq_ignore_ascii_case("NULL") {
        Value::Null
    } else {
        filters::parse_wire_value(s)
    }
}

/// 单事务提交结果（applied = 已执行且校验通过的语句数）。
pub(crate) struct CommitResult {
    pub ok: bool,
    pub applied: u64,
    pub error: Option<String>,
}
