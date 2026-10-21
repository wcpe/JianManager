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
		// 前端面板按节点命名空间调用（NodeLogRuntimePanel），必须继续可用。
		{"own_node_namespace", fmt.Sprintf("node:%d", node.ID), managerToken, http.StatusOK},
		// 存储命名空间的实际形态是 inst:<id>（Worker catalog 以 binding.TargetID 为键）。
		{"own_instance_namespace", fmt.Sprintf("inst:%d", owned.ID), managerToken, http.StatusOK},
		{"cross_group", fmt.Sprintf("inst:%d", otherGroup.ID), managerToken, http.StatusForbidden},
		{"other_node", fmt.Sprintf("node:%d", otherNode.ID), managerToken, http.StatusForbidden},
		{"roll_up_group_instance_on_other_node", fmt.Sprintf("inst:%d", remote.ID), managerToken, http.StatusForbidden},
		{"malformed", "whatever-the-caller-wants", managerToken, http.StatusBadRequest},
		// 平台管理员也不是万能通行证：越界命名空间同样被拒。
		{"admin_other_node", fmt.Sprintf("node:%d", otherNode.ID), adminToken, http.StatusForbidden},
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
// 不匹配任何分支），Worker 侧该 RPC 既不校验也不记录操作人——冷层还缺热层查询能力，
// 「哪一天被下沉」是复盘与容量核算的原始凭据。
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
	require.Equal(t, namespace, detail["storageNamespace"], "detail 必须记录被下沉的命名空间")
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

// TestLogClearIdentityConflictsRecordsAudit：成功清空必须落 CP 侧审计行，含操作人、理由、
// 目标源键与结果计数（clearedCount / clearedSources / skippedSources）；Worker 报错时不落。
//
// 为什么必须落 CP 侧：清空整节点质量标记是永久裁定（记录清掉不可回滚），Worker 侧的
// clearance 凭据随各自源 payload 存放，跨节点的统一审计视图只有 CP 这一行。
//
// 转红方式：删掉 handler 成功分支的 recordAudit 调用——本用例在「必须有一条清空审计」处变红。
func TestLogClearIdentityConflictsRecordsAudit(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, adminToken, "clear-audit-owner")
	id := makeInstanceInGroup(t, db, node.ID, groupA, "clear-audit-source", model.InstanceStatusStopped)
	var inst model.Instance
	require.NoError(t, db.First(&inst, id).Error)
	sourceKey := sourceKeyOf(inst, node, "file")

	stub := &recordingClearConflictsWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)
	resp := makeRequest(r, http.MethodPost, clearConflictsEndpoint(node.ID),
		map[string]any{"storageNamespace": sourceKey, "reason": "已核对 VL 保留版本正确"}, adminToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, stub.calls)

	var audits []model.AuditLog
	require.NoError(t, db.Where("action = ?", "node.log_ingest.clear_conflicts").Find(&audits).Error)
	require.Len(t, audits, 1, "成功清空必须落且只落一条审计")
	audit := audits[0]
	require.NotZero(t, audit.UserID, "操作人必须来自认证上下文")
	require.Equal(t, "node", audit.TargetType)
	require.Equal(t, fmt.Sprintf("%d", node.ID), audit.TargetID)
	require.False(t, audit.Failed)
	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(audit.Detail), &detail), "detail 应为 JSON：%s", audit.Detail)
	require.Equal(t, sourceKey, detail["storageNamespace"])
	require.Equal(t, "已核对 VL 保留版本正确", detail["reason"])
	require.Equal(t, "admin", detail["operator"])
	// 结果计数与「实际改了谁 / 请求覆盖了谁」必须能在同一份凭据里对照。
	require.EqualValues(t, 9, detail["clearedCount"])
	require.Equal(t, []any{"inst:145/file/g1", "inst:151/file/g1"}, detail["clearedSources"])
	require.Equal(t, []any{"inst:151/file/g2"}, detail["skippedSources"])

	// Worker 报错：不得留下成功审计。
	pool.SetWorkerClientForTest(node.UUID, &failingClearConflictsWorker{message: "采集轮忙"})
	resp = makeRequest(r, http.MethodPost, clearConflictsEndpoint(node.ID),
		map[string]any{"storageNamespace": sourceKey, "reason": "已核对"}, adminToken)
	require.Equal(t, http.StatusConflict, resp.Code, resp.Body.String())
	require.NoError(t, db.Where("action = ?", "node.log_ingest.clear_conflicts").Find(&audits).Error)
	require.Len(t, audits, 1, "Worker 报错时不得新增成功审计")
}

// TestLogResolveIngestGapsRecordsAbandonAuditOnlyForExplicitSource：显式单源放弃裁定必须落
// CP 侧审计行（操作人 / 理由 / 源键 / 结果），而空源键的自动解算不得产出「放弃裁定」审计。
//
// 为什么必须区分：带非空源键的调用是唯一允许跨过永久缺口的动作（ADR-101 要求可审计）；
// 空键只是按现有投影证据自动解算，把它也记成放弃裁定会让审计面出现从未有人做过的裁定。
//
// 转红方式：删掉 handler 里的 recordAudit 调用 → 第一条断言变红；
// 把 recordAudit 移出 `if body.StorageNamespace != ""` → 第二条断言（自动路径零审计）变红。
func TestLogResolveIngestGapsRecordsAbandonAuditOnlyForExplicitSource(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, adminToken, "abandon-audit-owner")
	id := makeInstanceInGroup(t, db, node.ID, groupA, "abandon-audit-source", model.InstanceStatusStopped)
	var inst model.Instance
	require.NoError(t, db.First(&inst, id).Error)
	sourceKey := sourceKeyOf(inst, node, "stderr")
	managerToken := gateUserToken(t, r, db, service.NewAuthzService(db), service.NewUserService(db),
		"abandon-manager", []string{"node.manage"}, groupA)
	endpoint := fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/resolve-gaps", node.ID)

	// 自动解算路径：即使带理由也不得产生放弃裁定审计。
	for _, body := range []any{nil, map[string]any{"reason": "想整节点放弃"},
		map[string]any{"storageNamespace": "  ", "reason": "想整节点放弃"}} {
		stub := &recordingResolveGapsWorker{}
		pool.SetWorkerClientForTest(node.UUID, stub)
		resp := makeRequest(r, http.MethodPost, endpoint, body, managerToken)
		require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
		require.Equal(t, 1, stub.snapshot().calls)
		var audits []model.AuditLog
		require.NoError(t, db.Where("action = ?", "node.log_ingest.abandon_gap").Find(&audits).Error)
		require.Empty(t, audits, "空源键的自动解算不是人工永久裁定，不得留下放弃审计：body=%v", body)
	}

	// 显式单源裁定：必须落一行，且字段完整。
	stub := &recordingResolveGapsWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)
	resp := makeRequest(r, http.MethodPost, endpoint,
		map[string]any{"storageNamespace": sourceKey, "reason": "已核验缺口永久丢失"}, managerToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, 1, stub.snapshot().calls)

	var audits []model.AuditLog
	require.NoError(t, db.Where("action = ?", "node.log_ingest.abandon_gap").Find(&audits).Error)
	require.Len(t, audits, 1, "显式单源放弃裁定必须落且只落一条审计")
	audit := audits[0]
	require.NotZero(t, audit.UserID, "操作人必须来自认证上下文")
	require.Equal(t, "node", audit.TargetType)
	require.Equal(t, fmt.Sprintf("%d", node.ID), audit.TargetID)
	require.False(t, audit.Failed)
	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(audit.Detail), &detail), "detail 应为 JSON：%s", audit.Detail)
	require.Equal(t, sourceKey, detail["storageNamespace"], "审计必须记录被放弃的源键")
	require.Equal(t, "已核验缺口永久丢失", detail["reason"])
	require.Equal(t, "abandon-manager", detail["operator"], "操作人取自认证上下文，不是请求体")
	require.Equal(t, "resolved", detail["state"])
}
