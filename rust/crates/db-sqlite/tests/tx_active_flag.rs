//! F11 回归：SqliteTxHandle 添加 active 标志，防止事务结束后
//! execute_in_tx/commit/rollback 以自动提交模式静默运行。
//!
//! 根因：SqliteTxHandle 是单元结构体，不携带事务状态。commit/rollback
//! 成功后 handle 仍"有效"，execute_in_tx 会在自动提交模式（BEGIN 已结束）
//! 下运行 SQL——调用方误以为事务性执行而实际不在事务内。
//!
//! 修复：添加 active: bool 字段，commit/rollback 成功后置 false；
//! execute_in_tx/commit/rollback 在 handle 已不活跃时返回 TransactionNotFound。
//! commit 失败路径不置 false（P1-2：commit 失败后 rollback 仍须成功）。

use polydb_core::CoreError;
use polydb_db_core::{DatabaseDriver, TxMode};
use polydb_db_sqlite::SqliteConn;

fn open() -> SqliteConn {
    SqliteConn::open(":memory:").unwrap()
}

#[test]
fn tx_normal_commit_flow() {
    let conn = open();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(async {
        let mut tx = d.begin_tx(TxMode::default()).await.unwrap();
        d.execute_in_tx(&mut tx, "CREATE TABLE t(id INTEGER PRIMARY KEY)", &[])
            .await
            .unwrap();
        d.commit(&mut tx).await.unwrap();
    });
}

#[test]
fn tx_normal_rollback_flow() {
    let conn = open();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(async {
        let mut tx = d.begin_tx(TxMode::default()).await.unwrap();
        d.execute_in_tx(&mut tx, "CREATE TABLE t(id INTEGER PRIMARY KEY)", &[])
            .await
            .unwrap();
        d.rollback(&mut tx).await.unwrap();
    });
}

#[test]
fn tx_execute_after_commit_fails() {
    let conn = open();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(async {
        let mut tx = d.begin_tx(TxMode::default()).await.unwrap();
        d.execute_in_tx(&mut tx, "CREATE TABLE t(id INTEGER PRIMARY KEY)", &[])
            .await
            .unwrap();
        d.commit(&mut tx).await.unwrap();

        // execute_in_tx 在 commit 后必须报错——否则 SQL 以自动提交模式
        // 静默运行，调用方误以为事务性执行而实际不在事务内。
        let err = d
            .execute_in_tx(&mut tx, "INSERT INTO t(id) VALUES(1)", &[])
            .await
            .expect_err("execute_in_tx after commit must fail");
        assert!(
            matches!(err, CoreError::TransactionNotFound(_)),
            "expected TransactionNotFound, got {err:?}"
        );
    });
}

#[test]
fn tx_execute_after_rollback_fails() {
    let conn = open();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(async {
        let mut tx = d.begin_tx(TxMode::default()).await.unwrap();
        d.execute_in_tx(&mut tx, "CREATE TABLE t(id INTEGER PRIMARY KEY)", &[])
            .await
            .unwrap();
        d.rollback(&mut tx).await.unwrap();

        let err = d
            .execute_in_tx(&mut tx, "INSERT INTO t(id) VALUES(1)", &[])
            .await
            .expect_err("execute_in_tx after rollback must fail");
        assert!(
            matches!(err, CoreError::TransactionNotFound(_)),
            "expected TransactionNotFound, got {err:?}"
        );
    });
}

#[test]
fn tx_double_commit_fails() {
    let conn = open();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(async {
        let mut tx = d.begin_tx(TxMode::default()).await.unwrap();
        d.commit(&mut tx).await.unwrap();

        let err = d
            .commit(&mut tx)
            .await
            .expect_err("double commit must fail");
        assert!(
            matches!(err, CoreError::TransactionNotFound(_)),
            "expected TransactionNotFound, got {err:?}"
        );
    });
}

#[test]
fn tx_double_rollback_fails() {
    let conn = open();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(async {
        let mut tx = d.begin_tx(TxMode::default()).await.unwrap();
        d.rollback(&mut tx).await.unwrap();

        let err = d
            .rollback(&mut tx)
            .await
            .expect_err("double rollback must fail");
        assert!(
            matches!(err, CoreError::TransactionNotFound(_)),
            "expected TransactionNotFound, got {err:?}"
        );
    });
}

#[test]
fn tx_commit_failure_then_rollback_succeeds() {
    // P1-2 回归：commit 因延迟约束违规失败后，handle 的 active 仍为 true
    // （错误路径不置 false），rollback 可以随后成功。
    let conn = open();
    let d = conn.as_sql().unwrap();

    polydb_rt::block_on(async {
        d.execute("CREATE TABLE parent(id INTEGER PRIMARY KEY)", &[])
            .await
            .unwrap();
        d.execute(
            "CREATE TABLE child(id INTEGER PRIMARY KEY, pid INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)",
            &[],
        )
        .await
        .unwrap();

        let mut tx = d.begin_tx(TxMode::default()).await.unwrap();
        // 插入违反延迟外键约束的行——INSERT 成功（延迟检查），COMMIT 失败
        d.execute_in_tx(&mut tx, "INSERT INTO child(id, pid) VALUES(1, 999)", &[])
            .await
            .unwrap();

        let commit_err = d
            .commit(&mut tx)
            .await
            .expect_err("commit should fail (deferred FK violation)");
        assert!(
            matches!(commit_err, CoreError::Driver(_)),
            "expected Driver error from SQL, got {commit_err:?}"
        );

        // rollback 仍须成功——commit 失败路径不置 active=false
        d.rollback(&mut tx)
            .await
            .expect("rollback must succeed after commit failure");
    });
}
