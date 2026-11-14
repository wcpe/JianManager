package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// delivery_scope_probe_test.go —— 配置交付域越权修复的回归用例。

// TestDelivery_UpdateScopedCannotEscape：UpdateScoped 必须把新 selector 收窄到可见集合。
func TestDelivery_UpdateScopedCannotEscape(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 3, 3)
	file := seedConfigFileWithLayer(t, db, "a=1\n")
	svc, _, _ := newDeliveryTestService(t, db, nil)

	mine := []uint{insts[0].ID}
	theirs := []uint{insts[1].ID, insts[2].ID}

	in := ConfigDeliveryInput{
		ConfigFileID:       file.ID,
		FilePath:           file.Name,
		TargetLayerLevel:   model.ConfigScopeGroup,
		TargetLayerScopeID: 1,
		Selector:           DeliverySelector{InstanceIDs: mine},
		BatchMode:          model.DeliveryBatchPercent,
		BatchSizes:         []int{100},
		ActivationMethod:   model.ActivationPushOnly,
	}
	order, err := svc.CreateScoped(in, 1, BusinessTargetScope{Scoped: true, IDs: mine})
	require.NoError(t, err)

	// 试着把目标换成不可见实例。
	hijack := in
	hijack.Selector = DeliverySelector{InstanceIDs: theirs}
	_, err = svc.UpdateScoped(order.ID, hijack, 1, BusinessTargetScope{Scoped: true, IDs: mine})
	// 收窄后没有目标 → ErrDeliveryEmptyTarget（越权目标被全部剔除）。
	require.ErrorIs(t, err, ErrDeliveryEmptyTarget,
		"越权 selector 被收窄为空时必须报错，而不是把 B、C 写进单里")

	after, err := svc.TargetInstanceIDs(order.ID)
	require.NoError(t, err)
	require.Equal(t, mine, after, "原目标集合不得被改成不可见实例")
}

// TestDelivery_UpdateScopedNavigatesNodeSelector：nodeIds 维度也必须求交，不得带出同节点其他实例。
func TestDelivery_UpdateScopedNodeSelectorIntersects(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 3, 3)
	file := seedConfigFileWithLayer(t, db, "a=1\n")
	svc, _, _ := newDeliveryTestService(t, db, nil)

	mine := []uint{insts[0].ID}
	in := ConfigDeliveryInput{
		ConfigFileID:       file.ID,
		FilePath:           file.Name,
		TargetLayerLevel:   model.ConfigScopeGroup,
		TargetLayerScopeID: 1,
		Selector:           DeliverySelector{InstanceIDs: mine},
		BatchMode:          model.DeliveryBatchPercent,
		BatchSizes:         []int{100},
		ActivationMethod:   model.ActivationPushOnly,
	}
	order, err := svc.CreateScoped(in, 1, BusinessTargetScope{Scoped: true, IDs: mine})
	require.NoError(t, err)

	// 同一节点下还有 insts[1]、insts[2]，但只有 insts[0] 可见。
	hijack := in
	hijack.Selector = DeliverySelector{NodeIDs: []uint{insts[0].NodeID}}
	updated, err := svc.UpdateScoped(order.ID, hijack, 1, BusinessTargetScope{Scoped: true, IDs: mine})
	require.NoError(t, err)
	require.Equal(t, 1, updated.PlannedCount, "nodeIds 必须与可见集合求交")

	after, err := svc.TargetInstanceIDs(order.ID)
	require.NoError(t, err)
	require.Equal(t, mine, after, "不得带出同节点的兄弟实例")
}

// TestDelivery_ImpactScopedHidesUnauthorized：Impact 不得遣露未授权实例的 ID/名称。
func TestDelivery_ImpactScopedHidesUnauthorized(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 3, 3)
	file := seedConfigFileWithLayer(t, db, "a=1\n")
	svc, _, _ := newDeliveryTestService(t, db, nil)

	mine := []uint{insts[0].ID}
	// 单子用 nodeIds 选：全节点三台。
	in := ConfigDeliveryInput{
		ConfigFileID:       file.ID,
		FilePath:           file.Name,
		TargetLayerLevel:   model.ConfigScopeGroup,
		TargetLayerScopeID: 1,
		Selector:           DeliverySelector{NodeIDs: []uint{insts[0].NodeID}},
		BatchMode:          model.DeliveryBatchPercent,
		BatchSizes:         []int{100},
		ActivationMethod:   model.ActivationPushOnly,
	}
	// 管理员建单：三台全进。
	order, err := svc.CreateScoped(in, 1, BusinessTargetScope{})
	require.NoError(t, err)
	require.Equal(t, 3, order.PlannedCount)

	// 非管理员看 Impact：只能看到自己的那台。
	impact, err := svc.ImpactScoped(order.ID, BusinessTargetScope{Scoped: true, IDs: mine})
	require.NoError(t, err)
	require.Equal(t, 1, impact.Planned, "Impact 必须按可见集合收窄")
	require.Len(t, impact.Targets, 1)
	require.Equal(t, insts[0].ID, impact.Targets[0].InstanceID)
	// 不得出现其它实例的 ID 或名称。
	for _, it := range impact.Targets {
		require.NotEqual(t, insts[1].ID, it.InstanceID)
		require.NotEqual(t, insts[2].ID, it.InstanceID)
	}
}

// TestDelivery_ImpactScopedAdminSeesAll 平台管理员（不收敛）仍看到全部。
func TestDelivery_ImpactScopedAdminSeesAll(t *testing.T) {
	db := newDeliveryTestDB(t)
	insts := seedDeliveryInstances(t, db, 3, 3)
	file := seedConfigFileWithLayer(t, db, "a=1\n")
	svc, _, _ := newDeliveryTestService(t, db, nil)

	in := ConfigDeliveryInput{
		ConfigFileID:       file.ID,
		FilePath:           file.Name,
		TargetLayerLevel:   model.ConfigScopeGroup,
		TargetLayerScopeID: 1,
		Selector:           DeliverySelector{NodeIDs: []uint{insts[0].NodeID}},
		BatchMode:          model.DeliveryBatchPercent,
		BatchSizes:         []int{100},
		ActivationMethod:   model.ActivationPushOnly,
	}
	order, err := svc.CreateScoped(in, 1, BusinessTargetScope{})
	require.NoError(t, err)

	impact, err := svc.ImpactScoped(order.ID, BusinessTargetScope{})
	require.NoError(t, err)
	require.Equal(t, 3, impact.Planned, "不收敛时必须看到全部")
}
