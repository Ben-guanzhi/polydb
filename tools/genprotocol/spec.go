package main

import (
	"fmt"
	"sort"
	"strings"
)

// ─── 规范模型 ───────────────────────────────────────────────

// Field 是 wire 层面的字段：Wire 为线上字段名。
type Field struct {
	Wire     string
	Type     string // 规范类型（str/int/num/bool/any/list:X/map:X/ref:Name/union:...）
	Optional bool
	Nullable bool
	Desc     string // schema description（生成文档注释用）
}

// Variant 是 tagged 消息的一个变体。
type Variant struct {
	Disc   string
	Fields []Field
}

// TypeDef 是一个契约类型。
type TypeDef struct {
	Name     string
	Kind     string // "object" | "tagged" | "enum" | "alias" | "skip"
	Enum     []string
	Alias    string    // alias 的规范目标（"str"/"any"/"union:..."/"ref:..."）
	Fields   []Field   // object
	Variants []Variant // tagged
}

// Spec 是全部契约类型的集合。
type Spec struct {
	Order []string
	Types map[string]*TypeDef
}

func (s *Spec) get(name string) *TypeDef { return s.Types[name] }

// enumSig 返回枚举的规范签名（值排序后拼接）。
func enumSig(values []string) string {
	v := append([]string(nil), values...)
	sort.Strings(v)
	return "enum:" + strings.Join(v, "|")
}

// resolve 把规范类型串里的命名引用解析到底（枚举→签名，别名→目标，对象引用保持）。
func (s *Spec) resolve(t string) string {
	switch {
	case strings.HasPrefix(t, "ref:"):
		name := strings.TrimPrefix(t, "ref:")
		def := s.get(name)
		if def == nil {
			return t
		}
		switch def.Kind {
		case "enum":
			return enumSig(def.Enum)
		case "alias":
			return s.resolve(def.Alias)
		default:
			// 内联枚举：查 inlineEnumRegistry 的签名
			for sig, n := range inlineEnumRegistry {
				if n == name {
					return sig
				}
			}
			return t // object / tagged 保持 ref 名
		}
	case strings.HasPrefix(t, "list:"):
		return "list:" + s.resolve(strings.TrimPrefix(t, "list:"))
	case strings.HasPrefix(t, "map:"):
		return "map:" + s.resolve(strings.TrimPrefix(t, "map:"))
	case strings.HasPrefix(t, "union:"):
		parts := strings.Split(strings.TrimPrefix(t, "union:"), "|")
		for i := range parts {
			parts[i] = s.resolve(parts[i])
		}
		sort.Strings(parts)
		return "union:" + strings.Join(unique(parts), "|")
	default:
		return t
	}
}

func unique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// collapseUnion：union 折叠。const 单值（enum:单值）在字段类型层面视作 str，
// 枚举取值集合由 tagged/enum 层单独比对。
func collapseUnion(alts []string) string {
	var out []string
	for _, a := range alts {
		if a == "null" {
			out = append(out, a)
			continue
		}
		if strings.HasPrefix(a, "enum:") {
			out = append(out, "str")
			continue
		}
		out = append(out, a)
	}
	out = unique(out)
	sort.Strings(out)
	if len(out) == 1 {
		return out[0]
	}
	return "union:" + strings.Join(out, "|")
}

var errPlaceholder = fmt.Errorf("placeholder")
