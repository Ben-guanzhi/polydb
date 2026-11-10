//! Value → JSON 文本序列化：跨线协议统一形态，NaN/Inf 先替换为字符串。
//!
//! serde_json 对 NaN/Inf 序列化为 `null`——不是错误，是设计行为——
//! 整个 Array/Object 会静默变成 `""`（unwrap_or_default 兜底）或丢失
//! 元素。必须在序列化前递归替换 NaN→"NaN"、+Inf→"Infinity"、
//! -Inf→"-Infinity"，保证数据不丢。

use polydb_protocol::common::Value;

/// 递归替换 Value 中的 NaN/Inf 为字符串值，避免 serde_json 静默转 null。
pub fn sanitize_nan_inf(v: &Value) -> Value {
    match v {
        Value::Float(f) if f.is_nan() => Value::String("NaN".to_string()),
        Value::Float(f) if f.is_infinite() => Value::String(if *f > 0.0 {
            "Infinity".to_string()
        } else {
            "-Infinity".to_string()
        }),
        Value::Array(arr) => Value::Array(arr.iter().map(sanitize_nan_inf).collect()),
        Value::Object(map) => Value::Object(
            map.iter()
                .map(|(k, v)| (k.clone(), sanitize_nan_inf(v)))
                .collect(),
        ),
        _ => v.clone(),
    }
}

/// Array/Object 参数的绑定形态：跨线协议统一为 JSON 文本。
/// 先 sanitize NaN/Inf，再 serde_json 序列化。
pub fn value_to_json_text(v: &Value) -> String {
    let sanitized = sanitize_nan_inf(v);
    serde_json::to_string(&sanitized).unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn nan_in_array_serializes_as_string() {
        let arr = Value::Array(vec![
            Value::Float(1.5),
            Value::Float(f64::NAN),
            Value::Float(3.5),
        ]);
        let json = value_to_json_text(&arr);
        let parsed: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(
            parsed[1], "NaN",
            "NaN must be preserved as string, not null"
        );
    }

    #[test]
    fn inf_in_array_serializes_as_string() {
        let arr = Value::Array(vec![
            Value::Float(f64::INFINITY),
            Value::Float(f64::NEG_INFINITY),
        ]);
        let json = value_to_json_text(&arr);
        let parsed: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed[0], "Infinity");
        assert_eq!(parsed[1], "-Infinity");
    }

    #[test]
    fn nan_in_object_serializes_as_string() {
        let mut map = std::collections::HashMap::new();
        map.insert("value".to_string(), Value::Float(f64::NAN));
        let obj = Value::Object(map);
        let json = value_to_json_text(&obj);
        let parsed: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed["value"], "NaN");
    }

    #[test]
    fn nested_array_with_nan_sanitized() {
        let inner = Value::Array(vec![Value::Float(f64::NAN), Value::Integer(42)]);
        let arr = Value::Array(vec![Value::String("a".to_string()), inner]);
        let json = value_to_json_text(&arr);
        let parsed: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed[1][0], "NaN");
        assert_eq!(parsed[1][1], 42);
    }

    #[test]
    fn normal_values_serialize_unchanged() {
        let arr = Value::Array(vec![
            Value::Integer(1),
            Value::String("hello".to_string()),
            Value::Bool(true),
        ]);
        let json = value_to_json_text(&arr);
        assert_eq!(json, r#"[1,"hello",true]"#);
    }
}
