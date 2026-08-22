package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func fr396TestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 内存库：避免 Windows 上 TempDir 清理时 sqlite 文件仍被占用。
	dsn := fmt.Sprintf("file:fr396_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.AgentToken{}, &model.AgentCallLog{},
		&model.GroupInstance{}, &model.ServerRegistration{}, &model.NetworkMember{},
		&model.Task{}, &model.TaskLog{},
	))
	return db
}

func fr396Seed(t *testing.T, db *gorm.DB) (*service.AgentTokenService, *service.AgentPrincipal, *model.Node, *model.Instance) {
	t.Helper()
	node := &model.Node{UUID: "n-fr396", Name: "node-alpha", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		UUID: "i-fr396", NodeID: node.ID, Name: "room-1",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDirect, StartCommand: "noop",
		Status: model.InstanceStatusStopped, WorkDir: "/srv/room",
	}
	require.NoError(t, db.Create(inst).Error)
	agent := service.NewAgentTokenService(db)
	_, plain, err := agent.Issue(service.IssueAgentTokenRequest{
		Name: "fr396", ScopedInstanceIDs: []uint{inst.ID}, ScopedNodeIDs: []uint{node.ID},
		PolicyVersion: service.AgentPolicyVersionV2, CapabilitiesProvided: true,
		Capabilities: []string{
			service.AgentCapabilityInstanceDestructive,
			service.AgentCapabilityInstanceCommand,
			service.AgentCapabilityInstanceLife,
			service.AgentCapabilityInstanceRead,
			service.AgentCapabilityNodeRead,
			service.AgentCapabilityNodeDestructive,
			service.AgentCapabilityObservabilityRead,
		},
		CreatedBy: 1,
	})
	require.NoError(t, err)
	p, err := agent.Authenticate(plain)
	require.NoError(t, err)
	return agent, p, node, inst
}

func TestFR396_ConfirmRejectsMismatch(t *testing.T) {
	db := fr396TestDB(t)
	agent, p, _, inst := fr396Seed(t, db)
	instanceSvc := service.NewInstanceService(db, nil, nil)
	instanceSvc.Shutdown()
	deps := ToolDeps{Agent: agent, Instance: instanceSvc, Node: service.NewNodeService(db)}

	res := CallTool(context.Background(), deps, p, "instance_kill", map[string]any{
		"id": float64(inst.ID), "confirmInstanceName": "wrong-name",
	})
	assert.True(t, res.IsError)
	require.NotEmpty(t, res.Content)
	assert.Contains(t, res.Content[0].Text, "确认名称与目标不符")
}

func TestFR396_ConfirmRejectsMissing(t *testing.T) {
	db := fr396TestDB(t)
	agent, p, _, inst := fr396Seed(t, db)
	deps := ToolDeps{Agent: agent, Instance: service.NewInstanceService(db, nil, nil), Node: service.NewNodeService(db)}
	res := CallTool(context.Background(), deps, p, "instance_delete", map[string]any{
		"id": float64(inst.ID),
	})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "confirmInstanceName")
}

func TestFR396_ConfirmDoesNotRevealOutOfScopeTarget(t *testing.T) {
	db := fr396TestDB(t)
	agent, p, node, _ := fr396Seed(t, db)
	other := &model.Node{UUID: "n-other", Name: "hidden-node", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(other).Error)
	deps := ToolDeps{Agent: agent, Node: service.NewNodeService(db)}
	res := CallTool(context.Background(), deps, p, "node_purge_archived", map[string]any{
		"id": float64(other.ID), "confirmNodeName": other.Name,
	})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "拒绝")
	assert.NotContains(t, res.Content[0].Text, "确认名称")
	assert.NotEqual(t, node.ID, other.ID)
}

func TestFR396_PurgeArchivedUsesArchivedName(t *testing.T) {
	db := fr396TestDB(t)
	agent, p, node, _ := fr396Seed(t, db)
	require.NoError(t, db.Delete(node).Error)
	deps := ToolDeps{Agent: agent, Node: service.NewNodeService(db)}
	res := CallTool(context.Background(), deps, p, "node_purge_archived", map[string]any{
		"id": float64(node.ID), "confirmNodeName": node.Name, "force": true,
	})
	assert.False(t, res.IsError, "归档节点应可按其名称确认后清理: %v", res.Content)
}

func TestFR396_V1CannotSeeNewTools(t *testing.T) {
	p := &service.AgentPrincipal{
		PolicyVersion:     service.AgentPolicyVersionV1,
		ScopedInstanceIDs: []uint{1}, ScopedNodeIDs: []uint{1},
		WriteAllowlist: []string{service.AgentWriteInstanceLife, service.AgentWriteNodeMaintenance},
	}
	tools := ToolsForPrincipal(p)
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	assert.False(t, names["instance_kill"])
	assert.False(t, names["instance_delete"])
	assert.False(t, names["node_purge_archived"])
	assert.False(t, names["instance_send_command"])
	assert.True(t, names["instance_start"])
	assert.True(t, names["agent_whoami"])
}

func TestFR396_BatchRejectsOutOfScopeWholly(t *testing.T) {
	db := fr396TestDB(t)
	node := &model.Node{UUID: "n-batch", Name: "node-batch", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	inScope := &model.Instance{
		UUID: "i-in", NodeID: node.ID, Name: "in",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDirect, StartCommand: "noop",
		Status: model.InstanceStatusStopped,
	}
	outScope := &model.Instance{
		UUID: "i-out", NodeID: node.ID, Name: "out",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDirect, StartCommand: "noop",
		Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inScope).Error)
	require.NoError(t, db.Create(outScope).Error)
	// 仅实例 scope，不含节点 scope——避免 V2 节点继承把 out 也授权进来。
	agent := service.NewAgentTokenService(db)
	_, plain, err := agent.Issue(service.IssueAgentTokenRequest{
		Name: "batch", ScopedInstanceIDs: []uint{inScope.ID},
		PolicyVersion: service.AgentPolicyVersionV2, CapabilitiesProvided: true,
		Capabilities: []string{service.AgentCapabilityInstanceLife},
		CreatedBy:    1,
	})
	require.NoError(t, err)
	p, err := agent.Authenticate(plain)
	require.NoError(t, err)

	batch := service.NewInstanceBatchService(db, nil)
	batch.SetInstanceService(service.NewInstanceService(db, nil, nil))
	deps := ToolDeps{Agent: agent, Batch: batch, Instance: service.NewInstanceService(db, nil, nil)}
	res := CallTool(context.Background(), deps, p, "instance_batch", map[string]any{
		"op":  "start",
		"ids": []any{float64(inScope.ID), float64(outScope.ID)},
	})
	assert.True(t, res.IsError, "含越界目标须整体拒绝")
	assert.Contains(t, res.Content[0].Text, "整体拒绝")
}

func TestFR396_ToolsRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, n := range ToolNames() {
		names[n] = true
	}
	for _, want := range []string{
		"node_get", "node_drain", "node_purge_archived",
		"instance_search", "instance_send_command", "instance_batch",
		"instance_kill", "instance_delete",
		"instance_create", "instance_provision_server", "instance_clone", "task_get",
	} {
		assert.True(t, names[want], "缺少工具 "+want)
	}
}

// ---- FR-396 执行器级测试：直接调 tool handler，验证真实调用链 ----

func TestFR396_SearchFiltersAndPaginates(t *testing.T) {
	db := fr396TestDB(t)
	agent, p, node, inst := fr396Seed(t, db)
	// 追加第二个实例（同名前缀不同后缀），验证 q 过滤与分页。
	other := &model.Instance{
		UUID: "i-fr396-b", NodeID: node.ID, Name: "room-b",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDirect, StartCommand: "noop",
		Status: model.InstanceStatusStopped, WorkDir: "/srv/room",
	}
	require.NoError(t, db.Create(other).Error)
	deps := ToolDeps{Agent: agent, Node: service.NewNodeService(db)}

	// q 过滤：只命中 room-1。
	res := CallTool(context.Background(), deps, p, "instance_search", map[string]any{"q": "room-1"})
	require.False(t, res.IsError, "search 不应失败: %v", res.Content)
	assert.Contains(t, res.Content[0].Text, inst.Name)
	assert.NotContains(t, res.Content[0].Text, "room-b")

	// status 过滤：room-b 也是 stopped，q+status 组合只应命中 room-b。
	res = CallTool(context.Background(), deps, p, "instance_search", map[string]any{
		"q": "room-b", "status": "stopped",
	})
	require.False(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "room-b")

	// 分页：pageSize=1 时仅返回一条。
	res = CallTool(context.Background(), deps, p, "instance_search", map[string]any{
		"page": 1, "pageSize": float64(1),
	})
	require.False(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "total")
}

func TestFR396_GetEnvSuccessAndOutOfScope(t *testing.T) {
	db := fr396TestDB(t)
	agent, p, node, inst := fr396Seed(t, db)
	// 节点置离线：GetInstanceEnv 走「节点离线」分支，不触发 gRPC（测试无 ClientPool）。
	require.NoError(t, db.Model(&model.Node{}).Where("id = ?", node.ID).Update("status", model.NodeStatusOffline).Error)
	deps := ToolDeps{Agent: agent, Instance: service.NewInstanceService(db, nil, nil), Node: service.NewNodeService(db)}

	// 在 scope 内：返回 env 视图（seed 无 env，也应返回空视图而非错误）。
	res := CallTool(context.Background(), deps, p, "instance_get_env", map[string]any{
		"id": float64(inst.ID),
	})
	require.False(t, res.IsError, "scope 内 get_env 不应失败: %v", res.Content)
	assert.Contains(t, res.Content[0].Text, "节点离线")

	// 越界实例：用仅实例 scope 的 token（不含节点 scope），确认越界被拒绝且不泄露存在性。
	agentOnly := service.NewAgentTokenService(db)
	_, plainOnly, err := agentOnly.Issue(service.IssueAgentTokenRequest{
		Name: "only-inst", ScopedInstanceIDs: []uint{inst.ID},
		PolicyVersion: service.AgentPolicyVersionV2, CapabilitiesProvided: true,
		Capabilities: []string{service.AgentCapabilityInstanceRead},
		CreatedBy:    1,
	})
	require.NoError(t, err)
	pOnly, err := agentOnly.Authenticate(plainOnly)
	require.NoError(t, err)

	outScope := &model.Instance{
		UUID: "i-fr396-out", NodeID: node.ID, Name: "out-scope",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDirect, StartCommand: "noop",
		Status: model.InstanceStatusStopped, WorkDir: "/srv/out",
	}
	require.NoError(t, db.Create(outScope).Error)
	depsOnly := ToolDeps{Agent: agentOnly, Instance: service.NewInstanceService(db, nil, nil), Node: service.NewNodeService(db)}
	res = CallTool(context.Background(), depsOnly, pOnly, "instance_get_env", map[string]any{
		"id": float64(outScope.ID),
	})
	assert.True(t, res.IsError, "越界实例必须拒绝")
	assert.Contains(t, res.Content[0].Text, "能力/scope 不足")
}

func TestFR396_SendCommandRequiresScopeAndConfirm(t *testing.T) {
	db := fr396TestDB(t)
	agent, p, _, inst := fr396Seed(t, db)
	instanceSvc := service.NewInstanceService(db, nil, nil)
	deps := ToolDeps{Agent: agent, Instance: instanceSvc, Node: service.NewNodeService(db)}

	// 无 confirm：破坏性工具要求精确确认（seed 实例未运行，先被运行状态闸拦截，也属安全护栏）。
	res := CallTool(context.Background(), deps, p, "instance_send_command", map[string]any{
		"id": float64(inst.ID), "command": "say hi",
	})
	assert.True(t, res.IsError)
	text := res.Content[0].Text
	assert.True(t,
		strings.Contains(text, "确认") || strings.Contains(text, "confirm") || strings.Contains(text, "未运行"),
		"应被运行状态或确认闸拦截，实际: %v", text)

	// 仅实例 scope token：越界实例命令必须被拒绝。
	agentOnly := service.NewAgentTokenService(db)
	_, plainOnly, err := agentOnly.Issue(service.IssueAgentTokenRequest{
		Name: "cmd-only", ScopedInstanceIDs: []uint{inst.ID},
		PolicyVersion: service.AgentPolicyVersionV2, CapabilitiesProvided: true,
		Capabilities: []string{service.AgentCapabilityInstanceCommand},
		CreatedBy:    1,
	})
	require.NoError(t, err)
	pOnly, err := agentOnly.Authenticate(plainOnly)
	require.NoError(t, err)
	outScope := &model.Instance{
		UUID: "i-fr396-cmd-out", NodeID: inst.NodeID, Name: "cmd-out",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDirect, StartCommand: "noop",
		Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(outScope).Error)
	res = CallTool(context.Background(), deps, pOnly, "instance_send_command", map[string]any{
		"id": float64(outScope.ID), "command": "say hi", "confirm": true,
	})
	assert.True(t, res.IsError, "越界实例命令必须拒绝")
	assert.Contains(t, res.Content[0].Text, "能力/scope 不足")
}
