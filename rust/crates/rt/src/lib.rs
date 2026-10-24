//! polydb-rt：PolyDB 的单一 tokio 运行时 home（DECISION §5.5：tokio 胜出，smol 退出）。
//!
//! 用途有二，且只有这二：
//! 1. **同步桥**——db-mssql 这类"async 驱动 + 同步门面"的 crate 在这里把
//!    future 驱动到完成（tiberius/tokio-native 的 future 本就需要 tokio 上下文）；
//! 2. **测试**——驱动真引擎的测试需要 block_on 时不再各起炉灶。
//!
//! 运行时懒启动、进程内共享、从不关闭（桌面/服务端进程生命周期一致）。
//! 不要在 runtime 线程上再 block_on（会 panic）；需要阻塞式卸载用
//! `tokio::task::spawn_blocking`。

use std::future::Future;
use std::sync::OnceLock;

static RT: OnceLock<tokio::runtime::Runtime> = OnceLock::new();

/// 进程级共享的 tokio 运行时（单 worker + 全 feature：io/time 皆可用）。
pub fn runtime() -> &'static tokio::runtime::Runtime {
    RT.get_or_init(|| {
        tokio::runtime::Builder::new_multi_thread()
            .worker_threads(1)
            .thread_name("polydb-rt")
            .enable_all()
            .build()
            .expect("polydb-rt: build tokio runtime")
    })
}

/// 在共享运行时上把 `fut` 驱动到完成（同步桥点）。
///
/// 若调用线程已在 tokio 上下文里（如 server 的 worker 线程），不能再嵌套
/// 第二个运行时（tokio 会 panic "Cannot start a runtime from within a
/// runtime"）——改用 `block_in_place` 让出 worker 后在当前 Handle 上等待。
/// 这要求外层是多线程运行时（server 的 `#[tokio::main]` 是）；current_thread
/// 运行时线程上调用会 panic，属于使用错误。
pub fn block_on<F: Future>(fut: F) -> F::Output {
    if let Ok(handle) = tokio::runtime::Handle::try_current() {
        return tokio::task::block_in_place(|| handle.block_on(fut));
    }
    runtime().block_on(fut)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn block_on_drives_async_with_timers() {
        // tokio::time 需要 tokio 上下文——这条用例证明共享运行时真的带 time driver。
        let out = block_on(async {
            tokio::time::sleep(std::time::Duration::from_millis(1)).await;
            41 + 1
        });
        assert_eq!(out, 42);
    }
}
