package service

import (
	"log"
	"strconv"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
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

// TestMetric_IngestHeartbeat_PlayersUnavailableWritesNull 直探来源可用但**在线人数缺测**时
// 落 NULL 断点而非伪造 0（FR-447；Query 有响应但缺 numplayers 的场景）。
// 注意：兼容回退刻意**不含 Query**（FR-446 复审 N1）——Query 可响应而缺 numplayers，回退会重新
// 引入「伪造 0」；故 Query-only 且未置 players_online_available 时仍写 NULL。
func TestMetric_IngestHeartbeat_PlayersUnavailableWritesNull(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	req := &workerpb.HeartbeatRequest{
		NodeUuid: "node-1",
		InstanceMetrics: []*workerpb.InstanceMetricSample{{
			InstanceUuid:   "inst-1",
			QueryAvailable: true, // 来源可用
			SlpAvailable:   false,
			PlayersOnline:  0, // 但 numplayers 缺测（Worker 明确置 false）
		}},
	}
	require.NoError(t, svc.ingestHeartbeatAt(req, base))

	from, to := wideWindow(base)
	_, instSeries, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: "inst-1", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	players := findSeries(instSeries, model.MetricInstPlayersOnline, "")
	require.NotNil(t, players, "仍写一条断点序列")
	require.Len(t, players.Points, 1)
	require.Nil(t, players.Points[0].Avg, "缺测写 NULL，绝不写成 0 在线")
}

// TestMetric_IngestHeartbeat_PlayersAvailableFromDirectProbe 直探明确给出在线人数时落真实值。
func TestMetric_IngestHeartbeat_PlayersAvailableFromDirectProbe(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	req := &workerpb.HeartbeatRequest{
		NodeUuid: "node-1",
		InstanceMetrics: []*workerpb.InstanceMetricSample{{
			InstanceUuid:           "inst-1",
			SlpAvailable:           true,
			PlayersOnline:          5,
			PlayersOnlineAvailable: true,
		}},
	}
	require.NoError(t, svc.ingestHeartbeatAt(req, base))

	from, to := wideWindow(base)
	_, instSeries, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: "inst-1", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	players := findSeries(instSeries, model.MetricInstPlayersOnline, "")
	require.NotNil(t, players)
	require.NotNil(t, players.Points[0].Avg)
	require.Equal(t, 5.0, *players.Points[0].Avg)
	// 探针不可用 → TPS 仍断点。
	tps := findSeries(instSeries, model.MetricInstTPS, "")
	require.NotNil(t, tps)
	require.Nil(t, tps.Points[0].Avg)
}

// TestMetric_IngestHeartbeat_PlayersAvailableLegacySlpSample 中间版本 Worker（有直探、置 SlpAvailable
// 与真实 PlayersOnline，但缺 players_online_available）仍落真实值而非 NULL（FR-446 复审 N1）：
// SLP 命中的实例在线人数被误判为缺测会污染全网总在线合计与 bot 容量采样。
func TestMetric_IngestHeartbeat_PlayersAvailableLegacySlpSample(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	req := &workerpb.HeartbeatRequest{
		NodeUuid: "node-1",
		InstanceMetrics: []*workerpb.InstanceMetricSample{{
			InstanceUuid:  "inst-1",
			SlpAvailable:  true, // 直探命中 SLP
			PlayersOnline: 7,    // SLP 协议必带字段，真实值
			// PlayersOnlineAvailable 缺省 false（中间版本 Worker 尚未置该位）
		}},
	}
	require.NoError(t, svc.ingestHeartbeatAt(req, base))

	from, to := wideWindow(base)
	_, instSeries, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: "inst-1", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	players := findSeries(instSeries, model.MetricInstPlayersOnline, "")
	require.NotNil(t, players)
	require.NotNil(t, players.Points[0].Avg, "SLP 可用时在线人数是协议必带真值，不得写 NULL")
	require.Equal(t, 7.0, *players.Points[0].Avg)
}

// TestMetric_IngestHeartbeat_PlayersAvailableLegacyProbeSample 旧 Worker 不置 players_online_available
// 但探针可用时仍落点（向后兼容，避免升级期曲线整段断掉）。
func TestMetric_IngestHeartbeat_PlayersAvailableLegacyProbeSample(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	req := &workerpb.HeartbeatRequest{
		NodeUuid: "node-1",
		InstanceMetrics: []*workerpb.InstanceMetricSample{{
			InstanceUuid:   "inst-1",
			ProbeAvailable: true,
			PlayersOnline:  9,
		}},
	}
	require.NoError(t, svc.ingestHeartbeatAt(req, base))

	from, to := wideWindow(base)
	_, instSeries, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: "inst-1", From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	players := findSeries(instSeries, model.MetricInstPlayersOnline, "")
	require.NotNil(t, players)
	require.NotNil(t, players.Points[0].Avg)
	require.Equal(t, 9.0, *players.Points[0].Avg)
}

func TestMetric_IngestHeartbeat_ProcessTop(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}))
	require.NoError(t, svc.db.Create(&model.Instance{
		UUID: "inst-1", Name: "survival", NodeID: 1, Type: model.InstanceTypeGeneric,
		ProcessType: model.ProcessTypeDirect, StartCommand: "sleep",
	}).Error)
	base := metricBase()

	require.NoError(t, svc.ingestHeartbeatAt(&workerpb.HeartbeatRequest{
		NodeUuid: "node-1",
		ProcessMetrics: []*workerpb.ProcessMetricSample{
			{InstanceUuid: "inst-1", Pid: 10, Name: "java", CpuPercent: 20, RssBytes: 100, CommandSummary: "java -jar server.jar", SampledAtUnixMs: base.UnixMilli()},
			{InstanceUuid: "inst-1", Pid: 11, Name: "worker", CpuPercent: 30, RssBytes: 50, SampledAtUnixMs: base.UnixMilli()},
		},
	}, base))

	got, err := svc.QueryProcessTop(ProcessTopQuery{InstanceUUID: "inst-1", Sort: "cpu", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, int32(11), got[0].PID)
	require.Equal(t, uint(1), got[0].InstanceID)
	require.Equal(t, "node-1", got[0].NodeUUID)
}

func TestMetric_QueryProcessTop_UsesLatestSamplePerInstance(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}))
	require.NoError(t, svc.db.Create(&[]model.Instance{
		{UUID: "inst-a", Name: "survival", NodeID: 1, Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "sleep"},
		{UUID: "inst-b", Name: "lobby", NodeID: 1, Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "sleep"},
	}).Error)
	base := metricBase()

	require.NoError(t, svc.IngestProcessMetrics("node-1", []*workerpb.ProcessMetricSample{
		{InstanceUuid: "inst-a", Pid: 10, Name: "java", CpuPercent: 20, SampledAtUnixMs: base.UnixMilli()},
		{InstanceUuid: "inst-b", Pid: 20, Name: "java", CpuPercent: 30, SampledAtUnixMs: base.Add(10 * time.Second).UnixMilli()},
	}, base))

	got, err := svc.QueryProcessTop(ProcessTopQuery{NodeUUID: "node-1", Sort: "cpu", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, int32(20), got[0].PID)
	require.Equal(t, int32(10), got[1].PID)
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
	freshAt := base

	// 两个在线节点（当前值用于 totals）
	require.NoError(t, svc.db.Create(&model.Node{
		Name: "n1", Status: model.NodeStatusOnline, LastHeartbeat: &freshAt, CPUUsage: 0.4, MemoryUsedMB: 1024, MemoryMB: 4096, LoadAvg1: 1, CPUCores: 4, // 25%
	}).Error)
	require.NoError(t, svc.db.Create(&model.Node{
		Name: "n2", Status: model.NodeStatusOnline, LastHeartbeat: &freshAt, CPUUsage: 0.6, MemoryUsedMB: 2048, MemoryMB: 4096, LoadAvg1: 3, CPUCores: 4, // 75%
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
	require.InDelta(t, 50.0, ov.Totals.CPUPct, 1e-4)  // (40+60)/2，float32 源值留容差
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

func TestMetric_OverviewExcludesStaleOnlineNode(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}))
	base := metricBase()
	freshAt := base.Add(-90 * time.Second)
	staleAt := base.Add(-91 * time.Second)
	require.NoError(t, svc.db.Create(&[]model.Node{
		{Name: "fresh", Status: model.NodeStatusOnline, LastHeartbeat: &freshAt, CPUUsage: 0.4, MemoryUsedMB: 1024, MemoryMB: 4096},
		{Name: "stale", Status: model.NodeStatusOnline, LastHeartbeat: &staleAt, CPUUsage: 0.9, MemoryUsedMB: 8192, MemoryMB: 8192},
	}).Error)

	got, err := svc.overviewAt(base, base.Add(-time.Hour), base, "raw")
	require.NoError(t, err)
	require.Equal(t, 2, got.Totals.NodeCount)
	require.Equal(t, 1, got.Totals.OnlineNodeCount)
	require.InDelta(t, 40.0, got.Totals.CPUPct, 1e-6)
	require.Equal(t, int64(1024)*1024*1024, got.Totals.MemUsedBytes)
}

// TestMetric_OverviewTrendQueryCountIsConstant （N-8）总览聚合曲线的查询条数必须与序列数
// （≈实例数）**无关**：原实现逐序列查点位（raw 档每序列还多发一条 COUNT），
// 而 `/metrics/overview` 被总览页每 10s 轮询一次，序列数会线性放大成持续往返成本。
func TestMetric_OverviewTrendQueryCountIsConstant(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	measure := func(seriesCount int) int {
		var queries []string
		cb := &gorm.Config{
			Logger: logger.New(log.New(&querySink{out: &queries}, "", 0), logger.Config{
				LogLevel: logger.Info, IgnoreRecordNotFoundError: true,
			}),
		}
		db2, err := gorm.Open(sqlite.Open("file:"+t.Name()+strconv.Itoa(seriesCount)+"?mode=memory&cache=shared"), cb)
		require.NoError(t, err)
		require.NoError(t, db2.AutoMigrate(&model.MetricSeries{}, &model.MetricSampleRaw{},
			&model.MetricRollup5m{}, &model.MetricRollup1h{}, &model.Node{}, &model.Instance{}))
		svc2 := NewMetricService(db2)
		for i := 0; i < seriesCount; i++ {
			uuid := "uuid-ov" + strconv.Itoa(i)
			rankSeedInstance(t, svc2, uuid, "ov"+strconv.Itoa(i))
			require.NoError(t, svc2.Ingest([]Sample{
				instSampleAt("node-1", uuid, model.MetricInstPlayersOnline, "count", now, float64(i)),
			}))
		}
		queries = queries[:0]
		_, err = svc2.Overview(now.Add(-365*24*time.Hour), now, "1h")
		require.NoError(t, err)
		return len(queries)
	}

	small := measure(4)
	large := measure(40)
	require.Equal(t, small, large,
		"聚合曲线的查询条数不得随序列数增长（%d 序列 %d 条 vs %d 序列 %d 条）", 4, small, 40, large)
	require.LessOrEqual(t, large, 12, "总览应为常数条 SQL（约 7 条），不得 N+1")
}

// TestMetric_AggregateTrendMatchesPerSeriesSemantics （N-8）单条聚合 SQL 必须与
// 原「逐序列取桶均值 → 跨序列合并」语义一致，且在 5m/1h 档与 raw 档都正确。
func TestMetric_AggregateTrendMatchesPerSeriesSemantics(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	require.NoError(t, svc.db.AutoMigrate(&model.Instance{}))
	rankSeedInstance(t, svc, "uuid-t1", "t1")
	rankSeedInstance(t, svc, "uuid-t2", "t2")

	// 两条序列在同一 1 分钟桶（raw 档对外桶）内各有两个样本 → 代表值分别是各自均值。
	require.NoError(t, svc.Ingest([]Sample{
		instSampleAt("node-1", "uuid-t1", model.MetricInstPlayersOnline, "count", base.Add(1*time.Second), 10),
		instSampleAt("node-1", "uuid-t1", model.MetricInstPlayersOnline, "count", base.Add(2*time.Second), 20),
		instSampleAt("node-1", "uuid-t2", model.MetricInstPlayersOnline, "count", base.Add(3*time.Second), 4),
		instSampleAt("node-1", "uuid-t2", model.MetricInstPlayersOnline, "count", base.Add(4*time.Second), 8),
		// 缺测样本（NULL）不参与：不得把桶代表值拉低。
		instSampleAt("node-1", "uuid-t2", model.MetricInstPlayersOnline, "count", base.Add(5*time.Second), 0),
	}))
	require.NoError(t, svc.db.Model(&model.MetricSampleRaw{}).
		Where("ts = ? AND value IS NOT NULL", base.Add(5*time.Second)).
		Update("value", nil).Error, "把最后一条改成缺测")

	from, to := base.Add(-time.Minute), base.Add(time.Minute)

	// sum=true：跨序列求和 → (10+20)/2 + (4+8)/2 = 15 + 6 = 21
	tr, err := svc.aggregateTrend(model.MetricScopeInstance, model.MetricInstPlayersOnline, "count",
		from, to, "raw", true)
	require.NoError(t, err)
	require.Len(t, tr.Points, 1)
	require.InDelta(t, 21.0, *tr.Points[0].Avg, 1e-9, "桶内先取各序列均值，再跨序列求和")
	require.Equal(t, base, tr.Points[0].TS.UTC(), "点位时间取桶起点（1 分钟桶）")

	// sum=false：跨序列求均值 → (15 + 6)/2 = 10.5
	trAvg, err := svc.aggregateTrend(model.MetricScopeInstance, model.MetricInstPlayersOnline, "count",
		from, to, "raw", false)
	require.NoError(t, err)
	require.Len(t, trAvg.Points, 1)
	require.InDelta(t, 10.5, *trAvg.Points[0].Avg, 1e-9, "跨序列取均值而非求和")

	// 跨桶：不同 1 分钟桶必须分成两个点（桶对齐基于 strftime 换算，不是 MIN(ts) 截断）。
	require.NoError(t, svc.Ingest([]Sample{
		instSampleAt("node-1", "uuid-t1", model.MetricInstPlayersOnline, "count", base.Add(2*time.Minute), 7),
	}))
	tr2, err := svc.aggregateTrend(model.MetricScopeInstance, model.MetricInstPlayersOnline, "count",
		from, to.Add(3*time.Minute), "raw", true)
	require.NoError(t, err)
	require.Len(t, tr2.Points, 2, "不同桶应分成两个点位")
	require.Equal(t, base, tr2.Points[0].TS.UTC())
	require.Equal(t, base.Add(2*time.Minute), tr2.Points[1].TS.UTC())
	require.InDelta(t, 7.0, *tr2.Points[1].Avg, 1e-9)

	// 无数据：返回空点位而非报错。
	empty, err := svc.aggregateTrend(model.MetricScopeInstance, model.MetricInstPlayersOnline, "count",
		base.Add(24*time.Hour), base.Add(25*time.Hour), "raw", true)
	require.NoError(t, err)
	require.Empty(t, empty.Points)
}

func findOverviewTrend(ov OverviewResult, metricKey string) *OverviewTrend {
	for i := range ov.Trends {
		if ov.Trends[i].MetricKey == metricKey {
			return &ov.Trends[i]
		}
	}
	return nil
}
