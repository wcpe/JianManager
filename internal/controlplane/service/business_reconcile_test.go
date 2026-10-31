package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeReconcilePluginWorker 是伪 Worker 客户端：按实例 UUID 决定 SendPluginCommand 的回执。
//
//   - outputByUUID 命中 → success=true，Output 为该原样字符串（用于验证「JM 不解析」）；
//   - failByUUID 命中   → success=false，Error 为该值；
//   - delayByUUID 命中  → 先睡指定时长（模拟慢实例，超时测试用）；
//   - 都不命中          → success=true + `{"ok":true}`。
type fakeReconcilePluginWorker struct {
	workerpb.WorkerServiceClient

	mu           sync.Mutex
	calls        []*workerpb.SendPluginCommandRequest
	outputByUUID map[string]string
	failByUUID   map[string]string
	delayByUUID  map[string]time.Duration
}

func (f *fakeReconcilePluginWorker) SendPluginCommand(
	ctx context.Context, req *workerpb.SendPluginCommandRequest, _ ...grpc.CallOption,
) (*workerpb.SendPluginCommandResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	output, hasOutput := f.outputByUUID[req.InstanceUuid]
	failMsg, hasFail := f.failByUUID[req.InstanceUuid]
	delay := f.delayByUUID[req.InstanceUuid]
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if hasFail {
		return &workerpb.SendPluginCommandResponse{Success: false, Error: failMsg}, nil
	}
	if hasOutput {
		return &workerpb.SendPluginCommandResponse{Success: true, Output: output}, nil
	}
	return &workerpb.SendPluginCommandResponse{Success: true, Output: `{"ok":true}`}, nil
}

func (f *fakeReconcilePluginWorker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// callsFor 返回针对某实例 UUID 的下发次数。
func (f *fakeReconcilePluginWorker) callsFor(uuid string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.InstanceUuid == uuid {
			n++
		}
	}
	return n
}

// reconcileFixture 是一次对账测试的装配结果。
type reconcileFixture struct {
	db     *gorm.DB
	svc    *BusinessReconcileService
	worker *fakeReconcilePluginWorker
	pool   *cpgrpc.ClientPool
	group  uint
	insts  []*model.Instance
}

// newReconcileFixture 装配「一个节点 + 一个分组 + n 个运行中实例」并注入伪 Worker。
func newReconcileFixture(t *testing.T, n int) *reconcileFixture {
	t.Helper()
	db := newBatchTestDB(t)
	node := &model.Node{Name: "n1", UUID: "node-uuid-1", GRPCPort: 9100, WSPort: 9102}
	require.NoError(t, db.Create(node).Error)
	g := mkGroup(t, db, "cluster", nil)

	insts := make([]*model.Instance, n)
	for i := 0; i < n; i++ {
		inst := mkBatchInstance(t, db, fmt.Sprintf("inst-%d", i), node.ID, model.InstanceStatusRunning)
		linkGroup(t, db, g.ID, inst.ID)
		insts[i] = inst
	}

	pool := cpgrpc.NewClientPool()
	worker := &fakeReconcilePluginWorker{}
	pool.SetWorkerClientForTest(node.UUID, worker)

	svc := NewBusinessReconcileService(db, NewBusinessService(db, pool))
	// 默认把 manifest 探针短路为「不可用」：否则每次对账都会额外拉一次清单，
	// 污染 worker 调用计数（那是用来断言「下发了几次」的）。需要验证只读校验的
	// 用例自行 SetManifestLookup 覆盖。
	svc.SetManifestLookup(func(uint) (*BusinessResult, error) {
		return &BusinessResult{Available: false}, nil
	})
	return &reconcileFixture{
		db: db, svc: svc, worker: worker, pool: pool, group: g.ID, insts: insts,
	}
}

// target 返回指向该分组的目标。
func (f *reconcileFixture) target() BusinessTarget {
	return BusinessTarget{GroupIDs: []uint{f.group}}
}

// request 构造一次标准对账请求。
func (f *reconcileFixture) request(requestID string) BusinessReconcileRequest {
	return BusinessReconcileRequest{
		Target:    f.target(),
		Domain:    "activity",
		Action:    "snapshot",
		RequestID: requestID,
		IssuerKey: "user:1",
	}
}

// TestBusinessReconcile_MultiInstance_ReturnsPerInstanceOutput
// 多实例对账：逐实例返回各自的快照，顺序与目标（ID 升序）一致。
func TestBusinessReconcile_MultiInstance_ReturnsPerInstanceOutput(t *testing.T) {
	f := newReconcileFixture(t, 3)
	f.worker.outputByUUID = map[string]string{
		f.insts[0].UUID: `{"activities":["spring-2026"]}`,
		f.insts[1].UUID: `{"activities":[]}`,
		f.insts[2].UUID: `{"activities":["summer"]}`,
	}

	resp, err := f.svc.Reconcile(context.Background(), f.request("req-1"), BusinessTargetScope{}, nil)
	require.NoError(t, err)
	require.Len(t, resp.Results, 3)
	assert.Equal(t, "req-1", resp.RequestID)

	for i, r := range resp.Results {
		assert.Equal(t, f.insts[i].ID, r.InstanceID, "结果顺序应与目标实例顺序一致")
		assert.True(t, r.Success)
		assert.Empty(t, r.Error)
		assert.JSONEq(t, f.worker.outputByUUID[f.insts[i].UUID], string(r.Output))
	}
	assert.Equal(t, 3, f.worker.callCount(), "三台实例各下发一次")
}

// TestBusinessReconcile_OutputPassThrough_NotParsed 断言 JM 完全不解包 output：
// 拓扑与字段都是插件私有的，只要 JSON 合法就必须逐字节等价地到调用方手里。
func TestBusinessReconcile_OutputPassThrough_NotParsed(t *testing.T) {
	f := newReconcileFixture(t, 1)
	// 刻意用 JM 完全不认识的域/字段结构，证明它没有做任何 schema 假设。
	raw := `{"z":1,"a":{"nested":[1,2,{"deep":null}]},"unknownDomainField":"x"}`
	f.worker.outputByUUID = map[string]string{f.insts[0].UUID: raw}

	resp, err := f.svc.Reconcile(context.Background(), f.request("req-passthrough"), BusinessTargetScope{}, nil)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)

	// 逐字节等价（含键序）：JSONEq 会忽略键序，这里用字符串全等才证明「没重新序列化」。
	assert.Equal(t, raw, string(resp.Results[0].Output), "output 必须原样透传，不得重新编码")
}

// TestBusinessReconcile_OfflineInstance_ReturnsExplicitError 离线实例给出明确错误，且不下发。
func TestBusinessReconcile_OfflineInstance_ReturnsExplicitError(t *testing.T) {
	f := newReconcileFixture(t, 2)
	// 把第二台设为停止态。
	require.NoError(t, f.svc.db.Model(&model.Instance{}).
		Where("id = ?", f.insts[1].ID).
		Update("status", model.InstanceStatusStopped).Error)

	resp, err := f.svc.Reconcile(context.Background(), f.request("req-offline"), BusinessTargetScope{}, nil)
	require.NoError(t, err, "离线实例不应变成整体失败")
	require.Len(t, resp.Results, 2)

	assert.True(t, resp.Results[0].Success)
	assert.False(t, resp.Results[1].Success)
	assert.Contains(t, resp.Results[1].Error, "INSTANCE_OFFLINE", "离线必须给出可机器识别的错误前缀")
	assert.Empty(t, resp.Results[1].Output)
	assert.Equal(t, 0, f.worker.callsFor(f.insts[1].UUID), "离线实例不应被下发")
}

// TestBusinessReconcile_ProbeOffline_DegradesToOfflineError 节点未连（探针不可达）按 offline 表达。
func TestBusinessReconcile_ProbeOffline_DegradesToOfflineError(t *testing.T) {
	f := newReconcileFixture(t, 1)
	// 换一个未连入连接池的节点 UUID：service 层的降级文案是「节点未连接」。
	orphan := &model.Node{Name: "n2", UUID: "node-uuid-orphan", GRPCPort: 9200, WSPort: 9202}
	require.NoError(t, f.svc.db.Create(orphan).Error)
	inst := mkBatchInstance(t, f.svc.db, "orphan-inst", orphan.ID, model.InstanceStatusRunning)

	resp, err := f.svc.Reconcile(context.Background(), BusinessReconcileRequest{
		Target:    BusinessTarget{InstanceIDs: []uint{inst.ID}},
		Domain:    "activity",
		Action:    "snapshot",
		RequestID: "req-probe-offline",
		IssuerKey: "user:1",
	}, BusinessTargetScope{}, nil)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.False(t, resp.Results[0].Success)
	assert.Contains(t, resp.Results[0].Error, "INSTANCE_OFFLINE")
}

// TestBusinessReconcile_WriteAction_Rejected 动作在 manifest 中声明为 WRITE → 整体拒绝（422 语义）。
func TestBusinessReconcile_WriteAction_Rejected(t *testing.T) {
	f := newReconcileFixture(t, 2)
	f.svc.SetManifestLookup(func(uint) (*BusinessResult, error) {
		return &BusinessResult{
			Available: true,
			Output:    json.RawMessage(`{"domains":{"activity":{"actions":[{"name":"grant","risk":"WRITE"}]}}}`),
		}, nil
	})

	_, err := f.svc.Reconcile(context.Background(), BusinessReconcileRequest{
		Target:    f.target(),
		Domain:    "activity",
		Action:    "grant",
		RequestID: "req-write",
		IssuerKey: "user:1",
	}, BusinessTargetScope{}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBusinessReconcileWriteAction), "应映射为写动作拒绝错误")

	code, status := BusinessReconcileErrorCode(err)
	assert.Equal(t, "ACTION_UNSUPPORTED", code)
	assert.Equal(t, 422, status)
	assert.Equal(t, 0, f.worker.callCount(), "被拒绝的请求不得下发任何实例")
}

// TestBusinessReconcile_DestructiveAction_Rejected DESTRUCTIVE 同样拒绝。
func TestBusinessReconcile_DestructiveAction_Rejected(t *testing.T) {
	f := newReconcileFixture(t, 1)
	f.svc.SetManifestLookup(func(uint) (*BusinessResult, error) {
		return &BusinessResult{
			Available: true,
			Output:    json.RawMessage(`{"domains":{"activity":{"actions":[{"name":"purge","risk":"DESTRUCTIVE"}]}}}`),
		}, nil
	})
	_, err := f.svc.Reconcile(context.Background(), BusinessReconcileRequest{
		Target:    f.target(),
		Domain:    "activity",
		Action:    "purge",
		RequestID: "req-destructive",
		IssuerKey: "user:1",
	}, BusinessTargetScope{}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBusinessReconcileWriteAction))
}

// TestBusinessReconcile_ReadAction_Allowed manifest 声明 READ 时放行。
func TestBusinessReconcile_ReadAction_Allowed(t *testing.T) {
	f := newReconcileFixture(t, 1)
	f.svc.SetManifestLookup(func(uint) (*BusinessResult, error) {
		return &BusinessResult{
			Available: true,
			Output:    json.RawMessage(`{"domains":{"activity":{"actions":[{"name":"snapshot","risk":"READ"}]}}}`),
		}, nil
	})
	resp, err := f.svc.Reconcile(context.Background(), f.request("req-read"), BusinessTargetScope{}, nil)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.True(t, resp.Results[0].Success)
}

// TestBusinessReconcile_ManifestUnavailable_DegradesToAllow
// manifest 拉不到（探针刚重连等）时放行：真正的裁决交给插件，逐实例错误更有信息量。
func TestBusinessReconcile_ManifestUnavailable_DegradesToAllow(t *testing.T) {
	f := newReconcileFixture(t, 1)
	f.svc.SetManifestLookup(func(uint) (*BusinessResult, error) {
		return &BusinessResult{Available: false, Error: "探针未连接"}, nil
	})
	resp, err := f.svc.Reconcile(context.Background(), f.request("req-manifest-down"), BusinessTargetScope{}, nil)
	require.NoError(t, err, "manifest 不可用不应拒绝只读请求")
	require.Len(t, resp.Results, 1)
	assert.True(t, resp.Results[0].Success)
}

// TestBusinessReconcile_ManifestError_DegradesToAllow manifest 调用硬失败时同样放行。
func TestBusinessReconcile_ManifestError_DegradesToAllow(t *testing.T) {
	f := newReconcileFixture(t, 1)
	f.svc.SetManifestLookup(func(uint) (*BusinessResult, error) {
		return nil, errors.New("gRPC 调用失败")
	})
	resp, err := f.svc.Reconcile(context.Background(), f.request("req-manifest-err"), BusinessTargetScope{}, nil)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.True(t, resp.Results[0].Success)
}

// TestBusinessReconcile_ActionNotDeclared_DegradesToAllow 动作未声明时放行（由插件最终裁决）。
func TestBusinessReconcile_ActionNotDeclared_DegradesToAllow(t *testing.T) {
	f := newReconcileFixture(t, 1)
	f.svc.SetManifestLookup(func(uint) (*BusinessResult, error) {
		return &BusinessResult{
			Available: true,
			Output:    json.RawMessage(`{"domains":{"economy":{"actions":[{"name":"balance","risk":"READ"}]}}}`),
		}, nil
	})
	resp, err := f.svc.Reconcile(context.Background(), f.request("req-undeclared"), BusinessTargetScope{}, nil)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.True(t, resp.Results[0].Success)
}

// TestBusinessReconcile_IdempotentReplay 同 requestId 重放返回首次结果，不重复下发。
func TestBusinessReconcile_IdempotentReplay(t *testing.T) {
	f := newReconcileFixture(t, 2)
	req := f.request("req-idem")

	first, err := f.svc.Reconcile(context.Background(), req, BusinessTargetScope{}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, f.worker.callCount())

	second, err := f.svc.Reconcile(context.Background(), req, BusinessTargetScope{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, f.worker.callCount(), "重放不得再次下发")
	assert.True(t, second.IdempotentReplay, "重放应被标记")
	assert.Equal(t, first.RequestID, second.RequestID)
	require.Len(t, second.Results, len(first.Results))
	for i := range first.Results {
		assert.Equal(t, first.Results[i].InstanceID, second.Results[i].InstanceID)
		assert.Equal(t, first.Results[i].Success, second.Results[i].Success)
		assert.Equal(t, string(first.Results[i].Output), string(second.Results[i].Output))
	}
}

// TestBusinessReconcile_IdempotencyKeyReused 同 requestId 不同 payload → 拒绝。
func TestBusinessReconcile_IdempotencyKeyReused(t *testing.T) {
	f := newReconcileFixture(t, 1)
	req := f.request("req-conflict")
	_, err := f.svc.Reconcile(context.Background(), req, BusinessTargetScope{}, nil)
	require.NoError(t, err)

	req.Payload = json.RawMessage(`{"activityId":"other"}`)
	_, err = f.svc.Reconcile(context.Background(), req, BusinessTargetScope{}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBusinessIdempotencyKeyReused))
	code, status := BusinessReconcileErrorCode(err)
	assert.Equal(t, "IDEMPOTENCY_KEY_REUSED", code)
	assert.Equal(t, 409, status)
}

// TestBusinessReconcile_InstanceTimeout_DoesNotBlockOthers
// 单实例超时不阻塞其他实例：慢实例记超时，其余照常拿到快照。
func TestBusinessReconcile_InstanceTimeout_DoesNotBlockOthers(t *testing.T) {
	f := newReconcileFixture(t, 3)
	f.svc.SetInstanceTimeout(80 * time.Millisecond)
	f.worker.delayByUUID = map[string]time.Duration{
		f.insts[1].UUID: 2 * time.Second, // 远超单实例超时
	}

	started := time.Now()
	resp, err := f.svc.Reconcile(context.Background(), f.request("req-timeout"), BusinessTargetScope{}, nil)
	elapsed := time.Since(started)
	require.NoError(t, err)
	require.Len(t, resp.Results, 3)

	assert.True(t, resp.Results[0].Success)
	assert.False(t, resp.Results[1].Success)
	assert.Contains(t, resp.Results[1].Error, "INSTANCE_TIMEOUT")
	assert.True(t, resp.Results[2].Success, "超时的实例不应阻塞其他实例")
	assert.Less(t, elapsed, time.Second, "整体耗时不应被慢实例拖到 2s")
}

// TestBusinessReconcile_TimeoutConfigurable 对账超时默认 60s，且可单独配置（独立于命令的 30s）。
func TestBusinessReconcile_TimeoutConfigurable(t *testing.T) {
	assert.Equal(t, 60*time.Second, BusinessReconcileInstanceTimeout, "对账默认超时应为 60s")
	assert.NotEqual(t, BusinessBatchInstanceTimeout, BusinessReconcileInstanceTimeout,
		"对账超时必须独立于批量命令的超时")

	f := newReconcileFixture(t, 0)
	f.svc.SetInstanceTimeout(1234 * time.Millisecond)
	assert.Equal(t, 1234*time.Millisecond, f.svc.instanceTimeout)
}

// TestBusinessReconcile_TargetEmpty 目标解析为空 → ErrBusinessTargetEmpty（400）。
func TestBusinessReconcile_TargetEmpty(t *testing.T) {
	f := newReconcileFixture(t, 0)
	_, err := f.svc.Reconcile(context.Background(), BusinessReconcileRequest{
		Target:    BusinessTarget{InstanceIDs: []uint{999999}},
		Domain:    "activity",
		Action:    "snapshot",
		RequestID: "req-empty",
		IssuerKey: "user:1",
	}, BusinessTargetScope{}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBusinessTargetEmpty))
	code, status := BusinessReconcileErrorCode(err)
	assert.Equal(t, "TARGET_EMPTY", code)
	assert.Equal(t, 400, status)
}

// TestBusinessReconcile_TargetUnspecified 完全未指定目标 → TARGET_EMPTY（400）。
func TestBusinessReconcile_TargetUnspecified(t *testing.T) {
	f := newReconcileFixture(t, 1)
	_, err := f.svc.Reconcile(context.Background(), BusinessReconcileRequest{
		Target:    BusinessTarget{},
		Domain:    "activity",
		Action:    "snapshot",
		RequestID: "req-unspecified",
		IssuerKey: "user:1",
	}, BusinessTargetScope{}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBusinessTargetUnspecified))
	code, status := BusinessReconcileErrorCode(err)
	assert.Equal(t, "TARGET_EMPTY", code)
	assert.Equal(t, 400, status)
}

// TestBusinessReconcile_MissingRequestID requestId 必填。
func TestBusinessReconcile_MissingRequestID(t *testing.T) {
	f := newReconcileFixture(t, 1)
	req := f.request("")
	_, err := f.svc.Reconcile(context.Background(), req, BusinessTargetScope{}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidBusinessCommand))
}

// TestBusinessReconcile_FailedPrecheck_DoesNotPoisonIdempotency
// 前置失败后撤销幂等占位：调用方修正参数后用同一 requestId 重试必须能真正执行。
func TestBusinessReconcile_FailedPrecheck_DoesNotPoisonIdempotency(t *testing.T) {
	f := newReconcileFixture(t, 1)
	f.svc.SetManifestLookup(func(uint) (*BusinessResult, error) {
		return &BusinessResult{
			Available: true,
			Output:    json.RawMessage(`{"domains":{"activity":{"actions":[{"name":"grant","risk":"WRITE"}]}}}`),
		}, nil
	})
	req := BusinessReconcileRequest{
		Target:    f.target(),
		Domain:    "activity",
		Action:    "grant",
		RequestID: "req-retry",
		IssuerKey: "user:1",
	}
	_, err := f.svc.Reconcile(context.Background(), req, BusinessTargetScope{}, nil)
	require.Error(t, err)
	assert.Equal(t, 0, f.svc.IdempotencyWindowSize(), "前置失败应撤销幂等占位")

	// 换成只读动作重试：必须真正执行，而不是拿回一个不存在的「首次结果」。
	req.Action = "snapshot"
	f.svc.SetManifestLookup(func(uint) (*BusinessResult, error) {
		return &BusinessResult{
			Available: true,
			Output:    json.RawMessage(`{"domains":{"activity":{"actions":[{"name":"snapshot","risk":"READ"}]}}}`),
		}, nil
	})
	resp, err := f.svc.Reconcile(context.Background(), req, BusinessTargetScope{}, nil)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.True(t, resp.Results[0].Success)
	assert.False(t, resp.IdempotentReplay)
	assert.Equal(t, 1, f.worker.callCount())
}

// TestBusinessReconcile_ProgressCallbackPerInstance 流式回调逐实例触发（不是按批）。
func TestBusinessReconcile_ProgressCallbackPerInstance(t *testing.T) {
	f := newReconcileFixture(t, 4)
	var mu sync.Mutex
	var seen []uint
	resp, err := f.svc.Reconcile(context.Background(), f.request("req-progress"), BusinessTargetScope{}, func(r BusinessReconcileResult) {
		mu.Lock()
		seen = append(seen, r.InstanceID)
		mu.Unlock()
	})
	require.NoError(t, err)
	require.Len(t, resp.Results, 4)
	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, seen, 4, "每完成一个实例就应回调一次")
}

// TestBusinessReconcile_ScopeConverged 越权目标不出现在结果里（跨组隔离）。
func TestBusinessReconcile_ScopeConverged(t *testing.T) {
	f := newReconcileFixture(t, 2)
	// 只允许访问第一台。
	resp, err := f.svc.Reconcile(context.Background(), f.request("req-scope"),
		BusinessTargetScope{IDs: []uint{f.insts[0].ID}, Scoped: true}, nil)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, f.insts[0].ID, resp.Results[0].InstanceID)
}

// TestBusinessReconcile_DifferentIssuer_SameRequestIDNotShared 不同调用方偶然同 requestId
// 不得拿到彼此的快照（跨租户信息泄漏）。
func TestBusinessReconcile_DifferentIssuer_SameRequestIDNotShared(t *testing.T) {
	f := newReconcileFixture(t, 1)
	reqA := f.request("req-shared")
	reqA.IssuerKey = "user:1"
	_, err := f.svc.Reconcile(context.Background(), reqA, BusinessTargetScope{}, nil)
	require.NoError(t, err)

	reqB := f.request("req-shared")
	reqB.IssuerKey = "user:2"
	respB, err := f.svc.Reconcile(context.Background(), reqB, BusinessTargetScope{}, nil)
	require.NoError(t, err)
	assert.False(t, respB.IdempotentReplay, "不同调用方的同 requestId 必须视为新请求")
	assert.Equal(t, 2, f.worker.callCount())
}

// TestBusinessReconcile_LookupActionRisk 清单解析的最小结构测试。
func TestBusinessReconcile_LookupActionRisk(t *testing.T) {
	cases := []struct {
		name      string
		output    string
		wantRisk  string
		wantFound bool
		wantErr   bool
	}{
		{name: "声明为 READ", output: `{"domains":{"a":{"actions":[{"name":"s","risk":"READ"}]}}}`, wantRisk: "READ", wantFound: true},
		{name: "声明为 WRITE", output: `{"domains":{"a":{"actions":[{"name":"s","risk":"WRITE"}]}}}`, wantRisk: "WRITE", wantFound: true},
		{name: "域不存在", output: `{"domains":{"b":{"actions":[{"name":"s","risk":"READ"}]}}}`, wantFound: false},
		{name: "动作不存在", output: `{"domains":{"a":{"actions":[{"name":"x","risk":"READ"}]}}}`, wantFound: false},
		{name: "空清单", output: `{}`, wantFound: false},
		{name: "空输出", output: ``, wantFound: false},
		{name: "非法 JSON", output: `{`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			risk, found, err := lookupActionRisk(json.RawMessage(tc.output), "a", "s")
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantFound, found)
			assert.Equal(t, tc.wantRisk, risk)
		})
	}
}

// TestBusinessReconcile_IdempotencyMemoryBounded 幂等记录条数有硬上限：
// 规格 §5 要求「不缓存快照」，而幂等重放要求保留上次结果，二者靠条数上限折中。
func TestBusinessReconcile_IdempotencyMemoryBounded(t *testing.T) {
	f := newReconcileFixture(t, 1)
	f.svc.SetIdempotencyMaxRecords(3)

	for i := 0; i < 10; i++ {
		req := f.request(fmt.Sprintf("req-bounded-%d", i))
		_, err := f.svc.Reconcile(context.Background(), req, BusinessTargetScope{}, nil)
		require.NoError(t, err)
	}
	assert.LessOrEqual(t, f.svc.IdempotencyWindowSize(), 3, "幂等记录不得无限增长")
}

// TestBusinessReconcile_EvictedRequest_ReExecutesRatherThanFail
// 记录被淘汰后同 requestId 再来会重新执行（多拉一次），而不是报错或拿错数据。
func TestBusinessReconcile_EvictedRequest_ReExecutesRatherThanFail(t *testing.T) {
	f := newReconcileFixture(t, 1)
	f.svc.SetIdempotencyMaxRecords(1)

	reqA := f.request("req-evict")
	first, err := f.svc.Reconcile(context.Background(), reqA, BusinessTargetScope{}, nil)
	require.NoError(t, err)
	require.Len(t, first.Results, 1)

	// 挤掉 reqA 的记录。
	_, err = f.svc.Reconcile(context.Background(), f.request("req-evict-2"), BusinessTargetScope{}, nil)
	require.NoError(t, err)

	again, err := f.svc.Reconcile(context.Background(), reqA, BusinessTargetScope{}, nil)
	require.NoError(t, err, "淘汰后应重新执行而不是报错")
	assert.False(t, again.IdempotentReplay)
	assert.Equal(t, 3, f.worker.callCount(), "重新执行意味着多下发一次（可接受）")
}

// TestBusinessReconcile_TimeoutPropagatedToDispatchLayer
// 单实例超时必须**真正传到业务下发层**，否则会被 BusinessService 内部的 8s 单命令超时
// 先掐断，规格 §5 要求的 60s 成为不可达的上限。
//
// 这里不靠计时（那会让用例随机器负载抖动），而是断言传下去的值本身。
func TestBusinessReconcile_TimeoutPropagatedToDispatchLayer(t *testing.T) {
	f := newReconcileFixture(t, 1)
	f.svc.SetInstanceTimeout(60 * time.Second)

	var gotTimeout time.Duration
	f.svc.dispatch = func(_ uint, _, _, _ string, timeout time.Duration) (*BusinessResult, error) {
		gotTimeout = timeout
		return &BusinessResult{Available: true, Output: json.RawMessage(`{"ok":1}`)}, nil
	}

	_, err := f.svc.Reconcile(context.Background(), f.request("req-timeout-propagated"), BusinessTargetScope{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 60*time.Second, gotTimeout, "对账超时必须传到业务下发层，不能被 8s 单命令超时覆盖")
}

// TestBusinessDispatchWithTimeout_DefaultFallback 超时 <=0 时回落到单命令默认值。
func TestBusinessDispatchWithTimeout_DefaultFallback(t *testing.T) {
	db := newBatchTestDB(t)
	node := &model.Node{Name: "n1", UUID: "node-uuid-fallback", GRPCPort: 9100, WSPort: 9102}
	require.NoError(t, db.Create(node).Error)
	inst := mkBatchInstance(t, db, "fallback-inst", node.ID, model.InstanceStatusRunning)

	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, &fakeReconcilePluginWorker{})
	svc := NewBusinessService(db, pool)

	// 0 与负数都应回落到默认值，而不是变成「立即超时」。
	for _, timeout := range []time.Duration{0, -time.Second} {
		res, err := svc.DispatchWithTimeout(inst.ID, "activity", "snapshot", "", timeout)
		require.NoError(t, err)
		assert.True(t, res.Available, "timeout=%s 应回落到默认超时而不是立即失败", timeout)
	}
}
