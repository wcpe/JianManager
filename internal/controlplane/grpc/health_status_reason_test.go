package grpc

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// FR-459 复审项 3（FR-312 回归）：巡检上报的**泛化**崩溃原因不得覆盖 CP 已写入的具体原因。
func TestSyncInstanceStates_DoesNotClobberSpecificCrashReason(t *testing.T) {
	h, db, node := newStateSyncFixture(t)

	// 具体原因：CP 侧异步委托失败时写入（如未绑定 JDK / Worker 操作失败）。
	specific := &model.Instance{
		UUID: "i-specific", NodeID: node.ID, Name: "specific",
		Status: model.InstanceStatusCrashed, StatusReason: "实例未绑定 JDK：请先在实例配置中选择 JDK",
	}
	require.NoError(t, db.Create(specific).Error)

	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{
		InstanceUuid: "i-specific",
		State:        string(model.InstanceStatusCrashed),
		Health:       "crashed",
		StatusReason: "实例已崩溃（等待自动重启）",
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-specific").First(&got).Error)
	require.Equal(t, "实例未绑定 JDK：请先在实例配置中选择 JDK", got.StatusReason,
		"CP 已写入的具体崩溃原因不得被巡检泛化原因覆盖（FR-312 回归）")
	require.Equal(t, model.InstanceStatusCrashed, got.Status)
}

// FR-459 终验 Major 回归（第二轮修复引入）：RUNNING 且带**非空**巡检原因（假死）的实例真崩溃后，
// status 必须变 CRASHED——不得因 status_reason 非空而让「库中原因为空」条件连带冻结整条 UPDATE。
func TestSyncInstanceStates_CrashedUpdatesStatusDespiteStaleReason(t *testing.T) {
	h, db, node := newStateSyncFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-desync", NodeID: node.ID, Name: "desync",
		Status: model.InstanceStatusRunning, StatusReason: "假死：进程在但 tcp 响应探测连续失败 3 次",
	}).Error)

	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{
		InstanceUuid: "i-desync",
		State:        string(model.InstanceStatusCrashed),
		Health:       "crashed",
		StatusReason: "实例已崩溃（等待自动重启）",
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-desync").First(&got).Error)
	require.Equal(t, model.InstanceStatusCrashed, got.Status,
		"真崩溃必须更新 status（此前非空 status_reason 会冻结整条 UPDATE，status 卡在 RUNNING）")
	require.Equal(t, "假死：进程在但 tcp 响应探测连续失败 3 次", got.StatusReason,
		"泛化崩溃原因不得覆盖库中已有非空原因（原因清理由健康恢复/停止转态负责）")
}

// 库中原因为空时，巡检泛化原因仍应补写（保持可见性）。
func TestSyncInstanceStates_FillsReasonWhenEmpty(t *testing.T) {
	h, db, node := newStateSyncFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-empty", NodeID: node.ID, Name: "empty",
		Status: model.InstanceStatusCrashed, StatusReason: "",
	}).Error)

	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{
		InstanceUuid: "i-empty",
		State:        string(model.InstanceStatusCrashed),
		Health:       "crashed",
		StatusReason: "实例已崩溃（等待自动重启）",
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-empty").First(&got).Error)
	require.Equal(t, "实例已崩溃（等待自动重启）", got.StatusReason, "原因为空时应补写巡检原因")
}

// 熔断原因是需要人工介入的具体升级原因，必须覆盖旧原因（否则面板看不到熔断）。
func TestSyncInstanceStates_CircuitBrokenOverwritesReason(t *testing.T) {
	h, db, node := newStateSyncFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-broken", NodeID: node.ID, Name: "broken",
		Status: model.InstanceStatusCrashed, StatusReason: "旧原因",
	}).Error)

	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{
		InstanceUuid: "i-broken",
		State:        string(model.InstanceStatusCrashed),
		Health:       "circuit_broken",
		StatusReason: "持续崩溃已熔断：10m0s 内重启 5 次，已停止自动重启等待人工确认",
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-broken").First(&got).Error)
	require.Contains(t, got.StatusReason, "熔断", "熔断原因必须写入面板")
}

// 假死实例状态仍是 RUNNING：其巡检原因（供健康墙降级判据）应写入。
func TestSyncInstanceStates_WritesDeadReasonOnRunning(t *testing.T) {
	h, db, node := newStateSyncFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-dead", NodeID: node.ID, Name: "dead",
		Status: model.InstanceStatusRunning, StatusReason: "",
	}).Error)

	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{
		InstanceUuid: "i-dead",
		State:        string(model.InstanceStatusRunning),
		Health:       "dead",
		StatusReason: "假死：进程在但 tcp 响应探测连续失败 3 次",
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-dead").First(&got).Error)
	require.Contains(t, got.StatusReason, "假死", "假死原因应写入（健康墙据此降级）")
}

// 巡检明确健康且无原因 → 清空历史原因（假死已恢复）。
func TestSyncInstanceStates_ClearsReasonWhenHealthy(t *testing.T) {
	h, db, node := newStateSyncFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-ok", NodeID: node.ID, Name: "ok",
		Status: model.InstanceStatusRunning, StatusReason: "假死：进程在但探测连续失败 3 次",
	}).Error)

	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{
		InstanceUuid: "i-ok",
		State:        string(model.InstanceStatusRunning),
		Health:       "healthy",
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-ok").First(&got).Error)
	require.Empty(t, got.StatusReason, "明确健康应清空历史原因")
}

// 老 Worker 不上报 health/status_reason（均空）：不得触碰库中现有原因。
func TestSyncInstanceStates_LegacyWorkerKeepsReason(t *testing.T) {
	h, db, node := newStateSyncFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-legacy", NodeID: node.ID, Name: "legacy",
		Status: model.InstanceStatusRunning, StatusReason: "历史原因",
	}).Error)

	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{
		InstanceUuid: "i-legacy",
		State:        string(model.InstanceStatusRunning),
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-legacy").First(&got).Error)
	require.Equal(t, "历史原因", got.StatusReason)
}

func newStateSyncFixture(t *testing.T) (*ControlPlaneHandler, *gorm.DB, *model.Node) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	node := &model.Node{UUID: "node-sync", Name: "n", Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	h := NewControlPlaneHandler(db, NewClientPool())
	return h, db, node
}
