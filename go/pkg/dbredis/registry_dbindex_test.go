package dbredis

// PD-16（2026-11-09）回归测试：Redis db 编号的解析纪律。
//
// 缺陷：Go 与 Rust 两侧对 `connection.database`（Redis db 号）的解析规则不同，
// 且**都会在某些输入上静默回落 0**——用户连到 db 0 而不是自己填的库，
// 数据写进错误的库且无任何提示。
//
//   Go   (旧)：逐字符累加，任何非数字字符 ⇒ 0；整数不溢出（int 是 64 位）。
//   Rust (旧)：`d.trim().parse::<u32>().ok().unwrap_or(0)`——trim 后解析，
//               解析失败（含超出 u32 上界）⇒ 0。
//
// 于是 `" 3"` 两端分别得到 0 与 3；`"4294967296"` 两端分别得到一个大 db 号
// 与 0。AGENTS.md §14 红线「双实现行为不一致」被违反。
//
// 修复后的规则（两端逐字一致）：
//   空串            → 0
//   纯十进制数字    → 该值（0..=4294967295）
//   其余            → 报 POLYDB_ERR_INVALID_PARAM
//                  （含空白、正负号、小数点、非 ASCII 数字、超出 u32 上界）
//
// Rust 侧对应用例见 rust/crates/app-core/tests/redis_db_index.rs。

import (
	"errors"
	"testing"

	"github.com/polydb/polydb/pkg/protocol"
)

func TestDbIndexParsing(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "", want: 0},
		{in: "0", want: 0},
		{in: "1", want: 1},
		{in: "15", want: 15},
		{in: "16", want: 16},
		{in: "03", want: 3},
		{in: "4294967295", want: 4294967295},

		// 非法输入必须报错，绝不静默回落 0。
		{in: " 3", wantErr: true},
		{in: "3 ", wantErr: true},
		{in: " 3 ", wantErr: true},
		{in: "3a", wantErr: true},
		{in: "a3", wantErr: true},
		{in: "+3", wantErr: true},
		{in: "-1", wantErr: true},
		{in: "3.0", wantErr: true},
		{in: "4294967296", wantErr: true}, // 超出 u32 上界
		{in: "99999999999999999999", wantErr: true},
		{in: "０", wantErr: true}, // 全角数字
	}

	for _, c := range cases {
		got, err := dbIndex(protocol.ConnectionInfo{Database: c.in})
		if c.wantErr {
			if err == nil {
				t.Errorf("dbIndex(%q) = %d，want error（不得静默回落 0）", c.in, got)
				continue
			}
			var pe *protocol.PolyDBError
			if !errors.As(err, &pe) {
				t.Errorf("dbIndex(%q) 的错误不是 PolyDBError：%v", c.in, err)
				continue
			}
			if pe.Code != protocol.ErrInvalidParam {
				t.Errorf("dbIndex(%q) 的错误码 = %q，want %q", c.in, pe.Code, protocol.ErrInvalidParam)
			}
			continue
		}
		if err != nil {
			t.Errorf("dbIndex(%q) 意外报错：%v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("dbIndex(%q) = %d，want %d", c.in, got, c.want)
		}
	}
}

// TestOpenFromInfoRejectsInvalidDbNumber 钉住错误在**打开连接之前**就返回：
// 非法 db 号不该走到拨号那一步（否则会对着真实 Redis 发 SELECT）。
func TestOpenFromInfoRejectsInvalidDbNumber(t *testing.T) {
	_, err := openFromInfo(t.Context(), protocol.ConnectionInfo{
		Kind:     protocol.DatabaseKindRedis,
		Host:     "127.0.0.1",
		Port:     1, // 不可达：若真去拨号，报错会变成连接错误而非参数错误
		Database: "not-a-number",
	}, "")
	if err == nil {
		t.Fatalf("非法 db 号竟然被接受")
	}
	var pe *protocol.PolyDBError
	if !errors.As(err, &pe) {
		t.Fatalf("错误不是 PolyDBError：%v", err)
	}
	if pe.Code != protocol.ErrInvalidParam {
		t.Errorf("错误码 = %q，want %q（必须在拨号前拒绝）", pe.Code, protocol.ErrInvalidParam)
	}
}
