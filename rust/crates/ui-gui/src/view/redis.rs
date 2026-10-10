//! Redis 面板视图：键树详情/浏览器与值渲染辅助。

use super::*;

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
    /// G4.4 Redis 键浏览面板：选库 + SCAN 模式 + 键列表 + 取值详情；
    /// D4 起 string 值可整值写回（其余类型仍只读，需走命令行）。
    pub(super) fn render_redis_panel(&mut self, cx: &mut Context<Self>) -> AnyElement {
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
}
