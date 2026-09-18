use std::sync::Arc;

use polydb_core::{CoreError, CoreResult, DatabaseKind};

use crate::driver::{DatabaseDriver, KvDriver, SqlDriver};

#[derive(Clone)]
pub struct Connection {
    driver: Arc<dyn DatabaseDriver>,
}

impl Connection {
    pub fn new(driver: Arc<dyn DatabaseDriver>) -> Self {
        Self { driver }
    }

    pub fn kind(&self) -> DatabaseKind {
        self.driver.kind()
    }

    pub fn driver(&self) -> &dyn DatabaseDriver {
        self.driver.as_ref()
    }

    pub fn driver_arc(&self) -> Arc<dyn DatabaseDriver> {
        Arc::clone(&self.driver)
    }

    pub fn as_sql(&self) -> CoreResult<&dyn SqlDriver> {
        self.driver.as_sql().ok_or_else(|| {
            CoreError::NotSupported(format!("{} is not a SQL database", self.driver.kind()))
        })
    }

    /// 返回指向 SQL 驱动的共享 Arc；用于让上层（app-core）在脱离
    /// Connection 借用的生命周期内继续持有具体驱动的连接引用。
    pub fn sql_driver_arc(&self) -> CoreResult<Arc<dyn SqlDriver>> {
        let sql = self.driver.as_sql().ok_or_else(|| {
            CoreError::NotSupported(format!("{} is not a SQL database", self.driver.kind()))
        })?;
        Ok(sql.clone_sql_driver_arc())
    }

    pub fn as_kv(&self) -> CoreResult<&dyn KvDriver> {
        self.driver.as_kv().ok_or_else(|| {
            CoreError::NotSupported(format!("{} is not a KV database", self.driver.kind()))
        })
    }
}
