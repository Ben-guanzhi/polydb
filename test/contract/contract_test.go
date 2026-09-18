package contract

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type st struct {
	b      *Backend
	c      *Client
	connID string
}

type stepFunc func(v any) any

type stepOpts struct {
	keepKeys  []string
	transform stepFunc
}

type recorder struct {
	t *testing.T
	s *st
}

func keep(k ...string) func(*stepOpts) {
	return func(o *stepOpts) { o.keepKeys = k }
}
func transform(f stepFunc) func(*stepOpts) { return func(o *stepOpts) { o.transform = f } }

// step 执行单步请求并断言：状态码 + want（归一化后子集比较）。
func (r *recorder) step(name, method, path string, body any, wantStatus int, want any, opts ...func(*stepOpts)) {
	r.t.Helper()
	o := stepOpts{}
	for _, f := range opts {
		f(&o)
	}
	gotStatus, got, err := r.s.c.Do(method, path, body)
	if err != nil {
		r.t.Errorf("%s / %s: request failed: %v", r.s.b.name, name, err)
		return
	}
	if gotStatus != wantStatus {
		r.t.Errorf("%s / %s: status = %d, want %d (body=%v)", r.s.b.name, name, gotStatus, wantStatus, got)
		return
	}
	v := normalize(got)
	if len(o.keepKeys) > 0 {
		v = keepKeys(v, o.keepKeys...)
	}
	if o.transform != nil {
		v = o.transform(v)
	}
	if want == nil {
		return
	}
	w := normalize(want)
	if len(o.keepKeys) > 0 {
		w = keepKeys(w, o.keepKeys...)
	}
	if o.transform != nil {
		w = o.transform(w)
	}
	var diffs []string
	diff(w, v, "want.vs.got", &diffs)
	for _, d := range diffs {
		r.t.Errorf("%s / %s: diff: %s", r.s.b.name, name, d)
	}
}

func firstConnID(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	id, ok := m["id"].(string)
	return id, ok
}

// ─── SQLite 场景 ───────────────────────────────────────────

func TestContractSQLite(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			sqliteScenario(r)
		})
	}
}

func sqliteScenario(r *recorder) {
	r.step("health", "GET", "/api/health", nil, 200,
		map[string]any{"status": "ok", "version": "0.1.0"})

	// 空库必须返回 [] 而非 null：锁定两端 wire 一致（msgpack null 会打崩 web 端）。
	r.step("empty connections", "GET", "/api/connections", nil, 200, []any{})

	createBody := map[string]any{"name": "sqlite-contract", "kind": "sqlite", "database": ":memory:"}
	status, got, err := r.s.c.Do("POST", "/api/connections", createBody)
	if err != nil || status != 201 {
		r.t.Fatalf("%s: create connection failed: status=%d err=%v", r.s.b.name, status, err)
	}
	r.s.connID, _ = firstConnID(got)
	if r.s.connID == "" {
		r.t.Fatalf("%s: no id in create response: %v", r.s.b.name, got)
	}
	id := r.s.connID

	r.step("test connection", "POST", "/api/connections/"+id+"/test", nil, 200,
		map[string]any{"connected": true})

	r.step("create table", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE users(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, age INTEGER)"},
		200, map[string]any{"statement_type": "ddl"})

	r.step("insert", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "INSERT INTO users(name, age) VALUES('alice', 30)"},
		200, map[string]any{"statement_type": "insert", "affected_rows": int64(1)})

	// 值类型矩阵：两端驱动必须产出相同的 Value 映射（NULL→nil、INTEGER→int、REAL→float、
	// TEXT→string、BLOB→"<blob N bytes>" 占位符），这是 QueryResult rows 的核心 parity。
	r.step("type matrix create", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE ct_types(id INTEGER PRIMARY KEY, s TEXT, i INTEGER, r REAL, b BLOB)"},
		200, map[string]any{"statement_type": "ddl"})

	r.step("type matrix insert", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "INSERT INTO ct_types VALUES(1, 'hello', 42, 1.5, X'0102'), (2, NULL, NULL, NULL, NULL)"},
		200, map[string]any{"statement_type": "insert"})

	r.step("type matrix select", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT s, i, r, b FROM ct_types ORDER BY id"},
		200, map[string]any{
			"rows": []any{
				[]any{"hello", int64(42), 1.5, "<blob 2 bytes>"},
				[]any{nil, nil, nil, nil},
			},
		}, keep("rows"))

	r.step("select", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT * FROM users ORDER BY id"},
		200, map[string]any{
			"columns": []any{map[string]any{"name": "id"}, map[string]any{"name": "name"}, map[string]any{"name": "age"}},
			"rows":    []any{[]any{int64(1), "alice", int64(30)}},
		}, keep("columns", "rows"), transform(onlyColumnNames))

	r.step("select with leading comment", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "-- 带注释的查询\n/* block */ SELECT * FROM users ORDER BY id"},
		200, map[string]any{
			"columns": []any{map[string]any{"name": "id"}, map[string]any{"name": "name"}, map[string]any{"name": "age"}},
			"rows":    []any{[]any{int64(1), "alice", int64(30)}},
			// 注释不改变语句类型：仍是 SELECT，必须返回结果集而非 Exec 语义。
			"statement_type": "select",
		}, keep("columns", "rows", "statement_type"), transform(onlyColumnNames))

	r.step("query missing table", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT * FROM missing_table"},
		500, map[string]any{"code": "POLYDB_ERR_QUERY_FAILED"})

	r.step("schemas", "GET", "/api/connections/"+id+"/schemas", nil, 200,
		[]any{map[string]any{"name": "main"}})

	r.step("tables", "GET", "/api/connections/"+id+"/schemas/main/tables", nil, 200,
		[]any{
			map[string]any{"name": "ct_types", "schema": "main", "type": "table"},
			map[string]any{"name": "users", "schema": "main", "type": "table"},
		})

	r.step("columns", "GET", "/api/connections/"+id+"/schemas/main/tables/users/columns", nil, 200,
		[]any{
			map[string]any{"name": "id", "data_type": "INTEGER", "nullable": true, "is_primary_key": true, "ordinal_position": int64(1)},
			map[string]any{"name": "name", "data_type": "TEXT", "nullable": false, "is_primary_key": false, "ordinal_position": int64(2)},
			map[string]any{"name": "age", "data_type": "INTEGER", "nullable": true, "is_primary_key": false, "ordinal_position": int64(3)},
		},
		keep("name", "data_type", "nullable", "is_primary_key", "ordinal_position"))

	r.step("indexes", "GET", "/api/connections/"+id+"/schemas/main/tables/users/indexes", nil, 200, []any{})
	r.step("foreign keys", "GET", "/api/connections/"+id+"/schemas/main/tables/users/foreign-keys", nil, 200, []any{})

	r.step("ddl", "GET", "/api/connections/"+id+"/schemas/main/tables/users/ddl", nil, 200,
		map[string]any{"sql": "CREATE TABLE users(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, age INTEGER)"},
		transform(collapseSQL))

	r.step("batch", "POST", "/api/connections/"+id+"/query/batch",
		map[string]any{
			"statements": []any{
				map[string]any{"sql": "INSERT INTO users(name, age) VALUES('bob', 25)"},
				map[string]any{"sql": "SELECT name FROM users ORDER BY id"},
			},
			"stop_on_error": false,
		},
		200, map[string]any{
			"results": []any{
				map[string]any{"statement_type": "insert"},
				map[string]any{"rows": []any{[]any{"alice"}, []any{"bob"}}},
			},
		}, transform(dropErrMessages))

	r.step("batch with error", "POST", "/api/connections/"+id+"/query/batch",
		map[string]any{
			"statements": []any{
				map[string]any{"sql": "SELECT * FROM missing_table"},
				map[string]any{"sql": "SELECT 1 AS one"},
			},
			"stop_on_error": false,
		},
		200, map[string]any{
			"results": []any{
				map[string]any{"code": "POLYDB_ERR_QUERY_FAILED"},
				map[string]any{"rows": []any{[]any{int64(1)}}},
			},
		}, transform(dropErrMessages))

	r.step("batch stop on error", "POST", "/api/connections/"+id+"/query/batch",
		map[string]any{
			"statements": []any{
				map[string]any{"sql": "SELECT * FROM missing_table"},
				map[string]any{"sql": "SELECT 1 AS one"},
			},
			"stop_on_error": true,
		},
		200, map[string]any{
			"results": []any{
				map[string]any{"code": "POLYDB_ERR_QUERY_FAILED"},
			},
		}, transform(dropErrMessages))

	r.step("update connection", "PUT", "/api/connections/"+id,
		map[string]any{"name": "sqlite-contract-renamed"}, 200,
		map[string]any{"name": "sqlite-contract-renamed"})

	r.step("get connection", "GET", "/api/connections/"+id, nil, 200,
		map[string]any{"name": "sqlite-contract-renamed", "kind": "sqlite"}, keep("name", "kind"))

	r.step("kv not supported", "POST", "/api/connections/"+id+"/kv/scan",
		map[string]any{"cursor": int64(0), "count": int64(10)}, 501,
		map[string]any{"code": "POLYDB_ERR_NOT_SUPPORTED"})

	r.step("delete connection", "DELETE", "/api/connections/"+id, nil, 204, nil)
	r.step("get deleted", "GET", "/api/connections/"+id, nil, 404,
		map[string]any{"code": "POLYDB_ERR_CONNECTION_NOT_FOUND"})
}

// ─── PostgreSQL 场景（Go + Rust 双后端对拍） ──

func TestContractPostgres(t *testing.T) {
	dsn := os.Getenv("POLYDB_TEST_PG")
	if dsn == "" {
		dsn = "postgres://postgres@localhost:5432/postgres?sslmode=disable"
	}
	host, port, database, username, ok := parsePGDSN(dsn)
	if !ok {
		t.Skipf("invalid POLYDB_TEST_PG DSN: %s", dsn)
	}

	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			pgScenario(r, host, port, database, username)
		})
	}
}

func pgScenario(r *recorder, host string, port int64, database, username string) {
	createBody := map[string]any{
		"name": "pg-contract", "kind": "postgres",
		"host": host, "port": port, "database": database, "username": username,
	}
	status, got, err := r.s.c.Do("POST", "/api/connections", createBody)
	if err != nil || status != 201 {
		r.t.Fatalf("%s: create pg connection failed: status=%d err=%v", r.s.b.name, status, err)
	}
	r.s.connID, _ = firstConnID(got)
	id := r.s.connID

	status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/test", nil)
	if err != nil || status != 200 {
		r.t.Errorf("%s: test pg connection request failed: status=%d err=%v", r.s.b.name, status, err)
		return
	}
	m := normalize(got).(map[string]any)
	connected, _ := m["connected"].(bool)
	if !connected {
		if msg, _ := m["error"].(string); msg != "" {
			r.t.Logf("%s: pg unavailable (%s) — 需本机 PostgreSQL 服务，跳过 PG 流程", r.s.b.name, msg)
		}
		return // 视为跳过：清理已创建的连接记录
	}

	r.step("pg create table", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "CREATE TABLE IF NOT EXISTS ct_users(id SERIAL PRIMARY KEY, name TEXT NOT NULL, age INTEGER)"},
		200, map[string]any{"statement_type": "ddl"})

	r.step("pg insert", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "INSERT INTO ct_users(name, age) VALUES('alice', 30)"},
		200, map[string]any{"statement_type": "insert"})

	r.step("pg select", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "SELECT id, name, age FROM ct_users ORDER BY id"},
		200, map[string]any{
			"columns": []any{
				map[string]any{"name": "id"}, map[string]any{"name": "name"}, map[string]any{"name": "age"},
			},
			"rows": []any{[]any{int64(1), "alice", int64(30)}},
		}, keep("columns", "rows"), transform(onlyColumnNames))

	r.step("pg cleanup", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": "DROP TABLE IF EXISTS ct_users"},
		200, map[string]any{"statement_type": "ddl"})

	r.step("pg delete connection", "DELETE", "/api/connections/"+id, nil, 204, nil)
}

func parsePGDSN(dsn string) (host string, port int64, database string, username string, ok bool) {
	if !strings.HasPrefix(dsn, "postgres://") {
		return "", 0, "", "", false
	}
	rest := strings.TrimPrefix(dsn, "postgres://")
	hostPart, dbPart := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		hostPart, dbPart = rest[:i], rest[i+1:]
		if j := strings.IndexByte(dbPart, '?'); j >= 0 {
			dbPart = dbPart[:j]
		}
	}
	if i := strings.LastIndexByte(hostPart, '@'); i >= 0 {
		username = hostPart[:i]
		if j := strings.IndexByte(username, ':'); j >= 0 {
			username = username[:j] // 不支持密码（设计上密码不落库）
		}
		hostPart = hostPart[i+1:]
	}
	host, port = hostPart, 5432
	if i := strings.LastIndexByte(hostPart, ':'); i >= 0 {
		host = hostPart[:i]
		if p, err := strconv.ParseInt(hostPart[i+1:], 10, 32); err == nil {
			port = p
		}
	}
	return host, port, dbPart, username, true
}

// ─── 网络数据库场景（MySQL / SQL Server / Oracle，Go + Rust 双后端对拍） ──

type netSpec struct {
	kind      string
	connName  string
	host      string
	port      int64
	database  string
	username  string
	password  string
	createSQL string
	insertSQL string
	selectSQL string
	dropSQL   string
}

func TestContractMySQL(t *testing.T) {
	netTest(t, "mysql", "mysql://root@localhost:3306/mysql", "localhost", 3306, "mysql", "root", "")
}
func TestContractMSSQL(t *testing.T) {
	// CI 中 MSSQL 容器强制 sa 密码，DSN 形如 mssql://sa:Passw0rd@localhost:1433/master。
	netTest(t, "mssql", "mssql://sa@localhost:1433/master", "localhost", 1433, "master", "sa", "")
}
func TestContractOracle(t *testing.T) {
	netTest(t, "oracle", "oracle://system@localhost:1521/ORCL", "localhost", 1521, "ORCL", "system", "")
}

func netTest(t *testing.T, kind, defaultDSN, defHost string, defPort int64, defDB, defUser, defPassword string) {
	env := "POLYDB_TEST_" + strings.ToUpper(kind)
	dsn := os.Getenv(env)
	if dsn == "" {
		dsn = defaultDSN
	}
	host, port, database, username, password, ok := parseNetDSN(dsn, defHost, defPort, defDB, defUser, defPassword)
	if !ok {
		t.Skipf("invalid %s DSN: %s", env, dsn)
	}
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			netScenario(r, netSpecFor(kind, host, port, database, username, password))
		})
	}
}

// netSpecFor 按库生成 SQL。表名带每轮唯一后缀：无需 IF NOT EXISTS / 容错 DROP，
// 也不会与上一次失败运行留下的残留表冲突（Oracle 无 IF EXISTS，PL/SQL 容错 DROP
// 会走 Query 分支，两端行为不统一，故避免）。
func netSpecFor(kind, host string, port int64, database, username, password string) netSpec {
	table := fmt.Sprintf("ct_users_%d", time.Now().UnixNano()%100000000)
	s := netSpec{
		kind:     kind,
		connName: kind + "-contract",
		host:     host,
		port:     port,
		database: database,
		username: username,
		password: password,
	}
	switch kind {
	case "mysql":
		s.createSQL = "CREATE TABLE " + table + "(id INT PRIMARY KEY AUTO_INCREMENT, name VARCHAR(100) NOT NULL, age INT)"
		s.insertSQL = "INSERT INTO " + table + "(name, age) VALUES('alice', 30)"
		s.selectSQL = "SELECT id, name, age FROM " + table + " ORDER BY id"
		s.dropSQL = "DROP TABLE " + table
	case "mssql":
		qt := "dbo." + table
		s.createSQL = "CREATE TABLE " + qt + "(id INT IDENTITY(1,1) PRIMARY KEY, name NVARCHAR(100) NOT NULL, age INT)"
		s.insertSQL = "INSERT INTO " + qt + "(name, age) VALUES('alice', 30)"
		s.selectSQL = "SELECT id, name, age FROM " + qt + " ORDER BY id"
		s.dropSQL = "DROP TABLE " + qt
	case "oracle":
		// Oracle 未加引号的列名大写：SELECT 用小写别名保证两端列名一致可比。
		s.createSQL = "CREATE TABLE " + table + "(id NUMBER PRIMARY KEY, name VARCHAR2(100) NOT NULL, age NUMBER)"
		s.insertSQL = "INSERT INTO " + table + "(id, name, age) VALUES(1, 'alice', 30)"
		s.selectSQL = "SELECT id AS \"id\", name AS \"name\", age AS \"age\" FROM " + table + " ORDER BY id"
		s.dropSQL = "DROP TABLE " + table
	}
	return s
}

func netScenario(r *recorder, s netSpec) {
	createBody := map[string]any{
		"name": s.connName, "kind": s.kind,
		"host": s.host, "port": s.port, "database": s.database, "username": s.username,
	}
	if s.password != "" {
		createBody["password"] = s.password
	}
	status, got, err := r.s.c.Do("POST", "/api/connections", createBody)
	if err != nil {
		r.t.Fatalf("%s: create %s connection request failed: %v", r.s.b.name, s.kind, err)
	}
	if status == 500 {
		// Rust 侧 mssql/oracle 在 create 时即建连：服务不可用会在此处失败，视为跳过。
		r.t.Logf("%s: %s unavailable at create (%v) — 需本机 %s 服务，跳过 %s 流程", r.s.b.name, s.kind, got, s.kind, s.kind)
		return
	}
	if status != 201 {
		r.t.Fatalf("%s: create %s connection failed: status=%d body=%v", r.s.b.name, s.kind, status, got)
	}
	r.s.connID, _ = firstConnID(got)
	id := r.s.connID

	status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/test", nil)
	if err != nil || status != 200 {
		r.t.Errorf("%s: test %s connection request failed: status=%d err=%v", r.s.b.name, s.kind, status, err)
		return
	}
	m := normalize(got).(map[string]any)
	connected, _ := m["connected"].(bool)
	if !connected {
		if msg, _ := m["error"].(string); msg != "" {
			r.t.Logf("%s: %s unavailable (%s) — 跳过 %s 流程", r.s.b.name, s.kind, msg, s.kind)
		}
		return // 视为跳过：清理已创建的连接记录
	}

	r.step("create table", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": s.createSQL}, 200, map[string]any{"statement_type": "ddl"})

	r.step("insert", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": s.insertSQL}, 200, map[string]any{"statement_type": "insert"})

	r.step("select", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": s.selectSQL}, 200, map[string]any{
			"columns": []any{
				map[string]any{"name": "id"}, map[string]any{"name": "name"}, map[string]any{"name": "age"},
			},
			"rows": []any{[]any{int64(1), "alice", int64(30)}},
		}, keep("columns", "rows"), transform(onlyColumnNames))

	r.step("cleanup", "POST", "/api/connections/"+id+"/query",
		map[string]any{"sql": s.dropSQL}, 200, map[string]any{"statement_type": "ddl"})

	r.step("delete connection", "DELETE", "/api/connections/"+id, nil, 204, nil)
}

// parseNetDSN 解析 scheme://user[:password]@host:port/database 形式的 DSN，
// 密码部分按 URL 百分号转义解码，缺省字段用默认值兜底。
func parseNetDSN(dsn, defHost string, defPort int64, defDB, defUser, defPassword string) (host string, port int64, database string, username string, password string, ok bool) {
	scheme, rest, found := strings.Cut(dsn, "://")
	if !found || (scheme != "mysql" && scheme != "mssql" && scheme != "oracle") {
		return "", 0, "", "", "", false
	}
	hostPart, dbPart := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		hostPart, dbPart = rest[:i], rest[i+1:]
		if j := strings.IndexByte(dbPart, '?'); j >= 0 {
			dbPart = dbPart[:j]
		}
	}
	username, password = defUser, defPassword
	if i := strings.LastIndexByte(hostPart, '@'); i >= 0 {
		userInfo := hostPart[:i]
		hostPart = hostPart[i+1:]
		if j := strings.IndexByte(userInfo, ':'); j >= 0 {
			username = userInfo[:j]
			if p, err := url.QueryUnescape(userInfo[j+1:]); err == nil {
				password = p
			} else {
				password = userInfo[j+1:]
			}
		} else {
			username = userInfo
		}
	}
	host, port = defHost, defPort
	if i := strings.LastIndexByte(hostPart, ':'); i >= 0 {
		host = hostPart[:i]
		if p, err := strconv.ParseInt(hostPart[i+1:], 10, 32); err == nil {
			port = p
		}
	}
	if dbPart != "" {
		database = dbPart
	} else {
		database = defDB
	}
	if host == "" {
		host = defHost
	}
	return host, port, database, username, password, true
}

// ─── Redis 场景（KvDriver，Go + Rust 双后端对拍） ──

func TestContractRedis(t *testing.T) {
	addr := os.Getenv("POLYDB_TEST_REDIS")
	if addr == "" {
		addr = "localhost:6379"
	}
	host, port, ok := parseRedisAddr(addr)
	if !ok {
		t.Skipf("invalid POLYDB_TEST_REDIS addr: %s", addr)
	}
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			redisScenario(r, host, port)
		})
	}
}

func redisScenario(r *recorder, host string, port int64) {
	// 空库必须返回 [] 而非 null：锁定两端 wire 一致（msgpack null 会打崩 web 端）。
	r.step("empty connections", "GET", "/api/connections", nil, 200, []any{})

	createBody := map[string]any{
		"name": "redis-contract", "kind": "redis",
		"host": host, "port": port, "database": "0",
	}
	status, got, err := r.s.c.Do("POST", "/api/connections", createBody)
	if err != nil {
		r.t.Fatalf("%s: create redis connection request failed: %v", r.s.b.name, err)
	}
	if status == 500 {
		// 两端都在 create 时即建连：Redis 服务不可用在此处失败，视为跳过。
		r.t.Logf("%s: redis unavailable at create (%v) — 需本机 Redis 服务，跳过 redis 流程", r.s.b.name, got)
		return
	}
	if status != 201 {
		r.t.Fatalf("%s: create redis connection failed: status=%d body=%v", r.s.b.name, status, got)
	}
	r.s.connID, _ = firstConnID(got)
	id := r.s.connID

	status, got, err = r.s.c.Do("POST", "/api/connections/"+id+"/test", nil)
	if err != nil || status != 200 {
		r.t.Errorf("%s: test redis connection request failed: status=%d err=%v", r.s.b.name, status, err)
		return
	}
	m := normalize(got).(map[string]any)
	connected, _ := m["connected"].(bool)
	if !connected {
		if msg, _ := m["error"].(string); msg != "" {
			r.t.Logf("%s: redis unavailable (%s) — 跳过 redis 流程", r.s.b.name, msg)
		}
		return // 视为跳过：清理已创建的连接记录
	}

	// 键名带每轮唯一后缀：不与上一次失败运行残留冲突，也不与另一后端实例冲突。
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%100000000)
	ks := map[string]string{
		"string": "ct:redis:str:" + suffix,
		"list":   "ct:redis:list:" + suffix,
		"set":    "ct:redis:set:" + suffix,
		"zset":   "ct:redis:zset:" + suffix,
		"hash":   "ct:redis:hash:" + suffix,
		"stream": "ct:redis:stream:" + suffix,
	}

	r.step("exec PING", "POST", "/api/connections/"+id+"/kv/exec",
		map[string]any{"args": []any{"PING"}}, 200,
		map[string]any{"type": "bulk_string", "value": "PONG"})

	r.step("exec SET", "POST", "/api/connections/"+id+"/kv/exec",
		map[string]any{"args": []any{"SET", ks["string"], "hello"}}, 200,
		map[string]any{"type": "bulk_string", "value": "OK"})

	r.step("get string", "GET", "/api/connections/"+id+"/kv/keys/"+ks["string"], nil, 200,
		map[string]any{"type": "string", "value": "hello"})

	r.step("get missing key", "GET", "/api/connections/"+id+"/kv/keys/ct:redis:missing:"+suffix, nil, 500,
		map[string]any{"code": "POLYDB_ERR_QUERY_FAILED"})

	r.step("set list", "PUT", "/api/connections/"+id+"/kv/keys/"+ks["list"],
		map[string]any{"key": ks["list"], "value": map[string]any{"type": "list", "value": []any{"a", "b", "c"}}}, 204, nil)
	r.step("get list", "GET", "/api/connections/"+id+"/kv/keys/"+ks["list"], nil, 200,
		map[string]any{"type": "list", "value": []any{"a", "b", "c"}})

	r.step("set set", "PUT", "/api/connections/"+id+"/kv/keys/"+ks["set"],
		map[string]any{"key": ks["set"], "value": map[string]any{"type": "set", "value": []any{"only"}}}, 204, nil)
	r.step("get set", "GET", "/api/connections/"+id+"/kv/keys/"+ks["set"], nil, 200,
		map[string]any{"type": "set", "value": []any{"only"}})

	r.step("set zset", "PUT", "/api/connections/"+id+"/kv/keys/"+ks["zset"],
		map[string]any{"key": ks["zset"], "value": map[string]any{"type": "zset", "value": []any{
			map[string]any{"member": "m1", "score": 1.5},
		}}}, 204, nil)
	r.step("get zset", "GET", "/api/connections/"+id+"/kv/keys/"+ks["zset"], nil, 200,
		map[string]any{"type": "zset", "value": []any{
			map[string]any{"member": "m1", "score": 1.5},
		}})

	r.step("set hash", "PUT", "/api/connections/"+id+"/kv/keys/"+ks["hash"],
		map[string]any{"key": ks["hash"], "value": map[string]any{"type": "hash", "value": map[string]any{"f1": "v1", "f2": "v2"}}}, 204, nil)
	r.step("get hash", "GET", "/api/connections/"+id+"/kv/keys/"+ks["hash"], nil, 200,
		map[string]any{"type": "hash", "value": map[string]any{"f1": "v1", "f2": "v2"}})

	r.step("set stream", "PUT", "/api/connections/"+id+"/kv/keys/"+ks["stream"],
		map[string]any{"key": ks["stream"], "value": map[string]any{"type": "stream", "value": "payload-1"}}, 204, nil)
	// 流 ID 由 Redis 生成且两端各自 XADD：只比对序列化的字段部分。
	r.step("get stream", "GET", "/api/connections/"+id+"/kv/keys/"+ks["stream"], nil, 200,
		map[string]any{"type": "stream", "value": "{data=payload-1}"}, transform(onlyStreamData))

	r.step("select db", "POST", "/api/connections/"+id+"/kv/select",
		map[string]any{"index": int64(0)}, 204, nil)

	// scan：SCAN 不保证单轮返回全部匹配，循环收集到 cursor=0 再断言键集合与类型。
	scanned := scanAllKeys(r, id, "ct:redis:*"+suffix)
	wantTypes := map[string]string{
		ks["string"]: "string", ks["list"]: "list", ks["set"]: "set",
		ks["zset"]: "zset", ks["hash"]: "hash", ks["stream"]: "stream",
	}
	for key, typ := range wantTypes {
		info, ok := scanned[key]
		if !ok {
			r.t.Errorf("%s / scan: key %s missing (scanned=%v)", r.s.b.name, key, scanned)
			continue
		}
		if info["type"] != typ {
			r.t.Errorf("%s / scan: key %s type = %v, want %s", r.s.b.name, key, info["type"], typ)
		}
		if ttl, ok := info["ttl"].(int64); !ok || ttl != -1 {
			r.t.Errorf("%s / scan: key %s ttl = %v, want -1", r.s.b.name, key, info["ttl"])
		}
	}

	r.step("exec EXPIRE", "POST", "/api/connections/"+id+"/kv/exec",
		map[string]any{"args": []any{"EXPIRE", ks["string"], "60"}}, 200,
		map[string]any{"type": "integer", "value": int64(1)})

	delArgs := []any{"DEL"}
	for _, key := range []string{ks["string"], ks["list"], ks["set"], ks["zset"], ks["hash"], ks["stream"]} {
		delArgs = append(delArgs, key)
	}
	r.step("exec DEL cleanup", "POST", "/api/connections/"+id+"/kv/exec",
		map[string]any{"args": delArgs}, 200,
		map[string]any{"type": "integer", "value": int64(6)})

	r.step("get deleted key", "GET", "/api/connections/"+id+"/kv/keys/"+ks["string"], nil, 500,
		map[string]any{"code": "POLYDB_ERR_QUERY_FAILED"})

	r.step("delete connection", "DELETE", "/api/connections/"+id, nil, 204, nil)
}

// scanAllKeys 循环 SCAN 直到 cursor=0，返回 key → 条目（type/ttl）映射。
func scanAllKeys(r *recorder, id, pattern string) map[string]map[string]any {
	r.t.Helper()
	cursor := int64(0)
	keys := map[string]map[string]any{}
	for i := 0; i < 100; i++ {
		status, got, err := r.s.c.Do("POST", "/api/connections/"+id+"/kv/scan",
			map[string]any{"cursor": cursor, "pattern": pattern, "count": int64(1000)})
		if err != nil {
			r.t.Errorf("%s / scan: request failed: %v", r.s.b.name, err)
			return keys
		}
		if status != 200 {
			r.t.Errorf("%s / scan: status = %d (body=%v)", r.s.b.name, status, got)
			return keys
		}
		m := normalize(got).(map[string]any)
		cursor, _ = m["cursor"].(int64)
		if ks, ok := m["keys"].([]any); ok {
			for _, e := range ks {
				if em, ok := e.(map[string]any); ok {
					if name, ok := em["key"].(string); ok {
						keys[name] = em
					}
				}
			}
		}
		if cursor == 0 {
			break
		}
	}
	return keys
}

// onlyStreamData 剥离流序列化开头的动态 ID（"<id> {data=...}" → "{data=...}"）。
func onlyStreamData(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if s, ok := m["value"].(string); ok {
		if i := strings.IndexByte(s, ' '); i >= 0 {
			m["value"] = s[i+1:]
		}
	}
	return m
}

// parseRedisAddr 解析 host:port（缺省端口 6379）。
func parseRedisAddr(addr string) (host string, port int64, ok bool) {
	host, port = addr, 6379
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		host = addr[:i]
		if p, err := strconv.ParseInt(addr[i+1:], 10, 32); err == nil {
			port = p
		}
	}
	if host == "" {
		return "", 0, false
	}
	return host, port, true
}
