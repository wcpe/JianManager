package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// linearPoints 生成一条严格线性（可选加固定噪声）的点集。
func linearPoints(n int, dt, base, slope, noise float64) []TrendPoint {
	pts := make([]TrendPoint, n)
	for i := range pts {
		t := float64(i) * dt
		v := base + slope*t
		if noise != 0 && i%2 == 1 {
			v += noise
		}
		pts[i] = TrendPoint{T: t, V: v}
	}
	return pts
}

// TestForecastLinear_ExactSlope 纯线性序列的 Theil–Sen 斜率与残差精确可断言（FR-464）。
func TestForecastLinear_ExactSlope(t *testing.T) {
	pts := linearPoints(50, 60, 1000, 0.5, 0)
	lf := ForecastLinear(pts)
	require.InDelta(t, 0.5, lf.SlopePerSec, 1e-9)
	require.InDelta(t, 1000, lf.Intercept, 1e-6)
	require.InDelta(t, 0, lf.ResidStd, 1e-9)
	require.InDelta(t, 0, lf.SlopeStdErr, 1e-9)
	require.Equal(t, 50, lf.Samples)
}

// TestForecastLinear_OutlierRobust 单个离群点不改变 Theil–Sen 斜率（稳健性）。
func TestForecastLinear_OutlierRobust(t *testing.T) {
	pts := linearPoints(51, 60, 1000, 0.5, 0)
	pts[25].V += 100000 // 注入离群
	lf := ForecastLinear(pts)
	require.InDelta(t, 0.5, lf.SlopePerSec, 1e-9, "中位斜率对单点离群不敏感")
}

// TestForecastLinear_Degenerate 点数不足/零跨度返回零值，不 panic。
func TestForecastLinear_Degenerate(t *testing.T) {
	require.Equal(t, 0.0, ForecastLinear(nil).SlopePerSec)
	require.Equal(t, 1, ForecastLinear([]TrendPoint{{T: 0, V: 5}}).Samples)
	pairs := []TrendPoint{{T: 3, V: 1}, {T: 3, V: 9}}
	require.Equal(t, 0.0, ForecastLinear(pairs).SlopePerSec, "同刻点无斜率")
}

// forecastSeed 造一台节点 + 一条磁盘增长序列 + 节点容量快照。
func forecastSeed(t *testing.T, svc *MetricService) (nodeUUID string, from, to time.Time) {
	t.Helper()
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}))
	nodeUUID = "node-disk"
	node := model.Node{
		UUID: nodeUUID, Name: "n1", Status: model.NodeStatusOnline,
		DiskTotalMB: 100 * 1024, // 100GB
		MemoryMB:    16 * 1024,
	}
	require.NoError(t, svc.db.Create(&node).Error)

	from = metricBase()
	to = from.Add(6 * time.Hour)
	// 每 30s +40MiB → 约 1.33 MiB/s；从 50GB 起、上限 100GB → 剩 50GB ≈ 38400s ≈ 0.44 天。
	const perStepMiB = 40
	steps := int((6 * time.Hour) / (30 * time.Second)) // 720
	samples := make([]Sample, 0, steps+1)
	for i := 0; i <= steps; i++ {
		ts := from.Add(time.Duration(i) * 30 * time.Second)
		usedMB := 50*1024 + float64(i)*perStepMiB
		if i%2 == 1 {
			usedMB += 4 // 轻微抖动：使残差 σ_res > 0，置信区间非退化
		}
		samples = append(samples, Sample{
			NodeUUID: nodeUUID, Scope: model.MetricScopeNode,
			MetricKey: model.MetricNodeDiskUsed, Unit: "bytes",
			TS: ts, Value: fp(usedMB * 1024 * 1024),
		})
	}
	require.NoError(t, svc.Ingest(samples))
	return nodeUUID, from, to
}

// TestMetric_ForecastCapacity_Growth 有增长时给出耗尽时间与 80% CI（FR-464 验收 4）。
func TestMetric_ForecastCapacity_Growth(t *testing.T) {
	svc := newMetricSvc(t)
	nodeUUID, from, to := forecastSeed(t, svc)

	res, err := svc.ForecastCapacity(ForecastQuery{
		Scope: model.MetricScopeNode, NodeUUID: nodeUUID,
		Metrics: []string{model.MetricNodeDiskUsed}, From: from, To: to,
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	fr := res[0]
	require.NotEqual(t, "insufficient", fr.Confidence)
	require.NotNil(t, fr.ExhaustAt)
	require.NotNil(t, fr.ExhaustLowDays)
	require.NotNil(t, fr.ExhaustHighDays)
	require.Greater(t, fr.SlopePerSec, 0.0)
	require.InDelta(t, float64(100*1024)*1024*1024, fr.LimitValue, 1, "上限取节点容量快照")
	// 窗口末已用 78.1GB（50GB + 720×40MiB），剩 21.9GB / (40MiB per 30s) ≈ 16400s ≈ 0.19 天。
	pointDays := fr.ExhaustAt.Sub(to).Hours() / 24
	require.InDelta(t, 0.19, pointDays, 0.01)
	// 80% CI 有界且下界 ≤ 点估计 ≤ 上界。
	require.Less(t, *fr.ExhaustLowDays, *fr.ExhaustHighDays)
	require.LessOrEqual(t, *fr.ExhaustLowDays, pointDays)
	require.GreaterOrEqual(t, *fr.ExhaustHighDays, pointDays)
}

// TestMetric_ForecastCapacity_Flat 无增长 → 不显著，不伪造预测（验收 5）。
func TestMetric_ForecastCapacity_Flat(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}))
	require.NoError(t, svc.db.Create(&model.Node{
		UUID: "node-flat", Name: "n2", Status: model.NodeStatusOnline, DiskTotalMB: 100 * 1024,
	}).Error)
	from := metricBase()
	to := from.Add(6 * time.Hour)
	steps := int((6 * time.Hour) / (30 * time.Second))
	samples := make([]Sample, 0, steps+1)
	for i := 0; i <= steps; i++ {
		samples = append(samples, Sample{
			NodeUUID: "node-flat", Scope: model.MetricScopeNode,
			MetricKey: model.MetricNodeDiskUsed, Unit: "bytes",
			TS: from.Add(time.Duration(i) * 30 * time.Second), Value: fp(float64(50*1024) * 1024 * 1024),
		})
	}
	require.NoError(t, svc.Ingest(samples))

	res, err := svc.ForecastCapacity(ForecastQuery{
		Scope: model.MetricScopeNode, NodeUUID: "node-flat",
		Metrics: []string{model.MetricNodeDiskUsed}, From: from, To: to,
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, "insufficient", res[0].Confidence)
	require.Nil(t, res[0].ExhaustAt)
	require.Nil(t, res[0].ExhaustLowDays)
	require.Contains(t, res[0].Note, "无增长趋势")
}

// TestMetric_ForecastCapacity_InsufficientSamples 样本不足 → insufficient，不预测（验收 5）。
func TestMetric_ForecastCapacity_InsufficientSamples(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}))
	require.NoError(t, svc.db.Create(&model.Node{
		UUID: "node-few", Name: "n3", Status: model.NodeStatusOnline, DiskTotalMB: 100 * 1024,
	}).Error)
	from := metricBase()
	to := from.Add(10 * time.Minute)
	samples := make([]Sample, 0, 3)
	for i := 0; i < 3; i++ {
		samples = append(samples, Sample{
			NodeUUID: "node-few", Scope: model.MetricScopeNode,
			MetricKey: model.MetricNodeDiskUsed, Unit: "bytes",
			TS: from.Add(time.Duration(i) * 5 * time.Minute), Value: fp(float64(50*1024)*1024*1024 + float64(i)),
		})
	}
	require.NoError(t, svc.Ingest(samples))

	res, err := svc.ForecastCapacity(ForecastQuery{
		Scope: model.MetricScopeNode, NodeUUID: "node-few",
		Metrics: []string{model.MetricNodeDiskUsed}, From: from, To: to,
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, "insufficient", res[0].Confidence)
	require.Nil(t, res[0].ExhaustLowDays)
	require.Contains(t, res[0].Note, "样本不足")
}

// TestCapacityTrendAlerter_FireAndDedup 命中阈值经 metric 规则触发，重复调用去抖（验收 6）。
func TestCapacityTrendAlerter_FireAndDedup(t *testing.T) {
	db := newAlertTestDB(t)
	dispatcher := NewAlertDispatcher(db)
	clock := time.Now()
	dispatcher.now = func() time.Time { return clock }
	alerter := NewCapacityTrendAlerter(db, dispatcher)

	rule := &model.AlertRule{
		Name: "disk 趋势", Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric,
		TargetType: "node", DedupWindowSec: 600, Enabled: true,
	}
	require.NoError(t, db.Create(rule).Error)

	low := 3.0
	results := []ForecastResult{{
		TargetID: "node-x", MetricKey: model.MetricNodeDiskUsed,
		NowValue: 90, LimitValue: 100, Confidence: "high", ExhaustLowDays: &low,
	}}
	require.Equal(t, 1, alerter.Notify(model.MetricScopeNode, 7, "n1", results, 7))

	// 阈值内 60s 轮询复查：由重发节流（capacityReNotifyInterval）抑制，不落新事件也不累计
	// ——趋势告警是瞬时型（Resolvable=false），复发不再聚合进旧事件，故重复抑制靠节流。
	require.Equal(t, 0, alerter.Notify(model.MetricScopeNode, 7, "n1", results, 7))
	var events []model.AlertEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1, "重发间隔内不新建事件")
	require.Equal(t, 1, events[0].Count, "瞬时型不累计复发计数")
	require.True(t, events[0].Resolved)
	require.Equal(t, string(model.MetricScopeNode), rule.TargetType)

	// 显式配置 DedupWindowSec 时以它为准（覆盖默认 6h 节流）：窗口内不发。
	windowsRule := &model.AlertRule{
		Name: "disk 趋势(去抖)", Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric,
		TargetType: "node", DedupWindowSec: 60, Enabled: true,
		TargetID: func() *uint { v := uint(7); return &v }(),
	}
	require.NoError(t, db.Create(windowsRule).Error)
	require.Equal(t, 1, alerter.Notify(model.MetricScopeNode, 7, "n1", results, 7))
	// 去抖窗口（60s）未过 → 抑制；把上次触发时间拨回 61s 前 → 可重发。
	require.Equal(t, 0, alerter.Notify(model.MetricScopeNode, 7, "n1", results, 7))
	require.NoError(t, db.Model(&model.AlertEvent{}).
		Where("rule_id = ?", windowsRule.ID).
		Update("fired_at", time.Now().Add(-61*time.Second)).Error)
	require.Equal(t, 1, alerter.Notify(model.MetricScopeNode, 7, "n1", results, 7))

	// 超阈值（下界 ≥ 7 天）不触发。
	far := 30.0
	require.Equal(t, 0, alerter.Notify(model.MetricScopeNode, 7, "n1", []ForecastResult{{
		TargetID: "node-x", MetricKey: model.MetricNodeDiskUsed, Confidence: "high", ExhaustLowDays: &far,
	}}, 7))

	// insufficient 不触发。
	require.Equal(t, 0, alerter.Notify(model.MetricScopeNode, 7, "n1", []ForecastResult{{
		TargetID: "node-x", MetricKey: model.MetricNodeDiskUsed, Confidence: "insufficient",
	}}, 7))
}

// TestForecastCapacity_SlopeTooSmallNoOverflow （M1）β 极小（1e-3 B/s）且剩余空间大时，
// 耗尽时间可达 3e8 天量级，time.Duration 会溢出成**过去时间**。
// 修复后：超过上界一律 ExhaustAt=nil + Note，且 days 不出现 3e8 量级、更不出现过去时间。
func TestForecastCapacity_SlopeTooSmallNoOverflow(t *testing.T) {
	svc := newMetricSvc(t)
	// nodeUUID 用于重造样本（下面的 NodeUUID 字段），from/to 定位查询窗口。
	nodeUUID, from, to := forecastSeed(t, svc)

	// 用真实序列重造一批「极小斜率」样本：窗口内每 30s 只涨 0.03 B（β = 1e-3 B/s），
	// 剩 10GB → tExhaust ≈ 1e13 秒 ≈ 3.17e8 天（远超 100 年上界）。
	require.NoError(t, svc.db.Where("1 = 1").Delete(&model.MetricSampleRaw{}).Error)
	steps := int((6 * time.Hour) / (30 * time.Second))
	// 已用量从 90GB 起（上限 100GB → 剩 10GB），每 30s 涨 0.03 B 保持「极小斜率」。
	const startUsedMB = 90 * 1024
	samples := make([]Sample, 0, steps+1)
	for i := 0; i <= steps; i++ {
		ts := from.Add(time.Duration(i) * 30 * time.Second)
		usedBytes := float64(startUsedMB)*1024*1024 + float64(i)*0.03
		samples = append(samples, Sample{
			NodeUUID: nodeUUID, Scope: model.MetricScopeNode,
			MetricKey: model.MetricNodeDiskUsed, Unit: "bytes",
			TS: ts, Value: fp(usedBytes),
		})
	}
	require.NoError(t, svc.Ingest(samples))

	res, err := svc.ForecastCapacity(ForecastQuery{
		Scope: model.MetricScopeNode, NodeUUID: nodeUUID,
		Metrics: []string{model.MetricNodeDiskUsed}, From: from, To: to,
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	fr := res[0]
	require.Nil(t, fr.ExhaustAt, "超出可预测上界不得给出耗尽时间（否则 Duration 溢出成过去时间）")
	require.Nil(t, fr.ExhaustLowDays)
	require.Nil(t, fr.ExhaustHighDays)
	require.Contains(t, fr.Note, "超出可预测范围")
	require.Less(t, fr.SlopePerSec, 1.0, "场景前提：斜率确实极小")
}

// TestForecastCapacity_NormalSlopeStillPredicts （M1 反例）常规斜率不受上界钳制影响。
func TestForecastCapacity_NormalSlopeStillPredicts(t *testing.T) {
	svc := newMetricSvc(t)
	nodeUUID, from, to := forecastSeed(t, svc)
	res, err := svc.ForecastCapacity(ForecastQuery{
		Scope: model.MetricScopeNode, NodeUUID: nodeUUID,
		Metrics: []string{model.MetricNodeDiskUsed}, From: from, To: to,
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.NotNil(t, res[0].ExhaustAt, "常规增长仍应给出耗尽时间")
	require.True(t, res[0].ExhaustAt.After(to), "耗尽时间必须在窗口之后，不能是过去时间")
	require.NotNil(t, res[0].ExhaustHighDays)
	require.LessOrEqual(t, *res[0].ExhaustHighDays, capacityMaxHorizonDays, "置信上界同受上界钳制")
}

// TestCapacityTrendAlerter_RefiresAfterRecovery （M2）首次触发 → 条件恢复 → 再次恶化，
// 第二次必须仍能产生事件/通知；不得因「存在未恢复事件」而永久只累计。
func TestCapacityTrendAlerter_RefiresAfterRecovery(t *testing.T) {
	db := newAlertTestDB(t)
	dispatcher := NewAlertDispatcher(db)
	clock := time.Now()
	dispatcher.now = func() time.Time { return clock }
	alerter := NewCapacityTrendAlerter(db, dispatcher)

	rule := &model.AlertRule{
		Name: "disk 趋势", Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric,
		TargetType: "node", Enabled: true,
	}
	require.NoError(t, db.Create(rule).Error)

	low := 3.0
	danger := []ForecastResult{{
		TargetID: "node-x", MetricKey: model.MetricNodeDiskUsed,
		NowValue: 90, LimitValue: 100, Confidence: "high", ExhaustLowDays: &low,
	}}
	// ① 首次触发。
	require.Equal(t, 1, alerter.Notify(model.MetricScopeNode, 7, "n1", danger, 7))
	var events []model.AlertEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1)
	require.True(t, events[0].Resolved, "瞬时型触发落库即视为已解决（无恢复路径可依赖）")

	// ② 条件恢复：预测不再命中 → 本次不 Fire（不产生任何信号）。
	far := 300.0
	require.Equal(t, 0, alerter.Notify(model.MetricScopeNode, 7, "n1", []ForecastResult{{
		TargetID: "node-x", MetricKey: model.MetricNodeDiskUsed, Confidence: "high", ExhaustLowDays: &far,
	}}, 7))

	// ③ 再次恶化：**必须**再发事件（这是自审点名的首要误报场景）。
	// 把时钟推过重发间隔，模拟「数小时后趋势再次恶化」。
	clock = clock.Add(capacityReNotifyInterval + time.Minute)
	require.Equal(t, 1, alerter.Notify(model.MetricScopeNode, 7, "n1", danger, 7), "恢复后再次恶化须能重新告警")
	require.NoError(t, db.Order("id").Find(&events).Error)
	require.Len(t, events, 2, "第二次恶化应落新事件，而非永久累计旧事件")
	require.True(t, events[1].Resolved)
	require.Equal(t, 1, events[1].Count, "新事件计数从 1 起，不继承上一轮")
	require.True(t, events[1].FiredAt.After(events[0].FiredAt), "新事件时间晚于上一轮")
}

// TestCapacityTrendAlerter_GlobalRuleThrottled （m6）全局规则（TargetID=nil）在实例维度仍按
// 「每目标一条」语义（与 AlertEvaluator.evaluateNodeMetricRule 同族，不擅自禁止），
// 但必须由重发节流压制事件量：64 台实例反复查询不再「每次查询各建一条」。
func TestCapacityTrendAlerter_GlobalRuleThrottled(t *testing.T) {
	db := newAlertTestDB(t)
	dispatcher := NewAlertDispatcher(db)
	dispatcher.now = time.Now
	alerter := NewCapacityTrendAlerter(db, dispatcher)

	global := &model.AlertRule{
		Name: "全局堆内存", Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric,
		TargetType: "instance", Enabled: true, // TargetID = nil → 全局规则
	}
	require.NoError(t, db.Create(global).Error)

	low := 2.0
	results := []ForecastResult{{
		TargetID: "inst-uuid", MetricKey: model.MetricInstHeapUsed,
		Confidence: "high", ExhaustLowDays: &low,
	}}
	// 模拟 64 台实例 × 3 轮 60s 轮询 = 192 次查询。
	fired := 0
	for round := 0; round < 3; round++ {
		for id := uint(1); id <= 64; id++ {
			fired += alerter.Notify(model.MetricScopeInstance, id, "inst", results, 7)
		}
	}
	var events []model.AlertEvent
	require.NoError(t, db.Find(&events).Error)
	require.Equal(t, 64, len(events), "每目标每指标只落一条事件（首轮），后两轮被节流")
	require.Equal(t, 64, fired, "后两轮不得再触发")
	for i := range events {
		require.True(t, events[i].Resolved)
	}

	// 目标专属规则照常即时触发（不因节流逻辑误伤）。
	targetID := uint(9)
	scoped := &model.AlertRule{
		Name: "实例 9 堆", Level: model.AlertLevelWarn, TriggerType: model.AlertTriggerMetric,
		TargetType: "instance", TargetID: &targetID, Enabled: true,
	}
	require.NoError(t, db.Create(scoped).Error)
	require.Equal(t, 1, alerter.Notify(model.MetricScopeInstance, targetID, "inst-9", results, 7))
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 65)
}
