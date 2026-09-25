// 结构比对（Compare & Sync E2，behavior.md §14）：采集两侧快照 → 纯函数 diff → 线路 DTO。
//
// 与 Rust 侧 polydb-app-core 的 schema_diff 逐行为对齐：同一份 Redis 拒绝文案、
// 同样的 schema 缺省规则、同样的「显式空串等同缺省」。
package appcore

import (
	"context"
	"strings"

	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/schemadiff"
)

// defaultSchema 在请求省略 schema 时按种类取默认（behavior.md §14.5）。
// SQLite 驱动不做 schema 寻址，恒 main；其余方言传空串，由驱动用自己的当前库/模式。
func defaultSchema(kind protocol.DatabaseKind) string {
	switch kind {
	case protocol.DatabaseKindSQLite:
		return "main"
	case protocol.DatabaseKindPostgres:
		return "public"
	default:
		return ""
	}
}

// collectSnapshot 采集一个（连接, schema）的结构快照；视图/物化视图整体跳过（§14.1）。
func (a *AppCore) collectSnapshot(ctx context.Context, t protocol.SchemaDiffTarget) (*schemadiff.Snapshot, error) {
	c, err := a.ensure(ctx, t.ConnectionID)
	if err != nil {
		return nil, err
	}
	kind := c.Kind()
	if kind == protocol.DatabaseKindRedis {
		return nil, &protocol.PolyDBError{
			Code:    protocol.ErrNotSupported,
			Message: "Redis 无模式概念，不参与结构比对",
		}
	}
	sql, err := c.AsSQL()
	if err != nil {
		return nil, err
	}
	// 显式空串与缺省同义：Rust 侧 Option<String> 也做同一 trim+判空解释。
	schema := strings.TrimSpace(t.Schema)
	if schema == "" {
		schema = defaultSchema(kind)
	}
	tables, err := sql.ListTables(ctx, schema)
	if err != nil {
		return nil, err
	}
	snap := &schemadiff.Snapshot{Kind: kind, Schema: schema}
	for _, tb := range tables {
		if tb.Type != protocol.TableTypeTable {
			continue
		}
		cols, err := sql.ListColumns(ctx, schema, tb.Name)
		if err != nil {
			return nil, err
		}
		idxs, err := sql.ListIndexes(ctx, schema, tb.Name)
		if err != nil {
			return nil, err
		}
		fks, err := sql.ListForeignKeys(ctx, schema, tb.Name)
		if err != nil {
			return nil, err
		}
		snap.Tables = append(snap.Tables, schemadiff.TableSnapshot{
			Name:        tb.Name,
			Type:        tb.Type,
			Columns:     cols,
			Indexes:     idxs,
			ForeignKeys: fks,
		})
	}
	return snap, nil
}

// diffOptions 把线路选项映射为纯函数层选项：整体省略与逐字段省略都按 false 处理。
func diffOptions(o *protocol.SchemaDiffOptions) *schemadiff.Options {
	opts := &schemadiff.Options{}
	if o != nil {
		opts.IgnoreDefaults = o.IgnoreDefaults
		opts.SelectDestructive = o.SelectDestructive
	}
	return opts
}

// SchemaDiff 比对两个连接的结构差异，产出有序计划（不含 SQL，语句生成属 E3）。
func (a *AppCore) SchemaDiff(ctx context.Context, req *protocol.SchemaDiffRequest) (*protocol.SchemaDiffPlan, error) {
	source, err := a.collectSnapshot(ctx, req.Source)
	if err != nil {
		return nil, err
	}
	target, err := a.collectSnapshot(ctx, req.Target)
	if err != nil {
		return nil, err
	}
	entries, err := schemadiff.Diff(source, target, diffOptions(req.Options))
	if err != nil {
		return nil, err
	}
	plan := &protocol.SchemaDiffPlan{Entries: []protocol.SchemaDiffEntry{}}
	for _, e := range entries {
		plan.Entries = append(plan.Entries, toWireEntry(e))
	}
	return plan, nil
}

// toWireEntry 把纯函数结果映射为线路 DTO。
func toWireEntry(e schemadiff.Entry) protocol.SchemaDiffEntry {
	out := protocol.SchemaDiffEntry{
		ID:          e.ID,
		Op:          protocol.SchemaDiffOp(string(e.Op)),
		Phase:       e.Phase,
		Table:       e.Table,
		Object:      e.Object,
		From:        e.From,
		To:          e.To,
		Destructive: e.Destructive,
		Selected:    e.Selected,
		Reason:      e.Reason,
	}
	for _, f := range e.Flags {
		out.Flags = append(out.Flags, protocol.DiffColumnFlag(string(f)))
	}
	return out
}
