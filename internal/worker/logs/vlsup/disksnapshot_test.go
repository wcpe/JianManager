package vlsup

// VL 磁盘占用读数与 /metrics 解析的回归（2026-10-29）。
//
// 背景：盘上告警整条链路的起点就是这里的解析。错一个字段就会直接变成
// 「告警不响」或「告警误响」——后者会让运维关掉告警。

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 真实 VL v1.52.0 的 /metrics 片段（逐字取自实跑输出，路径已脱敏）。
//
// 注意：`vl_free_disk_space_bytes` / `vl_total_disk_space_bytes` **仍在样里**，
// 但它们只用于证明「我们不拿它们算 used」（下面有专门的防回归用例）。
const realVLMetricsSample = `
# HELP vl_free_disk_space_bytes Available disk space at -storageDataPath
# TYPE vl_free_disk_space_bytes gauge
vl_free_disk_space_bytes{path="/data/vl"} 534399922176
# HELP vl_total_disk_space_bytes Total disk space at -storageDataPath
# TYPE vl_total_disk_space_bytes gauge
vl_total_disk_space_bytes{path="/data/vl"} 1504075358208
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

// 分区数与丢弃数解析正确。
func TestParsePrometheusCounters(t *testing.T) {
	g := parsePrometheus(realVLMetricsSample)
	if parts := g.scalar(vlMetricPartitions); parts != 5 {
		t.Fatalf("vl_partitions 应为 5，实际 %v", parts)
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
vl_partitions 4
vl_rows_dropped_total{reason="debug"} 999
vl_rows_dropped_total{reason="too_many_fields"} 888
vl_rows_dropped_total{reason="too_big_timestamp"} 777
vl_rows_dropped_total{reason="too_small_timestamp"} 5
`))
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	// 建两个已知大小的文件：600 字节总和。
	if err := os.WriteFile(filepath.Join(dataDir, "a.bin"), bytes.Repeat([]byte("x"), 400), 0o600); err != nil {
		t.Fatalf("写夹具失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "sub-dir-marker.bin"), bytes.Repeat([]byte("x"), 200), 0o600); err != nil {
		t.Fatalf("写夹具失败: %v", err)
	}

	s := &Supervisor{
		instances: map[Namespace]*instance{
			NamespaceHot: {cfg: InstanceConfig{
				Namespace:              NamespaceHot,
				Port:                   portOfTestServer(t, srv),
				StorageDataPath:        dataDir,
				MaxDiskSpaceUsageBytes: 10000,
			}},
		},
	}

	r := s.VLDiskSnapshot()
	if r.UsedBytes != 600 {
		t.Fatalf("used 应为目录实际大小 600，实际 %d", r.UsedBytes)
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

// portOfTestServer 取 httptest 服务的监听端口。
func portOfTestServer(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("拆解 httptest 地址失败: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("端口不是数字: %v", err)
	}
	return port
}

// ⚠ 防回归（2026-10-29 真机缺陷）：UsedBytes **必须**是 VL 目录自身的占用，
// 不得是「整块磁盘已用」(total-free)。
//
// 现场事故：第一版用 `vl_total_disk_space_bytes - vl_free_disk_space_bytes` 当 used，
// 部署后在 CP 时序库里读到 `log_vl_disk_used = 903 GiB`、`log_vl_disk_pct = 903%`，
// 而 VL 目录实际只有 22.3 GiB —— 多出的 880 GiB 是游戏服/数据库/其它。
// 拿这个值去比 100GiB 上限，告警会立刻误报（而且永远“超限”）。
//
// 本用例故意让假 /metrics 里的 total/free 差得很大（1TiB 已用），
// 而目录里只放 600 字节 —— 若实现又回去用 total-free，这条立红。
func TestVLDiskSnapshotUsedIsDirectorySizeNotFilesystem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 差 = 1TiB，若被当作 used，远大于下面的 600 与目录实际值。
		_, _ = w.Write([]byte(`
vl_free_disk_space_bytes{path="/x"} 100
vl_total_disk_space_bytes{path="/x"} 1099511627876
vl_partitions 3
vl_rows_dropped_total{reason="too_small_timestamp"} 0
`))
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "only.bin"), bytes.Repeat([]byte("y"), 600), 0o600); err != nil {
		t.Fatalf("写夹具失败: %v", err)
	}

	s := &Supervisor{instances: map[Namespace]*instance{
		NamespaceHot: {cfg: InstanceConfig{
			Namespace: NamespaceHot, Port: portOfTestServer(t, srv),
			StorageDataPath: dataDir, MaxDiskSpaceUsageBytes: 107374182400,
		}},
	}}

	r := s.VLDiskSnapshot()
	if r.UsedBytes != 600 {
		t.Fatalf("used 必须是目录自身大小 600，实际 %d"+
			"（若是 ~1TiB 说明又用 total-free 当目录占用了 —— 那会得出 >100%% 的 disk_pct）", r.UsedBytes)
	}
	if pct := float64(r.UsedBytes) / float64(r.LimitBytes) * 100; pct > 100 {
		t.Fatalf("used/limit 得出 %.2f%%，不可能超过 100%%", pct)
	}
}
