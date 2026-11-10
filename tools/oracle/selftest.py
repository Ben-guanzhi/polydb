#!/usr/bin/env python3
"""独立 oracle 的**自检**：证明它真能检出"两端共享的误解"。

这是 oracle 门禁的元验证——光跑 oracle 比对语料不能证明 oracle 有判别力：
一个"总是返回 True"的 oracle 也会让门禁全绿。故本自检：

1. 取一份语料，把它的 `expected` 改成**规则推导出的错误值**
   （模拟两端共享误解后回写语料）；
2. 跑 oracle ⇒ 必须报不吻合（退出码非 0）；
3. 还原语料 ⇒ oracle 必须恢复绿（退出码 0）。

若任一步不符，说明 oracle 没有判别力，门禁是假绿 ⇒ 本自检红灯。

用法：python3 tools/oracle/selftest.py
"""

import json
import os
import shutil
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ORACLE = os.path.join(HERE, "select_vectors_oracle.py")
VEC_DIR = os.path.abspath(os.path.join(HERE, "..", "..", "test", "schema-select-vectors"))


def run_oracle():
    r = subprocess.run(
        [sys.executable, ORACLE, "--check"],
        capture_output=True, timeout=300,
    )
    return r.returncode, (r.stdout or b"").decode("utf-8", "replace")


def with_tampered(name, mutate):
    """临时改一份语料的 expected，跑 oracle，再还原。返回 (检出?, 输出)。"""
    path = os.path.join(VEC_DIR, name)
    backup = path + ".selftest.bak"
    shutil.copyfile(path, backup)
    try:
        with open(path, "r", encoding="utf-8") as fh:
            v = json.load(fh)
        mutate(v)
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(v, fh, ensure_ascii=False, indent=2)
        code, out = run_oracle()
        return code != 0, out
    finally:
        shutil.move(backup, path)


def tamper_order(v):
    """场景 1：两端以为"输出按 selected 给出次序"⇒ 交换 expected 顺序。"""
    v["expected"] = list(reversed(v["expected"]))


def tamper_drop(v):
    """场景 2：两端以为"selected=null 也要过滤视图产生的条目"⇒ 删掉一条。"""
    v["expected"] = list(v["expected"]) + ["t/c/add_column"]


def tamper_selected_order(v):
    """场景 3：两端以为"去重保留最后出现"⇒ expected 用 selected 的给出次序。"""
    v["expected"] = list(v["selected"])


SCENARIOS = [
    ("s01_all_selected_null.json", tamper_order),
    ("s02_empty_selection.json", tamper_drop),
    ("s03_reordered_selection.json", tamper_selected_order),
]


def main():
    if not os.path.isdir(VEC_DIR):
        print(f"向量目录缺失：{VEC_DIR}", file=sys.stderr)
        return 2

    # 基线：未篡改时必须绿
    code, out = run_oracle()
    if code != 0:
        print("[baseline RED] oracle disagrees with the corpus; fix corpus or rules first:")
        print(out)
        return 1
    print("[baseline] oracle agrees with the corpus (green)")

    all_ok = True
    for name, mutate in SCENARIOS:
        path = os.path.join(VEC_DIR, name)
        if not os.path.exists(path):
            print(f"  SKIP {name}（不存在）")
            continue
        detected, out = with_tampered(name, mutate)
        status = "DETECTED" if detected else "NOT DETECTED (oracle has no discriminating power!)"
        print(f"  {name}: {status}")
        if not detected:
            all_ok = False
            print("    " + "\n    ".join(l for l in out.splitlines()[:6]))

    # 还原后必须恢复绿
    code, out = run_oracle()
    restored = code == 0
    print("[restored] " + ("green" if restored else "RED"))

    ok = all_ok and restored
    print("=" * 60)
    print("oracle discriminating-power selftest: " + ("PASS" if ok else "FAIL"))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
