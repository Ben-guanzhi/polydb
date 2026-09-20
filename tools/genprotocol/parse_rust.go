package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ─── Rust 协议 crate 解析 ───────────────────────────────────

var (
	rustStructRe  = regexp.MustCompile(`^pub struct (\w+)`)
	rustEnumRe    = regexp.MustCompile(`^pub enum (\w+)`)
	rustTypeRe    = regexp.MustCompile(`^pub type (\w+)\s*=\s*(.+?);`)
	rustFieldRe   = regexp.MustCompile(`^\s*(?:pub\s+)?(\w+):\s*(.+?),?$`)
	rustVariantRe = regexp.MustCompile(`^(\w+)\s*(\{|$)`)
	serdeAttrRe   = regexp.MustCompile(`#\[\s*serde\s*\((.*)\)\s*\]`)
)

// rustSnake camelCase → snake_case（模拟 serde rename_all）。
func rustSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteString(strings.ToLower(string(r)))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type pendingAttrs struct {
	serdeKVs   map[string]string
	hasDefault bool
	hasFlat    bool
	untagged   bool
	// #[serde(other)]：解码回退变体，不属于线上枚举集合（如 FilterOperator::Unknown）。
	hasOther bool
}

func (p *pendingAttrs) reset() { *p = pendingAttrs{serdeKVs: map[string]string{}} }

func (p *pendingAttrs) feed(line string) {
	m := serdeAttrRe.FindStringSubmatch(line)
	if m == nil {
		return
	}
	for _, kv := range splitAttrs(m[1]) {
		switch kv {
		case "untagged":
			p.untagged = true
			continue
		case "default":
			p.hasDefault = true
			continue
		case "flatten":
			p.hasFlat = true
			continue
		case "other":
			p.hasOther = true
			continue
		}
		name, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "default" {
			p.hasDefault = true
		}
		p.serdeKVs[name] = strings.Trim(strings.TrimSpace(val), `"`)
	}
}

// splitAttrs 按顶层逗号切 serde 参数。
func splitAttrs(s string) []string {
	var out []string
	depth := 0
	cur := strings.Builder{}
	for _, r := range s {
		switch r {
		case '(', '<':
			depth++
		case ')', '>':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(cur.String()))
				cur.Reset()
				continue
			}
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, strings.TrimSpace(cur.String()))
	}
	return out
}

// ParseRustDir 解析 rust/crates/protocol/src 下所有 .rs。
func ParseRustDir(dir string) (*Side, error) {
	side := &Side{Types: map[string]*SideType{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".rs") || e.Name() == "lib.rs" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if err := parseRustFile(side, string(data)); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
	}
	return side, nil
}

func parseRustFile(side *Side, src string) error {
	lines := strings.Split(src, "\n")
	pa := pendingAttrs{}
	pa.reset()
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "#[") {
			pa.feed(line)
			continue
		}
		if m := rustTypeRe.FindStringSubmatch(line); m != nil {
			side.Types[m[1]] = &SideType{Name: m[1], Kind: "alias", Alias: m[2]}
			pa.reset()
			continue
		}
		if m := rustStructRe.FindStringSubmatch(line); m != nil {
			fields, next, err := collectRustFields(lines, i)
			if err != nil {
				return err
			}
			side.Types[m[1]] = &SideType{Name: m[1], Kind: "object", Fields: fields}
			pa.reset()
			// 只跳过函数体最后一行（`}` 那一行），不是整个文件剩余部分
			i = next - 1
			continue
		}
		if m := rustEnumRe.FindStringSubmatch(line); m != nil {
			st, next, err := collectRustEnum(lines, i+1, pa)
			if err != nil {
				return err
			}
			st.Name = m[1]
			st.Untagged = pa.untagged
			st.Adjacent = pa.serdeKVs["tag"] != "" && pa.serdeKVs["content"] != ""
			applyRustEnumShape(side, st, pa)
			side.Types[m[1]] = st
			pa.reset()
			i = next - 1
			continue
		}
		if strings.HasPrefix(line, "pub use") || strings.HasPrefix(line, "pub mod") {
			pa.reset()
			continue
		}
	}
	return nil
}
