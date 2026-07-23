package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-390：Agent 调用流水 HTTP 集成（读成功 / 403 失败 / 401 不刷库 / 查询 API / callCount24h）。

func TestAgentCallLog_WhoamiRecordsAndListAPI(t *testing.T) {
	db, r, adminJWT, node, inst := setupAgentGate(t)
	tokenID, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "call-log-ci",
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
	})

	// 成功 whoami（带 jmagent client 头）
	w := makeRequestWithAgentClient(r, "GET", "/api/v1/agent/whoami", plain, "jmagent")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// list nodes 成功（无 client 头 → unknown）
	w = makeRequest(r, "GET", "/api/v1/agent/nodes", nil, plain)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 写操作 403（空写白名单）
	_, plainNoWrite := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "no-write",
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
		"writeAllowlist":    []string{},
	})
	w = makeRequest(r, "POST", "/api/v1/agent/instances/"+itoa(inst.ID)+"/start", nil, plainNoWrite)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// 401 不产生流水
	before := countCallLogs(t, db)
	w = makeRequest(r, "GET", "/api/v1/agent/whoami", nil, "jmat_invalid_token_xxx")
	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, before, countCallLogs(t, db), "401 不应写入调用流水")

	// 管理员查询流水（按 tokenId）
	w = makeRequest(r, "GET", "/api/v1/agent/call-logs?tokenId="+strconv.FormatUint(uint64(tokenID), 10)+"&page=1&pageSize=20", nil, adminJWT)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var page service.AgentCallLogPage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.GreaterOrEqual(t, page.Total, int64(2))
	actions := map[string]bool{}
	clients := map[string]bool{}
	for _, item := range page.Items {
		actions[item.Action] = true
		clients[item.Client] = true
		assert.Equal(t, tokenID, item.TokenID)
		assert.Equal(t, "call-log-ci", item.TokenName)
	}
	assert.True(t, actions[service.AgentActionWhoami], "应有 whoami 流水")
	assert.True(t, actions[service.AgentActionListNodes], "应有 list_nodes 流水")
	assert.True(t, clients[service.AgentClientJmagent], "whoami 应记 jmagent")

	// 按 client 过滤
	w = makeRequest(r, "GET", "/api/v1/agent/call-logs?client=jmagent", nil, adminJWT)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.GreaterOrEqual(t, page.Total, int64(1))
	for _, item := range page.Items {
		assert.Equal(t, service.AgentClientJmagent, item.Client)
	}

	// 按 success=false 过滤（403 start）
	w = makeRequest(r, "GET", "/api/v1/agent/call-logs?success=false&action="+service.AgentActionInstanceStart, nil, adminJWT)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.GreaterOrEqual(t, page.Total, int64(1))
	assert.False(t, page.Items[0].Success)

	// Token 列表含 lastUsedAt / callCount24h
	w = makeRequest(r, "GET", "/api/v1/agent/tokens", nil, adminJWT)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var tokens []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &tokens))
	require.NotEmpty(t, tokens)
	var found bool
	for _, tok := range tokens {
		id, _ := tok["id"].(float64)
		if uint(id) != tokenID {
			continue
		}
		found = true
		assert.NotNil(t, tok["lastUsedAt"], "lastUsedAt 应序列化")
		cnt, ok := tok["callCount24h"].(float64)
		require.True(t, ok, "callCount24h 字段缺失: %v", tok)
		assert.GreaterOrEqual(t, cnt, float64(2))
	}
	assert.True(t, found, "列表应含刚签发的 token")
}

func TestAgentCallLog_NonAdminForbidden(t *testing.T) {
	_, r, _, _, _ := setupAgentGate(t)
	memberJWT := getMemberToken(t, r, "agent-call-user", "password123")
	w := makeRequest(r, "GET", "/api/v1/agent/call-logs", nil, memberJWT)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestAgentCallLog_UnknownClientDefault(t *testing.T) {
	db, r, adminJWT, node, inst := setupAgentGate(t)
	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "no-header",
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
	})
	w := makeRequest(r, "GET", "/api/v1/agent/whoami", nil, plain)
	require.Equal(t, http.StatusOK, w.Code)

	var row model.AgentCallLog
	require.NoError(t, db.Where("action = ?", service.AgentActionWhoami).Order("id DESC").First(&row).Error)
	assert.Equal(t, service.AgentClientUnknown, row.Client)
	assert.True(t, row.Success)
	assert.Equal(t, "http", row.Transport)
}

// makeRequestWithAgentClient 发起 Agent 请求并设置 X-JM-Agent-Client。
func makeRequestWithAgentClient(r *gin.Engine, method, path, token, client string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBuffer(nil))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if client != "" {
		req.Header.Set("X-JM-Agent-Client", client)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func countCallLogs(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&model.AgentCallLog{}).Count(&n).Error)
	return n
}
