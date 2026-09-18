package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// genprotocol：polydb 协议防漂移工具。
//
//	ts   —— 从 spec/ 生成 web/src/api/index.ts（--check 仅校验不写回）
//	check —— spec ↔ Rust ↔ Go wire 形态比对，漂移即非零退出
func main() {
	repo := repoRoot()
	if repo == "" {
		fatal("cannot locate project root (spec/schemas/common.json not found above cwd)")
	}
	specDir := flag.String("spec", filepath.Join(repo, "spec"), "spec directory")
	rustDir := flag.String("rust", filepath.Join(repo, "rust", "crates", "protocol", "src"), "rust protocol src dir")
	goDir := flag.String("go", filepath.Join(repo, "go", "pkg", "protocol"), "go protocol dir")
	tsFile := flag.String("ts", filepath.Join(repo, "web", "src", "api", "index.ts"), "TS output file")
	flag.Parse()

	cmd := flag.Arg(0)
	switch cmd {
	case "ts":
		runTS(specDir, tsFile, flag.Args()[1:]...)
	case "check":
		runCheck(*specDir, *rustDir, *goDir)
	default:
		fmt.Fprintln(os.Stderr, "usage: genprotocol ts [--check] | genprotocol check")
		os.Exit(2)
	}
}

// repoRoot 从当前目录向上查找包含 spec/schemas/common.json 的项目根。
func repoRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "spec", "schemas", "common.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func loadSpecModel(specDir string) *Spec {
	spec, err := LoadSpec(specDir)
	if err != nil {
		fatal("load spec: %v", err)
	}
	return spec
}

func runTS(specDirPtr, tsFile *string, args ...string) {
	check := false
	for _, a := range args {
		if a == "--check" {
			check = true
		}
	}
	spec := loadSpecModel(*specDirPtr)
	out := GenTS(spec)
	if check {
		cur, err := os.ReadFile(*tsFile)
		if err != nil {
			fatal("read %s: %v", *tsFile, err)
		}
		if string(cur) != out {
			fmt.Fprintf(os.Stderr, "web/src/api/index.ts is stale — run `go run polydb.dev/tools/genprotocol ts` after spec changes\n")
			os.Exit(1)
		}
		fmt.Println("TS types up to date")
		return
	}
	if err := os.MkdirAll(filepath.Dir(*tsFile), 0o755); err != nil {
		fatal(err.Error())
	}
	if err := os.WriteFile(*tsFile, []byte(out), 0o644); err != nil {
		fatal("write %s: %v", *tsFile, err)
	}
	fmt.Printf("generated %s\n", *tsFile)
}

func runCheck(specDir, rustDir, goDir string) {
	spec := loadSpecModel(specDir)
	rust, err := ParseRustDir(rustDir)
	if err != nil {
		fatal("parse rust: %v", err)
	}
	gos, err := ParseGoDir(goDir)
	if err != nil {
		fatal("parse go: %v", err)
	}
	var allErrs []string
	allErrs = append(allErrs, CheckSide(spec, rust, "rust")...)
	allErrs = append(allErrs, CheckSide(spec, gos, "go")...)
	for _, w := range rust.Warns {
		allErrs = append(allErrs, "[rust] warn: "+w)
	}
	if len(allErrs) > 0 {
		fmt.Fprintf(os.Stderr, "protocol drift detected (%d):\n\n%s\n", len(allErrs), strings.Join(allErrs, "\n"))
		os.Exit(1)
	}
	fmt.Printf("protocol check OK: %d spec types vs rust + go\n", len(spec.Order))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "genprotocol: "+format+"\n", args...)
	os.Exit(1)
}
