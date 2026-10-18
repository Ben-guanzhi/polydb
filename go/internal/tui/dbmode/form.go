// Package dbmode wires the live-database mode into the UI: the connection
// picker and form, schema browsing, table loading and the Redis key browser,
// all through the shared overlay system. Connections are created and opened
// through transport.Client (AGENTS.md 铁律 2).
package dbmode

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/pkg/protocol"
)

// Engine kinds accepted by the form (mirrors protocol.DatabaseKind).
const (
	KindMysql     = "mysql"
	KindPostgres  = "postgres"
	KindRedis     = "redis"
	KindSqlServer = "sqlserver"
	KindOracle    = "oracle"
	KindSQLite    = "sqlite"
)

// Field keys shared by field construction and request building.
const (
	fkUser     = "user"
	fkPassword = "password"
	fkHost     = "host"
	fkPort     = "port"
	fkDatabase = "database"
	fkSslMode  = "sslmode"
	fkDbNum    = "dbnum"
)

// connField is one editable line of the connection form. The LineInput is
// used append-only: the cursor never moves off the end, so Backspace/Clear/
// InsertKey behave as "delete last rune / wipe / append".
type connField struct {
	key    string
	label  string
	masked bool
	input  tui.LineInput
}

// connDoneMsg reports the outcome of an asynchronous connection attempt.
type connDoneMsg struct {
	be  *backend.Adapter
	kv  backend.KVBackend
	err error
}

// connForm is the connection form overlay, parameterized by engine kind.
// enter connects asynchronously; ctrl+s saves without connecting.
type connForm struct {
	ctx        tui.AppContext
	kind       string
	fields     []connField
	focus      int
	errText    string
	connecting bool
	// editing is non-nil when the form edits a saved connection
	// (prefilled from it; submit updates instead of creating).
	editing *protocol.ConnectionInfo
}

func newConnForm(ctx tui.AppContext, kind string) *connForm {
	return &connForm{ctx: ctx, kind: kind, fields: loadFields(kind, nil)}
}

// newEditForm opens the form prefilled from a saved connection.
func newEditForm(ctx tui.AppContext, info protocol.ConnectionInfo) *connForm {
	return &connForm{ctx: ctx, kind: string(info.Kind), fields: loadFields(string(info.Kind), &info), editing: &info}
}

// loadFields builds the field list for one kind. prefill (when non-nil)
// seeds the inputs from a saved connection.
func loadFields(kind string, prefill *protocol.ConnectionInfo) []connField {
	spec, ok := engineSpecs[kind]
	if !ok {
		return nil
	}
	var vals map[string]string
	if prefill != nil {
		vals = map[string]string{
			fkUser:     prefill.Username,
			fkPassword: "",
			fkHost:     prefill.Host,
			fkPort:     strconv.Itoa(prefill.Port),
			fkDatabase: prefill.Database,
			fkSslMode:  prefill.Options["sslmode"],
			fkDbNum:    prefill.Options["dbnum"],
		}
	}
	fields := make([]connField, len(spec.fields))
	for i, s := range spec.fields {
		v := ""
		if vals != nil {
			v = vals[s.key]
		} else if s.key == fkPort && spec.defaultPort != "" {
			v = spec.defaultPort
		}
		fields[i] = connField{key: s.key, label: s.label, masked: s.masked, input: tui.NewLineInput(v)}
	}
	return fields
}

// fieldSpec is one row of the engineSpecs table: the form field key, display
// label and whether the input is masked.
type fieldSpec struct {
	key    string
	label  string
	masked bool
}

// engineSpec declares, for one engine kind, the connection form fields in
// display order and the default port for a fresh form.
type engineSpec struct {
	fields      []fieldSpec
	defaultPort string
}

func fieldsFor(tail ...fieldSpec) []fieldSpec {
	all := make([]fieldSpec, 0, len(baseFields)+len(tail))
	all = append(all, baseFields...)
	return append(all, tail...)
}

// baseFields is the common field head, in display order.
var baseFields = []fieldSpec{
	{key: fkUser, label: "user"},
	{key: fkPassword, label: "password", masked: true},
	{key: fkHost, label: "host"},
	{key: fkPort, label: "port"},
}

var engineSpecs = map[string]engineSpec{
	KindMysql:     {fields: fieldsFor(fieldSpec{key: fkDatabase, label: "database"}), defaultPort: "3306"},
	KindPostgres:  {fields: fieldsFor(fieldSpec{key: fkDatabase, label: "database"}, fieldSpec{key: fkSslMode, label: "sslmode"}), defaultPort: "5432"},
	KindRedis:     {fields: fieldsFor(fieldSpec{key: fkDbNum, label: "db number"}), defaultPort: "6379"},
	KindSqlServer: {fields: fieldsFor(fieldSpec{key: fkDatabase, label: "database"}), defaultPort: "1433"},
	KindOracle:    {fields: fieldsFor(fieldSpec{key: fkDatabase, label: "service name"}), defaultPort: "1521"},
	KindSQLite:    {fields: []fieldSpec{{key: fkDatabase, label: "file path"}}, defaultPort: ""},
}

// values snapshots the current field contents keyed by field key. Masked
// fields (passwords) are passed through verbatim: credentials may
// legitimately contain leading/trailing whitespace.
func (o *connForm) values() map[string]string {
	m := make(map[string]string, len(o.fields))
	for _, fl := range o.fields {
		s := fl.input.String()
		if !fl.masked {
			s = strings.TrimSpace(s)
		}
		m[fl.key] = s
	}
	return m
}

// request builds the protocol request for the current field values.
func (o *connForm) request() *protocol.CreateConnectionRequest {
	v := o.values()
	port, _ := strconv.Atoi(v[fkPort])
	opts := map[string]string{}
	if v[fkSslMode] != "" {
		opts["sslmode"] = v[fkSslMode]
	}
	if v[fkDbNum] != "" {
		opts["dbnum"] = v[fkDbNum]
	}
	if len(opts) == 0 {
		opts = nil
	}
	name := v[fkHost]
	if v[fkDatabase] != "" {
		name += "/" + v[fkDatabase]
	}
	return &protocol.CreateConnectionRequest{
		Name:     name,
		Kind:     protocol.DatabaseKind(o.kind),
		Host:     v[fkHost],
		Port:     port,
		Database: v[fkDatabase],
		Username: v[fkUser],
		Password: v[fkPassword],
		Options:  opts,
	}
}

// Fullscreen marks the form as a full page (tui.FullscreenOverlay): a
// centered box over a fully blanked body. It is the root page of the
// database-mode page stack (login -> browser -> table).
func (o *connForm) Fullscreen() bool { return true }

// startConnect flips into the connecting state and returns the asynchronous
// connect command: create/update the saved connection, open it, and wrap it
// in a backend.Adapter. A second request while one is in flight is ignored.
func (o *connForm) startConnect() tea.Cmd {
	if o.connecting {
		return nil
	}
	o.connecting = true
	o.errText = ""
	cli := o.ctx.Client()
	req := o.request()
	editing := o.editing
	return func() tea.Msg {
		var info protocol.ConnectionInfo
		var err error
		if editing != nil {
			upd := &protocol.UpdateConnectionRequest{
				Name: &req.Name, Host: &req.Host, Port: &req.Port,
				Database: &req.Database, Username: &req.Username,
				Password: req.Password, Options: req.Options,
			}
			info, err = cli.UpdateConnection(editing.ID, upd)
		} else {
			info, err = cli.CreateConnection(req)
		}
		if err != nil {
			return connDoneMsg{err: err}
		}
		if err := cli.Connect(context.Background(), info.ID); err != nil {
			return connDoneMsg{err: err}
		}
		ad := backend.NewAdapter(cli, info)
		var kv backend.KVBackend
		if info.Kind == protocol.DatabaseKindRedis {
			kv = ad
		}
		return connDoneMsg{be: ad, kv: kv}
	}
}

// successCmds is the ordered message sequence after a successful connect:
// close the form, attach the connection to the app, open the browser
// (schema for SQL engines, key browser via the wrapped schema factory for
// redis) and toast the connection title.
func (o *connForm) successCmds(m connDoneMsg) []tea.Cmd {
	return []tea.Cmd{
		tui.CloseOverlay,
		func() tea.Msg { return tui.SetBackendMsg{Backend: m.be, KV: m.kv} },
		func() tea.Msg { return tui.RunCommandMsg{Name: "schema"} },
		func() tea.Msg { return tui.ToastMsg{Text: "connected: " + m.be.Title()} },
	}
}

func (o *connForm) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	switch m := msg.(type) {
	case connDoneMsg:
		o.connecting = false
		if m.err != nil {
			o.errText = m.err.Error()
			return o, nil
		}
		return o, tea.Sequence(o.successCmds(m)...)

	case tea.PasteMsg:
		o.insertText(m.Content)
		return o, nil

	case tea.KeyPressMsg:
		return o.handleKey(m)
	}
	return o, nil
}

// insertText appends printable text to the focused field. Newlines (and other
// control characters) are stripped so multi-line pastes land as one value.
func (o *connForm) insertText(s string) {
	if len(o.fields) == 0 {
		return
	}
	o.fields[o.focus].input.InsertKey(s)
}

func (o *connForm) handleKey(key tea.KeyPressMsg) (tui.Overlay, tea.Cmd) {
	switch key.String() {
	case "esc":
		// While a connect is in flight the form must stay up: dismissing it
		// would drop the connDoneMsg (the fresh adapter replaces — and
		// closes — the one the app is still using).
		if o.connecting {
			return o, nil
		}
		// The form is the root page of the database-mode page stack: esc
		// closes it and returns to the picker / empty workspace.
		return o, tui.CloseOverlay
	case "tab", "down", "up", "shift+tab":
		if len(o.fields) == 0 {
			return o, nil
		}
		delta := 1
		if key.String() == "up" || key.String() == "shift+tab" {
			delta = -1
		}
		o.focus = (o.focus + delta + len(o.fields)) % len(o.fields)
		return o, nil
	case "enter":
		return o, o.startConnect()
	case "ctrl+s":
		// Save without connecting (best effort; errors surface as text).
		cli := o.ctx.Client()
		req := o.request()
		return o, func() tea.Msg {
			if o.editing != nil {
				upd := &protocol.UpdateConnectionRequest{
					Name: &req.Name, Host: &req.Host, Port: &req.Port,
					Database: &req.Database, Username: &req.Username,
					Password: req.Password, Options: req.Options,
				}
				if _, err := cli.UpdateConnection(o.editing.ID, upd); err != nil {
					return tui.ErrorMsg{Err: err}
				}
				return tui.ToastMsg{Text: "saved"}
			}
			if _, err := cli.CreateConnection(req); err != nil {
				return tui.ErrorMsg{Err: err}
			}
			return tui.ToastMsg{Text: "saved"}
		}
	case "backspace":
		if len(o.fields) > 0 {
			o.fields[o.focus].input.Backspace()
		}
		return o, nil
	case "ctrl+u":
		if len(o.fields) > 0 {
			o.fields[o.focus].input.Clear()
		}
		return o, nil
	default:
		if t := key.Text; t != "" {
			o.insertText(t)
		}
		return o, nil
	}
}

func (o *connForm) View(width, height int, th *theme.Theme) string {
	w := width - 4
	if w > 60 {
		w = 60
	}
	if w < 30 {
		w = 30
	}
	inner := w - 2

	labelW := 0
	for _, fl := range o.fields {
		if n := len(fl.label); n > labelW {
			labelW = n
		}
	}

	title := "connect " + o.kind
	if o.editing != nil {
		title = "edit " + o.editing.Name
	}

	var lines []string
	for i, fl := range o.fields {
		val := fl.input.String()
		if fl.masked {
			val = strings.Repeat("*", fl.input.Len())
		}
		label := fmt.Sprintf(" %-*s ", labelW, fl.label)
		room := inner - ansi.StringWidth(label) - 1
		if room < 1 {
			room = 1
		}
		// Keep the tail visible while typing long values.
		if r := []rune(val); len(r) > room {
			val = "…" + string(r[len(r)-room+1:])
		}
		if i == o.focus {
			cur := th.ListSelected.Render(" ")
			lines = append(lines, th.Text.Render(label)+th.Input.Render(val)+cur)
		} else {
			lines = append(lines, th.Subtle.Render(label)+th.Text.Render(val))
		}
	}

	lines = append(lines, "")
	switch {
	case o.connecting:
		lines = append(lines, th.Warning.Render(" connecting..."))
	case o.errText != "":
		for _, l := range strings.Split(ansi.Wrap(o.errText, inner-2, ""), "\n") {
			lines = append(lines, th.Error.Render(" "+l))
		}
	default:
		lines = append(lines, th.Placeholder.Render(" fill in the connection parameters"))
	}

	lines = append(lines, th.Placeholder.Render(" enter connect · tab/↑↓ move · ctrl+s save · esc back"))

	return tui.Box(title, strings.Join(lines, "\n"), w, th)
}

// compile-time interface checks
var (
	_ tui.Overlay           = (*connForm)(nil)
	_ tui.FullscreenOverlay = (*connForm)(nil)
)
