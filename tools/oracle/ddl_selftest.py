#!/usr/bin/env python3
"""`schema-ddl-vectors` 结构 oracle 的**判别力自检**（Item 9 / Item 10，2026-11-10）。

光"吻合"不能证明有判别力。本自检**在内存里**篡改语料（不动磁盘文件），
要求 oracle 必须检出：

1. 从某条语句的 `entry_ids` 里删掉一个（模拟"勾选的变更被静默丢弃"）；
2. 往 `entry_ids` 里塞一个计划里没有的 id（模拟"凭空造语句"）；
3. 把回滚条目的 op 改成非逆（模拟"回滚方向错"）；
4. 抹掉方言闸门用例的 `error_code`（模拟"非 SQLite 却不报错"）；
5. 抹掉 stale 用例的 `error_code`；
6. 把 `ddl_in_tx` 改成 false。

任何一项未检出 ⇒ oracle 没有判别力 ⇒ 本自检红灯。

    python3 tools/oracle/ddl_selftest.py
"""

import json
import os
import sys

import ddl_vectors_oracle as O


def vectors_dir():
    here = os.path.dirname(os.path.abspath(__file__))
    root = os.path.abspath(os.path.join(here, "..", ".."))
    return os.path.join(root, "test", "schema-ddl-vectors")


def load(name):
    with open(os.path.join(vectors_dir(), name), "r", encoding="utf-8") as fh:
        return json.load(fh)


def tamper_drop_entry_id(v):
    """把某个勾选条目从**所有**语句里删掉（模拟"整个变更被静默丢弃"）。

    注意：只从**一条**语句里删掉同一个 id 是**检测不到**的——本 oracle 用的是
    集合（union），而重建组会把同一组 id 重复挂在多条语句上。那属于"语句
    切分/分组"的范畴，已明确排除（见工具文档的"不校验"表）。
    """
    for s in v["expected"]["statements"]:
        if s.get("entry_ids"):
            victim = s["entry_ids"][0]
            break
    else:
        raise AssertionError("没有可删的 entry_ids")
    for s in v["expected"]["statements"]:
        if s.get("entry_ids") and victim in s["entry_ids"]:
            s["entry_ids"] = [e for e in s["entry_ids"] if e != victim]


def tamper_invent_entry(v):
    v["expected"]["statements"].insert(
        0,
        {"entry_ids": ["t/invented/add_column"], "sql": "x", "phase": 60,
         "table": "t", "non_reversible": False},
    )


def tamper_rollback_direction(v):
    for s in v["expected"]["rollback"]:
        if s.get("entry_ids"):
            eid = s["entry_ids"][0]
            table, obj, op = eid.split("/")
            # 换成正向 op（非逆）⇒ P2 应报"多"。
            s["entry_ids"] = [f"{table}/{obj}/{O.INVERSE_OP[op] if op in O.INVERSE_OP else op}"]
            return
    raise AssertionError("没有可篡改的回滚条目")


def tamper_clear_error(v):
    v["error_code"] = ""


def tamper_ddl_in_tx(v):
    v["expected"]["ddl_in_tx"] = False


SCENARIOS = [
    ("语句丢弃勾选条目", "d05_drop_column_indexed_rebuild.json", tamper_drop_entry_id),
    ("凭空造出条目", "d05_drop_column_indexed_rebuild.json", tamper_invent_entry),
    ("回滚方向非逆", "d03_drop_table.json", tamper_rollback_direction),
    ("抹掉方言闸门 error_code", "d16_pg_dialect_not_supported.json", tamper_clear_error),
    ("抹掉 stale error_code", "d22_stale_selected_id.json", tamper_clear_error),
    ("ddl_in_tx 改 false", "d02_create_table.json", tamper_ddl_in_tx),
]


def main():
    d = vectors_dir()
    if not os.path.isdir(d):
        print(f"语料目录缺失：{d}", file=sys.stderr)
        return 2

    bad = 0
    for f in sorted(x for x in os.listdir(d) if x.endswith(".json")):
        ok, problems = O.check_vector(os.path.join(d, f))
        if not ok:
            bad += 1
            print(f"基线不吻合 {f}: {problems[:2]}")
    if bad:
        print("基线就红——oracle 与当前语料不一致", file=sys.stderr)
        return 1
    print("基线：DDL 结构 oracle 与全部语料吻合（绿）")

    all_ok = True
    for desc, name, fn in SCENARIOS:
        v = load(name)
        fn(v)
        ok, problems = O.check_loaded(v)
        print(f"  {desc:<24} [{name}] -> {'检出' if not ok else '未检出（无判别力！）'}")
        if ok:
            all_ok = False
        else:
            print(f"       {problems[0]}")

    print("=" * 60)
    print("DDL 结构 oracle 判别力自检：" + ("通过" if all_ok else "未通过"))
    return 0 if all_ok else 1


if __name__ == "__main__":
    sys.exit(main())