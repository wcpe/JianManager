package ingest

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// fakeDelivery 固定读数，用来验证输出格式与字段映射。
type fakeDelivery struct{ st InsertBatchStats }

func (f fakeDelivery) InsertBatchStats() InsertBatchStats { return f.st }

// fakeSpoolMetrics 固定读数。
type fakeSpoolMetrics struct{ m SpoolMetrics }

func (f fakeSpoolMetrics) SpoolMetricsSnapshot() SpoolMetrics { return f.m }

// TestWriteDeliveryMetricsFormat 验证 Prometheus 文本格式与字段映射。
//
// 变异验证：把某个指标的值硬编码成 0，对应断言会转红。
func TestWriteDeliveryMetricsFormat(t *testing.T) {
	src := fakeDelivery{st: InsertBatchStats{
		Batches:           10,
		Events:            5000,
		Bytes:             1 << 20,
		Requests:          12,
		Retries:           2,
		Failures:          1,
		Aborted:           3,
		MaxEventsPerBatch: 500,
		MaxBytesPerBatch:  4 << 20,
		WaitTotal:         4 * time.Second,
		WaitMax:           900 * time.Millisecond,
		AvgEventsPerBatch: 500,
		AvgBytesPerBatch:  104857.6,
	}}

	var buf bytes.Buffer
	if err := WriteDeliveryMetrics(&buf, src); err != nil {
		t.Fatalf("WriteDeliveryMetrics: %v", err)
	}
	out := buf.String()

	// 必需的指标名与数值。
	for _, want := range []string{
		"log_delivery_batches_total 10",
		"log_delivery_events_total 5000",
		"log_delivery_bytes_total 1048576",
		"log_delivery_requests_total 12",
		"log_delivery_retries_total 2",
		"log_delivery_failures_total 1",
		"log_delivery_aborted_batches_total 3",
		"log_delivery_batch_max_events 500",
		"log_delivery_batch_max_bytes 4194304",
		"log_delivery_batch_wait_max_seconds 0.9",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少 %q\n实际输出：\n%s", want, out)
		}
	}

	// 每个指标都必须有 TYPE 声明（Prometheus 抓取侧靠它判断类型）。
	if got := strings.Count(out, "# TYPE"); got < 13 {
		t.Errorf("TYPE 声明只有 %d 条，应当 ≥ 13", got)
	}
	// 计数器必须有 _total 后缀。
	if strings.Contains(out, "log_delivery_batches ") {
		t.Error("counter 应当带 _total 后缀")
	}
}

// TestWriteDeliveryMetricsNilSafe 未装配时不得 panic，且输出零值。
func TestWriteDeliveryMetricsNilSafe(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDeliveryMetrics(&buf, nil); err != nil {
		t.Fatalf("nil source 应当安全：%v", err)
	}
	if !strings.Contains(buf.String(), "log_delivery_batches_total 0") {
		t.Errorf("零值输出不对：\n%s", buf.String())
	}
}

// TestWriteSpoolMetricsFormat 验证磁盘缓冲指标。
func TestWriteSpoolMetricsFormat(t *testing.T) {
	src := fakeSpoolMetrics{m: SpoolMetrics{
		UsedBytes:     1024,
		CapacityBytes: 100 << 20,
		Records:       5,
		Dropped:       2,
		RecoveredRecs: 3,
	}}
	var buf bytes.Buffer
	if err := WriteSpoolMetrics(&buf, src); err != nil {
		t.Fatalf("WriteSpoolMetrics: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"log_spool_enabled 1",
		"log_spool_used_bytes 1024",
		"log_spool_capacity_bytes 104857600",
		"log_spool_records 5",
		"log_spool_dropped_total 2",
		"log_spool_recovered_records 3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少 %q\n实际输出：\n%s", want, out)
		}
	}
}

// TestWriteSpoolMetricsDisabled 未启用时 enabled=0。
func TestWriteSpoolMetricsDisabled(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSpoolMetrics(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "log_spool_enabled 0") {
		t.Errorf("未启用应当 enabled=0：\n%s", buf.String())
	}
}

// TestDeliveryMetricsNoHighCardinalityLabels 高基数护栏：
//
// 输出里不得出现 log_source_id 这类每源/每事件不同的标签。
// 它们会让时序库序列数爆炸，而按源看问题应该用日志本身。
func TestDeliveryMetricsNoHighCardinalityLabels(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteDeliveryMetrics(&buf, fakeDelivery{st: InsertBatchStats{Batches: 1}})
	_ = WriteSpoolMetrics(&buf, fakeSpoolMetrics{})
	out := buf.String()
	for _, bad := range []string{"log_source_id=", "event_id=", "record_start=", "instance_id="} {
		if strings.Contains(out, bad) {
			t.Errorf("指标里出现了高基数标签 %q（会让时序库爆掉）", bad)
		}
	}
}
