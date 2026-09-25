package contract

import (
	"fmt"
	"path/filepath"
	"testing"
)

// E3 语句生成契约：POST /api/schema/ddl 在 Rust / Go 双后端行为一致，
// 且**产出的脚本必须能真的执行**——应用后比对收敛为空，回滚脚本能把库还原。
//
// 语句文本本身由 test/schema-ddl-vectors/*.json 双端锁死，这里不重复逐字比较，
// 只锁端点接线：勾选校验、PRAGMA 包裹位、空数组形状、执行收敛与回滚可逆。

// ddlReq 组装语句生成请求：连接 + 勾选条目 id（options 省略即默认）。
func ddlReq(source, target string, selected []string) map[string]any {
	sel := selected
	if sel == nil {
		sel = []string{}
	}
	return map[string]any{"source": diffSide(source), "target": diffSide(target), "selected": sel}
}

// planIDs 取一次比对计划的全部条目 id（顺序即相位序），供勾选使用。
func (r *recorder) planIDs(name, source, target string) []string {
	r.t.Helper()
	status, got, err := r.s.c.Do("POST", "/api/schema/diff", diffReq(source, target, nil))
	if err != nil || status != 200 {
		r.t.Fatalf("%s / %s: diff failed: status=%d err=%v body=%v", r.s.b.name, name, status, err, got)
	}
	ids := []string{}
	for _, e := range entryList(r.t, got) {
		ids = append(ids, e)
	}
	return ids
}

// entryList 从计划响应里取出条目 id 列表（不走 normalize：id 是要锁的契约内容）。
func entryList(t *testing.T, got any) []string {
	t.Helper()
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("计划响应不是 map：%T", got)
	}
	raw, _ := m["entries"].([]any)
	ids := make([]string, 0, len(raw))
	for _, e := range raw {
		em, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("计划条目不是 map：%T", e)
		}
		id, ok := em["id"].(string)
		if !ok {
			t.Fatalf("计划条目缺 id：%#v", em)
		}
		ids = append(ids, id)
	}
	return ids
}

// ddlStep 调语句生成端点，按 want 子集比较（want 为 nil 时只校验状态码），返回原始响应。
func (r *recorder) ddlStep(name string, body any, wantStatus int, want any) map[string]any {
	r.t.Helper()
	status, got, err := r.s.c.Do("POST", "/api/schema/ddl", body)
	if err != nil {
		r.t.Fatalf("%s / %s: request failed: %v", r.s.b.name, name, err)
	}
	if status != wantStatus {
		r.t.Fatalf("%s / %s: status = %d, want %d (body=%v)", r.s.b.name, name, status, wantStatus, got)
	}
	if want == nil {
		return nil
	}
	m, ok := got.(map[string]any)
	if !ok {
		r.t.Fatalf("%s / %s: 响应不是 map：%T", r.s.b.name, name, got)
	}
	var diffs []string
	diff(normalize(want), normalize(m), "want.vs.got", &diffs)
	for _, d := range diffs {
		r.t.Errorf("%s / %s: %s", r.s.b.name, name, d)
	}
	return m
}

// scriptField 取脚本的某一侧（statements / rollback），返回按相位排序的语句 map 列表。
func scriptField(t *testing.T, body map[string]any, field string) []map[string]any {
	t.Helper()
	raw, ok := body[field].([]any)
	if !ok {
		t.Fatalf("%s 缺失或不是数组：%#v", field, body[field])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, s := range raw {
		sm, ok := s.(map[string]any)
		if !ok {
			t.Fatalf("%s 元素不是 map：%T", field, s)
		}
		out = append(out, sm)
	}
	return out
}

// scriptSQL 取脚本某一侧的 sql 列表。
func scriptSQL(t *testing.T, body map[string]any, field string) []string {
	t.Helper()
	raw := scriptField(t, body, field)
	sqls := make([]string, 0, len(raw))
	for i, s := range raw {
		q, ok := s["sql"].(string)
		if !ok {
			t.Fatalf("%s[%d] 缺 sql：%#v", field, i, s)
		}
		sqls = append(sqls, q)
	}
	return sqls
}

// applyScript 按脚本顺序逐条执行（每条各自提交；PRAGMA 包裹条因此天然在事务外）。
func (r *recorder) applyScript(name, connID string, sqls []string) {
	r.t.Helper()
	for i, q := range sqls {
		r.step(fmt.Sprintf("%s [%d]", name, i), "POST", "/api/connections/"+connID+"/query",
			map[string]any{"sql": q}, 200, nil)
	}
}

// absorbedIDs 汇总脚本里所有语句的 entry_ids，用于校验「勾选的都被承载、未勾选的不掺进来」。
func absorbedIDs(t *testing.T, body map[string]any) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	for _, s := range scriptField(t, body, "statements") {
		ids, ok := s["entry_ids"].([]any)
		if !ok {
			t.Fatalf("语句缺 entry_ids：%#v", s)
		}
		for _, id := range ids {
			str, ok := id.(string)
			if !ok {
				t.Fatalf("entry_ids 元素不是字符串：%#v", id)
			}
			seen[str] = true
		}
	}
	return seen
}

func TestContractSchemaDDLSQLiteApplyAndRollback(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			dir := t.TempDir()
			src := createFileConn(r, "e3a-src", filepath.Join(dir, "src.db"))
			tgt := createFileConn(r, "e3a-tgt", filepath.Join(dir, "tgt.db"))
			r.execDDL("src: t", src, "CREATE TABLE t(id INTEGER PRIMARY KEY, name VARCHAR(20))")
			r.execDDL("tgt: t", tgt, "CREATE TABLE t(id INTEGER PRIMARY KEY, name VARCHAR(20), note TEXT)")
			r.execDDL("tgt: u", tgt, "CREATE TABLE u(id INTEGER)")

			ids := r.planIDs("初次比对", src, tgt)
			// 计划两条：新建表 + 加列（都是非破坏性，勾选全部）。
			if len(ids) != 2 {
				t.Fatalf("计划应为两条，got %v", ids)
			}

			// ─── 空勾选 → 空数组（不是 null）────────────────────
			r.ddlStep("空勾选", ddlReq(src, tgt, []string{}), 200, map[string]any{
				"statements":         []any{},
				"rollback":           []any{},
				"pragmas_outside_tx": false,
				"ddl_in_tx":          true,
			})

			// ─── 直改脚本：无重建 ⇒ 不含 PRAGMA 包裹 ────────────
			body := r.ddlStep("直改脚本", ddlReq(src, tgt, ids), 200, map[string]any{
				"pragmas_outside_tx": false,
				"ddl_in_tx":          true,
			})
			absorbed := absorbedIDs(t, body)
			for _, id := range ids {
				if !absorbed[id] {
					t.Errorf("勾选的 %s 未被任何语句承载：%v", id, absorbed)
				}
			}
			// 正向按相位序：create_table(50) 在 add_column(60) 之前。
			stmts := scriptField(t, body, "statements")
			if len(stmts) != 2 {
				t.Fatalf("直改应恰好两条语句：%#v", stmts)
			}
			if stmts[0]["table"] != "u" || stmts[1]["table"] != "t" {
				t.Errorf("语句顺序不符：%#v / %#v", stmts[0], stmts[1])
			}
			// 回滚是镜像：两条都可逆性明确（删表/删列不可逆）。
			rb := scriptField(t, body, "rollback")
			if len(rb) != 2 {
				t.Fatalf("回滚应恰好两条语句：%#v", rb)
			}
			for _, s := range rb {
				if v, ok := s["non_reversible"].(bool); !ok || !v {
					t.Errorf("回滚里的 drop 类语句必须标为不可逆：%#v", s)
				}
			}

			// ─── 真执行：应用正向脚本后必须收敛 ──────────────────
			r.applyScript("应用正向", src, scriptSQL(t, body, "statements"))
			r.diffStep("应用后收敛", diffReq(src, tgt, nil), 200, map[string]any{"entries": []any{}})

			// ─── 执行回滚脚本后应回到原计划 ──────────────────────
			r.applyScript("应用回滚", src, scriptSQL(t, body, "rollback"))
			r.diffStep("回滚后复原", diffReq(src, tgt, nil), 200, map[string]any{
				"entries": []any{
					map[string]any{"diff_id": "u//create_table"},
					map[string]any{"diff_id": "t/note/add_column"},
				},
			})

			r.step("delete src", "DELETE", "/api/connections/"+src, nil, 204, nil)
			r.step("delete tgt", "DELETE", "/api/connections/"+tgt, nil, 204, nil)
		})
	}
}

func TestContractSchemaDDLSQLiteRebuildKeepsChildRows(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
			dir := t.TempDir()
			src := createFileConn(r, "e3b-src", filepath.Join(dir, "src.db"))
			tgt := createFileConn(r, "e3b-tgt", filepath.Join(dir, "tgt.db"))
			// child 两侧定义一致 ⇒ 不进计划；t 上既有改类型（必须重建）又有加列。
			r.execDDL("src: t", src, "CREATE TABLE t(id INTEGER PRIMARY KEY, qty TEXT)")
			r.execDDL("src: child", src, "CREATE TABLE child(pid INTEGER REFERENCES t(id))")
			// 父子表各放一行：重建期间外键必须处于关闭状态，数据一行都不能丢。
			r.step("src: t 数据", "POST", "/api/connections/"+src+"/query",
				map[string]any{"sql": "INSERT INTO t(id, qty) VALUES(1, '7')"}, 200,
				map[string]any{"statement_type": "insert"})
			// 子表行是本次要守住的 data：重建期间外键必须处于关闭状态。
			r.step("src: child 数据", "POST", "/api/connections/"+src+"/query",
				map[string]any{"sql": "INSERT INTO child(pid) VALUES(1)"}, 200,
				map[string]any{"statement_type": "insert"})
			r.execDDL("tgt: t", tgt, "CREATE TABLE t(id INTEGER PRIMARY KEY, qty INTEGER, note TEXT)")
			r.execDDL("tgt: child", tgt, "CREATE TABLE child(pid INTEGER REFERENCES t(id))")

			ids := r.planIDs("初次比对", src, tgt)
			if len(ids) != 2 {
				t.Fatalf("计划应为改类型 + 加列两条，got %v", ids)
			}

			// ─── 陈旧勾选 → 400 ────────────────────────────────
			r.ddlStep("陈旧 id", ddlReq(src, tgt, []string{"t/ghost/drop_index"}), 400, map[string]any{
				"code":    "POLYDB_ERR_INVALID_PARAM",
				"message": "勾选的条目不在当前比对计划里：t/ghost/drop_index（请重新比对后再执行）",
			})

			// ─── 重建组未整组勾选 → 400，并把缺的 id 逐字列出 ────
			r.ddlStep("半组勾选", ddlReq(src, tgt, []string{"t/qty/alter_column"}), 400, map[string]any{
				"code":    "POLYDB_ERR_INVALID_PARAM",
				"message": "表 t 需要整表重建，同表变更必须整组勾选：缺少 t/note/add_column",
			})

			// ─── 整组勾选 → 重建脚本含首尾 PRAGMA 包裹 ───────────
			body := r.ddlStep("重建脚本", ddlReq(src, tgt, ids), 200, map[string]any{
				"pragmas_outside_tx": true,
				"ddl_in_tx":          true,
			})
			stmts := scriptField(t, body, "statements")
			head, tail := stmts[0], stmts[len(stmts)-1]
			if head["sql"] != "PRAGMA foreign_keys=OFF;" || len(head["entry_ids"].([]any)) != 0 {
				t.Errorf("首条必须是空 entry_ids 的 PRAGMA off：%#v", head)
			}
			if tail["sql"] != "PRAGMA foreign_keys=ON;" {
				t.Errorf("末条必须是 PRAGMA on：%#v", tail)
			}
			// 同表条目被重建组吸收：两条勾选 id 都要出现在组里。
			absorbed := absorbedIDs(t, body)
			for _, id := range ids {
				if !absorbed[id] {
					t.Errorf("勾选的 %s 未被重建组承载：%v", id, absorbed)
				}
			}

			// ─── 真执行：收敛 + 子表行数不丢（FK 关闭生效）────────
			r.applyScript("应用重建", src, scriptSQL(t, body, "statements"))
			r.diffStep("重建后收敛", diffReq(src, tgt, nil), 200, map[string]any{"entries": []any{}})
			r.step("重建后子表行数", "POST", "/api/connections/"+src+"/query",
				map[string]any{"sql": "SELECT COUNT(*) FROM child"}, 200,
				map[string]any{"rows": []any{[]any{int64(1)}}}, keep("rows"))
			r.step("重建后主表行数", "POST", "/api/connections/"+src+"/query",
				map[string]any{"sql": "SELECT COUNT(*) FROM t"}, 200,
				map[string]any{"rows": []any{[]any{int64(1)}}}, keep("rows"))

			// ─── 回滚同样重建（规则 7），执行后回到原计划 ─────────
			r.applyScript("应用回滚", src, scriptSQL(t, body, "rollback"))
			// 回滚把两条变更都还原：仍按相位排序 ⇒ 加列(60) 在改类型(70) 之前。
			r.diffStep("回滚后复原", diffReq(src, tgt, nil), 200, map[string]any{
				"entries": []any{
					map[string]any{"diff_id": "t/note/add_column"},
					map[string]any{"diff_id": "t/qty/alter_column"},
				},
			})
			r.step("回滚后子表行数", "POST", "/api/connections/"+src+"/query",
				map[string]any{"sql": "SELECT COUNT(*) FROM child"}, 200,
				map[string]any{"rows": []any{[]any{int64(1)}}}, keep("rows"))

			r.step("delete src", "DELETE", "/api/connections/"+src, nil, 204, nil)
			r.step("delete tgt", "DELETE", "/api/connections/"+tgt, nil, 204, nil)
		})
	}
}

// ─── 方言收窄：v1 只生成 SQLite，其余显式拒绝（两端逐字同文案）──

func TestContractSchemaDDLDialectsNotSupported(t *testing.T) {
	for _, d := range netDialects {
		d := d
		t.Run(d.kind, func(t *testing.T) {
			bs := backends(t)
			for _, b := range bs {
				b := b
				t.Run(b.name, func(t *testing.T) {
					r := &recorder{t: t, s: &st{b: b, c: NewClient(b.base)}}
					id := dialectConn(r, "e3", d)
					if id == "" {
						return // 本机无该数据库服务：soft-skip
					}
					r.ddlStep(d.kind+" 生成语句", ddlReq(id, id, []string{}), 501, map[string]any{
						"code":    "POLYDB_ERR_NOT_SUPPORTED",
						"message": d.kind + " 方言的 DDL 生成在 v1 未实现（当前只生成 sqlite）",
					})
					r.step("delete connection", "DELETE", "/api/connections/"+id, nil, 204, nil)
				})
			}
		})
	}
}
