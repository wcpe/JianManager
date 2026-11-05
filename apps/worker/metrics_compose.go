package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
)

// combineMetrics 把保留策略的 /metrics 输出与日志投递指标拼成一个响应。
//
// 为什么用组合而不是改 retention.MetricsHandler 的签名：
// retention 是日志保留策略的包，让它 import ingest（那个包 13000 行）只为多写几行指标，
// 会把一条本可以很浅的依赖变成深的。组合在这里不增加任何运行期成本——
// 抓取频率是分钟级，多一次函数调用可以忽略。
//
// base 为 nil 时只输出新指标（理论上不会发生，但 nil 安全比 panic 好）。
func combineMetrics(base http.HandlerFunc, delivery ingest.DeliveryMetricsSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if base != nil {
			// base 自己会设 Content-Type 并 Flush；直接调它的 ServeHTTP。
			base(w, r)
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		// 写错误只可能来自客户端断开，无处上报，丢弃。
		_ = writeDeliveryMetrics(w, delivery)
	}
}

// writeDeliveryMetrics 输出投递链路读数。
func writeDeliveryMetrics(w io.Writer, delivery ingest.DeliveryMetricsSource) error {
	if err := ingest.WriteDeliveryMetrics(w, delivery); err != nil {
		return fmt.Errorf("写投递指标: %w", err)
	}
	return nil
}
