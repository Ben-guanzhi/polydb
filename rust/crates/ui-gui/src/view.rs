//! 渲染层：单窗口主界面——左栏连接 + schema/表树，右栏对象 tab 条 + SQL 编辑器/结果。
//! 全部颜色取自 theme::Theme token。

use gpui::prelude::FluentBuilder;
use gpui::*;
use polydb_core::{DatabaseKind, FilterLogic, RedisKeyType, RedisValue, SortDirection, Value};

use crate::app::{
    conn_visible_fields, CmpStep, ConnField, GridSrc, PolyDBApp, PreviewTab, QuickTarget, Rail,
    PAGE_SIZES,
};
use crate::cmpsync;
use crate::exporters::ExportFormat;
use crate::filters;
use crate::profile;
use crate::tabs::ActiveView;
use crate::theme::{status_color, Theme};
use crate::util::{
    blob_ref_of, cell_str, clip, hex_bytes, hex_dump, try_pretty_json, value_str, CELL_CLIP,
    MAX_RESULT_ROWS,
};

pub(crate) mod ai_panel;
mod dialogs;
mod grid;
mod redis;
mod shell;
pub(crate) mod sql_completion;
pub(crate) mod sql_editor;
mod table;
pub(crate) mod tables_tree;

// 网格作用域 → 稳定元素 id 基数（workbench 与每个表 tab 的 id 空间隔离）。
fn src_key(src: GridSrc) -> usize {
    match src {
        GridSrc::Workbench => usize::MAX / 2,
        GridSrc::Table(i) => i * 1_000_000,
    }
}

fn btn(id: impl Into<ElementId>, label: impl Into<SharedString>, th: &Theme) -> Stateful<Div> {
    div()
        .id(id)
        .px(px(8.0))
        .py(px(3.0))
        .bg(th.surface)
        .text_color(th.fg)
        .text_size(px(12.0))
        .rounded(px(4.0))
        .cursor(CursorStyle::PointingHand)
        .hover(|d| d.bg(th.surface2))
        .child(label.into())
}

fn badge(label: impl Into<SharedString>, fg: gpui::Rgba, th: &Theme) -> Div {
    div()
        .px(px(6.0))
        .py(px(1.0))
        .rounded(px(8.0))
        .bg(th.surface)
        .text_color(fg)
        .text_size(px(10.0))
        .child(label.into())
}

impl Render for PolyDBApp {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        if self.quick_open {
            window.focus(&self.quick_focus, cx);
        }
        if self.compare.is_some() {
            window.focus(&self.cmp_focus, cx);
        }
        self.render_root(window, cx)
    }
}
