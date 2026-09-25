// 结构比对（Compare & Sync E2）的线路 DTO，与 Rust polydb-protocol::schema_diff 逐字段对齐。
//
// 纯函数计算结果（pkg/schemadiff 的 Entry）与线路类型是两套东西：转换在 pkg/appcore 完成，
// protocol 层不做 IO、也不依赖 schemadiff。
package protocol

// SchemaDiffOp 是一个原子动作的线路名（与 schemadiff.Op 取值逐字一致）。
type SchemaDiffOp string

const (
	OpCreateTable    SchemaDiffOp = "create_table"
	OpDropTable      SchemaDiffOp = "drop_table"
	OpAddColumn      SchemaDiffOp = "add_column"
	OpDropColumn     SchemaDiffOp = "drop_column"
	OpAlterColumn    SchemaDiffOp = "alter_column"
	OpAddIndex       SchemaDiffOp = "add_index"
	OpDropIndex      SchemaDiffOp = "drop_index"
	OpAddForeignKey  SchemaDiffOp = "add_foreign_key"
	OpDropForeignKey SchemaDiffOp = "drop_foreign_key"
)

// DiffColumnFlag 是 alter_column 的改变维度。
type DiffColumnFlag string

const (
	FlagType          DiffColumnFlag = "type"
	FlagNullability   DiffColumnFlag = "nullability"
	FlagDefault       DiffColumnFlag = "default"
	FlagAutoIncrement DiffColumnFlag = "auto_increment"
)

// SchemaDiffEntry 是计划里的一条变更。
type SchemaDiffEntry struct {
	// ID 是稳定标识：{表名小写}/{对象名小写}/{op}。
	ID string       `json:"id" msgpack:"id"`
	Op SchemaDiffOp `json:"op" msgpack:"op"`
	// Phase 是执行相位，小的先执行（behavior.md §14.4）。
	Phase int `json:"phase" msgpack:"phase"`
	// Table 取目标侧原样大小写；删表时取源侧。
	Table string `json:"table" msgpack:"table"`
	// Object 是列名/索引名/外键名；表级动作为空。
	Object string  `json:"object,omitempty" msgpack:"object,omitempty"`
	From   *string `json:"from,omitempty" msgpack:"from,omitempty"`
	To     *string `json:"to,omitempty" msgpack:"to,omitempty"`
	// Flags 仅 alter_column 使用；无改变时必须为 nil（不得是空切片）。
	Flags       []DiffColumnFlag `json:"flags,omitempty" msgpack:"flags,omitempty"`
	Destructive bool             `json:"destructive" msgpack:"destructive"`
	Selected    bool             `json:"selected" msgpack:"selected"`
	Reason      string           `json:"reason" msgpack:"reason"`
}

// SchemaDiffPlan 是排好序的完整计划；同构时 Entries 为空切片（须序列化成 []，不得省略）。
type SchemaDiffPlan struct {
	Entries []SchemaDiffEntry `json:"entries" msgpack:"entries"`
}

// SchemaDiffTarget 指定一侧的连接与模式。
type SchemaDiffTarget struct {
	ConnectionID ConnectionID `json:"connection_id" msgpack:"connection_id"`
	// Schema 省略时由服务端按种类取默认（behavior.md §14.5；SQLite 恒 main）。
	Schema string `json:"schema,omitempty" msgpack:"schema,omitempty"`
}

// SchemaDiffOptions 是比对选项。
type SchemaDiffOptions struct {
	IgnoreDefaults    bool `json:"ignore_defaults,omitempty" msgpack:"ignore_defaults,omitempty"`
	SelectDestructive bool `json:"select_destructive,omitempty" msgpack:"select_destructive,omitempty"`
}

// SchemaDiffRequest 是 POST /api/schema/diff 的请求体。
type SchemaDiffRequest struct {
	// Source 是基准侧（要变成 Target 的样子）。
	Source SchemaDiffTarget `json:"source" msgpack:"source"`
	Target SchemaDiffTarget `json:"target" msgpack:"target"`
	// Options 整体可省略。
	Options *SchemaDiffOptions `json:"options,omitempty" msgpack:"options,omitempty"`
}
