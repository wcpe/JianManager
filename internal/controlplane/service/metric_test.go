package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

func newMetricSvc(t *testing.T) *MetricService {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.MetricSeries{}, &model.MetricSampleRaw{},
		&model.MetricRollup5m{}, &model.MetricRollup1h{},
	))
	return NewMetricService(db)
}

func fp(v float64) *float64 { return &v }

// 整点对齐的基准时刻，便于 5m/1h 桶边界断言。
func metricBase() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) }

func cpuSample(node string, ts time.Time, v *float64) Sample {
	return Sample{
		NodeUUID: node, Scope: model.MetricScopeNode,
		MetricKey: model.MetricNodeCPUPct, Unit: "pct", TS: ts, Value: v,
	}
}

func TestMetric_SelectResolution(t *testing.T) {
	cases := []struct {
		span time.Duration
		req  string
		want string
	}{
		{3 * time.Hour, "auto", "raw"},
		{6 * time.Hour, "", "raw"},
		{24 * time.Hour, "auto", "5m"},
		{30 * 24 * time.Hour, "auto", "5m"},
		{90 * 24 * time.Hour, "auto", "1h"},
		{3 * time.Hour, "5m", "5m"},         // 显式覆盖
		{90 * 24 * time.Hour, "raw", "raw"}, // 显式覆盖
	}
	for _, c := range cases {
		require.Equal(t, c.want, selectResolution(c.span, c.req), "span=%v req=%q", c.span, c.req)
	}
}

func TestMetric_IngestQueryRaw(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	node := "node-1"
	require.NoError(t, svc.Ingest([]Sample{
		cpuSample(node, base, fp(10)),
		cpuSample(node, base.Add(30*time.Second), nil), // 缺测
		cpuSample(node, base.Add(60*time.Second), fp(30)),
	}))

	res, series, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeNode, NodeUUID: node,
		From: base.Add(-time.Minute), To: base.Add(2 * time.Minute), Resolution: "raw",
	})
	require.NoError(t, err)
	require.Equal(t, "raw", res)
	require.Len(t, series, 1)
	require.Equal(t, model.MetricNodeCPUPct, series[0].MetricKey)
	require.Len(t, series[0].Points, 3)
	require.Equal(t, 10.0, *series[0].Points[0].Avg)
	require.Nil(t, series[0].Points[1].Avg) // 缺测渲染为断点
	require.Equal(t, 30.0, *series[0].Points[2].Avg)
}

func TestMetric_IngestUpsertsSeries(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	require.NoError(t, svc.Ingest([]Sample{cpuSample("n", base, fp(1))}))
	require.NoError(t, svc.Ingest([]Sample{cpuSample("n", base.Add(30*time.Second), fp(2))}))

	var n int64
	require.NoError(t, svc.db.Model(&model.MetricSeries{}).Count(&n).Error)
	require.Equal(t, int64(1), n, "同身份样本复用同一序列")

	// 不同 metric_key → 新序列
	require.NoError(t, svc.Ingest([]Sample{{
		NodeUUID: "n", Scope: model.MetricScopeNode,
		MetricKey: model.MetricNodeMemUsed, Unit: "bytes", TS: base, Value: fp(5),
	}}))
	require.NoError(t, svc.db.Model(&model.MetricSeries{}).Count(&n).Error)
	require.Equal(t, int64(2), n)
}

func TestMetric_RollupAndQuery5m(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	node := "n"
	require.NoError(t, svc.Ingest([]Sample{
		cpuSample(node, base, fp(10)),
		cpuSample(node, base.Add(time.Minute), fp(20)),
		cpuSample(node, base.Add(2*time.Minute), fp(30)),
	}))
	// now=base+10m → [base,base+5m) 桶已完结
	require.NoError(t, svc.RollupAndPurge(base.Add(10*time.Minute)))

	res, series, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeNode, NodeUUID: node, MetricKeys: []string{model.MetricNodeCPUPct},
		From: base.Add(-time.Hour), To: base.Add(time.Hour), Resolution: "5m",
	})
	require.NoError(t, err)
	require.Equal(t, "5m", res)
	require.Len(t, series, 1)
	require.Len(t, series[0].Points, 1)
	p := series[0].Points[0]
	require.True(t, p.TS.Equal(base), "桶起点对齐 5m")
	require.Equal(t, 20.0, *p.Avg)
	require.Equal(t, 10.0, *p.Min)
	require.Equal(t, 30.0, *p.Max)
}

func TestMetric_Rollup1hWeightsBy5mCount(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	node := "n"
	// 桶 A [base,+5m)：两点 avg=10,count=2；桶 B [+5m,+10m)：一点 avg=40,count=1
	require.NoError(t, svc.Ingest([]Sample{
		cpuSample(node, base, fp(5)),
		cpuSample(node, base.Add(time.Minute), fp(15)),
		cpuSample(node, base.Add(6*time.Minute), fp(40)),
	}))
	// now 越过整点：[base,base+1h) 1h 桶完结，5m→1h 才会卷积
	require.NoError(t, svc.RollupAndPurge(base.Add(70*time.Minute)))

	_, series, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeNode, NodeUUID: node, MetricKeys: []string{model.MetricNodeCPUPct},
		From: base.Add(-2 * time.Hour), To: base.Add(2 * time.Hour), Resolution: "1h",
	})
	require.NoError(t, err)
	require.Len(t, series, 1)
	require.Len(t, series[0].Points, 1)
	p := series[0].Points[0]
	require.True(t, p.TS.Equal(base), "桶起点对齐 1h")
	// count 加权：(10*2 + 40*1)/3 = 20
	require.InDelta(t, 20.0, *p.Avg, 1e-9)
	require.Equal(t, 5.0, *p.Min)
	require.Equal(t, 40.0, *p.Max)
}

func TestMetric_PurgeTTL(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	node := "n"
	old := base.Add(-50 * time.Hour) // 超 48h
	require.NoError(t, svc.Ingest([]Sample{
		cpuSample(node, old, fp(1)),
		cpuSample(node, base, fp(2)),
	}))
	require.NoError(t, svc.RollupAndPurge(base.Add(time.Minute)))

	var rawCount int64
	require.NoError(t, svc.db.Model(&model.MetricSampleRaw{}).Count(&rawCount).Error)
	require.Equal(t, int64(1), rawCount, "超 48h 原始样本被清理，近端保留")
}

func TestMetric_QueryAutoResolutionPicksTier(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	node := "n"
	require.NoError(t, svc.Ingest([]Sample{cpuSample(node, base, fp(10))}))
	require.NoError(t, svc.RollupAndPurge(base.Add(10*time.Minute)))

	// 7d 区间 → auto 选 5m
	res, _, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeNode, NodeUUID: node,
		From: base.Add(-7 * 24 * time.Hour), To: base, Resolution: "auto",
	})
	require.NoError(t, err)
	require.Equal(t, "5m", res)
}
