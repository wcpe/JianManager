package heartbeat

import (
	"encoding/json"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/metrics"
)

// 端到端验证：托管推送帧 -> 存储 -> 心跳采集可用的快照。
//
// 走的是生产完全相同的路径：ws 侧解析出的 Metrics JSON
// → metrics.ProbeSnapshotFromPushed（含秒→毫秒换算）
// → heartbeat.PushedSnapshotStore().Store
// → heartbeat 的采集前提判定（这是真机上“托管实例指标全空”的根因所在）。
func TestManagedPushReachesHeartbeatStore(t *testing.T) {
	const instanceUUID = "170d8d98-c236-420c-a990-f8d572f8ca16"
	// 探针 MetricSnapshotFrame.metrics 的真实投影（单位：mspt 是秒）
	const metricsJSON = `{"tps":19.8,"msptAvg":0.0234,"playersOnline":3,
		"heapUsed":1191000000,"heapMax":4294967296,"threads":212,
		"cpuLoad":0.35,"uptimeSeconds":3600.5,"gcCountTotal":42,"gcTimeMillis":1250}`

	snap, ok := metrics.ProbeSnapshotFromPushed(json.RawMessage(metricsJSON))
	if !ok {
		t.Fatal("推送载荷应能解析")
	}
	PushedSnapshotStore().Store(instanceUUID, snap)
	t.Cleanup(func() { PushedSnapshotStore().Forget(instanceUUID) })

	got, found := PushedSnapshotStore().Load(instanceUUID)
	if !found {
		t.Fatal("心跳存储里应能读到刚写入的快照")
	}
	if got.TPS != 19.8 {
		t.Fatalf("TPS 应为 19.8，实际 %v", got.TPS)
	}
	// 0.0234 秒 = 23.4 毫秒（漏换算会让面板小一千倍）
	if got.MSPTAvgMillis < 23.39 || got.MSPTAvgMillis > 23.41 {
		t.Fatalf("MSPT 应为 23.4 毫秒，实际 %v", got.MSPTAvgMillis)
	}
	if got.PlayersOnline != 3 {
		t.Fatalf("在线人数应为 3，实际 %d", got.PlayersOnline)
	}
}
