package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-485：日志采集面指标走时序取值路径，能按阈值触发并恢复。
//
// 覆盖的语义要点：
//  1. 别名（可读短名）与标准 key 都能命中——运维手写规则不该要求背长名；
//  2. 无新鲜样本时既**不触发也不伪造恢复**（缺测不等于恢复）；
//  3. 未注入时序源时跳过（宁可不动，也不能拿快照的 -1 当值比较）。
func TestEvaluator_LogMetricsFromSeries(t *testing.T) {
	cases := []struct {
		name      string
		metric    string
		key       string
		threshold float64
		value     float64
	}{
		{"state 文件尺寸（别名）", "log_state_size", model.MetricLogStateFileSize, 1e9, 1.2e9},
		{"state 文件增速（别名）", "log_index_growth", model.MetricLogStateFileGrowth, 5e7, 1.17e8},
		{"persist 写字节/秒（别名）", "log_write_rate", model.MetricLogPersistWrite, 5e7, 1.17e8},
		{"WAL 积压条数（别名）", "wal_backlog", model.MetricLogWALBacklog, 1000, 5000},
		{"WAL 暂停时长（别名）", "wal_paused", model.MetricLogWALPaused, 600, 3600},
		{"投递失败率（别名）", "log_delivery_failure", model.MetricLogDeliveryFailRate, 5, 42.5},
		{"校验退避次数（标准键）", model.MetricLogVerifyBackoff, model.MetricLogVerifyBackoff, 100, 850},
		{"慢 SQL 次数（别名）", "slow_sql", model.MetricLogSlowSQLCount, 10, 240},
		{"慢 SQL 最长耗时（别名）", "slow_sql_max_ms", model.MetricLogSlowSQLMaxMs, 500, 2400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newAlertTestDB(t)
			require.NoError(t, db.AutoMigrate(&model.Node{}))
			now := time.Now()
			node := &model.Node{Name: "采集节点", UUID: "node-logmetric", Status: model.NodeStatusOnline, LastHeartbeat: &now}
			require.NoError(t, db.Create(node).Error)
			rule := &model.AlertRule{Name: "日志面规则", UUID: "rule-" + tc.key, Enabled: true,
				TriggerType: model.AlertTriggerMetric, TargetType: "node", TargetID: &node.ID,
				Metric: tc.metric, Operator: ">", Threshold: tc.threshold, Level: model.AlertLevelWarn}
			require.NoError(t, db.Create(rule).Error)

			fake := newFakeMetricSource()
			eval := NewAlertEvaluator(db, NewAlertDispatcher(db))
			eval.SetMetrics(fake)

			// 未注入样本：既不能触发，也不能凭空恢复。
			eval.evaluate()
			var count int64
			require.NoError(t, db.Model(&model.AlertEvent{}).Count(&count).Error)
			require.Zero(t, count, "无样本时不应触发（缺测不等于越线）")

			fake.setLatest(model.MetricScopeNode, node.UUID, "", tc.key, tc.value)
			eval.evaluate()
			var events []model.AlertEvent
			require.NoError(t, db.Find(&events).Error)
			require.Len(t, events, 1, "新鲜越线样本应触发")
			require.Equal(t, node.ID, events[0].TargetID)
			require.InDelta(t, tc.value, events[0].Value, 1e-6, "时序值应原样使用，不得再换量纲")
			require.False(t, events[0].Resolved)

			// 回落到阈值以内应沿 dispatcher 恢复同一事件。
			fake.setLatest(model.MetricScopeNode, node.UUID, "", tc.key, tc.threshold/2)
			eval.evaluate()
			var event model.AlertEvent
			require.NoError(t, db.First(&event, events[0].ID).Error)
			require.True(t, event.Resolved, "回落到阈值内应恢复")
		})
	}
}

// FR-485：未注入时序源时，只存在于时序库的日志指标必须跳过而非误判。
//
// 变异验证：若把 isSeriesOnlyNodeMetric 的日志分支去掉，getNodeMetric 会返回 -1，
// 调用方的 `value < 0 → continue` 恰好也能跳过——但那是**碰巧**正确：
// 一旦有人给 getNodeMetric 加了返回 0 的默认分支，规则就会在无数据时恒判「未越线」，
// 并持续 Resolve 掉真实告警。本用例把「跳过」这件事钉在明面上。
func TestEvaluator_LogMetricsSkipWithoutSeriesSource(t *testing.T) {
	db := newAlertTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}))
	now := time.Now()
	node := &model.Node{Name: "采集节点2", UUID: "node-logmetric-nil", Status: model.NodeStatusOnline, LastHeartbeat: &now}
	require.NoError(t, db.Create(node).Error)
	rule := &model.AlertRule{Name: "无源规则", UUID: "rule-logmetric-nil", Enabled: true,
		TriggerType: model.AlertTriggerMetric, TargetType: "node", TargetID: &node.ID,
		Metric: model.MetricLogWALBacklog, Operator: ">", Threshold: 1, Level: model.AlertLevelWarn}
	require.NoError(t, db.Create(rule).Error)

	eval := NewAlertEvaluator(db, NewAlertDispatcher(db)) // 刻意不 SetMetrics
	eval.evaluate()

	var count int64
	require.NoError(t, db.Model(&model.AlertEvent{}).Count(&count).Error)
	require.Zero(t, count, "未注入时序源时日志指标应跳过")
}

// FR-485：isSeriesOnlyNodeMetric 的判据必须与 getNodeMetric 的口径一致。
//
// 这两处一旦分叉，症状是「规则存在、指标有数据、但告警永不触发」——最难查的一类静默失效。
func TestIsSeriesOnlyNodeMetric_CoversLogKeys(t *testing.T) {
	seriesOnly := []string{
		"iowait", "io_wait", model.MetricNodeIOWait,
		"log_state_size", model.MetricLogStateFileSize,
		"log_state_growth", model.MetricLogStateFileGrowth,
		"log_persist_write", model.MetricLogPersistWrite,
		"log_wal_backlog", model.MetricLogWALBacklog,
		"log_wal_paused", model.MetricLogWALPaused,
		"log_delivery_failure", model.MetricLogDeliveryFailRate,
		"log_verify_backoff", model.MetricLogVerifyBackoff,
		"log_slow_sql", model.MetricLogSlowSQLCount,
		"log_slow_sql_max", model.MetricLogSlowSQLMaxMs,
	}
	for _, metric := range seriesOnly {
		require.True(t, isSeriesOnlyNodeMetric(metric), "%s 应走时序取值路径", metric)
	}
	// 快照型指标绝不能走时序：它们在 nodes 表里，时序库没有对应序列。
	for _, metric := range []string{"cpu", "cpu_usage", "memory", "memory_usage", "disk", "disk_usage"} {
		require.False(t, isSeriesOnlyNodeMetric(metric), "%s 应走节点快照", metric)
	}
}

// FR-485：日志采集面指标的别名必须全部解析到真实时序键。
//
// 别名表缺一项的后果是规则保存成功、评估时 resolveMetricKey 失败 ⇒ 静默不告警。
func TestNodeMetricAliases_LogKeysResolve(t *testing.T) {
	expected := map[string]string{
		"log_state_size":       model.MetricLogStateFileSize,
		"log_index_growth":     model.MetricLogStateFileGrowth,
		"log_write_rate":       model.MetricLogPersistWrite,
		"wal_backlog":          model.MetricLogWALBacklog,
		"wal_paused":           model.MetricLogWALPaused,
		"log_delivery_failure": model.MetricLogDeliveryFailRate,
		"log_verify_backoff":   model.MetricLogVerifyBackoff,
		"slow_sql":             model.MetricLogSlowSQLCount,
		"slow_sql_max_ms":      model.MetricLogSlowSQLMaxMs,
	}
	for alias, want := range expected {
		got, ok := resolveMetricKey("node", alias)
		require.True(t, ok, "别名 %s 必须可解析", alias)
		require.Equal(t, want, got, "别名 %s 应解析到标准键", alias)
	}
	// 标准键自身也必须可解析（规则里直接写标准键是最自然的用法）。
	for _, key := range expected {
		got, ok := resolveMetricKey("node", key)
		require.True(t, ok, "标准键 %s 必须可解析", key)
		require.Equal(t, key, got)
	}
}

// FR-485：饱和度型「投递失败率」必须自身即百分比，否则规则永不触发。
//
// 变异验证：把 MetricLogDeliveryFailRate 从 saturationPercent 白名单移除 ⇒ 返回 (0,false)。
func TestSaturationPercent_DeliveryFailureRateIsSelfPercent(t *testing.T) {
	v := 42.5
	pct, ok := saturationPercent(model.MetricLogDeliveryFailRate, &v, nil)
	require.True(t, ok, "投递失败率应被识别为百分比型指标")
	require.InDelta(t, 42.5, pct, 1e-9)
}

// FR-485：磁盘/内存/CPU 的百分比口径键必须把快照分数（0..1）换算成百分比（0..100）。
//
// 这是本次取证发现的真实缺陷：nodes 表存的是 `UsedPercent/100`（0..1），而规则阈值
// 在界面与直觉上都是百分比。「磁盘 > 85」若拿 0..1 的 0.91 去比，永远不触发且不报错。
//
// 同时钉住「旧键语义不变」：直接改旧键会把存量规则的阈值悄悄放大 100 倍。
func TestGetNodeMetric_PercentKeysScaleFractions(t *testing.T) {
	node := &model.Node{CPUUsage: 0.755, MemoryUsage: 0.60, DiskUsage: 0.91}

	for _, tc := range []struct {
		metric string
		want   float64
	}{
		{"node_cpu_usage_pct", 75.5},
		{"node_memory_usage_pct", 60.0},
		{"node_mem_usage_pct", 60.0},
		{"node_disk_usage_pct", 91.0},
	} {
		t.Run(tc.metric, func(t *testing.T) {
			// 节点资源读数是 float32（来源 model.Node 的 float32 列），
			// 换算后的有效精度约 1e-5 —— 容差按 float32 的实际精度取，不假装它是 float64。
			require.InDelta(t, tc.want, getNodeMetric(node, tc.metric), 1e-4)
		})
	}

	// 旧键保持分数口径（存量规则零影响）。这条是**刻意**断言的现状，不是理想设计：
	// 改它属于破坏性变更，需要单独的迁移方案。
	require.InDelta(t, 0.91, getNodeMetric(node, "disk"), 1e-6)
	require.InDelta(t, 0.60, getNodeMetric(node, "memory"), 1e-6)
	require.InDelta(t, 0.755, getNodeMetric(node, "cpu"), 1e-6)
}

// FR-485：百分比键的规则在真实求值链路里按百分比触发（端到端，不只单测换算函数）。
func TestEvaluator_NodeDiskPercentThresholdFires(t *testing.T) {
	db := newAlertTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}))
	now := time.Now()
	// 磁盘用掉 91%（快照分数 0.91）。
	node := &model.Node{Name: "磁盘紧张", UUID: "node-diskpct", Status: model.NodeStatusOnline,
		LastHeartbeat: &now, DiskUsage: 0.91}
	require.NoError(t, db.Create(node).Error)
	rule := &model.AlertRule{Name: "磁盘超 85%", UUID: "rule-diskpct", Enabled: true,
		TriggerType: model.AlertTriggerMetric, TargetType: "node", TargetID: &node.ID,
		Metric: "node_disk_usage_pct", Operator: ">", Threshold: 85, Level: model.AlertLevelWarn}
	require.NoError(t, db.Create(rule).Error)

	eval := NewAlertEvaluator(db, NewAlertDispatcher(db))
	eval.evaluate()

	var events []model.AlertEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1, "91% > 85% 必须触发（这正是旧键表达不出来的阈值）")
	require.InDelta(t, 91, events[0].Value, 1e-4)

	// 降到 70% 应恢复。
	require.NoError(t, db.Model(node).Update("disk_usage", 0.70).Error)
	eval.evaluate()
	var event model.AlertEvent
	require.NoError(t, db.First(&event, events[0].ID).Error)
	require.True(t, event.Resolved)
}

// FR-485：采集健康仍走快照（不属时序型），确认新判据没有把它误伤成「跳过」。
func TestEvaluator_LogIngestHealthyStillUsesSnapshot(t *testing.T) {
	db := newAlertTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}))
	now := time.Now()
	node := &model.Node{Name: "停摆节点", UUID: "node-ingest-down", Status: model.NodeStatusOnline,
		LastHeartbeat: &now, LogIngestHealthy: false}
	require.NoError(t, db.Create(node).Error)
	rule := &model.AlertRule{Name: "采集停摆", UUID: "rule-ingest-down", Enabled: true,
		TriggerType: model.AlertTriggerMetric, TargetType: "node", TargetID: &node.ID,
		Metric: "log_ingest_healthy", Operator: "==", Threshold: 0, Level: model.AlertLevelCritical}
	require.NoError(t, db.Create(rule).Error)

	eval := NewAlertEvaluator(db, NewAlertDispatcher(db))
	eval.evaluate()

	var events []model.AlertEvent
	require.NoError(t, db.Find(&events).Error)
	require.Len(t, events, 1, "采集停摆应立即触发")
	require.Zero(t, events[0].Value)
}
