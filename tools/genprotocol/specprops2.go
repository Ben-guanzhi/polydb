package main

import (
	"sort"
	"strings"
)

// specPropType 把 property schema 转成规范类型，返回 (type, nullable, error)。
func specPropType(spec *Spec, file, ctx string, p *omap) (string, bool, error) {
	if p == nil {
		return "any", false, nil
	}
	if r := asStr(p.get("$ref")); r != "" {
		_, n := resolveRefURL(r)
		return "ref:" + n, false, nil
	}
	if c := p.get("const"); c != nil {
		return "enum:" + asStr(c), false, nil
	}
	if e := asArr(p.get("enum")); len(e) > 0 {
		return enumSig(strSlice(e)), false, nil
	}
	if oneOf := asArr(p.get("oneOf")); len(oneOf) > 0 {
		td, err := buildOneOf(spec, file, "", ctx, oneOf)
		if err != nil {
			return "", false, err
		}
		if td.Kind == "alias" {
			return td.Alias, false, nil
		}
		return "inline:" + ctx, false, nil
	}
	var tvs []string
	switch tv := p.get("type").(type) {
	case string:
		tvs = []string{tv}
	case []any:
		for _, t := range tv {
			tvs = append(tvs, asStr(t))
		}
	default:
		return "any", false, nil
	}
	var alts []string
	nullable := false
	for _, tv := range tvs {
		if tv == "null" {
			nullable = true
			continue
		}
		bt, err := baseTypeInfo(spec, file, ctx, tv, p)
		if err != nil {
			return "", false, err
		}
		alts = append(alts, bt)
	}
	if len(alts) == 1 {
		return alts[0], nullable, nil
	}
	sort.Strings(alts)
	return collapseUnion(alts), nullable, nil
}

func baseTypeInfo(spec *Spec, file, ctx, tv string, p *omap) (string, error) {
	switch tv {
	case "string":
		switch asStr(p.get("format")) {
		case "uuid", "date-time":
			return "str", nil
		}
		return "str", nil
	case "integer":
		return "int", nil
	case "number":
		return "num", nil
	case "boolean":
		return "bool", nil
	case "array":
		items := asOmap(p.get("items"))
		inner, _, err := specPropType(spec, file, ctx, items)
		if err != nil {
			return "", err
		}
		return "list:" + inner, nil
	case "object":
		// 内联对象（如 RedisScanPage.keys 的 items）→ 命名为独立 def 并注册
		if props := specObj(p, "properties"); props != nil && spec != nil {
			name := inlineObjNames[ctx]
			if name == "" {
				name = pascalFromPath(ctx) + "Item"
			}
			if spec.get(name) == nil {
				td, err := buildTypeDef(spec, file, name, name, p)
				if err == nil && td != nil {
					spec.Types[name] = td
					spec.Order = append(spec.Order, name)
				}
			}
			return "ref:" + name, nil
		}
		am := asOmap(p.get("additionalProperties"))
		if am == nil {
			return "map:any", nil
		}
		inner, _, err := specPropType(spec, file, ctx, am)
		if err != nil {
			return "", err
		}
		return "map:" + inner, nil
	default:
		return "any", nil
	}
}

// buildFields 构造 object 的字段列表。
func buildFields(spec *Spec, file, typeName string, def *omap) ([]Field, error) {
	props := specObj(def, "properties")
	if props == nil {
		return nil, nil
	}
	reqSet := map[string]bool{}
	for _, r := range strSlice(def.get("required")) {
		reqSet[r] = true
	}
	fields := make([]Field, 0, len(props.keys))
	for _, pn := range props.keys {
		p := asOmap(props.get(pn))
		t, nullable, err := specPropType(spec, file, typeName+"."+pn, p)
		if err != nil {
			return nil, err
		}
		if sig, ok := strings.CutPrefix(t, "enum:"); ok {
			if n := nameForInlineEnum(typeName+"."+pn, sig, inlineEnumNames[typeName+"."+pn]); n != "" {
				registerInlineEnum(spec, n, sig)
				t = "ref:" + n
			}
		}
		fields = append(fields, Field{Wire: pn, Type: t, Optional: !reqSet[pn], Nullable: nullable, Desc: asStr(p.get("description"))})
	}
	return fields, nil
}

// registerInlineEnum 把内联枚举注册为 spec 顶层类型（同名已存在则跳过）。
func registerInlineEnum(spec *Spec, name, sig string) {
	if spec == nil || spec.get(name) != nil {
		return
	}
	sig = strings.TrimPrefix(sig, "enum:")
	spec.Types[name] = &TypeDef{Name: name, Kind: "enum", Enum: strings.Split(sig, "|")}
	spec.Order = append(spec.Order, name)
}

func nameForInlineEnum(path, sig, override string) string {
	if n, ok := inlineEnumRegistry[sig]; ok {
		return n
	}
	name := override
	if name == "" {
		name = pascalFromPath(path)
	}
	inlineEnumRegistry[sig] = name
	return name
}

func pascalFromPath(path string) string {
	i := strings.IndexByte(path, '.')
	prop := path
	if i >= 0 {
		prop = path[i+1:]
	}
	prop = strings.TrimSuffix(prop, "[]")
	parts := strings.Split(prop, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}
