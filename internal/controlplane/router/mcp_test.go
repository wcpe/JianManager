package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/mcp"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-389：CP 内嵌 MCP 鉴权、tools/list、会话列表/踢线。

func setupMCP(t *testing.T) (db *gorm.DB, r interface{ ServeHTTP(http.ResponseWriter, *http.Request) }, adminJWT string, agentPlain string, inst *model.Instance) {
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

func mcpPOST(t *testing.T, r interface{ ServeHTTP(http.ResponseWriter, *http.Request) }, path, token, sessionID string, body any) *httptest.ResponseRecorder {
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

func TestMCP_InitializeAndToolsList(t *testing.T) {
	_, r, _, plain, _ := setupMCP(t)

	// initialize
	w := mcpPOST(t, r, "/api/v1/mcp", plain, "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "0"}},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	sid := w.Header().Get(mcp.HeaderSessionID)
	require.NotEmpty(t, sid)
	var initResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &initResp))
	assert.Equal(t, "2.0", initResp["jsonrpc"])
	result, ok := initResp["result"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, mcp.ProtocolVersion, result["protocolVersion"])

	// tools/list
	w = mcpPOST(t, r, "/api/v1/mcp", plain, sid, map[string]any{
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

func TestMCP_ToolsCall_Whoami(t *testing.T) {
	db, r, adminJWT, plain, _ := setupMCP(t)
	w := mcpPOST(t, r, "/api/v1/mcp", plain, "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	})
	require.Equal(t, http.StatusOK, w.Code)
	sid := w.Header().Get(mcp.HeaderSessionID)

	w = mcpPOST(t, r, "/api/v1/mcp", plain, sid, map[string]any{
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

	// FR-390：MCP tool 应记 client=mcp 流水；initialize 记 session.open
	var openN, whoamiN int64
	require.NoError(t, db.Model(&model.AgentCallLog{}).Where("action = ? AND client = ?", "mcp.session.open", "mcp").Count(&openN).Error)
	require.NoError(t, db.Model(&model.AgentCallLog{}).Where("action = ? AND client = ?", "agent.whoami", "mcp").Count(&whoamiN).Error)
	assert.GreaterOrEqual(t, openN, int64(1), "应有会话 open 流水")
	assert.GreaterOrEqual(t, whoamiN, int64(1), "应有 agent.whoami 流水")

	// 管理端 call-logs 可查到 mcp 客户端
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/call-logs?client=mcp", nil)
	req.Header.Set("Authorization", "Bearer "+adminJWT)
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, req)
	require.Equal(t, http.StatusOK, lw.Code, lw.Body.String())
	assert.Contains(t, lw.Body.String(), "agent.whoami")
}

func TestMCP_AdminListAndKick(t *testing.T) {
	_, r, adminJWT, plain, _ := setupMCP(t)
	w := mcpPOST(t, r, "/api/v1/mcp", plain, "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	})
	require.Equal(t, http.StatusOK, w.Code)
	sid := w.Header().Get(mcp.HeaderSessionID)
	require.NotEmpty(t, sid)

	// 列表（管理员 JWT）
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/mcp/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+adminJWT)
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, req)
	require.Equal(t, http.StatusOK, lw.Code, lw.Body.String())
	var list struct {
		Sessions []struct {
			SessionID   string `json:"sessionId"`
			TokenName   string `json:"tokenName"`
			TokenPrefix string `json:"tokenPrefix"`
			Transport   string `json:"transport"`
		} `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(lw.Body.Bytes(), &list))
	require.NotEmpty(t, list.Sessions)
	found := false
	for _, s := range list.Sessions {
		if s.SessionID == sid {
			found = true
			assert.Equal(t, "mcp-test", s.TokenName)
			assert.Equal(t, mcp.TransportStreamableHTTP, s.Transport)
			assert.NotEmpty(t, s.TokenPrefix)
		}
	}
	assert.True(t, found, "列表应含刚建立的会话")

	// 踢线
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/agent/mcp/sessions/"+sid, nil)
	req.Header.Set("Authorization", "Bearer "+adminJWT)
	kw := httptest.NewRecorder()
	r.ServeHTTP(kw, req)
	require.Equal(t, http.StatusOK, kw.Code, kw.Body.String())

	// 踢线后 tools/call 失败
	w = mcpPOST(t, r, "/api/v1/mcp", plain, sid, map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "tools/call",
		"params": map[string]any{"name": "agent_whoami"},
	})
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
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
	w := mcpPOST(t, r, "/api/v1/mcp", plain, "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	})
	require.Equal(t, http.StatusOK, w.Code)
	sid := w.Header().Get(mcp.HeaderSessionID)

	// scope 外实例
	w = mcpPOST(t, r, "/api/v1/mcp", plain, sid, map[string]any{
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
