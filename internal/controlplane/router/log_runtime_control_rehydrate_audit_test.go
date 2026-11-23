package router

// 「日志运行时控制」与「归档恢复」两个端点的操作人/审计/权限纪律回归。
//
// 本文件针对同族剩下的两个缺口（migrate / resolve-gaps / clear-identity-conflicts /
// import-archives 已补齐）：
//  1. Control（POST /nodes/:id/log-runtime/:namespace/:action）此前既不解析操作人、不落
//     审计，也只在路由门上挂 node.manage——启停 VL 运行时会直接中断该节点日志的查询面与
//     写入面，属于有副作用的写操作，事后必须能回答「谁停的、停的哪一层」；
//  2. Rehydrate（POST /nodes/:id/log-archive/rehydrate）此前已有目标级授权，但不解析
//     操作人、不落审计——它会把归档数据重新写进查询面，属高影响动作。
//
// 每个用例末尾都注明「转红方式」，便于变异验证时不靠猜。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// recordingLogRuntimeControlWorker 记录 CP 转发的运行时控制请求（与同族端点桩同型）。
type recordingLogRuntimeControlWorker struct {
	workerpb.WorkerServiceClient
	calls    int
	request  *workerpb.LogRuntimeControlRequest
	failWith *workerpb.LogError
}

func (w *recordingLogRuntimeControlWorker) LogRuntimeControl(_ context.Context, in *workerpb.LogRuntimeControlRequest, _ ...grpc.CallOption) (*workerpb.LogTaskResponse, error) {
	w.calls++
	w.request = in
	if w.failWith != nil {
		return &workerpb.LogTaskResponse{
			TaskId: in.GetRequestId(),
			State:  workerpb.LogTaskState_LOG_TASK_FAILED,
			Error:  w.failWith,
		}, nil
	}
	return &workerpb.LogTaskResponse{TaskId: in.GetRequestId(), State: workerpb.LogTaskState_LOG_TASK_SUCCEEDED}, nil
}

// recordingLogRehydrateWorker 记录归档恢复请求。
type recordingLogRehydrateWorker struct {
	workerpb.WorkerServiceClient
	calls    int
	request  *workerpb.LogRehydrateRequest
	failWith *workerpb.LogError
}

func (w *recordingLogRehydrateWorker) LogRehydrate(_ context.Context, in *workerpb.LogRehydrateRequest, _ ...grpc.CallOption) (*workerpb.LogTaskResponse, error) {
	w.calls++
	w.request = in
	if w.failWith != nil {
		return &workerpb.LogTaskResponse{
			TaskId: in.GetQuery().GetRequestId(),
			State:  workerpb.LogTaskState_LOG_TASK_FAILED,
			Error:  w.failWith,
		}, nil
	}
	return &workerpb.LogTaskResponse{
		TaskId: in.GetQuery().GetRequestId(), State: workerpb.LogTaskState_LOG_TASK_SUCCEEDED,
	}, nil
}

// controlEndpoint 构造运行时控制端点的完整路径（前端 NodeLogRuntimePanel 的实际调用形态）。
func controlEndpoint(nodeID uint, namespace, action string) string {
	return fmt.Sprintf("/api/v1/nodes/%d/log-runtime/%s/%s", nodeID, namespace, action)
}

// rehydrateEndpoint 构造归档恢复端点的完整路径。
func rehydrateEndpoint(nodeID uint) string {
	return fmt.Sprintf("/api/v1/nodes/%d/log-archive/rehydrate", nodeID)
}

// TestLogRuntimeControlRejectsMissingOperator：取不到认证主体必须在 CP 就拒绝，且不下发 RPC。
//
// 为什么必须这样：Worker 侧 LogRuntimeControl 不消费操作人，CP 不解析就真的没有任何主体
// 记录——一次误停在生产上只能靠这条凭据回答「谁让它停的」。
//
// 转红方式：删掉 handler 里的 operatorFromRequest 拒绝分支——本用例在「403 / OPERATOR_UNKNOWN
// / 零 RPC」处变红。
func TestLogRuntimeControlRejectsMissingOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	node := createTestNode(t, db)
	stub := &recordingLogRuntimeControlWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)

	handler := NewLogRuntimeHandler(service.NewNodeService(db), pool, service.NewAuthzService(db),
		service.NewInstanceService(db, service.NewGroupService(db), pool))
	engine := gin.New()
	engine.POST("/api/v1/nodes/:id/log-runtime/:namespace/:action", handler.Control)

	resp := makeRequest(engine, http.MethodPost, controlEndpoint(node.ID, "hot", "restart"), nil, "")
	require.Equal(t, http.StatusForbidden, resp.Code, resp.Body.String())
	require.Contains(t, resp.Body.String(), "OPERATOR_UNKNOWN")
	require.Zero(t, stub.calls, "取不到主体时不得下发任何 RPC")
}

// TestLogRuntimeControlRequiresNodeManagePerm：handler 自身的节点级权限校验必须成立。
//
// 为什么在 handler 里再判一次：路由门已挂 node.manage，但写操作不该只依赖挂载点——本用例
// 绕过路由中间件、直接以「只有 node.read 的调用方」调 handler，确保 handler 自己就是一道门。
// 这也正是本端点唯一的越权面：:namespace 只有 hot/cold/rehydrate 三种取值，是「动这个节点上
// 的哪个日志库进程」，不携带 node:/inst: 前缀，因此没有实例级授权可做（详见 Control 的注释）。
//
// 转红方式：删掉 handler 里的 requireNodes——本用例从 403 变成 202 且 RPC 次数为 1。
func TestLogRuntimeControlRequiresNodeManagePerm(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	node := createTestNode(t, db)
	stub := &recordingLogRuntimeControlWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)

	handler := NewLogRuntimeHandler(service.NewNodeService(db), pool, service.NewAuthzService(db),
		service.NewInstanceService(db, service.NewGroupService(db), pool))
	engine := gin.New()
	// 只注入 node.read：模拟「自定义角色拿到该节点读权限，但没有 node.manage」。
	engine.Use(func(c *gin.Context) {
		c.Set(middleware.CtxAccess, &service.UserAccess{
			UserID: 9, IsPlatformAdmin: false,
			Nodes:          map[string]struct{}{"node.read": {}},
			MemberGroupIDs: map[uint]struct{}{}, AdminGroupIDs: map[uint]struct{}{},
			AccessibleGroups: map[uint]struct{}{},
		})
		c.Set(middleware.CtxUsername, "node-reader")
		c.Next()
	})
	engine.POST("/api/v1/nodes/:id/log-runtime/:namespace/:action", handler.Control)

	resp := makeRequest(engine, http.MethodPost, controlEndpoint(node.ID, "hot", "stop"), nil, "")
	require.Equal(t, http.StatusForbidden, resp.Code, resp.Body.String())
	require.Zero(t, stub.calls, "无 node.manage 时不得下发任何 RPC")
}

// TestLogRuntimeControlRecordsAudit：成功受理必须在 CP 侧落一条审计行（谁/哪个节点/
// 哪个命名空间/哪个动作/结果状态），Worker 报错时不得留下成功审计。
//
// 为什么必须落 CP 侧：审计中间件对 POST /nodes/:id/log-runtime/... 不产生 action，
// Worker 侧该 RPC 既不校验也不记录操作人——启停 VL 直接中断日志查询面与写入面，
// 不留行就等于这次停机从未发生。
//
// 转红方式：删掉 handler 末尾的 recordAudit——本用例在「必须有一条运行时控制审计」处变红。
func TestLogRuntimeControlRecordsAudit(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)

	stub := &recordingLogRuntimeControlWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)
	resp := makeRequest(r, http.MethodPost, controlEndpoint(node.ID, "cold", "restart"), nil, adminToken)
	// 先确认真实路径已被触发：否则「有审计行」可能只是桩没走到。
	require.Equal(t, http.StatusAccepted, resp.Code, resp.Body.String())
	require.Equal(t, 1, stub.calls)
	require.Equal(t, "cold", stub.request.GetNamespace(), "命名空间必须原样下发")
	require.Equal(t, "restart", stub.request.GetAction(), "动作必须原样下发")

	var audits []model.AuditLog
	require.NoError(t, db.Where("action = ?", "node.log_runtime.control").Find(&audits).Error)
	require.Len(t, audits, 1, "成功受理必须落且只落一条审计")
	audit := audits[0]
	require.NotZero(t, audit.UserID, "操作人必须来自认证上下文")
	require.Equal(t, "node", audit.TargetType)
	require.Equal(t, fmt.Sprintf("%d", node.ID), audit.TargetID, "审计目标必须是本次操作的节点")
	require.False(t, audit.Failed)
	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(audit.Detail), &detail), "detail 应为 JSON：%s", audit.Detail)
	require.Equal(t, "cold", detail["namespace"], "detail 必须记录被操作的运行时命名空间")
	require.Equal(t, "restart", detail["action"], "detail 必须记录动作")
	require.Equal(t, "admin", detail["operator"], "可读主体必须入 detail（Agent 通道的 user_id 是 token id）")
	require.Equal(t, workerpb.LogTaskState_LOG_TASK_SUCCEEDED.String(), detail["state"], "detail 必须记录结果状态")

	// Worker 报错：不得留下成功审计（否则审计会证明一次从未发生的启停）。
	pool.SetWorkerClientForTest(node.UUID, &recordingLogRuntimeControlWorker{failWith: &workerpb.LogError{
		Code: workerpb.LogErrorCode_LOG_NOT_READY, Message: "victorialogs supervisor is not running",
	}})
	resp = makeRequest(r, http.MethodPost, controlEndpoint(node.ID, "hot", "start"), nil, adminToken)
	require.Equal(t, http.StatusServiceUnavailable, resp.Code, resp.Body.String())
	require.NoError(t, db.Where("action = ?", "node.log_runtime.control").Find(&audits).Error)
	require.Len(t, audits, 1, "Worker 报错时不得新增成功审计")
}

// TestLogArchiveRehydrateRejectsMissingOperator：归档恢复同样要求可审计的操作人。
//
// 为什么：它把冷层归档重新写进查询面（读取 + 校验 + 发布归档事件），属高影响动作，
// 而 Worker 侧 LogRehydrate 不消费操作人——CP 不解析就全程无痕。
//
// 转红方式：删掉 handler 里的 operatorFromRequest 拒绝分支——本用例在「403 / 零 RPC」处变红。
func TestLogArchiveRehydrateRejectsMissingOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	node := createTestNode(t, db)
	stub := &recordingLogRehydrateWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)

	handler := NewLogRuntimeHandler(service.NewNodeService(db), pool, service.NewAuthzService(db),
		service.NewInstanceService(db, service.NewGroupService(db), pool))
	engine := gin.New()
	engine.POST("/api/v1/nodes/:id/log-archive/rehydrate", handler.Rehydrate)

	resp := makeRequest(engine, http.MethodPost, rehydrateEndpoint(node.ID),
		map[string]any{"archiveObjectIds": []string{"obj-1"}}, "")
	require.Equal(t, http.StatusForbidden, resp.Code, resp.Body.String())
	require.Contains(t, resp.Body.String(), "OPERATOR_UNKNOWN")
	require.Zero(t, stub.calls, "取不到主体时不得下发任何 RPC")
}

// TestLogArchiveRehydrateRecordsAudit：成功受理必须落 CP 侧审计行（谁/对哪个目标/
// 恢复了哪批对象/结果状态），Worker 报错时不落。
//
// 为什么必须落 CP 侧：审计中间件对本路径不产生 action，Worker 侧也不消费操作人；
// 「谁把哪批归档放回了可查范围」只能由 CP 这一行回答。
//
// 转红方式：删掉 handler 末尾的 recordAudit——本用例在「必须有一条归档恢复审计」处变红。
func TestLogArchiveRehydrateRecordsAudit(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)

	stub := &recordingLogRehydrateWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)
	// target 留空表示「本节点自身」（既有语义），审计里必须归一成 node:<id> 而不是空串。
	resp := makeRequest(r, http.MethodPost, rehydrateEndpoint(node.ID), map[string]any{
		"archiveObjectIds": []string{"obj-1", "obj-2"},
		"fromUtc":          "2026-10-01T00:00:00Z",
		"toUtc":            "2026-10-02T00:00:00Z",
	}, adminToken)
	require.Equal(t, http.StatusAccepted, resp.Code, resp.Body.String())
	require.Equal(t, 1, stub.calls)
	require.Len(t, stub.request.GetArchiveObjectIds(), 2, "归档对象 ID 必须原样下发")

	var audits []model.AuditLog
	require.NoError(t, db.Where("action = ?", "node.log_archive.rehydrate").Find(&audits).Error)
	require.Len(t, audits, 1, "成功受理必须落且只落一条审计")
	audit := audits[0]
	require.NotZero(t, audit.UserID, "操作人必须来自认证上下文")
	require.Equal(t, "node", audit.TargetType)
	require.Equal(t, fmt.Sprintf("%d", node.ID), audit.TargetID)
	require.False(t, audit.Failed)
	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(audit.Detail), &detail), "detail 应为 JSON：%s", audit.Detail)
	require.Equal(t, fmt.Sprintf("node:%d", node.ID), detail["target"], "空 target 必须归一成本节点自身")
	require.Equal(t, []any{"obj-1", "obj-2"}, detail["archiveObjectIds"], "必须记录恢复了哪批对象")
	require.Equal(t, "2026-10-01T00:00:00Z", detail["fromUtc"])
	require.Equal(t, "2026-10-02T00:00:00Z", detail["toUtc"])
	require.Equal(t, "admin", detail["operator"])
	require.Equal(t, workerpb.LogTaskState_LOG_TASK_SUCCEEDED.String(), detail["state"])

	// Worker 报错：不得留下成功审计。
	pool.SetWorkerClientForTest(node.UUID, &recordingLogRehydrateWorker{failWith: &workerpb.LogError{
		Code: workerpb.LogErrorCode_LOG_NOT_READY, Message: "archive object is not cold-resident",
	}})
	resp = makeRequest(r, http.MethodPost, rehydrateEndpoint(node.ID),
		map[string]any{"archiveObjectIds": []string{"obj-3"}}, adminToken)
	require.Equal(t, http.StatusServiceUnavailable, resp.Code, resp.Body.String())
	require.NoError(t, db.Where("action = ?", "node.log_archive.rehydrate").Find(&audits).Error)
	require.Len(t, audits, 1, "Worker 报错时不得新增成功审计")
}
