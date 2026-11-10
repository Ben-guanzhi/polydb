package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ─── spec/schemas/*.json 加载 ───────────────────────────────

var schemaFiles = []string{"common.json", "connection.json", "error.json", "metadata.json", "query.json", "redis.json", "transaction.json", "ws.json"}

// inlineEnumNames：内联枚举 → 导出名（生成 TS 用）。键为「类型.属性」。
var inlineEnumNames = map[string]string{
	"TableInfo.type":         "TableType",
	"IndexInfo.type":         "IndexType",
	"RedisReply.type":        "RedisReplyType",
	"TransactionInfo.status": "TransactionStatus",
	"RedisValue.type":        "RedisKeyType",
}

// inlineObjNames：内联对象 → 导出名。键为「类型.属性」。
var inlineObjNames = map[string]string{
	"RedisScanPage.keys": "RedisKeyInfo",
}

// inlineEnumRegistry：签名 → 导出名（跨文件去重，GenericType 只导出一次）。
var inlineEnumRegistry = map[string]string{}

// LoadSpec 从 specDir 读取 schemas/*.json 与 asyncapi.yaml。
func LoadSpec(specDir string) (*Spec, error) {
	spec := &Spec{Types: map[string]*TypeDef{}}
	for _, f := range schemaFiles {
		path := filepath.Join(specDir, "schemas", f)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		doc, err := parseOrderedJSON(strings.NewReader(string(data)))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if err := loadSchemaFile(spec, f, asOmap(doc)); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if err := loadAsyncAPI(spec, filepath.Join(specDir, "asyncapi.yaml")); err != nil {
		return nil, err
	}
	return spec, nil
}

func loadSchemaFile(spec *Spec, file string, doc *omap) error {
	defs := specObj(doc, "definitions")
	if defs == nil {
		return fmt.Errorf("no definitions")
	}
	for _, name := range defs.keys {
		def := asOmap(defs.get(name))
		td, err := buildTypeDef(spec, file, name, name, def)
		if err != nil {
			return err
		}
		if td == nil {
			td = &TypeDef{Name: name, Kind: "skip"}
		}
		spec.Types[name] = td
		spec.Order = append(spec.Order, name)
	}
	return nil
}

// resolveRefURL 解析 "common.json#/definitions/X" / "#/definitions/X"。
func resolveRefURL(ref string) (file, name string) {
	if i := strings.Index(ref, "#"); i >= 0 {
		return strings.TrimSuffix(ref[:i], ".json"), strings.TrimPrefix(ref[i:], "#/definitions/")
	}
	return "", strings.TrimPrefix(ref, "#/definitions/")
}

// buildTypeDef 把一个 JSON Schema definition 转成 TypeDef。
func buildTypeDef(spec *Spec, file, name, ctxKey string, def *omap) (*TypeDef, error) {
	if def == nil {
		return nil, nil
	}
	if r := asStr(def.get("$ref")); r != "" {
		_, n := resolveRefURL(r)
		return &TypeDef{Name: name, Kind: "alias", Alias: "ref:" + n}, nil
	}
	if e := asArr(def.get("enum")); len(e) > 0 {
		td := &TypeDef{Name: name, Kind: "enum", Enum: strSlice(e)}
		if name == "ErrorCodes" {
			// ErrorCodes 是 wire 上真正会出现的错误码全集：spec 的枚举值
			// 必须与 Go/Rust 两侧的常量逐一比对，缺失或漂移即门禁红灯。
			// （曾经标 skip 视作"documentation only"，结果 Go 悄悄多出
			// PAYLOAD_TOO_LARGE、Rust 缺 TRANSACTION_NOT_FOUND 都无人发现。）
			td.DocOnly = true
		}
		return td, nil
	}
	if oneOf := asArr(def.get("oneOf")); len(oneOf) > 0 {
		return buildOneOf(spec, file, name, name, oneOf)
	}
	typ := asStr(def.get("type"))
	switch typ {
	case "string":
		td := &TypeDef{Name: name, Kind: "alias", Alias: "str"}
		if name == "ErrorCodes" {
			td.DocOnly = true // 与枚举形态同义：不进 TS 生成，但参与比对
		}
		return td, nil
	case "object":
		fields, err := buildFields(spec, file, name, def)
		if err != nil {
			return nil, err
		}
		return &TypeDef{Name: name, Kind: "object", Fields: fields}, nil
	default:
		return nil, fmt.Errorf("unsupported definition %s (type=%q)", name, typ)
	}
}
