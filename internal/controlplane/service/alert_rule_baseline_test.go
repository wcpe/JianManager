package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestAlertService_CreateBaselineRuleDefaultsAndValidation(t *testing.T) {
	svc := NewAlertService(newAlertTestDB(t))

	rule, err := svc.CreateRule(CreateRuleRequest{
		Name:        "mem-baseline",
		TriggerType: model.AlertTriggerBaseline,
		TargetType:  "node",
		Metric:      "memory",
	})
	require.NoError(t, err)
	assert.Equal(t, model.BaselineMethodEWMA, rule.BaselineMethod)
	assert.Equal(t, baselineDefaultWindowSec, rule.BaselineWindowSec)
	assert.Equal(t, 3.0, rule.Sensitivity)
	assert.Equal(t, model.BaselineDirectionBoth, rule.Direction)
	assert.Equal(t, "node", rule.Scope)

	// 非法基线方法应被拒。
	_, err = svc.CreateRule(CreateRuleRequest{
		Name: "bad", TriggerType: model.AlertTriggerBaseline, TargetType: "node", BaselineMethod: "nope",
	})
	require.Error(t, err)

	// baseline 允许 instance 目标（维度由 Scope 决定）。
	inst, err := svc.CreateRule(CreateRuleRequest{
		Name: "tps-baseline", TriggerType: model.AlertTriggerBaseline,
		TargetType: "instance", Scope: "instance", Metric: "tps",
		BaselineMethod: model.BaselineMethodROC, Direction: "down",
	})
	require.NoError(t, err)
	assert.Equal(t, "instance", inst.Scope)
	assert.Equal(t, model.BaselineMethodROC, inst.BaselineMethod)
}

func TestAlertService_CreateSaturationRule(t *testing.T) {
	svc := NewAlertService(newAlertTestDB(t))
	rule, err := svc.CreateRule(CreateRuleRequest{
		Name: "disk-sat", TriggerType: model.AlertTriggerSaturation,
		TargetType: "node", Scope: "node", Metric: "disk", Threshold: 90,
	})
	require.NoError(t, err)
	assert.Equal(t, model.AlertTriggerSaturation, rule.TriggerType)
	assert.Equal(t, 90.0, rule.Threshold)

	// 非法评估维度被拒。
	_, err = svc.CreateRule(CreateRuleRequest{
		Name: "bad", TriggerType: model.AlertTriggerSaturation, TargetType: "node", Scope: "cluster",
	})
	require.Error(t, err)

	// 阈值缺省 0（或 >100）会使 pct 恒真 → 告警风暴，必须拒绝。
	for _, bad := range []float64{0, -1, 100.5} {
		_, err = svc.CreateRule(CreateRuleRequest{
			Name: "bad-threshold", TriggerType: model.AlertTriggerSaturation,
			TargetType: "node", Scope: "node", Metric: "disk", Threshold: bad,
		})
		require.Errorf(t, err, "饱和度阈值 %g 应被拒绝", bad)
	}

	// 阈值上界 100 合法。
	_, err = svc.CreateRule(CreateRuleRequest{
		Name: "full-sat", TriggerType: model.AlertTriggerSaturation,
		TargetType: "node", Scope: "node", Metric: "disk", Threshold: 100,
	})
	require.NoError(t, err)
}

// TestAlertService_SaturationThresholdValidationOnUpdate 更新饱和度规则时同样校验阈值（含现状值）。
func TestAlertService_SaturationThresholdValidationOnUpdate(t *testing.T) {
	svc := NewAlertService(newAlertTestDB(t))
	rule, err := svc.CreateRule(CreateRuleRequest{
		Name: "disk-sat", TriggerType: model.AlertTriggerSaturation,
		TargetType: "node", Scope: "node", Metric: "disk", Threshold: 90,
	})
	require.NoError(t, err)

	bad := 0.0
	_, err = svc.UpdateRule(rule.ID, UpdateRuleRequest{Threshold: &bad})
	require.Error(t, err, "更新为 0 阈值应被拒绝")

	ok := 85.0
	updated, err := svc.UpdateRule(rule.ID, UpdateRuleRequest{Threshold: &ok})
	require.NoError(t, err)
	assert.Equal(t, 85.0, updated.Threshold)
}

// TestAlertService_BaselineIgnoresThreshold baseline 规则自算基线，静态阈值应被忽略清零。
func TestAlertService_BaselineIgnoresThreshold(t *testing.T) {
	svc := NewAlertService(newAlertTestDB(t))
	rule, err := svc.CreateRule(CreateRuleRequest{
		Name: "mem-baseline", TriggerType: model.AlertTriggerBaseline,
		TargetType: "node", Metric: "memory", Threshold: 77,
	})
	require.NoError(t, err)
	assert.Zero(t, rule.Threshold, "baseline 规则应忽略并清零静态阈值")
}

func TestAlertService_UpdateBaselineRuleFields(t *testing.T) {
	svc := NewAlertService(newAlertTestDB(t))
	rule, err := svc.CreateRule(CreateRuleRequest{
		Name: "mem-baseline", TriggerType: model.AlertTriggerBaseline, TargetType: "node", Metric: "memory",
	})
	require.NoError(t, err)

	sensitivity := 4.5
	direction := model.BaselineDirectionUp
	window := 1800
	updated, err := svc.UpdateRule(rule.ID, UpdateRuleRequest{
		Sensitivity: &sensitivity, Direction: &direction, BaselineWindowSec: &window,
	})
	require.NoError(t, err)
	assert.Equal(t, 4.5, updated.Sensitivity)
	assert.Equal(t, model.BaselineDirectionUp, updated.Direction)
	assert.Equal(t, 1800, updated.BaselineWindowSec)
}
