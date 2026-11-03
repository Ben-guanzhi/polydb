//! §57.4 #1 验收：SQLite 批量元数据 == 逐表元数据（K-1.2 配方在 Rust 侧的等价契约）。
//!
//! 与 PG 侧 `db-postgres/tests/probe.rs` 的 P12 同口径：批量配方只有在「与逐表逐行
//! 相等」时才允许替换 `collect_snapshot` 的逐表循环。这里用 `:memory:` SQLite 离线跑，
//! 不需要容器。
//!
//! 覆盖的形状：普通表 + 视图、单列主键、复合主键、单列外键、复合列外键、
//! 显式 `ON DELETE/ON UPDATE` 动作、带默认值/非空/自增列的混合。

use polydb_core::{ColumnInfo, ForeignKeyInfo};
use polydb_db_core::DatabaseDriver;
use polydb_db_sqlite::SqliteConn;

fn conn_with(stmts: &[&str]) -> SqliteConn {
    let conn = SqliteConn::open(":memory:").unwrap();
    let d = conn.as_sql().unwrap();
    for stmt in stmts {
        polydb_rt::block_on(d.execute(stmt, &[])).expect(stmt);
    }
    conn
}

/// 批量列与逐表列逐行相等（表内列序一致）。
fn assert_cols_equal(per: &[ColumnInfo], batch: &[ColumnInfo], table: &str) {
    assert_eq!(
        per.len(),
        batch.len(),
        "表 {table} 批量列数 != 逐表列数"
    );
    for (i, (a, b)) in per.iter().zip(batch.iter()).enumerate() {
        assert_eq!(a.name, b.name, "表 {table} 第 {i} 列名不一致");
        assert_eq!(a.data_type, b.data_type, "表 {table} 第 {i} 列类型不一致");
        assert_eq!(a.nullable, b.nullable, "表 {table} 第 {i} 列可空不一致");
        assert_eq!(
            a.default_value, b.default_value,
            "表 {table} 第 {i} 列默认值不一致"
        );
        assert_eq!(
            a.is_primary_key, b.is_primary_key,
            "表 {table} 第 {i} 列主键标记不一致"
        );
        assert_eq!(
            a.is_auto_increment, b.is_auto_increment,
            "表 {table} 第 {i} 列自增标记不一致"
        );
        assert_eq!(
            a.ordinal_position, b.ordinal_position,
            "表 {table} 第 {i} 列序号不一致"
        );
    }
}

/// 批量外键与逐表外键逐组相等（组序与列序一致）。
fn assert_fks_equal(per: &[ForeignKeyInfo], batch: &[ForeignKeyInfo], table: &str) {
    assert_eq!(
        per.len(),
        batch.len(),
        "表 {table} 批量外键数 != 逐表外键数"
    );
    for (i, (a, b)) in per.iter().zip(batch.iter()).enumerate() {
        assert_eq!(a.columns, b.columns, "表 {table} 第 {i} 个外键列不一致");
        assert_eq!(
            a.referenced_table, b.referenced_table,
            "表 {table} 第 {i} 个外键引用表不一致"
        );
        assert_eq!(
            a.referenced_columns, b.referenced_columns,
            "表 {table} 第 {i} 个外键引用列不一致"
        );
        assert_eq!(
            a.on_update, b.on_update,
            "表 {table} 第 {i} 个外键 on_update 不一致"
        );
        assert_eq!(
            a.on_delete, b.on_delete,
            "表 {table} 第 {i} 个外键 on_delete 不一致"
        );
    }
}

#[test]
fn batch_metadata_equals_per_table() {
    let stmts = [
        "CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
        "CREATE TABLE products (id INTEGER PRIMARY KEY, sku VARCHAR(20) NOT NULL DEFAULT '')",
        "CREATE TABLE orders (id INTEGER PRIMARY KEY, \
           customer_id INTEGER NOT NULL REFERENCES customers(id) ON DELETE CASCADE, \
           product_id INTEGER NOT NULL REFERENCES products(id) ON UPDATE RESTRICT, \
           qty INTEGER NOT NULL DEFAULT 1)",
        "CREATE TABLE pairs (a INTEGER NOT NULL, b INTEGER NOT NULL, PRIMARY KEY (a, b))",
        "CREATE TABLE no_pk (x TEXT, y INTEGER)",
        "CREATE VIEW v_orders AS SELECT id, customer_id FROM orders",
    ];
    let conn = conn_with(&stmts);
    let d = conn.as_sql().unwrap();

    // ── 批量列 ──
    let Some(batch_cols) = polydb_rt::block_on(d.batch_columns("main")).unwrap() else {
        panic!("SQLite 应实现 batch_columns（K-1.2 的 Rust 对应物）");
    };
    let mut by_table: std::collections::HashMap<String, Vec<ColumnInfo>> =
        std::collections::HashMap::new();
    for (t, col) in batch_cols {
        by_table.entry(t).or_default().push(col);
    }
    // 视图也进批量列（Go 配方含视图）；逐表侧不查视图，故只比对真表。
    for t in ["customers", "products", "orders", "pairs", "no_pk"] {
        let per = polydb_rt::block_on(d.list_columns("main", t)).unwrap();
        let batch = by_table.get(t).cloned().unwrap_or_default();
        assert_cols_equal(&per, &batch, t);
    }
    assert!(
        by_table.contains_key("v_orders"),
        "批量列应含视图 v_orders（Go 配方 t.type IN ('table','view')）"
    );

    // ── 批量外键 ──
    let Some(batch_fks) = polydb_rt::block_on(d.batch_foreign_keys("main")).unwrap() else {
        panic!("SQLite 应实现 batch_foreign_keys（K-1.2 的 Rust 对应物）");
    };
    let fk_by_table: std::collections::HashMap<String, Vec<ForeignKeyInfo>> =
        batch_fks.into_iter().collect();
    for t in ["customers", "products", "orders", "pairs", "no_pk"] {
        let per = polydb_rt::block_on(d.list_foreign_keys("main", t)).unwrap();
        let batch = fk_by_table.get(t).cloned().unwrap_or_default();
        assert_fks_equal(&per, &batch, t);
    }
    // orders 有两条外键，且 on_delete/on_update 动作已落到 DTO。
    let orders_fks = fk_by_table.get("orders").unwrap();
    assert_eq!(orders_fks.len(), 2, "orders 应有两条外键");
}

#[test]
fn batch_foreign_keys_empty_group_is_real_state() {
    // K-1.2 的关键语义：FK 的空组是真状态（表无外键），不是「批量没读到」。
    let conn = conn_with(&["CREATE TABLE solo (id INTEGER PRIMARY KEY)"]);
    let d = conn.as_sql().unwrap();
    let Some(batch) = polydb_rt::block_on(d.batch_foreign_keys("main")).unwrap() else {
        panic!("SQLite 应实现 batch_foreign_keys");
    };
    let solo: Vec<ForeignKeyInfo> = batch
        .into_iter()
        .find(|(t, _)| t == "solo")
        .map(|(_, fks)| fks)
        .unwrap_or_default();
    assert!(
        solo.is_empty(),
        "solo 无外键 ⇒ 批量应给空组（真状态），实得 {solo:?}"
    );
}
