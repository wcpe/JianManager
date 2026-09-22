package service

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func constSlice(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// TestAnalyzeAttributionFactors_GCDominates 注入 GC 抖动后归因指向 GC 因子（FR-465 验收 4/5）。
func TestAnalyzeAttributionFactors_GCDominates(t *testing.T) {
	const n = 60
	target := constSlice(n, 20)
	gc := constSlice(n, 50)
	blocks := constSlice(n, 100)
	for i := 40; i < n; i++ {
		target[i] = 15 // 后 1/3 劣化
		gc[i] = 500
	}
	factors, samples, status := AnalyzeAttributionFactors(target, []AttributionFactorInput{
		{MetricKey: model.MetricInstGCTime, Label: "GC 暂停占用", Values: gc},
		{MetricKey: model.MetricWorldLoadedChunks, Label: "已加载区块", Values: blocks},
	})
	require.Equal(t, "ok", status)
	require.Equal(t, n, samples)
	require.NotEmpty(t, factors)
	require.Equal(t, model.MetricInstGCTime, factors[0].MetricKey, "GC 为劣化主因")
	require.InDelta(t, 1.0, factors[0].Weight, 1e-9)
	require.Negative(t, factors[0].Correlation, "TPS 越低 GC 越高 → 负相关")
	require.Len(t, factors, 1, "无方差因子（区块恒定）被跳过")
}

// TestAnalyzeAttributionFactors_Insufficient 样本不足返回 insufficient 且不给排序（验收 4）。
func TestAnalyzeAttributionFactors_Insufficient(t *testing.T) {
	target := constSlice(10, 20)
	gc := constSlice(10, 50)
	factors, samples, status := AnalyzeAttributionFactors(target, []AttributionFactorInput{
		{MetricKey: model.MetricInstGCTime, Label: "GC", Values: gc},
	})
	require.Equal(t, "insufficient", status)
	require.Nil(t, factors)
	require.Equal(t, 10, samples)
}

// TestAnalyzeAttributionFactors_NoSignal 目标无波动时无显著因子 → insufficient，不伪造排序。
func TestAnalyzeAttributionFactors_NoSignal(t *testing.T) {
	target := constSlice(40, 20)
	gc := constSlice(40, 50)
	factors, samples, status := AnalyzeAttributionFactors(target, []AttributionFactorInput{
		{MetricKey: model.MetricInstGCTime, Label: "GC", Values: gc},
	})
	require.Equal(t, "insufficient", status)
	require.Nil(t, factors)
	require.Equal(t, 40, samples)
}

// TestAnalyzeAttributionFactors_NaNAlign 缺测（NaN）样本对在相关性计算中被跳过而非当 0。
func TestAnalyzeAttributionFactors_NaNAlign(t *testing.T) {
	r, n := pearsonR([]float64{1, 2, math.NaN(), 4}, []float64{2, 4, 99, 8})
	require.Equal(t, 3, n)
	require.InDelta(t, 1.0, r, 1e-9)
}

// attributionHeartbeat 构造一拍 GC/区块/TPS 的心跳。
func attributionHeartbeat(tps, gcCount int64, gcMillis float64, chunks int64) *workerpb.HeartbeatRequest {
	return &workerpb.HeartbeatRequest{
		NodeUuid: "node-1",
		InstanceMetrics: []*workerpb.InstanceMetricSample{{
			InstanceUuid:   "inst-1",
			ProbeAvailable: true,
			Tps:            float64(tps),
			GcCountTotal:   gcCount,
			GcTimeMillis:   gcMillis,
			Worlds: []*workerpb.WorldMetric{
				{Name: "world", LoadedChunks: chunks},
				{Name: "world_nether", LoadedChunks: chunks / 2},
			},
		}},
	}
}

// TestMetric_AnalyzeAttribution_GCSeries 端到端：GC 速率入序后归因指向 GC 因子。
func TestMetric_AnalyzeAttribution_GCSeries(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	var gcMillis float64
	for i := 0; i < 40; i++ {
		ts := base.Add(time.Duration(i) * 5 * time.Minute)
		tps := int64(20)
		delta := 0.0
		if i >= 30 {
			tps = 15
			delta = 3000 // 每 5 分钟 3000ms GC 暂停
		}
		gcMillis += delta
		require.NoError(t, svc.ingestHeartbeatAt(attributionHeartbeat(tps, int64(i), gcMillis, 1000), ts))
	}

	to := base.Add(40 * 5 * time.Minute)
	res, err := svc.AnalyzeAttribution(AttributionQuery{
		InstanceID: "inst-1", From: base, To: to,
	})
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status)
	require.Equal(t, model.MetricInstTPS, res.Target)
	require.NotEmpty(t, res.Factors)
	require.Equal(t, model.MetricInstGCTime, res.Factors[0].MetricKey)
}

// TestMetric_AnalyzeAttribution_WorldSum 世界级因子跨 world 求和后归因指向区块（验收 5）。
func TestMetric_AnalyzeAttribution_WorldSum(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	for i := 0; i < 40; i++ {
		ts := base.Add(time.Duration(i) * 5 * time.Minute)
		tps := int64(20)
		chunks := int64(1000)
		if i >= 30 {
			tps = 15
			chunks = 5000
		}
		require.NoError(t, svc.ingestHeartbeatAt(attributionHeartbeat(tps, 1, 0, chunks), ts))
	}
	to := base.Add(40 * 5 * time.Minute)
	res, err := svc.AnalyzeAttribution(AttributionQuery{InstanceID: "inst-1", From: base, To: to})
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status)
	require.Equal(t, model.MetricWorldLoadedChunks, res.Factors[0].MetricKey)
}

// TestAnalyzeAttribution_MultiSeriesPicksOne （m2）同一实例同指标存在多条实例级序列时
// （实例换节点会新起序列），取「有数据的那个 bucket」而不是被后读到的空序列静默覆盖；
// world 级序列仍跨 world 求和（分区天然可加）。
func TestAnalyzeAttribution_MultiSeriesPicksOne(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	from, to := base, base.Add(time.Hour)
	require.NoError(t, svc.db.AutoMigrate(&model.Instance{}))
	require.NoError(t, svc.db.Create(&model.Instance{
		UUID: "uuid-multi", NodeID: 1, Name: "multi", Type: "minecraft_java",
		ProcessType: "direct", StartCommand: "java -jar s.jar", Status: model.InstanceStatusRunning,
	}).Error)

	// 旧节点序列：TPS 有值；新节点序列：TPS 为 NULL 断点（换节点后的空档）。
	samples := make([]Sample, 0, 120)
	for i := 0; i < 60; i++ {
		ts := base.Add(time.Duration(i) * 30 * time.Second)
		samples = append(samples,
			Sample{NodeUUID: "node-old", InstanceID: "uuid-multi", Scope: model.MetricScopeInstance,
				MetricKey: model.MetricInstTPS, Unit: "tps", TS: ts, Value: fp(20)},
			Sample{NodeUUID: "node-new", InstanceID: "uuid-multi", Scope: model.MetricScopeInstance,
				MetricKey: model.MetricInstTPS, Unit: "tps", TS: ts, Value: nil},
			Sample{NodeUUID: "node-old", InstanceID: "uuid-multi", Scope: model.MetricScopeInstance,
				MetricKey: model.MetricInstGCCount, Unit: "count_per_sec", TS: ts, Value: fp(1)},
		)
		// 两个世界的区块数：应跨 world 求和（分区可加），而非被覆盖成单世界值。
		samples = append(samples,
			Sample{NodeUUID: "node-old", InstanceID: "uuid-multi", Scope: model.MetricScopeWorld,
				World: "world", MetricKey: model.MetricWorldLoadedChunks, Unit: "count", TS: ts, Value: fp(100)},
			Sample{NodeUUID: "node-old", InstanceID: "uuid-multi", Scope: model.MetricScopeWorld,
				World: "world_nether", MetricKey: model.MetricWorldLoadedChunks, Unit: "count", TS: ts, Value: fp(40)},
		)
	}
	require.NoError(t, svc.Ingest(samples))

	_, series, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeInstance, InstanceID: "uuid-multi",
		MetricKeys: []string{model.MetricInstTPS}, From: from, To: to, Resolution: "raw",
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(series), 2, "场景前提：同指标存在多条实例级序列")

	// TPS 目标序列必须有 60 个非空拍（不被空序列覆盖）。
	var nonNull int
	for _, sr := range series {
		for _, p := range sr.Points {
			if p.Avg != nil {
				nonNull++
			}
		}
	}
	require.GreaterOrEqual(t, nonNull, 60, "有数据的序列不应被空序列覆盖")
}
