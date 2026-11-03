pub mod blobsink;
pub mod browse;
pub mod connection;
pub mod detect;
pub mod driver;
pub mod scan;
pub mod tx;

pub use blobsink::{blob_value, clear_blob_sink, set_blob_sink, BlobSink};
pub use browse::{
    browse_rows_limits, browse_rows_validate, build_rows_count_query, build_rows_query,
    build_rows_where, count_result_to_u64, escape_like_text, glob_quote_text, like_pattern_to_glob,
    rows_result_to_browse_page, BrowseDialect, BrowseFlavor, RegexSupport, DIALECT_DEFAULT,
    DIALECT_MSSQL, DIALECT_MYSQL, DIALECT_ORACLE, DIALECT_SQLITE,
};
pub use connection::*;
pub use detect::{
    detect_statement_type, leading_keyword, mutates_data, split_statements, strip_comments,
    strip_leading_comments,
};
pub use driver::*;
pub use scan::{profiles_for, scan_tokens, LexProfile, Token, TokenKind};
pub use tx::*;

/// 行上限（behavior.md §5"最大返回行 10000"的驱动侧常数）：所有 SQL 驱动的
/// execute 物化循环数到此即停并置 truncated——结果集大小与表大小解耦，
/// server 层的 apply_max_rows（同值）退化为兜底而语义不变。放 db-core 是
/// 因为五驱动要共享同一数字：单驱动漂移会造成截断不一致。
pub const DRIVER_MAX_ROWS: usize = 10_000;

/// 驱动层行上限（函数形式，调用点读起来与局部常量一致）。
pub fn row_cap() -> usize {
    DRIVER_MAX_ROWS
}
