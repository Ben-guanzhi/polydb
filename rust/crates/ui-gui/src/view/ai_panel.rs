//! AI 助手面板（polydb 版，移植自 dbstudio `crates/ui/src/components/ai_panel.rs`）。
//!
//! 能力：聊天问答、对当前编辑器 SQL 做「解释 / 优化」，以及提供者设置
//! （provider / base URL / model / API key，落盘到 `ai_config.json`）。
//!
//! 与 dbstudio 的差异：
//! - 数据来源不用全局 `AppState`，而是持有 `Entity<PolyDBApp>`：
//!   SQL 取 `self.sql`，schema 上下文由 `tables` + `cols_cache` 拼装。
//! - 图标改用文本标签（避免图标名在不同版本间漂移）。

use gpui::prelude::*;
use gpui::{div, px, App, ClickEvent, Context, Entity, IntoElement, Render, SharedString, Subscription, Window};
use gpui_component::{
    button::{Button, ButtonVariants as _},
    form::{field, v_form},
    h_flex,
    input::{Input, InputState},
    label::Label,
    list::ListItem,
    select::{Select, SelectEvent, SelectItem, SelectState},
    scroll::ScrollableElement as _,
    v_flex, ActiveTheme as _, Disableable as _, IndexPath, Selectable as _, Sizable as _,
};

use crate::ai::{self, provider_for, ChatMessage, LlmConfig, Role};
use crate::app::PolyDBApp;

#[derive(Debug, Clone)]
struct AiMessage {
    role: MessageRole,
    content: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum MessageRole {
    User,
    Assistant,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum AiTab {
    Chat,
    Explain,
}

#[derive(Clone)]
struct ProviderOption {
    wire: &'static str,
    title: &'static str,
}

impl SelectItem for ProviderOption {
    type Value = &'static str;

    fn title(&self) -> SharedString {
        self.title.into()
    }

    fn value(&self) -> &Self::Value {
        &self.wire
    }
}

fn all_providers() -> Vec<ProviderOption> {
    vec![
        ProviderOption {
            wire: "openai",
            title: "OpenAI compatible",
        },
        ProviderOption {
            wire: "ollama",
            title: "Ollama (local)",
        },
        ProviderOption {
            wire: "mock",
            title: "Mock (offline)",
        },
    ]
}

pub struct AiPanel {
    app: Entity<PolyDBApp>,
    chat_input: Entity<InputState>,
    messages: Vec<AiMessage>,
    active_tab: AiTab,
    is_loading: bool,
    show_settings: bool,
    selected_provider: String,
    provider_select: Entity<SelectState<Vec<ProviderOption>>>,
    base_url: Entity<InputState>,
    model: Entity<InputState>,
    api_key: Entity<InputState>,
    _subscriptions: Vec<Subscription>,
}

impl AiPanel {
    pub fn view(window: &mut Window, cx: &mut App, app: Entity<PolyDBApp>) -> Entity<Self> {
        cx.new(|cx| Self::new(window, cx, app))
    }

    fn text_input(
        window: &mut Window,
        cx: &mut Context<Self>,
        placeholder: &str,
        masked: bool,
    ) -> Entity<InputState> {
        let placeholder = placeholder.to_string();
        cx.new(|cx| {
            let input = InputState::new(window, cx)
                .placeholder(placeholder)
                .clean_on_escape();
            if masked {
                input.masked(true)
            } else {
                input
            }
        })
    }

    fn new(window: &mut Window, cx: &mut Context<Self>, app: Entity<PolyDBApp>) -> Self {
        let chat_input = cx.new(|cx| {
            InputState::new(window, cx)
                .placeholder("Ask about SQL...")
                .clean_on_escape()
        });

        let config = ai::load_config();
        let provider_index = match config.provider.as_str() {
            "ollama" => 1,
            "openai" | "openai-compatible" => 0,
            _ => 2,
        };
        let provider_select = cx.new(|cx| {
            SelectState::new(
                all_providers(),
                Some(IndexPath::new(provider_index)),
                window,
                cx,
            )
        });
        cx.subscribe_in(&provider_select, window, Self::on_provider_change)
            .detach();

        let base_url =
            Self::text_input(window, cx, "API base URL (blank = provider default)", false);
        let model = Self::text_input(window, cx, "Model (blank = provider default)", false);
        let api_key = Self::text_input(window, cx, "API key", true);

        let mut panel = Self {
            app,
            chat_input,
            messages: Vec::new(),
            active_tab: AiTab::Chat,
            is_loading: false,
            show_settings: false,
            selected_provider: match config.provider.as_str() {
                "ollama" => "ollama".to_string(),
                "mock" => "mock".to_string(),
                _ => "openai".to_string(),
            },
            provider_select,
            base_url,
            model,
            api_key,
            _subscriptions: Vec::new(),
        };
        panel.populate_from(&config, window, cx);
        panel
    }

    fn populate_from(&mut self, config: &LlmConfig, window: &mut Window, cx: &mut Context<Self>) {
        self.base_url.update(cx, |this, cx| {
            this.set_value(config.base_url.clone().unwrap_or_default(), window, cx);
        });
        self.model.update(cx, |this, cx| {
            this.set_value(config.model.clone().unwrap_or_default(), window, cx);
        });
        self.api_key.update(cx, |this, cx| {
            this.set_value(config.api_key.clone().unwrap_or_default(), window, cx);
        });
    }

    fn on_provider_change(
        &mut self,
        _: &Entity<SelectState<Vec<ProviderOption>>>,
        event: &SelectEvent<Vec<ProviderOption>>,
        _window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if let SelectEvent::Confirm(Some(value)) = event {
            self.selected_provider = value.to_string();
            cx.notify();
        }
    }

    fn on_save_settings(&mut self, _window: &mut Window, cx: &mut Context<Self>) {
        let base = self.base_url.read(cx).value().to_string();
        let model = self.model.read(cx).value().to_string();
        let key = self.api_key.read(cx).value().to_string();
        let config = LlmConfig {
            provider: self.selected_provider.clone(),
            base_url: (!base.trim().is_empty()).then(|| base.trim().to_string()),
            model: (!model.trim().is_empty()).then(|| model.trim().to_string()),
            api_key: (!key.trim().is_empty()).then(|| key.trim().to_string()),
        };
        self.show_settings = false;
        ai::save_config(&config);
        cx.notify();
    }

    /// Copy the current conversation into the wire message format for an
    /// OpenAI-compatible chat completion request.
    fn lm_messages(&self) -> Vec<ChatMessage> {
        self.messages
            .iter()
            .map(|m| ChatMessage {
                role: match m.role {
                    MessageRole::User => Role::User,
                    MessageRole::Assistant => Role::Assistant,
                },
                content: m.content.clone(),
            })
            .collect()
    }

    fn system_prompt() -> ChatMessage {
        ChatMessage {
            role: Role::System,
            content: "You are polydb's SQL assistant. Help with SQL queries, database \
                      schema design, and query optimization. Be concise and accurate; \
                      never invent database capabilities."
                .to_string(),
        }
    }

    /// 当前编辑器 SQL（在需要时读取，而非镜像）。
    fn current_sql(&self, cx: &Context<Self>) -> String {
        self.app.read(cx).sql.clone()
    }

    fn has_sql(&self, cx: &Context<Self>) -> bool {
        !self.app.read(cx).sql.trim().is_empty()
    }

    /// 把已加载表/列的 schema 摘要拼成 LLM 上下文。
    fn schema_context(&self, cx: &Context<Self>) -> String {
        let app = self.app.read(cx);
        if app.tables.is_empty() {
            return "No schema loaded for the active connection.".to_string();
        }
        let mut out = String::new();
        for (ti, t) in app.tables.iter().enumerate().take(20) {
            if let Some(cols) = app.cols_cache.get(&ti) {
                let columns: Vec<String> = cols
                    .iter()
                    .map(|c| format!("{} {}", c.name, c.data_type))
                    .collect();
                out.push_str("TABLE ");
                out.push_str(&t.name);
                out.push_str(" (");
                out.push_str(&columns.join(", "));
                out.push_str(")\n");
            } else {
                out.push_str("TABLE ");
                out.push_str(&t.name);
                out.push('\n');
            }
        }
        out
    }

    /// Run an LLM prompt off the UI thread and push the reply (or error) into
    /// the transcript.
    fn run_provider_prompt(&mut self, cx: &mut Context<Self>, prompt_messages: Vec<ChatMessage>) {
        if self.is_loading {
            return;
        }
        let config = ai::load_config();
        let provider = provider_for(&config);
        self.is_loading = true;
        cx.notify();

        cx.spawn(async move |this, cx| {
            let outcome = provider.chat(&prompt_messages).await;
            let _ = this.update(cx, |this, cx| {
                this.is_loading = false;
                let content = match outcome {
                    Ok(resp) => resp.content,
                    Err(e) => format!("AI request failed: {e}"),
                };
                this.messages.push(AiMessage {
                    role: MessageRole::Assistant,
                    content,
                });
                cx.notify();
            });
        })
        .detach();
    }

    fn send_message(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let text = self.chat_input.read(cx).value().to_string();
        if text.trim().is_empty() || self.is_loading {
            return;
        }
        self.messages.push(AiMessage {
            role: MessageRole::User,
            content: text.clone(),
        });
        self.chat_input.update(cx, |input, cx| {
            input.set_value(String::new(), window, cx);
        });

        let mut prompt = Vec::with_capacity(self.messages.len() + 1);
        prompt.push(Self::system_prompt());
        prompt.extend(self.lm_messages());
        self.run_provider_prompt(cx, prompt);
    }

    fn explain_sql(&mut self, cx: &mut Context<Self>) {
        let sql = self.current_sql(cx);
        if sql.trim().is_empty() {
            return;
        }
        self.messages.push(AiMessage {
            role: MessageRole::User,
            content: format!("Explain this SQL:\n```sql\n{}\n```", sql),
        });
        let prompt = vec![
            Self::system_prompt(),
            ChatMessage {
                role: Role::User,
                content: format!("Schema context:\n{}", self.schema_context(cx)),
            },
            ChatMessage {
                role: Role::User,
                content: format!(
                    "Explain this SQL query, point out performance issues and suggest \
                     improvements:\n```sql\n{}\n```",
                    sql
                ),
            },
        ];
        self.run_provider_prompt(cx, prompt);
    }

    fn optimize_sql(&mut self, cx: &mut Context<Self>) {
        let sql = self.current_sql(cx);
        if sql.trim().is_empty() {
            return;
        }
        self.messages.push(AiMessage {
            role: MessageRole::User,
            content: format!("Optimize this SQL:\n```sql\n{}\n```", sql),
        });
        let prompt = vec![
            Self::system_prompt(),
            ChatMessage {
                role: Role::User,
                content: format!("Schema context:\n{}", self.schema_context(cx)),
            },
            ChatMessage {
                role: Role::User,
                content: format!(
                    "Rewrite this SQL query for better performance. Return ONLY the \
                     optimized SQL:\n```sql\n{}\n```",
                    sql
                ),
            },
        ];
        self.run_provider_prompt(cx, prompt);
    }

    fn render_settings_form(&self, cx: &mut Context<Self>) -> impl IntoElement {
        v_flex()
            .size_full()
            .p_3()
            .gap_2()
            .child(
                v_form()
                    .columns(1)
                    .small()
                    .child(
                        field()
                            .col_span(1)
                            .label_indent(false)
                            .label("Provider")
                            .child(Select::new(&self.provider_select)),
                    )
                    .child(
                        field()
                            .col_span(1)
                            .label_indent(false)
                            .label("Base URL")
                            .child(Input::new(&self.base_url)),
                    )
                    .child(
                        field()
                            .col_span(1)
                            .label_indent(false)
                            .label("Model")
                            .child(Input::new(&self.model)),
                    )
                    .child(
                        field()
                            .col_span(1)
                            .label_indent(false)
                            .label("API Key")
                            .child(Input::new(&self.api_key)),
                    ),
            )
            .child(
                h_flex()
                    .justify_end()
                    .gap_2()
                    .child(
                        Button::new("ai-settings-cancel")
                            .label("Cancel")
                            .small()
                            .ghost()
                            .on_click(cx.listener(|this, _: &ClickEvent, _window, cx| {
                                this.show_settings = false;
                                cx.notify();
                            })),
                    )
                    .child(
                        Button::new("ai-settings-save")
                            .label("Save")
                            .small()
                            .primary()
                            .on_click(cx.listener(|this, _: &ClickEvent, window, cx| {
                                this.on_save_settings(window, cx)
                            })),
                    ),
            )
    }
}

impl Render for AiPanel {
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let has_sql = self.has_sql(cx);
        let is_loading = self.is_loading;

        let header = h_flex()
            .justify_between()
            .items_center()
            .px_2()
            .py_1()
            .child(div().text_base().child("AI Assistant"))
            .child(
                h_flex()
                    .items_center()
                    .gap_1()
                    .child(
                        Button::new("ai-explain")
                            .label("Explain")
                            .small()
                            .ghost()
                            .disabled(!has_sql || is_loading)
                            .on_click(cx.listener(|this, _: &ClickEvent, _window, cx| {
                                this.explain_sql(cx);
                            })),
                    )
                    .child(
                        Button::new("ai-optimize")
                            .label("Optimize")
                            .small()
                            .ghost()
                            .disabled(!has_sql || is_loading)
                            .on_click(cx.listener(|this, _: &ClickEvent, _window, cx| {
                                this.optimize_sql(cx);
                            })),
                    )
                    .child(
                        Button::new("ai-settings")
                            .label("Settings")
                            .small()
                            .ghost()
                            .on_click(cx.listener(|this, _: &ClickEvent, _window, cx| {
                                this.show_settings = !this.show_settings;
                                cx.notify();
                            })),
                    ),
            );

        let body = if self.show_settings {
            self.render_settings_form(cx).into_any_element()
        } else {
            let tab_bar = h_flex()
                .id("ai-tabs")
                .px_2()
                .py_1()
                .gap_1()
                .child(
                    Button::new("tab-chat")
                        .label("Chat")
                        .small()
                        .ghost()
                        .selected(self.active_tab == AiTab::Chat)
                        .on_click(cx.listener(|this, _: &ClickEvent, _window, cx| {
                            this.active_tab = AiTab::Chat;
                            cx.notify();
                        })),
                )
                .child(
                    Button::new("tab-explain")
                        .label("Explain")
                        .small()
                        .ghost()
                        .selected(self.active_tab == AiTab::Explain)
                        .on_click(cx.listener(|this, _: &ClickEvent, _window, cx| {
                            this.active_tab = AiTab::Explain;
                            cx.notify();
                        })),
                );

            let messages_list = if self.messages.is_empty() {
                v_flex()
                    .flex_1()
                    .items_center()
                    .justify_center()
                    .gap_2()
                    .child(
                        Label::new("Ask me anything about SQL or databases")
                            .text_sm()
                            .text_color(cx.theme().muted_foreground),
                    )
                    .child(
                        Label::new("Configure an AI provider via the Settings button")
                            .text_xs()
                            .text_color(cx.theme().muted_foreground),
                    )
                    .into_any_element()
            } else {
                v_flex()
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scrollbar()
                    .gap_3()
                    .px_2()
                    .py_1()
                    .children(self.messages.iter().enumerate().map(|(ix, msg)| {
                        let is_user = msg.role == MessageRole::User;
                        let bg = if is_user {
                            cx.theme().primary
                        } else {
                            cx.theme().background
                        };
                        let text_color = if is_user {
                            cx.theme().primary_foreground
                        } else {
                            cx.theme().foreground
                        };

                        ListItem::new(ix)
                            .w_full()
                            .py_2()
                            .px_3()
                            .rounded(cx.theme().radius)
                            .bg(bg)
                            .child(
                                v_flex()
                                    .gap_1()
                                    .child(
                                        Label::new(if is_user { "You" } else { "AI" })
                                            .text_xs()
                                            .text_color(text_color),
                                    )
                                    .child(
                                        Label::new(msg.content.clone())
                                            .text_sm()
                                            .text_color(text_color),
                                    ),
                            )
                    }))
                    .when(is_loading, |this| {
                        this.child(
                            h_flex()
                                .px_2()
                                .py_1()
                                .gap_2()
                                .child(
                                    Label::new("Thinking...")
                                        .text_sm()
                                        .text_color(cx.theme().muted_foreground),
                                ),
                        )
                    })
                    .into_any_element()
            };

            let input_bar = h_flex()
                .id("ai-input")
                .px_2()
                .py_2()
                .gap_2()
                .items_center()
                .child(Input::new(&self.chat_input).flex_1())
                .child(
                    Button::new("ai-send")
                        .label("Send")
                        .small()
                        .primary()
                        .ghost()
                        .disabled(is_loading)
                        .on_click(cx.listener(|this, _: &ClickEvent, window, cx| {
                            this.send_message(window, cx);
                        })),
                );

            v_flex()
                .id("ai-body")
                .flex_1()
                .min_h_0()
                .child(tab_bar)
                .child(messages_list)
                .child(input_bar)
                .into_any_element()
        };

        v_flex()
            .id("ai-panel")
            .w(px(320.0))
            .flex_none()
            .size_full()
            .border_l(px(1.0))
            .border_color(cx.theme().border)
            .bg(cx.theme().background)
            .child(header)
            .child(body)
    }
}
