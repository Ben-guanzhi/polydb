use polydb_protocol::error::PolyDBError;

#[derive(Debug, thiserror::Error)]
pub enum CoreError {
    #[error(transparent)]
    Protocol(#[from] Box<PolyDBError>),

    #[error("connection not found: {0}")]
    ConnectionNotFound(String),

    #[error("transaction not found: {0}")]
    TransactionNotFound(String),

    #[error("driver error: {0}")]
    Driver(String),

    #[error("connection failed: {0}")]
    Connection(String),

    #[error("storage error: {0}")]
    Storage(String),

    #[error("keyring error: {0}")]
    Keyring(String),

    #[error("ssh tunnel error: {0}")]
    SshTunnel(String),

    #[error("not supported: {0}")]
    NotSupported(String),

    #[error("internal error: {0}")]
    Internal(String),
}

impl From<PolyDBError> for CoreError {
    fn from(e: PolyDBError) -> Self {
        CoreError::Protocol(Box::new(e))
    }
}

impl CoreError {
    pub fn to_protocol(&self) -> PolyDBError {
        match self {
            Self::Protocol(e) => (**e).clone(),
            Self::ConnectionNotFound(id) => PolyDBError::new(
                "POLYDB_ERR_CONNECTION_NOT_FOUND",
                format!("connection not found: {id}"),
            ),
            Self::TransactionNotFound(id) => PolyDBError::new(
                "POLYDB_ERR_TRANSACTION_NOT_FOUND",
                format!("transaction not found: {id}"),
            ),
            Self::Driver(msg) => PolyDBError::new("POLYDB_ERR_QUERY_FAILED", msg.clone()),
            Self::Connection(msg) => {
                PolyDBError::new("POLYDB_ERR_CONNECTION_FAILED", msg.clone()).retryable()
            }
            Self::Storage(msg) => PolyDBError::new("POLYDB_ERR_STORAGE_FAILED", msg.clone()),
            Self::Keyring(msg) => PolyDBError::new("POLYDB_ERR_KEYRING_FAILED", msg.clone()),
            Self::SshTunnel(msg) => {
                PolyDBError::new("POLYDB_ERR_SSH_TUNNEL_FAILED", msg.clone()).retryable()
            }
            Self::NotSupported(msg) => PolyDBError::new("POLYDB_ERR_NOT_SUPPORTED", msg.clone()),
            Self::Internal(msg) => PolyDBError::new("POLYDB_ERR_UNKNOWN", msg.clone()),
        }
    }
}

pub type CoreResult<T> = Result<T, CoreError>;
