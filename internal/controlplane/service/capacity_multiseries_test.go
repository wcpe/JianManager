package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// seedHeapSeries 经真实写入路径（Ingest）向某节点身份写一条 inst_heap_used 序列。
// 用不同 NodeUUID + 同一 InstanceID 模拟「实例换节点后 node_uuid 变化产生新序列」：
// MetricSeries 的身份是 (node_uuid, instance_id, scope, metric_key, world)，
// node_uuid 一变就是**另一条**序列，而 capacity 按 instance_id 查回全部序列。
func seedHeapSeries(t *testing.T, svc *MetricService, nodeUUID, instUUID string, from time.Time, n int, step time.Duration, val float64) {
	t.Helper()
	samples := make([]Sample, 0, n)
	for i := 0; i < n; i++ {
		samples = append(samples, Sample{
			NodeUUID: nodeUUID, InstanceID: instUUID,
			Scope: model.MetricScopeInstance, MetricKey: model.MetricInstHeapUsed,
			Unit: "bytes", TS: from.Add(time.Duration(i) * step), Value: fp(val),
		})
	}
	require.NoError(t, svc.Ingest(samples))
}

// forecastMultiSeries 造「同实例两条序列」场景并跑容量预测，返回第一条结果。
//
// 构造（heap 上限固定为 inst_heap_max = 2000MiB 序列，使 limitValue 确定）：
//   - 序列 A（node-fresh）：贯穿整个窗口，heap 恒为 900MiB → 这才是当前真实占用；
//   - 序列 B（node-stale）：只在窗口**前半段**有数据，heap 恒为 100MiB → 早已停更的旧身份。
//
// 期望：NowValue 取「窗口内最新时点」的值 = 900MiB（与序列枚举顺序无关）；
// Samples 不因序列条数把两条序列的点数相加。
func forecastMultiSeries(t *testing.T, staleFirst bool) ForecastResult {
	t.Helper()
	svc := newMetricSvc(t)
	base := metricBase()
	const step = 30 * time.Second
	const n = 240 // 2h 窗口，每 30s 一拍
	from := base
	to := base.Add(time.Duration(n-1) * step)

	stale := func() { seedHeapSeries(t, svc, "node-stale", "u1", base, 120, step, 100*1024*1024) }
	fresh := func() { seedHeapSeries(t, svc, "node-fresh", "u1", base, n, step, 900*1024*1024) }

	// 上限序列：与两序列同身份无关，只要存在即可（limit 取窗口内最后一个非空点）。
	require.NoError(t, svc.Ingest([]Sample{{
		NodeUUID: "node-fresh", InstanceID: "u1",
		Scope: model.MetricScopeInstance, MetricKey: model.MetricInstHeapMax,
		Unit: "bytes", TS: base, Value: fp(2000 * 1024 * 1024),
	}}))

	if staleFirst {
		stale()
		fresh()
	} else {
		fresh()
		stale()
	}

	res, err := svc.ForecastCapacity(ForecastQuery{
		Scope: model.MetricScopeInstance, InstanceID: "u1",
		Metrics: []string{model.MetricInstHeapUsed}, From: from, To: to,
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	return res[0]
}

// TestMetric_ForecastCapacity_MultiSeriesNowValueIsLatest （B-2 复现）
//
// 缺陷：forecastPoints 把同指标**多条序列**的点无条件混进同一条趋势线，且 lastVal（对外
// nowValue）随迭代被覆盖成「枚举顺序上最后一条序列的末点」——序列枚举顺序由
// QuerySeries 的 Order("metric_key, world") + SQLite 行序决定，**未定义**。
// 后果：实例换节点（node_uuid 变化 → 新序列）后 nowValue / slope / samples 全被污染，
// remaining = limit - nowValue 随之失真，可能触发错误的趋势告警。
//
// 期望语义（与 attribution.go 的 m2 修复同口径）：实例级同指标多序列**按身份择一**，
// nowValue 取窗口内**最新时点**的值，与序列创建/枚举顺序无关。
func TestMetric_ForecastCapacity_MultiSeriesNowValueIsLatest(t *testing.T) {
	got := forecastMultiSeries(t, false)
	require.InDelta(t, 900*1024*1024, got.NowValue, 1,
		"nowValue 取窗口内最新时点的值（900MiB 的活跃序列），而非枚举末条序列的陈旧末值")
}

// TestMetric_ForecastCapacity_MultiSeriesOrderIndependent 反转序列创建顺序后结果必须不变
// （把「与枚举顺序无关」这一不变量锁进断言）。修复前该用例取到陈旧序列的 100MiB。
func TestMetric_ForecastCapacity_MultiSeriesOrderIndependent(t *testing.T) {
	forward := forecastMultiSeries(t, false)
	reversed := forecastMultiSeries(t, true)

	require.InDelta(t, forward.NowValue, reversed.NowValue, 1e-9,
		"nowValue 必须与序列创建/枚举顺序无关")
	require.InDelta(t, 900*1024*1024, reversed.NowValue, 1,
		"反转创建顺序后仍应取窗口内最新时点的值")
	require.Equal(t, forward.Samples, reversed.Samples, "samples 必须与序列顺序无关")
}

// TestMetric_ForecastCapacity_MultiSeriesSamplesNotInflated 样本数不得把多序列点数相加。
// 单序列 240 点；修复前两条序列相加 = 360（审查员实测 360）。
func TestMetric_ForecastCapacity_MultiSeriesSamplesNotInflated(t *testing.T) {
	got := forecastMultiSeries(t, false)
	require.Equal(t, 240, got.Samples,
		"样本数 = 择一后趋势线的点数（240），不是两条序列点数之和（360）")
	require.InDelta(t, 2000*1024*1024, got.LimitValue, 1, "上限仍取 inst_heap_max 序列（2000MiB）")
}

// TestMetric_ForecastCapacity_MultiSeriesLimitUsesLatest 迁移后旧/新上限序列共存时取最新一条。
//
// 缺陷背景：用量（inst_heap_used）已按「窗口内最新时点」收敛，上限（inst_heap_max）却直接取
// 枚举第一条正值——实例迁到新节点、Xmx 改大后，仍会用旧节点的旧上限去算剩余空间，
// 结果可能直接报「已超上限」。
func TestMetric_ForecastCapacity_MultiSeriesLimitUsesLatest(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	const step = 30 * time.Second
	const n = 240
	from := base
	to := base.Add(time.Duration(n-1) * step)

	// 旧节点的上限序列先写入（1 GiB），新节点后写入（4 GiB），两者都在窗口内。
	require.NoError(t, svc.Ingest([]Sample{
		{NodeUUID: "node-old", InstanceID: "u-limit", Scope: model.MetricScopeInstance,
			MetricKey: model.MetricInstHeapMax, Unit: "bytes", TS: base, Value: fp(1024 * 1024 * 1024)},
		{NodeUUID: "node-new", InstanceID: "u-limit", Scope: model.MetricScopeInstance,
			MetricKey: model.MetricInstHeapMax, Unit: "bytes", TS: base.Add(time.Minute), Value: fp(4 * 1024 * 1024 * 1024)},
	}))
	seedHeapSeries(t, svc, "node-new", "u-limit", base, n, step, 1536*1024*1024)

	res, err := svc.ForecastCapacity(ForecastQuery{
		Scope: model.MetricScopeInstance, InstanceID: "u-limit",
		Metrics: []string{model.MetricInstHeapUsed}, From: from, To: to,
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.InDelta(t, 4*1024*1024*1024, res[0].LimitValue, 1,
		"上限必须取最新身份序列（4GiB），而不是枚举第一条旧值（1GiB）")
}

// TestMetric_ForecastCapacity_StaleSampleNotProjected 最新样本过旧时不给预测，避免日期随查询时刻漂移。
func TestMetric_ForecastCapacity_StaleSampleNotProjected(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	const step = 30 * time.Second
	const n = 240
	from := base
	// 样本只覆盖窗口前段；查询末点比最后一个样本晚 1 小时（超过 capacityStaleTolerance）。
	to := base.Add(time.Duration(n-1)*step + time.Hour)
	seedHeapSeries(t, svc, "node-stale", "u-stale", base, n, step, 900*1024*1024)
	require.NoError(t, svc.Ingest([]Sample{{
		NodeUUID: "node-stale", InstanceID: "u-stale", Scope: model.MetricScopeInstance,
		MetricKey: model.MetricInstHeapMax, Unit: "bytes", TS: base, Value: fp(2000 * 1024 * 1024),
	}}))

	res, err := svc.ForecastCapacity(ForecastQuery{
		Scope: model.MetricScopeInstance, InstanceID: "u-stale",
		Metrics: []string{model.MetricInstHeapUsed}, From: from, To: to,
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, ForecastConfidenceInsufficient, res[0].Confidence)
	require.Nil(t, res[0].ExhaustAt, "陈旧样本不得外推耗尽时间")
	require.Contains(t, res[0].Note, "过期")
	require.Zero(t, res[0].NowValue, "陈旧值不得当作当前值展示")
}

// TestMetric_ForecastCapacity_SingleSeriesUnchanged 反向确认：单序列数据的数值与修复前一致。
func TestMetric_ForecastCapacity_SingleSeriesUnchanged(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	const step = 30 * time.Second
	const n = 240
	from := base
	to := base.Add(time.Duration(n-1) * step)
	seedHeapSeries(t, svc, "node-only", "u2", base, n, step, 900*1024*1024)
	require.NoError(t, svc.Ingest([]Sample{{
		NodeUUID: "node-only", InstanceID: "u2",
		Scope: model.MetricScopeInstance, MetricKey: model.MetricInstHeapMax,
		Unit: "bytes", TS: base, Value: fp(2000 * 1024 * 1024),
	}}))

	res, err := svc.ForecastCapacity(ForecastQuery{
		Scope: model.MetricScopeInstance, InstanceID: "u2",
		Metrics: []string{model.MetricInstHeapUsed}, From: from, To: to,
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.InDelta(t, 900*1024*1024, res[0].NowValue, 1)
	require.Equal(t, n, res[0].Samples, "单序列样本数不变")
	require.InDelta(t, 2000*1024*1024, res[0].LimitValue, 1)
}

func TestMetric_ForecastCapacity_PreservesSampleTimeOrigin(t *testing.T) {
	svc := newMetricSvc(t)
	from := metricBase()
	lastTS := from.Add(119 * 30 * time.Second)
	to := lastTS.Add(2 * time.Minute)
	for i := 0; i < 120; i++ {
		require.NoError(t, svc.Ingest([]Sample{{
			NodeUUID: "time-node", InstanceID: "time-inst", Scope: model.MetricScopeInstance,
			MetricKey: model.MetricInstHeapUsed, Unit: "bytes", TS: from.Add(time.Duration(i) * 30 * time.Second),
			Value: fp(float64(512+i) * 1024 * 1024),
		}}))
	}
	require.NoError(t, svc.Ingest([]Sample{{
		NodeUUID: "time-node", InstanceID: "time-inst", Scope: model.MetricScopeInstance,
		MetricKey: model.MetricInstHeapMax, Unit: "bytes", TS: lastTS, Value: fp(2048 * 1024 * 1024),
	}}))
	result, err := svc.ForecastCapacity(ForecastQuery{Scope: model.MetricScopeInstance, InstanceID: "time-inst", From: from, To: to})
	require.NoError(t, err)
	require.Len(t, result, 1)
	got := result[0]
	require.NotNil(t, got.ExhaustAt)
	remaining := time.Duration((got.LimitValue - got.NowValue) / got.SlopePerSec * float64(time.Second))
	require.WithinDuration(t, lastTS.Add(remaining), *got.ExhaustAt, time.Second,
		"exhaustion must remain anchored to the actual value timestamp, not move with the query end")
	wantDays := got.ExhaustAt.Sub(to).Hours() / 24
	require.InDelta(t, wantDays, *got.ExhaustLowDays, 1e-8)
}

func TestMetric_ForecastCapacity_RecentHourlyBucketsAreNotStale(t *testing.T) {
	svc := newMetricSvc(t)
	from := metricBase()
	to := from.Add(60*24*time.Hour + 40*time.Minute) // auto uses hourly rollups
	used := model.MetricSeries{NodeUUID: "hour-node", InstanceID: "hour-inst", Scope: model.MetricScopeInstance, MetricKey: model.MetricInstHeapUsed, Unit: "bytes"}
	limit := model.MetricSeries{NodeUUID: "hour-node", InstanceID: "hour-inst", Scope: model.MetricScopeInstance, MetricKey: model.MetricInstHeapMax, Unit: "bytes"}
	require.NoError(t, svc.db.Create(&used).Error)
	require.NoError(t, svc.db.Create(&limit).Error)
	var rows []model.MetricRollup1h
	for i := 0; i < 120; i++ {
		ts := from.Add(time.Duration(60*24-120+i) * time.Hour)
		v := float64(512+i) * 1024 * 1024
		rows = append(rows, model.MetricRollup1h{SeriesID: used.ID, BucketTS: ts, Avg: v, Min: v, Max: v, Last: v, Count: 120})
		v = 2048 * 1024 * 1024
		rows = append(rows, model.MetricRollup1h{SeriesID: limit.ID, BucketTS: ts, Avg: v, Min: v, Max: v, Last: v, Count: 120})
	}
	require.NoError(t, svc.db.CreateInBatches(rows, 100).Error)
	result, err := svc.ForecastCapacity(ForecastQuery{Scope: model.MetricScopeInstance, InstanceID: "hour-inst", From: from, To: to})
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, ForecastConfidenceHigh, result[0].Confidence,
		"the latest complete hourly bucket is normally older than 15 minutes; it is not a stale data source")
	require.NotNil(t, result[0].ExhaustAt)
}
