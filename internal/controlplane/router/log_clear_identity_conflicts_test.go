package router

// 「人工确认并清空身份冲突」端点的 CP 侧回归。
//
// 本文件覆盖四类红线（与 Worker 侧同一套语义，但判据在**产品面**上）：
//  1. 操作人取自**认证上下文**、经传输 metadata 下传（请求体伪造的 operator 无效）；
//  2. 理由必填，且同样经 metadata 下传（请求体可被伪造，理由要进审计凭据）；
//  3. 指名源须先完成对象授权（与缺口裁定同一判据），且**拒绝时不下发任何 RPC**；
//  4. 不传源键 = 批量动作，只允许平台管理员。
//
// 转红方式（实测，见各用例注释）。

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// recordingClearConflictsWorker 记录 CP 转发的 metadata 与请求体（与 resolve-gaps 的桩同型）。
type recordingClearConflictsWorker struct {
	workerpb.WorkerServiceClient
	calls    int
	hasMD    bool
	operator string
	reason   string
	request  *workerpb.LogClearIdentityConflictsRequest
}

func (w *recordingClearConflictsWorker) LogClearIdentityConflicts(ctx context.Context, in *workerpb.LogClearIdentityConflictsRequest, _ ...grpc.CallOption) (*workerpb.LogClearIdentityConflictsResponse, error) {
	md, ok := metadata.FromOutgoingContext(ctx)
	w.calls++
	w.hasMD = ok
	if values := md.Get(operatorMetadataKey); len(values) > 0 {
		w.operator = values[0]
	}
	if values := md.Get("x-jm-reason"); len(values) > 0 {
		w.reason = values[0]
	}
	w.request = in
	return &workerpb.LogClearIdentityConflictsResponse{
		RequestId:      in.GetRequestId(),
		ClearedCount:   9,
		ClearedSources: []string{"inst:145/file/g1", "inst:151/file/g1"},
		SkippedSources: []string{"inst:151/file/g2"},
	}, nil
}

func (w *recordingClearConflictsWorker) snapshot() (int, bool, string, string, *workerpb.LogClearIdentityConflictsRequest) {
	return w.calls, w.hasMD, w.operator, w.reason, w.request
}

// clearConflictsEndpoint 构造本端点的完整路径。
func clearConflictsEndpoint(nodeID uint) string {
	return fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/clear-identity-conflicts", nodeID)
}

// sourceKeyOf 按 CP 的绑定契约（instance:<uuid>@worker:<uuid>）构造完整源键。
func sourceKeyOf(inst model.Instance, node *model.Node, stream string) string {
	return fmt.Sprintf("inst:%d/%s/instance:%s@worker:%s", inst.ID, stream, inst.UUID, node.UUID)
}

// TestLogClearIdentityConflictsForwardsOperatorAndReason：单源路径必须把认证主体与理由
// 作为 `x-jm-operator` / `x-jm-reason` 下传，并把 Worker 的结果原样回给调用方。
//
// 转红方式（实测）：去掉 handler 里 metadata.Pairs(...) 的注入——桩读到空操作人，本用例红。
func TestLogClearIdentityConflictsForwardsOperatorAndReason(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, adminToken, "clear-conflict-owner")
	id := makeInstanceInGroup(t, db, node.ID, groupA, "clear-conflict-source", model.InstanceStatusStopped)
	var inst model.Instance
	require.NoError(t, db.First(&inst, id).Error)
	sourceKey := sourceKeyOf(inst, node, "file")

	stub := &recordingClearConflictsWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)
	resp := makeRequest(r, http.MethodPost, clearConflictsEndpoint(node.ID),
		map[string]any{"storageNamespace": sourceKey, "reason": "已核对 VL 保留版本正确"}, adminToken)

	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	calls, hasMD, operator, reason, request := stub.snapshot()
	require.Equal(t, 1, calls)
	require.True(t, hasMD, "CP 必须经 outgoing metadata 下达操作人与理由")
	require.Equal(t, "admin", operator, "操作人取自认证主体（登录名），不是请求体")
	require.Equal(t, "已核对 VL 保留版本正确", reason, "理由必须经可信传输 metadata 下发")
	require.Equal(t, sourceKey, request.GetStorageNamespace())
	// 结果凭据必须原样回给调用方（清了多少条 / 清了谁 / 跳过了谁是运维判断的依据）。
	require.Contains(t, resp.Body.String(), `"clearedCount":9`)
	require.Contains(t, resp.Body.String(), "inst:145/file/g1")
	require.Contains(t, resp.Body.String(), "inst:151/file/g2")
}

// TestLogClearIdentityConflictsRejectsMissingOperator：取不到认证主体 → 403 且**不下发 RPC**。
//
// 转红方式（实测）：把取不到主体时的拒绝改成继续转发——本用例在「403 / 零 RPC」处变红。
func TestLogClearIdentityConflictsRejectsMissingOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	node := createTestNode(t, db)
	stub := &recordingClearConflictsWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)

	// 刻意不走认证中间件：模拟「认证主体缺失」的调用面。
	handler := NewLogRuntimeHandler(service.NewNodeService(db), pool, service.NewAuthzService(db),
		service.NewInstanceService(db, service.NewGroupService(db), pool))
	engine := gin.New()
	engine.POST("/api/v1/nodes/:id/log-runtime/ingest/clear-identity-conflicts", handler.ClearIdentityConflicts)

	resp := makeRequest(engine, http.MethodPost, clearConflictsEndpoint(node.ID),
		map[string]any{"storageNamespace": "inst:9/file", "reason": "已核对"}, "")
	require.Equal(t, http.StatusForbidden, resp.Code, resp.Body.String())
	require.Contains(t, resp.Body.String(), "OPERATOR_UNKNOWN")
	require.Zero(t, stub.calls, "取不到主体时不得下发任何 RPC")
}

// TestLogClearIdentityConflictsRequiresReason：理由缺失必须在 CP 就拒绝，且不下发 RPC。
//
// 为什么在 CP 就拒（而不是让 Worker 报错）：Worker 的提示是「请在 incoming metadata 设置
// x-jm-reason」，对 CP 调用方不可执行——它没法设置 Worker 的入站 metadata。
func TestLogClearIdentityConflictsRequiresReason(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)

	for _, body := range []any{
		map[string]any{"storageNamespace": "inst:9/file"},
		map[string]any{"storageNamespace": "inst:9/file", "reason": ""},
		map[string]any{"storageNamespace": "inst:9/file", "reason": "   "},
		map[string]any{},
	} {
		stub := &recordingClearConflictsWorker{}
		pool.SetWorkerClientForTest(node.UUID, stub)
		resp := makeRequest(r, http.MethodPost, clearConflictsEndpoint(node.ID), body, adminToken)
		require.Equal(t, http.StatusBadRequest, resp.Code, "body=%v resp=%s", body, resp.Body.String())
		require.Contains(t, resp.Body.String(), "REASON_REQUIRED")
		require.Zero(t, stub.calls, "理由缺失时不得下发 RPC")
	}
}

// TestLogClearIdentityConflictsSourceAuthorization：与缺口裁定共用同一套对象授权判据。
//
// 判据不能比缺口裁定松：两者都是"让查询面不再提示某段不确定性"的人工动作。
func TestLogClearIdentityConflictsSourceAuthorization(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	otherNode := createTestNodeWithSuffix(t, db, "clear-conflict-other-worker")
	groupA := createGroupViaAPI(t, r, adminToken, "clear-owner-a")
	groupB := createGroupViaAPI(t, r, adminToken, "clear-owner-b")
	load := func(nodeID, groupID uint, name string) model.Instance {
		id := makeInstanceInGroup(t, db, nodeID, groupID, name, model.InstanceStatusStopped)
		var inst model.Instance
		require.NoError(t, db.First(&inst, id).Error)
		return inst
	}
	instA := load(node.ID, groupA, "clear-owned")
	instB := load(node.ID, groupB, "clear-other-group")
	other := load(otherNode.ID, groupA, "clear-other-node")
	var unassigned model.Instance
	unassigned.NodeID, unassigned.Name = node.ID, "clear-unassigned"
	require.NoError(t, db.Create(&unassigned).Error)

	userToken := gateUserToken(t, r, db, service.NewAuthzService(db), service.NewUserService(db),
		"clear-node-manager", []string{"node.manage"}, groupA)

	tests := []struct {
		name   string
		key    string
		token  string
		status int
	}{
		{"same_group_file", sourceKeyOf(instA, node, "file"), userToken, http.StatusOK},
		{"same_group_stderr", sourceKeyOf(instA, node, "stderr"), userToken, http.StatusOK},
		{"cross_group", sourceKeyOf(instB, node, "file"), userToken, http.StatusForbidden},
		{"unassigned", sourceKeyOf(unassigned, node, "file"), userToken, http.StatusForbidden},
		{"wrong_node", sourceKeyOf(other, otherNode, "file"), userToken, http.StatusForbidden},
		{"wrong_generation", fmt.Sprintf("inst:%d/file/instance:unknown@worker:%s", instA.ID, node.UUID), userToken, http.StatusForbidden},
		{"bare_source", fmt.Sprintf("inst:%d/file", instA.ID), adminToken, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &recordingClearConflictsWorker{}
			pool.SetWorkerClientForTest(node.UUID, stub)
			resp := makeRequest(r, http.MethodPost, clearConflictsEndpoint(node.ID),
				map[string]any{"storageNamespace": tt.key, "reason": "已核对", "operator": "forged-admin"}, tt.token)
			require.Equal(t, tt.status, resp.Code, resp.Body.String())
			if tt.status != http.StatusOK {
				require.Zero(t, stub.calls, "拒绝请求不能触发清空 RPC")
				return
			}
			calls, _, operator, _, request := stub.snapshot()
			require.Equal(t, 1, calls)
			require.Equal(t, "clear-node-manager", operator, "伪造的请求体 operator 必须无效")
			require.Equal(t, tt.key, request.GetStorageNamespace(), "授权后必须原样保留完整源键")
		})
	}
}

// TestLogClearIdentityConflictsEmptyKeyRequiresPlatformAdmin：空源键是**批量**动作，
// 组管理员（仅持有 node.manage）不得触发；平台管理员可以，且仍带操作人与理由。
//
// 转红方式（实测）：把 authorizeClearAllSources 整个去掉（沿用节点级权限）——第一个子用例
// 会从 403 变成 200 且 RPC 次数为 1，本用例红。
func TestLogClearIdentityConflictsEmptyKeyRequiresPlatformAdmin(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, adminToken, "clear-all-owner")
	makeInstanceInGroup(t, db, node.ID, groupA, "clear-all-source", model.InstanceStatusStopped)
	userToken := gateUserToken(t, r, db, service.NewAuthzService(db), service.NewUserService(db),
		"clear-all-node-manager", []string{"node.manage"}, groupA)

	// 组管理员 + 空源键：拒绝，且不下发 RPC。
	stub := &recordingClearConflictsWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)
	resp := makeRequest(r, http.MethodPost, clearConflictsEndpoint(node.ID),
		map[string]any{"reason": "想一次清掉全部"}, userToken)
	require.Equal(t, http.StatusForbidden, resp.Code, resp.Body.String())
	require.Contains(t, resp.Body.String(), "平台管理员", "错误必须可执行：说清需要什么权限/如何改")
	require.Zero(t, stub.calls, "越权请求不得触发清空 RPC")

	// 平台管理员 + 空源键：放行，且操作人与理由照旧经 metadata 下发。
	stub = &recordingClearConflictsWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)
	resp = makeRequest(r, http.MethodPost, clearConflictsEndpoint(node.ID),
		map[string]any{"reason": "全节点已核对"}, adminToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	calls, _, operator, reason, request := stub.snapshot()
	require.Equal(t, 1, calls)
	require.Equal(t, "", request.GetStorageNamespace(), "空源键表示全部源")
	require.Equal(t, "admin", operator)
	require.Equal(t, "全节点已核对", reason)
}

// TestLogClearIdentityConflictsWorkerFailureIsNotReportedAsSuccess：Worker 报错必须原样转述，
// 不得回 200（否则运维会以为标记已清、实际什么都没做）。
func TestLogClearIdentityConflictsWorkerFailureIsNotReportedAsSuccess(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, adminToken, "clear-fail-owner")
	id := makeInstanceInGroup(t, db, node.ID, groupA, "clear-fail-source", model.InstanceStatusStopped)
	var inst model.Instance
	require.NoError(t, db.First(&inst, id).Error)

	pool.SetWorkerClientForTest(node.UUID, &failingClearConflictsWorker{
		message: "ingest: 采集轮忙（cycleMu 等待超 5s 预算），清空身份冲突未写入任何变更，请稍后重试"})
	resp := makeRequest(r, http.MethodPost, clearConflictsEndpoint(node.ID),
		map[string]any{"storageNamespace": sourceKeyOf(inst, node, "file"), "reason": "已核对"}, adminToken)

	require.Equal(t, http.StatusConflict, resp.Code, resp.Body.String())
	require.Contains(t, resp.Body.String(), "CLEAR_CONFLICTS_FAILED")
	require.Contains(t, resp.Body.String(), "采集轮忙", "必须转述 Worker 的具体原因（否则运维只能盲猜）")
}

type failingClearConflictsWorker struct {
	workerpb.WorkerServiceClient
	message string
}

func (w *failingClearConflictsWorker) LogClearIdentityConflicts(context.Context, *workerpb.LogClearIdentityConflictsRequest, ...grpc.CallOption) (*workerpb.LogClearIdentityConflictsResponse, error) {
	return &workerpb.LogClearIdentityConflictsResponse{
		Error: &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_NOT_READY, Message: w.message},
	}, nil
}
