package pipeline

import "github.com/wcpe/JianManager/internal/worker/logs/logtypes"

// DeliveryResult 请求级投递结果。
// 契约：HTTP 2xx 只写 REQUEST_DONE；响应丢失 → UNKNOWN；2xx 单独不得 reclaim。
type DeliveryResult struct {
	HTTPStatus int
	AckLost    bool
}

// DeliveryHook WAL durable 之后的投递边界（VL JSON insert / 测试替身）。
type DeliveryHook interface {
	// Deliver 投递一批完整事件。
	// 成功时返回 HTTP 状态码；ackLost=true 表示请求可能已成功但 ACK 丢失。
	Deliver(events []logtypes.Event) (DeliveryResult, error)
}

// FuncHook 将函数适配为 DeliveryHook。
type FuncHook func(events []logtypes.Event) (DeliveryResult, error)

// Deliver 实现 DeliveryHook。
func (f FuncHook) Deliver(events []logtypes.Event) (DeliveryResult, error) {
	return f(events)
}

// NopHook 只记账不投递（测试/降级）。
type NopHook struct{}

// Deliver 返回 0 状态与 nil 错误；调用方应视为未投递。
func (NopHook) Deliver(events []logtypes.Event) (DeliveryResult, error) {
	_ = events
	return DeliveryResult{HTTPStatus: 0}, nil
}
