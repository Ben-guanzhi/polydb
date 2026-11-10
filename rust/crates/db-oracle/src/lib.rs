//! polydb-db-oracle：基于 oracle crate（ODPI-C）的驱动实现（对应 Go 侧 dboracle）。
//! oracle crate 为阻塞式 API（ODPI-C）：执行与事务路径（execute / execute_in_tx /
//! begin_tx / commit / rollback）全部经 spawn_blocking 移出 runtime worker。
//! OracleTxHandle 是单元结构体、不持有连接引用，所以阻塞闭包只需要
//! Arc<Mutex<Connection>>，`&mut` 句柄留在 async 侧、从不进闭包。

use std::any::Any;
use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use oracle::sql_type::ToSql;
use oracle::{Connection, SqlValue};
use parking_lot::Mutex;

use polydb_core::error_classify::driver_error;
use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyInfo, IndexColumn, IndexInfo,
    IndexType, IsolationLevel, QueryResult, ResultColumn, SchemaInfo, StatementType, TableInfo,
    TableType, Value,
};
use polydb_db_core::{has_returning_clause, DatabaseDriver, SqlDriver, TxMode};

/// 把 oracle crate 的 [`OracleType`] 映射成 **Oracle SQL 类型名**（PD-28，
/// 2026-11-10）。
///
/// ## 为什么不再用 `format!("{:?}", …)`
///
/// spec/schemas/query.json 对 `ResultColumn.type` 的定义是
/// **"Database-specific type name"**。`Debug` 形态（`Varchar2(50)`、
/// `Number(10, 0)`）既不是类型名（带上了长度/精度），大小写也与其它引擎
/// 不一致（PG 报 `INT4`、MySQL 报 `VARCHAR`、MSSQL 报 `NVARCHAR`，都是
/// 大写的裸类型名）。
///
/// 去掉长度/精度与其它引擎一致：MySQL 的 `DECIMAL(10,2)` 也只报 `DECIMAL`。
///
/// ## 分辨率上限（与 Go 侧对齐的取舍）
///
/// oracle crate 能区分 `Varchar2`/`NVarchar2`/`Char`/`NChar` 四个变体
/// （`sql_type/oracle_type.rs:205`），但 Go 侧经 `database/sql.ColumnType`
/// 只能拿到 go-ora 的 TNS 码族（`CHAR` 族含 CHAR+NCHAR、`NCHAR` 名其实是
/// VARCHAR 族），**拿不到字符集形态**。故两侧统一归到**族名**：
/// 字符型 ⇒ `VARCHAR2` / `CHAR`，`NCLOB` 亦归 `CLOB`。
/// 这是两端可达成一致的最大分辨率，不是哪一侧的缺陷。
///
/// 未识别类型在 **Go 侧**回落到 `"OTHER"`（那边拿到的是字符串，什么都可能有）；
/// **Rust 侧不需要这个回落**——`OracleType` 是闭合枚举，本 match 已穷尽所有
/// 变体（故没有 `_` 臂；clippy 的 `unreachable_pattern` 会挡住多余分支）。
/// 将来 crate 新增变体时，这里会**编译失败**，逼着补映射，而不是静默漂移。
/// 详见 Go 侧 `pkg/dboracle/typename.go` 的文件头说明。
fn oracle_type_name(t: &oracle::sql_type::OracleType) -> &'static str {
    use oracle::sql_type::OracleType as T;
    match t {
        // 字符族（族名，不含长度/字符集；见上「分辨率上限」）。
        T::Varchar2(_) | T::NVarchar2(_) => "VARCHAR2",
        T::Char(_) | T::NChar(_) => "CHAR",

        // 数值。Int64/UInt64 在 Oracle 里由 NUMBER 承载。
        T::Number(_, _) | T::Int64 | T::UInt64 => "NUMBER",
        T::Float(_) => "FLOAT",

        // 日期时间。
        T::Date => "DATE",
        T::Timestamp(_) => "TIMESTAMP",
        T::TimestampTZ(_) => "TIMESTAMP WITH TIME ZONE",
        T::TimestampLTZ(_) => "TIMESTAMP WITH LOCAL TIME ZONE",
        T::IntervalYM(_) => "INTERVAL YEAR TO MONTH",
        T::IntervalDS(_, _) => "INTERVAL DAY TO SECOND",

        // 二进制与大对象。NCLOB 归 CLOB（Go 侧无独立变体）。
        T::Raw(_) => "RAW",
        T::Long => "LONG",
        T::LongRaw => "LONG RAW",
        T::CLOB | T::NCLOB => "CLOB",
        T::BLOB => "BLOB",
        T::BFILE => "BFILE",

        // 其它。
        T::Rowid => "ROWID",
        T::BinaryFloat => "BINARY_FLOAT",
        T::BinaryDouble => "BINARY_DOUBLE",
        T::Boolean => "BOOLEAN",
        T::Json => "JSON",
        T::Xml => "XMLTYPE",
        T::RefCursor => "REF CURSOR",
        T::Object(_) => "OBJECT",
    }
}

/// Oracle 自带维护 schema，默认不展示（对应 Go 侧 maintainedSchemas）。
const MAINTAINED: &[&str] = &[
    "SYS",
    "SYSTEM",
    "OUTLN",
    "DBSNMP",
    "APPQOSSYS",
    "CTXSYS",
    "MDSYS",
    "ORDSYS",
    "ORDDATA",
    "ORDPLUGINS",
    "SI_INFORMTN_SCHEMA",
    "WMSYS",
    "XDB",
    "XS$NULL",
    "DVSYS",
    "AUDSYS",
    "GSMADMIN_INTERNAL",
    "OJVMSYS",
    "LBACSYS",
];

pub struct OracleConn {
    conn: Arc<Mutex<Connection>>,
    _dsn: String,
}

impl OracleConn {
    /// host:port/service 形式定位服务；password 为可选连接密码
    /// （明文来自 keyring，连接时一次性注入，不落库；与 Go 侧 oracleDSN 对齐）。
    pub fn open(
        host: &str,
        port: u16,
        service: &str,
        username: &str,
        password: &str,
    ) -> CoreResult<Self> {
        let service = if service.is_empty() { "ORCL" } else { service };
        let connect_string = format!("{host}:{port}/{service}");
        let conn = Connection::connect(username, password, &connect_string)
            .map_err(|e| CoreError::Driver(format!("connect oracle: {e}")))?;
        Ok(Self {
            conn: Arc::new(Mutex::new(conn)),
            _dsn: connect_string,
        })
    }

    /// 返回指向 self 的共享 `Arc<Self>`：`oracle::Connection` 不可克隆，
    /// 因此内部包 `Arc<Mutex<...>>`，两个 `Arc<Self>` 共享同一个连接。
    /// 用于 app-core 持有第二个 `Arc<dyn SqlDriver>`，与 `Connection`
    /// 内的驱动共享同一底层连接。
    pub fn clone_arc(&self) -> Arc<Self> {
        Arc::new(Self {
            conn: Arc::clone(&self.conn),
            _dsn: self._dsn.clone(),
        })
    }

    fn to_sql_params(v: &[Value]) -> Vec<Box<dyn ToSql>> {
        v.iter()
            .map(|val| match val {
                Value::Integer(i) => Box::new(*i) as Box<dyn ToSql>,
                Value::U64(u) => {
                    // Oracle NUMBER 可表示 u64，但 oracle crate 的 ToSql 不支持
                    // u64——≤ i64::MAX 绑为 i64，> i64::MAX 绑为 f64。
                    if *u <= i64::MAX as u64 {
                        Box::new(*u as i64) as Box<dyn ToSql>
                    } else {
                        Box::new(*u as f64) as Box<dyn ToSql>
                    }
                }
                Value::Float(f) => Box::new(*f) as Box<dyn ToSql>,
                Value::Bool(b) => Box::new(*b) as Box<dyn ToSql>,
                Value::String(s) => Box::new(s.clone()) as Box<dyn ToSql>,
                // NULL 必须绑成 NULL：oracle crate 对 Option<T: ToSql + ToSqlNull>
                // 的 None 绑定 NULL；此前兜底成 0i64 会把 NULL 过滤值静默变成 0。
                Value::Null => Box::new(Option::<i64>::None) as Box<dyn ToSql>,
                // Array/Object（含 blob_value 编码出的 $blob Object）：序列化为
                // JSON 字符串绑定，与 Go 侧 ValueToDriver 的 json.Marshal 对齐。
                // 此前兜底成 0i64 会把 JSON/blob 列的数据静默写成 0。
                Value::Array(_) | Value::Object(_) => {
                    Box::new(Self::json_text_param(val)) as Box<dyn ToSql>
                }
            })
            .collect()
    }

    /// Array/Object（含 $blob Object）的绑定形态：跨线协议统一为 JSON 文本。
    /// NaN/Inf 先经 polydb_db_core::value_to_json_text 替换为字符串，
    /// 避免 serde_json 静默转 null（P2-3）。
    fn json_text_param(val: &Value) -> String {
        polydb_db_core::value_to_json_text(val)
    }

    fn param_refs(boxes: &[Box<dyn ToSql>]) -> Vec<&dyn ToSql> {
        boxes.iter().map(|b| b.as_ref()).collect()
    }

    /// SqlValue 在 0.6 是结构体：先判空，再按 数值→字符串→原始字节 顺序尝试转换。
    ///
    /// 顺序有讲究：oracle crate 的 NUMBER 原生形态是**文本**（InnerValue::Number
    /// 是 &str，i64/f64 都走 str.parse）。String 必须先于 f64 尝试——否则
    /// NUMBER(38,10) 的精确文本（i64 parse 失败、f64 parse 成功）被 f64 静默
    /// 吞掉精度。go-ora（Go 侧）同样把 NUMBER 交给宿主的是 num.String() 的
    /// 精确文本，这里对齐后两端同值。
    fn or_value(v: &SqlValue) -> Value {
        if v.is_null().unwrap_or(true) {
            return Value::Null;
        }
        if let Ok(i) = v.get::<i64>() {
            return Value::Integer(i);
        }
        // Number 的精确文本先于 f64：见上。
        if let Ok(s) = v.get::<String>() {
            return Value::String(s);
        }
        if let Ok(f) = v.get::<f64>() {
            return Value::Float(f);
        }
        if let Ok(b) = v.get::<bool>() {
            return Value::Bool(b);
        }
        if let Ok(bytes) = v.get::<Vec<u8>>() {
            return polydb_db_core::blob_value(&bytes);
        }
        Value::Null
    }

    /// 语句类型判定（收敛到 db-core 公共版，方言固定为 Oracle）。
    ///
    /// 公共版按 token 类别判型（整词而非前缀），并认 Oracle 的 q'…' 备选引用
    /// （正文里可以含引号，不认它会把后面的写语句吞掉）。
    fn detect_statement_type(sql: &str) -> StatementType {
        polydb_db_core::detect_statement_type(sql, Some(DatabaseKind::Oracle))
    }
}

fn quote_ident(s: &str) -> String {
    format!("\"{}\"", s.replace('"', "\"\""))
}

/// Oracle 事务句柄。oracle crate 无独立 Transaction 对象，事务状态由
/// 连接上的第一条 DML 隐式开启，commit()/rollback() 收尾；本结构仅作
/// 不透明标记。它是**单元结构体**——不持有连接引用，因此 `&mut` 句柄
/// 留在 async 侧、从不进 spawn_blocking 闭包；闭包只需要 Arc<Mutex<Connection>>。
#[derive(Clone, Debug)]
pub struct OracleTxHandle;

/// Oracle 的隔离级别声明是 SET TRANSACTION：必须是新事务的第一条语句
///（本连接是隐式事务模型——begin_tx 后、第一条 DML 前执行即成立）。
/// 旧实现发的是 ALTER SESSION SET TRANSACTION …，两种拼写都不是 Oracle
/// 语法，一直靠"失败回退默认"掩盖。
/// 返回 None 表示该级别无需声明（回退默认 ReadCommitted）。
fn set_transaction_sql(level: IsolationLevel) -> Option<&'static str> {
    match level {
        // Oracle 没有 READ UNCOMMITTED：回退默认 ReadCommitted。
        IsolationLevel::ReadUncommitted => None,
        // Oracle 也没有 REPEATABLE READ：最近的对应物是 SERIALIZABLE
        //（与 Go 侧 go-ora 的映射一致）。
        IsolationLevel::RepeatableRead | IsolationLevel::Serializable => {
            Some("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")
        }
        IsolationLevel::ReadCommitted => None,
    }
}

impl OracleConn {
    pub async fn begin_tx(&self, mode: TxMode) -> CoreResult<OracleTxHandle> {
        if let Some(sql) = set_transaction_sql(mode.isolation_level) {
            // 非默认级别设置失败不视为致命错误：SET TRANSACTION 要求它确实是
            // 新事务的第一条语句，若此前有语句意外开着事务会失败——回退默认。
            let conn = Arc::clone(&self.conn);
            let sql = sql.to_string();
            tokio::task::spawn_blocking(move || {
                let _ = conn.lock().execute(&sql, &[]);
            })
            .await
            .map_err(|e| CoreError::Internal(format!("oracle task join: {e}")))?;
        }
        Ok(OracleTxHandle)
    }

    pub async fn commit(&self, _tx: &mut OracleTxHandle) -> CoreResult<()> {
        // ODPI-C 是阻塞式 API：与 execute / execute_in_tx 同范式，经
        // spawn_blocking 移出 runtime worker，否则慢提交/锁等待会钉死
        // WS 的取消/超时臂所在的那个 worker。
        let conn = Arc::clone(&self.conn);
        tokio::task::spawn_blocking(move || {
            conn.lock()
                .commit()
                .map_err(|e| CoreError::Driver(format!("commit failed: {e}")))
        })
        .await
        .map_err(|e| CoreError::Internal(format!("oracle task join: {e}")))?
    }

    pub async fn rollback(&self, _tx: &mut OracleTxHandle) -> CoreResult<()> {
        let conn = Arc::clone(&self.conn);
        tokio::task::spawn_blocking(move || {
            conn.lock()
                .rollback()
                .map_err(|e| CoreError::Driver(format!("rollback failed: {e}")))
        })
        .await
        .map_err(|e| CoreError::Internal(format!("oracle task join: {e}")))?
    }

    /// 事务内执行一条 SQL。Oracle 隐式事务：连接上的第一条 DML/DDL 即开启
    /// 事务，因此 execute_in_tx 与 execute 完全等价（同一 conn）。
    pub async fn execute_in_tx(
        &self,
        _tx: &mut OracleTxHandle,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        // Same spawn_blocking discipline as execute: the blocking ODPI-C call
        // must not run on a runtime worker, or the WS cancel/timeout arms
        // never get polled. OracleTxHandle is a unit struct — the `&mut`
        // is never read — so the connection Arc is what moves into the
        // closure, exactly as in the non-tx path.
        let conn = Arc::clone(&self.conn);
        let sql = sql.to_string();
        let params = params.to_vec();
        tokio::task::spawn_blocking(move || {
            let start = std::time::Instant::now();
            let stmt_type = Self::detect_statement_type(&sql);
            let boxes = Self::to_sql_params(&params);
            let refs = Self::param_refs(&boxes);

            let guard = conn.lock();
            let mut stmt = guard
                .statement(&sql)
                .build()
                .map_err(|e| db_err(&e, "prepare failed"))?;

            let wants_rows = stmt_type == StatementType::Select
                || stmt_type == StatementType::Other
                || has_returning_clause(&sql);
            if wants_rows {
                let mut rows = stmt.query(&refs).map_err(|e| db_err(&e, "query failed"))?;
                let columns = rows
                    .column_info()
                    .iter()
                    .map(|c| ResultColumn {
                        name: c.name().to_string(),
                        table: None,
                        data_type: format!("{:?}", c.oracle_type()),
                        generic_type: None,
                        nullable: Some(true),
                    })
                    .collect();
                let mut data = Vec::new();
                let mut truncated = false;
                // 物化上限（polydb_db_core::row_cap，behavior.md §5）：游标数到
                // 上限即停——大表不再在 server 层截断之前吃满内存。
                while let Some(row) = rows
                    .next()
                    .transpose()
                    .map_err(|e| db_err(&e, "row failed"))?
                {
                    if data.len() >= polydb_db_core::row_cap() {
                        truncated = true;
                        break;
                    }
                    data.push(row.sql_values().iter().map(Self::or_value).collect());
                }
                Ok(QueryResult {
                    columns,
                    rows: data,
                    affected_rows: 0,
                    execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                    truncated,
                    total_rows: None,
                    has_more: truncated,
                    statement_type: Some(stmt_type),
                })
            } else {
                stmt.execute(&refs)
                    .map_err(|e| db_err(&e, "execute failed"))?;
                let affected = stmt
                    .row_count()
                    .map_err(|e| db_err(&e, "row_count failed"))?;
                Ok(QueryResult {
                    columns: vec![],
                    rows: vec![],
                    affected_rows: affected,
                    execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                    truncated: false,
                    total_rows: None,
                    has_more: false,
                    statement_type: Some(stmt_type),
                })
            }
        })
        .await
        .map_err(|e| CoreError::Internal(format!("oracle task join: {e}")))?
    }
}

#[async_trait]
impl DatabaseDriver for OracleConn {
    fn kind(&self) -> DatabaseKind {
        DatabaseKind::Oracle
    }

    async fn ping(&self) -> CoreResult<()> {
        let conn = self.conn.lock();
        conn.query_as::<i64>("SELECT 1 FROM dual", &[])
            .map_err(|e| CoreError::Driver(format!("ping failed: {e}")))?;
        Ok(())
    }

    async fn close(&self) -> CoreResult<()> {
        let conn = self.conn.lock();
        conn.close()
            .map_err(|e| CoreError::Driver(format!("close: {e}")))?;
        Ok(())
    }

    fn as_sql(&self) -> Option<&dyn SqlDriver> {
        Some(self)
    }
}

#[async_trait]
impl SqlDriver for OracleConn {
    async fn browse_rows(
        &self,
        schema: &str,
        table: &str,
        req: &polydb_protocol::TableRowsRequest,
    ) -> CoreResult<polydb_protocol::TableRowsResult> {
        let (sql, args) =
            polydb_db_core::build_rows_query(polydb_db_core::DIALECT_ORACLE, schema, table, req)?;
        let limit = polydb_db_core::browse_rows_limits(req.limit);
        let res = self.execute(&sql, &args).await?;
        Ok(polydb_db_core::rows_result_to_browse_page(
            res, req.offset, limit,
        ))
    }

    async fn browse_rows_count(
        &self,
        schema: &str,
        table: &str,
        req: &polydb_protocol::TableRowsRequest,
    ) -> CoreResult<u64> {
        let (sql, args) = polydb_db_core::build_rows_count_query(
            polydb_db_core::DIALECT_ORACLE,
            schema,
            table,
            req,
        )?;
        let res = self.execute(&sql, &args).await?;
        polydb_db_core::count_result_to_u64(res)
    }

    fn clone_sql_driver_arc(&self) -> Arc<dyn SqlDriver> {
        let arc: Arc<OracleConn> = self.clone_arc();
        arc
    }

    async fn begin_tx(&self, mode: TxMode) -> CoreResult<Box<dyn Any + Send>> {
        Ok(Box::new(OracleConn::begin_tx(self, mode).await?))
    }

    async fn execute_in_tx(
        &self,
        tx: &mut Box<dyn Any + Send>,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let h = tx
            .downcast_mut::<OracleTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        OracleConn::execute_in_tx(self, h, sql, params).await
    }

    async fn commit(&self, tx: &mut Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast_mut::<OracleTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        OracleConn::commit(self, h).await
    }

    async fn rollback(&self, tx: &mut Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast_mut::<OracleTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        OracleConn::rollback(self, h).await
    }

    async fn execute(&self, sql: &str, params: &[Value]) -> CoreResult<QueryResult> {
        // oracle crate 是阻塞式 API（ODPI-C）：整段执行直接在 runtime worker
        // 上跑会阻塞该 worker，WS 的取消/超时臂没有机会被轮询（与 db-sqlite
        // 同范式）。移入 spawn_blocking：语句本身仍在后台跑完（best-effort
        // 取消，与 Go 侧语义一致），future 则能让步。事务内的 execute 见
        // execute_in_tx，同样经 spawn_blocking 迁移（OracleTxHandle 是单元
        // 结构体，`&mut` 句柄留在 async 侧、从不进闭包）。
        let conn = Arc::clone(&self.conn);
        let sql = sql.to_string();
        let params = params.to_vec();
        tokio::task::spawn_blocking(move || {
            let start = std::time::Instant::now();
            let stmt_type = Self::detect_statement_type(&sql);
            let boxes = Self::to_sql_params(&params);
            let refs = Self::param_refs(&boxes);

            let guard = conn.lock();
            let mut stmt = guard
                .statement(&sql)
                .build()
                .map_err(|e| db_err(&e, "prepare failed"))?;

            let wants_rows = stmt_type == StatementType::Select
                || stmt_type == StatementType::Other
                || has_returning_clause(&sql);
            if wants_rows {
                let mut rows = stmt.query(&refs).map_err(|e| db_err(&e, "query failed"))?;
                let columns = rows
                    .column_info()
                    .iter()
                    .map(|c| ResultColumn {
                        name: c.name().to_string(),
                        table: None,
                        data_type: oracle_type_name(c.oracle_type()).to_string(),
                        generic_type: None,
                        nullable: Some(true),
                    })
                    .collect();
                let mut data = Vec::new();
                while let Some(row) = rows
                    .next()
                    .transpose()
                    .map_err(|e| db_err(&e, "row failed"))?
                {
                    data.push(row.sql_values().iter().map(Self::or_value).collect());
                }
                Ok(QueryResult {
                    columns,
                    rows: data,
                    affected_rows: 0,
                    execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                    truncated: false,
                    total_rows: None,
                    has_more: false,
                    statement_type: Some(stmt_type),
                })
            } else {
                stmt.execute(&refs)
                    .map_err(|e| db_err(&e, "execute failed"))?;
                let affected = stmt
                    .row_count()
                    .map_err(|e| db_err(&e, "row_count failed"))?;
                Ok(QueryResult {
                    columns: vec![],
                    rows: vec![],
                    affected_rows: affected,
                    execution_time_ms: start.elapsed().as_secs_f64() * 1000.0,
                    truncated: false,
                    total_rows: None,
                    has_more: false,
                    statement_type: Some(stmt_type),
                })
            }
        })
        .await
        .map_err(|e| CoreError::Internal(format!("oracle task join: {e}")))?
    }

    async fn list_schemas(&self) -> CoreResult<Vec<SchemaInfo>> {
        let conn = self.conn.lock();
        let exclude: Vec<String> = MAINTAINED
            .iter()
            .map(|s| format!("'{}'", s.replace('\'', "''")))
            .collect();
        let sql = format!(
            "SELECT username FROM all_users WHERE username NOT IN ({}) ORDER BY username",
            exclude.join(",")
        );
        let mut rows = conn
            .query_as::<String>(&sql, &[])
            .map_err(|e| CoreError::Driver(format!("list_schemas: {e}")))?;
        let mut out = Vec::new();
        while let Some(name) = rows
            .next()
            .transpose()
            .map_err(|e| CoreError::Driver(format!("list_schemas row: {e}")))?
        {
            out.push(SchemaInfo { name });
        }
        Ok(out)
    }

    async fn list_tables(&self, schema: &str) -> CoreResult<Vec<TableInfo>> {
        let conn = self.conn.lock();
        let sql = "SELECT table_name, 'table' FROM all_tables WHERE owner = :1 \
                   UNION ALL \
                   SELECT view_name, 'view' FROM all_views WHERE owner = :1 ORDER BY 1";
        let mut rows = conn
            .query_as::<(String, String)>(sql, &[&schema])
            .map_err(|e| CoreError::Driver(format!("list_tables: {e}")))?;
        let mut out = Vec::new();
        while let Some((name, typ)) = rows
            .next()
            .transpose()
            .map_err(|e| CoreError::Driver(format!("list_tables row: {e}")))?
        {
            out.push(TableInfo {
                name,
                schema: schema.to_string(),
                table_type: if typ == "view" {
                    TableType::View
                } else {
                    TableType::Table
                },
                row_count: None,
                comment: None,
            });
        }
        Ok(out)
    }

    async fn list_columns(&self, schema: &str, table: &str) -> CoreResult<Vec<ColumnInfo>> {
        let conn = self.conn.lock();
        let sql = "SELECT c.column_name, \
                        c.data_type || CASE \
                          WHEN c.data_type IN ('VARCHAR2','VARCHAR','CHAR','NVARCHAR2','NCHAR') \
                            THEN '(' || c.char_length || ')' \
                          WHEN c.data_type = 'NUMBER' AND c.data_precision IS NOT NULL \
                            THEN '(' || c.data_precision || ',' || NVL(c.data_scale, 0) || ')' \
                          ELSE '' END, \
                        c.nullable, c.column_id, ic.column_name \
                   FROM all_tab_columns c \
                   LEFT JOIN all_tab_identity_cols ic \
                          ON ic.owner = c.owner AND ic.table_name = c.table_name AND ic.column_name = c.column_name \
                   WHERE c.owner = :1 AND c.table_name = :2 ORDER BY c.column_id";
        let mut rows = conn
            .query_as::<(String, String, String, i32, Option<String>)>(sql, &[&schema, &table])
            .map_err(|e| CoreError::Driver(format!("list_columns: {e}")))?;

        let mut cols: Vec<ColumnInfo> = Vec::new();
        while let Some((name, data_type, nullable, pos, auto)) = rows
            .next()
            .transpose()
            .map_err(|e| CoreError::Driver(format!("list_columns row: {e}")))?
        {
            cols.push(ColumnInfo {
                name,
                data_type,
                generic_type: None,
                nullable: nullable == "Y",
                default_value: None,
                max_length: None,
                precision: None,
                scale: None,
                is_primary_key: false,
                is_auto_increment: auto.is_some(),
                comment: None,
                ordinal_position: pos,
            });
        }

        let pk_sql = "SELECT cc.column_name FROM all_constraints c \
                      JOIN all_cons_columns cc ON cc.owner = c.owner AND cc.constraint_name = c.constraint_name \
                      WHERE c.owner = :1 AND c.table_name = :2 AND c.constraint_type = 'P' ORDER BY cc.position";
        let mut pk_rows = conn
            .query_as::<String>(pk_sql, &[&schema, &table])
            .map_err(|e| CoreError::Driver(format!("list_columns pk: {e}")))?;
        let mut pk_set = std::collections::HashSet::new();
        while let Some(name) = pk_rows
            .next()
            .transpose()
            .map_err(|e| CoreError::Driver(format!("list_columns pk row: {e}")))?
        {
            pk_set.insert(name);
        }
        for c in cols.iter_mut() {
            if pk_set.contains(&c.name) {
                c.is_primary_key = true;
            }
        }
        Ok(cols)
    }

    async fn list_indexes(&self, schema: &str, table: &str) -> CoreResult<Vec<IndexInfo>> {
        let conn = self.conn.lock();
        let sql = "SELECT i.index_name, i.uniqueness, i.index_type, ic.column_name, ic.column_position \
                   FROM all_indexes i \
                   JOIN all_ind_columns ic ON ic.index_owner = i.owner AND ic.index_name = i.index_name \
                   WHERE i.table_owner = :1 AND i.table_name = :2 \
                   ORDER BY i.index_name, ic.column_position";
        let mut rows = conn
            .query_as::<(String, String, String, String, i32)>(sql, &[&schema, &table])
            .map_err(|e| CoreError::Driver(format!("list_indexes: {e}")))?;

        let pk_sql = "SELECT constraint_name FROM all_constraints \
                      WHERE owner = :1 AND table_name = :2 AND constraint_type = 'P'";
        let mut pk_rows = conn
            .query_as::<String>(pk_sql, &[&schema, &table])
            .map_err(|e| CoreError::Driver(format!("list_indexes pk: {e}")))?;
        let mut primary = std::collections::HashSet::new();
        while let Some(name) = pk_rows
            .next()
            .transpose()
            .map_err(|e| CoreError::Driver(format!("list_indexes pk row: {e}")))?
        {
            primary.insert(name);
        }

        let mut order: Vec<String> = Vec::new();
        let mut groups: HashMap<String, (bool, bool, IndexType, Vec<IndexColumn>)> = HashMap::new();
        while let Some((name, uniqueness, index_type, col, pos)) = rows
            .next()
            .transpose()
            .map_err(|e| CoreError::Driver(format!("list_indexes row: {e}")))?
        {
            if !groups.contains_key(&name) {
                order.push(name.clone());
                groups.insert(
                    name.clone(),
                    (
                        uniqueness.eq_ignore_ascii_case("UNIQUE"),
                        primary.contains(&name),
                        if index_type.to_uppercase().contains("BITMAP") {
                            IndexType::Hash
                        } else if index_type.to_uppercase().contains("FUNCTION") {
                            IndexType::Other
                        } else {
                            IndexType::Btree
                        },
                        Vec::new(),
                    ),
                );
            }
            groups.get_mut(&name).unwrap().3.push(IndexColumn {
                name: col,
                position: pos,
                order: None,
                prefix_length: None,
            });
        }

        Ok(order
            .into_iter()
            .map(|name| {
                let (unique, is_primary, index_type, columns) = groups.remove(&name).unwrap();
                IndexInfo {
                    name,
                    unique,
                    primary: is_primary,
                    index_type: Some(index_type),
                    columns,
                    comment: None,
                }
            })
            .collect())
    }

    async fn list_foreign_keys(
        &self,
        schema: &str,
        table: &str,
    ) -> CoreResult<Vec<ForeignKeyInfo>> {
        let conn = self.conn.lock();
        let sql = "SELECT c.constraint_name, c1.column_name, rc.owner, rc.table_name, c2.column_name \
                   FROM all_constraints c \
                   JOIN all_cons_columns c1 ON c1.owner = c.owner AND c1.constraint_name = c.constraint_name \
                   JOIN all_constraints rc ON rc.owner = c.r_owner AND rc.constraint_name = c.r_constraint_name \
                   JOIN all_cons_columns c2 ON c2.owner = rc.owner AND c2.constraint_name = rc.constraint_name AND c2.position = c1.position \
                   WHERE c.owner = :1 AND c.table_name = :2 AND c.constraint_type = 'R' \
                   ORDER BY c.constraint_name, c1.position";
        let mut rows = conn
            .query_as::<(String, String, String, String, String)>(sql, &[&schema, &table])
            .map_err(|e| CoreError::Driver(format!("list_foreign_keys: {e}")))?;

        let mut order: Vec<String> = Vec::new();
        let mut groups: HashMap<String, (String, String, Vec<String>, Vec<String>)> =
            HashMap::new();
        while let Some((name, from, ref_schema, ref_table, to)) = rows
            .next()
            .transpose()
            .map_err(|e| CoreError::Driver(format!("list_foreign_keys row: {e}")))?
        {
            if !groups.contains_key(&name) {
                order.push(name.clone());
                groups.insert(
                    name.clone(),
                    (ref_schema, ref_table, Vec::new(), Vec::new()),
                );
            }
            let group = groups.get_mut(&name).unwrap();
            group.2.push(from);
            group.3.push(to);
        }

        Ok(order
            .into_iter()
            .map(|name| {
                let (ref_schema, ref_table, cols, ref_cols) = groups.remove(&name).unwrap();
                ForeignKeyInfo {
                    name,
                    columns: cols,
                    referenced_schema: ref_schema,
                    referenced_table: ref_table,
                    referenced_columns: ref_cols,
                    on_update: None,
                    on_delete: None,
                }
            })
            .collect())
    }

    async fn create_table_sql(&self, schema: &str, table: &str) -> CoreResult<String> {
        let cols = self.list_columns(schema, table).await?;
        if cols.is_empty() {
            return Err(CoreError::Driver(format!(
                "table not found: {schema}.{table}"
            )));
        }
        let mut b = format!(
            "CREATE TABLE {}.{} (\n",
            quote_ident(schema),
            quote_ident(table)
        );
        for (i, col) in cols.iter().enumerate() {
            let suffix = if col.is_primary_key {
                " PRIMARY KEY"
            } else if !col.nullable {
                " NOT NULL"
            } else {
                ""
            };
            if i < cols.len() - 1 {
                b.push_str(&format!(
                    "  {} {}{},\n",
                    quote_ident(&col.name),
                    col.data_type,
                    suffix
                ));
            } else {
                b.push_str(&format!(
                    "  {} {}{}\n",
                    quote_ident(&col.name),
                    col.data_type,
                    suffix
                ));
            }
        }
        b.push_str(");");
        Ok(b)
    }
}

// oracle errors carry the numeric ORA-XXXXX code: the second evidence layer.
fn db_err(e: &oracle::Error, what: &str) -> CoreError {
    let text = format!("{what}: {e}");
    let numeric = e.db_error().map(|d| d.code() as u32);
    driver_error(None, numeric, None, &text)
}

#[cfg(test)]
mod tests {
    use super::*;
    use oracle::sql_type::OracleType as T;

    /// 列类型名映射逐类钉住（PD-28，2026-11-10）。
    ///
    /// 规范形态是 **Oracle SQL 类型名、全大写、不含长度/精度**——与其它
    /// 引擎同口径（PG `INT4`、MySQL `VARCHAR`、MSSQL `NVARCHAR`），且符合
    /// spec 对 `ResultColumn.type` 的 "Database-specific type name" 定义。
    #[test]
    fn oracle_type_names_are_sql_type_names() {
        // 字符族：四个变体归成两个族名（Go 侧拿不到字符集形态，
        // 见函数文档的「分辨率上限」）。
        assert_eq!(oracle_type_name(&T::Varchar2(50)), "VARCHAR2");
        assert_eq!(oracle_type_name(&T::NVarchar2(50)), "VARCHAR2");
        assert_eq!(oracle_type_name(&T::Char(10)), "CHAR");
        assert_eq!(oracle_type_name(&T::NChar(10)), "CHAR");

        // 数值。
        assert_eq!(oracle_type_name(&T::Number(10, 2)), "NUMBER");
        assert_eq!(oracle_type_name(&T::Int64), "NUMBER");
        assert_eq!(oracle_type_name(&T::UInt64), "NUMBER");
        assert_eq!(oracle_type_name(&T::Float(63)), "FLOAT");

        // 日期时间。
        assert_eq!(oracle_type_name(&T::Date), "DATE");
        assert_eq!(oracle_type_name(&T::Timestamp(6)), "TIMESTAMP");
        assert_eq!(
            oracle_type_name(&T::TimestampTZ(6)),
            "TIMESTAMP WITH TIME ZONE"
        );
        assert_eq!(
            oracle_type_name(&T::TimestampLTZ(6)),
            "TIMESTAMP WITH LOCAL TIME ZONE"
        );
        assert_eq!(
            oracle_type_name(&T::IntervalYM(2)),
            "INTERVAL YEAR TO MONTH"
        );
        assert_eq!(
            oracle_type_name(&T::IntervalDS(2, 6)),
            "INTERVAL DAY TO SECOND"
        );

        // 二进制与大对象。NCLOB 归 CLOB（Go 侧无独立变体）。
        assert_eq!(oracle_type_name(&T::Raw(16)), "RAW");
        assert_eq!(oracle_type_name(&T::Long), "LONG");
        assert_eq!(oracle_type_name(&T::LongRaw), "LONG RAW");
        assert_eq!(oracle_type_name(&T::CLOB), "CLOB");
        assert_eq!(oracle_type_name(&T::NCLOB), "CLOB");
        assert_eq!(oracle_type_name(&T::BLOB), "BLOB");
        assert_eq!(oracle_type_name(&T::BFILE), "BFILE");

        // 其它。
        assert_eq!(oracle_type_name(&T::Rowid), "ROWID");
        assert_eq!(oracle_type_name(&T::BinaryFloat), "BINARY_FLOAT");
        assert_eq!(oracle_type_name(&T::BinaryDouble), "BINARY_DOUBLE");
        assert_eq!(oracle_type_name(&T::Boolean), "BOOLEAN");
        assert_eq!(oracle_type_name(&T::Json), "JSON");
        assert_eq!(oracle_type_name(&T::Xml), "XMLTYPE");
        assert_eq!(oracle_type_name(&T::RefCursor), "REF CURSOR");
    }

    /// 映射结果必须全大写（与 PG/MySQL/MSSQL 的列类型拼写同口径）。
    #[test]
    fn oracle_type_names_are_uppercase() {
        let all = [
            T::Varchar2(1),
            T::NVarchar2(1),
            T::Char(1),
            T::NChar(1),
            T::Number(1, 0),
            T::Float(1),
            T::Date,
            T::Timestamp(1),
            T::TimestampTZ(1),
            T::TimestampLTZ(1),
            T::IntervalYM(1),
            T::IntervalDS(1, 1),
            T::Raw(1),
            T::Long,
            T::LongRaw,
            T::CLOB,
            T::NCLOB,
            T::BLOB,
            T::BFILE,
            T::Rowid,
            T::BinaryFloat,
            T::BinaryDouble,
            T::Boolean,
            T::Json,
            T::Xml,
            T::RefCursor,
        ];
        for t in all {
            let name = oracle_type_name(&t);
            assert_eq!(
                name,
                name.to_uppercase(),
                "{t:?} 的规范名 {name:?} 不是全大写"
            );
        }
    }

    /// 编译期证据：spawn_blocking 闭包只捕获 `Arc<Mutex<Connection>>`，而
    /// `OracleConn` / `OracleTxHandle` 必须满足 Send + Sync + 'static 才能
    /// 跨进 `Box<dyn Any + Send>` 与阻塞池闭包。这条断言锁住"句柄留在
    /// async 侧、连接 Arc 进闭包"的迁移前提——若哪天句柄开始持有连接引用，
    /// 这里会先编译失败。
    #[test]
    fn oracle_conn_and_tx_handle_are_send_sync_static() {
        fn assert_send_sync_static<T: Send + Sync + 'static>() {}
        assert_send_sync_static::<OracleConn>();
        assert_send_sync_static::<OracleTxHandle>();
    }

    /// `OracleTxHandle` 是单元结构体：零尺寸、不持有连接引用。这正是事务内
    /// 三条路径（begin/commit/rollback）能改成 spawn_blocking 的原因——
    /// `&mut` 句柄留在 async 侧、从不进闭包。
    #[test]
    fn tx_handle_is_a_zero_sized_unit_struct() {
        assert_eq!(std::mem::size_of::<OracleTxHandle>(), 0);
        let h = OracleTxHandle;
        let mut h2 = h.clone();
        let _ = &mut h2; // 句柄可随意克隆/移动，不牵出连接
    }

    /// 隔离级别映射：只有 SERIALIZABLE 需要声明；Oracle 不支持的
    /// READ UNCOMMITTED 回退默认，默认 ReadCommitted 不发语句。
    #[test]
    fn set_transaction_sql_maps_only_the_supported_levels() {
        assert_eq!(set_transaction_sql(IsolationLevel::ReadCommitted), None);
        assert_eq!(set_transaction_sql(IsolationLevel::ReadUncommitted), None);
        // Oracle 没有 REPEATABLE READ：与 SERIALIZABLE 同发 SERIALIZABLE（对齐 Go 侧 go-ora）。
        assert_eq!(
            set_transaction_sql(IsolationLevel::Serializable),
            Some("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")
        );
        assert_eq!(
            set_transaction_sql(IsolationLevel::RepeatableRead),
            Some("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")
        );
    }

    /// 回归：旧实现发的是 `ALTER SESSION SET TRANSACTION …`，该拼写不是
    /// Oracle 语法，一直靠"失败回退默认"掩盖——真库用例的计数会因此变成 5。
    /// 这里把合法拼写钉死，防止再退回 ALTER SESSION。
    #[test]
    fn set_transaction_sql_is_oracle_legal_not_alter_session() {
        let sql = set_transaction_sql(IsolationLevel::Serializable).expect("must declare");
        assert!(
            sql.starts_with("SET TRANSACTION"),
            "must be SET TRANSACTION: {sql}"
        );
        assert!(
            !sql.starts_with("ALTER SESSION"),
            "ALTER SESSION 拼写不是 Oracle 语法: {sql}"
        );
    }

    /// P2-3 回归：Array/Object 中的 NaN/Inf 必须序列化为字符串值，
    /// 不能被 serde_json 静默转为 null（数据丢失）。
    #[test]
    fn json_text_param_array_with_nan_inf_serializes_as_string() {
        let arr = Value::Array(vec![
            Value::Float(1.5),
            Value::Float(f64::NAN),
            Value::Float(f64::INFINITY),
        ]);
        let json = OracleConn::json_text_param(&arr);
        let parsed: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed[0], 1.5);
        assert_eq!(
            parsed[1], "NaN",
            "NaN must be preserved as string, not null"
        );
        assert_eq!(parsed[2], "Infinity");
    }
    /// PD-19（2026-11-10）回归：Rust 侧 TIMESTAMP 读取走 oracle crate
    /// Timestamp 的 Display（"Y-M-D H:M:S[.prec][ ±HH:MM]"）——这是两端
    /// 对齐的**锚点形态**（Go 侧 DriverToValueTyped 的 TIMESTAMP 分支已按
    /// 本格式实现，见 go/pkg/dbcore/valueconv_orats_test.go）。
    ///
    /// 缺陷背景：Go 侧旧链对 Oracle TIMESTAMP* 走 time.Time ⇒ RFC3339Nano
    /// （"2026-01-02T15:04:05.5+08:00"），与本格式不同——同一列两端
    /// wire 值不同（AGENTS.md §14 红线）。
    ///
    /// or_value 链对 Timestamp 的命中路径：to_string 分支
    /// （oracle-0.6.3 sql_value.rs:1035 NativeType::Timestamp ⇒
    /// get_timestamp_unchecked().to_string()）。本用例把 crate Display
    /// 的关键样例钉在此处（取自 oracle-0.6.3 timestamp.rs:533-545 与
    /// 文档 :49-95 的既有断言），防止实现侧改动读取顺序后漂移。
    #[test]
    fn or_timestamp_display_contract() {
        use oracle::sql_type::Timestamp;
        // 生产路径的 precision 来自列类型（from_dpi_timestamp：
        // OracleType::Timestamp(prec) ⇒ precision=prec；DATE ⇒ 0），
        // 而 Timestamp::new 固定 precision=9（timestamp.rs:229）——
        // 故每条都显式 and_prec 对齐真实读取路径。
        let ts = Timestamp::new(2012, 3, 4, 5, 6, 7, 0)
            .unwrap()
            .and_prec(0)
            .unwrap();
        assert_eq!(ts.to_string(), "2012-03-04 05:06:07");
        let ts = Timestamp::new(2012, 3, 4, 5, 6, 7, 800000000)
            .unwrap()
            .and_prec(1)
            .unwrap();
        assert_eq!(ts.to_string(), "2012-03-04 05:06:07.8");
        let ts = Timestamp::new(2012, 3, 4, 5, 6, 7, 890000000)
            .unwrap()
            .and_prec(3)
            .unwrap();
        assert_eq!(ts.to_string(), "2012-03-04 05:06:07.890");
        let ts = Timestamp::new(2012, 3, 4, 5, 6, 7, 890123000)
            .unwrap()
            .and_prec(6)
            .unwrap();
        assert_eq!(ts.to_string(), "2012-03-04 05:06:07.890123");
    }
}
