package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestEvaluator_NodeIOWaitMetric 覆盖普通阈值链路，真实时序源负责百分比值和新鲜度过滤。
func TestEvaluator_NodeIOWaitMetric(t *testing.T) {
	for _, metric := range []string{"iowait", "io_wait", model.MetricNodeIOWait} {
		t.Run(metric, func(t *testing.T) {
			db := newAlertTestDB(t)
			require.NoError(t, db.AutoMigrate(&model.Node{}, &model.MetricSeries{}, &model.MetricSampleRaw{}))
			now := time.Now().UTC()
			// 节点心跳沿评估器既有本地时区查询，时序样本独立使用 UTC。
			heartbeat := time.Now()
			node := &model.Node{Name: "目标节点", UUID: "node-iowait", Status: model.NodeStatusOnline, LastHeartbeat: &heartbeat}
			other := &model.Node{Name: "非目标节点", UUID: "other-iowait", Status: model.NodeStatusOnline, LastHeartbeat: &heartbeat}
			require.NoError(t, db.Create(node).Error)
			require.NoError(t, db.Create(other).Error)
			rule := &model.AlertRule{Name: "普通IO等待", UUID: "rule-iowait", Enabled: true,
				TriggerType: model.AlertTriggerMetric, TargetType: "node", TargetID: &node.ID,
				Metric: metric, Operator: ">", Threshold: 80, DurationSec: 0, Level: model.AlertLevelWarn}
			require.NoError(t, db.Create(rule).Error)
			metrics := NewMetricService(db)
			eval := NewAlertEvaluator(db, NewAlertDispatcher(db))
			eval.SetMetrics(metrics)
			sample := func(uuid string, ts time.Time, value *float64) Sample {
				return Sample{NodeUUID: uuid, Scope: model.MetricScopeNode, MetricKey: model.MetricNodeIOWait, Unit: "pct", TS: ts, Value: value}
			}
			// 非目标节点的新鲜高值不能替代目标节点的缺失或过期样本。
			require.NoError(t, metrics.Ingest([]Sample{sample(other.UUID, now, fp(99))}))
			eval.evaluate()
			var count int64
			require.NoError(t, db.Model(&model.AlertEvent{}).Count(&count).Error)
			require.Zero(t, count)
			require.NoError(t, metrics.Ingest([]Sample{sample(node.UUID, now.Add(-6*time.Minute), fp(83.5)), sample(node.UUID, now.Add(-time.Minute), nil)}))
			latest, err := metrics.LatestValue(model.MetricScopeNode, node.UUID, "", model.MetricNodeIOWait, now.Add(-alertMetricFreshWindow))
			require.NoError(t, err)
			require.Nil(t, latest, "真实时序源应过滤过期高值和空样本")
			eval.evaluate()
			require.NoError(t, db.Model(&model.AlertEvent{}).Count(&count).Error)
			require.Zero(t, count)

			require.NoError(t, metrics.Ingest([]Sample{sample(node.UUID, now.Add(-30*time.Second), fp(83.5))}))
			eval.SetMetrics(nil)
			eval.evaluate()
			require.NoError(t, db.Model(&model.AlertEvent{}).Count(&count).Error)
			require.Zero(t, count, "未注入时序源时跳过 iowait")
			eval.SetMetrics(metrics)
			eval.evaluate()
			var events []model.AlertEvent
			require.NoError(t, db.Find(&events).Error)
			require.Len(t, events, 1, "普通 metric 的 83.5 > 80 应立即触发")
			require.Equal(t, model.AlertTriggerMetric, events[0].TriggerType)
			require.Equal(t, node.ID, events[0].TargetID)
			require.Equal(t, 83.5, events[0].Value, "时序值已是百分比，不得再次乘百")
			require.False(t, events[0].Resolved)

			// 当前高值过期且只有缺测时不能伪造恢复，已有事件必须保持未恢复。
			require.NoError(t, db.Model(&model.MetricSampleRaw{}).Where("value IS NOT NULL").Update("ts", now.Add(-6*time.Minute)).Error)
			eval.evaluate()
			var event model.AlertEvent
			require.NoError(t, db.First(&event, events[0].ID).Error)
			require.False(t, event.Resolved)
			require.NoError(t, metrics.Ingest([]Sample{sample(node.UUID, now, fp(20))}))
			eval.evaluate()
			require.NoError(t, db.First(&event, events[0].ID).Error)
			require.True(t, event.Resolved, "新鲜低值应沿 dispatcher 恢复同一普通阈值事件")
			require.NoError(t, db.Model(&model.AlertEvent{}).Count(&count).Error)
			require.Equal(t, int64(1), count)
		})
	}
}

func TestEvaluator_NodeMetricSnapshotUnchanged(t *testing.T) {
	for _, metric := range []string{"cpu", "cpu_usage", "memory", "memory_usage", "disk", "disk_usage"} {
		t.Run(metric, func(t *testing.T) {
			db := newAlertTestDB(t)
			require.NoError(t, db.AutoMigrate(&model.Node{}))
			now := time.Now()
			node := &model.Node{Name: "快照节点", UUID: "snapshot-node", Status: model.NodeStatusOnline,
				LastHeartbeat: &now, CPUUsage: 83.5, MemoryUsage: 83.5, DiskUsage: 83.5}
			require.NoError(t, db.Create(node).Error)
			rule := &model.AlertRule{Name: "快照规则", UUID: "snapshot-rule", Enabled: true,
				TriggerType: model.AlertTriggerMetric, TargetType: "node", Metric: metric,
				Operator: ">", Threshold: 80, Level: model.AlertLevelWarn}
			require.NoError(t, db.Create(rule).Error)
			// 未注入时序源也必须继续使用既有节点快照百分比。
			eval := NewAlertEvaluator(db, NewAlertDispatcher(db))
			eval.evaluate()
			var events []model.AlertEvent
			require.NoError(t, db.Find(&events).Error)
			require.Len(t, events, 1)
			require.Equal(t, 83.5, events[0].Value)
			require.NoError(t, db.Model(node).Updates(map[string]interface{}{"cpu_usage": 20, "memory_usage": 20, "disk_usage": 20}).Error)
			eval.evaluate()
			var event model.AlertEvent
			require.NoError(t, db.First(&event, events[0].ID).Error)
			require.True(t, event.Resolved)
		})
	}
}

// iowait 与 cpu 同为百分比型指标（unit=pct），必须「自身即饱和度」。
//
// 变异验证：把 MetricNodeIOWait 从 saturationPercent 的 case 白名单里去掉，
// 本测试立即转红（返回 (0,false)）。这正是切片 4b 修掉的静默失效缺口——
// 它不报错、不告警，只是让饱和度型 iowait 规则永不触发。
func TestSaturationPercent_IOWaitIsSelfPercent(t *testing.T) {
	v := 83.5
	pct, ok := saturationPercent(model.MetricNodeIOWait, &v, nil)
	require.True(t, ok, "iowait 应被识别为百分比型指标（自身即饱和度）")
	require.InDelta(t, 83.5, pct, 1e-9)

	// 对照：磁盘已用指标走 used/max 分支，无配对上限时不得当成百分比处理，
	// 否则「磁盘百分比」会退化成拿字节数当百分比比较。
	disk := 512.0
	if _, ok := saturationPercent(model.MetricNodeDiskUsed, &disk, nil); ok {
		t.Fatal("无配对上限的 disk_used 不应被判为百分比型")
	}

	// 对照：磁盘在「有上限」时确实走 used/max → 百分比（条目要求的另一半）
	limit := 1024.0
	pct, ok = saturationPercent(model.MetricNodeDiskUsed, &disk, &limit)
	require.True(t, ok)
	require.InDelta(t, 50.0, pct, 1e-9)
}
