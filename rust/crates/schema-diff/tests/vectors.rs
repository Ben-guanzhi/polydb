//! 共享向量对拍：同一份 JSON 语料喂给 Rust 实现，输出必须与 `expected` 逐字段相等。
//! Go 侧 `go/pkg/schemadiff/vectors_test.go` 读同一批文件做同样的断言。

use polydb_schema_diff::{diff, Entry, Options, Snapshot};
use serde::Deserialize;
use std::path::{Path, PathBuf};

#[derive(Deserialize)]
struct Vector {
    name: String,
    #[serde(default)]
    options: Options,
    source: Snapshot,
    target: Snapshot,
    #[serde(default)]
    expected: Vec<Entry>,
    #[serde(default)]
    error_code: String,
    #[serde(default)]
    error_message: String,
}

fn vectors_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../test/schema-diff-vectors")
        .canonicalize()
        .expect("向量目录缺失")
}

/// 语料自检：`name` 必须等于文件名（去 .json）。
/// 与 Go 侧 `checkVectorName`、另两套 Rust 加载器同规则。
fn check_name(path: &Path, name: &str) {
    let stem = path
        .file_stem()
        .and_then(|s| s.to_str())
        .expect("向量文件名异常");
    assert_eq!(
        name,
        stem,
        "{} 的 name 与文件名不符（实得 {name}）",
        path.display()
    );
}

fn load_all() -> Vec<(PathBuf, Vector)> {
    let dir = vectors_dir();
    let mut files: Vec<PathBuf> = std::fs::read_dir(&dir)
        .expect("读取向量目录失败")
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| p.extension().is_some_and(|x| x == "json"))
        .collect();
    files.sort();
    assert!(files.len() >= 15, "向量语料过少：{}", files.len());
    files
        .into_iter()
        .map(|p| {
            let text = std::fs::read_to_string(&p).expect("读取向量文件失败");
            let v: Vector = serde_json::from_str(&text)
                .unwrap_or_else(|e| panic!("{} 解析失败：{e}", p.display()));
            check_name(&p, &v.name);
            (p, v)
        })
        .collect()
}

#[test]
fn shared_vectors_match_expected_plan() {
    for (path, v) in load_all() {
        let out = diff(&v.source, &v.target, &v.options);
        if !v.error_code.is_empty() {
            let err = out
                .expect_err(&format!("{} 应当报错却产出了计划", v.name))
                .to_protocol();
            assert_eq!(err.code, v.error_code, "{} 错误码不符", v.name);
            assert_eq!(err.message, v.error_message, "{} 错误文案不符", v.name);
            continue;
        }
        let got = out.unwrap_or_else(|e| panic!("{} 意外报错：{e}", v.name));
        assert_eq!(
            got.len(),
            v.expected.len(),
            "{} 条目数不符：{got:?}",
            v.name
        );
        for (i, (g, w)) in got.iter().zip(v.expected.iter()).enumerate() {
            assert_eq!(g, w, "{} 第 {i} 条不符", v.name);
        }
        assert_eq!(got, v.expected, "{} 计划不一致", v.name);
        // 断言稳定性：同一输入再跑一次必须完全一致
        assert_eq!(got, diff(&v.source, &v.target, &v.options).unwrap());
        let _ = path;
    }
}

#[test]
fn every_op_and_phase_is_covered_by_vectors() {
    let mut seen: Vec<&'static str> = Vec::new();
    for (_, v) in load_all() {
        for e in &v.expected {
            if !seen.contains(&e.op.as_str()) {
                seen.push(e.op.as_str());
            }
        }
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
        assert!(seen.contains(&op), "向量语料未覆盖 op：{op}");
    }
}
