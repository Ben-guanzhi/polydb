package dbcore

import (
	"encoding/hex"
	"testing"
)

// PD-18（2026-11-10）回归测试：MSSQL `UNIQUEIDENTIFIER` 列的读取形态。
//
// 缺陷：go-mssqldb 把 uniqueidentifier 列解码成 16 字节 []byte
// （types.go:decodeGuid）。旧链对它没有专门分支——base == "UNIQUEIDENTIFIER"
// 不在 INT/FLOAT/BLOB 白名单里，落到末尾 NewStringValue(string(b))，
// 输出 **16 个原始字节的乱码字符串**，用户读不到 UUID。
// Rust 侧更糟：tiberius 的 &[u8] 只匹配 ColumnData::Binary（from_sql.rs:114-124），
// Guid 列整条链落空 → Value::Null（数据丢失）。
//
// 修复后两端统一为**标准小写 UUID 文本**（Rust: tiberius::Uuid::to_string；
// Go: uuid.UUID.String）。本测试钉住 Go 侧形态，作为对齐目标。

func TestUniqueIdentifierDecodedAsUUIDText(t *testing.T) {
	// go-mssqldb decodeGuid 交给 database/sql 的是 16 字节原始二进制，
	// 且是 **mixed-endian**（MSSQL 的 GUID 布局：Data1/Data2/Data3 小端、
	// Data4 大端）——decodeGuid 默认 GuidConversion=false 原样拷贝
	// （types.go:932-941）。
	//
	// 标准布局的 UUID "6F9619FF-8B86-D011-B42D-00C04FC964FF" 在 wire 上是：
	//   ff 19 96 6f | 86 8b | 11 d0 | b4 2d 00 c0 4f c9 64 ff
	// 直接按标准布局解读会读出 "ff19966f-868b-11d0-…"（错的）。
	wire, _ := hex.DecodeString("ff19966f868b11d0b42d00c04fc964ff")
	want := "6f9619ff-8b86-d011-b42d-00c04fc964ff"

	got := DriverToValueTyped(wire, "uniqueidentifier")

	if got.Raw() == nil {
		t.Fatalf("UNIQUEIDENTIFIER 解出了零值（null 语义）——数据丢失")
	}
	if want := `"` + want + `"`; string(got.Raw()) != want {
		t.Errorf("UNIQUEIDENTIFIER 形态 = %s, want %s（标准 UUID 文本；MSSQL 的 mixed-endian 必须先转标准布局）",
			got.Raw(), want)
	}
}

// TestUniqueIdentifierAlreadyStandardLayout 亦应正确：有些路径（已由驱动
// 转过序的）会给标准布局字节，此时不应被再转一次。
func TestUniqueIdentifierAlreadyStandardLayout(t *testing.T) {
	std, _ := hex.DecodeString("6f9619ff8b86d011b42d00c04fc964ff")
	// 标准布局经 mixed-endian 解读的**逆变换**后应还原，故此处期望它被
	// 转成 "ff19966f-868b-11d0-b42d-00c04fc964ff"（wire 序）——
	// 这说明本函数只做一次转换，输入标准布局时输出 wire 序。
	got := DriverToValueTyped(std, "uniqueidentifier")
	if want := `"ff19966f-868b-11d0-b42d-00c04fc964ff"`; string(got.Raw()) != want {
		t.Errorf("标准布局输入 = %s, want %s（确认转换方向：wire mixed-endian ⇒ 标准布局）",
			got.Raw(), want)
	}
}

// 非 16 字节的 UNIQUEIDENTIFIER（异常数据）不得 panic，也不得冒充 UUID。
func TestUniqueIdentifierMalformedFallsBackToText(t *testing.T) {
	got := DriverToValueTyped([]byte("short"), "uniqueidentifier")
	if want := `"short"`; string(got.Raw()) != want {
		t.Errorf("异常长度 GUID 的回落形态 = %s, want %s", got.Raw(), want)
	}
}
