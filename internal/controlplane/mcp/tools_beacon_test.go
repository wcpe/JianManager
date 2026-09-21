package mcp

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-444 Beacon 拓扑拉取工具的 MCP 层测试。

func newBeaconToolDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Instance{},
		&model.InstanceGroupNode{},
		&model.InstanceGroupMember{},
		&model.AuditLog{},
	))
	return db
}

// beaconV2Principal 返回持 instance.write / instance.read 且有节点 scope 的 V2 principal。
// scope 必需：CanDiscover 会对资源的潜在可达范围做一次判定，无 scope 的 token 一律拒绝。
func beaconV2Principal() *service.AgentPrincipal {
	return &service.AgentPrincipal{
		PolicyVersion: service.AgentPolicyVersionV2,
		ScopedNodeIDs: []uint{1},
		Capabilities:  []string{service.AgentCapabilityInstanceWrite, service.AgentCapabilityInstanceRead},
	}
}

// beaconReadOnlyPrincipal 返回只持 instance.read 的 V2 principal。
func beaconReadOnlyPrincipal() *service.AgentPrincipal {
	return &service.AgentPrincipal{
		PolicyVersion: service.AgentPolicyVersionV2,
		ScopedNodeIDs: []uint{1},
		Capabilities:  []string{service.AgentCapabilityInstanceRead},
	}
}

// TestBeaconTools_RegisteredWithExpectedCapabilities 两个工具已注册且能力面符合约定：
// 拉取写走 instance.write、状态读走 instance.read（与实例分组同权限面）。
func TestBeaconTools_RegisteredWithExpectedCapabilities(t *testing.T) {
	byName := map[string]toolSpec{}
	for _, spec := range allToolSpecs {
		byName[spec.Def.Name] = spec
	}

	pull, ok := byName["beacon_topology_pull"]
	require.True(t, ok, "应注册 beacon_topology_pull")
	require.Equal(t, service.AgentActionBeaconTopologyPull, pull.Action)

	status, ok := byName["beacon_topology_status"]
	require.True(t, ok, "应注册 beacon_topology_status")
	require.Equal(t, service.AgentActionBeaconTopologyStatus, status.Action)

	// V1 Token 不得因工具扩容而扩权。
	for _, name := range []string{"beacon_topology_pull", "beacon_topology_status"} {
		d, ok := service.DescribeAgentAction(byName[name].Action)
		require.True(t, ok)
		require.False(t, d.V1Allowed, name+" 应 V1Allowed=false")
		require.False(t, d.HTTPInContract, name+" 不进 HTTP 契约投影")
	}
	pullDesc, _ := service.DescribeAgentAction(service.AgentActionBeaconTopologyPull)
	require.Equal(t, service.AgentCapabilityInstanceWrite, pullDesc.V2Capability)
	statusDesc, _ := service.DescribeAgentAction(service.AgentActionBeaconTopologyStatus)
	require.Equal(t, service.AgentCapabilityInstanceRead, statusDesc.V2Capability)
}

// TestBeaconTools_VisibleToWriteCapablePrincipal 持 instance.write 的 V2 Token 能看到两个工具。
func TestBeaconTools_VisibleToWriteCapablePrincipal(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range ToolsForPrincipal(beaconV2Principal()) {
		names[tool.Name] = true
	}
	require.True(t, names["beacon_topology_pull"], "持 instance.write 应看到拉取工具")
	require.True(t, names["beacon_topology_status"], "持 instance.write 应看到状态工具")
}

// TestBeaconTools_HiddenFromReadOnlyPrincipal 只读能力看不到拉取工具（写工具按能力裁剪）。
func TestBeaconTools_HiddenFromReadOnlyPrincipal(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range ToolsForPrincipal(beaconReadOnlyPrincipal()) {
		names[tool.Name] = true
	}
	require.False(t, names["beacon_topology_pull"], "只读能力不应看到拉取工具")
	require.True(t, names["beacon_topology_status"], "只读能力应看到状态工具")
}

// TestBeaconTools_StatusWithoutService 未装配服务时状态工具返回未配置（不报错、不 panic）。
func TestBeaconTools_StatusWithoutService(t *testing.T) {
	res := execBeaconTopologyStatus(context.Background(), ToolDeps{}, beaconV2Principal(),
		service.AgentActionBeaconTopologyStatus, nil)
	require.False(t, res.IsError, "未装配服务时状态查询应为可读结果，而非错误")
	require.Contains(t, res.Content[0].Text, `"configured":false`)
}

// TestBeaconTools_PullDisabledReturnsChineseHint 未配置协同时拉取返回中文「未启用」提示
// （明确失败，不静默成功）——可选协同，绝非依赖。
func TestBeaconTools_PullDisabledReturnsChineseHint(t *testing.T) {
	db := newBeaconToolDB(t)
	// endpoint 为空 → NewBeaconClient 返回 nil → 服务不可用。
	svc := service.NewBeaconSyncService(db, service.NewBeaconClient(service.BeaconClientConfig{}), service.BeaconSyncConfig{})
	deps := ToolDeps{BeaconSync: svc}

	res := execBeaconTopologyPull(context.Background(), deps, beaconV2Principal(),
		service.AgentActionBeaconTopologyPull, nil)
	require.True(t, res.IsError, "未启用拉取应返回错误而非静默成功")
	require.Contains(t, res.Content[0].Text, "未启用")

	// nil 服务同样给出中文提示（不 panic）。
	res = execBeaconTopologyPull(context.Background(), ToolDeps{}, beaconV2Principal(),
		service.AgentActionBeaconTopologyPull, nil)
	require.True(t, res.IsError)
	require.Contains(t, res.Content[0].Text, "未启用")
}

// TestBeaconTools_PullUnreachableReportsError Beacon 不可达时工具返回错误且本地零改动。
func TestBeaconTools_PullUnreachableReportsError(t *testing.T) {
	db := newBeaconToolDB(t)
	client := service.NewBeaconClient(service.BeaconClientConfig{
		Endpoint: "http://127.0.0.1:1", PullEnabled: true,
	})
	require.NotNil(t, client)
	svc := service.NewBeaconSyncService(db, client, service.BeaconSyncConfig{})
	deps := ToolDeps{BeaconSync: svc}

	res := execBeaconTopologyPull(context.Background(), deps, beaconV2Principal(),
		service.AgentActionBeaconTopologyPull, nil)
	require.True(t, res.IsError)
	require.Contains(t, res.Content[0].Text, "Beacon 拓扑拉取失败")

	var groups int64
	require.NoError(t, db.Model(&model.InstanceGroupNode{}).Count(&groups).Error)
	require.Zero(t, groups, "不可达时本地零改动")
}
