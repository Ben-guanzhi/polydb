package appcore

// 数据字典导出（§373）：内核 dbcore.ExportDictionary 直驱——一个 schema
// 的表/列/主键/外键 → 人读的 Markdown（团队文档/评审/交接用途）。
//
// 输入走 AppCore 既有的 catalog 面（ListTables/ListColumns/ListForeignKeys，
// 内核 driver 直通），类型从 protocol 转回 dbcore（bridge.go 同模式的方向
// 反转：bridge 是 dbcore→protocol，这里是 protocol→dbcore）。注释字段
// protocol 有、内核 ColumnInfo 也有——原样带过，不丢注释。

import (
	"context"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// dictionarySource 把 AppCore 的 catalog 面包成内核 DictionarySource
// （三个方法，无需实现整个 SQLDriver）。
type dictionarySource struct {
	core   *AppCore
	connID string
}

func (s *dictionarySource) ListTables(ctx context.Context, schema string) ([]dbcore.TableInfo, error) {
	tables, err := s.core.ListTables(ctx, s.connID, schema)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.TableInfo, 0, len(tables))
	for _, t := range tables {
		if t.Type != protocol.TableTypeTable {
			continue // 字典只收 base table（与内核 ExportDictionary 的约定一致）
		}
		out = append(out, dbcore.TableInfo{Name: t.Name, Schema: t.Schema, Comment: t.Comment})
	}
	return out, nil
}

func (s *dictionarySource) ListColumns(ctx context.Context, schema, table string) ([]dbcore.ColumnInfo, error) {
	cols, err := s.core.ListColumns(ctx, s.connID, schema, table)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.ColumnInfo, 0, len(cols))
	for _, c := range cols {
		// MaxLength/Precision/Scale 不进字典——长度精度随类型文本已可见。
		out = append(out, dbcore.ColumnInfo{
			Name:         c.Name,
			Type:         c.DataType,
			Nullable:     c.Nullable,
			Default:      c.DefaultValue,
			IsPrimaryKey: c.IsPrimaryKey,
			Comment:      c.Comment,
		})
	}
	return out, nil
}

func (s *dictionarySource) ListForeignKeys(ctx context.Context, schema, table string) ([]dbcore.ForeignKeyInfo, error) {
	fks, err := s.core.ListForeignKeys(ctx, s.connID, schema, table)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.ForeignKeyInfo, 0, len(fks))
	for _, fk := range fks {
		// protocol 的 action 是 snake_case 线路名，过内核 NormalizeFKAction 归一。
		out = append(out, dbcore.ForeignKeyInfo{
			Name:       fk.Name,
			Columns:    fk.Columns,
			RefSchema:  fk.ReferencedSchema,
			RefTable:   fk.ReferencedTable,
			RefColumns: fk.ReferencedColumns,
			OnDelete:   dbcore.NormalizeFKAction(string(fk.OnDelete)),
			OnUpdate:   dbcore.NormalizeFKAction(string(fk.OnUpdate)),
		})
	}
	return out, nil
}

// ExportDictionary 导出一个连接某 schema 的 Markdown 数据字典。
// tables 为空 = 全部 base table。
func (a *AppCore) ExportDictionary(ctx context.Context, id, schema string, tables []string) (string, error) {
	if _, err := a.sqlDriver(ctx, id); err != nil {
		return "", err
	}
	src := &dictionarySource{core: a, connID: id}
	return dbcore.ExportDictionary(ctx, src, schema, tables)
}
