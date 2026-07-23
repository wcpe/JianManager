package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockCP 起一个假 CP，记录 Authorization 与路径。
func mockCP(t *testing.T, handler http.HandlerFunc) (*AgentClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewAgentClient(srv.URL, "jmat_test_token", srv.Client())
	return c, srv
}

func TestAgentClient_Whoami(t *testing.T) {
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v1/agent/whoami", r.URL.Path)
		assert.Equal(t, "Bearer jmat_test_token", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind": "agent", "name": "ci-bot", "tokenId": 1,
		})
	})
	raw, err := c.Whoami(context.Background())
	require.NoError(t, err)
	assert.Contains(t, string(raw), "ci-bot")
}

func TestAgentClient_ListInstances_WithNodeFilter(t *testing.T) {
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/agent/instances", r.URL.Path)
		assert.Equal(t, "7", r.URL.Query().Get("nodeId"))
		_, _ = w.Write([]byte(`[]`))
	})
	raw, err := c.ListInstances(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(raw))
}

func TestAgentClient_Forbidden(t *testing.T) {
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "FORBIDDEN", "message": "写白名单/scope 不足或硬拒绝",
		})
	})
	_, err := c.InstanceStart(context.Background(), 1)
	require.Error(t, err)
	ae, ok := err.(*APIError)
	require.True(t, ok)
	assert.True(t, ae.IsForbidden())
	assert.Equal(t, "写白名单/scope 不足或硬拒绝", ae.Message)
}

func TestAgentClient_UnauthorizedFallbackMessage(t *testing.T) {
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`not-json`))
	})
	_, err := c.Whoami(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "未授权")
}

func TestAgentClient_LifecycleAndMaintenancePaths(t *testing.T) {
	var paths []string
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	ctx := context.Background()
	_, err := c.InstanceStop(ctx, 3)
	require.NoError(t, err)
	_, err = c.InstanceRestart(ctx, 3)
	require.NoError(t, err)
	_, err = c.NodeMaintenanceEnter(ctx, 9)
	require.NoError(t, err)
	_, err = c.NodeMaintenanceLeave(ctx, 9)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"POST /api/v1/agent/instances/3/stop",
		"POST /api/v1/agent/instances/3/restart",
		"POST /api/v1/agent/nodes/9/maintenance/enter",
		"POST /api/v1/agent/nodes/9/maintenance/leave",
	}, paths)
}
