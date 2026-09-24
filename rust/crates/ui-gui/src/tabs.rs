//! 对象标签页模型（语义对齐 Web U1 lib/openTabs.ts）：workbench 常驻第一位 + 表 tab 可多开，
//! 同 schema.table 复用既有 tab；关闭活动 tab 回落到左邻、否则 workbench。
//! 这里只做索引层的纯函数，实体状态由 app.rs 的 TableTabState 承载。

/// workbench 固定占用逻辑索引 0，表 tab 从 1 起。
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(crate) enum ActiveView {
    Workbench,
    Table(usize),
}

pub(crate) fn table_key(schema: &str, table: &str) -> String {
    format!("{schema}.{table}")
}

/// 关闭第 idx 个表 tab 后的活动视图回落（tab_count 为关闭前总数）。
pub(crate) fn active_after_close(tab_count: usize, idx: usize, active: ActiveView) -> ActiveView {
    debug_assert!(idx < tab_count);
    let _ = tab_count;
    match active {
        ActiveView::Workbench => ActiveView::Workbench,
        ActiveView::Table(a) => {
            if a > idx {
                ActiveView::Table(a - 1)
            } else if a == idx {
                // 左邻优先；关的是第一个则回落 workbench。
                if idx > 0 {
                    ActiveView::Table(idx - 1)
                } else {
                    ActiveView::Workbench
                }
            } else {
                ActiveView::Table(a)
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::{active_after_close, table_key, ActiveView};

    #[test]
    fn table_key_format() {
        assert_eq!(table_key("main", "users"), "main.users");
    }

    #[test]
    fn close_falls_back_to_left_neighbor() {
        // 3 个 tab，活动=2，关 2 → 左邻 1。
        assert_eq!(
            active_after_close(3, 2, ActiveView::Table(2)),
            ActiveView::Table(1)
        );
        // 活动=0，关 0 → workbench。
        assert_eq!(
            active_after_close(3, 0, ActiveView::Table(0)),
            ActiveView::Workbench
        );
        // 活动=2，关 0 → 索引平移为 1。
        assert_eq!(
            active_after_close(3, 0, ActiveView::Table(2)),
            ActiveView::Table(1)
        );
        // 活动=0，关 1 → 不动。
        assert_eq!(
            active_after_close(2, 1, ActiveView::Table(0)),
            ActiveView::Table(0)
        );
        // workbench 激活时关任意 tab → 保持 workbench。
        assert_eq!(
            active_after_close(2, 1, ActiveView::Workbench),
            ActiveView::Workbench
        );
    }
}
