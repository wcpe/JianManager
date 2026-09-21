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
