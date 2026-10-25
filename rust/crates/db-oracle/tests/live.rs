//! Oracle 真库冒烟：浏览占位符（oracle crate 要求 :{n}）、NULL 绑定、
//! SET TRANSACTION 隔离声明。环境变量 POLYDB_LIVE_ORACLE 未设置时整体跳过；
//! 取值逗号分隔：`host,port,service,user,password`
//! （如 `127.0.0.1,51521,FREEPDB1,system,dbcore`）。
//! 容器复现（工作台 §6.1）：dbcore-oracle 映射 127.0.0.1:51521。
//! 两个用例各用一张独立表，可并行运行互不干扰。

use polydb_core::{FilterCondition, FilterOperator as Op, IsolationLevel, TableRowsRequest, Value};
use polydb_db_core::{SqlDriver, TxMode};
use polydb_db_oracle::OracleConn;

fn skip_unless_enabled() -> bool {
    match std::env::var("POLYDB_LIVE_ORACLE") {
        Ok(_) => true,
        Err(_) => {
            eprintln!("skip: POLYDB_LIVE_ORACLE not set");
            false
        }
    }
}

fn target() -> (String, u16, String, String, String) {
    let raw = std::env::var("POLYDB_LIVE_ORACLE").expect("POLYDB_LIVE_ORACLE not set");
    let p: Vec<&str> = raw.split(',').collect();
    assert!(
        p.len() == 5,
        "POLYDB_LIVE_ORACLE 形如 host,port,service,user,password"
    );
    Some((
        p[0].to_string(),
        p[1].parse().expect("port"),
        p[2].to_string(),
        p[3].to_string(),
        p[4].to_string(),
    ))
    .expect("env set (gated at test entry)")
}

fn table(suffix: &str) -> String {
    format!("POLYDB_LIVE_{}", suffix.to_uppercase())
}

async fn conn() -> OracleConn {
    let (host, port, service, user, pass) = target();
    OracleConn::open(&host, port, &service, &user, &pass).expect("connect oracle")
}

async fn conn_and_table(suffix: &str) -> OracleConn {
    let c = conn().await;
    let table = table(suffix);
    let _ = c.execute(&format!("DROP TABLE {table} PURGE"), &[]).await;
    c.execute(
        &format!("CREATE TABLE {table}(id NUMBER PRIMARY KEY, name VARCHAR2(50), score NUMBER)"),
        &[],
    )
    .await
    .expect("create table");
    c.execute(&format!("INSERT INTO {table} VALUES (1, 'alpha', 10)"), &[])
        .await
        .expect("insert 1");
    c.execute(&format!("INSERT INTO {table} VALUES (2, 'beta', NULL)"), &[])
        .await
        .expect("insert 2");
    c.execute(&format!("INSERT INTO {table} VALUES (3, 'gamma', 30)"), &[])
        .await
        .expect("insert 3");
    // 建表/插数有自己的隐式事务，提交后测试的 DML 才从干净状态开始
    let mut tx = c
        .begin_tx(TxMode {
            isolation_level: IsolationLevel::ReadCommitted,
        })
        .expect("setup commit");
    c.commit(&mut tx).expect("setup commit");
    c
}

async fn cleanup(c: &OracleConn, suffix: &str) {
    let table = table(suffix);
    let _ = c.execute(&format!("DROP TABLE {table} PURGE"), &[]).await;
}

fn req(conds: Vec<FilterCondition>) -> TableRowsRequest {
    TableRowsRequest {
        columns: vec!["ID".to_string()],
        conditions: conds,
        logic: None,
        order_by: Vec::new(),
        offset: 0,
        limit: 10,
    }
}

fn eq(col: &str, v: Value) -> FilterCondition {
    FilterCondition {
        column: col.to_string(),
        op: Op::Eq,
        value: v,
        second_value: Value::Null,
        values: vec![],
        case_sensitive: None,
    }
}

/// 浏览过滤条件一路走 :{n} 占位符：oracle crate 不认 `?`，任何一处漏改写
/// 都会整条报错——本用例即占位符修复的端到端证据。
#[test]
fn live_browse_filters_placeholders_and_count() {
    if !skip_unless_enabled() {
        return;
    }
    polydb_rt::block_on(async {
        let c = conn_and_table("browse").await;
        let table = table("browse");

        // Eq：:1 + 字符串绑定
        let r = c
            .browse_rows(
                "SYSTEM",
                &table,
                &req(vec![eq("NAME", Value::String("beta".into()))]),
            )
            .await
            .expect("browse eq");
        assert_eq!(r.rows, vec![vec![Value::Integer(2)]]);

        // In：:1,:2 连续编号
        let mut ic = eq("ID", Value::Null);
        ic.op = Op::In;
        ic.values = vec![Value::Integer(1), Value::Integer(3)];
        let r = c
            .browse_rows("SYSTEM", &table, &req(vec![ic]))
            .await
            .expect("browse in");
        assert_eq!(r.rows.len(), 2);

        // Between：:1 和 :2
        let mut bt = eq("SCORE", Value::Null);
        bt.op = Op::Between;
        bt.value = Value::Integer(5);
        bt.second_value = Value::Integer(35);
        let r = c
            .browse_rows("SYSTEM", &table, &req(vec![bt.clone()]))
            .await
            .expect("browse between");
        assert_eq!(r.rows.len(), 2);
        let n = c
            .browse_rows_count("SYSTEM", &table, &req(vec![bt]))
            .await
            .expect("count between");
        assert_eq!(n, 2);

        // NULL 绑定直达执行路径：修复前兜底成 0i64，TO_CHAR 返回 '0'
        let r = c
            .execute(
                "SELECT NVL(TO_CHAR(:1), 'null-marker') FROM dual",
                &[Value::Null],
            )
            .await
            .expect("null bind");
        assert_eq!(r.rows[0][0], Value::String("null-marker".into()));

        cleanup(&c, "browse").await;
    });
}

/// SET TRANSACTION 隔离声明的行为验证：SERIALIZABLE 事务的快照必须看不到
/// 另一连接在事务开始后提交的行（旧实现发的 ALTER SESSION 拼写不是合法
/// Oracle 语法，一直被"失败回退默认"掩盖——若声明失败，本用例的计数是 5）。
#[test]
fn live_transaction_serializable_snapshot_isolation() {
    if !skip_unless_enabled() {
        return;
    }
    polydb_rt::block_on(async {
        let a = conn_and_table("tx").await;
        let table = table("tx");
        let b = conn().await;

        // A：声明 SERIALIZABLE 并开启事务（SET TRANSACTION 是事务第一条语句）
        let mut tx = a
            .begin_tx(TxMode {
                isolation_level: IsolationLevel::Serializable,
            })
            .expect("begin tx");
        a.execute_in_tx(
            &mut tx,
            &format!("INSERT INTO {table} VALUES (4, 'delta', 40)"),
            &[],
        )
        .await
        .expect("insert in tx");

        // B：提交一行新数据（发生在 A 的事务开始之后）
        let mut tx_b = b
            .begin_tx(TxMode {
                isolation_level: IsolationLevel::ReadCommitted,
            })
            .expect("begin tx b");
        b.execute_in_tx(
            &mut tx_b,
            &format!("INSERT INTO {table} VALUES (5, 'epsilon', 50)"),
            &[],
        )
        .await
        .expect("insert in tx b");
        b.commit(&mut tx_b).expect("commit b");

        // A 的串行化快照看不到 B 的提交：4 行 = 基线 3 行 + A 自己的 1 行
        let r = a
            .execute_in_tx(&mut tx, &format!("SELECT COUNT(*) FROM {table}"), &[])
            .await
            .expect("count in tx");
        assert_eq!(
            r.rows[0][0],
            Value::Integer(4),
            "SERIALIZABLE 快照看不到 B 已提交的行；若隔离声明失效这里会是 5"
        );

        a.rollback(&mut tx).expect("rollback");
        let r = a
            .execute(&format!("SELECT COUNT(*) FROM {table} WHERE id = 4"), &[])
            .await
            .expect("count after rollback");
        assert_eq!(r.rows[0][0], Value::Integer(0), "回滚后第 4 行必须不存在");

        // ReadCommitted（默认级别，不声明）则必须能看到 B 已提交的行
        let r = a
            .execute(&format!("SELECT COUNT(*) FROM {table}"), &[])
            .await
            .expect("count rc");
        assert_eq!(
            r.rows[0][0],
            Value::Integer(4),
            "ReadCommitted 下应为 4 行（基线 3 行 + B 提交的第 5 行），A 已回滚的第 4 行不可见"
        );

        cleanup(&a, "tx").await;
    });
}
