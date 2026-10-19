package dbmode

// blob 命令：取回单元格里的大字段旁路引用（$polydb_blob:<uuid>）的内容并展示。
//
// 大字段不随行集传输 —— 单元格只带一个引用（spec/encoding.md §5.2），内容要经
// Client.GetBlob 按需取回。Adapter.FetchBlob 早就在，但它不在 Backend 接口里，
// UI 拿不到，于是成了死代码；这里补上 Blobber 可选接口后它才真正接上。
//
// 展示直接复用 redis 那个通用文本查看器（newValueViewer）：异步加载 + 可滚动，
// 和大字段的典型体量（可能是几 MB 的文本/JSON）正好匹配。

import (
	"fmt"
	"strings"

	"github.com/polydb/polydb/internal/tui"
	"github.com/polydb/polydb/internal/tui/backend"
	"github.com/polydb/polydb/pkg/protocol"
)

// blobFactory 打开当前（或指定）大字段引用的查看器。
//
// arg 为空时取当前选中单元格 sheet 模式下的字段；否则把 arg 当作引用使用，
// 这样 `:blob $polydb_blob:<uuid>` 也能直接看。
func blobFactory(ctx tui.AppContext, arg string) (tui.Overlay, error) {
	be := ctx.Backend()
	if be == nil {
		return nil, fmt.Errorf("blob: 没有打开的数据库连接")
	}
	blobber, ok := be.(backend.Blobber)
	if !ok {
		return nil, fmt.Errorf("blob: 当前连接不支持大字段取回")
	}

	ref := strings.TrimSpace(arg)
	if ref == "" {
		var err error
		ref, err = blobRefFromContext(ctx)
		if err != nil {
			return nil, err
		}
	}
	if err := validateBlobRef(ref); err != nil {
		return nil, err
	}

	return newValueViewer(blobViewerTitle(ref), func() (string, error) {
		b, err := blobber.FetchBlob(ref)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}), nil
}

// validateBlobRef 校验一个字符串是不是大字段引用。
func validateBlobRef(ref string) error {
	if !strings.HasPrefix(ref, protocol.BlobRefPrefix) {
		return fmt.Errorf("blob: 不是大字段引用（应以 %s 开头）：%q", protocol.BlobRefPrefix, ref)
	}
	return nil
}

// blobRefFromContext 从当前选中单元格（sheet 模式的字段）读出引用。
func blobRefFromContext(ctx tui.AppContext) (string, error) {
	f := ctx.CurrentFrame()
	if f == nil {
		return "", fmt.Errorf("blob: 当前没有结果集，无法定位单元格；可直接在命令里给出引用")
	}
	row, col := ctx.CurrentRow(), ctx.SheetFieldCursor()
	if row < 0 || row >= f.NumRows() || col < 0 || col >= f.NumCols() {
		return "", fmt.Errorf("blob: 当前没有选中的字段；可直接在命令里给出引用")
	}
	return f.CellString(row, col), nil
}

// blobViewerTitle 把引用截短做标题：完整 UUID 在标题里没有意义。
func blobViewerTitle(ref string) string {
	id := strings.TrimPrefix(ref, protocol.BlobRefPrefix)
	if len(id) > 12 {
		id = id[:12] + "…"
	}
	return "blob " + id
}
