// Package dbredis 把 polydb 的 KV 协议面接到共享内核 dbcore/redis 上
// （INTEGRATION-PLAN P1-5 收尾）：连接/SELECT、D5 续扫（每键 Type/TTL 富化）、
// 六类值读取、覆盖式写入、原始命令都在内核维护一份；本包只做 protocol 类型
// 映射与错误码包装。
package dbredis

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	dbc "github.com/dbcore/dbcore"
	dbcoreredis "github.com/dbcore/dbcore/redis"

	"github.com/polydb/polydb/pkg/dbcore"
	"github.com/polydb/polydb/pkg/protocol"
)

// timeout 覆盖拨号/读写：不可达主机（防火墙丢包）时快速失败而非长挂。
const timeout = 5 * time.Second

type Conn struct {
	mu  sync.Mutex
	drv *dbcoreredis.RedisDriver
	// db 记录当前逻辑库编号，只为填协议 RedisScanPage.DB；真值由内核
	// SelectDB 的换端语义保证（SELECT 是连接级状态，内核按库重拨）。
	db int
}

// Open 连接 addr（host:port），初始 dbIndex 为 Redis db 编号；password 来自 keyring（可为空）。
func Open(ctx context.Context, addr string, dbIndex int, password string) (*Conn, error) {
	drv, err := dbcoreredis.OpenOptions(&goredis.Options{
		Addr:         addr,
		DB:           dbIndex,
		Password:     password,
		DialTimeout:  timeout,
		ReadTimeout:  timeout,
		WriteTimeout: timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("ping redis %s: %w", addr, err)
	}
	_ = ctx // 拨号超时由 Options 承担；内核 Ping 用后台 ctx 即可
	return &Conn{drv: drv, db: dbIndex}, nil
}

func (c *Conn) Kind() protocol.DatabaseKind     { return protocol.DatabaseKindRedis }
func (c *Conn) AsSQL() (dbcore.SQLDriver, bool) { return nil, false }
func (c *Conn) AsKV() (dbcore.KVDriver, bool)   { return c, true }

func (c *Conn) Ping(ctx context.Context) error { return c.drv.Ping(ctx) }

func (c *Conn) Close() error { return c.drv.Close() }

// ─── KVDriver（委托内核，协议形状适配）──────────────────────

func (c *Conn) SelectDB(ctx context.Context, index int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.drv.SelectDB(ctx, index); err != nil {
		return kvErr(err)
	}
	// 只在内核确认后更新：失败时逻辑库未变。
	c.db = index
	return nil
}

func (c *Conn) ScanKeys(ctx context.Context, cursor uint64, pattern string, count int) (*protocol.RedisScanPage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// D5 服务端游标循环（spec/behavior.md §1.3）在内核 ScanKeysEnriched 里：
	// 续扫到凑满 count、游标回 0，或触及轮次上限（与 Rust 侧严格一致）。
	page, err := c.drv.ScanKeysEnriched(ctx, cursor, pattern, count)
	if err != nil {
		return nil, kvErr(err)
	}
	cur := uint32(c.db)
	out := &protocol.RedisScanPage{DB: &cur, Cursor: page.Cursor, Keys: make([]protocol.RedisKeyInfo, 0, len(page.Keys))}
	for _, e := range page.Keys {
		ttl := e.TTL
		out.Keys = append(out.Keys, protocol.RedisKeyInfo{
			Key:  e.Key,
			Type: redisKeyType(e.Kind),
			TTL:  ttl,
		})
	}
	return out, nil
}

func (c *Conn) KeyType(ctx context.Context, key string) (protocol.RedisKeyType, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kind, err := c.drv.KeyType(ctx, key)
	if err != nil {
		return protocol.RedisKeyTypeNone, kvErr(err)
	}
	return redisKeyType(kind), nil
}

func (c *Conn) GetValue(ctx context.Context, key string) (protocol.RedisValue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, err := c.drv.GetValue(ctx, key)
	if err != nil {
		// 内核对缺失键报 "redis: key %q not found"；协议的 RedisValue 无 none
		// 变体，缺失以错误返回（与 Rust 侧一致）。
		if strings.Contains(err.Error(), "not found") {
			return protocol.RedisValue{}, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: "key not found: " + key}
		}
		return protocol.RedisValue{}, kvErr(err)
	}
	switch v.Type {
	case dbc.RedisStr:
		return protocol.RedisValue{Type: protocol.RedisKeyTypeString, Value: v.Str}, nil
	case dbc.RedisList:
		return protocol.RedisValue{Type: protocol.RedisKeyTypeList, Value: v.Items}, nil
	case dbc.RedisSet:
		// 内核读回 set 时排序（确定性展示）；协议侧原样接收。
		return protocol.RedisValue{Type: protocol.RedisKeyTypeSet, Value: v.Items}, nil
	case dbc.RedisZSet:
		members := make([]protocol.RedisZSetMember, len(v.Items))
		for i, m := range v.Items {
			var score float64
			if i < len(v.Scores) {
				score = v.Scores[i]
			}
			members[i] = protocol.RedisZSetMember{Member: m, Score: score}
		}
		return protocol.RedisValue{Type: protocol.RedisKeyTypeZSet, Value: members}, nil
	case dbc.RedisHash:
		return protocol.RedisValue{Type: protocol.RedisKeyTypeHash, Value: v.Hash}, nil
	case dbc.RedisStream:
		return protocol.RedisValue{Type: protocol.RedisKeyTypeStream, Value: serializeStream(v.Stream)}, nil
	}
	return protocol.RedisValue{}, &protocol.PolyDBError{Code: protocol.ErrQueryFailed, Message: "key not found: " + key}
}

func (c *Conn) SetValue(ctx context.Context, key string, value protocol.RedisValue) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch value.Type {
	case protocol.RedisKeyTypeString:
		s, _ := value.Value.(string)
		return kvErr0(c.drv.SetValue(ctx, key, dbc.RedisValue{Type: dbc.RedisStr, Str: s}))
	case protocol.RedisKeyTypeList:
		return kvErr0(c.drv.SetValue(ctx, key, dbc.RedisValue{Type: dbc.RedisList, Items: strSlice(value.Value)}))
	case protocol.RedisKeyTypeSet:
		return kvErr0(c.drv.SetValue(ctx, key, dbc.RedisValue{Type: dbc.RedisSet, Items: strSlice(value.Value)}))
	case protocol.RedisKeyTypeZSet:
		members := zsetMembers(value.Value)
		v := dbc.RedisValue{Type: dbc.RedisZSet, Items: make([]string, len(members)), Scores: make([]float64, len(members))}
		for i, m := range members {
			v.Items[i] = m.Member
			v.Scores[i] = m.Score
		}
		return kvErr0(c.drv.SetValue(ctx, key, v))
	case protocol.RedisKeyTypeHash:
		fields := strMap(value.Value)
		return kvErr0(c.drv.SetValue(ctx, key, dbc.RedisValue{Type: dbc.RedisHash, Hash: fields}))
	case protocol.RedisKeyTypeStream:
		s, _ := value.Value.(string)
		// 单字段约定：XADD key * data <string>（与 Rust 侧一致），经内核
		// RowAppender 表达（流的 SetValue 是追加条目而非覆盖键）。
		_, err := c.drv.AppendRow(ctx, dbc.RedisRow{
			Key:    key,
			Kind:   dbc.RedisStream,
			Fields: []dbc.RedisField{{Name: "data", Value: s}},
		})
		return kvErr0(err)
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

func (c *Conn) ExecCommand(ctx context.Context, args []string) (protocol.RedisReply, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 结构化 RESP（simple/bulk 统一为 bulk_string、nil→null、嵌套错误保持
	// error 类型）在内核 ExecCommandTyped 维护一份约定。
	reply, err := c.drv.ExecCommandTyped(ctx, args)
	if err != nil {
		return protocol.RedisReply{Type: protocol.RedisReplyError, Value: err.Error()}, nil
	}
	return typedToProtocol(reply), nil
}

// typedToProtocol 把内核结构化回复映射为协议 RedisReply。
func typedToProtocol(r dbc.RedisTypedReply) protocol.RedisReply {
	switch r.Kind {
	case dbc.RedisReplyKindNull:
		return protocol.RedisReply{Type: protocol.RedisReplyNull}
	case dbc.RedisReplyKindInteger:
		n, _ := r.Value.(int64)
		return protocol.RedisReply{Type: protocol.RedisReplyInteger, Value: n}
	case dbc.RedisReplyKindError:
		return protocol.RedisReply{Type: protocol.RedisReplyError, Value: fmt.Sprint(r.Value)}
	case dbc.RedisReplyKindArray:
		arr, _ := r.Value.([]dbc.RedisTypedReply)
		out := make([]protocol.RedisReply, len(arr))
		for i, e := range arr {
			out[i] = typedToProtocol(e)
		}
		return protocol.RedisReply{Type: protocol.RedisReplyArray, Value: out}
	default: // bulk_string 及其他一律按 bulk_string
		return protocol.RedisReply{Type: protocol.RedisReplyBulkString, Value: fmt.Sprint(r.Value)}
	}
}

// ─── 转换辅助 ────────────────────────────────────────────────

func redisKeyType(kind dbc.RedisKeyType) protocol.RedisKeyType {
	switch kind {
	case dbc.RedisStr:
		return protocol.RedisKeyTypeString
	case dbc.RedisList:
		return protocol.RedisKeyTypeList
	case dbc.RedisSet:
		return protocol.RedisKeyTypeSet
	case dbc.RedisZSet:
		return protocol.RedisKeyTypeZSet
	case dbc.RedisHash:
		return protocol.RedisKeyTypeHash
	case dbc.RedisStream:
		return protocol.RedisKeyTypeStream
	}
	return protocol.RedisKeyTypeNone
}

// serializeStream 渲染协议约定的流形状："id {k=v, k2=v2}; id2 {...}"。
// 条目与字段结构由内核给出，这里只做展示格式。
func serializeStream(entries []dbc.RedisStreamEntry) string {
	var b strings.Builder
	for i, m := range entries {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(m.ID)
		b.WriteString(" {")
		fields := make([]string, 0, len(m.Fields))
		for _, f := range m.Fields {
			fields = append(fields, f.Name+"="+f.Value)
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

func kvErr0(err error) error {
	if err == nil {
		return nil
	}
	return kvErr(err)
}
