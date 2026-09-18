package main

import (
	"fmt"
	"sort"
	"strings"
)

// ─── 代码侧清单模型（Rust / Go 共用）────────────────────────

// SideField 代码侧字段。
type SideField struct {
	Wire      string
	Type      string // 与 spec 同词表的规范类型（含 name:X 占位，最终经 Resolve 展开）
	Pointer   bool   // Rust Option<...> / Go *T
	OmitEmpty bool   // Go omitempty
	Default   bool   // Rust #[serde(default...)]
	Flat      bool   // Rust #[serde(flatten)]
	Rename    string // serde rename（wire 名）
}

// SideType 代码侧类型定义。
type SideType struct {
	Name     string
	Kind     string        // "object" | "tagged" | "enum" | "alias"
	Fields   []SideField   // object / Go 拍平 tagged
	Variants []SideVariant // Rust tagged
	Enum     []string      // enum 取值
	Alias    string        // alias 目标（原始文本）
	EnumBase string        // Go named primitive 的底层类型（"string"）
	Untagged bool          // Rust #[serde(untagged)]
	Adjacent bool          // Rust tag+content（RedisValue）
}

// SideVariant Rust tagged 变体。
type SideVariant struct {
	Disc   string
	Fields []SideField
}

// Side 是一端代码的清单。
type Side struct {
	Types map[string]*SideType
	Warns []string
}

func (s *Side) get(name string) *SideType { return s.Types[name] }

// Resolve 把代码侧类型文本解析为规范类型串（与 Spec.Resolve 对齐）。
// ctxDepth 防循环引用。
func (s *Side) Resolve(text string, depth int) string {
	if depth > 16 {
		return "any"
	}
	t := strings.TrimSpace(text)
	t = strings.TrimPrefix(t, "&")
	switch {
	case t == "String" || t == "string" || t == "uuid::Uuid" || t == "Uuid" || t == "DateTime<Utc>" ||
		t == "chrono::DateTime<chrono::Utc>" || t == "time.Time":
		return "str"
	case t == "bool":
		return "bool"
	case t == "f64" || t == "f32" || t == "float64" || t == "float32":
		return "num"
	case isRustOrGoInt(t):
		return "int"
	case t == "serde_json::Value" || t == "json.RawMessage" || t == "interface{}" || t == "any":
		return "any"
	}
	if inner, ok := strings.CutPrefix(t, "Option<"); ok && strings.HasSuffix(inner, ">") {
		return s.Resolve(strings.TrimSuffix(inner, ">"), depth+1)
	}
	if inner, ok := strings.CutPrefix(t, "*"); ok {
		return s.Resolve(inner, depth+1)
	}
	if inner, ok := strings.CutPrefix(t, "Vec<"); ok && strings.HasSuffix(inner, ">") {
		return "list:" + s.Resolve(strings.TrimSuffix(inner, ">"), depth+1)
	}
	if inner, ok := strings.CutPrefix(t, "[]"); ok {
		return "list:" + s.Resolve(inner, depth+1)
	}
	if hm, ok := strings.CutPrefix(t, "std::collections::HashMap<"); ok {
		t = "HashMap<" + hm
	}
	if inner, ok := strings.CutPrefix(t, "HashMap<"); ok && strings.HasSuffix(inner, ">") {
		parts := splitTypeArgs(strings.TrimSuffix(inner, ">"))
		if len(parts) == 2 {
			return "map:" + s.Resolve(parts[1], depth+1)
		}
		return "map:any"
	}
	if inner, ok := strings.CutPrefix(t, "map["); ok {
		// map[string]string → map:str；key 固定 string，value 递归解析
		i := strings.Index(inner, "]")
		if i < 0 {
			return "map:any"
		}
		return "map:" + s.Resolve(inner[i+1:], depth+1)
	}
	// 命名类型：查本端定义及 goTypeOverrides
	name := t
	if i := strings.LastIndexByte(name, ':'); i >= 0 && strings.Contains(name, "::") {
		name = name[i+2:]
	}
	if ov, ok := goTypeOverrides[t]; ok {
		return ov
	}
	def := s.get(name)
	if def == nil {
		return "any"
	}
	switch def.Kind {
	case "enum":
		return enumSig(def.Enum)
	case "primitive":
		if len(def.Enum) > 0 {
			return enumSig(def.Enum)
		}
		return "str"
	case "alias":
		return s.Resolve(def.Alias, depth+1)
	default:
		return "ref:" + name
	}
}

func isRustOrGoInt(t string) bool {
	switch t {
	case "u8", "u16", "u32", "u64", "usize", "i8", "i16", "i32", "i64", "isize",
		"int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		return true
	}
	return false
}

// splitTypeArgs 按顶层逗号切分泛型参数（不含嵌套 <>）。
func splitTypeArgs(s string) []string {
	var out []string
	depth := 0
	cur := strings.Builder{}
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, cur.String())
				cur.Reset()
				continue
			}
		}
		cur.WriteRune(r)
	}
	out = append(out, cur.String())
	return out
}

// unionOf 生成 union 规范串。
func unionOf(alts []string) string {
	alts = unique(alts)
	sort.Strings(alts)
	if len(alts) == 1 {
		return alts[0]
	}
	return "union:" + strings.Join(alts, "|")
}

var errNoop = fmt.Errorf("noop")
