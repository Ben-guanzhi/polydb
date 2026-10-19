// Package popup contains the modal overlays (palette, query editor,
// exporter, ...) that plug into the app through the tui.Factories registry.
//
// This file holds the shared SQL-execution plumbing used by the query editor
// (queryeditor.go) and the inline select/filter/order prompts (prompts.go):
// routing between the live database backend and the embedded engine, the
// inline-prompt statement builders, and a small line-input + suggestion-list
// widget pair. Everything is unexported and prefixed "qx".
package popup

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/google/uuid"
	"github.com/polydb/polydb/internal/complete"
	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/changes"
	"github.com/polydb/polydb/pkg/protocol"
)

// qxExecInfo describes the outcome of a statement that returned no rows.
type qxExecInfo struct {
	rowsAffected int64
}

// qxRun executes sqlText through whichever facility the app has: a live
// database backend takes precedence (database mode), otherwise the embedded
// engine (file mode). Exactly one of frame/info is non-nil on success.
func qxRun(ctx tui.AppContext, sqlText string) (*tuidata.Frame, *qxExecInfo, error) {
	be := ctx.Backend()
	if be == nil {
		return nil, nil, fmt.Errorf("no database connection available")
	}
	var (
		res backend.Result
		err error
	)
	// 可取消的后端走 ExecuteQuery（带客户端预分配的 queryID），F8 才能凭该 ID
	// 让服务端中止它；不支持取消的后端退回原来的 Run。
	if c, ok := be.(backend.Canceller); ok {
		qid := uuid.NewString()
		tui.SetInFlightQuery(qid)
		defer tui.ClearInFlightQuery(qid)
		res, err = c.RunQuery(qid, sqlText)
	} else {
		res, err = be.Run(sqlText)
	}
	if err != nil {
		return nil, nil, err
	}
	if res.Frame != nil {
		return res.Frame, nil, nil
	}
	if res.Exec != nil {
		return nil, &qxExecInfo{rowsAffected: res.Exec.RowsAffected}, nil
	}
	return nil, &qxExecInfo{}, nil
}

// qxRunCmd wraps qxRun in a tea.Cmd, mapping the outcome to app messages:
// row sets go through onFrame, exec outcomes become a toast and errors
// become the error popup.
func qxRunCmd(ctx tui.AppContext, sqlText string, onFrame func(*tuidata.Frame) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		frame, info, err := qxRun(ctx, sqlText)
		switch {
		case err != nil:
			return tui.ErrorMsg{Err: err}
		case frame != nil:
			qxRecordHistory(ctx, sqlText)
			return onFrame(frame)
		case info != nil:
			qxRecordHistory(ctx, sqlText)
			return tui.ToastMsg{Text: fmt.Sprintf("%d rows affected", info.rowsAffected)}
		default:
			qxRecordHistory(ctx, sqlText)
			return tui.ToastMsg{Text: "ok"}
		}
	}
}

// qxQuoteIdent wraps an identifier in double quotes, doubling embedded
// quote characters.
func qxQuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// qxTableSource is the optional AppContext extension reporting the live
// table the active pane was fetched from (empty table when the pane came
// from a query or a file rather than a table open).
type qxTableSource interface {
	TableSource() (namespace, table string)
}

// qxTarget returns the quoted table name the inline prompts run against. In
// file mode the app keeps the frame under view registered as "_", so the
// prompts always target that alias. In live database mode the pane records
// the real namespace/table it was opened from (Pane source metadata), which
// is spelled into SQL by the backend's own TableTarget — the same spelling
// FetchTable used, including the namespace qualifier. When no source is
// recorded (tabs opened from full query results) we fall back to the
// current tab title, which by convention is the name of the table the tab
// was loaded from. That fallback is a heuristic and the resulting SQL will
// simply fail with a clear engine error. Quoting is dialect-aware: mysql
// rejects double-quoted identifiers by default (they are string literals
// without ANSI_QUOTES), so the fallback gets backticks.
func qxTarget(ctx tui.AppContext) string {
	be := ctx.Backend()
	if be == nil {
		return qxQuoteIdent("_")
	}
	if ts, ok := ctx.(qxTableSource); ok {
		if ns, table := ts.TableSource(); table != "" {
			return qualifyTable(be.Kind(), ns, table)
		}
	}
	return qualifyTable(be.Kind(), "", ctx.BaseCrumb())
}

// qualifyTable spells ns.table with the dialect's own identifier quoting
// (pkg/changes，AGENTS.md §12：SQL 生成不散落在 UI 层)。
func qualifyTable(kind, ns, table string) string {
	ident := changes.QuoteIdent(protocol.DatabaseKind(kind), table)
	if ns != "" {
		ident = changes.QuoteIdent(protocol.DatabaseKind(kind), ns) + "." + ident
	}
	return ident
}

// qxSelectSQL expands the inline select prompt: SELECT <arg> FROM <target>.
func qxSelectSQL(ctx tui.AppContext, arg string) string {
	return "SELECT " + arg + " FROM " + qxTarget(ctx)
}

// qxFilterSQL expands the inline filter prompt: SELECT * FROM <target> WHERE <arg>.
func qxFilterSQL(ctx tui.AppContext, arg string) string {
	return "SELECT * FROM " + qxTarget(ctx) + " WHERE " + arg
}

// qxOrderSQL expands the inline order prompt: SELECT * FROM <target> ORDER BY <arg>.
// When arg ends with a space-separated "ASC" or "DESC" token (case-insensitive)
// the direction is kept as-is; otherwise " ASC" is appended for backward compat.
func qxOrderSQL(ctx tui.AppContext, arg string) string {
	direction := ""
	if idx := strings.LastIndex(arg, " "); idx >= 0 {
		last := strings.ToUpper(strings.TrimSpace(arg[idx+1:]))
		if last == "ASC" || last == "DESC" {
			direction = "" // already present in arg
		} else {
			direction = " ASC"
		}
	} else {
		direction = " ASC"
	}
	return "SELECT * FROM " + qxTarget(ctx) + " ORDER BY " + arg + direction
}

// --- completion schema access --------------------------------------------------

// qxSchemaProvider is the optional AppContext extension that supplies a
// full completion schema (tables with columns + current frame columns)
// without blocking on the live connection.
type qxSchemaProvider interface {
	CompletionSchema() complete.Schema
}

// qxSchemaWarmer is the optional AppContext extension that fills the
// live-connection completion cache. It blocks on catalog queries, so it
// must only be called from inside a tea.Cmd goroutine.
type qxSchemaWarmer interface {
	WarmCompletionSchema(tables ...string)
}

// qxSchemaOf snapshots the completion schema for ctx. Contexts without
// CompletionSchema (test stubs) fall back to the legacy pair of accessors:
// current columns plus bare table names (no per-table columns). The
// fallback's TableNames may issue live catalog queries, so treat this like
// the warmer: fine inside a tea.Cmd, not on the update loop.
func qxSchemaOf(ctx tui.AppContext) complete.Schema {
	if sp, ok := ctx.(qxSchemaProvider); ok {
		sc := sp.CompletionSchema()
		if be := ctx.Backend(); be != nil {
			sc.Engine = be.Kind()
		}
		return sc
	}
	sc := complete.Schema{Current: ctx.ColumnNames()}
	if be := ctx.Backend(); be != nil {
		sc.Engine = be.Kind()
	}
	if ts := ctx.TableNames(); len(ts) > 0 {
		sc.Tables = make(map[string][]string, len(ts))
		for _, t := range ts {
			sc.Tables[t] = nil
		}
	}
	return sc
}

// qxWarm runs the schema warm-up when ctx supports it (blocking; tea.Cmd
// goroutines only).
func qxWarm(ctx tui.AppContext, tables ...string) {
	if w, ok := ctx.(qxSchemaWarmer); ok {
		w.WarmCompletionSchema(tables...)
	}
}

// qxShortTitle condenses a statement into a tab title: whitespace collapsed,
// truncated to 20 characters.
func qxShortTitle(sqlText string) string {
	fields := strings.Fields(sqlText)
	s := strings.Join(fields, " ")
	r := []rune(s)
	if len(r) > 20 {
		return string(r[:20])
	}
	if s == "" {
		return "query"
	}
	return s
}

// --- line input --------------------------------------------------------------
//
// The single-line editing state machine lives in tui.LineInput; the helpers
// below are the SQL-completion-specific additions on top of it.

// qxApplySuggestion replaces the identifier token ending at the cursor with
// s and places the cursor after it.
func qxApplySuggestion(in *tui.LineInput, s string) {
	text := in.String()
	b := in.ByteCursor()
	start := b
	for start > 0 && qxIdentByte(text[start-1]) {
		start--
	}
	head := text[:start] + s
	in.Set(head + text[b:])
	in.SetCursor(len([]rune(head)))
}

// qxIdentByte mirrors the completer's notion of an identifier byte.
func qxIdentByte(b byte) bool {
	return b == '_' ||
		('a' <= b && b <= 'z') ||
		('A' <= b && b <= 'Z') ||
		('0' <= b && b <= '9')
}

// --- suggestion list ---------------------------------------------------------

// qxMaxSuggestions caps how many completion candidates are visible at once;
// longer lists scroll as the highlight moves.
const qxMaxSuggestions = 8

// qxSuggest is the completion dropdown state. An empty items slice means the
// list is closed.
type qxSuggest struct {
	items []complete.Suggestion
	sel   int
}

func (s *qxSuggest) open() bool { return len(s.items) > 0 }

func (s *qxSuggest) selected() complete.Suggestion {
	if !s.open() {
		return complete.Suggestion{}
	}
	return s.items[s.sel]
}

// recompute refreshes the candidate list for the token at cursor.
func (s *qxSuggest) recompute(input string, cursor int, sc complete.Schema) {
	s.items = complete.Suggest(input, cursor, sc)
	s.sel = 0
}

// cycle moves the highlight by d, wrapping.
func (s *qxSuggest) cycle(d int) {
	if n := len(s.items); n > 0 {
		s.sel = ((s.sel+d)%n + n) % n
	}
}

func (s *qxSuggest) clear() {
	s.items = nil
	s.sel = 0
}

// qxKindMarker abbreviates a suggestion kind for the dropdown gutter.
func qxKindMarker(kind string) string {
	switch kind {
	case complete.KindColumn:
		return "col"
	case complete.KindTable:
		return "tbl"
	case complete.KindFunction:
		return "fn"
	case complete.KindKeyword:
		return "kw"
	}
	return kind
}

// view renders one line per visible candidate: the replacement text plus a
// dimmed kind marker and detail (e.g. the owning table). At most
// qxMaxSuggestions lines show at once; the window follows the highlight and
// a dimmed position line appears when the list scrolls.
func (s *qxSuggest) view(th *theme.Theme) []string {
	n := len(s.items)
	if n == 0 {
		return nil
	}
	off := 0
	if s.sel >= qxMaxSuggestions {
		off = s.sel - qxMaxSuggestions + 1
	}
	end := off + qxMaxSuggestions
	if end > n {
		end = n
	}
	lines := make([]string, 0, end-off+1)
	for i := off; i < end; i++ {
		it := s.items[i]
		marker := qxKindMarker(it.Kind)
		if it.Detail != "" {
			marker += " " + it.Detail
		}
		if i == s.sel {
			lines = append(lines, th.ListSelected.Render(" "+it.Text+"  "+marker))
		} else {
			lines = append(lines, th.ListItem.Render(" "+it.Text+"  ")+th.Placeholder.Render(marker))
		}
	}
	if n > qxMaxSuggestions {
		lines = append(lines, th.Placeholder.Render(fmt.Sprintf(" %d/%d", s.sel+1, n)))
	}
	return lines
}

// qxBoxWidth picks a sensible centered-box width for the given window width.
func qxBoxWidth(width int) int {
	w := width - 4
	if w > 70 {
		w = 70
	}
	if w < 24 {
		w = 24
	}
	return w
}
