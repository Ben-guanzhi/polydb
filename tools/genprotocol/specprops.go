package main

import (
	"fmt"
	"sort"
	"strings"
)

// ─── oneOf / property 类型 ──────────────────────────────────

// buildOneOf 处理 oneOf 定义：
//  1. common.Value 特例 → any
//  2. 全部是 $ref → 引用联合
//  3. 全部是同属性集对象 → 拍平（RedisValue 形态：type 判别 + value 联合）
//  4. 其余 → 简单类型联合
func buildOneOf(spec *Spec, file, name, ctx string, oneOf []any) (*TypeDef, error) {
	if name == "Value" {
		return &TypeDef{Name: name, Kind: "alias", Alias: "any"}, nil
	}
	allRef := true
	var refs []string
	for _, v := range oneOf {
		vm := asOmap(v)
		r := asStr(vm.get("$ref"))
		if r == "" {
			allRef = false
			break
		}
		_, n := resolveRefURL(r)
		refs = append(refs, "ref:"+n)
	}
	if allRef {
		sort.Strings(refs)
		return &TypeDef{Name: name, Kind: "alias", Alias: "union:" + strings.Join(unique(refs), "|")}, nil
	}
	var fieldNames []string
	objs := make([]*omap, 0, len(oneOf))
	simple := false
	for _, v := range oneOf {
		vm := asOmap(v)
		if asStr(vm.get("type")) != "object" {
			simple = true
			break
		}
		props := specObj(vm, "properties")
		objs = append(objs, vm)
		names := append([]string(nil), props.keys...)
		sort.Strings(names)
		if fieldNames == nil {
			fieldNames = names
		} else if strings.Join(fieldNames, ",") != strings.Join(names, ",") {
			simple = true
			break
		}
	}
	if simple {
		td, err := buildSimpleUnion(oneOf)
		if err != nil {
			return nil, err
		}
		return &TypeDef{Name: name, Kind: "alias", Alias: td.Alias}, nil
	}
	fields := make([]Field, 0, len(fieldNames))
	for _, fn := range fieldNames {
		var p *omap
		var alts []string
		var constVals []string
		allConst := true
		nullable := false
		for _, vm := range objs {
			p = asOmap(specObj(vm, "properties").get(fn))
			if c := p.get("const"); c != nil {
				constVals = append(constVals, asStr(c))
				alts = append(alts, "enum:"+asStr(c))
				continue
			}
			allConst = false
			t, nl, err := specPropType(spec, file, name+"."+fn, p)
			if err != nil {
				return nil, err
			}
			if nl {
				nullable = true
			} else {
				alts = append(alts, t)
			}
		}
		ftype := collapseUnion(alts)
		if allConst {
			// 判别字段：const 集合映射到命名枚举（如 RedisKeyType）
			sig := enumSig(constVals)
			n := lookupEnumName(spec, sig, name+"."+fn)
			if n != "" {
				registerInlineEnum(spec, n, sig)
				ftype = "ref:" + n
			}
		}
		fields = append(fields, Field{Wire: fn, Type: ftype, Nullable: nullable, Desc: asStr(p.get("description"))})
	}
	return &TypeDef{Name: name, Kind: "object", Fields: fields}, nil
}

// lookupEnumName：签名 → 命名枚举（先查 spec 既有 enum def，再查内联注册表）。
func lookupEnumName(spec *Spec, sig, path string) string {
	if spec != nil {
		for _, name := range spec.Order {
			def := spec.get(name)
			if def != nil && def.Kind == "enum" && enumSig(def.Enum) == sig {
				return name
			}
		}
	}
	return nameForInlineEnum(path, sig, inlineEnumNames[path])
}

// buildSimpleUnion 处理简单类型/数组的 oneOf。
func buildSimpleUnion(oneOf []any) (*TypeDef, error) {
	var alts []string
	for _, v := range oneOf {
		vm := asOmap(v)
		if r := asStr(vm.get("$ref")); r != "" {
			_, n := resolveRefURL(r)
			alts = append(alts, "ref:"+n)
			continue
		}
		t := asStr(vm.get("type"))
		switch t {
		case "null":
			alts = append(alts, "null")
		case "string":
			alts = append(alts, "str")
		case "integer":
			alts = append(alts, "int")
		case "number":
			alts = append(alts, "num")
		case "boolean":
			alts = append(alts, "bool")
		case "array":
			items := asOmap(vm.get("items"))
			inner, _, err := specPropType(nil, "", "", items)
			if err != nil {
				return nil, err
			}
			alts = append(alts, "list:"+inner)
		default:
			return nil, fmt.Errorf("unsupported oneOf variant type %q", t)
		}
	}
	return &TypeDef{Kind: "alias", Alias: collapseUnion(alts)}, nil
}
