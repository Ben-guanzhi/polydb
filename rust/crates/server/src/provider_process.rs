//! 阶段 8：进程端点（§16.12.1 kind 2 / §16.13）。与 Go 侧
//! `go/pkg/server/provider_process.go` 严格镜像。
//!
//! 进程端点 = 一个独立 provider 进程，经 **stdio newline-delimited JSON-RPC
//! 2.0** 与宿主通信（§16.12.2）。宿主内部消息是 msgpack 的 ServerMessage，
//! 端点内部负责两种帧之间的映射（§16.13.3）：
//!
//! - 宿主 → 进程：`provider_exec` → `{"method":"provider/exec",...}`
//! - 进程 → 宿主：`provider_result` → 宿主的 QueryResult / QueryError
//!
//! 进程来源**不可由客户端指定**（§16.13.1）：只能来自服务端配置
//! `POLYDB_PROVIDER_PROCS`。

use std::collections::HashMap;
use std::io::{BufRead, BufReader, Write};
use std::process::{Child, ChildStdin, Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicI64, Ordering};
use std::sync::{Arc, Mutex};

use polydb_core::protocol::driver_broker::DriverCapability;
use polydb_core::protocol::error::PolyDBError;
use polydb_core::protocol::query::QueryResult;
use polydb_core::protocol::ws::ServerMessage;
use serde_json::{json, Value as JsonValue};

use super::provider_hub::{EndpointKind, ProviderEndpoint, ProviderHub};

#[cfg(test)]
mod tests;

/// provider 进程协议的版本（§16.12.2）。
pub const PROVIDER_PROTOCOL_VERSION: u32 = 1;

/// 进程端点：一个独立 provider 进程。
pub struct ProcessEndpoint {
    id: u64,
    /// hub 用于把进程回的结果投递给等待方（按 query_id）。
    hub: Arc<ProviderHub>,
    /// key 是本端点提供的 provider key。
    key: String,
    /// cmd_line 是拉起时的完整命令行——配置对账用它匹配要撤下的条目
    ///（§16.19.3 的进程匹配键 = (key, cmd)）。
    cmd_line: String,
    /// 子进程句柄（Close 时 kill + wait 回收，§16.13.4）。
    child: Mutex<Option<Child>>,
    /// OS 级沙箱的清理（Windows：关闭 Job 句柄——KILL_ON_JOB_CLOSE 终止
    /// Job 内全部进程，§16.16.1）。互斥保护：清理只执行一次（Windows 上双关
    /// 句柄是未定义行为）。
    sandbox: Mutex<Option<Box<dyn FnOnce() + Send>>>,

    stdin: Mutex<Option<ChildStdin>>,
    alive: AtomicBool,
    /// pending 是进程内已发出、等待响应的请求 id → query_id 映射。
    pending: Mutex<HashMap<i64, uuid::Uuid>>,
    next_id: AtomicI64,
    /// on_dead 回调：进程不可达时通知 hub 移除登记（§16.13.4）。
    on_dead: Mutex<Option<Box<dyn Fn() + Send + Sync>>>,
}

impl std::fmt::Debug for ProcessEndpoint {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ProcessEndpoint")
            .field("id", &self.id)
            .field("key", &self.key)
            .field("alive", &self.alive.load(Ordering::SeqCst))
            .finish()
    }
}

impl ProcessEndpoint {
    /// 拉起一个 provider 进程并建立 stdio 通道。
    ///
    /// `cmd_line` 是完整命令行（可含参数与引号）。进程须按 §16.12.2 自行发送
    /// `provider/hello` 声明自己的 key 与能力；校验在 read_loop 侧完成。
    pub fn spawn(
        id: u64,
        hub: Arc<ProviderHub>,
        cmd_line: &str,
        key: &str,
    ) -> Result<Arc<Self>, String> {
        let parts = split_command(cmd_line);
        if parts.is_empty() {
            return Err("provider command is empty".to_string());
        }
        let mut command = Command::new(&parts[0]);
        command
            .args(&parts[1..])
            .env(
                super::provider_sandbox::SANDBOX_PARENT_PID_KEY,
                std::process::id().to_string(),
            )
            .env(
                "POLYDB_PROVIDER_MEM_MB",
                super::provider_sandbox::provider_mem_limit_mb().to_string(),
            )
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        // §16.16.1 Unix 行：provider 加入独立进程组（进程隔离 + 进程树级回收
        // 的 Unix 形态）。Windows 的生命周期绑定由 Job Object 兜底（spawn 后）。
        #[cfg(unix)]
        {
            use std::os::unix::process::CommandExt;
            command.process_group(0);
        }
        let mut child = command
            .spawn()
            .map_err(|e| format!("spawn provider {:?}: {}", parts[0], e))?;
        // §16.16：OS 级沙箱——Windows Job Object（KILL_ON_JOB_CLOSE + 内存
        // 上限）；Unix 无宿主侧动作（provider 自限 + 父进程自检）。失败不阻断
        // 启动（记日志、按无沙箱继续，§16.16.3）。
        let sandbox = match super::provider_sandbox::apply_sandbox(&mut child) {
            Ok(cleanup) => Some(cleanup),
            Err(e) => {
                tracing::warn!(error = %e, "provider sandbox unavailable");
                None
            }
        };
        let stdin = child.stdin.take();
        let stdout = child.stdout.take().ok_or("provider stdout unavailable")?;
        let stderr = child.stderr.take();

        let ep = Arc::new(ProcessEndpoint {
            id,
            hub: Arc::clone(&hub),
            key: key.to_string(),
            cmd_line: cmd_line.to_string(),
            child: Mutex::new(Some(child)),
            sandbox: Mutex::new(sandbox),
            stdin: Mutex::new(stdin),
            alive: AtomicBool::new(true),
            pending: Mutex::new(HashMap::new()),
            next_id: AtomicI64::new(0),
            on_dead: Mutex::new(None),
        });

        // 读 stdout：JSON-RPC 响应与 hello 声明。
        let reader = Arc::clone(&ep);
        std::thread::spawn(move || reader.read_loop(stdout));
        // §16.19：登记到 hub 的进程注册表（配置对账按 (key, cmd) 匹配撤下；
        // hello 校验失败的也要能被回收，§16.13.4）。
        hub.processes
            .lock()
            .expect("processes poisoned")
            .push(Arc::clone(&ep));
        // 读 stderr：只做诊断（stdout 只承载协议帧）。
        if let Some(stderr) = stderr {
            std::thread::spawn(move || {
                let reader = BufReader::new(stderr);
                for line in reader.lines() {
                    let _ = line;
                }
            });
        }
        // 回收子进程（不留僵尸，§16.13.4）。
        let waiter = Arc::clone(&ep);
        std::thread::spawn(move || waiter.wait_loop());
        Ok(ep)
    }

    /// 设置进程不可达时的回调（hub 移除登记）。
    pub fn set_on_dead(&self, f: Box<dyn Fn() + Send + Sync>) {
        *self.on_dead.lock().expect("on_dead poisoned") = Some(f);
    }

    /// matches 判定本进程是否为配置对账要撤下的条目（§16.19.3：匹配键 =
    /// (key, cmd)——命令相同的同 key 条目互为多实例）。
    pub(crate) fn matches(&self, key: &str, cmd: &str) -> bool {
        self.key == key && self.cmd_line == cmd
    }

    /// 终止 provider 进程并回收（幂等）。
    ///
    /// **非阻塞**：kill 立即返回，wait 由独立线程收尾（与 Go 侧 waitLoop
    /// goroutine 对齐）——同步 wait 在 tokio task 里会占住 worker，撤下路径
    /// （§16.19）调它时会把对账循环整个卡住。
    pub fn close(&self) {
        let child = self.child.lock().expect("child poisoned").take();
        if let Some(mut child) = child {
            let _ = child.kill();
            std::thread::spawn(move || {
                let _ = child.wait();
            });
        }
        // 释放 OS 级沙箱资源（Windows：关闭 Job 句柄——KILL_ON_JOB_CLOSE
        // 会终止 Job 内全部进程，§16.16.1）。
        if let Some(cleanup) = self.sandbox.lock().expect("sandbox poisoned").take() {
            cleanup();
        }
        self.alive.store(false, Ordering::SeqCst);
    }

    /// 写一帧协议到进程的 stdin（宿主 → 进程方向，§16.12.2）。
    fn write_frame(&self, frame: &JsonValue) -> bool {
        let mut line = frame.to_string();
        line.push('\n');
        let mut guard = self.stdin.lock().expect("stdin poisoned");
        let ok = match guard.as_mut() {
            Some(w) => w.write_all(line.as_bytes()).is_ok(),
            None => false,
        };
        ok
    }

    /// 读 stdout 的 JSON-RPC 行，把响应投递给等待方。
    fn read_loop(self: Arc<Self>, stdout: std::process::ChildStdout) {
        let reader = BufReader::new(stdout);
        for line in reader.lines() {
            let Ok(line) = line else { break };
            if line.trim().is_empty() {
                continue;
            }
            let Ok(frame) = serde_json::from_str::<JsonValue>(&line) else {
                continue;
            };
            // 通知帧：provider/hello 是进程声明自己（§16.13.2）。
            if frame.get("id").is_none() {
                if frame.get("method").and_then(|m| m.as_str()) == Some("provider/hello") {
                    self.handle_hello(&frame);
                }
                continue;
            }
            let Some(id) = frame.get("id").and_then(|i| i.as_i64()) else {
                continue;
            };
            let query_id = {
                let mut pending = self.pending.lock().expect("pending poisoned");
                pending.remove(&id)
            };
            let Some(query_id) = query_id else { continue };
            let outcome = decode_exec_result(&frame);
            self.hub.complete(query_id, outcome);
        }
        self.mark_dead();
    }

    /// 校验进程的 hello 声明并建立 attached 登记（§16.13.2）。
    fn handle_hello(self: &Arc<Self>, frame: &JsonValue) {
        let result = frame.get("result").cloned().unwrap_or(JsonValue::Null);
        let decl_key = result.get("key").and_then(|k| k.as_str()).unwrap_or("");
        let version = result
            .get("protocol_version")
            .and_then(|v| v.as_u64())
            .unwrap_or(0) as u32;
        // §16.18 witnessed coeffect（Thm 47）：provider 自证实例可互换。
        let decl_commutative = result
            .get("commutative")
            .and_then(|c| c.as_bool())
            .unwrap_or(false);
        if version != PROVIDER_PROTOCOL_VERSION || decl_key != self.key {
            // 声明不被接受 → 终止该进程（不登记）。
            self.mark_dead();
            self.close();
            return;
        }
        let caps: Vec<DriverCapability> = result
            .get("capabilities")
            .and_then(|c| c.as_array())
            .map(|arr| {
                arr.iter()
                    .filter_map(|v| v.as_str())
                    .filter_map(parse_capability)
                    .collect()
            })
            .unwrap_or_default();
        if caps.is_empty() {
            self.mark_dead();
            self.close();
            return;
        }
        // 建立登记（attach 内部再查白名单，§16.10.1）。commutative 由
        // provider 自证（§16.18.1 witnessed coeffect）。
        let me: Arc<dyn ProviderEndpoint> = Arc::clone(self) as Arc<dyn ProviderEndpoint>;
        if self
            .hub
            .attach(self.key.clone(), me, decl_commutative)
            .is_err()
        {
            self.mark_dead();
            self.close();
            return;
        }
        // 能力自动注册到 broker（与 broker_register 同一路径）。
        self.hub
            .register_provider_caps(&self.key, caps, decl_commutative);
        // §16.14.2：把回调端点地址与令牌回给进程（provider 据此把 SQL 交回宿主
        // 执行）。ack 走 stdin（宿主 → 进程方向，§16.12.2）。
        self.write_frame(&json!({
            "jsonrpc": "2.0",
            "method": "provider/ack",
            "result": {
                "type": "provider_ack",
                "event": "activating",
                "key": self.key,
                "attached": self.hub.attached_keys(),
                "callback_url": self.hub.callback_url(),
                "callback_token": self.hub.callback_token(),
            },
        }));
    }

    /// 标记进程不可达：清空在途请求并通知 hub 移除登记。
    fn mark_dead(&self) {
        if !self.alive.swap(false, Ordering::SeqCst) {
            return;
        }
        let pending: Vec<uuid::Uuid> = {
            let mut p = self.pending.lock().expect("pending poisoned");
            p.drain().map(|(_, qid)| qid).collect()
        };
        // 在途请求全部失败（不挂起，§16.13.4）。
        for query_id in pending {
            self.hub.complete(
                query_id,
                Err(PolyDBError::new(
                    polydb_core::protocol::error::codes::DRIVER_NOT_AVAILABLE,
                    "provider process exited",
                )),
            );
        }
        let cb = self.on_dead.lock().expect("on_dead poisoned").take();
        if let Some(cb) = cb {
            cb();
        }
    }

    /// 回收子进程（进程自然退出时触发）。
    fn wait_loop(self: Arc<Self>) {
        // **try_wait 轮询**——不能持锁阻塞在 child.wait()：那会让 close()
        // 的 child.lock() 永久阻塞（配置对账的撤下第一次真正调 close，暴露
        // 为 PD-39——此前 close 从未被调用过，缺陷一直潜伏）。短临界区 +
        // try_wait，kill（close）与回收（wait）互不阻塞。
        loop {
            std::thread::sleep(std::time::Duration::from_millis(100));
            let exited = {
                let mut guard = self.child.lock().expect("child poisoned");
                match guard.as_mut() {
                    None => true, // 已被 close take：无需再回收
                    Some(child) => match child.try_wait() {
                        Ok(Some(_)) => true,
                        Ok(None) => false,
                        Err(_) => true,
                    },
                }
            };
            if exited {
                self.mark_dead();
                return;
            }
        }
    }
}

impl ProviderEndpoint for ProcessEndpoint {
    fn id(&self) -> u64 {
        self.id
    }

    fn kind(&self) -> EndpointKind {
        EndpointKind::Process
    }

    /// 投递一帧给进程端点（emission：写成功即已越过边界，不可逆）。
    fn emit(&self, msg: &ServerMessage) -> bool {
        if !self.alive.load(Ordering::SeqCst) {
            return false;
        }
        let (method, params, query_id) = match msg {
            ServerMessage::ProviderExec {
                query_id,
                sql,
                params,
                handle,
            } => (
                "provider/exec",
                json!({
                    "query_id": query_id,
                    "sql": sql,
                    "handle": handle,
                    "params": params,
                }),
                Some(*query_id),
            ),
            ServerMessage::ProviderCancel { query_id } => {
                ("provider/cancel", json!({"query_id": query_id}), None)
            }
            _ => return false,
        };
        let id = self.next_id.fetch_add(1, Ordering::SeqCst) + 1;
        if let Some(qid) = query_id {
            self.pending
                .lock()
                .expect("pending poisoned")
                .insert(id, qid);
        }
        self.write_frame(&json!({
            "jsonrpc": "2.0",
            "id": id,
            "method": method,
            "params": params,
        }))
    }

    /// 端点被降级时的回执（§16.9.4）。进程端点没有会话可通知，回执由宿主合成。
    fn notify_retired(&self, _key: &str, _attached: Vec<String>) {}
}

/// decode_exec_result 把 provider 的响应帧映射成结果或错误（§16.13.3）。
fn decode_exec_result(frame: &JsonValue) -> Result<QueryResult, PolyDBError> {
    if let Some(err) = frame.get("error") {
        let msg = err
            .get("message")
            .and_then(|m| m.as_str())
            .unwrap_or("provider error");
        return Err(PolyDBError::new(
            polydb_core::protocol::error::codes::QUERY_FAILED,
            msg.to_string(),
        ));
    }
    let result = frame.get("result").cloned().unwrap_or(JsonValue::Null);
    if let Some(e) = result.get("error") {
        let code = e
            .get("code")
            .and_then(|c| c.as_str())
            .unwrap_or(polydb_core::protocol::error::codes::QUERY_FAILED);
        let msg = e
            .get("message")
            .and_then(|m| m.as_str())
            .unwrap_or("provider error");
        return Err(PolyDBError::new(code, msg.to_string()));
    }
    if let Some(r) = result.get("result") {
        return serde_json::from_value::<QueryResult>(r.clone()).map_err(|e| {
            PolyDBError::new(
                polydb_core::protocol::error::codes::INVALID_PARAM,
                format!("provider result decode failed: {e}"),
            )
        });
    }
    Err(PolyDBError::new(
        polydb_core::protocol::error::codes::INVALID_PARAM,
        "provider result has neither result nor error",
    ))
}

/// parse_capability 把能力名映射到能力位（未知能力 → None）。
fn parse_capability(name: &str) -> Option<DriverCapability> {
    match name.trim() {
        "query" => Some(DriverCapability::Query),
        "execute" => Some(DriverCapability::Execute),
        "metadata" => Some(DriverCapability::Metadata),
        "transaction" => Some(DriverCapability::Transaction),
        "kv" => Some(DriverCapability::Kv),
        _ => None,
    }
}

/// provider_proc_specs 解析 `POLYDB_PROVIDER_PROCS`：key=命令，逗号分隔
/// （§16.13.1）。命令由**服务端管理员**配置，客户端不能指定。
fn provider_proc_specs() -> Vec<(String, String)> {
    let raw = std::env::var("POLYDB_PROVIDER_PROCS").unwrap_or_default();
    let mut out = Vec::new();
    for pair in raw.split(',') {
        let pair = pair.trim();
        if pair.is_empty() {
            continue;
        }
        let Some(eq) = pair.find('=') else { continue };
        let key = pair[..eq].trim();
        let cmd = pair[eq + 1..].trim();
        if !key.is_empty() && !cmd.is_empty() {
            out.push((key.to_string(), cmd.to_string()));
        }
    }
    out
}

/// 按配置拉起全部 provider 进程（§16.13.2）。
///
/// 进程端点的 attached 登记在进程发出 provider/hello 声明后建立（校验通过才
/// 登记）；本函数只负责拉起进程。返回启动失败的 key 列表。
pub fn start_process_endpoints(hub: Arc<ProviderHub>) -> Vec<String> {
    let mut failed = Vec::new();
    let specs = provider_proc_specs();
    for (key, cmd_line) in specs {
        match ProcessEndpoint::spawn(hub.next_endpoint_id(), Arc::clone(&hub), &cmd_line, &key) {
            Ok(ep) => {
                let hub2 = Arc::clone(&hub);
                let ep2 = Arc::clone(&ep);
                ep.set_on_dead(Box::new(move || {
                    hub2.drop_session(ep2.id());
                }));
            }
            Err(e) => {
                tracing::warn!(key = %key, error = %e, "provider process failed to start");
                failed.push(key);
            }
        }
    }
    failed.sort();
    failed
}

/// split_command 按空白切分命令行，引号内的空白不算分隔符。
fn split_command(s: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut cur = String::new();
    let mut in_quote = false;
    let mut started = false;
    for c in s.chars() {
        match c {
            '"' => {
                in_quote = !in_quote;
                started = true;
            }
            c if (c == ' ' || c == '\t') && !in_quote => {
                if started {
                    out.push(std::mem::take(&mut cur));
                    started = false;
                }
            }
            c => {
                cur.push(c);
                started = true;
            }
        }
    }
    if started {
        out.push(cur);
    }
    out
}
