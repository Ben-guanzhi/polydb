//! 数据网格视图：过滤条、单元格检查器、结果网格与预览。

use super::*;

impl PolyDBApp {
    pub(super) fn render_filter_bar(
        &mut self,
        idx: usize,
        cx: &mut Context<Self>,
    ) -> Stateful<Div> {
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

    /// §5.2 旁路取回状态块（Inspector/预览共用）。单槽缓存按 `blob_key` 绑定：
    /// 未取回/换目标 → 可点按钮；取回中 → 状态；失败（含 404 过期文案）→ 可重试；
    /// 已取回 → 字节数 + 前 160 字节 hex 预览。
    fn blob_fetch_block(&self, uuid: &str, len: u64, cx: &mut Context<Self>) -> Stateful<Div> {
        let th = self.theme;
        let active = self.blob_key.as_deref() == Some(uuid);
        let (text, clickable) = if active && self.blob_loading {
            ("取回中…".to_string(), false)
        } else if active && self.blob_err.is_some() {
            (
                format!(
                    "取回失败：{} · 点击重试",
                    self.blob_err.as_deref().unwrap_or_default()
                ),
                true,
            )
        } else if active && self.blob_data.is_some() {
            let data = self.blob_data.as_deref().unwrap_or_default();
            let n = data.len().min(160);
            (
                format!(
                    "已取回 {} 字节 · 前 {n} 字节：\n{}",
                    data.len(),
                    hex_bytes(data, n)
                ),
                false,
            )
        } else {
            (format!("旁路引用 {len} 字节 · 点击取回内容"), true)
        };
        let uuid_owned = uuid.to_string();
        let mut d = div()
            .id(SharedString::from(format!("blob-fetch:{uuid}")))
            .mt(px(4.0))
            .px(px(6.0))
            .py(px(3.0))
            .rounded(px(4.0))
            .bg(th.surface)
            .font_family("monospace")
            .text_size(px(11.0))
            .text_color(if clickable { th.accent } else { th.fg_muted })
            .child(text);
        if clickable {
            d = d
                .cursor(CursorStyle::PointingHand)
                .hover(|d| d.bg(th.surface2))
                .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                    if let Some(conn) = this.sel_id() {
                        this.fetch_blob(conn, uuid_owned.clone(), cx);
                    }
                }));
        }
        d
    }

    pub(super) fn render_inspector(&mut self, idx: usize, cx: &mut Context<Self>) -> Option<Div> {
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
                    .children(
                        blob_ref_of(v).map(|(len, uuid)| self.blob_fetch_block(&uuid, len, cx)),
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

    pub(super) fn render_grid(
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
                        w = w.max(clip(&cell_str(v), CELL_CLIP).chars().count());
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
                        None => clip(&cell_str(v), CELL_CLIP).to_string(),
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

    pub(super) fn render_preview(&self, scope: GridSrc, cx: &mut Context<Self>) -> Option<Div> {
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
                .children(blob_ref_of(v).map(|(len, uuid)| self.blob_fetch_block(&uuid, len, cx)))
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
}
