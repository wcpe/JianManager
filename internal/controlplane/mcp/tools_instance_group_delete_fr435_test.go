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

// FR-435 回归：instance_group_delete 的确认目标既非实例也非节点——descriptor 复用 instance 资源类型
// 只为按节点/实例 scope 判定可发现性，其 id 指向分组自身。
//
// 修复前：callRegisteredTool 对带 ConfirmField 的工具先调 authorizeDestructiveTarget，后者按
// ResourceType=instance 把「分组 id」当「实例 id」填入 target.InstanceID，
// Authorize→principalCanAccessInstance(分组id, 0) 因实例 scope 不含该 id（且 NodeID=0 使节点
// scope 兜底失效）而恒为 false → 任何 token 都删不掉分组；精确确认也会误读实例服务。
// 修复后：这类「容器」目标按节点 scope 授权，确认名从分组服务解析。

func fr435TestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:fr435_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.InstanceGroupNode{}, &model.InstanceGroupMember{},
		&model.AgentToken{}, &model.AgentCallLog{},
	))
	return db
}

// fr435Principal 建一个 V2 token；nodeIDs/instIDs 决定 scope，instanceWrite 决定是否具备 instance.write。
func fr435Principal(t *testing.T, db *gorm.DB, name string, instanceWrite bool, nodeIDs, instIDs []uint) *service.AgentPrincipal {
	t.Helper()
	caps := []string{service.AgentCapabilityInstanceRead}
	if instanceWrite {
		caps = append(caps, service.AgentCapabilityInstanceWrite)
	}
	agent := service.NewAgentTokenService(db)
	_, plain, err := agent.Issue(service.IssueAgentTokenRequest{
		Name: name, ScopedInstanceIDs: instIDs, ScopedNodeIDs: nodeIDs,
		PolicyVersion: service.AgentPolicyVersionV2, CapabilitiesProvided: true,
		Capabilities: caps, CreatedBy: 1,
	})
	require.NoError(t, err)
	p, err := agent.Authenticate(plain)
	require.NoError(t, err)
	return p
}

// fr435Seed 建 1 节点 + 1 实例 + 1 空分组。注意三者在各自表中均为首行，id 天然相同（=1），
// 从而构造出「分组 id 与实例 id 相同」的碰撞场景——正是缺陷会误判的条件。
func fr435Seed(t *testing.T, db *gorm.DB) (*model.Node, *model.Instance, *model.InstanceGroupNode) {
	t.Helper()
	node := &model.Node{UUID: "n-fr435", Name: "node-fr435", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		UUID: "i-fr435", NodeID: node.ID, Name: "room-fr435",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDirect, StartCommand: "noop",
		Status: model.InstanceStatusStopped, WorkDir: "/srv/fr435",
	}
	require.NoError(t, db.Create(inst).Error)
	grp, err := service.NewInstanceGroupService(db).Create("fr435-group", nil)
	require.NoError(t, err)
	require.Equal(t, inst.ID, grp.ID, "前置：分组 id 与实例 id 相同（碰撞场景）")
	return node, inst, grp
}

// 节点 scope 的 V2 token（含 instance.write）应能删除空分组；且因分组 id 与实例 id 相同，
// 该用例同时证明确认名是从「分组」而非「实例」解析的（实例名为 room-fr435 ≠ 分组名 fr435-group）。
func TestFR435_GroupDeleteAllowsNodeScopedToken(t *testing.T) {
	db := fr435TestDB(t)
	node, _, grp := fr435Seed(t, db)
	grpSvc := service.NewInstanceGroupService(db)
	p := fr435Principal(t, db, "node-scoped", true, []uint{node.ID}, nil)
	deps := ToolDeps{Agent: service.NewAgentTokenService(db), InstanceGroup: grpSvc}

	res := CallTool(context.Background(), deps, p, "instance_group_delete", map[string]any{
		"id": float64(grp.ID), "confirmGroupName": grp.Name,
	})
	require.False(t, res.IsError, "节点 scope 应可删除分组: %v", res.Content)
	assert.Contains(t, res.Content[0].Text, "ok")
	if _, err := grpSvc.Get(grp.ID); err == nil {
		t.Fatal("分组应已删除")
	}
}

// 仅实例 scope（无节点 scope）的 V2 token 应被拒绝，且分组不得被动到。
func TestFR435_GroupDeleteRejectsTokenWithoutNodeScope(t *testing.T) {
	db := fr435TestDB(t)
	_, inst, grp := fr435Seed(t, db)
	grpSvc := service.NewInstanceGroupService(db)
	p := fr435Principal(t, db, "instance-only", true, nil, []uint{inst.ID})
	deps := ToolDeps{Agent: service.NewAgentTokenService(db), InstanceGroup: grpSvc}

	res := CallTool(context.Background(), deps, p, "instance_group_delete", map[string]any{
		"id": float64(grp.ID), "confirmGroupName": grp.Name,
	})
	assert.True(t, res.IsError, "无节点 scope 须拒绝")
	assert.Contains(t, res.Content[0].Text, "能力/scope 不足")
	_, err := grpSvc.Get(grp.ID)
	require.NoError(t, err, "被拒的分组不应被删除")
}

// 确认名不符应拒绝（不进入删除写路径）。
func TestFR435_GroupDeleteRejectsWrongConfirmName(t *testing.T) {
	db := fr435TestDB(t)
	node, _, grp := fr435Seed(t, db)
	grpSvc := service.NewInstanceGroupService(db)
	p := fr435Principal(t, db, "wrong-name", true, []uint{node.ID}, nil)
	deps := ToolDeps{Agent: service.NewAgentTokenService(db), InstanceGroup: grpSvc}

	res := CallTool(context.Background(), deps, p, "instance_group_delete", map[string]any{
		"id": float64(grp.ID), "confirmGroupName": "not-the-group",
	})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "确认名称与目标不符")
	_, err := grpSvc.Get(grp.ID)
	require.NoError(t, err, "确认失败不应删除分组")
}

// 无 instance.write 能力（仅 instance.read）不得删除分组。
func TestFR435_GroupDeleteRequiresWriteCapability(t *testing.T) {
	db := fr435TestDB(t)
	node, _, grp := fr435Seed(t, db)
	grpSvc := service.NewInstanceGroupService(db)
	p := fr435Principal(t, db, "read-only", false, []uint{node.ID}, nil)
	deps := ToolDeps{Agent: service.NewAgentTokenService(db), InstanceGroup: grpSvc}

	res := CallTool(context.Background(), deps, p, "instance_group_delete", map[string]any{
		"id": float64(grp.ID), "confirmGroupName": grp.Name,
	})
	assert.True(t, res.IsError, "缺 instance.write 须拒绝")
	assert.Contains(t, res.Content[0].Text, "能力/scope 不足")
}
