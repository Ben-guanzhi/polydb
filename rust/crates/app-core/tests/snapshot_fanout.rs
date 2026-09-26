// E6.4 快照采集扇出量化（Rust 侧，与 go/pkg/appcore/snapshot_fanout_test.go 同一命题）。
//
// 订正的是同一句话："diff 场景动辄上千表，要不要放宽 200 表上限"——
// 200 是 GUI 关系轨道的外键列表截断（`ui-gui/src/app.rs` 的 `.take(200)`），
// 比对路径（`collect_snapshot`）**没有**任何表数上限，代价是每表 3 次串行元数据查询。
//
// 与 Go 侧的不对称（如实记录）：Go 的测试在包内，可以把驱动换成计数装饰器，
// 于是"每表恰好 3 次"是**精确断言**；Rust 的 `connections` 表是私有的、
// 没有注入驱动的门，所以这一侧只能拿**耗时**与**末号表可见性**作证据。

use polydb_app_core::AppCore;
use polydb_core::{CreateConnectionRequest, DatabaseKind, SchemaDiffRequest, SchemaDiffTarget};
use polydb_storage::{FileKeyring, Storage};
use std::time::Instant;

fn setup() -> AppCore {
    let dir = std::env::temp_dir().join(format!("polydb-fanout-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&dir).unwrap();
    let storage = Storage::open(dir.join("test.db").to_str().unwrap()).unwrap();
    let kr = FileKeyring::open(&dir.to_string_lossy(), "").unwrap();
    AppCore::with_keyring(storage, std::sync::Arc::new(kr))
}

fn sqlite_req(name: &str) -> CreateConnectionRequest {
    CreateConnectionRequest {
        name: name.to_string(),
        kind: DatabaseKind::Sqlite,
        host: None,
        port: None,
        database: Some(":memory:".to_string()),
        username: None,
        password_ref: None,
        password: None,
        options: Default::default(),
        ssh_tunnel: None,
        default_schema: None,
        read_only: None,
        group: None,
        color: None,
    }
}

/// 建 `from..to` 编号的表（3 列 + 1 索引），与 Go 侧 createTables 同形状。
fn create_tables(app: &AppCore, id: polydb_core::ConnectionId, from: usize, to: usize) {
    for i in from..to {
        let name = format!("t{i}");
        smol::block_on(async {
            app.execute(
                id,
                &format!("CREATE TABLE {name}(id INTEGER PRIMARY KEY, a TEXT, b REAL);"),
                &[],
            )
            .await
            .unwrap();
            app.execute(id, &format!("CREATE INDEX ix_{name}_a ON {name}(a);"), &[])
                .await
                .unwrap();
        });
    }
}

fn diff_req(
    source: polydb_core::ConnectionId,
    target: polydb_core::ConnectionId,
) -> SchemaDiffRequest {
    SchemaDiffRequest {
        source: SchemaDiffTarget {
            connection_id: source,
            schema: None,
        },
        target: SchemaDiffTarget {
            connection_id: target,
            schema: None,
        },
        options: None,
    }
}

#[test]
fn snapshot_sees_the_last_table_no_cap() {
    let app = setup();
    let src = app.create_connection(&sqlite_req("src")).unwrap().id;
    let tgt = app.create_connection(&sqlite_req("tgt")).unwrap().id;
    const N: usize = 1000;
    create_tables(&app, src, 0, N);
    create_tables(&app, tgt, 0, N);

    // 期望侧**缺少**第 1000 张表（t999）：若快照被任何上限截断在 200，
    // 这条差异根本不会出现（要么两边都看不见 t999 ⇒ 无差异，要么截断点之前就没它）。
    smol::block_on(async {
        app.execute(tgt, "DROP TABLE t999", &[]).await.unwrap();
        let start = Instant::now();
        let plan = app.schema_diff(&diff_req(src, tgt)).await.unwrap();
        let cost = start.elapsed();
        assert_eq!(
            plan.entries.len(),
            1,
            "差异应恰好 1 条（末号表 t999 的 drop_table）：{:?}",
            plan.entries
                .iter()
                .map(|e| (&e.id, &e.table))
                .collect::<Vec<_>>()
        );
        assert_eq!(plan.entries[0].table, "t999");
        assert_eq!(plan.entries[0].op, polydb_core::SchemaDiffOp::DropTable);
        println!(
            "实测：schema_diff（两侧各 {N} 表，即 2×(1+3n) = {} 次元数据查询）耗时 {:?}",
            2 * (1 + 3 * N),
            cost
        );
        assert!(
            cost < std::time::Duration::from_secs(30),
            "采集慢到不可用：{cost:?}"
        );
    });
}

#[test]
fn snapshot_cost_is_linear() {
    let app = setup();
    let src = app.create_connection(&sqlite_req("lin-src")).unwrap().id;
    let tgt = app.create_connection(&sqlite_req("lin-tgt")).unwrap().id;

    // 同一对连接里逐档加表：只看耗时随 n 的形状，不看差异内容。
    let mut built = 0usize;
    for n in [50usize, 200, 1000] {
        create_tables(&app, src, built, n);
        create_tables(&app, tgt, built, n);
        built = n;
        let cost = smol::block_on(async {
            let start = Instant::now();
            app.schema_diff(&diff_req(src, tgt)).await.unwrap();
            start.elapsed()
        });
        println!(
            "实测：n={n} ⇒ 两侧各 {built} 表的比对耗时 {cost:?}（每表约 {:?}）",
            cost / n as u32
        );
        assert!(cost.as_micros() > 0, "n={n} 的比对耗时测不出来，计时无效");
    }
}
