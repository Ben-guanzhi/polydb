//! F0.4 MySQL 方言取证（Rust 侧）——`rust/crates/db-mysql/` 的第一份活 MySQL 测试。
//!
//! 与 Go 侧 `go/pkg/dbmysql/probe_test.go`（F0.4，13 点）分档：
//! 这里**只镜像驱动事实**（sqlx 把系统目录读成了什么）＝ M7、M8、M9、M10、M11、M12、M13；
//! 引擎事实（M1–M6，MySQL 服务器自己的脾气）与客户端语言无关，Go 侧探一次就够。
//!
//! 观测以 `OBS[mN]` 前缀打印（`cargo test -p polydb-db-mysql -- --nocapture` 才看得见），
//! 断言只打在我们自身层的形状不变式与该点要钉的判据上；结论回填 docs/plan-compare-sync.md A.27。
//!
//! 软跳过口径：`MySqlConn::open` 是**懒连接**，建不出池不代表连不上，
//! 所以真探是 `ping()`；打不开就 eprintln 一条 SKIP 后直接 return——
//! 与 Go 侧 `connected:false` 同一语义，缺引擎时全绿而非缺测。

use polydb_core::{DatabaseKind, Value};
use polydb_db_core::{DatabaseDriver, SqlDriver};
use polydb_db_mysql::MySqlConn;

const PROBE_DB: &str = "polydb_probe";
const PROBE_DB_ALT: &str = "polydb_probe_b";
const SYS_SCHEMA: &str = "information_schema";

/// 与 Go 侧、test/contract 同一默认串：要么三处都能连，要么三处都静默跳过。
const DEFAULT_DSN: &str = "mysql://polydb_probe@127.0.0.1:3306/polydb_probe";

fn dsn() -> String {
    std::env::var("POLYDB_TEST_MYSQL").unwrap_or_else(|_| DEFAULT_DSN.to_string())
}

/// 取一个干净的探测现场；连不上即软跳过（返回 None，调用方直接 return）。
async fn probe_conn(label: &str) -> Option<MySqlConn> {
    let url = dsn();
    let conn = match MySqlConn::open(&url) {
        Ok(c) => c,
        Err(e) => {
            eprintln!("SKIP[{label}] connected:false 打不开 MySQL（POLYDB_TEST_MYSQL={url}）：{e}");
            return None;
        }
    };
    if let Err(e) = conn.ping().await {
        eprintln!("SKIP[{label}] connected:false 无可用 MySQL（POLYDB_TEST_MYSQL={url}）：{e}");
        return None;
    }
    // 前置条件：两个预建探测库在不在权限范围内。
    // 探测账号刻意只拿到这两库、不给 CREATE DATABASE，所以缺库要现读出来。
    let visible = scalar(&conn, &format!(
        "SELECT COUNT(*) FROM {SYS_SCHEMA}.SCHEMATA WHERE SCHEMA_NAME IN ('{PROBE_DB}','{PROBE_DB_ALT}')"
    )).await;
    if visible != "2" {
        eprintln!(
            "SKIP[{label}] connected:false 探测库不齐（需要 {PROBE_DB} 与 {PROBE_DB_ALT} 两库且账号可见，当前读到 {visible}；POLYDB_TEST_MYSQL={url}）"
        );
        return None;
    }
    println!(
        "OBS[server] VERSION()={} sql_mode={}",
        scalar(&conn, "SELECT VERSION()").await,
        scalar(&conn, "SELECT @@sql_mode").await
    );
    Some(conn)
}

async fn must_exec(c: &MySqlConn, stmts: &[&str]) {
    for q in stmts {
        if let Err(e) = c.execute(q, &[]).await {
            panic!("前置语句失败 {q}：{e}");
        }
    }
}

async fn scalar(c: &MySqlConn, q: &str) -> String {
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

fn col_line(col: &polydb_core::ColumnInfo) -> String {
    format!(
        "{} | data_type={} | generic={:?} | nullable={} | default={:?} | maxLen={:?} prec={:?} scale={:?} | pk={} ai={} | comment={:?} | pos={}",
        col.name,
        col.data_type,
        col.generic_type,
        col.nullable,
        col.default_value,
        col.max_length,
        col.precision,
        col.scale,
        col.is_primary_key,
        col.is_auto_increment,
        col.comment,
        col.ordinal_position
    )
}

fn index_line(idx: &polydb_core::IndexInfo) -> String {
    let cols: Vec<String> = idx
        .columns
        .iter()
        .map(|c| format!("{}#{}", c.name, c.position))
        .collect();
    format!(
        "{} | unique={} primary={} type={:?} cols=[{}]",
        idx.name,
        idx.unique,
        idx.primary,
        idx.index_type,
        cols.join(",")
    )
}

fn fk_line(fk: &polydb_core::ForeignKeyInfo) -> String {
    format!(
        "{} | cols={:?} ref={}.{}[{:?}] on_update={:?} on_delete={:?}",
        fk.name,
        fk.columns,
        fk.referenced_schema,
        fk.referenced_table,
        fk.referenced_columns,
        fk.on_update,
        fk.on_delete
    )
}

/// M7（驱动事实）：identity / serial 的自增回读。
/// 对照 SQLite：E3.0 取证 sqlite 侧恒 false（驱动填不出），MySQL 侧是否不同？
/// 判据：AUTO_INCREMENT / SERIAL 必须被读成自增，读不出来就是驱动缺陷。
#[tokio::test]
async fn m7_auto_increment_readback() {
    let Some(c) = probe_conn("m7").await else {
        return;
    };
    must_exec(
        &c,
        &[
            "DROP TABLE IF EXISTS m7_a",
            "DROP TABLE IF EXISTS m7_b",
            "CREATE TABLE m7_a (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, v INT)",
            "CREATE TABLE m7_b (id SERIAL PRIMARY KEY, v INT)",
        ],
    )
    .await;

    for name in ["m7_a", "m7_b"] {
        let cols = c.list_columns(PROBE_DB, name).await.unwrap();
        let id = cols.iter().find(|c| c.name == "id").unwrap();
        println!("OBS[m7.{name}] driver readback: {}", col_line(id));

        let extra = scalar(
            &c,
            &format!(
                "SELECT EXTRA FROM {SYS_SCHEMA}.COLUMNS WHERE table_schema='{PROBE_DB}' AND table_name='{name}' AND column_name='id'"
            ),
        )
        .await;
        if !extra.to_lowercase().contains("auto_increment") {
            panic!("M7 判据红：目录里 EXTRA={extra:?} 不含 auto_increment，但本列是按 AUTO_INCREMENT/SERIAL 建的");
        }
        assert!(
            id.is_auto_increment,
            "M7 判据红：目录 EXTRA={extra:?} 而驱动 is_auto_increment=false ⇒ 读侧漏填，重建会当普通列"
        );
    }
    println!(
        "OBS[m7.serial_shape] SERIAL 的 COLUMN_TYPE 原文={}",
        scalar(
            &c,
            &format!(
                "SELECT COLUMN_TYPE FROM {SYS_SCHEMA}.COLUMNS WHERE table_schema='{PROBE_DB}' AND table_name='m7_b' AND column_name='id'"
            ),
        )
        .await
    );
    must_exec(
        &c,
        &["DROP TABLE IF EXISTS m7_a", "DROP TABLE IF EXISTS m7_b"],
    )
    .await;
}

/// M8（驱动事实）：索引名的形状——跨表撞车。
/// MySQL 的主键索引名**恒为 'PRIMARY'** ⇒ 同名跨表必然撞；身份键要是不带表限定就是错的。
#[tokio::test]
async fn m8_index_name_collision_across_tables() {
    let Some(c) = probe_conn("m8").await else {
        return;
    };
    must_exec(
        &c,
        &[
            "DROP TABLE IF EXISTS m8_a",
            "DROP TABLE IF EXISTS m8_b",
            "CREATE TABLE m8_a (id INT PRIMARY KEY, x INT, UNIQUE (x))",
            "CREATE TABLE m8_b (id INT PRIMARY KEY, x INT, UNIQUE (x))",
        ],
    )
    .await;

    let mut shared = Vec::new();
    let mut per_table: std::collections::HashMap<&str, Vec<_>> = std::collections::HashMap::new();
    for name in ["m8_a", "m8_b"] {
        let idx = c.list_indexes(PROBE_DB, name).await.unwrap();
        per_table.insert(name, idx.clone());
        println!("OBS[m8.{name}] indexes count={}", idx.len());
        for ix in &idx {
            println!("  {}", index_line(ix));
        }
    }
    for one in &per_table["m8_a"] {
        for other in &per_table["m8_b"] {
            if one.name == other.name {
                shared.push(one.name.clone());
            }
        }
    }
    println!("OBS[m8.cross_table_collision] shared index names={shared:?}");

    let primary_count = per_table["m8_a"].iter().filter(|ix| ix.primary).count();
    for ix in &per_table["m8_a"] {
        if ix.primary {
            assert_eq!(
                ix.name, "PRIMARY",
                "M8 判据红：Primary=true 的索引名字不是 'PRIMARY'（实际 {:?}）⇒ 驱动里 name=='PRIMARY' 那条硬判断要改",
                ix.name
            );
        }
    }
    assert_eq!(
        primary_count, 1,
        "M8 形状不变式红：一张表读回 {primary_count} 个 Primary 索引（应为 1）"
    );
    must_exec(
        &c,
        &["DROP TABLE IF EXISTS m8_a", "DROP TABLE IF EXISTS m8_b"],
    )
    .await;
}

/// M9（驱动事实）：外键动作回读。
/// 判据：目录给不给得出（给不出才要慌），驱动回读只记。
#[tokio::test]
async fn m9_foreign_key_actions_readback() {
    let Some(c) = probe_conn("m9").await else {
        return;
    };
    must_exec(
        &c,
        &[
            "DROP TABLE IF EXISTS m9_c",
            "DROP TABLE IF EXISTS m9_p",
            "CREATE TABLE m9_p (id INT PRIMARY KEY)",
            "CREATE TABLE m9_c (id INT PRIMARY KEY, p INT, CONSTRAINT m9_fk FOREIGN KEY (p) REFERENCES m9_p(id) ON UPDATE CASCADE ON DELETE SET NULL)",
        ],
    )
    .await;

    let catalog = scalar(
        &c,
        &format!(
            "SELECT CONCAT(UPDATE_RULE,'/',DELETE_RULE) FROM {SYS_SCHEMA}.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA='{PROBE_DB}' AND CONSTRAINT_NAME='m9_fk'"
        ),
    )
    .await;
    if catalog.starts_with("ERR") || catalog == "<no rows>" {
        panic!("M9 判据红：REFERENTIAL_CONSTRAINTS 查不到这条外键（{catalog}）⇒ on_update/on_delete 不是读侧的锅");
    }
    println!("OBS[m9.catalog] UPDATE_RULE/DELETE_RULE={catalog}");

    let fks = c.list_foreign_keys(PROBE_DB, "m9_c").await.unwrap();
    assert_eq!(
        fks.len(),
        1,
        "M9 形状不变式红：驱动读回 {} 条外键（应为 1）",
        fks.len()
    );
    println!("OBS[m9.driver] {}", fk_line(&fks[0]));
    if fks[0].on_update.is_some() || fks[0].on_delete.is_some() {
        println!("OBS[m9.driver.note] 驱动已填动作 ⇒ §8-7 的读侧缺口在本版本已补，A.27 的记录要翻");
    }
    must_exec(
        &c,
        &["DROP TABLE IF EXISTS m9_c", "DROP TABLE IF EXISTS m9_p"],
    )
    .await;
}

/// M10（驱动事实）：COLUMN_TYPE 的形状 + precision/scale 空槽。
/// 判据：DataType 非空、OrdinalPosition 正确、三个尺寸槽位是否空。
#[tokio::test]
async fn m10_column_type_shapes() {
    let Some(c) = probe_conn("m10").await else {
        return;
    };
    must_exec(
        &c,
        &[
            "DROP TABLE IF EXISTS m10_t",
            "CREATE TABLE m10_t (
                id BIGINT PRIMARY KEY, b BIGINT UNSIGNED, sm SMALLINT, ti TINYINT,
                de DECIMAL(10,2), fl FLOAT, do DOUBLE,
                ch CHAR(8), vc VARCHAR(255), tx TEXT, bl BLOB,
                dt DATE, tm TIME, ts DATETIME, js JSON, en ENUM('a','b'),
                gen INT AS (id * 2) STORED
            )",
        ],
    )
    .await;

    let cols = c.list_columns(PROBE_DB, "m10_t").await.unwrap();
    const WANT_COLS: usize = 17;
    assert_eq!(
        cols.len(),
        WANT_COLS,
        "M10 形状不变式红：建了 {WANT_COLS} 列，驱动读回 {} 列",
        cols.len()
    );
    let mut empty_slots = 0;
    for (i, col) in cols.iter().enumerate() {
        assert!(
            !col.data_type.is_empty(),
            "M10 形状不变式红：第 {} 列 {} 的 data_type 是空串",
            i + 1,
            col.name
        );
        assert_eq!(
            col.ordinal_position as usize,
            i + 1,
            "M10 形状不变式红：{} 的 ordinal_position={} 与列序 {} 不符",
            col.name,
            col.ordinal_position,
            i + 1
        );
        if col.max_length.is_none() && col.precision.is_none() && col.scale.is_none() {
            empty_slots += 1;
        }
        println!("OBS[m10.{}] {}", col.name, col_line(col));
    }
    println!(
        "OBS[m10.empty_slots] 三个尺寸槽位全空的列数={empty_slots}/{WANT_COLS}（收窄判断没有数值依据，只能解析 COLUMN_TYPE 原文）"
    );
    println!(
        "OBS[m10.gen_column] 生成列 gen 的目录 EXTRA={:?}",
        scalar(
            &c,
            &format!(
                "SELECT EXTRA FROM {SYS_SCHEMA}.COLUMNS WHERE table_schema='{PROBE_DB}' AND table_name='m10_t' AND column_name='gen'"
            ),
        )
        .await
    );
    println!(
        "OBS[m10.enum_raw] 枚举列原文={:?}",
        scalar(
            &c,
            &format!(
                "SELECT COLUMN_TYPE FROM {SYS_SCHEMA}.COLUMNS WHERE table_schema='{PROBE_DB}' AND table_name='m10_t' AND column_name='en'"
            ),
        )
        .await
    );
    println!(
        "OBS[m10.no_collation_slot] 字符列的 COLLATION_NAME={:?}",
        scalar(
            &c,
            &format!(
                "SELECT COLLATION_NAME FROM {SYS_SCHEMA}.COLUMNS WHERE table_schema='{PROBE_DB}' AND table_name='m10_t' AND column_name='vc'"
            ),
        )
        .await
    );
    must_exec(&c, &["DROP TABLE IF EXISTS m10_t"]).await;
}

/// M11（驱动事实）：list_tables 是否真的尊重 schema 入参。
/// 两头都成立才算「尊重」＝ §8-6 v1.1 可行。
#[tokio::test]
async fn m11_list_tables_respects_schema() {
    let Some(c) = probe_conn("m11").await else {
        return;
    };
    must_exec(
        &c,
        &[
            &format!("DROP TABLE IF EXISTS {PROBE_DB}.m11_in_probe"),
            &format!("DROP TABLE IF EXISTS {PROBE_DB_ALT}.m11_in_probe_b"),
            &format!("CREATE TABLE {PROBE_DB}.m11_in_probe (id INT PRIMARY KEY)"),
            &format!("CREATE TABLE {PROBE_DB_ALT}.m11_in_probe_b (id INT PRIMARY KEY)"),
        ],
    )
    .await;

    let in_a = c.list_tables(PROBE_DB).await.unwrap();
    let in_b = c.list_tables(PROBE_DB_ALT).await.unwrap();
    println!(
        "OBS[m11.counts] list_tables({PROBE_DB}) returned {} items, list_tables({PROBE_DB_ALT}) returned {} items",
        in_a.len(),
        in_b.len()
    );

    let has = |list: &[polydb_core::TableInfo], name: &str| list.iter().any(|t| t.name == name);
    assert!(
        has(&in_a, "m11_in_probe"),
        "M11 两头判据红：库 {PROBE_DB} 里建的表没在自己库的 list_tables 里出现"
    );
    assert!(
        !has(&in_a, "m11_in_probe_b"),
        "M11 两头判据红：{PROBE_DB_ALT} 的标记表串进了 {PROBE_DB} 的结果 ⇒ schema 入参没生效"
    );
    assert!(
        has(&in_b, "m11_in_probe_b"),
        "M11 两头判据红：库 {PROBE_DB_ALT} 里建的表没在自己库的 list_tables 里出现"
    );
    assert!(
        !has(&in_b, "m11_in_probe"),
        "M11 两头判据红：{PROBE_DB} 的标记表串进了 {PROBE_DB_ALT} 的结果 ⇒ schema 入参没生效"
    );

    let schemas = c.list_schemas().await.unwrap();
    let has_name = |name: &str| schemas.iter().any(|s| s.name == name);
    println!(
        "OBS[m11.schemas] list_schemas returned {} databases, contains probe DBs={}/{}",
        schemas.len(),
        has_name(PROBE_DB),
        has_name(PROBE_DB_ALT)
    );
}

/// M12（驱动事实）：驱动读法漂移——三处。
#[tokio::test]
async fn m12_driver_read_drifts() {
    let Some(c) = probe_conn("m12").await else {
        return;
    };
    must_exec(
        &c,
        &[
            "DROP TABLE IF EXISTS m12_t",
            "CREATE TABLE m12_t (id INT PRIMARY KEY, x INT, KEY ix_x (x))",
            "INSERT INTO m12_t (id, x) VALUES (1, 1)",
        ],
    )
    .await;

    // 1. ListSchemas——Go 滤掉四个系统库，Rust 一条 WHERE 都没有
    let raw = scalar(&c, &format!("SELECT COUNT(*) FROM {SYS_SCHEMA}.SCHEMATA")).await;
    let schemas = c.list_schemas().await.unwrap();
    let filtered = schemas
        .iter()
        .filter(|s| {
            s.name == "mysql"
                || s.name == SYS_SCHEMA
                || s.name == "performance_schema"
                || s.name == "sys"
        })
        .count();
    println!(
        "OBS[m12.schemas] catalog count={raw}, driver returned={}, system schemas present={filtered}",
        schemas.len(),
    );

    // 2. ListIndexes——Go 的 SQL 不查 INDEX_TYPE，Rust 查了并映射
    let idx = c.list_indexes(PROBE_DB, "m12_t").await.unwrap();
    let raw_index_types = scalar(
        &c,
        &format!(
            "SELECT GROUP_CONCAT(DISTINCT INDEX_TYPE) FROM {SYS_SCHEMA}.STATISTICS WHERE table_schema='{PROBE_DB}' AND table_name='m12_t'"
        ),
    )
    .await;
    let empty_type = idx.iter().filter(|ix| ix.index_type.is_none()).count();
    for ix in &idx {
        println!("OBS[m12.index] {}", index_line(ix));
    }
    println!(
        "OBS[m12.index_type] catalog INDEX_TYPE={raw_index_types:?}, driver empty type count={empty_type}/{}/len",
        idx.len()
    );

    // 3. CreateTableSQL——Rust 用 SHOW CREATE TABLE ?? 参数绑定
    let ddl = c.create_table_sql(PROBE_DB, "m12_t").await.unwrap();
    assert!(
        ddl.to_uppercase().contains("CREATE TABLE"),
        "M12 形状不变式红：create_table_sql 回的不是 DDL 文本：{ddl:?}"
    );
    let oneline = ddl.split_whitespace().collect::<Vec<_>>().join(" ");
    println!(
        "OBS[m12.create_table_sql] length={} content={oneline}",
        oneline.len()
    );

    must_exec(&c, &["DROP TABLE IF EXISTS m12_t"]).await;
}

/// M13（驱动事实）：依赖目录有无 + 视图定义文本。
#[tokio::test]
async fn m13_dependency_catalog() {
    let Some(c) = probe_conn("m13").await else {
        return;
    };
    must_exec(
        &c,
        &[
            "DROP VIEW IF EXISTS m13_v",
            "DROP TABLE IF EXISTS m13_t",
            "CREATE TABLE m13_t (id INT PRIMARY KEY, qty INT)",
            "CREATE VIEW m13_v AS SELECT id, qty FROM m13_t",
        ],
    )
    .await;

    let cat = scalar(
        &c,
        &format!(
            "SELECT COUNT(*) FROM {SYS_SCHEMA}.TABLES WHERE table_schema='{SYS_SCHEMA}' AND table_name IN ('VIEW_TABLE_USAGE','DEPENDENCIES','OBJECT_DEPENDENCIES','DEPENDS_ON')"
        ),
    )
    .await;
    println!("OBS[m13.dependency_catalog_tables] count={cat}");
    assert_eq!(
        cat,
        "0",
        "M13 判据红：MySQL {SYS_SCHEMA} 里查到了 {cat} 张疑似依赖目录表 ⇒ §8-8 的\"B 档在 MySQL 上判死\"要翻"
    );

    let def = scalar(
        &c,
        &format!(
            "SELECT VIEW_DEFINITION FROM {SYS_SCHEMA}.VIEWS WHERE table_schema='{PROBE_DB}' AND table_name='m13_v'"
        ),
    )
    .await;
    println!("OBS[m13.view_definition] {def:?}");
    println!(
        "OBS[m13.show_create_view] {}",
        !scalar(&c, "SHOW CREATE VIEW m13_v")
            .await
            .starts_with("ERR")
    );

    let mv = scalar(
        &c,
        &format!(
            "SELECT COUNT(*) FROM {SYS_SCHEMA}.TABLES WHERE table_schema='{PROBE_DB}' AND table_type='MATERIALIZED VIEW'"
        ),
    )
    .await;
    println!("OBS[m13.materialized] count={mv}");
    println!(
        "OBS[m13.table_type_domain] {}",
        scalar(
            &c,
            &format!(
                "SELECT GROUP_CONCAT(DISTINCT TABLE_TYPE) FROM {SYS_SCHEMA}.TABLES WHERE table_schema='{PROBE_DB}'"
            ),
        )
        .await
    );

    must_exec(
        &c,
        &["DROP VIEW IF EXISTS m13_v", "DROP TABLE IF EXISTS m13_t"],
    )
    .await;
}

#[test]
fn strip_leading_comments_test() {
    use polydb_db_mysql::strip_leading_comments;

    assert_eq!(strip_leading_comments("SELECT 1"), "SELECT 1");
    assert_eq!(strip_leading_comments("-- comment\nSELECT 1"), "SELECT 1");
    assert_eq!(strip_leading_comments("/* comment */SELECT 1"), "SELECT 1");
    assert_eq!(strip_leading_comments("-- c1\n-- c2\nSELECT 1"), "SELECT 1");
    assert_eq!(strip_leading_comments("-- no newline"), "");
    assert_eq!(strip_leading_comments("/* unclosed"), "");
    assert_eq!(strip_leading_comments("   SELECT 1"), "SELECT 1");
    assert_eq!(strip_leading_comments(""), "");
}

#[test]
fn detect_statement_type_test() {
    use polydb_core::StatementType;

    assert_eq!(
        MySqlConn::detect_statement_type("SELECT 1"),
        StatementType::Select
    );
    assert_eq!(
        MySqlConn::detect_statement_type("WITH x AS (SELECT 1) SELECT * FROM x"),
        StatementType::Select
    );
    assert_eq!(
        MySqlConn::detect_statement_type("INSERT INTO t VALUES (1)"),
        StatementType::Insert
    );
    assert_eq!(
        MySqlConn::detect_statement_type("UPDATE t SET x=1"),
        StatementType::Update
    );
    assert_eq!(
        MySqlConn::detect_statement_type("DELETE FROM t"),
        StatementType::Delete
    );
    assert_eq!(
        MySqlConn::detect_statement_type("CREATE TABLE t (id INT)"),
        StatementType::Ddl
    );
    assert_eq!(
        MySqlConn::detect_statement_type("ALTER TABLE t ADD COLUMN x INT"),
        StatementType::Ddl
    );
    assert_eq!(
        MySqlConn::detect_statement_type("DROP TABLE t"),
        StatementType::Ddl
    );
    assert_eq!(
        MySqlConn::detect_statement_type("SHOW TABLES"),
        StatementType::Other
    );
    assert_eq!(
        MySqlConn::detect_statement_type("-- comment\nSELECT 1"),
        StatementType::Select
    );
    assert_eq!(
        MySqlConn::detect_statement_type("/* comment */SELECT 1"),
        StatementType::Select
    );
    assert_eq!(
        MySqlConn::detect_statement_type("  select 1"),
        StatementType::Select
    );
}

/// 测试 begin_statement 的隔离级别映射
#[test]
fn begin_statement_isolation_levels() {
    use polydb_core::IsolationLevel;
    use polydb_db_core::TxMode;

    assert_eq!(
        MySqlConn::begin_statement(TxMode {
            isolation_level: IsolationLevel::ReadCommitted
        }),
        Some("BEGIN ISOLATION LEVEL READ COMMITTED".to_string())
    );
    assert_eq!(
        MySqlConn::begin_statement(TxMode {
            isolation_level: IsolationLevel::Serializable
        }),
        Some("BEGIN ISOLATION LEVEL SERIALIZABLE".to_string())
    );
    assert_eq!(
        MySqlConn::begin_statement(TxMode {
            isolation_level: IsolationLevel::ReadUncommitted
        }),
        Some("BEGIN ISOLATION LEVEL READ UNCOMMITTED".to_string())
    );
    assert_eq!(
        MySqlConn::begin_statement(TxMode {
            isolation_level: IsolationLevel::RepeatableRead
        }),
        Some("BEGIN ISOLATION LEVEL REPEATABLE READ".to_string())
    );
}

/// 测试 bind_value 的值映射（不需要数据库连接）
#[test]
fn bind_value_mapping() {
    use polydb_core::Value;

    // 测试各种 Value 类型都能被正确绑定
    let _q = MySqlConn::bind_value(sqlx::query::<sqlx::MySql>("SELECT ?"), &Value::Null);
    let _q = MySqlConn::bind_value(sqlx::query::<sqlx::MySql>("SELECT ?"), &Value::Bool(true));
    let _q = MySqlConn::bind_value(sqlx::query::<sqlx::MySql>("SELECT ?"), &Value::Integer(42));
    let _q = MySqlConn::bind_value(sqlx::query::<sqlx::MySql>("SELECT ?"), &Value::Float(3.14));
    let _q = MySqlConn::bind_value(
        sqlx::query::<sqlx::MySql>("SELECT ?"),
        &Value::String("hello".to_string()),
    );
}

/// 测试 DatabaseKind 返回 MySql
#[tokio::test]
async fn mysql_conn_kind() {
    let dsn = "mysql://fake@localhost:3306/fake";
    let conn = MySqlConn::open(dsn).expect("lazy open should succeed");
    assert_eq!(conn.kind(), DatabaseKind::MySql);
}
