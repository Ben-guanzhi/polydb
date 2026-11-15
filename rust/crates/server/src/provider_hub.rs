//! 阶段 4/5/6/7：进程级 provider 桥接 hub（behavior.md §16.9 / §16.10 / §16.12）。
//!
//! 两张全局表都必须跨 WS 连接共享，因此挂在 `AppState` 上、由每个 `Session`
//! 持有 `Arc`：
//!
//! - `attached`：`key → 执行端`（acquisition）。`provider_exec` 从这里取目标
//!   执行端；`provider_detach` / 断连 / rolling update 降级时移除。
//! - `pending`：`query_id → oneshot`。消费者会话的查询任务在这里等结果；
//!   `provider_result` 到达的是 **provider 那条会话**的 `receive_loop`，靠
//!   query_id 把结果跨会话送回等待方。
//!
//! 与 Go 侧 `go/pkg/server/provider_hub.go` 严格镜像。

use std::collections::{HashMap, HashSet};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use polydb_core::protocol::driver_broker::DriverCapability;
use polydb_core::protocol::error::PolyDBError;
use polydb_core::protocol::query::QueryResult;
use polydb_core::protocol::ws::ServerMessage;
use tokio::sync::{mpsc, oneshot};
use uuid::Uuid;

/// 一次 provider 执行的结果。
pub type ProviderOutcome = Result<QueryResult, PolyDBError>;

/// **provider 执行端**（§16.12.1）：能接收一次 exec 并最终产出一个结果的东西。
///
/// 端点对自己的传输无感：broker 只知道「向某个端点投递 exec」与「端点报告
/// 结果」。会话端点（§16.12.1 kind 1）与进程端点（kind 2）是同一抽象的
/// 两个实现——新增端点类型不需要改 broker 的分派逻辑。
pub trait ProviderEndpoint: Send + Sync + std::fmt::Debug {
    /// 端点唯一标识：归属判定用（detach / 断连 / 降级只移除自己的登记，Thm 68）。
    fn id(&self) -> u64;
    /// 端点类型：进程端点才需要不透明 handle 与回调面（§16.14）。
    fn kind(&self) -> EndpointKind;
    /// 投递一帧给端点（emission：返回 true = 已越过边界，不可逆）。
    fn emit(&self, msg: &ServerMessage) -> bool;
    /// 端点被自动降级时的回执（§16.9.4：降级须通知）。
    fn notify_retired(&self, key: &str, attached: Vec<String>);
}

/// 端点类型（§16.12.1）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EndpointKind {
    /// 会话端点（kind 1）：一条 WS 会话承载一个 provider。
    Session,
    /// 进程端点（kind 2）：一个独立进程承载一个 provider。
    Process,
}

/// 会话端点（§16.12.1 kind 1）：一条 WS 会话承载一个 provider。
/// exec 经该会话的 socket 发出，结果经该会话回到宿主。
#[derive(Debug)]
pub struct SessionEndpoint {
    id: u64,
    tx: mpsc::UnboundedSender<ServerMessage>,
}

impl SessionEndpoint {
    pub fn new(id: u64, tx: mpsc::UnboundedSender<ServerMessage>) -> Self {
        Self { id, tx }
    }
}

impl ProviderEndpoint for SessionEndpoint {
    fn id(&self) -> u64 {
        self.id
    }

    fn kind(&self) -> EndpointKind {
        EndpointKind::Session
    }

    fn emit(&self, msg: &ServerMessage) -> bool {
        self.tx.send(msg.clone()).is_ok()
    }

    fn notify_retired(&self, key: &str, attached: Vec<String>) {
        let _ = self.tx.send(ServerMessage::ProviderAck {
            event: polydb_core::protocol::driver_broker::DriverBrokerEvent::Deactivating,
            key: key.to_string(),
            attached,
            callback_url: None,
            callback_token: None,
            error: None,
        });
    }
}

/// 已登记的执行端。`endpoint` 是抽象（§16.12.1），broker 不依赖其具体传输。
///
/// `inflight` 是该执行端**已收到但未完成**的 exec 数（rolling update 的降级
/// 判定用，§16.9.4）。`retiring` 标记它已被新端点替换、只服务已收到的 exec。
/// 用 `Arc` 共享：rolling update 替换 map 条目后，已投递 exec 的在途计数仍要
/// 记在**原执行端**上（`Pending` 持有同一个 Arc）。
#[derive(Debug)]
pub(crate) struct Attached {
    endpoint: Arc<dyn ProviderEndpoint>,
    pub(crate) inflight: AtomicUsize,
    pub(crate) retiring: AtomicBool,
    /// 该执行端登记时的 key（降级通知要用）。
    key: String,
    /// §16.18 witnessed coeffect（Thm 47）：实例可互换，多实例并存。
    commutative: bool,
}

/// 一次在途转发。`endpoint` 是**投递时**的执行端（补偿经它，§16.12.1）；
/// `attached` 是它在 attached 表中的登记（在途计数记在它身上）。
#[derive(Debug)]
struct Pending {
    result_tx: oneshot::Sender<ProviderOutcome>,
    endpoint: Arc<dyn ProviderEndpoint>,
    attached: Arc<Attached>,
}

#[derive(Debug, Default)]
pub struct ProviderHub {
    /// key → 端点列表。非 commutative key 恒为单元素（rolling update 替换，
    /// §16.9.4）；commutative key 可并存多实例，按 per-key 轮询分派（§16.18.2）。
    pub(crate) attached: Mutex<HashMap<String, Vec<Arc<Attached>>>>,
    /// per-key 轮询计数（负载分担的分派指针，§16.18.2）。
    pub(crate) rr: Mutex<HashMap<String, u64>>,
    pub(crate) pending: Mutex<HashMap<Uuid, Pending>>,
    /// attach 准入门禁白名单（behavior.md §16.10.1）。空 = deny-by-default。
    /// Mutex 化：§16.19 的配置对账在运行时更新它。
    pub(crate) allowed: Mutex<HashSet<String>>,
    /// 全部拉起过的 provider 进程（§16.19 的配置对账按 (key, cmd) 匹配撤下；
    /// hello 校验失败的也要能被回收，§16.13.4）。
    pub(crate) processes: Mutex<Vec<Arc<super::provider_process::ProcessEndpoint>>>,
    /// 端点 id 分配器（归属判定用，§16.12.1）。
    next_endpoint_id: AtomicUsize,
    /// broker 是进程级驱动 broker：进程端点的能力声明自动注册到它
    /// （§16.13.2），与会话端点发 broker_register 同一路径。
    pub(crate) broker: Mutex<Option<Arc<polydb_app_core::driver_broker::DriverBroker>>>,
    /// callback 是阶段 9 的受限回调面（§16.14）：进程端点的 provider 经它把
    /// SQL 交回宿主执行，全程看不到 connection_id 与凭据。
    pub(crate) callback: Mutex<Option<Arc<super::provider_callback::CallbackRegistry>>>,
}

impl ProviderHub {
    /// 从 `POLYDB_PROVIDER_ALLOW`（逗号分隔）读取 attach 白名单。
    /// **未设置或为空 = 不允许任何 attach**（deny-by-default）。
    pub fn from_env(
        broker: Arc<polydb_app_core::driver_broker::DriverBroker>,
        callback: Option<Arc<super::provider_callback::CallbackRegistry>>,
    ) -> Arc<Self> {
        let raw = std::env::var("POLYDB_PROVIDER_ALLOW").unwrap_or_default();
        let allowed = raw
            .split(',')
            .map(|s| s.trim())
            .filter(|s| !s.is_empty())
            .map(|s| s.to_string())
            .collect();
        Arc::new(ProviderHub {
            attached: Mutex::new(HashMap::new()),
            rr: Mutex::new(HashMap::new()),
            pending: Mutex::new(HashMap::new()),
            allowed: Mutex::new(allowed),
            processes: Mutex::new(Vec::new()),
            next_endpoint_id: AtomicUsize::new(0),
            broker: Mutex::new(Some(broker)),
            callback: Mutex::new(callback),
        })
    }

    /// 回调端点地址（仅进程端点收到；§16.14.2）。
    pub fn callback_url(&self) -> String {
        self.callback
            .lock()
            .expect("callback poisoned")
            .as_ref()
            .map(|c| c.callback_url())
            .unwrap_or_default()
    }

    /// 回调令牌（仅进程端点收到；§16.14.2）。
    pub fn callback_token(&self) -> String {
        self.callback
            .lock()
            .expect("callback poisoned")
            .as_ref()
            .map(|c| c.token())
            .unwrap_or_default()
    }

    /// 为某连接分配不透明句柄（§16.14.1）：provider 只见 handle，看不到
    /// connection_id。
    pub fn handle_for(&self, connection_id: &str) -> String {
        self.callback
            .lock()
            .expect("callback poisoned")
            .as_ref()
            .and_then(|c| c.handle_for(connection_id))
            .unwrap_or_default()
    }

    /// 把进程端点声明的能力注册到 broker（§16.13.2）。
    ///
    /// 与会话端点发 broker_register 等价：同一 key 的能力声明只会成功一次
    /// （重复注册被 broker 拒绝，cannot provide twice）——除非 key 是
    /// commutative（§16.18.3：声明一致时幂等中性）。
    pub fn register_provider_caps(
        &self,
        key: &str,
        caps: Vec<DriverCapability>,
        commutative: bool,
    ) {
        let broker = self.broker.lock().expect("broker poisoned").clone();
        let Some(broker) = broker else {
            return;
        };
        broker.register(polydb_core::protocol::driver_broker::DriverProvider {
            key: key.to_string(),
            kind: key.to_string(),
            protocol_version: super::provider_process::PROVIDER_PROTOCOL_VERSION,
            capabilities: caps,
            endpoint: None,
            commutative: Some(commutative),
        });
    }

    /// 指定白名单构造（仅测试用；生产走 [`ProviderHub::from_env`]）。
    #[cfg(test)]
    pub fn with_allowed(keys: &[&str]) -> Arc<Self> {
        Arc::new(ProviderHub {
            attached: Mutex::new(HashMap::new()),
            rr: Mutex::new(HashMap::new()),
            pending: Mutex::new(HashMap::new()),
            allowed: Mutex::new(keys.iter().map(|s| s.to_string()).collect()),
            processes: Mutex::new(Vec::new()),
            next_endpoint_id: AtomicUsize::new(0),
            broker: Mutex::new(None),
            callback: Mutex::new(None),
        })
    }

    /// 分配一个新的端点 id（归属判定用，§16.12.1）。
    pub fn next_endpoint_id(&self) -> u64 {
        self.next_endpoint_id.fetch_add(1, Ordering::SeqCst) as u64 + 1
    }

    /// acquisition：登记一个执行端为 key 的执行端。返回当前全部 attached keys。
    ///
    /// §16.18 多实例：commutative key（witnessed，Thm 47）的新端点**并存**——
    /// 旧端点不退役，同 key 按轮询分派；同端点重复 attach 幂等。
    pub fn attach(
        &self,
        key: String,
        endpoint: Arc<dyn ProviderEndpoint>,
        commutative: bool,
    ) -> Result<Vec<String>, PolyDBError> {
        // §16.10.1：白名单外一律拒绝，且不登记。
        if !self
            .allowed
            .lock()
            .expect("allowed poisoned")
            .contains(&key)
        {
            return Err(PolyDBError::new(
                polydb_core::protocol::error::codes::PERMISSION_DENIED,
                format!("provider key is not allowed to attach: {key}"),
            ));
        }
        let mut map = self.attached.lock().expect("attached poisoned");
        let list = map.entry(key.clone()).or_default();
        if commutative {
            // §16.18 并存语义：同端点重复 attach 幂等；新端点追加。
            if list.iter().any(|a| a.endpoint.id() == endpoint.id()) {
                return Ok(sorted_keys(&map));
            }
            list.push(Arc::new(Attached {
                endpoint,
                inflight: AtomicUsize::new(0),
                retiring: AtomicBool::new(false),
                key,
                commutative: true,
            }));
            return Ok(sorted_keys(&map));
        }
        // rolling update（缺省）：替换——旧的标记退役，列表归一。
        for old in list.iter() {
            old.retiring.store(true, Ordering::SeqCst);
        }
        *list = vec![Arc::new(Attached {
            endpoint,
            inflight: AtomicUsize::new(0),
            retiring: AtomicBool::new(false),
            key,
            commutative: false,
        })];
        Ok(sorted_keys(&map))
    }

    /// acquisition 的逆：只有**登记过该 key** 的端点才能移除（id 判定）。
    ///
    /// §16.18：commutative key 只移除**本端点**的实例；列表非空则 key 仍可用
    ///（continuous availability），不向其余实例发补偿。
    pub fn detach(&self, key: &str, endpoint_id: u64) -> Result<Vec<String>, PolyDBError> {
        let mut map = self.attached.lock().expect("attached poisoned");
        let Some(list) = map.get_mut(key) else {
            return Err(PolyDBError::new(
                polydb_core::protocol::error::codes::INVALID_PARAM,
                format!("provider key not attached by this endpoint: {key}"),
            ));
        };
        let before = list.len();
        list.retain(|a| a.endpoint.id() != endpoint_id);
        if list.len() == before {
            return Err(PolyDBError::new(
                polydb_core::protocol::error::codes::INVALID_PARAM,
                format!("provider key not attached by this endpoint: {key}"),
            ));
        }
        if list.is_empty() {
            map.remove(key);
        }
        Ok(sorted_keys(&map))
    }

    /// 端点断开：移除该端点登记的所有 key（recovery exactness，Thm 68）。
    /// §16.18：commutative key 的列表非空则 key 仍登记（其余实例继续服务）。
    pub fn drop_session(&self, endpoint_id: u64) -> Vec<String> {
        let mut map = self.attached.lock().expect("attached poisoned");
        let mut dropped: Vec<String> = Vec::new();
        for (k, list) in map.iter_mut() {
            let before = list.len();
            list.retain(|a| a.endpoint.id() != endpoint_id);
            if list.len() != before {
                dropped.push(k.clone());
            }
        }
        map.retain(|_, list| !list.is_empty());
        dropped.sort();
        dropped
    }

    pub fn attached_keys(&self) -> Vec<String> {
        sorted_keys(&self.attached.lock().expect("attached poisoned"))
    }

    /// 取目标 provider 端点（供 emission / compensation 之外的用途）。
    ///
    /// §16.18.2：commutative key 有多实例时按 per-key 轮询分派（负载分担）。
    /// 查询路径的分派在 `register_pending` 里完成（与计数同锁）。
    pub fn provider_endpoint(&self, key: &str) -> Option<Arc<dyn ProviderEndpoint>> {
        let mut map = self.attached.lock().expect("attached poisoned");
        let list = map.get_mut(key)?;
        if list.is_empty() {
            return None;
        }
        if list.len() == 1 {
            return Some(Arc::clone(&list[0].endpoint));
        }
        let mut rr = self.rr.lock().expect("rr poisoned");
        let i = *rr.entry(key.to_string()).or_insert(0) % list.len() as u64;
        *rr.entry(key.to_string()).or_insert(0) += 1;
        Some(Arc::clone(&list[i as usize].endpoint))
    }

    /// 注册一次在途转发，返回等待结果的 receiver。
    ///
    /// `endpoint` 必须是**当前执行端**（由 `provider_endpoint(key)` 取得）；
    /// hub 内部按端点 id 找回该 key 的 `Arc<Attached>`，在途计数记在它身上——
    /// 这样 rolling update 替换 map 条目后，已投递 exec 的计数仍落在**原端点**，
    /// 降级判定才正确（§16.9.4 / §16.12.1 的「回收」语义）。
    pub fn register_pending(
        &self,
        query_id: Uuid,
        endpoint: Arc<dyn ProviderEndpoint>,
    ) -> oneshot::Receiver<ProviderOutcome> {
        let entry = {
            let map = self.attached.lock().expect("attached poisoned");
            map.values()
                .flatten()
                .find(|a| a.endpoint.id() == endpoint.id())
                .cloned()
        };
        let entry = match entry {
            Some(e) => {
                // 在途计数落在**原条目**上（降级判定用，§16.9.4）。此前缺失
                // 递增导致 inflight 恒 0，settle 的 fetch_sub 把它 wrap 到
                // usize::MAX——单次 rolling update 靠下溢"碰巧"触发降级，
                // 多次在途的判定全坏（配置对账单测暴露）。
                e.inflight.fetch_add(1, Ordering::SeqCst);
                e
            }
            None => {
                // 端点已不在 attached（极端竞态：投递与撤同时发生）：仍要能
                // 收结果，但计数落在临时条目上（归零即消失）。
                Arc::new(Attached {
                    endpoint: endpoint.clone(),
                    inflight: AtomicUsize::new(1),
                    retiring: AtomicBool::new(false),
                    key: String::new(),
                    commutative: false,
                })
            }
        };
        let (result_tx, rx) = oneshot::channel();
        let mut pending = self.pending.lock().expect("pending poisoned");
        pending.insert(
            query_id,
            Pending {
                result_tx,
                endpoint,
                attached: entry,
            },
        );
        rx
    }

    /// `provider_result` 到达：把结果送回等待方。
    /// 返回 `false` = 迟到的结果被 **withhold**（pending 已被 cancel/timeout
    /// 清除，§16.9.2 step 5）—— 已发射的帧不撤回，但未发射的必须扣住。
    pub fn complete(&self, query_id: Uuid, outcome: ProviderOutcome) -> bool {
        let pending = self
            .pending
            .lock()
            .expect("pending poisoned")
            .remove(&query_id);
        match pending {
            Some(p) => {
                self.settle(&p.attached);
                p.result_tx.send(outcome).is_ok()
            }
            None => false,
        }
    }

    /// cancel / timeout 的 compensation：清除 pending（withhold 后续 result）
    /// 并向 provider 端点发 best-effort `provider_cancel`（§16.9.3）。
    /// 明确不宣称 commutation 元理论成立（§16.9.5）。
    pub fn cancel(&self, query_id: Uuid) {
        let pending = self
            .pending
            .lock()
            .expect("pending poisoned")
            .remove(&query_id);
        if let Some(p) = pending {
            self.settle(&p.attached);
            // 补偿同样经端点抽象（传输由端点决定）。
            p.endpoint.emit(&ServerMessage::ProviderCancel { query_id });
        }
    }

    /// 结算一次在途：计数 -1；若该执行端已退役且归零 → 自动降级（§16.9.4）。
    ///
    /// 降级 = 从 attached 移除该端点 + 向它回 `provider_ack(deactivating)`
    /// （spec §16.9.4 最后一条：自动降级须回执）。
    /// settle 结算一次在途：计数 -1；退役且归零 → 自动降级（§16.9.4）。
    /// 配置对账的撤下路径（§16.19）也经它收尾空闲端点。
    pub(crate) fn settle(&self, attached: &Arc<Attached>) {
        let prev = attached.inflight.fetch_sub(1, Ordering::SeqCst);
        if prev > 1 || !attached.retiring.load(Ordering::SeqCst) {
            return;
        }
        // 退役端点的在途归零：把它从列表移除。**注意**：rolling update 的
        // 替换在 attach 时已把旧端点移出列表——它在途归零时仍须收到降级回执
        //（§16.9.4），所以回执无条件（commutative 实例 retiring 恒 false，
        // 走不到这里）。§16.18：列表里还有其它实例时 key 继续可用。
        {
            let mut map = self.attached.lock().expect("attached poisoned");
            for list in map.values_mut() {
                list.retain(|a| !Arc::ptr_eq(a, attached));
            }
            map.retain(|_, list| !list.is_empty());
        }
        // 回执：自动降级必须让旧端点知道（§16.9.4）。
        attached
            .endpoint
            .notify_retired(&attached.key, self.attached_keys());
    }
}

fn sorted_keys(map: &HashMap<String, Vec<Arc<Attached>>>) -> Vec<String> {
    let mut v: Vec<String> = map.keys().cloned().collect();
    v.sort();
    v
}

#[cfg(test)]
mod tests {
    use super::*;
    use polydb_core::protocol::error::codes;

    /// 造一个会话端点（§16.12.1 kind 1）与它的接收端。
    fn session_endpoint(id: u64) -> (Arc<SessionEndpoint>, mpsc::UnboundedReceiver<ServerMessage>) {
        let (tx, rx) = mpsc::unbounded_channel();
        (Arc::new(SessionEndpoint::new(id, tx)), rx)
    }

    #[test]
    fn attach_is_denied_by_default() {
        // §16.10.1：未授权的 key 不可 attach，且不产生执行端。
        let hub = ProviderHub::with_allowed(&[]);
        let (ep, _rx) = session_endpoint(1);
        let err = hub.attach("pkg:a".into(), ep, false).unwrap_err();
        assert_eq!(err.code, codes::PERMISSION_DENIED);
        assert!(hub.attached_keys().is_empty());
        assert!(hub.provider_endpoint("pkg:a").is_none());
    }

    #[test]
    fn attach_allowed_only_for_whitelisted_keys() {
        let hub = ProviderHub::with_allowed(&["pkg:ok"]);
        let (ok_ep, _rx1) = session_endpoint(1);
        assert!(hub.attach("pkg:ok".into(), ok_ep, false).is_ok());
        let (no_ep, _rx2) = session_endpoint(2);
        let err = hub.attach("pkg:no".into(), no_ep, false).unwrap_err();
        assert_eq!(err.code, codes::PERMISSION_DENIED);
        // 只登记了被授权的那个。
        assert_eq!(hub.attached_keys(), vec!["pkg:ok".to_string()]);
    }

    #[test]
    fn detach_only_removes_own_registration() {
        // 同 key 换端点（rolling update 语义）后，旧端点不能 detach 掉新的。
        let hub = ProviderHub::with_allowed(&["pkg:a"]);
        let (first, _rx1) = session_endpoint(1);
        let (second, _rx2) = session_endpoint(2);
        hub.attach("pkg:a".into(), first.clone(), false).unwrap();
        // 新端点接管同一 key。
        hub.attach("pkg:a".into(), second.clone(), false).unwrap();
        let err = hub.detach("pkg:a", first.id()).unwrap_err();
        assert_eq!(err.code, codes::INVALID_PARAM);
        assert!(hub.provider_endpoint("pkg:a").is_some());
    }

    #[test]
    fn drop_session_removes_only_its_own_keys() {
        let hub = ProviderHub::with_allowed(&["pkg:a", "pkg:b"]);
        let (mine, _rx1) = session_endpoint(1);
        let (other, _rx2) = session_endpoint(2);
        hub.attach("pkg:a".into(), mine.clone(), false).unwrap();
        hub.attach("pkg:b".into(), other, false).unwrap();
        let dropped = hub.drop_session(mine.id());
        assert_eq!(dropped, vec!["pkg:a".to_string()]);
        assert_eq!(hub.attached_keys(), vec!["pkg:b".to_string()]);
    }

    #[tokio::test]
    async fn late_result_is_withheld() {
        // §16.9.2 step 5：cancel 后迟到的 provider_result 被扣住。
        let hub = ProviderHub::with_allowed(&["pkg:a"]);
        let (ep, _rx) = session_endpoint(1);
        hub.attach("pkg:a".into(), ep.clone(), false).unwrap();
        let qid = Uuid::new_v4();
        let rx = hub.register_pending(qid, ep);
        assert!(hub.complete(qid, Err(PolyDBError::new(codes::UNKNOWN, "x"))));
        assert!(rx.await.is_ok());
        // 再次 complete（迟到的重复结果）→ 扣住。
        assert!(!hub.complete(qid, Err(PolyDBError::new(codes::UNKNOWN, "y"))));
    }

    #[test]
    fn rolling_update_replaces_endpoint_and_retires_old() {
        // §16.9.4：新端点接管同一 key；旧端点退役，服务完在途后自动降级。
        let hub = ProviderHub::with_allowed(&["pkg:a"]);
        let (first, mut first_rx) = session_endpoint(1);
        let (second, _second_rx) = session_endpoint(2);
        hub.attach("pkg:a".into(), first.clone(), false).unwrap();
        // 旧端点有一个在途 exec。
        let qid = Uuid::new_v4();
        let _rx = hub.register_pending(qid, first.clone());

        // 新端点接管。
        hub.attach("pkg:a".into(), second.clone(), false).unwrap();
        // 新 exec 走新端点。
        assert_eq!(hub.provider_endpoint("pkg:a").unwrap().id(), second.id());

        // 旧端点在途归零 → 自动降级：回 deactivating 回执。
        hub.complete(qid, Err(PolyDBError::new(codes::UNKNOWN, "x")));
        let ack = first_rx
            .try_recv()
            .expect("retired endpoint must be notified");
        match ack {
            ServerMessage::ProviderAck { event, key, .. } => {
                assert_eq!(
                    event,
                    polydb_core::protocol::driver_broker::DriverBrokerEvent::Deactivating
                );
                assert_eq!(key, "pkg:a");
            }
            other => panic!("unexpected message: {:?}", other),
        }
        // 当前端点仍是新端点。
        assert_eq!(hub.provider_endpoint("pkg:a").unwrap().id(), second.id());
    }
}
