package register

import (
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logmetrics"
)

// 日志采集面读数必须真的进入心跳负载。
//
// 与的主机级测试同源理由：心跳负载是内存结构，真机上只能看到落库结果，
// 而落库为空时无法区分「没人采到」与「采到了没写」。这里把「采集→负载」一环单独钉死。
type fakeLogMetrics struct {
	snap logmetrics.Snapshot
}

func (f fakeLogMetrics) Snapshot() logmetrics.Snapshot { return f.snap }

// TestCollectHeartbeatCarriesLogMetrics 采集器的读数必须进入心跳负载。
func TestCollectHeartbeatCarriesLogMetrics(t *testing.T) {
	SetLogMetricsObserver(fakeLogMetrics{snap: logmetrics.Snapshot{
		Available:                  true,
		StateFileSizeBytes:         1200 << 20,
		StateFileMeasured:          true,
		StateFileGrowthBytesPerSec: 117e6,
		StateFileGrowthMeasured:    true,
		PersistWriteBytesPerSec:    98e6,
		WALBacklogEntries:          50000,
		WALPausedSeconds:           10800,
		DeliveryFailureRate:        12.5,
		DeliveryFailureClass:       "status_5xx",
		VerifyBackoffCount:         850,
		SlowSQLCount:               240,
		SlowSQLMaxMs:               2400,
	}})
	t.Cleanup(func() { SetLogMetricsObserver(nil) })

	req := CollectHeartbeatData("node-uuid")

	if !req.LogMetricsAvailable {
		t.Fatal("可用时必须置 log_metrics_available")
	}
	if req.LogStateFileSizeBytes != 1200<<20 {
		t.Fatalf("索引尺寸不符: %d", req.LogStateFileSizeBytes)
	}
	if req.LogStateFileGrowthBytesPerSec != 117e6 {
		t.Fatalf("索引增速不符: %v", req.LogStateFileGrowthBytesPerSec)
	}
	if req.LogPersistWriteBytesPerSec != 98e6 {
		t.Fatalf("persist 吞吐不符: %v", req.LogPersistWriteBytesPerSec)
	}
	if req.LogWalBacklogEntries != 50000 {
		t.Fatalf("WAL 积压不符: %d", req.LogWalBacklogEntries)
	}
	if req.LogWalPausedSeconds != 10800 {
		t.Fatalf("暂停时长不符: %d", req.LogWalPausedSeconds)
	}
	if req.LogDeliveryFailureRate != 12.5 || req.LogDeliveryFailureClass != "status_5xx" {
		t.Fatalf("投递失败读数不符: %v %q", req.LogDeliveryFailureRate, req.LogDeliveryFailureClass)
	}
	if req.LogVerifyBackoffCount != 850 || req.LogSlowSqlCount != 240 || req.LogSlowSqlMaxMs != 2400 {
		t.Fatalf("校验退避/慢查询读数不符: %d %d %d",
			req.LogVerifyBackoffCount, req.LogSlowSqlCount, req.LogSlowSqlMaxMs)
	}
}

// TestCollectHeartbeatOmitsLogMetricsWhenUnavailable 采集面不可用时**整组不上报**。
//
// 这是本项最关键的一条：上报 0 会被读成「积压为零、零失败、零慢查询」——
// 恰好是最健康的读数，等于把真实故障伪装成正常。
func TestCollectHeartbeatOmitsLogMetricsWhenUnavailable(t *testing.T) {
	SetLogMetricsObserver(fakeLogMetrics{snap: logmetrics.Snapshot{Available: false}})
	t.Cleanup(func() { SetLogMetricsObserver(nil) })

	req := CollectHeartbeatData("node-uuid")
	if req.LogMetricsAvailable {
		t.Fatal("不可用时不得置 log_metrics_available")
	}
	// 全组保持零值（proto 默认），CP 侧据 available=false 不落点。
	if req.LogWalBacklogEntries != 0 || req.LogSlowSqlCount != 0 || req.LogDeliveryFailureRate != 0 {
		t.Fatal("不可用时不应上报任何读数")
	}
}

// TestCollectHeartbeatWithoutLogMetricsObserver 未注入采集器时不带日志指标，其余字段照常。
func TestCollectHeartbeatWithoutLogMetricsObserver(t *testing.T) {
	SetLogMetricsObserver(nil)
	req := CollectHeartbeatData("node-uuid")
	if req.LogMetricsAvailable {
		t.Fatal("未注入采集器时不应置 log_metrics_available")
	}
	if req.NodeUuid != "node-uuid" {
		t.Fatalf("其余字段应照常填充: %+v", req.NodeUuid)
	}
}

// TestCollectHeartbeatSizeMeasuredButGrowthMissing 尺寸测到而增速无基线时，只上报尺寸。
//
// 增速发 0 会被读成「索引停止增长」——而真相是「首拍还没有基线」。
func TestCollectHeartbeatSizeMeasuredButGrowthMissing(t *testing.T) {
	SetLogMetricsObserver(fakeLogMetrics{snap: logmetrics.Snapshot{
		Available:          true,
		StateFileSizeBytes: 4096,
		StateFileMeasured:  true,
		// 首拍：增速无基线。
		StateFileGrowthMeasured: false,
	}})
	t.Cleanup(func() { SetLogMetricsObserver(nil) })

	req := CollectHeartbeatData("node-uuid")
	if !req.LogMetricsAvailable || req.LogStateFileSizeBytes != 4096 {
		t.Fatal("尺寸测到时应上报")
	}
	if req.LogStateFileGrowthBytesPerSec != 0 {
		t.Fatal("无基线时增速应保持零值（CP 侧不落点）")
	}
}
