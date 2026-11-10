//! polydb-db-mssql：基于 tiberius 的 SQL Server 驱动实现（对应 Go 侧 dbmssql）。
//! tiberius Client 内部含非 Send 句柄，且 close 消费自身：所有交互经
//! polydb_rt::block_on（共享 tokio 运行时，DECISION §5.5）同步执行，外层
//! async 方法不跨 await 持有 guard，满足 Send。

use std::any::Any;
use std::collections::HashMap;
use std::future::Future;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use chrono::{DateTime, NaiveDate, NaiveDateTime, NaiveTime, Utc};
use parking_lot::Mutex;
use tiberius::numeric::Decimal;
use tiberius::{AuthMethod, Client, Config, Row, ToSql};
use tokio::net::TcpStream;
use tokio_util::compat::{Compat, TokioAsyncWriteCompatExt};

use polydb_core::error_classify::driver_error;
use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyInfo, IndexColumn, IndexInfo,
    IndexType, IsolationLevel, QueryResult, ResultColumn, SchemaInfo, StatementType, TableInfo,
    TableType, Value,
};
use polydb_db_core::{has_returning_clause, DatabaseDriver, SqlDriver, TxMode};

// tiberius 0.12 无内置连接/查询超时；不可达主机（防火墙丢包）会让 TCP 握手
// 挂到系统超时（本机实测 ~4s/地址，tokio 会串行尝试多个地址）。所有 block_on
// 一律套超时，保证契约测试中无服务时快速失败而非挂死 tokio worker。
// Connect/login bound: a black-holed address fails here instead of hanging
// the worker until the OS gives up.
const IO_TIMEOUT: Duration = Duration::from_secs(10);
// Statement backstop: 6s used to cap query/execute directly, so any
// legitimate analytical query died with "timeout after 6s". Real cancellation
// is the caller's ctx (select+retire); this only stops a wedged driver from
// pinning the worker forever.
const STATEMENT_TIMEOUT: Duration = Duration::from_secs(300);

/// tiberius 没有 NULL 的 ToSql 实现：ColumnData 的各变体以 Option 承载，
/// None 即 SQL NULL（选 I64 只承载可空性；TDS 参数无绑定类型强约束）。
struct SqlNull;

impl ToSql for SqlNull {
    fn to_sql(&self) -> tiberius::ColumnData<'_> {
        tiberius::ColumnData::I64(None)
    }
}

/// tiberius 的 [`tiberius::ColumnType`] → SQL Server 类型名。
/// Debug 输出（"Int4"、"NVarchar"）不是 SQL 拼写，此前直接塞进 data_type。
/// 变长 "n" 类型的长度信息经 tiberius 的 From 折叠成定长变体（如 Intn(4)
/// 已是 Int4），无法区分的按最常见拼写给出。
/// 列类型名（**大写**）——与 Go 侧 go-mssqldb 的 `makeGoLangTypeName`
/// （types.go:1359 起）逐字一致。
///
/// 大小写是该驱动的**规范拼写**：go-mssqldb 的文档明写 "Type names should be
/// uppercase"（types.go:1360-1362，例子 "VARCHAR"/"NVARCHAR"/"INT"/
/// "BIGINT"）。本函数此前给**小写**（"int"/"nvarchar"/"bigint"），
/// 于是两端对同一列给出不同拼写——契约测试为此对 net 场景用
/// `onlyColumnNames` 把列类型整个裁掉（PD-22，2026-11-10 收窄）。
fn column_type_name(t: tiberius::ColumnType) -> &'static str {
    use tiberius::ColumnType as T;
    match t {
        T::Null => "",
        T::Bit | T::Bitn => "BIT",
        T::Int1 => "TINYINT",
        T::Int2 => "SMALLINT",
        T::Int4 => "INT",
        T::Int8 => "BIGINT",
        // 变长骨架在长度不落在 1/2/4/8 时保留原样（tiberius 的 From 折叠规则），
        // 按最常见拼写兜底（Go 侧 typeIntN 亦按 size 分派到 1/2/4/8 族）。
        T::Intn => "INT",
        T::Datetime4 => "SMALLDATETIME",
        T::Datetime => "DATETIME",
        T::Datetimen => "DATETIME",
        T::Datetime2 => "DATETIME2",
        T::Daten => "DATE",
        T::Timen => "TIME",
        T::DatetimeOffsetn => "DATETIMEOFFSET",
        T::Float4 => "REAL",
        T::Float8 | T::Floatn => "FLOAT",
        T::Money => "MONEY",
        T::Money4 => "SMALLMONEY",
        T::Guid => "UNIQUEIDENTIFIER",
        // Go 侧 typeDecimalN 与 typeNumericN 都返回 "DECIMAL"（types.go
        // 的 case typeDecimalN, typeNumericN），故此处同样不区分二者。
        T::Decimaln | T::Numericn => "DECIMAL",
        T::BigVarBin => "VARBINARY",
        T::BigBinary => "BINARY",
        T::BigVarChar => "VARCHAR",
        T::BigChar => "CHAR",
        T::NVarchar => "NVARCHAR",
        T::NChar => "NCHAR",
        T::Xml => "XML",
        T::Udt => "UDT",
        T::Text => "TEXT",
        T::Image => "IMAGE",
        T::NText => "NTEXT",
        T::SSVariant => "SQL_VARIANT",
    }
}

pub struct MssqlConn {
    client: Arc<Mutex<Option<Client<Compat<TcpStream>>>>>,
    _dsn: String,
}

/// 以 IO_TIMEOUT 上限同步执行一个异步操作（tiberius 无内置超时）。
/// 用 tokio::select! 与操作竞速：超时分支获胜时，操作 future 被直接丢弃（对
/// 连接/查询来说是安全的取消，底层 socket 一并关闭）。
fn timed<T, E>(what: &str, fut: impl Future<Output = Result<T, E>>) -> CoreResult<T>
where
    E: std::fmt::Display,
{
    timed_within(what, IO_TIMEOUT, fut)
}

fn timed_within<T, E>(
    what: &str,
    limit: Duration,
    fut: impl Future<Output = Result<T, E>>,
) -> CoreResult<T>
where
    E: std::fmt::Display,
{
    enum Out<T, E> {
        Done(Result<T, E>),
        TimedOut,
    }
    let out = polydb_rt::block_on(async {
        tokio::select! {
            out = fut => Out::Done(out),
            _ = tokio::time::sleep(limit) => Out::TimedOut,
        }
    });
    match out {
        Out::Done(Ok(v)) => Ok(v),
        Out::Done(Err(e)) => Err(driver_error(None, None, None, &format!("{what}: {e}"))),
        Out::TimedOut => Err(CoreError::Driver(format!(
            "{what}: timeout after {limit:?}"
        ))),
    }
}

impl MssqlConn {
    /// host:port/database 形式定位 SQL Server；password 为可选连接密码
    /// （明文来自 keyring，连接时一次性注入，不落库；与 Go 侧 mssqlDSN 对齐）。
    pub fn open(
        host: &str,
        port: u16,
        database: Option<&str>,
        username: &str,
        password: &str,
    ) -> CoreResult<Self> {
        let mut config = Config::new();
        config.host(host);
        config.port(port);
        config.authentication(AuthMethod::sql_server(username, password));
        if let Some(db) = database {
            config.database(db);
        }
        config.trust_cert();

        let tcp = timed("connect mssql", TcpStream::connect(config.get_addr()))?;
        tcp.set_nodelay(true).ok();
        let client = timed("login mssql", Client::connect(config, tcp.compat_write()))?;

        Ok(Self {
            client: Arc::new(Mutex::new(Some(client))),
            _dsn: format!("{host}:{port}"),
        })
    }

    /// 返回指向 self 的共享 `Arc<Self>`：tiberius `Client` 不可克隆，
    /// 因此内部包 `Arc<Mutex<...>>`，两个 `Arc<Self>` 共享同一个 client。
    /// 用于 app-core 持有第二个 `Arc<dyn SqlDriver>`，与 `Connection`
    /// 内的驱动共享同一底层连接。
    pub fn clone_arc(&self) -> Arc<Self> {
        Arc::new(Self {
            client: Arc::clone(&self.client),
            _dsn: self._dsn.clone(),
        })
    }

    /// Array/Object（含 $blob Object）的绑定形态：跨线协议统一为 JSON 文本——
    /// 与 Go 侧 ValueToDriver 的 string(v.Raw()) 对齐。此前兜底成 0i64 会把
    /// JSON/blob 列的数据静默写成 0。NaN/Inf 先经 polydb_db_core::value_to_json_text
    /// 替换为字符串，避免 serde_json 静默转 null（P2-3）。
    /// 独立成纯函数供口径测试下探。
    fn json_text_param(val: &Value) -> String {
        polydb_db_core::value_to_json_text(val)
    }

    fn tsql_params(v: &[Value]) -> Vec<Box<dyn ToSql>> {
        v.iter()
            .map(|val| match val {
                Value::Integer(i) => Box::new(*i) as Box<dyn ToSql>,
                Value::U64(u) => {
                    // MSSQL BIGINT 是 i64——与 SQLite/Oracle 相同的限制。
                    if *u <= i64::MAX as u64 {
                        Box::new(*u as i64) as Box<dyn ToSql>
                    } else {
                        Box::new(*u as f64) as Box<dyn ToSql>
                    }
                }
                Value::Float(f) => Box::new(*f) as Box<dyn ToSql>,
                Value::Bool(b) => Box::new(*b) as Box<dyn ToSql>,
                Value::String(s) => Box::new(s.clone()) as Box<dyn ToSql>,
                // NULL 必须绑成 NULL：tiberius 的 ColumnData 各变体以 Option
                // 承载，None 即 SQL NULL；此前兜底成 0i64 会把 NULL 过滤值
                // 静默变成 0。
                Value::Null => Box::new(SqlNull) as Box<dyn ToSql>,
                Value::Array(_) | Value::Object(_) => {
                    Box::new(Self::json_text_param(val)) as Box<dyn ToSql>
                }
            })
            .collect()
    }

    fn param_refs(boxes: &[Box<dyn ToSql>]) -> Vec<&dyn ToSql> {
        boxes.iter().map(|b| b.as_ref()).collect()
    }

    /// tiberius 的 try_get 返回 Result<Option<T>>（None 即 SQL NULL）。
    fn mssql_value(row: &Row, i: usize) -> Value {
        if let Ok(Some(v)) = row.try_get::<bool, usize>(i) {
            return Value::Bool(v);
        }
        if let Ok(Some(v)) = row.try_get::<i64, usize>(i) {
            return Value::Integer(v);
        }
        if let Ok(Some(v)) = row.try_get::<i32, usize>(i) {
            return Value::Integer(v as i64);
        }
        if let Ok(Some(v)) = row.try_get::<i16, usize>(i) {
            return Value::Integer(v as i64);
        }
        if let Ok(Some(v)) = row.try_get::<u8, usize>(i) {
            return Value::Integer(v as i64);
        }
        if let Ok(Some(v)) = row.try_get::<f64, usize>(i) {
            return Value::Float(v);
        }
        if let Ok(Some(v)) = row.try_get::<f32, usize>(i) {
            return Value::Float(v as f64);
        }
        if let Ok(Some(v)) = row.try_get::<Decimal, usize>(i) {
            return Value::String(v.to_string());
        }
        // UNIQUEIDENTIFIER：tiberius 的 FromSql 有 Uuid 实现
        // （from_sql.rs:67 `Uuid: ColumnData::Guid(val) => ...`），但此前读取链
        // 没有该分支，且 `&[u8]`/`Vec<u8>` 只匹配 ColumnData::Binary——Guid 列
        // 整条链落空，最终掉到下面的 Value::Null，**数据丢失**。
        //
        // 产出标准 UUID 小写文本（PD-18），与 Go 侧修复后的形态一致：
        // go-mssqldb 解码 GUID 列给 16 字节 []byte（types.go:decodeGuid），
        // Go 旧链把它当 Blob/普通字节串输出——两端都不曾给用户可读的
        // UUID 文本，本分支按 spec 的字符串语义统一为标准文本形态。
        if let Ok(Some(v)) = row.try_get::<tiberius::Uuid, usize>(i) {
            return Value::String(v.to_string());
        }
        if let Ok(Some(v)) = row.try_get::<NaiveDateTime, usize>(i) {
            // RFC3339Nano —— 与 Go 侧同形（PD-20，2026-11-10）。
            //
            // MSSQL 的 datetime 是**无时区挂钟**，哪端都没带时区信息：
            // tiberius 给裸 NaiveDateTime（tiberius-0.12.3
            // src/tds/time/chrono.rs:55）；Go 侧 go-mssqldb 用
            // `encoding.GetTimezone()` 解释同一挂钟
            // （types.go:354/396/652），而该值在**未显式设置时默认 UTC**
            // （go-mssqldb timezone.go getTimezone 的兜底 `time.UTC`）。
            // polydb 两端连接都没设 Timezone（Go 侧连接串无 Timezone 参数，
            // Rust 侧 Config 未设），故两端都是 UTC 语义——
            // and_utc() 断言的 UTC 与既有行为一致，不是编造。
            //
            // 权威侧依据见 WORKSPACE §132。
            return Value::String(v.and_utc().to_rfc3339());
        }
        if let Ok(Some(v)) = row.try_get::<DateTime<Utc>, usize>(i) {
            return Value::String(v.to_rfc3339());
        }
        if let Ok(Some(v)) = row.try_get::<NaiveDate, usize>(i) {
            return Value::String(v.to_string());
        }
        if let Ok(Some(v)) = row.try_get::<NaiveTime, usize>(i) {
            return Value::String(v.to_string());
        }
        if let Ok(Some(v)) = row.try_get::<&str, usize>(i) {
            return Value::String(v.to_string());
        }
        if let Ok(Some(v)) = row.try_get::<&[u8], usize>(i) {
            return polydb_db_core::blob_value(v);
        }
        Value::Null
    }

    /// 语句类型判定（收敛到 db-core 公共版，方言固定为 SQL Server）。
    ///
    /// 公共版按 token 类别判型（整词而非前缀），并认 T-SQL 的方括号标识符。
    fn detect_statement_type(sql: &str) -> StatementType {
        polydb_db_core::detect_statement_type(sql, Some(DatabaseKind::Mssql))
    }

    fn columns_of(rows: &[Row], metadata: Option<Vec<ResultColumn>>) -> Vec<ResultColumn> {
        // 非空结果集优先用行内列描述（与既有行为一致）；空结果集（rows 为空）
        // 时回退到 Metadata 捕获的列——空集时它是 stream 里唯一携带列信息的
        // item（RUST-MSSQL-EMPTY-COLUMNS 修复：此前 query_rows 跳过 Metadata，
        // 空 SELECT 的列元数据整体丢失）。
        if let Some(first) = rows.first() {
            return Self::result_columns_from(first.columns());
        }
        metadata.unwrap_or_default()
    }

    /// tiberius 的列描述 → polydb ResultColumn。
    fn result_columns_from(cols: &[tiberius::Column]) -> Vec<ResultColumn> {
        cols.iter()
            .map(|c| ResultColumn {
                name: c.name().to_string(),
                table: None,
                data_type: column_type_name(c.column_type()).to_string(),
                generic_type: None,
                nullable: Some(true),
            })
            .collect()
    }

    /// 结果集行 + 可选的列元数据（stream 里第一个 QueryItem::Metadata）。
    /// Metadata 是结果集描述：TDS 协议保证它先于行数据到达；空结果集时
    /// 它是唯一的 item。
    fn query_rows(
        &self,
        sql: &str,
        refs: &[&dyn ToSql],
        what: &str,
    ) -> CoreResult<(Vec<Row>, Option<Vec<ResultColumn>>)> {
        let mut client = self.client.lock();
        let stream = timed_within(
            what,
            STATEMENT_TIMEOUT,
            client.as_mut().unwrap().query(sql, refs),
        )?;
        // 物化上限（polydb_db_core::row_cap，behavior.md §5）：逐行拉取到上限
        // 即停——into_first_result 会把整个结果集收进内存。MSSQL 另有
        // STATEMENT_TIMEOUT 兜底墙钟，但那不限制行数 × 行宽的内存总量。
        // 单行 next 是 Future<Option<QueryItem>>，timed_within 的墙钟按行
        // 续期：总时长不再有界，但受 cap 与行到达节奏约束，与游标语义一致。
        use futures_util::StreamExt;
        let mut rows: Vec<Row> = Vec::new();
        let mut metadata: Option<Vec<ResultColumn>> = None;
        let mut stream = stream;
        loop {
            if rows.len() >= polydb_db_core::row_cap() {
                break; // 余下行随 QueryStream 一起 drop 关闭。
            }
            // next() 解析成 Option<Result<QueryItem>>，而 timed_within 要
            // Result future：把 Option 适配进 Result（None → Ok(None)）。
            // stream 按 &mut 借用进 async 块（StreamExt::next 接收 Pin<&mut
            // Self>），不移动所有权。
            let next = std::pin::pin!(stream.next());
            let item = timed_within(what, STATEMENT_TIMEOUT, async {
                match next.await {
                    Some(r) => r.map(Some),
                    None => Ok(None),
                }
            })?;
            let item = match item {
                Some(item) => item,
                None => break,
            };
            // tiberius 的 Item = QueryItem：Row 是数据行，Metadata（结果集
            // 描述）此前被跳过——空结果集时它是唯一携带列信息的 item，改为
            // 捕获首份描述（多结果集时仅取第一个，与「只显示首个结果集」
            // 的既有语义一致）。
            match item {
                tiberius::QueryItem::Row(row) => rows.push(row),
                tiberius::QueryItem::Metadata(md) => {
                    if metadata.is_none() {
                        metadata = Some(Self::result_columns_from(md.columns()));
                    }
                }
            }
        }
        Ok((rows, metadata))
    }
}

fn quote_ident(s: &str) -> String {
    format!("[{}]", s.replace(']', "]]"))
}

/// MSSQL 事务句柄。tiberius Client 无独立事务对象，事务状态由
/// BEGIN/COMMIT/ROLLBACK TRAN 在连接上隐式维护；本结构仅作不透明标记。
#[derive(Clone, Debug)]
pub struct MssqlTxHandle;

impl MssqlConn {
    fn begin_stmt(mode: TxMode) -> Option<&'static str> {
        match mode.isolation_level {
            IsolationLevel::ReadCommitted => None,
            IsolationLevel::ReadUncommitted => {
                Some("SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED; BEGIN TRAN")
            }
            IsolationLevel::RepeatableRead => {
                Some("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ; BEGIN TRAN")
            }
            IsolationLevel::Serializable => {
                Some("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE; BEGIN TRAN")
            }
        }
    }

    fn run_batch(&self, sql: &str, what: &str) -> CoreResult<()> {
        let mut client = self.client.lock();
        let stream = timed_within(
            what,
            STATEMENT_TIMEOUT,
            client.as_mut().unwrap().simple_query(sql),
        )?;
        timed_within(what, STATEMENT_TIMEOUT, stream.into_first_result())?;
        Ok(())
    }

    pub fn begin_tx(&self, mode: TxMode) -> CoreResult<MssqlTxHandle> {
        let sql = match Self::begin_stmt(mode) {
            Some(s) => s.to_string(),
            None => "BEGIN TRAN".to_string(),
        };
        self.run_batch(&sql, "begin tx failed")?;
        Ok(MssqlTxHandle)
    }

    pub fn commit(&self, _tx: &mut MssqlTxHandle) -> CoreResult<()> {
        self.run_batch("COMMIT TRAN", "commit failed")
    }

    pub fn rollback(&self, _tx: &mut MssqlTxHandle) -> CoreResult<()> {
        self.run_batch("ROLLBACK TRAN", "rollback failed")
    }

    /// 事务内执行一条 SQL。MSSQL 事务状态由连接上的 BEGIN/COMMIT/ROLLBACK
    /// TRAN 隐式维护，因此 execute_in_tx 与 execute 完全等价（同一 client）。
    pub async fn execute_in_tx(
        &self,
        _tx: &mut MssqlTxHandle,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let start = std::time::Instant::now();
        let stmt_type = Self::detect_statement_type(sql);
        let boxes = Self::tsql_params(params);
        let refs = Self::param_refs(&boxes);

        let wants_rows = stmt_type == StatementType::Select
            || stmt_type == StatementType::Other
            || has_returning_clause(sql);
        if wants_rows {
            let (rows, metadata) = self.query_rows(sql, &refs, "query failed")?;
            let columns = Self::columns_of(&rows, metadata);
            let data = rows
                .iter()
                .map(|r| (0..r.len()).map(|i| Self::mssql_value(r, i)).collect())
                .collect();
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
            let mut client = self.client.lock();
            let result = timed_within(
                "execute failed",
                STATEMENT_TIMEOUT,
                client.as_mut().unwrap().execute(sql, &refs),
            )?;
            let affected = result.rows_affected().first().copied().unwrap_or(0);
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
    }
}

#[async_trait]
impl DatabaseDriver for MssqlConn {
    fn kind(&self) -> DatabaseKind {
        DatabaseKind::Mssql
    }

    async fn ping(&self) -> CoreResult<()> {
        let mut client = self.client.lock();
        let stream = timed(
            "ping failed",
            client.as_mut().unwrap().simple_query("SELECT 1"),
        )?;
        timed("ping failed", stream.into_first_result())?;
        Ok(())
    }

    async fn close(&self) -> CoreResult<()> {
        if let Some(client) = self.client.lock().take() {
            timed("close", client.close())?;
        }
        Ok(())
    }

    fn as_sql(&self) -> Option<&dyn SqlDriver> {
        Some(self)
    }
}

#[async_trait]
impl SqlDriver for MssqlConn {
    async fn browse_rows(
        &self,
        schema: &str,
        table: &str,
        req: &polydb_protocol::TableRowsRequest,
    ) -> CoreResult<polydb_protocol::TableRowsResult> {
        let (sql, args) =
            polydb_db_core::build_rows_query(polydb_db_core::DIALECT_MSSQL, schema, table, req)?;
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
            polydb_db_core::DIALECT_MSSQL,
            schema,
            table,
            req,
        )?;
        let res = self.execute(&sql, &args).await?;
        polydb_db_core::count_result_to_u64(res)
    }

    fn clone_sql_driver_arc(&self) -> Arc<dyn SqlDriver> {
        let arc: Arc<MssqlConn> = self.clone_arc();
        arc
    }

    async fn begin_tx(&self, mode: TxMode) -> CoreResult<Box<dyn Any + Send>> {
        Ok(Box::new(MssqlConn::begin_tx(self, mode)?))
    }

    async fn execute_in_tx(
        &self,
        tx: &mut Box<dyn Any + Send>,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let h = tx
            .downcast_mut::<MssqlTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        MssqlConn::execute_in_tx(self, h, sql, params).await
    }

    async fn commit(&self, tx: &mut Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast_mut::<MssqlTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        MssqlConn::commit(self, h)
    }

    async fn rollback(&self, tx: &mut Box<dyn Any + Send>) -> CoreResult<()> {
        let h = tx
            .downcast_mut::<MssqlTxHandle>()
            .ok_or_else(|| CoreError::Internal("tx handle downcast failed".into()))?;
        MssqlConn::rollback(self, h)
    }

    async fn execute(&self, sql: &str, params: &[Value]) -> CoreResult<QueryResult> {
        let start = std::time::Instant::now();
        let stmt_type = Self::detect_statement_type(sql);
        let boxes = Self::tsql_params(params);
        let refs = Self::param_refs(&boxes);

        let wants_rows = stmt_type == StatementType::Select
            || stmt_type == StatementType::Other
            || has_returning_clause(sql);
        if wants_rows {
            let (rows, metadata) = self.query_rows(sql, &refs, "query failed")?;
            let columns = Self::columns_of(&rows, metadata);
            let data = rows
                .iter()
                .map(|r| (0..r.len()).map(|i| Self::mssql_value(r, i)).collect())
                .collect();
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
            let mut client = self.client.lock();
            let result = timed_within(
                "execute failed",
                STATEMENT_TIMEOUT,
                client.as_mut().unwrap().execute(sql, &refs),
            )?;
            let affected = result.rows_affected().first().copied().unwrap_or(0);
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
    }

    async fn list_schemas(&self) -> CoreResult<Vec<SchemaInfo>> {
        // 系统内置对象按精确名单排除（与 Go 内核 sqltext.SchemasQuery 对齐）：
        // db_ 前缀的 9 个固定数据库角色逐个点名，而不是 NOT LIKE 'db[_]%'——
        // 否则 db_admin 这类以 db_ 开头的用户 schema 会被误藏。
        let (rows, _) = self.query_rows(
            "SELECT s.name FROM sys.schemas s \
             WHERE s.name NOT IN ('sys','INFORMATION_SCHEMA','guest',\
               'db_owner','db_accessadmin','db_securityadmin','db_ddladmin',\
               'db_backupoperator','db_datareader','db_datawriter',\
               'db_denydatareader','db_denydatawriter') \
               AND s.name NOT LIKE '##%' ORDER BY s.name",
            &[],
            "list_schemas",
        )?;
        Ok(rows
            .iter()
            .map(|r| SchemaInfo {
                name: r
                    .try_get::<&str, usize>(0)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
            })
            .collect())
    }

    async fn list_tables(&self, schema: &str) -> CoreResult<Vec<TableInfo>> {
        let (rows, _) = self.query_rows(
            "SELECT t.name, 'table' AS type FROM sys.tables t WHERE t.schema_id = SCHEMA_ID(@P1) \
             UNION ALL \
             SELECT v.name, 'view' FROM sys.views v WHERE v.schema_id = SCHEMA_ID(@P1) \
             ORDER BY name",
            &[&schema],
            "list_tables",
        )?;
        Ok(rows
            .iter()
            .map(|r| TableInfo {
                name: r
                    .try_get::<&str, usize>(0)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
                schema: schema.to_string(),
                table_type: match r.try_get::<&str, usize>(1).ok().flatten() {
                    Some("view") => TableType::View,
                    _ => TableType::Table,
                },
                row_count: None,
                comment: None,
            })
            .collect())
    }

    async fn list_columns(&self, schema: &str, table: &str) -> CoreResult<Vec<ColumnInfo>> {
        let (rows, _) = self.query_rows(
            "SELECT c.name, \
                    CASE WHEN t.name IN ('nvarchar','nchar','varchar','char','binary','varbinary') \
                         THEN t.name + '(' + CASE WHEN c.max_length = -1 THEN 'max' ELSE CAST(c.max_length AS varchar) END + ')' \
                         WHEN t.name IN ('decimal','numeric') \
                         THEN t.name + '(' + CAST(c.precision AS varchar) + ',' + CAST(c.scale AS varchar) + ')' \
                         ELSE t.name END AS data_type, \
                    c.is_nullable, d.definition, c.is_identity, c.column_id \
             FROM sys.columns c \
             JOIN sys.types t ON t.user_type_id = c.user_type_id \
             JOIN sys.tables tb ON tb.object_id = c.object_id \
             JOIN sys.schemas s ON s.schema_id = tb.schema_id \
             LEFT JOIN sys.default_constraints d ON d.object_id = c.default_object_id \
             WHERE s.name = @P1 AND tb.name = @P2 ORDER BY c.column_id",
            &[&schema, &table],
            "list_columns",
        )?;

        let mut cols: Vec<ColumnInfo> = rows
            .iter()
            .map(|r| ColumnInfo {
                name: r
                    .try_get::<&str, usize>(0)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
                data_type: r
                    .try_get::<&str, usize>(1)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
                generic_type: None,
                nullable: r.try_get::<bool, usize>(2).ok().flatten().unwrap_or(false),
                default_value: r
                    .try_get::<&str, usize>(3)
                    .ok()
                    .flatten()
                    .map(|s| s.to_string()),
                max_length: None,
                precision: None,
                scale: None,
                is_primary_key: false,
                is_auto_increment: r.try_get::<bool, usize>(4).ok().flatten().unwrap_or(false),
                comment: None,
                ordinal_position: r.try_get::<i32, usize>(5).ok().flatten().unwrap_or(0),
            })
            .collect();

        let (pk_rows, _) = self.query_rows(
            "SELECT col.name FROM sys.indexes i \
             JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id \
             JOIN sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id \
             JOIN sys.tables tb ON tb.object_id = i.object_id \
             JOIN sys.schemas s ON s.schema_id = tb.schema_id \
             WHERE i.is_primary_key = 1 AND s.name = @P1 AND tb.name = @P2 \
             ORDER BY ic.key_ordinal",
            &[&schema, &table],
            "list_columns pk",
        )?;
        let pk_set: std::collections::HashSet<String> = pk_rows
            .iter()
            .map(|r| {
                r.try_get::<&str, usize>(0)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string()
            })
            .collect();
        for c in cols.iter_mut() {
            if pk_set.contains(&c.name) {
                c.is_primary_key = true;
            }
        }
        Ok(cols)
    }

    async fn list_indexes(&self, schema: &str, table: &str) -> CoreResult<Vec<IndexInfo>> {
        let (rows, _) = self.query_rows(
            "SELECT i.name, i.is_unique, i.is_primary_key, i.type_desc, \
                    ic.key_ordinal, col.name \
             FROM sys.indexes i \
             JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id \
             JOIN sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id \
             JOIN sys.tables tb ON tb.object_id = i.object_id \
             JOIN sys.schemas s ON s.schema_id = tb.schema_id \
             WHERE s.name = @P1 AND tb.name = @P2 ORDER BY i.name, ic.key_ordinal",
            &[&schema, &table],
            "list_indexes",
        )?;

        let mut order: Vec<String> = Vec::new();
        let mut groups: HashMap<String, (bool, bool, String, Vec<IndexColumn>)> = HashMap::new();
        for r in &rows {
            let name = r
                .try_get::<&str, usize>(0)
                .ok()
                .flatten()
                .unwrap_or("")
                .to_string();
            if !groups.contains_key(&name) {
                order.push(name.clone());
                groups.insert(
                    name.clone(),
                    (
                        r.try_get::<bool, usize>(1).ok().flatten().unwrap_or(false),
                        r.try_get::<bool, usize>(2).ok().flatten().unwrap_or(false),
                        r.try_get::<&str, usize>(3)
                            .ok()
                            .flatten()
                            .unwrap_or("")
                            .to_string(),
                        Vec::new(),
                    ),
                );
            }
            groups.get_mut(&name).unwrap().3.push(IndexColumn {
                name: r
                    .try_get::<&str, usize>(5)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
                position: r.try_get::<i32, usize>(4).ok().flatten().unwrap_or(0),
                order: None,
                prefix_length: None,
            });
        }

        Ok(order
            .into_iter()
            .map(|name| {
                let (unique, primary, type_desc, columns) = groups.remove(&name).unwrap();
                IndexInfo {
                    name,
                    unique,
                    primary,
                    index_type: Some(
                        if type_desc.contains("CLUSTERED") || type_desc.contains("NONCLUSTERED") {
                            IndexType::Btree
                        } else {
                            IndexType::Other
                        },
                    ),
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
        let (rows, _) = self.query_rows(
            "SELECT fk.name, c.name, rs.name, rt.name, rc.name \
             FROM sys.foreign_keys fk \
             JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id \
             JOIN sys.columns c ON c.object_id = fkc.parent_object_id AND c.column_id = fkc.parent_column_id \
             JOIN sys.tables rt ON rt.object_id = fk.referenced_object_id \
             JOIN sys.schemas rs ON rs.schema_id = rt.schema_id \
             JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id \
             JOIN sys.tables tb ON tb.object_id = fk.parent_object_id \
             JOIN sys.schemas s ON s.schema_id = tb.schema_id \
             WHERE s.name = @P1 AND tb.name = @P2 \
             ORDER BY fk.name, fkc.constraint_column_id",
            &[&schema, &table],
            "list_foreign_keys",
        )?;

        let mut order: Vec<String> = Vec::new();
        let mut groups: HashMap<String, (String, String, Vec<String>, Vec<String>)> =
            HashMap::new();
        for r in &rows {
            let name = r
                .try_get::<&str, usize>(0)
                .ok()
                .flatten()
                .unwrap_or("")
                .to_string();
            if !groups.contains_key(&name) {
                order.push(name.clone());
                groups.insert(
                    name.clone(),
                    (
                        r.try_get::<&str, usize>(2)
                            .ok()
                            .flatten()
                            .unwrap_or("")
                            .to_string(),
                        r.try_get::<&str, usize>(3)
                            .ok()
                            .flatten()
                            .unwrap_or("")
                            .to_string(),
                        Vec::new(),
                        Vec::new(),
                    ),
                );
            }
            let group = groups.get_mut(&name).unwrap();
            group.2.push(
                r.try_get::<&str, usize>(1)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
            );
            group.3.push(
                r.try_get::<&str, usize>(4)
                    .ok()
                    .flatten()
                    .unwrap_or("")
                    .to_string(),
            );
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
            } else if col.is_auto_increment {
                " IDENTITY(1,1)"
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

#[cfg(test)]
mod tests {
    use super::*;
    use tiberius::ColumnType as T;

    /// 列类型名是 **大写**的 SQL 拼写——与 Go 侧 go-mssqldb 的
    /// `makeGoLangTypeName`（types.go:1359 起）逐字一致（其文档明写
    /// "Type names should be uppercase"）。
    ///
    /// PD-22（2026-11-10）：此前本函数给**小写**（"int"/"nvarchar"…），
    /// 于是两端对同一列给出不同拼写，契约测试对 net 场景只能用
    /// `onlyColumnNames` 把列类型整个裁掉。
    #[test]
    fn column_type_names_are_sql_spellings() {
        assert_eq!(column_type_name(T::Int4), "INT");
        assert_eq!(column_type_name(T::Int8), "BIGINT");
        assert_eq!(column_type_name(T::Int1), "TINYINT");
        assert_eq!(column_type_name(T::Int2), "SMALLINT");
        assert_eq!(column_type_name(T::Bit), "BIT");
        assert_eq!(column_type_name(T::Bitn), "BIT");
        assert_eq!(column_type_name(T::Float4), "REAL");
        assert_eq!(column_type_name(T::Float8), "FLOAT");
        assert_eq!(column_type_name(T::NVarchar), "NVARCHAR");
        assert_eq!(column_type_name(T::NChar), "NCHAR");
        assert_eq!(column_type_name(T::BigVarChar), "VARCHAR");
        assert_eq!(column_type_name(T::BigVarBin), "VARBINARY");
        // Go 侧 `case typeDecimalN, typeNumericN` 统一返回 "DECIMAL"，
        // 故此处 Decimaln 与 Numericn 同形。
        assert_eq!(column_type_name(T::Decimaln), "DECIMAL");
        assert_eq!(column_type_name(T::Numericn), "DECIMAL");
        assert_eq!(column_type_name(T::Datetime2), "DATETIME2");
        assert_eq!(column_type_name(T::Daten), "DATE");
        assert_eq!(column_type_name(T::Timen), "TIME");
        assert_eq!(column_type_name(T::Guid), "UNIQUEIDENTIFIER");
        assert_eq!(column_type_name(T::Money), "MONEY");
        assert_eq!(column_type_name(T::Xml), "XML");
        assert_eq!(column_type_name(T::SSVariant), "SQL_VARIANT");
        assert_eq!(column_type_name(T::Null), "");
    }

    /// RUST-MSSQL-EMPTY-COLUMNS：空结果集时列元数据只能来自 stream 的
    /// QueryItem::Metadata（TDS 协议保证它先于行数据到达；空集时是唯一
    /// 携带列信息的 item）。columns_of 的空 rows + metadata 兜底路径。
    #[test]
    fn columns_of_falls_back_to_metadata_when_rows_empty() {
        let cols = vec![
            tiberius::Column::new("id".into(), T::Int8),
            tiberius::Column::new("name".into(), T::BigVarChar),
        ];
        let meta = Some(MssqlConn::result_columns_from(&cols));
        let out = MssqlConn::columns_of(&[], meta);
        assert_eq!(out.len(), 2);
        assert_eq!(out[0].name, "id");
        assert_eq!(out[0].data_type, "BIGINT");
        assert_eq!(out[1].name, "name");
        assert_eq!(out[1].data_type, "VARCHAR");

        // 无 Metadata 也无行 → 空列（与修复前行为一致，仅多了一条兜底路径）。
        let out2 = MssqlConn::columns_of(&[], None);
        assert!(out2.is_empty());
    }

    /// Array/Object 参数跨线协议统一为 JSON 文本（P1-4 口径钉子）：此前兜底
    /// 成 0i64，把 JSON/blob 列的数据静默写成 0。
    #[test]
    fn tsql_params_bind_array_and_object_as_json_text() {
        let arr = Value::Array(vec![Value::Integer(1), Value::String("x".into())]);
        let blob = polydb_db_core::blob_value(&[0u8, 1, 2]);
        assert_eq!(MssqlConn::json_text_param(&arr), r#"[1,"x"]"#);
        // $blob 对象按 JSON 文本走（与 Go 侧 BlobValue 的 JSON 形态同构）。
        let s = MssqlConn::json_text_param(&blob);
        assert!(
            s.starts_with('{') && s.contains("$blob"),
            "blob bind = {s:?}"
        );
        // 标量不受影响（match 分支仍各自绑定）。
        let binds = MssqlConn::tsql_params(&[Value::Integer(7)]);
        assert_eq!(binds.len(), 1);
    }

    /// P2-3 回归：Array/Object 中的 NaN/Inf 必须序列化为字符串值，
    /// 不能被 serde_json 静默转为 null（数据丢失）。
    #[test]
    fn tsql_params_array_with_nan_inf_serializes_as_string() {
        let arr = Value::Array(vec![
            Value::Float(1.5),
            Value::Float(f64::NAN),
            Value::Float(f64::INFINITY),
        ]);
        let json = MssqlConn::json_text_param(&arr);
        let parsed: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed[0], 1.5);
        assert_eq!(
            parsed[1], "NaN",
            "NaN must be preserved as string, not null"
        );
        assert_eq!(parsed[2], "Infinity");
    }
    /// PD-18（2026-11-10）回归（读取链）：`mssql_value` 必须含 UNIQUEIDENTIFIER
    /// 分支，且产出标准 UUID 文本。
    ///
    /// 缺陷：tiberius 的 `&[u8]`/`Vec<u8>` 只匹配 ColumnData::Binary
    /// （tiberius from_sql.rs:114-124），Guid 列不匹配；旧读取链没有
    /// `try_get::<Uuid>` 分支——UNIQUEIDENTIFIER 列整条链落空，掉到链尾的
    /// `Value::Null`（数据丢失）。Go 侧旧链把 decodeGuid 的 16 字节当普通
    /// 字节串输出乱码。两端现已统一为标准小写 UUID 文本。
    ///
    /// tiberius 的 ColumnData 是 pub(crate)，无法离线构造 Guid 列值，故与
    /// PD-17 同法：源码形态断言钉住读取链 + 契约测试做端到端（无服务时
    /// soft-skip）。
    #[test]
    fn mssql_value_read_chain_has_uuid_branch() {
        let src = include_str!("lib.rs");

        // Uuid 分支必须存在，且产出 String 文本（不得是 Null/占位符）。
        let pos = src
            .find("row.try_get::<tiberius::Uuid, usize>(i)")
            .unwrap_or_else(|| panic!("读取链缺少 UNIQUEIDENTIFIER (Uuid) 分支"));

        // 分支体内必须是 v.to_string()（标准 UUID 文本）。
        let after = &src[pos..];
        let body_end = after.find("}").expect("Uuid 分支未闭合");
        let body = &after[..body_end];
        assert!(
            body.contains("Value::String(v.to_string())"),
            "Uuid 分支必须产出 Value::String(v.to_string())，实际：{body}"
        );

        // 链尾兜底是 Value::Null —— Uuid 分支必须在它之前。
        let tail = src
            .rfind("        Value::Null\n    }")
            .expect("缺少链尾 Null 兜底");
        assert!(
            pos < tail,
            "Uuid 分支必须排在链尾 Value::Null 之前（否则 GUID 落到 Null，数据丢失）"
        );
    }
}
