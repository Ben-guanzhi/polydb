//! 结构比对（Compare & Sync E2）的线路 DTO。
//!
//! 与 `polydb-schema-diff` 内部的计划类型是**两套东西**：那边是纯函数的计算结果，
//! 这边是对外契约。转换在 app-core 完成，protocol 层不做 IO、也不依赖 schema-diff。

use serde::{Deserialize, Serialize};

use crate::common::ConnectionId;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SchemaDiffOp {
    CreateTable,
    DropTable,
    AddColumn,
    DropColumn,
    AlterColumn,
    AddIndex,
    DropIndex,
    AddForeignKey,
    DropForeignKey,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum DiffColumnFlag {
    Type,
    Nullability,
    Default,
    AutoIncrement,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct SchemaDiffEntry {
    pub id: String,
    pub op: SchemaDiffOp,
    pub phase: u8,
    pub table: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub object: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub from: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub to: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub flags: Vec<DiffColumnFlag>,
    pub destructive: bool,
    pub selected: bool,
    pub reason: String,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct SchemaDiffPlan {
    pub entries: Vec<SchemaDiffEntry>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SchemaDiffTarget {
    pub connection_id: ConnectionId,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub schema: Option<String>,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct SchemaDiffOptions {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ignore_defaults: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub select_destructive: Option<bool>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SchemaDiffRequest {
    pub source: SchemaDiffTarget,
    pub target: SchemaDiffTarget,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub options: Option<SchemaDiffOptions>,
}
