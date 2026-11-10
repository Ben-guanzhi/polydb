// LocalTransport 新能力（KV/事务/浏览）的委托接线验证：
// 以 in-memory SQLite 走一遍「建连 → 建表 → 浏览 → 事务」，确认方法顺序/类型正确。
use std::sync::Arc;

use polydb_app_core::AppCore;
use polydb_core::{
    BeginTransactionRequest, ConnectionId, CreateConnectionRequest, DatabaseKind, IsolationLevel,
    SchemaDdlRequest, SchemaDiffRequest, SchemaDiffTarget, TableRowsRequest, TransactionStatus,
};
use polydb_storage::{FileKeyring, Storage};
use polydb_transport::{LocalTransport, Transport};

/// 全局 blob sink 是进程级单例（db-core `blobsink`），而每个用例的
/// `AppCore::with_keyring` 都会 `set_blob_sink`：并行跑时 A 的查询可能把大字段
/// 写进 B 的 BlobStore，随后 A 的 `get_blob` 查不到（BlobNotFound）。
/// 整组用例串行进出，与 `db-core/blobsink.rs` 单测的 `LOCK` 同一范式。
/// （wave 48 引入引用计数 + `AppCore::drop` 调 `clear_blob_sink`：销毁后不再
/// 悬垂，但并发存活期间仍共享全局——confinement 深层修复需驱动注入点改造。）
static SINK_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

/// `setup()` 的返回值：解引用到 `Arc<AppCore>`，同时持有串行锁直到用例结束。
struct TestApp {
    app: Arc<AppCore>,
    /// 仅用于在 Drop 时释放锁；字段本身不读。
    #[allow(dead_code)]
    _guard: std::sync::MutexGuard<'static, ()>,
}

impl std::ops::Deref for TestApp {
    type Target = Arc<AppCore>;
    fn deref(&self) -> &Arc<AppCore> {
        &self.app
    }
}

fn setup() -> TestApp {
    let _guard = SINK_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let dir = std::env::temp_dir().join(format!("polydb-transport-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&dir).unwrap();
    let storage = Storage::open(dir.join("test.db").to_str().unwrap()).unwrap();
    let kr = FileKeyring::open(&dir.to_string_lossy(), "").unwrap();
    TestApp {
        app: Arc::new(AppCore::with_keyring(storage, Arc::new(kr))),
        _guard,
    }
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
fn browse_and_transaction_delegate() {
    let app = setup();
    let t = LocalTransport::new(Arc::clone(&app));
    let info = t.create_connection(&sqlite_req("local")).unwrap();
    let id: ConnectionId = info.id;

    polydb_rt::block_on(async {
        t.execute(id, "CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)", &[])
            .await
            .unwrap();
        t.execute(id, "INSERT INTO t(name) VALUES('alice')", &[])
            .await
            .unwrap();

        // 表数据浏览（M11）
        let req = TableRowsRequest {
            limit: 100,
            ..Default::default()
        };
        let page = t.browse_rows(id, "main", "t", &req).await.unwrap();
        assert_eq!(
            page.rows.len(),
            1,
            "expected one row, got {}",
            page.rows.len()
        );
        let count = t.browse_rows_count(id, "main", "t", &req).await.unwrap();
        assert_eq!(count, 1, "expected count 1, got {count}");

        // 事务（M25）：begin → 事务内写入 → commit
        let txn = t
            .begin_transaction(&BeginTransactionRequest {
                connection_id: id,
                isolation_level: Some(IsolationLevel::ReadCommitted),
            })
            .await
            .unwrap();
        assert_eq!(txn.status, TransactionStatus::Active);
        t.execute_in_transaction(
            txn.id.to_string().as_str(),
            "INSERT INTO t(name) VALUES('bob')",
            &[],
        )
        .await
        .unwrap();
        let committed = t
            .commit_transaction(txn.id.to_string().as_str())
            .await
            .unwrap();
        assert_eq!(committed.status, TransactionStatus::Committed);
        assert_eq!(
            t.browse_rows_count(id, "main", "t", &TableRowsRequest::default())
                .await
                .unwrap(),
            2,
            "after commit both rows visible"
        );
    });
}

// §5.2 旁路大字段：超阈值单元格 → $polydb_blob ref，get_blob 双向（取回/404）委托验证。
#[test]
fn get_blob_roundtrip_and_404() {
    use polydb_core::Value;
    use polydb_protocol::{codes, BLOB_REF_PREFIX};

    let app = setup();
    let t = LocalTransport::new(Arc::clone(&app));
    let id: ConnectionId = t.create_connection(&sqlite_req("local")).unwrap().id;

    polydb_rt::block_on(async {
        t.execute(id, "CREATE TABLE blobref(b BLOB)", &[])
            .await
            .unwrap();
        t.execute(id, "INSERT INTO blobref VALUES(zeroblob(1048577))", &[])
            .await
            .unwrap();

        // 结果集里应是 >1MB 的旁路引用形状（驱动层 blob sink 感知）
        let res = t.execute(id, "SELECT b FROM blobref", &[]).await.unwrap();
        let cell = &res.rows[0][0];
        let blob_id = match cell {
            Value::Object(map) => match map.get("$blob") {
                Some(Value::Object(inner)) => match inner.get("ref") {
                    Some(Value::String(r)) => r
                        .strip_prefix(BLOB_REF_PREFIX)
                        .expect("ref must carry $polydb_blob prefix")
                        .to_string(),
                    other => panic!("ref field: {other:?}"),
                },
                other => panic!("$blob field: {other:?}"),
            },
            other => panic!("cell value: {other:?}"),
        };

        let bytes = t.get_blob(id, &blob_id).await.unwrap();
        assert_eq!(bytes.len(), 1_048_577, "取回长度与 len 字段一致");
        assert!(
            bytes.iter().all(|&b| b == 0),
            "zeroblob 内容应全零（前 1KB 内已足证）"
        );

        // 未知引用 → POLYDB_ERR_BLOB_NOT_FOUND（前端映射固定文案）
        let miss = t
            .get_blob(id, &uuid::Uuid::new_v4().to_string())
            .await
            .unwrap_err();
        assert_eq!(miss.to_protocol().code, codes::BLOB_NOT_FOUND);

        // 非法 uuid → INVALID_PARAM（AppCore::get_blob 先做格式校验）
        let bad = t.get_blob(id, "not-a-uuid").await.unwrap_err();
        assert_eq!(bad.to_protocol().code, codes::INVALID_PARAM);
    });
}

#[test]
fn rollback_discards_write() {
    let app = setup();
    let t = LocalTransport::new(Arc::clone(&app));
    let info = t.create_connection(&sqlite_req("local")).unwrap();
    let id: ConnectionId = info.id;

    polydb_rt::block_on(async {
        t.execute(id, "CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)", &[])
            .await
            .unwrap();
        let txn = t
            .begin_transaction(&BeginTransactionRequest {
                connection_id: id,
                isolation_level: None,
            })
            .await
            .unwrap();
        t.execute_in_transaction(
            txn.id.to_string().as_str(),
            "INSERT INTO t(name) VALUES('x')",
            &[],
        )
        .await
        .unwrap();
        let rolled = t
            .rollback_transaction(txn.id.to_string().as_str())
            .await
            .unwrap();
        assert_eq!(rolled.status, TransactionStatus::RolledBack);
        assert_eq!(
            t.browse_rows_count(id, "main", "t", &TableRowsRequest::default())
                .await
                .unwrap(),
            0,
            "rolled back write must not persist"
        );
    });
}

// 结构比对与同步（E5.3）：Transport 只做委托，规则判定在 app-core / schema-diff。
// 这里用两个 :memory: SQLite 走通 schema_diff → schema_ddl 的接线与类型形状。
#[test]
fn schema_diff_and_ddl_delegate() {
    let app = setup();
    let t = LocalTransport::new(Arc::clone(&app));
    let base: ConnectionId = t.create_connection(&sqlite_req("base")).unwrap().id;
    let want: ConnectionId = t.create_connection(&sqlite_req("want")).unwrap().id;

    polydb_rt::block_on(async {
        t.execute(
            base,
            "CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)",
            &[],
        )
        .await
        .unwrap();
        t.execute(
            want,
            "CREATE TABLE t(id INTEGER PRIMARY KEY, name VARCHAR(30), email TEXT)",
            &[],
        )
        .await
        .unwrap();

        let diff_req = SchemaDiffRequest {
            source: SchemaDiffTarget {
                connection_id: base,
                schema: None,
            },
            target: SchemaDiffTarget {
                connection_id: want,
                schema: None,
            },
            options: None,
        };
        let plan = t.schema_diff(&diff_req).await.unwrap();
        let ids: Vec<String> = plan.entries.iter().map(|e| e.id.clone()).collect();
        assert_eq!(
            ids,
            vec![
                "t/email/add_column".to_string(),
                "t/name/alter_column".to_string()
            ],
            "diff entries must come back in plan order"
        );

        let script = t
            .schema_ddl(&SchemaDdlRequest {
                source: diff_req.source.clone(),
                target: diff_req.target.clone(),
                options: None,
                selected: ids,
            })
            .await
            .unwrap();
        // 改列类型要重建表 ⇒ 首尾 PRAGMA 包裹，两个能力位都得传上来给前端分派执行。
        assert!(script.ddl_in_tx, "SQLite DDL 可进事务");
        assert!(script.pragmas_outside_tx, "PRAGMA 必须留在事务外");
        let head = script.statements.first().expect("脚本非空");
        let tail = script.statements.last().expect("脚本非空");
        assert_eq!(head.sql, "PRAGMA foreign_keys=OFF;");
        assert_eq!(tail.sql, "PRAGMA foreign_keys=ON;");
        assert_eq!(head.phase, 0);
        assert_eq!(tail.phase, 255);
        assert!(!script.rollback.is_empty(), "回滚脚本随正向一起给出");
    });
}

// 取消通道：慢查询在跑时 cancel_latest 停掉它。sqlite 的语句走 spawn_blocking
// （best-effort——语句本身继续在阻塞线程跑完，但执行立即以 POLYDB_ERR_CANCELLED
// 返回、注册表清空、后续 cancel_latest 如实报空），与 Go 侧 CancelRunning 语义一致。
#[test]
fn cancel_latest_stops_running_statement() {
    let app = setup();
    let t = Arc::new(LocalTransport::new(Arc::clone(&app)));
    let info = t.create_connection(&sqlite_req("cancel")).unwrap();
    let id: ConnectionId = info.id;

    polydb_rt::block_on(async {
        t.execute(id, "CREATE TABLE t(x INTEGER)", &[])
            .await
            .unwrap();

        // 足够久（注册与取消的窗口）、足够短（runtime 收尾等阻塞线程的时长）。
        const MEDIUM_CTE: &str = "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < 5000000) SELECT count(*) FROM n";
        let t2 = Arc::clone(&t);
        let task = tokio::spawn(async move { t2.execute(id, MEDIUM_CTE, &[]).await });

        tokio::time::sleep(std::time::Duration::from_millis(200)).await;
        assert!(
            t.cancel_latest().await,
            "cancel_latest must find the statement in flight"
        );
        assert!(
            !t.cancel_latest().await,
            "the registry must be empty after a cancel"
        );

        let err = task.await.unwrap().unwrap_err();
        assert!(err.to_string().contains("CANCELLED"), "unexpected: {err}");

        // 没有在跑的语句时如实报空。
        assert!(!t.cancel_latest().await);
    });
}

// 同步取消入口（GUI 用）：语义与异步 cancel_latest 一致——在飞语句被停、
// 注册表清空、无语句时报空。GUI 在按下取消按钮的瞬间同步 take 句柄：此前
// spawn 异步取消，用户立刻跑新查询时，新 execute 注册的新句柄会被陈旧取消
// 命中——取消打到错误的语句上（wave-27 修复）。
#[test]
fn cancel_latest_now_pins_the_target() {
    let app = setup();
    let t = Arc::new(LocalTransport::new(Arc::clone(&app)));
    let info = t.create_connection(&sqlite_req("cancel_now")).unwrap();
    let id: ConnectionId = info.id;

    polydb_rt::block_on(async {
        t.execute(id, "CREATE TABLE t2(x INTEGER)", &[])
            .await
            .unwrap();

        const MEDIUM_CTE: &str = "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < 5000000) SELECT count(*) FROM n";
        let t2 = Arc::clone(&t);
        let task = tokio::spawn(async move { t2.execute(id, MEDIUM_CTE, &[]).await });

        tokio::time::sleep(std::time::Duration::from_millis(200)).await;
        assert!(
            t.cancel_latest_now(),
            "cancel_latest_now must find the statement in flight"
        );
        assert!(
            !t.cancel_latest_now(),
            "the registry must be empty after a cancel"
        );

        let err = task.await.unwrap().unwrap_err();
        assert!(err.to_string().contains("CANCELLED"), "unexpected: {err}");

        // 空注册表如实报空（GUI 无语句可取消时的状态文案依赖它）。
        assert!(!t.cancel_latest_now());
    });
}
