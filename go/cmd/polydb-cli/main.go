// polydb-cli：单机脚本化的最小 SQLite 客户端。
//
// 连接本地 SQLite 文件（默认 :memory:），执行一条 SQL，把结果以文本表格打到 stdout。
// 面向脚本化/一次跑：非交互，不支持多语句切分（后续可复用前端 splitSql 语义的 Go 端实现）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	// 注册 modernc.org/sqlite 驱动（dbsqlite.Open 只 sql.Open("sqlite", ...)）
	_ "modernc.org/sqlite"

	"github.com/polydb/polydb/pkg/dbsqlite"
	"github.com/polydb/polydb/pkg/protocol"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "polydb-cli:", err)
		os.Exit(1)
	}
}

func run(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("polydb-cli", flag.ContinueOnError)
	dbPath := fs.String("db", ":memory:", "SQLite 文件路径（默认 :memory:）")
	sqlArg := fs.String("e", "", "要执行的 SQL；缺省时取位置参数或 stdin")
	compact := fs.Bool("t", false, "紧凑表格输出")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sql := ""
	switch {
	case *sqlArg != "":
		sql = *sqlArg
	case fs.NArg() > 0:
		sql = fs.Arg(0)
	default:
		b, err := io.ReadAll(in)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		sql = string(b)
	}
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return fmt.Errorf("no SQL given (use -e, a positional arg, or pipe via stdin)")
	}

	ctx := context.Background()
	conn, err := dbsqlite.Open(ctx, *dbPath)
	if err != nil {
		return fmt.Errorf("open %q: %w", *dbPath, err)
	}

	res, err := conn.Execute(ctx, sql)
	closeErr := conn.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return printResult(out, res, *compact)
}

func printResult(out io.Writer, res *protocol.QueryResult, compact bool) error {
	if len(res.Columns) == 0 {
		_, err := fmt.Fprintf(out, "ok (%d row(s) affected)\n", res.AffectedRows)
		return err
	}

	// 表头
	header := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		header[i] = c.Name
	}
	// 单元格文本（NULL 显示为 NULL）
	text := make([][]string, len(res.Rows))
	for r := range res.Rows {
		text[r] = make([]string, len(res.Columns))
		for c := range res.Columns {
			text[r][c] = renderCell(res.Rows[r][c])
		}
	}

	// 列宽 = max(表头宽, 该列最宽单元格)
	width := make([]int, len(header))
	for i := range header {
		width[i] = len(header[i])
		for r := range text {
			if l := len(text[r][i]); l > width[i] {
				width[i] = l
			}
		}
		if compact {
			width[i] = minInt(width[i], 24)
		}
	}

	line := func(cells []string) string {
		var b strings.Builder
		for i, cell := range cells {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(pad(cell, width[i]))
		}
		return strings.TrimRight(b.String(), " ")
	}

	var buf strings.Builder
	buf.WriteString(line(header))
	buf.WriteByte('\n')
	total := 0
	for i := range width {
		total += width[i] + 3
	}
	buf.WriteString(strings.Repeat("-", total-3+2)) // 粗略分隔线
	buf.WriteByte('\n')
	for _, row := range text {
		buf.WriteString(line(row))
		buf.WriteByte('\n')
	}
	fmt.Fprintf(&buf, "(%d row(s))\n", len(text))
	_, err := io.WriteString(out, buf.String())
	return err
}

func renderCell(v protocol.Value) string {
	if v.IsNull() {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	s := string(b)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
