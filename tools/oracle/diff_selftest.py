#!/usr/bin/env python3
"""`schema-diff-vectors` 独立 oracle 的**判别力自检**（Item 9 / Item 10，2026-11-10）。

光"oracle 与语料吻合"不能证明 oracle 有判别力——一个恒真的 oracle 也会全绿。
本自检**在内存里**篡改语料（不动磁盘文件），要求 oracle 必须检出：

1. 删掉一条条目（模拟"少了变更"）；
2. 改一个相位码（模拟"阶段顺序理解错"）；
3. 翻一个 `destructive` / `selected`（模拟"选择策略理解错"）；
4. 打乱 `flags` 顺序（模拟"标记顺序理解错"）；
5. 把条目顺序改成相位回退（模拟"计划序错"）；
6. 把 cross-kind 用例的 error_code 抹掉（模拟"不报错"）。

任何一项未被检出 ⇒ oracle 没有判别力 ⇒ 本自检红灯。

    python3 tools/oracle/diff_selftest.py
"""

import json
import os
import sys

import diff_vectors_oracle as O


def vectors_dir():
    here = os.path.dirname(os.path.abspath(__file__))
    root = os.path.abspath(os.path.join(here, "..", ".."))
    return os.path.join(root, "test", "schema-diff-vectors")


def load(name):
    with open(os.path.join(vectors_dir(), name), "r", encoding="utf-8") as fh:
        return json.load(fh)


def tamper_drop_entry(v):
    v["expected"].pop()


def tamper_phase(v):
    v["expected"][0]["phase"] += 1


def tamper_destructive(v):
    v["expected"][0]["destructive"] = not v["expected"][0]["destructive"]


def tamper_selected(v):
    v["expected"][0]["selected"] = not v["expected"][0]["selected"]


def tamper_flags_order(v):
    for e in v["expected"]:
        if e.get("flags") and len(e["flags"]) > 1:
            e["flags"] = list(reversed(e["flags"]))
            return
    raise AssertionError("没有可用的 flags 用例")


def tamper_order(v):
    v["expected"].reverse()


def tamper_error(v):
    v["error_code"] = ""


def tamper_reason(v):
    """改一个字的文案 ⇒ 必须检出。

    `reason` 自 2026-11-10 起纳入推导（格式契约见 spec/behavior.md §14.6.1），
    故它必须和结构字段一样被逐字比较——这一条防止 oracle 退回"排除文案"。
    """
    for e in v["expected"]:
        if e.get("reason"):
            e["reason"] = e["reason"] + "啊"
            return
    raise AssertionError("没有可用的 reason 用例")


# (说明, 语料文件, 篡改函数)
SCENARIOS = [
    ("删掉一条条目", "02_table_add_drop.json", tamper_drop_entry),
    ("改相位码", "02_table_add_drop.json", tamper_phase),
    ("翻 destructive", "02_table_add_drop.json", tamper_destructive),
    ("翻 selected", "02_table_add_drop.json", tamper_selected),
    ("打乱 flags 顺序", "05_type_nullability_autoincrement.json", tamper_flags_order),
    ("条目顺序相位回退", "13_phase_order.json", tamper_order),
    ("抹掉跨种类 error_code", "15_cross_kind_error.json", tamper_error),
    ("篡改 reason 文案", "03_column_add_drop.json", tamper_reason),
]


def main():
    d = vectors_dir()
    if not os.path.isdir(d):
        print(f"语料目录缺失：{d}", file=sys.stderr)
        return 2

    # 基线：未篡改时必须全部吻合
    bad = 0
    for f in sorted(x for x in os.listdir(d) if x.endswith(".json")):
        ok, problems = O.check_vector(os.path.join(d, f))
        if not ok:
            bad += 1
            print(f"基线不吻合 {f}: {problems[:2]}")
    if bad:
        print("基线就红——oracle 与当前语料不一致，先修规则或语料", file=sys.stderr)
        return 1
    print("基线：oracle 与全部语料吻合（绿）")

    all_ok = True
    for desc, name, fn in SCENARIOS:
        v = load(name)
        fn(v)
        ok, problems = O.check_loaded(v)
        status = "检出" if not ok else "未检出（oracle 无判别力！）"
        print(f"  {desc:<22} [{name}] -> {status}")
        if ok:
            all_ok = False
        else:
            print(f"       {problems[0]}")

    print("=" * 60)
    print("diff oracle 判别力自检：" + ("通过" if all_ok else "未通过"))
    return 0 if all_ok else 1


if __name__ == "__main__":
    sys.exit(main())