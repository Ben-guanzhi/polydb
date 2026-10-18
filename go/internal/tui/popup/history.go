package popup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tuidata"
)

// --- persistence ------------------------------------------------------------

// qxHistoryMax caps how many entries are kept on disk. Once the file exceeds
// this many lines the oldest are dropped on the next write.
const qxHistoryMax = 2000

type qxHistoryEntry struct {
	TS  int64  `json:"ts"`
	SQL string `json:"sql"`
}

var (
	qxHistoryMu   sync.Mutex
	qxHistoryPath string
)

// qxHistoryFile resolves (and memoizes) the history file path under the
// user config dir. Best-effort: errors are reported to callers, which drop
// the write rather than fail the query.
func qxHistoryFile() (string, error) {
	if qxHistoryPath != "" {
		return qxHistoryPath, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "polydb", "history.jsonl")
	qxHistoryPath = p
	return p, nil
}

// qxRecordHistory appends one executed statement to the history file.
// Gated on a non-nil transport client so it fires only in the real app
// (the test stub returns nil), and only on successful runs (callers check
// the outcome before invoking it). Errors are swallowed — history must
// never break a query.
func qxRecordHistory(ctx tui.AppContext, sql string) {
	sql = strings.TrimSpace(sql)
	if sql == "" || ctx == nil || ctx.Client() == nil {
		return
	}
	p, err := qxHistoryFile()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	qxHistoryMu.Lock()
	defer qxHistoryMu.Unlock()
	// Trim the tail once we exceed the cap: read, keep the newest max, rewrite.
	if fi, statErr := os.Stat(p); statErr == nil && fi.Size() > 0 {
		if lines, rErr := qxReadHistory(p); rErr == nil && len(lines) >= qxHistoryMax {
			keep := lines[len(lines)-qxHistoryMax+1:]
			if wErr := qxWriteHistory(p, keep); wErr != nil {
				return
			}
		}
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(qxHistoryEntry{TS: time.Now().Unix(), SQL: sql})
}

func qxReadHistory(p string) ([]string, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

func qxWriteHistory(p string, lines []string) error {
	return os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// --- overlay -----------------------------------------------------------------

type qxHistoryLoadMsg struct {
	entries []qxHistoryEntry
	err     error
}

func init() {
	RegisterCommand(
		Command{Name: "history", Aliases: nil, Description: "browse and replay recently run queries"},
		func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
			return &qxHistory{ctx: ctx, sel: 0, offset: 0}, nil
		},
	)
}

// qxHistory lists executed statements, newest first. Enter re-runs the
// selected query as a new tab; "e" loads it into the SQL editor; esc closes.
type qxHistory struct {
	ctx     tui.AppContext
	entries []qxHistoryEntry
	err     error
	sel     int
	offset  int
}

// Init loads the on-disk history off the update loop.
func (h *qxHistory) Init() tea.Cmd {
	return func() tea.Msg {
		p, err := qxHistoryFile()
		if err != nil {
			return qxHistoryLoadMsg{err: err}
		}
		lines, err := qxReadHistory(p)
		if err != nil {
			// Missing file is not an error: just an empty history.
			return qxHistoryLoadMsg{entries: nil}
		}
		entries := make([]qxHistoryEntry, 0, len(lines))
		for _, l := range lines {
			var e qxHistoryEntry
			if jErr := json.Unmarshal([]byte(l), &e); jErr == nil && strings.TrimSpace(e.SQL) != "" {
				entries = append(entries, e)
			}
		}
		// Newest first.
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
		return qxHistoryLoadMsg{entries: entries}
	}
}

func (h *qxHistory) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	switch m := msg.(type) {
	case qxHistoryLoadMsg:
		h.entries = m.entries
		h.sel = 0
		h.offset = 0
		return h, nil
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return h, nil
	}
	switch key.String() {
	case "esc", "ctrl+c":
		return h, tui.CloseOverlay
	case "up", "ctrl+p":
		if h.sel > 0 {
			h.sel--
		}
		return h, nil
	case "down", "ctrl+n":
		if h.sel < len(h.entries)-1 {
			h.sel++
		}
		return h, nil
	case "enter":
		if h.sel < 0 || h.sel >= len(h.entries) {
			return h, tui.CloseOverlay
		}
		sql := h.entries[h.sel].SQL
		paneID := h.ctx.ActivePaneID()
		return h, tea.Sequence(tui.CloseOverlay, qxRunCmd(h.ctx, sql, func(f *tuidata.Frame) tea.Msg {
			return tui.ApplyFrameMsg{Frame: f, Crumb: "history", NewTab: true, TabTitle: qxShortTitle(sql), PaneID: paneID}
		}))
	case "e":
		if h.sel < 0 || h.sel >= len(h.entries) {
			return h, nil
		}
		// Hand the statement to the SQL editor, prefilled, for editing.
		factory, ok := tui.Factories["query"]
		if !ok {
			return h, nil
		}
		ov, err := factory(h.ctx, h.entries[h.sel].SQL)
		if err != nil || ov == nil {
			return h, nil
		}
		return h, tea.Sequence(tui.CloseOverlay, func() tea.Msg { return tui.PushOverlayMsg{Overlay: ov} })
	}
	return h, nil
}

func (h *qxHistory) View(width, height int, th *theme.Theme) string {
	w := qxBoxWidth(width)
	inner := w - 2

	if h.err != nil {
		body := th.Error.Render(" " + h.err.Error())
		return tui.Box("history", body, w, th)
	}
	if len(h.entries) == 0 {
		body := th.Placeholder.Render(" no history yet — run a query first")
		return tui.Box("history", body, w, th)
	}

	listH := qxBoxListHeight(height)
	if listH > len(h.entries) {
		listH = len(h.entries)
	}
	if h.sel < h.offset {
		h.offset = h.sel
	}
	if listH > 0 && h.sel >= h.offset+listH {
		h.offset = h.sel - listH + 1
	}
	if h.offset > len(h.entries)-listH {
		h.offset = qxMax(0, len(h.entries)-listH)
	}

	lines := make([]string, 0, listH+1)
	for i := h.offset; i < h.offset+listH; i++ {
		e := h.entries[i]
		label := strings.ReplaceAll(strings.Join(strings.Fields(e.SQL), " "), "\n", " ")
		if len([]rune(label)) > inner-2 {
			label = ansi.Truncate(label, inner-2, "…")
		}
		row := " " + label
		if i == h.sel {
			lines = append(lines, th.ListSelected.Render(row))
		} else {
			lines = append(lines, th.ListItem.Render(row))
		}
	}
	lines = append(lines, th.Placeholder.Render(" enter run · e edit · esc close"))
	return tui.Box("history", strings.Join(lines, "\n"), w, th)
}

func qxMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func qxBoxListHeight(height int) int {
	h := height - 6
	if h < 3 {
		h = 3
	}
	if h > 16 {
		h = 16
	}
	return h
}

var _ tui.OverlayIniter = (*qxHistory)(nil)
