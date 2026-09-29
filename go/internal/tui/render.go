// Package tui 提供 polydb 终端界面（bubbletea）。仅依赖 transport 与 protocol，
// 不接触任何 driver / appcore（AGENTS.md 铁律：前端只经 app-core 或 transport 访问数据）。
package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/polydb/polydb/pkg/protocol"
)

const (
	cellCap    = 24
	ellipsis   = "…"
	maxCellRow = 500
)

// cellString 把协议值渲染为可读文本（字符串去引号、NULL 大写、其余取 JSON 原文）。
// $blob 值渲染为紧凑标签，与 Web 端 formatCell 对齐。
func cellString(v protocol.Value) string {
	if v.IsNull() {
		return "NULL"
	}
	if n, _, isBlob := protocol.TryGetBlob(v); isBlob {
		return fmt.Sprintf("<blob %d bytes>", n)
	}
	b, err := v.MarshalJSON()
	if err != nil {
		return "?"
	}
	s := string(b)
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

func truncate(s string, n int) string {
	w := runewidth.StringWidth(s)
	if w <= n {
		return s
	}
	// 逐字符裁剪直到达到宽度上限，再补省略号。
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if used+rw > n-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + ellipsis
}

func pad(s string, n int) string {
	w := runewidth.StringWidth(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

// resultTable 把查询结果渲染成对齐的文本表格，超宽列截断、超行数截断。
func resultTable(cols []protocol.ResultColumn, rows [][]protocol.Value, maxWidth, maxRows int) string {
	return renderTable(cols, rows, maxWidth, maxRows, 0, -1, -1)
}

// cellFullText 单元格完整值：对象/数组字面量自动美化 JSON（与 Web tryParseJson 对齐）。
// $blob 例外：完整值面板是 TUI 的取回/复制路径（spec §5.1），必须显示含 base64 的原始 JSON。
func cellFullText(v protocol.Value) string {
	if _, _, isBlob := protocol.TryGetBlob(v); isBlob {
		if b, err := v.MarshalJSON(); err == nil {
			var buf bytes.Buffer
			if json.Indent(&buf, b, "", "  ") == nil {
				return buf.String()
			}
			return string(b)
		}
	}
	s := cellString(v)
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") && !strings.HasPrefix(t, "[") {
		return s
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(t), "", "  "); err != nil {
		return s
	}
	return buf.String()
}

// headerLabel 表头单元格：列名 + 类型徽标（与 Web/Rust 预览头 "name (type)" 对齐）。
func headerLabel(c protocol.ResultColumn) string {
	if c.DataType == "" {
		return c.Name
	}
	return c.Name + " (" + c.DataType + ")"
}

// cellDecorator 按 (行下标, 列下标) 返回该单元格的覆盖显示文本与脏标记。
// 用于浏览网格高亮已暂存（未提交）的编辑值。返回 dirty=false 表示不覆盖。
type cellDecorator func(rowIdx, colIdx int) (display string, dirty bool)

// renderTable 表格渲染核心（无装饰）：见 renderTableDecorated。
func renderTable(cols []protocol.ResultColumn, rows [][]protocol.Value, maxWidth, maxRows, startRow, curRow, curCol int) string {
	return renderTableDecorated(cols, rows, maxWidth, maxRows, startRow, curRow, curCol, nil)
}

// renderTableDecorated 表格渲染核心：startRow 起窗口渲染 maxRows 行，
// (curRow, curCol) 单元格反显高亮（-1 表示无光标）；decor 非空时覆盖脏格显示并标色。
func renderTableDecorated(cols []protocol.ResultColumn, rows [][]protocol.Value, maxWidth, maxRows, startRow, curRow, curCol int, decor cellDecorator) string {
	if len(cols) == 0 {
		return "（无结果集）"
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = runewidth.StringWidth(headerLabel(c))
	}
	for _, row := range rows {
		for i, v := range row {
			if i >= len(widths) {
				break
			}
			w := runewidth.StringWidth(cellString(v))
			if w > widths[i] {
				widths[i] = w
			}
		}
	}
	for i := range widths {
		if widths[i] > cellCap {
			widths[i] = cellCap
		}
	}
	// 总宽超限时按比例收缩（至少保留表头）。
	total := 1
	for _, w := range widths {
		total += w + 3
	}
	if total > maxWidth && maxWidth > 0 {
		scale := float64(maxWidth) / float64(total)
		for i := range widths {
			if n := int(float64(widths[i]) * scale); n > 0 {
				widths[i] = n
			}
		}
		if h := len(cols); h > 0 {
			minW := maxWidth / h
			for i := range widths {
				if widths[i] < minW {
					widths[i] = minW
				}
			}
		}
	}

	var b strings.Builder
	line := func(cells []string, ri int, dirty []bool) string {
		var sb strings.Builder
		sb.WriteString(" ")
		for i, c := range cells {
			if i > 0 {
				sb.WriteString(" │ ")
			}
			s := pad(truncate(c, widths[i]), widths[i])
			switch {
			case ri == curRow && i == curCol:
				s = selected(s)
			case dirty != nil && i < len(dirty) && dirty[i]:
				s = warnText(s)
			}
			sb.WriteString(s)
		}
		return sb.String()
	}
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = headerLabel(c)
	}
	b.WriteString(line(headers, -1, nil))
	b.WriteString("\n")
	sep := make([]string, len(cols))
	for i := range cols {
		sep[i] = strings.Repeat("─", widths[i])
	}
	b.WriteString(line(sep, -2, nil))
	b.WriteString("\n")
	if startRow > len(rows) {
		startRow = len(rows)
	}
	end := min(startRow+maxRows, len(rows))
	for i := startRow; i < end; i++ {
		cells := make([]string, len(cols))
		var dirty []bool
		for j := range cols {
			if j < len(rows[i]) {
				cells[j] = cellString(rows[i][j])
			} else {
				cells[j] = ""
			}
			if decor != nil {
				if disp, d := decor(i, j); d {
					cells[j] = disp
					if dirty == nil {
						dirty = make([]bool, len(cols))
					}
					dirty[j] = true
				}
			}
		}
		b.WriteString(line(cells, i, dirty))
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	if startRow > 0 {
		fmt.Fprintf(&b, "\n… 上方还有 %d 行", startRow)
	}
	if len(rows) > end {
		if startRow == 0 {
			fmt.Fprintf(&b, "\n… 仅显示前 %d 行（共 %d 行）", maxRows, len(rows))
		} else {
			fmt.Fprintf(&b, "\n… 下方还有 %d 行（共 %d 行）", len(rows)-end, len(rows))
		}
	}
	return b.String()
}

// stBadge 渲染语句类型徽标（着色走 theme 的 badgeStyle；空类型回落纯文本「其他」）。
func stBadge(t protocol.StatementType) string {
	if t == "" {
		return "其他"
	}
	return badgeStyle(t).Render(strings.ToUpper(string(t)))
}
