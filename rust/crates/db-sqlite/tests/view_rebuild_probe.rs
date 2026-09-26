//! E6.0 取证（Rust 侧，镜像 `go/pkg/dbsqlite/view_rebuild_probe_test.go`）：
//! SQLite 视图按名字在查询期解析，那么「重建表」（CREATE tmp → INSERT SELECT → DROP 原表 →
//! RENAME）到底怎么处理依赖它的视图？`docs/plan-compare-sync.md` §7 一直记着"没证据就不写进规则"。
//! 用 bundled 真引擎把四条事实钉死，结论写回该文档 A.15。
//!
//! E6.5 已拍板（A 档：视图名进快照 + 粗粒度警告），故后两条事实在这里**同时是修复的回归**：
//! 事实 3 断言删表语句必须带警告，事实 4 断言临时名必须让开同名视图。

use polydb_core::{DatabaseKind, Value};
use polydb_db_core::DatabaseDriver;
use polydb_db_sqlite::SqliteConn;
use polydb_schema_diff::{
    diff, generate, select_entries, Options, Script, Snapshot, TableSnapshot,
};

const SRC_WITH_VIEW: &[&str] = &[
    "CREATE TABLE orders (id INTEGER PRIMARY KEY, qty INTEGER NOT NULL DEFAULT 1)",
    "INSERT INTO orders (id, qty) VALUES (1, 10), (2, 20)",
    "CREATE VIEW v_orders AS SELECT id, qty FROM orders",
    "CREATE TRIGGER t_after_ins AFTER INSERT ON orders BEGIN SELECT 1; END",
];
const TGT_WIDER_QTY: &[&str] =
    &["CREATE TABLE orders (id INTEGER PRIMARY KEY, qty TEXT NOT NULL DEFAULT '')"];

fn sql(conn: &SqliteConn) -> &dyn polydb_db_core::SqlDriver {
    conn.as_sql().unwrap()
}

fn exec_report(conn: &SqliteConn, q: &str) -> String {
    match smol::block_on(sql(conn).execute(q, &[])) {
        Ok(_) => "OK".to_string(),
        Err(e) => format!("ERR: {e}"),
    }
}

fn scalar(conn: &SqliteConn, q: &str) -> String {
    match smol::block_on(sql(conn).execute(q, &[])) {
        Err(e) => format!("ERR: {e}"),
        Ok(r) => match r.rows.first().and_then(|row| row.first()) {
            Some(Value::Integer(i)) => i.to_string(),
            Some(Value::String(s)) => s.clone(),
            Some(other) => format!("{other:?}"),
            None => "<no rows>".to_string(),
        },
    }
}

fn conn_with(stmts: &[&str]) -> SqliteConn {
    let conn = SqliteConn::open(":memory:").unwrap();
    for stmt in stmts {
        assert_eq!(exec_report(&conn, stmt), "OK", "{stmt}");
    }
    conn
}

fn snapshot(conn: &SqliteConn, schema: &str) -> Snapshot {
    let d = sql(conn);
    let mut snaps = Vec::new();
    for t in smol::block_on(d.list_tables(schema)).unwrap() {
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

/// 走完整管线取脚本：真快照 → diff → 勾选该表的整组条目 → 生成。
fn probe_script(src: &[&str], tgt: &[&str], table: &str) -> Script {
    let source = snapshot(&conn_with(src), "main");
    let target = snapshot(&conn_with(tgt), "main");
    let plan = diff(&source, &target, &Options::default()).unwrap();
    let ids: Vec<String> = plan
        .iter()
        .filter(|e| e.table == table)
        .map(|e| e.id.clone())
        .collect();
    let selected = select_entries(&source, &target, &plan, &ids).unwrap();
    let script = generate(&source, &target, &selected, &Options::default()).unwrap();
    for (i, s) in script.statements.iter().enumerate() {
        println!(
            "  [{i}] phase={} 包裹条={} 不可逆={} 警告={:?} {}",
            s.phase,
            s.entry_ids.is_empty(),
            s.non_reversible,
            s.warnings,
            s.sql
        );
    }
    println!(
        "能力位 pragmas_outside_tx={} ddl_in_tx={}",
        script.pragmas_outside_tx, script.ddl_in_tx
    );
    script
}

/// 事实 1：依赖视图不是"静默破掉"，而是**让重建在 RENAME 那一步直接失败**——
/// SQLite 的 ALTER TABLE RENAME 会重解析引用该名字的视图，此时原表刚被 DROP，视图解析不出来。
#[test]
fn rebuild_with_dependent_view_fails_at_rename() {
    let script = probe_script(SRC_WITH_VIEW, TGT_WIDER_QTY, "orders");
    let conn = conn_with(SRC_WITH_VIEW);
    let results: Vec<String> = script
        .statements
        .iter()
        .map(|s| exec_report(&conn, &s.sql))
        .collect();
    println!("逐条执行结果：{results:#?}");

    let rename = results.len() - 2; // 倒数第二条是 RENAME，最后一条是尾部 PRAGMA
    assert!(
        results[rename].contains("no such table: main.orders"),
        "RENAME 必须因依赖视图而失败，实际：{}",
        results[rename]
    );
    assert!(results[rename].contains("error in view v_orders"));
    // 逐条（等价 ddl_in_tx=false 的分派）跑完 ⇒ 中间态是**丢表**：原表已 DROP，数据困在临时表里。
    assert!(
        exec_report(&conn, "SELECT count(*) FROM orders").starts_with("ERR"),
        "中间态下 orders 应已不存在"
    );
    assert_eq!(
        scalar(&conn, "SELECT count(*) FROM polydb_tmp_orders"),
        "2",
        "数据应困在临时表里（2 行）"
    );
    assert_eq!(
        scalar(
            &conn,
            "SELECT count(*) FROM sqlite_master WHERE type='trigger'"
        ),
        "0",
        "触发器应随 DROP 一起没了（与取证 G4 一致）"
    );
}

/// 事实 2：SQLite 的真实分派是「首尾 PRAGMA 事务外 + 主体单事务」。走这一支时 RENAME 失败
/// ⇒ 整体回滚 ⇒ 表、数据、视图、列定义全部毫发无损。事务保护在这条路径上确实兜住了。
#[test]
fn rebuild_with_dependent_view_rolls_back_clean_inside_tx() {
    let script = probe_script(SRC_WITH_VIEW, TGT_WIDER_QTY, "orders");
    let conn = conn_with(SRC_WITH_VIEW);
    assert_eq!(exec_report(&conn, "BEGIN"), "OK");
    let mut failed = String::new();
    for s in &script.statements {
        if s.phase == 0 || s.phase == 255 {
            continue; // 取证 G6：PRAGMA 包裹条本就在事务外
        }
        let r = exec_report(&conn, &s.sql);
        if r.starts_with("ERR") {
            failed = format!("{} ⇒ {r}", s.sql);
            break;
        }
    }
    assert!(!failed.is_empty(), "取证前提：主体必须真的失败");
    println!("首条失败：{failed}");
    assert_eq!(exec_report(&conn, "ROLLBACK"), "OK");
    assert_eq!(scalar(&conn, "SELECT count(*) FROM orders"), "2");
    assert_eq!(scalar(&conn, "SELECT count(*) FROM v_orders"), "2");
    assert_eq!(
        scalar(&conn, "SELECT typeof(qty) FROM orders LIMIT 1"),
        "integer"
    );
    assert!(
        scalar(&conn, "SELECT count(*) FROM polydb_tmp_orders").starts_with("ERR"),
        "回滚后临时表不该残留"
    );
}

/// 事实 3：不涉及重建、只是 `drop_table` 时，DROP **引擎层面仍然静默成功**并把破视图留在库里
/// （`sqlite_master` 里条目还在，查询才报错）——与取证 G4「重建静默丢触发器」同类。
/// E6.5 拍板 A 档后补的是**语句级警告**：引擎不会拦，但脚本必须把这件事说出来。
#[test]
fn drop_table_leaves_a_dangling_view_but_warns() {
    let source = snapshot(&conn_with(SRC_WITH_VIEW), "main");
    let target = snapshot(&conn_with(&[]), "main");
    let plan = diff(&source, &target, &Options::default()).unwrap();
    let ids: Vec<String> = plan.iter().map(|e| e.id.clone()).collect();
    let selected = select_entries(&source, &target, &plan, &ids).unwrap();
    let script = generate(&source, &target, &selected, &Options::default()).unwrap();
    let warnings: Vec<Vec<String>> = script
        .statements
        .iter()
        .filter(|s| s.sql.starts_with("DROP TABLE"))
        .map(|s| s.warnings.clone())
        .collect();
    println!("实测：删表语句的警告 = {warnings:?}");
    // 引擎静默，但语句必须带警告，且警告要点名库里那个视图（E6.5 A 档的粗粒度警告）。
    assert_eq!(warnings.len(), 1, "这条脚本里就该有一条删表语句");
    let w = &warnings[0];
    assert_eq!(w.len(), 1, "删表只挂一条视图警告：{w:?}");
    assert!(
        w[0].contains("v_orders") && w[0].contains("不检查依赖"),
        "警告必须点名视图并说清「DROP 不检查依赖」：{w:?}"
    );

    let conn = conn_with(SRC_WITH_VIEW);
    for s in &script.statements {
        assert_eq!(
            exec_report(&conn, &s.sql),
            "OK",
            "删表脚本必须全程无错（引擎层面静默）"
        );
    }
    assert_eq!(
        scalar(
            &conn,
            "SELECT count(*) FROM sqlite_master WHERE type='view'"
        ),
        "1",
        "SQLite 不会连带删视图，sqlite_master 里应还留着"
    );
    assert!(
        exec_report(&conn, "SELECT * FROM v_orders").starts_with("ERR"),
        "视图此时必须已破"
    );
}

/// 事实 4（E6.5 拍板 A 档后从「取证」转为「回归」）：临时名去重集现在**看得见视图**
/// ⇒ 库里已有同名视图时生成的临时名自动避让，整条重建在真引擎上跑得通，那个视图也毫发无损，
/// 并且重建语句带着粗粒度视图警告。
/// 修复前的实测（本测试的旧版本）：临时名正好撞上视图名，首条 `CREATE TABLE` 即 already exists。
#[test]
fn temp_name_avoids_an_existing_view_name() {
    let src: &[&str] = &[
        "CREATE TABLE orders (id INTEGER PRIMARY KEY, qty INTEGER NOT NULL DEFAULT 1)",
        "INSERT INTO orders (id, qty) VALUES (1, 10)",
        "CREATE VIEW polydb_tmp_orders AS SELECT 1 AS one",
    ];
    let script = probe_script(src, TGT_WIDER_QTY, "orders");
    let head = &script.statements[1];
    assert!(
        head.sql.contains("\"polydb_tmp_orders_2\""),
        "临时名必须让开同名视图（撞名是修复前的行为）：{}",
        head.sql
    );
    assert!(
        head.warnings
            .last()
            .is_some_and(|w| w.contains("polydb_tmp_orders")),
        "视图警告要点名它挡路的那个视图：{:?}",
        head.warnings
    );

    let conn = conn_with(src);
    for s in &script.statements {
        assert_eq!(
            exec_report(&conn, &s.sql),
            "OK",
            "避让之后整条重建必须全程无错：{}",
            s.sql
        );
    }
    assert_eq!(
        scalar(&conn, "SELECT count(*) FROM orders"),
        "1",
        "重建后 orders 行数应保持不变"
    );
    assert!(
        scalar(
            &conn,
            "SELECT sql FROM sqlite_master WHERE type='table' AND name='orders'"
        )
        .to_uppercase()
        .contains("TEXT"),
        "库里那份表定义必须真的是新版的"
    );
    // 挡过路的那个视图必须还在、还能查。
    assert_eq!(
        scalar(&conn, "SELECT one FROM polydb_tmp_orders"),
        "1",
        "同名视图不该被重建波及"
    );
}

/// 补充取证（E6.5 定警告口径时顺带测的）：被视图引用的列能不能直改删掉？
///
/// 结论：**引擎会拦**，语句直接报错、列原地不动 ⇒ 删列属于 behavior.md §14.3 的
/// 「不猜语义、执行期报错兜底」那一档，所以生成器**不给**删列挂视图警告（报了等于重复报错）。
/// 这与删表正好相反：删表引擎不拦、静默留破视图 ⇒ 必须由生成器说一句话。
#[test]
fn drop_column_referenced_by_view_is_blocked_by_engine() {
    let conn = conn_with(SRC_WITH_VIEW);
    let r = exec_report(&conn, "ALTER TABLE orders DROP COLUMN qty");
    println!("实测：删掉被视图引用的列 ⇒ {r}");
    assert!(
        r.starts_with("ERR"),
        "引擎必须拦住「删掉视图正引用的列」，实际：{r}"
    );
    assert!(
        r.contains("error in view"),
        "报错必须来自视图重解析（error in view …），实际：{r}"
    );
    // 拦下来就得一事无成：列还在、视图还能查、数据没动。
    assert_eq!(
        scalar(
            &conn,
            "SELECT count(*) FROM pragma_table_info('orders') WHERE name='qty'"
        ),
        "1",
        "报错后 qty 必须还在"
    );
    assert_eq!(
        scalar(&conn, "SELECT count(*) FROM v_orders"),
        "2",
        "报错后视图必须还能查"
    );
}
