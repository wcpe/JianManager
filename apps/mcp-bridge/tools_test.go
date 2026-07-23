package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisteredTools_OnlyAllowedSubset(t *testing.T) {
	tools := RegisteredTools()
	names := make(map[string]bool, len(tools))
	for _, tl := range tools {
		names[tl.Name] = true
		assert.NotEmpty(t, tl.Description)
		assert.NotNil(t, tl.InputSchema)
	}
	// 约定工具集
	want := []string{
		"agent_whoami",
		"agent_list_nodes",
		"agent_list_instances",
		"agent_get_instance",
		"agent_get_instance_metrics",
		"instance_start",
		"instance_stop",
		"instance_restart",
		"node_maintenance_enter",
		"node_maintenance_leave",
	}
	assert.Len(t, tools, len(want))
	for _, n := range want {
		assert.True(t, names[n], "应注册 %s", n)
	}
	// 硬拒绝面示例：不得出现
	hardDenied := []string{
		"user_create", "delete_instance", "kill_instance",
		"db_browse", "self_update", "audit_delete", "agent_get_instance_logs",
	}
	for _, n := range hardDenied {
		assert.False(t, names[n], "硬拒绝/未约定工具不得注册: %s", n)
	}
}

func TestCallTool_WhoamiSuccess(t *testing.T) {
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": "agent", "name": "t"})
	})
	res := CallTool(context.Background(), c, "agent_whoami", nil)
	assert.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "text", res.Content[0].Type)
	assert.Contains(t, res.Content[0].Text, "agent")
}

func TestCallTool_ForbiddenIsErrorChinese(t *testing.T) {
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "FORBIDDEN", "message": "写白名单/scope 不足或硬拒绝",
		})
	})
	res := CallTool(context.Background(), c, "instance_start", map[string]any{"id": float64(1)})
	assert.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "写白名单/scope 不足或硬拒绝", res.Content[0].Text)
}

func TestCallTool_MissingID(t *testing.T) {
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("不应发 HTTP 请求")
	})
	res := CallTool(context.Background(), c, "instance_stop", map[string]any{})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "id")
}

func TestCallTool_UnknownTool(t *testing.T) {
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("不应发 HTTP 请求")
	})
	res := CallTool(context.Background(), c, "delete_instance", map[string]any{"id": float64(1)})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "未知工具")
}

func TestCallTool_ListInstancesNodeID(t *testing.T) {
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "2", r.URL.Query().Get("nodeId"))
		_, _ = w.Write([]byte(`[{"id":1}]`))
	})
	res := CallTool(context.Background(), c, "agent_list_instances", map[string]any{"nodeId": float64(2)})
	assert.False(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, `"id":1`)
}

func TestToUint(t *testing.T) {
	u, err := toUint(float64(42))
	require.NoError(t, err)
	assert.Equal(t, uint(42), u)
	u, err = toUint("9")
	require.NoError(t, err)
	assert.Equal(t, uint(9), u)
	_, err = toUint(-1.0)
	require.Error(t, err)
}
