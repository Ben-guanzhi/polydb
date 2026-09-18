package main

import (
	"sort"
	"strings"
)

// ─── 字段级比对（类型 + 可选性）────────────────────────────

func checkFieldsWithLang(
	fail func(string, ...any), spec *Spec, side *Side, name string,
	specFields []Field, sideFields []SideField, allOptional bool, lang string,
) {
	sm := map[string]Field{}
	for _, f := range specFields {
		sm[f.Wire] = f
	}
	gm := map[string]SideField{}
	for _, f := range sideFields {
		gm[f.Wire] = f
	}
	for _, w := range sortedKeysField(sm) {
		if _, ok := gm[w]; !ok {
			fail("%s.%s: field missing in code", name, w)
		}
	}
	for _, w := range sortedKeysSide(gm) {
		if _, ok := sm[w]; !ok {
			fail("%s.%s: field not in spec (code-only drift)", name, w)
		}
	}
	for w, sf := range sm {
		gf, ok := gm[w]
		if !ok {
			continue
		}
		want := spec.resolve(sf.Type)
		got := side.Resolve(gf.Type, 0)
		if !typeCompatible(want, got) {
			fail("%s.%s: type drift\n    spec: %s\n    code: %s (src: %s)", name, w, want, got, gf.Type)
		}
		if !allOptional {
			checkOptionalityFor(fail, lang, name, w, sf, gf)
		}
	}
}

// checkOptionalityFor 按端应用可选性规则：
//
//	  rust：spec.Optional 要求代码侧可容忍缺省（Option / flatten）；
//		spec required 禁止 Option（可能省略 → required 漂移），除非例外表命中。
//	  go：spec required 禁止 pointer / omitempty（可能省略）。
func checkOptionalityFor(fail func(string, ...any), lang string, name, wire string, sf Field, gf SideField) {
	key := name + "." + wire
	excepted := optionalExceptions[key] != ""
	switch lang {
	case "rust":
		if sf.Optional {
			if !gf.Pointer && !gf.Flat && !gf.Default {
				fail("%s.%s: spec optional but rust always requires the field (needs Option/flatten/serde(default))", name, wire)
			}
		} else if gf.Pointer && !excepted {
			fail("%s.%s: spec required but rust may omit (Option)", name, wire)
		}
	case "go":
		if !sf.Optional && !excepted && (gf.Pointer || gf.OmitEmpty) {
			fail("%s.%s: spec required but go may omit (pointer/omitempty)", name, wire)
		}
	}
}

// typeCompatible：any 双向兼容；类型串递归规范化（union 成员排序、去 null）后比对。
func typeCompatible(want, got string) bool {
	if want == "any" || got == "any" {
		return true
	}
	return normalizeType(want) == normalizeType(got)
}

// normalizeType 把类型串规范到可比形式：union 成员剔除 null 并排序，list/map 递归。
func normalizeType(t string) string {
	if strings.HasPrefix(t, "union:") {
		var out []string
		for _, p := range strings.Split(strings.TrimPrefix(t, "union:"), "|") {
			if p != "null" {
				out = append(out, normalizeType(p))
			}
		}
		sort.Strings(out)
		if len(out) == 1 {
			return out[0]
		}
		return "union:" + strings.Join(out, "|")
	}
	if strings.HasPrefix(t, "list:") {
		return "list:" + normalizeType(strings.TrimPrefix(t, "list:"))
	}
	if strings.HasPrefix(t, "map:") {
		return "map:" + normalizeType(strings.TrimPrefix(t, "map:"))
	}
	return t
}
