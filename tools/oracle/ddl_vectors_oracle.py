#!/usr/bin/env python3
"""`schema-ddl-vectors` 的**结构 oracle**（Item 9 / Item 10，2026-11-10）。

## 它校验什么

DDL 语料的 `expected` 含三类东西，可推导性差别很大：

| 内容 | 可推导？ | 本 oracle |
|---|---|---|
| `error_code`（方言闸门、stale、整组校验） | ✅ 纯规则 | 校验 |
| **渲染出的条目集合**（正/反两向） | ✅ 纯规则 | 校验 |
| 语句**条数/分组/顺序/相位** | ⚠️ 编码 SQLite 方言策略 | **不校验**（见下） |
| **`warnings` 文案** | ✅ 纯规则（§15.8 规定了文案、挂载位置与名单渲染）| **校验** |
| **`sql` 全文** | ✅ 纯规则（§15.3 五步模板 + §15.6 镜像回滚 + §15.7 不可逆）| **校验** |

### 为什么"条目集合"是可推导的、而"语句分组"不是

"哪些选中条目被渲染成语句"是**完整性**问题——每个勾选的变更都必须落到
脚本里，不能悄悄丢；这不需要任何方言知识。

而"一条变更走直改还是整表重建、重建拆成几条语句、整组什么时候吸收同表
其它条目"（见 `d14` 的 note：重建组输出在该表**第一个**条目的位置）**正是**
SQLite 方言策略本身。把它推导出来等于写第三个 DDL 生成器——那会引入新的
共享误解面，正是 §133 警告的"半吊子 oracle"。故**明确排除**，不假装覆盖。

### 正/反两向的性质

- **P1**（正向）：`union(statements[].entry_ids)` == 选中的计划条目 id 集合；
- **P2**（回滚）：`union(rollback[].entry_ids)` == P1 每个 id 的**逆 op**
  （`d03` 的 note："回滚按互换后的同一套规则"，故逆映射是固定表）。

两条都与语句如何切分无关，因此不依赖方言策略。

### 已知盲区（写清楚，不装作没有）

因为是**集合**比较，以下情形**查不出**：

- 同一 id 只从**部分**语句里漏掉（重建组会把同一组 id 挂在多条语句上，
  其它语句仍带着它 ⇒ union 不变）。这属"语句切分/分组"，在排除项里；
- 语句的**条数、先后、phase、table 字段**错（同上）；
- `pragmas_outside_tx` 与实际有无重建不符（"有无重建"是方言策略）。

> 判别力自检里"语句丢弃勾选条目"的篡改因此必须**从所有语句里**删同一个
> id——这正是上表第一条的实证，别误以为 oracle 能抓单语句内的缺失。

## 用法

    python3 tools/oracle/ddl_vectors_oracle.py [--check]
"""

import argparse
import json
import os
import sys

import ddl_sql_derive as SQL
import diff_vectors_oracle as DIFF
import schema_rules as R
import select_vectors_oracle as SEL

# 逆 op 表**不在此手写**：契约 `spec/schemas/schemadiff-ops.json` 的
# `inverse` 字段就是权威（`genprotocol ops` 由它生成两端）。
INVERSE_OP = R.INVERSE_OP


def vectors_dir():
    here = os.path.dirname(os.path.abspath(__file__))
    root = os.path.abspath(os.path.join(here, "..", ".."))
    return os.path.join(root, "test", "schema-ddl-vectors")


def inverse_id(eid):
    table, obj, op = eid.split("/")
    return f"{table}/{obj}/{INVERSE_OP[op]}"


def dialect_gate(source, target):
    """DDL 只对 SQLite 生成；其它方言 / Redis / 跨种类一律不支持。

    `d16`–`d19`（PG/MySQL/MSSQL/Oracle）、`d20`（Redis）、`d21`（跨种类）
    都是这个闸门；Redis 与跨种类由 `schema_rules.precheck` 统一判。
    """
    err, _msg = R.precheck(source, target)
    if err:
        return err
    # 支持哪些方言也来自契约（`ddl_dialects`），不在此硬写 "sqlite"。
    for snap in (source, target):
        if snap.get("kind") not in R.DDL_DIALECTS:
            return R.ERR_NOT_SUPPORTED
    return None


def derive(v):
    """返回 (error_code, 正向条目 id 集合, 回滚条目 id 集合)。"""
    src, tgt = v["source"], v["target"]
    err = dialect_gate(src, tgt)
    if err:
        return err, set(), set()

    # 计划来自 diff oracle（完整、含 drop_foreign_key 等），
    # 这里把 object 拆成 needs_rebuild 认得的 "target" 字段。
    plan = DIFF.derive(v)[2]
    plan_ids = {e["id"] for e in plan}
    norm = [
        {
            "id": e["id"],
            "op": e["op"],
            "table": e["table"],
            "target": (e.get("object") or "").lower(),
        }
        for e in plan
    ]

    selected = v.get("selected")
    if selected is None:
        chosen = set(plan_ids)
    else:
        # 去重（保留首次出现，顺序无关——本 oracle 只比集合）。
        chosen = set()
        for sid in selected:
            chosen.add(sid)

    # stale 优先（与 select 规则同序）。
    if any(sid not in plan_ids for sid in chosen):
        return R.ERR_INVALID_PARAM, set(), set()

    # 整组重建必须整组勾选——判定逻辑复用 select oracle 的语义规则，
    # 不在这里另写一份。
    srt = R.real_tables(src)
    trt = R.real_tables(tgt)
    by_id = {e["id"]: e for e in norm}
    rebuild_tables = set()
    for sid in chosen:
        e = by_id[sid]
        if SEL.needs_rebuild(e, srt.get(e["table"].lower()), trt.get(e["table"].lower())):
            rebuild_tables.add(e["table"].lower())
    for table in rebuild_tables:
        same = {e["id"] for e in norm if e["table"].lower() == table}
        if not same <= chosen:
            return R.ERR_INVALID_PARAM, set(), set()

    return None, chosen, {inverse_id(i) for i in chosen}


def check_loaded(v):
    """校验一份已加载的 DDL 语料；返回 (ok, 失败说明列表)。"""
    problems = []
    want_err = v.get("error_code") or ""
    got_err, forward, rollback = derive(v)

    if (got_err or "") != want_err:
        return False, [f"error_code: want={want_err!r} got={got_err or ''!r}"]
    if want_err:
        return True, []

    exp = v["expected"]
    st_union = set()
    for s in exp.get("statements") or []:
        st_union |= set(s.get("entry_ids") or [])
    rb_union = set()
    for s in exp.get("rollback") or []:
        rb_union |= set(s.get("entry_ids") or [])

    if st_union != forward:
        problems.append(
            f"P1 正向条目集合: 脚本缺 {sorted(forward - st_union)} 多 {sorted(st_union - forward)}"
        )
    if rb_union != rollback:
        problems.append(
            f"P2 回滚条目集合: 脚本缺 {sorted(rollback - rb_union)} 多 {sorted(rb_union - rollback)}"
        )

    # 空脚本的两个标志位也是纯规则：有重建才有 PRAGMA 外置（但"有无重建"
    # 属方言策略，故这里只校验 ddl_in_tx 恒 true，不校验 pragmas 标志）。
    if exp.get("ddl_in_tx") is not True:
        problems.append("ddl_in_tx 应为 true")

    # ── 警告文案（§15.8）：按 entry_ids 组核内容 + 「只挂在组内首条」
    problems.extend(check_warnings(v, "statements", forward))
    problems.extend(check_warnings(v, "rollback", rollback))

    # ── 脚本全文（§15.3–§15.8）：语句/回滚/PRAGMA/不可逆逐字段推导
    got_script = SQL.derive_script(v)
    for key in ("statements", "rollback", "pragmas_outside_tx", "ddl_in_tx"):
        if got_script.get(key) != exp.get(key):
            problems.append(f"{key}: 全文推导与语料不符")
    if any("全文推导与语料不符" in p for p in problems):
        # 给出首个差异细节，便于定位
        g, w = got_script.get("statements") or [], exp.get("statements") or []
        done = False
        for key in ("statements", "rollback"):
            g2, w2 = got_script.get(key) or [], exp.get(key) or []
            if len(g2) != len(w2):
                problems.append(f"  首个差异 {key}: 条数 want={len(w2)} got={len(g2)}")
                done = True
                break
            for i, (gi, wi) in enumerate(zip(g2, w2)):
                for k in sorted(set(gi) | set(wi)):
                    if gi.get(k) != wi.get(k):
                        problems.append(
                            f"  首个差异 {key}[{i}].{k}: "
                            f"want={wi.get(k)!r} got={gi.get(k)!r}"
                        )
                        done = True
                        break
                if done:
                    break
            if done:
                break

    return (not problems), problems


def _executed_sides(v, direction):
    """该方向「被执行的那一侧」快照。

    正向执行时库停在 `source`，回滚执行时停在 `target` —— 视图警告只报
    **被执行侧**的视图（`d24` 的 note：回滚方向互换两侧）。
    返回 (before, after, views, kind)。
    """
    src, tgt = v["source"], v["target"]
    if direction == "statements":
        before, after = src, tgt
    else:
        before, after = tgt, src
    views = [t["name"] for t in before.get("tables", []) or []
             if t.get("type") in R.SKIPPED_TABLE_TYPES]
    return before, after, views, after.get("kind", "")


def _cycle_tables(after):
    """参与外键环的表（小写键，升序）。只在新出现的建表之间判。"""
    real = R.real_tables(after)
    edges = {k: set() for k in real}
    for k, t in real.items():
        for fk in t.get("foreign_keys", []) or []:
            ref = str(fk.get("referenced_table") or "").lower()
            if ref in real and ref != k:
                edges[k].add(ref)
    # 找出处于环上的点：能从自己走回来。
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
    return cyclic


def _group_warnings(direction, ids, table, kind, before, after, views, rebuild, cyclic):
    """按 §15.8 推导某个 entry_ids 组应挂的警告。"""
    if rebuild:
        w = [R.W_TRIGGERS, R.W_GENERATED, R.W_CHECK]
        after_tbl = R.real_tables(after).get(table)
        before_tbl = R.real_tables(before).get(table)
        if after_tbl is None:
            return w
        if R.has_integer_pk(kind, after_tbl):
            w.append(R.W_AUTOINCREMENT)
        sc = R.columns_of(before_tbl) if before_tbl is not None else {}
        for c in R.ordered_columns(after_tbl):
            known = str(c["name"]).lower() in sc
            if not c.get("nullable") and R.literal_default(c.get("default_value")) == "" and not known:
                w.append(R.W_REBUILD_NEW_NOT_NULL)
                break
        if views:
            w.append(R.w_view_rebuild(views))
        return w

    op = ids[0].split("/")[2]
    if op == "create_table":
        w = [R.W_CREATE_TABLE]
        after_tbl = R.real_tables(after).get(table)
        if after_tbl is not None and R.has_integer_pk(kind, after_tbl):
            w.append(R.W_AUTOINCREMENT)
        if table in cyclic:
            w.append(R.w_fk_cycle(sorted(cyclic)))
        return w
    if op == "drop_table":
        return [R.w_view_drop(views)] if views else []
    if op == "add_column":
        after_tbl = R.real_tables(after).get(table)
        if after_tbl is None:
            return []
        c = R.columns_of(after_tbl).get(ids[0].split("/")[1])
        if c is None:
            return []
        if (not R.is_constant_default(c.get("default_value"))) or (
            not c.get("nullable") and R.literal_default(c.get("default_value")) == ""
        ):
            return [R.W_ADD_COLUMN_LIMITS]
        return []
    if op == "drop_column":
        return [R.W_DROP_COLUMN_CHECK]
    return []


def check_warnings(v, direction, selected_ids):
    """核该方向每条语句组挂的警告。

    规则（§15.8 + 语料结构）：
      - 同一 `entry_ids` 组的警告**只挂在组内首条**（建临时表那条），其余
        （INSERT / DROP / RENAME / CREATE INDEX / PRAGMA）不带警告；
      - 组内警告内容 = `_group_warnings`。
    """
    problems = []
    before, after, views, kind = _executed_sides(v, direction)

    # 重建表集合：正向/回滚共用同一套（spec §15：正向重建过的表，回滚也走重建）。
    src, tgt = v["source"], v["target"]
    plan = DIFF.derive(v)[2]
    norm = [{"id": e["id"], "op": e["op"], "table": e["table"],
             "target": (e.get("object") or "").lower()} for e in plan]
    srt, trt = R.real_tables(src), R.real_tables(tgt)
    rebuild_tables = set()
    for e in norm:
        tkey = e["table"].lower()
        if SEL.needs_rebuild(e, srt.get(tkey), trt.get(tkey)):
            rebuild_tables.add(tkey)

    cyclic = _cycle_tables(after)

    # 按 entry_ids 收集组内各语句的警告位于第几条
    order = []
    groups = {}
    for i, st in enumerate(v["expected"].get(direction) or []):
        ids = tuple(st.get("entry_ids") or [])
        if not ids:
            if st.get("warnings"):
                problems.append(f"{direction}[{i}] PRAGMA/空组语句不应带警告")
            continue
        if ids not in groups:
            groups[ids] = []
            order.append(ids)
        groups[ids].append((i, st.get("warnings") or []))

    for ids in order:
        table = ids[0].split("/")[0]
        want = _group_warnings(direction, ids, table, kind, before, after, views,
                              table in rebuild_tables, cyclic)
        occ = groups[ids]
        first_i = occ[0][0]
        for i, w in occ:
            if i != first_i and w:
                problems.append(
                    f"{direction}[{i}] 同组非首条不应带警告（组 {list(ids)[0]}）"
                )
        got = occ[0][1]
        if got != want:
            problems.append(
                f"{direction} 组 {list(ids)[0]} 警告不符: want={want} got={got}"
            )
    return problems


def check_vector(path):
    with open(path, "r", encoding="utf-8") as fh:
        return check_loaded(json.load(fh))


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true")
    ap.add_argument("--verbose", action="store_true")
    args = ap.parse_args(argv)

    d = vectors_dir()
    files = sorted(f for f in os.listdir(d) if f.endswith(".json"))
    if not files:
        print(f"未找到语料：{d}", file=sys.stderr)
        return 2

    bad = 0
    for f in files:
        ok, problems = check_vector(os.path.join(d, f))
        if ok:
            if args.verbose:
                print(f"  OK {f}")
        else:
            bad += 1
            print(f"  MISMATCH {os.path.splitext(f)[0]}")
            for p in problems:
                print(f"    {p}")

    print(
        f"\nDDL 结构 oracle：{len(files)} 份语料，吻合 {len(files) - bad}，不吻合 {bad}"
        f"（校验 error_code + 脚本全文：语句/回滚/PRAGMA/不可逆/警告——§15.3–§15.8 已全部写成规则）"
    )
    return 0 if bad == 0 else 1


if __name__ == "__main__":
    sys.exit(main())