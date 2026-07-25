package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

const routerStressOrchestrationYAML = `loop: true
staggerMs: 500
phases:
  - durationSec: 60
    behavior: idle
  - durationSec: 90
    behavior: custom
    steps:
      - type: wait
        durationMs: 1000
`

func TestBotStressSession_Flow(t *testing.T) {
	_, _, _, ctx := setupBotLoadHTTP(t, 50)
	r := ctx.router
	token := ctx.token
	inst := ctx.instanceID

	body := map[string]interface{}{
		"instanceId": inst,
		"count":      2,
		"behavior":   "idle",
		"namePrefix": "load",
		"config":     map[string]interface{}{"server": "127.0.0.1", "port": 25565},
	}
	w := makeRequest(r, "POST", "/api/v1/bots/stress-sessions", body, token)
	require.Equalf(t, http.StatusCreated, w.Code, "创建压测会话失败: %s", w.Body.String())
	created := parseJSON(t, w)
	sessionID := uint(created["id"].(float64))
	assert.Equal(t, "pending", created["status"])

	w = makeRequest(r, "POST", "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/start", nil, token)
	require.Equalf(t, http.StatusAccepted, w.Code, "启动压测会话失败: %s", w.Body.String())
	started := parseJSON(t, w)
	assert.Equal(t, "running", started["status"])
	counts := started["counts"].(map[string]interface{})
	assert.Equal(t, float64(2), counts["total"])

	w = makeRequest(r, "GET", "/api/v1/bots/stress-sessions", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	list := parseJSON(t, w)
	items := list["items"].([]interface{})
	require.Len(t, items, 1)
	item := items[0].(map[string]interface{})
	assert.Equal(t, "running", item["status"])

	w = makeRequest(r, "POST", "/api/v1/bots/stress-sessions/"+itoa(sessionID)+"/stop", nil, token)
	require.Equalf(t, http.StatusAccepted, w.Code, "停止压测会话失败: %s", w.Body.String())
	stopping := parseJSON(t, w)
	assert.Equal(t, "running", stopping["status"], "accepted 仅表示 Worker 接受停止命令")
	stoppingCounts := stopping["counts"].(map[string]interface{})
	byStatus := stoppingCounts["byStatus"].(map[string]interface{})
	assert.Equal(t, float64(2), byStatus[string(model.BotStatusConnecting)])
	assert.NotContains(t, byStatus, string(model.BotStatusStopped))
}

// TestBotStressSession_ReportAndStream FR-370：终态报告 HTTP + 最小 SSE 首帧。
func TestBotStressSession_ReportAndStream(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	inst := createInstanceViaAPI(t, r, token, 1, createGroupViaAPI(t, r, token, "g-report"))

	// 直接落库 V2 终态会话（报告仅 schemaVersion=2 且终态可导出）。
	runState := model.BotLoadRunCompleted
	verdict := model.BotLoadVerdictPassed
	maxStable := 3
	sess := &model.BotStressSession{
		InstanceID: inst, Name: "report-run", NamePrefix: "r", BotCount: 3,
		Status: model.BotStressSessionStopped, SchemaVersion: 2,
		RunState: &runState, Verdict: &verdict, MaxStableBots: &maxStable,
		Config:         `{"server":"127.0.0.1","port":25565}`,
		FailureSummary: `{"command_send_failed":1}`,
		ReportSummary:  "ok",
	}
	require.NoError(t, db.Create(sess).Error)

	// 非终态拒绝
	pendingState := model.BotLoadRunRunning
	pending := &model.BotStressSession{
		InstanceID: inst, Name: "pending-run", NamePrefix: "p", BotCount: 1,
		Status: model.BotStressSessionRunning, SchemaVersion: 2, RunState: &pendingState,
		Config: `{"server":"127.0.0.1","port":25565}`,
	}
	require.NoError(t, db.Create(pending).Error)
	w := makeRequest(r, "GET", "/api/v1/bots/stress-sessions/"+itoa(pending.ID)+"/report", nil, token)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "BOT_LOAD_REPORT_NOT_READY", parseJSON(t, w)["error"])

	// JSON 报告
	w = makeRequest(r, "GET", "/api/v1/bots/stress-sessions/"+itoa(sess.ID)+"/report?format=json", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	rep := parseJSON(t, w)
	assert.Equal(t, float64(sess.ID), rep["runId"])
	assert.Equal(t, "completed", rep["runState"])
	assert.Equal(t, "passed", rep["verdict"])
	assert.Contains(t, rep["disclaimer"].(string), "bot.chat")

	// CSV 报告
	w = makeRequest(r, "GET", "/api/v1/bots/stress-sessions/"+itoa(sess.ID)+"/report?format=csv", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/csv")
	assert.Contains(t, w.Body.String(), "runId")
	assert.Contains(t, w.Body.String(), sess.UUID)

	// SSE：读到 connected + snapshot 即可（短超时客户端）
	req, err := http.NewRequest(http.MethodGet, "/api/v1/bots/stress-sessions/"+itoa(sess.ID)+"/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	// 使用 httptest recorder 只能看到缓冲；直接调 handler 经 makeRequest 不适合长连接。
	// 这里用短读：gin 测试引擎 ServeHTTP 会跑到 ctx cancel——用带 cancel 的 context 不方便。
	// 改为断言路由存在且 200 + Content-Type event-stream（首包写入后客户端断开由测试侧不读完）。
	// 简化：用 makeRequest 会阻塞到超时；改用自定义 recorder + 立即 cancel 不可行。
	// 采用：启动 goroutine 读 body 前几字节。
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ServeHTTP(rec, req)
	}()
	// 等首包
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.Body.String(), "event: connected") && strings.Contains(rec.Body.String(), "event: snapshot") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	body := rec.Body.String()
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, body, "event: connected")
	assert.Contains(t, body, "event: snapshot")
	assert.Contains(t, body, `"sessionId":`)
	// 取消请求上下文：关闭底层连接不可直接，进程级测试结束即可。
	_ = done
}

// TestBotStressSession_Metrics FR-370：5s 样本可读。
func TestBotStressSession_Metrics(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	inst := createInstanceViaAPI(t, r, token, 1, createGroupViaAPI(t, r, token, "g-metric"))
	runState := model.BotLoadRunRunning
	stage := 0
	sess := &model.BotStressSession{
		InstanceID: inst, Name: "metric-run", NamePrefix: "m", BotCount: 1,
		Status: model.BotStressSessionRunning, SchemaVersion: 2, RunState: &runState, CurrentStage: &stage,
		Config: `{"server":"127.0.0.1","port":25565}`,
	}
	require.NoError(t, db.Create(sess).Error)
	ts := time.Date(2026, 7, 25, 5, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&model.BotLoadMetricSample{
		StressSessionID: sess.ID, SampledAt: ts, StageIndex: 0,
		CountsJSON: `{"planned":1,"total":1,"connected":1}`, CommandJSON: `{"sent":1}`,
		BarrierJSON: `{}`, ExecutorJSON: `[]`, LatencyJSON: `{}`, ErrorsJSON: `{}`,
	}).Error)

	w := makeRequest(r, "GET", "/api/v1/bots/stress-sessions/"+itoa(sess.ID)+"/metrics?resolution=raw", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, "raw", body["resolution"])
	items, ok := body["items"].([]interface{})
	require.True(t, ok)
	require.Len(t, items, 1)
	first := items[0].(map[string]interface{})
	counts := first["counts"].(map[string]interface{})
	assert.Equal(t, float64(1), counts["connected"])
}

func TestBotStressSession_GetDetailReturnsOrchestration(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	inst := createInstanceViaAPI(t, r, token, 1, createGroupViaAPI(t, r, token, "g"))

	w := makeRequest(r, "POST", "/api/v1/bots/stress-sessions", map[string]interface{}{
		"instanceId":        inst,
		"count":             2,
		"namePrefix":        "load",
		"orchestrationYaml": routerStressOrchestrationYAML,
	}, token)
	require.Equalf(t, http.StatusCreated, w.Code, "创建压测会话失败: %s", w.Body.String())
	created := parseJSON(t, w)
	sessionID := uint(created["id"].(float64))
	assert.Equal(t, "idle", created["behavior"])

	w = makeRequest(r, "GET", "/api/v1/bots/stress-sessions/"+itoa(sessionID), nil, token)
	require.Equalf(t, http.StatusOK, w.Code, "查询压测会话失败: %s", w.Body.String())
	detail := parseJSON(t, w)
	assert.Equal(t, routerStressOrchestrationYAML, detail["orchestrationYaml"])
	summary := detail["orchestrationSummary"].(map[string]interface{})
	assert.Equal(t, true, summary["enabled"])
	assert.Equal(t, float64(2), summary["phaseCount"])
}

func TestBotStressSession_GetLegacyDetailRedactsInternalScenarioFields(t *testing.T) {
	_, _, _, ctx := setupBotLoadHTTP(t, 50)
	legacyYAML := "phases:\n  - durationSec: 10\n    behavior: custom\n    steps:\n      - type: attack\n"
	created := makeRequest(ctx.router, http.MethodPost, "/api/v1/bots/stress-sessions", map[string]interface{}{
		"instanceId": ctx.instanceID, "count": 1, "namePrefix": "legacy", "orchestrationYaml": legacyYAML,
	}, ctx.token)
	require.Equalf(t, http.StatusCreated, created.Code, "创建 legacy 会话失败: %s", created.Body.String())
	createdBody := parseJSON(t, created)
	sessionID := uint(createdBody["id"].(float64))

	detail := makeRequest(ctx.router, http.MethodGet, "/api/v1/bots/stress-sessions/"+itoa(sessionID), nil, ctx.token)
	require.Equalf(t, http.StatusOK, detail.Code, "查询 legacy 会话失败: %s", detail.Body.String())
	body := parseJSON(t, detail)
	assert.NotContains(t, body, "scenario")
	assert.Equal(t, "custom", body["behavior"])
	assert.Equal(t, legacyYAML, body["orchestrationYaml"])
	assert.Contains(t, body, "orchestrationSummary")
	assert.NotContains(t, detail.Body.String(), "legacyDurationSuccess")
	assert.NotContains(t, detail.Body.String(), "legacy_behavior")
}

func TestBotStressSession_CreateValidation(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	inst := createInstanceViaAPI(t, r, token, 1, createGroupViaAPI(t, r, token, "g"))

	w := makeRequest(r, "POST", "/api/v1/bots/stress-sessions", map[string]interface{}{
		"instanceId": inst,
		"count":      0,
		"behavior":   "idle",
		"namePrefix": "load",
	}, token)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestBotStressSession_CrossGroupIsolation(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	groupA := createGroupViaAPI(t, r, adminToken, "组A")
	groupB := createGroupViaAPI(t, r, adminToken, "组B")
	instA := createInstanceViaAPI(t, r, adminToken, 1, groupA)
	instB := createInstanceViaAPI(t, r, adminToken, 1, groupB)

	aliceToken := getMemberToken(t, r, "alice", "password123")
	aliceID := findUserIDByUsername(t, db, "alice")
	addMemberViaAPI(t, r, adminToken, groupA, aliceID, model.GroupMemberRoleMember)

	for _, inst := range []uint{instA, instB} {
		w := makeRequest(r, "POST", "/api/v1/bots/stress-sessions", map[string]interface{}{
			"instanceId": inst,
			"count":      1,
			"behavior":   "idle",
			"namePrefix": "load",
		}, adminToken)
		require.Equal(t, http.StatusCreated, w.Code)
	}

	w := makeRequest(r, "POST", "/api/v1/bots/stress-sessions", map[string]interface{}{
		"instanceId": instB,
		"count":      1,
		"behavior":   "idle",
		"namePrefix": "load",
	}, aliceToken)
	assert.Equal(t, http.StatusForbidden, w.Code)

	w = makeRequest(r, "GET", "/api/v1/bots/stress-sessions", nil, aliceToken)
	require.Equal(t, http.StatusOK, w.Code)
	list := parseJSON(t, w)
	items := list["items"].([]interface{})
	require.Len(t, items, 1)
	item := items[0].(map[string]interface{})
	assert.Equal(t, float64(instA), item["instanceId"])
}
