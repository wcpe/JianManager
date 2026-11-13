package service

import (
	"context"
	"encoding/json"
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

// fakeBatchPluginWorker 是伪 Worker 客户端：按实例 UUID 决定 SendPluginCommand 的回执。
//
// behavior[uuid] 为 nil 表示成功；非 nil 表示返回 success=false 且 Error 为该值。
type fakeBatchPluginWorker struct {
	workerpb.WorkerServiceClient

	mu       sync.Mutex
	calls    []*workerpb.SendPluginCommandRequest
	behavior map[string]*string
	// delay 模拟慢实例（超时测试）。
	delay time.Duration
}

func (f *fakeBatchPluginWorker) SendPluginCommand(
	ctx context.Context, req *workerpb.SendPluginCommandRequest, _ ...grpc.CallOption,
) (*workerpb.SendPluginCommandResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	delay := f.delay
	var failReason *string
	if f.behavior != nil {
		failReason = f.behavior[req.InstanceUuid]
	}
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if failReason != nil {
		return &workerpb.SendPluginCommandResponse{Success: false, Error: *failReason}, nil
	}
	return &workerpb.SendPluginCommandResponse{Success: true, Output: `{"ok":true}`}, nil
}

func (f *fakeBatchPluginWorker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// batchExecuteFixture 是一次批量执行测试的装配结果。
type batchExecuteFixture struct {
	db     *gorm.DB
	svc    *BusinessBatchService
	signer *BusinessConfirmTokenSigner
	worker *fakeBatchPluginWorker
	pool   *cpgrpc.ClientPool
	group  uint
	insts  []*model.Instance
}

// newBatchExecuteFixture 装配「一个节点 + 一个分组 + n 个运行中实例」并注入伪 Worker。
func newBatchExecuteFixture(t *testing.T, n int) *batchExecuteFixture {
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
	worker := &fakeBatchPluginWorker{}
	pool.SetWorkerClientForTest(node.UUID, worker)

	signer := newTestSigner(t)
	svc := NewBusinessBatchService(db, NewBusinessService(db, pool), signer)
	return &batchExecuteFixture{
		db: db, svc: svc, signer: signer, worker: worker, pool: pool,
		group: g.ID, insts: insts,
	}
}

// target 返回指向该分组（含后代）的目标。
func (f *batchExecuteFixture) target() BusinessTarget {
	return BusinessTarget{GroupIDs: []uint{f.group}}
}

// TestBusinessBatch_Execute_PartialFailureIsNotOverallFailure
// 5 台里 1 台回执失败 → 整体仍成功返回（无 error），计数 4 success + 1 failed。
func TestBusinessBatch_Execute_PartialFailureIsNotOverallFailure(t *testing.T) {
	f := newBatchExecuteFixture(t, 5)
	failMsg := "插件拒绝执行"
	f.worker.behavior = map[string]*string{f.insts[2].UUID: &failMsg}

	resp, err := f.svc.Execute(context.Background(), BusinessBatchRequest{
		Target:     f.target(),
		Domain:     "activity",
		Action:     "start",
		RequestID:  "req-1",
		ActionRisk: BusinessActionRiskWrite,
	}, BusinessTargetScope{})
	require.NoError(t, err, "部分失败不应变成整体错误")
	assert.Equal(t, 5, resp.Total)
	assert.Equal(t, 4, resp.Succeeded)
	assert.Equal(t, 1, resp.Failed)
	assert.Equal(t, 0, resp.Offline)
	assert.Len(t, resp.Results, 5)

	for _, r := range resp.Results {
		if r.InstanceID == f.insts[2].ID {
			assert.Equal(t, BusinessInstanceFailed, r.Status)
			assert.Equal(t, failMsg, r.Error)
		} else {
			assert.Equal(t, BusinessInstanceSuccess, r.Status)
			assert.JSONEq(t, `{"ok":true}`, string(r.Output))
		}
	}
	// total == 各状态之和（对账口径）
	assert.Equal(t, resp.Total, resp.Succeeded+resp.Failed+resp.Offline+resp.Timeout+resp.Skipped)
}

// TestBusinessBatch_Execute_OfflineNotDispatched 非 RUNNING 实例记 offline 且**不下发**。
func TestBusinessBatch_Execute_OfflineNotDispatched(t *testing.T) {
	f := newBatchExecuteFixture(t, 3)
	require.NoError(t, f.db.Model(&model.Instance{}).Where("id = ?", f.insts[1].ID).
		Update("status", model.InstanceStatusStopped).Error)

	resp, err := f.svc.Execute(context.Background(), BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "req-off",
		ActionRisk: BusinessActionRiskWrite,
	}, BusinessTargetScope{})
	require.NoError(t, err)
	assert.Equal(t, 2, resp.Succeeded)
	assert.Equal(t, 1, resp.Offline)
	assert.Equal(t, 2, f.worker.callCount(), "离线实例不应触达探针通道")

	for _, r := range resp.Results {
		if r.InstanceID == f.insts[1].ID {
			assert.Equal(t, BusinessInstanceOffline, r.Status)
			assert.Contains(t, r.Error, "实例未运行")
		}
	}
}

// TestBusinessBatch_Execute_NodeNotConnectedIsOffline 节点未连（探针不可达）归 offline 而非 failed。
func TestBusinessBatch_Execute_NodeNotConnectedIsOffline(t *testing.T) {
	f := newBatchExecuteFixture(t, 2)
	// 换掉连接池：节点未连
	f.svc.biz.pool = cpgrpc.NewClientPool()

	resp, err := f.svc.Execute(context.Background(), BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "req-nc",
		ActionRisk: BusinessActionRiskWrite,
	}, BusinessTargetScope{})
	require.NoError(t, err)
	assert.Equal(t, 2, resp.Offline, "节点未连属于「未下发」，应计 offline")
	assert.Equal(t, 0, resp.Failed)
}

// TestBusinessBatch_Execute_TimeoutDoesNotBlockOthers 单实例超时不影响其他实例。
func TestBusinessBatch_Execute_TimeoutDoesNotBlockOthers(t *testing.T) {
	f := newBatchExecuteFixture(t, 3)
	f.worker.delay = 300 * time.Millisecond
	f.svc.SetInstanceTimeout(50 * time.Millisecond)
	f.svc.SetConcurrency(4)

	start := time.Now()
	resp, err := f.svc.Execute(context.Background(), BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "req-timeout",
		ActionRisk: BusinessActionRiskWrite,
	}, BusinessTargetScope{})
	require.NoError(t, err)
	elapsed := time.Since(start)

	assert.Equal(t, 3, resp.Timeout, "三台都超时")
	assert.Equal(t, resp.Total, resp.Succeeded+resp.Failed+resp.Offline+resp.Timeout+resp.Skipped)
	// 并发执行：总耗时接近单次超时而不是三次叠加
	assert.Less(t, elapsed, 250*time.Millisecond, "超时应并发生效而非串行累加")
}

// TestBusinessBatch_Execute_IdempotentReplay 同 requestId 重复提交返回首次结果且不重复下发。
func TestBusinessBatch_Execute_IdempotentReplay(t *testing.T) {
	f := newBatchExecuteFixture(t, 3)
	req := BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "req-idem",
		ActionRisk: BusinessActionRiskWrite,
	}
	first, err := f.svc.Execute(context.Background(), req, BusinessTargetScope{})
	require.NoError(t, err)
	assert.False(t, first.IdempotentReplay)
	callsAfterFirst := f.worker.callCount()

	second, err := f.svc.Execute(context.Background(), req, BusinessTargetScope{})
	require.NoError(t, err)
	assert.True(t, second.IdempotentReplay, "第二次应标记为幂等重放")
	assert.Equal(t, first.Total, second.Total)
	assert.Equal(t, first.Succeeded, second.Succeeded)
	assert.Equal(t, callsAfterFirst, f.worker.callCount(), "重放不得重复下发")
}

// TestBusinessBatch_Execute_IdempotencyKeyReused 同 requestId 但参数不同 → IDEMPOTENCY_KEY_REUSED。
func TestBusinessBatch_Execute_IdempotencyKeyReused(t *testing.T) {
	f := newBatchExecuteFixture(t, 2)
	req := BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "req-reuse",
		ActionRisk: BusinessActionRiskWrite,
	}
	_, err := f.svc.Execute(context.Background(), req, BusinessTargetScope{})
	require.NoError(t, err)

	// 同一个 requestId，换了 action → 必须报冲突而不是静默回放旧结果。
	req.Action = "stop"
	_, err = f.svc.Execute(context.Background(), req, BusinessTargetScope{})
	require.ErrorIs(t, err, ErrBusinessIdempotencyKeyReused)
	code, status := BusinessBatchErrorCode(err)
	assert.Equal(t, "IDEMPOTENCY_KEY_REUSED", code)
	assert.Equal(t, 409, status)
}

// TestBusinessBatch_Execute_IdempotencyScopedByIssuer 不同调用方的同一 requestId 互不干扰。
func TestBusinessBatch_Execute_IdempotencyScopedByIssuer(t *testing.T) {
	f := newBatchExecuteFixture(t, 2)
	base := BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "shared-id",
		ActionRisk: BusinessActionRiskWrite,
	}
	base.IssuerKey = "user:1"
	first, err := f.svc.Execute(context.Background(), base, BusinessTargetScope{})
	require.NoError(t, err)
	require.False(t, first.IdempotentReplay)
	calls := f.worker.callCount()

	base.IssuerKey = "user:2"
	second, err := f.svc.Execute(context.Background(), base, BusinessTargetScope{})
	require.NoError(t, err)
	assert.False(t, second.IdempotentReplay, "另一个调用方的同名 requestId 不应命中他人结果")
	assert.Greater(t, f.worker.callCount(), calls, "应重新执行")
}

// TestBusinessBatch_Execute_TargetEmpty 空目标返回 TARGET_EMPTY。
func TestBusinessBatch_Execute_TargetEmpty(t *testing.T) {
	f := newBatchExecuteFixture(t, 0)
	_, err := f.svc.Execute(context.Background(), BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "req-empty",
		ActionRisk: BusinessActionRiskWrite,
	}, BusinessTargetScope{})
	require.ErrorIs(t, err, ErrBusinessTargetEmpty)
	code, status := BusinessBatchErrorCode(err)
	assert.Equal(t, "TARGET_EMPTY", code)
	assert.Equal(t, 400, status)
}

// TestBusinessBatch_Execute_ConfirmationRequired 跨大区无 token → CONFIRMATION_REQUIRED。
func TestBusinessBatch_Execute_ConfirmationRequired(t *testing.T) {
	f := newBatchExecuteFixture(t, 1)
	// 再造一个大区，把目标铺到两个大区上
	regionA := mkGroup(t, f.db, "region-a", &f.group)
	regionB := mkGroup(t, f.db, "region-b", &f.group)
	require.NoError(t, f.db.Create(&model.InstanceGroupMember{
		GroupID: regionA.ID, InstanceID: f.insts[0].ID}).Error)
	other := mkBatchInstance(t, f.db, "other", f.insts[0].NodeID, model.InstanceStatusRunning)
	require.NoError(t, f.db.Create(&model.InstanceGroupMember{
		GroupID: regionB.ID, InstanceID: other.ID}).Error)

	_, err := f.svc.Execute(context.Background(), BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "req-confirm",
		ActionRisk: BusinessActionRiskWrite,
	}, BusinessTargetScope{})
	require.ErrorIs(t, err, ErrBusinessConfirmationRequired)
	code, status := BusinessBatchErrorCode(err)
	assert.Equal(t, "CONFIRMATION_REQUIRED", code)
	assert.Equal(t, 409, status)
	assert.Equal(t, 0, f.worker.callCount(), "被确认门槛拦下时不得下发任何实例")
}

// TestBusinessBatch_Execute_ReasonRequired DESTRUCTIVE 动作无 reason → REASON_REQUIRED。
func TestBusinessBatch_Execute_ReasonRequired(t *testing.T) {
	f := newBatchExecuteFixture(t, 1)
	// 先预览拿 token（critical 需要 token + reason）
	preview, err := f.svc.Preview(f.target(), BusinessActionRiskDestructive, "user:1", BusinessTargetScope{})
	require.NoError(t, err)
	require.Equal(t, BusinessRiskCritical, preview.Risk.Level)
	require.NotEmpty(t, preview.ConfirmToken)

	_, err = f.svc.Execute(context.Background(), BusinessBatchRequest{
		ConfirmToken: preview.ConfirmToken,
		Domain:       "activity", Action: "purge", RequestID: "req-reason",
		ActionRisk: BusinessActionRiskDestructive,
		IssuerKey:  "user:1",
	}, BusinessTargetScope{})
	require.ErrorIs(t, err, ErrBusinessReasonRequired)
	code, status := BusinessBatchErrorCode(err)
	assert.Equal(t, "REASON_REQUIRED", code)
	assert.Equal(t, 400, status)
	assert.Equal(t, 0, f.worker.callCount())
}

// TestBusinessBatch_Execute_TargetChanged 预览后分组新增实例 → TARGET_CHANGED。
func TestBusinessBatch_Execute_TargetChanged(t *testing.T) {
	f := newBatchExecuteFixture(t, 1)
	// 用 DESTRUCTIVE 强制走 token 路径（单实例 low 风险不签 token）
	preview, err := f.svc.Preview(f.target(), BusinessActionRiskDestructive, "user:1", BusinessTargetScope{})
	require.NoError(t, err)
	require.NotEmpty(t, preview.ConfirmToken)

	// 预览之后往分组里塞一个新实例（模拟运营在确认间隙加机器）
	late := mkBatchInstance(t, f.db, "late", f.insts[0].NodeID, model.InstanceStatusRunning)
	linkGroup(t, f.db, f.group, late.ID)

	_, err = f.svc.Execute(context.Background(), BusinessBatchRequest{
		ConfirmToken: preview.ConfirmToken,
		Domain:       "activity", Action: "purge", RequestID: "req-changed",
		ActionRisk: BusinessActionRiskDestructive, Reason: "清理活动数据",
		IssuerKey: "user:1",
	}, BusinessTargetScope{})
	require.ErrorIs(t, err, ErrBusinessTargetChanged)
	code, status := BusinessBatchErrorCode(err)
	assert.Equal(t, "TARGET_CHANGED", code)
	assert.Equal(t, 409, status)
	assert.Equal(t, 0, f.worker.callCount())
}

// TestBusinessBatch_Execute_TokenCrossAccount 他人 token → TOKEN_MISMATCH。
func TestBusinessBatch_Execute_TokenCrossAccount(t *testing.T) {
	f := newBatchExecuteFixture(t, 1)
	preview, err := f.svc.Preview(f.target(), BusinessActionRiskDestructive, "user:1", BusinessTargetScope{})
	require.NoError(t, err)

	_, err = f.svc.Execute(context.Background(), BusinessBatchRequest{
		ConfirmToken: preview.ConfirmToken,
		Domain:       "activity", Action: "purge", RequestID: "req-cross",
		ActionRisk: BusinessActionRiskDestructive, Reason: "x",
		IssuerKey: "user:2",
	}, BusinessTargetScope{})
	require.ErrorIs(t, err, ErrBusinessTokenMismatch)
	code, status := BusinessBatchErrorCode(err)
	assert.Equal(t, "TOKEN_MISMATCH", code)
	assert.Equal(t, 403, status)
}

// TestBusinessBatch_Execute_TokenExpired 过期 token → TOKEN_EXPIRED。
func TestBusinessBatch_Execute_TokenExpired(t *testing.T) {
	f := newBatchExecuteFixture(t, 1)
	base := time.Now()
	f.signer.SetTTL(time.Minute)
	f.signer.SetClock(func() time.Time { return base })

	preview, err := f.svc.Preview(f.target(), BusinessActionRiskDestructive, "user:1", BusinessTargetScope{})
	require.NoError(t, err)
	require.NotEmpty(t, preview.ConfirmToken)

	f.signer.SetClock(func() time.Time { return base.Add(5 * time.Minute) })
	_, err = f.svc.Execute(context.Background(), BusinessBatchRequest{
		ConfirmToken: preview.ConfirmToken,
		Domain:       "activity", Action: "purge", RequestID: "req-exp",
		ActionRisk: BusinessActionRiskDestructive, Reason: "x",
		IssuerKey: "user:1",
	}, BusinessTargetScope{})
	require.ErrorIs(t, err, ErrBusinessTokenExpired)
	code, status := BusinessBatchErrorCode(err)
	assert.Equal(t, "TOKEN_EXPIRED", code)
	assert.Equal(t, 409, status)
}

// TestBusinessBatch_Preview_LowRiskNoToken 低风险预览不签发 token。
func TestBusinessBatch_Preview_LowRiskNoToken(t *testing.T) {
	f := newBatchExecuteFixture(t, 3)
	preview, err := f.svc.Preview(f.target(), BusinessActionRiskWrite, "user:1", BusinessTargetScope{})
	require.NoError(t, err)
	assert.Equal(t, BusinessRiskLow, preview.Risk.Level)
	assert.Empty(t, preview.ConfirmToken, "low 风险不该诱导用户走确认流程")
	assert.True(t, preview.ExpiresAt.IsZero())
	assert.Equal(t, 3, preview.Resolved.Count())
}

// TestBusinessBatch_Preview_Empty 空目标预览返回 TARGET_EMPTY。
func TestBusinessBatch_Preview_Empty(t *testing.T) {
	f := newBatchExecuteFixture(t, 0)
	_, err := f.svc.Preview(f.target(), BusinessActionRiskWrite, "user:1", BusinessTargetScope{})
	require.ErrorIs(t, err, ErrBusinessTargetEmpty)
}

// TestBusinessBatch_Progress_MonotonicAndConsistent progress 单调递增且末帧等于 done。
func TestBusinessBatch_Progress_MonotonicAndConsistent(t *testing.T) {
	f := newBatchExecuteFixture(t, 45)
	f.svc.SetProgressBatch(10)
	f.svc.SetConcurrency(8)

	var mu sync.Mutex
	var frames []BusinessBatchProgress
	resp, err := f.svc.ExecuteWithProgress(context.Background(), BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "req-progress",
		ActionRisk: BusinessActionRiskWrite,
	}, BusinessTargetScope{}, func(p BusinessBatchProgress) {
		mu.Lock()
		frames = append(frames, p)
		mu.Unlock()
	})
	require.NoError(t, err)
	require.NotEmpty(t, frames)

	prev := 0
	for i, p := range frames {
		assert.Equal(t, 45, p.Total, "每帧 total 恒定")
		assert.GreaterOrEqual(t, p.Done, prev, "第 %d 帧 done 应单调不减", i)
		prev = p.Done
		assert.Equal(t, p.Done, p.Succeeded+p.Failed+p.Offline+p.Timeout+p.Skipped,
			"第 %d 帧的各状态之和应等于 done", i)
	}
	last := frames[len(frames)-1]
	assert.Equal(t, 45, last.Done, "末帧 done 应等于总数")
	assert.Equal(t, resp.Succeeded, last.Succeeded, "done 帧数值应与响应一致")
	assert.Equal(t, resp.Failed, last.Failed)
	assert.Equal(t, resp.Offline, last.Offline)
}

// TestBusinessBatch_Execute_ScopeConvergence 越权实例被剔除后执行。
func TestBusinessBatch_Execute_ScopeConvergence(t *testing.T) {
	f := newBatchExecuteFixture(t, 2)
	resp, err := f.svc.Execute(context.Background(), BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "req-scope",
		ActionRisk: BusinessActionRiskWrite,
	}, BusinessTargetScope{IDs: []uint{f.insts[0].ID}, Scoped: true})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.Total)
	assert.Equal(t, 1, resp.Succeeded)
	assert.Equal(t, f.insts[0].ID, resp.Results[0].InstanceID)
}

// TestBusinessBatch_Execute_InvalidPayload 批量载荷非 JSON 对象时提前拒绝（不产生 100 条 failed）。
func TestBusinessBatch_Execute_InvalidPayload(t *testing.T) {
	f := newBatchExecuteFixture(t, 2)
	_, err := f.svc.Execute(context.Background(), BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "req-payload",
		Payload: json.RawMessage(`"a string"`), ActionRisk: BusinessActionRiskWrite,
	}, BusinessTargetScope{})
	require.ErrorIs(t, err, ErrInvalidBusinessCommand)
	assert.Equal(t, 0, f.worker.callCount())
}

// TestBusinessBatch_Execution_WriteContextInjected 批量下发会注入 requestId 作为 taskId（幂等贯通）。
func TestBusinessBatch_Execution_WriteContextInjected(t *testing.T) {
	f := newBatchExecuteFixture(t, 1)
	_, err := f.svc.Execute(context.Background(), BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "task-key-1",
		Payload: json.RawMessage(`{"activityId":"spring"}`), ActionRisk: BusinessActionRiskWrite,
		Operator: "alice", OperatorID: 7,
	}, BusinessTargetScope{})
	require.NoError(t, err)
	require.Equal(t, 1, f.worker.callCount())

	var sent map[string]any
	require.NoError(t, json.Unmarshal([]byte(f.worker.calls[0].Command.PayloadJson), &sent))
	assert.Equal(t, "task-key-1", sent["taskId"], "requestId 应作为通道幂等键注入 payload")
	assert.Equal(t, "alice", sent["operator"])
	assert.Equal(t, float64(7), sent["operatorId"])
	assert.Equal(t, "spring", sent["activityId"], "业务方显式入参必须保留")
}

// TestBusinessBatchErrorCode 错误码映射覆盖 §8 全表。
func TestBusinessBatchErrorCode(t *testing.T) {
	cases := []struct {
		err    error
		code   string
		status int
	}{
		{nil, "", 200},
		{ErrBusinessTargetEmpty, "TARGET_EMPTY", 400},
		{ErrBusinessTargetUnspecified, "TARGET_EMPTY", 400},
		{ErrBusinessTargetChanged, "TARGET_CHANGED", 409},
		{ErrBusinessTokenExpired, "TOKEN_EXPIRED", 409},
		{ErrBusinessTokenInvalid, "TOKEN_EXPIRED", 409},
		{ErrBusinessTokenMismatch, "TOKEN_MISMATCH", 403},
		{ErrBusinessConfirmationRequired, "CONFIRMATION_REQUIRED", 409},
		{ErrBusinessReasonRequired, "REASON_REQUIRED", 400},
		{ErrBusinessIdempotencyKeyReused, "IDEMPOTENCY_KEY_REUSED", 409},
		{ErrInvalidBusinessCommand, "INVALID_REQUEST", 400},
	}
	for _, c := range cases {
		code, status := BusinessBatchErrorCode(c.err)
		assert.Equal(t, c.code, code, "错误码映射: %v", c.err)
		assert.Equal(t, c.status, status, "状态码映射: %v", c.err)
	}
}

// TestBusinessBatch_RetryAfterPrecheckFailure_NotSwallowedByPlaceholder
// 前置校验失败后，用同一个 requestId 修正参数重试必须真的执行（占位已撤销）。
func TestBusinessBatch_RetryAfterPrecheckFailure_NotSwallowedByPlaceholder(t *testing.T) {
	f := newBatchExecuteFixture(t, 2)
	preview, err := f.svc.Preview(f.target(), BusinessActionRiskDestructive, "", BusinessTargetScope{})
	require.NoError(t, err)
	require.NotEmpty(t, preview.ConfirmToken)

	// 首次：有 token 但缺 reason → critical 被 REASON_REQUIRED 拦下
	base := BusinessBatchRequest{
		ConfirmToken: preview.ConfirmToken,
		Target:       f.target(), Domain: "activity", Action: "start", RequestID: "req-retry",
		ActionRisk: BusinessActionRiskDestructive,
	}
	_, err = f.svc.Execute(context.Background(), base, BusinessTargetScope{})
	require.ErrorIs(t, err, ErrBusinessReasonRequired)
	assert.Equal(t, 0, f.worker.callCount())
	assert.Equal(t, 0, f.svc.IdempotencyWindowSize(), "失败的前置校验不该留下幂等占位")

	// 补上 reason 后重试同一个 requestId：应当正常执行。
	base.Reason = "清理活动数据"
	resp, err := f.svc.Execute(context.Background(), base, BusinessTargetScope{})
	require.NoError(t, err)
	assert.False(t, resp.IdempotentReplay, "修正后重试不该被当作重放")
	assert.Equal(t, 2, resp.Succeeded)
	assert.Equal(t, 2, f.worker.callCount())
}

// TestBusinessBatch_Execute_ReadActionSkipsWriteContext READ 动作不注入写上下文（不污染插件流水）。
func TestBusinessBatch_Execute_ReadActionSkipsWriteContext(t *testing.T) {
	f := newBatchExecuteFixture(t, 1)
	_, err := f.svc.Execute(context.Background(), BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "query", RequestID: "task-read",
		Payload: json.RawMessage(`{"activityId":"spring"}`), ActionRisk: BusinessActionRiskRead,
		Operator: "alice", OperatorID: 7,
	}, BusinessTargetScope{})
	require.NoError(t, err)
	require.Equal(t, 1, f.worker.callCount())

	var sent map[string]any
	require.NoError(t, json.Unmarshal([]byte(f.worker.calls[0].Command.PayloadJson), &sent))
	assert.NotContains(t, sent, "taskId", "只读动作不该注入写上下文")
	assert.NotContains(t, sent, "operator")
	assert.Equal(t, "spring", sent["activityId"])
}

// TestBusinessBatch_ConcurrentSameRequestID_NoDoubleDispatch
// 并发用同一 requestId 提交：第二个请求看到占位（尚未产生结果）会继续执行一次，
// 最终两条都写回，窗口里只留一份结果；关键是不得因占位而卡死或返回 nil。
func TestBusinessBatch_ConcurrentSameRequestID_NoDoubleDispatch(t *testing.T) {
	f := newBatchExecuteFixture(t, 2)
	req := BusinessBatchRequest{
		Target: f.target(), Domain: "activity", Action: "start", RequestID: "req-concurrent",
		ActionRisk: BusinessActionRiskWrite,
	}

	const n = 4
	var wg sync.WaitGroup
	responses := make([]*BusinessBatchResponse, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			responses[idx], errs[idx] = f.svc.Execute(context.Background(), req, BusinessTargetScope{})
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoErrorf(t, errs[i], "第 %d 个并发请求不该失败", i)
		require.NotNilf(t, responses[i], "第 %d 个并发请求必须拿到结果（不能因占位返回 nil）", i)
		assert.Equal(t, 2, responses[i].Total)
	}
	// 幂等窗口里只留一条记录
	assert.Equal(t, 1, f.svc.IdempotencyWindowSize())
}
