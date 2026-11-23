package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// captureLifecycleSlog 把默认 slog 重定向到内存缓冲，返回缓冲与恢复函数。
// 为什么要重定向：本用例要断言的可观测量正是「回写被跳过时有告警」，而 slog 无按消息计数的钩子。
func captureLifecycleSlog() (*bytes.Buffer, func()) {
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf, func() { slog.SetDefault(prev) }
}

// newLifecycleObservationInstance 建一条实例并返回「派发前快照」（与 delegateToWorker 同型的回读基准）。
func newLifecycleObservationInstance(t *testing.T, status model.InstanceStatus, reason string) (*gorm.DB, *model.Instance, *model.Instance) {
	t.Helper()
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "obs",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDocker,
		StartCommand: "x",
		Status:       status,
		StatusReason: reason,
	}
	require.NoError(t, db.Create(inst).Error)

	var observed model.Instance
	require.NoError(t, db.First(&observed, inst.ID).Error)
	return db, inst, &observed
}

// TestWriteLifecycleObservation_SurvivesHeartbeat 守护 CP-2：
// 回写不能拿 updated_at 做 CAS——心跳每拍都会推进 updated_at，而 start/restart 的委托链路
// （最长可达优雅停止超时 + 60s）几乎必然跨过一拍心跳。旧实现下 RowsAffected=0 且静默，
// 失败原因与终态都写不进去；本用例在旧实现（WHERE 含 updated_at）下必红。
func TestWriteLifecycleObservation_SurvivesHeartbeat(t *testing.T) {
	db, inst, observed := newLifecycleObservationInstance(t, model.InstanceStatusStarting, "")

	// 心跳介入：写入与库中**完全相同**的 status 值（handler.go ① 的同型写法）。
	time.Sleep(5 * time.Millisecond) // 保证 updated_at 可分辨（前提校验见下）
	require.NoError(t, db.Model(&model.Instance{}).Where("uuid = ?", inst.UUID).
		Update("status", model.InstanceStatusStarting).Error)

	var afterBeat model.Instance
	require.NoError(t, db.First(&afterBeat, inst.ID).Error)
	require.False(t, afterBeat.UpdatedAt.Equal(observed.UpdatedAt),
		"前提：心跳（即使 status 值未变）必须推进 updated_at，否则本用例测不到目标缺陷")

	// 迟到的 start 成功回写。
	writeLifecycleObservation(db, observed, model.InstanceStatusRunning, "")

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusRunning, got.Status, "心跳介入后生命周期回写仍须生效")
}

// TestWriteLifecycleObservation_WarnsWhenFactsChanged 守护 CP-2 的「不静默」：
// 派发前事实已变（此处状态被心跳推进为 RUNNING）时回写应放弃，但必须留下告警，
// 否则运维看不到「为什么这次回写没生效」。旧实现只查 Error，本用例在旧实现下必红（无告警）。
func TestWriteLifecycleObservation_WarnsWhenFactsChanged(t *testing.T) {
	buf, restore := captureLifecycleSlog()
	defer restore()

	db, inst, observed := newLifecycleObservationInstance(t, model.InstanceStatusStopping, "")

	// 心跳把状态推进为 RUNNING：CAS（status = 派发前状态）应失配。
	require.NoError(t, db.Model(&model.Instance{}).Where("uuid = ?", inst.UUID).
		Update("status", model.InstanceStatusRunning).Error)

	writeLifecycleObservation(db, observed, model.InstanceStatusStopped, "")

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusRunning, got.Status, "事实已变时不得覆盖新状态")
	require.Contains(t, buf.String(), "生命周期结果条件回写被跳过",
		"回写被跳过必须落告警，不得静默")
}

// TestReconcileLifecycleObservation_QueryFailureKeepsStatus 守护 CP-4：
// 结果查询失败时只留痕原因，**不得回写 status**——原样回写会把「什么都没查清」伪装成一次状态更新，
// 且 CP 写入的原因不会被心跳清掉，等于把实例钉在一条无信息量的兜底文案上。
//
// 判据用 SQLite 触发器「BEFORE UPDATE OF status」守门：只要 UPDATE 的 SET 列表里出现 status 列
// 就中止整条语句（与写入值是否等于原值无关），因此旧实现（回写 observed.Status）下本用例必红。
func TestReconcileLifecycleObservation_QueryFailureKeepsStatus(t *testing.T) {
	db, inst, observed := newLifecycleObservationInstance(t, model.InstanceStatusStopping, "此前的具体原因")
	require.NoError(t, db.Exec(
		`CREATE TRIGGER guard_status BEFORE UPDATE OF status ON instances
		 BEGIN SELECT RAISE(ABORT, 'status 列被改写'); END;`).Error)

	// 状态查询失败（节点不可达的等价形态）。
	reconcileLifecycleObservation(db, &failingStatusWorker{}, observed, "rpc 超时")

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopping, got.Status, "查询失败时不得改写状态（交由心跳收敛）")
	require.Contains(t, got.StatusReason, "操作结果待确认", "原因仍须留痕，不静默")
	require.Empty(t, got.StatusReasonSource, "CP 写入的原因来源为空串，心跳不会清它")
}

// TestReconcileLifecycleObservation_KnownStateWritesBack 守护既有语义：
// 查到明确状态时仍回写该状态（面板不必等下一拍心跳）。
func TestReconcileLifecycleObservation_KnownStateWritesBack(t *testing.T) {
	db, inst, observed := newLifecycleObservationInstance(t, model.InstanceStatusStopping, "")

	reconcileLifecycleObservation(db, &stateStatusWorker{state: string(model.InstanceStatusStopped)}, observed, "Worker 操作结果未确认")

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopped, got.Status)
	require.Contains(t, got.StatusReason, "操作结果待确认")
}

// TestReconcileLifecycleObservation_UnknownStateKeepsStatus 守护 CP-4 的另一半：
// Worker 上报了不可识别的状态时同样不猜状态，只留痕原因。
func TestReconcileLifecycleObservation_UnknownStateKeepsStatus(t *testing.T) {
	db, inst, observed := newLifecycleObservationInstance(t, model.InstanceStatusStopping, "")

	reconcileLifecycleObservation(db, &stateStatusWorker{state: "WEIRD"}, observed, "Worker 操作结果未确认")

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopping, got.Status, "未知状态不得改写 CP 状态")
	require.Contains(t, got.StatusReason, "操作结果待确认")
}

// failingStatusWorker 是 GetInstanceStatus 恒失败的 Worker 客户端替身（其余方法走嵌入接口）。
type failingStatusWorker struct {
	workerpb.WorkerServiceClient
}

func (f *failingStatusWorker) GetInstanceStatus(context.Context, *workerpb.InstanceActionRequest, ...grpc.CallOption) (*workerpb.GetInstanceStatusResponse, error) {
	return nil, errors.New("rpc 超时")
}

// stateStatusWorker 上报指定状态的 Worker 客户端替身。
type stateStatusWorker struct {
	workerpb.WorkerServiceClient
	state string
}

func (f *stateStatusWorker) GetInstanceStatus(context.Context, *workerpb.InstanceActionRequest, ...grpc.CallOption) (*workerpb.GetInstanceStatusResponse, error) {
	return &workerpb.GetInstanceStatusResponse{State: f.state}, nil
}
