package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func gcHeartbeat(uuid string, available bool, count int64, millis float64) *workerpb.HeartbeatRequest {
	return &workerpb.HeartbeatRequest{
		NodeUuid: "node-1",
		InstanceMetrics: []*workerpb.InstanceMetricSample{{
			InstanceUuid:   uuid,
			ProbeAvailable: available,
			Tps:            20,
			GcCountTotal:   count,
			GcTimeMillis:   millis,
		}},
	}
}

// TestMetric_IngestHeartbeat_GCRate 据相邻心跳累计 counter 差推导 GC 次数/耗时速率（FR-465 验收 1）。
func TestMetric_IngestHeartbeat_GCRate(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()

	// 首拍：无前值 → 不出速率（与 lastNet 同局限）。
	require.NoError(t, svc.ingestHeartbeatAt(gcHeartbeat("inst-1", true, 10, 100), base))
	// 次拍 +30s：count 10→16（Δ6）、millis 100→250（Δ150）→ 0.2 count/s、5 ms/s。
	require.NoError(t, svc.ingestHeartbeatAt(gcHeartbeat("inst-1", true, 16, 250), base.Add(30*time.Second)))

	from, to := wideWindow(base.Add(30 * time.Second))
	_, series, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: "inst-1", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)

	rate := findSeries(series, model.MetricInstGCCount, "")
	require.NotNil(t, rate)
	require.Len(t, rate.Points, 1, "首拍不出速率，仅次拍有值")
	require.InDelta(t, 0.2, *rate.Points[0].Avg, 1e-9)

	gcTime := findSeries(series, model.MetricInstGCTime, "")
	require.NotNil(t, gcTime)
	require.Len(t, gcTime.Points, 1)
	require.InDelta(t, 5.0, *gcTime.Points[0].Avg, 1e-9)
}

// TestMetric_IngestHeartbeat_GCCounterResetSkipped counter 归零（实例重启）拍被跳过，不产生负速率假尖峰（验收 2）。
func TestMetric_IngestHeartbeat_GCCounterResetSkipped(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()

	require.NoError(t, svc.ingestHeartbeatAt(gcHeartbeat("inst-1", true, 10, 100), base))
	require.NoError(t, svc.ingestHeartbeatAt(gcHeartbeat("inst-1", true, 16, 250), base.Add(30*time.Second)))
	// 重启：counter 归零到 2/5（Δ 为负）→ 整拍跳过。
	require.NoError(t, svc.ingestHeartbeatAt(gcHeartbeat("inst-1", true, 2, 5), base.Add(60*time.Second)))

	from, to := wideWindow(base)
	_, series, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: "inst-1", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	rate := findSeries(series, model.MetricInstGCCount, "")
	require.NotNil(t, rate)
	require.Len(t, rate.Points, 1, "归零拍不落点，避免负速率")
	require.InDelta(t, 0.2, *rate.Points[0].Avg, 1e-9)
}

// TestMetric_IngestHeartbeat_GCProbeUnavailableNULL 探针不可用时 GC 指标写 NULL 断点（验收 3）。
func TestMetric_IngestHeartbeat_GCProbeUnavailableNULL(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	require.NoError(t, svc.ingestHeartbeatAt(gcHeartbeat("inst-1", false, 0, 0), base))

	from, to := wideWindow(base)
	_, series, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: "inst-1", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	for _, key := range []string{model.MetricInstGCCount, model.MetricInstGCTime} {
		s := findSeries(series, key, "")
		require.NotNil(t, s, "探针不可用仍写断点序列 %s", key)
		require.Len(t, s.Points, 1)
		require.Nil(t, s.Points[0].Avg, "%s 探针不可用写 NULL", key)
	}
}

// TestMetric_IngestHeartbeat_GCStateAdvancedPerBeat （m3）读前值/写现值在同一临界区内完成，
// 故每拍只产生一次差值：同一 counter 重复上报（时钟未前进或 counter 未变）不得产生额外速率。
//
// 注：本用例为顺序断言——并发场景无法在共享缓存 SQLite 测试库上稳定复现
// （并发 Ingest 会触发表锁），并发安全性由同一临界区结构 + -race 运行既有 GC 用例保证。
func TestMetric_IngestHeartbeat_GCStateAdvancedPerBeat(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	require.NoError(t, svc.ingestHeartbeatAt(gcHeartbeat("inst-s", true, 0, 0), base))

	// 同一拍（同 ts、同 counter）重复上报 3 次：状态已推进，差值恒为 0 → 只余 0 速率样本。
	for i := 0; i < 3; i++ {
		require.NoError(t, svc.ingestHeartbeatAt(gcHeartbeat("inst-s", true, 30, 300), base.Add(30*time.Second)))
	}
	from, to := wideWindow(base)
	_, series, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: "inst-s", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	rate := findSeries(series, model.MetricInstGCCount, "")
	require.NotNil(t, rate)
	for _, p := range rate.Points {
		require.GreaterOrEqual(t, *p.Avg, 0.0, "不得出现负速率")
		require.LessOrEqual(t, *p.Avg, 1.0, "重复上报不得放大速率（单次上限 30/30=1）")
	}

	// 正常推进一拍：差值正确推导（1 count/s、10 ms/s）。
	require.NoError(t, svc.ingestHeartbeatAt(gcHeartbeat("inst-s", true, 60, 600), base.Add(60*time.Second)))
	_, series2, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: "inst-s", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	rate2 := findSeries(series2, model.MetricInstGCCount, "")
	require.NotNil(t, rate2)
	var sawOne bool
	for _, p := range rate2.Points {
		if *p.Avg == 1.0 {
			sawOne = true
		}
	}
	require.True(t, sawOne, "counter 30→60 / 30s 应得 1 count/s")
}
