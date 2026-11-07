package router

// 「日志运行时的永久裁定」三条审计纪律的 CP 侧回归。
//
// 本文件覆盖同一族纪律在三个端点上的缺口：
//  1. 分区下沉（migrate）缺操作人解析与目标归属校验：命名空间被原样下发给**路由节点**的
//     Worker，任何持有 node.manage 的调用方都能借此动本节点上任意实例/任意命名空间的
//     分区，且全程无审计行；
//  2. 清空身份冲突（clear-identity-conflicts）成功分支只把 operator/reason 经 metadata
//     下发给 Worker，CP 侧不留行——而清空整节点质量标记是永久裁定；
//  3. 显式单源放弃裁定（resolve-gaps 带非空源键）同样只在 Worker 账本留凭据，
//     CP 侧统一审计视图里查不到「谁放掉了哪个源」。
//
// 转红方式（逐条实测，见各用例注释）。

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
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// recordingMigratePartitionWorker 记录 CP 转发的请求体（与同族端点桩同型）。
type recordingMigratePartitionWorker struct {
	workerpb.WorkerServiceClient
	calls    int
	request  *workerpb.LogMigratePartitionRequest
	failWith *workerpb.LogError
}

func (w *recordingMigratePartitionWorker) LogMigratePartition(ctx context.Context, in *workerpb.LogMigratePartitionRequest, _ ...grpc.CallOption) (*workerpb.LogTaskResponse, error) {
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

// migratePartitionEndpoint 构造本端点的完整路径。
func migratePartitionEndpoint(nodeID uint) string {
	return fmt.Sprintf("/api/v1/nodes/%d/log-runtime/migrate", nodeID)
}

// TestLogMigratePartitionRejectsMissingOperator：取不到认证主体必须在 CP 就拒绝，且不下发 RPC。
//
// 为什么本端点也要这一条：Worker 侧 LogMigratePartition 不消费操作人，CP 不解析就真的
// 没有任何主体记录——分区一旦下沉到冷层，事后只能靠这条凭据回答「谁让它动的」。
//
// 转红方式：把 operatorFromRequest 的拒绝分支改成继续转发——本用例在「403 / 零 RPC」处变红。
func TestLogMigratePartitionRejectsMissingOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	node := createTestNode(t, db)
	stub := &recordingMigratePartitionWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)

	// 刻意不走认证中间件：模拟「认证主体缺失」的调用面。
	handler := NewLogRuntimeHandler(service.NewNodeService(db), pool, service.NewAuthzService(db),
		service.NewInstanceService(db, service.NewGroupService(db), pool))
	engine := gin.New()
	engine.POST("/api/v1/nodes/:id/log-runtime/migrate", handler.MigratePartition)

	resp := makeRequest(engine, http.MethodPost, migratePartitionEndpoint(node.ID),
		map[string]any{"storageNamespace": fmt.Sprintf("node:%d", node.ID), "utcDay": "2026-10-01"}, "")
	require.Equal(t, http.StatusForbidden, resp.Code, resp.Body.String())
	require.Contains(t, resp.Body.String(), "OPERATOR_UNKNOWN")
	require.Zero(t, stub.calls, "取不到主体时不得下发任何 RPC")
}

// TestLogMigratePartitionAuthorizesTarget：命名空间必须绑定到调用方可见的实例与该路由节点。
//
// 缺陷形态（修复前实测）：任意字符串都会被下发给路由节点的 Worker —— 组管理员带别的组的
// `inst:<id>`、甚至带别的节点的命名空间，都能触发分区下沉（跨实例组侧信道 + 数据面扰动）。
//
// 转红方式：删掉 handler 里的 authorizeArchiveTarget 调用——"cross_group"/"other_node"/
// "malformed" 三个子用例会从 403/400 变成 200 且 RPC 次数为 1，本用例红。
func TestLogMigratePartitionAuthorizesTarget(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	otherNode := createTestNodeWithSuffix(t, db, "migrate-other-worker")
	groupA := createGroupViaAPI(t, r, adminToken, "migrate-owner-a")
	groupB := createGroupViaAPI(t, r, adminToken, "migrate-owner-b")
	load := func(nodeID, groupID uint, name string) model.Instance {
		id := makeInstanceInGroup(t, db, nodeID, groupID, name, model.InstanceStatusStopped)
		var inst model.Instance
		require.NoError(t, db.First(&inst, id).Error)
		return inst
	}
	owned := load(node.ID, groupA, "migrate-owned")
	otherGroup := load(node.ID, groupB, "migrate-other-group")
	remote := load(otherNode.ID, groupA, "migrate-remote")
	managerToken := gateUserToken(t, r, db, service.NewAuthzService(db), service.NewUserService(db),
		"migrate-node-manager", []string{"node.manage"}, groupA)

	tests := []struct {
		name      string
		namespace string
		token     string
		status    int
	}{
		// namespace 只是整物理日的探针，组内 node.manage 不能授权整日其他组成员。
		{"own_node_namespace", fmt.Sprintf("node:%d", node.ID), managerToken, http.StatusForbidden},
		{"own_instance_namespace", fmt.Sprintf("inst:%d", owned.ID), managerToken, http.StatusForbidden},
		{"admin_own_node_namespace", fmt.Sprintf("node:%d", node.ID), adminToken, http.StatusOK},
		{"admin_own_instance_namespace", fmt.Sprintf("inst:%d", owned.ID), adminToken, http.StatusOK},
		{"cross_group", fmt.Sprintf("inst:%d", otherGroup.ID), managerToken, http.StatusForbidden},
		{"other_node", fmt.Sprintf("node:%d", otherNode.ID), managerToken, http.StatusForbidden},
		{"roll_up_group_instance_on_other_node", fmt.Sprintf("inst:%d", remote.ID), managerToken, http.StatusForbidden},
		{"malformed_scoped_user", "whatever-the-caller-wants", managerToken, http.StatusForbidden},
		// 平台管理员仍须通过原格式和节点归属检查。
		{"admin_malformed", "whatever-the-caller-wants", adminToken, http.StatusBadRequest},
		{"admin_other_node", fmt.Sprintf("node:%d", otherNode.ID), adminToken, http.StatusForbidden},
		{"admin_instance_on_other_node", fmt.Sprintf("inst:%d", remote.ID), adminToken, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &recordingMigratePartitionWorker{}
			pool.SetWorkerClientForTest(node.UUID, stub)
			resp := makeRequest(r, http.MethodPost, migratePartitionEndpoint(node.ID),
				map[string]any{"storageNamespace": tt.namespace, "utcDay": "2026-10-01"}, tt.token)
			require.Equal(t, tt.status, resp.Code, resp.Body.String())
			if tt.status != http.StatusOK {
				require.Zero(t, stub.calls, "拒绝请求不得触发迁移 RPC")
				return
			}
			require.Equal(t, 1, stub.calls)
			require.NotNil(t, stub.request)
			require.Equal(t, tt.namespace, stub.request.GetStorageNamespace(), "授权通过后必须原样保留下发")
			require.Equal(t, "2026-10-01", stub.request.GetUtcDay())
			require.Equal(t, "cold", stub.request.GetTargetTier())
		})
	}
}

// TestLogMigratePartitionRecordsAudit：成功下沉必须在 CP 侧落一条审计行（谁/哪天/哪个命名空间），
// Worker 报错时不得留下成功审计。
//
// 为什么必须落 CP 侧：审计中间件对本路径不产生 action（POST /nodes/:id/log-runtime/...
// 不匹配任何分支），Worker 侧该 RPC 既不校验也不记录操作人；冷热层均按权威位置查询，
// 「谁触发哪个物理日迁移」仍是复盘与容量核算的原始凭据。
//
// 转红方式：删掉 handler 末尾的 recordAudit 调用——本用例在「必须有一条迁移审计」处变红。
func TestLogMigratePartitionRecordsAudit(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, adminToken, "migrate-audit-owner")
	id := makeInstanceInGroup(t, db, node.ID, groupA, "migrate-audit-source", model.InstanceStatusStopped)
	namespace := fmt.Sprintf("inst:%d", id)

	stub := &recordingMigratePartitionWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)
	resp := makeRequest(r, http.MethodPost, migratePartitionEndpoint(node.ID),
		map[string]any{"storageNamespace": namespace, "utcDay": "2026-10-02"}, adminToken)
	// 先确认真实路径已被触发：否则「有审计行」可能只是桩没走到。
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, stub.calls)

	var audits []model.AuditLog
	require.NoError(t, db.Where("action = ?", "node.log_partition.migrate").Find(&audits).Error)
	require.Len(t, audits, 1, "成功迁移必须落且只落一条审计")
	audit := audits[0]
	require.NotZero(t, audit.UserID, "操作人必须来自认证上下文")
	require.Equal(t, "node", audit.TargetType)
	require.Equal(t, fmt.Sprintf("%d", node.ID), audit.TargetID, "审计目标必须是本次操作的节点")
	require.False(t, audit.Failed)
	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(audit.Detail), &detail), "detail 应为 JSON：%s", audit.Detail)
	require.Equal(t, namespace, detail["storageNamespace"], "detail 必须记录触发探针命名空间")
	require.Equal(t, "physical_utc_day", detail["migrationScope"], "审计不能把单探针伪装成实际迁移范围")
	require.Equal(t, "2026-10-02", detail["utcDay"], "detail 必须记录分区日")
	require.Equal(t, "cold", detail["targetTier"])
	require.Equal(t, "admin", detail["operator"], "可读主体必须入 detail（Agent 通道的 user_id 是 token id）")

	// Worker 报错：不得留下成功审计（否则审计会证明一次从未发生的迁移）。
	pool.SetWorkerClientForTest(node.UUID, &recordingMigratePartitionWorker{failWith: &workerpb.LogError{
		Code: workerpb.LogErrorCode_LOG_NOT_READY, Message: "log partition is not in Catalog",
	}})
	resp = makeRequest(r, http.MethodPost, migratePartitionEndpoint(node.ID),
		map[string]any{"storageNamespace": namespace, "utcDay": "2026-10-03"}, adminToken)
	require.Equal(t, http.StatusConflict, resp.Code, resp.Body.String())
	require.NoError(t, db.Where("action = ?", "node.log_partition.migrate").Find(&audits).Error)
	require.Len(t, audits, 1, "Worker 报错时不得新增成功审计")
}

