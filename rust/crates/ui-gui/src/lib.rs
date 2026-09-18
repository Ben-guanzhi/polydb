use gpui::*;
use polydb_transport::LocalTransport;
use std::sync::Arc;

pub struct PolyDBApp {
    _transport: Arc<LocalTransport>,
}

impl PolyDBApp {
    pub fn new(transport: Arc<LocalTransport>) -> Self {
        Self {
            _transport: transport,
        }
    }
}

impl Render for PolyDBApp {
    fn render(&mut self, _window: &mut Window, _cx: &mut Context<Self>) -> impl IntoElement {
        div()
            .size_full()
            .bg(rgb(0x1e1e2e))
            .flex()
            .flex_col()
            .child(
                div()
                    .h(px(40.0))
                    .bg(rgb(0x181825))
                    .flex()
                    .items_center()
                    .px(px(16.0))
                    .child(
                        div()
                            .text_color(rgb(0xcdd6f4))
                            .text_size(px(16.0))
                            .font_weight(FontWeight::BOLD)
                            .child("PolyDB"),
                    ),
            )
            .child(
                div()
                    .flex_1()
                    .flex()
                    .child(
                        div()
                            .w(px(250.0))
                            .bg(rgb(0x181825))
                            .flex()
                            .flex_col()
                            .child(
                                div()
                                    .p(px(12.0))
                                    .text_color(rgb(0xa6adc8))
                                    .text_size(px(12.0))
                                    .child("Connections"),
                            ),
                    )
                    .child(
                        div()
                            .flex_1()
                            .bg(rgb(0x1e1e2e))
                            .flex()
                            .items_center()
                            .justify_center()
                            .child(
                                div()
                                    .text_color(rgb(0x6c7086))
                                    .text_size(px(14.0))
                                    .child("Select a connection to start querying"),
                            ),
                    ),
            )
    }
}

pub fn run(transport: Arc<LocalTransport>) {
    let app = Application::new();
    app.run(move |cx: &mut App| {
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
            move |_window, cx| cx.new(|_cx| PolyDBApp::new(transport)),
        )
        .unwrap();
    });
}

fn rgb(hex: u32) -> Rgba {
    Rgba {
        r: ((hex >> 16) & 0xFF) as f32 / 255.0,
        g: ((hex >> 8) & 0xFF) as f32 / 255.0,
        b: (hex & 0xFF) as f32 / 255.0,
        a: 1.0,
    }
}
