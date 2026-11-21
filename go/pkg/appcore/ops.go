package appcore

// 运维能力四件套（§376）：内核 ProcessLister / ServerVariablesReader /
// UserAdmin / GenerateInserts 的 appcore 直驱——能力断言失败诚实报
// 「引擎不支持」（SQLite 无进程/变量/用户体系），与内核注释的分工一致：
// 会话/变量/用户是**读 + 生成 DDL**，执行仍由调用方经 ExecuteQuery 走
// 既有闸门；造数是生成 + 执行一体（appcore 的事务面包装）。

import (
	"context"
	"fmt"

	dbc "github.com/Ben-guanzhi/dbcore"
	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// ListServerProcesses 列出活动会话（不含当前连接——内核约定）。
func (a *AppCore) ListServerProcesses(ctx context.Context, id string) ([]protocol.ServerProcess, error) {
	d, err := a.kernelDriver(ctx, id)
	if err != nil {
		return nil, err
	}
	pl, ok := dbc.AsProcessLister(d)
	if !ok {
		return nil, protocolErrUnsupported("会话管理")
	}
	procs, err := pl.Processes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.ServerProcess, 0, len(procs))
	for _, p := range procs {
		out = append(out, protocol.ServerProcess{
			ID: p.ID, User: p.User, Client: p.Client, DB: p.DB,
			State: p.State, Command: p.Command, Query: p.Query, Time: p.Time,
		})
	}
	return out, nil
}

// KillServerProcess 终止一个会话（内核对「杀自己」返回错误）。
func (a *AppCore) KillServerProcess(ctx context.Context, id, pid string) error {
	d, err := a.kernelDriver(ctx, id)
	if err != nil {
		return err
	}
	pl, ok := dbc.AsProcessLister(d)
	if !ok {
		return protocolErrUnsupported("会话管理")
	}
	return pl.KillProcess(ctx, pid)
}

// ListServerVariables 列出服务器系统变量（只读——内核不提供改值）。
func (a *AppCore) ListServerVariables(ctx context.Context, id string) ([]protocol.ServerVariable, error) {
	d, err := a.kernelDriver(ctx, id)
	if err != nil {
		return nil, err
	}
	svr, ok := dbc.AsServerVariables(d)
	if !ok {
		return nil, protocolErrUnsupported("服务器变量")
	}
	vars, err := svr.ServerVariables(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.ServerVariable, 0, len(vars))
	for _, v := range vars {
		out = append(out, protocol.ServerVariable{
			Name: v.Name, Value: v.Value, Default: v.Default,
		})
	}
	return out, nil
}

// ListServerUsers 列出可登录用户。
func (a *AppCore) ListServerUsers(ctx context.Context, id string) ([]protocol.ServerUser, error) {
	d, err := a.kernelDriver(ctx, id)
	if err != nil {
		return nil, err
	}
	ua, ok := dbc.AsUserAdmin(d)
	if !ok {
		return nil, protocolErrUnsupported("用户管理")
	}
	users, err := ua.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.ServerUser, 0, len(users))
	for _, u := range users {
		out = append(out, protocol.ServerUser{Name: u.Name, Host: u.Host})
	}
	return out, nil
}

// GenerateUserDDL 生成方言正确的用户 DDL（不执行——前端确认后走 Execute）。
func (a *AppCore) GenerateUserDDL(ctx context.Context, id string, req protocol.UserOpsRequest) (string, error) {
	d, err := a.kernelDriver(ctx, id)
	if err != nil {
		return "", err
	}
	ua, ok := dbc.AsUserAdmin(d)
	if !ok {
		return "", protocolErrUnsupported("用户管理")
	}
	switch req.Action {
	case "create":
		return ua.CreateUserDDL(req.User, req.Host, req.Password), nil
	case "drop":
		return ua.DropUserDDL(req.User, req.Host), nil
	case "grant":
		return ua.GrantDatabaseDDL(req.User, req.Host, req.GrantDB), nil
	default:
		return "", fmt.Errorf("未知用户操作: %s", req.Action)
	}
}

// GenerateTestData 对一张表造数并执行（内核 GenerateInserts 生成 INSERT，
// 经 appcore.Execute 走既有闸门——每条一执行，失败即中止并报已执行数）。
func (a *AppCore) GenerateTestData(ctx context.Context, id string, req protocol.GenDataRequest) (int, error) {
	if req.Table == "" {
		return 0, fmt.Errorf("缺少 table")
	}
	kd, err := a.kernelDriver(ctx, id)
	if err != nil {
		return 0, err
	}
	d, ok := kd.AsSQL()
	if !ok {
		return 0, protocolErrUnsupported("造数（非 SQL 引擎）")
	}
	cols, err := d.ListColumns(ctx, req.Schema, req.Table)
	if err != nil {
		return 0, fmt.Errorf("读取列: %w", err)
	}
	rows := req.Rows
	if rows <= 0 {
		rows = 10
	}
	stmts := dbc.GenerateInserts(cols, dbc.GenOptions{
		Rows: rows, Seed: req.Seed, Table: req.Table, SkipPrimaryKey: true,
	})
	executed := 0
	for _, stmt := range stmts {
		if _, err := a.Execute(ctx, id, stmt); err != nil {
			return executed, fmt.Errorf("第 %d 条失败: %w（已执行 %d 条）", executed+1, err, executed)
		}
		executed++
	}
	return executed, nil
}

// kernelDriver 经 KernelDriver 逃生舱拿内核驱动（五引擎 Conn 原生满足
// 内核契约），能力断言在内核面上做——appcore 不为每个引擎写转发方法。
func (a *AppCore) kernelDriver(ctx context.Context, id string) (dbc.Driver, error) {
	c, err := a.ensure(ctx, id)
	if err != nil {
		return nil, err
	}
	var drv dbcore.Driver = c.Driver()
	if kd, ok := drv.(dbcore.KernelDriver); ok {
		if k, ok := kd.KernelDriver(); ok {
			if kd2, ok := k.(dbc.Driver); ok {
				return kd2, nil
			}
		}
	}
	return nil, protocolErrUnsupported("内核能力面（该驱动无 KernelDriver 逃生舱）")
}

// protocolErrUnsupported 是能力断言失败的统一错误文案（诚实告知而非神秘报错）。
func protocolErrUnsupported(cap string) error {
	return fmt.Errorf("当前引擎不支持%s（能力未实现；SQLite 等无此体系）", cap)
}
