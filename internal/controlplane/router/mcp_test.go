package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/mcp"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-389 / FR-489：CP 内嵌 MCP 鉴权、tools/list、tools/call 与 Token 维度活动视图。
// 无状态化（ADR-096）：Streamable HTTP 不再有会话，故不再断言 Mcp-Session-Id。

func setupMCP(t *testing.T) (db *gorm.DB, r *gin.Engine, adminJWT string, agentPlain string, inst *model.Instance) {
	t.Helper()
	db, engine, adminJWT, node, inst := setupAgentGate(t)
	_, agentPlain = issueAgentPlaintext(t, engine, adminJWT, map[string]any{
		"name":              "mcp-test",
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
		"writeAllowlist":    []string{"instance.life", "node.maintenance"},
		"ttlDays":           30,
	})
	return db, engine, adminJWT, agentPlain, inst
}

// mcpPOST 调 MCP Streamable HTTP 端点；sessionID 非空时额外携带会话头（用于验证被忽略）。
func mcpPOST(t *testing.T, r *gin.Engine, path, token, sessionID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID != "" {
		req.Header.Set(mcp.HeaderSessionID, sessionID)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMCP_AuthFailure_NoToken(t *testing.T) {
	_, r, _, _, _ := setupMCP(t)
	w := mcpPOST(t, r, "/api/v1/mcp", "", "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	})
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

func TestMCP_AuthFailure_InvalidToken(t *testing.T) {
	_, r, _, _, _ := setupMCP(t)
	w := mcpPOST(t, r, "/api/v1/mcp", "jmat_not_a_real_token_xxxxx", "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	})
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

// TestMCP_InitializeAndToolsList 无状态：initialize 不下发会话头，后续调用无需会话。
func TestMCP_InitializeAndToolsList(t *testing.T) {
	_, r, _, plain, _ := setupMCP(t)

	w := mcpPOST(t, r, "/api/v1/mcp", plain, "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "0"}},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, w.Header().Get(mcp.HeaderSessionID), "无状态化后不得下发 Mcp-Session-Id")
	var initResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &initResp))
	assert.Equal(t, "2.0", initResp["jsonrpc"])
	result, ok := initResp["result"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, mcp.ProtocolVersion, result["protocolVersion"])

	// tools/list 不携带任何会话 id 也应正常。
	w = mcpPOST(t, r, "/api/v1/mcp", plain, "", map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, w.Header().Get(mcp.HeaderSessionID))
	var listResp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	names := map[string]bool{}
	for _, tl := range listResp.Result.Tools {
		names[tl.Name] = true
	}
	for _, n := range []string{
		"agent_whoami", "agent_list_nodes", "agent_list_instances",
		"agent_get_instance", "agent_get_instance_metrics", "agent_get_instance_logs",
		"instance_start", "instance_stop", "instance_restart",
		"node_maintenance_enter", "node_maintenance_leave",
	} {
		assert.True(t, names[n], "应包含 tool %s", n)
	}
	assert.False(t, names["user_create"])
	assert.False(t, names["kill_instance"])
}

// TestMCP_StaleSessionHeaderIgnored 携带陈旧/陌生 Mcp-Session-Id 不被拒绝（ADR-096 后果）。
//
// 这正是无状态化要修的故障面：会话语义下客户端会收到 404 SESSION_GONE 后永久卡死。
func TestMCP_StaleSessionHeaderIgnored(t *testing.T) {
	_, r, _, plain, _ := setupMCP(t)
	w := mcpPOST(t, r, "/api/v1/mcp", plain, "mcps_stale_from_previous_run", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, w.Header().Get(mcp.HeaderSessionID))
	assert.NotContains(t, w.Body.String(), "SESSION_GONE")
}

// TestMCP_GETReturns405 GET /api/v1/mcp 改为 405 且带 Allow: POST。
func TestMCP_GETReturns405(t *testing.T) {
	_, r, _, plain, _ := setupMCP(t)
	w := makeRequest(r, http.MethodGet, "/api/v1/mcp", nil, plain)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code, w.Body.String())
	assert.Equal(t, "POST", w.Header().Get("Allow"))
}

func TestMCP_ToolsCall_Whoami(t *testing.T) {
	db, r, adminJWT, plain, _ := setupMCP(t)
	w := mcpPOST(t, r, "/api/v1/mcp", plain, "", map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "agent_whoami", "arguments": map[string]any{}},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.Result.IsError)
	require.NotEmpty(t, resp.Result.Content)
	assert.Contains(t, resp.Result.Content[0].Text, "mcp-test")

	// FR-390：MCP tool 记 client=mcp 流水（会话类流水已随无状态化移除，只剩调用类）。
	var whoamiN int64
	require.NoError(t, db.Model(&model.AgentCallLog{}).Where("action = ? AND client = ?", "agent.whoami", "mcp").Count(&whoamiN).Error)
	assert.GreaterOrEqual(t, whoamiN, int64(1), "应有 agent.whoami 流水")

	// 管理端 call-logs 可查到 mcp 客户端
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/call-logs?client=mcp", nil)
	req.Header.Set("Authorization", "Bearer "+adminJWT)
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, req)
	require.Equal(t, http.StatusOK, lw.Code, lw.Body.String())
	assert.Contains(t, lw.Body.String(), "agent.whoami")
}

// ---- 活动视图（取代会话列表/踢线）----

type mcpActivityResp struct {
	Window string `json:"window"`
	Items  []struct {
		TokenID        uint             `json:"tokenId"`
		TokenName      string           `json:"tokenName"`
		TokenPrefix    string           `json:"tokenPrefix"`
		LastActivityAt string           `json:"lastActivityAt"`
		LastAction     string           `json:"lastAction"`
		CallCount      int64            `json:"callCount"`
		FailureCount   int64            `json:"failureCount"`
		ClientIPs      []string         `json:"clientIPs"`
		Clients        map[string]int64 `json:"clients"`
	} `json:"items"`
}

// TestMCP_ActivityAggregatesByToken 活动端点按 Token 聚合调用流水（ADR-096 决策 6）。
func TestMCP_ActivityAggregatesByToken(t *testing.T) {
	_, r, adminJWT, plain, _ := setupMCP(t)

	for i := 0; i < 2; i++ {
		w := mcpPOST(t, r, "/api/v1/mcp", plain, "", map[string]any{
			"jsonrpc": "2.0", "id": i + 1, "method": "tools/call",
			"params": map[string]any{"name": "agent_whoami", "arguments": map[string]any{}},
		})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}

	w := makeRequest(r, http.MethodGet, "/api/v1/agent/mcp/activity?window=24h", nil, adminJWT)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp mcpActivityResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// window 回显实际生效窗口的 Go duration 字符串形态（见 docs/API.md）。
	assert.Equal(t, "24h0m0s", resp.Window)
	require.Len(t, resp.Items, 1, "窗口内只应有一个 Token 活动")

	item := resp.Items[0]
	assert.NotZero(t, item.TokenID)
	assert.Equal(t, "mcp-test", item.TokenName)
	assert.NotEmpty(t, item.TokenPrefix)
	assert.Equal(t, "agent.whoami", item.LastAction)
	assert.Equal(t, int64(2), item.CallCount)
	assert.Equal(t, int64(0), item.FailureCount)
	assert.NotEmpty(t, item.LastActivityAt)
	assert.NotEmpty(t, item.ClientIPs, "来源 IP 应取自调用流水")
	assert.Equal(t, int64(2), item.Clients["mcp"], "客户端分布应含 mcp")

	// 未显式给 window 时默认 24h。
	w = makeRequest(r, http.MethodGet, "/api/v1/agent/mcp/activity", nil, adminJWT)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var def mcpActivityResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &def))
	assert.Equal(t, "24h0m0s", def.Window)
}

// TestMCP_Activity_WindowValidation 窗口越界/非法回 400。
func TestMCP_Activity_WindowValidation(t *testing.T) {
	_, r, adminJWT, _, _ := setupMCP(t)
	for _, bad := range []string{"10m", "169h", "abc", "24"} {
		w := makeRequest(r, http.MethodGet, "/api/v1/agent/mcp/activity?window="+bad, nil, adminJWT)
		assert.Equal(t, http.StatusBadRequest, w.Code, "window=%s 应回 400: %s", bad, w.Body.String())
	}
	for _, ok := range []string{"1h", "168h"} {
		w := makeRequest(r, http.MethodGet, "/api/v1/agent/mcp/activity?window="+ok, nil, adminJWT)
		assert.Equal(t, http.StatusOK, w.Code, "window=%s 应被接受: %s", ok, w.Body.String())
	}
}

// TestMCP_Activity_NonAdminForbidden 权限门复用 agent.mcp.read。
func TestMCP_Activity_NonAdminForbidden(t *testing.T) {
	_, r, _, _, _ := setupMCP(t)
	memberJWT := getMemberToken(t, r, "mcp-activity-user", "password123")
	w := makeRequest(r, http.MethodGet, "/api/v1/agent/mcp/activity", nil, memberJWT)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

// TestMCP_SessionsEndpointsRemoved 会话列表/踢线端点已随无状态化移除。
func TestMCP_SessionsEndpointsRemoved(t *testing.T) {
	_, r, adminJWT, _, _ := setupMCP(t)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		path := "/api/v1/agent/mcp/sessions"
		if method == http.MethodDelete {
			path += "/mcps_whatever"
		}
		w := makeRequest(r, method, path, nil, adminJWT)
		assert.Equal(t, http.StatusNotFound, w.Code, "%s %s 应已移除: %s", method, path, w.Body.String())
	}
}

func TestMCP_HumanJWT_CannotOpenSession(t *testing.T) {
	_, r, adminJWT, _, _ := setupMCP(t)
	// 用人类 JWT 调 MCP
	w := mcpPOST(t, r, "/api/v1/mcp", adminJWT, "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	})
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

func TestMCP_ScopeDeniedIsErrorNot5xx(t *testing.T) {
	_, r, _, plain, _ := setupMCP(t)
	// scope 外实例
	w := mcpPOST(t, r, "/api/v1/mcp", plain, "", map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{
			"name":      "instance_start",
			"arguments": map[string]any{"id": float64(99999)},
		},
	})
	require.Equal(t, http.StatusOK, w.Code, "策略拒绝须 HTTP 200 + isError，不得 5xx")
	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Nil(t, resp.Error)
	assert.True(t, resp.Result.IsError)
	require.NotEmpty(t, resp.Result.Content)
	assert.NotEmpty(t, resp.Result.Content[0].Text)
}

// TestMCP_V2_ToolsListDynamicFilter V2 Token 的 tools/list 按能力动态裁剪。
//
// 无状态化后能力判定取每请求重建的 principal（原为 initialize 时的会话快照）。
func TestMCP_V2_ToolsListDynamicFilter(t *testing.T) {
	_, r, adminJWT, _, inst := setupAgentGate(t)

	// 只有 observability.read 的 V2 Token
	_, obs := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "v2-obs",
		"policyVersion":     2,
		"capabilities":      []string{service.AgentCapabilityObservabilityRead},
		"scopedInstanceIds": []uint{inst.ID},
	})

	w := mcpPOST(t, r, "/api/v1/mcp", obs, "", map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var listResp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	names := map[string]bool{}
	for _, tl := range listResp.Result.Tools {
		names[tl.Name] = true
	}
	// agent_whoami 对所有有效 Token 可见
	assert.True(t, names["agent_whoami"])
	// observability.read 可见指标与日志
	assert.True(t, names["agent_get_instance_metrics"])
	assert.True(t, names["agent_get_instance_logs"])
	// 无 instance.life → 生命周期工具不可见
	assert.False(t, names["instance_start"])
	assert.False(t, names["instance_stop"])
	// 无 node.read → 节点工具不可见
	assert.False(t, names["agent_list_nodes"])
	assert.False(t, names["node_maintenance_enter"])

	// 手工调用未列出的工具仍须 isError（不能靠 list 裁剪绕过）
	w = mcpPOST(t, r, "/api/v1/mcp", obs, "", map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "instance_start",
			"arguments": map[string]any{"id": float64(inst.ID)},
		},
	})
	require.Equal(t, http.StatusOK, w.Code)
	var callResp struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &callResp))
	assert.True(t, callResp.Result.IsError, "未列出的工具仍须被 tools/call 拒绝")
}

// TestMCP_V2_EmptyCapabilityOnlyWhoami 空能力 V2 Token 的 tools/list 只含 whoami。
func TestMCP_V2_EmptyCapabilityOnlyWhoami(t *testing.T) {
	_, r, adminJWT, node, inst := setupAgentGate(t)
	_, emptyPlain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "v2-zero",
		"policyVersion":     2,
		"capabilities":      []string{},
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
	})

	w := mcpPOST(t, r, "/api/v1/mcp", emptyPlain, "", map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	require.Equal(t, http.StatusOK, w.Code)
	var listResp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	require.Len(t, listResp.Result.Tools, 1)
	assert.Equal(t, "agent_whoami", listResp.Result.Tools[0].Name)
}
