use std::sync::Arc;

use async_trait::async_trait;
use polydb_app_core::AppCore;
use polydb_core::{
    BeginTransactionRequest, ColumnInfo, ConnectionId, ConnectionInfo, ConnectionStatus,
    CoreResult, CreateConnectionRequest, ForeignKeyInfo, IndexInfo, QueryResult, RedisKeyType,
    RedisReply, RedisScanPage, RedisValue, SchemaInfo, TableInfo, TableRowsRequest,
    TableRowsResult, TransactionInfo, UpdateConnectionRequest, Value,
};

#[async_trait]
pub trait Transport: Send + Sync {
    fn create_connection(&self, req: &CreateConnectionRequest) -> CoreResult<ConnectionInfo>;
    fn list_connections(&self) -> CoreResult<Vec<ConnectionInfo>>;
    fn get_connection_info(&self, id: ConnectionId) -> CoreResult<Option<ConnectionInfo>>;
    fn update_connection(
        &self,
        id: ConnectionId,
        req: &UpdateConnectionRequest,
    ) -> CoreResult<Option<ConnectionInfo>>;
    fn delete_connection(&self, id: ConnectionId) -> CoreResult<bool>;
    fn connect(&self, id: ConnectionId) -> CoreResult<()>;
    fn disconnect(&self, id: ConnectionId);
    fn connection_status(&self, id: ConnectionId) -> CoreResult<ConnectionStatus>;
    async fn ping(&self, id: ConnectionId) -> CoreResult<()>;
    async fn execute(
        &self,
        id: ConnectionId,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult>;
    async fn list_schemas(&self, id: ConnectionId) -> CoreResult<Vec<SchemaInfo>>;
    async fn list_tables(&self, id: ConnectionId, schema: &str) -> CoreResult<Vec<TableInfo>>;
    async fn list_columns(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
    ) -> CoreResult<Vec<ColumnInfo>>;
    async fn list_indexes(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
    ) -> CoreResult<Vec<IndexInfo>>;
    async fn list_foreign_keys(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
    ) -> CoreResult<Vec<ForeignKeyInfo>>;
    async fn create_table_sql(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
    ) -> CoreResult<String>;

    // ─── 表数据浏览（M11）──────────────────────────────────
    async fn browse_rows(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
        req: &TableRowsRequest,
    ) -> CoreResult<TableRowsResult>;
    async fn browse_rows_count(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
        req: &TableRowsRequest,
    ) -> CoreResult<u64>;

    // ─── Redis KV（M6）─────────────────────────────────────
    async fn select_db(&self, id: ConnectionId, index: u32) -> CoreResult<()>;
    async fn scan_keys(
        &self,
        id: ConnectionId,
        cursor: u64,
        pattern: &str,
        count: u32,
    ) -> CoreResult<RedisScanPage>;
    async fn key_type(&self, id: ConnectionId, key: &str) -> CoreResult<RedisKeyType>;
    async fn get_value(&self, id: ConnectionId, key: &str) -> CoreResult<RedisValue>;
    async fn set_value(&self, id: ConnectionId, key: &str, value: RedisValue) -> CoreResult<()>;
    async fn exec_command(&self, id: ConnectionId, args: &[String]) -> CoreResult<RedisReply>;

    // ─── 事务（M25）────────────────────────────────────────
    async fn begin_transaction(&self, req: &BeginTransactionRequest)
        -> CoreResult<TransactionInfo>;
    async fn execute_in_transaction(
        &self,
        txn_id: &str,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult>;
    async fn commit_transaction(&self, txn_id: &str) -> CoreResult<TransactionInfo>;
    async fn rollback_transaction(&self, txn_id: &str) -> CoreResult<TransactionInfo>;
}

pub struct LocalTransport {
    app: Arc<AppCore>,
}

impl LocalTransport {
    pub fn new(app: Arc<AppCore>) -> Self {
        Self { app }
    }
}

#[async_trait]
impl Transport for LocalTransport {
    fn create_connection(&self, req: &CreateConnectionRequest) -> CoreResult<ConnectionInfo> {
        self.app.create_connection(req)
    }

    fn list_connections(&self) -> CoreResult<Vec<ConnectionInfo>> {
        self.app.list_connections()
    }

    fn get_connection_info(&self, id: ConnectionId) -> CoreResult<Option<ConnectionInfo>> {
        self.app.get_connection_info(id)
    }

    fn update_connection(
        &self,
        id: ConnectionId,
        req: &UpdateConnectionRequest,
    ) -> CoreResult<Option<ConnectionInfo>> {
        self.app.update_connection(id, req)
    }

    fn delete_connection(&self, id: ConnectionId) -> CoreResult<bool> {
        self.app.delete_connection(id)
    }

    fn connect(&self, id: ConnectionId) -> CoreResult<()> {
        self.app.connect(id)
    }

    fn disconnect(&self, id: ConnectionId) {
        self.app.disconnect(id)
    }

    fn connection_status(&self, id: ConnectionId) -> CoreResult<ConnectionStatus> {
        self.app.connection_status(id)
    }

    async fn ping(&self, id: ConnectionId) -> CoreResult<()> {
        self.app.ping(id).await
    }

    async fn execute(
        &self,
        id: ConnectionId,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        self.app.execute(id, sql, params).await
    }

    async fn list_schemas(&self, id: ConnectionId) -> CoreResult<Vec<SchemaInfo>> {
        self.app.list_schemas(id).await
    }

    async fn list_tables(&self, id: ConnectionId, schema: &str) -> CoreResult<Vec<TableInfo>> {
        self.app.list_tables(id, schema).await
    }

    async fn list_columns(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
    ) -> CoreResult<Vec<ColumnInfo>> {
        self.app.list_columns(id, schema, table).await
    }

    async fn list_indexes(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
    ) -> CoreResult<Vec<IndexInfo>> {
        self.app.list_indexes(id, schema, table).await
    }

    async fn list_foreign_keys(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
    ) -> CoreResult<Vec<ForeignKeyInfo>> {
        self.app.list_foreign_keys(id, schema, table).await
    }

    async fn create_table_sql(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
    ) -> CoreResult<String> {
        self.app.create_table_sql(id, schema, table).await
    }

    // ─── 表数据浏览（M11）──────────────────────────────────
    async fn browse_rows(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
        req: &TableRowsRequest,
    ) -> CoreResult<TableRowsResult> {
        self.app.browse_rows(id, schema, table, req).await
    }

    async fn browse_rows_count(
        &self,
        id: ConnectionId,
        schema: &str,
        table: &str,
        req: &TableRowsRequest,
    ) -> CoreResult<u64> {
        self.app.browse_rows_count(id, schema, table, req).await
    }

    // ─── Redis KV（M6）─────────────────────────────────────
    async fn select_db(&self, id: ConnectionId, index: u32) -> CoreResult<()> {
        self.app.select_db(id, index).await
    }

    async fn scan_keys(
        &self,
        id: ConnectionId,
        cursor: u64,
        pattern: &str,
        count: u32,
    ) -> CoreResult<RedisScanPage> {
        self.app.scan_keys(id, cursor, pattern, count).await
    }

    async fn key_type(&self, id: ConnectionId, key: &str) -> CoreResult<RedisKeyType> {
        self.app.key_type(id, key).await
    }

    async fn get_value(&self, id: ConnectionId, key: &str) -> CoreResult<RedisValue> {
        self.app.get_value(id, key).await
    }

    async fn set_value(&self, id: ConnectionId, key: &str, value: RedisValue) -> CoreResult<()> {
        self.app.set_value(id, key, value).await
    }

    async fn exec_command(&self, id: ConnectionId, args: &[String]) -> CoreResult<RedisReply> {
        self.app.exec_command(id, args).await
    }

    // ─── 事务（M25）────────────────────────────────────────
    async fn begin_transaction(
        &self,
        req: &BeginTransactionRequest,
    ) -> CoreResult<TransactionInfo> {
        self.app.begin_transaction(req).await
    }

    async fn execute_in_transaction(
        &self,
        txn_id: &str,
        sql: &str,
        params: &[Value],
    ) -> CoreResult<QueryResult> {
        self.app.execute_in_transaction(txn_id, sql, params).await
    }

    async fn commit_transaction(&self, txn_id: &str) -> CoreResult<TransactionInfo> {
        self.app.commit_transaction(txn_id).await
    }

    async fn rollback_transaction(&self, txn_id: &str) -> CoreResult<TransactionInfo> {
        self.app.rollback_transaction(txn_id).await
    }
}
