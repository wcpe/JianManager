package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/wcpe/JianManager/internal/controlplane/model"
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
	reason   string
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
	if values := md.Get("x-jm-reason"); len(values) > 0 {
		w.reason = values[0]
	}
	w.request = in
	return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_SUCCEEDED}, nil
}

type resolveGapsCall struct {
	calls    int
	hasMD    bool
	operator string
	reason   string
	request  *workerpb.LogResolveIngestGapsRequest
}

func (w *recordingResolveGapsWorker) snapshot() resolveGapsCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return resolveGapsCall{calls: w.calls, hasMD: w.hasMD, operator: w.operator, reason: w.reason, request: w.request}
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
		Auth:              service.NewAuthService(db, jwtCfg),
		User:              service.NewUserService(db),
		Group:             groupSvc,
		Node:              nodeSvc,
		Instance:          instanceSvc,
		Audit:             service.NewAuditService(db),
		Authz:             authzSvc,
		LogRuntimePool:    pool,
		LogRuntimeHolders: service.NewLogTargetHolderService(db),
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
	inst := model.Instance{NodeID: node.ID, Name: "operator-log-source"}
	require.NoError(t, db.Create(&inst).Error)
	sourceKey := fmt.Sprintf("inst:%d/stderr/instance:%s@worker:%s", inst.ID, inst.UUID, node.UUID)
	stub := &recordingResolveGapsWorker{}
	pool.SetWorkerClientForTest(node.UUID, stub)

	resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/resolve-gaps", node.ID),
		map[string]any{"storageNamespace": sourceKey}, adminToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())

	call := stub.snapshot()
	require.Equal(t, 1, call.calls)
	require.True(t, call.hasMD, "CP 必须经 outgoing metadata 下达操作人")
	require.Equal(t, "admin", call.operator, "操作人取自认证主体（登录名），不是请求体")
	require.NotNil(t, call.request)
	require.Equal(t, sourceKey, call.request.GetStorageNamespace())
}

// 整节点自动解算不能携带人工裁定凭据，空键加理由也不能绕过单源授权。
func TestLogResolveIngestGapsWholeNodeOmitsAbandonmentMetadata(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	for _, body := range []any{nil, map[string]any{"reason": "不能整节点放弃"}, map[string]any{"storageNamespace": "  ", "reason": "不能整节点放弃"}} {
		stub := &recordingResolveGapsWorker{}
		pool.SetWorkerClientForTest(node.UUID, stub)
		resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/resolve-gaps", node.ID), body, adminToken)
		require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
		call := stub.snapshot()
		t.Logf("auto RPC=%d operator=%q reason=%q", call.calls, call.operator, call.reason)
		require.Equal(t, 1, call.calls)
		require.Empty(t, call.operator, "整节点自动路径不得收到人工操作人凭据")
		require.Empty(t, call.reason, "空键请求理由不得授权任何永久裁定")
		require.NotNil(t, call.request)
		require.Empty(t, call.request.GetStorageNamespace())
	}
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

// 完整源键必须按可信实例身份与节点代次校验，已委派的节点管理能力不能替代用户组隔离。
func TestLogResolveIngestGapsSourceAuthorization(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	otherNode := createTestNodeWithSuffix(t, db, "other-log-worker")
	groupA := createGroupViaAPI(t, r, adminToken, "log-owner-a")
	groupB := createGroupViaAPI(t, r, adminToken, "log-owner-b")
	loadInstance := func(nodeID, groupID uint, name string) model.Instance {
		id := makeInstanceInGroup(t, db, nodeID, groupID, name, model.InstanceStatusStopped)
		var inst model.Instance
		require.NoError(t, db.First(&inst, id).Error)
		return inst
	}
	instA := loadInstance(node.ID, groupA, "owned-log-source")
	instB := loadInstance(node.ID, groupB, "other-group-source")
	other := loadInstance(otherNode.ID, groupA, "other-node-source")
	var unassigned model.Instance
	unassigned.NodeID, unassigned.Name = node.ID, "unassigned-log-source"
	require.NoError(t, db.Create(&unassigned).Error)
	fullKey := func(inst model.Instance, workerUUID, stream string) string {
		return fmt.Sprintf("inst:%d/%s/instance:%s@worker:%s", inst.ID, stream, inst.UUID, workerUUID)
	}
	userToken := gateUserToken(t, r, db, service.NewAuthzService(db), service.NewUserService(db),
		"log-node-manager", []string{"node.manage"}, groupA)
	readToken := gateUserToken(t, r, db, service.NewAuthzService(db), service.NewUserService(db),
		"log-node-reader", []string{"node.read"}, groupA)
	tests := []struct {
		name   string
		key    string
		token  string
		status int
	}{
		{"same_group", fullKey(instA, node.UUID, "stderr"), userToken, http.StatusOK},
		{"same_group_stdout", fullKey(instA, node.UUID, "stdout"), userToken, http.StatusOK},
		{"same_group_file", fullKey(instA, node.UUID, "file"), userToken, http.StatusOK},
		{"cross_group", fullKey(instB, node.UUID, "stderr"), userToken, http.StatusForbidden},
		{"unassigned", fullKey(unassigned, node.UUID, "stderr"), userToken, http.StatusForbidden},
		{"wrong_node", fullKey(other, otherNode.UUID, "stderr"), userToken, http.StatusForbidden},
		{"wrong_worker", fullKey(instA, otherNode.UUID, "stderr"), userToken, http.StatusForbidden},
		{"wrong_generation", fmt.Sprintf("inst:%d/stderr/instance:unknown@worker:%s", instA.ID, node.UUID), userToken, http.StatusForbidden},
		{"unknown_instance", fmt.Sprintf("inst:999999/stderr/instance:unknown@worker:%s", node.UUID), adminToken, http.StatusForbidden},
		{"bare_source", fmt.Sprintf("inst:%d/stderr", instA.ID), adminToken, http.StatusBadRequest},
		{"malformed_source", fmt.Sprintf("inst:%d/stderr/instance:%s@worker:%s/extra", instA.ID, instA.UUID, node.UUID), adminToken, http.StatusBadRequest},
		{"unknown_stream", fullKey(instA, node.UUID, "unknown"), adminToken, http.StatusBadRequest},
		{"node_read_only", fullKey(instA, node.UUID, "stderr"), readToken, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &recordingResolveGapsWorker{}
			pool.SetWorkerClientForTest(node.UUID, stub)
			resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/resolve-gaps", node.ID),
				map[string]any{"storageNamespace": tt.key, "reason": "已核验缺口", "operator": "forged-admin"}, tt.token)
			call := stub.snapshot()
			t.Logf("HTTP=%d resolve_rpc=%d body=%s", resp.Code, call.calls, resp.Body.String())
			require.Equal(t, tt.status, resp.Code, resp.Body.String())
			if tt.status != http.StatusOK {
				require.Zero(t, call.calls, "拒绝请求不能触发裁定 RPC")
				return
			}
			require.Equal(t, 1, call.calls)
			require.Equal(t, "log-node-manager", call.operator)
			require.Equal(t, "已核验缺口", call.reason, "裁定理由仍经可信传输 metadata 下发")
			require.Equal(t, tt.key, call.request.GetStorageNamespace(), "授权后必须原样保留完整源键")
		})
	}
	t.Run("revoked_group_with_known_key", func(t *testing.T) {
		userID := findUserIDByUsername(t, db, "log-node-manager")
		require.NoError(t, db.Where("group_id = ? AND user_id = ?", groupA, userID).Delete(&model.GroupMember{}).Error)
		stub := &recordingResolveGapsWorker{}
		pool.SetWorkerClientForTest(node.UUID, stub)
		resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/resolve-gaps", node.ID),
			map[string]any{"storageNamespace": fullKey(instA, node.UUID, "stderr"), "reason": "已知旧标识不是授权凭证"}, userToken)
		require.Equal(t, http.StatusForbidden, resp.Code, resp.Body.String())
		require.Zero(t, stub.snapshot().calls)
		t.Logf("revoked group HTTP=%d resolve_rpc=0", resp.Code)
	})
}

// 迁移后的历史源只能凭 CP 已登记的完整 holder 绑定授权，不能仅凭客户端声称的旧代次。
func TestLogResolveIngestGapsHistoricalHolderAuthorization(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	oldNode := createTestNode(t, db)
	currentNode := createTestNodeWithSuffix(t, db, "current-log-holder")
	groupA := createGroupViaAPI(t, r, adminToken, "history-owner")
	groupB := createGroupViaAPI(t, r, adminToken, "history-other")
	id := makeInstanceInGroup(t, db, currentNode.ID, groupA, "moved-log-instance", model.InstanceStatusStopped)
	var inst model.Instance
	require.NoError(t, db.First(&inst, id).Error)
	target := fmt.Sprintf("inst:%d", id)
	oldGeneration := fmt.Sprintf("instance:%s@worker:%s", inst.UUID, oldNode.UUID)
	currentGeneration := fmt.Sprintf("instance:%s@worker:%s", inst.UUID, currentNode.UUID)
	holders := service.NewLogTargetHolderService(db)
	require.NoError(t, holders.RecordCurrent(id, oldNode.UUID, oldGeneration, target, time.Now().UTC().Add(-time.Hour)))
	require.NoError(t, holders.RecordCurrent(id, currentNode.UUID, currentGeneration, target, time.Now().UTC()))
	ownedToken := gateUserToken(t, r, db, service.NewAuthzService(db), service.NewUserService(db),
		"history-manager", []string{"node.manage"}, groupA)
	otherToken := gateUserToken(t, r, db, service.NewAuthzService(db), service.NewUserService(db),
		"history-outsider", []string{"node.manage"}, groupB)
	tests := []struct {
		name       string
		node       *model.Node
		generation string
		token      string
		status     int
	}{
		{"registered_history", oldNode, oldGeneration, ownedToken, http.StatusOK},
		{"current", currentNode, currentGeneration, ownedToken, http.StatusOK},
		{"history_cross_group", oldNode, oldGeneration, otherToken, http.StatusForbidden},
		{"history_wrong_worker", currentNode, oldGeneration, ownedToken, http.StatusForbidden},
		{"unregistered_generation", oldNode, oldGeneration + "-unknown", ownedToken, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &recordingResolveGapsWorker{}
			pool.SetWorkerClientForTest(tt.node.UUID, stub)
			key := target + "/stderr/" + tt.generation
			resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/resolve-gaps", tt.node.ID),
				map[string]any{"storageNamespace": key, "reason": "核对历史缺口"}, tt.token)
			call := stub.snapshot()
			t.Logf("HTTP=%d resolve_rpc=%d body=%s", resp.Code, call.calls, resp.Body.String())
			require.Equal(t, tt.status, resp.Code, resp.Body.String())
			if tt.status == http.StatusOK {
				require.Equal(t, 1, call.calls)
				require.Equal(t, "history-manager", call.operator)
				require.Equal(t, key, call.request.GetStorageNamespace())
			} else {
				require.Zero(t, call.calls)
			}
		})
	}
}

type localAbandonedRange struct {
	LogSourceID, SourceGeneration, StorageNamespace string
	From, To                                        uint64
	ReasonCode, Operator, AtUTC                     string
}

type localFixtureGap struct {
	ID               int64
	Key              string
	StartPos, EndPos uint64
	Reason, Detail   string
	Resolved         bool
	Resolution       string
}

type localFixtureReply struct {
	Error           string                    `json:"error"`
	RPC             *workerpb.LogTaskResponse `json:"rpc"`
	Ranges          []localAbandonedRange     `json:"ranges"`
	Gaps            []localFixtureGap         `json:"gaps"`
	ConsumedSources int                       `json:"consumedSources"`
}

// 本地子进程只隔离 SQLite 驱动注册；CP 真路由与 Worker 真服务/持久账本均不替换。
type localResolveFixture struct {
	mu     sync.Mutex
	writer *json.Encoder
	reader *json.Decoder
}

func startLocalResolveFixture(t *testing.T, bindings []map[string]string, seed string) *localResolveFixture {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "worker-fixture.exe")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-p=1", "-o", binary, "./testdata/log_resolve_gaps_worker")
	build.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "编译本地 Worker 夹具失败：%s", output)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	command := exec.CommandContext(ctx, binary)
	command.WaitDelay = 3 * time.Second
	stdin, err := command.StdinPipe()
	require.NoError(t, err)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	command.Stderr = os.Stderr
	require.NoError(t, command.Start())
	t.Cleanup(func() {
		defer cancel()
		_ = stdin.Close()
		require.NoError(t, command.Wait(), "本地 Worker 夹具应在 EOF 后退出，超时必须回收 PID")
	})
	fixture := &localResolveFixture{writer: json.NewEncoder(stdin), reader: json.NewDecoder(stdout)}
	_, err = fixture.exchange(map[string]any{"action": "init", "root": t.TempDir(), "sources": bindings, "seed": seed})
	require.NoError(t, err)
	return fixture
}

func (f *localResolveFixture) exchange(request map[string]any) (localFixtureReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var response localFixtureReply
	if err := f.writer.Encode(request); err != nil {
		return response, err
	}
	if err := f.reader.Decode(&response); err != nil {
		return response, err
	}
	if response.Error != "" {
		return response, fmt.Errorf("本地 Worker 夹具：%s", response.Error)
	}
	return response, nil
}

func (f *localResolveFixture) snapshot(t *testing.T, action string) localFixtureReply {
	t.Helper()
	response, err := f.exchange(map[string]any{"action": action})
	require.NoError(t, err)
	return response
}

// localResolveGapsWorker 只替代传输，裁定结果必须由真实 Worker 子进程返回。
type localResolveGapsWorker struct {
	recordingResolveGapsWorker
	fixture *localResolveFixture
}

func (w *localResolveGapsWorker) LogResolveIngestGaps(ctx context.Context, in *workerpb.LogResolveIngestGapsRequest, opts ...grpc.CallOption) (*workerpb.LogTaskResponse, error) {
	_, _ = w.recordingResolveGapsWorker.LogResolveIngestGaps(ctx, in, opts...)
	md, _ := metadata.FromOutgoingContext(ctx)
	response, err := w.fixture.exchange(map[string]any{"action": "rpc", "rpc": in, "metadata": md})
	return response.RPC, err
}

// 真实账本对照：空键仍默认拒绝自动解算，合法同组完整键才写入可持久化的操作人裁定凭据。
func TestLogResolveIngestGapsLocalManagerAudit(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	otherNode := createTestNodeWithSuffix(t, db, "wrong-manager-worker")
	groupA := createGroupViaAPI(t, r, adminToken, "manager-owner")
	groupB := createGroupViaAPI(t, r, adminToken, "manager-other")
	id := makeInstanceInGroup(t, db, node.ID, groupA, "manager-audit-source", model.InstanceStatusStopped)
	var inst model.Instance
	require.NoError(t, db.First(&inst, id).Error)
	generation := fmt.Sprintf("instance:%s@worker:%s", inst.UUID, node.UUID)
	target := fmt.Sprintf("inst:%d", id)
	fullKey := target + "/stderr/" + generation
	ownedToken := gateUserToken(t, r, db, service.NewAuthzService(db), service.NewUserService(db),
		"manager-operator", []string{"node.manage"}, groupA)
	otherToken := gateUserToken(t, r, db, service.NewAuthzService(db), service.NewUserService(db),
		"manager-outsider", []string{"node.manage"}, groupB)

	fixture := startLocalResolveFixture(t, []map[string]string{{"uuid": inst.UUID, "target": target, "generation": generation, "workDir": t.TempDir()}}, "raw")
	require.Empty(t, fixture.snapshot(t, "snapshot").Ranges)
	worker := &localResolveGapsWorker{fixture: fixture}
	pool.SetWorkerClientForTest(node.UUID, worker)
	endpoint := fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/resolve-gaps", node.ID)

	for _, key := range []string{"", "   "} {
		before := worker.snapshot().calls
		resp := makeRequest(r, http.MethodPost, endpoint,
			map[string]any{"storageNamespace": key, "reason": "不得用空键放弃缺口"}, ownedToken)
		t.Logf("auto HTTP=%d resolve_rpc_delta=%d body=%s", resp.Code, worker.snapshot().calls-before, resp.Body.String())
		require.Equal(t, http.StatusConflict, resp.Code, resp.Body.String())
		require.Contains(t, resp.Body.String(), "projection alone cannot resolve")
		require.Equal(t, before+1, worker.snapshot().calls)
		require.Empty(t, worker.snapshot().request.GetStorageNamespace())
		require.Empty(t, fixture.snapshot(t, "snapshot").Ranges, "自动路径不得生成永久裁定凭据")
	}
	for _, tt := range []struct {
		key    string
		token  string
		status int
	}{
		{fullKey, otherToken, http.StatusForbidden},
		{fullKey + "-unknown", ownedToken, http.StatusForbidden},
		{target + "/stderr/" + fmt.Sprintf("instance:%s@worker:%s", inst.UUID, otherNode.UUID), ownedToken, http.StatusForbidden},
		{target + "/stderr", ownedToken, http.StatusBadRequest},
	} {
		before := worker.snapshot().calls
		resp := makeRequest(r, http.MethodPost, endpoint,
			map[string]any{"storageNamespace": tt.key, "reason": "测试拒绝裁定"}, tt.token)
		t.Logf("denied HTTP=%d resolve_rpc_delta=%d body=%s", resp.Code, worker.snapshot().calls-before, resp.Body.String())
		require.Equal(t, tt.status, resp.Code, resp.Body.String())
		require.Equal(t, before, worker.snapshot().calls)
		require.Empty(t, fixture.snapshot(t, "snapshot").Ranges, "拒绝请求不能污染账本")
	}
	before := worker.snapshot().calls
	resp := makeRequest(r, http.MethodPost, endpoint,
		map[string]any{"storageNamespace": fullKey, "reason": "已确认临时夹具缺口", "operator": "forged-admin"}, ownedToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.Equal(t, before+1, worker.snapshot().calls)
	ranges := fixture.snapshot(t, "snapshot").Ranges
	require.Len(t, ranges, 1, "成功必须来自真实账本裁定，不接受桩返回成功")
	require.Equal(t, "manager-operator", ranges[0].Operator)
	require.Equal(t, target+"/stderr", ranges[0].LogSourceID)
	require.Equal(t, generation, ranges[0].SourceGeneration)
	t.Logf("allowed HTTP=%d resolve_rpc_delta=1 audit_operator=%s source=%s generation=%s", resp.Code,
		ranges[0].Operator, ranges[0].LogSourceID, ranges[0].SourceGeneration)
	require.Equal(t, ranges, fixture.snapshot(t, "restart").Ranges, "认证操作人裁定凭据必须持久化并跨重启保留")
}

// 两组未发布源已有消费水位与 DELIVER_ERROR：空键加理由仍只能自动证据解算，不能永久放弃组外源。
func TestLogResolveIngestGapsEmptyKeyCannotAbandonOtherGroup(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupLogRuntimeOperatorRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, adminToken, "auto-owner")
	groupB := createGroupViaAPI(t, r, adminToken, "auto-other")
	// 组外源先创建，确保旧全源排序先遇到组外缺口，而不是被同组源的错误提前遮挡。
	otherID := makeInstanceInGroup(t, db, node.ID, groupB, "auto-other-source", model.InstanceStatusStopped)
	ownedID := makeInstanceInGroup(t, db, node.ID, groupA, "auto-owned-source", model.InstanceStatusStopped)
	var sources []model.Instance
	require.NoError(t, db.Where("id IN ?", []uint{otherID, ownedID}).Order("id ASC").Find(&sources).Error)
	require.Len(t, sources, 2)
	bindings := make([]map[string]string, 0, 2)
	for _, source := range sources {
		bindings = append(bindings, map[string]string{"uuid": source.UUID, "target": fmt.Sprintf("inst:%d", source.ID),
			"generation": fmt.Sprintf("instance:%s@worker:%s", source.UUID, node.UUID), "workDir": t.TempDir()})
	}
	fixture := startLocalResolveFixture(t, bindings, "delivery")
	initial := fixture.snapshot(t, "snapshot")
	require.Equal(t, 2, initial.ConsumedSources, "两组源均须有 Read/Durable 消费水位，不能只测零水位 Raw 拒绝")
	require.Len(t, initial.Gaps, 2)
	for _, gap := range initial.Gaps {
		require.Equal(t, "DELIVER_ERROR", gap.Reason)
		require.False(t, gap.Resolved)
	}
	require.Empty(t, initial.Ranges)
	worker := &localResolveGapsWorker{fixture: fixture}
	pool.SetWorkerClientForTest(node.UUID, worker)
	token := gateUserToken(t, r, db, service.NewAuthzService(db), service.NewUserService(db),
		"auto-group-manager", []string{"node.manage"}, groupA)
	endpoint := fmt.Sprintf("/api/v1/nodes/%d/log-runtime/ingest/resolve-gaps", node.ID)
	for _, body := range []any{nil, map[string]any{"reason": "必须保留组外源"},
		map[string]any{"storageNamespace": "", "reason": "必须保留组外源"},
		map[string]any{"storageNamespace": "  ", "reason": "必须保留组外源"}} {
		before := worker.snapshot().calls
		resp := makeRequest(r, http.MethodPost, endpoint, body, token)
		require.Equal(t, http.StatusConflict, resp.Code, resp.Body.String())
		require.Contains(t, resp.Body.String(), "no complete published projection")
		require.Equal(t, before+1, worker.snapshot().calls)
		require.Empty(t, worker.snapshot().operator)
		require.Empty(t, worker.snapshot().reason)
		snapshot := fixture.snapshot(t, "snapshot")
		require.Empty(t, snapshot.Ranges, "自动请求不得生成任何组的永久裁定凭据")
		require.Equal(t, initial.Gaps, snapshot.Gaps, "所有源缺口保持未决，不能在返回失败前先永久裁定")
		t.Logf("multi-group auto HTTP=%d operator=%q reason=%q gaps=%d abandoned=%d", resp.Code,
			worker.snapshot().operator, worker.snapshot().reason, len(snapshot.Gaps), len(snapshot.Ranges))
	}
	// 即便绕过 CP 人工 metadata 剥离直接调夹具里的 Worker RPC，自动内核也不能永久裁定。
	direct, err := fixture.exchange(map[string]any{"action": "rpc", "rpc": &workerpb.LogResolveIngestGapsRequest{},
		"metadata": map[string][]string{operatorMetadataKey: {"auto-group-manager"}, "x-jm-reason": {"不能全源放弃"}}})
	require.NoError(t, err)
	require.Equal(t, workerpb.LogTaskState_LOG_TASK_FAILED, direct.RPC.GetState())
	require.Empty(t, direct.Ranges)
	require.Equal(t, initial.Gaps, direct.Gaps, "Worker 自动内核不能依赖 CP 防旁路")

	owned := bindings[1]
	ownedKey := owned["target"] + "/stderr/" + owned["generation"]
	resp := makeRequest(r, http.MethodPost, endpoint,
		map[string]any{"storageNamespace": ownedKey, "reason": "已确认本组缺口", "operator": "forged-admin"}, token)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	final := fixture.snapshot(t, "snapshot")
	require.Len(t, final.Ranges, 1)
	require.Equal(t, "auto-group-manager", final.Ranges[0].Operator)
	require.Equal(t, owned["target"]+"/stderr", final.Ranges[0].LogSourceID)
	require.Equal(t, "PERMANENTLY_LOST", final.Ranges[0].ReasonCode)
	for _, gap := range final.Gaps {
		if strings.HasPrefix(gap.Key, bindings[0]["target"]+"/") {
			require.False(t, gap.Resolved, "同组显式裁定不得触及组外缺口")
		}
	}
	restarted := fixture.snapshot(t, "restart")
	require.Equal(t, final.Ranges, restarted.Ranges)
	t.Logf("multi-group explicit HTTP=%d abandoned=%d operator=%s", resp.Code, len(restarted.Ranges), restarted.Ranges[0].Operator)
}
