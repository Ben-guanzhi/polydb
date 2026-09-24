package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// 执行计划视图（T4.4）：F9 由编辑器 SQL 生成方言 EXPLAIN 并在途执行，
// 结果树/表格文本在此以纯文本缩进渲染，↑/↓ 滚动。解析逻辑在 pkg/explain。

// explainAvail 计划视图可显示的正文行数。
func (m *model) explainAvail() int {
	return max(3, m.height-12)
}

func (m *model) explainKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc, tea.KeyBackspace:
		m.view = viewQuery
		return m, nil
	case tea.KeyUp:
		if m.explainScroll > 0 {
			m.explainScroll--
		}
	case tea.KeyDown:
		m.explainScrollDown()
	}
	if msg.Type == tea.KeyRunes {
		switch string(msg.Runes) {
		case "k":
			if m.explainScroll > 0 {
				m.explainScroll--
			}
		case "j":
			m.explainScrollDown()
		case "q":
			m.view = viewQuery
			return m, nil
		}
	}
	return m, nil
}

func (m *model) explainScrollDown() {
	if m.explainRes == nil {
		return
	}
	maxScroll := max(len(m.explainRes.Lines())-m.explainAvail(), 0)
	m.explainScroll = min(m.explainScroll+1, maxScroll)
}

func (m *model) explainView() string {
	kind, ro := m.connKindReadOnly()
	head := "方言 " + string(kind)
	if ro {
		head += " · 只读"
	}
	var b strings.Builder
	b.WriteString(head + "\n")
	b.WriteString(truncate(m.explainSQL, max(20, m.width-4)) + "\n\n")
	if m.explainRes == nil {
		b.WriteString("（无执行计划：先 F9 分析当前 SQL）\n")
		return b.String()
	}	lines := m.explainRes.Lines()
	avail := m.explainAvail()
	start := min(m.explainScroll, max(len(lines)-avail, 0))
	end := min(start+avail, len(lines))
	for _, l := range lines[start:end] {
		b.WriteString(truncate(l, max(20, m.width-2)) + "\n")
	}
	if len(lines) > end {
		fmt.Fprintf(&b, "… 显示 %d-%d / %d 行（↑/↓ 滚动）\n", start+1, end, len(lines))
	}
	return b.String()
}
