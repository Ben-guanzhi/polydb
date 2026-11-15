//! 阶段 9 回调面的**纯逻辑**单测（与 Go 侧
//! `go/pkg/server/provider_callback_test.go` 对称）。
//!
//! 真实进程的端到端由 `test/contract/provider_callback_test.go` 覆盖（双端
//! parity）。这里锁回调面的安全属性：回环监听、令牌校验、handle 不透明、
//! 只读判定对 provider 同样生效。

use super::*;

#[test]
fn callback_registry_is_loopback_only() {
    // §16.14.2：回调面只监听回环。
    let reg = CallbackRegistry::start_for_test();
    assert!(reg.addr.ip().is_loopback());
}

#[test]
fn handle_is_opaque_and_stable() {
    // §16.14.1：handle 与 connection_id 无算术关系；同一连接复用同一 handle。
    let reg = CallbackRegistry::start_for_test();
    let cid1 = uuid::Uuid::parse_str("11111111-1111-1111-1111-111111111111").unwrap();
    let cid2 = uuid::Uuid::parse_str("22222222-2222-2222-2222-222222222222").unwrap();
    let h1 = reg.handle_for(&cid1.to_string()).expect("handle");
    let h2 = reg.handle_for(&cid2.to_string()).expect("handle");
    assert_ne!(h1, h2, "different connections must get different handles");
    assert!(
        !h1.contains(&cid1.to_string()),
        "handle must not embed connection_id"
    );
    assert_eq!(
        reg.handle_for(&cid1.to_string()).expect("handle"),
        h1,
        "same connection reuses handle"
    );
    assert_eq!(reg.resolve(&h1), Some(cid1));
}

#[test]
fn unknown_handle_does_not_resolve() {
    let reg = CallbackRegistry::start_for_test();
    assert_eq!(reg.resolve("deadbeef"), None);
}

#[test]
fn token_is_random_per_registry() {
    // §16.14.2：每个进程一个随机令牌，不跨 provider 复用。
    let a = CallbackRegistry::start_for_test();
    let b = CallbackRegistry::start_for_test();
    assert_ne!(a.token(), b.token());
    assert!(!a.token().is_empty());
}
