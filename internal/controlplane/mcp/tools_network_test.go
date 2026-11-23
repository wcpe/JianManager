package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 群组服 Network 软标签与代理注册的 MCP 工具契约测试。
// 覆盖：工具注册完整性、capability 门控、以及「服务未装配 / 参数缺失 / 无权限」的降级语义。

// networkToolNames 是本域应注册的全部工具名。
var networkToolNames = []string{
	"network_list",
	"network_get",
	"network_create",
	"network_update",
	"network_delete",
	"network_add_members",
	"network_remove_member",
	"topology_get",
	"registration_list",
	"registration_create",
	"registration_delete",
}

func TestRegisteredTools_NetworkDomain(t *testing.T) {
	names := make(map[string]bool)
	for _, tl := range RegisteredTools() {
		names[tl.Name] = true
	}
	for _, n := range networkToolNames {
		assert.True(t, names[n], "应注册工具 %s", n)
	}
}

// toolNamesForPrincipal 收集给定 principal 可见的工具名。
func toolNamesForPrincipal(p *service.AgentPrincipal) map[string]bool {
	names := make(map[string]bool)
	for _, tl := range ToolsForPrincipal(p) {
		names[tl.Name] = true
	}
	return names
}

// TestNetworkTools_CapabilityGating 验证读写工具按 instance.read / instance.write 门控：
// 只持读能力的 token 不应看到写工具；持写能力的应两者都看到。
func TestNetworkTools_CapabilityGating(t *testing.T) {
	readOnly := &service.AgentPrincipal{
		PolicyVersion: service.AgentPolicyVersionV2,
		ScopedNodeIDs: []uint{1},
		Capabilities:  []string{service.AgentCapabilityInstanceRead},
	}
	names := toolNamesForPrincipal(readOnly)
	assert.True(t, names["network_list"], "instance.read 应看到 network_list")
	assert.True(t, names["topology_get"], "instance.read 应看到 topology_get")
	assert.True(t, names["registration_list"], "instance.read 应看到 registration_list")
	assert.False(t, names["network_create"], "instance.read 不应看到 network_create")
	assert.False(t, names["registration_create"], "instance.read 不应看到 registration_create")

	writer := &service.AgentPrincipal{
		PolicyVersion: service.AgentPolicyVersionV2,
		ScopedNodeIDs: []uint{1},
		Capabilities:  []string{service.AgentCapabilityInstanceRead, service.AgentCapabilityInstanceWrite},
	}
	names = toolNamesForPrincipal(writer)
	assert.True(t, names["network_create"], "instance.write 应看到 network_create")
	assert.True(t, names["network_add_members"], "instance.write 应看到 network_add_members")
	assert.True(t, names["registration_create"], "instance.write 应看到 registration_create")
	assert.True(t, names["registration_delete"], "instance.write 应看到 registration_delete")
}

// networkWriterPrincipal 是持有本域所需能力的 principal（节点 scope 内）。
func networkWriterPrincipal() *service.AgentPrincipal {
	return &service.AgentPrincipal{
		TokenID:       1,
		Name:          "network-writer",
		PolicyVersion: service.AgentPolicyVersionV2,
		ScopedNodeIDs: []uint{1},
		Capabilities: []string{
			service.AgentCapabilityInstanceRead, service.AgentCapabilityInstanceWrite,
		},
	}
}

// TestNetworkTools_ServiceUnavailable 验证依赖未装配时给出明确中文错误，
// 而不是 panic 或含糊的英文错误（与既有域工具一致的降级风格）。
func TestNetworkTools_ServiceUnavailable(t *testing.T) {
	p := networkWriterPrincipal()
	cases := []struct {
		name string
		exec func(context.Context, ToolDeps, *service.AgentPrincipal, string, map[string]any) ToolResult
		args map[string]any
		want string
	}{
		{"network_list", execNetworkList, map[string]any{}, "群组服务不可用"},
		{"network_get", execNetworkGet, map[string]any{"id": float64(1)}, "群组服务不可用"},
		{"network_create", execNetworkCreate, map[string]any{"name": "r1"}, "群组服务不可用"},
		{"topology_get", execTopologyGet, map[string]any{}, "拓扑服务不可用"},
		{"registration_list", execRegistrationList, map[string]any{"id": float64(1)}, "代理注册服务不可用"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.exec(context.Background(), ToolDeps{}, p, service.AgentActionNetworkList, tc.args)
			assert.True(t, res.IsError, "%s 应返回错误", tc.name)
			assert.Contains(t, res.Content[0].Text, tc.want)
		})
	}
}

// TestNetworkTools_ArgumentValidation 验证必填参数缺失时给出可操作的中文错误，
// 且校验先于服务调用（deps 为空也能捕获）。
func TestNetworkTools_ArgumentValidation(t *testing.T) {
	p := networkWriterPrincipal()
	deps := ToolDeps{}
	action := service.AgentActionNetworkList // 具体 action 不影响参数校验分支

	res := execNetworkCreate(context.Background(), deps, p, action, map[string]any{})
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "name")

	res = execNetworkAddMembers(context.Background(), deps, p, action, map[string]any{"id": float64(1)})
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "instanceIds")

	res = execRegistrationCreate(context.Background(), deps, p, action, map[string]any{"id": float64(1)})
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "backendId")

	// network_delete 必须有 confirmName（危险操作二次确认）。
	res = execNetworkDelete(context.Background(), deps, p, action, map[string]any{"id": float64(1)})
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "confirmName")
}

// TestNetworkTools_ForbiddenWithoutCapability 验证无相应能力时被拒（权限先于业务）。
func TestNetworkTools_ForbiddenWithoutCapability(t *testing.T) {
	noCaps := &service.AgentPrincipal{
		TokenID:       3,
		Name:          "bare",
		PolicyVersion: service.AgentPolicyVersionV2,
	}

	res := execNetworkList(context.Background(), ToolDeps{}, noCaps, service.AgentActionNetworkList, map[string]any{})
	assert.True(t, res.IsError, "无 instance.read 应被拒")

	res = execNetworkCreate(context.Background(), ToolDeps{}, noCaps, service.AgentActionNetworkCreate, map[string]any{"name": "r1"})
	assert.True(t, res.IsError, "无 instance.write 应被拒")
}
