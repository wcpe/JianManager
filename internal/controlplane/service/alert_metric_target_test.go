package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestP3Fix_InstanceMetricRuleIsCreatable 复现「FR-464 实例级容量趋势告警不可达」。
//
// 缺陷现场（真机验收 2026-09-23 发现）：
//   - `evaluateInstanceMetricRule` 早已支持实例级 metric 规则（FR-462 补齐），
//     `CapacityTrendAlerter.Notify` 也按 `target_type="instance"` 查规则；
//   - 但 `expectedTargetTypeForTrigger` 仍把 `metric` 硬编码为必须 `node` 目标，
//     且前端 `targetTypeForTrigger` 同规则 → **实例级 metric 规则根本建不出来**。
//   - 后果：`Notify` 的 `Find(&rules)` 返回空 → 实例维度容量预测即使算出
//     `exhaustLowDays` 也永远 `return 0`，趋势告警静默失效。
//
// 本用例断言「实例级 metric 规则可创建」——修复前必失败。
func TestP3Fix_InstanceMetricRuleIsCreatable(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)

	targetID := uint(18)
	rule, err := svc.CreateRule(CreateRuleRequest{
		Name:        "实例级容量趋势告警",
		TriggerType: "metric",
		Level:       "warn",
		TargetType:  "instance",
		TargetID:    &targetID,
		Metric:      "inst_heap_used",
		Operator:    "gt",
		Threshold:   0,
	})
	require.NoError(t, err, "实例级 metric 规则必须可创建（否则 FR-464 实例维度容量告警不可达）")
	require.NotNil(t, rule)
	assert.Equal(t, "instance", rule.TargetType)
	assert.Equal(t, "metric", rule.TriggerType)
}

// TestP3Fix_NodeMetricRuleStillCreatable 对照：节点级 metric 规则不得因本次修复而失效。
func TestP3Fix_NodeMetricRuleStillCreatable(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertService(db)

	nodeID := uint(2)
	rule, err := svc.CreateRule(CreateRuleRequest{
		Name:        "节点级指标阈值",
		TriggerType: "metric",
		Level:       "warn",
		TargetType:  "node",
		TargetID:    &nodeID,
		Metric:      "node_cpu_pct",
		Operator:    "gt",
		Threshold:   90,
	})
	require.NoError(t, err, "节点级 metric 规则是既有能力，不得回归")
	assert.Equal(t, "node", rule.TargetType)
}

// TestP3Fix_MetricTriggerAcceptsBothTargets 锁定校验函数本身：metric 两种目标都合法，
// 而 node_offline 仍必须 node（后者是物理约束——离线判定在节点维度）。
func TestP3Fix_MetricTriggerAcceptsBothTargets(t *testing.T) {
	assert.Equal(t, "", expectedTargetTypeForTrigger("metric"),
		"metric 触发在 node/instance 两个维度都有评估器，校验不应限定单一目标")
	assert.Equal(t, "node", expectedTargetTypeForTrigger("node_offline"),
		"节点离线判定只在节点维度，仍必须 node 目标")
	assert.Equal(t, "instance", expectedTargetTypeForTrigger("instance_crash"))
	assert.Equal(t, "", expectedTargetTypeForTrigger("baseline"))
	assert.Equal(t, "", expectedTargetTypeForTrigger("saturation"))
}

// TestP3Fix_InstanceCapacityAlertFiresEndToEnd 端到端复现真机场景：
// 经 API 建**实例级** metric 规则 → CapacityTrendAlerter.Notify 必须能触发告警。
//
// 这是真机验收（2026-09-23）暴露的完整链路：修复前规则建不出来 → Notify 的
// `target_type="instance"` 查询返回空 → 实例维度容量趋势告警静默失效。
func TestP3Fix_InstanceCapacityAlertFiresEndToEnd(t *testing.T) {
	db := newAlertTestDB(t)
	dispatcher := NewAlertDispatcher(db)
	alerter := NewCapacityTrendAlerter(db, dispatcher)

	// ① 经 CreateRule 建实例级 metric 规则（修复前此处即失败）
	targetID := uint(18)
	rule, err := NewAlertService(db).CreateRule(CreateRuleRequest{
		Name: "实例 heap 容量趋势", TriggerType: "metric", Level: "warn",
		TargetType: "instance", TargetID: &targetID,
		Metric: "inst_heap_used", Operator: "gt", Threshold: 0,
	})
	require.NoError(t, err, "实例级 metric 规则必须可建（真机缺陷点）")

	// ② 模拟容量预测命中：low < thresholdDays
	low := 0.06
	require.Equal(t, 1,
		alerter.Notify(model.MetricScopeInstance, targetID, "p3-probe",
			[]ForecastResult{{
				TargetID: "inst-uuid", MetricKey: model.MetricInstHeapUsed,
				NowValue: 512 << 20, LimitValue: 2 << 30, Confidence: "high",
				ExhaustLowDays: &low, Samples: 145,
			}}, 7),
		"实例维度容量趋势告警必须触发（修复前恒为 0）")

	// ③ 事件落库且去抖键为实例维度
	var ev model.AlertEvent
	require.NoError(t, db.Where("rule_id = ?", rule.ID).First(&ev).Error)
	assert.Contains(t, ev.DedupKey, "capacity:inst_heap_used")
	assert.Equal(t, rule.ID, ev.RuleID)

	// ④ 节流窗口内重复查询不重复告警
	assert.Equal(t, 0,
		alerter.Notify(model.MetricScopeInstance, targetID, "p3-probe",
			[]ForecastResult{{
				TargetID: "inst-uuid", MetricKey: model.MetricInstHeapUsed,
				Confidence: "high", ExhaustLowDays: &low, Samples: 145,
			}}, 7),
		"重发节流窗口内不得重复告警")
}
