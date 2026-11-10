package main

import "fmt"

// ─── spec ↔ Rust / Go 比对 ──────────────────────────────────

// optionalExceptions：允许「spec required 但代码侧可省略」的特例（附理由）。
var optionalExceptions = map[string]string{
	// server 从 URL 路径补 connection_id，body 可不带；Rust 用默认占位值兜底。
	"BeginTransactionRequest.connection_id": "server injects from URL path",
}

// CheckSide 把一端代码清单与 spec 比对，返回漂移错误列表。
func CheckSide(spec *Spec, side *Side, label string) []string {
	var errs []string
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf("[%s] %s", label, fmt.Sprintf(format, args...)))
	}
	for _, name := range spec.Order {
		def := spec.get(name)
		if def == nil || def.Kind == "skip" || def.Kind == "alias" {
			continue
		}
		st := side.get(name)
		if st == nil {
			fail("missing type %s", name)
			continue
		}
		switch def.Kind {
		case "enum":
			if st.Kind != "enum" && st.Kind != "primitive" {
				fail("%s: spec enum but side kind=%s", name, st.Kind)
				continue
			}
			if !equalSets(def.Enum, st.Enum) {
				fail("%s: enum values drift\n    spec: %v\n    %s:  %v", name, def.Enum, label, st.Enum)
			}
		case "object":
			if st.Kind != "object" {
				fail("%s: spec object but side kind=%s", name, st.Kind)
				continue
			}
			checkFieldsWithLang(fail, spec, side, name, def.Fields, st.Fields, false, labelLang(label))
		case "tagged":
			switch st.Kind {
			case "tagged":
				checkTaggedRust(fail, spec, side, name, def, st)
			case "object":
				checkTaggedGoFlattened(fail, spec, side, name, def, st)
			default:
				fail("%s: spec tagged but side kind=%s", name, st.Kind)
			}
		}
	}
	// DocOnly 枚举（ErrorCodes）：不作为独立 wire 类型生成，但取值集合必须与
	// 双端常量逐一相等——这是错误码表（behavior.md §4）的唯一机器防线。
	for _, name := range spec.Order {
		def := spec.get(name)
		if def == nil || !def.DocOnly || def.Kind != "enum" {
			continue
		}
		st := side.get(name)
		if st == nil {
			errs = append(errs, fmt.Sprintf("[%s] missing %s constants", label, name))
			continue
		}
		if !equalSets(def.Enum, st.Enum) {
			errs = append(errs, fmt.Sprintf("[%s] %s: error code drift\n    spec: %v\n    %s:  %v",
				label, name, setDiff(def.Enum, st.Enum), label, setDiff(st.Enum, def.Enum)))
		}
	}
	return errs
}

// setDiff 返回 in a but not in b（排序），用于漂移消息的可读性。
func setDiff(a, b []string) []string {
	var out []string
	for _, v := range a {
		if !containsStr(b, v) {
			out = append(out, v)
		}
	}
	return out
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func labelLang(label string) string {
	if label == "rust" {
		return "rust"
	}
	return "go"
}
