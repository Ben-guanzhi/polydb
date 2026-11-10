package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PD-15（2026-11-09）：契约门禁此前只覆盖 spec ↔ wire DTO，覆盖不到驱动分派层，
// 所以 PD-01/PD-02 这类「同一语义两处手写、漂移无法机械发现」的缺陷能长期存在。
// 这里新增 dispatch 门禁，把分派层纳入 spec ↔ 实现的四方比对：
//
//  1. spec/schemas/common.json 的 DatabaseKind 枚举 是 kind 的唯一真相源；
//  2. go/pkg/protocol/common.go 的 `DatabaseKindX DatabaseKind = "..."` 常量
//     必须与 spec 逐个相等（wire 层）；
//  3. 每个 kind 必须在 go/pkg/db*/registry.go 里被 RegisterDriver 登记——
//     分派注册表是新增引擎的唯一落点（PD-01/PD-02）；
//  4. appcore 里不得再出现 `switch info.Kind` 手写分派（PD-01）：一旦出现，
//     说明有人绕过注册表另起炉灶，门禁即红灯。
//
// 该检查是纯静态文本扫描（genprotocol 不导入 app 代码，保持工具零依赖）。

var (
	kindConstRe = regexp.MustCompile(`DatabaseKind([A-Za-z0-9_]+)\s+DatabaseKind\s*=\s*"([^"]+)"`)
	registerRe  = regexp.MustCompile(`RegisterDriver\(\s*protocol\.DatabaseKind([A-Za-z0-9_]+)\s*,`)
	switchRe    = regexp.MustCompile(`switch\s+info\.Kind\b`)
)

// CheckDispatch 返回分派层漂移的诊断列表；空列表表示通过。
func CheckDispatch(repo string) []string {
	var errs []string

	// ── 1. spec 的 DatabaseKind 枚举 ──
	specKinds, err := specDatabaseKinds(repo)
	if err != nil {
		return []string{"dispatch: cannot load spec DatabaseKind enum: " + err.Error()}
	}

	// ── 2. Go protocol 常量：value → const name ──
	protocolCommon := filepath.Join(repo, "go", "pkg", "protocol", "common.go")
	pb, err := os.ReadFile(protocolCommon)
	if err != nil {
		return []string{"dispatch: cannot read " + protocolCommon + ": " + err.Error()}
	}
	constByValue := map[string]string{}
	for _, m := range kindConstRe.FindAllStringSubmatch(string(pb), -1) {
		constByValue[m[2]] = "DatabaseKind" + m[1]
	}

	// ── 3. 分派注册表登记：value → 登记文件 ──
	registeredBy := map[string]string{}
	driverDirs, _ := filepath.Glob(filepath.Join(repo, "go", "pkg", "db*"))
	for _, dir := range driverDirs {
		files, _ := filepath.Glob(filepath.Join(dir, "registry.go"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				errs = append(errs, fmt.Sprintf("dispatch: cannot read %s: %v", f, err))
				continue
			}
			for _, m := range registerRe.FindAllStringSubmatch(string(b), -1) {
				constName := "DatabaseKind" + m[1]
				val, ok := valueOfConst(constByValue, constName)
				if !ok {
					errs = append(errs, fmt.Sprintf("dispatch: %s registers unknown protocol constant protocol.%s", f, constName))
					continue
				}
				if prev, dup := registeredBy[val]; dup {
					errs = append(errs, fmt.Sprintf("dispatch: kind %q registered twice (%s and %s)", val, prev, f))
				}
				registeredBy[val] = f
			}
		}
	}

	for _, k := range sortedKeysBool(specKinds) {
		if _, ok := constByValue[k]; !ok {
			errs = append(errs, fmt.Sprintf("dispatch: spec kind %q has no Go protocol constant", k))
		}
		if _, ok := registeredBy[k]; !ok {
			errs = append(errs, fmt.Sprintf("dispatch: kind %q is in spec and Go protocol but NOT registered in pkg/db*/registry.go", k))
		}
	}
	for _, k := range sortedKeysBool(boolMap(constByValue)) {
		if _, ok := specKinds[k]; !ok {
			errs = append(errs, fmt.Sprintf("dispatch: Go protocol kind %q is not in spec DatabaseKind enum", k))
		}
	}
	for k := range registeredBy {
		if _, ok := specKinds[k]; !ok {
			errs = append(errs, fmt.Sprintf("dispatch: registered kind %q is not in spec DatabaseKind enum", k))
		}
	}

	// ── 4. appcore 不得再手写 switch info.Kind ──
	appcore := filepath.Join(repo, "go", "pkg", "appcore", "appcore.go")
	if ab, err := os.ReadFile(appcore); err == nil {
		if switchRe.Match(ab) {
			errs = append(errs, fmt.Sprintf("dispatch: %s still contains hand-written `switch info.Kind`; use pkg/dbcore.OpenDriver registry (PD-01)", appcore))
		}
	}

	// ── 5. Rust 端 open_driver 的手写 match 也必须纳入分派层门禁 ──
	//
	// PD-01 的 fallback：Rust 端不改注册表（polydb 的 protocol.* 类型无法消费
	// 内核 dbcore 注册表，见 pkg/dbcore/registry.go 头注释），因此保留手写
	// match，但门禁要能机械发现「kind ↔ 驱动 crate」漂移——否则新增引擎
	// 只改 spec 而漏改 Rust match 时，两端的分派语义会静默分叉。
	errs = append(errs, checkRustDispatch(repo, specKinds)...)

	return errs
}

// rustKindVariants 把 spec 的 kind 值映射到 Rust `DatabaseKind` 变体名。
var rustKindVariants = map[string]string{
	"sqlite":   "Sqlite",
	"mysql":    "MySql",
	"postgres": "Postgres",
	"mssql":    "Mssql",
	"oracle":   "Oracle",
	"redis":    "Redis",
}

// rustVariantCrates 把 Rust 变体名映射到它必须引用的驱动 crate 前缀。
var rustVariantCrates = map[string]string{
	"Sqlite":   "polydb_db_sqlite",
	"MySql":    "polydb_db_mysql",
	"Postgres": "polydb_db_postgres",
	"Mssql":    "polydb_db_mssql",
	"Oracle":   "polydb_db_oracle",
	"Redis":    "polydb_db_redis",
}

func rustVariantForKind(kind string) (string, bool) {
	v, ok := rustKindVariants[kind]
	return v, ok
}

func rustCrateForVariant(variant string) (string, bool) {
	c, ok := rustVariantCrates[variant]
	return c, ok
}

// checkRustDispatch 扫描 rust/crates/app-core/src/lib.rs 的 `fn open_driver`
// match 分支，断言：
//  1. 每个 spec kind 恰好有一个 `DatabaseKind::<Variant> =>` 分支；
//  2. 该分支体引用对应的驱动 crate；
//  3. 没有 `_ =>` 兜底分支（否则新增 kind 会被静默吞掉，编译期也不再报错）。
func checkRustDispatch(repo string, specKinds map[string]bool) []string {
	var errs []string
	p := filepath.Join(repo, "rust", "crates", "app-core", "src", "lib.rs")
	b, err := os.ReadFile(p)
	if err != nil {
		return []string{"dispatch: cannot read " + p + ": " + err.Error()}
	}
	src := string(b)

	// 抽出 open_driver 函数体：定位 4 空格缩进的方法定义，取到下一个方法定义。
	fnRe := regexp.MustCompile(`(?m)^    (?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+\w+`)
	fnMatches := fnRe.FindAllStringIndex(src, -1)
	region := ""
	for i, m := range fnMatches {
		if strings.Contains(src[m[0]:m[1]], "open_driver") {
			end := len(src)
			if i+1 < len(fnMatches) {
				end = fnMatches[i+1][0]
			}
			region = src[m[0]:end]
			break
		}
	}
	if region == "" {
		return []string{"dispatch: rust app-core has no `fn open_driver` (PD-01 dispatch point)"}
	}

	matchRe := regexp.MustCompile(`match\s+orig\.kind\s*\{`)
	mm := matchRe.FindStringIndex(region)
	if mm == nil {
		return []string{"dispatch: rust open_driver has no `match orig.kind`"}
	}
	// 先掩掉注释与字符串字面量再数括号，避免 `format!("{host}")` 的
	// 花括号与注释里的 `}` 干扰分支边界判定。
	masked := maskRust(region)
	braceStart := mm[1] - 1
	depth := 0
	close := -1
	for i := braceStart; i < len(masked); i++ {
		switch masked[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				close = i
				break
			}
		}
	}
	if close < 0 {
		return []string{"dispatch: rust open_driver match braces unbalanced"}
	}
	matchBody := region[braceStart+1 : close]

	armRe := regexp.MustCompile(`DatabaseKind::([A-Za-z0-9_]+)\s*=>`)
	arms := armRe.FindAllStringSubmatchIndex(matchBody, -1)
	if len(arms) == 0 {
		return []string{"dispatch: rust open_driver match has no DatabaseKind arms"}
	}

	seen := map[string]int{}
	for ai, m := range arms {
		variant := matchBody[m[2]:m[3]]
		armEnd := len(matchBody)
		if ai+1 < len(arms) {
			armEnd = arms[ai+1][0]
		}
		arm := matchBody[m[0]:armEnd]
		seen[variant]++

		crateName, ok := rustCrateForVariant(variant)
		if !ok {
			errs = append(errs, fmt.Sprintf("dispatch: rust arm DatabaseKind::%s maps to no known driver crate", variant))
			continue
		}
		if !strings.Contains(arm, crateName) {
			errs = append(errs, fmt.Sprintf("dispatch: rust arm DatabaseKind::%s does not reference %s", variant, crateName))
		}
	}

	if strings.Contains(matchBody, "_ =>") {
		errs = append(errs, "dispatch: rust open_driver match has a catch-all `_ =>` arm; a new DatabaseKind variant would be silently swallowed (PD-01)")
	}

	for _, k := range sortedKeysBool(specKinds) {
		v, ok := rustVariantForKind(k)
		if !ok {
			errs = append(errs, fmt.Sprintf("dispatch: spec kind %q has no Rust DatabaseKind variant mapping", k))
			continue
		}
		if seen[v] != 1 {
			errs = append(errs, fmt.Sprintf("dispatch: spec kind %q (Rust DatabaseKind::%s) appears %d times in open_driver, want exactly 1", k, v, seen[v]))
		}
	}
	return errs
}

// maskRust 把 Rust 源码里的行/块注释与字符串、字符字面量替换为等长空格，
// 保留换行与下标，供括号配对使用。
func maskRust(s string) string {
	b := []byte(s)
	out := make([]byte, len(b))
	copy(out, b)
	i, n := 0, len(b)
	for i < n {
		c := b[i]
		if c == '/' && i+1 < n && b[i+1] == '/' {
			for i < n && b[i] != '\n' {
				out[i] = ' '
				i++
			}
		} else if c == '/' && i+1 < n && b[i+1] == '*' {
			out[i], out[i+1] = ' ', ' '
			i += 2
			for i < n && !(b[i] == '*' && i+1 < n && b[i+1] == '/') {
				if b[i] != '\n' {
					out[i] = ' '
				}
				i++
			}
			if i < n {
				out[i] = ' '
				i++
			}
			if i < n {
				out[i] = ' '
				i++
			}
		} else if c == '"' {
			out[i] = ' '
			i++
			for i < n && b[i] != '"' {
				if b[i] == '\\' {
					out[i] = ' '
					i++
					if i < n {
						out[i] = ' '
						i++
					}
					continue
				}
				if b[i] != '\n' {
					out[i] = ' '
				}
				i++
			}
			if i < n {
				out[i] = ' '
				i++
			}
		} else if c == '\'' {
			out[i] = ' '
			i++
			for i < n && b[i] != '\'' {
				if b[i] == '\\' {
					out[i] = ' '
					i++
					if i < n {
						out[i] = ' '
						i++
					}
					continue
				}
				if b[i] == '\n' {
					break
				}
				out[i] = ' '
				i++
			}
			if i < n && b[i] == '\'' {
				out[i] = ' '
				i++
			}
		} else {
			i++
		}
	}
	return string(out)
}

// valueOfConst 从 value→const 表里反查 const 名对应的值。
func valueOfConst(byValue map[string]string, constName string) (string, bool) {
	for v, c := range byValue {
		if c == constName {
			return v, true
		}
	}
	return "", false
}

func boolMap(m map[string]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func sortedKeysBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func specDatabaseKinds(repo string) (map[string]bool, error) {
	p := filepath.Join(repo, "spec", "schemas", "common.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Definitions struct {
			DatabaseKind struct {
				Enum []string `json:"enum"`
			} `json:"DatabaseKind"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, k := range raw.Definitions.DatabaseKind.Enum {
		out[strings.TrimSpace(k)] = true
	}
	return out, nil
}
