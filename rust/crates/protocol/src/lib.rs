//! PolyDB Protocol Types
//!
//! 手工维护的线路类型，唯一真相源是 `spec/schemas/*.json`（非代码生成——
//! `tools/genprotocol` 只生成 TS 与 ops 表）。一致性由 `go run polydb.dev/tools/genprotocol check`
//! 门禁校验：本 crate 与 Go 侧 `pkg/protocol` 若与 spec 漂移即 CI 失败。

pub mod common;
pub mod connection;
pub mod error;
pub mod metadata;
pub mod query;
pub mod redis;
pub mod schema_ddl;
pub mod schema_diff;
pub mod transaction;
pub mod ws;

pub use common::*;
pub use connection::*;
pub use error::*;
pub use metadata::*;
pub use query::*;
pub use schema_ddl::*;
pub use schema_diff::*;
pub use transaction::*;
pub use ws::*;
