package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
)

// ─── Go 协议包解析 ──────────────────────────────────────────

// goTypeOverrides：自定义编解码类型的规范覆盖（BatchResultItem 手写 MarshalJSON
// 将 {ok,err} 结构序列化为裸 QueryResult/PolyDBError，见 query.go 注释）。
var goTypeOverrides = map[string]string{
	"BatchResultItem": "union:ref:QueryResult|ref:PolyDBError",
	// Go 的 Value 以 json.RawMessage 裸存任意值（自定义 MarshalJSON），等价 spec 的 any。
	"Value": "any",
}

// ParseGoDir 解析 go/pkg/protocol 下所有 .go。
func ParseGoDir(dir string) (*Side, error) {
	side := &Side{Types: map[string]*SideType{}}
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				switch d.Tok {
				case token.TYPE:
					for _, spec := range d.Specs {
						ts := spec.(*ast.TypeSpec)
						st := goSideType(side, ts)
						if st != nil {
							side.Types[ts.Name.Name] = st
						}
					}
				case token.CONST:
					// 常量组：带类型的进枚举，无类型且值匹配 POLYDB_ERR_ 的归 ErrorCodes
					groupEnum := ""
					for _, spec := range d.Specs {
						vs := spec.(*ast.ValueSpec)
						if vs.Type != nil {
							if id, ok := vs.Type.(*ast.Ident); ok {
								groupEnum = id.Name
							}
						}
						for i, v := range vs.Values {
							bl, ok := v.(*ast.BasicLit)
							if !ok || bl.Kind != token.STRING {
								continue
							}
							val := strings.Trim(bl.Value, "`\"")
							target := groupEnum
							if target == "" && strings.HasPrefix(val, "POLYDB_ERR_") {
								target = "ErrorCodes"
							}
							if target == "" {
								continue
							}
							et := side.Types[target]
							if et == nil {
								et = &SideType{Name: target, Kind: "enum", EnumBase: "string"}
								side.Types[target] = et
							}
							if et.Kind != "enum" && et.Kind != "primitive" {
								continue
							}
							// ValueSpec 多名多值的情况按位置对应
							name := "const"
							if len(vs.Names) == len(vs.Values) {
								name = vs.Names[i].Name
							} else if len(vs.Names) == 1 {
								name = vs.Names[0].Name
							}
							_ = name
							et.Enum = append(et.Enum, val)
						}
					}
				}
			}
		}
	}
	return side, nil
}

// goSideType 把 TypeSpec 转成 SideType；非协议结构（方法接收者、内部类型）返回 nil。
func goSideType(side *Side, ts *ast.TypeSpec) *SideType {
	switch t := ts.Type.(type) {
	case *ast.StructType:
		st := &SideType{Name: ts.Name.Name, Kind: "object"}
		for _, fd := range t.Fields.List {
			if len(fd.Names) == 0 {
				continue // 内嵌
			}
			tag := ""
			if fd.Tag != nil {
				tag = strings.Trim(fd.Tag.Value, "`")
			}
			wire, omit := goJSONTag(tag)
			name := fd.Names[0].Name
			if wire == "-" {
				continue
			}
			if wire == "" {
				wire = strings.ToLower(name)
			}
			st.Fields = append(st.Fields, SideField{
				Wire:      wire,
				Type:      goTypeText(fd.Type),
				Pointer:   isPtr(fd.Type),
				OmitEmpty: omit,
			})
		}
		if ov, ok := goTypeOverrides[st.Name]; ok {
			st.Kind = "alias"
			st.Alias = ov
		}
		return st
	case *ast.InterfaceType:
		return nil
	case *ast.Ident:
		if ts.Assign.IsValid() {
			// type alias：type X = string
			return &SideType{Name: ts.Name.Name, Kind: "alias", Alias: t.Name}
		}
		// named primitive：type X string —— 潜在枚举容器
		return &SideType{Name: ts.Name.Name, Kind: "primitive", EnumBase: t.Name}
	case *ast.SelectorExpr:
		return &SideType{Name: ts.Name.Name, Kind: "alias", Alias: goTypeText(t)}
	case *ast.ArrayType, *ast.MapType, *ast.StarExpr:
		return &SideType{Name: ts.Name.Name, Kind: "alias", Alias: goTypeText(t)}
	}
	return nil
}

func goTypeText(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return goTypeText(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return goTypeText(t.X)
	case *ast.ArrayType:
		return "[]" + goTypeText(t.Elt)
	case *ast.MapType:
		return "map[" + goTypeText(t.Key) + "]" + goTypeText(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	default:
		return reflect.TypeOf(expr).String()
	}
}

func isPtr(expr ast.Expr) bool {
	_, ok := expr.(*ast.StarExpr)
	return ok
}

// goJSONTag 解析 struct tag 的 json 键。
// Go struct tag 的多组 key 用空格分隔：`json:"x,omitempty" msgpack:"x,omitempty"`。
func goJSONTag(tag string) (name string, omit bool) {
	fields := strings.Fields(tag)
	for _, part := range fields {
		key, val, ok := strings.Cut(part, ":")
		if !ok || key != "json" {
			continue
		}
		val = strings.Trim(val, "`\"")
		parts := strings.Split(val, ",")
		name = parts[0]
		for _, f := range parts[1:] {
			if f == "omitempty" {
				omit = true
			}
		}
		return name, omit
	}
	return "", false
}
