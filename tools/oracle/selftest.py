#!/usr/bin/env python3
"""独立 oracle 的**自检**：证明它真能检出"两端共享的误解"。

这是 oracle 门禁的元验证——光跑 oracle 比对语料不能证明 oracle 有判别力：
一个"总是返回 True"的 oracle 也会让门禁全绿。故本自检：

1. 取一份语料，把它的 `expected` 改成**规则推导出的错误值**
   （模拟两端共享误解后回写语料）；
2. 跑 oracle ⇒ 必须报不吻合（退出码非 0）；
3. 还原语料 ⇒ oracle 必须恢复绿（退出码 0）。

若任一步不符，说明 oracle 没有判别力，门禁是假绿 ⇒ 本自检红灯。

**篡改在内存里进行**（§146 §2）：早期版本把篡改写到磁盘再还原，会与并行的
oracle 读进程竞态（基线扫描偶发红）。现在与 diff/ddl 自检同型——读文件 →
内存篡改 → `check_loaded`，全程不碰磁盘。

用法：python3 tools/oracle/selftest.py
"""

import json
import os
import sys

import select_vectors_oracle as SEL

HERE = os.path.dirname(os.path.abspath(__file__))
VEC_DIR = os.path.abspath(os.path.join(HERE, "..", "..", "test", "schema-select-vectors"))


def load(name):
    with open(os.path.join(VEC_DIR, name), "r", encoding="utf-8") as fh:
        return json.load(fh)


def tamper_order(v):
    """场景 1：两端以为"输出按 selected 给出次序"⇒ 交换 expected 顺序。"""
    v["expected"] = list(reversed(v["expected"]))


def tamper_drop(v):
    """场景 2：两端以为"selected=null 也要过滤视图产生的条目"⇒ 删掉一条。"""
    v["expected"] = list(v["expected"]) + ["t/c/add_column"]


def tamper_selected_order(v):
    """场景 3：两端以为"去重保留最后出现"⇒ expected 用 selected 的给出次序。"""
    v["expected"] = list(v["selected"])


def tamper_stale(v):
    """场景 4：两端以为"stale id 静默忽略而非报错"⇒ 期望改成成功子集。"""
    v["expected"] = ["t/b/add_column"]
    v["error_code"] = ""
    v["error_message"] = ""


def tamper_group(v):
    """场景 5：两端以为"重建组不必整组勾选"⇒ 期望改成部分勾选成功。"""
    v["expected"] = ["t/qty/alter_column"]
    v["error_code"] = ""
    v["error_message"] = ""


SCENARIOS = [
    ("s01_all_selected_null.json", tamper_order),
    ("s02_empty_selection.json", tamper_drop),
    ("s03_reordered_selection.json", tamper_selected_order),
    ("s05_stale_single_id.json", tamper_stale),
    ("s07_rebuild_group_partial.json", tamper_group),
]


def main():
    if not os.path.isdir(VEC_DIR):
        print(f"向量目录缺失：{VEC_DIR}", file=sys.stderr)
        return 2

    # 基线：未篡改时必须绿
    bad = 0
    for f in sorted(x for x in os.listdir(VEC_DIR) if x.endswith(".json")):
        with open(os.path.join(VEC_DIR, f), "r", encoding="utf-8") as fh:
            v = json.load(fh)
        ok, why = SEL.check_loaded(v)
        if not ok:
            bad += 1
            print(f"  基线不吻合 {f}: {why[0][:120]}")
    if bad:
        print("基线就红——oracle 与当前语料不一致，先修语料或规则：", file=sys.stderr)
        return 1
    print("[baseline] oracle agrees with the corpus (green)")

    all_ok = True
    for name, mutate in SCENARIOS:
        path = os.path.join(VEC_DIR, name)
        if not os.path.exists(path):
            print(f"  SKIP {name} (missing)")
            continue
        v = load(name)
        mutate(v)
        ok, why = SEL.check_loaded(v)
        status = "DETECTED" if not ok else "NOT DETECTED (oracle has no discriminating power!)"
        print(f"  {name}: {status}")
        if not ok:
            print(f"       {why[0][:120]}")
        else:
            all_ok = False

    print("=" * 60)
    print("oracle discriminating-power selftest: " + ("PASS" if all_ok else "FAIL"))
    return 0 if all_ok else 1


if __name__ == "__main__":
    sys.exit(main())