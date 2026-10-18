package dbmode

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/pkg/protocol"
)

// newKinds lists the kinds offered by the "new connection" section of the
// picker, in display order.
var newKinds = []string{
	KindPostgres, KindMysql, KindSQLite, KindSqlServer, KindOracle, KindRedis,
}

// connPicker is the root overlay of database mode: it lists the saved
// connections (loaded through transport.Client) and offers the new-connection
// kinds below them. enter connects, e edits, d deletes, n jumps to the new
// section, r reloads.
type connPicker struct {
	ctx    tui.AppContext
	conns  []protocol.ConnectionInfo
	cursor int
	err    string
	// inNew is true when the cursor sits in the "new connection" section.
	inNew bool
}

func newConnPicker(ctx tui.AppContext) *connPicker {
	return &connPicker{ctx: ctx}
}

// Fullscreen marks the picker as a full page.
func (o *connPicker) Fullscreen() bool { return true }

// connsLoadedMsg delivers the saved-connection list (or the load error).
type connsLoadedMsg struct {
	conns []protocol.ConnectionInfo
	err   error
}

// connOpenedMsg reports the outcome of opening a saved connection.
type connOpenedMsg struct {
	be  *backend.Adapter
	kv  backend.KVBackend
	err error
}

// connDeletedMsg reports the outcome of a delete.
type connDeletedMsg struct {
	id  string
	err error
}

func (o *connPicker) Init() tea.Cmd {
	cli := o.ctx.Client()
	if cli == nil {
		return nil
	}
	return func() tea.Msg {
		conns, err := cli.ListConnections()
		return connsLoadedMsg{conns: conns, err: err}
	}
}

func (o *connPicker) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	switch m := msg.(type) {
	case connsLoadedMsg:
		if m.err != nil {
			o.err = m.err.Error()
			return o, nil
		}
		o.conns = m.conns
		o.err = ""
		o.cursor = 0
		o.inNew = false
		return o, nil
	case connOpenedMsg:
		if m.err != nil {
			o.err = m.err.Error()
			return o, nil
		}
		return o, tea.Sequence(
			tui.CloseOverlay,
			func() tea.Msg { return tui.SetBackendMsg{Backend: m.be, KV: m.kv} },
			func() tea.Msg { return tui.RunCommandMsg{Name: "schema"} },
			func() tea.Msg { return tui.ToastMsg{Text: "connected: " + m.be.Title()} },
		)
	case connDeletedMsg:
		if m.err != nil {
			o.err = m.err.Error()
			return o, nil
		}
		o.err = ""
		return o, tea.Sequence(
			func() tea.Msg { return tui.ToastMsg{Text: "deleted"} },
			o.Init(),
		)
	case tea.KeyPressMsg:
		return o.handleKey(m)
	}
	return o, nil
}

func (o *connPicker) handleKey(key tea.KeyPressMsg) (tui.Overlay, tea.Cmd) {
	switch key.String() {
	case "esc", "q":
		return o, tui.CloseOverlay
	case "up", "k":
		o.move(-1)
		return o, nil
	case "down", "j":
		o.move(1)
		return o, nil
	case "n":
		o.inNew = true
		o.cursor = 0
		return o, nil
	case "r":
		o.err = ""
		return o, o.Init()
	case "enter":
		if o.inNew {
			kind := newKinds[o.cursor]
			return o, tea.Sequence(
				tui.CloseOverlay,
				func() tea.Msg { return tui.RunCommandMsg{Name: "newconn", Arg: kind} },
			)
		}
		if o.cursor < len(o.conns) {
			return o, o.openConn(o.conns[o.cursor])
		}
		return o, nil
	case "e":
		if !o.inNew && o.cursor < len(o.conns) {
			id := o.conns[o.cursor].ID
			return o, tea.Sequence(
				tui.CloseOverlay,
				func() tea.Msg { return tui.RunCommandMsg{Name: "editconn", Arg: id} },
			)
		}
		return o, nil
	case "d":
		if !o.inNew && o.cursor < len(o.conns) {
			id := o.conns[o.cursor].ID
			cli := o.ctx.Client()
			return o, func() tea.Msg {
				_, err := cli.DeleteConnection(id)
				return connDeletedMsg{id: id, err: err}
			}
		}
		return o, nil
	}
	return o, nil
}

// move shifts the cursor within the active section.
func (o *connPicker) move(delta int) {
	n := len(o.conns)
	if o.inNew {
		n = len(newKinds)
	}
	if n == 0 {
		if delta > 0 {
			o.inNew = true
			o.cursor = 0
		}
		return
	}
	o.cursor = (o.cursor + delta + n) % n
}

// openConn opens a saved connection and wraps it in an Adapter.
func (o *connPicker) openConn(info protocol.ConnectionInfo) tea.Cmd {
	cli := o.ctx.Client()
	return func() tea.Msg {
		if err := cli.Connect(context.Background(), info.ID); err != nil {
			return connOpenedMsg{err: err}
		}
		fresh, err := cli.GetConnection(info.ID)
		if err != nil {
			return connOpenedMsg{err: err}
		}
		ad := backend.NewAdapter(cli, fresh)
		var kv backend.KVBackend
		if fresh.Kind == protocol.DatabaseKindRedis {
			kv = ad
		}
		return connOpenedMsg{be: ad, kv: kv}
	}
}

func (o *connPicker) View(width, height int, th *theme.Theme) string {
	w := width - 4
	if w > 70 {
		w = 70
	}
	if w < 30 {
		w = 30
	}
	var lines []string
	lines = append(lines, th.PopupTitle.Render(" connections "))
	lines = append(lines, "")
	if len(o.conns) == 0 && o.err == "" {
		lines = append(lines, th.Subtle.Render("  (no saved connections)"))
	}
	for i, c := range o.conns {
		marker := "  "
		style := th.Text
		if !o.inNew && i == o.cursor {
			marker = "> "
			style = th.ListSelected
		}
		label := fmt.Sprintf("%s  [%s]", c.Name, c.Kind)
		if c.Host != "" {
			label += "  " + c.Host
		}
		lines = append(lines, style.Render(marker+label))
	}

	lines = append(lines, "")
	lines = append(lines, th.PopupTitle.Render(" new connection "))
	for i, k := range newKinds {
		marker := "  "
		style := th.Text
		if o.inNew && i == o.cursor {
			marker = "> "
			style = th.ListSelected
		}
		lines = append(lines, style.Render(marker+k))
	}

	if o.err != "" {
		lines = append(lines, "")
		for _, l := range strings.Split(o.err, "\n") {
			lines = append(lines, th.Error.Render(" "+l))
		}
	}

	lines = append(lines, "")
	lines = append(lines, th.Placeholder.Render(" enter open · n new · e edit · d delete · r reload · esc close"))

	return tui.Box("connect", strings.Join(lines, "\n"), w, th)
}

// compile-time interface checks
var (
	_ tui.Overlay           = (*connPicker)(nil)
	_ tui.FullscreenOverlay = (*connPicker)(nil)
	_ tui.OverlayIniter     = (*connPicker)(nil)
)

// NewConnectOverlay 导出连接选择器，供组合根在启动时压栈
//（对应 sqltui dbmode.Run 的 PushOverlay(newConnForm(...))）。
func NewConnectOverlay(ctx tui.AppContext) tui.Overlay {
	return newConnPicker(ctx)
}
