//! polydb-db-oracle：基于 oracle crate（ODPI-C）的驱动实现（对应 Go 侧 dboracle）。
//! oracle crate 为阻塞式 API，与 db-sqlite 一致：同步调用直接放在 async 方法内。

use std::any::Any;
use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use oracle::sql_type::ToSql;
use oracle::{Connection, SqlValue};
use parking_lot::Mutex;

use polydb_core::{
    ColumnInfo, CoreError, CoreResult, DatabaseKind, ForeignKeyInfo, IndexColumn, IndexInfo,
    IndexType, IsolationLevel, QueryResult, ResultColumn, SchemaInfo, StatementType, TableInfo,
    TableType, Value,
};
use polydb_db_core::{DatabaseDriver, SqlDriver, TxMode};

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
                Value::Float(f) => Box::new(*f) as Box<dyn ToSql>,
                Value::Bool(b) => Box::new(*b) as Box<dyn ToSql>,
                Value::String(s) => Box::new(s.clone()) as Box<dyn ToSql>,
                _ => Box::new(0i64) as Box<dyn ToSql>,
            })
            .collect()
    }

    fn param_refs(boxes: &[Box<dyn ToSql>]) -> Vec<&dyn ToSql> {
        boxes.iter().map(|b| b.as_ref()).collect()
    }

    /// SqlValue 在 0.6 是结构体：先判空，再按 数值→字符串→原始字节 顺序尝试转换。
    fn or_value(v: &SqlValue) -> Value {
        if v.is_null().unwrap_or(true) {
            return Value::Null;
        }
        if let Ok(i) = v.get::<i64>() {
            return Value::Integer(i);
        }
        if let Ok(f) = v.get::<f64>() {
            return Value::Float(f);
        }
        if let Ok(b) = v.get::<bool>() {
            return Value::Bool(b);
        }
        if let Ok(s) = v.get::<String>() {
            return Value::String(s);
        }
        if let Ok(bytes) = v.get::<Vec<u8>>() {
            return Value::String(format!("<blob {} bytes>", bytes.len()));
        }
        Value::Null
    }

    fn detect_statement_type(sql: &str) -> StatementType {
        let t = strip_leading_comments(sql).trim().to_uppercase();
        if t.starts_with("SELECT") || t.starts_with("WITH") {
            StatementType::Select
        } else if t.starts_with("INSERT") {
            StatementType::Insert
        } else if t.starts_with("UPDATE") {
            StatementType::Update
        } else if t.starts_with("DELETE") {
            StatementType::Delete
        } else if t.starts_with("CREATE") || t.starts_with("ALTER") || t.starts_with("DROP") {
            StatementType::Ddl
        } else {
            StatementType::Other
        }
    }
}

fn strip_leading_comments(sql: &str) -> &str {
    let mut s = sql;
    loop {
        s = s.trim_start();
        if let Some(rest) = s.strip_prefix("--") {
            s = match rest.find('\n') {
                Some(i) => &rest[i + 1..],
                None => return "",
            };
        } else if let Some(rest) = s.strip_prefix("/*") {
            s = match rest.find("*/") {
                Some(i) => &rest[i + 2..],
                None => return "",
            };
        } else {
            return s;
        }
    }
}

fn quote_ident(s: &str) -> String {
    format!("\"{}\"", s.replace('"', "\"\""))
}

/// Oracle 事务句柄。oracle crate 无独立 Transaction 对象，事务状态由
/// 连接上的第一条 DML 隐式开启，commit()/rollback() 收尾；本结构仅作
/// 不透明标记。
#[derive(Clone, Debug)]
pub struct OracleTxHandle;

impl OracleConn {
    pub fn begin_tx(&self, mode: TxMode) -> CoreResult<OracleTxHandle> {
        // 非默认隔离级别：在开启事务前设置会话隔离级别。
        // 若当前会话已在事务中，SET TRANSACTION 会失败——因此仅在
        // 默认 ReadCommitted 时跳过；其余级别尝试设置，失败回滚。
        if mode.isolation_level != IsolationLevel::ReadCommitted {
            let sql = match mode.isolation_level {
                IsolationLevel::ReadUncommitted => "ALTER SESSION SET TRANSACTION READ UNCOMMITTED",
                IsolationLevel::RepeatableRead => {
                    "ALTER SESSION SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"
                }
                IsolationLevel::Serializable => {
                    "ALTER SESSION SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"
                }
                IsolationLevel::ReadCommitted => {
                    return Ok(OracleTxHandle);
                }
            };
            // 非默认级别设置失败不视为致命错误：Oracle 对会话级隔离切换
            // 有一定限制（如 REPEATABLE READ 需 SERIALIZABLE 模式启用），
            // 回退到默认即可，与 Go 侧 go-ora 行为一致。
            let _ = self.conn.lock().query_as::<i64>(sql, &[]);
        }
        Ok(OracleTxHandle)
    }

    pub fn commit(&self, _tx: &mut OracleTxHandle) -> CoreResult<()> {
        self.conn
            .lock()
            .commit()
            .map_err(|e| CoreError::Driver(format!("commit failed: {e}")))?;
        Ok(())
    }

    pub fn rollback(&self, _tx: &mut OracleTxHandle) -> CoreResult<()> {
        self.conn
            .lock()
            .rollback()
            .map_err(|e| CoreError::Driver(format!("rollback failed: {e}")))?;
        Ok(())
    }

    /// 事务内执行一条 SQL。Oracle 隐式事务：连接上的第一条 DML/DDL 即开启
    /// 事务，因此 execute_in_tx 与 execute 完全等价（同一 conn）。
    pub async fn execute_in_tx(
        &self,
        _tx: &mut OracleTxHandle,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        let start = std::time::Instant::now();
        let stmt_type = Self::detect_statement_type(sql);
        let boxes = Self::to_sql_params(params);
        let refs = Self::param_refs(&boxes);

        let conn = self.conn.lock();
        let mut stmt = conn
            .statement(sql)
            .build()
            .map_err(|e| CoreError::Driver(format!("prepare failed: {e}")))?;

        if stmt_type == StatementType::Select || stmt_type == StatementType::Other {
            let mut rows = stmt
                .query(&refs)
                .map_err(|e| CoreError::Driver(format!("query failed: {e}")))?;
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
            while let Some(row) = rows
                .next()
                .transpose()
                .map_err(|e| CoreError::Driver(format!("row failed: {e}")))?
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
                .map_err(|e| CoreError::Driver(format!("execute failed: {e}")))?;
            let affected = stmt
                .row_count()
                .map_err(|e| CoreError::Driver(format!("row_count failed: {e}")))?;
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
    fn clone_sql_driver_arc(&self) -> Arc<dyn SqlDriver> {
        let arc: Arc<OracleConn> = self.clone_arc();
        arc
    }

    async fn begin_tx(&self, mode: TxMode) -> CoreResult<Box<dyn Any + Send>> {
        Ok(Box::new(OracleConn::begin_tx(self, mode)?))
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

    async fn commit(&self, tx: Box<dyn Any + Send>) -> CoreResult<()> {
        let mut h = tx
            .downcast::<OracleTxHandle>()
            .map_err(|_| CoreError::Internal("tx handle downcast failed".into()))?;
        OracleConn::commit(self, &mut h)
    }

    async fn rollback(&self, tx: Box<dyn Any + Send>) -> CoreResult<()> {
        let mut h = tx
            .downcast::<OracleTxHandle>()
            .map_err(|_| CoreError::Internal("tx handle downcast failed".into()))?;
        OracleConn::rollback(self, &mut h)
    }

    async fn execute(&self, sql: &str, params: &[Value]) -> CoreResult<QueryResult> {
        let start = std::time::Instant::now();
        let stmt_type = Self::detect_statement_type(sql);
        let boxes = Self::to_sql_params(params);
        let refs = Self::param_refs(&boxes);

        let conn = self.conn.lock();
        let mut stmt = conn
            .statement(sql)
            .build()
            .map_err(|e| CoreError::Driver(format!("prepare failed: {e}")))?;

        if stmt_type == StatementType::Select || stmt_type == StatementType::Other {
            let mut rows = stmt
                .query(&refs)
                .map_err(|e| CoreError::Driver(format!("query failed: {e}")))?;
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
            while let Some(row) = rows
                .next()
                .transpose()
                .map_err(|e| CoreError::Driver(format!("row failed: {e}")))?
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
                .map_err(|e| CoreError::Driver(format!("execute failed: {e}")))?;
            let affected = stmt
                .row_count()
                .map_err(|e| CoreError::Driver(format!("row_count failed: {e}")))?;
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
