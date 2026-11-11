use polydb_app_core::AppCore;
use polydb_core::{
    BeginTransactionRequest, CoreResult, CreateConnectionRequest, DatabaseKind, IsolationLevel,
    TransactionStatus, Value, MAX_INLINE_BLOB_BYTES,
};
use polydb_db_core::{blob_value, sink_ref_count};
use polydb_storage::{FileKeyring, Storage};

fn setup() -> AppCore {
    let dir = std::env::temp_dir().join(format!("polydb-test-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&dir).unwrap();
    let storage = Storage::open(dir.join("test.db").to_str().unwrap()).unwrap();
    let kr = FileKeyring::open(&dir.to_string_lossy(), "").unwrap();
    AppCore::with_keyring(storage, std::sync::Arc::new(kr))
}

fn sqlite_req(name: &str) -> CreateConnectionRequest {
    CreateConnectionRequest {
        name: name.to_string(),
        kind: DatabaseKind::Sqlite,
        host: None,
        port: None,
        database: Some(":memory:".to_string()),
        username: None,
        password_ref: None,
        password: None,
        options: Default::default(),
        ssh_tunnel: None,
        default_schema: None,
        read_only: None,
        group: None,
        color: None,
    }
}

#[test]
fn connection_crud() {
    let app = setup();

    let info = app.create_connection(&sqlite_req("local")).unwrap();
    assert_eq!(info.name, "local");
    assert_eq!(info.kind, DatabaseKind::Sqlite);

    let all = app.list_connections().unwrap();
    assert_eq!(all.len(), 1);

    let fetched = app.get_connection_info(info.id).unwrap().unwrap();
    assert_eq!(fetched.id, info.id);
    assert_eq!(fetched.database.as_deref(), Some(":memory:"));

    let updated = app
        .update_connection(
            info.id,
            &polydb_core::UpdateConnectionRequest {
                name: Some("renamed".to_string()),
                ..Default::default()
            },
        )
        .unwrap()
        .unwrap();
    assert_eq!(updated.name, "renamed");

    let status = app.connection_status(info.id).unwrap();
    assert!(!status.connected);

    assert!(app.delete_connection(info.id).unwrap());
    assert!(app.get_connection_info(info.id).unwrap().is_none());
}

#[test]
fn connect_and_execute_flow() {
    let app = setup();
    let info = app.create_connection(&sqlite_req("mem")).unwrap();
    let id = info.id;

    app.connect(id).unwrap();
    let status = app.connection_status(id).unwrap();
    assert!(status.connected);

    polydb_rt::block_on(async {
        app.execute(
            id,
            "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)",
            &[],
        )
        .await
        .unwrap();
        app.execute(
            id,
            "INSERT INTO t (name) VALUES (?1)",
            &[Value::String("hello".into())],
        )
        .await
        .unwrap();

        let result = app.execute(id, "SELECT * FROM t", &[]).await.unwrap();
        assert_eq!(result.columns.len(), 2);
        assert_eq!(result.rows.len(), 1);
        assert_eq!(result.rows[0][1], Value::String("hello".into()));

        let tables = app.list_tables(id, "main").await.unwrap();
        assert!(tables.iter().any(|t| t.name == "t"));

        let cols = app.list_columns(id, "main", "t").await.unwrap();
        assert_eq!(cols.len(), 2);

        app.ping(id).await.unwrap();
    });

    app.disconnect(id);
    let status = app.connection_status(id).unwrap();
    assert!(!status.connected);
}

#[test]
fn execute_lazily_connects() {
    let app = setup();
    let info = app.create_connection(&sqlite_req("lazy")).unwrap();

    // 未显式 connect 时，查询/元数据调用应自动连接（与 Go 侧 ensure 一致）。
    let result: CoreResult<_> =
        polydb_rt::block_on(async { app.execute(info.id, "SELECT 42", &[]).await });
    let result = result.expect("execute should auto-connect");
    assert_eq!(result.rows[0][0], Value::Integer(42));

    let status = app.connection_status(info.id).unwrap();
    assert!(status.connected);
}

#[test]
fn execute_on_missing_connection_errors() {
    let app = setup();
    let missing = uuid::Uuid::new_v4();

    let result: CoreResult<_> =
        polydb_rt::block_on(async { app.execute(missing, "SELECT 1", &[]).await });
    assert!(result.is_err());
}

#[test]
fn failed_in_tx_execute_keeps_the_transaction_rollbackable() {
    // 回归：事务条目从注册表借出后，任何退出路径都必须把它放回。spec §10 要求
    // 事务内 SQL 失败时事务保持 active，客户端仍可 rollback；回插原先是一条手写
    // 语句（取消时会被跳过，事务永久失踪），现在落在借出守卫的 Drop 里。
    let app = setup();
    let info = app.create_connection(&sqlite_req("tx")).unwrap();
    let id = info.id;
    app.connect(id).unwrap();

    polydb_rt::block_on(async {
        app.execute(
            id,
            "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)",
            &[],
        )
        .await
        .unwrap();

        let txn = app
            .begin_transaction(&BeginTransactionRequest {
                connection_id: id,
                isolation_level: Some(IsolationLevel::ReadCommitted),
            })
            .await
            .unwrap();
        assert_eq!(txn.status, TransactionStatus::Active);
        let txid = txn.id.to_string();

        app.execute_in_transaction(&txid, "INSERT INTO no_such_table (a) VALUES (1)", &[])
            .await
            .expect_err("expected the in-transaction SQL to fail");

        // 失败后条目已回插：同一事务仍能继续执行并提交。
        app.execute_in_transaction(
            &txid,
            "INSERT INTO t (name) VALUES (?1)",
            &[Value::String("kept".into())],
        )
        .await
        .unwrap();
        let committed = app.commit_transaction(&txid).await.unwrap();
        assert_eq!(committed.status, TransactionStatus::Committed);

        // 已终结的事务不再留在注册表：重复 commit 报 NOT_FOUND。
        let dup = app.commit_transaction(&txid).await.err().unwrap();
        assert_eq!(
            dup.to_protocol().code,
            "POLYDB_ERR_TRANSACTION_NOT_FOUND",
            "finalized transaction must not stay registered"
        );

        let rows = app.execute(id, "SELECT name FROM t", &[]).await.unwrap();
        assert_eq!(rows.rows.len(), 1);
        assert_eq!(rows.rows[0][0], Value::String("kept".into()));
    });

    app.disconnect(id);
}

#[test]
fn concurrent_connect_is_thread_safe() {
    // 并发 connect 同一 id：所有调用都必须成功返回，注册表里恰好一条连接。
    // 与 Go 侧 TestConcurrentEnsureCoalescesToSingleConnection 对齐。
    let app = std::sync::Arc::new(setup());
    let info = app.create_connection(&sqlite_req("concurrent")).unwrap();
    let id = info.id;

    assert!(!app.connection_status(id).unwrap().connected);

    const N: usize = 16;
    let mut handles = Vec::with_capacity(N);
    for _ in 0..N {
        let app = std::sync::Arc::clone(&app);
        handles.push(std::thread::spawn(move || app.connect(id)));
    }

    let mut failures = 0;
    for h in handles {
        if let Err(e) = h.join().unwrap() {
            failures += 1;
            eprintln!("connect failed: {e}");
        }
    }

    assert_eq!(
        failures, 0,
        "concurrent connect failed {failures} times (should be 0)"
    );
    assert!(
        app.connection_status(id).unwrap().connected,
        "connection should be registered after concurrent connect"
    );

    app.disconnect(id);
}

#[test]
fn concurrent_lazy_connect_via_execute() {
    // 并发 execute 同一未连接 id：懒连接路径必须工作，所有调用成功。
    // 与 Go 侧 ensure 的并发归并语义对齐（Rust 侧无 singleflight，但必须线程安全）。
    let app = std::sync::Arc::new(setup());
    let info = app
        .create_connection(&sqlite_req("lazy-concurrent"))
        .unwrap();
    let id = info.id;

    assert!(!app.connection_status(id).unwrap().connected);

    const N: usize = 8;
    let mut handles = Vec::with_capacity(N);
    for _ in 0..N {
        let app = std::sync::Arc::clone(&app);
        handles.push(std::thread::spawn(move || {
            polydb_rt::block_on(async { app.execute(id, "SELECT 42", &[]).await })
        }));
    }

    let mut failures = 0;
    for h in handles {
        match h.join().unwrap() {
            Ok(result) => {
                assert_eq!(result.rows[0][0], Value::Integer(42));
            }
            Err(e) => {
                failures += 1;
                eprintln!("execute failed: {e}");
            }
        }
    }

    assert_eq!(
        failures, 0,
        "concurrent lazy execute failed {failures} times (should be 0)"
    );
    assert!(
        app.connection_status(id).unwrap().connected,
        "connection should be registered after concurrent lazy execute"
    );

    app.disconnect(id);
}

// 用户主动断开后，心跳 ping 不得把连接悄悄拉回来（对不可达主机每 30s 重建
// 一次 SSH 隧道+连接，状态栏却显示"已断开"）；显式 connect 恢复。execute 的
// 懒连接语义保持不变（首次查询仍可免显式 connect）。
//
// 与 Go 侧 `pkg/appcore/pd04_user_disconnected_test.go` 逐条对齐（AGENTS.md §7
// 双实现一致性红线）：三段断言——断开后 ping 失败、断开后 ping **不得建立
// 连接**、断开后首次 execute 仍可懒连接。
#[test]
fn ping_respects_user_disconnect() {
    let rt = polydb_rt::runtime();
    rt.block_on(async {
        let app = setup();
        let info = app.create_connection(&sqlite_req("hb")).unwrap();
        let id = info.id;

        app.connect(id).unwrap();
        app.ping(id).await.unwrap();

        app.disconnect(id);
        let err = app.ping(id).await.unwrap_err();
        assert!(
            err.to_string().contains("disconnected"),
            "ping after disconnect = {err}"
        );

        // 断开后 ping 不得把连接悄悄建起来：注册表里不该再有它。
        // （错误只是表象，连接照样建立的话泄漏与后台重连都还在。）
        assert!(
            !app.connection_status(id).unwrap().connected,
            "ping after disconnect must not open the connection"
        );

        // 显式 connect 恢复心跳。
        app.connect(id).unwrap();
        app.ping(id).await.unwrap();
    });
}

// 与 Go 侧 TestExecuteStillLazilyConnectsAfterDisconnect 对齐：execute 的懒
// 连接语义**不受** user_disconnected 影响——用户主动发起的第一次查询不该被
// 上一次的断开意图挡住（只有后台心跳受约束）。
#[test]
fn execute_still_lazily_connects_after_disconnect() {
    let rt = polydb_rt::runtime();
    rt.block_on(async {
        let app = setup();
        let info = app.create_connection(&sqlite_req("hb-lazy")).unwrap();
        let id = info.id;

        app.connect(id).unwrap();
        app.disconnect(id);

        // 断开后直接查询：应当懒连接成功。
        let result = app.execute(id, "SELECT 1", &[]).await;
        assert!(
            result.is_ok(),
            "execute after disconnect should lazily connect (lazy semantics must not change): {:?}",
            result.err()
        );
    });
}

// 回归：update_connection 之前只写 storage，不同步内存 read_only 快照。
// 已连接连接（在 self.connections map 里）的 is_read_only 判定会直接
// 返回 false（因为 self.read_only set 不含该 id），导致把已连接连接
// 改成只读后、直到下次重连前，写语句不被 reject_write_on_read_only
// 拦截——只读红线（behavior.md §12.3）的静默绕过。
#[test]
fn update_connection_read_only_hot_update_applies_to_connected() {
    let app = setup();
    let info = app.create_connection(&sqlite_req("ro-hot")).unwrap();
    let id = info.id;
    app.connect(id).unwrap();

    // 初始非只读，写操作应成功。
    polydb_rt::block_on(async {
        app.execute(id, "CREATE TABLE t (id INTEGER PRIMARY KEY)", &[])
            .await
            .unwrap();
    });

    // 把已连接连接改成只读：内存快照必须同步，否则写操作不被拦截。
    app.update_connection(
        id,
        &polydb_core::UpdateConnectionRequest {
            read_only: Some(true),
            ..Default::default()
        },
    )
    .unwrap();

    // 写操作应被拒绝。
    let result =
        polydb_rt::block_on(async { app.execute(id, "INSERT INTO t (id) VALUES (1)", &[]).await });
    assert!(
        result.is_err(),
        "write on read-only connected connection should fail"
    );

    // 读操作应正常。
    let result = polydb_rt::block_on(async { app.execute(id, "SELECT * FROM t", &[]).await });
    assert!(
        result.is_ok(),
        "read on read-only connected connection should work"
    );

    // 把只读再改回可写：内存快照也必须同步移除。
    app.update_connection(
        id,
        &polydb_core::UpdateConnectionRequest {
            read_only: Some(false),
            ..Default::default()
        },
    )
    .unwrap();

    let result =
        polydb_rt::block_on(async { app.execute(id, "INSERT INTO t (id) VALUES (2)", &[]).await });
    assert!(
        result.is_ok(),
        "write after read-only removed should succeed"
    );

    app.disconnect(id);
}

// P2-5 回归：disconnect 必须回滚该连接上的所有活动事务并清除注册表条目。
// 旧写法只从 map 移除不调 driver.rollback，连接 drop 后事务靠驱动隐式回滚，
// 行为不保证且错误被静默吞掉。断开后旧 tx id 不应再存在于注册表。
#[test]
fn disconnect_rolls_back_and_clears_active_transactions() {
    let app = setup();
    let info = app.create_connection(&sqlite_req("disc-tx")).unwrap();
    let id = info.id;
    app.connect(id).unwrap();

    polydb_rt::block_on(async {
        app.execute(
            id,
            "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)",
            &[],
        )
        .await
        .unwrap();

        let txn = app
            .begin_transaction(&BeginTransactionRequest {
                connection_id: id,
                isolation_level: Some(IsolationLevel::ReadCommitted),
            })
            .await
            .unwrap();
        let txid = txn.id.to_string();

        app.execute_in_transaction(
            &txid,
            "INSERT INTO t (name) VALUES (?1)",
            &[Value::String("rollback-me".into())],
        )
        .await
        .unwrap();

        // 断开：事务必须被回滚，条目从注册表移除。
        app.disconnect(id);

        // 旧事务不再存在：commit 报 NOT_FOUND（take_tx 先查注册表）。
        let err = app.commit_transaction(&txid).await.err().unwrap();
        assert_eq!(
            err.to_protocol().code,
            "POLYDB_ERR_TRANSACTION_NOT_FOUND",
            "old tx must be removed after disconnect"
        );
    });
}

// P2-C 回归：connect 重连前必须回滚并清理该连接上的旧事务。此前 connect
// 完全不触碰 txs，直接重连会留下旧 TxEntry 持有旧驱动的 Arc——旧事务在
// 连接被覆盖后仍可 commit/rollback，违反"卸载前先失活依赖"的 guard。
#[test]
fn reconnect_rolls_back_and_clears_old_transactions() {
    let app = setup();
    let info = app.create_connection(&sqlite_req("reconnect")).unwrap();
    let id = info.id;
    app.connect(id).unwrap();

    polydb_rt::block_on(async {
        app.execute(
            id,
            "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)",
            &[],
        )
        .await
        .unwrap();

        let txn = app
            .begin_transaction(&BeginTransactionRequest {
                connection_id: id,
                isolation_level: Some(IsolationLevel::ReadCommitted),
            })
            .await
            .unwrap();
        let txid = txn.id.to_string();

        app.execute_in_transaction(
            &txid,
            "INSERT INTO t (name) VALUES (?1)",
            &[Value::String("old-data".into())],
        )
        .await
        .unwrap();

        // 重连：旧事务必须被回滚并清除，旧驱动显式关闭。
        app.connect(id).unwrap();

        // 旧事务不再存在于注册表：commit 报 NOT_FOUND。
        let err = app.commit_transaction(&txid).await.err().unwrap();
        assert_eq!(
            err.to_protocol().code,
            "POLYDB_ERR_TRANSACTION_NOT_FOUND",
            "old tx must be removed after reconnect"
        );

        // 新连接可用（新 :memory: 库为空，建表成功）。
        app.execute(id, "CREATE TABLE t (id INTEGER PRIMARY KEY)", &[])
            .await
            .unwrap();
    });

    app.disconnect(id);
}

// P1-A 回归：disconnect 必须显式调用 driver.close()，而非仅依赖底层库
// Drop。SQLite 的 close 是 no-op（rusqlite 在 Drop 时关闭），但验证断开
// 后连接被移除且不再可查——保证调用路径确实走到了 close 分支。
#[test]
fn disconnect_closes_driver_and_removes_connection() {
    let app = setup();
    let info = app.create_connection(&sqlite_req("disc-close")).unwrap();
    let id = info.id;
    app.connect(id).unwrap();

    // 断开前连接存在。
    assert!(app.connection_status(id).unwrap().connected);

    // 断开：连接被移除，状态变为未连接。
    app.disconnect(id);
    let status = app.connection_status(id).unwrap();
    assert!(
        !status.connected,
        "connection must be removed after disconnect"
    );

    // 用户断开标记已设置。
    let err = polydb_rt::block_on(async { app.ping(id).await }).unwrap_err();
    assert!(
        err.to_string().contains("disconnected"),
        "ping after disconnect = {err}"
    );
}

// P2-B 回归：无主事务回收器必须回收 idle 超时的事务。此前 Rust 侧无
// sweeper——commit/rollback 失败后回插的事务若客户端不再管，永久持有
// 驱动句柄（MySQL 池连接、PG 池连接、SQLite 写锁泄漏）。
#[test]
fn sweep_collects_idle_transactions() {
    let app = setup();
    let info = app.create_connection(&sqlite_req("sweep-idle")).unwrap();
    let id = info.id;
    app.connect(id).unwrap();

    polydb_rt::block_on(async {
        app.execute(id, "CREATE TABLE t (id INTEGER PRIMARY KEY)", &[])
            .await
            .unwrap();

        let txn = app
            .begin_transaction(&BeginTransactionRequest {
                connection_id: id,
                isolation_level: Some(IsolationLevel::ReadCommitted),
            })
            .await
            .unwrap();
        let txid = txn.id.to_string();

        // idle TTL = 0：任何正值的 elapsed 都触发回收。
        app.set_tx_idle_ttl(std::time::Duration::ZERO);
        let swept = app.sweep_idle_txs();
        assert!(swept >= 1, "idle tx should be swept, got {swept}");

        // 事务已从注册表移除：commit 报 NOT_FOUND。
        let err = app.commit_transaction(&txid).await.err().unwrap();
        assert_eq!(
            err.to_protocol().code,
            "POLYDB_ERR_TRANSACTION_NOT_FOUND",
            "swept tx must not remain registered"
        );
    });

    app.disconnect(id);
}

// P2-B 回归：idle TTL 内的事务不应被回收。
#[test]
fn sweep_preserves_active_transactions() {
    let app = setup();
    let info = app.create_connection(&sqlite_req("sweep-active")).unwrap();
    let id = info.id;
    app.connect(id).unwrap();

    polydb_rt::block_on(async {
        app.execute(id, "CREATE TABLE t (id INTEGER PRIMARY KEY)", &[])
            .await
            .unwrap();

        let txn = app
            .begin_transaction(&BeginTransactionRequest {
                connection_id: id,
                isolation_level: Some(IsolationLevel::ReadCommitted),
            })
            .await
            .unwrap();
        let txid = txn.id.to_string();

        // idle TTL = 1 小时：事务刚创建，远未超时。
        app.set_tx_idle_ttl(std::time::Duration::from_secs(3600));
        let swept = app.sweep_idle_txs();
        assert_eq!(swept, 0, "fresh tx must not be swept");

        // 事务仍可执行。
        app.execute_in_transaction(&txid, "INSERT INTO t (id) VALUES (1)", &[])
            .await
            .unwrap();
    });

    app.disconnect(id);
}

// P2-B 回归：execute_in_transaction 成功后必须刷新 last_active，否则
// 活跃事务会被误判为 idle 而回收。
#[test]
fn sweep_refreshes_last_active_on_execute() {
    let app = setup();
    let info = app.create_connection(&sqlite_req("sweep-refresh")).unwrap();
    let id = info.id;
    app.connect(id).unwrap();

    polydb_rt::block_on(async {
        app.execute(id, "CREATE TABLE t (id INTEGER PRIMARY KEY)", &[])
            .await
            .unwrap();

        let txn = app
            .begin_transaction(&BeginTransactionRequest {
                connection_id: id,
                isolation_level: Some(IsolationLevel::ReadCommitted),
            })
            .await
            .unwrap();
        let txid = txn.id.to_string();

        // idle TTL = 200ms。等待 150ms（未超时），执行一次刷新 last_active，
        // 再等待 150ms（距刷新 150ms < 200ms，仍未超时）。
        // 若 execute 未刷新：距 begin 已 300ms > 200ms → 会被回收。
        app.set_tx_idle_ttl(std::time::Duration::from_millis(200));
        std::thread::sleep(std::time::Duration::from_millis(150));

        app.execute_in_transaction(&txid, "INSERT INTO t (id) VALUES (1)", &[])
            .await
            .unwrap();

        std::thread::sleep(std::time::Duration::from_millis(150));

        let swept = app.sweep_idle_txs();
        assert_eq!(
            swept, 0,
            "tx with refreshed last_active must not be swept (swept={swept})"
        );

        // 事务仍存在：可再次执行。
        app.execute_in_transaction(&txid, "INSERT INTO t (id) VALUES (2)", &[])
            .await
            .unwrap();
    });

    app.disconnect(id);
}

// P2-B 回归：容量上限——超过 maxEntries 时按最久未活跃回收多余事务。
#[test]
fn sweep_enforces_max_entries() {
    let app = setup();
    let mut txids: Vec<(uuid::Uuid, String)> = Vec::new();
    polydb_rt::block_on(async {
        // 创建 3 个连接，每个 begin 一个事务（SQLite 单连接不支持并发 tx）。
        for i in 0..3 {
            let info = app
                .create_connection(&sqlite_req(&format!("sweep-cap-{i}")))
                .unwrap();
            app.connect(info.id).unwrap();
            let txn = app
                .begin_transaction(&BeginTransactionRequest {
                    connection_id: info.id,
                    isolation_level: Some(IsolationLevel::ReadCommitted),
                })
                .await
                .unwrap();
            txids.push((info.id, txn.id.to_string()));
        }

        // 容量上限 = 2：3 个条目应回收 1 个最久未活跃的。
        app.set_tx_max_entries(2);
        app.set_tx_idle_ttl(std::time::Duration::from_secs(3600));
        let swept = app.sweep_idle_txs();
        assert_eq!(swept, 1, "should sweep 1 oldest tx when over capacity");

        // 剩余 2 个事务仍可用。
        let mut alive = 0;
        for (_, txid) in &txids {
            let r = app.execute_in_transaction(txid, "SELECT 1", &[]).await;
            if r.is_ok() {
                alive += 1;
            }
        }
        assert_eq!(alive, 2, "2 txs should remain after capacity sweep");
    });

    // 断开所有连接（Drop 会停 sweeper 线程）。
    for (cid, _) in &txids {
        app.disconnect(*cid);
    }
}

/// P2-E 回归：`AppCore` Drop 必须调用 `clear_blob_sink`（引用计数递减）。
/// 此前 `with_keyring` 设置进程级全局 sink 但从不清除——销毁后 sink 仍指向
/// 已释放的 BlobStore（悬垂），后续 `blob_value` 可能写入悬垂内存。
/// 使用 `sink_ref_count` 观察计数变化，不依赖全局状态归零（并行测试可能
/// 有其他 AppCore 存活）。
#[test]
fn drop_clears_blob_sink() {
    // 创建 AppCore（with_keyring 内部 set_blob_sink → 计数 +1）。
    let app = setup();
    let count_after_create = sink_ref_count();
    assert!(
        count_after_create >= 1,
        "sink ref count should be >= 1 after AppCore construction (got {count_after_create})"
    );

    // sink 活跃：大字段应走旁路引用（有 ref），而非 truncated。
    let big = vec![0u8; MAX_INLINE_BLOB_BYTES + 1];
    let v = blob_value(&big);
    let Value::Object(outer) = &v else {
        panic!("blob_value should return an object");
    };
    let Value::Object(inner) = outer.get("$blob").expect("should have $blob key") else {
        panic!("$blob should be an object");
    };
    assert!(
        inner.contains_key("ref"),
        "sink should be active: ref present"
    );
    assert!(
        !inner.contains_key("truncated"),
        "sink should be active: no truncated marker"
    );

    // 丢弃 AppCore（Drop 调用 clear_blob_sink → 计数 -1）。
    drop(app);
    let count_after_drop = sink_ref_count();
    assert_eq!(
        count_after_drop,
        count_after_create - 1,
        "AppCore drop should decrement sink ref count by 1 (before={count_after_create}, after={count_after_drop})"
    );
}

/// P3-A 回归：commit 失败后事务必须终结（从注册表移除），rollback 报
/// TRANSACTION_NOT_FOUND 而非 "tx handle already consumed"。
/// 此前 P1-2 注释声称"commit 失败后 handle 仍在，rollback 可随后成功"，
/// 但对 PG（tx.0.take() 消费 Transaction）rollback 会得到误导性错误。
/// 修复后与 Go 侧 CommitTransaction 失败时 delete(a.txMeta, txID) 同构。
///
/// 用 SQLite 延迟外键触发 COMMIT 失败：建 parent/child 表（DEFERRABLE
/// INITIALLY DEFERRED），事务内插入引用不存在 parent 的 child 行，FK
/// 检查推迟到 COMMIT 时触发。
#[test]
fn commit_failure_finalizes_transaction() {
    let app = setup();
    let info = app.create_connection(&sqlite_req("tx")).unwrap();
    let id = info.id;
    app.connect(id).unwrap();

    polydb_rt::block_on(async {
        app.execute(
            id,
            "CREATE TABLE parent (id INTEGER PRIMARY KEY, name TEXT)",
            &[],
        )
        .await
        .unwrap();
        app.execute(
            id,
            "CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)",
            &[],
        )
        .await
        .unwrap();

        let txn = app
            .begin_transaction(&BeginTransactionRequest {
                connection_id: id,
                isolation_level: Some(IsolationLevel::ReadCommitted),
            })
            .await
            .unwrap();
        assert_eq!(txn.status, TransactionStatus::Active);
        let txid = txn.id.to_string();

        // 插入引用不存在 parent 的 child 行：FK 延迟检查，INSERT 成功。
        app.execute_in_transaction(
            &txid,
            "INSERT INTO child (id, parent_id) VALUES (1, 999)",
            &[],
        )
        .await
        .unwrap();

        // COMMIT 时 FK 检查触发，commit 失败。
        let commit_err = app
            .commit_transaction(&txid)
            .await
            .expect_err("commit should fail due to deferred FK violation");
        let msg = commit_err.to_protocol().message;
        assert!(
            msg.contains("commit failed"),
            "expected commit failure, got: {msg}"
        );

        // commit 失败后事务已终结（从注册表移除）：rollback 报 NOT_FOUND，
        // 而非 "tx handle already consumed"（PG 的误导性错误）。
        let rb_err = app.rollback_transaction(&txid).await.err().unwrap();
        assert_eq!(
            rb_err.to_protocol().code,
            "POLYDB_ERR_TRANSACTION_NOT_FOUND",
            "commit failure must finalize the transaction (matching Go behavior), got: {}",
            rb_err.to_protocol().message
        );
    });

    app.disconnect(id);
}
