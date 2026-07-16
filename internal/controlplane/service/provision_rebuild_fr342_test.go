package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestProvisionFailure_EntersDamagedWithSpec 搭建下载失败 → 实例进 DAMAGED、原因写「搭建未完成」、
// 搭建参数存 ProvisionSpec 供重建（FR-342）。
func TestProvisionFailure_EntersDamagedWithSpec(t *testing.T) {
	svc, taskSvc, node, done := newFR319Harness(t, &failingDownloadWorker{})
	defer done()

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "dmg", CoreType: "spongevanilla", MCVersion: "1.21.1", MemoryMb: 1024,
	}, 1)
	require.NoError(t, err)
	waitTaskTerminal(t, taskSvc, taskID)

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	assert.Equal(t, model.InstanceStatusDamaged, got.Status, "搭建失败应进损毁态（非静默 STOPPED）")
	assert.Contains(t, got.StatusReason, "搭建未完成")
	assert.NotEmpty(t, got.ProvisionSpec, "搭建参数应存 ProvisionSpec 供重建复用")
}

// TestRebuildInstance_Guards 重建守卫：非损毁 / 损毁但无参数 分别拒绝（FR-342）。
func TestRebuildInstance_Guards(t *testing.T) {
	svc, _, node, done := newFR319Harness(t, &fakeProvisionWorker{})
	defer done()

	stopped := &model.Instance{NodeID: node.ID, Name: "s", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: model.InstanceStatusStopped}
	require.NoError(t, svc.db.Create(stopped).Error)
	_, err := svc.RebuildInstance(context.Background(), stopped.ID, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "仅损毁", "非损毁实例不可重建")

	dmg := &model.Instance{NodeID: node.ID, Name: "d", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: model.InstanceStatusDamaged}
	require.NoError(t, svc.db.Create(dmg).Error)
	_, err = svc.RebuildInstance(context.Background(), dmg.ID, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "无可复用的搭建参数", "损毁但无参数不可重建")
}

// TestRebuildInstance_Succeeds 损毁实例换好 worker 后重建 → 复用参数重跑搭建成功 → STOPPED（FR-342）。
func TestRebuildInstance_Succeeds(t *testing.T) {
	svc, taskSvc, node, done := newFR319Harness(t, &failingDownloadWorker{})
	defer done()

	// 先用下载失败 worker 造损毁。
	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "rb", CoreType: "spongevanilla", MCVersion: "1.21.1", MemoryMb: 1024,
	}, 1)
	require.NoError(t, err)
	waitTaskTerminal(t, taskSvc, taskID)
	var dmg model.Instance
	require.NoError(t, svc.db.First(&dmg, inst.ID).Error)
	require.Equal(t, model.InstanceStatusDamaged, dmg.Status)

	// 换成功 worker → 重建复用已存参数（无需重填）。
	svc.pool.SetWorkerClientForTest(node.UUID, &fakeProvisionWorker{})
	rbTaskID, err := svc.RebuildInstance(context.Background(), inst.ID, 1)
	require.NoError(t, err)
	waitTaskTerminal(t, taskSvc, rbTaskID)

	var rebuilt model.Instance
	require.NoError(t, svc.db.First(&rebuilt, inst.ID).Error)
	assert.Equal(t, model.InstanceStatusStopped, rebuilt.Status, "重建成功应回 STOPPED 可启动")
	assert.Empty(t, rebuilt.StatusReason, "重建成功应清空失败原因")
}

// TestStart_RejectsDamaged 损毁实例不可直接启动（FR-342）：Start 返回 *PreflightError，状态保持 DAMAGED。
func TestStart_RejectsDamaged(t *testing.T) {
	svc, _, _, inst := newPreflightFixture(t)
	require.NoError(t, svc.db.Model(inst).Update("status", model.InstanceStatusDamaged).Error)

	err := svc.Start(inst.ID)
	var pfErr *PreflightError
	require.ErrorAs(t, err, &pfErr)
	assert.Contains(t, pfErr.Reason, "损毁")

	got, err := svc.GetByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, model.InstanceStatusDamaged, got.Status, "损毁实例启动被拦，状态不变")
}
