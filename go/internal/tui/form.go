package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/polydb/polydb/pkg/protocol"
)

var kindCycle = []protocol.DatabaseKind{
	protocol.DatabaseKindSQLite,
	protocol.DatabaseKindMySQL,
	protocol.DatabaseKindPostgres,
	protocol.DatabaseKindMSSQL,
	protocol.DatabaseKindOracle,
	protocol.DatabaseKindRedis,
}

type formField struct {
	label string
	input textinput.Model
	cycle bool
}

// formModel 是新建连接表单：Tab/↑↓ 切换字段，←/→ 切换类型，Enter 到最后一项提交，Esc 返回。
type formModel struct {
	fields []formField
	focus  int
}

func newForm() formModel {
	mk := func(label, placeholder, value string) formField {
		in := textinput.New()
		in.Placeholder = placeholder
		in.SetValue(value)
		return formField{label: label, input: in}
	}
	mkPassword := func(label, placeholder string) formField {
		in := textinput.New()
		in.Placeholder = placeholder
		in.EchoMode = textinput.EchoPassword
		return formField{label: label, input: in}
	}
	f := formModel{}
	f.fields = append(f.fields, mk("名称", "本地开发库", ""))
	kind := mk("类型", "", string(protocol.DatabaseKindSQLite))
	kind.cycle = true
	f.fields = append(f.fields, kind)
	f.fields = append(f.fields, mk("主机", "127.0.0.1", ""))
	f.fields = append(f.fields, mk("端口", "5432", ""))
	f.fields = append(f.fields, mk("数据库", ":memory:", ""))
	f.fields = append(f.fields, mk("用户名", "postgres", ""))
	f.fields = append(f.fields, mkPassword("密码", ""))
	f.fields = append(f.fields, mk("SSH 主机", "留空不使用隧道", ""))
	f.fields = append(f.fields, mk("SSH 端口", "22", ""))
	f.fields = append(f.fields, mk("SSH 用户名", "", ""))
	f.fields = append(f.fields, mk("私钥路径", "~/.ssh/id_ed25519", ""))
	f.fields = append(f.fields, mkPassword("SSH 密码", ""))
	f.fields = append(f.fields, mkPassword("私钥口令", ""))
	f.fields[0].input.Focus()
	return f
}

func (f formModel) kind() protocol.DatabaseKind {
	for i, k := range kindCycle {
		if string(k) == f.fields[1].input.Value() {
			return kindCycle[i]
		}
	}
	return protocol.DatabaseKindSQLite
}

func (f *formModel) cycleKind(delta int) {
	cur := string(f.kind())
	idx := 0
	for i, k := range kindCycle {
		if string(k) == cur {
			idx = i
			break
		}
	}
	next := (idx + delta + len(kindCycle)) % len(kindCycle)
	f.fields[1].input.SetValue(string(kindCycle[next]))
}

func (f *formModel) setFocus(i int) {
	for j := range f.fields {
		f.fields[j].input.Blur()
	}
	f.focus = (i + len(f.fields)) % len(f.fields)
	f.fields[f.focus].input.Focus()
}

// updateKey 处理导航与输入，返回是否提交（focus 在最后一项按 Enter）。
func (f formModel) updateKey(msg tea.KeyMsg) (formModel, bool) {
	submitted := false
	switch msg.Type {
	case tea.KeyTab, tea.KeyDown:
		f.setFocus(f.focus + 1)
	case tea.KeyShiftTab, tea.KeyUp:
		f.setFocus(f.focus - 1)
	case tea.KeyLeft, tea.KeyRight:
		if f.fields[f.focus].cycle {
			d := 1
			if msg.Type == tea.KeyLeft {
				d = -1
			}
			f.cycleKind(d)
			return f, false
		}
		var cmd tea.Cmd
		f.fields[f.focus].input, cmd = f.fields[f.focus].input.Update(msg)
		_ = cmd
	case tea.KeyEnter:
		if f.focus == len(f.fields)-1 {
			submitted = true
		} else {
			f.setFocus(f.focus + 1)
		}
	default:
		var cmd tea.Cmd
		f.fields[f.focus].input, cmd = f.fields[f.focus].input.Update(msg)
		_ = cmd
	}
	return f, submitted
}

// request 把表单内容组装为创建请求（端口留空按 0；SQLite 数据库空则 :memory:；
// SSH 主机留空则不启用隧道，SSH 密码/私钥口令为空则不下发）。
func (f formModel) request() protocol.CreateConnectionRequest {
	val := func(i int) string { return strings.TrimSpace(f.fields[i].input.Value()) }
	port, _ := strconv.Atoi(val(3))
	req := protocol.CreateConnectionRequest{
		Name:     val(0),
		Kind:     f.kind(),
		Host:     val(2),
		Port:     port,
		Database: val(4),
		Username: val(5),
	}
	req.Password = val(6)
	if req.Kind == protocol.DatabaseKindSQLite && req.Database == "" {
		req.Database = ":memory:"
	}
	if req.Kind != protocol.DatabaseKindSQLite && val(7) != "" {
		sshPort, _ := strconv.Atoi(val(8))
		if sshPort == 0 {
			sshPort = 22
		}
		req.SSHTunnel = &protocol.SshTunnelConfig{
			Host:                 val(7),
			Port:                 sshPort,
			Username:             val(9),
			PrivateKeyPath:       val(10),
			Password:             val(11),
			PrivateKeyPassphrase: val(12),
		}
	}
	return req
}

func (f formModel) view() string {
	var b strings.Builder
	b.WriteString("新建连接（Tab/↑↓ 切换字段，Enter 到最后一项提交，Esc 返回）\n\n")
	for i, fd := range f.fields {
		marker := "  "
		if i == f.focus {
			marker = "▸ "
		}
		b.WriteString(marker + fd.label + "  ")
		if fd.cycle {
			b.WriteString(string(f.kind()) + "  ←/→ 切换\n")
		} else {
			b.WriteString(fd.input.View() + "\n")
		}
	}
	if f.kind() == protocol.DatabaseKindSQLite {
		b.WriteString("\n提示：SQLite 的「数据库」为文件路径，:memory: 为临时库。")
	}
	return b.String()
}
