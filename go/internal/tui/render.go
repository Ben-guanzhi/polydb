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
func cellString(v protocol.Value) string {
	if v.IsNull() {
		return "NULL"
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
func cellFullText(v protocol.Value) string {
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

// renderTable 表格渲染核心：startRow 起窗口渲染 maxRows 行，
// (curRow, curCol) 单元格反显高亮（-1 表示无光标）。
func renderTable(cols []protocol.ResultColumn, rows [][]protocol.Value, maxWidth, maxRows, startRow, curRow, curCol int) string {
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
	line := func(cells []string, ri int) string {
		var sb strings.Builder
		sb.WriteString(" ")
		for i, c := range cells {
			if i > 0 {
				sb.WriteString(" │ ")
			}
			s := pad(truncate(c, widths[i]), widths[i])
			if ri == curRow && i == curCol {
				s = "\x1b[7m" + s + "\x1b[0m"
			}
			sb.WriteString(s)
		}
		return sb.String()
	}
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = headerLabel(c)
	}
	b.WriteString(line(headers, -1))
	b.WriteString("\n")
	sep := make([]string, len(cols))
	for i := range cols {
		sep[i] = strings.Repeat("─", widths[i])
	}
	b.WriteString(line(sep, -2))
	b.WriteString("\n")
	if startRow > len(rows) {
		startRow = len(rows)
	}
	end := min(startRow+maxRows, len(rows))
	for i := startRow; i < end; i++ {
		cells := make([]string, len(cols))
		for j := range cols {
			if j < len(rows[i]) {
				cells[j] = cellString(rows[i][j])
			} else {
				cells[j] = ""
			}
		}
		b.WriteString(line(cells, i))
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

// stBadge 渲染语句类型徽标（着色）。
func stBadge(t protocol.StatementType) string {
	styles := map[protocol.StatementType]int{
		protocol.StatementTypeSelect: 2, // 绿
		protocol.StatementTypeInsert: 6, // 青
		protocol.StatementTypeUpdate: 6, // 青
		protocol.StatementTypeDelete: 6, // 青
		protocol.StatementTypeDDL:    3, // 黄
		protocol.StatementTypeOther:  8, // 灰
	}
	if t == "" {
		return "其他"
	}
	if c, ok := styles[t]; ok {
		return fmt.Sprintf("\x1b[%dm%s\x1b[0m", 30+c, strings.ToUpper(string(t)))
	}
	return strings.ToUpper(string(t))
}
