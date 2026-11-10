#!/usr/bin/env python3
"""`schema-ddl-vectors` 的**结构 oracle**（Item 9 / Item 10，2026-11-10）。

## 它校验什么

DDL 语料的 `expected` 含三类东西，可推导性差别很大：

| 内容 | 可推导？ | 本 oracle |
|---|---|---|
| `error_code`（方言闸门、stale、整组校验） | ✅ 纯规则 | 校验 |
| **渲染出的条目集合**（正/反两向） | ✅ 纯规则 | 校验 |
| 语句**条数/分组/顺序/相位** | ⚠️ 编码 SQLite 方言策略 | **不校验**（见下） |
| `sql` 全文、`warnings` 文案 | ❌ 渲染产物 | **不校验** |

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

    return (not problems), problems


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
        f"（校验 error_code + 正/反两向条目集合；不校验语句切分与 SQL 文本）"
    )
    return 0 if bad == 0 else 1


if __name__ == "__main__":
    sys.exit(main())