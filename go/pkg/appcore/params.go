package appcore

// 查询参数的 appcore 面（§384）：内核 v0.18.5 的 sqltext.ExtractParams /
// RewritePlaceholders 直驱——跨方言提取占位符并重写为目标引擎的原生标记，
// 值收集归前端（表单），绑定执行走 Execute 的原生 args 管线。

import (
	"context"

	"github.com/Ben-guanzhi/dbcore/sqltext"
	"github.com/polydb/polydb/pkg/protocol"
)

// ExtractParams 列出 SQL 的占位符（按绑定序；四式识别 + 词法安全）。
func (a *AppCore) ExtractParams(sql string) []protocol.ParamRef {
	refs := sqltext.ExtractParams(sql)
	out := make([]protocol.ParamRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, protocol.ParamRef{
			Label: r.Label(),
			Index: r.Index,
		})
	}
	return out
}

// RewritePlaceholders 把占位符重写为目标引擎的原生标记（按绑定序）。
func (a *AppCore) RewritePlaceholders(sql, dbType string) string {
	return sqltext.RewritePlaceholders(sql, sqltext.ExtractParams(sql), dbType)
}

// ExecuteWithParams 是「提取 → 前端填值 → 重写 → 绑定执行」管线的一半：
// 前端拿到 refs 的标签列表、收集同序位的值后调这里；值经 protocol.Value
// 直传（绑定而非拼接）。
func (a *AppCore) ExecuteWithParams(ctx context.Context, id, sql string, values []protocol.Value) (*protocol.QueryResult, int, error) {
	d, err := a.sqlDriver(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	// 引擎类型从连接信息取（重写目标）。
	kind := string(a.connectionKind(id))
	rewritten := sqltext.RewritePlaceholders(sql, sqltext.ExtractParams(sql), kind)
	res, err := d.Execute(ctx, rewritten, values...)
	if err != nil {
		return nil, 0, err
	}
	return res, len(values), nil
}
