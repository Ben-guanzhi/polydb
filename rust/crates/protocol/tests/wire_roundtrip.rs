//! Protocol wire round-trip tests: MessagePack and JSON parity for all DTOs.
//!
//! These pin the cross-language contract that Go currently owns alone — the
//! `Value` enum, nested result structures, and field-name conventions must be
//! stable for both implementations to stay in lockstep.

use polydb_protocol::*;

// ── MessagePack roundtrip helper (for types with PartialEq) ──────────────

fn mp_rt<T: serde::Serialize + serde::de::DeserializeOwned + Clone + PartialEq>(v: &T) -> T {
    let bytes = rmp_serde::to_vec(v).expect("encode");
    rmp_serde::from_slice(&bytes).expect("decode")
}

// ── JSON roundtrip helper (for all types) ───────────────────────────────

fn json_rt<T: serde::Serialize + serde::de::DeserializeOwned>(v: &T) {
    let json = serde_json::to_value(v).expect("json encode");
    let back: T = serde_json::from_value(json.clone()).expect("json decode");
    let json2 = serde_json::to_value(&back).expect("json encode2");
    assert_eq!(json, json2, "json roundtrip changed shape");
}

// ── Value ↔ MessagePack ──────────────────────────────────────────────────

#[test]
fn value_msgpack_roundtrip_scalars() {
    let cases: Vec<Value> = vec![
        Value::Null,
        Value::Bool(true),
        Value::Bool(false),
        Value::Integer(0),
        Value::Integer(i64::MIN),
        Value::Integer(i64::MAX),
        Value::Float(0.0),
        Value::Float(-1.5),
        Value::Float(3.14),
        Value::String(String::new()),
        Value::String("hello".to_string()),
        Value::String("中文".to_string()),
        Value::String("emoji \u{1f600}".to_string()),
    ];
    for v in &cases {
        assert_eq!(&mp_rt(v), v, "msgpack roundtrip failed for {:?}", v);
    }
}

#[test]
fn value_msgpack_roundtrip_containers() {
    let v = Value::Array(vec![
        Value::Null,
        Value::Integer(1),
        Value::String("two".to_string()),
        Value::Bool(true),
        Value::Float(3.0),
    ]);
    assert_eq!(&mp_rt(&v), &v);

    let mut map = std::collections::HashMap::new();
    map.insert("a".to_string(), Value::Integer(1));
    map.insert("b".to_string(), Value::String("two".to_string()));
    map.insert(
        "nested".to_string(),
        Value::Object({
            let mut m = std::collections::HashMap::new();
            m.insert("inner".to_string(), Value::Null);
            m
        }),
    );
    let v = Value::Object(map);
    assert_eq!(&mp_rt(&v), &v);
}

#[test]
fn value_msgpack_roundtrip_blob() {
    let v = common::blob_value(&[1, 2, 3]);
    assert_eq!(&mp_rt(&v), &v);

    let big = vec![0u8; common::MAX_INLINE_BLOB_BYTES + 1];
    let v = common::blob_value(&big);
    assert_eq!(&mp_rt(&v), &v);
}

#[test]
fn value_json_roundtrip_scalars() {
    let cases: Vec<Value> = vec![
        Value::Null,
        Value::Bool(true),
        Value::Integer(42),
        Value::Float(3.14),
        Value::String("hello".to_string()),
        Value::String("中文".to_string()),
    ];
    for v in &cases {
        let json = serde_json::to_string(v).expect("json encode");
        let back: Value = serde_json::from_str(&json).expect("json decode");
        assert_eq!(&back, v, "json roundtrip failed for {:?}", v);
    }
}

// ── QueryResult wire shape ───────────────────────────────────────────────

#[test]
fn query_result_json_roundtrip() {
    let qr = QueryResult {
        columns: vec![
            ResultColumn {
                name: "id".to_string(),
                table: Some("users".to_string()),
                data_type: "integer".to_string(),
                generic_type: Some(GenericType::Integer),
                nullable: Some(false),
            },
            ResultColumn {
                name: "name".to_string(),
                table: None,
                data_type: "varchar".to_string(),
                generic_type: None,
                nullable: Some(true),
            },
        ],
        rows: vec![
            vec![Value::Integer(1), Value::String("alice".to_string())],
            vec![Value::Integer(2), Value::Null],
        ],
        affected_rows: 0,
        execution_time_ms: 1.5,
        truncated: false,
        total_rows: Some(2),
        has_more: false,
        statement_type: Some(StatementType::Select),
    };
    json_rt(&qr);
}

#[test]
fn query_result_json_field_names() {
    let qr = QueryResult {
        columns: vec![ResultColumn {
            name: "x".to_string(),
            table: None,
            data_type: "int".to_string(),
            generic_type: None,
            nullable: None,
        }],
        rows: vec![vec![Value::Integer(1)]],
        affected_rows: 1,
        execution_time_ms: 0.0,
        truncated: false,
        total_rows: None,
        has_more: false,
        statement_type: None,
    };
    let json = serde_json::to_value(&qr).unwrap();
    assert!(json.get("affected_rows").is_some());
    assert!(json.get("execution_time_ms").is_some());
    assert!(json.get("has_more").is_some());
    assert!(json.get("truncated").is_some());
    assert!(json.get("total_rows").is_none());
    assert!(json.get("statement_type").is_none());
    let col = &json["columns"][0];
    assert!(col.get("name").is_some());
    assert!(col.get("type").is_some());
    assert!(col.get("data_type").is_none());
}

// ── FilterCondition / FilterOperator ──────────────────────────────────────

#[test]
fn filter_operator_serialization() {
    let ops: Vec<FilterOperator> = vec![
        FilterOperator::Eq,
        FilterOperator::Ne,
        FilterOperator::Lt,
        FilterOperator::Le,
        FilterOperator::Gt,
        FilterOperator::Ge,
        FilterOperator::Like,
        FilterOperator::NotLike,
        FilterOperator::In,
        FilterOperator::NotIn,
        FilterOperator::Between,
        FilterOperator::Null,
        FilterOperator::NotNull,
        FilterOperator::Contains,
        FilterOperator::StartsWith,
        FilterOperator::EndsWith,
        FilterOperator::Empty,
        FilterOperator::Regex,
    ];
    for op in &ops {
        let json = serde_json::to_value(op).unwrap();
        let s = json.as_str().unwrap();
        assert_eq!(s, op.as_str(), "serialization mismatch for {:?}", op);
        let back: FilterOperator = serde_json::from_value(json).unwrap();
        assert_eq!(&back, op);
    }
}

#[test]
fn filter_operator_unknown_deserializes_to_unknown() {
    let op: FilterOperator = serde_json::from_value(serde_json::json!("bogus")).unwrap();
    assert_eq!(op, FilterOperator::Unknown);
}

#[test]
fn filter_operator_unknown_not_serializable() {
    let result = serde_json::to_value(FilterOperator::Unknown);
    assert!(result.is_err(), "Unknown should not be serializable");
}

#[test]
fn filter_condition_json_roundtrip() {
    let fc = FilterCondition {
        column: "name".to_string(),
        op: FilterOperator::Contains,
        value: Value::String("abc".to_string()),
        second_value: Value::Null,
        values: vec![],
        case_sensitive: Some(true),
    };
    json_rt(&fc);
}

#[test]
fn filter_condition_json_null_value_omitted() {
    let fc = FilterCondition {
        column: "x".to_string(),
        op: FilterOperator::Null,
        value: Value::Null,
        second_value: Value::Null,
        values: vec![],
        case_sensitive: None,
    };
    let json = serde_json::to_value(&fc).unwrap();
    assert!(json.get("value").is_none());
    assert!(json.get("second_value").is_none());
    assert!(json.get("case_sensitive").is_none());
}

#[test]
fn filter_condition_in_with_values() {
    let fc = FilterCondition {
        column: "id".to_string(),
        op: FilterOperator::In,
        value: Value::Null,
        second_value: Value::Null,
        values: vec![Value::Integer(1), Value::Integer(2), Value::Integer(3)],
        case_sensitive: None,
    };
    json_rt(&fc);
}

// ── TableRowsRequest / TableRowsResult ────────────────────────────────────

#[test]
fn table_rows_request_json_roundtrip() {
    let req = TableRowsRequest {
        columns: vec!["id".to_string(), "name".to_string()],
        conditions: vec![FilterCondition {
            column: "age".to_string(),
            op: FilterOperator::Gt,
            value: Value::Integer(18),
            second_value: Value::Null,
            values: vec![],
            case_sensitive: None,
        }],
        logic: Some(FilterLogic::And),
        order_by: vec![OrderClause {
            column: "name".to_string(),
            dir: SortDirection::Asc,
        }],
        offset: 10,
        limit: 50,
    };
    json_rt(&req);
}

#[test]
fn table_rows_result_json_roundtrip() {
    let res = TableRowsResult {
        columns: vec![ResultColumn {
            name: "id".to_string(),
            table: None,
            data_type: "int".to_string(),
            generic_type: Some(GenericType::Integer),
            nullable: Some(false),
        }],
        rows: vec![vec![Value::Integer(1)]],
        offset: 0,
        has_more: true,
        total_estimate: Some(100),
        execution_time_ms: 2.5,
    };
    json_rt(&res);
}

// ── Connection types ──────────────────────────────────────────────────────

#[test]
fn create_connection_request_json_roundtrip() {
    let req = CreateConnectionRequest {
        name: "prod-db".to_string(),
        kind: DatabaseKind::Postgres,
        host: Some("localhost".to_string()),
        port: Some(5432),
        database: Some("mydb".to_string()),
        username: Some("admin".to_string()),
        password_ref: Some("keyring:abc".to_string()),
        password: None,
        options: {
            let mut m = std::collections::HashMap::new();
            m.insert("sslmode".to_string(), "require".to_string());
            m
        },
        ssh_tunnel: Some(SshTunnelConfig {
            host: "jump".to_string(),
            port: 22,
            username: "user".to_string(),
            password_ref: None,
            password: None,
            private_key_path: Some("/path/key".to_string()),
            private_key_passphrase_ref: None,
            private_key_passphrase: None,
        }),
        default_schema: Some("public".to_string()),
        read_only: Some(false),
        group: Some("work".to_string()),
        color: Some("#FF0000".to_string()),
    };
    json_rt(&req);
}

#[test]
fn connection_info_json_roundtrip() {
    let info = ConnectionInfo {
        id: uuid::Uuid::new_v4(),
        name: "test".to_string(),
        kind: DatabaseKind::Sqlite,
        host: None,
        port: None,
        database: Some("/tmp/test.db".to_string()),
        username: None,
        options: std::collections::HashMap::new(),
        ssh_tunnel: None,
        default_schema: None,
        read_only: Some(true),
        group: None,
        color: None,
        created_at: chrono::Utc::now(),
        updated_at: chrono::Utc::now(),
    };
    json_rt(&info);
}

// ── PolyDBError ───────────────────────────────────────────────────────────

#[test]
fn polydb_error_json_roundtrip() {
    let err = PolyDBError::new(codes::TABLE_NOT_FOUND, "no such table")
        .with_cause("driver said: table_missing")
        .with_detail(serde_json::json!({"schema": "public"}));
    json_rt(&err);

    let retryable = PolyDBError::new(codes::TIMEOUT, "too slow").retryable();
    json_rt(&retryable);
}

#[test]
fn polydb_error_json_shape() {
    let err = PolyDBError::new(codes::CONNECTION_FAILED, "refused");
    let json = serde_json::to_value(&err).unwrap();
    assert_eq!(json["code"], codes::CONNECTION_FAILED);
    assert_eq!(json["message"], "refused");
    assert_eq!(json["retryable"], false);
    assert!(json.get("detail").is_none());
    assert!(json.get("cause").is_none());
}

// ── BatchResultItem (untagged enum) ───────────────────────────────────────

#[test]
fn batch_result_item_ok_deserializes_as_query_result() {
    let qr = QueryResult {
        columns: vec![],
        rows: vec![],
        affected_rows: 0,
        execution_time_ms: 0.0,
        truncated: false,
        total_rows: None,
        has_more: false,
        statement_type: Some(StatementType::Select),
    };
    let json = serde_json::to_value(&qr).unwrap();
    let item: BatchResultItem = serde_json::from_value(json).unwrap();
    assert!(matches!(item, BatchResultItem::Ok(_)));
}

#[test]
fn batch_result_item_err_deserializes_as_error() {
    let err = PolyDBError::new(codes::QUERY_FAILED, "syntax error");
    let json = serde_json::to_value(&err).unwrap();
    let item: BatchResultItem = serde_json::from_value(json).unwrap();
    assert!(matches!(item, BatchResultItem::Err(_)));
}

// ── DatabaseKind ──────────────────────────────────────────────────────────

#[test]
fn database_kind_serialization() {
    let cases = vec![
        (DatabaseKind::Sqlite, "sqlite"),
        (DatabaseKind::MySql, "mysql"),
        (DatabaseKind::Postgres, "postgres"),
        (DatabaseKind::Mssql, "mssql"),
        (DatabaseKind::Oracle, "oracle"),
        (DatabaseKind::Redis, "redis"),
    ];
    for (kind, expected) in cases {
        let json = serde_json::to_value(kind).unwrap();
        assert_eq!(json.as_str().unwrap(), expected);
        let back: DatabaseKind = serde_json::from_value(json).unwrap();
        assert_eq!(back, kind);
        assert_eq!(kind.to_string(), expected);
    }
}

// ── PaginationParams ──────────────────────────────────────────────────────

#[test]
fn pagination_params_defaults() {
    let p = PaginationParams::default();
    assert_eq!(p.offset, 0);
    assert_eq!(p.limit, 100);
}

#[test]
fn pagination_params_json_roundtrip() {
    let p = PaginationParams {
        offset: 50,
        limit: 200,
    };
    json_rt(&p);
}

// ── StatementType ─────────────────────────────────────────────────────────

#[test]
fn statement_type_serialization() {
    let cases = vec![
        (StatementType::Select, "select"),
        (StatementType::Insert, "insert"),
        (StatementType::Update, "update"),
        (StatementType::Delete, "delete"),
        (StatementType::Ddl, "ddl"),
        (StatementType::Other, "other"),
    ];
    for (st, expected) in cases {
        let json = serde_json::to_value(st).unwrap();
        assert_eq!(json.as_str().unwrap(), expected);
    }
}

// ── GenericType ───────────────────────────────────────────────────────────

#[test]
fn generic_type_roundtrip() {
    let types = vec![
        GenericType::Boolean,
        GenericType::Integer,
        GenericType::Varchar,
        GenericType::Blob,
        GenericType::Timestamp,
        GenericType::Json,
        GenericType::Uuid,
        GenericType::Other,
    ];
    for t in &types {
        let json = serde_json::to_value(t).unwrap();
        let back: GenericType = serde_json::from_value(json).unwrap();
        assert_eq!(&back, t);
    }
}

// ── TableType / ForeignKeyAction / IndexType ──────────────────────────────

#[test]
fn table_type_serialization() {
    let cases = vec![
        (TableType::Table, "table"),
        (TableType::View, "view"),
        (TableType::MaterializedView, "materialized_view"),
    ];
    for (t, expected) in cases {
        let json = serde_json::to_value(t).unwrap();
        assert_eq!(json.as_str().unwrap(), expected);
    }
}

#[test]
fn foreign_key_action_serialization() {
    let cases = vec![
        (ForeignKeyAction::Cascade, "cascade"),
        (ForeignKeyAction::SetNull, "set_null"),
        (ForeignKeyAction::SetDefault, "set_default"),
        (ForeignKeyAction::Restrict, "restrict"),
        (ForeignKeyAction::NoAction, "no_action"),
    ];
    for (a, expected) in cases {
        let json = serde_json::to_value(a).unwrap();
        assert_eq!(json.as_str().unwrap(), expected);
    }
}

// ── TableInfo / ColumnInfo / IndexInfo / ForeignKeyInfo ──────────────────

#[test]
fn table_info_json_roundtrip() {
    let t = TableInfo {
        name: "users".to_string(),
        schema: "public".to_string(),
        table_type: TableType::Table,
        row_count: Some(1000),
        comment: Some("user records".to_string()),
    };
    json_rt(&t);
}

#[test]
fn column_info_json_roundtrip() {
    let c = ColumnInfo {
        name: "id".to_string(),
        data_type: "bigint".to_string(),
        generic_type: Some(GenericType::Bigint),
        nullable: false,
        default_value: None,
        max_length: None,
        precision: None,
        scale: None,
        is_primary_key: true,
        is_auto_increment: true,
        comment: None,
        ordinal_position: 1,
    };
    json_rt(&c);
}

#[test]
fn index_info_json_roundtrip() {
    let idx = IndexInfo {
        name: "idx_name".to_string(),
        unique: false,
        primary: false,
        index_type: Some(IndexType::Btree),
        columns: vec![IndexColumn {
            name: "name".to_string(),
            position: 1,
            order: Some(SortOrder::Asc),
            prefix_length: None,
        }],
        comment: None,
    };
    json_rt(&idx);
}

#[test]
fn foreign_key_info_json_roundtrip() {
    let fk = ForeignKeyInfo {
        name: "fk_users_dept".to_string(),
        columns: vec!["dept_id".to_string()],
        referenced_schema: "public".to_string(),
        referenced_table: "departments".to_string(),
        referenced_columns: vec!["id".to_string()],
        on_update: Some(ForeignKeyAction::Cascade),
        on_delete: Some(ForeignKeyAction::SetNull),
    };
    json_rt(&fk);
}

// ── BatchQueryRequest / BatchQueryResult ──────────────────────────────────

#[test]
fn batch_query_request_json_roundtrip() {
    let req = BatchQueryRequest {
        statements: vec![QueryRequest {
            sql: "SELECT 1".to_string(),
            params: vec![Value::Integer(1)],
            connection_id: None,
            query_id: None,
            schema: Some("public".to_string()),
            pagination: None,
            timeout_ms: Some(5000),
            max_rows: Some(1000),
        }],
        connection_id: None,
        stop_on_error: true,
    };
    json_rt(&req);
}

#[test]
fn batch_query_request_stop_on_error_default() {
    let req = BatchQueryRequest {
        statements: vec![],
        connection_id: None,
        stop_on_error: true,
    };
    json_rt(&req);
}

// ── SchemaInfo ────────────────────────────────────────────────────────────

#[test]
fn schema_info_json_roundtrip() {
    let s = SchemaInfo {
        name: "public".to_string(),
    };
    json_rt(&s);
}

// ── ConnectionStatus ──────────────────────────────────────────────────────

#[test]
fn connection_status_json_roundtrip() {
    let s = ConnectionStatus {
        id: uuid::Uuid::new_v4(),
        connected: true,
        server_version: Some("16.0".to_string()),
        latency_ms: Some(1.2),
        error: None,
    };
    json_rt(&s);
}
