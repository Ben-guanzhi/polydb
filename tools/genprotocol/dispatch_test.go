package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckDispatch 是 PD-15 门禁的自身测试：不仅验证 happy path 通过，
// 还构造三份临时 repo 分别注入 PD-01 的三类漂移，确保门禁真的会红灯——
// 门禁自身若无测试，就又退回「靠人肉发现」的旧状态。
func TestCheckDispatch(t *testing.T) {
	base := filepath.Join(t.TempDir(), "repo")
	write(t, filepath.Join(base, "spec", "schemas", "common.json"),
		`{"definitions":{"DatabaseKind":{"enum":["sqlite","mysql","postgres","mssql","oracle","redis"]}}}`)
	write(t, filepath.Join(base, "go", "pkg", "protocol", "common.go"),
		`package protocol
type DatabaseKind string
const (
DatabaseKindSQLite DatabaseKind = "sqlite"
DatabaseKindMySQL DatabaseKind = "mysql"
DatabaseKindPostgres DatabaseKind = "postgres"
DatabaseKindMSSQL DatabaseKind = "mssql"
DatabaseKindOracle DatabaseKind = "oracle"
DatabaseKindRedis DatabaseKind = "redis"
)`)
	write(t, filepath.Join(base, "go", "pkg", "appcore", "appcore.go"), `package appcore`)
	writeRustOpenDriver(t, base, rustOpenDriverOK)

	// happy path：6 个 kind 全部登记
	for _, k := range []string{
		"SQLite", "MySQL", "Postgres", "MSSQL", "Oracle", "Redis",
	} {
		write(t, filepath.Join(base, "go", "pkg", "db_"+k, "registry.go"),
			"package db_"+k+`
import "github.com/polydb/polydb/pkg/protocol"
func init(){ protocol.RegisterDriver(protocol.DatabaseKind`+k+`, nil) }`)
	}
	if errs := CheckDispatch(base); len(errs) != 0 {
		t.Fatalf("happy path should pass, got:\n%s", strings.Join(errs, "\n"))
	}
}

// rustOpenDriverOK 是 Rust 端 open_driver 手写 match 的合法样本：每个
// DatabaseKind 变体一个分支，分支体引用对应驱动 crate，无 `_ =>` 兜底。
const rustOpenDriverOK = `    fn open_driver(
        &self,
        orig: &ConnectionInfo,
    ) -> CoreResult<Arc<dyn DatabaseDriver>> {
        match orig.kind {
            DatabaseKind::Sqlite => {
                Ok(Arc::new(polydb_db_sqlite::SqliteConn::open("x")?))
            }
            DatabaseKind::Postgres => Ok(Arc::new(polydb_db_postgres::PostgresConn::open("x")?)),
            DatabaseKind::MySql => Ok(Arc::new(polydb_db_mysql::MySqlConn::open("x")?)),
            DatabaseKind::Mssql => {
                Ok(Arc::new(polydb_db_mssql::MssqlConn::open("x")?))
            }
            DatabaseKind::Oracle => {
                Ok(Arc::new(polydb_db_oracle::OracleConn::open("x")?))
            }
            DatabaseKind::Redis => {
                Ok(Arc::new(polydb_db_redis::RedisConn::open("x")?))
            }
        }
    }

    pub fn disconnect(&self, id: ConnectionId) {
        let _ = id;
    }
`

func writeRustOpenDriver(t *testing.T, base, content string) {
	t.Helper()
	write(t, filepath.Join(base, "rust", "crates", "app-core", "src", "lib.rs"), content)
}

func TestCheckDispatchCatchesDrift(t *testing.T) {
	const fullConsts = `package protocol
type DatabaseKind string
const (
DatabaseKindSQLite DatabaseKind = "sqlite"
DatabaseKindMySQL DatabaseKind = "mysql"
DatabaseKindPostgres DatabaseKind = "postgres"
DatabaseKindMSSQL DatabaseKind = "mssql"
DatabaseKindOracle DatabaseKind = "oracle"
DatabaseKindRedis DatabaseKind = "redis"
)`

	registerAll := func(base string) {
		for _, k := range []string{"SQLite", "MySQL", "Postgres", "MSSQL", "Oracle", "Redis"} {
			write(t, filepath.Join(base, "go", "pkg", "db_"+k, "registry.go"),
				"package db_"+k+`
import "github.com/polydb/polydb/pkg/protocol"
func init(){ protocol.RegisterDriver(protocol.DatabaseKind`+k+`, nil) }`)
		}
	}

	cases := []struct {
		name   string
		mutate func(base string)
		want   string
	}{
		{
			name: "missing registration (PD-01/02)",
			mutate: func(base string) {
				rmRegistry(t, base)
				write(t, filepath.Join(base, "go", "pkg", "dbsqlite", "registry.go"), `package dbsqlite`)
			},
			want: "NOT registered",
		},
		{
			name: "hand-written switch (PD-01)",
			mutate: func(base string) {
				write(t, filepath.Join(base, "go", "pkg", "appcore", "appcore.go"),
					`package appcore
func Connect(){ switch info.Kind { } }`)
			},
			want: "switch info.Kind",
		},
		{
			name: "kind not in spec",
			mutate: func(base string) {
				write(t, filepath.Join(base, "go", "pkg", "protocol", "common.go"),
					`package protocol
type DatabaseKind string
const (
DatabaseKindSQLite DatabaseKind = "sqlite"
DatabaseKindMongo DatabaseKind = "mongo"
)`)
			},
			want: "not in spec DatabaseKind enum",
		},
		{
			name: "rust missing arm (PD-01 fallback)",
			mutate: func(base string) {
				// 删掉 Redis 分支：新引擎若只在 spec 加枚举而漏改 Rust match，
				// 编译期仍会因非穷尽而报错，但门禁要能独立于编译给出漂移诊断。
				c := strings.Replace(rustOpenDriverOK, `            DatabaseKind::Redis => {
                Ok(Arc::new(polydb_db_redis::RedisConn::open("x")?))
            }
`, "", 1)
				writeRustOpenDriver(t, base, c)
			},
			want: "Rust DatabaseKind::Redis",
		},
		{
			name: "rust wrong crate",
			mutate: func(base string) {
				c := strings.Replace(rustOpenDriverOK, "polydb_db_mysql::MySqlConn", "polydb_db_sqlite::MySqlConn", 1)
				writeRustOpenDriver(t, base, c)
			},
			want: "does not reference polydb_db_mysql",
		},
		{
			name: "rust catch-all arm",
			mutate: func(base string) {
				c := strings.Replace(rustOpenDriverOK,
					"            }\n        }\n    }\n\n    pub fn disconnect",
					"            }\n            _ => Ok(Arc::new(polydb_db_sqlite::SqliteConn::open(\"x\")?)),\n        }\n    }\n\n    pub fn disconnect", 1)
				writeRustOpenDriver(t, base, c)
			},
			want: "catch-all",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "repo")
			write(t, filepath.Join(base, "spec", "schemas", "common.json"),
				`{"definitions":{"DatabaseKind":{"enum":["sqlite","mysql","postgres","mssql","oracle","redis"]}}}`)
			write(t, filepath.Join(base, "go", "pkg", "protocol", "common.go"), fullConsts)
			write(t, filepath.Join(base, "go", "pkg", "appcore", "appcore.go"), `package appcore`)
			registerAll(base)
			writeRustOpenDriver(t, base, rustOpenDriverOK)
			tc.mutate(base)
			errs := CheckDispatch(base)
			if !strings.Contains(strings.Join(errs, "\n"), tc.want) {
				t.Fatalf("expected an error containing %q, got:\n%s", tc.want, strings.Join(errs, "\n"))
			}
		})
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rmRegistry(t *testing.T, base string) {
	t.Helper()
	dirs, _ := filepath.Glob(filepath.Join(base, "go", "pkg", "db*"))
	for _, d := range dirs {
		_ = os.RemoveAll(filepath.Join(d, "registry.go"))
	}
}
