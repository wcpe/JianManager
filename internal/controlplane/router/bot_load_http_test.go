package router

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type botLoadHTTPWorker struct {
	workerpb.WorkerServiceClient

	mu            sync.Mutex
	ready         bool
	maxBots       int32
	activeBots    int32
	generation    int64
	capacityCalls int
	applyCalls    int
	applyErr      error
}

func (w *botLoadHTTPWorker) GetBotCapacity(context.Context, *workerpb.GetBotCapacityRequest, ...grpc.CallOption) (*workerpb.GetBotCapacityResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.capacityCalls++
	return &workerpb.GetBotCapacityResponse{
		Ready: w.ready, MaxBots: w.maxBots, ActiveBots: w.activeBots,
		CapacityGeneration: w.generation, WorkerEpoch: "worker-epoch", BotWorkerVersion: "test",
		ObservedAtUnixMs: time.Now().UnixMilli(), Features: []string{"fleet-v1"},
	}, nil
}

func (w *botLoadHTTPWorker) CreateInstance(context.Context, *workerpb.CreateInstanceRequest, ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (w *botLoadHTTPWorker) ListBots(context.Context, *workerpb.ListBotsRequest, ...grpc.CallOption) (*workerpb.ListBotsResponse, error) {
	return &workerpb.ListBotsResponse{}, nil
}

func (w *botLoadHTTPWorker) ApplyBotBatch(_ context.Context, req *workerpb.ApplyBotBatchRequest, _ ...grpc.CallOption) (*workerpb.ApplyBotBatchResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.applyCalls++
	if w.applyErr != nil {
		return nil, w.applyErr
	}
	results := make([]*workerpb.ApplyBotBatchItemResult, 0, len(req.Assignments))
	for _, assignment := range req.Assignments {
		results = append(results, &workerpb.ApplyBotBatchItemResult{BotUuid: assignment.BotUuid, Accepted: true, Status: "accepted"})
	}
	return &workerpb.ApplyBotBatchResponse{BatchId: req.BatchId, IdempotencyKey: req.IdempotencyKey, Results: results}, nil
}

func (w *botLoadHTTPWorker) counts() (capacity, apply int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.capacityCalls, w.applyCalls
}

func setupBotLoadHTTP(t *testing.T, maxBots int32) (*gorm.DB, *cpgrpc.ClientPool, *botLoadHTTPWorker, httpTestContext) {
	t.Helper()
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupTestRouterWithPool(db, pool)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	worker := &botLoadHTTPWorker{ready: true, maxBots: maxBots, generation: 7}
	pool.SetWorkerClientForTest(node.UUID, worker)
	groupID := createGroupViaAPI(t, r, token, "bot-load")
	instanceID := createInstanceViaAPI(t, r, token, node.ID, groupID)
	return db, pool, worker, httpTestContext{router: r, token: token, node: node, groupID: groupID, instanceID: instanceID}
}

type httpTestContext struct {
	router     *gin.Engine
	token      string
	node       *model.Node
	groupID    uint
	instanceID uint
}

func createBotLoadSession(t *testing.T, ctx httpTestContext, count int) uint {
	t.Helper()
	w := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions", map[string]any{
		"instanceId": ctx.instanceID,
		"count":      count,
		"behavior":   "idle",
		"namePrefix": "load",
		"config":     map[string]any{"server": "127.0.0.1", "port": 25565, "auth": "offline"},
	}, ctx.token)
	require.Equalf(t, http.StatusCreated, w.Code, "创建压测会话失败: %s", w.Body.String())
	return uint(parseJSON(t, w)["id"].(float64))
}

func preflightBotLoadSession(t *testing.T, ctx httpTestContext, sessionID uint, body map[string]any) map[string]any {
	t.Helper()
	w := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/preflight", body, ctx.token)
	require.Equalf(t, http.StatusOK, w.Code, "预检失败: %s", w.Body.String())
	return parseJSON(t, w)
}

func assertBotLoadStartHasNoRows(t *testing.T, db *gorm.DB, sessionID uint) {
	t.Helper()
	var botCount, batchCount int64
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ?", sessionID).Count(&botCount).Error)
	require.NoError(t, db.Model(&model.BotLoadBatch{}).Where("stress_session_id = ?", sessionID).Count(&batchCount).Error)
	assert.Zero(t, botCount)
	assert.Zero(t, batchCount)
}

func TestBotLoadNodes_RoutePermissionIsolationCacheAndRedaction(t *testing.T) {
	db, _, worker, ctx := setupBotLoadHTTP(t, 50)

	missing := makeRequest(ctx.router, http.MethodGet, "/api/v1/bots/load-nodes", nil, ctx.token)
	assert.Equal(t, http.StatusBadRequest, missing.Code)

	for range 2 {
		w := makeRequest(ctx.router, http.MethodGet, "/api/v1/bots/load-nodes?instanceId="+itoa(ctx.instanceID), nil, ctx.token)
		require.Equalf(t, http.StatusOK, w.Code, "容量路由应先于 /bots/:id: %s", w.Body.String())
		body := parseJSON(t, w)
		assert.Equal(t, float64(50), body["totalCapacity"])
		assert.Equal(t, float64(50), body["availableCapacity"])
		require.Len(t, body["items"], 1)
		assert.NotContains(t, w.Body.String(), "host")
		assert.NotContains(t, w.Body.String(), "secret")
	}
	capacityCalls, _ := worker.counts()
	assert.Equal(t, 1, capacityCalls, "15 秒缓存应由 CapacityDirectory 统一折叠")

	memberToken := getMemberToken(t, ctx.router, "reader", "password123")
	forbidden := makeRequest(ctx.router, http.MethodGet, "/api/v1/bots/load-nodes?instanceId="+itoa(ctx.instanceID), nil, memberToken)
	assert.Equal(t, http.StatusForbidden, forbidden.Code)

	otherGroup := createGroupViaAPI(t, ctx.router, ctx.token, "other")
	otherInstance := createInstanceViaAPI(t, ctx.router, ctx.token, ctx.node.ID, otherGroup)
	memberID := findUserIDByUsername(t, db, "reader")
	addMemberViaAPI(t, ctx.router, ctx.token, ctx.groupID, memberID, model.GroupMemberRoleMember)
	hidden := makeRequest(ctx.router, http.MethodGet, "/api/v1/bots/load-nodes?instanceId="+itoa(otherInstance), nil, memberToken)
	assert.Equal(t, http.StatusNotFound, hidden.Code)
}

func TestBotStressSessionCreate_AuditsCanonicalAndAliasResultsWithoutCredentials(t *testing.T) {
	db, _, _, ctx := setupBotLoadHTTP(t, 50)
	tests := []struct {
		name       string
		path       string
		count      int
		wantStatus int
		failed     bool
	}{
		{name: "标准端点成功", path: "/api/v1/bots/stress-sessions", count: 1, wantStatus: http.StatusCreated},
		{name: "标准端点失败", path: "/api/v1/bots/stress-sessions", count: 0, wantStatus: http.StatusBadRequest, failed: true},
		{name: "兼容别名成功", path: "/api/v1/bots/stress-test", count: 1, wantStatus: http.StatusCreated},
		{name: "兼容别名失败", path: "/api/v1/bots/stress-test", count: 0, wantStatus: http.StatusBadRequest, failed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := makeRequest(ctx.router, http.MethodPost, test.path, map[string]any{
				"instanceId": ctx.instanceID,
				"count":      test.count,
				"behavior":   "idle",
				"namePrefix": "audit",
				"config": map[string]any{
					"server": "127.0.0.1", "port": 25565, "auth": "offline",
					"username": "audit-user", "password": "credential-must-not-appear",
				},
			}, ctx.token)
			require.Equal(t, test.wantStatus, w.Code)
		})
	}

	var audits []model.AuditLog
	require.NoError(t, db.Where("action = ?", "bot_load.run.create").Order("id ASC").Find(&audits).Error)
	require.Len(t, audits, len(tests))
	for index, audit := range audits {
		require.Equal(t, tests[index].failed, audit.Failed)
		require.Equal(t, "bot_load_run", audit.TargetType)
		require.NotContains(t, audit.Detail, "credential-must-not-appear")
		require.NotContains(t, audit.Error, "credential-must-not-appear")
		detail := strings.ToLower(audit.Detail)
		require.NotContains(t, detail, "config")
		require.NotContains(t, detail, "password")
		require.NotContains(t, detail, "username")
	}
}

func TestBotLoadPreflight_ValidationCapacityReadyAndAuditRedaction(t *testing.T) {
	t.Run("请求校验", func(t *testing.T) {
		_, _, _, ctx := setupBotLoadHTTP(t, 50)
		sessionID := createBotLoadSession(t, ctx, 2)
		invalidRate := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/preflight", map[string]any{
			"connectRatePerSecondPerNode": 51,
		}, ctx.token)
		assert.Equal(t, http.StatusBadRequest, invalidRate.Code)

		ids := make([]uint, 257)
		for index := range ids {
			ids[index] = uint(index + 1)
		}
		tooMany := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/preflight", map[string]any{
			"executorNodeIds": ids,
		}, ctx.token)
		assert.Equal(t, http.StatusBadRequest, tooMany.Code)
	})

	t.Run("容量不足仍返回可展示结果", func(t *testing.T) {
		_, _, _, ctx := setupBotLoadHTTP(t, 1)
		sessionID := createBotLoadSession(t, ctx, 2)
		result := preflightBotLoadSession(t, ctx, sessionID, nil)
		assert.Equal(t, false, result["ready"])
		assert.NotContains(t, result, "planToken")
		blockers := result["blockers"].([]any)
		require.NotEmpty(t, blockers)
		assert.Equal(t, "BOT_LOAD_CAPACITY_INSUFFICIENT", blockers[0].(map[string]any)["code"])
	})

	t.Run("节点去重并签发计划且审计脱敏", func(t *testing.T) {
		db, _, _, ctx := setupBotLoadHTTP(t, 50)
		sessionID := createBotLoadSession(t, ctx, 2)
		result := preflightBotLoadSession(t, ctx, sessionID, map[string]any{
			"executorNodeIds": []uint{ctx.node.ID, ctx.node.ID},
		})
		assert.Equal(t, true, result["ready"])
		assert.NotEmpty(t, result["planToken"])
		require.Len(t, result["allocations"], 1)

		var audit model.AuditLog
		require.NoError(t, db.Where("action = ?", "bot_load.run.preflight").Last(&audit).Error)
		assert.NotContains(t, audit.Detail, result["planToken"].(string))
		assert.NotContains(t, strings.ToLower(audit.Detail), "secret")
		assert.NotContains(t, strings.ToLower(audit.Detail), "config")
	})
}

func TestBotLoadStart_TokenCompatibilityAndIdempotency(t *testing.T) {
	t.Run("计划启动返回202且重复不重复派发", func(t *testing.T) {
		db, _, worker, ctx := setupBotLoadHTTP(t, 50)
		sessionID := createBotLoadSession(t, ctx, 2)
		preflight := preflightBotLoadSession(t, ctx, sessionID, nil)
		token := preflight["planToken"].(string)

		for range 2 {
			w := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/start", map[string]any{"planToken": token}, ctx.token)
			require.Equalf(t, http.StatusAccepted, w.Code, "启动失败: %s", w.Body.String())
			body := parseJSON(t, w)
			assert.Equal(t, "running", body["status"])
			require.Len(t, body["allocations"], 1)
			require.Len(t, body["batches"], 1)
		}
		_, applyCalls := worker.counts()
		assert.Equal(t, 1, applyCalls)
		var botCount, batchCount int64
		require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ?", sessionID).Count(&botCount).Error)
		require.NoError(t, db.Model(&model.BotLoadBatch{}).Where("stress_session_id = ?", sessionID).Count(&batchCount).Error)
		assert.EqualValues(t, 2, botCount)
		assert.EqualValues(t, 1, batchCount)
	})

	t.Run("启动接受后拒绝再次预检且计划不变", func(t *testing.T) {
		db, _, _, ctx := setupBotLoadHTTP(t, 50)
		sessionID := createBotLoadSession(t, ctx, 2)
		preflight := preflightBotLoadSession(t, ctx, sessionID, nil)
		var before model.BotStressSession
		require.NoError(t, db.First(&before, sessionID).Error)

		started := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/start", map[string]any{"planToken": preflight["planToken"]}, ctx.token)
		require.Equal(t, http.StatusAccepted, started.Code)
		again := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/preflight", map[string]any{
			"connectRatePerSecondPerNode": 50,
		}, ctx.token)
		require.Equal(t, http.StatusConflict, again.Code)
		require.Equal(t, "BOT_LOAD_INVALID_STATE", parseJSON(t, again)["error"])
		var after model.BotStressSession
		require.NoError(t, db.First(&after, sessionID).Error)
		require.Equal(t, before.AllocationPlan, after.AllocationPlan)
	})

	t.Run("篡改令牌返回容量变化", func(t *testing.T) {
		_, _, _, ctx := setupBotLoadHTTP(t, 50)
		sessionID := createBotLoadSession(t, ctx, 2)
		preflight := preflightBotLoadSession(t, ctx, sessionID, nil)
		token := preflight["planToken"].(string) + "x"
		w := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/start", map[string]any{"planToken": token}, ctx.token)
		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Equal(t, "BOT_LOAD_CAPACITY_CHANGED", parseJSON(t, w)["error"])
		assert.NotContains(t, w.Body.String(), "签名")
	})

	t.Run("即时容量不足返回422且零副作用", func(t *testing.T) {
		db, _, worker, ctx := setupBotLoadHTTP(t, 50)
		sessionID := createBotLoadSession(t, ctx, 2)
		preflight := preflightBotLoadSession(t, ctx, sessionID, nil)
		worker.mu.Lock()
		worker.activeBots = 50
		worker.mu.Unlock()
		w := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/start", map[string]any{"planToken": preflight["planToken"]}, ctx.token)
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
		assert.Equal(t, "BOT_LOAD_CAPACITY_INSUFFICIENT", parseJSON(t, w)["error"])
		assertBotLoadStartHasNoRows(t, db, sessionID)
	})

	t.Run("节点不可用返回503且零副作用", func(t *testing.T) {
		db, _, worker, ctx := setupBotLoadHTTP(t, 50)
		sessionID := createBotLoadSession(t, ctx, 2)
		preflight := preflightBotLoadSession(t, ctx, sessionID, nil)
		worker.mu.Lock()
		worker.ready = false
		worker.mu.Unlock()
		w := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/start", map[string]any{"planToken": preflight["planToken"]}, ctx.token)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Equal(t, "BOT_LOAD_NODE_UNAVAILABLE", parseJSON(t, w)["error"])
		assertBotLoadStartHasNoRows(t, db, sessionID)
	})

	t.Run("旧V1空body内部单节点预检", func(t *testing.T) {
		db, _, _, ctx := setupBotLoadHTTP(t, 50)
		w := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions", map[string]any{
			"instanceId": ctx.instanceID,
			"count":      2,
			"behavior":   "idle",
			"namePrefix": "legacy",
			"config": map[string]any{
				"server": "127.0.0.1", "port": 25565, "version": "1.20.4", "auth": "offline",
				"username": "legacy-user", "password": "legacy-password",
			},
		}, ctx.token)
		require.Equalf(t, http.StatusCreated, w.Code, "创建 V1 会话失败: %s", w.Body.String())
		sessionID := uint(parseJSON(t, w)["id"].(float64))
		w = makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/start", nil, ctx.token)
		require.Equalf(t, http.StatusAccepted, w.Code, "V1 兼容启动失败: %s", w.Body.String())
		var audit model.AuditLog
		require.NoError(t, db.Where("action = ?", "bot_load.run.start").Last(&audit).Error)
		assert.Contains(t, audit.Detail, `"legacyCompat":true`)
	})

	t.Run("显式V2字段缺token拒绝", func(t *testing.T) {
		db, _, worker, ctx := setupBotLoadHTTP(t, 50)
		created := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions", map[string]any{
			"instanceId": ctx.instanceID,
			"count":      2,
			"behavior":   "idle",
			"namePrefix": "v2",
			"config": map[string]any{
				"server": "127.0.0.1", "port": 25565, "auth": "offline",
				"scenario": map[string]any{"name": "tower-defense"},
			},
		}, ctx.token)
		require.Equal(t, http.StatusCreated, created.Code)
		sessionID := uint(parseJSON(t, created)["id"].(float64))
		w := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/start", nil, ctx.token)
		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Equal(t, "BOT_LOAD_CAPACITY_CHANGED", parseJSON(t, w)["error"])
		_, applyCalls := worker.counts()
		assert.Zero(t, applyCalls)
		assertBotLoadStartHasNoRows(t, db, sessionID)
	})

	t.Run("分布式或已有计划缺token拒绝", func(t *testing.T) {
		_, _, _, ctx := setupBotLoadHTTP(t, 100)
		distributedID := createBotLoadSession(t, ctx, 51)
		w := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(distributedID)+"/start", nil, ctx.token)
		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Equal(t, "BOT_LOAD_CAPACITY_CHANGED", parseJSON(t, w)["error"])

		smallID := createBotLoadSession(t, ctx, 2)
		_ = preflightBotLoadSession(t, ctx, smallID, nil)
		w = makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(smallID)+"/start", nil, ctx.token)
		assert.Equal(t, http.StatusConflict, w.Code)
	})
}

func TestBotLoadStop_ReasonAsyncFailureIdempotencyAndAudit(t *testing.T) {
	t.Run("reason长度与不可达节点不伪装stopped", func(t *testing.T) {
		db, _, worker, ctx := setupBotLoadHTTP(t, 50)
		sessionID := createBotLoadSession(t, ctx, 2)
		preflight := preflightBotLoadSession(t, ctx, sessionID, nil)
		start := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/start", map[string]any{"planToken": preflight["planToken"]}, ctx.token)
		require.Equal(t, http.StatusAccepted, start.Code)

		tooLong := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/stop", map[string]any{"reason": strings.Repeat("停", 256)}, ctx.token)
		assert.Equal(t, http.StatusBadRequest, tooLong.Code)

		worker.mu.Lock()
		worker.applyErr = errors.New("节点不可达")
		worker.mu.Unlock()
		stop := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/stop", map[string]any{"reason": "人工停止"}, ctx.token)
		require.Equalf(t, http.StatusAccepted, stop.Code, "停止失败: %s", stop.Body.String())
		assert.NotEqual(t, "stopped", parseJSON(t, stop)["status"])
		var stopped int64
		require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ? AND status = ?", sessionID, model.BotStatusStopped).Count(&stopped).Error)
		assert.Zero(t, stopped)
		var audit model.AuditLog
		require.NoError(t, db.Where("action = ?", "bot_load.run.stop").Last(&audit).Error)
		assert.Contains(t, audit.Detail, `"reasonLength":4`)
	})

	t.Run("停止命令已接受时重复调用不重复RPC", func(t *testing.T) {
		_, _, worker, ctx := setupBotLoadHTTP(t, 50)
		sessionID := createBotLoadSession(t, ctx, 2)
		preflight := preflightBotLoadSession(t, ctx, sessionID, nil)
		start := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/start", map[string]any{"planToken": preflight["planToken"]}, ctx.token)
		require.Equal(t, http.StatusAccepted, start.Code)
		for range 2 {
			stop := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/stop", nil, ctx.token)
			require.Equal(t, http.StatusAccepted, stop.Code)
			assert.NotEqual(t, "stopped", parseJSON(t, stop)["status"], "accepted 仅表示 Worker 接受停止命令")
		}
		_, applyCalls := worker.counts()
		assert.Equal(t, 2, applyCalls, "一次 start + 一次 stop")
	})
}
