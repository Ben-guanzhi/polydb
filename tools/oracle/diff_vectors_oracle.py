#!/usr/bin/env python3
"""`schema-diff-vectors` 的**独立 oracle**（Item 9 / Item 10 剩余部分，2026-11-10）。

## 它做什么

对每份 diff 语料，**只依据 `schema_rules` 里语料 note 明示的规则**，从
`source`/`target`/`options` 重新推导出**计划条目**，再与语料的 `expected`
逐字段比对。

这与 `select_vectors_oracle.py` 是同一思路（第三套推导，不读两端实现），
只是覆盖面从"选中的 id 序列"扩到了"完整的计划条目"。

## 覆盖什么、不覆盖什么

**覆盖**（全部可由规则推导）：

- 条目**集合**：哪些表/列/索引/外键产生了变更、以及 op；
- `phase`（相位码表）、`table`、`object`（原大小写）、`from`/`to`、
  `flags`（规范顺序）、`destructive`、`selected`（选择策略）；
- **`reason` 文案**——2026-11-10 起：其格式已升为**契约**
  （`spec/behavior.md` §14.6.1），故可逐字推导，不再排除。
  词汇表与各 op 模板在 `schema_rules`（`null_word` / `index_change_reason`
  / `fk_change_reason` / `alter_reason` / `SIMPLE_REASON` / `PK_REASON`）。
- 条目**顺序**：按相位不减；同相位的 create_table 满足"被引用表在前"
  的拓扑性质（有环时跳过该检查——语料对环的处置见 `17` 的 note）。

**不覆盖**：无。本 oracle 现在逐字段比对**整条 entry**。

> 演进记录：`reason` 原先被排除，理由是"散文无法从快照推导"。那句判断在
> **当时的契约下**是对的——`spec/behavior.md` 只说"由语料锁定"，没规定格式。
> 把格式写进契约后（§14.6.1），文案就成了可推导量，排除项随之消失。
> 这也说明"不可推导"往往是**契约定得不够**，而不是真的不可推导。

## 用法

    python3 tools/oracle/diff_vectors_oracle.py [--check]

`--check`（默认）：比对并报告不符，退出码非 0 即红灯。
不加参数：打印每份语料的推导结果（人工复核用）。
"""

import argparse
import json
import os
import sys

import schema_rules as R


def vectors_dir():
    here = os.path.dirname(os.path.abspath(__file__))
    root = os.path.abspath(os.path.join(here, "..", ".."))
    return os.path.join(root, "test", "schema-diff-vectors")


def _entry(op, table, obj, tname, **kw):
    """按规则造一条计划条目。"""
    destructive = R.is_destructive(op)
    e = {
        "id": R.entry_id(tname, obj, op),
        "op": op,
        "phase": R.PHASE[op],
        "table": table,
        "destructive": destructive,
        "selected": R.policy_selected(destructive, kw.pop("select_destructive", False)),
    }
    if obj:
        e["object"] = obj
    e.update(kw)
    # 简单文案（对象仅在一侧）从契约表取；成对/组合文案由调用方显式传入。
    if "reason" not in e:
        e["reason"] = R.SIMPLE_REASON.get(op, "")
    return e


def derive(v):
    """从快照推导 (error_code, error_message, entries)。"""
    src_snap, tgt_snap = v["source"], v["target"]
    err_code, err_msg = R.precheck(src_snap, tgt_snap)
    if err_code:
        return err_code, err_msg, []

    opts = v.get("options") or {}
    sel_destr = bool(opts.get("select_destructive"))
    ignore_defaults = bool(opts.get("ignore_defaults"))

    src = R.real_tables(src_snap)
    tgt = R.real_tables(tgt_snap)
    entries = []

    def tname_for(key):
        """条目里的 table 字段用**目标侧**原名（计划朝目标走），无则用源侧。"""
        return (tgt.get(key) or src.get(key))["name"]

    # ── 表级
    for key in tgt:
        if key not in src:
            entries.append(
                _entry("create_table", tname_for(key), None, key, select_destructive=sel_destr)
            )
    for key in src:
        if key not in tgt:
            entries.append(
                _entry("drop_table", tname_for(key), None, key, select_destructive=sel_destr)
            )

    # ── 表内
    for key in tgt:
        if key not in src:
            continue
        s, t = src[key], tgt[key]
        table = tname_for(key)

        # 列
        kind = tgt_snap.get("kind", "")
        sc, tc = R.columns_of(s), R.columns_of(t)
        for ckey in tc:
            if ckey not in sc:
                entries.append(
                    _entry(
                        "add_column",
                        table,
                        tc[ckey]["name"],
                        key,
                        to=R.norm_type(tc[ckey].get("data_type"), kind),
                        select_destructive=sel_destr,
                    )
                )
        for ckey in sc:
            if ckey not in tc:
                entries.append(
                    _entry(
                        "drop_column",
                        table,
                        sc[ckey]["name"],
                        key,
                        **{"from": R.norm_type(sc[ckey].get("data_type"), kind)},
                        select_destructive=sel_destr,
                    )
                )
        for ckey in sc:
            if ckey not in tc:
                continue
            a, b = sc[ckey], tc[ckey]
            flags = R.alter_flags(a, b, ignore_defaults, kind)
            if not flags:
                continue
            entries.append(
                _entry(
                    "alter_column",
                    table,
                    b["name"],
                    key,
                    **{
                        # from/to 与 `类型` 分句同源：都是 NormalizeType 的结果
                        # （两端实现也是 `e.From = NormalizeType(...)`）。
                        "from": R.norm_type(a.get("data_type"), kind),
                        "to": R.norm_type(b.get("data_type"), kind),
                        "flags": flags,
                        "reason": R.alter_reason(flags, a, b, kind),
                    },
                    select_destructive=sel_destr,
                )
            )

        # 索引（主键按 primary 标志配对，其余按名字）
        si, ti = R.indexes_of(s), R.indexes_of(t)
        s_pk = [i for i in si.values() if i.get("primary")]
        t_pk = [i for i in ti.values() if i.get("primary")]
        if s_pk and t_pk and R.index_sig(s_pk[0]) != R.index_sig(t_pk[0]):
            # 主键列变化：成对产出的两条文案相同（§14.6.1）。
            entries.append(
                _entry(
                    "drop_index", table, R.index_object(s_pk[0]), key,
                    reason=R.PK_REASON, select_destructive=sel_destr,
                )
            )
            entries.append(
                _entry(
                    "add_index", table, R.index_object(t_pk[0]), key,
                    reason=R.PK_REASON, select_destructive=sel_destr,
                )
            )
        elif bool(s_pk) != bool(t_pk):
            for i in s_pk:
                entries.append(
                    _entry("drop_index", table, R.index_object(i), key, select_destructive=sel_destr)
                )
            for i in t_pk:
                entries.append(
                    _entry("add_index", table, R.index_object(i), key, select_destructive=sel_destr)
                )

        for ikey in ti:
            if ikey not in si or si[ikey].get("primary") or ti[ikey].get("primary"):
                continue
            if R.index_sig(si[ikey]) != R.index_sig(ti[ikey]):
                # 索引定义变化：成对产出的两条文案相同（§14.6.1）。
                rsn = R.index_change_reason(si[ikey], ti[ikey])
                entries.append(
                    _entry(
                        "drop_index", table, R.index_object(si[ikey]), key,
                        reason=rsn, select_destructive=sel_destr,
                    )
                )
                entries.append(
                    _entry(
                        "add_index", table, R.index_object(ti[ikey]), key,
                        reason=rsn, select_destructive=sel_destr,
                    )
                )
            continue
        for ikey in si:
            if ikey in ti or si[ikey].get("primary"):
                continue
            entries.append(
                _entry("drop_index", table, R.index_object(si[ikey]), key, select_destructive=sel_destr)
            )
        for ikey in ti:
            if ikey in si or ti[ikey].get("primary"):
                continue
            entries.append(
                _entry("add_index", table, R.index_object(ti[ikey]), key, select_destructive=sel_destr)
            )

        # 外键（按名字匹配）
        sf, tf = R.fks_of(s), R.fks_of(t)
        for fkey in tf:
            if fkey not in sf:
                entries.append(
                    _entry(
                        "add_foreign_key",
                        table,
                        R.fk_object(tf[fkey]),
                        key,
                        select_destructive=sel_destr,
                    )
                )
        for fkey in sf:
            if fkey not in tf:
                entries.append(
                    _entry(
                        "drop_foreign_key",
                        table,
                        R.fk_object(sf[fkey]),
                        key,
                        select_destructive=sel_destr,
                    )
                )
        for fkey in sf:
            if fkey not in tf:
                continue
            if R.fk_sig(sf[fkey]) != R.fk_sig(tf[fkey]):
                # 外键定义变化：成对产出的两条文案相同（§14.6.1）。
                rsn = R.fk_change_reason(sf[fkey], tf[fkey])
                entries.append(
                    _entry(
                        "drop_foreign_key",
                        table,
                        R.fk_object(sf[fkey]),
                        key,
                        reason=rsn,
                        select_destructive=sel_destr,
                    )
                )
                entries.append(
                    _entry(
                        "add_foreign_key",
                        table,
                        R.fk_object(tf[fkey]),
                        key,
                        reason=rsn,
                        select_destructive=sel_destr,
                    )
                )

    # 稳定排序：相位 → 表名 → id；create_table 段再做 §14.4 的依赖拓扑。
    entries.sort(key=lambda e: (e["phase"], e["table"].lower(), e["id"]))
    entries = _topo_create_tables(entries, tgt)
    return None, None, entries


def _topo_create_tables(entries, tgt):
    """§14.4 的建表拓扑：create_table 段内父表先建。

    依赖图只认**本段内**的新建表、排除自引用；起点按小写键升序；
    `visit(k)` 先递归依赖再输出自身，因此无环时父表在前、环时按 DFS 到达序
    （与两端实现同规则——Go `topoCreateTables`，spec §14.4 明文）。
    """
    by_key = {}
    for e in entries:
        if e["op"] == "create_table":
            by_key[e["table"].lower()] = e
    if len(by_key) < 2:
        return entries
    deps = {}
    for k in sorted(by_key):
        t = tgt.get(k)
        d = set()
        if t:
            for fk in t.get("foreign_keys", []) or []:
                r = str(fk.get("referenced_table") or "").lower()
                if r in by_key and r != k:
                    d.add(r)
        deps[k] = sorted(d)
    visited = set()
    order_keys = []

    def visit(k):
        if k in visited:
            return
        visited.add(k)
        for r in deps.get(k, ()):
            visit(r)
        order_keys.append(k)

    for k in sorted(by_key):
        visit(k)
    # create_table 段按 DFS 到达序替换原位（stable：段内次序即拓扑序）。
    create_order = iter([by_key[k] for k in order_keys])
    return [
        next(create_order) if e["op"] == "create_table" else e
        for e in entries
    ]


# ──────────────────────────────────────────────────────────── 比对
COMPARE_FIELDS = [
    "id",
    "op",
    "phase",
    "table",
    "object",
    "from",
    "to",
    "flags",
    "destructive",
    "selected",
    # `reason` 自 2026-11-10 起**也纳入比较**：其格式已升为契约
    # （spec/behavior.md §14.6.1），可逐字推导，不再需要排除。
    "reason",
]


def project(entries):
    """按 COMPARE_FIELDS 投影（**含** `reason`）。"""
    return [{k: e.get(k) for k in COMPARE_FIELDS} for e in entries]


def check_vector(path):
    """返回 (ok, 失败说明列表)。"""
    with open(path, "r", encoding="utf-8") as fh:
        return check_loaded(json.load(fh))


def check_loaded(v):
    """对一个**已加载**的语料 dict 做校验。

    单独拆出来是为了让判别力自检（`selftest.py`）能**在内存里**篡改语料
    再校验——不必改动磁盘上的文件（文件被中途失败的测试改坏是真实事故，
    见 WORKSPACE §135 §9）。
     """
    problems = []
    want_err = v.get("error_code") or ""
    got_err, got_msg, entries = derive(v)

    if (got_err or "") != want_err:
        problems.append(f"error_code: want={want_err!r} got={got_err or ''!r}")
    if want_err:
        return (not problems), problems

    want = project(v["expected"])
    got = project(entries)

    if want != got:
        if len(want) != len(got):
            problems.append(f"条目数: want={len(want)} got={len(got)}")
        want_by_id = {e["id"]: e for e in want}
        got_by_id = {e["id"]: e for e in got}
        for eid in sorted(set(want_by_id) - set(got_by_id)):
            problems.append(f"推导缺条目: {eid}")
        for eid in sorted(set(got_by_id) - set(want_by_id)):
            problems.append(f"推导多条目: {eid}")
        for eid in sorted(set(want_by_id) & set(got_by_id)):
            w, g = want_by_id[eid], got_by_id[eid]
            for f in COMPARE_FIELDS:
                if w.get(f) != g.get(f):
                    problems.append(f"{eid}.{f}: want={w.get(f)!r} got={g.get(f)!r}")

    # 顺序：相位不得回退（语料的既有约定）。
    phases = [e["phase"] for e in v["expected"]]
    for i in range(1, len(phases)):
        if phases[i] < phases[i - 1]:
            problems.append(f"语料顺序：第 {i} 条相位 {phases[i]} 小于前一条 {phases[i-1]}")

    # 同相位 create_table 的拓扑性质：被引用表须先出现（有环则跳过）。
    problems.extend(check_create_topo(v))

    return (not problems), problems


def check_create_topo(v):
    """同相位 create_table：被引用的表应排在引用者之前。

    只检查**能被推导为同一相位**的情形；有环时不检查（语料对环的处置是
    实现选择，见 `17_create_table_fk_cycle` 的 note）。
    """
    creates = [e for e in v.get("expected") or [] if e.get("op") == "create_table"]
    if len(creates) < 2:
        return []
    order = [e["id"].split("/")[0] for e in creates]
    pos = {t: i for i, t in enumerate(order)}

    # 从目标快照取外键依赖（表 -> 被引用表）。
    tgt = R.real_tables(v["target"])
    deps = []
    for key, t in tgt.items():
        for fk in t.get("foreign_keys", []) or []:
            ref = (fk.get("referenced_table") or "").lower()
            if ref and ref in pos and key in pos and ref != key:
                deps.append((key, ref))
    # 有环就不判（环内无拓扑序）。
    for a, b in deps:
        if any(x == a and y == b for x, y in deps) and any(x == b and y == a for x, y in deps):
            return []
    out = []
    for a, b in deps:
        if pos[b] > pos[a]:
            out.append(f"拓扑序：{b} 被 {a} 引用，但排在 {a} 之后")
    return out


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="仅为校验（默认行为）")
    ap.add_argument("--verbose", action="store_true")
    args = ap.parse_args(argv)

    d = vectors_dir()
    files = sorted(f for f in os.listdir(d) if f.endswith(".json"))
    if not files:
        print(f"未找到语料：{d}", file=sys.stderr)
        return 2

    bad = 0
    for f in files:
        path = os.path.join(d, f)
        ok, problems = check_vector(path)
        if ok:
            if args.verbose:
                _err, _msg, entries = derive(json.load(open(path, encoding="utf-8")))
                print(f"  OK {f}  ({len(entries)} 条)")
        else:
            bad += 1
            print(f"  MISMATCH {os.path.splitext(f)[0]}")
            for p in problems:
                print(f"    {p}")

    print(
        f"\n独立 oracle 比对：{len(files)} 份语料，"
        f"吻合 {len(files) - bad}，不吻合 {bad}"
    )
    return 0 if bad == 0 else 1


if __name__ == "__main__":
    sys.exit(main())