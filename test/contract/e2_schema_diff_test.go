package contract

import (
	"os"
	"path/filepath"
	"testing"
)

// E2 结构比对契约：POST /api/schema/diff 在 Rust / Go 双后端逐字段一致。
//
// 计划条目里 `id` 是有意契约内容（稳定标识），但 normalize 会把任意层级的 id 归为
// 占位符，故本文件先 hoist 成 diff_id 再比较，锁死 id 字面值。

// hoistDiffEntryIDs 把 entries[].id 复制一份为 diff_id（在 normalize 之前调用）。
func hoistDiffEntryIDs(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	entries, ok := m["entries"].([]any)
	if !ok {
		return m
	}
	for _, e := range entries {
		if em, ok := e.(map[string]any); ok {
			if id, ok := em["id"].(string); ok {
				em["diff_id"] = id
			}
		}
	}
	return m
}

// diffStep 调用比对端点并按 want 子集比较（want 为 nil 时只校验状态码）。
func (r *recorder) diffStep(name string, body any, wantStatus int, want any) {
	r.t.Helper()
	status, got, err := r.s.c.Do("POST", "/api/schema/diff", body)
	if err != nil {
		r.t.Errorf("%s / %s: request failed: %v", r.s.b.name, name, err)
		return
	}
	if status != wantStatus {
		r.t.Errorf("%s / %s: status = %d, want %d (body=%v)", r.s.b.name, name, status, wantStatus, got)
		return
	}
	if want == nil {
		return
	}
	var diffs []string
	diff(normalize(want), normalize(hoistDiffEntryIDs(got)), "want.vs.got", &diffs)
	for _, d := range diffs {
		r.t.Errorf("%s / %s: diff: %s", r.s.b.name, name, d)
	}
}

func diffSide(id string) map[string]any { return map[string]any{"connection_id": id} }

func diffReq(source, target string, options map[string]any) map[string]any {
	req := map[string]any{"source": diffSide(source), "target": diffSide(target)}
	if options != nil {
		req["options"] = options
	}
	return req
}

func TestContractSchemaDiffSQLite(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			dir := t.TempDir()
			src := createFileConn(r, "e2-src", filepath.Join(dir, "src.db"))
			tgt := createFileConn(r, "e2-tgt", filepath.Join(dir, "tgt.db"))

			r.execDDL("src: t", src, "CREATE TABLE t(id INTEGER PRIMARY KEY, name VARCHAR(20))")
			r.execDDL("src: dropme", src, "CREATE TABLE dropme(id INTEGER)")
			r.execDDL("tgt: t", tgt, "CREATE TABLE t(id INTEGER PRIMARY KEY, name VARCHAR(30))")
			r.execDDL("tgt: u", tgt, "CREATE TABLE u(id INTEGER)")

			// ─── 自比为空计划：[] 而非 null（web 端依赖）─────────
			r.diffStep("src 自比", diffReq(src, src, nil), 200,
				map[string]any{"entries": []any{}})
			r.diffStep("tgt 自比", diffReq(tgt, tgt, nil), 200,
				map[string]any{"entries": []any{}})

			// ─── src → tgt：按相位排序的三条动作 ────────────────
			// 破坏性条目默认不勾选（selected=false），非破坏性默认勾选。
			r.diffStep("src→tgt", diffReq(src, tgt, nil), 200, map[string]any{
				"entries": []any{
					map[string]any{
						"diff_id":     "dropme//drop_table",
						"op":          "drop_table",
						"phase":       40,
						"table":       "dropme",
						"destructive": true,
						"selected":    false,
						"reason":      "表中多余，需删除",
					},
					map[string]any{
						"diff_id":     "u//create_table",
						"op":          "create_table",
						"phase":       50,
						"table":       "u",
						"destructive": false,
						"selected":    true,
						"reason":      "表中缺失，需新建",
					},
					map[string]any{
						"diff_id":     "t/name/alter_column",
						"op":          "alter_column",
						"phase":       70,
						"table":       "t",
						"object":      "name",
						"from":        "varchar(20)",
						"to":          "varchar(30)",
						"flags":       []any{"type"},
						"destructive": false,
						"selected":    true,
						"reason":      "类型 varchar(20) → varchar(30)",
					},
				},
			})

			// ─── select_destructive=true 只翻转勾选，条目集合不变 ──
			r.diffStep("src→tgt 勾选破坏性", diffReq(src, tgt, map[string]any{"select_destructive": true}), 200,
				map[string]any{
					"entries": []any{
						map[string]any{"diff_id": "dropme//drop_table", "selected": true},
						map[string]any{"diff_id": "u//create_table", "selected": true},
						map[string]any{"diff_id": "t/name/alter_column", "selected": true},
					},
				})

			// ─── 反向：镜像计划（增删互换、类型方向互换）──────────
			r.diffStep("tgt→src", diffReq(tgt, src, nil), 200, map[string]any{
				"entries": []any{
					map[string]any{
						"diff_id":     "u//drop_table",
						"op":          "drop_table",
						"phase":       40,
						"table":       "u",
						"destructive": true,
						"selected":    false,
						"reason":      "表中多余，需删除",
					},
					map[string]any{
						"diff_id":     "dropme//create_table",
						"op":          "create_table",
						"phase":       50,
						"table":       "dropme",
						"destructive": false,
						"selected":    true,
						"reason":      "表中缺失，需新建",
					},
					map[string]any{
						"diff_id":     "t/name/alter_column",
						"op":          "alter_column",
						"phase":       70,
						"table":       "t",
						"object":      "name",
						"from":        "varchar(30)",
						"to":          "varchar(20)",
						"flags":       []any{"type"},
						"destructive": false,
						"selected":    true,
						"reason":      "类型 varchar(30) → varchar(20)",
					},
				},
			})

			// ─── 显式空串 schema 与缺省同义（behavior.md §14.5）───
			explicit := map[string]any{
				"source": map[string]any{"connection_id": src, "schema": ""},
				"target": diffSide(tgt),
			}
			r.diffStep("空串 schema", explicit, 200, map[string]any{
				"entries": []any{
					map[string]any{"diff_id": "dropme//drop_table"},
					map[string]any{"diff_id": "u//create_table"},
					map[string]any{"diff_id": "t/name/alter_column"},
				},
			})

			// ─── 未知连接 → 404 ─────────────────────────────────
			r.step("unknown connection", "POST", "/api/schema/diff",
				diffReq(src, "00000000-0000-0000-0000-000000000000", nil),
				404, map[string]any{"code": "POLYDB_ERR_CONNECTION_NOT_FOUND"})

			r.step("delete src", "DELETE", "/api/connections/"+src, nil, 204, nil)
			r.step("delete tgt", "DELETE", "/api/connections/"+tgt, nil, 204, nil)
		})
	}
}

func createFileConn(r *recorder, name, file string) string {
	r.t.Helper()
	status, got, err := r.s.c.Do("POST", "/api/connections",
		map[string]any{"name": name, "kind": "sqlite", "database": file})
	if err != nil || status != 201 {
		r.t.Fatalf("%s: create %s failed: status=%d err=%v body=%v", r.s.b.name, name, status, err, got)
	}
	id, ok := firstConnID(got)
	if !ok || id == "" {
		r.t.Fatalf("%s: no conn id in %s: %v", r.s.b.name, name, got)
	}
	return id
}

func (r *recorder) execDDL(name, connID, sql string) {
	r.t.Helper()
	r.step(name, "POST", "/api/connections/"+connID+"/query",
		map[string]any{"sql": sql}, 200, map[string]any{"statement_type": "ddl"})
}

// ─── Redis 侧：无模式概念，必须显式拒绝（两端逐字同文案）────────

func TestContractSchemaDiffRejectsRedis(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			c := NewClient(b.base)
			r := &recorder{t: t, s: &st{b: b, c: c}}
			id := createRedisConnForDiff(r)
			if id == "" {
				return // 无 Redis 实例：soft-skip
			}
			r.diffStep("redis 参与比对", diffReq(id, id, nil), 500, map[string]any{
				"code":    "POLYDB_ERR_NOT_SUPPORTED",
				"message": "Redis 无模式概念，不参与结构比对",
			})
			r.step("delete", "DELETE", "/api/connections/"+id, nil, 204, nil)
		})
	}
}

// createRedisConnForDiff 建一个 Redis 连接并确认连通；本机无实例时返回空串表示跳过。
func createRedisConnForDiff(r *recorder) string {
	r.t.Helper()
	status, got, err := r.s.c.Do("POST", "/api/connections", map[string]any{
		"name": "e2-redis", "kind": "redis",
		"host": "localhost", "port": int64(6379), "database": "0",
	})
	if err != nil {
		r.t.Fatalf("%s: create redis request failed: %v", r.s.b.name, err)
	}
	if status == 500 {
		r.t.Logf("%s: redis unavailable at create (%v) — 需本机 Redis 服务，跳过", r.s.b.name, got)
		return ""
	}
	if status != 201 {
		r.t.Fatalf("%s: create redis failed: status=%d body=%v", r.s.b.name, status, got)
	}
	id, _ := firstConnID(got)
	status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/test", nil)
	if err != nil || status != 200 {
		r.t.Errorf("%s: test redis failed: status=%d err=%v", r.s.b.name, status, err)
		return ""
	}
	m := normalize(got).(map[string]any)
	if connected, _ := m["connected"].(bool); !connected {
		msg, _ := m["error"].(string)
		r.t.Logf("%s: redis unavailable (%s) — 跳过", r.s.b.name, msg)
		return ""
	}
	return id
}

// ─── 网络方言：有实例时自比必须为空计划；无实例 soft-skip ────────
//
// 四方言的 on_update/on_delete 与 generic_type 字段未复核（plan §8-7：随 E3 / 有实例
// 环境补齐），这里只锁住"采集不炸、自比恒空"这一条下限。
func TestContractSchemaDiffDialectSelfEmpty(t *testing.T) {
	dialects := []struct {
		kind       string
		env        string
		defaultDSN string
		host       string
		port       int64
		database   string
		username   string
	}{
		{"postgres", "POLYDB_TEST_PG", "postgres://postgres@localhost:5432/postgres?sslmode=disable", "localhost", 5432, "postgres", "postgres"},
		{"mysql", "POLYDB_TEST_MYSQL", "mysql://root@localhost:3306/mysql", "localhost", 3306, "mysql", "root"},
		{"mssql", "POLYDB_TEST_MSSQL", "mssql://sa@localhost:1433/master", "localhost", 1433, "master", "sa"},
		{"oracle", "POLYDB_TEST_ORACLE", "oracle://system@localhost:1521/ORCL", "localhost", 1521, "ORCL", "system"},
	}
	for _, d := range dialects {
		d := d
		t.Run(d.kind, func(t *testing.T) {
			dsn := os.Getenv(d.env)
			if dsn == "" {
				dsn = d.defaultDSN
			}
			host, port, database, username, password, ok := parseNetDSN(dsn, d.host, d.port, d.database, d.username, "")
			if d.kind == "postgres" {
				// parseNetDSN 只认 mysql/mssql/oracle scheme；PG 沿用既有专用解析器。
				host, port, database, username, ok = parsePGDSN(dsn)
			}
			if !ok {
				t.Skipf("invalid %s DSN: %s", d.env, dsn)
			}
			bs := backends(t)
			for _, b := range bs {
				b := b
				t.Run(b.name, func(t *testing.T) {
					r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
					selfEmptyDiffForDialect(r, d.kind, host, port, database, username, password)
				})
			}
		})
	}
}

func selfEmptyDiffForDialect(r *recorder, kind, host string, port int64, database, username, password string) {
	r.t.Helper()
	body := map[string]any{
		"name": "e2-" + kind, "kind": kind,
		"host": host, "port": port, "database": database, "username": username,
	}
	if password != "" {
		body["password"] = password
	}
	status, got, err := r.s.c.Do("POST", "/api/connections", body)
	if err != nil {
		r.t.Fatalf("%s: create %s request failed: %v", r.s.b.name, kind, err)
	}
	if status == 500 {
		r.t.Logf("%s: %s unavailable at create (%v) — 需本机 %s 服务，跳过", r.s.b.name, kind, got, kind)
		return
	}
	if status != 201 {
		r.t.Fatalf("%s: create %s failed: status=%d body=%v", r.s.b.name, kind, status, got)
	}
	id, _ := firstConnID(got)

	status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/test", nil)
	if err != nil || status != 200 {
		r.t.Errorf("%s: test %s failed: status=%d err=%v", r.s.b.name, kind, status, err)
		return
	}
	m := normalize(got).(map[string]any)
	if connected, _ := m["connected"].(bool); !connected {
		msg, _ := m["error"].(string)
		r.t.Logf("%s: %s unavailable (%s) — 跳过", r.s.b.name, kind, msg)
		r.step("delete connection", "DELETE", "/api/connections/"+id, nil, 204, nil)
		return
	}

	r.diffStep(kind+" 自比", diffReq(id, id, nil), 200, map[string]any{"entries": []any{}})
	r.step("delete connection", "DELETE", "/api/connections/"+id, nil, 204, nil)
}
