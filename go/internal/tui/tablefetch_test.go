package tui

// 过滤下推接线的回归：能下推就下推，不能就退回整页取回，
// 且下推失败绝不能让取数本身报错。

import (
	"testing"

	"github.com/polydb/polydb/internal/tuidata"
	"github.com/polydb/polydb/pkg/protocol"
)

// fetchSpy 在 fakeBackend 之上记录两条取数路径各被走了几次。
type fetchSpy struct {
	fakeBackend
	plainCalls    int
	filteredCalls int
	lastConds     []protocol.FilterCondition
}

func (s *fetchSpy) FetchTable(ns, table string, limit int) (*tuidata.Frame, error) {
	s.plainCalls++
	return &tuidata.Frame{}, nil
}

func (s *fetchSpy) FetchTableWithFilters(ns, table string, limit int, conds []protocol.FilterCondition) (*tuidata.Frame, error) {
	s.filteredCalls++
	s.lastConds = conds
	return &tuidata.Frame{}, nil
}

func TestFetchTableNoFilterUsesPlainPath(t *testing.T) {
	s := &fetchSpy{}
	if _, err := fetchTableWithFilter(s, "", "users", ""); err != nil {
		t.Fatal(err)
	}
	if s.plainCalls != 1 || s.filteredCalls != 0 {
		t.Fatalf("无过滤应走整页取回，实际 plain=%d filtered=%d", s.plainCalls, s.filteredCalls)
	}
}

// TestFetchTableUnparseableFilterFallsBack pins the important safety property:
// a filter the parser cannot understand must not break the fetch — it just
// stays a client-side filter.
func TestFetchTableUnparseableFilterFallsBack(t *testing.T) {
	s := &fetchSpy{}
	if _, err := fetchTableWithFilter(s, "", "users", "just text"); err != nil {
		t.Fatalf("无法解析的过滤不应让取数报错：%v", err)
	}
	if s.plainCalls != 1 || s.filteredCalls != 0 {
		t.Fatalf("应退回整页取回，实际 plain=%d filtered=%d", s.plainCalls, s.filteredCalls)
	}
}

func TestFetchTablePushesDownWhenParseable(t *testing.T) {
	s := &fetchSpy{}
	if _, err := fetchTableWithFilter(s, "", "users", "age > 30"); err != nil {
		t.Fatal(err)
	}
	if s.filteredCalls != 1 {
		t.Fatalf("可解析且后端支持时应下推，实际 filtered=%d", s.filteredCalls)
	}
	if len(s.lastConds) != 1 || s.lastConds[0].Column != "age" || s.lastConds[0].Op != protocol.FilterOpGt {
		t.Fatalf("下推的条件 = %+v", s.lastConds)
	}
}

// TestFetchTableWithoutFilteredBackend pins that a backend that does not
// implement backend.Filtered keeps working.
func TestFetchTableWithoutFilteredBackend(t *testing.T) {
	be := fakeBackend{}
	if _, err := fetchTableWithFilter(be, "", "users", "age > 30"); err != nil {
		t.Fatalf("后端不支持下推时应照常取数：%v", err)
	}
}

func TestTabFilterNilContext(t *testing.T) {
	if got := tabFilter(nil); got != "" {
		t.Fatalf("nil ctx 应返回空串, got %q", got)
	}
}
