//! Text-level Vim editing subset used by the SQL editor's normal/visual modes.
//!
//! Ported 1:1 from dbstudio (`crates/ui/src/components/vim.rs`) during the
//! polydb ← dbstudio integration (sub-plan 5 / task #6). All operations work on
//! the whole buffer as an owned `String` plus a byte `caret`, so the module is
//! UI-free and easily unit-tested. The GPUI editor layer syncs its buffer into
//! [`VimBuf`] before dispatching a key and pushes results back via [`VimStep`].

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum VimMode {
    Normal,
    Insert,
    VisualChar,
    VisualLine,
    VisualBlock,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Operator {
    Delete,
    Yank,
}

/// What the GPUI layer must do after a key was consumed.
#[derive(Debug)]
pub struct VimStep {
    /// Full replacement buffer, when the key changed the text.
    pub text: Option<String>,
    /// Byte caret position to move to.
    pub caret: usize,
    /// When set, display this byte selection instead of a caret.
    pub selection: Option<(usize, usize)>,
    /// Switch the editor into insert mode after applying (`i/a/A/o/O`).
    pub to_insert: bool,
}

impl VimStep {
    fn moved(caret: usize) -> Self {
        VimStep {
            text: None,
            caret,
            selection: None,
            to_insert: false,
        }
    }
    fn selection(caret: usize, sel: (usize, usize)) -> Self {
        VimStep {
            text: None,
            caret,
            selection: Some(sel),
            to_insert: false,
        }
    }
    fn replaced(text: String, caret: usize) -> Self {
        VimStep {
            text: Some(text),
            caret,
            selection: None,
            to_insert: false,
        }
    }
    fn insert_mode(text: Option<String>, caret: usize) -> Self {
        VimStep {
            text,
            caret,
            selection: None,
            to_insert: true,
        }
    }
}

pub struct VimBuf {
    pub text: String,
    pub caret: usize,
    pub mode: VimMode,
    pub count: usize,
    pub operator: Option<Operator>,
    pub pending_r: bool,
    pub register: String,
    pub undo_stack: Vec<String>,
    pub redo_stack: Vec<String>,
    visual_anchor: Option<usize>,
    block_anchor: Option<(usize, usize)>,
}

impl Default for VimBuf {
    fn default() -> Self {
        Self {
            text: String::new(),
            caret: 0,
            mode: VimMode::Normal,
            count: 0,
            operator: None,
            pending_r: false,
            register: String::new(),
            undo_stack: Vec::new(),
            redo_stack: Vec::new(),
            visual_anchor: None,
            block_anchor: None,
        }
    }
}

// ---------- byte-safe buffer helpers ----------

fn char_offsets(text: &str) -> Vec<usize> {
    text.char_indices().map(|(i, _)| i).collect()
}

fn byte_after(text: &str, byte: usize) -> usize {
    match text[byte..].chars().next() {
        Some(c) => byte + c.len_utf8(),
        None => byte,
    }
}

fn newline_positions(text: &str) -> Vec<usize> {
    text.bytes()
        .enumerate()
        .filter(|(_, b)| *b == b'\n')
        .map(|(i, _)| i)
        .collect()
}

fn line_starts(text: &str) -> Vec<usize> {
    std::iter::once(0)
        .chain(newline_positions(text).iter().map(|p| p + 1))
        .collect()
}

fn lines(text: &str) -> usize {
    if text.is_empty() {
        1
    } else {
        newline_positions(text).len() + 1
    }
}

fn line_of(text: &str, byte: usize) -> usize {
    newline_positions(text)
        .iter()
        .position(|p| byte <= *p)
        .unwrap_or_else(|| newline_positions(text).len())
}

/// Byte offset just past the content of `line` (i.e. its trailing `\n`, or the
/// end of buffer for the last line).
fn line_content_end(text: &str, line: usize) -> usize {
    let starts = line_starts(text);
    if line + 1 < starts.len() {
        starts[line + 1] - 1
    } else {
        text.len()
    }
}

/// Byte offset of the last content character of `line`.
fn line_last_char(text: &str, line: usize) -> usize {
    let start = line_starts(text)[line];
    let end = line_content_end(text, line);
    if end > start {
        move_left(text, end)
    } else {
        start
    }
}

fn first_non_ws(text: &str, line: usize) -> usize {
    let start = line_starts(text)[line];
    let seg = &text[start..line_content_end(text, line)];
    match seg.find(|c: char| !c.is_ascii_whitespace()) {
        Some(i) => start + i,
        None => line_content_end(text, line),
    }
}

fn char_col(text: &str, line_start: usize, byte: usize) -> usize {
    text[line_start..byte].chars().count()
}

fn byte_for_col(text: &str, line: usize, col: usize) -> usize {
    let start = line_starts(text)[line];
    let seg = &text[start..line_content_end(text, line)];
    match seg.char_indices().nth(col) {
        Some((i, _)) => start + i,
        None if col == 0 => start,
        None => line_last_char(text, line),
    }
}

fn move_line(text: &str, caret: usize, delta: isize, col: usize) -> Option<usize> {
    let current = line_of(text, caret) as isize + delta;
    if current < 0 {
        return None;
    }
    let target = current as usize;
    if target >= lines(text) {
        return None;
    }
    Some(byte_for_col(text, target, col))
}

fn move_left(text: &str, caret: usize) -> usize {
    char_offsets(text)
        .iter()
        .rev()
        .find(|p| **p < caret)
        .copied()
        .unwrap_or(caret)
}

fn move_right(text: &str, caret: usize) -> usize {
    if caret >= text.len() {
        return caret;
    }
    byte_after(text, caret)
}

fn move_left_n(text: &str, caret: usize, n: usize) -> usize {
    let mut c = caret;
    for _ in 0..n {
        c = move_left(text, c);
    }
    c
}

fn move_right_n(text: &str, caret: usize, n: usize) -> usize {
    let mut c = caret;
    for _ in 0..n {
        c = move_right(text, c);
    }
    c
}

/// PL/SQL Developer 的 Smart AutoIndent 词表（Tools > Preferences > Editor，
/// 随工具发行 SmartIndent.txt）：光标所在行以这些词开头时，换行给下一行缩进一级。
const SMART_INDENT_WORDS: [&str; 13] = [
    "BEGIN", "IF", "THEN", "ELSE", "ELSIF", "PROCEDURE", "FUNCTION", "CASE", "WHEN", "LOOP",
    "WHILE", "FOR", "EXCEPTION",
];

/// 一级缩进 = 两个空格。
const SMART_INDENT_STEP: &str = "  ";

/// 行首第一个词（跳掉前导空白），返回切片引用不分配。以注释或字符串字面量
/// 开头的行不是语句，返回 `None`。
fn first_code_word(line: &str) -> Option<&str> {
    let rest = line.trim_start();
    let end = rest.find(|c: char| c.is_whitespace()).unwrap_or(rest.len());
    let word = &rest[..end];
    if word.is_empty()
        || word.starts_with("--")
        || word.starts_with("/*")
        || word.starts_with('#')
        || word.starts_with('\'')
        || word.starts_with('"')
        || word.starts_with('`')
    {
        return None;
    }
    Some(word)
}

/// 行首词是否命中词表（大小写不敏感、整词匹配：`FORM` 不算 `FOR`）。
fn smart_indent_line(line: &str) -> bool {
    first_code_word(line).is_some_and(|w| SMART_INDENT_WORDS.iter().any(|k| k.eq_ignore_ascii_case(w)))
}

fn is_word(c: char) -> bool {
    c.is_alphanumeric() || c == '_'
}

fn next_word_start(text: &str, caret: usize, big: bool) -> usize {
    let chars = char_offsets(text);
    if chars.is_empty() {
        return 0;
    }
    let ws = |c: char| c.is_whitespace();
    let word = if big {
        |c: char| !c.is_whitespace()
    } else {
        |c: char| is_word(c)
    };
    let mut i = chars
        .iter()
        .position(|p| *p >= caret)
        .unwrap_or(chars.len());
    if i < chars.len() && !ws(text[chars[i]..].chars().next().unwrap()) {
        while i < chars.len() && word(text[chars[i]..].chars().next().unwrap()) {
            i += 1;
        }
    }
    while i < chars.len() && ws(text[chars[i]..].chars().next().unwrap_or(' ')) {
        i += 1;
    }
    chars.get(i).copied().unwrap_or(text.len())
}

fn prev_word_start(text: &str, caret: usize, big: bool) -> usize {
    let chars = char_offsets(text);
    if chars.is_empty() {
        return 0;
    }
    let ws = |c: char| c.is_whitespace();
    let word = if big {
        |c: char| !c.is_whitespace()
    } else {
        |c: char| is_word(c)
    };
    // Point at the character before the caret (or 0 when the caret is at the
    // start of the buffer).
    let mut i = chars
        .iter()
        .position(|p| *p >= caret)
        .unwrap_or(chars.len());
    i = i.saturating_sub(1);
    // Skip whitespace, then the word run; `i` ends at the index just before
    // the previous word, which is where its first char sits.
    while i > 0 && ws(text[chars[i]..].chars().next().unwrap()) {
        i -= 1;
    }
    while i > 0 && word(text[chars[i]..].chars().next().unwrap()) {
        i -= 1;
    }
    // If we hit the buffer start while still on a word char, the word begins
    // at the first char.
    if i == 0 && word(text[chars[0]..].chars().next().unwrap()) {
        0
    } else {
        chars.get(i).copied().unwrap_or(0)
    }
}

/// Byte right after the last char of the word at or ahead of `caret`.
fn next_word_end(text: &str, caret: usize, big: bool) -> usize {
    let chars = char_offsets(text);
    if chars.is_empty() {
        return 0;
    }
    let ws = |c: char| c.is_whitespace();
    let word = if big {
        |c: char| !c.is_whitespace()
    } else {
        |c: char| is_word(c)
    };
    let mut i = chars
        .iter()
        .position(|p| *p >= caret)
        .unwrap_or(chars.len());
    if i >= chars.len() {
        return text.len();
    }
    if ws(text[chars[i]..].chars().next().unwrap()) {
        while i < chars.len() && ws(text[chars[i]..].chars().next().unwrap_or(' ')) {
            i += 1;
        }
    }
    let mut last = chars[i];
    while i < chars.len() && word(text[chars[i]..].chars().next().unwrap()) {
        last = chars[i];
        i += 1;
    }
    byte_after(text, last)
}

// ---------- geometry done ----------

fn byte_for_char_pos(text: &str, cp: usize) -> usize {
    text.char_indices()
        .nth(cp)
        .map(|(i, _)| i)
        .unwrap_or(text.len())
}

fn col_to_byte_relative(line: &str, col: usize) -> usize {
    line.char_indices()
        .nth(col)
        .map(|(i, _)| i)
        .unwrap_or(line.len())
}

fn remove_char_at(line: &str, col: usize) -> String {
    line.char_indices()
        .enumerate()
        .filter(|(i, _)| *i != col)
        .fold(String::with_capacity(line.len()), |mut acc, (_, (_, c))| {
            acc.push(c);
            acc
        })
}

fn normalized(range: (usize, usize)) -> (usize, usize) {
    (range.0.min(range.1), range.0.max(range.1))
}

impl VimBuf {
    pub fn reset(&mut self) {
        self.visual_anchor = None;
        self.block_anchor = None;
        self.count = 0;
        self.operator = None;
        self.pending_r = false;
    }

    /// Align the internal buffer with the (possibly natively edited) GPUI text.
    pub fn sync(&mut self, text: &str, caret: usize) {
        self.text = text.to_string();
        self.caret = caret.min(text.len());
    }

    fn push_snapshot(&mut self) {
        self.undo_stack.push(self.text.clone());
        // Bound the undo stack: a long editing session would otherwise grow it
        // without limit, each entry a full copy of the buffer.
        const MAX_UNDO: usize = 100;
        if self.undo_stack.len() > MAX_UNDO {
            self.undo_stack.remove(0);
        }
        self.redo_stack.clear();
    }

    fn undo(&mut self) -> VimStep {
        let snapshot = match self.undo_stack.pop() {
            Some(s) => s,
            None => return VimStep::moved(self.caret),
        };
        self.redo_stack.push(self.text.clone());
        self.text = snapshot;
        self.caret = self.caret.min(self.text.len());
        self.count = 0;
        self.reset();
        VimStep::replaced(self.text.clone(), self.caret)
    }

    fn redo(&mut self) -> VimStep {
        let snapshot = match self.redo_stack.pop() {
            Some(s) => s,
            None => return VimStep::moved(self.caret),
        };
        self.undo_stack.push(self.text.clone());
        self.text = snapshot;
        self.caret = self.caret.min(self.text.len());
        self.count = 0;
        self.reset();
        VimStep::replaced(self.text.clone(), self.caret)
    }

    fn leave_normal(&mut self) -> VimStep {
        self.mode = VimMode::Normal;
        self.reset();
        VimStep::moved(self.caret)
    }

    fn enter_insert(&mut self, caret: usize) -> VimStep {
        self.push_snapshot();
        self.mode = VimMode::Insert;
        self.caret = caret.min(self.text.len());
        self.reset();
        VimStep::insert_mode(None, self.caret)
    }

    /// Run one key against the current mode. Returns `None` when the key should
    /// be passed through to the native editor (only meaningful in insert mode).
    pub fn step(&mut self, key: &str, ctrl: bool, shift: bool, alt: bool) -> Option<VimStep> {
        match self.mode {
            VimMode::Insert => {
                let leave = key == "escape" || (ctrl && (key == "c" || key == "["));
                if leave {
                    Some(self.leave_normal())
                } else if key == "enter" {
                    self.smart_newline()
                } else {
                    None
                }
            }
            VimMode::VisualChar | VimMode::VisualLine => {
                if alt {
                    return None;
                }
                self.step_visual(key, ctrl, shift)
            }
            VimMode::VisualBlock => {
                if alt {
                    return None;
                }
                self.step_block(key, shift)
            }
            VimMode::Normal => {
                if alt {
                    return None;
                }
                self.step_normal(key, ctrl, shift)
            }
        }
    }

    fn selection_range(&self) -> Option<(usize, usize)> {
        let anchor = self.visual_anchor?;
        if self.mode == VimMode::VisualLine {
            let a_line = line_of(&self.text, anchor);
            let b_line = line_of(&self.text, self.caret);
            let (lo_line, hi_line) = (a_line.min(b_line), a_line.max(b_line));
            let start = line_starts(&self.text)[lo_line];
            let end = {
                let s = line_starts(&self.text);
                if hi_line + 1 < s.len() {
                    s[hi_line + 1]
                } else {
                    self.text.len()
                }
            };
            Some((start, end))
        } else {
            let (lo, hi) = normalized((anchor, self.caret));
            Some((lo, byte_after(&self.text, hi)))
        }
    }

    fn step_visual(&mut self, key: &str, ctrl: bool, shift: bool) -> Option<VimStep> {
        if key == "escape" || (ctrl && key == "c") {
            return Some(self.leave_normal());
        }
        let _ = shift;
        if ctrl {
            return None;
        }
        let mut cur = self.caret;
        match key {
            "d" | "x" | "delete" => {
                let Some((lo, hi)) = self.selection_range() else {
                    return Some(self.leave_normal());
                };
                self.register = self.text[lo..hi].to_string();
                self.push_snapshot();
                self.text = format!("{}{}", &self.text[..lo], &self.text[hi..]);
                let caret = lo.min(self.text.len());
                self.mode = VimMode::Normal;
                self.reset();
                Some(VimStep::replaced(self.text.clone(), caret))
            }
            "y" => {
                let Some((lo, hi)) = self.selection_range() else {
                    return Some(self.leave_normal());
                };
                self.register = self.text[lo..hi].to_string();
                self.mode = VimMode::Normal;
                self.reset();
                Some(VimStep::moved(
                    byte_after(&self.text, self.caret).min(self.text.len()),
                ))
            }
            "o" => {
                let anchor = self.visual_anchor?;
                self.visual_anchor = Some(self.caret);
                self.caret = anchor;
                let (lo, hi) = self
                    .selection_range()
                    .expect("visual_anchor just set, selection must exist");
                Some(VimStep::selection(self.caret, (lo, hi)))
            }
            _ => {
                let _step = self.step_motion(key, &mut cur);
                self.caret = cur;
                let (lo, hi) = self
                    .selection_range()
                    .expect("visual mode guarantees a selection");
                Some(VimStep::selection(self.caret, (lo, hi)))
            }
        }
    }

    /// Apply a motion to `cur` for the given key (shared by normal/visual).
    fn step_motion(&mut self, key: &str, cur: &mut usize) -> Option<VimStep> {
        let text = &self.text;
        match key {
            "h" | "left" | "backspace" => {
                *cur = move_left(text, *cur);
                Some(VimStep::moved(*cur))
            }
            "l" | "space" | "right" => {
                *cur = move_right(text, *cur);
                Some(VimStep::moved(*cur))
            }
            "j" | "down" | "enter" => {
                let line = line_of(text, *cur);
                let col = char_col(text, line_starts(text)[line], *cur);
                let target = move_line(text, *cur, 1, col)?;
                *cur = target;
                Some(VimStep::moved(*cur))
            }
            "k" | "up" => {
                let line = line_of(text, *cur);
                let col = char_col(text, line_starts(text)[line], *cur);
                let target = move_line(text, *cur, -1, col)?;
                *cur = target;
                Some(VimStep::moved(*cur))
            }
            "0" => {
                *cur = line_starts(text)[line_of(text, *cur)];
                Some(VimStep::moved(*cur))
            }
            "$" | "end" => {
                *cur = line_content_end(text, line_of(text, *cur));
                Some(VimStep::moved(*cur))
            }
            "^" | "home" => {
                *cur = first_non_ws(text, line_of(text, *cur));
                Some(VimStep::moved(*cur))
            }
            "w" => {
                *cur = next_word_start(text, *cur, false);
                Some(VimStep::moved(*cur))
            }
            "W" => {
                *cur = next_word_start(text, *cur, true);
                Some(VimStep::moved(*cur))
            }
            "b" => {
                *cur = prev_word_start(text, *cur, false);
                Some(VimStep::moved(*cur))
            }
            "B" => {
                *cur = prev_word_start(text, *cur, true);
                Some(VimStep::moved(*cur))
            }
            "e" => {
                let end = next_word_end(text, *cur, false);
                *cur = move_left(text, end);
                Some(VimStep::moved(*cur))
            }
            "E" => {
                let end = next_word_end(text, *cur, true);
                *cur = move_left(text, end);
                Some(VimStep::moved(*cur))
            }
            "gg" => {
                *cur = 0;
                Some(VimStep::moved(*cur))
            }
            "G" => {
                *cur = line_starts(text)[lines(text) - 1];
                Some(VimStep::moved(*cur))
            }
            _ => None,
        }
    }

    fn step_block(&mut self, key: &str, shift: bool) -> Option<VimStep> {
        if key == "escape" {
            return Some(self.leave_normal());
        }
        let _ = shift;
        let (anchor_line, _anchor_col) = self
            .block_anchor
            .unwrap_or((line_of(&self.text, self.caret), 0));
        let cur_line = line_of(&self.text, self.caret);
        let (top, bottom) = (anchor_line.min(cur_line), anchor_line.max(cur_line));
        match key {
            "j" | "down" | "enter" => {
                let line = line_of(&self.text, self.caret);
                let col = char_col(&self.text, line_starts(&self.text)[line], self.caret);
                if let Some(c) = move_line(&self.text, self.caret, 1, col) {
                    self.caret = c;
                }
                Some(VimStep::selection(self.caret, (self.caret, self.caret)))
            }
            "k" | "up" => {
                let line = line_of(&self.text, self.caret);
                let col = char_col(&self.text, line_starts(&self.text)[line], self.caret);
                if let Some(c) = move_line(&self.text, self.caret, -1, col) {
                    self.caret = c;
                }
                Some(VimStep::selection(self.caret, (self.caret, self.caret)))
            }
            "h" | "left" => {
                self.caret = move_left(&self.text, self.caret);
                Some(VimStep::selection(self.caret, (self.caret, self.caret)))
            }
            "l" | "right" | "space" => {
                self.caret = move_right(&self.text, self.caret);
                Some(VimStep::selection(self.caret, (self.caret, self.caret)))
            }
            "0" => {
                self.caret = line_starts(&self.text)[line_of(&self.text, self.caret)];
                Some(VimStep::selection(self.caret, (self.caret, self.caret)))
            }
            "$" | "end" => {
                self.caret = line_last_char(&self.text, line_of(&self.text, self.caret));
                Some(VimStep::selection(self.caret, (self.caret, self.caret)))
            }
            "d" | "c" => {
                self.block_delete(top, bottom);
                if key == "c" {
                    // keep the caret at the deletion point; insert text stays
                    // with the caller's "insert at caret after" semantics.
                    self.caret = self.caret.min(self.text.len());
                }
                Some(VimStep::replaced(self.text.clone(), self.caret))
            }
            "y" => {
                self.block_yank(top, bottom);
                Some(VimStep::moved(self.caret))
            }
            _ => None,
        }
    }

    /// Delete one character at the block column from each selected line.
    fn block_delete(&mut self, top: usize, bottom: usize) {
        let col = self.block_anchor.map(|(_, c)| c).unwrap_or(0);
        let starts = line_starts(&self.text);
        let count = bottom - top + 1;
        let mut new_lines: Vec<String> = Vec::with_capacity(count);
        for (line, start) in starts.iter().enumerate().skip(top).take(count) {
            let content = &self.text[*start..line_content_end(&self.text, line)];
            let c_ix = col_to_byte_relative(content, col);
            new_lines.push(remove_char_at(content, c_ix));
        }
        self.push_snapshot();
        self.text = self.replace_lines(top, bottom, new_lines);
        self.caret = self.caret.min(self.text.len());
    }

    fn block_yank(&mut self, top: usize, bottom: usize) {
        let col = self.block_anchor.map(|(_, c)| c).unwrap_or(0);
        let starts = line_starts(&self.text);
        let count = bottom - top + 1;
        let mut out = Vec::with_capacity(count);
        for (line, start) in starts.iter().enumerate().skip(top).take(count) {
            let content = &self.text[*start..line_content_end(&self.text, line)];
            let c_ix = col_to_byte_relative(content, col);
            if let Some((_, c)) = content.char_indices().nth(c_ix) {
                out.push(c);
            }
        }
        self.register = out.iter().collect();
    }

    /// Rebuild the buffer replacing lines `[top..=bottom]` with `new_lines`.
    fn replace_lines(&self, top: usize, bottom: usize, new_lines: Vec<String>) -> String {
        let starts = line_starts(&self.text);
        // Prefix keeps everything up to (and including) the newline that ends
        // the line before `top`.
        let mut rebuilt = String::with_capacity(self.text.len());
        rebuilt.push_str(&self.text[..starts[top]]);
        for (i, replacement) in new_lines.iter().enumerate() {
            let line = top + i;
            rebuilt.push_str(replacement);
            if line + 1 < lines(&self.text) {
                rebuilt.push('\n');
            }
        }
        // Suffix starts after the original newline of line `bottom`, which we
        // already emitted above.
        let suffix_start = if bottom + 1 < starts.len() {
            starts[bottom + 1]
        } else {
            self.text.len()
        };
        rebuilt.push_str(&self.text[suffix_start..]);
        rebuilt
    }

    // ---------- normal mode ----------

    fn step_normal(&mut self, key: &str, ctrl: bool, _shift: bool) -> Option<VimStep> {
        if self.pending_r {
            self.pending_r = false;
            self.count = 0;
            if let Some(c) = key.chars().next() {
                if key.chars().count() == 1 && self.caret < self.text.len() {
                    self.push_snapshot();
                    let cp = self.text[..self.caret].chars().count();
                    let mut chars: Vec<char> = self.text.chars().collect();
                    if cp < chars.len() {
                        let old = chars[cp];
                        chars[cp] = c;
                        self.text = chars.into_iter().collect();
                        self.register = old.to_string();
                        return Some(VimStep::replaced(
                            self.text.clone(),
                            byte_for_char_pos(&self.text, cp),
                        ));
                    }
                }
            }
            return Some(VimStep::moved(self.caret));
        }

        if let Some(op) = self.operator {
            let count = self.count.max(1);
            let result = if key == "d" && op == Operator::Delete {
                Some(self.delete_lines(count))
            } else if key == "y" && op == Operator::Yank {
                Some(self.yank_lines(count))
            } else {
                self.operator_motion_range(key, count)
                    .map(|range| match op {
                        Operator::Delete => self.apply_delete(range),
                        Operator::Yank => self.apply_yank(range),
                    })
            };
            self.operator = None;
            self.count = 0;
            return result.or_else(|| Some(VimStep::moved(self.caret)));
        }

        match key {
            "escape" => return Some(VimStep::moved(self.caret)),
            "1" | "2" | "3" | "4" | "5" | "6" | "7" | "8" | "9" => {
                self.count =
                    self.count * 10 + (key.chars().next().unwrap_or('0') as usize - '0' as usize);
                return Some(VimStep::moved(self.caret));
            }
            _ => {}
        }
        if ctrl {
            return match key {
                "r" => Some(self.redo()),
                "v" => {
                    self.mode = VimMode::VisualBlock;
                    let line = line_of(&self.text, self.caret);
                    let col = char_col(&self.text, line_starts(&self.text)[line], self.caret);
                    self.block_anchor = Some((line, col));
                    Some(VimStep::selection(self.caret, (self.caret, self.caret)))
                }
                "c" => Some(self.leave_normal()),
                _ => None,
            };
        }

        // plain operators
        match key {
            "u" => Some(self.undo()),
            "x" | "delete" => {
                let count = self.count.max(1);
                let end = move_right_n(&self.text, self.caret, count);
                self.count = 0;
                self.register = self.text[self.caret..end].to_string();
                self.push_snapshot();
                self.text = format!("{}{}", &self.text[..self.caret], &self.text[end..]);
                let caret = self.caret.min(self.text.len());
                self.caret = caret;
                Some(VimStep::replaced(self.text.clone(), caret))
            }
            "d" => {
                self.operator = Some(Operator::Delete);
                Some(VimStep::moved(self.caret))
            }
            "y" => {
                self.operator = Some(Operator::Yank);
                Some(VimStep::moved(self.caret))
            }
            "D" => {
                let range = (
                    self.caret,
                    line_content_end(&self.text, line_of(&self.text, self.caret)),
                );
                self.count = 0;
                Some(self.apply_delete(range))
            }
            "Y" => {
                let range = (
                    self.caret,
                    line_content_end(&self.text, line_of(&self.text, self.caret)),
                );
                self.count = 0;
                Some(self.apply_yank(range))
            }
            "p" | "P" => {
                let n = self.count.max(1);
                self.count = 0;
                if self.register.is_empty() {
                    Some(VimStep::moved(self.caret))
                } else {
                    Some(self.paste(key == "P", n))
                }
            }
            "J" => {
                self.count = 0;
                Some(self.join_lines())
            }
            "i" => {
                self.count = 0;
                Some(self.enter_insert(self.caret))
            }
            "a" => {
                self.count = 0;
                Some(self.enter_insert(byte_after(&self.text, self.caret)))
            }
            "I" => {
                self.count = 0;
                Some(self.enter_insert(first_non_ws(&self.text, line_of(&self.text, self.caret))))
            }
            "A" => {
                self.count = 0;
                let line = line_of(&self.text, self.caret);
                let caret = line_content_end(&self.text, line);
                Some(self.enter_insert(caret))
            }
            "o" | "O" => {
                self.count = 0;
                Some(self.open_line(key == "O"))
            }
            "v" => {
                self.count = 0;
                self.mode = VimMode::VisualChar;
                self.visual_anchor = Some(self.caret);
                let (lo, hi) = self
                    .selection_range()
                    .expect("visual_anchor just set, selection must exist");
                Some(VimStep::selection(self.caret, (lo, hi)))
            }
            "V" => {
                self.count = 0;
                self.mode = VimMode::VisualLine;
                self.visual_anchor =
                    Some(line_starts(&self.text)[line_of(&self.text, self.caret)]);
                let (lo, hi) = self
                    .selection_range()
                    .expect("visual_anchor just set, selection must exist");
                Some(VimStep::selection(self.caret, (lo, hi)))
            }
            "r" => {
                self.count = 0;
                self.pending_r = true;
                Some(VimStep::moved(self.caret))
            }
            "0" => {
                self.count = 0;
                self.caret = line_starts(&self.text)[line_of(&self.text, self.caret)];
                Some(VimStep::moved(self.caret))
            }
            "G" => {
                self.count = 0;
                self.caret = line_starts(&self.text)[lines(&self.text) - 1];
                Some(VimStep::moved(self.caret))
            }
            "j" | "down" | "enter" => {
                let n = self.count.max(1) as isize;
                self.count = 0;
                let line = line_of(&self.text, self.caret);
                let col = char_col(&self.text, line_starts(&self.text)[line], self.caret);
                if let Some(c) = move_line(&self.text, self.caret, n, col) {
                    self.caret = c;
                }
                Some(VimStep::moved(self.caret))
            }
            "k" | "up" => {
                let n = self.count.max(1) as isize;
                self.count = 0;
                let line = line_of(&self.text, self.caret);
                let col = char_col(&self.text, line_starts(&self.text)[line], self.caret);
                if let Some(c) = move_line(&self.text, self.caret, -n, col) {
                    self.caret = c;
                }
                Some(VimStep::moved(self.caret))
            }
            "h" | "left" | "backspace" => {
                let n = self.count.max(1);
                self.count = 0;
                self.caret = move_left_n(&self.text, self.caret, n);
                Some(VimStep::moved(self.caret))
            }
            "l" | "space" | "right" => {
                let n = self.count.max(1);
                self.count = 0;
                self.caret = move_right_n(&self.text, self.caret, n);
                Some(VimStep::moved(self.caret))
            }
            _ => {
                // Any other key in normal mode is consumed as a no-op motion
                // (or an ordinary motion like w/b/e/...), never passed through.
                let mut cur = self.caret;
                let _ = self.step_motion(key, &mut cur);
                self.caret = cur;
                self.count = 0;
                Some(VimStep::moved(self.caret))
            }
        }
    }

    fn operator_motion_range(&self, key: &str, count: usize) -> Option<(usize, usize)> {
        let start = self.caret;
        let raw = match key {
            "w" => Some((start, next_word_start(&self.text, start, false))),
            "W" => Some((start, next_word_start(&self.text, start, true))),
            "b" => Some((prev_word_start(&self.text, start, false), start)),
            "B" => Some((prev_word_start(&self.text, start, true), start)),
            "e" => Some((start, next_word_end(&self.text, start, false))),
            "E" => Some((start, next_word_end(&self.text, start, true))),
            "$" | "end" => Some((
                start,
                line_content_end(&self.text, line_of(&self.text, start)),
            )),
            "0" => Some((line_starts(&self.text)[line_of(&self.text, start)], start)),
            "^" | "home" => Some((first_non_ws(&self.text, line_of(&self.text, start)), start)),
            "h" | "left" | "backspace" => Some((move_left_n(&self.text, start, count), start)),
            "l" | "space" | "right" => Some((start, move_right_n(&self.text, start, count))),
            "gg" => Some((0, start)),
            "j" | "down" => {
                let line = line_of(&self.text, start);
                let col = char_col(&self.text, line_starts(&self.text)[line], start);
                let end = move_line(&self.text, start, count as isize, col)?;
                Some((start, end))
            }
            "k" | "up" => {
                let line = line_of(&self.text, start);
                let col = char_col(&self.text, line_starts(&self.text)[line], start);
                let end = move_line(&self.text, start, -(count as isize), col)?;
                Some((end, start))
            }
            _ => None,
        };
        raw.map(normalized)
    }

    fn line_range(&self, count: usize) -> (usize, usize) {
        let start = line_starts(&self.text)[line_of(&self.text, self.caret)];
        let mut end = start;
        for _ in 0..count {
            if end >= self.text.len() {
                break;
            }
            end = byte_after(&self.text, end);
            if end < self.text.len() && self.text.as_bytes()[end] == b'\n' {
                end += 1;
            }
        }
        (start, end.min(self.text.len()))
    }

    fn delete_lines(&mut self, count: usize) -> VimStep {
        let (start, end) = self.line_range(count);
        self.register = self.text[start..end].to_string();
        self.push_snapshot();
        self.text = format!("{}{}", &self.text[..start], &self.text[end..]);
        let caret = start.min(self.text.len());
        self.caret = caret;
        VimStep::replaced(self.text.clone(), caret)
    }

    fn yank_lines(&mut self, count: usize) -> VimStep {
        let (start, end) = self.line_range(count);
        self.register = self.text[start..end].to_string();
        VimStep::moved(self.caret)
    }

    fn apply_delete(&mut self, range: (usize, usize)) -> VimStep {
        let (lo, hi) = range;
        if hi > lo {
            self.register = self.text[lo..hi].to_string();
            self.push_snapshot();
            self.text = format!("{}{}", &self.text[..lo], &self.text[hi..]);
        }
        let caret = lo.min(self.text.len());
        self.caret = caret;
        VimStep::replaced(self.text.clone(), caret)
    }

    fn apply_yank(&mut self, range: (usize, usize)) -> VimStep {
        let (lo, hi) = range;
        if hi > lo {
            self.register = self.text[lo..hi].to_string();
        }
        VimStep::moved(self.caret)
    }

    fn paste(&mut self, before: bool, count: usize) -> VimStep {
        let nl = self.register.ends_with('\n');
        let mut block = String::new();
        for _ in 0..count {
            block.push_str(&self.register);
        }
        if nl {
            let line = line_of(&self.text, self.caret);
            let at = if before {
                line_starts(&self.text)[line]
            } else {
                let eol = line_content_end(&self.text, line);
                if line + 1 < lines(&self.text) {
                    eol + 1
                } else {
                    self.text.len()
                }
            };
            self.push_snapshot();
            self.text = format!("{}{}{}", &self.text[..at], block, &self.text[at..]);
            let caret = at.min(self.text.len());
            self.caret = caret;
            VimStep::replaced(self.text.clone(), caret)
        } else {
            let at = if before {
                self.caret
            } else {
                byte_after(&self.text, self.caret)
            };
            self.push_snapshot();
            self.text = format!("{}{}{}", &self.text[..at], block, &self.text[at..]);
            let caret = at.min(self.text.len());
            self.caret = caret;
            VimStep::replaced(self.text.clone(), caret)
        }
    }

    fn join_lines(&mut self) -> VimStep {
        let line = line_of(&self.text, self.caret);
        if line + 1 >= lines(&self.text) {
            return VimStep::moved(self.caret);
        }
        let starts = line_starts(&self.text);
        let a = self.text[starts[line]..line_content_end(&self.text, line)]
            .trim_end()
            .to_string();
        let b_start = starts[line + 1];
        let b = self.text[b_start..line_content_end(&self.text, line + 1)]
            .trim_start()
            .to_string();
        let joiner = if a.is_empty() || b.is_empty() {
            ""
        } else {
            " "
        };
        let joined = format!("{}{}{}", a, joiner, b);
        self.push_snapshot();
        let suffix_start = if line + 2 < starts.len() {
            starts[line + 2] - 1
        } else {
            self.text.len()
        };
        self.text = format!(
            "{}{}{}",
            &self.text[..starts[line]],
            joined,
            &self.text[suffix_start..]
        );
        let caret = (starts[line] + joined.len()).min(self.text.len());
        self.caret = caret;
        VimStep::replaced(self.text.clone(), caret)
    }

    /// Smart AutoIndent：光标行以 [`SMART_INDENT_WORDS`] 的词开头时，插入换行并
    /// 给新行缩进一级，光标落在缩进之后；未命中返回 `None`，交回原生编辑器插入
    /// 裸换行。语义对齐 PL/SQL Developer 的 Smart AutoIndent 选项。
    fn smart_newline(&mut self) -> Option<VimStep> {
        let caret = self.caret.min(self.text.len());
        let line = line_of(&self.text, caret);
        let starts = line_starts(&self.text);
        let (start, end) = if line + 1 < starts.len() {
            (starts[line], starts[line + 1])
        } else {
            (starts[line], self.text.len())
        };
        if !smart_indent_line(&self.text[start..end]) {
            return None;
        }
        self.push_snapshot();
        self.text.insert(caret, '\n');
        let indent_at = caret + 1;
        self.text.insert_str(indent_at, SMART_INDENT_STEP);
        self.reset();
        self.caret = (indent_at + SMART_INDENT_STEP.len()).min(self.text.len());
        Some(VimStep::insert_mode(Some(self.text.clone()), self.caret))
    }

    fn open_line(&mut self, before: bool) -> VimStep {
        self.push_snapshot();
        let line = line_of(&self.text, self.caret);
        let starts = line_starts(&self.text);
        let old_len = self.text.len();
        let at = if before {
            starts[line]
        } else {
            let eol = line_content_end(&self.text, line);
            (if line + 1 < lines(&self.text) {
                eol + 1
            } else {
                self.text.len()
            })
            .min(old_len)
        };
        self.text.insert(at, '\n');
        // The new blank line's insertion point is `at`, except for `o` on the
        // final line without a trailing newline, where the inserted newline is
        // trailing and the caret must land one byte after it.
        let caret = if before {
            at
        } else if at >= old_len {
            at + 1
        } else {
            at
        };
        self.mode = VimMode::Insert;
        self.reset();
        self.caret = caret.min(self.text.len());
        VimStep::insert_mode(Some(self.text.clone()), self.caret)
    }
}
