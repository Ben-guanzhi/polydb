//! MySQL 真库冒烟：begin_tx 的 SET TRANSACTION + START TRANSACTION 两段式
//! （同一连接）与 REPEATABLE READ 快照行为。环境变量 POLYDB_LIVE_MYSQL
//! 未设置时整体跳过；取值 `mysql://user:pass@host:port/db`。
//! 容器复现（工作台 §6.1）：dbcore-mysql 映射 127.0.0.1:53306。
//! 旧实现发的是 `BEGIN ISOLATION LEVEL …`（PG 语法），对真实服务端必然
//! 语法错误——本用例即事务修复的端到端证据。

use polydb_core::{IsolationLevel, Value};
use polydb_db_core::{SqlDriver, TxMode};
use polydb_db_mysql::MySqlConn;

const TABLE: &str = "polydb_live_tx";

fn skip_unless_enabled() -> bool {
    match std::env::var("POLYDB_LIVE_MYSQL") {
        Ok(_) => true,
        Err(_) => {
            eprintln!("skip: POLYDB_LIVE_MYSQL not set");
            false
        }
    }
}

fn dsn() -> String {
    std::env::var("POLYDB_LIVE_MYSQL").expect("POLYDB_LIVE_MYSQL not set")
}

async fn conn_and_table() -> MySqlConn {
    let c = MySqlConn::open(&dsn()).expect("open mysql");
    let _ = c
        .execute(&format!("DROP TABLE IF EXISTS {TABLE}"), &[])
        .await
        .expect("pre-drop");
    c.execute(
        &format!("CREATE TABLE {TABLE}(id INT PRIMARY KEY, name VARCHAR(50))"),
        &[],
    )
    .await
    .expect("create table");
    c.execute(&format!("INSERT INTO {TABLE} VALUES (1,'alpha'),(2,'beta')"), &[])
        .await
        .expect("insert");
    // 建表/插数提交，测试事务从干净状态开始
    let tx = c
        .begin_tx(TxMode {
            isolation_level: IsolationLevel::ReadCommitted,
        })
        .await
        .expect("setup begin");
    c.commit(tx).await.expect("setup commit");
    c
}

async fn cleanup(c: &MySqlConn) {
    let _ = c.execute(&format!("DROP TABLE IF EXISTS {TABLE}"), &[]).await;
}

/// REPEATABLE READ 快照行为：事务内的两次读一致，看不到另一连接在事务开始
/// 后提交的行；READ COMMITTED 连接则立即可见。若 begin_tx 的两段式失败
/// （语句没落到同一连接 / 语法错误），事务根本开不起来，快照语义随之消失。
#[tokio::test]
async fn live_transaction_repeatable_read_snapshot() {
    if !skip_unless_enabled() {
        return;
    }
    let a = conn_and_table().await;
    let b = MySqlConn::open(&dsn()).expect("open mysql b");

    // A：RR 事务开启后读一次基线
    let mut tx_a = a
        .begin_tx(TxMode {
            isolation_level: IsolationLevel::RepeatableRead,
        })
        .await
        .expect("begin tx a");
    let r = a
        .execute_in_tx(&mut tx_a, &format!("SELECT COUNT(*) FROM {TABLE}"), &[])
        .await
        .expect("count in tx");
    assert_eq!(r.rows[0][0], Value::Integer(2));

    // B：插入一行并提交（发生在 A 的事务开始之后）
    let mut tx_b = b
        .begin_tx(TxMode {
            isolation_level: IsolationLevel::ReadCommitted,
        })
        .await
        .expect("begin tx b");
    b.execute_in_tx(&mut tx_b, &format!("INSERT INTO {TABLE} VALUES (3,'gamma')"), &[])
        .await
        .expect("insert in tx b");
    b.commit(tx_b).await.expect("commit b");

    // A 再读：RR 快照必须仍是 2 行
    let r = a
        .execute_in_tx(&mut tx_a, &format!("SELECT COUNT(*) FROM {TABLE}"), &[])
        .await
        .expect("count in tx 2");
    assert_eq!(
        r.rows[0][0],
        Value::Integer(2),
        "REPEATABLE READ 快照不应看到 B 的提交"
    );
    a.rollback(tx_a).await.expect("rollback a");

    // 新事务（默认 RR，重新声明）里 B 的提交可见
    let mut tx = a
        .begin_tx(TxMode {
            isolation_level: IsolationLevel::ReadCommitted,
        })
        .await
        .expect("begin tx c");
    let r = a
        .execute_in_tx(&mut tx, &format!("SELECT COUNT(*) FROM {TABLE}"), &[])
        .await
        .expect("count new tx");
    assert_eq!(r.rows[0][0], Value::Integer(3), "新事务应看到 B 提交的 3 行");
    a.commit(tx).await.expect("commit c");

    cleanup(&a).await;
}
