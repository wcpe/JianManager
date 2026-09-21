package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeEvidenceProbe 可编程的进程侧证据客户端。
type fakeEvidenceProbe struct {
	running map[string]bool
	err     error
	calls   int
}

func (f *fakeEvidenceProbe) ProbeInstanceEvidence(_ context.Context, _ string, _ []string) (map[string]bool, error) {
	f.calls++
	return f.running, f.err
}

func newReconcileFixture(t *testing.T) (*ControlPlaneHandler, *gorm.DB, *model.Node) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	node := &model.Node{UUID: "node-rec-test", Name: "n", Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	h := NewControlPlaneHandler(db, NewClientPool())
	return h, db, node
}

func seedRunningInstance(t *testing.T, db *gorm.DB, nodeID uint, uuid string, status model.InstanceStatus) {
	t.Helper()
	require.NoError(t, db.Create(&model.Instance{
		UUID:         uuid,
		NodeID:       nodeID,
		Name:         uuid,
		Type:         model.InstanceTypeMinecraftJava,
		ProcessType:  model.ProcessTypeDaemon,
		Status:       status,
		StartCommand: "java -jar server.jar",
	}).Error)
}

func statusOf(t *testing.T, db *gorm.DB, uuid string) (model.InstanceStatus, string) {
	t.Helper()
	var got model.Instance
	require.NoError(t, db.Where("uuid = ?", uuid).First(&got).Error)
	return got.Status, got.StatusReason
}

// TestSyncInstanceStates_EvidenceRunningKeepsState 心跳清单缺失但证据显示在跑 → 不翻 STOPPED（FR-455③）。
func TestSyncInstanceStates_EvidenceRunningKeepsState(t *testing.T) {
	h, db, node := newReconcileFixture(t)
	seedRunningInstance(t, db, node.ID, "i-running", model.InstanceStatusRunning)
	h.SetEvidenceProbe(&fakeEvidenceProbe{running: map[string]bool{"i-running": true}})

	h.syncInstanceStates(node.UUID, nil) // Worker 未上报任何实例

	status, reason := statusOf(t, db, "i-running")
	require.Equal(t, model.InstanceStatusRunning, status, "证据显示在跑则保持当前态")
	require.Contains(t, reason, "仍在运行")
}

// TestSyncInstanceStates_EvidenceNotRunningStops 证据确认已不在跑 → 落 STOPPED。
func TestSyncInstanceStates_EvidenceNotRunningStops(t *testing.T) {
	h, db, node := newReconcileFixture(t)
	seedRunningInstance(t, db, node.ID, "i-stopped", model.InstanceStatusRunning)
	h.SetEvidenceProbe(&fakeEvidenceProbe{running: map[string]bool{"i-stopped": false}})

	h.syncInstanceStates(node.UUID, nil)

	status, _ := statusOf(t, db, "i-stopped")
	require.Equal(t, model.InstanceStatusStopped, status)
}

// TestSyncInstanceStates_EvidenceErrorGrace 证据拉取失败时宽限：连续 N 拍才落 STOPPED。
func TestSyncInstanceStates_EvidenceErrorGrace(t *testing.T) {
	h, db, node := newReconcileFixture(t)
	seedRunningInstance(t, db, node.ID, "i-grace", model.InstanceStatusRunning)
	h.SetEvidenceProbe(&fakeEvidenceProbe{err: errors.New("worker offline")})

	// 前 N-1 拍保持当前态（仅标 statusReason）。
	for i := 1; i < evidenceReconcileGraceBeats; i++ {
		h.syncInstanceStates(node.UUID, nil)
		status, reason := statusOf(t, db, "i-grace")
		require.Equal(t, model.InstanceStatusRunning, status, "宽限期内不得落 STOPPED（第 %d 拍）", i)
		require.Contains(t, reason, "宽限对账中")
	}
	// 第 N 拍落 STOPPED。
	h.syncInstanceStates(node.UUID, nil)
	status, _ := statusOf(t, db, "i-grace")
	require.Equal(t, model.InstanceStatusStopped, status, "连续 N 拍不可得才落 STOPPED")
}

// TestSyncInstanceStates_EvidenceErrorThenRunningResets 宽限期间证据恢复 → 保持态且清宽限计数。
func TestSyncInstanceStates_EvidenceErrorThenRunningResets(t *testing.T) {
	h, db, node := newReconcileFixture(t)
	seedRunningInstance(t, db, node.ID, "i-reset", model.InstanceStatusRunning)
	probe := &fakeEvidenceProbe{err: errors.New("flap")}
	h.SetEvidenceProbe(probe)

	h.syncInstanceStates(node.UUID, nil) // 记一拍宽限
	probe.err = nil
	probe.running = map[string]bool{"i-reset": true}
	h.syncInstanceStates(node.UUID, nil) // 证据恢复 → 清宽限

	status, reason := statusOf(t, db, "i-reset")
	require.Equal(t, model.InstanceStatusRunning, status)
	require.Contains(t, reason, "仍在运行")

	// 再做一次证据失败：宽限计数应从 1 重新计（未累积到阈值即不落 STOPPED）。
	probe.err = errors.New("flap2")
	h.syncInstanceStates(node.UUID, nil)
	status, _ = statusOf(t, db, "i-reset")
	require.Equal(t, model.InstanceStatusRunning, status, "清计数后重新计拍，不应立即落 STOPPED")
}

// TestSyncInstanceStates_NoEvidenceClientKeepsLegacyBehavior 未注入证据客户端 → 保持旧行为（直接 STOPPED）。
func TestSyncInstanceStates_NoEvidenceClientKeepsLegacyBehavior(t *testing.T) {
	h, db, node := newReconcileFixture(t)
	seedRunningInstance(t, db, node.ID, "i-legacy", model.InstanceStatusRunning)

	h.syncInstanceStates(node.UUID, nil)

	status, _ := statusOf(t, db, "i-legacy")
	require.Equal(t, model.InstanceStatusStopped, status, "未装配证据客户端时退化为旧行为")
}

// TestSyncInstanceStates_ReportedInstanceUntouched 本次清单上报的实例不进入证据对账路径：
// 若进入，证据为 false 会把它落 STOPPED，但上报态为 STARTING 才是权威（避免误判）。
func TestSyncInstanceStates_ReportedInstanceUntouched(t *testing.T) {
	h, db, node := newReconcileFixture(t)
	seedRunningInstance(t, db, node.ID, "i-reported", model.InstanceStatusRunning)
	probe := &fakeEvidenceProbe{running: map[string]bool{"i-reported": false}}
	h.SetEvidenceProbe(probe)

	h.syncInstanceStates(node.UUID, []*workerpb.InstanceState{{InstanceUuid: "i-reported", State: "STARTING"}})

	require.Zero(t, probe.calls, "上报的实例不应触发证据拉取")
	status, _ := statusOf(t, db, "i-reported")
	require.Equal(t, model.InstanceStatus("STARTING"), status, "以上报态为准")
}
