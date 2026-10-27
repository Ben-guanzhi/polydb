//! SQL Server 真库冒烟：浏览占位符（tiberius 要求 @P{n}）、NULL 绑定、事务。
//! 环境变量 POLYDB_LIVE_MSSQL 未设置时整体跳过；取值逗号分隔：
//! `host,port,database,user,password`（如 `127.0.0.1,51433,master,sa,Dbcore#2026`）。
//! 容器复现（工作台 §6.1）：dbcore-mssql 映射 127.0.0.1:51433。
//! 两个用例各用一张独立表，可并行运行互不干扰。

use polydb_core::{FilterCondition, FilterOperator as Op, IsolationLevel, TableRowsRequest, Value};
use polydb_db_core::{SqlDriver, TxMode};
use polydb_db_mssql::MssqlConn;

fn skip_unless_enabled() -> bool {
    match std::env::var("POLYDB_LIVE_MSSQL") {
        Ok(_) => true,
        Err(_) => {
            eprintln!("skip: POLYDB_LIVE_MSSQL not set");
            false
        }
    }
}

fn target() -> (String, u16, String, String, String) {
    let raw = std::env::var("POLYDB_LIVE_MSSQL").expect("POLYDB_LIVE_MSSQL not set");
    let p: Vec<&str> = raw.split(',').collect();
    assert!(
        p.len() == 5,
        "POLYDB_LIVE_MSSQL 形如 host,port,database,user,password"
    );
    (
        p[0].to_string(),
        p[1].parse().expect("port"),
        p[2].to_string(),
        p[3].to_string(),
        p[4].to_string(),
    )
}

fn table(suffix: &str) -> String {
    format!("polydb_live_{suffix}")
}

async fn conn_and_table(suffix: &str) -> MssqlConn {
    let table = table(suffix);
    let (host, port, db, user, pass) = target();
    let c = MssqlConn::open(&host, port, Some(&db), &user, &pass).expect("connect mssql");
    c.execute(
        &format!("IF OBJECT_ID(N'dbo.{table}') IS NOT NULL DROP TABLE dbo.{table}"),
        &[],
    )
    .await
    .expect("pre-drop");
    c.execute(
        &format!("CREATE TABLE dbo.{table}(id INT PRIMARY KEY, name NVARCHAR(50), score INT NULL)"),
        &[],
    )
    .await
    .expect("create table");
    c.execute(
        &format!(
            "INSERT INTO dbo.{table}(id,name,score) VALUES (1,N'alpha',10),(2,N'beta',NULL),(3,N'gamma',30)"
        ),
        &[],
    )
    .await
    .expect("insert rows");
    c
}

async fn cleanup(c: &MssqlConn, suffix: &str) {
    let table = table(suffix);
    let _ = c
        .execute(
            &format!("IF OBJECT_ID(N'dbo.{table}') IS NOT NULL DROP TABLE dbo.{table}"),
            &[],
        )
        .await;
}

fn req(conds: Vec<FilterCondition>) -> TableRowsRequest {
    TableRowsRequest {
        columns: vec!["id".to_string()],
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

fn is_null(col: &str) -> FilterCondition {
    FilterCondition {
        column: col.to_string(),
        op: Op::Null,
        value: Value::Null,
        second_value: Value::Null,
        values: vec![],
        case_sensitive: None,
    }
}

/// 浏览过滤条件一路走 @P{n} 占位符：任何一处仍是 `?`，tiberius 都会把它
/// 当字面语法，整条 SQL 直接报错——本用例即占位符修复的端到端证据。
#[test]
fn live_browse_filters_placeholders_and_count() {
    if !skip_unless_enabled() {
        return;
    }
    polydb_rt::block_on(async {
        let c = conn_and_table("browse").await;
        let table = table("browse");

        // Eq：@P1 + 字符串绑定
        let r = c
            .browse_rows(
                "dbo",
                &table,
                &req(vec![eq("name", Value::String("beta".into()))]),
            )
            .await
            .expect("browse eq");
        assert_eq!(r.rows, vec![vec![Value::Integer(2)]]);

        // In：@P1,@P2 连续编号
        let mut ic = eq("id", Value::Null);
        ic.op = Op::In;
        ic.values = vec![Value::Integer(1), Value::Integer(3)];
        let r = c
            .browse_rows("dbo", &table, &req(vec![ic]))
            .await
            .expect("browse in");
        assert_eq!(r.rows.len(), 2);

        // Between：@P1 AND @P2；NULL 分值天然不落区间
        let mut bt = eq("score", Value::Null);
        bt.op = Op::Between;
        bt.value = Value::Integer(5);
        bt.second_value = Value::Integer(35);
        let r = c
            .browse_rows("dbo", &table, &req(vec![bt.clone()]))
            .await
            .expect("browse between");
        assert_eq!(r.rows.len(), 2);
        let n = c
            .browse_rows_count("dbo", &table, &req(vec![bt]))
            .await
            .expect("count between");
        assert_eq!(n, 2);

        // MSSQL 默认排序规则（SQL_Latin1_General_CP1_CI_AS）不区分大小写：
        // BETA 命中 beta 是正确行为——固定该语义，防止将来误改。
        let mut cs = eq("name", Value::String("BETA".into()));
        cs.op = Op::Eq;
        let r = c
            .browse_rows("dbo", &table, &req(vec![cs]))
            .await
            .expect("browse case");
        assert_eq!(
            r.rows,
            vec![vec![Value::Integer(2)]],
            "默认 CI 排序规则下 BETA 应命中 beta"
        );

        // NULL 绑定直达执行路径：修复前兜底成 0i64，返回 '0'
        let r = c
            .execute(
                "SELECT COALESCE(CAST(@P1 AS NVARCHAR(20)), 'null-marker') AS v",
                &[Value::Null],
            )
            .await
            .expect("null bind");
        assert_eq!(r.rows[0][0], Value::String("null-marker".into()));

        cleanup(&c, "browse").await;
    });
}

/// 事务：Serializable 声明后回滚丢弃、提交可见；IS NULL 条件（无参数）合法。
#[test]
fn live_transaction_rollback_commit_and_isnull() {
    if !skip_unless_enabled() {
        return;
    }
    polydb_rt::block_on(async {
        let c = conn_and_table("tx").await;
        let table = table("tx");

        let mut tx = c
            .begin_tx(TxMode {
                isolation_level: IsolationLevel::Serializable,
            })
            .expect("begin tx");
        c.execute_in_tx(
            &mut tx,
            &format!("INSERT INTO dbo.{table}(id,name,score) VALUES (4,N'delta',40)"),
            &[],
        )
        .await
        .expect("insert in tx");
        c.rollback(&mut tx).expect("rollback");
        let r = c
            .execute(
                &format!("SELECT COUNT(*) AS n FROM dbo.{table} WHERE id = 4"),
                &[],
            )
            .await
            .expect("count after rollback");
        assert_eq!(r.rows[0][0], Value::Integer(0), "回滚后第 4 行必须不存在");

        let mut tx = c
            .begin_tx(TxMode {
                isolation_level: IsolationLevel::Serializable,
            })
            .expect("begin tx 2");
        c.execute_in_tx(
            &mut tx,
            &format!("INSERT INTO dbo.{table}(id,name,score) VALUES (5,N'epsilon',50)"),
            &[],
        )
        .await
        .expect("insert in tx 2");
        c.commit(&mut tx).expect("commit");
        let r = c
            .execute(
                &format!("SELECT COUNT(*) AS n FROM dbo.{table} WHERE id = 5"),
                &[],
            )
            .await
            .expect("count after commit");
        assert_eq!(r.rows[0][0], Value::Integer(1), "提交后第 5 行必须可见");

        // IS NULL 条件（无绑定参数）命中的正是 score 为 NULL 的第 2 行
        let r = c
            .browse_rows("dbo", &table, &req(vec![is_null("score")]))
            .await
            .expect("browse is null");
        assert_eq!(r.rows, vec![vec![Value::Integer(2)]]);

        cleanup(&c, "tx").await;
    });
}

/// 两项收尾修复的真库验证：
/// 1) list_schemas 用精确名单后，db_ 前缀的用户 schema（db_admin）必须可见
///    （旧过滤 NOT LIKE 'db[_]%' 会把它连同系统角色一起藏掉）；
/// 2) 浏览结果的 data_type 必须是 SQL 拼写（"int"/"nvarchar"…），
///    而不是 tiberius 的 Debug 输出（"Int4"/"NVarchar"）。
#[test]
fn live_list_schemas_db_prefixed_user_schema_and_type_names() {
    if !skip_unless_enabled() {
        return;
    }
    polydb_rt::block_on(async {
        let (host, port, db, user, pass) = target();
        let c = MssqlConn::open(&host, port, Some(&db), &user, &pass).expect("connect mssql");

        // 1) db_ 前缀的用户 schema
        c.execute(
            "IF SCHEMA_ID(N'db_admin') IS NULL EXEC('CREATE SCHEMA db_admin')",
            &[],
        )
        .await
        .expect("create schema");
        let schemas = c.list_schemas().await.expect("list schemas");
        let names: Vec<&str> = schemas.iter().map(|s| s.name.as_str()).collect();
        assert!(
            names.contains(&"db_admin"),
            "db_ 前缀的用户 schema 不应被过滤：{names:?}"
        );
        assert!(
            !names.contains(&"db_owner"),
            "系统角色 db_owner 仍应被排除：{names:?}"
        );

        // 2) 类型名映射：带各行类型的表浏览一列回来
        let table = table("types");
        c.execute(
            &format!("IF OBJECT_ID(N'dbo.{table}') IS NOT NULL DROP TABLE dbo.{table}"),
            &[],
        )
        .await
        .expect("pre-drop");
        c.execute(
            &format!(
                "CREATE TABLE dbo.{table}(id INT PRIMARY KEY, nm NVARCHAR(20), \
                 dec DECIMAL(10,2), dt2 DATETIME2, flt FLOAT, bin VARBINARY(16), flag BIT)"
            ),
            &[],
        )
        .await
        .expect("create types table");
        c.execute(
            &format!("INSERT INTO dbo.{table}(id,nm,dec,dt2,flt,bin,flag) VALUES (1,N'x',1.5,'2026-01-02T03:04:05',2.5,0x01,1)"),
            &[],
        )
        .await
        .expect("insert row");

        // 空 columns = SELECT *，7 列类型名一并验证
        let all = TableRowsRequest {
            columns: vec![],
            conditions: vec![],
            logic: None,
            order_by: Vec::new(),
            offset: 0,
            limit: 10,
        };
        let r = c
            .browse_rows("dbo", &table, &all)
            .await
            .expect("browse types");
        let got: Vec<&str> = r.columns.iter().map(|c| c.data_type.as_str()).collect();
        assert_eq!(
            got,
            vec![
                "int",
                "nvarchar",
                "decimal",
                "datetime2",
                "float",
                "varbinary",
                "bit"
            ],
            "data_type 应为 SQL 拼写而非 tiberius Debug 输出"
        );

        c.execute(&format!("DROP TABLE dbo.{table}"), &[])
            .await
            .expect("drop types table");
        c.execute("DROP SCHEMA IF EXISTS db_admin", &[])
            .await
            .expect("drop schema");
    });
}
