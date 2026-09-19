use polydb_app_core::AppCore;
use polydb_core::{CoreResult, CreateConnectionRequest, DatabaseKind, Value};
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

    smol::block_on(async {
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
        smol::block_on(async { app.execute(info.id, "SELECT 42", &[]).await });
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
        smol::block_on(async { app.execute(missing, "SELECT 1", &[]).await });
    assert!(result.is_err());
}
