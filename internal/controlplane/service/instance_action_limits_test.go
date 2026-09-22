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

// limitCaptureWorker 记录启动/重启请求里携带的生效限额（N-3）。
//
// 同步约定（R1）：委托经 spawnDelegate 在**独立 goroutine** 内执行，StartInstance /
// RestartInstance 的写入发生在那个 goroutine；而测试 goroutine 要在 Eventually 与断言里读
// 同样的四个字段。两侧都无同步即为真实 DATA RACE（整包 `-race` 门禁会判 FAIL），故：
//   - 写侧（StartInstance / RestartInstance）在 mu 保护下更新；
//   - 读侧**一律经下面的 getter**取快照，不得直接访问字段——只给写侧加锁仍会竞态。
type limitCaptureWorker struct {
	workerpb.WorkerServiceClient
	// createErr 模拟「启动前幂等重注册失败」——N-3 的缺陷现场正是这条链路断了。
	// 仅在构造时设定、之后只读，无需同步。
	createErr error

	mu           sync.Mutex
	startReq     *workerpb.InstanceActionRequest
	restartReq   *workerpb.InstanceActionRequest
	startCalls   int
	restartCalls int
}

// startCallsCount 加锁读启动调用次数。
func (w *limitCaptureWorker) startCallsCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.startCalls
}

// restartCallsCount 加锁读重启调用次数。
func (w *limitCaptureWorker) restartCallsCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.restartCalls
}

// startRequest 加锁取最近一次启动请求的快照（未发生时返回 nil）。
func (w *limitCaptureWorker) startRequest() *workerpb.InstanceActionRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.startReq
}

// restartRequest 加锁取最近一次重启请求的快照（未发生时返回 nil）。
func (w *limitCaptureWorker) restartRequest() *workerpb.InstanceActionRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.restartReq
}

func (w *limitCaptureWorker) CreateInstance(_ context.Context, _ *workerpb.CreateInstanceRequest, _ ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	if w.createErr != nil {
		return &workerpb.CreateInstanceResponse{Success: false, Error: w.createErr.Error()}, w.createErr
	}
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (w *limitCaptureWorker) PreflightStartInstance(context.Context, *workerpb.InstanceActionRequest, ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

func (w *limitCaptureWorker) StartInstance(_ context.Context, in *workerpb.InstanceActionRequest, _ ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	w.mu.Lock()
	w.startCalls++
	w.startReq = in
	w.mu.Unlock()
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

func (w *limitCaptureWorker) RestartInstance(_ context.Context, in *workerpb.InstanceActionRequest, _ ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	w.mu.Lock()
	w.restartCalls++
	w.restartReq = in
	w.mu.Unlock()
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// TestStartRequestCarriesEffectiveLimits N-3：启动请求必须**自带**生效限额。
//
// 缺陷现场：effectiveCPULimit/effectiveMemLimitMB 的唯一消费点是 registerOnWorkerLocked →
// CreateInstance。而 Start 与 delegateToWorker 各自还会**再调一次**重注册，那条调用对失败
// 只记 Debug 日志且不阻断（见 delegateToWorker 的 "start" 分支）；Restart 的同类调用失败时
// 更是直接取消重启。把限额随动作请求一并下发，使收紧不再依赖「重注册一定成功」。
func TestStartRequestCarriesEffectiveLimits(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool()
	worker := &limitCaptureWorker{}
	pool.SetWorkerClientForTest(node.UUID, worker)

	svc := NewInstanceService(db, NewGroupService(db), pool)
	inst := &model.Instance{
		NodeID: node.ID, Name: "n3-start", Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDocker, StartCommand: "./beacon",
		Status: model.InstanceStatusStopped,
		// 运维配置 2 核 / 2048MiB，巡检收紧登记为 1.8 核 / 1843MiB（限额的 90%）。
		CPULimit: 2, MemLimitMB: 2048, ThrottleCPULimit: 1.8, ThrottleMemLimitMB: 1843,
	}
	require.NoError(t, db.Create(inst).Error)

	require.NoError(t, svc.Start(inst.ID))
	// 委托是异步的：等它落地（Shutdown 会 join 在途委托）。
	require.Eventually(t, func() bool { return worker.startCallsCount() > 0 }, 3*time.Second, 10*time.Millisecond)
	svc.Shutdown()

	req := worker.startRequest()
	require.NotNil(t, req)
	require.True(t, req.WithLimits, "必须显式声明携带限额（0 也是有效值）")
	require.Equal(t, 1.8, req.CpuLimit, "启动请求必须带**收紧后**的 CPU 限额")
	require.EqualValues(t, 1843, req.MemLimitMb, "启动请求必须带**收紧后**的内存限额")
}

// TestDelegateStartAppliesLimitsFromRequest N-3 核心回归：**重注册失败也不影响收紧**。
//
// 缺陷现场的核心是「收紧值只随重注册下发」。本用例让重注册（CreateInstance）失败、
// 启动 RPC 成功，断言启动请求仍带收紧值——即收紧的送达不再挂在重注册成功上。
func TestDelegateStartAppliesLimitsFromRequest(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool()
	worker := &limitCaptureWorker{createErr: context.DeadlineExceeded}
	pool.SetWorkerClientForTest(node.UUID, worker)

	svc := NewInstanceService(db, NewGroupService(db), pool)
	inst := &model.Instance{
		NodeID: node.ID, Name: "n3-regfail", Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDocker, StartCommand: "./beacon",
		Status:   model.InstanceStatusStopped,
		CPULimit: 8, MemLimitMB: 8192, ThrottleCPULimit: 7.2, ThrottleMemLimitMB: 7372,
	}
	require.NoError(t, db.Create(inst).Error)

	// 直接走委托入口（Start 会先被 preflight 的重注册失败拦截，那是另一条独立守卫）。
	svc.delegateToWorker(inst, "start")

	req := worker.startRequest()
	require.NotNil(t, req, "重注册失败后必须仍然发起启动（旧行为：仅 Debug 日志，不阻断）")
	require.True(t, req.WithLimits)
	require.Equal(t, 7.2, req.CpuLimit, "重注册失败时收紧值仍必须随启动请求送达")
	require.EqualValues(t, 7372, req.MemLimitMb)
}

// TestStartRequestCarriesLimitsWhenThrottleCleared 收紧解除（改回 0）也必须送达：
// 0 在 cgroup 语义里是「不限制」这一有效值，不能用「非 0 才下发」的写法把它吞掉。
func TestStartRequestCarriesLimitsWhenThrottleCleared(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool()
	worker := &limitCaptureWorker{}
	pool.SetWorkerClientForTest(node.UUID, worker)

	svc := NewInstanceService(db, NewGroupService(db), pool)
	inst := &model.Instance{
		NodeID: node.ID, Name: "n3-clear", Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDocker, StartCommand: "./beacon",
		Status: model.InstanceStatusStopped,
		// 无任何限额 → 生效值 0（不限制），必须作为「显式解除」送达。
	}
	require.NoError(t, db.Create(inst).Error)

	require.NoError(t, svc.Start(inst.ID))
	require.Eventually(t, func() bool { return worker.startCallsCount() > 0 }, 3*time.Second, 10*time.Millisecond)
	svc.Shutdown()

	req := worker.startRequest()
	require.NotNil(t, req)
	require.True(t, req.WithLimits, "无限额时仍须声明「本次携带限额=0」")
	require.Zero(t, req.CpuLimit)
	require.Zero(t, req.MemLimitMb)
}

// TestRestartRequestCarriesEffectiveLimits 重启路径同样携带（重启是收紧落地的主要时机：
// cgroup 限额只能在容器重建时注入）。
func TestRestartRequestCarriesEffectiveLimits(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool()
	worker := &limitCaptureWorker{}
	pool.SetWorkerClientForTest(node.UUID, worker)

	svc := NewInstanceService(db, NewGroupService(db), pool)
	inst := &model.Instance{
		NodeID: node.ID, Name: "n3-restart", Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDocker, StartCommand: "./beacon",
		// RUNNING 才能进「重启-停止」转换（STOPPED 的合法转换里没有 STOPPING）。
		Status:   model.InstanceStatusRunning,
		CPULimit: 4, MemLimitMB: 4096,
		ThrottleCPULimit: 3.6, ThrottleMemLimitMB: 3686,
	}
	require.NoError(t, db.Create(inst).Error)

	require.NoError(t, svc.Restart(inst.ID))
	require.Eventually(t, func() bool { return worker.restartCallsCount() > 0 }, 3*time.Second, 10*time.Millisecond)
	svc.Shutdown()

	req := worker.restartRequest()
	require.NotNil(t, req)
	require.True(t, req.WithLimits)
	require.Equal(t, 3.6, req.CpuLimit)
	require.EqualValues(t, 3686, req.MemLimitMb)
}

// TestBatchStartRequestCarriesEffectiveLimits 批量启动不得绕过（同一生效链）。
func TestBatchStartRequestCarriesEffectiveLimits(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool()
	worker := &limitCaptureWorker{}
	pool.SetWorkerClientForTest(node.UUID, worker)

	instSvc := NewInstanceService(db, NewGroupService(db), pool)
	inst := &model.Instance{
		NodeID: node.ID, Name: "n3-batch", Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDocker, StartCommand: "./beacon",
		Status:   model.InstanceStatusStopped,
		CPULimit: 2, MemLimitMB: 2048,
		ThrottleCPULimit: 1.8, ThrottleMemLimitMB: 1843,
	}
	require.NoError(t, db.Create(inst).Error)

	batch := NewInstanceBatchService(db, pool)
	batch.SetInstanceService(instSvc)
	result, err := batch.Batch(InstanceBatchRequest{Action: InstanceBatchStart, IDs: []uint{inst.ID}}, nil, false)
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Succeeded)
	require.EqualValues(t, 0, result.Failed)

	req := worker.startRequest()
	require.NotNil(t, req)
	require.True(t, req.WithLimits)
	require.Equal(t, 1.8, req.CpuLimit)
	require.EqualValues(t, 1843, req.MemLimitMb)
}
