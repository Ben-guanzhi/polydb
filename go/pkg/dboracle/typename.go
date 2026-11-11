package dboracle

// Oracle 列类型名映射（PD-28，2026-11-10）。
//
// ## 为什么需要映射
//
// spec/schemas/query.json 对 ResultColumn.type 的定义是
// **"Database-specific type name"**（数据库自己的类型名）。对 Oracle 而言
// 就是 SQL 类型名：VARCHAR2 / NUMBER / BINARY_DOUBLE / DATE …
//
// go-ora v2 的 `ColumnTypeDatabaseTypeName()` 返回的**不是** SQL 类型名，
// 而是其 `TNSType` 枚举的 stringer 名（`oracletype_string.go:59` 的
// `_OracleType_map`）——即 **OCI/TNS 协议常量名**。实测后果：
//
//	go   : id=NUMBER  s=NCHAR      r=IBDouble
//	rust : id=Number(10, 0)  s=Varchar2(50)  r=BinaryDouble
//
// 典型错位：
//
//	VARCHAR2(50)  ⇒ TNS 码 1 ⇒ stringer 名 "NCHAR"（**名字是错的**：
//	                码 1 是 SQLT_CHR，即 VARCHAR 族，不是 NCHAR）
//	BINARY_DOUBLE ⇒ TNS 码 101 ⇒ "IBDouble"（SQLT_IBDOUBLE 常量名，
//	                不是 SQL 类型名）
//
// go-ora 自己的测试就把它当既定行为钉着（`TestIssues/column_type_test.go`：
// `char`/`nchar` 都期望 "CHAR"、`varchar2`/`nvarchar2` 都期望 "NCHAR"），
// 所以**等上游改名不是可依赖的路径**；而"码 → SQL 类型名"是 Oracle 的
// 稳定公开事实（SQLT_* 常量），polydb 自己可以确定性地做。
//
// ## 分辨率上限（重要）
//
// Go 侧经由 `database/sql.ColumnType` 只能拿到 stringer 名，**拿不到字符集
// 形态**，故：
//
//	CHAR 族（码 96/97）覆盖 CHAR + NCHAR   ⇒ 归为 "CHAR"
//	VARCHAR 族（码 1）覆盖 VARCHAR2 + NVARCHAR2 ⇒ 归为 "VARCHAR2"
//
// §151（2026-11-11）起：Rust 侧 oracle crate 能区分四个变体，已改为输出
// **精确名**（NVARCHAR2/NCHAR/NCLOB）。Go 侧仍报族名——这是
// `database/sql` 接口的限制，不是 polydb 的缺陷。**两端不对称**是用户
// 在 §149 §7 明确接受的取舍（"多一次往返换精确度"：快照路径两端都已
// 查 ALL_TAB_COLUMNS 拿到精确名，查询结果路径只有 Rust 能区分）。
//
// 契约测试用 VARCHAR2 表（非 NVARCHAR2），故不受此不对称影响。
//
// 同理 NCLOB（Rust 有独立变体、Go 只有 OCIClobLocator）——Rust 报 "NCLOB"，
// Go 报 "CLOB"。
//
// 未识别的类型两端都回落到 "OTHER"——**构造性保证**任何奇类型也两端一致，
// 而不是各自输出原生拼写后再也照不上面。

// oracleTypeNames 把 go-ora 的 TNSType stringer 名映射到 Oracle SQL 类型名。
//
// 键必须与 `oracletype_string.go` 的 `_OracleType_name` 切片**逐字一致**
// （大小写敏感，例如 "SB1" 不是 "BInteger"、"ResultSet" 不是 "RESULTSET"）。
// 少数码不在该 map 里，stringer 会输出 "TNSType(<code>)"（`TNSType.String`
// 的兜底），故这里也要按该形态登记。
var oracleTypeNames = map[string]string{
	// 字符族（分辨率见文件头：族名，不含长度/字符集）。
	"NCHAR":     "VARCHAR2", // 码 1 = SQLT_CHR ⇒ VARCHAR 族（go-ora 的取名是错位的）
	"VARCHAR":   "VARCHAR2",
	"OCIString": "VARCHAR2",
	"NullStr":   "VARCHAR2", // 码 5 = SQLT_STR（null 结尾字符串），仍属字符族
	"CHAR":      "CHAR",     // 码 96 = SQLT_AFC ⇒ CHAR 族
	"CHARZ":     "CHAR",

	// 数值。
	"NUMBER":     "NUMBER",
	"SB1":        "NUMBER", // 码 3 = SQLT_INT，Oracle 用 NUMBER 承载整数
	"VarNum":     "NUMBER",
	"UINT":       "NUMBER",
	"TNSType(7)": "NUMBER", // 码 7 = SQLT_PDN（正十进制数）
	"FLOAT":      "FLOAT",  // 码 4 = SQLT_FLT

	// 日期时间。
	"DATE":             "DATE",
	"OCIDate":          "DATE",
	"TIMESTAMP":        "TIMESTAMP",
	"TimeStampDTY":     "TIMESTAMP",
	"TimeStampTZ":      "TIMESTAMP WITH TIME ZONE",
	"TimeStampTZ_DTY":  "TIMESTAMP WITH TIME ZONE",
	"TimeStampLTZ_DTY": "TIMESTAMP WITH LOCAL TIME ZONE",
	"TimeStampeLTZ":    "TIMESTAMP WITH LOCAL TIME ZONE",
	"IntervalYM":       "INTERVAL YEAR TO MONTH",
	"IntervalYM_DTY":   "INTERVAL YEAR TO MONTH",
	"IntervalDS":       "INTERVAL DAY TO SECOND",
	"IntervalDS_DTY":   "INTERVAL DAY TO SECOND",
	// 码 186 = SQLT_TIME_TZ（TIME WITH TIME ZONE）。Oracle SQL **没有**独立的
	// TIME 类型，该码只作为 OCI 的取数类型出现在表达式结果上，极少见。
	// Rust 侧 oracle crate 无对应变体（其枚举只有 Date/Timestamp*），故
	// Rust **不会**产出这个值——今天不构成两端不一致的风险；登记它是为了
	// 不让一个具名类型悄悄落成 "OTHER"。
	"TimeTZ": "TIME WITH TIME ZONE",

	// 二进制。
	"RAW":            "RAW",
	"VarRaw":         "RAW",
	"LONG":           "LONG",
	"LongVarChar":    "LONG",
	"LongRaw":        "LONG RAW",
	"LongVarRaw":     "LONG RAW",
	"OCIClobLocator": "CLOB",
	"OCIBlobLocator": "BLOB",
	"OCIFileLocator": "BFILE",

	// 浮点（Oracle 的 BINARY_FLOAT / BINARY_DOUBLE）。
	"BFloat":   "BINARY_FLOAT",  // 码 21 = SQLT_BFLOAT
	"IBFloat":  "BINARY_FLOAT",  // 码 100 = SQLT_IBFLOAT
	"BDouble":  "BINARY_DOUBLE", // 码 22 = SQLT_BDOUBLE
	"IBDouble": "BINARY_DOUBLE", // 码 101 = SQLT_IBDOUBLE

	// 其他。
	"ROWID":        "ROWID",
	"UROWID":       "UROWID",
	"TNSType(252)": "BOOLEAN", // 码 0xFC = Boolean（未进 stringer map）
	"XMLType":      "XMLTYPE",
	"OCIXMLType":   "XMLTYPE",
	"TNSType(119)": "JSON", // 码 119 = JSON（未进 stringer map）
	"REFCURSOR":    "REF CURSOR",
	"ResultSet":    "REF CURSOR",
	"OCIRef":       "OBJECT",
}

// oracleTypeName 把 go-ora 的列类型拼写规范成 Oracle SQL 类型名。
//
// 未识别的类型返回 "OTHER"（与 Rust 侧同一回落，保证两端一致）。
func oracleTypeName(raw string) string {
	if name, ok := oracleTypeNames[raw]; ok {
		return name
	}
	return "OTHER"
}
