package main

import (
	"strings"
)

// ─── Rust 字段 / enum 体收集与形态归类 ──────────────────────

// collectRustFields 收集 struct/variant 内联字段，返回字段与结束行号。
// start 是包含 `{` 的行号（如 "Hello {" 或 "pub struct X {"）。
func collectRustFields(lines []string, start int) ([]SideField, int, error) {
	var fields []SideField
	fp := pendingAttrs{}
	fp.reset()
	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "#[") {
			fp.feed(line)
			continue
		}
		if strings.HasPrefix(line, "}") {
			return fields, i, nil
		}
		if strings.HasSuffix(line, "{") {
			depth := 1
			for j := i + 1; j < len(lines); j++ {
				l2 := strings.TrimSpace(lines[j])
				depth += strings.Count(l2, "{")
				depth -= strings.Count(l2, "}")
				if depth <= 0 {
					i = j
					break
				}
			}
			continue
		}
		if m := rustFieldRe.FindStringSubmatch(line); m != nil {
			wire := fp.serdeKVs["rename"]
			if wire == "" {
				wire = m[1]
			}
			typ := strings.TrimSuffix(m[2], ",")
			fields = append(fields, SideField{
				Wire:    wire,
				Type:    typ,
				Pointer: strings.HasPrefix(strings.TrimSpace(typ), "Option<"),
				Default: fp.hasDefault,
				Flat:    fp.hasFlat,
			})
			fp.reset()
		}
	}
	return fields, len(lines) - 1, nil
}

// findClose 返回从 start 行开始匹配 `{` 的对应 `}` 行号（绝对行号）。
// start 行本身已包含一个 `{`（函数签名行），故初始 depth=1。
func findClose(lines []string, start int) int {
	depth := 1
	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		depth += strings.Count(line, "{")
		depth -= strings.Count(line, "}")
		if depth <= 0 {
			return i
		}
	}
	return len(lines) - 1
}

// collectRustEnum 解析 enum 体。
func collectRustEnum(lines []string, start int, pa pendingAttrs) (*SideType, int, error) {
	st := &SideType{Kind: "enum"}
	end := findClose(lines, start)
	vp := pendingAttrs{}
	vp.reset()
	for i := start; i < end; i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "#[") {
			vp.feed(line)
			continue
		}
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasSuffix(line, "{") {
			// 结构体变体：Name { fields }
			m := rustVariantRe.FindStringSubmatch(strings.TrimSuffix(line, "{"))
			if m == nil || m[1] == "" {
				continue
			}
			fields, closeLine, err := collectRustFields(lines, i)
			if err != nil {
				return nil, 0, err
			}
			st.Variants = append(st.Variants, SideVariant{
				Disc:   variantDisc(m[1], vp, pa),
				Fields: fields,
			})
			i = closeLine
			vp.reset()
			continue
		}
		// 元组/单元变体：Name(Type), 或 Name,
		bare := strings.TrimSuffix(line, ",")
		if m := rustVariantRe.FindStringSubmatch(bare); m != nil && m[2] != "{" {
			name := m[1]
			if name == "" {
				continue
			}
			content := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(bare, name), "("))
			content = strings.TrimSuffix(content, ")")
			st.Variants = append(st.Variants, SideVariant{
				Disc:   variantDisc(name, vp, pa),
				Fields: contentVariantField(content),
			})
			vp.reset()
		}
	}
	return st, end, nil
}

// contentVariantField 把元组变体内容转成单个 value 字段（tagged+content 形态）。
func contentVariantField(content string) []SideField {
	if content == "" {
		return nil
	}
	return []SideField{{Wire: "value", Type: content}}
}

func variantDisc(name string, vp, pa pendingAttrs) string {
	if d := vp.serdeKVs["rename"]; d != "" {
		return d
	}
	if pa.serdeKVs["rename_all"] == "lowercase" {
		return strings.ToLower(name)
	}
	return rustSnake(name)
}

// applyRustEnumShape 按 serde 形态归类 SideType。
func applyRustEnumShape(side *Side, st *SideType, pa pendingAttrs) {
	switch {
	case st.Untagged:
		// untagged：Value → any；其余 → 内容类型 union
		if st.Name == "Value" {
			st.Kind = "alias"
			st.Alias = "any"
			return
		}
		var alts []string
		for _, v := range st.Variants {
			for _, f := range v.Fields {
				alts = append(alts, side.Resolve(f.Type, 1))
			}
		}
		st.Kind = "alias"
		st.Alias = unionOf(alts)
	case st.Adjacent:
		// adjacently tagged（tag=type, content=value）→ 拍平 object {type, value}
		var discs, alts []string
		for _, v := range st.Variants {
			discs = append(discs, v.Disc)
			for _, f := range v.Fields {
				alts = append(alts, side.Resolve(f.Type, 1))
			}
		}
		st.Kind = "object"
		st.Fields = []SideField{
			{Wire: "type", Type: enumSig(discs)},
			{Wire: "value", Type: unionOf(alts)},
		}
	case len(st.Variants) > 0 && len(st.Variants[0].Fields) > 0 && st.Variants[0].Fields[0].Wire != "value":
		// 内部 tagged（tag=type）→ tagged 消息
		st.Kind = "tagged"
	default:
		// 普通字符串枚举
		st.Kind = "enum"
		for _, v := range st.Variants {
			st.Enum = append(st.Enum, v.Disc)
		}
	}
}
