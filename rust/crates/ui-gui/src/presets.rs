//! G2 过滤预设持久化：按表 key（`kind.schema.table`）把命名过滤预设存进本地 JSON。
//!
//! 纯 (de)serialize 与 key 索引逻辑独立于文件 IO，便于单测；文件读写仅做包裹。
//! 目录约定见 AGENTS.md §6（配置 `~/.config/polydb/`，Windows 用 `%APPDATA%\polydb`）。

use std::collections::BTreeMap;
use std::path::PathBuf;

use crate::filters::FilterPreset;

/// 落盘结构：表 key → 预设列表（BTreeMap 保序，序列化稳定可 diff）。
type Store = BTreeMap<String, Vec<FilterPreset>>;

/// 解析配置目录；无法确定家目录时返回 None（调用方降级为“预设不可用”）。
pub(crate) fn config_dir() -> Option<PathBuf> {
    #[cfg(windows)]
    {
        let root = std::env::var("APPDATA").ok().map(PathBuf::from)?;
        Some(root.join("polydb"))
    }
    #[cfg(not(windows))]
    {
        let home = std::env::var("HOME").ok().map(PathBuf::from)?;
        Some(home.join(".config").join("polydb"))
    }
}

fn store_path() -> Option<PathBuf> {
    Some(config_dir()?.join("gui_filter_presets.json"))
}

// ─── 纯逻辑（可测）───

pub(crate) fn parse_store(s: &str) -> Store {
    serde_json::from_str(s).unwrap_or_default()
}

pub(crate) fn render_store(store: &Store) -> String {
    serde_json::to_string_pretty(store).unwrap_or_else(|_| "{}".into())
}

pub(crate) fn upsert(store: &mut Store, key: &str, preset: FilterPreset) {
    let list = store.entry(key.to_string()).or_default();
    if let Some(slot) = list.iter_mut().find(|p| p.name == preset.name) {
        *slot = preset;
    } else {
        list.push(preset);
    }
}

pub(crate) fn list_for(store: &Store, key: &str) -> Vec<FilterPreset> {
    store.get(key).cloned().unwrap_or_default()
}

/// 生成不冲突的自动预设名：P1、P2…（按该表已有数量）。
pub(crate) fn next_name(existing: &[FilterPreset]) -> String {
    let mut n = existing.len() + 1;
    loop {
        let cand = format!("P{n}");
        if !existing.iter().any(|p| p.name == cand) {
            return cand;
        }
        n += 1;
    }
}

// ─── 文件 IO 包装 ───

pub(crate) fn load_store() -> Store {
    store_path()
        .and_then(|p| std::fs::read_to_string(p).ok())
        .map(|s| parse_store(&s))
        .unwrap_or_default()
}

pub(crate) fn save_preset(key: &str, preset: FilterPreset) -> std::io::Result<()> {
    let path = store_path().ok_or_else(|| {
        std::io::Error::new(std::io::ErrorKind::NotFound, "cannot resolve config dir")
    })?;
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)?;
    }
    let mut store = load_store();
    upsert(&mut store, key, preset);
    std::fs::write(path, render_store(&store))
}

pub(crate) fn load_presets(key: &str) -> Vec<FilterPreset> {
    list_for(&load_store(), key)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::filters::FilterDraft;
    use polydb_core::FilterOperator;

    fn preset(name: &str) -> FilterPreset {
        FilterPreset::from_drafts(
            name,
            &[FilterDraft {
                column: "id".into(),
                op: FilterOperator::Gt,
                value: "5".into(),
                second: String::new(),
                values: String::new(),
                case_mode: crate::filters::CaseMode::Default,
            }],
            true,
        )
    }

    #[test]
    fn store_roundtrip_and_upsert() {
        let mut store: Store = BTreeMap::new();
        upsert(&mut store, "sqlite.main.t", preset("P1"));
        upsert(&mut store, "sqlite.main.t", preset("P2"));
        // 同名覆盖不新增。
        upsert(&mut store, "sqlite.main.t", preset("P1"));
        assert_eq!(list_for(&store, "sqlite.main.t").len(), 2);
        let s = render_store(&store);
        let back = parse_store(&s);
        assert_eq!(list_for(&back, "sqlite.main.t").len(), 2);
        assert!(parse_store("not json").is_empty());
    }

    #[test]
    fn next_name_avoids_collision() {
        assert_eq!(next_name(&[]), "P1");
        let v = vec![preset("P1")];
        assert_eq!(next_name(&v), "P2");
        let v2 = vec![preset("P1"), preset("P2")];
        assert_eq!(next_name(&v2), "P3");
    }
}
