package ingest

import (
	"fmt"
	"io"
)

// DeliveryMetricsSource 是投递链路的读数接口，供 /metrics 端点使用（FR-485）。
//
// 为什么定义接口而不是直接在 metrics 包 import *Manager：
//   - retention 包（现在挂 /metrics 的地方）不该依赖 ingest（后者巨大，会把它拖进来）；
//   - 测试可以注入一个固定的读数，不必起整个 Manager。
//
// `*Manager` 天然满足本接口（InsertBatchStats 已存在）。
type DeliveryMetricsSource interface {
	InsertBatchStats() InsertBatchStats
}

// WriteDeliveryMetrics 把投递链路读数写成 Prometheus 文本格式。
//
// 口径说明：
//   - 全部是单调递增的 counter，除了三个 gauge（批量上界、当前等待）。
//   - 不把 log_source_id 做成标签：投递计数是**全局聚合**的，按源拆分会让序列数
//     从 10 条涨到 10×19 条，而源数还会随实例增减变化（那是高基数风险）。
//     按源看问题请用日志本身（VL 里已有 log_source_id 字段），不要把时序库当明细库用。
//   - 所有量都带 `_total` 后缀（如果它是 counter），或者不带（如果是 gauge）。
//     这是 Prometheus 的命名惯例，抓取侧靠后缀判断类型。
func WriteDeliveryMetrics(w io.Writer, src DeliveryMetricsSource) error {
	var st InsertBatchStats
	if src != nil {
		st = src.InsertBatchStats()
	}

	// 攒批侧：被组织出来的批（成功或失败都已含在内）。
	if _, err := fmt.Fprintf(w, "# HELP log_delivery_batches_total 被组织并尝试投递的批数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_batches_total counter\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_delivery_batches_total %d\n", st.Batches); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP log_delivery_events_total 被投递的事件数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_events_total counter\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_delivery_events_total %d\n", st.Events); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP log_delivery_bytes_total 投递的压缩前字节数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_bytes_total counter\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_delivery_bytes_total %d\n", st.Bytes); err != nil {
		return err
	}

	// 请求侧：Requests 含重试，Retries 是其中重试部分。
	if _, err := fmt.Fprintf(w, "# HELP log_delivery_requests_total 实际发出的 HTTP 请求数（含重试）\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_requests_total counter\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_delivery_requests_total %d\n", st.Requests); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP log_delivery_retries_total 重试次数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_retries_total counter\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_delivery_retries_total %d\n", st.Retries); err != nil {
		return err
	}

	// 失败侧：Failures 是重试耗尽仍失败；Aborted 是因为更早的批失败而没发出去
	//（**不是丢弃**：它们的 WAL 条目未被回收，下一轮重投）。两者必须分开报。
	if _, err := fmt.Fprintf(w, "# HELP log_delivery_failures_total 重试耗尽后仍失败的批数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_failures_total counter\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_delivery_failures_total %d\n", st.Failures); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP log_delivery_aborted_batches_total 因更早的批失败而未发出的批数（非丢弃，会重投）\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_aborted_batches_total counter\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_delivery_aborted_batches_total %d\n", st.Aborted); err != nil {
		return err
	}

	// 批量大小分布的上界读数：这是回答「攒批到底有没有把批变大」的唯一直接证据。
	if _, err := fmt.Fprintf(w, "# HELP log_delivery_batch_max_events 观测到的单批最大事件数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_batch_max_events gauge\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_delivery_batch_max_events %d\n", st.MaxEventsPerBatch); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP log_delivery_batch_max_bytes 观测到的单批最大字节数（压缩前）\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_batch_max_bytes gauge\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_delivery_batch_max_bytes %d\n", st.MaxBytesPerBatch); err != nil {
		return err
	}

	// 攒批等待：这是「实时性」的直接读数。WaitMax 持续接近 flush_after（默认 1s）
	// 说明小流量下批总是等到超时才发，端到端延迟就是 1s 量级。
	if _, err := fmt.Fprintf(w, "# HELP log_delivery_batch_wait_max_seconds 单批最长攒批等待秒数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_batch_wait_max_seconds gauge\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_delivery_batch_wait_max_seconds %g\n", st.WaitMax.Seconds()); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP log_delivery_batch_wait_avg_seconds 平均攒批等待秒数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_batch_wait_avg_seconds gauge\n"); err != nil {
		return err
	}
	avgWait := 0.0
	if st.Batches > 0 {
		avgWait = st.WaitTotal.Seconds() / float64(st.Batches)
	}
	if _, err := fmt.Fprintf(w, "log_delivery_batch_wait_avg_seconds %g\n", avgWait); err != nil {
		return err
	}

	// 平均批大小：直接回答「攒批生效了吗」。
	if _, err := fmt.Fprintf(w, "# HELP log_delivery_batch_avg_events 平均每批事件数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_batch_avg_events gauge\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_delivery_batch_avg_events %g\n", st.AvgEventsPerBatch); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP log_delivery_batch_avg_bytes 平均每批字节数（压缩前）\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_delivery_batch_avg_bytes gauge\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_delivery_batch_avg_bytes %g\n", st.AvgBytesPerBatch); err != nil {
		return err
	}

	return nil
}
