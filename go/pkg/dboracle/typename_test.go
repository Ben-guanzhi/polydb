package dboracle

import (
	"strings"
	"testing"

	go_ora "github.com/sijms/go-ora/v2"
)

// TestOracleTypeNameCoversGoOraSpellings 把**真实驱动**的拼写逐个过一遍映射。
//
// 这样写而不是硬编码一份期望表：go-ora 若改了 stringer 名（或新增类型码），
// 本测试会立刻暴露——具名拼写必须在我方映射表里**显式登记**，不能靠
// "OTHER" 兜底混过去。
func TestOracleTypeNameCoversGoOraSpellings(t *testing.T) {
	var unnamed []string
	// TNSType 是 uint8 量级的枚举，把常见码段全扫一遍。
	for code := 0; code <= 255; code++ {
		raw := go_ora.TNSType(code).String()
		got := oracleTypeName(raw)
		if got == "" {
			t.Errorf("code %d (raw=%q) 映射出空串——任何类型都必须有确定结果", code, raw)
		}
		if strings.HasPrefix(raw, "TNSType(") {
			// 该码不在 stringer map 里，驱动只能打印 "TNSType(N)"。
			// 这类默认落到 OTHER；少数已知码（7/119/252）由映射表显式登记。
			if _, explicit := oracleTypeNames[raw]; !explicit {
				unnamed = append(unnamed, raw)
			}
			continue
		}
		// 具名拼写：必须显式登记。漏登记会落成 OTHER，把一个真类型
		// 降级成 "OTHER"，且会与 Rust 侧同类型对不上。
		if _, explicit := oracleTypeNames[raw]; !explicit {
			t.Errorf("go-ora 的具名拼写 %q（码 %d）未在 oracleTypeNames 里登记"+
				"——上游可能新增/改名了类型，请补映射（当前落成 %q）", raw, code, got)
		}
	}
	t.Logf("未经具名登记、按 OTHER 兜底的码共 %d 个", len(unnamed))
}

// TestOracleTypeNameCanonical 钉住每一类的规范结果。
func TestOracleTypeNameCanonical(t *testing.T) {
	cases := []struct{ raw, want string }{
		// 字符族：这是本次修复的核心——码 1 的 stringer 名是 "NCHAR"，
		// 但码 1 是 SQLT_CHR（VARCHAR 族），报 "NCHAR" 是错的。
		{"NCHAR", "VARCHAR2"},
		{"VARCHAR", "VARCHAR2"},
		{"OCIString", "VARCHAR2"},
		{"NullStr", "VARCHAR2"},
		{"CHAR", "CHAR"},
		{"CHARZ", "CHAR"},

		// 数值。
		{"NUMBER", "NUMBER"},
		{"SB1", "NUMBER"},
		{"VarNum", "NUMBER"},
		{"UINT", "NUMBER"},
		{"TNSType(7)", "NUMBER"},
		{"FLOAT", "FLOAT"},

		// 日期时间。
		{"DATE", "DATE"},
		{"OCIDate", "DATE"},
		{"TIMESTAMP", "TIMESTAMP"},
		{"TimeStampDTY", "TIMESTAMP"},
		{"TimeStampTZ", "TIMESTAMP WITH TIME ZONE"},
		{"TimeStampTZ_DTY", "TIMESTAMP WITH TIME ZONE"},
		{"TimeStampLTZ_DTY", "TIMESTAMP WITH LOCAL TIME ZONE"},
		{"TimeStampeLTZ", "TIMESTAMP WITH LOCAL TIME ZONE"},
		{"IntervalYM", "INTERVAL YEAR TO MONTH"},
		{"IntervalYM_DTY", "INTERVAL YEAR TO MONTH"},
		{"IntervalDS", "INTERVAL DAY TO SECOND"},
		{"IntervalDS_DTY", "INTERVAL DAY TO SECOND"},
		{"TimeTZ", "TIME WITH TIME ZONE"},

		// 二进制与大对象。
		{"RAW", "RAW"},
		{"VarRaw", "RAW"},
		{"LONG", "LONG"},
		{"LongVarChar", "LONG"},
		{"LongRaw", "LONG RAW"},
		{"LongVarRaw", "LONG RAW"},
		{"OCIClobLocator", "CLOB"},
		{"OCIBlobLocator", "BLOB"},
		{"OCIFileLocator", "BFILE"},

		// 浮点。
		{"BFloat", "BINARY_FLOAT"},
		{"IBFloat", "BINARY_FLOAT"},
		{"BDouble", "BINARY_DOUBLE"},
		{"IBDouble", "BINARY_DOUBLE"},

		// 其它。
		{"ROWID", "ROWID"},
		{"UROWID", "UROWID"},
		{"TNSType(252)", "BOOLEAN"},
		{"XMLType", "XMLTYPE"},
		{"OCIXMLType", "XMLTYPE"},
		{"TNSType(119)", "JSON"},
		{"REFCURSOR", "REF CURSOR"},
		{"ResultSet", "REF CURSOR"},
		{"OCIRef", "OBJECT"},

		// 未识别 ⇒ OTHER（与 Rust 侧同一回落，保证两端一致）。
		{"TNSType(200)", "OTHER"},
		{"", "OTHER"},
	}
	for _, c := range cases {
		if got := oracleTypeName(c.raw); got != c.want {
			t.Errorf("oracleTypeName(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// TestOracleTypeNameIsUpperCase 规范名必须全大写（与 PG 的 INT4、MySQL 的
// VARCHAR、MSSQL 的 NVARCHAR 等同口径）。
func TestOracleTypeNameIsUpperCase(t *testing.T) {
	for raw, want := range oracleTypeNames {
		if want != "" && want != upper(want) {
			t.Errorf("映射 %q → %q 不是全大写", raw, want)
		}
	}
}

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}
