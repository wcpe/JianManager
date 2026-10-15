package router

// 「手动触发归档导入」端点的 CP 侧回归（FR-504）。
//
// 为什么本端点值得单独守：Worker 侧的 gRPC `LogImportArchives` 早已存在且强制校验操作人，
// 但 CP 与前端此前**都没有入口** —— 「手动触发导入」在运维面上实际不可达，是生产
// 「814 个 gz 从未被导入」的第三块拼图。本文件锁住这条通路以及它的四条纪律：
//  1. 操作人取自**认证上下文**、经传输 metadata 下传（请求体伪造无效）；
//  2. 理由必填，且同样经 metadata 下传（审计凭据要记录"为什么可以导"）；
//  3. 指名源须先完成对象授权，拒绝时**不下发任何 RPC**；
//  4. 不传源键 = 批量动作，只允许平台管理员；Worker 报错不得伪装成成功；
//  5. 成功触发后在 CP 侧落一条审计行（含理由与源命名空间），Worker 报错时不落。

import (
	"context"
	"encoding/json"
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

// recordingImportArchivesWorker 记录 CP 转发的 metadata 与请求体（与同族端点桩同型）。
type recordingImportArchivesWorker struct {
	workerpb.WorkerServiceClient
	calls    int
	hasMD    bool
	operator string
	reason   string
	request  *workerpb.LogImportArchivesRequest
	failWith *workerpb.LogError
}

func (w *recordingImportArchivesWorker) LogImportArchives(ctx context.Context, in *workerpb.LogImportArchivesRequest, _ ...grpc.CallOption) (*workerpb.LogTaskResponse, error) {
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
	return &workerpb.LogTaskResponse{
		TaskId: in.GetRequestId(),
		State:  workerpb.LogTaskState_LOG_TASK_SUCCEEDED,
		Error:  w.failWith,
	}, nil
}

// importArchivesEndpoint 构造本端点的完整路径。
func importArchivesEndpoint(nodeID uint) string {
	return fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/import-archives", nodeID)
}

// TestLogImportArchivesForwardsOperatorAndReason：单源路径必须把认证主体与理由经
// `x-jm-operator` / `x-jm-reason` 下传，并把源键原样带给 Worker。
//
// 转红方式：去掉 handler 里 metadata.Pairs(...) 的注入——桩读到空操作人，本用例红。
func TestLogImportArchivesForwardsOperatorAndReason(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	inst := model.Instance{NodeID: node.ID, Name: "archive-import-source"}
	require.NoError(t, db.Create(&inst).Error)
	// 语义是**实例存储命名空间**（不是完整源键）：Worker 侧 ImportArchivesNow 的过滤用的
	// 就是它；传完整源键不会匹配到任何源（实测 scanned=0），见 authorizeImportNamespace。
	sourceKey := fmt.Sprintf("inst:%d", inst.ID)
	stub := &recordingImportArchivesWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)

	resp := makeRequest(r, http.MethodPost, importArchivesEndpoint(node.ID),
		map[string]any{"storageNamespace": sourceKey, "reason": "补历史归档"}, adminToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())

	require.Equal(t, 1, stub.calls)
	require.True(t, stub.hasMD, "CP 必须经 outgoing metadata 下达操作人")
	require.NotEmpty(t, stub.operator, "操作人必须来自认证上下文")
	require.Equal(t, "补历史归档", stub.reason, "理由必须经 metadata 下传（进审计凭据）")
	require.Equal(t, sourceKey, stub.request.GetStorageNamespace(), "源键必须原样下传")
}

// TestLogImportArchivesRejectsMissingOperator：取不到认证主体时必须在 CP 就拒绝，且不下发 RPC。
func TestLogImportArchivesRejectsMissingOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	node := createTestNode(t, db)
	stub := &recordingImportArchivesWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)

	// 刻意不走认证中间件：模拟「认证主体缺失」的调用面。
	handler := NewLogRuntimeHandler(service.NewNodeService(db), pool, service.NewAuthzService(db),
		service.NewInstanceService(db, service.NewGroupService(db), pool))
	engine := gin.New()
	engine.POST("/api/v1/nodes/:id/log-runtime/ingest/import-archives", handler.ImportArchives)

	resp := makeRequest(engine, http.MethodPost, importArchivesEndpoint(node.ID),
		map[string]any{"storageNamespace": "inst:9/file", "reason": "已核对"}, "")
	require.Equal(t, http.StatusForbidden, resp.Code, resp.Body.String())
	require.Contains(t, resp.Body.String(), "OPERATOR_UNKNOWN")
	require.Zero(t, stub.calls, "取不到主体时不得下发任何 RPC")
}

// TestLogImportArchivesRequiresReason：理由缺失必须在 CP 就拒绝，且不下发 RPC。
func TestLogImportArchivesRequiresReason(t *testing.T) {
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
		stub := &recordingImportArchivesWorker{}
		pool.SetWorkerClientForTest(node.UUID, stub)
		resp := makeRequest(r, http.MethodPost, importArchivesEndpoint(node.ID), body, adminToken)
		require.Equal(t, http.StatusBadRequest, resp.Code, "body=%v resp=%s", body, resp.Body.String())
		require.Contains(t, resp.Body.String(), "REASON_REQUIRED")
		require.Zero(t, stub.calls, "理由缺失时不得下发 RPC")
	}
}

// TestLogImportArchivesEmptyKeyRequiresPlatformAdmin：不传源键（全 Worker 范围）是批量动作，
// 只允许平台管理员；平台管理员放行时操作人与理由照旧经 metadata 下发。
func TestLogImportArchivesEmptyKeyRequiresPlatformAdmin(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, adminToken, "archive-import-owner")
	makeInstanceInGroup(t, db, node.ID, groupA, "archive-import-all", model.InstanceStatusStopped)
	userToken := gateUserToken(t, r, db, service.NewAuthzService(db), service.NewUserService(db),
		"archive-import-node-manager", []string{"node.manage"}, groupA)

	// 组管理员 + 空源键：拒绝，且不下发 RPC。
	stub := &recordingImportArchivesWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)
	resp := makeRequest(r, http.MethodPost, importArchivesEndpoint(node.ID),
		map[string]any{"reason": "想一次导全部"}, userToken)
	require.Equal(t, http.StatusForbidden, resp.Code, resp.Body.String())
	require.Contains(t, resp.Body.String(), "平台管理员", "错误必须可执行：说清需要什么权限/如何改")
	require.Zero(t, stub.calls, "越权请求不得触发导入 RPC")

	// 平台管理员 + 空源键：放行，字段照旧下发。
	stub = &recordingImportArchivesWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)
	resp = makeRequest(r, http.MethodPost, importArchivesEndpoint(node.ID),
		map[string]any{"reason": "全节点补历史"}, adminToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, stub.calls)
	require.Equal(t, "", stub.request.GetStorageNamespace(), "空源键表示全部源")
	require.Equal(t, "admin", stub.operator)
	require.Equal(t, "全节点补历史", stub.reason)
}

// TestLogImportArchivesRecordsAuditWithReason：成功触发后必须在 CP 侧落一条审计行，
// 含操作人、目标节点、理由与存储命名空间（FR-504 的「理由进审计凭据」）。
//
// 为什么必须落 CP 侧：审计中间件对本路径不产生 action
// （POST /nodes/:id/log-runtime/... 不匹配任何分支），Worker 侧只有一条会轮转的本地
// slog 且不读理由——CP 侧不落行就等于「谁导了、为何可以导」两者都没有凭据，
// 而手工导入正是跨过「gz 自动导入默认关」的应急动作。
//
// 转红方式：删掉 handler 末尾的 recordAudit 调用——本用例在「必须有一条审计行」处变红。
func TestLogImportArchivesRecordsAuditWithReason(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	inst := model.Instance{NodeID: node.ID, Name: "audit-import-source"}
	require.NoError(t, db.Create(&inst).Error)
	sourceKey := fmt.Sprintf("inst:%d", inst.ID)
	stub := &recordingImportArchivesWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)

	resp := makeRequest(r, http.MethodPost, importArchivesEndpoint(node.ID),
		map[string]any{"storageNamespace": sourceKey, "reason": "补 814 个 gz 的历史归档"}, adminToken)
	// 先确认真实路径已被触发：否则「有审计行」可能只是桩没走到。
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, stub.calls)

	var audits []model.AuditLog
	require.NoError(t, db.Where("action = ?", "node.log_archive.import").Find(&audits).Error)
	require.Len(t, audits, 1, "成功触发必须落且只落一条导入审计")
	audit := audits[0]
	require.NotZero(t, audit.UserID, "操作人必须来自认证上下文")
	require.Equal(t, "node", audit.TargetType)
	require.Equal(t, fmt.Sprintf("%d", node.ID), audit.TargetID, "审计目标必须是本次操作的节点")
	require.False(t, audit.Failed)
	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(audit.Detail), &detail), "detail 应为 JSON：%s", audit.Detail)
	require.Equal(t, sourceKey, detail["storageNamespace"], "detail 必须记录源命名空间")
	require.Equal(t, "补 814 个 gz 的历史归档", detail["reason"], "detail 必须记录理由")
}

// TestLogImportArchivesWorkerFailureWritesNoAudit：Worker 报错时不得写成功审计
// （否则审计会证明一个从未发生的导入，比没有审计更糟）。
func TestLogImportArchivesWorkerFailureWritesNoAudit(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, adminToken, "audit-import-fail-owner")
	id := makeInstanceInGroup(t, db, node.ID, groupA, "audit-import-fail", model.InstanceStatusStopped)
	var inst model.Instance
	require.NoError(t, db.First(&inst, id).Error)

	stub := &recordingImportArchivesWorker{failWith: &workerpb.LogError{
		Code: workerpb.LogErrorCode_LOG_NOT_READY, Message: "采集轮忙",
	}}
	pool.SetWorkerClientForTest(node.UUID, stub)
	resp := makeRequest(r, http.MethodPost, importArchivesEndpoint(node.ID),
		map[string]any{"storageNamespace": fmt.Sprintf("inst:%d", inst.ID), "reason": "补历史"}, adminToken)
	require.Equal(t, http.StatusConflict, resp.Code, resp.Body.String())

	var audits []model.AuditLog
	require.NoError(t, db.Where("action = ?", "node.log_archive.import").Find(&audits).Error)
	require.Empty(t, audits, "Worker 报错时不得留下成功审计（审计只能证明真的触发过）")
}

// TestLogImportArchivesWorkerFailureIsNotReportedAsSuccess：Worker 报错必须原样转述，
// 不得回 200（否则运维会以为已导入、实际什么都没做）。
func TestLogImportArchivesWorkerFailureIsNotReportedAsSuccess(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, adminToken, "archive-import-fail-owner")
	id := makeInstanceInGroup(t, db, node.ID, groupA, "archive-import-fail", model.InstanceStatusStopped)
	var inst model.Instance
	require.NoError(t, db.First(&inst, id).Error)

	stub := &recordingImportArchivesWorker{failWith: &workerpb.LogError{
		Code:    workerpb.LogErrorCode_LOG_NOT_READY,
		Message: "ingest: 采集轮忙（cycleMu 等待超 5s 预算），归档扫描未执行任何变更，请稍后重试",
	}}
	pool.SetWorkerClientForTest(node.UUID, stub)
	resp := makeRequest(r, http.MethodPost, importArchivesEndpoint(node.ID),
		map[string]any{"storageNamespace": fmt.Sprintf("inst:%d", inst.ID), "reason": "补历史"}, adminToken)

	require.Equal(t, http.StatusConflict, resp.Code, resp.Body.String())
	require.Contains(t, resp.Body.String(), "IMPORT_ARCHIVES_FAILED")
	require.Contains(t, resp.Body.String(), "采集轮忙", "必须转述 Worker 的具体原因（否则运维只能盲猜）")
}
