//! polydb-ui-gui：GPUI 桌面前端（最小可用版）。
//!
//! 铁律（AGENTS.md §2）：前端只通过 `transport`（进程内 LocalTransport）访问数据，
//! 不依赖任何 driver crate。布局：左侧连接列表 + 操作按钮，右侧 SQL 输入、
//! 状态栏与结果表格（单行输入，ASCII 键盘编辑 + Ctrl/Cmd+V 粘贴，Enter 执行）。

use std::future::Future;
use std::sync::Arc;

use gpui::*;
use polydb_core::{ConnectionId, ConnectionInfo, QueryResult, Value};
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
    io: IoBridge,
    conns: Vec<ConnectionInfo>,
    sel: Option<usize>,
    busy: bool,
    status: String,
    sql: String,
    cursor: usize,
    focus: FocusHandle,
    result: Option<QueryResult>,
}

// 结果表格最多渲染的行数（超出显示提示，与 TUI 行为一致）。
const MAX_RESULT_ROWS: usize = 200;

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

impl PolyDBApp {
    pub fn new(transport: Arc<dyn Transport>, cx: &mut Context<Self>) -> Self {
        let mut this = Self {
            transport,
            io: IoBridge::new(),
            conns: Vec::new(),
            sel: None,
            busy: false,
            status: "加载中…".into(),
            sql: String::new(),
            cursor: 0,
            focus: cx.focus_handle(),
            result: None,
        };
        this.refresh(cx);
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
                        this.sel = None;
                        this.conns = conns;
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
        self.busy = true;
        self.result = None;
        cx.notify();

        let t = Arc::clone(&self.transport);
        let join = self.io.spawn(async move { t.execute(id, &sql, &[]).await });
        cx.spawn(async move |this, cx| {
            let out = join.await;
            let _ = this.update(cx, |this, cx| {
                match out {
                    Ok(Ok(r)) => {
                        this.busy = false;
                        let rows = r.rows.len();
                        this.status =
                            format!("执行完成：{rows} 行，耗时 {:.1}ms", r.execution_time_ms);
                        this.result = Some(r);
                    }
                    Ok(Err(e)) => {
                        this.busy = false;
                        this.result = None;
                        this.status = format!("查询失败: {e}");
                    }
                    Err(e) => {
                        this.busy = false;
                        this.status = format!("IO 任务失败: {e}");
                    }
                }
                cx.notify();
            });
        })
        .detach();
    }

    // ─── 单行文本编辑（键盘驱动，cursor 为字节下标） ───

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

    fn on_input_key(&mut self, ev: &KeyDownEvent, cx: &mut Context<Self>) {
        let ks = &ev.keystroke;
        if ks.modifiers.control || ks.modifiers.platform {
            if ks.key == "v" {
                if let Some(item) = cx.read_from_clipboard() {
                    if let Some(text) = item.text() {
                        self.insert_text(&text);
                    }
                }
            }
            return;
        }
        match ks.key.as_str() {
            "enter" => {
                self.run_query(cx);
                return;
            }
            "backspace" => self.backspace(),
            "delete" => self.delete_forward(),
            "left" => self.move_left(),
            "right" => self.move_right(),
            "home" => self.cursor = 0,
            "end" => self.cursor = self.sql.len(),
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

    // 渲染 SQL 输入框：在 cursor 处插入光标字符（单行编辑器，光标恒显）。
    fn sql_input_text(&self) -> String {
        let caret = "▏";
        let at = char_ceil(&self.sql, self.cursor);
        let mut out = self.sql[..at].to_string();
        out.push_str(caret);
        out.push_str(&self.sql[at..]);
        out
    }
}

impl Render for PolyDBApp {
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        // ─── 侧栏：连接列表 + 操作按钮 ───
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
                .child(div().flex_1().flex().flex_col().children(
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
                                cx.notify();
                            }))
                            .child(label)
                    }),
                ))
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
                    .take(MAX_RESULT_ROWS)
                    .map(|row| {
                        div()
                            .flex()
                            .gap(px(12.0))
                            .px(px(12.0))
                            .py(px(4.0))
                            .text_color(rgb(0xcdd6f4))
                            .text_size(px(12.0))
                            .font_family("monospace")
                            .children(row.iter().map(|v| div().child(value_str(v))))
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
                    "选择连接并输入 SQL（Enter 执行，←/→ 移动光标，Ctrl/Cmd+V 粘贴）"
                }),
        };

        let sql_text = self.sql_input_text();
        div()
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
                    .child(
                        div()
                            .text_color(rgb(0xcdd6f4))
                            .text_size(px(16.0))
                            .font_weight(FontWeight::BOLD)
                            .child("PolyDB"),
                    ),
            )
            .child(
                div().flex_1().flex().child(sidebar).child(
                    div()
                        .flex_1()
                        .flex()
                        .flex_col()
                        // SQL 输入（单行，键盘编辑）
                        .child(
                            div()
                                .id("sql-input")
                                .h(px(36.0))
                                .flex()
                                .items_center()
                                .px(px(12.0))
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
                                .child(sql_text.clone()),
                        )
                        // 状态栏
                        .child(
                            div()
                                .h(px(26.0))
                                .bg(rgb(0x181825))
                                .px(px(12.0))
                                .flex()
                                .items_center()
                                .text_color(rgb(0xa6adc8))
                                .text_size(px(12.0))
                                .child(self.status.clone()),
                        )
                        .child(result_view),
                ),
            )
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
