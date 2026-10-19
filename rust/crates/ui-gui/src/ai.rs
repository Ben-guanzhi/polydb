//! LLM 提供者与 AI 配置（polydb 版，移植自 dbstudio `crates/core/src/ai.rs`）。
//!
//! 能力：
//! - `LlmProvider` trait + `OpenAiCompatibleProvider`（OpenAI / Ollama 通用
//!   `POST {base}/chat/completions` 契约）+ `MockProvider`（未配置时离线兜底）。
//! - `provider_for(config)` 依据存储的配置构造提供者；未知 provider 回退 Mock。
//! - 配置持久化：`~/.config/polydb/ai_config.json`（Windows: `%APPDATA%\polydb`），
//!   与 `presets.rs` 同目录约定。
//!
//! 与 dbstudio 的差异：HTTP 用 `ureq` 同步阻塞，跑在专用 worker 线程池上，
//! 不占用 gpui 的异步执行器；配置落盘走本地 JSON 而非全局态。

use anyhow::Result;
use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use std::path::PathBuf;

use crate::presets::config_dir;

/// Configuration for an LLM provider.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LlmConfig {
    pub provider: String,
    #[serde(default)]
    pub api_key: Option<String>,
    #[serde(default)]
    pub model: Option<String>,
    #[serde(default)]
    pub base_url: Option<String>,
}

impl Default for LlmConfig {
    fn default() -> Self {
        Self {
            provider: "openai".to_string(),
            api_key: None,
            model: Some("gpt-4o-mini".to_string()),
            base_url: None,
        }
    }
}

/// A message in a conversation.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChatMessage {
    pub role: Role,
    pub content: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub enum Role {
    System,
    User,
    Assistant,
}

/// Response from an LLM completion.
#[derive(Debug, Clone)]
pub struct CompletionResponse {
    pub content: String,
    pub tokens_used: Option<u32>,
}

/// Trait for LLM providers. Implement this to add new AI backends.
#[async_trait]
pub trait LlmProvider: Send + Sync {
    /// Get the name of this provider.
    fn name(&self) -> &str;

    /// Check if this provider is configured and ready.
    fn is_configured(&self) -> bool;

    /// Send a chat completion request.
    async fn chat(&self, messages: &[ChatMessage]) -> Result<CompletionResponse>;

    /// Send a simple completion request with a single prompt.
    async fn complete(&self, prompt: &str) -> Result<CompletionResponse> {
        self.chat(&[ChatMessage {
            role: Role::User,
            content: prompt.to_string(),
        }])
        .await
    }
}

/// A mock provider for testing or when no AI is configured.
pub struct MockProvider;

#[async_trait]
impl LlmProvider for MockProvider {
    fn name(&self) -> &str {
        "mock"
    }

    fn is_configured(&self) -> bool {
        true
    }

    async fn chat(&self, messages: &[ChatMessage]) -> Result<CompletionResponse> {
        let last_message = messages
            .last()
            .map(|m| m.content.as_str())
            .unwrap_or("No message");

        Ok(CompletionResponse {
            content: format!(
                "[Mock AI Response] This is a placeholder response. \
                 Configure an AI provider in settings to get real responses.\n\n\
                 Your message: {}",
                last_message
            ),
            tokens_used: None,
        })
    }
}

/// The `openai` / `ollama` backends use the same HTTP contract: an
/// OpenAI-compatible `POST {base}/chat/completions` endpoint.
#[derive(Debug, Clone)]
pub struct OpenAiCompatibleProvider {
    pub config: LlmConfig,
}

fn role_str(role: &Role) -> &'static str {
    match role {
        Role::System => "system",
        Role::User => "user",
        Role::Assistant => "assistant",
    }
}

/// Resolve the effective endpoint URL and model name for a config, applying
/// per-provider defaults when the user left a field blank.
fn endpoint_for(config: &LlmConfig) -> (String, String) {
    let is_ollama = config.provider == "ollama";
    let base = config.base_url.clone().unwrap_or_else(|| {
        if is_ollama {
            "http://localhost:11434/v1".to_string()
        } else {
            "https://api.openai.com/v1".to_string()
        }
    });
    let model = config.model.clone().unwrap_or_else(|| {
        if is_ollama {
            "llama3".to_string()
        } else {
            "gpt-4o-mini".to_string()
        }
    });
    (
        format!("{}/chat/completions", base.trim_end_matches('/')),
        model,
    )
}

/// Blocking call run on a helper thread; [`OpenAiCompatibleProvider::chat`]
/// bridges to it so the UI/async executor is never blocked by HTTP I/O.
fn openai_compatible_chat(
    config: &LlmConfig,
    messages: &[ChatMessage],
) -> Result<CompletionResponse> {
    use serde_json::{json, Value};

    let (url, model) = endpoint_for(config);
    let msgs: Vec<Value> = messages
        .iter()
        .map(|m| json!({ "role": role_str(&m.role), "content": m.content }))
        .collect();
    let body = json!({ "model": model, "messages": msgs });

    let mut request = ureq::post(&url).set("Content-Type", "application/json");
    if let Some(key) = config.api_key.as_deref().filter(|k| !k.is_empty()) {
        request = request.set("Authorization", &format!("Bearer {key}"));
    }

    let text = request
        .send_string(&body.to_string())
        .map_err(|err| match err {
            ureq::Error::Status(code, resp) => anyhow::anyhow!(
                "LLM returned HTTP {code}: {}",
                resp.into_string().unwrap_or_default()
            ),
            other => anyhow::anyhow!("LLM request failed: {other}"),
        })?
        .into_string()
        .map_err(|err| anyhow::anyhow!("failed to read LLM response: {err}"))?;

    let parsed: Value = serde_json::from_str(&text)
        .map_err(|err| anyhow::anyhow!("LLM response was not JSON: {err}"))?;
    let content = parsed["choices"][0]["message"]["content"]
        .as_str()
        .map(str::to_string)
        .unwrap_or(text);
    let tokens_used = parsed["usage"]["total_tokens"].as_u64().map(|t| t as u32);
    Ok(CompletionResponse {
        content,
        tokens_used,
    })
}

#[async_trait]
impl LlmProvider for OpenAiCompatibleProvider {
    fn name(&self) -> &str {
        &self.config.provider
    }

    fn is_configured(&self) -> bool {
        true
    }

    async fn chat(&self, messages: &[ChatMessage]) -> Result<CompletionResponse> {
        let config = self.config.clone();
        let messages = messages.to_vec();
        let (tx, rx) = async_channel::bounded::<Result<CompletionResponse>>(1);
        chat_jobs()
            .send(Box::new(move || {
                let _ = tx.try_send(openai_compatible_chat(&config, &messages));
            }))
            .map_err(|_| anyhow::anyhow!("AI worker pool is gone"))?;
        rx.recv()
            .await
            .map_err(|err| anyhow::anyhow!("AI worker died before answering: {err}"))?
    }
}

/// Chat requests are bridged onto a small pool of named worker threads that
/// the process builds once and keeps, instead of a fresh OS thread per
/// request: an LLM call is seconds of socket wait.
type ChatJob = Box<dyn FnOnce() + Send + 'static>;

const CHAT_POOL_SIZE: usize = 4;

fn chat_jobs() -> &'static std::sync::mpsc::Sender<ChatJob> {
    static POOL: std::sync::OnceLock<std::sync::mpsc::Sender<ChatJob>> = std::sync::OnceLock::new();
    POOL.get_or_init(|| {
        let (tx, rx) = std::sync::mpsc::channel::<ChatJob>();
        let rx = std::sync::Arc::new(std::sync::Mutex::new(rx));
        for id in 0..CHAT_POOL_SIZE {
            let rx = rx.clone();
            let _ = std::thread::Builder::new()
                .name(format!("ai-worker-{id}"))
                .spawn(move || loop {
                    let job = rx.lock().unwrap_or_else(|e| e.into_inner()).recv();
                    match job {
                        Ok(job) => job(),
                        Err(_) => break,
                    }
                });
        }
        tx
    })
}

/// Build a provider from a stored [`LlmConfig`]. Unknown provider names fall
/// back to the [`MockProvider`] so the app keeps working un-configured.
pub fn provider_for(config: &LlmConfig) -> Box<dyn LlmProvider> {
    match config.provider.as_str() {
        "openai" | "openai-compatible" | "ollama" => Box::new(OpenAiCompatibleProvider {
            config: config.clone(),
        }),
        _ => Box::new(MockProvider),
    }
}

// ─── 配置持久化（本地 JSON，目录同 presets）───

fn config_path() -> Option<PathBuf> {
    Some(config_dir()?.join("ai_config.json"))
}

/// 从磁盘读取 AI 配置；缺失/损坏时回退默认（OpenAI 兼容）。
pub fn load_config() -> LlmConfig {
    let Some(path) = config_path() else {
        return LlmConfig::default();
    };
    match std::fs::read_to_string(&path) {
        Ok(text) => serde_json::from_str(&text).unwrap_or_default(),
        Err(_) => LlmConfig::default(),
    }
}

/// 把 AI 配置写盘；失败静默（不影响运行时使用）。
pub fn save_config(config: &LlmConfig) {
    let Some(path) = config_path() else {
        return;
    };
    if let Some(dir) = path.parent() {
        let _ = std::fs::create_dir_all(dir);
    }
    if let Ok(text) = serde_json::to_string_pretty(config) {
        let _ = std::fs::write(&path, text.as_bytes());
    }
}
