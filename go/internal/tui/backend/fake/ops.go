package fake

// 运维四件套的 fake 面（§377）：与 Client 接口同步——Func 字段可注入，
// 缺省返回零值/nil（与既有 stub 语义一致）。

import (
	"context"

	"github.com/polydb/polydb/pkg/protocol"
)

func (c *Client) ListServerProcesses(ctx context.Context, id string) ([]protocol.ServerProcess, error) {
	if c.ListServerProcessesFunc != nil {
		return c.ListServerProcessesFunc(ctx, id)
	}
	return nil, nil
}

func (c *Client) KillServerProcess(ctx context.Context, id, pid string) error {
	if c.KillServerProcessFunc != nil {
		return c.KillServerProcessFunc(ctx, id, pid)
	}
	return nil
}

func (c *Client) ListServerVariables(ctx context.Context, id string) ([]protocol.ServerVariable, error) {
	if c.ListServerVariablesFunc != nil {
		return c.ListServerVariablesFunc(ctx, id)
	}
	return nil, nil
}

func (c *Client) ListServerUsers(ctx context.Context, id string) ([]protocol.ServerUser, error) {
	if c.ListServerUsersFunc != nil {
		return c.ListServerUsersFunc(ctx, id)
	}
	return nil, nil
}

func (c *Client) GenerateUserDDL(ctx context.Context, id string, req *protocol.UserOpsRequest) (string, error) {
	if c.GenerateUserDDLFunc != nil {
		return c.GenerateUserDDLFunc(ctx, id, req)
	}
	return "", nil
}

func (c *Client) GenerateTestData(ctx context.Context, id string, req *protocol.GenDataRequest) (int, error) {
	if c.GenerateTestDataFunc != nil {
		return c.GenerateTestDataFunc(ctx, id, req)
	}
	return 0, nil
}
