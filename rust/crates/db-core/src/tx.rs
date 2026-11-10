use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use chrono::{DateTime, Utc};
use parking_lot::Mutex;

use polydb_core::IsolationLevel;

/// TxMode 是各 SQL 驱动的 begin_tx 选项。与 Go 侧 dbcore.TxMode 对齐：
/// 驱动自行把 IsolationLevel 翻译成数据库方言；不支持任意隔离级别的驱动
/// 可忽略该字段。
#[derive(Debug, Clone, Copy, Default)]
pub struct TxMode {
    pub isolation_level: IsolationLevel,
}

impl TxMode {
    pub fn new(isolation_level: IsolationLevel) -> Self {
        Self { isolation_level }
    }
}

/// 一个可被空闲回收的事务条目。回收策略（idle TTL 超时 + 容量上限按最久未
/// 活跃回收 + 锁内收集候选/锁外回滚）在内核只有一份（PD-05，2026-11-09）：
/// 此前 Rust 端在 app-core 自造 sweeper，Go 端委托内核 `dbcore.TxManager`，
/// 两处 LIFO 回滚路径结构漂移。把策略抽到这里后，app-core 只提供条目类型
/// （`TxEntry`）与它的回滚实现，回收器本身不再有第二份。
///
/// 与 Go 侧 `dbcore.TxManager` 的对应关系：`insert`/`take` 对应注册表的
/// 增删，`sweep` 对应 `sweepIdleTx`，`spawn` 对应后台 sweeper goroutine。
pub trait Sweepable {
    /// 最近一次活跃时间，用于 idle 判定（与 Go txEntry.lastActive 同构）。
    fn last_active(&self) -> DateTime<Utc>;

    /// 在注册表锁外回滚。无论成败，条目都必须从注册表移除（事务已无主）。
    /// 实现方负责把驱动句柄取走并调用驱动的 rollback；失败可记录日志但
    /// 不应 panic——回收是尽力而为的清理。
    fn rollback(&mut self);
}

/// 无主事务回收器（PD-05）：持有事务注册表、idle 阈值、容量上限与后台
/// sweeper 的停止标志。条目的具体类型由上层给出（app-core 的 `TxEntry`），
/// 回收策略因此与具体驱动解耦。
///
/// 不变量与 Go 侧 `dbcore.TxManager` 一致：
/// - `insert`/`take` 是注册表的增删原语；
/// - `sweep` 先在锁内收集候选（idle 超时 + 超容量补最久未活跃），再在锁外
///   逐个 `take` + rollback——阻塞式 rollback 不得持锁；
/// - `spawn` 起的线程每轮重读 TTL 计算间隔（配置下轮即生效），分段 sleep
///   以便 `stop` 及时生效。
pub struct TxManager<T: Sweepable> {
    txs: Arc<Mutex<HashMap<String, T>>>,
    idle_ttl: Arc<Mutex<Duration>>,
    max_entries: Arc<Mutex<usize>>,
    stop: Arc<AtomicBool>,
}

impl<T: Sweepable> TxManager<T> {
    /// 默认 30min idle 超时、1024 容量上限（与 Go TxManager 一致）。
    pub fn new() -> Arc<Self> {
        Arc::new(Self {
            txs: Arc::new(Mutex::new(HashMap::new())),
            idle_ttl: Arc::new(Mutex::new(Duration::from_secs(30 * 60))),
            max_entries: Arc::new(Mutex::new(1024)),
            stop: Arc::new(AtomicBool::new(false)),
        })
    }

    /// 登记一条事务。
    pub fn insert(&self, id: String, entry: T) {
        self.txs.lock().insert(id, entry);
    }

    /// 借出并移除一条事务；不存在返回 None。
    pub fn take(&self, id: &str) -> Option<T> {
        self.txs.lock().remove(id)
    }

    /// 收集满足谓词的事务 id（锁内快照）。用于连接卸载前的逐条回滚清理。
    pub fn ids_matching(&self, pred: impl Fn(&T) -> bool) -> Vec<String> {
        self.txs
            .lock()
            .iter()
            .filter(|(_, t)| pred(t))
            .map(|(k, _)| k.clone())
            .collect()
    }

    /// 立即回收无主事务。返回被回收的事务数。
    pub fn sweep(&self) -> usize {
        let now = Utc::now();
        let idle_cutoff = chrono::Duration::from_std(*self.idle_ttl.lock())
            .unwrap_or_else(|_| chrono::Duration::minutes(30));

        // 锁内收集候选：先收 idle 超时的，再按容量上限补最久未活跃的。
        let mut candidates: Vec<String> = Vec::new();
        {
            let map = self.txs.lock();
            for (id, entry) in map.iter() {
                if now - entry.last_active() > idle_cutoff {
                    candidates.push(id.clone());
                }
            }
            let max = *self.max_entries.lock();
            if map.len() > max {
                let mut by_age: Vec<(String, DateTime<Utc>)> = map
                    .iter()
                    .filter(|(id, _)| !candidates.contains(id))
                    .map(|(id, e)| (id.clone(), e.last_active()))
                    .collect();
                by_age.sort_by_key(|(_, t)| *t);
                let excess = map.len() - max;
                for (id, _) in by_age.iter().take(excess) {
                    candidates.push(id.clone());
                }
            }
        }

        // 锁外 rollback：逐个 take + rollback，失败也要移除（事务已无主）。
        let mut swept = 0;
        for id in candidates {
            if let Some(mut entry) = self.take(&id) {
                entry.rollback();
                swept += 1;
            }
        }
        swept
    }

    /// 覆盖 idle 回收阈值（测试/嵌入式调优用）。
    pub fn set_idle_ttl(&self, ttl: Duration) {
        *self.idle_ttl.lock() = ttl;
    }

    /// 覆盖事务容量上限。
    pub fn set_max_entries(&self, max: usize) {
        *self.max_entries.lock() = max;
    }

    /// 请求后台 sweeper 停止。分段 sleep 的线程最迟在一个分段内退出。
    pub fn stop(&self) {
        self.stop.store(true, Ordering::Relaxed);
    }

    /// 启动后台 sweeper 线程。线程持有 `Arc<Self>` 的克隆（不持有 `Arc<AppCore>`），
    /// 看到 `stop` 后退出。间隔 = idleTTL / 6，与 Go TxManager sweeper 同节奏。
    pub fn spawn(self: &Arc<Self>) where T: Send + 'static {
        let manager = Arc::clone(self);
        let spawn_res = std::thread::Builder::new()
            .name("polydb-tx-sweeper".into())
            .spawn(move || loop {
                // 每轮重读 TTL 计算间隔，配置变更下轮即生效。
                let interval = {
                    let ttl = *manager.idle_ttl.lock();
                    let tick = ttl / 6;
                    if tick.is_zero() {
                        Duration::from_secs(60)
                    } else {
                        tick
                    }
                };
                // 分段 sleep 以便 stop 及时生效（100ms 粒度）。
                let mut slept = Duration::ZERO;
                while !manager.stop.load(Ordering::Relaxed) && slept < interval {
                    std::thread::sleep(Duration::from_millis(100));
                    slept += Duration::from_millis(100);
                }
                if manager.stop.load(Ordering::Relaxed) {
                    break;
                }
                manager.sweep();
            });
        // spawn 失败（进程退出中）静默忽略——sweeper 是尽力而为的清理。
        if spawn_res.is_err() {
            tracing::warn!("tx sweeper thread spawn failed");
        }
    }
}

impl<T: Sweepable> Default for TxManager<T> {
    fn default() -> Self {
        // 与 new() 相同的缺省值；new() 返回 Arc 是为 spawn 方便。
        Self {
            txs: Arc::new(Mutex::new(HashMap::new())),
            idle_ttl: Arc::new(Mutex::new(Duration::from_secs(30 * 60))),
            max_entries: Arc::new(Mutex::new(1024)),
            stop: Arc::new(AtomicBool::new(false)),
        }
    }
}
