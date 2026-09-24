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

// formModel 是新建/编辑连接表单：Tab/↑↓ 切换字段，←/→ 切换类型，
// Enter 到最后一项提交，Esc 返回。editing 为真时预填自已有连接，且类型不可改。
type formModel struct {
	fields  []formField
	focus   int
	editing bool
	connID  string
	// kindLocked 禁止 ←/→ 改类型：UpdateConnectionRequest 里没有 kind 字段。
	kindLocked bool
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

// newEditForm 用已有连接预填编辑表单。
// 密码类字段一律留空：协议不回读明文，app-core 对空密码保留原 keyring 引用。
// SSH 私钥口令/密码同理；隧道本身无法通过更新请求清除（SSHTunnel=nil 表示不改）。
func newEditForm(c protocol.ConnectionInfo) formModel {
	f := newForm()
	f.editing = true
	f.connID = c.ID
	f.kindLocked = true
	set := func(i int, v string) { f.fields[i].input.SetValue(v) }
	set(0, c.Name)
	set(1, string(c.Kind))
	set(2, c.Host)
	set(3, strconv.Itoa(c.Port))
	set(4, c.Database)
	set(5, c.Username)
	f.fields[6].input.Placeholder = "留空保持不变"
	if t := c.SSHTunnel; t != nil {
		set(7, t.Host)
		set(8, strconv.Itoa(t.Port))
		set(9, t.Username)
		set(10, t.PrivateKeyPath)
		f.fields[11].input.Placeholder = "留空保持不变"
		f.fields[12].input.Placeholder = "留空保持不变"
	}
	f.setFocus(0)
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
	if f.kindLocked {
		return
	}
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

// updateRequest 组装更新请求。只下发表单里可见的字段：
// ReadOnly / Group / Color / DefaultSchema / Options 表单没有对应输入，
// 保持 nil 即「不修改」（storage 的逐字段 partial update 语义）。
// 密码留空 → app-core 沿用原 password_ref；SSH 主机留空 → 不动现有隧道。
func (f formModel) updateRequest() protocol.UpdateConnectionRequest {
	val := func(i int) string { return strings.TrimSpace(f.fields[i].input.Value()) }
	strp := func(s string) *string { return &s }
	port, _ := strconv.Atoi(val(3))
	database := val(4)
	if f.kind() == protocol.DatabaseKindSQLite && database == "" {
		database = ":memory:"
	}
	req := protocol.UpdateConnectionRequest{
		Name:     strp(val(0)),
		Host:     strp(val(2)),
		Port:     &port,
		Database: strp(database),
		Username: strp(val(5)),
		Password: val(6),
	}
	if f.kind() != protocol.DatabaseKindSQLite && val(7) != "" {
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
	if f.editing {
		b.WriteString("编辑连接（Tab/↑↓ 切换字段，Enter 到最后一项保存，Esc 返回）\n\n")
	} else {
		b.WriteString("新建连接（Tab/↑↓ 切换字段，Enter 到最后一项提交，Esc 返回）\n\n")
	}
	for i, fd := range f.fields {
		marker := "  "
		if i == f.focus {
			marker = "▸ "
		}
		b.WriteString(marker + fd.label + "  ")
		switch {
		case fd.cycle && f.kindLocked:
			b.WriteString(string(f.kind()) + "  （类型不可修改）\n")
		case fd.cycle:
			b.WriteString(string(f.kind()) + "  ←/→ 切换\n")
		default:
			b.WriteString(fd.input.View() + "\n")
		}
	}
	if f.kind() == protocol.DatabaseKindSQLite {
		b.WriteString("\n提示：SQLite 的「数据库」为文件路径，:memory: 为临时库。")
	}
	if f.editing {
		b.WriteString("\n提示：密码留空即保持原值；只读标记 / 分组 / 配色不在表单内，编辑时不动。")
	}
	return b.String()
}
