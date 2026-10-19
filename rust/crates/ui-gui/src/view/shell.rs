//! 窗口骨架视图：状态栏 + 顶栏 + 左栏 + 主区分派 + 多语句日志。

use super::*;
use polydb_core::TableInfo;

impl PolyDBApp {
    pub(super) fn status_bar(&self) -> Div {
        let th = &self.theme;
        div()
            .h(px(26.0))
            .flex_none()
            .bg(th.bg_alt)
            .flex()
            .items_center()
            .px(px(12.0))
            .text_color(status_color(&self.status, th))
            .text_size(px(12.0))
            .child(self.status.clone())
    }

    // ─── 主界面：顶栏 + 左栏 + 右栏 ───

    pub(super) fn render_root(&mut self, window: &mut Window, cx: &mut Context<Self>) -> AnyElement {
        let th = &self.theme;
        let root_bg = th.bg;
        let top_bar = self.render_top_bar(cx);
        let sidebar = self.render_sidebar(window, cx);
        let main = self.render_main(window, cx);
        // AI 助手抽屉：懒创建，开关时挂到主列右侧。
        let ai_panel = if self.ai_open {
            if self.ai_panel.is_none() {
                let app = cx.entity();
                self.ai_panel = Some(crate::view::ai_panel::AiPanel::view(window, cx, app));
            }
            self.ai_panel.clone()
        } else {
            None
        };
        let ctx_menu = self.render_ctx_menu(cx);
        let conn_modal = self.render_conn_form(cx);
        let quick_modal = self.render_quick_switcher(cx);
        let explain_modal = self.render_explain_modal(cx);
        let compare_modal = self.render_compare_modal(cx);
        div()
            .relative()
            .size_full()
            .bg(root_bg)
            .flex()
            .flex_col()
            .child(top_bar)
            .child(
                div()
                    .flex_1()
                    .min_h(px(0.0))
                    .flex()
                    .child(sidebar)
                    .child(main)
                    .when_some(ai_panel, |d, ap| d.child(ap.into_element())),
            )
            .children(ctx_menu)
            .children(conn_modal)
            .children(quick_modal)
            .children(explain_modal)
            .children(compare_modal)
            .into_any_element()
    }

    pub(super) fn render_top_bar(&self, cx: &mut Context<Self>) -> Div {
        let th = &self.theme;
        let conn = self.workspace.and_then(|i| self.conns.get(i));
        let title = conn.map(|c| c.name.clone()).unwrap_or_default();
        let kind = conn.map(|c| c.kind.to_string()).unwrap_or_default();
        let ro_badge = conn
            .filter(|c| c.read_only == Some(true))
            .map(|_| badge("🛡 只读", th.warn, th));
        let hb_chip = self.hb.as_ref().map(|h| {
            badge(
                if h.ok {
                    format!("● 存活 {:.1}ms", h.ms)
                } else {
                    format!("● 失联 {}", h.err.clone().unwrap_or_default())
                },
                if h.ok { th.ok } else { th.danger },
                th,
            )
        });
        let cancel_btn = (self.busy && !self.multi_log.is_empty()).then(|| {
            div()
                .id("btn-cancel")
                .px(px(8.0))
                .py(px(3.0))
                .bg(th.surface2)
                .text_color(th.danger)
                .text_size(px(12.0))
                .rounded(px(4.0))
                .cursor(CursorStyle::PointingHand)
                .child("■ 取消")
                .on_click(cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                    this.request_cancel(cx);
                }))
        });
        // D3：SQL 连接才有权看执行计划（Redis 无）。
        let explain_btn = (!kind.is_empty() && !self.is_redis()).then(|| {
            btn("btn-explain", "⚙ 执行计划", th).on_click(cx.listener(
                |this, _ev: &ClickEvent, _window, cx| {
                    this.run_explain(cx);
                },
            ))
        });
        // E5.5：结构同步向导（同样只对「有模式概念」的连接开放入口）。
        let cmp_btn = (!kind.is_empty() && !self.is_redis()).then(|| {
            btn("btn-compare", "⇄ 结构同步", th).on_click(cx.listener(
                |this, _ev: &ClickEvent, _window, cx| {
                    this.open_compare(cx);
                },
            ))
        });
        div()
            .h(px(40.0))
            .flex_none()
            .bg(th.bg_alt)
            .flex()
            .items_center()
            .px(px(16.0))
            .gap(px(10.0))
            .child(
                div()
                    .text_color(th.fg)
                    .text_size(px(16.0))
                    .font_weight(FontWeight::BOLD)
                    .child("PolyDB"),
            )
            .child(
                div()
                    .flex_1()
                    .truncate()
                    .text_color(th.fg_dim)
                    .text_size(px(12.0))
                    .child(if title.is_empty() {
                        "未选择连接".to_string()
                    } else {
                        title.clone()
                    }),
            )
            .when(!kind.is_empty(), |d| {
                d.child(badge(kind.clone(), th.kind_color(&kind), th))
            })
            .children(ro_badge)
            .children(hb_chip)
            .children(cancel_btn)
            .children(explain_btn)
            .children(cmp_btn)
            .child(btn("btn-ai", "✨ AI", th).on_click(cx.listener(
                |this, _ev: &ClickEvent, _window, cx| {
                    this.toggle_ai(cx);
                },
            )))
            .child(btn("theme-ws", "🌗", th).on_click(cx.listener(
                |this, _ev: &ClickEvent, _window, cx| {
                    this.toggle_theme();
                    cx.notify();
                },
            )))
    }

    // ─── 侧栏：连接 + schema chips + 表树 ───

    pub(super) fn render_sidebar(&mut self, window: &mut Window, cx: &mut Context<Self>) -> Div {
        let th = &self.theme;
        let sidebar_bg = th.bg_alt;
        // 连接列表（点击=切换活动连接；展示名称 + 类型 + 目标地址）。
        let conn_rows: Vec<Stateful<Div>> = self
            .conns
            .iter()
            .enumerate()
            .map(|(i, c)| {
                let name = c.name.clone();
                let kind = c.kind.to_string();
                let ro = c.read_only == Some(true);
                let cid = c.id;
                let target = c
                    .host
                    .clone()
                    .or_else(|| c.database.clone())
                    .unwrap_or_else(|| "—".to_string());
                let port = c.port.map(|p| format!(":{p}")).unwrap_or_default();
                let active = self.workspace == Some(i);
                let dot = th.kind_color(&kind);
                let icon = |id: &'static str, glyph: &'static str, hover: gpui::Rgba| {
                    div()
                        .id((id, i))
                        .px(px(3.0))
                        .text_color(th.fg_dim)
                        .text_size(px(11.0))
                        .flex_none()
                        .cursor(CursorStyle::PointingHand)
                        .hover(move |d| d.text_color(hover))
                        .child(glyph)
                };
                div()
                    .id(("side-conn", i))
                    .w_full()
                    .px(px(12.0))
                    .py(px(6.0))
                    .flex()
                    .items_center()
                    .gap(px(8.0))
                    .rounded(px(4.0))
                    .bg(if active { th.surface } else { sidebar_bg })
                    .text_color(if active { th.fg } else { th.fg_muted })
                    .text_size(px(13.0))
                    .cursor(CursorStyle::PointingHand)
                    .hover(|d| d.bg(th.hover))
                    .child(div().size(px(8.0)).flex_none().rounded(px(4.0)).bg(dot))
                    .child(
                        div()
                            .flex_1()
                            .min_w(px(0.0))
                            .flex()
                            .flex_col()
                            .child(div().truncate().font_weight(FontWeight::BOLD).child(name))
                            .child(
                                div()
                                    .truncate()
                                    .text_size(px(10.0))
                                    .text_color(th.fg_dim)
                                    .font_family("monospace")
                                    .child(format!("{kind} · {target}{port}")),
                            ),
                    )
                    .when(ro, |d| {
                        d.child(div().text_color(th.warn).text_size(px(10.0)).child("🛡"))
                    })
                    .child(icon("conn-edit", "✎", th.accent).on_click(cx.listener(
                        move |this, _ev: &ClickEvent, window, cx| {
                            cx.stop_propagation();
                            this.open_conn_edit(cid, window, cx);
                        },
                    )))
                    .child(icon("conn-test", "⟳", th.ok).on_click(cx.listener(
                        move |this, _ev: &ClickEvent, _window, cx| {
                            cx.stop_propagation();
                            this.test_conn(cid, cx);
                        },
                    )))
                    .child(icon("conn-del", "🗑", th.danger).on_click(cx.listener(
                        move |this, _ev: &ClickEvent, _window, cx| {
                            cx.stop_propagation();
                            this.delete_conn(cid, cx);
                        },
                    )))
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                        this.enter_workspace(i, cx);
                    }))
            })
            .collect();
        // schema 切换 chips（多于一个才显示）。
        let multi_schema = self.schemas.len() > 1;
        let schema_chips = multi_schema.then(|| {
            let names: Vec<(usize, String)> = self
                .schemas
                .iter()
                .enumerate()
                .map(|(si, s)| (si, s.name.clone()))
                .collect();
            let active = self.active_schema.clone();
            div()
                .flex_none()
                .flex()
                .flex_wrap()
                .gap(px(4.0))
                .px(px(12.0))
                .py(px(6.0))
                .children(names.into_iter().map(|(si, n)| {
                    let is_active = n == active;
                    div()
                        .id(("schema-chip", si))
                        .px(px(8.0))
                        .py(px(2.0))
                        .rounded(px(8.0))
                        .text_size(px(11.0))
                        .cursor(CursorStyle::PointingHand)
                        .bg(if is_active { th.surface2 } else { th.surface })
                        .text_color(if is_active { th.accent } else { th.fg_muted })
                        .child(n.clone())
                        .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                            this.select_schema(n.clone(), cx);
                        }))
                }))
        });
        // 对象树：懒创建 gpui-component Tree 组件。
        if self.tables_tree.is_none() {
            let app = cx.entity();
            self.tables_tree = Some(crate::view::tables_tree::TablesTree::view(window, cx, app));
        }
        let tree_view = self.tables_tree.clone();
        div()
            .w(px(260.0))
            .flex_none()
            .bg(sidebar_bg)
            .flex()
            .flex_col()
            .child(
                div()
                    .flex_none()
                    .flex()
                    .items_center()
                    .justify_between()
                    .px(px(12.0))
                    .py(px(8.0))
                    .child(
                        div()
                            .text_color(th.fg_muted)
                            .text_size(px(12.0))
                            .child("Connections"),
                    )
                    .child(btn("btn-conn-new", "＋ 新建", th).on_click(cx.listener(
                        |this, _ev: &ClickEvent, window, cx| {
                            this.open_conn_new(window, cx);
                        },
                    ))),
            )
            .child(
                div()
                    .flex_none()
                    .max_h(px(160.0))
                    .id("side-conns")
                    .overflow_y_scroll()
                    .px(px(6.0))
                    .flex()
                    .flex_col()
                    .children(conn_rows),
            )
            .when_some(schema_chips, |d, sc| d.child(sc))
            .when_some(tree_view, |d, tv| d.child(tv.into_element()))
            .child(
                div()
                    .flex_none()
                    .flex()
                    .gap(px(6.0))
                    .p(px(8.0))
                    .child(
                        btn("btn-connect", "连接", th)
                            .on_click(cx.listener(|this, _, _window, cx| this.connect(cx))),
                    )
                    .child(
                        btn("btn-disconnect", "断开", th)
                            .on_click(cx.listener(|this, _, _window, cx| this.disconnect(cx))),
                    )
                    .child(
                        btn("btn-refresh", "刷新", th)
                            .on_click(cx.listener(|this, _, _window, cx| this.refresh(cx))),
                    ),
            )
    }


    // ─── 主列：对象 tab 条 + 活动视图 ───

    pub(super) fn render_main(&mut self, window: &mut Window, cx: &mut Context<Self>) -> Div {
        let th = &self.theme;
        let strip_bg = th.bg_alt;
        // tab 条：工作台常驻 + 表 tabs（× 关闭）。
        let tab_count = self.tabs.len();
        let mut tab_items: Vec<AnyElement> = Vec::with_capacity(tab_count + 1);
        {
            let active = matches!(self.active, ActiveView::Workbench);
            let bg = if active { th.bg } else { strip_bg };
            let fg = if active { th.fg } else { th.fg_muted };
            tab_items.push(
                div()
                    .id("tab-workbench")
                    .px(px(12.0))
                    .py(px(6.0))
                    .flex()
                    .items_center()
                    .gap(px(6.0))
                    .rounded_t(px(6.0))
                    .bg(bg)
                    .text_color(fg)
                    .text_size(px(12.0))
                    .cursor(CursorStyle::PointingHand)
                    .child("⌂ 工作台")
                    .on_click(cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                        this.active = ActiveView::Workbench;
                        cx.notify();
                    }))
                    .into_any_element(),
            );
        }
        for i in 0..tab_count {
            let th = &self.theme;
            let (label, active) = {
                let tab = &self.tabs[i];
                (
                    format!("▣ {}.{}", tab.schema, tab.table),
                    matches!(self.active, ActiveView::Table(a) if a == i),
                )
            };
            let bg = if active { th.bg } else { strip_bg };
            let fg = if active { th.fg } else { th.fg_muted };
            tab_items.push(
                div()
                    .id(("tab-table", i))
                    .px(px(12.0))
                    .py(px(6.0))
                    .flex()
                    .items_center()
                    .gap(px(6.0))
                    .rounded_t(px(6.0))
                    .bg(bg)
                    .text_color(fg)
                    .text_size(px(12.0))
                    .truncate()
                    .max_w(px(240.0))
                    .child(label)
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                        this.active = ActiveView::Table(i);
                        this.preview = None;
                        this.ctx_menu = None;
                        cx.notify();
                    }))
                    .child(
                        div()
                            .id(("tab-close", i))
                            .px(px(4.0))
                            .text_color(th.fg_dim)
                            .cursor(CursorStyle::PointingHand)
                            .hover(|d| d.text_color(th.danger))
                            .child("×")
                            .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                                cx.stop_propagation();
                                this.close_table_tab(i, cx);
                                cx.notify();
                            })),
                    )
                    .into_any_element(),
            );
        }
        let body: AnyElement = match self.active {
            ActiveView::Workbench => {
                if self.is_redis() {
                    self.render_redis_panel(cx)
                } else {
                    self.render_workbench(window, cx)
                }
            }
            ActiveView::Table(i) if i < self.tabs.len() => self.render_table_tab(i, cx),
            _ => {
                self.active = ActiveView::Workbench;
                if self.is_redis() {
                    self.render_redis_panel(cx)
                } else {
                    self.render_workbench(window, cx)
                }
            }
        };
        div()
            .flex_1()
            .min_w(px(0.0))
            .flex()
            .flex_col()
            .child(
                div()
                    .h(px(34.0))
                    .flex_none()
                    .bg(strip_bg)
                    .flex()
                    .items_end()
                    .px(px(8.0))
                    .gap(px(2.0))
                    .overflow_x_hidden()
                    .children(tab_items),
            )
            .child(body)
            .child(self.status_bar())
    }

    // ─── 工作台视图 ───

    pub(super) fn render_workbench(&mut self, window: &mut Window, cx: &mut Context<Self>) -> AnyElement {
        let log_view = self.render_multi_log();
        let grid = self.render_grid(GridSrc::Workbench, Some(MAX_RESULT_ROWS), None, cx);
        let preview_panel = self.render_preview(GridSrc::Workbench, cx);
        let th = self.theme;

        // 懒创建真·SQL 编辑器组件（需要 Window 才能构造 EditorState）。
        if self.sql_editor.is_none() {
            let editor = crate::view::sql_editor::SqlEditor::view(window, cx);
            // 首次注入当前 schema（表名；列名后续由 TableDetail 回填）。
            Self::feed_editor_schema(cx, &editor, &self.tables);
            editor.update(cx, |e, _cx| e.set_has_connection(self.workspace.is_some()));
            let sub = cx.subscribe(&editor, |this: &mut Self, _entity, event: &crate::view::sql_editor::SqlEditorEvent, cx| {
                match event {
                    crate::view::sql_editor::SqlEditorEvent::TextChanged(text) => {
                        this.sql = text.clone();
                    }
                    crate::view::sql_editor::SqlEditorEvent::RunQuery(sql) => {
                        this.sql = sql.clone();
                        this.run_query(cx);
                    }
                }
                cx.notify();
            });
            self.sql_editor = Some(editor);
            self.sql_editor_sub = Some(sub);
        } else {
            // 连接状态变化时同步给工具栏按钮。
            if let Some(ed) = self.sql_editor.as_ref() {
                let has = self.workspace.is_some();
                ed.update(cx, |e, _cx| e.set_has_connection(has));
            }
        }

        let editor_view = self.sql_editor.clone();

        let empty = div()
            .flex_1()
            .min_h(px(0.0))
            .flex()
            .items_center()
            .justify_center()
            .text_color(th.fg_dim)
            .text_size(px(14.0))
            .child(if self.workspace.is_none() {
                "请先在左侧点击一个连接"
            } else if self.busy {
                "执行中…"
            } else {
                "编辑器：Enter 执行，Shift+Enter 换行；右上角可开 Vim 模式与补全"
            });
        let has_grid = grid.is_some();
        let export_bar = has_grid.then(|| self.render_export_bar(GridSrc::Workbench, cx));
        div()
            .flex_1()
            .min_h(px(0.0))
            .flex()
            .flex_col()
            .when_some(editor_view, |d, ed| {
                d.child(ed.into_element())
            })
            .children(log_view)
            .children(export_bar)
            .children(grid)
            .when(!has_grid, |d| d.child(empty))
            .children(preview_panel)
            .into_any_element()
    }

    /// 把当前连接的表名清单注入编辑器补全器（列名为空，待 TableDetail 回填）。
    fn feed_editor_schema(cx: &mut Context<Self>, editor: &Entity<crate::view::sql_editor::SqlEditor>, tables: &[TableInfo]) {
        let schema: Vec<(String, Vec<String>)> = tables
            .iter()
            .map(|t| (t.name.clone(), Vec::new()))
            .collect();
        editor.update(cx, |e, _cx| e.set_schema(schema));
    }

    /// tables 刷新后调用：若编辑器已存在，同步最新 schema 进补全器。
    pub(crate) fn sync_editor_schema(&mut self, cx: &mut Context<Self>) {
        if let Some(ed) = self.sql_editor.as_ref() {
            let schema: Vec<(String, Vec<String>)> = self
                .tables
                .iter()
                .map(|t| (t.name.clone(), Vec::new()))
                .collect();
            ed.update(cx, |e, _cx| e.set_schema(schema));
        }
    }

    pub(super) fn render_multi_log(&self) -> Option<Div> {
        if self.multi_log.is_empty() {
            return None;
        }
        let th = &self.theme;
        Some(
            div()
                .flex_none()
                .flex()
                .flex_col()
                .gap(px(2.0))
                .px(px(12.0))
                .py(px(6.0))
                .bg(th.bg_alt)
                .children(self.multi_log.iter().enumerate().map(|(i, (text, oc))| {
                    let (mark, color) = match oc {
                        crate::app::StmtOutcome::Ok(n) => {
                            (format!("✓ 第 {} 条 · {n} 行", i + 1), th.ok)
                        }
                        crate::app::StmtOutcome::Err(e) => {
                            (format!("✗ 第 {} 条 · {e}", i + 1), th.danger)
                        }
                        crate::app::StmtOutcome::Skipped => {
                            (format!("⊘ 第 {} 条 · 未执行", i + 1), th.fg_dim)
                        }
                    };
                    div()
                        .text_size(px(11.0))
                        .font_family("monospace")
                        .text_color(color)
                        .child(format!("{mark} — {}", crate::util::stmt_preview(text)))
                })),
        )
    }

    // ─── 表 tab：面包屑 + rails + rail 内容 ───
}
