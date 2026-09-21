package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// fakeMetricSource 是一个内存时序数据源，实现 alertMetricSource，供评估器单测注入（零外部依赖）。
type fakeMetricSource struct {
	series map[string][]Series
	latest map[string]*float64
}

func newFakeMetricSource() *fakeMetricSource {
	return &fakeMetricSource{series: map[string][]Series{}, latest: map[string]*float64{}}
}

func fakeSeriesKey(scope model.MetricScope, nodeUUID, instanceID, metricKey string) string {
	return string(scope) + "|" + nodeUUID + "|" + instanceID + "|" + metricKey
}

func (f *fakeMetricSource) setSeries(scope model.MetricScope, nodeUUID, instanceID, metricKey string, points []SeriesPoint) {
	f.series[fakeSeriesKey(scope, nodeUUID, instanceID, metricKey)] = []Series{{MetricKey: metricKey, Points: points}}
}

func (f *fakeMetricSource) setLatest(scope model.MetricScope, nodeUUID, instanceID, metricKey string, value float64) {
	f.latest[fakeSeriesKey(scope, nodeUUID, instanceID, metricKey)] = &value
}

func (f *fakeMetricSource) QuerySeries(q SeriesQuery) (string, []Series, error) {
	out := []Series{}
	for _, k := range q.MetricKeys {
		if s, ok := f.series[fakeSeriesKey(q.Scope, q.NodeUUID, q.InstanceID, k)]; ok {
			out = append(out, s...)
		}
	}
	return "raw", out, nil
}

func (f *fakeMetricSource) LatestValue(scope model.MetricScope, nodeUUID, instanceID, metricKey string, _ time.Time) (*float64, error) {
	return f.latest[fakeSeriesKey(scope, nodeUUID, instanceID, metricKey)], nil
}

// rampSeries 生成等间隔 30s 的线性序列（缓慢劣化）。
func rampSeries(n int, start, slope float64) []SeriesPoint {
	t0 := metricBase()
	pts := make([]SeriesPoint, n)
	for i := 0; i < n; i++ {
		v := start + float64(i)*slope
		pts[i] = SeriesPoint{TS: t0.Add(time.Duration(i) * 30 * time.Second), Avg: &v, Min: &v, Max: &v}
	}
	return pts
}

// stepDownSeries 生成台阶下坠序列（突降）。
func stepDownSeries(n, step int, high, low float64) []SeriesPoint {
	t0 := metricBase()
	pts := make([]SeriesPoint, n)
	for i := 0; i < n; i++ {
		v := high
		if i >= step {
			v = low
		}
		pts[i] = SeriesPoint{TS: t0.Add(time.Duration(i) * 30 * time.Second), Avg: &v, Min: &v, Max: &v}
	}
	return pts
}

func TestEvaluator_BaselineNodeRuleFiresOnSlowDegradation(t *testing.T) {
	now := time.Now().UTC()
	db := newAlertTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	fresh := now.Add(-10 * time.Second)
	node := &model.Node{Name: "n1", UUID: "node-base", Status: model.NodeStatusOnline, LastHeartbeat: &fresh}
	require.NoError(t, db.Create(node).Error)

	fake := newFakeMetricSource()
	fake.setSeries(model.MetricScopeNode, "node-base", "", model.MetricNodeMemUsed, rampSeries(120, 1000, 5))

	eval := NewAlertEvaluator(db, NewAlertDispatcher(db))
	eval.SetMetrics(fake)

	rule := &model.AlertRule{
		Name: "mem-baseline", UUID: "r-base", Enabled: true,
		TriggerType: model.AlertTriggerBaseline, Level: model.AlertLevelWarn,
		TargetType: "node", Scope: "node", Metric: model.MetricNodeMemUsed,
		BaselineMethod: model.BaselineMethodEWMA, Sensitivity: 3,
	}
	require.NoError(t, db.Create(rule).Error)

	eval.evaluate()

	var events []model.AlertEvent
	require.NoError(t, db.Where("trigger_type = ?", model.AlertTriggerBaseline).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, node.ID, events[0].TargetID)
	assert.False(t, events[0].Resolved)
}

func TestEvaluator_BaselineROCDetectsStepDown(t *testing.T) {
	db := newAlertTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	inst := &model.Instance{UUID: "inst-1", Name: "lobby", Status: model.InstanceStatusRunning, NodeID: 1}
	require.NoError(t, db.Create(inst).Error)

	fake := newFakeMetricSource()
	fake.setSeries(model.MetricScopeInstance, "", "inst-1", model.MetricInstTPS, stepDownSeries(120, 60, 500, 200))

	eval := NewAlertEvaluator(db, NewAlertDispatcher(db))
	eval.SetMetrics(fake)

	rule := &model.AlertRule{
		Name: "tps-drop", UUID: "r-roc", Enabled: true,
		TriggerType: model.AlertTriggerBaseline, Level: model.AlertLevelCritical,
		TargetType: "instance", Scope: "instance", Metric: model.MetricInstTPS,
		BaselineMethod: model.BaselineMethodROC, Direction: model.BaselineDirectionDown, Sensitivity: 3,
	}
	require.NoError(t, db.Create(rule).Error)

	eval.evaluate()

	var events []model.AlertEvent
	require.NoError(t, db.Where("trigger_type = ?", model.AlertTriggerBaseline).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, inst.ID, events[0].TargetID)
	assert.Contains(t, events[0].Message, "下")
}

func TestEvaluator_SaturationRuleFires(t *testing.T) {
	db := newAlertTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	node := &model.Node{Name: "n1", UUID: "node-sat", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)

	fake := newFakeMetricSource()
	fake.setLatest(model.MetricScopeNode, "node-sat", "", model.MetricNodeDiskUsed, 95e9)
	fake.setLatest(model.MetricScopeNode, "node-sat", "", model.MetricNodeDiskTotal, 100e9)

	eval := NewAlertEvaluator(db, NewAlertDispatcher(db))
	eval.SetMetrics(fake)

	rule := &model.AlertRule{
		Name: "disk-sat", UUID: "r-sat", Enabled: true,
		TriggerType: model.AlertTriggerSaturation, Level: model.AlertLevelWarn,
		TargetType: "node", Scope: "node", Metric: "disk", Threshold: 90,
	}
	require.NoError(t, db.Create(rule).Error)

	eval.evaluate()

	var events []model.AlertEvent
	require.NoError(t, db.Where("trigger_type = ?", model.AlertTriggerSaturation).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, node.ID, events[0].TargetID)
	assert.InDelta(t, 95, events[0].Value, 0.01)
}

func TestEvaluator_InstanceMetricRuleEvaluated(t *testing.T) {
	db := newAlertTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	inst := &model.Instance{UUID: "inst-m", Name: "arena", Status: model.InstanceStatusRunning, NodeID: 1}
	require.NoError(t, db.Create(inst).Error)

	fake := newFakeMetricSource()
	fake.setLatest(model.MetricScopeInstance, "", "inst-m", model.MetricInstTPS, 19)

	eval := NewAlertEvaluator(db, NewAlertDispatcher(db))
	eval.SetMetrics(fake)

	rule := &model.AlertRule{
		Name: "tps-threshold", UUID: "r-metric", Enabled: true,
		TriggerType: model.AlertTriggerMetric, Level: model.AlertLevelWarn,
		TargetType: "instance", Metric: "tps", Operator: ">=", Threshold: 18,
	}
	require.NoError(t, db.Create(rule).Error)

	eval.evaluate()

	var events []model.AlertEvent
	require.NoError(t, db.Where("trigger_type = ?", model.AlertTriggerMetric).Find(&events).Error)
	require.Len(t, events, 1, "实例级 metric 规则应被评估（缺口消除）")
	assert.Equal(t, inst.ID, events[0].TargetID)
	assert.InDelta(t, 19, events[0].Value, 0.001)
}

func TestEvaluator_NoiseDoesNotFire(t *testing.T) {
	db := newAlertTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	node := &model.Node{Name: "n1", UUID: "node-noise", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)

	// 均值附近小幅波动：不应触发。
	noise := make([]SeriesPoint, 120)
	base := metricBase()
	for i := range noise {
		v := 1000.0
		if i%2 == 0 {
			v = 1000.02
		}
		noise[i] = SeriesPoint{TS: base.Add(time.Duration(i) * 30 * time.Second), Avg: &v, Min: &v, Max: &v}
	}
	fake := newFakeMetricSource()
	fake.setSeries(model.MetricScopeNode, "node-noise", "", model.MetricNodeMemUsed, noise)

	eval := NewAlertEvaluator(db, NewAlertDispatcher(db))
	eval.SetMetrics(fake)

	rule := &model.AlertRule{
		Name: "mem-baseline", UUID: "r-noise", Enabled: true,
		TriggerType: model.AlertTriggerBaseline, TargetType: "node", Scope: "node",
		Metric: model.MetricNodeMemUsed, BaselineMethod: model.BaselineMethodEWMA, Sensitivity: 3,
	}
	require.NoError(t, db.Create(rule).Error)

	eval.evaluate()

	var count int64
	require.NoError(t, db.Model(&model.AlertEvent{}).Where("trigger_type = ?", model.AlertTriggerBaseline).Count(&count).Error)
	assert.Zero(t, count, "正常波动不应触发")
}

func TestEvaluator_DedupWindowAggregatesWithoutDuplicateNotification(t *testing.T) {
	now := time.Now().UTC()
	db := newAlertTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	fresh := now.Add(-5 * time.Second)
	node := &model.Node{Name: "n1", UUID: "node-dedup", Status: model.NodeStatusOnline, LastHeartbeat: &fresh}
	require.NoError(t, db.Create(node).Error)

	fake := newFakeMetricSource()
	fake.setSeries(model.MetricScopeNode, "node-dedup", "", model.MetricNodeMemUsed, rampSeries(120, 1000, 5))

	dispatcher := NewAlertDispatcher(db)
	eval := NewAlertEvaluator(db, dispatcher)
	eval.SetMetrics(fake)

	rule := &model.AlertRule{
		Name: "mem-baseline", UUID: "r-dedup", Enabled: true,
		TriggerType: model.AlertTriggerBaseline, TargetType: "node", Scope: "node",
		Metric: model.MetricNodeMemUsed, BaselineMethod: model.BaselineMethodEWMA, Sensitivity: 3,
		DedupWindowSec: 600,
	}
	require.NoError(t, db.Create(rule).Error)

	eval.evaluate()
	eval.evaluate() // 去抖窗口内二次触发只累计，不重复建事件

	var events []model.AlertEvent
	require.NoError(t, db.Where("trigger_type = ?", model.AlertTriggerBaseline).Find(&events).Error)
	require.Len(t, events, 1, "去抖窗口内应聚合为同一事件")
	assert.Equal(t, 2, events[0].Count, "复发应累计计数")
}
