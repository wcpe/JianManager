package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient 指向 mock 服务器，注入 token。
func newTestClient(ts *httptest.Server, token string) *Client {
	c := newClient(Config{CPURL: ts.URL, Token: token})
	c.httpClient = ts.Client()
	return c
}

// TestClient_WhoamiSuccess 成功路径：Bearer 头正确，默认带 X-JM-Agent-Client，JSON 原样返回。
func TestClient_WhoamiSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v1/agent/whoami", r.URL.Path)
		assert.Equal(t, "Bearer jmat_test_token", r.Header.Get("Authorization"))
		assert.Equal(t, "jmagent", r.Header.Get("X-JM-Agent-Client"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind":              "agent",
			"name":              "ci-bot",
			"tokenId":           7,
			"scopedInstanceIds": []uint{1, 2},
			"scopedNodeIds":     []uint{3},
			"writeAllowlist":    []string{"instance.life"},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts, "jmat_test_token")
	body, err := c.get("/api/v1/agent/whoami", nil)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m))
	assert.Equal(t, "agent", m["kind"])
	assert.Equal(t, "ci-bot", m["name"])
}

// TestClient_Forbidden403 403 时返回 *APIError，message 取 CP 中文文案。
func TestClient_Forbidden403(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"FORBIDDEN","message":"写白名单/scope 不足或硬拒绝"}`))
	}))
	defer ts.Close()

	c := newTestClient(ts, "jmat_no_write")
	_, err := c.post("/api/v1/agent/instances/1/start")
	require.Error(t, err)

	ae, ok := asAPIError(err)
	require.True(t, ok)
	assert.Equal(t, 403, ae.Status)
	assert.Equal(t, "FORBIDDEN", ae.Code)
	assert.Equal(t, "写白名单/scope 不足或硬拒绝", ae.Message)
	assert.Contains(t, err.Error(), "写白名单")
}

// TestClient_Unauthorized401 401 兜底中文。
func TestClient_Unauthorized401(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"UNAUTHORIZED","message":"需要 Agent Token"}`))
	}))
	defer ts.Close()

	c := newTestClient(ts, "bad")
	_, err := c.get("/api/v1/agent/whoami", nil)
	ae, ok := asAPIError(err)
	require.True(t, ok)
	assert.Equal(t, 401, ae.Status)
	assert.Contains(t, ae.Message, "Agent Token")
}

// TestClient_ListInstancesWithNodeQuery 透传 nodeId 查询参数。
func TestClient_ListInstancesWithNodeQuery(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/agent/instances", r.URL.Path)
		assert.Equal(t, "5", r.URL.Query().Get("nodeId"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()

	c := newTestClient(ts, "jmat_x")
	q := map[string][]string{"nodeId": {"5"}}
	body, err := c.get("/api/v1/agent/instances", q)
	require.NoError(t, err)
	assert.Equal(t, "[]", strings.TrimSpace(string(body)))
}

// TestClient_TokenNotInBody Token 只出现在 Authorization，不进 URL/Body。
func TestClient_TokenNotInBody(t *testing.T) {
	const secret = "jmat_super_secret_never_log"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// URL 与 body 不得含 token
		assert.NotContains(t, r.URL.String(), secret)
		b, _ := io.ReadAll(r.Body)
		assert.NotContains(t, string(b), secret)
		assert.Equal(t, "Bearer "+secret, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	c := newTestClient(ts, secret)
	_, err := c.post("/api/v1/agent/nodes/1/maintenance/enter")
	require.NoError(t, err)
}
