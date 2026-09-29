package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// queryHistoryLimit 抽屉展示 / 落盘保留的最大条数。
const queryHistoryLimit = 200

// histEntry 一条查询历史（记录实际执行过的 SQL，对齐 Web 侧本地历史语义，独立实现）。
type histEntry struct {
	Time int64  `json:"time"` // unix 秒
	Conn string `json:"conn"` // 连接 id（短显用）
	SQL  string `json:"sql"`
}

// dataDirFn 解析历史 JSONL 所在的数据目录。TUI 不再直接 import pkg/storage
// （分层约束：UI 层经由组合根注入解析器）；由 main.go 传入 storage.DataDir。
var dataDirFn func() string

// SetDataDirFunc 注入数据目录解析器（组合根调用）；每次取路径时调用，
// 保持与直接调 storage.DataDir() 相同的 POLYDB_DATA_DIR 动态覆盖语义。
func SetDataDirFunc(f func() string) { dataDirFn = f }

// historyPath 历史 JSONL 路径：<dataDir>/query_history.jsonl。
func historyPath() string {
	dir := "."
	if dataDirFn != nil {
		dir = dataDirFn()
	}
	return filepath.Join(dir, "query_history.jsonl")
}

// appendHistory 追加一条历史并保留末尾 queryHistoryLimit 条（读回截断重写）。
func appendHistory(e histEntry) error {
	p := historyPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	items, err := loadHistory()
	if err != nil {
		items = nil
	}
	// 与上一条完全相同则不重复记录。
	if n := len(items); n > 0 && items[n-1].SQL == e.SQL && items[n-1].Conn == e.Conn {
		return nil
	}
	items = append(items, e)
	if len(items) > queryHistoryLimit {
		items = items[len(items)-queryHistoryLimit:]
	}
	var b strings.Builder
	for _, it := range items {
		line, err := json.Marshal(it)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return os.WriteFile(p, []byte(b.String()), 0o600)
}

// loadHistory 读取全部历史（旧→新）。文件不存在返回空切片。
func loadHistory() ([]histEntry, error) {
	f, err := os.Open(historyPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []histEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e histEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // 跳过损坏行
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// openHistory 载入历史并打开抽屉（最新在前，光标置顶）。
func (m *model) openHistory() (tea.Model, tea.Cmd) {
	items, err := loadHistory()
	if err != nil {
		m.status = "读取历史失败: " + err.Error()
		return m, nil
	}
	// 反转为最新在前。
	rev := make([]histEntry, 0, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		rev = append(rev, items[i])
	}
	m.hist = rev
	m.histCur = 0
	m.histOpen = true
	return m, nil
}

// historyKey 抽屉键位：↑/↓ 选择 · Enter 回填编辑器 · Esc/F4 关闭。
func (m *model) historyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEsc, tea.KeyF4:
		m.histOpen = false
		return m, nil
	case tea.KeyUp:
		if m.histCur > 0 {
			m.histCur--
		}
	case tea.KeyDown:
		if m.histCur < len(m.hist)-1 {
			m.histCur++
		}
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k":
			if m.histCur > 0 {
				m.histCur--
			}
		case "j":
			if m.histCur < len(m.hist)-1 {
				m.histCur++
			}
		}
	case tea.KeyEnter:
		if m.histCur < len(m.hist) {
			m.histOpen = false
			return m.gotoQuery(m.hist[m.histCur].SQL)
		}
	}
	return m, nil
}

// historyView 渲染历史抽屉（覆盖于查询视图）：最新在前，光标高亮，SQL 单行截断。
func (m *model) historyView() string {
	var b strings.Builder
	b.WriteString(selected(" 查询历史 "))
	b.WriteString(fmt.Sprintf("（%d 条 · Enter 回填 · Esc 关闭）", len(m.hist)))
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", min(m.width, 60)) + "\n")
	if len(m.hist) == 0 {
		b.WriteString("（暂无历史）")
		return b.String()
	}
	avail := m.contentLines()
	start, end := listWindow(len(m.hist), m.histCur, avail)
	for i := start; i < end; i++ {
		e := m.hist[i]
		marker := "  "
		if i == m.histCur {
			marker = "▸ "
		}
		ts := time.Unix(e.Time, 0).Format("01-02 15:04")
		oneLine := strings.Join(strings.Fields(e.SQL), " ")
		if len([]rune(oneLine)) > 80 {
			oneLine = string([]rune(oneLine)[:80]) + "…"
		}
		line := fmt.Sprintf("%s%s · %s  %s", marker, ts, shortConn(e.Conn), oneLine)
		if i == m.histCur {
			line = selected(line)
		}
		b.WriteString(line + "\n")
	}
	if end < len(m.hist) {
		b.WriteString(fmt.Sprintf("↓ 下方还有 %d", len(m.hist)-end))
	}
	return b.String()
}

// shortConn 连接 id 前 8 字符短显（与标题栏一致）。
func shortConn(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
