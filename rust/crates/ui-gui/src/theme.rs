//! 设计 token（对齐 Web U2 的 token 思路）：全部颜色集中于此，暗色默认 + 亮色。

use gpui::Rgba;

pub(crate) fn rgb(hex: u32) -> Rgba {
    Rgba {
        r: ((hex >> 16) & 0xFF) as f32 / 255.0,
        g: ((hex >> 8) & 0xFF) as f32 / 255.0,
        b: (hex & 0xFF) as f32 / 255.0,
        a: 1.0,
    }
}

#[derive(Clone, Copy)]
pub(crate) struct Theme {
    pub bg: Rgba,
    pub bg_alt: Rgba,
    pub surface: Rgba,
    pub surface2: Rgba,
    pub hover: Rgba,
    pub border: Rgba,
    pub fg: Rgba,
    pub fg_muted: Rgba,
    pub fg_dim: Rgba,
    pub accent: Rgba,
    pub ok: Rgba,
    pub warn: Rgba,
    pub danger: Rgba,
    pub teal: Rgba,
    pub peach: Rgba,
    pub stripe: Rgba,
}

impl Theme {
    // 暗色沿用既有 Catppuccin Mocha 取值，保证视觉延续。
    pub(crate) fn dark() -> Self {
        Self {
            bg: rgb(0x1E1E2E),
            bg_alt: rgb(0x181825),
            surface: rgb(0x313244),
            surface2: rgb(0x45475A),
            hover: rgb(0x26263A),
            border: rgb(0x313244),
            fg: rgb(0xCDD6F4),
            fg_muted: rgb(0xA6ADC8),
            fg_dim: rgb(0x6C7086),
            accent: rgb(0x89B4FA),
            ok: rgb(0xA6E3A1),
            warn: rgb(0xF9E2AF),
            danger: rgb(0xF38BA8),
            teal: rgb(0x94E2D5),
            peach: rgb(0xFAB387),
            stripe: rgb(0x181825),
        }
    }

    pub(crate) fn light() -> Self {
        Self {
            bg: rgb(0xFFFFFF),
            bg_alt: rgb(0xF4F6FA),
            surface: rgb(0xE6E9F0),
            surface2: rgb(0xD8DCE6),
            hover: rgb(0xEEF1F6),
            border: rgb(0xDFE3EC),
            fg: rgb(0x1F2430),
            fg_muted: rgb(0x4C5466),
            fg_dim: rgb(0x8A92A6),
            accent: rgb(0x3B6FD4),
            ok: rgb(0x2F8F4E),
            warn: rgb(0xB07C12),
            danger: rgb(0xC03A4B),
            teal: rgb(0x1F8A80),
            peach: rgb(0xC26627),
            stripe: rgb(0xF7F8FC),
        }
    }

    // 数据库类型徽章色（与 Web/Wingbadge 语义一致的六色映射）。
    pub(crate) fn kind_color(&self, kind: &str) -> Rgba {
        match kind {
            "sqlite" => self.ok,
            "mysql" => self.teal,
            "postgres" => self.accent,
            "mssql" => self.peach,
            "oracle" => self.warn,
            "redis" => self.danger,
            _ => self.fg,
        }
    }
}

// 状态栏着色：含「失败」用红色（与 TUI 查询失败着色语义一致）。
pub(crate) fn status_color(s: &str, th: &Theme) -> Rgba {
    if s.contains("失败") {
        th.danger
    } else {
        th.fg_muted
    }
}
