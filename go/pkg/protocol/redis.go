package protocol

type RedisKeyType string

const (
	RedisKeyTypeString RedisKeyType = "string"
	RedisKeyTypeList   RedisKeyType = "list"
	RedisKeyTypeSet    RedisKeyType = "set"
	RedisKeyTypeZSet   RedisKeyType = "zset"
	RedisKeyTypeHash   RedisKeyType = "hash"
	RedisKeyTypeStream RedisKeyType = "stream"
	RedisKeyTypeNone   RedisKeyType = "none"
)

type RedisValue struct {
	Type  RedisKeyType `json:"type" msgpack:"type"`
	Value interface{}  `json:"value" msgpack:"value"`
}

type RedisZSetMember struct {
	Member string  `json:"member" msgpack:"member"`
	Score  float64 `json:"score" msgpack:"score"`
}

type RedisScanPage struct {
	Cursor uint64         `json:"cursor" msgpack:"cursor"`
	Keys   []RedisKeyInfo `json:"keys" msgpack:"keys"`
}

type RedisKeyInfo struct {
	Key  string       `json:"key" msgpack:"key"`
	Type RedisKeyType `json:"type" msgpack:"type"`
	TTL  *int64       `json:"ttl,omitempty" msgpack:"ttl,omitempty"`
}

type RedisReplyType string

const (
	RedisReplySimpleString RedisReplyType = "simple_string"
	RedisReplyError        RedisReplyType = "error"
	RedisReplyInteger      RedisReplyType = "integer"
	RedisReplyBulkString   RedisReplyType = "bulk_string"
	RedisReplyArray        RedisReplyType = "array"
	RedisReplyNull         RedisReplyType = "null"
)

type RedisReply struct {
	Type  RedisReplyType `json:"type" msgpack:"type"`
	Value interface{}    `json:"value,omitempty" msgpack:"value,omitempty"`
}

type RedisSelectDbRequest struct {
	Index int `json:"index" msgpack:"index"`
}

type RedisScanRequest struct {
	Cursor  uint64 `json:"cursor,omitempty" msgpack:"cursor,omitempty"`
	Pattern string `json:"pattern,omitempty" msgpack:"pattern,omitempty"`
	Count   int    `json:"count,omitempty" msgpack:"count,omitempty"`
}

type RedisSetRequest struct {
	Key   string     `json:"key" msgpack:"key"`
	Value RedisValue `json:"value" msgpack:"value"`
	TTL   *uint64    `json:"ttl,omitempty" msgpack:"ttl,omitempty"`
}

type RedisExecCommandRequest struct {
	Args []string `json:"args" msgpack:"args"`
}
