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
// 注意种子必须带来源标记 worker：该列双语义混用，空来源表示「CP 写入的生命周期失败原因」，
// 按新口径心跳不得抹平（见 TestSyncInstanceStates_HealthyHeartbeatKeepsCPFailureReason）。
func TestSyncInstanceStates_ClearsReasonWhenHealthy(t *testing.T) {
	h, db, node := newStateSyncFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-ok", NodeID: node.ID, Name: "ok",
		Status: model.InstanceStatusRunning, StatusReason: "假死：进程在但探测连续失败 3 次",
		StatusReasonSource: "worker",
	}).Error)

	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{
		InstanceUuid: "i-ok",
		State:        string(model.InstanceStatusRunning),
		Health:       "healthy",
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-ok").First(&got).Error)
	require.Empty(t, got.StatusReason, "明确健康应清空历史原因")
	require.Empty(t, got.StatusReasonSource, "清空后来源标记应复位，避免残留 worker 标记误伤后续 CP 写入的原因")
}

// FR-312 回归（本轮修复）：心跳不得抹平 CP 写入的生命周期失败原因。
// 场景：CP 异步委托失败写入具体原因（来源标记为空）后，下一拍 Worker 上报 RUNNING+healthy 且不带原因，
// 旧行为无条件把 status_reason 置空，刚写下的原因下一拍就被抹掉——用户只看得到「运行中」而无任何原因。
func TestSyncInstanceStates_HealthyHeartbeatKeepsCPFailureReason(t *testing.T) {
	h, db, node := newStateSyncFixture(t)
	const cpReason = "重启前同步最新启动规格失败，已取消重启: 节点未连接"
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-cp-reason", NodeID: node.ID, Name: "cp-reason",
		Status: model.InstanceStatusCrashed, StatusReason: cpReason,
	}).Error)

	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{
		InstanceUuid: "i-cp-reason",
		State:        string(model.InstanceStatusRunning),
		Health:       "healthy",
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-cp-reason").First(&got).Error)
	require.Equal(t, cpReason, got.StatusReason, "CP 写入的具体失败原因必须留存，不得被心跳抹平（FR-312）")
	require.Equal(t, model.InstanceStatusRunning, got.Status, "status 仍按心跳推进（原因留存不得连带冻结状态）")
	require.Empty(t, got.StatusReasonSource, "来源标记不得被心跳改写为 worker（否则下一拍会被当成心跳自有原因清掉）")
}

// 心跳自写原因的两拍闭环：写入时标来源 worker，下一拍明确健康时仍能正常清空并复位标记。
// 守护的是「修复不得把心跳自己的巡检原因也保护起来」（假死恢复后必须能清除）。
func TestSyncInstanceStates_WorkerSourcedReasonStillClearable(t *testing.T) {
	h, db, node := newStateSyncFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-round", NodeID: node.ID, Name: "round",
		Status: model.InstanceStatusRunning,
	}).Error)

	// 第 1 拍：假死 → 写入巡检原因并标记来源。
	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{
		InstanceUuid: "i-round",
		State:        string(model.InstanceStatusRunning),
		Health:       "dead",
		StatusReason: "假死：进程在但 tcp 响应探测连续失败 3 次",
	}})

	var mid model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-round").First(&mid).Error)
	require.Contains(t, mid.StatusReason, "假死", "假死原因应写入")
	require.Equal(t, "worker", mid.StatusReasonSource, "心跳写入的原因必须标来源 worker")

	// 第 2 拍：明确健康且无原因 → 清空。
	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{
		InstanceUuid: "i-round",
		State:        string(model.InstanceStatusRunning),
		Health:       "healthy",
	}})

	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-round").First(&got).Error)
	require.Empty(t, got.StatusReason, "心跳自写的巡检原因在恢复健康后仍必须被清空")
	require.Empty(t, got.StatusReasonSource)
}

// 对账落 STOPPED（证据确认已不在跑）时同样不得清空 CP 写入的失败原因；
// 且 status 必须真的落 STOPPED——原因清空附加来源条件后若与 status 挤在同一条 UPDATE，
// 行不匹配会让 status 卡在 RUNNING（与 syncInstanceStates 内同一个踩坑模式）。
func TestSyncInstanceStates_EvidenceNotRunningKeepsCPReason(t *testing.T) {
	h, db, node := newReconcileFixture(t)
	const cpReason = "Worker 操作失败: 节点未连接"
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-cp-stop", NodeID: node.ID, Name: "cp-stop",
		Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDaemon,
		Status: model.InstanceStatusRunning, StatusReason: cpReason,
		StartCommand: "java -jar server.jar",
	}).Error)
	h.SetEvidenceProbe(&fakeEvidenceProbe{running: map[string]bool{"i-cp-stop": false}})

	h.syncInstanceStates(node.UUID, nil)

	status, reason := statusOf(t, db, "i-cp-stop")
	require.Equal(t, model.InstanceStatusStopped, status, "证据确认不在跑仍须落 STOPPED")
	require.Equal(t, cpReason, reason, "对账落 STOPPED 不得清空 CP 写入的失败原因")
}

// 对账落 STOPPED 时，心跳自写的巡检原因（来源 worker）仍必须被清空（否则 STOPPED 实例挂着假死文案）。
func TestSyncInstanceStates_EvidenceNotRunningClearsWorkerReason(t *testing.T) {
	h, db, node := newReconcileFixture(t)
	require.NoError(t, db.Create(&model.Instance{
		UUID: "i-worker-stop", NodeID: node.ID, Name: "worker-stop",
		Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDaemon,
		Status: model.InstanceStatusRunning, StartCommand: "java -jar server.jar",
		StatusReason: "假死：进程在但 tcp 响应探测连续失败 3 次", StatusReasonSource: "worker",
	}).Error)
	h.SetEvidenceProbe(&fakeEvidenceProbe{running: map[string]bool{"i-worker-stop": false}})

	h.syncInstanceStates(node.UUID, nil)

	status, reason := statusOf(t, db, "i-worker-stop")
	require.Equal(t, model.InstanceStatusStopped, status)
	require.Empty(t, reason, "落 STOPPED 应清掉心跳自写的巡检原因")
}

// 对账路径写下的自己的巡检文案（「心跳清单暂缺」）同属心跳侧巡检结论，来源须标 worker：
// 实例重新出现在心跳清单且健康时应被清掉——否则修复后（清空带来源条件）这句文案会永久残留在面板上。
func TestSyncInstanceStates_ReconcileReasonClearedAfterReappear(t *testing.T) {
	h, db, node := newReconcileFixture(t)
	seedRunningInstance(t, db, node.ID, "i-reappear", model.InstanceStatusRunning)
	h.SetEvidenceProbe(&fakeEvidenceProbe{running: map[string]bool{"i-reappear": true}})

	// 第 1 拍：心跳清单缺失但证据显示在跑 → 标对账文案。
	h.syncInstanceStates(node.UUID, nil)
	_, reason := statusOf(t, db, "i-reappear")
	require.Contains(t, reason, "仍在运行")

	var mid model.Instance
	require.NoError(t, db.Where("uuid = ?", "i-reappear").First(&mid).Error)
	require.Equal(t, "worker", mid.StatusReasonSource, "对账巡检文案的来源须标 worker")

	// 第 2 拍：实例回到心跳清单且健康 → 对账文案应被清空。
	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{
		InstanceUuid: "i-reappear",
		State:        string(model.InstanceStatusRunning),
		Health:       "healthy",
	}})

	_, reason = statusOf(t, db, "i-reappear")
	require.Empty(t, reason, "实例回归心跳清单并健康后不得残留过期对账文案")
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
