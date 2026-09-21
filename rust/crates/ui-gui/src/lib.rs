//! polydb-ui-gui：GPUI 桌面前端。
//!
//! 铁律（AGENTS.md §2）：前端只通过 `transport`（进程内 LocalTransport）访问数据，
//! 不依赖任何 driver crate。布局：启动卡片屏（TablePro 式交互设计，仅借鉴不 port 代码）
//! → 工作区：左侧连接侧栏 + 多行 SQL 编辑器（Enter 执行 / Shift+Enter 换行 /
//! Ctrl+V 粘贴）、状态栏、结果表格（左键预览、右键菜单、单元格 文本/JSON/HEX 面板）。

use std::future::Future;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use gpui::prelude::FluentBuilder;
use gpui::*;
use polydb_core::{
    ConnectionId, ConnectionInfo, CoreResult, QueryResult, TableInfo, TableRowsRequest, Value,
};
use polydb_transport::Transport;

// ─── IO 桥 ─────────────────────────────────────────────────
// gpui 自带 executor，而驱动（sqlx / tiberius / redis）的 future 需要 tokio
// reactor。所有 transport 调用统一委托给专用多线程 runtime；
// `JoinHandle` 是标准 `Future`，在 gpui 侧 await 合法。

struct IoBridge {
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

pub struct PolyDBApp {
    transport: Arc<dyn Transport>,
    io: Arc<IoBridge>,
    conns: Vec<ConnectionInfo>,
    sel: Option<usize>,
    busy: bool,
    status: String,
    sql: String,
    cursor: usize,
    focus: FocusHandle,
    result: Option<QueryResult>,
    preview: Option<PreviewState>,
    ctx_menu: Option<CellMenuState>,
    /// U2 式工作区开关：None=启动卡片屏，Some(i)=对应连接的工作区。
    workspace: Option<usize>,
    /// 表数据浏览：侧栏表清单 + 分页浏览状态（None=普通查询结果视图）。
    tables: Vec<TableInfo>,
    browse: Option<BrowseState>,
    /// 多语句执行（M27 语义）：逐条结果清单 + 尽力取消标志。
    multi_log: Vec<(String, StmtOutcome)>,
    cancel: Arc<AtomicBool>,
    /// 连接心跳（30s ping 当前工作区连接）。
    hb: Option<HbState>,
}

#[derive(Clone)]
struct HbState {
    ok: bool,
    ms: f64,
    err: Option<String>,
}

// 单条语句的执行结果（✓ 行数 / ✗ 错误 / ⊘ 取消跳过）。
#[derive(Clone)]
enum StmtOutcome {
    Ok(usize),
    Err(String),
    Skipped,
}

// 浏览模式状态：当前页偏移与分页信息；行数据仍复用 self.result 网格渲染。
#[derive(Clone)]
struct BrowseState {
    schema: String,
    table: String,
    offset: u64,
    limit: u32,
    has_more: bool,
    total: Option<u64>,
    shown: usize,
}

// 单元格右键菜单：窗口坐标（Points）+ 目标单元格。
#[derive(Clone, Copy)]
struct CellMenuState {
    x: f32,
    y: f32,
    row: usize,
    col: usize,
}

// 单元格预览（与 Web 端 CellPreviewPanel 语义对齐：文本/JSON/HEX 三页签）。
#[derive(Clone, Copy, PartialEq, Eq)]
enum PreviewTab {
    Text,
    Json,
    Hex,
}

#[derive(Clone, Copy)]
struct PreviewState {
    row: usize,
    col: usize,
    tab: PreviewTab,
}

// 结果表格最多渲染的行数（超出显示提示，与 TUI 行为一致）。
const MAX_RESULT_ROWS: usize = 200;

// 网格单元格显示截断长度（完整内容看预览面板）。
const CELL_CLIP: usize = 60;

fn value_str(v: &Value) -> String {
    match v {
        Value::Null => "NULL".to_string(),
        Value::Bool(b) => b.to_string(),
        Value::Integer(i) => i.to_string(),
        Value::Float(f) => f.to_string(),
        Value::String(s) => s.clone(),
        Value::Array(_) | Value::Object(_) => {
            serde_json::to_string(v).unwrap_or_else(|_| "<complex>".to_string())
        }
    }
}

// 与 Web 端 tryParseJson 对齐：仅对象/数组字面量视为可美化 JSON。
fn try_pretty_json(v: &Value) -> Option<String> {
    match v {
        Value::Array(_) | Value::Object(_) => serde_json::to_string_pretty(v).ok(),
        Value::String(s) => {
            let t = s.trim_start();
            if !(t.starts_with('{') || t.starts_with('[')) {
                return None;
            }
            serde_json::from_str::<serde_json::Value>(t)
                .ok()
                .and_then(|j| serde_json::to_string_pretty(&j).ok())
        }
        _ => None,
    }
}

// 16 字节/行的经典 hexdump（偏移 + hex + ASCII gutter）。
fn hex_dump(s: &str) -> String {
    let bytes = s.as_bytes();
    if bytes.is_empty() {
        return "(empty)".to_string();
    }
    let mut lines: Vec<String> = Vec::new();
    for (n, chunk) in bytes.chunks(16).enumerate() {
        let hex = chunk
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<Vec<_>>()
            .join(" ");
        let ascii: String = chunk
            .iter()
            .map(|&b| {
                if (0x20..0x7f).contains(&b) {
                    b as char
                } else {
                    '·'
                }
            })
            .collect();
        lines.push(format!("{:08x}  {:<47}  |{ascii}|", n * 16, hex));
    }
    lines.join("\n")
}

fn clip(s: &str, max: usize) -> String {
    let count = s.chars().count();
    if count <= max {
        return s.to_string();
    }
    let cut: String = s.chars().take(max).collect();
    format!("{cut}…({} 字符)", count)
}

// 把字节下标回退到最近的 char 边界（用于光标左移 / backspace）。
fn char_floor(s: &str, i: usize) -> usize {
    if s.is_char_boundary(i) {
        i
    } else {
        s[..i]
            .char_indices()
            .next_back()
            .map(|(pos, _)| pos)
            .unwrap_or(0)
    }
}

// 把字节下标前进到最近的 char 边界（用于光标右移 / 删除）。
fn char_ceil(s: &str, i: usize) -> usize {
    if s.is_char_boundary(i) {
        i
    } else {
        s[..i]
            .char_indices()
            .next_back()
            .map(|(pos, c)| pos + c.len_utf8())
            .unwrap_or(s.len())
    }
}

// 光标所在行的字节边界 [start, end)，end 不含换行符。
fn line_bounds(s: &str, i: usize) -> (usize, usize) {
    let start = s[..i].rfind('\n').map(|p| p + 1).unwrap_or(0);
    let end = s[i..].find('\n').map(|p| i + p).unwrap_or(s.len());
    (start, end)
}

// 行内按“字符列”定位字节下标（越界钳到行尾，保证 char 边界）。
fn col_to_byte(line: &str, col_chars: usize) -> usize {
    line.char_indices()
        .nth(col_chars)
        .map(|(i, _)| i)
        .unwrap_or(line.len())
}

// 按分号拆分多语句，字符串/注释感知（语义对齐 Web M27 splitSql）：
// 引号内分号不切；-- 与 # 行注释、块注释中的分号不切；纯注释不构成语句。
fn split_statements(sql: &str) -> Vec<String> {
    let cs: Vec<char> = sql.chars().collect();
    let mut out = Vec::new();
    let mut cur = String::new();
    let mut code = false; // 当前缓冲里是否已有真实代码（非空白/注释）
    let mut i = 0;
    while i < cs.len() {
        let c = cs[i];
        match c {
            '\'' | '"' | '`' => {
                let q = c;
                code = true;
                cur.push(q);
                i += 1;
                while i < cs.len() {
                    let d = cs[i];
                    if d == '\\' && i + 1 < cs.len() {
                        cur.push(d);
                        cur.push(cs[i + 1]);
                        i += 2;
                        continue;
                    }
                    cur.push(d);
                    i += 1;
                    if d == q {
                        // 双写转义（'' / "" / ``）继续留在字符串内。
                        if i < cs.len() && cs[i] == q {
                            cur.push(q);
                            i += 1;
                            continue;
                        }
                        break;
                    }
                }
            }
            '-' if i + 1 < cs.len() && cs[i + 1] == '-' => {
                while i < cs.len() && cs[i] != '\n' {
                    cur.push(cs[i]);
                    i += 1;
                }
            }
            '#' => {
                while i < cs.len() && cs[i] != '\n' {
                    cur.push(cs[i]);
                    i += 1;
                }
            }
            '/' if i + 1 < cs.len() && cs[i + 1] == '*' => {
                cur.push('/');
                cur.push('*');
                i += 2;
                while i < cs.len() {
                    if cs[i] == '*' && i + 1 < cs.len() && cs[i + 1] == '/' {
                        cur.push('*');
                        cur.push('/');
                        i += 2;
                        break;
                    }
                    cur.push(cs[i]);
                    i += 1;
                }
            }
            ';' => {
                if code {
                    let t = cur.trim();
                    if !t.is_empty() {
                        out.push(t.to_string());
                    }
                }
                cur.clear();
                code = false;
                i += 1;
            }
            _ => {
                if !c.is_whitespace() {
                    code = true;
                }
                cur.push(c);
                i += 1;
            }
        }
    }
    if code && !cur.trim().is_empty() {
        out.push(cur.trim().to_string());
    }
    out
}

// 多语句日志用的单行摘要。
fn stmt_preview(s: &str) -> String {
    let one: String = s.chars().map(|c| if c == '\n' { ' ' } else { c }).collect();
    clip(one.trim(), 60)
}

impl PolyDBApp {
    pub fn new(transport: Arc<dyn Transport>, cx: &mut Context<Self>) -> Self {
        let mut this = Self {
            transport,
            io: Arc::new(IoBridge::new()),
            conns: Vec::new(),
            sel: None,
            busy: false,
            status: "加载中…".into(),
            sql: String::new(),
            cursor: 0,
            focus: cx.focus_handle(),
            result: None,
            preview: None,
            ctx_menu: None,
            workspace: None,
            tables: Vec::new(),
            browse: None,
            multi_log: Vec::new(),
            cancel: Arc::new(AtomicBool::new(false)),
            hb: None,
        };
        this.refresh(cx);
        this.start_heartbeat(cx);
        this
    }

    fn sel_id(&self) -> Option<ConnectionId> {
        self.sel.and_then(|i| self.conns.get(i).map(|c| c.id))
    }

    // ─── 动作（异步委托 IoBridge，完成后回主线程更新状态） ───

    fn refresh(&mut self, cx: &mut Context<Self>) {
        let t = Arc::clone(&self.transport);
        let join = self.io.spawn(async move { t.list_connections() });
        cx.spawn(async move |this, cx| {
            let out = join.await;
            let _ = this.update(cx, |this, cx| {
                match out {
                    Ok(Ok(conns)) => {
                        // 列表索引可能变化：退回启动屏，清工作区态。
                        this.sel = None;
                        this.workspace = None;
                        this.conns = conns;
                        this.result = None;
                        this.preview = None;
                        this.ctx_menu = None;
                        this.browse = None;
                        this.tables.clear();
                        this.status = "已加载连接列表".into();
                    }
                    Ok(Err(e)) => this.status = format!("加载连接失败: {e}"),
                    Err(e) => this.status = format!("IO 任务失败: {e}"),
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn connect(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else {
            self.status = "请先在左侧选择连接".into();
            cx.notify();
            return;
        };
        let t = Arc::clone(&self.transport);
        let join = self.io.spawn(async move { t.connect(id) });
        cx.spawn(async move |this, cx| {
            let out = join.await;
            let _ = this.update(cx, |this, cx| {
                match out {
                    Ok(Ok(())) => this.status = "已连接（驱动实例已打开）".into(),
                    Ok(Err(e)) => this.status = format!("连接失败: {e}"),
                    Err(e) => this.status = format!("IO 任务失败: {e}"),
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn disconnect(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else {
            self.status = "请先在左侧选择连接".into();
            cx.notify();
            return;
        };
        let t = Arc::clone(&self.transport);
        let join = self.io.spawn(async move {
            t.disconnect(id);
        });
        cx.spawn(async move |this, cx| {
            let _ = join.await;
            let _ = this.update(cx, |this, cx| {
                this.status = "已断开".into();
                cx.notify();
            });
        })
        .detach();
    }

    fn run_query(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else {
            self.status = "请先在左侧选择连接".into();
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
        self.busy = true;
        self.result = None;
        self.preview = None;
        self.ctx_menu = None;
        self.browse = None; // 普通查询退出浏览模式
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

    // ─── 心跳：每 30s ping 当前工作区连接（后端 read_only 之外的存活显示）───

    fn beat(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id().filter(|_| self.workspace.is_some()) else {
            return;
        };
        let t = Arc::clone(&self.transport);
        let join = self.io.spawn(async move {
            let start = std::time::Instant::now();
            let res = t.ping(id).await;
            let ms = start.elapsed().as_secs_f64() * 1000.0;
            (res.is_ok(), res.err().map(|e| e.to_string()), ms)
        });
        cx.spawn(async move |this, cx| {
            let out = join.await;
            let _ = this.update(cx, |this, cx| {
                if let Ok((ok, err, ms)) = out {
                    this.hb = Some(HbState { ok, ms, err });
                    cx.notify();
                }
            });
        })
        .detach();
    }

    fn start_heartbeat(&mut self, cx: &mut Context<Self>) {
        cx.spawn(async move |this, cx| loop {
            cx.background_executor()
                .timer(std::time::Duration::from_secs(30))
                .await;
            let _ = this.update(cx, |this, cx| this.beat(cx));
        })
        .detach();
    }

    // ─── 表数据浏览（browse_rows，服务端分页；对齐 TUI F7 / Web TableDataView）───

    // load_tables 拉取首个 schema 的表清单，填充侧栏。
    fn load_tables(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else {
            return;
        };
        self.tables.clear();
        let t = Arc::clone(&self.transport);
        let join: tokio::task::JoinHandle<CoreResult<(String, Vec<TableInfo>)>> =
            self.io.spawn(async move {
                let schemas = t.list_schemas(id).await?;
                let schema = schemas
                    .first()
                    .map(|s| s.name.clone())
                    .unwrap_or_else(|| "main".to_string());
                let tables = t.list_tables(id, &schema).await?;
                Ok((schema, tables))
            });
        cx.spawn(async move |this, cx| {
            let out = join.await;
            let _ = this.update(cx, |this, cx| {
                match out {
                    Ok(Ok((schema, tables))) => {
                        this.tables = tables;
                        this.status = format!("{} · {} 张表", schema, this.tables.len());
                    }
                    Ok(Err(e)) => this.status = format!("加载表失败: {e}"),
                    Err(e) => this.status = format!("IO 任务失败: {e}"),
                }
                cx.notify();
            });
        })
        .detach();
    }

    // open_browse 拉取 schema.table 的一页数据（offset 起始），结果进共享网格。
    fn open_browse(&mut self, schema: String, table: String, offset: u64, cx: &mut Context<Self>) {
        let Some(id) = self.sel_id() else {
            self.status = "请先选择连接".into();
            cx.notify();
            return;
        };
        let limit = self.browse.as_ref().map_or(200, |b| b.limit);
        self.busy = true;
        self.result = None;
        self.preview = None;
        self.ctx_menu = None;
        self.browse = Some(BrowseState {
            schema: schema.clone(),
            table: table.clone(),
            offset,
            limit,
            has_more: false,
            total: None,
            shown: 0,
        });
        cx.notify();
        let t = Arc::clone(&self.transport);
        let join = self.io.spawn(async move {
            let req = TableRowsRequest {
                offset,
                limit,
                ..Default::default()
            };
            t.browse_rows(id, &schema, &table, &req).await
        });
        cx.spawn(async move |this, cx| {
            let out = join.await;
            let _ = this.update(cx, |this, cx| {
                this.busy = false;
                match out {
                    Ok(Ok(r)) => {
                        let shown = r.rows.len();
                        let total_est = r.total_estimate;
                        if let Some(b) = this.browse.as_mut() {
                            b.has_more = r.has_more;
                            b.total = total_est;
                            b.shown = shown;
                        }
                        this.result = Some(QueryResult {
                            columns: r.columns,
                            rows: r.rows,
                            affected_rows: 0,
                            execution_time_ms: r.execution_time_ms,
                            truncated: false,
                            total_rows: total_est,
                            has_more: r.has_more,
                            statement_type: None,
                        });
                        this.status = format!(
                            "浏览 {} 行–{} 行，耗时 {:.1}ms",
                            offset + 1,
                            offset + shown as u64,
                            r.execution_time_ms
                        );
                    }
                    Ok(Err(e)) => {
                        this.browse = None;
                        this.status = format!("浏览失败: {e}");
                    }
                    Err(e) => {
                        this.browse = None;
                        this.status = format!("IO 任务失败: {e}");
                    }
                }
                cx.notify();
            });
        })
        .detach();
    }

    // ─── 多行文本编辑（键盘驱动，cursor 为字节下标，缓冲允许 \n） ───

    fn insert_text(&mut self, text: &str) {
        let boundary = char_ceil(&self.sql, self.cursor);
        self.sql.insert_str(boundary, text);
        self.cursor = boundary + text.len();
    }

    fn backspace(&mut self) {
        if self.cursor == 0 {
            return;
        }
        let start = char_floor(&self.sql, self.cursor);
        self.sql.replace_range(start..self.cursor, "");
        self.cursor = start;
    }

    fn delete_forward(&mut self) {
        if self.cursor >= self.sql.len() {
            return;
        }
        let end = char_ceil(&self.sql, self.cursor);
        self.sql.replace_range(self.cursor..end, "");
    }

    fn move_left(&mut self) {
        if self.cursor == 0 {
            return;
        }
        self.cursor = char_floor(&self.sql, self.cursor - 1);
    }

    fn move_right(&mut self) {
        if self.cursor >= self.sql.len() {
            return;
        }
        self.cursor = char_ceil(&self.sql, self.cursor + 1);
    }

    fn move_home(&mut self) {
        self.cursor = line_bounds(&self.sql, self.cursor).0;
    }

    fn move_end(&mut self) {
        self.cursor = line_bounds(&self.sql, self.cursor).1;
    }

    // 跨行上下移动：保持字符列，越界钳到新行行尾。
    fn move_up(&mut self) {
        let (start, _) = line_bounds(&self.sql, self.cursor);
        if start == 0 {
            self.move_home();
            return;
        }
        let col = self.sql[start..self.cursor].chars().count();
        let (ps, pe) = line_bounds(&self.sql, start - 1);
        self.cursor = ps + col_to_byte(&self.sql[ps..pe], col);
    }

    fn move_down(&mut self) {
        let (start, end) = line_bounds(&self.sql, self.cursor);
        if end >= self.sql.len() {
            self.move_end();
            return;
        }
        let col = self.sql[start..self.cursor].chars().count();
        let (ns, ne) = line_bounds(&self.sql, end + 1);
        self.cursor = ns + col_to_byte(&self.sql[ns..ne], col);
    }

    fn on_input_key(&mut self, ev: &KeyDownEvent, cx: &mut Context<Self>) {
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
                "enter" => self.run_query(cx),
                _ => {}
            }
            return;
        }
        match ks.key.as_str() {
            // Enter 执行、Shift+Enter 换行（与多数 SQL 客户端一致）。
            "enter" => {
                if ks.modifiers.shift {
                    self.insert_text("\n");
                    cx.notify();
                } else {
                    self.run_query(cx);
                }
                return;
            }
            "backspace" => self.backspace(),
            "delete" => self.delete_forward(),
            "left" => self.move_left(),
            "right" => self.move_right(),
            "up" => self.move_up(),
            "down" => self.move_down(),
            "home" => self.move_home(),
            "end" => self.move_end(),
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

    // 渲染多行 SQL：光标行在 cursor 处插入光标字符，其余行原文。
    fn sql_lines(&self) -> Vec<String> {
        let mut lines: Vec<String> = self.sql.split('\n').map(|s| s.to_string()).collect();
        if lines.is_empty() {
            lines.push(String::new());
        }
        let (ls, _) = line_bounds(&self.sql, self.cursor);
        let idx = self.sql[..ls].matches('\n').count().min(lines.len() - 1);
        let col = self.cursor - ls;
        let at = char_ceil(&lines[idx], col.min(lines[idx].len()));
        let mut out = lines[idx][..at].to_string();
        out.push('▏');
        out.push_str(&lines[idx][at..]);
        lines[idx] = out;
        lines
    }

    // 单元格预览面板：文本 / JSON（自动美化）/ HEX 三页签，与 Web 端对齐。
    fn render_preview(&self, cx: &mut Context<Self>) -> Option<Div> {
        let p = self.preview?;
        let r = self.result.as_ref()?;
        let col = r.columns.get(p.col)?;
        let v = r.rows.get(p.row)?.get(p.col)?;
        let raw = value_str(v);
        let (hint, body) = match p.tab {
            PreviewTab::Text => (None, raw.clone()),
            PreviewTab::Json => match try_pretty_json(v) {
                Some(j) => (None, j),
                None => (Some("(无法解析为 JSON)"), raw.clone()),
            },
            PreviewTab::Hex => (None, hex_dump(&raw)),
        };
        let tabs: [(&str, PreviewTab); 3] = [
            ("文本", PreviewTab::Text),
            ("JSON", PreviewTab::Json),
            ("HEX", PreviewTab::Hex),
        ];
        let tab_btns = tabs.iter().enumerate().map(|(ti, (label, tab))| {
            let active = p.tab == *tab;
            let tab = *tab;
            div()
                .id(("ptab", ti))
                .px(px(8.0))
                .py(px(2.0))
                .rounded(px(3.0))
                .cursor(CursorStyle::PointingHand)
                .bg(if active { rgb(0x313244) } else { rgb(0x1e1e2e) })
                .text_color(if active { rgb(0xcdd6f4) } else { rgb(0x6c7086) })
                .text_size(px(11.0))
                .child((*label).to_string())
                .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                    if let Some(pr) = this.preview.as_mut() {
                        pr.tab = tab;
                    }
                    cx.notify();
                }))
        });
        Some(
            div()
                .flex_none()
                .flex()
                .flex_col()
                .bg(rgb(0x181825))
                .child(div().h(px(1.0)).bg(rgb(0x313244)))
                .child(
                    div()
                        .flex()
                        .items_center()
                        .gap(px(8.0))
                        .px(px(12.0))
                        .py(px(6.0))
                        .text_color(rgb(0xa6adc8))
                        .text_size(px(12.0))
                        .child(format!(
                            "单元格预览 · 行 {} · 列 {} ({})",
                            p.row + 1,
                            col.name,
                            col.data_type
                        ))
                        .children(tab_btns)
                        .child(
                            div()
                                .flex_1()
                                .text_size(px(11.0))
                                .text_color(rgb(0x6c7086))
                                .child(format!("{} 字符", raw.chars().count())),
                        )
                        .child(
                            div()
                                .id("ptab-close")
                                .px(px(6.0))
                                .cursor(CursorStyle::PointingHand)
                                .text_color(rgb(0xa6adc8))
                                .child("×")
                                .on_click(cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                                    this.preview = None;
                                    cx.notify();
                                })),
                        ),
                )
                .children(hint.map(|h| {
                    div()
                        .px(px(12.0))
                        .text_size(px(11.0))
                        .text_color(rgb(0xf38ba8))
                        .child(h.to_string())
                }))
                .child(
                    div()
                        .id("preview-body")
                        .max_h(px(240.0))
                        .overflow_y_scroll()
                        .px(px(12.0))
                        .pb(px(10.0))
                        .child(
                            div()
                                .text_size(px(12.0))
                                .font_family("monospace")
                                .text_color(rgb(0xcdd6f4))
                                .child(body),
                        ),
                ),
        )
    }

    // 启动卡片屏（TablePro 式交互语义，仅借鉴设计）：连接以卡片网格呈现，点击进入工作区。
    fn render_start(&self, cx: &mut Context<Self>) -> Div {
        let cards = self.conns.iter().enumerate().map(|(i, c)| {
            let name = c.name.clone();
            let kind = c.kind.to_string();
            let ro = c.read_only == Some(true);
            let target = c
                .host
                .clone()
                .or_else(|| c.database.clone())
                .unwrap_or_else(|| "—".to_string());
            let port = c.port.map(|p| format!(":{p}")).unwrap_or_default();
            div()
                .id(("conn-card", i))
                .w(px(280.0))
                .flex_none()
                .flex()
                .flex_col()
                .gap(px(8.0))
                .p(px(14.0))
                .rounded(px(10.0))
                .bg(rgb(0x181825))
                .cursor(CursorStyle::PointingHand)
                .child(
                    div()
                        .flex()
                        .items_center()
                        .gap(px(8.0))
                        .child(
                            div()
                                .text_color(rgb(0xcdd6f4))
                                .text_size(px(14.0))
                                .font_weight(FontWeight::BOLD)
                                .truncate()
                                .child(name),
                        )
                        .child(kind_badge(&kind))
                        .when(ro, |d| {
                            d.child(
                                div()
                                    .px(px(6.0))
                                    .py(px(1.0))
                                    .rounded(px(8.0))
                                    .bg(rgb(0x313244))
                                    .text_color(rgb(0xf9e2af))
                                    .text_size(px(10.0))
                                    .child("🛡 只读"),
                            )
                        }),
                )
                .child(
                    div()
                        .text_color(rgb(0x6c7086))
                        .text_size(px(11.0))
                        .font_family("monospace")
                        .child(format!("{target}{port}")),
                )
                .child(
                    div()
                        .text_color(rgb(0x89b4fa))
                        .text_size(px(11.0))
                        .child("进入工作区 →"),
                )
                .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                    this.sel = Some(i);
                    this.workspace = Some(i);
                    this.result = None;
                    this.preview = None;
                    this.ctx_menu = None;
                    this.browse = None;
                    this.status = "Enter 执行 · Shift+Enter 换行 · 右键单元格菜单".into();
                    this.load_tables(cx);
                    this.beat(cx);
                    cx.notify();
                }))
        });
        let empty = if self.conns.is_empty() {
            Some(
                div()
                    .w_full()
                    .py(px(48.0))
                    .flex()
                    .justify_center()
                    .text_color(rgb(0x6c7086))
                    .text_size(px(13.0))
                    .child("暂无连接——请在 Web 端创建后点右上角「刷新」"),
            )
        } else {
            None
        };
        div()
            .size_full()
            .bg(rgb(0x1e1e2e))
            .flex()
            .flex_col()
            .child(
                div()
                    .h(px(48.0))
                    .bg(rgb(0x181825))
                    .flex()
                    .items_center()
                    .px(px(16.0))
                    .gap(px(10.0))
                    .child(
                        div()
                            .text_color(rgb(0xcdd6f4))
                            .text_size(px(16.0))
                            .font_weight(FontWeight::BOLD)
                            .child("PolyDB"),
                    )
                    .child(
                        div()
                            .flex_1()
                            .text_color(rgb(0x6c7086))
                            .text_size(px(12.0))
                            .child("选择连接进入工作区"),
                    )
                    .child(
                        div()
                            .id("start-refresh")
                            .px(px(10.0))
                            .py(px(4.0))
                            .bg(rgb(0x313244))
                            .text_color(rgb(0xcdd6f4))
                            .text_size(px(12.0))
                            .rounded(px(4.0))
                            .cursor(CursorStyle::PointingHand)
                            .child("刷新")
                            .on_click(cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                                this.refresh(cx);
                            })),
                    ),
            )
            .child(
                div()
                    .id("start-scroll")
                    .flex_1()
                    .overflow_y_scroll()
                    .p(px(24.0))
                    .child(div().flex().flex_wrap().gap(px(14.0)).children(cards))
                    .children(empty),
            )
            .child(
                div()
                    .h(px(26.0))
                    .bg(rgb(0x181825))
                    .flex()
                    .items_center()
                    .px(px(12.0))
                    .text_color(status_color(&self.status))
                    .text_size(px(12.0))
                    .child(self.status.clone()),
            )
    }

    // 单元格右键菜单：预览 / 复制内容 / 复制列名（backdrop 全屏拦截点击以关闭）。
    fn render_ctx_menu(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let m = self.ctx_menu?;
        let r = self.result.as_ref()?;
        let col = r.columns.get(m.col)?;
        let v = r.rows.get(m.row)?.get(m.col)?;
        let raw = value_str(v);
        let col_name = col.name.clone();
        let tab = try_pretty_json(v).map_or(PreviewTab::Text, |_| PreviewTab::Json);
        let item = |id: &'static str, label: &'static str| {
            div()
                .id(id)
                .px(px(10.0))
                .py(px(5.0))
                .rounded(px(4.0))
                .cursor(CursorStyle::PointingHand)
                .text_color(rgb(0xcdd6f4))
                .text_size(px(12.0))
                .hover(|d| d.bg(rgb(0x313244)))
                .child(label)
        };
        Some(
            div()
                .id("ctx-backdrop")
                .absolute()
                .top_0()
                .left_0()
                .size_full()
                .on_click(cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                    this.ctx_menu = None;
                    cx.notify();
                }))
                .child(
                    div()
                        .absolute()
                        .top(px(m.y))
                        .left(px(m.x))
                        .min_w(px(180.0))
                        .flex()
                        .flex_col()
                        .gap(px(2.0))
                        .p(px(4.0))
                        .bg(rgb(0x181825))
                        .rounded(px(6.0))
                        .child(
                            div()
                                .px(px(10.0))
                                .pt(px(4.0))
                                .pb(px(6.0))
                                .text_color(rgb(0x6c7086))
                                .text_size(px(10.0))
                                .child(format!("{} · {}", col_name, col.data_type).to_uppercase()),
                        )
                        .child(item("ctx-preview", "🔍 预览单元格").on_click(cx.listener(
                            move |this, _ev: &ClickEvent, _window, cx| {
                                cx.stop_propagation();
                                this.preview = Some(PreviewState {
                                    row: m.row,
                                    col: m.col,
                                    tab,
                                });
                                this.ctx_menu = None;
                                cx.notify();
                            },
                        )))
                        .child(item("ctx-copy", "⧉ 复制单元格内容").on_click(cx.listener(
                            move |this, _ev: &ClickEvent, _window, cx| {
                                cx.stop_propagation();
                                cx.write_to_clipboard(ClipboardItem::new_string(raw.clone()));
                                this.status = "已复制单元格内容".into();
                                this.ctx_menu = None;
                                cx.notify();
                            },
                        )))
                        .child(item("ctx-copy-col", "⧉ 复制列名").on_click(cx.listener(
                            move |this, _ev: &ClickEvent, _window, cx| {
                                cx.stop_propagation();
                                cx.write_to_clipboard(ClipboardItem::new_string(col_name.clone()));
                                this.status = "已复制列名".into();
                                this.ctx_menu = None;
                                cx.notify();
                            },
                        ))),
                )
                .into_any_element(),
        )
    }
}

// 状态栏着色：含「失败」用红色（与 TUI 查询失败着色语义一致）。
fn status_color(s: &str) -> Rgba {
    if s.contains("失败") {
        rgb(0xf38ba8)
    } else {
        rgb(0xa6adc8)
    }
}

// 数据库类型徽章（卡片右上角小圆角标签）。
fn kind_badge(kind: &str) -> Div {
    let fg = match kind {
        "sqlite" => rgb(0xa6e3a1),
        "mysql" => rgb(0x94e2d5),
        "postgres" => rgb(0x89b4fa),
        "mssql" => rgb(0xfab387),
        "oracle" => rgb(0xf9e2af),
        "redis" => rgb(0xf38ba8),
        _ => rgb(0xcdd6f4),
    };
    div()
        .px(px(6.0))
        .py(px(1.0))
        .rounded(px(8.0))
        .bg(rgb(0x313244))
        .text_color(fg)
        .text_size(px(10.0))
        .child(kind.to_string())
}

impl Render for PolyDBApp {
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        if self.workspace.is_none() {
            return self.render_start(cx).into_any_element();
        }
        // ─── 侧栏：连接列表 + 表清单（单击进入分页浏览） ───
        let table_items: Vec<Stateful<Div>> = self
            .tables
            .iter()
            .enumerate()
            .map(|(ti, t)| {
                let name = t.name.clone();
                let schema = t.schema.clone();
                let active = self
                    .browse
                    .as_ref()
                    .is_some_and(|b| b.table == name && b.schema == schema);
                div()
                    .id(("table-item", ti))
                    .w_full()
                    .px(px(12.0))
                    .py(px(5.0))
                    .text_color(if active { rgb(0x89b4fa) } else { rgb(0xcdd6f4) })
                    .text_size(px(12.0))
                    .cursor(CursorStyle::PointingHand)
                    .truncate()
                    .hover(|d| d.bg(rgb(0x26263a)))
                    .child(name.clone())
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                        this.open_browse(schema.clone(), name.clone(), 0, cx);
                    }))
            })
            .collect();
        let tables_section = if self.workspace.is_some() && !table_items.is_empty() {
            div()
                .id("tables-scroll")
                .flex_1()
                .overflow_y_scroll()
                .flex()
                .flex_col()
                .child(
                    div()
                        .px(px(12.0))
                        .pt(px(10.0))
                        .pb(px(4.0))
                        .text_color(rgb(0xa6adc8))
                        .text_size(px(12.0))
                        .child(format!("Tables ({})", table_items.len())),
                )
                .children(table_items)
        } else {
            div().id("tables-empty").flex_1()
        };
        let sidebar =
            div()
                .w(px(250.0))
                .bg(rgb(0x181825))
                .flex()
                .flex_col()
                .child(
                    div()
                        .p(px(12.0))
                        .text_color(rgb(0xa6adc8))
                        .text_size(px(12.0))
                        .child("Connections"),
                )
                .child(div().flex_none().flex().flex_col().children(
                    self.conns.iter().enumerate().map(|(i, c)| {
                        let name = c.name.clone();
                        let kind = c.kind.to_string();
                        let label = format!("{name}  [{kind}]");
                        let selected = self.sel == Some(i);
                        let (bg, fg) = if selected {
                            (rgb(0x313244), rgb(0xcdd6f4))
                        } else {
                            (rgb(0x181825), rgb(0xa6adc8))
                        };
                        div()
                            .id(i)
                            .w_full()
                            .px(px(12.0))
                            .py(px(6.0))
                            .bg(bg)
                            .text_color(fg)
                            .text_size(px(13.0))
                            .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                                this.sel = Some(i);
                                this.status = format!("已选择「{name}」，可按 Enter 执行查询");
                                this.result = None;
                                this.preview = None;
                                this.browse = None;
                                this.load_tables(cx);
                                this.beat(cx);
                                cx.notify();
                            }))
                            .child(label)
                    }),
                ))
                .child(tables_section)
                .child(
                    div()
                        .flex()
                        .gap(px(6.0))
                        .p(px(8.0))
                        .child(
                            div()
                                .id("btn-connect")
                                .px(px(8.0))
                                .py(px(4.0))
                                .bg(rgb(0x313244))
                                .text_color(rgb(0xcdd6f4))
                                .text_size(px(12.0))
                                .rounded(px(4.0))
                                .child("连接")
                                .on_click(cx.listener(|this, _, _window, cx| {
                                    this.connect(cx);
                                })),
                        )
                        .child(
                            div()
                                .id("btn-disconnect")
                                .px(px(8.0))
                                .py(px(4.0))
                                .bg(rgb(0x313244))
                                .text_color(rgb(0xcdd6f4))
                                .text_size(px(12.0))
                                .rounded(px(4.0))
                                .child("断开")
                                .on_click(cx.listener(|this, _, _window, cx| {
                                    this.disconnect(cx);
                                })),
                        )
                        .child(
                            div()
                                .id("btn-refresh")
                                .px(px(8.0))
                                .py(px(4.0))
                                .bg(rgb(0x313244))
                                .text_color(rgb(0xcdd6f4))
                                .text_size(px(12.0))
                                .rounded(px(4.0))
                                .child("刷新")
                                .on_click(cx.listener(|this, _, _window, cx| {
                                    this.refresh(cx);
                                })),
                        ),
                );

        let result_view = match &self.result {
            Some(r) if !r.columns.is_empty() => {
                let header = div()
                    .flex()
                    .gap(px(12.0))
                    .px(px(12.0))
                    .py(px(6.0))
                    .text_color(rgb(0x89b4fa))
                    .text_size(px(12.0))
                    .children(r.columns.iter().map(|c| div().child(c.name.clone())));
                let rows: Vec<Div> = r
                    .rows
                    .iter()
                    .enumerate()
                    .take(MAX_RESULT_ROWS)
                    .map(|(ri, row)| {
                        let sel = self.preview.is_some_and(|p| p.row == ri);
                        div()
                            .flex()
                            .gap(px(12.0))
                            .px(px(12.0))
                            .py(px(4.0))
                            .text_color(rgb(0xcdd6f4))
                            .text_size(px(12.0))
                            .font_family("monospace")
                            .children(row.iter().enumerate().map(|(ci, v)| {
                                let active =
                                    self.preview.is_some_and(|p| p.row == ri && p.col == ci);
                                div()
                                    .id(("cell", ri * 1_048_576 + ci))
                                    .cursor(CursorStyle::PointingHand)
                                    .rounded(px(2.0))
                                    .bg(if active { rgb(0x45475a) } else if sel { rgb(0x26263a) } else { rgb(0x1e1e2e) })
                                    .child(clip(&value_str(v), CELL_CLIP))
                                    .on_click(cx.listener(move |this, ev: &ClickEvent, _window, cx| {
                                        if ev.is_right_click() {
                                            if let Some(p) = ev.mouse_position() {
                                                this.ctx_menu = Some(CellMenuState {
                                                    x: f32::from(p.x),
                                                    y: f32::from(p.y),
                                                    row: ri,
                                                    col: ci,
                                                });
                                            }
                                            cx.notify();
                                            return;
                                        }
                                        this.ctx_menu = None;
                                        let tab = this
                                            .result
                                            .as_ref()
                                            .and_then(|r| r.rows.get(ri).and_then(|row| row.get(ci)))
                                            .and_then(try_pretty_json)
                                            .map_or(PreviewTab::Text, |_| PreviewTab::Json);
                                        this.preview = Some(PreviewState { row: ri, col: ci, tab });
                                        cx.notify();
                                    }))
                            }))
                    })
                    .collect();
                let mut children: Vec<Div> = Vec::with_capacity(rows.len() + 2);
                children.push(header);
                children.extend(rows);
                if r.rows.len() > MAX_RESULT_ROWS {
                    children.push(
                        div()
                            .px(px(12.0))
                            .text_color(rgb(0x6c7086))
                            .text_size(px(11.0))
                            .child(format!(
                                "… 仅显示前 {MAX_RESULT_ROWS} 行（共 {} 行）",
                                r.rows.len()
                            )),
                    );
                }
                div().flex_1().flex().flex_col().children(children)
            }
            Some(r) => div()
                .flex_1()
                .p(px(16.0))
                .text_color(rgb(0x89b4fa))
                .text_size(px(13.0))
                .child(format!("影响行数：{}", r.affected_rows)),
            None => div()
                .flex_1()
                .flex()
                .items_center()
                .justify_center()
                .text_color(rgb(0x6c7086))
                .text_size(px(14.0))
                .child(if self.busy {
                    "执行中…"
                } else {
                    "输入 SQL（Enter 执行，Shift+Enter 换行，Ctrl/Cmd+V 粘贴）；左键预览单元格，右键弹菜单"
                }),
        };

        let sql_lines = self.sql_lines();
        // 浏览工具条：分页信息 + 上/下一页 + 退出浏览。
        let browse_bar = self.browse.as_ref().map(|b| {
            let label = format!(
                "浏览 {}.{} · 行 {}–{}{}{}",
                b.schema,
                b.table,
                b.offset + 1,
                b.offset + b.shown as u64,
                b.total.map(|t| format!(" / 共约 {t}")).unwrap_or_default(),
                if b.has_more { " · 有下一页" } else { "" },
            );
            let (schema, table, limit) = (b.schema.clone(), b.table.clone(), b.limit as u64);
            let btn = |id: &'static str, label: &'static str| {
                div()
                    .id(id)
                    .px(px(8.0))
                    .py(px(3.0))
                    .bg(rgb(0x313244))
                    .text_color(rgb(0xcdd6f4))
                    .text_size(px(12.0))
                    .rounded(px(4.0))
                    .cursor(CursorStyle::PointingHand)
                    .child(label)
            };
            div()
                .flex_none()
                .flex()
                .items_center()
                .gap(px(8.0))
                .px(px(12.0))
                .py(px(6.0))
                .bg(rgb(0x181825))
                .text_color(rgb(0xa6adc8))
                .text_size(px(12.0))
                .child(label)
                .child(btn("browse-prev", "‹ 上一页").on_click(cx.listener({
                    let (schema, table) = (schema.clone(), table.clone());
                    move |this, _ev: &ClickEvent, _window, cx| {
                        let cur = this.browse.as_ref().map(|bb| bb.offset);
                        if let Some(off) = cur.filter(|o| *o > 0) {
                            let prev = off.saturating_sub(limit);
                            this.open_browse(schema.clone(), table.clone(), prev, cx);
                        }
                    }
                })))
                .child(btn("browse-next", "下一页 ›").on_click(cx.listener(
                    move |this, _ev: &ClickEvent, _window, cx| {
                        let next = this
                            .browse
                            .as_ref()
                            .and_then(|bb| bb.has_more.then(|| bb.offset + limit));
                        if let Some(off) = next {
                            this.open_browse(schema.clone(), table.clone(), off, cx);
                        }
                    },
                )))
                .child(div().flex_1())
                .child(btn("browse-exit", "× 退出浏览").on_click(cx.listener(
                    |this, _ev: &ClickEvent, _window, cx| {
                        this.browse = None;
                        this.result = None;
                        this.preview = None;
                        this.status = "已退出浏览".into();
                        cx.notify();
                    },
                )))
        });
        let preview_panel = self.render_preview(cx);
        let ctx_menu = self.render_ctx_menu(cx);
        // 多语句逐条结果清单（✓/✗/⊘）。
        let log_view = (!self.multi_log.is_empty()).then(|| {
            div()
                .flex_none()
                .flex()
                .flex_col()
                .gap(px(2.0))
                .px(px(12.0))
                .py(px(6.0))
                .bg(rgb(0x181825))
                .children(self.multi_log.iter().enumerate().map(|(i, (text, oc))| {
                    let (mark, color) = match oc {
                        StmtOutcome::Ok(n) => {
                            (format!("✓ 第 {} 条 · {n} 行", i + 1), rgb(0xa6e3a1))
                        }
                        StmtOutcome::Err(e) => (format!("✗ 第 {} 条 · {e}", i + 1), rgb(0xf38ba8)),
                        StmtOutcome::Skipped => {
                            (format!("⊘ 第 {} 条 · 未执行", i + 1), rgb(0x6c7086))
                        }
                    };
                    div()
                        .text_size(px(11.0))
                        .font_family("monospace")
                        .text_color(color)
                        .child(format!("{mark} — {}", stmt_preview(text)))
                }))
        });
        // 执行中显示「取消」按钮（多语句尽力取消：当前条跑完、剩余标 ⊘）。
        let cancel_btn = (self.busy && !self.multi_log.is_empty()).then(|| {
            div()
                .id("btn-cancel")
                .px(px(8.0))
                .py(px(3.0))
                .bg(rgb(0x45475a))
                .text_color(rgb(0xf38ba8))
                .text_size(px(12.0))
                .rounded(px(4.0))
                .cursor(CursorStyle::PointingHand)
                .child("■ 取消")
                .on_click(cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                    this.cancel.store(true, Ordering::Relaxed);
                    this.status = "取消中：当前语句将执行完，剩余跳过".into();
                    cx.notify();
                }))
        });
        let conn_title = self
            .sel
            .and_then(|i| self.conns.get(i))
            .map(|c| c.name.clone())
            .unwrap_or_default();
        // 只读连接徽标 🛡（后端 read_only 字段）。
        let ro_badge = self
            .sel
            .and_then(|i| self.conns.get(i))
            .filter(|c| c.read_only == Some(true))
            .map(|_| {
                div()
                    .px(px(6.0))
                    .py(px(1.0))
                    .rounded(px(8.0))
                    .bg(rgb(0x313244))
                    .text_color(rgb(0xf9e2af))
                    .text_size(px(10.0))
                    .child("🛡 只读")
            });
        // 心跳指示灯：● 存活 xxms / ● 失联 原因。
        let hb_chip = self.hb.as_ref().map(|h| {
            div()
                .px(px(6.0))
                .py(px(1.0))
                .rounded(px(8.0))
                .text_size(px(10.0))
                .bg(rgb(0x313244))
                .text_color(if h.ok { rgb(0xa6e3a1) } else { rgb(0xf38ba8) })
                .child(if h.ok {
                    format!("● 存活 {:.1}ms", h.ms)
                } else {
                    format!("● 失联 {}", h.err.clone().unwrap_or_default())
                })
        });
        div()
            .relative()
            .size_full()
            .bg(rgb(0x1e1e2e))
            .flex()
            .flex_col()
            .child(
                div()
                    .h(px(40.0))
                    .bg(rgb(0x181825))
                    .flex()
                    .items_center()
                    .px(px(16.0))
                    .gap(px(10.0))
                    .child(
                        div()
                            .id("btn-back")
                            .px(px(8.0))
                            .py(px(3.0))
                            .bg(rgb(0x313244))
                            .text_color(rgb(0xcdd6f4))
                            .text_size(px(12.0))
                            .rounded(px(4.0))
                            .cursor(CursorStyle::PointingHand)
                            .child("‹ 连接")
                            .on_click(cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                                this.workspace = None;
                                this.sel = None;
                                this.result = None;
                                this.preview = None;
                                this.ctx_menu = None;
                                this.browse = None;
                                this.tables.clear();
                                cx.notify();
                            })),
                    )
                    .child(
                        div()
                            .text_color(rgb(0xcdd6f4))
                            .text_size(px(16.0))
                            .font_weight(FontWeight::BOLD)
                            .child("PolyDB"),
                    )
                    .child(
                        div()
                            .flex_1()
                            .text_color(rgb(0x6c7086))
                            .text_size(px(12.0))
                            .child(conn_title),
                    )
                    .children(ro_badge)
                    .children(hb_chip)
                    .children(cancel_btn),
            )
            .child(
                div().flex_1().flex().child(sidebar).child(
                    div()
                        .flex_1()
                        .flex()
                        .flex_col()
                        // SQL 输入（多行，键盘编辑；Enter 执行 / Shift+Enter 换行）
                        .child(
                            div()
                                .id("sql-input")
                                .min_h(px(36.0))
                                .max_h(px(180.0))
                                .overflow_y_scroll()
                                .flex()
                                .flex_col()
                                .justify_center()
                                .px(px(12.0))
                                .py(px(6.0))
                                .cursor(CursorStyle::IBeam)
                                .track_focus(&self.focus)
                                .on_click(cx.listener(|_, _ev, window, cx| {
                                    window.focus(&cx.focus_handle());
                                }))
                                .on_key_down(cx.listener(|this, ev: &KeyDownEvent, _window, cx| {
                                    this.on_input_key(ev, cx);
                                }))
                                .text_size(px(13.0))
                                .font_family("monospace")
                                .text_color(rgb(0xcdd6f4))
                                .children(sql_lines.into_iter().map(|line| {
                                    div().child(if line.is_empty() {
                                        " ".to_string()
                                    } else {
                                        line
                                    })
                                })),
                        )
                        // 状态栏
                        .child(
                            div()
                                .h(px(26.0))
                                .bg(rgb(0x181825))
                                .px(px(12.0))
                                .flex()
                                .items_center()
                                .text_color(status_color(&self.status))
                                .text_size(px(12.0))
                                .child(self.status.clone()),
                        )
                        .children(browse_bar)
                        .children(log_view)
                        .child(result_view)
                        .children(preview_panel),
                ),
            )
            .children(ctx_menu)
            .into_any_element()
    }
}

pub fn run(transport: Arc<dyn Transport>) {
    let app = Application::new();
    app.run(move |cx: &mut App| {
        let bounds = Bounds::centered(
            None,
            Size {
                width: px(1200.0),
                height: px(800.0),
            },
            cx,
        );
        cx.open_window(
            WindowOptions {
                window_bounds: Some(WindowBounds::Windowed(bounds)),
                titlebar: Some(TitlebarOptions {
                    title: Some("PolyDB".into()),
                    ..Default::default()
                }),
                ..Default::default()
            },
            move |_window, cx| cx.new(|cx| PolyDBApp::new(transport, cx)),
        )
        .unwrap();
    });
}

fn rgb(hex: u32) -> Rgba {
    Rgba {
        r: ((hex >> 16) & 0xFF) as f32 / 255.0,
        g: ((hex >> 8) & 0xFF) as f32 / 255.0,
        b: (hex & 0xFF) as f32 / 255.0,
        a: 1.0,
    }
}

#[cfg(test)]
mod tests {
    use super::split_statements;

    fn split(s: &str) -> Vec<String> {
        split_statements(s)
    }

    #[test]
    fn split_basic_and_trailing() {
        assert_eq!(split("SELECT 1; SELECT 2;"), ["SELECT 1", "SELECT 2"]);
        assert_eq!(split("SELECT 1"), ["SELECT 1"]);
        assert_eq!(split("  ;; ;"), Vec::<String>::new());
    }

    #[test]
    fn split_respects_strings() {
        assert_eq!(
            split("SELECT 'a;b', \"c;d\", `e;f`; SELECT 2"),
            ["SELECT 'a;b', \"c;d\", `e;f`", "SELECT 2"]
        );
        // 双写转义 + 反斜杠转义：引号内的 '' 与 \' 不闭合字符串。
        assert_eq!(
            split("SELECT 'it''s;ok'; SELECT 2"),
            ["SELECT 'it''s;ok'", "SELECT 2"]
        );
        assert_eq!(
            split("SELECT 'a\\';b'; SELECT 2"),
            ["SELECT 'a\\';b'", "SELECT 2"]
        );
    }

    #[test]
    fn split_respects_comments() {
        assert_eq!(
            split("-- up; front\nSELECT 1; /* mid; */ SELECT 2;"),
            ["-- up; front\nSELECT 1", "/* mid; */ SELECT 2"]
        );
        // 纯注释不构成语句；# 行注释（MySQL 风格）。
        assert_eq!(split("-- only a comment;"), Vec::<String>::new());
        assert_eq!(split("SELECT 1; # tail; cmt"), ["SELECT 1"]);
    }
}
