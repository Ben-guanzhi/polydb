package dbmode

// blob 命令的回归。真实取回要连服务端（GetBlob 走 transport），这里只钉住
// 纯逻辑：引用校验与标题截短。

import (
	"strings"
	"testing"

	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/pkg/protocol"
)

// Adapter 必须实现 Blobber，否则大字段取回在 UI 侧永远拿不到。
var _ backend.Blobber = (*backend.Adapter)(nil)

func TestValidateBlobRef(t *testing.T) {
	good := protocol.BlobRefPrefix + "6f1d2c3e-0000-4000-8000-000000000000"
	if err := validateBlobRef(good); err != nil {
		t.Fatalf("合法引用被拒：%v", err)
	}
	for _, bad := range []string{"", "abc", "6f1d2c3e", "SELECT 1"} {
		if err := validateBlobRef(bad); err == nil {
			t.Fatalf("%q 不是大字段引用，应报错", bad)
		}
	}
}

// TestBlobViewerTitleTruncates pins that the title does not carry the whole
// UUID — it would push the box title off screen for no benefit.
func TestBlobViewerTitleTruncates(t *testing.T) {
	ref := protocol.BlobRefPrefix + "6f1d2c3e-1111-4222-8333-444444444444"
	title := blobViewerTitle(ref)
	if !strings.HasPrefix(title, "blob ") {
		t.Fatalf("标题应以 blob 开头, got %q", title)
	}
	if strings.Contains(title, "6f1d2c3e-1111-4222-8333-444444444444") {
		t.Fatalf("标题不应带完整 UUID, got %q", title)
	}
	if !strings.HasSuffix(title, "…") {
		t.Fatalf("截断后应带省略号, got %q", title)
	}
}
