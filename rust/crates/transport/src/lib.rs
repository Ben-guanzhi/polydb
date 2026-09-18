use std::sync::Arc;

use async_trait::async_trait;
use polydb_app_core::AppCore;
use polydb_core::{
    ColumnInfo, ConnectionId, ConnectionInfo, ConnectionStatus, CoreResult,
    CreateConnectionRequest, ForeignKeyInfo, IndexInfo, QueryResult, SchemaInfo, TableInfo,
    UpdateConnectionRequest, Value,
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
}
