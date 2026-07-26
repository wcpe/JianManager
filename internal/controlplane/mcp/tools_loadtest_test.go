package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-398：压测编排工具的契约测试——容量不足不物化、双向 scope 处置、报告护栏、重试幂等键透传。

// fixedCapacityProvider 返回固定容量快照，便于构造「容量不足」与「节点可见性」场景。
type fixedCapacityProvider struct {
	snapshot service.BotLoadCapacitySnapshot
}

func (p *fixedCapacityProvider) Snapshot(_ context.Context, _ uint) (*service.BotLoadCapacitySnapshot, error) {
	out := p.snapshot
	out.NodeCapacities = append([]service.BotLoadNodeCapacity(nil), p.snapshot.NodeCapacities...)
	return &out, nil
}

// recordingExecutor 记录 stop/retry 的入参，用于断言 MCP 只做透传不改写。
type recordingExecutor struct {
	stubBotLoadExecutor
	stopCalls  int
	retryCalls []service.BotLoadRetryRequest
}

func (r *recordingExecutor) Stop(_ context.Context, sessionID uint, _ ...string) (*model.BotStressSession, error) {
	r.stopCalls++
	return &model.BotStressSession{ID: sessionID}, nil
}

func (r *recordingExecutor) RetryFailed(_ context.Context, _ uint, req service.BotLoadRetryRequest) (*service.BotLoadRetryResult, error) {
	r.retryCalls = append(r.retryCalls, req)
	return &service.BotLoadRetryResult{Errors: []service.BotLoadRetryItemError{}}, nil
}

// newPreflightDeps 组装一套可用的预检依赖，nodeCapacity 决定单节点可用容量。
func newPreflightDeps(t *testing.T, db *gorm.DB, nodeID uint, nodeCapacity int) ToolDeps {
	t.Helper()
	provider := &fixedCapacityProvider{snapshot: service.BotLoadCapacitySnapshot{
		NodeCapacities: []service.BotLoadNodeCapacity{{
			NodeID: nodeID, NodeUUID: "node-uuid", NodeName: "exec",
			Online: true, BotWorkerReady: true,
			MaxBots: nodeCapacity, AvailableBots: nodeCapacity, CapacityGeneration: 1,
		}},
		ReservationLimits: map[uint]int{nodeID: nodeCapacity},
		UpdatedAt:         time.Now().UTC(),
	}}
	reservations := service.NewBotLoadReservationStore(nil, time.Minute)
	signer, err := service.NewBotLoadPlanTokenSigner([]byte("fr398-test-plan-token-secret"), nil)
	require.NoError(t, err)
	return ToolDeps{
		Agent:         service.NewAgentTokenService(db),
		StressSession: service.NewBotStressSessionService(db, nil),
		Preflight:     service.NewBotLoadPreflightService(db, provider, reservations, signer, nil),
		Capacity:      provider,
	}
}

func TestCallTool_Preflight_CapacityShortageDoesNotMaterializeBots(t *testing.T) {
	db := newBotToolDB(t)
	f := seedBotToolFixture(t, db)
	// 会话需 5 个 Bot，节点仅剩 2 个容量 → 必然容量不足。
	deps := newPreflightDeps(t, db, f.node.ID, 2)

	res := CallTool(context.Background(), deps, botLoadPrincipal(f), "loadtest_run_preflight",
		map[string]any{"id": float64(f.session.ID)})
	require.False(t, res.IsError, res.Content[0].Text)

	var payload service.BotLoadPreflightResult
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload))
	assert.False(t, payload.Ready, "容量不足时不得就绪")
	assert.Empty(t, payload.PlanToken, "未就绪不得签发 planToken")
	require.NotEmpty(t, payload.Blockers, "必须返回缺口 blockers")
	assert.Equal(t, service.BotLoadCapacityInsufficientCode, payload.Blockers[0].Code)

	// 关键断言：容量不足绝不物化任何 Bot 或批次。
	var bots, batches int64
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ?", f.session.ID).Count(&bots).Error)
	require.NoError(t, db.Model(&model.BotLoadBatch{}).Count(&batches).Error)
	assert.Zero(t, bots, "容量不足不得创建 Bot")
	assert.Zero(t, batches, "容量不足不得创建批次")
}

func TestCallTool_Preflight_RejectsExecutorNodeOutOfScope(t *testing.T) {
	db := newBotToolDB(t)
	f := seedBotToolFixture(t, db)
	deps := newPreflightDeps(t, db, f.node.ID, 50)

	// 显式请求一个不在 scope 内的发压节点 → 整体拒绝，不静默缩减。
	res := CallTool(context.Background(), deps, botLoadPrincipal(f), "loadtest_run_preflight",
		map[string]any{"id": float64(f.session.ID), "executorNodeIds": []any{float64(f.node.ID), float64(f.node.ID + 777)}})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "不做静默缩减")
}

func TestCallTool_RunStop_ExecutesAndReportsOutOfScopeNodes(t *testing.T) {
	db := newBotToolDB(t)
	f := seedBotToolFixture(t, db)
	// 追加一个 scope 外节点的批次，构造停止方向的部分越界场景。
	outsideNode := &model.Node{Name: "n2", Host: "127.0.0.1", Secret: "s2", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(outsideNode).Error)
	seedExecutorBatch(t, db, f.session.ID, f.node.ID, 0)
	seedExecutorBatch(t, db, f.session.ID, outsideNode.ID, 1)

	exec := &recordingExecutor{}
	deps := ToolDeps{Agent: service.NewAgentTokenService(db), Execution: exec}
	res := CallTool(context.Background(), deps, botLoadPrincipal(f), "loadtest_run_stop",
		map[string]any{"id": float64(f.session.ID)})
	require.False(t, res.IsError, res.Content[0].Text)
	assert.Equal(t, 1, exec.stopCalls, "停止是安全方向，越界节点不阻断执行")

	var payload struct {
		OutOfScope []uint `json:"outOfScopeExecutorNodeIds"`
		Notice     string `json:"notice"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload))
	assert.Equal(t, []uint{outsideNode.ID}, payload.OutOfScope, "必须列出越界发压节点")
	assert.NotEmpty(t, payload.Notice)
}

func TestCallTool_RunStart_RejectsWhenExecutorNodeOutOfScope(t *testing.T) {
	db := newBotToolDB(t)
	f := seedBotToolFixture(t, db)
	outsideNode := &model.Node{Name: "n3", Host: "127.0.0.1", Secret: "s3", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(outsideNode).Error)
	seedExecutorBatch(t, db, f.session.ID, outsideNode.ID, 0)

	started := false
	deps := ToolDeps{
		Agent: service.NewAgentTokenService(db),
		Execution: &stubBotLoadExecutor{onStart: func(context.Context, uint, string) error {
			started = true
			return nil
		}},
	}
	res := CallTool(context.Background(), deps, botLoadPrincipal(f), "loadtest_run_start",
		map[string]any{"id": float64(f.session.ID), "planToken": "opaque.token"})
	assert.True(t, res.IsError)
	assert.False(t, started, "启动方向任一节点越界即整体拒绝")
}

func TestCallTool_RunRetryFailed_PassesRequestIDUnchanged(t *testing.T) {
	db := newBotToolDB(t)
	f := seedBotToolFixture(t, db)
	seedExecutorBatch(t, db, f.session.ID, f.node.ID, 0)

	exec := &recordingExecutor{}
	deps := ToolDeps{Agent: service.NewAgentTokenService(db), Execution: exec}
	const requestID = "3f7d1f2e-0000-4000-8000-000000000001"
	args := map[string]any{
		"id": float64(f.session.ID), "requestId": requestID,
		"botUuids": []any{"bot-uuid-1"},
	}
	// 同一 requestId 重复调用：MCP 必须原样透传，由 service 承担幂等。
	CallTool(context.Background(), deps, botLoadPrincipal(f), "loadtest_run_retry_failed", args)
	CallTool(context.Background(), deps, botLoadPrincipal(f), "loadtest_run_retry_failed", args)

	require.Len(t, exec.retryCalls, 2)
	for _, call := range exec.retryCalls {
		assert.Equal(t, requestID, call.RequestID, "requestId 必须原样透传")
		assert.Equal(t, []string{"bot-uuid-1"}, call.BotUUIDs)
	}
}

func TestCallTool_NodeCapacity_HidesNodesOutOfScope(t *testing.T) {
	db := newBotToolDB(t)
	f := seedBotToolFixture(t, db)
	provider := &fixedCapacityProvider{snapshot: service.BotLoadCapacitySnapshot{
		NodeCapacities: []service.BotLoadNodeCapacity{
			{NodeID: f.node.ID, NodeName: "in-scope", Online: true, BotWorkerReady: true, MaxBots: 30, AvailableBots: 20},
			{NodeID: f.node.ID + 888, NodeName: "out-of-scope", Online: true, BotWorkerReady: true, MaxBots: 99, AvailableBots: 99},
		},
		UpdatedAt: time.Now().UTC(),
	}}
	deps := ToolDeps{Agent: service.NewAgentTokenService(db), Capacity: provider}

	res := CallTool(context.Background(), deps, botLoadPrincipal(f), "loadtest_node_capacity", nil)
	require.False(t, res.IsError, res.Content[0].Text)

	var payload struct {
		Items             []service.BotLoadNodeCapacity `json:"items"`
		AvailableCapacity int                           `json:"availableCapacity"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload))
	require.Len(t, payload.Items, 1, "scope 外节点不得暴露")
	assert.Equal(t, f.node.ID, payload.Items[0].NodeID)
	assert.Equal(t, 20, payload.AvailableCapacity)
}

func TestOversizedReportNotice_GuidesToPagedDetails(t *testing.T) {
	// 报告超限只给摘要与引导，绝不截断正文冒充完整数据。
	notice := oversizedReportNotice(7, maxReportInlineBytes+1)
	assert.Equal(t, true, notice["oversized"])
	assert.Contains(t, notice["notice"], "loadtest_run_failures")
}

// seedExecutorBatch 为会话追加一个执行节点批次。
func seedExecutorBatch(t *testing.T, db *gorm.DB, sessionID, nodeID uint, ordinal int) {
	t.Helper()
	require.NoError(t, db.Create(&model.BotLoadBatch{
		StressSessionID: sessionID, ExecutorNodeID: nodeID, Ordinal: ordinal,
		PlannedCount: 5, State: model.BotLoadBatchPlanned,
		IdempotencyKey: fmt.Sprintf("fr398-mcp-%d-%d-%d", sessionID, nodeID, ordinal),
	}).Error)
}
