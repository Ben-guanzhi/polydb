package protocol

// 运维能力四件套的线路类型（§376）：会话管理 / 服务器变量 / 用户管理 /
// 造数——内核 ProcessLister/ServerVariablesReader/UserAdmin/GenerateInserts
// 的 polydb 线路形态。
//
// 命名与字段与内核一一对应（snake_case 线路名），转换在 appcore 完成；
// 前端（web/TUI/Rust）经既有 transport 消费，不依赖内核。

// ServerProcess 是引擎报告的一条活动会话。
type ServerProcess struct {
	ID      string `json:"id" msgpack:"id"`
	User    string `json:"user" msgpack:"user"`
	Client  string `json:"client" msgpack:"client"`
	DB      string `json:"db" msgpack:"db"`
	State   string `json:"state" msgpack:"state"`
	Command string `json:"command" msgpack:"command"`
	Query   string `json:"query,omitempty" msgpack:"query,omitempty"`
	Time    string `json:"time" msgpack:"time"`
}

// ServerVariable 是一个服务器系统变量。
type ServerVariable struct {
	Name    string `json:"name" msgpack:"name"`
	Value   string `json:"value" msgpack:"value"`
	Default string `json:"default,omitempty" msgpack:"default,omitempty"`
}

// ServerUser 是一个可登录的用户/角色（host 仅 MySQL 系）。
type ServerUser struct {
	Name string `json:"name" msgpack:"name"`
	Host string `json:"host,omitempty" msgpack:"host,omitempty"`
}

// UserOpsRequest 是用户 DDL 生成的请求（内核 UserAdmin 生成、调用方执行）。
type UserOpsRequest struct {
	Action   string `json:"action"` // create / drop / grant
	User     string `json:"user"`
	Host     string `json:"host,omitempty"`
	Password string `json:"password,omitempty"`
	GrantDB  string `json:"grant_db,omitempty"`
}

// GenDataRequest 是造数的请求（内核 GenerateInserts 生成、appcore 执行）。
type GenDataRequest struct {
	Schema string   `json:"schema,omitempty"`
	Table  string   `json:"table"`
	Rows   int      `json:"rows,omitempty"` // 0 = 默认 10
	Seed   uint64   `json:"seed,omitempty"`
	Tables []string `json:"tables,omitempty"` // 多表时走 Tables（单表走 Table）
}

// ParamRef 是一个占位符的展示形态（Label 给表单，Index 是绑定序位）。
type ParamRef struct {
	Label string `json:"label" msgpack:"label"`
	Index int    `json:"index" msgpack:"index"`
}
