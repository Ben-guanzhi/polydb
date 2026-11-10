#!/usr/bin/env python3
"""`schema-ddl-vectors` 的 `sql` 全文推导（§142 §7 / §143 §7 的最后一块）。

## 依据

`spec/behavior.md` §15.3 / §15.6 / §15.7 **早已把规则写全**：

- 直改语句模板（`create_table` / `drop_table` / `add_column` /
  `drop_column` / `add_index` / `drop_index`）；
- 整表重建的**五步模板**（建临时表 → 搬同名列 → 删原表 → RENAME →
  目标侧用户索引），临时表名 `polydb_tmp_{表}` 撞名追加序号；
- 回滚 = 互换两侧后按同一套规则再生成，**承接正向的重建**（forced）；
- `non_reversible` 判定（§15.7 的表）；
- `pragmas_outside_tx` = 任一方向含重建。

文本细节（引号、类型归一、列/约束排列）在语料里逐字锁定，且两端实现一致
——它们的**规则**都可从代码与 spec 读出，本模块按同一套规则独立重写。

## 排除（仍不校验）

- `pragmas_outside_tx` 与重建集合的**一致性**之外 nothing——实际上它也推导了。
- 无。本模块推导 `statements` / `rollback` / `pragmas_outside_tx` / `ddl_in_tx`
  的全部内容。

> 历史注记：`warnings` 之前被排除（§142），后来发现 §15.8 已有契约（§143）；
> `sql` 被排除的理由是"渲染产物"，但 §15.3 其实把模板写全了——**同一类
> "契约已写全、判断太悲观"**。
"""

import re

import schema_rules as R


# ────────────────────────────────────────────── 文本层
def quote_ident(name):
    """标识符：`"` 包裹，内部 `"` 翻倍。"""
    return '"' + str(name).replace('"', '""') + '"'


def join_idents(names):
    return ",".join(quote_ident(n) for n in names)


def sql_action(a):
    """FK 动作 → SQL 关键字；None/空 → None（不生成子句）。"""
    s = (a or "").strip().lower()
    return {
        "cascade": "CASCADE",
        "set_null": "SET NULL",
        "setnull": "SET NULL",
        "set_default": "SET DEFAULT",
        "setdefault": "SET DEFAULT",
        "restrict": "RESTRICT",
        "no_action": "NO ACTION",
        "noaction": "NO ACTION",
    }.get(s)


def ordered_columns(table):
    cols = list(table.get("columns", []) or [])
    return sorted(cols, key=lambda c: (c.get("ordinal_position", 0), str(c.get("name", "")).lower()))


def index_cols_by_position(idx):
    cols = list(idx.get("columns", []) or [])
    return [c["name"] for c in sorted(cols, key=lambda c: (c.get("position", 0), str(c.get("name", "")).lower()))]


def index_sig(idx):
    return R.index_sig_str(idx)


def pk_columns(table):
    """优先取主键索引（带列序），否则回退列上的 `is_primary_key`（按 ordinal 序）。"""
    for idx in table.get("indexes", []) or []:
        if idx.get("primary"):
            cols = index_cols_by_position(idx)
            if cols:
                return cols
            break
    return [c["name"] for c in ordered_columns(table) if c.get("is_primary_key")]


def is_auto_index(name):
    return str(name).lower().startswith("sqlite_autoindex_")


def column_def(kind, c):
    s = quote_ident(c["name"])
    ty = R.norm_type(c.get("data_type"), kind)
    if ty:
        s += " " + ty
    if not c.get("nullable"):
        s += " NOT NULL"
    d = R.literal_default(c.get("default_value"))
    if d:
        s += " DEFAULT " + d
    return s


def table_constraints(table):
    out = []
    pk = pk_columns(table)
    if pk:
        out.append("PRIMARY KEY (" + join_idents(pk) + ")")
    uniq = []
    for idx in table.get("indexes", []) or []:
        if idx.get("primary") or not is_auto_index(idx.get("name")):
            continue
        uniq.append((str(idx.get("name", "")).lower(), "UNIQUE (" + join_idents(index_cols_by_position(idx)) + ")"))
    uniq.sort(key=lambda kv: kv[0])
    out.extend(v for _, v in uniq)
    fks = []
    for fk in table.get("foreign_keys", []) or []:
        cols = join_idents(fk.get("columns") or [])
        refs = join_idents(fk.get("referenced_columns") or [])
        s = f"FOREIGN KEY ({cols}) REFERENCES {quote_ident(fk.get('referenced_table'))}({refs})"
        a = sql_action(fk.get("on_delete"))
        if a:
            s += f" ON DELETE {a}"
        a = sql_action(fk.get("on_update"))
        if a:
            s += f" ON UPDATE {a}"
        fks.append((cols + ">" + refs, s))
    fks.sort(key=lambda kv: kv[0])
    out.extend(v for _, v in fks)
    return out


def create_table_body(kind, table):
    parts = [column_def(kind, c) for c in ordered_columns(table)]
    parts.extend(table_constraints(table))
    return ",".join(parts)


def index_exec_name(table, idx):
    name = str(idx.get("name") or "").strip()
    if name:
        return name
    return f"polydb_idx_{table['name']}_{index_sig(idx).replace(',', '_')}"


def create_index_sql(table, idx):
    uniq = "UNIQUE " if idx.get("unique") else ""
    return (
        f"CREATE {uniq}INDEX {quote_ident(index_exec_name(table, idx))} "
        f"ON {quote_ident(table['name'])}({join_idents(index_cols_by_position(idx))});"
    )


def tmp_name(base, taken):
    cand = "polydb_tmp_" + base
    n = 2
    while str(cand).lower() in taken:
        cand = f"polydb_tmp_{base}_{n}"
        n += 1
    return cand


# ────────────────────────────────────────────── 计划层
def entry_non_reversible(op, flags):
    """§15.7 的不可逆判定（单条目）。"""
    if op in ("drop_table", "drop_column"):
        return True
    if op == "alter_column":
        return "type" in (flags or []) or "nullability" in (flags or [])
    return False


def needs_rebuild(entry, kind, src_table, tgt_table):
    """§15.3 的两路判定（与两端实现同规则，见 select oracle 的同名校验）。"""
    op = entry["op"]
    if op in ("alter_column", "add_foreign_key", "drop_foreign_key"):
        return True
    if op == "drop_column":
        if src_table is None:
            return False
        col = str(entry.get("object") or "").lower()
        for c in src_table.get("columns", []) or []:
            if str(c["name"]).lower() == col and c.get("is_primary_key"):
                return True
        for idx in src_table.get("indexes", []) or []:
            for ic in idx.get("columns", []) or []:
                if str(ic.get("name", "")).lower() == col:
                    return True
        return False
    if op in ("add_index", "drop_index"):
        table = tgt_table if op == "add_index" else src_table
        obj = str(entry.get("object") or "").lower()
        if table is not None:
            for idx in table.get("indexes", []) or []:
                if str(idx.get("name", "")).lower() == obj:
                    return bool(idx.get("primary")) or is_auto_index(idx.get("name"))
        return is_auto_index(obj)
    return False


def find_index(table, obj):
    obj = str(obj or "").lower()
    for idx in table.get("indexes", []) or []:
        if str(idx.get("name", "")).lower() == obj:
            return idx
        # 无名索引在计划里是签名形态 idx(a,b)
        if not str(idx.get("name") or "").strip():
            synth = "idx(" + R.index_sig_str(idx) + ")"
            if synth.lower() == obj:
                return idx
    return None


def find_column(table, obj):
    obj = str(obj or "").lower()
    for c in table.get("columns", []) or []:
        if str(c["name"]).lower() == obj:
            return c
    return None


def fk_cycle_keys(entries, tgt_real):
    """本次计划里成环的建表（小写键，升序）。"""
    built = {str(e["table"]).lower() for e in entries if e["op"] == "create_table"}
    if len(built) < 2:
        return []
    edges = {k: set() for k in built}
    for k in built:
        t = tgt_real.get(k)
        if not t:
            continue
        for fk in t.get("foreign_keys", []) or []:
            ref = str(fk.get("referenced_table") or "").lower()
            if ref in built and ref != k:
                edges[k].add(ref)
    cyclic = set()
    for start in edges:
        stack = list(edges[start])
        seen = set()
        while stack:
            cur = stack.pop()
            if cur == start:
                cyclic.add(start)
                break
            if cur in seen:
                continue
            seen.add(cur)
            stack.extend(edges.get(cur, ()))
    return sorted(cyclic)


def direct_statement(e, kind, src_t, tgt_t, views, cyclic):
    """§15.3 的直改语句（单条）。返回 (sql, warnings, nr, phase, table)。"""
    op = e["op"]
    nr = entry_non_reversible(op, e.get("flags"))
    if op == "create_table":
        t = tgt_t[e["table"].lower()]
        sql = f"CREATE TABLE {quote_ident(t['name'])}({create_table_body(kind, t)});"
        w = [R.W_CREATE_TABLE]
        if R.has_integer_pk(kind, t):
            w.append(R.W_AUTOINCREMENT)
        if str(e["table"]).lower() in cyclic:
            w.append(R.w_fk_cycle(cyclic))
        return sql, w, nr, e["phase"], t["name"]
    if op == "drop_table":
        t = src_t[e["table"].lower()]
        w = [R.w_view_drop(views)] if views else []
        return f"DROP TABLE {quote_ident(t['name'])};", w, nr, e["phase"], t["name"]
    if op == "add_column":
        t = tgt_t[e["table"].lower()]
        c = find_column(t, e["object"])
        if c is None:
            return None, [], nr, e["phase"], t["name"]
        w = []
        if (not R.is_constant_default(c.get("default_value"))) or (
            not c.get("nullable") and R.literal_default(c.get("default_value")) == ""
        ):
            w = [R.W_ADD_COLUMN_LIMITS]
        sql = f"ALTER TABLE {quote_ident(t['name'])} ADD COLUMN {column_def(kind, c)};"
        return sql, w, nr, e["phase"], t["name"]
    if op == "drop_column":
        t = src_t[e["table"].lower()]
        sql = f"ALTER TABLE {quote_ident(t['name'])} DROP COLUMN {quote_ident(e['object'])};"
        return sql, [R.W_DROP_COLUMN_CHECK], nr, e["phase"], t["name"]
    if op == "add_index":
        t = tgt_t[e["table"].lower()]
        idx = find_index(t, e["object"])
        return create_index_sql(t, idx), [], nr, e["phase"], t["name"]
    if op == "drop_index":
        # 名字以快照为准（无名索引取可执行名）。
        t = src_t[e["table"].lower()]
        idx = find_index(t, e["object"])
        name = index_exec_name(t, idx) if idx else e["object"]
        return f"DROP INDEX {quote_ident(name)};", [], nr, e["phase"], t["name"]
    raise ValueError(f"直改动作 {op} 必须走重建路径")


def rebuild_statements(e, kind, tgt_t, src_t, taken, ids, phase, nr, views):
    """§15.3 的五步重建模板。`views` 是被执行侧的视图名单（§15.8）。"""
    t = tgt_t[e["table"].lower()]
    s = src_t[e["table"].lower()]
    tmp = tmp_name(t["name"], taken)
    sc = R.columns_of(s)
    common = [c["name"] for c in ordered_columns(t) if str(c["name"]).lower() in sc]
    lst = join_idents(common)
    w = [R.W_TRIGGERS, R.W_GENERATED, R.W_CHECK]
    if R.has_integer_pk(kind, t):
        w.append(R.W_AUTOINCREMENT)
    for c in ordered_columns(t):
        known = str(c["name"]).lower() in sc
        if not c.get("nullable") and R.literal_default(c.get("default_value")) == "" and not known:
            w.append(R.W_REBUILD_NEW_NOT_NULL)
            break
    if views:
        w.append(R.w_view_rebuild(views))
    out = [
        (ids, f"CREATE TABLE {quote_ident(tmp)}({create_table_body(kind, t)});", phase, t["name"], w, nr),
        (ids, f"INSERT INTO {quote_ident(tmp)}({lst}) SELECT {lst} FROM {quote_ident(s['name'])};", phase, t["name"], [], nr),
        (ids, f"DROP TABLE {quote_ident(s['name'])};", phase, t["name"], [], nr),
        (ids, f"ALTER TABLE {quote_ident(tmp)} RENAME TO {quote_ident(t['name'])};", phase, t["name"], [], nr),
    ]
    user = []
    for idx in t.get("indexes", []) or []:
        if idx.get("primary") or is_auto_index(idx.get("name")):
            continue
        user.append((str(index_exec_name(t, idx)).lower(), create_index_sql(t, idx)))
    user.sort(key=lambda kv: kv[0])
    for _, sql in user:
        out.append((ids, sql, phase, t["name"], [], nr))
    return out


def taken_names(source, target):
    """临时表必须避让的名字：两侧表名 + 两侧视图名（小写键）。"""
    taken = set()
    for snap in (source, target):
        for t in snap.get("tables", []) or []:
            taken.add(str(t["name"]).lower())
    return taken


def view_names(snapshot):
    return [t["name"] for t in snapshot.get("tables", []) or []
            if t.get("type") in R.SKIPPED_TABLE_TYPES]


def real_tables_of(snapshot):
    return R.real_tables(snapshot)


def emit_script(v, direction, forced, picked_forward=None):
    """§15.3–§15.5 的脚本生成。返回 (statements, rebuilt_keys)。

    `statements` 每项: (entry_ids, sql, phase, table, warnings, non_reversible)。
    """
    src_snap, tgt_snap = v["source"], v["target"]
    if direction == "statements":
        exec_src, exec_tgt = src_snap, tgt_snap
    else:
        exec_src, exec_tgt = tgt_snap, src_snap
    kind = exec_tgt.get("kind", "")
    src_t = R.real_tables(exec_src)
    tgt_t = R.real_tables(exec_tgt)
    views = view_names(exec_src)
    taken = taken_names(src_snap, tgt_snap)

    entries = _selected_entries(v, direction, picked_forward)
    rebuild = {}
    for e in entries:
        k = str(e["table"]).lower()
        if forced.get(k) or needs_rebuild(e, kind, src_t.get(k), tgt_t.get(k)):
            rebuild[k] = True

    groups = {}
    order_keys = []
    for e in entries:
        k = str(e["table"]).lower()
        if not rebuild.get(k):
            continue
        if k not in groups:
            groups[k] = {"ids": [], "phase": e["phase"], "nr": False}
            order_keys.append(k)
        groups[k]["ids"].append(e["id"])
        groups[k]["nr"] = groups[k]["nr"] or entry_non_reversible(e["op"], e.get("flags"))

    cyclic = fk_cycle_keys(entries, tgt_t)
    out = []
    done = set()
    for e in entries:
        k = str(e["table"]).lower()
        if k in groups:
            if k in done:
                continue
            done.add(k)
            g = groups[k]
            out.extend(rebuild_statements(e, kind, tgt_t, src_t, taken, g["ids"], g["phase"], g["nr"], views))
            continue
        sql, w, nr, phase, table = direct_statement(e, kind, src_t, tgt_t, views, cyclic)
        out.append(([e["id"]], sql, phase, table, w, nr))

    if rebuild:
        out = (
            [([], "PRAGMA foreign_keys=OFF;", 0, "", [], False)]
            + out
            + [([], "PRAGMA foreign_keys=ON;", 255, "", [], False)]
        )
    return out, rebuild


def _selected_entries(v, direction, picked_forward=None):
    """该方向的**已勾选**计划条目（顺序 = 计划序）。

    正向：diff 计划里挑出勾选 id（勾选规则由 select oracle 的规则复现——
    stale/整组校验已在 `derive_script` 入口处做过）。
    回滚：Diff(target, source) 里与正向条目**镜像同名**的条目
    （`d03` note：id 只替换末段 op）。
    """
    import diff_vectors_oracle as DIFF

    if direction == "statements":
        _err, _msg, plan = DIFF.derive(v)
        if picked_forward is None:
            # 未传勾选 ⇒ 按 select 规则求（null ⇒ 全部；错误已在入口处理）
            return plan
        want = [e["id"] for e in picked_forward]
        by_id = {e["id"]: e for e in plan}
        return [by_id[i] for i in want if i in by_id]
    # 回滚：互换两侧重新推导，再按镜像 id 过滤
    flipped = {
        "name": v["name"] + " (reversed)",
        "note": v.get("note", ""),
        "options": v.get("options") or {},
        "source": v["target"],
        "target": v["source"],
        "selected": None,
    }
    _err, _msg, rev = DIFF.derive(flipped)
    want = {_mirror_id(e["id"]) for e in (picked_forward or [])}
    return [e for e in rev if e["id"] in want]


def _mirror_id(eid):
    table, obj, op = eid.rsplit("/", 2)[0], None, None
    parts = eid.split("/")
    op = parts[-1]
    inv = R.INVERSE_OP.get(op, op)
    return f"{parts[0]}/{parts[1]}/{inv}"


def derive_script(v):
    """推导整份脚本。返回 dict（statements/rollback/pragmas_outside_tx/ddl_in_tx）。"""
    import diff_vectors_oracle as DIFF
    import select_vectors_oracle as SEL

    # 勾选规则（stale / 整组）复用 select oracle；报错时返回空脚本由上层比对
    status, picked_ids = SEL.derive(v)
    _err, _msg, plan = DIFF.derive(v)
    by_id = {e["id"]: e for e in plan}
    if status != "ok":
        picked_ids = []
    picked = [by_id[i] for i in picked_ids if i in by_id]

    v2 = dict(v)
    v2["_selected_plan"] = plan

    fwd, rebuilt = emit_script(v2, "statements", {}, picked)
    rev, rebuilt_rev = emit_script(v2, "rollback", rebuilt, picked)

    def stmt_dict(ids, sql, phase, table, w, nr):
        # 语料由 Go/Rust 的 omitempty 序列化：空 warnings **省键**。
        d = {"entry_ids": ids, "sql": sql, "phase": phase, "table": table}
        if w:
            d["warnings"] = w
        d["non_reversible"] = nr
        return d

    return {
        "statements": [stmt_dict(*x) for x in fwd],
        "rollback": [stmt_dict(*x) for x in rev],
        "pragmas_outside_tx": bool(rebuilt) or bool(rebuilt_rev),
        "ddl_in_tx": True,
    }