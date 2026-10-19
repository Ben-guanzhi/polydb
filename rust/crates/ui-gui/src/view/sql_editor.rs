//! SQL 编辑器组件（polydb 版）。
//!
//! 对齐 dbstudio 的 `sql_editor.rs` 设计，但基于 gpui-component 0.6.6 的
//! `EditorState`（即 `gpui_base::input::InputBaseState<EditorMode>`）自建，
//! 不 1:1 复制 dbstudio 的 `AppState` 耦合。
//!
//! 能力：
//! - 真·多行代码编辑器（语法高亮 / 行号 / 选区 / 撤销，来自 gpui-component）。
//! - Vim 模式（普通/插入/可视），由已搬迁的 `polydb_vim::VimBuf` 驱动。
//! - Schema 感知补全：SQL 关键字 + 当前连接表名/列名。
//!
//! 与 polydb 现有 `self.sql` 状态机的关系：本组件是编辑器的事实来源；
//! 文本变更通过 `TextChanged(String)` 事件回写 `self.sql`（run_query / explain
//! 等仍读 `self.sql`，无需改动）。`RunQuery(String)` 触发执行。

use std::rc::Rc;

use gpui::prelude::*;
use gpui::{
    div, App, ClickEvent, Context, Entity, EventEmitter, IntoElement, KeyDownEvent, Render,
    SharedString, Subscription, Window,
};
use gpui_component::{
    button::{Button, ButtonVariants as _},
    h_flex,
    input::{CompletionProvider, Editor, EditorState, Enter, Rope},
    v_flex, ActiveTheme as _, Disableable as _, Sizable as _,
};
use lsp_types::{
    CompletionContext, CompletionItem, CompletionItemKind, CompletionResponse,
};
use polydb_vim::{VimBuf, VimMode, VimStep};

/// 编辑器向宿主（`PolyDBApp`）发出的事件。
pub enum SqlEditorEvent {
    /// 编辑器文本内容变化；负载为完整 SQL 文本。
    TextChanged(String),
    /// 请求执行查询；负载为待执行的 SQL（选区优先，否则全文）。
    RunQuery(String),
}

impl EventEmitter<SqlEditorEvent> for SqlEditor {}

/// 补全供给：表名 → 列名列表。由 `PolyDBApp` 在 schema 加载后通过
/// `set_schema` 注入。
pub type SchemaSupplier = Rc<dyn Fn() -> Vec<(String, Vec<String>)>>;

const SQL_KEYWORDS: &[&str] = &[
    "SELECT", "FROM", "WHERE", "INSERT", "INTO", "VALUES", "UPDATE", "SET", "DELETE", "CREATE",
    "TABLE", "DROP", "ALTER", "ADD", "COLUMN", "INDEX", "VIEW", "DISTINCT", "AS", "ON", "JOIN",
    "INNER", "LEFT", "RIGHT", "FULL", "OUTER", "CROSS", "GROUP", "BY", "ORDER", "HAVING", "LIMIT",
    "OFFSET", "ASC", "DESC", "AND", "OR", "NOT", "NULL", "IS", "IN", "EXISTS", "BETWEEN", "LIKE",
    "ILIKE", "CASE", "WHEN", "THEN", "ELSE", "END", "UNION", "ALL", "WITH", "PRIMARY", "KEY",
    "FOREIGN", "REFERENCES", "DEFAULT", "UNIQUE", "CHECK", "CONSTRAINT", "IF", "NULLS", "FIRST",
    "LAST", "TRUE", "FALSE", "NULL", "CAST", "COALESCE", "COUNT", "SUM", "AVG", "MIN", "MAX",
];

/// 基于关键字 + 连接 schema 的 SQL 补全提供器。
struct SqlCompletionProvider {
    schema: Rc<std::cell::RefCell<Vec<(String, Vec<String>)>>>,
}

impl CompletionProvider for SqlCompletionProvider {
    fn completions(
        &self,
        _rope: &Rope,
        _offset: usize,
        _trigger: CompletionContext,
        _window: &mut Window,
        _cx: &mut App,
    ) -> gpui::Task<anyhow::Result<CompletionResponse>> {
        let mut items: Vec<CompletionItem> = SQL_KEYWORDS
            .iter()
            .map(|kw| CompletionItem {
                label: kw.to_string(),
                kind: Some(CompletionItemKind::KEYWORD),
                detail: Some("keyword".into()),
                ..Default::default()
            })
            .collect();

        for (table, cols) in self.schema.borrow().iter() {
            items.push(CompletionItem {
                label: table.clone(),
                kind: Some(CompletionItemKind::CLASS),
                detail: Some("table".into()),
                ..Default::default()
            });
            for c in cols {
                items.push(CompletionItem {
                    label: c.clone(),
                    kind: Some(CompletionItemKind::FIELD),
                    detail: Some(format!("column of {table}")),
                    ..Default::default()
                });
            }
        }

        gpui::Task::ready(Ok(CompletionResponse::Array(items)))
    }

    fn is_completion_trigger(&self, _offset: usize, _new_text: &str, _cx: &mut App) -> bool {
        true
    }
}

pub struct SqlEditor {
    input_state: Entity<EditorState>,
    vim_on: bool,
    vim: VimBuf,
    schema: Rc<std::cell::RefCell<Vec<(String, Vec<String>)>>>,
    has_connection: bool,
    _subs: Subscription,
}

impl SqlEditor {
    pub fn view(window: &mut Window, cx: &mut App) -> Entity<Self> {
        cx.new(|cx| Self::new(window, cx))
    }

    fn new(window: &mut Window, cx: &mut Context<Self>) -> Self {
        let schema = Rc::new(std::cell::RefCell::new(Vec::new()));
        let provider = SqlCompletionProvider {
            schema: schema.clone(),
        };
        let input_state = cx.new(|cx| EditorState::new(window, cx).language("sql"));
        input_state.update(cx, |s, cx| {
            s.lsp_mut().completion_provider = Some(Rc::new(provider));
            cx.notify();
        });

        let _subs = cx.subscribe(&input_state, |this: &mut Self, _e, _window, cx| {
            let text = this.input_state.read(cx).value().to_string();
            cx.emit(SqlEditorEvent::TextChanged(text));
        });

        Self {
            input_state,
            vim_on: false,
            vim: VimBuf::default(),
            schema,
            has_connection: false,
            _subs,
        }
    }

    /// 由 `PolyDBApp` 在 schema 加载后注入表/列清单。
    pub fn set_schema(&mut self, tables: Vec<(String, Vec<String>)>) {
        *self.schema.borrow_mut() = tables;
    }

    /// 由 `PolyDBApp` 在连接状态变化时调用，控制工具栏按钮可用性。
    pub fn set_has_connection(&mut self, has: bool) {
        self.has_connection = has;
    }

    /// 外部设置编辑器文本（如 Quick Switcher / 历史回填）。
    pub fn set_query(&mut self, query: &str, window: &mut Window, cx: &mut Context<Self>) {
        self.input_state.update(cx, |s, cx| {
            s.set_value(SharedString::from(query), window, cx);
            cx.notify();
        });
    }

    /// 切换 Vim 模式。
    pub fn toggle_vim(&mut self) {
        self.vim_on = !self.vim_on;
        if !self.vim_on {
            self.vim.mode = VimMode::Normal;
            self.vim.reset();
        }
    }

    fn query_to_run(&self, cx: &Context<Self>) -> String {
        let state = self.input_state.read(cx);
        let sel = state.selected_range();
        let full = state.value().to_string();
        if sel.start < sel.end && sel.end <= full.len() {
            full[sel.start..sel.end].to_string()
        } else {
            full
        }
    }

    /// Vim 键位桥（移植自 dbstudio 的 `vim_handle_key`）。
    fn vim_handle_key(
        &mut self,
        event: &KeyDownEvent,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> bool {
        let modifiers = event.keystroke.modifiers;
        let key = event.keystroke.key.to_lowercase();
        let ctrl = modifiers.control;
        let shift = modifiers.shift;
        let alt = modifiers.alt;

        {
            let state = self.input_state.read(cx);
            let text = state.value().to_string();
            let caret = state.selected_range().start;
            self.vim.sync(&text, caret);
        }

        let Some(step) = self.vim.step(&key, ctrl, shift, alt) else {
            return false; // 交还原生编辑器（插入态输入等）。
        };
        self.apply_vim_step(step, window, cx);
        true
    }

    fn apply_vim_step(&mut self, step: VimStep, window: &mut Window, cx: &mut Context<Self>) {
        let entity = self.input_state.clone();
        entity.update(cx, |s, cx| {
            if let Some(text) = &step.text {
                s.set_value(SharedString::from(text.clone()), window, cx);
            }
            match step.selection {
                Some((lo, hi)) if lo < hi => s.set_selected_range(lo..hi, cx),
                _ => {
                    let c = step.caret.min(s.value().len());
                    s.set_selected_range(c..c, cx);
                }
            }
            if step.to_insert {
                s.focus(window, cx);
            }
            cx.notify();
        });
        cx.notify();
    }
}

impl Render for SqlEditor {
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let has = self.has_connection;

        let execute = Button::new("editor-execute")
            .label("执行")
            .small()
            .primary()
            .ghost()
            .tooltip("Execute (Ctrl-Enter)")
            .disabled(!has)
            .on_click(cx.listener(|this, _: &ClickEvent, _window, cx| {
                let q = this.query_to_run(cx);
                if !q.trim().is_empty() {
                    cx.emit(SqlEditorEvent::RunQuery(q));
                }
            }));

        let vim_toggle = Button::new("editor-vim")
            .label(if self.vim_on { "Vim:开" } else { "Vim:关" })
            .small()
            .ghost()
            .tooltip(if self.vim_on {
                "Vim: ON (click to disable)"
            } else {
                "Vim: OFF (click to enable)"
            })
            .on_click(cx.listener(|this, _: &ClickEvent, _w, _cx| {
                this.toggle_vim();
            }));

        let toolbar = h_flex()
            .id("sql-editor-toolbar")
            .justify_between()
            .items_center()
            .px_2()
            .py_1()
            .child(div())
            .child(
                h_flex()
                    .items_center()
                    .gap_1()
                    .child(vim_toggle)
                    .child(execute),
            );

        let editor = Editor::new(&self.input_state).w_full().h_full();

        v_flex()
            .id("sql-editor")
            .size_full()
            .when(self.vim_on, |el| {
                el.capture_key_down(cx.listener(
                    |this, event: &KeyDownEvent, window, cx| {
                        if this.vim_handle_key(event, window, cx) {
                            cx.stop_propagation();
                        }
                    },
                ))
            })
            .capture_action(cx.listener(
                |this, action: &Enter, _window: &mut Window, cx: &mut Context<Self>| {
                    if action.secondary {
                        cx.stop_propagation();
                        let q = this.query_to_run(cx);
                        if !q.trim().is_empty() {
                            cx.emit(SqlEditorEvent::RunQuery(q));
                        }
                    }
                },
            ))
            .child(toolbar)
            .child(
                div()
                    .id("editor-content")
                    .bg(cx.theme().background)
                    .w_full()
                    .flex_1()
                    .px_2()
                    .pb_2()
                    .child(editor),
            )
    }
}
