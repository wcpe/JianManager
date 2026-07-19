package router

import (
	"net/http"
	"testing"

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
