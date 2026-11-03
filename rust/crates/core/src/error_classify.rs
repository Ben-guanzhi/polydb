//! 驱动错误分类：镜像 Go 侧 dbcore/dberror 的分类表（SQLSTATE / 数字码 /
//! 文本模式三层证据），把驱动错误映射为可编程的协议码面。六驱动此前一律
//! POLYDB_ERR_QUERY_FAILED + 裸文本，DUPLICATE_KEY/DEADLOCK/TIMEOUT 等码永远
//! 产不出，客户端拿不到可编程的错误分类。

use polydb_protocol::error::codes;
use serde_json::Value;

use crate::CoreError;

// 证据最具体的优先：SQLSTATE 是五引擎共识的分类（PG/MySQL 直接暴露），
// 数字码是 MSSQL/Oracle/MySQL 的引擎本地编号，文本模式是 SQLite 与无码
// 情况的最后兜底。
pub fn driver_error(
    sqlstate: Option<&str>,
    numeric: Option<u32>,
    kind: Option<DriverClass>,
    text: &str,
) -> CoreError {
    let classified: Option<(String, bool)> = if let Some(state) = sqlstate {
        class_from_sqlstate(state)
    } else if let Some(n) = numeric {
        class_from_numeric(n)
    } else if let Some(k) = kind {
        Some(k.into_pair())
    } else {
        class_from_text(text)
    };

    match classified {
        Some((code, retryable)) => CoreError::DriverClassified {
            code: code.into_boxed_str(),
            message: text.to_string().into_boxed_str(),
            detail: sqlstate.map(|s| {
                Value::Object(serde_json::Map::from_iter([(
                    "sqlstate".to_string(),
                    Value::String(s.to_string()),
                )]))
            }),
            retryable,
        },
        None => CoreError::Driver(text.to_string()),
    }
}

// sqlx（MySQL/PostgreSQL）的 kind() 提供的分类证据，由驱动本地映射
// （sqlx 的 ErrorKind 只在 sqlx 里，core 不做驱动专属的类型引用）。
pub enum DriverClass {
    Duplicate,
    Constraint,
}

impl DriverClass {
    fn into_pair(self) -> (String, bool) {
        match self {
            DriverClass::Duplicate => (codes::DUPLICATE_KEY.into(), false),
            DriverClass::Constraint => (codes::CONSTRAINT_VIOLATION.into(), false),
        }
    }
}

fn class_from_sqlstate(state: &str) -> Option<(String, bool)> {
    Some(match state {
        "23505" => (codes::DUPLICATE_KEY.into(), false),
        "23503" | "23502" | "23514" => (codes::CONSTRAINT_VIOLATION.into(), false),
        "40P01" => (codes::DEADLOCK.into(), true),
        "40001" | "2D000" => (codes::TRANSACTION_FAILED.into(), true),
        "57014" => (codes::CANCELLED.into(), false),
        "HYT00" | "HYT01" => (codes::TIMEOUT.into(), true),
        _ if state.starts_with("08") => (codes::CONNECTION_FAILED.into(), true),
        _ => return None,
    })
}

fn class_from_numeric(n: u32) -> Option<(String, bool)> {
    Some(match n {
        // Oracle ORA-XXXXX 的 XXXXX，与 MSSQL 的 numeric error code。
        1 => (codes::DUPLICATE_KEY.into(), false), // ORA-00001
        60 => (codes::DEADLOCK.into(), true),      // ORA-00060
        2601 | 2627 => (codes::DUPLICATE_KEY.into(), false), // MSSQL 2601/2627
        1205 => (codes::DEADLOCK.into(), true),    // MSSQL 死锁
        547 => (codes::CONSTRAINT_VIOLATION.into(), false), // MSSQL FK
        2270 | 2290 | 2292 | 1400 => (codes::CONSTRAINT_VIOLATION.into(), false),
        8177 => (codes::TRANSACTION_FAILED.into(), true), // ORA-08177
        3113 | 3114 | 12545 | 1012 => (codes::CONNECTION_FAILED.into(), true),
        8003 | 8006 => (codes::CONNECTION_FAILED.into(), true), // MSSQL
        _ => return None,
    })
}

fn class_from_text(t: &str) -> Option<(String, bool)> {
    let upper = t.to_uppercase();
    if upper.contains("UNIQUE CONSTRAINT FAILED")
        || upper.contains("DUPLICATE ENTRY")
        || upper.contains("ERROR 1062")
        || upper.contains("VIOLATION OF UNIQUE KEY")
        || upper.contains("CANNOT INSERT DUPLICATE KEY")
        || upper.contains("DUPLICATE KEY")
        || upper.contains("ORA-00001")
    {
        return Some((codes::DUPLICATE_KEY.into(), false));
    }
    if upper.contains("FOREIGN KEY CONSTRAINT FAILED")
        || upper.contains("NOT NULL CONSTRAINT FAILED")
        || upper.contains("CHECK CONSTRAINT FAILED")
        || upper.contains("FOREIGN KEY")
        || upper.contains("CONSTRAINT VIOLATION")
        || upper.contains("ORA-02292")
        || upper.contains("ORA-02270")
        || upper.contains("ORA-01400")
        || upper.contains("ORA-02290")
    {
        return Some((codes::CONSTRAINT_VIOLATION.into(), false));
    }
    if upper.contains("DATABASE IS LOCKED")
        || upper.contains("DEADLOCK")
        || upper.contains("LOCK WAIT TIMEOUT")
    {
        return Some((codes::DEADLOCK.into(), true));
    }
    if upper.contains("COULD NOT SERIALIZE") || upper.contains("ORA-08177") {
        return Some((codes::TRANSACTION_FAILED.into(), true));
    }
    if upper.contains("SERVER HAS GONE AWAY")
        || upper.contains("LOST CONNECTION")
        || upper.contains("ORA-03113")
        || upper.contains("ORA-03114")
        || upper.contains("ORA-12545")
        || upper.contains("NOT LOGGED ON")
    {
        return Some((codes::CONNECTION_FAILED.into(), true));
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sqlstate_maps_to_protocol_codes() {
        let e = driver_error(Some("23505"), None, None, "UNIQUE violation");
        match e {
            CoreError::DriverClassified {
                code, retryable, ..
            } => {
                assert_eq!(code.as_ref(), codes::DUPLICATE_KEY);
                assert!(!retryable);
            }
            other => panic!("expected classified, got {other:?}"),
        }
        let e = driver_error(Some("40P01"), None, None, "deadlock");
        match e {
            CoreError::DriverClassified {
                code, retryable, ..
            } => {
                assert_eq!(code.as_ref(), codes::DEADLOCK);
                assert!(retryable);
            }
            other => panic!("expected classified, got {other:?}"),
        }
        // Class 08 prefix = connection class, anywhere in the five chars.
        let e = driver_error(Some("08006"), None, None, "connection lost");
        match e {
            CoreError::DriverClassified {
                code, retryable, ..
            } => {
                assert_eq!(code.as_ref(), codes::CONNECTION_FAILED);
                assert!(retryable);
            }
            other => panic!("expected classified, got {other:?}"),
        }
    }

    #[test]
    fn numeric_and_text_evidence_fill_the_gap() {
        let e = driver_error(None, Some(1205), None, "lock wait timed out");
        match e {
            CoreError::DriverClassified {
                code, retryable, ..
            } => {
                assert_eq!(code.as_ref(), codes::DEADLOCK);
                assert!(retryable);
            }
            other => panic!("expected classified, got {other:?}"),
        }
        let e = driver_error(None, None, None, "UNIQUE constraint failed: t.id");
        match e {
            CoreError::DriverClassified { code, .. } => {
                assert_eq!(code.as_ref(), codes::DUPLICATE_KEY)
            }
            other => panic!("expected classified, got {other:?}"),
        }
        let e = driver_error(None, None, None, "syntax error near ...");
        assert!(matches!(e, CoreError::Driver(_)));
    }

    #[test]
    fn sqlstate_lands_in_detail() {
        let e = driver_error(Some("23505"), None, None, "x");
        match e {
            CoreError::DriverClassified { detail, .. } => {
                let v = detail.expect("detail present");
                assert_eq!(v["sqlstate"], "23505");
            }
            other => panic!("expected classified, got {other:?}"),
        }
    }
}
