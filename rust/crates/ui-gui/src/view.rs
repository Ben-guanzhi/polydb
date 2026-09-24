//! 渲染层：单窗口主界面——左栏连接 + schema/表树，右栏对象 tab 条 + SQL 编辑器/结果。
//! 全部颜色取自 theme::Theme token。

use gpui::prelude::FluentBuilder;
use gpui::*;
use polydb_core::{DatabaseKind, FilterLogic, RedisKeyType, RedisValue, SortDirection, Value};

use crate::app::{
    conn_visible_fields, ConnField, GridSrc, PolyDBApp, PreviewTab, QuickTarget, Rail, PAGE_SIZES,
};
use crate::exporters::ExportFormat;
use crate::filters;
use crate::profile;
use crate::tabs::ActiveView;
use crate::theme::{status_color, Theme};
use crate::util::{clip, hex_dump, try_pretty_json, value_str, CELL_CLIP, MAX_RESULT_ROWS};

// 网格作用域 → 稳定元素 id 基数（workbench 与每个表 tab 的 id 空间隔离）。
fn src_key(src: GridSrc) -> usize {
    match src {
        GridSrc::Workbench => usize::MAX / 2,
        GridSrc::Table(i) => i * 1_000_000,
    }
}

fn btn(id: impl Into<ElementId>, label: impl Into<SharedString>, th: &Theme) -> Stateful<Div> {
    div()
        .id(id)
        .px(px(8.0))
        .py(px(3.0))
        .bg(th.surface)
        .text_color(th.fg)
        .text_size(px(12.0))
        .rounded(px(4.0))
        .cursor(CursorStyle::PointingHand)
        .hover(|d| d.bg(th.surface2))
        .child(label.into())
}

fn badge(label: impl Into<SharedString>, fg: gpui::Rgba, th: &Theme) -> Div {
    div()
        .px(px(6.0))
        .py(px(1.0))
        .rounded(px(8.0))
        .bg(th.surface)
        .text_color(fg)
        .text_size(px(10.0))
        .child(label.into())
}

fn redis_type_label(t: RedisKeyType) -> &'static str {
    match t {
        RedisKeyType::String => "string",
        RedisKeyType::List => "list",
        RedisKeyType::Set => "set",
        RedisKeyType::Zset => "zset",
        RedisKeyType::Hash => "hash",
        RedisKeyType::Stream => "stream",
        RedisKeyType::None => "none",
    }
}

/// RedisValue → (类型标签, 逐行文本)。哈希按字段名排序保证稳定。
fn redis_value_lines(value: &RedisValue) -> (&'static str, Vec<String>) {
    match value {
        RedisValue::String(s) => ("string", vec![s.clone()]),
        RedisValue::List(v) => (
            "list",
            v.iter()
                .enumerate()
                .map(|(i, e)| format!("{i}. {e}"))
                .collect(),
        ),
        RedisValue::Set(v) => ("set", v.to_vec()),
        RedisValue::Zset(v) => (
            "zset",
            v.iter()
                .map(|m| format!("{} · {}", m.member, m.score))
                .collect(),
        ),
        RedisValue::Hash(map) => {
            let mut entries: Vec<(&String, &String)> = map.iter().collect();
            entries.sort_by(|a, b| a.0.cmp(b.0));
            (
                "hash",
                entries
                    .into_iter()
                    .map(|(k, v)| format!("{k} = {v}"))
                    .collect(),
            )
        }
        RedisValue::Stream(s) => ("stream", vec![s.clone()]),
    }
}

impl PolyDBApp {
    fn status_bar(&self) -> Div {
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

    fn render_root(&mut self, cx: &mut Context<Self>) -> AnyElement {
        let th = &self.theme;
        let root_bg = th.bg;
        let top_bar = self.render_top_bar(cx);
        let sidebar = self.render_sidebar(cx);
        let main = self.render_main(cx);
        let ctx_menu = self.render_ctx_menu(cx);
        let conn_modal = self.render_conn_form(cx);
        let quick_modal = self.render_quick_switcher(cx);
        let explain_modal = self.render_explain_modal(cx);
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
            .into_any_element()
    }

    fn render_top_bar(&self, cx: &mut Context<Self>) -> Div {
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
            .child(btn("theme-ws", "🌗", th).on_click(cx.listener(
                |this, _ev: &ClickEvent, _window, cx| {
                    this.toggle_theme();
                    cx.notify();
                },
            )))
    }

    // ─── 侧栏：连接 + schema chips + 表树 ───

    fn render_sidebar(&mut self, cx: &mut Context<Self>) -> Div {
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

    fn render_main(&mut self, cx: &mut Context<Self>) -> Div {
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

    fn render_workbench(&mut self, cx: &mut Context<Self>) -> AnyElement {
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

    /// G4.4 Redis 键浏览面板：选库 + SCAN 模式 + 键列表 + 取值详情；
    /// D4 起 string 值可整值写回（其余类型仍只读，需走命令行）。
    fn render_redis_panel(&mut self, cx: &mut Context<Self>) -> AnyElement {
        let th = self.theme;
        let db = self.redis_db;
        let loading = self.redis_loading;
        let more = self.redis_scan_cursor != 0;
        let pattern = self.redis_pattern.clone();
        let editing = self.redis_editing;
        let pcur = self.redis_cursor;
        let keys = self.redis_keys.clone();
        let selected = self.redis_selected.clone();
        let vediting = self.redis_val_editing;
        let vbuf = self.redis_val_buf.clone();
        let vcur = self.redis_val_cursor;

        let db_chips: Vec<Stateful<Div>> = (0..16u32)
            .map(|i| {
                let on = i == db;
                div()
                    .id(("rd-db", i as usize))
                    .px(px(6.0))
                    .py(px(2.0))
                    .rounded(px(4.0))
                    .text_size(px(11.0))
                    .bg(if on { th.accent } else { th.surface })
                    .text_color(if on { th.bg } else { th.fg_muted })
                    .cursor(CursorStyle::PointingHand)
                    .child(i.to_string())
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                        this.redis_set_db(i, cx);
                    }))
            })
            .collect();

        let pat_body = if editing {
            let mut s = pattern.clone();
            let at = pcur.min(s.len());
            s.insert(at, '▏');
            s
        } else if pattern.is_empty() {
            "⟨匹配模式，如 user:*⟩".to_string()
        } else {
            pattern.clone()
        };
        let pat_box = div()
            .id("rd-pat")
            .px(px(8.0))
            .py(px(4.0))
            .rounded(px(4.0))
            .min_w(px(180.0))
            .max_w(px(320.0))
            .font_family("monospace")
            .text_size(px(12.0))
            .truncate()
            .bg(if editing { th.surface2 } else { th.bg })
            .border(px(1.0))
            .border_color(if editing { th.accent } else { th.border })
            .text_color(if pattern.is_empty() && !editing {
                th.fg_dim
            } else {
                th.fg
            })
            .cursor(CursorStyle::IBeam)
            .child(pat_body)
            .on_click(cx.listener(|this, _ev: &ClickEvent, window, cx| {
                this.focus_redis_pattern(window, cx);
            }));

        let key_rows: Vec<Stateful<Div>> = keys
            .iter()
            .enumerate()
            .map(|(ki, k)| {
                let on = selected.as_deref() == Some(k.key.as_str());
                let name = k.key.clone();
                let tl = redis_type_label(k.key_type);
                let ttl = k.ttl.map(|t| t.to_string()).unwrap_or_else(|| "-".into());
                div()
                    .id(("rdk", ki))
                    .w_full()
                    .px(px(8.0))
                    .py(px(4.0))
                    .flex()
                    .items_center()
                    .gap(px(6.0))
                    .rounded(px(4.0))
                    .bg(if on { th.surface2 } else { th.bg })
                    .cursor(CursorStyle::PointingHand)
                    .hover(|d| d.bg(th.hover))
                    .child(badge(tl, th.accent, &th))
                    .child(
                        div()
                            .flex_1()
                            .min_w(px(0.0))
                            .truncate()
                            .text_size(px(12.0))
                            .text_color(th.fg)
                            .child(name.clone()),
                    )
                    .child(
                        div()
                            .text_size(px(10.0))
                            .text_color(th.fg_dim)
                            .child(format!("ttl {ttl}")),
                    )
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                        this.redis_open_key(name.clone(), cx);
                    }))
            })
            .collect();

        let (type_label, vlines): (Option<&'static str>, Vec<Div>) = match &self.redis_value {
            Some(v) => {
                let (tl, lines) = redis_value_lines(v);
                (
                    Some(tl),
                    lines
                        .into_iter()
                        .map(|l| div().text_size(px(12.0)).text_color(th.fg).child(l))
                        .collect(),
                )
            }
            None => (
                None,
                vec![div().text_color(th.fg_dim).text_size(px(12.0)).child(
                    if selected.is_some() {
                        "读取中…"
                    } else {
                        "点击左侧键查看值"
                    },
                )],
            ),
        };
        let has_value = self.redis_value.is_some();
        let is_string = matches!(&self.redis_value, Some(RedisValue::String(_)));
        // 写回子模式下单行缓冲就地显示光标；否则展示驱动回读的值行。
        let val_body: AnyElement = if vediting {
            let mut s = vbuf.clone();
            let at = vcur.min(s.len());
            s.insert(at, '▏');
            div()
                .id("rd-val-edit")
                .w_full()
                .px(px(8.0))
                .py(px(4.0))
                .rounded(px(4.0))
                .bg(th.surface2)
                .border(px(1.0))
                .border_color(th.accent)
                .font_family("monospace")
                .text_size(px(12.0))
                .text_color(th.fg)
                .cursor(CursorStyle::IBeam)
                .child(s)
                .on_click(cx.listener(|this, _ev: &ClickEvent, window, _cx| {
                    window.focus(&this.redis_focus);
                }))
                .into_any_element()
        } else {
            div()
                .font_family("monospace")
                .children(vlines)
                .into_any_element()
        };

        div()
            .id("redis-panel")
            .flex_1()
            .min_h(px(0.0))
            .flex()
            .flex_col()
            .track_focus(&self.redis_focus)
            .on_key_down(cx.listener(|this, ev: &KeyDownEvent, _window, cx| {
                this.on_redis_key(ev, cx);
            }))
            .child(
                div()
                    .flex_none()
                    .flex()
                    .flex_wrap()
                    .items_center()
                    .gap(px(6.0))
                    .px(px(12.0))
                    .py(px(8.0))
                    .bg(th.bg_alt)
                    .child(
                        div()
                            .text_color(th.fg_muted)
                            .text_size(px(12.0))
                            .child("db"),
                    )
                    .children(db_chips)
                    .child(pat_box)
                    .child(btn("rd-scan", "▶ 扫描", &th).on_click(cx.listener(
                        |this, _ev: &ClickEvent, _window, cx| {
                            this.redis_scan(cx);
                        },
                    )))
                    .when(more, |d| {
                        d.child(btn("rd-more", "更多…", &th).on_click(cx.listener(
                            |this, _ev: &ClickEvent, _window, cx| {
                                this.redis_more(cx);
                            },
                        )))
                    })
                    .child(
                        div()
                            .text_color(th.fg_dim)
                            .text_size(px(11.0))
                            .child(if loading {
                                "扫描中…"
                            } else if more {
                                "· 还有更多"
                            } else {
                                ""
                            }),
                    ),
            )
            .child(
                div()
                    .flex_1()
                    .min_h(px(0.0))
                    .flex()
                    .child(
                        div()
                            .id("rd-keys")
                            .w(px(340.0))
                            .flex_none()
                            .min_h(px(0.0))
                            .overflow_y_scroll()
                            .border_r(px(1.0))
                            .border_color(th.border)
                            .py(px(4.0))
                            .px(px(6.0))
                            .flex()
                            .flex_col()
                            .children(key_rows),
                    )
                    .child(
                        div()
                            .id("rd-value")
                            .flex_1()
                            .min_w(px(0.0))
                            .overflow_y_scroll()
                            .p(px(12.0))
                            .flex()
                            .flex_col()
                            .gap(px(3.0))
                            .child(
                                div()
                                    .flex()
                                    .items_center()
                                    .gap(px(8.0))
                                    .child(
                                        div()
                                            .font_weight(FontWeight::BOLD)
                                            .text_size(px(13.0))
                                            .text_color(th.fg)
                                            .child(
                                                selected
                                                    .clone()
                                                    .unwrap_or_else(|| "（未选择键）".into()),
                                            ),
                                    )
                                    .when_some(type_label, |d, tl| d.child(badge(tl, th.ok, &th)))
                                    .when(vediting, |d| {
                                        d.child(
                                            div()
                                                .text_size(px(11.0))
                                                .text_color(th.fg_dim)
                                                .child("Enter 提交并回读 · Esc 放弃"),
                                        )
                                    })
                                    .when(!vediting && has_value && is_string, |d| {
                                        d.child(btn("rd-val-write", "✎ 写回整值", &th).on_click(
                                            cx.listener(|this, _ev: &ClickEvent, window, cx| {
                                                this.redis_start_edit_value(window, cx);
                                            }),
                                        ))
                                    })
                                    .when(!vediting && has_value && !is_string, |d| {
                                        d.child(
                                            div()
                                                .text_size(px(11.0))
                                                .text_color(th.fg_dim)
                                                .child("仅 string 支持整值写回"),
                                        )
                                    }),
                            )
                            .child(val_body),
                    ),
            )
            .into_any_element()
    }

    fn render_multi_log(&self) -> Option<Div> {
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

    fn render_table_tab(&mut self, idx: usize, cx: &mut Context<Self>) -> AnyElement {
        let th = &self.theme;
        let (schema, table, rail) = {
            let tab = &self.tabs[idx];
            (tab.schema.clone(), tab.table.clone(), tab.rail)
        };
        let rails = [Rail::Overview, Rail::Data, Rail::Structure, Rail::Relations];
        let rail_btns = rails.iter().enumerate().map(|(ri, r)| {
            let r = *r;
            let active = rail == r;
            div()
                .id(("rail-btn", idx * 10 + ri))
                .px(px(10.0))
                .py(px(3.0))
                .rounded(px(4.0))
                .text_size(px(12.0))
                .cursor(CursorStyle::PointingHand)
                .bg(if active { th.surface2 } else { th.bg_alt })
                .text_color(if active { th.accent } else { th.fg_muted })
                .child(r.label().to_string())
                .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                    this.set_rail(idx, r, cx);
                }))
        });
        let head = div()
            .flex_none()
            .flex()
            .items_center()
            .gap(px(8.0))
            .px(px(12.0))
            .py(px(6.0))
            .bg(th.bg_alt)
            .child(
                div()
                    .text_color(th.fg)
                    .text_size(px(13.0))
                    .font_weight(FontWeight::BOLD)
                    .child(format!("{schema}.{table}")),
            )
            .children(rail_btns)
            .child(div().flex_1())
            .child(btn(("tab-reload", idx), "⟳ 重载", th).on_click(cx.listener(
                move |this, _ev: &ClickEvent, _window, cx| {
                    this.reload_tab(idx, cx);
                },
            )));
        let body: AnyElement = match rail {
            Rail::Overview => self.render_rail_overview(idx, cx),
            Rail::Data => self.render_rail_data(idx, cx),
            Rail::Structure => self.render_rail_structure(idx, cx),
            Rail::Relations => self.render_rail_relations(idx, cx),
        };
        div()
            .flex_1()
            .min_h(px(0.0))
            .flex()
            .flex_col()
            .child(head)
            .child(body)
            .into_any_element()
    }

    fn render_rail_overview(&mut self, idx: usize, cx: &mut Context<Self>) -> AnyElement {
        let th = &self.theme;
        let tab = &self.tabs[idx];
        if tab.loading_profile {
            return div()
                .flex_1()
                .flex()
                .items_center()
                .justify_center()
                .text_color(th.fg_dim)
                .child("画像生成中…（单条聚合 SQL）")
                .into_any_element();
        }
        let Some(detail) = &tab.detail else {
            return div()
                .flex_1()
                .flex()
                .items_center()
                .justify_center()
                .text_color(th.fg_dim)
                .child(if tab.loading_detail {
                    "元数据加载中…"
                } else {
                    "无元数据"
                })
                .into_any_element();
        };
        let info = |label: &str, val: String| {
            div()
                .flex_none()
                .px(px(14.0))
                .py(px(10.0))
                .rounded(px(8.0))
                .bg(th.bg_alt)
                .flex()
                .flex_col()
                .gap(px(4.0))
                .child(
                    div()
                        .text_size(px(11.0))
                        .text_color(th.fg_dim)
                        .child(label.to_string()),
                )
                .child(
                    div()
                        .text_size(px(16.0))
                        .font_weight(FontWeight::BOLD)
                        .text_color(th.fg)
                        .child(val),
                )
        };
        let est = tab
            .browse
            .total
            .map(|t| format!("~{t}"))
            .or_else(|| detail.columns.first().map(|_| "—".to_string()))
            .unwrap_or_default();
        let cards = div()
            .flex_none()
            .flex()
            .gap(px(10.0))
            .p(px(12.0))
            .child(info("行数（估算）", est))
            .child(info("列数", detail.columns.len().to_string()))
            .child(info("索引", detail.indexes.len().to_string()))
            .child(info("外键", detail.fks.len().to_string()))
            .child(
                btn(("prof-reload", idx), "重新画像", th).on_click(cx.listener(
                    move |this, _ev: &ClickEvent, _window, cx| {
                        if let Some(t) = this.tabs.get_mut(idx) {
                            t.profile = None;
                        }
                        this.load_profile(idx, cx);
                    },
                )),
            );
        let Some(prof) = &tab.profile else {
            return div()
                .flex_1()
                .flex()
                .flex_col()
                .child(cards)
                .into_any_element();
        };
        // 画像表：列 / 类型 / NULL 数 / 完整度 / DISTINCT / MIN / MAX / AVG。
        let row_bg = th.bg;
        let col_bg = th.bg_alt;
        let total = prof.row_count;
        let head_row = div()
            .flex()
            .gap(px(12.0))
            .px(px(12.0))
            .py(px(6.0))
            .bg(col_bg)
            .text_color(th.accent)
            .text_size(px(12.0))
            .children(
                [
                    "列",
                    "类型",
                    "NULL",
                    "完整度",
                    "DISTINCT",
                    "MIN",
                    "MAX",
                    "AVG",
                ]
                .iter()
                .map(|h| div().child((*h).to_string())),
            );
        let rows: Vec<Div> = prof
            .by_column
            .iter()
            .map(|(name, p)| {
                let dtype = detail
                    .columns
                    .iter()
                    .find(|c| &c.name == name)
                    .map(|c| c.data_type.clone())
                    .unwrap_or_default();
                let completeness = match (total, p.nulls) {
                    (Some(t), Some(n)) if t > 0 => {
                        format!("{:.1}%", (1.0 - n as f64 / t as f64) * 100.0)
                    }
                    _ => "—".to_string(),
                };
                let cell = |v: String| {
                    div()
                        .text_size(px(12.0))
                        .font_family("monospace")
                        .text_color(th.fg)
                        .child(v)
                };
                div()
                    .flex()
                    .gap(px(12.0))
                    .px(px(12.0))
                    .py(px(4.0))
                    .bg(row_bg)
                    .children([
                        cell(name.clone()),
                        cell(dtype),
                        cell(p.nulls.map(|n| n.to_string()).unwrap_or_else(|| "—".into())),
                        cell(completeness),
                        cell(
                            p.distinct
                                .map(|n| n.to_string())
                                .unwrap_or_else(|| "—".into()),
                        ),
                        cell(p.min.as_ref().map(value_str).unwrap_or_else(|| "—".into())),
                        cell(p.max.as_ref().map(value_str).unwrap_or_else(|| "—".into())),
                        cell(
                            p.avg
                                .as_ref()
                                .map(|v| match v {
                                    Value::Float(f) => format!("{f:.2}"),
                                    other => value_str(other),
                                })
                                .unwrap_or_else(|| "—".into()),
                        ),
                    ])
            })
            .collect();
        let note = (detail.columns.len() > profile::MAX_PROFILE_COLS).then(|| {
            div()
                .px(px(12.0))
                .py(px(6.0))
                .text_size(px(11.0))
                .text_color(th.fg_dim)
                .child(format!(
                    "列数较多，仅画像前 {} 列。",
                    profile::MAX_PROFILE_COLS
                ))
        });
        div()
            .flex_1()
            .min_h(px(0.0))
            .flex()
            .flex_col()
            .child(cards)
            .child(
                div()
                    .id(("prof-scroll", idx))
                    .flex_1()
                    .min_h(px(0.0))
                    .overflow_y_scroll()
                    .child(head_row)
                    .children(rows),
            )
            .children(note)
            .into_any_element()
    }

    fn render_rail_data(&mut self, idx: usize, cx: &mut Context<Self>) -> AnyElement {
        let th = self.theme;
        let tab = &self.tabs[idx];
        let b = &tab.browse;
        let shown = b.grid.as_ref().map_or(0, |g| g.rows.len());
        let count_part = match b.exact {
            Some(n) => format!(" / 精确 {n} 行"),
            None => b.total.map(|t| format!(" / 共约 {t}")).unwrap_or_default(),
        };
        let label = format!(
            "行 {}–{}{}{}{}",
            b.offset + 1,
            b.offset + shown as u64,
            count_part,
            if b.counting {
                " · 计数中…"
            } else if b.loading {
                " · 加载中…"
            } else if b.has_more {
                " · 有下一页"
            } else {
                ""
            },
            b.grid
                .as_ref()
                .map(|g| format!(" · {:.1}ms", g.elapsed_ms))
                .unwrap_or_default(),
        );
        let (offset, has_more, limit) = (b.offset, b.has_more, b.limit);
        let change_n = self.change_count(idx);
        let editable = self.browse_editable(idx);
        let review = self.review_open;
        let committing = self.committing;
        let read_only = self.is_read_only();
        let size_chips = div()
            .flex()
            .gap(px(4.0))
            .children(PAGE_SIZES.into_iter().map(|ps| {
                let active = ps == limit;
                div()
                    .id(("psize", idx * 10 + ps as usize))
                    .px(px(6.0))
                    .py(px(2.0))
                    .rounded(px(4.0))
                    .text_size(px(11.0))
                    .cursor(CursorStyle::PointingHand)
                    .bg(if active { th.surface2 } else { th.surface })
                    .text_color(if active { th.accent } else { th.fg_muted })
                    .child(format!("{ps}"))
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                        this.set_page_size(idx, ps, cx);
                    }))
            }));
        let bar = div()
            .flex_none()
            .flex()
            .items_center()
            .gap(px(8.0))
            .px(px(12.0))
            .py(px(6.0))
            .bg(th.bg_alt)
            .text_color(th.fg_muted)
            .text_size(px(12.0))
            .child(label)
            .child(
                btn(("browse-prev", idx), "‹ 上一页", &th).on_click(cx.listener(
                    move |this, _ev: &ClickEvent, _window, cx| {
                        if offset > 0 {
                            let prev = offset.saturating_sub(limit as u64);
                            this.load_page(idx, prev, cx);
                        }
                    },
                )),
            )
            .child(
                btn(("browse-next", idx), "下一页 ›", &th).on_click(cx.listener(
                    move |this, _ev: &ClickEvent, _window, cx| {
                        if has_more {
                            this.load_page(idx, offset + limit as u64, cx);
                        }
                    },
                )),
            )
            .child(
                btn(("browse-count", idx), "计数", &th).on_click(cx.listener(
                    move |this, _ev: &ClickEvent, _window, cx| {
                        this.count_rows(idx, cx);
                    },
                )),
            )
            .child(size_chips)
            .child(div().flex_1())
            .when(editable, |d| {
                d.child(
                    btn(("chg-review", idx), format!("评审 {change_n}"), &th).on_click(
                        cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                            this.toggle_review(cx);
                        }),
                    ),
                )
                .child(btn(("chg-undo", idx), "撤销", &th).on_click(cx.listener(
                    move |this, _ev: &ClickEvent, _window, cx| {
                        this.undo_last_change(idx, cx);
                    },
                )))
                .child(
                    btn(
                        ("chg-commit", idx),
                        if committing {
                            "提交中…".to_string()
                        } else {
                            format!("提交 {change_n}")
                        },
                        &th,
                    )
                    .text_color(if read_only || change_n == 0 || committing {
                        th.fg_dim
                    } else {
                        th.accent
                    })
                    .on_click(cx.listener(
                        move |this, _ev: &ClickEvent, _window, cx| {
                            if this.committing {
                                return;
                            }
                            this.commit_changes(idx, cx);
                        },
                    )),
                )
            });
        let review_panel = if review && editable {
            self.render_changes_panel(idx, cx)
        } else {
            None
        };
        let filter_bar = self.render_filter_bar(idx, cx);
        let grid = self.render_grid(GridSrc::Table(idx), None, Some(idx), cx);
        let inspector = self.render_inspector(idx, cx);
        let has_grid = grid.is_some();
        let export_bar = has_grid.then(|| self.render_export_bar(GridSrc::Table(idx), cx));
        let empty = div()
            .flex_1()
            .flex()
            .items_center()
            .justify_center()
            .text_color(th.fg_dim)
            .text_size(px(13.0))
            .child(if self.tabs[idx].browse.loading {
                "加载中…"
            } else {
                "无数据"
            });
        div()
            .flex_1()
            .min_h(px(0.0))
            .flex()
            .flex_col()
            .child(filter_bar)
            .child(bar)
            .children(review_panel)
            .children(export_bar)
            .child(
                div()
                    .flex_1()
                    .min_h(px(0.0))
                    .flex()
                    .children(grid)
                    .when(!has_grid, |d| d.child(empty))
                    .children(inspector),
            )
            .children(self.render_preview(GridSrc::Table(idx), cx))
            .into_any_element()
    }

    /// G4 网格导出条：按格式落盘（保存对话框）。Workbench 无表名故隐藏 INSERT。
    fn render_export_bar(&mut self, src: GridSrc, cx: &mut Context<Self>) -> Div {
        let th = self.theme;
        let src_c = src;
        let is_table = matches!(src, GridSrc::Table(_));
        let chips: Vec<Stateful<Div>> = ExportFormat::ALL
            .iter()
            .enumerate()
            .filter(|(_, f)| is_table || !matches!(f, ExportFormat::Insert))
            .map(|(fi, f)| {
                let fmt = *f;
                div()
                    .id(("exp", src_key(src_c) + fi))
                    .px(px(6.0))
                    .py(px(2.0))
                    .rounded(px(3.0))
                    .bg(th.surface)
                    .text_color(th.fg_muted)
                    .text_size(px(11.0))
                    .cursor(CursorStyle::PointingHand)
                    .hover(|d| d.bg(th.surface2).text_color(th.accent))
                    .child(format!("↓ {}", fmt.label()))
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                        this.export_grid(src_c, fmt, cx);
                    }))
            })
            .collect();
        div()
            .flex_none()
            .flex()
            .items_center()
            .gap(px(4.0))
            .px(px(12.0))
            .py(px(4.0))
            .bg(th.bg_alt)
            .border_t(px(1.0))
            .border_color(th.border)
            .child(
                div()
                    .text_color(th.fg_dim)
                    .text_size(px(11.0))
                    .child("导出"),
            )
            .children(chips)
    }

    /// G3 待提交改动 Review 面板：逐条展示参数化前的语句 + 影响行数预期。
    fn render_changes_panel(&mut self, idx: usize, _cx: &mut Context<Self>) -> Option<AnyElement> {
        let th = self.theme;
        let stmts = self.pending_statements(idx);
        if stmts.is_empty() {
            return None;
        }
        let lines: Vec<Div> = stmts
            .iter()
            .enumerate()
            .map(|(n, s)| {
                let expect = if s.allow_zero {
                    "≤1".to_string()
                } else {
                    format!("{}", s.expect_affected)
                };
                div()
                    .flex()
                    .gap(px(8.0))
                    .px(px(12.0))
                    .py(px(3.0))
                    .text_size(px(11.0))
                    .font_family("monospace")
                    .child(
                        div()
                            .w(px(28.0))
                            .text_color(th.fg_dim)
                            .child(format!("#{}", n + 1)),
                    )
                    .child(div().flex_1().text_color(th.fg).child(s.sql.clone()))
                    .child(
                        div()
                            .flex_none()
                            .text_color(th.fg_muted)
                            .child(format!("行数 {expect} · {} 参", s.params.len())),
                    )
            })
            .collect();
        Some(
            div()
                .id("changes-panel")
                .flex_none()
                .max_h(px(180.0))
                .overflow_y_scroll()
                .border_t(px(1.0))
                .border_color(th.border)
                .bg(th.bg_alt)
                .py(px(4.0))
                .child(
                    div()
                        .px(px(12.0))
                        .py(px(4.0))
                        .text_size(px(11.0))
                        .text_color(th.fg_muted)
                        .child(format!(
                            "待提交 {} 条语句（单事务，任一失败整体回滚）· 输入 NULL 置空",
                            stmts.len()
                        )),
                )
                .children(lines)
                .into_any_element(),
        )
    }

    // ─── G4 连接 CRUD 表单（模态浮层）───

    fn render_quick_switcher(&mut self, cx: &mut Context<Self>) -> Option<AnyElement> {
        if !self.quick_open {
            return None;
        }
        let th = self.theme;
        let matches = self.quick_matches();
        let sel = self.quick_sel.min(matches.len().saturating_sub(1));
        // 输入行：'▸ ' 前缀 + 查询文本 + 光标字符。
        let q = self.quick_query.clone();
        let at = crate::util::char_ceil(&q, self.quick_cursor.min(q.len()));
        let mut qdisp = String::from("▸ ");
        qdisp.push_str(&q[..at]);
        qdisp.push('▏');
        qdisp.push_str(&q[at..]);
        let rows: Vec<AnyElement> = matches
            .iter()
            .enumerate()
            .map(|(i, (target, label))| {
                let tgt = target.clone();
                let prefix = match target {
                    QuickTarget::NewTable(..) => "＋ ",
                    _ => "  ",
                };
                div()
                    .id(("qs-row", i))
                    .px(px(12.0))
                    .py(px(6.0))
                    .cursor(CursorStyle::PointingHand)
                    .bg(if i == sel { th.surface2 } else { th.bg_alt })
                    .text_color(th.fg)
                    .text_size(px(13.0))
                    .child(format!("{prefix}{label}"))
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                        this.quick_activate(tgt.clone(), cx);
                    }))
                    .into_any_element()
            })
            .collect();
        let empty_hint = if matches.is_empty() {
            Some(
                div()
                    .px(px(12.0))
                    .py(px(10.0))
                    .text_color(th.fg_dim)
                    .text_size(px(12.0))
                    .child("无匹配对象"),
            )
        } else {
            None
        };
        Some(
            div()
                .id("qs-backdrop")
                .absolute()
                .inset_0()
                .flex()
                .items_start()
                .justify_center()
                .pt(px(80.0))
                .bg(Rgba {
                    r: 0.0,
                    g: 0.0,
                    b: 0.0,
                    a: 0.45,
                })
                .on_click(cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                    this.close_quick_switcher();
                    cx.notify();
                }))
                .child(
                    div()
                        .id("quick-switcher")
                        .w(px(420.0))
                        .max_h(px(360.0))
                        .flex()
                        .flex_col()
                        .rounded(px(8.0))
                        .border(px(1.0))
                        .border_color(th.border)
                        .bg(th.bg_alt)
                        .track_focus(&self.quick_focus)
                        .key_context("QuickSwitcher")
                        .on_key_down(cx.listener(|this, ev: &KeyDownEvent, _window, cx| {
                            this.on_quick_key(ev, cx);
                        }))
                        .on_click(cx.listener(|_this, _ev: &ClickEvent, _window, cx| {
                            cx.stop_propagation();
                        }))
                        .child(
                            div()
                                .px(px(12.0))
                                .py(px(8.0))
                                .border_b(px(1.0))
                                .border_color(th.border)
                                .text_color(th.fg)
                                .text_size(px(13.0))
                                .font_family("monospace")
                                .child(qdisp),
                        )
                        .child(
                            div()
                                .id("qs-list")
                                .flex()
                                .flex_col()
                                .overflow_y_scroll()
                                .children(rows)
                                .children(empty_hint),
                        ),
                )
                .into_any_element(),
        )
    }

    fn render_conn_form(&mut self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let th = self.theme;
        let form = self.conn_form.as_ref()?;
        let editing = form.editing.is_some();
        let kind = form.kind;
        let read_only = form.read_only;
        let ssh = form.ssh;
        let active = form.active;
        let cursor = form.cursor;
        let specs = conn_visible_fields(kind, ssh);
        let fields: Vec<(ConnField, &'static str, &'static str, bool, String)> = specs
            .iter()
            .map(|s| {
                (
                    s.field,
                    s.label,
                    s.placeholder,
                    s.secret,
                    form.get(s.field).to_string(),
                )
            })
            .collect();

        // 类型 chips（编辑既有连接时只读展示）。
        let kinds: [(DatabaseKind, &'static str); 6] = [
            (DatabaseKind::Sqlite, "sqlite"),
            (DatabaseKind::MySql, "mysql"),
            (DatabaseKind::Postgres, "postgres"),
            (DatabaseKind::Mssql, "mssql"),
            (DatabaseKind::Oracle, "oracle"),
            (DatabaseKind::Redis, "redis"),
        ];
        let kind_chips: Vec<Stateful<Div>> = kinds
            .iter()
            .enumerate()
            .map(|(n, (k, label))| {
                let on = *k == kind;
                let kk = *k;
                div()
                    .id(("cf-kind", n))
                    .px(px(8.0))
                    .py(px(2.0))
                    .rounded(px(8.0))
                    .text_size(px(11.0))
                    .bg(if on { th.accent } else { th.surface })
                    .text_color(if on {
                        th.bg
                    } else if editing {
                        th.fg_dim
                    } else {
                        th.fg_muted
                    })
                    .cursor(if editing {
                        CursorStyle::Arrow
                    } else {
                        CursorStyle::PointingHand
                    })
                    .child(label.to_string())
                    .when(!editing, |d| {
                        d.on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                            this.set_conn_kind(kk, cx);
                        }))
                    })
            })
            .collect();

        let toggle = |id: &'static str, label: &'static str, on: bool, danger: bool| {
            div()
                .id(id)
                .px(px(8.0))
                .py(px(2.0))
                .rounded(px(8.0))
                .text_size(px(11.0))
                .bg(if on {
                    if danger {
                        th.warn
                    } else {
                        th.accent
                    }
                } else {
                    th.surface
                })
                .text_color(if on { th.bg } else { th.fg_muted })
                .cursor(CursorStyle::PointingHand)
                .child(if on {
                    format!("☑ {label}")
                } else {
                    format!("☐ {label}")
                })
        };

        let field_rows: Vec<Div> = fields
            .into_iter()
            .enumerate()
            .map(|(si, (field, label, placeholder, secret, text))| {
                let focused = active == Some(field);
                let body = if focused {
                    if secret {
                        format!("{}▏", "●".repeat(text.len()))
                    } else {
                        let mut s = text.clone();
                        let at = cursor.min(s.len());
                        s.insert(at, '▏');
                        s
                    }
                } else if text.is_empty() {
                    format!("⟨{placeholder}⟩")
                } else if secret {
                    "●".repeat(text.len())
                } else {
                    text.clone()
                };
                div()
                    .flex()
                    .items_center()
                    .gap(px(8.0))
                    .child(
                        div()
                            .w(px(80.0))
                            .flex_none()
                            .text_color(th.fg_muted)
                            .text_size(px(12.0))
                            .child(label),
                    )
                    .child(
                        div()
                            .id(("cf-field", si))
                            .flex_1()
                            .min_w(px(0.0))
                            .px(px(8.0))
                            .py(px(4.0))
                            .rounded(px(4.0))
                            .font_family("monospace")
                            .text_size(px(12.0))
                            .truncate()
                            .bg(if focused { th.surface2 } else { th.bg })
                            .border(px(1.0))
                            .border_color(if focused { th.accent } else { th.border })
                            .text_color(if text.is_empty() && !focused {
                                th.fg_dim
                            } else {
                                th.fg
                            })
                            .cursor(CursorStyle::IBeam)
                            .child(body)
                            .on_click(cx.listener(move |this, _ev: &ClickEvent, window, cx| {
                                this.focus_conn_field(field, window, cx);
                            })),
                    )
            })
            .collect();

        let title = if editing {
            "编辑连接"
        } else {
            "新建连接"
        };
        Some(
            div()
                .id("conn-backdrop")
                .absolute()
                .inset_0()
                .flex()
                .items_center()
                .justify_center()
                .bg(Rgba {
                    r: 0.0,
                    g: 0.0,
                    b: 0.0,
                    a: 0.45,
                })
                .on_click(cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                    this.close_conn(cx);
                }))
                .child(
                    div()
                        .id("conn-form")
                        .w(px(460.0))
                        .max_h(px(520.0))
                        .flex()
                        .flex_col()
                        .rounded(px(8.0))
                        .border(px(1.0))
                        .border_color(th.border)
                        .bg(th.bg_alt)
                        .track_focus(&self.conn_focus)
                        .on_key_down(cx.listener(|this, ev: &KeyDownEvent, window, cx| {
                            this.on_conn_key(ev, window, cx);
                        }))
                        .on_click(cx.listener(|_this, _ev: &ClickEvent, _window, cx| {
                            cx.stop_propagation();
                        }))
                        .child(
                            div()
                                .flex()
                                .items_center()
                                .justify_between()
                                .px(px(14.0))
                                .py(px(10.0))
                                .child(
                                    div()
                                        .font_weight(FontWeight::BOLD)
                                        .text_size(px(14.0))
                                        .text_color(th.fg)
                                        .child(title),
                                )
                                .child(
                                    div()
                                        .id("cf-close")
                                        .px(px(6.0))
                                        .text_color(th.fg_dim)
                                        .cursor(CursorStyle::PointingHand)
                                        .hover(|d| d.text_color(th.danger))
                                        .child("×")
                                        .on_click(cx.listener(
                                            |this, _ev: &ClickEvent, _window, cx| {
                                                this.close_conn(cx);
                                            },
                                        )),
                                ),
                        )
                        .child(
                            div()
                                .id("cf-scroll")
                                .flex_1()
                                .min_h(px(0.0))
                                .overflow_y_scroll()
                                .px(px(14.0))
                                .pb(px(8.0))
                                .flex()
                                .flex_col()
                                .gap(px(8.0))
                                .child(
                                    div()
                                        .flex()
                                        .items_center()
                                        .gap(px(6.0))
                                        .child(
                                            div()
                                                .w(px(80.0))
                                                .flex_none()
                                                .text_color(th.fg_muted)
                                                .text_size(px(12.0))
                                                .child("类型"),
                                        )
                                        .children(kind_chips),
                                )
                                .child(
                                    div()
                                        .flex()
                                        .items_center()
                                        .gap(px(8.0))
                                        .child(
                                            div()
                                                .w(px(80.0))
                                                .flex_none()
                                                .text_color(th.fg_muted)
                                                .text_size(px(12.0))
                                                .child("选项"),
                                        )
                                        .child(toggle("cf-ro", "只读", read_only, true).on_click(
                                            cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                                                this.toggle_read_only(cx);
                                            }),
                                        ))
                                        .child(
                                            div()
                                                .id("cf-ssh")
                                                .px(px(8.0))
                                                .py(px(2.0))
                                                .rounded(px(8.0))
                                                .text_size(px(11.0))
                                                .bg(if ssh { th.accent } else { th.surface })
                                                .text_color(if ssh { th.bg } else { th.fg_muted })
                                                .cursor(CursorStyle::PointingHand)
                                                .child(if ssh { "☑ SSH" } else { "☐ SSH" })
                                                .when(kind != DatabaseKind::Sqlite, |d| {
                                                    d.on_click(cx.listener(
                                                        |this, _ev: &ClickEvent, _window, cx| {
                                                            this.toggle_ssh(cx);
                                                        },
                                                    ))
                                                }),
                                        ),
                                )
                                .children(field_rows),
                        )
                        .child(
                            div()
                                .flex()
                                .justify_end()
                                .gap(px(8.0))
                                .px(px(14.0))
                                .py(px(10.0))
                                .border_t(px(1.0))
                                .border_color(th.border)
                                .child(
                                    div()
                                        .flex_1()
                                        .text_color(th.fg_dim)
                                        .text_size(px(11.0))
                                        .child("Tab 切换字段 · Enter 保存 · Esc 取消"),
                                )
                                .child(btn("cf-cancel", "取消", &th).on_click(cx.listener(
                                    |this, _ev: &ClickEvent, _window, cx| {
                                        this.close_conn(cx);
                                    },
                                )))
                                .child(btn("cf-save", "保存", &th).on_click(cx.listener(
                                    |this, _ev: &ClickEvent, _window, cx| {
                                        this.save_conn(cx);
                                    },
                                ))),
                        ),
                )
                .into_any_element(),
        )
    }

    // ─── G2 过滤栏：列/操作符/取值 + AND-OR + 应用/清空 + 预设（可聚焦取值编辑）───

    fn render_filter_bar(&mut self, idx: usize, cx: &mut Context<Self>) -> Stateful<Div> {
        let th = self.theme;
        let editing = self.editing;
        let cursor = self.edit_cursor;
        let logic_and = self.tabs[idx].browse.logic == FilterLogic::And;
        let kind = self.sel_kind().unwrap_or(DatabaseKind::Sqlite);
        // 徽标只数「真正会下发」的条件：空列与方言不支持的 op 不计（对齐 Web 的 condToWire 计数）。
        let n_active = self
            .tabs
            .get(idx)
            .map(|t| {
                t.browse
                    .filters
                    .iter()
                    .filter(|d| filters::draft_to_condition(d, kind).is_some())
                    .count()
            })
            .unwrap_or(0);
        // 先克隆各行视图模型，避免构建期持有 self 借用。
        let rows: Vec<(usize, filters::FilterDraft)> = self
            .tabs
            .get(idx)
            .map(|t| t.browse.filters.iter().cloned().enumerate().collect())
            .unwrap_or_default();

        // 单个取值输入框（展示态；聚焦由容器统一持有 filter_focus）。
        let value_box = |fi: usize, field: u8, text: String, placeholder: &'static str| {
            let focused = editing == Some((fi, field));
            let body = if focused {
                let mut s = text.clone();
                let at = cursor.min(s.len());
                s.insert(at, '▏');
                s
            } else if text.is_empty() {
                format!("⟨{placeholder}⟩")
            } else {
                text.clone()
            };
            div()
                .id(("fv", idx * 100 + fi * 4 + field as usize))
                .px(px(6.0))
                .py(px(2.0))
                .min_w(px(120.0))
                .max_w(px(240.0))
                .truncate()
                .rounded(px(3.0))
                .font_family("monospace")
                .text_size(px(12.0))
                .bg(if focused { th.surface2 } else { th.bg })
                .text_color(if text.is_empty() && !focused {
                    th.fg_dim
                } else {
                    th.fg
                })
                .cursor(CursorStyle::IBeam)
                .child(body)
                .on_click(cx.listener(move |this, _ev: &ClickEvent, window, cx| {
                    this.begin_edit(idx, fi, field);
                    window.focus(&this.filter_focus);
                    cx.notify();
                }))
        };

        let draft_rows: Vec<Div> = rows
            .into_iter()
            .map(|(fi, d)| {
                let meta = filters::op_meta(d.op);
                let supported = filters::op_supported(kind, d.op);
                let col_chip = div()
                    .id(("fc", idx * 100 + fi))
                    .px(px(6.0))
                    .py(px(2.0))
                    .rounded(px(3.0))
                    .bg(th.surface)
                    .text_color(th.accent)
                    .text_size(px(12.0))
                    .max_w(px(140.0))
                    .truncate()
                    .cursor(CursorStyle::PointingHand)
                    .child(if d.column.is_empty() {
                        "选择列".to_string()
                    } else {
                        d.column.clone()
                    })
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                        this.cycle_filter_column(idx, fi, cx);
                    }));
                let op_chip = div()
                    .id(("fop", idx * 100 + fi))
                    .px(px(6.0))
                    .py(px(2.0))
                    .rounded(px(3.0))
                    .bg(th.surface)
                    .text_color(if supported { th.fg } else { th.danger })
                    .text_size(px(12.0))
                    .cursor(CursorStyle::PointingHand)
                    .child(if supported {
                        meta.label.to_string()
                    } else {
                        format!("{}（不支持）", meta.label)
                    })
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                        this.cycle_filter_op(idx, fi, cx);
                    }));
                // 大小写三态只在文本匹配类 op 上出现（behavior.md §13.5）。
                let case_chip: AnyElement = if meta.text_match {
                    div()
                        .id(("fcase", idx * 100 + fi))
                        .px(px(6.0))
                        .py(px(2.0))
                        .rounded(px(3.0))
                        .bg(th.surface)
                        .text_color(if d.case_mode == filters::CaseMode::Default {
                            th.fg_dim
                        } else {
                            th.accent
                        })
                        .text_size(px(12.0))
                        .cursor(CursorStyle::PointingHand)
                        .child(format!("Aa {}", d.case_mode.label()))
                        .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                            this.cycle_filter_case(idx, fi, cx);
                        }))
                        .into_any_element()
                } else {
                    div().into_any_element()
                };
                let unsupported_hint: AnyElement = if supported {
                    div().into_any_element()
                } else {
                    div()
                        .text_color(th.danger)
                        .text_size(px(11.0))
                        .child(
                            filters::op_unsupported_hint(kind, d.op)
                                .unwrap_or("当前数据库不支持该操作符")
                                .to_string(),
                        )
                        .into_any_element()
                };
                let editors: AnyElement = if meta.needs_values {
                    value_box(fi, 2, d.values.clone(), "逗号分隔,如 1,2,x").into_any_element()
                } else if meta.needs_second {
                    div()
                        .flex()
                        .items_center()
                        .gap(px(4.0))
                        .child(value_box(fi, 0, d.value.clone(), "起"))
                        .child(div().text_color(th.fg_dim).text_size(px(11.0)).child("且"))
                        .child(value_box(fi, 1, d.second.clone(), "止"))
                        .into_any_element()
                } else if meta.needs_value {
                    value_box(fi, 0, d.value.clone(), "值").into_any_element()
                } else {
                    div()
                        .px(px(6.0))
                        .text_color(th.fg_dim)
                        .text_size(px(12.0))
                        .child("—")
                        .into_any_element()
                };
                div()
                    .flex()
                    .items_center()
                    .gap(px(6.0))
                    .child(col_chip)
                    .child(op_chip)
                    .child(case_chip)
                    .child(editors)
                    .child(unsupported_hint)
                    .child(
                        div()
                            .id(("frm", idx * 100 + fi))
                            .px(px(5.0))
                            .text_color(th.fg_dim)
                            .cursor(CursorStyle::PointingHand)
                            .hover(|s| s.text_color(th.danger))
                            .child("×")
                            .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                                this.remove_filter(idx, fi, cx);
                            })),
                    )
            })
            .collect();

        let controls = div()
            .flex()
            .items_center()
            .gap(px(6.0))
            .child(
                div()
                    .id(("logic", idx))
                    .px(px(8.0))
                    .py(px(3.0))
                    .rounded(px(4.0))
                    .bg(th.surface2)
                    .text_color(th.accent)
                    .text_size(px(12.0))
                    .cursor(CursorStyle::PointingHand)
                    .child(if logic_and { "AND 且" } else { "OR 或" })
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                        this.toggle_logic(idx, cx);
                    })),
            )
            .child(btn(("fadd", idx), "+ 条件", &th).on_click(
                cx.listener(move |this, _ev: &ClickEvent, _window, cx| this.add_filter(idx, cx)),
            ))
            .child(btn(("fapply", idx), "▶ 应用", &th).on_click(
                cx.listener(move |this, _ev: &ClickEvent, _window, cx| this.apply_filters(idx, cx)),
            ))
            .child(btn(("fclear", idx), "清空", &th).on_click(
                cx.listener(move |this, _ev: &ClickEvent, _window, cx| this.clear_filters(idx, cx)),
            ))
            .child(btn(("fsave", idx), "存预设", &th).on_click(cx.listener(
                move |this, _ev: &ClickEvent, _window, cx| this.save_filter_preset(idx, cx),
            )))
            .child(btn(("fload", idx), "载预设", &th).on_click(cx.listener(
                move |this, _ev: &ClickEvent, _window, cx| this.load_last_preset(idx, cx),
            )))
            .child(
                div()
                    .text_color(th.fg_dim)
                    .text_size(px(11.0))
                    .child(format!("{n_active} 条有效条件 · Enter 应用 · Esc 退出编辑")),
            );

        div()
            .id("filter-bar")
            .flex_none()
            .flex()
            .flex_col()
            .gap(px(5.0))
            .px(px(12.0))
            .py(px(8.0))
            .bg(th.bg_alt)
            .track_focus(&self.filter_focus)
            .on_key_down(cx.listener(|this, ev: &KeyDownEvent, _window, cx| {
                this.on_filter_key(ev, cx);
            }))
            .child(controls)
            .when(draft_rows.is_empty(), |d| {
                d.child(
                    div()
                        .text_color(th.fg_dim)
                        .text_size(px(12.0))
                        .child("无过滤条件——点「+ 条件」新增，或直接「载预设」。"),
                )
            })
            .children(draft_rows)
    }

    // ─── G2 Inspector：选中行的逐字段只读详情（右缘尾板）───

    fn render_inspector(&mut self, idx: usize, cx: &mut Context<Self>) -> Option<Div> {
        let th = self.theme;
        let sel = self
            .preview
            .filter(|p| p.src == GridSrc::Table(idx))
            .map(|p| p.row)?;
        let cols = self.grid_cols(GridSrc::Table(idx))?.to_vec();
        let row = self.grid_rows(GridSrc::Table(idx))?.get(sel)?.clone();
        let field_rows: Vec<Div> = cols
            .iter()
            .enumerate()
            .map(|(ci, c)| {
                let v = row.get(ci).unwrap_or(&Value::Null);
                let raw = value_str(v);
                let is_null = matches!(v, Value::Null);
                div()
                    .flex()
                    .flex_col()
                    .px(px(10.0))
                    .py(px(5.0))
                    .gap(px(2.0))
                    .border_b(px(1.0))
                    .border_color(th.border)
                    .child(
                        div()
                            .flex()
                            .items_center()
                            .gap(px(6.0))
                            .child(
                                div()
                                    .text_color(th.fg_muted)
                                    .text_size(px(11.0))
                                    .font_weight(FontWeight::BOLD)
                                    .truncate()
                                    .child(c.name.clone()),
                            )
                            .child(
                                div()
                                    .text_color(th.fg_dim)
                                    .text_size(px(10.0))
                                    .child(c.data_type.clone()),
                            ),
                    )
                    .child(
                        div()
                            .text_size(px(12.0))
                            .font_family("monospace")
                            .text_color(if is_null { th.fg_dim } else { th.fg })
                            .child(if is_null {
                                "NULL".to_string()
                            } else {
                                clip(&raw, 400)
                            }),
                    )
            })
            .collect();
        Some(
            div()
                .w(px(320.0))
                .flex_none()
                .border_l(px(1.0))
                .border_color(th.border)
                .bg(th.bg_alt)
                .flex()
                .flex_col()
                .child(
                    div()
                        .flex()
                        .items_center()
                        .px(px(12.0))
                        .py(px(8.0))
                        .text_color(th.fg)
                        .text_size(px(12.0))
                        .font_weight(FontWeight::BOLD)
                        .child(format!("行详情 · 第 {} 行", sel + 1))
                        .child(div().flex_1())
                        .child(
                            div()
                                .id("insp-close")
                                .px(px(6.0))
                                .cursor(CursorStyle::PointingHand)
                                .text_color(th.fg_muted)
                                .hover(|d| d.text_color(th.danger))
                                .child("×")
                                .on_click(cx.listener(
                                    move |this, _ev: &ClickEvent, _window, cx| {
                                        this.preview = None;
                                        cx.notify();
                                    },
                                )),
                        ),
                )
                .child(
                    div()
                        .id("insp-scroll")
                        .flex_1()
                        .min_h(px(0.0))
                        .overflow_y_scroll()
                        .children(field_rows),
                ),
        )
    }

    fn render_rail_structure(&mut self, idx: usize, _cx: &mut Context<Self>) -> AnyElement {
        let th = &self.theme;
        let tab = &self.tabs[idx];
        let Some(detail) = &tab.detail else {
            return div()
                .flex_1()
                .flex()
                .items_center()
                .justify_center()
                .text_color(th.fg_dim)
                .child(if tab.loading_detail {
                    "加载中…"
                } else {
                    "无元数据（点 ⟳ 重载）"
                })
                .into_any_element();
        };
        let section = |title: &'static str, rows: Vec<Div>| {
            div()
                .flex_none()
                .flex()
                .flex_col()
                .px(px(12.0))
                .pt(px(10.0))
                .child(
                    div()
                        .pb(px(4.0))
                        .text_color(th.fg_muted)
                        .text_size(px(12.0))
                        .font_weight(FontWeight::BOLD)
                        .child(format!("{title} ({})", rows.len())),
                )
                .children(rows)
        };
        let col_rows: Vec<Div> = detail
            .columns
            .iter()
            .map(|c| {
                div()
                    .flex()
                    .gap(px(12.0))
                    .py(px(2.0))
                    .text_size(px(12.0))
                    .text_color(th.fg)
                    .child(
                        div()
                            .w(px(20.0))
                            .child(if c.is_primary_key { "🔑" } else { "" }.to_string()),
                    )
                    .child(div().w(px(200.0)).truncate().child(c.name.clone()))
                    .child(
                        div()
                            .w(px(160.0))
                            .truncate()
                            .text_color(th.fg_muted)
                            .child(c.data_type.clone()),
                    )
                    .child(
                        div()
                            .w(px(60.0))
                            .child(if c.nullable { "NULL" } else { "NOT NULL" }.to_string()),
                    )
                    .child(
                        div()
                            .flex_1()
                            .truncate()
                            .text_color(th.fg_dim)
                            .child(c.default_value.clone().unwrap_or_default()),
                    )
            })
            .collect();
        let idx_rows: Vec<Div> = detail
            .indexes
            .iter()
            .map(|ix| {
                div()
                    .flex()
                    .gap(px(12.0))
                    .py(px(2.0))
                    .text_size(px(12.0))
                    .text_color(th.fg)
                    .child(div().w(px(200.0)).truncate().child(ix.name.clone()))
                    .child(
                        div()
                            .w(px(80.0))
                            .child(if ix.unique { "UNIQUE" } else { "" }.to_string()),
                    )
                    .child(
                        div().flex_1().truncate().text_color(th.fg_muted).child(
                            ix.columns
                                .iter()
                                .map(|c| c.name.clone())
                                .collect::<Vec<_>>()
                                .join(", "),
                        ),
                    )
            })
            .collect();
        let fk_rows: Vec<Div> = detail
            .fks
            .iter()
            .map(|fk| {
                div()
                    .flex()
                    .gap(px(12.0))
                    .py(px(2.0))
                    .text_size(px(12.0))
                    .text_color(th.fg)
                    .child(div().w(px(200.0)).truncate().child(fk.name.clone()))
                    .child(
                        div()
                            .flex_1()
                            .truncate()
                            .text_color(th.fg_muted)
                            .child(format!(
                                "({}) → {}.{}.({})",
                                fk.columns.join(","),
                                fk.referenced_schema,
                                fk.referenced_table,
                                fk.referenced_columns.join(",")
                            )),
                    )
            })
            .collect();
        let ddl = detail.ddl.clone();
        div()
            .id(("struct-scroll", idx))
            .flex_1()
            .min_h(px(0.0))
            .overflow_y_scroll()
            .flex()
            .flex_col()
            .children(ddl.is_empty().then(|| {
                div()
                    .px(px(12.0))
                    .pt(px(8.0))
                    .text_size(px(11.0))
                    .text_color(th.danger)
                    .child("该驱动不支持 DDL 导出")
            }))
            .child(
                div()
                    .flex()
                    .px(px(12.0))
                    .pt(px(8.0))
                    .gap(px(20.0))
                    .child(section("列", col_rows).flex_1())
                    .child(section("索引", idx_rows).flex_1())
                    .child(section("外键", fk_rows).flex_1()),
            )
            .child(
                div()
                    .px(px(12.0))
                    .pt(px(10.0))
                    .pb(px(4.0))
                    .text_color(th.fg_muted)
                    .text_size(px(12.0))
                    .font_weight(FontWeight::BOLD)
                    .child("DDL"),
            )
            .child(
                div()
                    .id(("ddl-body", idx))
                    .mx(px(12.0))
                    .mb(px(12.0))
                    .p(px(10.0))
                    .rounded(px(6.0))
                    .bg(th.bg_alt)
                    .max_h(px(320.0))
                    .overflow_y_scroll()
                    .child(
                        div()
                            .text_size(px(12.0))
                            .font_family("monospace")
                            .text_color(th.fg)
                            .child(ddl),
                    ),
            )
            .into_any_element()
    }

    fn render_rail_relations(&mut self, idx: usize, cx: &mut Context<Self>) -> AnyElement {
        let th = &self.theme;
        let tab = &self.tabs[idx];
        let Some(detail) = &tab.detail else {
            return div()
                .flex_1()
                .flex()
                .items_center()
                .justify_center()
                .text_color(th.fg_dim)
                .child(if tab.loading_detail {
                    "加载中…"
                } else {
                    "无元数据"
                })
                .into_any_element();
        };
        let link_row = |id: usize, dir: &'static str, text: String, target: (String, String)| {
            div()
                .id(("fk-row", id))
                .flex()
                .gap(px(8.0))
                .px(px(12.0))
                .py(px(5.0))
                .rounded(px(4.0))
                .text_size(px(12.0))
                .text_color(th.fg)
                .cursor(CursorStyle::PointingHand)
                .hover(|d| d.bg(th.hover))
                .child(div().w(px(20.0)).text_color(th.accent).child(dir))
                .child(div().flex_1().truncate().child(text))
                .child(div().text_color(th.fg_dim).child("打开 →"))
                .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                    this.open_table_tab(target.0.clone(), target.1.clone(), cx);
                }))
        };
        let out_rows: Vec<Stateful<Div>> = detail
            .fks
            .iter()
            .enumerate()
            .map(|(i, fk)| {
                link_row(
                    i,
                    "→",
                    format!(
                        "{}: ({}) → {}.{}.({})",
                        fk.name,
                        fk.columns.join(","),
                        fk.referenced_schema,
                        fk.referenced_table,
                        fk.referenced_columns.join(",")
                    ),
                    (fk.referenced_schema.clone(), fk.referenced_table.clone()),
                )
            })
            .collect();
        let in_rows: Vec<Stateful<Div>> = tab
            .incoming
            .as_ref()
            .map(|list| {
                list.iter()
                    .enumerate()
                    .map(|(i, inc)| {
                        link_row(
                            1000 + i,
                            "←",
                            format!(
                                "{}.{}: ({}) → {}",
                                inc.schema,
                                inc.table,
                                inc.fk.columns.join(","),
                                inc.fk.name
                            ),
                            (inc.schema.clone(), inc.table.clone()),
                        )
                    })
                    .collect()
            })
            .unwrap_or_default();
        let incoming_state = if tab.loading_incoming && tab.incoming.is_none() {
            "入向外键扫描中…"
        } else if tab.incoming.is_none() {
            "未扫描"
        } else {
            ""
        };
        div()
            .id(("rel-scroll", idx))
            .flex_1()
            .min_h(px(0.0))
            .overflow_y_scroll()
            .flex()
            .flex_col()
            .child(
                div()
                    .px(px(12.0))
                    .pt(px(10.0))
                    .pb(px(4.0))
                    .text_color(th.fg_muted)
                    .text_size(px(12.0))
                    .font_weight(FontWeight::BOLD)
                    .child(format!("本表引用（出向） ({})", out_rows.len())),
            )
            .when(out_rows.is_empty(), |d| {
                d.child(
                    div()
                        .px(px(16.0))
                        .text_color(th.fg_dim)
                        .text_size(px(12.0))
                        .child("无"),
                )
            })
            .children(out_rows)
            .child(
                div()
                    .px(px(12.0))
                    .pt(px(14.0))
                    .pb(px(4.0))
                    .text_color(th.fg_muted)
                    .text_size(px(12.0))
                    .font_weight(FontWeight::BOLD)
                    .child(format!(
                        "被引用（入向）{}",
                        tab.incoming
                            .as_ref()
                            .map(|l| format!(" ({})", l.len()))
                            .unwrap_or_default()
                    )),
            )
            .when(!incoming_state.is_empty(), |d| {
                d.child(
                    div()
                        .px(px(16.0))
                        .text_color(th.fg_dim)
                        .text_size(px(12.0))
                        .child(incoming_state.to_string()),
                )
            })
            .children(in_rows)
            .into_any_element()
    }

    // ─── 共享网格（可滚动；修复：原结果区无滚动容器）───

    fn render_grid(
        &mut self,
        src: GridSrc,
        cap: Option<usize>,
        sort_ti: Option<usize>,
        cx: &mut Context<Self>,
    ) -> Option<Div> {
        let th = self.theme;
        let cols = self.grid_cols(src)?.to_vec();
        let all_rows = self.grid_rows(src)?.to_vec();
        let sort_info: Option<(String, SortDirection)> =
            sort_ti.and_then(|ti| self.tabs.get(ti).and_then(|t| t.browse.sort.clone()));
        let total = all_rows.len();
        let rows: Vec<Vec<Value>> = all_rows
            .into_iter()
            .take(cap.unwrap_or(usize::MAX).min(total))
            .collect();
        let shown = rows.len();
        // 列宽：按表头+内容（截断后）估计，等宽近似对齐。
        let widths: Vec<f32> = cols
            .iter()
            .enumerate()
            .map(|(ci, c)| {
                let mut w = c.name.chars().count();
                for r in rows.iter() {
                    if let Some(v) = r.get(ci) {
                        w = w.max(clip(&value_str(v), CELL_CLIP).chars().count());
                    }
                }
                ((w as f32) * 7.6 + 16.0).clamp(70.0, 340.0)
            })
            .collect();
        let head = div()
            .flex_none()
            .flex()
            .gap(px(1.0))
            .px(px(8.0))
            .py(px(6.0))
            .bg(th.bg_alt)
            .text_color(th.accent)
            .text_size(px(12.0))
            .children(cols.iter().enumerate().map(|(ci, c)| {
                let arrow = match &sort_info {
                    Some((sc, d)) if *sc == c.name => {
                        if *d == SortDirection::Asc {
                            " ▲"
                        } else {
                            " ▼"
                        }
                    }
                    _ => "",
                };
                let text = format!("{} · {}{arrow}", c.name, c.data_type);
                match sort_ti {
                    None => div()
                        .w(px(widths[ci]))
                        .flex_none()
                        .truncate()
                        .child(text)
                        .into_any_element(),
                    Some(ti) => {
                        let name = c.name.clone();
                        div()
                            .id(("sort", src_key(src) + ci))
                            .w(px(widths[ci]))
                            .flex_none()
                            .truncate()
                            .cursor(CursorStyle::PointingHand)
                            .hover(|d| d.bg(th.hover))
                            .child(text)
                            .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                                this.toggle_sort(ti, name.clone(), cx);
                            }))
                            .into_any_element()
                    }
                }
            }));
        let edit_ti: Option<usize> = match src {
            GridSrc::Table(i) => Some(i),
            _ => None,
        };
        let editable = edit_ti.is_some_and(|i| self.browse_editable(i));
        // 表网格：用「待提交改动覆盖后的显示值」替换原始值，并标记脏格。
        type CellOverlays = (Option<Vec<Vec<String>>>, Option<Vec<Vec<bool>>>);
        let (disp, dirty): CellOverlays = match edit_ti {
            Some(i) => {
                let d: Vec<Vec<String>> = (0..shown)
                    .map(|ri| {
                        (0..cols.len())
                            .map(|ci| self.browse_cell_text(i, ri, ci))
                            .collect()
                    })
                    .collect();
                let f: Vec<Vec<bool>> = (0..shown)
                    .map(|ri| {
                        (0..cols.len())
                            .map(|ci| self.browse_cell_dirty(i, ri, ci))
                            .collect()
                    })
                    .collect();
                (Some(d), Some(f))
            }
            None => (None, None),
        };
        let edit_cell = self.cell_edit;
        let cell_focus = self.cell_focus.clone();
        let edit_disp: Option<String> = self.cell_edit.map(|_| {
            let mut s = self.cell_buf.clone();
            let at = self.cell_cursor.min(s.len());
            s.insert(at, '▏');
            s
        });
        let body_rows = rows.into_iter().enumerate().map(|(ri, row)| {
            let row_sel = self.preview.is_some_and(|p| p.src == src && p.row == ri);
            div()
                .flex()
                .gap(px(1.0))
                .px(px(8.0))
                .py(px(3.0))
                .bg(if ri % 2 == 1 { th.stripe } else { th.bg })
                .children(row.iter().enumerate().map(|(ci, v)| {
                    let active = self
                        .preview
                        .is_some_and(|p| p.src == src && p.row == ri && p.col == ci);
                    let text = match &disp {
                        Some(d) => d[ri][ci].clone(),
                        None => clip(&value_str(v), CELL_CLIP).to_string(),
                    };
                    let is_dirty = dirty.as_ref().is_some_and(|f| f[ri][ci]);
                    let editing_this = matches!(edit_cell, Some((i, r, c))
                        if edit_ti == Some(i) && r == ri && c == ci);
                    if editing_this {
                        return div()
                            .id(("cell-edit", src_key(src) + ri * 1024 + ci))
                            .w(px(widths[ci]))
                            .flex_none()
                            .px(px(2.0))
                            .rounded(px(2.0))
                            .track_focus(&cell_focus)
                            .on_key_down(cx.listener(|this, ev: &KeyDownEvent, _window, cx| {
                                this.on_cell_key(ev, cx);
                            }))
                            .bg(th.surface2)
                            .border(px(1.0))
                            .border_color(th.accent)
                            .text_color(th.accent)
                            .text_size(px(12.0))
                            .font_family("monospace")
                            .child(edit_disp.clone().unwrap_or_default())
                            .into_any_element();
                    }
                    div()
                        .id(("cell", src_key(src) + ri * 1024 + ci))
                        .w(px(widths[ci]))
                        .flex_none()
                        .truncate()
                        .cursor(CursorStyle::PointingHand)
                        .rounded(px(2.0))
                        .bg(if active {
                            th.surface2
                        } else if is_dirty {
                            th.warn
                        } else if row_sel {
                            th.hover
                        } else {
                            th.bg
                        })
                        .text_color(th.fg)
                        .text_size(px(12.0))
                        .font_family("monospace")
                        .child(text)
                        .on_click(cx.listener(move |this, ev: &ClickEvent, window, cx| {
                            if ev.is_right_click() {
                                if let Some(p) = ev.mouse_position() {
                                    this.ctx_menu = Some(crate::app::CellMenuState {
                                        x: f32::from(p.x),
                                        y: f32::from(p.y),
                                        src,
                                        row: ri,
                                        col: ci,
                                    });
                                }
                                cx.notify();
                                return;
                            }
                            if editable && ev.click_count() >= 2 {
                                if let GridSrc::Table(i) = src {
                                    this.begin_cell_edit(i, ri, ci, window, cx);
                                    return;
                                }
                            }
                            this.ctx_menu = None;
                            let tab = this
                                .grid_cell(src, ri, ci)
                                .and_then(try_pretty_json)
                                .map_or(PreviewTab::Text, |_| PreviewTab::Json);
                            this.preview = Some(crate::app::PreviewState {
                                src,
                                row: ri,
                                col: ci,
                                tab,
                            });
                            cx.notify();
                        }))
                        .into_any_element()
                }))
        });
        let more = (total > shown).then(|| {
            div()
                .px(px(12.0))
                .py(px(4.0))
                .text_color(th.fg_dim)
                .text_size(px(11.0))
                .child(format!("… 仅显示前 {shown} 行（共 {total} 行）"))
        });
        Some(
            div()
                .flex_1()
                .min_h(px(0.0))
                .flex()
                .flex_col()
                .child(head)
                .child(
                    div()
                        .id(("grid-scroll", src_key(src)))
                        .flex_1()
                        .min_h(px(0.0))
                        .overflow_y_scroll()
                        .children(body_rows)
                        .children(more),
                ),
        )
    }

    // ─── 单元格预览面板（作用于当前视图的网格源）───

    fn render_preview(&self, scope: GridSrc, cx: &mut Context<Self>) -> Option<Div> {
        let p = self.preview.filter(|pr| pr.src == scope)?;
        let cols = self.grid_cols(scope)?;
        let col = cols.get(p.col)?;
        let v = self.grid_cell(scope, p.row, p.col)?;
        let th = &self.theme;
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
                .bg(if active { th.surface } else { th.bg_alt })
                .text_color(if active { th.fg } else { th.fg_dim })
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
                .bg(th.bg_alt)
                .child(div().h(px(1.0)).bg(th.border))
                .child(
                    div()
                        .flex()
                        .items_center()
                        .gap(px(8.0))
                        .px(px(12.0))
                        .py(px(6.0))
                        .text_color(th.fg_muted)
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
                                .text_color(th.fg_dim)
                                .child(format!("{} 字符", raw.chars().count())),
                        )
                        .child(
                            div()
                                .id("ptab-close")
                                .px(px(6.0))
                                .cursor(CursorStyle::PointingHand)
                                .text_color(th.fg_muted)
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
                        .text_color(th.danger)
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
                                .text_color(th.fg)
                                .child(body),
                        ),
                ),
        )
    }

    // ─── D3 执行计划浮层（EXPLAIN 树/表格文本，语义对齐 Web 与 TUI）───

    fn render_explain_modal(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let e = self.explain.as_ref()?;
        let th = &self.theme;
        let body = e.lines.join("\n");
        let clip = body.clone();
        let n = e.lines.len();
        let sql = e.sql.clone();
        Some(
            div()
                .id("explain-backdrop")
                .absolute()
                .inset_0()
                .flex()
                .items_start()
                .justify_center()
                .pt(px(56.0))
                .bg(Rgba {
                    r: 0.0,
                    g: 0.0,
                    b: 0.0,
                    a: 0.45,
                })
                .on_click(cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                    this.explain = None;
                    cx.notify();
                }))
                .child(
                    div()
                        .id("explain-modal")
                        .w(px(680.0))
                        .max_h(px(460.0))
                        .flex()
                        .flex_col()
                        .rounded(px(8.0))
                        .border(px(1.0))
                        .border_color(th.border)
                        .bg(th.bg_alt)
                        .on_click(cx.listener(|_this, _ev: &ClickEvent, _window, cx| {
                            cx.stop_propagation();
                        }))
                        .child(
                            div()
                                .flex()
                                .items_center()
                                .gap(px(8.0))
                                .px(px(12.0))
                                .py(px(8.0))
                                .border_b(px(1.0))
                                .border_color(th.border)
                                .child(
                                    div()
                                        .text_color(th.fg)
                                        .text_size(px(13.0))
                                        .font_weight(FontWeight::BOLD)
                                        .child(format!("⚙ 执行计划 · {n} 行")),
                                )
                                .child(
                                    div()
                                        .flex_1()
                                        .text_size(px(11.0))
                                        .text_color(th.fg_dim)
                                        .child("Esc / 点击外部关闭"),
                                )
                                .child(btn("explain-copy", "⧉ 复制", th).on_click(cx.listener(
                                    move |this, _ev: &ClickEvent, _window, cx| {
                                        cx.write_to_clipboard(ClipboardItem::new_string(
                                            clip.clone(),
                                        ));
                                        this.status = "已复制执行计划".into();
                                        cx.notify();
                                    },
                                )))
                                .child(
                                    div()
                                        .id("explain-close")
                                        .px(px(6.0))
                                        .cursor(CursorStyle::PointingHand)
                                        .text_color(th.fg_muted)
                                        .child("×")
                                        .on_click(cx.listener(
                                            |this, _ev: &ClickEvent, _window, cx| {
                                                this.explain = None;
                                                cx.notify();
                                            },
                                        )),
                                ),
                        )
                        .child(
                            div()
                                .id("explain-sql")
                                .flex_none()
                                .max_h(px(72.0))
                                .overflow_y_scroll()
                                .px(px(12.0))
                                .py(px(6.0))
                                .text_size(px(11.0))
                                .font_family("monospace")
                                .text_color(th.fg_muted)
                                .child(sql),
                        )
                        .child(
                            div()
                                .id("explain-body")
                                .flex_1()
                                .min_h(px(0.0))
                                .overflow_y_scroll()
                                .px(px(12.0))
                                .pb(px(12.0))
                                .text_size(px(12.0))
                                .font_family("monospace")
                                .text_color(th.fg)
                                .child(body),
                        ),
                )
                .into_any_element(),
        )
    }

    // ─── 右键菜单（预览 / 复制内容 / 复制列名）───

    fn render_ctx_menu(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let m = self.ctx_menu?;
        let cols = self.grid_cols(m.src)?;
        let col = cols.get(m.col)?;
        let v = self.grid_cell(m.src, m.row, m.col)?;
        let th = &self.theme;
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
                .text_color(th.fg)
                .text_size(px(12.0))
                .hover(|d| d.bg(th.surface))
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
                        .bg(th.bg_alt)
                        .rounded(px(6.0))
                        .child(
                            div()
                                .px(px(10.0))
                                .pt(px(4.0))
                                .pb(px(6.0))
                                .text_color(th.fg_dim)
                                .text_size(px(10.0))
                                .child(format!("{} · {}", col_name, col.data_type).to_uppercase()),
                        )
                        .child(item("ctx-preview", "🔍 预览单元格").on_click(cx.listener(
                            move |this, _ev: &ClickEvent, _window, cx| {
                                cx.stop_propagation();
                                this.preview = Some(crate::app::PreviewState {
                                    src: m.src,
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

impl Render for PolyDBApp {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        if self.quick_open {
            window.focus(&self.quick_focus);
        }
        self.render_root(cx)
    }
}
