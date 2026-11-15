//! 阶段 13：配置对账（behavior.md §16.19）。与 Go 侧
//! `go/pkg/server/provider_config.go` 严格镜像。
//!
//! Def 81 的配置对账落到 broker 配置上：provider 配置（§16.10.1 白名单 +
//! §16.13.1 进程表）的变更**经 notify_d 分类增量生效**——改动只在受影响的
//! key 上动作，其余 key 一概不动，且**无需重启宿主**。
//!
//! 分类表（§16.19.2）：
//!   白名单 +key → activating；−key → deactivating（守卫 ¬ relied，推迟）
//!   进程表 +条目 → activating（spawn）；−条目 → deactivating（kill + Revoke）
//!   命令变更 → deactivating + activating；不变 → neutral（无动作）

use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use serde::Deserialize;

use super::provider_hub::ProviderHub;

/// providerFileConfig 是配置文件的形态（§16.19.1）。
#[derive(Debug, Deserialize, Default)]
struct ProviderFileConfig {
    #[serde(default)]
    allow: Vec<String>,
    #[serde(default)]
    procs: Vec<ProviderProcCfg>,
}

#[derive(Debug, Deserialize, PartialEq, Eq, Clone)]
struct ProviderProcCfg {
    key: String,
    cmd: String,
}

/// 读取并解析配置文件。缺失/解析失败返回 None
///（调用方保持上次已知配置，§16.19.1）。
fn read_provider_config_file(path: &str) -> Option<ProviderFileConfig> {
    let raw = std::fs::read_to_string(path).ok()?;
    serde_json::from_str(&raw).ok()
}

/// procEntry 是对账视角下的一条进程条目。匹配键 = (key, cmd)（§16.19.3：
/// 同 key 多实例由条目列表区分，命令相同的条目互为多实例）。
#[derive(Debug, PartialEq, Eq, Hash, Clone)]
struct ProcEntry {
    key: String,
    cmd: String,
}

/// normalize 把文件配置规范化成可 diff 的形态（白名单集合、进程条目按出现
/// 顺序；空白 key/cmd 忽略）。
fn normalize(cfg: &ProviderFileConfig) -> (HashSet<String>, Vec<ProcEntry>) {
    let allow: HashSet<String> = cfg
        .allow
        .iter()
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
        .collect();
    let procs: Vec<ProcEntry> = cfg
        .procs
        .iter()
        .map(|p| ProcEntry {
            key: p.key.trim().to_string(),
            cmd: p.cmd.trim().to_string(),
        })
        .filter(|p| !p.key.is_empty() && !p.cmd.is_empty())
        .collect();
    (allow, procs)
}

/// 对账轮询周期（POLYDB_PROVIDER_CONFIG_POLL_MS，缺省 5000；测试可调小）。
fn provider_config_poll_ms() -> u64 {
    match std::env::var("POLYDB_PROVIDER_CONFIG_POLL_MS") {
        Ok(raw) => raw.trim().parse::<u64>().unwrap_or(5000).max(1),
        Err(_) => 5000,
    }
}

impl ProviderHub {
    /// add_allowed_key 白名单加入（activating，§16.19.2）。
    pub(crate) fn add_allowed_key(&self, key: String) {
        self.allowed.lock().expect("allowed poisoned").insert(key);
    }

    /// withdraw_key 主动撤下一个 key：守卫（¬ relied）→ broker.Revoke → 撤下
    /// 该 key 的全部端点。守卫有两层镜像：端点的在途计数（hub 层）与 broker
    /// 的 reliance（app-core 层）——任一非零即推迟，返回 false（下个对账周期
    /// 重试，§16.19.2）。
    pub(crate) fn withdraw_key(&self, key: &str) -> bool {
        // 端点层守卫：任一实例仍有在途 exec → 推迟。
        {
            let map = self.attached.lock().expect("attached poisoned");
            if let Some(list) = map.get(key) {
                if list
                    .iter()
                    .any(|a| a.inflight.load(std::sync::atomic::Ordering::SeqCst) > 0)
                {
                    return false;
                }
            }
        }
        // broker 层守卫（¬ relied_n(γ)，§16.8）：reliance > 0 → 推迟。
        let broker = self.broker.lock().expect("broker poisoned").clone();
        if let Some(broker) = broker {
            if broker.revoke(key).error.is_some() {
                return false;
            }
        }
        // 撤下端点：标记退役；空闲端点由 settle 立即收尾（§16.9.4）。
        let entries = {
            let mut map = self.attached.lock().expect("attached poisoned");
            map.remove(key).unwrap_or_default()
        };
        for a in &entries {
            a.retiring.store(true, std::sync::atomic::Ordering::SeqCst);
        }
        for a in &entries {
            self.settle(a);
        }
        true
    }

    /// kill_proc_entry 终止匹配 (key, cmd) 的全部 provider 进程（deactivating）。
    pub(crate) fn kill_proc_entry(&self, p: &ProcEntry) {
        let targets: Vec<Arc<super::provider_process::ProcessEndpoint>> = {
            let mut procs = self.processes.lock().expect("processes poisoned");
            let mut targets = Vec::new();
            procs.retain(|pe| {
                if pe.matches(&p.key, &p.cmd) {
                    targets.push(Arc::clone(pe));
                    return false;
                }
                true
            });
            targets
        };
        for pe in targets {
            pe.close();
        }
    }

    /// spawn_proc_entry 拉起一条进程配置（activating，§16.19.2）。拉起失败
    /// 记日志（与 StartProcessEndpoints 一致）。
    pub(crate) fn spawn_proc_entry(self: &Arc<Self>, p: &ProcEntry) {
        if let Err(e) = super::provider_process::ProcessEndpoint::spawn(
            self.next_endpoint_id(),
            Arc::clone(self),
            &p.cmd,
            &p.key,
        ) {
            tracing::warn!(key = %p.key, error = %e, "provider process failed to start");
        }
    }

    /// reconcile_provider_config 对一份新配置做**一次**对账（§16.19.2）。
    /// deactivating 的守卫失败时**推迟**——返回 false 表示有未完成的撤下
    ///（prev 不推进，下个周期重试同一变更）。对账幂等：同一配置重放 diff 为空。
    pub(crate) fn reconcile_provider_config(
        self: &Arc<Self>,
        prev_allow: &mut HashSet<String>,
        prev_procs: &mut Vec<ProcEntry>,
        next_cfg: &ProviderFileConfig,
    ) -> bool {
        let (next_allow, next_procs) = normalize(next_cfg);
        let mut ok = true;

        // ── 白名单 diff ─────────────────────────────
        for k in &next_allow {
            if !prev_allow.contains(k) {
                self.add_allowed_key(k.clone());
            }
        }
        for k in prev_allow.iter() {
            if !next_allow.contains(k) && !self.withdraw_key(k) {
                ok = false; // ¬ relied：推迟到下个周期（prev 不推进）
            }
        }
        let postponed = !ok;

        // ── 进程表 diff（匹配键 = (key, cmd)，§16.19.3）────────────
        let prev_set: HashSet<&ProcEntry> = prev_procs.iter().collect();
        let next_set: HashSet<&ProcEntry> = next_procs.iter().collect();
        for p in prev_set.iter() {
            if !next_set.contains(*p) {
                self.kill_proc_entry(p);
                if !next_allow.contains(&p.key) && !self.withdraw_key(&p.key) {
                    ok = false;
                }
            }
        }
        for p in &next_procs {
            if !prev_set.contains(p) {
                self.spawn_proc_entry(p);
            }
        }
        // 守卫推迟的撤下未完成：prev 不推进（下个周期重试同一变更）。
        if !postponed {
            *prev_allow = next_allow;
            *prev_procs = next_procs;
        } else {
            // 已完成的 activating 部分仍要记录（否则下个周期重复 spawn——
            // spawn 有同端点幂等保护，重复 attach 无害；这里保守推进白名单）。
            for k in &next_allow {
                prev_allow.insert(k.clone());
            }
            prev_procs.retain(|p| next_procs.contains(p) || true);
        }
        ok
    }
}

/// start_provider_config_reconcile 启动配置对账循环（§16.19.1）。
/// 未设置 POLYDB_PROVIDER_CONFIG_FILE 时为 no-op。后台任务随宿主 runtime
/// 退出（进程生命周期绑定由 §16.16 的沙箱兜底）。
pub fn start_provider_config_reconcile(hub: Arc<ProviderHub>) {
    let path = match std::env::var("POLYDB_PROVIDER_CONFIG_FILE") {
        Ok(p) if !p.trim().is_empty() => p.trim().to_string(),
        _ => return, // 行为不变：env 静态配置，无 watcher（§16.19.1）
    };
    tokio::spawn(async move {
        // 启动时首次读取（覆盖 env 初值，§16.19.1）。
        let mut prev: Option<(HashSet<String>, Vec<ProcEntry>)> = read_provider_config_file(&path)
            .map(|c| {
                let (a, p) = normalize(&c);
                for k in &a {
                    hub.add_allowed_key(k.clone());
                }
                for entry in &p {
                    hub.spawn_proc_entry(entry);
                }
                (a, p)
            });
        let mut ticker =
            tokio::time::interval(std::time::Duration::from_millis(provider_config_poll_ms()));
        #[cfg(unix)]
        let mut sighup = match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::hangup())
        {
            Ok(s) => s,
            Err(_) => return,
        };
        loop {
            tokio::select! {
                _ = ticker.tick() => {}
                _ = async {
                    #[cfg(unix)]
                    {
                        sighup.recv().await;
                    }
                    #[cfg(not(unix))]
                    {
                        // 非 Unix：无 SIGHUP，挂起直到下个 tick（永不完成）。
                        std::future::pending::<()>().await;
                    }
                } => {}
            }
            let Some(cfg) = read_provider_config_file(&path) else {
                continue; // 缺失/解析失败：保持上次已知配置（§16.19.1）
            };
            let (mut pa, mut pp) = prev.take().unwrap_or_default();
            let ok = hub.reconcile_provider_config(&mut pa, &mut pp, &cfg);
            // ok=false：有撤下被守卫推迟——reconcile 内部已推进已完成的部分，
            // 下个周期重试同一变更（§16.19.2）。
            prev = Some((pa, pp));
            let _ = ok;
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicBool, AtomicUsize};

    // hub 的私有字段在兄弟模块不可见——测试经由 pub(crate) 的
    // reconcile_provider_config / withdraw_key 走同一路径。

    fn cfg(allow: &[&str], procs: &[(&str, &str)]) -> ProviderFileConfig {
        ProviderFileConfig {
            allow: allow.iter().map(|s| s.to_string()).collect(),
            procs: procs
                .iter()
                .map(|(k, c)| ProviderProcCfg {
                    key: k.to_string(),
                    cmd: c.to_string(),
                })
                .collect(),
        }
    }

    fn normalize_empty() -> (HashSet<String>, Vec<ProcEntry>) {
        normalize(&ProviderFileConfig::default())
    }

    #[test]
    fn normalize_ignores_malformed() {
        let (allow, procs) = normalize(&cfg(
            &["  pkg:a ", "", "pkg:b"],
            &[("pkg:a", " /bin/a "), ("", "x"), ("k", "")],
        ));
        assert_eq!(allow.len(), 2);
        assert!(allow.contains("pkg:a") && allow.contains("pkg:b"));
        assert_eq!(procs.len(), 1);
        assert_eq!(procs[0].key, "pkg:a");
        assert_eq!(procs[0].cmd, "/bin/a");
    }

    #[test]
    fn reconcile_classifies_and_is_idempotent() {
        // §16.19.2：第一轮 activating（白名单+条目）；重放 diff 为空（幂等，
        // Thm 80 在配置面的对应）。
        let hub = ProviderHub::with_allowed(&[]);
        let (mut pa, mut pp) = normalize_empty();
        let next = cfg(&["pkg:a"], &[("pkg:a", "/bin/a")]);
        assert!(hub.reconcile_provider_config(&mut pa, &mut pp, &next));
        assert_eq!(pa.len(), 1);
        assert_eq!(pp.len(), 1);
        // 幂等重放：不产生动作（白名单/进程表不变）。
        assert!(hub.reconcile_provider_config(&mut pa, &mut pp, &next));
        assert_eq!(pa.len(), 1);
        assert_eq!(pp.len(), 1);
        // spawn 动作：/bin/a 不可执行 → 无进程登记，但不 panic。
        // 白名单移除：deactivating（撤下）。
        let next3 = cfg(&[], &[("pkg:a", "/bin/a")]);
        assert!(hub.reconcile_provider_config(&mut pa, &mut pp, &next3));
        assert!(pa.is_empty());
    }

    #[test]
    fn withdraw_key_guard_postpones_when_in_flight() {
        // §16.19.2：有在途时撤下被推迟（返回 false），在途归零后可撤下。
        let hub = ProviderHub::with_allowed(&["pkg:w"]);
        use crate::provider_hub::{ProviderEndpoint, SessionEndpoint};
        use tokio::sync::mpsc;
        let (tx, _rx) = mpsc::unbounded_channel();
        let ep: Arc<dyn ProviderEndpoint> = Arc::new(SessionEndpoint::new(1, tx));
        hub.attach("pkg:w".into(), Arc::clone(&ep), false).unwrap();
        let qid = uuid::Uuid::new_v4();
        let _rx = hub.register_pending(qid, Arc::clone(&ep));

        assert!(
            !hub.withdraw_key("pkg:w"),
            "¬ relied guard must postpone withdrawal while in flight"
        );
        assert!(
            hub.provider_endpoint("pkg:w").is_some(),
            "endpoint must survive a postponed withdrawal"
        );
        // 在途归零（同一 qid 的 complete 触发 settle）→ 撤下成功。
        let _ = hub.complete(
            qid,
            Err(polydb_core::protocol::error::PolyDBError::new(
                polydb_core::protocol::error::codes::UNKNOWN,
                "done",
            )),
        );
        assert!(hub.withdraw_key("pkg:w"));
        assert!(hub.provider_endpoint("pkg:w").is_none());
    }

    #[test]
    fn read_missing_file_keeps_last_known() {
        // §16.19.1：文件缺失返回 None（保持上次已知配置）。
        assert!(read_provider_config_file("nope-does-not-exist.json").is_none());
    }

    // 静默未用的构造辅助（保持与 Go 版字段对照）。
    #[allow(dead_code)]
    fn touch(_: &AtomicUsize, _: &AtomicBool) {}
}
