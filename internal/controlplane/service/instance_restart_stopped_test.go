package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 本文件钉死「重启一个没有活进程的实例」的语义。
//
// 缺陷现场：Restart 无条件先 transition(STOPPING)，而 STOPPED / CRASHED 的合法转换里没有
// STOPPING（只有 STARTING），于是 POST /instances/:id/restart 在已停止/已崩溃实例上返回
// 422 INVALID_TRANSITION「重启-停止: 当前状态 STOPPED 无法转换到 STOPPING」。
// 语义上这两个状态没有进程可停，重启等价于启动：跳过停止阶段，直接走 STARTING + start 委托。

// gatedRestartWorker 记录 start/restart 的调用次数，并在测试放行前**阻塞 RPC 返回**，
// 使「状态已转 STARTING / STOPPING、但异步委托尚未回写终态」的中间态可以被稳定断言。
// 放行前状态下断言才有意义：不阻塞时假 Worker 秒回，回写可能先于断言完成。
type gatedRestartWorker struct {
	workerpb.WorkerServiceClient

	gate     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	starts   int
	restarts int
}

func newGatedRestartWorker() *gatedRestartWorker {
	return &gatedRestartWorker{gate: make(chan struct{})}
}

// release 放行被阻塞的 RPC；可重复调用（内部 sync.Once）。
func (w *gatedRestartWorker) release() { w.once.Do(func() { close(w.gate) }) }

func (w *gatedRestartWorker) counts() (starts, restarts int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.starts, w.restarts
}

func (w *gatedRestartWorker) PreflightStartInstance(context.Context, *workerpb.InstanceActionRequest, ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

func (w *gatedRestartWorker) CreateInstance(context.Context, *workerpb.CreateInstanceRequest, ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (w *gatedRestartWorker) StartInstance(context.Context, *workerpb.InstanceActionRequest, ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	w.mu.Lock()
	w.starts++
	w.mu.Unlock()
	<-w.gate
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

func (w *gatedRestartWorker) RestartInstance(context.Context, *workerpb.InstanceActionRequest, ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	w.mu.Lock()
	w.restarts++
	w.mu.Unlock()
	<-w.gate
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// newRestartBranchFixture 建「在线节点 + 指定状态实例 + 可放行假 Worker」现场。
func newRestartBranchFixture(t *testing.T, status model.InstanceStatus) (*InstanceService, *gatedRestartWorker, *model.Instance) {
	t.Helper()
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool()
	worker := newGatedRestartWorker()
	pool.SetWorkerClientForTest(node.UUID, worker)

	svc := NewInstanceService(db, NewGroupService(db), pool)
	inst := &model.Instance{
		NodeID: node.ID, Name: "restart-branch-" + string(status),
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDocker, StartCommand: "./beacon",
		Status: status,
	}
	require.NoError(t, db.Create(inst).Error)

	// 兜底：用例提前失败时也必须放行 + join 在途委托，避免 goroutine 在 DB 关闭后写库。
	t.Cleanup(func() {
		worker.release()
		svc.Shutdown()
	})
	return svc, worker, inst
}

// TestRestart_NoProcessRunning_BehavesLikeStart 已停止 / 已崩溃实例的重启等价于启动：
// 不得再报「重启-停止: 当前状态 X 无法转换到 STOPPING」，须进 STARTING 并委托 StartInstance
// （Worker 侧没有进程可重启，下发 RestartInstance 只会得到「未运行」类失败）。
func TestRestart_NoProcessRunning_BehavesLikeStart(t *testing.T) {
	for _, status := range []model.InstanceStatus{model.InstanceStatusStopped, model.InstanceStatusCrashed} {
		t.Run(string(status), func(t *testing.T) {
			svc, worker, inst := newRestartBranchFixture(t, status)
			// 崩溃原因/上次失败原因必须在正常状态推进时被清空（与 Start 同口径）。
			require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
				Updates(map[string]any{"status_reason": "上次异常退出"}).Error)

			require.NoError(t, svc.Restart(inst.ID), "重启已停止/已崩溃实例必须成功，不得被「重启-停止」状态转换拦下")

			got, err := svc.GetByID(inst.ID)
			require.NoError(t, err)
			require.Equal(t, model.InstanceStatusStarting, got.Status, "必须与启动同栈：先进 STARTING")
			require.Empty(t, got.StatusReason, "正常状态推进须清空 statusReason")

			// 委托动作必须是 start：没有活进程可重启。
			require.Eventually(t, func() bool {
				starts, restarts := worker.counts()
				return starts == 1 && restarts == 0
			}, 3*time.Second, 10*time.Millisecond, "必须委托 StartInstance，且不得下发 RestartInstance")

			worker.release()
			svc.Shutdown()

			got, err = svc.GetByID(inst.ID)
			require.NoError(t, err)
			require.Equal(t, model.InstanceStatusRunning, got.Status, "启动委托成功后与 Start 一样收敛到 RUNNING")
			starts, restarts := worker.counts()
			require.Equal(t, 1, starts)
			require.Zero(t, restarts)
		})
	}
}

// TestRestart_FromRunning_StopsThenStarts 运行中实例仍走「先停后启」：进 STOPPING 并委托
// RestartInstance（Worker 侧需要优雅停止 + 等上一代退出 + 启动）。
func TestRestart_FromRunning_StopsThenStarts(t *testing.T) {
	svc, worker, inst := newRestartBranchFixture(t, model.InstanceStatusRunning)

	require.NoError(t, svc.Restart(inst.ID))

	got, err := svc.GetByID(inst.ID)
	require.NoError(t, err)
	require.Equal(t, model.InstanceStatusStopping, got.Status, "运行中实例的重启仍以「重启-停止」开始")

	require.Eventually(t, func() bool {
		starts, restarts := worker.counts()
		return starts == 0 && restarts == 1
	}, 3*time.Second, 10*time.Millisecond, "运行中实例必须委托 RestartInstance")

	worker.release()
	svc.Shutdown()

	got, err = svc.GetByID(inst.ID)
	require.NoError(t, err)
	require.Equal(t, model.InstanceStatusRunning, got.Status)
}

// TestValidTransitions_NoStoppedOrCrashedToStopping 修法必须是「Restart 内按状态选分支」，
// 不得靠在允许表里放行 STOPPED/CRASHED → STOPPING：那会让前端与其它调用方误以为
// 「没有进程的实例可以走停止路径」，并把一次无对象的停止委托下发给 Worker。
func TestValidTransitions_NoStoppedOrCrashedToStopping(t *testing.T) {
	for _, status := range []model.InstanceStatus{model.InstanceStatusStopped, model.InstanceStatusCrashed} {
		require.NotContains(t, validTransitions[status], model.InstanceStatusStopping,
			"%s 不应被允许转到 STOPPING", status)
		require.Contains(t, validTransitions[status], model.InstanceStatusStarting,
			"%s 必须可以转到 STARTING（重启走启动路径的前提）", status)
	}
}

// TestRestart_FromStopped_PreflightStillRejects 新分支不得绕过启动侧守卫：
// 预检失败仍返回 *PreflightError（HTTP 422 PREFLIGHT_FAILED），状态保持 STOPPED、不委托。
func TestRestart_FromStopped_PreflightStillRejects(t *testing.T) {
	svc, pool, node, inst := newPreflightFixture(t)
	pool.SetWorkerClientForTest(node.UUID, &fakeWorkerClient{
		preflightResp: &workerpb.InstanceActionResponse{Success: false, Error: "实例未绑定 JDK 且 PATH 上无可用 java"},
	})

	err := svc.Restart(inst.ID)
	var pfErr *PreflightError
	require.ErrorAs(t, err, &pfErr, "已停止实例的重启同样必须先过启动预检")
	require.Contains(t, pfErr.Reason, "未绑定 JDK")

	got, err := svc.GetByID(inst.ID)
	require.NoError(t, err)
	require.Equal(t, model.InstanceStatusStopped, got.Status, "预检失败状态不得进 STARTING")
	require.Contains(t, got.StatusReason, "未绑定 JDK")
}
