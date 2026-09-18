package protocol

type SchemaInfo struct {
	Name string `json:"name" msgpack:"name"`
}

type TableType string

const (
	TableTypeTable            TableType = "table"
	TableTypeView             TableType = "view"
	TableTypeMaterializedView TableType = "materialized_view"
)

type TableInfo struct {
	Name     string    `json:"name" msgpack:"name"`
	Schema   string    `json:"schema" msgpack:"schema"`
	Type     TableType `json:"type" msgpack:"type"`
	RowCount *int64    `json:"row_count,omitempty" msgpack:"row_count,omitempty"`
	Comment  string    `json:"comment,omitempty" msgpack:"comment,omitempty"`
}

type ColumnInfo struct {
	Name            string      `json:"name" msgpack:"name"`
	DataType        string      `json:"data_type" msgpack:"data_type"`
	GenericType     GenericType `json:"generic_type,omitempty" msgpack:"generic_type,omitempty"`
	Nullable        bool        `json:"nullable" msgpack:"nullable"`
	DefaultValue    *string     `json:"default_value,omitempty" msgpack:"default_value,omitempty"`
	MaxLength       *int32      `json:"max_length,omitempty" msgpack:"max_length,omitempty"`
	Precision       *int32      `json:"precision,omitempty" msgpack:"precision,omitempty"`
	Scale           *int32      `json:"scale,omitempty" msgpack:"scale,omitempty"`
	IsPrimaryKey    bool        `json:"is_primary_key" msgpack:"is_primary_key"`
	IsAutoIncrement bool        `json:"is_auto_increment" msgpack:"is_auto_increment"`
	Comment         string      `json:"comment,omitempty" msgpack:"comment,omitempty"`
	OrdinalPosition int32       `json:"ordinal_position" msgpack:"ordinal_position"`
}

type IndexType string

const (
	IndexTypeBTree    IndexType = "btree"
	IndexTypeHash     IndexType = "hash"
	IndexTypeGin      IndexType = "gin"
	IndexTypeGist     IndexType = "gist"
	IndexTypeFulltext IndexType = "fulltext"
	IndexTypeSpatial  IndexType = "spatial"
	IndexTypeOther    IndexType = "other"
)

type IndexInfo struct {
	Name    string        `json:"name" msgpack:"name"`
	Unique  bool          `json:"unique" msgpack:"unique"`
	Primary bool          `json:"primary" msgpack:"primary"`
	Type    IndexType     `json:"type,omitempty" msgpack:"type,omitempty"`
	Columns []IndexColumn `json:"columns" msgpack:"columns"`
	Comment string        `json:"comment,omitempty" msgpack:"comment,omitempty"`
}

type IndexColumn struct {
	Name         string    `json:"name" msgpack:"name"`
	Position     int32     `json:"position" msgpack:"position"`
	Order        SortOrder `json:"order,omitempty" msgpack:"order,omitempty"`
	PrefixLength *int32    `json:"prefix_length,omitempty" msgpack:"prefix_length,omitempty"`
}

type ForeignKeyAction string

const (
	FKActionCascade    ForeignKeyAction = "cascade"
	FKActionSetNull    ForeignKeyAction = "set_null"
	FKActionSetDefault ForeignKeyAction = "set_default"
	FKActionRestrict   ForeignKeyAction = "restrict"
	FKActionNoAction   ForeignKeyAction = "no_action"
)

type ForeignKeyInfo struct {
	Name              string           `json:"name" msgpack:"name"`
	Columns           []string         `json:"columns" msgpack:"columns"`
	ReferencedSchema  string           `json:"referenced_schema" msgpack:"referenced_schema"`
	ReferencedTable   string           `json:"referenced_table" msgpack:"referenced_table"`
	ReferencedColumns []string         `json:"referenced_columns" msgpack:"referenced_columns"`
	OnUpdate          ForeignKeyAction `json:"on_update,omitempty" msgpack:"on_update,omitempty"`
	OnDelete          ForeignKeyAction `json:"on_delete,omitempty" msgpack:"on_delete,omitempty"`
}
