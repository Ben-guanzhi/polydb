//! 左栏对象树（polydb 版）：基于 gpui-component 0.6.6 的 `Tree` 控件。
//!
//! 对齐 dbstudio 的 `tables_tree.rs` 设计（schema 分组 + 搜索框 + 点击开表），
//! 但适配 polydb 的数据模型：
//! - 数据来自 `PolyDBApp.tables`（`polydb_core::TableInfo`，含 `schema/name/table_type`）。
//! - 表节点展开时懒加载列清单（复用 `PolyDBApp::ensure_columns` + `cols_cache`），
//!   列作为该表节点的子项呈现（保留原手搓侧栏的「内联列」能力）。
//! - 点击表节点 → `PolyDBApp::open_table_tab`。
//!
//! 与 dbstudio 的差异：dbstudio 用全局 `AppState` 驱动重建；polydb 以
//! `Entity<PolyDBApp>` 直接观察并读写，省去全局态。

use std::collections::{BTreeMap, HashMap, HashSet};

use gpui::prelude::*;
use gpui::{
    div, px, App, ClickEvent, Context, Entity, Subscription, Window,
};
use gpui_component::{
    h_flex, input::InputEvent, input::InputState, label::Label, list::ListItem,
    tree::TreeEntry, tree::TreeEvent, tree::TreeItem, tree::TreeState, v_flex, ActiveTheme as _,
    Icon, IconName, Sizable as _,
};
use gpui_component::input::Input;
use gpui_component::tree::tree;
use polydb_core::{ColumnInfo, TableInfo, TableType};

use crate::app::PolyDBApp;

/// 表叶子节点 id 前缀（后接表在 `tables` 中的序号 `ti`）。
const TABLE_ID_PREFIX: &str = "t";

pub struct TablesTree {
    tree_state: Entity<TreeState>,
    search: Entity<InputState>,
    app: Entity<PolyDBApp>,
    /// 已展开的表序号集合（跨重建保持展开态）。
    expanded: HashSet<usize>,
    search_query: String,
    _subs: Vec<Subscription>,
}

impl TablesTree {
    pub fn view(window: &mut Window, cx: &mut App, app: Entity<PolyDBApp>) -> Entity<Self> {
        cx.new(|cx| Self::new(window, cx, app))
    }

    fn new(window: &mut Window, cx: &mut Context<Self>, app: Entity<PolyDBApp>) -> Self {
        let tree_state = cx.new(|cx| TreeState::new(cx));
        let search = cx.new(|cx| InputState::new(window, cx).placeholder("搜索表…"));

        let tree_state_for_ev = tree_state.clone();
        let _subs = vec![
            // 观察宿主：tables / cols_cache 变化时重建树。
            cx.observe(&app, |this, _app, cx| {
                this.rebuild(cx);
            }),
            // 树展开/折叠事件：展开表节点时触发列懒加载。
            cx.subscribe(&tree_state_for_ev, |this, _st, ev, cx| match ev {
                TreeEvent::Expanded(id) => {
                    if let Some(ti) = parse_table_id(id) {
                        this.expanded.insert(ti);
                        this.app.update(cx, |a, cx| a.ensure_columns(ti, cx));
                    }
                    this.rebuild(cx);
                }
                TreeEvent::Collapsed(id) => {
                    if let Some(ti) = parse_table_id(id) {
                        this.expanded.remove(&ti);
                    }
                    this.rebuild(cx);
                }
            }),
            // 搜索框输入 → 重建。
            cx.subscribe_in(&search, window, |this, _input, _ev: &InputEvent, _window, cx| {
                this.search_query = this.search.read(cx).value().to_string();
                this.rebuild(cx);
            }),
        ];

        let mut this = Self {
            tree_state,
            search,
            app,
            expanded: HashSet::new(),
            search_query: String::new(),
            _subs,
        };
        this.rebuild(cx);
        this
    }

    fn rebuild(&mut self, cx: &mut Context<Self>) {
        let (tables, cols_cache, cols_loading) = {
            let app = self.app.read(cx);
            (
                app.tables.clone(),
                app.cols_cache.clone(),
                app.cols_loading.clone(),
            )
        };
        let items = build_items(
            &tables,
            &cols_cache,
            &cols_loading,
            &self.expanded,
            &self.search_query,
        );
        self.tree_state.update(cx, |st, cx| {
            st.set_items(items, cx);
        });
        cx.notify();
    }

    fn render_tree_item(
        &self,
        ix: usize,
        entry: &TreeEntry,
        selected: bool,
        cx: &mut Context<Self>,
    ) -> ListItem {
        let item = entry.item();
        let id = item.id.to_string();
        let is_folder = entry.is_folder();
        let is_table = id.starts_with(TABLE_ID_PREFIX);
        let is_view = item.id.contains(":view");
        let is_materialized = item.id.contains(":materialized_view");

        let theme = cx.theme();
        let text_color = if selected {
            theme.accent_foreground
        } else {
            theme.foreground
        };
        let bg = if selected { theme.list_active } else { theme.list_even };

        let icon = if is_folder {
            if entry.is_expanded() {
                IconName::ChevronDown
            } else {
                IconName::ChevronRight
            }
        } else if is_view || is_materialized {
            IconName::Eye
        } else {
            IconName::Frame
        };

        let prefix = if is_folder {
            "SCHEMA"
        } else if is_view || is_materialized {
            "VIEW"
        } else {
            "TABLE"
        };

        let app = self.app.clone();
        let open_ti = parse_table_id(&id);
        ListItem::new(ix)
            .w_full()
            .py_1()
            .px_2()
            .pl(px(12.0) + px(14.0) * entry.depth() as f32)
            .bg(bg)
            .text_color(text_color)
            .child(
                h_flex()
                    .justify_between()
                    .items_center()
                    .child(
                        h_flex()
                            .min_w_0()
                            .items_center()
                            .gap_2()
                            .child(Icon::new(icon).size_4().text_color(text_color.opacity(0.7)))
                            .child(Label::new(item.label.clone()).text_sm()),
                    )
                    .child(Label::new(prefix).text_xs().text_color(text_color.opacity(0.6))),
            )
            .when(is_table, |el| {
                el.on_click(cx.listener(move |_this, _: &ClickEvent, _window, cx| {
                    if let Some(ti) = open_ti {
                        let (schema, name) = {
                            let a = app.read(cx);
                            match a.tables.get(ti) {
                                Some(t) => (t.schema.clone(), t.name.clone()),
                                None => return,
                            }
                        };
                        app.update(cx, |a, cx| a.open_table_tab(schema, name, cx));
                    }
                }))
            })
    }
}

fn parse_table_id(id: &str) -> Option<usize> {
    id.strip_prefix(TABLE_ID_PREFIX)?.parse::<usize>().ok()
}

fn tag_for(t: &TableInfo) -> &'static str {
    match t.table_type {
        TableType::Table => "",
        TableType::View => " (视图)",
        TableType::MaterializedView => " (物化视图)",
    }
}

fn build_items(
    tables: &[TableInfo],
    cols_cache: &HashMap<usize, Vec<ColumnInfo>>,
    cols_loading: &HashSet<usize>,
    expanded: &HashSet<usize>,
    query: &str,
) -> Vec<TreeItem> {
    let q = query.trim().to_lowercase();
    let mut by_schema: BTreeMap<String, Vec<(usize, &TableInfo)>> = BTreeMap::new();
    for (ti, t) in tables.iter().enumerate() {
        if !q.is_empty() && !t.name.to_lowercase().contains(&q) {
            continue;
        }
        by_schema.entry(t.schema.clone()).or_default().push((ti, t));
    }

    by_schema
        .into_iter()
        .map(|(schema, mut entries)| {
            entries.sort_by(|a, b| a.1.name.cmp(&b.1.name));
            let children: Vec<TreeItem> = entries
                .into_iter()
                .map(|(ti, t)| {
                    let mut leaf = TreeItem::new(
                        format!("{TABLE_ID_PREFIX}{ti}"),
                        format!("{}{}", t.name, tag_for(t)),
                    );
                    if expanded.contains(&ti) {
                        leaf = leaf.expanded(true);
                        if let Some(cols) = cols_cache.get(&ti) {
                            for c in cols.iter().take(80) {
                                let mark = if c.is_primary_key { "🔑 " } else { "" };
                                leaf = leaf.child(TreeItem::new(
                                    format!("{TABLE_ID_PREFIX}{ti}:col:{}", c.name),
                                    format!("{mark}{}  {}", c.name, c.data_type),
                                ));
                            }
                            if cols.len() > 80 {
                                leaf = leaf.child(TreeItem::new(
                                    format!("{TABLE_ID_PREFIX}{ti}:more"),
                                    format!("… 共 {} 列", cols.len()),
                                ));
                            }
                        } else if cols_loading.contains(&ti) {
                            leaf = leaf.child(TreeItem::new(
                                format!("{TABLE_ID_PREFIX}{ti}:loading"),
                                "列加载中…",
                            ));
                        }
                    }
                    leaf
                })
                .collect();
            let label = if schema.is_empty() {
                "Tables".to_string()
            } else {
                schema.clone()
            };
            TreeItem::new(format!("schema:{label}"), label)
                .expanded(true)
                .children(children)
        })
        .collect()
}

impl Render for TablesTree {
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let view = cx.entity();
        let count = self.app.read(cx).tables.len();
        let search = self.search.clone();
        let tree_state = self.tree_state.clone();

        v_flex()
            .id("tables-tree")
            .flex_1()
            .min_h_0()
            .flex_col()
            .child(
                div()
                    .px_2()
                    .pt_2()
                    .pb_1()
                    .text_color(cx.theme().foreground)
                    .text_size(px(12.0))
                    .child(format!("Tables ({count})")),
            )
            .child(
                div()
                    .px_1()
                    .pb_1()
                    .child(Input::new(&search).w_full().small()),
            )
            .child(
                tree(&tree_state, move |ix, entry, selected, _window, cx| {
                    view.update(cx, |this, cx| this.render_tree_item(ix, entry, selected, cx))
                })
                .flex_1()
                .min_h_0()
                .w_full()
                .overflow_hidden()
                .border_1()
                .border_color(cx.theme().border)
                .rounded(cx.theme().radius),
            )
            .into_any_element()
    }
}
