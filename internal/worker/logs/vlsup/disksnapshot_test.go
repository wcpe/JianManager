package vlsup

// VL 磁盘占用读数与 /metrics 解析的回归（2026-10-29）。
//
// 背景：盘上告警整条链路的起点就是这里的解析。错一个字段就会直接变成
// 「告警不响」或「告警误响」——后者会让运维关掉告警。

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// 真实 VL v1.52.0 的 /metrics 片段（逐字取自实跑输出，路径已脱敏）。
const realVLMetricsSample = `
# HELP vl_free_disk_space_bytes Available disk space at -storageDataPath
# TYPE vl_free_disk_space_bytes gauge
vl_free_disk_space_bytes{path="/data/vl"} 5.38967658496e+11
# HELP vl_total_disk_space_bytes Total disk space at -storageDataPath
# TYPE vl_total_disk_space_bytes gauge
vl_total_disk_space_bytes{path="/data/vl"} 1.504075358208e+12
# HELP vl_partitions Number of partitions
# TYPE vl_partitions gauge
vl_partitions 5
# HELP vl_rows_dropped_total Total number of log entries dropped
# TYPE vl_rows_dropped_total counter
vl_rows_dropped_total{reason="debug"} 0
vl_rows_dropped_total{reason="too_big_timestamp"} 0
vl_rows_dropped_total{reason="too_many_fields"} 0
vl_rows_dropped_total{reason="too_small_timestamp"} 42
`

// 字节数必须按带标签的样本正确取出，并能算出 used = total - free。
//
// 注意：这两个指标在真实 VL 上是**带标签**的（`{path="…"}`），
// 所以断言必须走 anyLabel 而不是 scalar —— 早期版本用了 scalar，
// 结果恒得 0，而那只会在"告警永不触发"时才被发现。这条用例就是为它写的。
func TestParsePrometheusDiskSpace(t *testing.T) {
	g := parsePrometheus(realVLMetricsSample)

	// 先钉住前提：这两个指标确实是带标签的。
	if v := g.scalar(vlMetricTotalDiskSpace); v != 0 {
		t.Fatalf("前提失效：真实样里 total 不应能被无标签 scalar 读到（得到 %v）", v)
	}

	total := g.anyLabel(vlMetricTotalDiskSpace)
	free := g.anyLabel(vlMetricFreeDiskSpace)
	if total <= 0 || free <= 0 {
		t.Fatalf("total=%v free=%v，两个都应有值", total, free)
	}
	if total < free {
		t.Fatalf("total(%v) 应 >= free(%v)", total, free)
	}
	// used = total - free 必须为正且小于 total（否则 used 就是脏值）。
	used := total - free
	if used <= 0 || used >= total {
		t.Fatalf("used=%v 不合理（total=%v free=%v）", used, total, free)
	}
	if parts := g.scalar(vlMetricPartitions); parts != 5 {
		t.Fatalf("vl_partitions 应为 5（无标签），实际 %v", parts)
	}
}

// 丢弃计数**必须按 reason 取值**：把四个 reason 加总会把正常丢弃的 debug 日志
// 也算成「数据丢了」，那是最典型的误报来源。
func TestParsePrometheusRowsDroppedIsPerReason(t *testing.T) {
	g := parsePrometheus(realVLMetricsSample)

	got := g.withLabel(vlMetricRowsDropped, "reason", vlDropReasonTooSmall)
	if got != 42 {
		t.Fatalf("too_small_timestamp 应为 42，实际 %v", got)
	}
	// 反向对照：不能取到其它 reason 的值。
	if v := g.withLabel(vlMetricRowsDropped, "reason", "debug"); v != 0 {
		t.Fatalf("debug reason 应为 0，实际 %v（说明按 reason 取值失效）", v)
	}
	// 缺失的 reason 返回 0，不是报错。
	if v := g.withLabel(vlMetricRowsDropped, "reason", "nonexistent"); v != 0 {
		t.Fatalf("不存在的 reason 应返回 0，实际 %v", v)
	}
}

// 注释行 / 空行 / 脏行不得干扰解析（宁可少读，不可读错）。
func TestParsePrometheusIgnoresCommentsAndJunk(t *testing.T) {
	g := parsePrometheus("# comment\n\nvl_partitions 3\nnot-a-metric\nvl_rows_dropped_total{reason=\"too_small_timestamp\"} abc\n")
	if v := g.scalar(vlMetricPartitions); v != 3 {
		t.Fatalf("vl_partitions 应为 3，实际 %v", v)
	}
	// 值不是数字 ⇒ 丢弃该行，不当 0 也不当脏值。
	if v := g.withLabel(vlMetricRowsDropped, "reason", vlDropReasonTooSmall); v != 0 {
		t.Fatalf("非法值应被忽略（返回 0），实际 %v", v)
	}
}

// 多标签行也能正确按任一标签取值（VL 未来给指标加标签时不至于全盘失效）。
func TestParsePrometheusMultipleLabels(t *testing.T) {
	g := parsePrometheus(`vl_rows_dropped_total{path="/data",reason="too_small_timestamp"} 7`)
	if v := g.withLabel(vlMetricRowsDropped, "reason", vlDropReasonTooSmall); v != 7 {
		t.Fatalf("多标签行取值失败，得到 %v", v)
	}
}

// 未注入 supervisor / 无实例时返回零值（= 无读数），不能返回「0 字节」。
func TestVLDiskSnapshotWithoutInstanceIsZero(t *testing.T) {
	var s *Supervisor
	if r := s.VLDiskSnapshot(); r.UsedBytes != 0 || r.LimitBytes != 0 || r.Partitions != 0 || r.RowsDroppedTotal != 0 {
		t.Fatalf("nil supervisor 应返回零值，实际 %+v", r)
	}
}

// VLDiskSnapshot 的**组装路径**必须按 reason 取丢弃计数，而不是同一个指标名的任意样本。
//
// 为什么单独一条：上面的解析用例只能证明 parsePrometheus 会取，
// 而“VLDiskSnapshot 到底传了哪个参数”是另一回事——变异验证证实过：
// 把 withLabel(...) 改成 scalar(...) 时，只有解析用例的话**全部存活**。
// 这条用例直接起一个假的 /metrics 端点走完整路径，把那个变异钉死。
func TestVLDiskSnapshotUsesPerReasonDropped(t *testing.T) {
	// 假 VL：只回一个最小 /metrics，但里刻意把几个 reason 写成不同值。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
vl_free_disk_space_bytes{path="/data"} 400
vl_total_disk_space_bytes{path="/data"} 1000
vl_partitions 4
vl_rows_dropped_total{reason="debug"} 999
vl_rows_dropped_total{reason="too_many_fields"} 888
vl_rows_dropped_total{reason="too_big_timestamp"} 777
vl_rows_dropped_total{reason="too_small_timestamp"} 5
`))
	}))
	defer srv.Close()

	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("拆解 httptest 地址失败: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("端口不是数字: %v", err)
	}

	s := &Supervisor{
		instances: map[Namespace]*instance{
			NamespaceHot: {cfg: InstanceConfig{
				Namespace:              NamespaceHot,
				Port:                   port,
				StorageDataPath:        "/data",
				MaxDiskSpaceUsageBytes: 10000,
			}},
		},
	}

	r := s.VLDiskSnapshot()
	if r.UsedBytes != 600 {
		t.Fatalf("used 应为 total-free=600，实际 %d", r.UsedBytes)
	}
	if r.LimitBytes != 10000 {
		t.Fatalf("limit 应原样带出 10000，实际 %d", r.LimitBytes)
	}
	if r.Partitions != 4 {
		t.Fatalf("partitions 应为 4，实际 %d", r.Partitions)
	}
	// 关键断言：只取 too_small_timestamp(=5)，**不得**取到 debug(999)/too_many_fields(888)/too_big_timestamp(777)。
	if r.RowsDroppedTotal != 5 {
		t.Fatalf("RowsDroppedTotal 必须是 too_small_timestamp 的值 5，实际 %d"+
			"（若是 999/888/777 说明把无关 reason 当成数据丢失了）", r.RowsDroppedTotal)
	}
}
