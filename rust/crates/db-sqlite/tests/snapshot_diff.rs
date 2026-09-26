//! E2.0 真快照复核：用**真实 SQLite 驱动**采集元数据，喂给 E1 的 diff。
//!
//! 这里测的不是 diff 的规则（那由 `test/schema-diff-vectors` 逐字锁死），而是
//! **驱动填出来的字段是否满足 diff 的假设**：
//! 1. 外键标识必须是 DDL 的确定函数——否则同一份 DDL 两次采集就能骗出一对 drop+add；
//! 2. 主键必须能被 `IndexInfo.primary` 认出来（`PRAGMA index_list.origin == 'pk'`）；
//! 3. `PRAGMA foreign_key_list` 的 ON DELETE / ON UPDATE 必须落到 DTO，否则 diff 的这个维度恒为死；
//! 4. 视图必须带 `TableType::View`，diff 才能整体跳过。

use polydb_core::{DatabaseKind, ForeignKeyAction, TableType};
use polydb_db_core::DatabaseDriver;
use polydb_db_sqlite::SqliteConn;
use polydb_schema_diff::{diff, Entry, Options, Snapshot, TableSnapshot};

/// 基线 DDL：两张被引用表 + 一张带两个外键与一个索引的表 + 一张复合主键表。
const BASE: &[&str] = &[
    "CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
    "CREATE TABLE products (id INTEGER PRIMARY KEY, sku VARCHAR(20) NOT NULL DEFAULT '')",
    "CREATE TABLE orders (id INTEGER PRIMARY KEY, \
       customer_id INTEGER NOT NULL REFERENCES customers(id) ON DELETE CASCADE, \
       product_id INTEGER NOT NULL REFERENCES products(id), \
       qty INTEGER NOT NULL DEFAULT 1)",
    "CREATE INDEX idx_orders_customer ON orders(customer_id)",
    "CREATE TABLE pairs (a INTEGER NOT NULL, b INTEGER NOT NULL, PRIMARY KEY (a, b))",
];

fn conn_with(stmts: &[&str]) -> SqliteConn {
    let conn = SqliteConn::open(":memory:").unwrap();
    let d = conn.as_sql().unwrap();
    for stmt in stmts {
        smol::block_on(d.execute(stmt, &[])).expect(stmt);
    }
    conn
}

/// 用驱动的四个元数据方法组装快照——E2 服务端要做的正是这段编排。
fn snapshot(conn: &SqliteConn, schema: &str) -> Snapshot {
    let d = conn.as_sql().unwrap();
    let tables = smol::block_on(d.list_tables(schema)).unwrap();
    let mut snaps = Vec::new();
    for t in tables {
        // 与 app-core 的 collect_snapshot 同口径（E6.5）：视图只带名字与类型，零元数据查询。
        if t.table_type != polydb_core::TableType::Table {
            snaps.push(TableSnapshot {
                name: t.name,
                table_type: Some(t.table_type),
                columns: Vec::new(),
                indexes: Vec::new(),
                foreign_keys: Vec::new(),
            });
            continue;
        }
        snaps.push(TableSnapshot {
            name: t.name.clone(),
            table_type: Some(t.table_type),
            columns: smol::block_on(d.list_columns(schema, &t.name)).unwrap(),
            indexes: smol::block_on(d.list_indexes(schema, &t.name)).unwrap(),
            foreign_keys: smol::block_on(d.list_foreign_keys(schema, &t.name)).unwrap(),
        });
    }
    Snapshot::new(DatabaseKind::Sqlite, schema, snaps)
}

fn plan(source: &Snapshot, target: &Snapshot) -> Vec<Entry> {
    diff(source, target, &Options::default()).expect("同种类比对不应报错")
}

/// 计划的可读摘要：`(op, 表, 对象)`，失败时比 `{:?}` 好认。
fn summary(entries: &[Entry]) -> Vec<(String, String, String)> {
    entries
        .iter()
        .map(|e| (e.op.as_str().to_string(), e.table.clone(), e.object.clone()))
        .collect()
}

#[test]
fn identical_real_databases_produce_empty_plan() {
    let source = snapshot(&conn_with(BASE), "main");
    let target = snapshot(&conn_with(BASE), "main");
    let got = plan(&source, &target);
    assert!(
        got.is_empty(),
        "同一份 DDL 的两个库必须零差异，实际：{:?}",
        summary(&got)
    );
}

#[test]
fn add_and_drop_a_real_table_match_the_phase_order() {
    let source = snapshot(&conn_with(BASE), "main");
    let mut stmts = vec![BASE[0], BASE[1], BASE[2], BASE[3]];
    stmts.push("CREATE TABLE shipments (id INTEGER PRIMARY KEY, orders_id INTEGER NOT NULL REFERENCES orders(id))");
    let target = snapshot(&conn_with(&stmts), "main");

    let got = plan(&source, &target);
    assert_eq!(
        summary(&got),
        vec![
            ("drop_table".into(), "pairs".into(), String::new()),
            ("create_table".into(), "shipments".into(), String::new()),
        ],
        "删表(40) 必须先于建表(50)"
    );
    assert!(!got[0].selected, "drop_table 是破坏性动作，默认不勾选");
    assert!(got[1].selected);
}

#[test]
fn foreign_keys_are_unnamed_and_stable_across_collections() {
    /// diff 真正用来配对的东西：自身列 → 引用目标 + 名字。**不按名字排序**，
    /// 因为要断言的是驱动发射序本身确定。
    fn keyed(snap: &Snapshot) -> Vec<(Vec<String>, String, String)> {
        snap.tables
            .iter()
            .filter(|t| t.name == "orders")
            .flat_map(|t| {
                t.foreign_keys.iter().map(|f| {
                    (
                        f.columns.clone(),
                        f.referenced_table.clone(),
                        f.name.clone(),
                    )
                })
            })
            .collect()
    }

    let a = keyed(&snapshot(&conn_with(BASE), "main"));
    let b = keyed(&snapshot(&conn_with(BASE), "main"));
    assert_eq!(
        a, b,
        "外键标识与发射顺序必须是 DDL 的确定函数，不能随采集次数变化"
    );
    // 顺序取自 PRAGMA 的 id（SQLite 内部序，实测与声明顺序相反）。两端读的是同一个
    // C 引擎的 PRAGMA，因此这个顺序双端一致；而 diff 的配对不依赖它（名为空 ⇒ 按内容配对）。
    assert_eq!(
        a,
        vec![
            (
                vec!["product_id".to_string()],
                "products".to_string(),
                String::new()
            ),
            (
                vec!["customer_id".to_string()],
                "customers".to_string(),
                String::new()
            ),
        ],
        "SQLite 外键本来无名：留空由 diff 按内容配对，绝不合成 fk_i 编号"
    );
}

#[test]
fn adding_one_real_foreign_key_does_not_disturb_the_others() {
    // 回归：驱动曾把外键命名为 fk_0/fk_1（HashMap 序 + 位置编号）。新增一条外键会让
    // 其余外键的编号整体错位，凭空产出成串的 drop/add 对。
    let source = snapshot(&conn_with(BASE), "main");
    let mut stmts = BASE.to_vec();
    stmts.push("CREATE TABLE warehouses (id INTEGER PRIMARY KEY)");
    let conn = conn_with(&stmts);
    smol::block_on(conn.as_sql().unwrap().execute(
        "ALTER TABLE orders ADD COLUMN warehouse_id INTEGER REFERENCES warehouses(id)",
        &[],
    ))
    .unwrap();
    let target = snapshot(&conn, "main");

    let got = plan(&source, &target);
    assert_eq!(
        summary(&got),
        vec![
            ("create_table".into(), "warehouses".into(), String::new()),
            ("add_column".into(), "orders".into(), "warehouse_id".into()),
            (
                "add_foreign_key".into(),
                "orders".into(),
                "fk(warehouse_id)".into()
            ),
        ],
        "只应产出 1 条新外键 + 1 条新列，其余外键不得被牵连"
    );
}

#[test]
fn foreign_key_actions_come_from_real_metadata() {
    let snap = snapshot(&conn_with(BASE), "main");
    let orders = snap.tables.iter().find(|t| t.name == "orders").unwrap();
    let by_col = |want: &str| {
        orders
            .foreign_keys
            .iter()
            .find(|f| f.columns == [want.to_string()])
            .unwrap_or_else(|| panic!("未采到 {want} 上的外键"))
            .on_delete
    };
    assert_eq!(
        by_col("customer_id"),
        Some(ForeignKeyAction::Cascade),
        "ON DELETE CASCADE 必须落到 DTO"
    );
    // SQLite 未写 ON 子句时的默认动作是 NO ACTION，不是「无信息」。
    assert_eq!(by_col("product_id"), Some(ForeignKeyAction::NoAction));
    let customer = orders
        .foreign_keys
        .iter()
        .find(|f| f.columns == ["customer_id"])
        .unwrap();
    assert_eq!(customer.on_update, Some(ForeignKeyAction::NoAction));
}

#[test]
fn primary_key_index_is_marked_from_real_metadata() {
    let snap = snapshot(&conn_with(BASE), "main");

    let pairs = snap.tables.iter().find(|t| t.name == "pairs").unwrap();
    let pks: Vec<&String> = pairs
        .indexes
        .iter()
        .filter(|i| i.primary)
        .map(|i| &i.name)
        .collect();
    assert_eq!(
        pks.len(),
        1,
        "复合主键表必须恰有一条 primary 索引，实际：{:?}",
        pks
    );
    assert!(
        pks[0].starts_with("sqlite_autoindex_"),
        "SQLite 主键索引是隐式的，名字形如 sqlite_autoindex_pairs_1，实际：{}",
        pks[0]
    );

    // rowid 别名式主键（INTEGER PRIMARY KEY）不建索引，orders 不得被误标 primary。
    let orders = snap.tables.iter().find(|t| t.name == "orders").unwrap();
    assert!(
        orders.indexes.iter().all(|i| !i.primary),
        "orders 的主键是 rowid 别名，没有索引，不该出现 primary"
    );
    assert!(orders
        .indexes
        .iter()
        .any(|i| i.name == "idx_orders_customer"));
}

#[test]
fn real_primary_key_change_rebuilds_the_index() {
    let source = snapshot(&conn_with(BASE), "main");
    let mut target_stmts = BASE.to_vec();
    target_stmts[4] =
        "CREATE TABLE pairs (b INTEGER NOT NULL, a INTEGER NOT NULL, PRIMARY KEY (b, a))";
    let target = snapshot(&conn_with(&target_stmts), "main");

    let got = plan(&source, &target);
    assert_eq!(
        summary(&got),
        vec![
            (
                "drop_index".into(),
                "pairs".into(),
                "sqlite_autoindex_pairs_1".into()
            ),
            (
                "add_index".into(),
                "pairs".into(),
                "sqlite_autoindex_pairs_1".into()
            ),
        ],
        "主键列序变化必须走主键重建路径"
    );
    assert!(
        got.iter().all(|e| e.reason == "主键列变化，需重建"),
        "原因文案必须是主键专用，实际：{:?}",
        got.iter().map(|e| e.reason.clone()).collect::<Vec<_>>()
    );
}

#[test]
fn real_differences_between_two_files_produce_expected_plan() {
    let source = snapshot(&conn_with(BASE), "main");

    let mut target_stmts = BASE.to_vec();
    target_stmts[1] =
        "CREATE TABLE products (id INTEGER PRIMARY KEY, sku varchar(30) NOT NULL DEFAULT '')";
    target_stmts[2] = "CREATE TABLE orders (id INTEGER PRIMARY KEY, \
        customer_id INTEGER NOT NULL REFERENCES customers(id) ON DELETE CASCADE, \
        product_id INTEGER NOT NULL REFERENCES products(id), \
        qty INTEGER NOT NULL DEFAULT 1, note TEXT)";
    target_stmts[3] = "CREATE INDEX idx_orders_note ON orders(note)";
    let target = snapshot(&conn_with(&target_stmts), "main");

    let got = plan(&source, &target);
    assert_eq!(
        summary(&got),
        vec![
            (
                "drop_index".into(),
                "orders".into(),
                "idx_orders_customer".into()
            ),
            ("add_column".into(), "orders".into(), "note".into()),
            ("alter_column".into(), "products".into(), "sku".into()),
            (
                "add_index".into(),
                "orders".into(),
                "idx_orders_note".into()
            ),
        ],
        "相位序：删索引(20) → 加列(60) → 改列(70) → 加索引(80)"
    );
    let alter = got
        .iter()
        .find(|e| e.op.as_str() == "alter_column")
        .unwrap();
    assert_eq!(alter.reason, "类型 varchar(20) → varchar(30)");
    assert_eq!(alter.flags.len(), 1);
    assert!(
        got.iter().find(|e| e.object == "note").unwrap().selected,
        "新增列不是破坏性动作，默认勾选"
    );
    assert!(
        !got.iter()
            .find(|e| e.op.as_str() == "drop_index")
            .unwrap()
            .selected,
        "删索引是破坏性动作，默认不勾选（D4）"
    );
}

#[test]
fn real_views_are_skipped() {
    let source = snapshot(&conn_with(BASE), "main");
    let mut stmts = BASE.to_vec();
    stmts.push("CREATE VIEW v_orders AS SELECT id FROM orders");
    let target_snap = conn_with(&stmts);
    let target = snapshot(&target_snap, "main");

    let view = target.tables.iter().find(|t| t.name == "v_orders").unwrap();
    assert_eq!(
        view.table_type,
        Some(TableType::View),
        "驱动必须区分表与视图"
    );
    assert!(
        plan(&source, &target).is_empty(),
        "视图不参与比对：{:?}",
        summary(&plan(&source, &target))
    );
}

#[test]
fn unique_constraint_index_is_not_a_primary_key() {
    let stmts = [
        "CREATE TABLE t (id INTEGER PRIMARY KEY, email TEXT UNIQUE)",
        "CREATE TABLE u (id INTEGER PRIMARY KEY, email TEXT UNIQUE)",
    ];
    let got = plan(
        &snapshot(&conn_with(&stmts), "main"),
        &snapshot(&conn_with(&stmts), "main"),
    );
    assert!(
        got.is_empty(),
        "UNIQUE 隐式索引两侧一致即无差异：{:?}",
        summary(&got)
    );

    let snap = snapshot(&conn_with(&stmts), "main");
    let t = snap.tables.iter().find(|x| x.name == "t").unwrap();
    let uniq = t
        .indexes
        .iter()
        .find(|i| i.name.starts_with("sqlite_autoindex_"))
        .unwrap();
    assert!(
        uniq.unique && !uniq.primary,
        "origin='u' 只能是唯一索引，不是主键"
    );
}
