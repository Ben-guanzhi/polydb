//! 共享 DDL 向量对拍：Go 侧 `go/pkg/schemadiff/ddl_vectors_test.go` 读同一批文件，
//! 两端必须产出逐字段一致的脚本（含中文 warning 与非可逆标记）。
//!
//! 流程与真实调用方一致：diff → select_entries → generate。
//! `selected` 为 null 表示「全部条目」，[] 表示「一条都不勾」，非空数组表示勾选子集。
//! 三个阶段中任何一个报错，都必须与 `error_code` / `error_message` 逐字一致。

use polydb_schema_diff::{diff, generate, select_entries, Options, Script, Snapshot};
use serde::Deserialize;
use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

#[derive(Deserialize)]
struct Vector {
    name: String,
    #[serde(default)]
    options: Options,
    source: Snapshot,
    target: Snapshot,
    #[serde(default)]
    selected: Option<Vec<String>>,
    #[serde(default)]
    expected: Option<Script>,
    #[serde(default)]
    error_code: String,
    #[serde(default)]
    error_message: String,
}

fn vectors_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../test/schema-ddl-vectors")
        .canonicalize()
        .expect("DDL 向量目录缺失")
}

fn load_all() -> Vec<Vector> {
    let dir = vectors_dir();
    let mut files: Vec<PathBuf> = std::fs::read_dir(&dir)
        .expect("读取 DDL 向量目录失败")
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| p.extension().is_some_and(|x| x == "json"))
        .collect();
    files.sort();
    assert!(files.len() >= 18, "DDL 向量语料过少：{}", files.len());
    files
        .into_iter()
        .map(|p| {
            let text = std::fs::read_to_string(&p).expect("读取 DDL 向量文件失败");
            serde_json::from_str(&text).unwrap_or_else(|e| panic!("{} 解析失败：{e}", p.display()))
        })
        .collect()
}

/// 跑完整管线；三个阶段的错误都原样上抛（调用方用 `to_protocol` 取错误码与文案）。
fn run(v: &Vector) -> Result<Script, polydb_core::CoreError> {
    let plan = diff(&v.source, &v.target, &v.options)?;
    // selected 为 null 表示「全部条目」。
    let selected: Vec<String> = v
        .selected
        .clone()
        .unwrap_or_else(|| plan.iter().map(|e| e.id.clone()).collect());
    let picked = select_entries(&v.source, &v.target, &plan, &selected)?;
    generate(&v.source, &v.target, &picked, &v.options)
}

#[test]
fn shared_ddl_vectors_match_expected_script() {
    for v in load_all() {
        let out = run(&v);
        if !v.error_code.is_empty() {
            let err = out
                .expect_err(&format!("{} 应当报错却产出了脚本", v.name))
                .to_protocol();
            assert_eq!(err.code, v.error_code, "{} 错误码不符", v.name);
            assert_eq!(err.message, v.error_message, "{} 错误文案不符", v.name);
            continue;
        }
        let got = out.unwrap_or_else(|e| panic!("{} 意外报错：{e:?}", e));
        let want = v
            .expected
            .as_ref()
            .unwrap_or_else(|| panic!("{} 缺 expected（非错误语料必须锁定脚本）", v.name));
        assert_eq!(
            got.statements.len(),
            want.statements.len(),
            "{} 正向语句数不符：got {:?}",
            v.name,
            got.statements.iter().map(|s| &s.sql).collect::<Vec<_>>()
        );
        for (i, (g, w)) in got
            .statements
            .iter()
            .zip(want.statements.iter())
            .enumerate()
        {
            assert_eq!(g, w, "{} 正向第 {i} 条不符", v.name);
        }
        assert_eq!(
            got.rollback.len(),
            want.rollback.len(),
            "{} 回滚语句数不符：got {:?}",
            v.name,
            got.rollback.iter().map(|s| &s.sql).collect::<Vec<_>>()
        );
        for (i, (g, w)) in got.rollback.iter().zip(want.rollback.iter()).enumerate() {
            assert_eq!(g, w, "{} 回滚第 {i} 条不符", v.name);
        }
        assert_eq!(
            got.pragmas_outside_tx, want.pragmas_outside_tx,
            "{} pragmas_outside_tx 不符",
            v.name
        );
        assert_eq!(got.ddl_in_tx, want.ddl_in_tx, "{} ddl_in_tx 不符", v.name);
        // 幂等：同一输入再生成一次必须逐字一致
        assert_eq!(got, run(&v).unwrap(), "{} 输出不稳定", v.name);
    }
}

/// 覆盖度门禁：9 个动作、8 种告警、可逆/不可逆、含重建/不含重建都要有语料打到。
#[test]
fn shared_ddl_vectors_cover_every_op_and_warning() {
    let mut ops: Vec<&'static str> = Vec::new();
    let mut warnings: Vec<String> = Vec::new();
    let mut nr = [0usize; 2];
    let mut pragmas = [0usize; 2];
    for v in load_all() {
        let Ok(plan) = diff(&v.source, &v.target, &v.options) else {
            continue;
        };
        let wanted: BTreeSet<String> = v.selected.clone().unwrap_or_default().into_iter().collect();
        for e in &plan {
            let picked = v.selected.is_none() || wanted.contains(&e.id);
            if picked && !ops.contains(&e.op.as_str()) {
                ops.push(e.op.as_str());
            }
        }
        let Ok(got) = run(&v) else { continue };
        for s in got.statements.iter().chain(got.rollback.iter()) {
            for w in &s.warnings {
                if !warnings.contains(w) {
                    warnings.push(w.clone());
                }
            }
            nr[usize::from(s.non_reversible)] += 1;
        }
        pragmas[usize::from(got.pragmas_outside_tx)] += 1;
    }
    for op in [
        "create_table",
        "drop_table",
        "add_column",
        "drop_column",
        "alter_column",
        "add_index",
        "drop_index",
        "add_foreign_key",
        "drop_foreign_key",
    ] {
        assert!(ops.contains(&op), "DDL 语料未覆盖动作：{op}");
    }
    for w in [
        polydb_schema_diff::W_TRIGGERS,
        polydb_schema_diff::W_GENERATED_COLUMNS,
        polydb_schema_diff::W_CHECK_CONSTRAINTS,
        polydb_schema_diff::W_AUTOINCREMENT,
        polydb_schema_diff::W_CREATE_TABLE,
        polydb_schema_diff::W_DROP_COLUMN_CHECK,
        polydb_schema_diff::W_ADD_COLUMN_LIMITS,
        polydb_schema_diff::W_REBUILD_NEW_NOT_NULL,
    ] {
        assert!(warnings.iter().any(|x| x == w), "DDL 语料未覆盖告警：{w}");
    }
    // 视图警告（E6.5）是带名字的动态文案，不能按常量逐字比 ⇒ 退一步锁它的两句话头，
    // 完整的逐字一致性由 d24/d25 两份语料的 expected 兜住。
    for needle in ["DROP TABLE 不检查依赖", "重建经 RENAME"] {
        assert!(
            warnings.iter().any(|w| w.contains(needle)),
            "DDL 语料未覆盖视图警告：{needle}"
        );
    }
    assert!(
        nr[0] > 0 && nr[1] > 0,
        "DDL 语料必须同时覆盖可逆与不可逆语句：{nr:?}"
    );
    assert!(
        pragmas[0] > 0 && pragmas[1] > 0,
        "DDL 语料必须同时覆盖含重建与不含重建的脚本：{pragmas:?}"
    );
}
