#!/usr/bin/env python3
"""polydb 结构比对语料的**共享规则集**（Item 9 / Item 10，2026-11-10）。

本模块只装**规则**，不装任何一端的实现：相位码表、类型/默认值的归一
规则、标识符匹配、条目 id 构成、选择策略。`select_vectors_oracle.py` 与
`diff_vectors_oracle.py` 都从这里取，**规则只此一份**。

> 为什么要共享：此前 `select_vectors_oracle.py` 自带一份相位表，漏了
> `drop_foreign_key(10)`——因为 select 语料恰好没覆盖"删外键"，18/18
> 全绿掩盖了这个缺口，而 diff 语料一跑就暴露。把相位表复制到第二个文件
> 只会重演同一类错误，故单列此模块。

## 规则的出处

每条规则都能在语料的 `note` 里找到明文（不是猜的）：

- 相位码：`schema-select-vectors` 各 note 给出的阶段顺序；
- 类型归一：`01_identical`（大小写/空白）、`04_mysql_type_width`
  （MySQL 显示宽度与 zerofill 属噪声）、`15_cross_kind_error`
  （跨种类**不猜**等价类型，直接报错）；
- 默认值归一：`06_default_normalize`（外层括号、尾部 `::type` 投射、
  `NULL` 字面量、引号外大小写都不产差异）；
- 索引：`08_index_rebuild`（列顺序与唯一性变化 ⇒ 成对 drop+add）、
  `09_pk_change`（主键按 `primary` 标志匹配，不比索引名）；
- 外键：`10_fk_change`（按名字匹配；引用表/列/ON DELETE/ON UPDATE 变化
  ⇒ 成对 drop+add）；
- 标识符：`11_case_insensitive_match`（按小写匹配，不误判删+建）；
- 视图：`12_views_skipped`（view / materialized_view 不进比对）；
- 条目 id：全部语料（`<table>/<object>/<op>`，**全小写**；表级变化对象段为空）。
"""

import json
import os
import re

# ──────────────────────────────────────────────────────────── 相位码
#
# **相位/破坏性/逆 op 一律从契约读**：`spec/schemas/schemadiff-ops.json` 是
# AGENTS.md §6 声明的 op 表唯一真相源，`genprotocol ops` 由它生成两端的
# `ops_gen.go` / `ops_gen.rs`。
#
# 为什么不在本文件再手写一份：本模块早先手写过一份，**漏了
# `drop_foreign_key(10)`**——select 语料恰好没覆盖该 op，18/18 全绿把缺口
# 遮了很久（PD-29）。读契约既去掉重复，也保证"契约改了 oracle 会跟着走"。
#
# 独立性不受影响：读的是**契约**（声明的真相源），不是任一端的实现。
# 至于"契约本身写错了怎么办"——那不是 oracle 的职责，由 spec 评审与
# `genprotocol ops` 门禁负责；oracle 只保证"语料 == 契约 + 规则文本"。
_SPEC_REL = os.path.join("spec", "schemas", "schemadiff-ops.json")


def _spec_path():
    here = os.path.dirname(os.path.abspath(__file__))
    root = os.path.abspath(os.path.join(here, "..", ".."))
    return os.path.join(root, _SPEC_REL)


# 契约里必须存在的 op（只列 id，值从契约读）。增删 op 时这里会先红，
# 逼着同步——这正是"漏一个 op"最可能的回归形态。
EXPECTED_OPS = {
    "create_table",
    "drop_table",
    "add_column",
    "drop_column",
    "alter_column",
    "add_index",
    "drop_index",
    "add_foreign_key",
    "drop_foreign_key",
}


def _load_spec():
    path = _spec_path()
    with open(path, "r", encoding="utf-8") as fh:
        spec = json.load(fh)
    ops = {o["id"]: o for o in spec["ops"]}
    missing = EXPECTED_OPS - set(ops)
    extra = set(ops) - EXPECTED_OPS
    if missing or extra:
        raise SystemExit(
            f"schemadiff-ops.json 的 op 集合与 oracle 期望不符："
            f"缺 {sorted(missing)} 多 {sorted(extra)}（改契约时请同步 EXPECTED_OPS）"
        )
    return ops, set(spec.get("ddl_dialects") or [])


_OPS, DDL_DIALECTS = _load_spec()

#: op → 执行相位。
PHASE = {op: o["phase"] for op, o in _OPS.items()}

#: op → 逆 op（回滚方向用，`d03` 的 note："回滚按互换后的同一套规则"）。
INVERSE_OP = {op: o["inverse"] for op, o in _OPS.items()}

#: 破坏性 op 集合（决定默认是否勾选）。
DESTRUCTIVE_OPS = {op for op, o in _OPS.items() if o["destructive"]}

# alter_column 的 flags 规范顺序（`05` / `06` / `13` 的 note：类型、可空性、
# 默认值、其它）。auto_increment 属"其它"，排最后。
FLAG_ORDER = ["type", "nullability", "default", "auto_increment"]

ERR_NOT_SUPPORTED = "POLYDB_ERR_NOT_SUPPORTED"
ERR_INVALID_PARAM = "POLYDB_ERR_INVALID_PARAM"

# 不参与结构比对的表类型（`12_views_skipped`）。
SKIPPED_TABLE_TYPES = {"view", "materialized_view"}


# ──────────────────────────────────────────────────────────── 归一
INTEGER_FAMILY = (
    "int",
    "integer",
    "bigint",
    "smallint",
    "tinyint",
    "mediumint",
    "int1",
    "int2",
    "int4",
    "int8",
)


def norm_type(dt, kind=""):
    """类型名归一：大小写、空白与 MySQL 的 zerofill 属噪声；
    **只有 MySQL 的整数族显示宽度**才是噪声，其余方言的括号是真实类型。

    规则来源：
      - `01_identical`：`integer` vs `INTEGER`、
        `character varying(100)` vs `CHARACTER   VARYING ( 100 )`；
      - `04_mysql_type_width`（mysql）：`bigint(20)` vs `BIGINT`、
        `decimal(10, 2)` vs `DECIMAL(10,2)`、
        `INT(11) UNSIGNED ZEROFILL` vs `int unsigned`；
      - `d06_alter_column_type_rebuild`（sqlite）：`varchar(20)` → `varchar(30)`
        **是**变更。

    ⇒ 括号里的长度/精度只有 **MySQL 的整数族**才丢（那是 8.0.17 之前的历史
    遗留显示宽度，不影响取值）；`varchar(20)`/`decimal(10,2)` 的括号是真实
    类型的一部分，任何方言都必须保留。

    **方言必须传进来**（`kind`）：MySQL 丢宽度、SQLite 不丢——
    两端实现本来就是方言感知的（Go `TestNormalizeTypeMySQLWidthAndZerofill`
    里就有 `NormalizeType(sqlite, "int(11)") == "int(11)"` 这条断言）。
    早先本模块是方言无关的，会对 SQLite 的 `int(11)` 也丢宽度——**与两端
    不一致的潜在偏差**（PD-32，2026-11-10）。现有语料恰好只在 MySQL 上出现
    整数带括号（`04_mysql_type_width`），所以一直没暴露。

    > PD-30 修正记录：更早的实现在**所有**类型上都砍括号，于是
    > `varchar(20)`→`varchar(30)` 被判"无变化"、漏掉 alter_column；
    > diff 语料没有"改字符长度"的用例，17/17 全绿把 bug 遮住，
    > 是 ddl 的 `d06` 逼出来的。

    **不做别名等价猜测**（如 `integer` ↔ `int`）：`15_cross_kind_error` 的
    note 明写"不猜等价类型"——那种情况语料的处置是**报错**（跨种类），
    而不是把两个类型判等。
    """
    if dt is None:
        return ""
    s = "".join(str(dt).split()).lower()
    # MySQL 的 zerofill 是显示修饰，不参与比较；unsigned 是真实语义，保留。
    s = s.replace("zerofill", "")
    i = s.find("(")
    if i < 0:
        return s
    base = s[:i]
    if kind == "mysql" and base in INTEGER_FAMILY:
        # 只有 MySQL 的整数 (n) 是显示宽度 ⇒ 丢掉。
        return re.sub(r"\([^)]*\)", "", s)
    # 其它情况：括号是类型的一部分（长度/精度）⇒ 保留。
    return s


def norm_default(v):
    """默认值归一：外层括号、尾部 `::type` 投射、`NULL` 字面量、引号外大小写。

    规则来源 `06_default_normalize`：
      - `'0'::character varying` 与 `(('0'))` 视为相同；
      - `NULL` 与"没有默认值"视为相同；
      - 引号**外**的大小写是噪声，引号**内**的保留（`'A'` ≠ `'a'`）。

    返回 None 表示"无默认值"。
    """
    if v is None:
        return None
    s = str(v).strip()
    if not s:
        return None
    # 尾部 ::type 投射（可连续多个）。
    s = re.sub(r"::[A-Za-z_][\w ]*(\([^)]*\))?\s*$", "", s).strip()
    # 外层括号：只有当整串被一对括号完整包住时才剥（`(('0'))` → `'0'`）。
    while len(s) >= 2 and s[0] == "(" and s[-1] == ")" and _balanced_inner(s[1:-1]):
        s = s[1:-1].strip()
    if not s:
        return None
    if s.upper() == "NULL":
        return None
    # 引号外折叠大小写，引号内保留。
    out = []
    i = 0
    while i < len(s):
        if s[i] == "'":
            j = i + 1
            while j < len(s):
                if s[j] == "'" and (j + 1 >= len(s) or s[j + 1] != "'"):
                    break
                j += 2 if s[j] == "'" else 1
            out.append(s[i : j + 1])
            i = j + 1
        else:
            out.append(s[i].lower())
            i += 1
    return "".join(out)


def _balanced_inner(s):
    """判断 s 内部括号是否配平（用于安全地剥外层括号）。"""
    depth = 0
    for ch in s:
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
            if depth < 0:
                return False
    return depth == 0


# ──────────────────────────────────────────────────────────── 快照
def real_tables(snapshot):
    """只取真表（`12_views_skipped`：view / materialized_view 不进比对）。"""
    out = {}
    for t in snapshot.get("tables", []) or []:
        if t.get("type") in SKIPPED_TABLE_TYPES:
            continue
        out[t["name"].lower()] = t
    return out


def columns_of(table):
    return {c["name"].lower(): c for c in table.get("columns", []) or []}


def indexes_of(table):
    return {i["name"].lower(): i for i in table.get("indexes", []) or []}


def fks_of(table):
    return {f["name"].lower(): f for f in table.get("foreign_keys", []) or []}


def index_sig(idx):
    """索引的可比签名：唯一性 + 列顺序（`08_index_rebuild`）。"""
    cols = [c["name"].lower() for c in idx.get("columns", []) or []]
    return (bool(idx.get("unique")), tuple(cols))


def fk_sig(fk):
    """外键的可比签名（`10_fk_change` + §14.6.1 的「列」分句）：

    **本侧列** + 引用 schema/表 + 引用列 + ON DELETE/ON UPDATE。

    > PD-36（2026-11-11）：早先的实现漏了**本侧列**——Go 的
    > `diffForeignKeys`（schemadiff.go:442）有
    > `joinLower(s.Columns) != joinLower(t.Columns) ⇒ 「列 a → b」分句`
    > 的分支，oracle 没跟上。diff 语料里 FK 的本侧列从未变过
    > （10 号改的是引用侧四项），17/17 全绿把缺口遮住；写 d19 语料时
    > 才暴露（又一个"语料覆盖不足 ⇒ oracle 规则 bug 隐身"）。
    """
    return (
        tuple(c.lower() for c in fk.get("columns", []) or []),
        (fk.get("referenced_schema") or "").lower(),
        (fk.get("referenced_table") or "").lower(),
        tuple(c.lower() for c in fk.get("referenced_columns", []) or []),
        (fk.get("on_delete") or "").lower(),
        (fk.get("on_update") or "").lower(),
    )


# ──────────────────────────────────────────────────────────── 条目
def entry_id(table, obj, op):
    """条目 id：`<table>/<object>/<op>`，**全小写**；表级变化对象段为空。"""
    return f"{table.lower()}/{(obj or '').lower()}/{op}"


# ──────────────────────────────────────────────────── reason 文案（§14.6.1）
#
# 文案格式是**契约**（spec/behavior.md §14.6.1），故这些词汇表也算规则。
# 加进来的直接收益：diff 语料的 `reason` 不再需要"抄一端实现"，可独立推导。
def null_word(nullable):
    """可空性词：`NULL` / `NOT NULL`。"""
    return "NULL" if nullable else "NOT NULL"


def yes_word(b):
    """布尔词：`是` / `否`。"""
    return "是" if b else "否"


def default_word(d):
    """默认值词：None → `无`，否则原文（已归一的默认值）。"""
    return "无" if d is None else d


def action_word(a):
    """FK 动作词：空串 → `无`，否则原文。"""
    return "无" if not (a or "").strip() else a


def cols_lower(names):
    """列名列表：各名转小写后以 `,` 连接（无空格）。"""
    return ",".join(str(n).lower() for n in names or [])


def index_sig_str(idx):
    """索引签名串：列按 (position, 小写名) 升序，转小写后 `,` 连接。"""
    cols = list(idx.get("columns", []) or [])
    cols.sort(key=lambda c: (c.get("position", 0), str(c.get("name", "")).lower()))
    return ",".join(str(c.get("name", "")).lower() for c in cols)


def ref_table(fk):
    """引用表串：`[schema/]table`（都转小写；schema 空则只有表名）。"""
    t = str(fk.get("referenced_table") or "").lower()
    s = str(fk.get("referenced_schema") or "").strip().lower()
    return f"{s}/{t}" if s else t


def index_change_reason(s_idx, t_idx):
    """索引定义变化的文案（成对两条相同，见 §14.6.1）。"""
    parts = []
    sa, ta = index_sig_str(s_idx), index_sig_str(t_idx)
    if sa != ta:
        parts.append(f"索引定义变化：{sa} → {ta}")
    if bool(s_idx.get("unique")) != bool(t_idx.get("unique")):
        parts.append(f"唯一性 {yes_word(bool(s_idx.get('unique')))} → {yes_word(bool(t_idx.get('unique')))}")
    return "；".join(parts)


def fk_change_reason(s_fk, t_fk):
    """外键定义变化的文案（成对两条相同，见 §14.6.1）。"""
    parts = []
    sc, tc = cols_lower(s_fk.get("columns")), cols_lower(t_fk.get("columns"))
    if sc != tc:
        parts.append(f"列 {sc} → {tc}")
    sr, tr = ref_table(s_fk), ref_table(t_fk)
    if sr != tr:
        parts.append(f"引用表 {sr} → {tr}")
    srcols = cols_lower(s_fk.get("referenced_columns"))
    trcols = cols_lower(t_fk.get("referenced_columns"))
    if srcols != trcols:
        parts.append(f"引用列 {srcols} → {trcols}")
    if (s_fk.get("on_delete") or "") != (t_fk.get("on_delete") or ""):
        parts.append(f"ON DELETE {action_word(s_fk.get('on_delete'))} → {action_word(t_fk.get('on_delete'))}")
    if (s_fk.get("on_update") or "") != (t_fk.get("on_update") or ""):
        parts.append(f"ON UPDATE {action_word(s_fk.get('on_update'))} → {action_word(t_fk.get('on_update'))}")
    return "；".join(parts)


def alter_reason(flags, sc, tc, kind=""):
    """`alter_column` 的文案：按 flags 规范顺序拼接发生变化的分句。"""
    parts = []
    for f in FLAG_ORDER:
        if f not in flags:
            continue
        if f == "type":
            parts.append(
                f"类型 {norm_type(sc.get('data_type'), kind)} → {norm_type(tc.get('data_type'), kind)}"
            )
        elif f == "nullability":
            parts.append(
                f"可空性 {null_word(bool(sc.get('nullable')))} → {null_word(bool(tc.get('nullable')))}"
            )
        elif f == "default":
            parts.append(
                "默认值 "
                f"{default_word(norm_default(sc.get('default_value')))} → "
                f"{default_word(norm_default(tc.get('default_value')))}"
            )
        elif f == "auto_increment":
            parts.append(
                f"自增 {yes_word(bool(sc.get('is_auto_increment')))} → "
                f"{yes_word(bool(tc.get('is_auto_increment')))}"
            )
    return "；".join(parts)


#: 各 op 的**简单**文案（对象仅在一侧时用，见 §14.6.1 表）。
SIMPLE_REASON = {
    "create_table": "表中缺失，需新建",
    "drop_table": "表中多余，需删除",
    "add_column": "列缺失，需新增",
    "drop_column": "列多余，需删除",
    "add_index": "索引缺失，需新增",
    "drop_index": "索引多余，需删除",
    "add_foreign_key": "外键缺失，需新增",
    "drop_foreign_key": "外键多余，需删除",
}

#: 主键列变化时成对产出的文案（两方向同一条）。
PK_REASON = "主键列变化，需重建"


# ──────────────────────────────────────────────────── DDL 警告（§15.x）
#
# 全部警告的**触发条件**都由语料 note 与实现口径给定（G1/G2/G4/G11/G13/G16
# 取证），故是可推导量。文案逐字，见 spec/behavior.md §15.8。
W_TRIGGERS = "重建表会丢失该表上的触发器（快照不含触发器）"
W_GENERATED = "生成列会退化为普通列（快照不标注生成列）"
W_CHECK = "CHECK 约束会丢失（快照不含 CHECK 约束）"
W_AUTOINCREMENT = "主键整数列的 AUTOINCREMENT 关键字无法从快照还原"
W_CREATE_TABLE = "新建表只按快照里的列定义生成，不含 CHECK 约束、生成列与触发器"
W_DROP_COLUMN_CHECK = "该列若被 CHECK 约束引用，删除后约束会一并消失（快照不可见）"
W_ADD_COLUMN_LIMITS = (
    "SQLite 的 ADD COLUMN 只接受常量默认值，非空表上加 NOT NULL 且无默认值的列也会报错"
)
W_REBUILD_NEW_NOT_NULL = (
    "重建只复制两侧同名列，新增的 NOT NULL 列若无默认值会让数据搬运 INSERT 报错"
)
W_FK_CYCLE_PREFIX = "外键依赖成环"

#: 名单上限：再多只报数 + 前 5 个 + 「等」。
SCOPE_MAX = 5


def ordered_columns(table):
    """按 ordinal_position 升序的列（同序数按名字稳定）。"""
    cols = list(table.get("columns", []) or [])
    return sorted(cols, key=lambda c: (c.get("ordinal_position", 0), str(c.get("name", "")).lower()))


def has_integer_pk(kind, table):
    """单列 INTEGER 主键（SQLite 里是 rowid 别名；AUTOINCREMENT 不在快照里）。"""
    pk = [c for c in table.get("columns", []) or [] if c.get("is_primary_key")]
    # 主键也可能来自 primary 索引；两者取并集更稳。
    for idx in table.get("indexes", []) or []:
        if idx.get("primary"):
            pk = pk or []
    if len(pk) != 1:
        return False
    return norm_type(pk[0].get("data_type"), kind) == "integer"


def literal_default(raw):
    """快照原样的默认值（只折叠空白）；空串表示「无默认值」，`NULL` 亦同。"""
    if raw is None:
        return ""
    d = " ".join(str(raw).split())
    if d == "" or d.lower() == "null":
        return ""
    return d


def _is_hex_digit(ch):
    return ch.isdigit() or ("a" <= ch <= "f") or ("A" <= ch <= "F")


def _is_number(s):
    body = s[1:] if s[:1] in ("-", "+") else s
    if not body:
        return False
    digits = dot = exp = False
    i = 0
    while i < len(body):
        ch = body[i]
        if ch.isdigit():
            digits = True
        elif ch == "." and not dot and not exp:
            dot = True
        elif ch in "eE" and digits and not exp:
            exp = True
            if i + 1 < len(body) and body[i + 1] in "+-":
                i += 1
        else:
            return False
        i += 1
    return digits


def is_constant_default(raw):
    """SQLite `ADD COLUMN` 认可的常量默认值（数字 / 单引号串 / blob / NULL）。"""
    d = literal_default(raw)
    if d == "":
        return True  # 无默认值即 NULL，本身是常量
    if d[0] == "'":
        i = 1
        while i < len(d):
            if d[i] != "'":
                i += 1
                continue
            if i + 1 < len(d) and d[i + 1] == "'":
                i += 2
                continue
            return i + 1 == len(d)
        return False
    if d[0] in "xX" and len(d) > 1 and d[1] == "'":
        if d[-1] != "'" or len(d) < 2:
            return False
        return all(_is_hex_digit(c) for c in d[2:-1])
    return _is_number(d)


def _scope(names, unit, lower_keys=False):
    """渲染「N 个{unit}（a, b 等）」。

    名单排序：按**小写键**升序；展示保留原样大小写；同名（小写相同）只留
    排序后第一个；超过 `SCOPE_MAX` 个时只列前 5 个并在末尾加 ` 等`，但**计数
    报全量**（`d26` 的 note）。
    """
    keyed = [(str(n).lower(), str(n)) for n in names]
    keyed.sort(key=lambda kv: kv[0])  # 稳定排序：同键保留原顺序
    seen = set()
    uniq = []
    for k, disp in keyed:
        if k in seen:
            continue
        seen.add(k)
        uniq.append(disp if not lower_keys else k)
    head = ", ".join(uniq[:SCOPE_MAX])
    if len(uniq) > SCOPE_MAX:
        head += " 等"
    return f"{len(uniq)} 个{unit}（{head}）"


def view_scope(names):
    """视图名单串（展示保留原样大小写）。"""
    return _scope(names, "视图")


def table_scope(names):
    """表名单串（**报小写键**：依赖图与拓扑都建在小写键上）。"""
    return _scope(names, "表", lower_keys=True)


def w_view_drop(views):
    """删表的视图警告（G16）。"""
    return (
        f"本库现有 {view_scope(views)}：DROP TABLE 不检查依赖，语句会成功，"
        "但引用它的视图将失效（快照不含视图定义，判定不了是哪一个）"
    )


def w_view_rebuild(views):
    """重建的视图警告（G13：失败点在 ALTER TABLE … RENAME）。"""
    return (
        f"本库现有 {view_scope(views)}：重建经 RENAME，而 SQLite 改名时会重解析"
        "依赖该表的视图 ⇒ 有视图引用它就整条重建报错"
    )


def w_fk_cycle(names):
    """新建表之间的外键环警告。"""
    return (
        f"{W_FK_CYCLE_PREFIX}：{table_scope(names)}互相引用，"
        "建表顺序无法保证父表在前（SQLite 到写入时才报错）"
    )


def index_object(idx):
    """索引在条目里的 object 名。

    有名用名；**无名索引**按快照的列合成 `idx(<c1>,<c2>)`
    （`d09_index_add_drop_direct` 的 note："无名索引按快照合成可执行名
    `polydb_idx_<表>_<列>`，与 E1 的 object 同规则"——即 object 段用
    `idx(<列>)`，而可执行名另由 DDL 侧合成）。
    """
    name = (idx.get("name") or "").strip()
    if name:
        return name
    cols = ",".join(c["name"] for c in idx.get("columns", []) or [])
    return f"idx({cols})"


def fk_object(fk):
    """外键在条目里的 object 名。

    有名用名；**无名外键**（真实 SQLite 快照恒无名，见 `d12` 的 note）
    退化为 `fk(<列>)`。
    """
    name = (fk.get("name") or "").strip()
    if name:
        return name
    return "fk(" + ",".join(fk.get("columns", []) or []) + ")"


def policy_selected(destructive, select_destructive):
    """默认选择策略：破坏性变更默认不勾（除非 select_destructive），其余默认勾。"""
    return (not destructive) or bool(select_destructive)


def is_destructive(op):
    """破坏性 op（决定默认是否勾选）。表来自契约，不在此手写。"""
    return op in DESTRUCTIVE_OPS


def alter_flags(sc, tc, ignore_defaults, kind=""):
    """alter_column 的 flags（规范顺序见 FLAG_ORDER）。

    `ignore_defaults=true` 时只有默认值的变化不产条目
    （`07_ignore_defaults`），故此处直接跳过 default 标记；
    若最终 flags 为空，调用方不应产出条目。

    `kind` 传给 `norm_type`：MySQL 的整数显示宽度是噪声，别的方言不是。
    """
    flags = []
    if norm_type(sc.get("data_type"), kind) != norm_type(tc.get("data_type"), kind):
        flags.append("type")
    if bool(sc.get("nullable")) != bool(tc.get("nullable")):
        flags.append("nullability")
    if not ignore_defaults and norm_default(sc.get("default_value")) != norm_default(
        tc.get("default_value")
    ):
        flags.append("default")
    if bool(sc.get("is_auto_increment")) != bool(tc.get("is_auto_increment")):
        flags.append("auto_increment")
    return [f for f in FLAG_ORDER if f in flags]


# ──────────────────────────────────────────────────────────── 前置校验
def precheck(source, target):
    """返回 (error_code, error_message)；无错则 (None, None)。

    `15_cross_kind_error`：跨种类比对不支持（不猜等价类型）；
    `16_redis_error`：Redis 无模式概念。
    """
    sk, tk = source.get("kind"), target.get("kind")
    if sk == "redis" or tk == "redis":
        return ERR_NOT_SUPPORTED, "Redis 无模式概念，不参与结构比对"
    if sk != tk:
        return ERR_NOT_SUPPORTED, f"跨种类比对未支持：{sk} vs {tk}（v1 只比同一种数据库）"
    return None, None