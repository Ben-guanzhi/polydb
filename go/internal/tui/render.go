// Package tui 提供 polydb 终端界面（bubbletea）。仅依赖 transport 与 protocol，
// 不接触任何 driver / appcore（AGENTS.md 铁律：前端只经 app-core 或 transport 访问数据）。
package tui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// blobRefOf 识别 spec/encoding.md §5.2 旁路引用值：返回长度与引用 UUID。
// 非 $blob、无 ref、前缀不符一律 ok=false（内联 base64 形状走 cellFullText 展示）。
func blobRefOf(v protocol.Value) (int64, string, bool) {
	if v.IsNull() {
		return 0, "", false
	}
	b, err := v.MarshalJSON()
	if err != nil {
		return 0, "", false
	}
	var outer struct {
		Blob *struct {
			Len *int64 `json:"len"`
			Ref string `json:"ref"`
		} `json:"$blob"`
	}
	if err := json.Unmarshal(b, &outer); err != nil || outer.Blob == nil ||
		outer.Blob.Len == nil || outer.Blob.Ref == "" {
		return 0, "", false
	}
	if !strings.HasPrefix(outer.Blob.Ref, protocol.BlobRefPrefix) {
		return 0, "", false
	}
	return *outer.Blob.Len, strings.TrimPrefix(outer.Blob.Ref, protocol.BlobRefPrefix), true
}

// blobFetchErrText 取回失败的展示文案：未知/过期用 §5.2 规定的前端固定文案，
// 其余（超时、INVALID_PARAM 等）原样透出便于排查。
func blobFetchErrText(err error) string {
	var pe *protocol.PolyDBError
	if errors.As(err, &pe) && pe.Code == protocol.ErrBlobNotFound {
		return "已过期，请重新执行查询"
	}
	return err.Error()
}

// chunkBase64 每 76 字符换行（PEM 风格），避免完整值面板出现超长单行。
func chunkBase64(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && i%76 == 0 {
			sb.WriteByte('\n')
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// blobFetchBlock 完整值面板的 §5.2 取回状态块：仅旁路引用值输出，其余返回空串。
// 状态与 blobKey 绑定，光标换到别的引用时回落「按 g 取回」。
func (m *model) blobFetchBlock(v protocol.Value) string {
	_, id, ok := blobRefOf(v)
	if !ok {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n── 取回（encoding.md §5.2）────────\n")
	switch {
	case m.blobLoading && m.blobKey == id:
		sb.WriteString("取回中…")
	case m.blobErr != "" && m.blobKey == id:
		sb.WriteString("取回失败：" + m.blobErr + " · g 重试")
	case m.blobData != nil && m.blobKey == id:
		fmt.Fprintf(&sb, "已取回 %d 字节 · base64：\n%s",
			len(m.blobData), chunkBase64(base64.StdEncoding.EncodeToString(m.blobData)))
	default:
		sb.WriteString("按 g 取回内容")
	}
	return sb.String()
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

// widthCache 缓存一次「全页值→文本扫描」的输出：钳制到 cellCap、尚未按 maxWidth 缩放的列宽。
//
// 键是结果集指针 + 行数 + 列数。TUI 从不原地改写 result.Rows[i][j]（暂存的编辑值走
// cellDecorator 覆盖显示，且不参与列宽），换页/换结果都会换成新指针 ⇒ 同键即同值。
// 行数在键里，是为了让「原地 append」这类改写必然失效；缓存本体持有该指针，最多滞留一页。
//
// 只缓存「未缩放」的列宽，因此同一份缓存可被不同 maxWidth / maxRows 的两次渲染共用
// （缩放/下限那两步是 Θ(列数)，每帧重跑）。
type widthCache struct {
	res   any
	rows  int
	cols  int
	width []int
}

// lookup 命中时返回缓存列宽的**副本**——调用方会就地按 maxWidth 缩放，不能改缓存本体。
func (c *widthCache) lookup(res any, rows, cols int) ([]int, bool) {
	if c == nil || c.width == nil || c.res != res || c.rows != rows || c.cols != cols {
		return nil, false
	}
	w := make([]int, len(c.width))
	copy(w, c.width)
	return w, true
}

func (c *widthCache) store(res any, rows, cols int, width []int) {
	if c == nil || res == nil {
		return // 没有身份就不存：res=nil 必须逐帧全量重算（见 renderTableDecoratedCached）
	}
	*c = widthCache{res: res, rows: rows, cols: cols, width: width}
}

// measureCellWidths 扫描全页求各列最宽文本（cellCap 钳制后、maxWidth 缩放前）。
func measureCellWidths(cols []protocol.ResultColumn, rows [][]protocol.Value) []int {
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
	return widths
}

// renderTable 表格渲染核心（无装饰）：见 renderTableDecorated。
func renderTable(cols []protocol.ResultColumn, rows [][]protocol.Value, maxWidth, maxRows, startRow, curRow, curCol int) string {
	return renderTableDecorated(cols, rows, maxWidth, maxRows, startRow, curRow, curCol, nil)
}

// renderTableDecorated 无缓存渲染：每次调用都重扫全页。见 renderTableDecoratedCached。
func renderTableDecorated(cols []protocol.ResultColumn, rows [][]protocol.Value, maxWidth, maxRows, startRow, curRow, curCol int, decor cellDecorator) string {
	return renderTableDecoratedCached(nil, nil, cols, rows, maxWidth, maxRows, startRow, curRow, curCol, decor)
}

// renderTableDecoratedCached 表格渲染核心：startRow 起窗口渲染 maxRows 行，
// (curRow, curCol) 单元格反显高亮（-1 表示无光标）；decor 非空时覆盖脏格显示并标色。
// cache 非空时按结果集身份复用全页列宽测量。
// res 必须是**该结果集的指针**（可比较的身份值，见 widthCache）；传切片会 panic。
// cache/res 任一为 nil 时退化为逐帧全量重算。
func renderTableDecoratedCached(cache *widthCache, res any, cols []protocol.ResultColumn, rows [][]protocol.Value, maxWidth, maxRows, startRow, curRow, curCol int, decor cellDecorator) string {
	if len(cols) == 0 {
		return "（无结果集）"
	}
	widths, ok := cache.lookup(res, len(rows), len(cols))
	if !ok {
		widths = measureCellWidths(cols, rows)
		cache.store(res, len(rows), len(cols), widths)
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
