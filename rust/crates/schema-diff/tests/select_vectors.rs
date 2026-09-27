//! 共享「勾选校验」向量：Go 侧 `go/pkg/schemadiff/select_vectors_test.go` 读同一批文件，
//! 两端必须产出**同一条目序列**与逐字一致的中文文案。
//!
//! 与 DDL 语料的分工：DDL 语料只是「路过」`select_entries`（断言最终脚本），这一份直接锁定
//! picked 的 id 序列、条目本体不被篡改、以及两条勾选规则的文案。
//! 计划由两端各自跑 `diff` 现算 ⇒ 同一条用例同时钉住「计划序」与「勾选结果」。
//!
//! `selected` 口径与 DDL 语料一致：`null` = 全部条目，`[]` = 一条都不勾，非空数组 = 勾选子集
//! （**顺序刻意可与计划序不同**，用来证明输出只认计划序）。
//!
//! 期望值由 Go 侧生产器回写（`POLYDB_SELECT_VECTORS_WRITE=1`），本文件是第二实现验证。

use polydb_core::{CoreError, TableType};
use polydb_schema_diff::{diff, generate, select_entries, Entry, Options, Snapshot};
use serde::Deserialize;
use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

/// stale 分支文案前缀（与 Go 侧 `SelectEntries` 逐字对应，覆盖度分类靠它）。
const STALE_PREFIX: &str = "勾选的条目不在当前比对计划里：";
/// 缺组分支文案片段。
const GROUP_INFIX: &str = "需要整表重建，同表变更必须整组勾选：";

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
    expected: Option<Vec<String>>,
    #[serde(default)]
    error_code: String,
    #[serde(default)]
    error_message: String,
}

fn vectors_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../test/schema-select-vectors")
        .canonicalize()
        .expect("勾选向量目录缺失")
}

fn load_all() -> Vec<Vector> {
    let dir = vectors_dir();
    let mut files: Vec<PathBuf> = std::fs::read_dir(&dir)
        .expect("读取勾选向量目录失败")
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| p.extension().is_some_and(|x| x == "json"))
        .collect();
    files.sort();
    assert!(files.len() >= 10, "勾选向量语料过少：{}", files.len());
    files
        .into_iter()
        .map(|p| {
            let text = std::fs::read_to_string(&p).expect("读取勾选向量文件失败");
            serde_json::from_str(&text).unwrap_or_else(|e| panic!("{} 解析失败：{e}", p.display()))
        })
        .collect()
}

fn ids(entries: &[Entry]) -> Vec<String> {
    entries.iter().map(|e| e.id.clone()).collect()
}

fn message(err: &CoreError) -> String {
    err.to_protocol().message
}

/// 跑 diff ⇒ select_entries；`selected = null` 先展开成计划里的全部 id。
/// 计划阶段报错一律视为语料写坏（勾选语料的两侧快照必须跑得出一份计划）。
fn run(v: &Vector) -> (Vec<Entry>, Result<Vec<Entry>, CoreError>) {
    let plan = diff(&v.source, &v.target, &v.options)
        .unwrap_or_else(|e| panic!("{} 的两侧快照跑不出计划（语料写坏）：{e:?}", v.name));
    let selected = v.selected.clone().unwrap_or_else(|| ids(&plan));
    let picked = select_entries(&v.source, &v.target, &plan, &selected);
    (plan, picked)
}

/// 勾选数组里「确实进了结果」的那些 id，按其**给出次序**列出（首个出现胜出）。
fn seq_as_given(want: &[String], got: &[String]) -> Vec<String> {
    let inset: BTreeSet<&String> = got.iter().collect();
    let mut seen = BTreeSet::new();
    want.iter()
        .filter(|id| inset.contains(id) && seen.insert(*id))
        .cloned()
        .collect()
}

/// picked 必须是计划的**严格递增子序列**，且条目本体逐字段未被改写。
fn assert_subsequence(v: &Vector, plan: &[Entry], picked: &[Entry]) {
    let mut prev: Option<usize> = None;
    for (i, p) in picked.iter().enumerate() {
        let at = plan
            .iter()
            .position(|e| e.id == p.id)
            .unwrap_or_else(|| panic!("{} picked 第 {i} 条 {} 不在计划里", v.name, p.id));
        assert!(
            prev.is_none_or(|p0| at > p0),
            "{} picked 顺序不是计划序（第 {i} 条落在计划位置 {at}）",
            v.name
        );
        prev = Some(at);
        assert_eq!(&plan[at], p, "{} picked 第 {i} 条被改写", v.name);
    }
}

/// 主对拍：picked 的 id 序列、错误码与错误文案都要与期望逐字一致。
#[test]
fn shared_select_vectors_match_expected_picked() {
    for v in load_all() {
        let (plan, res) = run(&v);
        if !v.error_code.is_empty() {
            let err = res
                .err()
                .unwrap_or_else(|| panic!("{} 应当报错，实得 picked {:?}", v.name, v.expected));
            let got = err.to_protocol();
            assert_eq!(got.code, v.error_code, "{} 错误码不符", v.name);
            assert_eq!(got.message, v.error_message, "{} 错误文案不符", v.name);
            continue;
        }
        let picked = res.unwrap_or_else(|e| panic!("{} 意外报错：{e}", message(&e)));
        let want = v
            .expected
            .as_ref()
            .unwrap_or_else(|| panic!("{} 缺 expected（非错误语料必须锁定 picked）", v.name));
        assert_eq!(
            &ids(&picked),
            want,
            "{} picked 不符（左实得／右期望）",
            v.name
        );
        assert_subsequence(&v, &plan, &picked);
        // 幂等：同一输入再选一次必须一模一样。
        let (_, again) = run(&v);
        assert_eq!(
            ids(&again.unwrap_or_else(|e| panic!("{} 二次选择报错：{e}", v.name))),
            ids(&picked),
            "{} 输出不稳定",
            v.name
        );
    }
}

/// 覆盖度门禁：勾选校验的每条分支都得有语料打到，与 Go 侧同名门禁共用一份清单。
#[test]
fn shared_select_vectors_cover_every_branch() {
    let mut cov: BTreeSet<&str> = BTreeSet::new();
    for v in load_all() {
        let (plan, res) = run(&v);
        match &v.selected {
            None => {
                cov.insert("selected_null");
            }
            Some(sel) if sel.is_empty() => {
                cov.insert("selected_empty");
            }
            Some(sel) => {
                if let Ok(picked) = &res {
                    if !picked.is_empty() && picked.len() < plan.len() {
                        cov.insert("subset_pass");
                    }
                    // 勾选给定的 id 数多于产出条目数 ⇒ 重复 id 被并掉。
                    if ids(picked).len() < sel.len() {
                        cov.insert("duplicate_ids");
                    }
                    if picked.len() >= 2 && seq_as_given(sel, &ids(picked)) != ids(picked) {
                        cov.insert("unordered_selected");
                    }
                }
            }
        }
        if v.source.tables.iter().any(|t| {
            matches!(
                t.table_type,
                Some(TableType::View) | Some(TableType::MaterializedView)
            )
        }) {
            cov.insert("view_present");
        }
        let msg = match &res {
            Ok(picked) => {
                assert!(
                    v.error_code.is_empty(),
                    "{} 语料标了错误却产出了 picked",
                    v.name
                );
                if !picked.is_empty() {
                    let script = generate(&v.source, &v.target, picked, &v.options)
                        .unwrap_or_else(|e| panic!("{} 勾选结果喂不进生成器：{e:?}", v.name));
                    if script.pragmas_outside_tx {
                        cov.insert("group_whole_pass");
                    }
                }
                // 计划序不等于 id 字典序 ⇒ 「按计划序输出」不是某种顺带排序的结果。
                let given = ids(picked);
                let mut lex = given.clone();
                lex.sort();
                if given.len() >= 2 && lex != given {
                    cov.insert("plan_order_not_lexicographic");
                }
                String::new()
            }
            Err(e) => message(e),
        };
        if msg.is_empty() {
            continue;
        }
        if msg.starts_with(STALE_PREFIX) {
            cov.insert("stale");
            if msg.contains('、') {
                cov.insert("stale_multi");
            }
            // 显示大小写的 id 被判过期 ⇒ 「勾选层不做大小写兜底」的护栏。
            if msg.contains("Foo/") {
                cov.insert("stale_display_casing");
            }
            // 分支优先级：摘掉幽灵 id 后重跑，若改报缺组 ⇒ 这次勾选同时踩中两个分支。
            if let Some(sel) = &v.selected {
                let inplan: BTreeSet<String> = ids(&plan).into_iter().collect();
                let filtered: Vec<String> = sel
                    .iter()
                    .filter(|id| inplan.contains(*id))
                    .cloned()
                    .collect();
                if let Err(e2) = select_entries(&v.source, &v.target, &plan, &filtered) {
                    if message(&e2).contains(GROUP_INFIX) {
                        cov.insert("stale_beats_group");
                    }
                }
            }
        } else if msg.contains(GROUP_INFIX) {
            cov.insert("group");
            if msg.contains('、') {
                cov.insert("group_multi_missing");
            }
            // 文案里的表名用原样大小写、id 用小写形式。
            if msg.contains("表 Foo") {
                cov.insert("group_mixed_case_display");
            }
        } else {
            panic!("{} 勾选错误文案不在已知分支里：{msg}", v.name);
        }
    }
    for branch in [
        "selected_null",
        "selected_empty",
        "subset_pass",
        "duplicate_ids",
        "unordered_selected",
        "view_present",
        "stale",
        "stale_multi",
        "stale_display_casing",
        "stale_beats_group",
        "group",
        "group_multi_missing",
        "group_mixed_case_display",
        "group_whole_pass",
        "plan_order_not_lexicographic",
    ] {
        assert!(cov.contains(branch), "勾选语料缺少分支覆盖：{branch}");
    }
}
