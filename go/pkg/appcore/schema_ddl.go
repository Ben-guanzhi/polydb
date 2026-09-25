// 结构同步语句生成（Compare & Sync E3，behavior.md §15）：
// 重算快照与计划 → 校验勾选 → 生成脚本 → 线路 DTO。
//
// 与 Rust 侧 polydb-app-core 的 schema_ddl 逐行为对齐：同一套错误文案、
// 同样的「空脚本也要序列化成 []」纪律。
package appcore

import (
	"context"

	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/schemadiff"
)

// SchemaDDL 由「连接 + 勾选条目 id」产出可执行语句脚本。服务端**重算**比对（§15.1）：
// 计划可能已过期，重算顺带做了勾选有效性校验（陈旧 id / 重建组未整组勾选都会报错）。
func (a *AppCore) SchemaDDL(ctx context.Context, req *protocol.SchemaDdlRequest) (*protocol.DdlScript, error) {
	source, err := a.collectSnapshot(ctx, req.Source)
	if err != nil {
		return nil, err
	}
	target, err := a.collectSnapshot(ctx, req.Target)
	if err != nil {
		return nil, err
	}
	opts := diffOptions(req.Options)
	plan, err := schemadiff.Diff(source, target, opts)
	if err != nil {
		return nil, err
	}
	picked, err := schemadiff.SelectEntries(source, target, plan, req.Selected)
	if err != nil {
		return nil, err
	}
	script, err := schemadiff.Generate(source, target, picked, opts)
	if err != nil {
		return nil, err
	}
	return toWireScript(script), nil
}

// toWireStatement 把纯函数生成的语句映射为线路 DTO。
func toWireStatement(s schemadiff.Statement) protocol.DdlStatement {
	return protocol.DdlStatement{
		EntryIDs:      s.EntryIDs,
		SQL:           s.SQL,
		Phase:         s.Phase,
		Table:         s.Table,
		Warnings:      s.Warnings,
		NonReversible: s.NonReversible,
	}
}

// toWireScript 把脚本映射为线路 DTO。空脚本必须落成非 nil 切片：
// 纯函数层允许 nil ≡ 空，线路层不行（前端按数组直接渲染）。
func toWireScript(s *schemadiff.Script) *protocol.DdlScript {
	out := &protocol.DdlScript{
		Statements:       []protocol.DdlStatement{},
		Rollback:         []protocol.DdlStatement{},
		PragmasOutsideTx: s.PragmasOutsideTx,
		DdlInTx:          s.DdlInTx,
	}
	for _, st := range s.Statements {
		out.Statements = append(out.Statements, toWireStatement(st))
	}
	for _, st := range s.Rollback {
		out.Rollback = append(out.Rollback, toWireStatement(st))
	}
	return out
}
