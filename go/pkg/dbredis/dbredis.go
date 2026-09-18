// Package dbredis 实现基于 go-redis/v9 的 KV 驱动（Redis）。
// 用 client.Conn() 取一条专用连接：SELECT 的 db 状态在该连接上持久，
// 保证 SelectDB 语义与 Rust 侧单连接实现一致。
package dbredis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// timeout 覆盖拨号/读写：不可达主机（防火墙丢包）时快速失败而非长挂。
const timeout = 5 * time.Second

type Conn struct {
	mu     sync.Mutex
	client *redis.Client
	conn   *redis.Conn
	addr   string
}

// Open 连接 addr（host:port），初始 dbIndex 为 Redis db 编号；password 来自 keyring（可为空）。
func Open(ctx context.Context, addr string, dbIndex int, password string) (*Conn, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		DB:           dbIndex,
		Password:     password,
		DialTimeout:  timeout,
		ReadTimeout:  timeout,
		WriteTimeout: timeout,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping redis %s: %w", addr, err)
	}
	conn := client.Conn()
	return &Conn{client: client, conn: conn, addr: addr}, nil
}

func (c *Conn) Kind() protocol.DatabaseKind     { return protocol.DatabaseKindRedis }
func (c *Conn) AsSQL() (dbcore.SQLDriver, bool) { return nil, false }
func (c *Conn) AsKV() (dbcore.KVDriver, bool)   { return c, true }

func (c *Conn) Ping(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Ping(ctx).Err()
}

func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	if c.client != nil {
		err := c.client.Close()
		c.client = nil
		return err
	}
	return nil
}

// ─── KVDriver ────────────────────────────────────────────────

func (c *Conn) SelectDB(ctx context.Context, index int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Do(ctx, "SELECT", index).Err()
}

func (c *Conn) ScanKeys(ctx context.Context, cursor uint64, pattern string, count int) (*protocol.RedisScanPage, error) {
	if pattern == "" {
		pattern = "*"
	}
	if count <= 0 {
		count = 100
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	keys, next, err := c.conn.Scan(ctx, cursor, pattern, int64(count)).Result()
	if err != nil {
		return nil, kvErr(err)
	}
	page := &protocol.RedisScanPage{Cursor: next, Keys: []protocol.RedisKeyInfo{}}
	for _, k := range keys {
		typ, err := c.conn.Type(ctx, k).Result()
		if err != nil {
			return nil, kvErr(err)
		}
		ttl := c.conn.TTL(ctx, k).Val()
		// go-redis 的 DurationCmd 对负值（-1 无过期 / -2 不存在）以纳秒原样返回，
		// 正值是整秒；直接 /time.Second 会把 -1ns 截断成 0（与 Rust 原始 TTL 对齐）。
		ttlSec := int64(ttl / time.Second)
		if ttl < 0 {
			ttlSec = int64(ttl)
		}
		page.Keys = append(page.Keys, protocol.RedisKeyInfo{
			Key:  k,
			Type: redisKeyType(typ),
			TTL:  &ttlSec,
		})
	}
	return page, nil
}

func (c *Conn) KeyType(ctx context.Context, key string) (protocol.RedisKeyType, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	typ, err := c.conn.Type(ctx, key).Result()
	if err != nil {
		return protocol.RedisKeyTypeNone, kvErr(err)
	}
	return redisKeyType(typ), nil
}

func (c *Conn) GetValue(ctx context.Context, key string) (protocol.RedisValue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	typ, err := c.conn.Type(ctx, key).Result()
	if err != nil {
		return protocol.RedisValue{}, kvErr(err)
	}
	switch redisKeyType(typ) {
	case protocol.RedisKeyTypeNone:
		// spec 的 RedisValue 无 none 变体：缺失键以错误返回（与 Rust 侧一致）。
		return protocol.RedisValue{}, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: "key not found: " + key}
	case protocol.RedisKeyTypeString:
		s, err := c.conn.Get(ctx, key).Result()
		if err == redis.Nil {
			return protocol.RedisValue{}, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: "key not found: " + key}
		}
		if err != nil {
			return protocol.RedisValue{}, kvErr(err)
		}
		return protocol.RedisValue{Type: protocol.RedisKeyTypeString, Value: s}, nil
	case protocol.RedisKeyTypeList:
		v, err := c.conn.LRange(ctx, key, 0, -1).Result()
		if err != nil {
			return protocol.RedisValue{}, kvErr(err)
		}
		return protocol.RedisValue{Type: protocol.RedisKeyTypeList, Value: v}, nil
	case protocol.RedisKeyTypeSet:
		v, err := c.conn.SMembers(ctx, key).Result()
		if err != nil {
			return protocol.RedisValue{}, kvErr(err)
		}
		return protocol.RedisValue{Type: protocol.RedisKeyTypeSet, Value: v}, nil
	case protocol.RedisKeyTypeZSet:
		zs, err := c.conn.ZRangeWithScores(ctx, key, 0, -1).Result()
		if err != nil {
			return protocol.RedisValue{}, kvErr(err)
		}
		members := make([]protocol.RedisZSetMember, len(zs))
		for i, z := range zs {
			members[i] = protocol.RedisZSetMember{Member: fmt.Sprint(z.Member), Score: z.Score}
		}
		return protocol.RedisValue{Type: protocol.RedisKeyTypeZSet, Value: members}, nil
	case protocol.RedisKeyTypeHash:
		v, err := c.conn.HGetAll(ctx, key).Result()
		if err != nil {
			return protocol.RedisValue{}, kvErr(err)
		}
		return protocol.RedisValue{Type: protocol.RedisKeyTypeHash, Value: v}, nil
	case protocol.RedisKeyTypeStream:
		msgs, err := c.conn.XRangeN(ctx, key, "-", "+", 100).Result()
		if err != nil {
			return protocol.RedisValue{}, kvErr(err)
		}
		return protocol.RedisValue{Type: protocol.RedisKeyTypeStream, Value: serializeStream(msgs)}, nil
	default:
		return protocol.RedisValue{}, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: "key not found: " + key}
	}
}

func (c *Conn) SetValue(ctx context.Context, key string, value protocol.RedisValue) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch value.Type {
	case protocol.RedisKeyTypeString:
		s, _ := value.Value.(string)
		return c.conn.Set(ctx, key, s, 0).Err()
	case protocol.RedisKeyTypeList:
		elems := strSlice(value.Value)
		return c.setAndExpire(ctx, key, func() error {
			return c.conn.RPush(ctx, key, elems).Err()
		})
	case protocol.RedisKeyTypeSet:
		elems := strSlice(value.Value)
		return c.setAndExpire(ctx, key, func() error {
			return c.conn.SAdd(ctx, key, elems).Err()
		})
	case protocol.RedisKeyTypeZSet:
		members := zsetMembers(value.Value)
		zs := make([]redis.Z, len(members))
		for i, m := range members {
			zs[i] = redis.Z{Member: m.Member, Score: m.Score}
		}
		return c.setAndExpire(ctx, key, func() error {
			return c.conn.ZAdd(ctx, key, zs...).Err()
		})
	case protocol.RedisKeyTypeHash:
		fields := make(map[string]interface{}, 8)
		for k, v := range strMap(value.Value) {
			fields[k] = v
		}
		return c.setAndExpire(ctx, key, func() error {
			return c.conn.HSet(ctx, key, fields).Err()
		})
	case protocol.RedisKeyTypeStream:
		s, _ := value.Value.(string)
		// 单字段约定：XADD key * data <string>（与 Rust 侧一致）
		return c.conn.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]interface{}{"data": s}}).Err()
	}
	return nil
}

// strSlice / strMap / zsetMembers 把 HTTP 解码后的 Value（msgpack 对 interface{}
// 字段产出 []interface{} / map[string]interface{}）归一到强类型，与 Rust 侧
// serde 强类型解码对齐。也兼容测试与进程内调用直接传强类型。
func strSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []interface{}:
		out := make([]string, len(t))
		for i, e := range t {
			out[i] = fmt.Sprint(e)
		}
		return out
	case nil:
		return nil
	}
	return nil
}

func strMap(v any) map[string]string {
	switch t := v.(type) {
	case map[string]string:
		return t
	case map[string]interface{}:
		out := make(map[string]string, len(t))
		for k, e := range t {
			out[k] = fmt.Sprint(e)
		}
		return out
	case nil:
		return nil
	}
	return nil
}

func zsetMembers(v any) []protocol.RedisZSetMember {
	switch t := v.(type) {
	case []protocol.RedisZSetMember:
		return t
	case []interface{}:
		out := make([]protocol.RedisZSetMember, 0, len(t))
		for _, e := range t {
			m, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			var score float64
			switch s := m["score"].(type) {
			case float64:
				score = s
			case int64:
				score = float64(s)
			case uint64:
				score = float64(s)
			}
			out = append(out, protocol.RedisZSetMember{Member: fmt.Sprint(m["member"]), Score: score})
		}
		return out
	case nil:
		return nil
	}
	return nil
}

// setAndExpire 先删后建（覆盖语义）。TTL 不在 driver API（AGENTS.md 定义的
// set_value 只有 key+value），由 exec_command EXPIRE 等命令显式设置。
func (c *Conn) setAndExpire(ctx context.Context, key string, build func() error) error {
	if err := c.conn.Del(ctx, key).Err(); err != nil {
		return kvErr(err)
	}
	if err := build(); err != nil {
		return kvErr(err)
	}
	return nil
}

func (c *Conn) ExecCommand(ctx context.Context, args []string) (protocol.RedisReply, error) {
	if len(args) == 0 {
		return protocol.RedisReply{Type: protocol.RedisReplyError, Value: "empty command"}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	argv := make([]interface{}, len(args))
	for i, a := range args {
		argv[i] = a
	}
	cmd := redis.NewCmd(ctx, argv...)
	if err := c.conn.Process(ctx, cmd); err != nil {
		if errors.Is(err, redis.Nil) {
			// GET 等缺失 key 返回 null 而非 error（与 Rust 侧 Value::Nil 一致）
			return protocol.RedisReply{Type: protocol.RedisReplyNull}, nil
		}
		return protocol.RedisReply{Type: protocol.RedisReplyError, Value: err.Error()}, nil
	}
	return goReply(cmd.Val()), nil
}

// ─── 转换辅助 ────────────────────────────────────────────────

func redisKeyType(typ string) protocol.RedisKeyType {
	switch typ {
	case "string":
		return protocol.RedisKeyTypeString
	case "list":
		return protocol.RedisKeyTypeList
	case "set":
		return protocol.RedisKeyTypeSet
	case "zset":
		return protocol.RedisKeyTypeZSet
	case "hash":
		return protocol.RedisKeyTypeHash
	case "stream":
		return protocol.RedisKeyTypeStream
	default:
		return protocol.RedisKeyTypeNone
	}
}

// goReply 将 go-redis 的原始 RESP 值转换为协议 RedisReply。
//
// 约定（双实现统一）：RESP 的 simple string 与 bulk string 一律映射为
// bulk_string。go-redis 的 proto.Reader 对两者都返回 Go string，
// Cmd.Val() 无法区分；为与 Rust 侧 redis crate 保持一致，两端统一按
// bulk_string 处理。数组内嵌套的 RESP error 以 proto.RedisError（error
// 接口）形式存储，映射为 error 类型。
func goReply(v interface{}) protocol.RedisReply {
	switch r := v.(type) {
	case nil:
		return protocol.RedisReply{Type: protocol.RedisReplyNull}
	case string:
		return protocol.RedisReply{Type: protocol.RedisReplyBulkString, Value: r}
	case []byte:
		return protocol.RedisReply{Type: protocol.RedisReplyBulkString, Value: string(r)}
	case int64:
		return protocol.RedisReply{Type: protocol.RedisReplyInteger, Value: r}
	case error:
		return protocol.RedisReply{Type: protocol.RedisReplyError, Value: r.Error()}
	case []interface{}:
		arr := make([]protocol.RedisReply, len(r))
		for i, e := range r {
			arr[i] = goReply(e)
		}
		return protocol.RedisReply{Type: protocol.RedisReplyArray, Value: arr}
	default:
		return protocol.RedisReply{Type: protocol.RedisReplyBulkString, Value: fmt.Sprint(r)}
	}
}

func serializeStream(msgs []redis.XMessage) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(m.ID)
		b.WriteString(" {")
		fields := make([]string, 0, len(m.Values))
		for k, v := range m.Values {
			fields = append(fields, k+"="+fmt.Sprint(v))
		}
		sort.Strings(fields)
		b.WriteString(strings.Join(fields, ", "))
		b.WriteString("}")
	}
	return b.String()
}

func kvErr(err error) *protocol.PolyDBError {
	return &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: "redis: " + err.Error()}
}
