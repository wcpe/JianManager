package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// findSeries 从 QuerySeries 结果里按 metricKey(+world) 取一条序列；找不到返回 nil。
func findSeries(series []Series, metricKey, world string) *Series {
	for i := range series {
		if series[i].MetricKey == metricKey && series[i].World == world {
			return &series[i]
		}
	}
	return nil
}

// wideWindow 返回覆盖 base 的查询区间，便于 raw 档断言全部样本。
func wideWindow(base time.Time) (time.Time, time.Time) {
	return base.Add(-time.Hour), base.Add(time.Hour)
}

func TestMetric_IngestHeartbeat_NodeAndInstance(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	req := &workerpb.HeartbeatRequest{
		NodeUuid:     "node-1",
		CpuUsage:     0.5, // → 50 pct
		MemoryUsedMb: 2048,
		DiskUsedMb:   10240,
		LoadAvg1:     3.5,
		InstanceMetrics: []*workerpb.InstanceMetricSample{{
			InstanceUuid:   "inst-1",
			ProbeAvailable: true,
			Tps:            19.5,
			MsptMillis:     12.3,
			PlayersOnline:  7,
			HeapUsedBytes:  1 << 30,
			HeapMaxBytes:   2 << 30,
			Threads:        42,
			CpuLoad:        0.25, // → 25 pct
			UptimeSeconds:  3600,
			Worlds: []*workerpb.WorldMetric{
				{Name: "world", LoadedChunks: 100, Entities: 50, TileEntities: 20},
			},
		}},
	}
	require.NoError(t, svc.ingestHeartbeatAt(req, base))

	from, to := wideWindow(base)

	// 节点维度
	_, nodeSeries, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeNode, NodeUUID: "node-1", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	cpu := findSeries(nodeSeries, model.MetricNodeCPUPct, "")
	require.NotNil(t, cpu)
	require.Equal(t, 50.0, *cpu.Points[0].Avg)
	mem := findSeries(nodeSeries, model.MetricNodeMemUsed, "")
	require.NotNil(t, mem)
	require.Equal(t, float64(2048)*1024*1024, *mem.Points[0].Avg)
	loadS := findSeries(nodeSeries, model.MetricNodeLoad, "")
	require.NotNil(t, loadS, "节点 load average 落 node_load 时序")
	require.Equal(t, 3.5, *loadS.Points[0].Avg)

	// 实例维度 + 世界维度（同一 instance_id 下都返回）
	_, instSeries, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: "inst-1", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	tps := findSeries(instSeries, model.MetricInstTPS, "")
	require.NotNil(t, tps)
	require.Equal(t, 19.5, *tps.Points[0].Avg)
	instCPU := findSeries(instSeries, model.MetricInstCPUPct, "")
	require.NotNil(t, instCPU)
	require.Equal(t, 25.0, *instCPU.Points[0].Avg)
	players := findSeries(instSeries, model.MetricInstPlayersOnline, "")
	require.NotNil(t, players)
	require.Equal(t, 7.0, *players.Points[0].Avg)

	chunks := findSeries(instSeries, model.MetricWorldLoadedChunks, "world")
	require.NotNil(t, chunks, "分世界负载落 world 维度序列")
	require.Equal(t, 100.0, *chunks.Points[0].Avg)
}

func TestMetric_IngestHeartbeat_ProbeUnavailableWritesNull(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	req := &workerpb.HeartbeatRequest{
		NodeUuid: "node-1",
		InstanceMetrics: []*workerpb.InstanceMetricSample{
			{InstanceUuid: "inst-1", ProbeAvailable: false},
		},
	}
	require.NoError(t, svc.ingestHeartbeatAt(req, base))

	from, to := wideWindow(base)
	_, instSeries, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: "inst-1", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	tps := findSeries(instSeries, model.MetricInstTPS, "")
	require.NotNil(t, tps, "探针不可用仍写一条断点序列")
	require.Len(t, tps.Points, 1)
	require.Nil(t, tps.Points[0].Avg, "探针不可用写 NULL 断点")
	// 探针不可用时不应写堆/线程等其他指标
	require.Nil(t, findSeries(instSeries, model.MetricInstHeapUsed, ""))
}

func TestMetric_IngestHeartbeat_NetworkRateFromCumulative(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()

	// 首拍：仅累计字节，无速率
	require.NoError(t, svc.ingestHeartbeatAt(&workerpb.HeartbeatRequest{
		NodeUuid: "node-1", NetworkBytesSent: 1000, NetworkBytesRecv: 2000,
	}, base))
	// 30s 后：发送 +3000、接收 +6000 → tx=100/s, rx=200/s
	require.NoError(t, svc.ingestHeartbeatAt(&workerpb.HeartbeatRequest{
		NodeUuid: "node-1", NetworkBytesSent: 4000, NetworkBytesRecv: 8000,
	}, base.Add(30*time.Second)))

	from, to := wideWindow(base)
	_, nodeSeries, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeNode, NodeUUID: "node-1", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)

	tx := findSeries(nodeSeries, model.MetricNodeNetTxRate, "")
	require.NotNil(t, tx)
	require.Len(t, tx.Points, 1, "首拍无速率，仅第二拍出速率样本")
	require.InDelta(t, 100.0, *tx.Points[0].Avg, 1e-9)
	rx := findSeries(nodeSeries, model.MetricNodeNetRxRate, "")
	require.NotNil(t, rx)
	require.InDelta(t, 200.0, *rx.Points[0].Avg, 1e-9)
}

func TestMetric_IngestHeartbeat_NetworkCounterResetSkipped(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	require.NoError(t, svc.ingestHeartbeatAt(&workerpb.HeartbeatRequest{
		NodeUuid: "node-1", NetworkBytesSent: 10000, NetworkBytesRecv: 20000,
	}, base))
	// 计数器回绕（节点重启）：累计字节变小 → 不出负速率
	require.NoError(t, svc.ingestHeartbeatAt(&workerpb.HeartbeatRequest{
		NodeUuid: "node-1", NetworkBytesSent: 5, NetworkBytesRecv: 5,
	}, base.Add(30*time.Second)))

	from, to := wideWindow(base)
	_, nodeSeries, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeNode, NodeUUID: "node-1", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	require.Nil(t, findSeries(nodeSeries, model.MetricNodeNetTxRate, ""), "计数器回绕拍不产出速率")
}

func TestMetric_Overview(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}))
	base := metricBase()

	// 两个在线节点（当前值用于 totals）
	require.NoError(t, svc.db.Create(&model.Node{
		Name: "n1", Status: model.NodeStatusOnline, CPUUsage: 0.4, MemoryUsedMB: 1024, MemoryMB: 4096, LoadAvg1: 1, CPUCores: 4, // 25%
	}).Error)
	require.NoError(t, svc.db.Create(&model.Node{
		Name: "n2", Status: model.NodeStatusOnline, CPUUsage: 0.6, MemoryUsedMB: 2048, MemoryMB: 4096, LoadAvg1: 3, CPUCores: 4, // 75%
	}).Error)
	require.NoError(t, svc.db.Create(&model.Node{
		Name: "n3", Status: model.NodeStatusOffline, CPUUsage: 0.9, MemoryUsedMB: 9999, MemoryMB: 4096,
	}).Error)
	require.NoError(t, svc.db.Create(&model.Instance{
		UUID: "i1", Name: "i1", NodeID: 1, Type: "paper", ProcessType: "daemon",
		Status: model.InstanceStatusRunning, StartCommand: "x",
	}).Error)

	// 两节点心跳样本（同一分钟桶 → 跨序列对齐聚合）
	require.NoError(t, svc.ingestHeartbeatAt(&workerpb.HeartbeatRequest{
		NodeUuid: "node-a", CpuUsage: 0.4, MemoryUsedMb: 1024,
		InstanceMetrics: []*workerpb.InstanceMetricSample{
			{InstanceUuid: "i1", ProbeAvailable: true, PlayersOnline: 5, Tps: 20},
		},
	}, base))
	require.NoError(t, svc.ingestHeartbeatAt(&workerpb.HeartbeatRequest{
		NodeUuid: "node-b", CpuUsage: 0.6, MemoryUsedMb: 2048,
		InstanceMetrics: []*workerpb.InstanceMetricSample{
			{InstanceUuid: "i2", ProbeAvailable: true, PlayersOnline: 3, Tps: 20},
		},
	}, base))

	from, to := wideWindow(base)
	ov, err := svc.overviewAt(base, from, to, "raw")
	require.NoError(t, err)

	// 当前总量（来自 Node/Instance 表 + 最近样本）
	require.Equal(t, 3, ov.Totals.NodeCount)
	require.Equal(t, 2, ov.Totals.OnlineNodeCount)
	require.Equal(t, 1, ov.Totals.RunningInstances)
	require.InDelta(t, 50.0, ov.Totals.CPUPct, 1e-4) // (40+60)/2，float32 源值留容差
	require.InDelta(t, 50.0, ov.Totals.LoadAvg, 1e-9) // 负载利用率均值 (25+75)/2
	require.Equal(t, int64(3072)*1024*1024, ov.Totals.MemUsedBytes)
	require.Equal(t, int64(8), ov.Totals.OnlinePlayers) // 5+3 最近样本

	// 聚合曲线：CPU 跨节点均值、内存合计、玩家合计
	cpu := findOverviewTrend(ov, model.MetricNodeCPUPct)
	require.NotNil(t, cpu)
	require.Len(t, cpu.Points, 1)
	require.InDelta(t, 50.0, *cpu.Points[0].Avg, 1e-4) // (40+60)/2 同桶两序列均值，float32 源值留容差
	mem := findOverviewTrend(ov, model.MetricNodeMemUsed)
	require.NotNil(t, mem)
	require.InDelta(t, float64(3072)*1024*1024, *mem.Points[0].Avg, 1.0) // 合计
	players := findOverviewTrend(ov, model.MetricInstPlayersOnline)
	require.NotNil(t, players)
	require.InDelta(t, 8.0, *players.Points[0].Avg, 1e-9) // 5+3 合计
}

func findOverviewTrend(ov OverviewResult, metricKey string) *OverviewTrend {
	for i := range ov.Trends {
		if ov.Trends[i].MetricKey == metricKey {
			return &ov.Trends[i]
		}
	}
	return nil
}
