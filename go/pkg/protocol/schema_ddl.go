// 结构同步语句生成（Compare & Sync E3）的线路 DTO，与 Rust polydb-protocol::schema_ddl 逐字段对齐。
//
// 纯函数生成结果（pkg/schemadiff 的 Statement/Script）与线路类型是两套东西：
// 转换在 pkg/appcore 完成，protocol 层不做 IO、也不依赖 schemadiff。
package protocol

// SchemaDdlRequest 是 POST /api/schema/ddl 的请求体：连接 + 勾选条目 id，服务端重算比对（behavior.md §15.1）。
type SchemaDdlRequest struct {
	// Source 是基准侧（当前库）。
	Source SchemaDiffTarget `json:"source" msgpack:"source"`
	// Target 是期望侧。
	Target SchemaDiffTarget `json:"target" msgpack:"target"`
	// Options 必须与比对时同一份：回滚方向按它镜像重算。
	Options *SchemaDiffOptions `json:"options,omitempty" msgpack:"options,omitempty"`
	// Selected 是勾选的变更条目 id（须来自同一次比对计划）；空数组产出空脚本。
	Selected []string `json:"selected" msgpack:"selected"`
}

// DdlStatement 是一条可执行语句。
type DdlStatement struct {
	// EntryIDs 是该语句实现的变更条目 id：直改恰好 1 个；重建的一组承载该表被吸收的全部条目；
	// PRAGMA 包裹条为空数组。
	EntryIDs []string `json:"entry_ids" msgpack:"entry_ids"`
	// SQL 已带结尾分号。
	SQL string `json:"sql" msgpack:"sql"`
	// Phase 是执行相位；PRAGMA 首条 0、末条 255。
	Phase int `json:"phase" msgpack:"phase"`
	// Table 是语句作用的表名（原样大小写）；PRAGMA 包裹条为空串，字段本身仍在。
	Table string `json:"table" msgpack:"table"`
	// Warnings 是人读告警，双实现逐字一致；无告警时省略该字段。
	Warnings []string `json:"warnings,omitempty" msgpack:"warnings,omitempty"`
	// NonReversible：承载条目里任一条不可逆 ⇒ 整条语句不可逆。
	NonReversible bool `json:"non_reversible" msgpack:"non_reversible"`
}

// DdlScript 是正向脚本 + 回滚脚本。
type DdlScript struct {
	// Statements 为空时必须是非 nil 切片（序列化成 []，不得成 null）。
	Statements []DdlStatement `json:"statements" msgpack:"statements"`
	// Rollback 是**建议脚本**：其中不可逆语句执行后无法再回退。
	Rollback []DdlStatement `json:"rollback" msgpack:"rollback"`
	// PragmasOutsideTx 为 true 时，首尾两条 PRAGMA 必须留在事务外执行（事务内是 no-op）。
	PragmasOutsideTx bool `json:"pragmas_outside_tx" msgpack:"pragmas_outside_tx"`
	// DdlInTx 是该数据库种类的 DDL 是否可进事务（SQLite 实测 true）。
	DdlInTx bool `json:"ddl_in_tx" msgpack:"ddl_in_tx"`
}
