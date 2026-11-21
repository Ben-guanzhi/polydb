//! 共享向量对拍：读 test/param-vectors/*.json，Rust 实现输出与
//! expected 逐字段相等（Go 侧 pkg/appcore/param_vectors_test.go 读同一批）。

use polydb_params::{extract_params, rewrite_placeholders};
use serde::Deserialize;
use std::path::{Path, PathBuf};

#[derive(Debug, Deserialize)]
struct Vector {
    name: String,
    #[allow(dead_code)]
    note: String,
    sql: String,
    labels: Vec<String>,
    rewrite: Rewrite,
}

#[derive(Debug, Deserialize)]
struct Rewrite {
    db_type: String,
    sql: String,
}

fn vectors_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../test/param-vectors")
        .canonicalize()
        .expect("向量目录缺失")
}

#[test]
fn vectors_match_expected() {
    let dir = vectors_dir();
    let mut files: Vec<PathBuf> = std::fs::read_dir(&dir)
        .expect("读取向量目录")
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| p.extension().and_then(|x| x.to_str()) == Some("json"))
        .collect();
    files.sort();
    assert!(!files.is_empty(), "没有向量语料");

    for f in files {
        let body = std::fs::read_to_string(&f).expect("读向量");
        let v: Vector = serde_json::from_str(&body).expect("解析向量");

        // 语料自检：name == 文件名（去 .json）。
        let stem = f.file_stem().unwrap().to_str().unwrap();
        assert_eq!(v.name, stem, "name 与文件名不符: {:?}", f);

        let refs = extract_params(&v.sql);
        let labels: Vec<String> = refs.iter().map(|r| r.label()).collect();
        assert_eq!(labels, v.labels, "{}: labels", f.display());

        let out = rewrite_placeholders(&v.sql, &refs, &v.rewrite.db_type);
        assert_eq!(out, v.rewrite.sql, "{}: rewrite", f.display());
    }
}

#[test]
fn no_placeholder_unchanged() {
    assert!(extract_params("SELECT 1").is_empty());
    assert_eq!(rewrite_placeholders("SELECT 1", &[], "mysql"), "SELECT 1");
}
