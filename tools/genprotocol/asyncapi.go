package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ─── asyncapi.yaml（WebSocket 消息）──────────────────────────

func loadAsyncAPI(spec *Spec, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	comps, _ := doc["components"].(map[string]any)
	msgs, _ := comps["messages"].(map[string]any)
	if msgs == nil {
		return fmt.Errorf("%s: no components.messages", path)
	}
	channels, _ := doc["channels"].(map[string]any)
	query, _ := channels["query"].(map[string]any)
	qmsgs, _ := query["messages"].(map[string]any)
	if qmsgs == nil {
		return fmt.Errorf("%s: no channels.query.messages", path)
	}
	keys := make([]string, 0, len(qmsgs))
	for k := range qmsgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var clientOrder, serverOrder []string
	for _, key := range keys {
		refVal, _ := qmsgs[key].(string)
		if refVal == "" {
			if m, ok := qmsgs[key].(map[string]any); ok {
				refVal, _ = m["$ref"].(string)
			}
		}
		name := refName(refVal)
		if name == "" {
			continue
		}
		if name == "Hello" || name == "Query" || name == "QueryCancel" {
			clientOrder = append(clientOrder, name)
		} else {
			serverOrder = append(serverOrder, name)
		}
	}
	buildTagged := func(defName string, order []string) error {
		var variants []Variant
		for _, n := range order {
			mm, _ := msgs[n].(map[string]any)
			if mm == nil {
				return fmt.Errorf("%s: missing message %s", path, n)
			}
			payload, _ := mm["payload"].(map[string]any)
			v, err := asyncVariant(spec, payload)
			if err != nil {
				return err
			}
			variants = append(variants, v)
		}
		spec.Types[defName] = &TypeDef{Name: defName, Kind: "tagged", Variants: variants}
		spec.Order = append(spec.Order, defName)
		return nil
	}
	if err := buildTagged("ClientMessage", clientOrder); err != nil {
		return err
	}
	return buildTagged("ServerMessage", serverOrder)
}

func refName(ref string) string {
	i := strings.LastIndexByte(ref, '/')
	if i < 0 {
		return ""
	}
	return ref[i+1:]
}

// asyncVariant 把一个 WS payload schema 转成 Variant（disc 来自 type const）。
func asyncVariant(spec *Spec, payload map[string]any) (Variant, error) {
	props, _ := payload["properties"].(map[string]any)
	req, _ := payload["required"].([]any)
	reqSet := map[string]bool{}
	for _, r := range req {
		if s, ok := r.(string); ok {
			reqSet[s] = true
		}
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	disc := ""
	var fields []Field
	for _, key := range keys {
		p, _ := props[key].(map[string]any)
		if key == "type" {
			disc, _ = p["const"].(string)
			continue
		}
		t, nullable, err := yamlPropType(spec, key, p)
		if err != nil {
			return Variant{}, err
		}
		fields = append(fields, Field{Wire: key, Type: t, Optional: !reqSet[key], Nullable: nullable})
	}
	return Variant{Disc: disc, Fields: fields}, nil
}

// yamlPropType 与 specPropType 等价，但输入来自 YAML map。
func yamlPropType(spec *Spec, ctx string, p map[string]any) (string, bool, error) {
	if p == nil {
		return "any", false, nil
	}
	if r, ok := p["$ref"].(string); ok {
		_, n := resolveRefURL(r)
		return "ref:" + n, false, nil
	}
	if e, ok := p["enum"].([]any); ok {
		vals := make([]string, 0, len(e))
		for _, v := range e {
			vals = append(vals, fmt.Sprint(v))
		}
		return enumSig(vals), false, nil
	}
	var tvs []string
	switch tv := p["type"].(type) {
	case string:
		tvs = []string{tv}
	case []any:
		for _, t := range tv {
			tvs = append(tvs, fmt.Sprint(t))
		}
	default:
		return "any", false, nil
	}
	var alts []string
	nullable := false
	for _, tv := range tvs {
		if tv == "" || tv == "null" {
			nullable = true
			continue
		}
		bt, err := yamlBaseType(spec, ctx, tv, p)
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

func yamlBaseType(spec *Spec, ctx, tv string, p map[string]any) (string, error) {
	switch tv {
	case "string":
		if f, _ := p["format"].(string); f == "uuid" || f == "date-time" {
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
		items, _ := p["items"].(map[string]any)
		inner, _, err := yamlPropType(spec, ctx, items)
		if err != nil {
			return "", err
		}
		return "list:" + inner, nil
	case "object":
		add, ok := p["additionalProperties"].(map[string]any)
		if !ok {
			return "map:any", nil
		}
		inner, _, err := yamlPropType(spec, ctx, add)
		if err != nil {
			return "", err
		}
		return "map:" + inner, nil
	default:
		return "any", nil
	}
}
