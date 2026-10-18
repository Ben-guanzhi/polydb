package popup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/pkg/protocol"
)

// Compare & Sync wizard (net-new TUI feature, behavior.md §14/§15). It drives
// transport.SchemaDiff (read-only snapshot diff) then transport.SchemaDDL
// (server recomputes and emits forward + rollback scripts for the selected
// entries) entirely through the transport layer — no driver access (铁律 2).
//
// Stages form a page stack: pick source connection → source schema → target
// connection → target schema → diff (toggle entries) → DDL. Esc walks back.

const (
	cmpStageLoading = iota
	cmpStageSrcConn
	cmpStageSrcSchema
	cmpStageTgtConn
	cmpStageTgtSchema
	cmpStageDiff
	cmpStageDDL
)

type cmpConnsMsg struct {
	conns []protocol.ConnectionInfo
	err   error
}

type cmpSchemasMsg struct {
	side    int // 0 source, 1 target
	schemas []protocol.SchemaInfo
	err     error
}

type cmpDiffMsg struct {
	plan *protocol.SchemaDiffPlan
	err  error
}

type cmpDDLMsg struct {
	script *protocol.DdlScript
	err    error
}

func init() {
	RegisterCommand(
		Command{Name: "compare", Aliases: []string{"sync", "diff"}, Description: "compare two schemas and generate a sync script"},
		func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
			return &cmpOverlay{ctx: ctx, stage: cmpStageLoading, offset: 0}, nil
		},
	)
}

type cmpOverlay struct {
	ctx       tui.AppContext
	stage     int
	conns     []protocol.ConnectionInfo
	schemas   []protocol.SchemaInfo // schemas for the side currently being picked
	pickSide  int                   // 0 source, 1 target
	srcConn   int
	tgtConn   int
	srcSchema string
	tgtSchema string
	plan      *protocol.SchemaDiffPlan
	sel       map[string]bool
	ddl       *protocol.DdlScript
	list      int // list cursor within the current stage
	offset    int
	err       error
}

func (o *cmpOverlay) Fullscreen() bool { return true }

// Init loads the connection list off the update loop.
func (o *cmpOverlay) Init() tea.Cmd {
	if o.ctx.Client() == nil {
		o.err = errors.New("no database connection client available")
		return nil
	}
	o.stage = cmpStageLoading
	return func() tea.Msg {
		conns, err := o.ctx.Client().ListConnections()
		return cmpConnsMsg{conns: conns, err: err}
	}
}

func (o *cmpOverlay) loadSchemas(side int) tea.Cmd {
	connID := o.conns[o.srcConn].ID
	if side == 1 {
		connID = o.conns[o.tgtConn].ID
	}
	return func() tea.Msg {
		schemas, err := o.ctx.Client().ListSchemas(context.Background(), connID)
		return cmpSchemasMsg{side: side, schemas: schemas, err: err}
	}
}

func (o *cmpOverlay) targets() (protocol.SchemaDiffTarget, protocol.SchemaDiffTarget) {
	return protocol.SchemaDiffTarget{
		ConnectionID: protocol.ConnectionID(o.conns[o.srcConn].ID),
		Schema:       o.srcSchema,
	}, protocol.SchemaDiffTarget{
		ConnectionID: protocol.ConnectionID(o.conns[o.tgtConn].ID),
		Schema:       o.tgtSchema,
	}
}

func (o *cmpOverlay) loadDiff() tea.Cmd {
	src, tgt := o.targets()
	return func() tea.Msg {
		plan, err := o.ctx.Client().SchemaDiff(context.Background(), &protocol.SchemaDiffRequest{
			Source:  src,
			Target:  tgt,
			Options: &protocol.SchemaDiffOptions{SelectDestructive: false},
		})
		return cmpDiffMsg{plan: plan, err: err}
	}
}

func (o *cmpOverlay) loadDDL() tea.Cmd {
	src, tgt := o.targets()
	selected := make([]string, 0, len(o.plan.Entries))
	for _, e := range o.plan.Entries {
		if o.sel[e.ID] {
			selected = append(selected, e.ID)
		}
	}
	return func() tea.Msg {
		script, err := o.ctx.Client().SchemaDDL(context.Background(), &protocol.SchemaDdlRequest{
			Source:   src,
			Target:   tgt,
			Options:  &protocol.SchemaDiffOptions{SelectDestructive: false},
			Selected: selected,
		})
		return cmpDDLMsg{script: script, err: err}
	}
}

func (o *cmpOverlay) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	switch m := msg.(type) {
	case cmpConnsMsg:
		o.err = m.err
		o.conns = m.conns
		o.list, o.offset = 0, 0
		if m.err == nil && len(m.conns) > 0 {
			o.stage = cmpStageSrcConn
		}
		return o, nil
	case cmpSchemasMsg:
		o.err = m.err
		o.schemas = m.schemas
		o.list, o.offset = 0, 0
		if m.err == nil {
			if m.side == 0 {
				o.stage = cmpStageSrcSchema
			} else {
				o.stage = cmpStageTgtSchema
			}
		}
		return o, nil
	case cmpDiffMsg:
		o.err = m.err
		o.plan = m.plan
		o.sel = make(map[string]bool, len(m.plan.Entries))
		for _, e := range m.plan.Entries {
			o.sel[e.ID] = true // default: sync everything; user deselects
		}
		o.list, o.offset = 0, 0
		if m.err == nil {
			o.stage = cmpStageDiff
		}
		return o, nil
	case cmpDDLMsg:
		o.err = m.err
		o.ddl = m.script
		o.list, o.offset = 0, 0
		if m.err == nil {
			o.stage = cmpStageDDL
		}
		return o, nil
	}

	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil
	}
	switch key.String() {
	case "esc", "ctrl+c":
		return o, o.back()
	}

	switch o.stage {
	case cmpStageLoading:
		return o, nil
	case cmpStageSrcConn, cmpStageTgtConn:
		return o, o.updateConnPick(key)
	case cmpStageSrcSchema, cmpStageTgtSchema:
		return o, o.updateSchemaPick(key)
	case cmpStageDiff:
		return o, o.updateDiff(key)
	case cmpStageDDL:
		return o, o.updateDDL(key)
	}
	return o, nil
}

// back walks one stage up the page stack; at the top it closes the overlay.
func (o *cmpOverlay) back() tea.Cmd {
	switch o.stage {
	case cmpStageSrcConn:
		return tui.CloseOverlay
	case cmpStageSrcSchema:
		o.stage = cmpStageSrcConn
	case cmpStageTgtConn:
		o.stage = cmpStageSrcSchema
	case cmpStageTgtSchema:
		o.stage = cmpStageTgtConn
	case cmpStageDiff:
		o.stage = cmpStageTgtSchema
	case cmpStageDDL:
		o.stage = cmpStageDiff
	}
	return nil
}

func (o *cmpOverlay) updateConnPick(key tea.KeyPressMsg) tea.Cmd {
	n := len(o.conns)
	switch key.String() {
	case "up", "ctrl+p":
		if o.list > 0 {
			o.list--
		}
	case "down", "ctrl+n":
		if o.list < n-1 {
			o.list++
		}
	case "enter":
		if n == 0 {
			return tui.CloseOverlay
		}
		side := 0
		if o.stage == cmpStageTgtConn {
			o.tgtConn = o.list
			side = 1
		} else {
			o.srcConn = o.list
		}
		o.pickSide = side
		return o.loadSchemas(side)
	}
	return nil
}

func (o *cmpOverlay) updateSchemaPick(key tea.KeyPressMsg) tea.Cmd {
	n := len(o.schemas) + 1 // +1 for the "(default)" entry
	switch key.String() {
	case "up", "ctrl+p":
		if o.list > 0 {
			o.list--
		}
	case "down", "ctrl+n":
		if o.list < n-1 {
			o.list++
		}
	case "enter":
		chosen := ""
		if o.list > 0 {
			chosen = o.schemas[o.list-1].Name
		}
		if o.stage == cmpStageSrcSchema {
			o.srcSchema = chosen
			o.stage = cmpStageTgtConn
			o.list, o.offset = 0, 0
		} else {
			o.tgtSchema = chosen
			return o.loadDiff()
		}
	}
	return nil
}

func (o *cmpOverlay) updateDiff(key tea.KeyPressMsg) tea.Cmd {
	n := len(o.plan.Entries)
	switch key.String() {
	case "up", "ctrl+p":
		if o.list > 0 {
			o.list--
		}
	case "down", "ctrl+n":
		if o.list < n-1 {
			o.list++
		}
	case " ":
		if n > 0 {
			e := o.plan.Entries[o.list]
			o.sel[e.ID] = !o.sel[e.ID]
		}
	case "a":
		all := true
		for _, e := range o.plan.Entries {
			if !o.sel[e.ID] {
				all = false
				break
			}
		}
		for _, e := range o.plan.Entries {
			o.sel[e.ID] = !all
		}
	case "enter":
		if n == 0 {
			return tui.CloseOverlay
		}
		return o.loadDDL()
	}
	return nil
}

func (o *cmpOverlay) updateDDL(key tea.KeyPressMsg) tea.Cmd {
	switch key.String() {
	case "y":
		if o.ddl != nil {
			return func() tea.Msg { return tui.CopyTextMsg{Text: cmpScriptText(o.ddl.Statements)} }
		}
	case "r":
		if o.ddl != nil {
			return func() tea.Msg { return tui.CopyTextMsg{Text: cmpScriptText(o.ddl.Rollback)} }
		}
	}
	return nil
}

func cmpScriptText(stmts []protocol.DdlStatement) string {
	parts := make([]string, 0, len(stmts))
	for _, s := range stmts {
		parts = append(parts, s.SQL)
	}
	return strings.Join(parts, "\n")
}

// --- rendering -------------------------------------------------------------

func (o *cmpOverlay) View(width, height int, th *theme.Theme) string {
	w := width - 4
	if w > 80 {
		w = 80
	}
	if w < 40 {
		w = 40
	}
	title := o.stageTitle()

	if o.err != nil {
		return tui.Box(title, th.Error.Render(" "+o.err.Error())+"\n"+th.Placeholder.Render(" esc back"), w, th)
	}
	switch o.stage {
	case cmpStageLoading:
		return tui.Box(title, th.Placeholder.Render(" loading connections…"), w, th)
	case cmpStageSrcConn, cmpStageTgtConn:
		return o.viewList(title, o.connItems(), " ↑↓ choose · enter select · esc back", w, height, th)
	case cmpStageSrcSchema, cmpStageTgtSchema:
		return o.viewList(title, o.schemaItems(), " ↑↓ choose · enter select · esc back", w, height, th)
	case cmpStageDiff:
		return o.viewDiff(w, height, th)
	case cmpStageDDL:
		return o.viewDDL(w, height, th)
	}
	return tui.Box(title, "", w, th)
}

func (o *cmpOverlay) stageTitle() string {
	switch o.stage {
	case cmpStageSrcConn:
		return "compare · source connection"
	case cmpStageSrcSchema:
		return "compare · source schema"
	case cmpStageTgtConn:
		return "compare · target connection"
	case cmpStageTgtSchema:
		return "compare · target schema"
	case cmpStageDiff:
		return "compare · differences"
	case cmpStageDDL:
		return "compare · sync script"
	default:
		return "compare"
	}
}

func (o *cmpOverlay) connItems() []string {
	items := make([]string, len(o.conns))
	for i, c := range o.conns {
		items[i] = fmt.Sprintf("%s  [%s]  %s", c.Name, c.Kind, c.Database)
	}
	return items
}

func (o *cmpOverlay) schemaItems() []string {
	items := []string{"(default schema)"}
	for _, s := range o.schemas {
		items = append(items, s.Name)
	}
	return items
}

func (o *cmpOverlay) viewList(title string, items []string, hint string, w, height int, th *theme.Theme) string {
	listH := height - 8
	if listH < 3 {
		listH = 3
	}
	if listH > 14 {
		listH = 14
	}
	if listH > len(items) {
		listH = len(items)
	}
	if o.list < o.offset {
		o.offset = o.list
	}
	if listH > 0 && o.list >= o.offset+listH {
		o.offset = o.list - listH + 1
	}
	if o.offset > len(items)-listH {
		o.offset = qxMax(0, len(items)-listH)
	}
	inner := w - 2
	lines := make([]string, 0, listH+1)
	for i := o.offset; i < o.offset+listH; i++ {
		label := ansi.Truncate(items[i], inner-1, "…")
		if i == o.list {
			lines = append(lines, th.ListSelected.Render(" "+label))
		} else {
			lines = append(lines, th.ListItem.Render(" "+label))
		}
	}
	if len(items) == 0 {
		lines = append(lines, th.Placeholder.Render(" no items"))
	}
	lines = append(lines, th.Placeholder.Render(hint))
	return tui.Box(title, strings.Join(lines, "\n"), w, th)
}

func (o *cmpOverlay) viewDiff(w, height int, th *theme.Theme) string {
	if len(o.plan.Entries) == 0 {
		return tui.Box(o.stageTitle(),
			th.Text.Render(" no differences between source and target")+"\n"+
				th.Placeholder.Render(" esc back"), w, th)
	}
	listH := height - 10
	if listH < 3 {
		listH = 3
	}
	if listH > 16 {
		listH = 16
	}
	if listH > len(o.plan.Entries) {
		listH = len(o.plan.Entries)
	}
	if o.list < o.offset {
		o.offset = o.list
	}
	if listH > 0 && o.list >= o.offset+listH {
		o.offset = o.list - listH + 1
	}
	if o.offset > len(o.plan.Entries)-listH {
		o.offset = qxMax(0, len(o.plan.Entries)-listH)
	}
	inner := w - 2
	lines := make([]string, 0, listH+2)
	for i := o.offset; i < o.offset+listH; i++ {
		e := o.plan.Entries[i]
		mark := " "
		if o.sel[e.ID] {
			mark = "x"
		}
		label := fmt.Sprintf("[%s] %s %s", mark, e.Op, e.Table)
		if e.Object != "" {
			label += "." + e.Object
		}
		if e.Destructive {
			label += "  (destructive)"
		}
		label = ansi.Truncate(label, inner-1, "…")
		if i == o.list {
			lines = append(lines, th.ListSelected.Render(" "+label))
		} else {
			lines = append(lines, th.ListItem.Render(" "+label))
		}
	}
	// Footer: detail of the highlighted entry.
	if o.list >= 0 && o.list < len(o.plan.Entries) {
		e := o.plan.Entries[o.list]
		if e.Reason != "" {
			lines = append(lines, th.Subtle.Render("  "+ansi.Truncate(e.Reason, inner-2, "…")))
		} else if e.From != nil && e.To != nil {
			lines = append(lines, th.Subtle.Render(fmt.Sprintf("  %s → %s", *e.From, *e.To)))
		} else if e.From != nil {
			lines = append(lines, th.Subtle.Render("  from: "+*e.From))
		} else if e.To != nil {
			lines = append(lines, th.Subtle.Render("  to: "+*e.To))
		}
	}
	selCount := 0
	for _, e := range o.plan.Entries {
		if o.sel[e.ID] {
			selCount++
		}
	}
	lines = append(lines, th.Placeholder.Render(fmt.Sprintf(" %d/%d selected · space toggle · a all/none · enter generate · esc back",
		selCount, len(o.plan.Entries))))
	return tui.Box(o.stageTitle(), strings.Join(lines, "\n"), w, th)
}

func (o *cmpOverlay) viewDDL(w, height int, th *theme.Theme) string {
	if o.ddl == nil {
		return tui.Box(o.stageTitle(), th.Placeholder.Render(" no script"), w, th)
	}
	var b strings.Builder
	b.WriteString(th.Subtle.Render(" forward:"))
	b.WriteString("\n")
	for _, s := range o.ddl.Statements {
		line := s.SQL
		if s.NonReversible {
			line += "  -- non-reversible"
		}
		b.WriteString(" " + ansi.Truncate(line, w-3, "…") + "\n")
	}
	b.WriteString(th.Subtle.Render(" rollback:"))
	b.WriteString("\n")
	if len(o.ddl.Rollback) == 0 {
		b.WriteString(th.Placeholder.Render("  (none — forward is reversible)"))
		b.WriteString("\n")
	}
	for _, s := range o.ddl.Rollback {
		b.WriteString(" " + ansi.Truncate(s.SQL, w-3, "…") + "\n")
	}
	b.WriteString(th.Placeholder.Render(" y copy forward · r copy rollback · esc back"))
	return tui.Box(o.stageTitle(), b.String(), w, th)
}

var _ tui.Overlay = (*cmpOverlay)(nil)
var _ tui.FullscreenOverlay = (*cmpOverlay)(nil)
