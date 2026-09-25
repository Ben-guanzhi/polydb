//! 一次性开发工具：把 Rust 实现算出的计划写回向量文件的 `expected` 字段。
//!
//! 用法：`cargo run -p polydb-schema-diff --example regen_expected`
//!
//! 报错用例（带 `error_code`）**不写回**：其 `error_code` / `error_message` 为手写，
//! 本工具只在实际输出与手写不一致时 panic，以保持断言的独立性。
//! `expected` 写回后必须逐条人工复核（op / 相位 / reason 文案 / 勾选状态）。

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

fn set_expected(raw: &str, entries: &[Entry]) -> String {
    let crlf = raw.contains("\r\n");
    let body = raw.replace("\r\n", "\n");
    let src: Vec<&str> = body.split('\n').collect();
    let start = src
        .iter()
        .position(|l| l.trim_start().starts_with("\"expected\""))
        .expect("向量文件缺少 expected 字段");
    let mut depth = 0i32;
    let mut end = start;
    for (i, l) in src.iter().enumerate().skip(start) {
        for ch in l.chars() {
            match ch {
                '[' | '{' => depth += 1,
                ']' | '}' => depth -= 1,
                _ => {}
            }
        }
        if depth == 0 {
            end = i;
            break;
        }
    }
    let lines: Vec<String> = if entries.is_empty() {
        vec!["  \"expected\": []".to_string()]
    } else {
        let mut v = vec!["  \"expected\": [".to_string()];
        for (i, e) in entries.iter().enumerate() {
            let json = serde_json::to_string(e).expect("序列化条目失败");
            v.push(if i + 1 < entries.len() {
                format!("    {json},")
            } else {
                format!("    {json}")
            });
        }
        v.push("  ]".to_string());
        v
    };
    let mut out: Vec<String> = src[..start].iter().map(|s| s.to_string()).collect();
    out.extend(lines);
    out.extend(src[end + 1..].iter().map(|s| s.to_string()));
    let joined = out.join("\n");
    if crlf {
        joined.replace('\n', "\r\n")
    } else {
        joined
    }
}

fn main() {
    let mut files: Vec<PathBuf> = std::fs::read_dir(vectors_dir())
        .expect("读取向量目录失败")
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| p.extension().is_some_and(|x| x == "json"))
        .collect();
    files.sort();
    let mut rewritten = 0;
    for path in files {
        let raw = std::fs::read_to_string(&path).expect("读取向量文件失败");
        let v: Vector = serde_json::from_str(&raw).expect("解析向量文件失败");
        match diff(&v.source, &v.target, &v.options) {
            Ok(entries) => {
                if !v.error_code.is_empty() {
                    panic!(
                        "{} 手写为报错用例，实际却产出了 {} 条计划",
                        v.name,
                        entries.len()
                    );
                }
                let next = set_expected(&raw, &entries);
                if next != raw {
                    std::fs::write(&path, next).expect("写回向量文件失败");
                    rewritten += 1;
                }
                println!("{:<28} 计划 {} 条", v.name, entries.len());
            }
            Err(err) => {
                let err = err.to_protocol();
                assert_eq!(
                    v.error_code, err.code,
                    "{} 错误码不符：手写 {:?} 实际 {:?}",
                    v.name, v.error_code, err.code
                );
                assert_eq!(
                    v.error_message, err.message,
                    "{} 错误文案不符：手写 {:?} 实际 {:?}",
                    v.name, v.error_message, err.message
                );
                println!(
                    "{:<28} 报错校验通过：[{}] {}",
                    v.name, err.code, err.message
                );
            }
        }
    }
    println!("写回 {rewritten} 个文件");
}
