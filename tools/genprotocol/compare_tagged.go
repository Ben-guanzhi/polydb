package main

import (
	"fmt"
	"sort"
	"strings"
)

// ─── tagged（WS 消息）比对与工具 ────────────────────────────

// checkTaggedRust：Rust 内部 tagged enum，按变体逐一对拍。
func checkTaggedRust(fail func(string, ...any), spec *Spec, side *Side, name string, def *TypeDef, st *SideType) {
	specVars := map[string][]Field{}
	var discs []string
	for _, v := range def.Variants {
		specVars[v.Disc] = v.Fields
		discs = append(discs, v.Disc)
	}
	sideVars := map[string][]SideField{}
	var sideDiscs []string
	for _, v := range st.Variants {
		sideVars[v.Disc] = expandFlatten(side, v.Fields)
		sideDiscs = append(sideDiscs, v.Disc)
	}
	if !equalSets(discs, sideDiscs) {
		fail("%s: variant discriminators drift\n    spec: %v\n    rust: %v", name, discs, sideDiscs)
		return
	}
	for _, v := range def.Variants {
		sideFields, ok := sideVars[v.Disc]
		if !ok {
			continue
		}
		wrapped := func(format string, args ...any) {
			fail("%s(%s): %s", name, v.Disc, fmt.Sprintf(format, args...))
		}
		checkFieldsWithLang(wrapped, spec, side, name, v.Fields, sideFields, false, "rust")
	}
}

// checkTaggedGoFlattened：Go 用单个 struct 拍平表示 tagged 消息
// （ws.go ClientMessage / ServerMessage）。比对：字段并集 + 判别值集合。
func checkTaggedGoFlattened(fail func(string, ...any), spec *Spec, side *Side, name string, def *TypeDef, st *SideType) {
	merged := map[string]Field{}
	var discs []string
	for _, v := range def.Variants {
		discs = append(discs, v.Disc)
		for _, f := range v.Fields {
			if f.Wire == "type" {
				continue
			}
			merged[f.Wire] = f
		}
	}
	var specList []Field
	for _, w := range sortedKeysField(merged) {
		f := merged[w]
		f.Optional = true // 拍平后各字段可选性随变体而异，只查字段集与类型
		specList = append(specList, f)
	}
	var goFields []SideField
	for _, f := range st.Fields {
		if f.Wire == "type" {
			continue
		}
		goFields = append(goFields, f)
	}
	wrapped := func(format string, args ...any) {
		fail("%s: %s", name, fmt.Sprintf(format, args...))
	}
	checkFieldsWithLang(wrapped, spec, side, name, specList, goFields, true, "go")

	var goDiscs []string
	for _, f := range st.Fields {
		if f.Wire == "type" {
			typeName := strings.TrimPrefix(strings.TrimPrefix(f.Type, "protocol."), "")
			if d := side.get(typeName); d != nil && (d.Kind == "enum" || d.Kind == "primitive") {
				goDiscs = d.Enum
			}
		}
	}
	if !equalSets(discs, goDiscs) {
		fail("%s: variant discriminators drift\n    spec: %v\n    go:   %v", name, discs, goDiscs)
	}
}

// expandFlatten 展开 Rust #[serde(flatten)] 字段引用的结构体字段（同名去重）。
func expandFlatten(side *Side, fields []SideField) []SideField {
	var out []SideField
	seen := map[string]bool{}
	for _, f := range fields {
		if !f.Flat {
			if !seen[f.Wire] {
				seen[f.Wire] = true
				out = append(out, f)
			}
			continue
		}
		innerName := strings.TrimSpace(f.Type)
		if i := strings.LastIndexByte(innerName, ':'); i >= 0 {
			innerName = innerName[i+1:]
		}
		inner := side.get(innerName)
		if inner == nil {
			continue
		}
		for _, ff := range inner.Fields {
			if !seen[ff.Wire] {
				seen[ff.Wire] = true
				out = append(out, ff)
			}
		}
	}
	return out
}

func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	am := map[string]bool{}
	for _, s := range a {
		am[s] = true
	}
	for _, s := range b {
		if !am[s] {
			return false
		}
	}
	return true
}

func sortedKeysField(m map[string]Field) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysSide(m map[string]SideField) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
