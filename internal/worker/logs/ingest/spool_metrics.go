package ingest

import (
	"fmt"
	"io"
)

// SpoolMetricsSource 是磁盘缓冲的读数接口（FR-485）。
//
// `*spool.Spool` 不直接满足（它的 Stats 返回 spool.Stats），
// 但可以由装配层用一个极小的适配器包一层，避免 ingest 依赖 spool 的类型。
type SpoolMetricsSource interface {
	SpoolMetricsSnapshot() SpoolMetrics
}

// SpoolMetrics 是磁盘缓冲的读数快照。
type SpoolMetrics struct {
	UsedBytes       int64
	CapacityBytes   int64
	Records         uint64
	Dropped         uint64
	RecoveredRecs   uint64
	Disabled        bool
}

// WriteSpoolMetrics 把磁盘缓冲读数写成 Prometheus 文本格式。
//
// 为什么这几个指标重要：磁盘缓冲是「VL 不可用时不丢日志」的唯一保障，
// 但它的工作是**安静的**——不丢的时候什么都不发生。没有这些读数，
// 运维无法分辨「缓冲在正常工作」与「缓冲根本没启用」。
func WriteSpoolMetrics(w io.Writer, src SpoolMetricsSource) error {
	var m SpoolMetrics
	enabled := int64(0)
	if src != nil {
		m = src.SpoolMetricsSnapshot()
		if !m.Disabled {
			enabled = 1
		}
	}

	if _, err := fmt.Fprintf(w, "# HELP log_spool_enabled 磁盘缓冲是否启用\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_spool_enabled gauge\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_spool_enabled %d\n", enabled); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP log_spool_used_bytes 缓冲当前占用的可读字节数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_spool_used_bytes gauge\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_spool_used_bytes %d\n", m.UsedBytes); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP log_spool_capacity_bytes 缓冲容量上限\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_spool_capacity_bytes gauge\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_spool_capacity_bytes %d\n", m.CapacityBytes); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP log_spool_records 缓冲中待重投的记录数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_spool_records gauge\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_spool_records %d\n", m.Records); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP log_spool_dropped_total 因容量淘汰而丢弃的记录数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_spool_dropped_total counter\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_spool_dropped_total %d\n", m.Dropped); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP log_spool_recovered_records 启动时从磁盘恢复出的记录数\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# TYPE log_spool_recovered_records gauge\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "log_spool_recovered_records %d\n", m.RecoveredRecs); err != nil {
		return err
	}

	return nil
}
