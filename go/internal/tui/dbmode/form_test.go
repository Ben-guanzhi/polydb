package dbmode

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tui/backend/fake"
	"github.com/polydb/polydb/pkg/protocol"
)

func TestConnFormFieldsPerKind(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want []string
	}{
		{KindMysql, []string{fkUser, fkPassword, fkHost, fkPort, fkDatabase}},
		{KindPostgres, []string{fkUser, fkPassword, fkHost, fkPort, fkDatabase, fkSslMode}},
		{KindRedis, []string{fkUser, fkPassword, fkHost, fkPort, fkDbNum}},
	} {
		f := newConnForm(nil, tc.kind)
		if len(f.fields) != len(tc.want) {
			t.Fatalf("%s: %d fields, want %d", tc.kind, len(f.fields), len(tc.want))
		}
		for i, k := range tc.want {
			if f.fields[i].key != k {
				t.Errorf("%s field %d = %q, want %q", tc.kind, i, f.fields[i].key, k)
			}
		}
	}
}

// TestConnFormDefaultPorts pins the fresh-form default ports per kind.
func TestConnFormDefaultPorts(t *testing.T) {
	for kind, want := range map[string]string{
		KindMysql: "3306", KindPostgres: "5432", KindRedis: "6379",
		KindSqlServer: "1433", KindOracle: "1521",
	} {
		f := newConnForm(nil, kind)
		got := f.values()
		if got[fkPort] != want {
			t.Errorf("%s default port = %q, want %q", kind, got[fkPort], want)
		}
	}
	// sqlite has no host/port at all: its only field is the file path.
	f := newConnForm(nil, KindSQLite)
	if len(f.fields) != 1 || f.fields[0].key != fkDatabase {
		t.Fatalf("sqlite fields = %+v, want a single file-path field", f.fields)
	}
}

func TestConnFormNavigationAndEditing(t *testing.T) {
	f := newConnForm(nil, KindMysql)

	// tab / down move forward, wrapping.
	f.Update(keyPress("tab"))
	if f.focus != 1 {
		t.Fatalf("focus after tab = %d, want 1", f.focus)
	}
	f.Update(keyPress("down"))
	if f.focus != 2 {
		t.Fatalf("focus after down = %d, want 2", f.focus)
	}
	// shift+tab / up move backward, wrapping past the first field.
	f.Update(keyPress("shift+tab"))
	f.Update(keyPress("up"))
	f.Update(keyPress("up"))
	if want := len(f.fields) - 1; f.focus != want {
		t.Fatalf("focus after wrap = %d, want %d", f.focus, want)
	}

	// printable keys edit the focused field, backspace deletes.
	f.focus = 1
	typeText(t, f, "abc")
	if got := f.fields[1].input.String(); got != "abc" {
		t.Fatalf("typed value = %q, want abc", got)
	}
	f.Update(keyPress("backspace"))
	if got := f.fields[1].input.String(); got != "ab" {
		t.Fatalf("after backspace = %q, want ab", got)
	}
	// ctrl+u clears the focused field.
	f.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if got := f.fields[1].input.String(); got != "" {
		t.Fatalf("after ctrl+u = %q, want empty", got)
	}

	// password field renders masked.
	f.fields[1].input = tui.NewLineInput("hunter2")
	view := f.View(80, 24, testTheme())
	if strings.Contains(view, "hunter2") {
		t.Fatal("password rendered in clear text")
	}
	if !strings.Contains(view, "*******") {
		t.Fatal("password not masked with asterisks")
	}
}

// TestConnFormEnterConnectsFromAnyField covers the key UX rule: enter always
// attempts a connection immediately, never moves focus.
func TestConnFormEnterConnectsFromAnyField(t *testing.T) {
	for _, focus := range []int{0, 2, 4} {
		created := false
		cli := &fake.Client{
			CreateConnectionFunc: func(req *protocol.CreateConnectionRequest) (protocol.ConnectionInfo, error) {
				created = true
				return protocol.ConnectionInfo{ID: "c1", Name: req.Name, Kind: req.Kind}, nil
			},
			ConnectFunc: func(ctx context.Context, id string) error {
				return errors.New("refused")
			},
		}
		ctx := &fakeCtx{cli: cli}

		f := newConnForm(ctx, KindMysql)
		f.focus = focus
		_, cmd := f.Update(keyPress("enter"))
		if cmd == nil {
			t.Fatalf("enter on field %d did not start the connect", focus)
		}
		if f.focus != focus {
			t.Fatalf("enter moved focus from %d to %d", focus, f.focus)
		}
		if !f.connecting {
			t.Fatalf("enter on field %d did not flip the connecting state", focus)
		}
		msg := runCmd(cmd)
		done, ok := msg.(connDoneMsg)
		if !ok {
			t.Fatalf("connect cmd produced %T, want connDoneMsg", msg)
		}
		if !created {
			t.Fatalf("enter on field %d never created the connection", focus)
		}
		if done.err == nil || !strings.Contains(done.err.Error(), "refused") {
			t.Fatalf("connect error = %v, want the refused error surfaced", done.err)
		}
	}
}

func TestConnFormPasteIntoFocusedField(t *testing.T) {
	f := newConnForm(nil, KindMysql)
	f.focus = 2 // host

	_, cmd := f.Update(tea.PasteMsg{Content: "db-1.internal\r\ndb-2.internal\n"})
	if cmd != nil {
		t.Fatal("paste must not emit commands")
	}
	if got := f.fields[2].input.String(); got != "db-1.internaldb-2.internal" {
		t.Fatalf("pasted value = %q, want newlines stripped", got)
	}

	// Paste appends to what is already typed, into the focused field only.
	// Other fields keep whatever they held before the paste (fresh forms
	// carry default ports, so compare against the pre-paste snapshot).
	before := make([]string, len(f.fields))
	for i := range f.fields {
		before[i] = f.fields[i].input.String()
	}
	f.Update(tea.PasteMsg{Content: ":3306"})
	if got := f.fields[2].input.String(); got != "db-1.internaldb-2.internal:3306" {
		t.Fatalf("second paste = %q, want appended", got)
	}
	for i := range f.fields {
		if i != 2 && f.fields[i].input.String() != before[i] {
			t.Fatalf("paste leaked into field %d (%q)", i, f.fields[i].input.String())
		}
	}
}

// TestConnFormSave pins ctrl+s: it saves the connection through the
// transport client and toasts, without connecting.
func TestConnFormSave(t *testing.T) {
	var gotHost string
	cli := &fake.Client{
		CreateConnectionFunc: func(req *protocol.CreateConnectionRequest) (protocol.ConnectionInfo, error) {
			gotHost = req.Host
			return protocol.ConnectionInfo{ID: "c1"}, nil
		},
	}
	f := newConnForm(&fakeCtx{cli: cli}, KindMysql)
	for i := range f.fields {
		if f.fields[i].key == fkHost {
			f.fields[i].input = tui.NewLineInput("db.example.internal")
		}
	}

	_, cmd := f.Update(keyPress("ctrl+s"))
	msg := runCmd(cmd)
	if _, ok := msg.(tui.ToastMsg); !ok {
		t.Fatalf("ctrl+s produced %T, want ToastMsg", msg)
	}
	if gotHost != "db.example.internal" {
		t.Fatalf("saved host = %q, want db.example.internal", gotHost)
	}
}

// TestConnFormEscCloses pins the page-stack rule: the form is a page over
// the picker, so esc closes the form (and returns to the picker) whether or
// not a live connection exists.
func TestConnFormEscCloses(t *testing.T) {
	for name, f := range map[string]*connForm{
		"not connected": newConnForm(&fakeCtx{}, KindMysql),
		"connected kv":  newConnForm(&fakeCtx{kv: &fakeKV{}}, KindRedis),
		"connected sql": newConnForm(&fakeCtx{be: &fakeBackend{}}, KindMysql),
	} {
		_, cmd := f.Update(keyPress("esc"))
		if _, ok := runCmd(cmd).(tui.CloseOverlayMsg); !ok {
			t.Errorf("%s: esc on the connection form must close it", name)
		}
	}
}

// TestConnFormFullscreenPage: the form renders as a full page (a centered
// box over a blanked body), not a floating popup.
func TestConnFormFullscreenPage(t *testing.T) {
	var ov tui.Overlay = newConnForm(&fakeCtx{}, KindMysql)
	fs, ok := ov.(tui.FullscreenOverlay)
	if !ok || !fs.Fullscreen() {
		t.Fatal("connection form must be a fullscreen page overlay")
	}
}

// TestConnFormFooterHint: the footer advertises the picker as esc's target.
func TestConnFormFooterHint(t *testing.T) {
	for name, f := range map[string]*connForm{
		"not connected": newConnForm(&fakeCtx{}, KindMysql),
		"connected":     newConnForm(&fakeCtx{kv: &fakeKV{}}, KindRedis),
	} {
		v := f.View(80, 24, testTheme())
		if !strings.Contains(v, "esc back") {
			t.Errorf("%s: footer hint missing %q", name, "esc back")
		}
	}
}

// TestConnFormConnectSuccess drives the full asynchronous connect flow
// against a fake transport client.
func TestConnFormConnectSuccess(t *testing.T) {
	created := false
	cli := &fake.Client{
		CreateConnectionFunc: func(req *protocol.CreateConnectionRequest) (protocol.ConnectionInfo, error) {
			created = true
			if req.Host != "db.internal" {
				t.Fatalf("created host %q, want db.internal", req.Host)
			}
			return protocol.ConnectionInfo{ID: "c1", Name: "db.internal", Kind: req.Kind}, nil
		},
		ConnectFunc: func(ctx context.Context, id string) error { return nil },
	}

	f := newConnForm(&fakeCtx{cli: cli}, KindMysql)
	for i := range f.fields {
		if f.fields[i].key == fkHost {
			f.fields[i].input = tui.NewLineInput("db.internal")
		}
	}

	// enter starts the async connect from any field.
	_, cmd := f.Update(keyPress("enter"))
	if cmd == nil {
		t.Fatal("enter did not start the connect")
	}
	if !f.connecting {
		t.Fatal("form not in connecting state")
	}
	// A second request while in flight is ignored.
	if _, again := f.Update(keyPress("enter")); again != nil {
		t.Fatal("second connect while in flight must be ignored")
	}

	msg := runCmd(cmd)
	done, ok := msg.(connDoneMsg)
	if !ok {
		t.Fatalf("connect produced %T, want connDoneMsg", msg)
	}
	if done.err != nil {
		t.Fatalf("connect failed: %v", done.err)
	}
	if !created {
		t.Fatal("connect never created the connection")
	}
	if done.be == nil || done.kv != nil {
		t.Fatalf("connect: be=%v kv=%v, want the SQL adapter only", done.be, done.kv)
	}

	// Feeding the success message yields the follow-up sequence (close form,
	// attach backend, open schema, toast).
	_, cmd = f.Update(done)
	if f.connecting {
		t.Fatal("still connecting after success")
	}
	if cmd == nil {
		t.Fatal("success produced no follow-up commands")
	}
	cmds := f.successCmds(done)
	if len(cmds) != 4 {
		t.Fatalf("successCmds returned %d commands, want 4", len(cmds))
	}
	if _, ok := runCmd(cmds[0]).(tui.CloseOverlayMsg); !ok {
		t.Fatal("first success command must close the form")
	}
	sb, ok := runCmd(cmds[1]).(tui.SetBackendMsg)
	if !ok || sb.Backend == nil {
		t.Fatal("second success command must attach the backend")
	}
	rc, ok := runCmd(cmds[2]).(tui.RunCommandMsg)
	if !ok || rc.Name != "schema" {
		t.Fatal("third success command must open the schema browser")
	}
	if _, ok := runCmd(cmds[3]).(tui.ToastMsg); !ok {
		t.Fatal("fourth success command must toast")
	}
}

// TestConnFormConnectFailure checks the inline-error path: the form stays
// open and shows the error.
func TestConnFormConnectFailure(t *testing.T) {
	cli := &fake.Client{
		CreateConnectionFunc: func(req *protocol.CreateConnectionRequest) (protocol.ConnectionInfo, error) {
			return protocol.ConnectionInfo{}, errors.New("connection refused")
		},
	}

	f := newConnForm(&fakeCtx{cli: cli}, KindMysql)
	_, cmd := f.Update(keyPress("enter"))
	if cmd == nil {
		t.Fatal("enter did not start the connect")
	}
	done, ok := runCmd(cmd).(connDoneMsg)
	if !ok || done.err == nil {
		t.Fatalf("expected failing connDoneMsg, got %#v", done)
	}

	ov, cmd := f.Update(done)
	if cmd != nil {
		t.Fatal("failure must not emit follow-up commands")
	}
	if ov != tui.Overlay(f) {
		t.Fatal("failure must keep the form open")
	}
	if f.errText == "" {
		t.Fatal("failure did not set the inline error")
	}
	view := f.View(80, 24, testTheme())
	if !strings.Contains(view, strings.Split(f.errText, " ")[0]) {
		t.Fatal("inline error not rendered")
	}
}

func TestConnFormEscIgnoredWhileConnecting(t *testing.T) {
	f := newConnForm(&fakeCtx{}, KindMysql)
	f.connecting = true
	// Dismissing the form mid-dial would drop the connDoneMsg (orphaning the
	// new backend after the app closed the old one) and allow a second
	// concurrent dial; esc must be a no-op until the dial resolves.
	_, cmd := f.Update(keyPress("esc"))
	if cmd != nil {
		t.Fatal("esc while connecting must be ignored")
	}
}

func TestConnFormPasswordKeepsWhitespace(t *testing.T) {
	f := newConnForm(nil, KindMysql)
	for i := range f.fields {
		switch f.fields[i].key {
		case fkPassword:
			f.fields[i].input = tui.NewLineInput("  p@ss ")
		case fkUser:
			f.fields[i].input = tui.NewLineInput("  alice ")
		}
	}
	v := f.values()
	if v[fkPassword] != "  p@ss " {
		t.Errorf("password = %q, want whitespace preserved", v[fkPassword])
	}
	if v[fkUser] != "alice" {
		t.Errorf("user = %q, want trimmed", v[fkUser])
	}
}
