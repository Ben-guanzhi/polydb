// LocalTransport 新能力（KV/事务/浏览）的委托接线验证：
// 以 in-memory SQLite 走一遍「建连 → 建表 → 浏览 → 事务」，确认方法顺序/类型正确。
use std::sync::Arc;

use polydb_app_core::AppCore;
use polydb_core::{
    BeginTransactionRequest, ConnectionId, CreateConnectionRequest, DatabaseKind, IsolationLevel,
    TableRowsRequest, TransactionStatus,
};
use polydb_storage::{FileKeyring, Storage};
use polydb_transport::{LocalTransport, Transport};

fn setup() -> Arc<AppCore> {
    let dir = std::env::temp_dir().join(format!("polydb-transport-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&dir).unwrap();
    let storage = Storage::open(dir.join("test.db").to_str().unwrap()).unwrap();
    let kr = FileKeyring::open(&dir.to_string_lossy(), "").unwrap();
    Arc::new(AppCore::with_keyring(storage, Arc::new(kr)))
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

    smol::block_on(async {
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

#[test]
fn rollback_discards_write() {
    let app = setup();
    let t = LocalTransport::new(Arc::clone(&app));
    let info = t.create_connection(&sqlite_req("local")).unwrap();
    let id: ConnectionId = info.id;

    smol::block_on(async {
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
