package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func setupAgentScopeDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AgentToken{}, &model.Instance{}, &model.Node{}))
	return db
}

func seedNodeAndInstances(t *testing.T, db *gorm.DB) (nodeA, nodeB *model.Node, instA1, instA2, instB1 *model.Instance) {
	t.Helper()
	nodeA = &model.Node{Name: "a", Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9101, Secret: "sa", Status: model.NodeStatusOnline}
	nodeB = &model.Node{Name: "b", Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9101, Secret: "sb", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(nodeA).Error)
	require.NoError(t, db.Create(nodeB).Error)
	instA1 = &model.Instance{NodeID: nodeA.ID, Name: "a1", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped}
	instA2 = &model.Instance{NodeID: nodeA.ID, Name: "a2", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped}
	instB1 = &model.Instance{NodeID: nodeB.ID, Name: "b1", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(instA1).Error)
	require.NoError(t, db.Create(instA2).Error)
	require.NoError(t, db.Create(instB1).Error)
	return
}

func TestAgentScope_ListAccessibleInstances_V2UnionAndMove(t *testing.T) {
	db := setupAgentScopeDB(t)
	svc := NewAgentTokenService(db)
	nodeA, nodeB, instA1, instA2, instB1 := seedNodeAndInstances(t, db)

	p := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{instB1.ID}, // 显式 b1
		ScopedNodeIDs:     []uint{nodeA.ID},  // 节点 A 上全部
		Capabilities:      []string{AgentCapabilityInstanceRead},
	}

	list, err := svc.ListAccessibleInstances(p, nil)
	require.NoError(t, err)
	ids := make([]uint, 0, len(list))
	for _, inst := range list {
		ids = append(ids, inst.ID)
	}
	assert.ElementsMatch(t, []uint{instA1.ID, instA2.ID, instB1.ID}, ids)

	// 可选 node 过滤
	nid := nodeA.ID
	list, err = svc.ListAccessibleInstances(p, &nid)
	require.NoError(t, err)
	require.Len(t, list, 2)

	// 把 instA1 移到 nodeB → 对仅有 nodeA scope 的 token 失权；显式 b1 仍在
	require.NoError(t, db.Model(instA1).Update("node_id", nodeB.ID).Error)
	list, err = svc.ListAccessibleInstances(p, nil)
	require.NoError(t, err)
	ids = ids[:0]
	for _, inst := range list {
		ids = append(ids, inst.ID)
	}
	assert.ElementsMatch(t, []uint{instA2.ID, instB1.ID}, ids)

	// 移回 nodeA 后恢复
	require.NoError(t, db.Model(instA1).Update("node_id", nodeA.ID).Error)
	list, err = svc.ListAccessibleInstances(p, nil)
	require.NoError(t, err)
	assert.Len(t, list, 3)

	// 新建到授权节点自动可见
	instA3 := &model.Instance{NodeID: nodeA.ID, Name: "a3", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(instA3).Error)
	list, err = svc.ListAccessibleInstances(p, nil)
	require.NoError(t, err)
	assert.Len(t, list, 4)
}

func TestAgentScope_ListAccessibleInstances_V1NoInheritance(t *testing.T) {
	db := setupAgentScopeDB(t)
	svc := NewAgentTokenService(db)
	nodeA, _, instA1, _, _ := seedNodeAndInstances(t, db)

	p := &AgentPrincipal{
		PolicyVersion: AgentPolicyVersionV1,
		ScopedNodeIDs: []uint{nodeA.ID},
		// 无显式实例
	}
	_, err := svc.ListAccessibleInstances(p, nil)
	assert.ErrorIs(t, err, ErrAgentForbidden)

	p.ScopedInstanceIDs = []uint{instA1.ID}
	list, err := svc.ListAccessibleInstances(p, nil)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, instA1.ID, list[0].ID)
}

func TestAgentScope_AuthorizeInstanceAction_ConvergesNotFound(t *testing.T) {
	db := setupAgentScopeDB(t)
	svc := NewAgentTokenService(db)
	_, _, instA1, _, _ := seedNodeAndInstances(t, db)

	p := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{instA1.ID},
		Capabilities:      []string{AgentCapabilityInstanceRead},
	}
	auth, inst, err := svc.AuthorizeInstanceAction(p, AgentActionGetInstance, instA1.ID)
	require.NoError(t, err)
	require.NotNil(t, inst)
	assert.Equal(t, AgentCapabilityInstanceRead, auth.Capability)

	_, _, err = svc.AuthorizeInstanceAction(p, AgentActionGetInstance, 99999)
	assert.ErrorIs(t, err, ErrAgentForbidden)

	// 实例 scope 不反向授权节点
	_, err = svc.AuthorizeNodeAction(p, AgentActionListNodes, instA1.NodeID)
	assert.ErrorIs(t, err, ErrAgentForbidden)
}
