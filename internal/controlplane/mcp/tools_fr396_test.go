package mcp

import (
	"context"
	"fmt"
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

func TestFR396_V1CannotSeeNewTools(t *testing.T) {
	p := &service.AgentPrincipal{
		PolicyVersion: service.AgentPolicyVersionV1,
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
