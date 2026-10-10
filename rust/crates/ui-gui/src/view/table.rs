//! 表对象 tab 视图：四态 rail（概览/数据/结构/关系）+ 导出条 + 变更面板。

use super::*;

impl PolyDBApp {
    pub(super) fn render_table_tab(&mut self, idx: usize, cx: &mut Context<Self>) -> AnyElement {
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

    pub(super) fn render_rail_overview(
        &mut self,
        idx: usize,
        cx: &mut Context<Self>,
    ) -> AnyElement {
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

    pub(super) fn render_rail_data(&mut self, idx: usize, cx: &mut Context<Self>) -> AnyElement {
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
    pub(super) fn render_export_bar(&mut self, src: GridSrc, cx: &mut Context<Self>) -> Div {
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
    pub(super) fn render_changes_panel(
        &mut self,
        idx: usize,
        _cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
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

    pub(super) fn render_rail_structure(
        &mut self,
        idx: usize,
        _cx: &mut Context<Self>,
    ) -> AnyElement {
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

    pub(super) fn render_rail_relations(
        &mut self,
        idx: usize,
        cx: &mut Context<Self>,
    ) -> AnyElement {
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
}
