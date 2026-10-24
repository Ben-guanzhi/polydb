//! 浮层视图：快速切换、连接表单、Explain/结构同步弹层、单元格右键菜单。

use super::*;

// ─── E5.5 结构同步向导：弹层行定高，靠窗口裁剪而非真滚动条（三端「上方还有 N 行」文案才一致）

const CMP_ROWS: usize = 12;
const CMP_STMT_ROWS: usize = 8;
const CMP_RB_ROWS: usize = 5;

/// ③ 步的渲染行：相位组头 + 该组条目（光标只对条目移动，故行号与条目下标分开维护）。
enum CmpRow {
    Head(usize),
    Entry(usize),
}

fn cmp_line(text: impl Into<SharedString>, color: gpui::Rgba, size: f32) -> Div {
    div()
        .px(px(12.0))
        .py(px(2.0))
        .w_full()
        .text_color(color)
        .text_size(px(size))
        .font_family("monospace")
        .child(text.into())
}

fn cmp_list_row(id: impl Into<ElementId>, on: bool, th: &Theme) -> Stateful<Div> {
    div()
        .id(id)
        .w_full()
        .px(px(12.0))
        .py(px(3.0))
        .flex()
        .flex_col()
        .cursor(CursorStyle::PointingHand)
        .bg(if on { th.surface2 } else { th.bg_alt })
        .text_color(th.fg)
        .text_size(px(12.0))
        .font_family("monospace")
        .hover(|d| d.bg(th.hover))
}

fn on_off(b: bool) -> &'static str {
    if b {
        "开"
    } else {
        "关"
    }
}

/// ④ 步的一条语句：序号 + 相位 + SQL（截断）+ 不可逆徽标 + 告警，全部挤在一行内，
/// 这样「行数 == 语句数」，窗口裁剪和失败标记的下标才对得上。
fn cmp_stmt_line(
    s: &polydb_core::DdlStatement,
    i: usize,
    marker: &str,
    failed_ix: Option<usize>,
    th: &Theme,
) -> Div {
    let mark = if failed_ix == Some(i) { "✗" } else { " " };
    let txt = format!("{:>2} [{}] {}", i + 1, s.phase, clip(&s.sql, 78));
    let txt = format!("{marker}{mark}{txt}");
    let warn = s.warnings.join(" | ");
    div()
        .w_full()
        .flex()
        .items_center()
        .gap(px(8.0))
        .child(
            div()
                .flex_1()
                .truncate()
                .text_color(if failed_ix == Some(i) {
                    th.danger
                } else {
                    th.fg
                })
                .child(txt),
        )
        .children(s.non_reversible.then(|| badge("不可逆", th.danger, th)))
        .children((!warn.is_empty()).then(|| {
            div()
                .flex_none()
                .text_color(th.warn)
                .child(format!("! {}", clip(&warn, 36)))
        }))
}

/// 不可点的行（回滚区）：与 `cmp_list_row` 同尺寸，但没有 id / 光标语义。
fn cmp_plain_row(th: &Theme) -> Div {
    div()
        .w_full()
        .px(px(12.0))
        .py(px(3.0))
        .bg(th.bg_alt)
        .text_size(px(12.0))
        .font_family("monospace")
        .text_color(th.fg)
}

fn table_list(tables: &[String]) -> String {
    if tables.is_empty() {
        "无".to_string()
    } else {
        tables.join(",")
    }
}

impl PolyDBApp {
    pub(super) fn render_quick_switcher(&mut self, cx: &mut Context<Self>) -> Option<AnyElement> {
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
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, window, cx| {
                        this.quick_activate(tgt.clone(), window, cx);
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
                        .on_key_down(cx.listener(|this, ev: &KeyDownEvent, window, cx| {
                            this.on_quick_key(ev, window, cx);
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

    pub(super) fn render_conn_form(&mut self, cx: &mut Context<Self>) -> Option<AnyElement> {
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

        // A2：sslmode 建议值点选行（PG/MySQL 才出现；输入框仍可自由键入其他
        // 合法值，点选只是快捷方式）。
        let sslmode_values: &[&str] = match kind {
            DatabaseKind::Postgres => &["disable", "require", "verify-ca", "verify-full"],
            DatabaseKind::MySql => &[
                "DISABLED",
                "PREFERRED",
                "REQUIRED",
                "VERIFY_CA",
                "VERIFY_IDENTITY",
            ],
            _ => &[],
        };
        let ssl_suggestions: Vec<_> = sslmode_values
            .iter()
            .enumerate()
            .map(|(n, val)| {
                let v = (*val).to_string();
                let on = form.get(ConnField::SslMode).eq_ignore_ascii_case(val);
                div()
                    .id(("cf-ssl-sug", n))
                    .px(px(8.0))
                    .py(px(2.0))
                    .rounded(px(8.0))
                    .text_size(px(11.0))
                    .bg(if on { th.accent } else { th.surface })
                    .text_color(if on { th.bg } else { th.fg_muted })
                    .cursor(CursorStyle::PointingHand)
                    .child(v.clone())
                    .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                        this.set_conn_field_value(ConnField::SslMode, v.clone(), cx);
                    }))
            })
            .collect();
        let ssl_row = (!ssl_suggestions.is_empty()).then(|| {
            div()
                .id("cf-ssl-row")
                .flex()
                .items_center()
                .flex_wrap()
                .gap(px(4.0))
                .child(
                    div()
                        .w(px(80.0))
                        .flex_none()
                        .text_color(th.fg_muted)
                        .text_size(px(12.0))
                        .child("建议"),
                )
                .children(ssl_suggestions)
        });

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
                                .children(field_rows)
                                .children(ssl_row),
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

    pub(super) fn render_explain_modal(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
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

    // ─── E5.5 结构同步向导（浮层四步；键位与 TUI 的 viewCompare 同集合）───

    pub(super) fn render_compare_modal(&mut self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let th = self.theme;
        let c = self.compare.as_ref()?;
        let (step, loading, confirming) = (c.step, c.loading, c.confirming);
        let (src_name, tgt_name) = self.cmp_names();
        let base = c.src;
        let fwd_n = c
            .script
            .as_ref()
            .map(|s| s.statements.len())
            .unwrap_or_default();

        let steps = [
            CmpStep::Source,
            CmpStep::Target,
            CmpStep::Review,
            CmpStep::Apply,
        ];
        let glyphs = ["①", "②", "③", "④"];
        let step_chips: Vec<AnyElement> = steps
            .iter()
            .enumerate()
            .map(|(i, s)| {
                let on = *s == step;
                div()
                    .flex_none()
                    .px(px(6.0))
                    .py(px(2.0))
                    .rounded(px(8.0))
                    .bg(if on { th.surface2 } else { th.bg_alt })
                    .text_color(if on { th.accent } else { th.fg_dim })
                    .text_size(px(11.0))
                    .child(format!("{} {}", glyphs[i], s.label()))
                    .into_any_element()
            })
            .collect();

        let mut body: Vec<AnyElement> = Vec::new();
        match step {
            CmpStep::Source | CmpStep::Target => {
                let is_src = step == CmpStep::Source;
                let list = if is_src {
                    self.cmp_source_list()
                } else {
                    self.cmp_target_list()
                };
                let cur = if is_src { c.src_cur } else { c.tgt_cur };
                let title = if is_src {
                    format!(
                        "① 基准侧：要被改成期望侧的样子，也是执行侧（候选 {} 个）",
                        list.len()
                    )
                } else {
                    format!(
                        "② 期望侧：结构以它为准（与「{src_name}」同种类，候选 {} 个）",
                        list.len()
                    )
                };
                body.push(cmp_line(title, th.fg_muted, 12.0).into_any_element());
                if list.is_empty() {
                    body.push(
                        cmp_line(
                            "没有同种类的候选连接（Redis 与跨种类服务端一律拒绝）。",
                            th.fg_dim,
                            12.0,
                        )
                        .into_any_element(),
                    );
                    body.push(
                        cmp_line("先新建一个同类连接，再回到这里比对。", th.fg_dim, 12.0)
                            .into_any_element(),
                    );
                }
                let cur = cur.min(list.len().saturating_sub(1));
                let (start, end) = cmpsync::list_window(list.len(), cur, CMP_ROWS);
                if start > 0 {
                    body.push(
                        cmp_line(format!("↑ 上方还有 {start} 项"), th.fg_dim, 11.0)
                            .into_any_element(),
                    );
                }
                for (i, info) in list.iter().enumerate().take(end).skip(start) {
                    let on = i == cur;
                    let ro = info.read_only == Some(true);
                    let is_base = info.id == base;
                    let target = info
                        .host
                        .clone()
                        .or_else(|| info.database.clone())
                        .unwrap_or_else(|| "—".to_string());
                    let port = info.port.map(|p| format!(":{p}")).unwrap_or_default();
                    let kind = info.kind.to_string();
                    let dot = th.kind_color(&kind);
                    let name = info.name.clone();
                    body.push(
                        cmp_list_row(("cmp-conn", i), on, &th)
                            .child(
                                div()
                                    .w_full()
                                    .flex()
                                    .items_center()
                                    .gap(px(8.0))
                                    .child(div().flex_1().truncate().child(format!(
                                        "{} {name}  {target}{port}",
                                        if on { "▸" } else { " " }
                                    )))
                                    .child(badge(kind, dot, &th))
                                    .children(
                                        is_base
                                            .then(|| badge("基准侧自己：比对必为空", th.warn, &th)),
                                    )
                                    .children(ro.then(|| badge("只读", th.danger, &th))),
                            )
                            .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                                this.cmp_set_cur(i, cx);
                                if is_src {
                                    this.cmp_pick_source(cx);
                                } else {
                                    this.cmp_run_diff(cx);
                                }
                            }))
                            .into_any_element(),
                    );
                }
                if end < list.len() {
                    let n = list.len() - end;
                    body.push(
                        cmp_line(format!("↓ 下方还有 {n} 项"), th.fg_dim, 11.0).into_any_element(),
                    );
                }
            }
            CmpStep::Review => {
                body.push(
                    cmp_line(
                        {
                            let mut head =
                                format!("③ 差异与勾选 · 基准 {src_name} → 期望 {tgt_name}");
                            if let Some((schema, table)) = &c.anchor {
                                head += &format!(" · 锚定 {schema}.{table}（u 解除）");
                            }
                            head
                        },
                        th.fg_muted,
                        12.0,
                    )
                    .into_any_element(),
                );
                if c.entries.is_empty() {
                    body.push(cmp_line("两侧结构一致，无需同步。", th.ok, 12.5).into_any_element());
                } else {
                    let destruct = c
                        .entries
                        .iter()
                        .filter(|e| e.destructive && c.sel.iter().any(|s| s == &e.id))
                        .count();
                    body.push(
                        cmp_line(
                            format!(
                                "共 {} 条变更 · 已勾 {} 条（其中破坏性 {destruct}）· i 忽略默认值={} · n 破坏性默认勾选={}",
                                c.entries.len(),
                                c.sel.len(),
                                on_off(c.ignore_defaults),
                                on_off(c.select_destructive)
                            ),
                            th.fg,
                            12.0,
                        )
                        .into_any_element(),
                    );
                    for g in &c.gaps {
                        body.push(
                            cmp_line(
                                format!(
                                    "⚠ 重建表 {} 的条目未整组勾选（还差 {}）：按 f 补齐",
                                    g.table,
                                    g.missing.join(", ")
                                ),
                                th.warn,
                                11.5,
                            )
                            .into_any_element(),
                        );
                    }
                    let groups = cmpsync::group_by_phase(&c.entries);
                    let heads: Vec<String> = groups
                        .iter()
                        .map(|g| format!("── {}（相位 {}）──", g.label, g.phase))
                        .collect();
                    let mut rows: Vec<CmpRow> = Vec::new();
                    let mut ix = 0usize;
                    for (gi, g) in groups.iter().enumerate() {
                        rows.push(CmpRow::Head(gi));
                        for _ in &g.entries {
                            rows.push(CmpRow::Entry(ix));
                            ix += 1;
                        }
                    }
                    let cursor_row = rows
                        .iter()
                        .position(|r| matches!(r, CmpRow::Entry(i) if *i == c.entry_cur))
                        .unwrap_or(0);
                    let (start, end) = cmpsync::list_window(rows.len(), cursor_row, CMP_ROWS);
                    if start > 0 {
                        body.push(
                            cmp_line(format!("↑ 上方还有 {start} 行"), th.fg_dim, 11.0)
                                .into_any_element(),
                        );
                    }
                    for (ri, row) in rows.iter().enumerate().take(end).skip(start) {
                        match row {
                            CmpRow::Head(gi) => body.push(
                                cmp_line(heads[*gi].clone(), th.warn, 11.5).into_any_element(),
                            ),
                            CmpRow::Entry(ix) => {
                                let e = &c.entries[*ix];
                                let on = *ix == c.entry_cur;
                                let checked = c.sel.iter().any(|s| s == &e.id);
                                let head = format!(
                                    "{} [{}] {} {}",
                                    if on { "▸" } else { " " },
                                    if checked { "x" } else { " " },
                                    cmpsync::op_label(e.op),
                                    e.table
                                );
                                let detail = match (&e.object, &e.from, &e.to) {
                                    (obj, None, None) => obj.clone(),
                                    (obj, f, t) => format!(
                                        "{}{} {}→{}",
                                        obj,
                                        if obj.is_empty() { "" } else { " · " },
                                        f.clone().unwrap_or_else(|| "∅".to_string()),
                                        t.clone().unwrap_or_else(|| "∅".to_string())
                                    ),
                                };
                                let reason = e.reason.clone();
                                let destructive = e.destructive;
                                let ix = *ix;
                                let mut row = cmp_list_row(("cmp-entry", ri), on, &th).child(
                                    div()
                                        .w_full()
                                        .flex()
                                        .items_center()
                                        .gap(px(8.0))
                                        .child(div().flex_1().truncate().child(head))
                                        .children(
                                            (!detail.is_empty()).then(|| {
                                                div().child(detail).text_color(th.fg_muted)
                                            }),
                                        )
                                        .children(
                                            destructive.then(|| badge("破坏性", th.danger, &th)),
                                        ),
                                );
                                if !reason.is_empty() {
                                    row = row.child(
                                        div()
                                            .pl(px(20.0))
                                            .text_color(th.fg_dim)
                                            .text_size(px(11.0))
                                            .child(reason),
                                    );
                                }
                                body.push(
                                    row.on_click(cx.listener(
                                        move |this, _ev: &ClickEvent, _window, cx| {
                                            this.cmp_set_cur(ix, cx);
                                            this.cmp_toggle_entry(ix, cx);
                                        },
                                    ))
                                    .into_any_element(),
                                );
                            }
                        }
                    }
                    if end < rows.len() {
                        let n = rows.len() - end;
                        body.push(
                            cmp_line(format!("↓ 下方还有 {n} 行"), th.fg_dim, 11.0)
                                .into_any_element(),
                        );
                    }
                }
                if fwd_n > 0 {
                    body.push(
                        cmp_line(
                            format!("④ 已有脚本 {fwd_n} 条：按 g 重新生成后可直接执行"),
                            th.fg_dim,
                            11.0,
                        )
                        .into_any_element(),
                    );
                }
            }
            CmpStep::Apply => {
                body.push(
                    cmp_line(
                        format!("④ 脚本与应用 · 基准 {src_name} → 期望 {tgt_name}"),
                        th.fg_muted,
                        12.0,
                    )
                    .into_any_element(),
                );
                if c.script.is_none() {
                    body.push(
                        cmp_line("（还没有脚本：回第三步按 g 生成）", th.fg_dim, 12.5)
                            .into_any_element(),
                    );
                }
            }
        }

        // ④ 步正文放在 match 之后渲染：这里 `script` 必然是 Some，省掉一整层早退分支。
        if step == CmpStep::Apply {
            if let Some(script) = c.script.as_ref() {
                let f = cmpsync::summarize_script(&script.statements);
                let r = cmpsync::summarize_script(&script.rollback);
                body.push(
                    cmp_line(
                        format!(
                            "正向 {} 条（不可逆 {} · 告警 {} · 表 {}）· 回滚 {} 条（不可逆 {}）",
                            f.total,
                            f.non_reversible,
                            f.warned,
                            table_list(&f.tables),
                            r.total,
                            r.non_reversible
                        ),
                        th.fg,
                        12.0,
                    )
                    .into_any_element(),
                );
                body.push(
                    cmp_line(
                        format!("执行方式：{}", cmpsync::exec_mode(script)),
                        if script.ddl_in_tx { th.ok } else { th.warn },
                        11.5,
                    )
                    .into_any_element(),
                );
                if c.applied {
                    body.push(
                        cmp_line("正向脚本已成功执行 ⇒ 可代跑回滚（r）", th.ok, 11.5)
                            .into_any_element(),
                    );
                }
                let failed_ix = c
                    .outcome
                    .as_ref()
                    .and_then(|o| o.failed.first().map(|x| x.index));
                body.push(cmp_line("正向语句：", th.fg_muted, 11.5).into_any_element());
                let cur = c.stmt_cur.min(f.total.saturating_sub(1));
                let (start, end) = cmpsync::list_window(f.total, cur, CMP_STMT_ROWS);
                if start > 0 {
                    body.push(
                        cmp_line(format!("↑ 上方还有 {start} 条"), th.fg_dim, 11.0)
                            .into_any_element(),
                    );
                }
                for i in start..end {
                    let s = &script.statements[i];
                    let txt = cmp_stmt_line(s, i, if i == cur { "▸" } else { " " }, failed_ix, &th);
                    body.push(
                        cmp_list_row(("cmp-stmt", i), i == cur, &th)
                            .child(txt)
                            .on_click(cx.listener(move |this, _ev: &ClickEvent, _window, cx| {
                                this.cmp_set_cur(i, cx);
                            }))
                            .into_any_element(),
                    );
                }
                if end < f.total {
                    let n = f.total - end;
                    body.push(
                        cmp_line(format!("↓ 下方还有 {n} 条"), th.fg_dim, 11.0).into_any_element(),
                    );
                }
                body.push(
                    cmp_line("回滚语句（建议脚本，r 代跑）：", th.fg_muted, 11.5)
                        .into_any_element(),
                );
                if r.total == 0 {
                    body.push(cmp_line("  （空）", th.fg_dim, 11.5).into_any_element());
                }
                for i in 0..r.total.min(CMP_RB_ROWS) {
                    body.push(
                        cmp_plain_row(&th)
                            .child(cmp_stmt_line(&script.rollback[i], i, " ", failed_ix, &th))
                            .into_any_element(),
                    );
                }
                if r.total > CMP_RB_ROWS {
                    let n = r.total - CMP_RB_ROWS;
                    body.push(
                        cmp_line(
                            format!("↓ 回滚还有 {n} 条（r 代跑时整体执行）"),
                            th.fg_dim,
                            11.0,
                        )
                        .into_any_element(),
                    );
                }
                if let Some(o) = c.outcome.as_ref() {
                    let run = if o.rollback { "回滚" } else { "正向" };
                    if o.failed.is_empty() {
                        body.push(
                            cmp_line(
                                format!("{run}脚本执行成功：{} 条已执行", o.ran),
                                th.ok,
                                12.0,
                            )
                            .into_any_element(),
                        );
                    } else {
                        let fm = &o.failed[0];
                        let suffix = if o.rolled_back {
                            "，事务已回滚"
                        } else {
                            "，未回滚（无开放事务可回，或该方言 DDL 已隐式提交）"
                        };
                        body.push(
                            cmp_line(
                                format!(
                                    "{run}脚本中断：已执行 {} 条，第 {} 条失败，跳过 {} 条{suffix}",
                                    o.ran,
                                    fm.index + 1,
                                    o.skipped
                                ),
                                th.danger,
                                12.0,
                            )
                            .into_any_element(),
                        );
                        body.push(
                            cmp_line(format!("  SQL: {}", fm.sql), th.fg_dim, 11.0)
                                .into_any_element(),
                        );
                        body.push(
                            cmp_line(format!("  原因: {}", fm.reason), th.fg_dim, 11.0)
                                .into_any_element(),
                        );
                        if !fm.table.is_empty() {
                            body.push(
                                cmp_line(
                                    format!(
                                        "  相位 {} · 表 {} · 条目 {}",
                                        fm.phase,
                                        fm.table,
                                        fm.entries.join(", ")
                                    ),
                                    th.fg_dim,
                                    11.0,
                                )
                                .into_any_element(),
                            );
                        }
                    }
                }
                if confirming {
                    body.push(
                        cmp_line(
                            format!("回滚强确认：请逐字输入基准侧连接名「{src_name}」"),
                            th.warn,
                            12.0,
                        )
                        .into_any_element(),
                    );
                    let buf = c.confirm_buf.clone();
                    let at = crate::util::char_ceil(&buf, c.confirm_cursor.min(buf.len()));
                    body.push(
                        cmp_line(format!("▸ {}▏{}", &buf[..at], &buf[at..]), th.fg, 13.0)
                            .into_any_element(),
                    );
                }
            }
        }

        let footer = self.cmp_footer_text().to_string();
        let header = div()
            .w_full()
            .flex_none()
            .items_center()
            .gap(px(8.0))
            .px(px(12.0))
            .py(px(8.0))
            .border_b(px(1.0))
            .border_color(th.border)
            .child(
                div()
                    .flex_none()
                    .text_color(th.fg)
                    .text_size(px(13.0))
                    .font_weight(FontWeight::BOLD)
                    .child("⇄ 结构同步向导"),
            )
            .children(step_chips)
            .child(
                div()
                    .flex_1()
                    .truncate()
                    .text_size(px(11.0))
                    .text_color(th.fg_dim)
                    .child(format!("{src_name} → {tgt_name}")),
            )
            .child(
                div()
                    .id("cmp-close")
                    .flex_none()
                    .px(px(6.0))
                    .cursor(CursorStyle::PointingHand)
                    .text_color(th.fg_muted)
                    .child("×")
                    .on_click(cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                        this.close_compare(cx);
                    })),
            );
        Some(
            div()
                .id("cmp-backdrop")
                .absolute()
                .inset_0()
                .flex()
                .items_start()
                .justify_center()
                .pt(px(28.0))
                .bg(Rgba {
                    r: 0.0,
                    g: 0.0,
                    b: 0.0,
                    a: 0.45,
                })
                .on_click(cx.listener(|this, _ev: &ClickEvent, _window, cx| {
                    // 在途时点空白不收窗：脚本执行到一半被关掉，屏幕上就没有失败详情了。
                    if !this.compare.as_ref().is_some_and(|c| c.loading) {
                        this.close_compare(cx);
                    }
                }))
                .child(
                    div()
                        .id("compare-wizard")
                        .w(px(760.0))
                        .max_h(px(600.0))
                        .flex()
                        .flex_col()
                        .rounded(px(8.0))
                        .border(px(1.0))
                        .border_color(th.border)
                        .bg(th.bg_alt)
                        .track_focus(&self.cmp_focus)
                        .key_context("CompareWizard")
                        .on_key_down(cx.listener(|this, ev: &KeyDownEvent, _window, cx| {
                            this.on_cmp_key(ev, cx);
                        }))
                        .on_click(cx.listener(|_this, _ev: &ClickEvent, _window, cx| {
                            cx.stop_propagation();
                        }))
                        .child(header)
                        .child(
                            div()
                                .id("cmp-body")
                                .flex_1()
                                .min_h(px(0.0))
                                .flex()
                                .flex_col()
                                .overflow_y_scroll()
                                .children(body),
                        )
                        .child(
                            div()
                                .w_full()
                                .flex_none()
                                .items_center()
                                .gap(px(8.0))
                                .px(px(12.0))
                                .py(px(6.0))
                                .border_t(px(1.0))
                                .border_color(th.border)
                                .child(
                                    div()
                                        .flex_1()
                                        .text_size(px(11.0))
                                        .text_color(th.fg_dim)
                                        .child(footer),
                                )
                                .children(loading.then(|| {
                                    div()
                                        .flex_none()
                                        .text_size(px(11.0))
                                        .text_color(th.warn)
                                        .child("⏳ 在途…")
                                })),
                        ),
                )
                .into_any_element(),
        )
    }

    // ─── 右键菜单（预览 / 复制内容 / 复制列名）───

    pub(super) fn render_ctx_menu(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
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
