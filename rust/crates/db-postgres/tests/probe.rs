//! F0.2 PG 方言取证（Rust 侧）——`rust/crates/db-postgres/` 的第一份活 PG 测试。
//!
//! 与 Go 侧 `go/pkg/dbpostgres/probe_test.go`（F0.1，12 点）分档：
//! 这里**只镜像驱动事实**（sqlx 把系统目录读成了什么）＝ P1、P7–P11；
//! 引擎事实（P2–P6、P12，PG 服务器自己的脾气）与客户端语言无关，Go 侧探一次就够，
//! 两端各探一遍只会造出「两个实现两个真值」的假象。
//!
//! 观测以 `OBS[px]` 前缀打印（`cargo test -p polydb-db-postgres -- --nocapture` 才看得见），
//! 断言只打在我们自身层的形状不变式与该点要钉的判据上；结论回填 docs/plan-compare-sync.md A.26。
//!
//! 软跳过口径（本 crate 首次建立）：`PostgresConn::open` 是**懒连接**，建不出池不代表连不上，
//! 所以真探是 `ping()`；打不开就 eprintln 一条 SKIP 后直接 return——
//! 与 Go 侧 `connected:false` 同一语义，缺引擎时全绿而非缺测。

use polydb_core::Value;
use polydb_db_core::{DatabaseDriver, SqlDriver, TxMode};
use polydb_db_postgres::PostgresConn;

const PROBE_SCHEMA: &str = "polydb_probe";

/// 与 Go 侧、test/contract 同一默认串：要么三处都能连，要么三处都静默跳过。
const DEFAULT_DSN: &str = "postgres://postgres@localhost:5432/postgres?sslmode=disable";

fn dsn() -> String {
    std::env::var("POLYDB_TEST_PG").unwrap_or_else(|_| DEFAULT_DSN.to_string())
}

/// 取一个干净的探测现场；连不上即软跳过（返回 None，调用方直接 return）。
async fn probe_conn(label: &str) -> Option<PostgresConn> {
    let url = dsn();
    let conn = match PostgresConn::open(&url) {
        Ok(c) => c,
        Err(e) => {
            eprintln!("SKIP[{label}] connected:false 打不开 PG（POLYDB_TEST_PG={url}）：{e}");
            return None;
        }
    };
    if let Err(e) = conn.ping().await {
        eprintln!("SKIP[{label}] connected:false 无可用 PG（POLYDB_TEST_PG={url}）：{e}");
        return None;
    }
    // 探测会在真库上建对象，专用 schema 每次都重建，绝不落在 public 里。
    if let Err(e) = conn
        .execute(
            &format!("DROP SCHEMA IF EXISTS {PROBE_SCHEMA} CASCADE"),
            &[],
        )
        .await
    {
        panic!("清理旧探测 schema 失败：{e}");
    }
    if let Err(e) = conn
        .execute(&format!("CREATE SCHEMA {PROBE_SCHEMA}"), &[])
        .await
    {
        panic!("建探测 schema 失败：{e}");
    }
    Some(conn)
}

async fn must_exec(c: &PostgresConn, stmts: &[&str]) {
    for q in stmts {
        if let Err(e) = c.execute(q, &[]).await {
            panic!("前置语句失败 {q}：{e}");
        }
    }
}

async fn scalar(c: &PostgresConn, q: &str) -> String {
    match c.execute(q, &[]).await {
        Ok(res) => first_cell(&res.rows),
        Err(e) => format!("ERR: {e}"),
    }
}

fn first_cell(rows: &[Vec<Value>]) -> String {
    match rows.first().and_then(|r| r.first()) {
        Some(Value::String(s)) => s.clone(),
        Some(Value::Null) => "<null>".to_string(),
        Some(v) => format!("{v:?}"),
        None => "<no rows>".to_string(),
    }
}

/// 在探测 schema 里按名字数对象——把「挡住 vs 静默改掉」两条分支都判得开。
async fn count_objects(c: &PostgresConn, names: &[&str]) -> String {
    let quoted: Vec<String> = names.iter().map(|n| format!("'{n}'")).collect();
    scalar(
        c,
        &format!(
            "SELECT count(*) FROM pg_class cl JOIN pg_namespace n ON n.oid = cl.relnamespace \
             WHERE n.nspname = '{PROBE_SCHEMA}' AND cl.relname IN ({})",
            quoted.join(",")
        ),
    )
    .await
}

/// P1（驱动事实 + 判据）：DDL 能否进事务并整体回滚。
/// 要钉进口径：`Script.ddl_in_tx`（现在在 ddl.rs:892 / ddl.go:851 硬编码 true，只被 SQLite 取证背书过）。
///
/// 注：Rust 侧 `TxMode::default()` 的隔离级别是 `ReadCommitted`，于是 `begin_tx` 走
/// `BEGIN ISOLATION LEVEL READ COMMITTED`；Go 侧零值 TxMode 不发 statement 走驱动默认。
/// PG 的默认隔离级别本就是 READ COMMITTED，所以这条差异不影响「整体回滚」判据，但记录在案。
#[tokio::test]
async fn p1_ddl_in_transaction_rolls_back_whole() {
    let Some(c) = probe_conn("p1").await else {
        return;
    };

    // 事务路径只能 begin_tx + execute_in_tx；裸 `BEGIN` 走 execute 会落在池的另一条连接上，
    // 把那条连接留在事务态、污染后续探测（这条是读代码定下的用法约束，不需引擎验证）。
    let mut tx = match c.begin_tx(TxMode::default()).await {
        Ok(t) => t,
        Err(e) => panic!("begin_tx：{e}"),
    };
    let ddl_err = match c
        .execute_in_tx(
            &mut tx,
            &format!("CREATE TABLE {PROBE_SCHEMA}.p1 (id bigint)"),
            &[],
        )
        .await
    {
        Ok(_) => "none".to_string(),
        Err(e) => format!("{e}"),
    };
    println!("OBS[p1] ddl_in_tx_err={ddl_err}");

    let inside = c
        .execute_in_tx(
            &mut tx,
            &format!(
                "SELECT count(*) FROM pg_class cl JOIN pg_namespace n ON n.oid = cl.relnamespace \
                 WHERE n.nspname = '{PROBE_SCHEMA}' AND cl.relname = 'p1'"
            ),
            &[],
        )
        .await;
    println!(
        "OBS[p1] visible_inside_tx={}",
        match inside {
            Ok(res) => first_cell(&res.rows),
            Err(e) => format!("ERR: {e}"),
        }
    );

    if let Err(e) = c.rollback(tx).await {
        panic!("rollback：{e}");
    }
    let after = count_objects(&c, &["p1"]).await;
    println!("OBS[p1] ddl_in_tx={}（回滚后残留={after}）", after == "0");
    assert_eq!(
        after,
        "0",
        "DDL 未随事务整体回滚（残留 {after} 个对象）⇒ PG 不能声明 ddl_in_tx=true，规则层需按方言落能力位，见 A.26 P1"
    );
}

/// P7（驱动事实）：identity / serial 的自增回读。
/// 对照 SQLite：E3.0 取证 sqlite 侧恒 false（驱动填不出），PG 侧是否不同？
/// 判据：IDENTITY（attidentity）必须被读成自增，读不出来就是驱动缺陷。
#[tokio::test]
async fn p7_auto_increment_readback() {
    let Some(c) = probe_conn("p7").await else {
        return;
    };
    must_exec(
        &c,
        &[&format!(
            "CREATE TABLE {PROBE_SCHEMA}.p7 (id bigint GENERATED BY DEFAULT AS IDENTITY, legacy serial, n bigint)"
        )],
    )
    .await;

    let cols = c.list_columns(PROBE_SCHEMA, "p7").await.unwrap();
    for col in &cols {
        println!(
            "OBS[p7] col {} type=\"{}\" nullable={} default={:?} auto={} pk={}",
            col.name,
            col.data_type,
            col.nullable,
            col.default_value,
            col.is_auto_increment,
            col.is_primary_key
        );
    }
    let auto = |name: &str| {
        cols.iter()
            .find(|c| c.name == name)
            .map(|c| c.is_auto_increment)
            .unwrap_or(false)
    };
    assert!(
        auto("id"),
        "IDENTITY 列 id 回读 is_auto_increment=false ⇒ pg 驱动读侧漏了 attidentity，见 A.26 P7"
    );
    // 观测（不赌）：serial 走 nextval 默认值而非 attidentity，sqlx 这头认不认它。
    println!(
        "OBS[p7] identity_auto={} serial_auto={}",
        auto("id"),
        auto("legacy")
    );
}

/// P8（驱动事实）：唯一约束/主键的自动索引名字形状。
/// 对照 SQLite 的 `sqlite_autoindex_*`。diff 拿索引名当身份键，名字为空就点不中。
#[tokio::test]
async fn p8_constraint_index_names() {
    let Some(c) = probe_conn("p8").await else {
        return;
    };
    must_exec(
        &c,
        &[&format!(
            "CREATE TABLE {PROBE_SCHEMA}.p8 (id bigint PRIMARY KEY, code text NOT NULL UNIQUE)"
        )],
    )
    .await;

    let idx = c.list_indexes(PROBE_SCHEMA, "p8").await.unwrap();
    for ix in &idx {
        let cols: Vec<&str> = ix.columns.iter().map(|x| x.name.as_str()).collect();
        println!(
            "OBS[p8] index name=\"{}\" unique={} primary={} cols={:?}",
            ix.name, ix.unique, ix.primary, cols
        );
    }
    assert_eq!(
        idx.len(),
        2,
        "主键 + 唯一约束应回读到 2 条索引，实得 {} ⇒ pg 索引读侧口径有问题，见 A.26 P8",
        idx.len()
    );
    let mut names: Vec<&str> = idx.iter().map(|x| x.name.as_str()).collect();
    names.sort_unstable();
    for n in &names {
        assert!(
            !n.is_empty(),
            "回读到无名索引，diff 的身份键会失效，见 A.26 P8"
        );
    }
    let counted = names.len();
    names.dedup();
    assert_eq!(
        names.len(),
        counted,
        "索引名重复 ⇒ diff 的身份键会撞车，见 A.26 P8"
    );
    println!(
        "OBS[p8] pg_constraint_conname={}",
        scalar(
            &c,
            &format!(
                "SELECT coalesce(string_agg(conname::text, ',' order by conname),'<none>') FROM pg_constraint \
                 WHERE conrelid='{PROBE_SCHEMA}.p8'::regclass"
            )
        )
        .await
    );
}

/// P9（驱动事实）：外键 on_update / on_delete 回读（docs/plan-compare-sync.md §8-7 的债）。
/// 两头都要探：引擎目录给不给得出、我们驱动读不读——缺口在哪一头，处置完全不同。
#[tokio::test]
async fn p9_foreign_key_actions_readback() {
    let Some(c) = probe_conn("p9").await else {
        return;
    };
    must_exec(
        &c,
        &[
            &format!("CREATE TABLE {PROBE_SCHEMA}.p9_parent (id bigint PRIMARY KEY)"),
            &format!(
                "CREATE TABLE {PROBE_SCHEMA}.p9_child (id bigint PRIMARY KEY, parent_id bigint \
                 REFERENCES {PROBE_SCHEMA}.p9_parent(id) ON UPDATE CASCADE ON DELETE SET NULL)"
            ),
        ],
    )
    .await;

    let fks = c.list_foreign_keys(PROBE_SCHEMA, "p9_child").await.unwrap();
    for fk in &fks {
        println!(
            "OBS[p9] fk name=\"{}\" cols={:?} ref={}.{} refcols={:?} on_update={:?} on_delete={:?}",
            fk.name,
            fk.columns,
            fk.referenced_schema,
            fk.referenced_table,
            fk.referenced_columns,
            fk.on_update,
            fk.on_delete
        );
    }
    assert_eq!(
        fks.len(),
        1,
        "应回读到 1 条外键，实得 {} ⇒ pg 外键读侧有问题，见 A.26 P9",
        fks.len()
    );
    assert_eq!(fks[0].referenced_table, "p9_parent");
    assert_eq!(fks[0].columns.len(), 1, "外键列回读形状不符，见 A.26 P9");

    println!(
        "OBS[p9] pg_action_codes(confupdtype/confdeltype)={} driver_on_update={:?} driver_on_delete={:?}",
        scalar(
            &c,
            &format!(
                "SELECT confupdtype::text||'/'||confdeltype::text FROM pg_constraint \
                 WHERE conrelid='{PROBE_SCHEMA}.p9_child'::regclass AND contype='f'"
            )
        )
        .await,
        fks[0].on_update,
        fks[0].on_delete
    );
}

/// P10（驱动事实）：format_type 的文本形状——§8-3「generic_type 归一命名表」的输入。
/// 本条不猜映射，只把 sqlx 实际交给规则层的原始文本钉下来。
#[tokio::test]
async fn p10_format_type_shapes() {
    let Some(c) = probe_conn("p10").await else {
        return;
    };
    must_exec(
        &c,
        &[&format!(
            "CREATE TABLE {PROBE_SCHEMA}.p10 (a bigint, b integer, c text, d varchar(20), e char(3), \
             f numeric(10,2), g boolean, h timestamptz, i date, j jsonb, k bytea, l text[], m uuid, n inet)"
        )],
    )
    .await;

    let cols = c.list_columns(PROBE_SCHEMA, "p10").await.unwrap();
    for col in &cols {
        println!(
            "OBS[p10] col {} type=\"{}\" max_length={:?} precision={:?} scale={:?}",
            col.name, col.data_type, col.max_length, col.precision, col.scale
        );
    }
    assert_eq!(
        cols.len(),
        14,
        "应回读到 14 列，实得 {}，见 A.26 P10",
        cols.len()
    );
    for (i, col) in cols.iter().enumerate() {
        assert!(
            !col.data_type.is_empty(),
            "列 {} 的 data_type 为空，见 A.26 P10",
            col.name
        );
        assert_eq!(
            col.ordinal_position as usize,
            i + 1,
            "列 {} 的 ordinal_position={} 与回读序不符，见 A.26 P10",
            col.name,
            col.ordinal_position
        );
    }
}

/// P11（驱动事实）：list_tables 是否真的尊重 schema 入参。
/// 对照 SQLite：驱动忽略入参（A.9 F6 / §8-6）。两头都成立才算「尊重」＝ §8-6 v1.1 可行。
#[tokio::test]
async fn p11_list_tables_respects_schema() {
    let Some(c) = probe_conn("p11").await else {
        return;
    };
    must_exec(
        &c,
        &[
            &format!("CREATE TABLE {PROBE_SCHEMA}.p11_marker (id bigint)"),
            &format!(
                "CREATE VIEW {PROBE_SCHEMA}.p11_marker_v AS SELECT id FROM {PROBE_SCHEMA}.p11_marker"
            ),
        ],
    )
    .await;

    let in_probe = c.list_tables(PROBE_SCHEMA).await.unwrap();
    let in_public = c.list_tables("public").await.unwrap();
    for t in &in_probe {
        println!(
            "OBS[p11] probe_schema entry name=\"{}\" type={:?}",
            t.name, t.table_type
        );
    }
    println!(
        "OBS[p11] public_entries={} probe_entries={}",
        in_public.len(),
        in_probe.len()
    );

    let has = |list: &[polydb_core::TableInfo], name: &str| list.iter().any(|t| t.name == name);
    assert!(
        has(&in_probe, "p11_marker"),
        "list_tables({PROBE_SCHEMA}) 未返回 p11_marker ⇒ 驱动没按 schema 过滤，见 A.26 P11"
    );
    assert!(
        !has(&in_public, "p11_marker"),
        "list_tables(public) 混进了 {PROBE_SCHEMA} 的对象 ⇒ 跨 schema 泄漏，§8-6 同库多 schema 不可行，见 A.26 P11"
    );
}
