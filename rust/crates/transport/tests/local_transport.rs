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

// §5.2 旁路大字段：超阈值单元格 → $polydb_blob ref，get_blob 双向（取回/404）委托验证。
#[test]
fn get_blob_roundtrip_and_404() {
    use polydb_core::Value;
    use polydb_protocol::{codes, BLOB_REF_PREFIX};

    let app = setup();
    let t = LocalTransport::new(Arc::clone(&app));
    let id: ConnectionId = t.create_connection(&sqlite_req("local")).unwrap().id;

    smol::block_on(async {
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

// 结构比对与同步（E5.3）：Transport 只做委托，规则判定在 app-core / schema-diff。
// 这里用两个 :memory: SQLite 走通 schema_diff → schema_ddl 的接线与类型形状。
#[test]
fn schema_diff_and_ddl_delegate() {
    let app = setup();
    let t = LocalTransport::new(Arc::clone(&app));
    let base: ConnectionId = t.create_connection(&sqlite_req("base")).unwrap().id;
    let want: ConnectionId = t.create_connection(&sqlite_req("want")).unwrap().id;

    smol::block_on(async {
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
