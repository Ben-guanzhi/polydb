package dbsqlite

import (
	"database/sql/driver"
	"fmt"
	"regexp"
	"strconv"

	sqlite "modernc.org/sqlite"
)

// SQLite 内置函数里没有 REGEXP：`x REGEXP y` 只是 `regexp(y, x)` 的语法糖，
// 标量函数必须由驱动注册（behavior.md §13.5）。这里注册的实现与 Rust 侧
// （rusqlite + regex crate）语义对齐：同为 RE2 家族（无后向引用、无环视）、
// 任一参数 NULL 则结果为 NULL、非法正则报错冒泡为 POLYDB_ERR_QUERY_FAILED。
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("regexp", 2, sqliteRegexp)
}

func sqliteRegexp(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	pattern, ok := regexpText(args[0])
	if !ok {
		return nil, nil
	}
	text, ok := regexpText(args[1])
	if !ok {
		return nil, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex %q: %w", pattern, err)
	}
	return re.MatchString(text), nil
}

// regexpText 把 SQLite 参数转成文本；NULL 返回 false 表示不参与匹配。
func regexpText(v driver.Value) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		return t, true
	case []byte:
		return string(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		return fmt.Sprint(t), true
	}
}
