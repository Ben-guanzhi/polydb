//! 窗口骨架视图：状态栏 + 顶栏 + 左栏 + 主区分派 + 多语句日志。

use super::*;

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

    pub(super) fn render_root(&mut self, cx: &mut Context<Self>) -> AnyElement {
        let th = &self.theme;
        let root_bg = th.bg;
        let top_bar = self.render_top_bar(cx);
        let sidebar = self.render_sidebar(cx);
        let main = self.render_main(cx);
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
                    .child(main),
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
            .child(btn("theme-ws", "🌗", th).on_click(cx.listener(
                |this, _ev: &ClickEvent, _window, cx| {
                    this.toggle_theme();
                    cx.notify();
                },
            )))
    }

    // ─── 侧栏：连接 + schema chips + 表树 ───

    pub(super) fn render_sidebar(&mut self, cx: &mut Context<Self>) -> Div {
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
        // 表树：caret 单击展开列；名称双击开 tab。
        let tree_rows: Vec<Div> = self
            .tables
            .iter()
            .enumerate()
            .map(|(ti, t)| {
                let th = &self.theme;
                let expanded = self.expanded.contains(&ti);
                let name = t.name.clone();
                let schema = t.schema.clone();
                let view_tag = match t.table_type {
                    polydb_core::TableType::Table => "",
                    polydb_core::TableType::View => " (视图)",
                    polydb_core::TableType::MaterializedView => " (物化视图)",
                };
                let row = div()
                    .flex()
                    .items_center()
                    .gap(px(4.0))
                    .px(px(8.0))
                    .py(px(4.0))
                    .child(
                        div()
                            .id(("tree-caret", ti))
                            .w(px(16.0))
                            .flex_none()
                            .text_color(th.fg_dim)
                            .text_size(px(10.0))
                            .cursor(CursorStyle::PointingHand)
                            .child(if expanded { "▾" } else { "▸" })
                            .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                                this.toggle_expand(ti, cx);
                            })),
                    )
                    .child(
                        div()
                            .id(("tree-name", ti))
                            .flex_1()
                            .truncate()
                            .text_color(th.fg)
                            .text_size(px(12.0))
                            .cursor(CursorStyle::PointingHand)
                            .hover(|d| d.text_color(th.accent))
                            .child(format!("{name}{view_tag}"))
                            .on_click(cx.listener(move |this, ev: &ClickEvent, _window, cx| {
                                if ev.click_count() >= 2 {
                                    this.open_table_tab(schema.clone(), name.clone(), cx);
                                }
                            })),
                    );
                let children = expanded.then(|| {
                    let th = &self.theme;
                    if self.cols_loading.contains(&ti) {
                        return div()
                            .pl(px(28.0))
                            .py(px(2.0))
                            .text_color(th.fg_dim)
                            .text_size(px(11.0))
                            .child("列加载中…");
                    }
                    match self.cols_cache.get(&ti) {
                        Some(cols) => div()
                            .pl(px(28.0))
                            .flex()
                            .flex_col()
                            .py(px(2.0))
                            .children(cols.iter().take(80).map(|c| {
                                div()
                                    .flex()
                                    .gap(px(6.0))
                                    .text_size(px(11.0))
                                    .text_color(th.fg_muted)
                                    .child(if c.is_primary_key { "🔑" } else { "·" }.to_string())
                                    .child(div().truncate().child(c.name.clone()))
                                    .child(div().text_color(th.fg_dim).child(c.data_type.clone()))
                            }))
                            .when(cols.len() > 80, |d| {
                                d.child(
                                    div()
                                        .text_size(px(10.0))
                                        .text_color(th.fg_dim)
                                        .child(format!("… 共 {} 列", cols.len())),
                                )
                            }),
                        None => div()
                            .pl(px(28.0))
                            .text_color(th.fg_dim)
                            .text_size(px(11.0))
                            .child("—"),
                    }
                });
                div().flex().flex_col().child(row).children(children)
            })
            .collect();
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
            .child(
                div()
                    .px(px(12.0))
                    .pt(px(10.0))
                    .pb(px(4.0))
                    .text_color(th.fg_muted)
                    .text_size(px(12.0))
                    .child(format!(
                        "{} · Tables ({})",
                        self.active_schema,
                        self.tables.len()
                    )),
            )
            .children(schema_chips)
            .child(
                div()
                    .id("tables-scroll")
                    .flex_1()
                    .min_h(px(0.0))
                    .overflow_y_scroll()
                    .px(px(4.0))
                    .flex()
                    .flex_col()
                    .children(tree_rows),
            )
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

    pub(super) fn render_main(&mut self, cx: &mut Context<Self>) -> Div {
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
                    self.render_workbench(cx)
                }
            }
            ActiveView::Table(i) if i < self.tabs.len() => self.render_table_tab(i, cx),
            _ => {
                self.active = ActiveView::Workbench;
                if self.is_redis() {
                    self.render_redis_panel(cx)
                } else {
                    self.render_workbench(cx)
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

    pub(super) fn render_workbench(&mut self, cx: &mut Context<Self>) -> AnyElement {
        let (sql_disp, sql_ranges) = self.sql_display();
        let log_view = self.render_multi_log();
        let grid = self.render_grid(GridSrc::Workbench, Some(MAX_RESULT_ROWS), None, cx);
        let preview_panel = self.render_preview(GridSrc::Workbench, cx);
        let th = self.theme;
        let hl = sql_ranges
            .into_iter()
            .map(|r| {
                (
                    r,
                    HighlightStyle {
                        background_color: Some(th.surface2.into()),
                        ..Default::default()
                    },
                )
            })
            .collect::<Vec<_>>();
        let styled = StyledText::new(sql_disp).with_highlights(hl);
        // 存下本帧文本布局句柄，供鼠标事件把像素映射回字节下标。
        self.sql_layout = Some(styled.layout().clone());
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
                "输入 SQL（Enter 执行，Shift+Enter 换行，Ctrl/Cmd+V 粘贴）；左键预览单元格，右键弹菜单"
            });
        let has_grid = grid.is_some();
        let export_bar = has_grid.then(|| self.render_export_bar(GridSrc::Workbench, cx));
        div()
            .flex_1()
            .min_h(px(0.0))
            .flex()
            .flex_col()
            .child(
                div()
                    .id("sql-input")
                    .min_h(px(36.0))
                    .max_h(px(180.0))
                    .flex_none()
                    .overflow_y_scroll()
                    .flex()
                    .flex_col()
                    .px(px(12.0))
                    .py(px(6.0))
                    .cursor(CursorStyle::IBeam)
                    .track_focus(&self.focus)
                    .on_click(cx.listener(|this, _ev: &ClickEvent, window, _cx| {
                        // 修复：聚焦回编辑器自身持有的 FocusHandle（原为游离句柄）。
                        window.focus(&this.focus);
                    }))
                    .on_key_down(cx.listener(|this, ev: &KeyDownEvent, _window, cx| {
                        this.on_input_key(ev, cx);
                    }))
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(|this, ev: &MouseDownEvent, window, cx| {
                            this.on_sql_mouse_down(ev, window, cx);
                        }),
                    )
                    .on_mouse_move(cx.listener(|this, ev: &MouseMoveEvent, _window, cx| {
                        this.on_sql_mouse_move(ev, cx);
                    }))
                    .on_mouse_up(
                        MouseButton::Left,
                        cx.listener(|this, ev: &MouseUpEvent, _window, cx| {
                            this.on_sql_mouse_up(ev, cx);
                        }),
                    )
                    .text_size(px(13.0))
                    .font_family("monospace")
                    .text_color(th.fg)
                    .child(styled),
            )
            .children(log_view)
            .children(export_bar)
            .children(grid)
            .when(!has_grid, |d| d.child(empty))
            .children(preview_panel)
            .into_any_element()
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
