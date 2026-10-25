//! polydb-ui-gui：GPUI 桌面前端。
//!
//! 铁律（AGENTS.md §2）：前端只通过 `transport`（进程内 LocalTransport）访问数据，
//! 不依赖任何 driver crate。单窗口主界面（对照 TablePro 交互设计，仅借鉴不 port 代码）：
//! 左栏 = 连接信息 + schema/表导航树；右栏 = 对象标签页（工作台常驻 + 每表一 tab，
//! 概览/数据/结构/关系四 rails）+ SQL 编辑器（Enter 执行 / Shift+Enter 换行 /
//! Ctrl+V 粘贴）+ 结果网格（左键预览、右键菜单、文本/JSON/HEX 面板）。

mod ai;
mod app;
mod changes;
mod cmpsync;
mod explain;
mod exporters;
mod filters;
mod presets;
mod profile;
mod tabs;
mod theme;
mod util;
mod view;

use std::sync::Arc;

use gpui::*;
use gpui_platform::application;
use polydb_transport::Transport;

use app::PolyDBApp;

pub fn run(transport: Arc<dyn Transport>) {
    let app = application();
    app.run(move |cx: &mut App| {
        gpui_component::init(cx);
        gpui_component::theme::init(cx);
        let bounds = Bounds::centered(
            None,
            Size {
                width: px(1200.0),
                height: px(800.0),
            },
            cx,
        );
        cx.open_window(
            WindowOptions {
                window_bounds: Some(WindowBounds::Windowed(bounds)),
                titlebar: Some(TitlebarOptions {
                    title: Some("PolyDB".into()),
                    ..Default::default()
                }),
                ..Default::default()
            },
            move |_window, cx| cx.new(|cx| PolyDBApp::new(transport, cx)),
        )
        .unwrap();
    });
}
