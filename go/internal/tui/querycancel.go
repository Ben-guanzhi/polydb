package tui

// 在途查询的取消（F8）。
//
// 查询经 transport.Client.ExecuteQuery 带上客户端预分配的 queryID 执行，F8 把
// 该 ID 交给 CancelQuery。状态放在 tui 包而不是 popup 包：popup 依赖 tui，
// 反过来 import 会成环。
//
// Local 没有服务端 in-flight registry，CancelQuery 恒返回未命中 —— 未命中不是
// 错误（behavior.md §2.2，best-effort），此时状态栏如实说明：进程内取消靠调用
// 方中断自己的上下文。

import (
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/polydb/polydb/internal/tui/backend"
)

// inFlight 记录当前在途查询的 queryID；空串表示没有在途查询。
var inFlight struct {
	mu sync.Mutex
	id string
}

// SetInFlightQuery 在一条查询开始执行时记下它的 queryID，作为之后取消的凭据。
func SetInFlightQuery(id string) {
	inFlight.mu.Lock()
	inFlight.id = id
	inFlight.mu.Unlock()
}

// ClearInFlightQuery 在查询结束后清掉记录。只清自己那一轮，避免把期间后发起
// 的另一条查询的 ID 误清掉。
func ClearInFlightQuery(id string) {
	inFlight.mu.Lock()
	if inFlight.id == id {
		inFlight.id = ""
	}
	inFlight.mu.Unlock()
}

// currentInFlight 取出在途 ID（空串表示没有）。
func currentInFlight() string {
	inFlight.mu.Lock()
	defer inFlight.mu.Unlock()
	return inFlight.id
}

// CancelInFlightQuery 是 F8 的动作：取消当前在途查询，并把结果作为提示投递。
func CancelInFlightQuery(ctx AppContext) tea.Cmd {
	return func() tea.Msg {
		if currentInFlight() == "" {
			return ToastMsg{Text: "没有在途查询可取消"}
		}
		be := ctx.Backend()
		if be == nil {
			return ToastMsg{Text: "没有在途查询可取消"}
		}
		c, ok := be.(backend.Canceller)
		if !ok {
			return ToastMsg{Text: "当前连接不支持取消"}
		}
		id := currentInFlight()
		hit, err := c.Cancel(id)
		switch {
		case err != nil:
			return ToastMsg{Text: "取消失败：" + err.Error()}
		case !hit:
			return ToastMsg{Text: "取消未命中：Local 模式无服务端取消"}
		default:
			return ToastMsg{Text: "已请求取消"}
		}
	}
}
