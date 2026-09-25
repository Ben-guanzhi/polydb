//! 结构同步语句生成（Compare & Sync E3）的线路 DTO。
//!
//! 与 `polydb-schema-diff` 内部生成器的结果类型是**两套东西**：那边是纯函数的输出，
//! 这边是对外契约。转换在 app-core 完成，protocol 层不做 IO、也不依赖 schema-diff。
//!
//! 空数组的线路表示是契约的一部分：`statements` / `rollback` / `entry_ids` 为空时必须
//! 序列化成 `[]`（Rust 的 `Vec::new()` 天然如此），不能变成 `null`。

use serde::{Deserialize, Serialize};

use crate::schema_diff::{SchemaDiffOptions, SchemaDiffTarget};

/// `POST /api/schema/ddl` 的请求体：连接 + 勾选条目 id，服务端重算比对（behavior.md §15.1）。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SchemaDdlRequest {
    /// 基准侧（当前库）。
    pub source: SchemaDiffTarget,
    /// 期望侧。
    pub target: SchemaDiffTarget,
    /// 必须与比对时同一份选项：回滚方向按它镜像重算。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub options: Option<SchemaDiffOptions>,
    /// 勾选的变更条目 id（须来自同一次比对计划）。空数组产出空脚本。
    pub selected: Vec<String>,
}

/// 一条可执行语句。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct DdlStatement {
    /// 该语句实现的变更条目 id：直改恰好 1 个；重建的一组承载该表被吸收的全部条目；
    /// PRAGMA 包裹条为空数组。
    pub entry_ids: Vec<String>,
    /// 已带结尾分号。
    pub sql: String,
    /// 执行相位；PRAGMA 首条 0、末条 255。
    pub phase: u8,
    /// 语句作用的表名（原样大小写）；PRAGMA 包裹条为空串，字段本身仍在。
    pub table: String,
    /// 人读告警；双实现逐字一致，无告警时省略该字段。
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub warnings: Vec<String>,
    /// 承载条目里任一条不可逆 ⇒ 整条语句不可逆。
    pub non_reversible: bool,
}

/// 正向脚本 + 回滚脚本。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct DdlScript {
    pub statements: Vec<DdlStatement>,
    /// 回滚脚本是**建议脚本**：其中不可逆语句执行后无法再回退。
    pub rollback: Vec<DdlStatement>,
    /// true ⇒ 首尾两条 PRAGMA 必须留在事务外执行（事务内是 no-op）。
    pub pragmas_outside_tx: bool,
    /// 该数据库种类的 DDL 是否可进事务（SQLite 实测 true）。
    pub ddl_in_tx: bool,
}
