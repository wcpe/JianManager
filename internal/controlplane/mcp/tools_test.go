package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func TestRegisteredTools_Set(t *testing.T) {
	tools := RegisteredTools()
	names := make(map[string]bool, len(tools))
	for _, tl := range tools {
		names[tl.Name] = true
		assert.NotEmpty(t, tl.Description)
		assert.NotNil(t, tl.InputSchema)
	}
	want := []string{
		"agent_whoami",
		"agent_list_nodes",
		"agent_list_instances",
		"agent_get_instance",
		"agent_get_instance_metrics",
		"agent_get_instance_logs",
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
	// 硬拒绝面不得出现
	hardDenied := []string{
		"user_create", "delete_instance", "kill_instance",
		"db_browse", "self_update", "audit_delete",
	}
	for _, n := range hardDenied {
		assert.False(t, names[n], "硬拒绝工具不得注册: %s", n)
	}
}

func TestCallTool_Whoami(t *testing.T) {
	p := &service.AgentPrincipal{
		TokenID:           1,
		Name:              "ci",
		TokenPrefix:       "jmat_xx",
		ScopedInstanceIDs: []uint{1},
		WriteAllowlist:    []string{service.AgentWriteInstanceLife},
	}
	res := CallTool(context.Background(), ToolDeps{}, p, "agent_whoami", nil)
	assert.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Contains(t, res.Content[0].Text, "ci")
	assert.Contains(t, res.Content[0].Text, "jmat_xx")
}

func TestCallTool_UnknownIsError(t *testing.T) {
	p := &service.AgentPrincipal{TokenID: 1, Name: "x"}
	res := CallTool(context.Background(), ToolDeps{}, p, "user_create", nil)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "未知工具")
}

func TestCallTool_ScopeDenied(t *testing.T) {
	p := &service.AgentPrincipal{
		TokenID:           1,
		Name:              "x",
		ScopedInstanceIDs: []uint{1},
		WriteAllowlist:    []string{service.AgentWriteInstanceLife},
	}
	// id=99 不在 scope
	res := CallTool(context.Background(), ToolDeps{}, p, "instance_start", map[string]any{"id": float64(99)})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "拒绝")
}

func TestCallTool_MissingID(t *testing.T) {
	p := &service.AgentPrincipal{
		TokenID:           1,
		ScopedInstanceIDs: []uint{1},
		WriteAllowlist:    []string{service.AgentWriteInstanceLife},
	}
	res := CallTool(context.Background(), ToolDeps{}, p, "instance_stop", map[string]any{})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "id")
}

func TestCallTool_SessionClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &service.AgentPrincipal{TokenID: 1, Name: "x"}
	res := CallTool(ctx, ToolDeps{}, p, "agent_whoami", nil)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "会话已关闭")
}
