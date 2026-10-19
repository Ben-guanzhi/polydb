package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/transport"
)

// fetchTableLimit 是 FetchTable 的默认行上限（与 sqltui 原实现的量级一致）。
const fetchTableLimit = 200

// scanKeyCap 限制一次 ScanKeys 最多返回的键数，避免大库全量扫描。
const scanKeyCap = 1000

// scanPageSize 是 Redis SCAN 每批的 COUNT。
const scanPageSize = 100

// Adapter 把一个 transport.Client 连接包装成 UI 层的 Backend/KVBackend。
// 每个打开的连接一个实例；Close 不关底层连接（生命周期归组合根）。
type Adapter struct {
	cli    transport.Client
	connID string
	info   protocol.ConnectionInfo
}

// NewAdapter 为已保存的连接构造 Adapter。调用方负责先 Connect。
func NewAdapter(cli transport.Client, info protocol.ConnectionInfo) *Adapter {
	return &Adapter{cli: cli, connID: info.ID, info: info}
}

// Info 返回底层连接信息。
func (a *Adapter) Info() protocol.ConnectionInfo { return a.info }

// ConnID 返回连接 ID。
func (a *Adapter) ConnID() string { return a.connID }

// Kind 实现 Backend。
func (a *Adapter) Kind() string { return string(a.info.Kind) }

// Title 实现 Backend/KVBackend。
func (a *Adapter) Title() string { return a.info.Name }

// Run 实现 Backend：执行一条语句，查询返回 Frame，其余返回 Exec。
func (a *Adapter) Run(stmt string) (Result, error) {
	res, err := a.cli.Execute(context.Background(), a.connID, stmt)
	if err != nil {
		return Result{}, err
	}
	return resultFromQuery(res), nil
}

// RunQuery 实现 backend.Canceller：同 Run，但带上客户端预分配的 queryID，
// 使执行期间的 Cancel 能在服务端 in-flight registry 命中。
func (a *Adapter) RunQuery(queryID, stmt string) (Result, error) {
	res, err := a.cli.ExecuteQuery(context.Background(), queryID, a.connID, stmt)
	if err != nil {
		return Result{}, err
	}
	return resultFromQuery(res), nil
}

// Cancel 实现 backend.Canceller：请求取消在途查询。Local 没有服务端 registry，
// 恒返回 (false, nil) —— 未命中不是错误，调用方据此在状态栏标注。
func (a *Adapter) Cancel(queryID string) (bool, error) {
	return a.cli.CancelQuery(context.Background(), queryID)
}

// Namespaces 实现 Backend。
func (a *Adapter) Namespaces() ([]string, error) {
	schemas, err := a.cli.ListSchemas(context.Background(), a.connID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(schemas))
	for _, s := range schemas {
		out = append(out, s.Name)
	}
	if len(out) == 0 {
		return []string{""}, nil
	}
	return out, nil
}

// Tables 实现 Backend。
func (a *Adapter) Tables(namespace string) ([]string, error) {
	tables, err := a.cli.ListTables(context.Background(), a.connID, namespace)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		out = append(out, t.Name)
	}
	return out, nil
}

// FetchTable 实现 Backend。
func (a *Adapter) FetchTable(namespace, table string, limit int) (*tuidata.Frame, error) {
	if limit <= 0 {
		limit = fetchTableLimit
	}
	res, err := a.cli.BrowseRows(context.Background(), a.connID, namespace, table, &protocol.TableRowsRequest{
		Limit: uint32(limit),
	})
	if err != nil {
		return nil, err
	}
	return frameFromRows(res.Columns, res.Rows), nil
}

// PrimaryKeys 实现 Backend：走 ListColumns 的 is_primary_key 标记，按序号排序
// （不同驱动不保证返回顺序）。
func (a *Adapter) PrimaryKeys(namespace, table string) ([]string, error) {
	cols, err := a.cli.ListColumns(context.Background(), a.connID, namespace, table)
	if err != nil {
		return nil, err
	}
	type named struct {
		name string
		ord  int32
	}
	all := make([]named, 0, len(cols))
	for _, c := range cols {
		if c.IsPrimaryKey {
			all = append(all, named{c.Name, c.OrdinalPosition})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].ord < all[j].ord })
	out := make([]string, 0, len(all))
	for _, n := range all {
		out = append(out, n.name)
	}
	return out, nil
}

// ColumnsMeta 实现 Backend。
func (a *Adapter) ColumnsMeta(namespace, table string) ([]ColumnMeta, error) {
	cols, err := a.cli.ListColumns(context.Background(), a.connID, namespace, table)
	if err != nil {
		return nil, err
	}
	out := make([]ColumnMeta, 0, len(cols))
	for _, c := range cols {
		nullable := ""
		if c.Nullable {
			nullable = "YES"
		} else {
			nullable = "NO"
		}
		def := ""
		if c.DefaultValue != nil {
			def = *c.DefaultValue
		}
		out = append(out, ColumnMeta{
			Name:       c.Name,
			DataType:   c.DataType,
			IsNullable: nullable,
			Default:    def,
			Comment:    c.Comment,
		})
	}
	return out, nil
}

// ColumnIndexTypes 实现 Backend：PK > UNIQUE > INDEX 优先级。
func (a *Adapter) ColumnIndexTypes(namespace, table string) (map[string]string, error) {
	indexes, err := a.cli.ListIndexes(context.Background(), a.connID, namespace, table)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, idx := range indexes {
		label := "INDEX"
		switch {
		case idx.Primary:
			label = "PK"
		case idx.Unique:
			label = "UNIQUE"
		}
		for _, col := range idx.Columns {
			if cur, ok := out[col.Name]; !ok || priority(label) > priority(cur) {
				out[col.Name] = label
			}
		}
	}
	return out, nil
}

func priority(label string) int {
	switch label {
	case "PK":
		return 3
	case "UNIQUE":
		return 2
	default:
		return 1
	}
}

// Close 实现 Backend/KVBackend：连接生命周期归组合根，此处无操作。
func (a *Adapter) Close() error { return nil }

// CurrentNamespace 实现可选的 Namespaced：连接配置里的默认 schema/库名
// （未配置时为空串，UI 据此把该组标记为“未连接”）。
func (a *Adapter) CurrentNamespace() string { return a.info.DefaultSchema }

// Do 实现 KVBackend。
func (a *Adapter) Do(args []string) (string, error) {
	reply, err := a.cli.ExecCommand(context.Background(), a.connID, args)
	if err != nil {
		return "", err
	}
	return renderRedisReply(reply), nil
}

// ScanKeys 实现 KVBackend：cursor 循环翻页，按类型过滤，上限 scanKeyCap。
func (a *Adapter) ScanKeys(keyType string) ([]string, error) {
	var (
		cursor uint64
		out    []string
	)
	for i := 0; i < 64; i++ { // 防御性上限：64 批 × COUNT
		page, err := a.cli.ScanKeys(context.Background(), a.connID, cursor, "*", scanPageSize)
		if err != nil {
			return nil, err
		}
		for _, k := range page.Keys {
			if keyType == "" || string(k.Type) == keyType {
				out = append(out, k.Key)
			}
		}
		cursor = page.Cursor
		if cursor == 0 || len(out) >= scanKeyCap {
			break
		}
	}
	if len(out) > scanKeyCap {
		out = out[:scanKeyCap]
	}
	return out, nil
}

// Value 实现 KVBackend。
func (a *Adapter) Value(key string) (string, error) {
	v, err := a.cli.GetValue(context.Background(), a.connID, key)
	if err != nil {
		return "", err
	}
	return renderRedisValue(v), nil
}

// SelectDB 切换到指定 Redis 库（UI 层 KV 模式专用，不属于 Backend 接口）。
func (a *Adapter) SelectDB(index int) error {
	return a.cli.SelectDB(context.Background(), a.connID, index)
}

// FetchBlob 取回 $polydb_blob 旁路引用的大字段内容（UI 层查看/编辑专用）。
func (a *Adapter) FetchBlob(blobID string) ([]byte, error) {
	return a.cli.GetBlob(context.Background(), a.connID, blobID)
}

// --- 结果转换 ----------------------------------------------------------------

// resultFromQuery 把协议查询结果转成 UI 的 Result：有列即行集，否则是 exec。
func resultFromQuery(res *protocol.QueryResult) Result {
	if len(res.Columns) > 0 {
		return Result{Frame: frameFromRows(res.Columns, res.Rows)}
	}
	return Result{Exec: &ExecResult{RowsAffected: res.AffectedRows}}
}

// frameFromRows 把协议列/行转成 tuidata.Frame。
func frameFromRows(cols []protocol.ResultColumn, rows [][]protocol.Value) *tuidata.Frame {
	cells := make([][]any, len(cols))
	for _, row := range rows {
		for c := range cols {
			var v protocol.Value
			if c < len(row) {
				v = row[c]
			}
			cells[c] = append(cells[c], valueToCell(v))
		}
	}
	columns := make([]tuidata.Column, len(cols))
	for c, col := range cols {
		columns[c] = tuidata.NewColumn(col.Name, genericToDType(col.GenericType, cells[c]), cells[c])
	}
	return &tuidata.Frame{Columns: columns}
}

// valueToCell 把协议值解成冻结契约允许的 cell 类型：
// nil|string|int64|float64|bool|time.Time。整数在 2^53 内收敛为 int64；
// blob 引用渲染为占位字符串（内容经 FetchBlob 按需取回）。
func valueToCell(v protocol.Value) any {
	if v.IsNull() {
		return nil
	}
	if n, _, isBlob := protocol.TryGetBlob(v); isBlob {
		return fmt.Sprintf("<blob %d bytes>", n)
	}
	b, err := v.MarshalJSON()
	if err != nil {
		return "?"
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return string(b)
	}
	switch t := raw.(type) {
	case nil:
		return nil
	case bool:
		return t
	case string:
		return t
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 2^53 {
			return int64(t)
		}
		return t
	default:
		return string(b)
	}
}

// genericToDType 把协议通用类型映射为 tuidata 列类型；样本值兜底推断。
func genericToDType(g protocol.GenericType, sample []any) tuidata.DType {
	switch g {
	case protocol.GenericTypeBoolean:
		return tuidata.TypeBool
	case protocol.GenericTypeSmallint, protocol.GenericTypeInteger, protocol.GenericTypeBigint:
		return tuidata.TypeInt
	case protocol.GenericTypeFloat, protocol.GenericTypeDouble, protocol.GenericTypeDecimal, protocol.GenericTypeNumeric:
		return tuidata.TypeFloat
	case protocol.GenericTypeDate:
		return tuidata.TypeDate
	case protocol.GenericTypeDatetime, protocol.GenericTypeTimestamp:
		return tuidata.TypeDatetime
	case protocol.GenericTypeText, protocol.GenericTypeVarchar, protocol.GenericTypeChar,
		protocol.GenericTypeJSON, protocol.GenericTypeJSONB, protocol.GenericTypeUUID,
		protocol.GenericTypeXML, protocol.GenericTypeBinary, protocol.GenericTypeVarbinary,
		protocol.GenericTypeBlob, protocol.GenericTypeArray, protocol.GenericTypeEnum,
		protocol.GenericTypeInterval, protocol.GenericTypeTime, protocol.GenericTypeOther, "":
		return tuidata.TypeString
	}
	return tuidata.TypeString
}

// --- Redis 渲染 --------------------------------------------------------------

func renderRedisReply(r protocol.RedisReply) string {
	switch r.Type {
	case protocol.RedisReplyNull:
		return "(nil)"
	case protocol.RedisReplyError:
		return fmt.Sprintf("(error) %v", r.Value)
	case protocol.RedisReplyInteger:
		return fmt.Sprintf("(integer) %v", r.Value)
	case protocol.RedisReplyArray:
		items, ok := r.Value.([]any)
		if !ok {
			return fmt.Sprintf("%v", r.Value)
		}
		parts := make([]string, 0, len(items))
		for i, it := range items {
			parts = append(parts, fmt.Sprintf("%d) %v", i+1, it))
		}
		return strings.Join(parts, "\n")
	default: // simple_string / bulk_string
		return fmt.Sprintf("%v", r.Value)
	}
}

func renderRedisValue(v protocol.RedisValue) string {
	switch v.Type {
	case protocol.RedisKeyTypeString:
		s, _ := v.Value.(string)
		return strconv.Quote(s)
	case protocol.RedisKeyTypeNone:
		return "(key does not exist)"
	default:
		b, err := json.Marshal(v.Value)
		if err != nil {
			return fmt.Sprintf("%v", v.Value)
		}
		return string(b)
	}
}
