package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 本文件是 2026-10-02 现场缺陷的回归：**放弃裁定经产品面不可达**。
//
// 现场形态：实例重启产生的 DELIVER_ERROR 未决缺口会**永久阻塞该源恢复采集**
// （`ledger: unresolved gaps still block acquisition`，设计如此），唯一解阻手段是带
// `x-jm-operator` 的人工放弃裁定（ADR-101）；Worker 侧「取不到操作人一律拒绝」也已实现，
// 但 CP 代理该端点时**不透传任何操作人 metadata** ⇒ 经产品面永远取不到操作人 ⇒ 5 个源
// 停在暂停态，运维只能绕过产品面直连 Worker。
//
// 三条断言（各可转红，见各用例注释）：
//  1. 转发必须携带 `x-jm-operator`（值 = 认证主体）；
//  2. 取不到主体 → 拒绝调用并给出可执行错误（不得把请求下发给 Worker 换一句不可执行的提示）；
//  3. 端到端：带主体的裁定经产品面（真 Setup 路由 + 真 JWT 认证链）成功返回。

// recordingResolveGapsWorker 记录 CP 转发的 metadata 与请求体。
//
// 采用「嵌入 nil 接口 + 只实现被调用方法」的桩形态（与包内既有假 Worker 同型）：
// 未实现的方法在本用例中不会被触达，因此不需要为整个 WorkerService 写桩。
type recordingResolveGapsWorker struct {
	workerpb.WorkerServiceClient
	mu       sync.Mutex
	calls    int
	hasMD    bool
	operator string
	request  *workerpb.LogResolveIngestGapsRequest
}

func (w *recordingResolveGapsWorker) LogResolveIngestGaps(ctx context.Context, in *workerpb.LogResolveIngestGapsRequest, _ ...grpc.CallOption) (*workerpb.LogTaskResponse, error) {
	md, ok := metadata.FromOutgoingContext(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	w.hasMD = ok
	if values := md.Get(operatorMetadataKey); len(values) > 0 {
		w.operator = values[0]
	}
	w.request = in
	return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_SUCCEEDED}, nil
}

type resolveGapsCall struct {
	calls    int
	hasMD    bool
	operator string
	request  *workerpb.LogResolveIngestGapsRequest
}

func (w *recordingResolveGapsWorker) snapshot() resolveGapsCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return resolveGapsCall{calls: w.calls, hasMD: w.hasMD, operator: w.operator, request: w.request}
}

// setupLogRuntimeOperatorRouter 装配**真实** Setup 路由（含 JWT 认证 + 权限中间件），
// 只注入本用例所需的少量服务与连接池——这样断言的链路就是生产链路，而不是手工挂 handler。
func setupLogRuntimeOperatorRouter(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool) *gin.Engine {
	t.Helper()
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-log-operator", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	groupSvc := service.NewGroupService(db)
	authzSvc := service.NewAuthzService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	instanceSvc.SetDelegateBypassForTest(true)
	nodeSvc := service.NewNodeService(db)
	nodeSvc.SetInstanceService(instanceSvc)
	return Setup(&Services{
		Auth:           service.NewAuthService(db, jwtCfg),
		User:           service.NewUserService(db),
		Group:          groupSvc,
		Node:           nodeSvc,
		Instance:       instanceSvc,
		Audit:          service.NewAuditService(db),
		Authz:          authzSvc,
		LogRuntimePool: pool,
	}, jwtCfg.Secret)
}

// TestLogResolveIngestGapsForwardsOperatorMetadata：放弃裁定路径（带 storageNamespace）
// 必须把认证主体作为 `x-jm-operator` 下传给 Worker。
//
// 转红方式（实测）：去掉 handler 里的 metadata 注入——桩读到空操作人，本用例在
// 「必须携带操作人」处变红。
func TestLogResolveIngestGapsForwardsOperatorMetadata(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	stub := &recordingResolveGapsWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)

	resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/resolve-gaps", node.ID),
		map[string]any{"storageNamespace": "inst:9/stderr"}, adminToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())

	call := stub.snapshot()
	require.Equal(t, 1, call.calls)
	require.True(t, call.hasMD, "CP 必须经 outgoing metadata 下达操作人")
	require.Equal(t, "admin", call.operator, "操作人取自认证主体（登录名），不是请求体")
	require.NotNil(t, call.request)
	require.Equal(t, "inst:9/stderr", call.request.GetStorageNamespace())
}

// TestLogResolveIngestGapsWholeNodeCarriesOperator：同一端点的**整节点**形态（不传
// storageNamespace）也必须携带操作人——「一处不漏」：Worker 侧的放弃裁定分支由请求体决定，
// 而操作人由传输层决定，两者不得各改一半。
//
// 转红方式（实测）：同①，去掉注入即红。
func TestLogResolveIngestGapsWholeNodeCarriesOperator(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	stub := &recordingResolveGapsWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)

	resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/resolve-gaps", node.ID), nil, adminToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())

	call := stub.snapshot()
	require.Equal(t, 1, call.calls)
	require.Equal(t, "admin", call.operator)
	require.NotNil(t, call.request)
	require.Equal(t, "", call.request.GetStorageNamespace())
}

// TestLogResolveIngestGapsRejectsMissingOperator：取不到认证主体 → 拒绝，且**不下发 RPC**。
//
// 为什么必须在这里拒：Worker 侧同样取不到操作人时只会回一句「请在 incoming metadata 设置
// x-jm-operator」——对 CP 调用方不可执行（它没法设置 Worker 的入站 metadata）。CP 必须给出
// 调用方能照做的提示，并在本地就拒绝，避免在 Worker 侧留下无谓的拒绝记录。
//
// 转红方式（实测）：把取不到主体时的拒绝改成继续转发——本用例在「403 / 零 RPC」处变红。
func TestLogResolveIngestGapsRejectsMissingOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	node := createTestNode(t, db)
	stub := &recordingResolveGapsWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)

	// 刻意不走认证中间件：模拟「认证主体缺失」的调用面（如未注入 username 的自定义认证通道）。
	handler := NewLogRuntimeHandler(service.NewNodeService(db), pool, service.NewAuthzService(db),
		service.NewInstanceService(db, service.NewGroupService(db), pool))
	engine := gin.New()
	engine.POST("/api/v1/nodes/:id/log-runtime/ingest/resolve-gaps", handler.ResolveIngestGaps)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/resolve-gaps", node.ID),
		strings.NewReader(`{"storageNamespace":"inst:9/stderr"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "OPERATOR_UNKNOWN")
	require.Contains(t, rec.Body.String(), "认证主体", "错误必须可执行：说清缺什么")
	require.Zero(t, stub.snapshot().calls, "取不到主体时不得下发任何 RPC")
}
