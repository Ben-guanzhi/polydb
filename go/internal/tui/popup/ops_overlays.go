package popup

// 运维四件套的 TUI 面板（§380）：:procs / :vars / :users 三个列表 overlay
// + :gendata / :procs kill / :users gen 动作命令。数据经 AppContext.Client()
// 的 transport 面（§378 已通管道）——铁律 2：UI 只经 transport 访问数据。
//
// 呈现约定：列表 overlay 走 info.go 同款的异步加载 + 滚动模式；动作命令
// （kill/gendata/user-DDL）返回 (nil, err)——err 文案即结果呈现（成功也走
// 这条路：errBox 显示「已终止/已生成」）。

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/polydb/polydb/internal/theme"
	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/pkg/protocol"
)

// opsListMsg 是异步加载结果的载体（owner 比对防串台——info.go 同款）。
type opsListMsg struct {
	owner tui.Overlay
	rows  []opsRow
	err   error
}

// opsRow 是列表面板的一行（三面板共用：vars/procs/users 的列集合不同）。
type opsRow struct {
	cells []string
}

// opsListOverlay 是通用列表 overlay：列头 + 行 + 滚动。
type opsListOverlay struct {
	ctx      tui.AppContext
	title    string
	headers  []string
	rows     []opsRow
	loading  bool
	loadErr  string
	offset   int
	viewRows int
	fetch    func(cctx context.Context, connID string, cli transportClient) ([]opsRow, error)
}

// transportClient 是本包对 transport.Client 的窄别名（接口嵌入避免 import 环）。
type transportClient = interface {
	ListServerProcesses(ctx context.Context, id string) ([]protocol.ServerProcess, error)
	KillServerProcess(ctx context.Context, id, pid string) error
	ListServerVariables(ctx context.Context, id string) ([]protocol.ServerVariable, error)
	ListServerUsers(ctx context.Context, id string) ([]protocol.ServerUser, error)
	GenerateUserDDL(ctx context.Context, id string, req *protocol.UserOpsRequest) (string, error)
	GenerateTestData(ctx context.Context, id string, req *protocol.GenDataRequest) (int, error)
}

func newOpsListOverlay(ctx tui.AppContext, title string, headers []string,
	fetch func(cctx context.Context, connID string, cli transportClient) ([]opsRow, error)) *opsListOverlay {
	return &opsListOverlay{ctx: ctx, title: title, headers: headers, loading: true, viewRows: 10, fetch: fetch}
}

func (o *opsListOverlay) Init() tea.Cmd {
	cli := o.ctx.Client()
	if cli == nil {
		o.loading = false
		o.loadErr = "没有已连接的数据库"
		return nil
	}
	owner, fetch, id := o, o.fetch, currentConnID(o.ctx)
	return func() tea.Msg {
		rows, err := fetch(context.Background(), id, cli)
		return opsListMsg{owner: owner, rows: rows, err: err}
	}
}

func (o *opsListOverlay) Update(msg tea.Msg) (tui.Overlay, tea.Cmd) {
	if m, ok := msg.(opsListMsg); ok && m.owner == o {
		o.loading = false
		if m.err != nil {
			o.loadErr = m.err.Error()
		} else {
			o.rows = m.rows
		}
		o.offset = 0
		return o, nil
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil
	}
	switch key.String() {
	case "q", "esc":
		return o, tui.CloseOverlay
	case "up", "k":
		o.offset--
	case "down", "j":
		o.offset++
	case "pgup", "ctrl+u":
		o.offset -= max(1, o.viewRows)
	case "pgdown", "ctrl+f":
		o.offset += max(1, o.viewRows)
	case "g", "home":
		o.offset = 0
	case "G", "end":
		o.offset = len(o.rows)
	}
	o.offset = clampOps(o.offset, 0, max(0, len(o.rows)-max(1, o.viewRows)))
	return o, nil
}

func (o *opsListOverlay) View(width, height int, th *theme.Theme) string {
	boxW := clampOps(84, 30, max(30, width-4))
	inner := boxW - 2

	var lines []string
	lines = append(lines, " "+th.Subtle.Render(o.title), "")

	// 列宽自适应（cap 28）。
	ws := make([]int, len(o.headers))
	for i, h := range o.headers {
		ws[i] = len(h)
	}
	for _, r := range o.rows {
		for i, c := range r.cells {
			if i < len(ws) && len(c) > ws[i] {
				ws[i] = len(c)
			}
		}
	}
	for i := range ws {
		if ws[i] > 28 {
			ws[i] = 28
		}
	}

	header := " "
	for i, h := range o.headers {
		header += fmt.Sprintf("%-*s ", ws[i], h)
	}
	lines = append(lines, th.Header.Render(ansi.Truncate(header, inner, "…")))

	avail := height - 2 - len(lines) - 1
	if avail < 3 {
		avail = 3
	}
	if avail > len(o.rows) {
		avail = len(o.rows)
	}
	o.viewRows = max(1, avail)
	o.offset = clampOps(o.offset, 0, max(0, len(o.rows)-o.viewRows))

	switch {
	case o.loading:
		lines = append(lines, th.Subtle.Render(" loading..."))
	case o.loadErr != "":
		lines = append(lines, th.Error.Render(" "+ansi.Truncate(o.loadErr, inner, "…")))
	default:
		for i := o.offset; i < o.offset+o.viewRows && i < len(o.rows); i++ {
			row := " "
			for j, c := range o.rows[i].cells {
				if j < len(ws) {
					row += fmt.Sprintf("%-*s ", ws[j], ansi.Truncate(c, ws[j], "…"))
				}
			}
			lines = append(lines, th.Text.Render(ansi.Truncate(row, inner, "…")))
		}
	}

	hint := " q/esc close"
	if !o.loading && o.loadErr == "" && len(o.rows) > o.viewRows {
		hint = fmt.Sprintf(" %d-%d of %d  •  j/k scroll  •  q/esc close",
			o.offset+1, o.offset+o.viewRows, len(o.rows))
	}
	lines = append(lines, th.Subtle.Render(hint))

	return tui.Box(o.title, strings.Join(lines, "\n"), boxW, th)
}

func clampOps(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

var (
	_ tui.Overlay       = (*opsListOverlay)(nil)
	_ tui.OverlayIniter = (*opsListOverlay)(nil)
)

// ─── 注册：三个列表面板 + 动作命令 ─────────────────────────

// opsClientOf 取当前连接的 transport（nil 时给统一的「未连接」错误）。
func opsClientOf(ctx tui.AppContext) (transportClient, string, error) {
	cli := ctx.Client()
	if cli == nil {
		return nil, "", fmt.Errorf("没有已连接的数据库")
	}
	id := ctx.BaseCrumb()
	return cli, id, nil
}

func init() {
	RegisterCommand(Command{Name: "procs", Description: "server sessions (kill: :procs kill <id>)"}, func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		if rest, ok := strings.CutPrefix(arg, "kill "); ok {
			cli, id, err := opsClientOf(ctx)
			if err != nil {
				return nil, err
			}
			pid := strings.TrimSpace(rest)
			if err := cli.KillServerProcess(context.Background(), id, pid); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("已终止会话 %s", pid)
		}
		return newOpsListOverlay(ctx, "server sessions (:procs kill <id> 终止)", []string{"id", "user", "db", "state", "command", "time"},
			func(cctx context.Context, connID string, cli transportClient) ([]opsRow, error) {
				procs, err := cli.ListServerProcesses(cctx, connID)
				if err != nil {
					return nil, err
				}
				rows := make([]opsRow, 0, len(procs))
				for _, p := range procs {
					rows = append(rows, opsRow{cells: []string{p.ID, p.User, p.DB, p.State, p.Command, p.Time}})
				}
				return rows, nil
			}), nil
	})

	RegisterCommand(Command{Name: "vars", Description: "server variables (read-only)"}, func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		return newOpsListOverlay(ctx, "server variables (read-only)", []string{"name", "value", "default"},
			func(cctx context.Context, connID string, cli transportClient) ([]opsRow, error) {
				vars, err := cli.ListServerVariables(cctx, connID)
				if err != nil {
					return nil, err
				}
				rows := make([]opsRow, 0, len(vars))
				for _, v := range vars {
					rows = append(rows, opsRow{cells: []string{v.Name, v.Value, v.Default}})
				}
				return rows, nil
			}), nil
	})

	RegisterCommand(Command{Name: "users", Description: "login users (gen DDL: :users gen <action> <user> [host])"}, func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		if rest, ok := strings.CutPrefix(arg, "gen "); ok {
			cli, id, err := opsClientOf(ctx)
			if err != nil {
				return nil, err
			}
			parts := strings.Fields(rest)
			if len(parts) < 2 {
				return nil, fmt.Errorf("用法: :users gen <create|drop|grant> <user> [host|db]")
			}
			req := protocol.UserOpsRequest{Action: parts[0], User: parts[1]}
			if len(parts) > 2 {
				if parts[0] == "grant" {
					req.GrantDB = parts[2]
				} else {
					req.Host = parts[2]
				}
			}
			stmt, err := cli.GenerateUserDDL(context.Background(), id, &req)
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("DDL 已生成（确认后经执行入口运行）:\n%s", stmt)
		}
		return newOpsListOverlay(ctx, "login users", []string{"name", "host"},
			func(cctx context.Context, connID string, cli transportClient) ([]opsRow, error) {
				users, err := cli.ListServerUsers(cctx, connID)
				if err != nil {
					return nil, err
				}
				rows := make([]opsRow, 0, len(users))
				for _, u := range users {
					rows = append(rows, opsRow{cells: []string{u.Name, u.Host}})
				}
				return rows, nil
			}), nil
	})

	RegisterCommand(Command{Name: "gendata", Description: "generate test rows: :gendata <table> [rows] [seed]"}, func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		cli, id, err := opsClientOf(ctx)
		if err != nil {
			return nil, err
		}
		parts := strings.Fields(arg)
		if len(parts) == 0 {
			return nil, fmt.Errorf("用法: :gendata <table> [rows] [seed]")
		}
		req := protocol.GenDataRequest{Table: parts[0]}
		if len(parts) > 1 {
			fmt.Sscanf(parts[1], "%d", &req.Rows)
		}
		if len(parts) > 2 {
			fmt.Sscanf(parts[2], "%d", &req.Seed)
		}
		n, err := cli.GenerateTestData(context.Background(), id, &req)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("已生成 %d 行到 %s", n, req.Table)
	})
}

// currentConnID 从连接信息取 id（BaseCrumb 是表名不是连接——backend 有 ConnID）。
func currentConnID(ctx tui.AppContext) string {
	if be := ctx.Backend(); be != nil {
		if a, ok := be.(interface{ ConnID() string }); ok {
			return a.ConnID()
		}
	}
	return ""
}
