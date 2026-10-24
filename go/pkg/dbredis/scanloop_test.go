package dbredis

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	dbcoreredis "github.com/dbcore/dbcore/redis"
)

// D5（spec/behavior.md §1.3）验证：用最小 RESP 假服务器模拟真实 Redis 的分批 SCAN
// （每批 ≤ COUNT 个键、MATCH 在批内过滤），钉死驱动侧游标循环的三个语义：
// 凑满一页、无匹配时单调用穷尽、轮次上限处停下并给出真实续点。
// 契约测试只有连真实 Redis 时才跑（soft-skip），这里让循环逻辑离线可测。

type fakeRedis struct {
	keys  []string
	scans atomic.Int64
	ln    net.Listener
	wg    sync.WaitGroup
}

// keyspace：250 个键，i%50==7 的用 "sel:" 前缀 → sel:* 恰好匹配 5 个（i=7,57,107,157,207）。
func startFake(t *testing.T) *fakeRedis {
	t.Helper()
	f := &fakeRedis{}
	for i := 0; i < 250; i++ {
		if i%50 == 7 {
			f.keys = append(f.keys, fmt.Sprintf("sel:%03d", i))
		} else {
			f.keys = append(f.keys, fmt.Sprintf("k%03d", i))
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f.ln = ln
	f.wg.Add(1)
	go f.acceptLoop()
	t.Cleanup(func() {
		_ = ln.Close()
		f.wg.Wait()
	})
	return f
}

func (f *fakeRedis) addr() string { return f.ln.Addr().String() }

func (f *fakeRedis) acceptLoop() {
	defer f.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return // listener 已关闭
		}
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			f.serveConn(c)
		}()
	}
}

// glob 只需覆盖测试用到的三种形态："*"、"prefix*"、精确名。
func globMatch(pattern, key string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(key, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == key
}

func (f *fakeRedis) serveConn(c net.Conn) {
	defer c.Close()
	rd := bufio.NewReader(c)
	for {
		args, err := readCmd(rd)
		if err != nil {
			return
		}
		var reply string
		switch strings.ToUpper(args[0]) {
		case "PING":
			reply = "+PONG\r\n"
		case "HELLO":
			// go-redis v9 收到 unknown command 会回落 RESP2。
			reply = "-ERR unknown command 'HELLO'\r\n"
		case "TYPE":
			reply = "+string\r\n"
		case "TTL":
			reply = ":-1\r\n"
		case "SCAN":
			reply = f.scanReply(args[1:])
		default:
			reply = "+OK\r\n"
		}
		if _, err := c.Write([]byte(reply)); err != nil {
			return
		}
	}
}

func (f *fakeRedis) scanReply(args []string) string {
	cur, _ := strconv.ParseUint(args[0], 10, 64)
	pattern, count := "*", 10
	for i := 1; i+1 < len(args); i += 2 {
		switch strings.ToUpper(args[i]) {
		case "MATCH":
			pattern = args[i+1]
		case "COUNT":
			count, _ = strconv.Atoi(args[i+1])
		}
	}
	f.scans.Add(1)
	if count <= 0 {
		count = 10
	}
	// 真实 SCAN 语义：按游标切片采样（≤COUNT 个键），MATCH 只过滤采样批不过滤游标推进。
	start := cur
	if start > uint64(len(f.keys)) {
		start = uint64(len(f.keys))
	}
	end := start + uint64(count)
	next := end
	if end >= uint64(len(f.keys)) {
		end = uint64(len(f.keys))
		next = 0
	}
	var matched []string
	for _, k := range f.keys[start:end] {
		if globMatch(pattern, k) {
			matched = append(matched, k)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "*2\r\n$%d\r\n%d\r\n*%d\r\n", len(strconv.FormatUint(next, 10)), next, len(matched))
	for _, k := range matched {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(k), k)
	}
	return b.String()
}

// readCmd 解析 RESP2 数组命令（驱动只会发数组，无需内联命令）。
func readCmd(rd *bufio.Reader) ([]string, error) {
	line, err := rd.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("expected array, got %q", line)
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil || n < 1 {
		return nil, fmt.Errorf("bad array header %q", line)
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		hdr, err := rd.ReadString('\n')
		if err != nil {
			return nil, err
		}
		hdr = strings.TrimRight(hdr, "\r\n")
		if !strings.HasPrefix(hdr, "$") {
			return nil, fmt.Errorf("expected bulk, got %q", hdr)
		}
		size, err := strconv.Atoi(hdr[1:])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("bad bulk size %q", hdr)
		}
		buf := make([]byte, size+2) // 含结尾 CRLF
		if _, err := readFull(rd, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

func readFull(rd *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := rd.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func openFake(t *testing.T, f *fakeRedis) *Conn {
	t.Helper()
	c, err := Open(context.Background(), f.addr(), 0, "")
	if err != nil {
		t.Fatalf("open fake redis: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestScanKeysFillsOnePageAcrossRounds：count=20 必须一次调用凑满 20 个键，
// 游标停在真实续点（单轮假服务器批次恰为 20 → 一轮即满，与旧行为一致的部分
// 由第二个断言组覆盖：count=7 时 20 个键需要 3 轮）。
func TestScanKeysFillsOnePageAcrossRounds(t *testing.T) {
	f := startFake(t)
	c := openFake(t, f)
	ctx := context.Background()

	page, err := c.ScanKeys(ctx, 0, "*", 20)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(page.Keys) != 20 || page.Keys[0].Key != "k000" || page.Keys[19].Key != "k019" {
		t.Fatalf("page keys = %d, want 20 (k000..k019)", len(page.Keys))
	}
	if page.Cursor != 20 {
		t.Errorf("cursor = %d, want 20（真实续点，遍历未完成）", page.Cursor)
	}
	if page.DB == nil || *page.DB != 0 {
		t.Errorf("db echo = %v, want 0", page.DB)
	}
	for _, k := range page.Keys {
		if k.TTL == nil || *k.TTL != -1 {
			t.Fatalf("ttl for %s = %v, want -1", k.Key, k.TTL)
		}
	}

	// count=7：凑满 20 个键需 3 轮，且第三轮整批保留（len≥count 才停）。
	f.scans.Store(0)
	page, err = c.ScanKeys(ctx, 0, "*", 7)
	if err != nil {
		t.Fatalf("scan(count7): %v", err)
	}
	if len(page.Keys) < 7 {
		t.Errorf("count=7 page = %d keys, want ≥7（循环须凑满一页）", len(page.Keys))
	}
	if page.Cursor == 0 {
		t.Errorf("cursor = 0, want 非 0 续点（250 键未扫完）")
	}
}

// TestScanKeysExhaustsWithSelectivePattern：sel:* 只匹配 5 个键。
// 旧实现（单轮）首轮 [0,3) 返回 0 键、cursor=3，前端会显示"无匹配键"；
// D5 要求单调用就跨轮收集，直到凑满或游标穷尽。
func TestScanKeysExhaustsWithSelectivePattern(t *testing.T) {
	f := startFake(t)
	c := openFake(t, f)
	ctx := context.Background()

	// count=3：第 7/57/107 号键分属不同批次 → 需跨 36 轮凑满 3 个后停在真实续点。
	page, err := c.ScanKeys(ctx, 0, "sel:*", 3)
	if err != nil {
		t.Fatalf("scan sel: %v", err)
	}
	if len(page.Keys) != 3 {
		t.Fatalf("first page = %d keys, want 3", len(page.Keys))
	}
	want := []string{"sel:007", "sel:057", "sel:107"}
	for i, k := range page.Keys {
		if k.Key != want[i] {
			t.Errorf("key %d = %s, want %s", i, k.Key, want[i])
		}
	}
	if page.Cursor != 108 {
		t.Errorf("cursor = %d, want 108（凑满即停的真实续点）", page.Cursor)
	}

	// 续扫到底：剩余 2 个匹配 + 游标穷尽，同样单调用完成。
	page2, err := c.ScanKeys(ctx, page.Cursor, "sel:*", 10)
	if err != nil {
		t.Fatalf("scan resume: %v", err)
	}
	if len(page2.Keys) != 2 || page2.Cursor != 0 {
		t.Fatalf("resume = %d keys cursor %d, want 2 keys cursor 0", len(page2.Keys), page2.Cursor)
	}
	if page2.Keys[0].Key != "sel:157" || page2.Keys[1].Key != "sel:207" {
		t.Errorf("resume keys = %s,%s want sel:157,sel:207", page2.Keys[0].Key, page2.Keys[1].Key)
	}

	// 无匹配模式：单调用穷尽整个键空间（250/10=25 轮 < 上限），cursor=0、0 键。
	f.scans.Store(0)
	page3, err := c.ScanKeys(ctx, 0, "nomatch:*", 10)
	if err != nil {
		t.Fatalf("scan nomatch: %v", err)
	}
	if len(page3.Keys) != 0 || page3.Cursor != 0 {
		t.Errorf("nomatch = %d keys cursor %d, want 0 keys cursor 0", len(page3.Keys), page3.Cursor)
	}
	if got := f.scans.Load(); got != 25 {
		t.Errorf("scan rounds = %d, want 25（穷尽所需轮数）", got)
	}
}

// TestScanKeysStopsAtRoundCap：nomatch + count=2 需 125 轮才能穷尽，
// 必须停在 scanMaxRounds=100 并返回非 0 续点，防止单请求无界扫描。
func TestScanKeysStopsAtRoundCap(t *testing.T) {
	f := startFake(t)
	c := openFake(t, f)

	page, err := c.ScanKeys(context.Background(), 0, "nomatch:*", 2)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := f.scans.Load(); got != int64(dbcoreredis.ScanMaxRounds) {
		t.Errorf("rounds = %d, want %d（轮次上限）", got, dbcoreredis.ScanMaxRounds)
	}
	if page.Cursor != uint64(dbcoreredis.ScanMaxRounds*2) {
		t.Errorf("cursor = %d, want %d（续点，调用方可继续）", page.Cursor, dbcoreredis.ScanMaxRounds*2)
	}
	if len(page.Keys) != 0 {
		t.Errorf("keys = %d, want 0", len(page.Keys))
	}
}
