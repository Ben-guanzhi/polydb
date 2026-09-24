package tui

import (
	"strings"
)

// helpEntry 一条键位说明（终端里 ⌘ 系键映射为 Ctrl 系）。
type helpEntry struct{ key, desc string }

// helpSection 一组键位（按视图或全局归并）。
type helpSection struct {
	title   string
	entries []helpEntry
}

// globalHelp 返回所有视图通用的键位。
func globalHelp() []helpEntry {
	return []helpEntry{
		{"F1", "打开 / 关闭本帮助面板"},
		{"Ctrl+C", "退出"},
		{"Esc", "返回上一级 / 关闭当前面板"},
	}
}

// viewHelp 返回当前视图专属的键位表，与各自 footer/handler 保持一致。
func (m *model) viewHelp() []helpEntry {
	switch m.view {
	case viewConns:
		return []helpEntry{
			{"↑/↓ · k/j", "上下选择连接"},
			{"Enter", "连接并进入库表"},
			{"n", "新建连接"},
			{"t", "测试连接"},
			{"d", "删除（连按两次确认）"},
			{"r", "刷新列表"},
			{"q", "退出"},
		}
	case viewForm:
		return []helpEntry{
			{"Tab · Shift+Tab", "切换字段"},
			{"←/→", "切换连接类型"},
			{"Enter", "提交新建"},
			{"Esc", "返回连接列表"},
		}
	case viewTables:
		return []helpEntry{
			{"↑/↓", "选择表"},
			{"Enter", "查看表详情"},
			{"s", "切换 schema"},
			{"Ctrl+P", "搜索表（跨 schema Quick-open）"},
			{"q", "打开查询编辑器"},
			{"F7", "浏览表数据"},
			{"Esc", "返回连接列表"},
		}
	case viewTable:
		return []helpEntry{
			{"↑/↓", "切换表"},
			{"q", "查询该表"},
			{"F7", "浏览表数据"},
			{"Esc", "返回库表结构"},
		}
	case viewQuery:
		return []helpEntry{
			{"Tab", "补全表 / 列"},
			{"F5 · Ctrl+E", "执行查询"},
			{"F6", "导航结果"},
			{"Ctrl+←/→", "词级左右移动"},
			{"Esc", "返回库表结构"},
		}
	case viewKV:
		return []helpEntry{
			{"↑/↓", "选择键"},
			{"Enter", "查看键的值"},
			{"/", "按模式过滤"},
			{"g", "加载更多键"},
			{"b", "切换 db"},
			{"c", "命令行模式"},
			{"r", "刷新"},
			{"Esc", "返回连接列表"},
		}
	case viewResults:
		if m.cellFull {
			return []helpEntry{
				{"Enter", "刷新完整值"},
				{"Esc", "关闭完整值（再按返回查询）"},
			}
		}
		return []helpEntry{
			{"↑/↓/←/→", "移动单元格光标"},
			{"Enter", "查看完整值（JSON 自动美化）"},
			{"Esc", "返回查询编辑器"},
		}
	case viewBrowse:
		if m.brEditing {
			return []helpEntry{
				{"Tab · Shift+Tab", "切换字段（列/操作符/值）"},
				{"←/→", "调整当前列或操作符"},
				{"↑/↓", "循环切换列"},
				{"Enter", "应用过滤条件"},
				{"Esc", "取消编辑"},
			}
		}
		if m.brFull {
			return []helpEntry{
				{"Enter", "刷新完整值"},
				{"Esc", "关闭完整值（再按返回列表）"},
			}
		}
		return []helpEntry{
			{"↑/↓", "移动行"},
			{"←/→", "翻页"},
			{"Shift+←/→", "移动列"},
			{"Enter", "查看完整值"},
			{"/ · Tab", "进入过滤栏编辑"},
			{"s", "按当前列切换排序（升/降）"},
			{"c", "精确计数（COUNT(*)）"},
			{"o", "切换 AND / OR"},
			{"d", "删除末条过滤条件"},
			{"r", "刷新当前页"},
			{"Esc", "返回库表结构"},
		}
	case viewQuickOpen:
		return []helpEntry{
			{"输入", "即时过滤（大小写不敏感子串）"},
			{"↑/↓", "选择结果"},
			{"Enter", "打开选中的表"},
			{"Esc", "返回库表结构"},
		}
	}
	return nil
}

// helpView 渲染覆盖式快捷键帮助面板：全局面板 + 当前视图面板。
func (m *model) helpView() string {
	secs := []helpSection{
		{"当前视图 · " + viewTitles[m.view], m.viewHelp()},
		{"全局", globalHelp()},
	}
	keyWidth := 0
	for _, s := range secs {
		for _, e := range s.entries {
			if n := len([]rune(e.key)); n > keyWidth {
				keyWidth = n
			}
		}
	}
	var b strings.Builder
	b.WriteString(selected(" polydb TUI · 快捷键 "))
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", min(m.width, 60)) + "\n")
	for _, s := range secs {
		if len(s.entries) == 0 {
			continue
		}
		b.WriteString("\n" + warnText(s.title) + "\n")
		for _, e := range s.entries {
			pad := strings.Repeat(" ", keyWidth-len([]rune(e.key)))
			b.WriteString("  " + okText(e.key+pad) + "  " + e.desc + "\n")
		}
	}
	b.WriteString("\n按任意键关闭")
	return b.String()
}
