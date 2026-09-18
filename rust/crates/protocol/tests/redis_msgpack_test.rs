use polydb_protocol::redis::{
    RedisReply, RedisReplyType, RedisReplyValue, RedisSetRequest, RedisValue, RedisZSetMember,
};
use serde::de::DeserializeOwned;
use serde::Serialize;
use std::collections::HashMap;

// 验证 internally-tagged RedisValue 与 RedisReply 在 rmp-serde（with_human_readable）
// 下的 msgpack 编解码，与 Go 侧 vmihailenco/msgpack 的 wire 形状一致。

fn encode<T: Serialize>(v: &T) -> Vec<u8> {
    let mut buf = Vec::new();
    let mut ser = rmp_serde::Serializer::new(&mut buf).with_struct_map();
    v.serialize(&mut ser).unwrap();
    buf
}

fn decode<T: DeserializeOwned>(buf: &[u8]) -> T {
    let mut de = rmp_serde::Deserializer::new(buf).with_human_readable();
    T::deserialize(&mut de).unwrap()
}

// 稳定往返：decode(encode(v)) 再 encode 必须与原字节一致（数值保真、无信息丢失）。
fn roundtrip_stable<T: Serialize + DeserializeOwned>(v: &T) {
    let first = encode(v);
    let back: T = decode(&first);
    assert_eq!(first, encode(&back));
}

#[test]
fn redis_value_msgpack_roundtrip() {
    let cases: Vec<RedisValue> = vec![
        RedisValue::String("hello".into()),
        RedisValue::List(vec!["a".into(), "b".into()]),
        RedisValue::Set(vec!["x".into()]),
        RedisValue::Zset(vec![RedisZSetMember {
            member: "m1".into(),
            score: 1.5,
        }]),
        RedisValue::Hash(HashMap::from([("f1".into(), "v1".into())])),
        RedisValue::Stream("payload-1".into()),
    ];
    for v in cases {
        roundtrip_stable(&v);
    }
}

#[test]
fn redis_set_request_msgpack_roundtrip() {
    let req = RedisSetRequest {
        key: "k".into(),
        value: RedisValue::Hash(HashMap::from([("f1".into(), "v1".into())])),
        ttl: None,
    };
    roundtrip_stable(&req);
}

#[test]
fn redis_reply_msgpack_roundtrip() {
    let cases: Vec<RedisReply> = vec![
        RedisReply {
            reply_type: RedisReplyType::BulkString,
            value: Some(RedisReplyValue::String("PONG".into())),
        },
        RedisReply {
            reply_type: RedisReplyType::Integer,
            value: Some(RedisReplyValue::Integer(6)),
        },
        RedisReply {
            reply_type: RedisReplyType::Array,
            value: Some(RedisReplyValue::Array(vec![
                RedisReply {
                    reply_type: RedisReplyType::BulkString,
                    value: Some(RedisReplyValue::String("a".into())),
                },
                RedisReply {
                    reply_type: RedisReplyType::Null,
                    value: None,
                },
            ])),
        },
        RedisReply {
            reply_type: RedisReplyType::Null,
            value: None,
        },
    ];
    for v in cases {
        roundtrip_stable(&v);
    }
}

// 与 Go vmihailenco/msgpack 的 wire 形状对拍（字段名 type/value）。
#[test]
fn redis_value_wire_shape() {
    let buf = encode(&RedisValue::String("hello".into()));
    assert!(
        buf.windows(6).any(|w| w == b"string"),
        "missing type=string"
    );
    assert!(buf.windows(5).any(|w| w == b"hello"), "missing value");
}
