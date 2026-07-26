package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func setupAgentTokenDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AgentToken{}))
	return db
}

func TestAgentToken_IssueAuthRevoke(t *testing.T) {
	db := setupAgentTokenDB(t)
	svc := NewAgentTokenService(db)

	tok, plain, err := svc.Issue(IssueAgentTokenRequest{
		Name:              "ci",
		ScopedInstanceIDs: []uint{1, 2},
		ScopedNodeIDs:     []uint{10},
		CreatedBy:         1,
	})
	require.NoError(t, err)
	require.NotEmpty(t, plain)
	assert.Contains(t, plain, "jmat_")
	assert.NotEmpty(t, tok.TokenPrefix)

	p, err := svc.Authenticate(plain)
	require.NoError(t, err)
	assert.Equal(t, "ci", p.Name)
	assert.Equal(t, []uint{1, 2}, p.ScopedInstanceIDs)
	assert.Contains(t, p.WriteAllowlist, AgentWriteInstanceLife)

	// 错误明文
	_, err = svc.Authenticate("jmat_bogus")
	assert.ErrorIs(t, err, ErrAgentTokenInvalid)

	// 吊销后失效
	require.NoError(t, svc.Revoke(tok.ID))
	_, err = svc.Authenticate(plain)
	assert.ErrorIs(t, err, ErrAgentTokenInvalid)
}

func TestAgentToken_ResolveAction(t *testing.T) {
	p := &AgentPrincipal{
		TokenID:           1,
		Name:              "ops",
		ScopedInstanceIDs: []uint{5},
		ScopedNodeIDs:     []uint{7},
		WriteAllowlist:    []string{AgentWriteInstanceLife, AgentWriteNodeMaintenance},
	}

	// 读：scope 内
	assert.NoError(t, ResolveAction(p, AgentActionGetInstance, 5, 0))
	// 读：scope 外
	assert.ErrorIs(t, ResolveAction(p, AgentActionGetInstance, 99, 0), ErrAgentForbidden)
	// 写：白名单 + scope
	assert.NoError(t, ResolveAction(p, AgentActionInstanceStart, 5, 0))
	// 写：实例不在 scope
	assert.ErrorIs(t, ResolveAction(p, AgentActionInstanceStop, 99, 0), ErrAgentForbidden)
	// 硬拒绝 kill
	assert.ErrorIs(t, ResolveAction(p, AgentHardDenyInstanceKill, 5, 0), ErrAgentForbidden)
	// 用户写硬拒绝
	assert.ErrorIs(t, ResolveAction(p, AgentHardDenyUserWrite, 0, 0), ErrAgentForbidden)
	// 维护态
	assert.NoError(t, ResolveAction(p, AgentActionNodeMaintenanceEnter, 0, 7))
	assert.ErrorIs(t, ResolveAction(p, AgentActionNodeMaintenanceLeave, 0, 8), ErrAgentForbidden)

	// 无写白名单
	p2 := &AgentPrincipal{ScopedInstanceIDs: []uint{5}, WriteAllowlist: nil}
	assert.ErrorIs(t, ResolveAction(p2, AgentActionInstanceStart, 5, 0), ErrAgentForbidden)
}

func TestAgentToken_Expired(t *testing.T) {
	db := setupAgentTokenDB(t)
	svc := NewAgentTokenService(db)
	tok, plain, err := svc.Issue(IssueAgentTokenRequest{
		Name: "exp", ScopedInstanceIDs: []uint{1}, CreatedBy: 1, TTLDays: 1,
	})
	require.NoError(t, err)
	// 强制过期
	require.NoError(t, db.Model(tok).Update("expires_at", time.Now().Add(-time.Hour)).Error)
	_, err = svc.Authenticate(plain)
	assert.ErrorIs(t, err, ErrAgentTokenInvalid)
}

func TestAgentToken_List(t *testing.T) {
	db := setupAgentTokenDB(t)
	svc := NewAgentTokenService(db)
	_, _, err := svc.Issue(IssueAgentTokenRequest{Name: "a", ScopedInstanceIDs: []uint{1}, CreatedBy: 1})
	require.NoError(t, err)
	list, err := svc.List()
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.NotEmpty(t, list[0].TokenPrefix)
	assert.Equal(t, "a", list[0].Name)
	assert.Equal(t, AgentPolicyVersionV1, list[0].PolicyVersion)
}

func TestAgentToken_IssueV1RejectsCapabilities(t *testing.T) {
	db := setupAgentTokenDB(t)
	svc := NewAgentTokenService(db)
	_, _, err := svc.Issue(IssueAgentTokenRequest{
		Name: "v1-bad", CreatedBy: 1, PolicyVersion: AgentPolicyVersionV1,
		CapabilitiesProvided: true, Capabilities: []string{AgentCapabilityInstanceRead},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "capabilities")
}

func TestAgentToken_IssueV2RequiresCapabilitiesAndRejectsWriteAllowlist(t *testing.T) {
	db := setupAgentTokenDB(t)
	svc := NewAgentTokenService(db)

	_, _, err := svc.Issue(IssueAgentTokenRequest{
		Name: "v2-missing", CreatedBy: 1, PolicyVersion: AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "capabilities")

	_, _, err = svc.Issue(IssueAgentTokenRequest{
		Name: "v2-mix", CreatedBy: 1, PolicyVersion: AgentPolicyVersionV2,
		CapabilitiesProvided: true, Capabilities: []string{AgentCapabilityInstanceRead},
		WriteAllowlistProvided: true, WriteAllowlist: []string{AgentWriteInstanceLife},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writeAllowlist")

	_, _, err = svc.Issue(IssueAgentTokenRequest{
		Name: "v2-unknown", CreatedBy: 1, PolicyVersion: AgentPolicyVersionV2,
		CapabilitiesProvided: true, Capabilities: []string{"not.a.cap"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "未知 capability")

	tok, plain, err := svc.Issue(IssueAgentTokenRequest{
		Name: "v2-ok", CreatedBy: 1, PolicyVersion: AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1}, ScopedNodeIDs: []uint{2},
		CapabilitiesProvided: true,
		Capabilities:         []string{AgentCapabilityInstanceRead, AgentCapabilityNodeRead},
	})
	require.NoError(t, err)
	assert.Equal(t, AgentPolicyVersionV2, tok.PolicyVersion)
	p, err := svc.Authenticate(plain)
	require.NoError(t, err)
	assert.Equal(t, AgentPolicyVersionV2, p.PolicyVersion)
	assert.ElementsMatch(t, []string{AgentCapabilityInstanceRead, AgentCapabilityNodeRead}, p.Capabilities)
	assert.Empty(t, p.WriteAllowlist)
}

func TestAgentToken_IssueV2EmptyCapabilities(t *testing.T) {
	db := setupAgentTokenDB(t)
	svc := NewAgentTokenService(db)
	tok, plain, err := svc.Issue(IssueAgentTokenRequest{
		Name: "v2-empty", CreatedBy: 1, PolicyVersion: AgentPolicyVersionV2,
		CapabilitiesProvided: true, Capabilities: []string{},
	})
	require.NoError(t, err)
	assert.Equal(t, AgentPolicyVersionV2, tok.PolicyVersion)
	p, err := svc.Authenticate(plain)
	require.NoError(t, err)
	assert.Empty(t, p.Capabilities)
	assert.NoError(t, ResolveAction(p, AgentActionWhoami, 0, 0))
	assert.ErrorIs(t, ResolveAction(p, AgentActionListInstances, 0, 0), ErrAgentForbidden)
}

func TestAgentToken_V1NoNodeInheritance(t *testing.T) {
	p := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV1,
		ScopedNodeIDs:     []uint{7},
		ScopedInstanceIDs: nil,
		WriteAllowlist:    []string{AgentWriteInstanceLife},
	}
	// 节点上的实例 5 对 V1 不可见
	assert.ErrorIs(t, ResolveAction(p, AgentActionGetInstance, 5, 7), ErrAgentForbidden)
	assert.ErrorIs(t, ResolveAction(p, AgentActionInstanceStart, 5, 7), ErrAgentForbidden)
	assert.ErrorIs(t, ResolveAction(p, AgentActionListInstances, 0, 0), ErrAgentForbidden)
}

func TestAgentToken_V2NodeInheritanceAuthorize(t *testing.T) {
	p := &AgentPrincipal{
		PolicyVersion: AgentPolicyVersionV2,
		ScopedNodeIDs: []uint{7},
		Capabilities:  []string{AgentCapabilityInstanceRead, AgentCapabilityInstanceLife},
	}
	auth, err := Authorize(p, AgentActionGetInstance, AgentTrustedTarget{
		ResourceType: AgentResourceInstance, InstanceID: 5, NodeID: 7,
	})
	require.NoError(t, err)
	assert.Equal(t, AgentCapabilityInstanceRead, auth.Capability)

	_, err = Authorize(p, AgentActionGetInstance, AgentTrustedTarget{
		ResourceType: AgentResourceInstance, InstanceID: 5, NodeID: 8,
	})
	assert.ErrorIs(t, err, ErrAgentForbidden)

	// 无 instance.life 不可启动
	p2 := &AgentPrincipal{
		PolicyVersion: AgentPolicyVersionV2,
		ScopedNodeIDs: []uint{7},
		Capabilities:  []string{AgentCapabilityInstanceRead},
	}
	_, err = Authorize(p2, AgentActionInstanceStart, AgentTrustedTarget{
		ResourceType: AgentResourceInstance, InstanceID: 5, NodeID: 7,
	})
	assert.ErrorIs(t, err, ErrAgentForbidden)
}

func TestAgentCapability_CatalogAndDiscover(t *testing.T) {
	require.NotEmpty(t, AgentKnownCapabilities())
	require.True(t, IsAgentKnownCapability(AgentCapabilityInstanceLife))
	require.False(t, IsAgentKnownCapability("nope"))

	d, ok := DescribeAgentAction(AgentActionInstanceStart)
	require.True(t, ok)
	assert.Equal(t, AgentCapabilityInstanceLife, d.V2Capability)
	assert.Equal(t, AgentWriteInstanceLife, d.V1WriteAllow)

	contract := AgentOpsContract()
	require.GreaterOrEqual(t, len(contract), 10)
	for _, row := range contract {
		assert.NotEmpty(t, row.Method)
		assert.NotEmpty(t, row.PathTemplate)
		if row.Kind == "write" {
			assert.NotEmpty(t, row.WriteAllow)
			assert.NotEmpty(t, row.Capability)
		}
	}

	// 空能力 V2 只能 whoami
	p := &AgentPrincipal{PolicyVersion: AgentPolicyVersionV2, Capabilities: []string{}}
	_, err := CanDiscover(p, AgentActionWhoami)
	assert.NoError(t, err)
	_, err = CanDiscover(p, AgentActionListNodes)
	assert.ErrorIs(t, err, ErrAgentForbidden)

	// 有 node.read 但无节点 scope → 不可发现
	p2 := &AgentPrincipal{
		PolicyVersion: AgentPolicyVersionV2,
		Capabilities:  []string{AgentCapabilityNodeRead},
	}
	_, err = CanDiscover(p2, AgentActionListNodes)
	assert.ErrorIs(t, err, ErrAgentForbidden)

	p2.ScopedNodeIDs = []uint{1}
	auth, err := CanDiscover(p2, AgentActionListNodes)
	require.NoError(t, err)
	assert.Equal(t, AgentCapabilityNodeRead, auth.Capability)
}
