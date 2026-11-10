#!/usr/bin/env python3
"""polydb 共享向量语料的**独立 oracle**（Item 9 / Item 10，2026-11-10）。

## 为什么需要它

三套共享向量语料的 `expected` 原本都由**某一端实现**回写（见
WORKSPACE §112 §3.5）：

| 语料 | 原生产者 | 另一端 |
|---|---|---|
| `schema-diff-vectors` | Rust（`examples/regen_expected.rs`） | Go 只读 |
| `schema-ddl-vectors` | Go（`ddl_vectors_test.go`） | Rust 只读 |
| `schema-select-vectors` | Go（`select_vectors_test.go`） | Rust 自认"第二实现验证" |

于是两端只互相核对"我与它一致"——若两端**共享同一个误解**（相位码、
方言解析、类型宽度判断都写错但写法一致），语料会把**共享的 bug 锁死
为范式**。这就是"循环论证"。

本工具是**第三套推导**：它不读 Go 也不读 Rust 的实现，只依据语料 note
里明示的**规则**（相位码约定、id 构成、去重/排序/校验规则）从
`source`/`target`/`selected` 直接重算期望值，再与语料比对。

**因此它能发现"两端都错"**：两端共享误解时，本工具仍会算出规则要求的
值，与语料不符 ⇒ 红灯。

## 覆盖范围

本文件覆盖 **`schema-select-vectors`**（18 份）——它是三套里最"元"的一层：
输出的 id 序列是另两层（diff 计划、DDL 脚本）的基础。

- `schema-diff-vectors`（17 份）：**已由 `diff_vectors_oracle.py` 覆盖**
  （2026-11-10 补上）。它推导的是**完整计划条目**（id/op/phase/table/
  object/from/to/flags/destructive/selected），只把 `reason` **文案**显式
  排除——文案无法从快照推导，强行复刻等于抄实现。
- `schema-ddl-vectors`（27 份）：**仍不覆盖**。它的 expected 含完整 SQL
  文本与 warnings 文案，要独立推导就得复刻 SQL 生成与措辞——那等于把实现
  重写一遍，反而引入新的共享误解面。

规则集（相位码、类型/默认值归一等）统一放在 `schema_rules.py`，两个 oracle
共用**同一份**规则。

## 用法

    python3 tools/oracle/select_vectors_oracle.py [--check]

`--check`（默认）：比对并报告不符的语料，退出码非 0 即红灯。
不加参数：打印每份语料的推导结果（人工复核用）。

## 规则（来源：语料 note 的明示约定）

1. **相位码**（决定计划序，**完整九种**，见 `schema_rules.PHASE`）：
   `drop_foreign_key(10) < drop_index(20) < drop_column(30) < drop_table(40)
    < create_table(50) < add_column(60) < alter_column(70) < add_index(80)
    < add_foreign_key(90)`
2. **id 形状**：`<table>/<column|index|fk>/<op>`，**全部小写**；
   表级变更（create_table/drop_table）的目标段为空。
3. **selected**：`null` ⇒ 全部条目；`[]` ⇒ 空；非空 ⇒ 只取勾选子集。
4. **去重**：同一 id 在 selected 里重复出现只算一次。
5. **输出顺序** = 计划序（**不是** selected 数组的给出次序）。
6. **stale 优先**：selected 里有不在当前计划中的 id ⇒ 报
   `POLYDB_ERR_INVALID_PARAM`（stale 检查在整组校验**之前**）。
7. **整组重建**：表被 `alter_column` 触发整表重建时，同表变更必须**整组**
   勾选，否则报 `POLYDB_ERR_INVALID_PARAM`（缺组名按计划序列出）。
8. **视图不进比对**：源/目标里的 view 与 materialized_view 既不产生条目，
   也不参与整组判定。
9. **id 逐字匹配**（大小写敏感）：`Foo/Note/add_column` 不匹配
   `foo/note/add_column` ⇒ 视为 stale。
"""

import argparse
import glob
import json
import os
import sys

import schema_rules as R

# ---------------------------------------------------------------- 相位码
#
# **规则只此一份**：从 `schema_rules` 取（2026-11-10 起）。此前本文件自带的
# 那份相位表漏了 `drop_foreign_key(10)`——select 语料恰好没覆盖"删外键"，
# 18/18 全绿把这个缺口掩盖了，直到 diff 语料才暴露。别在调用方另立一份。
PHASE = R.PHASE

ERR_INVALID_PARAM = R.ERR_INVALID_PARAM


def vectors_dir():
    here = os.path.dirname(os.path.abspath(__file__))
    # tools/oracle/ → 仓库根 → test/schema-select-vectors
    root = os.path.abspath(os.path.join(here, "..", ".."))
    return os.path.join(root, "test", "schema-select-vectors")


def real_tables(snapshot):
    """只取真表（view / materialized_view 不进比对）。"""
    out = {}
    for t in snapshot.get("tables", []) or []:
        if t.get("type") == "table":
            out[t["name"]] = t
    return out


def norm_type(dt, kind=""):
    """类型名归一（规则见 `schema_rules.norm_type`）。

    语料 01_identical 刻意用 `integer` vs `INTEGER`、
    `character varying(100)` vs `  CHARACTER   VARYING ( 100 )` 两套写法
    表达"同一类型"——故类型比较必须归一。**实现在 `schema_rules`**：
    本文件不再自带一份，避免两份规则漂移。
    """
    return R.norm_type(dt, kind)


def columns_of(table):
    return {c["name"].lower(): c for c in table.get("columns", []) or []}


def indexes_of(table):
    return {i["name"].lower(): i for i in table.get("indexes", []) or []}


def fks_of(table):
    return {f["name"].lower(): f for f in table.get("foreign_keys", []) or []}


def build_plan(source, target, options):
    """独立推导计划条目。

    返回 list of dict: {"id": str, "phase": int, "table": str,
    "target": str, "op": str}
    """
    src = real_tables(source)
    tgt = real_tables(target)
    entries = []

    for name in tgt:
        if name not in src:
            entries.append(dict(id=f"{name.lower()}//create_table",
                                phase=PHASE["create_table"], table=name,
                                target="", op="create_table"))
    for name in src:
        if name not in tgt:
            entries.append(dict(id=f"{name.lower()}//drop_table",
                                phase=PHASE["drop_table"], table=name,
                                target="", op="drop_table"))

    for name, s in src.items():
        if name not in tgt:
            continue
        t = tgt[name]
        scol, tcol = columns_of(s), columns_of(t)
        for cn in scol:
            if cn not in tcol:
                entries.append(dict(id=f"{name.lower()}/{cn}/drop_column",
                                    phase=PHASE["drop_column"], table=name,
                                    target=cn, op="drop_column"))
        for cn in tcol:
            if cn not in scol:
                entries.append(dict(id=f"{name.lower()}/{cn}/add_column",
                                    phase=PHASE["add_column"], table=name,
                                    target=cn, op="add_column"))
        for cn, bc in tcol.items():
            if cn not in scol:
                continue
            ac = scol[cn]
            kind = (source.get("kind") or "")
            changed = norm_type(ac.get("data_type"), kind) != norm_type(bc.get("data_type"), kind)
            changed |= bool(ac.get("nullable")) != bool(bc.get("nullable"))
            changed |= bool(ac.get("is_primary_key")) != bool(bc.get("is_primary_key"))
            if not changed and not options.get("ignore_defaults"):
                changed |= ac.get("default_value") != bc.get("default_value")
            if changed:
                entries.append(dict(id=f"{name.lower()}/{cn}/alter_column",
                                    phase=PHASE["alter_column"], table=name,
                                    target=cn, op="alter_column"))
        sidx, tidx = indexes_of(s), indexes_of(t)
        for iname in sidx:
            if iname not in tidx:
                obj = R.index_object(sidx[iname]).lower()
                entries.append(dict(id=f"{name.lower()}/{obj}/drop_index",
                                    phase=PHASE["drop_index"], table=name,
                                    target=iname, op="drop_index"))
        for iname in tidx:
            if iname not in sidx:
                obj = R.index_object(tidx[iname]).lower()
                entries.append(dict(id=f"{name.lower()}/{obj}/add_index",
                                    phase=PHASE["add_index"], table=name,
                                    target=iname, op="add_index"))
        sfk, tfk = fks_of(s), fks_of(t)
        for fname in tfk:
            if fname not in sfk:
                obj = R.fk_object(tfk[fname]).lower()
                entries.append(dict(id=f"{name.lower()}/{obj}/add_foreign_key",
                                    phase=PHASE["add_foreign_key"], table=name,
                                    target=fname, op="add_foreign_key"))
        # 删外键（相位 10，全表最低）。**此前缺失**——与相位表漏
        # `drop_foreign_key` 是同一个盲区：select 语料从没出现过「删外键」，
        # 于是这条分支不存在也能 18/18 全绿（PD-31，2026-11-10）。
        for fname in sfk:
            if fname not in tfk:
                obj = R.fk_object(sfk[fname]).lower()
                entries.append(dict(id=f"{name.lower()}/{obj}/drop_foreign_key",
                                    phase=PHASE["drop_foreign_key"], table=name,
                                    target=fname, op="drop_foreign_key"))

    # 计划序：相位码 → 表名 → 目标名
    entries.sort(key=lambda e: (e["phase"], e["table"], e["target"]))
    return entries


def is_auto_index(name):
    """SQLite 的自动索引（主键/唯一约束的隐式索引）——删改它必须整表重建。"""
    return name.lower().startswith("sqlite_autoindex_")


def needs_rebuild(entry, src_table, tgt_table):
    """判定某个条目是否**必须整表重建**（规则 2/3/4）。

    这是**语义规则**——"哪些变更无法用 ALTER 直改、必须走
    建新表→搬数据→改名 的路径"，与任一端的代码实现无关：

      * alter_column / add_foreign_key / drop_foreign_key：一律重建
        （改列类型、改约束、加/删外键都不是 SQLite/PG 的直改能力）；
      * drop_column：删的是**主键列**或**被某个索引覆盖的列** ⇒ 重建
        （删了它，索引/主键就没法原地 ALTER）；
      * add_index / drop_index：索引是 **primary** 或 **autoindex** ⇒ 重建
        （主键/自动索引不能靠 CREATE INDEX 改）。
    """
    op = entry["op"]
    if op in ("alter_column", "add_foreign_key", "drop_foreign_key"):
        return True
    if op == "drop_column":
        if src_table is None:
            return False
        col = entry["target"].lower()
        for c in src_table.get("columns", []) or []:
            if c["name"].lower() == col and c.get("is_primary_key"):
                return True
        for i in src_table.get("indexes", []) or []:
            for ic in i.get("columns", []) or []:
                if ic["name"].lower() == col:
                    return True
        return False
    if op in ("add_index", "drop_index"):
        table = tgt_table if op == "add_index" else src_table
        if table is not None:
            for i in table.get("indexes", []) or []:
                if i["name"].lower() == entry["target"].lower():
                    return bool(i.get("primary")) or is_auto_index(i["name"])
        return is_auto_index(entry["target"])
    return False


def rebuild_groups(plan, source, target):
    """整表重建的分组：含"必须重建"条目的表，同表所有条目必须整组勾选。

    返回 {table: [该表在计划中的全部条目 id]}。
    """
    src = real_tables(source)
    tgt = real_tables(target)
    groups = {}
    for e in plan:
        groups.setdefault(e["table"], []).append(e["id"])
    triggers = set()
    for e in plan:
        st = src.get(e["table"])
        tt = tgt.get(e["table"])
        if needs_rebuild(e, st, tt):
            triggers.add(e["table"])
    return {t: ids for t, ids in groups.items() if t in triggers}


def derive(v):
    """从语料的 source/target/selected 独立推导期望值。

    返回 ("ok", [id...]) 或 ("err", code)。
    """
    options = v.get("options", {}) or {}
    plan = build_plan(v["source"], v["target"], options)
    plan_ids = [e["id"] for e in plan]

    selected = v.get("selected")
    if selected is None:
        chosen = list(plan_ids)
    else:
        # 去重（保留首次出现）
        seen = set()
        chosen = []
        for sid in selected:
            if sid not in seen:
                seen.add(sid)
                chosen.append(sid)

    # 规则 6：stale 优先于整组校验。
    stale = [sid for sid in chosen if sid not in set(plan_ids)]
    if stale:
        return ("err", ERR_INVALID_PARAM)

    # 规则 7：整组重建必须整组勾选。
    # 关键细节（s16 vs s17 的分歧点）：**只对"已勾选条目本身需要重建"**
    # 的表判定整组——已勾选条目若都是直改（如新增列），则不触发组检查。
    # s17：勾选 t/p_id/add_column（新增列是直改）⇒ 通过；
    # s16：勾选 t/fk_t_p/add_foreign_key（加外键必重建）⇒ 需该表整组。
    picked = set(chosen)
    src = real_tables(v["source"])
    tgt = real_tables(v["target"])
    by_id = {e["id"]: e for e in plan}
    rebuild_tables = set()
    for sid in picked:
        e = by_id.get(sid)
        if e is None:
            continue
        if needs_rebuild(e, src.get(e["table"]), tgt.get(e["table"])):
            rebuild_tables.add(e["table"])
    for table in rebuild_tables:
        ids = [e["id"] for e in plan if e["table"] == table]
        missing = [i for i in ids if i not in picked]
        if missing:
            return ("err", ERR_INVALID_PARAM)

    # 规则 5：输出按计划序。
    return ("ok", [i for i in plan_ids if i in picked])


def load_vectors():
    d = vectors_dir()
    if not os.path.isdir(d):
        print(f"向量目录缺失：{d}", file=sys.stderr)
        return []
    out = []
    for p in sorted(glob.glob(os.path.join(d, "*.json"))):
        with open(p, "r", encoding="utf-8") as fh:
            out.append((p, json.load(fh)))
    return out


def main():
    ap = argparse.ArgumentParser(description="select 向量语料的独立 oracle")
    ap.add_argument("--check", action="store_true",
                    help="比对语料并以退出码报告（默认行为）")
    args = ap.parse_args()

    vectors = load_vectors()
    if not vectors:
        print("没有可读的向量语料", file=sys.stderr)
        return 2

    agree = mismatch = 0
    problems = []
    for path, v in vectors:
        kind, got = derive(v)
        want_kind = "err" if v.get("expected") is None else "ok"
        want = v.get("expected")
        ok = (kind == want_kind) and (want_kind == "err" or got == want)
        if ok:
            agree += 1
        else:
            mismatch += 1
            problems.append((v.get("name"), kind, got, want_kind, want))

    print(f"独立 oracle 比对：{len(vectors)} 份语料，吻合 {agree}，不吻合 {mismatch}")
    for name, kind, got, want_kind, want in problems:
        print(f"  MISMATCH {name}")
        print(f"    oracle 推导: {kind} {got}")
        print(f"    语料 expected: {want_kind} {want}")
        print("    ==> 两端共享的误解被独立 oracle 检出（语料锁死了 bug）")
    return 1 if mismatch else 0


if __name__ == "__main__":
    sys.exit(main())
