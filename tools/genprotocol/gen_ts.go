package main

import (
	"fmt"
	"sort"
	"strings"
)

// ─── TypeScript 生成 ────────────────────────────────────────

// GenTS 从 spec 生成 web/src/api/index.ts 全文。
func GenTS(spec *Spec) string {
	var b strings.Builder
	b.WriteString("// PolyDB Protocol Types — GENERATED from spec/ by tools/genprotocol. DO NOT EDIT.\n")
	b.WriteString("// 契约唯一真相源：spec/schemas/*.json + spec/asyncapi.yaml\n\n")

	for _, name := range spec.Order {
		def := spec.get(name)
		if def == nil || def.Kind == "skip" || def.Name == "ClientMessage" || def.Name == "ServerMessage" {
			continue
		}
		switch def.Kind {
		case "enum":
			b.WriteString(fmt.Sprintf("export type %s = %s;\n\n", name, tsUnionLits(def.Enum)))
		case "alias":
			switch {
			case def.Name == "Value" && def.Alias == "any":
				b.WriteString("export type Value = null | boolean | number | string | Value[] | { [key: string]: Value };\n\n")
			case def.Alias == "str":
				b.WriteString(fmt.Sprintf("export type %s = string;\n\n", name))
			case strings.HasPrefix(def.Alias, "union:"):
				parts := strings.Split(strings.TrimPrefix(def.Alias, "union:"), "|")
				var ts []string
				for _, p := range parts {
					ts = append(ts, tsType(spec, p, name))
				}
				b.WriteString(fmt.Sprintf("export type %s = %s;\n\n", name, strings.Join(ts, " | ")))
			default:
				b.WriteString(fmt.Sprintf("export type %s = %s;\n\n", name, tsType(spec, def.Alias, name)))
			}
		case "object":
			b.WriteString(fmt.Sprintf("export interface %s {\n", name))
			for _, f := range def.Fields {
				t := tsType(spec, f.Type, name+"."+f.Wire)
				if f.Nullable {
					t += " | null"
				}
				opt := ""
				if f.Optional {
					opt = "?"
				}
				if f.Desc != "" {
					b.WriteString(fmt.Sprintf("  /** %s */\n", f.Desc))
				}
				b.WriteString(fmt.Sprintf("  %s%s: %s;\n", f.Wire, opt, t))
			}
			b.WriteString("}\n\n")
		}
	}

	// ─── WebSocket ─────────────────────────────────────────
	client := spec.get("ClientMessage")
	server := spec.get("ServerMessage")
	if client != nil {
		b.WriteString("// ─── WebSocket ────────────────────────────────────────────\n\n")
		b.WriteString("export type ClientMessage =\n  " + strings.Join(tsVariants(spec, client), "\n  | ") + ";\n\n")
	}
	if server != nil {
		b.WriteString("export type ServerMessage =\n  | " + strings.Join(tsVariants(spec, server), "\n  | ") + ";\n")
	}
	return b.String()
}

// tsVariants 生成 tagged 消息的联合成员。
func tsVariants(spec *Spec, def *TypeDef) []string {
	var out []string
	for _, v := range def.Variants {
		// Query 变体复用 QueryRequest（与双端实现一致：Rust flatten / Go 拍平）
		if def.Name == "ClientMessage" && v.Disc == "query" {
			out = append(out, fmt.Sprintf("{ type: 'query'; query_id: string } & QueryRequest"))
			continue
		}
		var parts []string
		parts = append(parts, fmt.Sprintf("type: '%s'", v.Disc))
		for _, f := range v.Fields {
			t := tsType(spec, f.Type, def.Name+"."+f.Wire)
			if f.Nullable {
				t += " | null"
			}
			opt := ""
			if f.Optional {
				opt = "?"
			}
			parts = append(parts, fmt.Sprintf("%s%s: %s", f.Wire, opt, t))
		}
		out = append(out, "{ "+strings.Join(parts, "; ")+" }")
	}
	return out
}

// tsType 规范类型 → TS 类型文本。
func tsType(spec *Spec, t, ctx string) string {
	switch {
	case t == "str":
		return "string"
	case t == "int" || t == "num":
		return "number"
	case t == "bool":
		return "boolean"
	case t == "any":
		return "unknown"
	case t == "map:any":
		return "Record<string, unknown>"
	case strings.HasPrefix(t, "map:"):
		return "Record<string, " + tsType(spec, strings.TrimPrefix(t, "map:"), ctx) + ">"
	case strings.HasPrefix(t, "list:"):
		inner := tsType(spec, strings.TrimPrefix(t, "list:"), ctx)
		if strings.Contains(inner, " ") {
			inner = "(" + inner + ")"
		}
		return inner + "[]"
	case strings.HasPrefix(t, "union:"):
		var parts []string
		for _, p := range strings.Split(strings.TrimPrefix(t, "union:"), "|") {
			parts = append(parts, tsType(spec, p, ctx))
		}
		sort.SliceStable(parts, func(i, j int) bool { return false })
		return strings.Join(parts, " | ")
	case strings.HasPrefix(t, "ref:"):
		return strings.TrimPrefix(t, "ref:")
	case strings.HasPrefix(t, "enum:"):
		// 兜底：未命名的内联枚举 → 字面量联合
		vals := strings.Split(strings.TrimPrefix(t, "enum:"), "|")
		return tsUnionLits(vals)
	default:
		return "unknown"
	}
}

func tsUnionLits(vals []string) string {
	sorted := append([]string(nil), vals...)
	sort.Strings(sorted)
	quoted := make([]string, 0, len(sorted))
	for _, v := range sorted {
		quoted = append(quoted, fmt.Sprintf("'%s'", v))
	}
	return strings.Join(quoted, " | ")
}
