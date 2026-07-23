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
}
