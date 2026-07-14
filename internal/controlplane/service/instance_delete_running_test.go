package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeStopDeleteWorker 覆盖 StopInstance + RemoveInstance 的伪 Worker 客户端（FR-310）。
// 记录调用次序与「StopInstance 时刻的 DB 状态」，用于断言删除编排全程走状态机合法转换。
type fakeStopDeleteWorker struct {
	workerpb.WorkerServiceClient

	mu        sync.Mutex
	callOrder []string

	stopResp *workerpb.InstanceActionResponse
	stopErr  error
	// statusAtStop 记录 StopInstance 被调用时 DB 里的实例状态（经 db/instID 闭包读取）。
	statusAtStop model.InstanceStatus
	db           *gorm.DB
	instID       uint

	// removeFailures 前 N 次 RemoveInstance 返回失败（模拟停止收敛窗口内工作目录文件锁）。
	removeFailures int
	removeErrMsg   string
	removeCalls    int
	stopCalls      int
}

func (f *fakeStopDeleteWorker) StopInstance(_ context.Context, _ *workerpb.InstanceActionRequest, _ ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	f.callOrder = append(f.callOrder, "stop")
	if f.db != nil && f.instID != 0 {
		var inst model.Instance
		if err := f.db.First(&inst, f.instID).Error; err == nil {
			f.statusAtStop = inst.Status
		}
	}
	if f.stopErr != nil {
		return nil, f.stopErr
	}
	if f.stopResp != nil {
		return f.stopResp, nil
	}
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

func (f *fakeStopDeleteWorker) RemoveInstance(_ context.Context, _ *workerpb.RemoveInstanceRequest, _ ...grpc.CallOption) (*workerpb.RemoveInstanceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls++
	f.callOrder = append(f.callOrder, "remove")
	if f.removeCalls <= f.removeFailures {
		msg := f.removeErrMsg
		if msg == "" {
			msg = "删除工作目录失败: 另一个程序正在使用此文件"
		}
		return &workerpb.RemoveInstanceResponse{Success: false, Error: msg}, nil
	}
	return &workerpb.RemoveInstanceResponse{Success: true}, nil
}

// newRunningDeleteEnv 建 db + 在线节点 + 指定状态实例的删除编排测试环境；
// 缩短停止收敛窗口的重试节奏，避免用例真等秒级睡眠。
func newRunningDeleteEnv(t *testing.T, status model.InstanceStatus) (*InstanceService, *cpgrpc.ClientPool, *model.Node, *model.Instance) {
	t.Helper()
	db := newCloneTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.NetworkMember{}))
	pool := cpgrpc.NewClientPool()
	svc := NewInstanceService(db, nil, pool)
	t.Cleanup(svc.Shutdown)

	origInterval, origMargin := deleteStopSettleInterval, deleteStopSettleMargin
	deleteStopSettleInterval, deleteStopSettleMargin = 5*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() {
		deleteStopSettleInterval, deleteStopSettleMargin = origInterval, origMargin
	})

	node := &model.Node{UUID: "del-run-node", Name: "del-run-node", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		NodeID: node.ID, Name: "paper-run", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar server.jar",
		Status: status, WorkDir: "var/servers/paper-run-1a2b3c4d",
	}
	require.NoError(t, db.Create(inst).Error)
	return svc, pool, node, inst
}

// RUNNING 实例删除：先经状态机（RUNNING→STOPPING）同步停止，停成后再清理并删记录（FR-310 主路径）。
func TestDeleteRunningInstanceStopsThenDeletes(t *testing.T) {
	svc, pool, node, inst := newRunningDeleteEnv(t, model.InstanceStatusRunning)
	worker := &fakeStopDeleteWorker{db: svc.db, instID: inst.ID}
	pool.SetWorkerClientForTest(node.UUID, worker)

	require.NoError(t, svc.Delete(inst.ID))

	require.Equal(t, 1, worker.stopCalls, "运行中实例删除必须先经 StopInstance 停止")
	require.Equal(t, []string{"stop", "remove"}, worker.callOrder, "必须先停后删")
	require.Equal(t, model.InstanceStatusStopping, worker.statusAtStop,
		"下发停止时 DB 状态必须已合法转换到 STOPPING（不得绕状态机）")
	_, err := svc.GetByID(inst.ID)
	require.ErrorIs(t, err, ErrInstanceNotFound, "停止成功后记录应删除")
}

// STARTING 实例删除同样先停再删（STARTING→STOPPING 是合法转换）。
func TestDeleteStartingInstanceStopsThenDeletes(t *testing.T) {
	svc, pool, node, inst := newRunningDeleteEnv(t, model.InstanceStatusStarting)
	worker := &fakeStopDeleteWorker{db: svc.db, instID: inst.ID}
	pool.SetWorkerClientForTest(node.UUID, worker)

	require.NoError(t, svc.Delete(inst.ID))
	require.Equal(t, 1, worker.stopCalls)
	require.Equal(t, model.InstanceStatusStopping, worker.statusAtStop)
	_, err := svc.GetByID(inst.ID)
	require.ErrorIs(t, err, ErrInstanceNotFound)
}

// 停止收敛窗口内清理失败（Windows 文件锁 = 进程还没退干净）要在窗口内重试而非报错终局
// ——对应「超时强杀树」落地后锁才释放的真机时序。
func TestDeleteRunningInstanceRetriesCleanupInSettleWindow(t *testing.T) {
	svc, pool, node, inst := newRunningDeleteEnv(t, model.InstanceStatusRunning)
	worker := &fakeStopDeleteWorker{db: svc.db, instID: inst.ID, removeFailures: 2}
	pool.SetWorkerClientForTest(node.UUID, worker)

	require.NoError(t, svc.Delete(inst.ID))
	require.Equal(t, 3, worker.removeCalls, "锁窗口内的清理失败应重试直至成功")
	_, err := svc.GetByID(inst.ID)
	require.ErrorIs(t, err, ErrInstanceNotFound)
}

// Worker 停止失败（业务失败）→ 不删记录，返回含原因的可操作错误（FR-310：绝不孤儿化进程）。
func TestDeleteRunningInstanceAbortsWhenStopFails(t *testing.T) {
	svc, pool, node, inst := newRunningDeleteEnv(t, model.InstanceStatusRunning)
	worker := &fakeStopDeleteWorker{
		db: svc.db, instID: inst.ID,
		stopResp: &workerpb.InstanceActionResponse{Success: false, Error: "wrapper 控制连接不可用"},
	}
	pool.SetWorkerClientForTest(node.UUID, worker)

	err := svc.Delete(inst.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "wrapper 控制连接不可用", "停止失败原因必须透传给用户")
	require.Equal(t, 0, worker.removeCalls, "停止失败不得继续清理")

	got, gerr := svc.GetByID(inst.ID)
	require.NoError(t, gerr, "停止失败时记录必须保留")
	require.Equal(t, inst.ID, got.ID)
}

// 停止 RPC 通信失败（节点在线但链路异常）同样中止删除、记录保留。
func TestDeleteRunningInstanceAbortsWhenStopRPCFails(t *testing.T) {
	svc, pool, node, inst := newRunningDeleteEnv(t, model.InstanceStatusRunning)
	worker := &fakeStopDeleteWorker{db: svc.db, instID: inst.ID, stopErr: errors.New("网络中断")}
	pool.SetWorkerClientForTest(node.UUID, worker)

	err := svc.Delete(inst.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "网络中断")
	_, gerr := svc.GetByID(inst.ID)
	require.NoError(t, gerr)
}

// 节点未连接 + DB 声称在运行 → 拒绝删除（否则复刻原始事故：记录没了、java 继续跑成孤儿）。
func TestDeleteRunningInstanceRefusedWhenNodeDisconnected(t *testing.T) {
	svc, _, _, inst := newRunningDeleteEnv(t, model.InstanceStatusRunning)

	err := svc.Delete(inst.ID)
	require.ErrorIs(t, err, ErrInstanceRunning)
	require.Contains(t, err.Error(), "未连接", "错误必须指出节点未连接这一可操作原因")

	got, gerr := svc.GetByID(inst.ID)
	require.NoError(t, gerr, "无法核实进程处置时记录必须保留")
	require.Equal(t, inst.ID, got.ID)
}

// STOPPING 在途实例删除：Worker 报「未运行」视作现实已停止（DB 状态滞后），照常删除。
func TestDeleteStoppingInstanceTreatsNotRunningAsStopped(t *testing.T) {
	svc, pool, node, inst := newRunningDeleteEnv(t, model.InstanceStatusStopping)
	worker := &fakeStopDeleteWorker{
		db: svc.db, instID: inst.ID,
		stopResp: &workerpb.InstanceActionResponse{Success: false, Error: fmt.Sprintf("实例 %s 未运行", inst.UUID)},
	}
	pool.SetWorkerClientForTest(node.UUID, worker)

	require.NoError(t, svc.Delete(inst.ID))
	require.Equal(t, model.InstanceStatusStopping, worker.statusAtStop, "STOPPING 在途不得重复状态转换")
	_, err := svc.GetByID(inst.ID)
	require.ErrorIs(t, err, ErrInstanceNotFound)
}

// STOPPED 实例照常直删：不发 StopInstance，行为与 FR-310 之前完全一致。
func TestDeleteStoppedInstanceSkipsStop(t *testing.T) {
	svc, pool, node, inst := newRunningDeleteEnv(t, model.InstanceStatusStopped)
	worker := &fakeStopDeleteWorker{db: svc.db, instID: inst.ID}
	pool.SetWorkerClientForTest(node.UUID, worker)

	require.NoError(t, svc.Delete(inst.ID))
	require.Equal(t, 0, worker.stopCalls, "已停止实例不得下发 StopInstance")
	require.Equal(t, 1, worker.removeCalls)
	_, err := svc.GetByID(inst.ID)
	require.ErrorIs(t, err, ErrInstanceNotFound)
}

// STOPPED 实例清理一次性失败仍立即报错（收敛窗口重试仅限「刚停过」的删除，不改既有直删语义）。
func TestDeleteStoppedInstanceCleanupFailureNoRetry(t *testing.T) {
	svc, pool, node, inst := newRunningDeleteEnv(t, model.InstanceStatusStopped)
	worker := &fakeStopDeleteWorker{db: svc.db, instID: inst.ID, removeFailures: 99, removeErrMsg: "磁盘只读"}
	pool.SetWorkerClientForTest(node.UUID, worker)

	err := svc.Delete(inst.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "磁盘只读")
	require.Equal(t, 1, worker.removeCalls, "非停止收敛窗口不得重试清理")
}
