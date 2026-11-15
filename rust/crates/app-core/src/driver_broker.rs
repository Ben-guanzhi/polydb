//! 进程外 driver broker 骨架（spec/behavior.md §16）。
//!
//! 这一层只实现**组合语义**，不碰 IO、不碰 WS 收发循环、不碰内置
//! `DatabaseKind` 分派。它把论文的两条局部保证落成可验证的数据结构：
//!
//! - 时间维度（§3.1）：每次登记是 tracked effect，逆被压进 `accumulator`；
//!   撤回按 **LIFO** 应用，一个 provider 只撤回自己的贡献（Thm 68）。
//! - 空间维度（§3.2/§6.3）：`capabilities` 是 coeffect 声明，
//!   `granted_capabilities` 是 interception 后的能力；后者原地改写不触发
//!   reload（notify_d 分类为 `Neutral`）。
//!
//! broker 常驻，消费者只依赖它（§6.2），所以 backing provider 的装卸不扰动
//! 依赖图。与 Go 侧 `pkg/appcore/driver_broker.go` 严格镜像。

use std::collections::HashMap;

use parking_lot::RwLock;
use polydb_protocol::error::codes;
use polydb_protocol::{
    DriverBrokerEvent, DriverCapability, DriverProvider, DriverProviderKey, DriverProviderState,
    PolyDBError,
};

/// 当前 broker 异步契约版本。注册时先探版本、再解引用（plugin-abi.md §2.3）。
pub const BROKER_PROTOCOL_VERSION: u32 = 1;

/// 组合操作的结果：终态视图 + `notify_d` 的分类 + 可选错误。
///
/// `PolyDBError` 未实现 `PartialEq`，故本结构只 derives `Debug`/`Clone`；
/// 测试用 `error.is_some()` 与 `state` 断言，不做整体相等比较。
#[derive(Debug, Clone)]
pub struct BrokerTransition {
    pub state: Vec<DriverProviderState>,
    pub event: DriverBrokerEvent,
    /// `None` 表示成功；失败时不产生转移（Def 19 / 版本不符 / 守卫拒绝）。
    pub error: Option<PolyDBError>,
}

impl BrokerTransition {
    fn ok(state: Vec<DriverProviderState>, event: DriverBrokerEvent) -> Self {
        Self {
            state,
            event,
            error: None,
        }
    }

    fn err(
        state: Vec<DriverProviderState>,
        event: DriverBrokerEvent,
        code: &str,
        msg: impl Into<String>,
    ) -> Self {
        Self {
            state,
            event,
            error: Some(PolyDBError::new(code, msg)),
        }
    }
}

/// 单个 provider 的 fiber 状态。
///
/// `inverse` 是该 fiber 的 accumulator 条目（revertible effect 的显式逆）：
/// 撤回时按 LIFO 弹出并精确移除这一条登记，不影响其它 provider。
#[derive(Debug, Clone)]
struct Fiber {
    provider: DriverProvider,
    granted: Vec<DriverCapability>,
    reliance: u32,
    /// 该 fiber 贡献的逆：`Snapshot` 中它自己的 key。
    inverse: DriverProviderKey,
}

impl Fiber {
    fn to_state(&self) -> DriverProviderState {
        DriverProviderState {
            key: self.provider.key.clone(),
            kind: self.provider.kind.clone(),
            protocol_version: self.provider.protocol_version,
            capabilities: self.provider.capabilities.clone(),
            granted_capabilities: self.granted.clone(),
            reliance: self.reliance,
        }
    }
}

/// 进程内 broker 视图（并发安全）。
///
/// `accumulator: Vec<DriverProviderKey>` 是 twisted composition 的 `φ`：
/// 正向登记按发生顺序压栈，撤回时逆序弹出，故 Thm 16 的 LIFO 与 Thm 68
/// 的 recovery exactness 在任意交错下成立。
#[derive(Debug, Default)]
pub struct DriverBroker {
    inner: RwLock<Inner>,
}

#[derive(Debug, Default)]
struct Inner {
    fibers: HashMap<DriverProviderKey, Fiber>,
    /// LIFO 逆栈。每个条目只属于登记它的那一个 fiber。
    accumulator: Vec<DriverProviderKey>,
}

impl DriverBroker {
    pub fn new() -> Self {
        Self::default()
    }

    /// 注册一个 backing provider（tracked effect）。
    ///
    /// 校验顺序：版本 → 能力集合 → cannot provide twice。任一失败都**不产生
    /// 转移**：不压 accumulator、不改 fibers、不返回 activating 事件之外的
    /// 状态变化。
    pub fn register(&self, provider: DriverProvider) -> BrokerTransition {
        let mut g = self.inner.write();
        let before = snapshot(&g);

        if provider.protocol_version != BROKER_PROTOCOL_VERSION {
            return BrokerTransition::err(
                before,
                DriverBrokerEvent::Neutral,
                codes::INVALID_PARAM,
                format!(
                    "broker protocol version mismatch: got {}, want {}",
                    provider.protocol_version, BROKER_PROTOCOL_VERSION
                ),
            );
        }
        if !valid_capabilities(&provider.capabilities) {
            return BrokerTransition::err(
                before,
                DriverBrokerEvent::Neutral,
                codes::INVALID_PARAM,
                "capabilities must be a non-empty set without duplicates",
            );
        }
        if let Some(fiber) = g.fibers.get(&provider.key) {
            // §16.18.3：commutative key（Thm 47 witnessed coeffect）的多实例声明
            // 是**幂等中性**——能力集与既有声明一致时无转移（不重复压栈），回
            // neutral 事件；声明不一致（能力/方言/版本漂移）仍拒绝。
            let same = provider.commutative.unwrap_or(false)
                && fiber.provider.kind == provider.kind
                && fiber.provider.protocol_version == provider.protocol_version
                && same_capabilities(&fiber.provider.capabilities, &provider.capabilities);
            if !same {
                return BrokerTransition::err(
                    before,
                    DriverBrokerEvent::Neutral,
                    codes::INVALID_PARAM,
                    format!("provider key already provided: {}", provider.key),
                );
            }
            return BrokerTransition::ok(snapshot(&g), DriverBrokerEvent::Neutral);
        }

        // `set(key, value)` 本身是 `ctx.effect`：登记与其逆一起写入。
        let inverse = provider.key.clone();
        let fiber = Fiber {
            granted: provider.capabilities.clone(), // 初始未衰减
            provider,
            reliance: 0,
            inverse: inverse.clone(),
        };
        g.fibers.insert(inverse.clone(), fiber);
        g.accumulator.push(inverse);

        BrokerTransition::ok(snapshot(&g), DriverBrokerEvent::Activating)
    }

    /// 撤回一个 provider。
    ///
    /// **决定与动作分离**：`¬ relied_n(γ)` 守卫先判定；拒绝时不应用
    /// accumulator（不产生转移）。接受后才 LIFO 弹出该 fiber 的逆并移除它。
    /// 注意只移除目标 fiber，其它 fiber 的登记与逆栈条目保持不变（Thm 68）。
    pub fn revoke(&self, key: &str) -> BrokerTransition {
        let mut g = self.inner.write();
        let before = snapshot(&g);

        let Some(fiber) = g.fibers.get(key) else {
            return BrokerTransition::err(
                before,
                DriverBrokerEvent::Neutral,
                codes::INVALID_PARAM,
                format!("provider key not registered: {key}"),
            );
        };
        if fiber.reliance > 0 {
            return BrokerTransition::err(
                before,
                DriverBrokerEvent::Deactivating,
                codes::DRIVER_NOT_AVAILABLE,
                format!(
                    "¬ relied_n(γ): provider {key} still has {} in-flight operation(s)",
                    fiber.reliance
                ),
            );
        }

        // 动作：应用该 fiber 的 accumulator。LIFO 语义下，若它是栈顶则直接
        // 弹出；否则（目标先于别的 provider 登记）从栈中移除属于它的那一条，
        // 保持其余条目的相对顺序不变——这正是 twisted composition 的
        // `𝑔₂∘𝑔₁` 逆序在「只撤回一个 fiber」情形下的表现。
        let inverse = fiber.inverse.clone();
        if let Some(pos) = g.accumulator.iter().rposition(|k| k == &inverse) {
            g.accumulator.remove(pos);
        }
        g.fibers.remove(key);

        BrokerTransition::ok(snapshot(&g), DriverBrokerEvent::Deactivating)
    }

    /// interception：原地改写生效能力集合，不触发 reload。
    ///
    /// `granted` 必须是 `capabilities` 的子集。合法改动被 `notify_d` 分类为
    /// `Neutral`（声明仍被满足），不产生 activating/deactivating 转移。
    pub fn set_granted(&self, key: &str, granted: Vec<DriverCapability>) -> BrokerTransition {
        let mut g = self.inner.write();
        let before = snapshot(&g);

        let Some(fiber) = g.fibers.get_mut(key) else {
            return BrokerTransition::err(
                before,
                DriverBrokerEvent::Neutral,
                codes::INVALID_PARAM,
                format!("provider key not registered: {key}"),
            );
        };
        if !valid_capabilities(&granted) || !is_subset(&granted, &fiber.provider.capabilities) {
            return BrokerTransition::err(
                before,
                DriverBrokerEvent::Neutral,
                codes::INVALID_PARAM,
                "granted_capabilities must be a non-empty subset of declared capabilities",
            );
        }
        fiber.granted = granted;
        BrokerTransition::ok(snapshot(&g), DriverBrokerEvent::Neutral)
    }

    /// 能力校验 + 在途绑定，**在同一个写临界区里原子完成**（behavior.md §16.8）。
    ///
    /// 分开做「先 snapshot 查能力、再 acquire」会在两步之间给 `revoke` 留下
    /// 窗口，破坏 Thm 71 的 resolution coherence：校验通过后 provider 被撤回，
    /// 查询仍会绑到一个已消失的 fiber。这里一次加锁完成三件事：
    /// 存在性 → `granted_capabilities` 含 `Query` → `reliance += 1`。
    pub fn check_and_acquire(&self, key: &str) -> Result<(), PolyDBError> {
        let mut g = self.inner.write();
        let fiber = g.fibers.get_mut(key).ok_or_else(|| {
            PolyDBError::new(
                codes::DRIVER_NOT_AVAILABLE,
                format!("provider key not registered: {key}"),
            )
        })?;
        if !fiber.granted.contains(&DriverCapability::Query) {
            return Err(PolyDBError::new(
                codes::PERMISSION_DENIED,
                format!("provider {key} is not granted the 'query' capability"),
            ));
        }
        fiber.reliance += 1;
        Ok(())
    }

    /// 记录/归还一次在途绑定（供 `¬ relied_n(γ)` 守卫使用）。
    /// 返回递增后的 reliance。
    pub fn acquire(&self, key: &str) -> Option<u32> {
        let mut g = self.inner.write();
        let fiber = g.fibers.get_mut(key)?;
        fiber.reliance += 1;
        Some(fiber.reliance)
    }

    pub fn release(&self, key: &str) -> Option<u32> {
        let mut g = self.inner.write();
        let fiber = g.fibers.get_mut(key)?;
        fiber.reliance = fiber.reliance.saturating_sub(1);
        Some(fiber.reliance)
    }

    /// 当前视图（按 key 排序，双端一致）。
    pub fn snapshot(&self) -> Vec<DriverProviderState> {
        snapshot(&self.inner.read())
    }

    /// accumulator 长度（测试观察用；生产代码不应依赖）。
    #[doc(hidden)]
    pub fn accumulator_len(&self) -> usize {
        self.inner.read().accumulator.len()
    }
}

/// 从 `Inner` 生成按 key 排序的视图（自由函数，避开闭包捕获 guard 的借用冲突）。
fn snapshot(g: &Inner) -> Vec<DriverProviderState> {
    let mut v: Vec<_> = g.fibers.values().map(Fiber::to_state).collect();
    v.sort_by(|a, b| a.key.cmp(&b.key));
    v
}

/// 能力集合合法：非空、无重复。
fn valid_capabilities(caps: &[DriverCapability]) -> bool {
    if caps.is_empty() {
        return false;
    }
    let mut seen = std::collections::HashSet::new();
    caps.iter().all(|c| seen.insert(*c))
}

/// `granted ⊆ declared`（集合包含，与顺序无关）。
fn is_subset(granted: &[DriverCapability], declared: &[DriverCapability]) -> bool {
    granted.iter().all(|g| declared.contains(g))
}

/// 两个能力集合相同（集合相等，与顺序无关）——§16.18.3 commutative 多实例
/// 声明一致性的判据。
fn same_capabilities(a: &[DriverCapability], b: &[DriverCapability]) -> bool {
    a.len() == b.len() && a.iter().all(|c| b.contains(c))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn provider(key: &str) -> DriverProvider {
        DriverProvider {
            key: key.to_string(),
            kind: "duckdb".to_string(),
            protocol_version: BROKER_PROTOCOL_VERSION,
            capabilities: vec![
                DriverCapability::Query,
                DriverCapability::Execute,
                DriverCapability::Metadata,
            ],
            endpoint: None,
            commutative: None,
        }
    }

    #[test]
    fn register_is_a_tracked_effect_with_lifo_inverse() {
        let b = DriverBroker::new();
        let r = b.register(provider("acme:duckdb"));
        assert!(r.error.is_none());
        assert_eq!(r.event, DriverBrokerEvent::Activating);
        assert_eq!(r.state.len(), 1);
        assert_eq!(b.accumulator_len(), 1, "登记必须压入 accumulator");

        // 撤回应用逆：accumulator 归零，终态为空。
        let r = b.revoke("acme:duckdb");
        assert!(r.error.is_none());
        assert_eq!(r.event, DriverBrokerEvent::Deactivating);
        assert!(r.state.is_empty());
        assert_eq!(b.accumulator_len(), 0);
    }

    #[test]
    fn recovery_is_exact_under_interleaving() {
        // Thm 68：A、B 交错登记，只撤回 B，A 的贡献必须原封不动。
        let b = DriverBroker::new();
        b.register(provider("pkg:a"));
        b.register(provider("pkg:b"));
        assert_eq!(b.accumulator_len(), 2);

        let r = b.revoke("pkg:b");
        assert!(r.error.is_none());
        assert_eq!(r.state.len(), 1);
        assert_eq!(r.state[0].key, "pkg:a");
        assert_eq!(b.accumulator_len(), 1, "只弹出 B 的逆，A 的逆仍在");

        // 再撤回 A，终态与静态装配的空配置一致（Thm 80）。
        let r = b.revoke("pkg:a");
        assert!(r.state.is_empty(), "confluence: 动态历史不留痕迹");
        assert_eq!(b.accumulator_len(), 0);
    }

    #[test]
    fn confluence_independent_of_registration_order() {
        // Thm 80：两种到达同一最终配置的路线，静止态相同。
        let b1 = DriverBroker::new();
        b1.register(provider("pkg:a"));
        b1.register(provider("pkg:b"));
        b1.revoke("pkg:a");

        let b2 = DriverBroker::new();
        b2.register(provider("pkg:b"));
        assert_eq!(b1.snapshot(), b2.snapshot());
    }

    #[test]
    fn cannot_provide_twice_and_revoke_absent_are_no_transitions() {
        let b = DriverBroker::new();
        b.register(provider("pkg:a"));
        let dup = b.register(provider("pkg:a"));
        assert!(dup.error.is_some(), "重复注册必须报错");
        assert_eq!(dup.state.len(), 1, "失败不产生转移");
        assert_eq!(b.accumulator_len(), 1, "失败不压 accumulator");

        let absent = b.revoke("pkg:missing");
        assert!(absent.error.is_some());
        assert_eq!(absent.state.len(), 1);
    }

    #[test]
    fn version_probe_precedes_registration() {
        let b = DriverBroker::new();
        let mut p = provider("pkg:a");
        p.protocol_version = BROKER_PROTOCOL_VERSION + 1;
        let r = b.register(p);
        assert!(r.error.is_some());
        assert!(b.snapshot().is_empty(), "版本不符绝不解引用/登记");
    }

    #[test]
    fn guard_blocks_withdrawal_while_relied() {
        let b = DriverBroker::new();
        b.register(provider("pkg:a"));
        assert_eq!(b.acquire("pkg:a"), Some(1));

        let r = b.revoke("pkg:a");
        assert!(r.error.is_some(), "¬ relied_n(γ) 必须拒绝");
        assert_eq!(r.event, DriverBrokerEvent::Deactivating);
        assert_eq!(r.state.len(), 1, "决定与动作分离：守卫拒绝不卸载");
        assert_eq!(b.accumulator_len(), 1);

        assert_eq!(b.release("pkg:a"), Some(0));
        let r = b.revoke("pkg:a");
        assert!(r.error.is_none());
        assert!(r.state.is_empty());
    }

    #[test]
    fn interception_is_neutral_and_never_reloads() {
        // §6.3：能力衰减只改变「如何调用」，不改变「是否满足」。
        let b = DriverBroker::new();
        b.register(provider("pkg:a"));
        let r = b.set_granted(
            "pkg:a",
            vec![DriverCapability::Query, DriverCapability::Metadata],
        );
        assert!(r.error.is_none());
        assert_eq!(r.event, DriverBrokerEvent::Neutral);
        let s = &r.state[0];
        assert_eq!(s.capabilities.len(), 3, "声明不变");
        assert_eq!(s.granted_capabilities.len(), 2, "生效能力被衰减");
        assert_eq!(
            b.accumulator_len(),
            1,
            "interception 不是 effect，不动 accumulator"
        );
    }

    #[test]
    fn check_and_acquire_is_atomic_and_enforces_capabilities() {
        // §16.8：校验与 acquire 同一临界区；能力被衰减后立即拒绝。
        let b = DriverBroker::new();
        b.register(provider("pkg:a"));

        assert!(b.check_and_acquire("pkg:a").is_ok());
        assert_eq!(b.snapshot()[0].reliance, 1);
        b.release("pkg:a");

        // 衰减掉 execute/query？这里衰减到只剩 metadata → query 被拒。
        let tr = b.set_granted("pkg:a", vec![DriverCapability::Metadata]);
        assert!(tr.error.is_none());
        let err = b.check_and_acquire("pkg:a").unwrap_err();
        assert_eq!(err.code, codes::PERMISSION_DENIED);
        assert_eq!(b.snapshot()[0].reliance, 0, "失败不许占 reliance");

        // 未注册 key。
        let err = b.check_and_acquire("pkg:missing").unwrap_err();
        assert_eq!(err.code, codes::DRIVER_NOT_AVAILABLE);
    }

    #[test]
    fn reliance_guard_blocks_revoke_throughout_in_flight_lifetime() {
        // Thm 70：provider 的 episode 严格包住消费者。在途期间 revoke 被拒，
        // release 后立刻可撤回。
        let b = DriverBroker::new();
        b.register(provider("pkg:a"));
        b.check_and_acquire("pkg:a").unwrap();

        let r = b.revoke("pkg:a");
        assert!(r.error.is_some(), "¬ relied_n(γ) 必须拒绝");
        assert_eq!(r.error.as_ref().unwrap().code, codes::DRIVER_NOT_AVAILABLE);
        assert_eq!(r.state.len(), 1);

        b.release("pkg:a");
        let r = b.revoke("pkg:a");
        assert!(r.error.is_none());
        assert!(r.state.is_empty());
    }

    #[test]
    fn granted_must_be_a_subset_of_declared() {
        let b = DriverBroker::new();
        b.register(provider("pkg:a"));
        let r = b.set_granted("pkg:a", vec![DriverCapability::Kv]);
        assert!(r.error.is_some(), "Kv 未声明，越权必须拒绝");
        assert_eq!(r.state[0].granted_capabilities.len(), 3, "失败不产生转移");
    }

    // ─── 阶段 12：§16.18 ─────────────────────────────────────────────

    fn commutative_provider(key: &str) -> DriverProvider {
        let mut p = provider(key);
        p.commutative = Some(true);
        p
    }

    #[test]
    fn commutative_duplicate_register_is_idempotent_neutral() {
        // §16.18.3：commutative key 的重复声明（能力集一致）幂等中性；
        // 漂移仍拒绝；非 commutative 重复仍拒绝（Def 19 不变）。
        let b = DriverBroker::new();
        let r = b.register(commutative_provider("k1"));
        assert!(r.error.is_none());
        assert_eq!(r.event, DriverBrokerEvent::Activating);

        let r = b.register(commutative_provider("k1"));
        assert!(r.error.is_none(), "一致的多实例声明必须幂等接受");
        assert_eq!(r.event, DriverBrokerEvent::Neutral);
        assert_eq!(b.accumulator_len(), 1, "幂等声明不重复压栈");

        let mut drift = commutative_provider("k1");
        drift.capabilities = vec![DriverCapability::Query];
        assert!(b.register(drift).error.is_some(), "能力漂移必须拒绝");

        assert!(
            b.register(provider("k1")).error.is_some(),
            "非 commutative 重复仍拒绝"
        );
    }

    // ─── Thm 80 收敛性穷举（§16.18.4）───────────────────────────────

    #[derive(Clone, Copy)]
    enum ThmOp {
        Register(usize),
        Revoke(usize),
        Grant(usize),
    }

    fn thm_keys() -> [&'static str; 2] {
        ["k1", "k2"]
    }

    fn apply_thm_op(b: &DriverBroker, op: ThmOp) -> bool {
        let keys = thm_keys();
        match op {
            ThmOp::Register(k) => b.register(provider(keys[k])).error.is_none(),
            ThmOp::Revoke(k) => b.revoke(keys[k]).error.is_none(),
            ThmOp::Grant(k) => b
                .set_granted(keys[k], vec![DriverCapability::Query])
                .error
                .is_none(),
        }
    }

    fn thm_snapshot(b: &DriverBroker) -> String {
        let mut out = String::new();
        for st in b.snapshot() {
            let mut caps: Vec<String> = st
                .granted_capabilities
                .iter()
                .map(|c| format!("{c:?}"))
                .collect();
            caps.sort();
            out.push_str(&format!("{}:{}:{:?};", st.key, st.reliance, caps));
        }
        out
    }

    fn enumerate_thm_seqs(max_len: usize) -> Vec<Vec<ThmOp>> {
        let base = [
            ThmOp::Register(0),
            ThmOp::Register(1),
            ThmOp::Revoke(0),
            ThmOp::Revoke(1),
            ThmOp::Grant(0),
            ThmOp::Grant(1),
        ];
        let mut out = Vec::new();
        fn rec(cur: &mut Vec<ThmOp>, base: &[ThmOp; 6], max_len: usize, out: &mut Vec<Vec<ThmOp>>) {
            if !cur.is_empty() {
                out.push(cur.clone());
            }
            if cur.len() >= max_len {
                return;
            }
            for op in base {
                cur.push(*op);
                rec(cur, base, max_len, out);
                cur.pop();
            }
        }
        rec(&mut Vec::new(), &base, max_len, &mut out);
        out
    }

    #[test]
    fn broker_confluence_exhaustive() {
        // §16.18.4：net-intent 收敛（全部序列）+ 幂等重放（仅全成功序列）。
        let seqs = enumerate_thm_seqs(4);
        assert!(seqs.len() >= 500, "enumeration too small: {}", seqs.len());
        for (si, seq) in seqs.iter().enumerate() {
            let b = DriverBroker::new();
            let mut applied = Vec::with_capacity(seq.len());
            for &op in seq {
                applied.push(apply_thm_op(&b, op));
            }
            let all_ok = applied.iter().all(|&a| a);

            if all_ok {
                let after_first = thm_snapshot(&b);
                for &op in seq {
                    apply_thm_op(&b, op);
                }
                assert_eq!(
                    thm_snapshot(&b),
                    after_first,
                    "seq#{si}: replay changed state (Thm 80 violated)"
                );
            }

            // net-intent：只由成功转移构成（Bounds: excludes failed fibers）。
            let mut net: Vec<Vec<ThmOp>> = vec![Vec::new(), Vec::new()];
            for (i, &op) in seq.iter().enumerate() {
                if !applied[i] {
                    continue;
                }
                match op {
                    ThmOp::Register(k) => net[k] = vec![op],
                    ThmOp::Revoke(k) => net[k] = Vec::new(),
                    ThmOp::Grant(k) => {
                        if !net[k].is_empty() {
                            net[k].push(op);
                        }
                    }
                }
            }
            let want = DriverBroker::new();
            for k in 0..2 {
                for &op in &net[k] {
                    apply_thm_op(&want, op);
                }
            }
            assert_eq!(
                thm_snapshot(&b),
                thm_snapshot(&want),
                "seq#{si}: replayed state != net-intent (Thm 80 violated)"
            );
        }
    }
}
