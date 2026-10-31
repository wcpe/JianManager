package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/spool"
)

// spoolAdapter 把 *spool.Spool 适配成 ingest.SpoolMetricsSource。
//
// 为什么要这么一层：spool 包不知道 ingest 的存在（也不应该知道），而 /metrics 的
// 指标输出代码在 ingest 包（那里已经有投递链路的读数）。两边各自定义同形结构，
// 由装配层在这里做一次转换——与 vlDiskProviderAdapter 同一个模式。
type spoolAdapter struct {
	s *spool.Spool
}

func (a spoolAdapter) SpoolMetricsSnapshot() ingest.SpoolMetrics {
	if a.s == nil {
		return ingest.SpoolMetrics{Disabled: true}
	}
	st := a.s.Stats()
	rc := a.s.Recovered()
	return ingest.SpoolMetrics{
		UsedBytes:     st.Bytes,
		CapacityBytes: st.Capacity,
		Records:       st.Records,
		Dropped:       st.Dropped,
		RecoveredRecs: rc.Records,
	}
}

// combineMetrics 把保留策略的 /metrics 输出与日志投递/缓冲指标拼成一个响应。
//
// 为什么用组合而不是改 retention.MetricsHandler 的签名：
// retention 是日志保留策略的包，让它 import ingest（那个包 13000 行）只为多写几行指标，
// 会把一条本可以很浅的依赖变成深的。组合在这里不增加任何运行期成本——
// 抓取频率是分钟级，多一次函数调用可以忽略。
//
// base 为 nil 时只输出新指标（理论上不会发生，但 nil 安全比 panic 好）。
func combineMetrics(base http.HandlerFunc, delivery ingest.DeliveryMetricsSource, sp ingest.SpoolMetricsSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if base != nil {
			// base 自己会设 Content-Type 并 Flush；直接调它的 ServeHTTP。
			base(w, r)
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		// 写错误只可能来自客户端断开，无处上报，丢弃。
		_ = writeExtraMetrics(w, delivery, sp)
	}
}

// writeExtraMetrics 输出投递与磁盘缓冲读数。
func writeExtraMetrics(w io.Writer, delivery ingest.DeliveryMetricsSource, sp ingest.SpoolMetricsSource) error {
	if err := ingest.WriteDeliveryMetrics(w, delivery); err != nil {
		return fmt.Errorf("写投递指标: %w", err)
	}
	if err := ingest.WriteSpoolMetrics(w, sp); err != nil {
		return fmt.Errorf("写缓冲指标: %w", err)
	}
	return nil
}
